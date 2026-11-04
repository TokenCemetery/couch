// Package server implements the Emby-compatible HTTP API, WebSocket events, and
// Emby Web asset serving.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/TokenCemetery/couch/internal/state"
)

var authParameter = regexp.MustCompile(`([A-Za-z][A-Za-z0-9_-]*)="([^"\r\n]*)"`)

// Request values are case-insensitive in the observed Emby client contract.
func values(r *http.Request, key string) []string {
	var result []string
	for k, vv := range r.URL.Query() {
		if strings.EqualFold(k, key) {
			result = append(result, vv...)
		}
	}
	result = append(result, r.Header.Values(key)...)
	return result
}

func value(r *http.Request, key string) string {
	v := values(r, key)
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

func authorization(r *http.Request) map[string][]string {
	result := map[string][]string{}
	for _, header := range []string{"Authorization", "X-Emby-Authorization"} {
		for _, v := range r.Header.Values(header) {
			scheme, rest, ok := strings.Cut(v, " ")
			if !ok || (!strings.EqualFold(scheme, "Emby") && !strings.EqualFold(scheme, "MediaBrowser")) {
				continue
			}
			for _, pair := range authParameter.FindAllStringSubmatch(rest, -1) {
				k := strings.ToLower(pair[1])
				result[k] = append(result[k], pair[2])
			}
		}
	}
	return result
}

func requestToken(r *http.Request) (string, error) {
	candidates := authorization(r)["token"]
	for _, key := range []string{"X-Emby-Token", "X-MediaBrowser-Token", "api_key"} {
		candidates = append(candidates, values(r, key)...)
	}
	var token string
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if len(candidate) > 512 || (token != "" && token != candidate) {
			return "", errors.New("conflicting or invalid authentication")
		}
		token = candidate
	}
	return token, nil
}

func clientInfo(r *http.Request) state.Session {
	auth := authorization(r)
	get := func(header, legacy string) string {
		v := value(r, header)
		if v == "" && len(auth[legacy]) > 0 {
			v = auth[legacy][0]
		}
		if len(v) > 256 {
			v = v[:256]
		}
		return v
	}
	return state.Session{Client: get("X-Emby-Client", "client"), DeviceID: get("X-Emby-Device-Id", "deviceid"), DeviceName: get("X-Emby-Device-Name", "device"), Version: get("X-Emby-Client-Version", "version")}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

// readFields reads the string fields Emby clients post as a form or as JSON
// (Emby Web sends JSON as text/plain with reqformat=json). Names are returned
// in lower case; booleans become "true"/"false" and other JSON values are
// ignored. On failure the response has been written.
func readFields(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	fields := map[string]string{}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case ct == "application/x-www-form-urlencoded":
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if r.ParseForm() != nil {
			fail(w, 400, "InvalidRequest")
			return nil, false
		}
		for k, v := range r.PostForm {
			if len(v) != 1 {
				fail(w, 400, "InvalidRequest")
				return nil, false
			}
			fields[strings.ToLower(k)] = v[0]
		}
	case ct == "application/json" || (ct == "text/plain" && strings.EqualFold(value(r, "reqformat"), "json")):
		var body map[string]any
		if decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return nil, false
		}
		for k, v := range body {
			switch v := v.(type) {
			case string:
				fields[strings.ToLower(k)] = v
			case bool:
				fields[strings.ToLower(k)] = strconv.FormatBool(v)
			}
		}
	default:
		fail(w, 415, "UnsupportedMediaType")
		return nil, false
	}
	return fields, true
}

// verifying applies the login limit to any request that checks a password. It
// returns false after writing 429; otherwise release must be called.
func (s *Server) verifying(w http.ResponseWriter, r *http.Request) (release func(), ok bool) {
	if !s.logins.allow(clientAddress(r)) {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "TooManyRequests")
		return nil, false
	}
	select {
	case s.logins.active <- struct{}{}:
		return func() { <-s.logins.active }, true
	default:
		w.Header().Set("Retry-After", "1")
		fail(w, 429, "TooManyRequests")
		return nil, false
	}
}

