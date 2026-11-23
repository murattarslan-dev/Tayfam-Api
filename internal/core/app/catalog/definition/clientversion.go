package definition

import (
	"strconv"
	"strings"
)

// v3 is the first client version that draws events from the catalog
// (ADR-0041 §4).
const v3 = "0.3.0"

// MinClientVersions is, for every component kind, the lowest client
// version that knows it (ADR-0025 §7). An older client shows "update the
// app" in that component's place (ADR-0025 §9). A new kind is added here
// together with its server and client code.
var MinClientVersions = map[string]string{
	CompRoute:        v3,
	CompMeetingPoint: v3,
	CompSchedule:     v3,
	CompChecklist:    v3,
	CompRollCall:     v3,
	CompVehicles:     v3,
	CompTeams:        v3,
	CompDraw:         v3,
	CompBracket:      v3,
	CompScoreboard:   v3,
	CompRanking:      v3,
	CompVote:         v3,
	CompCelebration:  v3,
	CompMeasure:      v3,
	CompGoal:         v3,
	CompLog:          v3,
	CompQuestions:    v3,
	CompTimer:        v3,
	CompPoolLink:     v3,
}

// MinClientVersion is the lowest client version that knows every component
// of the definition; v3 for a definition without components.
func (d *Definition) MinClientVersion() string {
	out := v3
	for _, c := range d.Components {
		if v := MinClientVersions[c.Kind]; versionLess(out, v) {
			out = v
		}
	}
	return out
}

// versionLess compares two MAJOR.MINOR.PATCH versions.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
