package server

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// homeSectionTitles gives the locales/*.yaml key of each section type's
// default title. These are the types of Emby Web's section editor that Couch
// can fill; On Now and Active Recordings need Live TV, which Couch does not
// have. A Latest Media block becomes one row per library on the home screen.
var homeSectionTitles = map[string]string{
	"userviews":             "HeaderMyMedia",
	"resume":                "HeaderContinueWatching",
	"resumeaudio":           "HeaderContinueListening",
	"latestmediablock":      "HeaderLatestMedia",
	"collections":           "Collections",
	"playlists":             "Playlists",
	"boxset":                "Collections",
	"playlist":              "Playlists",
	"items":                 "DynamicMedia",
	"latestmoviereleases":   "RecentlyReleasedMovies",
	"latestepisodereleases": "RecentlyReleasedEpisodes",
}

var errHomeSectionNotFound = errors.New("home section not found")

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

// defaultHomeSections is the home screen of a user who has not arranged one:
// the library tiles, the resume rows and the latest rows of unplayed items.
// Continue Listening is left out without a music library.
func defaultHomeSections(catalog *media.Catalog) []state.HomeSection {
	unplayed := false
	sections := []state.HomeSection{
		{ID: "smalllibrarytiles", SectionType: "userviews", IncludeNextUpInResume: true},
		{ID: "resume", SectionType: "resume", IncludeNextUpInResume: true},
	}
	if slices.ContainsFunc(catalog.Libraries(), func(l media.Library) bool { return l.Type == media.Music }) {
		sections = append(sections, state.HomeSection{ID: "resumeaudio", SectionType: "resumeaudio", IncludeNextUpInResume: true})
	}
	return append(sections, state.HomeSection{ID: "latestmedia", SectionType: "latestmediablock", IncludeNextUpInResume: true,
		Query: state.HomeSectionQuery{IsPlayed: &unplayed}})
}

// homeRow is a row of the home screen: a section, or one library's row of a
// Latest Media block.
type homeRow struct {
	id      string
	section state.HomeSection
	library string
}

// homeRows lists the user's home screen. The editor sees each section once;
// the home screen sees the sections for displayMode ("tv" or
// "mobile,desktop"), with a Latest Media block expanded into one row per
// library it includes.
func homeRows(user state.User, catalog *media.Catalog, edit bool, displayMode string) []homeRow {
	sections := user.HomeSections
	if sections == nil {
		sections = defaultHomeSections(catalog)
	}
	rows := []homeRow{}
	for _, section := range sections {
		if !edit && !shownIn(section.DisplayMode, displayMode) {
			continue
		}
		if edit || section.SectionType != "latestmediablock" {
			rows = append(rows, homeRow{id: section.ID, section: section})
			continue
		}
		for _, id := range libraryIDs(catalog) {
			if !slices.Contains(section.ExcludedFolders, id) {
				rows = append(rows, homeRow{id: section.ID + "_" + id, section: section, library: id})
			}
		}
	}
	return rows
}

// shownIn reports whether a section for the display modes sectionModes shows
// in displayMode; an empty list stands for every mode.
func shownIn(sectionModes, displayMode string) bool {
	if sectionModes == "" || displayMode == "" {
		return true
	}
	for _, mode := range strings.Split(sectionModes, ",") {
		if slices.Contains(strings.Split(displayMode, ","), mode) {
			return true
		}
	}
	return false
}

func (s *Server) homeSections(r *http.Request, session state.Session) []object {
	snapshot := s.store.Snapshot()
	catalog := s.catalogFor(session)
	language := value(r, "X-Emby-Language")
	edit := strings.EqualFold(r.URL.Query().Get("isEditMode"), "true")
	result := []object{}
	for _, row := range homeRows(userOf(snapshot, session), catalog, edit, r.URL.Query().Get("displayMode")) {
		if row.library != "" {
			result = append(result, s.latestSection(row, catalog, snapshot.ServerID, language))
			continue
		}
		result = append(result, homeSectionDTO(row.section, s.homeSectionName(row.section, language, session)))
	}
	return result
}

