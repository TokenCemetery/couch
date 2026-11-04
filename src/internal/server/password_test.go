package server

import (
	"net/url"
	"strings"
	"testing"
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
		// Emby Web sends the current password empty.
		{path, form(url.Values{"CurrentPw": {""}, "NewPw": {next}}), 400},
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
