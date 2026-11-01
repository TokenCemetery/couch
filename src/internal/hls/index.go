package hls

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// errNoIndex means the file has no index the keyframes can be read from.
var errNoIndex = errors.New("no keyframe index")

// maxIndexBytes caps an index element or box read into memory.
const maxIndexBytes = 64 << 20

// maxIndexElements caps the elements or boxes walked at one level; real
// files have a handful, a hostile one could have millions of tiny ones.
const maxIndexElements = 1024

// indexKeyframes reads the presentation times in seconds of one video
// stream's keyframes from the container's index: the Cues of Matroska and
// WebM, or the sample tables of MP4 and MOV. It reads the headers and the
// index, not the packets. stream counts streams as FFmpeg does.
func indexKeyframes(r *io.SectionReader, stream int) ([]float64, error) {
	var head [8]byte
	if _, err := r.ReadAt(head[:], 0); err != nil {
		return nil, errNoIndex
	}
	switch {
	case binary.BigEndian.Uint32(head[:4]) == ebmlID:
		return matroskaKeyframes(r, stream)
	case string(head[4:]) == "ftyp" || string(head[4:]) == "moov":
		return mp4Keyframes(r, stream)
	}
	return nil, errNoIndex
}

const (
	ebmlID           = 0x1A45DFA3
	segmentID        = 0x18538067
	seekHeadID       = 0x114D9B74
	seekID           = 0x4DBB
	seekIDID         = 0x53AB
	seekPositionID   = 0x53AC
	infoID           = 0x1549A966
	timestampScaleID = 0x2AD7B1
	tracksID         = 0x1654AE6B
	trackEntryID     = 0xAE
	trackNumberID    = 0xD7
	trackTypeID      = 0x83
	codecIDID        = 0x86
	trackOffsetID    = 0x537F
	clusterID        = 0x1F43B675
	cuesID           = 0x1C53BB6B
	cuePointID       = 0xBB
	cueTimeID        = 0xB3
	cueTrackPosID    = 0xB7
	cueTrackID       = 0xF7
	matroskaVideo    = 1
	matroskaAudio    = 2
	matroskaSubtitle = 0x11
	matroskaMetadata = 0x21
	defaultTimeScale = 1000000
)

// unknownSize is the size vint gives for an element of unknown size.
const unknownSize uint64 = math.MaxUint64

// vint decodes an EBML variable-length integer; marker keeps the length
// marker bit, as element IDs do.
func vint(b []byte, marker bool) (v uint64, n int, ok bool) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0, false
	}
	n = 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if len(b) < n {
		return 0, 0, false
	}
	v = uint64(b[0])
	if !marker {
		v &= 0xFF >> n
	}
	all := v == 0xFF>>n
	for _, c := range b[1:n] {
		v = v<<8 | uint64(c)
		all = all && c == 0xFF
	}
	if !marker && all {
		return unknownSize, n, true
	}
	return v, n, true
}

// ebmlElement splits the element at the start of b into its ID, data and the
// bytes after it.
func ebmlElement(b []byte) (id uint32, data, rest []byte, ok bool) {
	v, n, ok := vint(b, true)
	if !ok || n > 4 {
		return 0, nil, nil, false
	}
	size, m, ok := vint(b[n:], false)
	if !ok || size > uint64(len(b)-n-m) { //nolint:gosec // a length is not negative
		return 0, nil, nil, false
	}
	return uint32(v), b[n+m : n+m+int(size)], b[n+m+int(size):], true //nolint:gosec // an ID has at most 4 bytes
}

// ebmlAt reads the header of the element at off: its ID, where its data
// starts and its size, or -1 if unknown.
func ebmlAt(r *io.SectionReader, off int64) (id uint32, data, size int64, err error) {
	var b [12]byte
	n, _ := r.ReadAt(b[:], off)
	v, idLen, ok := vint(b[:n], true)
	if !ok || idLen > 4 {
		return 0, 0, 0, errNoIndex
	}
	s, sizeLen, ok := vint(b[idLen:n], false)
	if !ok || (s != unknownSize && s > uint64(r.Size())) { //nolint:gosec // a size is not negative
		return 0, 0, 0, errNoIndex
	}
	data = off + int64(idLen+sizeLen)
	if s == unknownSize {
		return uint32(v), data, -1, nil //nolint:gosec // an ID has at most 4 bytes
	}
	return uint32(v), data, int64(s), nil //nolint:gosec // as above, and s is below the file size
}

