package membership

import (
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/authz"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/notify"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// A join request notifies the admins and moderators; its approval the user
// (issue #64). Joining an open tayfa notifies nobody.
func TestJoinRequestAndApprovalNotifications(t *testing.T) {
	e := setup(t)
	tf := e.newTayfa(t, "approval")
	mod := e.seed(t, tf.id, authz.Moderator, authz.StatusActive)
	e.seed(t, tf.id, authz.Member, authz.StatusActive)
	e.seed(t, tf.id, authz.Member, authz.StatusBanned)

	u := id.New()
	if _, err := e.svc.JoinByCode(ctx, u, tf.code); err != nil {
		t.Fatal(err)
	}
	req := noticesOf(t, e.pool, notify.TypeJoinRequest)
	if len(req) != 1 || req[0].actor != u || req[0].target != u || req[0].payload["target_type"] != notify.TargetMembership ||
		!sameUsers(req[0].recipients, tf.admin, mod) {
		t.Fatalf("başvuru = %+v", req)
	}
	if _, err := e.svc.ApproveMember(ctx, mod, tf.id, u); err != nil {
		t.Fatal(err)
	}
	ok := noticesOf(t, e.pool, notify.TypeJoinApproved)
	if len(ok) != 1 || ok[0].actor != mod || ok[0].target != tf.id || !sameUsers(ok[0].recipients, u) {
		t.Fatalf("onay = %+v", ok)
	}

	open := e.newTayfa(t, "open")
	if _, err := e.svc.JoinByCode(ctx, id.New(), open.code); err != nil {
		t.Fatal(err)
	}
	if n := len(noticesOf(t, e.pool, notify.TypeJoinRequest)); n != 1 {
		t.Fatalf("açık tayfaya katılım bildirim istedi: %d", n)
	}
}
