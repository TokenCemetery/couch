package state

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// UserImage describes an image uploaded from Emby Web: a user's avatar, or
// the Primary image of a collection or playlist. The file is named by its
// revision, a content hash, so a reader never sees a partly replaced image;
// owners who upload the same image share the file. Width and Height are
// kept for a collection's or playlist's image, and are 0 for an avatar.
type UserImage struct {
	Revision      string
	MIME          string
	Width, Height int
}

func (s *Store) userImagePath(revision string) string {
	return filepath.Join(s.dir, "user-image-"+revision)
}

// images lists the uploaded images the state references.
func (d Data) images() []*UserImage {
	var images []*UserImage
	for _, u := range d.Users {
		images = append(images, u.Image)
	}
	for _, c := range d.Collections {
		images = append(images, c.Image)
	}
	for _, p := range d.Playlists {
		images = append(images, p.Image)
	}
	return images
}

// setImage stores an already validated image, or none when content is nil,
// through swap, which commits it and returns the image it replaced. The file
// and its directory are synced before the state refers to it. Afterwards,
// whether or not the change was committed, only files the committed state no
// longer references are removed.
func (s *Store) setImage(content []byte, info UserImage, swap func(*UserImage) (*UserImage, error)) error {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	var image *UserImage
	if content != nil {
		sum := sha256.Sum256(content)
		info.Revision = hex.EncodeToString(sum[:16])
		image = &info
		if err := writeFileSynced(s.userImagePath(image.Revision), content); err != nil {
			return err
		}
	}
	previous, err := swap(image)
	s.removeUnreferencedImages(previous, image)
	return err
}

// SetUserImage stores the avatar of the user of token.
func (s *Store) SetUserImage(token string, content []byte, mime string) error {
	return s.setImage(content, UserImage{MIME: mime}, func(image *UserImage) (previous *UserImage, err error) {
		err = s.Change(token, func(u *User, _ *Session) { previous, u.Image = u.Image, image })
		return previous, err
	})
}

// DeleteUserImage removes the avatar of the user of token. Deleting a missing
// avatar succeeds.
func (s *Store) DeleteUserImage(token string) error {
	return s.SetUserImage(token, nil, "")
}

// SetCollectionImage stores the Primary image of the collection, of the
// type and size in info, or removes it when content is nil.
func (s *Store) SetCollectionImage(id string, content []byte, info UserImage) error {
	return s.setImage(content, info, func(image *UserImage) (previous *UserImage, err error) {
		err = s.changeCollection(id, func(c *Collection) error {
			previous, c.Image = c.Image, image
			return nil
		})
		return previous, err
	})
}

// SetPlaylistImage stores the Primary image of the user's playlist, of the
// type and size in info, or removes it when content is nil. The playlist's
// modification time, which follows its name and entries, is kept.
func (s *Store) SetPlaylistImage(userID, id string, content []byte, info UserImage) error {
	return s.setImage(content, info, func(image *UserImage) (previous *UserImage, err error) {
		err = s.editPlaylist(userID, id, false, func(p *Playlist) error {
			previous, p.Image = p.Image, image
			return nil
		})
		return previous, err
	})
}

// removeUnreferencedImages deletes the files of the given images unless the
// committed state still references them. Callers hold imageMu.
func (s *Store) removeUnreferencedImages(images ...*UserImage) {
	s.mu.RLock()
	current := map[string]bool{}
	for _, image := range s.data.images() {
		if image != nil {
			current[image.Revision] = true
		}
	}
	s.mu.RUnlock()
	for _, image := range images {
		if image != nil && !current[image.Revision] {
			_ = os.Remove(s.userImagePath(image.Revision))
		}
	}
}

// OpenUserImage opens the user's current avatar file.
func (s *Store) OpenUserImage(userID string) (*os.File, UserImage, error) {
	s.mu.RLock()
	image := s.data.Users[userID].Image
	s.mu.RUnlock()
	return s.OpenImage(image)
}

// OpenImage opens the file of an uploaded image the state referenced.
func (s *Store) OpenImage(image *UserImage) (*os.File, UserImage, error) {
	if image == nil {
		return nil, UserImage{}, os.ErrNotExist
	}
	file, err := os.Open(s.userImagePath(image.Revision))
	return file, *image, err
}

func writeFileSynced(name string, content []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".user-image-*")
	if err != nil {
		return err
	}
	// After a successful rename this removal fails harmlessly.
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), name); err != nil {
		return err
	}
	return syncFile(filepath.Dir(name))
}
