// Package l10n is the product's localization system: every string a person
// reads is a keyed message in a catalog, and a request is answered in the
// locale its reader prefers.
//
// English is the source catalog. It is complete by definition — a key exists
// because English names it — and every other locale is a translation of it
// that may be partial: a key a locale has not translated yet falls back to
// English for that key alone, so adding a language never makes a page
// unreadable. Catalogs are embedded JSON files under locales/, one per BCP 47
// tag, so a translator edits data rather than Go.
//
// A message is either a string or, when it depends on a count, an object of
// CLDR plural categories ("zero", "one", "two", "few", "many", "other";
// "other" is required). Placeholders are named, {like_this}, so a translation
// may reorder them; a count message also receives {count}.
package l10n

import (
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Locale is a BCP 47 language tag naming a catalog, such as "en" or "pt-BR".
type Locale string

// Default is the source locale. Its catalog is complete, and it is what a
// request is answered in when nothing it prefers is available.
const Default Locale = "en"

// Category is a CLDR plural category.
type Category string

const (
	Zero  Category = "zero"
	One   Category = "one"
	Two   Category = "two"
	Few   Category = "few"
	Many  Category = "many"
	Other Category = "other"
)

// PluralRule maps a count to its plural category for one language.
type PluralRule func(n int) Category

// message is one catalog entry: a plain text, or plural forms by category.
type message struct {
	text   string
	plural map[Category]string
}

// Catalog is one locale's messages.
type Catalog struct {
	Locale Locale
	// Name is the language's own name for itself, as the language picker
	// shows it: "English", "Deutsch", "日本語".
	Name string
	// Listed is whether the language picker offers it. Only the generated
	// pseudo-locale is unlisted.
	Listed   bool
	rule     PluralRule
	messages map[string]message
}

//go:embed locales/*.json
var embedded embed.FS

// pluralRules are the CLDR cardinal rules of the languages that have a
// catalog. A catalog for a language without a rule here is refused at load,
// so a translation can never be shipped that picks plural forms wrongly.
var pluralRules = map[string]PluralRule{
	"en": func(n int) Category {
		if n == 1 {
			return One
		}
		return Other
	},
}

// categoriesOf is every category a rule can produce, found by asking it, so
// a catalog cannot carry a form its language never selects nor miss one it
// does.
func categoriesOf(rule PluralRule) map[Category]bool {
	seen := map[Category]bool{Other: true}
	for n := 0; n <= 1000; n++ {
		seen[rule(n)] = true
	}
	return seen
}

var (
	loadOnce sync.Once
	catalogs map[Locale]*Catalog
	loadErr  error
)

// keyPattern is what a message key looks like: dotted lowercase segments,
// grouped by the surface that shows them ("prefs.region.title").
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// placeholderPattern finds {name} placeholders.
var placeholderPattern = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

func load() {
	catalogs = map[Locale]*Catalog{}
	entries, err := embedded.ReadDir("locales")
	if err != nil {
		loadErr = err
		return
	}
	for _, entry := range entries {
		catalog, err := parseCatalog(entry.Name())
		if err != nil {
			loadErr = err
			return
		}
		catalogs[catalog.Locale] = catalog
	}
	if catalogs[Default] == nil {
		loadErr = fmt.Errorf("l10n: the %s source catalog is missing", Default)
		return
	}
	if catalogs[Pseudo] != nil {
		loadErr = fmt.Errorf("l10n: %s is generated from the source catalog and has no file", Pseudo)
		return
	}
	catalogs[Pseudo] = pseudoCatalog(catalogs[Default])
	for _, catalog := range catalogs {
		if err := checkAgainstSource(catalog, catalogs[Default]); err != nil {
			loadErr = err
			return
		}
	}
}

func parseCatalog(file string) (*Catalog, error) {
	raw, err := embedded.ReadFile(path.Join("locales", file))
	if err != nil {
		return nil, err
	}
	var document struct {
		Locale   string                     `json:"locale"`
		Name     string                     `json:"name"`
		Messages map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("l10n: %s: %w", file, err)
	}
	locale := Locale(document.Locale)
	if file != string(locale)+".json" {
		return nil, fmt.Errorf("l10n: %s declares locale %q; a catalog is named for its locale", file, locale)
	}
	rule, ok := pluralRules[baseLanguage(locale)]
	if !ok {
		return nil, fmt.Errorf("l10n: %s has no plural rule; add its CLDR rule to pluralRules", locale)
	}
	if strings.TrimSpace(document.Name) == "" {
		return nil, fmt.Errorf("l10n: %s names no language for the picker", locale)
	}
	catalog := &Catalog{Locale: locale, Name: document.Name, Listed: true, rule: rule, messages: map[string]message{}}
	allowed := categoriesOf(rule)
	for key, value := range document.Messages {
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("l10n: %s: key %q is not a dotted lowercase key", locale, key)
		}
		var text string
		if json.Unmarshal(value, &text) == nil {
			catalog.messages[key] = message{text: text}
			continue
		}
		var forms map[Category]string
		if err := json.Unmarshal(value, &forms); err != nil {
			return nil, fmt.Errorf("l10n: %s: %s is neither a string nor plural forms", locale, key)
		}
		if _, ok := forms[Other]; !ok {
			return nil, fmt.Errorf("l10n: %s: %s has no %q form", locale, key, Other)
		}
		for category := range forms {
			if !allowed[category] {
				return nil, fmt.Errorf("l10n: %s: %s has a %q form %s never selects", locale, key, category, locale)
			}
		}
		catalog.messages[key] = message{plural: forms}
	}
	return catalog, nil
}

