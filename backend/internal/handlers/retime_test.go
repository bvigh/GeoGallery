package handlers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSameWallClock(t *testing.T) {
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	if err != nil {
		t.Fatal(err)
	}
	// The EXIF wall clock 2018:10:23 11:50:53, as ingest reads it today.
	now := time.Date(2018, 10, 23, 11, 50, 53, 0, warsaw)
	tests := []struct {
		name   string
		stored time.Time
		want   bool
	}{
		{"read as UTC (Docker)", time.Date(2018, 10, 23, 11, 50, 53, 0, time.UTC), true},
		{"read as Warsaw", time.Date(2018, 10, 23, 9, 50, 53, 0, time.UTC), true},
		{"read as +05:45", time.Date(2018, 10, 23, 6, 5, 53, 0, time.UTC), true},
		{"read as -12:00", time.Date(2018, 10, 23, 23, 50, 53, 0, time.UTC), true},
		{"14 hours east", time.Date(2018, 10, 22, 21, 50, 53, 0, time.UTC), true},
		{"more than 14 hours away", time.Date(2018, 10, 22, 20, 50, 53, 0, time.UTC), false},
		{"seven minutes off: edited by hand", time.Date(2018, 10, 23, 11, 57, 53, 0, time.UTC), false},
		{"a file time with milliseconds", time.Date(2018, 10, 23, 11, 50, 53, 123e6, time.UTC), false},
		{"a different day", time.Date(2018, 10, 24, 11, 50, 53, 0, time.UTC).Add(15 * time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sameWallClock(tt.stored, now))
		})
	}
}
