package clientversion

import (
	"errors"
	"fmt"
	"strings"
)

// Header carries the client's platform and build on every request
// (ADR-0016): "<platform>/<version>", for example "android/1.4.2".
const Header = "X-Tayfam-Client-Version"

// maxHeaderLen bounds the header before it is parsed.
const maxHeaderLen = 80

// Platform is a client platform.
type Platform string

// Client platforms; the same names as X-Tayfam-Client (ADR-0004).
const (
	Android Platform = "android"
	IOS     Platform = "ios"
	Web     Platform = "web"
)

// Platforms returns every platform in a fixed order.
func Platforms() []Platform { return []Platform{Android, IOS, Web} }

func knownPlatform(p Platform) bool {
	return p == Android || p == IOS || p == Web
}

// Range is one platform's configuration. A zero Min means no build is
// refused; Latest is only reported to clients.
type Range struct {
	Min    Version
	Latest Version
}

// RawRange is a platform's configuration as text, as read from the
// environment. Empty strings mean "not set".
type RawRange struct {
	Min    string
	Latest string
}

// Policy holds the supported range of each platform. The zero Policy
// refuses nothing.
type Policy struct {
	ranges map[Platform]Range
}

// NewPolicy validates raw. An unset Min turns enforcement off for that
// platform; an unset Latest takes Min. A value that does not parse, an
// unknown platform or Latest below Min is an error, so the service does
// not start with a configuration it would misread.
func NewPolicy(raw map[Platform]RawRange) (Policy, error) {
	p := Policy{ranges: map[Platform]Range{}}
	var errs []error
	for plat, rr := range raw {
		if !knownPlatform(plat) {
			errs = append(errs, fmt.Errorf("bilinmeyen platform %q", plat))
			continue
		}
		var r Range
		var err error
		if rr.Min != "" {
			if r.Min, err = Parse(rr.Min); err != nil {
				errs = append(errs, fmt.Errorf("%s en düşük sürüm %q: %w", plat, rr.Min, err))
				continue
			}
		}
		if rr.Latest != "" {
			if r.Latest, err = Parse(rr.Latest); err != nil {
				errs = append(errs, fmt.Errorf("%s son sürüm %q: %w", plat, rr.Latest, err))
				continue
			}
		} else {
			r.Latest = r.Min
		}
		if !r.Min.IsZero() && r.Latest.Compare(r.Min) < 0 {
			errs = append(errs, fmt.Errorf("%s son sürüm %s, en düşük sürüm %s'ten küçük", plat, r.Latest, r.Min))
			continue
		}
		p.ranges[plat] = r
	}
	if err := errors.Join(errs...); err != nil {
		return Policy{}, err
	}
	return p, nil
}

// Range returns plat's configuration; the zero Range when unset.
func (p Policy) Range(plat Platform) Range { return p.ranges[plat] }

// Decision is the outcome of Check.
type Decision int

// Check outcomes.
const (
	// Allowed: the build is supported, or its platform has no minimum.
	Allowed Decision = iota
	// Missing: no header. The request is allowed (ADR-0016): browsers'
	// WebSocket, curl and health checks cannot or do not send it.
	Missing
	// Invalid: a header that does not parse, an unknown platform or more
	// than one header.
	Invalid
	// Outdated: the build is below its platform's minimum.
	Outdated
)

// Check decides on the values of the X-Tayfam-Client-Version header.
func (p Policy) Check(values []string) Decision {
	switch len(values) {
	case 0:
		return Missing
	case 1:
	default:
		return Invalid
	}
	plat, v, err := ParseHeader(values[0])
	if err != nil {
		return Invalid
	}
	r := p.ranges[plat]
	if !r.Min.IsZero() && v.Compare(r.Min) < 0 {
		return Outdated
	}
	return Allowed
}

// ErrInvalidHeader is returned for a malformed X-Tayfam-Client-Version.
var ErrInvalidHeader = errors.New("geçersiz istemci sürümü başlığı")

// ParseHeader reads "<platform>/<version>". The platform is one of
// android, ios, web in lower case; the version is a semantic version.
func ParseHeader(h string) (Platform, Version, error) {
	if h == "" || len(h) > maxHeaderLen {
		return "", Version{}, ErrInvalidHeader
	}
	name, ver, ok := strings.Cut(h, "/")
	plat := Platform(name)
	if !ok || !knownPlatform(plat) {
		return "", Version{}, ErrInvalidHeader
	}
	v, err := Parse(ver)
	if err != nil {
		return "", Version{}, ErrInvalidHeader
	}
	return plat, v, nil
}
