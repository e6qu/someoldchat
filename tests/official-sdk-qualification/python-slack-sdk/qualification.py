import io
import json
import os
import time

from slack_sdk import WebClient
from slack_sdk.errors import SlackApiError


client = WebClient(
    token=os.environ.get("SAMEOLDCHAT_API_TOKEN", "xoxb-test"),
    base_url=os.environ.get("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/"),
)

assert client.api_test()["ok"] is True
identity = client.auth_test()
assert identity["team_id"] == "T1"
assert identity["user_id"] == "U1"
created_list = client.api_call(
    "slackLists.create",
    params={
        "name": "SDK qualification list",
        "description_blocks": json.dumps([{"type": "rich_text", "elements": []}]),
        "schema": json.dumps([{"key": "title", "name": "Title", "type": "text", "is_primary_column": True}]),
    },
)
assert created_list["ok"] is True
assert created_list["list"]["id"].startswith("F")
created_list_item = client.api_call(
    "slackLists.items.create",
    params={
        "list_id": created_list["list"]["id"],
        "initial_fields": json.dumps([{"column_id": "title", "value": "first row"}]),
    },
)
assert created_list_item["ok"] is True
assert created_list_item["item"]["id"].startswith("Rec")
listed_items = client.api_call("slackLists.items.list", params={"list_id": created_list["list"]["id"], "limit": 10})
assert listed_items["ok"] is True
assert len(listed_items["items"]) == 1
assert client.api_call(
    "slackLists.items.update",
    params={
        "list_id": created_list["list"]["id"],
        "cells": json.dumps([{"row_id": created_list_item["item"]["id"], "column_id": "title", "value": "updated row"}]),
    },
)["ok"] is True
assert client.api_call(
    "slackLists.access.set",
    params={"list_id": created_list["list"]["id"], "access_level": "read", "channel_ids": json.dumps(["C1"])},
)["ok"] is True
started_list_download = client.api_call("slackLists.download.start", params={"list_id": created_list["list"]["id"], "include_archived": "true"})
assert started_list_download["ok"] is True
list_download = client.api_call(
    "slackLists.download.get",
    params={"list_id": created_list["list"]["id"], "job_id": started_list_download["job_id"]},
)
assert list_download["ok"] is True
assert list_download["status"] == "COMPLETED"
assert "/internal/slack-lists/download.csv" in list_download["download_url"]
assert client.api_call(
    "slackLists.items.delete",
    params={"list_id": created_list["list"]["id"], "id": created_list_item["item"]["id"]},
)["ok"] is True
bot = client.bots_info(bot="B1")
assert bot["ok"] is True
assert bot["bot"]["id"] == "B1"
access_logs = client.team_accessLogs(count=1)
assert access_logs["ok"] is True
assert isinstance(access_logs["logins"], list)
billable_info = client.team_billableInfo(user="U1")
assert billable_info["ok"] is True
assert billable_info["billable_info"]["U1"]["billing_active"] is True
integration_logs = client.team_integrationLogs(count=1)
assert integration_logs["ok"] is True
migration = client.migration_exchange(users=["U1"])
assert migration["ok"] is True
assert migration["user_id_map"]["U1"] == "W1"
reverse_migration = client.migration_exchange(users=["W1"], to_old=True)
assert reverse_migration["ok"] is True
assert reverse_migration["user_id_map"]["W1"] == "U1"
oauth = client.oauth_access(
    client_id="qualification-client",
    client_secret="qualification-secret",
    code="qualification-code",
    redirect_uri="https://example.com/oauth",
)
assert oauth["ok"] is True
assert isinstance(oauth["access_token"], str)
oauth_v2 = client.oauth_v2_access(
    client_id="qualification-client",
    client_secret="qualification-secret",
    code="qualification-v2-code",
    redirect_uri="https://example.com/oauth",
)
assert oauth_v2["ok"] is True
assert oauth_v2["authed_user"]["id"] == "U1"
assert isinstance(oauth_v2["authed_user"]["access_token"], str)
oauth_token = client.api_call(
    "oauth.token",
    params={
        "client_id": "qualification-client",
        "client_secret": "qualification-secret",
        "code": "qualification-token-code",
        "redirect_uri": "https://example.com/oauth",
    },
)
assert oauth_token["ok"] is True
assert isinstance(oauth_token["access_token"], str)
authorizations = client.apps_event_authorizations_list(event_context="qualification-event")
assert authorizations["ok"] is True
assert authorizations["authorizations"][0]["team_id"] == "T1"
admin_users = client.admin_users_list(team_id="T1", limit=10)
assert admin_users["ok"] is True
assert any(user["id"] == "U1" for user in admin_users["users"])
admin_emoji = client.admin_emoji_list()
assert admin_emoji["ok"] is True
admin_teams = client.admin_teams_list(limit=10)
assert admin_teams["ok"] is True
assert any(team["id"] == "T1" for team in admin_teams["teams"])
assert client.admin_emoji_add(name="qualified", url="https://example.com/qualified.png")["ok"] is True
assert client.admin_emoji_addAlias(name="qualified-alias", alias_for="qualified")["ok"] is True
assert client.admin_emoji_rename(name="qualified", new_name="qualified-renamed")["ok"] is True
assert client.admin_emoji_remove(name="qualified-alias")["ok"] is True
assert client.admin_emoji_remove(name="qualified-renamed")["ok"] is True
assert client.admin_conversations_rename(channel_id="C2", name="renamed-lifecycle")["ok"] is True
assert client.admin_conversations_archive(channel_id="C2")["ok"] is True
assert client.admin_conversations_unarchive(channel_id="C2")["ok"] is True
admin_team_admins = client.admin_teams_admins_list(team_id="T1", limit=10)
assert admin_team_admins["ok"] is True
assert "U2" in admin_team_admins["admin_ids"]
admin_team_owners = client.admin_teams_owners_list(team_id="T1", limit=10)
assert admin_team_owners["ok"] is True
assert "U1" in admin_team_owners["owner_ids"]
created_admin_team = client.admin_teams_create(
    team_domain="sdk-created-workspace",
    team_name="SDK Created Workspace",
    team_description="created by SDK qualification",
    team_discoverability="closed",
)
assert created_admin_team["ok"] is True
assert isinstance(created_admin_team["team"], str)
admin_team_settings = client.admin_teams_settings_info(team_id="T1")
assert admin_team_settings["ok"] is True
assert admin_team_settings["team"]["id"] == "T1"
assert admin_team_settings["team"]["name"] == "test"
assert client.admin_users_setAdmin(team_id="T1", user_id="U2")["ok"] is True
assert client.admin_users_setOwner(team_id="T1", user_id="U2")["ok"] is True
assert client.admin_users_setRegular(team_id="T1", user_id="U2")["ok"] is True
assert client.admin_teams_settings_setName(team_id="T1", name="qualified-test")["ok"] is True
assert client.admin_teams_settings_setDescription(team_id="T1", description="qualified description")["ok"] is True
assert client.admin_teams_settings_setDiscoverability(team_id="T1", discoverability="closed")["ok"] is True
assert client.admin_teams_settings_setIcon(team_id="T1", image_url="https://example.com/qualified.png")["ok"] is True
assert client.admin_teams_settings_setDefaultChannels(team_id="T1", channel_ids=["C1"])["ok"] is True
invite_requests = client.admin_inviteRequests_list(team_id="T1", limit=10)
assert invite_requests["ok"] is True
assert isinstance(invite_requests["invite_requests"], list)
approved_invite_requests = client.admin_inviteRequests_approved_list(team_id="T1", limit=10)
assert approved_invite_requests["ok"] is True
assert isinstance(approved_invite_requests["approved_requests"], list)
denied_invite_requests = client.admin_inviteRequests_denied_list(team_id="T1", limit=10)
assert denied_invite_requests["ok"] is True
assert isinstance(denied_invite_requests["denied_requests"], list)
assert client.admin_users_invite(
    team_id="T1", email="sdk-approve@example.com", channel_ids=["C1"], is_restricted=False, is_ultra_restricted=False
)["ok"] is True
assert client.admin_users_invite(
    team_id="T1", email="sdk-deny@example.com", channel_ids=["C1"], is_restricted=False, is_ultra_restricted=False
)["ok"] is True
pending_invite_requests = client.admin_inviteRequests_list(team_id="T1", limit=10)
approval_request = next(request for request in pending_invite_requests["invite_requests"] if request["email"] == "sdk-approve@example.com")
denial_request = next(request for request in pending_invite_requests["invite_requests"] if request["email"] == "sdk-deny@example.com")
assert client.admin_inviteRequests_approve(team_id="T1", invite_request_id=approval_request["id"])["ok"] is True
assert client.admin_inviteRequests_deny(team_id="T1", invite_request_id=denial_request["id"])["ok"] is True
approved_apps = client.admin_apps_approved_list(team_id="T1", limit=10)
assert approved_apps["ok"] is True
assert isinstance(approved_apps["approved_apps"], list)
app_requests = client.admin_apps_requests_list(team_id="T1", limit=10)
assert app_requests["ok"] is True
assert isinstance(app_requests["app_requests"], list)
restricted_apps = client.admin_apps_restricted_list(team_id="T1", limit=10)
assert restricted_apps["ok"] is True
assert isinstance(restricted_apps["restricted_apps"], list)
assert client.api_call("apps.permissions.info")["ok"] is True
assert client.api_call("apps.permissions.scopes.list")["ok"] is True
assert client.api_call("apps.permissions.resources.list", params={"limit": 10})["ok"] is True
assert client.api_call("apps.permissions.users.list", params={"limit": 10})["ok"] is True
assert client.api_call(
    "apps.permissions.request", params={"scopes": "channels:read", "trigger_id": "permission-trigger"}
)["ok"] is True
assert client.api_call(
    "apps.permissions.users.request",
    params={"scopes": "channels:read", "trigger_id": "permission-user-trigger", "user": "U1"},
)["ok"] is True
assert client.admin_apps_approve(app_id="A1", team_id="T1")["ok"] is True
assert client.admin_apps_restrict(app_id="A1", team_id="T1")["ok"] is True
assert client.admin_conversations_invite(channel_id="C2", user_ids="U2")["ok"] is True
searched_conversations = client.admin_conversations_search(query="general", limit=10)
assert searched_conversations["ok"] is True
assert any(conversation["id"] == "C1" for conversation in searched_conversations["conversations"])
assert client.admin_conversations_setConversationPrefs(
    channel_id="C1", prefs={"can_thread": {"type": ["everyone"]}, "who_can_post": {"type": ["everyone"]}}
)["ok"] is True
conversation_prefs = client.admin_conversations_getConversationPrefs(channel_id="C1")
assert conversation_prefs["ok"] is True
assert isinstance(conversation_prefs["prefs"], dict)
conversation_teams = client.admin_conversations_getTeams(channel_id="C1", limit=10)
assert conversation_teams["ok"] is True
assert "T1" in conversation_teams["team_ids"]
completed_step = client.workflows_stepCompleted(
    workflow_step_execute_id="qualification-execute", outputs={"answer": "ok"}
)
assert completed_step["ok"] is True
failed_step = client.workflows_stepFailed(
    workflow_step_execute_id="qualification-failed", error={"message": "qualification failure"}
)
assert failed_step["ok"] is True
updated_step = client.workflows_updateStep(
    workflow_step_edit_id="qualification-edit",
    inputs={"input": {"value": "qualification"}},
    outputs=[{"type": "text", "name": "answer", "label": "Answer"}],
)
assert updated_step["ok"] is True
opened_dialog = client.dialog_open(
    trigger_id="qualification-trigger",
    dialog={
        "callback_id": "qualification-dialog",
        "title": "Qualification",
        "submit_label": "Submit",
        "elements": [{"type": "text", "name": "answer", "label": "Answer"}],
    },
)
assert opened_dialog["ok"] is True
opened_view = client.views_open(
    trigger_id="qualification-trigger",
    view={
        "type": "modal",
        "callback_id": "qualification",
        "title": {"type": "plain_text", "text": "Qualification"},
        "blocks": [],
    },
)
assert opened_view["ok"] is True
assert isinstance(opened_view["view"]["id"], str)
published_view = client.views_publish(user_id="U1", view={"type": "home", "blocks": []})
assert published_view["ok"] is True
pushed_view = client.views_push(
    trigger_id="qualification-trigger",
    view={
        "type": "modal",
        "callback_id": "qualification-pushed",
        "title": {"type": "plain_text", "text": "Pushed qualification"},
        "blocks": [],
    },
)
assert pushed_view["ok"] is True
updated_view = client.views_update(
    view_id=opened_view["view"]["id"],
    view={**opened_view["view"], "callback_id": "qualification-updated"},
)
assert updated_view["ok"] is True
added_call = client.calls_add(
    external_unique_id="qualification-call",
    external_display_id="qualification",
    join_url="https://example.com/call",
    desktop_app_join_url="https://example.com/call-desktop",
    title="Qualification call",
    date_start=int(time.time()),
)
assert added_call["ok"] is True
call_id = added_call["call"]["id"]
call_info = client.calls_info(id=call_id)
assert call_info["ok"] is True
updated_call = client.calls_update(id=call_id, title="Updated qualification call")
assert updated_call["ok"] is True
added_call_participant = client.calls_participants_add(id=call_id, users=[{"slack_id": "U2"}])
assert added_call_participant["ok"] is True
removed_call_participant = client.calls_participants_remove(id=call_id, users=[{"slack_id": "U2"}])
assert removed_call_participant["ok"] is True
ended_call = client.calls_end(id=call_id, duration=30)
assert ended_call["ok"] is True

