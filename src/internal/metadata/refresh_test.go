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
	"strconv"
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

// ImageInfos reports a Primary image when the item's folder has cover.jpg.
func (d dirFiles) ImageInfos(item media.Item) map[string]media.Image {
	if _, err := os.Stat(filepath.Join(string(d), item.Path, "cover.jpg")); err == nil {
		return map[string]media.Image{"Primary": {}}
	}
	return map[string]media.Image{}
}

// fakeProviders serves the TMDb, TheTVDB and image requests a refresh makes
// and records their paths.
type fakeProviders struct {
	mu    sync.Mutex
	paths []string
	// uris holds the paths with their queries.
	uris []string
	// down answers 500 to every request, audioDBDown to TheAudioDB's;
	// limited answers that many 429s first; delay slows every answer.
	down, audioDBDown bool
	limited           int
	delay             time.Duration
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
	down, limited, delay := f.down || (f.audioDBDown && strings.HasPrefix(r.URL.Path, "/audiodb/")), f.limited > 0, f.delay
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
	if body, found := audioDBAnswer(r); found {
		_, _ = w.Write([]byte(body))
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
			"imdb_id":"tt0000001","belongs_to_collection":{"id":5,"name":"Online Collection"},"credits":{"cast":[{"id":11,"name":"Actor One","character":"Hero","profile_path":"/img/actor.png"},{"id":97,"name":"Same Name"}],"crew":[{"name":"Dir One","job":"Director","department":"Directing"}]},
			"keywords":{"keywords":[{"name":"heist"}]},"release_dates":{"results":[{"iso_3166_1":"US","release_dates":[{"certification":"PG"}]}]}}`,
		"/tmdb/movie/55":      `{"title":"TMDb Title","overview":"Overview of 55.","release_date":"2001-01-01","genres":[{"name":"Action"}]}`,
		"/tmdb/tv/9":          `{"name":"TMDb Show","overview":"TMDb overview.","vote_average":8.1,"networks":[{"name":"Net"}],"backdrop_path":"/img/show-backdrop.png"}`,
		"/tmdb/tv/9/season/1": `{"id":90,"name":"Season 1","overview":"TMDb season overview.","air_date":"2008-01-21","poster_path":"/img/season.png","episodes":[{"episode_number":2,"name":"TMDb Episode","overview":"TMDb episode overview.","air_date":"2008-02-01","vote_average":8.5,"guest_stars":[{"id":98,"name":"Same Name"}]}]}`,
		"/tmdb/person/11":     `{"biography":"Biography from TMDb.","birthday":"1970-02-03","profile_path":"/img/person.png"}`,
		"/tvdb/people/22/extended": `{"data":{"image":"/img/people.png","birth":"1960-04-05",
			"biographies":[{"biography":"Biographie.","language":"fra"},{"biography":"Biography from TheTVDB.","language":"eng"}]}}`,
		"/tvdb/login":  `{"data":{"token":"tvdb-token"}}`,
		"/tvdb/search": `{"data":[{"tvdb_id":"7"}]}`,
		"/tvdb/series/7/extended": `{"data":{"name":"Original Show","overview":"Original overview.","firstAired":"2008-01-20","image":"/img/show.png",
			"genres":[{"name":"Drama"}],"characters":[{"name":"Walter","personName":"Actor Two","peopleType":"Actor","peopleId":22}],
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

// audioDBAnswer answers TheAudioDB's searches by their query, as it does:
// an empty list is null.
func audioDBAnswer(r *http.Request) (string, bool) {
	query := r.URL.Query()
	host := strings.TrimSuffix(tmdbImages, "/t/p/")
	switch r.URL.Path {
	case "/audiodb/audiodb-key/search.php":
		switch query.Get("s") {
		case "Artist A":
			return `{"artists":[{"idArtist":"7","strArtist":"Artist A","strBiographyEN":"Biography.","strBiographyRU":"Биография.",
				"strArtistThumb":"` + host + `/img/artist.png","strArtistFanart":"` + host + `/img/fanart.png","strMusicBrainzID":"mb-a"}]}`, true
		case "Guest":
			return `{"artists":[{"idArtist":"8","strArtist":"The Guest Band","strArtistAlternate":"Guest, G","strBiography":"Guest biography.","strArtistThumb":null}]}`, true
		}
		return `{"artists":null}`, true
	case "/audiodb/audiodb-key/searchalbum.php":
		if query.Get("s") == "Artist A" && query.Get("a") == "Album One" {
			return `{"album":[{"idAlbum":"19","strAlbum":"Album One","intYearReleased":"1999","strDescriptionEN":"Original release."},
				{"idAlbum":"20","strAlbum":"Album One","intYearReleased":"2001","strDescriptionEN":"Description.","strDescriptionFR":"Description française.",
				"strAlbumThumb":"` + host + `/img/album.png","strMusicBrainzID":"mb-rg"}]}`, true
		}
		if query.Get("s") == "Guest" && query.Get("a") == "Covered" {
			return `{"album":[{"idAlbum":"21","strAlbum":"Covered","strDescriptionEN":"Covered album.","strAlbumThumb":"` + host + `/img/covered.png"}]}`, true
		}
		return `{"album":null}`, true
	}
	return "", false
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
	saved := []any{tmdbAPI, tmdbImages, tvdbAPI, tvdbArtwork, audioDBAPI, httpClient, spacing.audiodb}
	tmdbAPI, tmdbImages, tvdbAPI, tvdbArtwork, httpClient = server.URL+"/tmdb", server.URL+"/t/p/", server.URL+"/tvdb", server.URL, server.Client()
	audioDBAPI, spacing.audiodb = server.URL+"/audiodb/", time.Millisecond
	t.Cleanup(func() {
		tmdbAPI, tmdbImages, tvdbAPI, tvdbArtwork, audioDBAPI = saved[0].(string), saved[1].(string), saved[2].(string), saved[3].(string), saved[4].(string)
		httpClient, spacing.audiodb = saved[5].(*http.Client), saved[6].(time.Duration)
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
		len(movie.People) != 3 || movie.ProviderIDs["Imdb"] != "tt0000001" ||
		movie.Set != "Online Collection" || movie.SetTmdbID != "5" || movie.Pending {
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

func TestRefreshPeople(t *testing.T) {
	store, fake := newRefreshTest(t)
	dir := t.TempDir()
	host := strings.TrimSuffix(tmdbImages, "/t/p/")
	// Actor One is credited again here, without IDs; Local Actor has only
	// the .nfo's photo; Actor Two's http photo is not used.
	nfo := `<movie><title>Other</title><uniqueid type="tmdb">55</uniqueid>
		<actor><name>Actor One</name></actor>
		<actor><name>Local Actor</name><thumb>` + host + `/img/local.png</thumb></actor>
		<actor><name>Actor Two</name><thumb>http://example.test/two.jpg</thumb></actor>
		<actor><name>Nobody</name></actor></movie>`
	if err := os.MkdirAll(filepath.Join(dir, "Other (2001)"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Other (2001)", "Other (2001).nfo"), []byte(nfo), 0600); err != nil {
		t.Fatal(err)
	}
	items := mediaItems()
	images := t.TempDir()
	var published Library
	r, err := NewRefresher(store, Config{TMDbKey: "tmdb-key", TVDbKey: "tvdb-key", Language: "en", ImageDir: images}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	r.refresh(t.Context(), items, dirFiles(dir))

	// TMDb fills the biography; the credit's photo wins over the person's.
	one := published[ID("Person", "Actor One")]
	if one == nil || one.Overview != "Biography from TMDb." || one.Premiere.Format(time.DateOnly) != "1970-02-03" ||
		one.ProviderIDs["Tmdb"] != "11" || !strings.HasSuffix(one.Images["Primary"].URL, "/img/actor.png") {
		t.Fatalf("Actor One: %+v", one)
	}
	if _, err := os.Stat(filepath.Join(images, one.Images["Primary"].File)); err != nil {
		t.Fatal(err)
	}
	// TheTVDB's biography is taken in the metadata language, else English,
	// with its photo when no credit has an https one.
	two := published[ID("Person", "Actor Two")]
	if two == nil || two.Overview != "Biography from TheTVDB." || two.Year != 1960 || !strings.HasSuffix(two.Images["Primary"].URL, "/img/people.png") {
		t.Fatalf("Actor Two: %+v", two)
	}
	local := published[ID("Person", "Local Actor")]
	if local == nil || local.Overview != "" || !strings.HasSuffix(local.Images["Primary"].URL, "/img/local.png") {
		t.Fatalf("Local Actor: %+v", local)
	}
	for _, name := range []string{"Nobody", "Same Name"} {
		if _, found := published[ID("Person", name)]; found {
			t.Fatalf("%s was looked up", name)
		}
	}
	if n := fake.count("/tmdb/person/11"); n != 1 {
		t.Fatalf("Actor One fetched %d times, want once", n)
	}

	// An unchanged refresh asks nobody; refreshing an item asks for its
	// people again.
	before := len(fake.requests())
	r.refresh(t.Context(), items, dirFiles(dir))
	if len(fake.requests()) != before {
		t.Fatalf("unchanged refresh made %d requests", len(fake.requests())-before)
	}
	r.Refresh([]string{"movie"})
	r.refresh(t.Context(), items, dirFiles(dir))
	if n := fake.count("/tmdb/person/11"); n != 2 {
		t.Fatalf("Actor One fetched %d times after a forced refresh, want twice", n)
	}
	if n := fake.count("/tvdb/people/22/extended"); n != 1 {
		t.Fatalf("Actor Two fetched %d times, want once", n)
	}

	// Without the series, Actor Two's credit gives no ID or https photo,
	// so the record and photo go.
	photo := filepath.Join(images, two.Images["Primary"].File)
	r.refresh(t.Context(), items[:2], dirFiles(dir))
	if _, found := published[ID("Person", "Actor Two")]; found {
		t.Fatal("uncredited person still published")
	}
	if _, err := os.Stat(photo); !os.IsNotExist(err) {
		t.Fatalf("unused photo kept: %v", err)
	}
}

func TestRefreshMusic(t *testing.T) {
	store, fake := newRefreshTest(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Artist A", "Covered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Artist A", "Covered", "cover.jpg"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	items := []media.Item{
		{ID: "album", Name: "Album One", Kind: "MusicAlbum", Path: "Artist A/Album One",
			Tags: media.Tags{Album: "Album One", AlbumArtist: "Artist A", Artists: []string{"Artist A", "Featured"}, Year: 2001, Genres: []string{"Rock"}}},
		{ID: "covered", Name: "Covered", Kind: "MusicAlbum", Path: "Artist A/Covered", Tags: media.Tags{Album: "Covered", AlbumArtist: "Guest", Artists: []string{"Guest"}}},
		{ID: "track", Name: "Song", Kind: "Audio", Path: "Artist A/Album One/01 Song.mp3", ParentID: "album",
			Tags: media.Tags{AlbumArtist: "Artist A", Artists: []string{"Artist A", "Featured"}}},
		{ID: "single", Name: "Single", Kind: "Audio", Path: "Single.mp3", Tags: media.Tags{AlbumArtist: "Solo", Artists: []string{"Solo"}}},
	}
	images := t.TempDir()
	var published Library
	r, err := NewRefresher(store, Config{AudioDBKey: "audiodb-key", Language: "fr", ImageDir: images}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	if !r.refresh(t.Context(), items, dirFiles(dir)) {
		t.Fatal("refresh did not finish")
	}

	// The release of the tagged year wins; its description is in the
	// metadata language, its year and genres stay the tags'.
	album := published["album"]
	if album == nil || album.Overview != "Description française." || album.Year != 2001 || strings.Join(album.Genres, ",") != "Rock" ||
		album.ProviderIDs["AudioDbAlbum"] != "20" || album.ProviderIDs["MusicBrainzReleaseGroup"] != "mb-rg" ||
		!strings.HasSuffix(album.Images["Primary"].URL, "/img/album.png") {
		t.Fatalf("album: %+v", album)
	}
	if _, err := os.Stat(filepath.Join(images, album.Images["Primary"].File)); err != nil {
		t.Fatal(err)
	}
	// A local cover wins: the provider's is not downloaded.
	if covered := published["covered"]; covered == nil || covered.Overview != "Covered album." || len(covered.Images) != 0 || fake.count("/img/covered.png") != 0 {
		t.Fatalf("covered: %+v", covered)
	}
	// Without a biography in the language, the English one is taken.
	artist := published[ID("MusicArtist", "Artist A")]
	if artist == nil || artist.Overview != "Biography." || artist.ProviderIDs["AudioDbArtist"] != "7" || artist.ProviderIDs["MusicBrainzArtist"] != "mb-a" ||
		!strings.HasSuffix(artist.Images["Primary"].URL, "/img/artist.png") || !strings.HasSuffix(artist.Images["Backdrop"].URL, "/img/fanart.png") {
		t.Fatalf("artist: %+v", artist)
	}
	// An alternate name matches and a biography under the bare field name is
	// taken; an artist TheAudioDB does not know has no metadata; tracks and
	// track artists are not looked up.
	if guest := published[ID("MusicArtist", "Guest")]; guest == nil || guest.Overview != "Guest biography." || len(guest.Images) != 0 {
		t.Fatalf("guest: %+v", guest)
	}
	if solo := published[ID("MusicArtist", "Solo")]; solo == nil || solo.Overview != "" {
		t.Fatalf("solo: %+v", solo)
	}
	if _, found := published["track"]; found {
		t.Fatal("track was looked up")
	}
	if _, found := published[ID("MusicArtist", "Featured")]; found {
		t.Fatal("track artist was looked up")
	}
	if n := fake.count("/audiodb/audiodb-key/search.php"); n != 3 {
		t.Fatalf("%d artist searches, want one per album artist", n)
	}

	// An unchanged refresh asks nobody; refreshing an album asks for it and
	// its artists again.
	before := len(fake.requests())
	r.refresh(t.Context(), items, dirFiles(dir))
	if len(fake.requests()) != before {
		t.Fatalf("unchanged refresh made %d requests", len(fake.requests())-before)
	}
	r.Refresh([]string{"covered"})
	r.refresh(t.Context(), items, dirFiles(dir))
	if albums, artists := fake.count("/audiodb/audiodb-key/searchalbum.php"), fake.count("/audiodb/audiodb-key/search.php"); albums != 3 || artists != 4 {
		t.Fatalf("after refreshing an album: %d album and %d artist searches, want 3 and 4", albums, artists)
	}
	// A new year tag looks the album up again, for that year's release.
	items[0].Tags.Year = 1999
	r.refresh(t.Context(), items, dirFiles(dir))
	if album := published["album"]; album == nil || album.Overview != "Original release." || album.ProviderIDs["AudioDbAlbum"] != "19" {
		t.Fatalf("album retagged 1999: %+v", album)
	}

	// Without the key nothing is sent and the albums' and artists' metadata
	// goes: the music index keeps their tags.
	offline, err := NewRefresher(store, Config{TMDbKey: "tmdb-key", Language: "fr", ImageDir: images}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	before = len(fake.requests())
	offline.refresh(t.Context(), items, dirFiles(dir))
	if len(fake.requests()) != before {
		t.Fatalf("refresh without a key made %d requests", len(fake.requests())-before)
	}
	if album, found := published["album"]; found {
		t.Fatalf("album metadata kept without a key: %+v", album)
	}
	if _, found := published[ID("MusicArtist", "Artist A")]; found {
		t.Fatal("artist metadata kept without a key")
	}
}

// TheAudioDB failing stops neither the movie nor the people lookups.
func TestRefreshMusicDown(t *testing.T) {
	store, fake := newRefreshTest(t)
	fake.set(func(f *fakeProviders) { f.audioDBDown = true })
	items := mediaItems()[:1]
	for n := range maxFailures + 1 {
		name := "Album " + strconv.Itoa(n)
		items = append(items, media.Item{ID: name, Name: name, Kind: "MusicAlbum", Path: name, Tags: media.Tags{AlbumArtist: "Artist A"}})
	}
	var published Library
	r, err := NewRefresher(store, Config{TMDbKey: "tmdb-key", AudioDBKey: "audiodb-key", Language: "en", ImageDir: t.TempDir()}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	if r.refresh(t.Context(), items, dirFiles(t.TempDir())) {
		t.Fatal("refresh with TheAudioDB down reported success")
	}
	if published["movie"].Name != "Online Movie" || published[ID("Person", "Actor One")].Overview != "Biography from TMDb." {
		t.Fatalf("movie: %+v, person: %+v", published["movie"], published[ID("Person", "Actor One")])
	}
	if n := fake.count("/audiodb/audiodb-key/searchalbum.php"); n != maxFailures {
		t.Fatalf("%d album searches, want %d before music stops", n, maxFailures)
	}
}

// A TheAudioDB key alone looks up music only.
func TestRefreshMusicKeyOnly(t *testing.T) {
	store, fake := newRefreshTest(t)
	var published Library
	r, err := NewRefresher(store, Config{AudioDBKey: "audiodb-key", Language: "en", ImageDir: t.TempDir()}, func(l Library) { published = l })
	if err != nil {
		t.Fatal(err)
	}
	if !r.refresh(t.Context(), mediaItems(), dirFiles(t.TempDir())) {
		t.Fatal("refresh did not finish")
	}
	if n := len(fake.requests()); n != 0 {
		t.Fatalf("%d requests for video items", n)
	}
	for id, info := range published {
		if info.Pending {
			t.Fatalf("%s is pending without a video provider", id)
		}
	}
	if _, err := r.Search(t.Context(), "Movie", "The Movie", 0, nil); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("Search: %v", err)
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
	// Items not looked up yet wait for it, so they leave no movie set.
	if movie := published["movie"]; movie == nil || !movie.Pending {
		t.Fatalf("movie not pending: %+v", movie)
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
