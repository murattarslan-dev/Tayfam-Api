// Package guest is the guest player (issue #252; ADR-0037 §6): someone
// without a Tayfam account who plays at a scoreboard table or in a bracket
// under a nickname only.
//
// A guest belongs to the event (event_guest_players), at most MaxGuests of
// them. Guests are added through a scoreboard component whose `guests`
// parameter is on (Param); without one the event has none. A guest sits at
// a player table of such a component (package scoreboard) and enters the
// event's participant draws (package draw), so the bracket that seeds from
// a draw plays guests unchanged.
//
// A guest has no profile, no personal record and no contact: no user ID,
// no notification, no statistic, no celebration and no friendship score
// ever reads one. Its nickname is content of the person who added it
// (ADR-0037 §6, the narrowest reading of "Misafir bir kişi verisi değildir"):
//
//   - the nickname stays as long as the event exists and goes with it
//     (ON DELETE CASCADE; ADR-0037 §6, decision of 2026-11-23); no job
//     clears it after the event's end;
//   - account deletion of the one who added it clears the nickname and the
//     adder at once;
//   - the export of the one who added it lists the nicknames they gave;
//   - a nickname never reaches a log, an audit record or an outbox event.
//
// Who may do what (ADR-0037 §6; the framework's roles, package component):
//
//	read     every active member who may see the event's components
//	add      the organizer (managers) and the referees assigned to the
//	         event, to the component or to the given table
//	         (authz.EventGuestAdd)
//	remove   managers; only a guest without a seat or a draw place
//
// Every change is live (event.component_changed, ADR-0025 §6) and keeps the
// write window of every component change.
package guest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind guests are added through.
const Kind = definition.CompScoreboard

// Param is the scoreboard parameter that lets a component take guests.
const Param = "guests"

// Limits (ADR-0037 §6; the CHECK on event_guest_players).
const (
	NicknameMax = 24
	MaxGuests   = 30
)

// Audit actions. The records hold the guest's ID only, never the nickname.
const (
	AuditAdded   = "event.guest_added"
	AuditRemoved = "event.guest_removed"
)

// Guest is one guest player of an event.
type Guest struct {
	ID id.ID
	// Nickname is "" once it was cleared (shown as "Guest").
	Nickname  string
	CreatedAt time.Time
}

// Guests are the guest players of an event, as the caller sees them
// through one scoreboard component.
type Guests struct {
	// Guests in the order they were added.
	Guests    []Guest
	MaxGuests int32
	// CallerCanAdd reports whether the caller may add a guest now without
	// naming a table: a manager, or a referee of the event or the
	// component. A referee of one table may add with that table.
	CallerCanAdd bool
	// CallerCanRemove reports whether the caller may remove a guest now.
	CallerCanRemove bool
}

// Input is a new guest.
type Input struct {
	Nickname string
	// Table is the scoreboard table the referee adding the guest is
	// assigned to; zero for the organizer or a referee of the event or the
	// component.
	Table id.ID
}

// Service is the guest player, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// List reads the guest players of event: for the components that seat
// them (scoreboard, draw), inside their own View or Change.
func List(ctx context.Context, q *store.Queries, event id.ID) ([]Guest, error) {
	rows, err := q.ListGuestPlayers(ctx, event.PG())
	if err != nil {
		return nil, fmt.Errorf("misafirler: %w", err)
	}
	out := make([]Guest, 0, len(rows))
	for _, r := range rows {
		out = append(out, Guest{ID: id.ID(r.ID.Bytes), Nickname: r.Nickname.String, CreatedAt: r.CreatedAt.Time.UTC()})
	}
	return out, nil
}

// IDs returns the IDs of event's guest players as a set.
func IDs(ctx context.Context, q *store.Queries, event id.ID) (map[id.ID]bool, error) {
	list, err := List(ctx, q, event)
	if err != nil {
		return nil, err
	}
	out := make(map[id.ID]bool, len(list))
	for _, g := range list {
		out[g.ID] = true
	}
	return out, nil
}

// maxRuneBytes is the longest UTF-8 encoding of one rune.
const maxRuneBytes = 4

// CleanNickname trims a nickname and folds its inner spaces to one, and
// refuses an empty one, invalid UTF-8, control and format characters
// (zero-width and direction marks) and more than NicknameMax characters.
func CleanNickname(s string) (string, bool) {
	if len(s) > (NicknameMax+64)*maxRuneBytes || !utf8.ValidString(s) {
		return "", false
	}
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return "", false
	}
	t := strings.Join(strings.Fields(s), " ")
	if t == "" || utf8.RuneCountInString(t) > NicknameMax {
		return "", false
	}
	return t, true
}

// checkTerms is the one place a nickname meets the abuse term check
// (ADR-0037 §6: "Takma ad kötüye kullanım filtresinden geçer"; ADR-0012).
// The term list (moderation_terms) and its check are not in core yet; when
// they come, a nickname is checked here and nowhere else.
func checkTerms(string) error { return nil }

