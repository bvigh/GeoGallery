# goexif

This is a copy of the `exif` and `tiff` packages of
[github.com/rwcarlsen/goexif](https://github.com/rwcarlsen/goexif), at pseudo-version
`v0.0.0-20190401172101-9e8deecbddbd`. `LICENSE` is the upstream license.

It lives in this repository because upstream is dormant and decoding needs limits on what a file
can make it allocate and follow. Those limits are local changes to this copy.

Left out: upstream's tests and samples, `mknote`, `exifstat` and `regen_regress.go`. The only
other difference from upstream is the import path of the `tiff` package in `exif/exif.go`.
