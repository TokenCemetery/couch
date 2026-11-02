package media

import (
	"encoding/binary"
	"errors"
	"image"
	_ "image/gif" // register GIF for image.DecodeConfig
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	_ "golang.org/x/image/bmp"  // register BMP for image.DecodeConfig
	_ "golang.org/x/image/webp" // register WebP for image.DecodeConfig
)

const maxPhotos = 50000

// maxPhotoBytes bounds a photo served as its own image. Its header alone is
// read: the pixels are never decoded in the server.
const maxPhotoBytes = 64 << 20

// Photo is what a scan reads from a picture file.
type Photo struct {
	// Width and Height are as displayed, after the EXIF orientation.
	Width  int `json:",omitempty"`
	Height int `json:",omitempty"`
	// Taken is the EXIF DateTimeOriginal; zero when the file has none.
	Taken time.Time `json:",omitzero"`
}

// isPhoto reports whether a file extension, in lower case, is a picture's.
func isPhoto(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return true
	}
	return false
}

// isImageSidecar reports whether a picture's name is one of the shared names
// that hold a folder's images, such as folder.jpg, rather than a photo.
func isImageSidecar(file string) bool {
	stem := strings.ToLower(strings.TrimSuffix(path.Base(file), path.Ext(file)))
	for _, names := range imageNames {
		for _, name := range names.shared {
			if stem == name {
				return true
			}
		}
	}
	return false
}

// Date is when a photo was taken, else when its file was last modified.
func (i Item) Date() time.Time {
	if !i.Photo.Taken.IsZero() {
		return i.Photo.Taken
	}
	return i.Modified
}

// readPhoto checks that file is a picture of a supported format and reads its
// dimensions and, from a JPEG's EXIF, when it was taken.
func readPhoto(file *os.File) (Photo, error) {
	config, format, err := image.DecodeConfig(io.LimitReader(file, 1<<20))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 65535 || config.Height > 65535 {
		return Photo{}, errors.New("not a supported picture")
	}
	photo := Photo{Width: config.Width, Height: config.Height}
	if format != "jpeg" {
		return photo, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Photo{}, err
	}
	taken, orientation := jpegExif(file)
	photo.Taken = taken
	// Orientations 5 to 8 turn the picture a quarter.
	if orientation >= 5 && orientation <= 8 {
		photo.Width, photo.Height = photo.Height, photo.Width
	}
	return photo, nil
}

// EXIF tags read from a JPEG.
const (
	tagOrientation        = 0x0112
	tagExifIFD            = 0x8769
	tagDateTimeOriginal   = 0x9003
	tagOffsetTimeOriginal = 0x9011
)

// jpegExif returns the DateTimeOriginal and the orientation in a JPEG's EXIF
// segment, or zero values. A date without an OffsetTimeOriginal is read as
// UTC, since EXIF does not say its zone.
func jpegExif(r io.Reader) (time.Time, int) {
	segment := exifSegment(r)
	if len(segment) < 8 {
		return time.Time{}, 0
	}
	var order binary.ByteOrder
	switch string(segment[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return time.Time{}, 0
	}
	if order.Uint16(segment[2:]) != 42 {
		return time.Time{}, 0
	}
	ifd0 := readIFD(segment, order, order.Uint32(segment[4:]))
	orientation := 0
	if entry, ok := ifd0[tagOrientation]; ok {
		orientation = int(order.Uint16(entry.value))
	}
	exif, ok := ifd0[tagExifIFD]
	if !ok {
		return time.Time{}, orientation
	}
	tags := readIFD(segment, order, order.Uint32(exif.value))
	taken := exifString(segment, order, tags[tagDateTimeOriginal])
	zone := time.UTC
	if offset, err := time.Parse("-07:00", exifString(segment, order, tags[tagOffsetTimeOriginal])); err == nil {
		zone = offset.Location()
	}
	date, err := time.ParseInLocation("2006:01:02 15:04:05", taken, zone)
	if err != nil {
		return time.Time{}, orientation
	}
	return date, orientation
}

// exifSegment returns the TIFF data of a JPEG's EXIF APP1 segment, reading
// no further than the segments before the image data.
func exifSegment(r io.Reader) []byte {
	var marker [4]byte
	if _, err := io.ReadFull(r, marker[:2]); err != nil || marker[0] != 0xFF || marker[1] != 0xD8 {
		return nil
	}
	for range 32 {
		if _, err := io.ReadFull(r, marker[:]); err != nil || marker[0] != 0xFF {
			return nil
		}
		// Start of scan, end of image: no EXIF before the pixels.
		if marker[1] == 0xDA || marker[1] == 0xD9 {
			return nil
		}
		length := int(binary.BigEndian.Uint16(marker[2:]))
		if length < 2 {
			return nil
		}
		data := make([]byte, length-2)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil
		}
		if marker[1] == 0xE1 && len(data) > 6 && string(data[:6]) == "Exif\x00\x00" {
			return data[6:]
		}
	}
	return nil
}

