package handlers

import "github.com/bvigh/geogallery/internal/ingest"

// TimeReport is the receipt of one upload or scan: how the capture times of the files it stored as
// new rows were read, as their capture_evidence records it. A duplicate was dated when it was first
// stored and a failed file was never stored, so neither is counted.
type TimeReport struct {
	BySource TimeSourceCounts `json:"by_source"`
	ByZone   TimeZoneCounts   `json:"by_zone"`
	// CameraZone names the zone a wall clock is read in when nothing else gives its offset
	// (ingest.CameraZone.Name).
	CameraZone string `json:"camera_zone"`
	// CameraZoneFrom says who named it: "batch" (this upload or scan), "user" (the owner's
	// setting) or "server" (CAMERA_TIMEZONE, or the server's own zone).
	CameraZoneFrom string `json:"camera_zone_from"`
	// AssumedZoneIsUTC is true when a file was read in the camera zone, that zone is UTC all year
	// and the upload or scan did not name it: CAMERA_TIMEZONE unset on a server running in UTC, or
	// the owner's setting or CAMERA_TIMEZONE set to UTC. Few camera clocks show UTC, so such a
	// file's time is likely an hour or more off. A zone named for the batch was chosen for those
	// very files, so it is not warned about.
	AssumedZoneIsUTC bool `json:"assumed_zone_is_utc"`
	// FileDateNames are the new files dated by their modified time (src mtime), in the order they
	// came. That time is often when the file was copied, not when it was taken.
	FileDateNames []string `json:"file_date_names"`

	warnUTC bool // the camera zone is UTC all year, and the batch did not name it
}

// TimeSourceCounts counts new files by what produced their taken_at (capture_evidence.src).
type TimeSourceCounts struct {
	ExifOriginal int `json:"exif_original"`
	ExifIFD0     int `json:"exif_ifd0"`
	Container    int `json:"container"`
	Mtime        int `json:"mtime"`
	None         int `json:"none"`
}

// TimeZoneCounts counts new files dated from EXIF by the rule that gave the wall clock its offset
// (capture_evidence.zone).
type TimeZoneCounts struct {
	GPS       int `json:"gps"`
	ClockRule int `json:"clock_rule"`
	Offset    int `json:"offset"`
	Camera    int `json:"camera"`
}

// newTimeReport starts the receipt of files read in zone, the camera zone of the registrar that
// stores them (MediaRegistrar.forOwner).
func newTimeReport(zone ingest.CameraZone) TimeReport {
	return TimeReport{
		CameraZone:     zone.Name(),
		CameraZoneFrom: zone.Source(),
		FileDateNames:  []string{},
		warnUTC:        ingest.UTCAllYear(zone.Loc) && zone.Source() != ingest.CameraZoneFromBatch,
	}
}

// add counts a file registerMedia stored as a new row under filename.
func (r *TimeReport) add(filename string, res registerResult) {
	switch res.src {
	case captureSrcExifOriginal:
		r.BySource.ExifOriginal++
	case captureSrcExifIFD0:
		r.BySource.ExifIFD0++
	case captureSrcContainer:
		r.BySource.Container++
	case captureSrcMtime:
		r.BySource.Mtime++
		r.FileDateNames = append(r.FileDateNames, filename)
	case captureSrcNone:
		r.BySource.None++
	}
	switch res.zone {
	case ingest.ZoneGPS:
		r.ByZone.GPS++
	case ingest.ZoneClockRule:
		r.ByZone.ClockRule++
	case ingest.ZoneOffset:
		r.ByZone.Offset++
	case ingest.ZoneCamera:
		r.ByZone.Camera++
	}
	r.AssumedZoneIsUTC = r.ByZone.Camera > 0 && r.warnUTC
}
