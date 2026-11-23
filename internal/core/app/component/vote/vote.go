// Package vote is the vote component (kind "vote", issue #254; ADR-0025 §4,
// §5; ADR-0037 §3): a vote tied to an event, one vote per person.
//
// The definition's source parameter decides the options (the platform
// owner dropped route_stops on 2026-11-23):
//
//	free          options the organizer writes (event_vote_options), at
//	              most MaxOptions
//	participants  the event's participants: active members who answered
//	              going or whose attendance was verified (the option's ID
//	              is the person's). The catalog refuses it in a type whose
//	              attendance is sensitive.
//	vehicles      the vehicles of the type's vehicles component (the
//	              event vehicle's ID); none while it is switched off
//	album         the photos of the event's album (the photo's ID)
//
// Only the free options are stored; the others are read from their source
// whenever the vote is read, and a vote whose target left the source (a
// participant who no longer comes) does not count. A vehicle or a photo
// that goes takes its votes with it (the migration's triggers).
//
// The ballot is secret: no Vote this package returns says who voted for
// what, to anyone and whatever their role; the caller sees their own vote.
// Votes are never audited and never reach an outbox event or a log. The
// counts and the winners show once the organizer closed the vote (for
// good) or the component froze (seven days after the event's end); before
// that every reader sees only how many voted.
//
// Who may do what (the framework's actions, package component):
//
//	read                     every active member who may see the event's
//	                         components
//	vote, withdraw           a member (not muted) who answered going or
//	                         whose attendance was verified (ByParticipant);
//	                         not for oneself, one's vehicle or one's photo
//	add or remove an option  managers (the organizer, moderators, admins),
//	                         free votes only
//	close                    managers
//
// Changes are open within the framework's write window (until seven days
// after the event's end, not on a cancelled event or a read-only tayfa) and
// while the vote is open.
//
// The celebration component reads a closed vote's winners with Load.
package vote

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompVote

// ParamSource is the definition's source parameter.
const ParamSource = "source"

// Sources (definition.VoteSources).
const (
	SourceFree         = "free"
	SourceParticipants = "participants"
	SourceVehicles     = "vehicles"
	SourceAlbum        = "album"
)

// Bounds (event_vote_options_label; the others are the app's).
const (
	// LabelMax is a free option's length in characters, after trimming.
	LabelMax = 100
	// MaxOptions bounds a free vote's options.
	MaxOptions = 20
)

// Audit actions. The records name the option only, never its label; a
// vote itself is never audited (the record would tell who voted for
// what).
const (
	AuditOptionAdded   = "event.vote_option_added"
	AuditOptionRemoved = "event.vote_option_removed"
	AuditClosed        = "event.vote_closed"
)

// maxRuneBytes is the longest UTF-8 encoding of one rune.
const maxRuneBytes = 4

// Vehicle is a vehicles-source option's vehicle; never its plate.
type Vehicle struct {
	Make      string
	Model     string
	ModelYear int32
	Color     string
}

// Option is one option of a vote.
type Option struct {
	// ID is the free option's, the participant's user ID, the event
	// vehicle's or the album photo's: what Cast takes.
	ID id.ID
	// Label is a free option's text.
	Label string
	// User is the person behind the option: the participant, the
	// vehicle's owner, the photo's uploader; zero for a free option.
	User id.ID
	// Vehicle is set for a vehicles source.
	Vehicle *Vehicle
	// Upload is an album photo's file.
	Upload id.ID
	// VoteCount and Winner (one of the most voted, with at least one vote).
	VoteCount int32
	Winner    bool
}

// Result is a vote as Load reads it, counts included: for the readers that
// build on it. It is never returned to a client as it is.
type Result struct {
	Source  string
	Options []Option
	// VoterCount is how many people voted for one of Options.
	VoterCount int32
	// Closed reports that the organizer closed the vote, at ClosedAt.
	Closed   bool
	ClosedAt time.Time
	// mine is each voter's option among Options.
	mine map[id.ID]id.ID
}

// Winners returns the most voted options of a closed vote (a tie gives
// several); none while the vote is open or nobody voted.
func (r Result) Winners() []Option {
	if !r.Closed {
		return nil
	}
	var out []Option
	for _, o := range r.Options {
		if o.Winner {
			out = append(out, o)
		}
	}
	return out
}

