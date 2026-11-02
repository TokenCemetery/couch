package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/library"
	"github.com/TokenCemetery/couch/internal/state"
)

func TestLibrarySetup(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	for _, dir := range []string{"films", "shows", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	store, _, _ := newTestServer(t)
	api := New(store, "test", nil, nil)
	m, err := library.New(store, []string{root}, filepath.Join(t.TempDir(), "catalog.json"), api.SetCatalog)
	if err != nil {
		t.Fatal(err)
	}
	api.SetLibraries(m)
	h := api.Handler()
	admin := login(t, h)
	query := func() []struct {
		Name, CollectionType, Id string
		Locations                []string
	} {
		t.Helper()
		w := request(h, "GET", "/Library/VirtualFolders/Query", "", "", admin)
		expectStatus(t, w, 200)
		var page struct {
			Items []struct {
				Name, CollectionType, Id string
				Locations                []string
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	create := func(name, kind, path string) *httptest.ResponseRecorder {
		body := `{"LibraryOptions":{"PathInfos":[{"Path":"` + path + `"}]}}`
		return request(h, "POST", "/Library/VirtualFolders?collectionType="+kind+"&refreshLibrary=true&name="+url.QueryEscape(name), "application/json", body, admin)
	}
	films, shows := filepath.Join(root, "films"), filepath.Join(root, "shows")
	if r := create("Films", "movies", films); r.Code != 204 {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	for _, tc := range []struct{ name, kind, path, want string }{
		{"Films", "tvshows", shows, "already exists"},
		{"Books", "books", shows, "only movies, TV shows, music and home videos"},
		{"Out", "movies", outside, "not inside a media root"},
		{"Escape", "movies", filepath.Join(root, "escape"), "not inside a media root"},
		{"Dots", "movies", root + "/shows/../films", "without . or .."},
		{"Twice", "movies", films, "overlaps"},
	} {
		if r := create(tc.name, tc.kind, tc.path); r.Code < 400 || !strings.Contains(r.Body.String(), tc.want) {
			t.Errorf("%s: %d %s, want %q", tc.name, r.Code, r.Body, tc.want)
		}
	}
	libraries := query()
	if len(libraries) != 1 || libraries[0].Name != "Films" || libraries[0].CollectionType != "movies" || libraries[0].Locations[0] != films {
		t.Fatalf("libraries %+v", libraries)
	}
	id := libraries[0].Id
	expectStatus(t, request(h, "POST", "/Library/VirtualFolders/Name?Id="+id+"&newName=Movies&name=Films", "", "", admin), 204)
	expectStatus(t, request(h, "POST", "/Library/VirtualFolders/Paths?refreshLibrary=true", "application/json", `{"Name":"Movies","Id":"`+id+`","PathInfo":{"Path":"`+shows+`"}}`, admin), 204)
	expectStatus(t, request(h, "POST", "/Library/VirtualFolders/Paths/Delete?Id="+id+"&path="+url.QueryEscape(films), "", "", admin), 204)
	if l := query(); l[0].Name != "Movies" || len(l[0].Locations) != 1 || l[0].Locations[0] != shows {
		t.Fatalf("after edits %+v", l)
	}
	if d := store.Snapshot(); !d.LibrariesConfigured || d.Libraries[0].Paths[0] != shows {
		t.Fatalf("stored %+v", d.Libraries)
	}
	expectStatus(t, request(h, "POST", "/Library/VirtualFolders/LibraryOptions", "application/json", `{"Id":"`+id+`","LibraryOptions":{}}`, admin), 204)

	// The folder browser lists only the roots and the folders below them.
	w := request(h, "POST", "/Environment/DirectoryContents", "application/json", `{"Path":"`+root+`","IncludeDirectories":true}`, admin)
	expectStatus(t, w, 200)
	var entries []struct{ Name, Path string }
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "films" || entries[1].Path != shows {
		t.Fatalf("entries %+v", entries)
	}
	expectStatus(t, request(h, "GET", "/Environment/DirectoryContents?Path="+url.QueryEscape(outside), "", "", admin), 400)
	expectStatus(t, request(h, "GET", "/Environment/DirectoryContents?Path="+url.QueryEscape(filepath.Join(root, "escape")), "", "", admin), 400)
	if w := request(h, "GET", "/Environment/ParentPath?Path="+url.QueryEscape(shows), "", "", admin); w.Body.String() != root {
		t.Fatalf("parent %q", w.Body)
	}
	if w := request(h, "GET", "/Environment/ParentPath?Path="+url.QueryEscape(root), "", "", admin); w.Body.String() != "" {
		t.Fatalf("parent of a root %q", w.Body)
	}
	w = request(h, "GET", "/Environment/Drives", "", "", admin)
	if !strings.Contains(w.Body.String(), root) {
		t.Fatalf("drives %s", w.Body)
	}

	// The library scan is a scheduled task.
	w = request(h, "GET", "/ScheduledTasks?IsEnabled=true", "", "", admin)
	var tasks []struct{ Key, Id, State string }
	if err := json.Unmarshal(w.Body.Bytes(), &tasks); err != nil || len(tasks) != 1 || tasks[0].Key != "RefreshLibrary" || tasks[0].State != "Idle" {
		t.Fatalf("tasks %s", w.Body)
	}
	expectStatus(t, request(h, "POST", "/ScheduledTasks/Running/"+tasks[0].Id, "", "", admin), 204)
	expectStatus(t, request(h, "POST", "/ScheduledTasks/Running/unknown", "", "", admin), 404)

	// None of this is open to a user who is not an administrator.
	if _, err := store.CreateUser("guest", state.Policy{EnableAllFolders: true}, nil); err != nil {
		t.Fatal(err)
	}
	guestID := ""
	for id, u := range store.Snapshot().Users {
		if u.Name == "guest" {
			guestID = id
		}
	}
	if _, err := store.SetPassword(admin, guestID, "guest-test-password"); err != nil {
		t.Fatal(err)
	}
	w = request(h, "POST", "/Users/authenticatebyname", "application/x-www-form-urlencoded", url.Values{"Username": {"guest"}, "Pw": {"guest-test-password"}}.Encode(), "")
	var signedIn struct{ AccessToken string }
	if err := json.Unmarshal(w.Body.Bytes(), &signedIn); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/Library/VirtualFolders/Query", "/Environment/Drives", "/ScheduledTasks"} {
		expectStatus(t, request(h, "GET", path, "", "", signedIn.AccessToken), 403)
	}

	expectStatus(t, request(h, "POST", "/Library/VirtualFolders/Delete?id="+id+"&name=Movies", "", "", admin), 204)
	if len(query()) != 0 {
		t.Fatal("library not removed")
	}
}
