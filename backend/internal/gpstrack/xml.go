package gpstrack

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"time"
)

// Elements are matched by local name, whatever their namespace (GPX 1.0 and 1.1, TCX v1 and
// v2, KML with or without the gx prefix declared).

func newXMLDecoder(r io.Reader) *xml.Decoder {
	dec := xml.NewDecoder(r)
	dec.CharsetReader = charsetReader
	return dec
}

// charsetReader accepts the encodings expat (Loom's XML parser) reads besides UTF-8.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "l1":
		return &latin1Reader{r: input}, nil
	case "us-ascii", "ascii":
		return input, nil
	}
	return nil, errors.New("unsupported XML encoding " + label)
}

type latin1Reader struct {
	r   io.Reader
	buf []byte
}

// Read expands each byte to its UTF-8 encoding (one or two bytes), so it reads at most half of p.
func (l *latin1Reader) Read(p []byte) (int, error) {
	if len(p) < 2 {
		return 0, io.ErrShortBuffer
	}
	if cap(l.buf) < len(p)/2 {
		l.buf = make([]byte, len(p)/2)
	}
	n, err := l.r.Read(l.buf[:len(p)/2])
	out := p[:0]
	for _, b := range l.buf[:n] {
		out = append(out, string(rune(b))...)
	}
	return len(out), err
}

// eachElement streams the XML in r and calls fn for every element with the local name name.
// texts maps the path of each element below it ("time", "Position/LatitudeDegrees") to the
// element's text; the first element on a path wins.
func eachElement(r io.Reader, name string, fn func(attrs []xml.Attr, texts map[string]string)) error {
	dec := newXMLDecoder(r)
	var stack []string // local names below the matched element
	var attrs []xml.Attr
	var texts map[string]string
	var text strings.Builder
	inside := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			text.Reset()
			if inside {
				stack = append(stack, t.Name.Local)
			} else if t.Name.Local == name {
				inside, attrs, texts, stack = true, t.Attr, map[string]string{}, stack[:0]
			}
		case xml.CharData:
			if inside {
				text.Write(t)
			}
		case xml.EndElement:
			if !inside {
				break
			}
			if len(stack) == 0 {
				fn(attrs, texts)
				inside = false
				break
			}
			p := strings.Join(stack, "/")
			if _, seen := texts[p]; !seen {
				texts[p] = text.String()
			}
			stack = stack[:len(stack)-1]
			text.Reset()
		}
	}
}

func attr(attrs []xml.Attr, name string) string {
	for _, a := range attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// --- GPX (Strava, Komoot, any GPX) ---

// parseGPX reads every trkpt with a time; ele is optional.
func parseGPX(r io.Reader) ([]Point, error) {
	var points []Point
	err := eachElement(r, "trkpt", func(attrs []xml.Attr, texts map[string]string) {
		lat, err1 := parseFloat(attr(attrs, "lat"))
		lng, err2 := parseFloat(attr(attrs, "lon"))
		t, err3 := parseISO8601(texts["time"])
		if err1 != nil || err2 != nil || err3 != nil {
			return
		}
		points = append(points, Point{Time: t, Lat: lat, Lng: lng, AltitudeM: optFloat(texts["ele"])})
	})
	return points, err
}

// --- TCX (Garmin Training Center XML, Strava export) ---

// parseTCX reads every Trackpoint with a Time and a Position; AltitudeMeters is optional.
func parseTCX(r io.Reader) ([]Point, error) {
	var points []Point
	err := eachElement(r, "Trackpoint", func(_ []xml.Attr, texts map[string]string) {
		t, err1 := parseISO8601(texts["Time"])
		lat, err2 := parseFloat(texts["Position/LatitudeDegrees"])
		lng, err3 := parseFloat(texts["Position/LongitudeDegrees"])
		if err1 != nil || err2 != nil || err3 != nil {
			return
		}
		points = append(points, Point{Time: t, Lat: lat, Lng: lng, AltitudeM: optFloat(texts["AltitudeMeters"])})
	})
	return points, err
}

// --- KML / KMZ (Google Earth, Google Maps history) ---

// xmlNode is a parsed element, for the KML lookups that search below an element.
type xmlNode struct {
	name     string // local name
	text     string
	children []*xmlNode
}

func parseXMLTree(r io.Reader) (*xmlNode, error) {
	dec := newXMLDecoder(r)
	var root *xmlNode
	var stack []*xmlNode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{name: t.Name.Local}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, errors.New("no root element")
	}
	return root, nil
}

