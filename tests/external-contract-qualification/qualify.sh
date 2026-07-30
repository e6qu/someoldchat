#!/bin/sh

set -eu

work=$(mktemp -d "${TMPDIR:-/tmp}/sameoldchat-external-contracts.XXXXXX")
cleanup() {
	rm -rf "$work"
}
trap cleanup EXIT HUP INT TERM

fetch() {
	curl --fail --silent --show-error --location --compressed \
		--retry 4 --retry-all-errors --connect-timeout 15 --max-time 45 \
		--user-agent 'sameoldchat-contract-qualification/1.0' "$1" |
		sed -e 's/<[^>]*>/ /g' -e 's/&nbsp;/ /g' -e 's/&#160;/ /g' -e "s/&#39;/'/g" -e 's/&amp;/\\&/g' |
		tr '\n\r\t\302\240' '     ' |
		sed -e 's/  */ /g' >"$2"
}

assert_contains() {
	if ! grep -F "$2" "$1" >/dev/null; then
		echo "official Slack source no longer supports contract assertion: $3" >&2
		echo "source: $4" >&2
		exit 1
	fi
	assertions=$((assertions + 1))
}

assertions=0
sign_in_url='https://slack.com/help/articles/212681477-Sign-in-to-Slack'
keyboard_url='https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands'
keyboard_navigation_url='https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard'
dm_url='https://slack.com/help/articles/212281468-Understand-direct-messages'
add_dm_url='https://slack.com/help/articles/1500002969782-Add-people-to-a-direct-message'
convert_dm_url='https://slack.com/help/articles/217555437-Convert-a-group-direct-message-to-a-private-channel'
close_dm_api_url='https://docs.slack.dev/reference/methods/conversations.close/'
message_url='https://slack.com/help/articles/201457107-Send-and-read-messages'
emoji_help_url='https://slack.com/help/articles/202931348-Use-emoji-and-reactions'
emoji_list_url='https://docs.slack.dev/reference/methods/emoji.list/'
message_formatting_url='https://docs.slack.dev/messaging/formatting-message-text/'
search_url='https://slack.com/help/articles/202528808-Search-in-Slack'
search_messages_url='https://docs.slack.dev/reference/methods/search.messages/'
search_files_url='https://docs.slack.dev/reference/methods/search.files/'
search_all_url='https://docs.slack.dev/reference/methods/search.all/'
file_url='https://slack.com/help/articles/201330736-Add-files-to-Slack'
slash_url='https://docs.slack.dev/interactivity/implementing-slash-commands/'
manifest_url='https://docs.slack.dev/reference/app-manifest/'
oauth_url='https://docs.slack.dev/authentication/installing-with-oauth/'
status_url='https://slack.com/help/articles/201864558-Set-your-Slack-status-and-availability'
profile_set_url='https://docs.slack.dev/reference/methods/users.profile.set/'
presence_url='https://docs.slack.dev/reference/methods/users.setPresence/'
notification_url='https://slack.com/help/articles/201355156-Configure-your-Slack-notifications'
conversation_notification_url='https://slack.com/help/articles/360056534254-Manage-notifications-for-specific-channels-and-direct-messages'
dnd_url='https://slack.com/help/articles/214908388-Pause-notifications-with-Do-Not-Disturb'
huddle_url='https://slack.com/help/articles/4402059015315-Use-huddles-in-Slack'
canvas_url='https://slack.com/help/articles/203950418-Use-a-canvas-in-Slack'
list_url='https://slack.com/help/articles/27452748828179-Use-lists-in-Slack'
workflow_url='https://slack.com/help/articles/360035692513-Guide-to-Workflow-Builder'
roles_url='https://slack.com/help/articles/360018112273-Roles-in-Slack'
connect_url='https://slack.com/help/articles/360035092414-What-is-Slack-Connect'
accessibility_url='https://slack.com/help/articles/4455747966739-Accessibility-in-Slack'
screen_reader_url='https://slack.com/help/articles/360000411963-Use-Slack-with-a-screen-reader'
reminder_help_url='https://slack.com/help/articles/208423427-Set-a-reminder'
later_help_url='https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later'
activity_help_url='https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack'
reminder_api_url='https://docs.slack.dev/reference/methods/reminders.add/'
later_api_url='https://docs.slack.dev/changelog/2023-07-its-later-already-for-stars-and-reminders/'
conversation_join_url='https://docs.slack.dev/reference/methods/conversations.join/'
schedule_api_url='https://docs.slack.dev/messaging/sending-and-scheduling-messages/'

