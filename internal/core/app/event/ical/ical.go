// Package ical writes a single event as an RFC 5545 iCalendar object (the
// .ics file of "Takvime ekleme", data model v14). It uses the standard
// library only and knows nothing about who may see what: the caller passes
// only what the requesting member may see.
//
// Times are written in UTC (the "Z" form), so no VTIMEZONE component is
// needed. Text values are escaped (backslash, semicolon, comma, line
// breaks) and other control characters are dropped, so a value can never
// start a new property. Lines longer than 75 octets are folded without
// splitting a UTF-8 sequence. Lines end with CRLF.
package ical

import (
	"strings"
	"time"
	"unicode/utf8"
)

// ContentType is the media type of the output.
const ContentType = "text/calendar; charset=utf-8"

const (
	prodID     = "-//Tayfam//Tayfam API//TR"
	timeLayout = "20060102T150405Z"
	// maxLine is the longest line in octets, without the CRLF.
	maxLine = 75
)

// Event is what goes into the VEVENT.
type Event struct {
	// UID is the event ID; it stays the same across exports, so calendars
	// update the entry instead of adding a second one.
	UID         string
	Start, End  time.Time
	Created     time.Time
	Modified    time.Time
	Summary     string
	Description string
	// Location is free text (place name, district, city, or a link).
	Location string
	// URL is optional; written as a URI value.
	URL       string
	Cancelled bool
}

// Write returns the iCalendar object for e, stamped at now.
func Write(e Event, now time.Time) []byte {
	var b strings.Builder
	line := func(name, value string) { b.WriteString(fold(name + ":" + value)) }
	text := func(name, value string) {
		if value != "" {
			line(name, Escape(value))
		}
	}
	line("BEGIN", "VCALENDAR")
	line("VERSION", "2.0")
	line("PRODID", prodID)
	line("CALSCALE", "GREGORIAN")
	line("METHOD", "PUBLISH")
	line("BEGIN", "VEVENT")
	line("UID", Escape(e.UID))
	line("DTSTAMP", utc(now))
	line("DTSTART", utc(e.Start))
	line("DTEND", utc(e.End))
	if !e.Created.IsZero() {
		line("CREATED", utc(e.Created))
	}
	if !e.Modified.IsZero() {
		line("LAST-MODIFIED", utc(e.Modified))
	}
	text("SUMMARY", e.Summary)
	text("DESCRIPTION", e.Description)
	text("LOCATION", e.Location)
	if u := uri(e.URL); u != "" {
		line("URL", u)
	}
	if e.Cancelled {
		line("STATUS", "CANCELLED")
	} else {
		line("STATUS", "CONFIRMED")
	}
	line("END", "VEVENT")
	line("END", "VCALENDAR")
	return []byte(b.String())
}

func utc(t time.Time) string { return t.UTC().Format(timeLayout) }

// Escape escapes a TEXT value (RFC 5545 3.3.11): backslash, semicolon and
// comma get a backslash, every line break (CRLF, LF or CR) becomes \n, and
// control characters other than tab are dropped. Invalid UTF-8 becomes
// U+FFFD.
func Escape(s string) string {
	s = strings.ToValidUTF8(s, "�")
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == ';':
			b.WriteString(`\;`)
		case r == ',':
			b.WriteString(`\,`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			// CRLF is one break; a lone CR is one too.
			if i+1 < len(s) && s[i+1] == '\n' {
				continue
			}
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			// Dropped: control characters cannot appear in TEXT.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// uri keeps a URI value only if it has no characters that could break the
// line (the app layer only stores https URLs without spaces or controls).
func uri(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c == 0x7f {
			return ""
		}
	}
	return s
}

// fold splits a content line into lines of at most 75 octets: every
// continuation line starts with one space, which counts towards its 75.
// Splits fall on UTF-8 boundaries. The result ends with CRLF.
func fold(s string) string {
	if len(s) <= maxLine {
		return s + "\r\n"
	}
	var b strings.Builder
	limit := maxLine
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		limit = maxLine - 1
	}
	b.WriteString(s)
	b.WriteString("\r\n")
	return b.String()
}
