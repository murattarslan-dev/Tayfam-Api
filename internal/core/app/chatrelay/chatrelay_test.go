package chatrelay

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/outbox"
)

func ev(t *testing.T, typ string, actor id.ID, payload map[string]any) outbox.Event {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return outbox.Event{ID: id.New(), Type: typ, Actor: actor, Payload: b, OccurredAt: time.Now().UTC()}
}

// The list is the contract with chat (ADR-0009): notification requests,
// the purge, and the membership and tayfa state changes chat uses to drop
// cached channel permissions (issue #81).
func TestTypesAreExplicit(t *testing.T) {
	want := []string{
		"notification.requested", "tayfa.purged", "membership.changed",
		"tayfa.warned", "tayfa.archived", "tayfa.reactivated", "tayfa.deleted",
		"tayfa.deletion_requested", "tayfa.deletion_cancelled",
		// Platform suspension (issue #104).
		"tayfa.suspended", "tayfa.unsuspended",
		// Event channels (issue #183).
		"rsvp.changed", "rsvp.promoted", "event.updated", "event.cancelled",
	}
	if got := Types(); !slices.Equal(got, want) {
		t.Fatalf("Types = %v", got)
	}
	if c := Consumer(nil); c.Name != "chat" || !slices.Equal(c.Types, want) {
		t.Fatalf("tüketici = %s %v", c.Name, c.Types)
	}
}

