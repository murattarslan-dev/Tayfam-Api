package admin

import (
	"errors"
	"strings"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/chatrelay"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// A reported channel message (issue #205, ADR-0031): the panel shows it as
// it was filed (text, place, or that there is a photo), signs the photo's
// link only when opened (its own audit action), and a content_removed
// decision asks chat to hide it.
func TestChannelMessageReport(t *testing.T) {
	e := setup(t)
	fm := &fakeMedia{}
	e.svc.cfg.Media = fm
	c := e.crew(t)
	ch := id.New()
	file := func(kind, body string, photo id.ID, lat, lng any, name any) (id.ID, id.ID) {
		t.Helper()
		r, msg := id.New(), id.New()
		var p any
		if photo != (id.ID{}) {
			p = photo.PG()
		}
		var b any
		if body != "" {
			b = body
		}
		_, err := e.pool.Exec(ctx, `INSERT INTO reports (id, tayfa_id, reporter_id, target_type, target_id, subject_user_id, reason,
			priority, platform_only, escalated_to_platform_at, escalation_reason, platform_status, created_at,
			channel_id, message_kind, message_body, message_photo_upload_id, place_latitude, place_longitude, place_name)
			VALUES ($1, $2, $3, 'channel_message', $4, $5, 'harassment', 'normal', false, now(), 'unresolved', 'open', now(),
			$6, $7, $8, $9, $10, $11, $12)`,
			r.PG(), c.tayfa.PG(), c.mod.PG(), msg.PG(), c.member.PG(), ch.PG(), kind, b, p, lat, lng, name)
		must(t, err)
		return r, msg
	}

	text, msg := file("text", "Kaba bir mesaj", id.ID{}, nil, nil, nil)
	_, item, err := e.svc.GetReportedItem(ctx, e.claims, text)
	must(t, err)
	if item == nil || item.Body != "Kaba bir mesaj" || item.Author != c.member || item.Channel != ch || item.HasMessagePhoto || item.Place != nil {
		t.Fatalf("metin = %+v", item)
	}
	place, _ := file("place", "", id.ID{}, 38.4, 27.1, "İskele")
	if _, item, err := e.svc.GetReportedItem(ctx, e.claims, place); err != nil || item.Place == nil || item.Place.Name != "İskele" || item.Place.Latitude != 38.4 || item.Body != "" {
		t.Fatalf("yer = %+v %v", item, err)
	}
	up := id.New()
	photo, _ := file("photo", "", up, nil, nil, nil)
	if _, item, err := e.svc.GetReportedItem(ctx, e.claims, photo); err != nil || !item.HasMessagePhoto || item.MediaURL != "" || len(fm.asked) != 0 {
		t.Fatalf("fotoğraf = %+v %v; okumada link imzalanmamalı", item, err)
	}
	link, _, err := e.svc.ReportedMessagePhotoURL(ctx, e.claims, photo)
	if err != nil || link != "https://r2.example.test/mesaj" || len(fm.asked) != 1 || fm.asked[0] != up {
		t.Fatalf("link = %q %v %v", link, err, fm.asked)
	}
	a := e.audits(t)
	last := a[len(a)-1]
	if last["action"] != "report.view_message_photo" || last["upload_id"] != up.String() || last["url"] != "" {
		t.Fatalf("denetim = %v", a)
	}
	// No photo, no link.
	for _, r := range []id.ID{text, id.New()} {
		if _, _, err := e.svc.ReportedMessagePhotoURL(ctx, e.claims, r); !errors.Is(err, apperr.ErrReportNotFound) {
			t.Errorf("%v: %v", r, err)
		}
	}

	// content_removed: an event for chat, IDs only, with the administrator
	// as actor; the decision is recorded.
	must(t, e.svc.DecideReport(ctx, e.claims, text, DecisionContentRemoved, reason))
	var (
		n       int
		payload string
	)
	must(t, e.pool.QueryRow(ctx, `SELECT count(*), max(payload::text) FROM domain_events WHERE type = $1 AND actor_id = $2`,
		chatrelay.TypeChannelMessageRemoved, e.admin.PG()).Scan(&n, &payload))
	if n != 1 || !strings.Contains(payload, msg.String()) || strings.Contains(payload, "Kaba") {
		t.Fatalf("olay = %d %s", n, payload)
	}
}
