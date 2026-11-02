package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// playlistsViewID is the Playlists library. Like Emby, it is listed in My
// Media once the user has a playlist, and holds that user's playlists.
const playlistsViewID = "playlists"

// playlistView is a playlist as its owner sees it: an entry whose item is not
// in the owner's catalogue is left out, and comes back with its path.
type playlistView struct {
	state.Playlist
	entries []playlistEntry
}

type playlistEntry struct {
	id   string
	item *media.Item
}

// item is the playlist as a catalogue entry, for listing and sorting.
func (p playlistView) item() *media.Item {
	return &media.Item{ID: p.ID, Name: p.Name, Added: p.Added, Modified: p.Modified, Kind: "Playlist"}
}

// playlists returns the user's playlists, by ID. Playlists are private: no
// other user, administrators included, sees them.
func playlists(catalog *media.Catalog, user state.User, d state.Data) map[string]playlistView {
	lists := map[string]playlistView{}
	for _, p := range d.Playlists {
		if p.UserID != user.ID {
			continue
		}
		view := playlistView{Playlist: p}
		for _, e := range p.Entries {
			if catalog == nil {
				break
			}
			if item, found := catalog.Lookup(e.ItemID); found {
				view.entries = append(view.entries, playlistEntry{id: e.ID, item: item})
			}
		}
		lists[p.ID] = view
	}
	return lists
}

// sessionPlaylists is playlists for the session's user.
func (s *Server) sessionPlaylists(session state.Session) map[string]playlistView {
	snapshot := s.store.Snapshot()
	return playlists(s.catalogFor(session), userOf(snapshot, session), snapshot)
}

// videos lists the playlist's videos once each, which its played state and
// "Mark played" cover.
func (p playlistView) videos() []media.Item {
	videos := []media.Item{}
	seen := map[string]bool{}
	for _, e := range p.entries {
		if !seen[e.item.ID] {
			seen[e.item.ID] = true
			videos = append(videos, *e.item)
		}
	}
	return videos
}

func playlistUserData(p playlistView, items map[string]state.ItemState) object {
	unplayed := 0
	videos := p.videos()
	for _, video := range videos {
		if !items[video.ID].Played {
			unplayed++
		}
	}
	return object{"UnplayedItemCount": unplayed, "PlaybackPositionTicks": 0, "PlayCount": 0,
		"IsFavorite": items[p.ID].IsFavorite, "Played": len(videos) > 0 && unplayed == 0}
}

// playlistDTO is the playlist as a Playlist. Its owner may reorder, rename
// and delete it, which Emby Web decides by CanEditItems and CanDelete.
func playlistDTO(p playlistView, serverID string, items map[string]state.ItemState) object {
	dto := baseFields(p.ID, p.Name, "Playlist", serverID)
	dto["DateCreated"] = embyDate(p.Added)
	dto["DateModified"] = embyDate(p.Modified)
	dto["IsFolder"] = true
	dto["MediaType"] = "Video"
	dto["ParentId"] = playlistsViewID
	dto["ChildCount"] = len(p.entries)
	var ticks int64
	for _, e := range p.entries {
		ticks += e.item.RunTimeTicks
	}
	dto["RunTimeTicks"] = ticks
	dto["CanEditItems"], dto["CanDelete"] = true, true
	dto["UserData"] = playlistUserData(p, items)
	return dto
}

// playlistsViewDTO is the Playlists library, a CollectionFolder of type
// playlists; Emby Web opens it as a plain list of the user's playlists.
func playlistsViewDTO(serverID string, count int, detail bool) object {
	dto := baseFields(playlistsViewID, "Playlists", "CollectionFolder", serverID)
	dto["DateCreated"] = zeroDate
	dto["IsFolder"] = true
	dto["ParentId"] = rootID
	dto["CollectionType"] = "playlists"
	dto["UserData"] = folderUserData()
	dto["PrimaryImageAspectRatio"] = 1.7777777777777777
	if detail {
		dto["ChildCount"] = count
		dto["Subviews"] = []string{}
	}
	return dto
}

// ownsPlaylist reports whether the request's {item} is a playlist of the
// session's user.
func (s *Server) ownsPlaylist(r *http.Request, session state.Session) bool {
	_, ok := s.store.Snapshot().UserPlaylist(session.UserID, r.PathValue("item"))
	return ok
}

// ownerOrAdmin lets the owner of the playlist {item} through, and otherwise
// only an administrator: the item menu offers a playlist's owner the editors
// it offers an administrator for other items.
func (s *Server) ownerOrAdmin(next authenticated) http.HandlerFunc {
	return s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if !s.ownsPlaylist(r, session) && !userOf(s.store.Snapshot(), session).Policy.IsAdministrator {
			fail(w, 403, "Forbidden")
			return
		}
		next(w, r, token, session)
	})
}

// renamePlaylist saves the name from Emby Web's metadata editor, which sends
// the whole item; the other fields are not stored for a playlist.
func (s *Server) renamePlaylist(w http.ResponseWriter, r *http.Request, session state.Session, id string) {
	var body struct{ Name string }
	if decodeJSON(w, r, &body) != nil {
		fail(w, 400, "InvalidRequest")
		return
	}
	s.playlistChanged(w, s.store.RenamePlaylist(session.UserID, id, body.Name))
}

func (s *Server) playlistChanged(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, state.ErrPlaylistNotFound):
		fail(w, 404, "NotFound")
	case errors.Is(err, state.ErrPlaylistName):
		failText(w, 400, "A playlist name must contain 1–128 bytes.")
	default:
		s.changed(w, err)
	}
}

