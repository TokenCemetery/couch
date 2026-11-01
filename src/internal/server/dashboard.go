package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/logfile"
	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
	"github.com/TokenCemetery/couch/internal/state"
)

// activeWindow is how recently a session must have made a request to be
// listed as active on the dashboard.
const activeWindow = 10 * time.Minute

// nowPlaying is what a session plays, from its playback reports.
type nowPlaying struct {
	item     string
	position int64
	paused   bool
}

// SetRefresher lets administrators run the metadata refresh as a task.
func (s *Server) SetRefresher(r *metadata.Refresher) {
	s.refresher = r
	var last *metadata.Result
	r.OnChange(func() {
		s.publishTasks()
		running, result := r.Status()
		if running || result == nil || result == last {
			return
		}
		last = result
		// Every published catalogue starts a refresh, so a completed one is
		// recorded only when an administrator ran the task.
		if result.Complete {
			if s.refreshRequested.Swap(false) {
				s.record(state.Activity{Type: "MetadataRefreshCompleted", Name: "Metadata refresh completed", Severity: "Info"})
			}
		} else {
			s.record(state.Activity{Type: "MetadataRefreshStopped", Name: "Metadata refresh stopped before it finished", Severity: "Warn"})
		}
	})
}

// SetLogDir lets administrators read the server log files in dir.
func (s *Server) SetLogDir(dir string) { s.logDir = dir }

// serverName is the name set in Emby Web, or -name.
func (s *Server) serverName() string {
	if name := s.store.Identity().ServerName; name != "" {
		return name
	}
	return s.name
}

// record adds an activity log entry. A failure is logged, not returned: the
// action it records has already happened.
func (s *Server) record(a state.Activity) {
	if a.Date.IsZero() {
		a.Date = time.Now().UTC()
	}
	if err := s.store.AddActivity(context.Background(), a); err != nil {
		slog.Warn("Activity not recorded", "type", a.Type, "error", err)
	}
}

// userName returns the name of a user, for activity entries.
func (s *Server) userName(id string) string {
	return s.store.Identity().Users[id].Name
}

// touch notes that a session made a request.
func (s *Server) touch(sessionID string) {
	s.sessionMu.Lock()
	s.seen[sessionID] = time.Now()
	s.sessionMu.Unlock()
}

// played notes a session's playback report, and records the start and stop.
func (s *Server) played(session state.Session, event string, item media.Item, position int64, paused bool) {
	s.sessionMu.Lock()
	if event == "stop" {
		delete(s.playing, session.ID)
	} else {
		s.playing[session.ID] = nowPlaying{item: item.ID, position: position, paused: paused}
	}
	s.sessionMu.Unlock()
	switch event {
	case "start":
		s.record(state.Activity{Type: "VideoPlayback", Name: s.userName(session.UserID) + " is playing " + s.displayName(item) + " on " + session.DeviceName,
			Severity: "Info", UserID: session.UserID, ItemID: item.ID})
	case "stop":
		s.record(state.Activity{Type: "VideoPlaybackStopped", Name: s.userName(session.UserID) + " has finished playing " + s.displayName(item) + " on " + session.DeviceName,
			Severity: "Info", UserID: session.UserID, ItemID: item.ID})
	}
}

// sessionInfo describes a session as the dashboard lists it: when it was
// last seen and what it plays.
func (s *Server) sessionInfo(d state.Data, session state.Session) object {
	dto := sessionDTO(d, session)
	s.sessionMu.Lock()
	seen, playing := s.seen[session.ID], s.playing[session.ID]
	s.sessionMu.Unlock()
	if !seen.IsZero() {
		dto["LastActivityDate"] = embyDate(seen)
	}
	if playing.item == "" {
		return dto
	}
	item, found := s.findItem(playing.item)
	if !found {
		return dto
	}
	nowItem := baseFields(item.ID, s.displayName(item), item.Type(), d.ServerID)
	nowItem["RunTimeTicks"], nowItem["MediaType"] = item.RunTimeTicks, "Video"
	if item.Type() == "Episode" {
		nowItem["SeriesName"], nowItem["ParentIndexNumber"], nowItem["IndexNumber"] = s.seriesName(item), item.SeasonNumber, item.EpisodeNumber
	}
	dto["NowPlayingItem"] = nowItem
	dto["PlayState"] = object{"PositionTicks": playing.position, "IsPaused": playing.paused, "IsMuted": false, "CanSeek": true}
	return dto
}

