// Package main is the GeoForge builder entry point.
//
// Pipeline per IP block:
//  1. Read DB-IP CIDR from CSV (the seed — defines what blocks exist)
//  2. Pull candidate records from MaxMind, IP2Location, and Sypex (CIS only)
//  3. Run them through consensus.MergeCandidates — pre-validate, cluster, merge
//  4. Align coordinates with GeoNames when a credible public place match exists
//  5. Bidirectional ZIP enrichment (city↔postal)
//  6. Apply output normalisation (round coords, clean state/city, dedup)
//  7. Compute timezone from final coordinates
//  8. Write to MMDB / CSV
package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ip2location/ip2location-go/v9"
	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
	"github.com/night-codes/go-sypexgeo"
	"github.com/oschwald/maxminddb-golang"
	"github.com/ringsaturn/tzf"
	"go4.org/netipx"

	"geoip-builder/internal/consensus"
	"geoip-builder/internal/geofeed"
	"geoip-builder/internal/geozip"
	"geoip-builder/internal/output"
	"geoip-builder/internal/refdata"
	"geoip-builder/internal/rirstats"
)

const (
	OutputMMDB = true
	OutputFile = "geo" // produces geo.mmdb / geo.csv
	DataDir    = "data"
	ReleaseDir = "release"

	// MinConfidenceToWrite — records below this are dropped silently.
	// 0 writes every merged record. Confidence remains available for run stats
	// and the output schema.
	MinConfidenceToWrite = 0
)

// ANSI colour helpers — small enough to keep inline rather than in a package.
const (
	CR  = "\x1b[0m"
	CG  = "\x1b[32m"
	CY  = "\x1b[33m"
	CB  = "\x1b[34m"
	CC  = "\x1b[36m"
	CGY = "\x1b[90m"
	CRD = "\x1b[31m"
	BD  = "\x1b[1m"
)

// maxmindRecord contains only the GeoLite2 fields consumed by the builder.
type maxmindRecord struct {
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Country struct {
		IsoCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		Names   map[string]string `maxminddb:"names"`
		IsoCode string            `maxminddb:"iso_code"`
	} `maxminddb:"subdivisions"`
	Postal struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"postal"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
	Continent struct {
		Code string `maxminddb:"code"`
	} `maxminddb:"continent"`
}

var (
	tzFinder      tzf.F
	locationCache = make(map[string]*time.Location)
	locationMu    sync.RWMutex
)

func init() {
	var err error
	tzFinder, err = tzf.NewDefaultFinder()
	if err != nil {
		log.Fatal("tzf init failed:", err)
	}
}