// Vote is one vote component of an event, as the caller sees it.
type Vote struct {
	Source string
	// Options with VoteCount and Winner only while ResultsVisible.
	Options    []Option
	VoterCount int32
	// CallerOption is the caller's own vote; zero without one.
	CallerOption id.ID
	Closed       bool
	ClosedAt     time.Time
	// ResultsVisible: closed, or the component froze.
	ResultsVisible bool
	// CallerCanVote reports whether the caller may vote now.
	CallerCanVote bool
	// CallerCanEditOptions reports whether the caller may add and remove
	// options now (a free vote).
	CallerCanEditOptions bool
	// CallerCanClose reports whether the caller may close the vote now.
	CallerCanClose bool
	MaxOptions     int32
}

// Service is the vote component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// CleanLabel trims a free option, folds its whitespace (line breaks and
// tabs included) to single spaces, and refuses an empty one, invalid
// UTF-8, other control and format characters (zero-width and direction
// marks) and more than LabelMax characters.
func CleanLabel(s string) (string, bool) {
	if len(s) > (LabelMax+64)*maxRuneBytes || !utf8.ValidString(s) {
		return "", false
	}
	if strings.IndexFunc(s, func(r rune) bool {
		if r == '\n' || r == '\r' || r == '\t' {
			return false
		}
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}) >= 0 {
		return "", false
	}
	t := strings.Join(strings.Fields(s), " ")
	if t == "" || utf8.RuneCountInString(t) > LabelMax {
		return "", false
	}
	return t, true
}

// normalized is a label's uniqueness key: Turkish lower case.
func normalized(label string) string { return strings.ToLowerSpecial(unicode.TurkishCase, label) }

// checkTerms is the one place a free option meets the abuse term check
// (ADR-0012). The term list and its check are not in core yet; when they
// come, an option is checked here and nowhere else.
func checkTerms(string) error { return nil }

// ---------------------------------------------------------------------------
// Reading

// enabled reports whether an optional component is switched on for event.
func enabled(ctx context.Context, q *store.Queries, event id.ID, c definition.Component) (bool, error) {
	if c.Optional == nil {
		return true, nil
	}
	row, err := q.GetEventComponent(ctx, store.GetEventComponentParams{EventID: event.PG(), ComponentKey: c.Key})
	if errors.Is(err, pgx.ErrNoRows) {
		return *c.Optional, nil
	}
	if err != nil {
		return false, fmt.Errorf("araçlar bileşeninin seçimi: %w", err)
	}
	return row.Enabled, nil
}

// options reads the options of the source as they are now.
func options(ctx context.Context, q *store.Queries, event id.ID, def *definition.Definition, comp definition.Component, source string) ([]Option, error) {
	out := []Option{}
	switch source {
	case SourceFree:
		rows, err := q.ListVoteOptions(ctx, store.ListVoteOptionsParams{EventID: event.PG(), ComponentKey: comp.Key})
		if err != nil {
			return nil, fmt.Errorf("oylamanın seçenekleri: %w", err)
		}
		for _, r := range rows {
			out = append(out, Option{ID: id.ID(r.ID.Bytes), Label: r.Label})
		}
	case SourceParticipants:
		users, err := q.ListVoteCandidates(ctx, event.PG())
		if err != nil {
			return nil, fmt.Errorf("oylamanın katılımcıları: %w", err)
		}
		for _, u := range users {
			out = append(out, Option{ID: id.ID(u.Bytes), User: id.ID(u.Bytes)})
		}
	case SourceVehicles:
		var vc definition.Component
		found := false
		for _, c := range def.Components {
			if c.Kind == definition.CompVehicles {
				vc, found = c, true
				break
			}
		}
		if !found {
			return out, nil
		}
		on, err := enabled(ctx, q, event, vc)
		if err != nil || !on {
			return out, err
		}
		rows, err := q.ListVoteVehicles(ctx, store.ListVoteVehiclesParams{EventID: event.PG(), ComponentKey: vc.Key})
		if err != nil {
			return nil, fmt.Errorf("oylamanın araçları: %w", err)
		}
		for _, r := range rows {
			v := &Vehicle{Make: r.Make, Model: r.Model.String, Color: r.Color.String}
			if r.ModelYear.Valid {
				v.ModelYear = r.ModelYear.Int32
			}
			out = append(out, Option{ID: id.ID(r.ID.Bytes), User: id.ID(r.UserID.Bytes), Vehicle: v})
		}
	case SourceAlbum:
		rows, err := q.ListVotePhotos(ctx, event.PG())
		if err != nil {
			return nil, fmt.Errorf("oylamanın fotoğrafları: %w", err)
		}
		for _, r := range rows {
			out = append(out, Option{ID: id.ID(r.ID.Bytes), User: id.ID(r.UploaderID.Bytes), Upload: id.ID(r.UploadID.Bytes)})
		}
	default:
		// The catalog takes no other source.
		return nil, fmt.Errorf("oylama %q: bilinmeyen kaynak %q", comp.Key, source)
	}
	return out, nil
}

