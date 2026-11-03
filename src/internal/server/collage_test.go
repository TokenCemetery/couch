package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

func solidPNG(t *testing.T, c color.Color, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.Set(x, y, c)
		}
	}
	var content bytes.Buffer
	if err := png.Encode(&content, img); err != nil {
		t.Fatal(err)
	}
	return content.Bytes()
}

// TestCollectionAndPlaylistImages draws the collage of a collection and of a
// playlist from their items' covers, then has the administrator upload and
// delete a collection's image and the owner a playlist's, which other users
// may not.
func TestCollectionAndPlaylistImages(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required to open media root")
	}
	dir := t.TempDir()
	catalog, err := media.Scan(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	red, green, blue, yellow, purple := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 255, 0, 255}, color.RGBA{0, 0, 255, 255}, color.RGBA{255, 255, 0, 255}, color.RGBA{128, 0, 128, 255}
	catalog.Items = []media.Item{
		{ID: "a", Name: "A", Path: "A.mp4"}, {ID: "b", Name: "B", Path: "B.mp4"},
		{ID: "c", Name: "C", Path: "C.mp4"}, {ID: "d", Name: "D", Path: "D.mp4"},
		{ID: "bare", Name: "Bare", Path: "Bare.mp4"},
		{ID: "episode", Kind: "Episode", Name: "Show S01E01", Path: "Show/Season 01/Show.S01E01.mp4", SeriesID: "series"},
	}
	catalog.Folders = []media.Item{{ID: "series", Kind: "Series", Name: "Show", Path: "Show"}}
	// A wide cover is cropped to its middle: red, with green sides.
	wide := image.NewRGBA(image.Rect(0, 0, 90, 30))
	for x := range 90 {
		for y := range 30 {
			wide.Set(x, y, map[bool]color.Color{true: red, false: green}[x >= 30 && x < 60])
		}
	}
	var wideCover bytes.Buffer
	if err := png.Encode(&wideCover, wide); err != nil {
		t.Fatal(err)
	}
	covers := map[string][]byte{"A-poster.png": wideCover.Bytes(), "B-poster.png": solidPNG(t, green, 2, 3), "C-poster.png": solidPNG(t, blue, 2, 3),
		"D-poster.png": solidPNG(t, yellow, 2, 3), "Show/poster.png": solidPNG(t, purple, 2, 3)}
	for name, content := range covers {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, _, _ := newTestServer(t)
	server := New(store, "test", nil, catalog)
	coverDir := t.TempDir()
	server.SetCoverCache(coverDir)
	h := server.Handler()
	admin := login(t, h)
	guest, err := store.CreateUser("guest", state.Policy{EnableAllFolders: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetPassword(admin, guest.ID, "guest-test-password"); err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", "/Users/authenticatebyname", "application/x-www-form-urlencoded", url.Values{"Username": {"guest"}, "Pw": {"guest-test-password"}}.Encode(), "")
	var signedIn struct{ AccessToken string }
	if err := json.Unmarshal(w.Body.Bytes(), &signedIn); err != nil || signedIn.AccessToken == "" {
		t.Fatalf("guest sign-in: %s", w.Body)
	}
	guestToken := signedIn.AccessToken

	primary := func(id, token string) (string, float64) {
		t.Helper()
		w := request(h, "GET", "/Items/"+id, "", "", token)
		expectStatus(t, w, 200)
		var dto struct {
			ImageTags               map[string]string
			PrimaryImageAspectRatio float64
		}
		if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
			t.Fatal(err)
		}
		return dto.ImageTags["Primary"], dto.PrimaryImageAspectRatio
	}
	fetch := func(id, tag string) ([]byte, string) {
		t.Helper()
		w := request(h, "GET", "/Items/"+id+"/Images/Primary?tag="+url.QueryEscape(tag), "", "", "")
		expectStatus(t, w, 200)
		return w.Body.Bytes(), w.Header().Get("Content-Type")
	}
	// tiles checks the colors at the middle of the collage's four tiles,
	// filled column by column.
	tiles := func(content []byte, width, height int, want ...color.RGBA) {
		t.Helper()
		img, err := jpeg.Decode(bytes.NewReader(content))
		if err != nil || img.Bounds() != image.Rect(0, 0, width, height) {
			t.Fatalf("collage %v: %v", img.Bounds(), err)
		}
		points := []image.Point{{width / 4, height / 4}, {width / 4, height * 3 / 4}, {width * 3 / 4, height / 4}, {width * 3 / 4, height * 3 / 4}}
		for i, p := range points {
			r, g, b, _ := img.At(p.X, p.Y).RGBA()
			near := func(got uint32, want uint8) bool { return int(got>>8)-int(want) < 40 && int(want)-int(got>>8) < 40 }
			if !near(r, want[i].R) || !near(g, want[i].G) || !near(b, want[i].B) {
				t.Fatalf("tile %d at %v = %d,%d,%d, want %v", i, p, r>>8, g>>8, b>>8, want[i])
			}
		}
	}
	type imageInfo struct {
		ImageType     string
		Width, Height int
	}
	editor := func(id, token string) []imageInfo {
		t.Helper()
		w := request(h, "GET", "/Items/"+id+"/Images", "", "", token)
		expectStatus(t, w, 200)
		var infos []imageInfo
		if err := json.Unmarshal(w.Body.Bytes(), &infos); err != nil {
			t.Fatal(err)
		}
		return infos
	}

	// A collection lists its items by name here, as none has a release date.
	w = request(h, "POST", "/Collections?Name=Saga&Ids=d,bare,c,b,a", "", "", admin)
	expectStatus(t, w, 200)
	var created struct{ Id string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	set := created.Id
	tag, ratio := primary(set, admin)
	if tag == "" || ratio != 2.0/3.0 {
		t.Fatalf("collection image %q, ratio %v", tag, ratio)
	}
	content, mime := fetch(set, tag)
	if mime != "image/jpeg" {
		t.Fatalf("collage type %s", mime)
	}
	tiles(content, 400, 600, red, green, blue, yellow)
	if again, _ := fetch(set, tag); !bytes.Equal(again, content) {
		t.Fatal("collage drawn again differs")
	}
	if cached, _ := filepath.Glob(filepath.Join(coverDir, "collage-*")); len(cached) != 1 {
		t.Fatalf("cached collages: %v", cached)
	}
	if infos := editor(set, admin); len(infos) != 1 || infos[0].ImageType != "Primary" || infos[0].Width != 400 || infos[0].Height != 600 {
		t.Fatalf("image editor: %+v", infos)
	}
	if guestTag, _ := primary(set, guestToken); guestTag == "" {
		t.Fatal("collage not shown to another user")
	}

	// The administrator uploads a Primary image, as Emby Web does, and
	// deletes it again; the collage comes back.
	upload := solidPNG(t, blue, 4, 5)
	body := base64.StdEncoding.EncodeToString(upload)
	expectStatus(t, request(h, "POST", "/Items/"+set+"/Images/Primary", "image/png", body, guestToken), 403)
	expectStatus(t, request(h, "POST", "/Items/"+set+"/Images/Backdrop", "image/png", body, admin), 400)
	expectStatus(t, request(h, "POST", "/Items/"+set+"/Images/Primary", "image/png", "not an image", admin), 400)
	expectStatus(t, request(h, "POST", "/Items/"+set+"/Images/Primary", "image/png", body, admin), 204)
	uploadedTag, ratio := primary(set, admin)
	if uploadedTag == "" || uploadedTag == tag || ratio != 4.0/5.0 {
		t.Fatalf("uploaded image %q, ratio %v", uploadedTag, ratio)
	}
	if content, mime := fetch(set, uploadedTag); !bytes.Equal(content, upload) || mime != "image/png" {
		t.Fatalf("uploaded image served as %s", mime)
	}
	expectStatus(t, request(h, "GET", "/Items/"+set+"/Images/Primary?tag="+url.QueryEscape(tag), "", "", ""), 404)
	if infos := editor(set, admin); len(infos) != 1 || infos[0].Width != 4 || infos[0].Height != 5 {
		t.Fatalf("image editor after upload: %+v", infos)
	}
	expectStatus(t, request(h, "POST", "/Items/"+set+"/Images/Primary/0/Delete", "", "", admin), 204)
	if again, _ := primary(set, admin); again != tag {
		t.Fatal("collage not restored after delete")
	}
	expectStatus(t, request(h, "DELETE", "/Items/"+set+"/Images/Primary", "", "", admin), 204)
	expectStatus(t, request(h, "DELETE", "/Items/"+set+"/Images/Primary/0/Delete", "", "", admin), 204)

	// A cover that cannot be read fails the collage without caching it, so
	// the next request draws it again.
	before, _ := filepath.Glob(filepath.Join(coverDir, "collage-*"))
	unreadable := collage{bounds: posterCollage, sources: []media.Item{catalog.Items[1], catalog.Items[4]}, info: media.Image{Revision: "unreadable"}}
	if file, _, err := server.openCollage(t.Context(), catalog, unreadable); err == nil {
		_ = file.Close()
		t.Fatal("collage drawn with an unreadable cover")
	}
	if cached, _ := filepath.Glob(filepath.Join(coverDir, "collage-*")); len(cached) != len(before) {
		t.Fatalf("collage with an unreadable cover was cached: %v, before %v", cached, before)
	}

	// A playlist's collage is square; an episode shows its series' cover,
	// and an item without a cover is left out.
	w = request(h, "POST", "/Playlists?Name=Evening&Ids=episode,bare,a,b&userId="+testUser(store).ID, "", "", admin)
	expectStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	list := created.Id
	tag, ratio = primary(list, admin)
	if tag == "" || ratio != 1 {
		t.Fatalf("playlist image %q, ratio %v", tag, ratio)
	}
	content, _ = fetch(list, tag)
	tiles(content, 600, 600, purple, red, green, purple)
	expectStatus(t, request(h, "POST", "/Items/"+list+"/Images/Primary", "image/png", body, guestToken), 403)
	expectStatus(t, request(h, "POST", "/Items/"+list+"/Images/Primary", "image/png", body, admin), 204)
	if uploadedTag, _ := primary(list, admin); uploadedTag == tag {
		t.Fatal("playlist upload not shown")
	}
	expectStatus(t, request(h, "POST", "/Items/"+list+"/Images/Primary/Delete", "", "", admin), 204)
	if again, _ := primary(list, admin); again != tag {
		t.Fatal("playlist collage not restored after delete")
	}

	// Without a cover among its items, a collection has no image.
	w = request(h, "POST", "/Collections?Name=Plain&Ids=bare", "", "", admin)
	expectStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if tag, _ := primary(created.Id, admin); tag != "" {
		t.Fatalf("collection without covers has image %q", tag)
	}
	expectStatus(t, request(h, "GET", "/Items/"+created.Id+"/Images/Primary", "", "", admin), 404)
	if infos := editor(created.Id, admin); len(infos) != 0 {
		t.Fatalf("image editor without covers: %+v", infos)
	}
	// Catalogue items keep refusing image changes.
	expectStatus(t, request(h, "POST", "/Items/a/Images/Primary", "image/png", body, admin), 400)
}
