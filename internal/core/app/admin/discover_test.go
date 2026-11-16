package admin

import (
	"testing"
	"time"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// TestDiscoverReview (issue #210, ADR-0033 §8): the queue shows pending and
// dropped applications with counts only; approve and reject need a reason
// and a recent sign-in and leave both audit records.
func TestDiscoverReview(t *testing.T) {
	e := setup(t)
	c := e.crew(t)
	is(t, "başvuru yokken onay", e.svc.ApproveDiscover(ctx, e.claims, c.tayfa, reason), apperr.ErrTayfaStateConflict)

	if _, err := e.super.Exec(ctx, `UPDATE tayfas SET visibility = 'listed' WHERE id = $1`, c.tayfa.PG()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.super.Exec(ctx, `INSERT INTO tayfa_discover (tayfa_id, status, drop_reason, requested_at, status_changed_at)
	    VALUES ($1, 'dropped', 'negative_feedback', now(), now())`, c.tayfa.PG()); err != nil {
		t.Fatal(err)
	}
	q, err := e.svc.ListDiscoverQueue(ctx, e.claims)
	must(t, err)
	if len(q) != 1 || q[0].TayfaID != c.tayfa || q[0].Status != "dropped" || q[0].DropReason != "negative_feedback" ||
		q[0].MemberCount != 3 || !q[0].Enabled {
		t.Fatalf("kuyruk = %+v", q)
	}

	is(t, "gerekçesiz", e.svc.ApproveDiscover(ctx, e.claims, c.tayfa, "kısa"), apperr.ErrReasonRequired)
	old := e.claims
	old.AuthTime = e.now.Add(-time.Hour)
	is(t, "eski giriş", e.svc.ApproveDiscover(ctx, old, c.tayfa, reason), apperr.ErrReauthenticationRequired)

	must(t, e.svc.ApproveDiscover(ctx, e.claims, c.tayfa, reason))
	if e.count(t, `SELECT count(*) FROM tayfa_discover WHERE tayfa_id = $1 AND status = 'approved' AND drop_reason IS NULL AND reviewed_by = $2`,
		c.tayfa.PG(), e.admin.PG()) != 1 {
		t.Fatal("onaylanmadı")
	}
	if q, _ := e.svc.ListDiscoverQueue(ctx, e.claims); len(q) != 0 {
		t.Fatalf("onaylı tayfa kuyrukta: %+v", q)
	}
	is(t, "ikinci onay", e.svc.ApproveDiscover(ctx, e.claims, c.tayfa, reason), apperr.ErrTayfaStateConflict)
	must(t, e.svc.RejectDiscover(ctx, e.claims, c.tayfa, reason))
	is(t, "ikinci ret", e.svc.RejectDiscover(ctx, e.claims, c.tayfa, reason), apperr.ErrTayfaStateConflict)
	is(t, "bilinmeyen tayfa", e.svc.RejectDiscover(ctx, e.claims, id.New(), reason), apperr.ErrTayfaNotFound)

	a := e.audits(t)
	if len(a) != 2 || a[0]["action"] != "tayfa.discover_approve" || a[1]["action"] != "tayfa.discover_reject" || a[1]["reason"] != reason {
		t.Fatalf("denetim olayları = %v", a)
	}
	if e.count(t, `SELECT count(*) FROM audit_logs WHERE tayfa_id = $1 AND action IN ('platform.discover_approved', 'platform.discover_rejected') AND actor_id IS NULL`,
		c.tayfa.PG()) != 2 {
		t.Fatal("tayfanın denetim kaydı yok ya da yöneticiyi gösteriyor")
	}
}
