package slackemoji

import "testing"

func TestPinnedCatalogIsCompleteAndSearchable(t *testing.T) {
	values := All()
	if len(values) != 1911 {
		t.Fatalf("catalog has %d entries, want 1911", len(values))
	}
	tada, ok := Lookup("tada")
	if !ok || Unicode(tada) != "🎉" || tada.Category != "Activities" {
		t.Fatalf("tada=%+v ok=%v unicode=%q", tada, ok, Unicode(tada))
	}
	thumb, ok := Lookup("+1")
	if !ok || thumb.Name != "+1" {
		t.Fatalf("+1 alias=%+v ok=%v", thumb, ok)
	}
	if _, ok := Lookup("thumbsup"); !ok {
		t.Fatal("thumbsup alias is missing")
	}
	if rendered, ok := ReactionUnicode("wave::skin-tone-3"); !ok || rendered != "👋🏼" {
		t.Fatalf("skin-tone reaction=%q ok=%v, want 👋🏼", rendered, ok)
	}
	if _, ok := ReactionUnicode("tada::skin-tone-3"); ok {
		t.Fatal("an emoji without skin variations accepted a skin tone")
	}
	categories := Categories()
	if len(categories) != 9 || categories[0].Name == "" || len(categories[0].EmojiNames) == 0 {
		t.Fatalf("categories=%+v", categories)
	}
	if matches := Search("party", 20); len(matches) == 0 {
		t.Fatal("party search returned no emoji")
	}
	if matches := Search("tad", 10); len(matches) == 0 || matches[0].Name != "tada" {
		t.Fatalf("Search(tad)=%+v, want a name prefix before description matches", matches)
	}
	if matches := Search("wave", 10); len(matches) == 0 || matches[0].Name != "wave" {
		t.Fatalf("Search(wave)=%+v, want an exact name first", matches)
	}
}

// indexOf returns the position of a name in a result, or -1.
func indexOf(matches []Emoji, name string) int {
	for index, emoji := range matches {
		if emoji.Name == name {
			return index
		}
	}
	return -1
}

// TestSearchOrdersByHowDirectlyItAnswersTheQuery covers the autocomplete ranking:
// an exact name beats a prefix, and within a tier the shorter, then alphabetically
// earlier, name comes first — so what a member most likely meant leads the list
// rather than whatever the pinned catalog happens to order first.
func TestSearchOrdersByHowDirectlyItAnswersTheQuery(t *testing.T) {
	// A prefix shared by several emoji: the shortest name comes first, and a
	// shorter match precedes a longer one that merely extends it.
	matches := Search("smi", 20)
	if len(matches) == 0 || matches[0].Name != "smile" {
		t.Fatalf("Search(smi)[0]=%q, want smile (shortest name-prefix, alphabetically first)", firstName(matches))
	}
	if smile, smiley := indexOf(matches, "smile"), indexOf(matches, "smiley"); smile < 0 || smiley < 0 || smile >= smiley {
		t.Fatalf("smile at %d, smiley at %d: the shorter name must come first", smile, smiley)
	}

	// An exact name outranks every prefix, however early the prefix sits in the
	// catalog: :grin: leads a query that also prefixes :grinning:.
	grin := Search("grin", 20)
	if len(grin) == 0 || grin[0].Name != "grin" {
		t.Fatalf("Search(grin)[0]=%q, want the exact name grin first", firstName(grin))
	}
	if g, gg := indexOf(grin, "grin"), indexOf(grin, "grinning"); g < 0 || gg < 0 || g >= gg {
		t.Fatalf("grin at %d, grinning at %d: the exact name must precede the prefix", g, gg)
	}

	// The empty query still returns the pinned catalog order, unchanged.
	if browse := Search("", 5); len(browse) != 5 || browse[0].Name != All()[0].Name {
		t.Fatalf("Search(\"\") did not return the catalog head in order: %q", firstName(browse))
	}
}

func firstName(matches []Emoji) string {
	if len(matches) == 0 {
		return ""
	}
	return matches[0].Name
}
