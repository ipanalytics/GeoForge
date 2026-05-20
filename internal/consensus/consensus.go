package consensus

import (
	"math"
	"sort"

	"geoip-builder/internal/refdata"
	"geoip-builder/internal/strnorm"
)

// Candidate is one source's view of a single IP record. Treating each source
// as an immutable snapshot keeps source semantics independent until merge time.
type Candidate struct {
	Source    string // "dbip" | "maxmind" | "ip2loc" | "sypex"
	Country   string // ISO 3166-1 alpha-2 from the source
	State     string
	City      string
	CityRu    string // Cyrillic name where available (Sypex)
	Postal    string
	Latitude  float64
	Longitude float64
	HasCoords bool
	Trust     int // set by preValidateTrust, 0..100
}

// MergedRecord is the consensus output written to MMDB.
type MergedRecord struct {
	Country         string
	RegistryCountry string
	State           string
	City            string
	Postal          string
	Latitude        float64
	Longitude       float64
	GeonameID       int64    // when coords were snapped to GeoNames, transparency
	Confidence      int      // 0..100, how sure are we
	Sources         []string // contributing sources
	Snapped         bool     // true if coords come from GeoNames, not a donor
}

func (r MergedRecord) HasCoords() bool {
	return r.Latitude != 0 || r.Longitude != 0
}

// AgreementLevel classifies how much two candidates agree.
type AgreementLevel int

const (
	AgreementConflict AgreementLevel = iota
	AgreementWeak
	AgreementModerate
	AgreementGood
	AgreementHigh
)

// MatchScore is the result of comparing two candidates.
type MatchScore struct {
	GeoProximity int // 0..100 (or 50 if no coords on either side)
	NameMatch    int // 0..100
	PostalMatch  int // -1 if N/A, else 0..100
	StateMatch   int // -1 if N/A, else 0..100
	Total        int // weighted average, 0..100
	Agreement    AgreementLevel
}

// ScoreWeights — sum need not equal 1; weighted average normalises by
// the sum of *applicable* weights (so missing PostalMatch doesn't drag the
// total down). Tweak these to push the system toward different priorities.
var ScoreWeights = struct {
	Geo, Name, Postal, State float64
}{
	Geo:    0.35,
	Name:   0.30,
	Postal: 0.20,
	State:  0.15,
}

// Distance buckets for the geo-proximity score. Tuned for the realities of
// IP geolocation (where most "errors" are wrong-city-in-the-right-region).
const (
	distSameSpot    = 5.0   // 100
	distSameUrban   = 25.0  // 85
	distSameMetro   = 75.0  // 65
	distSameRegion  = 200.0 // 40
	distSameCountry = 500.0 // 15
	// > 500 km → 0
)

// MergeCandidates is the main entry point of the consensus engine.
//
// Pipeline:
//  1. Pre-validate each candidate (drop Null Island, country fallbacks, ...)
//  2. Compute all pairwise agreements
//  3. Find the largest mutually-agreeing cluster — this is the "truth set"
//  4. Inside the cluster, pick the primary by per-country priority
//  5. Donate empty fields from secondaries, with field-level safety rules
//  6. Compute final confidence
//
// Coordinate normalization happens later in main.go via the GeoNames index.
func MergeCandidates(candidates []Candidate, country string) MergedRecord {
	if len(candidates) == 0 {
		return MergedRecord{Country: country}
	}

	// 1. Pre-validate
	for i := range candidates {
		candidates[i].Trust = preValidateTrust(&candidates[i], country)
	}

	// 2. Drop fully-untrusted candidates (no usable info at all).
	// New slice — never reuse the input backing array during filtering.
	valid := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Trust > 0 {
			valid = append(valid, c)
		}
	}
	if len(valid) == 0 {
		return MergedRecord{Country: country}
	}

	// 3. Find consensus cluster (largest mutually-agreeing subset)
	cluster := findConsensusCluster(valid)

	// 4. Pick primary inside the cluster by priority
	primary := selectPrimary(cluster, country)

	// 5. Build the merged record starting from primary
	result := MergedRecord{
		Country:   primary.Country,
		State:     primary.State,
		City:      primary.City,
		Postal:    primary.Postal,
		Latitude:  primary.Latitude,
		Longitude: primary.Longitude,
		Sources:   []string{primary.Source},
	}
	if primary.Country == "" {
		result.Country = country
	}

	// Pre-compute primary-vs-each-other scores so we can decide what to merge
	for _, c := range cluster {
		if c.Source == primary.Source {
			continue
		}
		s := computeMatchScore(primary, c)
		result = enrichWith(result, c, s)
	}

	// 6. Allow candidates OUTSIDE the cluster to fill *empty* fields only,
	// but cap their contribution by trust × 0.5 (low-confidence enrichment).
	// This catches cases like "Sypex disagrees on the city, but is the only
	// source with a postal code".
	for _, c := range valid {
		if inSlice(cluster, c) {
			continue
		}
		result = lowConfidenceFill(result, c)
	}

	// 7. Confidence
	result.Confidence = computeConfidence(result, cluster, len(valid))

	return result
}

