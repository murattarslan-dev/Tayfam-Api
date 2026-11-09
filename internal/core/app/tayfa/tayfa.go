// Package tayfa creates tayfas and manages their settings, modules, join
// code and deletion (TayfaService, proto/tayfam/core/v1/tayfa.proto; data
// model v14 "Tayfa", "Tayfa yönetimi").
//
// Every tayfa-scoped method starts the same way: the caller comes from the
// verified token, access.Load checks for an active membership (anyone else
// gets apperr.ErrTayfaNotFound) and access.Authorize asks the permission
// table. Writes run in txn.WithActor and record an audit log entry and an
// outbox event in the same transaction.
package tayfa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const (
	// DeletionDelay is how long a deletion request waits (v14 "Tayfa silmede
	// bekleme").
	DeletionDelay = 72 * time.Hour
	// createWindow is the per-user tayfa creation window: one per 24 hours.
	createWindow = 24 * time.Hour
	// joinCodeAttempts bounds retries on a join code clash.
	joinCodeAttempts = 5
)

// Audit actions and event types. Audit records and events carry IDs and
// field names only, never names, descriptions or the join code.
const (
	EventCreated           = "tayfa.created"
	EventUpdated           = "tayfa.updated"
	EventDeletionRequested = "tayfa.deletion_requested"
	EventDeletionCancelled = "tayfa.deletion_cancelled"

	auditModuleSet       = "tayfa.module_set"
	auditJoinCodeRotated = "tayfa.join_code_rotated"
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  func() time.Time
}

