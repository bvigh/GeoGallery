package ingest

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
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

// TestProbeURL reads a real file's tags through ffprobe over HTTP, with range requests like S3's.
func TestProbeURL(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	path := filepath.Join(t.TempDir(), "clip.mp4")
	out, err := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.1:r=10", "-c:v", "libx264",
		"-metadata", "location=+10.0000-020.0000/",
		"-metadata", "creation_time=2001-02-03T04:05:06Z",
		path).CombinedOutput()
	require.NoError(t, err, string(out))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/clip.mp4" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	}))
	defer srv.Close()

	result, err := ProbeURL(context.Background(), srv.URL+"/clip.mp4")
	require.NoError(t, err)
	require.NotNil(t, result.Lat)
	assert.InDelta(t, 10.0, *result.Lat, 1e-9)
	assert.InDelta(t, -20.0, *result.Lng, 1e-9)
	require.NotNil(t, result.CreatedAt)
	assert.True(t, time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC).Equal(*result.CreatedAt))
	assert.Equal(t, "2001-02-03T04:05:06.000000Z", result.RawTags["format.creation_time"])

	// A presigned URL carries a signature, so it must not end up in the error a caller logs.
	const secret = "X-Amz-Signature=deadbeef"
	_, err = ProbeURL(context.Background(), srv.URL+"/missing.mp4?"+secret)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
}

// Leaving an item cancels the request, and the cancellation must reach the process.
func TestProbeURL_ContextKillsTheProcess(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not in PATH")
	}
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := ProbeURL(ctx, srv.URL+"/clip.mp4")

	require.Error(t, err)
	assert.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
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

// A file needs only a video extension to reach ffprobe, at upload and on every re-read. Treated as
// a manifest, its contents would make the server fetch whatever it names.
func TestVideoTools_DoNotFollowAManifestInAVideoFile(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	var followed atomic.Int32
	var manifest string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/upload.mp4" {
			http.ServeContent(w, r, "upload.mp4", time.Time{}, strings.NewReader(manifest))
			return
		}
		followed.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	manifest = dashManifest(srv.URL + "/segment.mp4")

	t.Run("probing by URL", func(t *testing.T) {
		_, _ = ProbeURL(context.Background(), srv.URL+"/upload.mp4")
		assert.Zero(t, followed.Load())
	})
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

		_, err := TranscodeH264(path, nil)

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

			transcoded, err := TranscodeH264(path, nil)
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

func TestParseProbeOutput_RawTags(t *testing.T) {
	t.Run("real ffprobe output, prefixed by where each tag came from", func(t *testing.T) {
		fixture, err := os.ReadFile("testdata/ffprobe-6.1.1.json")
		require.NoError(t, err)

		raw := parseProbeOutput(fixture).RawTags

		assert.Equal(t, "Lavf60.16.100", raw["format.encoder"])
		assert.Equal(t, "Lavc60.31.102 libx264", raw["stream.encoder"])
		assert.Equal(t, "VideoHandler", raw["stream.handler_name"])
		assert.Equal(t, "isom", raw["format.major_brand"])
		assert.Equal(t, "+10.0000+020.0000/", raw["format.location"])
		assert.Len(t, raw, 12)
		assert.NotContains(t, raw, "format.filename", "the input name is not a tag: for a presigned URL it would be the URL")
	})
	t.Run("only the first stream", func(t *testing.T) {
		raw := parseProbeOutput([]byte(`{"streams":[{"width":16,"height":16,"tags":{"a":"first"}},{"tags":{"a":"second","b":"second"}}],"format":{"tags":{"c":"format"}}}`)).RawTags

		assert.Equal(t, map[string]string{"stream.a": "first", "format.c": "format"}, raw)
	})
	t.Run("no tags at all", func(t *testing.T) {
		assert.Empty(t, parseProbeOutput([]byte(`{"streams":[{"width":16,"height":16}],"format":{}}`)).RawTags)
	})
	t.Run("an odd tag costs only itself, and never the probe result", func(t *testing.T) {
		result := parseProbeOutput([]byte(`{"streams":[{"width":16,"height":16,"codec_name":"h264","tags":{"rotate":90,"language":"und"}}],` +
			`"format":{"duration":"2.000000","tags":{"count":7,"nested":{"x":1},"creation_time":"2001-02-03T04:05:06.000000Z"}}}`))

		assert.Equal(t, 16, result.WidthPx)
		assert.Equal(t, "h264", result.CodecName)
		require.NotNil(t, result.CreatedAt)
		require.NotNil(t, result.DurationS)
		assert.Equal(t, "und", result.RawTags["stream.language"])
		assert.Equal(t, "2001-02-03T04:05:06.000000Z", result.RawTags["format.creation_time"])
	})
}

