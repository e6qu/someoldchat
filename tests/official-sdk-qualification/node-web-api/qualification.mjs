import assert from "node:assert/strict";
import { LogLevel, WebClient } from "@slack/web-api";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const token = process.env.SAMEOLDCHAT_API_TOKEN ?? "xoxb-test";
const clientOptions = {
	slackApiUrl: apiUrl,
	...(process.env.SAMEOLDCHAT_SDK_DEBUG === "1" ? { logLevel: LogLevel.DEBUG } : {}),
};
const client = new WebClient(token, clientOptions);
const reminderClient = new WebClient("xoxp-reminder-qualification", clientOptions);
const workflowClient = new WebClient("xoxb-workflow-qualification", clientOptions);
// The invited organization's own credential; see the Slack Connect walk below.
const externalClient = new WebClient("xoxb-external-org", clientOptions);
const thirdOrgClient = new WebClient("xoxb-third-org", clientOptions);
const appClient = new WebClient(process.env.SAMEOLDCHAT_APP_TOKEN ?? "xapp-test", clientOptions);
const configurationClient = new WebClient(process.env.SAMEOLDCHAT_CONFIGURATION_TOKEN ?? "xoxe.xoxp-qualification", clientOptions);

const socketMode = await appClient.apiCall("apps.connections.open");
assert.equal(socketMode.ok, true);
assert.equal(socketMode.url.startsWith("ws://127.0.0.1:18080/socket-mode?connection_id="), true);

const success = await client.api.test();
assert.equal(success.ok, true);
// blocks.validate is newer than the SDK's generated convenience namespace,
// so exercise it through the official client's supported raw-method path. This
// still qualifies the SDK's argument serialization and response decoding.
const validBlocks = await client.apiCall("blocks.validate", {
	blocks: [{ type: "section", text: { type: "plain_text", text: "SDK validated" } }],
});
assert.equal(validBlocks.ok, true);
const validMessageBlocks = await client.apiCall("blocks.validate", {
	message: { text: "fallback", blocks: [{ type: "divider" }] },
});
assert.equal(validMessageBlocks.ok, true);
const validViewBlocks = await client.apiCall("blocks.validate", {
	view: {
		type: "modal",
		title: { type: "plain_text", text: "SDK view" },
		blocks: [{ type: "section", text: { type: "mrkdwn", text: "*Valid*" } }],
	},
});
assert.equal(validViewBlocks.ok, true);

// @slack/web-api 8.0.0 does not currently generate convenience methods for
// apps.datastore.*, so qualify the documented methods through WebClient's
// supported raw-method path. This still exercises the official SDK's JSON
// argument serialization, bearer authentication, error handling, and response
// decoding against the current Slack wire contract.
const datastorePut = await client.apiCall("apps.datastore.put", {
	datastore: "incidents",
	item: { id: "SDK-1", title: "Investigate", priority: 1 },
});
assert.equal(datastorePut.ok, true);
assert.deepEqual(datastorePut.item, { id: "SDK-1", priority: 1, title: "Investigate" });
const datastoreGet = await client.apiCall("apps.datastore.get", {
	datastore: "incidents",
	id: "SDK-1",
});
assert.equal(datastoreGet.ok, true);
assert.equal(datastoreGet.item.title, "Investigate");
const datastoreUpdate = await client.apiCall("apps.datastore.update", {
	datastore: "incidents",
	item: { id: "SDK-1", priority: 2 },
});
assert.equal(datastoreUpdate.ok, true);
assert.equal(datastoreUpdate.item.title, "Investigate");
assert.equal(datastoreUpdate.item.priority, 2);
const datastoreBulkPut = await client.apiCall("apps.datastore.bulkPut", {
	datastore: "incidents",
	items: [
		{ id: "SDK-2", title: "Mitigate" },
		{ id: "SDK-3", title: "Recover", priority: 3 },
	],
});
assert.equal(datastoreBulkPut.ok, true);
assert.deepEqual(datastoreBulkPut.failed_items, []);
const datastoreBulkGet = await client.apiCall("apps.datastore.bulkGet", {
	datastore: "incidents",
	ids: ["SDK-3", "missing", "SDK-1"],
});
assert.equal(datastoreBulkGet.ok, true);
assert.deepEqual(datastoreBulkGet.items.map((item) => item.id), ["SDK-3", "SDK-1"]);
const datastoreQueryPageOne = await client.apiCall("apps.datastore.query", {
	datastore: "incidents",
	expression: "#priority >= :minimum",
	expression_attributes: { "#priority": "priority" },
	expression_values: { ":minimum": 3 },
	limit: 1,
});
assert.equal(datastoreQueryPageOne.ok, true);
assert.deepEqual(datastoreQueryPageOne.items, []);
assert.equal(typeof datastoreQueryPageOne.response_metadata.next_cursor, "string");
assert.notEqual(datastoreQueryPageOne.response_metadata.next_cursor, "");
const datastoreCount = await client.apiCall("apps.datastore.count", {
	datastore: "incidents",
	expression: "contains (#title, :term)",
	expression_attributes: { "#title": "title" },
	expression_values: { ":term": "i" },
});
assert.equal(datastoreCount.ok, true);
assert.equal(datastoreCount.count, 2);
const datastoreBulkDelete = await client.apiCall("apps.datastore.bulkDelete", {
	datastore: "incidents",
	ids: ["SDK-2", "SDK-3"],
});
assert.equal(datastoreBulkDelete.ok, true);
assert.deepEqual(datastoreBulkDelete.failed_items, []);
assert.equal((await client.apiCall("apps.datastore.delete", {
	datastore: "incidents",
	id: "SDK-1",
})).ok, true);
const missingDatastoreItem = await client.apiCall("apps.datastore.get", {
	datastore: "incidents",
	id: "SDK-1",
});
assert.equal(missingDatastoreItem.ok, true);
assert.deepEqual(missingDatastoreItem.item, {});

// Exercise the current typed App Manifest surface, not only SameOldChat's raw
// HTTP handlers. This catches argument serialization and response-shape drift
// in @slack/web-api itself.
const qualificationManifest = {
	display_information: { name: "SDK Manifest Qualification", description: "created by the official SDK" },
	oauth_config: {
		redirect_urls: ["https://example.com/oauth"],
		scopes: { bot: ["chat:write"] },
	},
	settings: { socket_mode_enabled: true },
};
const manifestValidation = await configurationClient.apps.manifest.validate({ manifest: qualificationManifest });
assert.equal(manifestValidation.ok, true);
assert.deepEqual(manifestValidation.errors, []);
const manifestCreation = await configurationClient.apps.manifest.create({ manifest: qualificationManifest });
assert.equal(manifestCreation.ok, true);
assert.match(manifestCreation.app_id, /^A/);
assert.equal(typeof manifestCreation.credentials.client_secret, "string");
assert.equal(typeof manifestCreation.credentials.signing_secret, "string");
assert.equal(typeof manifestCreation.oauth_authorize_url, "string");
const manifestExport = await configurationClient.apps.manifest.export({ app_id: manifestCreation.app_id });
assert.equal(manifestExport.ok, true);
assert.equal(manifestExport.manifest.display_information.name, "SDK Manifest Qualification");
const updatedQualificationManifest = structuredClone(qualificationManifest);
updatedQualificationManifest.display_information.name = "SDK Manifest Updated";
updatedQualificationManifest.oauth_config.scopes.bot.push("commands");
const manifestUpdate = await configurationClient.apps.manifest.update({
	app_id: manifestCreation.app_id,
	manifest: updatedQualificationManifest,
});
assert.equal(manifestUpdate.ok, true);
assert.equal(manifestUpdate.permissions_updated, true);
const rotatedConfiguration = await configurationClient.tooling.tokens.rotate({ refresh_token: "xoxe-qualification" });
assert.equal(rotatedConfiguration.ok, true);
assert.match(rotatedConfiguration.token, /^xoxe\.xoxp-/);
assert.match(rotatedConfiguration.refresh_token, /^xoxe-/);
assert.equal(rotatedConfiguration.team_id, "T1");
assert.equal(rotatedConfiguration.user_id, "U1");
const rotatedConfigurationClient = new WebClient(rotatedConfiguration.token, clientOptions);
const manifestDeletion = await rotatedConfigurationClient.apps.manifest.delete({ app_id: manifestCreation.app_id });
assert.equal(manifestDeletion.ok, true);

