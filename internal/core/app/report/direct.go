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

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// EvidenceMax is the longest evidence text of a direct message report
// (issue #95; the same CHECK is in the database).
const EvidenceMax = 2000

// DirectMessages asks chat about a reported direct message with the
// reporter's own token (DirectMessageService.VerifyDirectMessageForReport;
// ADR-0009, ADR-0013): it returns the message's sender when the reporter is
// a party of its conversation, apperr.ErrReportTargetNotFound when not (or
// the message is the reporter's own, unknown, or its sender's account is
// gone), apperr.ErrUnavailable when chat cannot answer. chat never returns
// the message's content.
type DirectMessages interface {
	Sender(ctx context.Context, message id.ID) (id.ID, error)
}

// createDirect files a report about a direct message (issue #95; platform
// owner's choice (a), 2026-11-09):
//
//   - it is not about a tayfa: no membership check, tayfa_id stays empty;
//   - chat confirms, with the reporter's own token, that the reporter is a
//     party of the conversation and names the sender; the content does not
//     come from chat;
//   - the evidence is the text the reporter shares from their own device
//     (ADR-0005 "Şikâyet"), required, 1–EvidenceMax characters; it is stored
//     for the platform only and never logged, put in an event or returned;
//   - it goes to the platform only, at once (EscalationDirectMessage), and
//     never to a tayfa's queue;
//   - the duplicate, daily limit and priority rules are those of every
//     report: the priority asks IsMinor about the reporter and the sender.
func (s *Service) createDirect(ctx context.Context, user id.ID, in CreateInput) (v MyView, created bool, err error) {
	target, ok := id.Parse(in.TargetID)
	if !ok {
		return MyView{}, false, apperr.Validation("target_id")
	}
	if !slices.Contains(Reasons(), in.Reason) || in.Reason == ReasonStaffConduct {
		return MyView{}, false, apperr.Validation("reason")
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return MyView{}, false, err
	}
	evidence, err := cleanEvidence(in.Evidence)
	if err != nil {
		return MyView{}, false, err
	}
	now := s.now()
	// First pass, without locks: an earlier report, the daily limit, then
	// chat and identity before the write transaction (no lock is held while
	// waiting for them).
	if prev, found, err := s.directPrecheck(ctx, s.q, user, target, now); err != nil || found {
		return prev, false, err
	}
	if s.cfg.DirectMessages == nil {
		return MyView{}, false, apperr.ErrUnavailable
	}
	sender, err := s.cfg.DirectMessages.Sender(ctx, target)
	if err != nil {
		return MyView{}, false, err
	}
	if sender == (id.ID{}) || sender == user {
		return MyView{}, false, apperr.ErrReportTargetNotFound
	}
	minor := s.minorInvolved(ctx, user, sender)

	err = txn.WithActor(ctx, s.cfg.Pool, user, func(_ pgx.Tx, q *store.Queries) error {
		// One user's reports are counted one at a time.
		if err := q.LockUserLimit(ctx, user.PG()); err != nil {
			return fmt.Errorf("şikâyet sınırı kilidi: %w", err)
		}
		prev, found, err := s.directPrecheck(ctx, q, user, target, now)
		if err != nil || found {
			v = prev
			return err
		}
		p := store.CreateReportParams{
			ID: id.New().PG(), ReporterID: user.PG(), TargetType: string(TargetDMMessage), TargetID: target.PG(),
			SubjectUserID: sender.PG(), Reason: string(in.Reason), Priority: string(PriorityNormal),
			EvidenceText: pgtype.Text{String: evidence, Valid: true},
			PlatformOnly: true, CreatedAt: ts(now),
			EscalatedToPlatformAt: ts(now), EscalationReason: pgtype.Text{String: EscalationDirectMessage, Valid: true},
			PlatformStatus: pgtype.Text{String: "open", Valid: true},
		}
		if desc != "" {
			p.Description = pgtype.Text{String: desc, Valid: true}
		}
		if minor {
			p.Priority, p.MinorInvolvedAt = string(PriorityMinorInvolved), ts(now)
		}
		rid, err := q.CreateReport(ctx, p)
		if errors.Is(err, pgx.ErrNoRows) {
			v, _, err = mine(ctx, q, user, TargetDMMessage, target)
			return err
		}
		if err != nil {
			return fmt.Errorf("şikâyet: %w", err)
		}
		created = true
		if v, _, err = mine(ctx, q, user, TargetDMMessage, target); err != nil {
			return err
		}
		// IDs and classification only: never the evidence, the description
		// or the reporter (no actor).
		rep := id.ID(rid.Bytes)
		ref := txn.Target{Type: subjectType, ID: rep}
		if err := txn.Event(ctx, q, EventCreated, id.ID{}, id.ID{}, ref, map[string]any{
			"report_id": rep.String(), "target_type": p.TargetType, "reason": p.Reason, "priority": p.Priority,
		}); err != nil {
			return err
		}
		return txn.Event(ctx, q, EventEscalated, id.ID{}, id.ID{}, ref, map[string]any{
			"report_id": rep.String(), "escalation_reason": EscalationDirectMessage, "priority": p.Priority,
		})
	})
	if err != nil {
		return MyView{}, false, err
	}
	return v, created, nil
}

// directPrecheck returns the caller's earlier report about the message, or
// apperr.ErrReportLimit when the daily limit is used up.
func (s *Service) directPrecheck(ctx context.Context, q *store.Queries, user, target id.ID, now time.Time) (MyView, bool, error) {
	if prev, found, err := mine(ctx, q, user, TargetDMMessage, target); err != nil || found {
		return prev, found, err
	}
	n, err := q.CountRecentReportsByReporter(ctx, store.CountRecentReportsByReporterParams{ReporterID: user.PG(), Since: ts(now.Add(-24 * time.Hour))})
	if err != nil {
		return MyView{}, false, fmt.Errorf("şikâyet sayısı: %w", err)
	}
	if n >= DailyLimit {
		return MyView{}, false, apperr.ErrReportLimit
	}
	return MyView{}, false, nil
}

// cleanEvidence checks the shared message text: required, 1–EvidenceMax
// characters after trimming, line breaks allowed, other control characters
// not. Errors never echo it.
func cleanEvidence(s string) (string, error) {
	if len(s) > (EvidenceMax+64)*maxRuneBytes {
		return "", apperr.Validation("evidence_text")
	}
	e := strings.TrimSpace(s)
	if e == "" || !utf8.ValidString(e) || utf8.RuneCountInString(e) > EvidenceMax {
		return "", apperr.Validation("evidence_text")
	}
	for _, r := range e {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return "", apperr.Validation("evidence_text")
		}
	}
	return e, nil
}