posted = client.chat_postMessage(channel="C1", text="python sdk qualification")
assert posted["ok"] is True
assert posted["channel"] == "C1"

updated = client.chat_update(channel="C1", ts=posted["ts"], text="python sdk qualification updated")
assert updated["ok"] is True
deleted = client.chat_delete(channel="C1", ts=posted["ts"])
assert deleted["ok"] is True

conversation = client.conversations_info(channel="C1")
assert conversation["ok"] is True
assert conversation["channel"]["id"] == "C1"
members = client.conversations_members(channel="C1", limit=1)
assert members["ok"] is True
assert members["members"] == ["U1"]
conversations = client.conversations_list(limit=1)
assert conversations["ok"] is True
assert len(conversations["channels"]) == 1
joined = client.conversations_join(channel="C2")
assert joined["ok"] is True
assert joined["channel"]["id"] == "C2"
invited = client.conversations_invite(channel="C1", users="U2")
assert invited["ok"] is True
kicked = client.conversations_kick(channel="C1", user="U2")
assert kicked["ok"] is True
left = client.conversations_leave(channel="C2")
assert left["ok"] is True
assert client.admin_conversations_convertToPrivate(channel_id="C2")["ok"] is True
assert client.admin_conversations_delete(channel_id="C2")["ok"] is True
created_admin_conversation = client.admin_conversations_create(name="sdk-admin-created", is_private=True, team_id="T1")
assert created_admin_conversation["ok"] is True
assert isinstance(created_admin_conversation["channel_id"], str)
assert client.admin_conversations_delete(channel_id=created_admin_conversation["channel_id"])["ok"] is True
connected_channel_info = client.admin_conversations_ekm_listOriginalConnectedChannelInfo(channel_ids=["C1"], limit=10)
assert connected_channel_info["ok"] is True
assert isinstance(connected_channel_info["channels"], list)
assert client.admin_conversations_disconnectShared(channel_id="C1", leaving_team_ids=["T1"])["ok"] is True
assert client.admin_conversations_setTeams(channel_id="C1", org_channel=False, target_team_ids=["T1"])["ok"] is True
restricted_conversation = client.admin_conversations_create(name="sdk-restricted-private", is_private=True, team_id="T1")
assert restricted_conversation["ok"] is True
access_group = client.usergroups_create(name="SDK Access Group", handle="sdk-access-group", team_id="T1")
assert access_group["ok"] is True
access_group_id = access_group["usergroup"]["id"]
assert isinstance(access_group_id, str)
assert client.admin_conversations_restrictAccess_addGroup(
    channel_id=restricted_conversation["channel_id"], group_id=access_group_id, team_id="T1"
)["ok"] is True
access_groups = client.admin_conversations_restrictAccess_listGroups(
    channel_id=restricted_conversation["channel_id"], team_id="T1"
)
assert access_groups["ok"] is True
assert access_groups["group_ids"] == [access_group_id]
assert client.admin_conversations_restrictAccess_removeGroup(
    channel_id=restricted_conversation["channel_id"], group_id=access_group_id, team_id="T1"
)["ok"] is True
assert client.admin_conversations_delete(channel_id=restricted_conversation["channel_id"])["ok"] is True
assert client.usergroups_disable(usergroup=access_group_id, team_id="T1")["ok"] is True
uploaded_file = client.files_upload(content="sdk upload", filename="sdk-upload.txt", title="SDK upload")
assert uploaded_file["ok"] is True
assert isinstance(uploaded_file["file"]["id"], str)
files = client.files_list(count=10)
assert files["ok"] is True
assert len(files["files"]) == 2
file_id = uploaded_file["file"]["id"]
qualification_file = next(file for file in files["files"] if file["name"] == "qualification.txt")
deleted_comment = client.api_call("files.comments.delete", params={"file": qualification_file["id"], "id": "FC1"})
assert deleted_comment["ok"] is True
file_info = client.files_info(file=file_id)
assert file_info["ok"] is True
assert file_info["file"]["id"] == file_id
public_file = client.files_sharedPublicURL(file=file_id)
assert public_file["ok"] is True
assert isinstance(public_file["permalink_public"], str)
revoked_public_file = client.files_revokePublicURL(file=file_id)
assert revoked_public_file["ok"] is True
deleted_file = client.files_delete(file=file_id)
assert deleted_file["ok"] is True
remote_file = client.files_remote_add(
    external_id="remote-qualification",
    title="Remote qualification",
    filetype="text",
    external_url="https://example.com/qualification",
)
assert remote_file["ok"] is True
assert remote_file["file"]["external_id"] == "remote-qualification"
remote_info = client.files_remote_info(external_id="remote-qualification")
assert remote_info["ok"] is True
remote_list = client.files_remote_list(limit=1)
assert remote_list["ok"] is True
assert len(remote_list["files"]) == 1
remote_update = client.files_remote_update(external_id="remote-qualification", title="Updated remote qualification")
assert remote_update["ok"] is True
remote_share = client.files_remote_share(external_id="remote-qualification", channels="C1")
assert remote_share["ok"] is True
assert remote_share["file"]["channels"] == ["C1"]
remote_remove = client.files_remote_remove(external_id="remote-qualification")
assert remote_remove["ok"] is True
bookmark = client.bookmarks_add(channel_id="C1", title="SDK bookmark", type="link", link="https://example.com/bookmark", emoji=":link:")
assert bookmark["ok"] is True
assert isinstance(bookmark["bookmark"]["id"], str)
bookmarks = client.bookmarks_list(channel_id="C1")
assert bookmarks["ok"] is True
assert len(bookmarks["bookmarks"]) == 1
edited_bookmark = client.bookmarks_edit(channel_id="C1", bookmark_id=bookmark["bookmark"]["id"], title="Updated SDK bookmark")
assert edited_bookmark["ok"] is True
assert edited_bookmark["bookmark"]["title"] == "Updated SDK bookmark"
removed_bookmark = client.bookmarks_remove(channel_id="C1", bookmark_id=bookmark["bookmark"]["id"])
assert removed_bookmark["ok"] is True
scheduled = client.chat_scheduleMessage(
    channel="C1", text="scheduled qualification", post_at=int(time.time()) + 60
)
assert scheduled["ok"] is True
assert isinstance(scheduled["scheduled_message_id"], str)
scheduled_list = client.chat_scheduledMessages_list(channel="C1", limit=10)
assert scheduled_list["ok"] is True
assert len(scheduled_list["scheduled_messages"]) == 1
deleted_scheduled = client.chat_deleteScheduledMessage(
    channel="C1", scheduled_message_id=scheduled["scheduled_message_id"]
)
assert deleted_scheduled["ok"] is True
dnd_info = client.dnd_info()
assert dnd_info["ok"] is True
assert dnd_info["dnd_enabled"] is False
dnd_snooze = client.dnd_setSnooze(num_minutes=5)
assert dnd_snooze["ok"] is True
assert dnd_snooze["snooze_enabled"] is True
dnd_end_snooze = client.dnd_endSnooze()
assert dnd_end_snooze["ok"] is True
assert dnd_end_snooze["snooze_enabled"] is False
dnd_end = client.dnd_endDnd()
assert dnd_end["ok"] is True
dnd_team = client.dnd_teamInfo(users="U1")
assert dnd_team["ok"] is True
rtm = client.rtm_connect()
assert rtm["ok"] is True
assert isinstance(rtm["url"], str)
assert rtm["team"]["id"] == "T1"
assert rtm["self"]["id"] == "U1"
reminder = client.reminders_add(text="reminder qualification", time=int(time.time()) + 3600)
assert reminder["ok"] is True
assert isinstance(reminder["reminder"]["id"], str)
reminders = client.reminders_list()
assert reminders["ok"] is True
assert len(reminders["reminders"]) == 1
reminder_info = client.reminders_info(reminder=reminder["reminder"]["id"])
assert reminder_info["ok"] is True
assert reminder_info["reminder"]["id"] == reminder["reminder"]["id"]
completed_reminder = client.reminders_complete(reminder=reminder["reminder"]["id"])
assert completed_reminder["ok"] is True
deleted_reminder = client.reminders_delete(reminder=reminder["reminder"]["id"])
assert deleted_reminder["ok"] is True
created_canvas = client.canvases_create(
    title="SDK qualification canvas",
    document_content={"type": "h1", "markdown": "SDK canvas"},
    channel_id="C1",
)
assert created_canvas["ok"] is True
assert isinstance(created_canvas["canvas_id"], str)
edited_canvas = client.canvases_edit(
    canvas_id=created_canvas["canvas_id"],
    changes=[{"operation": "insert_at_end", "document_content": {"type": "paragraph", "markdown": "SDK details"}}],
)
assert edited_canvas["ok"] is True
canvas_sections = client.canvases_sections_lookup(
    canvas_id=created_canvas["canvas_id"], criteria={"contains_text": "SDK details"}
)
assert canvas_sections["ok"] is True
assert len(canvas_sections["sections"]) == 1
assert client.canvases_access_set(canvas_id=created_canvas["canvas_id"], access_level="write", user_ids=["U1"])["ok"] is True
assert client.canvases_access_delete(canvas_id=created_canvas["canvas_id"], user_ids=["U1"])["ok"] is True
assert client.canvases_delete(canvas_id=created_canvas["canvas_id"])["ok"] is True
created_usergroup = client.usergroups_create(
    name="Qualification group", handle="qualification-group", description="SDK qualification"
)
assert created_usergroup["ok"] is True
usergroup_id = created_usergroup["usergroup"]["id"]
assert client.admin_usergroups_addChannels(usergroup_id=usergroup_id, channel_ids=["C1"])["ok"] is True
assert client.admin_usergroups_addTeams(usergroup_id=usergroup_id, team_ids=["T1"])["ok"] is True
admin_usergroup_channels = client.admin_usergroups_listChannels(usergroup_id=usergroup_id, team_id="T1")
assert admin_usergroup_channels["ok"] is True
assert len(admin_usergroup_channels["channels"]) == 1
assert admin_usergroup_channels["channels"][0]["id"] == "C1"
assert client.admin_usergroups_removeChannels(usergroup_id=usergroup_id, channel_ids=["C1"])["ok"] is True
updated_usergroup = client.usergroups_update(usergroup=usergroup_id, name="Updated qualification group")
assert updated_usergroup["ok"] is True
updated_usergroup_users = client.usergroups_users_update(usergroup=usergroup_id, users="U1")
assert updated_usergroup_users["ok"] is True
usergroup_users = client.usergroups_users_list(usergroup=usergroup_id)
assert usergroup_users["ok"] is True
assert usergroup_users["users"] == ["U1"]
usergroups = client.usergroups_list(include_users=True)
assert usergroups["ok"] is True
assert len(usergroups["usergroups"]) == 1
disabled_usergroup = client.usergroups_disable(usergroup=usergroup_id)
assert disabled_usergroup["ok"] is True
enabled_usergroup = client.usergroups_enable(usergroup=usergroup_id)
assert enabled_usergroup["ok"] is True