const identity = await client.auth.test();
assert.equal(identity.ok, true);
assert.equal(identity.team_id, "T1");
assert.equal(identity.user_id, "U1");
assert.equal(identity.bot_id, "B1");
assert.equal(identity.is_enterprise_install, false);
assert.equal((await client.apiCall("entity.presentDetails", {
	trigger_id: "entity-details-trigger",
	metadata: { entity_type: "slack#/entities/file" },
	user_auth_required: true,
	user_auth_url: "https://example.com/login",
})).ok, true);
assert.equal((await client.apiCall("entity.presentComments", {
	trigger_id: "entity-comments-trigger",
	comments: [{ id: "comment-1", can_delete: true }],
	delete_action_id: "delete-comment",
})).ok, true);
assert.equal((await client.apiCall("entity.acknowledgeCommentAction", {
	trigger_id: "entity-ack-trigger",
	comment: { id: "comment-1", value: "saved" },
})).ok, true);
const createdList = await client.apiCall("slackLists.create", {
	name: "SDK qualification list",
	description_blocks: [{ type: "rich_text", elements: [] }],
	schema: [{ key: "title", name: "Title", type: "text", is_primary_column: true }],
});
assert.equal(createdList.ok, true);
assert.match(createdList.list.id, /^F/);
const createdListItem = await client.apiCall("slackLists.items.create", {
	list_id: createdList.list.id,
	initial_fields: [{ column_id: "title", value: "first row" }],
});
assert.equal(createdListItem.ok, true);
assert.match(createdListItem.item.id, /^Rec/);
const listItemInfo = await client.apiCall("slackLists.items.info", {
	list_id: createdList.list.id,
	id: createdListItem.item.id,
});
assert.equal(listItemInfo.ok, true);
assert.equal(listItemInfo.item.id, createdListItem.item.id);
const updatedList = await client.apiCall("slackLists.update", {
	id: createdList.list.id,
	name: "SDK qualification list updated",
	todo_mode: true,
});
assert.equal(updatedList.ok, true);
assert.equal(updatedList.list.name, "SDK qualification list updated");
const listedItems = await client.apiCall("slackLists.items.list", { list_id: createdList.list.id, limit: 10 });
assert.equal(listedItems.ok, true);
assert.equal(listedItems.items.length, 1);
assert.equal((await client.apiCall("slackLists.items.update", {
	list_id: createdList.list.id,
	cells: [{ row_id: createdListItem.item.id, column_id: "title", value: "updated row" }],
})).ok, true);
assert.equal((await client.apiCall("slackLists.access.set", {
	list_id: createdList.list.id,
	access_level: "read",
	channel_ids: ["C1"],
})).ok, true);
assert.equal((await client.apiCall("slackLists.access.delete", {
	list_id: createdList.list.id,
	channel_ids: ["C1"],
})).ok, true);
const listItemTwo = await client.apiCall("slackLists.items.create", {
	list_id: createdList.list.id,
	initial_fields: [{ column_id: "title", value: "second row" }],
});
const listItemThree = await client.apiCall("slackLists.items.create", {
	list_id: createdList.list.id,
	initial_fields: [{ column_id: "title", value: "third row" }],
});
assert.equal(listItemTwo.ok, true);
assert.equal(listItemThree.ok, true);
assert.equal((await client.apiCall("slackLists.items.deleteMultiple", {
	list_id: createdList.list.id,
	ids: [listItemTwo.item.id, listItemThree.item.id],
})).ok, true);
const startedListDownload = await client.apiCall("slackLists.download.start", { list_id: createdList.list.id, include_archived: true });
assert.equal(startedListDownload.ok, true);
const listDownload = await client.apiCall("slackLists.download.get", {
	list_id: createdList.list.id,
	job_id: startedListDownload.job_id,
});
assert.equal(listDownload.ok, true);
assert.equal(listDownload.status, "COMPLETED");
assert.equal(listDownload.download_url.includes("/internal/slack-lists/download.csv"), true);
assert.equal((await client.apiCall("slackLists.items.delete", {
	list_id: createdList.list.id,
	id: createdListItem.item.id,
})).ok, true);
const bot = await client.bots.info({ bot: "B1" });
assert.equal(bot.ok, true);
assert.equal(bot.bot.id, "B1");
const accessLogs = await client.team.accessLogs({ count: 1 });
assert.equal(accessLogs.ok, true);
assert.equal(Array.isArray(accessLogs.logins), true);
const billableInfo = await client.team.billableInfo({ user: "U1" });
assert.equal(billableInfo.ok, true);
assert.equal(billableInfo.billable_info.U1.billing_active, true);
const integrationLogs = await client.team.integrationLogs({ count: 1 });
assert.equal(integrationLogs.ok, true);
// team.externalTeams.* is the whole-organization half of Slack Connect. The
// walk exercises the read and the refusal: this fixture shares no channels with
// another organization, so disconnecting one is the "no such connection" answer
// rather than a success, which is the case worth pinning — reporting success
// would tell an administrator they had ended a connection that never existed.
const externalTeams = await client.team.externalTeams.list({ limit: 10 });
assert.equal(externalTeams.ok, true);
assert.equal(Array.isArray(externalTeams.organizations), true);
await assert.rejects(
  () => client.team.externalTeams.disconnect({ target_team: "T-not-connected" }),
  (error) => String(error).includes("team_not_found"),
);
const migration = await client.migration.exchange({ users: "U1" });
assert.equal(migration.ok, true);
assert.equal(migration.user_id_map.U1, "W1");
const reverseMigration = await client.migration.exchange({ users: "W1", to_old: true });
assert.equal(reverseMigration.ok, true);
assert.equal(reverseMigration.user_id_map.W1, "U1");
const oauth = await client.oauth.access({
  client_id: "qualification-client",
  client_secret: "qualification-secret",
  code: "qualification-code",
  redirect_uri: "https://example.com/oauth",
});
assert.equal(oauth.ok, true);
assert.equal(typeof oauth.access_token, "string");
const oauthV2 = await client.oauth.v2.access({
  client_id: "qualification-client",
  client_secret: "qualification-secret",
  code: "qualification-v2-code",
  redirect_uri: "https://example.com/oauth",
});
assert.equal(oauthV2.ok, true);
assert.equal(oauthV2.authed_user.id, "U1");
assert.equal(oauthV2.token_type, "bot");
assert.equal(oauthV2.bot_user_id, "U1");
assert.equal(oauthV2.access_token.startsWith("xoxe.xoxb-"), true);
assert.equal(oauthV2.refresh_token.startsWith("xoxe-"), true);
assert.equal(oauthV2.expires_in, 43200);
assert.equal(oauthV2.authed_user.access_token.startsWith("xoxe.xoxp-"), true);
assert.equal(oauthV2.authed_user.refresh_token.startsWith("xoxe-"), true);
assert.equal(oauthV2.authed_user.expires_in, 43200);
assert.equal(oauthV2.authed_user.token_type, "user");
assert.equal(typeof oauthV2.authed_user.scope, "string");
const oauthV2Refreshed = await client.oauth.v2.access({
  client_id: "qualification-client",
  client_secret: "qualification-secret",
  grant_type: "refresh_token",
  refresh_token: oauthV2.refresh_token,
});
assert.equal(oauthV2Refreshed.ok, true);
assert.equal(oauthV2Refreshed.token_type, "bot");
assert.equal(oauthV2Refreshed.access_token.startsWith("xoxe.xoxb-"), true);
assert.equal(oauthV2Refreshed.refresh_token.startsWith("xoxe-"), true);
assert.notEqual(oauthV2Refreshed.refresh_token, oauthV2.refresh_token);
assert.equal(oauthV2Refreshed.expires_in, 43200);
const oauthV2User = await client.apiCall("oauth.v2.user.access", {
  client_id: "qualification-client",
  client_secret: "qualification-secret",
  code: "qualification-v2-user-code",
  redirect_uri: "https://example.com/oauth",
});
assert.equal(oauthV2User.ok, true);
assert.equal(oauthV2User.access_token, undefined);
assert.equal(oauthV2User.authed_user.id, "U1");
assert.equal(oauthV2User.authed_user.token_type, "user");
assert.equal(oauthV2User.authed_user.access_token.startsWith("xoxe.xoxp-"), true);
assert.equal(oauthV2User.authed_user.refresh_token.startsWith("xoxe-"), true);
assert.equal(oauthV2User.authed_user.expires_in, 43200);
const oauthV2Exchanged = await client.oauth.v2.exchange({
  client_id: "qualification-client",
  client_secret: "qualification-secret",
  token: "xoxb-qualification-legacy",
});
assert.equal(oauthV2Exchanged.ok, true);
assert.equal(oauthV2Exchanged.token_type, "bot");
assert.equal(oauthV2Exchanged.access_token.startsWith("xoxe.xoxb-"), true);
assert.equal(oauthV2Exchanged.refresh_token.startsWith("xoxe-"), true);
assert.equal(oauthV2Exchanged.expires_in, 43200);
const oauthToken = await client.apiCall("oauth.token", {
	client_id: "qualification-client",
	client_secret: "qualification-secret",
	code: "qualification-token-code",
	redirect_uri: "https://example.com/oauth",
});
assert.equal(oauthToken.ok, true);
assert.equal(typeof oauthToken.access_token, "string");
const openidToken = await client.apiCall("openid.connect.token", {
	client_id: "qualification-client",
	client_secret: "qualification-secret",
	code: "qualification-openid-code",
	redirect_uri: "https://example.com/oauth",
	grant_type: "authorization_code",
});
assert.equal(openidToken.ok, true);
assert.equal(openidToken.token_type, "Bearer");
assert.equal(typeof openidToken.id_token, "string");
assert.equal(typeof openidToken.refresh_token, "string");
const openidInfo = await client.apiCall("openid.connect.userInfo", { token: openidToken.access_token });
assert.equal(openidInfo.ok, true);
assert.equal(openidInfo.sub, "U1");
assert.equal(openidInfo["https://slack.com/team_id"], "T1");
const refreshedOpenIDToken = await client.apiCall("openid.connect.token", {
	client_id: "qualification-client",
	client_secret: "qualification-secret",
	grant_type: "refresh_token",
	refresh_token: openidToken.refresh_token,
});
assert.equal(refreshedOpenIDToken.ok, true);
assert.notEqual(refreshedOpenIDToken.access_token, openidToken.access_token);
const eventContextResponse = await fetch(new URL("../qualification/event-context", process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/"));
assert.equal(eventContextResponse.ok, true);
const eventContext = await eventContextResponse.text();
const authorizations = await appClient.apps.event.authorizations.list({ event_context: eventContext });
assert.equal(authorizations.ok, true);
assert.equal(authorizations.authorizations[0].team_id, "T1");
assert.equal(authorizations.authorizations[0].is_bot, true);
const adminUsers = await client.admin.users.list({ team_id: "T1", limit: 10 });
assert.equal(adminUsers.ok, true);
assert.equal(adminUsers.users.some((user) => user.id === "U1"), true);
const adminEmoji = await client.admin.emoji.list();
assert.equal(adminEmoji.ok, true);
const adminTeams = await client.admin.teams.list({ limit: 10 });
assert.equal(adminTeams.ok, true);
assert.equal(adminTeams.teams.some((team) => team.id === "T1"), true);
assert.equal((await client.admin.emoji.add({ name: "qualified", url: "https://example.com/qualified.png" })).ok, true);
assert.equal((await client.admin.emoji.addAlias({ name: "qualified-alias", alias_for: "qualified" })).ok, true);
assert.equal((await client.admin.emoji.rename({ name: "qualified", new_name: "qualified-renamed" })).ok, true);
assert.equal((await client.admin.emoji.remove({ name: "qualified-alias" })).ok, true);
assert.equal((await client.admin.emoji.remove({ name: "qualified-renamed" })).ok, true);
assert.equal((await client.admin.conversations.rename({ channel_id: "C2", name: "renamed-lifecycle" })).ok, true);
assert.equal((await client.admin.conversations.archive({ channel_id: "C2" })).ok, true);
assert.equal((await client.admin.conversations.unarchive({ channel_id: "C2" })).ok, true);
const adminTeamAdmins = await client.admin.teams.admins.list({ team_id: "T1", limit: 10 });
assert.equal(adminTeamAdmins.ok, true);
assert.equal(adminTeamAdmins.admin_ids.includes("U2"), true);
const adminTeamOwners = await client.admin.teams.owners.list({ team_id: "T1", limit: 10 });
assert.equal(adminTeamOwners.ok, true);
assert.equal(adminTeamOwners.owner_ids.includes("U1"), true);
const createdAdminTeam = await client.admin.teams.create({
	team_domain: "sdk-created-workspace",
	team_name: "SDK Created Workspace",
	team_description: "created by SDK qualification",
	team_discoverability: "closed",
});
assert.equal(createdAdminTeam.ok, true);
assert.equal(typeof createdAdminTeam.team, "string");
const adminTeamSettings = await client.admin.teams.settings.info({ team_id: "T1" });
assert.equal(adminTeamSettings.ok, true);
assert.equal(adminTeamSettings.team.id, "T1");
assert.equal(adminTeamSettings.team.name, "test");
assert.equal((await client.admin.users.setAdmin({ team_id: "T1", user_id: "U2" })).ok, true);
assert.equal((await client.admin.users.setOwner({ team_id: "T1", user_id: "U2" })).ok, true);
assert.equal((await client.admin.users.setRegular({ team_id: "T1", user_id: "U2" })).ok, true);
assert.equal((await client.admin.teams.settings.setName({ team_id: "T1", name: "qualified-test" })).ok, true);
assert.equal((await client.admin.teams.settings.setDescription({ team_id: "T1", description: "qualified description" })).ok, true);
assert.equal((await client.admin.teams.settings.setDiscoverability({ team_id: "T1", discoverability: "closed" })).ok, true);
assert.equal((await client.admin.teams.settings.setIcon({ team_id: "T1", image_url: "https://example.com/qualified.png" })).ok, true);
assert.equal((await client.admin.teams.settings.setDefaultChannels({ team_id: "T1", channel_ids: ["C1"] })).ok, true);
const inviteRequests = await client.admin.inviteRequests.list({ team_id: "T1", limit: 10 });
assert.equal(inviteRequests.ok, true);
assert.equal(Array.isArray(inviteRequests.invite_requests), true);
const approvedInviteRequests = await client.admin.inviteRequests.approved.list({ team_id: "T1", limit: 10 });
assert.equal(approvedInviteRequests.ok, true);
assert.equal(Array.isArray(approvedInviteRequests.approved_requests), true);
const deniedInviteRequests = await client.admin.inviteRequests.denied.list({ team_id: "T1", limit: 10 });
assert.equal(deniedInviteRequests.ok, true);
assert.equal(Array.isArray(deniedInviteRequests.denied_requests), true);
assert.equal((await client.admin.users.invite({
	team_id: "T1",
	email: "sdk-approve@example.com",
	channel_ids: ["C1"],
	is_restricted: false,
	is_ultra_restricted: false,
})).ok, true);
assert.equal((await client.admin.users.invite({
	team_id: "T1",
	email: "sdk-deny@example.com",
	channel_ids: ["C1"],
	is_restricted: false,
	is_ultra_restricted: false,
})).ok, true);
const pendingInviteRequests = await client.admin.inviteRequests.list({ team_id: "T1", limit: 10 });
const approvalRequest = pendingInviteRequests.invite_requests.find((request) => request.email === "sdk-approve@example.com");
const denialRequest = pendingInviteRequests.invite_requests.find((request) => request.email === "sdk-deny@example.com");
assert.equal(typeof approvalRequest?.id, "string");
assert.equal(typeof denialRequest?.id, "string");
assert.equal((await client.admin.inviteRequests.approve({ team_id: "T1", invite_request_id: approvalRequest.id })).ok, true);
assert.equal((await client.admin.inviteRequests.deny({ team_id: "T1", invite_request_id: denialRequest.id })).ok, true);
const approvedApps = await client.admin.apps.approved.list({ team_id: "T1", limit: 10 });
assert.equal(approvedApps.ok, true);
assert.equal(Array.isArray(approvedApps.approved_apps), true);
const appRequests = await client.admin.apps.requests.list({ team_id: "T1", limit: 10 });
assert.equal(appRequests.ok, true);
assert.equal(Array.isArray(appRequests.app_requests), true);
const restrictedApps = await client.admin.apps.restricted.list({ team_id: "T1", limit: 10 });
assert.equal(restrictedApps.ok, true);
assert.equal(Array.isArray(restrictedApps.restricted_apps), true);
assert.equal((await client.apiCall("apps.permissions.info")).ok, true);
assert.equal((await client.apiCall("apps.permissions.scopes.list")).ok, true);
assert.equal((await client.apiCall("apps.permissions.resources.list", { limit: 10 })).ok, true);
assert.equal((await client.apiCall("apps.permissions.users.list", { limit: 10 })).ok, true);
assert.equal((await client.apiCall("apps.permissions.request", {
	scopes: "channels:read",
	trigger_id: "permission-trigger",
})).ok, true);
assert.equal((await client.apiCall("apps.permissions.users.request", {
	scopes: "channels:read",
	trigger_id: "permission-user-trigger",
	user: "U1",
})).ok, true);
assert.equal((await client.admin.apps.approve({ app_id: "A1", team_id: "T1" })).ok, true);
assert.equal((await client.admin.apps.restrict({ app_id: "A1", team_id: "T1" })).ok, true);
const adminInvite = await client.admin.conversations.invite({ channel_id: "C2", users: "U2" });
assert.equal(adminInvite.ok, true);
const searchedConversations = await client.admin.conversations.search({ query: "general", limit: 10 });
assert.equal(searchedConversations.ok, true);
assert.equal(searchedConversations.conversations.some((conversation) => conversation.id === "C1"), true);
const setConversationPrefs = await client.admin.conversations.setConversationPrefs({
  channel_id: "C1",
  prefs: { can_thread: { type: ["everyone"] }, who_can_post: { type: ["everyone"] } },
});
assert.equal(setConversationPrefs.ok, true);
const conversationPrefs = await client.admin.conversations.getConversationPrefs({ channel_id: "C1" });
assert.equal(conversationPrefs.ok, true);
assert.equal(typeof conversationPrefs.prefs, "object");
const conversationTeams = await client.admin.conversations.getTeams({ channel_id: "C1", limit: 10 });
assert.equal(conversationTeams.ok, true);
assert.equal(conversationTeams.team_ids.includes("T1"), true);
// Web API 8 removed the typed Workflow Steps from Apps helpers after Slack
// retired that platform. SameOldChat deliberately preserves the legacy HTTP
// methods, so qualify them through WebClient.apiCall instead of pinning an old
// SDK merely to retain removed convenience functions.
const completedStep = await client.apiCall("workflows.stepCompleted", {
  workflow_step_execute_id: "qualification-execute",
  outputs: { answer: "ok" },
});
assert.equal(completedStep.ok, true);
const failedStep = await client.apiCall("workflows.stepFailed", {
  workflow_step_execute_id: "qualification-failed",
  error: { message: "qualification failure" },
});
assert.equal(failedStep.ok, true);
const updatedStep = await client.apiCall("workflows.updateStep", {
  workflow_step_edit_id: "qualification-edit",
  inputs: { input: { value: "qualification" } },
  outputs: [{ type: "text", name: "answer", label: "Answer" }],
});
assert.equal(updatedStep.ok, true);
const functionPermission = await workflowClient.apiCall("functions.distributions.permissions.set", {
  function_callback_id: "triage",
  function_app_id: "A3",
  permission_type: "named_entities",
  user_ids: ["U1", "U2"],
});
assert.equal(functionPermission.ok, true);
assert.equal(functionPermission.permission_type, "named_entities");
assert.deepEqual(functionPermission.users.map((user) => user.user_id), ["U1", "U2"]);
assert.equal((await workflowClient.apiCall("functions.distributions.permissions.add", {
  function_id: "FnB4D6AFBF12045549",
  user_ids: ["U3"],
})).ok, true);
assert.equal((await workflowClient.apiCall("functions.distributions.permissions.remove", {
  function_id: "FnB4D6AFBF12045549",
  user_ids: ["U3"],
})).ok, true);
const listedFunctionPermission = await workflowClient.apiCall("functions.distributions.permissions.list", {
  function_id: "FnB4D6AFBF12045549",
});
assert.equal(listedFunctionPermission.ok, true);
assert.deepEqual(listedFunctionPermission.users.map((user) => user.user_id), ["U1", "U2"]);
const triggerPermission = await workflowClient.apiCall("workflows.triggers.permissions.set", {
  trigger_id: "FtQualification",
  permission_type: "named_entities",
  user_ids: ["U1", "U2"],
});
assert.equal(triggerPermission.ok, true);
assert.equal((await workflowClient.apiCall("workflows.triggers.permissions.add", {
  trigger_id: "FtQualification",
  user_ids: ["U3"],
})).ok, true);
assert.equal((await workflowClient.apiCall("workflows.triggers.permissions.remove", {
  trigger_id: "FtQualification",
  user_ids: ["U3"],
})).ok, true);
assert.equal((await workflowClient.apiCall("workflows.triggers.permissions.set", {
  trigger_id: "FtQualification",
  permission_type: "everyone",
})).ok, true);
assert.equal((await workflowClient.apiCall("workflows.triggers.permissions.list", {
  trigger_id: "FtQualification",
})).permission_type, "everyone");
assert.equal((await workflowClient.apiCall("workflows.featured.set", {
  channel_id: "C1",
  trigger_ids: ["FtQualification"],
})).ok, true);
const featuredWorkflows = await workflowClient.apiCall("workflows.featured.list", { channel_ids: ["C1"] });
assert.equal(featuredWorkflows.ok, true);
assert.equal(featuredWorkflows.featured_workflows[0].triggers[0].id, "FtQualification");
assert.equal((await workflowClient.apiCall("workflows.featured.remove", {
  channel_id: "C1",
  trigger_ids: ["FtQualification"],
})).ok, true);
assert.equal((await workflowClient.apiCall("workflows.featured.add", {
  channel_id: "C1",
  trigger_ids: ["FtQualification"],
})).ok, true);
const functionSteps = await workflowClient.apiCall("functions.workflows.steps.list", {
  function_id: "FnB4D6AFBF12045549",
  workflow_id: "WfQualification",
});
assert.equal(functionSteps.ok, true);
assert.equal(functionSteps.steps_versions[0].title, "Triage");
assert.equal((await workflowClient.apiCall("functions.completeSuccess", {
  function_execution_id: "FxQualification",
  outputs: { result: "qualified by Node" },
})).ok, true);
assert.equal((await workflowClient.apiCall("functions.completeError", {
  function_execution_id: "FxQualificationError",
  error: "qualified failure from Node",
})).ok, true);
const openedDialog = await client.dialog.open({
  trigger_id: "qualification-dialog-trigger",
  dialog: {
    callback_id: "qualification-dialog",
    title: "Qualification",
    submit_label: "Submit",
    elements: [{ type: "text", name: "answer", label: "Answer" }],
  },
});
assert.equal(openedDialog.ok, true);
const openedView = await client.views.open({
  trigger_id: "qualification-open-trigger",
  view: {
    type: "modal",
    callback_id: "qualification",
    title: { type: "plain_text", text: "Qualification" },
    blocks: [],
  },
});
assert.equal(openedView.ok, true);
assert.equal(typeof openedView.view.id, "string");
const publishedView = await client.views.publish({ user_id: "U1", view: { type: "home", blocks: [] } });
assert.equal(publishedView.ok, true);
const pushedView = await client.views.push({
  trigger_id: "qualification-push-trigger",
  view: {
    type: "modal",
    callback_id: "qualification-pushed",
    title: { type: "plain_text", text: "Pushed qualification" },
    blocks: [],
  },
});
assert.equal(pushedView.ok, true);
const updatedView = await client.views.update({
  view_id: openedView.view.id,
  view: { ...openedView.view, callback_id: "qualification-updated" },
});
assert.equal(updatedView.ok, true);
const addedCall = await client.calls.add({
  external_unique_id: "qualification-call",
  external_display_id: "qualification",
  join_url: "https://example.com/call",
  desktop_app_join_url: "https://example.com/call-desktop",
  title: "Qualification call",
  date_start: Math.floor(Date.now() / 1000),
});
assert.equal(addedCall.ok, true);
const callId = addedCall.call.id;
const callInfo = await client.calls.info({ id: callId });
assert.equal(callInfo.ok, true);
const updatedCall = await client.calls.update({ id: callId, title: "Updated qualification call" });
assert.equal(updatedCall.ok, true);
const addedCallParticipant = await client.calls.participants.add({ id: callId, users: [{ slack_id: "U2" }] });
assert.equal(addedCallParticipant.ok, true);
const removedCallParticipant = await client.calls.participants.remove({ id: callId, users: [{ slack_id: "U2" }] });
assert.equal(removedCallParticipant.ok, true);
const endedCall = await client.calls.end({ id: callId, duration: 30 });
assert.equal(endedCall.ok, true);

const posted = await client.chat.postMessage({ channel: "C1", text: "node sdk qualification" });
assert.equal(posted.ok, true);
assert.equal(posted.channel, "C1");
assert.equal(typeof posted.ts, "string");

const updated = await client.chat.update({ channel: "C1", ts: posted.ts, text: "node sdk qualification updated" });
assert.equal(updated.ok, true);
const deleted = await client.chat.delete({ channel: "C1", ts: posted.ts });
assert.equal(deleted.ok, true);

// Slack's chat.update contract is presence-sensitive. These calls go through
// the official client's real array serialization so the suite catches a
// server that treats an omitted field like [] and silently erases rich content.
const richForUpdate = await client.chat.postMessage({
	channel: "C1",
	text: "rich update fallback",
	blocks: [{ type: "section", text: { type: "plain_text", text: "retained block" } }],
	attachments: [{ text: "retained attachment" }],
});
const attachmentsOnly = await client.chat.update({
	channel: "C1",
	ts: richForUpdate.ts,
	attachments: [{ text: "changed attachment" }],
});
assert.equal(attachmentsOnly.message.text, "rich update fallback");
assert.equal(attachmentsOnly.message.blocks[0].text.text, "retained block");
assert.equal(attachmentsOnly.message.attachments[0].text, "changed attachment");
const removeBlocks = await client.chat.update({ channel: "C1", ts: richForUpdate.ts, blocks: [] });
assert.deepEqual(removeBlocks.message.blocks ?? [], []);
assert.equal(removeBlocks.message.attachments[0].text, "changed attachment");
const removeAttachments = await client.chat.update({ channel: "C1", ts: richForUpdate.ts, attachments: [] });
assert.deepEqual(removeAttachments.message.attachments ?? [], []);
assert.equal(removeAttachments.message.text, "rich update fallback");
assert.equal((await client.chat.delete({ channel: "C1", ts: richForUpdate.ts })).ok, true);

const conversation = await client.conversations.info({ channel: "C1" });
assert.equal(conversation.ok, true);
assert.equal(conversation.channel.id, "C1");
const channelCanvas = await client.apiCall("conversations.canvases.create", {
	channel_id: "C1",
	title: "Channel canvas qualification",
	document_content: { type: "markdown", markdown: "# Qualification" },
});
assert.equal(channelCanvas.ok, true);
assert.match(channelCanvas.canvas_id, /^F/);
const conversationWithCanvas = await client.conversations.info({ channel: "C1" });
assert.equal(conversationWithCanvas.channel.properties.canvas.file_id, channelCanvas.canvas_id);
assert.equal(conversationWithCanvas.channel.properties.canvas.is_empty, false);
const members = await client.conversations.members({ channel: "C1", limit: 1 });
assert.equal(members.ok, true);
assert.deepEqual(members.members, ["U1"]);
const conversations = await client.conversations.list({ limit: 1 });
assert.equal(conversations.ok, true);
assert.equal(conversations.channels.length, 1);
const joined = await client.conversations.join({ channel: "C2" });
assert.equal(joined.ok, true);
assert.equal(joined.channel.id, "C2");
const invited = await client.conversations.invite({ channel: "C1", users: "U2" });
assert.equal(invited.ok, true);
const forceInvited = await client.conversations.invite({
	channel: "C1",
	users: "U-missing,U3",
	force: true,
});
assert.equal(forceInvited.ok, true);
const kicked = await client.conversations.kick({ channel: "C1", user: "U2" });
assert.equal(kicked.ok, true);
const privateInvitationChannel = await client.conversations.create({
	name: "sdk-private-invitation",
	is_private: true,
});
assert.equal(privateInvitationChannel.ok, true);
const privateInvited = await client.conversations.invite({
	channel: privateInvitationChannel.channel.id,
	users: "U2",
});
assert.equal(privateInvited.ok, true);
const left = await client.conversations.leave({ channel: "C2" });
assert.equal(left.ok, true);
assert.equal((await client.admin.conversations.convertToPrivate({ channel_id: "C2" })).ok, true);
assert.equal((await client.admin.conversations.delete({ channel_id: "C2" })).ok, true);
const createdAdminConversation = await client.admin.conversations.create({
	name: "sdk-admin-created",
	is_private: true,
	team_id: "T1",
});
assert.equal(createdAdminConversation.ok, true);
assert.equal(typeof createdAdminConversation.channel_id, "string");
assert.equal((await client.admin.conversations.delete({ channel_id: createdAdminConversation.channel_id })).ok, true);
const connectedChannelInfo = await client.admin.conversations.ekm.listOriginalConnectedChannelInfo({
	channel_ids: ["C1"],
	limit: 10,
});
assert.equal(connectedChannelInfo.ok, true);
assert.equal(Array.isArray(connectedChannelInfo.channels), true);
assert.equal((await client.admin.conversations.disconnectShared({ channel_id: "C1", leaving_team_ids: ["T1"] })).ok, true);

// Retention. The workspace default governs a channel with no override; the
// per-channel API sets and removes one; getCustomRetention reports the duration
// that actually applies either way.
const noRetention = await client.admin.conversations.getCustomRetention({ channel_id: "C-retention" });
assert.equal(noRetention.ok, true);
assert.equal(noRetention.is_policy_enabled, false);

assert.equal((await client.admin.conversations.setCustomRetention({ channel_id: "C-retention", duration_days: 30 })).ok, true);
const withRetention = await client.admin.conversations.getCustomRetention({ channel_id: "C-retention" });
assert.equal(withRetention.is_policy_enabled, true);
assert.equal(withRetention.duration_days, 30);

// Slack's bound is greater than zero and below 36500; both ends are refused.
for (const duration of [0, 36500]) {
	await assert.rejects(
		client.admin.conversations.setCustomRetention({ channel_id: "C-retention", duration_days: duration }),
		(error) => error.data.error === "invalid_duration",
	);
}

assert.equal((await client.admin.conversations.removeCustomRetention({ channel_id: "C-retention" })).ok, true);
assert.equal((await client.admin.conversations.getCustomRetention({ channel_id: "C-retention" })).is_policy_enabled, false);

// Slack Connect, walked across the boundary it exists for. The host sends and
// approves; the invited organization accepts, through its own credential —
// doing it all with one token would prove the opposite of what CONNECT-02
// asks for.
const sharedInvite = await client.conversations.inviteShared({ channel: "C1", external_limited: "T2" });
assert.equal(sharedInvite.ok, true);
assert.equal(typeof sharedInvite.invite.id, "string");
assert.equal(sharedInvite.invite.status, "pending");

const requested = await client.conversations.requestSharedInvite.list({});
assert.equal(requested.ok, true);
assert.equal(requested.invites.some((invite) => invite.id === sharedInvite.invite.id), true);

assert.equal((await client.conversations.approveSharedInvite({ invite_id: sharedInvite.invite.id })).ok, true);
const issued = await client.conversations.listConnectInvites({});
assert.equal(issued.ok, true);
assert.equal(issued.invites.some((invite) => invite.id === sharedInvite.invite.id), true);

const accepted = await externalClient.conversations.acceptSharedInvite({ invite_id: sharedInvite.invite.id });
assert.equal(accepted.ok, true);
assert.equal(accepted.is_ext_shared, true);

// conversations.info reports the connection, and no longer reports it pending.
const connectedInfo = await client.conversations.info({ channel: "C1" });
assert.equal(connectedInfo.channel.is_ext_shared, true);
assert.equal(connectedInfo.channel.is_pending_ext_shared, false);

assert.equal((await client.conversations.externalInvitePermissions.set({
	channel: "C1",
	target_team: "T2",
	action: "downgrade",
})).ok, true);

// Denying and declining are different outcomes, so both are exercised.
const denied = await client.conversations.inviteShared({ channel: "C1", external_limited: "T3" });
assert.equal((await client.conversations.requestSharedInvite.deny({ invite_id: denied.invite.id })).ok, true);

const toDecline = await client.conversations.inviteShared({ channel: "C1", external_limited: "T3" });
// Slack publishes the same host approval under a request-oriented name too, so
// both reach the same decision here.
assert.equal((await client.conversations.requestSharedInvite.approve({ invite_id: toDecline.invite.id })).ok, true);
// Declined by the organization it was actually sent to: an invitation names
// one organization, and only that one may answer it.
assert.equal((await thirdOrgClient.conversations.declineSharedInvite({ invite_id: toDecline.invite.id })).ok, true);
assert.equal((await client.admin.conversations.setTeams({
	channel_id: "C1",
	org_channel: false,
	target_team_ids: ["T1"],
})).ok, true);
const restrictedConversation = await client.admin.conversations.create({
	name: "sdk-restricted-private",
	is_private: true,
	team_id: "T1",
});
assert.equal(restrictedConversation.ok, true);
const accessGroup = await client.usergroups.create({
	name: "SDK Access Group",
	handle: "sdk-access-group",
	team_id: "T1",
});
assert.equal(accessGroup.ok, true);
assert.equal(typeof accessGroup.usergroup.id, "string");
const accessGroupID = accessGroup.usergroup.id;
assert.equal((await client.admin.conversations.restrictAccess.addGroup({
	channel_id: restrictedConversation.channel_id,
	group_id: accessGroupID,
	team_id: "T1",
})).ok, true);
const accessGroups = await client.admin.conversations.restrictAccess.listGroups({
	channel_id: restrictedConversation.channel_id,
	team_id: "T1",
});
assert.equal(accessGroups.ok, true);
assert.deepEqual(accessGroups.group_ids, [accessGroupID]);
assert.equal((await client.admin.conversations.restrictAccess.removeGroup({
	channel_id: restrictedConversation.channel_id,
	group_id: accessGroupID,
	team_id: "T1",
})).ok, true);
assert.equal((await client.admin.conversations.delete({ channel_id: restrictedConversation.channel_id })).ok, true);
assert.equal((await client.usergroups.disable({ usergroup: accessGroupID, team_id: "T1" })).ok, true);
// Web API 8 removed the retired files.upload helper. filesUploadV2 exercises
// the current three-step upload protocol through the SDK's public convenience
// method and therefore qualifies both the method pair and the byte upload URL.
const uploadedFile = await client.filesUploadV2({
	filename: "sdk-upload.txt",
	content: "sdk upload",
	title: "SDK upload",
});
assert.equal(uploadedFile.ok, true);
const uploadedFileMetadata = uploadedFile.files[0].files[0];
assert.equal(typeof uploadedFileMetadata.id, "string");
const files = await client.files.list({ count: 10 });
assert.equal(files.ok, true);
assert.equal(files.files.length, 2);
const fileId = uploadedFileMetadata.id;
const qualificationFile = files.files.find((file) => file.name === "qualification.txt");
assert.notEqual(qualificationFile, undefined);
const deletedComment = await client.files.comments.delete({ file: qualificationFile.id, id: "FC1" });
assert.equal(deletedComment.ok, true);
const fileInfo = await client.files.info({ file: fileId });
assert.equal(fileInfo.ok, true);
assert.equal(fileInfo.file.id, fileId);
const publicFile = await client.files.sharedPublicURL({ file: fileId });
assert.equal(publicFile.ok, true);
assert.equal(typeof publicFile.permalink_public, "string");
const revokedPublicFile = await client.files.revokePublicURL({ file: fileId });
assert.equal(revokedPublicFile.ok, true);
const deletedFile = await client.files.delete({ file: fileId });
assert.equal(deletedFile.ok, true);
const remoteFile = await client.files.remote.add({
  external_id: "remote-qualification",
  title: "Remote qualification",
  filetype: "text",
  external_url: "https://example.com/qualification",
});
assert.equal(remoteFile.ok, true);
assert.equal(remoteFile.file.external_id, "remote-qualification");
const remoteInfo = await client.files.remote.info({ external_id: "remote-qualification" });
assert.equal(remoteInfo.ok, true);
const remoteList = await client.files.remote.list({ limit: 1 });
assert.equal(remoteList.ok, true);
assert.equal(remoteList.files.length, 1);
const remoteUpdate = await client.files.remote.update({ external_id: "remote-qualification", title: "Updated remote qualification" });
assert.equal(remoteUpdate.ok, true);
const remoteShare = await client.files.remote.share({ external_id: "remote-qualification", channels: "C1" });
assert.equal(remoteShare.ok, true);
assert.deepEqual(remoteShare.file.channels, ["C1"]);
const remoteRemove = await client.files.remote.remove({ external_id: "remote-qualification" });
assert.equal(remoteRemove.ok, true);
const bookmark = await client.bookmarks.add({ channel_id: "C1", title: "SDK bookmark", type: "link", link: "https://example.com/bookmark", emoji: ":link:" });
assert.equal(bookmark.ok, true);
assert.equal(typeof bookmark.bookmark.id, "string");
const bookmarks = await client.bookmarks.list({ channel_id: "C1" });
assert.equal(bookmarks.ok, true);
assert.equal(bookmarks.bookmarks.length, 1);
const editedBookmark = await client.bookmarks.edit({ channel_id: "C1", bookmark_id: bookmark.bookmark.id, title: "Updated SDK bookmark" });
assert.equal(editedBookmark.ok, true);
assert.equal(editedBookmark.bookmark.title, "Updated SDK bookmark");
const removedBookmark = await client.bookmarks.remove({ channel_id: "C1", bookmark_id: bookmark.bookmark.id });
assert.equal(removedBookmark.ok, true);
const scheduledRoot = await client.chat.postMessage({ channel: "C1", text: "scheduled thread root" });
const scheduledPostAt = Math.floor(Date.now() / 1000) + 60;
const scheduled = await client.chat.scheduleMessage({
	channel: "C1",
	text: "scheduled qualification",
	post_at: scheduledPostAt,
	thread_ts: scheduledRoot.ts,
});
assert.equal(scheduled.ok, true);
assert.equal(typeof scheduled.scheduled_message_id, "string");
const scheduledList = await client.chat.scheduledMessages.list({
	channel: "C1",
	limit: 10,
	oldest: String(scheduledPostAt - 1),
	latest: String(scheduledPostAt + 1),
});
assert.equal(scheduledList.ok, true);
assert.equal(scheduledList.scheduled_messages.length, 1);
assert.equal(scheduledList.scheduled_messages[0].thread_ts, scheduledRoot.ts);
const deletedScheduled = await client.chat.deleteScheduledMessage({
	channel: "C1",
	scheduled_message_id: scheduled.scheduled_message_id,
});
assert.equal(deletedScheduled.ok, true);
assert.equal((await client.chat.delete({ channel: "C1", ts: scheduledRoot.ts })).ok, true);
const dndInfo = await client.dnd.info();
assert.equal(dndInfo.ok, true);
assert.equal(dndInfo.dnd_enabled, false);
const dndSnooze = await client.dnd.setSnooze({ num_minutes: 5 });
assert.equal(dndSnooze.ok, true);
assert.equal(dndSnooze.snooze_enabled, true);
const dndEndSnooze = await client.dnd.endSnooze();
assert.equal(dndEndSnooze.ok, true);
assert.equal(dndEndSnooze.snooze_enabled, false);
const dndEnd = await client.dnd.endDnd();
assert.equal(dndEnd.ok, true);
const dndTeam = await client.dnd.teamInfo();
assert.equal(dndTeam.ok, true);
const rtm = await client.rtm.connect();
assert.equal(rtm.ok, true);
assert.equal(typeof rtm.url, "string");
assert.equal(rtm.team.id, "T1");
assert.equal(rtm.self.id, "U1");
const legacyRtm = await client.apiCall("rtm.start", { no_latest: true, no_unreads: true });
assert.equal(legacyRtm.ok, true);
assert.equal(typeof legacyRtm.url, "string");
assert.equal(legacyRtm.team.id, "T1");
const authorizedTeams = await client.apiCall("auth.teams.list", { limit: 100, include_icon: true });
assert.equal(authorizedTeams.ok, true);
assert.deepEqual(authorizedTeams.teams.map((team) => team.id), ["T1"]);
const teamPreferences = await client.apiCall("team.preferences.list");
assert.equal(teamPreferences.ok, true);
assert.equal(teamPreferences.disable_file_uploads, "allow_all");
assert.equal(teamPreferences.who_can_post_general, "everyone");
// reminders.* is a deprecated user-token surface. Exercise the official
// client's real request and response types with that identity rather than the
// suite's broad bot token, and keep known behavioral gaps executable.
const reminder = await reminderClient.reminders.add({
	text: "reminder qualification",
	time: Math.floor(Date.now() / 1000) + 3600,
});
assert.equal(reminder.ok, true);
assert.equal(typeof reminder.reminder.id, "string");
assert.equal(reminder.reminder.creator, "U1");
assert.equal(reminder.reminder.user, "U1");
assert.equal(reminder.reminder.recurring, false);
assert.equal(reminder.reminder.complete_ts, 0);
await assert.rejects(
	reminderClient.reminders.add({ text: "not another user's reminder", time: 300, user: "U2" }),
	(error) => error?.data?.error === "cannot_add_others",
);
await assert.rejects(
	reminderClient.reminders.add({ text: "documented natural language", time: "in 15 minutes" }),
	(error) => error?.data?.error === "cannot_parse",
);
const reminders = await reminderClient.reminders.list();
assert.equal(reminders.ok, true);
assert.equal(reminders.reminders.length, 1);
const reminderInfo = await reminderClient.reminders.info({ reminder: reminder.reminder.id });
assert.equal(reminderInfo.ok, true);
assert.equal(reminderInfo.reminder.id, reminder.reminder.id);
const completedReminder = await reminderClient.reminders.complete({ reminder: reminder.reminder.id });
assert.equal(completedReminder.ok, true);
const deletedReminder = await reminderClient.reminders.delete({ reminder: reminder.reminder.id });
assert.equal(deletedReminder.ok, true);
const createdCanvas = await client.canvases.create({
	title: "SDK qualification canvas",
	document_content: { type: "h1", markdown: "SDK canvas" },
	channel_id: "C1",
});
assert.equal(createdCanvas.ok, true);
assert.equal(typeof createdCanvas.canvas_id, "string");
const editedCanvas = await client.canvases.edit({
	canvas_id: createdCanvas.canvas_id,
	changes: [{ operation: "insert_at_end", document_content: { type: "paragraph", markdown: "SDK details" } }],
});
assert.equal(editedCanvas.ok, true);
const canvasSections = await client.canvases.sections.lookup({
	canvas_id: createdCanvas.canvas_id,
	criteria: { contains_text: "SDK details" },
});
assert.equal(canvasSections.ok, true);
assert.equal(canvasSections.sections.length, 1);
assert.equal((await client.canvases.access.set({ canvas_id: createdCanvas.canvas_id, access_level: "write", user_ids: ["U1"] })).ok, true);
assert.equal((await client.canvases.access.delete({ canvas_id: createdCanvas.canvas_id, user_ids: ["U1"] })).ok, true);
assert.equal((await client.canvases.delete({ canvas_id: createdCanvas.canvas_id })).ok, true);
const createdUsergroup = await client.usergroups.create({
	name: "Qualification group",
	handle: "qualification-group",
	description: "SDK qualification",
});
assert.equal(createdUsergroup.ok, true);
assert.equal(createdUsergroup.usergroup.is_subteam, true);
const usergroupId = createdUsergroup.usergroup.id;
assert.equal((await client.admin.usergroups.addChannels({ usergroup_id: usergroupId, channel_ids: ["C1"] })).ok, true);
assert.equal((await client.admin.usergroups.addTeams({ usergroup_id: usergroupId, team_ids: ["T1"] })).ok, true);
const adminUsergroupChannels = await client.admin.usergroups.listChannels({ usergroup_id: usergroupId, team_id: "T1" });
assert.equal(adminUsergroupChannels.ok, true);
assert.equal(adminUsergroupChannels.channels.length, 1);
assert.equal(adminUsergroupChannels.channels[0].id, "C1");
assert.equal((await client.admin.usergroups.removeChannels({ usergroup_id: usergroupId, channel_ids: ["C1"] })).ok, true);
const updatedUsergroup = await client.usergroups.update({
	usergroup: usergroupId,
	name: "Updated qualification group",
});
assert.equal(updatedUsergroup.ok, true);
const updatedUsergroupUsers = await client.usergroups.users.update({ usergroup: usergroupId, users: "U1" });
assert.equal(updatedUsergroupUsers.ok, true);
const usergroupUsers = await client.usergroups.users.list({ usergroup: usergroupId });
assert.equal(usergroupUsers.ok, true);
assert.deepEqual(usergroupUsers.users, ["U1"]);
const usergroups = await client.usergroups.list({ include_users: true });
assert.equal(usergroups.ok, true);
assert.equal(usergroups.usergroups.length, 1);
const disabledUsergroup = await client.usergroups.disable({ usergroup: usergroupId });
assert.equal(disabledUsergroup.ok, true);
const enabledUsergroup = await client.usergroups.enable({ usergroup: usergroupId });
assert.equal(enabledUsergroup.ok, true);

const user = await client.users.info({ user: "U1" });
assert.equal(user.ok, true);
assert.equal(user.user.id, "U1");
const profile = await client.users.profile.get({ user: "U1" });
assert.equal(profile.ok, true);
assert.equal(profile.profile.display_name, "alice");
// A real one-pixel PNG. The fixture used to send the ASCII string
// "qualification-photo" named .png; the product now reads the bytes and refuses
// a stream that is not the image it claims to be, which is what stops an
// uploaded document being served back from this origin. A fixture that sends a
// lie asserts the product accepts one.
const image = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==", "base64");
const photo = await client.users.setPhoto({ image });
assert.equal(photo.ok, true);
const deletedPhoto = await client.users.deletePhoto();
assert.equal(deletedPhoto.ok, true);

const root = await client.chat.postMessage({ channel: "C1", text: "thread root" });
assert.equal(root.ok, true);
// Exercise Slack's current high-level ChatStreamer, not only raw method names.
// It buffers fragments, starts on the first flush, appends against the returned
// timestamp, and finalizes with blocks and metadata.
const streamer = client.chatStream({
	channel: "C1",
	thread_ts: root.ts,
	recipient_team_id: "T1",
	recipient_user_id: "U1",
	buffer_size: 1,
	task_display_mode: "dense",
	username: "SDK assistant",
	icon_emoji: ":robot_face:",
});
const firstStreamFlush = await streamer.append({ markdown_text: "**streamed " });
assert.equal(firstStreamFlush?.ok, true);
assert.equal(typeof streamer.ts, "string");
const secondStreamFlush = await streamer.append({
	chunks: [
		{ type: "markdown_text", text: "answer**" },
		{ type: "plan_update", title: "SDK plan" },
		{ type: "task_update", id: "sdk-task", title: "Qualify stream", status: "complete", output: "green" },
	],
});
assert.equal(secondStreamFlush?.ok, true);
const stoppedStream = await streamer.stop({
	markdown_text: " Done.",
	blocks: [{ type: "context", elements: [{ type: "plain_text", text: "Final SDK block" }] }],
	metadata: { event_type: "qualification", event_payload: { sdk: "node" } },
});
assert.equal(stoppedStream.ok, true);
assert.equal(stoppedStream.ts, streamer.ts);
assert.equal(stoppedStream.message.text, "**streamed answer** Done.");
assert.equal(stoppedStream.message.bot_id, "B1");
assert.equal(stoppedStream.message.username, "SDK assistant");
assert.equal(stoppedStream.message.icons.emoji, ":robot_face:");
assert.equal(stoppedStream.message.metadata.event_type, "qualification");
const unfurled = await client.chat.unfurl({
  channel: "C1",
  ts: root.ts,
  unfurls: { "https://example.com/qualification": { text: "unfurled" } },
});
assert.equal(unfurled.ok, true);
const reply = await client.chat.postMessage({ channel: "C1", text: "thread reply", thread_ts: root.ts });
assert.equal(reply.ok, true);
const replies = await client.conversations.replies({ channel: "C1", ts: root.ts, limit: 2 });
assert.equal(replies.ok, true);
assert.equal(replies.messages.length, 2);

const reaction = await client.reactions.add({ channel: "C1", timestamp: root.ts, name: "thumbsup" });
assert.equal(reaction.ok, true);
const reactions = await client.reactions.get({ channel: "C1", timestamp: root.ts });
assert.equal(reactions.ok, true);
assert.equal(reactions.message.reactions.length, 1);
const pinsAdded = await client.pins.add({ channel: "C1", timestamp: root.ts });
assert.equal(pinsAdded.ok, true);
const pins = await client.pins.list({ channel: "C1" });
assert.equal(pins.ok, true);
assert.equal(pins.items.length, 1);
const pinsRemoved = await client.pins.remove({ channel: "C1", timestamp: root.ts });
assert.equal(pinsRemoved.ok, true);
const reactionRemoved = await client.reactions.remove({ channel: "C1", timestamp: root.ts, name: "thumbsup" });
assert.equal(reactionRemoved.ok, true);

const createdConversation = await client.conversations.create({ name: "qualification-tranche" });
assert.equal(createdConversation.ok, true);
const lifecycleChannel = createdConversation.channel.id;
const renamedConversation = await client.conversations.rename({ channel: lifecycleChannel, name: "qualification-renamed" });
assert.equal(renamedConversation.ok, true);
const topic = await client.conversations.setTopic({ channel: lifecycleChannel, topic: "qualification topic" });
assert.equal(topic.ok, true);
const purpose = await client.conversations.setPurpose({ channel: lifecycleChannel, purpose: "qualification purpose" });
assert.equal(purpose.ok, true);
const archived = await client.conversations.archive({ channel: lifecycleChannel });
assert.equal(archived.ok, true);
const unarchived = await client.conversations.unarchive({ channel: lifecycleChannel });
assert.equal(unarchived.ok, true);
const lifecycleInfo = await client.conversations.info({ channel: lifecycleChannel });
assert.equal(lifecycleInfo.ok, true);
assert.equal(lifecycleInfo.channel.name, "qualification-renamed");
assert.equal(lifecycleInfo.channel.topic.value, "qualification topic");
assert.equal(lifecycleInfo.channel.purpose.value, "qualification purpose");

const meMessage = await client.chat.meMessage({ channel: "C1", text: "qualification me message" });
assert.equal(meMessage.ok, true);
const ephemeral = await client.chat.postEphemeral({ channel: "C1", user: "U1", text: "ephemeral qualification" });
assert.equal(ephemeral.ok, true);
assert.equal(typeof ephemeral.message_ts, "string");
const starred = await client.stars.add({ channel: "C1", timestamp: root.ts });
assert.equal(starred.ok, true);
const stars = await client.stars.list({ limit: 10 });
assert.equal(stars.ok, true);
assert.equal(stars.items.length, 1);
const unstarred = await client.stars.remove({ channel: "C1", timestamp: root.ts });
assert.equal(unstarred.ok, true);
const permalink = await client.chat.getPermalink({ channel: "C1", message_ts: root.ts });
assert.equal(permalink.ok, true);
assert.equal(typeof permalink.permalink, "string");
const userReactions = await client.reactions.list({ limit: 1 });
assert.equal(userReactions.ok, true);
const team = await client.team.info();
assert.equal(team.ok, true);
assert.equal(team.team.id, "T1");
const teamProfile = await client.team.profile.get();
assert.equal(teamProfile.ok, true);
assert.deepEqual(teamProfile.profile.fields, []);
const emoji = await client.emoji.list({ include_categories: true });
assert.equal(emoji.ok, true);
assert.equal(emoji.categories_version, "097705020bcf82331c9ef10df3425aad15f5043c");
assert.equal(emoji.categories.some((category) => category.name === "Smileys & Emotion" && category.emoji_names.includes("grinning")), true);
const identityResult = await client.users.identity();
assert.equal(identityResult.ok, true);
assert.equal(identityResult.user.id, "U1");
const byEmail = await client.users.lookupByEmail({ email: "alice@example.com" });
assert.equal(byEmail.ok, true);
assert.equal(byEmail.user.id, "U1");
const presence = await client.users.getPresence({ user: "U1" });
assert.equal(presence.ok, true);
const setPresence = await client.users.setPresence({ presence: "away" });
assert.equal(setPresence.ok, true);
const profileSet = await client.users.profile.set({ profile: { status_text: "qualification", status_emoji: ":wave:", status_expiration: 4102444800 } });
assert.equal(profileSet.ok, true);
assert.equal(profileSet.profile.status_text, "qualification");
assert.equal(profileSet.profile.status_expiration, 4102444800);
const userConversations = await client.users.conversations({ user: "U1", limit: 1 });
assert.equal(userConversations.ok, true);
assert.equal(userConversations.channels.length, 1);
const direct = await client.conversations.open({ users: "U2" });
assert.equal(direct.ok, true);
const closed = await client.conversations.close({ channel: direct.channel.id });
assert.equal(closed.ok, true);
const alreadyClosed = await client.conversations.close({ channel: direct.channel.id });
assert.equal(alreadyClosed.ok, true);
assert.equal(alreadyClosed.no_op, true);
assert.equal(alreadyClosed.already_closed, true);
const reopenedDirect = await client.conversations.open({ users: "U2" });
assert.equal(reopenedDirect.ok, true);
assert.equal(reopenedDirect.channel.id, direct.channel.id);
const groupDirect = await client.conversations.open({ users: "U2,U3" });
assert.equal(groupDirect.ok, true);
const canonicalGroupDirect = await client.conversations.open({ users: "U3,U2" });
assert.equal(canonicalGroupDirect.ok, true);
assert.equal(canonicalGroupDirect.channel.id, groupDirect.channel.id);
const marked = await client.conversations.mark({ channel: "C1", ts: root.ts });
assert.equal(marked.ok, true);

// assistant.threads.*: the argument names come from this SDK's own types
// (channel_id, thread_ts, and title/status/prompts), so a mismatch fails to
// compile against them rather than being discovered by a reader.
const assistantTitle = await client.assistant.threads.setTitle({
  channel_id: "C1", thread_ts: root.ts, title: "Deploy help",
});
assert.equal(assistantTitle.ok, true);
const assistantStatus = await client.assistant.threads.setStatus({
  channel_id: "C1", thread_ts: root.ts, status: "is thinking...",
});
assert.equal(assistantStatus.ok, true);
const assistantPrompts = await client.assistant.threads.setSuggestedPrompts({
  channel_id: "C1", thread_ts: root.ts, title: "Try one",
  prompts: [{ title: "Roll back", message: "How do I roll back?" }],
});
assert.equal(assistantPrompts.ok, true);

const history = await client.conversations.history({ channel: "C1", limit: 10 });
assert.equal(history.ok, true);
assert.equal(history.messages.length >= 3, true);
assert.equal(history.messages.some((message) => message.ts === posted.ts), false);
assert.equal(history.has_more, false);
const search = await reminderClient.search.messages({ query: "thread", count: 999, cursor: "*" });
assert.equal(search.ok, true);
assert.equal(search.messages.matches.length >= 2, true);
assert.equal(search.messages.total >= 2, true);
assert.equal(search.messages.pagination.per_page, 100);
assert.equal(search.messages.paging.count, 100);
assert.equal(search.messages.matches[0].channel.id, "C1");
assert.equal(search.messages.matches[0].channel.name, "general");
assert.equal(search.messages.matches[0].team, "T1");
assert.equal(search.messages.matches[0].username, "alice");
assert.equal(typeof search.messages.matches[0].permalink, "string");
const fileSearch = await reminderClient.search.files({ query: "qualification", count: 10, page: 1 });
assert.equal(fileSearch.ok, true);
assert.equal(fileSearch.files.matches.some((file) => file.name === "qualification.txt"), true);
const allSearch = await reminderClient.search.all({ query: "thread", count: 10, page: 1 });
assert.equal(allSearch.ok, true);
assert.equal(allSearch.messages.matches.length >= 2, true);
assert.equal(Array.isArray(allSearch.files.matches), true);

const users = await client.users.list({ limit: 10 });
assert.equal(users.ok, true);
assert.equal(users.members.length, 3);
assert.equal(users.response_metadata?.next_cursor ?? "", "");
assert.equal((await client.apiCall("users.setActive")).ok, true);
assert.equal((await client.admin.users.assign({
	team_id: "T1",
	user_id: "U2",
	channel_ids: ["C1"],
	is_restricted: false,
	is_ultra_restricted: false,
})).ok, true);
assert.equal((await client.admin.users.setExpiration({
	team_id: "T1",
	user_id: "U2",
	expiration_ts: Math.floor(Date.now() / 1000) + 3600,
})).ok, true);
assert.equal((await client.apiCall("admin.users.session.invalidate", {
	team_id: "T1",
	session_id: "qualification-session",
})).ok, true);
assert.equal((await client.apiCall("admin.users.session.reset", { user_id: "U2" })).ok, true);
assert.equal((await client.admin.users.remove({ team_id: "T1", user_id: "U2" })).ok, true);

// files.getUploadURLExternal hands back a file_id before any bytes exist, and
// the documented flow references the file by that same identifier once
// files.completeUploadExternal returns. Exercising all three steps through the
// official client is what distinguishes a registered handler from a working
// upload.
const externalUpload = await client.files.getUploadURLExternal({
	filename: "external-qualification.txt",
	length: 11,
});
assert.equal(externalUpload.ok, true);
assert.equal(typeof externalUpload.upload_url, "string");
assert.equal(typeof externalUpload.file_id, "string");

const externalUploadResponse = await fetch(`${externalUpload.upload_url}?token=${token}`, {
	method: "POST",
	body: "hello world",
});
assert.equal(externalUploadResponse.ok, true);

const completedExternal = await client.files.completeUploadExternal({
	files: [{ id: externalUpload.file_id, title: "External qualification" }],
	channel_id: "C1",
	initial_comment: "external upload",
});
assert.equal(completedExternal.ok, true);
assert.equal(completedExternal.files.length, 1);
assert.equal(completedExternal.files[0].id, externalUpload.file_id);

const externalInfo = await client.files.info({ file: externalUpload.file_id });
assert.equal(externalInfo.ok, true);
assert.equal(externalInfo.file.id, externalUpload.file_id);
assert.equal(externalInfo.file.title, "External qualification");
assert.equal(externalInfo.file.size, 11);
const externalHistory = await client.conversations.history({ channel: "C1", limit: 100 });
const externalMessages = externalHistory.messages.filter((message) =>
	message.subtype === "file_share" &&
	message.files?.some((file) => file.id === externalUpload.file_id)
);
assert.equal(externalMessages.length, 1);
assert.equal(externalMessages[0].text, "external upload");
assert.equal(externalMessages[0].files[0].mode, "hosted");
assert.equal(externalMessages[0].files[0].url_private, `/api/files/${externalUpload.file_id}`);

// The upload is single-use: completing it again must not mint a second file.
const repeatedExternal = await client.files.completeUploadExternal({
	files: [{ id: externalUpload.file_id, title: "External qualification" }],
	channel_id: "C1",
});
assert.equal(repeatedExternal.ok, true);
assert.equal(repeatedExternal.files[0].id, externalUpload.file_id);
const repeatedHistory = await client.conversations.history({ channel: "C1", limit: 100 });
assert.equal(repeatedHistory.messages.filter((message) =>
	message.files?.some((file) => file.id === externalUpload.file_id)
).length, 1);

await assert.rejects(
	client.api.test({ error: "synthetic" }),
	(error) => error?.data?.ok === false && error.data.error === "synthetic",
);
const revoked = await client.auth.revoke({ test: true });
assert.equal(revoked.ok, true);
assert.equal(revoked.revoked, false);
const uninstallClient = new WebClient("xoxp-uninstall-node", { slackApiUrl: apiUrl });
const uninstalled = await uninstallClient.apps.uninstall({ client_id: "uninstall-node", client_secret: "uninstall-secret" });
assert.equal(uninstalled.ok, true);

console.log("node-web-api qualification passed");