// target is the option a stored vote names: the one column its source
// writes.
func target(r store.ListEventVotesRow) id.ID {
	for _, v := range []pgtype.UUID{r.OptionID, r.CandidateUserID, r.EventVehicleID, r.PhotoID} {
		if v.Valid {
			return id.ID(v.Bytes)
		}
	}
	return id.ID{}
}

// Load reads the vote comp of the definition def for event, with every
// count: for the readers that build on it (the celebration), inside their
// own transaction. Whether the vote itself is switched on is the caller's
// to check; whether the reader may see the event too. A Result's counts
// are never the client's to see before Winners returns them.
func Load(ctx context.Context, q *store.Queries, event id.ID, def *definition.Definition, comp definition.Component) (Result, error) {
	out := Result{Source: comp.Params[ParamSource].String(), mine: map[id.ID]id.ID{}}
	closedAt, err := q.GetVoteClosure(ctx, store.GetVoteClosureParams{EventID: event.PG(), ComponentKey: comp.Key})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Result{}, fmt.Errorf("oylamanın kapanışı: %w", err)
	default:
		out.Closed, out.ClosedAt = true, closedAt.Time.UTC()
	}
	if out.Options, err = options(ctx, q, event, def, comp, out.Source); err != nil {
		return Result{}, err
	}
	at := make(map[id.ID]int, len(out.Options))
	for i, o := range out.Options {
		at[o.ID] = i
	}
	votes, err := q.ListEventVotes(ctx, store.ListEventVotesParams{EventID: event.PG(), ComponentKey: comp.Key})
	if err != nil {
		return Result{}, fmt.Errorf("oylar: %w", err)
	}
	for _, v := range votes {
		t := target(v)
		i, ok := at[t]
		if !ok {
			// The target left the source (a participant who no longer
			// comes): the vote does not count.
			continue
		}
		out.Options[i].VoteCount++
		out.VoterCount++
		out.mine[id.ID(v.UserID.Bytes)] = t
	}
	var most int32
	for _, o := range out.Options {
		most = max(most, o.VoteCount)
	}
	if most > 0 {
		for i := range out.Options {
			out.Options[i].Winner = out.Options[i].VoteCount == most
		}
	}
	return out, nil
}

