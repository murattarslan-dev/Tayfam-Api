// Package poll runs tayfa polls (PollService, proto/tayfam/core/v1/poll.proto;
// data model v14 "Anket ve Albüm (v12)" → "Anket (MVP)"; issue #60).
//
// Every method starts the same way: the caller comes from the verified
// token, access.Load checks for an active membership (anyone else gets
// apperr.ErrTayfaNotFound), access.RequireModule refuses every method, reads
// included, while the tayfa has the polls module turned off, and the
// permission table decides the rest. Every active member creates polls and
// votes; a muted member does neither. The creator closes and deletes their
// own poll; moderators and admins any poll above the creator's current role
// (authz.CanActOnContent).
//
// Anonymous polls. The voter is stored (poll_votes.user_id) only to stop
// double votes; who voted for which option never leaves this package for
// anyone but the voter themself:
//
//   - View.OwnOptionIDs is always the caller's own vote, read with the
//     caller's user ID; the per-option totals are the only other vote data
//     in a view.
//   - ListVoters refuses anonymous polls, and its query returns no rows for
//     them even if that check were skipped.
//   - Votes are not audited (a vote is not a moderation action, and the
//     audit log is readable by admins). The poll.voted event of an anonymous
//     poll holds the tayfa and the poll only, never an option.
//   - No error or log line carries an option ID: options are checked
//     against the poll before any insert, so a database error cannot echo
//     one either.
//
// Writes run in txn.WithActor with the tayfa row locked; votes also lock
// the poll row, which the poll_votes trigger checks again.
package poll

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits (v14 "Anket (MVP)"; the same CHECKs are in the database).
const (
	QuestionMax = 200
	OptionMax   = 100
	MinOptions  = 2
	MaxOptions  = 10
	// MaxCloseAhead bounds a poll's planned close time.
	MaxCloseAhead = 365 * 24 * time.Hour
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// Event types. Payloads hold IDs and settings only; never the question,
// the option texts, or an anonymous poll's options.
const (
	EventCreated = "poll.created"
	EventVoted   = "poll.voted"
	EventClosed  = "poll.closed"
	EventDeleted = "poll.deleted"
)

const subjectType = "poll"

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
}

// Service implements PollService's business logic.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("poll: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// View is a poll as one active member sees it.
type View struct {
	ID      id.ID
	TayfaID id.ID
	// EventID is zero when the poll is not tied to an event.
	EventID id.ID
	// CreatedBy is zero when the creator's account was deleted.
	CreatedBy      id.ID
	Question       string
	Anonymous      bool
	MultipleChoice bool
	// ClosesAt is the planned close time; zero when unset.
	ClosesAt time.Time
	// ClosedAt is the early close time; zero when not closed early.
	ClosedAt time.Time
	// Closed: closed early or past ClosesAt.
	Closed     bool
	CreatedAt  time.Time
	Options    []Option
	VoterCount int64
	// OwnOptionIDs is the caller's own vote, in option order. Never anyone
	// else's.
	OwnOptionIDs []id.ID
}

// Option is a poll option with its total.
type Option struct {
	ID        id.ID
	Text      string
	Position  int
	VoteCount int64
}

// CreateInput is a create request.
type CreateInput struct {
	Question       string
	Options        []string
	Anonymous      bool
	MultipleChoice bool
	// ClosesAt is optional (zero: open until closed).
	ClosesAt time.Time
	// EventID is optional: an event of the same tayfa.
	EventID string
}

