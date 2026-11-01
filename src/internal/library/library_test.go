package library

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

func testStore(t *testing.T) *state.Store {
	t.Helper()
	s, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Initialize("viewer", "test-only-password"); err != nil {
		t.Fatal(err)
	}
	return s
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
	m, err := New(testStore(t), []string{root}, filepath.Join(t.TempDir(), "catalog.json"), func(*media.Catalog) {})
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
		{media.Library{ID: "x", Type: "music"}, filepath.Join(root, "shows"), ErrType},
	} {
		got, err := m.Folder(existing, tc.library, tc.path)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.path, err, tc.want)
		}
		if err == nil && got != filepath.Clean(tc.path) {
			t.Errorf("%s: cleaned to %s", tc.path, got)
		}
	}
	none, err := New(testStore(t), nil, "", func(*media.Catalog) {})
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
	store := testStore(t)
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
	// A restart loads the index, which survives the stored libraries.
	again, err := New(store, nil, index, func(*media.Catalog) {})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if loaded, err := again.Open(context.Background(), dir); err != nil || !loaded {
		t.Fatalf("reopen: loaded %v, %v", loaded, err)
	}
}
