// Package session signs users in with a provider ID token, rotates refresh
// tokens and ends sessions (ADR-0004). Users list their own sessions and end
// one or all others from another device (issue #75).
//
// A session is one device's sign-in and one refresh token family. Refresh
// tokens are 256-bit random values; only their SHA-256 hashes are stored.
// Every refresh replaces the token. Presenting an already used token
// revokes the whole session, because it means the token was copied.
//
// Every path that ends a session writes session.revoked in the same
// transaction, so chat closes the session's real-time connections at once
// rather than when the access token expires (ADR-0014, issue #92).
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

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/consent"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/integrity"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/userevents"
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
	// ErrSanctioned: the account is under an active platform suspension or
	// ban (ADR-0004 "Platform yaptırımları", issue #104).
	ErrSanctioned     = errors.New("hesap platform yaptırımı altında")
	ErrInvalidRefresh = errors.New("geçersiz veya süresi dolmuş oturum")
	// ErrRefreshConflict is a concurrent duplicate refresh within reuseGrace.
	// The client should use the token it got from the other request.
	ErrRefreshConflict = errors.New("aynı token ile eşzamanlı yenileme")
	ErrUnavailable     = errors.New("kimlik doğrulama şu anda yapılamıyor")
	// ErrCallerSession: the caller's own session (from the access token) is
	// ended, expired or not the caller's. The access token outlives its
	// session by up to 15 minutes (ADR-0004); session management refuses it.
	ErrCallerSession = errors.New("oturum kapalı")
	// ErrSessionNotFound: no open session of the caller has this ID. Another
	// user's session gives the same answer.
	ErrSessionNotFound = errors.New("oturum bulunamadı")
)

// Reasons written to sessions.revoked_reason by this package.
const (
	reasonLogout      = "logout"
	reasonRemote      = "user_signout_remote"
	reasonTokenReuse  = "token_reuse"
	reasonAccountGone = "account_deleted"
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
	// ConsentRequired: the user is registered but has not accepted the
	// current version of the privacy notice or terms. The client shows the
	// new texts and calls AccountService.AcceptConsents (v14 "Onay kaydı").
	ConsentRequired bool
	// IntegrityPassed: the session passed its last integrity check
	// (ADR-0022); the access token's claim (issue #164).
	IntegrityPassed bool
	// IntegrityCheckAfter is when the client should next send an integrity
	// proof with a refresh; zero when integrity checks are off.
	IntegrityCheckAfter time.Time
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
	// Documents are the current text versions; a registered user who has
	// not accepted them gets Tokens.ConsentRequired.
	Documents consent.Documents
	// Integrity checks that sessions belong to the official app (ADR-0022);
	// nil is off.
	Integrity *integrity.Checker
	Now       func() time.Time
}

// Service implements sign-in, refresh and sign-out.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.API == nil || cfg.Registration == nil || len(cfg.Verifiers) == 0 || !cfg.Documents.Valid() {
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
	// Integrity is the optional proof that the official app signs in
	// (ADR-0022), bound to Nonce.
	Integrity integrity.Proof
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
	// The integrity check runs after the ID token is verified (a bad sign-in
	// spends no quota) and outside the transaction (it asks Google).
	var check *integrity.Result
	if s.cfg.Integrity.Enabled() {
		check = s.cfg.Integrity.Check(ctx, in.Platform, in.Integrity, integrity.SignInBinding(in.Nonce))
	}

	// Two first sign-ins of the same account can race to create it; the
	// loser hits the unique key and retries, finding the winner's account.
	var t Tokens
	for attempt := 0; ; attempt++ {
		t, err = s.signInTx(ctx, ident, in.Platform, check)
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

func (s *Service) signInTx(ctx context.Context, ident oidc.Identity, platform string, check *integrity.Result) (Tokens, error) {
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
		if err := checkSanction(ctx, q, userID); err != nil {
			return err
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
		t, err = s.issue(ctx, q, userID, sessionID, user.RegisteredAt.Valid, refresh, expires)
		if err != nil {
			return err
		}
		return s.applyIntegrity(ctx, q, sessionID, integrity.State{Status: integrity.StatusUnverified}, check, &t)
	})
	return t, err
}

// Refresh exchanges a refresh token for a new access and refresh token.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	return s.RefreshWithProof(ctx, refreshToken, integrity.Proof{})
}

