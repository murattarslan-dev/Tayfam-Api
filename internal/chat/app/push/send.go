package push

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/chat/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Sender sends one message (fcm.Client). It returns nil, a *SendError or
// another error, which counts as transient.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// ErrorKind classifies a failed send.
type ErrorKind int

const (
	// Transient: try again later (network, 429, 5xx, expired credential).
	Transient ErrorKind = iota + 1
	// InvalidToken: the token is gone for good; it is removed.
	InvalidToken
	// Rejected: this message will never be accepted; not retried.
	Rejected
)

// SendError is a classified send failure. Code is FCM's error code or a
// short word; it never holds the token or any response body.
type SendError struct {
	Kind       ErrorKind
	Status     int
	Code       string
	RetryAfter time.Duration
}

func (e *SendError) Error() string {
	return fmt.Sprintf("push gönderilemedi: %d %s", e.Status, e.Code)
}

// Outcome counts what one job did.
type Outcome struct {
	Sent, Removed, Failed int
	Skipped               string // why nothing was sent ("" when tried)
	Retry                 bool
}

// Run sends due pushes until ctx is done. It may run in more than one pod:
// jobs are leased (ADR-0019 item 2).
func (s *Service) Run(ctx context.Context) error {
	if s.cfg.Sender == nil {
		<-ctx.Done()
		return nil
	}
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		for {
			n, err := s.Drain(ctx)
			if err != nil && ctx.Err() == nil {
				s.cfg.Log.WarnContext(ctx, "push işleri alınamadı", slog.String("hata", err.Error()))
			}
			if err != nil || n < batch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Drain leases one batch of due jobs and processes them, a few at a time.
// It returns how many jobs it leased.
func (s *Service) Drain(ctx context.Context) (int, error) {
	if s.cfg.Sender == nil {
		return 0, nil
	}
	now := s.now()
	jobs, err := store.New(s.cfg.Pool).ClaimPushJobs(ctx, store.ClaimPushJobsParams{
		Now: ts(now), LeaseUntil: ts(now.Add(lease)), N: batch,
	})
	if err != nil {
		return 0, fmt.Errorf("push işleri: %w", err)
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for _, j := range jobs {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if _, err := s.process(ctx, j); err != nil && ctx.Err() == nil {
				s.cfg.Log.WarnContext(ctx, "push işi başarısız", slog.String("hata", err.Error()))
			}
		}()
	}
	wg.Wait()
	return len(jobs), nil
}

// process sends one job's push and settles the job: done, or retried later
// for the sessions that failed.
func (s *Service) process(ctx context.Context, j store.ClaimPushJobsRow) (Outcome, error) {
	q := store.New(s.cfg.Pool)
	done := func(o Outcome) (Outcome, error) {
		if err := q.DeletePushJob(ctx, j.NotificationID); err != nil {
			return o, fmt.Errorf("push işi: %w", err)
		}
		return o, nil
	}
	n := note{
		ID: uuidOf(j.NotificationID), User: uuidOf(j.UserID), Type: j.Type, TargetType: j.TargetType,
		Target: uuidOf(j.TargetID), Payload: j.Payload,
	}
	if j.TayfaID.Valid {
		n.Tayfa = uuidOf(j.TayfaID)
	}
	now := s.now()
	switch {
	case j.ReadAt.Valid:
		return done(Outcome{Skipped: "read"})
	case int(j.Attempts) > MaxAttempts:
		return done(Outcome{Skipped: "attempts"})
	case j.QueuedAt.Valid && now.Sub(j.QueuedAt.Time) > MaxAge:
		return done(Outcome{Skipped: "old"})
	case s.cfg.Presence.Online(n.User):
		// Delivered live over the connection (ADR-0005).
		return done(Outcome{Skipped: "online"})
	}
	ok, err := s.wanted(ctx, q, n, now)
	if err != nil {
		return s.failed(ctx, q, j, err)
	}
	if !ok {
		return done(Outcome{Skipped: "muted"})
	}
	tokens, err := q.ListUserPushTokens(ctx, store.ListUserPushTokensParams{UserID: j.UserID, N: MaxTokensPerUser})
	if err != nil {
		return s.failed(ctx, q, j, fmt.Errorf("push token'ları: %w", err))
	}
	only := map[pgtype.UUID]bool{}
	for _, sid := range j.SessionIds {
		only[sid] = true
	}
	base := content(n)
	var o Outcome
	var again []pgtype.UUID
	var wait time.Duration
	for _, t := range tokens {
		if len(only) > 0 && !only[t.SessionID] {
			continue
		}
		m := base
		m.Token, m.Platform = t.Token, t.Platform
		sctx, cancel := context.WithTimeout(ctx, sendTimeout)
		err := s.cfg.Sender.Send(sctx, m)
		cancel()
		if err == nil {
			o.Sent++
			continue
		}
		if ctx.Err() != nil {
			return o, ctx.Err()
		}
		var se *SendError
		if !errors.As(err, &se) {
			se = &SendError{Kind: Transient, Code: "error"}
		}
		switch se.Kind {
		case InvalidToken:
			if _, err := q.DeleteInvalidPushToken(ctx, store.DeleteInvalidPushTokenParams{SessionID: t.SessionID, Token: t.Token}); err != nil {
				return o, fmt.Errorf("geçersiz push token'ı: %w", err)
			}
			o.Removed++
		case Rejected:
			o.Failed++
			s.cfg.Log.WarnContext(ctx, "push reddedildi", slog.Int("durum", se.Status), slog.String("kod", se.Code))
		default:
			o.Failed++
			again = append(again, t.SessionID)
			wait = max(wait, se.RetryAfter)
		}
	}
	if len(tokens) == 0 {
		o.Skipped = "no_token"
	}
	if len(again) > 0 && int(j.Attempts) < MaxAttempts {
		o.Retry = true
		return o, s.retry(ctx, q, j, again, wait)
	}
	return done(o)
}

// failed retries a job that failed before sending (a database error).
func (s *Service) failed(ctx context.Context, q *store.Queries, j store.ClaimPushJobsRow, cause error) (Outcome, error) {
	if err := s.retry(ctx, q, j, nil, 0); err != nil {
		return Outcome{}, errors.Join(cause, err)
	}
	return Outcome{Retry: true}, cause
}

// wanted reads the recipient's preference for n.
func (s *Service) wanted(ctx context.Context, q *store.Queries, n note, now time.Time) (bool, error) {
	typ, scopeID, ok := scope(n)
	if !ok {
		return allowed(n.Type, "", time.Time{}, now), nil
	}
	p, err := q.GetNotificationPreference(ctx, store.GetNotificationPreferenceParams{
		UserID: n.User.PG(), ScopeType: typ, ScopeID: scopeID.PG(),
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("tercih: %w", err)
	}
	until := time.Time{}
	if p.MutedUntil.Valid {
		until = p.MutedUntil.Time
	}
	return allowed(n.Type, p.Level, until, now), nil
}

// retry schedules the job again with exponential backoff (never sooner than
// FCM's Retry-After), for the given sessions only (nil: all).
func (s *Service) retry(ctx context.Context, q *store.Queries, j store.ClaimPushJobsRow, sessions []pgtype.UUID, after time.Duration) error {
	if int(j.Attempts) >= MaxAttempts {
		if err := q.DeletePushJob(ctx, j.NotificationID); err != nil {
			return fmt.Errorf("push işi: %w", err)
		}
		return nil
	}
	if err := q.RetryPushJob(ctx, store.RetryPushJobParams{
		NotificationID: j.NotificationID, NextAttemptAt: ts(s.now().Add(Backoff(int(j.Attempts), after))), SessionIds: sessions,
	}); err != nil {
		return fmt.Errorf("push yeniden deneme: %w", err)
	}
	return nil
}

// Backoff is the wait after the attempt-th failed attempt (1-based): 30 s,
// 1, 2, 4 minutes…, at most 30 minutes, and never less than retryAfter
// (itself capped at an hour).
func Backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := backoffBase
	for i := 1; i < attempt && d < backoffMax; i++ {
		d *= 2
	}
	d = min(d, backoffMax)
	return max(d, min(retryAfter, time.Hour))
}

func uuidOf(u pgtype.UUID) id.ID { return id.ID(u.Bytes) }
