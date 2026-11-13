package ingest

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bvigh/geogallery/internal/ingest/exiftest"
)

// panasonicClip is what a Panasonic camera writes into its MP4: its wall clock, and the offset of
// its zone setting, which the Panasonic rule ignores. In February, Warsaw is at +01:00.
var panasonicClip = exiftest.Photo{
	Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2021:02:10 15:30:00", OffsetTimeOriginal: "+01:00",
}

// mp4Box builds an ISO media box from its payload parts.
func mp4Box(name string, parts ...[]byte) []byte {
	var payload []byte
	for _, p := range parts {
		payload = append(payload, p...)
	}
	b := binary.BigEndian.AppendUint32(nil, uint32(8+len(payload)))
	return append(append(b, name...), payload...)
}

var ftyp = mp4Box("ftyp", []byte("mp42\x00\x00\x00\x01mp42avc1"))

func TestVideoExif(t *testing.T) {
	warsaw := CameraZone{Loc: mustZone(t, "Europe/Warsaw"), Set: true, Rules: panasonicRules}

	t.Run("a Panasonic MP4 is dated from its EXIF, read like a photo's", func(t *testing.T) {
		got, ok := VideoExif(exiftest.PanasonicMP4(panasonicClip), warsaw)
		require.True(t, ok)
		require.NotNil(t, got.TakenAt)
		assert.Equal(t, "2021-02-10T14:30:00Z", got.TakenAt.UTC().Format(time.RFC3339))
		assert.Equal(t, ZoneCamera, got.TakenAtZone)
		assert.Equal(t, RawExif{WallClock: "2021:02:10 15:30:00", WallTag: WallTagOriginal, OffsetTag: "+01:00", OffsetIgnored: true, Make: "Panasonic"}, got.Raw)
		assert.Equal(t, "DC-TZ200", got.CameraModel)
	})

	t.Run("every offset ExifTool knows inside the box", func(t *testing.T) {
		for _, off := range panaExifOffsets {
			head := append(append([]byte{}, ftyp...), mp4Box("moov", mp4Box("udta", exiftest.PANA(panasonicClip, off)))...)
			_, ok := VideoExif(head, warsaw)
			assert.True(t, ok, "offset %#x", off)
		}
		head := append(append([]byte{}, ftyp...), mp4Box("moov", mp4Box("udta", exiftest.PANA(panasonicClip, 0x100)))...)
		_, ok := VideoExif(head, warsaw)
		assert.False(t, ok, "anywhere else is not read")
	})

	t.Run("the position and the dimensions are left out", func(t *testing.T) {
		clip := panasonicClip
		clip.GPS = &exiftest.GPS{Lat: 10, Lng: 20}
		clip.PixelX, clip.PixelY = 1920, 1080
		got, ok := VideoExif(exiftest.PanasonicMP4(clip), warsaw)
		require.True(t, ok)
		assert.Nil(t, got.Lat)
		assert.Nil(t, got.Lng)
		assert.Nil(t, got.WidthPx)
		assert.Nil(t, got.HeightPx)
	})

	t.Run("the GPS clock in the EXIF still outranks the camera zone", func(t *testing.T) {
		clip := panasonicClip
		clip.GPS = gpsAt(t, "2021-02-10T06:30:03Z")
		got, ok := VideoExif(exiftest.PanasonicMP4(clip), warsaw)
		require.True(t, ok)
		assert.Equal(t, ZoneGPS, got.TakenAtZone)
		assert.Equal(t, "2021-02-10T06:30:00Z", got.TakenAt.UTC().Format(time.RFC3339))
	})

	t.Run("the rules decide", func(t *testing.T) {
		head := exiftest.PanasonicMP4(panasonicClip)
		for name, rules := range map[string]CameraRules{
			"no rules":                     nil,
			"a rule about the offset only": {{Make: "Panasonic", IgnoreOffsetTag: true, Why: "test"}},
			"a rule for another make":      {{Make: "Acme", VideoTimeFromExif: true, Why: "test"}},
		} {
			_, ok := VideoExif(head, CameraZone{Loc: warsaw.Loc, Rules: rules})
			assert.False(t, ok, name)
		}
	})

	t.Run("an EXIF block without a capture time is not used", func(t *testing.T) {
		_, ok := VideoExif(exiftest.PanasonicMP4(exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200"}), warsaw)
		assert.False(t, ok)
	})

	t.Run("the PANA box in a second udta is found", func(t *testing.T) {
		head := append(append([]byte{}, ftyp...), mp4Box("moov",
			mp4Box("mvhd", make([]byte, 100)),
			mp4Box("udta", mp4Box("meta", make([]byte, 20))),
			mp4Box("udta", exiftest.PANA(panasonicClip, 0x4080)),
		)...)
		_, ok := VideoExif(head, warsaw)
		assert.True(t, ok)
	})

	t.Run("box sizes of 1 (64-bit) and 0 (to the end of the file)", func(t *testing.T) {
		udta := mp4Box("udta", exiftest.PANA(panasonicClip, 0x4080))
		large := binary.BigEndian.AppendUint32(nil, 1)
		large = append(large, "moov"...)
		large = binary.BigEndian.AppendUint64(large, uint64(16+len(udta)))
		_, ok := VideoExif(append(append(append([]byte{}, ftyp...), large...), udta...), warsaw)
		assert.True(t, ok, "64-bit size")

		toEnd := append(binary.BigEndian.AppendUint32(nil, 0), "moov"...)
		_, ok = VideoExif(append(append(append([]byte{}, ftyp...), toEnd...), udta...), warsaw)
		assert.True(t, ok, "size 0")
	})

	t.Run("an EXIF block cut off by the end of the head is not read", func(t *testing.T) {
		head := exiftest.PanasonicMP4(panasonicClip)
		_, ok := VideoExif(head[:len(head)-1], warsaw)
		assert.False(t, ok)
	})

	t.Run("what is not a Panasonic MP4 gives nothing", func(t *testing.T) {
		pana := exiftest.PANA(panasonicClip, 0x4080)
		badSegment := append([]byte{}, pana...)
		badSegment[8+0x4080+3] = 0xE0 // APP0, not APP1
		shortSegment := append([]byte{}, pana...)
		binary.BigEndian.PutUint16(shortSegment[8+0x4080+4:], 8) // an APP1 length with no TIFF block
		for name, head := range map[string][]byte{
			"nothing":                    nil,
			"a JPEG photo":               exiftest.JPEG(panasonicClip),
			"an MP4 without moov":        append(append([]byte{}, ftyp...), mp4Box("mdat", make([]byte, 64))...),
			"moov without udta":          append(append([]byte{}, ftyp...), mp4Box("moov", mp4Box("mvhd", make([]byte, 100)))...),
			"PANA outside udta":          append(append([]byte{}, ftyp...), mp4Box("moov", pana)...),
			"a segment that is not APP1": append(append([]byte{}, ftyp...), mp4Box("moov", mp4Box("udta", badSegment))...),
			"an APP1 without a TIFF":     append(append([]byte{}, ftyp...), mp4Box("moov", mp4Box("udta", shortSegment))...),
			"a box size below its header": append(append([]byte{}, ftyp...),
				append(binary.BigEndian.AppendUint32(nil, 4), "moov"...)...),
			"a 64-bit size cut short": append(append([]byte{}, ftyp...),
				append(binary.BigEndian.AppendUint32(nil, 1), "moov\x00\x00"...)...),
			"a 64-bit size beyond any file": append(append(append([]byte{}, ftyp...),
				append(binary.BigEndian.AppendUint32(nil, 1), "moov"...)...), 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF),
		} {
			_, ok := VideoExif(head, warsaw)
			assert.False(t, ok, name)
		}
	})
}
