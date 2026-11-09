package tiff

import "errors"

// errBudget is returned when a decode runs out of budget.
var errBudget = errors.New("tiff: tags and values exceed the size of the data")

// unsizedBudget is the budget of a reader that cannot report its size. It equals that of a 1 MiB
// file, the most that ingest passes to the decoder.
const unsizedBudget = 2 << 20

// budget limits the bytes one decode may spend, which keeps what it allocates and how long it
// walks proportional to the size of the data. An IFD costs 6 and a tag 12 plus the length of its
// value. A file's entries and values do not normally overlap, so decoding one spends about the
// length of its data, and at most 4/3 of it when every value fits inside its entry. The budget is
// twice the length of the data: files in a real library spent up to about 0.975 of that length.
type budget struct {
	left uint64
}

// dataSize returns the size of the data behind r, when r reports one.
func dataSize(r ReadAtReader) (uint64, bool) {
	if s, ok := r.(interface{ Size() int64 }); ok && s.Size() >= 0 {
		return uint64(s.Size()), true
	}
	return 0, false
}

// newBudget returns the budget for decoding the data behind r.
func newBudget(r ReadAtReader) *budget {
	if size, ok := dataSize(r); ok {
		return &budget{left: 2*size + 4096}
	}
	return &budget{left: unsizedBudget}
}

// spend takes n bytes from the budget, or returns errBudget when fewer than n are left.
func (b *budget) spend(n uint64) error {
	if n > b.left {
		return errBudget
	}
	b.left -= n
	return nil
}
