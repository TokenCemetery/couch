package media

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TokenCemetery/couch/internal/state"
)

func TestSavedCatalog(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"Movie.mkv":                     "movie",
		"Movie.en.srt":                  "1\n00:00:01,000 --> 00:00:02,000\nHi\n",
		"Show/Season 1/Show.S01E01.mp4": "episode",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	probes := 0
	probe := func(ctx context.Context, f *os.File) (Item, error) {
		probes++
		return Item{RunTimeTicks: 10000000, Streams: []Stream{{Index: 0, Type: "Video", Codec: "h264", Width: 64, Level: 3.1}}}, nil
	}
	scanned, err := scan(context.Background(), dir, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scanned.Close() }()
	if _, err := load(dir, state.CatalogIndex{}, probe); err == nil {
		t.Fatal("an empty index loaded")
	}
	saved, err := scanned.Index()
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := load(dir, saved, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loaded.Close() }()
	if loaded.ID != scanned.ID || !reflect.DeepEqual(plain(loaded.Items), plain(scanned.Items)) || !reflect.DeepEqual(plain(loaded.Folders), plain(scanned.Folders)) {
		t.Fatalf("loaded catalog differs:\n%+v\n%+v", loaded.Items, scanned.Items)
	}
	if f, err := loaded.Open(loaded.Items[0]); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	// A rescan after loading probes nothing unchanged and keeps dates added.
	probes = 0
	start := time.Now().UTC()
	rescanned, err := loaded.Rescan(context.Background(), loaded.Libraries())
	if err != nil {
		t.Fatal(err)
	}
	if probes != 0 || !reflect.DeepEqual(plain(rescanned.Items), plain(scanned.Items)) {
		t.Fatalf("rescan of the loaded catalog probed %d files or changed items", probes)
	}
	if err := os.WriteFile(filepath.Join(dir, "New.mp4"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	rescanned, err = loaded.Rescan(context.Background(), loaded.Libraries())
	if err != nil {
		t.Fatal(err)
	}
	if saved, err = rescanned.Index(); err != nil {
		t.Fatal(err)
	}
	again, err := load(dir, saved, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	for _, item := range again.Items {
		if item.Name == "New" && item.Added.Before(start) {
			t.Fatalf("first-seen date lost: %v", item.Added)
		}
	}
	if len(again.Items) != 3 {
		t.Fatalf("items %d, want 3", len(again.Items))
	}

	changed := func(change func(e *state.CatalogEntry)) state.CatalogIndex {
		out := state.CatalogIndex{Folders: saved.Folders, Entries: slices.Clone(saved.Entries)}
		change(&out.Entries[0])
		return out
	}
	for name, index := range map[string]state.CatalogIndex{
		"path outside":    changed(func(e *state.CatalogEntry) { e.Path = "../" + e.Path }),
		"duplicate path":  {Folders: saved.Folders, Entries: append(slices.Clone(saved.Entries), saved.Entries[0])},
		"other directory": {Folders: []string{"/elsewhere"}, Entries: saved.Entries},
	} {
		if catalog, err := load(dir, index, probe); err == nil {
			_ = catalog.Close()
			t.Errorf("%s: loaded", name)
		}
	}
	// A file whose probe cannot be read keeps its date added and is probed
	// again by the next rescan; the others are served as saved.
	unreadable, err := load(dir, changed(func(e *state.CatalogEntry) { e.Probe = "{" }), probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unreadable.Close() }()
	bad, ok := unreadable.Lookup(again.Items[0].ID)
	if !ok || !bad.reprobe || len(bad.Streams) != 0 || !bad.Added.Equal(again.Items[0].Added) || len(unreadable.Items) != len(again.Items) {
		t.Fatalf("unreadable probe loaded as %+v", bad)
	}
	probes = 0
	if _, err := unreadable.Rescan(context.Background(), unreadable.Libraries()); err != nil || probes != 1 {
		t.Fatalf("rescan after an unreadable probe probed %d files, %v", probes, err)
	}
	// A folder without files is kept, so files found in it later are dated
	// by the rescan that finds them.
	empty := filepath.Join(dir, "Empty")
	if err := os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	libraries := append(again.Libraries(), Library{ID: "home", Name: "Home", Type: HomeVideos, Paths: []string{empty}})
	withEmpty, err := again.Rescan(context.Background(), libraries)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = withEmpty.Close() }()
	emptyIndex, err := withEmpty.Index()
	if err != nil || !slices.Contains(emptyIndex.Folders, empty) {
		t.Fatalf("empty folder not saved: %v %+v", err, emptyIndex.Folders)
	}
	reloaded, err := loadLibraries(libraries, emptyIndex, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reloaded.Close() }()
	if err := os.WriteFile(filepath.Join(empty, "Later.mp4"), []byte("later"), 0600); err != nil {
		t.Fatal(err)
	}
	found := time.Now().UTC()
	rescanned, err = reloaded.Rescan(context.Background(), libraries)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range rescanned.Items {
		if item.Name == "Later" && item.Added.Before(found) {
			t.Fatalf("a file found in a saved empty folder is dated %v, before the rescan", item.Added)
		}
	}
	if _, err := load(filepath.Join(dir, "missing"), saved, probe); err == nil {
		t.Fatal("index loaded for another directory")
	}
}

// TestIndexFile checks the import of catalog.json, the index of releases
// before it was kept in the database.
func TestIndexFile(t *testing.T) {
	dir, data := t.TempDir(), t.TempDir()
	for _, name := range []string{"Movie.mkv", "Show/Season 1/Show.S01E01.mp4"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	probes := 0
	probe := func(ctx context.Context, f *os.File) (Item, error) {
		probes++
		return Item{RunTimeTicks: 10000000, Streams: []Stream{{Index: 0, Type: "Video", Codec: "h264", Width: 64, Level: 3.1}}}, nil
	}
	scanned, err := scan(context.Background(), dir, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = scanned.Close() }()
	libraries := scanned.Libraries()
	index, err := scanned.Index()
	if err != nil {
		t.Fatal(err)
	}
	items := make([]savedItem, len(index.Entries))
	for n, e := range index.Entries {
		var p savedProbe
		if err := json.Unmarshal([]byte(e.Probe), &p); err != nil {
			t.Fatal(err)
		}
		items[n] = savedItem{Path: e.Path, Size: e.Size, Modified: e.Modified, Added: e.Added, RunTimeTicks: p.RunTimeTicks, Streams: p.Streams}
	}
	write := func(name string, content any) string {
		t.Helper()
		b, ok := content.([]byte)
		if !ok {
			var err error
			if b, err = json.Marshal(content); err != nil {
				t.Fatal(err)
			}
		}
		file := filepath.Join(data, name)
		if err := os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
		return file
	}

	if _, err := ReadIndexFile(libraries, filepath.Join(data, "missing.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
	current := write("v6.json", savedCatalog{Version: savedVersion, Folders: []savedFolder{{Path: dir, Items: items}}})
	got, err := ReadIndexFile(libraries, current)
	if err != nil || !reflect.DeepEqual(got, index) {
		t.Fatalf("version 6: %v\n%+v\n%+v", err, got, index)
	}
	// A version 1 index, of the one directory, is still read; it predates
	// colour properties too.
	got, err = ReadIndexFile(libraries, write("v1.json", savedCatalog{Version: 1, ID: stableID("movies\x00" + dir), Items: items}))
	if err != nil {
		t.Fatal(err)
	}
	for n := range got.Entries {
		if !got.Entries[n].Reprobe {
			t.Fatalf("version 1: %s not marked", got.Entries[n].Path)
		}
		got.Entries[n].Reprobe = false
	}
	if !reflect.DeepEqual(got, index) {
		t.Fatalf("version 1: %+v", got)
	}
	// A version 5 index, from before colour properties, is served as it is,
	// and its videos keep a mark until a rescan probes them again (#125).
	got, err = ReadIndexFile(libraries, write("v5.json", savedCatalog{Version: 5, Folders: []savedFolder{{Path: dir, Items: items}}}))
	if err != nil {
		t.Fatal(err)
	}
	fromV5, err := load(dir, got, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fromV5.Close() }()
	if !reflect.DeepEqual(plain(fromV5.Items), plain(scanned.Items)) {
		t.Fatal("a version 5 index loads differently")
	}
	if saved, err := fromV5.Index(); err != nil || !saved.Entries[0].Reprobe {
		t.Fatalf("the reprobe mark is not saved: %v", err)
	}
	probes = 0
	reprobed, err := fromV5.Rescan(context.Background(), fromV5.Libraries())
	if err != nil {
		t.Fatal(err)
	}
	if probes != len(fromV5.Items) || !reflect.DeepEqual(plain(reprobed.Items), plain(scanned.Items)) {
		t.Fatalf("rescan of a version 5 index probed %d of %d files", probes, len(fromV5.Items))
	}
	for _, item := range reprobed.Items {
		if item.reprobe {
			t.Fatalf("%s still marked after the rescan", item.Path)
		}
	}

	raw, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"corrupted":     string(raw[:len(raw)/2]),
		"other version": strings.Replace(string(raw), `"Version":6`, `"Version":99`, 1),
		"version 3":     strings.Replace(string(raw), `"Version":6`, `"Version":3`, 1),
		"empty file":    "",
		"folder twice":  `{"Version":6,"Folders":[{"Path":"` + dir + `","Items":[]},{"Path":"` + dir + `","Items":[]}]}`,
	} {
		if _, err := ReadIndexFile(libraries, write("bad.json", []byte(content))); err == nil {
			t.Errorf("%s: read", name)
		}
	}
	// A version 1 index of another directory holds nothing for these
	// libraries.
	if got, err := ReadIndexFile(libraries, write("other.json", savedCatalog{Version: 1, ID: stableID("movies\x00/elsewhere")})); err != nil || len(got.Entries) != 0 {
		t.Fatalf("version 1 of another directory: %v %+v", err, got)
	}
}
