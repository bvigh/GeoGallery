package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/labstack/gommon/log"
)

type VideoProbeResult struct {
	WidthPx         int
	HeightPx        int
	Equirectangular bool
	CreatedAt       *time.Time
	CodecName       string // e.g. "h264", "mpeg4", "hevc"
	CodecTag        string // e.g. "avc1", "mp4v", "hvc1"
}

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
// at the front of the file (required for browser streaming). The caller is
// responsible for closing and removing the returned *os.File.
func TranscodeH264(srcPath string) (*os.File, error) {
	out, err := os.CreateTemp("", "geogallery-transcode-*.mp4")
	if err != nil {
		return nil, fmt.Errorf("transcode: create temp file: %w", err)
	}
	outPath := out.Name()

	cmd := exec.Command(
		"ffmpeg",
		"-y",
		"-i", srcPath,
		"-c:v", "libx264",
		"-preset", "fast",
		"-crf", "23",
		"-c:a", "aac",
		"-movflags", "+faststart",
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

	out, err := exec.Command(
		"ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		"-select_streams", "v:0",
		tmp.Name(),
	).Output()
	if err != nil {
		log.Print(fmt.Errorf("ffprobe failed: %w", err))
		return tmp, VideoProbeResult{}, nil
	}

	return tmp, parseProbeOutput(out), nil
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
			Tags struct {
				CreationTime string `json:"creation_time"`
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

	return VideoProbeResult{
		WidthPx:         s.Width,
		HeightPx:        s.Height,
		Equirectangular: equirect,
		CreatedAt:       createdAt,
		CodecName:       s.CodecName,
		CodecTag:        s.CodecTag,
	}
}
