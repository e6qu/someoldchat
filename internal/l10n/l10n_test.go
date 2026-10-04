package l10n

import (
	"strings"
	"testing"
)

// The embedded catalogs load, the source is complete and listed, and the
// pseudo-locale is generated from it but kept out of the language picker.
func TestCatalogsLoad(t *testing.T) {
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	supported := Supported()
	if len(supported) < 2 || supported[0].Locale != Default || !supported[0].Listed {
		t.Fatalf("supported=%+v, want the listed source first", supported)
	}
	pseudo := false
	for _, catalog := range supported {
		if catalog.Locale == Pseudo {
			pseudo = true
			if catalog.Listed {
				t.Error("the pseudo-locale is offered by the language picker")
			}
		}
	}
	if !pseudo {
		t.Error("the pseudo-locale was not generated")
	}
	if len(Keys()) == 0 {
		t.Fatal("the source catalog has no messages")
	}
}

// A message fills its named placeholders in any order, leaves a placeholder
// nobody supplied visible, and a missing key reads as the key.
func TestMessagesFillPlaceholders(t *testing.T) {
	catalog := &Catalog{Locale: "xx", rule: pluralRules["en"], messages: map[string]message{
		"greeting.named":  {text: "{greeting}, {name}!"},
		"inbox.count":     {plural: map[Category]string{One: "One message for {name}", Other: "{count} messages for {name}"}},
		"inbox.unchanged": {text: "{count} items"},
	}}
	withCatalog(t, catalog, func() {
		localizer := Localizer{locale: "xx"}
		if got := localizer.T("greeting.named", "name", "Ada", "greeting", "Hello"); got != "Hello, Ada!" {
			t.Errorf("T = %q", got)
		}
		if got := localizer.T("greeting.named", "name", "Ada"); got != "{greeting}, Ada!" {
			t.Errorf("an unsupplied placeholder = %q, want it left visible", got)
		}
		if got := localizer.N("inbox.count", 1, "name", "Ada"); got != "One message for Ada" {
			t.Errorf("N(1) = %q", got)
		}
		if got := localizer.N("inbox.count", 3, "name", "Ada"); got != "3 messages for Ada" {
			t.Errorf("N(3) = %q", got)
		}
		if got := localizer.N("inbox.unchanged", 4); got != "4 items" {
			t.Errorf("N on a plain message = %q", got)
		}
		if got := localizer.T("no.such_key"); got != "no.such_key" {
			t.Errorf("a missing key = %q, want the key", got)
		}
	})
}

// A locale answers what it has translated and falls back to the source, key
// by key, for what it has not.
func TestALocaleFallsBackToTheSourceKeyByKey(t *testing.T) {
	key := Keys()[0]
	partial := &Catalog{Locale: "xx", rule: pluralRules["en"], messages: map[string]message{}}
	withCatalog(t, partial, func() {
		if got, want := For("xx").T(key), For(Default).T(key); got != want {
			t.Errorf("untranslated %s = %q, want the source %q", key, got, want)
		}
	})
	translated := &Catalog{Locale: "xx", rule: pluralRules["en"], messages: map[string]message{key: {text: "translated"}}}
	withCatalog(t, translated, func() {
		if got := For("xx").T(key); got != "translated" {
			t.Errorf("translated %s = %q", key, got)
		}
	})
	if For("zz").Locale() != Default {
		t.Error("a locale with no catalog is not answered in the source")
	}
}

// A translation may not invent keys, change whether a key counts, or use
// placeholders other than the source's.
func TestATranslationIsHeldToTheSource(t *testing.T) {
	source := &Catalog{Locale: Default, messages: map[string]message{
		"a.plain":  {text: "Hello {name}"},
		"a.counts": {plural: map[Category]string{One: "one", Other: "{count} things"}},
	}}
	for name, translation := range map[string]map[string]message{
		"an invented key":               {"a.invented": {text: "x"}},
		"a dropped placeholder":         {"a.plain": {text: "Bonjour"}},
		"an extra placeholder":          {"a.plain": {text: "Bonjour {name} {other}"}},
		"a plain message that counts":   {"a.plain": {plural: map[Category]string{Other: "{name}"}}},
		"a count message that does not": {"a.counts": {text: "things"}},
	} {
		catalog := &Catalog{Locale: "xx", messages: translation}
		if err := checkAgainstSource(catalog, source); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	consistent := &Catalog{Locale: "xx", messages: map[string]message{
		"a.plain":  {text: "Bonjour {name}"},
		"a.counts": {plural: map[Category]string{Other: "{count} choses"}},
	}}
	if err := checkAgainstSource(consistent, source); err != nil {
		t.Errorf("a consistent translation was refused: %v", err)
	}
}

// The browser's Accept-Language is honoured by quality, an explicit choice
// wins over it, a base language matches its variants, and an unknown
// language is answered in the source.
func TestNegotiate(t *testing.T) {
	for _, item := range []struct {
		chosen Locale
		accept string
		want   Locale
	}{
		{"", "", Default},
		{"", "fr-FR,fr;q=0.9", Default},
		{"", "en-GB,en;q=0.8", Default},
		{"", "fr;q=0.9, en-XA;q=0.95", Pseudo},
		{"", "en-XA;q=0", Default},
		{Pseudo, "en", Pseudo},
		{"zz", "en-XA", Pseudo},
		{"EN-xa", "", Pseudo},
	} {
		if got := Negotiate(item.chosen, item.accept); got != item.want {
			t.Errorf("Negotiate(%q, %q) = %q, want %q", item.chosen, item.accept, got, item.want)
		}
	}
}

// The pseudo-locale changes every letter it can and nothing a placeholder
// names, so a page in it shows which text is localized and that
// placeholders survive.
func TestPseudoLocaleKeepsPlaceholders(t *testing.T) {
	got := pseudoText("Hello {name}, you have {count} new")
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
		t.Fatalf("%q is not bracketed", got)
	}
	if !strings.Contains(got, "{name}") || !strings.Contains(got, "{count}") {
		t.Fatalf("%q lost a placeholder", got)
	}
	if strings.ContainsAny(strings.NewReplacer("{name}", "", "{count}", "").Replace(got), "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("%q kept an unaccented letter outside a placeholder", got)
	}
	for _, key := range Keys() {
		if For(Pseudo).T(key) == For(Default).T(key) {
			t.Errorf("%s reads the same in the pseudo-locale", key)
		}
	}
}

// withCatalog runs body with catalog registered beside the embedded ones.
func withCatalog(t *testing.T, catalog *Catalog, body func()) {
	t.Helper()
	if err := Load(); err != nil {
		t.Fatal(err)
	}
	previous, had := catalogs[catalog.Locale]
	catalogs[catalog.Locale] = catalog
	defer func() {
		if had {
			catalogs[catalog.Locale] = previous
		} else {
			delete(catalogs, catalog.Locale)
		}
	}()
	body()
}
