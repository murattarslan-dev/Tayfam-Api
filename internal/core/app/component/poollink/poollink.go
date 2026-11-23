// Package poollink is the question pool link component (kind "pool_link",
// issue #271; ADR-0037 §3, ADR-0040 §2): it ties an event (a problem-solving
// session) to questions of the tayfa's question pool, at most max_links of
// them (1–50, default 50), in the organizer's order.
//
// The questions stay the question pool's (package qa): answers, votes,
// edits and deletion happen there. A deleted question no longer shows here;
// a restored one shows again. The component is not shown while the tayfa
// has the question_pool module off (apperr.ErrModuleDisabled).
//
// Who may do what (the framework's actions, package component):
//
//	read    every active member who may see the event's components
//	write   managers: the organizer, and moderators and admins as for
//	        every event; until the write window closes
//
// Question texts never reach a log, an audit record or an outbox event.
package poollink

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/access"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/catalog/definition"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/component"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Kind is the component kind this package serves.
const Kind = definition.CompPoolLink

// MaxLinks is the most a component links, and its default max_links.
const MaxLinks = 50

// FieldQuestionIDs is the validation field of a list change.
const FieldQuestionIDs = "question_ids"

// AuditSet is the audit action of a list change; the record names the
// component and the number of links only.
const AuditSet = "event.pool_links_set"

// Question is a linked question as the list shows it.
type Question struct {
	ID id.ID
	// AuthorID is zero when the author's account was deleted.
	AuthorID    id.ID
	Body        string
	SubjectKey  string
	TopicKey    string
	Photos      []id.ID
	AnswerCount int32
	CreatedAt   time.Time
}

// Links is the list of one component of an event.
type Links struct {
	Questions []Question
	// MaxLinks is the component's max_links.
	MaxLinks int
	// CallerCanEdit reports whether the caller may change the list now.
	CallerCanEdit bool
}

// Service is the pool_link component, built on the component framework.
type Service struct {
	fw *component.Service
}

// New returns a Service on the framework fw.
func New(fw *component.Service) *Service { return &Service{fw: fw} }

// canEdit reports whether Set would be allowed for the caller now, apart
// from the request itself.
func canEdit(sc *component.Scope) bool {
	return sc.IsManager() && !sc.Cancelled && !sc.Frozen() && !access.ReadOnly(sc.TayfaRow())
}

// maxLinks is the component's max_links (the definition checks 1–50).
func maxLinks(sc *component.Scope) int {
	if n, ok := sc.ParamInt("max_links"); ok && n >= 1 && n <= MaxLinks {
		return int(n)
	}
	return MaxLinks
}

// Get returns the linked visible questions. Refusals are those of
// component.Service.View and apperr.ErrModuleDisabled.
func (s *Service) Get(ctx context.Context, ref component.Ref) (Links, error) {
	var out Links
	err := s.fw.View(ctx, ref, Kind, func(sc *component.Scope) error {
		if err := access.RequireModule(ctx, sc.Q(), sc.Tayfa, access.ModuleQuestionPool); err != nil {
			return err
		}
		var err error
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Links{}, err
	}
	return out, nil
}

// Set replaces the linked questions with questions, in that order; empty
// removes every link. Managers only. Refused: a repeated question or more
// than max_links (validation error on "question_ids"), a question that is
// not a visible question of the tayfa's question pool
// (apperr.ErrQuestionNotFound), and the refusals of Get and
// component.Service.Change. Setting the current list changes nothing.
func (s *Service) Set(ctx context.Context, ref component.Ref, questions []id.ID) (Links, error) {
	if len(questions) > MaxLinks {
		return Links{}, apperr.Validation(FieldQuestionIDs)
	}
	for i, q := range questions {
		if slices.Contains(questions[:i], q) {
			return Links{}, apperr.Validation(FieldQuestionIDs)
		}
	}
	var out Links
	err := s.fw.Change(ctx, ref, Kind, component.ByManager(), func(sc *component.Scope) error {
		if err := access.RequireModule(ctx, sc.Q(), sc.Tayfa, access.ModuleQuestionPool); err != nil {
			return err
		}
		if len(questions) > maxLinks(sc) {
			return apperr.Validation(FieldQuestionIDs)
		}
		q := sc.Q()
		ids := slices.Clone(questions)
		pg := make([]pgtype.UUID, 0, len(ids))
		for _, x := range ids {
			pg = append(pg, x.PG())
		}
		n, err := q.CountVisiblePoolQuestions(ctx, store.CountVisiblePoolQuestionsParams{TayfaID: sc.Tayfa.PG(), Ids: pg})
		if err != nil {
			return fmt.Errorf("havuz soruları: %w", err)
		}
		if int(n) != len(ids) {
			return apperr.ErrQuestionNotFound
		}
		prev, err := q.ListEventPoolLinkIDs(ctx, store.ListEventPoolLinkIDsParams{EventID: sc.Event.PG(), ComponentKey: sc.Key})
		if err != nil {
			return fmt.Errorf("bağlar: %w", err)
		}
		same := len(prev) == len(ids)
		for i := 0; same && i < len(ids); i++ {
			same = prev[i].Valid && id.ID(prev[i].Bytes) == ids[i]
		}
		if !same {
			if _, err := q.DeleteEventPoolLinks(ctx, store.DeleteEventPoolLinksParams{EventID: sc.Event.PG(), ComponentKey: sc.Key}); err != nil {
				return fmt.Errorf("bağları silme: %w", err)
			}
			for i, x := range ids {
				if err := q.AddEventPoolLink(ctx, store.AddEventPoolLinkParams{
					EventID: sc.Event.PG(), ComponentKey: sc.Key, QuestionID: x.PG(), Position: int16(i), //nolint:gosec // at most MaxLinks
				}); err != nil {
					return fmt.Errorf("bağ yazma: %w", err)
				}
			}
			sc.Changed()
			if err := sc.Audit(ctx, AuditSet, map[string]any{"links": len(ids)}); err != nil {
				return err
			}
		}
		out, err = load(ctx, sc)
		return err
	})
	if err != nil {
		return Links{}, err
	}
	return out, nil
}

// load reads the list as the caller sees it.
func load(ctx context.Context, sc *component.Scope) (Links, error) {
	rows, err := sc.Q().ListEventPoolLinks(ctx, store.ListEventPoolLinksParams{
		TayfaID: sc.Tayfa.PG(), EventID: sc.Event.PG(), ComponentKey: sc.Key,
	})
	if err != nil {
		return Links{}, fmt.Errorf("bağlı sorular: %w", err)
	}
	out := Links{Questions: make([]Question, 0, len(rows)), MaxLinks: maxLinks(sc), CallerCanEdit: canEdit(sc)}
	for _, r := range rows {
		v := Question{
			ID: id.ID(r.ID.Bytes), Body: r.Body, SubjectKey: r.SubjectKey.String, TopicKey: r.TopicKey.String,
			Photos: []id.ID{}, AnswerCount: r.AnswerCount, CreatedAt: r.CreatedAt.Time.UTC(),
		}
		if r.AuthorID.Valid {
			v.AuthorID = id.ID(r.AuthorID.Bytes)
		}
		for _, p := range []struct {
			ok bool
			b  [16]byte
		}{{r.Photo1UploadID.Valid, r.Photo1UploadID.Bytes}, {r.Photo2UploadID.Valid, r.Photo2UploadID.Bytes}} {
			if p.ok {
				v.Photos = append(v.Photos, id.ID(p.b))
			}
		}
		out.Questions = append(out.Questions, v)
	}
	return out, nil
}
