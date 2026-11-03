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
	"unicode"
	"unicode/utf8"

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

// wantsPeople reports whether a list request asks for its items' People.
// Emby lists carry them only when Fields names them, as Emby Web does where
// it shows them; a list without Fields keeps them.
func wantsPeople(r *http.Request) bool {
	fields := values(r, "Fields")
	return len(fields) == 0 || slices.ContainsFunc(fields, func(f string) bool { return member(f, "People") })
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
		for _, id := range libraryIDs(s.catalogFor(session)) {
			items = append(items, s.collectionDTO(id, serverID, false))
		}
		if sets := s.sessionBoxSets(session); len(sets) > 0 {
			items = append(items, collectionsViewDTO(serverID, len(sets), false))
		}
		if lists := s.sessionPlaylists(session); len(lists) > 0 {
			items = append(items, playlistsViewDTO(serverID, len(lists), false))
		}
		respond(w, 200, object{"Items": items, "TotalRecordCount": len(items)})
	}))
	for _, path := range []string{"/items/root", "/users/{user}/items/root"} {
		mux.HandleFunc("GET "+path, s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			views := len(libraryIDs(s.catalogFor(session)))
			if len(s.sessionBoxSets(session)) > 0 {
				views++
			}
			if len(s.sessionPlaylists(session)) > 0 {
				views++
			}
			respond(w, 200, rootFolderDTO(s.store.Snapshot().ServerID, views))
		}))
	}
	for _, path := range []string{"/items/{item}", "/users/{user}/items/{item}"} {
		mux.HandleFunc("GET "+path, s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			id := r.PathValue("item")
			snapshot := s.store.Snapshot()
			sets := s.sessionBoxSets(session)
			if set, ok := sets[id]; ok {
				respond(w, 200, s.boxSetDTO(s.catalogFor(session), set, snapshot.ServerID, userOf(snapshot, session).Items, userOf(snapshot, session).Policy.IsAdministrator, token))
				return
			}
			if id == collectionsViewID && len(sets) > 0 {
				respond(w, 200, collectionsViewDTO(snapshot.ServerID, len(sets), true))
				return
			}
			lists := s.sessionPlaylists(session)
			if p, ok := lists[id]; ok {
				respond(w, 200, s.playlistDTO(s.catalogFor(session), p, snapshot.ServerID, userOf(snapshot, session).Items, token))
				return
			}
			if id == playlistsViewID && len(lists) > 0 {
				respond(w, 200, playlistsViewDTO(snapshot.ServerID, len(lists), true))
				return
			}
			// A rescan replaces the catalog, so each request reads it (#75).
			if catalog := s.catalogFor(session); catalog != nil {
				if _, ok := catalog.Library(id); ok {
					respond(w, 200, s.collectionDTO(id, snapshot.ServerID, true))
					return
				}
				if item, found := findItem(catalog, id); found {
					respond(w, 200, s.movieDTO(item, snapshot.ServerID, userOf(snapshot, session).Items, token, true))
					return
				}
				if e, found := s.entityIn(catalog, id); found {
					dto := entityDTO(id, e, snapshot.ServerID)
					switch e.Type {
					case "MusicArtist":
						dto["UserData"] = itemUserData(userOf(snapshot, session).Items[id], 0)
					case "Person":
						s.addPerson(dto, id, token)
					}
					respond(w, 200, dto)
					return
				}
			}
			fail(w, 404, "NotFound")
		}))
	}
	for _, path := range []string{"/items", "/users/{user}/items", "/users/{user}/items/latest"} {
		latest := strings.HasSuffix(path, "/latest")
		mux.HandleFunc("GET "+path, s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			s.listItems(w, r, session, latest, false)
		}))
	}
	mux.HandleFunc("POST /users/{user}/searcheditems", s.protect(s.reportSearched))
	// The "Clear" button on the search page; Emby Web sends no body.
	mux.HandleFunc("POST /users/{user}/recentlysearched/delete", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		searched := []string{}
		for id, st := range userOf(s.store.Snapshot(), session).Items {
			if !st.LastSearched.IsZero() {
				searched = append(searched, id)
			}
		}
		_, err := s.store.SetItems(token, searched, func(_ string, st *state.ItemState) { st.LastSearched = time.Time{} })
		s.changed(w, err)
	}))
	mux.HandleFunc("GET /persons/{item}/credits", s.protect(s.personCredits))
	s.extrasRoutes(mux)
	s.seriesRoutes(mux)
	s.imageRoutes(mux)
	s.userImageRoutes(mux)
	for _, path := range []string{"/itemtypes", "/items/prefixes"} {
		mux.HandleFunc("GET "+path, s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			s.listItems(w, r, session, false, false)
		}))
	}
}

