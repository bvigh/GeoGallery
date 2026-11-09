package tiff

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bvigh/geogallery/internal/goexif/goexiftest"
)

const budgetError = "tiff: tags and values exceed the size of the data"

func TestDecode_StopsEarly(t *testing.T) {
	for _, in := range goexiftest.Inputs() {
		t.Run(in.Name, func(t *testing.T) {
			var err error
			goexiftest.Bounded(t, goexiftest.MaxAlloc(len(in.TIFF), 1), func() {
				_, err = Decode(bytes.NewReader(in.TIFF))
			})
			if in.ShortRead {
				assert.ErrorIs(t, err, ErrShortReadTagValue)
			} else {
				assert.EqualError(t, err, budgetError)
			}
		})
	}
}

// sharedValues returns a TIFF of fileSize bytes with one IFD whose tags all use the first bytes of
// the file as their value, the given counts being their lengths.
func sharedValues(fileSize int, counts ...uint32) []byte {
	entries := make([]goexiftest.Entry, len(counts))
	for i, c := range counts {
		entries[i] = goexiftest.Entry{ID: 0x0100, Type: goexiftest.TypeUndefined, Count: c}
	}
	b := append(goexiftest.Header(8), goexiftest.IFD(0, entries...)...)
	return append(b, make([]byte, fileSize-len(b))...)
}

func TestDecode_BudgetBoundary(t *testing.T) {
	const (
		fileSize = 1024
		budget   = 2*fileSize + 4096
		tags     = 5
	)
	// The IFD costs 6, each full tag 12 + fileSize, and the last tag takes what is left.
	last := budget - 6 - tags*(12+fileSize) - 12
	counts := func(lastCount int) []uint32 {
		c := make([]uint32, tags, tags+1)
		for i := range c {
			c[i] = fileSize
		}
		return append(c, uint32(lastCount))
	}

	decoded, err := Decode(bytes.NewReader(sharedValues(fileSize, counts(last)...)))
	require.NoError(t, err, "spending exactly the budget decodes")
	require.Len(t, decoded.Dirs, 1)
	assert.Len(t, decoded.Dirs[0].Tags, tags+1)

	_, err = Decode(bytes.NewReader(sharedValues(fileSize, counts(last+1)...)))
	assert.EqualError(t, err, budgetError, "one byte more does not")
}

func TestDecodeTag_CountThatOverflows(t *testing.T) {
	for _, c := range goexiftest.OverflowCounts {
		t.Run(c.Name, func(t *testing.T) {
			data := goexiftest.Entry{ID: 0x0100, Type: c.Type, Count: c.Count}.Bytes()
			var err error
			goexiftest.Bounded(t, goexiftest.MaxAlloc(len(data), 1), func() {
				_, err = DecodeTag(bytes.NewReader(data), binary.LittleEndian)
			})
			assert.ErrorIs(t, err, ErrShortReadTagValue)
		})
	}
}

func TestDecodeTag_ExistingRefusalsStay(t *testing.T) {
	cases := []struct {
		name  string
		typ   uint16
		count uint32
		want  string
	}{
		{"count of all ones", goexiftest.TypeByte, 1<<32 - 1, "invalid Count offset in tag"},
		{"count of zero", goexiftest.TypeByte, 0, "zero length tag value"},
		{"unknown type", 99, 4, "zero length tag value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := goexiftest.Entry{ID: 0x0100, Type: c.typ, Count: c.count}.Bytes()
			_, err := DecodeTag(bytes.NewReader(data), binary.LittleEndian)
			assert.EqualError(t, err, c.want)
		})
	}
}

// noSize hides the Size method of a reader.
type noSize struct{ ReadAtReader }

func TestDecodeTag_ValueOutsideItsEntry(t *testing.T) {
	const entrySize = 12
	values := []byte("abcdefghi")
	cases := []struct {
		name   string
		count  uint32
		offset uint32
		want   string
		short  bool
	}{
		{"inside the data", 5, entrySize, "abcde", false},
		{"up to the end of the data", 5, entrySize + 4, "efghi", false},
		{"one byte past the end", 5, entrySize + 5, "", true},
		{"offset past the end", 5, 1 << 31, "", true},
		{"count past the end", 1 << 31, entrySize, "", true},
	}
	readers := []struct {
		name  string
		wrap  func([]byte) ReadAtReader
		exact bool
	}{
		{"sized", func(b []byte) ReadAtReader { return bytes.NewReader(b) }, true},
		{"unsized", func(b []byte) ReadAtReader { return noSize{bytes.NewReader(b)} }, false},
	}
	for _, reader := range readers {
		for _, c := range cases {
			t.Run(reader.name+"/"+c.name, func(t *testing.T) {
				entry := goexiftest.Entry{ID: 0x0100, Type: goexiftest.TypeUndefined, Count: c.count, Value: c.offset}
				tag, err := DecodeTag(reader.wrap(append(entry.Bytes(), values...)), binary.LittleEndian)
				if c.short {
					assert.ErrorIs(t, err, ErrShortReadTagValue)
					return
				}
				require.NoError(t, err)
				assert.Equal(t, c.want, string(tag.Val))
				if reader.exact {
					assert.Equal(t, len(c.want), cap(tag.Val), "a value keeps no spare capacity")
				}
			})
		}
	}
}

func TestDecodeDir_Budget(t *testing.T) {
	const fileSize = 1 << 16
	file := func(tags int) []byte {
		counts := make([]uint32, tags)
		for i := range counts {
			counts[i] = fileSize
		}
		return sharedValues(fileSize, counts...)
	}
	const (
		overSized   = 3  // more than 2*fileSize + 4096
		overUnsized = 32 // more than the 2 MiB of a reader that cannot report its size
	)
	sized := func(data []byte) ReadAtReader { return bytes.NewReader(data) }
	unsized := func(data []byte) ReadAtReader { return noSize{bytes.NewReader(data)} }

	cases := []struct {
		name    string
		reader  func([]byte) ReadAtReader
		tags    int
		wantErr bool
	}{
		{"sized, within its budget", sized, overSized - 1, false},
		{"sized, past its budget", sized, overSized, true},
		{"unsized, within the fixed cap", unsized, overUnsized - 1, false},
		{"unsized, past the fixed cap", unsized, overUnsized, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.reader(file(c.tags))
			_, _ = r.Read(make([]byte, 8)) // skip the header, as Decode does
			dir, _, err := DecodeDir(r, binary.LittleEndian)
			if c.wantErr {
				assert.EqualError(t, err, budgetError)
				return
			}
			require.NoError(t, err)
			assert.Len(t, dir.Tags, c.tags)
		})
	}
}

func TestTagRat(t *testing.T) {
	cases := []struct {
		name     string
		num, den uint32
		want     *big.Rat
	}{
		{"half", 1, 2, big.NewRat(1, 2)},
		{"zero over zero", 0, 0, nil},
		{"one over zero", 1, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := goexiftest.Entry{ID: 0x0002, Type: goexiftest.TypeRational, Count: 1, Value: 12}.Bytes()
			data = binary.LittleEndian.AppendUint32(data, c.num)
			data = binary.LittleEndian.AppendUint32(data, c.den)
			tag, err := DecodeTag(bytes.NewReader(data), binary.LittleEndian)
			require.NoError(t, err)

			got, err := tag.Rat(0)
			if c.want == nil {
				assert.Error(t, err)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Zero(t, c.want.Cmp(got))
		})
	}
}