user = client.users_info(user="U1")
assert user["ok"] is True
assert user["user"]["id"] == "U1"
profile = client.users_profile_get(user="U1")
assert profile["ok"] is True
assert profile["profile"]["display_name"] == "alice"
image = io.BytesIO(b"qualification-photo")
image.name = "qualification.png"
photo = client.users_setPhoto(image=image)
assert photo["ok"] is True
deleted_photo = client.users_deletePhoto()
assert deleted_photo["ok"] is True

root = client.chat_postMessage(channel="C1", text="thread root")
assert root["ok"] is True
unfurled = client.chat_unfurl(
    channel="C1",
    ts=root["ts"],
    unfurls={"https://example.com/qualification": {"text": "unfurled"}},
)
assert unfurled["ok"] is True
reply = client.chat_postMessage(channel="C1", text="thread reply", thread_ts=root["ts"])
assert reply["ok"] is True
replies = client.conversations_replies(channel="C1", ts=root["ts"], limit=2)
assert replies["ok"] is True
assert len(replies["messages"]) == 2

reaction = client.reactions_add(channel="C1", timestamp=root["ts"], name="thumbsup")
assert reaction["ok"] is True
reactions = client.reactions_get(channel="C1", timestamp=root["ts"])
assert reactions["ok"] is True
assert len(reactions["message"]["reactions"]) == 1
pins_added = client.pins_add(channel="C1", timestamp=root["ts"])
assert pins_added["ok"] is True
pins = client.pins_list(channel="C1")
assert pins["ok"] is True
assert len(pins["items"]) == 1
pins_removed = client.pins_remove(channel="C1", timestamp=root["ts"])
assert pins_removed["ok"] is True
reaction_removed = client.reactions_remove(channel="C1", timestamp=root["ts"], name="thumbsup")
assert reaction_removed["ok"] is True

