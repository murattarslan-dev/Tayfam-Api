package chatrelay

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	chatv1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/chat/v1"
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

// The list is the contract with chat (ADR-0009): only notification
// requests and the purge.
func TestTypesAreExplicit(t *testing.T) {
	want := []string{"notification.requested", "tayfa.purged"}
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
