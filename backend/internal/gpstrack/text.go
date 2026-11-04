package gpstrack

import (
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// --- NMEA 0183 (dashcams) ---

// parseNMEA reads the RMC sentences with an active fix (status A). Loom reads $GPRMC only; any
// talker is accepted here, so multi-GNSS receivers ($GNRMC) work too.
func parseNMEA(r io.Reader) ([]Point, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var points []Point
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if len(line) < 6 || line[0] != '$' || line[3:6] != "RMC" {
			continue
		}
		if i := strings.LastIndexByte(line, '*'); i >= 0 {
			line = line[:i] // the checksum is not verified, as in Loom
		}
		// $GPRMC,HHMMSS.ss,A,DDMM.MM,N,DDDMM.MM,E,speed,heading,DDMMYY,...
		f := strings.Split(line, ",")
		if len(f) < 10 || f[2] != "A" || f[1] == "" || f[3] == "" || f[5] == "" || f[9] == "" {
			continue
		}
		t, ok := nmeaTime(f[9], f[1])
		if !ok {
			continue
		}
		lat, err1 := nmeaDegrees(f[3], f[4])
		lng, err2 := nmeaDegrees(f[5], f[6])
		if err1 != nil || err2 != nil {
			continue
		}
		points = append(points, Point{Time: t, Lat: lat, Lng: lng})
	}
	return points, sc.Err()
}

// nmeaDegrees converts DDMM.MMMM or DDDMM.MMMM to decimal degrees, negative for S and W.
func nmeaDegrees(value, hemisphere string) (float64, error) {
	dot := strings.IndexByte(value, '.')
	if dot < 3 {
		return 0, fmt.Errorf("bad NMEA coordinate %q", value)
	}
	deg, err := strconv.Atoi(value[:dot-2])
	if err != nil {
		return 0, err
	}
	min, err := strconv.ParseFloat(value[dot-2:], 64)
	if err != nil {
		return 0, err
	}
	dd := float64(deg) + min/60
	if hemisphere == "S" || hemisphere == "W" {
		dd = -dd
	}
	return dd, nil
}

// nmeaTime combines DDMMYY and HHMMSS.sss (UTC). Years 80–99 are 19xx, as in Loom.
func nmeaTime(date, clock string) (time.Time, bool) {
	if len(date) != 6 || len(clock) < 6 {
		return time.Time{}, false
	}
	var n [5]int
	for i, s := range []string{date[0:2], date[2:4], date[4:6], clock[0:2], clock[2:4]} {
		v, err := strconv.Atoi(s)
		if err != nil || v < 0 {
			return time.Time{}, false
		}
		n[i] = v
	}
	day, month, yy, hour, minute := n[0], n[1], n[2], n[3], n[4]
	year := 2000 + yy
	if yy >= 80 {
		year = 1900 + yy
	}
	sec, err := strconv.ParseFloat(clock[4:], 64)
	if err != nil || !(sec >= 0 && sec < 60) || hour > 23 || minute > 59 {
		return time.Time{}, false
	}
	whole := int(sec)
	t := time.Date(year, time.Month(month), day, hour, minute, whole, int((sec-float64(whole))*1e6)*1e3, time.UTC)
	// time.Date normalises 31 June to 1 July; Python's datetime refuses it, so refuse it too.
	if t.Day() != day || int(t.Month()) != month {
		return time.Time{}, false
	}
	return t, true
}

// --- esoko CSV log ---

// esokoColumns is the column order of the older esoko logs, which have no header row.
var esokoColumns = []string{"time", "speed", "heading", "latitude", "longitude", "altitude"}

// parseEsokoLog reads the esoko log: a CSV with the header time,speed,heading,latitude,
// longitude,altitude, where time is unix seconds. Rows without a position are skipped. A log
// whose first row is data has those columns in that order (Loom reads no points from it).
func parseEsokoLog(r io.Reader) ([]Point, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	first, err := cr.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	header, headerless := first, false
	if _, err := parseFloat(first[0]); err == nil {
		header, headerless = esokoColumns, true
	}
	col := map[string]int{}
	for i, name := range header {
		col[strings.TrimSpace(name)] = i
	}
	iTime, ok1 := col["time"]
	iLat, ok2 := col["latitude"]
	iLng, ok3 := col["longitude"]
	if !ok1 || !ok2 || !ok3 {
		return nil, errors.New("not an esoko log: the header needs time, latitude and longitude")
	}
	iAlt, hasAlt := col["altitude"]

	field := func(rec []string, i int) string {
		if i < len(rec) {
			return rec[i]
		}
		return ""
	}
	point := func(rec []string) (Point, bool) {
		latS, lngS := field(rec, iLat), field(rec, iLng)
		if latS == "" || lngS == "" {
			return Point{}, false
		}
		sec, err1 := parseFloat(field(rec, iTime))
		lat, err2 := parseFloat(latS)
		lng, err3 := parseFloat(lngS)
		if err1 != nil || err2 != nil || err3 != nil {
			return Point{}, false
		}
		t, ok := unixTime(sec)
		if !ok {
			return Point{}, false
		}
		p := Point{Time: t, Lat: lat, Lng: lng}
		if hasAlt {
			p.AltitudeM = optFloat(field(rec, iAlt))
		}
		return p, true
	}

	var points []Point
	if headerless {
		if p, ok := point(first); ok {
			points = append(points, p)
		}
	}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return points, nil
		}
		if err != nil {
			return nil, err
		}
		if p, ok := point(rec); ok {
			points = append(points, p)
		}
	}
}
