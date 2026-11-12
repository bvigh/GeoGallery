package ingest

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bep/imagemeta"

	"github.com/bvigh/geogallery/internal/geo"
)

// Where ExifData.TakenAt's UTC offset came from.
const (
	ZoneGPS    = "gps"    // the camera clock measured against the photo's GPS timestamp
	ZoneOffset = "offset" // the photo's OffsetTimeOriginal tag
	ZoneCamera = "camera" // the camera timezone (CAMERA_TIMEZONE)
)

// The tags a wall clock can come from (RawExif.WallTag), under EXIF's names. imagemeta calls IFD0's
// DateTime ModifyDate, as ExifTool does.
const (
	WallTagOriginal = "DateTimeOriginal"
	WallTagIFD0     = "DateTime"
)

type ExifData struct {
	Lat         *float64
	Lng         *float64
	TakenAt     *time.Time
	TakenAtZone string // ZoneGPS, ZoneOffset or ZoneCamera; empty without TakenAt
	CameraModel string // after CleanTag
	WidthPx     *int
	HeightPx    *int
	Raw         RawExif
}

// RawExif is what the file said about its capture time, including what takenAt could not use or
// chose to ignore. It is filled whether or not takenAt succeeds, and ingest keeps it in
// media.capture_evidence. Text is after CleanTag, so it is safe to store; parsing uses the original.
type RawExif struct {
	WallClock     string         // the wall clock as written, even when it does not parse
	WallTag       string         // WallTagOriginal or WallTagIFD0; empty when the file has neither
	GPSStamp      *time.Time     // GPSDateStamp + GPSTimeStamp (UTC), whenever they parse
	WallMinusGPS  *time.Duration // the wall clock minus GPSStamp, before rounding; nil unless both parse
	OffsetTag     string         // OffsetTimeOriginal as written, even when it is ignored
	OffsetIgnored bool           // the tag exists and the make is in offsetIgnoredMakes
	Make          string
	Partial       bool // the decoder stopped at an error, and the tags read before it were kept
}

// CameraZone is the zone camera clocks are assumed to show, with what ingest records about where it
// came from. time.Local's name is "Local", which would hide a container running in UTC. A zero
// CameraZone reads wall clocks as UTC.
type CameraZone struct {
	Loc         *time.Location // CAMERA_TIMEZONE, or time.Local when unset
	Set         bool           // CAMERA_TIMEZONE was set
	UTCFallback bool           // unset, and the server's zone is UTC all year
}

// Name returns the IANA name when Set, otherwise "server zone (unset)".
func (z CameraZone) Name() string {
	if z.Set {
		return z.Loc.String()
	}
	return "server zone (unset)"
}

// maxTagRunes caps one text value read from a file. The figure is a judgement.
const maxTagRunes = 200

// tagJunk is what CleanTag removes wherever it is: NUL, and U+FFFD, which a decoder (imagemeta
// included) writes in place of bytes that were not UTF-8.
var tagJunk = strings.NewReplacer("\x00", "", "�", "")

// CleanTag removes invalid UTF-8, U+FFFD and NUL bytes, trims surrounding space and cuts the result
// to 200 runes. An empty result means the value is absent: NULL in a column, null in
// capture_evidence. PostgreSQL refuses invalid UTF-8 in TEXT and a NUL in jsonb, so an unclean value
// would fail the INSERT, and with it an upload that would otherwise work.
func CleanTag(s string) string {
	s = strings.TrimSpace(tagJunk.Replace(strings.ToValidUTF8(s, "")))
	if utf8.RuneCountInString(s) > maxTagRunes {
		s = string([]rune(s)[:maxTagRunes])
	}
	return s
}

// What one decode may read. imagemeta's defaults (5000 tags of 10 KB) let a 60 KB file cost about
// 900 MB of allocation, because every byte of a byte-typed value is boxed on its own. With these,
// the worst file found costs about 6 MB. Real files are nowhere near: in a library of 13,476
// JPEGs the most tags in one file was 68, and the tags read here are a few dozen bytes each.
const (
	exifTagLimit     = 1000
	exifTagSizeLimit = 256
)

