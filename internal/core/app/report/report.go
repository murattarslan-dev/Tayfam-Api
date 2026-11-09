// Package report files reports and runs a tayfa's moderation queue
// (ReportService, proto/tayfam/core/v1/report.proto; data model v14
// "Denetim", "Spam ve taciz", "Platforma yükselen şikâyetler", "Doğum Tarihi
// ve Şikâyet Önceliği"; Notion "Kötüye Kullanım ve Genç Kullanıcı
// Güvenliği", "Topluluk Kuralları"; ADR-0012; issue #76).
//
// Who learns who reported:
//
//   - The reported person never does: the queue does not list a report to
//     the person it is about, whatever their role, and nothing they can read
//     (audit log, notifications, events they could see) names the reporter.
//   - The tayfa's moderators and admins do not either. Notion only rules out
//     the reported person; the queue is run by fellow members who may be
//     close to the reported one, and a moderator may be the next subject.
//     Following ADR-0011 ("tutmadığın veri çalınamaz" / what is not returned
//     cannot leak), the moderator view (ModView) has no reporter field and
//     the queue queries do not read reporter_id. Creating a report writes
//     nothing to the tayfa's audit log (admins read it) and its outbox event
//     has no actor.
//   - The reporter sees their own reports and only whether action was taken
//     (Outcome), not which action and not who handled it; the report_resolved
//     notification has no actor.
//   - The platform panel (a separate issue) will read reporter_id.
//
// Priority (ADR-0012 "Şikâyetlerde yaş önceliği"): when a report is filed,
// core asks identity's IsMinor (MinorChecker) about the reporter and the
// reported person (the content's author). MinorInvolved when either is
// under 18, identity does not know them, or identity cannot be reached: a
// report never loses its priority to an identity outage. The call is made
// before the write transaction, so no lock is held while waiting. The
// priority is decided then and never changes (reports_guard); the answer
// itself is not kept, and nobody's age is ever stored, returned or logged.
//
// The priority is for the platform only. Tayfa admins and moderators never
// learn that a party is under 18 (ADR-0012): the queue (ModView) carries
// no priority and no escalation flag, and is ordered by time only.
//
// Escalation to the platform (v14 "Platforma yükselen şikâyetler"): at once
// for reports about the tayfa itself and about staff conduct (those go to
// the platform only and never reach the tayfa's queue), for child safety
// reports and for MinorInvolved; after seven days unresolved by the jobs
// package (EscalationAfter).
//
// Resolutions link to an action taken with the content's or the
// membership's own methods, which apply their own rules (hierarchy,
// restore window, legal hold later): the service only checks that the
// action happened (content no longer visible; member muted or banned).
package report

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

// Limits and periods.
const (
	// DescriptionMax is the longest description (issue #76; the same CHECK
	// is in the database).
	DescriptionMax = 1000
	// DailyLimit bounds the reports one user files in 24 hours, so reports
	// cannot be used to flood a tayfa's moderators.
	DailyLimit = 20
	// EscalationAfter: an open report goes to the platform after this long
	// (v14 "Platforma yükselen şikâyetler").
	EscalationAfter = 7 * 24 * time.Hour
	// Retention: resolved reports are deleted after this long (v14
	// "Saklama süreleri"); audit_logs too.
	Retention = 365 * 24 * time.Hour
	// maxRuneBytes bounds the byte length before counting characters.
	maxRuneBytes = 4
)

// TargetType is what a report is about (reports.target_type).
type TargetType string

// Target types.
const (
	TargetUser         TargetType = "user"
	TargetTayfa        TargetType = "tayfa"
	TargetAnnouncement TargetType = "announcement"
	TargetEvent        TargetType = "event"
	TargetQuestion     TargetType = "question"
	TargetAnswer       TargetType = "answer"
	TargetPoll         TargetType = "poll"
	TargetFishingCatch TargetType = "fishing_catch"
)

// content reports whether t is content (has an author and can be removed).
func (t TargetType) content() bool {
	switch t {
	case TargetAnnouncement, TargetEvent, TargetQuestion, TargetAnswer, TargetPoll, TargetFishingCatch:
		return true
	}
	return false
}

