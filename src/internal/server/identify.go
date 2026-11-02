package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/metadata"
	"github.com/TokenCemetery/couch/internal/state"
)

// externalIDs are the provider IDs Identify can look an item up by, as Emby
// names them; TMDb also resolves IMDb IDs.
var externalIDs = map[string][]object{
	"Movie": {
		{"Name": "TheMovieDb", "Key": "Tmdb", "Website": "https://www.themoviedb.org", "UrlFormatString": "https://www.themoviedb.org/movie/{0}", "IsSupportedAsIdentifier": true},
		{"Name": "IMDb", "Key": "Imdb", "Website": "https://www.imdb.com", "UrlFormatString": "https://www.imdb.com/title/{0}", "IsSupportedAsIdentifier": true},
	},
	"Series": {
		{"Name": "TheTVDB", "Key": "Tvdb", "Website": "https://thetvdb.com", "UrlFormatString": "https://thetvdb.com/?tab=series&id={0}", "IsSupportedAsIdentifier": true},
		{"Name": "TheMovieDb", "Key": "Tmdb", "Website": "https://www.themoviedb.org", "UrlFormatString": "https://www.themoviedb.org/tv/{0}", "IsSupportedAsIdentifier": true},
		{"Name": "IMDb", "Key": "Imdb", "Website": "https://www.imdb.com", "UrlFormatString": "https://www.imdb.com/title/{0}", "IsSupportedAsIdentifier": true},
	},
}

// notIdentified answers Identify and Remove Identification on a collection,
// which Emby Web offers by the role alone.
const notIdentified = "Couch identifies movies and series, not collections."

// searchTimeout bounds a provider search the Identify dialog waits for.
const searchTimeout = 20 * time.Second

// identifyRoutes serve the administrator's metadata actions on an item page:
// Refresh Metadata, Identify, Remove Identification and the Metadata
// Manager, which shows an item's metadata but cannot save it (#115).
func (s *Server) identifyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /items/{item}/refresh", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		ids, ok := s.refreshScope(r.PathValue("item"))
		if !ok {
			fail(w, 404, "NotFound")
			return
		}
		if s.refresher == nil {
			failText(w, 400, "Metadata refresh is not available.")
			return
		}
		s.refresher.Refresh(ids)
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("GET /items/{item}/externalidinfos", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if _, ok := s.store.Snapshot().Collection(r.PathValue("item")); ok {
			failText(w, 400, notIdentified)
			return
		}
		item, found := findItem(s.catalog(), r.PathValue("item"))
		if !found {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, identifiers(item))
	}))
	for _, kind := range []string{"Movie", "Series"} {
		mux.HandleFunc("POST /items/remotesearch/"+strings.ToLower(kind), s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			var query struct {
				SearchInfo struct {
					Name        string
					Year        int
					ProviderIds map[string]string
				}
			}
			if decodeJSON(w, r, &query) != nil {
				fail(w, 400, "InvalidRequest")
				return
			}
			if s.refresher == nil {
				failText(w, 400, metadata.ErrNoProvider.Error()+".")
				return
			}
			ids := providerIDs(query.SearchInfo.ProviderIds)
			ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
			defer cancel()
			found, err := s.refresher.Search(ctx, kind, strings.TrimSpace(query.SearchInfo.Name), query.SearchInfo.Year, ids)
			// The Identify dialog shows no server error, only results, so a
			// search that cannot run answers with one result naming why. It
			// has no provider IDs, so applying it is refused.
			switch {
			case errors.Is(err, metadata.ErrNoProvider):
				respond(w, 200, searchMessage("No online metadata provider is configured: start Couch with COUCH_TMDB_API_KEY or COUCH_TVDB_API_KEY"))
				return
			case err != nil:
				respond(w, 200, searchMessage("The online metadata provider did not answer: "+err.Error()))
				return
			}
			results := []object{}
			for _, f := range found {
				result := object{"Name": f.Name, "ProviderIds": f.ProviderIDs, "SearchProviderName": f.Provider, "Overview": f.Overview}
				if f.Year > 0 {
					result["ProductionYear"] = f.Year
				}
				if f.ImageURL != "" {
					result["ImageUrl"] = f.ImageURL
				}
				results = append(results, result)
			}
			respond(w, 200, results)
		}))
	}
	mux.HandleFunc("GET /items/remotesearch/image", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
		defer cancel()
		body, mime, err := metadata.SearchImage(ctx, value(r, "imageUrl"))
		if err != nil {
			slog.Info("Identify poster unavailable", "error", err)
			fail(w, 404, "ImageUnavailable")
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(body) //nolint:gosec,nolintlint // a checked JPEG or PNG, sent with its type and nosniff
	}))
	mux.HandleFunc("POST /items/remotesearch/apply/{item}", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var result struct{ ProviderIds map[string]string }
		if decodeJSON(w, r, &result) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		item, found := findItem(s.catalog(), r.PathValue("item"))
		if !found {
			fail(w, 404, "NotFound")
			return
		}
		ids := providerIDs(result.ProviderIds)
		if len(identifiers(item)) == 0 || len(ids) == 0 || s.refresher == nil {
			failText(w, 400, "Couch identifies movies and series by a TheMovieDb, TheTVDB or IMDb ID.")
			return
		}
		s.refresher.Identify(item.ID, ids)
		children, _ := s.refreshScope(item.ID)
		s.refresher.Refresh(children)
		s.record(state.Activity{Type: "ItemIdentified", Name: s.displayName(item) + " was identified", Severity: "Info", UserID: session.UserID, ItemID: item.ID})
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("POST /items/metadata/reset", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		// Emby Web sends the IDs as JSON; the contract names a query
		// parameter.
		var body struct{ ItemIds string }
		if r.ContentLength != 0 && decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		if body.ItemIds == "" {
			body.ItemIds = value(r, "ItemIds")
		}
		if body.ItemIds == "" {
			fail(w, 400, "InvalidRequest")
			return
		}
		items := []media.Item{}
		for _, id := range strings.Split(body.ItemIds, ",") {
			if _, ok := s.store.Snapshot().Collection(strings.TrimSpace(id)); ok {
				failText(w, 400, notIdentified)
				return
			}
			item, found := findItem(s.catalog(), strings.TrimSpace(id))
			if !found {
				fail(w, 404, "NotFound")
				return
			}
			items = append(items, item)
		}
		if s.refresher == nil {
			failText(w, 400, "Metadata refresh is not available.")
			return
		}
		for _, item := range items {
			s.refresher.Identify(item.ID, nil)
			children, _ := s.refreshScope(item.ID)
			s.refresher.Refresh(children)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	// The Metadata Manager and the item editor show an item's fields; the
	// library is read-only, so a save is refused with the way to change them.
	// Saving a collection renames it.
	mux.HandleFunc("GET /items/{item}/metadataeditor", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if _, ok := s.store.Snapshot().Collection(r.PathValue("item")); ok {
			respond(w, 200, object{"ParentalRatingOptions": []any{}, "Countries": []any{}, "Cultures": []any{},
				"ExternalIdInfos": []any{}, "PersonExternalIdInfos": []any{}})
			return
		}
		item, found := findItem(s.catalog(), r.PathValue("item"))
		if !found {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, object{"ParentalRatingOptions": []any{}, "Countries": []any{}, "Cultures": []any{},
			"ExternalIdInfos": identifiers(item), "PersonExternalIdInfos": []any{}})
	}))
	mux.HandleFunc("POST /items/{item}", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if _, ok := s.store.Snapshot().Collection(r.PathValue("item")); ok {
			s.renameCollection(w, r, r.PathValue("item"))
			return
		}
		if _, found := findItem(s.catalog(), r.PathValue("item")); !found {
			fail(w, 404, "NotFound")
			return
		}
		failText(w, 400, "Couch does not edit metadata: change the item's .nfo file, or use Identify, then Refresh Metadata.")
	}))
}

