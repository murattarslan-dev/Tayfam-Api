package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/auth"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Platform decisions (reports.platform_decision).
const (
	DecisionContentRemoved = "content_removed"
	DecisionWarned         = "warned"
	DecisionTayfaSuspended = "tayfa_suspended"
	DecisionUserBanned     = "user_banned"
	DecisionDismissed      = "dismissed"
)

// resolutionOf is the report's resolution when the platform decides a
// report the tayfa had not resolved.
var resolutionOf = map[string]string{
	DecisionContentRemoved: "content_removed",
	DecisionWarned:         "member_sanctioned",
	DecisionTayfaSuspended: "member_sanctioned",
	DecisionUserBanned:     "member_sanctioned",
	DecisionDismissed:      "dismissed",
}

// Report is a report as the platform sees it.
type Report struct {
	ID            id.ID
	Tayfa         id.ID // zero for a direct message report
	TargetType    string
	Target        id.ID
	Subject       id.ID // reported person; zero if deleted
	Reporter      id.ID // zero if deleted
	Reason        string
	Description   string
	Priority      string
	CreatedAt     time.Time
	EscalatedAt   time.Time
	Resolved      bool // on the platform
	Decision      string
	DecidedAt     time.Time
	evidenceText  string
	tayfaResolved bool
}

// Item is the reported item alone.
type Item struct {
	TargetType string
	Target     id.ID
	Author     id.ID
	Title      string
	Body       string
	Options    []string
	CreatedAt  time.Time
	DeletedAt  time.Time
}

func reportOf(r store.AdminListPlatformReportsRow) Report {
	return Report{
		ID: idOf(r.ID), Tayfa: idOf(r.TayfaID), TargetType: r.TargetType, Target: idOf(r.TargetID),
		Subject: idOf(r.SubjectUserID), Reporter: idOf(r.ReporterID), Reason: r.Reason, Description: r.Description.String,
		Priority: r.Priority, CreatedAt: timeOf(r.CreatedAt), EscalatedAt: timeOf(r.EscalatedToPlatformAt),
		Resolved: r.PlatformStatus.String == "resolved", Decision: r.PlatformDecision.String, DecidedAt: timeOf(r.PlatformHandledAt),
	}
}

// ListPlatformReports is the platform queue: open first, minor_involved and
// child_safety first among them, then in escalation order.
func (s *Service) ListPlatformReports(ctx context.Context, c auth.AdminClaims, includeResolved bool) ([]Report, error) {
	if _, err := s.caller(ctx, c); err != nil {
		return nil, err
	}
	rows, err := s.q.AdminListPlatformReports(ctx, includeResolved)
	if err != nil {
		return nil, fmt.Errorf("platform şikâyetleri: %w", err)
	}
	out := make([]Report, len(rows))
	for i, r := range rows {
		out[i] = reportOf(r)
	}
	return out, nil
}

func (s *Service) report(ctx context.Context, q *store.Queries, report id.ID) (Report, error) {
	r, err := q.AdminGetPlatformReport(ctx, report.PG())
	if err != nil {
		return Report{}, notFound(err, apperr.ErrReportNotFound)
	}
	out := reportOf(store.AdminListPlatformReportsRow{
		ID: r.ID, TayfaID: r.TayfaID, TargetType: r.TargetType, TargetID: r.TargetID, SubjectUserID: r.SubjectUserID,
		ReporterID: r.ReporterID, Reason: r.Reason, Description: r.Description, Priority: r.Priority, CreatedAt: r.CreatedAt,
		EscalatedToPlatformAt: r.EscalatedToPlatformAt, PlatformStatus: r.PlatformStatus, PlatformDecision: r.PlatformDecision,
		PlatformHandledAt: r.PlatformHandledAt,
	})
	out.evidenceText = r.EvidenceText.String
	out.tayfaResolved = r.Status == "resolved"
	return out, nil
}

// viewReason is the audit reason of viewing a reported item; the viewing is
// not a change, so the administrator writes none.
const viewReason = "Şikâyet edilen öğenin incelenmesi"

// GetReportedItem returns the report and the reported item only, and
// records the viewing in the platform audit log. A missing item (purged)
// returns the report with a nil item.
func (s *Service) GetReportedItem(ctx context.Context, c auth.AdminClaims, report id.ID) (Report, *Item, error) {
	cl, err := s.caller(ctx, c)
	if err != nil {
		return Report{}, nil, err
	}
	var (
		rep  Report
		item *Item
	)
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		var err error
		if rep, err = s.report(ctx, q, report); err != nil {
			return err
		}
		if item, err = reportedItem(ctx, q, rep); err != nil {
			return err
		}
		return audit(ctx, q, cl, rep.Tayfa, "report.view_item", "report", rep.ID, viewReason, nil)
	})
	if err != nil {
		return Report{}, nil, err
	}
	return rep, item, nil
}

