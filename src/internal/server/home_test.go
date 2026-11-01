package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
)

// TestHomeSectionTitlesFollowLanguage covers #18: row titles follow
// X-Emby-Language for the chosen languages and fall back to English.
func TestHomeSectionTitlesFollowLanguage(t *testing.T) {
	store, _, _ := newTestServer(t)
	catalog := &media.Catalog{ID: "movies", Items: []media.Item{{ID: "m", Name: "Movie"}}}
	h := New(store, "test", nil, catalog).Handler()
	token := login(t, h)
	path := "/Users/" + testUser(store).ID + "/HomeSections"
	titles := func(query, header string) string {
		r := httptest.NewRequestWithContext(t.Context(), "GET", path+query, nil)
		r.Header.Set("X-Emby-Token", token)
		if header != "" {
			r.Header.Set("X-Emby-Language", header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expectStatus(t, w, 200)
		var sections []struct{ Name string }
		if err := json.Unmarshal(w.Body.Bytes(), &sections); err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, s := range sections {
			names = append(names, s.Name)
		}
		return strings.Join(names, "|")
	}
	// Editing is refused with a message the client shows, not "not found" (#100).
	for _, suffix := range []string{"", "/Delete", "/Move"} {
		r := httptest.NewRequestWithContext(t.Context(), "POST", path+suffix, strings.NewReader(`{"Id":"resume"}`))
		r.Header.Set("X-Emby-Token", token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expectStatus(t, w, 400)
		if !strings.Contains(w.Body.String(), "cannot be added, removed or moved") {
			t.Fatalf("POST HomeSections%s: %q", suffix, w.Body.String())
		}
	}
	for _, tc := range []struct{ query, header, want string }{
		{"?X-Emby-Language=ru", "", "Мои медиаданные|Продолжение просмотра|Недавно добавленные фильмы"},
		{"?X-Emby-Language=es-MX", "", "Mis Contenidos|Continuar viendo|Ultimas Películas"},
		{"", "de", "Meine Medien|Weiterschauen|Neueste Filme"},
		{"?X-Emby-Language=ja", "", "My Media|Continue Watching|Latest Movies"},
		{"", "", "My Media|Continue Watching|Latest Movies"},
	} {
		if got := titles(tc.query, tc.header); got != tc.want {
			t.Fatalf("%q %q: got %s, want %s", tc.query, tc.header, got, tc.want)
		}
	}
}
