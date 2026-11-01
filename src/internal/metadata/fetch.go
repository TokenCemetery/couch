package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG for image.DecodeConfig
	_ "image/png"  // register PNG for image.DecodeConfig
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
)

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
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// fetch sends req and returns the body of a 200 response, at most limit bytes.
func fetch(req *http.Request, pace *throttle, limit int64) ([]byte, error) {
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
	name := ID("file", url)
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
	if err := f.Close(); err != nil {
		return Image{}, err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, name+ext)); err != nil {
		return Image{}, err
	}
	return Image{URL: url, File: name + ext, Width: config.Width, Height: config.Height, MIME: "image/" + format}, nil
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
		return image.Config{}, "", errors.New("not a usable JPEG or PNG image")
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
