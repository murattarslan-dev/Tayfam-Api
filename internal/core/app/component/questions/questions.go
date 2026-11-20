// Package questions is the questions component (kind "questions", issue
// #256; ADR-0025 §4, §5): live questions at a talk, which participants ask
// and vote for and the organizer marks answered. It is a component of its
// own (the platform owner's decision), apart from the tayfa's Q&A module.
//
// Who may do what (the permission table, package authz):
//
//	read             every active member who may see the event's components
//	ask, vote        a member (not muted) who answered going or whose
//	                 attendance was verified (authz.EventQuestionAsk)
//	remove own       the author (authz.EventQuestionAsk), within the write
//	                 window
//	remove anyone's  moderators and admins (authz.EventQuestionDeleteAny),
//	                 also on cancelled, frozen and switched-off components:
//	                 moderation
//	mark answered    the organizer, moderators and admins (ByManager)
//
// Changes are open within the framework's write window: until seven days
// after the event's end (ADR-0025 §5), not on a cancelled event or a
// read-only tayfa.
//
// Anonymous questions (the component's `anonymous` parameter): the author
// is kept in event_questions.author_id, on the server only. No Question this
// package returns carries the author of an anonymous question, to anyone,
// the tayfa's moderators and admins included; the author learns only that
// the question is theirs (CallerIsAuthor). Neither asking nor the author's
// own removal of an anonymous question writes an audit record, which the
// tayfa's admins read. A report about one keeps its author for the platform
// only (package report, reports.subject_hidden). Question texts never reach
// a log, an audit record or an outbox event.
package questions

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
const Kind = definition.CompQuestions

// ParamAnonymous is the component's parameter that allows anonymous
// questions.
const ParamAnonymous = "anonymous"

// Bounds (event_questions_body; the others are the app's).
const (
	// BodyMax is a question's length in characters, after trimming.
	BodyMax = 500
	// MaxPerPerson bounds one person's live questions in one component.
	MaxPerPerson = 20
	// MaxQuestions bounds a component's live questions.
	MaxQuestions = 500
)

// Audit actions of the component's changes. The records name the question
// only, never its text; asking and the author's own removal of an anonymous
// question are not audited (the record would name its author).
const (
	AuditAsked      = "event.question_asked"
	AuditRemoved    = "event.question_removed"
	AuditAnswered   = "event.question_answered"
	AuditUnanswered = "event.question_unanswered"
)

// maxRuneBytes is the longest UTF-8 encoding of one rune.
const maxRuneBytes = 4

// Input is a new question.
type Input struct {
	Body string
	// Anonymous asks without the author being shown; only where the
	// component allows it.
	Anonymous bool
	// ClientRequestID makes a repeat return the first question.
	ClientRequestID string
}

// Question is a question as the caller sees it.
type Question struct {
	ID   id.ID
	Body string
	// Anonymous: Author is zero, whoever asks.
	Anonymous bool
	// Author is zero for an anonymous question.
	Author id.ID
	// CallerIsAuthor reports whether the caller asked the question.
	CallerIsAuthor bool
	VoteCount      int32
	CallerVoted    bool
	// AnsweredAt is zero while the question is not marked answered.
	AnsweredAt time.Time
	CreatedAt  time.Time
	// CallerCanRemove reports whether the caller may remove the question
	// now.
	CallerCanRemove bool
}

// List is a component's questions, unanswered first, then by votes (most
// first), then oldest first, with what the caller may do.
type List struct {
	Questions []Question
	// AnonymousAllowed is the component's anonymous parameter.
	AnonymousAllowed bool
	// CallerCanAsk reports whether the caller may ask and vote now (the
	// write window, the permission and going or a verified attendance; not
	// the limits).
	CallerCanAsk bool
	// CallerCanMarkAnswered reports whether the caller may mark questions
	// answered now.
	CallerCanMarkAnswered bool
	// MaxPerPerson bounds one person's live questions.
	MaxPerPerson int32
}

