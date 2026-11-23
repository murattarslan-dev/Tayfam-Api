package media

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// A question pool photo (issue #271; ADR-0040 §2): the tayfa's active
// members see it while the question_pool module is on; nobody else.
func TestCanViewQuestionPoolPhoto(t *testing.T) {
	now := time.Now()
	text := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
	row := func(target, status string, on bool) store.ListMediaForViewerRow {
		r := store.ListMediaForViewerRow{
			UserID: id.New().PG(), AttachedType: text(target), AttachedID: id.New().PG(),
			ContentTayfaID: id.New().PG(), MemberRole: text("member"), QuestionPoolModuleOn: on,
		}
		if status != "" {
			r.MemberStatus = text(status)
		}
		return r
	}
	for name, tc := range map[string]struct {
		r    store.ListMediaForViewerRow
		want bool
	}{
		"soru, üye":       {row(TargetQuestion, "active", true), true},
		"cevap, üye":      {row(TargetAnswer, "active", true), true},
		"modül kapalı":    {row(TargetQuestion, "active", false), false},
		"üye değil":       {row(TargetAnswer, "", true), false},
		"onay bekleyen":   {row(TargetQuestion, "pending", true), false},
		"silinmiş içerik": {store.ListMediaForViewerRow{UserID: id.New().PG(), AttachedType: text(TargetQuestion), QuestionPoolModuleOn: true}, false},
		"kütüphane bayrağı yetmez": {func() store.ListMediaForViewerRow {
			r := row(TargetQuestion, "active", false)
			r.LibraryModuleOn = true
			return r
		}(), false},
	} {
		got, err := canView(id.New(), tc.r, now)
		if err != nil || got != tc.want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}
