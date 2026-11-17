package ingest

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"math"
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
	ZoneGPS       = "gps"        // the camera clock measured against the photo's GPS timestamp
	ZoneClockRule = "clock_rule" // the owner's clock rule for the camera (CameraZone.Clock)
	ZoneOffset    = "offset"     // the photo's OffsetTimeOriginal tag
	ZoneCamera    = "camera"     // the camera zone (CameraZone.Loc: the batch's, the owner's or CAMERA_TIMEZONE)
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
	TakenAtZone string // ZoneGPS, ZoneClockRule, ZoneOffset or ZoneCamera; empty without TakenAt
	// ClockRule is the owner's rule that gave TakenAt its offset: nil unless TakenAtZone is
	// ZoneClockRule. It is a copy, so what ingest records survives a later change to the rule.
	ClockRule   *ClockRule
	CameraModel string // after CleanTag
	WidthPx     *int
	HeightPx    *int
	Raw         RawExif
	Pano        Pano // from the XMP, not the EXIF: what the photo says about its projection
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

// Where CameraZone.Loc came from (CameraZone.From), in the order ingest picks it.
const (
	CameraZoneFromBatch  = "batch"  // given for one upload (?camera_timezone) or scan (--tz)
	CameraZoneFromUser   = "user"   // the owner's setting (users.camera_timezone)
	CameraZoneFromServer = "server" // CAMERA_TIMEZONE, or the server's own zone when that is unset
)

// CameraZone is how ingest reads camera clocks: the zone they are assumed to show, with what ingest
// records about where it came from, the camera rules that say which of a file's own clocks not to
// trust, and the owner's clock rules for their own cameras. time.Local's name is "Local", which
// would hide a container running in UTC. A zero CameraZone reads wall clocks as UTC and applies no
// rules of either kind.
type CameraZone struct {
	Loc *time.Location // the batch's zone, the owner's, CAMERA_TIMEZONE, or time.Local when none is set
	Set bool           // Loc was named: by the batch, the owner or CAMERA_TIMEZONE
	// From is CameraZoneFromBatch, CameraZoneFromUser or CameraZoneFromServer; "" counts as the server.
	From        string
	UTCFallback bool        // none was named, and the server's zone is UTC all year
	Rules       CameraRules // CAMERA_RULES_FILE: what a make writes, for every user
	// Clock holds the clock rules of the owner whose files are read (camera_clock_rules). Unlike
	// Rules, they describe that user's own cameras, so a CameraZone carrying them serves one owner.
	Clock ClockRules
}

// Name returns the IANA name when Set, otherwise "server zone (unset)".
func (z CameraZone) Name() string {
	if z.Set {
		return z.Loc.String()
	}
	return "server zone (unset)"
}

// Source returns From, with "" read as CameraZoneFromServer.
func (z CameraZone) Source() string {
	if z.From == "" {
		return CameraZoneFromServer
	}
	return z.From
}

// In returns z reading wall clocks in loc, a zone named for them by from (CameraZoneFromBatch or
// CameraZoneFromUser). The camera rules and the clock rules stay. A nil loc returns z unchanged.
func (z CameraZone) In(loc *time.Location, from string) CameraZone {
	if loc == nil {
		return z
	}
	z.Loc, z.Set, z.From, z.UTCFallback = loc, true, from, false
	return z
}

// maxZoneNameBytes bounds a zone name before it is looked up. IANA names are under 40 bytes.
const maxZoneNameBytes = 64

