package media

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/murattarslan-dev/tayfam-api/internal/core/store"
	"github.com/murattarslan-dev/tayfam-api/internal/platform/id"
)

// A garage vehicle's photo (issue #272; ADR-0040 §4): the SQL
// garage_vehicle_seen_by decides (garage_visible); the owner always sees
// their own; nothing else opens it.
func TestCanViewGaragePhoto(t *testing.T) {
	now := time.Now()
	text := func(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }
	owner := id.New()
	row := func(purpose string, visible bool) store.ListMediaForViewerRow {
		return store.ListMediaForViewerRow{
			UserID: owner.PG(), Purpose: purpose, AttachedType: text(TargetGarageVehicle), AttachedID: id.New().PG(),
			GarageVisible: visible,
			// A tayfa membership alone never opens a garage photo.
			ContentTayfaID: id.New().PG(), MemberRole: text("member"), MemberStatus: text("active"), EventsModuleOn: true,
		}
	}
	for name, tc := range map[string]struct {
		viewer id.ID
		r      store.ListMediaForViewerRow
		want   bool
	}{
		"görünür tayfadaki üye": {id.New(), row(PurposeGarageVehicle, true), true},
		"görünmeyen":            {id.New(), row(PurposeGarageVehicle, false), false},
		"sahibi":                {owner, row(PurposeGarageVehicle, false), true},
		"başka amaçlı dosya":    {id.New(), row(PurposeAvatar, true), false},
		"hesabı silinmiş sahibin aracı": {id.New(), func() store.ListMediaForViewerRow {
			r := row(PurposeGarageVehicle, false)
			r.UserID = pgtype.UUID{}
			return r
		}(), false},
	} {
		got, err := canView(tc.viewer, tc.r, now)
		if err != nil || got != tc.want {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}