// The IFDs ExtractExif reads from, as imagemeta names them. A new imagemeta that renames one fails
// the tests.
const (
	exifIFD0 = "IFD0"
	exifIFD  = "IFD0/ExifIFDP"
	gpsIFD   = "IFD0/GPSInfoIFD"
)

// exifTagsRead maps each tag ExtractExif uses to its IFD. The names are imagemeta's, which are
// ExifTool's. The IFD matters: tags are numbered per IFD, and imagemeta names the Interoperability
// IFD's 0x0001 and 0x0002 GPSLatitudeRef and GPSLatitude. Most phones write that IFD, and it comes
// before the real GPS tags.
var exifTagsRead = map[string]string{
	"Make": exifIFD0, "Model": exifIFD0, "ModifyDate": exifIFD0,
	"DateTimeOriginal": exifIFD, "OffsetTimeOriginal": exifIFD,
	"ExifImageWidth": exifIFD, "ExifImageHeight": exifIFD,
	"GPSDateStamp": gpsIFD, "GPSTimeStamp": gpsIFD,
	"GPSLatitude": gpsIFD, "GPSLatitudeRef": gpsIFD, "GPSLongitude": gpsIFD, "GPSLongitudeRef": gpsIFD,
}

// ExtractExif reads the EXIF block of the image at the start of r, which may be only the head of
// the file. Anything that is not a supported image gives an empty result. cameraZone is the
// timezone camera clocks are assumed to show (CAMERA_TIMEZONE); nil means UTC. takenAt explains
// how the capture time is read, and Raw keeps what it read it from.
func ExtractExif(r io.ReadSeeker, cameraZone *time.Location) ExifData {
	tags, err := readExifTags(r)
	var result ExifData

	if lat, lng, ok := tags.position(); ok {
		result.Lat = &lat
		result.Lng = &lng
	}

	if cameraZone == nil {
		cameraZone = time.UTC
	}
	result.TakenAt, result.TakenAtZone = tags.takenAt(cameraZone)
	result.Raw = tags.raw()
	result.Raw.Partial = err != nil && len(tags) > 0

	if s, ok := tags.text("Model"); ok {
		result.CameraModel = CleanTag(s)
	}
	result.WidthPx = tags.dimension("ExifImageWidth")
	result.HeightPx = tags.dimension("ExifImageHeight")

	return result
}

// exifTags holds the tags of one file by name. The values come from the file, so every read below checks the type. That is why imagemeta's Tags.GetLatLong is not
// used: it panics on a GPSLatitudeRef that is not text, outside Decode's recovery.
type exifTags map[string]any

// readExifTags returns the tags in exifTagsRead. The error ends the walk, but the tags read before
// it are good: an old Motorola writes a broken tag after its model and position.
func readExifTags(r io.ReadSeeker) (exifTags, error) {
	tags := exifTags{}
	err := decodeExif(r,
		func(t imagemeta.TagInfo) bool { return exifTagsRead[t.Tag] == t.Namespace },
		func(t imagemeta.TagInfo) { tags[t.Tag] = t.Value },
	)
	return tags, err
}

// decodeExif walks the EXIF of the image at the start of r within the limits above, and gives handle
// every tag that want accepts. Anything that is not a supported image is walked over with no error.
func decodeExif(r io.ReadSeeker, want func(imagemeta.TagInfo) bool, handle func(imagemeta.TagInfo)) error {
	format, ok := imageFormat(r)
	if !ok {
		return nil
	}
	_, err := imagemeta.Decode(imagemeta.Options{
		R:               r,
		ImageFormat:     format,
		Sources:         imagemeta.EXIF,
		LimitNumTags:    exifTagLimit,
		LimitTagSize:    exifTagSizeLimit,
		ShouldHandleTag: want,
		HandleTag: func(t imagemeta.TagInfo) error {
			handle(t)
			return nil
		},
	})
	return err
}