// sessions answers Sessions: the caller's sessions, or with
// IncludeAllSessionsIfAdmin every user's for an administrator, active within
// activeWindow and filtered by IsPlaying, DeviceId and Id.
func (s *Server) sessions(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	d := s.store.Snapshot()
	all := strings.EqualFold(value(r, "IncludeAllSessionsIfAdmin"), "true") && userOf(d, session).Policy.IsAdministrator
	playingOnly := strings.EqualFold(value(r, "IsPlaying"), "true")
	device, id := value(r, "DeviceId"), value(r, "Id")
	list := []object{}
	for _, other := range d.Sessions {
		if !other.ExpiresAt.After(time.Now()) || (!all && other.UserID != session.UserID) ||
			(device != "" && other.DeviceID != device) || (id != "" && other.ID != id) {
			continue
		}
		s.sessionMu.Lock()
		seen, playing := s.seen[other.ID], s.playing[other.ID]
		s.sessionMu.Unlock()
		if other.ID != session.ID && time.Since(seen) > activeWindow {
			continue
		}
		if playingOnly && playing.item == "" {
			continue
		}
		list = append(list, s.sessionInfo(d, other))
	}
	slices.SortFunc(list, func(a, b object) int {
		last := func(o object) string { text, _ := o["LastActivityDate"].(string); return text }
		return strings.Compare(last(b), last(a))
	})
	respond(w, 200, list)
}

// Task keys and IDs of the scheduled tasks.
const metadataTaskKey = "RefreshMetadata"

var metadataTaskID = derive("task", metadataTaskKey)

// taskDTO describes a scheduled task.
func taskDTO(name, key, id, description string, running bool, start, end time.Time, failure string, hasResult bool) object {
	taskState := "Idle"
	if running {
		taskState = "Running"
	}
	task := object{"Name": name, "Key": key, "Id": id, "State": taskState, "Category": "Library",
		"Description": description, "IsHidden": false, "Triggers": []any{}}
	if hasResult {
		result := object{"StartTimeUtc": embyDate(start), "EndTimeUtc": embyDate(end), "Status": "Completed", "Name": name, "Key": key, "Id": id}
		if failure != "" {
			result["Status"], result["ErrorMessage"] = "Failed", failure
		}
		task["LastExecutionResult"] = result
	}
	return task
}

// scheduledTasks lists the library scan and the metadata refresh, each when
// this server runs it.
func (s *Server) scheduledTasks() []object {
	tasks := []object{}
	if s.libraries != nil {
		tasks = append(tasks, s.scanTask())
	}
	if s.refresher != nil {
		running, last := s.refresher.Status()
		task := taskDTO("Refresh metadata", metadataTaskKey, metadataTaskID,
			"Reads changed .nfo files and looks up items online that have no or aged metadata.", running, time.Time{}, time.Time{}, "", false)
		if last != nil {
			failure := ""
			if !last.Complete {
				failure = "The refresh stopped before it finished; it is retried."
			}
			task = taskDTO("Refresh metadata", metadataTaskKey, metadataTaskID, task["Description"].(string), running, last.Start, last.End, failure, true)
		}
		tasks = append(tasks, task)
	}
	return tasks
}

