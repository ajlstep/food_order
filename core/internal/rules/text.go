package rules

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// norm lowercases, removes Romanian diacritics and turns everything that is
// not a letter or digit into one space. "Ciorbă de PUI!" -> "ciorba de pui".
func norm(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'ă', 'â':
			r = 'a'
		case 'î':
			r = 'i'
		case 'ș', 'ş':
			r = 's'
		case 'ț', 'ţ':
			r = 't'
		case 'ё':
			r = 'е'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// text is a dish (or anything) prepared for matching.
type text struct {
	joined string   // " a b c " (padded, for phrases)
	tokens []string // words
}

func newText(parts ...string) text {
	n := norm(strings.Join(parts, " "))
	return text{joined: " " + n + " ", tokens: strings.Fields(n)}
}

// has says whether a keyword (already normalised) is in the text:
// - a phrase ("la cuptor") must appear as whole words;
// - a short word (< 3 letters) must be a whole word ("ou");
// - a longer word may be the start of a word ("smantan" ~ "smantana").
func (t text) has(kw string) bool {
	if kw == "" {
		return false
	}
	if strings.Contains(kw, " ") {
		return strings.Contains(t.joined, " "+kw+" ") || strings.Contains(t.joined, " "+kw)
	}
	short := utf8.RuneCountInString(kw) < 3
	for _, tok := range t.tokens {
		if tok == kw || !short && strings.HasPrefix(tok, kw) {
			return true
		}
	}
	return false
}

// anyOf says whether one of the keywords is in the text.
func (t text) anyOf(kws []string) bool {
	for _, kw := range kws {
		if t.has(kw) {
			return true
		}
	}
	return false
}

// allergenGroups maps an allergen to words that give it away in a dish name
// or component (ro + ru). The keys are what a user is likely to write.
// This is the code-side safety net on top of the allergens catalog gives.
var allergenGroups = normGroups(map[string][]string{
	"lactate": {"lactate", "lactat", "lapte", "lactoza", "smantana", "unt", "branza", "cascaval", "iaurt",
		"frisca", "chefir", "mozzarella", "parmezan", "telemea", "urda", "ricotta", "mascarpone", "sana",
		"молок", "молочн", "сметан", "сливоч", "сливк", "сыр", "сырн", "творог", "творож", "кефир", "йогурт", "лактоз", "брынз"},
	"gluten": {"gluten", "grau", "faina", "paste", "taitei", "spaghete", "macaroane", "paine", "crutoane", "gris",
		"orz", "secara", "pesmet", "aluat", "clatite", "galuste", "colac", "placinta", "lipie", "chec", "biscuit", "cuscus", "bulgur",
		"глютен", "пшениц", "мук", "макарон", "лапш", "хлеб", "сухар", "панир", "тест", "ячм", "рж", "манн", "блин", "пирог", "лаваш", "булгур", "кускус"},
	"oua": {"oua", "ou", "oul", "ouale", "oului", "omleta", "maioneza", "maioneza", "яйц", "яичн", "омлет", "майонез"},
	"peste": {"peste", "pestele", "somon", "ton", "macrou", "hering", "crap", "salau", "pastrav", "scrumbie", "biban", "merluciu",
		"рыб", "лосос", "тунец", "скумбри", "сельд", "треск", "карп", "судак", "форел", "минта", "хек"},
	"crustacee": {"creveti", "crab", "homar", "raci", "креветк", "краб", "рак"},
	"moluste":   {"midii", "calamar", "scoici", "caracatita", "мидии", "кальмар", "осьминог"},
	"nuci": {"nuci", "nuca", "alune", "migdale", "fistic", "caju", "pecan",
		"орех", "миндал", "фундук", "фисташ", "кешью"},
	"arahide": {"arahide", "arahida", "арахис"},
	"soia":    {"soia", "tofu", "соя", "соев", "тофу"},
	"telina":  {"telina", "сельдер"},
	"mustar":  {"mustar", "горчиц"},
	"susan":   {"susan", "tahini", "кунжут", "тахин"},
	"lupin":   {"lupin", "люпин"},
	"sulfiti": {"sulfiti", "сульфит"},
})

// methodWords are the words a user may write for a cooking method.
var methodWords = normGroups(map[string][]string{
	"raw":        {"crud", "cruda", "proaspat", "сырой", "свеж"},
	"boiled":     {"fiert", "fiarta", "вареный", "варен", "отварн"},
	"steamed":    {"abur", "la abur", "на пару"},
	"baked":      {"copt", "la cuptor", "запечен", "запеч", "печен"},
	"grilled":    {"grill", "gratar", "la gratar", "гриль"},
	"fried":      {"prajit", "prajita", "prajeli", "prajeala", "жарен", "жарк"},
	"deep_fried": {"prajit", "prajeli", "friteuza", "pane", "фритюр", "жарен"},
	"stewed":     {"inabusit", "tocana", "tushit", "туш", "тушен"},
	"smoked":     {"afumat", "afumata", "копчен"},
	"pickled":    {"murat", "murata", "marinat", "марин", "квашен", "солен"},
})

func normGroups(m map[string][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, words := range m {
		nk := norm(k)
		seen := map[string]bool{nk: true}
		list := []string{nk}
		for _, w := range words {
			if nw := norm(w); nw != "" && !seen[nw] {
				seen[nw] = true
				list = append(list, nw)
			}
		}
		out[k] = list
	}
	return out
}

// allergenKeywords returns the words to look for when a user is allergic to
// value: the whole group if value names it (or one of its words), plus the
// value itself.
func allergenKeywords(value string) []string {
	v := norm(value)
	kws := []string{v}
	vt := newText(v)
	for _, words := range allergenGroups {
		for _, w := range words {
			if w == v || vt.has(w) || newText(w).has(v) && len([]rune(v)) >= 4 {
				kws = append(kws, words...)
				break
			}
		}
	}
	return kws
}
