package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/TokenCemetery/couch/internal/library"
	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// SetLibraries lets administrators manage the libraries in Emby Web and run
// the library scan task. Without it the library routes refuse.
func (s *Server) SetLibraries(m *library.Manager) {
	s.libraries = m
	var last *library.Result
	m.OnChange(func() {
		s.publishTasks()
		running, result := m.Status()
		if running || result == nil || result == last {
			return
		}
		last = result
		if result.Err == "" {
			s.record(state.Activity{Type: "LibraryScanCompleted", Name: "Library scan completed", Severity: "Info"})
		} else {
			s.record(state.Activity{Type: "LibraryScanFailed", Name: "Library scan could not read every folder", Overview: result.Err, Severity: "Warn"})
		}
	})
}

// scanTaskKey is the key Emby Web's library page looks up to run a scan.
const scanTaskKey = "RefreshLibrary"

var scanTaskID = derive("task", scanTaskKey)

// libraryFailure answers the errors of a library change in plain text, which
// Emby Web shows in its dialog.
func libraryFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, library.ErrNoRoots):
		failText(w, 400, "No media folders are configured: start Couch with -media-root.")
	case errors.Is(err, library.ErrOutsideRoot), errors.Is(err, library.ErrPath), errors.Is(err, library.ErrNotFolder),
		errors.Is(err, library.ErrOverlap), errors.Is(err, library.ErrType):
		failText(w, 400, "Couch cannot use this folder: "+err.Error()+".")
	default:
		failText(w, 400, err.Error())
	}
}

// virtualFolderDTO describes a library as the library setup page lists it.
// Libraries have no image, so no PrimaryImageItemId: Emby Web would request
// one and show a broken image.
func (s *Server) virtualFolderDTO(l media.Library) object {
	paths := []object{}
	for _, p := range l.Paths {
		paths = append(paths, object{"Path": p})
	}
	status := "Idle"
	if running, _ := s.libraries.Status(); running {
		status = "Active"
	}
	return object{"Name": l.Name, "Locations": l.Paths, "CollectionType": l.Type, "ItemId": l.ID, "Id": l.ID,
		"Guid": derive("guid", l.ID), "RefreshStatus": status,
		"LibraryOptions": object{"PathInfos": paths, "EnableRealtimeMonitor": false, "EnablePhotos": false}}
}

// libraryByID finds the library named by the Id parameter or field.
func libraryByID(libraries []media.Library, id string) (int, bool) {
	n := slices.IndexFunc(libraries, func(l media.Library) bool { return strings.EqualFold(l.ID, id) })
	return n, n >= 0
}

// changeLibraries applies change to a copy of the stored libraries, stores
// the result and records what change describes in the activity log; change
// writes any error response itself.
func (s *Server) changeLibraries(w http.ResponseWriter, session state.Session, change func(libraries []media.Library) ([]media.Library, string, bool)) {
	next, activity, ok := change(slices.Clone(s.libraries.Libraries()))
	if !ok {
		return
	}
	if err := s.libraries.SetLibraries(next); err != nil {
		s.internalError(w)
		return
	}
	s.record(state.Activity{Type: "LibraryChanged", Name: activity, Severity: "Info", UserID: session.UserID})
	w.WriteHeader(http.StatusNoContent)
}

// libraryName checks a library name, which stays unique ignoring case.
func libraryName(w http.ResponseWriter, libraries []media.Library, id, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		failText(w, 400, "The library name must contain 1 to 128 bytes.")
		return "", false
	}
	if slices.ContainsFunc(libraries, func(l media.Library) bool { return l.ID != id && strings.EqualFold(l.Name, name) }) {
		failText(w, 409, "A library with this name already exists.")
		return "", false
	}
	return name, true
}