// RefreshWithProof is Refresh with an integrity proof bound to refreshToken
// (ADR-0022). The proof is checked only when the session is due
// (Tokens.IntegrityCheckAfter); a due session without a proof is recorded
// as such. Nothing is refused for integrity here: enforcement is gateway's
// (issue #164).
func (s *Service) RefreshWithProof(ctx context.Context, refreshToken string, proof integrity.Proof) (Tokens, error) {
	hash, ok := hashRefreshToken(refreshToken)
	if !ok {
		return Tokens{}, ErrInvalidRefresh
	}
	check := s.refreshCheck(ctx, hash, refreshToken, proof)
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
			if _, err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: row.SessionID, Reason: text(reasonTokenReuse)}); err != nil {
				return fmt.Errorf("oturum iptali: %w", err)
			}
			revoked = sessionID
			userID, _ := id.FromPG(row.UserID)
			return userevents.SessionsRevoked(ctx, tx, userID, sessionID)
		}

		if n, err := q.MarkRefreshTokenUsed(ctx, row.ID); err != nil || n != 1 {
			return fmt.Errorf("refresh token işaretleme: n=%d: %w", n, err)
		}
		user, err := q.GetUser(ctx, row.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			// No session.revoked: the account's deletion wrote user.deleted,
			// which closes every connection of the user in chat.
			if _, err := q.RevokeSession(ctx, store.RevokeSessionParams{ID: row.SessionID, Reason: text(reasonAccountGone)}); err != nil {
				return fmt.Errorf("oturum iptali: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("kullanıcı: %w", err)
		}
		uid, _ := id.FromPG(row.UserID)
		if err := checkSanction(ctx, q, uid); err != nil {
			return err
		}
		expires := now.Add(sessionTTL)
		if err := q.ExtendSession(ctx, store.ExtendSessionParams{ID: row.SessionID, ExpiresAt: ts(expires)}); err != nil {
			return fmt.Errorf("oturum uzatma: %w", err)
		}
		refresh, newHash := newRefreshToken()
		if err := q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{ID: id.New().PG(), SessionID: row.SessionID, TokenHash: newHash}); err != nil {
			return fmt.Errorf("refresh token: %w", err)
		}
		// The device is seen: the 90-day device retention counts from here
		// (v14 "Saklama süreleri").
		if err := q.TouchDevice(ctx, row.DeviceID); err != nil {
			return fmt.Errorf("cihaz: %w", err)
		}
		userID, _ := id.FromPG(row.UserID)
		t, err = s.issue(ctx, q, userID, sessionID, user.RegisteredAt.Valid, refresh, expires)
		if err != nil {
			return err
		}
		prev := stateOf(row.IntegrityStatus, row.IntegrityReason, row.IntegrityCheckedAt, row.IntegrityPassedAt)
		if check != nil && !prev.Due(now) {
			// Another refresh checked the session in the meantime.
			check = nil
		}
		return s.applyIntegrity(ctx, q, sessionID, prev, check, &t)
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

// SignOut ends a session. Ending an already ended session is not an error
// and writes no event.
func (s *Service) SignOut(ctx context.Context, sessionID id.ID) error {
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		users, err := s.q.WithTx(tx).RevokeSession(ctx, store.RevokeSessionParams{ID: sessionID.PG(), Reason: text(reasonLogout)})
		if err != nil {
			return fmt.Errorf("oturum kapatma: %w", err)
		}
		for _, u := range users {
			if err := userevents.SessionsRevoked(ctx, tx, id.ID(u.Bytes), sessionID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.cfg.Log.InfoContext(ctx, "oturum kapatıldı", slog.String("session_id", sessionID.String()))
	return nil
}

func (s *Service) issue(ctx context.Context, q *store.Queries, user, session id.ID, registered bool, refresh string, refreshExpires time.Time) (Tokens, error) {
	signer := s.cfg.Registration
	var consentRequired bool
	if registered {
		signer = s.cfg.API
		var err error
		if consentRequired, err = consent.Required(ctx, q, user, s.cfg.Documents); err != nil {
			return Tokens{}, err
		}
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
		ConsentRequired:     consentRequired,
	}, nil
}

// Caller is the verified user and session of an access token.
type Caller struct {
	User    id.ID
	Session id.ID
}

// Info is one of the caller's open sessions. It has no IP address and no
// token.
type Info struct {
	ID        id.ID
	Platform  string // android, ios or web
	CreatedAt time.Time
	// LastSeenAt is the last refresh; a session is used at least every 15
	// minutes while the app is open.
	LastSeenAt time.Time
	Current    bool // the session of the caller's access token
}

// List returns the caller's open sessions, most recently used first.
func (s *Service) List(ctx context.Context, c Caller) ([]Info, error) {
	if err := s.checkCaller(ctx, s.q, c); err != nil {
		return nil, err
	}
	rows, err := s.q.ListUserSessions(ctx, store.ListUserSessionsParams{UserID: c.User.PG(), Now: ts(s.cfg.Now())})
	if err != nil {
		return nil, fmt.Errorf("oturumlar: %w", err)
	}
	out := make([]Info, 0, len(rows))
	for _, r := range rows {
		sid := id.ID(r.ID.Bytes)
		out = append(out, Info{
			ID: sid, Platform: r.Platform,
			CreatedAt: r.CreatedAt.Time.UTC(), LastSeenAt: r.LastRefreshedAt.Time.UTC(),
			Current: sid == c.Session,
		})
	}
	return out, nil
}

// Revoke ends one of the caller's sessions. Ending the caller's own
// session is a sign-out. A session of another user, an unknown or an
// already ended one is ErrSessionNotFound: the answer does not tell them
// apart.
func (s *Service) Revoke(ctx context.Context, c Caller, target id.ID) error {
	reason := reasonRemote
	if target == c.Session {
		reason = reasonLogout
	}
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := s.checkCaller(ctx, q, c); err != nil {
			return err
		}
		ended, err := q.RevokeOwnSession(ctx, store.RevokeOwnSessionParams{ID: target.PG(), UserID: c.User.PG(), Reason: text(reason)})
		if err != nil {
			return fmt.Errorf("oturum kapatma: %w", err)
		}
		if len(ended) != 1 {
			return ErrSessionNotFound
		}
		return userevents.SessionsRevoked(ctx, tx, c.User, target)
	})
	if err != nil {
		return err
	}
	s.cfg.Log.InfoContext(ctx, "oturum kapatıldı",
		slog.String("user_id", c.User.String()),
		slog.String("session_id", target.String()),
		slog.String("reason", reason))
	return nil
}

// RevokeOthers ends every session of the caller except the current one and
// returns how many it ended.
func (s *Service) RevokeOthers(ctx context.Context, c Caller) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if err := s.checkCaller(ctx, q, c); err != nil {
			return err
		}
		ended, err := q.RevokeOtherSessions(ctx, store.RevokeOtherSessionsParams{UserID: c.User.PG(), KeepID: c.Session.PG()})
		if err != nil {
			return fmt.Errorf("oturum kapatma: %w", err)
		}
		n = len(ended)
		ids := make([]id.ID, len(ended))
		for i, e := range ended {
			ids[i] = id.ID(e.Bytes)
		}
		return userevents.SessionsRevoked(ctx, tx, c.User, ids...)
	})
	if err != nil {
		return 0, err
	}
	s.cfg.Log.InfoContext(ctx, "diğer oturumlar kapatıldı",
		slog.String("user_id", c.User.String()),
		slog.String("session_id", c.Session.String()),
		slog.Int("count", n))
	return n, nil
}

