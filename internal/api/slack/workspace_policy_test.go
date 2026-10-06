package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
)

// policyCall posts a method as one token and returns the HTTP status with the
// decoded body, because a policy refusal is a handled error and must answer
// Slack's 200 with ok=false, never a 500.
func policyCall(t *testing.T, mux http.Handler, path string, values url.Values, token string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("%s body=%s err=%v", path, response.Body.String(), err)
	}
	return response.Code, decoded
}

func TestPrivateChannelCreationAnswersRestrictedActionUnderTheWorkspacePolicy(t *testing.T) {
	store, mux := connectWorkspace(t)
	ctx := context.Background()
	// U2 is a plain member of T1; U1, whose token is session-one, administers it.
	if err := store.SeedToken(ctx, "session-member", domain.TokenRecord{WorkspaceID: "T1", UserID: "U2", AppID: "A1", TokenType: "user", Scopes: auth.AllScopes()}); err != nil {
		t.Fatal(err)
	}
	create := func(token, name, private string) (int, map[string]any) {
		return policyCall(t, mux, "/api/conversations.create", url.Values{"name": {name}, "is_private": {private}}, token)
	}

	if status, body := create("session-member", "default-private", "true"); status != http.StatusOK || body["ok"] != true {
		t.Fatalf("a member creating a private channel by default: status=%d body=%v", status, body)
	}

	if _, err := (service.Messages{Store: store}).SetWorkspacePolicy(ctx, "T1", "U1", domain.WorkspacePolicy{PrivateChannelCreators: domain.PolicyAudienceAdmins}); err != nil {
		t.Fatal(err)
	}
	if status, body := create("session-member", "member-private", "true"); status != http.StatusOK || body["ok"] != false || body["error"] != "restricted_action" {
		t.Fatalf("a member under admins-only: status=%d body=%v, want 200 restricted_action", status, body)
	}
	if status, body := create("session-member", "member-public", "false"); status != http.StatusOK || body["ok"] != true {
		t.Fatalf("a public channel under admins-only: status=%d body=%v", status, body)
	}
	if status, body := create("session-one", "admin-private", "true"); status != http.StatusOK || body["ok"] != true {
		t.Fatalf("an administrator under admins-only: status=%d body=%v", status, body)
	}

	if _, err := (service.Messages{Store: store}).SetWorkspacePolicy(ctx, "T1", "U1", domain.WorkspacePolicy{PrivateChannelCreators: domain.PolicyAudienceOwners}); err != nil {
		t.Fatal(err)
	}
	// admin.conversations.create declares the same code, and the policy is
	// the same whichever method makes the channel.
	if status, body := policyCall(t, mux, "/api/admin.conversations.create", url.Values{"name": {"admin-api-private"}, "is_private": {"true"}, "team_id": {"T1"}}, "session-one"); status != http.StatusOK || body["error"] != "restricted_action" {
		t.Fatalf("admin.conversations.create by an administrator under owners-only: status=%d body=%v, want restricted_action", status, body)
	}
}

func TestTeamPreferencesListReportsTheWorkspacePolicy(t *testing.T) {
	store, mux := connectWorkspace(t)
	ctx := context.Background()
	_, before := policyCall(t, mux, "/api/team.preferences.list", url.Values{}, "session-one")
	if before["warn_before_at_channel"] != "always" || before["who_can_create_private_channel"] != "regular" {
		t.Fatalf("an unconfigured workspace reports %v, want Slack's defaults", before)
	}
	if _, err := (service.Messages{Store: store}).SetWorkspacePolicy(ctx, "T1", "U1", domain.WorkspacePolicy{BroadcastWarningOff: true, PrivateChannelCreators: domain.PolicyAudienceAdmins}); err != nil {
		t.Fatal(err)
	}
	_, after := policyCall(t, mux, "/api/team.preferences.list", url.Values{}, "session-one")
	if after["warn_before_at_channel"] != "never" || after["who_can_create_private_channel"] != "admin" {
		t.Fatalf("a configured workspace reports %v", after)
	}
}
