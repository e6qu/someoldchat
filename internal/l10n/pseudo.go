package l10n

import "strings"

// Pseudo is a generated locale in which every message is the source text with
// its letters accented and the whole bracketed: "Preferences" reads
// "[Ƥŕéƒéŕéñçéš]". A page rendered in it shows at a glance which text came
// from a catalog and which is still written into the markup, and that
// placeholders survive translation; the brackets show where a layout clips a
// string. It is never offered by the language picker and has no file: it is
// derived from the source catalog at load, so it is always complete.
const Pseudo Locale = "en-XA"

const pseudoLetters = "åƀçđéƒĝĥîĵķļɱñöƥǫŕšţûṽŵẋýž"

func pseudoCatalog(source *Catalog) *Catalog {
	catalog := &Catalog{Locale: Pseudo, Name: "Pseudo-localized English", rule: source.rule, messages: map[string]message{}}
	for key, value := range source.messages {
		if value.plural == nil {
			catalog.messages[key] = message{text: pseudoText(value.text)}
			continue
		}
		forms := make(map[Category]string, len(value.plural))
		for category, form := range value.plural {
			forms[category] = pseudoText(form)
		}
		catalog.messages[key] = message{plural: forms}
	}
	return catalog
}

// pseudoText accents every ASCII letter outside a {placeholder} and brackets
// the result.
func pseudoText(text string) string {
	letters := []rune(pseudoLetters)
	var out strings.Builder
	out.WriteString("[")
	inPlaceholder := false
	for _, r := range text {
		switch {
		case r == '{':
			inPlaceholder = true
		case r == '}':
			inPlaceholder = false
		case !inPlaceholder && r >= 'a' && r <= 'z':
			r = letters[r-'a']
		case !inPlaceholder && r >= 'A' && r <= 'Z':
			r = []rune(strings.ToUpper(string(letters[r-'A'])))[0]
		}
		out.WriteRune(r)
	}
	out.WriteString("]")
	return out.String()
}