fetch "$sign_in_url" "$work/sign-in.html"
fetch "$keyboard_url" "$work/keyboard.html"
fetch "$keyboard_navigation_url" "$work/keyboard-navigation.html"
fetch "$dm_url" "$work/dm.html"
fetch "$add_dm_url" "$work/add-dm.html"
fetch "$convert_dm_url" "$work/convert-dm.html"
fetch "$close_dm_api_url" "$work/conversations-close.html"
fetch "$message_url" "$work/messages.html"
fetch "$emoji_help_url" "$work/emoji-help.html"
fetch "$emoji_list_url" "$work/emoji-list.html"
fetch "$message_formatting_url" "$work/message-formatting.html"
fetch "$search_url" "$work/search.html"
fetch "$search_messages_url" "$work/search-messages.html"
fetch "$search_files_url" "$work/search-files.html"
fetch "$search_all_url" "$work/search-all.html"
fetch "$file_url" "$work/files.html"
fetch "$slash_url" "$work/slash.html"
fetch "$manifest_url" "$work/manifest.html"
fetch "$oauth_url" "$work/oauth.html"
fetch "$status_url" "$work/status.html"
fetch "$profile_set_url" "$work/users-profile-set.html"
fetch "$presence_url" "$work/user-presence.html"
fetch "$notification_url" "$work/notifications.html"
fetch "$conversation_notification_url" "$work/conversation-notifications.html"
fetch "$dnd_url" "$work/dnd.html"
fetch "$huddle_url" "$work/huddles.html"
fetch "$canvas_url" "$work/canvas.html"
fetch "$list_url" "$work/list.html"
fetch "$workflow_url" "$work/workflow.html"
fetch "$roles_url" "$work/roles.html"
fetch "$connect_url" "$work/connect.html"
fetch "$accessibility_url" "$work/accessibility.html"
fetch "$screen_reader_url" "$work/screen-reader.html"
fetch "$reminder_help_url" "$work/reminders.html"
fetch "$later_help_url" "$work/later.html"
fetch "$activity_help_url" "$work/activity.html"
fetch "$reminder_api_url" "$work/reminders-add.html"
fetch "$later_api_url" "$work/later-api.html"
fetch "$conversation_join_url" "$work/conversations-join.html"
fetch "$schedule_api_url" "$work/scheduling-api.html"

assert_contains "$work/sign-in.html" 'Sign in to your workspace' \
	'[AUTH-01] workspace sign-in is an explicit first-party journey' "$sign_in_url"
assert_contains "$work/keyboard.html" 'Start a search' \
	'[SEARCH-01] global search has an official keyboard shortcut' "$keyboard_url"
assert_contains "$work/keyboard.html" 'Search in the current conversation' \
	'[SEARCH-03] current-conversation search has a distinct shortcut' "$keyboard_url"
assert_contains "$work/keyboard-navigation.html" 'Type the name of a channel or person into the search field' \
	'[NAV-03] the conversation switcher searches both channels and people' "$keyboard_navigation_url"
assert_contains "$work/dm.html" 'up to nine people' \
	'[DM-02] group DM participant limit' "$dm_url"
assert_contains "$work/dm.html" 'give group DMs a name' \
	'[DM-02] group DMs have a naming journey' "$dm_url"
assert_contains "$work/dm.html" 'see a list of your direct messages' \
	'[DM-01] DMs have a dedicated first-party surface' "$dm_url"
assert_contains "$work/dm.html" 'search for a specific conversation' \
	'[DM-01 DM-04] direct conversations remain searchable' "$dm_url"
assert_contains "$work/add-dm.html" 'conversation history you choose to include is moved to a new group DM' \
	'[DM-03] adding DM participants creates a new conversation with selected history' "$add_dm_url"