// Create opens a poll by user.
func (s *Service) Create(ctx context.Context, user, tayfa id.ID, in CreateInput) (View, error) {
	question, err := cleanLine(in.Question, QuestionMax, "question")
	if err != nil {
		return View{}, err
	}
	options, err := cleanOptions(in.Options)
	if err != nil {
		return View{}, err
	}
	var event id.ID
	if in.EventID != "" {
		var ok bool
		if event, ok = id.Parse(in.EventID); !ok {
			return View{}, apperr.Validation("event_id")
		}
	}

	var v View
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller) error {
		if err := c.authorize(authz.PollCreate); err != nil {
			return err
		}
		closes := pgtype.Timestamptz{}
		if !in.ClosesAt.IsZero() {
			at := in.ClosesAt.UTC().Truncate(time.Microsecond)
			if !at.After(c.now) || at.Sub(c.now) > MaxCloseAhead {
				return apperr.Validation("close_time")
			}
			closes = ts(at)
		}
		eventID := pgtype.UUID{}
		if event != (id.ID{}) {
			ok, err := q.PollEventExists(ctx, store.PollEventExistsParams{TayfaID: tayfa.PG(), ID: event.PG()})
			if err != nil {
				return fmt.Errorf("etkinlik: %w", err)
			}
			if !ok {
				return apperr.ErrEventNotFound
			}
			eventID = event.PG()
		}
		pid := id.New()
		if err := q.CreatePoll(ctx, store.CreatePollParams{
			ID: pid.PG(), TayfaID: tayfa.PG(), EventID: eventID, CreatedBy: user.PG(), Question: question,
			Anonymous: in.Anonymous, MultipleChoice: in.MultipleChoice, ClosesAt: closes, CreatedAt: ts(c.now),
		}); err != nil {
			return fmt.Errorf("anket: %w", err)
		}
		for i, text := range options {
			if err := q.CreatePollOption(ctx, store.CreatePollOptionParams{
				ID: id.New().PG(), PollID: pid.PG(), Text: text, Position: int16(i), //nolint:gosec // at most MaxOptions
			}); err != nil {
				return fmt.Errorf("anket seçeneği: %w", err)
			}
		}
		if err := txn.TouchTayfa(ctx, q, tayfa, c.now); err != nil {
			return err
		}
		meta := map[string]any{"anonymous": in.Anonymous, "multiple_choice": in.MultipleChoice, "options": len(options)}
		payload := map[string]any{"anonymous": in.Anonymous, "multiple_choice": in.MultipleChoice}
		if eventID.Valid {
			meta["event_id"] = event.String()
			payload["event_id"] = event.String()
		}
		if err := txn.Audit(ctx, q, tayfa, user, EventCreated, target(pid), meta); err != nil {
			return err
		}
		if v, err = get(ctx, q, user, tayfa, pid, c.now); err != nil {
			return err
		}
		return emit(ctx, q, tayfa, user, pid, EventCreated, payload)
	})
	if err != nil {
		return View{}, err
	}
	return v, nil
}

// Get returns one poll.
func (s *Service) Get(ctx context.Context, user, tayfa, poll id.ID) (View, error) {
	c, err := s.read(ctx, user, tayfa, authz.PollView)
	if err != nil {
		return View{}, err
	}
	return get(ctx, s.q, user, tayfa, poll, c.now)
}

// Vote sets user's vote in poll to options, replacing an earlier vote.
// Setting the current vote again records nothing.
func (s *Service) Vote(ctx context.Context, user, tayfa, poll id.ID, options []string) (View, error) {
	chosen, err := parseOptionIDs(options)
	if err != nil {
		return View{}, err
	}
	var v View
	err = s.write(ctx, user, tayfa, func(q *store.Queries, c caller) error {
		if err := c.authorize(authz.PollVote); err != nil {
			return err
		}
		p, err := lockOpen(ctx, q, tayfa, poll, c.now)
		if err != nil {
			return err
		}
		if !p.MultipleChoice && len(chosen) > 1 {
			return apperr.Validation("option_ids")
		}
		valid, err := q.ListPollOptionIDs(ctx, poll.PG())
		if err != nil {
			return fmt.Errorf("anket seçenekleri: %w", err)
		}
		for _, o := range chosen {
			if !slices.Contains(valid, o.PG()) {
				return apperr.Validation("option_ids")
			}
		}
		own, err := ownVotes(ctx, q, user, []pgtype.UUID{poll.PG()})
		if err != nil {
			return err
		}
		if sameSet(own[poll], chosen) {
			v, err = get(ctx, q, user, tayfa, poll, c.now)
			return err
		}
		// Changing a vote deletes the old one first (v14); the trigger
		// allows one vote per person in a single-choice poll.
		if _, err := q.DeleteOwnPollVotes(ctx, store.DeleteOwnPollVotesParams{PollID: poll.PG(), UserID: user.PG()}); err != nil {
			return fmt.Errorf("eski oy: %w", err)
		}
		for _, o := range chosen {
			if err := q.CreatePollVote(ctx, store.CreatePollVoteParams{PollID: poll.PG(), OptionID: o.PG(), UserID: user.PG(), CreatedAt: ts(c.now)}); err != nil {
				return fmt.Errorf("oy: %w", err)
			}
		}
		if err := txn.TouchTayfa(ctx, q, tayfa, c.now); err != nil {
			return err
		}
		if v, err = get(ctx, q, user, tayfa, poll, c.now); err != nil {
			return err
		}
		payload := map[string]any{}
		if !p.Anonymous {
			ids := make([]string, 0, len(chosen))
			for _, o := range chosen {
				ids = append(ids, o.String())
			}
			payload["option_ids"] = ids
		}
		return emit(ctx, q, tayfa, user, poll, EventVoted, payload)
	})
	if err != nil {
		return View{}, err
	}
	return v, nil
}

