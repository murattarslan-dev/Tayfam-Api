package qa

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Paging (qa.proto): page_size 0 means 20, above 100 means 100, negative
// is a validation error.
const (
	defaultPageSize = 20
	maxPageSize     = 100

	cursorQuestions = "questions"
	cursorSearch    = "question_search"
	cursorAnswers   = "answers"
)

// cursor is the position after the last row of a page; the page token is
// its JSON form in base64url. It carries the tayfa and a digest of the
// request it was made for (question, query, tag), never the search text.
type cursor struct {
	Method string `json:"m"`
	Tayfa  string `json:"t"`
	// Scope is a digest of the request's other parameters.
	Scope string `json:"s,omitempty"`
	// Micros is created_at in Unix microseconds (Postgres precision).
	Micros int64 `json:"c,omitempty"`
	// Rank is the float32 bits of the search rank.
	Rank uint32 `json:"r,omitempty"`
	// Votes is the answer's vote_count.
	Votes int32  `json:"v,omitempty"`
	ID    string `json:"i"`
}

// ListQuestionsInput selects questions.
type ListQuestionsInput struct {
	// Query is optional search text (Turkish full-text, İ/ı aware). It is
	// never logged or recorded.
	Query string
	// TagID is an optional tag filter; empty for none.
	TagID     string
	PageSize  int32
	PageToken string
}

// ListQuestions lists visible questions: newest first, or most relevant
// first with a query.
func (s *Service) ListQuestions(ctx context.Context, user, tayfa id.ID, in ListQuestionsInput) ([]Question, string, error) {
	size, err := normalizePageSize(in.PageSize)
	if err != nil {
		return nil, "", err
	}
	if len(in.Query) > (QueryMax+64)*maxRuneBytes {
		return nil, "", apperr.Validation("query")
	}
	query := strings.TrimSpace(in.Query)
	if utf8.RuneCountInString(query) > QueryMax || !validText(query, false) {
		return nil, "", apperr.Validation("query")
	}
	var tag id.ID
	hasTag := in.TagID != ""
	if hasTag {
		var ok bool
		if tag, ok = id.Parse(in.TagID); !ok {
			return nil, "", apperr.Validation("tag_id")
		}
	}
	method := cursorQuestions
	if query != "" {
		method = cursorSearch
	}
	scope := digest(query, in.TagID)
	var c cursor
	var after id.ID
	if in.PageToken != "" {
		var ok bool
		if c, after, ok = decodeCursor(in.PageToken, method, tayfa, scope); !ok {
			return nil, "", apperr.Validation("page_token")
		}
	}
	if err := s.read(ctx, user, tayfa); err != nil {
		return nil, "", err
	}
	if hasTag {
		if err := requireTag(ctx, s.q, tayfa, tag); err != nil {
			return nil, "", err
		}
	}

	var (
		out  []Question
		next cursor
	)
	if query == "" {
		p := store.ListQuestionsPageParams{TayfaID: tayfa.PG(), HasTag: hasTag, TagID: tag.PG(), PageSize: size}
		if in.PageToken != "" {
			p.HasCursor, p.AfterID = true, after.PG()
			p.AfterCreatedAt = pgtype.Timestamptz{Time: time.UnixMicro(c.Micros).UTC(), Valid: true}
		}
		rows, err := s.q.ListQuestionsPage(ctx, p)
		if err != nil {
			return nil, "", fmt.Errorf("sorular: %w", err)
		}
		for _, r := range rows {
			out = append(out, questionView(r))
		}
		if len(rows) == int(size) {
			last := rows[len(rows)-1]
			next = cursor{Micros: last.CreatedAt.Time.UnixMicro(), ID: id.ID(last.ID.Bytes).String()}
		}
	} else {
		p := store.SearchQuestionsPageParams{TayfaID: tayfa.PG(), Query: query, HasTag: hasTag, TagID: tag.PG(), PageSize: size}
		if in.PageToken != "" {
			p.HasCursor, p.AfterID, p.AfterRank = true, after.PG(), math.Float32frombits(c.Rank)
		}
		rows, err := s.q.SearchQuestionsPage(ctx, p)
		if err != nil {
			// The error never holds the query: pgx does not put arguments
			// into errors.
			return nil, "", fmt.Errorf("soru arama: %w", err)
		}
		for _, r := range rows {
			out = append(out, Question{
				ID: id.ID(r.ID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes), AuthorID: optional(r.AuthorID),
				Title: r.Title, Body: r.Body, Tags: []Tag{}, AnswerCount: r.AnswerCount,
				CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
			})
		}
		if len(rows) == int(size) {
			last := rows[len(rows)-1]
			next = cursor{Rank: math.Float32bits(last.Rank), ID: id.ID(last.ID.Bytes).String()}
		}
	}
	if out == nil {
		out = []Question{}
	}
	if err := attachTags(ctx, s.q, tayfa, out); err != nil {
		return nil, "", err
	}
	token := ""
	if next.ID != "" {
		next.Method, next.Tayfa, next.Scope = method, tayfa.String(), scope
		token = next.encode()
	}
	return out, token, nil
}

