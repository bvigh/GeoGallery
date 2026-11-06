package ingest

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/rwcarlsen/goexif/exif"
	"github.com/rwcarlsen/goexif/tiff"

	"codeberg.org/bvigh/geogallery/internal/geo"
)

// Where ExifData.TakenAt's UTC offset came from.
const (
	ZoneGPS    = "gps"    // the camera clock measured against the photo's GPS timestamp
	ZoneOffset = "offset" // the photo's OffsetTimeOriginal tag
	ZoneCamera = "camera" // the camera timezone (CAMERA_TIMEZONE)
)

type ExifData struct {
	Lat         *float64
	Lng         *float64
	TakenAt     *time.Time
	TakenAtZone string // ZoneGPS, ZoneOffset or ZoneCamera; empty without TakenAt
	CameraModel string
	WidthPx     *int
	HeightPx    *int
}

// ExtractExif reads the EXIF block at the start of r. cameraZone is the timezone camera clocks are
// assumed to show (CAMERA_TIMEZONE); nil means UTC. takenAt explains how the capture time is read.
func ExtractExif(r io.Reader, cameraZone *time.Location) ExifData {
	x, err := exif.Decode(r)
	// A non-critical error means one sub-IFD was unreadable; the tags read from the rest are good.
	// Old Sony cameras write an Exif IFD goexif rejects, and their IFD0 DateTime is still right.
	if err != nil && (x == nil || exif.IsCriticalError(err)) {
		return ExifData{}
	}

	var result ExifData

	if lat, lng, err := x.LatLong(); err == nil && geo.IsValidGPS(lat, lng) {
		result.Lat = &lat
		result.Lng = &lng
	}

	if cameraZone == nil {
		cameraZone = time.UTC
	}
	result.TakenAt, result.TakenAtZone = takenAt(x, cameraZone)

	if tag, err := x.Get(exif.Model); err == nil {
		if s, err := tag.StringVal(); err == nil {
			result.CameraModel = s
		}
	}

	if tag, err := x.Get(exif.PixelXDimension); err == nil {
		if v, err := tag.Int(0); err == nil {
			result.WidthPx = &v
		}
	}
	if tag, err := x.Get(exif.PixelYDimension); err == nil {
		if v, err := tag.Int(0); err == nil {
			result.HeightPx = &v
		}
	}

	return result
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
func takenAt(x *exif.Exif, cameraZone *time.Location) (*time.Time, string) {
	wall, ok := exifWallClock(x)
	if !ok {
		return nil, ""
	}
	if offset, ok := gpsClockOffset(x, wall); ok {
		t := wallAtOffset(wall, offset)
		return &t, ZoneGPS
	}
	if offset, ok := offsetTimeOriginal(x); ok {
		t := wallAtOffset(wall, offset)
		return &t, ZoneOffset
	}
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, cameraZone)
	return &t, ZoneCamera
}

// wallAtOffset reads wall, a clock reading held as a time in UTC, at a UTC offset.
func wallAtOffset(wall time.Time, offset time.Duration) time.Time {
	return wall.Add(-offset).In(time.FixedZone("", int(offset/time.Second)))
}

// offsetIgnoredMakes lists camera makes, in lower case, whose OffsetTimeOriginal is wrong, so their
// photos are read in the camera zone instead. The value alone cannot tell them apart: the Panasonic
// DC-TZ200 writes "+01:00" all year, even while its clock shows Central European Summer Time, and a
// phone in London writes the same "+01:00" and means it. If another camera turns out to write a
// wrong offset, add its make here and run `server retime` to repair what it already ingested.
var offsetIgnoredMakes = map[string]bool{"panasonic": true}

// offsetTimeOriginalTag is EXIF's OffsetTimeOriginal in the Exif IFD. goexif has no field for it.
const offsetTimeOriginalTag = 0x9011

// offsetTimeOriginal returns the photo's OffsetTimeOriginal when it is a UTC offset in real use and
// its make is trusted.
func offsetTimeOriginal(x *exif.Exif) (time.Duration, bool) {
	if tag, err := x.Get(exif.Make); err == nil {
		if cameraMake, err := tag.StringVal(); err == nil && offsetIgnoredMakes[strings.ToLower(strings.TrimSpace(cameraMake))] {
			return 0, false
		}
	}
	ptr, err := x.Get(exif.ExifIFDPointer)
	if err != nil {
		return 0, false
	}
	at, err := ptr.Int64(0)
	if err != nil {
		return 0, false
	}
	r := bytes.NewReader(x.Raw)
	if _, err := r.Seek(at, io.SeekStart); err != nil {
		return 0, false
	}
	dir, _, err := tiff.DecodeDir(r, x.Tiff.Order)
	if err != nil {
		return 0, false
	}
	for _, tag := range dir.Tags {
		if tag.Id != offsetTimeOriginalTag {
			continue
		}
		s, err := tag.StringVal()
		if err != nil {
			return 0, false
		}
		return parseUTCOffset(s)
	}
	return 0, false
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

// exifWallClock returns the capture time's wall clock, as a time in UTC: DateTimeOriginal, or IFD0
// DateTime only when that tag is missing. The tags and parsing match goexif's DateTime, which
// ingest used before, so a re-read (server retime) recognises the rows dated from EXIF.
func exifWallClock(x *exif.Exif) (time.Time, bool) {
	tag, err := x.Get(exif.DateTimeOriginal)
	if err != nil {
		if tag, err = x.Get(exif.DateTime); err != nil {
			return time.Time{}, false
		}
	}
	if tag.Format() != tiff.StringVal {
		return time.Time{}, false
	}
	t, err := time.Parse(exifTimeLayout, strings.TrimRight(string(tag.Val), "\x00"))
	return t, err == nil
}

// gpsClockTolerance is how far the camera clock may sit from a whole UTC offset against the GPS
// timestamp. A phone's fix is seconds old. A stale fix (minutes or days old, or 1970-01-01) is
// rejected unless it happens to land this close to a real offset. On a real library of about 2,300
// GPS-timed photos, 2 minutes rejected every stale fix; 5 minutes let two through.
const gpsClockTolerance = 2 * time.Minute

// gpsClockOffset returns the camera clock's UTC offset, measured as wall minus the GPS timestamp
// and rounded to a quarter hour, when that is a real offset within gpsClockTolerance.
func gpsClockOffset(x *exif.Exif, wall time.Time) (time.Duration, bool) {
	gpsUTC, ok := gpsTimestamp(x)
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

// gpsTimestamp returns GPSDateStamp + GPSTimeStamp, which EXIF defines as UTC.
func gpsTimestamp(x *exif.Exif) (time.Time, bool) {
	dateTag, err := x.Get(exif.GPSDateStamp)
	if err != nil {
		return time.Time{}, false
	}
	date, err := dateTag.StringVal()
	if err != nil {
		return time.Time{}, false
	}
	day, err := time.Parse("2006:01:02", strings.TrimSpace(strings.TrimRight(date, "\x00")))
	if err != nil {
		return time.Time{}, false
	}
	timeTag, err := x.Get(exif.GPSTimeStamp)
	if err != nil || timeTag.Count != 3 {
		return time.Time{}, false
	}
	t := day
	for i, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		num, den, err := timeTag.Rat2(i)
		if err != nil || den <= 0 || num < 0 {
			return time.Time{}, false
		}
		t = t.Add(time.Duration(float64(num) / float64(den) * float64(unit)))
	}
	return t, true
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
