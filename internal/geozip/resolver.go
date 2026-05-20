package geozip

import (
	"archive/zip"
	"bufio"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"geoip-builder/internal/output"
	"geoip-builder/internal/strnorm"
)

// The GeoNames postal codes file is loaded into TWO indexes that together
// power three operations:
//
//   1) byCity[country][normalizedCity] -> []*GeoEntry
//        -> "I have a city, find me a ZIP" + coordinate snapping
//   2) byPostal[country][postal] -> *GeoEntry
//        -> "I have a ZIP, find me a city" (reverse lookup)
//
// Storing coordinates in the index is what enables coordinate normalization:
// when a city match is available, final coordinates can be aligned with a
// public GeoNames place record instead of a provider-specific coordinate.

const (
	GeoNamesURL       = "http://download.geonames.org/export/zip/allCountries.zip"
	GeoNamesZip       = "allCountries.zip"
	GeoNamesTxt       = "allCountries.txt"
	GeoNamesCitiesURL = "https://download.geonames.org/export/dump/cities1000.zip"
	GeoNamesCitiesZip = "cities1000.zip"
	GeoNamesCitiesTxt = "cities1000.txt"
	ChunkSize         = 1024 * 1024
)

// ANSI colour helpers used by the progress UI. Kept package-local so the
// resolver does not depend on main; names mirror cmd/builder/main.go for
// progress output consistency.
const (
	CR  = "\x1b[0m"
	CG  = "\x1b[32m"
	CY  = "\x1b[33m"
	CB  = "\x1b[34m"
	CC  = "\x1b[36m"
	CGY = "\x1b[90m"
	BD  = "\x1b[1m"
)

// DataDir is where this package looks for and unpacks the GeoNames archive.
// The default "data" is relative to the working directory of the running
// binary — when the binary is launched from the project root (as `geo.sh`
// does) this resolves to ./data/. Override via SetDataDir if you want to
// stash the GeoNames data elsewhere; the input geo databases (DB-IP,
// MaxMind, etc.) are read from the same path by main.go.
var DataDir = "data"

// SetDataDir lets the caller (main.go) override where geozip looks for the
// allCountries.zip and unpacks it. Must be called before LoadGeoNamesZIP.
func SetDataDir(p string) { DataDir = p }

// Package-local filesystem helpers. Kept here (rather than importing from
// main) so the package is self-contained — geozip is `internal/` so the
// only consumer is main, but a private copy avoids a circular-import
// hazard and lets tests under internal/geozip run in isolation.

func dataPath(filename string) string {
	return filepath.Join(DataDir, filename)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// haversineKm returns the great-circle distance between two lat/lon points
// in kilometres. A package-local copy: consensus has the same function for
// its proximity scoring, but cross-package sharing of a 12-line hot-loop
// math function loses the inlining win and gains nothing. Keep two copies.
func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0 // Earth radius, km
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := rad(lat2 - lat1)
	dLon := rad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * R * math.Asin(math.Sqrt(a))
}

// GeoEntry — one row from the GeoNames postal-code dump, augmented with
// normalised city name for fuzzy matching.
type GeoEntry struct {
	Postal    string
	City      string // original case
	CityNorm  string // normalised for matching
	State     string
	StateCode string
	Latitude  float64
	Longitude float64
}

var zipIndex struct {
	sync.RWMutex
	byCity   map[string]map[string][]*GeoEntry // country -> normCity -> entries
	byPostal map[string]map[string]*GeoEntry   // country -> postal -> first entry seen
	loaded   bool
}

type CityIDEntry struct {
	GeonameID  uint32
	Population int64
}

var cityIDIndex struct {
	sync.RWMutex
	byCity map[string]map[string]CityIDEntry // country -> normCity -> best geonameid
	loaded bool
}

func init() {
	zipIndex.byCity = make(map[string]map[string][]*GeoEntry)
	zipIndex.byPostal = make(map[string]map[string]*GeoEntry)
	cityIDIndex.byCity = make(map[string]map[string]CityIDEntry)
}

// ----- Public lookup API ---------------------------------------------------

