package library

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

func testStore(t *testing.T) (*state.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Initialize("viewer", "test-only-password"); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestFolder(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	for _, dir := range []string{"films", "films/old", "shows"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file.mp4"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	store, _ := testStore(t)
	m, err := New(store, []string{root}, filepath.Join(t.TempDir(), "catalog.json"), func(*media.Catalog) {})
	if err != nil {
		t.Fatal(err)
	}
	existing := []media.Library{{ID: "m", Type: media.Movies, Paths: []string{filepath.Join(root, "films")}}}
	movies := media.Library{ID: "n", Type: media.Movies}
	tv := media.Library{ID: "t", Type: media.TVShows}
	for _, tc := range []struct {
		library media.Library
		path    string
		want    error
	}{
		{tv, filepath.Join(root, "films"), nil},
		{movies, filepath.Join(root, "shows") + "/", nil},
		{movies, filepath.Join(root, "films", "old"), ErrOverlap},
		{movies, root, ErrOverlap},
		{movies, outside, ErrOutsideRoot},
		{movies, filepath.Join(root, "escape"), ErrOutsideRoot},
		{movies, root + "/shows/../films", ErrPath},
		{movies, "shows", ErrPath},
		{movies, filepath.Join(root, "missing"), ErrNotFolder},
		{movies, filepath.Join(root, "file.mp4"), ErrNotFolder},
		{media.Library{ID: "x", Type: "photos"}, filepath.Join(root, "shows"), ErrType},
	} {
		got, err := m.Folder(existing, tc.library, tc.path)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.path, err, tc.want)
		}
		if err == nil && got != filepath.Clean(tc.path) {
			t.Errorf("%s: cleaned to %s", tc.path, got)
		}
	}
	empty, _ := testStore(t)
	none, err := New(empty, nil, "", func(*media.Catalog) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := none.Folder(nil, movies, root); !errors.Is(err, ErrNoRoots) {
		t.Fatalf("without roots: %v", err)
	}
}

func TestManager(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required")
		}
	}
	dir := t.TempDir()
	movie := filepath.Join(dir, "First.mp4")
	build := exec.CommandContext(context.Background(), "ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=black:s=64x64:d=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", movie) //nolint:gosec // fixed arguments and temp paths
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build fixture: %v %s", err, out)
	}
	store, storeDir := testStore(t)
	index := filepath.Join(t.TempDir(), "catalog.json")
	published := make(chan *media.Catalog, 4)
	m, err := New(store, nil, index, func(c *media.Catalog) { published <- c })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// The first start creates the -media-dir libraries and scans them.
	loaded, err := m.Open(context.Background(), dir)
	if err != nil || loaded {
		t.Fatalf("open: loaded %v, %v", loaded, err)
	}
	defaults, _ := media.DefaultLibraries(dir)
	if d := store.Snapshot(); !d.LibrariesConfigured || len(d.Libraries) != 2 || d.Libraries[0].ID != defaults[0].ID {
		t.Fatalf("libraries %+v", d.Libraries)
	}
	if first := <-published; len(first.Items) != 1 {
		t.Fatalf("first scan: %d videos", len(first.Items))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hangup := make(chan os.Signal, 1)
	done := make(chan struct{})
	go func() {
		m.Run(ctx, hangup, 0, false)
		close(done)
	}()
	next := func(what string) *media.Catalog {
		t.Helper()
		select {
		case c := <-published:
			return c
		case <-time.After(30 * time.Second):
			t.Fatalf("no catalog published after %s", what)
			return nil
		}
	}
	data, err := os.ReadFile(movie)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Second.mp4"), data, 0600); err != nil { //nolint:gosec // test temp directory
		t.Fatal(err)
	}
	hangup <- syscall.SIGHUP
	if c := next("SIGHUP"); len(c.Items) != 2 {
		t.Fatalf("published %d videos, want 2", len(c.Items))
	}
	// An emptied directory keeps its videos and the scan reports it.
	for _, name := range []string{"First.mp4", "Second.mp4"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	m.Scan()
	if c := next("a request"); len(c.Items) != 2 {
		t.Fatalf("an empty folder published %d videos", len(c.Items))
	}
	if running, last := m.Status(); running || last == nil || last.Err == "" {
		t.Fatalf("status %v %+v", running, last)
	}
	// Removing a library publishes at once.
	if err := m.SetLibraries(m.Libraries()[1:]); err != nil {
		t.Fatal(err)
	}
	if c := next("removing a library"); len(c.Libraries()) != 1 || len(c.Items) != 0 {
		t.Fatalf("after removal: %d libraries, %d videos", len(c.Libraries()), len(c.Items))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not stop")
	}
	// A restart loads the index from the database, which survives the
	// stored libraries.
	saved, err := store.Catalog(context.Background())
	if err != nil || len(saved.Entries) != 2 {
		t.Fatalf("saved index %+v, %v", saved, err)
	}
	again, err := New(store, nil, index, func(*media.Catalog) {})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if loaded, err := again.Open(context.Background(), dir); err != nil || !loaded {
		t.Fatalf("reopen: loaded %v, %v", loaded, err)
	}

	// Second.mp4 was found by a rescan, so its date added is that rescan's,
	// not its mtime, and a backup keeps it.
	var second state.CatalogEntry
	for _, e := range saved.Entries {
		if e.Path == "Second.mp4" {
			second = e
		}
	}
	if !second.Added.After(second.Modified) {
		t.Fatalf("Second.mp4 added %v, modified %v", second.Added, second.Modified)
	}
	backup := filepath.Join(t.TempDir(), "backup")
	if err := state.Backup(storeDir, backup); err != nil {
		t.Fatal(err)
	}
	restored, err := state.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	fromBackup, err := New(restored, nil, filepath.Join(t.TempDir(), "catalog.json"), func(*media.Catalog) {})
	if err != nil {
		t.Fatal(err)
	}
	defer fromBackup.Close()
	if loaded, err := fromBackup.Open(context.Background(), dir); err != nil || !loaded {
		t.Fatalf("open the backup: loaded %v, %v", loaded, err)
	}
	if got, err := restored.Catalog(context.Background()); err != nil || !slices.ContainsFunc(got.Entries, func(e state.CatalogEntry) bool {
		return e.Path == "Second.mp4" && e.Added.Equal(second.Added)
	}) {
		t.Fatalf("the backup lost the date added: %+v, %v", got, err)
	}

	// A folder that cannot be opened at the start keeps its entries while
	// it stays in a library.
	gone := dir + ".gone"
	if err := os.Rename(dir, gone); err != nil {
		t.Fatal(err)
	}
	unreachable, err := New(store, nil, index, func(*media.Catalog) {})
	if err != nil {
		t.Fatal(err)
	}
	defer unreachable.Close()
	_, _ = unreachable.Open(context.Background(), dir)
	if err := os.Rename(gone, dir); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Catalog(context.Background()); err != nil || len(got.Entries) != 2 {
		t.Fatalf("an unreachable folder lost its entries: %+v, %v", got, err)
	}

	// A catalog.json of an earlier release is imported when the database
	// holds no index, keeping its dates added, and left for a rollback.
	added := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	items := []map[string]any{}
	for _, e := range saved.Entries {
		var item map[string]any
		if err := json.Unmarshal([]byte(e.Probe), &item); err != nil {
			t.Fatal(err)
		}
		item["Path"], item["Size"], item["Modified"], item["Added"] = e.Path, e.Size, e.Modified, added
		items = append(items, item)
	}
	legacy, err := json.Marshal(map[string]any{"Version": 6, "Folders": []any{map[string]any{"Path": dir, "Items": items}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangeCatalog(context.Background(), state.CatalogChange{Drop: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	imported, err := New(store, nil, index, func(*media.Catalog) {})
	if err != nil {
		t.Fatal(err)
	}
	defer imported.Close()
	if loaded, err := imported.Open(context.Background(), dir); err != nil || !loaded {
		t.Fatalf("import: loaded %v, %v", loaded, err)
	}
	if saved, err := store.Catalog(context.Background()); err != nil || len(saved.Entries) != 2 || !saved.Entries[0].Added.Equal(added) || !saved.Entries[1].Added.Equal(added) {
		t.Fatalf("imported index %+v, %v", saved, err)
	}
	if _, err := os.Stat(index); err != nil {
		t.Fatalf("catalog.json not left for a rollback: %v", err)
	}
}
