package contracts

import (
	"encoding/json"
	"testing"
)

func TestRangeTolerant(t *testing.T) {
	for in, want := range map[string]Range{
		`{"min": 1, "max": 2}`: {1, 2},
		`{"min": 3}`:           {3, 3},
		`{"value": 4}`:         {4, 4},
		`5`:                    {5, 5},
		`[6, 7]`:               {6, 7},
	} {
		var r Range
		if err := json.Unmarshal([]byte(in), &r); err != nil || r != want {
			t.Errorf("%s -> %+v %v, want %+v", in, r, err, want)
		}
	}
	var r Range
	if err := json.Unmarshal([]byte(`"lots"`), &r); err == nil {
		t.Error("a string is not a range")
	}
}

func TestComponentFromString(t *testing.T) {
	var list []Component
	if err := json.Unmarshal([]byte(`["Supă de linte", {"name": "morcov", "grams": 40, "cooking_method": "boiled"}]`), &list); err != nil {
		t.Fatal(err)
	}
	if list[0].Name != "Supă de linte" || list[1].Name != "morcov" || *list[1].Grams != 40 || list[1].Method != "boiled" {
		t.Errorf("%+v", list)
	}
}

func TestNormMethod(t *testing.T) {
	for in, want := range map[string]string{"": "", "Fried": "fried", "deep-fried": "deep_fried", "la cuptor": "other"} {
		if got := NormMethod(in); got != want {
			t.Errorf("NormMethod(%q) = %q, want %q", in, got, want)
		}
	}
}
