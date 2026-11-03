package library

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
)

func TestSettle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan struct{})
	fired := make(chan time.Time, 4)
	go settle(ctx, changes, 50*time.Millisecond, 200*time.Millisecond, func() { fired <- time.Now() })

	// A burst fires once, after it stops.
	start := time.Now()
	for range 3 {
		changes <- struct{}{}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case at := <-fired:
		if at.Sub(start) < 70*time.Millisecond {
			t.Fatalf("fired %v after the first change, during the burst", at.Sub(start))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a burst did not fire")
	}

	// Changes that never stop still fire, after longest.
	deadline := time.Now().Add(10 * time.Second)
	fired2 := false
	for !fired2 && time.Now().Before(deadline) {
		select {
		case changes <- struct{}{}:
		case <-fired:
			fired2 = true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !fired2 {
		t.Fatal("steady changes never fired")
	}
}

func TestMonitor(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("folder changes are watched on Linux")
	}
	settleQuiet = 100 * time.Millisecond
	defer func() { settleQuiet = 10 * time.Second }()
	dir := t.TempDir()
	store, _ := testStore(t)
	published := make(chan *media.Catalog, 8)
	m, err := New(store, []string{dir}, "", func(c *media.Catalog) { published <- c })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	photos := media.Library{ID: "p", Name: "Photos", Type: media.HomeVideos, Paths: []string{dir}, Monitor: true}
	if err := m.SetLibraries([]media.Library{photos}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	<-published
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	defer func() {
		cancel()
		<-stopped
	}()
	go func() {
		m.Run(ctx, nil, 0, false)
		close(stopped)
	}()
	next := func(what string) *media.Catalog {
		t.Helper()
		for {
			select {
			case c := <-published:
				if len(published) == 0 {
					return c
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("no catalog published after %s", what)
				return nil
			}
		}
	}
	addPhoto := func(name string) {
		t.Helper()
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, image.NewGray(image.Rect(0, 0, 8, 8))); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// The scan the stored libraries asked for runs first.
	next("the first scan")
	time.Sleep(100 * time.Millisecond)
	addPhoto("first.png")
	if _, _, n := counts(next("a new photo")); n != 1 {
		t.Fatalf("a new photo published %d photos", n)
	}

	// Turned off, the folder is no longer watched, and nothing is scanned.
	if err := m.SetMonitor("p", false); err != nil {
		t.Fatal(err)
	}
	addPhoto("second.png")
	select {
	case c := <-published:
		t.Fatalf("published %d items without monitoring", len(c.Items))
	case <-time.After(time.Second):
	}
	// Turned on again, a rescan finds what changed meanwhile.
	if err := m.SetMonitor("p", true); err != nil {
		t.Fatal(err)
	}
	if _, _, n := counts(next("turning monitoring on")); n != 2 {
		t.Fatalf("turning monitoring on published %d photos", n)
	}
	if err := m.SetMonitor("missing", true); !errors.Is(err, ErrNoLibrary) {
		t.Fatalf("an unknown library: %v", err)
	}
}