// extrasRoutes answers the companion requests Emby Web makes on an item page.
// Couch has none of this content, so each returns an empty result in the shape
// the reference server uses; a 404 here leaves the page in an error state.
func (s *Server) extrasRoutes(mux *http.ServeMux) {
	known := func(r *http.Request, session state.Session) bool {
		id := r.PathValue("item")
		catalog := s.catalogFor(session)
		_, found := findItem(catalog, id)
		_, isEntity := s.entityIn(catalog, id)
		sets := s.sessionBoxSets(session)
		_, isBoxSet := sets[id]
		lists := s.sessionPlaylists(session)
		_, isPlaylist := lists[id]
		return found || isEntity || isBoxSet || isPlaylist || (id == collectionsViewID && len(sets) > 0) || (id == playlistsViewID && len(lists) > 0) ||
			slices.Contains(libraryIDs(catalog), id)
	}
	emptyPage := func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !known(r, session) {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, object{"Items": []any{}, "TotalRecordCount": 0})
	}
	emptyArray := func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !known(r, session) {
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
		if !known(r, session) {
			fail(w, 404, "NotFound")
			return
		}
		empty := object{"Items": []any{}, "TotalRecordCount": 0, "OwnerId": r.PathValue("item")}
		respond(w, 200, object{"ThemeVideosResult": empty, "ThemeSongsResult": empty, "SoundtrackSongsResult": empty})
	}))
	// Couch generates no seek-bar previews. Emby Web's player re-requests the
	// set after every failure, so an empty set is answered instead of 404.
	mux.HandleFunc("GET /items/{item}/thumbnailset", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !known(r, session) {
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
		if _, found := findItem(s.catalogFor(session), id); !found {
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

func (s *Server) listItems(w http.ResponseWriter, r *http.Request, session state.Session, latest, resume bool) {
	s.listItemsIn(w, r, session, s.catalogFor(session), latest, resume)
}

// supportedSorts lists the SortBy keys listItemsIn accepts, in lower case.
const supportedSorts = "sortname,name,datecreated,dateplayed,runtime,indexnumber,parentindexnumber,sortindexnumber,sortparentindexnumber,datelastsearched,isfolder,filename,seriesname,seriessortname,channelnumber," +
	"totalbitrate,videocodec,container,size,resolution,framerate,playcount,random,seriesdateplayed,datelastcontentadded," +
	"officialrating,productionyear,premieredate,communityrating,criticrating,director,lastcontentpremieredate,displayorder,listitemorder,datemodified," +
	"album,albumartist,artist,bitrate,composer"

// listItemsIn answers an Items query over catalog, which is the session's
// catalogue or part of it.
func (s *Server) listItemsIn(w http.ResponseWriter, r *http.Request, session state.Session, catalog *media.Catalog, latest, resume bool) {
	token, _ := requestToken(r) // Callers have already authenticated this request.
	typesOnly := r.URL.Path == "/itemtypes"
	prefixesOnly := r.URL.Path == "/items/prefixes"
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
			"genres", "studios", "tags", "officialratings", "years", "genreids", "studioids", "tagids", "personids", "persontypes",
			"namestartswithorgreater", "groupitemsintocollections", "projecttomedia",
			"artistids", "albumartistids", "contributingartistids", "composerartistids", "albumids":
		default:
			if !strings.HasPrefix(key, "x-emby-") {
				fail(w, 400, "UnsupportedQuery")
				return
			}
		}
	}
	for _, key := range []string{"recursive", "isfolder", "isplayed", "isfavorite", "groupprogramsbyseries", "includesearchtypes", "isstandalonespecial", "collapseboxsetitems", "wassearched", "isunaired", "includenextup", "isspecialepisode", "ismissing", "isvirtualunaired", "canedititems", "groupitemsintocollections", "projecttomedia"} {
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
	// A page holds at most 1000 items: a larger Limit, as Emby Web's "play
	// from here" on a song asks for, is reduced, not rejected.
	for key, target := range map[string]*int{"startindex": &start, "limit": &limit} {
		if text, exists := query[key]; exists {
			v, err := strconv.Atoi(text)
			if err != nil || v < 0 {
				fail(w, 400, "InvalidPagination")
				return
			}
			*target = v
		}
	}
	limit = min(limit, 1000)
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
		if !member(supportedSorts, key) {
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
		for _, c := range s.nextUpEpisodes(catalog, userOf(snapshot, session).Items, "", "") {
			if !userOf(snapshot, session).Items[c.next.ID].HiddenFromResume {
				nextUp[c.next.ID] = c.lastPlayed
			}
		}
	}
	lastPlayed := func(id string) time.Time {
		played := userOf(snapshot, session).Items[id].LastPlayed
		if series, ok := nextUp[id]; ok && series.After(played) {
			return series
		}
		return played
	}
	// Select and paginate metadata before allocating response DTOs.
	items := []*media.Item{}
	user := userOf(snapshot, session)
	sets := boxSets(catalog, user, snapshot)
	view := &media.Item{ID: collectionsViewID, Name: "Collections", Kind: "CollectionFolder"}
	isBoxSet := func(item *media.Item) bool { return item.Kind == "BoxSet" }
	lists := playlists(catalog, user, snapshot)
	listsView := &media.Item{ID: playlistsViewID, Name: "Playlists", Kind: "CollectionFolder"}
	isPlaylist := func(item *media.Item) bool { return item.Kind == "Playlist" }
	// In a playlist's listing each item is one entry: entryOf holds its
	// PlaylistItemId and listOrder its position.
	entryOf, listOrder := map[*media.Item]string{}, map[*media.Item]int{}
	// isCollection reports a library folder, including the Collections and
	// Playlists ones.
	isCollection := func(item *media.Item) bool {
		_, ok := catalog.Library(item.ID)
		return (catalog != nil && ok) || item == view || item == listsView
	}
	// collectionType names the library an item belongs to, for CollectionTypes
	// (#76).
	collectionType := func(item *media.Item) string {
		if library, ok := catalog.Library(item.ID); ok {
			return library.Type
		}
		if item == view || isBoxSet(item) {
			return "boxsets"
		}
		if item == listsView || isPlaylist(item) {
			return "playlists"
		}
		switch item.Type() {
		case "Movie":
			return media.Movies
		case "Audio", "MusicAlbum":
			return media.Music
		case "Photo", "Video", "PhotoAlbum":
			return media.HomeVideos
		}
		return media.TVShows
	}
	libraries := func() []*media.Item {
		folders := []*media.Item{}
		for _, library := range catalog.Libraries() {
			folders = append(folders, &media.Item{ID: library.ID, Name: library.Name})
		}
		if len(sets) > 0 {
			folders = append(folders, view)
		}
		if len(lists) > 0 {
			folders = append(folders, listsView)
		}
		return folders
	}
	// grouped holds the collections that stand in for their members when a
	// listing groups items into collections; they pass the type filter.
	grouped := map[string]bool{}
	if catalog != nil {
		parent := query["parentid"]
		root := parent == "" || parent == "root"
		set, inSet := sets[parent]
		list, inList := lists[parent]
		switch {
		case parent == collectionsViewID:
			for _, set := range sets {
				items = append(items, set.item())
			}
		case parent == playlistsViewID:
			for _, list := range lists {
				items = append(items, list.item())
			}
		case inList:
			for n, e := range list.entries {
				entry := *e.item
				entryOf[&entry], listOrder[&entry] = e.id, n
				items = append(items, &entry)
			}
		case inSet:
			items = append(items, set.members...)
			// Playing a collection asks for its media: a series or season
			// stands for its episodes.
			if query["projecttomedia"] == "true" {
				items = items[:0]
				for _, member := range set.members {
					if !member.IsFolder() {
						items = append(items, member)
						continue
					}
					for _, episode := range catalog.Episodes(member.ID) {
						if found, ok := catalog.Lookup(episode.ID); ok {
							items = append(items, found)
						}
					}
				}
			}
		case root && query["recursive"] != "true" && !latest && query["ids"] == "":
			items = append(items, libraries()...)
		default:
			items = make([]*media.Item, 0, len(catalog.Items)+1)
			// Latest lists the albums of new tracks, as Emby groups them.
			latestAlbums := map[string]bool{}
			for _, candidates := range [][]media.Item{catalog.Items, catalog.Folders} {
				for i := range candidates {
					item := &candidates[i]
					if (latest || resume) && item.IsFolder() {
						continue
					}
					if root || catalog.Parent(*item) == parent || ((query["recursive"] == "true" || latest || resume) && (item.SeriesID == parent || catalog.LibraryOf(*item) == parent || catalog.Within(*item, parent))) {
						if album, ok := catalog.Lookup(item.ParentID); latest && ok && album.Type() == "MusicAlbum" {
							if !latestAlbums[album.ID] {
								latestAlbums[album.ID] = true
								items = append(items, album)
							}
							continue
						}
						items = append(items, item)
					}
				}
			}
			if query["ids"] != "" && root {
				items = append(items, libraries()...)
			}
			// Collections join a listing that asks for them by type, by item
			// or by ID, as Emby Web's tabs, pickers and item pages do: those
			// with an item in the parent library, or every one from the root.
			wanted := member(query["includeitemtypes"], "BoxSet") || query["listitemids"] != ""
			for id := range sets {
				wanted = wanted || member(query["ids"], id)
			}
			if wanted && !latest && !resume {
				for _, set := range sets {
					if root || slices.ContainsFunc(set.members, func(m *media.Item) bool { return catalog.LibraryOf(*m) == parent }) {
						items = append(items, set.item())
					}
				}
			}
			// Playlists join a listing that asks for them by type or by ID, as
			// the add-to-playlist dialog and the navigation menu do.
			for _, list := range lists {
				if !latest && !resume && (member(query["includeitemtypes"], "Playlist") || member(query["ids"], list.ID)) &&
					(root || slices.ContainsFunc(list.entries, func(e playlistEntry) bool { return catalog.LibraryOf(*e.item) == parent })) {
					items = append(items, list.item())
				}
			}
			if query["groupitemsintocollections"] == "true" && !latest && !resume {
				items = groupIntoCollections(items, sets, grouped)
			}
		}
	}
	items = slices.DeleteFunc(items, func(item *media.Item) bool {
		id, kind, folder := item.ID, item.Type(), item.IsFolder() || isCollection(item) || isBoxSet(item) || isPlaylist(item)
		st := user.Items[id]
		info := s.info(id)
		if item.IsFolder() {
			st.Played, _ = s.folderPlayed(*item, user.Items)
		}
		if isBoxSet(item) {
			st.Played = s.boxSetUserData(sets[id], user.Items)["Played"].(bool)
		}
		if isPlaylist(item) {
			st.Played = playlistUserData(lists[id], user.Items)["Played"].(bool)
		}
		if isCollection(item) {
			kind = "CollectionFolder"
		}
		// ListItemIds asks for the collections and playlists that hold an item.
		holds := func(ids string) bool {
			if isPlaylist(item) {
				return slices.ContainsFunc(lists[id].entries, func(e playlistEntry) bool { return member(ids, e.item.ID) })
			}
			return slices.ContainsFunc(sets[id].members, func(m *media.Item) bool { return member(ids, m.ID) })
		}
		return (query["ids"] != "" && !member(query["ids"], id)) || member(query["excludeitemids"], id) ||
			(query["collectiontypes"] != "" && !member(query["collectiontypes"], collectionType(item))) ||
			(resume && !resumable(st, item.RunTimeTicks) && nextUp[id].IsZero()) ||
			(query["includeitemtypes"] != "" && !member(query["includeitemtypes"], kind) && !grouped[id]) || member(query["excludeitemtypes"], kind) ||
			(query["listitemids"] != "" && ((!isBoxSet(item) && !isPlaylist(item)) || !holds(query["listitemids"]))) ||
			(query["namestartswithorgreater"] != "" && strings.ToLower(s.sortName(*item)) < strings.ToLower(query["namestartswithorgreater"])) ||
			(query["mediatypes"] != "" && (folder || !member(query["mediatypes"], mediaType(item)))) ||
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
			// Only an administrator edits items, and no one a library (#77);
			// every user edits their own playlists.
			(query["canedititems"] == "true" && !isPlaylist(item) && (!user.Policy.IsAdministrator || isCollection(item))) ||
			failsMediaFilters(catalog, item, query, info) || failsMusicFilters(catalog, item, query) ||
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
	if prefixesOnly {
		// The A–Z jump bar offers the first letter of each sort name in the
		// filtered result; picking one sends NameStartsWithOrGreater (#124).
		letters := []string{}
		for _, item := range items {
			first, _ := utf8.DecodeRuneInString(s.sortName(*item))
			if first == utf8.RuneError {
				continue
			}
			if letter := string(unicode.ToUpper(first)); !slices.Contains(letters, letter) {
				letters = append(letters, letter)
			}
		}
		slices.SortFunc(letters, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
		result := make([]object, 0, len(letters))
		for _, letter := range letters {
			result = append(result, object{"Name": letter})
		}
		respond(w, 200, result)
		return
	}
	// A series or season sorts by its episodes for the newest added and the
	// last watched one.
	newestAdded, seriesPlayed, newestPremiere := map[string]time.Time{}, map[string]time.Time{}, map[string]time.Time{}
	if catalog != nil {
		for _, episode := range catalog.Items {
			played := userOf(snapshot, session).Items[episode.ID].LastPlayed
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
				comparison = userOf(snapshot, session).Items[a.ID].LastSearched.Compare(userOf(snapshot, session).Items[b.ID].LastSearched)
			case "runtime":
				comparison = cmp.Compare(a.RunTimeTicks, b.RunTimeTicks)
			case "parentindexnumber", "sortparentindexnumber":
				comparison = cmp.Compare(parentIndexNumber(a), parentIndexNumber(b))
			case "indexnumber", "sortindexnumber":
				comparison = cmp.Compare(indexNumber(a), indexNumber(b))
			case "album":
				comparison = strings.Compare(strings.ToLower(albumName(catalog, a)), strings.ToLower(albumName(catalog, b)))
			case "albumartist":
				comparison = strings.Compare(strings.ToLower(albumArtist(catalog, *a)), strings.ToLower(albumArtist(catalog, *b)))
			case "artist":
				comparison = strings.Compare(strings.ToLower(firstOf(artistsOf(catalog, *a, "Artist"))), strings.ToLower(firstOf(artistsOf(catalog, *b, "Artist"))))
			case "isfolder":
				// Folders first, as in a file browser.
				comparison = -cmp.Compare(boolRank(a.IsFolder() || isCollection(a) || isBoxSet(a) || isPlaylist(a)), boolRank(b.IsFolder() || isCollection(b) || isBoxSet(b) || isPlaylist(b)))
			case "filename":
				comparison = strings.Compare(strings.ToLower(path.Base(a.Path)), strings.ToLower(path.Base(b.Path)))
			case "seriesname":
				comparison = strings.Compare(strings.ToLower(s.seriesName(*a)), strings.ToLower(s.seriesName(*b)))
			case "seriessortname":
				comparison = strings.Compare(strings.ToLower(s.seriesSortName(*a)), strings.ToLower(s.seriesSortName(*b)))
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
			case "displayorder":
				comparison = s.displayOrder(a, b)
			case "listitemorder":
				comparison = cmp.Compare(listOrder[a], listOrder[b])
			case "datemodified":
				comparison = a.Modified.Compare(b.Modified)
			case "lastcontentpremieredate":
				comparison = newestPremiere[a.ID].Compare(newestPremiere[b.ID])
			case "totalbitrate", "bitrate":
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
				comparison = cmp.Compare(userOf(snapshot, session).Items[a.ID].PlayCount, userOf(snapshot, session).Items[b.ID].PlayCount)
			case "seriesdateplayed":
				comparison = seriesPlayed[a.ID].Compare(seriesPlayed[b.ID])
			case "datelastcontentadded":
				comparison = added(a).Compare(added(b))
			case "random":
				// The items were shuffled above; a later key cannot reshuffle.
				if key == sortKeys[0] {
					return 0
				}
			case "channelnumber", "composer":
				// Couch has no channels and reads no composer tag: every item
				// compares equal and the next key decides (#79).
				comparison = 0
			default:
				comparison = strings.Compare(s.sortKey(*a), s.sortKey(*b))
			}
			if comparison != 0 {
				break
			}
		}
		// Ties fall back to the playlist order, then the ID, in the order of
		// the last key.
		if comparison == 0 {
			comparison = cmp.Compare(listOrder[a], listOrder[b])
		}
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
	people := wantsPeople(r)
	result := make([]object, 0, len(items))
	for _, item := range items {
		switch {
		case item == view:
			result = append(result, collectionsViewDTO(serverID, len(sets), false))
		case item == listsView:
			result = append(result, playlistsViewDTO(serverID, len(lists), false))
		case isPlaylist(item):
			result = append(result, s.playlistDTO(catalog, lists[item.ID], serverID, user.Items, token))
		case isBoxSet(item):
			result = append(result, s.boxSetDTO(catalog, sets[item.ID], serverID, user.Items, user.Policy.IsAdministrator, token))
		case isCollection(item):
			result = append(result, s.collectionDTO(item.ID, serverID, false))
		default:
			dto := s.movieDTO(*item, serverID, userOf(snapshot, session).Items, token, people)
			if entry, ok := entryOf[item]; ok {
				dto["PlaylistItemId"] = entry
			}
			result = append(result, dto)
		}
	}
	if latest {
		respond(w, 200, result)
	} else {
		respond(w, 200, object{"Items": result, "TotalRecordCount": total})
	}
}

// mediaType is the Emby media type of a video, track or photo.
func mediaType(item *media.Item) string {
	switch item.Type() {
	case "Audio":
		return "Audio"
	case "Photo":
		return "Photo"
	}
	return "Video"
}

// indexNumber is an episode's or track's number, parentIndexNumber its
// season's or disc's.
func indexNumber(item *media.Item) int {
	if item.Type() == "Audio" {
		return item.Tags.Track
	}
	return item.EpisodeNumber
}

func parentIndexNumber(item *media.Item) int {
	if item.Type() == "Audio" {
		return item.Tags.Disc
	}
	return item.SeasonNumber
}

// albumName is an album's name, or a track's album's.
func albumName(catalog *media.Catalog, item *media.Item) string {
	if item.Type() == "MusicAlbum" {
		return item.Name
	}
	if album, ok := catalog.Folder(item.ParentID); ok && item.Type() == "Audio" {
		return album.Name
	}
	return ""
}

func firstOf(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
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

// groupIntoCollections replaces the items that belong to a collection by that
// collection, as Emby's "Group into collections" view option does, and records
// the collections it added in grouped.
func groupIntoCollections(items []*media.Item, sets map[string]boxSet, grouped map[string]bool) []*media.Item {
	listed := map[string]bool{}
	for _, item := range items {
		listed[item.ID] = true
	}
	hidden := map[*media.Item]bool{}
	for id, set := range sets {
		if listed[id] {
			continue
		}
		for _, m := range set.members {
			if listed[m.ID] {
				hidden[m] = true
				grouped[id] = true
			}
		}
		if grouped[id] {
			items = append(items, set.item())
		}
	}
	return slices.DeleteFunc(items, func(item *media.Item) bool { return hidden[item] })
}