// readAt reads size bytes at off, at most maxIndexBytes.
func readAt(r *io.SectionReader, off, size int64) ([]byte, error) {
	if size < 0 || size > maxIndexBytes || off+size > r.Size() {
		return nil, errNoIndex
	}
	b := make([]byte, size)
	if _, err := r.ReadAt(b, off); err != nil {
		return nil, errNoIndex
	}
	return b, nil
}

func ebmlUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// matroskaKeyframes reads the Cues of the stream's track. The Info, Tracks
// and Cues elements are found among the Segment's children before the first
// Cluster, or through the SeekHead, as Cues are usually at the end.
func matroskaKeyframes(r *io.SectionReader, stream int) ([]float64, error) {
	_, data, size, err := ebmlAt(r, 0)
	if err != nil || size < 0 {
		return nil, errNoIndex
	}
	id, segment, size, err := ebmlAt(r, data+size)
	if err != nil || id != segmentID {
		return nil, errNoIndex
	}
	end := r.Size()
	if size >= 0 {
		end = min(end, segment+size)
	}
	bodies := map[uint32][]byte{}
	positions := map[uint32]int64{}
	seekHeads := map[int64]bool{}
	readSeekHead := func(at int64, body []byte) {
		seekHeads[at] = true
		for b := body; len(b) > 0; {
			id, entry, rest, ok := ebmlElement(b)
			if !ok {
				return
			}
			b = rest
			if id != seekID {
				continue
			}
			var target uint32
			position := int64(-1)
			for e := entry; len(e) > 0; {
				id, value, rest, ok := ebmlElement(e)
				if !ok {
					break
				}
				e = rest
				switch {
				case id == seekIDID && len(value) <= 4:
					target = uint32(ebmlUint(value)) //nolint:gosec // at most 4 bytes
				case id == seekPositionID && len(value) <= 7:
					position = segment + int64(ebmlUint(value)) //nolint:gosec // at most 7 bytes
				}
			}
			if _, seen := positions[target]; !seen && position >= 0 {
				positions[target] = position
			}
		}
	}
	for at, n := segment, 0; at < end; n++ {
		if n == maxIndexElements {
			return nil, errNoIndex
		}
		id, data, size, err := ebmlAt(r, at)
		if err != nil || size < 0 || id == clusterID {
			break
		}
		switch id {
		case seekHeadID, infoID, tracksID, cuesID:
			body, err := readAt(r, data, size)
			if err != nil {
				return nil, err
			}
			if id == seekHeadID {
				readSeekHead(at, body)
			} else if bodies[id] == nil {
				bodies[id] = body
			}
		}
		at = data + size
	}
	if at, ok := positions[seekHeadID]; ok && !seekHeads[at] {
		if id, data, size, err := ebmlAt(r, at); err == nil && id == seekHeadID {
			if body, err := readAt(r, data, size); err == nil {
				readSeekHead(at, body)
			}
		}
	}
	for _, want := range []uint32{infoID, tracksID, cuesID} {
		at, ok := positions[want]
		if bodies[want] != nil || !ok {
			continue
		}
		id, data, size, err := ebmlAt(r, at)
		if err != nil || id != want {
			return nil, errNoIndex
		}
		if bodies[want], err = readAt(r, data, size); err != nil {
			return nil, err
		}
	}
	if bodies[tracksID] == nil || bodies[cuesID] == nil {
		return nil, errNoIndex
	}
	scale := uint64(defaultTimeScale)
	for b := bodies[infoID]; len(b) > 0; {
		id, value, rest, ok := ebmlElement(b)
		if !ok {
			return nil, errNoIndex
		}
		b = rest
		if id == timestampScaleID {
			scale = ebmlUint(value)
		}
	}
	track, err := matroskaTrack(bodies[tracksID], stream)
	if err != nil || scale == 0 {
		return nil, errNoIndex
	}
	var times []float64
	for b := bodies[cuesID]; len(b) > 0; {
		id, point, rest, ok := ebmlElement(b)
		if !ok {
			return nil, errNoIndex
		}
		b = rest
		if id != cuePointID {
			continue
		}
		var cueTime uint64
		hasTime, hasTrack := false, false
		for p := point; len(p) > 0; {
			id, value, rest, ok := ebmlElement(p)
			if !ok {
				return nil, errNoIndex
			}
			p = rest
			switch id {
			case cueTimeID:
				cueTime, hasTime = ebmlUint(value), true
			case cueTrackPosID:
				for q := value; len(q) > 0; {
					id, value, rest, ok := ebmlElement(q)
					if !ok {
						return nil, errNoIndex
					}
					q = rest
					if id == cueTrackID && ebmlUint(value) == track {
						hasTrack = true
					}
				}
			}
		}
		if hasTime && hasTrack {
			times = append(times, float64(cueTime)*float64(scale)/1e9)
		}
	}
	return times, nil
}

