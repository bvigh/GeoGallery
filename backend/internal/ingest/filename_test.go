package ingest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilenameWall(t *testing.T) {
	tests := []struct {
		name string
		want string // "2006-01-02 15:04:05"; empty: no date and time
	}{
		// The forms phones and apps write.
		{"20230729_210400.jpg", "2023-07-29 21:04:00"},
		{"IMG_20230729_210400.jpg", "2023-07-29 21:04:00"},
		{"VID_20230729_210400.mp4", "2023-07-29 21:04:00"},
		{"PANO_20230729_210400.jpg", "2023-07-29 21:04:00"},
		{"2012-08-24_18-14-24_124.jpg", "2012-08-24 18:14:24"},
		{"2012-08-24_18-14-24_12.jpg", "2012-08-24 18:14:24"},
		{"Screenshot_2023-07-29-21-04-00-123_com.example.png", "2023-07-29 21:04:00"},
		{"Screenshot_20230729-210400_Chrome.jpg", "2023-07-29 21:04:00"},
		{"2012-08-24 18.14.24.jpg", "2012-08-24 18:14:24"},
		{"WP_20140712_12_34_56_Pro.jpg", "2014-07-12 12:34:56"},
		{"20230729T210400.jpg", "2023-07-29 21:04:00"},
		{"2023_07_29_21_04_00.jpg", "2023-07-29 21:04:00"},
		// What follows the time, as copies and bursts name themselves.
		{"20230729_210400(0).jpg", "2023-07-29 21:04:00"},
		{"20230729_210400 (Copy).jpg", "2023-07-29 21:04:00"},
		{"IMG_20230729_210400_1.jpg", "2023-07-29 21:04:00"},
		{"IMG_20230729_210400~2.jpg", "2023-07-29 21:04:00"},
		{"20230729_210400-EFFECTS.jpg", "2023-07-29 21:04:00"},
		{"VID_20220730_141234_00_034.mp4", "2022-07-30 14:12:34"},
		{"no extension 20230729_210400", "2023-07-29 21:04:00"},
		// The same date and time twice is one.
		{"20230729_210400_20230729_210400.jpg", "2023-07-29 21:04:00"},
		// Bounds.
		{"19900101_000000.jpg", "1990-01-01 00:00:00"},
		{"20991231_235959.jpg", "2099-12-31 23:59:59"},
		{"20240229_120000.jpg", "2024-02-29 12:00:00"},
		// Only the last path element counts, after either slash.
		{"2012-08-24_18-00-00/DSCF0001.jpg", ""},
		{`2012-08-24_18-00-00\DSCF0001.jpg`, ""},
		{"trip/20230729_210400.jpg", "2023-07-29 21:04:00"},
		{`trip\20230729_210400.jpg`, "2023-07-29 21:04:00"},

		// Names without a date and time.
		{"DSCF0372.AVI", ""},
		{"P1030729.JPG", ""},
		{"DSC_0001.JPG", ""},
		{"", ""},
		// A date alone: WhatsApp, a day's folder export, a counter after the date.
		{"IMG-20230729-WA0001.jpg", ""},
		{"2023-07-29.jpg", ""},
		{"2013-05-12-123.jpg", ""},
		{"20120824(001).jpg", ""},
		{"WP_20140712_001.jpg", ""},
		// A time without seconds.
		{"2023-07-29 21.04.jpg", ""},
		// Names that only look like dates.
		{"12345678_123456.jpg", ""},        // month 56
		{"20231301_120000.jpg", ""},        // month 13
		{"20230001_120000.jpg", ""},        // month 0
		{"20230431_120000.jpg", ""},        // 31 April
		{"20230229_120000.jpg", ""},        // not a leap year
		{"20230700_120000.jpg", ""},        // day 0
		{"20230729_240000.jpg", ""},        // hour 24
		{"20230729_216000.jpg", ""},        // minute 60
		{"20230729_210460.jpg", ""},        // second 60
		{"19700101_000000.jpg", ""},        // a reset clock
		{"19891231_235959.jpg", ""},        // before 1990
		{"21000101_000000.jpg", ""},        // after 2099
		{"20230729210400.jpg", ""},         // fourteen digits in a row
		{"IMG20230729210400.jpg", ""},      // the same after letters
		{"PHOTO_1690664640000.jpg", ""},    // a Unix time in milliseconds
		{"1690664640000.jpg", ""},          // the same alone
		{"120230729_210400.jpg", ""},       // a longer number before
		{"20230729_2104001.jpg", ""},       // a longer number after
		{"PXL_20230729_210400123.jpg", ""}, // a Pixel's, whose time is UTC
		{"2023-07-29_21-0400.jpg", ""},     // the time's separators differ
		{"2023-0729_21-04-00.jpg", ""},     // the date's separators differ
		{"2023-07-29_21:04:00.jpg", ""},    // colons are not read
		{"20230729__210400.jpg", ""},       // two separators between date and time
		// Two different dates and times.
		{"20230729_210400_20230730_120000.jpg", ""},
		{"IMG_20230729_210400 edited 2024-01-02 10-11-12.jpg", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := filenameWall(tt.name)
			if tt.want == "" {
				assert.False(t, ok, "read %v", got)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.want, got.Format(time.DateTime))
			assert.Equal(t, time.UTC, got.Location())
		})
	}
}

