package server

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

func (s *Server) imageRoutes(mux *http.ServeMux) {
	for _, route := range []string{"/items/{item}/images/{kind}", "/items/{item}/images/{kind}/{index}"} {
		mux.HandleFunc("GET "+route, s.itemImage)
	}
	// The image editor lists an item's images; Couch has no image search
	// and changes only the Primary image of a collection or playlist. The
	// owner of a playlist may open it too.
	mux.HandleFunc("GET /items/{item}/images", s.ownerOrAdmin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		id := r.PathValue("item")
		if image, hasImage, found := s.listOf(session, id); found {
			infos := []object{}
			if hasImage {
				info := image.info()
				infos = append(infos, object{"ImageType": "Primary", "ImageTag": s.store.ImageTag(token, id, info.Revision), "Width": info.Width, "Height": info.Height})
			}
			respond(w, 200, infos)
			return
		}
		catalog := s.catalog()
		item, found := findItem(catalog, id)
		if !found {
			fail(w, 404, "NotFound")
			return
		}
		images := s.imageInfos(catalog, item)
		infos := []object{}
		for _, kind := range media.ImageTypes {
			image, ok := images[kind]
			if !ok {
				continue
			}
			info := object{"ImageType": kind, "ImageTag": s.store.ImageTag(token, id, image.Revision), "Width": image.Width, "Height": image.Height}
			if kind == "Backdrop" {
				info["ImageIndex"] = 0
			}
			infos = append(infos, info)
		}
		respond(w, 200, infos)
	}))
	mux.HandleFunc("GET /items/{item}/remoteimages/providers", s.ownerOrAdmin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		respond(w, 200, []object{})
	}))
	change := s.ownerOrAdmin(s.changeItemImage)
	mux.HandleFunc("POST /items/{item}/images/{rest...}", change)
	mux.HandleFunc("DELETE /items/{item}/images/{rest...}", change)
}

// changeItemImage uploads or deletes the Primary image of a collection, for
// an administrator, or of the user's own playlist. Emby Web uploads with
// POST .../Images/{Type} and deletes with POST .../Images/{Type}/{Index}/Delete.
func (s *Server) changeItemImage(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
	id := r.PathValue("item")
	snapshot := s.store.Snapshot()
	_, collection := snapshot.Collection(id)
	_, playlist := snapshot.UserPlaylist(session.UserID, id)
	if !collection && !playlist {
		failText(w, 400, "Couch does not change images: replace the image file next to the video, or use Identify, then Refresh Metadata.")
		return
	}
	parts := strings.Split(r.PathValue("rest"), "/")
	remove := r.Method == http.MethodDelete
	if len(parts) > 1 && parts[len(parts)-1] == "delete" {
		remove, parts = true, parts[:len(parts)-1]
	}
	if parts[0] != "primary" || len(parts) > 2 || (len(parts) == 2 && parts[1] != "0") {
		failText(w, 400, "Couch keeps only a Primary image for a collection or playlist.")
		return
	}
	var content []byte
	var info state.UserImage
	if !remove {
		var ok bool
		if content, info, ok = readImageUpload(w, r); !ok {
			return
		}
	}
	if collection {
		err := s.store.SetCollectionImage(id, content, info)
		if errors.Is(err, state.ErrCollectionNotFound) {
			fail(w, 404, "NotFound")
			return
		}
		s.changed(w, err)
		return
	}
	s.playlistChanged(w, s.store.SetPlaylistImage(session.UserID, id, content, info))
}

// imageAccess authorizes an image request by API token or, because browsers
// load images without headers, by an ImageTag signed for subject. The returned
// revision is the one the tag grants; it is empty for token access.
func (s *Server) imageAccess(w http.ResponseWriter, r *http.Request, subject string) (state.Session, string, bool) {
	token, err := requestToken(r)
	if err != nil {
		fail(w, 400, "InvalidAuthentication")
		return state.Session{}, "", false
	}
	revision := ""
	session, authErr := s.store.Authenticate(token)
	if token == "" {
		// ImageTags are opaque to Emby Web; it sends them as the tag query.
		query, err := catalogQuery(r)
		if err != nil {
			fail(w, 400, "InvalidQuery")
			return state.Session{}, "", false
		}
		session, revision, authErr = s.store.AuthenticateImageTag(subject, query["tag"])
	}
	if authErr != nil {
		fail(w, 401, "Unauthorized")
		return state.Session{}, "", false
	}
	for _, id := range values(r, "UserId") {
		if id != "" && !strings.EqualFold(id, session.UserID) {
			fail(w, 403, "Forbidden")
			return state.Session{}, "", false
		}
	}
	return session, revision, true
}

// primaryImage reports whether the route names the first Primary image.
func primaryImage(r *http.Request) bool {
	return imageKind(r) == "primary"
}

// imageKind is the image type the route names when it is the first of its
// type, or "": Couch keeps at most one image per type.
func imageKind(r *http.Request) string {
	if r.PathValue("index") != "" && r.PathValue("index") != "0" {
		return ""
	}
	return r.PathValue("kind")
}

// serveImage sends a validated JPEG/PNG with private revalidated caching.
func serveImage(w http.ResponseWriter, r *http.Request, file *os.File, mime, revision string) {
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-cache, no-transform")
	w.Header().Set("ETag", `W/"`+revision+`"`)
	// No second-resolution Last-Modified: mtime nanoseconds are in the ETag.
	http.ServeContent(w, r, "", time.Time{}, file)
}

func (s *Server) itemImage(w http.ResponseWriter, r *http.Request) {
	session, revision, ok := s.imageAccess(w, r, r.PathValue("item"))
	if !ok {
		return
	}
	kind := imageKind(r)
	if image, hasImage, found := s.listOf(session, r.PathValue("item")); found {
		if !hasImage || kind != "primary" {
			fail(w, 404, "NotFound")
			return
		}
		file, info, err := s.openListImage(r.Context(), image)
		if err != nil {
			fail(w, 404, "ImageUnavailable")
			return
		}
		defer func() { _ = file.Close() }()
		if revision != "" && revision != info.Revision {
			fail(w, 404, "ImageChanged")
			return
		}
		serveImage(w, r, file, info.MIME, info.Revision)
		return
	}
	catalog := s.catalogFor(session)
	item, found := findItem(catalog, r.PathValue("item"))
	if !found {
		if e, ok := s.entityIn(catalog, r.PathValue("item")); !ok || (e.Type != "Person" && e.Type != "MusicArtist") {
			fail(w, 404, "NotFound")
			return
		}
	}
	if kind == "" {
		fail(w, 404, "NotFound")
		return
	}
	var file *os.File
	var info media.Image
	var err error
	if found {
		file, info, err = s.openImage(r.Context(), s.catalog(), item, kind)
	} else {
		file, info, err = s.openProviderImage(r.PathValue("item"), kind)
	}
	if err != nil {
		fail(w, 404, "ImageUnavailable")
		return
	}
	defer func() { _ = file.Close() }()
	if revision != "" && revision != info.Revision {
		fail(w, 404, "ImageChanged")
		return
	}
	serveImage(w, r, file, info.MIME, info.Revision)
}
