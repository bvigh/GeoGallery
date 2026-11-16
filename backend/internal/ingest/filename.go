package ingest

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// NameTime is a capture time read from a file's name: the last fallback before the file's modified
// time.
type NameTime struct {
	Wall string    // the name's date and time as "2006-01-02 15:04:05"
	At   time.Time // Wall read in the camera zone
}

// The years a date in a file name may have. Before 1990 no camera named its files by the date, and
// a name that reads as one (a reset clock's 19700101_000000, a counter) is not a capture time. After
// 2099 it is not one either.
const (
	nameFirstYear = 1990
	nameLastYear  = 2099
)

// nameDateTime matches a date and a time with seconds at the start of a run of digits, as phones and
// apps name their files:
//
//	20230729_210400          Samsung; IMG_, VID_ and PANO_ before it on many Android phones
//	2012-08-24_18-14-24_124  Motorola (the _124 is milliseconds, which are not read)
//	2023-07-29-21-04-00      Samsung screenshots; Screenshot_20230729-210400 on other Android phones
//	2012-08-24 18.14.24      Dropbox camera uploads
//	WP_20140712_12_34_56_Pro Windows Phone
//
// The date's two separators must be the same, and so must the time's (checked in filenameWall, as
// RE2 has no backreferences). The date and the time must be apart: fourteen digits in a row
// (IMG20230729210400, some Oppo and OnePlus phones) are not read, since so long a number is as
// likely a counter or an ID.
var nameDateTime = regexp.MustCompile(`^(\d{4})([-_.]?)(\d{2})([-_.]?)(\d{2})[-_ T](\d{2})([-_.]?)(\d{2})([-_.]?)(\d{2})`)

// FilenameTime returns the date and time filename holds, read in zone's camera zone (nil Loc reads
// UTC), with that zone's daylight saving on that date, as an EXIF time without an offset is
// (ExifData.TakenAt with ZoneCamera). The owner's clock rules never apply: a name says nothing of
// the camera that wrote it. ok is false when the name holds no date and time, or two different ones.
func FilenameTime(filename string, zone CameraZone) (NameTime, bool) {
	wall, ok := filenameWall(filename)
	if !ok {
		return NameTime{}, false
	}
	loc := zone.Loc
	if loc == nil {
		loc = time.UTC
	}
	at := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc)
	return NameTime{Wall: wall.Format(time.DateTime), At: at}, true
}

// filenameWall returns the wall clock filename holds, as a time in UTC. Only the last path element
// is read, split at either kind of slash, so a folder named by a date never dates its files.
//
// A date and time starts where a run of digits starts and ends where one ends, so neither a longer
// number (a Unix time in milliseconds, 120230729_210400) nor a Pixel's PXL_20230729_210400123, whose
// time is UTC, is read. It must be a real date and time of day in nameFirstYear to nameLastYear, with
// seconds. Every date and time in the name must agree, so an edited copy named after two photos
// gives none.
func filenameWall(filename string) (time.Time, bool) {
	name := filename[strings.LastIndexAny(filename, `/\`)+1:]
	var found time.Time
	for i := 0; i < len(name); i++ {
		if !isDigit(name[i]) || (i > 0 && isDigit(name[i-1])) {
			continue
		}
		wall, ok := nameWallAt(name[i:])
		switch {
		case !ok:
		case found.IsZero():
			found = wall
		case !found.Equal(wall):
			return time.Time{}, false
		}
	}
	return found, !found.IsZero()
}

// nameWallAt reads a date and time at the start of s, or reports false.
func nameWallAt(s string) (time.Time, bool) {
	m := nameDateTime.FindStringSubmatch(s)
	if m == nil || m[2] != m[4] || m[7] != m[9] {
		return time.Time{}, false
	}
	if end := len(m[0]); end < len(s) && isDigit(s[end]) {
		return time.Time{}, false
	}
	var n [6]int
	for i, group := range []string{m[1], m[3], m[5], m[6], m[8], m[10]} {
		n[i], _ = strconv.Atoi(group) // the pattern allows digits only
	}
	year, month, day, hour, minute, second := n[0], n[1], n[2], n[3], n[4], n[5]
	if year < nameFirstYear || year > nameLastYear || hour > 23 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	wall := time.Date(year, time.Month(month), day, hour, minute, second, 0, time.UTC)
	// time.Date normalises 31 April to 1 May, and month 13 to January: a date it moved is no date.
	if wall.Month() != time.Month(month) || wall.Day() != day {
		return time.Time{}, false
	}
	return wall, true
}

func isDigit(b byte) bool {
	return '0' <= b && b <= '9'
}
