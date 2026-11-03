package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
)

func TestItemMetadata(t *testing.T) {
	store, _, _ := newTestServer(t)
	catalog := &media.Catalog{ID: "movies", Items: []media.Item{
		{ID: "a", Name: "a.file", Container: "mkv"},
		{ID: "b", Name: "b.file", Container: "mkv"},
		{ID: "c", Name: "c.file", Container: "mkv"},
	}}
	images := t.TempDir()
	var poster bytes.Buffer
	if err := png.Encode(&poster, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "poster.png"), poster.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	api := New(store, "test", nil, catalog)
	api.SetMetadataImages(images)
	library := metadata.Library{
		"a": {Name: "Zulu", OriginalTitle: "Zoulou", Overview: "About a.", Tagline: "Tag.", Year: 2010,
			Premiere: time.Date(2010, 5, 1, 0, 0, 0, 0, time.UTC), Genres: []string{"Drama", "Crime, Noir"}, Studios: []string{"Studio A"},
			Tags: []string{"heist"}, Community: 7.5, Critic: 80, OfficialRating: "PG-13",
			People:      []metadata.Person{{Name: "Ann Actor", Role: "Hero", Type: "Actor"}, {Name: "Dee Director", Role: "Director", Type: "Director"}},
			ProviderIDs: map[string]string{"Tmdb": "1", "Imdb": "tt1"},
			Images:      map[string]metadata.Image{"Primary": {URL: "https://example.test/p.png", File: "poster.png", Width: 2, Height: 3, MIME: "image/png"}}},
		"b": {Name: "Alpha", SortName: "Yankee", Year: 1999, Genres: []string{"Comedy"}, Community: 9,
			People: []metadata.Person{{Name: "Ann Actor", Type: "GuestStar"}}},
	}
	api.SetMetadata(library)
	h := api.Handler()
	token := login(t, h)

	w := request(h, "GET", "/Items/a", "", "", token)
	expectStatus(t, w, 200)
	var dto struct {
		Name, OriginalTitle, SortName, Overview, OfficialRating, PremiereDate string
		Taglines, Genres                                                      []string
		ProductionYear                                                        int
		CommunityRating, CriticRating                                         float64
		// Emby's NameLongIdPair: the ID is a JSON integer (#121).
		GenreItems, Studios, TagItems []struct {
			Name string
			Id   json.Number
		}
		People       []struct{ Name, Id, Role, Type string }
		ProviderIds  map[string]string
		ExternalUrls []struct{ Name, Url string }
		ImageTags    map[string]string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	genreID := metadata.ID("Genre", "Drama")
	personID := metadata.ID("Person", "Ann Actor")
	if dto.Name != "Zulu" || dto.SortName != "Zulu" || dto.OriginalTitle != "Zoulou" || dto.Overview != "About a." || dto.Taglines[0] != "Tag." ||
		dto.ProductionYear != 2010 || dto.PremiereDate != "2010-05-01T00:00:00.0000000Z" || dto.CommunityRating != 7.5 || dto.CriticRating != 80 ||
		dto.OfficialRating != "PG-13" || strings.Join(dto.Genres, "|") != "Drama|Crime, Noir" || dto.GenreItems[0].Id.String() != genreID ||
		dto.Studios[0].Name != "Studio A" || dto.TagItems[0].Name != "heist" || len(dto.People) != 2 || dto.People[0].Id != personID ||
		dto.People[0].Role != "Hero" || dto.ProviderIds["Tmdb"] != "1" || len(dto.ExternalUrls) != 2 || dto.ImageTags["Primary"] == "" {
		t.Fatalf("%s", w.Body.String())
	}
	// The downloaded poster is served with the tag the DTO advertises.
	w = request(h, "GET", "/Items/a/Images/Primary?tag="+url.QueryEscape(dto.ImageTags["Primary"]), "", "", "")
	expectStatus(t, w, 200)
	if !bytes.Equal(w.Body.Bytes(), poster.Bytes()) || w.Header().Get("Content-Type") != "image/png" {
		t.Fatal("poster not served")
	}

	ids := func(path string) string {
		t.Helper()
		w := request(h, "GET", path, "", "", token)
		expectStatus(t, w, 200)
		var page struct{ Items []struct{ Id, Name string } }
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		values := []string{}
		for _, item := range page.Items {
			values = append(values, item.Id)
		}
		return strings.Join(values, ",")
	}
	for _, tc := range []struct{ path, ids string }{
		// Names sort by SortName, then the title, then the file name.
		{"/Items?Recursive=true&SortBy=SortName", "c,b,a"},
		{"/Items?Recursive=true&SortBy=ProductionYear,SortName&SortOrder=Descending,Ascending", "a,b,c"},
		{"/Items?Recursive=true&SortBy=CommunityRating&SortOrder=Descending", "b,a,c"},
		{"/Items?Recursive=true&SortBy=Director,SortName", "c,b,a"},
		{"/Items?Recursive=true&SearchTerm=zoulou", "a"},
		{"/Items?Recursive=true&SearchTerm=alpha", "b"},
		{"/Items?Recursive=true&SearchTerm=c.file", "c"},
		{"/Items?Recursive=true&Genres=" + url.QueryEscape("Crime, Noir|Comedy"), "b,a"},
		{"/Items?Recursive=true&GenreIds=" + genreID, "a"},
		{"/Items?Recursive=true&StudioIds=" + metadata.ID("Studio", "Studio A"), "a"},
		{"/Items?Recursive=true&TagIds=" + metadata.ID("Tag", "heist"), "a"},
		{"/Items?Recursive=true&Years=1999,2011", "b"},
		{"/Items?Recursive=true&OfficialRatings=PG-13", "a"},
		{"/Items?Recursive=true&PersonIds=" + personID, "b,a"},
		{"/Items?Recursive=true&PersonIds=" + personID + "&PersonTypes=Actor", "a"},
		{"/Items?Recursive=true&MinPremiereDate=2000-01-01T00:00:00Z", "a"},
		{"/Items?Recursive=true&IsUnaired=true", ""},
		// The library filter dialog's value lists.
		{"/Genres?ParentId=movies", metadata.ID("Genre", "Comedy") + "," + metadata.ID("Genre", "Crime, Noir") + "," + genreID},
		{"/Years?SortOrder=Descending", "2010,1999"},
		{"/OfficialRatings", "PG-13"},
		{"/Studios?Limit=1", metadata.ID("Studio", "Studio A")},
		{"/Persons?PersonTypes=Director", metadata.ID("Person", "Dee Director")},
		{"/Persons?Filters=IsFavorite", ""},
		{"/Genres?IncludeItemTypes=Series", ""},
	} {
		if got := ids(tc.path); got != tc.ids {
			t.Errorf("%s: %s, want %s", tc.path, got, tc.ids)
		}
	}
	// A titled episode keeps its place among untitled ones.
	series := &media.Catalog{ID: "movies", Items: []media.Item{
		{ID: "e1", Name: "Show.S01E01", Kind: "Episode", SeriesID: "show", SeasonID: "season", ParentID: "season", SeasonNumber: 1, EpisodeNumber: 1},
		{ID: "e2", Name: "Show.S01E02", Kind: "Episode", SeriesID: "show", SeasonID: "season", ParentID: "season", SeasonNumber: 1, EpisodeNumber: 2},
		{ID: "e10", Name: "Show.S01E10", Kind: "Episode", SeriesID: "show", SeasonID: "season", ParentID: "season", SeasonNumber: 1, EpisodeNumber: 10},
	}}
	api.SetCatalog(series)
	api.SetMetadata(metadata.Library{"e1": {Name: "Zebra"}})
	if got := ids("/Items?Recursive=true&IsFolder=false&ParentId=show"); got != "e1,e2,e10" {
		t.Errorf("episodes: %s", got)
	}
	api.SetCatalog(catalog)
	api.SetMetadata(library)
	// A person's, genre's or studio's page opens.
	for id, kind := range map[string]string{personID: "Person", genreID: "Genre"} {
		w := request(h, "GET", "/Items/"+id, "", "", token)
		expectStatus(t, w, 200)
		var page struct{ Type, Name string }
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Type != kind {
			t.Fatalf("%s: %s", kind, w.Body.String())
		}
		expectStatus(t, request(h, "GET", "/Items/"+id+"/ThemeMedia", "", "", token), 200)
	}
}

func TestPersonCredits(t *testing.T) {
	store, _, _ := newTestServer(t)
	episode := func(id string, number int) media.Item {
		return media.Item{ID: id, Name: id, Kind: "Episode", SeriesID: "show", SeasonID: "season", ParentID: "season", SeasonNumber: 1, EpisodeNumber: number}
	}
	api := New(store, "test", nil, &media.Catalog{ID: "movies",
		Items: []media.Item{
			{ID: "old", Name: "old.file"}, {ID: "new", Name: "new.file"}, {ID: "undated", Name: "undated.file"},
			episode("e1", 1), episode("e2", 2),
		},
		Folders: []media.Item{{ID: "show", Name: "Show", Kind: "Series"}, {ID: "season", Kind: "Season", ParentID: "show", SeriesID: "show", SeasonNumber: 1}},
	})
	pat := metadata.Person{Name: "Pat Person", Type: "Actor"}
	api.SetMetadata(metadata.Library{
		"old": {Name: "Old Film", Year: 1999, Overview: "Old.", ProviderIDs: map[string]string{"Tmdb": "9"},
			People: []metadata.Person{{Name: "Pat Person", Role: "Villain", Type: "Actor"}}},
		"new": {Name: "New Film", OriginalTitle: "Nouveau", Premiere: time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC), Year: 2020,
			People: []metadata.Person{{Name: "Pat Person", Type: "Director"}, {Name: "pat person", Role: "Self", Type: "Actor"}}},
		"undated": {People: []metadata.Person{pat}},
		"show":    {Name: "The Show", Year: 2015, People: []metadata.Person{{Name: "Pat Person", Role: "Host", Type: "Actor"}}},
		"e1":      {People: []metadata.Person{{Name: "Pat Person", Role: "Guest", Type: "Actor"}, {Name: "Pat Person", Type: "GuestStar"}}},
		"e2":      {People: []metadata.Person{{Name: "Pat Person", Type: "GuestStar"}}},
	})
	h := api.Handler()
	token := login(t, h)

	w := request(h, "GET", "/Persons/"+metadata.ID("Person", "Pat Person")+"/Credits", "", "", token)
	expectStatus(t, w, 200)
	var credits []struct {
		PersonType string
		Items      []struct {
			Name, OriginalTitle, Type, PersonType, Role, PremiereDate, Overview string
			ProductionYear                                                      int
			ProviderIds                                                         map[string]string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &credits); err != nil {
		t.Fatal(err)
	}
	// Tabs follow Emby's PersonType order; credits are newest first, and
	// episodes credit their series once, with the series' own role.
	got := []string{}
	for _, list := range credits {
		names := []string{}
		for _, item := range list.Items {
			names = append(names, item.Name+"/"+item.Type+"/"+item.Role)
		}
		got = append(got, list.PersonType+": "+strings.Join(names, ", "))
	}
	if want := "Actor: New Film/Movie/Self, The Show/Series/Host, Old Film/Movie/Villain, undated.file/Movie/ | Director: New Film/Movie/ | GuestStar: The Show/Series/"; strings.Join(got, " | ") != want {
		t.Fatalf("credits\n%s\nwant\n%s", strings.Join(got, " | "), want)
	}
	newFilm, oldFilm := credits[0].Items[0], credits[0].Items[2]
	if newFilm.OriginalTitle != "Nouveau" || newFilm.PremiereDate != "2020-03-01T00:00:00.0000000Z" || newFilm.PersonType != "Actor" ||
		oldFilm.ProductionYear != 1999 || oldFilm.Overview != "Old." || oldFilm.ProviderIds["Tmdb"] != "9" || credits[0].Items[1].ProductionYear != 2015 {
		t.Fatalf("%s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ProviderIds":{}`) {
		t.Fatalf("an item without provider IDs needs an empty object: %s", w.Body.String())
	}

	// Only a person has credits.
	expectStatus(t, request(h, "GET", "/Persons/"+metadata.ID("Person", "Nobody")+"/Credits", "", "", token), 404)
	expectStatus(t, request(h, "GET", "/Persons/old/Credits", "", "", token), 404)
}
