// Package food holds the small deterministic helpers of catalog:
// normalising names and lists, reading a weight like "250/50", reading the
// nutrition a source sends, and the hashes that tell whether a dish changed.
// No nutrition is computed here: values come from the source or from llm.
package food

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"food-order/catalog/internal/contracts"
)

var nameReplacer = strings.NewReplacer(
	"ş", "ș", "ţ", "ț", // cedilla -> comma below (both are used for Romanian)
	"«", `"`, "»", `"`, "„", `"`, "“", `"`, "”", `"`, "’", "'", "`", "'",
	"ё", "е",
)

// NormName is the fallback key of a dish when the source gives no
// external_id: lower case, one space between words, one spelling of ș/ț and
// of the quotes.
func NormName(s string) string {
	s = nameReplacer.Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// NormList normalises a list of short labels (allergens, tags): trimmed,
// lower case, without empties and duplicates, in the original order.
// nil stays nil ("not given"); an empty list stays empty ("none").
func NormList(in []string) []string {
	if in == nil {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = NormName(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

var weightPart = regexp.MustCompile(`^(\d+(?:[.,]\d+)?)\s*(g|gr|grame|г|гр|ml|мл|l|л|kg|кг)?\.?$`)

// ParseWeight reads the weight of a portion from text like "250/50",
// "300 g", "0,5 l", "150+30 гр": the parts are added up (ml count as g,
// l and kg as 1000). Anything else ("1 buc", "") gives nil.
func ParseWeight(text string) *float64 {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}
	total := 0.0
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == '/' || r == '+' }) {
		m := weightPart.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			return nil
		}
		f, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", "."), 64)
		if err != nil {
			return nil
		}
		switch m[2] {
		case "l", "л", "kg", "кг":
			f *= 1000
		}
		total += f
	}
	if total <= 0 {
		return nil
	}
	return &total
}

// nutritionKeys maps the names a source may use to the catalog fields.
var nutritionKeys = map[string]int{
	"kcal": 0, "calories": 0, "energy_kcal": 0, "energy": 0,
	"protein_g": 1, "protein": 1, "proteins": 1,
	"fat_g": 2, "fat": 2, "fats": 2,
	"saturated_fat_g": 3, "saturated_fat": 3,
	"carbs_g": 4, "carbs": 4, "carbohydrates": 4, "carbohydrates_g": 4,
	"sugars_g": 5, "sugars": 5, "sugar": 5,
	"fiber_g": 6, "fiber": 6, "fibre": 6,
	"salt_g": 7, "salt": 7,
}

// ParseNutrition reads the nutrition a source sent. Values may be numbers
// (exact: min = max) or {min, max}. With "per": "100g" (or "100 g") the
// values are per 100 g and per100 is true; the caller scales them by
// weight_g / 100. nil, nil means the source sent nothing usable.
func ParseNutrition(raw json.RawMessage) (n *contracts.Nutrition, per100 bool, err error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false, fmt.Errorf("nutrition: expected an object: %w", err)
	}
	out := &contracts.Nutrition{}
	fields := out.Fields()
	for k, v := range obj {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "per" {
			var s string
			if json.Unmarshal(v, &s) == nil {
				s = strings.ReplaceAll(strings.ToLower(s), " ", "")
				per100 = s == "100g" || s == "100ml" || s == "per_100g"
			}
			continue
		}
		i, ok := nutritionKeys[k]
		if !ok {
			continue
		}
		var r contracts.Range
		if err := json.Unmarshal(v, &r); err != nil {
			return nil, false, fmt.Errorf("nutrition: %s: %w", k, err)
		}
		if string(bytes.TrimSpace(v)) == "null" {
			continue
		}
		*fields[i] = &r
	}
	return out.Clean(), per100, nil
}

// Hash is a short stable fingerprint of the parts.
func Hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// F formats an optional number for a hash ("" when nil).
func F(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'f', -1, 64)
}

// JSON marshals v for a hash or a TEXT column (never fails for plain data).
func JSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
