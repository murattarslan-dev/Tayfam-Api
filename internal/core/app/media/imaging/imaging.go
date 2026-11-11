// Package imaging re-encodes uploaded images with the Go standard library
// only (ADR-0020 item 1, option A): the real type comes from the first
// bytes, the dimensions are checked before decoding, the EXIF orientation is
// applied to the pixels, and two JPEG sizes are written from the pixels
// alone. No metadata of the input (EXIF, GPS, XMP, ICC, comments, embedded
// thumbnails) can reach the output: the encoder is given nothing but
// pixels.
//
// Inputs are untrusted. Decoding is memory safe (Go); a panic in a decoder
// is recovered and the file rejected as corrupt.
package imaging

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
)

// Kind is the real type of a file, read from its first bytes.
type Kind int

// Kinds. WebP and HEIC are recognised but not processed (option A).
const (
	Unknown Kind = iota
	JPEG
	PNG
	WebP
	HEIC
)

// Sniff returns the kind of b from its signature; the declared type and
// the file name are never consulted.
func Sniff(b []byte) Kind {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return JPEG
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return PNG
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return WebP
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		switch string(b[8:12]) {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1":
			return HEIC
		}
	}
	return Unknown
}

// Reason is why a file is refused (uploads.reject_reason).
type Reason string

// Reasons.
const (
	ReasonNotImage    Reason = "not_image"
	ReasonUnsupported Reason = "unsupported_type"
	ReasonTooLarge    Reason = "too_large"
	ReasonCorrupt     Reason = "corrupt"
)

// RejectError is a refusal caused by the file itself; retrying does not
// help.
type RejectError struct{ Reason Reason }

func (e *RejectError) Error() string { return "dosya reddedildi: " + string(e.Reason) }

func reject(r Reason) error { return &RejectError{Reason: r} }

// Options bound the work.
type Options struct {
	// MaxSide is the longest side the input may have (pixels).
	MaxSide int
	// MaxPixels is the most pixels the input may have (decompression
	// bombs: a small file that decodes to gigabytes).
	MaxPixels int
	// Full and Preview are the longest sides of the outputs; smaller
	// inputs are not enlarged.
	Full, Preview int
	// Quality is the JPEG quality (1–100).
	Quality int
}

// Defaults returns the default options (ADR-0020 item 2).
func Defaults() Options {
	return Options{MaxSide: 10000, MaxPixels: 25_000_000, Full: 1600, Preview: 400, Quality: 82}
}

// Result is a processed image.
type Result struct {
	Full, Preview []byte
	// ContentType of both outputs.
	ContentType string
	// Width and Height of Full, after orientation.
	Width, Height int
}

// Process decodes data and writes the two sizes. A *RejectError means the
// file is bad; any other error is a bug.
func Process(data []byte, o Options) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			res, err = Result{}, reject(ReasonCorrupt)
		}
	}()
	var decodeConfig func([]byte) (image.Config, error)
	var decode func([]byte) (image.Image, error)
	orientation := 1
	switch Sniff(data) {
	case JPEG:
		decodeConfig = func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) }
		orientation = jpegOrientation(data)
	case PNG:
		decodeConfig = func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
	case WebP, HEIC:
		return Result{}, reject(ReasonUnsupported)
	default:
		return Result{}, reject(ReasonNotImage)
	}
	cfg, err := decodeConfig(data)
	if err != nil {
		return Result{}, reject(ReasonCorrupt)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Result{}, reject(ReasonCorrupt)
	}
	if cfg.Width > o.MaxSide || cfg.Height > o.MaxSide || cfg.Width*cfg.Height > o.MaxPixels {
		return Result{}, reject(ReasonTooLarge)
	}
	src, err := decode(data)
	if err != nil {
		return Result{}, reject(ReasonCorrupt)
	}
	b := src.Bounds()
	if b.Dx() != cfg.Width || b.Dy() != cfg.Height {
		return Result{}, reject(ReasonCorrupt)
	}

	// Sizes in display orientation; scaling happens before rotating, on
	// the stored axes.
	sw, sh := b.Dx(), b.Dy()
	swapped := orientation >= 5
	dispW, dispH := sw, sh
	if swapped {
		dispW, dispH = sh, sw
	}
	fw, fh := fit(dispW, dispH, o.Full)
	tw, th := fw, fh
	if swapped {
		tw, th = fh, fw
	}
	full := orient(downscale(src, tw, th), orientation)
	pw, ph := fit(fw, fh, o.Preview)
	preview := downscale(full, pw, ph)

	fullJPEG, err := encode(full, o.Quality)
	if err != nil {
		return Result{}, err
	}
	previewJPEG, err := encode(preview, o.Quality)
	if err != nil {
		return Result{}, err
	}
	return Result{Full: fullJPEG, Preview: previewJPEG, ContentType: "image/jpeg", Width: fw, Height: fh}, nil
}

