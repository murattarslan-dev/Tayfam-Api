package report

import (
	"io"
	"log/slog"
	"testing"
	"time"

	coredb "github.com/murattarslan-dev/tayfam-api/db/core"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/announcement"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/apperr"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/qa"
	"github.com/murattarslan-dev/tayfam-api/internal/core/app/tayfa"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/db/dbtest"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// Question pool content (issue #271) is reported as a question or an
// answer; its module is the question pool's, and paid_tutoring is a reason.
func TestReportQuestionPool(t *testing.T) {
	d := dbtest.Open(t, "core", coredb.Migrations())
	e := &env{pool: d.App, minors: &fakeMinors{minors: map[id.ID]bool{}}}
	now := func() time.Time { return time.Now().Add(e.shift) }
	var err error
	e.svc, err = New(Config{Pool: d.App, Minors: e.minors, Now: now})
	must(t, err)
	e.tayfas, err = tayfa.New(tayfa.Config{Pool: d.App, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	e.ann, err = announcement.New(announcement.Config{Pool: d.App, Now: now})
	must(t, err)
	_, err = d.Admin.Exec(ctx, `INSERT INTO catalog_study_tags (key, parent_key, position, labels) VALUES
		('level_a', NULL, 1, '{"tr": "Düzey A"}'), ('subject_a', 'level_a', 1, '{"tr": "Ders A"}')`)
	must(t, err)

	c := e.crew(t)
	qs, err := qa.New(qa.Config{Pool: d.App})
	must(t, err)
	q, err := qs.CreateQuestion(ctx, c.member2, c.tayfa, qa.CreateQuestionInput{
		Pool: qa.PoolQuestionPool, SubjectKey: "subject_a", Body: "Özel ders veriyorum, yazın", ClientRequestID: id.New().String(),
	})
	must(t, err)
	a, err := qs.CreateAnswer(ctx, c.member2, c.tayfa, q.ID, qa.CreateAnswerInput{Body: "Ücretli anlatırım", ClientRequestID: id.New().String()})
	must(t, err)

	// Soru-Cevap off does not hide the pool.
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "qa", false))
	v := e.report(t, c, c.member, TargetQuestion, q.ID, ReasonPaidTutoring)
	if v.Reason != ReasonPaidTutoring || v.TargetType != TargetQuestion {
		t.Fatalf("şikâyet: %+v", v)
	}
	// The question pool off hides its content.
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "qa", true))
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "question_pool", false))
	_, _, err = e.svc.Create(ctx, c.member, c.tayfa, CreateInput{TargetType: TargetAnswer, TargetID: a.ID.String(), Reason: ReasonPaidTutoring})
	is(t, "kapalı soru havuzu", err, apperr.ErrReportTargetNotFound)
	must(t, e.tayfas.SetModule(ctx, c.admin, c.tayfa, "question_pool", true))
	e.report(t, c, c.member, TargetAnswer, a.ID, ReasonPaidTutoring)
}
