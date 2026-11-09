// Package announcement publishes, edits, pins, deletes and restores tayfa
// announcements (AnnouncementService, proto/tayfam/core/v1/announcement.proto;
// data model v14 "Duyuru ve Etkinlik", "Silinen içeriği geri alma"; issue
// #48).
//
// Every method starts the same way: the caller comes from the verified
// token, access.Load checks for an active membership (anyone else gets
// apperr.ErrTayfaNotFound), access.RequireModule refuses every method,
// reads included, while the tayfa has the announcements module turned off,
// and access.Authorize asks the permission table. Only moderators and
// admins publish (decision 2026-11-09). Changing someone else's
// announcement also follows the role hierarchy against the author's
// current role (authz.CanActOnContent).
//
// Writes run in txn.WithActor with the tayfa row locked and record an audit
// log entry and an outbox event in the same transaction. Neither ever holds
// the title or the body; only IDs and the kind of change.
package announcement

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
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Limits (veri modeli v14 "Uzunluk sınırları"; the same CHECKs are in the
// database).
const (
	TitleMax = 120
	BodyMax  = 5000
	// RestoreWindow is how long a moderator's or admin's deletion can be
	// undone (v14 "Silinen içeriği geri alma").
	RestoreWindow = 30 * 24 * time.Hour
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// Update mask paths (UpdateAnnouncementRequest.update_mask).
const (
	FieldTitle = "title"
	FieldBody  = "body"
)

// Event types. Payloads hold the tayfa and announcement IDs, and for
// updates the kind of change; never the title or the body.
const (
	EventCreated = "announcement.created"
	EventUpdated = "announcement.updated"
	EventDeleted = "announcement.deleted"
)

// Kinds of announcement.updated, also the audit action suffixes.
const (
	changeEdited   = "edited"
	changePinned   = "pinned"
	changeUnpinned = "unpinned"
	changeRestored = "restored"
)

const subjectType = "announcement"

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	Now  func() time.Time
}

// Service implements AnnouncementService's business logic.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil {
		return nil, errors.New("announcement: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// View is an announcement as the tayfa's active members see it.
type View struct {
	ID      id.ID
	TayfaID id.ID
	// AuthorID is zero when the author's account was deleted.
	AuthorID  id.ID
	Title     string
	Body      string
	Pinned    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateInput is a publish request.
type CreateInput struct {
	Title string
	Body  string
	// ClientRequestID is required: a UUID the client reuses on retry.
	ClientRequestID string
}

// Create publishes an announcement by user. A repeat with the same
// ClientRequestID by the same user returns the first announcement and
// records nothing.
func (s *Service) Create(ctx context.Context, user, tayfa id.ID, in CreateInput) (View, error) {
	title, err := cleanTitle(in.Title)
	if err != nil {
		return View{}, err
	}
	body, err := cleanBody(in.Body)
	if err != nil {
		return View{}, err
	}
	crid, ok := id.Parse(in.ClientRequestID)
	if !ok {
		return View{}, apperr.Validation("client_request_id")
	}

	var v View
	err = s.write(ctx, user, tayfa, authz.AnnouncementCreate, func(q *store.Queries, _ authz.Membership) error {
		prev, err := q.GetAnnouncementByClientRequest(ctx, store.GetAnnouncementByClientRequestParams{AuthorID: user.PG(), ClientRequestID: crid.PG()})
		switch {
		case err == nil:
			// The same request again: only valid for the same, still
			// visible announcement in the same tayfa.
			if id.ID(prev.TayfaID.Bytes) != tayfa || prev.DeletedAt.Valid {
				return apperr.Validation("client_request_id")
			}
			v, err = get(ctx, q, tayfa, id.ID(prev.ID.Bytes))
			return err
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("tekrar gönderim: %w", err)
		}

		aid := id.New()
		if err := q.CreateAnnouncement(ctx, store.CreateAnnouncementParams{
			ID: aid.PG(), TayfaID: tayfa.PG(), AuthorID: user.PG(), Title: title, Body: body, ClientRequestID: crid.PG(),
		}); err != nil {
			return fmt.Errorf("duyuru: %w", err)
		}
		if v, err = get(ctx, q, tayfa, aid); err != nil {
			return err
		}
		if err := txn.TouchTayfa(ctx, q, tayfa, s.now()); err != nil {
			return err
		}
		if err := record(ctx, q, tayfa, user, aid, "announcement.created", EventCreated, nil, nil); err != nil {
			return err
		}
		// Every active member hears about it, but the author (ADR-0009).
		to, err := notify.Members(ctx, q, tayfa, user)
		if err != nil {
			return err
		}
		return notify.Write(ctx, q, notify.Request{
			Type: notify.TypeAnnouncement, Tayfa: tayfa, Actor: user, TargetType: notify.TargetAnnouncement, Target: aid,
		}, to)
	})
	if err != nil {
		return View{}, err
	}
	return v, nil
}

// Get returns one visible announcement.
func (s *Service) Get(ctx context.Context, user, tayfa, announcement id.ID) (View, error) {
	if err := s.read(ctx, user, tayfa); err != nil {
		return View{}, err
	}
	return get(ctx, s.q, tayfa, announcement)
}

// UpdateInput is an edit request. Only the fields named in Fields change.
type UpdateInput struct {
	Fields []string
	Title  string
	Body   string
}

// Update edits an announcement: the author, or a moderator or admin above
// the author's current role. Setting the current values changes nothing
// and records nothing.
func (s *Service) Update(ctx context.Context, user, tayfa, announcement id.ID, in UpdateInput) (View, error) {
	if len(in.Fields) == 0 {
		return View{}, apperr.Validation("update_mask")
	}
	set := map[string]bool{}
	for _, f := range in.Fields {
		if f != FieldTitle && f != FieldBody {
			return View{}, apperr.Validation("update_mask")
		}
		set[f] = true
	}
	var (
		title, body string
		err         error
	)
	if set[FieldTitle] {
		if title, err = cleanTitle(in.Title); err != nil {
			return View{}, err
		}
	}
	if set[FieldBody] {
		if body, err = cleanBody(in.Body); err != nil {
			return View{}, err
		}
	}

	var v View
	err = s.write(ctx, user, tayfa, authz.AnnouncementEdit, func(q *store.Queries, m authz.Membership) error {
		row, err := s.lockVisible(ctx, q, tayfa, announcement)
		if err != nil {
			return err
		}
		if err := mayChange(ctx, q, tayfa, user, m, row.AuthorID); err != nil {
			return err
		}
		if !set[FieldTitle] {
			title = row.Title
		}
		if !set[FieldBody] {
			body = row.Body
		}
		var changed []string
		if title != row.Title {
			changed = append(changed, FieldTitle)
		}
		if body != row.Body {
			changed = append(changed, FieldBody)
		}
		if len(changed) > 0 {
			n, err := q.UpdateAnnouncementContent(ctx, store.UpdateAnnouncementContentParams{TayfaID: tayfa.PG(), ID: announcement.PG(), Title: title, Body: body})
			if err != nil {
				return fmt.Errorf("duyuru güncelleme: %w", err)
			}
			if n != 1 {
				return apperr.ErrAnnouncementNotFound
			}
		}
		if v, err = get(ctx, q, tayfa, announcement); err != nil || len(changed) == 0 {
			return err
		}
		return record(ctx, q, tayfa, user, announcement, "announcement."+changeEdited, EventUpdated,
			map[string]any{"fields": changed, "author_id": idString(row.AuthorID)},
			map[string]any{"change": changeEdited, "fields": changed})
	})
	if err != nil {
		return View{}, err
	}
	return v, nil
}

// SetPinned pins or unpins an announcement. Moderator and above, on any
// author's announcement: pinning arranges the tayfa's feed and does not
// change anyone's words. Setting the current value records nothing.
func (s *Service) SetPinned(ctx context.Context, user, tayfa, announcement id.ID, pinned bool) (View, error) {
	var v View
	err := s.write(ctx, user, tayfa, authz.AnnouncementPin, func(q *store.Queries, _ authz.Membership) error {
		row, err := s.lockVisible(ctx, q, tayfa, announcement)
		if err != nil {
			return err
		}
		if row.Pinned != pinned {
			n, err := q.SetAnnouncementPinned(ctx, store.SetAnnouncementPinnedParams{TayfaID: tayfa.PG(), ID: announcement.PG(), Pinned: pinned})
			if err != nil {
				return fmt.Errorf("sabitleme: %w", err)
			}
			if n != 1 {
				return apperr.ErrAnnouncementNotFound
			}
		}
		if v, err = get(ctx, q, tayfa, announcement); err != nil || row.Pinned == pinned {
			return err
		}
		change := changeUnpinned
		if pinned {
			change = changePinned
		}
		return record(ctx, q, tayfa, user, announcement, "announcement."+change, EventUpdated, nil, map[string]any{"change": change})
	})
	if err != nil {
		return View{}, err
	}
	return v, nil
}

// Delete removes an announcement. The author's own deletion empties the
// title and the body at once and cannot be undone. A moderator's or admin's
// deletion of someone else's announcement (hierarchy as in Update) hides it;
// an admin can restore it within RestoreWindow.
func (s *Service) Delete(ctx context.Context, user, tayfa, announcement id.ID) error {
	now := s.now()
	return s.write(ctx, user, tayfa, authz.AnnouncementDelete, func(q *store.Queries, m authz.Membership) error {
		row, err := s.lockVisible(ctx, q, tayfa, announcement)
		if err != nil {
			return err
		}
		if err := mayChange(ctx, q, tayfa, user, m, row.AuthorID); err != nil {
			return err
		}
		own := id.ID(row.AuthorID.Bytes) == user && row.AuthorID.Valid
		var n int64
		if own {
			n, err = q.EraseAnnouncement(ctx, store.EraseAnnouncementParams{TayfaID: tayfa.PG(), ID: announcement.PG(), DeletedAt: ts(now), DeletedBy: user.PG()})
		} else {
			n, err = q.HideAnnouncement(ctx, store.HideAnnouncementParams{TayfaID: tayfa.PG(), ID: announcement.PG(), DeletedAt: ts(now), DeletedBy: user.PG()})
		}
		if err != nil {
			return fmt.Errorf("duyuru silme: %w", err)
		}
		if n != 1 {
			return apperr.ErrAnnouncementNotFound
		}
		return record(ctx, q, tayfa, user, announcement, "announcement.deleted", EventDeleted,
			map[string]any{"author_id": idString(row.AuthorID), "restorable": !own}, nil)
	})
}

// Restore undoes a moderator's or admin's deletion within RestoreWindow.
// Admin only, and only above the author's current role. The author's own
// deletions cannot be restored (their content is gone). Restoring a visible
// announcement changes nothing.
func (s *Service) Restore(ctx context.Context, user, tayfa, announcement id.ID) (View, error) {
	now := s.now()
	var v View
	err := s.write(ctx, user, tayfa, authz.AnnouncementRestore, func(q *store.Queries, m authz.Membership) error {
		row, err := q.GetAnnouncementForUpdate(ctx, store.GetAnnouncementForUpdateParams{TayfaID: tayfa.PG(), ID: announcement.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrAnnouncementNotFound
		case err != nil:
			return fmt.Errorf("duyuru: %w", err)
		}
		if !row.DeletedAt.Valid {
			v, err = get(ctx, q, tayfa, announcement)
			return err
		}
		// Erased by its author, or past the window (the purge job removes
		// it): the same answer as for an unknown announcement.
		if row.Title == "" || now.Sub(row.DeletedAt.Time) > RestoreWindow {
			return apperr.ErrAnnouncementNotFound
		}
		if err := mayChange(ctx, q, tayfa, user, m, row.AuthorID); err != nil {
			return err
		}
		n, err := q.RestoreAnnouncement(ctx, store.RestoreAnnouncementParams{TayfaID: tayfa.PG(), ID: announcement.PG()})
		if err != nil {
			return fmt.Errorf("geri alma: %w", err)
		}
		if n != 1 {
			return apperr.ErrAnnouncementNotFound
		}
		if v, err = get(ctx, q, tayfa, announcement); err != nil {
			return err
		}
		return record(ctx, q, tayfa, user, announcement, "announcement."+changeRestored, EventUpdated,
			map[string]any{"author_id": idString(row.AuthorID), "deleted_by": idString(row.DeletedBy)},
			map[string]any{"change": changeRestored})
	})
	if err != nil {
		return View{}, err
	}
	return v, nil
}

// read checks a reader: active member, module on, TayfaView.
func (s *Service) read(ctx context.Context, user, tayfa id.ID) error {
	_, m, err := access.Load(ctx, s.q, tayfa, user, false)
	if err != nil {
		return err
	}
	if err := access.RequireModule(ctx, s.q, tayfa, access.ModuleAnnouncements); err != nil {
		return err
	}
	return access.Authorize(m, authz.TayfaView, s.now())
}

// write locks the tayfa, checks membership, module, permission and that the
// tayfa is not read-only, then runs fn, all in one transaction.
func (s *Service) write(ctx context.Context, user, tayfa id.ID, a authz.Action, fn func(q *store.Queries, m authz.Membership) error) error {
	now := s.now()
	return txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		t, m, err := access.Load(ctx, q, tayfa, user, true)
		if err != nil {
			return err
		}
		if err := access.RequireModule(ctx, q, tayfa, access.ModuleAnnouncements); err != nil {
			return err
		}
		if err := access.Authorize(m, a, now); err != nil {
			return err
		}
		if access.ReadOnly(t) {
			return apperr.ErrTayfaReadOnly
		}
		return fn(q, m)
	})
}

// lockVisible locks a visible announcement of tayfa.
func (s *Service) lockVisible(ctx context.Context, q *store.Queries, tayfa, announcement id.ID) (store.GetAnnouncementForUpdateRow, error) {
	row, err := q.GetAnnouncementForUpdate(ctx, store.GetAnnouncementForUpdateParams{TayfaID: tayfa.PG(), ID: announcement.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return row, apperr.ErrAnnouncementNotFound
	case err != nil:
		return row, fmt.Errorf("duyuru: %w", err)
	case row.DeletedAt.Valid:
		return row, apperr.ErrAnnouncementNotFound
	}
	return row, nil
}

// mayChange applies the hierarchy to someone else's announcement: the
// caller's role must be above the author's current role in this tayfa. An
// author who is no longer an active member has no role here.
func mayChange(ctx context.Context, q *store.Queries, tayfa, user id.ID, m authz.Membership, author pgtype.UUID) error {
	if author.Valid && id.ID(author.Bytes) == user {
		return nil
	}
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

func get(ctx context.Context, q *store.Queries, tayfa, announcement id.ID) (View, error) {
	r, err := q.GetAnnouncement(ctx, store.GetAnnouncementParams{TayfaID: tayfa.PG(), ID: announcement.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return View{}, apperr.ErrAnnouncementNotFound
	case err != nil:
		return View{}, fmt.Errorf("duyuru: %w", err)
	}
	return view(store.ListAnnouncementsPageRow(r)), nil
}

func view(r store.ListAnnouncementsPageRow) View {
	v := View{
		ID: id.ID(r.ID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes), Title: r.Title, Body: r.Body, Pinned: r.Pinned,
		CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC(),
	}
	if r.AuthorID.Valid {
		v.AuthorID = id.ID(r.AuthorID.Bytes)
	}
	return v
}

// record writes the audit record and the outbox event of a change, the event
// last (outbox.Write). audit is audit metadata, payload extra event fields;
// both hold IDs and classification only.
func record(ctx context.Context, q *store.Queries, tayfa, actor, announcement id.ID, action, event string, audit, payload map[string]any) error {
	target := txn.Target{Type: subjectType, ID: announcement}
	if err := txn.Audit(ctx, q, tayfa, actor, action, target, audit); err != nil {
		return err
	}
	p := map[string]any{"tayfa_id": tayfa.String(), "announcement_id": announcement.String()}
	for k, v := range payload {
		p[k] = v
	}
	return txn.Event(ctx, q, event, tayfa, actor, target, p)
}

// cleanTitle trims surrounding spaces: 1–120 characters on one line.
func cleanTitle(s string) (string, error) {
	if len(s) > (TitleMax+64)*maxRuneBytes {
		return "", apperr.Validation(FieldTitle)
	}
	t := strings.TrimSpace(s)
	if n := utf8.RuneCountInString(t); n < 1 || n > TitleMax || !validText(t, false) {
		return "", apperr.Validation(FieldTitle)
	}
	return t, nil
}

// cleanBody trims surrounding spaces: up to 5000 characters, may be empty.
func cleanBody(s string) (string, error) {
	if len(s) > (BodyMax+64)*maxRuneBytes {
		return "", apperr.Validation(FieldBody)
	}
	b := strings.TrimSpace(s)
	if utf8.RuneCountInString(b) > BodyMax || !validText(b, true) {
		return "", apperr.Validation(FieldBody)
	}
	return b, nil
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

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