// matroskaTrack returns the number of the track that is FFmpeg's stream
// index stream, which must be video. FFmpeg makes a stream of each track
// with a codec of a type it reads, in order.
func matroskaTrack(tracks []byte, stream int) (uint64, error) {
	index := 0
	for b := tracks; len(b) > 0; {
		id, entry, rest, ok := ebmlElement(b)
		if !ok {
			return 0, errNoIndex
		}
		b = rest
		if id != trackEntryID {
			continue
		}
		var number, kind uint64
		codec, offset := false, false
		for e := entry; len(e) > 0; {
			id, value, rest, ok := ebmlElement(e)
			if !ok {
				return 0, errNoIndex
			}
			e = rest
			switch id {
			case trackNumberID:
				number = ebmlUint(value)
			case trackTypeID:
				kind = ebmlUint(value)
			case codecIDID:
				codec = len(value) > 0
			case trackOffsetID:
				offset = ebmlUint(value) != 0
			}
		}
		if !codec || (kind != matroskaVideo && kind != matroskaAudio && kind != matroskaSubtitle && kind != matroskaMetadata) {
			continue
		}
		if index == stream {
			if kind != matroskaVideo || offset || number == 0 {
				return 0, errNoIndex
			}
			return number, nil
		}
		index++
	}
	return 0, errNoIndex
}

// mp4Box is a box's type and where its payload is.
type mp4Box struct {
	kind      string
	off, size int64
}

// mp4Boxes reads the headers of the boxes from off to end.
func mp4Boxes(r *io.SectionReader, off, end int64) ([]mp4Box, error) {
	var boxes []mp4Box
	for off+8 <= end {
		if len(boxes) == maxIndexElements {
			return nil, errNoIndex
		}
		var b [16]byte
		if _, err := r.ReadAt(b[:8], off); err != nil {
			return nil, errNoIndex
		}
		size, header := int64(binary.BigEndian.Uint32(b[:4])), int64(8)
		switch size {
		case 0:
			size = end - off
		case 1:
			if _, err := r.ReadAt(b[8:], off+8); err != nil {
				return nil, errNoIndex
			}
			large := binary.BigEndian.Uint64(b[8:])
			if large > math.MaxInt64 {
				return nil, errNoIndex
			}
			size, header = int64(large), 16
		}
		if size < header || size > end-off {
			return nil, errNoIndex
		}
		boxes = append(boxes, mp4Box{string(b[4:8]), off + header, size - header})
		off += size
	}
	return boxes, nil
}

// mp4Child returns the first box of kind among boxes.
func mp4Child(boxes []mp4Box, kind string) (mp4Box, bool) {
	for _, b := range boxes {
		if b.kind == kind {
			return b, true
		}
	}
	return mp4Box{}, false
}

// mp4Path follows kinds down from the payload of box.
func mp4Path(r *io.SectionReader, box mp4Box, kinds ...string) (mp4Box, bool) {
	for _, kind := range kinds {
		children, err := mp4Boxes(r, box.off, box.off+box.size)
		if err != nil {
			return mp4Box{}, false
		}
		if box, _ = mp4Child(children, kind); box.kind != kind {
			return mp4Box{}, false
		}
	}
	return box, true
}