func (t TargetType) valid() bool { return t == TargetUser || t == TargetTayfa || t.content() }

// Reason is a report category (reports.reason; Notion "Topluluk Kuralları").
type Reason string

// Reasons.
const (
	ReasonHarassment        Reason = "harassment"
	ReasonHateSpeech        Reason = "hate_speech"
	ReasonViolence          Reason = "violence"
	ReasonPrivacy           Reason = "privacy"
	ReasonImpersonation     Reason = "impersonation"
	ReasonIllegalTrade      Reason = "illegal_trade"
	ReasonDrugs             Reason = "drugs"
	ReasonGambling          Reason = "gambling"
	ReasonDangerousActivity Reason = "dangerous_activity"
	ReasonFakeEvent         Reason = "fake_event"
	ReasonSexualContent     Reason = "sexual_content"
	// ReasonChildSafety ("Çocuk/genç güvenliği") goes first in the queue
	// and to the platform at once.
	ReasonChildSafety Reason = "child_safety"
	ReasonSpam        Reason = "spam"
	ReasonCopyright   Reason = "copyright"
	// ReasonStaffConduct: an admin's or moderator's use of their powers;
	// platform only.
	ReasonStaffConduct Reason = "staff_conduct"
	ReasonOther        Reason = "other"
)

// Reasons lists every reason, in the proto's order.
func Reasons() []Reason {
	return []Reason{
		ReasonHarassment, ReasonHateSpeech, ReasonViolence, ReasonPrivacy, ReasonImpersonation,
		ReasonIllegalTrade, ReasonDrugs, ReasonGambling, ReasonDangerousActivity, ReasonFakeEvent,
		ReasonSexualContent, ReasonChildSafety, ReasonSpam, ReasonCopyright, ReasonStaffConduct, ReasonOther,
	}
}

// Status of a report.
type Status string

// Statuses.
const (
	StatusOpen     Status = "open"
	StatusResolved Status = "resolved"
)

// Resolution of a resolved report.
type Resolution string

// Resolutions.
const (
	ResolutionContentRemoved   Resolution = "content_removed"
	ResolutionMemberSanctioned Resolution = "member_sanctioned"
	ResolutionDismissed        Resolution = "dismissed"
)

// Priority of a report.
type Priority string

// Priorities.
const (
	PriorityNormal        Priority = "normal"
	PriorityMinorInvolved Priority = "minor_involved"
)

// Outcome is all the reporter learns about a resolution.
type Outcome string

// Outcomes; empty while open.
const (
	OutcomeActionTaken Outcome = "action_taken"
	OutcomeDismissed   Outcome = "dismissed"
)

// Why a report went to the platform (reports.escalation_reason).
const (
	EscalationTayfaTarget   = "tayfa_target"
	EscalationStaffConduct  = "staff_conduct"
	EscalationChildSafety   = "child_safety"
	EscalationMinorInvolved = "minor_involved"
	EscalationUnresolved    = "unresolved"
)

// Outbox event types. Payloads hold IDs and classification only: never the
// description, the reporter or anyone's age. report.created has no actor.
//
//	report.created    tayfa_id, report_id, target_type, reason, priority
//	report.escalated  tayfa_id, report_id, escalation_reason, priority
//	report.resolved   tayfa_id, report_id, resolution
//
// report.escalated is the platform panel's hook for the platform owner's
// immediate notice (v14); the panel is a separate issue.
const (
	EventCreated   = "report.created"
	EventEscalated = "report.escalated"
	EventResolved  = "report.resolved"
)

const subjectType = "report"

// MinorChecker answers whether users are under 18 (identity's IsMinor,
// package identityminor). Users missing from the answer are unknown.
type MinorChecker interface {
	IsMinor(ctx context.Context, users []id.ID) (map[id.ID]bool, error)
}

// Config wires a Service.
type Config struct {
	Pool *pgxpool.Pool
	// Minors is required. An error from it counts as "under 18".
	Minors MinorChecker
	Now    func() time.Time
}

// Service implements ReportService's business logic.
type Service struct {
	cfg Config
	q   *store.Queries
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Pool == nil || cfg.Minors == nil {
		return nil, errors.New("report: eksik yapılandırma")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, q: store.New(cfg.Pool)}, nil
}