created_conversation = client.conversations_create(name="qualification-tranche")
assert created_conversation["ok"] is True
lifecycle_channel = created_conversation["channel"]["id"]
renamed_conversation = client.conversations_rename(channel=lifecycle_channel, name="qualification-renamed")
assert renamed_conversation["ok"] is True
topic = client.conversations_setTopic(channel=lifecycle_channel, topic="qualification topic")
assert topic["ok"] is True
purpose = client.conversations_setPurpose(channel=lifecycle_channel, purpose="qualification purpose")
assert purpose["ok"] is True
archived = client.conversations_archive(channel=lifecycle_channel)
assert archived["ok"] is True
unarchived = client.conversations_unarchive(channel=lifecycle_channel)
assert unarchived["ok"] is True
lifecycle_info = client.conversations_info(channel=lifecycle_channel)
assert lifecycle_info["ok"] is True
assert lifecycle_info["channel"]["name"] == "qualification-renamed"
assert lifecycle_info["channel"]["topic"]["value"] == "qualification topic"
assert lifecycle_info["channel"]["purpose"]["value"] == "qualification purpose"

me_message = client.chat_meMessage(channel="C1", text="qualification me message")
assert me_message["ok"] is True
ephemeral = client.chat_postEphemeral(channel="C1", user="U1", text="ephemeral qualification")
assert ephemeral["ok"] is True
assert isinstance(ephemeral["message_ts"], str)
starred = client.stars_add(channel="C1", timestamp=root["ts"])
assert starred["ok"] is True
stars = client.stars_list(limit=10)
assert stars["ok"] is True
assert len(stars["items"]) == 1
unstarred = client.stars_remove(channel="C1", timestamp=root["ts"])
assert unstarred["ok"] is True
permalink = client.chat_getPermalink(channel="C1", message_ts=root["ts"])
assert permalink["ok"] is True
assert isinstance(permalink["permalink"], str)
user_reactions = client.reactions_list(limit=1)
assert user_reactions["ok"] is True
team = client.team_info()
assert team["ok"] is True
assert team["team"]["id"] == "T1"
team_profile = client.team_profile_get()
assert team_profile["ok"] is True
assert team_profile["profile"]["fields"] == []
emoji = client.emoji_list()
assert emoji["ok"] is True
identity_result = client.users_identity()
assert identity_result["ok"] is True
assert identity_result["user"]["id"] == "U1"
by_email = client.users_lookupByEmail(email="alice@example.com")
assert by_email["ok"] is True
assert by_email["user"]["id"] == "U1"
presence = client.users_getPresence(user="U1")
assert presence["ok"] is True
set_presence = client.users_setPresence(presence="away")
assert set_presence["ok"] is True
profile_set = client.users_profile_set(profile={"status_text": "qualification", "status_emoji": ":wave:"})
assert profile_set["ok"] is True
assert profile_set["profile"]["status_text"] == "qualification"
user_conversations = client.users_conversations(user="U1", limit=1)
assert user_conversations["ok"] is True
assert len(user_conversations["channels"]) == 1
direct = client.conversations_open(users="U2")
assert direct["ok"] is True
closed = client.conversations_close(channel=direct["channel"]["id"])
assert closed["ok"] is True
marked = client.conversations_mark(channel="C1", ts=root["ts"])
assert marked["ok"] is True