// scanTask is the library scan as a scheduled task.
func (s *Server) scanTask() object {
	running, last := s.libraries.Status()
	description := "Scans the library folders for new, changed and removed files."
	var task object
	if last == nil {
		task = taskDTO("Scan media library", scanTaskKey, scanTaskID, description, running, time.Time{}, time.Time{}, "", false)
	} else {
		task = taskDTO("Scan media library", scanTaskKey, scanTaskID, description, running, last.Start, last.End, last.Err, true)
	}
	if interval := s.libraries.Interval(); interval > 0 {
		task["Triggers"] = []object{{"Type": "IntervalTrigger", "IntervalTicks": int64(interval / 100)}}
	}
	return task
}

// publishTasks tells the administrators watching the tasks that one started
// or ended; Emby Web's task buttons wait for this message.
func (s *Server) publishTasks() {
	s.publish(object{"MessageType": "ScheduledTasksInfo", "Data": s.scheduledTasks()}, func(sock *socket) bool { return sock.tasks })
}

// fixedConfiguration is the server configuration Couch reports; only the
// server name can be changed. The rest describes what Couch does: no UPnP,
// remote access or HTTPS settings, English UI and fixed metadata sources.
func (s *Server) fixedConfiguration() object {
	return object{
		"ServerName": s.serverName(), "UICulture": "en-US", "IsStartupWizardCompleted": true,
		"EnableUPnP": false, "EnableRemoteAccess": false, "EnableHttps": false, "RequireHttps": false,
		"PublicPort": 8097, "HttpServerPortNumber": 8097, "PublicHttpsPort": 8920, "HttpsPortNumber": 8920,
		"PreferredMetadataLanguage": "", "MetadataCountryCode": "", "LogFileRetentionDays": 0,
		"EnableDebugLevelLogging": false, "RemoteClientBitrateLimit": 0, "LocalNetworkSubnets": []string{},
		"RemoteIPFilter": []string{}, "IsRemoteIPFilterBlacklist": false, "CachePath": "", "MetadataPath": "",
	}
}

