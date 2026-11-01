package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
	"github.com/TokenCemetery/couch/internal/state"
)

func TestMetadataActions(t *testing.T) {
	store, _, _ := newTestServer(t)
	movie := playbackMovie()
	tvID := (&media.Catalog{ID: "movies"}).SeriesLibraryID()
	series := media.Item{ID: "series", Kind: "Series", Name: "Show", ParentID: tvID}
	season := media.Item{ID: "season", Kind: "Season", ParentID: "series", SeriesID: "series", SeasonNumber: 1}
	episode := media.Item{ID: "episode", Kind: "Episode", ParentID: "season", SeriesID: "series", SeasonID: "season", SeasonNumber: 1, EpisodeNumber: 1}
	catalog := &media.Catalog{ID: "movies", Items: []media.Item{movie, episode}, Folders: []media.Item{series, season}}
	api := New(store, "test", nil, catalog)
	t.Cleanup(api.Close)
	refresher, err := metadata.NewRefresher(store, metadata.Config{Language: "en"}, api.SetMetadata)
	if err != nil {
		t.Fatal(err)
	}
	api.SetRefresher(refresher)
	refresher.Update(catalog)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go refresher.Run(ctx)
	h := api.Handler()
	admin := login(t, h)

	identified := func(want string) {
		t.Helper()
		for range 100 {
			records, err := store.Metadata(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if string(records["movie"].Identified) == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("identification is not %q", want)
	}
	// Identify stores the chosen IDs, and Remove Identification drops them.
	expectStatus(t, request(h, "POST", "/Items/RemoteSearch/Apply/movie?ReplaceAllImages=true", "application/json",
		`{"Name":"Online Movie","ProviderIds":{"Tmdb":"1","Other":"x"},"SearchProviderName":"TheMovieDb"}`, admin), 204)
	identified(`{"Tmdb":"1"}`)
	expectStatus(t, request(h, "POST", "/Items/Metadata/Reset", "application/json", `{"ItemIds":"movie"}`, admin), 204)
	identified("")
	expectStatus(t, request(h, "POST", "/Items/RemoteSearch/Apply/movie", "application/json", `{"ProviderIds":{}}`, admin), 400)
	expectStatus(t, request(h, "POST", "/Items/RemoteSearch/Apply/episode", "application/json", `{"ProviderIds":{"Tmdb":"1"}}`, admin), 400)

	// Refreshing a series covers its seasons and episodes; a library, its
	// items.
	if ids, ok := api.refreshScope("series"); !ok || !slices.Equal(ids, []string{"series", "season", "episode"}) {
		t.Fatalf("series scope %v", ids)
	}
	if ids, ok := api.refreshScope("movies"); !ok || !slices.Equal(ids, []string{"movie"}) {
		t.Fatalf("library scope %v", ids)
	}
	expectStatus(t, request(h, "POST", "/Items/series/Refresh?Recursive=true&MetadataRefreshMode=FullRefresh", "", "", admin), 204)
	expectStatus(t, request(h, "POST", "/Items/missing/Refresh", "", "", admin), 404)

	// Without a provider key the Identify dialog is told so, in the only
	// place it shows: a result, which cannot be applied.
	var results []struct {
		Name        string
		ProviderIds map[string]string
	}
	decodeBody(t, request(h, "POST", "/Items/RemoteSearch/Movie", "application/json", `{"SearchInfo":{"Name":"Movie"},"ItemId":"movie"}`, admin), &results)
	if len(results) != 1 || !strings.Contains(results[0].Name, "No online metadata provider") || !strings.Contains(results[0].Name, "COUCH_TMDB_API_KEY") || len(results[0].ProviderIds) != 0 {
		t.Fatalf("search without keys: %+v", results)
	}
	// Only the providers' image hosts are fetched.
	expectStatus(t, request(h, "GET", "/Items/RemoteSearch/Image?ProviderName=TheMovieDb&imageUrl="+url.QueryEscape("http://127.0.0.1/secret"), "", "", admin), 404)

	var infos []struct{ Key string }
	decodeBody(t, request(h, "GET", "/Items/series/ExternalIdInfos?IsSupportedAsIdentifier=true", "", "", admin), &infos)
	if len(infos) != 3 || infos[0].Key != "Tvdb" {
		t.Fatalf("series IDs %+v", infos)
	}
	var editor struct{ ExternalIdInfos []struct{ Key string } }
	decodeBody(t, request(h, "GET", "/Items/movie/MetadataEditor", "", "", admin), &editor)
	if len(editor.ExternalIdInfos) != 2 {
		t.Fatalf("editor %+v", editor)
	}
	if w := request(h, "POST", "/Items/movie", "application/json", `{"Name":"Edited"}`, admin); w.Code != 400 || !strings.Contains(w.Body.String(), ".nfo") {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}

	// None of it is open to a user who is not an administrator.
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
	decodeBody(t, w, &signedIn)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/Items/movie/Refresh", ""},
		{"GET", "/Items/movie/ExternalIdInfos", ""},
		{"POST", "/Items/RemoteSearch/Movie", `{"SearchInfo":{"Name":"Movie"}}`},
		{"POST", "/Items/RemoteSearch/Series", `{"SearchInfo":{"Name":"Show"}}`},
		{"GET", "/Items/RemoteSearch/Image?imageUrl=x", ""},
		{"POST", "/Items/RemoteSearch/Apply/movie", `{"ProviderIds":{"Tmdb":"1"}}`},
		{"POST", "/Items/Metadata/Reset", `{"ItemIds":"movie"}`},
		{"GET", "/Items/movie/MetadataEditor", ""},
		{"POST", "/Items/movie", `{}`},
	} {
		contentType := ""
		if tc.body != "" {
			contentType = "application/json"
		}
		if w := request(h, tc.method, tc.path, contentType, tc.body, signedIn.AccessToken); w.Code != 403 {
			t.Errorf("guest %s %s: %d", tc.method, tc.path, w.Code)
		}
	}
}

// decodeBody expects a 200 answer and decodes its JSON body into v.
func decodeBody(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	expectStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatal(err)
	}
}
