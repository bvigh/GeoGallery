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
		got := ExtractExif(bytes.NewReader(data), time.UTC)
		if got.Lat != nil {
			assert.True(t, geo.IsValidGPS(*got.Lat, *got.Lng))
		}
	})
}
