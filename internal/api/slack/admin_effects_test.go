package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// The methods below reached the ledger with a route, a parity case and a
// storage contract, and no Web API test of their own. Each walk here asserts
// the effect the caller can observe, because a handler that answered ok and
// changed nothing satisfied every check that existed.
func adminCall(t *testing.T, handler http.Handler, method, endpoint, body string) map[string]any {
	t.Helper()
	request := httptest.NewRequest(method, "/api/"+endpoint, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", endpoint, response.Code, response.Body)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// TestAdminBulkConversationChangesTakeEffect holds
// admin.conversations.bulkArchive, admin.conversations.bulkDelete and
// admin.conversations.convertToPublic. A batch that names a channel the
// workspace does not hold must change nothing at all, because a half-applied
// batch leaves an administrator unable to say which half landed.
func TestAdminBulkConversationChangesTakeEffect(t *testing.T) {
	handler, store := testHandlerWithStore()
	store.SeedConversation(domain.Conversation{ID: "C3", WorkspaceID: "T1", Name: "private-room", Kind: domain.ConversationTypePrivate})

	// convertToPublic answers the converted channel, and the channel really is
	// public afterwards rather than merely acknowledged.
	converted := adminCall(t, handler, http.MethodPost, "admin.conversations.convertToPublic", "channel_id=C3")
	if converted["ok"] != true {
		t.Fatalf("convertToPublic=%v", converted)
	}
	if channel := converted["channel"].(map[string]any); channel["is_private"] == true {
		t.Fatalf("the channel is still private after conversion: %v", channel)
	}
	if again := adminCall(t, handler, http.MethodPost, "admin.conversations.convertToPublic", "channel_id=C3"); again["ok"] == true {
		t.Fatalf("a public channel was converted to public: %v", again)
	}
	if missing := adminCall(t, handler, http.MethodPost, "admin.conversations.convertToPublic", "channel_id=C-nobody"); missing["ok"] == true {
		t.Fatalf("a channel that does not exist was converted: %v", missing)
	}

	// A batch naming an absent channel changes nothing: C1 is still readable.
	if partial := adminCall(t, handler, http.MethodPost, "admin.conversations.bulkArchive", "channel_ids=C1,C-nobody"); partial["ok"] == true {
		t.Fatalf("a batch naming an absent channel was applied: %v", partial)
	}
	if info := adminCall(t, handler, http.MethodPost, "conversations.info", "channel=C1"); info["channel"].(map[string]any)["is_archived"] == true {
		t.Fatalf("a refused batch archived a channel anyway: %v", info)
	}

	if archived := adminCall(t, handler, http.MethodPost, "admin.conversations.bulkArchive", "channel_ids=C1,C3"); archived["ok"] != true {
		t.Fatalf("bulkArchive=%v", archived)
	}
	for _, id := range []string{"C1", "C3"} {
		info := adminCall(t, handler, http.MethodPost, "conversations.info", "channel="+id)
		if info["channel"].(map[string]any)["is_archived"] != true {
			t.Fatalf("%s was acknowledged but not archived: %v", id, info)
		}
	}

	if deleted := adminCall(t, handler, http.MethodPost, "admin.conversations.bulkDelete", "channel_ids=C3"); deleted["ok"] != true {
		t.Fatalf("bulkDelete=%v", deleted)
	}
	if info := adminCall(t, handler, http.MethodPost, "conversations.info", "channel=C3"); info["ok"] == true {
		t.Fatalf("a deleted channel is still readable: %v", info)
	}
	for _, endpoint := range []string{"admin.conversations.bulkArchive", "admin.conversations.bulkDelete"} {
		if unnamed := adminCall(t, handler, http.MethodPost, endpoint, ""); unnamed["ok"] == true {
			t.Fatalf("%s with no channels was accepted: %v", endpoint, unnamed)
		}
	}
}

// TestAdminAppUninstallAndRequestCancelTakeEffect holds admin.apps.uninstall
// and admin.apps.requests.cancel. Cancelling withdraws a request nobody has
// decided; cancelling a decided one is refused, because reopening a decision by
// withdrawing it would let an administrator undo an approval silently.
func TestAdminAppUninstallAndRequestCancelTakeEffect(t *testing.T) {
	handler, store := testHandlerWithStore()
	now := time.Now().UTC()
	if err := store.SetAppApproval(context.Background(), "T1", "A2", "R2", domain.AppApprovalRequested, now, events.Event{
		ID: "event-request-A2", WorkspaceID: "T1", ActorID: "U1", Topic: "app.requested", Payload: "A2", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// The request is pending, so cancelling it succeeds and cancelling it twice
	// does not.
	if cancelled := adminCall(t, handler, http.MethodPost, "admin.apps.requests.cancel", "app_id=A2&request_id=R2"); cancelled["ok"] != true {
		t.Fatalf("requests.cancel=%v", cancelled)
	}
	if again := adminCall(t, handler, http.MethodPost, "admin.apps.requests.cancel", "app_id=A2&request_id=R2"); again["ok"] == true {
		t.Fatalf("a cancelled request was cancelled again: %v", again)
	}
	// A1 was approved by the fixture. Withdrawing a decided request would undo
	// an approval without saying so.
	if decided := adminCall(t, handler, http.MethodPost, "admin.apps.requests.cancel", "app_id=A1&request_id=R1"); decided["ok"] == true {
		t.Fatalf("a decided request was cancelled: %v", decided)
	}
	if unnamed := adminCall(t, handler, http.MethodPost, "admin.apps.requests.cancel", "request_id=R2"); unnamed["ok"] == true {
		t.Fatalf("requests.cancel with no app was accepted: %v", unnamed)
	}

	// Uninstalling removes the installation, so the app stops being installed
	// rather than merely being acknowledged.
	before, err := store.ListAppInstallations(context.Background(), "A1")
	if err != nil || len(before) == 0 {
		t.Fatalf("the fixture app is not installed: %+v err=%v", before, err)
	}
	if uninstalled := adminCall(t, handler, http.MethodPost, "admin.apps.uninstall", "app_ids=A1"); uninstalled["ok"] != true {
		t.Fatalf("uninstall=%v", uninstalled)
	}
	after, err := store.ListAppInstallations(context.Background(), "A1")
	if err != nil {
		t.Fatal(err)
	}
	for _, installation := range after {
		if installation.WorkspaceID == "T1" && installation.Enabled {
			t.Fatalf("the app is still installed after uninstall: %+v", installation)
		}
	}
	if unnamed := adminCall(t, handler, http.MethodPost, "admin.apps.uninstall", ""); unnamed["error"] != "invalid_arg_name" {
		t.Fatalf("uninstall with no apps=%v", unnamed)
	}
}

// TestAdminFunctionsListReadsTheManifest holds admin.functions.list. A function
// exists in an app's manifest and nowhere else, so a route that answered an
// empty list would look the same as a workspace with no functions.
func TestAdminFunctionsListReadsTheManifest(t *testing.T) {
	handler, _ := testHandlerWithStore()
	listed := adminCall(t, handler, http.MethodGet, "admin.functions.list", "")
	functions, ok := listed["functions"].([]any)
	if !ok || len(functions) == 0 {
		t.Fatalf("the fixture app declares a function and none was listed: %v", listed)
	}
	first := functions[0].(map[string]any)
	if first["app_id"] != "A1" || first["callback_id"] != "triage" {
		t.Fatalf("the listed function is not the one the manifest declares: %v", first)
	}
}

// TestAdminUsersGetExpirationReportsTheGuestHorizon holds
// admin.users.getExpiration. An account that does not lapse reports 0, which is
// what Slack reports for one; reporting the current instant instead would read
// as an account that has just expired.
func TestAdminUsersGetExpirationReportsTheGuestHorizon(t *testing.T) {
	handler, _ := testHandlerWithStore()
	none := adminCall(t, handler, http.MethodGet, "admin.users.getExpiration?user_id=U2", "")
	if none["ok"] != true || none["expiration_ts"].(float64) != 0 {
		t.Fatalf("an account that does not lapse reported %v", none)
	}
	horizon := time.Now().UTC().Add(72 * time.Hour).Unix()
	if set := adminCall(t, handler, http.MethodPost, "admin.users.setExpiration", "user_id=U2&expiration_ts="+itoa(horizon)); set["ok"] != true {
		t.Fatalf("setExpiration=%v", set)
	}
	read := adminCall(t, handler, http.MethodPost, "admin.users.getExpiration", "user_id=U2")
	if int64(read["expiration_ts"].(float64)) != horizon {
		t.Fatalf("getExpiration answered %v, want %d", read["expiration_ts"], horizon)
	}
	if unnamed := adminCall(t, handler, http.MethodPost, "admin.users.getExpiration", ""); unnamed["error"] != "invalid_arg_name" {
		t.Fatalf("getExpiration with no member=%v", unnamed)
	}
	if missing := adminCall(t, handler, http.MethodPost, "admin.users.getExpiration", "user_id=U-nobody"); missing["ok"] == true {
		t.Fatalf("a member that does not exist reported an expiration: %v", missing)
	}
}

// TestAdminWorkflowCollaboratorsTakeEffect holds
// admin.workflows.collaborators.add and remove. A collaborator who is not
// stored cannot manage the workflow, so the walk reads the manager list back
// rather than trusting the acknowledgement.
func TestAdminWorkflowCollaboratorsTakeEffect(t *testing.T) {
	handler, store := testHandlerWithStore()
	now := time.Now().UTC()
	workflow := domain.WorkflowDefinition{
		ID: "WfAdmin", WorkspaceID: "T1", AppID: "A1", OwnerID: "U1", CallbackID: "admin-workflow",
		Title: "Admin workflow", InputSchema: "{}", Steps: "[]", Status: domain.WorkflowPublished,
		Version: 1, PublishedVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateWorkflow(context.Background(), workflow, events.Event{
		ID: "event-admin-workflow", WorkspaceID: "T1", Topic: "workflow.created", Payload: "WfAdmin", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	if added := adminCall(t, handler, http.MethodPost, "admin.workflows.collaborators.add", "workflow_ids=WfAdmin&collaborator_ids=U2"); added["ok"] != true {
		t.Fatalf("collaborators.add=%v", added)
	}
	found, err := store.GetWorkflow(context.Background(), "T1", "WfAdmin")
	if err != nil {
		t.Fatal(err)
	}
	if len(found.ManagerIDs) != 1 || found.ManagerIDs[0] != "U2" {
		t.Fatalf("the collaborator was acknowledged and not stored: %+v", found.ManagerIDs)
	}

	if removed := adminCall(t, handler, http.MethodPost, "admin.workflows.collaborators.remove", "workflow_ids=WfAdmin&collaborator_ids=U2"); removed["ok"] != true {
		t.Fatalf("collaborators.remove=%v", removed)
	}
	after, err := store.GetWorkflow(context.Background(), "T1", "WfAdmin")
	if err != nil {
		t.Fatal(err)
	}
	if len(after.ManagerIDs) != 0 {
		t.Fatalf("the collaborator survived removal: %+v", after.ManagerIDs)
	}
	for _, body := range []string{"collaborator_ids=U2", "workflow_ids=WfAdmin"} {
		if refused := adminCall(t, handler, http.MethodPost, "admin.workflows.collaborators.add", body); refused["error"] != "invalid_arg_name" {
			t.Fatalf("body=%q refused=%v", body, refused)
		}
	}
}

// TestDiscoverableContactsLookupFollowsTheWorkspaceSetting holds
// users.discoverableContacts.lookup. The answer tells the caller who works
// here, so a workspace that hides itself must answer nothing whatever addresses
// match.
func TestDiscoverableContactsLookupFollowsTheWorkspaceSetting(t *testing.T) {
	handler, store := testHandlerWithStore()
	open := adminCall(t, handler, http.MethodPost, "users.discoverableContacts.lookup", "emails=alice@example.com,nobody@example.invalid")
	contacts, ok := open["contacts"].([]any)
	if !ok || len(contacts) != 1 {
		t.Fatalf("an open workspace answered %v", open)
	}
	if contacts[0].(map[string]any)["email"] != "alice@example.com" {
		t.Fatalf("the wrong contact was matched: %v", contacts[0])
	}
	if unnamed := adminCall(t, handler, http.MethodGet, "users.discoverableContacts.lookup", ""); unnamed["ok"] == true {
		t.Fatalf("a lookup naming no address answered: %v", unnamed)
	}

	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test", Discoverability: domain.WorkspaceDiscoverabilityClosed})
	closed := adminCall(t, handler, http.MethodPost, "users.discoverableContacts.lookup", "emails=alice@example.com")
	if found := closed["contacts"].([]any); len(found) != 0 {
		t.Fatalf("a closed workspace disclosed its members: %v", closed)
	}
}

func itoa(value int64) string {
	digits := ""
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

var _ = memory.New
