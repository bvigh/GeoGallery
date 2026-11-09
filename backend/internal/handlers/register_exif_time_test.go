package handlers

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterMedia_ExifTimeZone pins that the stored taken_at and the EXIF GPS track point come
// from the registrar's camera zone (or the photo's GPS clock), never from the server's own zone.
func TestRegisterMedia_ExifTimeZone(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	mtime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		photo     exiftest.Photo
		zone      *time.Location
		want      string
		wantTrack bool
	}{
		{"camera zone", exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53"}, warsaw, "2018-10-23T09:50:53Z", false},
		{"no camera zone reads UTC", exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53"}, nil, "2018-10-23T11:50:53Z", false},
		{"GPS clock abroad, track point at the same instant",
			exiftest.Photo{DateTimeOriginal: "2023:05:01 18:00:00", GPS: &exiftest.GPS{Lat: 35.68, Lng: 139.76, Time: time.Date(2023, 5, 1, 9, 0, 3, 0, time.UTC)}},
			warsaw, "2023-05-01T09:00:00Z", true},
		{"EXIF GPS without a GPS clock, track point in the camera zone",
			exiftest.Photo{DateTimeOriginal: "2018:10:23 11:50:53", GPS: &exiftest.GPS{Lat: 47.4979, Lng: 19.0402}},
			warsaw, "2018-10-23T09:50:53Z", true},
		{"a phone abroad by OffsetTimeOriginal, track point at the same instant",
			exiftest.Photo{Make: "samsung", DateTimeOriginal: "2025:07:14 15:30:00", OffsetTimeOriginal: "+01:00", GPS: &exiftest.GPS{Lat: 51.5074, Lng: -0.1278}},
			warsaw, "2025-07-14T14:30:00Z", true},
		{"no EXIF time falls back to the file time", exiftest.Photo{}, warsaw, "2026-01-01T00:00:00Z", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &mockRegistrarDB{}
			reg := &MediaRegistrar{db: db, storage: &mockUploadStorage{}, cameraZone: tt.zone}
			photo := exiftest.JPEG(tt.photo)
			_, inserted, err := reg.registerMedia(context.Background(), "owner-1", bytes.NewReader(photo), int64(len(photo)), "P1011841.JPG", &mtime)
			require.NoError(t, err)
			require.True(t, inserted)

			want, err := time.Parse(time.RFC3339, tt.want)
			require.NoError(t, err)
			var takenAt, trackAt *time.Time
			for i, sql := range db.sqls {
				switch {
				case strings.Contains(sql, "INSERT INTO media"):
					takenAt = db.args[i][7].(*time.Time)
				case strings.Contains(sql, "INSERT INTO gps_tracks"):
					at := db.args[i][1].(time.Time)
					trackAt = &at
				}
			}
			require.NotNil(t, takenAt)
			assert.True(t, want.Equal(*takenAt), "taken_at %s, want %s", takenAt.UTC(), tt.want)
			if !tt.wantTrack {
				assert.Nil(t, trackAt)
				return
			}
			require.NotNil(t, trackAt, "EXIF GPS becomes a track point")
			assert.True(t, want.Equal(*trackAt), "track point at %s, want %s", trackAt.UTC(), tt.want)
		})
	}
}

func TestJSONCanCarry(t *testing.T) {
	east, west := time.FixedZone("", 14*3600), time.FixedZone("", -12*3600)
	tests := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"an ordinary date", time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC), true},
		{"the first instant of year 1", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"the last second of year 0", time.Date(0, 12, 31, 23, 59, 59, 0, time.UTC), false},
		{"the last second of year 9998", time.Date(9998, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"the first instant of year 9999", time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"west of UTC, past the end in UTC", time.Date(9998, 12, 31, 23, 0, 0, 0, west), false},
		{"east of UTC, still inside in UTC", time.Date(9999, 1, 1, 0, 0, 0, 0, east), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, jsonCanCarry(tt.t))
		})
	}
}
