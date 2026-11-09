// Package platformadmin is the access layer of the platform administration
// panel (issue #104 stage 1; Notion "Platform Yönetim Paneli"; ADR-0008,
// ADR-0011). identity owns three of the panel's seven independent layers:
//
//  4. platform_admins: only listed users get in. The application never
//     writes to that table; the platform owner adds a row with one SQL
//     command (docs/runbooks/platform-yoneticisi.md).
//  5. A separate session: SignIn issues an admin token (auth.AdminSigner:
//     its own token type and audience, 8 hours, no refresh). App tokens do
//     not open the panel and admin tokens do not open the app.
//  6. Destructive actions need a provider sign-in in the last 5 minutes and
//     a written reason (CheckDestructive).
//  7. Every panel action is written with its reason to platform_audit_logs,
//     which only takes inserts (Record).
//
// Layers 1–3 (separate host, Cloudflare Access, the Access JWT checked on
// every request) are gateway's. gateway passes the e-mail address from the
// verified Access JWT; SignIn requires the Google account to have the same
// verified address, so the person who passed Cloudflare Access and the one
// who signed in with Google are the same.
//
// Being an administrator is checked again on every call (Get), not only at
// sign-in: deleting the platform_admins row shuts every open panel session
// at its next request, although the token is valid for 8 hours.
package platformadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/oidc"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Provider is the only sign-in provider of the panel (Notion: "Google ile").
const Provider = "google"

// Reason length limits; the database checks the same (00006).
const (
	MinReasonLength = 10
	MaxReasonLength = 1000
)

// Audit actions written by this package.
const (
	ActionSignIn = "admin.sign_in"
	// signInReason is the reason of the automatic sign-in entry. It is data
	// in the audit log, not interface text.
	signInReason = "Panel girişi"
)

var (
	// ErrNotAllowed: panel sign-in refused. One error for every cause (not
	// an administrator, address mismatch, stale or invalid ID token) so the
	// answer says nothing about which check failed.
	ErrNotAllowed = errors.New("panel girişi reddedildi")
	// ErrNotAdmin: the admin token's user is no longer an administrator.
	ErrNotAdmin = errors.New("platform yöneticisi değil")
	// ErrReasonRequired: a destructive action without a valid reason.
	ErrReasonRequired = errors.New("gerekçe zorunlu")
	// ErrInvalidAudit: an audit entry that does not fit the table.
	ErrInvalidAudit = errors.New("geçersiz denetim kaydı")
	// ErrUnavailable: the provider's keys could not be fetched.
	ErrUnavailable = errors.New("kimlik doğrulama şu anda yapılamıyor")
	// ErrReauthenticationRequired: see auth.ErrReauthenticationRequired.
	ErrReauthenticationRequired = auth.ErrReauthenticationRequired
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Google verifies the panel's Google ID tokens.
	Google oidc.Verifier
	Signer *auth.AdminSigner
	Now    func() time.Time // optional, for tests
}

// Service signs administrators in to the panel and keeps its audit log.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Google == nil || cfg.Signer == nil {
		return nil, errors.New("platformadmin: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// SignInInput is one panel sign-in attempt.
type SignInInput struct {
	IDToken string
	// Nonce is the value gateway generated for this attempt and kept in an
	// HttpOnly cookie; the ID token must carry it.
	Nonce string
	// AccessEmail is the address in the Cloudflare Access JWT that gateway
	// verified on this request. Personal data: never logged.
	AccessEmail string
}

// Token is a signed admin token.
type Token struct {
	UserID    id.ID
	SessionID id.ID
	Token     string
	ExpiresAt time.Time
	AuthTime  time.Time
}

// SignIn verifies the Google ID token, matches it to the Cloudflare Access
// identity, checks platform_admins and issues an admin token. It writes the
// sign-in to the audit log in the same transaction.
func (s *Service) SignIn(ctx context.Context, in SignInInput) (Token, error) {
	ident, err := s.cfg.Google.Verify(ctx, in.IDToken, in.Nonce)
	if errors.Is(err, oidc.ErrInvalidToken) {
		return Token{}, s.refuse(ctx, "kimlik token'ı geçersiz")
	}
	if err != nil {
		return Token{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	now := s.cfg.Now().UTC()
	// The provider must have checked the person just now: an ID token is
	// valid for an hour, and a copied one must not open a fresh panel
	// session (the #75 rule for account deletion, here with 5 minutes).
	if ident.IssuedAt.IsZero() || now.Sub(ident.IssuedAt) > auth.AdminReauthWindow || ident.IssuedAt.After(now.Add(time.Minute)) {
		return Token{}, s.refuse(ctx, "sağlayıcı girişi güncel değil")
	}
	if !sameAddress(ident.Email, in.AccessEmail) {
		return Token{}, s.refuse(ctx, "Cloudflare Access kimliği eşleşmedi")
	}

	var t Token
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		admin, err := q.GetPlatformAdminByIdentity(ctx, store.GetPlatformAdminByIdentityParams{Provider: Provider, Subject: ident.Subject})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotAllowed
		}
		if err != nil {
			return fmt.Errorf("platform yöneticisi: %w", err)
		}
		user, _ := id.FromPG(admin.ID)
		sid := id.New()
		tok, exp, err := s.cfg.Signer.Issue(user.String(), sid.String(), ident.IssuedAt)
		if err != nil {
			return fmt.Errorf("admin token'ı: %w", err)
		}
		t = Token{UserID: user, SessionID: sid, Token: tok, ExpiresAt: exp, AuthTime: ident.IssuedAt.Truncate(time.Second)}
		return Record(ctx, q, user, Entry{
			Action: ActionSignIn, TargetType: "user", TargetID: user, Reason: signInReason,
			Metadata: map[string]string{"admin_session_id": sid.String()},
		})
	})
	if errors.Is(err, ErrNotAllowed) {
		return Token{}, s.refuse(ctx, "platform yöneticisi değil")
	}
	if err != nil {
		return Token{}, err
	}
	// ADR-0011 "Fark et": every panel sign-in is a security event.
	s.cfg.Log.WarnContext(ctx, "panel girişi yapıldı",
		slog.String("user_id", t.UserID.String()),
		slog.String("admin_session_id", t.SessionID.String()))
	return t, nil
}

