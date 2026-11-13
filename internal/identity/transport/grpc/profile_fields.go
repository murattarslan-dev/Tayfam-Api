package grpc

import (
	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/profilefields"
)

// Profile completion form (issue #186): proto enums ↔ the stored names.

var interestNames = map[identityv1.ProfileInterest]string{
	identityv1.ProfileInterest_PROFILE_INTEREST_CONVOY:  "convoy",
	identityv1.ProfileInterest_PROFILE_INTEREST_CAMP:    "camp",
	identityv1.ProfileInterest_PROFILE_INTEREST_HIKE:    "hike",
	identityv1.ProfileInterest_PROFILE_INTEREST_FISHING: "fishing",
	identityv1.ProfileInterest_PROFILE_INTEREST_GAMING:  "gaming",
	identityv1.ProfileInterest_PROFILE_INTEREST_SPORTS:  "sports",
	identityv1.ProfileInterest_PROFILE_INTEREST_GENERAL: "general",
}

var platformNamesPB = map[identityv1.SocialPlatform]string{
	identityv1.SocialPlatform_SOCIAL_PLATFORM_INSTAGRAM: "instagram",
	identityv1.SocialPlatform_SOCIAL_PLATFORM_X:         "x",
	identityv1.SocialPlatform_SOCIAL_PLATFORM_TIKTOK:    "tiktok",
	identityv1.SocialPlatform_SOCIAL_PLATFORM_YOUTUBE:   "youtube",
}

var fieldNames = map[identityv1.ProfileField]string{
	identityv1.ProfileField_PROFILE_FIELD_INTERESTS:       profilefields.FieldInterests,
	identityv1.ProfileField_PROFILE_FIELD_ABOUT:           profilefields.FieldAbout,
	identityv1.ProfileField_PROFILE_FIELD_OCCUPATION:      profilefields.FieldOccupation,
	identityv1.ProfileField_PROFILE_FIELD_TEAM:            profilefields.FieldTeam,
	identityv1.ProfileField_PROFILE_FIELD_LANGUAGES:       profilefields.FieldLanguages,
	identityv1.ProfileField_PROFILE_FIELD_SOCIAL_ACCOUNTS: profilefields.FieldSocialAccounts,
}

var aboutVisibilityNames = map[identityv1.AboutVisibility]profilefields.AboutVisibility{
	identityv1.AboutVisibility_ABOUT_VISIBILITY_FRIENDS_AND_MUTUAL: profilefields.VisibleFriendsAndMutual,
	identityv1.AboutVisibility_ABOUT_VISIBILITY_FRIENDS:            profilefields.VisibleFriends,
	identityv1.AboutVisibility_ABOUT_VISIBILITY_NONE:               profilefields.VisibleNone,
}

func invert[K, V comparable](m map[K]V) map[V]K {
	out := make(map[V]K, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

var (
	interestValues        = invert(interestNames)
	platformValues        = invert(platformNamesPB)
	fieldValues           = invert(fieldNames)
	aboutVisibilityValues = invert(aboutVisibilityNames)
)

// mapAll converts every value, false when one is unknown.
func mapAll[K comparable, V any](in []K, m map[K]V) ([]V, bool) {
	out := make([]V, 0, len(in))
	for _, k := range in {
		v, ok := m[k]
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

func profileFieldsToPB(interests []string, about, occupation, team string, languages []string, social []profilefields.SocialAccount) *identityv1.ProfileFields {
	out := &identityv1.ProfileFields{About: about, Occupation: occupation, Team: team, Languages: languages}
	out.Interests, _ = mapAll(interests, interestValues)
	for _, a := range social {
		out.SocialAccounts = append(out.SocialAccounts, &identityv1.SocialAccount{Platform: platformValues[a.Platform], Username: a.Username})
	}
	return out
}

// profileUpdateFromPB converts an update; false when it names an unknown
// value. Bounds of the free text are the app layer's.
func profileUpdateFromPB(p *identityv1.ProfileUpdate) (profilefields.Update, bool) {
	var u profilefields.Update
	if p == nil {
		return u, true
	}
	if p.Interests != nil {
		v, ok := mapAll(p.GetInterests().GetValues(), interestNames)
		if !ok {
			return u, false
		}
		u.Interests = &v
	}
	if p.Languages != nil {
		v := append([]string{}, p.GetLanguages().GetValues()...)
		u.Languages = &v
	}
	if p.SocialAccounts != nil {
		v := []profilefields.SocialAccount{}
		for _, a := range p.GetSocialAccounts().GetValues() {
			name, ok := platformNamesPB[a.GetPlatform()]
			if !ok {
				return u, false
			}
			v = append(v, profilefields.SocialAccount{Platform: name, Username: a.GetUsername()})
		}
		u.SocialAccounts = &v
	}
	if p.Declined != nil {
		v, ok := mapAll(p.GetDeclined().GetValues(), fieldNames)
		if !ok {
			return u, false
		}
		u.Declined = &v
	}
	u.About, u.Occupation, u.Team = p.About, p.Occupation, p.Team
	return u, true
}

// maxProfileText bounds each free text before any work; the lists are
// bounded to 32 values.
const maxProfileText = 4096

func profileUpdateTooLarge(p *identityv1.ProfileUpdate) bool {
	if p == nil {
		return false
	}
	return len(p.GetAbout()) > maxProfileText || len(p.GetOccupation()) > maxProfileText || len(p.GetTeam()) > maxProfileText ||
		len(p.GetInterests().GetValues()) > 32 || len(p.GetLanguages().GetValues()) > 32 ||
		len(p.GetSocialAccounts().GetValues()) > 32 || len(p.GetDeclined().GetValues()) > 32
}
