// Package exiftest builds small JPEGs with chosen EXIF tags for tests. Only the EXIF segment is
// real: there is no image data, so the bytes suit EXIF readers, not image decoders.
package exiftest

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"math"
	"sort"
	"time"
)

// Photo lists the tags to write. An empty string or nil leaves a tag out.
type Photo struct {
	Make               string // IFD0 0x010F
	Model              string // IFD0 0x0110
	DateTimeOriginal   string // Exif IFD 0x9003, "2006:01:02 15:04:05"
	DateTime           string // IFD0 0x0132 (ModifyDate), the fallback when DateTimeOriginal is missing
	OffsetTimeOriginal string // Exif IFD 0x9011, e.g. "+01:00"
	PixelX, PixelY     uint32 // Exif IFD 0xA002 and 0xA003; zero leaves a tag out
	GPS                *GPS
	// InteropIFD adds an Interoperability IFD to the Exif IFD, as most phones write. Its tags 1 and
	// 2 have the numbers of GPSLatitudeRef and GPSLatitude.
	InteropIFD bool
	// BrokenExifIFD adds a zero-length tag to the Exif IFD, as old Sony cameras write.
	BrokenExifIFD bool
	Extra         []RawTag // added to the Exif IFD
}

// RawTag is a tag written as given, for what Photo has no field for: an id the decoder has no name
// for, or a binary type.
type RawTag struct {
	ID    uint16
	Type  uint16 // TIFF type: 2 is ASCII, 7 is undefined
	Count uint32
	Value []byte
}

// GPS is the GPS IFD. A zero Time leaves GPSDateStamp and GPSTimeStamp out.
type GPS struct {
	Lat, Lng float64
	Time     time.Time // written in UTC, seconds to the millisecond
	Hour     uint32    // when set, GPSTimeStamp's hour in place of Time's: a file can say 24, a clock cannot
	// ZeroLatDegrees writes the latitude's degrees as 0/0.
	ZeroLatDegrees bool
}

const (
	typeByte      = 1
	typeASCII     = 2
	typeLong      = 4
	typeRational  = 5
	typeUndefined = 7
)

var le = binary.LittleEndian

type entry struct {
	tag   uint16
	typ   uint16
	count uint32
	value []byte
}

// JPEG returns a JPEG holding p's tags in a little-endian TIFF APP1 segment.
func JPEG(p Photo) []byte {
	tiff := TIFF(p)
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	_ = binary.Write(&b, binary.BigEndian, uint16(2+6+len(tiff)))
	b.WriteString("Exif\x00\x00")
	b.Write(tiff)
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

// PNG returns a PNG holding p's tags in an eXIf chunk.
func PNG(p Photo) []byte {
	b := []byte("\x89PNG\r\n\x1a\n")
	b = pngChunk(b, "IHDR", make([]byte, 13))
	b = pngChunk(b, "eXIf", TIFF(p))
	return pngChunk(b, "IEND", nil)
}

func pngChunk(b []byte, name string, data []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
	start := len(b)
	b = append(b, name...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[start:]))
}

// WebP returns an extended WebP holding p's tags in an EXIF chunk.
func WebP(p Photo) []byte {
	const exifFlag = 1 << 3
	vp8x := make([]byte, 10)
	vp8x[0] = exifFlag
	body := []byte("WEBP")
	body = webpChunk(body, "VP8X", vp8x)
	body = webpChunk(body, "EXIF", TIFF(p))
	b := le.AppendUint32([]byte("RIFF"), uint32(len(body)))
	return append(b, body...)
}

