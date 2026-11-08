// Package membership joins users to tayfas (join code, invite link), manages
// invite links and join requests, and moderates members (ADR-0004, data
// model v14 "Tayfaya davet", "Rol hiyerarşisi").
//
// The specification is proto/tayfam/core/v1/membership.proto. Every method
// takes the verified caller; no input can act on behalf of another user.
// Every tayfa-scoped query is scoped by tayfa_id and checked against the
// caller's own membership; a caller who is not an active member learns
// nothing about the tayfa (apperr.ErrTayfaNotFound).
//
// Writes run in one transaction that names the caller (txn.WithActor) and
// first locks the tayfa row, the same lock the memberships trigger takes, so
// the checks made here stay true until commit. The trigger checks the role
// hierarchy and the last admin again. Each write also appends an audit log
// row and a domain event in the same transaction (ADR-0002). Invite tokens
// are returned once by CreateInviteLink and otherwise exist only as their
// SHA-256 hash; they never reach logs, audit logs, events or errors.
package membership

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

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

// Limits from the contract.
const (
	MinInviteTTL   = time.Hour
	MaxInviteTTL   = 30 * 24 * time.Hour
	MaxInviteUses  = 100
	MaxMuteAhead   = 30 * 24 * time.Hour
	inviteTokenLen = 32 // bytes; 43 characters in base64url without padding
)

// joinCodeFormat is the tayfas_join_code_format CHECK: 8 characters without
// the easily confused 0/O and 1/I.
var joinCodeFormat = regexp.MustCompile(`^[2-9A-HJ-NP-Z]{8}$`)

// Limiter limits join-code attempts per user (ratelimit.Limiter).
type Limiter interface {
	Allow(key string) bool
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// JoinLimiter limits JoinByCode attempts per user against code
	// guessing.
	JoinLimiter Limiter
	Now         func() time.Time
}

// Service implements the membership operations.
type Service struct {
	pool    *pgxpool.Pool
	limiter Limiter
	now     func() time.Time
	q       *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.JoinLimiter == nil {
		return nil, errors.New("membership: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{pool: cfg.Pool, limiter: cfg.JoinLimiter, now: func() time.Time { return cfg.Now().UTC() }, q: store.New(cfg.Pool)}, nil
}

// Member is one user's membership in one tayfa as the caller may see it.
type Member struct {
	TayfaID id.ID
	UserID  id.ID
	Role    authz.Role
	Status  authz.Status
	// JoinedAt is zero unless the membership became active.
	JoinedAt time.Time
	// MutedUntil is zero when not muted, or when the caller may not see
	// mutes (only the member and moderators and admins see them).
	MutedUntil time.Time
}

// Pending is a join request as moderators see it.
type Pending struct {
	Member
	RequestedAt time.Time
	// JoinedVia is memberships.joined_via ("code", "invite_link", ...).
	JoinedVia string
	// InviterID is the direct inviter or the invite link's creator; zero if
	// none or deleted.
	InviterID id.ID
	// InviteID is the invite link used; zero if none or deleted.
	InviteID id.ID
}

// Invite describes an invite link. It never holds the token or its hash.
type Invite struct {
	ID        id.ID
	TayfaID   id.ID
	CreatedBy id.ID // zero if the creator's account was deleted
	CreatedAt time.Time
	ExpiresAt time.Time
	MaxUses   int32
	UseCount  int32
	RevokedAt time.Time // zero unless revoked
}

// Preview is the narrow view of a tayfa that an invite link reveals.
type Preview struct {
	TayfaID       id.ID
	Name          string
	MemberCount   int32
	AlreadyMember bool
}

// Page is one page of a list.
type Page[T any] struct {
	Items []T
	// Next is the token of the next page; empty on the last page.
	Next string
}

// ---------------------------------------------------------------------------
// Joining

// JoinByCode joins the tayfa that owns code. Malformed, unknown and rotated
// codes, deleted and read-only tayfas and tayfas that banned the caller all
// return apperr.ErrInvalidJoinCode, so guessing codes reveals nothing; the
// attempts are limited per user.
func (s *Service) JoinByCode(ctx context.Context, caller id.ID, code string) (Member, error) {
	if !s.limiter.Allow(caller.String()) {
		return Member{}, apperr.ErrRateLimited
	}
	code = strings.ToUpper(code)
	if !joinCodeFormat.MatchString(code) {
		return Member{}, apperr.ErrInvalidJoinCode
	}
	var out Member
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		found, err := q.GetTayfaByJoinCode(ctx, code)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrInvalidJoinCode
		}
		if err != nil {
			return fmt.Errorf("kod ile tayfa: %w", err)
		}
		t, err := lockTayfa(ctx, q, id.ID(found.ID.Bytes))
		if err != nil {
			if errors.Is(err, apperr.ErrTayfaNotFound) {
				return apperr.ErrInvalidJoinCode
			}
			return err
		}
		if t.readOnly {
			return apperr.ErrInvalidJoinCode
		}
		if err := s.checkNotMember(ctx, q, t.id, caller, apperr.ErrInvalidJoinCode); err != nil {
			return err
		}
		st := authz.StatusPending
		if t.joinPolicy == "open" {
			st = authz.StatusActive
		}
		out, err = s.join(ctx, q, t.id, caller, st, "code", id.ID{})
		return err
	})
	return out, err
}