// now is UTC with the database's microsecond precision.
func (s *Service) now() time.Time { return s.cfg.Now().UTC().Truncate(time.Microsecond) }

// MyView is a report as its reporter sees it.
type MyView struct {
	ID          id.ID
	TayfaID     id.ID
	TargetType  TargetType
	TargetID    id.ID
	Reason      Reason
	Description string
	Status      Status
	Outcome     Outcome // empty while open
	CreatedAt   time.Time
	ResolvedAt  time.Time // zero while open
}

// ModView is a report as the tayfa's moderators see it. It deliberately has
// no reporter, priority or escalation field (see the package comment); a
// test keeps it that way.
type ModView struct {
	ID         id.ID
	TayfaID    id.ID
	TargetType TargetType
	TargetID   id.ID
	// SubjectID is the reported user or the content's author; zero when
	// unknown or the account was deleted.
	SubjectID   id.ID
	Reason      Reason
	Description string
	Status      Status
	Resolution  Resolution // empty while open
	HandledBy   id.ID      // zero while open or when the account was deleted
	CreatedAt   time.Time
	ResolvedAt  time.Time
}

// CreateInput is a report.
type CreateInput struct {
	TargetType  TargetType
	TargetID    string
	Reason      Reason
	Description string
}

// Create files a report by user. A second report by the same user about the
// same target returns the first one and created is false.
func (s *Service) Create(ctx context.Context, user, tayfa id.ID, in CreateInput) (v MyView, created bool, err error) {
	if !in.TargetType.valid() {
		return MyView{}, false, apperr.Validation("target_type")
	}
	target, ok := id.Parse(in.TargetID)
	if !ok || (in.TargetType == TargetTayfa && target != tayfa) || (in.TargetType == TargetUser && target == user) {
		return MyView{}, false, apperr.Validation("target_id")
	}
	if !slices.Contains(Reasons(), in.Reason) {
		return MyView{}, false, apperr.Validation("reason")
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return MyView{}, false, err
	}

	now := s.now()
	// First pass, without locks: who is reported, so identity can be asked
	// before the write transaction (no lock is held while waiting).
	prev, subject, err := s.prepare(ctx, s.q, user, tayfa, in.TargetType, target, now, false)
	if err != nil || prev != nil {
		if prev != nil {
			return *prev, false, nil
		}
		return MyView{}, false, err
	}
	minor := s.minorInvolved(ctx, user, subject)

	err = txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		prev, again, err := s.prepare(ctx, q, user, tayfa, in.TargetType, target, now, true)
		if err != nil || prev != nil {
			if prev != nil {
				v = *prev
			}
			return err
		}
		if again != subject {
			// The author's account went away in between: the answer was
			// about someone else, so take the safe side.
			subject, minor = again, true
		}

		p := store.CreateReportParams{
			ID: id.New().PG(), TayfaID: tayfa.PG(), ReporterID: user.PG(), TargetType: string(in.TargetType),
			TargetID: target.PG(), Reason: string(in.Reason), Priority: string(PriorityNormal),
			PlatformOnly: in.TargetType == TargetTayfa || in.Reason == ReasonStaffConduct, CreatedAt: ts(now),
		}
		if subject != (id.ID{}) {
			p.SubjectUserID = subject.PG()
		}
		if desc != "" {
			p.Description = pgtype.Text{String: desc, Valid: true}
		}
		if minor {
			p.Priority, p.MinorInvolvedAt = string(PriorityMinorInvolved), ts(now)
		}
		escalation := escalationAtCreate(in.TargetType, in.Reason, minor)
		if escalation != "" {
			p.EscalatedToPlatformAt = ts(now)
			p.EscalationReason = pgtype.Text{String: escalation, Valid: true}
			p.PlatformStatus = pgtype.Text{String: "open", Valid: true}
		}
		rid, err := q.CreateReport(ctx, p)
		if errors.Is(err, pgx.ErrNoRows) {
			// The same report filed concurrently: return that one.
			prev, _, err := mine(ctx, q, user, in.TargetType, target)
			v = prev
			return err
		}
		if err != nil {
			return fmt.Errorf("şikâyet: %w", err)
		}
		created = true
		if v, _, err = mine(ctx, q, user, in.TargetType, target); err != nil {
			return err
		}
		// No actor and no audit record: the tayfa's admins read the audit
		// log, and nothing they read may name the reporter.
		rep := id.ID(rid.Bytes)
		subjectRef := txn.Target{Type: subjectType, ID: rep}
		if err := txn.Event(ctx, q, EventCreated, tayfa, id.ID{}, subjectRef, map[string]any{
			"tayfa_id": tayfa.String(), "report_id": rep.String(), "target_type": p.TargetType,
			"reason": p.Reason, "priority": p.Priority,
		}); err != nil {
			return err
		}
		if escalation == "" {
			return nil
		}
		return txn.Event(ctx, q, EventEscalated, tayfa, id.ID{}, subjectRef, map[string]any{
			"tayfa_id": tayfa.String(), "report_id": rep.String(), "escalation_reason": escalation, "priority": p.Priority,
		})
	})
	if err != nil {
		return MyView{}, false, err
	}
	return v, created, nil
}

