// Package link checks and normalizes a material's link (issue #261;
// ADR-0040 §3). It only parses text: it never resolves a name, dials,
// fetches or follows anything, and imports nothing that could (TestNoNetwork
// keeps it so). The server never opens a link — no preview, title or
// favicon — so it is neither a request tool for an attacker (SSRF) nor a
// tracker of who looks at what.
//
// A link is accepted when it is:
//
//   - https, with a host and no user info ("https://user@host" is a classic
//     disguise);
//   - a host name, never an IP address (IPv4 in any of its spellings, IPv6
//     in brackets), of LDH labels with a top-level label that starts with a
//     letter; non-ASCII names must come in punycode (xn--…), which keeps
//     look-alike letters out;
//   - not a special-use or private name (localhost, .local, .internal, …);
//   - port 443 or none;
//   - free of white space, control, format (bidi, zero-width) characters and
//     backslashes;
//   - at most MaxLength characters once normalized.
//
// Normalized: scheme and host lower case, the default port and a trailing
// dot of the host dropped, user info gone, the path, query and fragment
// escaped as net/url writes them. The domain is the normalized host; the
// blocked domain list (blocked_domains) is checked against it and every
// domain above it by the caller.
package link

import (
	"net/netip"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLength bounds a normalized link, in characters (ADR-0040 §3).
const MaxLength = 500

const (
	// maxHost bounds a host name (RFC 1035).
	maxHost = 253
	// maxLabel bounds one label of it.
	maxLabel = 63
	// maxRawBytes bounds the text before parsing.
	maxRawBytes = MaxLength * 4
)

// special are top-level labels that never name a public site: special-use
// names (RFC 6761, RFC 7686, RFC 8375, RFC 9476) and the usual private ones.
var special = map[string]bool{
	"localhost": true, "local": true, "localdomain": true, "internal": true, "intranet": true, "lan": true,
	"home": true, "corp": true, "private": true, "test": true, "invalid": true, "example": true, "onion": true,
	"arpa": true, "alt": true,
}

// Link is a checked link.
type Link struct {
	// URL is the normalized link.
	URL string
	// Domain is its host: lower case ASCII, no trailing dot.
	Domain string
}

// Parse checks raw and returns it normalized; ok is false for anything
// that is not an acceptable link. Surrounding white space is trimmed.
func Parse(raw string) (Link, bool) {
	if len(raw) > maxRawBytes || !utf8.ValidString(raw) {
		return Link{}, false
	}
	s := strings.TrimSpace(raw)
	if s == "" || strings.IndexFunc(s, bad) >= 0 {
		return Link{}, false
	}
	u, err := url.Parse(s)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.User != nil || u.Host == "" {
		return Link{}, false
	}
	if strings.HasPrefix(u.Host, "[") {
		return Link{}, false // an IPv6 address
	}
	if p := u.Port(); p != "" && p != "443" {
		return Link{}, false
	}
	domain, ok := Domain(u.Hostname())
	if !ok {
		return Link{}, false
	}
	u.Scheme, u.Host, u.User = "https", domain, nil
	out := u.String()
	if utf8.RuneCountInString(out) > MaxLength || strings.IndexFunc(out, bad) >= 0 {
		return Link{}, false
	}
	return Link{URL: out, Domain: domain}, true
}

// Domain checks a host name and returns it lower case without a trailing
// dot. It is also the form of the blocked domain list.
func Domain(host string) (string, bool) {
	d := strings.ToLower(strings.TrimSuffix(host, "."))
	if d == "" || len(d) > maxHost {
		return "", false
	}
	if _, err := netip.ParseAddr(d); err == nil {
		return "", false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, l := range labels {
		if !ldh(l) {
			return "", false
		}
	}
	tld := labels[len(labels)-1]
	// A top-level label that starts with a digit could be read as a number
	// (an IPv4 address in decimal, octal or 0x hex form).
	if tld[0] < 'a' || tld[0] > 'z' || special[tld] {
		return "", false
	}
	return d, true
}

// ldh reports whether l is a host name label: letters, digits and hyphens,
// 1–63 long, no hyphen at either end.
func ldh(l string) bool {
	if l == "" || len(l) > maxLabel || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// bad reports a rune a link never carries: white space, control and format
// characters (bidi controls, zero-width marks) and the backslash, which
// browsers read as a slash.
func bad(r rune) bool {
	return r == '\\' || unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}
