package slack

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// A member's own workflow, built with no developer app, is administered like
// any other: admin.workflows.search finds it and reports no app for it.
func TestAdminWorkflowSearchFindsAMembersOwnWorkflow(t *testing.T) {
	handler, repository := testUserHandlerWithStore()
	messages := service.Messages{Store: repository}
	created, err := messages.CreateWorkflow(context.Background(), "T1", "U1", domain.WorkflowDefinition{Title: "Welcome wagon"})
	if err != nil {
		t.Fatal(err)
	}
	response := callSlackForm(t, handler, "/api/admin.workflows.search", url.Values{"query": {"Welcome"}, "limit": {"10"}}.Encode())
	var payload struct {
		OK        bool             `json:"ok"`
		Workflows []map[string]any `json:"workflows"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || !payload.OK {
		t.Fatalf("search: %s", response.Body)
	}
	for _, workflow := range payload.Workflows {
		if workflow["id"] == string(created.ID) {
			if workflow["app_id"] != "" {
				t.Fatalf("app_id=%v, want none for a member's own workflow", workflow["app_id"])
			}
			return
		}
	}
	t.Fatalf("the member's workflow was not found: %s", response.Body)
}
