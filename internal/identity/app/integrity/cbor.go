package integrity

import (
	"encoding/binary"
	"errors"
)

// A small, strict CBOR (RFC 8949) reader for the two App Attest objects
// (attestation and assertion). It reads only what those objects use:
// unsigned and negative integers, byte and text strings, arrays, maps and
// the simple values false, true and null, all with definite lengths.
// Indefinite lengths, tags and floats are refused; so are trailing bytes,
// duplicate map keys and anything nested deeper than maxCBORDepth. It
// decodes nothing cryptographic: signatures and certificates are checked
// with the standard library.

var errCBOR = errors.New("integrity: geçersiz CBOR")

const (
	maxCBORDepth = 8
	// maxCBORItems bounds the elements of one array or map; App Attest
	// objects have at most a handful.
	maxCBORItems = 64
)

// cborMap is a decoded map with text keys; other keys are refused.
type cborMap map[string]any

// decodeCBOR decodes one item that fills data.
func decodeCBOR(data []byte) (any, error) {
	d := cborDecoder{data: data}
	v, err := d.item(0)
	if err != nil {
		return nil, err
	}
	if d.off != len(d.data) {
		return nil, errCBOR
	}
	return v, nil
}

type cborDecoder struct {
	data []byte
	off  int
}

func (d *cborDecoder) item(depth int) (any, error) {
	if depth > maxCBORDepth || d.off >= len(d.data) {
		return nil, errCBOR
	}
	head := d.data[d.off]
	d.off++
	major, info := head>>5, head&0x1f
	if major == 7 {
		switch info {
		case 20:
			return false, nil
		case 21:
			return true, nil
		case 22:
			return nil, nil
		}
		return nil, errCBOR // floats, undefined, other simple values
	}
	n, err := d.argument(info)
	if err != nil {
		return nil, err
	}
	switch major {
	case 0:
		return n, nil
	case 1:
		if n > 1<<63-1 {
			return nil, errCBOR
		}
		return -1 - int64(n), nil
	case 2, 3:
		b, err := d.take(n)
		if err != nil {
			return nil, err
		}
		if major == 3 {
			return string(b), nil
		}
		return b, nil
	case 4:
		if n > maxCBORItems {
			return nil, errCBOR
		}
		out := make([]any, 0, n)
		for range n {
			v, err := d.item(depth + 1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case 5:
		if n > maxCBORItems {
			return nil, errCBOR
		}
		out := make(cborMap, n)
		for range n {
			k, err := d.item(depth + 1)
			if err != nil {
				return nil, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, errCBOR
			}
			if _, dup := out[key]; dup {
				return nil, errCBOR
			}
			v, err := d.item(depth + 1)
			if err != nil {
				return nil, err
			}
			out[key] = v
		}
		return out, nil
	}
	return nil, errCBOR // tags (6)
}

// argument reads the head's argument. Indefinite lengths (31) and the
// reserved values (28–30) are refused.
func (d *cborDecoder) argument(info byte) (uint64, error) {
	switch {
	case info < 24:
		return uint64(info), nil
	case info == 24:
		b, err := d.take(1)
		if err != nil {
			return 0, err
		}
		return uint64(b[0]), nil
	case info == 25:
		b, err := d.take(2)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint16(b)), nil
	case info == 26:
		b, err := d.take(4)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint32(b)), nil
	case info == 27:
		b, err := d.take(8)
		if err != nil {
			return 0, err
		}
		return binary.BigEndian.Uint64(b), nil
	}
	return 0, errCBOR
}

func (d *cborDecoder) take(n uint64) ([]byte, error) {
	left := len(d.data) - d.off // 0 ≤ left ≤ len(d.data)
	if n > uint64(left) {       //nolint:gosec // left is not negative
		return nil, errCBOR
	}
	m := int(n) //nolint:gosec // n ≤ left, an int
	b := d.data[d.off : d.off+m]
	d.off += m
	return b, nil
}

// bytesOf returns m[key] as a byte string.
func (m cborMap) bytesOf(key string) ([]byte, bool) {
	b, ok := m[key].([]byte)
	return b, ok
}
