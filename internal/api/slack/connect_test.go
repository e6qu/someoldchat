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
		"channel": {"C1"}, "user_ids": {"U-two"},
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

	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "user_ids": {"U-two"}})
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
	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "user_ids": {"U-two"}})
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

	denied := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "user_ids": {"U-two"}})
	deniedID := denied["invite"].(map[string]any)["id"].(string)
	if result := connectCall(t, mux, "/api/conversations.requestSharedInvite.deny", url.Values{"invite_id": {deniedID}}); !result["ok"].(bool) {
		t.Fatalf("deny=%v", result)
	}
	stored, err := store.GetSharedInvite(ctx, domain.SharedInviteID(deniedID))
	if err != nil || stored.Status != domain.SharedInviteRevoked {
		t.Fatalf("denied invitation=%+v err=%v, want it revoked by the host", stored, err)
	}

	sent := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "user_ids": {"U-two"}})
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

// TestRequestSharedInviteApproveDecidesTheSameInvitation holds
// conversations.requestSharedInvite.approve. Slack publishes the same decision
// under two method names, so the request-oriented one has to reach the same
// invitation and leave it in the same state; a route that answered ok without
// deciding anything would read as working and approve nobody.
func TestRequestSharedInviteApproveDecidesTheSameInvitation(t *testing.T) {
	_, mux := connectWorkspace(t)

	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{
		"channel": {"C1"}, "user_ids": {"U-two"},
	})
	if !created["ok"].(bool) {
		t.Fatalf("inviteShared=%v", created)
	}
	id := created["invite"].(map[string]any)["id"].(string)

	approved := connectCall(t, mux, "/api/conversations.requestSharedInvite.approve", url.Values{"invite_id": {id}})
	if !approved["ok"].(bool) {
		t.Fatalf("requestSharedInvite.approve=%v", approved)
	}
	if status := approved["invite"].(map[string]any)["status"].(string); status != string(domain.SharedInviteApproved) {
		t.Fatalf("status after approval=%q", status)
	}

	// The decision is durable and singular: approving again is a conflict
	// rather than a second approval, and the invited organization can now
	// accept exactly as it could through the other method name.
	if again := connectCall(t, mux, "/api/conversations.requestSharedInvite.approve", url.Values{"invite_id": {id}}); again["ok"] == true {
		t.Fatalf("an already approved invitation was approved again: %v", again)
	}
	accepted := connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"invite_id": {id}}, "session-two")
	if !accepted["ok"].(bool) {
		t.Fatalf("acceptSharedInvite after requestSharedInvite.approve=%v", accepted)
	}
	if missing := connectCall(t, mux, "/api/conversations.requestSharedInvite.approve", url.Values{"invite_id": {"I-nobody"}}); missing["ok"] == true {
		t.Fatalf("an invitation that does not exist was approved: %v", missing)
	}
}

