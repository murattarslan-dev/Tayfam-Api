package demo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/app/deletion"
	"github.com/murattarslan-dev/tayfam-api/internal/identity/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// The demo purge (`identity demo-purge`, issue #227, ADR-0035) takes demo
// data out through the normal deletion paths:
//
//   - A demo tayfa is deleted by its demo founder's own token with
//     TayfaService.RequestDeletion: read-only at once, out of Keşfet at
//     once, soft-deleted by core's job after the usual 72 hours.
//   - A demo account is closed with the account deletion flow
//     (deletion.Erase): its personal data goes at once and user.deleted
//     goes to the outbox, so core and chat remove its memberships,
//     friendships, blocks and profile copies. The platform owner's
//     friendships and blocks with demo accounts go with them.
//
// The boundary is fixed here and in tests: an account is touched only
// through its auth_identities row with provider = 'demo' and a subject of
// the data set (checked again in the transaction that erases it); a tayfa
// only when a demo founder of the data set is its admin and its name is
// that founder's data set name, which contains "Demo". A real account, the
// platform owner's included, and a tayfa a real user founded are never
// touched. The platform owner's token is not used at all.

// PurgeOptions chooses what Purge removes.
type PurgeOptions struct {
	// All (-hepsi) also removes the closed set (store review tayfas and
	// demo-01..demo-18); by default only the Keşfet set goes.
	All bool
	// DryRun (-dry-run) only counts.
	DryRun bool
}

// Outcomes of a demo tayfa in a purge.
const (
	PurgeWillDelete      = "silinecek"
	PurgeDeletionAsked   = "silme istendi"
	PurgeAlreadyPending  = "silinmeyi zaten bekliyor"
	PurgeReadOnlySkipped = "salt okunur, atlandı"
)

// PurgedTayfa is one demo tayfa the purge found.
type PurgedTayfa struct {
	ID, Name string
	Outcome  string // Purge*
}

// PurgeReport is what a purge did (or, in a dry run, would do).
type PurgeReport struct {
	Tayfas []PurgedTayfa
	// Accounts: live demo accounts closed (or to close).
	Accounts int
}

var demoSubject = regexp.MustCompile(`^demo-[0-9]{2}$`)

// Purge removes the Keşfet demo set, and with opts.All the closed set too.
// Running it again finds less and less; once everything is gone it does
// nothing.
func (s *Seeder) Purge(ctx context.Context, opts PurgeOptions) (PurgeReport, error) {
	var rep PurgeReport
	persons, specs := discoverPeople, discoverTayfas
	if opts.All {
		persons, specs = allPeople(), append(append([]tayfaSpec{}, tayfas...), discoverTayfas...)
	}
	users := map[string]id.ID{}
	for _, p := range persons {
		u, ok, err := s.findDemoUser(ctx, p)
		if err != nil {
			return rep, err
		}
		if ok {
			users[p.Key] = u
		}
	}

	// Tayfas first: their founders' tokens are needed, and the accounts
	// go only once every tayfa is asked to be deleted.
	for _, t := range specs {
		founder, ok := users[t.Founder]
		if !ok {
			continue // the founder's account is gone; so is the tayfa's admin
		}
		found, err := s.founderTayfas(ctx, founder, t)
		if err != nil {
			return rep, fmt.Errorf("tayfa %s: %w", t.Key, err)
		}
		for _, mt := range found {
			res := PurgedTayfa{ID: mt.GetTayfaId(), Name: mt.GetName()}
			switch {
			case mt.GetTayfa().GetDeletionTime() != nil:
				res.Outcome = PurgeAlreadyPending
			case opts.DryRun:
				res.Outcome = PurgeWillDelete
			default:
				_, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.RequestDeletionResponse, error) {
					return s.cfg.Core.Tayfas.RequestDeletion(ctx, &corev1.RequestDeletionRequest{TayfaId: mt.GetTayfaId()})
				})
				switch {
				case err == nil:
					res.Outcome = PurgeDeletionAsked
				case is(err, "ERROR_REASON_DELETION_ALREADY_REQUESTED"):
					res.Outcome = PurgeAlreadyPending
				case is(err, "ERROR_REASON_TAYFA_READ_ONLY"):
					// Archived or suspended: the founder cannot delete it.
					// Once its demo accounts are closed and no active member
					// is left, core schedules its deletion itself.
					res.Outcome = PurgeReadOnlySkipped
				default:
					return rep, fmt.Errorf("tayfa %s silme: %w", t.Key, err)
				}
			}
			rep.Tayfas = append(rep.Tayfas, res)
		}
	}

	for _, p := range persons {
		u, ok := users[p.Key]
		if !ok {
			continue
		}
		if !opts.DryRun {
			if err := s.eraseDemoUser(ctx, p, u); err != nil {
				return rep, fmt.Errorf("demo hesabı %s: %w", p.Key, err)
			}
		}
		rep.Accounts++
	}
	s.cfg.Log.InfoContext(ctx, "demo temizliği",
		slog.Bool("all", opts.All), slog.Bool("dry_run", opts.DryRun),
		slog.Int("tayfas", len(rep.Tayfas)), slog.Int("accounts", rep.Accounts))
	return rep, nil
}

