package ingest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseISO6709(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		lat, lng float64
		ok       bool
	}{
		{"north west, as a Samsung phone writes it", "+51.5074-000.1278/", 51.5074, -0.1278, true},
		{"north east", "+46.0000+008.0000/", 46.0, 8.0, true},
		{"south west", "-33.4489-070.6693/", -33.4489, -70.6693, true},
		{"with an altitude", "+47.4979+019.0402+120.00/", 47.4979, 19.0402, true},
		{"whole degrees", "+47+019/", 47, 19, true},
		{"trailing bytes after the slash", "+47.4979+019.0402/\xff\xfe", 47.4979, 19.0402, true},
		{"no fix", "+00.0000+000.0000/", 0, 0, false},
		{"no fix, with trailing bytes", "+00.0000+000.0000/\xff\xfe", 0, 0, false},
		{"latitude out of range", "+91.0000+019.0402/", 0, 0, false},
		{"longitude out of range", "+47.0000+181.0000/", 0, 0, false},
		{"degrees and minutes are not read as degrees", "+4729.87+01902.41/", 0, 0, false},
		{"empty", "", 0, 0, false},
		{"not a position", "London", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lat, lng, ok := parseISO6709(tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.InDelta(t, tt.lat, lat, 1e-9)
			assert.InDelta(t, tt.lng, lng, 1e-9)
		})
	}
}

func TestParseProbeOutput_Location(t *testing.T) {
	const stream = `{"streams":[{"width":1920,"height":1080}],"format":{"tags":`
	tests := []struct {
		name string
		tags string
		lat  float64
	}{
		{"location", `{"location":"+51.5074-000.1278/"}`, 51.5074},
		{"location-eng only", `{"location-eng":"+51.5074-000.1278/"}`, 51.5074},
		{"QuickTime key", `{"com.apple.quicktime.location.ISO6709":"+51.5074-000.1278+050.000/"}`, 51.5074},
		{"a tag without a fix does not hide a real one", `{"location":"+00.0000+000.0000/","location-eng":"+51.5074-000.1278/"}`, 51.5074},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseProbeOutput([]byte(stream + tt.tags + `}}`))
			require.NotNil(t, result.Lat)
			require.NotNil(t, result.Lng)
			assert.InDelta(t, tt.lat, *result.Lat, 1e-9)
			assert.InDelta(t, -0.1278, *result.Lng, 1e-9)
		})
	}
	for name, tags := range map[string]string{
		"no location tag": `{"creation_time":"2024-01-01T12:00:00Z"}`,
		"no fix":          `{"location":"+00.0000+000.0000/"}`,
	} {
		t.Run(name, func(t *testing.T) {
			result := parseProbeOutput([]byte(stream + tags + `}}`))
			assert.Nil(t, result.Lat)
			assert.Nil(t, result.Lng)
		})
	}
}

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

func TestFfprobeArgs_AcceptOnlyTheContainersIngestTakes(t *testing.T) {
	args := ffprobeArgs("target")

	require.Equal(t, "target", args[len(args)-1])
	assert.Equal(t, []string{"-format_whitelist", videoDemuxers}, args[len(args)-3:len(args)-1])
}

// dashManifest is a DASH manifest whose only representation is the file at segmentURL.
func dashManifest(segmentURL string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT3S" minBufferTime="PT1S" profiles="urn:mpeg:dash:profile:isoff-on-demand:2011">
<Period><AdaptationSet mimeType="video/mp4" segmentAlignment="true">
<Representation id="1" bandwidth="100000" width="322" height="122" codecs="avc1.42E00A">
<BaseURL>` + segmentURL + `</BaseURL>
</Representation></AdaptationSet></Period></MPD>
`
}

// A file needs only a video extension to reach ffprobe at upload. Treated as a manifest, its
// contents would make the server fetch whatever it names.
func TestVideoTools_DoNotFollowAManifestInAVideoFile(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	var followed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	manifest := dashManifest(srv.URL + "/segment.mp4")

	t.Run("probing an upload", func(t *testing.T) {
		tmp, _, err := ProbeVideo(strings.NewReader(manifest))
		require.NoError(t, err)
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		assert.Zero(t, followed.Load())
	})
	t.Run("transcoding", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "upload.mp4")
		require.NoError(t, os.WriteFile(path, []byte(manifest), 0o600))

		_, err := TranscodeH264(path)

		require.Error(t, err)
		assert.Zero(t, followed.Load())
	})
}

func TestVideoTools_OpenEveryContainerIngestTakes(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not in PATH")
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	cases := []struct {
		ext, encoder, codec string
	}{
		{".mp4", "libx264", "h264"},
		{".mov", "libx264", "h264"},
		{".mkv", "libx264", "h264"},
		{".webm", "libvpx-vp9", "vp9"},
		{".avi", "mpeg4", "mpeg4"},
		{".3gp", "libx264", "h264"},
	}
	for _, c := range cases {
		t.Run(c.ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "clip"+c.ext)
			out, err := exec.Command("ffmpeg", "-y", "-v", "error",
				"-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.1:r=10", "-c:v", c.encoder, path).CombinedOutput()
			require.NoError(t, err, string(out))
			f, err := os.Open(path)
			require.NoError(t, err)
			defer f.Close()

			tmp, result, err := ProbeVideo(f)
			require.NoError(t, err)
			defer os.Remove(tmp.Name())
			defer tmp.Close()
			assert.Equal(t, c.codec, result.CodecName)

			transcoded, err := TranscodeH264(path)
			require.NoError(t, err)
			defer os.Remove(transcoded.Name())
			defer transcoded.Close()
		})
	}
}

// A video in a container off the list is not refused: the file stays available for the upload, but
// the probe result is empty, so nothing comes from its container and it is never transcoded.
func TestProbeVideo_KeepsAnotherContainerUnread(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	cases := []struct {
		format, encoder string
	}{
		{"mpegts", "libx264"},
		{"flv", "flv1"},
		{"mpeg", "mpeg2video"},
		{"asf", "wmv2"},
	}
	for _, c := range cases {
		t.Run(c.format, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "clip.mp4")
			out, err := exec.Command("ffmpeg", "-y", "-v", "error",
				"-f", "lavfi", "-i", "color=c=black:s=64x48:d=0.4:r=5", "-c:v", c.encoder, "-f", c.format, path).CombinedOutput()
			require.NoError(t, err, string(out))
			want, err := os.ReadFile(path)
			require.NoError(t, err)

			tmp, result, err := ProbeVideo(bytes.NewReader(want))

			require.NoError(t, err)
			defer os.Remove(tmp.Name())
			defer tmp.Close()
			assert.Zero(t, result)
			assert.False(t, result.NeedsTranscode())
			got, err := os.ReadFile(tmp.Name())
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
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