func TestRequest(t *testing.T) {
	tf, actor, target, u1, u2, q := id.New(), id.New(), id.New(), id.New(), id.New(), id.New()
	r, err := Request(ev(t, "notification.requested", actor, map[string]any{
		"type": "answer_received", "tayfa_id": tf.String(), "target_type": "answer", "target_id": target.String(),
		"recipient_ids": []string{u1.String(), u2.String()}, "question_id": q.String(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	n := r.GetNotificationRequested()
	if r.GetTayfaId() != tf.String() || r.GetActorId() != actor.String() || n.GetType() != chatv1.NotificationType_NOTIFICATION_TYPE_ANSWER_RECEIVED ||
		n.GetTargetType() != chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_ANSWER || n.GetTargetId() != target.String() ||
		!slices.Equal(n.GetRecipientIds(), []string{u1.String(), u2.String()}) || n.GetQuestionId() != q.String() {
		t.Fatalf("istek = %v", r)
	}

	r, err = Request(ev(t, "notification.requested", id.ID{}, map[string]any{
		"type": "tayfa_warned", "tayfa_id": tf.String(), "target_type": "tayfa", "target_id": tf.String(),
		"recipient_ids": []string{u1.String()}, "reason": "few_members",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if n := r.GetNotificationRequested(); n.GetWarningReason() != chatv1.TayfaWarningReason_TAYFA_WARNING_REASON_FEW_MEMBERS || r.GetActorId() != "" {
		t.Fatalf("uyarı = %v", r)
	}

	r, err = Request(ev(t, "notification.requested", id.ID{}, map[string]any{
		"type": "hike_overdue", "tayfa_id": tf.String(), "target_type": "event", "target_id": target.String(),
		"recipient_ids": []string{u1.String()}, "user_id": u2.String(),
	}))
	if err != nil || r.GetNotificationRequested().GetSubjectUserId() != u2.String() {
		t.Fatalf("alarm = %v %v", r, err)
	}

	// Friend notifications carry no tayfa (issue #71).
	r, err = Request(ev(t, "notification.requested", actor, map[string]any{
		"type": "friend_request", "target_type": "user", "target_id": actor.String(), "recipient_ids": []string{u1.String()},
	}))
	if err != nil || r.GetTayfaId() != "" || r.GetNotificationRequested().GetType() != chatv1.NotificationType_NOTIFICATION_TYPE_FRIEND_REQUEST ||
		r.GetNotificationRequested().GetTargetType() != chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_USER {
		t.Fatalf("arkadaşlık = %v %v", r, err)
	}
	for typ, want := range map[string]chatv1.NotificationType{
		"friend_accepted":  chatv1.NotificationType_NOTIFICATION_TYPE_FRIEND_ACCEPTED,
		"tayfa_invitation": chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_INVITATION,
	} {
		target := "user"
		if typ == "tayfa_invitation" {
			target = "tayfa"
		}
		r, err := Request(ev(t, "notification.requested", actor, map[string]any{
			"type": typ, "tayfa_id": tf.String(), "target_type": target, "target_id": tf.String(), "recipient_ids": []string{u1.String()},
		}))
		if err != nil || r.GetNotificationRequested().GetType() != want {
			t.Fatalf("%s = %v %v", typ, r, err)
		}
	}

	rep := id.New()
	r, err = Request(ev(t, "notification.requested", id.ID{}, map[string]any{
		"type": "report_resolved", "tayfa_id": tf.String(), "target_type": "report", "target_id": rep.String(),
		"recipient_ids": []string{u1.String()},
	}))
	if n := r.GetNotificationRequested(); err != nil || n.GetType() != chatv1.NotificationType_NOTIFICATION_TYPE_REPORT_RESOLVED ||
		n.GetTargetType() != chatv1.NotificationTargetType_NOTIFICATION_TARGET_TYPE_REPORT || n.GetTargetId() != rep.String() || r.GetActorId() != "" {
		t.Fatalf("şikâyet sonucu = %v %v", r, err)
	}

	r, err = Request(ev(t, "tayfa.purged", id.ID{}, map[string]any{"tayfa_id": tf.String()}))
	if err != nil || r.GetTayfaPurged() == nil || r.GetTayfaId() != tf.String() {
		t.Fatalf("kalıcı silme = %v %v", r, err)
	}
}

// An event chat could misread stops the relay instead of being skipped.
func TestRequestRefusesUnknown(t *testing.T) {
	tf := id.New().String()
	for name, e := range map[string]outbox.Event{
		"bilinmeyen tür":   ev(t, "notification.requested", id.ID{}, map[string]any{"type": "poke", "tayfa_id": tf, "target_type": "tayfa"}),
		"bilinmeyen hedef": ev(t, "notification.requested", id.ID{}, map[string]any{"type": "announcement", "tayfa_id": tf, "target_type": "poll"}),
		"bilinmeyen neden": ev(t, "notification.requested", id.ID{}, map[string]any{"type": "tayfa_warned", "tayfa_id": tf, "target_type": "tayfa", "reason": "boring"}),
		"başka olay":       ev(t, "announcement.created", id.ID{}, map[string]any{"tayfa_id": tf}),
	} {
		if _, err := Request(e); err == nil {
			t.Errorf("%s: hata yok", name)
		}
	}
}

// membership.changed carries only the member and whether the membership
// ended (issue #183); the kind of change and the actor's role do not
// reach chat.
func TestMembershipAndStatusEvents(t *testing.T) {
	tf, u, actor := id.New(), id.New(), id.New()
	r, err := Request(ev(t, "membership.changed", actor, map[string]any{"tayfa_id": tf.String(), "user_id": u.String(), "change": "muted"}))
	if err != nil {
		t.Fatal(err)
	}
	if r.GetTayfaId() != tf.String() || r.GetMembershipChanged().GetUserId() != u.String() || r.GetMembershipChanged().GetEnded() {
		t.Fatalf("üyelik = %v", r)
	}
	for change, ended := range map[string]bool{"left": true, "removed": true, "banned": true, "joined": false, "role_changed": false, "withdrawn": false} {
		r, err := Request(ev(t, "membership.changed", actor, map[string]any{"tayfa_id": tf.String(), "user_id": u.String(), "change": change}))
		if err != nil || r.GetMembershipChanged().GetEnded() != ended {
			t.Errorf("%s: %v %v", change, r, err)
		}
		if strings.Contains(r.String(), change) {
			t.Errorf("%s: değişiklik türü chat'e gitti", change)
		}
	}
	if _, err := Request(ev(t, "membership.changed", actor, map[string]any{"tayfa_id": tf.String()})); err == nil {
		t.Fatal("kullanıcısız üyelik olayı kabul edildi")
	}
	for _, typ := range []string{"tayfa.warned", "tayfa.archived", "tayfa.reactivated", "tayfa.deleted"} {
		r, err := Request(ev(t, typ, id.ID{}, map[string]any{"tayfa_id": tf.String(), "reason": "inactive"}))
		if err != nil || r.GetTayfaId() != tf.String() || r.GetTayfaStatusChanged() == nil {
			t.Fatalf("%s = %v, %v", typ, r, err)
		}
	}
	// The tayfa package names the tayfa as the subject only.
	e := ev(t, "tayfa.deletion_requested", actor, map[string]any{"deletion_scheduled_for": "2026-11-12T00:00:00Z"})
	e.SubjectType, e.SubjectID = "tayfa", tf
	r, err = Request(e)
	if err != nil || r.GetTayfaId() != tf.String() || r.GetTayfaStatusChanged() == nil {
		t.Fatalf("silme isteği = %v, %v", r, err)
	}
	// Without a tayfa the event cannot be applied.
	if _, err := Request(ev(t, "tayfa.deletion_cancelled", actor, nil)); err == nil {
		t.Fatal("tayfasız durum olayı kabul edildi")
	}
}

// TestSuspensionReachesChat: the platform's suspension events reach chat as
// tayfa_status_changed, and their notifications as the new types.
func TestSuspensionReachesChat(t *testing.T) {
	tf := id.New()
	for _, typ := range []string{TypeTayfaSuspended, TypeTayfaUnsuspended} {
		req, err := Request(ev(t, typ, id.ID{}, map[string]any{"tayfa_id": tf.String()}))
		if err != nil || req.GetTayfaStatusChanged() == nil || req.GetTayfaId() != tf.String() {
			t.Fatalf("%s: %v %v", typ, req, err)
		}
	}
	for typ, want := range map[string]chatv1.NotificationType{
		notify.TypeTayfaSuspended:   chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_SUSPENDED,
		notify.TypeTayfaUnsuspended: chatv1.NotificationType_NOTIFICATION_TYPE_TAYFA_UNSUSPENDED,
	} {
		req, err := Request(ev(t, notify.EventType, id.ID{}, map[string]any{
			"tayfa_id": tf.String(), "type": typ, "target_type": notify.TargetTayfa, "target_id": tf.String(),
			"recipient_ids": []string{id.New().String()},
		}))
		if err != nil || req.GetNotificationRequested().GetType() != want {
			t.Fatalf("%s: %v %v", typ, req, err)
		}
	}
}

// TestEventChannelEvents (issue #183): an answer change reaches chat
// without the answer; the event's end, when it moved, and its
// cancellation reach chat with the time.
func TestEventChannelEvents(t *testing.T) {
	tf, event, user := id.New(), id.New(), id.New()
	for _, typ := range []string{"rsvp.changed", "rsvp.promoted"} {
		r, err := Request(ev(t, typ, user, map[string]any{"tayfa_id": tf.String(), "event_id": event.String(), "user_id": user.String(), "status": "going"}))
		a := r.GetEventAttendanceChanged()
		if err != nil || r.GetTayfaId() != tf.String() || a.GetEventId() != event.String() || a.GetUserId() != user.String() {
			t.Fatalf("%s: %v %v", typ, r, err)
		}
	}
	end := time.Date(2026, 12, 1, 18, 0, 0, 0, time.UTC)
	r, err := Request(ev(t, "event.updated", user, map[string]any{"tayfa_id": tf.String(), "event_id": event.String(), "fields": []string{"time"}, "ends_at": end}))
	if s := r.GetEventScheduleChanged(); err != nil || !s.GetEndTime().AsTime().Equal(end) || s.GetCancelled() || s.GetEventId() != event.String() {
		t.Fatalf("güncelleme: %v %v", r, err)
	}
	// An update that did not move the end: nothing for chat.
	r, err = Request(ev(t, "event.updated", user, map[string]any{"tayfa_id": tf.String(), "event_id": event.String(), "fields": []string{"title"}}))
	if err != nil || r != nil {
		t.Fatalf("başlık değişikliği: %v %v", r, err)
	}
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	r, err = Request(ev(t, "event.cancelled", user, map[string]any{"tayfa_id": tf.String(), "event_id": event.String(), "cancelled_at": at}))
	if s := r.GetEventScheduleChanged(); err != nil || !s.GetEndTime().AsTime().Equal(at) || !s.GetCancelled() {
		t.Fatalf("iptal: %v %v", r, err)
	}
	// An older cancellation without the time uses the event's own time.
	old := ev(t, "event.cancelled", user, map[string]any{"tayfa_id": tf.String(), "event_id": event.String()})
	r, err = Request(old)
	if s := r.GetEventScheduleChanged(); err != nil || !s.GetEndTime().AsTime().Equal(old.OccurredAt) {
		t.Fatalf("eski iptal: %v %v", r, err)
	}
	for name, p := range map[string]map[string]any{
		"etkinliksiz katılım":  {"tayfa_id": tf.String(), "user_id": user.String()},
		"kullanıcısız katılım": {"tayfa_id": tf.String(), "event_id": event.String()},
	} {
		if _, err := Request(ev(t, "rsvp.changed", user, p)); err == nil {
			t.Errorf("%s kabul edildi", name)
		}
	}
	// The status never reaches chat.
	r, _ = Request(ev(t, "rsvp.changed", user, map[string]any{"tayfa_id": tf.String(), "event_id": event.String(), "user_id": user.String(), "status": "not_going"}))
	if strings.Contains(r.String(), "not_going") {
		t.Fatal("katılım cevabı chat'e gitti")
	}
}