// refuse logs a refused sign-in (without the address or the token) and
// returns ErrNotAllowed.
func (s *Service) refuse(ctx context.Context, reason string) error {
	s.cfg.Log.WarnContext(ctx, "panel girişi reddedildi", slog.String("reason", reason))
	return ErrNotAllowed
}

// sameAddress compares two e-mail addresses case-insensitively. An empty
// address (the provider did not mark it verified) never matches.
func sameAddress(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

// Admin is a signed-in platform administrator.
type Admin struct {
	UserID      id.ID
	DisplayName string
	SessionID   id.ID
	AuthTime    time.Time
	ExpiresAt   time.Time
}

// Get returns the administrator of a verified admin token, or ErrNotAdmin
// when the user is no longer one (row removed, account deleted). Every
// admin method calls it first.
func (s *Service) Get(ctx context.Context, c auth.AdminClaims) (Admin, error) {
	return get(ctx, s.q, c)
}

func get(ctx context.Context, q *store.Queries, c auth.AdminClaims) (Admin, error) {
	user, ok := id.Parse(c.Subject)
	if !ok {
		return Admin{}, ErrNotAdmin
	}
	sid, ok := id.Parse(c.SessionID)
	if !ok {
		return Admin{}, ErrNotAdmin
	}
	row, err := q.GetPlatformAdmin(ctx, user.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return Admin{}, ErrNotAdmin
	}
	if err != nil {
		return Admin{}, fmt.Errorf("platform yöneticisi: %w", err)
	}
	return Admin{UserID: user, DisplayName: row.DisplayName.String, SessionID: sid, AuthTime: c.AuthTime, ExpiresAt: c.ExpiresAt}, nil
}

// Action is the work of a destructive panel action. Do runs first, in the
// transaction; it may fill in e (target, metadata). Events, if set, runs
// after the audit entry is written: outbox events must be the last writes
// of a transaction (outbox.Write).
type Action struct {
	Do     func(ctx context.Context, q *store.Queries, e *Entry) error
	Events func(ctx context.Context, tx pgx.Tx) error
}

// Destructive runs a destructive panel action (ban, tayfa deletion, plan
// change, ...): in one transaction it checks that the caller is still an
// administrator, that the provider sign-in is at most 5 minutes old and
// that a reason is given, runs the action, writes the audit entry and then
// the action's events. If any step fails, nothing is written. The reason
// and recency checks are CheckDestructive.
func (s *Service) Destructive(ctx context.Context, c auth.AdminClaims, e Entry, a Action) error {
	if err := CheckDestructive(c, e.Reason, s.cfg.Now()); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		admin, err := get(ctx, q, c)
		if err != nil {
			return err
		}
		if a.Do != nil {
			if err := a.Do(ctx, q, &e); err != nil {
				return err
			}
		}
		if e.Metadata == nil {
			e.Metadata = map[string]string{}
		}
		e.Metadata["admin_session_id"] = admin.SessionID.String()
		if err := Record(ctx, q, admin.UserID, e); err != nil {
			return err
		}
		if a.Events != nil {
			return a.Events(ctx, tx)
		}
		return nil
	})
}

