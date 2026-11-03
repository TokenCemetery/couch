package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"

	"github.com/TokenCemetery/couch/internal/media"
	"github.com/TokenCemetery/couch/internal/state"
)

// Like Emby, a collection or playlist without an uploaded image shows a
// collage of its items' covers: two columns of two, filled column by column
// as in the reference collage of four movies, a 400×600 JPEG. Fewer images
// repeat from the first. Emby's collage of a playlist was not captured;
// Couch makes it square, the shape of album covers.
var (
	posterCollage = image.Rect(0, 0, 400, 600)
	squareCollage = image.Rect(0, 0, 600, 600)
)

const (
	collageTiles = 4
	// collageCandidates bounds how many items a collage looks at, so a large
	// collection without covers stays cheap to list.
	collageCandidates = 20
)

// collage is the generated Primary image of a collection or playlist.
type collage struct {
	bounds  image.Rectangle
	sources []media.Item
	info    media.Image
}

// collageOf picks the covers of up to four items: an episode shows its
// series' cover, and a season or track without its own its series' or
// album's. Items that show the same cover appear once, and covers larger than
// an upload may be are skipped. ok is false when no item has a cover.
func (s *Server) collageOf(catalog *media.Catalog, bounds image.Rectangle, items []*media.Item) (collage, bool) {
	c := collage{bounds: bounds}
	hash := sha256.New()
	_, _ = hash.Write([]byte(bounds.String()))
	seen := map[string]bool{}
	for _, item := range items[:min(len(items), collageCandidates)] {
		source, info, found := s.coverSource(catalog, *item)
		if !found || seen[source.ID] || info.Width*info.Height > maxUserImageArea {
			continue
		}
		seen[source.ID] = true
		c.sources = append(c.sources, source)
		_, _ = hash.Write([]byte("\x00" + source.ID + "\x00" + info.Revision))
		if len(c.sources) == collageTiles {
			break
		}
	}
	sum := hash.Sum(nil)
	c.info = media.Image{Revision: hex.EncodeToString(sum[:16]), Width: bounds.Dx(), Height: bounds.Dy(), MIME: "image/jpeg"}
	return c, len(c.sources) > 0
}

// coverSource is the item whose Primary image stands for item in a collage.
func (s *Server) coverSource(catalog *media.Catalog, item media.Item) (media.Item, media.Image, bool) {
	candidates := []string{item.ID}
	switch item.Type() {
	case "Episode":
		candidates = []string{item.SeriesID, item.ID}
	case "Season":
		candidates = append(candidates, item.SeriesID)
	case "Audio":
		candidates = append(candidates, item.ParentID)
	}
	for _, id := range candidates {
		source := item
		if id != item.ID {
			folder, found := catalog.Folder(id)
			if !found {
				continue
			}
			source = folder
		}
		if info, found := s.imageInfos(catalog, source)["Primary"]; found {
			return source, info, true
		}
	}
	return media.Item{}, media.Image{}, false
}

// errCoverUnreadable reports a collage that could not be drawn whole.
var errCoverUnreadable = errors.New("a cover of the collage cannot be read")

// openCollage opens the collage from the cover cache, drawing it the first
// time. Collages are drawn one at a time, decoding one cover at a time. A
// collage with a cover that cannot be read is not cached, so the next
// request tries again.
func (s *Server) openCollage(ctx context.Context, catalog *media.Catalog, c collage) (*os.File, media.Image, error) {
	if s.coverDir == "" {
		return nil, media.Image{}, os.ErrNotExist
	}
	name := filepath.Join(s.coverDir, "collage-"+c.info.Revision)
	if file, err := openCachedCover(name); err == nil {
		return file, c.info, nil
	}
	s.collageMu.Lock()
	defer s.collageMu.Unlock()
	// Another request may have drawn it meanwhile.
	if file, err := openCachedCover(name); err == nil {
		return file, c.info, nil
	}
	canvas := image.NewRGBA(c.bounds)
	tile := image.Rect(0, 0, c.bounds.Dx()/2, c.bounds.Dy()/2)
	for i := range collageTiles {
		at := tile.Add(image.Pt(i/2*tile.Dx(), i%2*tile.Dy()))
		if !s.drawCover(ctx, catalog, canvas, at, c.sources[i%len(c.sources)]) {
			return nil, media.Image{}, errCoverUnreadable
		}
	}
	var data bytes.Buffer
	if err := jpeg.Encode(&data, canvas, &jpeg.Options{Quality: 90}); err != nil {
		return nil, media.Image{}, err
	}
	if err := s.cacheCover(name, data.Bytes()); err != nil {
		return nil, media.Image{}, err
	}
	file, err := openCachedCover(name)
	return file, c.info, err
}

