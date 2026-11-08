package tayfa

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
)

// Length limits (veri modeli v14; the same CHECKs are in the database).
const (
	nameMin        = 3
	nameMax        = 50
	descriptionMax = 500
	// maxRuneBytes bounds the byte length before counting characters, so an
	// oversized request is refused without scanning all of it.
	maxRuneBytes = 4
)

// Interests are the event types a tayfa can list, in EventType order
// (tayfa_interests.event_type).
var Interests = []string{"convoy", "camp", "hike", "fishing", "gaming", "sports", "general"}

// Modules are the modules a tayfa can turn on or off, in Module order
// (tayfa_modules.module).
var Modules = []string{"announcements", "events", "qa", "polls"}

// Join policies (tayfas.join_policy).
const (
	JoinApproval = "approval"
	JoinOpen     = "open"
)

// Update mask paths (UpdateTayfaRequest.update_mask).
const (
	FieldName        = "name"
	FieldDescription = "description"
	FieldInterests   = "interests"
	FieldJoinPolicy  = "join_policy"
)

// cleanName trims surrounding spaces and returns the name and its
// normalized form (CLAUDE.md "Türkçe metin").
func cleanName(s string) (name, normalized string, err error) {
	if len(s) > (nameMax+64)*maxRuneBytes {
		return "", "", apperr.Validation(FieldName)
	}
	name = strings.TrimSpace(s)
	if !validText(name, false) {
		return "", "", apperr.Validation(FieldName)
	}
	normalized = strings.ToLowerSpecial(unicode.TurkishCase, name)
	for _, v := range []string{name, normalized} {
		if n := utf8.RuneCountInString(v); n < nameMin || n > nameMax {
			return "", "", apperr.Validation(FieldName)
		}
	}
	return name, normalized, nil
}

// cleanDescription trims surrounding spaces; an empty description is stored
// as NULL.
func cleanDescription(s string) (string, error) {
	if len(s) > (descriptionMax+64)*maxRuneBytes {
		return "", apperr.Validation(FieldDescription)
	}
	d := strings.TrimSpace(s)
	if !validText(d, true) || utf8.RuneCountInString(d) > descriptionMax {
		return "", apperr.Validation(FieldDescription)
	}
	return d, nil
}

// validText refuses invalid UTF-8 and control characters; multiline text
// may contain line breaks and tabs.
func validText(s string, multiline bool) bool {
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

// cleanInterests checks for known, distinct values and returns them in
// EventType order.
func cleanInterests(in []string) ([]string, error) {
	if len(in) > len(Interests) {
		return nil, apperr.Validation(FieldInterests)
	}
	seen := make(map[string]bool, len(in))
	for _, v := range in {
		if !slices.Contains(Interests, v) || seen[v] {
			return nil, apperr.Validation(FieldInterests)
		}
		seen[v] = true
	}
	out := make([]string, 0, len(in))
	for _, v := range Interests {
		if seen[v] {
			out = append(out, v)
		}
	}
	return out, nil
}

func validJoinPolicy(p string) bool { return p == JoinApproval || p == JoinOpen }