// TestExternalInvitePermissionsSetUpgradesAndDowngrades holds
// conversations.externalInvitePermissions.set. Withdrawing the ability to
// invite is a downgrade of the association and not a removal of the
// organization: an implementation that disconnected instead would look like it
// worked and would quietly evict everybody that organization had added.
func TestExternalInvitePermissionsSetUpgradesAndDowngrades(t *testing.T) {
	_, mux := connectWorkspace(t)

	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{
		"channel": {"C1"}, "user_ids": {"U-two"},
	})
	id := created["invite"].(map[string]any)["id"].(string)
	if !connectCall(t, mux, "/api/conversations.approveSharedInvite", url.Values{"invite_id": {id}})["ok"].(bool) {
		t.Fatal("the invitation was not approved")
	}
	if !connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"invite_id": {id}}, "session-two")["ok"].(bool) {
		t.Fatal("the invitation was not accepted")
	}

	for _, action := range []string{"upgrade", "downgrade"} {
		result := connectCall(t, mux, "/api/conversations.externalInvitePermissions.set", url.Values{
			"channel": {"C1"}, "target_team": {"T2"}, "action": {action},
		})
		if !result["ok"].(bool) || result["channel"].(map[string]any)["id"].(string) != "C1" {
			t.Fatalf("%s=%v", action, result)
		}
		// The organization is still connected either way. A downgrade that
		// disconnected would pass an ok check and lose the association.
		teams := connectCall(t, mux, "/api/admin.conversations.getTeams", url.Values{"channel_id": {"C1"}})
		if !teams["ok"].(bool) {
			t.Fatalf("getTeams after %s=%v", action, teams)
		}
		if !slices.Contains(stringsOf(teams["team_ids"]), "T2") {
			t.Fatalf("%s removed the organization instead of changing its permission: %v", action, teams)
		}
	}

	for _, values := range []url.Values{
		{"target_team": {"T2"}, "action": {"upgrade"}},
		{"channel": {"C1"}, "action": {"upgrade"}},
		{"channel": {"C1"}, "target_team": {"T2"}},
		{"channel": {"C1"}, "target_team": {"T2"}, "action": {"sideways"}},
	} {
		if refused := connectCall(t, mux, "/api/conversations.externalInvitePermissions.set", values); refused["error"] != "invalid_arg_name" {
			t.Fatalf("values=%v refused=%v", values, refused)
		}
	}
	// An organization that is not on the channel has no permission to change.
	if absent := connectCall(t, mux, "/api/conversations.externalInvitePermissions.set", url.Values{
		"channel": {"C1"}, "target_team": {"T-nobody"}, "action": {"upgrade"},
	}); absent["ok"] == true {
		t.Fatalf("a permission was set for an organization that is not connected: %v", absent)
	}
}

func stringsOf(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, isText := item.(string); isText {
			result = append(result, text)
			continue
		}
		if object, isObject := item.(map[string]any); isObject {
			if id, hasID := object["id"].(string); hasID {
				result = append(result, id)
			}
		}
	}
	return result
}