func dataPath(filename string) string {
	return filepath.Join(DataDir, filename)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

func p(icon, color, msg string) {
	fmt.Printf("%s%s %s%s\n", color, icon, CR, msg)
}

// stats — global counters surfaced at the end of the run.
var stats struct {
	highConf, modConf, lowConf, snapped, zipFromGN, cityFromZip int
}

func main() {
	startTimer := time.Now()

	fmt.Printf("\n%s%s+--------------------------------------------------------------+%s\n", BD, CB, CR)
	fmt.Printf("%s%s|                         GeoForge                             |%s\n", BD, CB, CR)
	fmt.Printf("%s%s|        Consensus GeoIP Compiler | Offline | Multi-Source     |%s\n", BD, CB, CR)
	fmt.Printf("%s%s+--------------------------------------------------------------+%s\n\n", BD, CB, CR)

	geozip.SetDataDir(DataDir)

	if err := geozip.LoadGeoNamesZIP(); err != nil {
		p("!", CY, fmt.Sprintf("GeoNames ZIP: %v", err))
		p("i", CGY, "Snapping & ZIP repair will be unavailable")
	}
	if err := geozip.LoadGeoNamesGazetteer(); err != nil {
		p("!", CY, fmt.Sprintf("GeoNames gazetteer: %v", err))
		p("i", CGY, "city_geoname_id will be unavailable")
	}

	// --- Load all source databases -----------------------------------------

	var sx sypexgeo.SxGEO
	var useSypex bool
	if fileExists(dataPath("SxGeoCity.dat")) {
		sx = sypexgeo.New(dataPath("SxGeoCity.dat"))
		useSypex = true
		p("+", CG, "Sypex Geo loaded (priority for CIS)")
	} else {
		p("!", CY, "SxGeoCity.dat not found -- Sypex disabled")
	}

	var mmDB *maxminddb.Reader
	if fileExists(dataPath("GeoLite2-City.mmdb")) {
		var err error
		mmDB, err = maxminddb.Open(dataPath("GeoLite2-City.mmdb"))
		if err != nil {
			p("x", CRD, fmt.Sprintf("GeoLite2-City: %v", err))
		} else {
			defer mmDB.Close()
			p("+", CG, "MaxMind GeoLite2 loaded")
		}
	}

	var ip2locDB *ip2location.DB
	if fileExists(dataPath("IP2LOCATION-LITE-DB5.BIN")) {
		var err error
		ip2locDB, err = ip2location.OpenDB(dataPath("IP2LOCATION-LITE-DB5.BIN"))
		if err != nil {
			p("x", CRD, fmt.Sprintf("IP2Location: %v", err))
		} else {
			defer ip2locDB.Close()
			p("+", CG, "IP2Location loaded")
		}
	}

	rirIdx, err := rirstats.LoadDir(dataPath("rir"))
	if err != nil {
		p("!", CY, fmt.Sprintf("RIR delegated stats: %v", err))
	}
	if rirIdx.Loaded() {
		p("+", CG, "RIR delegated stats loaded")
	} else {
		p("!", CY, "RIR delegated stats not found -- registry_country_code falls back to seed country")
	}

	geofeedIdx, err := geofeed.LoadDir(dataPath("geofeeds"))
	if err != nil {
		p("!", CY, fmt.Sprintf("Geofeeds: %v", err))
	}
	if geofeedIdx.Loaded() {
		p("+", CG, "Allowlisted geofeeds loaded")
	}

	dbipPath := dataPath("dbip-city-lite.csv")
	if !fileExists(dbipPath) {
		p("x", CRD, "dbip-city-lite.csv not found -- required base file!")
		os.Exit(1)
	}
	csvFile, err := os.Open(dbipPath)
	if err != nil {
		log.Fatal(err)
	}
	defer csvFile.Close()
	reader := csv.NewReader(csvFile)
	p("+", CG, "DB-IP CSV opened (seed)")

	// --- Output setup ------------------------------------------------------

	if err := os.MkdirAll(ReleaseDir, 0755); err != nil {
		log.Fatal("Cannot create release dir:", err)
	}
	var tree *mmdbwriter.Tree
	var csvOut *csv.Writer
	var csvOutFile *os.File

	fmt.Println()
	if OutputMMDB {
		tree, _ = mmdbwriter.New(mmdbwriter.Options{
			DatabaseType: "GeoForge-World",
			RecordSize:   32,
		})
		p(">>", CC, "Output: MMDB")
	}
	csvOutFile, _ = os.Create(filepath.Join(ReleaseDir, OutputFile+".csv"))
	defer csvOutFile.Close()
	csvOut = csv.NewWriter(csvOutFile)
	csvOut.Write([]string{"start_ip", "end_ip", "matched_prefix", "continent_code", "continent_name",
		"country_code", "country_name", "registry_country_code", "subdivision_name",
		"city_geoname_id", "city_name", "postal_code", "latitude", "longitude",
		"time_zone", "accuracy_radius_km", "confidence", "source_updated_at"})
	if !OutputMMDB {
		p(">>", CC, "Output: CSV")
	}

	fmt.Printf("\n%s%s>> Processing networks...%s\n\n", BD, CB, CR)

	linesProcessed, recordsSaved := 0, 0
	lastUpdate := time.Now()

	for {
		record, err := reader.Read()
		if err != nil {
			break
		}
		if strings.Contains(record[0], ":") {
			continue
		}

		startIP, err1 := netip.ParseAddr(record[0])
		endIP, err2 := netip.ParseAddr(record[1])
		if err1 != nil || err2 != nil {
			continue
		}

		// Build the DB-IP candidate (always available, this is our seed)
		dbipCand := consensus.Candidate{
			Source:  "dbip",
			Country: record[3],
			City:    record[5],
		}
		if len(record) > 4 {
			dbipCand.State = record[4]
		}
		dbipLat := toFloat(record[6])
		dbipLon := toFloat(record[7])
		if dbipLat != 0 || dbipLon != 0 {
			dbipCand.Latitude = dbipLat
			dbipCand.Longitude = dbipLon
			dbipCand.HasCoords = true
		}
		country := record[3]

		ipRange := netipx.IPRangeFrom(startIP, endIP)
		for _, prefix := range ipRange.Prefixes() {

			// CIS countries with Sypex available -> split to /24 for granularity.
			// Other countries process the prefix as a single block.
			if refdata.CISCountries[country] && useSypex {
				for _, sub := range splitTo24(prefix) {
					processBlock(sub, country, dbipCand, mmDB, ip2locDB, sx, useSypex, rirIdx, geofeedIdx, tree, csvOut)
					recordsSaved++
				}
			} else {
				processBlock(prefix, country, dbipCand, mmDB, ip2locDB, sx, useSypex, rirIdx, geofeedIdx, tree, csvOut)
				recordsSaved++
			}
		}

		linesProcessed++
		if time.Since(lastUpdate) > 2*time.Second {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			memUsage := float64(m.Alloc) / 1024 / 1024 / 1024
			fmt.Printf("\r%s  >> Blocks: %s%d%s | Records: %s%d%s | Mem: %s%.2fGB%s | Elapsed: %s%v%s",
				CC, BD, linesProcessed, CR,
				BD, recordsSaved, CR,
				CY, memUsage, CR,
				CGY, time.Since(startTimer).Round(time.Second), CR)
			lastUpdate = time.Now()
		}
	}

	fmt.Printf("\n\n")
	geozip.ClearZipIndex()
	p("**", CB, "Saving output file...")

	if OutputMMDB {
		outPath := filepath.Join(ReleaseDir, OutputFile+".mmdb")
		tmpPath := outPath + ".tmp"
		outFile, err := os.Create(tmpPath)
		if err != nil {
			log.Fatalf("create %s: %v", tmpPath, err)
		}
		if _, err := tree.WriteTo(outFile); err != nil {
			outFile.Close()
			os.Remove(tmpPath)
			log.Fatalf("write MMDB %s: %v", tmpPath, err)
		}
		if err := outFile.Sync(); err != nil {
			outFile.Close()
			os.Remove(tmpPath)
			log.Fatalf("sync MMDB %s: %v", tmpPath, err)
		}
		if err := outFile.Close(); err != nil {
			os.Remove(tmpPath)
			log.Fatalf("close MMDB %s: %v", tmpPath, err)
		}
		if err := os.Rename(tmpPath, outPath); err != nil {
			os.Remove(tmpPath)
			log.Fatalf("rename %s -> %s: %v", tmpPath, outPath, err)
		}
		info, err := os.Stat(outPath)
		if err != nil {
			log.Fatalf("stat %s: %v", outPath, err)
		}
		size := float64(info.Size()) / 1024 / 1024
		p("+", CG, fmt.Sprintf("MMDB saved: %s (%.1f MB)", outPath, size))
	}
	if csvOut != nil {
		csvOut.Flush()
		if err := csvOut.Error(); err != nil {
			log.Fatalf("write CSV: %v", err)
		}
		p("+", CG, fmt.Sprintf("CSV saved: %s", filepath.Join(ReleaseDir, OutputFile+".csv")))
	}

	elapsed := time.Since(startTimer).Round(time.Second)
	fmt.Printf("\n%s%s+--------------------------------------------------------------+%s\n", BD, CG, CR)
	fmt.Printf("%s%s|  DONE!                                                       |%s\n", BD, CG, CR)
	fmt.Printf("%s%s|     DB-IP blocks:        %-34d  |%s\n", BD, CG, linesProcessed, CR)
	fmt.Printf("%s%s|     Records saved:       %-34d  |%s\n", BD, CG, recordsSaved, CR)
	fmt.Printf("%s%s|     Confidence >= 75:    %-34d  |%s\n", BD, CG, stats.highConf, CR)
	fmt.Printf("%s%s|     Confidence 50-74:    %-34d  |%s\n", BD, CG, stats.modConf, CR)
	fmt.Printf("%s%s|     Confidence < 50:     %-34d  |%s\n", BD, CG, stats.lowConf, CR)
	fmt.Printf("%s%s|     Coords snapped:      %-34d  |%s\n", BD, CG, stats.snapped, CR)
	fmt.Printf("%s%s|     ZIPs from GeoNames:  %-34d  |%s\n", BD, CG, stats.zipFromGN, CR)
	fmt.Printf("%s%s|     City from ZIP:       %-34d  |%s\n", BD, CG, stats.cityFromZip, CR)
	fmt.Printf("%s%s|     Time:                %-34v  |%s\n", BD, CG, elapsed, CR)
	fmt.Printf("%s%s+--------------------------------------------------------------+%s\n\n", BD, CG, CR)
}

// processBlock builds candidates for one IP prefix, runs the consensus engine,
// and writes the result.
func processBlock(prefix netip.Prefix, country string, dbipCand consensus.Candidate,
	mmDB *maxminddb.Reader, ip2locDB *ip2location.DB,
	sx sypexgeo.SxGEO, useSypex bool,
	rirIdx *rirstats.Index, geofeedIdx *geofeed.Index,
	tree *mmdbwriter.Tree, csvOut *csv.Writer) {

	ipStr := prefix.Addr().String()
	candidates := []consensus.Candidate{dbipCand}

	if geofeedIdx != nil {
		if gf := geofeedIdx.Lookup(prefix); gf != nil {
			candidates = append(candidates, consensus.Candidate{
				Source:  "geofeed",
				Country: gf.Country,
				State:   gf.State,
				City:    gf.City,
				Postal:  gf.Postal,
				Trust:   100,
			})
		}
	}

	// MaxMind
	if mmDB != nil {
		var rec maxmindRecord
		if err := mmDB.Lookup(net.ParseIP(ipStr), &rec); err == nil {
			c := consensus.Candidate{
				Source:  "maxmind",
				Country: rec.Country.IsoCode,
				City:    rec.City.Names["en"],
				Postal:  rec.Postal.Code,
			}
			if len(rec.Subdivisions) > 0 {
				c.State = rec.Subdivisions[0].Names["en"]
			}
			if rec.Location.Latitude != 0 || rec.Location.Longitude != 0 {
				c.Latitude = rec.Location.Latitude
				c.Longitude = rec.Location.Longitude
				c.HasCoords = true
			}
			if c.Country == "" {
				c.Country = country
			}
			candidates = append(candidates, c)
		}
	}

	// IP2Location
	if ip2locDB != nil {
		if res, err := ip2locDB.Get_all(ipStr); err == nil {
			c := consensus.Candidate{
				Source:  "ip2loc",
				Country: strings.ToUpper(strings.TrimSpace(res.Country_short)),
				City:    res.City,
				State:   res.Region,
				Postal:  res.Zipcode,
			}
			if c.Country == "" || c.Country == "-" {
				c.Country = country
			}
			if res.Latitude != 0 || res.Longitude != 0 {
				c.Latitude = float64(res.Latitude)
				c.Longitude = float64(res.Longitude)
				c.HasCoords = true
			}
			candidates = append(candidates, c)
		}
	}

	// Sypex (CIS only)
	if useSypex && refdata.CISCountries[country] {
		if info, err := sx.GetCityFull(ipStr); err == nil {
			c := consensus.Candidate{Source: "sypex", Country: country}
			// Some Sypex builds expose country.iso — use it when present
			if cMap, ok := info["country"].(map[string]interface{}); ok {
				if iso, ok := cMap["iso"].(string); ok && iso != "" {
					c.Country = strings.ToUpper(iso)
				}
			}
			if cityMap, ok := info["city"].(map[string]interface{}); ok {
				if name, ok := cityMap["name_en"].(string); ok {
					c.City = name
				}
				if name, ok := cityMap["name_ru"].(string); ok {
					c.CityRu = name
				}
				if lat, ok := cityMap["lat"].(float64); ok && lat != 0 {
					c.Latitude = lat
					c.HasCoords = true
				}
				if lon, ok := cityMap["lon"].(float64); ok && lon != 0 {
					c.Longitude = lon
					c.HasCoords = true
				}
			}
			if regMap, ok := info["region"].(map[string]interface{}); ok {
				if name, ok := regMap["name_en"].(string); ok {
					c.State = name
				}
			}
			if c.City != "" || c.CityRu != "" || c.HasCoords {
				candidates = append(candidates, c)
			}
		}
	}

	// === Run consensus ===
	merged := consensus.MergeCandidates(candidates, country)
	merged.RegistryCountry = country
	if rirIdx != nil {
		if cc := rirIdx.Country(prefix.Addr()); cc != "" {
			merged.RegistryCountry = cc
		}
	}

	// === Coordinate normalization against the public GeoNames reference ===
	// Pass the source city before output cleanup. geozip strips parenthetical
	// labels internally before lookup.
	if merged.City != "" && geozip.IsZipIndexLoaded() {
		if lat, lon, ok := geozip.SnapCoordsToGeoNames(country, merged.City, merged.State,
			merged.Latitude, merged.Longitude); ok {
			merged.Latitude = lat
			merged.Longitude = lon
			merged.Snapped = true
			stats.snapped++
		}
	}

	// === ZIP enrichment from GeoNames ===
	if merged.Postal == "" && merged.City != "" && geozip.IsZipIndexLoaded() {
		if z := geozip.FindZipByCity(country, merged.State, merged.City); z != "" {
			merged.Postal = z
			merged.Sources = append(merged.Sources, "geonames")
			stats.zipFromGN++
		}
	}

	// === Reverse: city from postal (when MaxMind only gave us the country + ZIP) ===
	if merged.City == "" && merged.Postal != "" && geozip.IsZipIndexLoaded() {
		if entry := geozip.FindCityByPostal(country, merged.Postal); entry != nil {
			merged.City = entry.City
			if merged.State == "" {
				merged.State = entry.State
			}
			if !merged.HasCoords() {
				merged.Latitude = entry.Latitude
				merged.Longitude = entry.Longitude
				merged.Snapped = true
			}
			merged.Sources = append(merged.Sources, "geonames-rev")
			stats.cityFromZip++
		}
	}

	// === Confidence stats bucket (kept for the run summary, not written) ===
	switch {
	case merged.Confidence >= 75:
		stats.highConf++
	case merged.Confidence >= 50:
		stats.modConf++
	default:
		stats.lowConf++
	}

	// === Write ===
	if merged.Confidence >= MinConfidenceToWrite {
		saveRecord(tree, csvOut, prefix, merged)
	}
}

// saveRecord serialises a MergedRecord to MMDB or CSV, applying output-side
// normalisation (coordinate rounding, state/city cleanup, dedup) right before
// the bytes hit disk.
func saveRecord(tree *mmdbwriter.Tree, csvOut *csv.Writer, prefix netip.Prefix, m consensus.MergedRecord) {
	iso := m.Country
	countryName := refdata.CountryNameMap[iso]
	if countryName == "" {
		countryName = iso
	}
	continentCode := refdata.ContinentMap[iso]
	if continentCode == "" {
		continentCode = "Unknown"
	}
	continent := refdata.ContinentNameMap[continentCode]
	if continent == "" {
		continent = "Unknown"
	}
	// --- OUTPUT NORMALISATION ----------------------------------------------
	// Apply right before the write so all downstream invariants are
	// satisfied at once, regardless of which source ended up as primary.

	// Round lat/lon to 5 decimals (~1.1m) — meaningful precision for IP
	// blocks without preserving unstable sub-meter source noise.
	lat := output.RoundCoord(m.Latitude)
	lon := output.RoundCoord(m.Longitude)

	// Clean state suffixes/prefixes (Horad, Qalasy, -teukbyeolsi, voblasts'),
	// drop the parenthetical neighbourhood from city, and dedup the
	// state==city case (Tokyo/Tokyo, Almaty Qalasy/Almaty, ...).
	stateClean, cityClean := output.DedupeStateCity(m.State, m.City)

	postal := m.Postal
	cityGeonameID := geozip.FindCityGeonameID(iso, cityClean)
	accuracyRadiusKm := accuracyRadius(m, prefix)
	sourceUpdatedAt := time.Now().UTC().Format(time.RFC3339)
	registryCountry := m.RegistryCountry
	if registryCountry == "" {
		registryCountry = iso
	}

	// Time zone is derived from the final coordinates selected by the merge step.
	tzName := tzFinder.GetTimezoneName(lon, lat)
	if tzName == "" {
		tzName = "UTC"
	}

	if OutputMMDB {
		_, ipNet, err := net.ParseCIDR(prefix.String())
		if err != nil {
			return
		}
		location := mmdbtype.Map{
			"latitude":              mmdbtype.Float64(lat),
			"longitude":             mmdbtype.Float64(lon),
			"time_zone":             mmdbtype.String(tzName),
			"accuracy_radius_km":    mmdbtype.Uint16(uint16(accuracyRadiusKm)),
			"continent_name":        mmdbtype.String(continent),
			"continent_code":        mmdbtype.String(continentCode),
			"country_name":          mmdbtype.String(countryName),
			"country_code":          mmdbtype.String(iso),
			"registry_country_code": mmdbtype.String(registryCountry),
			"subdivision_name":      mmdbtype.String(stateClean),
			"city_name":             mmdbtype.String(cityClean),
			"postal_code":           mmdbtype.String(postal),
		}
		if cityGeonameID != 0 {
			location["city_geoname_id"] = mmdbtype.Uint32(cityGeonameID)
		}
		rec := mmdbtype.Map{
			"matched_prefix":    mmdbtype.String(prefix.String()),
			"confidence":        mmdbtype.Uint16(uint16(m.Confidence)),
			"source_updated_at": mmdbtype.String(sourceUpdatedAt),
			"country_metadata": mmdbtype.Map{
				mmdbtype.String(iso): mmdbtype.Map{
					"country_name":  mmdbtype.String(countryName),
					"calling_code":  mmdbtype.String(refdata.CallingCodeMap[iso]),
					"currency_code": mmdbtype.String(refdata.CurrencyMap[iso]),
					"is_eu_member":  mmdbtype.Bool(refdata.EUMemberMap[iso]),
				},
			},
			"location": mmdbtype.Map{},
		}
		rec["location"] = location
		_ = tree.Insert(ipNet, rec)
	}
	if csvOut != nil {
		r := netipx.RangeOfPrefix(prefix)
		csvOut.Write([]string{
			r.From().String(), r.To().String(), prefix.String(), continentCode, continent,
			iso, countryName, registryCountry, stateClean,
			formatUint32(cityGeonameID), cityClean, postal,
			strconv.FormatFloat(lat, 'f', output.CoordPrecision, 64),
			strconv.FormatFloat(lon, 'f', output.CoordPrecision, 64),
			tzName, strconv.Itoa(accuracyRadiusKm), strconv.Itoa(m.Confidence),
			sourceUpdatedAt,
		})
	}
}

func formatUint32(v uint32) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatUint(uint64(v), 10)
}

