package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// seedProfileFieldWorkspace builds a workspace with an administrator (U1), two
// plain members (U2, U3), and a stranger who is not a member (U9).
func seedProfileFieldWorkspace(t *testing.T) (*memory.Store, Messages) {
	t.Helper()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.UserID{"U1", "U2", "U3"} {
		if err := s.SeedUser(domain.User{ID: id, WorkspaceID: "T1", Name: string(id)}); err != nil {
			t.Fatal(err)
		}
	}
	seedWorkspaceAdmin(t, s, "T1", "U1")
	return s, Messages{Store: s}
}

func TestWorkspaceProfileFieldsAreAdminDefinedAndMemberSet(t *testing.T) {
	ctx := context.Background()
	_, messages := seedProfileFieldWorkspace(t)

	// A plain member cannot define a field.
	if _, err := messages.SetWorkspaceProfileField(ctx, "T1", "U2", domain.ProfileFieldDefinition{Label: "Pronouns", Type: domain.ProfileFieldText}); !errors.Is(err, ErrNotWorkspaceAdmin) {
		t.Fatalf("member define = %v, want ErrNotWorkspaceAdmin", err)
	}
	// A stranger cannot either, and the store never records the attempt.
	if _, err := messages.SetWorkspaceProfileField(ctx, "T1", "U9", domain.ProfileFieldDefinition{Label: "Pronouns", Type: domain.ProfileFieldText}); err == nil {
		t.Fatal("stranger define returned nil error")
	}

	// The administrator defines a text field and an options_list field.
	pronouns, err := messages.SetWorkspaceProfileField(ctx, "T1", "U1", domain.ProfileFieldDefinition{Label: "Pronouns", Type: domain.ProfileFieldText, Ordering: 1})
	if err != nil {
		t.Fatalf("admin define text: %v", err)
	}
	if pronouns.ID == "" {
		t.Fatal("defined field has no id")
	}
	team, err := messages.SetWorkspaceProfileField(ctx, "T1", "U1", domain.ProfileFieldDefinition{Label: "Team", Type: domain.ProfileFieldOptionsList, PossibleValues: []string{"red", "blue"}, Ordering: 2})
	if err != nil {
		t.Fatalf("admin define options: %v", err)
	}
	// An options_list with no options is refused.
	if _, err := messages.SetWorkspaceProfileField(ctx, "T1", "U1", domain.ProfileFieldDefinition{Label: "Empty", Type: domain.ProfileFieldOptionsList}); !errors.Is(err, ErrInvalidProfileField) {
		t.Fatalf("empty options define = %v, want ErrInvalidProfileField", err)
	}

	// Any member reads the definitions, ordered.
	definitions, err := messages.WorkspaceProfileFields(ctx, "T1", "U2")
	if err != nil || len(definitions) != 2 || definitions[0].ID != pronouns.ID || definitions[1].ID != team.ID {
		t.Fatalf("definitions=%+v err=%v", definitions, err)
	}

	// A member sets their own values, validated against the field types.
	if err := messages.SetUserProfileFields(ctx, "T1", "U2", "U2", []domain.UserProfileFieldValue{{FieldID: pronouns.ID, Value: "she/her"}, {FieldID: team.ID, Value: "blue"}}); err != nil {
		t.Fatalf("set values: %v", err)
	}
	// A value the options_list does not offer is refused.
	if err := messages.SetUserProfileFields(ctx, "T1", "U2", "U2", []domain.UserProfileFieldValue{{FieldID: team.ID, Value: "green"}}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("bad option = %v, want ErrInvalidProfile", err)
	}
	// A value for an undefined field is refused.
	if err := messages.SetUserProfileFields(ctx, "T1", "U2", "U2", []domain.UserProfileFieldValue{{FieldID: "Xf-nope", Value: "x"}}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("undefined field = %v, want ErrInvalidProfile", err)
	}
	// A member cannot set another member's fields.
	if err := messages.SetUserProfileFields(ctx, "T1", "U2", "U3", []domain.UserProfileFieldValue{{FieldID: pronouns.ID, Value: "they/them"}}); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("set other's fields = %v, want ErrInvalidProfile", err)
	}

	values, err := messages.UserProfileFields(ctx, "T1", "U2", "U2")
	if err != nil || len(values) != 2 {
		t.Fatalf("read own values=%+v err=%v", values, err)
	}

	// Deleting the field removes it and its values.
	if err := messages.DeleteWorkspaceProfileField(ctx, "T1", "U1", pronouns.ID); err != nil {
		t.Fatalf("delete field: %v", err)
	}
	after, err := messages.UserProfileFields(ctx, "T1", "U2", "U2")
	if err != nil || len(after) != 1 || after[0].FieldID != team.ID {
		t.Fatalf("values after delete=%+v err=%v", after, err)
	}
	// A plain member cannot delete a field.
	if err := messages.DeleteWorkspaceProfileField(ctx, "T1", "U2", team.ID); !errors.Is(err, ErrNotWorkspaceAdmin) {
		t.Fatalf("member delete = %v, want ErrNotWorkspaceAdmin", err)
	}
}

