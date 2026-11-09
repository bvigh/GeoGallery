package exif

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bvigh/geogallery/internal/goexif/goexiftest"
	"github.com/bvigh/geogallery/internal/goexif/tiff"
)

// decodesPerFile is IFD0 and its chain, plus the three sub-IFDs.
const decodesPerFile = 4

func TestDecode_StopsEarly(t *testing.T) {
	for _, in := range goexiftest.Inputs() {
		forms := map[string][]byte{"tiff": in.TIFF, "jpeg": in.JPEG}
		for form, data := range forms {
			if data == nil {
				continue
			}
			t.Run(in.Name+"/"+form, func(t *testing.T) {
				var err error
				goexiftest.Bounded(t, goexiftest.MaxAlloc(len(data), decodesPerFile), func() {
					_, err = Decode(bytes.NewReader(data))
				})
				require.Error(t, err)
				assert.True(t, IsCriticalError(err), "%v", err)
			})
		}
	}
}

func TestDecode_ABadExifIFDCostsOnlyThatIFD(t *testing.T) {
	for _, in := range goexiftest.BadExifIFDs() {
		t.Run(in.Name, func(t *testing.T) {
			var x *Exif
			var err error
			goexiftest.Bounded(t, goexiftest.MaxAlloc(len(in.TIFF), decodesPerFile), func() {
				x, err = Decode(bytes.NewReader(in.TIFF))
			})
			require.Error(t, err)
			assert.False(t, IsCriticalError(err), "%v", err)
			assert.True(t, IsExifError(err), "%v", err)
			require.NotNil(t, x)

			model, err := x.Get(Model)
			require.NoError(t, err)
			s, err := model.StringVal()
			require.NoError(t, err)
			assert.Equal(t, goexiftest.Model, s)

			dateTime, err := x.Get(DateTime)
			require.NoError(t, err)
			s, err = dateTime.StringVal()
			require.NoError(t, err)
			assert.Equal(t, goexiftest.DateTime, s)
		})
	}
}

// thumbnailTIFF returns a TIFF whose second IFD holds the thumbnail tags.
func thumbnailTIFF(offsetType uint16, offset, length uint32) []byte {
	const ifd1 = 8 + 6
	b := goexiftest.Header(8)
	b = append(b, goexiftest.IFD(ifd1)...)
	return append(b, goexiftest.IFD(0,
		goexiftest.Entry{ID: 0x0201, Type: offsetType, Count: 1, Value: offset},
		goexiftest.Entry{ID: 0x0202, Type: goexiftest.TypeLong, Count: 1, Value: length},
	)...)
}

func TestJpegThumbnail(t *testing.T) {
	const fileSize = 8 + 6 + 6 + 2*12
	cases := []struct {
		name       string
		offsetType uint16
		offset     uint32
		length     uint32
		wantLen    int
		wantErr    bool
	}{
		{"inside the data", goexiftest.TypeLong, 8, 6, 6, false},
		{"up to the end of the data", goexiftest.TypeLong, 8, fileSize - 8, fileSize - 8, false},
		{"empty, at the end of the data", goexiftest.TypeLong, fileSize, 0, 0, false},
		{"one byte past the end", goexiftest.TypeLong, 8, fileSize - 7, 0, true},
		{"offset past the end", goexiftest.TypeLong, fileSize + 1, 0, 0, true},
		{"length near the top of the range", goexiftest.TypeLong, 8, 1<<32 - 1, 0, true},
		{"negative offset", goexiftest.TypeSLong, 1<<32 - 1, 4, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x, err := Decode(bytes.NewReader(thumbnailTIFF(c.offsetType, c.offset, c.length)))
			require.NoError(t, err)

			got, err := x.JpegThumbnail()
			if c.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, c.wantLen)
		})
	}
}

// useAll calls every accessor the way a reader of the decoded data would.
type useAll struct{}

func (useAll) Walk(_ FieldName, tag *tiff.Tag) error {
	_ = tag.String()
	_, _ = tag.StringVal()
	for i := 0; i < int(tag.Count) && i < 16; i++ {
		_, _, _ = tag.Rat2(i)
		_, _ = tag.Rat(i)
		_, _ = tag.Int64(i)
		_, _ = tag.Int(i)
		_, _ = tag.Float(i)
	}
	return nil
}

// FuzzDecode checks that no input panics and that what a decode allocates follows from its length.
func FuzzDecode(f *testing.F) {
	for _, in := range append(goexiftest.Inputs(), goexiftest.BadExifIFDs()...) {
		f.Add(in.TIFF)
		if in.JPEG != nil {
			f.Add(in.JPEG)
		}
	}
	f.Add(goexiftest.PhotoTIFF())
	f.Add(thumbnailTIFF(goexiftest.TypeLong, 8, 6))

	f.Fuzz(func(t *testing.T, data []byte) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		x, err := Decode(bytes.NewReader(data))
		runtime.ReadMemStats(&after)
		if used, limit := after.TotalAlloc-before.TotalAlloc, goexiftest.MaxAlloc(len(data), decodesPerFile); used > limit {
			t.Errorf("allocated %d bytes for %d bytes of data, the limit is %d", used, len(data), limit)
		}
		if x == nil || (err != nil && IsCriticalError(err)) {
			return
		}
		_ = x.Walk(useAll{})
		_ = x.String()
		_, _ = x.MarshalJSON()
		_, _, _ = x.LatLong()
		_, _ = x.DateTime()
		_, _ = x.JpegThumbnail()
	})
}