// mp4Read reads a box's payload; it must hold at least n bytes.
func mp4Read(r *io.SectionReader, box mp4Box, n int) ([]byte, error) {
	b, err := readAt(r, box.off, box.size)
	if err != nil || len(b) < n {
		return nil, errNoIndex
	}
	return b, nil
}

// mp4Table reads a full box with a 32-bit entry count and entries of size
// bytes each from its payload.
func mp4Table(r *io.SectionReader, box mp4Box, size int) ([]byte, int, error) {
	b, err := mp4Read(r, box, 8)
	if err != nil {
		return nil, 0, err
	}
	count := int(binary.BigEndian.Uint32(b[4:8]))
	if count > (len(b)-8)/size {
		return nil, 0, errNoIndex
	}
	return b[8:], count, nil
}

// mp4Timescale reads the time scale of an mvhd or mdhd box.
func mp4Timescale(r *io.SectionReader, box mp4Box) (int64, error) {
	b, err := mp4Read(r, box, 24)
	if err != nil {
		return 0, err
	}
	at := 12
	if b[0] == 1 {
		at = 20
	}
	scale := int64(binary.BigEndian.Uint32(b[at : at+4]))
	if scale == 0 {
		return 0, errNoIndex
	}
	return scale, nil
}

// mp4Keyframes reads the sync samples of the stream's track and gives their
// presentation times as FFmpeg does: decode time plus composition offset,
// moved by the edit list. Fragmented files, edit lists other than one edit
// after optional empty ones, and tracks whose sample groups add random access
// points are not read.
func mp4Keyframes(r *io.SectionReader, stream int) ([]float64, error) {
	top, err := mp4Boxes(r, 0, r.Size())
	if err != nil {
		return nil, err
	}
	moovBox, ok := mp4Child(top, "moov")
	if !ok {
		return nil, errNoIndex
	}
	moov, err := mp4Boxes(r, moovBox.off, moovBox.off+moovBox.size)
	if err != nil {
		return nil, err
	}
	if _, fragmented := mp4Child(moov, "mvex"); fragmented {
		return nil, errNoIndex
	}
	mvhd, ok := mp4Child(moov, "mvhd")
	if !ok {
		return nil, errNoIndex
	}
	movieScale, err := mp4Timescale(r, mvhd)
	if err != nil {
		return nil, err
	}
	var trak mp4Box
	index := 0
	for _, b := range moov {
		if b.kind == "trak" {
			if index == stream {
				trak = b
			}
			index++
		}
	}
	if trak.kind == "" {
		return nil, errNoIndex
	}
	hdlrBox, ok := mp4Path(r, trak, "mdia", "hdlr")
	if !ok {
		return nil, errNoIndex
	}
	if hdlr, err := mp4Read(r, hdlrBox, 12); err != nil || string(hdlr[8:12]) != "vide" {
		return nil, errNoIndex
	}
	mdhd, ok := mp4Path(r, trak, "mdia", "mdhd")
	if !ok {
		return nil, errNoIndex
	}
	scale, err := mp4Timescale(r, mdhd)
	if err != nil {
		return nil, err
	}
	shift, limit, err := mp4Edits(r, trak, scale, movieScale)
	if err != nil {
		return nil, err
	}
	stbl, ok := mp4Path(r, trak, "mdia", "minf", "stbl")
	if !ok {
		return nil, errNoIndex
	}
	tables, err := mp4Boxes(r, stbl.off, stbl.off+stbl.size)
	if err != nil {
		return nil, err
	}
	if sbgp, ok := mp4Child(tables, "sbgp"); ok {
		if b, err := mp4Read(r, sbgp, 8); err != nil || string(b[4:8]) == "rap " {
			return nil, errNoIndex
		}
	}
	sttsBox, ok := mp4Child(tables, "stts")
	stssBox, hasSync := mp4Child(tables, "stss")
	if !ok || !hasSync {
		return nil, errNoIndex
	}
	stts, sttsCount, err := mp4Table(r, sttsBox, 8)
	if err != nil {
		return nil, err
	}
	stss, stssCount, err := mp4Table(r, stssBox, 4)
	if err != nil || stssCount == 0 || stssCount > maxKeyframes {
		return nil, errNoIndex
	}
	var ctts []byte
	cttsCount := 0
	if cttsBox, ok := mp4Child(tables, "ctts"); ok {
		if ctts, cttsCount, err = mp4Table(r, cttsBox, 8); err != nil {
			return nil, err
		}
	}
	times := make([]float64, 0, stssCount)
	// Samples count from 1, and stts and ctts are run-length coded. dts is
	// the decode time of sample current, in the stts run that ends before
	// sample sttsEnd.
	var dts int64
	current, prev := uint64(1), uint64(0)
	sttsEntry, sttsEnd := 0, uint64(1)
	cttsEntry, cttsStart := 0, uint64(1)
	for i := range stssCount {
		sample := uint64(binary.BigEndian.Uint32(stss[4*i:]))
		if sample <= prev {
			return nil, errNoIndex
		}
		prev = sample
		for {
			if sttsEntry >= sttsCount {
				return nil, errNoIndex
			}
			count := uint64(binary.BigEndian.Uint32(stts[8*sttsEntry:]))
			delta := int64(binary.BigEndian.Uint32(stts[8*sttsEntry+4:]))
			if current == sttsEnd {
				sttsEnd += count
			}
			if sample < sttsEnd {
				dts += int64(sample-current) * delta //nolint:gosec // a difference of 32-bit sample numbers
				current = sample
				break
			}
			dts += int64(sttsEnd-current) * delta
			current = sttsEnd
			sttsEntry++
		}
		var offset int64
		for cttsEntry < cttsCount {
			count := uint64(binary.BigEndian.Uint32(ctts[8*cttsEntry:]))
			if sample < cttsStart+count {
				offset = int64(int32(binary.BigEndian.Uint32(ctts[8*cttsEntry+4:]))) //nolint:gosec // signed in version 1, and FFmpeg reads version 0 so too
				break
			}
			cttsStart += count
			cttsEntry++
		}
		pts := dts + offset - shift
		if limit >= 0 && pts >= limit {
			continue
		}
		times = append(times, float64(pts)/float64(scale))
	}
	return times, nil
}

