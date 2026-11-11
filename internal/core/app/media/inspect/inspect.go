// Package inspect checks the headers of an uploaded image variant without
// decoding any pixels (ADR-0020 item 2): the real type from the first
// bytes, the dimensions from the header, and that no metadata block is
// present. The mobile client strips metadata and encodes the variants; this
// is the server's cheap check of that promise.
//
// JPEG: every segment up to the start of scan is walked; the size comes
// from the SOFn segment. APP1 (EXIF, which carries GPS, and XMP), APP13
// (Photoshop/IPTC) and APP2 MPF (multi-picture: a second, unchecked image
// after the first) are metadata. JFIF, ICC profiles, Adobe and comment
// segments are allowed.
//
// WebP: the RIFF chunks are walked; the size comes from VP8X, VP8 or VP8L.
// An EXIF or XMP chunk, or the VP8X flag for one, is metadata; animation is
// not accepted. The image data chunk must be the file's last chunk and the
// RIFF size must match the object's size, so no chunk can hide after the
// part that was read.
//
// Input is untrusted: every offset is bounds-checked, and running out of
// the bytes read before the header is complete rejects the file.
package inspect

import (
	"bytes"
	"encoding/binary"
)

// Types.
const (
	JPEG = "image/jpeg"
	WebP = "image/webp"
)

// Reason is why a variant is refused (uploads.reject_reason).
type Reason string

// Reasons.
const (
	NotImage    Reason = "not_image"
	Unsupported Reason = "unsupported_type"
	Metadata    Reason = "metadata_present"
)

// RejectError is a refusal caused by the file.
type RejectError struct{ Reason Reason }

func (e *RejectError) Error() string { return "dosya reddedildi: " + string(e.Reason) }

func reject(r Reason) error { return &RejectError{Reason: r} }

// Info is what the header says.
type Info struct {
	Type          string
	Width, Height int
}