func accuracyRadius(m consensus.MergedRecord, prefix netip.Prefix) int {
	switch {
	case m.Snapped:
		return 20
	case m.Confidence >= 85 && prefix.Bits() >= 24:
		return 25
	case m.Confidence >= 75:
		return 50
	case m.Confidence >= 50:
		return 100
	default:
		return 250
	}
}

// splitTo24 returns the /24 sub-prefixes of a larger v4 prefix (or returns
// the prefix unchanged if it's already /24+ or v6). Used when Sypex is
// available and granularity is desirable for CIS countries.
func splitTo24(p netip.Prefix) []netip.Prefix {
	if p.Addr().Is6() || p.Bits() >= 24 {
		return []netip.Prefix{p}
	}
	count := 1 << (24 - p.Bits())
	startBytes := p.Addr().As4()
	startUint := uint32(startBytes[0])<<24 | uint32(startBytes[1])<<16 |
		uint32(startBytes[2])<<8 | uint32(startBytes[3])
	out := make([]netip.Prefix, 0, count)
	for i := 0; i < count; i++ {
		ipUint := startUint + uint32(i<<8)
		ipBytes := [4]byte{
			byte(ipUint >> 24), byte(ipUint >> 16),
			byte(ipUint >> 8), byte(ipUint),
		}
		out = append(out, netip.PrefixFrom(netip.AddrFrom4(ipBytes), 24))
	}
	return out
}

func toFloat(s string) float64 {
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
