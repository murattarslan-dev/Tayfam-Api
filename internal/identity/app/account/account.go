// Package account holds the account settings identity owns for the user
// themself: the referral code and who sees the attendance history (data
// model v14 "Arkadaşlık ve Davet (v11)", "Profil görünürlüğü"; issue #71).
//
// Every method acts on the verified caller only. Looking a user up by
// referral code is limited per user (LookupsPerHour), counted in the
// database so a restart does not reset it: codes are short enough to guess,
// and the limit is what keeps them from being scanned.
package account

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/refcode"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/userevents"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// LookupsPerHour is the referral code lookup limit per user (v14).
const LookupsPerHour = 10

const lookupWindow = time.Hour

var (
	// ErrUnknownUser: the caller has no registered, live account.
	ErrUnknownUser = errors.New("kullanıcı bulunamadı")
	// ErrRateLimited: the caller used up their lookups for the hour.
	ErrRateLimited = errors.New("çok fazla arama")
	// ErrCodeNotFound: the code is malformed, unknown or replaced.
	ErrCodeNotFound = errors.New("referans kodu bulunamadı")
	// ErrInvalidVisibility: not one of the Visibility values.
	ErrInvalidVisibility = errors.New("geçersiz görünürlük")

	// Avatar errors (issue #135): core's answer about the upload.
	ErrUploadNotFound = errors.New("yükleme bulunamadı")
	ErrUploadNotReady = errors.New("yükleme hazır değil")
	ErrUploadInUse    = errors.New("yükleme başka içerikte")
	// ErrMediaUnavailable: core could not be asked, or media is off there.
	ErrMediaUnavailable = errors.New("medya servisi yanıt vermedi")
)

// Media is core's MediaService as identity uses it: it attaches the
// caller's avatar upload with the caller's own token (the context carries
// it). It returns one of the Err* avatar errors above or
// ErrMediaUnavailable.
type Media interface {
	AttachAvatar(ctx context.Context, upload id.ID) error
}

// Visibility is users.activity_visibility.
type Visibility string

// Visibility values (users_activity_visibility CHECK).
const (
	VisibilityPrivate Visibility = "private"
	VisibilityTayfa   Visibility = "tayfa"
	VisibilityPublic  Visibility = "public"
)

// Valid reports whether v is one of the values.
func (v Visibility) Valid() bool {
	return v == VisibilityPrivate || v == VisibilityTayfa || v == VisibilityPublic
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// Media is nil when core is not configured (a local run): SetAvatar is
	// then ErrMediaUnavailable.
	Media Media
	Now   func() time.Time // optional, for tests
}

// Service implements the account settings.
type Service struct {
	pool  *pgxpool.Pool
	q     *store.Queries
	media Media
	now   func() time.Time
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("account: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{pool: cfg.Pool, q: store.New(cfg.Pool), media: cfg.Media, now: func() time.Time { return cfg.Now().UTC() }}, nil
}

// Account is the caller's own view.
type Account struct {
	ReferralCode       string
	ActivityVisibility Visibility
	AvatarUploadID     id.ID // zero: no avatar
}

// Get returns the caller's referral code and settings.
func (s *Service) Get(ctx context.Context, caller id.ID) (Account, error) {
	u, err := s.registered(ctx, s.q, caller)
	if err != nil {
		return Account{}, err
	}
	a := Account{ReferralCode: u.RefCode.String, ActivityVisibility: Visibility(u.ActivityVisibility)}
	if u.AvatarUploadID.Valid {
		a.AvatarUploadID = id.ID(u.AvatarUploadID.Bytes)
	}
	return a, nil
}

// SetAvatar makes the caller's READY avatar upload their avatar (issue
// #135). identity does not own media: core attaches the upload first, with
// the caller's own token, and refuses another user's, an unprocessed one or
// one of another purpose. Then the avatar is stored and core and chat get
// it with user.profile_updated; core deletes the previous avatar then.
func (s *Service) SetAvatar(ctx context.Context, caller, upload id.ID) error {
	if _, err := s.registered(ctx, s.q, caller); err != nil {
		return err
	}
	if s.media == nil {
		return ErrMediaUnavailable
	}
	if err := s.media.AttachAvatar(ctx, upload); err != nil {
		return err
	}
	return s.setAvatar(ctx, caller, upload)
}

// ClearAvatar removes the caller's avatar; core deletes the image when
// user.profile_updated reaches it. Without an avatar nothing changes.
func (s *Service) ClearAvatar(ctx context.Context, caller id.ID) error {
	return s.setAvatar(ctx, caller, id.ID{})
}

func (s *Service) setAvatar(ctx context.Context, caller, upload id.ID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.LockRegisteredUser(ctx, caller.PG()); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnknownUser
			}
			return fmt.Errorf("kullanıcı kilidi: %w", err)
		}
		u, err := s.registered(ctx, q, caller)
		if err != nil {
			return err
		}
		next := pgtype.UUID{}
		if upload != (id.ID{}) {
			next = upload.PG()
		}
		if u.AvatarUploadID == next {
			return nil
		}
		n, err := q.SetUserAvatar(ctx, store.SetUserAvatarParams{ID: caller.PG(), AvatarUploadID: next})
		if err != nil {
			return fmt.Errorf("avatar: %w", err)
		}
		if n != 1 {
			return ErrUnknownUser
		}
		p := userevents.Profile{DisplayName: u.DisplayName.String, ActivityVisibility: u.ActivityVisibility}
		if upload != (id.ID{}) {
			p.AvatarUploadID = upload.String()
		}
		return userevents.ProfileUpdated(ctx, tx, caller, p)
	})
}

