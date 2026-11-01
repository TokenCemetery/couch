package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	contractPath = "../api/openapi.yaml"
	testUser     = "viewer"
	testPassword = "e2e-only-password-not-a-real-secret" //nolint:gosec // test-only credential for a throwaway instance
)

// Suite holds what every server instance shares: the binary, the read-only
// media directory and an initialized state directory copied per instance.
type Suite struct {
	Binary   string
	Media    string
	Template string
}

// Fixtures are entity IDs discovered through the API of a started instance.
type Fixtures struct {
	UserID        string
	SessionID     string
	DeviceID      string
	LibraryID     string
	MovieID       string
	MediaSourceID string
	SubtitleIndex string
	SeriesID      string
	SeasonID      string
	EpisodeID     string
}

// Instance is one running couch process with a logged-in session.
type Instance struct {
	URL   string
	Token string
	IDs   Fixtures

	cmd    *exec.Cmd
	args   []string
	stderr *bytes.Buffer
	data   string
	port   string
}

var instanceCount atomic.Int64

// NewSuite builds the binary (unless COUCH_BINARY is set), generates media
// with FFmpeg and initializes the template state under dir.
func NewSuite(dir string) (*Suite, error) {
	s := &Suite{Binary: os.Getenv("COUCH_BINARY"), Media: filepath.Join(dir, "media"), Template: filepath.Join(dir, "template")}
	if s.Binary == "" {
		s.Binary = filepath.Join(dir, "couch")
		build := exec.CommandContext(context.Background(), "go", "build", "-o", s.Binary, "./cmd/couch") //nolint:gosec // output path is a temp dir created by the test
		build.Dir = "../src"
		if out, err := build.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("build couch: %w\n%s", err, out)
		}
	}
	if err := generateMedia(s.Media); err != nil {
		return nil, err
	}
	initCmd := exec.CommandContext(context.Background(), s.Binary, "-init", "-data", s.Template, "-username", testUser) //nolint:gosec // binary is built by the test or chosen by COUCH_BINARY
	initCmd.Stdin = strings.NewReader(testPassword + "\n")
	if out, err := initCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("init couch: %w\n%s", err, out)
	}
	return s, nil
}

// Start launches a fresh instance on a copy of the template state, logs in
// and discovers fixture IDs. The caller must Stop it.
func (s *Suite) Start() (*Instance, error) {
	data, err := os.MkdirTemp("", "couch-e2e-data-")
	if err != nil {
		return nil, err
	}
	if err := copyDir(s.Template, data); err != nil {
		_ = os.RemoveAll(data)
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		_ = os.RemoveAll(data)
		return nil, err
	}
	inst := &Instance{URL: "http://127.0.0.1:" + port, stderr: &bytes.Buffer{}, data: data, port: port}
	inst.args = []string{s.Binary, "-listen", "127.0.0.1:" + port, "-data", data, "-media-dir", s.Media}
	inst.cmd = exec.CommandContext(context.Background(), inst.args[0], inst.args[1:]...) //nolint:gosec // see NewSuite
	inst.cmd.Stderr = inst.stderr
	if err := inst.cmd.Start(); err != nil {
		releasePort(port)
		_ = os.RemoveAll(data)
		return nil, err
	}
	if err := inst.waitReady(); err == nil {
		err = inst.login()
		if err == nil {
			err = inst.discover()
		}
		if err == nil {
			err = inst.uploadAvatar()
		}
		if err != nil {
			inst.Stop()
			return nil, fmt.Errorf("%w\n%s", err, inst.stderr)
		}
	} else {
		inst.Stop()
		return nil, fmt.Errorf("%w\n%s", err, inst.stderr)
	}
	return inst, nil
}

// KillAndRestart stops the process with SIGKILL, as a power cut or OOM kill
// would, and starts it again on the same state, address and media.
func (inst *Instance) KillAndRestart() error {
	_ = inst.cmd.Process.Kill()
	_ = inst.cmd.Wait()
	return inst.restart()
}

// Restart stops the process with SIGTERM, runs offline on its data directory,
// as an operator would with Couch stopped, and starts it again on the same
// state, address and media.
func (inst *Instance) Restart(offline func(data string) error) error {
	inst.terminate()
	if err := offline(inst.data); err != nil {
		return err
	}
	return inst.restart()
}

// Backup runs the binary with -backup on the running instance's data
// directory, as an operator would with Couch running.
func (inst *Instance) Backup(dest string) error {
	out, err := exec.CommandContext(context.Background(), inst.args[0], "-data", inst.data, "-backup", dest).CombinedOutput() //nolint:gosec // see NewSuite
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// restart starts the stopped process again with the same arguments.
func (inst *Instance) restart() error {
	inst.cmd = exec.CommandContext(context.Background(), inst.args[0], inst.args[1:]...) //nolint:gosec // see NewSuite
	inst.cmd.Stderr = inst.stderr
	if err := inst.cmd.Start(); err != nil {
		return err
	}
	if err := inst.waitReady(); err != nil {
		return fmt.Errorf("%w\n%s", err, inst.stderr)
	}
	return nil
}

// Stop terminates the process, removes its state and frees its port.
func (inst *Instance) Stop() {
	inst.terminate()
	_ = os.RemoveAll(inst.data)
	releasePort(inst.port)
}

// terminate stops the process with SIGTERM, or SIGKILL after 10 seconds.
func (inst *Instance) terminate() {
	if inst.cmd.Process != nil {
		_ = inst.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = inst.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = inst.cmd.Process.Kill()
			<-done
		}
	}
}

func (inst *Instance) waitReady() error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, inst.URL+"/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if inst.cmd.ProcessState != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("couch did not become ready")
}

