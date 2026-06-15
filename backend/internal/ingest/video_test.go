package ingest

import (
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProbeOutput_Equirectangular_WithCreatedAt(t *testing.T) {
	fixture := `{"streams":[{"width":5760,"height":2880,"side_data_list":[{"side_data_type":"Spherical Mapping","projection":"equirectangular"}]}],"format":{"tags":{"creation_time":"2022-07-20T17:06:24.000000Z"}}}`
	result := parseProbeOutput([]byte(fixture))
	assert.True(t, result.Equirectangular)
	assert.Equal(t, 5760, result.WidthPx)
	assert.Equal(t, 2880, result.HeightPx)
	require.NotNil(t, result.CreatedAt)
	assert.Equal(t, 2022, result.CreatedAt.Year())
	assert.Equal(t, time.July, result.CreatedAt.Month())
	assert.Equal(t, 20, result.CreatedAt.Day())
	assert.Equal(t, 17, result.CreatedAt.Hour())
	assert.Equal(t, time.UTC, result.CreatedAt.Location())
}

func TestParseProbeOutput_MissingCreationTime(t *testing.T) {
	fixture := `{"streams":[{"width":1920,"height":1080}],"format":{"tags":{}}}`
	result := parseProbeOutput([]byte(fixture))
	assert.Nil(t, result.CreatedAt)
	assert.False(t, result.Equirectangular)
	assert.Equal(t, 1920, result.WidthPx)
}

func TestParseProbeOutput_EmptyOutput(t *testing.T) {
	result := parseProbeOutput([]byte(`{}`))
	assert.Nil(t, result.CreatedAt)
	assert.Zero(t, result.WidthPx)
	assert.False(t, result.Equirectangular)
}

func TestParseProbeOutput_NonEquirectangular(t *testing.T) {
	fixture := `{"streams":[{"width":1920,"height":1080,"side_data_list":[{"side_data_type":"Stereo 3D","projection":""}]}],"format":{"tags":{"creation_time":"2024-01-01T12:00:00.000000Z"}}}`
	result := parseProbeOutput([]byte(fixture))
	assert.False(t, result.Equirectangular)
	require.NotNil(t, result.CreatedAt)
	assert.Equal(t, 2024, result.CreatedAt.Year())
}

func TestParseProbeOutput_CodecFields(t *testing.T) {
	fixture := `{"streams":[{"width":640,"height":352,"codec_name":"mpeg4","codec_tag_string":"mp4v"}],"format":{"tags":{}}}`
	result := parseProbeOutput([]byte(fixture))
	assert.Equal(t, "mpeg4", result.CodecName)
	assert.Equal(t, "mp4v", result.CodecTag)
	assert.False(t, result.Equirectangular)
}

func TestParseProbeOutput_H264CodecFields(t *testing.T) {
	fixture := `{"streams":[{"width":1920,"height":1080,"codec_name":"h264","codec_tag_string":"avc1"}],"format":{"tags":{}}}`
	result := parseProbeOutput([]byte(fixture))
	assert.Equal(t, "h264", result.CodecName)
	assert.Equal(t, "avc1", result.CodecTag)
}

func TestVideoProbeResult_NeedsTranscode(t *testing.T) {
	cases := []struct {
		codec string
		want  bool
	}{
		{"h264", false},
		{"hevc", false},
		{"vp8", false},
		{"vp9", false},
		{"av1", false},
		{"mpeg4", true},
		{"mpeg2video", true},
		{"wmv2", true},
		{"", false}, // unknown / no stream
	}
	for _, tc := range cases {
		p := VideoProbeResult{CodecName: tc.codec}
		assert.Equal(t, tc.want, p.NeedsTranscode(), "codec=%q", tc.codec)
	}
}

func TestTranscodeH264_Mpeg4Sample(t *testing.T) {
	samplePath := "../../../ingest/videos_with_playback_issues/2013-08-15-038.mp4"
	if _, err := os.Stat(samplePath); os.IsNotExist(err) {
		t.Skip("sample file not present")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not in PATH")
	}

	out, err := TranscodeH264(samplePath)
	require.NoError(t, err)
	require.NotNil(t, out)
	defer func() {
		out.Close()
		os.Remove(out.Name())
	}()

	fi, err := out.Stat()
	require.NoError(t, err)
	assert.Greater(t, fi.Size(), int64(0), "transcoded file should be non-empty")

	// Verify the output is H.264 by probing it.
	probeOut, err := exec.Command(
		"ffprobe", "-v", "quiet",
		"-print_format", "json",
		"-show_streams", "-select_streams", "v:0",
		out.Name(),
	).Output()
	require.NoError(t, err)
	result := parseProbeOutput(probeOut)
	assert.Equal(t, "h264", result.CodecName, "transcoded file should be H.264")
	assert.False(t, result.NeedsTranscode())
}

func TestProbeVideo_Insta360Sample(t *testing.T) {
	samplePath := "../../../../ingest/360_samples/VID_20220720_170634_00_111.mp4"
	if _, err := os.Stat(samplePath); os.IsNotExist(err) {
		t.Skip("sample file not present")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not in PATH")
	}

	f, err := os.Open(samplePath)
	require.NoError(t, err)
	defer f.Close()

	tmp, result, err := ProbeVideo(f)
	require.NoError(t, err)
	if tmp != nil {
		tmp.Close()
		os.Remove(tmp.Name())
	}

	assert.True(t, result.Equirectangular)
	assert.Equal(t, 5760, result.WidthPx)
	assert.Equal(t, 2880, result.HeightPx)
	require.NotNil(t, result.CreatedAt)
	assert.Equal(t, 2022, result.CreatedAt.Year())
	assert.Equal(t, time.July, result.CreatedAt.Month())
	assert.Equal(t, 20, result.CreatedAt.Day())
}