// writable reports whether the event, the tayfa, the window and the vote
// take changes now, apart from the caller.
func writable(sc *component.Scope, r Result) bool {
	return sc.Enabled && !r.Closed && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// participant reports whether the caller answered going or has a verified
// attendance.
func participant(ctx context.Context, sc *component.Scope) (bool, error) {
	going, attended, err := sc.Participation(ctx, sc.User)
	return going || attended, err
}

// read builds the vote as the caller sees it, inside View or Change.
func read(ctx context.Context, sc *component.Scope) (Vote, error) {
	r, err := Load(ctx, sc.Q(), sc.Event, sc.Definition, sc.Component)
	if err != nil {
		return Vote{}, err
	}
	return view(ctx, sc, r)
}

// view is r as the caller sees it: the counts only once visible, never
// anyone else's vote.
func view(ctx context.Context, sc *component.Scope, r Result) (Vote, error) {
	out := Vote{
		Source: r.Source, Options: make([]Option, 0, len(r.Options)), VoterCount: r.VoterCount,
		CallerOption: r.mine[sc.User], Closed: r.Closed, ClosedAt: r.ClosedAt,
		ResultsVisible: r.Closed || sc.Frozen(), MaxOptions: MaxOptions,
	}
	for _, o := range r.Options {
		if !out.ResultsVisible {
			o.VoteCount, o.Winner = 0, false
		}
		out.Options = append(out.Options, o)
	}
	if writable(sc, r) {
		manager := sc.IsManager()
		out.CallerCanClose = manager
		out.CallerCanEditOptions = manager && r.Source == SourceFree
		if sc.Authorize(authz.EventComponentParticipate) == nil {
			var err error
			if out.CallerCanVote, err = participant(ctx, sc); err != nil {
				return Vote{}, err
			}
		}
	}
	return out, nil
}

// Get returns the vote. Refusals are those of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Vote, error) {
	var out Vote
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		var err error
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Vote{}, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Voting

// Cast sets the caller's vote to option, replacing an earlier one; the
// same vote again changes nothing. Refused: a closed vote
// (apperr.ErrVoteClosed), no going answer or attendance
// (apperr.ErrEventNotGoing), an option that is not one now
// (apperr.ErrEventItemNotFound), oneself, one's vehicle or photo
// (apperr.ErrVoteOwnOption) and the refusals of component.Service.Change.
func (s *Service) Cast(ctx context.Context, ref component.Ref, option id.ID) (Vote, error) {
	var out Vote
	err := s.fw.Change(ctx, ref, Kind, component.ByParticipant(), func(sc *component.Scope) error {
		r, err := Load(ctx, sc.Q(), sc.Event, sc.Definition, sc.Component)
		if err != nil {
			return err
		}
		if r.Closed {
			return apperr.ErrVoteClosed
		}
		going, err := participant(ctx, sc)
		if err != nil {
			return err
		}
		if !going {
			return apperr.ErrEventNotGoing
		}
		i := -1
		for j, o := range r.Options {
			if o.ID == option {
				i = j
				break
			}
		}
		if i < 0 {
			return apperr.ErrEventItemNotFound
		}
		if r.Options[i].User == sc.User {
			return apperr.ErrVoteOwnOption
		}
		if r.mine[sc.User] != option {
			p := store.UpsertEventVoteParams{
				ID: id.New().PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(), Now: ts(sc.Now),
			}
			switch r.Source {
			case SourceFree:
				p.OptionID = option.PG()
			case SourceParticipants:
				p.CandidateUserID = option.PG()
			case SourceVehicles:
				p.EventVehicleID = option.PG()
			case SourceAlbum:
				p.PhotoID = option.PG()
			}
			if err := sc.Q().UpsertEventVote(ctx, p); err != nil {
				return fmt.Errorf("oy: %w", err)
			}
			sc.Changed()
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Vote{}, err
	}
	return out, nil
}

// Withdraw takes back the caller's vote; without one it changes nothing.
// Refused: a closed vote (apperr.ErrVoteClosed) and the refusals of
// component.Service.Change.
func (s *Service) Withdraw(ctx context.Context, ref component.Ref) (Vote, error) {
	var out Vote
	err := s.fw.Change(ctx, ref, Kind, component.ByParticipant(), func(sc *component.Scope) error {
		closed, err := isClosed(ctx, sc)
		if err != nil {
			return err
		}
		if closed {
			return apperr.ErrVoteClosed
		}
		n, err := sc.Q().DeleteEventVote(ctx, store.DeleteEventVoteParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG()})
		if err != nil {
			return fmt.Errorf("oy: %w", err)
		}
		if n > 0 {
			sc.Changed()
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Vote{}, err
	}
	return out, nil
}

// isClosed reports whether the organizer closed the vote.
func isClosed(ctx context.Context, sc *component.Scope) (bool, error) {
	_, err := sc.Q().GetVoteClosure(ctx, store.GetVoteClosureParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("oylamanın kapanışı: %w", err)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// The organizer's steps

// openFree checks that the vote takes option changes: a free vote
// (apperr.ErrVoteOptionsFixed) that is open (apperr.ErrVoteClosed).
func openFree(ctx context.Context, sc *component.Scope) error {
	if sc.Component.Params[ParamSource].String() != SourceFree {
		return apperr.ErrVoteOptionsFixed
	}
	closed, err := isClosed(ctx, sc)
	if err != nil {
		return err
	}
	if closed {
		return apperr.ErrVoteClosed
	}
	return nil
}

// AddOption adds a free option; managers only. Refused: a bad label
// (validation error on "label"), a vote that is not free
// (apperr.ErrVoteOptionsFixed), a closed vote (apperr.ErrVoteClosed), the
// same label (apperr.ErrVoteOptionExists), MaxOptions options
// (apperr.ErrEventLimit) and the refusals of component.Service.Change.
func (s *Service) AddOption(ctx context.Context, ref component.Ref, label string) (Vote, error) {
	clean, ok := CleanLabel(label)
	if !ok {
		return Vote{}, apperr.Validation("label")
	}
	if err := checkTerms(clean); err != nil {
		return Vote{}, err
	}
	var out Vote
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if err := openFree(ctx, sc); err != nil {
			return err
		}
		norm := normalized(clean)
		taken, err := sc.Q().VoteOptionLabelTaken(ctx, store.VoteOptionLabelTakenParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, LabelNormalized: norm,
		})
		if err != nil {
			return fmt.Errorf("oylamanın seçenekleri: %w", err)
		}
		if taken {
			return apperr.ErrVoteOptionExists
		}
		n, err := sc.Q().CountVoteOptions(ctx, store.CountVoteOptionsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("oylamanın seçenekleri: %w", err)
		}
		if n >= MaxOptions {
			return apperr.ErrEventLimit
		}
		pos, err := sc.Q().NextVoteOptionPosition(ctx, store.NextVoteOptionPositionParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("oylamanın seçenekleri: %w", err)
		}
		option := id.New()
		if err := sc.Q().CreateVoteOption(ctx, store.CreateVoteOptionParams{
			ID: option.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, Label: clean, LabelNormalized: norm,
			Position: pos, CreatedBy: sc.User.PG(), CreatedAt: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("oylama seçeneği: %w", err)
		}
		sc.Changed()
		if err := sc.Audit(ctx, AuditOptionAdded, map[string]any{"option_id": option.String()}); err != nil {
			return err
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Vote{}, err
	}
	return out, nil
}

// RemoveOption removes a free option and the votes for it; managers only.
// Refused: an unknown option (apperr.ErrEventItemNotFound), a vote that is
// not free (apperr.ErrVoteOptionsFixed), a closed vote
// (apperr.ErrVoteClosed) and the refusals of component.Service.Change.
func (s *Service) RemoveOption(ctx context.Context, ref component.Ref, option id.ID) (Vote, error) {
	var out Vote
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if err := openFree(ctx, sc); err != nil {
			return err
		}
		n, err := sc.Q().DeleteVoteOption(ctx, store.DeleteVoteOptionParams{EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: option.PG()})
		if err != nil {
			return fmt.Errorf("oylama seçeneği: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		sc.Changed()
		if err := sc.Audit(ctx, AuditOptionRemoved, map[string]any{"option_id": option.String()}); err != nil {
			return err
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Vote{}, err
	}
	return out, nil
}

// Close closes the vote for good; managers only. Closing a closed vote
// changes nothing. Refused: the refusals of component.Service.Change.
func (s *Service) Close(ctx context.Context, ref component.Ref) (Vote, error) {
	var out Vote
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		n, err := sc.Q().CloseVote(ctx, store.CloseVoteParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, ClosedBy: sc.User.PG(), ClosedAt: ts(sc.Now),
		})
		if err != nil {
			return fmt.Errorf("oylamayı kapatma: %w", err)
		}
		if n > 0 {
			sc.Changed()
			if err := sc.Audit(ctx, AuditClosed, map[string]any{}); err != nil {
				return err
			}
		}
		out, err = read(ctx, sc)
		return err
	})
	if err != nil {
		return Vote{}, err
	}
	return out, nil
}
