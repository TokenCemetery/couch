package server

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// collectionsViewID is the Collections library. Like Emby, it is listed in
// My Media once a collection exists, and holds the collections themselves.
const collectionsViewID = "collections"

// boxSet is a collection as one user sees it: members are the items of its
// list in that user's catalogue, in list order.
type boxSet struct {
	state.Collection
	members []*media.Item
}

// item is the collection as a catalogue entry, for listing and sorting.
func (b boxSet) item() *media.Item {
	return &media.Item{ID: b.ID, Name: b.Name, Added: b.Added, Kind: "BoxSet"}
}

// boxSets returns the collections the user sees, by ID: an administrator
// every one, to fill an empty one, and other users those with an item they
// can see.
func boxSets(catalog *media.Catalog, user state.User, d state.Data) map[string]boxSet {
	sets := map[string]boxSet{}
	for _, c := range d.Collections {
		set := boxSet{Collection: c}
		for _, id := range c.Items {
			if catalog == nil {
				break
			}
			if item, found := catalog.Lookup(id); found {
				set.members = append(set.members, item)
			}
		}
		if len(set.members) > 0 || user.Policy.IsAdministrator {
			sets[c.ID] = set
		}
	}
	return sets
}

// sessionBoxSets is boxSets for the session's user.
func (s *Server) sessionBoxSets(session state.Session) map[string]boxSet {
	snapshot := s.store.Snapshot()
	return boxSets(s.catalogFor(session), userOf(snapshot, session), snapshot)
}

// videos lists the movies and episodes of the collection, which its played
// state and "Mark played" cover.
func (s *Server) videos(set boxSet) []media.Item {
	videos := []media.Item{}
	for _, member := range set.members {
		if member.IsFolder() {
			videos = append(videos, s.folderEpisodes(*member)...)
		} else {
			videos = append(videos, *member)
		}
	}
	return videos
}

func (s *Server) boxSetUserData(set boxSet, items map[string]state.ItemState) object {
	unplayed := 0
	videos := s.videos(set)
	for _, video := range videos {
		if !items[video.ID].Played {
			unplayed++
		}
	}
	return object{"UnplayedItemCount": unplayed, "PlaybackPositionTicks": 0, "PlayCount": 0,
		"IsFavorite": items[set.ID].IsFavorite, "Played": len(videos) > 0 && unplayed == 0}
}

// boxSetDTO is the collection as a BoxSet. An administrator may edit and
// delete it, which Emby Web decides by CanEditItems and CanDelete.
func (s *Server) boxSetDTO(set boxSet, serverID string, items map[string]state.ItemState, admin bool) object {
	dto := baseFields(set.ID, set.Name, "BoxSet", serverID)
	dto["DateCreated"] = embyDate(set.Added)
	dto["IsFolder"] = true
	dto["ParentId"] = collectionsViewID
	dto["ChildCount"] = len(set.members)
	// Couch lists a collection by release date, then name (SortBy=DisplayOrder).
	dto["DisplayOrder"] = "PremiereDate"
	dto["CanEditItems"], dto["CanDelete"] = admin, admin
	dto["UserData"] = s.boxSetUserData(set, items)
	return dto
}

// collectionsViewDTO is the Collections library, a CollectionFolder of type
// boxsets; Emby Web opens it as a plain list of its collections.
func collectionsViewDTO(serverID string, count int, detail bool) object {
	dto := baseFields(collectionsViewID, "Collections", "CollectionFolder", serverID)
	dto["DateCreated"] = zeroDate
	dto["IsFolder"] = true
	dto["ParentId"] = rootID
	dto["CollectionType"] = "boxsets"
	dto["UserData"] = folderUserData()
	dto["PrimaryImageAspectRatio"] = 1.7777777777777777
	if detail {
		dto["ChildCount"] = count
		dto["Subviews"] = []string{}
	}
	return dto
}

