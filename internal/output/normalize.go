// Package output applies post-merge cleanup to a finalized record before it
// is written to the MMDB / CSV output. It handles three classes of issues:
//
//  1. Coordinate noise — sources may publish lat/lon with up to 6 decimal
//     places (sub-meter precision), which is more precise than IP-block
//     geolocation can support. Rounding to 5 decimals (~1.1 m at the
//     equator), which is more than enough for "which city".
//
//  2. Verbose region names — different sources use different transliteration
//     conventions for non-Latin admin areas. "Horad Minsk", "Almaty Qalasy",
//     "Seoul-teukbyeolsi", "Minskaya voblasts'" — all are inconsistent
//     romanizations of the local language. The cleanup strips local-language
//     "city/region" markers so the state field carries just the place name.
//
//  3. State==City duplicates — when the donor reports the same string for
//     both fields (Tokyo/Tokyo, Moscow/Moscow, Almaty/Almaty), one of them
//     is redundant. The state is dropped in that case so the downstream
//     consumer doesn't render "Tokyo, Tokyo, Japan".
//
// City names with bracketed neighborhoods ("San Francisco (South Beach)")
// are also cleaned here — the parenthetical is dropped both for output and
// for the subsequent ZIP lookup, where the neighborhood form would never
// hit GeoNames.
package output

import (
	"math"
	"regexp"
	"strings"
)

// CoordPrecision is the number of decimal places to keep on lat/lon.
// 5 decimals == ~1.1 m at the equator. Sub-meter precision is unnecessary for
// a CIDR block and tends to create false precision in downstream consumers.
const CoordPrecision = 5

// RoundCoord rounds to CoordPrecision decimal places.
func RoundCoord(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	pow := math.Pow10(CoordPrecision)
	return math.Round(v*pow) / pow
}

// parenSuffixRe matches a trailing parenthetical neighborhood, e.g.
// " (South Beach)" or "(downtown)". Used to clean city names.
var parenSuffixRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)

// softSignMarks are apostrophe-like runes commonly used by geo donors to
// romanize Cyrillic soft/hard signs, e.g. Kazan' / Kazan'.
const softSignMarks = "'`’ʼʹ′´"

// CleanCity strips a trailing parenthetical and soft-sign apostrophe from a
// city name.
// "San Francisco (South Beach)" → "San Francisco"; "Kazan’" → "Kazan"
// Apply this BEFORE feeding the city to the ZIP resolver — GeoNames
// indexes cities by their canonical name, never with bracketed suffixes.
func CleanCity(city string) string {
	s := RepairMojibake(city)
	s = strings.TrimSpace(parenSuffixRe.ReplaceAllString(s, ""))
	s = cleanCityAdminMarkers(s)
	return strings.TrimSpace(strings.TrimRight(s, softSignMarks))
}

// stateAdminPrefixes are local-language "city of" / "town of" prefixes that
// some sources prepend to admin names. Strip them so the state shows just
// the place name.
//
// Belarusian "Horad" and Russian-style "g." are both city/admin markers.
var stateAdminPrefixes = []string{
	"Horad ", "horad ",
	"g. ", "G. ", "gor. ", "Gor. ",
	"Oblast ", "oblast ",
	"Viloyat ", "viloyat ",
	"Wilaya ", "wilaya ",
	"Muhafazat ", "muhafazat ",
	"Provincia de ", "provincia de ",
	"Province de ", "province de ",
	"Departamento de ", "departamento de ",
	"Estado de ", "estado de ",
}

