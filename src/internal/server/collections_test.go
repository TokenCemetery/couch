package server

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
	"github.com/TokenCemetery/couch/internal/state"
)

// TestCollections walks an administrator through Emby Web's collection
// requests, then checks what another user sees and may do.
func TestCollections(t *testing.T) {
	store, _, _ := newTestServer(t)
	catalog := &media.Catalog{ID: "movies", Items: []media.Item{
		playbackMovie(),
		{ID: "episode", Kind: "Episode", Name: "Show S01E01", ParentID: "season", SeriesID: "series", SeasonID: "season", SeasonNumber: 1, EpisodeNumber: 1},
	}}
	catalog.Folders = []media.Item{
		{ID: "series", Kind: "Series", Name: "Show", ParentID: catalog.SeriesLibraryID()},
		{ID: "season", Kind: "Season", Name: "Season 1", ParentID: "series", SeriesID: "series", SeasonNumber: 1},
	}
	h := New(store, "test", nil, catalog).Handler()
	token := login(t, h)
	user := testUser(store).ID
	type item struct {
		Id, Name, Type, CollectionType string
		ChildCount                     int
		CanDelete, CanEditItems        bool
		UserData                       struct{ IsFavorite, Played bool }
	}
	list := func(path, token string) []item {
		t.Helper()
		w := request(h, "GET", path, "", "", token)
		expectStatus(t, w, 200)
		var page struct{ Items []item }
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	ids := func(items []item) string {
		names := []string{}
		for _, i := range items {
			names = append(names, i.Id)
		}
		slices.Sort(names)
		return strings.Join(names, ",")
	}
	views := "/Users/" + user + "/Views"
	if got := ids(list(views, token)); strings.Contains(got, collectionsViewID) {
		t.Fatalf("views before any collection: %s", got)
	}

	w := request(h, "POST", "/Collections?Name=Saga&Ids=movie,series&userId="+user, "", "", token)
	expectStatus(t, w, 200)
	var created struct{ Id, Name string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Id == "" || created.Name != "Saga" {
		t.Fatalf("create: %s", w.Body)
	}
	id := created.Id
	expectStatus(t, request(h, "POST", "/Collections?Name=Bad&Ids=nothing", "", "", token), 400)
	expectStatus(t, request(h, "POST", "/Collections?Name=%20&Ids=movie", "", "", token), 400)

	if got := ids(list(views, token)); !strings.Contains(got, collectionsViewID) {
		t.Fatalf("views: %s", got)
	}
	sets := list("/Users/"+user+"/Items?ParentId="+collectionsViewID, token)
	if len(sets) != 1 || sets[0].Type != "BoxSet" || sets[0].ChildCount != 2 || !sets[0].CanDelete || !sets[0].CanEditItems {
		t.Fatalf("Collections library: %+v", sets)
	}
	w = request(h, "GET", "/ItemTypes?ParentId="+id+"&userId="+user, "", "", token)
	if !strings.Contains(w.Body.String(), `"Movie"`) || !strings.Contains(w.Body.String(), `"Series"`) {
		t.Fatalf("item types: %s", w.Body)
	}
	if got := ids(list("/Users/"+user+"/Items?ParentId="+id+"&SortBy=DisplayOrder&SortOrder=Ascending", token)); got != "movie,series" {
		t.Fatalf("collection items: %s", got)
	}
	// Play on a collection expands it to its media.
	if got := ids(list("/Users/"+user+"/Items?ParentId="+id+"&SortBy=DisplayOrder&SortOrder=Ascending&ProjectToMedia=true&ExcludeLocationTypes=Virtual&EnableTotalRecordCount=false&CollapseBoxSetItems=false", token)); got != "episode,movie" {
		t.Fatalf("collection media: %s", got)
	}
	for _, library := range []string{"movies", catalog.SeriesLibraryID()} {
		if got := ids(list("/Users/"+user+"/Items?IncludeItemTypes=BoxSet&Recursive=true&ParentId="+library, token)); got != id {
			t.Fatalf("Collections tab of %s: %s", library, got)
		}
	}
	if got := ids(list("/Users/"+user+"/Items?Recursive=true&IncludeItemTypes=Boxset&SortBy=SortName&CanEditItems=true&EnableUserData=false&NameStartsWithOrGreater=R", token)); got != id {
		t.Fatalf("Add to collection picker: %s", got)
	}
	appearsIn := "/Users/" + user + "/Items?IncludeItemTypes=Playlist,BoxSet&Recursive=true&SortBy=SortName&ListItemIds="
	if got := ids(list(appearsIn+"movie", token)); got != id {
		t.Fatalf("collections of movie: %s", got)
	}
	if got := ids(list(appearsIn+"episode", token)); got != "" {
		t.Fatalf("collections of episode: %s", got)
	}
	if got := ids(list("/Users/"+user+"/Items?ParentId=movies&Recursive=true&IncludeItemTypes=Movie&GroupItemsIntoCollections=true", token)); got != id {
		t.Fatalf("grouped movies: %s", got)
	}

	// Emby Web posts the items as JSON in a text/plain body.
	expectStatus(t, request(h, "POST", "/Collections/"+id+"/Items?reqformat=json", "text/plain", `{"Ids":"episode","userId":"`+user+`","SkipDuplicates":null}`, token), 204)
	expectStatus(t, request(h, "POST", "/Collections/"+id+"/Items", "", "", token), 400)
	expectStatus(t, request(h, "POST", "/Collections/missing/Items?Ids=movie", "", "", token), 404)
	if got := list("/Users/"+user+"/Items?Ids="+id, token); len(got) != 1 || got[0].ChildCount != 3 {
		t.Fatalf("after add: %+v", got)
	}
	expectStatus(t, request(h, "POST", "/Users/"+user+"/FavoriteItems/"+id, "", "", token), 200)
	expectStatus(t, request(h, "POST", "/Users/"+user+"/PlayedItems/"+id, "", "", token), 200)
	if got := list("/Users/"+user+"/Items?Ids="+id, token); !got[0].UserData.IsFavorite || !got[0].UserData.Played {
		t.Fatalf("user data: %+v", got[0].UserData)
	}
	if got := list("/Users/"+user+"/Items?Ids=movie,episode&Filters=IsPlayed", token); len(got) != 2 {
		t.Fatalf("Mark played did not mark the items: %+v", got)
	}
	// Identify is for movies and series; without a refresher, Refresh
	// Metadata on a collection is refused like on any item.
	for _, r := range [][2]string{{"GET", "/Items/" + id + "/ExternalIdInfos"}, {"POST", "/Items/Metadata/Reset?ItemIds=" + id}, {"POST", "/Items/" + id + "/Refresh"}} {
		w := request(h, r[0], r[1], "", "", token)
		if w.Code != 400 {
			t.Fatalf("%s %s: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}
	expectStatus(t, request(h, "POST", "/Items/"+id, "application/json", `{"Id":"`+id+`","Name":"Renamed","DisplayOrder":"SortName"}`, token), 204)
	expectStatus(t, request(h, "POST", "/Collections/"+id+"/Items/Delete?Ids=movie", "", "", token), 204)
	if got := list("/Users/"+user+"/Items?Ids="+id, token); got[0].Name != "Renamed" || got[0].ChildCount != 2 {
		t.Fatalf("after rename and remove: %+v", got)
	}

	// Another user sees a collection with an item they can see, and changes none.
	empty, err := store.CreateCollection("Empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateUser("other", state.Policy{IsAdministrator: true, EnableAllFolders: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPolicy(user, state.Policy{EnableAllFolders: true}); err != nil {
		t.Fatal(err)
	}
	if got := list("/Users/"+user+"/Items?ParentId="+collectionsViewID, token); len(got) != 1 || got[0].Id != id || got[0].CanDelete || got[0].CanEditItems {
		t.Fatalf("user's collections: %+v", got)
	}
	expectStatus(t, request(h, "GET", "/Users/"+user+"/Items/"+empty.ID, "", "", token), 404)
	expectStatus(t, request(h, "POST", "/Collections?Name=Mine&Ids=movie", "", "", token), 403)
	expectStatus(t, request(h, "POST", "/Items/Delete?Ids="+id, "", "", token), 403)
	if _, err := store.SetPolicy(user, state.Policy{IsAdministrator: true, EnableAllFolders: true}); err != nil {
		t.Fatal(err)
	}

	w = request(h, "GET", "/Items/"+id+"/DeleteInfo", "", "", token)
	expectStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"Paths":[]}` {
		t.Fatalf("delete info: %s", w.Body)
	}
	expectStatus(t, request(h, "GET", "/Items/movie/DeleteInfo", "", "", token), 404)
	expectStatus(t, request(h, "POST", "/Items/Delete?Ids=movie", "", "", token), 400)
	expectStatus(t, request(h, "POST", "/Items/Delete?Ids="+id+","+empty.ID, "", "", token), 204)
	expectStatus(t, request(h, "GET", "/Users/"+user+"/Items/"+id, "", "", token), 404)
	if got := ids(list(views, token)); strings.Contains(got, collectionsViewID) {
		t.Fatalf("views after deleting every collection: %s", got)
	}
}

// TestSetCollections checks that movies naming the same set in their
// metadata appear as one collection in Emby Web.
func TestSetCollections(t *testing.T) {
	store, _, _ := newTestServer(t)
	one, two := playbackMovie(), playbackMovie()
	two.ID = "movie2"
	catalog := &media.Catalog{ID: "movies", Items: []media.Item{one, two}}
	api := New(store, "test", nil, catalog)
	h := api.Handler()
	token := login(t, h)
	api.SetMetadata(metadata.Library{"movie": {Name: "One", Set: "Saga"}, "movie2": {Name: "Two", Set: "saga"}})
	w := request(h, "GET", "/Users/"+testUser(store).ID+"/Items?ParentId="+collectionsViewID, "", "", token)
	expectStatus(t, w, 200)
	var page struct{ Items []struct{ Name, Type string } }
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].Name != "Saga" || page.Items[0].Type != "BoxSet" {
		t.Fatalf("Collections library: %s", w.Body)
	}
	c := store.Snapshot().Collections[0]
	if !slices.Equal(c.Items, []string{"movie", "movie2"}) {
		t.Fatalf("collection %+v", c)
	}
	// Publishing again changes nothing.
	api.SetMetadata(metadata.Library{"movie": {Name: "One", Set: "Saga"}, "movie2": {Name: "Two", Set: "Saga"}})
	if got := store.Snapshot().Collections; len(got) != 1 || got[0].ID != c.ID {
		t.Fatalf("collections after republishing: %+v", got)
	}
}
