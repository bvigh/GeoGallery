package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/labstack/gommon/log"

	"github.com/bvigh/geogallery/internal/geo"
)

type VideoProbeResult struct {
	WidthPx         int
	HeightPx        int
	Equirectangular bool
	CreatedAt       *time.Time
	CodecName       string // e.g. "h264", "mpeg4", "hevc"
	CodecTag        string // e.g. "avc1", "mp4v", "hvc1"
	// Lat and Lng are the container's location tag, nil when it is absent or unusable.
	Lat, Lng *float64
	// DurationS is the container's duration in seconds, nil when it is absent or unusable.
	DurationS *float64
	Encoder   string // format.tags.encoder after CleanTag, e.g. "Lavf60.16.100"
	// RawTags is ffprobe's format.tags and the first video stream's tags as written, prefixed
	// "format." and "stream.", for the tag list of a file re-read.
	RawTags map[string]string
}

// videoDemuxers lists the only input formats ffprobe and ffmpeg may open: every container ingest
// accepts. Left to detect the format from the bytes, they treat a DASH manifest under a video
// extension as a manifest and fetch the URLs and local files it names, on the server.
const videoDemuxers = "mov,mp4,m4a,3gp,3g2,mj2,matroska,webm,avi"

// NeedsTranscode reports whether the video requires transcoding to H.264 for
// browser playback. Browsers only support H.264 (avc1), H.265/HEVC, VP8/VP9,
// and AV1; legacy codecs like MPEG-4 Part 2 (mp4v) are not decoded.
func (p VideoProbeResult) NeedsTranscode() bool {
	switch p.CodecName {
	case "h264", "hevc", "vp8", "vp9", "av1":
		return false
	case "":
		return false // unknown / no video stream — don't attempt transcode
	default:
		return true
	}
}

// TranscodeH264 re-encodes the file at srcPath to a new temp file using
// H.264 video + copy audio, with -movflags +faststart so the moov atom is
// at the front of the file (required for browser streaming). -map_metadata 0
// keeps the container's tags. creation_time is then written from createdAt,
// the time ingest read from the source, so the stored object says when it was
// recorded. A nil createdAt clears the tag: ffmpeg would otherwise turn a time
// with no zone, such as an AVI's IDIT chunk, into an instant in the server's
// own zone. The caller is responsible for closing and removing the returned
// *os.File.
func TranscodeH264(srcPath string, createdAt *time.Time) (*os.File, error) {
	creationTime := ""
	if createdAt != nil {
		creationTime = createdAt.UTC().Format(time.RFC3339)
	}

	out, err := os.CreateTemp("", "geogallery-transcode-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("transcode: create temp file: %w", err)
	}
	outPath := out.Name()

	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-format_whitelist", videoDemuxers,
		"-i", srcPath,
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "23",
		"-c:a", "aac",
		"-movflags", "+faststart",
		"-map_metadata", "0",
		"-metadata", "creation_time="+creationTime,
		outPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		out.Close()
		os.Remove(outPath)
		return nil, fmt.Errorf("transcode: ffmpeg: %w\n%s", err, output)
	}

	if _, err := out.Seek(0, io.SeekStart); err != nil {
		out.Close()
		os.Remove(outPath)
		return nil, fmt.Errorf("transcode: seek: %w", err)
	}
	return out, nil
}

// ProbeVideo writes the reader to a temp file, runs ffprobe, and returns
// video stream dimensions plus whether the stream is tagged as equirectangular.
// The caller is responsible for closing and removing the returned *os.File.
// Returns (nil, zero result) if ffprobe is unavailable or finds no video stream.
func ProbeVideo(r io.Reader) (*os.File, VideoProbeResult, error) {
	tmp, err := os.CreateTemp("", "geogallery-probe-*.mp4")
	if err != nil {
		return nil, VideoProbeResult{}, err
	}

	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, VideoProbeResult{}, err
	}

	out, err := exec.Command("ffprobe", ffprobeArgs(tmp.Name())...).Output()
	if err != nil {
		log.Print(fmt.Errorf("ffprobe failed: %w", err))
		return tmp, VideoProbeResult{}, nil
	}

	return tmp, parseProbeOutput(out), nil
}

