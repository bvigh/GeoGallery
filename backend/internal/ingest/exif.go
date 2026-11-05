package ingest

import (
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
	ZoneCamera = "camera" // the camera timezone (CAMERA_TIMEZONE)
)

type ExifData struct {
	Lat         *float64
	Lng         *float64
	TakenAt     *time.Time
	TakenAtZone string // ZoneGPS or ZoneCamera; empty without TakenAt
	CameraModel string
	WidthPx     *int
	HeightPx    *int
}

// ExtractExif reads the EXIF block at the start of r. cameraZone is the timezone camera clocks are
// assumed to show (CAMERA_TIMEZONE); nil means UTC. takenAt explains how the capture time is read.
func ExtractExif(r io.Reader, cameraZone *time.Location) ExifData {
	x, err := exif.Decode(r)
	if err != nil {
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

// takenAt reads the capture time. EXIF stores it as a wall clock without a zone, so the zone is
// chosen here, and never from the server's own timezone (that made the same photo an hour or two
// apart between a native and a Docker server):
//  1. when the photo's GPS timestamp (UTC) puts the camera clock within gpsClockTolerance of a
//     UTC offset in real use, that offset. It follows a phone abroad;
//  2. otherwise cameraZone, with its daylight saving rules on that date.
//
// OffsetTimeOriginal is deliberately ignored: the Panasonic DC-TZ200 writes "+01:00" all year, so
// a summer photo from a clock showing Central European wall time would land an hour late.
func takenAt(x *exif.Exif, cameraZone *time.Location) (*time.Time, string) {
	wall, ok := exifWallClock(x)
	if !ok {
		return nil, ""
	}
	if offset, ok := gpsClockOffset(x, wall); ok {
		t := wall.Add(-offset).In(time.FixedZone("", int(offset/time.Second)))
		return &t, ZoneGPS
	}
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, cameraZone)
	return &t, ZoneCamera
}

// exifWallClock returns the capture time's wall clock, as a time in UTC: DateTimeOriginal, or IFD0
// DateTime only when that tag is missing. The tags and parsing match goexif's DateTime, which
// ingest used before: only the zone differs.
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
