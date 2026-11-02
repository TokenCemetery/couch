package media

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG for image.DecodeConfig
	_ "image/png"  // register PNG for image.DecodeConfig
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

const maxImageBytes = 8 << 20

// Image describes a validated local cover image without its pixels.
type Image struct {
	Revision      string
	Width, Height int
	MIME          string
}

// ImageTypes lists the Emby image types found as local sidecars.
var ImageTypes = []string{"Primary", "Backdrop", "Logo", "Thumb"}

// imageNames holds, by lowercase image type, the suffixes added to a video's
// name and the names shared by its folder, in search order.
var imageNames = map[string]struct{ own, shared []string }{
	"primary":  {[]string{"-poster", ""}, []string{"poster", "folder", "cover"}},
	"backdrop": {[]string{"-fanart", "-backdrop"}, []string{"backdrop", "fanart"}},
	"logo":     {[]string{"-clearlogo", "-logo"}, []string{"clearlogo", "logo"}},
	"thumb":    {[]string{"-landscape"}, []string{"landscape", "thumb"}},
}

// OpenImage searches local JPEG/PNG sidecars of an image type (any case).
// Headers and encoded file size are bounded; pixels are never decoded or
// resized in the server.
func (c *Catalog) OpenImage(item Item, kind string) (*os.File, Image, error) {
	names, found := imageNames[strings.ToLower(kind)]
	if !found {
		return nil, Image{}, os.ErrNotExist
	}
	if c == nil || c.rootOf(item) == nil {
		return nil, Image{}, os.ErrNotExist
	}
	primary := strings.EqualFold(kind, "Primary")
	if item.Type() == "Photo" {
		if !primary {
			return nil, Image{}, os.ErrNotExist
		}
		return c.openPhoto(item)
	}
	file, info, err := c.openImage(item, names.own, names.shared, c.imageFiles(item))
	if err != nil && primary && item.Type() == "PhotoAlbum" {
		if photo, found := c.firstPhoto(item); found {
			return c.openPhoto(photo)
		}
	}
	return file, info, err
}

// openPhoto opens a photo as its own Primary image, checking that the file
// is still a picture within the limits.
func (c *Catalog) openPhoto(item Item) (*os.File, Image, error) {
	file, err := c.Open(item)
	if err != nil {
		return nil, Image{}, err
	}
	info, err := file.Stat()
	if err != nil || info.Size() <= 0 || info.Size() > maxPhotoBytes {
		_ = file.Close()
		return nil, Image{}, os.ErrNotExist
	}
	_, format, err := image.DecodeConfig(io.LimitReader(file, 1<<20))
	if err != nil || !isPhoto("."+format) {
		_ = file.Close()
		return nil, Image{}, os.ErrNotExist
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, Image{}, err
	}
	return file, Image{Revision: photoRevision(item.Path, info.Size(), info.ModTime()), Width: item.Photo.Width, Height: item.Photo.Height, MIME: "image/" + format}, nil
}

// photoImage describes a photo's Primary image from its scan, without
// opening the file; openPhoto refuses it if the file has changed since.
func photoImage(item Item) Image {
	mime := "image/" + strings.TrimPrefix(strings.ToLower(path.Ext(item.Path)), ".")
	if mime == "image/jpg" {
		mime = "image/jpeg"
	}
	return Image{Revision: photoRevision(item.Path, item.Size, item.Modified), Width: item.Photo.Width, Height: item.Photo.Height, MIME: mime}
}

func photoRevision(file string, size int64, modified time.Time) string {
	return stableID(fmt.Sprintf("photo\x00%s\x00%d\x00%d", file, size, modified.UnixNano()))
}

// firstPhoto returns the first photo directly in a photo album, by path,
// which stands in for an album without an image of its own.
func (c *Catalog) firstPhoto(album Item) (Item, bool) {
	var first Item
	for _, item := range c.Episodes(album.ID) {
		if item.Type() == "Photo" && (first.Path == "" || item.Path < first.Path) {
			first = item
		}
	}
	return first, first.Path != ""
}

// imageDirOf is the folder searched for an item's images.
func imageDirOf(item Item) string {
	if item.IsFolder() {
		return item.Path
	}
	return path.Dir(item.Path)
}

// openImage tries the video's own sidecars (its name plus each suffix), then
// the shared names in its folder; a folder item has only the shared names.
// present lists the image names in that folder.
func (c *Catalog) openImage(item Item, suffixes, shared []string, present map[string]bool) (*os.File, Image, error) {
	bases := []string{}
	dir := imageDirOf(item)
	if !item.IsFolder() {
		stem := strings.TrimSuffix(item.Path, path.Ext(item.Path))
		for _, suffix := range suffixes {
			bases = append(bases, stem+suffix)
		}
	}
	for _, name := range shared {
		bases = append(bases, path.Join(dir, name))
	}
	for _, base := range bases {
		for _, ext := range []string{".jpg", ".jpeg", ".png"} {
			if !present[path.Base(base+ext)] {
				continue
			}
			file, err := c.Open(Item{Path: base + ext, src: c.sourceOf(item)})
			if err != nil {
				continue
			}
			info, err := file.Stat()
			if err != nil || info.Size() <= 0 || info.Size() > maxImageBytes {
				_ = file.Close()
				continue
			}
			config, format, err := image.DecodeConfig(io.LimitReader(file, 1<<20))
			if err != nil || (format != "jpeg" && format != "png") || config.Width <= 0 || config.Height <= 0 || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > 32_000_000 {
				_ = file.Close()
				continue
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				_ = file.Close()
				continue
			}
			return file, Image{Revision: stableID(fmt.Sprintf("%s\x00%d\x00%d", base+ext, info.Size(), info.ModTime().UnixNano())), Width: config.Width, Height: config.Height, MIME: "image/" + format}, nil
		}
	}
	return nil, Image{}, os.ErrNotExist
}