// imageFormat tells from the first bytes of r which decoder it needs, and leaves r at its start.
// The extension is not used: a file's name says nothing about what it holds.
func imageFormat(r io.ReadSeeker) (imagemeta.ImageFormat, bool) {
	var buf [12]byte
	n, _ := io.ReadFull(r, buf[:])
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return 0, false
	}
	b := buf[:n]
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return imagemeta.JPEG, true
	case bytes.HasPrefix(b, []byte("II*\x00")), bytes.HasPrefix(b, []byte("MM\x00*")):
		return imagemeta.TIFF, true
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return imagemeta.PNG, true
	case n == len(buf) && string(b[:4]) == "RIFF" && string(b[8:]) == "WEBP":
		return imagemeta.WebP, true
	case n == len(buf) && string(b[4:8]) == "ftyp" && heifBrands[string(b[8:])]:
		return imagemeta.HEIF, true
	}
	return 0, false
}

// heifBrands are the major brands of HEIC and HEIF files. MP4 and QuickTime files have an ftyp box
// too, with other brands.
var heifBrands = map[string]bool{
	"heic": true, "heix": true, "heim": true, "heis": true,
	"hevc": true, "hevx": true, "hevm": true, "hevs": true,
	"mif1": true, "msf1": true,
}

func (t exifTags) text(name string) (string, bool) {
	s, ok := t[name].(string)
	return s, ok
}

// dimension returns a pixel count, or nil when it is missing or does not fit the database column.
func (t exifTags) dimension(name string) *int {
	var v int64
	switch n := t[name].(type) {
	case uint16:
		v = int64(n)
	case uint32:
		v = int64(n)
	case int32:
		v = int64(n)
	default:
		return nil
	}
	if v < 0 || v > math.MaxInt32 {
		return nil
	}
	i := int(v)
	return &i
}

func (t exifTags) position() (lat, lng float64, ok bool) {
	if lat, ok = t.coordinate("GPSLatitude", "GPSLatitudeRef", "S"); !ok {
		return 0, 0, false
	}
	if lng, ok = t.coordinate("GPSLongitude", "GPSLongitudeRef", "W"); !ok {
		return 0, 0, false
	}
	return lat, lng, geo.IsValidGPS(lat, lng)
}

// coordinate returns one signed coordinate. Exactly 0 counts as unreadable: imagemeta reads a
// rational with a zero denominator as 0, and a real position is not exactly on the equator or the
// prime meridian. A zero denominator in only the minutes or seconds cannot be told apart.
func (t exifTags) coordinate(value, ref, negative string) (float64, bool) {
	v, ok := t[value].(float64)
	if !ok || v == 0 {
		return 0, false
	}
	hemisphere, ok := t.text(ref)
	if !ok {
		return 0, false
	}
	if hemisphere == negative {
		v = -v
	}
	return v, true
}

const exifTimeLayout = "2006:01:02 15:04:05"

// takenAt reads the capture time. EXIF stores it as a wall clock, so the zone is chosen here, and
// never from the server's own timezone (that made the same photo an hour or two apart between a
// native and a Docker server):
//  1. when the photo's GPS timestamp (UTC) puts the camera clock within gpsClockTolerance of a
//     UTC offset in real use, that offset. It is measured, so it outranks what the camera declares;
//  2. the photo's OffsetTimeOriginal, unless its make is in offsetIgnoredMakes. A phone writes the
//     offset its clock really shows, abroad too, and many phones write no GPS timestamp;
//  3. otherwise cameraZone, with its daylight saving rules on that date.
func (t exifTags) takenAt(cameraZone *time.Location) (*time.Time, string) {
	clock := t.wallClock()
	if !clock.parsed {
		return nil, ""
	}
	wall := clock.time
	if offset, ok := t.gpsClockOffset(wall); ok {
		at := wallAtOffset(wall, offset)
		return &at, ZoneGPS
	}
	if offset, ok := t.offsetTimeOriginal(); ok {
		at := wallAtOffset(wall, offset)
		return &at, ZoneOffset
	}
	at := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, cameraZone)
	return &at, ZoneCamera
}

