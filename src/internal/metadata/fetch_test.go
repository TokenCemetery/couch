package metadata

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"testing"
)

// A 403 is a rejected key, unless Cloudflare marks it as a bot check.
func TestReadResponseForbidden(t *testing.T) {
	for header, want := range map[string]error{"": errAuth, "challenge": errRequest} {
		resp := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
		if header != "" {
			resp.Header.Set("Cf-Mitigated", header)
		}
		if _, err := readResponse(resp, 1); !errors.Is(err, want) {
			t.Errorf("Cf-Mitigated %q: %v, want %v", header, err, want)
		}
	}
}

func TestShrink(t *testing.T) {
	encode := func(w, h int, alpha uint8) []byte {
		t.Helper()
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // seeded noise for a test image
		for y := range h {
			for x := range w {
				img.SetNRGBA(x, y, color.NRGBA{uint8(r.UintN(256)), uint8(x), uint8(y), alpha})
			}
		}
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	for _, tc := range []struct {
		name          string
		body          []byte
		width, height int
		format        string
	}{
		{"wide backdrop", encode(1920, 1080, 255), 1280, 720, "jpeg"},
		{"tall poster", encode(1000, 1500, 255), 780, 1170, "jpeg"},
		{"small image, re-encoded only", encode(600, 400, 255), 600, 400, "jpeg"},
		{"transparent logo kept", encode(1920, 400, 128), 1920, 400, "png"},
	} {
		config, format, err := decodeConfig(tc.body)
		if err != nil {
			t.Fatal(err)
		}
		body, config, format := shrink(tc.body, config, format)
		got, gotFormat, err := image.DecodeConfig(bytes.NewReader(body))
		if err != nil || got.Width != config.Width || got.Height != config.Height || gotFormat != format {
			t.Fatalf("%s: reported %+v %s, file is %+v %s, %v", tc.name, config, format, got, gotFormat, err)
		}
		if config.Width != tc.width || config.Height != tc.height || format != tc.format {
			t.Errorf("%s: %dx%d %s, want %dx%d %s", tc.name, config.Width, config.Height, format, tc.width, tc.height, tc.format)
		}
		if format == "jpeg" && len(body) >= len(tc.body) {
			t.Errorf("%s: %d bytes, not smaller than %d", tc.name, len(body), len(tc.body))
		}
	}
	// An image that would not get smaller is kept as it is.
	tiny := encode(2, 3, 255)
	config, format, _ := decodeConfig(tiny)
	if body, _, format := shrink(tiny, config, format); !bytes.Equal(body, tiny) || format != "png" {
		t.Error("tiny image was re-encoded")
	}
}

func TestSameTitle(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"The Movie!", "the movie", true},
		{"Кино", "кино", true},
		{"!!!", "!!!", true},
		{" ", "!!!", false},
		{"!!!", "Chk Chk Chk", false},
		{"", "", false},
	} {
		if got := sameTitle(c.a, c.b); got != c.same {
			t.Errorf("sameTitle(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
