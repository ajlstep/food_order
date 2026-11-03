package profile

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// How text from people is brought to one form, so that the same thing written
// two ways is stored once. The rules for labels are the ones of catalog
// (food.NormName), because allergens are compared with what catalog writes.

var labelReplacer = strings.NewReplacer(
	"ş", "ș", "ţ", "ț", // cedilla -> comma below (both are used for Romanian)
	"«", `"`, "»", `"`, "„", `"`, "“", `"`, "”", `"`, "’", "'", "`", "'",
	"ё", "е",
)

// normLabel: lower case, one space between words, one spelling of ș and ț.
func normLabel(s string) string {
	s = labelReplacer.Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// tooLong says whether s has more than max characters (not bytes).
func tooLong(s string, max int) bool { return utf8.RuneCountInString(s) > max }

var phoneForm = regexp.MustCompile(`^[0-9]{8,15}$`)

// NormPhone keeps the digits and puts + in front: "+373 69 123-456" and
// "37369123456" are the same phone. ok is false if it cannot be a phone.
func NormPhone(s string) (string, bool) {
	var digits strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return "", false
		}
	}
	if !phoneForm.MatchString(digits.String()) {
		return "", false
	}
	return "+" + digits.String(), true
}

// targetNames are the nutrients a target can be set for.
var targetNames = map[string]bool{"kcal": true, "protein_g": true, "fat_g": true, "carbs_g": true}

// normTarget reads "kcal=700" (also "kcal 700", "kcal:700") and writes it as "kcal=700".
func normTarget(s string) (string, error) {
	f := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r == '=' || r == ':' || r == ' ' })
	if len(f) != 2 || !targetNames[f[0]] {
		return "", fmt.Errorf("a target looks like kcal=700 (kcal, protein_g, fat_g, carbs_g)")
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(f[1], ",", "."), 64)
	if err != nil || v <= 0 || v > 10000 {
		return "", fmt.Errorf("%q is not a positive number", f[1])
	}
	return f[0] + "=" + strconv.FormatFloat(v, 'f', -1, 64), nil
}
