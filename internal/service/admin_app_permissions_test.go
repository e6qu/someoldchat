package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// seedAppAccessWorld is a workspace with an administrator (UA), a member (U1),
// two channels, and the apps the cases name: AA declares two MCP servers, AB
// none, AC one, and AD one but is deleted. AA, AB, AC and AD are approved.
func seedAppAccessWorld(t *testing.T) (context.Context, *memory.Store, Messages) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	now := time.Now().UTC()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}),
		repository.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1", Name: "admin"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "member"}),
		repository.SeedWorkspaceRole("T1", "UA", domain.WorkspaceRoleAdmin),
		repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}),
		repository.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "other"}),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	servers := map[domain.AppID]string{
		"AA": `[{"name":"search","url":"https://mcp.example.test/search"},{"name":"tickets","url":"https://mcp.example.test/tickets"}]`,
		"AB": `[]`,
		"AC": `[{"name":"wiki","url":"https://mcp.example.test/wiki"}]`,
		"AD": `[{"name":"gone","url":"https://mcp.example.test/gone"}]`,
	}
	for _, app := range []domain.AppID{"AA", "AB", "AC", "AD"} {
		manifest := fmt.Sprintf(`{"display_information":{"name":"%s"},"features":{"mcp_servers":%s}}`, app, servers[app])
		if err := repository.CreateApp(ctx, domain.App{
			ID: app, DevelopmentWorkspaceID: "T1", OwnerID: "UA", Name: string(app), ClientID: "client-" + string(app),
			SigningSecretHash: "hash", SigningSecretCiphertext: "cipher", VerificationTokenHash: "hash", VerificationTokenCiphertext: "cipher",
			ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
		}, domain.AppManifestRevision{AppID: app, Version: 1, Manifest: manifest, CreatedBy: "UA", CreatedAt: now},
			domain.OAuthClient{ID: "client-" + string(app), SecretHash: "secret", AppID: app}); err != nil {
			t.Fatal(err)
		}
		if err := repository.SetAppApproval(ctx, "T1", app, domain.AppRequestID("R"+string(app)), domain.AppApprovalApproved, now,
			events.Event{ID: domain.EventID("Ev" + string(app)), WorkspaceID: "T1", Topic: "app.approved", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.DeleteApp(ctx, "AD", "UA", now); err != nil {
		t.Fatal(err)
	}
	return ctx, repository, Messages{Store: repository, AppCredentialKey: []byte("0123456789abcdef0123456789abcdef")}
}

// The allowlist pages server by server across apps: an approved app with no
// servers contributes none, a deleted app's servers are gone, and a page can
// end inside one app's list and resume there.
func TestAdminMCPServersPagesTheApprovedAllowlist(t *testing.T) {
	ctx, _, messages := seedAppAccessWorld(t)
	want := make([]domain.MCPServerID, 0, 3)
	for _, server := range []struct {
		app  domain.AppID
		name string
	}{{"AA", "search"}, {"AA", "tickets"}} {
		want = append(want, domain.NewMCPServerID(server.app, server.name))
	}
	if want[0] > want[1] {
		want[0], want[1] = want[1], want[0]
	}
	want = append(want, domain.NewMCPServerID("AC", "wiki"))

	whole, err := messages.AdminMCPServers(ctx, "T1", "UA", domain.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got := serverIDs(whole.Servers); !reflect.DeepEqual(got, want) || whole.HasMore || whole.NextCursor != "" {
		t.Fatalf("whole list=%v more=%t, want %v", got, whole.HasMore, want)
	}
	walked := make([]domain.MCPServerID, 0, 3)
	request := domain.PageRequest{Limit: 1}
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("the walk did not end")
		}
		page, err := messages.AdminMCPServers(ctx, "T1", "UA", request)
		if err != nil {
			t.Fatal(err)
		}
		walked = append(walked, serverIDs(page.Servers)...)
		if !page.HasMore {
			break
		}
		request.Cursor = page.NextCursor
	}
	if !reflect.DeepEqual(walked, want) {
		t.Fatalf("walked=%v, want %v", walked, want)
	}
	if _, err := messages.AdminMCPServers(ctx, "T1", "UA", domain.PageRequest{Limit: 1, Cursor: "garbage"}); !errors.Is(err, domain.ErrInvalidCursor) {
		t.Fatalf("garbage cursor err=%v", err)
	}
	if _, err := messages.AdminMCPServers(ctx, "T1", "U1", domain.PageRequest{Limit: 1}); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("member err=%v", err)
	}
	if _, err := messages.AdminAppMCPServerPermissions(ctx, "T1", "UA", "AD"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted app err=%v", err)
	}
}

