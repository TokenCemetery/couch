package server

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/TokenCemetery/couch/internal/state"
)

type managed func(w http.ResponseWriter, r *http.Request, token string, session state.Session, target state.User)

// manage authenticates like protect, but the user named by {user} may be
// another user when the caller is an administrator. With adminOnly the caller
// must be an administrator even to act on themself. A user who is not an
// administrator gets 403 for any other user, existing or not.
func (s *Server) manage(adminOnly bool, next managed) http.HandlerFunc {
	return s.protectAny(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		d := s.store.Snapshot()
		id := r.PathValue("user")
		if (adminOnly || id != session.UserID) && !userOf(d, session).Policy.IsAdministrator {
			fail(w, 403, "Forbidden")
			return
		}
		target, ok := d.Users[id]
		if !ok {
			fail(w, 404, "NotFound")
			return
		}
		next(w, r, token, session, target)
	})
}

// userFailure answers the user-management errors the client shows verbatim.
func (s *Server) userFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, state.ErrUserName):
		failText(w, 400, "The user name must contain 1 to 128 bytes.")
	// Emby Web's new-user page answers a 400 by showing a label its page
	// lacks, and fails silently; another status shows this text.
	case errors.Is(err, state.ErrUserNameTaken):
		failText(w, 409, "A user with this name already exists.")
	case errors.Is(err, state.ErrLastAdministrator):
		failText(w, 400, "Couch needs at least one enabled administrator.")
	case errors.Is(err, state.ErrUserNotFound):
		fail(w, 404, "NotFound")
	default:
		s.changed(w, err)
	}
}

// storedPolicy names the policy fields Couch stores; it reports the others
// with fixed values that match what it can do. A change to ignoredPolicy is
// dropped: Emby Web's new-user page sets the first two to the administrator
// role on its own, which the read-only library cannot grant, and channel
// access has no effect without channels.
var (
	storedPolicy  = []string{"IsAdministrator", "IsDisabled", "IsHidden", "EnableAllFolders", "EnabledFolders"}
	ignoredPolicy = []string{"EnableContentDeletion", "EnableSubtitleManagement", "EnableAllChannels", "EnabledChannels"}
)

// fixedPolicyChange returns the first posted policy field that Couch reports
// with a fixed value and that the post changes, or "".
func fixedPolicyChange(posted map[string]json.RawMessage, reported object) string {
	return fixedChange(posted, reported, slices.Concat(storedPolicy, ignoredPolicy))
}

// fixedChange returns the first posted field that reported has, that skip
// does not name, and whose value the post changes, or "". Fields reported
// lacks are ignored, null equals an empty list, and a number may be posted
// as text.
func fixedChange(posted map[string]json.RawMessage, reported object, skip []string) string {
	for _, key := range slices.Sorted(maps.Keys(posted)) {
		value, known := reported[key]
		if !known || slices.Contains(skip, key) {
			continue
		}
		var got, want any
		wantJSON, _ := json.Marshal(value)
		if json.Unmarshal(posted[key], &got) != nil || json.Unmarshal(wantJSON, &want) != nil {
			return key
		}
		if list, ok := want.([]any); ok && len(list) == 0 && got == nil {
			continue
		}
		// Select boxes post numbers as text, such as the stream limit.
		if text, ok := got.(string); ok {
			if number, err := strconv.ParseFloat(text, 64); err == nil {
				got = number
			}
		}
		if !reflect.DeepEqual(got, want) {
			return key
		}
	}
	return ""
}

