package admin

import (
	"errors"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// A DM report's evidence photo (issue #205, ADR-0031): reading the report
// says only that there is one; the link is signed when the administrator
// opens it, as its own audit action.
func TestReportEvidencePhoto(t *testing.T) {
	e := setup(t)
	fm := &fakeMedia{}
	e.svc.cfg.Media = fm
	reporter, sender := id.New(), id.New()
	rep := id.New()
	up := mediatest.Ready(t, e.pool, reporter, media.PurposeReportEvidence, id.ID{})
	_, err := e.pool.Exec(ctx, `UPDATE uploads SET attached_type = 'report', attached_id = $2, attached_at = now() WHERE id = $1`, up.PG(), rep.PG())
	must(t, err)
	_, err = e.pool.Exec(ctx, `INSERT INTO reports (id, reporter_id, target_type, target_id, subject_user_id, reason, evidence_upload_id,
		priority, platform_only, escalated_to_platform_at, escalation_reason, platform_status, created_at)
		VALUES ($1, $2, 'dm_message', $3, $4, 'harassment', $5, 'normal', true, now(), 'direct_message', 'open', now())`,
		rep.PG(), reporter.PG(), id.New().PG(), sender.PG(), up.PG())
	must(t, err)

	_, item, err := e.svc.GetReportedItem(ctx, e.claims, rep)
	must(t, err)
	if item == nil || !item.HasEvidencePhoto || item.MediaURL != "" || len(fm.asked) != 0 {
		t.Fatalf("öğe = %+v; okumada link imzalanmamalı (%v)", item, fm.asked)
	}
	link, _, err := e.svc.ReportEvidencePhotoURL(ctx, e.claims, rep)
	if err != nil || link != "https://r2.example.test/kanit" || len(fm.asked) != 1 || fm.asked[0] != up {
		t.Fatalf("link = %q %v %v", link, err, fm.asked)
	}
	a := e.audits(t)
	if len(a) != 2 || a[0]["action"] != "report.view_item" || a[1]["action"] != "report.view_evidence_photo" || a[1]["upload_id"] != up.String() {
		t.Fatalf("denetim kayıtları = %v", a)
	}
	for _, v := range a {
		if v["media_link"] != "" || v["url"] != "" {
			t.Fatalf("link denetime yazılmamalı: %v", v)
		}
	}
	// A report without a photo, or an unknown one.
	other := id.New()
	_, err = e.pool.Exec(ctx, `INSERT INTO reports (id, reporter_id, target_type, target_id, subject_user_id, reason, evidence_text,
		priority, platform_only, escalated_to_platform_at, escalation_reason, platform_status, created_at)
		VALUES ($1, $2, 'dm_message', $3, $4, 'harassment', 'metin', 'normal', true, now(), 'direct_message', 'open', now())`,
		other.PG(), reporter.PG(), id.New().PG(), sender.PG())
	must(t, err)
	for _, r := range []id.ID{other, id.New()} {
		if _, _, err := e.svc.ReportEvidencePhotoURL(ctx, e.claims, r); !errors.Is(err, apperr.ErrReportNotFound) {
			t.Errorf("%v: %v", r, err)
		}
	}
	if _, item, err := e.svc.GetReportedItem(ctx, e.claims, other); err != nil || item.HasEvidencePhoto {
		t.Fatalf("fotoğrafsız: %+v %v", item, err)
	}
}
