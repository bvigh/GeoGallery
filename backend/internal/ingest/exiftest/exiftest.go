// Package exiftest builds small JPEGs with chosen EXIF tags for tests. Only the EXIF segment is
// real: there is no image data, so the bytes suit EXIF readers, not image decoders.
package exiftest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"sort"
	"strings"
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
	// XMP is an XMP packet, written where each format keeps it: an APP1 segment after the EXIF one
	// (JPEG, XMPSegment), IFD0's XMLPacket tag 0x02BC (TIFF), an iTXt chunk (PNG), an XMP chunk
	// (WebP) or an item of type mime (HEIF).
	XMP string
}

// withoutXMP is p for the formats that keep the XMP outside the TIFF structure.
func (p Photo) withoutXMP() Photo {
	p.XMP = ""
	return p
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

// JPEG returns a JPEG holding p's tags in a little-endian TIFF APP1 segment, followed by the XMP
// segment when p has a packet.
func JPEG(p Photo) []byte {
	tiff := TIFF(p.withoutXMP())
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	_ = binary.Write(&b, binary.BigEndian, uint16(2+6+len(tiff)))
	b.WriteString("Exif\x00\x00")
	b.Write(tiff)
	if p.XMP != "" {
		b.Write(XMPSegment(p.XMP))
	}
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

// GPanoNS is the XML namespace of Google's Photo Sphere properties (GPano).
const GPanoNS = "http://ns.google.com/photos/1.0/panorama/"

// XMPPacket wraps descriptions (rdf:Description elements) in the frame every writer puts round them.
func XMPPacket(descriptions string) string {
	return "<?xpacket begin=\"\uFEFF\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>\n" +
		`<x:xmpmeta xmlns:x="adobe:ns:meta/">` + "\n" +
		` <rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` + "\n" +
		"  " + descriptions + "\n" +
		" </rdf:RDF>\n</x:xmpmeta>\n" +
		`<?xpacket end="w"?>`
}

// GPanoPacket returns an XMP packet as Insta360 Studio writes one: the GPano properties as
// attributes of an rdf:Description. Each prop is "Name=value", the value written as it is.
func GPanoPacket(props ...string) string {
	var attrs strings.Builder
	for _, prop := range props {
		name, value, _ := strings.Cut(prop, "=")
		fmt.Fprintf(&attrs, "\n    GPano:%s=\"%s\"", name, value)
	}
	return XMPPacket(`<rdf:Description rdf:about="" xmlns:GPano="` + GPanoNS + `"` + attrs.String() + `/>`)
}

// XMPSegment returns the JPEG APP1 segment that holds an XMP packet.
func XMPSegment(packet string) []byte {
	const header = "http://ns.adobe.com/xap/1.0/\x00"
	if 2+len(header)+len(packet) > math.MaxUint16 {
		panic("exiftest: the packet does not fit one APP1 segment")
	}
	b := binary.BigEndian.AppendUint16([]byte{0xFF, 0xE1}, uint16(2+len(header)+len(packet)))
	b = append(b, header...)
	return append(b, packet...)
}

// PNG returns a PNG holding p's tags in an eXIf chunk, and p's XMP in an uncompressed iTXt chunk
// with the keyword XMP uses.
func PNG(p Photo) []byte {
	b := []byte("\x89PNG\r\n\x1a\n")
	b = pngChunk(b, "IHDR", make([]byte, 13))
	b = pngChunk(b, "eXIf", TIFF(p.withoutXMP()))
	if p.XMP != "" {
		// The keyword, then no compression, no language and no translated keyword.
		b = pngChunk(b, "iTXt", append([]byte("XML:com.adobe.xmp\x00\x00\x00\x00\x00"), p.XMP...))
	}
	return pngChunk(b, "IEND", nil)
}

func pngChunk(b []byte, name string, data []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
	start := len(b)
	b = append(b, name...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[start:]))
}

// WebP returns an extended WebP holding p's tags in an EXIF chunk, and p's XMP in an XMP chunk.
func WebP(p Photo) []byte {
	const exifFlag, xmpFlag = 1 << 3, 1 << 2
	vp8x := make([]byte, 10)
	vp8x[0] = exifFlag
	if p.XMP != "" {
		vp8x[0] |= xmpFlag
	}
	body := []byte("WEBP")
	body = webpChunk(body, "VP8X", vp8x)
	body = webpChunk(body, "EXIF", TIFF(p.withoutXMP()))
	if p.XMP != "" {
		body = webpChunk(body, "XMP ", []byte(p.XMP))
	}
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

// heifItem is one item of a HEIF file: its type, what follows the type in its infe box (its name,
// and for a mime item the content type), and its data.
type heifItem struct {
	typ, info string
	data      []byte
}

// HEIF returns a HEIC holding p's tags in an Exif item, laid out as phones write it: the item
// starts with the offset of the TIFF header and "Exif\0\0". p's XMP is a second item, of type mime
// with XMP's content type.
func HEIF(p Photo) []byte {
	const exifPrefix = "Exif\x00\x00"
	exif := binary.BigEndian.AppendUint32(nil, uint32(len(exifPrefix)))
	exif = append(exif, exifPrefix...)
	exif = append(exif, TIFF(p.withoutXMP())...)
	items := []heifItem{{typ: "Exif", info: "\x00", data: exif}}
	if p.XMP != "" {
		items = append(items, heifItem{typ: "mime", info: "\x00application/rdf+xml\x00", data: []byte(p.XMP)})
	}

	ftyp := box("ftyp", []byte("heic\x00\x00\x00\x00mif1heic"))
	hdlr := box("hdlr", append(make([]byte, 8), []byte("pict\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00")...))
	iinf := binary.BigEndian.AppendUint16([]byte{0, 0, 0, 0}, uint16(len(items)))
	for i, item := range items {
		// infe version 2: the item's ID, not protected, its type, then its name and content type.
		infe := binary.BigEndian.AppendUint16([]byte{2, 0, 0, 0}, uint16(i+1))
		infe = append(infe, 0, 0)
		infe = append(infe, item.typ...)
		iinf = append(iinf, box("infe", append(infe, item.info...))...)
	}

	iloc := func(offset uint32) []byte {
		// Version 0, 4-byte offsets and lengths, no base offset; one extent per item, in item order.
		v := binary.BigEndian.AppendUint16([]byte{0, 0, 0, 0, 0x44, 0x00}, uint16(len(items)))
		for i, item := range items {
			v = binary.BigEndian.AppendUint16(v, uint16(i+1))
			v = append(v, 0, 0, 0, 1) // data reference 0, one extent
			v = binary.BigEndian.AppendUint32(v, offset)
			v = binary.BigEndian.AppendUint32(v, uint32(len(item.data)))
			offset += uint32(len(item.data))
		}
		return box("iloc", v)
	}
	meta := func(offset uint32) []byte {
		v := append([]byte{0, 0, 0, 0}, hdlr...)
		v = append(v, box("iinf", iinf)...)
		return box("meta", append(v, iloc(offset)...))
	}

	var mdat []byte
	for _, item := range items {
		mdat = append(mdat, item.data...)
	}
	const mdatHeader = 8
	b := append(ftyp, meta(uint32(len(ftyp)+len(meta(0))+mdatHeader))...)
	return append(b, box("mdat", mdat)...)
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
	if p.XMP != "" {
		ifd0 = append(ifd0, entry{0x02BC, typeByte, uint32(len(p.XMP)), []byte(p.XMP)})
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