// child is the first direct child named name.
func (n *xmlNode) child(name string) *xmlNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

// find is the first element named name below n, in document order (ElementTree's ".//name").
func (n *xmlNode) find(name string) *xmlNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
		if f := c.find(name); f != nil {
			return f
		}
	}
	return nil
}

// each calls fn for n and every element below it named name, in document order.
func (n *xmlNode) each(name string, fn func(*xmlNode)) {
	if n.name == name {
		fn(n)
	}
	for _, c := range n.children {
		c.each(name, fn)
	}
}

func parseKML(r io.Reader) ([]Point, error) {
	root, err := parseXMLTree(r)
	if err != nil {
		return nil, err
	}
	var points []Point

	// gx:Track: the n-th <when> belongs to the n-th <gx:coord> ("lon lat [alt]").
	root.each("Track", func(track *xmlNode) {
		var whens, coords []string
		for _, c := range track.children {
			switch {
			case c.text == "":
			case c.name == "when":
				whens = append(whens, c.text)
			case c.name == "coord":
				coords = append(coords, c.text)
			}
		}
		for i := 0; i < len(whens) && i < len(coords); i++ {
			t, err := parseISO8601(whens[i])
			if err != nil {
				continue
			}
			if p, ok := kmlPosition(strings.Fields(coords[i]), t); ok {
				points = append(points, p)
			}
		}
	})

	// A Placemark with a TimeSpan or TimeStamp and a Point, or a LineString whose vertices are
	// spread evenly over the span.
	root.each("Placemark", func(pm *xmlNode) {
		var begin, end time.Time
		var err error
		if span := pm.find("TimeSpan"); span != nil {
			begin, err = kmlChildTime(span, "begin")
			if err != nil {
				return
			}
			if end, err = kmlChildTime(span, "end"); err != nil {
				end = begin
			}
		} else if stamp := pm.find("TimeStamp"); stamp != nil {
			if begin, err = kmlChildTime(stamp, "when"); err != nil {
				return
			}
			end = begin
		} else {
			return
		}

		if pt := pm.find("Point"); pt != nil {
			if c := pt.child("coordinates"); c != nil {
				if p, ok := kmlPosition(strings.Split(strings.TrimSpace(c.text), ","), begin); ok {
					points = append(points, p)
				}
			}
			return
		}
		ls := pm.find("LineString")
		if ls == nil {
			return
		}
		c := ls.child("coordinates")
		if c == nil {
			return
		}
		vertices := strings.Fields(c.text)
		for i, v := range vertices {
			if p, ok := kmlPosition(strings.Split(v, ","), spread(begin, end, i, len(vertices))); ok {
				points = append(points, p)
			}
		}
	})
	return points, nil
}

func kmlChildTime(n *xmlNode, name string) (time.Time, error) {
	c := n.child(name)
	if c == nil {
		return time.Time{}, errors.New("missing " + name)
	}
	return parseISO8601(c.text)
}

// kmlPosition reads lon, lat and an optional altitude.
func kmlPosition(parts []string, at time.Time) (Point, bool) {
	if len(parts) < 2 {
		return Point{}, false
	}
	lng, err1 := parseFloat(parts[0])
	lat, err2 := parseFloat(parts[1])
	if err1 != nil || err2 != nil {
		return Point{}, false
	}
	p := Point{Time: at, Lat: lat, Lng: lng}
	if len(parts) > 2 {
		p.AltitudeM = optFloat(parts[2])
	}
	return p, true
}

// parseKMZ reads the first .kml file in the ZIP archive.
func parseKMZ(r io.Reader) ([]Point, error) {
	data, err := io.ReadAll(limitExpanded(r))
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".kml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return parseKML(limitExpanded(rc))
	}
	return nil, errors.New("no .kml file in the archive")
}
