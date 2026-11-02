package media

import (
	"context"
	"os"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseProbe(t *testing.T) {
	item, err := parseProbe([]byte(`{"format":{"duration":"1.25"},"streams":[
		{"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"disposition":{"default":1},"side_data_list":[{},{"rotation":-90}]},
		{"index":1,"codec_type":"audio","codec_name":"aac","channels":2,"sample_rate":"48000","bit_rate":"128000","tags":{"language":"rus"}},
		{"index":2,"codec_type":"subtitle","codec_name":"subrip","disposition":{"forced":1}},
		{"index":3,"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}},
		{"index":4,"codec_type":"data"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if item.RunTimeTicks != 12500000 || len(item.Streams) != 3 {
		t.Fatal("incorrect duration/stream count")
	}
	if item.Streams[0].Width != 1920 || item.Streams[0].Rotation != 270 || !item.Streams[0].IsDefault || item.Streams[1].Language != "rus" || item.Streams[1].SampleRate != 48000 || item.Streams[1].BitRate != 128000 || !item.Streams[2].IsForced {
		t.Fatal("incorrect stream metadata")
	}
	for _, raw := range []string{"{", "null", `{}`, `{"streams":[{"codec_type":"data"}]}`, `{"streams":[{"codec_type":"video","disposition":{"attached_pic":1}}]}`} {
		if _, err := parseProbe([]byte(raw)); err == nil {
			t.Fatal("invalid/streamless probe accepted")
		}
	}
	for _, duration := range []string{"N/A", "NaN", "+Inf", "-1", "1e30"} {
		item, err := parseProbe([]byte(`{"format":{"duration":"` + duration + `"},"streams":[{"codec_type":"video"}]}`))
		if err != nil || item.RunTimeTicks != 0 {
			t.Fatal("invalid duration became ticks")
		}
	}
}

func TestParseProbeAudioTags(t *testing.T) {
	// MP3 and FLAC keep their tags in the format, in any case.
	item, err := parseProbe([]byte(`{"format":{"duration":"3","bit_rate":"900000","tags":{"TITLE":"Song","ARTIST":"A; B ;A","album_artist":"A","ALBUM":"Album","GENRE":"Rock/Pop","track":"3/12","DISC":"2/2","date":"2001-05-02"}},
		"streams":[{"index":0,"codec_type":"audio","codec_name":"flac","channels":2,"sample_rate":"44100","bits_per_sample":16},
		{"index":1,"codec_type":"video","codec_name":"mjpeg","width":500,"height":400,"disposition":{"attached_pic":1}},
		{"index":2,"codec_type":"video","codec_name":"png","width":10,"height":10,"disposition":{"attached_pic":1}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := Tags{Title: "Song", Album: "Album", AlbumArtist: "A", Artists: []string{"A", "B"}, Genres: []string{"Rock", "Pop"}, Track: 3, Disc: 2, Year: 2001}
	if !reflect.DeepEqual(item.Tags, want) || len(item.Streams) != 1 || item.Streams[0].BitRate != 900000 || item.Streams[0].BitDepth != 16 {
		t.Fatalf("tags %+v, streams %+v", item.Tags, item.Streams)
	}
	// The first attached picture is the cover.
	if item.Cover != (Image{Width: 500, Height: 400, MIME: "image/jpeg"}) {
		t.Fatalf("cover %+v", item.Cover)
	}
	// Ogg and Opus keep them in the stream.
	item, err = parseProbe([]byte(`{"format":{"duration":"3"},"streams":[{"index":0,"codec_type":"audio","codec_name":"opus","tags":{"title":"Other","artist":"C","track":"x"}}]}`))
	if err != nil || item.Tags.Title != "Other" || !slices.Equal(item.Tags.Artists, []string{"C"}) || item.Tags.Track != 0 || item.Streams[0].Title != "Other" {
		t.Fatalf("stream tags %+v, %v", item.Tags, err)
	}
}

func TestProbeProcess(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "couch-probe-helper" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "overflow":
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", maxProbeOutput+1)))
	case "wait":
		time.Sleep(30 * time.Second)
	case "crash":
		// Partial output, then death by signal, as a crashing FFprobe would.
		_, _ = os.Stdout.WriteString(`{"streams":[`)
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	case "environment":
		if os.Getenv("FFREPORT") != "" {
			os.Exit(1)
		}
		_, _ = os.Stdout.WriteString("clean")
	}
	os.Exit(0)
}

func TestProbeExecutionLimits(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Failed runs count too: their CPU time is spent all the same.
	var stats ProbeStats
	for _, mode := range []string{"overflow", "wait", "crash"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := runProbe(ctx, binary, &stats, nil, "-test.run=TestProbeProcess", "couch-probe-helper", mode)
		cancel()
		if err == nil {
			t.Fatalf("%s limit not enforced", mode)
		}
	}
	if stats.Runs != 3 || stats.CPU <= 0 || stats.PeakKiB <= 0 {
		t.Fatalf("stats %+v", stats)
	}
	t.Setenv("FFREPORT", "test-only-value")
	output, err := runProbe(context.Background(), binary, nil, nil, "-test.run=TestProbeProcess", "couch-probe-helper", "environment")
	if err != nil || string(output) != "clean" {
		t.Fatal("child inherited report environment")
	}
	t.Setenv("PATH", t.TempDir())
	if _, _, err := newProbe(context.Background()); err == nil {
		t.Fatal("missing ffprobe accepted")
	}
}
