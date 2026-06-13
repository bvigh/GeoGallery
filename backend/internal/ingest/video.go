package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/labstack/gommon/log"
)

type VideoProbeResult struct {
	WidthPx         int
	HeightPx        int
	Equirectangular bool
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
		"-select_streams", "v:0",
		tmp.Name(),
	).Output()
	if err != nil {
		log.Print(fmt.Errorf("ffprobe failed: %w", err))
		return tmp, VideoProbeResult{}, nil
	}

	var parsed struct {
		Streams []struct {
			Width    int `json:"width"`
			Height   int `json:"height"`
			SideData []struct {
				Type       string `json:"side_data_type"`
				Projection string `json:"projection"`
			} `json:"side_data_list"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed.Streams) == 0 {
		return tmp, VideoProbeResult{}, nil
	}

	s := parsed.Streams[0]
	equirect := false
	for _, sd := range s.SideData {
		if sd.Type == "Spherical Mapping" && sd.Projection == "equirectangular" {
			equirect = true
			break
		}
	}

	return tmp, VideoProbeResult{
		WidthPx:        s.Width,
		HeightPx:       s.Height,
		Equirectangular: equirect,
	}, nil
}
