package consensus

import (
	"math"
	"strings"
)

// Pre-validation lowers (or zeros) the trust score of candidates with obviously
// suspicious data before it enters the consensus engine. The point is
// to neutralise:
//   - "Null Island" (0,0) sentinels
//   - country-fallback centres (always (55.75, 37.61) for everything in RU)
//   - coordinates outside the country's bounding box
//   - empty / placeholder strings ("-", "N/A", etc.)
//
// This is the first line of validation against bad coordinates and against the
// natural ambiguity of LITE databases that "fall back" to country centroids.

// CountryBBox roughly delimits each country's geographic extent.
// (minLat, maxLat, minLon, maxLon). Used to spot data that's lying about
// being in a country. Boxes are intentionally generous to catch
// "obvious garbage" (coords in the ocean, wrong continent), not edge cases.
type bbox struct {
	minLat, maxLat, minLon, maxLon float64
}

var countryBBoxes = map[string]bbox{
	"RU": {41.18, 81.85, 19.64, -169.05}, // crosses 180° — handled below
	"US": {18.92, 71.39, -171.79, -66.96},
	"CA": {41.68, 83.11, -141.00, -52.62},
	"BR": {-33.75, 5.27, -73.99, -34.79},
	"CN": {18.16, 53.55, 73.50, 134.77},
	"AU": {-43.64, -10.66, 113.15, 153.64},
	"IN": {6.75, 35.50, 68.16, 97.40},
	"DE": {47.27, 55.06, 5.87, 15.04},
	"FR": {41.33, 51.12, -5.14, 9.56},
	"GB": {49.91, 60.85, -8.62, 1.76},
	"UA": {44.39, 52.38, 22.14, 40.23},
	"BY": {51.26, 56.17, 23.18, 32.78},
	"KZ": {40.57, 55.45, 46.49, 87.32},
	"JP": {24.04, 45.55, 122.93, 145.82},
	"KR": {33.11, 38.61, 124.61, 131.87},
	"IT": {35.49, 47.10, 6.62, 18.52},
	"ES": {27.64, 43.79, -18.16, 4.32},
	"PL": {49.00, 54.84, 14.12, 24.15},
	"TR": {35.81, 42.10, 25.66, 44.83},
	"MX": {14.53, 32.72, -118.40, -86.71},
	"AR": {-55.06, -21.78, -73.58, -53.63},
	// extend as needed; missing countries skip the bbox check
}

// Common "country-fallback" coordinates that LITE databases use when they
// cannot place an IP. Exact matches are treated
// the coords as untrusted (trust < 30) — a real city centre will have its
// own coordinates, not the country centroid.
var countryFallbackCoords = map[string][][2]float64{
	"RU": {{55.7558, 37.6173}, {55.75, 37.62}, {60.0, 100.0}}, // Moscow + Siberia centre
	"US": {{37.7510, -97.8220}, {39.76, -98.50}},              // Kansas centre
	"CN": {{34.7732, 113.7220}, {35.0, 105.0}},                // China centre
	"DE": {{51.2993, 9.491}, {51.0, 9.0}},
	"FR": {{46.2276, 2.2137}, {46.0, 2.0}},
	"GB": {{55.3781, -3.4360}, {54.0, -2.0}},
	"BR": {{-14.235, -51.9253}, {-10.0, -55.0}},
}

const fallbackTolerance = 0.05 // ~5km

// preValidateTrust returns a 0..100 trust score for a candidate.
// 0   → fully discarded
// 100 → no red flags
func preValidateTrust(c *Candidate, expectedCountry string) int {
	trust := 100

	// Empty / placeholder strings are stripped before scoring.
	c.City = scrubString(c.City)
	c.CityRu = scrubString(c.CityRu)
	c.State = scrubString(c.State)
	c.Postal = scrubPostal(c.Postal)

	// Country mismatch is severe — IP belongs to one country, source claims another
	if c.Country != "" && expectedCountry != "" && c.Country != expectedCountry {
		trust -= 40
	}

	// Coordinate sanity
	if c.HasCoords {
		// Null Island
		if math.Abs(c.Latitude) < 0.01 && math.Abs(c.Longitude) < 0.01 {
			c.HasCoords = false
			c.Latitude = 0
			c.Longitude = 0
			trust -= 25
		}
		// Out of valid range (corrupted data)
		if c.Latitude < -90 || c.Latitude > 90 || c.Longitude < -180 || c.Longitude > 180 {
			c.HasCoords = false
			c.Latitude = 0
			c.Longitude = 0
			trust -= 30
		}
		// Country bounding box check
		if c.HasCoords && expectedCountry != "" {
			if !inCountryBBox(expectedCountry, c.Latitude, c.Longitude) {
				// Coords claim to be in this country but aren't — likely a lie
				trust -= 20
			}
		}
		// Country-fallback centroid → coords are untrusted, but record itself
		// might still have a useful city name
		if c.HasCoords && isCountryFallback(expectedCountry, c.Latitude, c.Longitude) {
			// Mark coordinates as unusable without dropping the full candidate.
			c.HasCoords = false
			trust -= 15
		}
	}

	// No useful information at all
	if c.City == "" && c.CityRu == "" && c.Postal == "" && !c.HasCoords {
		return 0
	}

	if trust < 0 {
		trust = 0
	}
	return trust
}

func inCountryBBox(country string, lat, lon float64) bool {
	bb, ok := countryBBoxes[country]
	if !ok {
		return true // no data; no penalty
	}
	if lat < bb.minLat || lat > bb.maxLat {
		return false
	}
	// Russia crosses the antimeridian: minLon=19.64, maxLon=-169.05 wraps east
	if bb.minLon > bb.maxLon {
		return lon >= bb.minLon || lon <= bb.maxLon
	}
	return lon >= bb.minLon && lon <= bb.maxLon
}

func isCountryFallback(country string, lat, lon float64) bool {
	for _, fc := range countryFallbackCoords[country] {
		if math.Abs(lat-fc[0]) < fallbackTolerance && math.Abs(lon-fc[1]) < fallbackTolerance {
			return true
		}
	}
	return false
}

// scrubString removes placeholder values that LITE databases emit when they
// cannot provide an answer. Parsing them as real data is a source of
// "Frankenstein" output.
func scrubString(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	low := strings.ToLower(t)
	switch low {
	case "-", "n/a", "na", "unknown", "null", "undefined", "none":
		return ""
	}
	if strings.Contains(low, "unavailable") ||
		strings.Contains(low, "not supported") ||
		strings.Contains(low, "this parameter is") {
		return ""
	}
	return t
}

func scrubPostal(s string) string {
	t := scrubString(s)
	// Some sources emit "0", "00000", "----" as placeholder ZIPs
	if t == "" {
		return ""
	}
	allZero := true
	for _, r := range t {
		if r != '0' && r != '-' && r != ' ' {
			allZero = false
			break
		}
	}
	if allZero {
		return ""
	}
	return t
}
