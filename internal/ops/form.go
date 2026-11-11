package ops

import (
	"bytes"
	"errors"
	"io"
)

// errBadForm is returned for any malformed or oversized form body.
var errBadForm = errors.New("bad form")

// readBody reads at most limit bytes into a fresh buffer the caller wipes.
// io.ReadAll is avoided because its growing buffers leave copies behind that
// cannot be wiped.
func readBody(r io.Reader, limit int) ([]byte, error) {
	buf := make([]byte, limit+1)
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			wipe(buf)
			return nil, errBadForm
		}
	}
	if n > limit {
		wipe(buf)
		return nil, errBadForm
	}
	return buf[:n], nil
}

// parseForm decodes an application/x-www-form-urlencoded body into byte
// slices so that values can be wiped after use (url.ParseQuery would leave
// immutable strings behind). Repeated keys are rejected.
func parseForm(body []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, pair := range bytes.Split(body, []byte("&")) {
		if len(pair) == 0 {
			continue
		}
		k, v, _ := bytes.Cut(pair, []byte("="))
		key, err := unescape(k)
		if err != nil {
			wipeForm(out)
			return nil, errBadForm
		}
		name := string(key)
		if _, dup := out[name]; dup {
			wipeForm(out)
			return nil, errBadForm
		}
		val, err := unescape(v)
		if err != nil {
			wipeForm(out)
			return nil, errBadForm
		}
		out[name] = val
	}
	return out, nil
}

func unescape(s []byte) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '+':
			out = append(out, ' ')
		case '%':
			if i+2 >= len(s) {
				wipe(out[:cap(out)])
				return nil, errBadForm
			}
			h, ok1 := unhex(s[i+1])
			l, ok2 := unhex(s[i+2])
			if !ok1 || !ok2 {
				wipe(out[:cap(out)])
				return nil, errBadForm
			}
			out = append(out, h<<4|l)
			i += 2
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// wipe overwrites b with zeros.
func wipe(b []byte) {
	clear(b)
}

func wipeForm(f map[string][]byte) {
	for k, v := range f {
		wipe(v)
		delete(f, k)
	}
}