// Service is the questions component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// CleanBody trims a question, folds its whitespace (line breaks and tabs
// included) to single spaces, and refuses an empty one, invalid UTF-8,
// other control and format characters (zero-width and direction marks) and
// more than BodyMax characters.
func CleanBody(s string) (string, bool) {
	if len(s) > (BodyMax+64)*maxRuneBytes || !utf8.ValidString(s) {
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
	if t == "" || utf8.RuneCountInString(t) > BodyMax {
		return "", false
	}
	return t, true
}

// checkTerms is the one place a question meets the abuse term check
// (ADR-0012). The term list and its check are not in core yet; when they
// come, a question is checked here and nowhere else.
func checkTerms(string) error { return nil }

// writable reports whether the event, the tayfa and the write window allow
// changes now: the checks component.Service.Change makes.
func writable(sc *component.Scope) bool {
	return sc.Enabled && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// participant reports whether the caller answered going or has a verified
// attendance ("Soru gönder / oyla: Katılımcı", ADR-0025 §5).
func participant(ctx context.Context, sc *component.Scope) (bool, error) {
	going, attended, err := sc.Participation(ctx, sc.User)
	return going || attended, err
}

// rights is what the caller may do to questions now.
type rights struct {
	// own: remove one's own questions (the write window and the
	// permission).
	own bool
	// anyone: remove anyone else's (moderation).
	anyone bool
}

func rightsOf(sc *component.Scope) rights {
	return rights{
		own:    writable(sc) && sc.Authorize(authz.EventQuestionAsk) == nil,
		anyone: sc.Authorize(authz.EventQuestionDeleteAny) == nil && !access.ReadOnly(sc.TayfaRow()),
	}
}

// questionOf is a stored question as the caller sees it: never the author
// of an anonymous one.
func questionOf(sc *component.Scope, r store.ListEventQuestionsRow, can rights) Question {
	author := id.ID(r.AuthorID.Bytes)
	mine := author == sc.User
	q := Question{
		ID: id.ID(r.ID.Bytes), Body: r.Body, Anonymous: r.Anonymous, CallerIsAuthor: mine,
		VoteCount: r.VoteCount, CallerVoted: r.CallerVoted, CreatedAt: r.CreatedAt.Time.UTC(),
		CallerCanRemove: (mine && can.own) || (!mine && can.anyone),
	}
	if !r.Anonymous {
		q.Author = author
	}
	if r.AnsweredAt.Valid {
		q.AnsweredAt = r.AnsweredAt.Time.UTC()
	}
	return q
}

// get reads a live question of the component; apperr.ErrEventItemNotFound
// when there is none.
func get(ctx context.Context, sc *component.Scope, question id.ID) (store.ListEventQuestionsRow, error) {
	r, err := sc.Q().GetEventQuestion(ctx, store.GetEventQuestionParams{
		ViewerID: sc.User.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: question.PG(),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return store.ListEventQuestionsRow{}, apperr.ErrEventItemNotFound
	case err != nil:
		return store.ListEventQuestionsRow{}, fmt.Errorf("soru: %w", err)
	}
	return store.ListEventQuestionsRow(r), nil
}

// List returns the component's questions. Refused: the refusals of
// component.Service.View.
func (s *Service) List(ctx context.Context, ref component.Ref) (List, error) {
	var out List
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		rows, err := sc.Q().ListEventQuestions(ctx, store.ListEventQuestionsParams{
			ViewerID: sc.User.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
		})
		if err != nil {
			return fmt.Errorf("sorular: %w", err)
		}
		can := rightsOf(sc)
		out = List{
			Questions: make([]Question, 0, len(rows)), AnonymousAllowed: sc.ParamBool(ParamAnonymous),
			CallerCanMarkAnswered: writable(sc) && sc.IsManager(), MaxPerPerson: MaxPerPerson,
		}
		for _, r := range rows {
			out.Questions = append(out.Questions, questionOf(sc, r, can))
		}
		if can.own {
			if out.CallerCanAsk, err = participant(ctx, sc); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return List{}, err
	}
	return out, nil
}

// Ask asks a question; the caller must have answered going or have a
// verified attendance. A repeat with the same ClientRequestID returns the
// first question. Refused: a bad body, an anonymous question where the
// component does not allow it, a bad client_request_id (validation errors
// on those fields), no going answer or attendance (apperr.ErrEventNotGoing),
// the caller's MaxPerPerson or the component's MaxQuestions live questions
// (apperr.ErrEventLimit) and the refusals of component.Service.Change.
func (s *Service) Ask(ctx context.Context, ref component.Ref, in Input) (Question, error) {
	body, ok := CleanBody(in.Body)
	if !ok {
		return Question{}, apperr.Validation("body")
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return Question{}, apperr.Validation("client_request_id")
	}
	if err := checkTerms(body); err != nil {
		return Question{}, err
	}
	var out Question
	err := s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventQuestionAsk), func(sc *component.Scope) error {
		prev, err := sc.Q().GetEventQuestionByClientRequest(ctx, store.GetEventQuestionByClientRequestParams{
			AuthorID: sc.User.PG(), ClientRequestID: crid.PG(),
		})
		switch {
		case err == nil:
			if prev.DeletedAt.Valid || id.ID(prev.EventID.Bytes) != sc.Event || prev.ComponentKey != sc.Key {
				return apperr.Validation("client_request_id")
			}
			r, gerr := get(ctx, sc, id.ID(prev.ID.Bytes))
			if gerr != nil {
				return gerr
			}
			out = questionOf(sc, r, rightsOf(sc))
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}
		if in.Anonymous && !sc.ParamBool(ParamAnonymous) {
			return apperr.Validation("anonymous")
		}
		going, err := participant(ctx, sc)
		if err != nil {
			return err
		}
		if !going {
			return apperr.ErrEventNotGoing
		}
		mine, err := sc.Q().CountUserEventQuestions(ctx, store.CountUserEventQuestionsParams{
			EventID: sc.Event.PG(), ComponentKey: sc.Key, AuthorID: sc.User.PG(),
		})
		if err != nil {
			return fmt.Errorf("sorular: %w", err)
		}
		all, err := sc.Q().CountEventQuestions(ctx, store.CountEventQuestionsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("sorular: %w", err)
		}
		if mine >= MaxPerPerson || all >= MaxQuestions {
			return apperr.ErrEventLimit
		}
		question := id.New()
		if err := sc.Q().CreateEventQuestion(ctx, store.CreateEventQuestionParams{
			ID: question.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, AuthorID: sc.User.PG(),
			Anonymous: in.Anonymous, Body: body, ClientRequestID: crid.PG(), CreatedAt: ts(sc.Now),
		}); err != nil {
			return fmt.Errorf("soru: %w", err)
		}
		sc.Changed()
		if !in.Anonymous {
			if err := sc.Audit(ctx, AuditAsked, map[string]any{"question_id": question.String()}); err != nil {
				return err
			}
		}
		out = Question{
			ID: question, Body: body, Anonymous: in.Anonymous, CallerIsAuthor: true, CreatedAt: sc.Now,
			CallerCanRemove: true,
		}
		if !in.Anonymous {
			out.Author = sc.User
		}
		return nil
	})
	if err != nil {
		return Question{}, err
	}
	return out, nil
}

// Remove removes a question (soft delete; kept for 30 days, then purged
// with its votes). The author removes their own within the write window
// (authz.EventQuestionAsk); moderators and admins remove anyone's, an
// anonymous one included, whatever the event's state
// (authz.EventQuestionDeleteAny), as moderation. Refused: an unknown
// question (apperr.ErrEventItemNotFound) and the refusals of
// component.Service.View and Change.
func (s *Service) Remove(ctx context.Context, ref component.Ref, question id.ID) error {
	var (
		author    id.ID
		anonymous bool
	)
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		r, err := get(ctx, sc, question)
		if err != nil {
			return err
		}
		author, anonymous = id.ID(r.AuthorID.Bytes), r.Anonymous
		return nil
	})
	if errors.Is(err, apperr.ErrComponentDisabled) {
		// A switched-off component still has questions moderators may
		// remove; the author's own removal is refused below by Change.
		author = id.ID{}
	} else if err != nil {
		return err
	}
	own := author == ref.User
	who := component.BySafetySignal(authz.EventQuestionDeleteAny)
	if own {
		who = component.ByAction(authz.EventQuestionAsk)
	}
	return s.fw.Change(ctx, ref, Kind, who, func(sc *component.Scope) error {
		n, err := sc.Q().DeleteEventQuestion(ctx, store.DeleteEventQuestionParams{
			DeletedAt: ts(sc.Now), DeletedBy: sc.User.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: question.PG(),
		})
		if err != nil {
			return fmt.Errorf("soru: %w", err)
		}
		if n == 0 {
			return apperr.ErrEventItemNotFound
		}
		sc.Changed()
		if own && anonymous {
			return nil
		}
		return sc.Audit(ctx, AuditRemoved, map[string]any{"question_id": question.String()})
	})
}