// findDemoUser returns the live demo account of p: the user behind its
// auth_identities row with provider = 'demo'. A closed account has no such
// row any more (deletion removes sign-in identities).
func (s *Seeder) findDemoUser(ctx context.Context, p person) (id.ID, bool, error) {
	if !demoSubject.MatchString(p.Subject) {
		return id.ID{}, false, fmt.Errorf("demo subject %q", p.Subject)
	}
	ident, err := store.New(s.cfg.Pool).GetAuthIdentity(ctx, store.GetAuthIdentityParams{Provider: Provider, Subject: p.Subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return id.ID{}, false, nil
	}
	if err != nil {
		return id.ID{}, false, fmt.Errorf("demo kimliği: %w", err)
	}
	if !isDemoIdentity(ident, p.Subject) {
		return id.ID{}, false, nil
	}
	u, ok := id.FromPG(ident.UserID)
	return u, ok, nil
}

// isDemoIdentity: the sign-in identity is a demo one of subject (ADR-0023
// marker: provider, subject and e-mail).
func isDemoIdentity(ident store.GetAuthIdentityRow, subject string) bool {
	return ident.Provider == Provider && ident.Subject == subject && demoSubject.MatchString(subject) &&
		ident.Email.Valid && ident.Email.String == Email(subject)
}

// eraseDemoUser closes the demo account with the account deletion flow.
// The demo identity is read again in the same transaction, after the user
// row is locked: only an account that is a demo account right then is
// erased.
func (s *Seeder) eraseDemoUser(ctx context.Context, p person, user id.ID) error {
	return pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		u, err := q.LockLiveUser(ctx, user.PG())
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already closed
		}
		if err != nil {
			return fmt.Errorf("kullanıcı kilidi: %w", err)
		}
		ident, err := q.GetAuthIdentity(ctx, store.GetAuthIdentityParams{Provider: Provider, Subject: p.Subject})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("demo kimliği: %w", err)
		}
		if !isDemoIdentity(ident, p.Subject) || ident.UserID != u.ID {
			return errors.New("hesap demo hesabı değil; dokunulmadı")
		}
		_, err = deletion.Erase(ctx, tx, user)
		return err
	})
}

// founderTayfas lists the tayfas of t the demo founder is the admin of.
func (s *Seeder) founderTayfas(ctx context.Context, founder id.ID, t tayfaSpec) ([]*corev1.MyTayfa, error) {
	var out []*corev1.MyTayfa
	token := ""
	for {
		resp, err := call(ctx, s, founder, func(ctx context.Context) (*corev1.ListMyTayfasResponse, error) {
			return s.cfg.Core.Tayfas.ListMyTayfas(ctx, &corev1.ListMyTayfasRequest{PageSize: 100, PageToken: token})
		})
		if err != nil {
			return nil, fmt.Errorf("tayfa listesi: %w", err)
		}
		for _, mt := range resp.GetTayfas() {
			if isDemoTayfa(t, mt) {
				out = append(out, mt)
			}
		}
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

// isDemoTayfa: an entry of a demo founder's tayfa list is the data set's
// tayfa t: the founder is its active admin, it is a standard tayfa, and its
// name is t's name, which contains "Demo".
func isDemoTayfa(t tayfaSpec, mt *corev1.MyTayfa) bool {
	return mt.GetStatus() == corev1.MembershipStatus_MEMBERSHIP_STATUS_ACTIVE &&
		mt.GetRole() == corev1.Role_ROLE_ADMIN &&
		mt.GetKind() != corev1.TayfaKind_TAYFA_KIND_INSTANT &&
		strings.Contains(t.Name, "Demo") && mt.GetName() == t.Name
}