// collectionIDs reads the comma-separated Ids of a request: from the query,
// or from the body Emby Web sends to add items. On failure the response has
// been written.
func collectionIDs(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	list := values(r, "Ids")
	if r.Header.Get("Content-Type") != "" {
		fields, ok := readFields(w, r)
		if !ok {
			return nil, false
		}
		list = append(list, fields["ids"])
	}
	ids := []string{}
	for _, v := range list {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids, true
}

func (s *Server) collectionRoutes(mux *http.ServeMux) {
	// The items must be movies, series, seasons or episodes of the catalogue.
	items := func(w http.ResponseWriter, r *http.Request) ([]string, bool) {
		ids, ok := collectionIDs(w, r)
		if !ok {
			return nil, false
		}
		for _, id := range ids {
			if _, found := s.catalog().Lookup(id); !found {
				failText(w, 400, "Only movies, series, seasons and episodes can be added to a collection.")
				return nil, false
			}
		}
		return ids, true
	}
	collectionChanged := func(w http.ResponseWriter, err error) {
		switch {
		case errors.Is(err, state.ErrCollectionNotFound):
			fail(w, 404, "NotFound")
		case errors.Is(err, state.ErrCollectionName):
			failText(w, 400, "A collection name must contain 1–128 bytes.")
		default:
			s.changed(w, err)
		}
	}
	mux.HandleFunc("POST /collections", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		ids, ok := items(w, r)
		if !ok {
			return
		}
		c, err := s.store.CreateCollection(value(r, "Name"), ids)
		if err != nil {
			collectionChanged(w, err)
			return
		}
		s.record(state.Activity{Type: "CollectionCreated", Name: "Collection " + c.Name + " created", Severity: "Info", UserID: session.UserID, ItemID: c.ID})
		respond(w, 200, object{"Id": c.ID, "Name": c.Name, "ItemAddedCount": len(c.Items)})
	}))
	mux.HandleFunc("POST /collections/{id}/items", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		ids, ok := items(w, r)
		if !ok {
			return
		}
		if len(ids) == 0 {
			fail(w, 400, "InvalidRequest")
			return
		}
		_, err := s.store.AddToCollection(r.PathValue("id"), ids)
		collectionChanged(w, err)
	}))
	remove := s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		ids, ok := collectionIDs(w, r)
		if !ok {
			return
		}
		collectionChanged(w, s.store.RemoveFromCollection(r.PathValue("id"), ids))
	})
	// A collection page offers "View missing items": the movies of the
	// collection's online list that the library lacks. Couch fetches no such
	// list, so none is missing.
	mux.HandleFunc("GET /collections/{id}/missing", s.protect(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if _, ok := s.sessionBoxSets(session)[r.PathValue("id")]; !ok {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, object{"Items": []any{}, "TotalRecordCount": 0})
	}))
	mux.HandleFunc("POST /collections/{id}/items/delete", remove)
	mux.HandleFunc("DELETE /collections/{id}/items", remove)
	// Couch deletes only collections, never media files: a media item reports
	// CanDelete false, so Emby Web offers deletion for collections alone.
	mux.HandleFunc("GET /items/{item}/deleteinfo", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		if _, ok := s.store.Snapshot().Collection(r.PathValue("item")); !ok {
			fail(w, 404, "NotFound")
			return
		}
		respond(w, 200, object{"Paths": []string{}})
	}))
	deleteCollections := func(w http.ResponseWriter, ids []string, session state.Session) {
		snapshot := s.store.Snapshot()
		for _, id := range ids {
			if _, ok := snapshot.Collection(id); !ok {
				failText(w, 400, "Couch deletes only collections, not media files.")
				return
			}
		}
		if err := s.store.DeleteCollections(ids); err != nil {
			collectionChanged(w, err)
			return
		}
		for _, id := range ids {
			c, _ := snapshot.Collection(id)
			s.record(state.Activity{Type: "CollectionDeleted", Name: "Collection " + c.Name + " deleted", Severity: "Info", UserID: session.UserID, ItemID: id})
		}
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("POST /items/delete", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		ids, ok := collectionIDs(w, r)
		if !ok {
			return
		}
		if len(ids) == 0 {
			fail(w, 400, "InvalidRequest")
			return
		}
		deleteCollections(w, ids, session)
	}))
	mux.HandleFunc("DELETE /items/{item}", s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		deleteCollections(w, []string{r.PathValue("item")}, session)
	}))
}

// renameCollection saves the name from Emby Web's metadata editor, which
// sends the whole item; the other fields are not stored for a collection.
func (s *Server) renameCollection(w http.ResponseWriter, r *http.Request, id string) {
	var body struct{ Name string }
	if decodeJSON(w, r, &body) != nil {
		fail(w, 400, "InvalidRequest")
		return
	}
	err := s.store.RenameCollection(id, body.Name)
	if errors.Is(err, state.ErrCollectionName) {
		failText(w, 400, "A collection name must contain 1–128 bytes.")
		return
	}
	s.changed(w, err)
}

// setBoxSetFlag marks a collection favorite, or marks its movies and
// episodes played, as Emby does.
func (s *Server) setBoxSetFlag(w http.ResponseWriter, token string, session state.Session, set boxSet, favorite, value bool) {
	if favorite {
		err := s.store.SetItem(token, set.ID, func(st *state.ItemState) { st.IsFavorite = value })
		if err != nil {
			s.changed(w, err)
			return
		}
	} else if err := s.setEpisodesPlayed(token, session.UserID, s.videos(set), value); err != nil {
		s.changed(w, err)
		return
	}
	data := s.boxSetUserData(set, userOf(s.store.Snapshot(), session).Items)
	data["ItemId"] = set.ID
	respond(w, 200, data)
}
