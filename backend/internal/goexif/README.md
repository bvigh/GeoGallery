# goexif

This is a copy of the `exif` and `tiff` packages of
[github.com/rwcarlsen/goexif](https://github.com/rwcarlsen/goexif), at pseudo-version
`v0.0.0-20190401172101-9e8deecbddbd`. `LICENSE` is the upstream license.

It lives in this repository because upstream is dormant and decoding needs limits on what a file
can make it allocate and follow.

Differences from upstream:

- `tiff/budget.go`: every decode spends a budget proportional to the size of its data, 6 bytes for
  an IFD and 12 plus the length of its value for a tag. `Decode` shares one budget across IFD0 and
  its chain. `DecodeDir` and `DecodeTag` each make their own.
- A tag's value length is computed in 64 bits.
- `tiff/tag.go`: a value inside the data is read into a slice of exactly its length, not a
  `bytes.Buffer`, which holds 1024 bytes for even the shortest value.
- `Tag.Rat` returns an error for a zero denominator, and `Exif.JpegThumbnail` for an offset or
  length outside the data.
- The import path of the `tiff` package in `exif/exif.go`.

Left out: upstream's tests and samples, `mknote`, `exifstat` and `regen_regress.go`. `goexiftest`
is local: it builds the TIFF structures for the limit tests in `tiff`, `exif` and
`internal/ingest`.
