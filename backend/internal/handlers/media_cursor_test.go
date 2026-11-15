package handlers

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMediaCursor_RoundTrip(t *testing.T) {
	const id = "22222222-2222-4222-8222-222222222222"
	warsaw, err := time.LoadLocation("Europe/Warsaw")
	require.NoError(t, err)
	tests := []struct {
		name    string
		takenAt *time.Time
	}{
		{"undated", nil},
		{"UTC, microseconds", ptr(time.Date(2024, 5, 2, 12, 0, 0, 123456000, time.UTC))},
		{"a whole second", ptr(time.Date(2024, 5, 2, 12, 0, 0, 0, time.UTC))},
		// pgx returns times in the server's zone.
		{"the server's zone", ptr(time.Date(2024, 7, 1, 14, 0, 0, 1000, warsaw))},
		// Warsaw's local mean time; RFC 3339 has no seconds in an offset, so only UTC keeps the instant.
		{"an offset with seconds", ptr(time.Date(1880, 1, 1, 12, 0, 0, 0, warsaw))},
		{"the first year", ptr(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC))},
		{"the last year", ptr(time.Date(9999, 12, 31, 23, 59, 59, 999999000, time.UTC))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := mediaCursor{takenAt: tt.takenAt, id: id}.String()
			got, err := parseMediaCursor(s)
			require.NoError(t, err, s)
			assert.Equal(t, id, got.id)
			if tt.takenAt == nil {
				assert.Nil(t, got.takenAt)
				return
			}
			require.NotNil(t, got.takenAt)
			assert.True(t, tt.takenAt.Equal(*got.takenAt), "want %v, got %v", tt.takenAt, got.takenAt)
		})
	}
}

// Clients must not build cursors, but the text is plain: the time in RFC 3339 UTC, a space, the ID.
func TestMediaCursor_Encoding(t *testing.T) {
	at := time.Date(2024, 7, 1, 14, 0, 0, 1000, time.FixedZone("", 2*3600))
	s := mediaCursor{takenAt: &at, id: "22222222-2222-4222-8222-222222222222"}.String()
	raw, err := base64.RawURLEncoding.DecodeString(s)
	require.NoError(t, err, "base64url without padding, safe in a query string")
	assert.Equal(t, "2024-07-01T12:00:00.000001Z 22222222-2222-4222-8222-222222222222", string(raw))

	raw, err = base64.RawURLEncoding.DecodeString(mediaCursor{id: "22222222-2222-4222-8222-222222222222"}.String())
	require.NoError(t, err)
	assert.Equal(t, " 22222222-2222-4222-8222-222222222222", string(raw), "an undated item has no time")
}

func TestParseMediaCursor_CanonicalID(t *testing.T) {
	for _, raw := range []string{
		"22222222-2222-4222-8222-22222222ABCD",
		"22222222x2222x4222x8222x22222222abcd",
		"2222222222224222822222222222abcd",
	} {
		t.Run(raw, func(t *testing.T) {
			got, err := parseMediaCursor(base64.RawURLEncoding.EncodeToString([]byte(" " + raw)))
			require.NoError(t, err)
			assert.Equal(t, "22222222-2222-4222-8222-22222222abcd", got.id)
		})
	}
}

// A time String cannot write in four digits never comes from a listed item: ingest keeps times
// within the UTC years 1-9998. Such a cursor would not read back, so it is refused, not misread.
func TestMediaCursor_YearOutsideRFC3339(t *testing.T) {
	at := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := parseMediaCursor(mediaCursor{takenAt: &at, id: "22222222-2222-4222-8222-222222222222"}.String())
	assert.ErrorIs(t, err, errInvalidCursor)
}
