// Package profilefields holds the profile completion form's fields, their
// rules and when the form is due (issue #186; Notion Proje Planı §4 item 8,
// platform owner 2026-11-13).
//
// Every field is optional. A field is answered when it has a value or the
// user declined it ("Paylaşmak istemiyorum"). The values never leave
// identity but through its ProfileService, by the user's about visibility;
// they are never logged.
package profilefields

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// FieldsVersion is the version of the field list. Raise it when a field is
// added: a snooze made under an older version no longer holds back the
// form.
const FieldsVersion = 1

// SnoozeFor is how long "Daha sonra doldur" holds back the form.
const SnoozeFor = 7 * 24 * time.Hour

// Field names; the values of profile_details.declined_fields.
const (
	FieldInterests      = "interests"
	FieldAbout          = "about"
	FieldOccupation     = "occupation"
	FieldTeam           = "team"
	FieldLanguages      = "languages"
	FieldSocialAccounts = "social_accounts"
)

// AllFields is every field of FieldsVersion, in form order.
var AllFields = []string{FieldInterests, FieldAbout, FieldOccupation, FieldTeam, FieldLanguages, FieldSocialAccounts}

// Interests are the event types (EventType in the REST contract).
var Interests = []string{"convoy", "camp", "hike", "fishing", "gaming", "sports", "general"}

// Limits (profile_details CHECKs).
const (
	MaxAbout     = 160
	MaxShortText = 60 // occupation, team
	MaxLanguages = 5
)

// AboutVisibility is who sees the fields.
type AboutVisibility string

// AboutVisibility values. Social accounts are shown to friends at most,
// whatever the setting.
const (
	VisibleFriendsAndMutual AboutVisibility = "friends_and_mutual" // the default
	VisibleFriends          AboutVisibility = "friends"
	VisibleNone             AboutVisibility = "none"
)

// Valid reports whether v is one of the values.
func (v AboutVisibility) Valid() bool {
	return v == VisibleFriendsAndMutual || v == VisibleFriends || v == VisibleNone
}

// SocialAccount is a user name on one of the known platforms; the client
// builds the link from the platform's own address.
type SocialAccount struct {
	Platform string
	Username string
}

