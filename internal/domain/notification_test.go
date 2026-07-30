package domain

import (
	"reflect"
	"testing"
)

func TestNotificationKeywordsNormalizeAndMatchSlackBoundaries(t *testing.T) {
	keywords := NormalizeNotificationKeywords([]string{" Release ", "customer escalation", "RELEASE", ""})
	if want := []string{"customer escalation", "release"}; !reflect.DeepEqual(keywords, want) {
		t.Fatalf("normalized keywords = %v, want %v", keywords, want)
	}
	for _, testCase := range []struct {
		text string
		want bool
	}{
		{text: "The RELEASE is ready", want: true},
		{text: "A customer escalation arrived", want: true},
		{text: "releases are ready", want: false},
		{text: "prerelease is ready", want: false},
		{text: "ordinary update", want: false},
	} {
		if got := MatchesNotificationKeyword(testCase.text, keywords); got != testCase.want {
			t.Errorf("MatchesNotificationKeyword(%q) = %v, want %v", testCase.text, got, testCase.want)
		}
	}
}

func TestConversationNotificationEffectiveLevel(t *testing.T) {
	workspace := DefaultWorkspaceNotificationPreferences("T1", "U1")
	workspace.Level = NotificationAll
	override := DefaultConversationNotificationPreferences("T1", "U1", "C1")
	if got := override.EffectiveLevel(workspace); got != NotificationAll {
		t.Fatalf("inherited level = %q, want %q", got, NotificationAll)
	}
	override.Level = NotificationMute
	if got := override.EffectiveLevel(workspace); got != NotificationMute {
		t.Fatalf("overridden level = %q, want %q", got, NotificationMute)
	}
}
