package server

import (
	"slices"
	"strings"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// libraryLimit returns the libraries a user may see and true, or false when
// the user sees every library. An administrator always does.
func libraryLimit(user state.User) ([]string, bool) {
	if user.Policy.IsAdministrator || user.Policy.EnableAllFolders {
		return nil, false
	}
	return user.Policy.EnabledFolders, true
}

// catalogFor returns the catalogue as the session's user sees it: whole, or
// only the libraries the user's policy enables (#116). An item outside them
// is then not found, like a missing one.
func (s *Server) catalogFor(session state.Session) *media.Catalog {
	return s.catalogView(s.catalog(), userOf(s.store.Snapshot(), session))
}

func (s *Server) catalogView(catalog *media.Catalog, user state.User) *media.Catalog {
	ids, limited := libraryLimit(user)
	if catalog == nil || !limited {
		return catalog
	}
	return s.catalogOnly(catalog, ids)
}

// catalogOnly is catalog.Only(ids), kept until the catalogue changes.
func (s *Server) catalogOnly(catalog *media.Catalog, ids []string) *media.Catalog {
	key := strings.Join(ids, "\x00")
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	if s.viewsOf != catalog {
		s.viewsOf, s.views = catalog, map[string]*media.Catalog{}
	}
	view, ok := s.views[key]
	if !ok {
		view = catalog.Only(ids)
		s.views[key] = view
	}
	return view
}

// entityIn returns the genre, studio, tag or person with this ID when an item
// of catalog names it, so a limited user does not open one named only by
// items they cannot see.
func (s *Server) entityIn(catalog *media.Catalog, id string) (entity, bool) {
	e, found := s.entity(id)
	if !found || catalog == s.catalog() {
		return e, found
	}
	for _, items := range [][]media.Item{catalog.Items, catalog.Folders} {
		for _, item := range items {
			if slices.Contains(metadataNames(s.info(item.ID), e.Type), e.Name) ||
				(e.Type == "MusicArtist" && slices.Contains(artistsOf(catalog, item, "Artist,AlbumArtist"), e.Name)) {
				return e, true
			}
		}
	}
	return entity{}, false
}

// folderGUIDs reports a policy's libraries by their Guids, which Emby Web's
// Access tab matches against its library list.
func folderGUIDs(ids []string) []string {
	guids := make([]string, len(ids))
	for i, id := range ids {
		guids[i] = derive("guid", id)
	}
	return guids
}
