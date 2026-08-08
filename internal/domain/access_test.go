package domain

import "testing"

// The access model is a closed set on purpose: every layer compares it, and a
// level or entity nobody declared used to be representable everywhere. These
// assert the two properties the rest of the system leans on.
func TestAccessLevelRanksAndRejectsWhatNobodyDeclared(t *testing.T) {
	ordered := []AccessLevel{AccessRead, AccessWrite, AccessOwner}
	for index, level := range ordered {
		if !level.Valid() {
			t.Fatalf("%q is not valid", level)
		}
		if level.Rank() != index+1 {
			t.Fatalf("%q ranks %d, want %d", level, level.Rank(), index+1)
		}
	}
	// A value cast in from a wire, a database row written by a future version,
	// or a typo must rank below every real grant rather than satisfying a
	// requirement.
	for _, level := range []AccessLevel{"", "admin", "Owner", "read "} {
		if level.Valid() {
			t.Fatalf("%q was accepted as a level", level)
		}
		if level.Rank() != 0 {
			t.Fatalf("%q ranks %d, want 0", level, level.Rank())
		}
		if level.Rank() >= AccessRead.Rank() {
			t.Fatalf("%q satisfies a read requirement", level)
		}
	}
}

func TestGrantEntityKnowsWhichGrantsReachAChannel(t *testing.T) {
	if !GrantUser.Valid() || !GrantChannel.Valid() || !GrantChannelCanvas.Valid() {
		t.Fatal("a declared entity was rejected")
	}
	if GrantUser.ReachesChannelMembers() {
		t.Fatal("a grant on one person was treated as reaching a channel")
	}
	if !GrantChannel.ReachesChannelMembers() || !GrantChannelCanvas.ReachesChannelMembers() {
		t.Fatal("a channel grant was treated as reaching one person")
	}
	for _, entity := range []GrantEntity{"", "group", "User"} {
		if entity.Valid() {
			t.Fatalf("%q was accepted as an entity", entity)
		}
		if entity.ReachesChannelMembers() {
			t.Fatalf("%q was treated as reaching a channel", entity)
		}
	}
}
