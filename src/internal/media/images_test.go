package media

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPrimaryImagesBoundedAndConfined(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{root: root}
	defer func() { _ = catalog.Close() }()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	poster := filepath.Join(dir, "Movie-poster.png")
	if err := os.WriteFile(poster, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	item := Item{ID: "movie", Path: "Movie.mp4"}
	file, info, err := catalog.OpenImage(item, "Primary")
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(content, encoded.Bytes()) || info.Width != 2 || info.Height != 3 || info.MIME != "image/png" {
		t.Fatal("incorrect image")
	}
	changed := time.Now().Add(time.Second)
	if err := os.Chtimes(poster, changed, changed); err != nil {
		t.Fatal(err)
	}
	updated, ok := catalog.ImageInfo(item, "Primary")
	if !ok || updated.Revision == info.Revision {
		t.Fatal("stale image revision")
	}
	if err := os.Remove(poster); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ImageInfo(item, "Primary"); found {
		t.Fatal("removed image advertised")
	}
	outside := filepath.Join(t.TempDir(), "private.png")
	if err := os.WriteFile(outside, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, poster); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ImageInfo(item, "Primary"); found {
		t.Fatal("symlink escaped root")
	}
	if err := os.Remove(poster); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(poster, 0600); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ImageInfo(item, "Primary"); found {
		t.Fatal("FIFO accepted")
	}
	if err := os.Remove(poster); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(poster)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxImageBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, found := catalog.ImageInfo(item, "Primary"); found {
		t.Fatal("oversized image accepted")
	}
	if err := os.WriteFile(poster, []byte("<svg/>"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ImageInfo(item, "Primary"); found {
		t.Fatal("invalid image accepted")
	}
	// Directory sidecars work for virtual series and season nodes too.
	if err := os.Mkdir(filepath.Join(dir, "Show"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Show", "poster.png"), encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ImageInfo(Item{Kind: "Series", Path: "Show"}, "Primary"); !found {
		t.Fatal("series poster missing")
	}
}

func TestBackdropImageLookup(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{root: root}
	defer func() { _ = catalog.Close() }()
	write := func(name string, width int) {
		t.Helper()
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, width, 1))); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), encoded.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	width := func(item Item) int {
		t.Helper()
		info, found := catalog.ImageInfo(item, "Backdrop")
		if !found {
			return 0
		}
		return info.Width
	}
	movie := Item{ID: "movie", Path: "Movie.mp4"}
	// A cover named after the video is not a backdrop.
	write("Movie.png", 1)
	if width(movie) != 0 {
		t.Fatal("cover used as backdrop")
	}
	write("fanart.png", 2)
	if width(movie) != 2 {
		t.Fatal("folder fanart not found")
	}
	write("backdrop.png", 3)
	if width(movie) != 3 {
		t.Fatal("backdrop does not precede fanart")
	}
	write("Movie-backdrop.png", 4)
	if width(movie) != 4 {
		t.Fatal("video backdrop does not precede the folder's")
	}
	write("Movie-fanart.png", 5)
	if width(movie) != 5 {
		t.Fatal("video fanart does not come first")
	}
	series := Item{ID: "series", Kind: "Series", Path: "Show"}
	if width(series) != 0 {
		t.Fatal("parent folder backdrop used for a series")
	}
	write("Show/fanart.png", 6)
	if width(series) != 6 {
		t.Fatal("series backdrop not found")
	}
	if info, found := catalog.ImageInfo(series, "Primary"); found {
		t.Fatalf("backdrop used as cover: %+v", info)
	}
	kindWidth := func(item Item, kind string) int {
		t.Helper()
		info, _ := catalog.ImageInfo(item, kind)
		return info.Width
	}
	write("logo.png", 7)
	write("Movie-logo.png", 8)
	write("thumb.png", 9)
	write("landscape.png", 10)
	if kindWidth(movie, "Logo") != 8 || kindWidth(movie, "logo") != 8 || kindWidth(movie, "Thumb") != 10 || kindWidth(movie, "Banner") != 0 {
		t.Fatal("logo or thumb lookup")
	}
	// ImageInfos finds what ImageInfo finds for each type, from one listing.
	for _, item := range []Item{movie, series} {
		infos := catalog.ImageInfos(item)
		for _, kind := range ImageTypes {
			want, found := catalog.ImageInfo(item, kind)
			if got, ok := infos[kind]; ok != found || got != want {
				t.Fatalf("%s %s: ImageInfos %+v %v, ImageInfo %+v %v", item.ID, kind, got, ok, want, found)
			}
		}
	}
}

func TestImageDirectoryListingIsRevalidated(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &Catalog{root: root}
	defer func() { _ = catalog.Close() }()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	item := Item{ID: "movie", Path: "Movie.mp4"}
	// add writes an image and sets the directory mtime, as a coarse clock
	// or an unchanged directory would leave it.
	add := func(name string, mtime time.Time) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), encoded.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	if _, found := catalog.ImageInfo(item, "Primary"); found {
		t.Fatal("image in an empty directory")
	}
	add("poster.png", old)
	if _, found := catalog.ImageInfo(item, "Primary"); found {
		t.Fatal("listing of an unchanged directory was not reused")
	}
	add("poster.png", old.Add(time.Minute))
	if _, found := catalog.ImageInfo(item, "Primary"); !found {
		t.Fatal("changed directory was not listed again")
	}
	recent := time.Now()
	if err := os.Chtimes(dir, recent, recent); err != nil {
		t.Fatal(err)
	}
	catalog.ImageInfo(item, "Logo")
	add("logo.png", recent)
	if _, found := catalog.ImageInfo(item, "Logo"); !found {
		t.Fatal("listing taken in the directory's mtime tick was reused")
	}
}