// CheckDestructive enforces layer 6 for an action about to run: a provider
// sign-in within auth.AdminReauthWindow and a reason of MinReasonLength to
// MaxReasonLength characters.
func CheckDestructive(c auth.AdminClaims, reason string, now time.Time) error {
	if err := c.RequireRecentAuth(now); err != nil {
		return err
	}
	if !ValidReason(reason) {
		return ErrReasonRequired
	}
	return nil
}

// ValidReason reports whether reason is an acceptable written reason.
func ValidReason(reason string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(reason))
	return utf8.ValidString(reason) && n >= MinReasonLength && utf8.RuneCountInString(reason) <= MaxReasonLength
}

// Entry is one platform audit log entry.
type Entry struct {
	// Action is "<area>.<verb>", e.g. "user.ban", "tayfa.suspend".
	Action string
	// TargetType and TargetID name what the action was done to; both or
	// neither.
	TargetType string
	TargetID   id.ID
	Reason     string
	// Metadata holds IDs and short values only: never an e-mail address,
	// message text, location or token.
	Metadata map[string]string
}

var (
	actionPattern = regexp.MustCompile(`^[a-z][a-z_]{0,30}(\.[a-z][a-z_]{0,30}){1,2}$`)
	targetTypes   = map[string]bool{"user": true, "tayfa": true, "membership": true, "report": true, "moderation_term": true, "subscription": true}
)

const maxMetadataBytes = 4096

// Record writes e to platform_audit_logs with q, which should be the
// transaction of the action itself so the action and its record commit
// together.
func Record(ctx context.Context, q *store.Queries, actor id.ID, e Entry) error {
	params, err := auditParams(actor, e)
	if err != nil {
		return err
	}
	if err := q.InsertPlatformAuditLog(ctx, params); err != nil {
		return fmt.Errorf("platform denetim kaydı: %w", err)
	}
	return nil
}

func auditParams(actor id.ID, e Entry) (store.InsertPlatformAuditLogParams, error) {
	var zero store.InsertPlatformAuditLogParams
	if !actionPattern.MatchString(e.Action) || !ValidReason(e.Reason) {
		return zero, ErrInvalidAudit
	}
	params := store.InsertPlatformAuditLogParams{
		ID: id.New().PG(), ActorID: actor.PG(), Action: e.Action, Reason: e.Reason, Metadata: []byte("{}"),
	}
	switch {
	case e.TargetType == "" && e.TargetID == (id.ID{}):
	case targetTypes[e.TargetType] && e.TargetID != (id.ID{}):
		params.TargetType = pgtype.Text{String: e.TargetType, Valid: true}
		params.TargetID = e.TargetID.PG()
	default:
		return zero, ErrInvalidAudit
	}
	if len(e.Metadata) > 0 {
		b, err := json.Marshal(e.Metadata)
		if err != nil || len(b) > maxMetadataBytes {
			return zero, ErrInvalidAudit
		}
		params.Metadata = b
	}
	return params, nil
}

// CoreEntry is a platform audit entry of an action core carried out
// (PlatformAuditService, issue #104). core writes the action and the entry
// in one transaction to its outbox; the relay delivers it at least once.
type CoreEntry struct {
	EventID    id.ID // core's outbox event ID; the entry's ID
	OccurredAt time.Time
	Actor      id.ID
	Entry
	AdminSession id.ID
}

// RecordFromCore writes e unless an entry with its ID exists. It does not
// check that the actor is still an administrator: core checked that before
// the action, and the record of an action that happened must not be lost.
func (s *Service) RecordFromCore(ctx context.Context, e CoreEntry) error {
	if e.EventID == (id.ID{}) || e.Actor == (id.ID{}) || e.OccurredAt.IsZero() {
		return ErrInvalidAudit
	}
	if e.Metadata == nil {
		e.Metadata = map[string]string{}
	}
	if e.AdminSession != (id.ID{}) {
		e.Metadata["admin_session_id"] = e.AdminSession.String()
	}
	params, err := auditParams(e.Actor, e.Entry)
	if err != nil {
		return err
	}
	params.ID = e.EventID.PG()
	if _, err := s.q.InsertPlatformAuditLogOnce(ctx, store.InsertPlatformAuditLogOnceParams{
		ID: params.ID, ActorID: params.ActorID, Action: params.Action, TargetType: params.TargetType,
		TargetID: params.TargetID, Reason: params.Reason, Metadata: params.Metadata,
		CreatedAt: pgtype.Timestamptz{Time: e.OccurredAt.UTC(), Valid: true},
	}); err != nil {
		return fmt.Errorf("platform denetim kaydı: %w", err)
	}
	return nil
}