// PreviewInvite returns the narrow view of the tayfa behind token without
// using the link up.
func (s *Service) PreviewInvite(ctx context.Context, caller id.ID, token string) (Preview, error) {
	hash, err := hashToken(token)
	if err != nil {
		return Preview{}, err
	}
	inv, err := s.q.GetInviteByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Preview{}, apperr.ErrInviteInvalid
	}
	if err != nil {
		return Preview{}, fmt.Errorf("davet: %w", err)
	}
	if access.ReadOnly(access.Tayfa{Status: inv.Status, SuspendedAt: inv.SuspendedAt, DeletionRequestedAt: inv.DeletionRequestedAt}) {
		return Preview{}, apperr.ErrInviteInvalid
	}
	if err := usable(inv.RevokedAt, inv.ExpiresAt, inv.MaxUses, inv.UseCount, s.now()); err != nil {
		return Preview{}, err
	}
	tayfa, _ := id.FromPG(inv.TayfaID)
	already := false
	m, err := s.q.GetMembership(ctx, store.GetMembershipParams{TayfaID: inv.TayfaID, UserID: caller.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Preview{}, fmt.Errorf("üyelik: %w", err)
	case m.Status == string(authz.StatusBanned):
		return Preview{}, apperr.ErrBanned
	case m.Status == string(authz.StatusActive), m.Status == string(authz.StatusPending):
		already = true
	}
	n, err := s.q.CountActiveMembers(ctx, inv.TayfaID)
	if err != nil {
		return Preview{}, fmt.Errorf("üye sayısı: %w", err)
	}
	return Preview{TayfaID: tayfa, Name: inv.Name, MemberCount: int32(min(n, 1<<31-1)), AlreadyMember: already}, nil //nolint:gosec // clamped
}

// JoinByInvite joins the tayfa behind token. The link is locked for the
// whole transaction: expiry, revocation and max_uses are checked and
// use_count is raised together, so a link with max_uses N admits at most N
// users even under concurrent use.
func (s *Service) JoinByInvite(ctx context.Context, caller id.ID, token string) (Member, error) {
	hash, err := hashToken(token)
	if err != nil {
		return Member{}, err
	}
	var out Member
	err = txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		inv, err := q.LockInviteByHash(ctx, hash)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrInviteInvalid
		}
		if err != nil {
			return fmt.Errorf("davet kilidi: %w", err)
		}
		tayfa, _ := id.FromPG(inv.TayfaID)
		t, err := lockTayfa(ctx, q, tayfa)
		if err != nil {
			if errors.Is(err, apperr.ErrTayfaNotFound) {
				return apperr.ErrInviteInvalid
			}
			return err
		}
		if t.readOnly {
			return apperr.ErrInviteInvalid
		}
		if err := usable(inv.RevokedAt, inv.ExpiresAt, inv.MaxUses, inv.UseCount, s.now()); err != nil {
			return err
		}
		if err := s.checkNotMember(ctx, q, tayfa, caller, apperr.ErrBanned); err != nil {
			return err
		}

		// v14 "Tayfaya davet": active when the tayfa is open, or when the
		// link's creator is an active moderator or admin right now.
		st := authz.StatusPending
		if t.joinPolicy == "open" {
			st = authz.StatusActive
		} else if inv.CreatedBy.Valid {
			cm, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: inv.TayfaID, UserID: inv.CreatedBy})
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return fmt.Errorf("davet eden: %w", err)
			case cm.Status == string(authz.StatusActive) && (cm.Role == "admin" || cm.Role == "moderator"):
				st = authz.StatusActive
			}
		}
		inviteID, _ := id.FromPG(inv.ID)
		out, err = s.join(ctx, q, tayfa, caller, st, "invite_link", inviteID)
		if err != nil {
			return err
		}
		n, err := q.IncrementInviteUse(ctx, inv.ID)
		if err != nil {
			return fmt.Errorf("davet kullanımı: %w", err)
		}
		if n != 1 {
			// The row is locked, so only the clock can have moved between
			// the check above and the database's now().
			return apperr.ErrInviteExpired
		}
		return nil
	})
	return out, err
}

