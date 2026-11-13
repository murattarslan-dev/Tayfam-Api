// Package demo writes the closed demo tayfas and their content to an
// environment once (issue #179, ADR-0023): for store review, screenshots and
// demos. The platform owner runs it by hand (`identity demo-seed`,
// docs/runbooks/demo-verisi.md); nothing runs it on its own.
//
// How it writes, and why:
//   - Demo accounts are made in identity's own database the way
//     registration makes accounts (ensureUser): encrypted birth date,
//     referral code, user.profile_updated in the outbox. They have no
//     Google or Apple identity (auth_identities.provider = "demo") and so
//     cannot sign in. Their e-mail, <subject>@demo.tayfam.app, marks them.
//   - Everything in core and chat goes through the services' public gRPC
//     methods with each demo account's own short-lived access token, the
//     same path a client takes through gateway. core and chat check
//     permissions, write their audit records (app.actor_id), encrypt
//     channel messages and write their outbox events themselves; this
//     package never touches their databases (ADR-0002) and needs no service
//     identity (ADR-0013).
//   - The platform owner's account, found by its referral code, is used for
//     one call per tayfa only: JoinByCode. Its token is never written
//     anywhere and expires in minutes.
//   - Running it again creates no copies: accounts are found by their demo
//     subject, every create carries a client_request_id derived from the
//     record's key (requestID), and lists (members, channels, camp items)
//     are read before adding. Nothing is refreshed or deleted: a record
//     that exists stays as it is, and the inactive tayfa cleanup archives
//     the tayfas when they go quiet.
//
// Logs carry counts, IDs and error reasons only: no names, texts, join
// codes, referral codes or tokens.
package demo

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/birthdate"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// callTimeout bounds one gRPC call.
const callTimeout = 20 * time.Second

// rateLimitRetries is how often a call refused with RATE_LIMITED is tried
// again (chat's message limit is 30 a minute, bursts of 10).
const rateLimitRetries = 6

// turkey is Turkey's time: UTC+3 all year. Event times are written in it.
var turkey = time.FixedZone("TRT", 3*60*60)

// TokenIssuer issues access tokens; *auth.Signer is one.
type TokenIssuer interface {
	Issue(userID, sessionID string) (string, time.Time, error)
}