// raw collects RawExif through the helpers takenAt uses.
func (t exifTags) raw() RawExif {
	clock := t.wallClock()
	cameraMake, _ := t.text("Make")
	raw := RawExif{
		WallClock: CleanTag(clock.text),
		WallTag:   clock.tag,
		Make:      CleanTag(cameraMake),
	}
	if stamp, ok := t.gpsTimestamp(); ok {
		raw.GPSStamp = &stamp
		if clock.parsed {
			diff := clock.time.Sub(stamp)
			raw.WallMinusGPS = &diff
		}
	}
	if _, found := t["OffsetTimeOriginal"]; found {
		offsetTag, _ := t.text("OffsetTimeOriginal")
		raw.OffsetTag = CleanTag(offsetTag)
		raw.OffsetIgnored = t.offsetMakeIgnored()
	}
	return raw
}

// wallAtOffset reads wall, a clock reading held as a time in UTC, at a UTC offset.
func wallAtOffset(wall time.Time, offset time.Duration) time.Time {
	return wall.Add(-offset).In(time.FixedZone("", int(offset/time.Second)))
}

// offsetIgnoredMakes lists camera makes, in lower case, whose OffsetTimeOriginal is wrong, so their
// photos are read in the camera zone instead. The value alone cannot tell them apart: the Panasonic
// DC-TZ200 writes "+01:00" all year, even while its clock shows Central European Summer Time, and a
// phone in London writes the same "+01:00" and means it. If another camera turns out to write a
// wrong offset, add its make here. Photos it already ingested keep their stored times until repaired.
var offsetIgnoredMakes = map[string]bool{"panasonic": true}

// offsetTimeOriginal returns the photo's OffsetTimeOriginal when it is a UTC offset in real use and
// its make is trusted.
func (t exifTags) offsetTimeOriginal() (time.Duration, bool) {
	if t.offsetMakeIgnored() {
		return 0, false
	}
	s, ok := t.text("OffsetTimeOriginal")
	if !ok {
		return 0, false
	}
	return parseUTCOffset(s)
}

// offsetMakeIgnored reports whether the photo's make is in offsetIgnoredMakes.
func (t exifTags) offsetMakeIgnored() bool {
	cameraMake, _ := t.text("Make")
	return offsetIgnoredMakes[strings.ToLower(strings.TrimSpace(cameraMake))]
}

// parseUTCOffset reads "+01:00". Cameras without a zone write blanks or colons, which fail here.
func parseUTCOffset(s string) (time.Duration, bool) {
	t, err := time.Parse("-07:00", strings.TrimSpace(s))
	if err != nil {
		return 0, false
	}
	_, seconds := t.Zone()
	offset := time.Duration(seconds) * time.Second
	return offset, isRealOffset(offset)
}

// wallClock is the capture time's wall clock tag.
type wallClock struct {
	tag    string    // WallTagOriginal or WallTagIFD0; empty when the file has neither
	text   string    // the value as imagemeta read it; empty when it is not text
	time   time.Time // the reading, held as a time in UTC, when parsed is true
	parsed bool
}

// wallClock reads DateTimeOriginal, or IFD0 ModifyDate (EXIF's DateTime) only when that tag is
// missing, so a DateTimeOriginal that does not parse is still the tag reported.
func (t exifTags) wallClock() wallClock {
	clock := wallClock{tag: WallTagOriginal}
	name := "DateTimeOriginal"
	if t[name] == nil {
		clock.tag, name = WallTagIFD0, "ModifyDate"
		if t[name] == nil {
			return wallClock{}
		}
	}
	s, ok := t.text(name)
	if !ok {
		return clock
	}
	clock.text = s
	wall, err := time.Parse(exifTimeLayout, s)
	clock.time, clock.parsed = wall, err == nil
	return clock
}