type ifdEntry struct {
	kind  uint16
	count uint32
	// value holds the value when it fits in four bytes, else its offset.
	value []byte
}

// readIFD reads the entries of the image file directory at offset in a TIFF
// segment; entries past its end are left out.
func readIFD(segment []byte, order binary.ByteOrder, offset uint32) map[uint16]ifdEntry {
	entries := map[uint16]ifdEntry{}
	if uint64(offset)+2 > uint64(len(segment)) {
		return entries
	}
	count := int(order.Uint16(segment[offset:]))
	for n := range count {
		at := uint64(offset) + 2 + uint64(n)*12
		if at+12 > uint64(len(segment)) {
			break
		}
		e := segment[at : at+12]
		entries[order.Uint16(e)] = ifdEntry{kind: order.Uint16(e[2:]), count: order.Uint32(e[4:]), value: e[8:12]}
	}
	return entries
}

// exifString returns an ASCII entry's text without its terminating NUL.
func exifString(segment []byte, order binary.ByteOrder, entry ifdEntry) string {
	const ascii = 2
	if entry.kind != ascii || entry.count == 0 || entry.count > 64 {
		return ""
	}
	data := entry.value
	if entry.count > 4 {
		offset := uint64(order.Uint32(entry.value))
		if offset+uint64(entry.count) > uint64(len(segment)) {
			return ""
		}
		data = segment[offset : offset+uint64(entry.count)]
	}
	return strings.TrimRight(string(data[:min(int(entry.count), len(data))]), "\x00 ")
}

// classifyPhotos marks the folder's pictures as photos and, when a home
// videos library uses the folder, makes each directory that holds a photo or
// video a photo album inside the album of the directory above it. Photos and
// albums at the top of the folder have no parent until assemble sets the
// library.
func (s *source) classifyPhotos() {
	if !s.photo {
		return
	}
	albums := map[string]Item{}
	for n := range s.items {
		item := &s.items[n]
		ext := strings.ToLower(path.Ext(item.Path))
		if isAudio(ext) {
			continue
		}
		dir := path.Dir(item.Path)
		if isPhoto(ext) {
			item.Kind, item.ParentID = "Photo", s.albumID(dir)
		}
		for ; dir != "."; dir = path.Dir(dir) {
			id := s.albumID(dir)
			album, exists := albums[id]
			if !exists {
				album = Item{ID: id, Name: path.Base(dir), Kind: "PhotoAlbum", Path: dir, ParentID: s.albumID(path.Dir(dir)), src: s}
			}
			album.Modified = later(album.Modified, item.Modified)
			album.Added = later(album.Added, item.Added)
			albums[id] = album
		}
	}
	for _, album := range albums {
		s.folders = append(s.folders, album)
	}
}

// albumID is the ID of the photo album of a directory in the folder, or ""
// for the folder itself.
func (s *source) albumID(dir string) string {
	if dir == "." {
		return ""
	}
	return stableID(s.key + "\x00photoalbum\x00" + dir)
}

// homeVideo is a video as a home videos library lists it: a plain video in
// the photo album of its directory, even where a TV shows library would see
// an episode. Its ID differs from the movie's or episode's that a library of
// another type makes of the same file, so both are listed.
func homeVideo(item Item, library string) Item {
	video := Item{ID: stableID("video\x00" + item.ID), Name: item.Name, Kind: "Video", Path: item.Path, Container: item.Container, Size: item.Size,
		Modified: item.Modified, Added: item.Added, RunTimeTicks: item.RunTimeTicks, Streams: item.Streams, src: item.src}
	video.ParentID = item.src.albumID(path.Dir(item.Path))
	if video.ParentID == "" {
		video.ParentID = library
	}
	return video
}

// dropVideoImages leaves out the pictures that are a video's own images,
// such as Clip-poster.jpg next to Clip.mp4, which are not photos.
func dropVideoImages(items []Item) []Item {
	stems := map[string]bool{}
	for _, item := range items {
		if isVideo(strings.ToLower(path.Ext(item.Path))) {
			stems[strings.ToLower(strings.TrimSuffix(item.Path, path.Ext(item.Path)))] = true
		}
	}
	if len(stems) == 0 {
		return items
	}
	return slices.DeleteFunc(items, func(item Item) bool {
		if !isPhoto(strings.ToLower(path.Ext(item.Path))) {
			return false
		}
		stem := strings.ToLower(strings.TrimSuffix(item.Path, path.Ext(item.Path)))
		for _, names := range imageNames {
			for _, suffix := range names.own {
				if base, found := strings.CutSuffix(stem, suffix); found && stems[base] {
					return true
				}
			}
		}
		return false
	})
}
