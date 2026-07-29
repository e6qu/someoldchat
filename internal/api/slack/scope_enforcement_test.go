package slack

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// scopedRoute records the scope a Slack method requires.
//
// Most entries are the pinned `token` parameter's own words: each operation in
// specs/upstream/slack-api-specs/web-api/slack_web_openapi_v2.json states
// "Requires scope: `…`". Some are deliberate modern-scope substitutions, and the
// header used to claim the whole table was a copy of the snapshot, which was
// false for about forty rows. The substitutions are:
//
//   - chat.* uses chat:write where the snapshot says chat:write:bot /
//     chat:write:user, which Slack replaced with the single chat:write;
//   - conversations.* mutators use channels:manage where the snapshot says
//     channels:write / groups:write / im:write / mpim:write, which Slack
//     replaced with channels:manage;
//   - files.* writes use files:write where the snapshot says files:write:user;
//   - users.profile.get and team.profile.get use users:read where the snapshot
//     says users.profile:read, because internal/auth declares no
//     users.profile:read scope. That one is a gap, not a modernisation — see the
//     follow-up recorded for internal/auth.
//
// Routes this repository adds and the snapshot does not describe at all
// (/internal/…, emoji.list, slackLists.*) carry the scope this system defines.
type scopedRoute struct {
	method string
	path   string
	scope  auth.Scope
}