func TestFilenameTime_ReadsTheWallClockInTheCameraZone(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	kathmandu, err := time.LoadLocation("Asia/Kathmandu")
	require.NoError(t, err)

	tests := []struct {
		name     string
		filename string
		zone     CameraZone
		wantUTC  string
		wantWall string
	}{
		{"summer time on that date", "2012-08-24_18-14-24_124.jpg", CameraZone{Loc: warsaw, Set: true}, "2012-08-24T16:14:24Z", "2012-08-24 18:14:24"},
		{"winter time on that date", "20230115_120000.jpg", CameraZone{Loc: warsaw, Set: true}, "2023-01-15T11:00:00Z", "2023-01-15 12:00:00"},
		{"a fractional offset", "20230729_210400.jpg", CameraZone{Loc: kathmandu, Set: true}, "2023-07-29T15:19:00Z", "2023-07-29 21:04:00"},
		{"a zero zone reads UTC", "20230729_210400.jpg", CameraZone{}, "2023-07-29T21:04:00Z", "2023-07-29 21:04:00"},
		{"a zone named for a batch", "20230729_210400.jpg", CameraZone{}.In(warsaw, CameraZoneFromBatch), "2023-07-29T19:04:00Z", "2023-07-29 21:04:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FilenameTime(tt.filename, tt.zone)
			require.True(t, ok)
			assert.Equal(t, tt.wantUTC, got.At.UTC().Format(time.RFC3339))
			assert.Equal(t, tt.wantWall, got.Wall)
		})
	}
}

// The owner's clock rules describe cameras by the make and model in their EXIF, which a name lacks.
func TestFilenameTime_NeverAppliesAClockRule(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	zone := CameraZone{Loc: warsaw, Set: true, Clock: ClockRules{{ID: "any", Make: "Motorola", Model: "MB526", OffsetMin: 0}}}
	got, ok := FilenameTime("2012-08-24_18-14-24_124.jpg", zone)
	require.True(t, ok)
	assert.Equal(t, "2012-08-24T16:14:24Z", got.At.UTC().Format(time.RFC3339))
}

func TestFilenameTime_NoTime(t *testing.T) {
	_, ok := FilenameTime("DSCF0372.AVI", CameraZone{})
	assert.False(t, ok)
}

// Whatever the process's own zone, a zero zone reads UTC.
func TestFilenameTime_IndependentOfTheProcessZone(t *testing.T) {
	orig := time.Local
	t.Cleanup(func() { time.Local = orig })
	time.Local = time.FixedZone("elsewhere", 5*3600)
	got, ok := FilenameTime("20230729_210400.jpg", CameraZone{})
	require.True(t, ok)
	assert.Equal(t, "2023-07-29T21:04:00Z", got.At.UTC().Format(time.RFC3339))
}
