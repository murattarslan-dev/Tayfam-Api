// Package imagingtest builds test images: JPEGs carrying an EXIF block with
// an orientation and a GPS position (the case ADR-0007 is about: a fishing
// spot leaking through a photo), and plain PNGs.
package imagingtest

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// Marker is written into the EXIF block (ImageDescription and the GPS
// processing method) so a test can search the output for it.
const Marker = "GIZLI-MERA-38.4237N-27.1428E"

// Quadrants returns a w×h image whose top-left quadrant is red, top-right
// green, bottom-left blue and bottom-right white.
func Quadrants(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{255, 255, 255, 255}
			switch {
			case x < w/2 && y < h/2:
				c = color.RGBA{255, 0, 0, 255}
			case x >= w/2 && y < h/2:
				c = color.RGBA{0, 255, 0, 255}
			case x < w/2 && y >= h/2:
				c = color.RGBA{0, 0, 255, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// JPEG encodes img with an APP1 EXIF block holding orientation (1–8; 0
// leaves the tag out), a GPS IFD (latitude 38°25'25" N, longitude
// 27°8'34" E) and Marker.
func JPEG(img image.Image, orientation int) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	raw := buf.Bytes()
	app1 := exifSegment(orientation)
	out := append([]byte{}, raw[:2]...) // SOI
	out = append(out, app1...)
	return append(out, raw[2:]...)
}

// PNG encodes img.
func PNG(img image.Image) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
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