// prepare checks the caller and the target. It returns the caller's earlier
// report about the target if there is one, otherwise the reported person.
// Writes pass lock = true inside the transaction.
func (s *Service) prepare(ctx context.Context, q *store.Queries, user, tayfa id.ID, t TargetType, target id.ID, now time.Time, lock bool) (*MyView, id.ID, error) {
	// Reports work on read-only tayfas, and a muted member may report
	// (authz.allowedWhileMuted).
	_, m, err := access.Load(ctx, q, tayfa, user, lock)
	if err != nil {
		return nil, id.ID{}, err
	}
	if err := access.Authorize(m, authz.ReportCreate, now); err != nil {
		return nil, id.ID{}, err
	}
	if prev, found, err := mine(ctx, q, user, t, target); err != nil || found {
		if found {
			return &prev, id.ID{}, nil
		}
		return nil, id.ID{}, err
	}
	n, err := q.CountRecentReportsByReporter(ctx, store.CountRecentReportsByReporterParams{ReporterID: user.PG(), Since: ts(now.Add(-24 * time.Hour))})
	if err != nil {
		return nil, id.ID{}, fmt.Errorf("şikâyet sayısı: %w", err)
	}
	if n >= DailyLimit {
		return nil, id.ID{}, apperr.ErrReportLimit
	}
	subject, err := s.subjectOf(ctx, q, tayfa, user, t, target)
	return nil, subject, err
}

// escalationAtCreate returns why a new report goes to the platform at once,
// or "" (v14 "Platforma yükselen şikâyetler", "Şikâyet önceliği").
func escalationAtCreate(t TargetType, r Reason, minor bool) string {
	switch {
	case t == TargetTayfa:
		return EscalationTayfaTarget
	case r == ReasonStaffConduct:
		return EscalationStaffConduct
	case r == ReasonChildSafety:
		return EscalationChildSafety
	case minor:
		return EscalationMinorInvolved
	}
	return ""
}

// subjectOf checks that user can see the target in tayfa and returns the
// reported person: the user target itself, the content's author (zero when
// the account was deleted), or zero for the tayfa. A target the caller
// cannot see, including content of a module the tayfa turned off, is
// apperr.ErrReportTargetNotFound. Reporting oneself or one's own content is
// a validation error.
func (s *Service) subjectOf(ctx context.Context, q *store.Queries, tayfa, user id.ID, t TargetType, target id.ID) (id.ID, error) {
	if t == TargetTayfa {
		return id.ID{}, nil
	}
	if t == TargetUser {
		row, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: target.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return id.ID{}, apperr.ErrReportTargetNotFound
		case err != nil:
			return id.ID{}, fmt.Errorf("üyelik: %w", err)
		case row.Status != string(authz.StatusActive):
			return id.ID{}, apperr.ErrReportTargetNotFound
		}
		return target, nil
	}
	if err := access.RequireModule(ctx, q, tayfa, moduleOf(t)); errors.Is(err, apperr.ErrModuleDisabled) {
		return id.ID{}, apperr.ErrReportTargetNotFound
	} else if err != nil {
		return id.ID{}, err
	}
	c, err := lookup(ctx, q, tayfa, t, target)
	if err != nil {
		return id.ID{}, err
	}
	if !c.found || !c.visible || (c.ownOnly && c.author != user) {
		return id.ID{}, apperr.ErrReportTargetNotFound
	}
	if c.author == user {
		return id.ID{}, apperr.Validation("target_id")
	}
	return c.author, nil
}

