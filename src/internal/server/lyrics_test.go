package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
)

func TestLyrics(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg required to make tracks")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe required to scan tracks and read their tags")
	}
	dir := t.TempDir()
	track := func(name string, args ...string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		args = append(append([]string{"-v", "error", "-y", "-f", "lavfi", "-i", "sine=duration=2"}, args...), path)
		if out, err := exec.CommandContext(context.Background(), ffmpeg, args...).CombinedOutput(); err != nil { //nolint:gosec // test input
			t.Fatalf("%v %s", err, out)
		}
	}
	track("A/Album/01 One.flac", "-metadata", "title=One", "-metadata", "LYRICS=[00:00.50]Half\n[00:01.25]Later")
	track("A/Album/02 Two.mp3", "-metadata", "title=Two", "-metadata", "lyrics-eng=First\nSecond")
	track("A/Album/03 Three.mp3", "-metadata", "title=Three")
	track("A/Album/04 Four.mp3", "-metadata", "title=Four", "-metadata", "lyrics-Текст, v2-rus=[00:01]Раз")
	if err := os.WriteFile(filepath.Join(dir, "A/Album/02 Two.de.lrc"), []byte("[00:01.00]Eins\n"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := media.ScanLibraries(context.Background(), []media.Library{{ID: "music", Name: "Music", Type: media.Music, Paths: []string{dir}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	store, _, _ := newTestServer(t)
	api := New(store, "test", nil, catalog)
	t.Cleanup(api.Close)
	h := api.Handler()
	token := login(t, h)
	ids := map[string]string{}
	for _, item := range catalog.Items {
		ids[item.Name] = item.ID
	}

	// A track lists its lyrics as subtitle streams, the first as default:
	// Emby Web plays tracks without PlaybackInfo, and its lyrics view shows
	// the default subtitle stream of the item's source.
	var dto struct {
		MediaSources []struct {
			DefaultSubtitleStreamIndex *int
			MediaStreams               []map[string]any
		}
	}
	w := request(h, "GET", "/Users/"+testUser(store).ID+"/Items/"+ids["Two"], "", "", token)
	expectStatus(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	type lyricsStream struct{ Index, Codec, Language any }
	got := []lyricsStream{}
	for _, s := range dto.MediaSources[0].MediaStreams {
		if s["Type"] == "Subtitle" {
			got = append(got, lyricsStream{s["Index"], s["Codec"], s["Language"]})
		}
	}
	if want := []lyricsStream{{float64(1), "txt", "eng"}, {float64(2), "lrc", "de"}}; !reflect.DeepEqual(got, want) || *dto.MediaSources[0].DefaultSubtitleStreamIndex != 1 {
		t.Fatalf("streams %+v, default %v", got, dto.MediaSources[0].DefaultSubtitleStreamIndex)
	}
	events := func(id string, index string) []map[string]any {
		t.Helper()
		w := request(h, "GET", "/Items/"+id+"/mediasource_"+id+"/Subtitles/"+index+"/Stream.js", "", "", token)
		expectStatus(t, w, 200)
		var result struct{ TrackEvents []map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.TrackEvents
	}
	// Embedded timed lyrics end at the next line, the last at the end of the track.
	one := events(ids["One"], "1")
	if len(one) != 2 || one[0]["Text"] != "Half" || one[0]["StartPositionTicks"] != float64(5000000) || one[0]["EndPositionTicks"] != float64(12500000) ||
		one[1]["EndPositionTicks"].(float64) < 19000000 {
		t.Fatalf("embedded lrc %v", one)
	}
	// Plain lyrics have no times.
	if two := events(ids["Two"], "1"); len(two) != 2 || two[1]["Text"] != "Second" || two[0]["StartPositionTicks"] != nil {
		t.Fatalf("embedded text %v", two)
	}
	// A description FFprobe's tag selection cannot name, and times without a fraction.
	if four := events(ids["Four"], "1"); len(four) != 1 || four[0]["Text"] != "Раз" || four[0]["StartPositionTicks"] != float64(10000000) {
		t.Fatalf("described tag %v", four)
	}
	if sidecar := events(ids["Two"], "2"); len(sidecar) != 1 || sidecar[0]["Text"] != "Eins" || sidecar[0]["StartPositionTicks"] != float64(10000000) {
		t.Fatalf("sidecar %v", sidecar)
	}
	for _, path := range []string{
		"/Items/" + ids["Two"] + "/mediasource_" + ids["Two"] + "/Subtitles/1/Stream.vtt",
		"/Items/" + ids["Two"] + "/mediasource_" + ids["Two"] + "/Subtitles/0/Stream.js",
		"/Items/" + ids["Three"] + "/mediasource_" + ids["Three"] + "/Subtitles/1/Stream.js",
		"/Items/" + ids["Two"] + "/other/Subtitles/1/Stream.js",
	} {
		expectStatus(t, request(h, "GET", path, "", "", token), 404)
	}
	expectStatus(t, request(h, "GET", "/Items/"+ids["Two"]+"/mediasource_"+ids["Two"]+"/Subtitles/1/Stream.js", "", "", ""), 401)
}
