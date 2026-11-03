package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/hls"
	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
	"github.com/TokenCemetery/couch/internal/state"
)

func TestLibraryAccess(t *testing.T) {
	store, _, dir := newTestServer(t)
	movie := playbackMovie()
	tvID := (&media.Catalog{ID: "movies"}).SeriesLibraryID()
	series := media.Item{ID: "series", Kind: "Series", Name: "Show", ParentID: tvID}
	season := media.Item{ID: "season", Kind: "Season", ParentID: "series", SeriesID: "series", SeasonNumber: 1}
	episode := playbackMovie()
	episode.ID, episode.Kind, episode.Container = "episode", "Episode", "mkv"
	episode.ParentID, episode.SeriesID, episode.SeasonID, episode.SeasonNumber, episode.EpisodeNumber = "season", "series", "season", 1, 1
	api := New(store, "test", nil, &media.Catalog{ID: "movies", Items: []media.Item{movie, episode}, Folders: []media.Item{series, season}})
	t.Cleanup(api.Close)
	api.SetMetadata(metadata.Library{
		"movie":   {Genres: []string{"Drama"}, People: []metadata.Person{{Name: "Movie Star", Type: "Actor"}, {Name: "Both", Type: "Director"}}},
		"episode": {Genres: []string{"Comedy"}, People: []metadata.Person{{Name: "Both", Type: "Actor"}}},
	})
	m, err := hls.NewManager("ffmpeg", "ffprobe", filepath.Join(dir, "transcode"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	api.SetHLS(m)
	h := api.Handler()
	decode := func(body []byte, v any) {
		t.Helper()
		if err := json.Unmarshal(body, v); err != nil {
			t.Fatal(err)
		}
	}
	admin := login(t, h)
	guest, err := store.CreateUser("guest", state.Policy{EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPassword(admin, guest.ID, "guest-test-password"); err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", "/Users/AuthenticateByName?X-Emby-Client=Test&X-Emby-Device-Id=guest&X-Emby-Device-Name=guest", "application/x-www-form-urlencoded",
		url.Values{"Username": {"guest"}, "Pw": {"guest-test-password"}}.Encode(), "")
	var signedIn struct{ AccessToken string }
	_ = json.Unmarshal(w.Body.Bytes(), &signedIn)
	user := signedIn.AccessToken

	// The administrator limits the guest to the TV library. The Access tab
	// names it by its Guid, and the policy reports it so; the library ID
	// is accepted too. The guest keeps their session.
	expectStatus(t, request(h, "POST", "/Users/"+guest.ID+"/Policy", "application/json",
		`{"EnableAllFolders":false,"EnabledFolders":["`+derive("guid", tvID)+`","`+tvID+`"]}`, admin), 204)
	var read struct {
		Policy struct {
			EnableAllFolders bool
			EnabledFolders   []string
		}
	}
	decode(request(h, "GET", "/Users/"+guest.ID, "", "", admin).Body.Bytes(), &read)
	if read.Policy.EnableAllFolders || !slices.Equal(read.Policy.EnabledFolders, []string{derive("guid", tvID)}) || !slices.Equal(store.Snapshot().Users[guest.ID].Policy.EnabledFolders, []string{tvID}) {
		t.Fatalf("policy %+v", read.Policy)
	}

	ids := func(path, token string) []string {
		t.Helper()
		w := request(h, "GET", path, "", "", token)
		expectStatus(t, w, 200)
		// Latest answers with a bare list, the others with a page.
		var page struct{ Items []struct{ Id string } }
		if strings.HasPrefix(w.Body.String(), "[") {
			decode(w.Body.Bytes(), &page.Items)
		} else {
			decode(w.Body.Bytes(), &page)
		}
		found := []string{}
		for _, item := range page.Items {
			found = append(found, item.Id)
		}
		slices.Sort(found)
		return found
	}
	u := "/Users/" + guest.ID
	for _, tc := range []struct {
		path string
		want []string
	}{
		{u + "/Views", []string{tvID}},
		{u + "/Items?Recursive=true", []string{"episode", "season", "series"}},
		{u + "/Items?Ids=movie,episode", []string{"episode"}},
		{u + "/Items/Latest?ParentId=movies", []string{}},
		{u + "/Sections/smalllibrarytiles/Items", []string{tvID}},
		{"/Containers?UserId=" + guest.ID, []string{"mkv"}},
		{"/Genres?UserId=" + guest.ID, []string{metadata.ID("Genre", "Comedy")}},
		{"/Shows/series/Episodes?UserId=" + guest.ID, []string{"episode"}},
	} {
		if got := ids(tc.path, user); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.path, got, tc.want)
		}
	}
	var root struct{ ChildCount int }
	decode(request(h, "GET", u+"/Items/Root", "", "", user).Body.Bytes(), &root)
	var sections []struct{ Id string }
	decode(request(h, "GET", u+"/HomeSections", "", "", user).Body.Bytes(), &sections)
	if root.ChildCount != 1 || len(sections) != 3 || sections[2].Id != "latestmedia_"+tvID {
		t.Fatalf("root %d, sections %+v", root.ChildCount, sections)
	}

	// Every route answers 404 for the movie, as for a missing item, and
	// serves the episode.
	const notFound = "The requested item was not found."
	hidden := []struct{ method, path, body string }{
		{"GET", u + "/Items/movie", ""},
		{"GET", u + "/Items/movies", ""},
		{"GET", "/Items/" + metadata.ID("Genre", "Drama"), ""},
		{"GET", "/Persons/" + metadata.ID("Person", "Movie Star") + "/Credits", ""},
		{"GET", "/Items/movie/Similar", ""},
		{"GET", u + "/Sections/latestmedia_movies/Items", ""},
		{"POST", "/Items/movie/PlaybackInfo", "{}"},
		{"GET", "/Videos/movie/stream?Static=true", ""},
		{"GET", "/Items/movie/Download", ""},
		{"GET", "/Videos/movie/mediasource_movie/Subtitles/3/Stream.vtt", ""},
		{"GET", "/Videos/movie/master.m3u8?MediaSourceId=movie&PlaySessionId=p", ""},
		{"GET", "/Items/movie/Images/Primary", ""},
		{"POST", u + "/PlayedItems/movie", ""},
		{"POST", u + "/FavoriteItems/movie", ""},
		{"POST", "/Sessions/Playing", `{"ItemId":"movie"}`},
		{"POST", u + "/SearchedItems", `{"Ids":["movie"],"WasSearched":true}`},
	}
	for _, tc := range hidden {
		contentType := ""
		if tc.body != "" {
			contentType = "application/json"
		}
		if w := request(h, tc.method, tc.path, contentType, tc.body, user); w.Code != 404 || !strings.Contains(w.Body.String(), notFound) {
			t.Errorf("guest %s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		// The administrator still sees everything. The test catalogue has
		// no files, so a route that opens one fails later, differently.
		adminPath := strings.Replace(tc.path, u, "/Users/"+testUser(store).ID, 1)
		if w := request(h, tc.method, adminPath, contentType, tc.body, admin); strings.Contains(w.Body.String(), notFound) && !strings.Contains(tc.path, "/Subtitles/") {
			t.Errorf("admin %s %s: %d %s", tc.method, adminPath, w.Code, w.Body)
		}
	}
	expectStatus(t, request(h, "GET", u+"/Items/episode", "", "", user), 200)
	expectStatus(t, request(h, "GET", "/Items/"+metadata.ID("Genre", "Comedy"), "", "", user), 200)
	// A person's credits list only what the guest can see.
	var credits []struct {
		PersonType string
		Items      []struct{ Name string }
	}
	decode(request(h, "GET", "/Persons/"+metadata.ID("Person", "Both")+"/Credits", "", "", user).Body.Bytes(), &credits)
	if len(credits) != 1 || credits[0].PersonType != "Actor" || len(credits[0].Items) != 1 || credits[0].Items[0].Name != "Show" {
		t.Fatalf("guest credits %+v", credits)
	}
	if got := ids("/Users/"+testUser(store).ID+"/Views", admin); len(got) != 2 {
		t.Fatalf("admin views %v", got)
	}

	// A rescan tells the guest only of changes in their libraries: the
	// first message after a new movie is their own user data.
	server := httptest.NewServer(h)
	defer server.Close()
	_, reader := openEventSocket(t, server, user)
	added := media.Item{ID: "added", Size: 1}
	api.SetCatalog(&media.Catalog{ID: "movies", Items: []media.Item{movie, episode, added}, Folders: []media.Item{series, season}})
	expectStatus(t, request(h, "POST", u+"/FavoriteItems/episode", "", "", user), 200)
	if kind, raw := expectSocketMessage(t, reader); kind != "UserDataChanged" || !strings.Contains(string(raw), `"episode"`) {
		t.Fatalf("first message %s %s", kind, raw)
	}

	// Enabling every library again shows the movie.
	expectStatus(t, request(h, "POST", "/Users/"+guest.ID+"/Policy", "application/json", `{"EnableAllFolders":true}`, admin), 204)
	expectStatus(t, request(h, "GET", u+"/Items/movie", "", "", user), 200)
}
