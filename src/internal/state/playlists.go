package state

import (
	"errors"
	"slices"
	"strings"
	"time"
)

// Limits on playlists, which any user creates from Emby Web.
const (
	maxPlaylists       = 1000 // per user
	maxPlaylistEntries = 10000
)

// Errors returned by the playlist methods of Store.
var (
	ErrPlaylistNotFound = errors.New("playlist or playlist entry not found")
	ErrPlaylistName     = errors.New("playlist name must contain 1–128 bytes")
)

// Playlist is an ordered list of catalogue items private to the user who
// created it. An item may appear more than once, so each entry has its own ID:
// Emby's PlaylistItemId. Entries keep IDs whose files are gone, so an item
// comes back when its path does. Image is the Primary image its user
// uploaded.
type Playlist struct {
	ID       string
	UserID   string
	Name     string
	Added    time.Time
	Modified time.Time
	Entries  []PlaylistEntry
	Image    *UserImage
}

// PlaylistEntry is one position in a playlist.
type PlaylistEntry struct {
	ID     string
	ItemID string
}

// Playlist returns the playlist with this ID.
func (d Data) Playlist(id string) (Playlist, bool) {
	for _, p := range d.Playlists {
		if p.ID == id {
			return p, true
		}
	}
	return Playlist{}, false
}

// UserPlaylist returns the playlist with this ID if the user owns it.
func (d Data) UserPlaylist(userID, id string) (Playlist, bool) {
	p, ok := d.Playlist(id)
	return p, ok && p.UserID == userID
}

func playlistName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return "", ErrPlaylistName
	}
	return name, nil
}

// appendEntries appends an entry for each non-empty item ID; with
// skipDuplicates, for those the playlist does not hold yet. It returns how many
// it appended.
func appendEntries(entries []PlaylistEntry, items []string, skipDuplicates bool) ([]PlaylistEntry, int) {
	n := len(entries)
	for _, id := range items {
		if id == "" || (skipDuplicates && slices.ContainsFunc(entries, func(e PlaylistEntry) bool { return e.ItemID == id })) {
			continue
		}
		entries = append(entries, PlaylistEntry{ID: randomID(), ItemID: id})
	}
	return entries, len(entries) - n
}

// changePlaylist replaces the user's playlist id with the result of fn, which
// works on a copy, and records the change time.
func (s *Store) changePlaylist(userID, id string, fn func(*Playlist) error) error {
	return s.editPlaylist(userID, id, true, fn)
}

// editPlaylist is changePlaylist that records the change time only when
// touch is set.
func (s *Store) editPlaylist(userID, id string, touch bool, fn func(*Playlist) error) error {
	return s.update("", nil, func(d *Data) error {
		i := slices.IndexFunc(d.Playlists, func(p Playlist) bool { return p.ID == id && p.UserID == userID })
		if i < 0 {
			return ErrPlaylistNotFound
		}
		p := d.Playlists[i]
		p.Entries = slices.Clone(p.Entries)
		if err := fn(&p); err != nil {
			return err
		}
		if len(p.Entries) > maxPlaylistEntries {
			return ErrStateLimit
		}
		if touch {
			p.Modified = time.Now().UTC()
		}
		d.Playlists = slices.Clone(d.Playlists)
		d.Playlists[i] = p
		return nil
	})
}

// CreatePlaylist stores a new playlist of the user with an entry per item.
func (s *Store) CreatePlaylist(userID, name string, items []string) (Playlist, error) {
	name, err := playlistName(name)
	if err != nil {
		return Playlist{}, err
	}
	now := time.Now().UTC()
	p := Playlist{ID: randomID(), UserID: userID, Name: name, Added: now, Modified: now}
	p.Entries, _ = appendEntries([]PlaylistEntry{}, items, false)
	if len(p.Entries) > maxPlaylistEntries {
		return Playlist{}, ErrStateLimit
	}
	err = s.update("", nil, func(d *Data) error {
		if _, ok := d.Users[userID]; !ok {
			return ErrUserNotFound
		}
		owned := 0
		for _, other := range d.Playlists {
			if other.UserID == userID {
				owned++
			}
		}
		if owned >= maxPlaylists {
			return ErrStateLimit
		}
		d.Playlists = append(slices.Clone(d.Playlists), p)
		return nil
	})
	return p, err
}

// AddToPlaylist appends an entry per item and returns how many it appended.
func (s *Store) AddToPlaylist(userID, id string, items []string, skipDuplicates bool) (int, error) {
	added := 0
	err := s.changePlaylist(userID, id, func(p *Playlist) error {
		p.Entries, added = appendEntries(p.Entries, items, skipDuplicates)
		return nil
	})
	return added, err
}

// RemoveFromPlaylist removes the entries; an unknown entry ID is ignored.
func (s *Store) RemoveFromPlaylist(userID, id string, entryIDs []string) error {
	return s.changePlaylist(userID, id, func(p *Playlist) error {
		p.Entries = slices.DeleteFunc(p.Entries, func(e PlaylistEntry) bool { return slices.Contains(entryIDs, e.ID) })
		return nil
	})
}

// MovePlaylistEntry moves the entry to index, counted after the entry is taken
// out; an index past the end moves it to the end.
func (s *Store) MovePlaylistEntry(userID, id, entryID string, index int) error {
	return s.changePlaylist(userID, id, func(p *Playlist) error {
		i := slices.IndexFunc(p.Entries, func(e PlaylistEntry) bool { return e.ID == entryID })
		if i < 0 {
			return ErrPlaylistNotFound
		}
		entry := p.Entries[i]
		p.Entries = slices.Delete(p.Entries, i, i+1)
		p.Entries = slices.Insert(p.Entries, min(max(index, 0), len(p.Entries)), entry)
		return nil
	})
}

// RenamePlaylist changes the name of the user's playlist.
func (s *Store) RenamePlaylist(userID, id, name string) error {
	name, err := playlistName(name)
	if err != nil {
		return err
	}
	return s.changePlaylist(userID, id, func(p *Playlist) error {
		p.Name = name
		return nil
	})
}

// DeletePlaylists removes the user's playlists and their images; their items
// are not touched.
func (s *Store) DeletePlaylists(userID string, ids []string) error {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	var images []*UserImage
	err := s.update("", nil, func(d *Data) error {
		images = nil
		for _, id := range ids {
			p, ok := d.UserPlaylist(userID, id)
			if !ok {
				return ErrPlaylistNotFound
			}
			images = append(images, p.Image)
		}
		d.Playlists = slices.DeleteFunc(slices.Clone(d.Playlists), func(p Playlist) bool { return slices.Contains(ids, p.ID) })
		return nil
	})
	s.removeUnreferencedImages(images...)
	return err
}
