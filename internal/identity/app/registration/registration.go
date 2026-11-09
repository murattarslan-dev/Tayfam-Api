// Package registration completes a new user's registration: birth date,
// consents and display name (ADR-0008, ADR-0012).
//
// Until registration is complete the user's access token only reaches
// identity's registration methods (aud=tayfam-registration). A user under
// MinimumAge is refused for good: the account is blocked without storing the
// birth date, its sessions end and the same sign-in account cannot try again
// with another date.
package registration

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

var (
	ErrInvalidBirthDate   = errors.New("geçersiz doğum tarihi")
	ErrUnderAge           = errors.New("yaş sınırının altında")
	ErrConsentOutdated    = errors.New("onaylanan belge sürümü güncel değil")
	ErrInvalidDisplayName = errors.New("geçersiz ad")
	ErrAlreadyRegistered  = errors.New("kayıt zaten tamamlanmış")
	ErrAgeBlocked         = errors.New("hesap yaş sınırı nedeniyle kapalı")
	ErrUnknownUser        = errors.New("kullanıcı bulunamadı")
)

// Documents are the current versions of the texts a user must accept, such
// as "2026-11-08". They come from configuration and change when a text is
// republished.
type Documents struct {
	PrivacyNotice string
	Terms         string
}

// Config wires a Service.
type Config struct {
	Pool      *pgxpool.Pool
	Log       *slog.Logger
	Cipher    *birthdate.Cipher
	Documents Documents
	Now       func() time.Time
}

// Service completes registrations.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Cipher == nil || cfg.Documents.PrivacyNotice == "" || cfg.Documents.Terms == "" {
		return nil, errors.New("registration: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// Requirements tells the client what registration needs.
type Requirements struct {
	Documents  Documents
	MinimumAge int
}

// Requirements returns the current document versions and age limit.
func (s *Service) Requirements() Requirements {
	return Requirements{Documents: s.cfg.Documents, MinimumAge: birthdate.MinimumAge}
}

// Input is what the user submits.
type Input struct {
	BirthDate string // YYYY-MM-DD
	// DisplayName replaces the provider's name when not empty.
	DisplayName string
	// Versions of the texts the user was shown and accepted.
	PrivacyNoticeVersion string
	TermsVersion         string
}

// Complete validates input and finishes registration. On ErrUnderAge the
// refusal has already been stored when it returns.
func (s *Service) Complete(ctx context.Context, user id.ID, in Input) error {
	date, err := birthdate.Parse(in.BirthDate)
	if err != nil {
		return ErrInvalidBirthDate
	}
	if in.PrivacyNoticeVersion != s.cfg.Documents.PrivacyNotice || in.TermsVersion != s.cfg.Documents.Terms {
		return ErrConsentOutdated
	}
	name, err := cleanDisplayName(in.DisplayName)
	if err != nil {
		return err
	}

	var underAge bool
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		u, err := q.GetUser(ctx, user.PG())
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrUnknownUser
		case err != nil:
			return fmt.Errorf("kullanıcı: %w", err)
		case u.AgeBlockedAt.Valid:
			return ErrAgeBlocked
		case u.RegisteredAt.Valid:
			return ErrAlreadyRegistered
		}

		switch err := date.Validate(s.cfg.Now()); {
		case errors.Is(err, birthdate.ErrUnderAge):
			underAge = true
			return s.block(ctx, q, user)
		case err != nil:
			return ErrInvalidBirthDate
		}

		n, err := q.CompleteRegistration(ctx, store.CompleteRegistrationParams{ID: user.PG(), BirthDateEnc: s.cfg.Cipher.Encrypt(user, date)})
		if err != nil {
			return fmt.Errorf("kayıt: %w", err)
		}
		if n != 1 {
			return ErrAlreadyRegistered
		}
		for _, doc := range []struct{ typ, version string }{
			{"privacy_notice", in.PrivacyNoticeVersion},
			{"terms", in.TermsVersion},
		} {
			if err := q.RecordConsent(ctx, store.RecordConsentParams{ID: id.New().PG(), UserID: user.PG(), DocumentType: doc.typ, DocumentVersion: doc.version}); err != nil {
				return fmt.Errorf("onay kaydı: %w", err)
			}
		}
		if name != "" {
			if err := q.SetDisplayName(ctx, store.SetDisplayNameParams{ID: user.PG(), DisplayName: pgtype.Text{String: name, Valid: true}}); err != nil {
				return fmt.Errorf("ad: %w", err)
			}
		}
		if err := assignRefCode(ctx, tx, q, user); err != nil {
			return err
		}
		// The user is now visible to other services: core and chat get the
		// profile copy (ADR-0009). Written last, see outbox.Write.
		profile := userevents.Profile{DisplayName: name}
		if profile.DisplayName == "" {
			profile.DisplayName = u.DisplayName.String
		}
		if u.AvatarUploadID.Valid {
			profile.AvatarUploadID = id.ID(u.AvatarUploadID.Bytes).String()
		}
		return userevents.ProfileUpdated(ctx, tx, user, profile)
	})
	if err != nil {
		return err
	}
	if underAge {
		s.cfg.Log.InfoContext(ctx, "kayıt yaş sınırı nedeniyle reddedildi", slog.String("user_id", user.String()))
		return ErrUnderAge
	}
	s.cfg.Log.InfoContext(ctx, "kayıt tamamlandı", slog.String("user_id", user.String()))
	return nil
}

// block refuses the account for good and keeps as little as possible: no
// birth date, no e-mail, no live sessions.
func (s *Service) block(ctx context.Context, q *store.Queries, user id.ID) error {
	if _, err := q.BlockRegistrationForAge(ctx, user.PG()); err != nil {
		return fmt.Errorf("yaş engeli: %w", err)
	}
	if err := q.ClearIdentityEmails(ctx, user.PG()); err != nil {
		return fmt.Errorf("e-posta temizleme: %w", err)
	}
	if _, err := q.RevokeUserSessions(ctx, store.RevokeUserSessionsParams{UserID: user.PG(), Reason: pgtype.Text{String: "age_blocked", Valid: true}}); err != nil {
		return fmt.Errorf("oturum iptali: %w", err)
	}
	return nil
}

// refCodeAlphabet has no easily confused characters (0/O, 1/I/L).
const refCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ" + "L"

// assignRefCode gives the user a referral code such as TYF-8K2M4Q. A clash
// with an existing code is retried inside a savepoint.
func assignRefCode(ctx context.Context, tx pgx.Tx, q *store.Queries, user id.ID) error {
	for range 5 {
		sp, err := tx.Begin(ctx)
		if err != nil {
			return fmt.Errorf("savepoint: %w", err)
		}
		_, err = q.WithTx(sp).SetRefCode(ctx, store.SetRefCodeParams{ID: user.PG(), RefCode: pgtype.Text{String: newRefCode(), Valid: true}})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = sp.Rollback(ctx)
			continue
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			return fmt.Errorf("referans kodu: %w", err)
		}
		return sp.Commit(ctx)
	}
	return errors.New("referans kodu üretilemedi")
}

func newRefCode() string {
	var b [6]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error
	out := []byte("TYF-")
	for _, x := range b {
		out = append(out, refCodeAlphabet[int(x)%len(refCodeAlphabet)])
	}
	return string(out)
}

// cleanDisplayName normalises spaces and checks the 1–40 character limit.
// Empty means "keep the current name".
func cleanDisplayName(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", nil
	}
	if utf8.RuneCountInString(s) > 40 {
		return "", ErrInvalidDisplayName
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return "", ErrInvalidDisplayName
		}
	}
	return s, nil
}
