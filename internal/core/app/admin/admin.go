// Package admin is core's side of the platform administration panel
// (issue #104 stage 2; Notion "Platform Yönetim Paneli", "Kötüye Kullanım
// ve Genç Kullanıcı Güvenliği"; ADR-0008).
//
// Every method runs for a verified admin token (auth.Guard admin path) and
// first asks identity, with the same token, whether the caller is still a
// platform administrator (Checker): platform_admins is identity's table.
//
// Changes (suspend, unsuspend, delete, role, report decision) also need a
// reason (10–1000 characters) and a Google sign-in in the last 5 minutes
// (the admin token's auth_time). Each change, its tayfa audit record (for
// the tayfa's staff, without the administrator's identity), its events and
// its platform audit entry are written in ONE core transaction. The
// platform audit entry is a platform.audit outbox event, which the relay
// delivers at least once to identity's PlatformAuditService
// (platform_audit_logs, idempotent by event ID). So there is never a change
// without its record, nor a record of a change that did not happen; the
// record reaches identity with the outbox's delay.
//
// What the panel never sees from here: direct message content (a DM
// report shows the reporter's own evidence_text only), poll votes,
// locations (meeting points, catch coordinates, check-ins), and the content
// of a tayfa except the one reported item. There is no way to act as a
// user: no method takes a user's token or writes as a user.
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// EventAudit is the outbox event that carries a platform audit entry to
// identity (package identityaudit's consumer).
const EventAudit = "platform.audit"

// Tayfa events of this package; chatrelay delivers them as
// tayfa_status_changed.
const (
	EventTayfaSuspended   = "tayfa.suspended"
	EventTayfaUnsuspended = "tayfa.unsuspended"
	EventTayfaDeleted     = "tayfa.deleted"
	eventMembershipChange = "membership.changed"
)

// systemJob is the app.system_job value of the panel's role changes
// (db/core/migrations/00014_platform_admin.sql).
const systemJob = "platform_admin"

// Reason limits; identity checks the same.
const (
	MinReason = 10
	MaxReason = 1000
	// maxSuspendReason is tayfas.suspended_reason's limit.
	maxSuspendReason = 500
)

// Caller is a platform administrator confirmed by identity.
type Caller struct {
	User    id.ID
	Session id.ID
	Claims  auth.AdminClaims
}

// Checker asks identity whether the admin token's user is still a platform
// administrator. It returns apperr.ErrNotPlatformAdmin when not and
// apperr.ErrUnavailable when identity cannot answer.
type Checker interface {
	Check(ctx context.Context) error
}

// Config wires a Service.
type Config struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Checker Checker
	Now     func() time.Time // optional, for tests
}

// Service implements the panel's core methods.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Checker == nil {
		return nil, errors.New("admin: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// caller confirms c with identity.
func (s *Service) caller(ctx context.Context, c auth.AdminClaims) (Caller, error) {
	user, ok1 := id.Parse(c.Subject)
	sid, ok2 := id.Parse(c.SessionID)
	if !ok1 || !ok2 {
		return Caller{}, apperr.ErrNotPlatformAdmin
	}
	if err := s.cfg.Checker.Check(ctx); err != nil {
		return Caller{}, err
	}
	return Caller{User: user, Session: sid, Claims: c}, nil
}

// destructive confirms the caller and checks layer 6: a recent sign-in and
// a reason.
func (s *Service) destructive(ctx context.Context, c auth.AdminClaims, reason string) (Caller, error) {
	if !ValidReason(reason) {
		return Caller{}, apperr.ErrReasonRequired
	}
	if err := c.RequireRecentAuth(s.cfg.Now()); err != nil {
		return Caller{}, apperr.ErrReauthenticationRequired
	}
	return s.caller(ctx, c)
}

// ValidReason reports whether reason is an acceptable written reason.
func ValidReason(reason string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(reason))
	return utf8.ValidString(reason) && n >= MinReason && utf8.RuneCountInString(reason) <= MaxReason
}

// audit writes the platform audit entry of an action as the outbox event
// identity records. It must be written in the action's transaction, after
// the action's other writes (outbox events last).
func audit(ctx context.Context, q *store.Queries, c Caller, tayfa id.ID, action, targetType string, target id.ID, reason string, extra map[string]string) error {
	p := map[string]any{
		"action": action, "target_type": targetType, "target_id": target.String(),
		"reason": reason, "admin_session_id": c.Session.String(),
	}
	if tayfa != (id.ID{}) {
		p["tayfa_id"] = tayfa.String()
	}
	for k, v := range extra {
		p[k] = v
	}
	return txn.Event(ctx, q, EventAudit, tayfa, c.User, txn.Target{Type: targetType, ID: target}, p)
}

func pgtext(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// tayfaRecord writes the tayfa's own audit record of a platform action,
// without the administrator's identity: the tayfa's staff see that the
// platform acted, not who.
func tayfaRecord(ctx context.Context, q *store.Queries, tayfa id.ID, action string, target txn.Target) error {
	tt := pgtype.Text{String: target.Type, Valid: target.Type != ""}
	tid := pgtype.UUID{}
	if target.Type != "" {
		tid = target.ID.PG()
	}
	if err := q.CreateSystemAuditLog(ctx, store.CreateSystemAuditLogParams{
		ID: id.New().PG(), TayfaID: tayfa.PG(), Action: action, TargetType: tt, TargetID: tid, Metadata: []byte("{}"),
	}); err != nil {
		return fmt.Errorf("tayfa denetim kaydı: %w", err)
	}
	return nil
}

// normalize is the tayfa name search key (tayfas.name_normalized), with
// LIKE wildcards escaped.
func normalize(q string) string {
	q = strings.ToLowerSpecial(unicode.TurkishCase, strings.TrimSpace(q))
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func timeOf(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time.UTC()
}

func idOf(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	v, _ := id.FromPG(u)
	return v
}

func notFound(err error, nf error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return nf
	}
	return err
}