// RotateReferralCode replaces the caller's referral code; the old one finds
// nobody afterwards.
func (s *Service) RotateReferralCode(ctx context.Context, caller id.ID) (string, error) {
	var code string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.registered(ctx, store.New(tx), caller); err != nil {
			return err
		}
		var err error
		code, err = refcode.Assign(ctx, tx, caller, true)
		if errors.Is(err, refcode.ErrNotAssigned) {
			return ErrUnknownUser
		}
		return err
	})
	return code, err
}

// Found is what a referral code reveals about its owner.
type Found struct {
	UserID         id.ID
	DisplayName    string
	AvatarUploadID id.ID // zero: no avatar
}

// FindByReferralCode returns the owner of code. Every attempt counts toward
// the caller's hourly limit, malformed and unknown codes included; the
// attempts of one caller are counted one at a time (the caller's row is
// locked), so parallel requests cannot pass the limit together.
func (s *Service) FindByReferralCode(ctx context.Context, caller id.ID, code string) (Found, error) {
	var out Found
	var limited bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.LockRegisteredUser(ctx, caller.PG()); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnknownUser
			}
			return fmt.Errorf("kullanıcı kilidi: %w", err)
		}
		now := s.now()
		since := pgtype.Timestamptz{Time: now.Add(-lookupWindow), Valid: true}
		if err := q.DeleteOldRefCodeLookups(ctx, store.DeleteOldRefCodeLookupsParams{UserID: caller.PG(), Since: since}); err != nil {
			return fmt.Errorf("eski aramalar: %w", err)
		}
		n, err := q.CountRecentRefCodeLookups(ctx, store.CountRecentRefCodeLookupsParams{UserID: caller.PG(), Since: since})
		if err != nil {
			return fmt.Errorf("arama sayısı: %w", err)
		}
		if n >= LookupsPerHour {
			limited = true
			return nil
		}
		if err := q.RecordRefCodeLookup(ctx, store.RecordRefCodeLookupParams{
			ID: id.New().PG(), UserID: caller.PG(), LookedUpAt: pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("arama kaydı: %w", err)
		}
		norm, ok := refcode.Normalize(code)
		if !ok {
			return nil
		}
		u, err := q.FindUserByRefCode(ctx, pgtype.Text{String: norm, Valid: true})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("referans kodu: %w", err)
		}
		out = Found{UserID: id.ID(u.ID.Bytes), DisplayName: u.DisplayName.String}
		if u.AvatarUploadID.Valid {
			out.AvatarUploadID = id.ID(u.AvatarUploadID.Bytes)
		}
		return nil
	})
	switch {
	case err != nil:
		return Found{}, err
	case limited:
		return Found{}, ErrRateLimited
	case out.UserID == (id.ID{}):
		// The attempt is committed: it counts even though nothing was found.
		return Found{}, ErrCodeNotFound
	}
	return out, nil
}

// SetActivityVisibility changes who sees the caller's attendance history
// and sends core the profile with the new setting (user.profile_updated,
// ADR-0009). Setting the current value again writes nothing.
func (s *Service) SetActivityVisibility(ctx context.Context, caller id.ID, v Visibility) (Visibility, error) {
	if !v.Valid() {
		return "", ErrInvalidVisibility
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := q.LockRegisteredUser(ctx, caller.PG()); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrUnknownUser
			}
			return fmt.Errorf("kullanıcı kilidi: %w", err)
		}
		u, err := s.registered(ctx, q, caller)
		if err != nil {
			return err
		}
		if Visibility(u.ActivityVisibility) == v {
			return nil
		}
		n, err := q.SetActivityVisibility(ctx, store.SetActivityVisibilityParams{ID: caller.PG(), ActivityVisibility: string(v)})
		if err != nil {
			return fmt.Errorf("görünürlük: %w", err)
		}
		if n != 1 {
			return ErrUnknownUser
		}
		p := userevents.Profile{DisplayName: u.DisplayName.String, ActivityVisibility: string(v)}
		if u.AvatarUploadID.Valid {
			p.AvatarUploadID = id.ID(u.AvatarUploadID.Bytes).String()
		}
		return userevents.ProfileUpdated(ctx, tx, caller, p)
	})
	if err != nil {
		return "", err
	}
	return v, nil
}

// registered loads the caller's live, registered account.
func (s *Service) registered(ctx context.Context, q *store.Queries, caller id.ID) (store.GetUserRow, error) {
	u, err := q.GetUser(ctx, caller.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return store.GetUserRow{}, ErrUnknownUser
	}
	if err != nil {
		return store.GetUserRow{}, fmt.Errorf("kullanıcı: %w", err)
	}
	if !u.RegisteredAt.Valid {
		return store.GetUserRow{}, ErrUnknownUser
	}
	return u, nil
}