// TestSharedInviteArgumentsTheSDKsSendAreApplied walks every argument the
// published SDKs send to the Slack Connect methods (python slack_sdk 3.45,
// Java slack-api-client 1.52, @slack/web-api 8.2). Each used to be dropped
// while the call answered ok: external_limited was read as the organization to
// invite, user_ids was ignored, the approval's restriction, channel and message
// never reached the invitation, and requestSharedInvite.list filtered by a
// `status` no SDK sends.
func TestSharedInviteArgumentsTheSDKsSendAreApplied(t *testing.T) {
	store, mux := connectWorkspace(t)
	ctx := context.Background()
	store.SeedConversation(domain.Conversation{ID: "C-public", WorkspaceID: "T1", Name: "public"})
	store.SeedConversationMember("C-public", "U1")
	store.SeedWorkspace(domain.Workspace{ID: "T3", Name: "Third"})
	store.SeedUser(domain.User{ID: "U-three", WorkspaceID: "T3", Name: "third"})

	// external_limited is a boolean; it names no organization, so on its own
	// the invitation has no recipient.
	for _, values := range []url.Values{
		{"channel": {"C1"}, "external_limited": {"T2"}},
		{"channel": {"C1"}, "user_ids": {"U-two"}, "external_limited": {"T2"}},
		{"channel": {"C1"}, "user_ids": {"U-two,U-three"}},
		{"channel": {"C1"}, "user_ids": {"U2"}},
	} {
		if refused := connectCall(t, mux, "/api/conversations.inviteShared", values); refused["ok"] == true || refused["error"] != "invalid_arg_name" {
			t.Fatalf("values=%v answered %v, want invalid_arg_name", values, refused)
		}
	}
	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "user_ids": {"U-two"}})
	invite := created["invite"].(map[string]any)
	id := invite["id"].(string)
	if created["invite_id"] != id || invite["target_team"] != "T2" || invite["is_external_limited"] != true {
		t.Fatalf("inviteShared=%v, want T2 invited external-limited by default", created)
	}
	unlimited := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "user_ids": {"U-three"}, "external_limited": {"false"}})
	if unlimited["invite"].(map[string]any)["is_external_limited"] != false {
		t.Fatalf("external_limited=false was not kept: %v", unlimited)
	}
	unlimitedID := unlimited["invite_id"].(string)

	// requestSharedInvite.list: pending by default, narrowed by invite_ids and
	// user_id, reported in Slack's invite_requests shape as well.
	listed := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{"invite_ids": {id}})
	requests := listed["invite_requests"].([]any)
	if len(requests) != 1 || requests[0].(map[string]any)["id"] != id || requests[0].(map[string]any)["is_external_limited"] != true {
		t.Fatalf("invite_ids narrowed to %v", listed)
	}
	if mine := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{"user_id": {"U1"}}); len(mine["invites"].([]any)) != 2 {
		t.Fatalf("user_id=U1 listed %v, want both of U1's requests", mine)
	}
	if others := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{"user_id": {"U2"}}); len(others["invites"].([]any)) != 0 {
		t.Fatalf("user_id=U2 listed %v, want none", others)
	}
	if bad := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{"include_denied": {"perhaps"}}); bad["error"] != "invalid_arg_name" {
		t.Fatalf("a malformed boolean answered %v", bad)
	}

	// requestSharedInvite.approve moves the invitation to channel_id, sets the
	// restriction with is_external_limited, and keeps the message.
	if refused := connectCall(t, mux, "/api/conversations.requestSharedInvite.approve", url.Values{"invite_id": {id}, "message": {"not json"}}); refused["error"] != "invalid_arg_name" {
		t.Fatalf("a malformed message answered %v", refused)
	}
	approved := connectCall(t, mux, "/api/conversations.requestSharedInvite.approve", url.Values{
		"invite_id": {id}, "channel_id": {"C-public"}, "is_external_limited": {"true"},
		"message": {`{"is_override":true,"text":"Welcome aboard"}`},
	})
	if approved["ok"] != true || approved["invite_id"] != id {
		t.Fatalf("requestSharedInvite.approve=%v", approved)
	}
	stored, err := store.GetSharedInvite(ctx, domain.SharedInviteID(id))
	if err != nil || stored.ConversationID != "C-public" || !stored.ExternalLimited || stored.ReviewMessage != "Welcome aboard" {
		t.Fatalf("approved invitation=%+v err=%v", stored, err)
	}
	if pending := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{}); len(pending["invites"].([]any)) != 1 {
		t.Fatalf("the approved request is still listed as pending: %v", pending)
	}
	if withApproved := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{"include_approved": {"true"}}); len(withApproved["invites"].([]any)) != 2 {
		t.Fatalf("include_approved listed %v", withApproved)
	}

	// The denial's message is kept for the member who asked.
	if denied := connectCall(t, mux, "/api/conversations.requestSharedInvite.deny", url.Values{"invite_id": {unlimitedID}, "message": {"Not this quarter"}}); denied["ok"] != true {
		t.Fatalf("deny=%v", denied)
	}
	if deniedInvite, err := store.GetSharedInvite(ctx, domain.SharedInviteID(unlimitedID)); err != nil || deniedInvite.ReviewMessage != "Not this quarter" {
		t.Fatalf("denied invitation=%+v err=%v", deniedInvite, err)
	}
	if withDenied := connectCall(t, mux, "/api/conversations.requestSharedInvite.list", url.Values{"include_denied": {"1"}}); len(withDenied["invites"].([]any)) != 1 {
		t.Fatalf("include_denied listed %v, want the denial alone", withDenied)
	}

	// acceptSharedInvite names the invitation by channel_id. A request for a
	// private channel is refused for a public conversation rather than
	// answered by joining one the whole organization can read, and a team_id
	// other than the token's is refused.
	if foreign := connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"channel_id": {"C-public"}, "team_id": {"T9"}, "channel_name": {"shared"}}, "session-two"); foreign["error"] != "invalid_arg_name" {
		t.Fatalf("a foreign team_id answered %v", foreign)
	}
	if private := connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"channel_id": {"C-public"}, "is_private": {"true"}, "channel_name": {"shared"}}, "session-two"); private["ok"] == true {
		t.Fatalf("a private acceptance joined a public conversation: %v", private)
	}
	if none := connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{"channel_id": {"C1"}, "channel_name": {"shared"}}, "session-two"); none["error"] != "invite_not_found" {
		t.Fatalf("a channel with no invitation answered %v", none)
	}
	accepted := connectCallAs(t, mux, "/api/conversations.acceptSharedInvite", url.Values{
		"channel_id": {"C-public"}, "channel_name": {"shared"}, "free_trial_accepted": {"false"}, "team_id": {"T2"},
	}, "session-two")
	if accepted["ok"] != true || accepted["channel_id"] != "C-public" || accepted["invite_id"] != id {
		t.Fatalf("acceptSharedInvite by channel=%v", accepted)
	}
	// The restriction is applied: the external-limited organization may not
	// invite another organization into the channel it joined.
	if canInvite, err := store.GetExternalInvitePermission(ctx, "T1", "C-public", "T2"); err != nil || canInvite {
		t.Fatalf("external-limited T2 may invite=%v err=%v", canInvite, err)
	}
	store.SeedConversationMember("C-public", "U-two")
	if onward := connectCallAs(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C-public"}, "user_ids": {"U-three"}}, "session-two"); onward["error"] != "not_allowed" {
		t.Fatalf("an external-limited organization invited onward: %v", onward)
	}
}

