package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMusicLibrary(t *testing.T) {
	base := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(base, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("Artist A/Album One/01 First.mp3")
	write("Artist A/Album One/CD2/1-02. Second.flac")
	write("Artist B/Untagged/03 - Plain Track.wav")
	write("Loose.ogg")
	write("Movie.mp4")
	write("Artist A/Album One/01 First.srt")
	tags := map[string]Tags{
		"01 First.mp3":         {Title: "First Song", Album: "Album One", AlbumArtist: "Artist A", Artists: []string{"Artist A", "Guest"}, Genres: []string{"Rock"}, Track: 1, Year: 2001},
		"1-02. Second.flac":    {Title: "Second Song", Album: "Album One", Artists: []string{"Artist A"}, Genres: []string{"Pop"}, Year: 2003},
		"03 - Plain Track.wav": {},
		"Loose.ogg":            {Title: "Loose"},
	}
	probes := 0
	probe := func(_ context.Context, f *os.File) (Item, error) {
		probes++
		name := filepath.Base(f.Name())
		if name == "Movie.mp4" {
			return Item{Streams: []Stream{{Type: "Video"}}}, nil
		}
		return Item{RunTimeTicks: 30000000, Streams: []Stream{{Type: "Audio", Codec: "mp3"}}, Tags: tags[name]}, nil
	}
	music := Library{ID: "music", Name: "Music", Type: Music, Paths: []string{base}}
	catalog, err := (&Catalog{probe: probe, sources: map[string]*source{}}).Rescan(context.Background(), []Library{music})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	// A music library alone does not probe videos.
	if probes != 4 || len(catalog.Items) != 4 {
		t.Fatalf("probes %d, items %+v", probes, catalog.Items)
	}
	byName := map[string]Item{}
	for _, item := range catalog.Items {
		byName[item.Name] = item
	}
	first, second, plain, loose := byName["First Song"], byName["Second Song"], byName["Plain Track"], byName["Loose"]
	// Tags win; the disc folder and the name fill what tags lack.
	if first.Type() != "Audio" || first.Tags.Track != 1 || len(first.Streams) != 1 || second.Tags.Disc != 1 || second.Tags.Track != 2 || plain.Tags.Track != 3 {
		t.Fatalf("tracks %+v %+v %+v", first, second, plain)
	}
	// Tracks of one album artist and album share the album, even in
	// another folder; the folder fallback names an untagged album.
	if first.ParentID == "" || first.ParentID != second.ParentID || plain.ParentID == first.ParentID {
		t.Fatalf("albums %q %q %q", first.ParentID, second.ParentID, plain.ParentID)
	}
	album, ok := catalog.Folder(first.ParentID)
	if !ok || album.Type() != "MusicAlbum" || !album.IsFolder() || album.Name != "Album One" || album.Tags.AlbumArtist != "Artist A" ||
		album.Tags.Year != 2003 || len(album.Tags.Genres) != 2 || album.Path != "Artist A/Album One" || album.RunTimeTicks != 60000000 ||
		album.ParentID != music.ID || catalog.LibraryOf(first) != music.ID || catalog.LibraryOf(album) != music.ID {
		t.Fatalf("album %+v", album)
	}
	untagged, _ := catalog.Folder(plain.ParentID)
	if untagged.Name != "Untagged" || untagged.Tags.AlbumArtist != "Artist B" {
		t.Fatalf("untagged album %+v", untagged)
	}
	// A file with neither an album tag nor a folder sits in the library.
	if loose.ParentID != music.ID || catalog.LibraryOf(loose) != music.ID || catalog.ChildCount(music.ID) != 3 {
		t.Fatalf("loose %+v, children %d", loose, catalog.ChildCount(music.ID))
	}

	// The same folder in a movies library adds its videos, and the index
	// keeps the tags.
	movies := Library{ID: "movies", Name: "Movies", Type: Movies, Paths: []string{base}}
	both, err := catalog.Rescan(context.Background(), []Library{music, movies})
	if err != nil {
		t.Fatal(err)
	}
	if probes != 5 || len(both.Items) != 5 {
		t.Fatalf("probes %d, items %d", probes, len(both.Items))
	}
	index, err := both.Index()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadLibraries([]Library{music, movies}, index, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = loaded.Close() }()
	got, _ := loaded.Lookup(first.ID)
	if len(loaded.Items) != 5 || len(loaded.Folders) != 2 || got.Name != "First Song" || got.ParentID != first.ParentID || len(got.Tags.Artists) != 2 {
		t.Fatalf("loaded %+v, %d folders", got, len(loaded.Folders))
	}
}
