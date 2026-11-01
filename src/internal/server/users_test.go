package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
)

// TestUserManagement walks the administrator's user pages: create a user,
// give them a password, let them sign in, and check what they may not do.
func TestUserManagement(t *testing.T) {
	store, _, _ := newTestServer(t)
	h := New(store, "test", nil, &media.Catalog{ID: "movies", Items: []media.Item{playbackMovie()}}).Handler()
	admin := login(t, h)
	adminID := testUser(store).ID
	form := "application/x-www-form-urlencoded; charset=UTF-8"
	decode := func(body []byte, v any) {
		t.Helper()
		if err := json.Unmarshal(body, v); err != nil {
			t.Fatal(err)
		}
	}
	w := request(h, "POST", "/Users/New", form, url.Values{"Name": {"Guest"}}.Encode(), admin)
	expectStatus(t, w, 200)
	var guest struct {
		Id          string
		HasPassword bool
		Policy      struct{ IsAdministrator, EnableAllFolders bool }
	}
	decode(w.Body.Bytes(), &guest)
	if guest.Id == "" || guest.HasPassword || guest.Policy.IsAdministrator || !guest.Policy.EnableAllFolders {
		t.Fatalf("new user %+v", guest)
	}
	expectStatus(t, request(h, "POST", "/Users/New", form, url.Values{"Name": {"guest"}}.Encode(), admin), 409)
	signIn := func(name, pw string) (string, int) {
		w := request(h, "POST", "/Users/authenticatebyname", form, url.Values{"Username": {name}, "Pw": {pw}}.Encode(), "")
		var result struct{ AccessToken string }
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result.AccessToken, w.Code
	}
	if _, code := signIn("Guest", ""); code != 401 {
		t.Fatalf("passwordless sign-in: %d", code)
	}
	// The administrator sets the password without knowing a current one.
	expectStatus(t, request(h, "POST", "/Users/"+guest.Id+"/Password", form, url.Values{"NewPw": {"guest-test-password"}}.Encode(), admin), 204)
	user, code := signIn("guest", "guest-test-password")
	if code != 200 {
		t.Fatalf("guest sign-in: %d", code)
	}
	// The guest's watch state is their own.
	expectStatus(t, request(h, "POST", "/Users/"+guest.Id+"/PlayedItems/movie", "", "", user), 200)
	if store.Snapshot().Users[guest.Id].Items["movie"].PlayCount != 1 || testUser(store).Items["movie"].Played {
		t.Fatal("played state was not kept per user")
	}
	// A user who is not an administrator manages nothing and reads no one else.
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/Users/Query", ""},
		{"POST", "/Users/New", url.Values{"Name": {"Third"}}.Encode()},
		{"GET", "/Users/" + adminID, ""},
		{"POST", "/Users/" + adminID + "/Password", url.Values{"NewPw": {"guest-takes-over-1"}}.Encode()},
		{"POST", "/Users/" + guest.Id + "/Policy", `{"IsAdministrator":true,"EnableAllFolders":true}`},
		{"POST", "/Users/" + adminID + "/Delete", ""},
		{"GET", "/Users/unknown", ""},
		{"GET", "/ScheduledTasks", ""},
	} {
		if w := request(h, tc.method, tc.path, form, tc.body, user); w.Code != 403 {
			t.Fatalf("%s %s as a user: %d", tc.method, tc.path, w.Code)
		}
	}
	expectStatus(t, request(h, "GET", "/Users/"+guest.Id, "", "", user), 200)
	// The administrator lists, renames and hides the guest.
	w = request(h, "GET", "/Users/Query", "", "", admin)
	expectStatus(t, w, 200)
	var page struct {
		Items            []struct{ Name string }
		TotalRecordCount int
	}
	decode(w.Body.Bytes(), &page)
	if page.TotalRecordCount != 2 || page.Items[0].Name != "Guest" || page.Items[1].Name != "viewer" {
		t.Fatalf("users %+v", page)
	}
	expectStatus(t, request(h, "POST", "/Users/"+guest.Id, "application/json", `{"Name":"Visitor","Policy":{}}`, admin), 204)
	// Emby Web posts back the whole policy it read, changed by the form.
	w = request(h, "GET", "/Users/"+guest.Id, "", "", admin)
	var read struct{ Policy map[string]any }
	decode(w.Body.Bytes(), &read)
	read.Policy["IsHidden"] = true
	// The new-user page sets these with the role; they are ignored.
	read.Policy["EnableContentDeletion"], read.Policy["EnableSubtitleManagement"] = true, true
	read.Policy["EnableAllChannels"] = true
	read.Policy["SimultaneousStreamLimit"] = "0"
	body, _ := json.Marshal(read.Policy)
	expectStatus(t, request(h, "POST", "/Users/"+guest.Id+"/Policy", "text/plain", string(body), admin), 204)
	// A setting Couch reports with a fixed value cannot be changed.
	read.Policy["EnableAllDevices"] = false
	body, _ = json.Marshal(read.Policy)
	if w := request(h, "POST", "/Users/"+guest.Id+"/Policy", "text/plain", string(body), admin); w.Code != 400 || !strings.Contains(w.Body.String(), "EnableAllDevices") {
		t.Fatalf("fixed policy change: %d %s", w.Code, w.Body)
	}
	w = request(h, "GET", "/Users/Public", "", "", "")
	var public []struct{ Name string }
	decode(w.Body.Bytes(), &public)
	if len(public) != 1 || public[0].Name != "viewer" {
		t.Fatalf("public users %+v", public)
	}
	// Refused: limiting libraries (not enforced yet), demoting the last
	// administrator, deleting oneself.
	expectStatus(t, request(h, "POST", "/Users/"+guest.Id+"/Policy", "application/json", `{"EnableAllFolders":false,"EnabledFolders":["movies"]}`, admin), 400)
	expectStatus(t, request(h, "POST", "/Users/"+adminID+"/Policy", "application/json", `{"EnableAllFolders":true}`, admin), 400)
	expectStatus(t, request(h, "POST", "/Users/"+adminID+"/Delete", "", "", admin), 400)
	// Disabling signs the guest out and keeps them out.
	expectStatus(t, request(h, "POST", "/Users/"+guest.Id+"/Policy", "application/json", `{"IsDisabled":true,"EnableAllFolders":true}`, admin), 204)
	expectStatus(t, request(h, "GET", "/Users/me", "", "", user), 401)
	if _, code := signIn("visitor", "guest-test-password"); code != 401 {
		t.Fatalf("disabled sign-in: %d", code)
	}
	// Copying the administrator's configuration to the guest.
	expectStatus(t, request(h, "POST", "/Users/"+adminID+"/Configuration", "application/json", `{"SubtitleMode":"Always"}`, admin), 204)
	copyData := `{"ToUserIds":["` + guest.Id + `"],"CopyOptions":["userconfiguration"]}`
	expectStatus(t, request(h, "POST", "/Users/"+adminID+"/CopyData", "application/json", copyData, admin), 204)
	if string(store.Snapshot().Users[guest.Id].Configuration["SubtitleMode"]) != `"Always"` {
		t.Fatal("the configuration was not copied")
	}
	expectStatus(t, request(h, "POST", "/Users/"+adminID+"/CopyData", "application/json", `{"ToUserIds":["`+guest.Id+`"],"CopyOptions":["userdata"]}`, admin), 400)
	// Copying a user's policy over the only administrator is refused.
	expectStatus(t, request(h, "POST", "/Users/"+guest.Id+"/CopyData", "application/json", `{"ToUserIds":["`+adminID+`"],"CopyOptions":["userpolicy"]}`, admin), 400)
	expectStatus(t, request(h, "DELETE", "/Users/"+guest.Id, "", "", admin), 204)
	expectStatus(t, request(h, "GET", "/Users/"+guest.Id, "", "", admin), 404)
	if _, ok := store.Snapshot().Users[guest.Id]; ok {
		t.Fatal("the user was not deleted")
	}
}