// checkNotMember refuses users who already have a membership. banned is the
// error for a banned user (each join path has its own).
func (s *Service) checkNotMember(ctx context.Context, q *store.Queries, tayfa, user id.ID, banned error) error {
	m, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return fmt.Errorf("üyelik: %w", err)
	}
	switch authz.Status(m.Status) {
	case authz.StatusBanned:
		return banned
	case authz.StatusActive:
		return apperr.ErrAlreadyMember
	default:
		// pending, or a direct invitation waiting for an answer (not
		// produced in this version).
		return apperr.ErrAlreadyPending
	}
}

func (s *Service) join(ctx context.Context, q *store.Queries, tayfa, user id.ID, st authz.Status, via string, invite id.ID) (Member, error) {
	p := store.CreateMembershipParams{
		TayfaID: tayfa.PG(), UserID: user.PG(), Role: authz.Member.String(), Status: string(st), JoinedVia: via,
	}
	if invite != (id.ID{}) {
		p.InviteID = invite.PG()
	}
	now := s.now()
	if st == authz.StatusActive {
		p.JoinedAt = pgtype.Timestamptz{Time: now, Valid: true}
	}
	if err := q.CreateMembership(ctx, p); err != nil {
		return Member{}, fmt.Errorf("üyelik: %w", err)
	}
	change := changeJoined
	if st == authz.StatusPending {
		change = changeRequested
	}
	meta := map[string]any{"via": via}
	if invite != (id.ID{}) {
		meta["invite_id"] = invite.String()
	}
	if err := s.membershipChanged(ctx, q, tayfa, user, user, change, meta); err != nil {
		return Member{}, err
	}
	m := Member{TayfaID: tayfa, UserID: user, Role: authz.Member, Status: st}
	if st == authz.StatusActive {
		m.JoinedAt = now
	}
	return m, nil
}

// hashToken checks the shape of an invite token and returns its SHA-256.
func hashToken(token string) ([]byte, error) {
	if token == "" {
		return nil, apperr.Validation("token")
	}
	if len(token) != base64.RawURLEncoding.EncodedLen(inviteTokenLen) {
		return nil, apperr.ErrInviteInvalid
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != inviteTokenLen {
		return nil, apperr.ErrInviteInvalid
	}
	h := sha256.Sum256(raw)
	return h[:], nil
}

// usable reports why an invite link cannot be used, or nil.
func usable(revoked, expires pgtype.Timestamptz, maxUses pgtype.Int4, used int32, now time.Time) error {
	switch {
	case revoked.Valid:
		return apperr.ErrInviteRevoked
	case !expires.Time.After(now):
		return apperr.ErrInviteExpired
	case maxUses.Valid && used >= maxUses.Int32:
		return apperr.ErrInviteExhausted
	}
	return nil
}

// ---------------------------------------------------------------------------
// Invite links

// CreateInviteLink creates an invite link and returns it with its token. The
// token is returned only here; core keeps only its SHA-256 hash.
func (s *Service) CreateInviteLink(ctx context.Context, caller, tayfa id.ID, ttl time.Duration, maxUses int32) (Invite, string, error) {
	if ttl < MinInviteTTL || ttl > MaxInviteTTL {
		return Invite{}, "", apperr.Validation("ttl")
	}
	if maxUses < 1 || maxUses > MaxInviteUses {
		return Invite{}, "", apperr.Validation("max_uses")
	}
	var raw [inviteTokenLen]byte
	_, _ = rand.Read(raw[:]) // crypto/rand.Read never returns an error
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256(raw[:])

	var out Invite
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		t, _, err := s.actorFor(ctx, q, tayfa, caller, authz.InviteCreate, true)
		if err != nil {
			return err
		}
		if t.readOnly {
			return apperr.ErrTayfaReadOnly
		}
		inv := id.New()
		if err := q.CreateInvite(ctx, store.CreateInviteParams{
			ID: inv.PG(), TayfaID: tayfa.PG(), TokenHash: hash[:], CreatedBy: caller.PG(),
			ExpiresAt: pgtype.Timestamptz{Time: s.now().Add(ttl), Valid: true},
			MaxUses:   pgtype.Int4{Int32: maxUses, Valid: true},
		}); err != nil {
			return fmt.Errorf("davet: %w", err)
		}
		row, err := q.GetInvite(ctx, store.GetInviteParams{TayfaID: tayfa.PG(), ID: inv.PG()})
		if err != nil {
			return fmt.Errorf("davet: %w", err)
		}
		out = Invite{
			ID: inv, TayfaID: tayfa, CreatedBy: caller, CreatedAt: row.CreatedAt.Time.UTC(),
			ExpiresAt: row.ExpiresAt.Time.UTC(), MaxUses: maxUses,
		}
		meta := map[string]any{"max_uses": maxUses, "ttl_seconds": int64(ttl / time.Second)}
		return s.inviteChanged(ctx, q, tayfa, caller, inv, "invite.created", meta)
	})
	if err != nil {
		return Invite{}, "", err
	}
	return out, token, nil
}