func moduleOf(t TargetType) string {
	switch t {
	case TargetAnnouncement:
		return access.ModuleAnnouncements
	case TargetQuestion, TargetAnswer:
		return access.ModuleQA
	case TargetPoll:
		return access.ModulePolls
	}
	return access.ModuleEvents // events and fishing catches
}

// content is a reported piece of content as the report needs it.
type content struct {
	found   bool
	visible bool
	// removed: no longer visible to members (deleted, or a cancelled
	// event), or gone for good.
	removed bool
	// ownOnly: only the author sees it (a fishing catch kept to oneself).
	ownOnly bool
	author  id.ID // zero when the account was deleted
}

// lookup reads content of tayfa; a missing row is found = false.
func lookup(ctx context.Context, q *store.Queries, tayfa id.ID, t TargetType, target id.ID) (content, error) {
	var (
		c       content
		author  pgtype.UUID
		err     error
		visible bool
	)
	p := struct{ t, i pgtype.UUID }{tayfa.PG(), target.PG()}
	switch t {
	case TargetAnnouncement:
		var r store.GetReportTargetAnnouncementRow
		r, err = q.GetReportTargetAnnouncement(ctx, store.GetReportTargetAnnouncementParams{TayfaID: p.t, ID: p.i})
		author, visible = r.AuthorID, r.Visible
	case TargetEvent:
		var r store.GetReportTargetEventRow
		r, err = q.GetReportTargetEvent(ctx, store.GetReportTargetEventParams{TayfaID: p.t, ID: p.i})
		author, visible = r.AuthorID, r.Visible
		c.removed = r.Cancelled
	case TargetQuestion:
		var r store.GetReportTargetQuestionRow
		r, err = q.GetReportTargetQuestion(ctx, store.GetReportTargetQuestionParams{TayfaID: p.t, ID: p.i})
		author, visible = r.AuthorID, r.Visible
	case TargetAnswer:
		var r store.GetReportTargetAnswerRow
		r, err = q.GetReportTargetAnswer(ctx, store.GetReportTargetAnswerParams{TayfaID: p.t, ID: p.i})
		author, visible = r.AuthorID, r.Visible
	case TargetPoll:
		var r store.GetReportTargetPollRow
		r, err = q.GetReportTargetPoll(ctx, store.GetReportTargetPollParams{TayfaID: p.t, ID: p.i})
		author, visible = r.AuthorID, r.Visible
	case TargetFishingCatch:
		var r store.GetReportTargetCatchRow
		r, err = q.GetReportTargetCatch(ctx, store.GetReportTargetCatchParams{TayfaID: p.t, ID: p.i})
		author, visible = r.AuthorID, r.Visible
		c.ownOnly = r.Visibility != "tayfa"
	default:
		return content{}, errors.New("içerik olmayan hedef")
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Purged: removed for good.
		return content{removed: true}, nil
	case err != nil:
		return content{}, fmt.Errorf("şikâyet hedefi: %w", err)
	}
	c.found, c.visible = true, visible
	c.removed = c.removed || !visible
	if author.Valid {
		c.author = id.ID(author.Bytes)
	}
	return c, nil
}

// minorInvolved asks identity whether the reporter or the subject is under
// 18. Unknown users and any failure count as "under 18" (ADR-0012: the safe
// side). The answer is not logged or kept.
func (s *Service) minorInvolved(ctx context.Context, reporter, subject id.ID) bool {
	parties := []id.ID{reporter}
	if subject != (id.ID{}) {
		parties = append(parties, subject)
	}
	answers, err := s.cfg.Minors.IsMinor(ctx, parties)
	if err != nil {
		return true
	}
	for _, u := range parties {
		if minor, known := answers[u]; !known || minor {
			return true
		}
	}
	return false
}

