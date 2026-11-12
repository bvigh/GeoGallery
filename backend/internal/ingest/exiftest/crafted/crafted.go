// Package crafted builds malformed TIFF structures for the tests of the EXIF decoder's limits.
// Nothing is validated: types, counts and offsets are written exactly as given.
package crafted

import (
	"encoding/binary"
	"runtime"
	"testing"
	"time"
)

// TIFF data types.
const (
	TypeByte      = 1
	TypeASCII     = 2
	TypeShort     = 3
	TypeLong      = 4
	TypeRational  = 5
	TypeUndefined = 7
	TypeSShort    = 8
	TypeSLong     = 9
	TypeSRational = 10
	TypeFloat     = 11
	TypeDouble    = 12
)

// maxJPEGTIFF is the most TIFF data one APP1 segment can hold.
const maxJPEGTIFF = 1<<16 - 1 - 2 - 6

var le = binary.LittleEndian

// Entry is one 12-byte IFD entry. Value is the offset of the value, or the value itself when it
// fits in four bytes.
type Entry struct {
	ID    uint16
	Type  uint16
	Count uint32
	Value uint32
}

// Bytes returns the entry as written in an IFD.
func (e Entry) Bytes() []byte {
	b := le.AppendUint16(nil, e.ID)
	b = le.AppendUint16(b, e.Type)
	b = le.AppendUint32(b, e.Count)
	return le.AppendUint32(b, e.Value)
}

// Header returns the little-endian TIFF header naming the offset of the first IFD.
func Header(firstIFD uint32) []byte {
	return le.AppendUint32([]byte("II*\x00"), firstIFD)
}

// IFD returns an IFD holding entries and the offset of the next IFD.
func IFD(next uint32, entries ...Entry) []byte {
	b := le.AppendUint16(nil, uint16(len(entries)))
	for _, e := range entries {
		b = append(b, e.Bytes()...)
	}
	return le.AppendUint32(b, next)
}

// IFDSize returns the size of an IFD with n entries.
func IFDSize(n int) uint32 { return uint32(6 + 12*n) }

// JPEG wraps a TIFF in the APP1 segment of a JPEG. It returns nil when the TIFF does not fit in
// one segment.
func JPEG(tiff []byte) []byte {
	if len(tiff) > maxJPEGTIFF {
		return nil
	}
	b := []byte{0xFF, 0xD8, 0xFF, 0xE1}
	b = binary.BigEndian.AppendUint16(b, uint16(2+6+len(tiff)))
	b = append(b, "Exif\x00\x00"...)
	b = append(b, tiff...)
	return append(b, 0xFF, 0xD9)
}

// Input is a structure the decoder has to survive.
type Input struct {
	Name      string
	TIFF      []byte
	JPEG      []byte // nil when TIFF does not fit in an APP1 segment
	ShortRead bool   // the decode ends with a short read, not by running out of budget
}

func input(name string, tiff []byte) Input {
	return Input{Name: name, TIFF: tiff, JPEG: JPEG(tiff)}
}

// OverflowCount is a tag type with a Count that overflows uint32 once it is multiplied by the size
// of the type, leaving a value length of four or eight bytes.
type OverflowCount struct {
	Name  string
	Type  uint16
	Count uint32
}

// OverflowCounts covers every type wider than one byte.
var OverflowCounts = []OverflowCount{
	{"short", TypeShort, 0x80000002},
	{"long", TypeLong, 0xC0000001},
	{"rational", TypeRational, 0xE0000001},
	{"signed short", TypeSShort, 0x80000002},
	{"signed long", TypeSLong, 0xC0000001},
	{"signed rational", TypeSRational, 0xE0000001},
	{"float", TypeFloat, 0xC0000001},
	{"double", TypeDouble, 0xE0000001},
}

// Inputs returns structures whose IFD0 or IFD chain holds nothing a decoder should return.
func Inputs() []Input {
	var inputs []Input
	for _, c := range OverflowCounts {
		tiff := append(Header(8), IFD(0, Entry{ID: 0x0100, Type: c.Type, Count: c.Count})...)
		in := input("count overflows, "+c.Name, tiff)
		in.ShortRead = true
		inputs = append(inputs, in)
	}

	byteTag := Entry{ID: 0x0100, Type: TypeByte, Count: 1, Value: 1}
	shortValue := Entry{ID: 0x0100, Type: TypeByte, Count: 5} // kept outside the entry, at offset 0
	return append(inputs,
		input("two IFDs pointing at each other", cycle(Repeat(byteTag, 1))),
		input("two empty IFDs pointing at each other", cycle(nil)),
		input("two IFDs of short values pointing at each other", cycle(Repeat(shortValue, 2700))),
		input("many tags sharing one value", sharedValue()),
		input("chain of IFDs sharing their entries", overlappingChain()),
	)
}

