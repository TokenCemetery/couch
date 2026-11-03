package media

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParseLyrics(t *testing.T) {
	// describe writes each line as start-end:text, in milliseconds.
	describe := func(lines []LyricsLine) string {
		parts := []string{}
		for _, l := range lines {
			if l.Start == nil {
				parts = append(parts, l.Text)
				continue
			}
			parts = append(parts, strconv.FormatInt(*l.Start/10000, 10)+"-"+strconv.FormatInt(*l.End/10000, 10)+":"+l.Text)
		}
		return strings.Join(parts, "|")
	}
	for _, tc := range []struct{ name, text, want string }{
		{"lrc", "\ufeff[ar:Artist]\r\n[ti:Song]\r\n[00:01.5]One\r\n[00:03.25] Two \r\nnot timed\r\n[00:05.123]Three", "1500-3250:One|3250-5123:Two|5123-60000:Three"},
		{"repeated chorus, out of order", "[00:10.00][00:02.00]Chorus\n[00:05.00]Verse", "2000-5000:Chorus|5000-10000:Verse|10000-60000:Chorus"},
		{"offset shows lines earlier", "[offset:+500]\n[00:00.20]Zero\n[00:02.00]Two", "0-1500:Zero|1500-60000:Two"},
		{"word times and an empty line", "[00:01.00]<00:01.00>Hello <00:01.50>world\n[00:02.00]\n[00:04.00]Again", "1000-2000:Hello world|4000-60000:Again"},
		{"minutes past an hour, colon fraction", "[61:00:50]Late", "3660500-3660500:Late"},
		{"no fraction", "[00:12]Twelve\n[1:05]More", "12000-65000:Twelve|65000-65000:More"},
		{"plain text", "First line\n\n[Chorus]\n[Bridge:x2]\nSecond line\r\n", "First line|[Chorus]|[Bridge:x2]|Second line"},
		{"only tags", "[ar:Artist]\n[ti:Song]", ""},
	} {
		text, err := decodeText([]byte(tc.text))
		if err != nil {
			t.Fatal(err)
		}
		if got := describe(parseLyrics(text, 600000000)); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestParseProbeLyrics(t *testing.T) {
	// ID3 USLT is named after its description and language; MP4 and Vorbis
	// use one name. Lyrics are numbered after the streams, in name order.
	item, err := parseProbe([]byte(`{"format":{"duration":"3","tags":{"title":"Song","lyrics-eng":"[00:01.00]One","lyrics-Live take-rus":"Раз","LYRICS-XXX":"Other","lyrics-Текст, v2-ukr":"Слова","lyrics-":"no language","lyricist":"Someone"}},
		"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3"},{"index":1,"codec_type":"video","codec_name":"mjpeg","width":5,"height":5,"disposition":{"attached_pic":1}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Stream{
		{Index: 1, Type: "Subtitle", Codec: "lrc", Language: "eng", Lyrics: true, LyricsTag: "lyrics-eng"},
		{Index: 2, Type: "Subtitle", Codec: "txt", Lyrics: true, LyricsTag: "lyrics-live take-rus", Language: "rus"},
		{Index: 3, Type: "Subtitle", Codec: "txt", Lyrics: true, LyricsTag: "lyrics-xxx"},
		{Index: 4, Type: "Subtitle", Codec: "txt", Language: "ukr", Lyrics: true, LyricsTag: "lyrics-текст, v2-ukr"},
	}
	if got := item.Streams[1:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("lyrics streams:\n got %+v\nwant %+v", got, want)
	}
	// Ogg and Opus keep them in the stream; an empty tag is no lyrics.
	item, err = parseProbe([]byte(`{"format":{"duration":"3"},"streams":[{"index":0,"codec_type":"audio","codec_name":"opus","tags":{"LYRICS":"Line","UNSYNCEDLYRICS":" "}}]}`))
	if err != nil || len(item.Streams) != 2 || item.Streams[1] != (Stream{Index: 1, Type: "Subtitle", Codec: "txt", Lyrics: true, LyricsTag: "lyrics"}) {
		t.Fatalf("stream tags: %+v, %v", item.Streams, err)
	}
	// A video's tags are not lyrics.
	item, err = parseProbe([]byte(`{"format":{"duration":"3","tags":{"lyrics":"Line"}},"streams":[{"index":0,"codec_type":"video","codec_name":"h264"},{"index":1,"codec_type":"audio","codec_name":"aac"}]}`))
	if err != nil || len(item.Streams) != 2 {
		t.Fatalf("video: %+v, %v", item.Streams, err)
	}
}

func TestScanAttachesLyricsFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"01 Song.mp3":    "audio",
		"01 Song.lrc":    "[00:01.00]Hello\n[00:02.00]World\n",
		"01 Song.ru.txt": "Привет",
		"01 Song.srt":    "1\n00:00:01,000 --> 00:00:02,000\nnot for tracks\n",
		"02 Other.flac":  "audio",
		"02 Other.lrc":   "\xcf\xf0\xe8",
		"Movie.mp4":      "video",
		"Movie.lrc":      "[00:01.00]not for videos\n",
		"notes.txt":      "nobody's",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	probe := func(_ context.Context, f *os.File) (Item, error) {
		if strings.HasSuffix(f.Name(), ".mp4") {
			return Item{Streams: []Stream{{Index: 0, Type: "Video", Codec: "h264"}}}, nil
		}
		return Item{RunTimeTicks: 30000000, Streams: []Stream{{Index: 0, Type: "Audio", Codec: "mp3"}}}, nil
	}
	libraries := []Library{{ID: "music", Name: "Music", Type: Music, Paths: []string{dir}}, {ID: "movies", Name: "Movies", Type: Movies, Paths: []string{dir}}}
	catalog, err := (&Catalog{probe: probe, sources: map[string]*source{}}).Rescan(context.Background(), libraries)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.Close() }()
	byPath := map[string]Item{}
	for _, item := range catalog.Items {
		byPath[item.Path] = item
	}
	song := byPath["01 Song.mp3"]
	want := []Stream{
		{Index: 0, Type: "Audio", Codec: "mp3"},
		{Index: 1, Type: "Subtitle", Codec: "lrc", Path: "01 Song.lrc", Lyrics: true},
		{Index: 2, Type: "Subtitle", Codec: "txt", Language: "ru", Path: "01 Song.ru.txt", Lyrics: true},
	}
	if !reflect.DeepEqual(song.Streams, want) || LyricsIndex(song) != 1 {
		t.Fatalf("song streams %+v", song.Streams)
	}
	if movie := byPath["Movie.mp4"]; len(movie.Streams) != 1 || LyricsIndex(movie) != -1 {
		t.Fatalf("a video got lyrics: %+v", movie.Streams)
	}
	lines, err := catalog.OpenLyrics(context.Background(), song, 1)
	if err != nil || len(lines) != 2 || lines[0].Text != "Hello" || *lines[0].Start != 10000000 || *lines[1].End != 30000000 {
		t.Fatalf("lrc: %+v, %v", lines, err)
	}
	if lines, err := catalog.OpenLyrics(context.Background(), song, 2); err != nil || len(lines) != 1 || lines[0].Text != "Привет" || lines[0].Start != nil {
		t.Fatalf("txt: %+v, %v", lines, err)
	}
	// Lyrics are not subtitles, and only UTF-8 is read.
	if catalog.SubtitleDeliverable(song, 1) {
		t.Fatal("lyrics offered as WebVTT")
	}
	if _, err := catalog.OpenLyrics(context.Background(), song, 0); !os.IsNotExist(err) {
		t.Fatalf("audio stream as lyrics: %v", err)
	}
	if _, err := catalog.OpenLyrics(context.Background(), byPath["02 Other.flac"], 1); err == nil || os.IsNotExist(err) {
		t.Fatalf("legacy encoding: %v", err)
	}
}