// Resolve closes an open report of tayfa's queue with resolution r.
func (s *Service) Resolve(ctx context.Context, user, tayfa, report id.ID, r Resolution) (ModView, error) {
	if r != ResolutionContentRemoved && r != ResolutionMemberSanctioned && r != ResolutionDismissed {
		return ModView{}, apperr.Validation("resolution")
	}
	now := s.now()
	var v ModView
	err := txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		_, m, err := access.Load(ctx, q, tayfa, user, true)
		if err != nil {
			return err
		}
		if err := access.Authorize(m, authz.ReportResolve, now); err != nil {
			return err
		}
		row, err := q.LockTayfaReport(ctx, store.LockTayfaReportParams{TayfaID: tayfa.PG(), ID: report.PG(), ViewerID: user.PG()})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return apperr.ErrReportNotFound
		case err != nil:
			return fmt.Errorf("şikâyet: %w", err)
		case row.Status != string(StatusOpen):
			return apperr.ErrReportAlreadyResolved
		}
		subject := pgID(row.SubjectUserID)
		sub, err := memberOf(ctx, q, tayfa, subject)
		if err != nil {
			return err
		}
		// The same hierarchy as for acting on the person: a moderator
		// cannot close a report about a moderator or an admin.
		if !authz.CanActOnContent(m.Role, sub.activeRole) {
			return apperr.ErrRoleHierarchy
		}
		t := TargetType(row.TargetType)
		switch r {
		case ResolutionContentRemoved:
			if !t.content() {
				return apperr.Validation("resolution")
			}
			c, err := lookup(ctx, q, tayfa, t, id.ID(row.TargetID.Bytes))
			if err != nil {
				return err
			}
			if !c.removed {
				return apperr.ErrReportActionMissing
			}
		case ResolutionMemberSanctioned:
			if subject == (id.ID{}) || (!sub.banned && !sub.muted(now)) {
				return apperr.ErrReportActionMissing
			}
		}
		n, err := q.ResolveReport(ctx, store.ResolveReportParams{
			TayfaID: tayfa.PG(), ID: report.PG(), Resolution: pgtype.Text{String: string(r), Valid: true},
			HandledBy: user.PG(), HandledAt: ts(now),
		})
		if err != nil {
			return fmt.Errorf("şikâyet çözümü: %w", err)
		}
		if n != 1 {
			return apperr.ErrReportAlreadyResolved
		}
		v = modView(store.ListOpenTayfaReportsRow{
			ID: row.ID, TayfaID: row.TayfaID, TargetType: row.TargetType, TargetID: row.TargetID,
			SubjectUserID: row.SubjectUserID, Reason: row.Reason, Description: row.Description,
			Status: string(StatusResolved), Resolution: pgtype.Text{String: string(r), Valid: true},
			HandledBy: user.PG(), HandledAt: ts(now), CreatedAt: row.CreatedAt,
		})

		ref := txn.Target{Type: subjectType, ID: report}
		if err := txn.Audit(ctx, q, tayfa, user, "report.resolved", ref, map[string]any{
			"target_type": row.TargetType, "reason": row.Reason, "resolution": string(r),
		}); err != nil {
			return err
		}
		if err := txn.Event(ctx, q, EventResolved, tayfa, user, ref, map[string]any{
			"tayfa_id": tayfa.String(), "report_id": report.String(), "resolution": string(r),
		}); err != nil {
			return err
		}
		reporter := pgID(row.ReporterID)
		if reporter == (id.ID{}) {
			return nil
		}
		// The reporter only, while still an active member, and without an
		// actor: they are not told who handled it.
		to, err := notify.Only(ctx, q, tayfa, []id.ID{reporter}, user)
		if err != nil {
			return err
		}
		return notify.Write(ctx, q, notify.Request{
			Type: notify.TypeReportResolved, Tayfa: tayfa, TargetType: notify.TargetReport, Target: report,
		}, to)
	})
	if err != nil {
		return ModView{}, err
	}
	return v, nil
}

// subjectMembership is the reported person's standing in the tayfa.
type subjectMembership struct {
	activeRole authz.Role // 0 unless an active member
	banned     bool
	mutedUntil time.Time
}