// Config wires a Seeder.
type Config struct {
	Pool   *pgxpool.Pool // identity's database
	Cipher *birthdate.Cipher
	Log    *slog.Logger
	// Tokens issues the API access tokens the demo accounts and the owner
	// call core and chat with (aud = AUTH_AUDIENCE, short-lived).
	Tokens TokenIssuer
	Core   Core
	Chat   ChannelAPI
	Now    func() time.Time
	// Sleep waits between retries; time.Sleep with ctx when nil.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Seeder writes the demo data.
type Seeder struct {
	cfg Config

	mu       sync.Mutex
	sessions map[id.ID]string // one random session ID per account per run
}

// New returns a Seeder.
func New(cfg Config) (*Seeder, error) {
	if cfg.Pool == nil || cfg.Cipher == nil || cfg.Log == nil || cfg.Tokens == nil || !cfg.Core.complete() || cfg.Chat == nil {
		return nil, errors.New("demo: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return &Seeder{cfg: cfg, sessions: map[id.ID]string{}}, nil
}

// Report is what a run did.
type Report struct {
	UsersCreated  int
	UsersExisting int
	Tayfas        []TayfaResult
}

// TayfaResult is one demo tayfa after the run.
type TayfaResult struct {
	ID        string
	Name      string
	OwnerRole string
	// ReadOnly: the tayfa was archived, suspended or is being deleted; its
	// remaining steps were skipped.
	ReadOnly bool
}

// Run writes the demo data and adds the owner (found by referral code) to
// every demo tayfa with the role the data set gives. The owner is looked up
// first: an unknown code writes nothing.
func (s *Seeder) Run(ctx context.Context, ownerRefCode string) (Report, error) {
	var rep Report
	owner, err := s.findOwner(ctx, ownerRefCode)
	if err != nil {
		return rep, err
	}
	users := make(map[string]id.ID, len(people))
	for _, p := range people {
		u, created, err := s.ensureUser(ctx, p)
		if err != nil {
			return rep, err
		}
		users[p.Key] = u
		if created {
			rep.UsersCreated++
		} else {
			rep.UsersExisting++
		}
	}
	s.cfg.Log.InfoContext(ctx, "demo hesapları hazır",
		slog.Int("created", rep.UsersCreated), slog.Int("existing", rep.UsersExisting))

	for _, t := range tayfas {
		res, err := s.seedTayfa(ctx, t, users, owner)
		if err != nil {
			return rep, fmt.Errorf("tayfa %s: %w", t.Key, err)
		}
		rep.Tayfas = append(rep.Tayfas, res)
		s.cfg.Log.InfoContext(ctx, "demo tayfası hazır",
			slog.String("tayfa_id", res.ID), slog.Bool("read_only", res.ReadOnly))
	}
	return rep, nil
}

// requestID is the client_request_id of the demo record named key: the
// same key always gives the same UUID (version 8, RFC 9562), so a second
// run replays the first create instead of making a copy. It is not a
// secret and not random on purpose.
func requestID(key string) string {
	h := sha256.Sum256([]byte("tayfam-demo/v1:" + key))
	var u id.ID
	copy(u[:], h[:16])
	u[6] = (u[6] & 0x0f) | 0x80 // version 8
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant
	return u.String()
}

// as returns ctx carrying a fresh access token of user, valid for a few
// minutes. Each account gets one random session ID per run; there is no
// session row, so the token cannot be refreshed.
func (s *Seeder) as(ctx context.Context, user id.ID) (context.Context, error) {
	s.mu.Lock()
	sid, ok := s.sessions[user]
	if !ok {
		sid = id.New().String()
		s.sessions[user] = sid
	}
	s.mu.Unlock()
	tok, _, err := s.cfg.Tokens.Issue(user.String(), sid)
	if err != nil {
		return nil, fmt.Errorf("erişim token'ı: %w", err)
	}
	return auth.WithToken(ctx, tok), nil
}

// call runs f as user with a fresh token and a timeout, again after a pause
// while the service answers RATE_LIMITED.
func call[T any](ctx context.Context, s *Seeder, user id.ID, f func(context.Context) (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		cctx, err := s.as(ctx, user)
		if err != nil {
			return zero, err
		}
		cctx, cancel := context.WithTimeout(cctx, callTimeout)
		v, err := f(cctx)
		cancel()
		if err == nil || reason(err) != "ERROR_REASON_RATE_LIMITED" || attempt > rateLimitRetries {
			return v, err
		}
		if err := s.cfg.Sleep(ctx, time.Duration(attempt)*5*time.Second); err != nil {
			return zero, err
		}
	}
}

// reason is the ErrorInfo reason of a core or chat error ("" if none).
func reason(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

func is(err error, reasons ...string) bool {
	r := reason(err)
	for _, want := range reasons {
		if r == want {
			return true
		}
	}
	return false
}

// errReadOnly stops the steps of a tayfa that no longer takes changes.
var errReadOnly = errors.New("tayfa salt okunur")

// check turns a read-only refusal into errReadOnly and wraps other errors
// with the step name. The step name holds no content.
func check(step string, err error) error {
	switch {
	case err == nil:
		return nil
	case is(err, "ERROR_REASON_TAYFA_READ_ONLY"):
		return errReadOnly
	default:
		return fmt.Errorf("%s: %w", step, err)
	}
}

// startTime is a.Days after today, at a.Hour:a.Minute Turkey time.
func (s *Seeder) startTime(a at) time.Time {
	now := s.cfg.Now().In(turkey)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, turkey)
	return day.AddDate(0, 0, a.Days).Add(time.Duration(a.Hour)*time.Hour + time.Duration(a.Minute)*time.Minute)
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// keyOf joins record keys: "ege/event/konvoy".
func keyOf(parts ...string) string { return strings.Join(parts, "/") }
