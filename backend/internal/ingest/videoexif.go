package ingest

import (
	"bytes"
	"encoding/binary"
)

// panaExifOffsets are where a PANA box (moov/udta/PANA) holds the camera's EXIF, from the start of
// the box's payload, as ExifTool reads it (Image::ExifTool::Panasonic::PANA): 0x10 on the DC-FT7,
// 0x4068, and 0x4080 on the FZ1000 and DC-TZ200. The GH6 puts it at 0x200080, past any head.
var panaExifOffsets = []int{0x10, 0x4068, 0x4080}

// VideoExif reads the EXIF a camera keeps inside a video, within head, the start of the file, and
// returns it when the camera rules date the make's videos from it (CameraRule.VideoTimeFromExif) and
// it holds a capture time. That time is read like a photo's, with zone. Only Panasonic's PANA box is
// read, and only when it lies within head: Panasonic writes moov before the media data. The position
// and the dimensions are left out, because a video's location comes from its container tag and its
// size from ffprobe. Decoding costs what ExtractExif costs, so callers take an EXIF decode slot.
func VideoExif(head []byte, zone CameraZone) (ExifData, bool) {
	if !zone.Rules.DatesVideosFromExif() {
		return ExifData{}, false
	}
	tiff, ok := panasonicVideoExif(head)
	if !ok {
		return ExifData{}, false
	}
	e := ExtractExif(bytes.NewReader(tiff), zone)
	if e.TakenAt == nil || !zone.Rules.For(e.Raw.Make).VideoTimeFromExif {
		return ExifData{}, false
	}
	e.Lat, e.Lng, e.WidthPx, e.HeightPx = nil, nil, nil, nil
	return e, true
}

// DatesVideosFromExif reports whether any rule dates videos from their EXIF, so that nothing is read
// for it when none does.
func (rs CameraRules) DatesVideosFromExif() bool {
	for _, rule := range rs {
		if rule.VideoTimeFromExif {
			return true
		}
	}
	return false
}

// panasonicVideoExif returns the TIFF block of the EXIF in a PANA box within head.
func panasonicVideoExif(head []byte) ([]byte, bool) {
	pana, ok := findBox(head, "moov", "udta", "PANA")
	if !ok {
		return nil, false
	}
	for _, off := range panaExifOffsets {
		if tiff, ok := exifSegmentAt(pana, off); ok {
			return tiff, true
		}
	}
	return nil, false
}

// exifSegmentAt returns the TIFF block of the JPEG-style EXIF segment at b[off:]: FF D8 FF E1, the
// APP1 length (which counts its own two bytes, "Exif\0\0" and the TIFF block), "Exif\0\0", then the
// TIFF block. A segment cut short by the end of b is not read.
func exifSegmentAt(b []byte, off int) ([]byte, bool) {
	const header = 12 // FF D8 FF E1, the length, "Exif\0\0"
	if off < 0 || off > len(b) || len(b)-off < header {
		return nil, false
	}
	seg := b[off:]
	if !bytes.HasPrefix(seg, []byte{0xFF, 0xD8, 0xFF, 0xE1}) || string(seg[6:header]) != "Exif\x00\x00" {
		return nil, false
	}
	end := 4 + int(binary.BigEndian.Uint16(seg[4:6]))
	if end <= header || end > len(seg) {
		return nil, false
	}
	return seg[header:end], true
}

// findBox returns the payload of the box at path, each name a child of the one before, in ISO base
// media (MP4, QuickTime) data. Every box of a name is searched, since a file may hold two udta boxes.
// A box that runs past the end of data is cut to what is there.
func findBox(data []byte, path ...string) ([]byte, bool) {
	if len(path) == 0 {
		return data, true
	}
	var found []byte
	ok := false
	eachBox(data, func(name string, payload []byte) bool {
		if name == path[0] {
			found, ok = findBox(payload, path[1:]...)
		}
		return !ok
	})
	return found, ok
}

// eachBox calls visit with the name and payload of each box in data, in order, until visit returns
// false. It stops at a size that cannot be right, and after a box that runs past the end of data.
func eachBox(data []byte, visit func(name string, payload []byte) bool) {
	for off := 0; len(data)-off >= 8; {
		avail := uint64(len(data) - off)
		size := uint64(binary.BigEndian.Uint32(data[off:]))
		name := string(data[off+4 : off+8])
		header := uint64(8)
		switch size {
		case 0: // the box runs to the end of the file
			size = avail
		case 1: // a 64-bit size follows the name
			if avail < 16 {
				return
			}
			size, header = binary.BigEndian.Uint64(data[off+8:]), 16
		}
		if size < header {
			return
		}
		if size >= avail {
			visit(name, data[off+int(header):])
			return
		}
		if !visit(name, data[off+int(header):off+int(size)]) {
			return
		}
		off += int(size)
	}
}