// FindZipByCity returns the best ZIP for (country, state, city). state can be
// empty. Uses normalised matching for dashes, spaces, and transliteration.
func FindZipByCity(country, state, city string) string {
	if country == "" || city == "" {
		return ""
	}
	entries := lookupCityEntries(country, city)
	if len(entries) == 0 {
		return ""
	}
	// If state given, prefer entries from the same state
	if state != "" {
		stateNorm := strnorm.NormalizeName(state)
		for _, e := range entries {
			if strnorm.NormalizeName(e.State) == stateNorm {
				return e.Postal
			}
		}
	}
	return entries[0].Postal
}

// FindCityByPostal does the reverse — given a known ZIP, return the city/state.
// This is used when a source provides a country and postal code but no city.
func FindCityByPostal(country, postal string) *GeoEntry {
	if country == "" || postal == "" {
		return nil
	}
	zipIndex.RLock()
	defer zipIndex.RUnlock()
	cm, ok := zipIndex.byPostal[country]
	if !ok {
		return nil
	}
	if e, ok := cm[postal]; ok {
		return e
	}
	// Try without spaces/dashes (Canada, UK)
	clean := strnorm.NormalizePostal(country, postal)
	if e, ok := cm[clean]; ok {
		return e
	}
	return nil
}

func FindCityGeonameID(country, city string) uint32 {
	if country == "" || city == "" {
		return 0
	}
	cityIDIndex.RLock()
	defer cityIDIndex.RUnlock()
	cm, ok := cityIDIndex.byCity[strings.ToUpper(country)]
	if !ok {
		return 0
	}
	if e, ok := cm[strnorm.NormalizeName(output.CleanCity(city))]; ok {
		return e.GeonameID
	}
	return 0
}

// SnapCoordsToGeoNames returns GeoNames coordinates for a city, but only if
// they're plausibly close to the input coords (within 100 km). This keeps the
// final coordinates aligned with a public place reference when the match is
// credible.
//
// If inLat/inLon are zero, we return GeoNames coords unconditionally.
// Returns (lat, lon, ok).
func SnapCoordsToGeoNames(country, city, state string, inLat, inLon float64) (float64, float64, bool) {
	if country == "" || city == "" {
		return 0, 0, false
	}
	entries := lookupCityEntries(country, city)
	if len(entries) == 0 {
		return 0, 0, false
	}
	// Choose entry: state-match if possible, else first
	chosen := entries[0]
	if state != "" {
		stateNorm := strnorm.NormalizeName(state)
		for _, e := range entries {
			if strnorm.NormalizeName(e.State) == stateNorm {
				chosen = e
				break
			}
		}
	}
	// Sanity gate: if input coords exist, GeoNames coords must be reasonably
	// close. Otherwise we'd snap "Moscow, Idaho" to "Moscow, Russia".
	if inLat != 0 || inLon != 0 {
		d := haversineKm(inLat, inLon, chosen.Latitude, chosen.Longitude)
		if d > 100.0 {
			return 0, 0, false
		}
	}
	return chosen.Latitude, chosen.Longitude, true
}

// lookupCityEntries normalises the city name and returns matching entries,
// trying transliteration as a fallback.
func lookupCityEntries(country, city string) []*GeoEntry {
	zipIndex.RLock()
	defer zipIndex.RUnlock()
	cm, ok := zipIndex.byCity[country]
	if !ok {
		return nil
	}
	// Strip a parenthetical neighbourhood ("San Francisco (South Beach)" →
	// "San Francisco") before normalising. GeoNames indexes cities by their
	// canonical name; the suffix would never match.
	city = output.CleanCity(city)
	norm := strnorm.NormalizeName(city)
	if entries, ok := cm[norm]; ok && len(entries) > 0 {
		return entries
	}
	// If input was Cyrillic, try transliteration
	if strnorm.ContainsCyrillic(norm) {
		translit := strnorm.TransliterateRuToEn(norm)
		if entries, ok := cm[translit]; ok && len(entries) > 0 {
			return entries
		}
	}
	// If input was Latin, try mapping to known Cyrillic
	if !strnorm.ContainsCyrillic(norm) {
		// Reverse-lookup the strnorm.CyrToKnownLatin map (rare but cheap)
		for cyr, lat := range strnorm.CyrToKnownLatin {
			if lat == norm {
				if entries, ok := cm[cyr]; ok && len(entries) > 0 {
					return entries
				}
			}
		}
	}
	return nil
}

