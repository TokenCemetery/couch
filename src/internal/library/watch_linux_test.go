package library

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatch(t *testing.T) {
	dir, later := t.TempDir(), filepath.Join(t.TempDir(), "later")
	// A library folder may be a symbolic link, which the scan follows.
	target, link := t.TempDir(), filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	changed := make(chan struct{}, 64)
	done := make(chan error, 1)
	go func() {
		done <- watch(ctx, []string{dir, later, link}, 50*time.Millisecond, func() { changed <- struct{}{} })
	}()
	expect := func(what string) {
		t.Helper()
		select {
		case <-changed:
		case <-time.After(5 * time.Second):
			t.Fatalf("no change reported for %s", what)
		}
		// One change can arrive as several events.
		time.Sleep(50 * time.Millisecond)
		for len(changed) > 0 {
			<-changed
		}
	}
	quiet := func(what string) {
		t.Helper()
		time.Sleep(200 * time.Millisecond)
		if len(changed) > 0 {
			t.Fatalf("a change reported for %s", what)
		}
	}
	// Watching starts before the first change is made.
	time.Sleep(100 * time.Millisecond)
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "a.mp4"))
	expect("a new file")
	if _, err := os.ReadFile(filepath.Join(dir, "a.mp4")); err != nil {
		t.Fatal(err)
	}
	quiet("reading a file")

	// A folder created later is watched too.
	sub := filepath.Join(dir, "Show", "Season 01")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	expect("a new folder")
	write(filepath.Join(sub, "Show.S01E01.mp4"))
	expect("a file in a new folder")
	if err := os.Rename(filepath.Join(sub, "Show.S01E01.mp4"), filepath.Join(dir, "moved.mp4")); err != nil {
		t.Fatal(err)
	}
	expect("a moved file")
	if err := os.Remove(filepath.Join(dir, "moved.mp4")); err != nil {
		t.Fatal(err)
	}
	expect("a deleted file")

	// A library folder that is missing at the start is watched once it
	// appears.
	if err := os.Mkdir(later, 0700); err != nil {
		t.Fatal(err)
	}
	expect("a library folder appearing")
	write(filepath.Join(later, "b.mp4"))
	expect("a file in a library folder that appeared")
	write(filepath.Join(target, "c.mp4"))
	expect("a file in a linked library folder")

	// A library folder moved away is watched again at its path.
	if err := os.Rename(later, later+".old"); err != nil {
		t.Fatal(err)
	}
	expect("a library folder moved away")
	if err := os.Mkdir(later, 0700); err != nil {
		t.Fatal(err)
	}
	expect("a library folder recreated")
	write(filepath.Join(later, "d.mp4"))
	expect("a file in a recreated library folder")
	write(filepath.Join(later+".old", "e.mp4"))
	quiet("a file in the moved-away folder")

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not stop")
	}
}

func TestBelow(t *testing.T) {
	for _, tc := range []struct {
		path, root string
		depth      int
		ok         bool
	}{
		{"/media", "/media", 0, true},
		{"/media/a/b", "/media", 2, true},
		{"/mediax/a", "/media", 0, false},
		{"/", "/media", 0, false},
		{"/a", "/", 1, true},
		{"/a/b", "/", 2, true},
	} {
		if depth, ok := below(tc.path, tc.root); depth != tc.depth || ok != tc.ok {
			t.Errorf("below(%s, %s) = %d, %v", tc.path, tc.root, depth, ok)
		}
	}
}