func (m subjectMembership) muted(now time.Time) bool {
	return m.activeRole != 0 && m.mutedUntil.After(now)
}

func memberOf(ctx context.Context, q *store.Queries, tayfa, user id.ID) (subjectMembership, error) {
	if user == (id.ID{}) {
		return subjectMembership{}, nil
	}
	row, err := q.GetMembership(ctx, store.GetMembershipParams{TayfaID: tayfa.PG(), UserID: user.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return subjectMembership{}, nil
	case err != nil:
		return subjectMembership{}, fmt.Errorf("üyelik: %w", err)
	}
	m, err := access.Membership(row.Role, row.Status, row.MutedUntil)
	if err != nil {
		return subjectMembership{}, err
	}
	out := subjectMembership{banned: m.Status == authz.StatusBanned, mutedUntil: m.MutedUntil}
	if m.Status == authz.StatusActive {
		out.activeRole = m.Role
	}
	return out, nil
}

// GetMine returns one of user's own reports.
func (s *Service) GetMine(ctx context.Context, user, report id.ID) (MyView, error) {
	r, err := s.q.GetReportOfReporter(ctx, store.GetReportOfReporterParams{ReporterID: user.PG(), ID: report.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return MyView{}, apperr.ErrReportNotFound
	case err != nil:
		return MyView{}, fmt.Errorf("şikâyet: %w", err)
	}
	return myView(store.ListReportsOfReporterRow(r)), nil
}

// mine returns user's report about target, if any.
func mine(ctx context.Context, q *store.Queries, user id.ID, t TargetType, target id.ID) (MyView, bool, error) {
	r, err := q.GetReportOfReporterByTarget(ctx, store.GetReportOfReporterByTargetParams{ReporterID: user.PG(), TargetType: string(t), TargetID: target.PG()})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return MyView{}, false, nil
	case err != nil:
		return MyView{}, false, fmt.Errorf("şikâyet: %w", err)
	}
	return myView(store.ListReportsOfReporterRow(r)), true, nil
}

func myView(r store.ListReportsOfReporterRow) MyView {
	v := MyView{
		ID: id.ID(r.ID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes), TargetType: TargetType(r.TargetType),
		TargetID: id.ID(r.TargetID.Bytes), Reason: Reason(r.Reason), Description: r.Description.String,
		Status: Status(r.Status), CreatedAt: r.CreatedAt.Time.UTC(),
	}
	if r.Resolution.Valid {
		v.Outcome = OutcomeActionTaken
		if Resolution(r.Resolution.String) == ResolutionDismissed {
			v.Outcome = OutcomeDismissed
		}
	}
	if r.HandledAt.Valid {
		v.ResolvedAt = r.HandledAt.Time.UTC()
	}
	return v
}

func modView(r store.ListOpenTayfaReportsRow) ModView {
	v := ModView{
		ID: id.ID(r.ID.Bytes), TayfaID: id.ID(r.TayfaID.Bytes), TargetType: TargetType(r.TargetType),
		TargetID: id.ID(r.TargetID.Bytes), SubjectID: pgID(r.SubjectUserID), Reason: Reason(r.Reason),
		Description: r.Description.String, Status: Status(r.Status),
		Resolution: Resolution(r.Resolution.String), HandledBy: pgID(r.HandledBy),
		CreatedAt: r.CreatedAt.Time.UTC(),
	}
	if r.HandledAt.Valid {
		v.ResolvedAt = r.HandledAt.Time.UTC()
	}
	return v
}

// cleanDescription trims surrounding spaces: up to 1000 characters, may be
// empty; line breaks and tabs allowed, other control characters not.
func cleanDescription(s string) (string, error) {
	if len(s) > (DescriptionMax+64)*maxRuneBytes {
		return "", apperr.Validation("description")
	}
	d := strings.TrimSpace(s)
	if !utf8.ValidString(d) || utf8.RuneCountInString(d) > DescriptionMax {
		return "", apperr.Validation("description")
	}
	for _, r := range d {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", apperr.Validation("description")
		}
	}
	return d, nil
}

func pgID(u pgtype.UUID) id.ID {
	if !u.Valid {
		return id.ID{}
	}
	return id.ID(u.Bytes)
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }
