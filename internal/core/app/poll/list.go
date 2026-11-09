package poll

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Paging (poll.proto): page_size 0 means 20, above 100 means 100, negative
// is a validation error.
const (
	defaultPageSize = 20
	maxPageSize     = 100
	methodPolls     = "polls"
	methodVoters    = "poll_voters"
)

// cursor is the position after the last row of a page; the page token is
// its JSON form in base64url. It carries the method and the request fields
// it was made for, and nothing the caller could not already see.
type cursor struct {
	Method string `json:"m"`
	Tayfa  string `json:"t"`
	// Scope is the event filter of ListPolls, or "poll/option" of
	// ListVoters.
	Scope string `json:"s,omitempty"`
	// Micros is created_at in Unix microseconds (Postgres precision).
	Micros int64  `json:"c"`
	ID     string `json:"i"`
}

// List returns the tayfa's visible polls, newest first; with event, only
// that event's polls.
func (s *Service) List(ctx context.Context, user, tayfa id.ID, event string, pageSize int32, pageToken string) ([]View, string, error) {
	size, err := normalizePageSize(pageSize)
	if err != nil {
		return nil, "", err
	}
	p := store.ListPollsPageParams{TayfaID: tayfa.PG(), PageSize: size}
	if event != "" {
		e, ok := id.Parse(event)
		if !ok {
			return nil, "", apperr.Validation("event_id")
		}
		p.EventID = e.PG()
	}
	if pageToken != "" {
		c, after, ok := decodeCursor(pageToken, methodPolls, tayfa, event)
		if !ok {
			return nil, "", apperr.Validation("page_token")
		}
		p.HasCursor, p.AfterID = true, after.PG()
		p.AfterCreatedAt = pgtype.Timestamptz{Time: time.UnixMicro(c.Micros).UTC(), Valid: true}
	}
	c, err := s.read(ctx, user, tayfa, authz.PollView)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.q.ListPollsPage(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("anketler: %w", err)
	}
	out, err := views(ctx, s.q, user, rows, c.now)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) == int(size) {
		last := rows[len(rows)-1]
		next = cursor{
			Method: methodPolls, Tayfa: tayfa.String(), Scope: event,
			Micros: last.CreatedAt.Time.UnixMicro(), ID: id.ID(last.ID.Bytes).String(),
		}.encode()
	}
	return out, next, nil
}

// Voter is one vote for an option of a poll that is not anonymous.
type Voter struct {
	UserID  id.ID
	VotedAt time.Time
}

// ListVoters lists who voted for option of poll, oldest vote first. An
// anonymous poll gets apperr.ErrPollAnonymous, whatever the caller's role.
func (s *Service) ListVoters(ctx context.Context, user, tayfa, poll id.ID, option string, pageSize int32, pageToken string) ([]Voter, string, error) {
	size, err := normalizePageSize(pageSize)
	if err != nil {
		return nil, "", err
	}
	o, ok := id.Parse(option)
	if !ok {
		return nil, "", apperr.Validation("option_id")
	}
	scope := poll.String() + "/" + o.String()
	p := store.ListPollVotersParams{TayfaID: tayfa.PG(), PollID: poll.PG(), OptionID: o.PG(), PageSize: size}
	if pageToken != "" {
		c, after, ok := decodeCursor(pageToken, methodVoters, tayfa, scope)
		if !ok {
			return nil, "", apperr.Validation("page_token")
		}
		p.HasCursor, p.AfterUserID = true, after.PG()
		p.AfterCreatedAt = pgtype.Timestamptz{Time: time.UnixMicro(c.Micros).UTC(), Valid: true}
	}
	if _, err := s.read(ctx, user, tayfa, authz.PollViewVoters); err != nil {
		return nil, "", err
	}
	pr, err := s.q.GetPoll(ctx, store.GetPollParams{TayfaID: tayfa.PG(), ID: poll.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, "", apperr.ErrPollNotFound
	case err != nil:
		return nil, "", fmt.Errorf("anket: %w", err)
	}
	if pr.Anonymous {
		return nil, "", apperr.ErrPollAnonymous
	}
	valid, err := s.q.ListPollOptionIDs(ctx, poll.PG())
	if err != nil {
		return nil, "", fmt.Errorf("anket seçenekleri: %w", err)
	}
	if !slices.Contains(valid, o.PG()) {
		return nil, "", apperr.Validation("option_id")
	}
	rows, err := s.q.ListPollVoters(ctx, p)
	if err != nil {
		return nil, "", fmt.Errorf("oy verenler: %w", err)
	}
	out := make([]Voter, 0, len(rows))
	for _, r := range rows {
		out = append(out, Voter{UserID: id.ID(r.UserID.Bytes), VotedAt: r.CreatedAt.Time.UTC()})
	}
	next := ""
	if len(rows) == int(size) {
		last := rows[len(rows)-1]
		next = cursor{
			Method: methodVoters, Tayfa: tayfa.String(), Scope: scope,
			Micros: last.CreatedAt.Time.UnixMicro(), ID: id.ID(last.UserID.Bytes).String(),
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

func (c cursor) encode() string {
	b, _ := json.Marshal(c) // plain struct; cannot fail
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor parses a token made by method for the same tayfa and scope.
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
