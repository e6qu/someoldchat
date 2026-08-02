package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// CONNECT-01..03: the nine conversations.*SharedInvite* methods were ledger
// rows with no route. The whole point of the lifecycle is that approval and
// acceptance are different decisions taken by different organizations, so the
// walk below crosses that boundary rather than testing one side.
func TestSlackConnectInvitationWalksApprovalAndAcceptance(t *testing.T) {
	store, mux := connectWorkspace(t)
	ctx := context.Background()

	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{
		"channel": {"C1"}, "external_limited": {"T2"},
	})
	if !created["ok"].(bool) {
		t.Fatalf("inviteShared=%v", created)
	}
	invite := created["invite"].(map[string]any)
	id := invite["id"].(string)
	if invite["status"].(string) != string(domain.SharedInvitePending) {
		t.Fatalf("a new invitation is not pending: %v", invite)
	}

	// The invited organization cannot accept what the host has not approved.
	refused := connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"invite_id": {id}}, "session-two")
	if refused["ok"].(bool) {
		t.Fatalf("an unapproved invitation was accepted: %v", refused)
	}

	approved := connectCall(t, mux, "/api/conversations.approveSharedInvite", url.Values{"invite_id": {id}})
	if !approved["ok"].(bool) || approved["invite"].(map[string]any)["status"].(string) != string(domain.SharedInviteApproved) {
		t.Fatalf("approveSharedInvite=%v", approved)
	}

	// The host cannot accept on the invited organization's behalf: that is the
	// distinction CONNECT-02 exists for.
	hostAccept := connectCall(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"invite_id": {id}})
	if hostAccept["ok"].(bool) {
		t.Fatalf("the host accepted its own invitation: %v", hostAccept)
	}

	accepted := connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"invite_id": {id}}, "session-two")
	if !accepted["ok"].(bool) || accepted["is_ext_shared"] != true {
		t.Fatalf("acceptSharedInvite=%v", accepted)
	}
	// The host is always listed; what acceptance adds is the invited one.
	teams, _, err := store.ListConversationTeams(ctx, "T1", "C1")
	if err != nil || !slices.Contains(teams, domain.WorkspaceID("T2")) {
		t.Fatalf("teams=%v err=%v, want the invited organization connected", teams, err)
	}

	// Accepted is terminal: a second acceptance is a settled invitation, not a
	// malformed request.
	again := connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"invite_id": {id}}, "session-two")
	if again["ok"].(bool) || again["error"].(string) != "already_resolved" {
		t.Fatalf("a settled invitation was accepted again: %v", again)
	}
}

// conversations.info must report the Slack Connect identity, and the pending
// and shared states are different facts a client renders differently.
func TestConversationInfoReportsTheConnectIdentity(t *testing.T) {
	_, mux := connectWorkspace(t)

	before := connectCall(t, mux, "/api/conversations.info", url.Values{"channel": {"C1"}})
	channel := before["channel"].(map[string]any)
	if channel["is_ext_shared"] == true || channel["is_pending_ext_shared"] == true {
		t.Fatalf("an unshared channel claims a Connect identity: %v", channel)
	}

	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "external_limited": {"T2"}})
	id := created["invite"].(map[string]any)["id"].(string)
	pending := connectCall(t, mux, "/api/conversations.info", url.Values{"channel": {"C1"}})["channel"].(map[string]any)
	if pending["is_pending_ext_shared"] != true || pending["is_ext_shared"] == true {
		t.Fatalf("a channel with an outstanding invitation reads %v, want pending and not yet shared", pending)
	}

	connectCall(t, mux, "/api/conversations.approveSharedInvite", url.Values{"invite_id": {id}})
	connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"invite_id": {id}}, "session-two")
	shared := connectCall(t, mux, "/api/conversations.info", url.Values{"channel": {"C1"}})["channel"].(map[string]any)
	if shared["is_ext_shared"] != true || shared["is_pending_ext_shared"] == true {
		t.Fatalf("a connected channel reads %v, want shared and no longer pending", shared)
	}
}