func serverIDs(servers []domain.MCPServer) []domain.MCPServerID {
	ids := make([]domain.MCPServerID, 0, len(servers))
	for _, server := range servers {
		ids = append(ids, server.ID)
	}
	return ids
}

// A replacement that leaves the channel restriction out keeps the one the app
// has; no_one clears it; the stored list never names an entity twice; and an
// event records each change.
func TestAdminSetAppPermissionKeepsOrClearsTheChannelRestriction(t *testing.T) {
	ctx, repository, messages := seedAppAccessWorld(t)
	if _, err := messages.AdminSetAppPermission(ctx, "T1", "UA", domain.AppPermission{
		AppID: "AA", PermissionType: domain.AppPermissionEveryone,
		ChannelRestrictionMode: domain.ChannelRestrictionAllChannelsExcept, ChannelIDs: []domain.ConversationID{"C2", "C2"},
	}); err != nil {
		t.Fatal(err)
	}
	named, err := messages.AdminSetAppPermission(ctx, "T1", "UA", domain.AppPermission{
		AppID: "AA", PermissionType: domain.AppPermissionNamedEntities, UserIDs: []domain.UserID{"U1", "U1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if named.ChannelRestrictionMode != domain.ChannelRestrictionAllChannelsExcept || !reflect.DeepEqual(named.ChannelIDs, []domain.ConversationID{"C2"}) ||
		!reflect.DeepEqual(named.UserIDs, []domain.UserID{"U1"}) {
		t.Fatalf("named=%+v", named)
	}
	nobody, err := messages.AdminSetAppPermission(ctx, "T1", "UA", domain.AppPermission{AppID: "AA", PermissionType: domain.AppPermissionNoOne})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetAppPermission(ctx, "T1", "AA")
	if err != nil {
		t.Fatal(err)
	}
	if nobody.ChannelRestrictionMode != domain.ChannelRestrictionUnset || len(stored.ChannelIDs) != 0 || stored.PermissionType != domain.AppPermissionNoOne {
		t.Fatalf("no_one=%+v stored=%+v", nobody, stored)
	}
	topics := map[string]int{}
	for _, event := range repository.Outbox() {
		topics[event.Topic]++
	}
	if topics["app.permissions_set"] != 3 {
		t.Fatalf("events=%v, want three app.permissions_set", topics)
	}
	if _, err := messages.AdminSetAppPermission(ctx, "T1", "UA", domain.AppPermission{AppID: "AD", PermissionType: domain.AppPermissionEveryone}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted app err=%v", err)
	}
}

// A server rule that names no entities stores none.
func TestAdminSetMCPServerPermissionDropsEntitiesATypeDoesNotTake(t *testing.T) {
	ctx, _, messages := seedAppAccessWorld(t)
	server := domain.NewMCPServerID("AC", "wiki")
	stored, err := messages.AdminSetMCPServerPermission(ctx, "T1", "UA", domain.MCPServerPermission{
		AppID: "AC", ServerID: server, PermissionType: domain.MCPServerPermissionNoOne, UserIDs: []domain.UserID{"U1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.UserIDs) != 0 || stored.UserIDs == nil {
		t.Fatalf("stored=%+v", stored)
	}
	access, err := messages.AdminAppMCPServerPermissions(ctx, "T1", "UA", "AC")
	if err != nil {
		t.Fatal(err)
	}
	if len(access) != 1 || access[0].Server.Name != "wiki" || access[0].Permission.PermissionType != domain.MCPServerPermissionNoOne {
		t.Fatalf("access=%+v", access)
	}
}

// The access control list decides who may invoke an app: a member it does not
// admit does not see the app's commands and is refused when invoking one, and
// a channel the restriction leaves out refuses the command there. The refusal
// comes before the app is contacted.
func TestAppAccessControlDecidesWhoMayInvokeAnApp(t *testing.T) {
	ctx, repository, messages := seedAppAccessWorld(t)
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Deploy"},"oauth_config":{"scopes":{"bot":["commands"]}},"features":{"slash_commands":[{"command":"/deploy","url":"https://deploy.example.test/command","description":"Deploy"}]}}`
	if err := repository.CreateApp(ctx, domain.App{
		ID: "AS", DevelopmentWorkspaceID: "T1", OwnerID: "UA", Name: "Deploy", ClientID: "client-AS",
		SigningSecretHash: "hash", SigningSecretCiphertext: "cipher", VerificationTokenHash: "hash", VerificationTokenCiphertext: "cipher",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "AS", Version: 1, Manifest: manifest, CreatedBy: "UA", CreatedAt: now},
		domain.OAuthClient{ID: "client-AS", SecretHash: "secret", AppID: "AS"}); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []error{
		repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "AS", WorkspaceID: "T1", Enabled: true, CreatedAt: now}),
		repository.SeedConversationMember("C1", "U1"),
		repository.SeedConversationMember("C2", "U1"),
		repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "grouped"}),
		repository.CreateUserGroup(ctx, domain.UserGroup{
			WorkspaceID: "T1", ID: "S1", Name: "Ops", Handle: "ops", Creator: "UA", UpdatedBy: "UA",
			CreatedAt: now, UpdatedAt: now, Enabled: true, Users: []domain.UserID{"U2"},
		}, events.Event{ID: "EvS1", WorkspaceID: "T1", Topic: "subteam.created", CreatedAt: now}),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	commands := func(user domain.UserID) int {
		t.Helper()
		listed, err := messages.ListAppShortcuts(ctx, "T1", user, "slash")
		if err != nil {
			t.Fatal(err)
		}
		return len(listed)
	}
	if commands("U1") != 1 {
		t.Fatal("an unrestricted app's command is not offered")
	}
	if _, err := messages.AdminSetAppPermission(ctx, "T1", "UA", domain.AppPermission{
		AppID: "AS", PermissionType: domain.AppPermissionNamedEntities, UserGroupIDs: []domain.UserGroupID{"S1"},
	}); err != nil {
		t.Fatal(err)
	}
	if commands("U1") != 0 || commands("U2") != 1 {
		t.Fatalf("named list offered U1 %d and U2 %d commands, want 0 and 1", commands("U1"), commands("U2"))
	}
	if err := messages.DispatchSlashCommand(ctx, "T1", "U1", "C1", "", "/deploy", "now", "https://chat.example.test"); !errors.Is(err, domain.ErrAppUseRestricted) {
		t.Fatalf("a member the list does not admit invoked the command: %v", err)
	}
	for _, testCase := range []struct {
		value   domain.AppPermission
		channel domain.ConversationID
		user    domain.UserID
		want    error
	}{
		{domain.AppPermission{PermissionType: domain.AppPermissionNoOne}, "", "U2", domain.ErrAppUseRestricted},
		{domain.AppPermission{PermissionType: domain.AppPermissionEveryone, ChannelRestrictionMode: domain.ChannelRestrictionSpecificChannels, ChannelIDs: []domain.ConversationID{"C1"}}, "C1", "U1", nil},
		{domain.AppPermission{PermissionType: domain.AppPermissionEveryone, ChannelRestrictionMode: domain.ChannelRestrictionSpecificChannels, ChannelIDs: []domain.ConversationID{"C1"}}, "C2", "U1", domain.ErrAppUseRestricted},
		{domain.AppPermission{PermissionType: domain.AppPermissionEveryone, ChannelRestrictionMode: domain.ChannelRestrictionAllChannelsExcept, ChannelIDs: []domain.ConversationID{"C1"}}, "C1", "U1", domain.ErrAppUseRestricted},
		{domain.AppPermission{PermissionType: domain.AppPermissionEveryone, ChannelRestrictionMode: domain.ChannelRestrictionAllChannelsExcept, ChannelIDs: []domain.ConversationID{"C1"}}, "C2", "U1", nil},
		{domain.AppPermission{PermissionType: domain.AppPermissionNamedEntities, UserIDs: []domain.UserID{"U1"}}, "C2", "U1", nil},
	} {
		value := testCase.value
		value.AppID = "AS"
		if _, err := messages.AdminSetAppPermission(ctx, "T1", "UA", value); err != nil {
			t.Fatal(err)
		}
		if err := messages.requireAppUse(ctx, "T1", testCase.user, "AS", testCase.channel); !errors.Is(err, testCase.want) || (testCase.want == nil && err != nil) {
			t.Errorf("%+v in %q for %s: %v, want %v", testCase.value, testCase.channel, testCase.user, err, testCase.want)
		}
	}
}

// A member who has left can still be taken off a list naming them; an
// identifier that is neither on the list nor here is user_not_found.
func TestAdminRemoveAppPermissionEntitiesRemovesADeactivatedMember(t *testing.T) {
	ctx, repository, messages := seedAppAccessWorld(t)
	if _, err := messages.AdminSetAppPermission(ctx, "T1", "UA", domain.AppPermission{
		AppID: "AA", PermissionType: domain.AppPermissionNamedEntities, UserIDs: []domain.UserID{"U1", "UA"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "member", Deleted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.AdminRemoveAppPermissionEntities(ctx, "T1", "UA", domain.AppPermissionChange{AppID: "AA", UserIDs: []domain.UserID{"U-ghost"}}); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("unknown user err=%v", err)
	}
	removed, err := messages.AdminRemoveAppPermissionEntities(ctx, "T1", "UA", domain.AppPermissionChange{AppID: "AA", UserIDs: []domain.UserID{"U1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(removed.UserIDs, []domain.UserID{"UA"}) {
		t.Fatalf("removed=%+v", removed)
	}
}

// Every way a member uses an app answers to its access control, not only a
// slash command: an interactive element of its message, its Home tab, and its
// Messages tab. A member the list admits keeps all three.
func TestAppAccessControlGatesEveryUseOfAnApp(t *testing.T) {
	ctx, repository, messages := seedAppAccessWorld(t)
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Helper"},"oauth_config":{"scopes":{"bot":["commands","chat:write"]}},` +
		`"features":{"app_home":{"home_tab_enabled":true,"messages_tab_enabled":true}},` +
		`"settings":{"socket_mode_enabled":true,"interactivity":{"is_enabled":true}}}`
	verificationCiphertext, err := secretbox.Seal(messages.AppCredentialKey, appVerificationTokenAssociatedData("AH"), "verification-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateApp(ctx, domain.App{
		ID: "AH", DevelopmentWorkspaceID: "T1", OwnerID: "UA", Name: "Helper", ClientID: "client-AH",
		SigningSecretHash: "hash", SigningSecretCiphertext: "cipher", VerificationTokenHash: domain.HashToken("verification-token"), VerificationTokenCiphertext: verificationCiphertext,
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{AppID: "AH", Version: 1, Manifest: manifest, CreatedBy: "UA", CreatedAt: now},
		domain.OAuthClient{ID: "client-AH", SecretHash: "secret", AppID: "AH"}); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []error{
		repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "AH", WorkspaceID: "T1", Enabled: true, CreatedAt: now}),
		repository.SeedUser(domain.User{ID: "UBOT", WorkspaceID: "T1", Name: "helper"}),
		repository.CreateBot(ctx, domain.Bot{ID: "BH", AppID: "AH", WorkspaceID: "T1", UserID: "UBOT", Name: "helper", UpdatedAt: now}),
		repository.SeedConversationMember("C1", "U1"),
		repository.SeedConversationMember("C1", "UBOT"),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	blocks := `[{"type":"actions","block_id":"choice","elements":[{"type":"button","action_id":"go","text":{"type":"plain_text","text":"Go"},"value":"go"}]}]`
	message, err := messages.PostWithBlocksAndAttachments(ctx, "T1", "UBOT", "C1", "Pick one", blocks, "", "", "", "AH")
	if err != nil {
		t.Fatal(err)
	}
	uses := func(user domain.UserID) map[string]error {
		t.Helper()
		_, _, home := messages.AppHome(ctx, "T1", user, "AH")
		_, tab := messages.OpenAppMessages(ctx, "T1", user, "AH")
		action := messages.DispatchBlockAction(ctx, "T1", user, domain.AppBlockAction{
			MessageID: message.ID, BlockID: "choice", ActionID: "go", Type: "button", Value: "go",
		}, "https://chat.example.test")
		return map[string]error{"home": home, "messages tab": tab, "block action": action}
	}
	for use, err := range uses("U1") {
		if err != nil {
			t.Fatalf("unrestricted %s: %v", use, err)
		}
	}
	if _, err := messages.AdminSetAppPermission(ctx, "T1", "UA", domain.AppPermission{
		AppID: "AH", PermissionType: domain.AppPermissionNamedEntities, UserIDs: []domain.UserID{"UA"},
	}); err != nil {
		t.Fatal(err)
	}
	for use, err := range uses("U1") {
		if !errors.Is(err, domain.ErrAppUseRestricted) {
			t.Errorf("%s by a member the list does not admit: %v, want ErrAppUseRestricted", use, err)
		}
	}
	// A channel restriction refuses the block action in the channel it
	// excludes; the Home and Messages tabs are in no channel.
	if _, err := messages.AdminSetAppPermission(ctx, "T1", "UA", domain.AppPermission{
		AppID: "AH", PermissionType: domain.AppPermissionEveryone,
		ChannelRestrictionMode: domain.ChannelRestrictionAllChannelsExcept, ChannelIDs: []domain.ConversationID{"C1"},
	}); err != nil {
		t.Fatal(err)
	}
	restricted := uses("U1")
	if restricted["home"] != nil || restricted["messages tab"] != nil || !errors.Is(restricted["block action"], domain.ErrAppUseRestricted) {
		t.Fatalf("channel-restricted uses = %v, want only the block action refused", restricted)
	}
}