// TestHiddenProfileFieldIsFilteredFromOtherMembers covers the visibility rule: a
// hidden field's value is seen by its owner and by administrators, but filtered
// out for a plain member reading someone else's profile.
func TestHiddenProfileFieldIsFilteredFromOtherMembers(t *testing.T) {
	ctx := context.Background()
	_, messages := seedProfileFieldWorkspace(t)
	hidden, err := messages.SetWorkspaceProfileField(ctx, "T1", "U1", domain.ProfileFieldDefinition{Label: "Home address", Type: domain.ProfileFieldText, IsHidden: true})
	if err != nil {
		t.Fatalf("define hidden: %v", err)
	}
	if err := messages.SetUserProfileFields(ctx, "T1", "U2", "U2", []domain.UserProfileFieldValue{{FieldID: hidden.ID, Value: "1 Main St"}}); err != nil {
		t.Fatalf("set hidden value: %v", err)
	}

	// The owner sees it.
	if own, err := messages.UserProfileFields(ctx, "T1", "U2", "U2"); err != nil || len(own) != 1 {
		t.Fatalf("owner read=%+v err=%v", own, err)
	}
	// The administrator sees it.
	if adminRead, err := messages.UserProfileFields(ctx, "T1", "U1", "U2"); err != nil || len(adminRead) != 1 {
		t.Fatalf("admin read=%+v err=%v", adminRead, err)
	}
	// A plain member reading U2 does not.
	if otherRead, err := messages.UserProfileFields(ctx, "T1", "U3", "U2"); err != nil || len(otherRead) != 0 {
		t.Fatalf("member read of hidden=%+v err=%v", otherRead, err)
	}
}

// TestProfileFieldOperationsRefuseAStranger keeps the workspace guards on the
// read and value-setting paths load-bearing: a caller who is not a member of the
// workspace is refused before any field is read or written.
func TestProfileFieldOperationsRefuseAStranger(t *testing.T) {
	ctx := context.Background()
	_, messages := seedProfileFieldWorkspace(t)
	// A real field exists, so a stranger's refusal is about their standing rather
	// than about an undefined field — which keeps the workspace guard on the
	// value-setting path load-bearing.
	field, err := messages.SetWorkspaceProfileField(ctx, "T1", "U1", domain.ProfileFieldDefinition{Label: "Pronouns", Type: domain.ProfileFieldText})
	if err != nil {
		t.Fatalf("define field: %v", err)
	}
	// A stranger cannot read a member's profile fields.
	if _, err := messages.UserProfileFields(ctx, "T1", "U9", "U2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stranger read values = %v, want ErrNotFound", err)
	}
	// A stranger cannot list the workspace's field definitions.
	if _, err := messages.WorkspaceProfileFields(ctx, "T1", "U9"); err == nil {
		t.Fatal("stranger list definitions returned nil error")
	}
	// A stranger cannot set a valid field's value: the refusal is the workspace
	// guard, not the field validation, so the value passed would otherwise be
	// accepted.
	if err := messages.SetUserProfileFields(ctx, "T1", "U9", "U9", []domain.UserProfileFieldValue{{FieldID: field.ID, Value: "she/her"}}); err == nil {
		t.Fatal("stranger set values returned nil error")
	}
}
