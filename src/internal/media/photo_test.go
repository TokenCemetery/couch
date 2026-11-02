package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// exifJPEG encodes a width×height JPEG carrying an EXIF segment with an
// orientation and, when taken is set, a DateTimeOriginal and its offset.
func exifJPEG(t *testing.T, width, height, orientation int, taken, offset string) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	order := binary.BigEndian
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08")
	entry := func(tag, kind uint16, count, value uint32) []byte {
		e := make([]byte, 12)
		order.PutUint16(e, tag)
		order.PutUint16(e[2:], kind)
		order.PutUint32(e[4:], count)
		order.PutUint32(e[8:], value)
		return e
	}
	// IFD0 at 8: two entries and the next IFD offset, 2+24+4 bytes; the
	// EXIF IFD follows at 38 with two entries, its strings at 68.
	tiff = append(tiff, 0, 2)
	tiff = append(tiff, entry(tagOrientation, 3, 1, uint32(orientation)<<16)...)
	tiff = append(tiff, entry(tagExifIFD, 4, 1, 38)...)
	tiff = append(tiff, 0, 0, 0, 0)
	tiff = append(tiff, 0, 2)
	tiff = append(tiff, entry(tagDateTimeOriginal, 2, uint32(len(taken)+1), 68)...)
	tiff = append(tiff, entry(tagOffsetTimeOriginal, 2, uint32(len(offset)+1), uint32(68+len(taken)+1))...)
	tiff = append(tiff, 0, 0, 0, 0)
	tiff = append(tiff, taken+"\x00"+offset+"\x00"...)
	segment := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(segment)+2))
	out := append([]byte{0xFF, 0xD8}, app1...)
	out = append(out, segment...)
	return append(out, encoded.Bytes()[2:]...)
}

