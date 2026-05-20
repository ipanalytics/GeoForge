package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"geoip-builder/internal/output"

	"github.com/oschwald/maxminddb-golang"
)

type row struct {
	StartIP      string
	Matched      string
	Country      string
	Registry     string
	State        string
	GeonameID    string
	City         string
	Postal       string
	Lat          string
	Lon          string
	Timezone     string
	Accuracy     string
	Confidence   int
	UpdatedAt    string
	columnByName map[string]string
}

type stats struct {
	Rows          int
	Country       int
	City          int
	Postal        int
	Geoname       int
	RegistryDiff  int
	LowConfidence int
	BadArtifacts  int
	ConfidenceSum int
	ByCountry     map[string]int
}

type comparison struct {
	Common              int
	Added               int
	Removed             int
	CountryChanged      int
	CityChanged         int
	CountryRegressions  int
	CityRegressions     int
	PostalRegressions   int
	GeonameRegressions  int
	ConfidenceDrop10    int
	ConfidenceImprove10 int
}

func main() {
	currentPath := flag.String("current", "release/geo.csv", "current release CSV")
	previousPath := flag.String("previous", "release/geo.previous.csv", "previous release CSV")
	mmdbPath := flag.String("mmdb", "release/geo.mmdb", "current release MMDB")
	reportPath := flag.String("report", "release/geo-quality-report.txt", "report path")
	strict := flag.Bool("strict", false, "exit non-zero on quality regressions")
	flag.Parse()

	current, err := loadCSV(*currentPath)
	if err != nil {
		log.Fatalf("load current csv: %v", err)
	}
	curStats := summarize(current)

	var prev map[string]row
	if fileExists(*previousPath) {
		prev, err = loadCSV(*previousPath)
		if err != nil {
			log.Fatalf("load previous csv: %v", err)
		}
	}

	var cmp comparison
	if len(prev) > 0 {
		cmp = compare(prev, current)
	}

	mmdbOK, mmdbMsg := smokeMMDB(*mmdbPath, current)
	report := renderReport(*currentPath, *previousPath, *mmdbPath, curStats, summarize(prev), cmp, mmdbOK, mmdbMsg)
	if err := os.WriteFile(*reportPath, []byte(report), 0644); err != nil {
		log.Fatalf("write report: %v", err)
	}
	fmt.Print(report)

	if !mmdbOK {
		os.Exit(1)
	}
	if *strict && hasRegression(curStats, summarize(prev), cmp) {
		os.Exit(2)
	}
}

func loadCSV(path string) (map[string]row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[h] = i
	}

	rows := map[string]row{}
	for {
		rec, err := r.Read()
		if err != nil {
			if err == io.EOF {
				return rows, nil
			}
			return nil, err
		}
		get := func(name string) string {
			i, ok := idx[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		conf, _ := strconv.Atoi(get("confidence"))
		prefix := get("matched_prefix")
		if prefix == "" {
			continue
		}
		rows[prefix] = row{
			StartIP:      get("start_ip"),
			Matched:      prefix,
			Country:      get("country_code"),
			Registry:     get("registry_country_code"),
			State:        get("subdivision_name"),
			GeonameID:    get("city_geoname_id"),
			City:         get("city_name"),
			Postal:       get("postal_code"),
			Lat:          get("latitude"),
			Lon:          get("longitude"),
			Timezone:     get("time_zone"),
			Accuracy:     get("accuracy_radius_km"),
			Confidence:   conf,
			UpdatedAt:    get("source_updated_at"),
			columnByName: nil,
		}
	}
}

func summarize(rows map[string]row) stats {
	s := stats{ByCountry: map[string]int{}}
	for _, r := range rows {
		s.Rows++
		if r.Country != "" {
			s.Country++
			s.ByCountry[r.Country]++
		}
		if r.City != "" {
			s.City++
		}
		if r.Postal != "" {
			s.Postal++
		}
		if r.GeonameID != "" {
			s.Geoname++
		}
		if r.Registry != "" && r.Country != "" && r.Registry != r.Country {
			s.RegistryDiff++
		}
		if r.Confidence < 70 {
			s.LowConfidence++
		}
		if hasBadArtifact(r.City) || hasBadArtifact(r.State) {
			s.BadArtifacts++
		}
		s.ConfidenceSum += r.Confidence
	}
	return s
}

func compare(prev, cur map[string]row) comparison {
	var c comparison
	for pfx, n := range cur {
		o, ok := prev[pfx]
		if !ok {
			c.Added++
			continue
		}
		c.Common++
		if o.Country != n.Country {
			c.CountryChanged++
		}
		if o.City != n.City {
			c.CityChanged++
		}
		if o.Country != "" && n.Country == "" {
			c.CountryRegressions++
		}
		if o.City != "" && n.City == "" {
			c.CityRegressions++
		}
		if o.Postal != "" && n.Postal == "" {
			c.PostalRegressions++
		}
		if o.GeonameID != "" && n.GeonameID == "" {
			c.GeonameRegressions++
		}
		if o.Confidence-n.Confidence >= 10 {
			c.ConfidenceDrop10++
		}
		if n.Confidence-o.Confidence >= 10 {
			c.ConfidenceImprove10++
		}
	}
	for pfx := range prev {
		if _, ok := cur[pfx]; !ok {
			c.Removed++
		}
	}
	return c
}

func smokeMMDB(path string, rows map[string]row) (bool, string) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return false, err.Error()
	}
	defer db.Close()

	checked := 0
	prefixes := make([]string, 0, len(rows))
	for pfx := range rows {
		prefixes = append(prefixes, pfx)
	}
	sort.Strings(prefixes)
	for _, pfx := range prefixes {
		r := rows[pfx]
		ip := net.ParseIP(r.StartIP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		var out map[string]any
		if err := db.Lookup(ip, &out); err != nil {
			return false, fmt.Sprintf("lookup %s: %v", ip, err)
		}
		if len(out) == 0 {
			return false, fmt.Sprintf("lookup %s returned empty record", ip)
		}
		loc, ok := out["location"].(map[string]any)
		if !ok || len(loc) == 0 {
			return false, fmt.Sprintf("lookup %s has no location map", ip)
		}
		checked++
		if checked >= 25 {
			return true, fmt.Sprintf("MMDB smoke lookup OK (%d sample IPs)", checked)
		}
	}
	return checked > 0, fmt.Sprintf("MMDB smoke lookup OK (%d sample IPs)", checked)
}

