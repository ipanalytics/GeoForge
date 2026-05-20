// Package strnorm holds string-normalisation primitives shared by the
// consensus matcher and the GeoNames ZIP resolver: lower-casing, diacritic
// stripping, the Cyrillic → Latin canonical-city dictionary, and a basic
// GOST-like transliteration map.
//
// These are pure utilities — no I/O, no globals beyond the constant
// dictionaries below. The dictionaries are declared `var` (not `const`)
// because Go lacks const maps; treat them as read-only.
package strnorm

import (
	"strings"
	"unicode"
)

func NormalizeName(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ToLower(strings.TrimSpace(s))
	s = stripDiacritics(s)
	s = strings.ReplaceAll(s, "ё", "е")
	// Saint variants
	for _, prefix := range []string{"saint-", "saint ", "st. ", "st-", "st "} {
		if strings.HasPrefix(s, prefix) {
			s = "st " + s[len(prefix):]
			break
		}
	}
	// Strip punctuation, keep only letters, digits, spaces
	var b strings.Builder
	prevSpace := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevSpace = false
		case unicode.IsSpace(r) || r == '-' || r == '_':
			if !prevSpace && b.Len() > 0 {
				b.WriteRune(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// stripDiacritics: München → Munchen, Łódź → Łódź (only handles common Latin marks).
func stripDiacritics(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case 'ä':
			b.WriteRune('a')
		case 'ö':
			b.WriteRune('o')
		case 'ü':
			b.WriteRune('u')
		case 'ß':
			b.WriteString("ss")
		case 'á', 'à', 'â', 'ã':
			b.WriteRune('a')
		case 'é', 'è', 'ê', 'ë':
			b.WriteRune('e')
		case 'í', 'ì', 'î', 'ï':
			b.WriteRune('i')
		case 'ó', 'ò', 'ô', 'õ':
			b.WriteRune('o')
		case 'ú', 'ù', 'û':
			b.WriteRune('u')
		case 'ñ':
			b.WriteRune('n')
		case 'ç':
			b.WriteRune('c')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func ContainsCyrillic(s string) bool {
	for _, r := range s {
		if r >= 0x0400 && r <= 0x04FF {
			return true
		}
	}
	return false
}

// CyrToKnownLatin holds canonical mappings for cities whose English names
// don't follow simple transliteration rules (Moscow ≠ Moskva, etc.).
// Keys AND values are post-normalize forms (note: "saint X" → "st X" per
// the prefix-rewrite rule in NormalizeName).
var CyrToKnownLatin = map[string]string{
	"москва":                   "moscow",
	"санкт петербург":          "st petersburg",
	"петербург":                "st petersburg",
	"нижний новгород":          "nizhny novgorod",
	"екатеринбург":             "yekaterinburg",
	"казань":                   "kazan",
	"новосибирск":              "novosibirsk",
	"самара":                   "samara",
	"уфа":                      "ufa",
	"красноярск":               "krasnoyarsk",
	"воронеж":                  "voronezh",
	"волгоград":                "volgograd",
	"пермь":                    "perm",
	"саратов":                  "saratov",
	"ростов на дону":           "rostov on don",
	"краснодар":                "krasnodar",
	"тольятти":                 "tolyatti",
	"ижевск":                   "izhevsk",
	"ульяновск":                "ulyanovsk",
	"ярославль":                "yaroslavl",
	"барнаул":                  "barnaul",
	"владивосток":              "vladivostok",
	"иркутск":                  "irkutsk",
	"хабаровск":                "khabarovsk",
	"оренбург":                 "orenburg",
	"новокузнецк":              "novokuznetsk",
	"рязань":                   "ryazan",
	"тюмень":                   "tyumen",
	"набережные челны":         "naberezhnye chelny",
	"томск":                    "tomsk",
	"кемерово":                 "kemerovo",
	"астрахань":                "astrakhan",
	"пенза":                    "penza",
	"липецк":                   "lipetsk",
	"тула":                     "tula",
	"киров":                    "kirov",
	"чебоксары":                "cheboksary",
	"калининград":              "kaliningrad",
	"брянск":                   "bryansk",
	"курск":                    "kursk",
	"иваново":                  "ivanovo",
	"магнитогорск":             "magnitogorsk",
	"тверь":                    "tver",
	"ставрополь":               "stavropol",
	"белгород":                 "belgorod",
	"архангельск":              "arkhangelsk",
	"владимир":                 "vladimir",
	"сочи":                     "sochi",
	"курган":                   "kurgan",
	"смоленск":                 "smolensk",
	"калуга":                   "kaluga",
	"чита":                     "chita",
	"орел":                     "oryol",
	"волжский":                 "volzhsky",
	"череповец":                "cherepovets",
	"вологда":                  "vologda",
	"мурманск":                 "murmansk",
	"сургут":                   "surgut",
	"тамбов":                   "tambov",
	"стерлитамак":              "sterlitamak",
	"грозный":                  "grozny",
	"якутск":                   "yakutsk",
	"кострома":                 "kostroma",
	"комсомольск на амуре":     "komsomolsk on amur",
	"петрозаводск":             "petrozavodsk",
	"таганрог":                 "taganrog",
	"нижневартовск":            "nizhnevartovsk",
	"йошкар ола":               "yoshkar ola",
	"братск":                   "bratsk",
	"новороссийск":             "novorossiysk",
	"дзержинск":                "dzerzhinsk",
	"шахты":                    "shakhty",
	"нальчик":                  "nalchik",
	"орск":                     "orsk",
	"сыктывкар":                "syktyvkar",
	"нижнекамск":               "nizhnekamsk",
	"ангарск":                  "angarsk",
	"балашиха":                 "balashikha",
	"благовещенск":             "blagoveshchensk",
	"прокопьевск":              "prokopyevsk",
	"химки":                    "khimki",
	"псков":                    "pskov",
	"бийск":                    "biysk",
	"энгельс":                  "engels",
	"рыбинск":                  "rybinsk",
	"подольск":                 "podolsk",
	"южно сахалинск":           "yuzhno sakhalinsk",
	"армавир":                  "armavir",
	"северодвинск":             "severodvinsk",
	"петропавловск камчатский": "petropavlovsk kamchatsky",
	"сызрань":                  "syzran",
	"новочеркасск":             "novocherkassk",
	"каменск уральский":        "kamensk uralsky",
	"златоуст":                 "zlatoust",
	"электросталь":             "elektrostal",
	"альметьевск":              "almetyevsk",
	"салават":                  "salavat",
	"миасс":                    "miass",
	"находка":                  "nakhodka",
	"копейск":                  "kopeysk",
	"пятигорск":                "pyatigorsk",
	"рубцовск":                 "rubtsovsk",
	"березники":                "berezniki",
	"коломна":                  "kolomna",
	"мытищи":                   "mytishchi",
	"майкоп":                   "maykop",
	"одинцово":                 "odintsovo",
	"ковров":                   "kovrov",
	"киселевск":                "kiselyovsk",
	"нефтеюганск":              "nefteyugansk",
	"хасавюрт":                 "khasavyurt",
	"новый уренгой":            "novy urengoy",
	"первоуральск":             "pervouralsk",
	"черкесск":                 "cherkessk",
	"серпухов":                 "serpukhov",
	"димитровград":             "dimitrovgrad",
	"новочебоксарск":           "novocheboksarsk",
	"щелково":                  "shchyolkovo",
	"камышин":                  "kamyshin",
	"нижний тагил":             "nizhny tagil",
	// Other CIS capitals
	"минск":     "minsk",
	"киев":      "kyiv",
	"харьков":   "kharkiv",
	"одесса":    "odesa",
	"днепр":     "dnipro",
	"львов":     "lviv",
	"астана":    "astana",
	"алматы":    "almaty",
	"шымкент":   "shymkent",
	"бишкек":    "bishkek",
	"ташкент":   "tashkent",
	"ереван":    "yerevan",
	"баку":      "baku",
	"кишинев":   "chisinau",
	"тбилиси":   "tbilisi",
	"душанбе":   "dushanbe",
	"ашхабад":   "ashgabat",
	"нур султан": "nur sultan",
}

// TransliterateRuToEn does a best-effort GOST-like transliteration. Used as
// a fallback when a city isn't in CyrToKnownLatin. Imperfect but catches
// the long tail of small towns.
var ruToEn = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	// Ukrainian / Belarusian extras
	'і': "i", 'ї': "yi", 'є': "ye", 'ґ': "g", 'ў': "w",
}

func TransliterateRuToEn(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' {
			b.WriteRune(' ')
			continue
		}
		if v, ok := ruToEn[r]; ok {
			b.WriteString(v)
		} else if v, ok := ruToEn[unicode.ToLower(r)]; ok {
			b.WriteString(v)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizePostal returns a canonical form of a postal code suitable for
// equality comparison and index lookups: upper-case, no spaces, no dashes.
//
// Examples:
//   NormalizePostal("CA", "K1A 0B1")  → "K1A0B1"
//   NormalizePostal("US", "94103-1234") → "941031234"
//   NormalizePostal("GB", "sw1a 1aa")  → "SW1A1AA"
//
// The `country` argument is reserved for future country-specific rules
// (e.g. Brazilian CEP "12345-678" semantics differ from arbitrary dashes).
// For now, all countries get the same conservative treatment.
func NormalizePostal(country, postal string) string {
	_ = country // reserved for future country-specific rules
	p := strings.ToUpper(strings.TrimSpace(postal))
	p = strings.ReplaceAll(p, " ", "")
	p = strings.ReplaceAll(p, "-", "")
	return p
}
