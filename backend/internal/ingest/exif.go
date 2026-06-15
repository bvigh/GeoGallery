package ingest

import (
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/rwcarlsen/goexif/exif"

	"codeberg.org/bvigh/geogallery/internal/geo"
)

type ExifData struct {
	Lat         *float64
	Lng         *float64
	TakenAt     *time.Time
	CameraModel string
	WidthPx     *int
	HeightPx    *int
}

func ExtractExif(r io.Reader) ExifData {
	x, err := exif.Decode(r)
	if err != nil {
		return ExifData{}
	}

	var result ExifData

	if lat, lng, err := x.LatLong(); err == nil && geo.IsValidGPS(lat, lng) {
		result.Lat = &lat
		result.Lng = &lng
	}

	if t, err := x.DateTime(); err == nil {
		result.TakenAt = &t
	}

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
