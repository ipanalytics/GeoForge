// Package refdata holds compile-time reference tables: country names,
// continent maps, calling codes, currencies, EU membership, source-priority
// tables, and the CIS country set used to decide whether to consult Sypex.
package refdata

// SourcePriority defines per-country preference order for picking the primary
// candidate when sources disagree. The earlier in the list, the higher the
// trust for that region.
//
// Reasoning:
//   - Sypex is run by a Russian team with strong RIPE/RU coverage → CIS
//   - IP2Location is strong in Asia (their HQ is Malaysia)
//   - MaxMind dominates the western market (US/EU/global ASN coverage)
//   - DB-IP is the seed (defines which CIDR blocks exist) — last resort
//
// This is a TIE-BREAKER, not the only signal. Consensus voting still wins
// over priority: if 2 of 3 sources agree on the city, that beats priority.
var SourcePriority = map[string][]string{
	// CIS countries — Sypex first
	"RU": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"BY": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"UA": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"KZ": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"KG": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"UZ": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"TJ": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"AM": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"AZ": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},
	"MD": {"geofeed", "sypex", "ip2loc", "maxmind", "dbip"},

	// Asian markets where IP2Location historically excels
	"CN": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"JP": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"KR": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"VN": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"TH": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"MY": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"SG": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"ID": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"PH": {"geofeed", "ip2loc", "maxmind", "dbip"},
	"IN": {"geofeed", "ip2loc", "maxmind", "dbip"},
}

// DefaultPriority is used for countries not in the table above.
var DefaultPriority = []string{"geofeed", "maxmind", "ip2loc", "dbip"}

// CISCountries — used by the orchestrator to decide whether to query Sypex.
var CISCountries = map[string]bool{
	"RU": true, "UA": true, "BY": true, "KZ": true,
	"KG": true, "UZ": true, "TJ": true, "AM": true, "AZ": true, "MD": true,
}

// PriorityFor returns the priority list for a country (or DefaultPriority).
func PriorityFor(country string) []string {
	if p, ok := SourcePriority[country]; ok {
		return p
	}
	return DefaultPriority
}

// PriorityRank returns the rank of `source` for `country` (lower = better).
// Returns a high number if not in the priority list.
func PriorityRank(source, country string) int {
	list := PriorityFor(country)
	for i, s := range list {
		if s == source {
			return i
		}
	}
	return 100
}