assert_contains "$work/add-dm.html" 'Members of the DM will be notified automatically with a message posted to the relevant conversations' \
	'[DM-03] adding DM participants posts notices to both relevant conversations' "$add_dm_url"
assert_contains "$work/add-dm.html" 'select them from the list and click Next' \
	'[DM-03] desktop participant selection precedes the history step' "$add_dm_url"
assert_contains "$work/add-dm.html" 'Select an option to Include conversation history' \
	'[DM-03] desktop history selection is a distinct step' "$add_dm_url"
assert_contains "$work/add-dm.html" 'Click Done and select Confirm' \
	'[DM-03] desktop history selection is reviewed before confirmation' "$add_dm_url"
assert_contains "$work/convert-dm.html" 'messages and files from the DM will be visible to any new members' \
	'[DM-05] converting a group DM preserves its content for future members' "$convert_dm_url"
assert_contains "$work/convert-dm.html" 'Members of the group DM will be notified about the move in a message posted to the new channel' \
	'[DM-05] converting a group DM posts the move notice in the resulting channel' "$convert_dm_url"
assert_contains "$work/convert-dm.html" 'By default, all members and Multi-Channel Guests' \
	'[DM-05] full members and multi-channel guests may convert by default' "$convert_dm_url"
assert_contains "$work/convert-dm.html" 'Select the Settings tab' \
	'[DM-05] conversion begins in group DM settings' "$convert_dm_url"
assert_contains "$work/convert-dm.html" 'Click Change to a private channel' \
	'[DM-05] conversion uses Slack current action label' "$convert_dm_url"
assert_contains "$work/convert-dm.html" 'Enter a name for the new channel' \
	'[DM-05] conversion requires a channel name' "$convert_dm_url"
assert_contains "$work/convert-dm.html" 'Click Change to Private' \
	'[DM-05] conversion uses Slack current confirmation label' "$convert_dm_url"
assert_contains "$work/conversations-close.html" 'channel was already closed the response will include' \
	'[DM-04] a repeated conversations.close is an explicit no-op' "$close_dm_api_url"
assert_contains "$work/conversations-close.html" 'already_closed properties' \
	'[DM-04] a repeated conversations.close reports already_closed' "$close_dm_api_url"
assert_contains "$work/messages.html" 'automatically save as a draft' \
	'[DRAFT-01] unfinished composer text is saved as a draft' "$message_url"
assert_contains "$work/messages.html" 'Manage draft, scheduled, and sent messages' \
	'[DRAFT-02] Drafts and sent is the current aggregate work surface' "$message_url"
assert_contains "$work/messages.html" 'edit, reschedule, send, cancel, or delete it' \
	'[SCHED-02] scheduled items expose the current management actions' "$message_url"
assert_contains "$work/emoji-help.html" 'using a : colon followed by the code or emoji alias' \
	'[COMP-03] colon codes are a documented Slack composer entry point' "$emoji_help_url"
assert_contains "$work/emoji-help.html" 'browse categories' \
	'[COMP-03 ACT-02] the emoji picker supports category browsing' "$emoji_help_url"
assert_contains "$work/emoji-help.html" 'Click the Add reaction icon and select an option.' \
	'[ACT-02] Slack exposes reactions from a message action' "$emoji_help_url"
assert_contains "$work/emoji-list.html" 'include_categories' \
	'[COMP-03 ACT-02] emoji.list accepts the current category projection argument' "$emoji_list_url"
assert_contains "$work/emoji-list.html" 'Include a list of categories for Unicode emoji' \
	'[COMP-03 ACT-02] emoji.list category projection is Unicode emoji metadata' "$emoji_list_url"
assert_contains "$work/message-formatting.html" 'The list of supported emoji are taken from' \
	'[COMP-03 ACT-02] Slack names the upstream standard emoji dataset' "$message_formatting_url"
assert_contains "$work/scheduling-api.html" 'delete the old message and then' \
	'[SCHED-02] the public Web API updates by delete plus schedule rather than an invented update method' "$schedule_api_url"
