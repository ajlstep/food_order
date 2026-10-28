package menu

import (
	"regexp"
	"strconv"
	"strings"
)

var roFix = strings.NewReplacer("ş", "ș", "ţ", "ț")

// NormName turns a dish name into the key used for recognising the same dish
// when it comes back: lower case, one space between words, Romanian
// cedilla letters replaced by the comma-below ones.
func NormName(s string) string {
	s = roFix.Replace(strings.ToLower(s))
	return strings.Join(strings.Fields(s), " ")
}

// CleanText collapses all whitespace (including non-breaking spaces).
func CleanText(s string) string { return strings.Join(strings.Fields(s), " ") }

var priceRe = regexp.MustCompile(`\d+(?:[.,]\d+)?`)

// ParsePrice takes the first number from text such as "45 lei" or "45,50 MDL".
func ParsePrice(s string) (float64, bool) {
	m := priceRe.FindString(s)
	if m == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.Replace(m, ",", ".", 1), 64)
	return v, err == nil
}