func Repeat(e Entry, n int) []Entry {
	es := make([]Entry, n)
	for i := range es {
		es[i] = e
	}
	return es
}

// cycle returns two IFDs holding entries, each naming the other as its next IFD.
func cycle(entries []Entry) []byte {
	const first = 8
	b := Header(first)
	b = append(b, IFD(first+IFDSize(len(entries)), entries...)...)
	return append(b, IFD(first, entries...)...)
}

// sharedValue returns one IFD of many tags whose values are all the whole file.
func sharedValue() []byte {
	const tags = 5400
	b := Header(8)
	b = append(b, IFD(0, Repeat(Entry{ID: 0x0100, Type: TypeByte, Count: maxJPEGTIFF}, tags)...)...)
	return append(b, make([]byte, maxJPEGTIFF-len(b))...)
}

// overlappingChain returns a long chain of IFDs that share one table of entries.
func overlappingChain() []byte {
	const (
		table = 1 << 16
		tags  = 2730
		ifds  = 2730
	)
	b := make([]byte, table+2+12*(tags+ifds))
	copy(b, Header(table))
	le.PutUint16(b[table:], tags)
	for j := 0; j < tags+ifds; j++ {
		e := Entry{ID: 0x0100, Type: TypeByte, Count: 4, Value: tags << 16}
		if j >= tags {
			next := uint32(table + 12*(j-tags+1))
			if j == tags+ifds-1 {
				next = 0
			}
			e.ID, e.Type = uint16(next), uint16(next>>16)
		}
		copy(b[table+2+12*j:], e.Bytes())
	}
	return b
}

// The tags PhotoTIFF writes to IFD0.
const (
	Model    = "Test Camera"
	DateTime = "2021:03:04 05:06:07"
)

// PhotoTIFF returns a TIFF whose IFD0 holds Model, DateTime and a pointer to an Exif IFD of
// the given entries.
func PhotoTIFF(exifEntries ...Entry) []byte {
	const first = 8
	exifAt := first + IFDSize(3)
	valuesAt := exifAt + IFDSize(len(exifEntries))
	model, dateTime := Model+"\x00", DateTime+"\x00"

	b := Header(first)
	b = append(b, IFD(0,
		Entry{ID: 0x0110, Type: TypeASCII, Count: uint32(len(model)), Value: valuesAt},
		Entry{ID: 0x0132, Type: TypeASCII, Count: uint32(len(dateTime)), Value: valuesAt + uint32(len(model))},
		Entry{ID: 0x8769, Type: TypeLong, Count: 1, Value: exifAt},
	)...)
	b = append(b, IFD(0, exifEntries...)...)
	b = append(b, model...)
	return append(b, dateTime...)
}

// BadExifIFDs returns photos with a good IFD0 and an Exif IFD that holds nothing useful.
func BadExifIFDs() []Input {
	const tags = 4096
	shared := Repeat(Entry{ID: 0x9286, Type: TypeByte, Count: 40000}, tags)
	return []Input{
		input("count overflows", PhotoTIFF(Entry{ID: 0x9286, Type: TypeLong, Count: 0xC0000001})),
		input("many tags sharing one value", PhotoTIFF(shared...)),
	}
}

// watchdog is far above the few milliseconds a decode of these inputs takes.
const watchdog = time.Second

// Bounded runs fn and fails the test if it takes longer than the watchdog or allocates more than
// limit bytes.
func Bounded(t *testing.T, limit uint64, fn func()) {
	t.Helper()
	var before, after runtime.MemStats
	done := make(chan struct{})
	runtime.ReadMemStats(&before)
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(watchdog):
		t.Fatalf("still running after %v", watchdog)
	}
	runtime.ReadMemStats(&after)
	if used := after.TotalAlloc - before.TotalAlloc; used > limit {
		t.Errorf("allocated %d bytes, the limit is %d", used, limit)
	}
}