func encode(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("jpeg yazılamadı: %w", err)
	}
	return buf.Bytes(), nil
}

// fit scales w×h down so the longest side is at most max; never up.
func fit(w, h, max int) (int, int) {
	if w <= max && h <= max {
		return w, h
	}
	if w >= h {
		return max, maxInt(1, (h*max+w/2)/w)
	}
	return maxInt(1, (w*max+h/2)/h), max
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// downscale averages the source pixels that fall into each destination
// pixel (box filter; dw ≤ width, dh ≤ height). Transparency is flattened
// onto white. The result is opaque RGBA.
func downscale(src image.Image, dw, dh int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	col := make([]int, sw)
	for x := range col {
		col[x] = x * dw / sw
	}
	acc := make([]uint64, dw*3)
	cnt := make([]uint32, dw)
	row := make([]uint8, sw*3)
	flush := func(dy int) {
		p := dst.Pix[dy*dst.Stride:]
		for dx := 0; dx < dw; dx++ {
			n := uint64(cnt[dx])
			if n == 0 {
				n = 1
			}
			// Averages of 8-bit values: at most 255.
			p[dx*4+0] = uint8((acc[dx*3+0] + n/2) / n) //nolint:gosec // ≤ 255
			p[dx*4+1] = uint8((acc[dx*3+1] + n/2) / n) //nolint:gosec // ≤ 255
			p[dx*4+2] = uint8((acc[dx*3+2] + n/2) / n) //nolint:gosec // ≤ 255
			p[dx*4+3] = 0xFF
			acc[dx*3+0], acc[dx*3+1], acc[dx*3+2], cnt[dx] = 0, 0, 0, 0
		}
	}
	cur := 0
	for sy := 0; sy < sh; sy++ {
		if dy := sy * dh / sh; dy != cur {
			flush(cur)
			cur = dy
		}
		readRow(src, b.Min.Y+sy, row)
		for sx := 0; sx < sw; sx++ {
			c := col[sx]
			acc[c*3+0] += uint64(row[sx*3+0])
			acc[c*3+1] += uint64(row[sx*3+1])
			acc[c*3+2] += uint64(row[sx*3+2])
			cnt[c]++
		}
	}
	flush(cur)
	return dst
}

// over composites a premultiplied channel with alpha a onto white.
func over(c, a uint8) uint8 { return c + (0xFF - a) }

// readRow writes row y of src as RGB (on white) into out.
func readRow(src image.Image, y int, out []uint8) {
	b := src.Bounds()
	switch s := src.(type) {
	case *image.YCbCr:
		for x := 0; x < b.Dx(); x++ {
			yi, ci := s.YOffset(b.Min.X+x, y), s.COffset(b.Min.X+x, y)
			out[x*3], out[x*3+1], out[x*3+2] = color.YCbCrToRGB(s.Y[yi], s.Cb[ci], s.Cr[ci])
		}
	case *image.Gray:
		p := s.Pix[(y-b.Min.Y)*s.Stride:]
		for x := 0; x < b.Dx(); x++ {
			out[x*3], out[x*3+1], out[x*3+2] = p[x], p[x], p[x]
		}
	case *image.RGBA:
		p := s.Pix[(y-b.Min.Y)*s.Stride:]
		for x := 0; x < b.Dx(); x++ {
			a := p[x*4+3]
			out[x*3], out[x*3+1], out[x*3+2] = over(p[x*4], a), over(p[x*4+1], a), over(p[x*4+2], a)
		}
	case *image.NRGBA:
		p := s.Pix[(y-b.Min.Y)*s.Stride:]
		for x := 0; x < b.Dx(); x++ {
			a := uint16(p[x*4+3])
			for c := 0; c < 3; c++ {
				out[x*3+c] = uint8((uint16(p[x*4+c])*a + 0xFF*(0xFF-a) + 127) / 0xFF) //nolint:gosec // ≤ 255
			}
		}
	case *image.CMYK:
		p := s.Pix[(y-b.Min.Y)*s.Stride:]
		for x := 0; x < b.Dx(); x++ {
			out[x*3], out[x*3+1], out[x*3+2] = color.CMYKToRGB(p[x*4], p[x*4+1], p[x*4+2], p[x*4+3])
		}
	default:
		for x := 0; x < b.Dx(); x++ {
			// RGBA() is 16-bit per channel: the high byte fits.
			r, g, bl, a := src.At(b.Min.X+x, y).RGBA()
			a8 := uint8(a >> 8)                                                                                     //nolint:gosec // 16-bit value
			out[x*3], out[x*3+1], out[x*3+2] = over(uint8(r>>8), a8), over(uint8(g>>8), a8), over(uint8(bl>>8), a8) //nolint:gosec // 16-bit values
		}
	}
}

// orient applies an EXIF orientation (1–8) to img.
func orient(img *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return img
	}
	w, h := img.Rect.Dx(), img.Rect.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			case 8:
				sx, sy = w-1-y, x
			}
			si, di := sy*img.Stride+sx*4, y*dst.Stride+x*4
			copy(dst.Pix[di:di+4], img.Pix[si:si+4])
		}
	}
	return dst
}