// ----- Loader / indexer -----------------------------------------------------

func ensureDataDir() string {
	if err := os.MkdirAll(DataDir, 0755); err != nil {
		panic(fmt.Sprintf("Cannot create dir %s: %v", DataDir, err))
	}
	return DataDir
}

func fileSize(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "unknown"
	}
	size := float64(info.Size())
	units := []string{"B", "KB", "MB", "GB"}
	idx := 0
	for size >= 1024 && idx < len(units)-1 {
		size /= 1024
		idx++
	}
	return fmt.Sprintf("%.1f %s", size, units[idx])
}

func LoadGeoNamesZIP() error {
	ensureDataDir()
	zipPath := dataPath(GeoNamesZip)
	txtPath := dataPath(GeoNamesTxt)

	fmt.Printf("\n%s%s>> GeoNames index (postal + coords for snapping)%s\n", BD, CB, CR)

	if !fileExists(txtPath) {
		if !fileExists(zipPath) {
			fmt.Printf("%s>> Downloading %s%s\n", CY, GeoNamesURL, CR)
			if err := download(GeoNamesURL, zipPath); err != nil {
				return fmt.Errorf("download: %w", err)
			}
		}
		if err := unzipFile(zipPath, DataDir); err != nil {
			return fmt.Errorf("unzip: %w", err)
		}
	}

	return indexFile(txtPath)
}

func LoadGeoNamesGazetteer() error {
	ensureDataDir()
	zipPath := dataPath(GeoNamesCitiesZip)
	txtPath := dataPath(GeoNamesCitiesTxt)

	fmt.Printf("\n%s%s>> GeoNames gazetteer index (city geonameid)%s\n", BD, CB, CR)

	if !fileExists(txtPath) {
		if !fileExists(zipPath) {
			fmt.Printf("%s>> Downloading %s%s\n", CY, GeoNamesCitiesURL, CR)
			if err := download(GeoNamesCitiesURL, zipPath); err != nil {
				return fmt.Errorf("download: %w", err)
			}
		}
		if err := unzipFileTo(zipPath, DataDir, GeoNamesCitiesTxt); err != nil {
			return fmt.Errorf("unzip: %w", err)
		}
	}

	return indexGazetteerFile(txtPath)
}

type ProgressBar struct {
	Total, Current int64
	Width          int
	Prefix         string
	StartTime      time.Time
}

func NewProgressBar(total int64, prefix string) *ProgressBar {
	return &ProgressBar{Total: total, Width: 40, Prefix: prefix, StartTime: time.Now()}
}

func (pb *ProgressBar) Update(n int64) {
	pb.Current += n
	if pb.Total <= 0 {
		return
	}
	if pb.Current > pb.Total {
		pb.Current = pb.Total
	}
	percent := float64(pb.Current) / float64(pb.Total) * 100
	filled := int(percent / 100 * float64(pb.Width))
	if filled > pb.Width {
		filled = pb.Width
	}
	bar := strings.Repeat("=", filled) + strings.Repeat("-", pb.Width-filled)
	elapsed := time.Since(pb.StartTime).Seconds()
	speed := float64(pb.Current) / elapsed / 1024 / 1024
	fmt.Printf("\r%s%s%s [%s] %.1f%%  %.1f MB/s%s",
		CC, pb.Prefix, CR, bar, percent, speed, CGY)
}

func (pb *ProgressBar) Finish() { fmt.Printf("\n%s", CR) }