// checkAgainstSource holds a translation to the source catalog: it may omit
// keys (they fall back) but may not invent them, must agree on whether a key
// counts, and must use exactly the source's placeholders, so a translation
// can neither drop a name the reader needs nor ask for one nobody supplies.
func checkAgainstSource(catalog, source *Catalog) error {
	for key, translated := range catalog.messages {
		original, ok := source.messages[key]
		if !ok {
			return fmt.Errorf("l10n: %s: %s is not a key of the %s source catalog", catalog.Locale, key, source.Locale)
		}
		if (translated.plural == nil) != (original.plural == nil) {
			return fmt.Errorf("l10n: %s: %s and its source disagree about whether it counts", catalog.Locale, key)
		}
		want := placeholders(original)
		if got := placeholders(translated); !equalSets(got, want) {
			return fmt.Errorf("l10n: %s: %s uses placeholders %v, the source uses %v", catalog.Locale, key, sorted(got), sorted(want))
		}
	}
	return nil
}

func placeholders(value message) map[string]bool {
	found := map[string]bool{}
	texts := []string{value.text}
	for _, form := range value.plural {
		texts = append(texts, form)
	}
	for _, text := range texts {
		for _, match := range placeholderPattern.FindAllStringSubmatch(text, -1) {
			found[match[1]] = true
		}
	}
	if value.plural != nil {
		// A count message always receives {count}; a form need not show it
		// ("one message" spelled out), so its absence is not a mismatch.
		delete(found, "count")
	}
	return found
}

func equalSets(left, right map[string]bool) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if !right[key] {
			return false
		}
	}
	return true
}

func sorted(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func baseLanguage(locale Locale) string {
	base, _, _ := strings.Cut(strings.ToLower(string(locale)), "-")
	return base
}

// Load reports whether every embedded catalog is well formed and consistent
// with the source. The server calls it at startup so a broken catalog stops a
// deployment rather than a page.
func Load() error {
	loadOnce.Do(load)
	return loadErr
}

// Supported is every locale with a catalog, the source first, then by tag.
func Supported() []*Catalog {
	if Load() != nil {
		return nil
	}
	result := make([]*Catalog, 0, len(catalogs))
	for _, catalog := range catalogs {
		result = append(result, catalog)
	}
	sort.Slice(result, func(left, right int) bool {
		if (result[left].Locale == Default) != (result[right].Locale == Default) {
			return result[left].Locale == Default
		}
		return result[left].Locale < result[right].Locale
	})
	return result
}

// Has reports whether the locale has a catalog.
func Has(locale Locale) bool {
	if Load() != nil {
		return false
	}
	_, ok := catalogs[locale]
	return ok
}

// Keys is every key of the source catalog, sorted.
func Keys() []string {
	if Load() != nil {
		return nil
	}
	keys := make([]string, 0, len(catalogs[Default].messages))
	for key := range catalogs[Default].messages {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Localizer answers messages in one locale, falling back to the source
// catalog key by key.
type Localizer struct {
	locale Locale
}

// For is the localizer for a locale; a locale without a catalog is answered
// in the source locale.
func For(locale Locale) Localizer {
	if !Has(locale) {
		locale = Default
	}
	return Localizer{locale: locale}
}

// Locale is the locale this localizer answers in.
func (l Localizer) Locale() Locale {
	if l.locale == "" {
		return Default
	}
	return l.locale
}

func (l Localizer) lookup(key string) (message, PluralRule, bool) {
	if Load() != nil {
		return message{}, nil, false
	}
	if catalog, ok := catalogs[l.Locale()]; ok {
		if value, ok := catalog.messages[key]; ok {
			return value, catalog.rule, true
		}
	}
	source := catalogs[Default]
	value, ok := source.messages[key]
	return value, source.rule, ok
}

// T is the message for key with its placeholders filled from args, given as
// alternating names and values: T("prefs.saved") or
// T("greeting", "name", "Ada"). A key no catalog has is answered as the key
// itself, which a test then finds on the page rather than a blank.
func (l Localizer) T(key string, args ...any) string {
	value, _, ok := l.lookup(key)
	if !ok {
		return key
	}
	if value.plural != nil {
		return fill(value.plural[Other], args)
	}
	return fill(value.text, args)
}

// N is the count message for key, in the plural form the locale selects for
// count, with {count} and the other placeholders filled.
func (l Localizer) N(key string, count int, args ...any) string {
	value, rule, ok := l.lookup(key)
	if !ok {
		return key
	}
	if value.plural == nil {
		return fill(value.text, append(args, "count", count))
	}
	form, ok := value.plural[rule(count)]
	if !ok {
		form = value.plural[Other]
	}
	return fill(form, append(args, "count", count))
}

// Client is the messages under the "client." prefix, for scripts that build
// text in the browser. Only those are sent, so a page does not carry the
// whole catalog.
func (l Localizer) Client() map[string]any {
	result := map[string]any{}
	for _, key := range Keys() {
		if !strings.HasPrefix(key, "client.") {
			continue
		}
		value, _, _ := l.lookup(key)
		if value.plural != nil {
			forms := map[string]string{}
			for category, form := range value.plural {
				forms[string(category)] = form
			}
			result[key] = forms
			continue
		}
		result[key] = value.text
	}
	return result
}

func fill(text string, args []any) string {
	if len(args) == 0 {
		return text
	}
	values := make(map[string]string, len(args)/2)
	for index := 0; index+1 < len(args); index += 2 {
		name, ok := args[index].(string)
		if !ok {
			continue
		}
		values[name] = format(args[index+1])
	}
	return placeholderPattern.ReplaceAllStringFunc(text, func(match string) string {
		if value, ok := values[match[1:len(match)-1]]; ok {
			return value
		}
		return match
	})
}

func format(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case int:
		return strconv.Itoa(typed)
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}
