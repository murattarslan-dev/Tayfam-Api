package report

import (
	"errors"
	"testing"

	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/media/mediatest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// A direct message report may carry a photo of the message instead of, or
// with, its text (issue #205, ADR-0031).
func TestDirectMessageReportEvidencePhoto(t *testing.T) {
	e := setup(t)
	chat := e.withChat(t)
	reporter, sender := id.New(), id.New()

	photo := mediatest.Ready(t, e.pool, reporter, media.PurposeReportEvidence, id.ID{})
	in := CreateInput{TargetType: TargetDMMessage, TargetID: chat.message(sender).String(), Reason: ReasonHarassment, EvidenceUpload: photo}
	v, created, err := e.svc.Create(ctx, reporter, id.ID{}, in)
	if err != nil || !created {
		t.Fatalf("yalnız fotoğrafla: %v", err)
	}
	var (
		text      *string
		attached  id.ID
		reportsUp id.ID
	)
	must(t, e.pool.QueryRow(ctx, "SELECT evidence_text, evidence_upload_id FROM reports WHERE id = $1", v.ID.PG()).Scan(&text, &reportsUp))
	must(t, e.pool.QueryRow(ctx, "SELECT attached_id FROM uploads WHERE id = $1 AND attached_type = 'report'", photo.PG()).Scan(&attached))
	if text != nil || reportsUp != photo || attached != v.ID {
		t.Fatalf("rapor: metin %v, fotoğraf %v, bağlı %v", text, reportsUp, attached)
	}

	// The same photo cannot go to a second report.
	in2 := CreateInput{TargetType: TargetDMMessage, TargetID: chat.message(sender).String(), Reason: ReasonHarassment, EvidenceUpload: photo}
	if _, _, err := e.svc.Create(ctx, reporter, id.ID{}, in2); !errors.Is(err, apperr.ErrUploadInUse) {
		t.Fatalf("kullanılmış fotoğraf: %v", err)
	}
	// Someone else's, another purpose or unknown.
	other := mediatest.Ready(t, e.pool, sender, media.PurposeReportEvidence, id.ID{})
	avatar := mediatest.Ready(t, e.pool, reporter, media.PurposeAvatar, id.ID{})
	for name, up := range map[string]id.ID{"başkasının": other, "avatar": avatar, "bilinmeyen": id.New()} {
		in := CreateInput{TargetType: TargetDMMessage, TargetID: chat.message(sender).String(), Reason: ReasonHarassment, EvidenceUpload: up}
		if _, _, err := e.svc.Create(ctx, reporter, id.ID{}, in); !errors.Is(err, apperr.ErrUploadNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A refused report leaves nothing attached.
	var n int
	must(t, e.pool.QueryRow(ctx, "SELECT count(*) FROM uploads WHERE attached_type = 'report'").Scan(&n))
	if n != 1 {
		t.Fatalf("bağlı kanıt sayısı = %d", n)
	}
	// Text and photo together.
	both := mediatest.Ready(t, e.pool, reporter, media.PurposeReportEvidence, id.ID{})
	in3 := dmInput(chat.message(sender))
	in3.EvidenceUpload = both
	if _, created, err := e.svc.Create(ctx, reporter, id.ID{}, in3); err != nil || !created {
		t.Fatalf("metin ve fotoğraf: %v", err)
	}
	// Neither is a validation error.
	in4 := CreateInput{TargetType: TargetDMMessage, TargetID: chat.message(sender).String(), Reason: ReasonHarassment}
	_, _, err = e.svc.Create(ctx, reporter, id.ID{}, in4)
	isValidation(t, "kanıtsız", err, "evidence_text")
	// Only direct message reports carry a photo.
	c := e.crew(t)
	_, _, err = e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetUser, TargetID: c.member2.String(), Reason: ReasonSpam, EvidenceUpload: photo})
	isValidation(t, "üye şikâyetinde fotoğraf", err, "evidence_upload_id")
	_, _, err = e.svc.Create(ctx, c.member, id.ID{}, CreateInput{TargetType: TargetUser, TargetID: c.member2.String(), Reason: ReasonSpam, EvidenceUpload: photo})
	isValidation(t, "profil şikâyetinde fotoğraf", err, "evidence_upload_id")

	// The report goes after a year: the photo goes with it.
	if _, err := e.pool.Exec(ctx, "DELETE FROM reports WHERE id = $1", v.ID.PG()); err != nil {
		t.Fatal(err)
	}
	if s := mediatest.Status(t, e.pool, photo); s != "deleting" {
		t.Fatalf("rapor silinince fotoğraf: %s", s)
	}
}
