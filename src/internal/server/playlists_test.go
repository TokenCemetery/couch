package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// TestPlaylists walks a user who is not an administrator through Emby Web's
// playlist requests, then checks that an administrator neither sees nor
// changes the playlist, and that an entry whose file is gone is hidden.
func TestPlaylists(t *testing.T) {
	store, _, _ := newTestServer(t)
	catalog := &media.Catalog{ID: "movies", Items: []media.Item{
		playbackMovie(),
		{ID: "e1", Kind: "Episode", Name: "Show S01E01", ParentID: "season", SeriesID: "series", SeasonID: "season", SeasonNumber: 1, EpisodeNumber: 1},
		{ID: "e2", Kind: "Episode", Name: "Show S01E02", ParentID: "season", SeriesID: "series", SeasonID: "season", SeasonNumber: 1, EpisodeNumber: 2},
	}}
	catalog.Folders = []media.Item{
		{ID: "series", Kind: "Series", Name: "Show", ParentID: catalog.SeriesLibraryID()},
		{ID: "season", Kind: "Season", Name: "Season 1", ParentID: "series", SeriesID: "series", SeasonNumber: 1},
	}
	h := New(store, "test", nil, catalog).Handler()
	admin := login(t, h)
	guest, err := store.CreateUser("guest", state.Policy{EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPassword(admin, guest.ID, "guest-test-password"); err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", "/Users/authenticatebyname", "application/x-www-form-urlencoded", url.Values{"Username": {"guest"}, "Pw": {"guest-test-password"}}.Encode(), "")
	var signedIn struct{ AccessToken string }
	if err := json.Unmarshal(w.Body.Bytes(), &signedIn); err != nil || signedIn.AccessToken == "" {
		t.Fatalf("guest sign-in: %s", w.Body)
	}
	token := signedIn.AccessToken
	type item struct {
		Id, Name, Type, CollectionType, PlaylistItemId string
		ChildCount                                     int
		CanDelete, CanEditItems                        bool
		UserData                                       struct{ IsFavorite, Played bool }
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
		return strings.Join(names, ",")
	}
	userItems := "/Users/" + guest.ID + "/Items"
	views := "/Users/" + guest.ID + "/Views"
	dialog := userItems + "?Recursive=true&IncludeItemTypes=Playlist&SortBy=SortName&Fields=PrimaryImageAspectRatio&EnableUserData=false&CanEditItems=true&Limit=30"
	if got := ids(list(views, token)); strings.Contains(got, playlistsViewID) {
		t.Fatalf("views before any playlist: %s", got)
	}
	if got := list(dialog, token); len(got) != 0 {
		t.Fatalf("dialog before any playlist: %+v", got)
	}

	w = request(h, "POST", "/Playlists?Name=Evening&Ids=movie&userId="+guest.ID, "", "", token)
	expectStatus(t, w, 200)
	var created struct {
		Id, Name       string
		ItemAddedCount int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.Id == "" || created.Name != "Evening" || created.ItemAddedCount != 1 {
		t.Fatalf("create: %s", w.Body)
	}
	id := created.Id
	for _, path := range []string{"/Playlists?Name=Bad&Ids=nothing", "/Playlists?Name=%20&Ids=movie", "/Playlists?Name=Songs&Ids=movie&MediaType=Audio"} {
		expectStatus(t, request(h, "POST", path, "", "", token), 400)
	}
	if got := ids(list(dialog, token)); got != id {
		t.Fatalf("dialog: %s", got)
	}

	// Emby Web asks about duplicates first; a series adds its episodes.
	info := func(ids string) (out struct {
		ItemCount          int
		ContainsDuplicates bool
	}) {
		t.Helper()
		w := request(h, "GET", "/Playlists/"+id+"/AddToPlaylistInfo?Ids="+ids+"&userId="+guest.ID, "", "", token)
		expectStatus(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := info("movie"); !got.ContainsDuplicates || got.ItemCount != 1 {
		t.Fatalf("info for a held movie: %+v", got)
	}
	if got := info("series"); got.ContainsDuplicates || got.ItemCount != 2 {
		t.Fatalf("info for a new series: %+v", got)
	}
	add := func(ids, skip string, want int) {
		t.Helper()
		w := request(h, "POST", "/Playlists/"+id+"/Items?reqformat=json", "text/plain", `{"Ids":"`+ids+`","userId":"`+guest.ID+`","SkipDuplicates":`+skip+`}`, token)
		expectStatus(t, w, 200)
		var result struct{ ItemAddedCount int }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.ItemAddedCount != want {
			t.Fatalf("add %s skipping %s: %s", ids, skip, w.Body)
		}
	}
	add("series", "null", 2)
	add("movie", "true", 0)
	add("movie", "false", 1)
	expectStatus(t, request(h, "POST", "/Playlists/"+id+"/Items", "", "", token), 400)
	expectStatus(t, request(h, "POST", "/Playlists/missing/Items?Ids=movie", "", "", token), 404)

	if got := ids(list(views, token)); !strings.Contains(got, playlistsViewID) {
		t.Fatalf("views: %s", got)
	}
	lists := list(userItems+"?ParentId="+playlistsViewID, token)
	if len(lists) != 1 || lists[0].Type != "Playlist" || lists[0].ChildCount != 4 || !lists[0].CanDelete || !lists[0].CanEditItems {
		t.Fatalf("Playlists library: %+v", lists)
	}
	entries := list(userItems+"?ParentId="+id+"&SortBy=ListItemOrder", token)
	if got := ids(entries); got != "movie,e1,e2,movie" || entries[0].PlaylistItemId == "" || entries[0].PlaylistItemId == entries[3].PlaylistItemId {
		t.Fatalf("playlist entries: %+v", entries)
	}
	// Dragging the repeated movie up, then removing the first entry.
	expectStatus(t, request(h, "POST", "/Playlists/"+id+"/Items/"+entries[3].PlaylistItemId+"/Move/1", "", "", token), 204)
	expectStatus(t, request(h, "POST", "/Playlists/"+id+"/Items/missing/Move/1", "", "", token), 404)
	expectStatus(t, request(h, "POST", "/Playlists/"+id+"/Items/Delete?EntryIds="+entries[0].PlaylistItemId, "", "", token), 204)
	if got := ids(list("/Playlists/"+id+"/Items?userId="+guest.ID, token)); got != "movie,e1,e2" {
		t.Fatalf("after move and remove: %s", got)
	}
	// Play on a playlist asks for its videos in list order.
	play := userItems + "?ParentId=" + id + "&SortBy=ListItemOrder&Fields=Chapters&ExcludeLocationTypes=Virtual&EnableTotalRecordCount=false&CollapseBoxSetItems=false&Filters=IsNotFolder"
	if got := ids(list(play, token)); got != "movie,e1,e2" {
		t.Fatalf("play: %s", got)
	}
	if got := ids(list(userItems+"?ParentId="+id+"&SortBy=SortName&SortOrder=Descending", token)); got != "e2,e1,movie" {
		t.Fatalf("sorted by name: %s", got)
	}
	if got := ids(list(userItems+"?Fields=PrimaryImageAspectRatio&Recursive=true&IncludeItemTypes=Playlist", token)); got != id {
		t.Fatalf("navigation menu: %s", got)
	}
	includedIn := userItems + "?IncludeItemTypes=Playlist,BoxSet&Recursive=true&SortBy=SortName&ListItemIds="
	if got := ids(list(includedIn+"e1", token)); got != id {
		t.Fatalf("playlists of e1: %s", got)
	}
	if got := ids(list(includedIn+"series", token)); got != "" {
		t.Fatalf("playlists of the series: %s", got)
	}

	// The owner renames the playlist from Edit Metadata and opens the image editor.
	expectStatus(t, request(h, "GET", "/Items/"+id+"/MetadataEditor", "", "", token), 200)
	expectStatus(t, request(h, "POST", "/Items/"+id, "application/json", `{"Id":"`+id+`","Name":"Night"}`, token), 204)
	expectStatus(t, request(h, "GET", "/Items/"+id+"/Images", "", "", token), 200)
	expectStatus(t, request(h, "POST", "/Users/"+guest.ID+"/FavoriteItems/"+id, "", "", token), 200)
	expectStatus(t, request(h, "POST", "/Users/"+guest.ID+"/PlayedItems/"+id, "", "", token), 200)
	if got := list(userItems+"?Ids="+id, token); len(got) != 1 || got[0].Name != "Night" || !got[0].UserData.IsFavorite || !got[0].UserData.Played {
		t.Fatalf("after rename, favorite and played: %+v", got)
	}

	// The administrator sees no playlist of another user and changes none.
	adminID := testUser(store).ID
	if got := ids(list("/Users/"+adminID+"/Views", admin)); strings.Contains(got, playlistsViewID) {
		t.Fatalf("administrator's views: %s", got)
	}
	if got := list("/Users/"+adminID+"/Items?Recursive=true&IncludeItemTypes=Playlist", admin); len(got) != 0 {
		t.Fatalf("administrator's playlists: %+v", got)
	}
	expectStatus(t, request(h, "GET", "/Users/"+adminID+"/Items/"+id, "", "", admin), 404)
	expectStatus(t, request(h, "POST", "/Playlists/"+id+"/Items?Ids=movie", "", "", admin), 404)
	expectStatus(t, request(h, "GET", "/Items/"+id+"/DeleteInfo", "", "", admin), 404)
	expectStatus(t, request(h, "POST", "/Items/Delete?Ids="+id, "", "", admin), 400)
	expectStatus(t, request(h, "POST", "/Items/"+id, "application/json", `{"Name":"Theirs"}`, admin), 404)

	// An entry whose file is gone is hidden, and comes back with its path.
	smaller := &media.Catalog{ID: "movies", Items: []media.Item{playbackMovie()}}
	if got := list(userItems+"?ParentId="+playlistsViewID, token); got[0].ChildCount != 3 {
		t.Fatalf("before the rescan: %+v", got)
	}
	hs := New(store, "test", nil, smaller).Handler()
	w = request(hs, "GET", userItems+"?ParentId="+id, "", "", token)
	if !strings.Contains(w.Body.String(), `"TotalRecordCount":1`) {
		t.Fatalf("after the episodes are gone: %s", w.Body)
	}

	// The owner deletes the playlist; media files are never deleted.
	expectStatus(t, request(h, "GET", "/Items/"+id+"/DeleteInfo", "", "", token), 200)
	expectStatus(t, request(h, "POST", "/Items/Delete?Ids=movie", "", "", token), 403)
	expectStatus(t, request(h, "DELETE", "/Items/"+id, "", "", token), 204)
	if got := ids(list(views, token)); strings.Contains(got, playlistsViewID) {
		t.Fatalf("views after deleting the playlist: %s", got)
	}
}
