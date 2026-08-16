package auth

// scopeDescriptions gives every grantable scope a short, human sentence for the
// install-consent screen, so a person authorizing an app reads "Send messages"
// rather than the raw "chat:write" token. The wording follows Slack's own scope
// catalogue: it names the capability, not the API method, and stays in the
// present tense a consent screen reads best in.
//
// Every scope in allScopes must appear here — TestEveryGrantableScopeIsExplained
// enforces it, so a new scope cannot ship a bare token to the consent screen.
var scopeDescriptions = map[Scope]string{
	ScopeChatWrite:          "Send messages",
	ScopeChatWriteCustomize: "Send messages with a customized name and icon",
	ScopeIncomingWebhook:    "Post messages to a specific channel through an incoming webhook",
	ScopeChannelsHistory:    "View messages and content in conversations it has been added to",

	ScopeUsersRead:         "View people in the workspace",
	ScopeUsersReadEmail:    "View the email addresses of people in the workspace",
	ScopeUsersWrite:        "Edit profiles and set presence",
	ScopeUsersProfileRead:  "View profile details and status",
	ScopeUsersProfileWrite: "Edit profile information and status",

	ScopeChannelsRead:         "View basic information about public channels",
	ScopeChannelsJoin:         "Join public channels",
	ScopeChannelsWrite:        "Rename public channels and edit their settings",
	ScopeChannelsManage:       "Create, archive, and manage channels",
	ScopeChannelsWriteInvites: "Invite people to public channels",
	ScopeGroupsWrite:          "Manage private channels and their settings",
	ScopeGroupsWriteInvites:   "Invite people to private channels",
	ScopeIMWrite:              "Start direct messages with people",
	ScopeMPIMWrite:            "Start group direct messages",

	ScopeReactionsWrite: "Add and remove emoji reactions",
	ScopeReactionsRead:  "View emoji reactions and their content",
	ScopePinsWrite:      "Pin and unpin messages",
	ScopePinsRead:       "View pinned messages",
	ScopeBookmarksRead:  "View bookmarks",
	ScopeBookmarksWrite: "Create, edit, and remove bookmarks",

	ScopeSearchRead:       "Search the workspace's messages, files, and content",
	ScopeFilesWrite:       "Upload, edit, and delete files",
	ScopeFilesRead:        "View files shared in the workspace",
	ScopeRemoteFilesRead:  "View remote files the app has added",
	ScopeRemoteFilesWrite: "Add and edit remote files",
	ScopeRemoteFilesShare: "Share remote files into conversations",

	ScopeCanvasesRead:  "View canvases",
	ScopeCanvasesWrite: "Create and edit canvases",
	ScopeListsRead:     "View lists",
	ScopeListsWrite:    "Create and edit lists",

	ScopeTeamRead:            "View basic information about the workspace",
	ScopeTeamPreferencesRead: "View the workspace's preferences",
	ScopeEmojiRead:           "View custom emoji",
	ScopeAuthorizationsRead:  "View the app's own installations",
	ScopeLinksWrite:          "Unfurl links it posts into rich previews",
	ScopeIdentityBasic:       "View the signed-in person's basic identity",

	ScopeRTMStream:        "Connect to the real-time message stream",
	ScopeConnectionsWrite: "Open a Socket Mode connection",
	ScopeDatastoreRead:    "Read from the app's datastores",
	ScopeDatastoreWrite:   "Write to the app's datastores",

	ScopeDNDRead:         "View Do Not Disturb settings",
	ScopeDNDWrite:        "Change Do Not Disturb settings",
	ScopeStarsRead:       "View saved items",
	ScopeStarsWrite:      "Save and unsave items",
	ScopeRemindersRead:   "View reminders",
	ScopeRemindersWrite:  "Create and manage reminders",
	ScopeUserGroupsRead:  "View user groups",
	ScopeUserGroupsWrite: "Create and manage user groups",
	ScopeCallsRead:       "View calls",
	ScopeCallsWrite:      "Start and manage calls",

	ScopeWorkflowStepsExecute: "Run the app's custom workflow steps",
	ScopeTriggersRead:         "View workflow triggers",
	ScopeTriggersWrite:        "Create and manage workflow triggers",
	ScopeTokensBasic:          "Exchange the authorization for an access token",

	ScopeConversationsConnectRead:   "View Slack Connect invitations",
	ScopeConversationsConnectWrite:  "Create and accept Slack Connect invitations",
	ScopeConversationsConnectManage: "Manage Slack Connect channels and their approvals",

	ScopeAdmin:                   "Administer the workspace",
	ScopeAdminUsersRead:          "View members for administration",
	ScopeAdminUsersWrite:         "Add, remove, and manage members",
	ScopeAdminConversationsRead:  "View channels for administration",
	ScopeAdminConversationsWrite: "Manage channels across the workspace",
	ScopeAdminUserGroupsRead:     "View user groups for administration",
	ScopeAdminUserGroupsWrite:    "Manage user groups across the workspace",
	ScopeAdminTeamsRead:          "View workspaces for administration",
	ScopeAdminTeamsWrite:         "Create and manage workspaces",
	ScopeAdminInvitesRead:        "View invitations for administration",
	ScopeAdminInvitesWrite:       "Send and manage invitations",
	ScopeAdminAppsRead:           "View app installations and requests",
	ScopeAdminAppsWrite:          "Approve, restrict, and manage app installations",
	ScopeAdminWorkflowsRead:      "View workflows for administration",
	ScopeAdminWorkflowsWrite:     "Manage workflows across the workspace",
	ScopeAdminRolesRead:          "View administrative role assignments",
	ScopeAdminRolesWrite:         "Assign and remove administrative roles",
	ScopeAdminBarriersRead:       "View information barriers",
	ScopeAdminBarriersWrite:      "Create and manage information barriers",
	ScopeAdminAnalyticsRead:      "View workspace analytics",
	ScopeAuditLogsRead:           "View the audit logs",
}

// Description returns a short human sentence naming what the scope permits, for
// display on the install-consent screen. It is empty for a scope with no entry,
// which lets a caller fall back to showing the raw token rather than inventing a
// description that might misstate what an app can do.
func (s Scope) Description() string {
	return scopeDescriptions[s]
}