// Withdraw removes user's vote from poll. Without a vote it records
// nothing.
func (s *Service) Withdraw(ctx context.Context, user, tayfa, poll id.ID) (View, error) {
	var v View
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller) error {
		if err := c.authorize(authz.PollVote); err != nil {
			return err
		}
		if _, err := lockOpen(ctx, q, tayfa, poll, c.now); err != nil {
			return err
		}
		n, err := q.DeleteOwnPollVotes(ctx, store.DeleteOwnPollVotesParams{PollID: poll.PG(), UserID: user.PG()})
		if err != nil {
			return fmt.Errorf("oy geri çekme: %w", err)
		}
		if v, err = get(ctx, q, user, tayfa, poll, c.now); err != nil || n == 0 {
			return err
		}
		return emit(ctx, q, tayfa, user, poll, EventVoted, map[string]any{"withdrawn": true})
	})
	if err != nil {
		return View{}, err
	}
	return v, nil
}

// Close closes poll before its planned close time. Closing a closed poll
// changes nothing.
func (s *Service) Close(ctx context.Context, user, tayfa, poll id.ID) (View, error) {
	var v View
	err := s.write(ctx, user, tayfa, func(q *store.Queries, c caller) error {
		p, err := lock(ctx, q, tayfa, poll)
		if err != nil {
			return err
		}
		if err := c.manage(ctx, q, p.CreatedBy, authz.PollCloseOwn, authz.PollCloseAny); err != nil {
			return err
		}
		if closed(p.ClosesAt, p.ClosedAt, c.now) {
			v, err = get(ctx, q, user, tayfa, poll, c.now)
			return err
		}
		n, err := q.ClosePoll(ctx, store.ClosePollParams{TayfaID: tayfa.PG(), ID: poll.PG(), ClosedAt: ts(c.now)})
		if err != nil {
			return fmt.Errorf("anket kapatma: %w", err)
		}
		if n != 1 {
			return apperr.ErrPollNotFound
		}
		if err := txn.Audit(ctx, q, tayfa, user, EventClosed, target(poll), map[string]any{"created_by": idString(p.CreatedBy)}); err != nil {
			return err
		}
		if v, err = get(ctx, q, user, tayfa, poll, c.now); err != nil {
			return err
		}
		return emit(ctx, q, tayfa, user, poll, EventClosed, nil)
	})
	if err != nil {
		return View{}, err
	}
	return v, nil
}

// Delete deletes poll and its votes; it cannot be restored.
func (s *Service) Delete(ctx context.Context, user, tayfa, poll id.ID) error {
	return s.write(ctx, user, tayfa, func(q *store.Queries, c caller) error {
		p, err := lock(ctx, q, tayfa, poll)
		if err != nil {
			return err
		}
		if err := c.manage(ctx, q, p.CreatedBy, authz.PollDeleteOwn, authz.PollDeleteAny); err != nil {
			return err
		}
		n, err := q.DeletePoll(ctx, store.DeletePollParams{TayfaID: tayfa.PG(), ID: poll.PG(), DeletedAt: ts(c.now), DeletedBy: user.PG()})
		if err != nil {
			return fmt.Errorf("anket silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrPollNotFound
		}
		// A deleted poll is never shown again: its votes are not kept.
		if err := q.DeletePollVotes(ctx, poll.PG()); err != nil {
			return fmt.Errorf("anketin oyları: %w", err)
		}
		if err := txn.Audit(ctx, q, tayfa, user, EventDeleted, target(poll), map[string]any{"created_by": idString(p.CreatedBy)}); err != nil {
			return err
		}
		return emit(ctx, q, tayfa, user, poll, EventDeleted, nil)
	})
}

// caller is the checked caller of a method.
type caller struct {
	user  id.ID
	tayfa id.ID
	m     authz.Membership
	now   time.Time
}

func (c caller) authorize(a authz.Action) error { return access.Authorize(c.m, a, c.now) }

// manage checks a change to a poll created by creator: the creator with own,
// anyone else with anyAction and a role above the creator's current role in
// the tayfa. A creator who is no longer an active member has no role.
func (c caller) manage(ctx context.Context, q *store.Queries, creator pgtype.UUID, own, anyAction authz.Action) error {
	if creator.Valid && id.ID(creator.Bytes) == c.user {
		return c.authorize(own)
	}
	if err := c.authorize(anyAction); err != nil {
		return err
	}
	var role authz.Role
	if creator.Valid {
		row, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: c.tayfa.PG(), UserID: creator})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("anketi açanın üyeliği: %w", err)
		case row.Status == string(authz.StatusActive):
			if role, err = authz.ParseRole(row.Role); err != nil {
				return fmt.Errorf("anketi açanın rolü: %w", err)
			}
		}
	}
	if !authz.CanActOnContent(c.m.Role, role) {
		return apperr.ErrRoleHierarchy
	}
	return nil
}

