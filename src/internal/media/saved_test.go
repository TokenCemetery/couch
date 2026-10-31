package media

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSavedCatalog(t *testing.T) {
	dir, data := t.TempDir(), t.TempDir()
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
	file := filepath.Join(data, "catalog.json")
	if _, err := load(dir, file, probe); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing index: %v", err)
	}
	if err := scanned.Save(file); err != nil {
		t.Fatal(err)
	}

	loaded, err := load(dir, file, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loaded.Close() }()
	if loaded.ID != scanned.ID || !reflect.DeepEqual(loaded.Items, scanned.Items) || !reflect.DeepEqual(loaded.Folders, scanned.Folders) {
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
	rescanned, err := loaded.Rescan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if probes != 0 || !reflect.DeepEqual(rescanned.Items, scanned.Items) {
		t.Fatalf("rescan of the loaded catalog probed %d files or changed items", probes)
	}
	if err := os.WriteFile(filepath.Join(dir, "New.mp4"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	rescanned, err = loaded.Rescan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := rescanned.Save(file); err != nil {
		t.Fatal(err)
	}
	again, err := load(dir, file, probe)
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

	saved, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"corrupted":       string(saved[:len(saved)/2]),
		"other version":   strings.Replace(string(saved), `"Version":1`, `"Version":99`, 1),
		"path outside":    strings.Replace(string(saved), `"Path":"Movie.mkv"`, `"Path":"../Movie.mkv"`, 1),
		"duplicate path":  strings.Replace(string(saved), `"Path":"New.mp4"`, `"Path":"Movie.mkv"`, 1),
		"no videos":       `{"Version":1,"ID":"` + scanned.ID + `","Items":[]}`,
		"empty file":      "",
		"other directory": strings.Replace(string(saved), scanned.ID, stableID("movies\x00/elsewhere"), 1),
	} {
		bad := filepath.Join(data, "bad.json")
		if err := os.WriteFile(bad, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if catalog, err := load(dir, bad, probe); err == nil || errors.Is(err, fs.ErrNotExist) {
			_ = catalog.Close()
			t.Errorf("%s: loaded, err %v", name, err)
		}
	}
	if _, err := load(filepath.Join(dir, "missing"), file, probe); err == nil {
		t.Fatal("index loaded for another directory")
	}
	if entries, _ := os.ReadDir(data); len(entries) != 2 {
		t.Fatalf("temporary files left: %v", entries)
	}
}
