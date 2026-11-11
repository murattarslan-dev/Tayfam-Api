// Package inspecttest builds image variants for tests of the header check:
// clean JPEGs and WebPs, and ones carrying EXIF with a GPS position (the
// case ADR-0007 is about: a fishing spot leaking through a photo), XMP,
// IPTC or a multi-picture segment. WebP files are synthetic: valid
// containers and frame headers with filler data; nothing decodes pixels.
package inspecttest

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
)

// Marker is written into the EXIF block (ImageDescription and the GPS
// processing method) so a test can search for it.
const Marker = "GIZLI-MERA-38.4237N-27.1428E"

// JPEG encodes a w×h image with the standard library (no metadata).
func JPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x), uint8(y), 128, 255}) //nolint:gosec // test pattern
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// WithSegment inserts a segment (marker FFxx and payload) after SOI.
func WithSegment(j []byte, marker byte, payload []byte) []byte {
	seg := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2)) //nolint:gosec // small test payloads
	out := append([]byte{}, j[:2]...)
	out = append(out, seg...)
	out = append(out, payload...)
	return append(out, j[2:]...)
}

// EXIF is an APP1 payload with orientation 6, a GPS IFD and Marker.
func EXIF() []byte { return exifSegment(6)[4:] }

// XMP is an APP1 XMP payload.
func XMP() []byte {
	return []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta><exif:GPSLatitude>38,25.42N</exif:GPSLatitude></x:xmpmeta>")
}

// WebPOptions shape a synthetic WebP.
type WebPOptions struct {
	// Lossy writes a VP8 frame, otherwise VP8L.
	Lossy bool
	// Extended writes a VP8X header with Flags and the canvas size.
	Extended bool
	Flags    byte
	// CanvasW and CanvasH override the canvas size (0: the frame's).
	CanvasW, CanvasH int
	// Before and After are extra chunks around the image data.
	Before, After [][2]string
}

func chunk(name string, payload []byte) []byte {
	h := make([]byte, 8)
	copy(h, name)
	binary.LittleEndian.PutUint32(h[4:], uint32(len(payload))) //nolint:gosec // small test payloads
	out := append(h, payload...)
	if len(payload)%2 == 1 {
		out = append(out, 0)
	}
	return out
}

// WebP builds a w×h WebP container.
func WebP(w, h int, o WebPOptions) []byte {
	var frame []byte
	if o.Lossy {
		frame = []byte{0x10, 0x02, 0x00, 0x9D, 0x01, 0x2A, 0, 0, 0, 0}
		binary.LittleEndian.PutUint16(frame[6:], uint16(w)) //nolint:gosec // test sizes
		binary.LittleEndian.PutUint16(frame[8:], uint16(h)) //nolint:gosec // test sizes
		frame = append(frame, bytes.Repeat([]byte{0x55}, 64)...)
	} else {
		bits := uint32(w-1) | uint32(h-1)<<14 //nolint:gosec // test sizes
		frame = []byte{0x2F, 0, 0, 0, 0}
		binary.LittleEndian.PutUint32(frame[1:], bits)
		frame = append(frame, bytes.Repeat([]byte{0xAA}, 63)...)
	}
	var body []byte
	if o.Extended {
		cw, ch := w, h
		if o.CanvasW > 0 {
			cw, ch = o.CanvasW, o.CanvasH
		}
		x := make([]byte, 10)
		x[0] = o.Flags
		put24 := func(p []byte, v int) { p[0], p[1], p[2] = byte(v), byte(v>>8), byte(v>>16) }
		put24(x[4:], cw-1)
		put24(x[7:], ch-1)
		body = append(body, chunk("VP8X", x)...)
	}
	for _, c := range o.Before {
		body = append(body, chunk(c[0], []byte(c[1]))...)
	}
	name := "VP8L"
	if o.Lossy {
		name = "VP8 "
	}
	body = append(body, chunk(name, frame)...)
	for _, c := range o.After {
		body = append(body, chunk(c[0], []byte(c[1]))...)
	}
	out := []byte("RIFF\x00\x00\x00\x00WEBP")
	binary.LittleEndian.PutUint32(out[4:], uint32(len(body)+4)) //nolint:gosec // small test files
	return append(out, body...)
}

type entry struct {
	tag, typ uint16
	count    uint32
	value    []byte // inline (≤ 4 bytes) or out of line
}

// exifSegment builds a little-endian TIFF with IFD0 and a GPS IFD.
func exifSegment(orientation int) []byte {
	le := binary.LittleEndian
	rational := func(n, d uint32) []byte {
		b := make([]byte, 8)
		le.PutUint32(b, n)
		le.PutUint32(b[4:], d)
		return b
	}
	cat := func(bs ...[]byte) []byte { return bytes.Join(bs, nil) }
	short := func(v uint16) []byte { b := make([]byte, 2); le.PutUint16(b, v); return b }
	ascii := func(s string) []byte { return append([]byte(s), 0) }

	gps := []entry{
		{0x0001, 2, 2, ascii("N")},
		{0x0002, 5, 3, cat(rational(38, 1), rational(25, 1), rational(25, 1))},
		{0x0003, 2, 2, ascii("E")},
		{0x0004, 5, 3, cat(rational(27, 1), rational(8, 1), rational(34, 1))},
		{0x001B, 7, uint32(len(Marker) + 8), append([]byte("ASCII\x00\x00\x00"), Marker...)},
	}
	ifd0 := []entry{{0x010E, 2, uint32(len(Marker) + 1), ascii(Marker)}}
	if orientation > 0 {
		ifd0 = append(ifd0, entry{0x0112, 3, 1, short(uint16(orientation))}) //nolint:gosec // 1–8
	}
	ifd0 = append(ifd0, entry{0x8825, 4, 1, nil}) // GPS IFD pointer, filled below

	ifdSize := func(es []entry) int { return 2 + 12*len(es) + 4 }
	ifd0Off := 8
	gpsOff := ifd0Off + ifdSize(ifd0)
	dataOff := gpsOff + ifdSize(gps)

	var data bytes.Buffer
	write := func(es []entry, gpsPtr uint32) []byte {
		var b bytes.Buffer
		b.Write(short(uint16(len(es)))) //nolint:gosec // a few entries
		for _, e := range es {
			b.Write(short(e.tag))
			b.Write(short(e.typ))
			c := make([]byte, 4)
			le.PutUint32(c, e.count)
			b.Write(c)
			v := make([]byte, 4)
			switch {
			case e.tag == 0x8825:
				le.PutUint32(v, gpsPtr)
			case len(e.value) <= 4:
				copy(v, e.value)
			default:
				le.PutUint32(v, uint32(dataOff+data.Len())) //nolint:gosec // small offsets
				data.Write(e.value)
				if data.Len()%2 == 1 {
					data.WriteByte(0)
				}
			}
			b.Write(v)
		}
		b.Write([]byte{0, 0, 0, 0})
		return b.Bytes()
	}
	i0 := write(ifd0, uint32(gpsOff)) //nolint:gosec // small offset
	ig := write(gps, 0)
	tiff := cat([]byte("II*\x00"), []byte{8, 0, 0, 0}, i0, ig, data.Bytes())
	payload := cat([]byte("Exif\x00\x00"), tiff)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2)) //nolint:gosec // under 64 KiB
	return append(seg, payload...)
}