// splitIDs splits comma-separated IDs, keeping order and repeats.
func splitIDs(list []string) []string {
	ids := []string{}
	for _, v := range list {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// playlistMedia resolves the IDs of a request to the videos they add, as Emby
// does: a series or season adds its episodes. On failure the response has
// been written.
func (s *Server) playlistMedia(w http.ResponseWriter, session state.Session, ids []string) ([]string, bool) {
	catalog := s.catalogFor(session)
	videos := []string{}
	for _, id := range ids {
		if catalog == nil {
			failText(w, 400, "Only movies, series, seasons and episodes can be added to a playlist.")
			return nil, false
		}
		item, found := catalog.Lookup(id)
		if !found {
			failText(w, 400, "Only movies, series, seasons and episodes can be added to a playlist.")
			return nil, false
		}
		if !item.IsFolder() {
			videos = append(videos, item.ID)
			continue
		}
		for _, episode := range catalog.Episodes(item.ID) {
			videos = append(videos, episode.ID)
		}
	}
	return videos, true
}

func (s *Server) playlistRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /playlists", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if t := value(r, "MediaType"); t != "" && !strings.EqualFold(t, "Video") {
			failText(w, 400, "Couch has only video playlists.")
			return
		}
		videos, ok := s.playlistMedia(w, session, splitIDs(values(r, "Ids")))
		if !ok {
			return
		}
		p, err := s.store.CreatePlaylist(session.UserID, value(r, "Name"), videos)
		if err != nil {
			s.playlistChanged(w, err)
			return
		}
		respond(w, 200, object{"Id": p.ID, "Name": p.Name, "ItemAddedCount": len(p.Entries)})
	}))
	// Emby Web sends the IDs and SkipDuplicates as a JSON body; the API also
	// takes Ids in the query.
	mux.HandleFunc("POST /playlists/{id}/items", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		list, skip := values(r, "Ids"), value(r, "SkipDuplicates")
		if r.Header.Get("Content-Type") != "" {
			fields, ok := readFields(w, r)
			if !ok {
				return
			}
			list = append(list, fields["ids"])
			if fields["skipduplicates"] != "" {
				skip = fields["skipduplicates"]
			}
		}
		ids := splitIDs(list)
		if len(ids) == 0 {
			fail(w, 400, "InvalidRequest")
			return
		}
		videos, ok := s.playlistMedia(w, session, ids)
		if !ok {
			return
		}
		added, err := s.store.AddToPlaylist(session.UserID, r.PathValue("id"), videos, strings.EqualFold(skip, "true"))
		if err != nil {
			s.playlistChanged(w, err)
			return
		}
		respond(w, 200, object{"Id": r.PathValue("id"), "ItemAddedCount": added})
	}))
	// Before adding, Emby Web asks whether the playlist already holds any of
	// the items, and if so offers to add them again or skip them.
	mux.HandleFunc("GET /playlists/{id}/addtoplaylistinfo", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		p, ok := s.store.Snapshot().UserPlaylist(session.UserID, r.PathValue("id"))
		if !ok {
			fail(w, 404, "NotFound")
			return
		}
		videos, ok := s.playlistMedia(w, session, splitIDs(values(r, "Ids")))
		if !ok {
			return
		}
		held := map[string]bool{}
		for _, e := range p.Entries {
			held[e.ItemID] = true
		}
		duplicates := 0
		for _, id := range videos {
			if held[id] {
				duplicates++
			}
		}
		count := len(videos)
		if duplicates > 0 {
			count = duplicates
		}
		respond(w, 200, object{"ItemCount": count, "ContainsDuplicates": duplicates > 0})
	}))
	mux.HandleFunc("GET /playlists/{id}/items", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if _, ok := s.sessionPlaylists(session)[r.PathValue("id")]; !ok {
			fail(w, 404, "NotFound")
			return
		}
		query := url.Values{}
		for key, vv := range r.URL.Query() {
			if !strings.EqualFold(key, "ParentId") && !strings.EqualFold(key, "SortBy") {
				query[key] = vv
			}
		}
		query.Set("ParentId", r.PathValue("id"))
		query.Set("SortBy", "ListItemOrder")
		r = r.Clone(r.Context())
		r.URL.RawQuery = query.Encode()
		s.listItems(w, r, session, false, false)
	}))
	remove := s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		entries := splitIDs(values(r, "EntryIds"))
		if len(entries) == 0 {
			fail(w, 400, "InvalidRequest")
			return
		}
		s.playlistChanged(w, s.store.RemoveFromPlaylist(session.UserID, r.PathValue("id"), entries))
	})
	mux.HandleFunc("POST /playlists/{id}/items/delete", remove)
	mux.HandleFunc("DELETE /playlists/{id}/items", remove)
	mux.HandleFunc("POST /playlists/{id}/items/{entry}/move/{index}", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		index, err := strconv.Atoi(r.PathValue("index"))
		if err != nil || index < 0 {
			fail(w, 400, "InvalidRequest")
			return
		}
		s.playlistChanged(w, s.store.MovePlaylistEntry(session.UserID, r.PathValue("id"), r.PathValue("entry"), index))
	}))
}

// setPlaylistFlag marks a playlist favorite, or marks its videos played.
func (s *Server) setPlaylistFlag(w http.ResponseWriter, token string, session state.Session, p playlistView, favorite, value bool) {
	if favorite {
		if err := s.store.SetItem(token, p.ID, func(st *state.ItemState) { st.IsFavorite = value }); err != nil {
			s.changed(w, err)
			return
		}
	} else if err := s.setEpisodesPlayed(token, session.UserID, p.videos(), value); err != nil {
		s.changed(w, err)
		return
	}
	data := playlistUserData(p, userOf(s.store.Snapshot(), session).Items)
	data["ItemId"] = p.ID
	respond(w, 200, data)
}
