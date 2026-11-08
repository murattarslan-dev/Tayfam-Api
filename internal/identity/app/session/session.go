// Package session signs users in with a provider ID token, rotates refresh
// tokens and ends sessions (ADR-0004).
//
// A session is one device's sign-in and one refresh token family. Refresh
// tokens are 256-bit random values; only their SHA-256 hashes are stored.
// Every refresh replaces the token. Presenting an already used token
// revokes the whole session, because it means the token was copied.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const (
	// Lifetime of an unused session (ADR-0004); every refresh extends it.
	sessionTTL = 90 * 24 * time.Hour
	// reuseGrace tolerates a client that sends the same refresh token twice
	// at once (two tabs, a retried request). Within it the second request
	// fails without revoking the session (ADR-0004).
	reuseGrace = 10 * time.Second
	// defaultName is stored when the provider gives no name; the user can
	// change it. It is data, not interface text.
	defaultName = "Tayfam üyesi"
)

// Audiences of access tokens. A user who has not finished registration gets
// a registration-only token: core and chat accept only AudienceAPI, so the
// token cannot reach them (ADR-0012).
const (
	AudienceAPI          = "tayfam-api"
	AudienceRegistration = "tayfam-registration"
)

var (
	ErrUnsupportedProvider = errors.New("desteklenmeyen giriş sağlayıcısı")
	ErrInvalidCredentials  = errors.New("kimlik doğrulanamadı")
	ErrAgeBlocked          = errors.New("hesap yaş sınırı nedeniyle açılamadı")
	ErrInvalidRefresh      = errors.New("geçersiz veya süresi dolmuş oturum")
	// ErrRefreshConflict is a concurrent duplicate refresh within reuseGrace.
	// The client should use the token it got from the other request.
	ErrRefreshConflict = errors.New("aynı token ile eşzamanlı yenileme")
	ErrUnavailable     = errors.New("kimlik doğrulama şu anda yapılamıyor")
)

// Platforms a device can be.
var platforms = map[string]bool{"android": true, "ios": true, "web": true}

// Tokens is what a client receives after sign-in or refresh.
type Tokens struct {
	UserID              id.ID
	SessionID           id.ID
	AccessToken         string
	AccessExpiresAt     time.Time
	RefreshToken        string
	RefreshExpiresAt    time.Time
	RegistrationPending bool
}

// Config wires a Service.
type Config struct {
	Pool      *pgxpool.Pool
	Log       *slog.Logger
	Verifiers map[string]oidc.Verifier // by provider name
	// API signs tokens for registered users, Registration for the rest. Both
	// use identity's signing key.
	API          *auth.Signer
	Registration *auth.Signer
	Now          func() time.Time
}