func (s *Server) dashboardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sessions", s.protect(s.sessions))
	mux.HandleFunc("GET /system/activitylog/entries", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		query, err := catalogQuery(r)
		start, limit, ok := pageBounds(query["startindex"], query["limit"])
		var minDate time.Time
		if text := query["mindate"]; text != "" && err == nil {
			minDate, err = time.Parse(time.RFC3339Nano, text)
		}
		if err != nil || !ok {
			fail(w, 400, "InvalidQuery")
			return
		}
		entries, total, err := s.store.ActivityLog(r.Context(), minDate, 0, 1000)
		if err != nil {
			s.internalError(w)
			return
		}
		// HasUserId splits user activity from server events.
		if has := strings.ToLower(query["hasuserid"]); has == "true" || has == "false" {
			entries = slices.DeleteFunc(entries, func(a state.Activity) bool { return (a.UserID != "") != (has == "true") })
			total = len(entries)
		}
		items := []object{}
		for _, a := range entries[min(start, len(entries)):min(start+limit, len(entries))] {
			items = append(items, object{"Id": a.ID, "Name": a.Name, "Overview": a.Overview, "ShortOverview": a.Overview, "Type": a.Type,
				"ItemId": a.ItemID, "Date": embyDate(a.Date), "UserId": a.UserID, "Severity": a.Severity})
		}
		respond(w, 200, object{"Items": items, "TotalRecordCount": total})
	}))

	// Logs: the server log in the data directory, current and rotated.
	mux.HandleFunc("GET /system/logs/query", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		items := []object{}
		if s.logDir != "" {
			files, err := logfile.Files(s.logDir)
			if err != nil {
				s.internalError(w)
				return
			}
			for _, f := range files {
				items = append(items, object{"Name": f.Name, "Size": f.Size, "DateCreated": embyDate(f.Modified), "DateModified": embyDate(f.Modified)})
			}
		}
		respond(w, 200, object{"Items": items, "TotalRecordCount": len(items)})
	}))
	openLog := func(w http.ResponseWriter, r *http.Request) (io.ReadCloser, bool) {
		if s.logDir == "" {
			fail(w, 404, "NotFound")
			return nil, false
		}
		file, err := logfile.OpenFile(s.logDir, r.PathValue("name"))
		if err != nil {
			fail(w, 404, "NotFound")
			return nil, false
		}
		return file, true
	}
	mux.HandleFunc("GET /system/logs/{name}", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		file, ok := openLog(w, r)
		if !ok {
			return
		}
		defer func() { _ = file.Close() }()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.Copy(w, file)
	}))
	mux.HandleFunc("GET /system/logs/{name}/lines", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		file, ok := openLog(w, r)
		if !ok {
			return
		}
		defer func() { _ = file.Close() }()
		lines := []string{}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), 1<<20)
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		respond(w, 200, object{"Items": lines, "TotalRecordCount": len(lines)})
	}))

	// Devices: each device a session signed in from. Deleting one signs it
	// out; Couch keeps no custom device names.
	deviceInfo := func(d state.Data, id string) (object, bool) {
		var latest state.Session
		for _, session := range d.Sessions {
			if session.DeviceID == id && session.ExpiresAt.After(latest.ExpiresAt) {
				latest = session
			}
		}
		if latest.DeviceID == "" {
			return nil, false
		}
		return object{"Name": latest.DeviceName, "Id": id, "ReportedDeviceId": id, "LastUserId": latest.UserID,
			"LastUserName": d.Users[latest.UserID].Name, "AppName": latest.Client, "AppVersion": latest.Version,
			"DateLastActivity": embyDate(latest.ExpiresAt.Add(-state.SessionLifetime))}, true
	}
	mux.HandleFunc("GET /devices", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		d := s.store.Snapshot()
		ids := map[string]bool{}
		for _, other := range d.Sessions {
			if other.DeviceID != "" {
				ids[other.DeviceID] = true
			}
		}
		devices := []object{}
		for _, id := range slices.Sorted(maps.Keys(ids)) {
			device, _ := deviceInfo(d, id)
			device["InternalId"] = len(devices) + 1
			devices = append(devices, device)
		}
		respond(w, 200, object{"Items": devices, "TotalRecordCount": len(devices)})
	}))
	mux.HandleFunc("GET /devices/info", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		device, ok := deviceInfo(s.store.Snapshot(), value(r, "Id"))
		if !ok {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, device)
	}))
	mux.HandleFunc("GET /devices/options", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, object{"CustomName": ""})
	}))
	mux.HandleFunc("POST /devices/options", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var body struct{ CustomName string }
		if decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		if body.CustomName != "" {
			failText(w, 400, "Couch does not rename devices.")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	deleteDevice := s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		id := value(r, "Id")
		if id == "" || id == session.DeviceID {
			failText(w, 400, "You cannot delete the device you are using.")
			return
		}
		revoked, err := s.store.RevokeDevice(id)
		if err != nil {
			s.internalError(w)
			return
		}
		for _, sessionID := range revoked {
			s.disconnectSession(sessionID)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /devices", deleteDevice)
	mux.HandleFunc("POST /devices/delete", deleteDevice)

	// Scheduled tasks: the library scan and the metadata refresh.
	mux.HandleFunc("GET /scheduledtasks", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, s.scheduledTasks())
	}))
	task := func(r *http.Request) (object, bool) {
		for _, t := range s.scheduledTasks() {
			if t["Id"] == r.PathValue("task") {
				return t, true
			}
		}
		return nil, false
	}
	mux.HandleFunc("GET /scheduledtasks/{task}", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		t, ok := task(r)
		if !ok {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, t)
	}))
	mux.HandleFunc("POST /scheduledtasks/running/{task}", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		t, ok := task(r)
		if !ok {
			fail(w, 404, "NotFound")
			return
		}
		if t["Key"] == scanTaskKey {
			s.libraries.Scan()
		} else {
			s.refreshRequested.Store(true)
			s.refresher.RefreshNow()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	stop := s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		failText(w, 400, "Couch cannot stop a running task.")
	})
	mux.HandleFunc("DELETE /scheduledtasks/running/{task}", stop)
	mux.HandleFunc("POST /scheduledtasks/running/{task}/delete", stop)

	// Server settings: only the name can be changed; a change to anything
	// else is refused by name.
	mux.HandleFunc("GET /system/configuration", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, s.fixedConfiguration())
	}))
	mux.HandleFunc("POST /system/configuration", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var posted map[string]json.RawMessage
		if decodeJSON(w, r, &posted) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		reported := s.fixedConfiguration()
		var name string
		if raw, ok := posted["ServerName"]; ok && json.Unmarshal(raw, &name) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		delete(posted, "ServerName")
		if field := fixedChange(posted, reported, nil); field != "" {
			failText(w, 400, "Couch does not support changing the "+field+" setting.")
			return
		}
		name = strings.TrimSpace(name)
		if len(name) > 128 {
			failText(w, 400, "The server name must contain at most 128 bytes.")
			return
		}
		if name != s.serverName() {
			if name == s.name {
				name = ""
			}
			if err := s.store.SetServerName(name); err != nil {
				s.internalError(w)
				return
			}
			s.record(state.Activity{Type: "ServerNameChanged", Name: "The server was renamed " + s.serverName(), Severity: "Info", UserID: session.UserID})
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	// Named configurations (branding, Live TV, encoding and others) are not
	// settings Couch has: they read as empty, and a save is accepted only
	// when it changes nothing, as the General page's does.
	named := func(key string) object {
		if key == "branding" {
			return object{"LoginDisclaimer": "", "CustomCss": ""}
		}
		return object{}
	}
	mux.HandleFunc("GET /system/configuration/{key}", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, named(r.PathValue("key")))
	}))
	mux.HandleFunc("POST /system/configuration/{key}", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var posted map[string]json.RawMessage
		if decodeJSON(w, r, &posted) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		reported := named(r.PathValue("key"))
		for key, raw := range posted {
			if _, known := reported[key]; !known && !emptyJSON(raw) {
				failText(w, 400, "Couch does not support changing the "+key+" setting.")
				return
			}
		}
		if field := fixedChange(posted, reported, nil); field != "" {
			failText(w, 400, "Couch does not support changing the "+field+" setting.")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /localization/options", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, []object{{"Name": "English (United States)", "Value": "en-US"}})
	}))

	// Features Couch does not have: their pages list nothing.
	for _, path := range []string{"/plugins", "/livetv/tunerhosts", "/livetv/listingproviders", "/system/releasenotes/versions", "/system/wakeonlaninfo",
		"/encoding/codecconfiguration/defaults", "/encoding/codecinformation/video"} {
		mux.HandleFunc("GET "+path, s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			respond(w, 200, []any{})
		}))
	}
	for _, path := range []string{"/auth/keys", "/sync/jobs"} {
		mux.HandleFunc("GET "+path, s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			respond(w, 200, object{"Items": []any{}, "TotalRecordCount": 0})
		}))
	}
	// The dashboard's restart and shut down buttons: Couch is run by a
	// service manager or a container runtime, which restarts it.
	for _, path := range []string{"/system/restart", "/system/shutdown"} {
		mux.HandleFunc("POST "+path, s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			failText(w, 400, "Restart or stop Couch with its service manager or container runtime.")
		}))
	}
	mux.HandleFunc("POST /auth/keys", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		failText(w, 400, "Couch does not issue API keys: sign in as a user instead.")
	}))
	mux.HandleFunc("GET /plugins/securityinfo", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, object{"SupporterKey": "", "IsMBSupporter": false})
	}))
	mux.HandleFunc("GET /encoding/tonemapoptions", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, object{})
	}))
}

// emptyJSON reports whether raw is null, false, zero, an empty string, list
// or object.
func emptyJSON(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch v := v.(type) {
	case nil:
		return true
	case bool:
		return !v
	case float64:
		return v == 0
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}
