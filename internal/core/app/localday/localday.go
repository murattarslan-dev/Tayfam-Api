// Package localday gives core's calendar "today": the Europe/Istanbul date
// (as for ages, data model v15). "Boşum" days are calendar dates, not
// instants (issue #207, ADR-0029).
package localday

import (
	"time"
	_ "time/tzdata" // Europe/Istanbul in every image
)

var istanbul = mustLoad("Europe/Istanbul")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err) // time/tzdata is embedded, so this cannot fail
	}
	return loc
}

// Today returns the Europe/Istanbul date of now, at midnight UTC.
func Today(now time.Time) time.Time {
	y, m, d := now.In(istanbul).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
