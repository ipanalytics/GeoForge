package output

import (
	"math"
	"testing"
)

func TestRoundCoord(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{52.05780123, 52.05780},
		{10.00580789, 10.00581},
		{-122.41949999, -122.4195},
		{0, 0},
		{math.NaN(), 0},
	}
	for _, c := range cases {
		got := RoundCoord(c.in)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("RoundCoord(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCleanCity(t *testing.T) {
	cases := map[string]string{
		"San Francisco (South Beach)": "San Francisco",
		"Moscow":                      "Moscow",
		"Kazan’":                      "Kazan",
		"Kazan'":                      "Kazan",
		"Tverʼ":                       "Tver",
		"Kazan’ (Sovetskiy Rayon)":    "Kazan",
		"Frankfurt am Main (Westend)": "Frankfurt am Main",
		"Thanh pho Ho Chi Minh":       "Ho Chi Minh",
		"Ciudad de Mexico":            "Mexico",
		"Al Baladiyah Doha":           "Doha",
		"Kyoto-shi":                   "Kyoto",
		"Seoul City":                  "Seoul",
		"SÃ£o Paulo":                  "São Paulo",
		"Sanaâ€™a":                    "Sana'a",
		"  Tokyo  ":                   "Tokyo",
		"London (City of London)":     "London",
		"":                            "",
	}
	for in, want := range cases {
		if got := CleanCity(in); got != want {
			t.Errorf("CleanCity(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanState(t *testing.T) {
	cases := map[string]string{
		"Horad Minsk":              "Minsk",
		"Minskaya voblasts'":       "Minskaya Voblast",
		"Minskaya voblasts’":       "Minskaya Voblast",
		"Minskaya voblasts":        "Minskaya Voblast",
		"Odeska oblast":            "Odeska oblast",
		"Odes'ka oblast'":          "Odes'ka Oblast",
		"Mangistauskaya Oblast’":   "Mangistauskaya Oblast",
		"Seoul-teukbyeolsi":        "Seoul",
		"Busan-gwangyeoksi":        "Busan",
		"Sejong-teukbyeolja-chi":   "Sejong",
		"Almaty Qalasy":            "Almaty",
		"Aqmola Oblysy":            "Aqmola",
		"Toshkent Viloyati":        "Toshkent",
		"Fargona viloyati":         "Fargona",
		"Doha Governorate":         "Doha",
		"Cairo Governorate":        "Cairo",
		"Provincia de Cordoba":     "Cordoba",
		"Estado de Sao Paulo":      "Sao Paulo",
		"SÃ£o Paulo Estado":        "São Paulo",
		"Baladiyat ad DawÃ¡Â¸Â©ah": "Baladiyat ad DawáÂ¸Â©ah",
		"g. Moskva":                "Moskva",
		"Moskovskaya oblast'":      "Moskovskaya Oblast",
		"Northern District":        "Northern District",
		"Tokyo":                    "Tokyo",
		"":                         "",
	}
	for in, want := range cases {
		if got := CleanState(in); got != want {
			t.Errorf("CleanState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepairMojibake(t *testing.T) {
	cases := map[string]string{
		"SÃ£o Paulo": "São Paulo",
		"BogotÃ¡":    "Bogotá",
		"MÃ¼nchen":   "München",
		"Sanaâ€™a":   "Sana'a",
	}
	for in, want := range cases {
		if got := RepairMojibake(in); got != want {
			t.Errorf("RepairMojibake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHasTextArtifact(t *testing.T) {
	cases := map[string]bool{
		"SÃ£o Paulo": false, // repairable before final artifact check
		"Bogotá":     false,
		"Kazan’":     true,
		"city �":     true,
	}
	for in, want := range cases {
		cleaned := RepairMojibake(in)
		if got := HasTextArtifact(cleaned); got != want {
			t.Errorf("HasTextArtifact(%q after repair %q) = %v, want %v", in, cleaned, got, want)
		}
	}
}

func TestDedupeStateCity(t *testing.T) {
	cases := []struct {
		state, city         string
		wantState, wantCity string
	}{
		{"Moscow", "Moscow", "", "Moscow"},
		// Cross-language transliteration variants are not deduplicated at the
		// output layer; that equivalence belongs in the consensus layer.
		{"Moskva", "Moscow", "Moskva", "Moscow"},
		{"Tokyo", "Tokyo", "", "Tokyo"},
		{"Almaty Qalasy", "Almaty", "", "Almaty"},
		{"Horad Minsk", "Minsk", "", "Minsk"},
		{"Northern District", "Ramat Yishai", "Northern District", "Ramat Yishai"},
		{"Seoul-teukbyeolsi", "Seoul", "", "Seoul"},
		{"", "Berlin", "", "Berlin"},
		{"Bayern", "", "Bayern", ""},
	}
	for _, c := range cases {
		gotS, gotC := DedupeStateCity(c.state, c.city)
		if gotS != c.wantState || gotC != c.wantCity {
			t.Errorf("DedupeStateCity(%q, %q) = (%q, %q), want (%q, %q)",
				c.state, c.city, gotS, gotC, c.wantState, c.wantCity)
		}
	}
}
