package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// dirFiles reads sidecars from a directory, as media.Catalog does.
type dirFiles string

func (d dirFiles) ReadSidecar(name string, limit int64) ([]byte, fs.FileInfo, error) {
	info, err := os.Stat(filepath.Join(string(d), name))
	if err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(filepath.Join(string(d), name))
	return b, info, err
}

// fakeProviders serves the TMDb, TheTVDB and image requests a refresh makes
// and records their paths.
type fakeProviders struct {
	mu    sync.Mutex
	paths []string
}

func (f *fakeProviders) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeProviders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/img/") {
		img := image.NewRGBA(image.Rect(0, 0, 2, 3))
		_ = png.Encode(w, img)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/tmdb/") && r.URL.Query().Get("api_key") != "tmdb-key" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/tvdb/") && r.URL.Path != "/tvdb/login" && r.Header.Get("Authorization") != "Bearer tvdb-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	bodies := map[string]string{
		// Ranked as TMDb does for a query in another language than the
		// titles: a minor title first.
		"/tmdb/search/movie": `{"results":[{"id":2,"title":"The Movie: Making Of","popularity":1.5},{"id":1,"title":"Le Film","original_title":"The Movie","popularity":40}]}`,
		"/tmdb/movie/1": `{"title":"Online Movie","original_title":"Film en ligne","overview":"From TMDb.","release_date":"2010-05-01",
			"vote_average":7.2,"genres":[{"name":"Comedy"}],"production_companies":[{"name":"Studio A"}],"poster_path":"/img/poster.png",
			"imdb_id":"tt0000001","credits":{"cast":[{"name":"Actor One","character":"Hero"}],"crew":[{"name":"Dir One","job":"Director","department":"Directing"}]},
			"keywords":{"keywords":[{"name":"heist"}]},"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"PG"}]}]}}`,
		"/tmdb/movie/55":      `{"title":"TMDb Title","overview":"Overview of 55.","release_date":"2001-01-01","genres":[{"name":"Action"}]}`,
		"/tmdb/tv/9":          `{"name":"TMDb Show","overview":"TMDb overview.","vote_average":8.1,"networks":[{"name":"Net"}],"backdrop_path":"/img/show-backdrop.png"}`,
		"/tmdb/tv/9/season/1": `{"episodes":[{"episode_number":2,"name":"TMDb Episode","overview":"TMDb episode overview.","air_date":"2008-02-01","vote_average":8.5}]}`,
		"/tvdb/login":         `{"data":{"token":"tvdb-token"}}`,
		"/tvdb/search":        `{"data":[{"tvdb_id":"7"}]}`,
		"/tvdb/series/7/extended": `{"data":{"name":"Original Show","overview":"Original overview.","firstAired":"2008-01-20","image":"/img/show.png",
			"genres":[{"name":"Drama"}],"characters":[{"name":"Walter","personName":"Actor Two","peopleType":"Actor"}],
			"remoteIds":[{"id":"9","sourceName":"TheMovieDB.com"}]}}`,
		"/tvdb/series/7/translations/eng":     `{"data":{"name":"Translated Show","overview":"Translated overview."}}`,
		"/tvdb/series/7/episodes/default/eng": `{"data":{"episodes":[{"seasonNumber":1,"number":2,"name":"TVDB Episode","aired":"2008-01-27"}]}}`,
	}
	body, found := bodies[r.URL.Path]
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write([]byte(body))
}