// Both listing methods answer, and they answer about different states: one
// reports what was issued, the other what is still awaiting a host decision.
func TestConnectListingsSeparateIssuedFromRequested(t *testing.T) {
	_, mux := connectWorkspace(t)
	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "external_limited": {"T2"}})
	id := created["invite"].(map[string]any)["id"].(string)

	requested := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{})
	if len(requested["invites"].([]any)) != 1 {
		t.Fatalf("a pending invitation is not listed as requested: %v", requested)
	}
	issued := connectCall(t, mux, "/api/conversations.listConnectInvites", url.Values{})
	if len(issued["invites"].([]any)) != 0 {
		t.Fatalf("an unapproved invitation is listed as issued: %v", issued)
	}

	connectCall(t, mux, "/api/conversations.approveSharedInvite", url.Values{"invite_id": {id}})
	afterRequested := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{})
	if len(afterRequested["invites"].([]any)) != 0 {
		t.Fatalf("an approved invitation is still awaiting a decision: %v", afterRequested)
	}
	afterIssued := connectCall(t, mux, "/api/conversations.listConnectInvites", url.Values{})
	if len(afterIssued["invites"].([]any)) != 1 {
		t.Fatalf("an approved invitation is not listed as issued: %v", afterIssued)
	}
}

// Declining is the invited organization's answer and denying is the host's
// refusal to send. They are recorded as different statuses because an
// administrator reading the record needs to tell them apart.
func TestDecliningAndDenyingAreDifferentOutcomes(t *testing.T) {
	store, mux := connectWorkspace(t)
	ctx := context.Background()

	denied := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "external_limited": {"T2"}})
	deniedID := denied["invite"].(map[string]any)["id"].(string)
	if result := connectCall(t, mux, "/api/conversations.requestSharedInvite.deny", url.Values{"invite_id": {deniedID}}); !result["ok"].(bool) {
		t.Fatalf("deny=%v", result)
	}
	stored, err := store.GetSharedInvite(ctx, domain.SharedInviteID(deniedID))
	if err != nil || stored.Status != domain.SharedInviteRevoked {
		t.Fatalf("denied invitation=%+v err=%v, want it revoked by the host", stored, err)
	}

	sent := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "external_limited": {"T2"}})
	sentID := sent["invite"].(map[string]any)["id"].(string)
	connectCall(t, mux, "/api/conversations.approveSharedInvite", url.Values{"invite_id": {sentID}})
	if result := connectCallAs(t, mux, "/api/conversations.declineSharedInvite", url.Values{"invite_id": {sentID}}, "session-two"); !result["ok"].(bool) {
		t.Fatalf("decline=%v", result)
	}
	declined, err := store.GetSharedInvite(ctx, domain.SharedInviteID(sentID))
	if err != nil || declined.Status != domain.SharedInviteDeclined {
		t.Fatalf("declined invitation=%+v err=%v, want it declined by the invited organization", declined, err)
	}
}

// connectWorkspace builds the shared fixture plus a second workspace with its
// own administrator, because Slack Connect is only meaningful across two: the
// host approves and the invited organization accepts, and a single-workspace
// fixture cannot tell those two decisions apart.
func connectWorkspace(t *testing.T) (*memory.Store, http.Handler) {
	t.Helper()
	// The stored authenticator, because this fixture needs two real tokens for
	// two different workspaces; the static one answers as one principal.
	scopes := make([]auth.Scope, 0)
	for _, name := range auth.AllScopes() {
		scopes = append(scopes, auth.Scope(name))
	}
	mux, store := testHandlerWithStoredTokenAuth(scopes...)
	ctx := context.Background()
	store.SeedWorkspace(domain.Workspace{ID: "T2", Name: "Second"})
	store.SeedUser(domain.User{ID: "U-two", WorkspaceID: "T2", Name: "outsider", Email: "outsider@example.com"})
	if err := store.SeedWorkspaceRole("T2", "U-two", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedToken(ctx, "session-one", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", AppID: "A1", TokenType: "user", Scopes: auth.AllScopes()}); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedToken(ctx, "session-two", domain.TokenRecord{WorkspaceID: "T2", UserID: "U-two", AppID: "A1", TokenType: "user", Scopes: auth.AllScopes()}); err != nil {
		t.Fatal(err)
	}
	_ = service.Messages{}
	return store, mux
}

func connectCall(t *testing.T, mux http.Handler, path string, values url.Values) map[string]any {
	t.Helper()
	return connectCallAs(t, mux, path, values, "session-one")
}

func connectCallAs(t *testing.T, mux http.Handler, path string, values url.Values, token string) map[string]any {
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
	return decoded
}