// read checks a reader: active member, module on, action a.
func (s *Service) read(ctx context.Context, user, tayfa id.ID, a authz.Action) (caller, error) {
	_, m, err := access.Load(ctx, s.q, tayfa, user, false)
	if err != nil {
		return caller{}, err
	}
	if err := access.RequireModule(ctx, s.q, tayfa, access.ModulePolls); err != nil {
		return caller{}, err
	}
	c := caller{user: user, tayfa: tayfa, m: m, now: s.now()}
	return c, c.authorize(a)
}

// write locks the tayfa, checks membership, module and that the tayfa is
// not read-only, then runs fn, all in one transaction. fn checks the
// permission itself.
func (s *Service) write(ctx context.Context, user, tayfa id.ID, fn func(q *store.Queries, c caller) error) error {
	return txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		t, m, err := access.Load(ctx, q, tayfa, user, true)
		if err != nil {
			return err
		}
		if err := access.RequireModule(ctx, q, tayfa, access.ModulePolls); err != nil {
			return err
		}
		if access.ReadOnly(t) {
			return apperr.ErrTayfaReadOnly
		}
		return fn(q, caller{user: user, tayfa: tayfa, m: m, now: s.now()})
	})
}

// lock locks a visible poll of tayfa.
func lock(ctx context.Context, q *store.Queries, tayfa, poll id.ID) (store.GetPollForUpdateRow, error) {
	p, err := q.GetPollForUpdate(ctx, store.GetPollForUpdateParams{TayfaID: tayfa.PG(), ID: poll.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return p, apperr.ErrPollNotFound
	case err != nil:
		return p, fmt.Errorf("anket: %w", err)
	}
	return p, nil
}

// lockOpen locks a visible poll that still takes votes.
func lockOpen(ctx context.Context, q *store.Queries, tayfa, poll id.ID, now time.Time) (store.GetPollForUpdateRow, error) {
	p, err := lock(ctx, q, tayfa, poll)
	if err != nil {
		return p, err
	}
	if closed(p.ClosesAt, p.ClosedAt, now) {
		return p, apperr.ErrPollClosed
	}
	return p, nil
}

func closed(closesAt, closedAt pgtype.Timestamptz, now time.Time) bool {
	return closedAt.Valid || (closesAt.Valid && !closesAt.Time.After(now))
}

func get(ctx context.Context, q *store.Queries, user, tayfa, poll id.ID, now time.Time) (View, error) {
	r, err := q.GetPoll(ctx, store.GetPollParams{TayfaID: tayfa.PG(), ID: poll.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return View{}, apperr.ErrPollNotFound
	case err != nil:
		return View{}, fmt.Errorf("anket: %w", err)
	}
	vs, err := views(ctx, q, user, []store.ListPollsPageRow{store.ListPollsPageRow(r)}, now)
	if err != nil {
		return View{}, err
	}
	return vs[0], nil
}

// views builds the views of rows for user: options with totals, voter
// counts, and user's own vote.
func views(ctx context.Context, q *store.Queries, user id.ID, rows []store.ListPollsPageRow, now time.Time) ([]View, error) {
	out := make([]View, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]pgtype.UUID, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	opts, err := q.ListPollOptionsWithCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("anket seçenekleri: %w", err)
	}
	byPoll := map[id.ID][]Option{}
	for _, o := range opts {
		p := id.ID(o.PollID.Bytes)
		byPoll[p] = append(byPoll[p], Option{ID: id.ID(o.ID.Bytes), Text: o.Text, Position: int(o.Position), VoteCount: o.VoteCount})
	}
	counts, err := q.CountPollVoters(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("oy veren sayısı: %w", err)
	}
	voters := map[id.ID]int64{}
	for _, c := range counts {
		voters[id.ID(c.PollID.Bytes)] = c.Voters
	}
	own, err := ownVotes(ctx, q, user, ids)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		pid := id.ID(r.ID.Bytes)
		v := View{
			ID: pid, TayfaID: id.ID(r.TayfaID.Bytes), Question: r.Question,
			Anonymous: r.Anonymous, MultipleChoice: r.MultipleChoice,
			Closed: closed(r.ClosesAt, r.ClosedAt, now), CreatedAt: r.CreatedAt.Time.UTC(),
			Options: byPoll[pid], VoterCount: voters[pid], OwnOptionIDs: []id.ID{},
		}
		if r.EventID.Valid {
			v.EventID = id.ID(r.EventID.Bytes)
		}
		if r.CreatedBy.Valid {
			v.CreatedBy = id.ID(r.CreatedBy.Bytes)
		}
		if r.ClosesAt.Valid {
			v.ClosesAt = r.ClosesAt.Time.UTC()
		}
		if r.ClosedAt.Valid {
			v.ClosedAt = r.ClosedAt.Time.UTC()
		}
		// In option order.
		for _, o := range v.Options {
			if slices.Contains(own[pid], o.ID) {
				v.OwnOptionIDs = append(v.OwnOptionIDs, o.ID)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// ownVotes returns user's own votes in polls; the query is scoped by user.
func ownVotes(ctx context.Context, q *store.Queries, user id.ID, polls []pgtype.UUID) (map[id.ID][]id.ID, error) {
	rows, err := q.ListOwnPollVotes(ctx, store.ListOwnPollVotesParams{PollIds: polls, UserID: user.PG()})
	if err != nil {
		return nil, fmt.Errorf("kendi oyu: %w", err)
	}
	out := map[id.ID][]id.ID{}
	for _, r := range rows {
		p := id.ID(r.PollID.Bytes)
		out[p] = append(out[p], id.ID(r.OptionID.Bytes))
	}
	return out, nil
}

func sameSet(a, b []id.ID) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func target(poll id.ID) txn.Target { return txn.Target{Type: subjectType, ID: poll} }

// emit writes the outbox event of a change as the last write of the
// transaction (outbox.Write). payload holds IDs and settings only.
func emit(ctx context.Context, q *store.Queries, tayfa, actor, poll id.ID, typ string, payload map[string]any) error {
	p := map[string]any{"tayfa_id": tayfa.String(), "poll_id": poll.String()}
	for k, v := range payload {
		p[k] = v
	}
	return txn.Event(ctx, q, typ, tayfa, actor, target(poll), p)
}

// parseOptionIDs reads a vote: 1–MaxOptions distinct option IDs. The
// rejected value is never reported, only the field.
func parseOptionIDs(raw []string) ([]id.ID, error) {
	if len(raw) == 0 || len(raw) > MaxOptions {
		return nil, apperr.Validation("option_ids")
	}
	out := make([]id.ID, 0, len(raw))
	for _, s := range raw {
		o, ok := id.Parse(s)
		if !ok || slices.Contains(out, o) {
			return nil, apperr.Validation("option_ids")
		}
		out = append(out, o)
	}
	return out, nil
}

// cleanOptions checks 2–10 options of 1–100 characters each, distinct
// ignoring case (Turkish rules).
func cleanOptions(raw []string) ([]string, error) {
	if len(raw) < MinOptions || len(raw) > MaxOptions {
		return nil, apperr.Validation("options")
	}
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, s := range raw {
		t, err := cleanLine(s, OptionMax, "options")
		if err != nil {
			return nil, err
		}
		k := strings.ToLowerSpecial(unicode.TurkishCase, t)
		if seen[k] {
			return nil, apperr.Validation("options")
		}
		seen[k] = true
		out = append(out, t)
	}
	return out, nil
}

// cleanLine trims surrounding spaces: 1–limit characters on one line.
func cleanLine(s string, limit int, field string) (string, error) {
	if len(s) > (limit+64)*maxRuneBytes {
		return "", apperr.Validation(field)
	}
	t := strings.TrimSpace(s)
	if n := utf8.RuneCountInString(t); n < 1 || n > limit || !utf8.ValidString(t) ||
		strings.ContainsFunc(t, unicode.IsControl) {
		return "", apperr.Validation(field)
	}
	return t, nil
}

func idString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return id.ID(u.Bytes).String()
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