// ListInviteLinks lists live invite links: every link for callers who may
// revoke any link, otherwise only the caller's own.
func (s *Service) ListInviteLinks(ctx context.Context, caller, tayfa id.ID) ([]Invite, error) {
	_, m, err := s.actorFor(ctx, s.q, tayfa, caller, authz.InviteList, false)
	if err != nil {
		return nil, err
	}
	p := store.ListLiveInvitesParams{TayfaID: tayfa.PG()}
	if !authz.Allowed(m, authz.InviteRevokeAny, s.now()) {
		p.CreatedBy = caller.PG()
	}
	rows, err := s.q.ListLiveInvites(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("davetler: %w", err)
	}
	out := make([]Invite, 0, len(rows))
	for _, r := range rows {
		inv, _ := id.FromPG(r.ID)
		by, _ := id.FromPG(r.CreatedBy)
		out = append(out, Invite{
			ID: inv, TayfaID: tayfa, CreatedBy: by, CreatedAt: r.CreatedAt.Time.UTC(),
			ExpiresAt: r.ExpiresAt.Time.UTC(), MaxUses: r.MaxUses.Int32, UseCount: r.UseCount,
		})
	}
	return out, nil
}

// RevokeInviteLink revokes an invite link. Members may revoke their own
// links, admins any link of the tayfa. Revoking twice changes nothing. It
// works on a read-only tayfa, since it only narrows access.
func (s *Service) RevokeInviteLink(ctx context.Context, caller, tayfa, invite id.ID) error {
	return txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		_, m, err := s.actorFor(ctx, q, tayfa, caller, authz.InviteRevokeOwn, false)
		if err != nil {
			return err
		}
		inv, err := q.GetInvite(ctx, store.GetInviteParams{TayfaID: tayfa.PG(), ID: invite.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrInviteNotFound
		}
		if err != nil {
			return fmt.Errorf("davet: %w", err)
		}
		own := inv.CreatedBy.Valid && id.ID(inv.CreatedBy.Bytes) == caller
		if !own && !authz.Allowed(m, authz.InviteRevokeAny, s.now()) {
			// Another member's link looks like no link at all.
			return apperr.ErrInviteNotFound
		}
		n, err := q.RevokeInvite(ctx, store.RevokeInviteParams{TayfaID: tayfa.PG(), ID: invite.PG()})
		if err != nil {
			return fmt.Errorf("davet iptali: %w", err)
		}
		if n == 0 {
			return nil // already revoked
		}
		return s.inviteChanged(ctx, q, tayfa, caller, invite, "invite.revoked", map[string]any{"own": own})
	})
}

// ---------------------------------------------------------------------------
// Lists

// ListMembers lists members with status active (also for the zero status)
// or banned.
func (s *Service) ListMembers(ctx context.Context, caller, tayfa id.ID, st authz.Status, size int32, token string) (Page[Member], error) {
	if st == "" {
		st = authz.StatusActive
	}
	if st != authz.StatusActive && st != authz.StatusBanned {
		return Page[Member]{}, apperr.Validation("status")
	}
	size, err := pageSize(size)
	if err != nil {
		return Page[Member]{}, err
	}
	c, after, at, ok, err := decodeCursor(token, "ListMembers", tayfa, string(st))
	if err != nil {
		return Page[Member]{}, err
	}
	_, m, err := s.actorFor(ctx, s.q, tayfa, caller, authz.MemberList, false)
	if err != nil {
		return Page[Member]{}, err
	}
	now := s.now()
	// Banned members: the roles allowed to ban. This is a read, so a mute
	// does not hide it.
	seesMod := authz.Allowed(unmuted(m), authz.MemberBan, now)
	if st == authz.StatusBanned && !seesMod {
		return Page[Member]{}, apperr.ErrPermissionDenied
	}
	p := store.ListMembersPageParams{TayfaID: tayfa.PG(), Status: string(st), AfterRank: 4, PageSize: size + 1}
	if ok {
		p.AfterRank, p.AfterTime, p.AfterUserID = c.Rank, pgtype.Timestamptz{Time: at, Valid: true}, after.PG()
	} else {
		p.AfterTime, p.AfterUserID = pgtype.Timestamptz{Time: time.Unix(0, 0), Valid: true}, id.ID{}.PG()
	}
	rows, err := s.q.ListMembersPage(ctx, p)
	if err != nil {
		return Page[Member]{}, fmt.Errorf("üyeler: %w", err)
	}
	var out Page[Member]
	for i, r := range rows {
		if i == int(size) {
			last := rows[i-1]
			role, _ := authz.ParseRole(last.Role)
			out.Next = cursor{
				Method: "ListMembers", Tayfa: tayfa.String(), Status: string(st), Rank: int32(role), //nolint:gosec // role is 1..3
				Micros: last.SortTime.Time.UnixMicro(), User: id.ID(last.UserID.Bytes).String(),
			}.encode()
			break
		}
		u, _ := id.FromPG(r.UserID)
		mem, err := toMember(tayfa, u, r.Role, r.Status, r.JoinedAt, r.MutedUntil, now, seesMod || u == caller)
		if err != nil {
			return Page[Member]{}, err
		}
		out.Items = append(out.Items, mem)
	}
	return out, nil
}