history = client.conversations_history(channel="C1", limit=10)
assert history["ok"] is True
assert len(history["messages"]) == 4
assert history["has_more"] is False
search = client.search_messages(query="thread")
assert search["ok"] is True
assert len(search["messages"]["matches"]) >= 2

users = client.users_list(limit=10)
assert users["ok"] is True
assert len(users["members"]) == 2
assert client.api_call("users.setActive")["ok"] is True
assert client.admin_users_assign(
    team_id="T1", user_id="U2", channel_ids=["C1"], is_restricted=False, is_ultra_restricted=False
)["ok"] is True
assert client.admin_users_setExpiration(
    team_id="T1", user_id="U2", expiration_ts=int(time.time()) + 3600
)["ok"] is True
assert client.api_call(
    "admin.users.session.invalidate", params={"team_id": "T1", "session_id": "qualification-session"}
)["ok"] is True
assert client.api_call("admin.users.session.reset", params={"user_id": "U2"})["ok"] is True
assert client.admin_users_remove(team_id="T1", user_id="U2")["ok"] is True

try:
    client.api_test(error="synthetic")
except SlackApiError as error:
    assert error.response["ok"] is False
    assert error.response["error"] == "synthetic"
else:
    raise AssertionError("api.test error was not raised")

revoked = client.auth_revoke(test=True)
assert revoked["ok"] is True
assert revoked["revoked"] is False
uninstalled = client.apps_uninstall(client_id="client", client_secret="secret")
assert uninstalled["ok"] is True

print("python-slack-sdk qualification passed")
