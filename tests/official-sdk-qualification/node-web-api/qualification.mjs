import assert from "node:assert/strict";
import { Readable } from "node:stream";
import { WebClient } from "@slack/web-api";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const token = process.env.SAMEOLDCHAT_API_TOKEN ?? "xoxb-test";
const client = new WebClient(token, { slackApiUrl: apiUrl });
const appClient = new WebClient(process.env.SAMEOLDCHAT_APP_TOKEN ?? "xapp-test", { slackApiUrl: apiUrl });

const socketMode = await appClient.apiCall("apps.connections.open");
assert.equal(socketMode.ok, true);
assert.equal(socketMode.url.startsWith("ws://127.0.0.1:18080/socket-mode?connection_id="), true);

const success = await client.api.test();
assert.equal(success.ok, true);

const identity = await client.auth.test();
assert.equal(identity.ok, true);
assert.equal(identity.team_id, "T1");
assert.equal(identity.user_id, "U1");
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
assert.equal(typeof oauthV2.authed_user.access_token, "string");
const oauthToken = await client.apiCall("oauth.token", {
	client_id: "qualification-client",
	client_secret: "qualification-secret",
	code: "qualification-token-code",
	redirect_uri: "https://example.com/oauth",
});
assert.equal(oauthToken.ok, true);
assert.equal(typeof oauthToken.access_token, "string");
const authorizations = await client.apps.event.authorizations.list({ event_context: "qualification-event" });
assert.equal(authorizations.ok, true);
assert.equal(authorizations.authorizations[0].team_id, "T1");
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
const completedStep = await client.workflows.stepCompleted({
  workflow_step_execute_id: "qualification-execute",
  outputs: { answer: "ok" },
});
assert.equal(completedStep.ok, true);
const failedStep = await client.workflows.stepFailed({
  workflow_step_execute_id: "qualification-failed",
  error: { message: "qualification failure" },
});
assert.equal(failedStep.ok, true);
const updatedStep = await client.workflows.updateStep({
  workflow_step_edit_id: "qualification-edit",
  inputs: { input: { value: "qualification" } },
  outputs: [{ type: "text", name: "answer", label: "Answer" }],
});
assert.equal(updatedStep.ok, true);
const openedDialog = await client.dialog.open({
  trigger_id: "qualification-trigger",
  dialog: {
    callback_id: "qualification-dialog",
    title: "Qualification",
    submit_label: "Submit",
    elements: [{ type: "text", name: "answer", label: "Answer" }],
  },
});
assert.equal(openedDialog.ok, true);
const openedView = await client.views.open({
  trigger_id: "qualification-trigger",
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
  trigger_id: "qualification-trigger",
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

const conversation = await client.conversations.info({ channel: "C1" });
assert.equal(conversation.ok, true);
assert.equal(conversation.channel.id, "C1");
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
const kicked = await client.conversations.kick({ channel: "C1", user: "U2" });
assert.equal(kicked.ok, true);
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
const uploadedFile = await client.files.upload({
	filename: "sdk-upload.txt",
	content: "sdk upload",
	title: "SDK upload",
});
assert.equal(uploadedFile.ok, true);
assert.equal(typeof uploadedFile.file.id, "string");
const files = await client.files.list({ count: 10 });
assert.equal(files.ok, true);
assert.equal(files.files.length, 2);
const fileId = uploadedFile.file.id;
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
const scheduled = await client.chat.scheduleMessage({
	channel: "C1",
	text: "scheduled qualification",
	post_at: Math.floor(Date.now() / 1000) + 60,
});
assert.equal(scheduled.ok, true);
assert.equal(typeof scheduled.scheduled_message_id, "string");
const scheduledList = await client.chat.scheduledMessages.list({ channel: "C1", limit: 10 });
assert.equal(scheduledList.ok, true);
assert.equal(scheduledList.scheduled_messages.length, 1);
const deletedScheduled = await client.chat.deleteScheduledMessage({
	channel: "C1",
	scheduled_message_id: scheduled.scheduled_message_id,
});
assert.equal(deletedScheduled.ok, true);
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
const reminder = await client.reminders.add({
	text: "reminder qualification",
	time: Math.floor(Date.now() / 1000) + 3600,
});
assert.equal(reminder.ok, true);
assert.equal(typeof reminder.reminder.id, "string");
const reminders = await client.reminders.list();
assert.equal(reminders.ok, true);
assert.equal(reminders.reminders.length, 1);
const reminderInfo = await client.reminders.info({ reminder: reminder.reminder.id });
assert.equal(reminderInfo.ok, true);
assert.equal(reminderInfo.reminder.id, reminder.reminder.id);
const completedReminder = await client.reminders.complete({ reminder: reminder.reminder.id });
assert.equal(completedReminder.ok, true);
const deletedReminder = await client.reminders.delete({ reminder: reminder.reminder.id });
assert.equal(deletedReminder.ok, true);
const createdUsergroup = await client.usergroups.create({
	name: "Qualification group",
	handle: "qualification-group",
	description: "SDK qualification",
});
assert.equal(createdUsergroup.ok, true);
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
const image = Readable.from(Buffer.from("qualification-photo"));
image.path = "qualification.png";
const photo = await client.users.setPhoto({ image });
assert.equal(photo.ok, true);
const deletedPhoto = await client.users.deletePhoto();
assert.equal(deletedPhoto.ok, true);

const root = await client.chat.postMessage({ channel: "C1", text: "thread root" });
assert.equal(root.ok, true);
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
const emoji = await client.emoji.list();
assert.equal(emoji.ok, true);
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
const profileSet = await client.users.profile.set({ profile: { status_text: "qualification", status_emoji: ":wave:" } });
assert.equal(profileSet.ok, true);
assert.equal(profileSet.profile.status_text, "qualification");
const userConversations = await client.users.conversations({ user: "U1", limit: 1 });
assert.equal(userConversations.ok, true);
assert.equal(userConversations.channels.length, 1);
const direct = await client.conversations.open({ users: "U2" });
assert.equal(direct.ok, true);
const closed = await client.conversations.close({ channel: direct.channel.id });
assert.equal(closed.ok, true);
const marked = await client.conversations.mark({ channel: "C1", ts: root.ts });
assert.equal(marked.ok, true);

const history = await client.conversations.history({ channel: "C1", limit: 10 });
assert.equal(history.ok, true);
assert.equal(history.messages.length, 4);
assert.equal(history.has_more, false);
const search = await client.search.messages({ query: "thread" });
assert.equal(search.ok, true);
assert.equal(search.messages.matches.length >= 2, true);

const users = await client.users.list({ limit: 10 });
assert.equal(users.ok, true);
assert.equal(users.members.length, 2);
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

await assert.rejects(
	client.api.test({ error: "synthetic" }),
	(error) => error?.data?.ok === false && error.data.error === "synthetic",
);
const revoked = await client.auth.revoke({ test: true });
assert.equal(revoked.ok, true);
assert.equal(revoked.revoked, false);
const uninstalled = await client.apps.uninstall({ client_id: "client" });
assert.equal(uninstalled.ok, true);

console.log("node-web-api qualification passed");
