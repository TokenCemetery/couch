package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/state"
)

func TestChangePassword(t *testing.T) {
	store, h, _ := newTestServer(t)
	token, other := login(t, h), login(t, h)
	path := "/Users/" + testUser(store).ID + "/Password"
	const next = "another-test-only-password"
	form := func(values url.Values) string { return values.Encode() }
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{path, form(url.Values{"CurrentPw": {"wrong-test-password"}, "NewPw": {next}}), 401},
		{path, form(url.Values{"CurrentPw": {testPassword}, "NewPw": {strings.Repeat("p", 1025)}}), 400},
		{"/Users/other/Password", form(url.Values{"CurrentPw": {testPassword}, "NewPw": {next}}), 404},
	} {
		w := request(h, "POST", tc.path, "application/x-www-form-urlencoded; charset=UTF-8", tc.body, token)
		if w.Code != tc.status {
			t.Fatalf("%s %s: status %d, want %d", tc.path, tc.body, w.Code, tc.status)
		}
	}
	// Emby Web posts the Profile form as application/x-www-form-urlencoded.
	expectStatus(t, request(h, "POST", path, "application/x-www-form-urlencoded; charset=UTF-8", form(url.Values{"CurrentPw": {testPassword}, "NewPw": {next}}), token), 204)
	expectStatus(t, request(h, "GET", "/Users/me", "", "", token), 200)
	expectStatus(t, request(h, "GET", "/Users/me", "", "", other), 401)
	signIn := func(pw string) int {
		return request(h, "POST", "/Users/authenticatebyname", "application/x-www-form-urlencoded", form(url.Values{"Username": {"viewer"}, "Pw": {pw}}), "").Code
	}
	if signIn(testPassword) != 401 || signIn(next) != 200 {
		t.Fatal("login does not use the new password")
	}
	// JSON as Emby Web sends it with reqformat=json.
	expectStatus(t, request(h, "POST", path+"?reqformat=json", "text/plain", `{"CurrentPw":"`+next+`","NewPw":"`+testPassword+`"}`, token), 204)
}

// TestRemoveOwnPassword follows Emby: without a password the user signs in
// with a blank one and sets a new one with a blank current password.
func TestRemoveOwnPassword(t *testing.T) {
	store, h, _ := newTestServer(t)
	token := login(t, h)
	path := "/Users/" + testUser(store).ID + "/Password"
	const form = "application/x-www-form-urlencoded"
	signIn := func(pw string) int {
		return request(h, "POST", "/Users/authenticatebyname", form, url.Values{"Username": {"viewer"}, "Pw": {pw}}.Encode(), "").Code
	}
	expectStatus(t, request(h, "POST", path, form, url.Values{"ResetPassword": {"true"}}.Encode(), token), 204)
	if signIn(testPassword) != 401 || signIn("") != 200 {
		t.Fatal("login does not accept a blank password after the reset")
	}
	expectStatus(t, request(h, "POST", path, form, url.Values{"CurrentPw": {""}, "NewPw": {testPassword}}.Encode(), token), 204)
	if signIn("") != 401 || signIn(testPassword) != 200 {
		t.Fatal("login does not use the password set without one")
	}
}

// TestPasswordPermissions follows Emby from 4.8.0.38: the session suffices to
// set or remove one's own password, an administrator sets a user's, and only
// an administrator changes their own.
func TestPasswordPermissions(t *testing.T) {
	store, h, _ := newTestServer(t)
	admin := login(t, h)
	adminID := testUser(store).ID
	const form = "application/x-www-form-urlencoded"
	setPassword := func(id, token string, values url.Values) int {
		return request(h, "POST", "/Users/"+id+"/Password", form, values.Encode(), token).Code
	}
	signIn := func(name, pw string) (string, int) {
		w := request(h, "POST", "/Users/authenticatebyname", form, url.Values{"Username": {name}, "Pw": {pw}}.Encode(), "")
		var result struct{ AccessToken string }
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result.AccessToken, w.Code
	}
	other, err := store.CreateUser("Other", state.Policy{IsAdministrator: true, EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := store.CreateUser("Guest", state.Policy{EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range []url.Values{{"NewPw": {"admin-takes-over-1"}}, {"NewPw": {""}}, {"ResetPassword": {"true"}}} {
		if code := setPassword(other.ID, admin, values); code != 403 {
			t.Fatalf("another administrator's password %v: %d", values, code)
		}
	}
	if code := setPassword(guest.ID, admin, url.Values{"NewPw": {"guest-test-password"}}); code != 204 {
		t.Fatalf("a user's password: %d", code)
	}
	guestToken, code := signIn("guest", "guest-test-password")
	if code != 200 {
		t.Fatalf("guest sign-in: %d", code)
	}
	// Emby Web sends the current password empty; an empty new one removes it.
	if code := setPassword(guest.ID, guestToken, url.Values{"CurrentPw": {""}, "NewPw": {""}}); code != 204 {
		t.Fatalf("removing one's own password: %d", code)
	}
	if _, code := signIn("guest", ""); code != 200 {
		t.Fatalf("blank sign-in: %d", code)
	}
	if code := setPassword(adminID, admin, url.Values{"CurrentPw": {""}, "NewPw": {"admin-test-password"}}); code != 204 {
		t.Fatalf("an administrator's own password: %d", code)
	}
	if _, code := signIn("viewer", "admin-test-password"); code != 200 {
		t.Fatalf("administrator sign-in: %d", code)
	}
}
