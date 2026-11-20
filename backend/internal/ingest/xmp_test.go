package ingest

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bvigh/geogallery/internal/ingest/exiftest"
	"github.com/bvigh/geogallery/internal/ingest/exiftest/crafted"
)

// sphereProps are the GPano properties of a whole photo sphere, from an Insta360 ONE RS.
var sphereProps = []string{
	"UsePanoramaViewer=True", "CaptureSoftware=insta360 oners", "StitchingSoftware=Insta360 Studio 5.9.8",
	"ProjectionType=equirectangular",
	"CroppedAreaImageWidthPixels=6080", "CroppedAreaImageHeightPixels=3040",
	"FullPanoWidthPixels=6080", "FullPanoHeightPixels=3040",
	"CroppedAreaLeftPixels=0", "CroppedAreaTopPixels=0",
}

var photoSphere = Pano{Projection: "equirectangular"}

func readPanoOf(data []byte) Pano {
	return ExtractExif(bytes.NewReader(data), CameraZone{Loc: time.UTC}).Pano
}

func TestExtractExif_Pano(t *testing.T) {
	part := func(cropW, cropH int) string {
		return exiftest.GPanoPacket("ProjectionType=equirectangular",
			fmt.Sprintf("CroppedAreaImageWidthPixels=%d", cropW), fmt.Sprintf("CroppedAreaImageHeightPixels=%d", cropH),
			"FullPanoWidthPixels=8000", "FullPanoHeightPixels=4000")
	}
	tests := []struct {
		name   string
		packet string
		want   Pano
	}{
		{"a photo sphere, as Insta360 Studio writes it", exiftest.GPanoPacket(sphereProps...), photoSphere},
		{"the properties as elements, as in Google's example", exiftest.XMPPacket(`<rdf:Description rdf:about="" xmlns:GPano="` + exiftest.GPanoNS + `">
   <GPano:UsePanoramaViewer>True</GPano:UsePanoramaViewer>
   <GPano:ProjectionType>
     equirectangular
   </GPano:ProjectionType>
   <GPano:CroppedAreaImageWidthPixels>4000</GPano:CroppedAreaImageWidthPixels>
   <GPano:FullPanoWidthPixels>8000</GPano:FullPanoWidthPixels>
  </rdf:Description>`), Pano{Projection: "equirectangular", Partial: true}},
		{"the namespace under another prefix", exiftest.XMPPacket(`<rdf:Description xmlns:gp="` + exiftest.GPanoNS + `" gp:ProjectionType="equirectangular"/>`), photoSphere},
		{"the GPano prefix for another namespace", exiftest.XMPPacket(`<rdf:Description xmlns:GPano="http://example.com/not-gpano/" GPano:ProjectionType="equirectangular"/>`), Pano{}},
		{"a prefix nobody declared", exiftest.XMPPacket(`<rdf:Description GPano:ProjectionType="equirectangular"/>`), Pano{}},
		{"a phone's cylindrical panorama", exiftest.GPanoPacket("ProjectionType=cylindrical", "CroppedAreaImageWidthPixels=9000", "FullPanoWidthPixels=9000"), Pano{Projection: "cylindrical"}},
		{"part of a sphere: narrower", part(6000, 4000), Pano{Projection: "equirectangular", Partial: true}},
		{"part of a sphere: lower", part(8000, 2000), Pano{Projection: "equirectangular", Partial: true}},
		{"a cropped area as large as the panorama", part(8000, 4000), photoSphere},
		{"a cropped area larger than the panorama says nothing", part(9000, 4000), photoSphere},
		{"a cropped size without the full one says nothing", exiftest.GPanoPacket("ProjectionType=equirectangular", "CroppedAreaImageWidthPixels=4000"), photoSphere},
		{"sizes that are not whole numbers say nothing", exiftest.GPanoPacket("ProjectionType=equirectangular", "CroppedAreaImageWidthPixels=4000.5", "FullPanoWidthPixels=8000"), photoSphere},
		{"a cropped size of zero says nothing", exiftest.GPanoPacket("ProjectionType=equirectangular", "CroppedAreaImageWidthPixels=0", "FullPanoWidthPixels=8000"), photoSphere},
		{"values with spaces round them", exiftest.GPanoPacket("ProjectionType=&#9;equirectangular ", "CroppedAreaImageWidthPixels= 4000", "FullPanoWidthPixels=8000 "), Pano{Projection: "equirectangular", Partial: true}},
		{"GPano without a projection", exiftest.GPanoPacket("UsePanoramaViewer=True"), Pano{}},
		{"an empty projection", exiftest.GPanoPacket("ProjectionType="), Pano{}},
		{"XMP without GPano", exiftest.XMPPacket(`<rdf:Description xmlns:xmp="http://ns.adobe.com/xap/1.0/" xmp:CreatorTool="Editor"/>`), Pano{}},
		{"the first projection wins", exiftest.XMPPacket(`<rdf:Description xmlns:GPano="` + exiftest.GPanoNS + `" GPano:ProjectionType="cylindrical"/>
  <rdf:Description xmlns:GPano="` + exiftest.GPanoNS + `" GPano:ProjectionType="equirectangular"/>`), Pano{Projection: "cylindrical"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, readPanoOf(exiftest.JPEG(exiftest.Photo{XMP: tt.packet})))
		})
	}
	t.Run("no XMP", func(t *testing.T) {
		assert.Equal(t, Pano{}, readPanoOf(exiftest.JPEG(exiftest.Photo{Model: "SM-S921B"})))
	})
}

