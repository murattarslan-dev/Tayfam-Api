// Package qa is a tayfa's questions and answers: helpful votes, tags and
// Turkish full-text search (QAService, proto/tayfam/core/v1/qa.proto; data
// model v14 "Soru-Cevap", "Türkçe büyük/küçük harf", "Silinen içeriği geri
// alma", "Çevrimdışı yazma"; issue #59). There is no accepted answer;
// answers are ordered by helpful votes.
//
// Every method starts the same way as the announcement package: the caller
// comes from the verified token, access.Load checks for an active
// membership (anyone else gets apperr.ErrTayfaNotFound), access.RequireModule
// refuses every method, reads included, while the tayfa has the qa module
// turned off, and access.Authorize asks the permission table. Every active
// member asks, answers and votes; changing someone else's question or
// answer needs a moderator above the author's current role
// (authz.CanActOnContent).
//
// Writes run in txn.WithActor with the tayfa row locked; that lock also
// serialises votes, so answers.vote_count always equals the number of
// answer_votes rows. Moderation (someone else's content, restores, tag
// removal) is written to audit_logs; every change ends with an outbox event.
// Neither ever holds a title, a body, a tag name or a search query; only IDs
// and the kind of change.
package qa

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

// Limits (veri modeli v14 "Uzunluk sınırları"; the same CHECKs are in the
// database).
const (
	TitleMin   = 10
	TitleMax   = 150
	BodyMax    = 10000
	AnswerMax  = 10000
	TagNameMax = 30
	// MaxQuestionTags is how many tags one question carries at most.
	MaxQuestionTags = 5
	// MaxTayfaTags bounds a tayfa's tags: every member can open new ones.
	MaxTayfaTags = 200
	// QueryMax is the longest search text.
	QueryMax = 100
	// RestoreWindow is how long a moderator's or admin's deletion can be
	// undone (v14 "Silinen içeriği geri alma").
	RestoreWindow = 30 * 24 * time.Hour
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// Update mask paths (UpdateQuestionRequest.update_mask).
const (
	FieldTitle = "title"
	FieldBody  = "body"
	FieldTags  = "tags"
)

// Event types. Payloads hold IDs and the kind of change only.
const (
	EventQuestionCreated = "question.created"
	EventQuestionUpdated = "question.updated"
	EventQuestionDeleted = "question.deleted"
	// EventAnswerCreated carries question_author_id, the recipient of the
	// answer_received notification (empty when the account was deleted).
	EventAnswerCreated = "answer.created"
	EventAnswerUpdated = "answer.updated"
	EventAnswerDeleted = "answer.deleted"
	// EventAnswerVoted: change is "added" or "removed".
	EventAnswerVoted = "answer.voted"
	EventTagDeleted  = "tag.deleted"
)

// Kinds of change in update events, also audit action suffixes.
const (
	changeEdited   = "edited"
	changeRestored = "restored"
	changeAdded    = "added"
	changeRemoved  = "removed"
)

const (
	subjectQuestion = "question"
	subjectAnswer   = "answer"
	subjectTag      = "tag"
)

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
}

// Service implements QAService's business logic.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("qa: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// Tag is a tayfa's tag.
type Tag struct {
	ID   id.ID
	Name string
	// QuestionCount is only set by ListTags.
	QuestionCount int32
}

// Question is a question as the tayfa's active members see it.
type Question struct {
	ID      id.ID
	TayfaID id.ID
	// AuthorID is zero when the author's account was deleted.
	AuthorID    id.ID
	Title       string
	Body        string
	Tags        []Tag
	AnswerCount int32
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Answer is an answer as the tayfa's active members see it.
type Answer struct {
	ID         id.ID
	QuestionID id.ID
	TayfaID    id.ID
	// AuthorID is zero when the author's account was deleted.
	AuthorID  id.ID
	Body      string
	VoteCount int32
	// Voted reports whether the caller voted for the answer.
	Voted     bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// read checks a reader: active member, module on, TayfaView.
func (s *Service) read(ctx context.Context, user, tayfa id.ID) error {
	_, m, err := access.Load(ctx, s.q, tayfa, user, false)
	if err != nil {
		return err
	}
	if err := access.RequireModule(ctx, s.q, tayfa, access.ModuleQA); err != nil {
		return err
	}
	return access.Authorize(m, authz.TayfaView, s.now())
}

// writer is what a write's fn gets: the transaction's queries and the
// caller's standing, to authorize once the action is known (own or any).
type writer struct {
	q   *store.Queries
	m   authz.Membership
	t   access.Tayfa
	now time.Time
}

// authorize checks the permission table and that the tayfa is not
// read-only.
func (w writer) authorize(a authz.Action) error {
	if err := access.Authorize(w.m, a, w.now); err != nil {
		return err
	}
	if access.ReadOnly(w.t) {
		return apperr.ErrTayfaReadOnly
	}
	return nil
}

// write locks the tayfa, checks membership and module, then runs fn in the
// same transaction. fn authorizes with w.authorize before it changes
// anything. With a, the action is checked before fn runs.
func (s *Service) write(ctx context.Context, user, tayfa id.ID, a authz.Action, fn func(w writer) error) error {
	now := s.now()
	return txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		t, m, err := access.Load(ctx, q, tayfa, user, true)
		if err != nil {
			return err
		}
		if err := access.RequireModule(ctx, q, tayfa, access.ModuleQA); err != nil {
			return err
		}
		w := writer{q: q, m: m, t: t, now: now}
		if a != 0 {
			if err := w.authorize(a); err != nil {
				return err
			}
		}
		return fn(w)
	})
}