// remarshal decodes already parsed JSON fields into v.
func remarshal(fields map[string]json.RawMessage, v any) error {
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// copyOptions are the "copy from user" choices of the new-user page that
// Couch performs.
var copyOptions = []object{{"Name": "User policy", "Id": "userpolicy"}, {"Name": "User configuration", "Id": "userconfiguration"}}

func (s *Server) userRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /users/{user}", s.manage(false, func(w http.ResponseWriter, r *http.Request, token string, session state.Session, target state.User) {
		respond(w, 200, s.userDTO(s.store.Snapshot(), target, token))
	}))
	mux.HandleFunc("POST /users/{user}/password", s.manage(false, s.changePassword))
	// The profile page posts the whole user; Couch takes only the name.
	mux.HandleFunc("POST /users/{user}", s.manage(false, func(w http.ResponseWriter, r *http.Request, token string, session state.Session, target state.User) {
		var body struct{ Name *string }
		if decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		if body.Name == nil || *body.Name == target.Name {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := s.store.RenameUser(target.ID, *body.Name); err != nil {
			s.userFailure(w, err)
			return
		}
		s.record(state.Activity{Type: "UserRenamed", Name: target.Name + " was renamed " + s.userName(target.ID), Severity: "Info", UserID: session.UserID})
		s.publishUser(target.ID, "UserUpdated")
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /users/{user}/policy", s.manage(true, func(w http.ResponseWriter, r *http.Request, token string, session state.Session, target state.User) {
		var raw map[string]json.RawMessage
		var body state.Policy
		if decodeJSON(w, r, &raw) != nil || remarshal(raw, &body) != nil || len(body.EnabledFolders) > 1000 {
			fail(w, 400, "InvalidRequest")
			return
		}
		// Per-user library access is not enforced yet (#116), so a policy
		// that limits it is refused instead of being saved and ignored.
		if !body.EnableAllFolders {
			failText(w, 400, "Couch cannot limit a user to some libraries yet: keep \"Enable access to all libraries\" on.")
			return
		}
		if field := fixedPolicyChange(raw, policy(s.hls != nil, target.Policy)); field != "" {
			failText(w, 400, "Couch does not support changing the "+field+" setting.")
			return
		}
		policy := state.Policy{IsAdministrator: body.IsAdministrator, IsDisabled: body.IsDisabled,
			IsHidden: body.IsHidden, EnableAllFolders: true, EnabledFolders: []string{}}
		revoked, err := s.store.SetPolicy(target.ID, policy)
		if err != nil {
			s.userFailure(w, err)
			return
		}
		for _, id := range revoked {
			s.disconnectSession(id)
		}
		// The profile page posts the policy on every save; only a change is
		// recorded.
		if !reflect.DeepEqual(target.Policy, policy) {
			s.record(state.Activity{Type: "UserPolicyUpdated", Name: "The policy of " + target.Name + " was changed", Severity: "Info", UserID: session.UserID})
		}
		s.publishUser(target.ID, "UserPolicyUpdated")
		w.WriteHeader(http.StatusNoContent)
	}))
	remove := s.manage(true, func(w http.ResponseWriter, r *http.Request, token string, session state.Session, target state.User) {
		if target.ID == session.UserID {
			failText(w, 400, "You cannot delete your own account.")
			return
		}
		revoked, err := s.store.DeleteUser(target.ID)
		if err != nil {
			s.userFailure(w, err)
			return
		}
		for _, id := range revoked {
			s.disconnectSession(id)
		}
		s.record(state.Activity{Type: "UserDeleted", Name: target.Name + " was deleted", Severity: "Info", UserID: session.UserID})
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /users/{user}", remove)
	mux.HandleFunc("POST /users/{user}/delete", remove)
	mux.HandleFunc("GET /users/query", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		query, err := catalogQuery(r)
		start, limit, ok := pageBounds(query["startindex"], query["limit"])
		if err != nil || !ok {
			fail(w, 400, "InvalidQuery")
			return
		}
		d := s.store.Snapshot()
		users := []any{}
		for _, u := range d.SortedUsers() {
			if (query["ishidden"] != "" && strings.EqualFold(query["ishidden"], "true") != u.Policy.IsHidden) ||
				(query["isdisabled"] != "" && strings.EqualFold(query["isdisabled"], "true") != u.Policy.IsDisabled) ||
				(query["namestartswithorgreater"] != "" && strings.ToLower(u.Name) < strings.ToLower(query["namestartswithorgreater"])) {
				continue
			}
			users = append(users, s.userDTO(d, u, token))
		}
		total := len(users)
		users = users[min(start, total):min(start+limit, total)]
		respond(w, 200, object{"Items": users, "TotalRecordCount": total})
	}))
	mux.HandleFunc("POST /users/new", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		fields, ok := readFields(w, r)
		if !ok {
			return
		}
		d := s.store.Snapshot()
		policy, configuration := state.Policy{EnableAllFolders: true}, map[string]json.RawMessage(nil)
		if from := strings.ToLower(fields["copyfromuserid"]); from != "" {
			source, exists := d.Users[from]
			if !exists {
				fail(w, 404, "NotFound")
				return
			}
			options := strings.Split(strings.ToLower(fields["usercopyoptions"]), ",")
			if slices.Contains(options, "userpolicy") {
				policy = source.Policy
			}
			if slices.Contains(options, "userconfiguration") {
				configuration = maps.Clone(source.Configuration)
			}
		}
		created, err := s.store.CreateUser(fields["name"], policy, configuration)
		if err != nil {
			s.userFailure(w, err)
			return
		}
		s.record(state.Activity{Type: "UserCreated", Name: created.Name + " was created", Severity: "Info", UserID: session.UserID})
		respond(w, 200, s.userDTO(s.store.Snapshot(), created, token))
	}))
	// "Copy Data to Users" in the users list, with the copyOptions choices.
	mux.HandleFunc("POST /users/{user}/copydata", s.manage(true, func(w http.ResponseWriter, r *http.Request, token string, session state.Session, target state.User) {
		var body struct {
			ToUserIds   []string
			CopyOptions []string
		}
		if decodeJSON(w, r, &body) != nil || len(body.ToUserIds) == 0 || len(body.ToUserIds) > 1000 {
			fail(w, 400, "InvalidRequest")
			return
		}
		var policy, configuration bool
		for _, option := range body.CopyOptions {
			switch strings.ToLower(option) {
			case "userpolicy":
				policy = true
			case "userconfiguration":
				configuration = true
			default:
				failText(w, 400, "Couch copies only the user policy and the user configuration.")
				return
			}
		}
		to := make([]string, len(body.ToUserIds))
		for i, id := range body.ToUserIds {
			to[i] = strings.ToLower(id)
		}
		revoked, err := s.store.CopyUserData(target.ID, to, policy, configuration)
		if err != nil {
			s.userFailure(w, err)
			return
		}
		for _, id := range revoked {
			s.disconnectSession(id)
		}
		for _, id := range to {
			if policy {
				s.publishUser(id, "UserPolicyUpdated")
			}
			if configuration {
				s.publishUser(id, "UserConfigurationUpdated")
			}
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /users/copydataoptions", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, object{"DataOptions": copyOptions})
	}))
	// Couch signs users in with its own passwords only.
	mux.HandleFunc("GET /auth/providers", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, []object{{"Name": "Couch", "Id": "couch"}})
	}))
	// The user pages list the libraries for the access settings.
	mux.HandleFunc("GET /library/selectablemediafolders", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		folders := []object{}
		serverID := s.store.Snapshot().ServerID
		for _, id := range s.libraryIDs() {
			dto := s.collectionDTO(id, serverID, false)
			folders = append(folders, object{"Name": dto["Name"], "Id": id, "Guid": dto["Guid"], "SubFolders": []any{}, "IsUserAccessConfigurable": true})
		}
		respond(w, 200, folders)
	}))
	mux.HandleFunc("GET /library/mediafolders", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		items := []any{}
		serverID := s.store.Snapshot().ServerID
		for _, id := range s.libraryIDs() {
			items = append(items, s.collectionDTO(id, serverID, false))
		}
		respond(w, 200, object{"Items": items, "TotalRecordCount": len(items)})
	}))
}
