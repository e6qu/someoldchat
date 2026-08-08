package domain

import "testing"

// Kind is the only place the three booleans are interpreted, so the precedence
// it applies is worth pinning — including for the four combinations that mean
// nothing, which the struct still permits and which used to be read differently
// depending on which layer looked.
func TestConversationKindDecidesOneAnswerForEveryCombination(t *testing.T) {
	for _, testCase := range []struct {
		name                   string
		private, direct, group bool
		want                   ConversationType
		directOrGroup          bool
	}{
		{"public channel", false, false, false, ConversationTypePublic, false},
		{"private channel", true, false, false, ConversationTypePrivate, false},
		{"one-to-one", false, true, false, ConversationTypeIM, true},
		{"group DM", false, false, true, ConversationTypeMPIM, true},
		// The four that mean nothing. A conversation between people stays one
		// whatever else is set, and the group flag is the more specific claim.
		{"private one-to-one", true, true, false, ConversationTypeIM, true},
		{"private group", true, false, true, ConversationTypeMPIM, true},
		{"both direct flags", false, true, true, ConversationTypeMPIM, true},
		{"everything set", true, true, true, ConversationTypeMPIM, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			conversation := Conversation{IsPrivate: testCase.private, IsDirect: testCase.direct, IsGroupDirect: testCase.group}
			if kind := conversation.Kind(); kind != testCase.want {
				t.Fatalf("kind = %q, want %q", kind, testCase.want)
			}
			if conversation.IsDirectOrGroup() != testCase.directOrGroup {
				t.Fatalf("IsDirectOrGroup = %v, want %v", conversation.IsDirectOrGroup(), testCase.directOrGroup)
			}
			// Nothing is two kinds: exactly one type matches.
			matched := 0
			for _, candidate := range []ConversationType{ConversationTypePublic, ConversationTypePrivate, ConversationTypeIM, ConversationTypeMPIM} {
				if MatchesConversationType(conversation, candidate) {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("%d types matched, want exactly one", matched)
			}
		})
	}
}
