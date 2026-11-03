package gpstrack

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gpxSample = `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1" xmlns="http://www.topografix.com/GPX/1/1">
  <trk>
    <name>Test hike</name>
    <trkseg>
      <trkpt lat="47.5403860" lon="19.0367950">
        <ele>104.8</ele>
        <time>2022-04-03T13:08:40Z</time>
      </trkpt>
      <trkpt lat="47.5403910" lon="19.0368040">
        <ele>105.0</ele>
        <time>2022-04-03T13:08:41Z</time>
      </trkpt>
      <trkpt lat="47.5403840" lon="19.0368030">
        <time>2022-04-03T13:08:42Z</time>
      </trkpt>
    </trkseg>
  </trk>
</gpx>
`

func TestGPX(t *testing.T) {
	_, points := mustParse(t, "activity.gpx", gpxSample)
	require.Len(t, points, 3)
	assert.Equal(t, utc("2022-04-03T13:08:40Z"), points[0].Time)
	assert.Equal(t, 47.5403860, points[0].Lat)
	assert.Equal(t, 19.0367950, points[0].Lng)
	require.NotNil(t, points[0].AltitudeM)
	assert.Equal(t, 104.8, *points[0].AltitudeM)
	assert.Nil(t, points[2].AltitudeM, "ele is optional")
}

func TestGPX_Variants(t *testing.T) {
	tests := []struct {
		label  string
		doc    string
		points int
	}{
		{"GPX 1.0 namespace", `<gpx xmlns="http://www.topografix.com/GPX/1/0"><trk><trkseg>
			<trkpt lat="1" lon="2"><time>2022-04-03T13:08:40Z</time></trkpt></trkseg></trk></gpx>`, 1},
		{"no namespace", `<gpx><trkpt lat="1" lon="2"><time>2022-04-03T13:08:40Z</time></trkpt></gpx>`, 1},
		{"a point without time is skipped (planned routes)", `<gpx><trkpt lat="1" lon="2"><ele>5</ele></trkpt></gpx>`, 0},
		{"waypoints and route points are not track points", `<gpx>
			<wpt lat="1" lon="2"><time>2022-04-03T13:08:40Z</time></wpt>
			<rte><rtept lat="1" lon="2"><time>2022-04-03T13:08:41Z</time></rtept></rte></gpx>`, 0},
		{"bad coordinate", `<gpx><trkpt lat="north" lon="2"><time>2022-04-03T13:08:40Z</time></trkpt></gpx>`, 0},
		{"a time inside extensions is not the point's time", `<gpx><trkpt lat="1" lon="2">
			<extensions><time>2022-04-03T13:08:40Z</time></extensions></trkpt></gpx>`, 0},
		{"empty document", ``, 0},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			_, points := mustParse(t, "x.gpx", tt.doc)
			assert.Len(t, points, tt.points)
		})
	}
}

func TestGPX_BadElevationIsMissing(t *testing.T) {
	// Loom raises on an unreadable <ele> and loses the whole file; the point is kept here.
	_, points := mustParse(t, "x.gpx",
		`<gpx><trkpt lat="1" lon="2"><ele>n/a</ele><time>2022-04-03T13:08:40Z</time></trkpt></gpx>`)
	require.Len(t, points, 1)
	assert.Nil(t, points[0].AltitudeM)
}

func TestGPX_Latin1(t *testing.T) {
	doc := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?>\n<gpx><trk><name>Bal\xe1ton</name><trkseg>" +
		`<trkpt lat="46.9" lon="17.9"><time>2022-06-10T10:00:00Z</time></trkpt></trkseg></trk></gpx>`
	_, points := mustParse(t, "x.gpx", doc)
	assert.Len(t, points, 1)

	_, _, err := Parse("x.gpx", strings.NewReader(`<?xml version="1.0" encoding="Shift_JIS"?><gpx/>`))
	assert.ErrorContains(t, err, "Shift_JIS")
}

func TestLatin1Reader(t *testing.T) {
	r := &latin1Reader{r: strings.NewReader("a\xe1\xffz")}
	var out bytes.Buffer
	buf := make([]byte, 3) // reads one input byte at a time
	for {
		n, err := r.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			break
		}
	}
	assert.Equal(t, "aáÿz", out.String())
}