assert_contains "$work/scheduling-api.html" 'Messages can only be scheduled up to 120 days in advance' \
	'[SCHED-01] the public scheduling window is 120 days' "$schedule_api_url"
assert_contains "$work/search.html" 'switch between result types' \
	'[SEARCH-01] desktop search result types' "$search_url"
assert_contains "$work/search.html" 'select a recent search if you' \
	'[SEARCH-01] search exposes recent-query suggestions' "$search_url"
assert_contains "$work/search.html" 'in:#team-marketing from:@Sara' \
	'[SEARCH-02] search modifiers can be combined' "$search_url"
assert_contains "$work/search.html" 'using hasmy::eyes:' \
	'[SEARCH-02] search distinguishes reactions added by the current member' "$search_url"
assert_contains "$work/search.html" 'partial word with at least three characters' \
	'[SEARCH-02] search supports documented prefix matching' "$search_url"
assert_contains "$work/search.html" 'Messages , Files , People , Channels , or Canvases' \
	'[SEARCH-01 SEARCH-02] search switches among current result types' "$search_url"
assert_contains "$work/search.html" 'typing ⌘ F on a Mac' \
	'[SEARCH-03] macOS current-conversation search shortcut' "$search_url"
assert_contains "$work/search.html" 'Ctrl F on Windows or Linux' \
	'[SEARCH-03] Windows and Linux current-conversation search shortcut' "$search_url"
assert_contains "$work/search-messages.html" 'User token:' \
	'[SEARCH-01] search.messages is a user-token method' "$search_messages_url"
assert_contains "$work/search-messages.html" 'Maximum of 100' \
	'[SEARCH-02] search.messages page size is bounded at 100' "$search_messages_url"
assert_contains "$work/search-files.html" 'max count value is 100' \
	'[SEARCH-02 FILE-04] search.files count and page bounds' "$search_files_url"
assert_contains "$work/search-all.html" 'search both messages and files in a single call' \
	'[SEARCH-01 FILE-04] search.all combines the two legacy result collections' "$search_all_url"
assert_contains "$work/files.html" 'files up to 1GB in size' \
	'[FILE-03] current hosted file size limit' "$file_url"
assert_contains "$work/files.html" 'Drag and drop up to 10 files' \
	'[FILE-01] current composer file count and drag entry point' "$file_url"
assert_contains "$work/slash.html" '3000 milliseconds' \
	'[APP-05] slash commands require a three-second acknowledgement' "$slash_url"
assert_contains "$work/manifest.html" 'features.slash_commands[].should_escape' \
	'[APP-05] slash command entity escaping is manifest-controlled' "$manifest_url"
assert_contains "$work/oauth.html" 'requesting scopes, waiting for a user to give their approval, and exchanging a temporary authorization code' \
	'[APP-02] Slack OAuth installation sequence' "$oauth_url"
assert_contains "$work/oauth.html" 'token rotation' \
	'[APP-02] Slack OAuth supports expiring access and refresh token rotation' "$oauth_url"
assert_contains "$work/conversations-join.html" 'channels:join' \
	'[APP-02 CONV-01] installed bot tokens join public channels with channels:join' "$conversation_join_url"
assert_contains "$work/status.html" 'Remove status after...' \
	'[STATUS-01] status supports an explicit clear time' "$status_url"
assert_contains "$work/status.html" 'away after 10 minutes of desktop inactivity' \
	'[STATUS-02] automatic availability transition' "$status_url"
assert_contains "$work/status.html" 'schedule up to five statuses at a time' \
	'[STATUS-03] future statuses have a five-item first-party limit' "$status_url"
assert_contains "$work/status.html" 'Choose a start and end time' \
	'[STATUS-03] scheduled status duration has explicit start and end instants' "$status_url"
assert_contains "$work/status.html" 'view, edit, or cancel a status before it starts' \
	'[STATUS-03] scheduled statuses remain manageable before their start' "$status_url"
assert_contains "$work/users-profile-set.html" 'status_expiration' \
	'[STATUS-01] users.profile.set carries the Unix status expiration' "$profile_set_url"
