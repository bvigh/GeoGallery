package ingest

import (
	"io"
	"time"

	"github.com/rwcarlsen/goexif/exif"
)

type ExifData struct {
	Lat       *float64
	Lng       *float64
	TakenAt   *time.Time
	CameraModel string
}

func ExtractExif(r io.Reader) ExifData {
	x, err := exif.Decode(r)
	if err != nil {
		return ExifData{}
	}

	var result ExifData

	if lat, lng, err := x.LatLong(); err == nil {
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

	return result
}
