package cleaning

import (
	"math"
	"testing"
)

func TestNormalizeTitle(t *testing.T) {
	cases := map[string]string{
		"Padre Vieira: Sonhos Proféticos, Profecias Oníricas.": "padre vieira sonhos profeticos profecias oniricas",
		"  (E) Análise de redistribuição  ":                    "e analise de redistribuicao",
		"ＦＵＬＬＷＩＤＴＨ ﬁle":                                        "fullwidth file",
		"Über—Straße":                                          "uber straße",
		"":                                                     "",
		"?!":                                                   "",
	}
	for in, want := range cases {
		if got := NormalizeTitle(in); got != want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitleSimilarity(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		min  float64 // inclusive
		max  float64 // inclusive
	}{
		{"identical", "Aids na escola", "Aids na escola", 1, 1},
		{"case punctuation and accents only",
			"Aids na escola: representações docentes sobre o cotidiano",
			"AIDS NA ESCOLA - REPRESENTACOES DOCENTES SOBRE O COTIDIANO", 1, 1},
		{"subtitle dropped, long enough to trust",
			"Padre Vieira: Sonhos Proféticos, Profecias Oníricas",
			"Padre Vieira: Sonhos Proféticos, Profecias Oníricas. O tempo do Quinto Império dos Sermões de Xavier Dormindo.",
			1, 1},
		{"short prefix does not count",
			"Introduction", "Introduction to the theory of games", 0, 0.5},
		{"prefix must end on a word boundary",
			"Análise de redistribuição de esforços em vigas de concreto prot",
			"Análise de redistribuição de esforços em vigas de concreto protendido com seções compostas",
			0, 0.85},
		{"one typo", "Efeitos ocasionados pela ingestão de microplásticos",
			"Efeitos ocasionados pela ingestao de microplasticos no", 0.9, 1},
		{"different theses, same field",
			"Efeitos ocasionados pela ingestão de microplásticos no lambari rosa",
			"Efeitos da exposição a microplásticos em tilápias do Nilo", 0, 0.6},
		{"empty", "", "anything", 0, 0},
		{"both punctuation", "??", "!!", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TitleSimilarity(c.a, c.b)
			if got < c.min || got > c.max {
				t.Errorf("TitleSimilarity = %.3f, want in [%.2f, %.2f]", got, c.min, c.max)
			}
			if back := TitleSimilarity(c.b, c.a); math.Abs(back-got) > 1e-9 {
				t.Errorf("not symmetric: %.3f vs %.3f", got, back)
			}
		})
	}
}