// checkCaller makes sure the caller's own session is still open. A token
// of an ended session (signed out, ended remotely, account deleted) cannot
// manage sessions even while it has not expired.
func (s *Service) checkCaller(ctx context.Context, q *store.Queries, c Caller) error {
	_, err := q.GetCallerSession(ctx, store.GetCallerSessionParams{ID: c.Session.PG(), UserID: c.User.PG(), Now: ts(s.cfg.Now())})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCallerSession
	}
	if err != nil {
		return fmt.Errorf("oturum: %w", err)
	}
	return nil
}

// checkSanction refuses an account under an active platform sanction. Its
// sessions were ended when the sanction started; this keeps it from opening
// or refreshing one while it lasts.
func checkSanction(ctx context.Context, q *store.Queries, user id.ID) error {
	_, err := q.GetActiveSanctionType(ctx, user.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("yaptırım: %w", err)
	}
	return ErrSanctioned
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

// refreshCheck runs the integrity check of a refresh before its transaction
// (it may ask Google), if the session is due. nil means no check.
func (s *Service) refreshCheck(ctx context.Context, hash []byte, refreshToken string, proof integrity.Proof) *integrity.Result {
	if !s.cfg.Integrity.Enabled() {
		return nil
	}
	row, err := s.q.PeekRefreshIntegrity(ctx, hash)
	if err != nil {
		// An unknown, used or revoked token: the transaction answers it.
		return nil
	}
	prev := stateOf(row.IntegrityStatus, row.IntegrityReason, row.IntegrityCheckedAt, row.IntegrityPassedAt)
	if !prev.Due(s.cfg.Now()) {
		return nil
	}
	return s.cfg.Integrity.Check(ctx, row.Platform, proof, integrity.RefreshBinding(refreshToken))
}

// applyIntegrity stores the session's next integrity state, when a check
// ran, and puts it on t. With checks off it does nothing.
func (s *Service) applyIntegrity(ctx context.Context, q *store.Queries, session id.ID, prev integrity.State, check *integrity.Result, t *Tokens) error {
	if !s.cfg.Integrity.Enabled() {
		return nil
	}
	now := s.cfg.Now()
	next := integrity.Next(prev, check, now)
	if check != nil {
		if err := q.SetSessionIntegrity(ctx, store.SetSessionIntegrityParams{
			ID: session.PG(), Status: string(next.Status), Reason: optText(string(next.Reason)),
			CheckedAt: optTS(next.CheckedAt), PassedAt: optTS(next.PassedAt),
		}); err != nil {
			return fmt.Errorf("oturum bütünlüğü: %w", err)
		}
	}
	t.IntegrityPassed = next.Passed()
	t.IntegrityCheckAfter = next.CheckAfter(now)
	return nil
}

// stateOf reads a session's stored integrity state.
func stateOf(status string, reason pgtype.Text, checked, passed pgtype.Timestamptz) integrity.State {
	st := integrity.State{Status: integrity.Status(status), Reason: integrity.Reason(reason.String)}
	if checked.Valid {
		st.CheckedAt = checked.Time.UTC()
	}
	if passed.Valid {
		st.PassedAt = passed.Time.UTC()
	}
	return st
}

// optText is NULL for "" (a passed or never checked session has no reason).
func optText(v string) pgtype.Text {
	if v == "" {
		return pgtype.Text{}
	}
	return text(v)
}

func optTS(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return ts(t)
}
