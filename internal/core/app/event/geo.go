package event

import "math"

// earthRadiusM is the mean Earth radius (IUGG) in metres.
const earthRadiusM = 6371008.8

// checkInRadiusM is the largest distance from the meeting point at which a
// location check-in succeeds (data model v14 "Konumla check-in").
const checkInRadiusM = 300

// distanceM returns the great-circle distance between a and b in metres
// (haversine). PostGIS is not used in the MVP (decision 2026-11-09); at
// check-in distances the spherical error is far below GPS accuracy.
func distanceM(a, b Point) float64 {
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLng := (b.Lng - a.Lng) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusM * math.Asin(math.Sqrt(math.Min(1, h)))
}

// withinCheckIn reports whether p is close enough to the meeting point.
func withinCheckIn(meeting, p Point) bool {
	return distanceM(meeting, p) <= checkInRadiusM
}
