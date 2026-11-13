// Package displayname holds the one rule for a user's display name, used at
// registration and when the user changes it later (issue #184; data model
// v15 users.display_name: 1–40 characters).
package displayname

import (
	"strings"
	"unicode/utf8"
)

// MaxRunes is the users_display_name_len limit.
const MaxRunes = 40

// Clean collapses runs of white space to one space and trims the ends. It
// returns false when the result is longer than MaxRunes or holds a control
// character. An empty result (blank input) is returned as is, with true:
// the caller decides whether empty is allowed.
func Clean(s string) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxRunes {
		return "", false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return "", false
		}
	}
	return s, true
}