func download(url, destPath string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()
	pb := NewProgressBar(resp.ContentLength, "   Download")
	buf := make([]byte, ChunkSize)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			pb.Update(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	pb.Finish()
	fmt.Printf("%s+ Saved:%s %s (%s)\n", CG, CR, destPath, fileSize(destPath))
	return nil
}

func unzipFile(zipPath, destDir string) error {
	return unzipFileTo(zipPath, destDir, GeoNamesTxt)
}

func unzipFileTo(zipPath, destDir, txtName string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	var target *zip.File
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, ".txt") {
			target = f
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no .txt in archive")
	}
	pb := NewProgressBar(int64(target.UncompressedSize64), "   Extract")
	rc, err := target.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(filepath.Join(destDir, txtName))
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, ChunkSize)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
			pb.Update(int64(n))
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	pb.Finish()
	os.Remove(zipPath)
	return nil
}

// indexFile parses the tab-separated GeoNames file and builds both indexes.
// Format (12 cols): country, postal, place, admin1, admin1code, admin2,
// admin2code, admin3, admin3code, latitude, longitude, accuracy.
func indexFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, _ := file.Stat()
	pb := NewProgressBar(info.Size(), "   Index")

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	count, cities, postals := 0, 0, 0
	bytesRead := int64(0)
	lastUpdate := time.Now()

	zipIndex.Lock()
	defer zipIndex.Unlock()

	for scanner.Scan() {
		line := scanner.Text()
		bytesRead += int64(len(line) + 1)
		parts := strings.Split(line, "\t")
		if len(parts) < 12 {
			continue
		}
		country := strings.ToUpper(parts[0])
		postal := strings.TrimSpace(parts[1])
		place := strings.TrimSpace(parts[2])
		admin1 := strings.TrimSpace(parts[3])
		admin1Code := strings.TrimSpace(parts[4])
		if postal == "" || place == "" {
			continue
		}
		lat, _ := strconv.ParseFloat(parts[9], 64)
		lon, _ := strconv.ParseFloat(parts[10], 64)

		entry := &GeoEntry{
			Postal:    postal,
			City:      place,
			CityNorm:  strnorm.NormalizeName(place),
			State:     admin1,
			StateCode: admin1Code,
			Latitude:  lat,
			Longitude: lon,
		}

		// Ensure country buckets exist
		if zipIndex.byCity[country] == nil {
			zipIndex.byCity[country] = make(map[string][]*GeoEntry)
			zipIndex.byPostal[country] = make(map[string]*GeoEntry)
		}

		// byCity
		if _, exists := zipIndex.byCity[country][entry.CityNorm]; !exists {
			cities++
		}
		zipIndex.byCity[country][entry.CityNorm] = append(zipIndex.byCity[country][entry.CityNorm], entry)

		// Parent-city alias.
		//
		// GeoNames Canada (and some KR/AU) data stores neighbourhood-level
		// place names like "Pointe-aux-Trembles (Montréal-Est)" rather than
		// the canonical city. Without aliasing, a search for "Montreal"
		// returns nothing for CA, even though every Montreal FSA is in the
		// index. The bracketed portion is added as an alias.
		//
		// Examples:
		//   "Pointe-aux-Trembles (Montréal-Est)" → alias "montreal est"
		//   "Saint-Louis (Montréal Central-Sud)" → alias "montreal central sud"
		// The first word of the bracketed name is also indexed because it is
		// often the parent city:
		//   "(Montréal-Est)" → first word "Montréal" → alias "montreal"
		//
		// Done only when the place name contains '(' to avoid over-eager
		// aliasing for cities that legitimately share a word.
		if openParen := strings.IndexByte(place, '('); openParen >= 0 {
			if closeParen := strings.IndexByte(place[openParen:], ')'); closeParen > 0 {
				inside := strings.TrimSpace(place[openParen+1 : openParen+closeParen])
				if inside != "" {
					// Full bracketed name (e.g. "montreal est")
					alias := strnorm.NormalizeName(inside)
					if alias != "" && alias != entry.CityNorm {
						zipIndex.byCity[country][alias] = append(zipIndex.byCity[country][alias], entry)
					}
					// First word of bracketed name (e.g. "montreal"). Use
					// the first space- or hyphen-separated token, since
					// "Montréal-Est" should reduce to "Montréal".
					firstWord := inside
					for _, sep := range []byte{' ', '-'} {
						if i := strings.IndexByte(firstWord, sep); i > 0 {
							firstWord = firstWord[:i]
							break
						}
					}
					firstAlias := strnorm.NormalizeName(firstWord)
					if firstAlias != "" && firstAlias != alias && firstAlias != entry.CityNorm {
						zipIndex.byCity[country][firstAlias] = append(zipIndex.byCity[country][firstAlias], entry)
					}
				}
			}
		}

		// byPostal — first one wins (multiple cities can share a ZIP, we
		// keep the first as the canonical reverse-lookup answer)
		if _, exists := zipIndex.byPostal[country][postal]; !exists {
			zipIndex.byPostal[country][postal] = entry
			postals++
		}
		clean := strnorm.NormalizePostal(country, postal)
		if clean != postal {
			if _, exists := zipIndex.byPostal[country][clean]; !exists {
				zipIndex.byPostal[country][clean] = entry
			}
		}

		count++
		if time.Since(lastUpdate) > 100*time.Millisecond {
			pb.Update(bytesRead - pb.Current)
			lastUpdate = time.Now()
		}
	}
	pb.Update(bytesRead - pb.Current)
	pb.Finish()
	zipIndex.loaded = true
	fmt.Printf("%s+ Indexed:%s %d records, %d unique cities, %d postal codes\n",
		CG, CR, count, cities, postals)
	return scanner.Err()
}