// scopedRoutes covers every registered Slack method that enforces a scope. It is
// the regression net for the whole authorization surface: before it existed
// nothing failed when a handler passed an empty scope to authenticate, which is
// how conversations.info and users.info came to enforce nothing at all.
func scopedRoutes() []scopedRoute {
	return []scopedRoute{
		{http.MethodGet, "/api/workflows.stepCompleted", auth.ScopeWorkflowStepsExecute},
		{http.MethodGet, "/api/workflows.stepFailed", auth.ScopeWorkflowStepsExecute},
		{http.MethodGet, "/api/workflows.updateStep", auth.ScopeWorkflowStepsExecute},
		{http.MethodGet, "/api/team.info", auth.ScopeTeamRead},
		{http.MethodGet, "/api/rtm.connect", auth.ScopeRTMStream},
		{http.MethodGet, "/api/bots.info", auth.ScopeUsersRead},
		{http.MethodGet, "/api/migration.exchange", auth.ScopeTokensBasic},
		{http.MethodGet, "/api/team.billableInfo", auth.ScopeAdmin},
		{http.MethodGet, "/api/team.profile.get", auth.ScopeUsersRead},
		{http.MethodGet, "/api/team.accessLogs", auth.ScopeAdmin},
		{http.MethodGet, "/api/team.integrationLogs", auth.ScopeAdmin},
		{http.MethodGet, "/api/admin.users.list", auth.ScopeAdminUsersRead},
		{http.MethodPost, "/api/admin.users.remove", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.users.session.invalidate", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.users.session.reset", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.users.setAdmin", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.users.setOwner", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.users.setRegular", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.users.setExpiration", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.users.invite", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.users.assign", auth.ScopeAdminUsersWrite},
		{http.MethodPost, "/api/admin.inviteRequests.approve", auth.ScopeAdminInvitesWrite},
		{http.MethodGet, "/api/admin.inviteRequests.approved.list", auth.ScopeAdminInvitesRead},
		{http.MethodGet, "/api/admin.inviteRequests.denied.list", auth.ScopeAdminInvitesRead},
		{http.MethodPost, "/api/admin.inviteRequests.deny", auth.ScopeAdminInvitesWrite},
		{http.MethodGet, "/api/admin.inviteRequests.list", auth.ScopeAdminInvitesRead},
		{http.MethodPost, "/api/admin.apps.approve", auth.ScopeAdminAppsWrite},
		{http.MethodGet, "/api/admin.apps.approved.list", auth.ScopeAdminAppsRead},
		{http.MethodGet, "/api/admin.apps.requests.list", auth.ScopeAdminAppsRead},
		{http.MethodPost, "/api/admin.apps.restrict", auth.ScopeAdminAppsWrite},
		{http.MethodGet, "/api/admin.apps.restricted.list", auth.ScopeAdminAppsRead},
		{http.MethodPost, "/api/admin.conversations.rename", auth.ScopeAdminConversationsWrite},
		{http.MethodPost, "/api/admin.conversations.create", auth.ScopeAdminConversationsWrite},
		{http.MethodPost, "/api/admin.conversations.archive", auth.ScopeAdminConversationsWrite},
		{http.MethodPost, "/api/admin.conversations.unarchive", auth.ScopeAdminConversationsWrite},
		{http.MethodPost, "/api/admin.conversations.delete", auth.ScopeAdminConversationsWrite},
		{http.MethodPost, "/api/admin.conversations.restrictAccess.addGroup", auth.ScopeAdminConversationsWrite},
		{http.MethodGet, "/api/admin.conversations.restrictAccess.listGroups", auth.ScopeAdminConversationsRead},
		{http.MethodPost, "/api/admin.conversations.restrictAccess.removeGroup", auth.ScopeAdminConversationsWrite},
		{http.MethodPost, "/api/admin.conversations.invite", auth.ScopeAdminConversationsWrite},
		{http.MethodPost, "/api/admin.conversations.convertToPrivate", auth.ScopeAdminConversationsWrite},
		{http.MethodGet, "/api/admin.conversations.getConversationPrefs", auth.ScopeAdminConversationsRead},
		{http.MethodPost, "/api/admin.conversations.setConversationPrefs", auth.ScopeAdminConversationsWrite},
		{http.MethodGet, "/api/admin.conversations.search", auth.ScopeAdminConversationsRead},
		{http.MethodGet, "/api/admin.conversations.getTeams", auth.ScopeAdminConversationsRead},
		{http.MethodPost, "/api/admin.conversations.setTeams", auth.ScopeAdminConversationsWrite},
		{http.MethodPost, "/api/admin.conversations.disconnectShared", auth.ScopeAdminConversationsWrite},
		{http.MethodGet, "/api/admin.conversations.ekm.listOriginalConnectedChannelInfo", auth.ScopeAdminConversationsRead},
		{http.MethodPost, "/api/admin.emoji.add", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/admin.emoji.addAlias", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/admin.emoji.remove", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/admin.emoji.rename", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/chat.postMessage", auth.ScopeChatWrite},
		{http.MethodPost, "/api/chat.startStream", auth.ScopeChatWrite},
		{http.MethodPost, "/api/chat.appendStream", auth.ScopeChatWrite},
		{http.MethodPost, "/api/chat.stopStream", auth.ScopeChatWrite},
		// chat.unfurl requires links:write, not chat:write: the pinned token
		// parameter says so, and attaching a preview to a message is a different
		// grant from writing one.
		{http.MethodPost, "/api/chat.unfurl", auth.ScopeLinksWrite},
		{http.MethodGet, "/api/apps.event.authorizations.list", auth.ScopeAuthorizationsRead},
		{http.MethodPost, "/api/apps.datastore.get", auth.ScopeDatastoreRead},
		{http.MethodPost, "/api/apps.datastore.bulkGet", auth.ScopeDatastoreRead},
		{http.MethodPost, "/api/apps.datastore.put", auth.ScopeDatastoreWrite},
		{http.MethodPost, "/api/apps.datastore.update", auth.ScopeDatastoreWrite},
		{http.MethodPost, "/api/apps.datastore.delete", auth.ScopeDatastoreWrite},
		{http.MethodPost, "/api/apps.datastore.bulkPut", auth.ScopeDatastoreWrite},
		{http.MethodPost, "/api/apps.datastore.bulkDelete", auth.ScopeDatastoreWrite},
		{http.MethodPost, "/api/chat.postEphemeral", auth.ScopeChatWrite},
		{http.MethodPost, "/api/chat.meMessage", auth.ScopeChatWrite},
		{http.MethodPost, "/api/chat.update", auth.ScopeChatWrite},
		{http.MethodPost, "/api/chat.delete", auth.ScopeChatWrite},
		{http.MethodPost, "/api/chat.scheduleMessage", auth.ScopeChatWrite},
		{http.MethodPost, "/api/chat.deleteScheduledMessage", auth.ScopeChatWrite},
		{http.MethodGet, "/api/conversations.history", auth.ScopeChannelsHistory},
		{http.MethodGet, "/api/conversations.replies", auth.ScopeChannelsHistory},
		{http.MethodGet, "/api/conversations.info", auth.ScopeChannelsRead},
		{http.MethodGet, "/api/users.info", auth.ScopeUsersRead},
		{http.MethodGet, "/api/users.identity", auth.ScopeIdentityBasic},
		{http.MethodGet, "/api/users.lookupByEmail", auth.ScopeUsersReadEmail},
		{http.MethodGet, "/api/users.getPresence", auth.ScopeUsersRead},
		{http.MethodPost, "/api/users.setPresence", auth.ScopeUsersWrite},
		{http.MethodGet, "/api/dnd.info", auth.ScopeDNDRead},
		{http.MethodPost, "/api/dnd.endDnd", auth.ScopeDNDWrite},
		{http.MethodPost, "/api/dnd.endSnooze", auth.ScopeDNDWrite},
		{http.MethodPost, "/api/dnd.setSnooze", auth.ScopeDNDWrite},
		{http.MethodGet, "/api/dnd.teamInfo", auth.ScopeDNDRead},
		{http.MethodGet, "/api/users.profile.get", auth.ScopeUsersRead},
		{http.MethodGet, "/api/users.list", auth.ScopeUsersRead},
		{http.MethodPost, "/api/users.profile.set", auth.ScopeUsersProfileWrite},
		{http.MethodPost, "/api/users.deletePhoto", auth.ScopeUsersProfileWrite},
		{http.MethodPost, "/api/users.setPhoto", auth.ScopeUsersProfileWrite},
		{http.MethodPost, "/api/users.setActive", auth.ScopeUsersWrite},
		{http.MethodGet, "/api/conversations.list", auth.ScopeChannelsRead},
		{http.MethodGet, "/api/users.conversations", auth.ScopeChannelsRead},
		{http.MethodGet, "/api/conversations.members", auth.ScopeChannelsRead},
		{http.MethodPost, "/api/conversations.create", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.join", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.invite", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.leave", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.kick", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.rename", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.setTopic", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.setPurpose", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.archive", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.unarchive", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.close", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.open", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/conversations.mark", auth.ScopeChannelsManage},
		{http.MethodPost, "/api/reactions.add", auth.ScopeReactionsWrite},
		{http.MethodPost, "/api/reactions.remove", auth.ScopeReactionsWrite},
		{http.MethodGet, "/api/reactions.get", auth.ScopeReactionsRead},
		{http.MethodGet, "/api/reactions.list", auth.ScopeReactionsRead},
		{http.MethodPost, "/api/pins.add", auth.ScopePinsWrite},
		{http.MethodPost, "/api/pins.remove", auth.ScopePinsWrite},
		{http.MethodGet, "/api/pins.list", auth.ScopePinsRead},
		{http.MethodPost, "/api/stars.add", auth.ScopeStarsWrite},
		{http.MethodGet, "/api/stars.list", auth.ScopeStarsRead},
		{http.MethodPost, "/api/stars.remove", auth.ScopeStarsWrite},
		{http.MethodPost, "/api/bookmarks.add", auth.ScopeBookmarksWrite},
		{http.MethodPost, "/api/bookmarks.edit", auth.ScopeBookmarksWrite},
		{http.MethodGet, "/api/bookmarks.list", auth.ScopeBookmarksRead},
		{http.MethodPost, "/api/bookmarks.remove", auth.ScopeBookmarksWrite},
		{http.MethodPost, "/api/canvases.create", auth.ScopeCanvasesWrite},
		{http.MethodPost, "/api/canvases.edit", auth.ScopeCanvasesWrite},
		{http.MethodPost, "/api/canvases.delete", auth.ScopeCanvasesWrite},
		{http.MethodPost, "/api/canvases.access.set", auth.ScopeCanvasesWrite},
		{http.MethodPost, "/api/canvases.access.delete", auth.ScopeCanvasesWrite},
		{http.MethodPost, "/api/canvases.sections.lookup", auth.ScopeCanvasesRead},
		{http.MethodPost, "/api/slackLists.create", auth.ScopeListsWrite},
		{http.MethodPost, "/api/slackLists.update", auth.ScopeListsWrite},
		{http.MethodPost, "/api/slackLists.items.create", auth.ScopeListsWrite},
		{http.MethodPost, "/api/slackLists.items.info", auth.ScopeListsRead},
		{http.MethodPost, "/api/slackLists.items.list", auth.ScopeListsRead},
		{http.MethodPost, "/api/slackLists.items.update", auth.ScopeListsWrite},
		{http.MethodPost, "/api/slackLists.access.set", auth.ScopeListsWrite},
		{http.MethodPost, "/api/slackLists.access.delete", auth.ScopeListsWrite},
		{http.MethodPost, "/api/slackLists.download.start", auth.ScopeListsRead},
		{http.MethodPost, "/api/slackLists.download.get", auth.ScopeListsRead},
		{http.MethodPost, "/api/reminders.add", auth.ScopeRemindersWrite},
		{http.MethodPost, "/api/reminders.complete", auth.ScopeRemindersWrite},
		{http.MethodPost, "/api/reminders.delete", auth.ScopeRemindersWrite},
		{http.MethodGet, "/api/reminders.info", auth.ScopeRemindersRead},
		{http.MethodGet, "/api/reminders.list", auth.ScopeRemindersRead},
		{http.MethodPost, "/api/usergroups.create", auth.ScopeUserGroupsWrite},
		{http.MethodPost, "/api/usergroups.update", auth.ScopeUserGroupsWrite},
		{http.MethodPost, "/api/usergroups.enable", auth.ScopeUserGroupsWrite},
		{http.MethodPost, "/api/usergroups.disable", auth.ScopeUserGroupsWrite},
		{http.MethodGet, "/api/usergroups.list", auth.ScopeUserGroupsRead},
		{http.MethodGet, "/api/usergroups.users.list", auth.ScopeUserGroupsRead},
		{http.MethodPost, "/api/usergroups.users.update", auth.ScopeUserGroupsWrite},
		{http.MethodPost, "/api/admin.usergroups.addChannels", auth.ScopeAdminUserGroupsWrite},
		{http.MethodPost, "/api/admin.usergroups.addTeams", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/admin.usergroups.removeChannels", auth.ScopeAdminUserGroupsWrite},
		{http.MethodGet, "/api/admin.usergroups.listChannels", auth.ScopeAdminUserGroupsRead},
		{http.MethodGet, "/api/admin.teams.settings.info", auth.ScopeAdminTeamsRead},
		{http.MethodPost, "/api/admin.teams.settings.setName", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/admin.teams.settings.setDescription", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/admin.teams.settings.setDiscoverability", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/admin.teams.settings.setIcon", auth.ScopeAdminTeamsWrite},
		{http.MethodPost, "/api/admin.teams.settings.setDefaultChannels", auth.ScopeAdminTeamsWrite},
		{http.MethodGet, "/api/admin.teams.list", auth.ScopeAdminTeamsRead},
		{http.MethodPost, "/api/admin.teams.create", auth.ScopeAdminTeamsWrite},
		{http.MethodGet, "/api/admin.teams.admins.list", auth.ScopeAdminTeamsRead},
		{http.MethodGet, "/api/admin.teams.owners.list", auth.ScopeAdminTeamsRead},
		{http.MethodPost, "/api/calls.add", auth.ScopeCallsWrite},
		{http.MethodPost, "/api/calls.end", auth.ScopeCallsWrite},
		{http.MethodGet, "/api/calls.info", auth.ScopeCallsRead},
		{http.MethodPost, "/api/calls.update", auth.ScopeCallsWrite},
		{http.MethodPost, "/api/calls.participants.add", auth.ScopeCallsWrite},
		{http.MethodPost, "/api/calls.participants.remove", auth.ScopeCallsWrite},
		{http.MethodGet, "/api/search.messages", auth.ScopeSearchRead},
		{http.MethodGet, "/api/files.info", auth.ScopeFilesRead},
		{http.MethodPost, "/api/files.delete", auth.ScopeFilesWrite},
		{http.MethodPost, "/api/files.comments.delete", auth.ScopeFilesWrite},
		{http.MethodGet, "/api/files.list", auth.ScopeFilesRead},
		{http.MethodPost, "/api/files.upload", auth.ScopeFilesWrite},
		{http.MethodPost, "/api/files.remote.add", auth.ScopeRemoteFilesWrite},
		{http.MethodGet, "/api/files.remote.info", auth.ScopeRemoteFilesRead},
		{http.MethodGet, "/api/files.remote.list", auth.ScopeRemoteFilesRead},
		{http.MethodPost, "/api/files.remote.remove", auth.ScopeRemoteFilesWrite},
		{http.MethodGet, "/api/files.remote.share", auth.ScopeRemoteFilesShare},
		{http.MethodPost, "/api/files.remote.update", auth.ScopeRemoteFilesWrite},
		{http.MethodPost, "/api/files.sharedPublicURL", auth.ScopeFilesWrite},
		{http.MethodPost, "/api/files.revokePublicURL", auth.ScopeFilesWrite},
		{http.MethodGet, "/api/files/{file}", auth.ScopeFilesRead},
		{http.MethodGet, "/api/files.getUploadURLExternal", auth.ScopeFilesWrite},
		{http.MethodGet, "/api/files.completeUploadExternal", auth.ScopeFilesWrite},
		// Routes that enforce a scope and were absent from this table, so nothing
		// regression-tested them.
		{http.MethodPost, "/internal/admin/incoming-webhooks/create", auth.ScopeAdminAppsWrite},
		{http.MethodPost, "/internal/admin/incoming-webhooks/enable", auth.ScopeAdminAppsWrite},
		{http.MethodGet, "/internal/slack-lists/download.csv", auth.ScopeListsRead},
		{http.MethodGet, "/api/emoji.list", auth.ScopeEmojiRead},
		{http.MethodGet, "/api/admin.emoji.list", auth.ScopeAdminTeamsRead},
		{http.MethodPost, "/api/slackLists.items.delete", auth.ScopeListsWrite},
		{http.MethodPost, "/api/slackLists.items.deleteMultiple", auth.ScopeListsWrite},
		// apps.connections.open authenticates an app-level token through a separate
		// authenticator, so the shared fixture cannot exercise it; the entry keeps
		// the coverage assertion honest and TestEveryScopedMethodRejects… skips it.
		{http.MethodPost, "/api/apps.connections.open", auth.ScopeConnectionsWrite},
	}
}

// TestEveryScopedMethodRejectsATokenMissingItsScope grants every scope the
// fixture knows about except the one under test, so a handler that enforces the
// wrong scope, or no scope, fails here.
func TestEveryScopedMethodRejectsATokenMissingItsScope(t *testing.T) {
	// apps.connections.open reads an app-level token through Handler.SocketAuth,
	// a different authenticator from the one this fixture builds, so withholding a
	// scope from the user token proves nothing about it. It stays in scopedRoutes
	// so the coverage assertion below can see it.
	separateAuthenticator := map[string]struct{}{"/api/apps.connections.open": {}}
	for _, route := range scopedRoutes() {
		if _, ok := separateAuthenticator[route.path]; ok {
			continue
		}
		granted := make([]auth.Scope, 0, len(defaultTestScopes()))
		for _, scope := range defaultTestScopes() {
			if scope != route.scope {
				granted = append(granted, scope)
			}
		}
		handler, _ := testHandlerWithScopes(granted...)
		request := httptest.NewRequest(route.method, route.path, nil)
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Errorf("%s %s: status=%d body=%s", route.method, route.path, response.Code, response.Body)
			continue
		}
		var body struct {
			OK       bool   `json:"ok"`
			Error    string `json:"error"`
			Needed   string `json:"needed"`
			Provided string `json:"provided"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Errorf("%s %s: decode: %v", route.method, route.path, err)
			continue
		}
		if body.OK || body.Error != "missing_scope" {
			t.Errorf("%s %s: body=%+v, want ok=false error=missing_scope", route.method, route.path, body)
			continue
		}
		if body.Needed != string(route.scope) {
			t.Errorf("%s %s: needed=%q, want %q", route.method, route.path, body.Needed, route.scope)
		}
		for _, provided := range strings.Split(body.Provided, ",") {
			if provided == string(route.scope) {
				t.Errorf("%s %s: provided still lists the withheld scope %s", route.method, route.path, route.scope)
			}
		}
	}
}

// TestNarrowScopeTokenCannotReadConversationsOrUsers is the concrete case behind
// the table above: a token whose only grant is chat:write must not be able to
// read channel metadata or a user profile. Both handlers used to call
// authenticate with an empty scope, which skips the check entirely.
func TestNarrowScopeTokenCannotReadConversationsOrUsers(t *testing.T) {
	handler, _ := testHandlerWithScopes(auth.ScopeChatWrite)
	cases := []struct {
		path   string
		needed auth.Scope
	}{
		{"/api/conversations.info?channel=C1", auth.ScopeChannelsRead},
		{"/api/users.info?user=U1", auth.ScopeUsersRead},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", testCase.path, response.Code, response.Body)
		}
		var body struct {
			OK     bool   `json:"ok"`
			Error  string `json:"error"`
			Needed string `json:"needed"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.OK || body.Error != "missing_scope" || body.Needed != string(testCase.needed) {
			t.Fatalf("%s: body=%+v", testCase.path, body)
		}
	}
}

// A read must never require a write scope: admin.emoji.list is a read and the
// pinned contract requires admin.teams:read for it, so a read-only admin token
// has to succeed.
func TestAdminEmojiListAcceptsAReadOnlyAdminToken(t *testing.T) {
	handler, _ := testHandlerWithScopes(auth.ScopeAdminTeamsRead)
	request := httptest.NewRequest(http.MethodGet, "/api/admin.emoji.list", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
}

// A token scope is a statement about what an application may attempt. It is not
// a statement about who the actor is. Before the workspace-role check existed,
// every admin.* method authorized on the scope alone and verified only that the
// actor was a workspace MEMBER, so any member holding an admin.users:write token
// could promote themselves to administrator, deactivate other people, or disable
// the workspace's only sign-in provider.
//
// The fixture's U1 is seeded as an administrator because most tests here drive
// administrative operations. This test demotes it to member while keeping the
// full admin scope set, which is exactly the attacker's position.
func TestAdministrativeMutationsRefuseAMemberHoldingAdminScopes(t *testing.T) {
	for _, testCase := range []struct {
		path string
		form string
	}{
		{"/api/admin.users.setAdmin", "team_id=T1&user_id=U1"},
		{"/api/admin.users.setOwner", "team_id=T1&user_id=U1"},
		{"/api/admin.users.remove", "team_id=T1&user_id=U2"},
		{"/api/admin.conversations.rename", "channel_id=C1&name=renamed"},
		{"/api/admin.conversations.delete", "channel_id=C1"},
		{"/api/admin.teams.settings.setName", "team_id=T1&name=seized"},
	} {
		handler, store := testHandlerWithStore()
		if err := store.SeedWorkspaceRole("T1", "U1", domain.WorkspaceRoleMember); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(testCase.form))
		request.Header.Set("Authorization", "Bearer token")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)

		// Handled refusals are HTTP 200 with ok:false, per the Slack contract.
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d, want 200 with a handled refusal", testCase.path, response.Code)
		}
		body := response.Body.String()
		if strings.Contains(body, `"ok":true`) {
			t.Fatalf("%s: a member holding admin scopes performed an administrative mutation: %s", testCase.path, body)
		}
		if !strings.Contains(body, "no_permission") && !strings.Contains(body, "not_an_admin") {
			t.Fatalf("%s: refusal did not name a permission failure: %s", testCase.path, body)
		}
	}
}