// Service implements TayfaService's business logic.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil {
		return nil, errors.New("tayfa: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// now is UTC with the database's microsecond precision, so returned times
// match what is read back later.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// ModuleState is one module's switch.
type ModuleState struct {
	Module  string
	Enabled bool
}

// View is the full view of a tayfa, for its active members only.
type View struct {
	ID          id.ID
	Name        string
	Description string
	// Interests in EventType order.
	Interests  []string
	JoinPolicy string
	// MembersCanCreateEvents lets every active member create events; when
	// false only moderators and admins can (2026-11-09).
	MembersCanCreateEvents bool
	// Modules has one entry per Modules value, in that order.
	Modules []ModuleState
	Status  string
	// SuspendedAt is zero unless the platform suspended the tayfa.
	SuspendedAt time.Time
	// DeletionAt is zero unless a deletion is pending.
	DeletionAt  time.Time
	MemberCount int64
	// JoinCode is set only for callers allowed to rotate it.
	JoinCode  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is a create request.
type CreateInput struct {
	Name        string
	Description string
	Interests   []string
	// JoinPolicy "" means JoinApproval.
	JoinPolicy      string
	ClientRequestID string
}

// Create creates a tayfa with user as its only member, an active admin. A
// repeat with the same ClientRequestID returns the first tayfa.
func (s *Service) Create(ctx context.Context, user id.ID, in CreateInput) (View, error) {
	name, normalized, err := cleanName(in.Name)
	if err != nil {
		return View{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return View{}, err
	}
	interests, err := cleanInterests(in.Interests)
	if err != nil {
		return View{}, err
	}
	policy := in.JoinPolicy
	if policy == "" {
		policy = JoinApproval
	}
	if !validJoinPolicy(policy) {
		return View{}, apperr.Validation(FieldJoinPolicy)
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return View{}, apperr.Validation("client_request_id")
	}

	now := s.now()
	var (
		tayfaID id.ID
		replay  bool
	)
	err = txn.WithActor(ctx, s.cfg.Pool, user, func(tx pgx.Tx, q *store.Queries) error {
		// Same-user creates run one at a time: neither the daily limit nor
		// the client_request_id check can be raced.
		if err := q.LockTayfaCreation(ctx, "tayfa_create:"+user.String()); err != nil {
			return fmt.Errorf("oluşturma kilidi: %w", err)
		}
		prev, err := q.GetTayfaByClientRequestID(ctx, store.GetTayfaByClientRequestIDParams{CreatedBy: user.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			if prev.DeletedAt.Valid {
				return apperr.Validation("client_request_id")
			}
			tayfaID, replay = id.ID(prev.ID.Bytes), true
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}

		n, err := q.CountTayfasCreatedSince(ctx, store.CountTayfasCreatedSinceParams{CreatedBy: user.PG(), Since: ts(now.Add(-createWindow))})
		if err != nil {
			return fmt.Errorf("günlük sınır: %w", err)
		}
		if n > 0 {
			return apperr.ErrTayfaCreateLimit
		}

		tayfaID = id.New()
		if err := withJoinCode(ctx, tx, func(q *store.Queries, code string) error {
			return q.CreateTayfa(ctx, store.CreateTayfaParams{
				ID: tayfaID.PG(), Name: name, NameNormalized: normalized,
				Description: text(desc), JoinCode: code, JoinPolicy: policy,
				CreatedBy: user.PG(), ClientRequestID: crid.PG(),
			})
		}); err != nil {
			return fmt.Errorf("tayfa: %w", err)
		}
		if err := q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: tayfaID.PG(), UserID: user.PG(), Role: authz.Admin.String(),
			Status: string(authz.StatusActive), JoinedVia: "creator", JoinedAt: ts(now),
		}); err != nil {
			return fmt.Errorf("kurucu üyelik: %w", err)
		}
		for _, m := range Modules {
			if err := q.SetTayfaModule(ctx, store.SetTayfaModuleParams{TayfaID: tayfaID.PG(), Module: m, Enabled: true}); err != nil {
				return fmt.Errorf("modül: %w", err)
			}
		}
		for _, v := range interests {
			if err := q.AddTayfaInterest(ctx, store.AddTayfaInterestParams{TayfaID: tayfaID.PG(), EventType: v}); err != nil {
				return fmt.Errorf("ilgi alanı: %w", err)
			}
		}
		return record(ctx, q, tayfaID, user, EventCreated, EventCreated, nil)
	})
	if err != nil {
		return View{}, err
	}

	v, _, err := s.get(ctx, user, tayfaID)
	if replay && errors.Is(err, apperr.ErrTayfaNotFound) {
		// The first create's tayfa exists but the caller is no longer an
		// active member: the request ID cannot be reused.
		return View{}, apperr.Validation("client_request_id")
	}
	if err == nil && !replay {
		s.cfg.Log.InfoContext(ctx, "tayfa oluşturuldu", slog.String("tayfa_id", tayfaID.String()), slog.String("user_id", user.String()))
	}
	return v, err
}

// Get returns the tayfa and the caller's role.
func (s *Service) Get(ctx context.Context, user, tayfa id.ID) (View, authz.Role, error) {
	return s.get(ctx, user, tayfa)
}

func (s *Service) get(ctx context.Context, user, tayfa id.ID) (View, authz.Role, error) {
	t, m, err := access.Load(ctx, s.q, tayfa, user, false)
	if err != nil {
		return View{}, 0, err
	}
	if err := access.Authorize(m, authz.TayfaView, s.now()); err != nil {
		return View{}, 0, err
	}
	vs, err := s.views(ctx, []viewRow{fromTayfa(t, m)})
	if err != nil {
		return View{}, 0, err
	}
	return vs[0], m.Role, nil
}

// UpdateInput is an update request. Only the fields named in Fields change.
type UpdateInput struct {
	Fields      []string
	Name        string
	Description string
	Interests   []string
	JoinPolicy  string
	// MembersCanCreateEvents is the event creation setting.
	MembersCanCreateEvents bool
}

// updateFields are the allowed update mask paths, in audit record order.
var updateFields = []string{FieldName, FieldDescription, FieldInterests, FieldJoinPolicy, FieldMembersCanCreateEvents}

// Update changes the tayfa's settings. Admin only.
func (s *Service) Update(ctx context.Context, user, tayfa id.ID, in UpdateInput) (View, error) {
	if len(in.Fields) == 0 {
		return View{}, apperr.Validation("update_mask")
	}
	set := map[string]bool{}
	for _, f := range in.Fields {
		if !slices.Contains(updateFields, f) {
			return View{}, apperr.Validation("update_mask")
		}
		set[f] = true
	}
	var (
		name, normalized, desc string
		interests              []string
		err                    error
	)
	if set[FieldName] {
		if name, normalized, err = cleanName(in.Name); err != nil {
			return View{}, err
		}
	}
	if set[FieldDescription] {
		if desc, err = cleanDescription(in.Description); err != nil {
			return View{}, err
		}
	}
	if set[FieldInterests] {
		if interests, err = cleanInterests(in.Interests); err != nil {
			return View{}, err
		}
	}
	if set[FieldJoinPolicy] && !validJoinPolicy(in.JoinPolicy) {
		return View{}, apperr.Validation(FieldJoinPolicy)
	}
	// Field names in a fixed order for the audit record.
	var fields []string
	for _, f := range updateFields {
		if set[f] {
			fields = append(fields, f)
		}
	}

	err = s.write(ctx, user, tayfa, authz.TayfaUpdateSettings, func(q *store.Queries, _ pgx.Tx, t access.Tayfa) error {
		p := store.UpdateTayfaSettingsParams{
			ID: tayfa.PG(), Name: t.Name, NameNormalized: t.NameNormalized,
			Description: t.Description, JoinPolicy: t.JoinPolicy,
			MembersCanCreateEvents: t.MembersCanCreateEvents,
		}
		if set[FieldName] {
			p.Name, p.NameNormalized = name, normalized
		}
		if set[FieldDescription] {
			p.Description = text(desc)
		}
		if set[FieldJoinPolicy] {
			p.JoinPolicy = in.JoinPolicy
		}
		if set[FieldMembersCanCreateEvents] {
			p.MembersCanCreateEvents = in.MembersCanCreateEvents
		}
		if n, err := q.UpdateTayfaSettings(ctx, p); err != nil {
			return fmt.Errorf("ayarlar: %w", err)
		} else if n != 1 {
			return apperr.ErrTayfaNotFound
		}
		if set[FieldInterests] {
			if err := q.DeleteTayfaInterests(ctx, tayfa.PG()); err != nil {
				return fmt.Errorf("ilgi alanları: %w", err)
			}
			for _, v := range interests {
				if err := q.AddTayfaInterest(ctx, store.AddTayfaInterestParams{TayfaID: tayfa.PG(), EventType: v}); err != nil {
					return fmt.Errorf("ilgi alanı: %w", err)
				}
			}
		}
		return record(ctx, q, tayfa, user, EventUpdated, EventUpdated, map[string]any{"fields": fields})
	})
	if err != nil {
		return View{}, err
	}
	v, _, err := s.get(ctx, user, tayfa)
	return v, err
}

// SetModule turns a module on or off. Setting the current value changes
// nothing and records nothing. Admin only.
func (s *Service) SetModule(ctx context.Context, user, tayfa id.ID, module string, enabled bool) error {
	if !slices.Contains(Modules, module) {
		return apperr.Validation("module")
	}
	return s.write(ctx, user, tayfa, authz.TayfaManageModules, func(q *store.Queries, _ pgx.Tx, _ access.Tayfa) error {
		current, err := q.ListTayfaModules(ctx, tayfa.PG())
		if err != nil {
			return fmt.Errorf("modüller: %w", err)
		}
		for _, c := range current {
			if c.Module == module && c.Enabled == enabled {
				return nil
			}
		}
		if err := q.SetTayfaModule(ctx, store.SetTayfaModuleParams{TayfaID: tayfa.PG(), Module: module, Enabled: enabled}); err != nil {
			return fmt.Errorf("modül: %w", err)
		}
		meta := map[string]any{"fields": []string{"modules"}, "module": module, "enabled": enabled}
		return record(ctx, q, tayfa, user, auditModuleSet, EventUpdated, meta)
	})
}

// RotateJoinCode replaces the join code and returns the new one. Admin only.
func (s *Service) RotateJoinCode(ctx context.Context, user, tayfa id.ID) (string, error) {
	var code string
	err := s.write(ctx, user, tayfa, authz.TayfaRotateJoinCode, func(q *store.Queries, tx pgx.Tx, _ access.Tayfa) error {
		if err := withJoinCode(ctx, tx, func(q *store.Queries, c string) error {
			n, err := q.SetTayfaJoinCode(ctx, store.SetTayfaJoinCodeParams{ID: tayfa.PG(), JoinCode: c})
			if err == nil && n != 1 {
				return apperr.ErrTayfaNotFound
			}
			code = c
			return err
		}); err != nil {
			return fmt.Errorf("katılım kodu: %w", err)
		}
		// The code itself is never recorded.
		return record(ctx, q, tayfa, user, auditJoinCodeRotated, EventUpdated, map[string]any{"fields": []string{"join_code"}})
	})
	if err != nil {
		return "", err
	}
	return code, nil
}

// RequestDeletion schedules the tayfa for deletion DeletionDelay from now and
// returns that time. Admin only.
func (s *Service) RequestDeletion(ctx context.Context, user, tayfa id.ID) (time.Time, error) {
	now := s.now()
	at := now.Add(DeletionDelay)
	err := s.writeChecked(ctx, user, tayfa, authz.TayfaRequestDeletion, func(t access.Tayfa) error {
		switch {
		case access.Suspended(t) || access.Archived(t):
			return apperr.ErrTayfaReadOnly
		case access.DeletionPending(t):
			return apperr.ErrDeletionAlreadyRequested
		}
		return nil
	}, func(q *store.Queries, _ pgx.Tx, _ access.Tayfa) error {
		n, err := q.RequestTayfaDeletion(ctx, store.RequestTayfaDeletionParams{
			ID: tayfa.PG(), RequestedAt: ts(now), RequestedBy: user.PG(), ScheduledFor: ts(at),
		})
		if err != nil {
			return fmt.Errorf("silme talebi: %w", err)
		}
		if n != 1 {
			return apperr.ErrDeletionAlreadyRequested
		}
		if err := record(ctx, q, tayfa, user, EventDeletionRequested, EventDeletionRequested,
			map[string]any{"deletion_scheduled_for": at.Format(time.RFC3339Nano)}); err != nil {
			return err
		}
		// v14 "Tayfa silmede bekleme": all admins and moderators hear about it.
		to, err := notify.Staff(ctx, q, tayfa, user)
		if err != nil {
			return err
		}
		return notify.Write(ctx, q, notify.Request{
			Type: notify.TypeTayfaDeletionRequested, Tayfa: tayfa, Actor: user, TargetType: notify.TargetTayfa, Target: tayfa,
		}, to)
	})
	if err != nil {
		return time.Time{}, err
	}
	return at, nil
}

// CancelDeletion cancels a pending deletion. Any admin may cancel; it works
// while the tayfa is read-only.
func (s *Service) CancelDeletion(ctx context.Context, user, tayfa id.ID) error {
	return s.writeChecked(ctx, user, tayfa, authz.TayfaCancelDeletion, func(t access.Tayfa) error {
		if !access.DeletionPending(t) {
			return apperr.ErrDeletionNotRequested
		}
		return nil
	}, func(q *store.Queries, _ pgx.Tx, _ access.Tayfa) error {
		n, err := q.CancelTayfaDeletion(ctx, tayfa.PG())
		if err != nil {
			return fmt.Errorf("silme iptali: %w", err)
		}
		if n != 1 {
			return apperr.ErrDeletionNotRequested
		}
		return record(ctx, q, tayfa, user, EventDeletionCancelled, EventDeletionCancelled, nil)
	})
}

type writeFunc func(q *store.Queries, tx pgx.Tx, t access.Tayfa) error

// write runs fn for an active member allowed to perform a on a tayfa that is
// not read-only.
func (s *Service) write(ctx context.Context, user, tayfa id.ID, a authz.Action, fn writeFunc) error {
	return s.writeChecked(ctx, user, tayfa, a, func(t access.Tayfa) error {
		if access.ReadOnly(t) {
			return apperr.ErrTayfaReadOnly
		}
		return nil
	}, fn)
}

// writeChecked locks the tayfa, checks membership and permission, then
// check (the tayfa's state), then runs fn, all in one transaction.
func (s *Service) writeChecked(ctx context.Context, user, tayfa id.ID, a authz.Action, check func(access.Tayfa) error, fn writeFunc) error {
	now := s.now()
	return txn.WithActor(ctx, s.cfg.Pool, user, func(tx pgx.Tx, q *store.Queries) error {
		t, m, err := access.Load(ctx, q, tayfa, user, true)
		if err != nil {
			return err
		}
		if err := access.Authorize(m, a, now); err != nil {
			return err
		}
		if err := check(t); err != nil {
			return err
		}
		return fn(q, tx, t)
	})
}

// record writes the audit record and the outbox event of a change. Both get
// the same metadata: IDs and field names only.
func record(ctx context.Context, q *store.Queries, tayfa, actor id.ID, action, event string, meta map[string]any) error {
	subject := txn.Target{Type: "tayfa", ID: tayfa}
	if err := txn.Audit(ctx, q, tayfa, actor, action, subject, meta); err != nil {
		return err
	}
	return txn.Event(ctx, q, event, tayfa, actor, subject, meta)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func text(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