assert_contains "$work/user-presence.html" 'Either auto or away' \
	'[STATUS-02] users.setPresence stores manual auto or away rather than active' "$presence_url"
assert_contains "$work/notifications.html" 'Everything or Mentions and direct messages' \
	'[NOTIFY-01] workspace notification trigger choices' "$notification_url"
assert_contains "$work/notifications.html" 'only exact matches will trigger notifications' \
	'[NOTIFY-01] channel keywords use exact case-insensitive matching' "$notification_url"
assert_contains "$work/notifications.html" "Keywords in messages sent in threads won't trigger a notification" \
	'[NOTIFY-01] channel keywords do not trigger from thread replies' "$notification_url"
assert_contains "$work/notifications.html" 'Channels with notifications set to "All new posts"' \
	'[NOTIFY-01 ACTIVITY-01] all-post channels can be included in Activity' "$notification_url"
assert_contains "$work/conversation-notifications.html" 'All new posts' \
	'[NOTIFY-02] channels and group DMs expose all-post conversation overrides' "$conversation_notification_url"
assert_contains "$work/conversation-notifications.html" 'Just mentions' \
	'[NOTIFY-02] channels and group DMs expose mention-only conversation overrides' "$conversation_notification_url"
assert_contains "$work/conversation-notifications.html" 'Follow every thread' \
	'[NOTIFY-02 ACTIVITY-01] conversation settings support following every thread' "$conversation_notification_url"
assert_contains "$work/conversation-notifications.html" 'Exceptions to the defaults' \
	'[NOTIFY-02] preference UI lists conversation exceptions' "$conversation_notification_url"
assert_contains "$work/dnd.html" 'set a notification schedule' \
	'[NOTIFY-03] notification pause schedules are first-party behavior' "$dnd_url"
assert_contains "$work/dnd.html" 'override this setting to notify you about an urgent message once per day' \
	'[NOTIFY-03] urgent DM notification override is bounded' "$dnd_url"
assert_contains "$work/huddles.html" 'maximum of two participants' \
	'[HUDDLE-03] free-plan huddle participant limit' "$huddle_url"
assert_contains "$work/huddles.html" 'up to 50 participants' \
	'[HUDDLE-03] paid-plan huddle participant limit' "$huddle_url"
assert_contains "$work/huddles.html" 'Google Chrome' \
	'[HUDDLE-01] huddle browser support is explicitly bounded' "$huddle_url"
assert_contains "$work/canvas.html" 'Add a canvas as a tab' \
	'[CANVAS-01] conversations add an existing or newly created canvas as a tab' "$canvas_url"
assert_contains "$work/list.html" 'create a list' \
	'[LIST-01] lists have a current first-party creation journey' "$list_url"
assert_contains "$work/workflow.html" 'Workflow Builder' \
	'[WORKFLOW-03] workflow creation is a current first-party surface' "$workflow_url"
assert_contains "$work/roles.html" 'Workspace Primary Owner' \
	'[ADMIN-01] workspace roles have distinct administrative authority' "$roles_url"
assert_contains "$work/connect.html" 'work alongside people from other companies' \
	'[CONNECT-01] Slack Connect is an external-organization journey' "$connect_url"
assert_contains "$work/connect.html" 'up to 250 organizations, including your own' \
	'[CONNECT-01] current Slack Connect channel organization capacity' "$connect_url"
assert_contains "$work/accessibility.html" 'keyboard' \
	'[A11Y-01] Slack documents keyboard accessibility as a product contract' "$accessibility_url"
assert_contains "$work/screen-reader.html" 'screen reader' \
	'[A11Y-02] Slack documents a dedicated screen-reader journey' "$screen_reader_url"

assert_contains "$work/reminders.html" 'Later tab in your sidebar.' \
	'[REMIND-02] personal reminder creation starts in Later' "$reminder_help_url"
assert_contains "$work/reminders.html" 'Remind me about this' \
	'[REMIND-01] message and file reminders use the message action' "$reminder_help_url"
assert_contains "$work/reminders.html" '/remind [#channel] [what] [when]' \
	'[REMIND-03] channel reminder slash-command grammar' "$reminder_help_url"