// gpsClockTolerance is how far the camera clock may sit from a whole UTC offset against the GPS
// timestamp. A phone's fix is seconds old. A stale fix (minutes or days old, or 1970-01-01) is
// rejected unless it happens to land this close to a real offset. On a real library of about 2,300
// GPS-timed photos, 2 minutes rejected every stale fix; 5 minutes let two through.
const gpsClockTolerance = 2 * time.Minute

// gpsClockOffset returns the camera clock's UTC offset, measured as wall minus the GPS timestamp
// and rounded to a quarter hour, when that is a real offset within gpsClockTolerance.
func (t exifTags) gpsClockOffset(wall time.Time) (time.Duration, bool) {
	gpsUTC, ok := t.gpsTimestamp()
	if !ok {
		return 0, false
	}
	diff := wall.Sub(gpsUTC)
	offset := diff.Round(15 * time.Minute)
	if (diff-offset).Abs() > gpsClockTolerance || !isRealOffset(offset) {
		return 0, false
	}
	return offset, true
}

// fractionalOffsets are the UTC offsets in use since the 1990s that are not whole hours, in
// minutes east of UTC (Marquesas, Venezuela 2007–2016, Newfoundland, Iran, Afghanistan, India,
// Nepal, Myanmar, North Korea 2015–2018, Eucla, central Australia, Lord Howe, Chatham).
var fractionalOffsets = map[time.Duration]bool{
	-570: true, -270: true, -210: true, -150: true, 210: true, 270: true, 330: true, 345: true,
	390: true, 510: true, 525: true, 570: true, 630: true, 765: true, 825: true,
}

func isRealOffset(d time.Duration) bool {
	if d < -12*time.Hour || d > 14*time.Hour {
		return false
	}
	return d%time.Hour == 0 || (d%time.Minute == 0 && fractionalOffsets[d/time.Minute])
}

// gpsTimestamp returns GPSDateStamp + GPSTimeStamp, which EXIF defines as UTC. imagemeta gives the
// time as text, "10:13:14" or "10:13:14.5".
func (t exifTags) gpsTimestamp() (time.Time, bool) {
	date, ok := t.text("GPSDateStamp")
	if !ok {
		return time.Time{}, false
	}
	at, err := time.Parse("2006:01:02", strings.TrimSpace(date))
	if err != nil {
		return time.Time{}, false
	}
	stamp, ok := t.text("GPSTimeStamp")
	if !ok {
		return time.Time{}, false
	}
	parts := strings.Split(strings.TrimSpace(stamp), ":")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		v, err := strconv.ParseFloat(parts[i], 64)
		if err != nil || !(v >= 0) { // written this way round so that NaN is refused too
			return time.Time{}, false
		}
		at = at.Add(time.Duration(v * float64(unit)))
	}
	return at, true
}

// Tag is one tag of a file as written, for display.
type Tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// maxListedTags caps one file's tag list. The figure is a judgement.
const maxListedTags = 200

// maxRenderedValues is the longest array tagText spells out. Two characters each already fill the
// 200 that a listed value keeps.
const maxRenderedValues = 100

// listedIFDs are the IFDs ReadTags lists. The Interoperability IFD is left out, because imagemeta
// names its tags 0x0001 and 0x0002 GPSLatitudeRef and GPSLatitude, and so is IFD1, the thumbnail's.
var listedIFDs = map[string]bool{exifIFD0: true, exifIFD: true, gpsIFD: true}

// ReadTags lists the tags of IFD0 and the Exif and GPS sub-IFDs for display, sorted by name, under
// imagemeta's names (ExifTool's); a tag it has no name for is shown by id. It decodes within
// ExtractExif's limits, so a value over exifTagSizeLimit bytes is left out. imagemeta reports no
// tag types, so a binary value shows its printable characters as text, and one with none is
// dropped. The list is empty, and not nil, when the head holds no readable EXIF.
func ReadTags(head []byte) []Tag {
	var tags []Tag
	_ = decodeExif(bytes.NewReader(head),
		func(t imagemeta.TagInfo) bool { return listedIFDs[t.Namespace] },
		func(t imagemeta.TagInfo) { tags = append(tags, Tag{Name: tagName(t.Tag), Value: tagText(t.Value)}) },
	)
	return finishTags(tags)
}

