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