// stateAdminSuffixes are local-language "city" / "region" suffixes appended
// after the place name in some romanizations. Stripping them yields the
// canonical place name.
//
//	-teukbyeolsi   = «특별시» = "Special City" (Seoul)
//	-gwangyeoksi   = «광역시» = "Metropolitan City"  (Busan, Daegu, Incheon, ...)
//	-teukbyeolja-chi = «특별자치시» = "Special Self-Governing City"  (Sejong)
//	-teukbyeolja-do  = «특별자치도» = "Special Self-Governing Province"  (Jeju)
//	-do            = «도»     = "Province" (Korean)
//	 Qalasy        = "City"     (Kazakh romanization)
//	 Oblysy        = "Region"   (Kazakh romanization)
//	-shi           = «市»     = "City"     (Japanese)
//	-fu / -to / -ken — Japanese prefecture suffixes — leave alone, they're
//	  semantically meaningful (Tokyo-to ≠ Tokyo city, etc.)
var stateAdminSuffixes = []string{
	"-teukbyeolja-chi", "-teukbyeolja-do",
	"-teukbyeolsi", "-gwangyeoksi",
	" Qalasy", " qalasy", " Oblysy", " oblysy",
	" Viloyati", " viloyati",
	" Wilaayati", " wilaayati",
	" Muhafazah", " muhafazah",
	" Governorate", " governorate",
	" Province", " province",
	" Provincia", " provincia",
	" Departamento", " departamento",
	" Department", " department",
	" Estado", " estado",
	" Region", " region",
}

// voblastsRe matches the Belarusian "voblasts" suffix (with or without trailing
// apostrophe / transliterated soft-sign marker) and replaces it with the
// canonical "Voblast".
var voblastsRe = regexp.MustCompile(`(?i)\s*voblasts['` + "`" + `’ʼʹ′´]?\s*$`)

// oblastApostropheRe normalizes "Oblast'" / "oblast'" → "Oblast" (drops
// the soft-sign apostrophe used in some transliterations).
var oblastApostropheRe = regexp.MustCompile(`(?i)\boblast['` + "`" + `’ʼʹ′´]\s*$`)

// CleanState normalises an admin region name.
//
// Steps:
//  1. trim trailing apostrophes (soft-sign artefacts)
//  2. strip known local-language prefixes (Horad, g., gor.)
//  3. strip known local-language suffixes (-teukbyeolsi, Qalasy, ...)
//  4. canonicalise voblasts → Voblast, oblast' → Oblast
//  5. trim whitespace
func CleanState(state string) string {
	if state == "" {
		return ""
	}
	s := RepairMojibake(state)

	// strip a known prefix
	for _, p := range stateAdminPrefixes {
		if strings.HasPrefix(s, p) {
			s = strings.TrimSpace(s[len(p):])
			break
		}
	}

	// strip a known suffix
	for _, sfx := range stateAdminSuffixes {
		if strings.HasSuffix(s, sfx) {
			s = strings.TrimSpace(s[:len(s)-len(sfx)])
			break
		}
	}

	// canonicalise Belarusian voblasts → Voblast (English-uniform)
	s = voblastsRe.ReplaceAllString(s, " Voblast")
	// canonicalise oblast' → Oblast (no trailing apostrophe)
	s = oblastApostropheRe.ReplaceAllString(s, "Oblast")

	// strip any remaining trailing soft-sign apostrophe (commonly used to
	// romanize a soft sign), e.g. city-like admin names such as Tver'.
	s = strings.TrimRight(s, softSignMarks)

	return strings.TrimSpace(s)
}

var cityAdminPrefixes = []string{
	"Thanh pho ", "thanh pho ",
	"TP. ", "tp. ",
	"Ciudad de ", "ciudad de ",
	"City of ", "city of ",
	"Al Madinah ", "al madinah ",
	"Al Baladiyah ", "al baladiyah ",
	"Baladiyat ", "baladiyat ",
}

var cityAdminSuffixes = []string{
	"-shi", "-si", "-gun", "-gu", "-ku",
	" Shi", " shi",
	" City", " city",
	" District", " district",
	" Municipality", " municipality",
}

func cleanCityAdminMarkers(city string) string {
	s := city
	for _, p := range cityAdminPrefixes {
		if strings.HasPrefix(s, p) {
			s = strings.TrimSpace(s[len(p):])
			break
		}
	}
	for _, sfx := range cityAdminSuffixes {
		if strings.HasSuffix(s, sfx) && len(s) > len(sfx)+2 {
			s = strings.TrimSpace(s[:len(s)-len(sfx)])
			break
		}
	}
	return s
}

