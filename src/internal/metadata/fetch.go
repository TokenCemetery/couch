package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register PNG for image.DecodeConfig
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

const (
	maxJSONBytes  = 4 << 20
	maxImageBytes = 8 << 20
)

// Errors a provider returns. Messages never include the request URL, which
// may carry an API key.
var (
	errNotFound = errors.New("not found")
	errAuth     = errors.New("API key rejected")
	errRequest  = errors.New("request failed")
	errUnusable = errors.New("not a usable JPEG or PNG image")
)

// A provider answering 429 is asked again after its Retry-After, at most
// maxRetryAfter later and rateRetries times; tests shorten the wait.
var (
	maxRetryAfter     = time.Minute
	defaultRetryAfter = 5 * time.Second
	rateRetries       = 3
)

// spacing is the least time between two requests to each provider: TMDb
// allows about 50 a second, TheTVDB publishes no limit, and the image
// servers serve a CDN. Tests shorten it.
var spacing = struct{ tmdb, tvdb, images time.Duration }{50 * time.Millisecond, 100 * time.Millisecond, 20 * time.Millisecond}

// throttle spaces a provider's requests.
type throttle struct {
	mu    sync.Mutex
	every time.Duration
	next  time.Time
}

func (t *throttle) wait(ctx context.Context) error {
	t.mu.Lock()
	now := time.Now()
	at := t.next
	if at.Before(now) {
		at = now
	}
	t.next = at.Add(t.every)
	t.mu.Unlock()
	return sleep(ctx, time.Until(at))
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// fetch sends req and returns the body of a 200 response, at most limit
// bytes. A 429 is retried after the provider's Retry-After.
func fetch(req *http.Request, pace *throttle, limit int64) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		if err := pace.wait(req.Context()); err != nil {
			return nil, err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			if req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			return nil, errRequest
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt == rateRetries {
			return readResponse(resp, limit)
		}
		_ = resp.Body.Close()
		if err := sleep(req.Context(), retryAfter(resp.Header.Get("Retry-After"))); err != nil {
			return nil, err
		}
		if req.GetBody != nil {
			if req.Body, err = req.GetBody(); err != nil {
				return nil, errRequest
			}
		}
	}
}

// retryAfter reads a Retry-After in seconds, within (0, maxRetryAfter].
func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds <= 0 {
		return defaultRetryAfter
	}
	return min(time.Duration(seconds)*time.Second, maxRetryAfter)
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func readResponse(resp *http.Response, limit int64) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNotFound
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errAuth
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: HTTP %d", errRequest, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, errRequest
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: response too large", errRequest)
	}
	return body, nil
}

func fetchJSON(req *http.Request, pace *throttle, v any) error {
	req.Header.Set("Accept", "application/json")
	body, err := fetch(req, pace, maxJSONBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: unreadable response", errRequest)
	}
	return nil
}

// download saves a provider image as a JPEG or PNG in dir, named by its URL,
// and returns its description. An image already saved is not fetched again.
func download(ctx context.Context, dir, url string, pace *throttle) (Image, error) {
	if !strings.HasPrefix(url, "https://") {
		return Image{}, errNotFound
	}
	name := hexID("file", url)
	for _, ext := range []string{".jpg", ".png"} {
		if img, err := readImage(dir, name+ext); err == nil {
			img.URL = url
			return img, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Image{}, errRequest
	}
	body, err := fetch(req, pace, maxImageBytes)
	if err != nil {
		return Image{}, err
	}
	config, format, err := decodeConfig(body)
	if err != nil {
		return Image{}, err
	}
	body, config, format = shrink(body, config, format)
	ext := map[string]string{"jpeg": ".jpg", "png": ".png"}[format]
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Image{}, err
	}
	f, err := os.CreateTemp(dir, ".image-*")
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return Image{}, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return Image{}, err
	}
	if err := f.Close(); err != nil {
		return Image{}, err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name+ext)); err != nil {
		return Image{}, err
	}
	if err := syncDir(dir); err != nil {
		return Image{}, err
	}
	return Image{URL: url, File: name + ext, Width: config.Width, Height: config.Height, MIME: "image/" + format}, nil
}

// syncDir flushes a rename in dir, so the saved metadata never refers to an
// image a power loss took back.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // dir is the image directory under -data
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// Downloaded images are kept at most this wide, landscape and portrait, and
// re-encoded at this JPEG quality: TheTVDB serves only originals, and
// Emby Web shows nothing wider than this.
const (
	maxLandscapeWidth = 1280
	maxPortraitWidth  = 780
	shrinkQuality     = 85
)

// shrink scales an image down to the widths above and re-encodes it as
// JPEG. It returns the image unchanged when that is not smaller, or when
// it has transparency a JPEG would lose.
func shrink(body []byte, config image.Config, format string) ([]byte, image.Config, string) {
	limit := maxPortraitWidth
	if config.Width > config.Height {
		limit = maxLandscapeWidth
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return body, config, format
	}
	if o, ok := img.(interface{ Opaque() bool }); ok && !o.Opaque() {
		return body, config, format
	}
	if config.Width > limit {
		scaled := image.NewRGBA(image.Rect(0, 0, limit, max(1, config.Height*limit/config.Width)))
		draw.CatmullRom.Scale(scaled, scaled.Bounds(), img, img.Bounds(), draw.Src, nil)
		img = scaled
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: shrinkQuality}); err != nil || out.Len() >= len(body) {
		return body, config, format
	}
	return out.Bytes(), image.Config{Width: img.Bounds().Dx(), Height: img.Bounds().Dy()}, "jpeg"
}

// readImage describes a saved image, checking it as download did.
func readImage(dir, file string) (Image, error) {
	f, err := os.Open(filepath.Join(dir, file)) //nolint:gosec // file is a hex name in the image directory
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = f.Close() }()
	head, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return Image{}, err
	}
	config, format, err := decodeConfig(head)
	if err != nil {
		return Image{}, err
	}
	return Image{File: file, Width: config.Width, Height: config.Height, MIME: "image/" + format}, nil
}

func decodeConfig(b []byte) (image.Config, string, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || (format != "jpeg" && format != "png") || config.Width <= 0 || config.Height <= 0 ||
		config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > 32_000_000 {
		return image.Config{}, "", errUnusable
	}
	return config, format, nil
}

// language holds what each provider calls a metadata language.
type language struct {
	tmdb, tvdb, country, tvdbCountry string
}

// Languages are the metadata languages Couch supports: those its home
// screen strings are translated to.
var languages = map[string]language{
	"en": {"en-US", "eng", "US", "usa"},
	"ru": {"ru-RU", "rus", "RU", "rus"},
	"de": {"de-DE", "deu", "DE", "deu"},
	"fr": {"fr-FR", "fra", "FR", "fra"},
	"es": {"es-ES", "spa", "ES", "esp"},
	"it": {"it-IT", "ita", "IT", "ita"},
}

// SupportedLanguages lists the codes Config.Language accepts.
func SupportedLanguages() []string { return []string{"en", "ru", "de", "fr", "es", "it"} }