// testdata/ffprobe-6.1.1.json is ffprobe's real output for a synthetic clip, flags as in ProbeVideo.
func TestParseProbeOutput_RealFfprobeOutput(t *testing.T) {
	fixture, err := os.ReadFile("testdata/ffprobe-6.1.1.json")
	require.NoError(t, err)

	result := parseProbeOutput(fixture)

	require.NotNil(t, result.DurationS)
	assert.InDelta(t, 1.0, *result.DurationS, 1e-9)
	assert.Equal(t, "Lavf60.16.100", result.Encoder)
	assert.Equal(t, 16, result.WidthPx)
	assert.Equal(t, 16, result.HeightPx)
	assert.Equal(t, "h264", result.CodecName)
	require.NotNil(t, result.CreatedAt)
	assert.True(t, time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC).Equal(*result.CreatedAt))
	require.NotNil(t, result.Lat)
	assert.InDelta(t, 10.0, *result.Lat, 1e-9)
}

func TestParseProbeOutput_Duration(t *testing.T) {
	const format = `{"streams":[{"width":5760,"height":2880,"codec_name":"hevc","side_data_list":[{"side_data_type":"Spherical Mapping","projection":"equirectangular"}]}],` +
		`"format":{%s"tags":{"creation_time":"2001-02-03T04:05:06.000000Z","location":"+10.0000+020.0000/"}}}`
	valid := 12.5
	tests := []struct {
		name  string
		field string
		want  *float64
	}{
		{"a JSON string, as ffprobe writes it", `"duration":"12.500000",`, &valid},
		{"missing", ``, nil},
		{"N/A", `"duration":"N/A",`, nil},
		{"empty", `"duration":"",`, nil},
		{"NaN", `"duration":"NaN",`, nil},
		{"infinite", `"duration":"Inf",`, nil},
		{"negative infinite", `"duration":"-Infinity",`, nil},
		{"zero", `"duration":"0.000000",`, nil},
		{"negative", `"duration":"-1.000000",`, nil},
		{"too large for a float", `"duration":"1e999",`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseProbeOutput([]byte(fmt.Sprintf(format, tt.field)))

			if tt.want == nil {
				assert.Nil(t, result.DurationS)
			} else {
				require.NotNil(t, result.DurationS)
				assert.InDelta(t, *tt.want, *result.DurationS, 1e-9)
			}
			// A duration that cannot be read costs nothing else.
			assert.Equal(t, 5760, result.WidthPx)
			assert.Equal(t, 2880, result.HeightPx)
			assert.True(t, result.Equirectangular)
			assert.Equal(t, "hevc", result.CodecName)
			require.NotNil(t, result.CreatedAt)
			require.NotNil(t, result.Lat)
			require.NotNil(t, result.Lng)
		})
	}
}

func TestParseProbeOutput_Encoder(t *testing.T) {
	const format = `{"streams":[{"width":16,"height":16}],"format":{"tags":%s}}`
	tests := []struct{ name, tags, want string }{
		{"as written", `{"encoder":"Lavf60.16.100"}`, "Lavf60.16.100"},
		{"missing", `{}`, ""},
		{"blank", `{"encoder":"   "}`, ""},
		{"surrounding space", `{"encoder":"  HandBrake 1.0  "}`, "HandBrake 1.0"},
		{"an escaped NUL is removed", `{"encoder":"Lavf\u000060"}`, "Lavf60"},
		{"Matroska writes it in capitals", `{"ENCODER":"Lavf60.16.100"}`, "Lavf60.16.100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseProbeOutput([]byte(fmt.Sprintf(format, tt.tags)))
			assert.Equal(t, tt.want, result.Encoder)
			assert.Equal(t, 16, result.WidthPx)
		})
	}
}

// probeFile runs ProbeVideo over the file at path.
func probeFile(t *testing.T, path string) VideoProbeResult {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	tmp, result, err := ProbeVideo(f)
	require.NoError(t, err)
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	return result
}