// mp4Edits reads the track's edit list and returns how far it moves media
// times back, in the media time scale, and where the presentation ends, or
// -1 without an end.
func mp4Edits(r *io.SectionReader, trak mp4Box, scale, movieScale int64) (shift, limit int64, err error) {
	box, ok := mp4Path(r, trak, "edts", "elst")
	if !ok {
		return 0, -1, nil
	}
	b, err := mp4Read(r, box, 8)
	if err != nil {
		return 0, 0, err
	}
	size := 12
	if b[0] == 1 {
		size = 20
	}
	count := int(binary.BigEndian.Uint32(b[4:8]))
	if count > (len(b)-8)/size {
		return 0, 0, errNoIndex
	}
	var empty int64
	for i := range count {
		e := b[8+i*size:]
		var duration uint64
		var mediaTime int64
		var rate []byte
		if size == 12 {
			duration = uint64(binary.BigEndian.Uint32(e))
			mediaTime = int64(int32(binary.BigEndian.Uint32(e[4:]))) //nolint:gosec // a signed field
			rate = e[8:12]
		} else {
			duration = binary.BigEndian.Uint64(e)
			mediaTime = int64(binary.BigEndian.Uint64(e[8:])) //nolint:gosec // a signed field
			rate = e[16:20]
		}
		if duration > math.MaxInt32*uint64(movieScale) { //nolint:gosec // a 32-bit time scale
			return 0, 0, errNoIndex
		}
		movie := int64(duration) * scale / movieScale //nolint:gosec // checked above
		if mediaTime == -1 {
			empty += movie
			continue
		}
		if i != count-1 || mediaTime < 0 || binary.BigEndian.Uint32(rate) != 1<<16 {
			return 0, 0, errNoIndex
		}
		limit = -1
		if duration > 0 {
			limit = empty + movie
		}
		return mediaTime - empty, limit, nil
	}
	return 0, 0, errNoIndex
}
