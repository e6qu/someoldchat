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
	buckets := [4][]Emoji{}
	for _, emoji := range all {
		if rank := searchRank(emoji, query); rank >= 0 {
			buckets[rank] = append(buckets[rank], emoji)
		}
	}
	result := make([]Emoji, 0, min(limit, len(all)))
	for _, bucket := range buckets {
		for _, emoji := range bucket {
			result = append(result, emoji)
			if len(result) == limit {
				return result
			}
		}
	}
	return result
}

func searchRank(emoji Emoji, query string) int {
	if emoji.Name == query {
		return 0
	}
	for _, alias := range emoji.Aliases {
		if alias == query {
			return 0
		}
	}
	if strings.HasPrefix(emoji.Name, query) {
		return 1
	}
	for _, alias := range emoji.Aliases {
		if strings.HasPrefix(alias, query) {
			return 1
		}
	}
	if strings.Contains(emoji.Name, query) {
		return 2
	}
	for _, alias := range emoji.Aliases {
		if strings.Contains(alias, query) {
			return 2
		}
	}
	if strings.Contains(strings.ToLower(emoji.Description), query) {
		return 3
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