// TestExtractExif_PanoEveryImageFormat reads the projection wherever each format keeps its XMP, and
// the EXIF as it reads without it.
func TestExtractExif_PanoEveryImageFormat(t *testing.T) {
	zone := CameraZone{Loc: time.UTC}
	photo := exiftest.Photo{Make: "Arashi Vision", Model: "insta360 oners", DateTimeOriginal: "2022:07:20 17:06:24", PixelX: 6080, PixelY: 3040}
	withXMP := photo
	withXMP.XMP = exiftest.GPanoPacket(sphereProps...)
	formats := []struct {
		name  string
		build func(exiftest.Photo) []byte
		want  Pano
	}{
		{"jpeg", exiftest.JPEG, photoSphere},
		{"tiff", exiftest.TIFF, photoSphere},
		{"webp", exiftest.WebP, photoSphere},
		{"heif", exiftest.HEIF, photoSphere},
		{"png, whose XMP imagemeta does not read", exiftest.PNG, Pano{}},
	}
	for _, f := range formats {
		t.Run(f.name, func(t *testing.T) {
			want := ExtractExif(bytes.NewReader(f.build(photo)), zone)
			require.NotNil(t, want.TakenAt)
			want.Pano = f.want

			assert.Equal(t, want, ExtractExif(bytes.NewReader(f.build(withXMP)), zone))
		})
	}
}

