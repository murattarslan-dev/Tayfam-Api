package definition

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ValueError reports an event field whose value does not fit the type's
// definition. Field is the field's key, or "" when the request names a
// field the type does not have; the rejected value is never kept.
type ValueError struct {
	Field string
}

func (e *ValueError) Error() string {
	if e.Field == "" {
		return "alan değeri: bilinmeyen alan"
	}
	return "alan değeri: " + e.Field
}

// maxValueBytes bounds one value's JSON text before it is looked at: a
// long_text of 1000 characters, escaped, with room to spare.
const maxValueBytes = 8 << 10

// CleanValues checks the values of an event's fields against the type's
// fields (ADR-0025 §3) and returns them in their stored form, as JSON by
// field key. in maps a field key to its JSON value. A field that is not in
// in has no value; with complete set, every required field must be there.
// A JSON null, an empty text and an empty selection are refused: a field
// without a value is left out.
//
// Stored forms: text and long_text a trimmed string; integer and duration
// (minutes) an integer; decimal a number rounded to the field's decimals;
// choice a string; multi_choice a list of strings in the definition's
// order; boolean true or false; date "YYYY-MM-DD".
func (d *Definition) CleanValues(in map[string]json.RawMessage, complete bool) (map[string]json.RawMessage, error) {
	if len(in) > len(d.Fields) {
		return nil, &ValueError{}
	}
	out := make(map[string]json.RawMessage, len(in))
	for _, f := range d.Fields {
		raw, ok := in[f.Key]
		if !ok {
			if complete && f.Required {
				return nil, &ValueError{Field: f.Key}
			}
			continue
		}
		v, err := f.Clean(raw)
		if err != nil {
			return nil, err
		}
		out[f.Key] = v
	}
	if len(out) != len(in) {
		// A key that is not a field of the type.
		return nil, &ValueError{}
	}
	return out, nil
}

// Clean checks one value of the field and returns its stored form.
func (f Field) Clean(raw json.RawMessage) (json.RawMessage, error) {
	bad := &ValueError{Field: f.Key}
	if len(raw) == 0 || len(raw) > maxValueBytes {
		return nil, bad
	}
	v, err := decode(raw)
	if err != nil {
		return nil, bad
	}
	var clean any
	switch f.Kind {
	case KindText, KindLongText:
		s, ok := v.(string)
		if !ok {
			return nil, bad
		}
		s = strings.TrimSpace(s)
		if !validValueText(s, f.Kind == KindLongText) {
			return nil, bad
		}
		if n := utf8.RuneCountInString(s); n < 1 || n > f.MaxLength {
			return nil, bad
		}
		clean = s
	case KindInteger, KindDuration:
		n, ok := v.(json.Number)
		if !ok {
			return nil, bad
		}
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil || float64(i) < f.Min || float64(i) > f.Max {
			return nil, bad
		}
		clean = i
	case KindDecimal:
		n, ok := v.(json.Number)
		if !ok {
			return nil, bad
		}
		x, err := n.Float64()
		if err != nil || math.IsNaN(x) || math.IsInf(x, 0) || x < f.Min || x > f.Max {
			return nil, bad
		}
		scale := math.Pow10(f.Decimals)
		x = math.Round(x*scale) / scale
		if x < f.Min || x > f.Max {
			return nil, bad
		}
		clean = x
	case KindChoice:
		s, ok := v.(string)
		if !ok || !slices.Contains(f.Options, s) {
			return nil, bad
		}
		clean = s
	case KindMultiChoice:
		a, ok := v.([]any)
		if !ok || len(a) == 0 || len(a) > f.MaxSelected {
			return nil, bad
		}
		chosen := map[string]bool{}
		for _, item := range a {
			s, ok := item.(string)
			if !ok || !slices.Contains(f.Options, s) || chosen[s] {
				return nil, bad
			}
			chosen[s] = true
		}
		list := make([]string, 0, len(a))
		for _, o := range f.Options {
			if chosen[o] {
				list = append(list, o)
			}
		}
		clean = list
	case KindBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, bad
		}
		clean = b
	case KindDate:
		s, ok := v.(string)
		if !ok {
			return nil, bad
		}
		t, err := time.Parse(time.DateOnly, s)
		if err != nil || t.Format(time.DateOnly) != s {
			return nil, bad
		}
		clean = s
	default:
		return nil, bad
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return nil, bad
	}
	return b, nil
}

// validValueText refuses invalid UTF-8 and control characters; a long text
// may contain line breaks and tabs.
func validValueText(s string, multiline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
