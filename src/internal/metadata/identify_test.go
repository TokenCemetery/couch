package metadata

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRefreshAndIdentify(t *testing.T) {
	store, fake := newRefreshTest(t)
	dir := t.TempDir()
	nfo := filepath.Join(dir, "Other (2001)", "Other (2001).nfo")
	if err := os.MkdirAll(filepath.Dir(nfo), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nfo, []byte(`<movie><title>Local Title</title><uniqueid type="tmdb">55</uniqueid></movie>`), 0600); err != nil {
		t.Fatal(err)
	}
	items := mediaItems()[:2]
	var published Library
	cfg := Config{TMDbKey: "tmdb-key", Language: "en", ImageDir: t.TempDir()}
	r, err := NewRefresher(store, cfg, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	if !r.refresh(t.Context(), items, dirFiles(dir)) || nameOf(published, "other") != "Local Title" {
		t.Fatalf("first refresh: %+v", published["other"])
	}

	// Refresh fetches the movie again although it was just fetched.
	r.Refresh([]string{"movie"})
	r.refresh(t.Context(), items, dirFiles(dir))
	if fake.count("/tmdb/movie/1") != 2 || fake.count("/tmdb/movie/55") != 1 {
		t.Fatalf("forced refresh: %v", fake.requests())
	}

	// Identify looks the item up by the chosen ID alone, and the provider's
	// metadata replaces the .nfo's. A stopped refresh keeps the request.
	r.Identify("other", map[string]string{"Tmdb": "1"})
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if r.refresh(canceled, items, dirFiles(dir)) {
		t.Fatal("a canceled refresh finished")
	}
	searches := fake.count("/tmdb/search/movie")
	r.refresh(t.Context(), items, dirFiles(dir))
	other := published["other"]
	if other.Name != "Online Movie" || other.ProviderIDs["Tmdb"] != "1" || fake.count("/tmdb/search/movie") != searches {
		t.Fatalf("identified: %+v, %v", other, fake.requests())
	}

	// The identification is stored: a restart keeps it and asks nobody.
	before := len(fake.requests())
	restarted, err := NewRefresher(store, cfg, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	restarted.refresh(t.Context(), items, dirFiles(dir))
	if nameOf(published, "other") != "Online Movie" || len(fake.requests()) != before {
		t.Fatalf("after restart: %+v, %d requests", published["other"], len(fake.requests())-before)
	}

	// Removing it goes back to the .nfo and its provider ID.
	restarted.Identify("other", nil)
	restarted.refresh(t.Context(), items, dirFiles(dir))
	if other := published["other"]; other.Name != "Local Title" || other.Overview != "Overview of 55." || other.ProviderIDs["Tmdb"] != "55" {
		t.Fatalf("identification removed: %+v", other)
	}
}

func TestSearch(t *testing.T) {
	store, _ := newRefreshTest(t)
	r, err := NewRefresher(store, Config{TMDbKey: "tmdb-key", TVDbKey: "tvdb-key", Language: "en"}, func(Library) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	movies, err := r.Search(ctx, "Movie", "The Movie", 2010, nil)
	if err != nil || len(movies) != 2 || movies[1].Name != "Le Film" || movies[1].ProviderIDs["Tmdb"] != "1" || movies[1].Provider != "TheMovieDb" {
		t.Fatalf("movies by name: %+v, %v", movies, err)
	}
	byID, err := r.Search(ctx, "Movie", "", 0, map[string]string{"Tmdb": "1"})
	if err != nil || len(byID) != 1 || byID[0].Name != "Online Movie" || byID[0].Year != 2010 || byID[0].ImageURL == "" || byID[0].ProviderIDs["Imdb"] != "tt0000001" {
		t.Fatalf("movie by ID: %+v, %v", byID, err)
	}
	// TheTVDB answers series first; TMDb finds nothing here.
	series, err := r.Search(ctx, "Series", "Show", 0, nil)
	if err != nil || len(series) != 1 || series[0].ProviderIDs["Tvdb"] != "7" || series[0].Provider != "TheTVDB" {
		t.Fatalf("series by name: %+v, %v", series, err)
	}
	series, err = r.Search(ctx, "Series", "", 0, map[string]string{"Tvdb": "7"})
	if err != nil || len(series) != 1 || series[0].Name != "Translated Show" {
		t.Fatalf("series by ID: %+v, %v", series, err)
	}

	offline, err := NewRefresher(store, Config{Language: "en"}, func(Library) {})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := offline.Search(ctx, "Movie", "The Movie", 0, nil); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("without keys: %v", err)
	}
}
