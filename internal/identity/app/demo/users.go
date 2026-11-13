package demo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/refcode"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Provider is the auth_identities.provider of demo accounts (migration
// 00011). No sign-in path accepts it: session.SignIn only has the google
// and apple verifiers.
const Provider = "demo"

// ErrOwnerNotFound: the referral code finds no registered, live account.
var ErrOwnerNotFound = errors.New("referans koduyla kayıtlı bir hesap bulunamadı")

// Email returns the marker e-mail of a demo account (ADR-0023).
func Email(subject string) string { return subject + "@" + EmailDomain }

// ensureUser returns the demo account for p, creating it the first time
// the same way registration does: a registered user with an encrypted
// birth date, a referral code and a user.profile_updated event, so core and
// chat get their profile copies through the outbox (ADR-0009). There are no
// consent records: nobody accepted the texts. created is false when the
// account already existed; it is then left as it is.
func (s *Seeder) ensureUser(ctx context.Context, p person) (user id.ID, created bool, err error) {
	date, err := birthdate.Parse(p.BirthDate)
	if err != nil {
		return id.ID{}, false, fmt.Errorf("demo doğum tarihi %s: %w", p.Key, err)
	}
	if date.IsMinor(s.cfg.Now()) {
		return id.ID{}, false, fmt.Errorf("demo hesabı %s yetişkin değil", p.Key)
	}
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		existing, err := q.GetAuthIdentity(ctx, store.GetAuthIdentityParams{Provider: Provider, Subject: p.Subject})
		switch {
		case err == nil:
			user, _ = id.FromPG(existing.UserID)
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("demo kimliği: %w", err)
		}
		user, created = id.New(), true
		if err := q.CreateUser(ctx, store.CreateUserParams{ID: user.PG(), DisplayName: pgtype.Text{String: p.Name, Valid: true}, Locale: "tr"}); err != nil {
			return fmt.Errorf("demo kullanıcı: %w", err)
		}
		if err := q.CreateAuthIdentity(ctx, store.CreateAuthIdentityParams{
			ID: id.New().PG(), UserID: user.PG(), Provider: Provider, Subject: p.Subject,
			Email: pgtype.Text{String: Email(p.Subject), Valid: true},
		}); err != nil {
			return fmt.Errorf("demo kimliği: %w", err)
		}
		n, err := q.CompleteRegistration(ctx, store.CompleteRegistrationParams{ID: user.PG(), BirthDateEnc: s.cfg.Cipher.Encrypt(user, date)})
		if err != nil {
			return fmt.Errorf("demo kayıt: %w", err)
		}
		if n != 1 {
			return errors.New("demo kayıt tamamlanamadı")
		}
		if _, err := refcode.Assign(ctx, tx, user, false); err != nil {
			return err
		}
		return userevents.ProfileUpdated(ctx, tx, user, userevents.Profile{DisplayName: p.Name, ActivityVisibility: "tayfa"})
	})
	if err != nil {
		return id.ID{}, false, err
	}
	return user, created, nil
}

// findOwner returns the account behind the platform owner's referral code.
// The code itself is never logged or returned.
func (s *Seeder) findOwner(ctx context.Context, code string) (id.ID, error) {
	code, ok := refcode.Normalize(code)
	if !ok {
		return id.ID{}, ErrOwnerNotFound
	}
	q := store.New(s.cfg.Pool)
	u, err := q.FindUserByRefCode(ctx, pgtype.Text{String: code, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return id.ID{}, ErrOwnerNotFound
	}
	if err != nil {
		return id.ID{}, fmt.Errorf("referans kodu: %w", err)
	}
	owner, _ := id.FromPG(u.ID)
	// A demo account is not the platform owner.
	for _, p := range people {
		ident, err := q.GetAuthIdentity(ctx, store.GetAuthIdentityParams{Provider: Provider, Subject: p.Subject})
		if err == nil && ident.UserID == u.ID {
			return id.ID{}, ErrOwnerNotFound
		}
	}
	return owner, nil
}