var mojibakeReplacements = []struct {
	bad  string
	good string
}{
	{"â€™", "'"},
	{"â€˜", "'"},
	{"â€œ", "\""},
	{"â€", "\""},
	{"â€“", "-"},
	{"â€”", "-"},
	{"Ã¡", "á"},
	{"Ã ", "à"},
	{"Ã¢", "â"},
	{"Ã£", "ã"},
	{"Ã¤", "ä"},
	{"Ã¥", "å"},
	{"Ã¦", "æ"},
	{"Ã§", "ç"},
	{"Ã©", "é"},
	{"Ã¨", "è"},
	{"Ãª", "ê"},
	{"Ã«", "ë"},
	{"Ã­", "í"},
	{"Ã¬", "ì"},
	{"Ã®", "î"},
	{"Ã¯", "ï"},
	{"Ã±", "ñ"},
	{"Ã³", "ó"},
	{"Ã²", "ò"},
	{"Ã´", "ô"},
	{"Ãµ", "õ"},
	{"Ã¶", "ö"},
	{"Ã¸", "ø"},
	{"Ãº", "ú"},
	{"Ã¹", "ù"},
	{"Ã»", "û"},
	{"Ã¼", "ü"},
	{"Ã½", "ý"},
	{"Ã¿", "ÿ"},
	{"Ã", "Á"},
	{"Ã€", "À"},
	{"Ã‚", "Â"},
	{"Ãƒ", "Ã"},
	{"Ã„", "Ä"},
	{"Ã…", "Å"},
	{"Ã‡", "Ç"},
	{"Ã‰", "É"},
	{"Ãˆ", "È"},
	{"ÃŠ", "Ê"},
	{"Ã‹", "Ë"},
	{"Ã", "Í"},
	{"ÃŒ", "Ì"},
	{"ÃŽ", "Î"},
	{"Ã", "Ï"},
	{"Ã‘", "Ñ"},
	{"Ã“", "Ó"},
	{"Ã’", "Ò"},
	{"Ã”", "Ô"},
	{"Ã•", "Õ"},
	{"Ã–", "Ö"},
	{"Ãš", "Ú"},
	{"Ã™", "Ù"},
	{"Ã›", "Û"},
	{"Ãœ", "Ü"},
}

func RepairMojibake(s string) string {
	if s == "" {
		return ""
	}
	out := s
	for _, repl := range mojibakeReplacements {
		out = strings.ReplaceAll(out, repl.bad, repl.good)
	}
	return out
}

func HasTextArtifact(s string) bool {
	if s == "" {
		return false
	}
	lower := strings.ToLower(s)
	return strings.Contains(s, "Ã") ||
		strings.Contains(s, "â€") ||
		strings.Contains(s, "Â") ||
		strings.Contains(s, "Ð") ||
		strings.Contains(s, "Ñ") ||
		strings.Contains(s, "�") ||
		strings.Contains(s, "’") ||
		strings.Contains(s, "ʼ") ||
		strings.Contains(lower, "oblast'") ||
		strings.Contains(lower, "voblasts'")
}

// DedupeStateCity returns (state, city) with the redundant duplicate
// removed. If the cleaned state equals the cleaned city (case-insensitive),
// the state is dropped — the city already carries that information and a
// downstream renderer would otherwise produce "Tokyo, Tokyo, Japan".
//
// The comparison uses cleaned forms so "Almaty Qalasy" / "Almaty" deduplicates
// correctly, and the returned state preserves the
// original cleaning.
func DedupeStateCity(state, city string) (string, string) {
	cleanState := CleanState(state)
	cleanCity := CleanCity(city)

	if cleanState != "" && cleanCity != "" &&
		strings.EqualFold(cleanState, cleanCity) {
		return "", cleanCity
	}
	return cleanState, cleanCity
}