// ImageInfo reports the metadata of the item's image of a type, if a valid
// one exists.
func (c *Catalog) ImageInfo(item Item, kind string) (Image, bool) {
	return imageInfo(c.OpenImage(item, kind))
}

// ImageInfos reports the item's valid images by type, as ImageInfo does for
// each of ImageTypes, but checks its folder once. A track or album without a
// local Primary image has its embedded cover's, if any (EmbeddedCover). A
// photo is its own Primary image, and a photo album without one has its
// first photo's.
func (c *Catalog) ImageInfos(item Item) map[string]Image {
	infos := map[string]Image{}
	if c == nil || c.rootOf(item) == nil {
		return infos
	}
	if item.Type() == "Photo" {
		infos["Primary"] = photoImage(item)
		return infos
	}
	if present := c.imageFiles(item); len(present) > 0 {
		for _, kind := range ImageTypes {
			names := imageNames[strings.ToLower(kind)]
			if info, found := imageInfo(c.openImage(item, names.own, names.shared, present)); found {
				infos[kind] = info
			}
		}
	}
	if _, local := infos["Primary"]; !local {
		if _, cover, found := c.EmbeddedCover(item); found {
			infos["Primary"] = cover
		}
		if item.Type() == "PhotoAlbum" {
			if photo, found := c.firstPhoto(item); found {
				infos["Primary"] = photoImage(photo)
			}
		}
	}
	return infos
}

// EmbeddedCover returns the track whose embedded cover is item's: a track's
// own, or for an album the first of its tracks by disc and track number that
// has one, with that cover's Image. Its revision changes with the track file.
// Without FFmpeg no cover can be read, so none is reported.
func (c *Catalog) EmbeddedCover(item Item) (Item, Image, bool) {
	if c == nil || c.extractor == nil {
		return Item{}, Image{}, false
	}
	tracks := []Item{item}
	if item.Type() == "MusicAlbum" {
		tracks = c.Episodes(item.ID)
		slices.SortFunc(tracks, func(a, b Item) int {
			return cmp.Or(cmp.Compare(max(a.Tags.Disc, 1), max(b.Tags.Disc, 1)), cmp.Compare(a.Tags.Track, b.Tags.Track), strings.Compare(a.Path, b.Path))
		})
	}
	for _, track := range tracks {
		if track.Type() == "Audio" && track.Cover != (Image{}) {
			cover := track.Cover
			cover.Revision = stableID(fmt.Sprintf("cover\x00%s\x00%d\x00%d", track.Path, track.Size, track.Modified.UnixNano()))
			return track, cover, true
		}
	}
	return Item{}, Image{}, false
}

// ExtractCover reads the embedded cover of track, which EmbeddedCover
// returned, as the file holds it: JPEG or PNG within the local image limits.
// It needs FFmpeg.
func (c *Catalog) ExtractCover(ctx context.Context, track Item) ([]byte, error) {
	file, err := c.Open(track)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := c.extractor.extractCover(ctx, file)
	if err != nil {
		return nil, err
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "image/"+format != track.Cover.MIME || config.Width <= 0 || config.Height <= 0 || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > 32_000_000 {
		return nil, errors.New("embedded cover is not a valid JPEG or PNG image")
	}
	return data, nil
}

func imageInfo(file *os.File, info Image, err error) (Image, bool) {
	if err != nil {
		return Image{}, false
	}
	_ = file.Close()
	return info, true
}

// imageDirCache keeps, per directory, the names that may be images, so that
// building an item costs one stat of its directory instead of a failed open
// per candidate name.
type imageDirCache struct {
	sync.Mutex
	dirs map[string]imageDir
}

type imageDir struct {
	modified, listed time.Time
	names            map[string]bool
}

// imageFiles lists the JPEG/PNG names in item's image folder. A listing is
// reused while the directory's mtime is unchanged, unless it was taken within
// two seconds of that mtime: a file added in the same coarse mtime tick would
// go unseen.
func (c *Catalog) imageFiles(item Item) map[string]bool {
	src, dir := c.sourceOf(item), imageDirOf(item)
	root, key := src.root, src.key+"\x00"+dir
	info, err := root.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}
	c.imageDirs.Lock()
	cached, found := c.imageDirs.dirs[key]
	c.imageDirs.Unlock()
	if found && cached.modified.Equal(info.ModTime()) && cached.listed.Sub(cached.modified) > 2*time.Second {
		return cached.names
	}
	listed := time.Now()
	file, err := root.Open(dir)
	if err != nil {
		return nil
	}
	all, err := file.Readdirnames(-1)
	_ = file.Close()
	if err != nil {
		return nil
	}
	names := map[string]bool{}
	for _, name := range all {
		switch path.Ext(name) {
		case ".jpg", ".jpeg", ".png":
			names[name] = true
		}
	}
	c.imageDirs.Lock()
	if c.imageDirs.dirs == nil {
		c.imageDirs.dirs = map[string]imageDir{}
	}
	c.imageDirs.dirs[key] = imageDir{modified: info.ModTime(), listed: listed, names: names}
	c.imageDirs.Unlock()
	return names
}