// Service implements sign-in, refresh and sign-out.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.API == nil || cfg.Registration == nil || len(cfg.Verifiers) == 0 {
		return nil, errors.New("session: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// SignInInput is one sign-in attempt.
type SignInInput struct {
	Provider string
	IDToken  string
	Nonce    string
	Platform string
}

// SignIn verifies the provider token, finds or creates the user and opens a
// new session on a new device.
func (s *Service) SignIn(ctx context.Context, in SignInInput) (Tokens, error) {
	v, ok := s.cfg.Verifiers[in.Provider]
	if !ok {
		return Tokens{}, ErrUnsupportedProvider
	}
	if !platforms[in.Platform] {
		return Tokens{}, fmt.Errorf("%w: platform", ErrInvalidCredentials)
	}
	ident, err := v.Verify(ctx, in.IDToken, in.Nonce)
	if errors.Is(err, oidc.ErrInvalidToken) {
		s.cfg.Log.InfoContext(ctx, "giriş reddedildi", slog.String("provider", in.Provider), slog.String("reason", err.Error()))
		return Tokens{}, ErrInvalidCredentials
	}
	if err != nil {
		return Tokens{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	// Two first sign-ins of the same account can race to create it; the
	// loser hits the unique key and retries, finding the winner's account.
	var t Tokens
	for attempt := 0; ; attempt++ {
		t, err = s.signInTx(ctx, ident, in.Platform)
		var pgErr *pgconn.PgError
		if attempt == 0 && errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue
		}
		break
	}
	if err != nil {
		return Tokens{}, err
	}
	s.cfg.Log.InfoContext(ctx, "giriş yapıldı",
		slog.String("provider", in.Provider),
		slog.String("user_id", t.UserID.String()),
		slog.String("session_id", t.SessionID.String()),
		slog.Bool("registration_pending", t.RegistrationPending))
	return t, nil
}

func (s *Service) signInTx(ctx context.Context, ident oidc.Identity, platform string) (Tokens, error) {
	var t Tokens
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		email := pgtype.Text{String: ident.Email, Valid: ident.Email != ""}
		existing, err := q.GetAuthIdentity(ctx, store.GetAuthIdentityParams{Provider: ident.Provider, Subject: ident.Subject})
		var userID id.ID
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			userID = id.New()
			name := ident.Name
			if name == "" {
				name = defaultName
			}
			if err := q.CreateUser(ctx, store.CreateUserParams{ID: userID.PG(), DisplayName: pgtype.Text{String: name, Valid: true}, Locale: "tr"}); err != nil {
				return fmt.Errorf("kullanıcı oluşturma: %w", err)
			}
			if err := q.CreateAuthIdentity(ctx, store.CreateAuthIdentityParams{ID: id.New().PG(), UserID: userID.PG(), Provider: ident.Provider, Subject: ident.Subject, Email: email}); err != nil {
				return fmt.Errorf("giriş kimliği oluşturma: %w", err)
			}
		case err != nil:
			return fmt.Errorf("giriş kimliği: %w", err)
		default:
			userID, _ = id.FromPG(existing.UserID)
			if err := q.TouchAuthIdentity(ctx, store.TouchAuthIdentityParams{ID: existing.ID, Email: email}); err != nil {
				return fmt.Errorf("giriş kimliği güncelleme: %w", err)
			}
		}

		user, err := q.GetUser(ctx, userID.PG())
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidCredentials // deleted account
		}
		if err != nil {
			return fmt.Errorf("kullanıcı: %w", err)
		}
		if user.AgeBlockedAt.Valid {
			return ErrAgeBlocked
		}

		deviceID := id.New()
		if err := q.CreateDevice(ctx, store.CreateDeviceParams{ID: deviceID.PG(), UserID: userID.PG(), Platform: platform}); err != nil {
			return fmt.Errorf("cihaz: %w", err)
		}
		sessionID := id.New()
		expires := s.cfg.Now().Add(sessionTTL)
		if err := q.CreateSession(ctx, store.CreateSessionParams{ID: sessionID.PG(), UserID: userID.PG(), DeviceID: deviceID.PG(), ExpiresAt: ts(expires)}); err != nil {
			return fmt.Errorf("oturum: %w", err)
		}
		refresh, hash := newRefreshToken()
		if err := q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{ID: id.New().PG(), SessionID: sessionID.PG(), TokenHash: hash}); err != nil {
			return fmt.Errorf("refresh token: %w", err)
		}
		t, err = s.issue(userID, sessionID, user.RegisteredAt.Valid, refresh, expires)
		return err
	})
	return t, err
}