// writable is what a change needs besides the caller's role: the checks
// Change makes.
func writable(sc *component.Scope) bool {
	return sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// denied reports whether err is a refusal of the caller's role.
func denied(err error) bool {
	return errors.Is(err, apperr.ErrPermissionDenied) || errors.Is(err, apperr.ErrMuted)
}

// load reads the guests as the caller sees them.
func load(ctx context.Context, sc *component.Scope) (Guests, error) {
	list, err := List(ctx, sc.Q(), sc.Event)
	if err != nil {
		return Guests{}, err
	}
	out := Guests{Guests: list, MaxGuests: MaxGuests}
	if !writable(sc) {
		return out, nil
	}
	out.CallerCanRemove = sc.IsManager()
	if sc.Authorize(authz.EventGuestAdd) == nil {
		switch err := sc.AuthorizeReferee(ctx, id.ID{}); {
		case err == nil:
			out.CallerCanAdd = true
		case !denied(err):
			return Guests{}, err
		}
	}
	return out, nil
}

// guestsOn refuses a component that does not take guests.
func guestsOn(sc *component.Scope) error {
	if !sc.ParamBool(Param) {
		return apperr.ErrGuestPlayersDisabled
	}
	return nil
}

// Get returns the event's guests through the scoreboard component of ref.
// Refused: a component without guests (apperr.ErrGuestPlayersDisabled) and
// the refusals of component.Service.View.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Guests, error) {
	var out Guests
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		if err := guestsOn(sc); err != nil {
			return err
		}
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Guests{}, err
	}
	return out, nil
}

// Add adds a guest player. Refused: a bad nickname (validation error on
// nickname), a table the component does not have
// (apperr.ErrScoreTableNotFound), a component without guests
// (apperr.ErrGuestPlayersDisabled), a caller who is neither a manager nor a
// referee of the event, the component or the table
// (apperr.ErrPermissionDenied), MaxGuests guests (apperr.ErrEventLimit) and
// the refusals of component.Service.Change.
func (s *Service) Add(ctx context.Context, ref component.Ref, in Input) (Guests, error) {
	nickname, ok := CleanNickname(in.Nickname)
	if !ok {
		return Guests{}, apperr.Validation("nickname")
	}
	if err := checkTerms(nickname); err != nil {
		return Guests{}, err
	}
	var out Guests
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventGuestAdd), func(sc *component.Scope) error {
		if err := guestsOn(sc); err != nil {
			return err
		}
		q := sc.Q()
		if in.Table != (id.ID{}) {
			found, err := q.GuestScoreTableExists(ctx, store.GuestScoreTableExistsParams{
				ID: in.Table.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
			})
			if err != nil {
				return fmt.Errorf("masa: %w", err)
			}
			if !found {
				return apperr.ErrScoreTableNotFound
			}
		}
		if err := sc.AuthorizeReferee(ctx, in.Table); err != nil {
			return err
		}
		// Change locks the event: two adds cannot pass the count together.
		n, err := q.CountGuestPlayers(ctx, sc.Event.PG())
		if err != nil {
			return fmt.Errorf("misafir sayısı: %w", err)
		}
		if n >= MaxGuests {
			return apperr.ErrEventLimit
		}
		g := id.New()
		if err := q.AddGuestPlayer(ctx, store.AddGuestPlayerParams{
			ID: g.PG(), EventID: sc.Event.PG(), Nickname: pgtype.Text{String: nickname, Valid: true},
			CreatedBy: sc.User.PG(), CreatedAt: pgtype.Timestamptz{Time: sc.Now, Valid: true},
		}); err != nil {
			return fmt.Errorf("misafir: %w", err)
		}
		return done(ctx, sc, AuditAdded, g, &out)
	})
	if err != nil {
		return Guests{}, err
	}
	return out, nil
}

// Remove removes a guest player who has no seat at a table and no place in
// a draw; managers only. Refused: a guest the event does not have
// (apperr.ErrGuestPlayerNotFound), a guest with a seat or a draw place
// (apperr.ErrGuestPlayerInUse), a component without guests
// (apperr.ErrGuestPlayersDisabled) and the refusals of
// component.Service.Change.
func (s *Service) Remove(ctx context.Context, ref component.Ref, guest id.ID) (Guests, error) {
	var out Guests
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if err := guestsOn(sc); err != nil {
			return err
		}
		q := sc.Q()
		used, err := q.GuestPlayerInUse(ctx, store.GuestPlayerInUseParams{ID: guest.PG(), EventID: sc.Event.PG()})
		if err != nil {
			return fmt.Errorf("misafirin yerleri: %w", err)
		}
		if used.Bool {
			return apperr.ErrGuestPlayerInUse
		}
		n, err := q.DeleteGuestPlayer(ctx, store.DeleteGuestPlayerParams{ID: guest.PG(), EventID: sc.Event.PG()})
		if err != nil {
			return fmt.Errorf("misafiri silme: %w", err)
		}
		if n == 0 {
			return apperr.ErrGuestPlayerNotFound
		}
		return done(ctx, sc, AuditRemoved, guest, &out)
	})
	if err != nil {
		return Guests{}, err
	}
	return out, nil
}

// done finishes a change: live update, audit record (the guest's ID only)
// and the guests as the caller sees them now.
func done(ctx context.Context, sc *component.Scope, action string, guest id.ID, out *Guests) error {
	sc.Changed()
	if err := sc.Audit(ctx, action, map[string]any{"guest_id": guest.String()}); err != nil {
		return err
	}
	var err error
	*out, err = load(ctx, sc)
	return err
}
