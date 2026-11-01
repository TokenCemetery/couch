package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotation(t *testing.T) {
	maxBytes, keep = 100, 2
	defer func() { maxBytes, keep = 10<<20, 4 }()
	dir := filepath.Join(t.TempDir(), "logs")
	w, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n"
	for range 10 {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	files, err := Files(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int64{}
	for _, f := range files {
		names[f.Name] = f.Size
	}
	if len(files) != 3 || names["couch.log"] == 0 || names["couch-1.log"] != 80 || names["couch-2.log"] != 80 {
		t.Fatalf("files %+v", files)
	}
	// A reopened writer appends to the current file.
	w, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("more\n")); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if info, err := os.Stat(filepath.Join(dir, "couch.log")); err != nil || info.Size() != names["couch.log"]+5 {
		t.Fatalf("current file after reopen: %v", err)
	}
	for _, name := range []string{"../state.lock", "couch-.log", "couch-1a.log", "other.log", "couch.log/"} {
		if f, err := OpenFile(dir, name); err == nil {
			_ = f.Close()
			t.Errorf("%s opened", name)
		}
	}
	if f, err := OpenFile(dir, "couch-2.log"); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}
}
