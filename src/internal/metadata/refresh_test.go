package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// dirFiles reads sidecars from a directory, as media.Catalog does.
type dirFiles string

func (d dirFiles) ReadSidecar(_ media.Item, name string, limit int64) ([]byte, fs.FileInfo, error) {
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
	// uris holds the paths with their queries.
	uris []string
	// down answers 500 to every request; limited answers that many 429s
	// first; delay slows every answer.
	down    bool
	limited int
	delay   time.Duration
}

func (f *fakeProviders) set(fn func(*fakeProviders)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeProviders) count(path string) int {
	n := 0
	for _, p := range f.requests() {
		if p == path {
			n++
		}
	}
	return n
}

func (f *fakeProviders) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func (f *fakeProviders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.uris = append(f.uris, r.URL.RequestURI())
	down, limited, delay := f.down, f.limited > 0, f.delay
	if limited {
		f.limited--
	}
	f.mu.Unlock()
	time.Sleep(delay)
	switch {
	case down:
		w.WriteHeader(http.StatusInternalServerError)
		return
	case limited:
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		return
	}
	if strings.Contains(r.URL.Path, "/img/") {
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
		"/tmdb/tv/9/season/1": `{"id":90,"name":"Season 1","overview":"TMDb season overview.","air_date":"2008-01-21","poster_path":"/img/season.png","episodes":[{"episode_number":2,"name":"TMDb Episode","overview":"TMDb episode overview.","air_date":"2008-02-01","vote_average":8.5}]}`,
		"/tvdb/login":         `{"data":{"token":"tvdb-token"}}`,
		"/tvdb/search":        `{"data":[{"tvdb_id":"7"}]}`,
		"/tvdb/series/7/extended": `{"data":{"name":"Original Show","overview":"Original overview.","firstAired":"2008-01-20","image":"/img/show.png",
			"genres":[{"name":"Drama"}],"characters":[{"name":"Walter","personName":"Actor Two","peopleType":"Actor"}],
			"remoteIds":[{"id":"9","sourceName":"TheMovieDB.com"}],
			"seasons":[{"id":71,"number":1,"type":{"type":"dvd"}},{"id":70,"number":1,"year":"2008","image":"/img/season-tvdb.png","type":{"type":"official"},"nameTranslations":["eng"]}]}}`,
		"/tvdb/seasons/70/translations/eng":   `{"data":{"name":"Translated Season"}}`,
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

func (f *fakeProviders) requestURIs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.uris...)
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
	tmdbAPI, tmdbImages, tvdbAPI, tvdbArtwork, httpClient = server.URL+"/tmdb", server.URL+"/t/p/", server.URL+"/tvdb", server.URL, server.Client()
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
	// A season takes the official order's season from TheTVDB, then TMDb's,
	// reusing the series and season answers its series and episodes need.
	season := published["season"]
	if season.Name != "Translated Season" || season.Overview != "TMDb season overview." || season.Year != 2008 ||
		season.Premiere.Format(time.DateOnly) != "2008-01-21" || season.Images["Primary"].File == "" || len(season.ProviderIDs) != 0 {
		t.Fatalf("season: %+v", season)
	}
	if n := fake.count("/tvdb/series/7/extended") + fake.count("/tmdb/tv/9/season/1"); n != 2 {
		t.Fatalf("series record and season fetched %d times, want once each", n)
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

	// A season.nfo wins over the providers without fetching again.
	writeFile("Show/Season 01/season.nfo", `<season><plot>Local season plot.</plot></season>`, time.Unix(1000, 0))
	r.refresh(t.Context(), items, dirFiles(dir))
	if season := published["season"]; season.Overview != "Local season plot." || season.Name != "Translated Season" || len(fake.requests()) != before {
		t.Fatalf("season.nfo: %+v, %d requests", season, len(fake.requests())-before)
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

// shorten makes retries and publishing quick for a test.
func shorten(t *testing.T) {
	saved := []time.Duration{maxRetryAfter, retryFirst, retryMax, publishEvery}
	maxRetryAfter, retryFirst, retryMax, publishEvery = 10*time.Millisecond, 20*time.Millisecond, 40*time.Millisecond, 0
	t.Cleanup(func() { maxRetryAfter, retryFirst, retryMax, publishEvery = saved[0], saved[1], saved[2], saved[3] })
}

func TestRefreshWaitsOutRateLimit(t *testing.T) {
	store, fake := newRefreshTest(t)
	shorten(t)
	fake.set(func(f *fakeProviders) { f.limited = 2 })
	var published Library
	r, err := NewRefresher(store, Config{TMDbKey: "tmdb-key", Language: "en", ImageDir: t.TempDir()}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	if !r.refresh(t.Context(), mediaItems()[:1], dirFiles(t.TempDir())) || published["movie"].Name != "Online Movie" {
		t.Fatalf("movie: %+v", published["movie"])
	}
	if n := fake.count("/tmdb/search/movie"); n != 3 {
		t.Fatalf("search sent %d times, want 3: two 429s, then the answer", n)
	}
}

// failingStore fails every save after the first allowed ones.
type failingStore struct {
	metadataStore
	allowed int
}

func (s *failingStore) SaveMetadata(ctx context.Context, records []state.MetadataRecord, remove []string) error {
	if s.allowed == 0 {
		return os.ErrPermission
	}
	s.allowed--
	return s.metadataStore.SaveMetadata(ctx, records, remove)
}

func TestRefreshStopsWhenSaveFails(t *testing.T) {
	store, _ := newRefreshTest(t)
	failing := &failingStore{metadataStore: store, allowed: 1}
	r, err := NewRefresher(failing, Config{TMDbKey: "tmdb-key", Language: "en", ImageDir: t.TempDir()}, func(Library) {})
	if err != nil {
		t.Fatal(err)
	}
	if r.refresh(t.Context(), mediaItems()[:1], dirFiles(t.TempDir())) {
		t.Fatal("a refresh whose lookup was not saved reported success")
	}
	records, err := store.Metadata(t.Context())
	if err != nil || records["movie"].OnlineKey != "" {
		t.Fatalf("%+v, %v", records["movie"], err)
	}
}

func TestRefreshRetriesUnsavedImage(t *testing.T) {
	store, _ := newRefreshTest(t)
	// A file where the image directory should be makes every image
	// write fail, as a full disk would.
	images := filepath.Join(t.TempDir(), "metadata")
	if err := os.WriteFile(images, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var published Library
	r, err := NewRefresher(store, Config{TMDbKey: "tmdb-key", Language: "en", ImageDir: images}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	if r.refresh(t.Context(), mediaItems()[:1], dirFiles(t.TempDir())) {
		t.Fatal("a lookup whose image was not saved reported success")
	}
	if records, _ := store.Metadata(t.Context()); records["movie"].OnlineKey != "" {
		t.Fatal("movie stored without its poster")
	}
	if err := os.Remove(images); err != nil {
		t.Fatal(err)
	}
	if !r.refresh(t.Context(), mediaItems()[:1], dirFiles(t.TempDir())) || published["movie"].Images["Primary"].File == "" {
		t.Fatalf("movie: %+v", published["movie"])
	}
}

func TestRunRetriesStoppedLookup(t *testing.T) {
	store, fake := newRefreshTest(t)
	shorten(t)
	fake.set(func(f *fakeProviders) { f.down = true })
	published := make(chan Library, 100)
	r, err := NewRefresher(store, Config{TMDbKey: "tmdb-key", Language: "en", ImageDir: t.TempDir()}, func(l Library) { published <- l })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	r.Update(&media.Catalog{Items: mediaItems()[:1]})
	deadline := time.After(10 * time.Second)
	for fake.count("/tmdb/search/movie") < 2 {
		select {
		case <-published:
		case <-deadline:
			t.Fatal("a failed lookup was not retried")
		case <-time.After(5 * time.Millisecond):
		}
	}
	fake.set(func(f *fakeProviders) { f.down = false })
	for {
		select {
		case l := <-published:
			if nameOf(l, "movie") == "Online Movie" {
				return
			}
		case <-deadline:
			t.Fatal("the retried lookup did not finish")
		}
	}
}

func TestRunKeepsLookupOnUnchangedRescan(t *testing.T) {
	store, fake := newRefreshTest(t)
	shorten(t)
	fake.set(func(f *fakeProviders) { f.delay = 30 * time.Millisecond })
	published := make(chan Library, 100)
	r, err := NewRefresher(store, Config{TMDbKey: "tmdb-key", Language: "en", ImageDir: t.TempDir()}, func(l Library) { published <- l })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	defer func() {
		cancel()
		<-done
	}()
	items := mediaItems()[:1]
	r.Update(&media.Catalog{Items: items})
	// Rescans that find the same items while the movie's poster downloads.
	for fake.count("/tmdb/movie/1") == 0 {
		time.Sleep(time.Millisecond)
	}
	for range 3 {
		r.Update(&media.Catalog{Items: slices.Clone(items)})
		time.Sleep(10 * time.Millisecond)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case l := <-published:
			if nameOf(l, "movie") != "Online Movie" {
				continue
			}
			if n := fake.count("/tmdb/movie/1"); n != 1 {
				t.Fatalf("movie fetched %d times: an unchanged rescan restarted the lookup", n)
			}
			return
		case <-deadline:
			t.Fatal("lookup did not finish")
		}
	}
}

// nameOf is the item's published name, or "" while it has no record.
func nameOf(l Library, id string) string {
	if info := l[id]; info != nil {
		return info.Name
	}
	return ""
}

func TestProviderSeasons(t *testing.T) {
	_, fake := newRefreshTest(t)
	// Without a translation in the metadata language, TheTVDB is asked only
	// for the short series record, once.
	tv := newTVDb("tvdb-key", languages["ru"])
	info, images, err := tv.season(t.Context(), "7", 1)
	if err != nil || info.Name != "" || info.Overview != "" || info.Year != 2008 || images["Primary"] == "" {
		t.Fatalf("tvdb season: %+v, %v, %v", info, images, err)
	}
	if _, _, err := tv.season(t.Context(), "7", 2); !errors.Is(err, errNotFound) {
		t.Fatalf("tvdb missing season: %v", err)
	}
	uris := fake.requestURIs()
	if !slices.Contains(uris, "/tvdb/series/7/extended?short=true") || fake.count("/tvdb/series/7/extended") != 1 ||
		slices.ContainsFunc(uris, func(uri string) bool { return strings.HasPrefix(uri, "/tvdb/seasons/") }) {
		t.Fatalf("tvdb requests: %v", uris)
	}

	tm := newTMDb("tmdb-key", languages["en"])
	info, images, err = tm.season(t.Context(), "9", 1)
	if err != nil || info.Name != "Season 1" || info.Overview != "TMDb season overview." || info.Premiere.Format(time.DateOnly) != "2008-01-21" ||
		images["Primary"] == "" {
		t.Fatalf("tmdb season: %+v, %v, %v", info, images, err)
	}
	// A season TMDb does not have is not found, for the season and its
	// episodes, with one request.
	if _, _, err := tm.season(t.Context(), "9", 2); !errors.Is(err, errNotFound) {
		t.Fatalf("tmdb missing season: %v", err)
	}
	if _, _, err := tm.episode(t.Context(), "9", 2, 1); !errors.Is(err, errNotFound) || fake.count("/tmdb/tv/9/season/2") != 1 {
		t.Fatalf("tmdb missing season's episode: %v, %d requests", err, fake.count("/tmdb/tv/9/season/2"))
	}
}