// reportedItem reads the one reported thing. No other content of the
// tayfa, no poll votes and no coordinates are read.
func reportedItem(ctx context.Context, q *store.Queries, r Report) (*Item, error) {
	it := &Item{TargetType: r.TargetType, Target: r.Target}
	tf := r.Tayfa.PG()
	var err error
	switch r.TargetType {
	case "dm_message":
		// The server cannot read direct messages (end-to-end encrypted);
		// the evidence is the reporter's own copy (ADR-0005).
		it.Author, it.Body, it.CreatedAt = r.Subject, r.evidenceText, r.CreatedAt
		return it, nil
	case "user":
		var name pgtype.Text
		name, err = q.AdminGetProfileName(ctx, r.Target.PG())
		it.Author, it.Title = r.Target, name.String
	case "tayfa":
		var t store.AdminGetTayfaRow
		t, err = q.AdminGetTayfa(ctx, r.Target.PG())
		it.Title, it.Body, it.CreatedAt, it.DeletedAt = t.Name, t.Description.String, timeOf(t.CreatedAt), timeOf(t.DeletedAt)
	case "announcement":
		var a store.AdminGetAnnouncementRow
		a, err = q.AdminGetAnnouncement(ctx, store.AdminGetAnnouncementParams{ID: r.Target.PG(), TayfaID: tf})
		it.Author, it.Title, it.Body, it.CreatedAt, it.DeletedAt = idOf(a.AuthorID), a.Title, a.Body, timeOf(a.CreatedAt), timeOf(a.DeletedAt)
	case "event":
		var e store.AdminGetEventRow
		e, err = q.AdminGetEvent(ctx, store.AdminGetEventParams{ID: r.Target.PG(), TayfaID: tf})
		it.Author, it.Title, it.Body, it.CreatedAt, it.DeletedAt = idOf(e.CreatedBy), e.Title, e.Description.String, timeOf(e.CreatedAt), timeOf(e.DeletedAt)
	case "question":
		var x store.AdminGetQuestionRow
		x, err = q.AdminGetQuestion(ctx, store.AdminGetQuestionParams{ID: r.Target.PG(), TayfaID: tf})
		it.Author, it.Title, it.Body, it.CreatedAt, it.DeletedAt = idOf(x.AuthorID), x.Title, x.Body, timeOf(x.CreatedAt), timeOf(x.DeletedAt)
	case "answer":
		var x store.AdminGetAnswerRow
		x, err = q.AdminGetAnswer(ctx, store.AdminGetAnswerParams{ID: r.Target.PG(), TayfaID: tf})
		it.Author, it.Body, it.CreatedAt, it.DeletedAt = idOf(x.AuthorID), x.Body, timeOf(x.CreatedAt), timeOf(x.DeletedAt)
	case "poll":
		var p store.AdminGetPollRow
		p, err = q.AdminGetPoll(ctx, store.AdminGetPollParams{ID: r.Target.PG(), TayfaID: tf})
		if err == nil {
			it.Author, it.Title, it.CreatedAt, it.DeletedAt = idOf(p.CreatedBy), p.Question, timeOf(p.CreatedAt), timeOf(p.DeletedAt)
			it.Options, err = q.AdminGetPollOptions(ctx, r.Target.PG())
		}
	case "fishing_catch":
		var x store.AdminGetCatchRow
		x, err = q.AdminGetCatch(ctx, store.AdminGetCatchParams{ID: r.Target.PG(), TayfaID: tf})
		it.Author, it.Title, it.CreatedAt, it.DeletedAt = idOf(x.UserID), x.Species, timeOf(x.CreatedAt), timeOf(x.DeletedAt)
	default:
		return nil, fmt.Errorf("bilinmeyen şikâyet hedefi %q", r.TargetType)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("şikâyet edilen öğe: %w", err)
	}
	return it, nil
}

// removeContent soft deletes the reported content. Already deleted content
// is not an error.
func removeContent(ctx context.Context, q *store.Queries, r Report, by id.ID, now time.Time) error {
	at, tf, tid, b := ts(now), r.Tayfa.PG(), r.Target.PG(), by.PG()
	var err error
	switch r.TargetType {
	case "announcement":
		_, err = q.AdminDeleteAnnouncement(ctx, store.AdminDeleteAnnouncementParams{ID: tid, TayfaID: tf, At: at, By: b})
	case "event":
		_, err = q.AdminDeleteEvent(ctx, store.AdminDeleteEventParams{ID: tid, TayfaID: tf, At: at, By: b})
	case "question":
		_, err = q.AdminDeleteQuestion(ctx, store.AdminDeleteQuestionParams{ID: tid, TayfaID: tf, At: at, By: b})
	case "answer":
		_, err = q.AdminDeleteAnswer(ctx, store.AdminDeleteAnswerParams{ID: tid, TayfaID: tf, At: at, By: b})
	case "poll":
		_, err = q.AdminDeletePoll(ctx, store.AdminDeletePollParams{ID: tid, TayfaID: tf, At: at, By: b})
	case "fishing_catch":
		_, err = q.AdminDeleteCatch(ctx, store.AdminDeleteCatchParams{ID: tid, TayfaID: tf, At: at, By: b})
	default:
		// Users, tayfas and direct messages are not content to remove.
		return apperr.ErrDecisionNotApplicable
	}
	if err != nil {
		return fmt.Errorf("içerik kaldırma: %w", err)
	}
	return nil
}