// Without creation_time on its command line, ffmpeg writes the re-encoded file without one, and the
// only record of when a legacy-codec video was recorded would be lost from the stored object.
func TestTranscodeH264_KeepsCreationTime(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	src := filepath.Join(t.TempDir(), "clip.mp4")
	out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.2:r=10",
		"-c:v", "mpeg4", "-metadata", "creation_time=2001-02-03T04:05:06Z", src).CombinedOutput()
	require.NoError(t, err, string(out))

	transcoded, err := TranscodeH264(src, probeFile(t, src).CreatedAt)
	require.NoError(t, err)
	defer func() {
		transcoded.Close()
		os.Remove(transcoded.Name())
	}()

	tmp, result, err := ProbeVideo(transcoded)
	require.NoError(t, err)
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()
	assert.Equal(t, "h264", result.CodecName)
	require.NotNil(t, result.CreatedAt, "the transcoded file keeps its creation_time")
	assert.True(t, time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC).Equal(*result.CreatedAt), "creation_time %s", result.CreatedAt)
}

// writeIDITClip encodes a synthetic MJPEG AVI and adds an IDIT chunk before the movi list, which
// ffmpeg's muxer cannot write. Cameras put the local wall clock there, with no zone.
func writeIDITClip(t *testing.T, stamp string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.avi")
	out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.2:r=10",
		"-c:v", "mjpeg", "-pix_fmt", "yuvj420p", path).CombinedOutput()
	require.NoError(t, err, string(out))
	content, err := os.ReadFile(path)
	require.NoError(t, err)

	movi := bytes.Index(content, []byte("movi"))
	require.Greater(t, movi, 8, "the AVI has a movi list")
	listStart := movi - 8 // the list's "LIST" and size come before its "movi"

	payload := append([]byte(stamp), 0)
	if len(payload)%2 == 1 {
		payload = append(payload, 0) // RIFF chunks are word-aligned
	}
	chunk := append([]byte("IDIT"), binary.LittleEndian.AppendUint32(nil, uint32(len(payload)))...)
	chunk = append(chunk, payload...)

	patched := append(append(append([]byte{}, content[:listStart]...), chunk...), content[listStart:]...)
	binary.LittleEndian.PutUint32(patched[4:8], uint32(len(patched)-8))
	require.NoError(t, os.WriteFile(path, patched, 0o600))
	return path
}

// The stored object carries the time ingest read, or none. ffmpeg would otherwise turn a time ingest
// refuses, such as an AVI's wall clock with no zone, into an instant using the server's own zone: a
// time the original never stated, which the file re-read would then compare with taken_at.
func TestTranscodeH264_WritesOnlyTheTimeIngestRead(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not in PATH")
		}
	}
	transcodeAndProbe := func(t *testing.T, src string, createdAt *time.Time) VideoProbeResult {
		t.Helper()
		transcoded, err := TranscodeH264(src, createdAt)
		require.NoError(t, err)
		defer func() {
			transcoded.Close()
			os.Remove(transcoded.Name())
		}()
		tmp, result, err := ProbeVideo(transcoded)
		require.NoError(t, err)
		defer func() {
			tmp.Close()
			os.Remove(tmp.Name())
		}()
		return result
	}

	for _, zone := range []string{"UTC", "Europe/Warsaw"} {
		t.Run("a wall clock with no zone is cleared, on a server in "+zone, func(t *testing.T) {
			t.Setenv("TZ", zone)
			src := writeIDITClip(t, "Sat Jun 15 14:30:00 2024\n")

			source := probeFile(t, src)
			require.Equal(t, "2024-06-15 14:30:00", source.RawTags["format.creation_time"], "the source does carry the wall clock")
			require.Nil(t, source.CreatedAt, "ingest cannot read it")
			require.True(t, source.NeedsTranscode())

			result := transcodeAndProbe(t, src, source.CreatedAt)

			assert.Nil(t, result.CreatedAt)
			assert.NotContains(t, result.RawTags, "format.creation_time")
			assert.NotContains(t, result.RawTags, "stream.creation_time")
		})

		t.Run("a readable time is the same instant, on a server in "+zone, func(t *testing.T) {
			t.Setenv("TZ", zone)
			src := writeIDITClip(t, "Sat Jun 15 14:30:00 2024\n")
			read := time.Date(2001, 2, 3, 4, 5, 6, 0, time.FixedZone("", 2*60*60))

			result := transcodeAndProbe(t, src, &read)

			require.NotNil(t, result.CreatedAt)
			assert.True(t, read.Equal(*result.CreatedAt), "creation_time %s", result.CreatedAt)
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

	out, err := TranscodeH264(samplePath, nil)
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