// isAuthor reports whether user wrote content whose author column is
// author.
func isAuthor(author pgtype.UUID, user id.ID) bool {
	return author.Valid && id.ID(author.Bytes) == user
}

// mayChange applies the hierarchy to someone else's content: the caller's
// role must be above the author's current role in this tayfa. An author who
// is no longer an active member has no role here.
func mayChange(ctx context.Context, q *store.Queries, tayfa id.ID, m authz.Membership, author pgtype.UUID) error {
	var role authz.Role
	if author.Valid {
		row, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: author})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return fmt.Errorf("yazarın üyeliği: %w", err)
		case row.Status == string(authz.StatusActive):
			if role, err = authz.ParseRole(row.Role); err != nil {
				return fmt.Errorf("yazarın rolü: %w", err)
			}
		}
	}
	if !authz.CanActOnContent(m.Role, role) {
		return apperr.ErrRoleHierarchy
	}
	return nil
}

// authorizeOn picks the own or any action for content by author and
// checks it, with the hierarchy for someone else's content.
func (w writer) authorizeOn(ctx context.Context, tayfa, user id.ID, author pgtype.UUID, own, anyone authz.Action) error {
	if isAuthor(author, user) {
		return w.authorize(own)
	}
	if err := w.authorize(anyone); err != nil {
		return err
	}
	return mayChange(ctx, w.q, tayfa, w.m, author)
}

// record writes the outbox event of a change and, with audit set, the audit
// record first (outbox.Write must be last). payload and auditMeta hold IDs
// and classification only.
func record(ctx context.Context, q *store.Queries, tayfa, actor id.ID, target txn.Target, event string, payload map[string]any, audit string, auditMeta map[string]any) error {
	if audit != "" {
		if err := txn.Audit(ctx, q, tayfa, actor, audit, target, auditMeta); err != nil {
			return err
		}
	}
	p := map[string]any{"tayfa_id": tayfa.String()}
	for k, v := range payload {
		p[k] = v
	}
	return txn.Event(ctx, q, event, tayfa, actor, target, p)
}

// cleanTitle trims surrounding spaces: 10–150 characters on one line.
func cleanTitle(s string) (string, error) {
	if len(s) > (TitleMax+64)*maxRuneBytes {
		return "", apperr.Validation(FieldTitle)
	}
	t := strings.TrimSpace(s)
	if n := utf8.RuneCountInString(t); n < TitleMin || n > TitleMax || !validText(t, false) {
		return "", apperr.Validation(FieldTitle)
	}
	return t, nil
}

// cleanBody trims surrounding spaces: up to max characters; min is 0 or 1.
func cleanBody(s, field string, minLen, maxLen int) (string, error) {
	if len(s) > (maxLen+64)*maxRuneBytes {
		return "", apperr.Validation(field)
	}
	b := strings.TrimSpace(s)
	if n := utf8.RuneCountInString(b); n < minLen || n > maxLen || !validText(b, true) {
		return "", apperr.Validation(field)
	}
	return b, nil
}

// tagName is a cleaned tag name and its Turkish case folded form.
type tagName struct {
	name, normalized string
}

// cleanTags trims each name, joins inner runs of spaces, and folds it with
// Turkish case rules (v14 "Türkçe büyük/küçük harf"): "ÇADIR", "Çadır" and
// "çadır" are one tag; "IŞIK" is "ışık" and "İzmir" is "izmir". Names that
// fold the same count once; the first spelling is kept.
func cleanTags(in []string) ([]tagName, error) {
	if len(in) > MaxQuestionTags {
		return nil, apperr.Validation(FieldTags)
	}
	out := make([]tagName, 0, len(in))
	for _, raw := range in {
		if len(raw) > (TagNameMax+64)*maxRuneBytes || !validText(raw, false) {
			return nil, apperr.Validation(FieldTags)
		}
		name := strings.Join(strings.Fields(raw), " ")
		norm := strings.ToLowerSpecial(unicode.TurkishCase, name)
		if n := utf8.RuneCountInString(name); n < 1 || n > TagNameMax || utf8.RuneCountInString(norm) > TagNameMax {
			return nil, apperr.Validation(FieldTags)
		}
		if !slices.ContainsFunc(out, func(t tagName) bool { return t.normalized == norm }) {
			out = append(out, tagName{name: name, normalized: norm})
		}
	}
	return out, nil
}

// validText refuses invalid UTF-8 and control characters; multiline text
// may contain line breaks and tabs.
func validText(s string, multiline bool) bool {
	if !utf8.ValidString(s) {
		return false
	}
	return !slices.ContainsFunc([]rune(s), func(r rune) bool {
		lineBreak := r == '\n' || r == '\r' || r == '\t'
		return unicode.IsControl(r) && (!multiline || !lineBreak)
	})
}

func idString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	return id.ID(u.Bytes).String()
}

func optional(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	return id.ID(u.Bytes)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
