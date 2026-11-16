package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// A channel message report keeps the message as it was (issue #205,
// ADR-0031): exactly what its kind needs, within the limits, never changed
// afterwards.
func TestChannelMessageReportConstraints(t *testing.T) {
	pool, q := setup(t)
	ctx := context.Background()
	admin := id.New()
	tf := newTayfa(t, pool, admin, tayfaOpts{})
	text := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
	f8 := func(v float64) pgtype.Float8 { return pgtype.Float8{Float64: v, Valid: true} }
	base := func() CreateReportParams {
		return CreateReportParams{
			ID: id.New().PG(), TayfaID: tf.PG(), ReporterID: id.New().PG(), TargetType: "channel_message", TargetID: id.New().PG(),
			SubjectUserID: id.New().PG(), Reason: "harassment", Priority: "normal", CreatedAt: ts(time.Now()),
			ChannelID: id.New().PG(), MessageKind: text("text"), MessageBody: text("kaba"),
		}
	}
	for name, tc := range map[string]struct {
		mut  func(p *CreateReportParams)
		cons string
	}{
		"kanal yok":        {func(p *CreateReportParams) { p.ChannelID = pgtype.UUID{} }, "reports_channel_message"},
		"tür yok":          {func(p *CreateReportParams) { p.MessageKind = pgtype.Text{} }, "reports_channel_message"},
		"bilinmeyen tür":   {func(p *CreateReportParams) { p.MessageKind = text("video") }, "reports_channel_message"},
		"metinsiz metin":   {func(p *CreateReportParams) { p.MessageBody = pgtype.Text{} }, "reports_channel_message"},
		"metinde fotoğraf": {func(p *CreateReportParams) { p.MessagePhotoUploadID = id.New().PG() }, "reports_channel_message"},
		"metinde yer adı":  {func(p *CreateReportParams) { p.PlaceName = text("İskele") }, "reports_channel_message"},
		"fotoğrafsız":      {func(p *CreateReportParams) { p.MessageKind, p.MessageBody = text("photo"), pgtype.Text{} }, "reports_channel_message"},
		"yarım yer": {func(p *CreateReportParams) {
			p.MessageKind, p.MessageBody, p.PlaceLatitude = text("place"), pgtype.Text{}, f8(1)
		}, "reports_channel_message"},
		"uzun metin":    {func(p *CreateReportParams) { p.MessageBody = text(strings.Repeat("ş", 4001)) }, "reports_message_body_len"},
		"başka hedefte": {func(p *CreateReportParams) { p.TargetType = "announcement" }, "reports_channel_message"},
		"dünya dışı enlem": {func(p *CreateReportParams) {
			p.MessageKind, p.MessageBody, p.PlaceLatitude, p.PlaceLongitude = text("place"), pgtype.Text{}, f8(91), f8(1)
		}, "reports_place"},
		"bilinmeyen hedef": {func(p *CreateReportParams) {
			p.TargetType, p.ChannelID, p.MessageKind, p.MessageBody = "channel", pgtype.UUID{}, pgtype.Text{}, pgtype.Text{}
		}, "reports_target_type"},
	} {
		t.Run(name, func(t *testing.T) {
			p := base()
			tc.mut(&p)
			_, err := q.CreateReport(ctx, p)
			if got := constraintOf(err); got != tc.cons {
				t.Fatalf("kısıt = %q (err=%v), %q bekleniyordu", got, err, tc.cons)
			}
		})
	}
	// Each kind as it should be.
	for _, mut := range []func(p *CreateReportParams){
		func(*CreateReportParams) {},
		func(p *CreateReportParams) {
			p.MessageKind, p.MessageBody, p.MessagePhotoUploadID = text("photo"), pgtype.Text{}, id.New().PG()
		},
		func(p *CreateReportParams) {
			p.MessageKind, p.MessageBody, p.PlaceLatitude, p.PlaceLongitude, p.PlaceName = text("place"), pgtype.Text{}, f8(38.4), f8(27.1), text("İskele")
		},
	} {
		p := base()
		mut(&p)
		if _, err := q.CreateReport(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	// What was reported never changes.
	p := base()
	if _, err := q.CreateReport(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE reports SET message_body = 'değişti' WHERE id = $1`, p.ID); err == nil {
		t.Fatal("şikâyet edilen mesaj değiştirildi")
	}
}
