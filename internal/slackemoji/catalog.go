// Package slackemoji exposes the standard emoji catalog Slack identifies as
// the source for its colon-code representation.
package slackemoji

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	Revision          = "097705020bcf82331c9ef10df3425aad15f5043c"
	SourceSHA256      = "1d602e65be88772bf8cc368ce16b855d719eeddbafe128d471b80203f494d29f"
	CategoriesVersion = Revision
)

//go:embed catalog.json
var rawCatalog []byte

type Emoji struct {
	Name        string   `json:"n"`
	Description string   `json:"d"`
	Unified     string   `json:"u"`
	Category    string   `json:"c"`
	Aliases     []string `json:"a"`
	SortOrder   int      `json:"o"`
	SkinTones   bool     `json:"s"`
}

type Category struct {
	Name       string   `json:"name"`
	EmojiNames []string `json:"emoji_names"`
}

var (
	loadOnce sync.Once
	all      []Emoji
	byName   map[string]Emoji
)

func load() {
	loadOnce.Do(func() {
		if err := json.Unmarshal(rawCatalog, &all); err != nil {
			panic("decode pinned Slack emoji catalog: " + err.Error())
		}
		sort.SliceStable(all, func(left, right int) bool {
			return all[left].SortOrder < all[right].SortOrder
		})
		byName = make(map[string]Emoji, len(all)*2)
		for _, emoji := range all {
			byName[emoji.Name] = emoji
			for _, alias := range emoji.Aliases {
				if _, exists := byName[alias]; !exists {
					byName[alias] = emoji
				}
			}
		}
	})
}

func All() []Emoji {
	load()
	result := make([]Emoji, len(all))
	copy(result, all)
	return result
}

func Lookup(name string) (Emoji, bool) {
	load()
	emoji, ok := byName[strings.ToLower(strings.Trim(strings.TrimSpace(name), ":"))]
	return emoji, ok
}

func Categories() []Category {
	load()
	order := make([]string, 0, 10)
	values := make(map[string][]string, 10)
	for _, emoji := range all {
		if emoji.Category == "" || emoji.Category == "Component" {
			continue
		}
		if _, exists := values[emoji.Category]; !exists {
			order = append(order, emoji.Category)
		}
		values[emoji.Category] = append(values[emoji.Category], emoji.Name)
	}
	result := make([]Category, 0, len(order))
	for _, name := range order {
		result = append(result, Category{Name: name, EmojiNames: values[name]})
	}
	return result
}

func Search(query string, limit int) []Emoji {
	load()
	if limit <= 0 {
		return nil
	}
	query = strings.ToLower(strings.Trim(strings.TrimSpace(query), ":"))
	if query == "" {
		result := make([]Emoji, min(limit, len(all)))
		copy(result, all[:len(result)])
		return result
	}
	type ranked struct {
		emoji Emoji
		rank  int
	}
	matches := make([]ranked, 0, 64)
	for _, emoji := range all {
		if rank := searchRank(emoji, query); rank >= 0 {
			matches = append(matches, ranked{emoji: emoji, rank: rank})
		}
	}
	// Order by how directly each emoji answers the query — its exact name, then
	// its exact alias, then a name that begins with it, and so on down the tiers
	// searchRank assigns. Within a tier the shorter name wins, because the closer
	// the whole name is to what was typed the more likely it is what was meant, and
	// equal-length names break alphabetically for a stable, predictable list.
	// SliceStable leaves the pinned catalog order as the final tiebreak, so the
	// result never depends on iteration order.
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].rank != matches[right].rank {
			return matches[left].rank < matches[right].rank
		}
		leftName, rightName := matches[left].emoji.Name, matches[right].emoji.Name
		if len(leftName) != len(rightName) {
			return len(leftName) < len(rightName)
		}
		return leftName < rightName
	})
	count := min(limit, len(matches))
	result := make([]Emoji, count)
	for index := 0; index < count; index++ {
		result[index] = matches[index].emoji
	}
	return result
}

// searchRank scores how directly an emoji answers a query, lowest first: an
// exact name, then an exact alias, a name that begins with the query, an alias
// that does, the query appearing anywhere in the name, then in an alias, and
// finally in the description. A name match always outranks the same kind of alias
// match, which is what keeps :smile: above an emoji that merely lists "smile"
// among its aliases. It returns -1 when nothing matches.
func searchRank(emoji Emoji, query string) int {
	if emoji.Name == query {
		return 0
	}
	for _, alias := range emoji.Aliases {
		if alias == query {
			return 1
		}
	}
	if strings.HasPrefix(emoji.Name, query) {
		return 2
	}
	for _, alias := range emoji.Aliases {
		if strings.HasPrefix(alias, query) {
			return 3
		}
	}
	if strings.Contains(emoji.Name, query) {
		return 4
	}
	for _, alias := range emoji.Aliases {
		if strings.Contains(alias, query) {
			return 5
		}
	}
	if strings.Contains(strings.ToLower(emoji.Description), query) {
		return 6
	}
	return -1
}

func Unicode(emoji Emoji) string {
	if emoji.Unified == "" {
		return ""
	}
	var value strings.Builder
	for _, encoded := range strings.Split(emoji.Unified, "-") {
		codePoint, err := strconv.ParseInt(encoded, 16, 32)
		if err != nil || codePoint < 0 || codePoint > utf8.MaxRune {
			return ""
		}
		value.WriteRune(rune(codePoint))
	}
	return value.String()
}

// ParseReactionName accepts Slack's documented reaction modifier syntax,
// `<base>::skin-tone-<2..6>`, and rejects modifiers for emoji that do not
// declare skin variations in the pinned iamcal source.
func ParseReactionName(value string) (Emoji, int, bool) {
	value = strings.ToLower(strings.Trim(strings.TrimSpace(value), ":"))
	base, modifier, hasModifier := strings.Cut(value, "::skin-tone-")
	emoji, ok := Lookup(base)
	if !ok {
		return Emoji{}, 0, false
	}
	if !hasModifier {
		return emoji, 0, true
	}
	tone, err := strconv.Atoi(modifier)
	if err != nil || tone < 2 || tone > 6 || !emoji.SkinTones {
		return Emoji{}, 0, false
	}
	return emoji, tone, true
}

func ReactionUnicode(value string) (string, bool) {
	emoji, tone, ok := ParseReactionName(value)
	if !ok {
		return "", false
	}
	rendered := Unicode(emoji)
	if rendered == "" {
		return "", false
	}
	if tone != 0 {
		rendered += string(rune(0x1F3FB + tone - 2))
	}
	return rendered, true
}
