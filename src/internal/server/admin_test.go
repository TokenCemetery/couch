package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// TestAdministratorRole checks that the policy reports the stored role and
// that administrator-only routes refuse a user without it.
func TestAdministratorRole(t *testing.T) {
	store, h, _ := newTestServer(t)
	token := login(t, h)
	isAdministrator := func() bool {
		t.Helper()
		w := request(h, "GET", "/Users/me", "", "", token)
		expectStatus(t, w, 200)
		var user struct {
			Policy struct{ IsAdministrator bool }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &user); err != nil {
			t.Fatal(err)
		}
		return user.Policy.IsAdministrator
	}
	routes := []string{"/emby/ScheduledTasks", "/emby/System/ActivityLog/Entries"}
	if !isAdministrator() {
		t.Fatal("the -init user is not an administrator")
	}
	for _, path := range routes {
		expectStatus(t, request(h, "GET", path, "", "", token), 200)
		expectStatus(t, request(h, "GET", path, "", "", ""), 401)
	}
	// A second administrator lets the test user give up the role.
	if _, err := store.CreateUser("other", state.Policy{IsAdministrator: true, EnableAllFolders: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPolicy(testUser(store).ID, state.Policy{EnableAllFolders: true}); err != nil {
		t.Fatal(err)
	}
	if isAdministrator() {
		t.Fatal("the policy still reports an administrator")
	}
	for _, path := range routes {
		expectStatus(t, request(h, "GET", path, "", "", token), 403)
	}
}

// TestItemsEditableByAdministrator checks that media items let an
// administrator add them to a collection, which Emby Web offers by
// CanEditItems, while libraries and other users' items stay read-only.
func TestItemsEditableByAdministrator(t *testing.T) {
	store, _, _ := newTestServer(t)
	h := New(store, "test", nil, &media.Catalog{ID: "movies", Items: []media.Item{playbackMovie()}}).Handler()
	token := login(t, h)
	editable := func(path string) []any {
		t.Helper()
		w := request(h, "GET", path, "", "", token)
		expectStatus(t, w, 200)
		var page struct{ Items []map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			t.Fatalf("%s: no items", path)
		}
		flags := []any{}
		for _, item := range page.Items {
			flags = append(flags, item["CanEditItems"])
		}
		return flags
	}
	movies, views := "/Items?Recursive=true&IncludeItemTypes=Movie", "/Users/"+testUser(store).ID+"/Views"
	if got := editable(movies); got[0] != true {
		t.Fatalf("administrator: movie CanEditItems %v", got)
	}
	if got := editable(views); got[0] != false {
		t.Fatalf("administrator: library CanEditItems %v", got)
	}
	// The image editor opens on an item's images and refuses changes.
	for path, want := range map[string]string{"/Items/movie/Images": "[]", "/Items/movie/RemoteImages/Providers?itemId=movie": "[]"} {
		w := request(h, "GET", path, "", "", token)
		expectStatus(t, w, 200)
		if strings.TrimSpace(w.Body.String()) != want {
			t.Fatalf("%s: %s", path, w.Body)
		}
	}
	expectStatus(t, request(h, "POST", "/Items/movie/Images/Primary", "image/png", "x", token), 400)
	expectStatus(t, request(h, "DELETE", "/Items/movie/Images/Backdrop/0", "", "", token), 400)
	if _, err := store.CreateUser("other", state.Policy{IsAdministrator: true, EnableAllFolders: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPolicy(testUser(store).ID, state.Policy{EnableAllFolders: true}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{movies, views} {
		for _, flag := range editable(path) {
			if flag != false {
				t.Fatalf("user: %s has CanEditItems %v", path, flag)
			}
		}
	}
}