// The check must not be a lockout: the same operations must still succeed for an
// actual administrator, or the fix would simply have broken workspace
// administration instead of securing it.
func TestAdministrativeMutationsStillAcceptAnAdministrator(t *testing.T) {
	handler, _ := testHandlerWithStore()
	request := httptest.NewRequest(http.MethodPost, "/api/admin.conversations.rename", strings.NewReader("channel_id=C1&name=renamed"))
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("administrator was refused: status=%d body=%s", response.Code, response.Body)
	}
}

// scopedRoutes was hand-maintained, so a route that enforced a scope and was
// never added to it was regression-tested by nothing — seven were missing when
// this check was written, including both internal admin webhook routes and the
// list-CSV download. The table is still the statement of *which* scope each route
// requires; this asserts it is complete.
func TestScopedRoutesTableCoversEveryRouteThatEnforcesAScope(t *testing.T) {
	facts := handlerFacts(t)
	constants := scopeConstantNames(t)
	listed := make(map[string]auth.Scope, 400)
	for _, route := range scopedRoutes() {
		listed[route.path] = route.scope
	}
	registered := make(map[string]struct{}, 400)
	for _, route := range registeredRoutes(t) {
		registered[route.path] = struct{}{}
		scopes := reachableScopes(facts, route.handler)
		if len(scopes) == 0 {
			if _, ok := listed[route.path]; ok {
				t.Errorf("%s is in scopedRoutes but enforces no scope", route.path)
			}
			continue
		}
		required, ok := listed[route.path]
		if !ok {
			t.Errorf("%s enforces one of %v and is absent from scopedRoutes, so nothing tests it", route.path, sortedKeys(scopes))
			continue
		}
		if _, ok := scopes[constants[required]]; !ok {
			t.Errorf("%s is listed as %s but the handler reaches %v", route.path, required, sortedKeys(scopes))
		}
	}
	for path := range listed {
		if _, ok := registered[path]; !ok {
			t.Errorf("scopedRoutes lists %s, which Register does not register", path)
		}
	}
}

// scopeConstantNames reads internal/auth for the Scope… constant declarations, so
// a scope value can be matched against the identifier the source scan sees.
func scopeConstantNames(t *testing.T) map[auth.Scope]string {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, filepath.Join("..", "..", "auth", "auth.go"), nil, 0)
	if err != nil {
		t.Fatalf("parse auth.go: %v", err)
	}
	names := make(map[auth.Scope]string)
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			if !strings.HasPrefix(value.Names[0].Name, "Scope") {
				continue
			}
			literal, ok := stringLiteral(value.Values[0])
			if !ok {
				continue
			}
			names[auth.Scope(literal)] = value.Names[0].Name
		}
	}
	if len(names) == 0 {
		t.Fatal("no auth.Scope constants discovered; the scan is broken")
	}
	return names
}
