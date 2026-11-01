package state

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
)

// UserImage describes a stored avatar. The file is named by its revision, a
// content hash, so a reader never sees a partly replaced image; users who
// upload the same image share the file.
type UserImage struct {
	Revision string
	MIME     string
}

func (s *Store) userImagePath(revision string) string {
	return filepath.Join(s.dir, "user-image-"+revision)
}

// SetUserImage stores an already validated avatar for the user of token. The file is written and
// synced before the state refers to it. Afterwards, whether or not the change
// was committed, only files the committed state no longer references are
// removed.
func (s *Store) SetUserImage(token string, content []byte, mime string) error {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	sum := sha256.Sum256(content)
	image := UserImage{Revision: hex.EncodeToString(sum[:16]), MIME: mime}
	if err := writeFileSynced(s.userImagePath(image.Revision), content); err != nil {
		return err
	}
	var previous *UserImage
	err := s.Change(token, func(u *User, _ *Session) {
		previous = u.Image
		u.Image = &image
	})
	s.removeUnreferencedImages(previous, &image)
	return err
}

// DeleteUserImage removes the avatar of the user of token. Deleting a missing
// avatar succeeds.
func (s *Store) DeleteUserImage(token string) error {
	s.imageMu.Lock()
	defer s.imageMu.Unlock()
	var previous *UserImage
	err := s.Change(token, func(u *User, _ *Session) {
		previous = u.Image
		u.Image = nil
	})
	s.removeUnreferencedImages(previous)
	return err
}

// removeUnreferencedImages deletes the files of the given avatars unless the
// committed state still references them. Callers hold imageMu.
func (s *Store) removeUnreferencedImages(images ...*UserImage) {
	s.mu.RLock()
	current := map[string]bool{}
	for _, u := range s.data.Users {
		if u.Image != nil {
			current[u.Image.Revision] = true
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
	return os.Rename(f.Name(), name)
}