// ListPending lists join requests, oldest first, with where they came from.
func (s *Service) ListPending(ctx context.Context, caller, tayfa id.ID, size int32, token string) (Page[Pending], error) {
	size, err := pageSize(size)
	if err != nil {
		return Page[Pending]{}, err
	}
	_, after, at, ok, err := decodeCursor(token, "ListPending", tayfa, "")
	if err != nil {
		return Page[Pending]{}, err
	}
	if _, _, err := s.actorFor(ctx, s.q, tayfa, caller, authz.MemberListPending, false); err != nil {
		return Page[Pending]{}, err
	}
	p := store.ListPendingPageParams{TayfaID: tayfa.PG(), PageSize: size + 1,
		AfterTime: pgtype.Timestamptz{Time: time.Unix(0, 0), Valid: true}, AfterUserID: id.ID{}.PG()}
	if ok {
		p.AfterTime, p.AfterUserID = pgtype.Timestamptz{Time: at, Valid: true}, after.PG()
	}
	rows, err := s.q.ListPendingPage(ctx, p)
	if err != nil {
		return Page[Pending]{}, fmt.Errorf("başvurular: %w", err)
	}
	var out Page[Pending]
	for i, r := range rows {
		if i == int(size) {
			last := rows[i-1]
			out.Next = cursor{Method: "ListPending", Tayfa: tayfa.String(),
				Micros: last.CreatedAt.Time.UnixMicro(), User: id.ID(last.UserID.Bytes).String()}.encode()
			break
		}
		u, _ := id.FromPG(r.UserID)
		inviter, _ := id.FromPG(r.InviterUserID)
		inv, _ := id.FromPG(r.InviteID)
		out.Items = append(out.Items, Pending{
			Member:      Member{TayfaID: tayfa, UserID: u, Role: authz.Member, Status: authz.StatusPending},
			RequestedAt: r.CreatedAt.Time.UTC(), JoinedVia: r.JoinedVia, InviterID: inviter, InviteID: inv,
		})
	}
	return out, nil
}

// GetMembership returns the caller's own membership in any status. target
// must be the caller.
func (s *Service) GetMembership(ctx context.Context, caller, tayfa, target id.ID) (Member, error) {
	if target != caller {
		return Member{}, apperr.ErrPermissionDenied
	}
	if _, err := s.q.GetTayfa(ctx, tayfa.PG()); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Member{}, apperr.ErrTayfaNotFound
		}
		return Member{}, fmt.Errorf("tayfa: %w", err)
	}
	r, err := s.q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: caller.PG()})
	if errors.Is(err, pgx.ErrNoRows) {
		return Member{}, apperr.ErrTayfaNotFound
	}
	if err != nil {
		return Member{}, fmt.Errorf("üyelik: %w", err)
	}
	return toMember(tayfa, caller, r.Role, r.Status, r.JoinedAt, r.MutedUntil, s.now(), true)
}

// ---------------------------------------------------------------------------
// Join requests and moderation

// ApproveMember makes a pending membership active.
func (s *Service) ApproveMember(ctx context.Context, caller, tayfa, target id.ID) (Member, error) {
	var out Member
	err := s.moderate(ctx, caller, tayfa, target, authz.MemberApprove, func(q *store.Queries, tm store.GetMembershipRow, _ authz.Role) error {
		if tm.Status != string(authz.StatusPending) {
			return apperr.ErrMemberNotFound
		}
		n, err := q.ApproveMembership(ctx, store.ApproveMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG(), ApprovedBy: caller.PG()})
		if err != nil {
			return fmt.Errorf("onay: %w", err)
		}
		if n != 1 {
			return apperr.ErrMemberNotFound
		}
		if err := s.membershipChanged(ctx, q, tayfa, caller, target, changeApproved, nil); err != nil {
			return err
		}
		out, err = s.reload(ctx, q, tayfa, target)
		return err
	})
	return out, err
}

// RejectMember deletes a pending membership; the user may ask again later.
func (s *Service) RejectMember(ctx context.Context, caller, tayfa, target id.ID) error {
	return s.moderate(ctx, caller, tayfa, target, authz.MemberReject, func(q *store.Queries, tm store.GetMembershipRow, _ authz.Role) error {
		if tm.Status != string(authz.StatusPending) {
			return apperr.ErrMemberNotFound
		}
		if _, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()}); err != nil {
			return fmt.Errorf("ret: %w", err)
		}
		return s.membershipChanged(ctx, q, tayfa, caller, target, changeRejected, nil)
	})
}

