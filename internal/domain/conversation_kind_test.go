package domain

import "testing"

// ConversationKindFor decides one kind from the three stored flags, and
// ConversationKindFlags reverses it. The stores call both, so the pair must
// round-trip every kind.
func TestConversationKindRoundTripsThroughTheStoredFlags(t *testing.T) {
	for _, kind := range []ConversationType{
		ConversationTypePublic, ConversationTypePrivate, ConversationTypeIM, ConversationTypeMPIM,
	} {
		private, direct, groupDirect := ConversationKindFlags(kind)
		if got := ConversationKindFor(private, direct, groupDirect); got != kind {
			t.Fatalf("%q became %q", kind, got)
		}
	}
}

// The flags allow eight combinations. Four mean nothing, and every layer read
// them in its own order before. ConversationKindFor gives one answer for all
// eight: a conversation between people stays one whatever else the row says,
// and the group flag beats the one-to-one flag.
func TestConversationKindForDecidesEveryCombination(t *testing.T) {
	for _, testCase := range []struct {
		name                   string
		private, direct, group bool
		want                   ConversationType
	}{
		{"public channel", false, false, false, ConversationTypePublic},
		{"private channel", true, false, false, ConversationTypePrivate},
		{"one-to-one", true, true, false, ConversationTypeIM},
		{"group", true, false, true, ConversationTypeMPIM},
		{"public one-to-one", false, true, false, ConversationTypeIM},
		{"public group", false, false, true, ConversationTypeMPIM},
		{"both direct flags", false, true, true, ConversationTypeMPIM},
		{"everything set", true, true, true, ConversationTypeMPIM},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			kind := ConversationKindFor(testCase.private, testCase.direct, testCase.group)
			if kind != testCase.want {
				t.Fatalf("kind = %q, want %q", kind, testCase.want)
			}
			conversation := Conversation{Kind: kind}
			matched := 0
			for _, candidate := range []ConversationType{
				ConversationTypePublic, ConversationTypePrivate, ConversationTypeIM, ConversationTypeMPIM,
			} {
				if MatchesConversationType(conversation, candidate) {
					matched++
				}
			}
			if matched != 1 {
				t.Fatalf("%d types matched, want one", matched)
			}
		})
	}
}