// ProbeURL runs ffprobe on a video behind a URL. ffprobe fetches only the parts it needs through
// ranged requests, so nothing is downloaded whole, even a 360° video of several GB. The URL is
// typically presigned: it must never reach a log, and the returned error does not contain it.
// Cancelling ctx kills the process.
func ProbeURL(ctx context.Context, url string) (VideoProbeResult, error) {
	out, err := exec.CommandContext(ctx, "ffprobe", ffprobeArgs(url)...).Output()
	if err != nil {
		return VideoProbeResult{}, fmt.Errorf("ffprobe failed: %w", err)
	}
	return parseProbeOutput(out), nil
}

func ffprobeArgs(target string) []string {
	return []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-select_streams", "v:0",
		"-format_whitelist", videoDemuxers,
		target,
	}
}

// iso6709Degrees matches ISO 6709 in decimal degrees at the start of a tag: "+47.4979+019.0402/".
// Real tags can carry an altitude or trailing bytes. The degrees-and-minutes forms, such as
// "+4729.87+01902.41/", do not match, so they are refused instead of being read as far-off degrees.
var iso6709Degrees = regexp.MustCompile(`^([+-]\d{2}(?:\.\d+)?)([+-]\d{3}(?:\.\d+)?)`)

// parseISO6709 returns the position in a location tag. Phones without a fix write
// "+00.0000+000.0000/", which geo.IsValidGPS refuses.
func parseISO6709(s string) (lat, lng float64, ok bool) {
	m := iso6709Degrees.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	lat, errLat := strconv.ParseFloat(m[1], 64)
	lng, errLng := strconv.ParseFloat(m[2], 64)
	if errLat != nil || errLng != nil || !geo.IsValidGPS(lat, lng) {
		return 0, 0, false
	}
	return lat, lng, true
}

// parseProbeDuration reads ffprobe's format.duration. A missing, unparseable, NaN, infinite or
// non-positive value gives nil.
func parseProbeDuration(raw string) *float64 {
	d, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(d) || math.IsInf(d, 0) || d <= 0 {
		return nil
	}
	return &d
}

func parseProbeOutput(out []byte) VideoProbeResult {
	var parsed struct {
		Streams []struct {
			Width     int    `json:"width"`
			Height    int    `json:"height"`
			CodecName string `json:"codec_name"`
			CodecTag  string `json:"codec_tag_string"`
			SideData  []struct {
				Type       string `json:"side_data_type"`
				Projection string `json:"projection"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			// ffprobe writes the duration as a JSON string ("1.000000"). A float64 field here would
			// fail the Unmarshal below, and every video would lose everything else in this result.
			Duration string `json:"duration"`
			Tags     struct {
				CreationTime string `json:"creation_time"`
				Location     string `json:"location"`
				LocationEng  string `json:"location-eng"`
				LocationMOV  string `json:"com.apple.quicktime.location.ISO6709"`
				Encoder      string `json:"encoder"`
			} `json:"tags"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed.Streams) == 0 {
		return VideoProbeResult{}
	}

	s := parsed.Streams[0]
	equirect := false
	for _, sd := range s.SideData {
		if sd.Type == "Spherical Mapping" && sd.Projection == "equirectangular" {
			equirect = true
			break
		}
	}

	var createdAt *time.Time
	if raw := parsed.Format.Tags.CreationTime; raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			createdAt = &t
		}
	}

	result := VideoProbeResult{
		WidthPx:         s.Width,
		HeightPx:        s.Height,
		Equirectangular: equirect,
		CreatedAt:       createdAt,
		CodecName:       s.CodecName,
		CodecTag:        s.CodecTag,
		DurationS:       parseProbeDuration(parsed.Format.Duration),
		Encoder:         CleanTag(parsed.Format.Tags.Encoder),
		RawTags:         parseRawTags(out),
	}
	tags := parsed.Format.Tags
	for _, raw := range []string{tags.Location, tags.LocationEng, tags.LocationMOV} {
		if lat, lng, ok := parseISO6709(raw); ok {
			result.Lat, result.Lng = &lat, &lng
			break
		}
	}
	return result
}

// parseRawTags decodes every format and first-stream tag. It is a separate Unmarshal whose failure
// is ignored, so a tag of an odd type costs at most that tag and never the probe result.
func parseRawTags(out []byte) map[string]string {
	var parsed struct {
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	_ = json.Unmarshal(out, &parsed)

	raw := make(map[string]string, len(parsed.Format.Tags))
	for name, value := range parsed.Format.Tags {
		raw["format."+name] = value
	}
	if len(parsed.Streams) > 0 {
		for name, value := range parsed.Streams[0].Tags {
			raw["stream."+name] = value
		}
	}
	return raw
}