func (s *Server) homeSectionName(section state.HomeSection, language string, session state.Session) string {
	if section.CustomName != "" {
		return section.CustomName
	}
	switch section.SectionType {
	case "boxset":
		if set, ok := s.sessionBoxSets(session)[section.ParentID]; ok {
			return set.Name
		}
	case "playlist":
		if p, ok := s.sessionPlaylists(session)[section.ParentID]; ok {
			return p.Name
		}
	}
	return translate(language, homeSectionTitles[section.SectionType])
}

// homeSectionDTO returns the section with what the editor saved, so editing
// it again starts from its values.
func homeSectionDTO(section state.HomeSection, name string) object {
	monitor, cardSizeOffset := []string{}, 0
	switch section.SectionType {
	case "userviews":
		cardSizeOffset = -1
	case "resume":
		monitor = []string{"videoplayback", "markplayed"}
	case "resumeaudio":
		monitor = []string{"audioplayback", "markplayed"}
	}
	dto := homeSection(section.ID, name, section.SectionType, monitor, cardSizeOffset)
	dto["IncludeNextUpInResume"] = section.IncludeNextUpInResume
	if section.ItemTypes != nil {
		dto["ItemTypes"] = section.ItemTypes
	}
	if section.ExcludedFolders != nil {
		dto["ExcludedFolders"] = section.ExcludedFolders
	}
	for key, value := range map[string]string{
		"CustomName": section.CustomName, "Subtitle": section.Subtitle, "ViewType": section.ViewType,
		"DisplayMode": section.DisplayMode, "ScrollDirection": section.ScrollDirection, "ImageType": section.ImageType,
		"ParentId": section.ParentID, "SortBy": section.SortBy, "SortOrder": section.SortOrder,
	} {
		if value != "" {
			dto[key] = value
		}
	}
	query := dto["Query"].(object)
	for key, ids := range map[string][]string{"GenreIds": section.Query.GenreIDs, "StudioIds": section.Query.StudioIDs, "TagIds": section.Query.TagIDs} {
		if ids != nil {
			query[key] = ids
		}
	}
	if section.Query.IsPlayed != nil {
		query["IsPlayed"] = *section.Query.IsPlayed
	}
	if section.Query.IsFavorite != nil {
		query["IsFavorite"] = *section.Query.IsFavorite
	}
	return dto
}

// latestSection is one library's row of a Latest Media block, titled by the
// library's type.
func (s *Server) latestSection(row homeRow, catalog *media.Catalog, serverID, language string) object {
	name, collectionType, monitor := translate(language, "HeaderLatestMovies"), media.Movies, []string{"markplayed", "videoplayback"}
	switch library, _ := catalog.Library(row.library); library.Type {
	case media.TVShows:
		name, collectionType = translate(language, "HeaderLatestEpisodes"), media.TVShows
	case media.Music:
		name, collectionType, monitor = translate(language, "HeaderLatestMusic"), media.Music, []string{"markplayed", "audioplayback"}
	}
	latest := homeSection(row.id, name, "latestmedia", monitor, 0)
	if row.section.ScrollDirection != "" {
		latest["ScrollDirection"] = row.section.ScrollDirection
	}
	latest["CollectionType"] = collectionType
	latest["ParentItem"] = s.collectionDTO(row.library, serverID, false)
	latest["ParentId"] = row.library
	if row.section.Query.IsPlayed != nil {
		latest["Query"].(object)["IsPlayed"] = *row.section.Query.IsPlayed
	}
	return latest
}

// sectionCatalog is catalog without the libraries a section excludes.
func (s *Server) sectionCatalog(catalog *media.Catalog, excluded []string) *media.Catalog {
	all := libraryIDs(catalog)
	ids := slices.DeleteFunc(slices.Clone(all), func(id string) bool { return slices.Contains(excluded, id) })
	if len(ids) == len(all) {
		return catalog
	}
	return s.catalogOnly(s.catalog(), ids)
}

