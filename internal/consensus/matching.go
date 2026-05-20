package consensus

import (
	"strings"

	"geoip-builder/internal/strnorm"
)

// Name comparison is multi-layered because three databases will write
// "St. Petersburg", "Saint-Petersburg", and a native-script spelling for the
// same city; a naive string compare misses those matches.
//
// Layers (cheapest first):
//  1) exact normalised match in the same script
//  2) Cyrillic <-> Latin via the known-city dictionary
//  3) transliteration (GOST 7.79 / ISO 9 simplified) for the long tail
//  4) Damerau-Levenshtein on the longer of the two normalised strings
//
// Each layer can short-circuit if it finds a strong match.

// compareCityNames returns 0..100 indicating how similar two city names are.
// It tries every known representation (English, native, Cyrillic) on both
// sides and takes the best.
func compareCityNames(aEn, bEn, aRu, bRu string) int {
	best := 0
	tries := []struct{ a, b string }{
		{aEn, bEn},
		{aRu, bRu},
		{aEn, bRu},
		{aRu, bEn},
	}
	for _, p := range tries {
		if p.a == "" || p.b == "" {
			continue
		}
		s := singlePairScore(p.a, p.b)
		if s > best {
			best = s
		}
	}
	return best
}

func singlePairScore(a, b string) int {
	na := strnorm.NormalizeName(a)
	nb := strnorm.NormalizeName(b)
	if na == "" || nb == "" {
		return 0
	}

	// Exact match after normalisation — handles "Saint Petersburg" vs "St-Petersburg"
	if na == nb {
		return 100
	}

	// Try cross-script: maybe one is Cyrillic and the other Latin
	aIsCyr := strnorm.ContainsCyrillic(na)
	bIsCyr := strnorm.ContainsCyrillic(nb)
	if aIsCyr != bIsCyr {
		// Map known city pairs first.
		var cyr, lat string
		if aIsCyr {
			cyr, lat = na, nb
		} else {
			cyr, lat = nb, na
		}
		if knownLat, ok := strnorm.CyrToKnownLatin[cyr]; ok && knownLat == lat {
			return 100
		}
		// Fall back to transliteration
		translit := strnorm.TransliterateRuToEn(cyr)
		if translit == lat {
			return 95
		}
		// Fuzzy on transliteration
		if d := levenshteinNorm(translit, lat); d >= 88 {
			return d - 5 // small confidence penalty for relying on translit
		}
		return 0
	}

	// Same script — just fuzzy match
	d := levenshteinNorm(na, nb)
	if d >= 90 {
		return d
	}
	// Substring containment for "New York" vs "New York City"
	if len(na) >= 4 && len(nb) >= 4 {
		if strings.Contains(na, nb) || strings.Contains(nb, na) {
			return 80
		}
	}
	if d >= 70 {
		return d - 5
	}
	return 0
}

// levenshteinNorm returns 100 * (1 - distance / max_len) clamped to 0..100.
// 100 = identical, 0 = totally different.
func levenshteinNorm(a, b string) int {
	if a == b {
		return 100
	}
	if a == "" || b == "" {
		return 0
	}
	d := levenshtein([]rune(a), []rune(b))
	maxLen := len([]rune(a))
	if l := len([]rune(b)); l > maxLen {
		maxLen = l
	}
	if maxLen == 0 {
		return 100
	}
	score := 100 - (100*d)/maxLen
	if score < 0 {
		score = 0
	}
	return score
}

func levenshtein(a, b []rune) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	curr := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// compareStates is a lighter-weight comparison for state/region names.
// States vary even more in spelling than cities, so we're more lenient.
func compareStates(a, b string) int {
	na := strnorm.NormalizeName(a)
	nb := strnorm.NormalizeName(b)
	if na == "" || nb == "" {
		return -1
	}
	if na == nb {
		return 100
	}
	// "Moscow Oblast" vs "Moscow" → containment
	if strings.Contains(na, nb) || strings.Contains(nb, na) {
		return 85
	}
	if d := levenshteinNorm(na, nb); d >= 80 {
		return d
	}
	return 0
}