// ListAnswers lists the visible answers of a visible question: most
// helpful votes first, then oldest first. Voted is the caller's vote.
func (s *Service) ListAnswers(ctx context.Context, user, tayfa, question id.ID, pageSize int32, pageToken string) ([]Answer, string, error) {
	size, err := normalizePageSize(pageSize)
	if err != nil {
		return nil, "", err
	}
	scope := question.String()
	p := store.ListAnswersPageParams{UserID: user.PG(), TayfaID: tayfa.PG(), QuestionID: question.PG(), PageSize: size}
	if pageToken != "" {
		c, after, ok := decodeCursor(pageToken, cursorAnswers, tayfa, scope)
		if !ok {
			return nil, "", apperr.Validation("page_token")
		}
		p.HasCursor, p.AfterVotes, p.AfterID = true, c.Votes, after.PG()
	}
	if err := s.read(ctx, user, tayfa); err != nil {
		return nil, "", err
	}
	switch _, err := s.q.GetQuestion(ctx, store.GetQuestionParams{TayfaID: tayfa.PG(), ID: question.PG()}); {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, "", apperr.ErrQuestionNotFound
	case err != nil:
		return nil, "", fmt.Errorf("soru: %w", err)
	}
	rows, err := s.q.ListAnswersPage(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("cevaplar: %w", err)
	}
	out := make([]Answer, 0, len(rows))
	for _, r := range rows {
		out = append(out, answerView(r))
	}
	next := ""
	if len(rows) == int(size) {
		last := rows[len(rows)-1]
		next = cursor{
			Method: cursorAnswers, Tayfa: tayfa.String(), Scope: scope,
			Votes: last.VoteCount, ID: id.ID(last.ID.Bytes).String(),
		}.encode()
	}
	return out, next, nil
}

func normalizePageSize(n int32) (int32, error) {
	switch {
	case n < 0:
		return 0, apperr.Validation("page_size")
	case n == 0:
		return defaultPageSize, nil
	case n > maxPageSize:
		return maxPageSize, nil
	}
	return n, nil
}

// digest binds a token to the request's parameters without carrying them.
func digest(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func (c cursor) encode() string {
	b, _ := json.Marshal(c) // plain struct; cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses a token made for the same method, tayfa and scope.
func decodeCursor(token, method string, tayfa id.ID, scope string) (cursor, id.ID, bool) {
	if len(token) > 512 {
		return cursor{}, id.ID{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var c cursor
	if err != nil || json.Unmarshal(b, &c) != nil {
		return cursor{}, id.ID{}, false
	}
	after, ok := id.Parse(c.ID)
	if !ok || c.Method != method || c.Tayfa != tayfa.String() || c.Scope != scope {
		return cursor{}, id.ID{}, false
	}
	return c, after, true
}
