package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestSlackbotResponsePhrasesAreWholeWordsWithoutCase(t *testing.T) {
	triggers, replies, err := NormalizeSlackbotResponse([]string{" lunch,  what's  for LUNCH , Lunch", "wifi password"}, []string{"", " Tacos at noon! ", "Ask Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(triggers, "|") != "lunch|what's for LUNCH|wifi password" || strings.Join(replies, "|") != "Tacos at noon!|Ask Ada" {
		t.Fatalf("triggers=%q replies=%q", triggers, replies)
	}
	response := SlackbotResponse{Triggers: triggers, Replies: replies}
	for text, want := range map[string]bool{
		"Lunch?":                      true,
		"so, what's for lunch today":  true,
		"what is for lunch":           true,
		"lunchtime":                   false,
		"What's the WiFi password?":   true,
		"the wifi is down, password?": false,
		"Café LUNCH":                  true,
		"":                            false,
	} {
		if got := response.Matches(text); got != want {
			t.Errorf("%q matched=%v, want %v", text, got, want)
		}
	}
	if response.Reply("E1") != response.Reply("E1") {
		t.Fatal("the same message chose two replies")
	}
	for name, input := range map[string][2][]string{
		"no phrase":        {{" , "}, {"x"}},
		"no reply":         {{"x"}, {" "}},
		"a long phrase":    {{strings.Repeat("x", 201)}, {"x"}},
		"a long reply":     {{"x"}, {strings.Repeat("x", 4001)}},
		"too many replies": {{"x"}, strings.Split(strings.Repeat("r,", 51), ",")[:51]},
	} {
		if _, _, err := NormalizeSlackbotResponse(input[0], input[1]); !errors.Is(err, ErrInvalidSlackbotResponse) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
