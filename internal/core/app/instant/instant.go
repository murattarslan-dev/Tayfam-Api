// Package instant creates instant (individual) events: a hidden tayfa of
// kind instant with the organizer as its only admin and a single event, in
// one transaction, with the organizer's friends invited directly (ADR-0032,
// issue #209; Notion "Faz 2 (v2)" §5).
//
// The tayfa is an ordinary tayfa afterwards: membership, moderation, the
// event, check-in, album and chat use their own services, and the
// permission table applies the instant rules (authz.Settings.Instant).
// Converting it into a standard tayfa is tayfa.Service.Convert; archiving
// it 7 days after the event is the instant-archive job.
package instant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/event"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/membership"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

const (
	// MaxOpen is how many open instant events (not ended, not cancelled) a
	// user may organize at a time (ADR-0032 §5).
	MaxOpen = 3
	// MaxInvitees bounds the friends invited with the create request; the
	// daily direct invitation limit applies as well.
	MaxInvitees = 10
)

// Config wires a Service.
type Config struct {
	Pool        *pgxpool.Pool
	Log         *slog.Logger
	Now         func() time.Time
	Tayfas      *tayfa.Service
	Events      *event.Service
	Memberships *membership.Service
}

// Service creates instant events.
type Service struct {
	cfg Config
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Log == nil || cfg.Tayfas == nil || cfg.Events == nil || cfg.Memberships == nil {
		return nil, errors.New("instant: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg}, nil
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Input is a create request: the event and the friends to invite.
type Input struct {
	Event    event.Input
	Invitees []id.ID
}

// Result is the created (or, on a retry, the first created) tayfa and
// event.
type Result struct {
	Tayfa tayfa.View
	Event event.Event
}

// Create creates the caller's instant event. A repeat with the same
// Event.ClientRequestID returns the tayfa and event created the first time.
func (s *Service) Create(ctx context.Context, user id.ID, in Input) (Result, error) {
	if in.Event.Recurrence != nil {
		return Result{}, apperr.Validation("recurrence")
	}
	if in.Event.CoverUploadID != (id.ID{}) {
		return Result{}, apperr.Validation("cover_upload_id")
	}
	// An instant event never reaches discovery (ADR-0032 §8).
	if in.Event.Discoverable {
		return Result{}, apperr.Validation("discoverable")
	}
	if len(in.Invitees) > MaxInvitees {
		return Result{}, apperr.Validation("invitee_user_ids")
	}
	seen := make(map[id.ID]bool, len(in.Invitees))
	for _, u := range in.Invitees {
		if u == (id.ID{}) || u == user || seen[u] {
			return Result{}, apperr.Validation("invitee_user_ids")
		}
		seen[u] = true
	}
	plan, err := s.cfg.Events.Prepare(in.Event)
	if err != nil {
		return Result{}, err
	}
	crid := plan.ClientRequestID()
	name := plan.Title()
	normalized := strings.ToLowerSpecial(unicode.TurkishCase, name)

	now := s.now()
	var (
		tayfaID, eventID id.ID
		replay           bool
	)
	err = txn.WithActor(ctx, s.cfg.Pool, user, func(tx pgx.Tx, q *store.Queries) error {
		// The same user's creates run one at a time: neither the open
		// limit nor the retry check can be raced.
		if err := q.LockTayfaCreation(ctx, "instant_create:"+user.String()); err != nil {
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

		n, err := q.CountOpenInstantEvents(ctx, store.CountOpenInstantEventsParams{CreatedBy: user.PG(), Now: ts(now)})
		if err != nil {
			return fmt.Errorf("açık anlık etkinlikler: %w", err)
		}
		if n >= MaxOpen {
			return apperr.ErrInstantEventLimit
		}

		tayfaID = id.New()
		if err := tayfa.WithJoinCode(ctx, tx, func(q *store.Queries, code string) error {
			return q.CreateInstantTayfa(ctx, store.CreateInstantTayfaParams{
				ID: tayfaID.PG(), Name: name, NameNormalized: normalized, JoinCode: code,
				CreatedBy: user.PG(), ClientRequestID: crid.PG(),
			})
		}); err != nil {
			return fmt.Errorf("anlık tayfa: %w", err)
		}
		if err := q.CreateMembership(ctx, store.CreateMembershipParams{
			TayfaID: tayfaID.PG(), UserID: user.PG(), Role: authz.Admin.String(),
			Status: string(authz.StatusActive), JoinedVia: "creator", JoinedAt: ts(now),
		}); err != nil {
			return fmt.Errorf("kurucu üyelik: %w", err)
		}
		// Only the events module is on (ADR-0032 §2); the rest cannot be
		// turned on while the tayfa is instant.
		for _, m := range tayfa.Modules {
			if err := q.SetTayfaModule(ctx, store.SetTayfaModuleParams{TayfaID: tayfaID.PG(), Module: m, Enabled: m == access.ModuleEvents}); err != nil {
				return fmt.Errorf("modül: %w", err)
			}
		}
		subject := txn.Target{Type: "tayfa", ID: tayfaID}
		meta := map[string]any{"kind": access.KindInstant}
		if err := txn.Audit(ctx, q, tayfaID, user, tayfa.EventCreated, subject, meta); err != nil {
			return err
		}
		if err := txn.Event(ctx, q, tayfa.EventCreated, tayfaID, user, subject, meta); err != nil {
			return err
		}

		if eventID, err = s.cfg.Events.CreateInstantTx(ctx, q, user, tayfaID, plan); err != nil {
			return err
		}
		for _, u := range in.Invitees {
			if _, err := s.cfg.Memberships.InviteFriendTx(ctx, q, user, tayfaID, u); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	v, _, err := s.cfg.Tayfas.Get(ctx, user, tayfaID)
	if replay {
		// The first request's tayfa must be an instant tayfa the caller
		// still belongs to; anything else reuses the request ID.
		if errors.Is(err, apperr.ErrTayfaNotFound) || (err == nil && (v.Kind != access.KindInstant || v.InstantEventID == (id.ID{}))) {
			return Result{}, apperr.Validation("client_request_id")
		}
		eventID = v.InstantEventID
	}
	if err != nil {
		return Result{}, err
	}
	ev, err := s.cfg.Events.Get(ctx, user, tayfaID, eventID)
	if err != nil {
		return Result{}, err
	}
	if !replay {
		s.cfg.Log.InfoContext(ctx, "anlık etkinlik oluşturuldu",
			slog.String("tayfa_id", tayfaID.String()), slog.String("event_id", eventID.String()), slog.String("user_id", user.String()))
	}
	return Result{Tayfa: v, Event: ev}, nil
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
