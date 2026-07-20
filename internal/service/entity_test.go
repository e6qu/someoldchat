package service

import (
	"context"
	"errors"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestEntityWorkObjectResponsesValidateRequiredRelationships(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	service := Messages{Store: store}
	ctx := context.Background()

	if err := service.PresentEntityDetails(ctx, "T1", "U1", "trigger-details", `{"entity_type":"slack#/entities/file"}`, true, "https://example.test/login", ""); err != nil {
		t.Fatal(err)
	}
	if err := service.PresentEntityComments(ctx, "T1", "U1", "trigger-comments", `[{"id":"comment-1","can_delete":true}]`, "next", true, "delete-action", false, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := service.AcknowledgeEntityCommentAction(ctx, "T1", "U1", "trigger-comment", `{"id":"comment-1","value":"saved"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := service.PresentEntityComments(ctx, "T1", "U1", "trigger-comments", `[{"id":"comment-1","can_delete":true}]`, "", false, "", false, "", ""); !errors.Is(err, ErrInvalidEntity) {
		t.Fatalf("missing delete action error=%v", err)
	}
	if err := service.PresentEntityDetails(ctx, "T1", "U1", "", "{}", false, "", ""); !errors.Is(err, ErrInvalidEntity) {
		t.Fatalf("missing trigger error=%v", err)
	}
}