// TestExtractExif_BrokenXMPKeepsTheExif checks that the XMP is read apart from the EXIF: a packet
// that is not well-formed, before or after the EXIF, costs at most the projection.
func TestExtractExif_BrokenXMPKeepsTheExif(t *testing.T) {
	zone := CameraZone{Loc: mustZone(t, "Europe/Warsaw")}
	photo := exiftest.Photo{Make: "Arashi Vision", Model: "insta360 x5", DateTimeOriginal: "2025:06:01 12:13:14",
		PixelX: 11904, PixelY: 5952, GPS: &exiftest.GPS{Lat: 47.4979, Lng: 19.0402}}
	plain := ExtractExif(bytes.NewReader(exiftest.JPEG(photo)), zone)
	require.NotNil(t, plain.TakenAt)
	require.NotNil(t, plain.Lat)

	packets := []struct {
		name   string
		packet string
		want   Pano
	}{
		{"not XML", "this is not XML <<<", Pano{}},
		{"cut off in the description", exiftest.GPanoPacket(sphereProps...)[:200], Pano{}},
		{"an end tag that does not match, after the properties", `<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">` +
			`<rdf:Description xmlns:GPano="` + exiftest.GPanoNS + `" GPano:ProjectionType="cylindrical"></rdf:RDF></x:xmpmeta>`, Pano{Projection: "cylindrical"}},
		{"an entity nobody declared", exiftest.XMPPacket(`<rdf:Description xmlns:GPano="` + exiftest.GPanoNS + `" GPano:ProjectionType="&sphere;"/>`), Pano{}},
		{"a DTD", `<!DOCTYPE x [<!ENTITY e "equirectangular">]>` + exiftest.XMPPacket(`<rdf:Description xmlns:GPano="`+exiftest.GPanoNS+`" GPano:ProjectionType="&e;"/>`), Pano{}},
		{"empty", "", Pano{}},
	}
	for _, p := range packets {
		after := photo
		after.XMP = p.packet
		jpeg := exiftest.JPEG(photo)
		before := append(append(append([]byte{}, jpeg[:2]...), exiftest.XMPSegment(p.packet)...), jpeg[2:]...)
		for layout, data := range map[string][]byte{"after the exif": exiftest.JPEG(after), "before the exif": before} {
			t.Run(p.name+"/"+layout, func(t *testing.T) {
				want := plain
				want.Pano = p.want
				assert.Equal(t, want, ExtractExif(bytes.NewReader(data), zone))
			})
		}
	}
}

// webpWithXMP is a WebP whose XMP chunk holds packet, which may be larger than a JPEG segment.
func webpWithXMP(packet string) []byte {
	return exiftest.WebP(exiftest.Photo{XMP: packet})
}

func TestExtractExif_XMPLimits(t *testing.T) {
	description := `<rdf:Description xmlns:GPano="` + exiftest.GPanoNS + `" GPano:ProjectionType="equirectangular"/>`

	t.Run("properties past the scanned bytes are not read", func(t *testing.T) {
		comment := "<!--" + strings.Repeat(" ", xmpScanSize) + "-->"
		assert.Equal(t, Pano{}, readPanoOf(webpWithXMP(exiftest.XMPPacket(comment+description))))
		assert.Equal(t, photoSphere, readPanoOf(webpWithXMP(exiftest.XMPPacket(description+comment))), "padding after the properties")
	})
	t.Run("properties past the token limit are not read", func(t *testing.T) {
		elements := func(n int) string { return strings.Repeat("<a/>", n) }
		assert.Equal(t, Pano{}, readPanoOf(webpWithXMP(exiftest.XMPPacket(elements(xmpTokenLimit)+description))))
		assert.Equal(t, photoSphere, readPanoOf(webpWithXMP(exiftest.XMPPacket(elements(500)+description))), "a thousand tokens before them")
	})

	// Packets of about 1 MiB built to be costly: reading one stays within what one EXIF decode may cost.
	costly := map[string]string{
		"deep nesting":       strings.Repeat("<a>", 1<<18),
		"many elements":      strings.Repeat("<a/>", 1<<18),
		"many attributes":    "<a " + strings.Repeat(`b="" `, 1<<18) + "/>",
		"many namespaces":    "<a " + strings.Repeat(`xmlns:b="c" `, 1<<16) + "/>",
		"one long text":      "<a>" + strings.Repeat("t", 1<<20) + "</a>",
		"one long attribute": "<a b=\"" + strings.Repeat("v", 1<<20) + "\"/>",
	}
	for name, body := range costly {
		t.Run(name, func(t *testing.T) {
			data := webpWithXMP(exiftest.XMPPacket(body))
			var got Pano
			crafted.Bounded(t, maxDecodeAlloc, func() { got = readPanoOf(data) })
			assert.Equal(t, Pano{}, got)
		})
	}
}