// ChangeRole changes an active member's role. On another member: the
// target's role must be lower than the caller's and the new role not higher.
// On oneself: stepping down only, and never as the last active admin.
func (s *Service) ChangeRole(ctx context.Context, caller, tayfa, target id.ID, newRole authz.Role) (Member, error) {
	if !newRole.Valid() {
		return Member{}, apperr.Validation("new_role")
	}
	// Lowering one's own role is its own permission: a moderator may step
	// down even though only admins change other members' roles.
	action := authz.MemberChangeRole
	if target == caller {
		action = authz.RoleStepDown
	}
	var out Member
	err := s.moderate(ctx, caller, tayfa, target, action, func(q *store.Queries, tm store.GetMembershipRow, actorRole authz.Role) error {
		if tm.Status != string(authz.StatusActive) {
			return apperr.ErrMemberNotFound
		}
		cur, err := authz.ParseRole(tm.Role)
		if err != nil {
			return fmt.Errorf("rol: %w", err)
		}
		if newRole == cur {
			return apperr.Validation("new_role")
		}
		if target == caller {
			if !authz.CanDemoteSelf(cur, newRole) {
				return apperr.ErrRoleHierarchy
			}
			if cur == authz.Admin {
				others, err := q.CountOtherActiveAdmins(ctx, store.CountOtherActiveAdminsParams{TayfaID: tayfa.PG(), UserID: caller.PG()})
				if err != nil {
					return fmt.Errorf("yöneticiler: %w", err)
				}
				if others == 0 {
					return apperr.ErrLastAdmin
				}
			}
		} else if !authz.CanActOn(actorRole, cur) || !authz.CanAssign(actorRole, newRole) {
			return apperr.ErrRoleHierarchy
		}
		n, err := q.SetMembershipRole(ctx, store.SetMembershipRoleParams{TayfaID: tayfa.PG(), UserID: target.PG(), Role: newRole.String()})
		if err != nil {
			return fmt.Errorf("rol: %w", err)
		}
		if n != 1 {
			return apperr.ErrMemberNotFound
		}
		meta := map[string]any{"from": cur.String(), "to": newRole.String()}
		if err := s.membershipChanged(ctx, q, tayfa, caller, target, changeRoleChanged, meta); err != nil {
			return err
		}
		out, err = s.reload(ctx, q, tayfa, target)
		return err
	})
	return out, err
}

// MuteMember mutes an active member of a lower role until until. Muting
// again replaces the end time.
func (s *Service) MuteMember(ctx context.Context, caller, tayfa, target id.ID, until time.Time) (Member, error) {
	now := s.now()
	if !until.After(now) || until.After(now.Add(MaxMuteAhead)) {
		return Member{}, apperr.Validation("expire_time")
	}
	until = until.UTC()
	var out Member
	err := s.moderate(ctx, caller, tayfa, target, authz.MemberMute, func(q *store.Queries, tm store.GetMembershipRow, actorRole authz.Role) error {
		if err := onLowerActive(tm, actorRole, caller, target); err != nil {
			return err
		}
		n, err := q.MuteMembership(ctx, store.MuteMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG(),
			MutedUntil: pgtype.Timestamptz{Time: until, Valid: true}, MutedBy: caller.PG()})
		if err != nil {
			return fmt.Errorf("susturma: %w", err)
		}
		if n != 1 {
			return apperr.ErrMemberNotFound
		}
		meta := map[string]any{"until": until.Format(time.RFC3339)}
		if err := s.membershipChanged(ctx, q, tayfa, caller, target, changeMuted, meta); err != nil {
			return err
		}
		out, err = s.reload(ctx, q, tayfa, target)
		return err
	})
	return out, err
}

// UnmuteMember ends a mute at once. A member who is not muted is left as is.
func (s *Service) UnmuteMember(ctx context.Context, caller, tayfa, target id.ID) (Member, error) {
	var out Member
	err := s.moderate(ctx, caller, tayfa, target, authz.MemberMute, func(q *store.Queries, tm store.GetMembershipRow, actorRole authz.Role) error {
		if err := onLowerActive(tm, actorRole, caller, target); err != nil {
			return err
		}
		if tm.MutedUntil.Valid && tm.MutedUntil.Time.After(s.now()) {
			if _, err := q.UnmuteMembership(ctx, store.UnmuteMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()}); err != nil {
				return fmt.Errorf("susturma kaldırma: %w", err)
			}
			if err := s.membershipChanged(ctx, q, tayfa, caller, target, changeUnmuted, nil); err != nil {
				return err
			}
		}
		var err error
		out, err = s.reload(ctx, q, tayfa, target)
		return err
	})
	return out, err
}