// drawCover scales the source's Primary image to fill at, cropping its
// middle. It reports false when the cover cannot be read.
func (s *Server) drawCover(ctx context.Context, catalog *media.Catalog, canvas *image.RGBA, at image.Rectangle, source media.Item) bool {
	file, _, err := s.openImage(ctx, catalog, source, "Primary")
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	config, _, err := image.DecodeConfig(file)
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width*config.Height > maxUserImageArea {
		return false
	}
	if _, err := file.Seek(0, 0); err != nil {
		return false
	}
	cover, _, err := image.Decode(file)
	if err != nil {
		return false
	}
	crop := cover.Bounds()
	if crop.Dx()*at.Dy() > crop.Dy()*at.Dx() {
		width := crop.Dy() * at.Dx() / at.Dy()
		crop.Min.X += (crop.Dx() - width) / 2
		crop.Max.X = crop.Min.X + width
	} else {
		height := crop.Dx() * at.Dy() / at.Dx()
		crop.Min.Y += (crop.Dy() - height) / 2
		crop.Max.Y = crop.Min.Y + height
	}
	draw.CatmullRom.Scale(canvas, at, cover, crop, draw.Src, nil)
	return true
}

// listImage is the Primary image of a collection or playlist: the image
// uploaded for it, or else a collage of its items in catalog.
type listImage struct {
	uploaded *state.UserImage
	collage  collage
	catalog  *media.Catalog
}

func (s *Server) listImage(catalog *media.Catalog, uploaded *state.UserImage, bounds image.Rectangle, items []*media.Item) (listImage, bool) {
	if uploaded != nil {
		return listImage{uploaded: uploaded, catalog: catalog}, true
	}
	c, ok := s.collageOf(catalog, bounds, items)
	return listImage{collage: c, catalog: catalog}, ok
}

// info describes the uploaded image, or the collage.
func (l listImage) info() media.Image {
	if l.uploaded != nil {
		return media.Image{Revision: l.uploaded.Revision, MIME: l.uploaded.MIME, Width: l.uploaded.Width, Height: l.uploaded.Height}
	}
	return l.collage.info
}

// addListImage names the image in a BoxSet or Playlist, with its aspect
// ratio, which sets the card's shape.
func (s *Server) addListImage(dto object, id string, l listImage, token string) {
	info := l.info()
	if tag := s.store.ImageTag(token, id, info.Revision); tag != "" {
		dto["ImageTags"].(object)["Primary"] = tag
		if info.Width > 0 && info.Height > 0 {
			dto["PrimaryImageAspectRatio"] = float64(info.Width) / float64(info.Height)
		}
	}
}

// openListImage opens the uploaded image, or the collage.
func (s *Server) openListImage(ctx context.Context, l listImage) (*os.File, media.Image, error) {
	if l.uploaded == nil {
		return s.openCollage(ctx, l.catalog, l.collage)
	}
	file, _, err := s.store.OpenImage(l.uploaded)
	return file, l.info(), err
}

// listOf finds id among the collections and the playlists of the session's
// user, as that user sees them. hasImage is false for one without an uploaded
// image whose items have no cover.
func (s *Server) listOf(session state.Session, id string) (image listImage, hasImage, found bool) {
	snapshot := s.store.Snapshot()
	c, collection := snapshot.Collection(id)
	p, playlist := snapshot.UserPlaylist(session.UserID, id)
	if !collection && !playlist {
		return listImage{}, false, false
	}
	catalog := s.catalogFor(session)
	if playlist {
		image, hasImage = s.playlistImage(catalog, playlistViewOf(catalog, p))
		return image, hasImage, true
	}
	set, visible := boxSetOf(catalog, userOf(snapshot, session), c)
	if !visible {
		return listImage{}, false, false
	}
	image, hasImage = s.boxSetImage(catalog, set)
	return image, hasImage, true
}
