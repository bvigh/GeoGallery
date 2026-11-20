package ingest

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"github.com/bep/imagemeta"
)

// Pano is what a photo's XMP says about its projection, in Google's Photo Sphere properties
// (GPano, https://developers.google.com/streetview/spherical-metadata). The zero value is a photo
// whose XMP says nothing about it, or that has none.
type Pano struct {
	// Projection is GPano:ProjectionType after CleanTag: "equirectangular" for a photo sphere,
	// "cylindrical" for some phones' panoramas, "" when the file names none.
	Projection string
	// Partial is set when GPano's cropped area is smaller than its full panorama: the image is a
	// part of a sphere, as a photo sphere whose capture was stopped early.
	Partial bool
}

// gpanoNS is the XML namespace of the GPano properties. The prefix is the file's choice ("GPano" in
// every file seen), so properties are matched by this URI.
const gpanoNS = "http://ns.google.com/photos/1.0/panorama/"

// What one XMP packet may cost. imagemeta hands over a packet it holds in memory already: a JPEG
// segment (64 KB at most) or a chunk of the 1 MiB head. Only its first xmpScanSize bytes and
// xmpTokenLimit tokens are parsed, which bounds what an XML decoder allocates for names, attributes
// and nesting. In a library of 3,640 XMP packets the largest was 8 KB and 232 tokens, and the GPano
// properties came in the 7th token.
const (
	xmpScanSize   = 64 << 10
	xmpTokenLimit = 2000
)

// readPano reads the GPano properties from the XMP of the image at the start of r. It is a pass of
// its own after the EXIF's, so a packet that is broken or too large costs the projection and nothing
// else. Only the first XMP packet of a JPEG is read, and none of a PNG: imagemeta reads no PNG XMP.
func readPano(r io.ReadSeeker) Pano {
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Pano{}
	}
	var props panoProps
	_ = decode(r, imagemeta.Options{
		Sources: imagemeta.XMP,
		HandleXMP: func(packet io.Reader) error {
			props.scan(packet)
			// imagemeta wants the packet read to its end. It is in memory, so this costs nothing.
			_, err := io.Copy(io.Discard, packet)
			return err
		},
	})
	return props.pano()
}

// panoProps collects the GPano properties a projection is judged by, as written. The first value
// of each wins, also across the packets of a HEIF file.
type panoProps struct {
	projection, croppedWidth, croppedHeight, fullWidth, fullHeight string
}

func (p *panoProps) set(name, value string) {
	var field *string
	switch name {
	case "ProjectionType":
		field = &p.projection
	case "CroppedAreaImageWidthPixels":
		field = &p.croppedWidth
	case "CroppedAreaImageHeightPixels":
		field = &p.croppedHeight
	case "FullPanoWidthPixels":
		field = &p.fullWidth
	case "FullPanoHeightPixels":
		field = &p.fullHeight
	}
	if field != nil && *field == "" {
		*field = value
	}
}

// scan reads one packet's GPano properties, written as attributes of an element (how every file
// seen writes them) or as elements of their own (as in Google's example). A packet that is not
// well-formed keeps what was read before the error.
func (p *panoProps) scan(packet io.Reader) {
	d := xml.NewDecoder(io.LimitReader(packet, xmpScanSize))
	var prop string // the GPano element whose text is being read
	var text strings.Builder
	for range xmpTokenLimit {
		token, err := d.Token()
		if err != nil {
			return
		}
		switch t := token.(type) {
		case xml.StartElement:
			for _, a := range t.Attr {
				if a.Name.Space == gpanoNS {
					p.set(a.Name.Local, a.Value)
				}
			}
			prop = ""
			if t.Name.Space == gpanoNS {
				prop = t.Name.Local
				text.Reset()
			}
		case xml.CharData:
			if prop != "" {
				text.Write(t)
			}
		case xml.EndElement:
			if prop != "" {
				p.set(prop, text.String())
				prop = ""
			}
		}
	}
}

func (p panoProps) pano() Pano {
	return Pano{
		Projection: CleanTag(p.projection),
		Partial:    smaller(p.croppedWidth, p.fullWidth) || smaller(p.croppedHeight, p.fullHeight),
	}
}

// smaller reports whether a cropped size is a positive number below the full size. A size that is
// missing or not a whole number says nothing.
func smaller(cropped, full string) bool {
	c, errC := strconv.Atoi(strings.TrimSpace(cropped))
	f, errF := strconv.Atoi(strings.TrimSpace(full))
	return errC == nil && errF == nil && c > 0 && c < f
}
