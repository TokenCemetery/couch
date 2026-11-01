package server

import (
	"cmp"
	"errors"
	"math/rand/v2"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// rootID is the parent of every library. Emby uses a numeric string here; Couch
// keeps its own opaque value because clients treat item IDs as opaque.
const rootID = "root"

func catalogQuery(r *http.Request) (map[string]string, error) {
	query := map[string]string{}
	for key, vv := range r.URL.Query() {
		key = strings.ToLower(key)
		for _, v := range vv {
			if old, ok := query[key]; ok && old != v {
				return nil, errors.New("conflicting query parameters")
			}
			query[key] = v
		}
	}
	return query, nil
}

// boolRank orders false before true.
func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func member(list, value string) bool {
	for _, item := range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return true
		}
	}
	return false
}

func (s *Server) catalogRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /users/{user}/views", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		items := []any{}
		serverID := s.store.Snapshot().ServerID
		for _, id := range s.libraryIDs() {
			items = append(items, s.collectionDTO(id, serverID, false))
		}
		respond(w, 200, object{"Items": items, "TotalRecordCount": len(items)})
	}))
	for _, path := range []string{"/items/root", "/users/{user}/items/root"} {
		mux.HandleFunc("GET "+path, s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			respond(w, 200, s.rootFolderDTO(s.store.Snapshot().ServerID))
		}))
	}
	for _, path := range []string{"/items/{item}", "/users/{user}/items/{item}"} {
		mux.HandleFunc("GET "+path, s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			id := r.PathValue("item")
			// A rescan replaces the catalog, so each request reads it (#75).
			if catalog := s.catalog(); catalog != nil {
				snapshot := s.store.Snapshot()
				if id == catalog.ID || (len(catalog.Folders) > 0 && id == catalog.SeriesLibraryID()) {
					respond(w, 200, s.collectionDTO(id, snapshot.ServerID, true))
					return
				}
				if item, found := s.findItem(id); found {
					respond(w, 200, s.movieDTO(item, snapshot.ServerID, snapshot.User.Items, token))
					return
				}
				if e, found := s.entity(id); found {
					respond(w, 200, entityDTO(id, e, snapshot.ServerID))
					return
				}
			}
			fail(w, 404, "NotFound")
		}))
	}
	for _, path := range []string{"/items", "/users/{user}/items", "/users/{user}/items/latest"} {
		latest := strings.HasSuffix(path, "/latest")
		mux.HandleFunc("GET "+path, s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			s.listItems(w, r, latest, false)
		}))
	}
	mux.HandleFunc("POST /users/{user}/searcheditems", s.protect(s.reportSearched))
	// The "Clear" button on the search page; Emby Web sends no body.
	mux.HandleFunc("POST /users/{user}/recentlysearched/delete", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		searched := []string{}
		for id, st := range s.store.Snapshot().User.Items {
			if !st.LastSearched.IsZero() {
				searched = append(searched, id)
			}
		}
		_, err := s.store.SetItems(token, searched, func(_ string, st *state.ItemState) { st.LastSearched = time.Time{} })
		s.changed(w, err)
	}))
	s.extrasRoutes(mux)
	s.seriesRoutes(mux)
	s.imageRoutes(mux)
	s.userImageRoutes(mux)
	mux.HandleFunc("GET /itemtypes", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		s.listItems(w, r, false, false)
	}))
}

// extrasRoutes answers the companion requests Emby Web makes on an item page.
// Couch has none of this content, so each returns an empty result in the shape
// the reference server uses; a 404 here leaves the page in an error state.
func (s *Server) extrasRoutes(mux *http.ServeMux) {
	known := func(r *http.Request) bool {
		id := r.PathValue("item")
		_, found := s.findItem(id)
		_, isEntity := s.entity(id)
		return found || isEntity || slices.Contains(s.libraryIDs(), id)
	}
	emptyPage := func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !known(r) {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, object{"Items": []any{}, "TotalRecordCount": 0})
	}
	emptyArray := func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !known(r) {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, []any{})
	}
	for _, path := range []string{"/users/{user}/items/{item}/intros", "/items/{item}/similar"} {
		mux.HandleFunc("GET "+path, s.protect(emptyPage))
	}
	for _, path := range []string{"/users/{user}/items/{item}/localtrailers", "/users/{user}/items/{item}/specialfeatures", "/items/{item}/specialfeatures"} {
		mux.HandleFunc("GET "+path, s.protect(emptyArray))
	}
	mux.HandleFunc("GET /items/{item}/thememedia", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !known(r) {
			fail(w, 404, "NotFound")
			return
		}
		empty := object{"Items": []any{}, "TotalRecordCount": 0, "OwnerId": r.PathValue("item")}
		respond(w, 200, object{"ThemeVideosResult": empty, "ThemeSongsResult": empty, "SoundtrackSongsResult": empty})
	}))
	// Couch generates no seek-bar previews. Emby Web's player re-requests the
	// set after every failure, so an empty set is answered instead of 404.
	mux.HandleFunc("GET /items/{item}/thumbnailset", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !known(r) {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, object{"Thumbnails": []any{}})
	}))
}

