package gpstrack

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nmeaSample = `$GPGSV,4,4,15,39,17,126,,40,21,135,,41,06,109,*4F
$GSENSORD,0.060,0.060,-0.180
$GPRMC,095452.000,A,5127.5794,N,01653.0359,E,66.12,180.90,310823,,,A*59
$GPGGA,095452.000,5127.5794,N,01653.0359,E,1,11,0.7,94.3,M,40.0,M,,0000*6E
$GPRMC,095453.000,V,5127.5611,N,01653.0360,E,65.53,179.46,310823,,,A*55
$GPRMC,095454.000,A,5127.5430,N,01653.0367,E,65.29,178.42,310823,,,A*5C
`

func TestNMEA(t *testing.T) {
	_, points := mustParse(t, "FILE230831-095452.NMEA", nmeaSample)
	require.Len(t, points, 2, "the void (V) fix is skipped")
	assert.Equal(t, utc("2023-08-31T09:54:52Z"), points[0].Time)
	assert.InDelta(t, 51+27.5794/60, points[0].Lat, 1e-9)
	assert.InDelta(t, 16+53.0359/60, points[0].Lng, 1e-9)
	assert.Equal(t, utc("2023-08-31T09:54:54Z"), points[1].Time)
}

func TestNMEA_Sentences(t *testing.T) {
	tests := []struct {
		label string
		line  string
		want  string // RFC3339 time of the point; "" for none
	}{
		{"GNRMC (multi-GNSS) is read", "$GNRMC,120000.00,A,4730.0000,N,01900.0000,E,0.0,0.0,150624,,,A*00", "2024-06-15T12:00:00Z"},
		{"CRLF and fraction", "$GPRMC,120000.250,A,4730.0000,N,01900.0000,E,,,150624,,\r", "2024-06-15T12:00:00.25Z"},
		{"1999", "$GPRMC,120000,A,4730.0000,N,01900.0000,E,,,150699,,", "1999-06-15T12:00:00Z"},
		{"31 June", "$GPRMC,120000,A,4730.0000,N,01900.0000,E,,,310624,,", ""},
		{"hour 24", "$GPRMC,240000,A,4730.0000,N,01900.0000,E,,,150624,,", ""},
		{"negative minute", "$GPRMC,12-100,A,4730.0000,N,01900.0000,E,,,150624,,", ""},
		{"no date", "$GPRMC,120000,A,4730.0000,N,01900.0000,E,,,,,", ""},
		{"too few fields", "$GPRMC,120000,A,4730.0000,N,01900.0000,E", ""},
		{"no position", "$GPRMC,120000,A,,,,,,,150624,,", ""},
		{"GGA is not read", "$GPGGA,120000,4730.0000,N,01900.0000,E,1,11,0.7,94.3,M,40.0,M,,0000*6E", ""},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			_, points := mustParse(t, "x.nmea", tt.line+"\n")
			if tt.want == "" {
				assert.Empty(t, points)
				return
			}
			require.Len(t, points, 1)
			assert.Equal(t, utc(tt.want), points[0].Time)
		})
	}
}

func TestNMEADegrees(t *testing.T) {
	tests := []struct {
		value, hemisphere string
		want              float64
	}{
		{"5127.5794", "N", 51 + 27.5794/60},
		{"01653.0359", "E", 16 + 53.0359/60},
		{"3302.1234", "S", -(33 + 2.1234/60)},
		{"07430.0000", "W", -(74 + 30.0/60)},
	}
	for _, tt := range tests {
		got, err := nmeaDegrees(tt.value, tt.hemisphere)
		require.NoError(t, err, tt.value)
		assert.InDelta(t, tt.want, got, 1e-9, tt.value)
	}
	for _, bad := range []string{"", "5127", "12.5", "ab27.5"} {
		_, err := nmeaDegrees(bad, "N")
		assert.Error(t, err, bad)
	}
}

const esokoSample = `time,speed,heading,latitude,longitude,altitude
1691051735.000,10.88,303.8,46.535585,8.573741,1937.3
1691051736.000,11.71,300.2,46.535639,8.573613,1936.4
1691051737.000,12.51,297.2,46.535691,8.573472,1935.6
`

func TestEsokoLog(t *testing.T) {
	_, points := mustParse(t, "gps_svajc-5-ev_2017-8-10.log", esokoSample)
	require.Len(t, points, 3)
	assert.Equal(t, utc("2023-08-03T08:35:35Z"), points[0].Time)
	assert.Equal(t, 46.535585, points[0].Lat)
	assert.Equal(t, 8.573741, points[0].Lng)
	require.NotNil(t, points[0].AltitudeM)
	assert.Equal(t, 1937.3, *points[0].AltitudeM)
}

func TestEsokoLog_Rows(t *testing.T) {
	log := `time,speed,heading,latitude,longitude,altitude
1691051735.5,0,0,,,
1691051736,0,0,46.5,8.5,
1691051737,0,0,46.6,8.6,high
x,0,0,46.7,8.7,1
1691051738,0,0,46.8
`
	_, points := mustParse(t, "x.log", log)
	require.Len(t, points, 2, "rows without a position, with a bad time or cut short are skipped")
	assert.Nil(t, points[0].AltitudeM)
	assert.Nil(t, points[1].AltitudeM, "an unreadable altitude is missing")
	assert.Equal(t, 46.6, points[1].Lat)
}

func TestEsokoLog_Headerless(t *testing.T) {
	// Older esoko logs start with data; Loom takes the first row for a header and reads nothing.
	log := `1376200286.206,0.00,0.0,47.584791,19.108958,202.0
1376200287.206,1.50,90.0,47.584800,19.108990,203.5
`
	_, points := mustParse(t, "gps_2013.log", log)
	require.Len(t, points, 2, "the first row is a point too")
	assert.Equal(t, utc("2013-08-11T05:51:26.206Z"), points[0].Time)
	assert.Equal(t, 47.584791, points[0].Lat)
	assert.Equal(t, 19.108958, points[0].Lng)
	require.NotNil(t, points[1].AltitudeM)
	assert.Equal(t, 203.5, *points[1].AltitudeM)
}

func TestEsokoLog_NotAnEsokoLog(t *testing.T) {
	_, _, err := Parse("server.log", strings.NewReader("2024-06-15 12:00:00 INFO started\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an esoko log")

	_, points := mustParse(t, "empty.log", "")
	assert.Empty(t, points)
}
