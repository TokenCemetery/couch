package server

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
)

// TestHomeSectionTitlesFollowLanguage covers #18: row titles follow
// X-Emby-Language for the chosen languages and fall back to English.
func TestHomeSectionTitlesFollowLanguage(t *testing.T) {
	store, _, _ := newTestServer(t)
	catalog := &media.Catalog{ID: "movies", Items: []media.Item{{ID: "m", Name: "Movie"}}}
	h := New(store, "test", nil, catalog).Handler()
	token := login(t, h)
	path := "/Users/" + testUser(store).ID + "/HomeSections"
	titles := func(query, header string) string {
		r := httptest.NewRequestWithContext(t.Context(), "GET", path+query, nil)
		r.Header.Set("X-Emby-Token", token)
		if header != "" {
			r.Header.Set("X-Emby-Language", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expectStatus(t, w, 200)
		var sections []struct{ Name string }
		if err := json.Unmarshal(w.Body.Bytes(), &sections); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, s := range sections {
			names = append(names, s.Name)
		}
		return strings.Join(names, "|")
	}
	for _, tc := range []struct{ query, header, want string }{
		{"?X-Emby-Language=ru", "", "Мои медиаданные|Продолжение просмотра|Недавно добавленные фильмы"},
		{"?X-Emby-Language=es-MX", "", "Mis Contenidos|Continuar viendo|Ultimas Películas"},
		{"", "de", "Meine Medien|Weiterschauen|Neueste Filme"},
		{"?X-Emby-Language=ja", "", "My Media|Continue Watching|Latest Movies"},
		{"", "", "My Media|Continue Watching|Latest Movies"},
	} {
		if got := titles(tc.query, tc.header); got != tc.want {
			t.Fatalf("%q %q: got %s, want %s", tc.query, tc.header, got, tc.want)
		}
	}
}

// TestHomeSectionsCanBeEdited covers #100: Emby Web's Home Screen settings
// add, edit, move and remove sections, the home screen follows, and section
// types Couch cannot fill are refused with a reason.
func TestHomeSectionsCanBeEdited(t *testing.T) {
	store, _, _ := newTestServer(t)
	tvID := (&media.Catalog{ID: "movies"}).SeriesLibraryID()
	movie := playbackMovie()
	movie.Name = "Movie"
	episode := playbackMovie()
	episode.ID, episode.Kind, episode.Name = "episode", "Episode", "Episode"
	episode.ParentID, episode.SeriesID, episode.SeasonID, episode.SeasonNumber, episode.EpisodeNumber = "season", "series", "season", 1, 1
	catalog := &media.Catalog{ID: "movies", Items: []media.Item{movie, episode}, Folders: []media.Item{
		{ID: "series", Kind: "Series", Name: "Show", ParentID: tvID},
		{ID: "season", Kind: "Season", ParentID: "series", SeriesID: "series", SeasonNumber: 1},
	}}
	h := New(store, "test", nil, catalog).Handler()
	token := login(t, h)
	path := "/Users/" + testUser(store).ID + "/HomeSections"
	type section struct {
		Id, Name, SectionType, CustomName string
		ItemTypes, ExcludedFolders        []string
		Query                             struct{ IsPlayed *bool }
	}
	list := func(query string) []section {
		t.Helper()
		w := request(h, "GET", path+query, "", "", token)
		expectStatus(t, w, 200)
		var sections []section
		if err := json.Unmarshal(w.Body.Bytes(), &sections); err != nil {
			t.Fatal(err)
		}
		return sections
	}
	ids := func(query string) string {
		t.Helper()
		names := []string{}
		for _, s := range list(query) {
			names = append(names, s.Id)
		}
		return strings.Join(names, ",")
	}
	post := func(suffix, body string, status int) string {
		t.Helper()
		w := request(h, "POST", path+suffix, "application/json", body, token)
		if w.Code != status {
			t.Fatalf("POST HomeSections%s %s: %d %q", suffix, body, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	items := func(id string) []string {
		t.Helper()
		w := request(h, "GET", "/Users/"+testUser(store).ID+"/Sections/"+id+"/Items?RandomSeed=42&Limit=10&Fields=PrimaryImageAspectRatio", "", "", token)
		expectStatus(t, w, 200)
		var page struct{ Items []struct{ Id string } }
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		found := []string{}
		for _, item := range page.Items {
			found = append(found, item.Id)
		}
		return found
	}

	// The editor sees Latest Media once; the home screen one row per library.
	if got := ids("?isEditMode=true"); got != "smalllibrarytiles,resume,latestmedia" {
		t.Fatalf("default editor list: %s", got)
	}
	if got := ids(""); got != "smalllibrarytiles,resume,latestmedia_movies,latestmedia_"+tvID {
		t.Fatalf("default home screen: %s", got)
	}

	// Add a Dynamic Media row of unplayed movies, as the editor sends it.
	post("", `{"SectionType":"items","CustomName":"Films","Subtitle":null,"ViewType":null,"DisplayMode":null,"ScrollDirection":null,
		"ImageType":null,"ParentId":null,"SortBy":null,"SortOrder":null,"ItemTypes":["Movie"],"IncludeNextUpInResume":true,"Name":null,
		"ExcludedFolders":[],"Query":{"IsPlayed":false,"GenreIds":[]}}`, 204)
	sections := list("?isEditMode=true")
	added := sections[len(sections)-1]
	if len(sections) != 4 || added.Id == "" || added.Name != "Films" || !slices.Equal(added.ItemTypes, []string{"Movie"}) || added.Query.IsPlayed == nil || *added.Query.IsPlayed {
		t.Fatalf("added: %+v", sections)
	}
	if got := items(added.Id); !slices.Equal(got, []string{"movie"}) {
		t.Fatalf("Dynamic Media items: %v", got)
	}
	expectStatus(t, request(h, "POST", "/Users/"+testUser(store).ID+"/PlayedItems/movie", "", "", token), 200)
	if got := items(added.Id); len(got) != 0 {
		t.Fatalf("played movie still listed: %v", got)
	}

	// Latest Media hides played items until its filter allows them.
	if got := items("latestmedia_movies"); len(got) != 0 {
		t.Fatalf("played movie in Latest Movies: %v", got)
	}
	if editor := list("?isEditMode=true"); editor[2].Query.IsPlayed == nil || *editor[2].Query.IsPlayed {
		t.Fatalf("default Latest Media filter: %+v", editor[2])
	}
	post("", `{"Id":"latestmedia","SectionType":"latestmediablock","ExcludedFolders":[],"Query":{"IsPlayed":null}}`, 204)
	if got := items("latestmedia_movies"); !slices.Equal(got, []string{"movie"}) {
		t.Fatalf("Latest Movies of any play state: %v", got)
	}

	// Edit it: without a custom name it takes the type's title.
	post("", `{"Id":"`+added.Id+`","SectionType":"items","CustomName":null,"ItemTypes":["Movie"],"ExcludedFolders":[],"Query":{}}`, 204)
	if got := list("?isEditMode=true")[3].Name; got != "Dynamic Media" {
		t.Fatalf("renamed: %s", got)
	}

	// Drag it to the top, hide the TV library from Latest Media, and keep a
	// row for the TV display mode only.
	post("/Move", `{"Ids":["`+added.Id+`"],"NewIndex":0}`, 204)
	post("", `{"Id":"latestmedia","SectionType":"latestmediablock","ExcludedFolders":["`+tvID+`"],"Query":{}}`, 204)
	post("", `{"Id":"resume","SectionType":"resume","DisplayMode":"tv","IncludeNextUpInResume":false,"Query":{}}`, 204)
	if got := ids(""); got != added.Id+",smalllibrarytiles,resume,latestmedia_movies" {
		t.Fatalf("edited home screen: %s", got)
	}
	if got := ids("?displayMode=mobile,desktop"); got != added.Id+",smalllibrarytiles,latestmedia_movies" {
		t.Fatalf("home screen outside TV mode: %s", got)
	}
	if got := ids("?isEditMode=true&displayMode=mobile,desktop"); got != added.Id+",smalllibrarytiles,resume,latestmedia" {
		t.Fatalf("editor list: %s", got)
	}
	post("/Move", `{"Ids":["`+added.Id+`"],"NewIndex":99}`, 204)
	if got := ids("?isEditMode=true"); got != "smalllibrarytiles,resume,latestmedia,"+added.Id {
		t.Fatalf("moved past the end: %s", got)
	}

	// Remove two; their rows are gone.
	post("/Delete", `{"Ids":["resume","SmallLibraryTiles"]}`, 204)
	if got := ids("?isEditMode=true"); got != "latestmedia,"+added.Id {
		t.Fatalf("after delete: %s", got)
	}
	expectStatus(t, request(h, "GET", "/Users/"+testUser(store).ID+"/Sections/resume/Items", "", "", token), 404)

	// Refusals say why.
	if got := post("", `{"SectionType":"onnow","Query":{}}`, 400); !strings.Contains(got, "no Live TV") {
		t.Fatalf("On Now: %q", got)
	}
	post("", `{"SectionType":"latestdownloads","Query":{}}`, 400)
	post("", `{"SectionType":"items","ItemTypes":[],"Query":{}}`, 400)
	post("", `{"SectionType":"boxset","ParentId":"missing","Query":{}}`, 400)
	post("", `{"SectionType":"collections","SortBy":"Bogus","Query":{}}`, 400)
	post("", `{"Id":"gone","SectionType":"resume","Query":{}}`, 404)
	post("/Move", `{"Ids":["latestmedia"],"NewIndex":-1}`, 400)
	if got := ids("?isEditMode=true"); got != "latestmedia,"+added.Id {
		t.Fatalf("after refusals: %s", got)
	}
}