func renderReport(curPath, prevPath, mmdbPath string, cur, prev stats, cmp comparison, mmdbOK bool, mmdbMsg string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Geo quality report\n")
	fmt.Fprintf(&b, "Current CSV: %s\n", curPath)
	fmt.Fprintf(&b, "Previous CSV: %s\n", prevPath)
	fmt.Fprintf(&b, "MMDB: %s\n\n", mmdbPath)

	fmt.Fprintf(&b, "Current coverage\n")
	fmt.Fprintf(&b, "- records: %d\n", cur.Rows)
	fmt.Fprintf(&b, "- country filled: %d (%.2f%%)\n", cur.Country, pct(cur.Country, cur.Rows))
	fmt.Fprintf(&b, "- city filled: %d (%.2f%%)\n", cur.City, pct(cur.City, cur.Rows))
	fmt.Fprintf(&b, "- postal filled: %d (%.2f%%)\n", cur.Postal, pct(cur.Postal, cur.Rows))
	fmt.Fprintf(&b, "- city_geoname_id filled: %d (%.2f%%)\n", cur.Geoname, pct(cur.Geoname, cur.Rows))
	fmt.Fprintf(&b, "- registry != geo country: %d (%.2f%%)\n", cur.RegistryDiff, pct(cur.RegistryDiff, cur.Rows))
	fmt.Fprintf(&b, "- low confidence <70: %d (%.2f%%)\n", cur.LowConfidence, pct(cur.LowConfidence, cur.Rows))
	fmt.Fprintf(&b, "- suspicious text artifacts: %d\n", cur.BadArtifacts)
	fmt.Fprintf(&b, "- average confidence: %.2f\n\n", avgConfidence(cur))

	if prev.Rows == 0 {
		fmt.Fprintf(&b, "Comparison\n")
		fmt.Fprintf(&b, "- previous release CSV not found; this run becomes the first baseline\n\n")
	} else {
		fmt.Fprintf(&b, "Comparison with previous release\n")
		fmt.Fprintf(&b, "- common prefixes: %d\n", cmp.Common)
		fmt.Fprintf(&b, "- added prefixes: %d\n", cmp.Added)
		fmt.Fprintf(&b, "- removed prefixes: %d\n", cmp.Removed)
		fmt.Fprintf(&b, "- country changes: %d\n", cmp.CountryChanged)
		fmt.Fprintf(&b, "- city changes: %d\n", cmp.CityChanged)
		fmt.Fprintf(&b, "- country filled -> empty: %d\n", cmp.CountryRegressions)
		fmt.Fprintf(&b, "- city filled -> empty: %d\n", cmp.CityRegressions)
		fmt.Fprintf(&b, "- postal filled -> empty: %d\n", cmp.PostalRegressions)
		fmt.Fprintf(&b, "- geoname filled -> empty: %d\n", cmp.GeonameRegressions)
		fmt.Fprintf(&b, "- confidence drops >=10: %d\n", cmp.ConfidenceDrop10)
		fmt.Fprintf(&b, "- confidence improvements >=10: %d\n\n", cmp.ConfidenceImprove10)
	}

	status := "OK"
	if !mmdbOK {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "MMDB smoke test\n")
	fmt.Fprintf(&b, "- %s: %s\n\n", status, mmdbMsg)

	fmt.Fprintf(&b, "Interpretation\n")
	fmt.Fprintf(&b, "- better: more city/postal/geoname coverage, fewer low-confidence records, fewer empty regressions\n")
	fmt.Fprintf(&b, "- suspicious: many country/city changes at once, added / removed prefixes far above source update size, artifacts > 0\n")
	fmt.Fprintf(&b, "- expected: some city changes when geofeeds or donor monthly releases update\n")
	return b.String()
}

func hasRegression(cur, prev stats, cmp comparison) bool {
	if prev.Rows == 0 {
		return cur.BadArtifacts > 0
	}
	if cur.BadArtifacts > 0 {
		return true
	}
	if cur.Rows < int(float64(prev.Rows)*0.95) {
		return true
	}
	if cur.City < int(float64(prev.City)*0.95) {
		return true
	}
	if cur.Geoname < int(float64(prev.Geoname)*0.90) {
		return true
	}
	if cmp.CountryRegressions > maxInt(100, cmp.Common/1000) {
		return true
	}
	if cmp.CityRegressions > maxInt(1000, cmp.Common/100) {
		return true
	}
	if cmp.ConfidenceDrop10 > maxInt(1000, cmp.Common/100) {
		return true
	}
	return false
}

func hasBadArtifact(s string) bool {
	return output.HasTextArtifact(output.RepairMojibake(s))
}

func avgConfidence(s stats) float64 {
	if s.Rows == 0 {
		return 0
	}
	return float64(s.ConfidenceSum) / float64(s.Rows)
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) * 100 / float64(d)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir() && st.Size() > 0
}
