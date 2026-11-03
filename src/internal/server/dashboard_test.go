package server

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
	"github.com/TokenCemetery/couch/internal/state"
)

func TestDashboard(t *testing.T) {
	store, _, _ := newTestServer(t)
	api := New(store, "Couch test", nil, &media.Catalog{ID: "movies", Items: []media.Item{playbackMovie()}})
	refresher, err := metadata.NewRefresher(store, metadata.Config{Language: "en"}, func(metadata.Library) {})
	if err != nil {
		t.Fatal(err)
	}
	api.SetRefresher(refresher)
	logs := t.TempDir()
	if err := os.WriteFile(filepath.Join(logs, "couch.log"), []byte("first line\nsecond line\n"), 0600); err != nil {
		t.Fatal(err)
	}
	api.SetLogDir(logs)
	h := api.Handler()
	form := "application/x-www-form-urlencoded"
	signIn := func(name, pw, device string) (string, int) {
		w := request(h, "POST", "/Users/authenticatebyname?X-Emby-Client=Emby+Web&X-Emby-Device-Id="+device+"&X-Emby-Device-Name="+device, form,
			url.Values{"Username": {name}, "Pw": {pw}}.Encode(), "")
		var result struct{ AccessToken string }
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result.AccessToken, w.Code
	}
	decode := func(path, token string, v any) {
		t.Helper()
		w := request(h, "GET", path, "", "", token)
		expectStatus(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := signIn("viewer", testPassword, "laptop")
	if _, code := signIn("viewer", "wrong-password-here", "laptop"); code != 401 {
		t.Fatalf("failed sign-in: %d", code)
	}
	guest, err := store.CreateUser("guest", state.Policy{EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPassword(admin, guest.ID, "guest-test-password"); err != nil {
		t.Fatal(err)
	}
	user, _ := signIn("guest", "guest-test-password", "phone")

	// The guest plays; the administrator sees every active session and what
	// it plays, the guest only their own.
	expectStatus(t, request(h, "POST", "/Sessions/Playing", "application/json", `{"ItemId":"movie","PositionTicks":5000000,"PlaySessionId":"p1"}`, user), 204)
	var sessions []struct {
		UserName       string
		NowPlayingItem *struct{ Id string }
		PlayState      struct{ PositionTicks int64 }
	}
	decode("/Sessions?IncludeAllSessionsIfAdmin=true&IsPlaying=true", admin, &sessions)
	if len(sessions) != 1 || sessions[0].UserName != "guest" || sessions[0].NowPlayingItem == nil || sessions[0].NowPlayingItem.Id != "movie" || sessions[0].PlayState.PositionTicks != 5000000 {
		t.Fatalf("playing sessions %+v", sessions)
	}
	decode("/Sessions?IncludeAllSessionsIfAdmin=true", user, &sessions)
	if len(sessions) != 1 || sessions[0].UserName != "guest" {
		t.Fatalf("a user sees %+v", sessions)
	}
	expectStatus(t, request(h, "POST", "/Sessions/Playing/Stopped", "application/json", `{"ItemId":"movie","PositionTicks":6000000,"PlaySessionId":"p1"}`, user), 204)
	decode("/Sessions?IncludeAllSessionsIfAdmin=true&IsPlaying=true", admin, &sessions)
	if len(sessions) != 0 {
		t.Fatalf("still playing %+v", sessions)
	}

	// The activity log has the sign-ins, the failure and the playback.
	var activity struct {
		Items []struct {
			Type, Name, UserId, Severity string
		}
		TotalRecordCount int
	}
	decode("/System/ActivityLog/Entries?StartIndex=0&Limit=50&MinDate=2000-01-01T00:00:00.000Z", admin, &activity)
	types := []string{}
	for _, a := range activity.Items {
		types = append(types, a.Type)
	}
	if strings.Join(types, ",") != "VideoPlaybackStopped,VideoPlayback,AuthenticationSucceeded,AuthenticationFailed,AuthenticationSucceeded" {
		t.Fatalf("activity %v", types)
	}
	decode("/System/ActivityLog/Entries?HasUserId=false", admin, &activity)
	if activity.TotalRecordCount != 1 || activity.Items[0].Type != "AuthenticationFailed" || activity.Items[0].Severity != "Warn" {
		t.Fatalf("server events %+v", activity)
	}

	// Deleting a device signs it out; the caller's own device is kept.
	expectStatus(t, request(h, "POST", "/Devices/Delete?Id=laptop", "", "", admin), 400)
	expectStatus(t, request(h, "POST", "/Devices/Delete?Id=phone", "", "", admin), 204)
	expectStatus(t, request(h, "GET", "/Users/me", "", "", user), 401)

	// Only the server name can be changed, and it is saved.
	var config map[string]any
	decode("/System/Configuration", admin, &config)
	config["ServerName"] = "Living room"
	body, _ := json.Marshal(config)
	expectStatus(t, request(h, "POST", "/System/Configuration", "application/json", string(body), admin), 204)
	var info struct{ ServerName string }
	decode("/System/Info/Public", "", &info)
	if info.ServerName != "Living room" || store.Snapshot().ServerName != "Living room" {
		t.Fatalf("server name %q", info.ServerName)
	}
	config["UICulture"] = "de-DE"
	body, _ = json.Marshal(config)
	if w := request(h, "POST", "/System/Configuration", "application/json", string(body), admin); w.Code != 400 || !strings.Contains(w.Body.String(), "UICulture") {
		t.Fatalf("fixed setting: %d %s", w.Code, w.Body)
	}

	// The Database page saves its four fields through Partial: the two
	// maintenance settings are stored, the name is kept, the analysis limit
	// is fixed and the cache size, which Couch does not report, is ignored.
	if config["VacuumDatabaseOnStartup"] != false || config["OptimizeDatabaseOnShutdown"] != true || config["DatabaseAnalysisLimit"] != float64(400) {
		t.Fatalf("database settings %v %v %v", config["VacuumDatabaseOnStartup"], config["OptimizeDatabaseOnShutdown"], config["DatabaseAnalysisLimit"])
	}
	page := `{"DatabaseCacheSizeMB":"","OptimizeDatabaseOnShutdown":false,"DatabaseAnalysisLimit":"400","VacuumDatabaseOnStartup":true}`
	expectStatus(t, request(h, "POST", "/System/Configuration/Partial", "application/json", page, admin), 204)
	if d := store.Snapshot(); !d.VacuumOnStartup || d.OptimizeOnShutdown || api.serverName() != "Living room" {
		t.Fatalf("after the Database page: vacuum %v, optimize %v, name %q", d.VacuumOnStartup, d.OptimizeOnShutdown, api.serverName())
	}
	decode("/System/Configuration", admin, &config)
	if config["VacuumDatabaseOnStartup"] != true || config["OptimizeDatabaseOnShutdown"] != false {
		t.Fatalf("reported %v %v", config["VacuumDatabaseOnStartup"], config["OptimizeDatabaseOnShutdown"])
	}
	if w := request(h, "POST", "/System/Configuration/Partial", "application/json", `{"DatabaseAnalysisLimit":"1000"}`, admin); w.Code != 400 || !strings.Contains(w.Body.String(), "DatabaseAnalysisLimit") {
		t.Fatalf("analysis limit change: %d %s", w.Code, w.Body)
	}
	if w := request(h, "POST", "/System/Configuration/Partial", "application/json", `{"DatabaseCacheSizeMB":"256"}`, admin); w.Code != 400 || !strings.Contains(w.Body.String(), "DatabaseCacheSizeMB") {
		t.Fatalf("cache size: %d %s", w.Code, w.Body)
	}
	// A full save that leaves them out keeps them.
	expectStatus(t, request(h, "POST", "/System/Configuration", "application/json", `{"ServerName":"Living room"}`, admin), 204)
	if d := store.Snapshot(); !d.VacuumOnStartup || d.OptimizeOnShutdown {
		t.Fatalf("after a full save without them: vacuum %v, optimize %v", d.VacuumOnStartup, d.OptimizeOnShutdown)
	}
	notAdmin, _ := signIn("guest", "guest-test-password", "watch")
	expectStatus(t, request(h, "POST", "/System/Configuration/Partial", "application/json", `{"VacuumDatabaseOnStartup":false}`, notAdmin), 403)

	// A named configuration saved unchanged is accepted, a change refused.
	expectStatus(t, request(h, "POST", "/System/Configuration/branding", "application/json", `{"LoginDisclaimer":"","CustomCss":"","SplashscreenEnabled":false}`, admin), 204)
	if w := request(h, "POST", "/System/Configuration/branding", "application/json", `{"LoginDisclaimer":"Welcome"}`, admin); w.Code != 400 || !strings.Contains(w.Body.String(), "LoginDisclaimer") {
		t.Fatalf("branding change: %d %s", w.Code, w.Body)
	}

	// Logs: listed, opened by name, nothing else.
	var files struct{ Items []struct{ Name string } }
	decode("/System/Logs/Query", admin, &files)
	if len(files.Items) != 1 || files.Items[0].Name != "couch.log" {
		t.Fatalf("logs %+v", files)
	}
	if w := request(h, "GET", "/System/Logs/couch.log", "", "", admin); w.Body.String() != "first line\nsecond line\n" {
		t.Fatalf("log %q", w.Body)
	}
	var lines struct{ Items []string }
	decode("/System/Logs/couch.log/Lines", admin, &lines)
	if len(lines.Items) != 2 {
		t.Fatalf("lines %v", lines.Items)
	}
	if err := os.WriteFile(filepath.Join(logs, "secret.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, request(h, "GET", "/System/Logs/secret.txt", "", "", admin), 404)

	// The metadata refresh is a task next to nothing else here.
	var tasks []struct{ Key, Id string }
	decode("/ScheduledTasks", admin, &tasks)
	if len(tasks) != 1 || tasks[0].Key != "RefreshMetadata" {
		t.Fatalf("tasks %+v", tasks)
	}
	expectStatus(t, request(h, "POST", "/ScheduledTasks/Running/"+tasks[0].Id, "", "", admin), 204)

	// Pages of features Couch does not have open with empty lists.
	for _, path := range []string{"/Plugins", "/Auth/Keys", "/Sync/Jobs", "/LiveTv/TunerHosts", "/Plugins/SecurityInfo",
		"/Encoding/ToneMapOptions", "/System/Configuration/livetv", "/System/ReleaseNotes/Versions", "/Localization/Options"} {
		expectStatus(t, request(h, "GET", path, "", "", admin), 200)
	}

	// None of it is open to a user who is not an administrator.
	other, _ := signIn("guest", "guest-test-password", "tablet")
	for _, path := range []string{"/System/ActivityLog/Entries", "/System/Logs/Query", "/System/Configuration", "/Devices", "/ScheduledTasks", "/Plugins"} {
		expectStatus(t, request(h, "GET", path, "", "", other), 403)
	}
}