// Inspect reads head, the first bytes of an object of total bytes (head is
// the whole object when it is small).
func Inspect(head []byte, total int64) (Info, error) {
	switch {
	case len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return inspectJPEG(head)
	case len(head) >= 12 && string(head[0:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return inspectWebP(head, total)
	case bytes.HasPrefix(head, []byte("\x89PNG\r\n\x1a\n")),
		bytes.HasPrefix(head, []byte("GIF87a")), bytes.HasPrefix(head, []byte("GIF89a")),
		len(head) >= 12 && string(head[4:8]) == "ftyp" && isHEIF(string(head[8:12])):
		return Info{}, reject(Unsupported)
	}
	return Info{}, reject(NotImage)
}

func isHEIF(brand string) bool {
	switch brand {
	case "heic", "heix", "hevc", "hevx", "heim", "heis", "mif1", "msf1", "avif":
		return true
	}
	return false
}

func isSOF(m byte) bool {
	switch m {
	case 0xC0, 0xC1, 0xC2, 0xC3, 0xC5, 0xC6, 0xC7, 0xC9, 0xCA, 0xCB, 0xCD, 0xCE, 0xCF:
		return true
	}
	return false
}

func inspectJPEG(b []byte) (Info, error) {
	var info Info
	i := 2 // after SOI
	for {
		if i+2 > len(b) || b[i] != 0xFF {
			return Info{}, reject(NotImage)
		}
		m := b[i+1]
		switch {
		case m == 0xFF: // fill byte
			i++
			continue
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7): // no length
			i += 2
			continue
		case m == 0xD8 || m == 0xD9: // SOI again, EOI before the scan
			return Info{}, reject(NotImage)
		}
		if i+4 > len(b) {
			return Info{}, reject(NotImage)
		}
		n := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if n < 2 || i+2+n > len(b) {
			return Info{}, reject(NotImage)
		}
		seg := b[i+4 : i+2+n]
		switch {
		case m == 0xDA: // start of scan: the header is complete
			if info.Width == 0 {
				return Info{}, reject(NotImage)
			}
			info.Type = JPEG
			return info, nil
		case m == 0xE1, m == 0xED:
			return Info{}, reject(Metadata)
		case m == 0xE2 && bytes.HasPrefix(seg, []byte("MPF\x00")):
			return Info{}, reject(Metadata)
		case isSOF(m):
			if info.Width != 0 || len(seg) < 5 {
				return Info{}, reject(NotImage)
			}
			info.Height = int(binary.BigEndian.Uint16(seg[1:3]))
			info.Width = int(binary.BigEndian.Uint16(seg[3:5]))
			if info.Width == 0 || info.Height == 0 {
				return Info{}, reject(NotImage)
			}
		}
		i += 2 + n
	}
}

// WebP chunk names.
const (
	chunkVP8  = "VP8 "
	chunkVP8L = "VP8L"
	chunkVP8X = "VP8X"
)

func le24(p []byte) int { return int(p[0]) | int(p[1])<<8 | int(p[2])<<16 }

func inspectWebP(b []byte, total int64) (Info, error) {
	riff := int64(binary.LittleEndian.Uint32(b[4:8]))
	if riff+8 != total || riff%2 != 0 {
		return Info{}, reject(NotImage)
	}
	var (
		canvas   Info
		extended bool
	)
	off := int64(12)
	for first := true; ; first = false {
		if off+8 > int64(len(b)) {
			return Info{}, reject(NotImage)
		}
		name := string(b[off : off+4])
		size := int64(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		next := off + 8 + size + size%2
		if next > total {
			return Info{}, reject(NotImage)
		}
		payload := b[off+8 : min(off+8+size, int64(len(b)))]
		switch name {
		case chunkVP8X:
			if !first || len(payload) < 10 {
				return Info{}, reject(NotImage)
			}
			flags := payload[0]
			if flags&0x0C != 0 { // EXIF (0x08), XMP (0x04)
				return Info{}, reject(Metadata)
			}
			if flags&0x02 != 0 { // animation
				return Info{}, reject(Unsupported)
			}
			extended = true
			canvas = Info{Width: le24(payload[4:7]) + 1, Height: le24(payload[7:10]) + 1}
		case "ICCP", "ALPH":
			if !extended {
				return Info{}, reject(NotImage)
			}
		case "EXIF", "XMP ":
			return Info{}, reject(Metadata)
		case "ANIM", "ANMF":
			return Info{}, reject(Unsupported)
		case chunkVP8, chunkVP8L:
			if !first && !extended {
				return Info{}, reject(NotImage)
			}
			info, ok := frameSize(name, payload)
			if !ok {
				return Info{}, reject(NotImage)
			}
			// The image data must be the last chunk: nothing after it is
			// read, so nothing may be there.
			if next != total {
				return Info{}, reject(Metadata)
			}
			if extended && info != canvas {
				return Info{}, reject(NotImage)
			}
			info.Type = WebP
			return info, nil
		default:
			return Info{}, reject(NotImage)
		}
		off = next
	}
}

// frameSize reads the dimensions of a VP8 (lossy key frame) or VP8L
// (lossless) bitstream header.
func frameSize(name string, p []byte) (Info, bool) {
	if name == chunkVP8 {
		// Frame tag (3 bytes; bit 0 clear for a key frame), start code
		// 9d 01 2a, then 14-bit width and height.
		if len(p) < 10 || p[0]&1 != 0 || p[3] != 0x9D || p[4] != 0x01 || p[5] != 0x2A {
			return Info{}, false
		}
		w := int(binary.LittleEndian.Uint16(p[6:8]) & 0x3FFF)
		h := int(binary.LittleEndian.Uint16(p[8:10]) & 0x3FFF)
		return Info{Width: w, Height: h}, w > 0 && h > 0
	}
	// Signature 0x2f, then 14-bit width-1, 14-bit height-1, alpha bit and
	// a 3-bit version that must be 0.
	if len(p) < 5 || p[0] != 0x2F {
		return Info{}, false
	}
	bits := binary.LittleEndian.Uint32(p[1:5])
	if bits>>29 != 0 {
		return Info{}, false
	}
	return Info{Width: int(bits&0x3FFF) + 1, Height: int(bits>>14&0x3FFF) + 1}, true
}