// changePassword serves the password forms, which post CurrentPw and NewPw,
// or ResetPassword. As in Emby from 4.8.0.38, the session suffices: a user
// sets their own password without the current one, which is verified only
// when a client sends it, and an administrator sets a user's. Another
// administrator's password is theirs alone. An empty NewPw or ResetPassword
// removes the password, and the user signs in with a blank one. manage has
// checked the caller may act on target.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, token string, session state.Session, target state.User) {
	release, ok := s.verifying(w, r)
	if !ok {
		return
	}
	defer release()
	fields, ok := readFields(w, r)
	if !ok {
		return
	}
	self := target.ID == session.UserID
	var revoked []string
	var err error
	current, next := fields["currentpw"], fields["newpw"]
	if fields["resetpassword"] == "true" {
		next = ""
	}
	switch {
	// The client's error dialog shows a plain-text answer as written.
	case !self && target.Policy.IsAdministrator:
		failText(w, 403, "Only that administrator can change an administrator's password.")
		return
	case len(current) > 1024:
		fail(w, 400, "InvalidRequest")
		return
	// Emby Web hides the current-password field for servers from 4.8.0.38 and
	// sends it empty.
	case self && current != "" && fields["resetpassword"] != "true":
		revoked, err = s.store.ChangePassword(token, current, next)
	default:
		revoked, err = s.store.SetPassword(token, target.ID, next)
	}
	switch {
	case errors.Is(err, state.ErrPassword):
		failText(w, 400, "The new password must contain at most 1024 bytes.")
		return
	case errors.Is(err, state.ErrCredentials):
		failText(w, 401, "The current password is incorrect.")
		return
	case errors.Is(err, state.ErrUserNotFound):
		fail(w, 404, "NotFound")
		return
	case err != nil:
		s.changed(w, err)
		return
	}
	for _, id := range revoked {
		s.disconnectSession(id)
	}
	s.record(state.Activity{Type: "UserPasswordChanged", Name: "The password of " + target.Name + " was changed", Severity: "Info", UserID: session.UserID})
	w.WriteHeader(http.StatusNoContent)
}

// Login attempts are limited per client address within a one-minute window,
// so one LAN host cannot lock the owner out (#59). A global cap still bounds
// brute force spread over many addresses, and a semaphore caps concurrent
// password hashing. Addresses are only recorded for allowed attempts, so the
// map never holds more than globalLogins keys.
const (
	addressLogins = 10
	globalLogins  = 100
)

type loginLimit struct {
	mu        sync.Mutex
	start     time.Time
	count     int
	addresses map[netip.Addr]int
	active    chan struct{}
}

func (l *loginLimit) allow(addr netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.addresses == nil || time.Since(l.start) >= time.Minute {
		l.start, l.count, l.addresses = time.Now(), 0, map[netip.Addr]int{}
	}
	if l.count >= globalLogins || l.addresses[addr] >= addressLogins {
		return false
	}
	l.count++
	l.addresses[addr]++
	return true
}

// clientAddress is the TCP peer address; forwarding headers are not trusted.
// IPv6 addresses are reduced to their /64 network, which one host controls.
func clientAddress(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr := ap.Addr().Unmap()
	if addr.Is6() {
		prefix, _ := addr.Prefix(64)
		addr = prefix.Addr()
	}
	return addr
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	release, ok := s.verifying(w, r)
	if !ok {
		return
	}
	defer release()
	fields, ok := readFields(w, r)
	if !ok {
		return
	}
	username, pw := fields["username"], fields["pw"]
	if username == "" || len(username) > 128 || len(pw) > 1024 {
		fail(w, 400, "InvalidRequest")
		return
	}
	token, session, err := s.store.Login(username, pw, clientInfo(r))
	if errors.Is(err, state.ErrCredentials) {
		s.record(state.Activity{Type: "AuthenticationFailed", Name: "Failed sign-in for " + username,
			Overview: "From " + clientAddress(r).String(), Severity: "Warn"})
		fail(w, 401, "Unauthorized")
		return
	}
	if errors.Is(err, state.ErrSessionLimit) {
		fail(w, 429, "SessionLimitReached")
		return
	}
	if err != nil {
		s.internalError(w)
		return
	}
	s.touch(session.ID)
	s.record(state.Activity{Type: "AuthenticationSucceeded", Name: s.userName(session.UserID) + " signed in from " + session.Client + " on " + session.DeviceName,
		Overview: "From " + clientAddress(r).String(), Severity: "Info", UserID: session.UserID})
	d := s.store.Snapshot()
	respond(w, 200, object{"User": s.userDTO(d, userOf(d, session), token), "SessionInfo": sessionDTO(d, session), "AccessToken": token, "ServerId": d.ServerID})
}

type authenticated func(http.ResponseWriter, *http.Request, string, state.Session)

// admin is protect for operations only an administrator may perform.
func (s *Server) admin(next authenticated) http.HandlerFunc {
	return s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !userOf(s.store.Snapshot(), session).Policy.IsAdministrator {
			fail(w, 403, "Forbidden")
			return
		}
		next(w, r, token, session)
	})
}

// protect authenticates the request and refuses one whose UserId or {user}
// names another user.
func (s *Server) protect(next authenticated) http.HandlerFunc {
	return s.protectAny(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		ids := append(values(r, "UserId"), r.PathValue("user"))
		for _, id := range ids {
			if id != "" && !strings.EqualFold(id, session.UserID) {
				fail(w, 403, "Forbidden")
				return
			}
		}
		next(w, r, token, session)
	})
}

// protectAny authenticates the request whichever user it names.
func (s *Server) protectAny(next authenticated) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := requestToken(r)
		if err != nil {
			fail(w, 400, "InvalidAuthentication")
			return
		}
		session, err := s.store.Authenticate(token)
		if err != nil {
			fail(w, 401, "Unauthorized")
			return
		}
		s.touch(session.ID)
		next(w, r, token, session)
	}
}
