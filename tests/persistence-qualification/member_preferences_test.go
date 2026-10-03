package qualification

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// memberPreferencesAreKeptPerMember holds the record behind preferences that
// follow a member to every client: a value replaces the one before it, an
// empty value removes it, each member keeps their own, a member keeps at most
// domain.MemberPreferenceLimit, and a member of another workspace has none
// to keep here.
func memberPreferencesAreKeptPerMember(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	at := time.Unix(1_700_000_000, 0).UTC()
	other := f.secondMember(t, ctx)
	for _, preference := range []struct{ name, value string }{{"theme", "dark"}, {"theme", "light"}, {"section-sort:starred", "recent"}, {"composer-enter", "newline"}, {"composer-enter", ""}} {
		if err := f.repository.SetMemberPreference(ctx, f.workspaceID, f.userID, preference.name, preference.value, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.repository.SetMemberPreference(ctx, f.workspaceID, other, "theme", "dark", at); err != nil {
		t.Fatal(err)
	}
	mine, err := f.repository.MemberPreferences(ctx, f.workspaceID, f.userID)
	if err != nil || !reflect.DeepEqual(mine, map[string]string{"theme": "light", "section-sort:starred": "recent"}) {
		t.Fatalf("preferences=%v err=%v", mine, err)
	}
	if err := f.repository.SetMemberPreference(ctx, domain.WorkspaceID("T-other-"+f.suffix), f.userID, "theme", "dark", at); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("keeping a preference in another workspace: %v", err)
	}

	for index := len(mine); index < domain.MemberPreferenceLimit; index++ {
		if err := f.repository.SetMemberPreference(ctx, f.workspaceID, f.userID, fmt.Sprintf("filler-%d", index), "x", at); err != nil {
			t.Fatalf("preference %d: %v", index, err)
		}
	}
	if err := f.repository.SetMemberPreference(ctx, f.workspaceID, f.userID, "one-too-many", "x", at); !errors.Is(err, domain.ErrInvalidMemberPreference) {
		t.Fatalf("a preference past the limit: %v", err)
	}
	if err := f.repository.SetMemberPreference(ctx, f.workspaceID, f.userID, "theme", "dark", at); err != nil {
		t.Fatalf("replacing one at the limit: %v", err)
	}
	if theirs, err := f.repository.MemberPreferences(ctx, f.workspaceID, other); err != nil || !reflect.DeepEqual(theirs, map[string]string{"theme": "dark"}) {
		t.Fatalf("another member's=%v err=%v", theirs, err)
	}
}