func (s *Server) libraryRoutes(mux *http.ServeMux) {
	route := func(pattern string, handler authenticated) {
		mux.HandleFunc(pattern, s.admin(func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			if s.libraries == nil {
				failText(w, 400, "This server does not manage libraries.")
				return
			}
			handler(w, r, token, session)
		}))
	}
	route("GET /library/virtualfolders/query", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		items := []object{}
		for _, l := range s.libraries.Libraries() {
			items = append(items, s.virtualFolderDTO(l))
		}
		respond(w, 200, object{"Items": items, "TotalRecordCount": len(items)})
	})
	route("POST /library/virtualfolders", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var body struct {
			LibraryOptions struct{ PathInfos []struct{ Path string } }
			Paths          []string
		}
		if decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		paths := body.Paths
		for _, info := range body.LibraryOptions.PathInfos {
			paths = append(paths, info.Path)
		}
		s.changeLibraries(w, session, func(libraries []media.Library) ([]media.Library, string, bool) {
			name, ok := libraryName(w, libraries, "", value(r, "name"))
			if !ok {
				return nil, "", false
			}
			created := media.Library{ID: media.NewLibraryID(), Name: name, Type: strings.ToLower(value(r, "collectionType")), Paths: []string{}}
			if created.Type != media.Movies && created.Type != media.TVShows && created.Type != media.Music {
				libraryFailure(w, library.ErrType)
				return nil, "", false
			}
			for _, p := range paths {
				folder, err := s.libraries.Folder(libraries, created, p)
				if err != nil {
					libraryFailure(w, err)
					return nil, "", false
				}
				created.Paths = append(created.Paths, folder)
			}
			return append(libraries, created), "Library " + name + " was created", true
		})
	})
	route("POST /library/virtualfolders/name", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		s.changeLibraries(w, session, func(libraries []media.Library) ([]media.Library, string, bool) {
			n, ok := libraryByID(libraries, value(r, "id"))
			if !ok {
				fail(w, 404, "NotFound")
				return nil, "", false
			}
			name, ok := libraryName(w, libraries, libraries[n].ID, value(r, "newName"))
			old := libraries[n].Name
			libraries[n].Name = name
			return libraries, "Library " + old + " was renamed " + name, ok
		})
	})
	remove := func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		s.changeLibraries(w, session, func(libraries []media.Library) ([]media.Library, string, bool) {
			n, ok := libraryByID(libraries, value(r, "id"))
			if !ok {
				fail(w, 404, "NotFound")
				return nil, "", false
			}
			name := libraries[n].Name
			return slices.Delete(libraries, n, n+1), "Library " + name + " was removed", true
		})
	}
	route("POST /library/virtualfolders/delete", remove)
	route("DELETE /library/virtualfolders", remove)
	route("POST /library/virtualfolders/paths", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var body struct {
			ID       string `json:"Id"`
			Path     string
			PathInfo struct{ Path string }
		}
		if decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		if body.Path == "" {
			body.Path = body.PathInfo.Path
		}
		s.changeLibraries(w, session, func(libraries []media.Library) ([]media.Library, string, bool) {
			n, ok := libraryByID(libraries, body.ID)
			if !ok {
				fail(w, 404, "NotFound")
				return nil, "", false
			}
			folder, err := s.libraries.Folder(libraries, libraries[n], body.Path)
			if err != nil {
				libraryFailure(w, err)
				return nil, "", false
			}
			libraries[n].Paths = append(slices.Clone(libraries[n].Paths), folder)
			return libraries, "A folder was added to library " + libraries[n].Name, true
		})
	})
	removePath := func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		s.changeLibraries(w, session, func(libraries []media.Library) ([]media.Library, string, bool) {
			n, ok := libraryByID(libraries, value(r, "id"))
			path := filepath.Clean(value(r, "path"))
			if !ok || !slices.Contains(libraries[n].Paths, path) {
				fail(w, 404, "NotFound")
				return nil, "", false
			}
			libraries[n].Paths = slices.DeleteFunc(slices.Clone(libraries[n].Paths), func(p string) bool { return p == path })
			return libraries, "A folder was removed from library " + libraries[n].Name, true
		})
	}
	route("POST /library/virtualfolders/paths/delete", removePath)
	route("DELETE /library/virtualfolders/paths", removePath)
	// A folder's path cannot change, and Couch reads only local folders, so
	// the network path the edit dialog offers is refused.
	route("POST /library/virtualfolders/paths/update", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var body struct {
			ID       string `json:"Id"`
			PathInfo struct{ Path, NetworkPath string }
		}
		if decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		libraries := s.libraries.Libraries()
		n, ok := libraryByID(libraries, body.ID)
		if !ok || !slices.Contains(libraries[n].Paths, filepath.Clean(body.PathInfo.Path)) {
			fail(w, 404, "NotFound")
			return
		}
		if body.PathInfo.NetworkPath != "" {
			failText(w, 400, "Couch reads only local folders and does not use network paths.")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// Couch stores no library options: its scanner and metadata sources are
	// fixed, so the options editor's settings take no effect.
	route("POST /library/virtualfolders/libraryoptions", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		var body struct {
			ID string `json:"Id"`
		}
		if decodeJSON(w, r, &body) != nil {
			fail(w, 400, "InvalidRequest")
			return
		}
		if _, ok := libraryByID(s.libraries.Libraries(), body.ID); !ok {
			fail(w, 404, "NotFound")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	route("GET /libraries/availableoptions", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		typeOptions := []object{}
		for _, kind := range []string{media.Movies, media.TVShows, media.Music} {
			typeOptions = append(typeOptions, object{"Type": kind, "MetadataFetchers": []any{}, "ImageFetchers": []any{},
				"SupportedImageTypes": []string{}, "DefaultImageOptions": []any{}})
		}
		respond(w, 200, object{"MetadataSavers": []any{}, "MetadataReaders": []any{}, "SubtitleFetchers": []any{},
			"LyricsFetchers": []any{}, "TypeOptions": typeOptions, "DefaultLibraryOptions": object{"PathInfos": []any{}}})
	})

	// The folder browser of the library dialogs sees only the media roots.
	route("GET /environment/drives", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		entries := []object{}
		for _, root := range s.libraries.Roots() {
			entries = append(entries, object{"Name": root, "Path": root, "Type": "Directory"})
		}
		respond(w, 200, entries)
	})
	contents := func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		path := value(r, "path")
		if r.Method == http.MethodPost {
			var body struct{ Path string }
			if decodeJSON(w, r, &body) != nil {
				fail(w, 400, "InvalidRequest")
				return
			}
			if body.Path != "" {
				path = body.Path
			}
		}
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			libraryFailure(w, library.ErrPath)
			return
		}
		if _, err := s.libraries.Inside(path); err != nil {
			libraryFailure(w, err)
			return
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			libraryFailure(w, library.ErrNotFolder)
			return
		}
		folders := []object{}
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				folders = append(folders, object{"Name": entry.Name(), "Path": filepath.Join(path, entry.Name()), "Type": "Directory"})
			}
		}
		respond(w, 200, folders)
	}
	route("GET /environment/directorycontents", contents)
	route("POST /environment/directorycontents", contents)
	route("GET /environment/parentpath", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		path := filepath.Clean(value(r, "path"))
		parent := ""
		if root, err := s.libraries.Inside(path); err == nil && path != root {
			parent = filepath.Dir(path)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(parent)) //nolint:gosec // text/plain, and every response is sent with nosniff
	})
	route("GET /environment/defaultdirectorybrowser", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		path := ""
		if roots := s.libraries.Roots(); len(roots) > 0 {
			path = roots[0]
		}
		respond(w, 200, object{"Path": path})
	})
	route("POST /environment/validatepath", func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
		// Emby Web posts a form, with Emby's spelling ValidateWriteable.
		fields, ok := readFields(w, r)
		if !ok {
			return
		}
		if fields["validatewriteable"] == "true" || fields["validatewritable"] == "true" || fields["isfile"] == "true" {
			failText(w, 400, "Couch reads only folders and never writes to them.")
			return
		}
		if _, err := s.libraries.Inside(filepath.Clean(fields["path"])); err != nil {
			libraryFailure(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	for _, path := range []string{"/environment/networkdevices", "/environment/networkshares"} {
		route("GET "+path, func(w http.ResponseWriter, r *http.Request, token string, session state.Session) {
			respond(w, 200, []any{})
		})
	}
}