// tagName is imagemeta's name for a tag, or "0x9C9B" for an id it has no name for.
func tagName(name string) string {
	if id, ok := strings.CutPrefix(name, imagemeta.UnknownPrefix); ok {
		if n, err := strconv.ParseUint(strings.TrimPrefix(id, "0x"), 16, 16); err == nil {
			return fmt.Sprintf("0x%04X", n)
		}
	}
	return name
}

// tagText is a tag's value as text: a string as imagemeta gives it, a number as 2, a rational as
// 1/100, an array as [2,3,0,0]. A long array reads as its length: rendering it costs more than it
// shows, and the list keeps only the first 200 characters anyway.
func tagText(v any) string {
	values, ok := v.([]any)
	if !ok {
		return fmt.Sprint(v)
	}
	if len(values) > maxRenderedValues {
		return fmt.Sprintf("%d values", len(values))
	}
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprint(value)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// TagsFromMap is ReadTags for a video's VideoProbeResult.RawTags.
func TagsFromMap(raw map[string]string) []Tag {
	tags := make([]Tag, 0, len(raw))
	for name, value := range raw {
		tags = append(tags, Tag{Name: name, Value: value})
	}
	return finishTags(tags)
}

// finishTags applies what every tag list promises: clean text with values of at most 200 characters,
// a tag with nothing left of it dropped, sorted by name, at most maxListedTags entries. The value
// breaks ties so that the order, and with it the cap, is the same on every request.
func finishTags(tags []Tag) []Tag {
	clean := make([]Tag, 0, len(tags))
	for _, tag := range tags {
		tag.Name, tag.Value = CleanTag(tag.Name), CleanTag(tag.Value)
		if tag.Name != "" && tag.Value != "" {
			clean = append(clean, tag)
		}
	}
	slices.SortFunc(clean, func(a, b Tag) int {
		return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.Value, b.Value))
	})
	return clean[:min(len(clean), maxListedTags)]
}

// DetectMediaType returns "photo", "video", "photo_360", or "video_360".
// 360° detection uses two signals, either of which is sufficient:
//  1. Filename contains "360", "pano", "panorama", or "spherical"
//  2. EXIF dimensions are present and aspect ratio is within 5% of 2:1 with
//     width ≥ 3000px (rules out accidental landscape crops)
func DetectMediaType(e ExifData, filename, ext string) string {
	isVideo := IsVideoExt(ext)
	if is360(e, filename) {
		if isVideo {
			return "video_360"
		}
		return "photo_360"
	}
	if isVideo {
		return "video"
	}
	return "photo"
}

// DetectMediaTypeFromProbe applies a VideoProbeResult to override 360°
// detection for videos where EXIF dimensions are absent. Equirectangular
// spherical mapping metadata takes precedence; the 2:1 aspect ratio check
// is used as a fallback when that tag is absent.
func DetectMediaTypeFromProbe(e ExifData, filename, ext string, probe VideoProbeResult) string {
	if probe.Equirectangular {
		return "video_360"
	}
	if e.WidthPx == nil && probe.HeightPx > 0 {
		e.WidthPx = &probe.WidthPx
		e.HeightPx = &probe.HeightPx
	}
	return DetectMediaType(e, filename, ext)
}

func IsVideoExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".mov", ".avi", ".mkv", ".3gp":
		return true
	}
	return false
}

func is360(e ExifData, filename string) bool {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
	for _, kw := range []string{"360", "pano", "panorama", "spherical"} {
		if strings.Contains(base, kw) {
			return true
		}
	}
	if e.WidthPx != nil && e.HeightPx != nil && *e.HeightPx > 0 && *e.WidthPx >= 3000 {
		ratio := float64(*e.WidthPx) / float64(*e.HeightPx)
		if ratio >= 1.95 && ratio <= 2.05 {
			return true
		}
	}
	return false
}