func webpChunk(b []byte, name string, data []byte) []byte {
	b = append(b, name...)
	b = le.AppendUint32(b, uint32(len(data)))
	b = append(b, data...)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// HEIF returns a HEIC holding p's tags in an Exif item, laid out as phones write it: the item
// starts with the offset of the TIFF header and "Exif\0\0".
func HEIF(p Photo) []byte {
	const exifPrefix = "Exif\x00\x00"
	item := binary.BigEndian.AppendUint32(nil, uint32(len(exifPrefix)))
	item = append(item, exifPrefix...)
	item = append(item, TIFF(p)...)

	ftyp := box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic"))
	hdlr := box("hdlr", append(make([]byte, 8), []byte("pict\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")...))
	// infe is version 2: item 1, not protected, type Exif, no name.
	infe := box("infe", []byte("\x02\x00\x00\x00\x00\x01\x00\x00Exif\x00"))
	iinf := box("iinf", append([]byte{0, 0, 0, 0, 0, 1}, infe...))

	iloc := func(offset uint32) []byte {
		// Version 0, 4-byte offsets and lengths, no base offset; one item with one extent.
		v := []byte{0, 0, 0, 0, 0x44, 0x00, 0, 1, 0, 1, 0, 0, 0, 1}
		v = binary.BigEndian.AppendUint32(v, offset)
		return box("iloc", binary.BigEndian.AppendUint32(v, uint32(len(item))))
	}
	meta := func(offset uint32) []byte {
		v := append([]byte{0, 0, 0, 0}, hdlr...)
		v = append(v, iinf...)
		return box("meta", append(v, iloc(offset)...))
	}

	const mdatHeader = 8
	b := append(ftyp, meta(uint32(len(ftyp)+len(meta(0))+mdatHeader))...)
	return append(b, box("mdat", item)...)
}

func box(name string, payload []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
	b = append(b, name...)
	return append(b, payload...)
}

// PANA returns the box a Panasonic camera keeps in an MP4's moov/udta, holding p's tags at off of
// its payload (0x4080 on the DC-TZ200) as the camera frames them: FF D8 FF E1, the APP1 length,
// "Exif\0\0", then the TIFF block.
func PANA(p Photo, off int) []byte {
	tiff := TIFF(p)
	if len(tiff) > math.MaxUint16-8 {
		panic("exiftest: the tags do not fit one APP1 segment")
	}
	payload := make([]byte, off, off+12+len(tiff))
	payload = append(payload, 0xFF, 0xD8, 0xFF, 0xE1)
	payload = binary.BigEndian.AppendUint16(payload, uint16(8+len(tiff)))
	payload = append(payload, "Exif\x00\x00"...)
	return box("PANA", append(payload, tiff...))
}

// PanasonicMP4 returns the start of an MP4 laid out as a Panasonic camera writes it: ftyp, then moov
// with udta/PANA holding p's tags at 0x4080. It holds no media; only its boxes matter.
func PanasonicMP4(p Photo) []byte {
	ftyp := box("ftyp", []byte("mp42\x00\x00\x00\x01mp42avc1"))
	moov := box("moov", append(box("mvhd", make([]byte, 100)), box("udta", PANA(p, 0x4080))...))
	return append(ftyp, moov...)
}

// WithPANA returns mp4, a whole file whose last box is moov (ffmpeg's layout without faststart),
// with a udta box holding a PANA box for p added at the end of moov. Nothing before moov moves, so
// the file still plays and ffprobe still reads it. ok is false for any other layout.
func WithPANA(mp4 []byte, p Photo) (out []byte, ok bool) {
	last := -1
	for off := 0; off < len(mp4); {
		if len(mp4)-off < 8 {
			return nil, false
		}
		size := int(binary.BigEndian.Uint32(mp4[off:]))
		if size < 8 || size > len(mp4)-off {
			return nil, false
		}
		last, off = off, off+size
	}
	if last < 0 || string(mp4[last+4:last+8]) != "moov" {
		return nil, false
	}
	out = append(append([]byte{}, mp4...), box("udta", PANA(p, 0x4080))...)
	binary.BigEndian.PutUint32(out[last:], uint32(len(out)-last))
	return out, true
}

// TIFF returns the EXIF TIFF structure (header, IFD0, Exif IFD, GPS IFD) for p.
func TIFF(p Photo) []byte {
	var ifd0, exifIFD, interopIFD, gpsIFD []entry
	if p.Make != "" {
		ifd0 = append(ifd0, ascii(0x010F, p.Make))
	}
	if p.Model != "" {
		ifd0 = append(ifd0, ascii(0x0110, p.Model))
	}
	if p.DateTime != "" {
		ifd0 = append(ifd0, ascii(0x0132, p.DateTime))
	}
	if p.DateTimeOriginal != "" {
		exifIFD = append(exifIFD, ascii(0x9003, p.DateTimeOriginal))
	}
	if p.OffsetTimeOriginal != "" {
		exifIFD = append(exifIFD, ascii(0x9011, p.OffsetTimeOriginal))
	}
	if p.PixelX != 0 {
		exifIFD = append(exifIFD, entry{0xA002, typeLong, 1, le.AppendUint32(nil, p.PixelX)})
	}
	if p.PixelY != 0 {
		exifIFD = append(exifIFD, entry{0xA003, typeLong, 1, le.AppendUint32(nil, p.PixelY)})
	}
	if p.InteropIFD {
		exifIFD = append(exifIFD, entry{0xA005, typeLong, 1, make([]byte, 4)})
		interopIFD = append(interopIFD, ascii(0x0001, "R98"), entry{0x0002, typeUndefined, 4, []byte("0100")})
	}
	if p.BrokenExifIFD {
		exifIFD = append(exifIFD, entry{0x9286, typeASCII, 0, nil})
	}
	for _, raw := range p.Extra {
		exifIFD = append(exifIFD, entry{raw.ID, raw.Type, raw.Count, raw.Value})
	}
	if g := p.GPS; g != nil {
		lat := degrees(0x0002, g.Lat)
		if g.ZeroLatDegrees {
			clear(lat.value[:8])
		}
		gpsIFD = append(gpsIFD,
			entry{0x0000, typeByte, 4, []byte{2, 3, 0, 0}},
			ascii(0x0001, hemisphere(g.Lat, "N", "S")),
			lat,
			ascii(0x0003, hemisphere(g.Lng, "E", "W")),
			degrees(0x0004, g.Lng),
		)
		if !g.Time.IsZero() {
			t := g.Time.UTC()
			ms := t.Second()*1000 + t.Nanosecond()/int(time.Millisecond)
			hour := uint32(t.Hour())
			if g.Hour != 0 {
				hour = g.Hour
			}
			gpsIFD = append(gpsIFD,
				rationals(0x0007, [2]uint32{hour, 1}, [2]uint32{uint32(t.Minute()), 1}, [2]uint32{uint32(ms), 1000}),
				ascii(0x001D, t.Format("2006:01:02")),
			)
		}
	}

	// IFD0 points at the other two, which follow it in that order.
	if len(exifIFD) > 0 {
		ifd0 = append(ifd0, entry{0x8769, typeLong, 1, make([]byte, 4)})
	}
	if len(gpsIFD) > 0 {
		ifd0 = append(ifd0, entry{0x8825, typeLong, 1, make([]byte, 4)})
	}
	exifOffset := 8 + ifdSize(ifd0)
	interopOffset := exifOffset
	if len(exifIFD) > 0 {
		interopOffset += ifdSize(exifIFD)
	}
	gpsOffset := interopOffset
	if len(interopIFD) > 0 {
		gpsOffset += ifdSize(interopIFD)
	}
	for i := range exifIFD {
		if exifIFD[i].tag == 0xA005 {
			le.PutUint32(exifIFD[i].value, uint32(interopOffset))
		}
	}
	for i := range ifd0 {
		switch ifd0[i].tag {
		case 0x8769:
			le.PutUint32(ifd0[i].value, uint32(exifOffset))
		case 0x8825:
			le.PutUint32(ifd0[i].value, uint32(gpsOffset))
		}
	}

	var b bytes.Buffer
	b.WriteString("II")
	_ = binary.Write(&b, le, uint16(42))
	_ = binary.Write(&b, le, uint32(8))
	writeIFD(&b, ifd0)
	if len(exifIFD) > 0 {
		writeIFD(&b, exifIFD)
	}
	if len(interopIFD) > 0 {
		writeIFD(&b, interopIFD)
	}
	if len(gpsIFD) > 0 {
		writeIFD(&b, gpsIFD)
	}
	return b.Bytes()
}

func ascii(tag uint16, s string) entry {
	v := append([]byte(s), 0)
	return entry{tag, typeASCII, uint32(len(v)), v}
}

func rationals(tag uint16, vals ...[2]uint32) entry {
	v := make([]byte, 0, 8*len(vals))
	for _, r := range vals {
		v = le.AppendUint32(v, r[0])
		v = le.AppendUint32(v, r[1])
	}
	return entry{tag, typeRational, uint32(len(vals)), v}
}

// degrees writes |deg| as degrees, minutes and seconds (to 1/10000 s).
func degrees(tag uint16, deg float64) entry {
	deg = math.Abs(deg)
	d := math.Floor(deg)
	m := math.Floor((deg - d) * 60)
	s := ((deg-d)*60 - m) * 60
	return rationals(tag, [2]uint32{uint32(d), 1}, [2]uint32{uint32(m), 1}, [2]uint32{uint32(math.Round(s * 10000)), 10000})
}

func hemisphere(v float64, pos, neg string) string {
	if v < 0 {
		return neg
	}
	return pos
}

func ifdSize(es []entry) int {
	n := 2 + 12*len(es) + 4
	for _, e := range es {
		if len(e.value) > 4 {
			n += len(e.value) + len(e.value)%2
		}
	}
	return n
}

// writeIFD appends the IFD at b's current length (offsets count from the TIFF header, which starts
// b) followed by the values that do not fit in an entry.
func writeIFD(b *bytes.Buffer, es []entry) {
	sort.Slice(es, func(i, j int) bool { return es[i].tag < es[j].tag })
	dataOffset := b.Len() + 2 + 12*len(es) + 4
	var data bytes.Buffer
	_ = binary.Write(b, le, uint16(len(es)))
	for _, e := range es {
		_ = binary.Write(b, le, e.tag)
		_ = binary.Write(b, le, e.typ)
		_ = binary.Write(b, le, e.count)
		if len(e.value) <= 4 {
			var inline [4]byte
			copy(inline[:], e.value)
			b.Write(inline[:])
			continue
		}
		_ = binary.Write(b, le, uint32(dataOffset+data.Len()))
		data.Write(e.value)
		if len(e.value)%2 == 1 {
			data.WriteByte(0)
		}
	}
	_ = binary.Write(b, le, uint32(0))
	b.Write(data.Bytes())
}
