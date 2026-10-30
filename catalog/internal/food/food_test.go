package food

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormName(t *testing.T) {
	for in, want := range map[string]string{
		"  Ciorbă  de PUI ":       "ciorbă de pui",
		"Mămăligă cu brânză ŞI":   "mămăligă cu brânză și",
		"Supă «Țărănească»":       `supă "țărănească"`,
		"Салат   Оливье\tс ёлкой": "салат оливье с елкой",
	} {
		if got := NormName(in); got != want {
			t.Errorf("NormName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormList(t *testing.T) {
	if NormList(nil) != nil {
		t.Error("nil must stay nil")
	}
	if got := NormList([]string{}); got == nil || len(got) != 0 {
		t.Errorf("empty must stay empty: %#v", got)
	}
	if got := NormList([]string{" Gluten", "gluten", "", "Ouă"}); !reflect.DeepEqual(got, []string{"gluten", "ouă"}) {
		t.Errorf("%v", got)
	}
}

func TestParseWeight(t *testing.T) {
	for in, want := range map[string]float64{
		"250/50": 300, "300 g": 300, "300g": 300, "300 гр.": 300, "0,5 l": 500,
		"150+30 гр": 180, "1 kg": 1000, "200 ml": 200, "100/50/20": 170,
	} {
		got := ParseWeight(in)
		if got == nil || *got != want {
			t.Errorf("ParseWeight(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"", "1 buc", "porție", "abc/50", "0"} {
		if got := ParseWeight(in); got != nil {
			t.Errorf("ParseWeight(%q) = %v, want nil", in, *got)
		}
	}
}

func TestParseNutrition(t *testing.T) {
	n, per100, err := ParseNutrition(json.RawMessage(`{"kcal": 120, "protein": {"min": 5, "max": 7}, "fat_g": {"min": 4, "max": 2}, "salt_g": -1, "x": 3, "per": "100 g"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !per100 {
		t.Error("per 100 g not seen")
	}
	if n.Kcal == nil || n.Kcal.Min != 120 || n.Kcal.Max != 120 {
		t.Errorf("kcal %+v", n.Kcal)
	}
	if n.ProteinG == nil || n.ProteinG.Min != 5 || n.ProteinG.Max != 7 {
		t.Errorf("protein %+v", n.ProteinG)
	}
	if n.FatG == nil || n.FatG.Min != 2 || n.FatG.Max != 4 {
		t.Errorf("reversed fat must be swapped: %+v", n.FatG)
	}
	if n.SaltG != nil {
		t.Errorf("negative salt must be dropped: %+v", n.SaltG)
	}
	s := n.Scale(2.5)
	if s.Kcal.Min != 300 || s.ProteinG.Max != 17.5 {
		t.Errorf("scale %+v %+v", s.Kcal, s.ProteinG)
	}

	if n, _, err := ParseNutrition(nil); n != nil || err != nil {
		t.Errorf("nothing => nil, nil: %v %v", n, err)
	}
	if n, _, err := ParseNutrition(json.RawMessage(`{"kcal": null}`)); n != nil || err != nil {
		t.Errorf("only nulls => nil: %+v %v", n, err)
	}
	if _, _, err := ParseNutrition(json.RawMessage(`[1]`)); err == nil {
		t.Error("an array is not nutrition")
	}
}
