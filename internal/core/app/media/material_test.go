package media

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// A material's note photo (issue #261; ADR-0040 §3): the tayfa's active
// members see it while the materials module is on; nobody else.
func TestCanViewMaterialPhoto(t *testing.T) {
	now := time.Now()
	text := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
	row := func(status string, on bool) store.ListMediaForViewerRow {
		r := store.ListMediaForViewerRow{
			UserID: id.New().PG(), AttachedType: text(TargetMaterial), AttachedID: id.New().PG(),
			ContentTayfaID: id.New().PG(), MemberRole: text("member"), MaterialsModuleOn: on,
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
		"üye":             {row("active", true), true},
		"modül kapalı":    {row("active", false), false},
		"üye değil":       {row("", true), false},
		"onay bekleyen":   {row("pending", true), false},
		"silinmiş içerik": {store.ListMediaForViewerRow{UserID: id.New().PG(), AttachedType: text(TargetMaterial), MaterialsModuleOn: true}, false},
		"soru havuzu bayrağı yetmez": {func() store.ListMediaForViewerRow {
			r := row("active", false)
			r.QuestionPoolModuleOn = true
			return r
		}(), false},
	} {
		got, err := canView(id.New(), tc.r, now)
		if err != nil || got != tc.want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}