// usernameRules are each platform's user name characters. No rule admits
// ":" or "/", so no URL passes.
var usernameRules = map[string]*regexp.Regexp{
	"instagram": regexp.MustCompile(`^[A-Za-z0-9._]{1,30}$`),
	"x":         regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`),
	"tiktok":    regexp.MustCompile(`^[A-Za-z0-9._]{2,24}$`),
	"youtube":   regexp.MustCompile(`^[A-Za-z0-9._-]{3,30}$`),
}

// Platforms is the closed list of social platforms.
var Platforms = []string{"instagram", "x", "tiktok", "youtube"}

// Values is the user's own view of the fields.
type Values struct {
	Interests      []string
	About          string
	Occupation     string
	Team           string
	Languages      []string
	SocialAccounts []SocialAccount
	Declined       []string
}

// Answered reports whether field has a value or was declined.
func (v Values) Answered(field string) bool {
	if slices.Contains(v.Declined, field) {
		return true
	}
	switch field {
	case FieldInterests:
		return len(v.Interests) > 0
	case FieldAbout:
		return v.About != ""
	case FieldOccupation:
		return v.Occupation != ""
	case FieldTeam:
		return v.Team != ""
	case FieldLanguages:
		return len(v.Languages) > 0
	case FieldSocialAccounts:
		return len(v.SocialAccounts) > 0
	}
	return false
}

// Complete reports whether every field is answered.
func (v Values) Complete() bool {
	for _, f := range AllFields {
		if !v.Answered(f) {
			return false
		}
	}
	return true
}

// Snooze is the stored "Daha sonra doldur".
type Snooze struct {
	Until         time.Time // zero: never snoozed
	FieldsVersion int
}

// Pending reports whether the form is due at now: a field is unanswered
// and there is no snooze in force. A snooze made under an older field list
// is not in force.
func Pending(v Values, s Snooze, now time.Time) bool {
	if v.Complete() {
		return false
	}
	if s.FieldsVersion < FieldsVersion {
		return true
	}
	return !now.Before(s.Until)
}

// ErrInvalid is returned for a value that breaks a rule. Which field and
// why is not told: the contract documents the rules.
var ErrInvalid = errors.New("geçersiz profil alanı")

// Update is a partial change of the fields; nil leaves a field as it is.
// An empty value clears the field (unanswered unless declined).
type Update struct {
	Interests      *[]string
	About          *string
	Occupation     *string
	Team           *string
	Languages      *[]string
	SocialAccounts *[]SocialAccount
	// Declined, when set, replaces the declined set. Declining a field
	// clears its value.
	Declined *[]string
}

// Empty reports whether u changes nothing.
func (u Update) Empty() bool {
	return u.Interests == nil && u.About == nil && u.Occupation == nil && u.Team == nil &&
		u.Languages == nil && u.SocialAccounts == nil && u.Declined == nil
}

// Apply returns cur with u applied, cleaned and checked. Giving a value to
// a field in the same update that declines it is ErrInvalid; giving a value
// to a field declined earlier takes it off the declined set.
func Apply(cur Values, u Update) (Values, error) {
	next := cur
	if u.Declined != nil {
		d, err := cleanDeclined(*u.Declined)
		if err != nil {
			return Values{}, err
		}
		next.Declined = d
	}
	set := map[string]bool{}
	var err error
	if u.Interests != nil {
		if next.Interests, err = cleanInterests(*u.Interests); err != nil {
			return Values{}, err
		}
		set[FieldInterests] = len(next.Interests) > 0
	}
	if u.About != nil {
		if next.About, err = cleanText(*u.About, MaxAbout, true); err != nil {
			return Values{}, err
		}
		set[FieldAbout] = next.About != ""
	}
	if u.Occupation != nil {
		if next.Occupation, err = cleanText(*u.Occupation, MaxShortText, false); err != nil {
			return Values{}, err
		}
		set[FieldOccupation] = next.Occupation != ""
	}
	if u.Team != nil {
		if next.Team, err = cleanText(*u.Team, MaxShortText, false); err != nil {
			return Values{}, err
		}
		set[FieldTeam] = next.Team != ""
	}
	if u.Languages != nil {
		if next.Languages, err = cleanLanguages(*u.Languages); err != nil {
			return Values{}, err
		}
		set[FieldLanguages] = len(next.Languages) > 0
	}
	if u.SocialAccounts != nil {
		if next.SocialAccounts, err = cleanSocial(*u.SocialAccounts); err != nil {
			return Values{}, err
		}
		set[FieldSocialAccounts] = len(next.SocialAccounts) > 0
	}
	declined := make([]string, 0, len(next.Declined))
	for _, f := range next.Declined {
		switch {
		case set[f] && u.Declined != nil:
			return Values{}, fmt.Errorf("%w: hem değer hem ret", ErrInvalid)
		case set[f]:
			// A value given now answers the field: no longer declined.
		default:
			declined = append(declined, f)
		}
	}
	next.Declined = declined
	for _, f := range next.Declined {
		switch f {
		case FieldInterests:
			next.Interests = nil
		case FieldAbout:
			next.About = ""
		case FieldOccupation:
			next.Occupation = ""
		case FieldTeam:
			next.Team = ""
		case FieldLanguages:
			next.Languages = nil
		case FieldSocialAccounts:
			next.SocialAccounts = nil
		}
	}
	return next, nil
}

func cleanDeclined(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, f := range in {
		if !slices.Contains(AllFields, f) || slices.Contains(out, f) {
			return nil, fmt.Errorf("%w: bilinmeyen ya da tekrar eden alan", ErrInvalid)
		}
		out = append(out, f)
	}
	return sortedLike(out, AllFields), nil
}

func cleanInterests(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !slices.Contains(Interests, v) || slices.Contains(out, v) {
			return nil, fmt.Errorf("%w: ilgi alanı", ErrInvalid)
		}
		out = append(out, v)
	}
	return sortedLike(out, Interests), nil
}

// cleanText trims the ends; a single-line field also collapses runs of
// white space. Control characters are refused, line breaks only in
// multi-line fields. Empty clears.
func cleanText(s string, limit int, multiline bool) (string, error) {
	if multiline {
		lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
		for i, l := range lines {
			lines[i] = strings.Join(strings.Fields(l), " ")
		}
		s = strings.TrimSpace(strings.Join(lines, "\n"))
	} else {
		s = strings.Join(strings.Fields(s), " ")
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > limit {
		return "", fmt.Errorf("%w: uzunluk", ErrInvalid)
	}
	for _, r := range s {
		if (r < 0x20 && r != '\n') || (r >= 0x7f && r < 0xa0) {
			return "", fmt.Errorf("%w: kontrol karakteri", ErrInvalid)
		}
	}
	return s, nil
}

func cleanLanguages(in []string) ([]string, error) {
	if len(in) > MaxLanguages {
		return nil, fmt.Errorf("%w: dil sayısı", ErrInvalid)
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.ToLower(strings.TrimSpace(v))
		if !isLanguage(v) || slices.Contains(out, v) {
			return nil, fmt.Errorf("%w: dil", ErrInvalid)
		}
		out = append(out, v)
	}
	return out, nil
}

func cleanSocial(in []SocialAccount) ([]SocialAccount, error) {
	out := make([]SocialAccount, 0, len(in))
	for _, a := range in {
		rule, ok := usernameRules[a.Platform]
		if !ok || slices.ContainsFunc(out, func(o SocialAccount) bool { return o.Platform == a.Platform }) {
			return nil, fmt.Errorf("%w: sosyal medya platformu", ErrInvalid)
		}
		name := strings.TrimPrefix(strings.TrimSpace(a.Username), "@")
		if !rule.MatchString(name) {
			return nil, fmt.Errorf("%w: sosyal medya kullanıcı adı", ErrInvalid)
		}
		out = append(out, SocialAccount{Platform: a.Platform, Username: name})
	}
	slices.SortFunc(out, func(a, b SocialAccount) int {
		return slices.Index(Platforms, a.Platform) - slices.Index(Platforms, b.Platform)
	})
	return out, nil
}

// sortedLike orders in as the values appear in order.
func sortedLike(in, order []string) []string {
	slices.SortFunc(in, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
	return in
}