func newRefreshTest(t *testing.T) (*state.Store, *fakeProviders) {
	t.Helper()
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Initialize("viewer", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeProviders{}
	server := httptest.NewTLSServer(fake)
	t.Cleanup(server.Close)
	saved := []any{tmdbAPI, tmdbImages, tvdbAPI, tvdbArtwork, httpClient}
	tmdbAPI, tmdbImages, tvdbAPI, tvdbArtwork, httpClient = server.URL+"/tmdb", server.URL, server.URL+"/tvdb", server.URL, server.Client()
	t.Cleanup(func() {
		tmdbAPI, tmdbImages, tvdbAPI, tvdbArtwork = saved[0].(string), saved[1].(string), saved[2].(string), saved[3].(string)
		httpClient = saved[4].(*http.Client)
	})
	return store, fake
}

func TestRefresh(t *testing.T) {
	store, fake := newRefreshTest(t)
	dir := t.TempDir()
	writeFile := func(name, content string, modified time.Time) {
		t.Helper()
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("Other (2001)/Other (2001).nfo", `<movie><title>Local Title</title><genre>Drama</genre><uniqueid type="tmdb">55</uniqueid></movie>`, time.Unix(1000, 0))
	items := mediaItems()
	images := t.TempDir()
	var published Library
	cfg := Config{TMDbKey: "tmdb-key", TVDbKey: "tvdb-key", Language: "en", ImageDir: images}
	r, err := NewRefresher(store, cfg, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	r.refresh(t.Context(), items, dirFiles(dir))

	movie := published["movie"]
	if movie.Name != "Online Movie" || movie.OriginalTitle != "Film en ligne" || movie.Year != 2010 || movie.OfficialRating != "PG" ||
		movie.Community != 7.2 || strings.Join(movie.Genres, ",") != "Comedy" || strings.Join(movie.Tags, ",") != "heist" ||
		len(movie.People) != 2 || movie.ProviderIDs["Imdb"] != "tt0000001" {
		t.Fatalf("movie: %+v", movie)
	}
	poster := movie.Images["Primary"]
	if poster.Width != 2 || poster.Height != 3 || poster.MIME != "image/png" {
		t.Fatalf("poster: %+v", poster)
	}
	if _, err := os.Stat(filepath.Join(images, poster.File)); err != nil {
		t.Fatal(err)
	}
	// The .nfo wins where it has a value; TMDb, asked by the .nfo's ID,
	// fills the rest.
	other := published["other"]
	if other.Name != "Local Title" || strings.Join(other.Genres, ",") != "Drama" || other.Overview != "Overview of 55." || other.Year != 2001 {
		t.Fatalf("other: %+v", other)
	}
	// TheTVDB answers first; TMDb, found through TheTVDB's IDs, fills the rest.
	series := published["series"]
	if series.Name != "Translated Show" || series.OriginalTitle != "Original Show" || series.Overview != "Translated overview." ||
		series.Community != 8.1 || strings.Join(series.Studios, ",") != "Net" || series.ProviderIDs["Tmdb"] != "9" ||
		series.Images["Primary"].File == "" || series.Images["Backdrop"].File == "" {
		t.Fatalf("series: %+v", series)
	}
	episode := published["episode"]
	if episode.Name != "TVDB Episode" || episode.Overview != "TMDb episode overview." || episode.Premiere.Format(time.DateOnly) != "2008-01-27" {
		t.Fatalf("episode: %+v", episode)
	}

	// A second refresh reads nothing new and asks nobody.
	before := len(fake.requests())
	published = nil
	r.refresh(t.Context(), items, dirFiles(dir))
	if len(fake.requests()) != before || published["movie"].Name != "Online Movie" {
		t.Fatalf("unchanged refresh made %d requests", len(fake.requests())-before)
	}

	// A changed .nfo is read again; with the same provider ID nothing is
	// fetched again.
	writeFile("Other (2001)/Other (2001).nfo", `<movie><title>New Title</title><uniqueid type="tmdb">55</uniqueid></movie>`, time.Unix(2000, 0))
	r.refresh(t.Context(), items, dirFiles(dir))
	if published["other"].Name != "New Title" || strings.Join(published["other"].Genres, ",") != "Action" || len(fake.requests()) != before {
		t.Fatalf("changed nfo: %+v, %d requests", published["other"], len(fake.requests())-before)
	}

	// A removed item loses its record and its downloaded image.
	r.refresh(t.Context(), items[1:], dirFiles(dir))
	if _, found := published["movie"]; found {
		t.Fatal("removed item still published")
	}
	if _, err := os.Stat(filepath.Join(images, poster.File)); !os.IsNotExist(err) {
		t.Fatalf("unused image kept: %v", err)
	}

	// Restarted with the keys removed, Couch sends nothing and keeps what
	// was fetched.
	offline, err := NewRefresher(store, Config{Language: "en", ImageDir: images}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	before = len(fake.requests())
	offline.refresh(t.Context(), items, dirFiles(dir))
	if len(fake.requests()) != before || published["series"].Name != "Translated Show" || published["movie"].Name != "" {
		t.Fatalf("offline: %d requests, %+v", len(fake.requests())-before, published["series"])
	}
}

func TestRefreshInterval(t *testing.T) {
	store, fake := newRefreshTest(t)
	items := mediaItems()[:1]
	var published Library
	cfg := Config{TMDbKey: "tmdb-key", Language: "en", ImageDir: t.TempDir(), Refresh: 24 * time.Hour}
	r, err := NewRefresher(store, cfg, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	r.refresh(t.Context(), items, dirFiles(t.TempDir()))
	if published["movie"].Name != "Online Movie" {
		t.Fatalf("movie: %+v", published["movie"])
	}
	before := len(fake.requests())
	r.refresh(t.Context(), items, dirFiles(t.TempDir()))
	if len(fake.requests()) != before {
		t.Fatal("fresh metadata fetched again")
	}
	records, err := store.Metadata(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	old := records["movie"]
	old.FetchedAt = time.Now().Add(-25 * time.Hour)
	if err := store.SaveMetadata(t.Context(), []state.MetadataRecord{old}, nil); err != nil {
		t.Fatal(err)
	}
	r.refresh(t.Context(), items, dirFiles(t.TempDir()))
	if len(fake.requests()) == before {
		t.Fatal("aged metadata not fetched again")
	}
}

func TestRefreshRejectedKey(t *testing.T) {
	store, fake := newRefreshTest(t)
	var published Library
	r, err := NewRefresher(store, Config{TMDbKey: "wrong", Language: "en", ImageDir: t.TempDir()}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	r.refresh(t.Context(), mediaItems(), dirFiles(t.TempDir()))
	// The first rejection stops the run; nothing is stored as not found.
	if n := len(fake.requests()); n != 1 {
		t.Fatalf("%d requests after a rejected key", n)
	}
	records, err := store.Metadata(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for id, record := range records {
		if record.OnlineKey != "" {
			t.Fatalf("%s stored after a rejected key", id)
		}
	}
	if published == nil {
		t.Fatal("local metadata not published")
	}
}

func TestRefresherRunStopsOnCancel(t *testing.T) {
	store, _ := newRefreshTest(t)
	published := make(chan Library, 10)
	r, err := NewRefresher(store, Config{Language: "en", ImageDir: t.TempDir()}, func(l Library) { published <- l })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	r.Update(&media.Catalog{Items: mediaItems()[:1]})
	select {
	case <-published:
	case <-time.After(10 * time.Second):
		t.Fatal("nothing published")
	}
	cancel()
	<-done
}

func mediaItems() []media.Item {
	return []media.Item{
		{ID: "movie", Name: "The.Movie.2010.1080p", Path: "The.Movie.2010.1080p.mkv"},
		{ID: "other", Name: "Other (2001)", Path: "Other (2001)/Other (2001).mkv"},
		{ID: "series", Name: "Show", Kind: "Series", Path: "Show"},
		{ID: "season", Name: "Season 1", Kind: "Season", Path: "Show/Season 01", SeriesID: "series", SeasonNumber: 1},
		{ID: "episode", Name: "Show.S01E02", Kind: "Episode", Path: "Show/Season 01/Show.S01E02.mkv", SeriesID: "series", SeasonID: "season", SeasonNumber: 1, EpisodeNumber: 2},
	}
}

func TestLibraryJSONRoundTrip(t *testing.T) {
	info := Info{Name: "x", Premiere: time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC), Images: map[string]Image{"Primary": {URL: "https://a/b.jpg", File: "f.jpg"}}}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(b); !got.Premiere.Equal(info.Premiere) || got.Images["Primary"].File != "f.jpg" || bytes.Contains(b, []byte("Overview")) {
		t.Fatalf("%s", b)
	}
}
