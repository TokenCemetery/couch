package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/hls"
	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
)

// chromiumAudio is the Container list Emby Web's Chromium profile sends to
// Audio/{id}/universal.
const chromiumAudio = "opus,mp3|mp3,mp2,mp3|mp2,aac|aac,m4a|aac,mp4|aac,flac,webma,webm,wav|PCM_S16LE,wav|PCM_S24LE,ogg"

// TestMusic scans tagged tracks into a music library and walks Emby Web's
// music requests: albums and their tracks, artists, artist filters, Latest,
// favorites, playlists and playback, direct and encoded.
func TestMusic(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required to make and encode tracks")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe required to scan tracks")
	}
	dir := t.TempDir()
	track := func(name string, args ...string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(context.Background(), ffmpeg, append(append([]string{"-v", "error", "-f", "lavfi", "-i", "sine=duration=1", "-ac", "2"}, args...), path)...) //nolint:gosec // test input
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", name, err, out)
		}
	}
	track("Artist A/Album One/02 Second.mp3", "-metadata", "title=Second", "-metadata", "artist=Artist A; Guest", "-metadata", "album=Album One", "-metadata", "album_artist=Artist A", "-metadata", "genre=Rock", "-metadata", "track=2", "-metadata", "date=2001")
	track("Artist A/Album One/01 First.flac", "-metadata", "title=First", "-metadata", "artist=Artist A", "-metadata", "album=Album One", "-metadata", "track=1")
	track("Artist B/Other/01 Encoded.wma", "-metadata", "title=Encoded", "-metadata", "artist=Artist B", "-metadata", "album=Other", "-c:a", "wmav2")
	catalog, err := media.ScanLibraries(context.Background(), []media.Library{{ID: "music", Name: "Music", Type: media.Music, Paths: []string{dir}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	store, _, data := newTestServer(t)
	api := New(store, "test", nil, catalog)
	t.Cleanup(api.Close)
	m, err := hls.NewManager(ffmpeg, ffprobe, filepath.Join(data, "transcode"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	api.SetHLS(m)
	h := api.Handler()
	token := login(t, h)
	user := "/Users/" + testUser(store).ID
	type item struct {
		Id, Name, Type, MediaType, Album, AlbumId, AlbumArtist, CollectionType string
		IndexNumber, ChildCount                                                int
		Artists, Subviews                                                      []string
		UserData                                                               struct{ IsFavorite bool }
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

	var library item
	get(user+"/Items/music", &library)
	if library.CollectionType != "music" || !strings.Contains(strings.Join(library.Subviews, ","), "albums") {
		t.Fatalf("library %+v", library)
	}
	albums := page(user + "/Items?ParentId=music&Recursive=true&IncludeItemTypes=MusicAlbum&SortBy=SortName")
	if names(albums) != "Album One,Other" || albums[0].AlbumArtist != "Artist A" || albums[0].ChildCount != 2 {
		t.Fatalf("albums %+v", albums)
	}
	album := albums[0].Id
	tracks := page(user + "/Items?ParentId=" + album + "&SortBy=ParentIndexNumber,IndexNumber,SortName")
	if names(tracks) != "First,Second" || tracks[1].IndexNumber != 2 || tracks[1].Album != "Album One" || tracks[1].AlbumId != album ||
		tracks[1].MediaType != "Audio" || strings.Join(tracks[1].Artists, ",") != "Artist A,Guest" {
		t.Fatalf("tracks %+v", tracks)
	}
	if got := names(page("/Artists/AlbumArtists?ParentId=music")); got != "Artist A,Artist B" {
		t.Fatalf("album artists %s", got)
	}
	artists := page("/Artists?ParentId=music&ArtistType=all")
	if names(artists) != "Artist A,Artist B,Guest" || artists[0].Type != "MusicArtist" {
		t.Fatalf("artists %+v", artists)
	}
	if got := page("/Artists?CollectionTypes=audiobooks"); len(got) != 0 {
		t.Fatalf("audiobook authors %+v", got)
	}
	artistA, guest := artists[0].Id, artists[2].Id
	var artistPage item
	get(user+"/Items/"+artistA, &artistPage)
	if artistPage.Name != "Artist A" || artistPage.Type != "MusicArtist" {
		t.Fatalf("artist page %+v", artistPage)
	}
	for query, want := range map[string]string{
		"IncludeItemTypes=MusicAlbum&AlbumArtistIds=" + artistA:                "Album One",
		"IncludeItemTypes=MusicAlbum&ContributingArtistIds=" + guest:           "Album One",
		"IncludeItemTypes=MusicAlbum&ContributingArtistIds=" + artistA:         "",
		"IncludeItemTypes=Audio&ArtistIds=" + guest:                            "Second",
		"IncludeItemTypes=MusicAlbum&ComposerArtistIds=" + artistA:             "",
		"IncludeItemTypes=Audio&MediaTypes=Audio&SortBy=Album,SortName":        "First,Second,Encoded",
		"IncludeItemTypes=MusicAlbum&GenreIds=" + metadata.ID("Genre", "Rock"): "Album One",
	} {
		if got := names(page(user + "/Items?Recursive=true&" + query)); got != want {
			t.Errorf("%s: %q, want %q", query, got, want)
		}
	}
	if got := page("/Genres?ParentId=music&IncludeItemTypes=MusicAlbum"); names(got) != "Rock" {
		t.Fatalf("genres %+v", got)
	}
	var latest []item
	get(user+"/Items/Latest?ParentId=music", &latest)
	if len(latest) != 2 || latest[0].Type != "MusicAlbum" {
		t.Fatalf("latest %+v", latest)
	}
	var sections []struct{ Id string }
	get(user+"/HomeSections", &sections)
	if len(sections) < 3 || sections[2].Id != "resumeaudio" {
		t.Fatalf("sections %+v", sections)
	}

	// Marking an album played marks its tracks.
	expectStatus(t, request(h, "POST", user+"/PlayedItems/"+album, "", "", token), 200)
	if got := names(page(user + "/Items?Recursive=true&IncludeItemTypes=Audio&IsPlayed=true")); got != "First,Second" {
		t.Fatalf("played tracks %s", got)
	}

	// An artist is marked favorite like an item.
	expectStatus(t, request(h, "POST", user+"/FavoriteItems/"+artistA, "", "", token), 200)
	if got := page("/Artists?Filters=IsFavorite"); names(got) != "Artist A" || !got[0].UserData.IsFavorite {
		t.Fatalf("favorite artists %+v", got)
	}

	// An album adds its tracks to a playlist in track order.
	w := request(h, "POST", "/Playlists?Name=Mix&Ids="+album, "", "", token)
	expectStatus(t, w, 200)
	var created struct{ Id string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if got := names(page(user + "/Items?ParentId=" + created.Id + "&SortBy=ListItemOrder")); got != "First,Second" {
		t.Fatalf("playlist %s", got)
	}
	var playlist item
	get(user+"/Items/"+created.Id, &playlist)
	if playlist.MediaType != "Audio" {
		t.Fatalf("playlist %+v", playlist)
	}

	// The browser plays MP3 and FLAC as they are, and gets WMA as AAC in M4A.
	universal := func(id string) (string, string) {
		t.Helper()
		w := request(h, "GET", "/Audio/"+id+"/universal?TranscodingProtocol=hls&AudioCodec=aac&Container="+url.QueryEscape(chromiumAudio), "", "", token)
		expectStatus(t, w, 200)
		return w.Header().Get("Content-Type"), w.Body.String()
	}
	if mime, _ := universal(tracks[1].Id); mime != "audio/mpeg" {
		t.Fatalf("mp3 content type %s", mime)
	}
	encoded := page(user + "/Items?Recursive=true&IncludeItemTypes=Audio&SearchTerm=Encoded")
	mime, body := universal(encoded[0].Id)
	if mime != "audio/mp4" || len(body) < 8 || body[4:8] != "ftyp" {
		t.Fatalf("encoded track: %s, %d bytes", mime, len(body))
	}
	// Without a Container list the client gets the file, as from Emby.
	if w := request(h, "GET", "/Audio/"+encoded[0].Id+"/universal", "", "", token); w.Header().Get("Content-Type") != "audio/x-ms-wma" {
		t.Fatalf("no container list: %s", w.Header().Get("Content-Type"))
	}

	// Instant Mix: the seed's tracks lead, then those sharing an artist or a
	// genre; a track of another artist and genre stays out.
	mix := func(path string) string {
		t.Helper()
		got := page(path)
		sorted := names(got[min(1, len(got)):])
		parts := strings.Split(sorted, ",")
		slices.Sort(parts)
		if len(got) == 0 {
			return ""
		}
		return got[0].Name + "|" + strings.Join(parts, ",")
	}
	for path, want := range map[string]string{
		"/Items/" + tracks[0].Id + "/InstantMix?Limit=5000":          "First|Second",
		"/Songs/" + tracks[1].Id + "/InstantMix":                     "Second|First",
		"/Albums/" + albums[1].Id + "/InstantMix":                    "Encoded|",
		"/Artists/InstantMix?Id=" + guest:                            "Second|First",
		"/MusicGenres/Rock/InstantMix":                               "Second|First",
		"/MusicGenres/InstantMix?Id=" + metadata.ID("Genre", "Rock"): "Second|First",
		"/Playlists/" + created.Id + "/InstantMix":                   "First|Second",
		"/Items/" + album + "/InstantMix?Limit=1":                    "First|",
	} {
		if got := mix(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	expectStatus(t, request(h, "GET", "/Items/missing/InstantMix", "", "", token), 404)
}

// TestEmbeddedCovers serves the covers embedded in MP3 and FLAC files as
// track and album images, caches what FFmpeg read, and lets a local image win.
func TestEmbeddedCovers(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required to make tracks and read covers")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required to scan tracks")
	}
	dir, pictures := t.TempDir(), t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), ffmpeg, append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil { //nolint:gosec // test input
			t.Fatalf("%v %s", err, out)
		}
	}
	picture := func(name, color string) string {
		path := filepath.Join(pictures, name)
		run("-f", "lavfi", "-i", "color=c="+color+":s=40x30", "-frames:v", "1", path)
		return path
	}
	png, jpeg := picture("blue.png", "blue"), picture("red.jpg", "red")
	track := func(name, cover string, args ...string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		input := []string{"-f", "lavfi", "-i", "sine=duration=1"}
		if cover != "" {
			input = append(input, "-i", cover, "-map", "0:a", "-map", "1:v", "-c:v", "copy", "-disposition:v:0", "attached_pic")
		}
		run(append(append(input, args...), path)...)
	}
	track("A/Embedded/01 One.flac", png, "-metadata", "title=One", "-metadata", "album=Embedded", "-metadata", "artist=A", "-metadata", "track=1")
	track("A/Embedded/02 Two.mp3", jpeg, "-metadata", "title=Two", "-metadata", "album=Embedded", "-metadata", "artist=A", "-metadata", "track=2", "-id3v2_version", "3")
	track("B/Local/01 Three.mp3", jpeg, "-metadata", "title=Three", "-metadata", "album=Local", "-metadata", "artist=B", "-id3v2_version", "3")
	track("B/Plain/01 Four.mp3", "", "-metadata", "title=Four", "-metadata", "album=Plain", "-metadata", "artist=B")
	run("-f", "lavfi", "-i", "color=c=green:s=40x30", "-frames:v", "1", filepath.Join(dir, "B/Local/cover.png"))
	embedded, err := os.ReadFile(png)
	if err != nil {
		t.Fatal(err)
	}
	local, err := os.ReadFile(filepath.Join(dir, "B/Local/cover.png"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := media.ScanLibraries(context.Background(), []media.Library{{ID: "music", Name: "Music", Type: media.Music, Paths: []string{dir}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	store, _, data := newTestServer(t)
	api := New(store, "test", nil, catalog)
	t.Cleanup(api.Close)
	covers := filepath.Join(data, "covers")
	api.SetCoverCache(covers)
	h := api.Handler()
	token := login(t, h)
	user := "/Users/" + testUser(store).ID
	type item struct {
		Id, Name  string
		ImageTags map[string]string
	}
	list := func(types string) map[string]item {
		t.Helper()
		w := request(h, "GET", user+"/Items?Recursive=true&IncludeItemTypes="+types, "", "", token)
		expectStatus(t, w, 200)
		var p struct{ Items []item }
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		byName := map[string]item{}
		for _, i := range p.Items {
			byName[i.Name] = i
		}
		return byName
	}
	image := func(i item) (string, []byte) {
		t.Helper()
		if i.ImageTags["Primary"] == "" {
			t.Fatalf("%s has no Primary image", i.Name)
		}
		w := request(h, "GET", "/Items/"+i.Id+"/Images/Primary?tag="+i.ImageTags["Primary"], "", "", token)
		expectStatus(t, w, 200)
		return w.Header().Get("Content-Type"), w.Body.Bytes()
	}
	albums, tracks := list("MusicAlbum"), list("Audio")
	// An album shows its first track's cover; each track its own.
	if mime, body := image(albums["Embedded"]); mime != "image/png" || !bytes.Equal(body, embedded) {
		t.Fatalf("album cover: %s, %d bytes", mime, len(body))
	}
	if mime, body := image(tracks["Two"]); mime != "image/jpeg" || len(body) < 3 || body[0] != 0xFF || body[1] != 0xD8 {
		t.Fatalf("track cover: %s", mime)
	}
	cached, err := os.ReadDir(covers)
	if err != nil || len(cached) != 2 {
		t.Fatalf("cached covers %d, %v", len(cached), err)
	}
	// A cached cover is served again without reading the track.
	if err := os.Remove(filepath.Join(dir, "A/Embedded/02 Two.mp3")); err != nil {
		t.Fatal(err)
	}
	if mime, _ := image(tracks["Two"]); mime != "image/jpeg" {
		t.Fatalf("cached track cover: %s", mime)
	}
	// A local image wins over an embedded one; a track without either has none.
	if mime, body := image(albums["Local"]); mime != "image/png" || !bytes.Equal(body, local) {
		t.Fatalf("local album cover: %s", mime)
	}
	if mime, body := image(tracks["Three"]); mime != "image/png" || !bytes.Equal(body, local) {
		t.Fatalf("track in a folder with a local cover: %s", mime)
	}
	if tag := albums["Plain"].ImageTags["Primary"]; tag != "" {
		t.Fatalf("album without a cover has image %s", tag)
	}
}