const tcxSample = `<?xml version="1.0" encoding="UTF-8"?>
<TrainingCenterDatabase
  xmlns="http://www.garmin.com/xmlschemas/TrainingCenterDatabase/v2"
  xmlns:ns3="http://www.garmin.com/xmlschemas/ActivityExtension/v2"
  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
  <Activities>
    <Activity Sport="Ride">
      <Id>2023-05-20T08:54:43.000Z</Id>
      <Lap StartTime="2023-05-20T08:54:43.000Z">
        <Track>
          <Trackpoint>
            <Time>2023-05-20T08:54:43.000Z</Time>
            <Position>
              <LatitudeDegrees>50.00742797</LatitudeDegrees>
              <LongitudeDegrees>20.01809136</LongitudeDegrees>
            </Position>
            <AltitudeMeters>273.89</AltitudeMeters>
            <Extensions>
              <ns3:TPX><ns3:Speed>0.0</ns3:Speed></ns3:TPX>
            </Extensions>
          </Trackpoint>
          <Trackpoint>
            <Time>2023-05-20T08:54:50.000Z</Time>
            <Position>
              <LatitudeDegrees>50.00744070</LatitudeDegrees>
              <LongitudeDegrees>20.01806864</LongitudeDegrees>
            </Position>
            <AltitudeMeters>272.75</AltitudeMeters>
            <Extensions>
              <ns3:TPX><ns3:Speed>3.5</ns3:Speed></ns3:TPX>
            </Extensions>
          </Trackpoint>
          <Trackpoint>
            <Time>2023-05-20T08:54:57.000Z</Time>
            <!-- No Position element — should be skipped -->
            <AltitudeMeters>271.00</AltitudeMeters>
          </Trackpoint>
        </Track>
      </Lap>
    </Activity>
  </Activities>
</TrainingCenterDatabase>
`

func TestTCX(t *testing.T) {
	_, points := mustParse(t, "ride.tcx", tcxSample)
	require.Len(t, points, 2, "the Trackpoint without a Position is skipped")
	assert.Equal(t, utc("2023-05-20T08:54:43Z"), points[0].Time)
	assert.Equal(t, 50.00742797, points[0].Lat)
	assert.Equal(t, 20.01809136, points[0].Lng)
	require.NotNil(t, points[0].AltitudeM)
	assert.Equal(t, 273.89, *points[0].AltitudeM)
}

func TestTCX_LeadingWhitespace(t *testing.T) {
	// Some Strava TCX files start with whitespace before the XML declaration.
	_, points := mustParse(t, "ride.tcx", "\n          "+tcxSample)
	assert.Len(t, points, 2)
}

const kmlSample = `<?xml version="1.0" encoding="UTF-8"?>
<kml xmlns="http://www.opengis.net/kml/2.2"
     xmlns:gx="http://www.google.com/kml/ext/2.2">
  <Document>
    <Placemark>
      <gx:Track>
        <when>2013-08-14T15:26:46Z</when>
        <gx:coord>9.475613 47.068538 507.0</gx:coord>
        <when>2013-08-14T15:26:47Z</when>
        <gx:coord>9.475407 47.068467 513.0</gx:coord>
      </gx:Track>
    </Placemark>
    <Placemark>
      <LineString>
        <coordinates>13.733500,45.575700,0 13.734000,45.576000,0</coordinates>
      </LineString>
      <TimeSpan>
        <begin>2022-08-04T19:49:37Z</begin>
        <end>2022-08-04T20:49:37Z</end>
      </TimeSpan>
    </Placemark>
    <Placemark>
      <Point>
        <coordinates>13.7357201,45.5777100,0</coordinates>
      </Point>
      <TimeSpan>
        <begin>2022-08-05T19:19:01Z</begin>
        <end>2022-08-05T19:25:00Z</end>
      </TimeSpan>
    </Placemark>
  </Document>
</kml>
`

