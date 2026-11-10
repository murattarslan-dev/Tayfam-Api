// Package clientversion decides whether a client build is still supported
// (issue #114, ADR-0016). It holds no HTTP code: the gateway's transport
// layer reads the header and writes the answer.
package clientversion

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxVersionLen bounds a version string; real ones are far shorter.
const maxVersionLen = 64

// Version is a Semantic Versioning 2.0.0 version (https://semver.org).
// Build metadata is accepted but ignored when comparing, as the
// specification says.
type Version struct {
	major, minor, patch uint64
	pre                 []string
	raw                 string
}

// ErrInvalidVersion is returned for a string that is not a semantic version.
var ErrInvalidVersion = errors.New("geçersiz sürüm")

// Parse reads MAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]. A leading "v",
// missing parts, leading zeros in numeric identifiers and empty
// identifiers are rejected.
func Parse(s string) (Version, error) {
	if s == "" || len(s) > maxVersionLen {
		return Version{}, ErrInvalidVersion
	}
	rest, build, hasBuild := strings.Cut(s, "+")
	if hasBuild {
		for id := range strings.SplitSeq(build, ".") {
			if !isIdentifier(id) {
				return Version{}, ErrInvalidVersion
			}
		}
	}
	core, pre, hasPre := strings.Cut(rest, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, ErrInvalidVersion
	}
	var nums [3]uint64
	for i, p := range parts {
		n, ok := numeric(p)
		if !ok {
			return Version{}, ErrInvalidVersion
		}
		nums[i] = n
	}
	v := Version{major: nums[0], minor: nums[1], patch: nums[2], raw: s}
	if hasPre {
		for id := range strings.SplitSeq(pre, ".") {
			if !isIdentifier(id) {
				return Version{}, ErrInvalidVersion
			}
			// Numeric pre-release identifiers may exceed uint64; they are
			// compared by length and digits, so only leading zeros matter.
			if isDigits(id) && len(id) > 1 && id[0] == '0' {
				return Version{}, ErrInvalidVersion
			}
			v.pre = append(v.pre, id)
		}
	}
	return v, nil
}

// MustParse is Parse for constants in tests and defaults; it panics on an
// invalid version.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(fmt.Sprintf("clientversion: %q: %v", s, err))
	}
	return v
}

// String returns the version as it was given, build metadata included.
func (v Version) String() string { return v.raw }

// IsZero reports whether v is the zero Version (not set).
func (v Version) IsZero() bool { return v.raw == "" }

// Compare returns -1, 0 or +1 by semantic version precedence: numbers
// first, then a pre-release ranks below its release (1.0.0-rc.1 <
// 1.0.0); pre-release identifiers compare numerically when both are
// numbers, otherwise in ASCII order, and numbers rank below words. Build
// metadata never counts (1.0.0+a == 1.0.0+b).
func (v Version) Compare(w Version) int {
	for _, d := range [...][2]uint64{{v.major, w.major}, {v.minor, w.minor}, {v.patch, w.patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.pre) == 0 && len(w.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(w.pre) == 0:
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(w.pre); i++ {
		if c := compareIdentifier(v.pre[i], w.pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.pre) < len(w.pre):
		return -1
	case len(v.pre) > len(w.pre):
		return 1
	}
	return 0
}

func compareIdentifier(a, b string) int {
	an, bn := isDigits(a), isDigits(b)
	switch {
	case an && bn:
		// Same length without leading zeros means numeric order is
		// string order; no overflow for long numbers.
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

// numeric parses a numeric identifier without a leading zero.
func numeric(s string) (uint64, bool) {
	if !isDigits(s) || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isIdentifier: non-empty, [0-9A-Za-z-] only.
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' {
			return false
		}
	}
	return true
}