// BanMember bans an active or pending member of a lower role (a pending user
// counts as member). The row is kept so the user cannot join again.
func (s *Service) BanMember(ctx context.Context, caller, tayfa, target id.ID) error {
	return s.moderate(ctx, caller, tayfa, target, authz.MemberBan, func(q *store.Queries, tm store.GetMembershipRow, actorRole authz.Role) error {
		if tm.Status != string(authz.StatusActive) && tm.Status != string(authz.StatusPending) {
			return apperr.ErrMemberNotFound
		}
		if err := onLower(tm, actorRole, caller, target); err != nil {
			return err
		}
		n, err := q.BanMembership(ctx, store.BanMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()})
		if err != nil {
			return fmt.Errorf("ban: %w", err)
		}
		if n != 1 {
			return apperr.ErrMemberNotFound
		}
		return s.membershipChanged(ctx, q, tayfa, caller, target, changeBanned, map[string]any{"from_status": tm.Status})
	})
}

// RemoveMember removes an active member of a lower role; they may join again.
func (s *Service) RemoveMember(ctx context.Context, caller, tayfa, target id.ID) error {
	return s.moderate(ctx, caller, tayfa, target, authz.MemberRemove, func(q *store.Queries, tm store.GetMembershipRow, actorRole authz.Role) error {
		if err := onLowerActive(tm, actorRole, caller, target); err != nil {
			return err
		}
		n, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()})
		if err != nil {
			return fmt.Errorf("çıkarma: %w", err)
		}
		if n != 1 {
			return apperr.ErrMemberNotFound
		}
		return s.membershipChanged(ctx, q, tayfa, caller, target, changeRemoved, nil)
	})
}

// LeaveTayfa ends the caller's active membership or withdraws their pending
// request. A banned membership cannot be left (that would erase the ban). It
// works on a read-only tayfa.
func (s *Service) LeaveTayfa(ctx context.Context, caller, tayfa id.ID) error {
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		if _, err := lockTayfa(ctx, q, tayfa); err != nil {
			return err
		}
		m, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: caller.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrTayfaNotFound
		}
		if err != nil {
			return fmt.Errorf("üyelik: %w", err)
		}
		change := changeLeft
		switch authz.Status(m.Status) {
		case authz.StatusActive:
			if m.Role == authz.Admin.String() {
				others, err := q.CountOtherActiveAdmins(ctx, store.CountOtherActiveAdminsParams{TayfaID: tayfa.PG(), UserID: caller.PG()})
				if err != nil {
					return fmt.Errorf("yöneticiler: %w", err)
				}
				if others == 0 {
					return apperr.ErrLastAdmin
				}
			}
		case authz.StatusPending:
			change = changeWithdrawn
		default:
			return apperr.ErrTayfaNotFound
		}
		if _, err := q.DeleteMembership(ctx, store.DeleteMembershipParams{TayfaID: tayfa.PG(), UserID: caller.PG()}); err != nil {
			return fmt.Errorf("ayrılma: %w", err)
		}
		return s.membershipChanged(ctx, q, tayfa, caller, caller, change, nil)
	})
	return err
}

// moderate runs fn for an action of caller on target's membership, in one
// transaction with the tayfa locked. It checks the caller's membership and
// permission, that the tayfa is writable, and that target has a membership.
func (s *Service) moderate(ctx context.Context, caller, tayfa, target id.ID, a authz.Action, fn func(q *store.Queries, target store.GetMembershipRow, actorRole authz.Role) error) error {
	err := txn.WithActor(ctx, s.pool, caller, func(_ pgx.Tx, q *store.Queries) error {
		t, m, err := s.actorFor(ctx, q, tayfa, caller, a, true)
		if err != nil {
			return err
		}
		if t.readOnly {
			return apperr.ErrTayfaReadOnly
		}
		tm, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrMemberNotFound
		}
		if err != nil {
			return fmt.Errorf("hedef üyelik: %w", err)
		}
		return fn(q, tm, m.Role)
	})
	return err
}

// onLowerActive requires an active target whose role is below the caller's.
func onLowerActive(tm store.GetMembershipRow, actor authz.Role, caller, target id.ID) error {
	if tm.Status != string(authz.StatusActive) {
		return apperr.ErrMemberNotFound
	}
	return onLower(tm, actor, caller, target)
}

func onLower(tm store.GetMembershipRow, actor authz.Role, caller, target id.ID) error {
	if target == caller {
		return apperr.ErrRoleHierarchy
	}
	role, err := authz.ParseRole(tm.Role)
	if err != nil {
		return fmt.Errorf("rol: %w", err)
	}
	if !authz.CanActOn(actor, role) {
		return apperr.ErrRoleHierarchy
	}
	return nil
}