var errNoOrientation = errors.New("yön etiketi yok")

// jpegOrientation reads the EXIF Orientation tag (0x0112) of a JPEG; 1
// when there is none or the data is malformed. Nothing else of the EXIF
// block is read or kept.
func jpegOrientation(b []byte) int {
	o, err := findOrientation(b)
	if err != nil || o < 1 || o > 8 {
		return 1
	}
	return o
}

func findOrientation(b []byte) (int, error) {
	i := 2 // after SOI
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 0, errNoOrientation
		}
		marker := b[i+1]
		if marker == 0xFF { // fill byte
			i++
			continue
		}
		if marker == 0xDA || marker == 0xD9 { // start of scan, end of image
			return 0, errNoOrientation
		}
		size := int(b[i+2])<<8 | int(b[i+3])
		if size < 2 || i+2+size > len(b) {
			return 0, errNoOrientation
		}
		seg := b[i+4 : i+2+size]
		if marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 0, errNoOrientation
}

func tiffOrientation(t []byte) (int, error) {
	if len(t) < 8 {
		return 0, errNoOrientation
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(t[:2]) {
	case "II":
		u16 = func(p []byte) int { return int(p[0]) | int(p[1])<<8 }
		u32 = func(p []byte) int { return int(p[0]) | int(p[1])<<8 | int(p[2])<<16 | int(p[3])<<24 }
	case "MM":
		u16 = func(p []byte) int { return int(p[1]) | int(p[0])<<8 }
		u32 = func(p []byte) int { return int(p[3]) | int(p[2])<<8 | int(p[1])<<16 | int(p[0])<<24 }
	default:
		return 0, errNoOrientation
	}
	if u16(t[2:4]) != 42 {
		return 0, errNoOrientation
	}
	off := u32(t[4:8])
	if off < 8 || off+2 > len(t) {
		return 0, errNoOrientation
	}
	n := u16(t[off : off+2])
	for k := 0; k < n && k < 512; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			break
		}
		if u16(t[e:e+2]) == 0x0112 && u16(t[e+2:e+4]) == 3 && u32(t[e+4:e+8]) == 1 {
			return u16(t[e+8 : e+10]), nil
		}
	}
	return 0, errNoOrientation
}