// indexGazetteerFile parses the main GeoNames table. cities1000.txt is used
// instead of allCountries.txt to keep the index small while covering the
// city-level records that IP geolocation normally emits.
//
// Main geoname table columns:
// 0 geonameid, 1 name, 2 asciiname, 3 alternatenames, 4 latitude,
// 5 longitude, 6 feature class, 7 feature code, 8 country code, ...
// 14 population.
func indexGazetteerFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, _ := file.Stat()
	pb := NewProgressBar(info.Size(), "   Index")

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	count, aliases := 0, 0
	bytesRead := int64(0)
	lastUpdate := time.Now()

	cityIDIndex.Lock()
	defer cityIDIndex.Unlock()
	cityIDIndex.byCity = make(map[string]map[string]CityIDEntry)

	for scanner.Scan() {
		line := scanner.Text()
		bytesRead += int64(len(line) + 1)
		parts := strings.Split(line, "\t")
		if len(parts) < 15 {
			continue
		}
		if parts[6] != "P" {
			continue
		}
		id64, err := strconv.ParseUint(parts[0], 10, 32)
		if err != nil || id64 == 0 {
			continue
		}
		country := strings.ToUpper(strings.TrimSpace(parts[8]))
		if country == "" {
			continue
		}
		pop, _ := strconv.ParseInt(parts[14], 10, 64)
		entry := CityIDEntry{GeonameID: uint32(id64), Population: pop}
		names := []string{parts[1], parts[2]}
		if parts[3] != "" {
			names = append(names, strings.Split(parts[3], ",")...)
		}
		for _, name := range names {
			norm := strnorm.NormalizeName(output.CleanCity(name))
			if norm == "" {
				continue
			}
			if cityIDIndex.byCity[country] == nil {
				cityIDIndex.byCity[country] = make(map[string]CityIDEntry)
			}
			if old, ok := cityIDIndex.byCity[country][norm]; !ok || entry.Population > old.Population {
				cityIDIndex.byCity[country][norm] = entry
				aliases++
			}
		}

		count++
		if time.Since(lastUpdate) > 100*time.Millisecond {
			pb.Update(bytesRead - pb.Current)
			lastUpdate = time.Now()
		}
	}
	pb.Update(bytesRead - pb.Current)
	pb.Finish()
	cityIDIndex.loaded = true
	fmt.Printf("%s+ Indexed:%s %d city records, %d city aliases\n", CG, CR, count, aliases)
	return scanner.Err()
}

// ClearZipIndex frees the in-memory indexes once the build is complete.
func ClearZipIndex() {
	zipIndex.Lock()
	defer zipIndex.Unlock()
	zipIndex.byCity = nil
	zipIndex.byPostal = nil
	zipIndex.loaded = false
	runtime.GC()
}

func IsZipIndexLoaded() bool {
	zipIndex.RLock()
	defer zipIndex.RUnlock()
	return zipIndex.loaded
}