// SetVote adds (voted) or takes back the caller's vote for a question;
// doing what is already done is not an error. Voting needs going or a
// verified attendance (apperr.ErrEventNotGoing); taking a vote back does
// not. Refused: an unknown question (apperr.ErrEventItemNotFound) and the
// refusals of component.Service.Change.
func (s *Service) SetVote(ctx context.Context, ref component.Ref, question id.ID, voted bool) error {
	return s.fw.Change(ctx, ref, Kind, component.ByAction(authz.EventQuestionAsk), func(sc *component.Scope) error {
		if _, err := get(ctx, sc, question); err != nil {
			return err
		}
		var n int64
		if voted {
			going, err := participant(ctx, sc)
			if err != nil {
				return err
			}
			if !going {
				return apperr.ErrEventNotGoing
			}
			if n, err = sc.Q().CreateEventQuestionVote(ctx, store.CreateEventQuestionVoteParams{
				QuestionID: question.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(), CreatedAt: ts(sc.Now),
			}); err != nil {
				return fmt.Errorf("oy: %w", err)
			}
		} else {
			var err error
			if n, err = sc.Q().DeleteEventQuestionVote(ctx, store.DeleteEventQuestionVoteParams{
				QuestionID: question.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, UserID: sc.User.PG(),
			}); err != nil {
				return fmt.Errorf("oy: %w", err)
			}
		}
		if n > 0 {
			sc.Changed()
		}
		return nil
	})
}

// SetAnswered marks a question answered or takes the mark back; doing what
// is already done is not an error. The organizer, moderators and admins
// (ByManager). Refused: an unknown question (apperr.ErrEventItemNotFound)
// and the refusals of component.Service.Change.
func (s *Service) SetAnswered(ctx context.Context, ref component.Ref, question id.ID, answered bool) error {
	return s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if _, err := get(ctx, sc, question); err != nil {
			return err
		}
		var (
			n      int64
			err    error
			action = AuditAnswered
		)
		if answered {
			n, err = sc.Q().MarkEventQuestionAnswered(ctx, store.MarkEventQuestionAnsweredParams{
				AnsweredAt: ts(sc.Now), AnsweredBy: sc.User.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: question.PG(),
			})
		} else {
			action = AuditUnanswered
			n, err = sc.Q().UnmarkEventQuestionAnswered(ctx, store.UnmarkEventQuestionAnsweredParams{
				EventID: sc.Event.PG(), ComponentKey: sc.Key, ID: question.PG(),
			})
		}
		if err != nil {
			return fmt.Errorf("soru: %w", err)
		}
		if n == 0 {
			return nil
		}
		sc.Changed()
		return sc.Audit(ctx, action, map[string]any{"question_id": question.String()})
	})
}