// DecideReport records the platform's decision on an escalated report and
// carries it out where core can: content removal and tayfa suspension.
// A ban is identity's (AdminUserService.SanctionUser, called first by the
// panel); here it is recorded on the report. A warning is recorded only.
// The reporter is notified when the report is about a tayfa.
func (s *Service) DecideReport(ctx context.Context, c auth.AdminClaims, report id.ID, decision, reason string) error {
	resolution, ok := resolutionOf[decision]
	if !ok {
		return apperr.Validation("decision")
	}
	cl, err := s.destructive(ctx, c, reason)
	if err != nil {
		return err
	}
	now := s.cfg.Now().UTC()
	err = pgx.BeginFunc(ctx, s.cfg.Pool, func(tx pgx.Tx) error {
		q := s.q.WithTx(tx)
		if _, err := q.AdminLockPlatformReport(ctx, report.PG()); err != nil {
			return notFound(err, apperr.ErrReportNotFound)
		}
		r, err := s.report(ctx, q, report)
		if err != nil {
			return err
		}
		if r.Resolved {
			return apperr.ErrReportAlreadyResolved
		}
		extra := map[string]string{"report_id": r.ID.String(), "decision": decision}
		if r.Subject != (id.ID{}) {
			extra["user_id"] = r.Subject.String()
		}
		switch decision {
		case DecisionContentRemoved:
			if err := removeContent(ctx, q, r, cl.User, now); err != nil {
				return err
			}
		case DecisionTayfaSuspended:
			if r.Tayfa == (id.ID{}) {
				return apperr.ErrDecisionNotApplicable
			}
			t, err := lockTayfa(ctx, q, r.Tayfa)
			if err != nil {
				return err
			}
			if !t.SuspendedAt.Valid {
				// The suspension is part of the decision; its own audit
				// entry is the decision's (below).
				if err := suspendForReport(ctx, q, cl, r.Tayfa, reason, now); err != nil {
					return err
				}
			}
		case DecisionWarned, DecisionUserBanned:
			if r.Subject == (id.ID{}) {
				return apperr.ErrDecisionNotApplicable
			}
		}
		if n, err := q.AdminDecideReport(ctx, store.AdminDecideReportParams{
			ID: r.ID.PG(), Decision: pgtext(decision), By: cl.User.PG(), At: ts(now), Resolution: resolution,
		}); err != nil {
			return fmt.Errorf("karar: %w", err)
		} else if n != 1 {
			return apperr.ErrReportAlreadyResolved
		}
		if r.Tayfa != (id.ID{}) && r.Reporter != (id.ID{}) && !r.tayfaResolved {
			if err := notify.Write(ctx, q, notify.Request{Type: notify.TypeReportResolved, Tayfa: r.Tayfa,
				TargetType: notify.TargetReport, Target: r.ID}, []id.ID{r.Reporter}); err != nil {
				return err
			}
		}
		return audit(ctx, q, cl, r.Tayfa, "report.decide", "report", r.ID, reason, extra)
	})
	if err != nil {
		return err
	}
	s.cfg.Log.WarnContext(ctx, "platform şikâyet kararı", slog.String("report_id", report.String()), slog.String("decision", decision))
	return nil
}

// suspendForReport suspends the report's tayfa without a separate audit
// entry: the record of the report decision covers it.
func suspendForReport(ctx context.Context, q *store.Queries, c Caller, tayfa id.ID, reason string, now time.Time) error {
	r := []rune(reason)
	if len(r) > maxSuspendReason {
		reason = string(r[:maxSuspendReason])
	}
	n, err := q.AdminSuspendTayfa(ctx, store.AdminSuspendTayfaParams{ID: tayfa.PG(), At: ts(now), Reason: pgtext(reason), By: c.User.PG()})
	if err != nil {
		return fmt.Errorf("askı: %w", err)
	}
	if n != 1 {
		return apperr.ErrTayfaStateConflict
	}
	return statusChange(ctx, q, c, tayfa, EventTayfaSuspended, notify.TypeTayfaSuspended, "platform.suspended", "", "", "", id.ID{}, nil)
}
