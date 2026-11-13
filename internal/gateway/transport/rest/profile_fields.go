package rest

import (
	"context"
	"net/http"

	identityv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/identity/v1"
)

// The profile completion form (issue #186). identity checks every rule;
// gateway only maps names and bounds the input.

var (
	profileInterests = newEnum(map[string]identityv1.ProfileInterest{
		"convoy": identityv1.ProfileInterest_PROFILE_INTEREST_CONVOY, "camp": identityv1.ProfileInterest_PROFILE_INTEREST_CAMP,
		"hike": identityv1.ProfileInterest_PROFILE_INTEREST_HIKE, "fishing": identityv1.ProfileInterest_PROFILE_INTEREST_FISHING,
		"gaming": identityv1.ProfileInterest_PROFILE_INTEREST_GAMING, "sports": identityv1.ProfileInterest_PROFILE_INTEREST_SPORTS,
		"general": identityv1.ProfileInterest_PROFILE_INTEREST_GENERAL,
	})
	socialPlatforms = newEnum(map[string]identityv1.SocialPlatform{
		"instagram": identityv1.SocialPlatform_SOCIAL_PLATFORM_INSTAGRAM, "x": identityv1.SocialPlatform_SOCIAL_PLATFORM_X,
		"tiktok": identityv1.SocialPlatform_SOCIAL_PLATFORM_TIKTOK, "youtube": identityv1.SocialPlatform_SOCIAL_PLATFORM_YOUTUBE,
	})
	profileFieldNames = newEnum(map[string]identityv1.ProfileField{
		"interests": identityv1.ProfileField_PROFILE_FIELD_INTERESTS, "about": identityv1.ProfileField_PROFILE_FIELD_ABOUT,
		"occupation": identityv1.ProfileField_PROFILE_FIELD_OCCUPATION, "team": identityv1.ProfileField_PROFILE_FIELD_TEAM,
		"languages": identityv1.ProfileField_PROFILE_FIELD_LANGUAGES, "social_accounts": identityv1.ProfileField_PROFILE_FIELD_SOCIAL_ACCOUNTS,
	})
	aboutVisibilities = newEnum(map[string]identityv1.AboutVisibility{
		"friends_and_mutual": identityv1.AboutVisibility_ABOUT_VISIBILITY_FRIENDS_AND_MUTUAL,
		"friends":            identityv1.AboutVisibility_ABOUT_VISIBILITY_FRIENDS,
		"none":               identityv1.AboutVisibility_ABOUT_VISIBILITY_NONE,
	})
)

type socialAccountJSON struct {
	Platform string `json:"platform"`
	Username string `json:"username"`
}

func socialFrom(in []*identityv1.SocialAccount) []socialAccountJSON {
	out := make([]socialAccountJSON, 0, len(in))
	for _, a := range in {
		out = append(out, socialAccountJSON{Platform: socialPlatforms.name(a.GetPlatform()), Username: a.GetUsername()})
	}
	return out
}

func stringsOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// aboutJSON is the shown fields of another user's profile; empty ones are
// absent.
type aboutJSON struct {
	Interests      []string            `json:"interests,omitempty"`
	About          string              `json:"about,omitempty"`
	Occupation     string              `json:"occupation,omitempty"`
	Team           string              `json:"team,omitempty"`
	Languages      []string            `json:"languages,omitempty"`
	SocialAccounts []socialAccountJSON `json:"social_accounts,omitempty"`
}

func aboutFrom(p *identityv1.ProfileFields) aboutJSON {
	out := aboutJSON{
		Interests: profileInterests.names(p.GetInterests()), About: p.GetAbout(), Occupation: p.GetOccupation(),
		Team: p.GetTeam(), Languages: p.GetLanguages(),
	}
	if len(p.GetSocialAccounts()) > 0 {
		out.SocialAccounts = socialFrom(p.GetSocialAccounts())
	}
	return out
}