// LoadZone loads an IANA zone name a user gave for camera clocks: a setting, an upload's zone or
// server scan --tz. It refuses "" and "Local", which time.LoadLocation reads as UTC and as the
// server's own zone rather than as a zone of that name, and a name too long to be one.
func LoadZone(name string) (*time.Location, error) {
	switch {
	case name == "", name == "Local":
		return nil, fmt.Errorf("unknown time zone %q", name)
	case len(name) > maxZoneNameBytes:
		return nil, fmt.Errorf("time zone name longer than %d bytes", maxZoneNameBytes)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	return loc, nil
}

// UTCAllYear reports whether loc is at UTC all year: offset 0 on 1 January and on 1 July of the
// current year, so neither hemisphere's summer time can hide. A zone at UTC for part of the year
// only, such as Europe/London, is not. A nil loc is UTC, as a zero CameraZone reads it.
func UTCAllYear(loc *time.Location) bool {
	if loc == nil {
		return true
	}
	year := time.Now().Year()
	for _, month := range []time.Month{time.January, time.July} {
		if _, offset := time.Date(year, month, 1, 12, 0, 0, 0, loc).Zone(); offset != 0 {
			return false
		}
	}
	return true
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
// the file, and the projection its XMP names (readPano). Anything that is not a supported image
// gives an empty result. zone holds the timezone camera clocks are assumed to show (the batch's,
// the owner's or CAMERA_TIMEZONE; nil Loc means UTC), the camera rules and the owner's clock rules.
// takenAt explains how the capture time is read, and Raw keeps what it read it from.
func ExtractExif(r io.ReadSeeker, zone CameraZone) ExifData {
	tags, err := readExifTags(r)
	var result ExifData

	if lat, lng, ok := tags.position(); ok {
		result.Lat = &lat
		result.Lng = &lng
	}

	result.TakenAt, result.TakenAtZone, result.ClockRule = tags.takenAt(zone)
	result.Raw = tags.raw(zone.Rules)
	result.Raw.Partial = err != nil && len(tags) > 0

	if s, ok := tags.text("Model"); ok {
		result.CameraModel = CleanTag(s)
	}
	result.WidthPx = tags.dimension("ExifImageWidth")
	result.HeightPx = tags.dimension("ExifImageHeight")
	result.Pano = readPano(r)

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
	return decode(r, imagemeta.Options{
		Sources:         imagemeta.EXIF,
		ShouldHandleTag: want,
		HandleTag: func(t imagemeta.TagInfo) error {
			handle(t)
			return nil
		},
	})
}

// decode runs imagemeta with opts on the image at the start of r, within the limits above: every
// decode of a file goes through here. A TIFF keeps its XMP in an IFD0 tag, so reading only the XMP
// walks IFD0 too. Anything that is not a supported image is walked over with no error.
func decode(r io.ReadSeeker, opts imagemeta.Options) error {
	format, ok := imageFormat(r)
	if !ok {
		return nil
	}
	opts.R, opts.ImageFormat = r, format
	opts.LimitNumTags, opts.LimitTagSize = exifTagLimit, exifTagSizeLimit
	_, err := imagemeta.Decode(opts)
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

// takenAt reads the capture time, with the zone rule that applied and, for ZoneClockRule, the
// owner's rule. EXIF stores the time as a wall clock, so the zone is chosen here, and never from the
// server's own timezone (that made the same photo an hour or two apart between a native and a
// Docker server):
//  1. when the photo's GPS timestamp (UTC) puts the camera clock within gpsClockTolerance of a
//     UTC offset in real use, that offset. It is measured, so it outranks anything stated;
//  2. the owner's clock rule for the camera (make and model) on the wall clock's date. It is what
//     the owner knows of that camera, which the camera itself does not: a Panasonic DC-TZ200 left
//     on summer time all year still writes its zone's +01:00, and no GPS timestamp;
//  3. the photo's OffsetTimeOriginal, unless the camera rules say to ignore it for the make. A
//     phone writes the offset its clock really shows, abroad too, and many phones write no GPS
//     timestamp;
//  4. otherwise the camera zone (the batch's, the owner's or the server's), with its daylight saving
//     rules on that date (nil Loc reads UTC).
func (t exifTags) takenAt(zone CameraZone) (*time.Time, string, *ClockRule) {
	clock := t.wallClock()
	if !clock.parsed {
		return nil, "", nil
	}
	wall := clock.time
	if offset, ok := t.gpsClockOffset(wall); ok {
		at := wallAtOffset(wall, offset)
		return &at, ZoneGPS, nil
	}
	cameraMake, _ := t.text("Make")
	model, _ := t.text("Model")
	if rule, ok := zone.Clock.Match(cameraMake, model, wall); ok {
		at := wallAtOffset(wall, time.Duration(rule.OffsetMin)*time.Minute)
		return &at, ZoneClockRule, &rule
	}
	if offset, ok := t.offsetTimeOriginal(zone.Rules); ok {
		at := wallAtOffset(wall, offset)
		return &at, ZoneOffset, nil
	}
	loc := zone.Loc
	if loc == nil {
		loc = time.UTC
	}
	at := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc)
	return &at, ZoneCamera, nil
}

// raw collects RawExif through the helpers takenAt uses.
func (t exifTags) raw(rules CameraRules) RawExif {
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
		raw.OffsetIgnored = t.offsetTagIgnored(rules)
	}
	return raw
}

// wallAtOffset reads wall, a clock reading held as a time in UTC, at a UTC offset.
func wallAtOffset(wall time.Time, offset time.Duration) time.Time {
	return wall.Add(-offset).In(time.FixedZone("", int(offset/time.Second)))
}

// offsetTimeOriginal returns the photo's OffsetTimeOriginal when it is a UTC offset in real use and
// the camera rules do not say to ignore it for the make. The value alone cannot tell a wrong offset
// apart: a camera whose clock was set forward by hand writes "+01:00" while showing Central European
// Summer Time, and a phone in London writes the same "+01:00" and means it. So the exemption goes by
// make, in the rules file. Photos already ingested keep their stored times until repaired.
func (t exifTags) offsetTimeOriginal(rules CameraRules) (time.Duration, bool) {
	if t.offsetTagIgnored(rules) {
		return 0, false
	}
	s, ok := t.text("OffsetTimeOriginal")
	if !ok {
		return 0, false
	}
	return parseUTCOffset(s)
}

// offsetTagIgnored reports whether the camera rules say to ignore the make's OffsetTimeOriginal.
func (t exifTags) offsetTagIgnored(rules CameraRules) bool {
	cameraMake, _ := t.text("Make")
	return rules.For(cameraMake).IgnoreOffsetTag
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

// DetectMediaType returns "photo", "video", "photo_360" or "video_360": the extension tells a video
// from a photo, and what the file says about its projection makes it 360°.
//  1. The XMP decides when it names a projection (GPano:ProjectionType). Only an equirectangular
//     image of the whole sphere is 360°. Another projection, such as a phone's cylindrical
//     panorama, or a part of a sphere is flat: the 360° viewers wrap the image round the whole
//     sphere.
//  2. Otherwise the frame does (isSphereFrame).
//
// The file name is never read. Its keywords made flat files 360°: "360" turns up in dates and
// counters (20190713_143602.jpg, P1036012.JPG), and a phone's "PANO" is a flat panorama. An
// Insta360 name is no sign either: a reframed export keeps the name of the 360° clip it was cut
// from.
func DetectMediaType(e ExifData, ext string) string {
	isVideo := IsVideoExt(ext)
	if is360(e) {
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

// DetectMediaTypeFromProbe is DetectMediaType for a video that ffprobe read. Its spherical metadata
// (side data "Spherical Mapping", equirectangular) makes it 360°; otherwise the probed frame stands
// in for EXIF dimensions the video does not have.
func DetectMediaTypeFromProbe(e ExifData, ext string, probe VideoProbeResult) string {
	if probe.Equirectangular {
		return "video_360"
	}
	if e.WidthPx == nil && probe.HeightPx > 0 {
		e.WidthPx = &probe.WidthPx
		e.HeightPx = &probe.HeightPx
	}
	return DetectMediaType(e, ext)
}

func IsVideoExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".mp4", ".mov", ".avi", ".mkv", ".3gp":
		return true
	}
	return false
}

func is360(e ExifData) bool {
	if e.Pano.Projection != "" {
		return strings.EqualFold(e.Pano.Projection, "equirectangular") && !e.Pano.Partial
	}
	return isSphereFrame(e.WidthPx, e.HeightPx)
}

// The frame of an equirectangular image of the whole sphere: twice as wide as it is high. In one
// real library every one of 401 360° photos and videos was exactly 2:1, while phone panoramas came
// as close as 2.03:1. The floor keeps out smaller 2:1 frames, such as a crop or a screenshot.
const (
	sphereRatioPercent = 1 // how far from 2:1 a frame's ratio may be, in percent
	sphereMinWidth     = 3000
)

// isSphereFrame reports whether a frame is at least sphereMinWidth pixels wide and its ratio within
// sphereRatioPercent of 2:1. |w/h − 2| ≤ 2·percent/100 is |w − 2h|·100 ≤ 2h·percent, which is
// exact in whole numbers.
func isSphereFrame(width, height *int) bool {
	if width == nil || height == nil || *height <= 0 || *width < sphereMinWidth {
		return false
	}
	w, h := int64(*width), int64(*height)
	off := w - 2*h
	if off < 0 {
		off = -off
	}
	return off*100 <= 2*h*sphereRatioPercent
}