// Refresh exchanges a refresh token for a new access and refresh token.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	hash, ok := hashRefreshToken(refreshToken)
	if !ok {
		return Tokens{}, ErrInvalidRefresh
	}
	var (
		t       Tokens
		revoked id.ID
	)
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		row, err := q.LockRefreshToken(ctx, hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidRefresh
		}
		if err != nil {
			return fmt.Errorf("refresh token: %w", err)
		}
		now := s.cfg.Now()
		sessionID, _ := id.FromPG(row.SessionID)
		if row.RevokedAt.Valid || !row.ExpiresAt.Time.After(now) {
			return ErrInvalidRefresh
		}
		if row.UsedAt.Valid {
			if now.Sub(row.UsedAt.Time) <= reuseGrace {
				return ErrRefreshConflict
			}
			// A used token came back: someone else holds a copy. End the
			// session for both; the real user signs in again (ADR-0004).
			if _, err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: row.SessionID, Reason: text("token_reuse")}); err != nil {
				return fmt.Errorf("oturum iptali: %w", err)
			}
			revoked = sessionID
			return nil
		}

		if n, err := q.MarkRefreshTokenUsed(ctx, row.ID); err != nil || n != 1 {
			return fmt.Errorf("refresh token işaretleme: n=%d: %w", n, err)
		}
		user, err := q.GetUser(ctx, row.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: row.SessionID, Reason: text("account_deleted")}); err != nil {
				return fmt.Errorf("oturum iptali: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("kullanıcı: %w", err)
		}
		expires := now.Add(sessionTTL)
		if err := q.ExtendSession(ctx, store.ExtendSessionParams{ID: row.SessionID, ExpiresAt: ts(expires)}); err != nil {
			return fmt.Errorf("oturum uzatma: %w", err)
		}
		refresh, newHash := newRefreshToken()
		if err := q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{ID: id.New().PG(), SessionID: row.SessionID, TokenHash: newHash}); err != nil {
			return fmt.Errorf("refresh token: %w", err)
		}
		userID, _ := id.FromPG(row.UserID)
		t, err = s.issue(userID, sessionID, user.RegisteredAt.Valid, refresh, expires)
		return err
	})
	if revoked != (id.ID{}) {
		s.cfg.Log.WarnContext(ctx, "kullanılmış refresh token tekrar geldi; oturum iptal edildi",
			slog.String("session_id", revoked.String()))
		return Tokens{}, ErrInvalidRefresh
	}
	if err != nil {
		return Tokens{}, err
	}
	if t.AccessToken == "" {
		return Tokens{}, ErrInvalidRefresh // account deleted, session revoked
	}
	return t, nil
}

// SignOut ends a session. Ending an already ended session is not an error.
func (s *Service) SignOut(ctx context.Context, sessionID id.ID) error {
	if _, err := s.q.RevokeSession(ctx, store.RevokeSessionParams{ID: sessionID.PG(), Reason: text("logout")}); err != nil {
		return fmt.Errorf("oturum kapatma: %w", err)
	}
	s.cfg.Log.InfoContext(ctx, "oturum kapatıldı", slog.String("session_id", sessionID.String()))
	return nil
}

func (s *Service) issue(user, session id.ID, registered bool, refresh string, refreshExpires time.Time) (Tokens, error) {
	signer := s.cfg.Registration
	if registered {
		signer = s.cfg.API
	}
	access, exp, err := signer.Issue(user.String(), session.String())
	if err != nil {
		return Tokens{}, fmt.Errorf("erişim token'ı: %w", err)
	}
	return Tokens{
		UserID: user, SessionID: session,
		AccessToken: access, AccessExpiresAt: exp,
		RefreshToken: refresh, RefreshExpiresAt: refreshExpires,
		RegistrationPending: !registered,
	}, nil
}

var b64 = base64.RawURLEncoding

// newRefreshToken returns a 256-bit random token and its SHA-256 hash.
func newRefreshToken() (string, []byte) {
	var raw [32]byte
	_, _ = rand.Read(raw[:]) // crypto/rand.Read never returns an error
	h := sha256.Sum256(raw[:])
	return b64.EncodeToString(raw[:]), h[:]
}

func hashRefreshToken(token string) ([]byte, bool) {
	raw, err := b64.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		return nil, false
	}
	h := sha256.Sum256(raw)
	return h[:], true
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
func text(s string) pgtype.Text         { return pgtype.Text{String: s, Valid: true} }
