// Package notify writes core's notification.requested outbox events
// (ADR-0009 "Bildirim oluşturma", issue #64): core owns memberships, so core
// decides who hears about a change; chat only stores the in-app
// notifications and (later) sends push.
//
// A request names the notification type, the tayfa, the actor, what it is
// about (target type and ID, plus a few extra IDs or a classification) and
// the recipients. It never carries content text: no titles, bodies, places
// or names. Recipients are active members only, never the actor, computed
// in the same transaction as the change (Members, Staff, Participants,
// Only). Large audiences are split over several events of at most
// MaxRecipients IDs each (the outbox payload is limited to 8 KiB), all in
// the same transaction.
//
// Write must come after the change's other outbox events: outbox.Write
// takes the ordering lock, and events are the last writes of a transaction.
package notify

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/txn"
	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// EventType is the outbox event chat turns into notifications.
const EventType = "notification.requested"

// MaxRecipients bounds the recipient IDs of one event: 150 IDs with JSON
// quoting and separators stay well under the 8 KiB payload limit.
const MaxRecipients = 150

// Notification types (data model v14 "Senaryo Kuralları → Bildirimler",
// plus the background job notices of issue #66).
const (
	TypeEventCreated           = "event_created"
	TypeEventUpdated           = "event_updated"
	TypeEventCancelled         = "event_cancelled"
	TypeRSVPPromoted           = "rsvp_promoted"
	TypeAnnouncement           = "announcement"
	TypeAnswerReceived         = "answer_received"
	TypeJoinRequest            = "join_request"
	TypeJoinApproved           = "join_approved"
	TypeTayfaDeletionRequested = "tayfa_deletion_requested"
	TypeTayfaWarned            = "tayfa_warned"
	TypeTayfaArchived          = "tayfa_archived"
	TypeHikeOverdue            = "hike_overdue"
)

// Target types: what the client opens (and reads from core).
const (
	TargetTayfa        = "tayfa"
	TargetMembership   = "membership" // target ID: the member's user ID
	TargetAnnouncement = "announcement"
	TargetEvent        = "event"
	TargetAnswer       = "answer"
)

// Extra keys: further IDs or a classification, never text.
const (
	ExtraQuestionID = "question_id" // answer_received
	ExtraUserID     = "user_id"     // hike_overdue: the hiker
	ExtraReason     = "reason"      // tayfa_warned: inactive|few_members
)

// Request is one notification for many recipients.
type Request struct {
	Type       string
	Tayfa      id.ID
	Actor      id.ID // zero: the system
	TargetType string
	Target     id.ID
	Extra      map[string]string // optional; keys Extra*
}

// Write writes r for recipients as notification.requested events of at
// most MaxRecipients each. No recipients, no event. q must run in the
// change's transaction.
func Write(ctx context.Context, q *store.Queries, r Request, recipients []id.ID) error {
	if r.Type == "" || r.TargetType == "" || r.Target == (id.ID{}) || r.Tayfa == (id.ID{}) {
		return errors.New("bildirim isteği eksik")
	}
	for k := range r.Extra {
		if k != ExtraQuestionID && k != ExtraUserID && k != ExtraReason {
			return fmt.Errorf("bildirim isteğinde izin verilmeyen alan %q", k)
		}
	}
	for start := 0; start < len(recipients); start += MaxRecipients {
		part := recipients[start:min(start+MaxRecipients, len(recipients))]
		ids := make([]string, len(part))
		for i, u := range part {
			ids[i] = u.String()
		}
		p := map[string]any{
			"type": r.Type, "tayfa_id": r.Tayfa.String(),
			"target_type": r.TargetType, "target_id": r.Target.String(),
			"recipient_ids": ids,
		}
		for k, v := range r.Extra {
			p[k] = v
		}
		if err := txn.Event(ctx, q, EventType, r.Tayfa, r.Actor, txn.Target{Type: r.TargetType, ID: r.Target}, p); err != nil {
			return fmt.Errorf("bildirim isteği: %w", err)
		}
	}
	return nil
}

// Members returns the tayfa's active members except exclude.
func Members(ctx context.Context, q *store.Queries, tayfa, exclude id.ID) ([]id.ID, error) {
	return ids(q.ListNotifyMembers(ctx, store.ListNotifyMembersParams{TayfaID: tayfa.PG(), ExcludeUserID: exclude.PG()}))
}

// Staff returns the tayfa's active admins and moderators except exclude.
func Staff(ctx context.Context, q *store.Queries, tayfa, exclude id.ID) ([]id.ID, error) {
	return ids(q.ListNotifyStaff(ctx, store.ListNotifyStaffParams{TayfaID: tayfa.PG(), ExcludeUserID: exclude.PG()}))
}

// Participants returns the active members who answered going, maybe or
// waitlist to the event, except exclude.
func Participants(ctx context.Context, q *store.Queries, tayfa, event, exclude id.ID) ([]id.ID, error) {
	return ids(q.ListNotifyParticipants(ctx, store.ListNotifyParticipantsParams{TayfaID: tayfa.PG(), EventID: event.PG(), ExcludeUserID: exclude.PG()}))
}

// Only returns those of users who are active members, except exclude.
func Only(ctx context.Context, q *store.Queries, tayfa id.ID, users []id.ID, exclude id.ID) ([]id.ID, error) {
	if len(users) == 0 {
		return nil, nil
	}
	in := make([]pgtype.UUID, 0, len(users))
	for _, u := range users {
		if u != (id.ID{}) {
			in = append(in, u.PG())
		}
	}
	return ids(q.FilterNotifyActive(ctx, store.FilterNotifyActiveParams{TayfaID: tayfa.PG(), UserIds: in, ExcludeUserID: exclude.PG()}))
}

func ids(rows []pgtype.UUID, err error) ([]id.ID, error) {
	if err != nil {
		return nil, fmt.Errorf("bildirim alıcıları: %w", err)
	}
	out := make([]id.ID, len(rows))
	for i, r := range rows {
		out[i] = id.ID(r.Bytes)
	}
	return out, nil
}
