package media

import (
	"fmt"
	"image"
	_ "image/jpeg" // register JPEG for image.DecodeConfig
	_ "image/png"  // register PNG for image.DecodeConfig
	"io"
	"os"
	"path"
	"strings"
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
	"primary":  {[]string{"-poster", ""}, []string{"poster", "folder"}},
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
	return c.openImage(item, names.own, names.shared)
}

// openImage tries the video's own sidecars (its name plus each suffix), then
// the shared names in its folder; a folder item has only the shared names.
func (c *Catalog) openImage(item Item, suffixes, shared []string) (*os.File, Image, error) {
	if c == nil || c.root == nil {
		return nil, Image{}, os.ErrNotExist
	}
	bases := []string{}
	dir := path.Dir(item.Path)
	if item.IsFolder() {
		dir = item.Path
	} else {
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
			file, err := c.Open(Item{Path: base + ext})
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

func imageInfo(file *os.File, info Image, err error) (Image, bool) {
	if err != nil {
		return Image{}, false
	}
	_ = file.Close()
	return info, true
}