// sectionItems answers /Users/{id}/Sections/{section}/Items. Emby Web does not
// build a query per home row itself: it asks the server to fill each section it
// was given, so every row homeRows lists must be answerable.
func (s *Server) sectionItems(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	snapshot := s.store.Snapshot()
	catalog := s.catalogFor(session)
	var row homeRow
	found := false
	for _, candidate := range homeRows(userOf(snapshot, session), catalog, false, "") {
		if strings.EqualFold(candidate.id, r.PathValue("section")) {
			row, found = candidate, true
			break
		}
	}
	if !found {
		fail(w, 404, "NotFound")
		return
	}
	if row.section.SectionType != "userviews" && row.library == "" {
		s.sectionQuery(w, r, session, s.sectionCatalog(catalog, row.section.ExcludedFolders), row.section)
		return
	}
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
	serverID := snapshot.ServerID
	items := []object{}
	if row.library == "" {
		excluded := row.section.ExcludedFolders
		for _, id := range libraryIDs(catalog) {
			if !slices.Contains(excluded, id) {
				items = append(items, s.collectionDTO(id, serverID, false))
			}
		}
		if sets := s.sessionBoxSets(session); len(sets) > 0 && !slices.Contains(excluded, collectionsViewID) {
			items = append(items, collectionsViewDTO(serverID, len(sets), false))
		}
		if lists := s.sessionPlaylists(session); len(lists) > 0 && !slices.Contains(excluded, playlistsViewID) {
			items = append(items, playlistsViewDTO(serverID, len(lists), false))
		}
	} else {
		latest := make([]*media.Item, 0, len(catalog.Items))
		// New tracks are shown as their albums, dated by the newest one.
		albums := map[string]*media.Item{}
		for i := range catalog.Items {
			item := &catalog.Items[i]
			played := row.section.Query.IsPlayed
			if (played != nil && userOf(snapshot, session).Items[item.ID].Played != *played) || catalog.LibraryOf(*item) != row.library {
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
	}
	if len(items) > limit {
		items = items[:limit]
	}
	respond(w, 200, object{"Items": items, "TotalRecordCount": len(items)})
}

// sectionQuery fills a section with an Items query over catalog: the client's
// paging and fields, and the section's types, parent, sort and filter.
func (s *Server) sectionQuery(w http.ResponseWriter, r *http.Request, session state.Session, catalog *media.Catalog, section state.HomeSection) {
	set := map[string]string{}
	resume := false
	switch section.SectionType {
	case "resume":
		resume = true
		set["MediaTypes"] = "Video"
		if !section.IncludeNextUpInResume {
			set["IncludeNextUp"] = "false"
		}
	case "resumeaudio":
		resume = true
		set["MediaTypes"] = "Audio"
	case "collections":
		set["IncludeItemTypes"], set["Recursive"] = "BoxSet", "true"
	case "playlists":
		set["IncludeItemTypes"], set["Recursive"] = "Playlist", "true"
	case "boxset", "playlist":
		set["ParentId"] = section.ParentID
	case "items":
		set["IncludeItemTypes"], set["Recursive"] = strings.Join(section.ItemTypes, ","), "true"
	case "latestmoviereleases", "latestepisodereleases":
		// Like Emby Web's own query for these rows: released in the last year, newest first.
		set["IncludeItemTypes"], set["SortBy"] = "Movie", "ProductionYear,PremiereDate,SortName"
		if section.SectionType == "latestepisodereleases" {
			set["IncludeItemTypes"], set["SortBy"] = "Episode", "PremiereDate,SortName"
		}
		set["Recursive"], set["SortOrder"] = "true", "Descending"
		set["MinPremiereDate"] = time.Now().AddDate(-1, 0, 0).UTC().Format(time.RFC3339)
	}
	if section.SortBy != "" {
		set["SortBy"], set["SortOrder"] = section.SortBy, section.SortOrder
	}
	if q := section.Query; q.IsPlayed != nil {
		set["IsPlayed"] = strconv.FormatBool(*q.IsPlayed)
	}
	if q := section.Query; q.IsFavorite != nil {
		set["IsFavorite"] = strconv.FormatBool(*q.IsFavorite)
	}
	for key, ids := range map[string][]string{"GenreIds": section.Query.GenreIDs, "StudioIds": section.Query.StudioIDs, "TagIds": section.Query.TagIDs} {
		if len(ids) > 0 {
			set[key] = strings.Join(ids, ",")
		}
	}
	query := r.URL.Query()
	for key := range query {
		// RandomSeed only keeps a shuffled row stable, and Couch has no shuffled rows.
		if strings.EqualFold(key, "RandomSeed") || containsFold(slices.Collect(maps.Keys(set)), key) {
			delete(query, key)
		}
	}
	for key, v := range set {
		query.Set(key, v)
	}
	items := r.Clone(r.Context())
	items.URL.RawQuery = query.Encode()
	s.listItemsIn(w, items, session, catalog, false, resume)
}

// homeSectionRequest is the section Emby Web's editor saves.
type homeSectionRequest struct {
	ID                    string `json:"Id"`
	SectionType           string
	CustomName            string
	Subtitle              string
	ViewType              string
	DisplayMode           string
	ScrollDirection       string
	ImageType             string
	ParentID              string `json:"ParentId"`
	SortBy                string
	SortOrder             string
	ItemTypes             []string
	ExcludedFolders       []string
	IncludeNextUpInResume *bool
	Query                 struct {
		IsPlayed, IsFavorite, IsResumable flexBool
		GenreIds, StudioIds, TagIds       flexIDs
	}
}

// flexBool is a filter value, which the editor's filter menu sends as a
// boolean, as "true" or "false", or as null for either.
type flexBool struct{ value *bool }

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch v := v.(type) {
	case nil:
		b.value = nil
	case bool:
		b.value = &v
	case string:
		if v == "" {
			b.value = nil
			return nil
		}
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			return err
		}
		b.value = &parsed
	default:
		return errors.New("not a boolean")
	}
	return nil
}

