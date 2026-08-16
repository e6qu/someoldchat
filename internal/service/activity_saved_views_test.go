package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func activitySavedViewWorld(t *testing.T) (context.Context, Messages, *memory.Store) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	return ctx, Messages{Store: repository}, repository
}

// A saved view is one member's named filter: read back through their
// preferences, invisible to anyone else, and unaffected by a layout change.
func TestActivitySavedViewsAreCreatedListedAndDeletedPerMember(t *testing.T) {
	ctx, messages, _ := activitySavedViewWorld(t)
	view, err := messages.CreateActivitySavedView(ctx, "T1", "U1", "Important", []domain.ActivityKind{domain.ActivityMention, domain.ActivityDM, domain.ActivityMention})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Kinds) != 2 {
		t.Fatalf("view kinds = %v, want the duplicate mention collapsed to two", view.Kinds)
	}
	prefs, err := messages.ActivityPreferences(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	if len(prefs.SavedViews) != 1 || prefs.SavedViews[0].Name != "Important" || prefs.SavedViews[0].ID != view.ID {
		t.Fatalf("saved views = %+v, want the created view", prefs.SavedViews)
	}
	if other, err := messages.ActivityPreferences(ctx, "T1", "U2"); err != nil || len(other.SavedViews) != 0 {
		t.Fatalf("another member saw %+v err=%v, want none", other.SavedViews, err)
	}
	// Changing the layout must not clobber the saved views.
	if _, err := messages.SetActivityPreferences(ctx, "T1", "U1", domain.ActivityDense); err != nil {
		t.Fatal(err)
	}
	afterLayout, err := messages.ActivityPreferences(ctx, "T1", "U1")
	if err != nil || len(afterLayout.SavedViews) != 1 || afterLayout.Layout != domain.ActivityDense {
		t.Fatalf("prefs after layout change = %+v err=%v, want dense with the view kept", afterLayout, err)
	}
	// The view belongs to its maker; another member cannot delete it, and a
	// bogus id is refused like a missing one.
	if err := messages.DeleteActivitySavedView(ctx, "T1", "U2", view.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another member deleted the view: %v", err)
	}
	if err := messages.DeleteActivitySavedView(ctx, "T1", "U1", "not-a-real-view"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("bogus id delete: %v", err)
	}
	if err := messages.DeleteActivitySavedView(ctx, "T1", "U1", view.ID); err != nil {
		t.Fatal(err)
	}
	if gone, err := messages.ActivityPreferences(ctx, "T1", "U1"); err != nil || len(gone.SavedViews) != 0 {
		t.Fatalf("saved views after delete = %+v err=%v, want none", gone.SavedViews, err)
	}
}

func TestActivitySavedViewValidationAndLimit(t *testing.T) {
	ctx, messages, _ := activitySavedViewWorld(t)
	for name, kinds := range map[string][]domain.ActivityKind{
		"empty name":     {domain.ActivityDM},
		"no kinds":       nil,
		"bad kind":       {"nonsense"},
		"over-long name": {domain.ActivityDM},
	} {
		label := "Important"
		switch name {
		case "empty name":
			label = "   "
		case "over-long name":
			label = strings.Repeat("x", domain.ActivitySavedViewNameLimit+1)
		}
		if _, err := messages.CreateActivitySavedView(ctx, "T1", "U1", label, kinds); !errors.Is(err, ErrInvalidActivitySavedView) {
			t.Fatalf("%s = %v, want ErrInvalidActivitySavedView", name, err)
		}
	}
	for index := 0; index < domain.ActivitySavedViewLimit; index++ {
		if _, err := messages.CreateActivitySavedView(ctx, "T1", "U1", "view"+strconv.Itoa(index), []domain.ActivityKind{domain.ActivityDM}); err != nil {
			t.Fatalf("creating view %d: %v", index, err)
		}
	}
	if _, err := messages.CreateActivitySavedView(ctx, "T1", "U1", "one too many", []domain.ActivityKind{domain.ActivityDM}); err == nil {
		t.Fatal("created a view past the per-member limit")
	}
}

// TestDeactivatedMembersLoseTheirActivitySavedViewGuards makes the
// workspace-membership guard load-bearing: the store scopes a view to its owner
// but records nothing about their standing, so only the service's
// authorizeWorkspace refuses a removed member.
func TestDeactivatedMembersLoseTheirActivitySavedViewGuards(t *testing.T) {
	ctx, messages, repository := activitySavedViewWorld(t)
	view, err := messages.CreateActivitySavedView(ctx, "T1", "U1", "Important", []domain.ActivityKind{domain.ActivityDM})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SetUserDeleted(ctx, "T1", "U1", true, events.Event{ID: "gone", WorkspaceID: "T1", Topic: "user.removed", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.CreateActivitySavedView(ctx, "T1", "U1", "New", []domain.ActivityKind{domain.ActivityMention}); err == nil {
		t.Fatal("a deactivated member created a saved view")
	}
	if err := messages.DeleteActivitySavedView(ctx, "T1", "U1", view.ID); err == nil {
		t.Fatal("a deactivated member deleted a saved view")
	}
	if _, err := messages.ActivityPreferences(ctx, "T1", "U1"); err == nil {
		t.Fatal("a deactivated member read activity preferences")
	}
}