// reportSearched records items the user opened from search results; Emby Web
// lists them on the empty search page as "Recently searched".
func (s *Server) reportSearched(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	var report struct {
		Ids         []string
		WasSearched *bool
	}
	if decodeJSON(w, r, &report) != nil || len(report.Ids) == 0 || len(report.Ids) > 100 || report.WasSearched == nil {
		fail(w, 400, "InvalidRequest")
		return
	}
	for _, id := range report.Ids {
		if _, found := s.findItem(id); !found {
			fail(w, 404, "NotFound")
			return
		}
	}
	now := time.Now().UTC()
	for _, id := range report.Ids {
		err := s.store.SetItem(token, id, func(st *state.ItemState) {
			if *report.WasSearched {
				st.LastSearched = now
			} else {
				st.LastSearched = time.Time{}
			}
		})
		if err != nil {
			s.changed(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request, latest, resume bool) {
	catalog := s.catalog()
	token, _ := requestToken(r) // Callers have already authenticated this request.
	typesOnly := r.URL.Path == "/itemtypes"
	query, err := catalogQuery(r)
	if err != nil {
		fail(w, 400, "InvalidQuery")
		return
	}
	// Reject unsupported filters rather than returning a misleading unfiltered catalogue.
	for key := range query {
		switch key {
		case "parentid", "recursive", "searchterm", "includeitemtypes", "excludeitemtypes", "mediatypes", "ids", "excludeitemids",
			"startindex", "limit", "sortby", "sortorder", "isfolder", "isplayed", "isfavorite", "filters",
			"fields", "enableimages", "enableimagetypes", "imagetypelimit", "enableuserdata", "enabletotalrecordcount", "groupitems",
			"groupprogramsbyseries", "includesearchtypes", "isstandalonespecial", "collapseboxsetitems", "excludelocationtypes",
			"userid", "api_key", "x-mediabrowser-token", "reqformat", "listitemids", "wassearched", "minpremieredate", "isunaired", "includenextup", "isspecialepisode", "ismissing", "isvirtualunaired", "collectiontypes", "canedititems",
			"containers", "videocodecs", "audiocodecs", "audiolayouts", "subtitlecodecs", "audiolanguages", "subtitlelanguages", "extendedvideotypes",
			"genres", "studios", "tags", "officialratings", "years", "genreids", "studioids", "tagids", "personids", "persontypes":
		default:
			if !strings.HasPrefix(key, "x-emby-") {
				fail(w, 400, "UnsupportedQuery")
				return
			}
		}
	}
	for _, key := range []string{"recursive", "isfolder", "isplayed", "isfavorite", "groupprogramsbyseries", "includesearchtypes", "isstandalonespecial", "collapseboxsetitems", "wassearched", "isunaired", "includenextup", "isspecialepisode", "ismissing", "isvirtualunaired", "canedititems"} {
		query[key] = strings.ToLower(query[key])
		if v := query[key]; v != "" && v != "true" && v != "false" {
			fail(w, 400, "InvalidQuery")
			return
		}
	}
	for _, filter := range strings.Split(query["filters"], ",") {
		if filter != "" && !member("IsUnplayed,IsPlayed,IsFavorite,IsFolder,IsNotFolder", filter) {
			fail(w, 400, "UnsupportedFilter")
			return
		}
	}
	// Every catalogued item is a file on disk, so excluding the other location types is a no-op.
	for _, location := range strings.Split(query["excludelocationtypes"], ",") {
		if location != "" && !member("Virtual,Offline,Remote", location) {
			fail(w, 400, "UnsupportedQuery")
			return
		}
	}
	start, limit := 0, 100
	if latest {
		limit = 20
	}
	if resume {
		limit = 24
		query["recursive"] = "true"
	}
	if typesOnly {
		query["recursive"] = "true"
	}
	for key, target := range map[string]*int{"startindex": &start, "limit": &limit} {
		if text, exists := query[key]; exists {
			v, err := strconv.Atoi(text)
			if err != nil || v < 0 || (key == "limit" && v > 1000) {
				fail(w, 400, "InvalidPagination")
				return
			}
			*target = v
		}
	}
	var minPremiere time.Time
	if text, exists := query["minpremieredate"]; exists {
		if minPremiere, err = time.Parse(time.RFC3339, text); err != nil {
			fail(w, 400, "InvalidQuery")
			return
		}
	}
	sortBy, order := strings.ToLower(query["sortby"]), strings.ToLower(query["sortorder"])
	if latest && sortBy == "" {
		sortBy, order = "datecreated", "descending"
	}
	if resume && sortBy == "" {
		sortBy, order = "dateplayed", "descending"
	}
	if sortBy == "" {
		sortBy = "sortname"
	}
	sortKeys := strings.Split(sortBy, ",")
	for _, key := range sortKeys {
		if !member("sortname,name,datecreated,dateplayed,runtime,indexnumber,parentindexnumber,sortindexnumber,sortparentindexnumber,datelastsearched,isfolder,filename,seriessortname,channelnumber,"+
			"totalbitrate,videocodec,container,size,resolution,framerate,playcount,random,seriesdateplayed,datelastcontentadded,"+
			"officialrating,productionyear,premieredate,communityrating,criticrating,director,lastcontentpremieredate", key) {
			fail(w, 400, "UnsupportedSort")
			return
		}
	}
	// SortOrder may give one order per sort key; the last one applies to
	// the keys after it.
	orders := strings.Split(order, ",")
	for _, o := range orders {
		if o != "" && o != "ascending" && o != "descending" {
			fail(w, 400, "UnsupportedSort")
			return
		}
	}
	snapshot := s.store.Snapshot()
	serverID := snapshot.ServerID
	now := time.Now()
	// Continue Watching also offers each started series' next episode, dated
	// by when the series was last watched. The reference Emby includes them
	// without IncludeNextUp; only IncludeNextUp=false leaves them out.
	nextUp := map[string]time.Time{}
	if resume && query["includenextup"] != "false" {
		for _, c := range s.nextUpEpisodes(snapshot.User.Items, "", "") {
			if !snapshot.User.Items[c.next.ID].HiddenFromResume {
				nextUp[c.next.ID] = c.lastPlayed
			}
		}
	}
	lastPlayed := func(id string) time.Time {
		played := snapshot.User.Items[id].LastPlayed
		if series, ok := nextUp[id]; ok && series.After(played) {
			return series
		}
		return played
	}
	// Select and paginate metadata before allocating response DTOs.
	items := []*media.Item{}
	isCollection := func(item *media.Item) bool {
		return catalog != nil && (item.ID == catalog.ID || item.ID == catalog.SeriesLibraryID())
	}
	// collectionType names the library an item belongs to, for CollectionTypes;
	// Couch has only movie and TV libraries (#76).
	collectionType := func(item *media.Item) string {
		if (catalog != nil && item.ID == catalog.ID) || (!isCollection(item) && item.Type() == "Movie") {
			return "movies"
		}
		return "tvshows"
	}
	if catalog != nil {
		parent := query["parentid"]
		root := parent == "" || parent == "root"
		if root && query["recursive"] != "true" && !latest && query["ids"] == "" {
			items = append(items, &media.Item{ID: catalog.ID, Name: "Movies"})
			if len(catalog.Folders) > 0 {
				items = append(items, &media.Item{ID: catalog.SeriesLibraryID(), Name: "TV Shows"})
			}
		} else {
			items = make([]*media.Item, 0, len(catalog.Items)+1)
			for _, candidates := range [][]media.Item{catalog.Items, catalog.Folders} {
				for i := range candidates {
					item := &candidates[i]
					if (latest || resume) && item.IsFolder() {
						continue
					}
					if root || catalog.Parent(*item) == parent || ((query["recursive"] == "true" || latest || resume) && (item.SeriesID == parent || (parent == catalog.SeriesLibraryID() && item.Type() != "Movie"))) {
						items = append(items, item)
					}
				}
			}
			if query["ids"] != "" && root {
				items = append(items, &media.Item{ID: catalog.ID, Name: "Movies"})
				if len(catalog.Folders) > 0 {
					items = append(items, &media.Item{ID: catalog.SeriesLibraryID(), Name: "TV Shows"})
				}
			}
		}
	}
	items = slices.DeleteFunc(items, func(item *media.Item) bool {
		id, kind, folder := item.ID, item.Type(), item.IsFolder() || isCollection(item)
		st := snapshot.User.Items[id]
		info := s.info(id)
		if item.IsFolder() {
			st.Played, _ = s.folderPlayed(*item, snapshot.User.Items)
		}
		if isCollection(item) {
			kind = "CollectionFolder"
		}
		return (query["ids"] != "" && !member(query["ids"], id)) || member(query["excludeitemids"], id) ||
			(query["collectiontypes"] != "" && !member(query["collectiontypes"], collectionType(item))) ||
			(resume && !resumable(st, item.RunTimeTicks) && nextUp[id].IsZero()) ||
			(query["includeitemtypes"] != "" && !member(query["includeitemtypes"], kind)) || member(query["excludeitemtypes"], kind) ||
			(query["mediatypes"] != "" && (folder || !member(query["mediatypes"], "Video"))) ||
			(query["isfolder"] != "" && (query["isfolder"] == "true") != folder) ||
			!matchesSearch(query["searchterm"], item.Name, s.displayName(*item), info.OriginalTitle) ||
			// Undated items never satisfy a premiere bound and are never
			// unaired; Emby likewise leaves them out.
			(!minPremiere.IsZero() && (info.Premiere.IsZero() || info.Premiere.Before(minPremiere))) ||
			(query["isunaired"] != "" && (query["isunaired"] == "true") != info.Premiere.After(now)) ||
			(query["isplayed"] != "" && (query["isplayed"] == "true") != st.Played) ||
			(query["isfavorite"] != "" && (query["isfavorite"] == "true") != st.IsFavorite) ||
			(query["wassearched"] != "" && (query["wassearched"] == "true") == st.LastSearched.IsZero()) ||
			(member(query["filters"], "IsPlayed") && !st.Played) ||
			(member(query["filters"], "IsUnplayed") && st.Played) ||
			(member(query["filters"], "IsFavorite") && !st.IsFavorite) ||
			(member(query["filters"], "IsFolder") && !folder) ||
			(member(query["filters"], "IsNotFolder") && folder) ||
			// Without airing-order metadata, every Season 00 episode is a standalone special.
			(query["isstandalonespecial"] != "" && (query["isstandalonespecial"] == "true") != (kind == "Episode" && item.SeasonNumber == 0)) ||
			// Every catalogued item is a file on disk: none is missing or a
			// virtual unaired episode. Playing an episode sends both as false.
			query["ismissing"] == "true" || query["isvirtualunaired"] == "true" ||
			// The library is read-only and Couch has no playlists or
			// collections, so the user can edit no item (#77).
			query["canedititems"] == "true" ||
			failsMediaFilters(catalog, item, query, info) ||
			// The series page lists specials (Season 00 episodes) in their own row.
			(query["isspecialepisode"] != "" && (query["isspecialepisode"] == "true") != (kind == "Episode" && item.SeasonNumber == 0))
	})
	if typesOnly {
		// Search tabs describe the full filtered result, independent of its page.
		names := []string{}
		for _, item := range items {
			kind := item.Type()
			if isCollection(item) {
				kind = "CollectionFolder"
			}
			if !slices.Contains(names, kind) {
				names = append(names, kind)
			}
		}
		slices.Sort(names)
		result := make([]object, 0, len(names))
		for _, name := range names {
			result = append(result, object{"Name": name})
		}
		respond(w, 200, object{"Items": result, "TotalRecordCount": len(result)})
		return
	}
	// A series or season sorts by its episodes for the newest added and the
	// last watched one.
	newestAdded, seriesPlayed, newestPremiere := map[string]time.Time{}, map[string]time.Time{}, map[string]time.Time{}
	if catalog != nil {
		for _, episode := range catalog.Items {
			played := snapshot.User.Items[episode.ID].LastPlayed
			premiere := s.info(episode.ID).Premiere
			for _, folder := range []string{episode.SeriesID, episode.SeasonID} {
				if folder == "" {
					continue
				}
				if episode.Added.After(newestAdded[folder]) {
					newestAdded[folder] = episode.Added
				}
				if played.After(seriesPlayed[folder]) {
					seriesPlayed[folder] = played
				}
				if premiere.After(newestPremiere[folder]) {
					newestPremiere[folder] = premiere
				}
			}
		}
	}
	director := func(item *media.Item) string {
		if names := s.info(item.ID).Directors(); len(names) > 0 {
			return strings.ToLower(names[0])
		}
		return ""
	}
	added := func(item *media.Item) time.Time {
		if t, ok := newestAdded[item.ID]; ok {
			return t
		}
		return item.Added
	}
	video := func(item *media.Item) media.Stream {
		for _, stream := range item.Streams {
			if stream.Type == "Video" {
				return stream
			}
		}
		return media.Stream{}
	}
	if sortKeys[0] == "random" {
		rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] }) //nolint:gosec // a display order, not a secret
	}
	slices.SortFunc(items, func(a, b *media.Item) int {
		comparison := 0
		// Keys are compared in order, each in its own SortOrder.
		descending := false
		for n, key := range sortKeys {
			descending = orders[min(n, len(orders)-1)] == "descending"
			switch key {
			case "datecreated":
				comparison = a.Added.Compare(b.Added)
			case "dateplayed":
				comparison = lastPlayed(a.ID).Compare(lastPlayed(b.ID))
			case "datelastsearched":
				comparison = snapshot.User.Items[a.ID].LastSearched.Compare(snapshot.User.Items[b.ID].LastSearched)
			case "runtime":
				comparison = cmp.Compare(a.RunTimeTicks, b.RunTimeTicks)
			case "parentindexnumber", "sortparentindexnumber":
				comparison = cmp.Compare(a.SeasonNumber, b.SeasonNumber)
			case "indexnumber", "sortindexnumber":
				comparison = cmp.Compare(a.EpisodeNumber, b.EpisodeNumber)
			case "isfolder":
				// Folders first, as in a file browser.
				comparison = -cmp.Compare(boolRank(a.IsFolder() || isCollection(a)), boolRank(b.IsFolder() || isCollection(b)))
			case "filename":
				comparison = strings.Compare(strings.ToLower(path.Base(a.Path)), strings.ToLower(path.Base(b.Path)))
			case "seriessortname":
				comparison = strings.Compare(strings.ToLower(s.seriesName(*a)), strings.ToLower(s.seriesName(*b)))
			case "officialrating":
				comparison = strings.Compare(s.info(a.ID).OfficialRating, s.info(b.ID).OfficialRating)
			case "productionyear":
				comparison = cmp.Compare(s.info(a.ID).Year, s.info(b.ID).Year)
			case "premieredate":
				comparison = s.info(a.ID).Premiere.Compare(s.info(b.ID).Premiere)
			case "communityrating":
				comparison = cmp.Compare(s.info(a.ID).Community, s.info(b.ID).Community)
			case "criticrating":
				comparison = cmp.Compare(s.info(a.ID).Critic, s.info(b.ID).Critic)
			case "director":
				comparison = strings.Compare(director(a), director(b))
			case "lastcontentpremieredate":
				comparison = newestPremiere[a.ID].Compare(newestPremiere[b.ID])
			case "totalbitrate":
				comparison = cmp.Compare(bitrate(*a), bitrate(*b))
			case "videocodec":
				comparison = strings.Compare(video(a).Codec, video(b).Codec)
			case "container":
				comparison = strings.Compare(strings.ToLower(a.Container), strings.ToLower(b.Container))
			case "size":
				comparison = cmp.Compare(a.Size, b.Size)
			case "resolution":
				comparison = cmp.Compare(video(a).Width*video(a).Height, video(b).Width*video(b).Height)
			case "framerate":
				comparison = cmp.Compare(video(a).AverageFrameRate, video(b).AverageFrameRate)
			case "playcount":
				comparison = cmp.Compare(snapshot.User.Items[a.ID].PlayCount, snapshot.User.Items[b.ID].PlayCount)
			case "seriesdateplayed":
				comparison = seriesPlayed[a.ID].Compare(seriesPlayed[b.ID])
			case "datelastcontentadded":
				comparison = added(a).Compare(added(b))
			case "random":
				// The items were shuffled above; a later key cannot reshuffle.
				if key == sortKeys[0] {
					return 0
				}
			case "channelnumber":
				// Couch has no channels: every item compares equal and the
				// next key decides (#79).
				comparison = 0
			default:
				comparison = strings.Compare(s.sortKey(*a), s.sortKey(*b))
			}
			if comparison != 0 {
				break
			}
		}
		// Ties fall back to the ID, in the order of the last key.
		if comparison == 0 {
			comparison = strings.Compare(a.ID, b.ID)
		}
		if descending {
			return -comparison
		}
		return comparison
	})
	total := len(items)
	start = min(start, total)
	items = items[start : start+min(limit, total-start)]
	result := make([]object, 0, len(items))
	for _, item := range items {
		if isCollection(item) {
			result = append(result, s.collectionDTO(item.ID, serverID, false))
		} else {
			result = append(result, s.movieDTO(*item, serverID, snapshot.User.Items, token))
		}
	}
	if latest {
		respond(w, 200, result)
	} else {
		respond(w, 200, object{"Items": result, "TotalRecordCount": total})
	}
}

// matchesSearch reports whether any of names contains term, ignoring case.
func matchesSearch(term string, names ...string) bool {
	term = strings.ToLower(term)
	for _, name := range names {
		if name != "" && strings.Contains(strings.ToLower(name), term) {
			return true
		}
	}
	return term == ""
}
