package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
)

// TestPhotos scans a home videos library and walks Emby Web's requests for
// it: the library's tabs, folders as photo albums, a photo's image for the
// slideshow, home videos, Latest and photo playlists.
func TestPhotos(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required to make a home video")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required to scan the home video")
	}
	dir := t.TempDir()
	write := func(name string, img image.Image, encode func(*bytes.Buffer, image.Image) error) {
		t.Helper()
		var b bytes.Buffer
		if err := encode(&b, img); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	asJPEG := func(b *bytes.Buffer, img image.Image) error { return jpeg.Encode(b, img, nil) }
	asPNG := func(b *bytes.Buffer, img image.Image) error { return png.Encode(b, img) }
	write("Trip/Beach.jpg", image.NewGray(image.Rect(0, 0, 30, 20)), asJPEG)
	write("Trip/Day 2/Hill.png", image.NewGray(image.Rect(0, 0, 10, 40)), asPNG)
	write("Loose.png", image.NewGray(image.Rect(0, 0, 5, 5)), asPNG)
	clip := filepath.Join(dir, "Trip", "Clip.mp4")
	cmd := exec.CommandContext(context.Background(), ffmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc=duration=1:size=64x48", "-c:v", "libx264", "-pix_fmt", "yuv420p", clip) //nolint:gosec // test input
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clip: %v %s", err, out)
	}
	catalog, err := media.ScanLibraries(context.Background(), []media.Library{{ID: "home", Name: "Family", Type: media.HomeVideos, Paths: []string{dir}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	store, _, _ := newTestServer(t)
	api := New(store, "test", nil, catalog)
	t.Cleanup(api.Close)
	h := api.Handler()
	token := login(t, h)
	user := "/Users/" + testUser(store).ID
	type item struct {
		Id, Name, Type, MediaType, CollectionType, ParentId, PremiereDate string
		Width, Height, ChildCount                                         int
		IsFolder                                                          bool
		Subviews                                                          []string
		ImageTags                                                         map[string]string
		MediaSources                                                      []json.RawMessage
	}
	get := func(path string, v any) {
		t.Helper()
		w := request(h, "GET", path, "", "", token)
		expectStatus(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("%s: %s", path, w.Body)
		}
	}
	page := func(path string) []item {
		t.Helper()
		var p struct{ Items []item }
		get(path, &p)
		return p.Items
	}
	names := func(items []item) string {
		list := []string{}
		for _, i := range items {
			list = append(list, i.Name)
		}
		return strings.Join(list, ",")
	}

	// The library opens in Emby Web's videos view with Videos and Photos tabs.
	var library item
	get(user+"/Items/home", &library)
	if library.CollectionType != "homevideos" || strings.Join(library.Subviews, ",") != "videos,photos" {
		t.Fatalf("library %+v", library)
	}
	photos := page(user + "/Items?ParentId=home&Recursive=true&IncludeItemTypes=Photo&SortBy=SortName")
	if names(photos) != "Beach,Hill,Loose" {
		t.Fatalf("photos %+v", photos)
	}
	beach := photos[0]
	if beach.Type != "Photo" || beach.MediaType != "Photo" || beach.Width != 30 || beach.Height != 20 || beach.ImageTags["Primary"] == "" ||
		beach.PremiereDate == "" || len(beach.MediaSources) != 0 {
		t.Fatalf("beach %+v", beach)
	}
	videos := page(user + "/Items?ParentId=home&Recursive=true&IncludeItemTypes=Video")
	if names(videos) != "Clip" || videos[0].MediaType != "Video" || len(videos[0].MediaSources) != 1 {
		t.Fatalf("videos %+v", videos)
	}
	// The Folders tab lists a level at a time; a folder is a photo album.
	top := page(user + "/Items?ParentId=home&SortBy=IsFolder,SortName")
	if names(top) != "Trip,Loose" || top[0].Type != "PhotoAlbum" || !top[0].IsFolder || top[0].ChildCount != 3 || top[0].ImageTags["Primary"] == "" {
		t.Fatalf("top %+v", top)
	}
	trip := top[0].Id
	if got := names(page(user + "/Items?ParentId=" + trip + "&SortBy=IsFolder,SortName")); got != "Day 2,Beach,Clip" {
		t.Fatalf("trip %s", got)
	}
	// Playing an album as a slideshow asks for its photos at every depth.
	if got := names(page(user + "/Items?ParentId=" + trip + "&Recursive=true&MediaTypes=Photo&SortBy=SortName")); got != "Beach,Hill" {
		t.Fatalf("slideshow %s", got)
	}
	if got := page(user + "/Items?Recursive=true&CollectionTypes=homevideos&IncludeItemTypes=Photo"); len(got) != 3 {
		t.Fatalf("collection type %+v", got)
	}
	// The photo's image is the file itself.
	w := request(h, "GET", "/Items/"+beach.Id+"/Images/Primary?maxWidth=400&tag="+url.QueryEscape(beach.ImageTags["Primary"]), "", "", "")
	expectStatus(t, w, 200)
	original, err := os.ReadFile(filepath.Join(dir, "Trip", "Beach.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	if w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), original) {
		t.Fatalf("image %s, %d bytes", w.Header().Get("Content-Type"), w.Body.Len())
	}
	expectStatus(t, request(h, "GET", "/Items/"+beach.Id+"/Images/Backdrop", "", "", token), 404)
	var latest []item
	get(user+"/Items/Latest?ParentId=home&Limit=10", &latest)
	if len(latest) != 4 {
		t.Fatalf("latest %+v", latest)
	}

	// A playlist of photos only is a photo playlist; a video makes it a video one.
	w = request(h, "POST", "/Playlists?Name=Best&MediaType=Photo&Ids="+beach.Id, "", "", token)
	expectStatus(t, w, 200)
	var created struct{ Id string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	var playlist item
	get(user+"/Items/"+created.Id, &playlist)
	if playlist.MediaType != "Photo" {
		t.Fatalf("photo playlist %+v", playlist)
	}
	expectStatus(t, request(h, "POST", "/Playlists/"+created.Id+"/Items?Ids="+videos[0].Id, "", "", token), 200)
	get(user+"/Items/"+created.Id, &playlist)
	if playlist.MediaType != "Video" {
		t.Fatalf("mixed playlist %+v", playlist)
	}
}