// accountProfileJSON is the caller's own fields in Account; lists are
// always present.
type accountProfileJSON struct {
	Interests                []string            `json:"interests"`
	About                    string              `json:"about,omitempty"`
	Occupation               string              `json:"occupation,omitempty"`
	Team                     string              `json:"team,omitempty"`
	Languages                []string            `json:"languages"`
	SocialAccounts           []socialAccountJSON `json:"social_accounts"`
	DeclinedProfileFields    []string            `json:"declined_profile_fields"`
	AboutVisibility          string              `json:"about_visibility"`
	ProfileCompletionPending bool                `json:"profile_completion_pending"`
}

func accountProfileFrom(a *identityv1.GetAccountResponse) accountProfileJSON {
	p := a.GetProfile()
	return accountProfileJSON{
		Interests: profileInterests.names(p.GetInterests()), About: p.GetAbout(), Occupation: p.GetOccupation(),
		Team: p.GetTeam(), Languages: stringsOrEmpty(p.GetLanguages()), SocialAccounts: socialFrom(p.GetSocialAccounts()),
		DeclinedProfileFields:    profileFieldNames.names(a.GetDeclinedProfileFields()),
		AboutVisibility:          aboutVisibilities.name(a.GetAboutVisibility()),
		ProfileCompletionPending: a.GetProfileCompletionPending(),
	}
}

// profilePatch is the profile part of AccountUpdate.
type profilePatch struct {
	Interests             *[]string            `json:"interests"`
	About                 *string              `json:"about"`
	Occupation            *string              `json:"occupation"`
	Team                  *string              `json:"team"`
	Languages             *[]string            `json:"languages"`
	SocialAccounts        *[]socialAccountJSON `json:"social_accounts"`
	DeclinedProfileFields *[]string            `json:"declined_profile_fields"`
	AboutVisibility       *string              `json:"about_visibility"`
}

func (p profilePatch) empty() bool {
	return p.Interests == nil && p.About == nil && p.Occupation == nil && p.Team == nil && p.Languages == nil &&
		p.SocialAccounts == nil && p.DeclinedProfileFields == nil && p.AboutVisibility == nil
}

// maxProfileTextBytes and maxProfileList bound the input before identity.
const (
	maxProfileTextBytes = 2048
	maxProfileList      = 16
)

// apply fills req; false when a name is unknown or the input too large.
func (p profilePatch) apply(req *identityv1.UpdateAccountRequest) bool {
	for _, s := range []*string{p.About, p.Occupation, p.Team} {
		if s != nil && len(*s) > maxProfileTextBytes {
			return false
		}
	}
	u := &identityv1.ProfileUpdate{About: p.About, Occupation: p.Occupation, Team: p.Team}
	if p.Interests != nil {
		v, ok := profileInterests.values(*p.Interests)
		if !ok {
			return false
		}
		u.Interests = &identityv1.InterestList{Values: v}
	}
	if p.Languages != nil {
		if len(*p.Languages) > maxProfileList {
			return false
		}
		u.Languages = &identityv1.LanguageList{Values: *p.Languages}
	}
	if p.SocialAccounts != nil {
		if len(*p.SocialAccounts) > maxProfileList {
			return false
		}
		list := &identityv1.SocialAccountList{}
		for _, a := range *p.SocialAccounts {
			pl, ok := socialPlatforms.value(a.Platform)
			if !ok || len(a.Username) > maxProfileTextBytes {
				return false
			}
			list.Values = append(list.Values, &identityv1.SocialAccount{Platform: pl, Username: a.Username})
		}
		u.SocialAccounts = list
	}
	if p.DeclinedProfileFields != nil {
		v, ok := profileFieldNames.values(*p.DeclinedProfileFields)
		if !ok {
			return false
		}
		u.Declined = &identityv1.ProfileFieldList{Values: v}
	}
	if p.AboutVisibility != nil {
		v, ok := aboutVisibilities.value(*p.AboutVisibility)
		if !ok {
			return false
		}
		req.AboutVisibility = v
	}
	if u.Interests != nil || u.About != nil || u.Occupation != nil || u.Team != nil || u.Languages != nil ||
		u.SocialAccounts != nil || u.Declined != nil {
		req.Profile = u
	}
	return true
}

func (h coreHandlers) snoozeProfilePrompt(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if _, err := h.d.Accounts.SnoozeProfilePrompt(ctx, &identityv1.SnoozeProfilePromptRequest{}); err != nil {
		h.identityFail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
