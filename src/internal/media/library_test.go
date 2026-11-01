package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLibraries(t *testing.T) {
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
	// films holds a movie and an episode; shows holds an episode; kids a movie.
	write("films/Film.mp4")
	write("films/Show/Season 1/Show.S01E01.mp4")
	write("shows/Other/Season 2/Other.S02E03.mkv")
	write("kids/Cartoon.mp4")
	probes := 0
	probe := func(context.Context, *os.File) (Item, error) {
		probes++
		return Item{RunTimeTicks: 10000000, Streams: []Stream{{Index: 0, Type: "Video", Codec: "h264"}}}, nil
	}
	films, shows, kids := filepath.Join(base, "films"), filepath.Join(base, "shows"), filepath.Join(base, "kids")
	defaults, err := DefaultLibraries(films)
	if err != nil {
		t.Fatal(err)
	}
	movies, tv := defaults[0], defaults[1]
	tv.Paths = append(tv.Paths, shows)
	libraries := []Library{movies, tv}
	catalog, err := (&Catalog{probe: probe, sources: map[string]*source{}}).Rescan(context.Background(), libraries)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	// films is walked once although both libraries use it.
	if probes != 3 {
		t.Fatalf("probes %d, want 3", probes)
	}
	byName := map[string]Item{}
	for _, item := range catalog.Items {
		byName[item.Name] = item
	}
	film, episode, other := byName["Film"], byName["Show.S01E01"], byName["Other.S02E03"]
	if len(catalog.Items) != 3 || catalog.LibraryOf(film) != movies.ID || catalog.LibraryOf(episode) != tv.ID || catalog.LibraryOf(other) != tv.ID {
		t.Fatalf("items %+v", catalog.Items)
	}
	// A one-directory catalogue's IDs do not change.
	single, err := scan(context.Background(), films, probe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = single.Close() }()
	for _, item := range single.Items {
		if byName[item.Name].ID != item.ID {
			t.Fatalf("%s: ID %s, want %s", item.Name, byName[item.Name].ID, item.ID)
		}
	}
	series, ok := catalog.Folder(episode.SeriesID)
	if !ok || series.ParentID != tv.ID || catalog.ChildCount(tv.ID) != 2 || catalog.ChildCount(movies.ID) != 1 {
		t.Fatalf("series %+v, children %d %d", series, catalog.ChildCount(tv.ID), catalog.ChildCount(movies.ID))
	}
	if f, err := catalog.Open(other); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}

	// A new library is listed at once and filled by the next rescan.
	cartoons := Library{ID: NewLibraryID(), Name: "Kids", Type: Movies, Paths: []string{kids}}
	libraries = append(libraries, cartoons)
	reassigned := catalog.Reassign(libraries)
	if _, ok := reassigned.Library(cartoons.ID); !ok || reassigned.ChildCount(cartoons.ID) != 0 || len(reassigned.Items) != 3 {
		t.Fatal("reassign did not list the new library without scanning")
	}
	probes = 0
	next, err := reassigned.Rescan(context.Background(), libraries)
	if err != nil {
		t.Fatal(err)
	}
	if probes != 1 || next.ChildCount(cartoons.ID) != 1 {
		t.Fatalf("probes %d, kids %d", probes, next.ChildCount(cartoons.ID))
	}

	// A user limited to the TV library sees its episodes only, though films
	// also holds a movie, and not the library added since.
	view := next.Only([]string{tv.ID})
	if len(view.Libraries()) != 1 || len(view.Items) != 2 || view.LibraryOf(view.Items[0]) != tv.ID || view.ChildCount(movies.ID) != 0 || view.ChildCount(cartoons.ID) != 0 {
		t.Fatalf("view %+v %+v", view.Libraries(), view.Items)
	}
	if f, err := view.Open(episode); err != nil {
		t.Fatal(err)
	} else {
		_ = f.Close()
	}
	if none := next.Only(nil); len(none.Libraries()) != 0 || len(none.Items)+len(none.Folders) != 0 {
		t.Fatal("a user without libraries sees items")
	}

	// One folder that cannot be read keeps its items and does not hold up
	// the others.
	write("kids/Second.mp4")
	if err := os.Chmod(shows, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(shows, 0700) }() //nolint:gosec // a directory needs its search bit
	after, err := next.Rescan(context.Background(), libraries)
	if err == nil || after == nil {
		t.Fatalf("unreadable folder: %v, %v", after, err)
	}
	if after.ChildCount(cartoons.ID) != 2 || len(after.Episodes(other.SeriesID)) != 1 {
		t.Fatal("a failed folder lost its items or held up another")
	}

	// Removing a library drops its items and releases its folder.
	without := after.Reassign(libraries[:2])
	after.CloseRemoved(without)
	if _, ok := without.Library(cartoons.ID); ok || len(without.Items) != 3 {
		t.Fatalf("removed library still listed: %d items", len(without.Items))
	}
}

func TestOverlap(t *testing.T) {
	libraries := []Library{
		{ID: "m", Type: Movies, Paths: []string{"/media/films"}},
		{ID: "t", Type: TVShows, Paths: []string{"/media/films"}},
	}
	other := Library{ID: "n", Type: Movies}
	for path, want := range map[string]bool{"/media/films": true, "/media/films/new": true, "/media": true, "/media/films2": false, "/media/shows": false} {
		if got := Overlap(libraries, other, path); got != want {
			t.Errorf("%s: overlap %v, want %v", path, got, want)
		}
	}
	// A TV library may share a movie library's folder.
	if Overlap(libraries, Library{ID: "u", Type: TVShows, Paths: nil}, "/media/other") {
		t.Error("unrelated folder overlaps")
	}
	if !Inside("/media/films/a", "/media") || Inside("/media2", "/media") || !Inside("/media", "/") {
		t.Error("Inside")
	}
}