assert_contains "$work/reminders.html" "Channel reminders can’t be edited" \
	'[REMIND-03] channel reminders are delete-and-recreate' "$reminder_help_url"
assert_contains "$work/reminders.html" 'A message that is only visible to you will appear' \
	'[REMIND-03] /remind list is private to the caller' "$reminder_help_url"
assert_contains "$work/reminders.html" '9 a.m. in your time zone' \
	'[REMIND-02] date-only reminder default is local 9 AM' "$reminder_help_url"
assert_contains "$work/reminders.html" 'guests can only set reminders for themselves' \
	'[REMIND-02] guest reminder boundary' "$reminder_help_url"
assert_contains "$work/reminders.html" 'see a badge on the' \
	'[REMIND-04] due personal reminders badge Later and Activity' "$reminder_help_url"
assert_contains "$work/reminders.html" 'every Monday' \
	'[REMIND-03] channel reminders accept named weekday recurrence' "$reminder_help_url"
assert_contains "$work/activity.html" 'personal reminders in Activity' \
	'[ACTIVITY-01 REMIND-04] current Activity includes personal reminders' "$activity_help_url"
assert_contains "$work/activity.html" 'mark them all as read' \
	'[ACTIVITY-03] current Activity supports bulk read acknowledgement' "$activity_help_url"
assert_contains "$work/activity.html" 'Detailed layout with full previews of messages' \
	'[ACTIVITY-01] detailed and dense layouts are one feed presentation' "$activity_help_url"
assert_contains "$work/activity.html" 'Clearing notifications hides them from Activity and automatically marks any unread notifications as read' \
	'[ACTIVITY-03] clear hides and marks read atomically' "$activity_help_url"
assert_contains "$work/activity.html" 'there’ll always be a record in the Cleared notifications filter' \
	'[ACTIVITY-03] cleared notifications remain recoverable' "$activity_help_url"
assert_contains "$work/activity.html" 'DMs Mentions Threads Channels Reactions Invitations Apps Reminders VIP' \
	'[ACTIVITY-02] current Activity notification-type filters' "$activity_help_url"
assert_contains "$work/keyboard-navigation.html" 'Enter to reply to a message' \
	'[ACTIVITY-03] Activity Enter replies rather than merely opening an item' "$keyboard_navigation_url"
assert_contains "$work/keyboard-navigation.html" 'X to select or un-select an item' \
	'[ACTIVITY-03] Activity keyboard selection participates in bulk triage' "$keyboard_navigation_url"
assert_contains "$work/keyboard-navigation.html" 'Ctrl 3' \
	'[ACTIVITY-01] Slack web on macOS opens Activity with the third navigation-tab shortcut' "$keyboard_navigation_url"
assert_contains "$work/keyboard-navigation.html" 'Ctrl Shift 3' \
	'[ACTIVITY-01] Slack web on Windows and Linux opens Activity with the third navigation-tab shortcut' "$keyboard_navigation_url"

for section in 'In progress' 'Archived' 'Completed' 'Show upcoming reminders'; do
	assert_contains "$work/later.html" "$section" "[LATER-02] Later exposes $section" "$later_help_url"
done

assert_contains "$work/reminders-add.html" 'natural language description (Ex. "in 15 minutes," or "every Thursday")' \
	'[REMIND-API-01] reminders.add natural-language argument' "$reminder_api_url"
assert_contains "$work/reminders-add.html" 'No longer supported - reminders cannot be set for other users.' \
	'[REMIND-API-01] reminders.add user-token targeting retirement' "$reminder_api_url"
assert_contains "$work/reminders-add.html" 'Available options: daily , weekly , monthly , or yearly .' \
	'[REMIND-API-01] reminders.add recurrence object' "$reminder_api_url"
assert_contains "$work/reminders-add.html" 'have become degraded or useless' \
	'[REMIND-API-01] reminders API retirement state' "$reminder_api_url"
assert_contains "$work/later-api.html" 'There are no direct APIs for Save it for Later to integrate with.' \
	'[LATER-01 REMIND-API-01] current Later has no direct app API' "$later_api_url"

echo "external Slack journey contract qualification passed ($assertions assertions)"