func TestHomeVideosLibrary(t *testing.T) {
	base := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(base, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, image.NewGray(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	write("Trip/Day 1/Beach.jpg", exifJPEG(t, 8, 6, 6, "2024:07:01 10:30:00", "+02:00"))
	write("Trip/Day 1/folder.jpg", pngData.Bytes())
	write("Trip/Sunset.png", pngData.Bytes())
	write("Trip/Clip.mp4", []byte("video"))
	write("Trip/Clip-poster.jpg", pngData.Bytes())
	write("Show/Season 01/Show.S01E01.mp4", []byte("video"))
	write("Loose.jpeg", exifJPEG(t, 2, 2, 1, "", ""))
	write("Broken.jpg", []byte("not a picture"))
	write("Song.mp3", []byte("audio"))
	probes := 0
	probe := func(_ context.Context, f *os.File) (Item, error) {
		probes++
		return Item{RunTimeTicks: 10000000, Streams: []Stream{{Type: "Video", Codec: "h264"}}}, nil
	}
	home := Library{ID: "home", Name: "Home", Type: HomeVideos, Paths: []string{base}}
	shows := Library{ID: "shows", Name: "Shows", Type: TVShows, Paths: []string{base}}
	catalog, err := (&Catalog{probe: probe, sources: map[string]*source{}}).Rescan(context.Background(), []Library{home, shows})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	// Pictures are read without FFprobe; covers, a broken file and audio
	// are not photos.
	if probes != 2 || catalog.Skipped != 1 {
		t.Fatalf("probes %d, skipped %d", probes, catalog.Skipped)
	}
	byName := map[string]Item{}
	for _, item := range slices.Concat(catalog.Items, catalog.Folders) {
		if _, dup := byName[item.Type()+item.Name]; dup {
			t.Fatalf("duplicate %s %s", item.Type(), item.Name)
		}
		byName[item.Type()+item.Name] = item
	}
	if len(catalog.Items) != 6 {
		t.Fatalf("items %+v", catalog.Items)
	}
	beach, sunset, loose := byName["PhotoBeach"], byName["PhotoSunset"], byName["PhotoLoose"]
	clip, episode := byName["VideoClip"], byName["EpisodeShow.S01E01"]
	trip, day := byName["PhotoAlbumTrip"], byName["PhotoAlbumDay 1"]
	// The EXIF orientation 6 turns 8×6 into 6×8, and the offset is kept.
	if beach.Photo.Width != 6 || beach.Photo.Height != 8 || !beach.Date().Equal(time.Date(2024, 7, 1, 8, 30, 0, 0, time.UTC)) {
		t.Fatalf("beach %+v", beach.Photo)
	}
	// Without EXIF a photo is dated by its file.
	if loose.Photo.Taken != (time.Time{}) || !loose.Date().Equal(loose.Modified) || sunset.Photo.Width != 4 {
		t.Fatalf("loose %+v sunset %+v", loose.Photo, sunset.Photo)
	}
	// Folders nest as albums; the top ones and loose files are the library's.
	if day.ParentID != trip.ID || trip.ParentID != "home" || beach.ParentID != day.ID || sunset.ParentID != trip.ID || loose.ParentID != "home" || clip.ParentID != trip.ID {
		t.Fatalf("parents trip %+v day %+v beach %+v clip %+v", trip, day, beach, clip)
	}
	// The TV shows library keeps its episode; the home videos library has
	// the same file as a video in the Show and Season 01 albums.
	if episode.ID == "" || catalog.LibraryOf(episode) != "shows" {
		t.Fatalf("episode %+v", episode)
	}
	if _, ok := byName["PhotoAlbumSeason 01"]; !ok {
		t.Fatal("no album for the season folder")
	}
	for _, item := range []Item{beach, sunset, loose, clip, trip, day} {
		if catalog.LibraryOf(item) != "home" {
			t.Fatalf("%s is in %q", item.Name, catalog.LibraryOf(item))
		}
	}
	if !catalog.Within(beach, trip.ID) || !catalog.Within(beach, day.ID) || catalog.Within(sunset, day.ID) {
		t.Fatal("Within does not follow the albums")
	}
	// A photo is its own image; an album has its folder.jpg, or else its
	// first photo.
	if info := catalog.ImageInfos(beach)["Primary"]; info.MIME != "image/jpeg" || info.Width != 6 {
		t.Fatalf("beach image %+v", info)
	}
	file, info, err := catalog.OpenImage(beach, "Primary")
	if err != nil || info.Revision != catalog.ImageInfos(beach)["Primary"].Revision {
		t.Fatalf("open beach: %v %+v", err, info)
	}
	_ = file.Close()
	if _, _, err := catalog.OpenImage(beach, "Backdrop"); err == nil {
		t.Fatal("a photo has a backdrop")
	}
	if catalog.ImageInfos(day)["Primary"].MIME != "image/png" {
		t.Fatalf("day album image %+v", catalog.ImageInfos(day))
	}
	if catalog.ImageInfos(trip)["Primary"].Revision != catalog.ImageInfos(sunset)["Primary"].Revision {
		t.Fatalf("trip album image %+v", catalog.ImageInfos(trip))
	}

	// The saved index keeps the photos' size and date.
	index := filepath.Join(t.TempDir(), "catalog.json")
	if err := catalog.Save(index); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadLibraries([]Library{home, shows}, index, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loaded.Close() }()
	if len(loaded.Items) != len(catalog.Items) || len(loaded.Folders) != len(catalog.Folders) {
		t.Fatalf("loaded %d items %d folders", len(loaded.Items), len(loaded.Folders))
	}
	again, ok := loaded.Lookup(beach.ID)
	if !ok || again.Photo != beach.Photo || again.ParentID != day.ID {
		t.Fatalf("loaded beach %+v", again)
	}
	// Without a home videos library, pictures are not cataloged.
	movies := Library{ID: "movies", Name: "Movies", Type: Movies, Paths: []string{base}}
	only, err := catalog.Rescan(context.Background(), []Library{movies})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range only.Items {
		if item.Type() == "Photo" || item.Type() == "Video" {
			t.Fatalf("movies library has %s %s", item.Type(), item.Name)
		}
	}
}

func TestJpegExifRejectsBadSegments(t *testing.T) {
	good := exifJPEG(t, 2, 2, 8, "2020:01:02 03:04:05", "")
	if taken, orientation := jpegExif(bytes.NewReader(good)); orientation != 8 || !taken.Equal(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("good: %v %d", taken, orientation)
	}
	for name, data := range map[string][]byte{
		"empty":     nil,
		"not jpeg":  []byte("PNG"),
		"truncated": good[:40],
		"bad date":  exifJPEG(t, 2, 2, 1, "yesterday", ""),
	} {
		if taken, _ := jpegExif(bytes.NewReader(data)); !taken.IsZero() {
			t.Errorf("%s: taken %v", name, taken)
		}
	}
}
