package server

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// homeSection mirrors the section descriptor Emby returns from
// /Users/{id}/HomeSections. The client reads every key below without a guard,
// so all of them are emitted even when empty.
func homeSection(id, name, sectionType string, monitor []string, cardSizeOffset int) object {
	return object{
		"Name":                  name,
		"Id":                    id,
		"SectionType":           sectionType,
		"Monitor":               monitor,
		"ItemTypes":             []string{},
		"ExcludedFolders":       []string{},
		"CardSizeOffset":        cardSizeOffset,
		"IncludeNextUpInResume": true,
		"Query": object{
			"StudioIds":       []string{},
			"TagIds":          []string{},
			"GenreIds":        []string{},
			"CollectionTypes": []string{},
		},
	}
}

// homeSections describes the home screen Couch can actually populate: the
// library tiles, the resume rows, and one latest row per library. Titles come
// from locales/*.yaml by X-Emby-Language (#18). Sections for
// media Couch does not serve (Live TV) are not advertised, nor Continue
// Listening without a music library.
func (s *Server) homeSections(catalog *media.Catalog, serverID, language string) []object {
	sections := []object{
		homeSection("smalllibrarytiles", translate(language, "HeaderMyMedia"), "userviews", []string{}, -1),
		homeSection("resume", translate(language, "HeaderContinueWatching"), "resume", []string{"videoplayback", "markplayed"}, 0),
	}
	if slices.ContainsFunc(catalog.Libraries(), func(l media.Library) bool { return l.Type == media.Music }) {
		sections = append(sections, homeSection("resumeaudio", translate(language, "HeaderContinueListening"), "resumeaudio", []string{"audioplayback", "markplayed"}, 0))
	}
	for _, id := range libraryIDs(catalog) {
		name, collectionType, monitor := translate(language, "HeaderLatestMovies"), media.Movies, []string{"markplayed", "videoplayback"}
		switch library, _ := catalog.Library(id); library.Type {
		case media.TVShows:
			name, collectionType = translate(language, "HeaderLatestEpisodes"), media.TVShows
		case media.Music:
			name, collectionType, monitor = translate(language, "HeaderLatestMusic"), media.Music, []string{"markplayed", "audioplayback"}
		}
		latest := homeSection("latestmedia_"+id, name, "latestmedia", monitor, 0)
		latest["CollectionType"] = collectionType
		latest["ParentItem"] = s.collectionDTO(id, serverID, false)
		latest["ParentId"] = id
		latest["Query"] = object{
			"StudioIds":       []string{},
			"TagIds":          []string{},
			"GenreIds":        []string{},
			"CollectionTypes": []string{},
			"IsPlayed":        false,
		}
		sections = append(sections, latest)
	}
	return sections
}

// sectionItems answers /Users/{id}/Sections/{section}/Items. Emby Web does not
// build a query per home row itself: it asks the server to fill each section it
// was given, so every section id advertised by homeSections must be answerable.
func (s *Server) sectionItems(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	catalog := s.catalogFor(session)
	section := strings.ToLower(r.PathValue("section"))
	limit := 12
	if text := r.URL.Query().Get("Limit"); text != "" {
		v, err := strconv.Atoi(text)
		if err != nil || v < 0 {
			fail(w, 400, "InvalidPagination")
			return
		}
		// The card menu asks for the whole row with Limit=5000 to offer
		// "play from here"; a row never needs more than 1000 items.
		limit = min(v, 1000)
	}
	snapshot := s.store.Snapshot()
	serverID := snapshot.ServerID
	items := []object{}
	switch {
	case section == "smalllibrarytiles":
		for _, id := range libraryIDs(catalog) {
			items = append(items, s.collectionDTO(id, serverID, false))
		}
		if sets := s.sessionBoxSets(session); len(sets) > 0 {
			items = append(items, collectionsViewDTO(serverID, len(sets), false))
		}
		if lists := s.sessionPlaylists(session); len(lists) > 0 {
			items = append(items, playlistsViewDTO(serverID, len(lists), false))
		}
	case section == "resume":
		items = s.resumeItems(limit, token, session, false)
	case section == "resumeaudio":
		items = s.resumeItems(limit, token, session, true)
	case strings.HasPrefix(section, "latestmedia_") && slices.Contains(libraryIDs(catalog), strings.TrimPrefix(section, "latestmedia_")):
		library := strings.TrimPrefix(section, "latestmedia_")
		latest := make([]*media.Item, 0, len(catalog.Items))
		// New tracks are shown as their albums, dated by the newest one.
		albums := map[string]*media.Item{}
		for i := range catalog.Items {
			item := &catalog.Items[i]
			if userOf(snapshot, session).Items[item.ID].Played || catalog.LibraryOf(*item) != library {
				continue
			}
			album, ok := catalog.Lookup(item.ParentID)
			if !ok || album.Type() != "MusicAlbum" {
				latest = append(latest, item)
				continue
			}
			if albums[album.ID] == nil {
				albums[album.ID] = album
				latest = append(latest, album)
			}
		}
		slices.SortFunc(latest, func(a, b *media.Item) int {
			if c := b.Added.Compare(a.Added); c != 0 {
				return c
			}
			return strings.Compare(a.ID, b.ID)
		})
		for _, item := range latest[:min(limit, len(latest))] {
			items = append(items, s.movieDTO(*item, serverID, userOf(snapshot, session).Items, token))
		}
	default:
		fail(w, 404, "NotFound")
		return
	}
	if len(items) > limit {
		items = items[:limit]
	}
	respond(w, 200, object{"Items": items, "TotalRecordCount": len(items)})
}

func (s *Server) homeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /users/{user}/homesections", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, s.homeSections(s.catalogFor(session), s.store.Snapshot().ServerID, value(r, "X-Emby-Language")))
	}))
	mux.HandleFunc("GET /users/{user}/sections/{section}/items", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		s.sectionItems(w, r, token, session)
	}))
	// Emby Web's Home Screen settings add, remove and move sections with
	// these. Until editing is implemented (#100), the client's error dialog
	// says so instead of "not found".
	fixed := s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		failText(w, 400, "Couch's home screen sections are fixed for now: they cannot be added, removed or moved.")
	})
	for _, route := range []string{"POST /users/{user}/homesections", "POST /users/{user}/homesections/delete", "POST /users/{user}/homesections/move"} {
		mux.HandleFunc(route, fixed)
	}
}
