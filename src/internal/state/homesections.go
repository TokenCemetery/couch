package state

import (
	"errors"
	"slices"
	"time"
)

// maxHomeSections bounds a user's home screen.
const maxHomeSections = 100

// ErrHomeSectionLimit is returned when a home screen would exceed maxHomeSections.
var ErrHomeSectionLimit = errors.New("home section limit reached")

// HomeSection is one row of a user's home screen as Emby Web's section
// editor saves it.
type HomeSection struct {
	ID                    string
	SectionType           string
	CustomName            string   `json:",omitempty"`
	Subtitle              string   `json:",omitempty"`
	ViewType              string   `json:",omitempty"`
	DisplayMode           string   `json:",omitempty"`
	ScrollDirection       string   `json:",omitempty"`
	ImageType             string   `json:",omitempty"`
	ParentID              string   `json:",omitempty"`
	SortBy                string   `json:",omitempty"`
	SortOrder             string   `json:",omitempty"`
	ItemTypes             []string `json:",omitempty"`
	ExcludedFolders       []string `json:",omitempty"`
	IncludeNextUpInResume bool
	Query                 HomeSectionQuery
}

// HomeSectionQuery is the filter of a home section; nil means either value.
type HomeSectionQuery struct {
	IsPlayed   *bool    `json:",omitempty"`
	IsFavorite *bool    `json:",omitempty"`
	GenreIDs   []string `json:",omitempty"`
	StudioIDs  []string `json:",omitempty"`
	TagIDs     []string `json:",omitempty"`
}

// ChangeHomeSections replaces the home screen of the session's user with what
// fn returns for the current one, which is nil until the user first edits it.
// fn must not modify its argument. A section without an ID gets a new one.
func (s *Store) ChangeHomeSections(token string, fn func([]HomeSection) ([]HomeSection, error)) error {
	userID := s.sessionUser(token)
	return s.update(userID, nil, func(d *Data) error {
		session, ok := d.Sessions[tokenHash(token)]
		u, exists := d.Users[userID]
		if !ok || !exists || !session.ExpiresAt.After(time.Now()) || session.UserID != userID {
			return ErrSession
		}
		sections, err := fn(u.HomeSections)
		if err != nil {
			return err
		}
		if len(sections) > maxHomeSections {
			return ErrHomeSectionLimit
		}
		sections = slices.Clone(sections)
		if sections == nil {
			sections = []HomeSection{}
		}
		for i := range sections {
			if sections[i].ID == "" {
				sections[i].ID = randomID()
			}
		}
		u.HomeSections = sections
		d.Users[userID] = u
		return nil
	})
}