// AuthHeader is the client identification Emby clients send on every request.
func (inst *Instance) AuthHeader() string {
	return fmt.Sprintf(`Emby Client="Couch e2e", Device="e2e", DeviceId=%q, Version="0.1.0"`, inst.IDs.DeviceID)
}

// Response is a fully read HTTP response.
type Response struct {
	Status      int
	ContentType string
	Header      http.Header
	Body        []byte
}

var client = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// Send performs r, with the session token when authorized is true. Bodies are
// read up to 16 MiB.
func (inst *Instance) Send(r Request, authorized bool) (Response, error) {
	req, err := http.NewRequestWithContext(context.Background(), r.Method, r.URL(inst.URL), bytes.NewReader(r.Body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("X-Emby-Authorization", inst.AuthHeader())
	if authorized {
		req.Header.Set("X-Emby-Token", inst.Token)
	}
	if r.ContentType != "" {
		req.Header.Set("Content-Type", r.ContentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return Response{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Header: resp.Header, Body: body}, err
}

// Get performs an authorized GET and decodes the JSON response into out.
func (inst *Instance) Get(path string, out any) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, inst.URL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Emby-Authorization", inst.AuthHeader())
	req.Header.Set("X-Emby-Token", inst.Token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (inst *Instance) login() error {
	inst.IDs.DeviceID = fmt.Sprintf("couch-e2e-%d", instanceCount.Add(1))
	form := url.Values{"Username": {testUser}, "Pw": {testPassword}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, inst.URL+"/emby/Users/AuthenticateByName", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Emby-Authorization", inst.AuthHeader())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		AccessToken string
		User        struct{ Id string }
		SessionInfo struct{ Id string }
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("login: status %d: %s", resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	inst.Token, inst.IDs.UserID, inst.IDs.SessionID = result.AccessToken, result.User.Id, result.SessionInfo.Id
	return nil
}

// uploadAvatar gives the user an avatar the way Emby Web uploads one, so the
// user image operations have an image to serve and delete.
func (inst *Instance) uploadAvatar() error {
	r := Request{Method: "POST", Path: "/Users/" + inst.IDs.UserID + "/Images/Primary", Body: avatarBody(), ContentType: "image/png"}
	resp, err := inst.Send(r, true)
	if err == nil && resp.Status != http.StatusNoContent {
		err = fmt.Errorf("upload avatar: status %d: %s", resp.Status, snippet(resp.Body))
	}
	return err
}

// avatarBody is a 1x1 PNG encoded as base64, as Emby Web sends an avatar.
func avatarBody() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1)))
	return []byte(base64.StdEncoding.EncodeToString(b.Bytes()))
}

func (inst *Instance) discover() error {
	var views struct{ Items []struct{ Id string } }
	if err := inst.Get("/emby/Users/"+inst.IDs.UserID+"/Views", &views); err != nil {
		return err
	}
	if len(views.Items) > 0 {
		inst.IDs.LibraryID = views.Items[0].Id
	}
	var items struct {
		Items []struct {
			Id           string
			Type         string
			MediaSources []struct {
				Id           string
				MediaStreams []struct {
					Type  string
					Index int
				}
			}
		}
	}
	path := "/emby/Users/" + inst.IDs.UserID + "/Items?Recursive=true&IncludeItemTypes=Movie,Series,Season,Episode&Fields=MediaSources"
	if err := inst.Get(path, &items); err != nil {
		return err
	}
	for _, it := range items.Items {
		switch it.Type {
		case "Movie":
			if inst.IDs.MovieID != "" {
				continue
			}
			inst.IDs.MovieID = it.Id
			if len(it.MediaSources) > 0 {
				inst.IDs.MediaSourceID = it.MediaSources[0].Id
				for _, ms := range it.MediaSources[0].MediaStreams {
					if ms.Type == "Subtitle" && inst.IDs.SubtitleIndex == "" {
						inst.IDs.SubtitleIndex = fmt.Sprint(ms.Index)
					}
				}
			}
		case "Series":
			setOnce(&inst.IDs.SeriesID, it.Id)
		case "Season":
			setOnce(&inst.IDs.SeasonID, it.Id)
		case "Episode":
			setOnce(&inst.IDs.EpisodeID, it.Id)
		}
	}
	if inst.IDs.MovieID == "" || inst.IDs.SeriesID == "" || inst.IDs.EpisodeID == "" {
		return fmt.Errorf("media fixtures not found in catalog: %+v", inst.IDs)
	}
	return nil
}

func setOnce(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

// reservedPorts holds the ports of live instances. An instance keeps its
// port while it restarts, when the port is briefly unbound; without this a
// parallel Start could be handed it, and one of the two processes would fail
// to bind while the other answered both tests (#91).
var reservedPorts = struct {
	sync.Mutex
	ports map[string]bool
}{ports: map[string]bool{}}

// freePort returns a port that is unbound now and reserved until releasePort.
func freePort() (string, error) {
	for {
		l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		_, port, err := net.SplitHostPort(l.Addr().String())
		_ = l.Close()
		if err != nil {
			return "", err
		}
		reservedPorts.Lock()
		taken := reservedPorts.ports[port]
		reservedPorts.ports[port] = true
		reservedPorts.Unlock()
		if !taken {
			return port, nil
		}
	}
}

func releasePort(port string) {
	reservedPorts.Lock()
	delete(reservedPorts.ports, port)
	reservedPorts.Unlock()
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasSuffix(e.Name(), ".lock") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name())) //nolint:gosec // src is a directory the test created
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), raw, 0o600); err != nil { //nolint:gosec // dst is a directory the test created; names come from src
			return err
		}
	}
	return nil
}
