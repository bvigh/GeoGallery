package ingest

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/bvigh/geogallery/internal/geo"
	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest/crafted"
)

// FuzzExtractExif checks that no file can make decoding panic or report an impossible position.
// Its seeds run in plain go test.
func FuzzExtractExif(f *testing.F) {
	photo := exiftest.Photo{
		Make: "samsung", Model: "SM-S921B", DateTimeOriginal: "2024:06:01 12:13:14", OffsetTimeOriginal: "+02:00",
		GPS: &exiftest.GPS{Lat: 47.4979, Lng: 19.0402, Time: time.Date(2024, 6, 1, 10, 13, 14, 0, time.UTC)},
	}
	for _, seed := range [][]byte{
		exiftest.JPEG(photo), exiftest.TIFF(photo), exiftest.PNG(photo), exiftest.WebP(photo), exiftest.HEIF(photo),
		exiftest.JPEG(exiftest.Photo{DateTime: "2009:04:15 10:20:30", BrokenExifIFD: true}),
	} {
		f.Add(seed)
	}
	for _, in := range append(crafted.Inputs(), crafted.BadExifIFDs()...) {
		if len(in.TIFF) < 1<<16 {
			f.Add(in.TIFF)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got := ExtractExif(bytes.NewReader(data), CameraZone{Loc: time.UTC})
		if got.Lat != nil {
			assert.True(t, geo.IsValidGPS(*got.Lat, *got.Lng))
		}
	})
}

// FuzzVideoExif checks that no file can make the box walk or the decode panic, and that a video
// dated from its EXIF always has a time and never a position or size. Its seeds run in plain go test.
func FuzzVideoExif(f *testing.F) {
	clip := exiftest.Photo{Make: "Panasonic", Model: "DC-TZ200", DateTimeOriginal: "2021:02:10 15:30:00", OffsetTimeOriginal: "+01:00",
		GPS: &exiftest.GPS{Lat: 10, Lng: 20}, PixelX: 1920, PixelY: 1080}
	whole := exiftest.PanasonicMP4(clip)
	for _, seed := range [][]byte{whole, whole[:len(whole)/2], whole[:0x4080], exiftest.JPEG(clip), nil} {
		f.Add(seed)
	}
	zone := CameraZone{Loc: time.UTC, Rules: panasonicRules}
	f.Fuzz(func(t *testing.T, data []byte) {
		got, ok := VideoExif(data, zone)
		if ok {
			assert.NotNil(t, got.TakenAt)
			assert.Nil(t, got.Lat)
			assert.Nil(t, got.WidthPx)
		}
	})
}
