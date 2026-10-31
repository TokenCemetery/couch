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

// OpenImage searches local JPEG/PNG cover sidecars. Headers and encoded file
// size are bounded; pixels are never decoded or resized in the server.
func (c *Catalog) OpenImage(item Item) (*os.File, Image, error) {
	return c.openImage(item, []string{"-poster", ""}, []string{"poster", "folder"})
}

// OpenBackdrop searches local JPEG/PNG backdrop sidecars, with the same bounds
// as OpenImage.
func (c *Catalog) OpenBackdrop(item Item) (*os.File, Image, error) {
	return c.openImage(item, []string{"-fanart", "-backdrop"}, []string{"backdrop", "fanart"})
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

// PrimaryImage reports the item's cover image metadata, if a valid one exists.
func (c *Catalog) PrimaryImage(item Item) (Image, bool) {
	return imageInfo(c.OpenImage(item))
}

// BackdropImage reports the item's backdrop metadata, if a valid one exists.
func (c *Catalog) BackdropImage(item Item) (Image, bool) {
	return imageInfo(c.OpenBackdrop(item))
}

func imageInfo(file *os.File, info Image, err error) (Image, bool) {
	if err != nil {
		return Image{}, false
	}
	_ = file.Close()
	return info, true
}