func (s *Service) reload(ctx context.Context, q *store.Queries, tayfa, user id.ID) (Member, error) {
	r, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: user.PG()})
	if err != nil {
		return Member{}, fmt.Errorf("üyelik: %w", err)
	}
	return toMember(tayfa, user, r.Role, r.Status, r.JoinedAt, r.MutedUntil, s.now(), true)
}

// ---------------------------------------------------------------------------
// Caller and tayfa

type tayfaState struct {
	id         id.ID
	joinPolicy string
	readOnly   bool
}

func stateOf(tayfa id.ID, t access.Tayfa) tayfaState {
	return tayfaState{id: tayfa, joinPolicy: t.JoinPolicy, readOnly: access.ReadOnly(t)}
}

// lockTayfa locks the tayfa row for a membership change (the same lock the
// memberships trigger takes).
func lockTayfa(ctx context.Context, q *store.Queries, tayfa id.ID) (tayfaState, error) {
	t, err := q.GetTayfaForUpdate(ctx, tayfa.PG())
	if errors.Is(err, pgx.ErrNoRows) {
		return tayfaState{}, apperr.ErrTayfaNotFound
	}
	if err != nil {
		return tayfaState{}, fmt.Errorf("tayfa kilidi: %w", err)
	}
	return stateOf(tayfa, access.Tayfa(t)), nil
}

// actorFor loads the tayfa and the caller's active membership (access.Load:
// anyone else gets ErrTayfaNotFound) and checks action a. With lock the
// tayfa row is locked (writes).
func (s *Service) actorFor(ctx context.Context, q *store.Queries, tayfa, caller id.ID, a authz.Action, lock bool) (tayfaState, authz.Membership, error) {
	t, m, err := access.Load(ctx, q, tayfa, caller, lock)
	if err != nil {
		return tayfaState{}, authz.Membership{}, err
	}
	if err := access.Authorize(m, a, s.now()); err != nil {
		return tayfaState{}, authz.Membership{}, err
	}
	return stateOf(tayfa, t), m, nil
}

func unmuted(m authz.Membership) authz.Membership {
	m.MutedUntil = time.Time{}
	return m
}

func toMember(tayfa, user id.ID, role, st string, joined, muted pgtype.Timestamptz, now time.Time, showMute bool) (Member, error) {
	r, err := authz.ParseRole(role)
	if err != nil {
		return Member{}, fmt.Errorf("rol: %w", err)
	}
	status, err := authz.ParseStatus(st)
	if err != nil {
		return Member{}, fmt.Errorf("durum: %w", err)
	}
	m := Member{TayfaID: tayfa, UserID: user, Role: r, Status: status}
	if joined.Valid {
		m.JoinedAt = joined.Time.UTC()
	}
	if showMute && muted.Valid && muted.Time.After(now) {
		m.MutedUntil = muted.Time.UTC()
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Audit log and events

// Membership change kinds, in membership.changed payloads and as audit
// actions "member.<kind>".
const (
	changeJoined      = "joined"
	changeRequested   = "requested"
	changeApproved    = "approved"
	changeRejected    = "rejected"
	changeRoleChanged = "role_changed"
	changeMuted       = "muted"
	changeUnmuted     = "unmuted"
	changeBanned      = "banned"
	changeRemoved     = "removed"
	changeLeft        = "left"
	changeWithdrawn   = "withdrawn"
)

// EventMembershipChanged is the outbox event of every membership change
// (ADR-0009). Its payload holds only IDs and the kind of change.
const EventMembershipChanged = "membership.changed"

// membershipChanged writes the audit log row and the membership.changed
// event. meta is audit metadata: IDs and classification only, never tokens
// or personal data.
func (s *Service) membershipChanged(ctx context.Context, q *store.Queries, tayfa, actor, user id.ID, change string, meta map[string]any) error {
	target := txn.Target{Type: "membership", ID: user}
	if err := txn.Audit(ctx, q, tayfa, actor, "member."+change, target, meta); err != nil {
		return err
	}
	payload := map[string]any{"tayfa_id": tayfa.String(), "user_id": user.String(), "change": change}
	return txn.Event(ctx, q, EventMembershipChanged, tayfa, actor, target, payload)
}

func (s *Service) inviteChanged(ctx context.Context, q *store.Queries, tayfa, actor, invite id.ID, action string, meta map[string]any) error {
	target := txn.Target{Type: "invite", ID: invite}
	if err := txn.Audit(ctx, q, tayfa, actor, action, target, meta); err != nil {
		return err
	}
	payload := map[string]any{"tayfa_id": tayfa.String(), "invite_id": invite.String()}
	return txn.Event(ctx, q, action, tayfa, actor, target, payload)
}