// findConsensusCluster: build a graph where edge (i,j) exists if candidates
// i and j have AgreementGood or higher. Return the largest connected component.
// Falls back to the highest-trust singleton if no edges exist.
func findConsensusCluster(cands []Candidate) []Candidate {
	n := len(cands)
	if n <= 1 {
		return cands
	}

	adj := make([][]int, n)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			s := computeMatchScore(cands[i], cands[j])
			if s.Agreement >= AgreementGood {
				adj[i] = append(adj[i], j)
				adj[j] = append(adj[j], i)
			}
		}
	}

	visited := make([]bool, n)
	var bestCluster []int
	for i := 0; i < n; i++ {
		if visited[i] {
			continue
		}
		// BFS
		component := []int{}
		queue := []int{i}
		visited[i] = true
		for len(queue) > 0 {
			v := queue[0]
			queue = queue[1:]
			component = append(component, v)
			for _, w := range adj[v] {
				if !visited[w] {
					visited[w] = true
					queue = append(queue, w)
				}
			}
		}
		if len(component) > len(bestCluster) {
			bestCluster = component
		} else if len(component) == len(bestCluster) {
			// tie-break: cluster with higher total trust wins
			if clusterTrust(cands, component) > clusterTrust(cands, bestCluster) {
				bestCluster = component
			}
		}
	}

	out := make([]Candidate, 0, len(bestCluster))
	for _, idx := range bestCluster {
		out = append(out, cands[idx])
	}
	return out
}

func clusterTrust(cands []Candidate, indices []int) int {
	t := 0
	for _, i := range indices {
		t += cands[i].Trust
	}
	return t
}

// selectPrimary picks the highest-priority candidate inside the cluster,
// breaking ties by trust score.
func selectPrimary(cluster []Candidate, country string) Candidate {
	if len(cluster) == 1 {
		return cluster[0]
	}
	sorted := make([]Candidate, len(cluster))
	copy(sorted, cluster)
	sort.SliceStable(sorted, func(i, j int) bool {
		ri := refdata.PriorityRank(sorted[i].Source, country)
		rj := refdata.PriorityRank(sorted[j].Source, country)
		if ri != rj {
			return ri < rj
		}
		return sorted[i].Trust > sorted[j].Trust
	})
	return sorted[0]
}

// computeMatchScore is the heart of the multi-factor system. Two candidates
// are compared on every available dimension; missing dimensions are skipped
// rather than treated as zero; incomplete dimensions are not penalized.
func computeMatchScore(a, b Candidate) MatchScore {
	s := MatchScore{PostalMatch: -1, StateMatch: -1}

	// --- Geo proximity (graded, not binary) ---
	if a.HasCoords && b.HasCoords {
		d := haversineKm(a.Latitude, a.Longitude, b.Latitude, b.Longitude)
		switch {
		case d < distSameSpot:
			s.GeoProximity = 100
		case d < distSameUrban:
			s.GeoProximity = 85
		case d < distSameMetro:
			s.GeoProximity = 65
		case d < distSameRegion:
			s.GeoProximity = 40
		case d < distSameCountry:
			s.GeoProximity = 15
		default:
			s.GeoProximity = 0
		}
	} else {
		// Missing coordinates are neutral for this dimension.
		s.GeoProximity = 50
	}

	// --- Name match ---
	s.NameMatch = compareCityNames(a.City, b.City, a.CityRu, b.CityRu)

	// --- Postal ---
	if a.Postal != "" && b.Postal != "" {
		na := strnorm.NormalizePostal(a.Country, a.Postal)
		nb := strnorm.NormalizePostal(b.Country, b.Postal)
		switch {
		case na == nb:
			s.PostalMatch = 100
		case len(na) >= 3 && len(nb) >= 3 && na[:3] == nb[:3]:
			// Same prefix → same region (e.g. Russian indices share first 3 digits)
			s.PostalMatch = 60
		default:
			s.PostalMatch = 0
		}
	}

	// --- State ---
	if a.State != "" && b.State != "" {
		s.StateMatch = compareStates(a.State, b.State)
	}

	// --- Weighted average over applicable dimensions ---
	var sum, weight float64
	sum += float64(s.GeoProximity) * ScoreWeights.Geo
	weight += ScoreWeights.Geo
	if s.NameMatch >= 0 && (a.City != "" || a.CityRu != "") && (b.City != "" || b.CityRu != "") {
		sum += float64(s.NameMatch) * ScoreWeights.Name
		weight += ScoreWeights.Name
	}
	if s.PostalMatch >= 0 {
		sum += float64(s.PostalMatch) * ScoreWeights.Postal
		weight += ScoreWeights.Postal
	}
	if s.StateMatch >= 0 {
		sum += float64(s.StateMatch) * ScoreWeights.State
		weight += ScoreWeights.State
	}
	if weight > 0 {
		s.Total = int(math.Round(sum / weight))
	}

	switch {
	case s.Total >= 80:
		s.Agreement = AgreementHigh
	case s.Total >= 60:
		s.Agreement = AgreementGood
	case s.Total >= 40:
		s.Agreement = AgreementModerate
	case s.Total >= 20:
		s.Agreement = AgreementWeak
	default:
		s.Agreement = AgreementConflict
	}
	return s
}