// flexIDs is a list of genre, studio or tag IDs, which the filter menu sends
// as an array of numbers or strings, or as a comma-separated string.
type flexIDs []string

func (ids *flexIDs) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*ids = nil
	switch v := v.(type) {
	case nil:
	case string:
		for _, id := range strings.Split(v, ",") {
			if id != "" {
				*ids = append(*ids, id)
			}
		}
	case []any:
		for _, id := range v {
			switch id := id.(type) {
			case string:
				*ids = append(*ids, id)
			case float64:
				*ids = append(*ids, strconv.FormatFloat(id, 'f', -1, 64))
			default:
				return errors.New("not an ID")
			}
		}
	default:
		return errors.New("not a list of IDs")
	}
	return nil
}

// saveHomeSection adds a section, or replaces the one with the request's Id.
func (s *Server) saveHomeSection(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	var req homeSectionRequest
	if decodeJSON(w, r, &req) != nil {
		fail(w, 400, "InvalidRequest")
		return
	}
	switch {
	case req.SectionType == "onnow" || req.SectionType == "activerecordings":
		fail(w, 400, "NoLiveTV")
		return
	case homeSectionTitles[req.SectionType] == "":
		fail(w, 400, "UnsupportedSection")
		return
	case req.SectionType == "items" && len(req.ItemTypes) == 0,
		req.SectionType == "boxset" && !s.hasBoxSet(session, req.ParentID),
		req.SectionType == "playlist" && !s.hasPlaylist(session, req.ParentID),
		!member(",Horizontal,Vertical", req.ScrollDirection),
		!member(",Ascending,Descending", req.SortOrder):
		fail(w, 400, "InvalidRequest")
		return
	case req.Query.IsResumable.value != nil:
		fail(w, 400, "UnsupportedFilter")
		return
	}
	for _, key := range strings.Split(strings.ToLower(req.SortBy), ",") {
		if req.SortBy != "" && !member(supportedSorts, key) {
			fail(w, 400, "UnsupportedSort")
			return
		}
	}
	section := state.HomeSection{
		ID: req.ID, SectionType: req.SectionType, CustomName: req.CustomName, Subtitle: req.Subtitle,
		ViewType: req.ViewType, DisplayMode: req.DisplayMode, ScrollDirection: req.ScrollDirection, ImageType: req.ImageType,
		SortBy: req.SortBy, SortOrder: req.SortOrder, ItemTypes: req.ItemTypes, ExcludedFolders: req.ExcludedFolders,
		IncludeNextUpInResume: req.IncludeNextUpInResume == nil || *req.IncludeNextUpInResume,
		Query: state.HomeSectionQuery{
			IsPlayed: req.Query.IsPlayed.value, IsFavorite: req.Query.IsFavorite.value,
			GenreIDs: req.Query.GenreIds, StudioIDs: req.Query.StudioIds, TagIDs: req.Query.TagIds,
		},
	}
	if section.SectionType == "boxset" || section.SectionType == "playlist" {
		section.ParentID = req.ParentID
	}
	s.changeHomeSections(w, token, session, func(sections []state.HomeSection) ([]state.HomeSection, error) {
		if section.ID == "" {
			return append(slices.Clone(sections), section), nil
		}
		i := slices.IndexFunc(sections, func(old state.HomeSection) bool { return strings.EqualFold(old.ID, section.ID) })
		if i < 0 {
			return nil, errHomeSectionNotFound
		}
		sections = slices.Clone(sections)
		section.ID = sections[i].ID
		sections[i] = section
		return sections, nil
	})
}

