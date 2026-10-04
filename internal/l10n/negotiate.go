package l10n

import (
	"sort"
	"strconv"
	"strings"
)

// Negotiate picks the locale a request is answered in: the reader's own
// choice when it has a catalog, else the best of the browser's
// Accept-Language that does, else the source locale.
//
// A language range matches a catalog of the same tag first and then one of
// the same base language, in both directions — "de-AT" is answered by "de",
// and "pt" by "pt-BR" when that is the only Portuguese — because a reader is
// better served by a neighbouring variant than by English.
func Negotiate(chosen Locale, acceptLanguage string) Locale {
	if chosen != "" {
		if match, ok := match(string(chosen)); ok {
			return match
		}
	}
	for _, candidate := range acceptedLanguages(acceptLanguage) {
		if match, ok := match(candidate); ok {
			return match
		}
	}
	return Default
}

func match(tag string) (Locale, bool) {
	tag = strings.TrimSpace(tag)
	if tag == "" || tag == "*" {
		return "", false
	}
	supported := Supported()
	for _, catalog := range supported {
		if strings.EqualFold(string(catalog.Locale), tag) {
			return catalog.Locale, true
		}
	}
	base := baseLanguage(Locale(tag))
	for _, catalog := range supported {
		if baseLanguage(catalog.Locale) == base {
			return catalog.Locale, true
		}
	}
	return "", false
}

// acceptedLanguages is an Accept-Language header's ranges, best first. A range
// with q=0 is one the reader refuses, and a malformed quality is read as 1,
// which is what the header means when it carries none.
func acceptedLanguages(header string) []string {
	type ranged struct {
		tag     string
		quality float64
	}
	var ranges []ranged
	for _, part := range strings.Split(header, ",") {
		tag, parameters, _ := strings.Cut(strings.TrimSpace(part), ";")
		tag = strings.TrimSpace(tag)
		if tag == "" || len(tag) > 35 {
			continue
		}
		quality := 1.0
		if value, ok := strings.CutPrefix(strings.TrimSpace(parameters), "q="); ok {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				quality = parsed
			}
		}
		if quality <= 0 {
			continue
		}
		ranges = append(ranges, ranged{tag: tag, quality: quality})
	}
	sort.SliceStable(ranges, func(left, right int) bool { return ranges[left].quality > ranges[right].quality })
	result := make([]string, 0, len(ranges))
	for _, value := range ranges {
		result = append(result, value.tag)
	}
	return result
}