// enrichWith merges secondary candidate `c` into `result` according to the
// agreement level. Stricter agreement permits more fields to be contributed.
func enrichWith(result MergedRecord, c Candidate, s MatchScore) MergedRecord {
	contributed := false

	switch s.Agreement {
	case AgreementHigh, AgreementGood:
		// Free donation of any empty field
		if result.City == "" && c.City != "" {
			result.City = c.City
			contributed = true
		}
		if result.State == "" && c.State != "" {
			result.State = c.State
			contributed = true
		}
		if result.Postal == "" && c.Postal != "" {
			result.Postal = c.Postal
			contributed = true
		}
		if !result.HasCoords() && c.HasCoords {
			result.Latitude = c.Latitude
			result.Longitude = c.Longitude
			contributed = true
		}

	case AgreementModerate:
		// Only safe administrative fields, postal needs state confirmation
		if result.State == "" && c.State != "" {
			result.State = c.State
			contributed = true
		}
		if result.Postal == "" && c.Postal != "" && s.StateMatch > 50 {
			result.Postal = c.Postal
			contributed = true
		}

	case AgreementWeak, AgreementConflict:
		// Don't trust this source's data, even for empty fields
	}

	if contributed {
		result.Sources = append(result.Sources, c.Source)
	}
	return result
}

// lowConfidenceFill is for sources OUTSIDE the consensus cluster — they get
// to fill only truly empty fields. Fields agreed by the cluster are preserved.
func lowConfidenceFill(result MergedRecord, c Candidate) MergedRecord {
	contributed := false
	// Postal: only if missing and trust is reasonable
	if result.Postal == "" && c.Postal != "" && c.Trust >= 50 {
		result.Postal = c.Postal
		contributed = true
	}
	// State: same idea
	if result.State == "" && c.State != "" && c.Trust >= 50 {
		result.State = c.State
		contributed = true
	}
	if contributed {
		result.Sources = append(result.Sources, c.Source+"?") // marked with ?
	}
	return result
}

// computeConfidence translates the merge process into a 0..100 score that
// downstream consumers can use to filter records.
func computeConfidence(r MergedRecord, cluster []Candidate, totalValid int) int {
	score := 0

	// Core agreement: how many sources backed this result?
	switch len(cluster) {
	case 0, 1:
		score += 25
	case 2:
		score += 50
	default:
		score += 65
	}

	// All sources agreed (no outliers)?
	if len(cluster) == totalValid && totalValid >= 2 {
		score += 5
	}

	// Field completeness
	if r.City != "" {
		score += 10
	}
	if r.State != "" {
		score += 5
	}
	if r.Postal != "" {
		score += 10
	}
	if r.HasCoords() {
		score += 5
	}

	// Coordinate snapping bonus — implies at least one external source agreed
	// with the city name (snapping requires a GeoNames hit).
	if r.Snapped {
		score += 5
	}

	if score > 100 {
		score = 100
	}
	return score
}

// haversineKm — great-circle distance in km. Fast Go implementation.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	rad := func(d float64) float64 { return d * math.Pi / 180.0 }
	dLat := rad(lat2 - lat1)
	dLon := rad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}

func inSlice(s []Candidate, c Candidate) bool {
	for _, x := range s {
		if x.Source == c.Source {
			return true
		}
	}
	return false
}