// approveSharedInvite and declineSharedInvite take target_team, the other
// party to the invitation; naming a different one does not decide this one.
func TestSharedInviteTargetTeamMustNameTheOtherParty(t *testing.T) {
	store, mux := connectWorkspace(t)
	created := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "user_ids": {"U-two"}})
	id := created["invite_id"].(string)
	if wrong := connectCall(t, mux, "/api/conversations.approveSharedInvite", url.Values{"invite_id": {id}, "target_team": {"T9"}}); wrong["error"] != "invite_not_found" {
		t.Fatalf("approval naming another organization answered %v", wrong)
	}
	if stored, err := store.GetSharedInvite(context.Background(), domain.SharedInviteID(id)); err != nil || stored.Status != domain.SharedInvitePending {
		t.Fatalf("the invitation was decided anyway: %+v err=%v", stored, err)
	}
	if right := connectCall(t, mux, "/api/conversations.approveSharedInvite", url.Values{"invite_id": {id}, "target_team": {"T2"}}); right["ok"] != true {
		t.Fatalf("approval naming the invited organization=%v", right)
	}
	if wrong := connectCallAs(t, mux, "/api/conversations.declineSharedInvite", url.Values{"invite_id": {id}, "target_team": {"T9"}}, "session-two"); wrong["error"] != "invite_not_found" {
		t.Fatalf("decline naming another host answered %v", wrong)
	}
	if right := connectCallAs(t, mux, "/api/conversations.declineSharedInvite", url.Values{"invite_id": {id}, "target_team": {"T1"}}, "session-two"); right["ok"] != true {
		t.Fatalf("decline naming the host=%v", right)
	}
	// listConnectInvites pages by count, which is what the SDKs send.
	for _, address := range []string{"one@example.org", "two@example.org"} {
		next := connectCall(t, mux, "/api/conversations.inviteShared", url.Values{"channel": {"C1"}, "emails": {address}})
		connectCall(t, mux, "/api/conversations.approveSharedInvite", url.Values{"invite_id": {next["invite_id"].(string)}})
	}
	page := connectCall(t, mux, "/api/conversations.listConnectInvites", url.Values{"count": {"1"}})
	if len(page["invites"].([]any)) != 1 || page["response_metadata"].(map[string]any)["next_cursor"] == "" {
		t.Fatalf("count=1 answered %v", page)
	}
	if foreign := connectCall(t, mux, "/api/conversations.listConnectInvites", url.Values{"team_id": {"T9"}}); foreign["error"] != "invalid_arg_name" {
		t.Fatalf("a foreign team_id answered %v", foreign)
	}
}
