package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/TokenCemetery/coach/internal/media"
)

func TestSignedImageHTTP(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required to open media root")
	}
	dir := t.TempDir()
	catalog, err := media.Scan(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	catalog.Items = []media.Item{{ID: "movie", Path: "Movie.mp4"}, {ID: "other", Path: "Other.mp4"}}
	var content bytes.Buffer
	if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, "Movie-poster.png")
	if err := os.WriteFile(name, content.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	store, _, _ := newTestServer(t)
	h := New(store, "test", nil, catalog).Handler()
	token := login(t, h)
	getTag := func() string {
		t.Helper()
		w := request(h, "GET", "/Items/movie", "", "", token)
		expectStatus(t, w, 200)
		var dto struct {
			ImageTags               map[string]string
			PrimaryImageAspectRatio float64
		}
		if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		if dto.PrimaryImageAspectRatio != 2.0/3.0 || dto.ImageTags["Primary"] == "" {
			t.Fatal("image not advertised")
		}
		return dto.ImageTags["Primary"]
	}
	tag := getTag()
	imageURL := "/emby/Items/movie/Images/Primary?tag=" + url.QueryEscape(tag) + "&maxWidth=160&quality=90"
	w := request(h, "GET", imageURL, "", "", "")
	expectStatus(t, w, 200)
	if w.Header().Get("Content-Type") != "image/png" || !bytes.Equal(w.Body.Bytes(), content.Bytes()) || w.Header().Get("Cache-Control") != "private, no-cache, no-transform" {
		t.Fatal("incorrect image response")
	}
	etag := w.Header().Get("ETag")
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, imageURL, nil)
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expectStatus(t, w, 304)
	expectStatus(t, request(h, "HEAD", imageURL, "", "", ""), 200)
	expectStatus(t, request(h, "GET", "/Items/movie/Images/Primary/0", "", "", token), 200)
	expectStatus(t, request(h, "GET", "/Items/movie/Images/Primary", "", "", ""), 401)
	expectStatus(t, request(h, "GET", "/Items/other/Images/Primary?tag="+tag, "", "", ""), 401)
	expectStatus(t, request(h, "GET", imageURL+"&UserId=other", "", "", ""), 403)
	expectStatus(t, request(h, "GET", imageURL+"&TAG=wrong", "", "", ""), 400)
	expectStatus(t, request(h, "GET", imageURL, "", "", "bad-token"), 401)
	expectStatus(t, request(h, "GET", "/Items/movie", "", "", tag), 401)
	expectStatus(t, request(h, "GET", "/Items/other/Images/Primary", "", "", token), 404)
	changed := time.Now().Add(time.Second)
	if err := os.Chtimes(name, changed, changed); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, request(h, "GET", imageURL, "", "", ""), 404)
	newTag := getTag()
	if newTag == tag {
		t.Fatal("stale ImageTag")
	}
	imageURL = "/Items/movie/Images/Primary?tag=" + newTag
	expectStatus(t, request(h, "POST", "/Sessions/Logout", "", "", token), 204)
	r = httptest.NewRequestWithContext(t.Context(), http.MethodGet, imageURL, nil)
	r.Header.Set("If-None-Match", etag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expectStatus(t, w, 401)
}

func TestBackdropImageHTTP(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required to open media root")
	}
	dir := t.TempDir()
	catalog, err := media.Scan(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	catalog.Items = []media.Item{
		{ID: "movie", Path: "Movie.mp4"},
		{ID: "episode", Path: "Show/Season 01/Show.S01E01.mp4", SeriesID: "series"},
	}
	catalog.Folders = []media.Item{{ID: "series", Kind: "Series", Path: "Show"}}
	write := func(name string, width int) []byte {
		t.Helper()
		var content bytes.Buffer
		if err := png.Encode(&content, image.NewRGBA(image.Rect(0, 0, width, 1))); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		return content.Bytes()
	}
	poster := write("Movie-poster.png", 1)
	movieBackdrop := write("Movie-fanart.png", 2)
	seriesBackdrop := write("Show/backdrop.png", 3)
	store, _, _ := newTestServer(t)
	h := New(store, "test", nil, catalog).Handler()
	token := login(t, h)
	type dto struct {
		ImageTags               map[string]string
		BackdropImageTags       []string
		ParentBackdropItemId    string
		ParentBackdropImageTags []string
	}
	get := func(id string) dto {
		t.Helper()
		w := request(h, "GET", "/Items/"+id, "", "", token)
		expectStatus(t, w, 200)
		var d dto
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	fetch := func(path string, want []byte) {
		t.Helper()
		w := request(h, "GET", path, "", "", "")
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), want) {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	movie := get("movie")
	if len(movie.BackdropImageTags) != 1 || movie.ParentBackdropItemId != "" {
		t.Fatalf("movie: %+v", movie)
	}
	fetch("/Items/movie/Images/Backdrop/0?tag="+url.QueryEscape(movie.BackdropImageTags[0]), movieBackdrop)
	fetch("/Items/movie/Images/Primary?tag="+url.QueryEscape(movie.ImageTags["Primary"]), poster)
	// A tag grants one image: the backdrop's does not open the cover.
	expectStatus(t, request(h, "GET", "/Items/movie/Images/Primary?tag="+url.QueryEscape(movie.BackdropImageTags[0]), "", "", ""), 404)
	expectStatus(t, request(h, "GET", "/Items/movie/Images/Backdrop/1", "", "", token), 404)
	expectStatus(t, request(h, "GET", "/Items/movie/Images/Thumb", "", "", token), 404)
	series := get("series")
	if len(series.BackdropImageTags) != 1 {
		t.Fatalf("series: %+v", series)
	}
	fetch("/Items/series/Images/Backdrop?tag="+url.QueryEscape(series.BackdropImageTags[0]), seriesBackdrop)
	episode := get("episode")
	if len(episode.BackdropImageTags) != 0 || episode.ParentBackdropItemId != "series" || len(episode.ParentBackdropImageTags) != 1 {
		t.Fatalf("episode: %+v", episode)
	}
	fetch("/Items/series/Images/Backdrop/0?tag="+url.QueryEscape(episode.ParentBackdropImageTags[0]), seriesBackdrop)
}
