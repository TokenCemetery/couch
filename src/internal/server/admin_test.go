package server

import (
	"encoding/json"
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

// TestItemsAreNotEditable checks that items tell Emby Web the library is
// read-only, so an administrator is not offered editors Couch does not serve.
func TestItemsAreNotEditable(t *testing.T) {
	store, _, _ := newTestServer(t)
	h := New(store, "test", nil, &media.Catalog{ID: "movies", Items: []media.Item{playbackMovie()}}).Handler()
	token := login(t, h)
	for _, path := range []string{"/Items?Recursive=true&IncludeItemTypes=Movie", "/Users/" + testUser(store).ID + "/Views"} {
		w := request(h, "GET", path, "", "", token)
		expectStatus(t, w, 200)
		var page struct{ Items []map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			t.Fatalf("%s: no items", path)
		}
		for _, item := range page.Items {
			if item["CanEditItems"] != false {
				t.Fatalf("%s: %v has CanEditItems %v", path, item["Id"], item["CanEditItems"])
			}
		}
	}
}