func TestKML(t *testing.T) {
	_, points := mustParse(t, "history-2022-08-04.kml", kmlSample)
	require.Len(t, points, 5)

	assert.Equal(t, utc("2013-08-14T15:26:46Z"), points[0].Time, "gx:Track pairs when and coord")
	assert.Equal(t, 47.068538, points[0].Lat)
	assert.Equal(t, 9.475613, points[0].Lng)
	require.NotNil(t, points[0].AltitudeM)
	assert.Equal(t, 507.0, *points[0].AltitudeM)
	assert.Equal(t, utc("2013-08-14T15:26:47Z"), points[1].Time)

	assert.Equal(t, utc("2022-08-04T19:49:37Z"), points[2].Time, "LineString starts at begin")
	assert.Equal(t, 45.5757, points[2].Lat)
	assert.Equal(t, utc("2022-08-04T20:49:37Z"), points[3].Time, "and ends at end")
	assert.Equal(t, 45.576, points[3].Lat)

	assert.Equal(t, utc("2022-08-05T19:19:01Z"), points[4].Time, "a Point at begin")
	assert.Equal(t, 45.57771, points[4].Lat)
	assert.Equal(t, 13.7357201, points[4].Lng)
}

func TestKML_Placemarks(t *testing.T) {
	tests := []struct {
		label string
		body  string
		want  []string // point times
	}{
		{"three vertices spread over the span", `<Placemark><TimeSpan><begin>2022-08-04T19:00:00Z</begin><end>2022-08-04T20:00:00Z</end></TimeSpan>
			<LineString><coordinates>13.1,45.1 13.2,45.2 13.3,45.3</coordinates></LineString></Placemark>`,
			[]string{"2022-08-04T19:00:00Z", "2022-08-04T19:30:00Z", "2022-08-04T20:00:00Z"}},
		{"TimeStamp", `<Placemark><TimeStamp><when>2022-08-04T19:00:00Z</when></TimeStamp>
			<Point><coordinates>13.1,45.1</coordinates></Point></Placemark>`,
			[]string{"2022-08-04T19:00:00Z"}},
		{"TimeSpan without end", `<Placemark><TimeSpan><begin>2022-08-04T19:00:00Z</begin></TimeSpan>
			<LineString><coordinates>13.1,45.1 13.2,45.2</coordinates></LineString></Placemark>`,
			[]string{"2022-08-04T19:00:00Z", "2022-08-04T19:00:00Z"}},
		{"no time", `<Placemark><Point><coordinates>13.1,45.1</coordinates></Point></Placemark>`, nil},
		{"TimeSpan without begin", `<Placemark><TimeSpan><end>2022-08-04T19:00:00Z</end></TimeSpan>
			<Point><coordinates>13.1,45.1</coordinates></Point></Placemark>`, nil},
		{"a Point wins over a LineString", `<Placemark><TimeStamp><when>2022-08-04T19:00:00Z</when></TimeStamp>
			<MultiGeometry><Point><coordinates>13.1,45.1</coordinates></Point>
			<LineString><coordinates>13.1,45.1 13.2,45.2</coordinates></LineString></MultiGeometry></Placemark>`,
			[]string{"2022-08-04T19:00:00Z"}},
		{"gx:Track with an empty when keeps Loom's pairing", `<Placemark><gx:Track>
			<when>2013-08-14T15:26:46Z</when><when></when><gx:coord>9.4 47.0</gx:coord><gx:coord>9.5 47.1</gx:coord>
			</gx:Track></Placemark>`,
			[]string{"2013-08-14T15:26:46Z"}},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			doc := `<kml xmlns="http://www.opengis.net/kml/2.2" xmlns:gx="http://www.google.com/kml/ext/2.2"><Document>` +
				tt.body + `</Document></kml>`
			_, points := mustParse(t, "x.kml", doc)
			var got []string
			for _, p := range points {
				got = append(got, p.Time.Format("2006-01-02T15:04:05Z07:00"))
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func kmz(t *testing.T, files map[string]string) string {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return b.String()
}

func TestKMZ(t *testing.T) {
	format, points := mustParse(t, "Luzern.kmz", kmz(t, map[string]string{"doc.kml": kmlSample}))
	assert.Equal(t, FormatKMZ, format)
	assert.Len(t, points, 5)

	_, _, err := Parse("empty.kmz", strings.NewReader(kmz(t, map[string]string{"images/a.png": "x"})))
	assert.EqualError(t, err, "kmz: no .kml file in the archive")

	_, _, err = Parse("broken.kmz", strings.NewReader("not a zip"))
	assert.Error(t, err)
}
