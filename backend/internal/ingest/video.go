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
			Width    int `json:"width"`
			Height   int `json:"height"`
			SideData []struct {
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
	}
}