// changeHomeSections applies fn to the user's home screen, starting from the
// default one when the user has not arranged it yet.
func (s *Server) changeHomeSections(w http.ResponseWriter, token string, session state.Session, fn func([]state.HomeSection) ([]state.HomeSection, error)) {
	catalog := s.catalogFor(session)
	err := s.store.ChangeHomeSections(token, func(sections []state.HomeSection) ([]state.HomeSection, error) {
		if sections == nil {
			sections = defaultHomeSections(catalog)
		}
		return fn(sections)
	})
	switch {
	case errors.Is(err, errHomeSectionNotFound):
		fail(w, 404, "NotFound")
	case errors.Is(err, state.ErrHomeSectionLimit):
		fail(w, 400, "HomeSectionLimitReached")
	default:
		s.changed(w, err)
	}
}

func (s *Server) homeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /users/{user}/homesections", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, s.homeSections(r, session))
	}))
	mux.HandleFunc("GET /users/{user}/sections/{section}/items", s.protect(s.sectionItems))
	mux.HandleFunc("POST /users/{user}/homesections", s.protect(s.saveHomeSection))
	mux.HandleFunc("POST /users/{user}/homesections/delete", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var req struct{ Ids []string }
		if decodeJSON(w, r, &req) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		s.changeHomeSections(w, token, session, func(sections []state.HomeSection) ([]state.HomeSection, error) {
			return slices.DeleteFunc(slices.Clone(sections), func(section state.HomeSection) bool { return containsFold(req.Ids, section.ID) }), nil
		})
	}))
	// Move puts the sections with Ids, in their current order, at NewIndex
	// of the list without them, as dragging one in the editor expects.
	mux.HandleFunc("POST /users/{user}/homesections/move", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var req struct {
			Ids      []string
			NewIndex int
		}
		if decodeJSON(w, r, &req) != nil || req.NewIndex < 0 {
			fail(w, 400, "InvalidRequest")
			return
		}
		s.changeHomeSections(w, token, session, func(sections []state.HomeSection) ([]state.HomeSection, error) {
			var moved, rest []state.HomeSection
			for _, section := range sections {
				if containsFold(req.Ids, section.ID) {
					moved = append(moved, section)
				} else {
					rest = append(rest, section)
				}
			}
			at := min(req.NewIndex, len(rest))
			return slices.Concat(rest[:at], moved, rest[at:]), nil
		})
	}))
}

func (s *Server) hasBoxSet(session state.Session, id string) bool {
	_, ok := s.sessionBoxSets(session)[id]
	return ok
}

func (s *Server) hasPlaylist(session state.Session, id string) bool {
	_, ok := s.sessionPlaylists(session)[id]
	return ok
}

func containsFold(list []string, value string) bool {
	return slices.ContainsFunc(list, func(v string) bool { return strings.EqualFold(v, value) })
}
