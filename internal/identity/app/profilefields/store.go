package profilefields

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Stored is a user's stored fields, visibility and snooze. A user without
// a profile_details row has no values, the default visibility and no
// snooze.
type Stored struct {
	Values     Values
	Visibility AboutVisibility
	Snooze     Snooze
}

// Load reads user's fields.
func Load(ctx context.Context, q *store.Queries, user id.ID) (Stored, error) {
	out := Stored{Visibility: VisibleFriendsAndMutual}
	row, err := q.GetOwnProfileDetails(ctx, user.PG())
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Stored{}, fmt.Errorf("profil alanları: %w", err)
	default:
		out.Values = Values{
			Interests: row.Interests, About: row.About.String, Occupation: row.Occupation.String,
			Team: row.Team.String, Languages: row.Languages, Declined: row.DeclinedFields,
		}
		out.Visibility = AboutVisibility(row.AboutVisibility)
		if row.PromptSnoozedUntil.Valid {
			out.Snooze = Snooze{Until: row.PromptSnoozedUntil.Time.UTC(), FieldsVersion: int(row.PromptFieldsVersion)}
		}
	}
	social, err := q.ListSocialAccounts(ctx, user.PG())
	if err != nil {
		return Stored{}, fmt.Errorf("sosyal medya hesapları: %w", err)
	}
	for _, s := range social {
		out.Values.SocialAccounts = append(out.Values.SocialAccounts, SocialAccount{Platform: s.Platform, Username: s.Username})
	}
	return out, nil
}

// Stored values of catalog_interests.status (identity's copy of core's
// catalog).
const (
	interestActive  = "active"
	interestRetired = "retired"
)

// CheckInterests checks next, the interests about to be stored, against
// identity's copy of the catalog (ADR-0025 §10, ADR-0009 copy model): every
// key is an interest the copy knows, and a retired interest can only be
// kept, not chosen: it passes only when the user already has it (cur). A
// key that fails is ErrInvalid.
func CheckInterests(ctx context.Context, q *store.Queries, cur, next []string) error {
	if len(next) == 0 {
		return nil
	}
	rows, err := q.ListCatalogInterests(ctx)
	if err != nil {
		return fmt.Errorf("katalog kopyası: %w", err)
	}
	status := make(map[string]string, len(rows))
	for _, r := range rows {
		status[r.Key] = r.Status
	}
	for _, k := range next {
		switch status[k] {
		case interestActive:
		case interestRetired:
			if !slices.Contains(cur, k) {
				return fmt.Errorf("%w: emekli ilgi alanı", ErrInvalid)
			}
		default:
			return fmt.Errorf("%w: bilinmeyen ilgi alanı", ErrInvalid)
		}
	}
	return nil
}

// Save writes user's fields and visibility; the snooze stays. The caller
// checks changed interests with CheckInterests first.
func Save(ctx context.Context, q *store.Queries, user id.ID, v Values, vis AboutVisibility) error {
	text := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
	if err := q.UpsertProfileDetails(ctx, store.UpsertProfileDetailsParams{
		UserID: user.PG(), Interests: nonNil(v.Interests), About: text(v.About), Occupation: text(v.Occupation),
		Team: text(v.Team), Languages: nonNil(v.Languages), DeclinedFields: nonNil(v.Declined), AboutVisibility: string(vis),
	}); err != nil {
		return fmt.Errorf("profil alanları: %w", err)
	}
	if err := q.DeleteSocialAccounts(ctx, user.PG()); err != nil {
		return fmt.Errorf("sosyal medya hesapları: %w", err)
	}
	for _, a := range v.SocialAccounts {
		if err := q.AddSocialAccount(ctx, store.AddSocialAccountParams{UserID: user.PG(), Platform: a.Platform, Username: a.Username}); err != nil {
			return fmt.Errorf("sosyal medya hesabı: %w", err)
		}
	}
	return nil
}

// SaveSnooze records "Daha sonra doldur" at now: SnoozeFor under the
// current FieldsVersion.
func SaveSnooze(ctx context.Context, q *store.Queries, user id.ID, now time.Time) error {
	if err := q.SnoozeProfilePrompt(ctx, store.SnoozeProfilePromptParams{
		UserID: user.PG(), Until: pgtype.Timestamptz{Time: now.Add(SnoozeFor), Valid: true}, FieldsVersion: FieldsVersion,
	}); err != nil {
		return fmt.Errorf("erteleme: %w", err)
	}
	return nil
}

// Delete removes every field of user (account deletion).
func Delete(ctx context.Context, q *store.Queries, user id.ID) error {
	if err := q.DeleteSocialAccounts(ctx, user.PG()); err != nil {
		return fmt.Errorf("sosyal medya hesapları: %w", err)
	}
	if err := q.DeleteUserProfileDetails(ctx, user.PG()); err != nil {
		return fmt.Errorf("profil alanları: %w", err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