// searchMessage is a search answer that tells the Identify dialog why there
// are no results; the dialog shows a result's name in full once chosen.
func searchMessage(text string) []object {
	return []object{{"Name": text, "ProviderIds": map[string]string{}, "SearchProviderName": "Couch"}}
}

// identifiers lists the IDs Identify can look the item up by.
func identifiers(item media.Item) []object {
	if infos, ok := externalIDs[item.Type()]; ok {
		return infos
	}
	return []object{}
}

// providerIDs keeps the non-empty IDs of the providers Couch asks.
func providerIDs(posted map[string]string) map[string]string {
	ids := map[string]string{}
	for key, value := range posted {
		value = strings.TrimSpace(value)
		for _, known := range []string{"Tmdb", "Tvdb", "Imdb"} {
			if strings.EqualFold(key, known) && value != "" && len(value) <= 64 {
				ids[known] = value
			}
		}
	}
	return ids
}

// refreshScope lists what refreshing id covers: the item; a series with its
// seasons and episodes; a library with everything in it. It reports false
// for an unknown ID.
func (s *Server) refreshScope(id string) ([]string, bool) {
	catalog := s.catalog()
	if _, ok := catalog.Library(id); ok {
		ids := []string{}
		for _, items := range [][]media.Item{catalog.Folders, catalog.Items} {
			for _, item := range items {
				if catalog.LibraryOf(item) == id {
					ids = append(ids, item.ID)
				}
			}
		}
		return ids, true
	}
	// A collection refreshes its items.
	if c, ok := s.store.Snapshot().Collection(id); ok {
		ids := []string{}
		for _, member := range c.Items {
			if scope, found := s.refreshScope(member); found {
				ids = append(ids, scope...)
			}
		}
		return ids, true
	}
	item, found := findItem(catalog, id)
	if !found {
		return nil, false
	}
	ids := []string{item.ID}
	if item.Type() == "Series" {
		for _, items := range [][]media.Item{catalog.Folders, catalog.Items} {
			for _, child := range items {
				if child.SeriesID == item.ID && child.ID != item.ID {
					ids = append(ids, child.ID)
				}
			}
		}
	}
	return ids, true
}
