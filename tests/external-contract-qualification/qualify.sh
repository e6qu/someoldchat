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
		sed -e 's/<[^>]*>/ /g' -e 's/&nbsp;/ /g' -e "s/&#39;/'/g" -e 's/&amp;/\\&/g' |
		tr '\n\r\t' '   ' |
		sed -e 's/  */ /g' >"$2"
}

assert_contains() {
	if ! grep -F "$2" "$1" >/dev/null; then
		echo "official Slack source no longer supports contract assertion: $3" >&2
		echo "source: $4" >&2
		exit 1
	fi
}

reminder_help_url='https://slack.com/help/articles/208423427-Set-a-reminder'
later_help_url='https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later'
reminder_api_url='https://docs.slack.dev/reference/methods/reminders.add/'
later_api_url='https://docs.slack.dev/changelog/2023-07-its-later-already-for-stars-and-reminders/'

fetch "$reminder_help_url" "$work/reminders.html"
fetch "$later_help_url" "$work/later.html"
fetch "$reminder_api_url" "$work/reminders-add.html"
fetch "$later_api_url" "$work/later-api.html"

assert_contains "$work/reminders.html" 'Later tab in your sidebar.' \
	'personal reminder creation starts in Later' "$reminder_help_url"
assert_contains "$work/reminders.html" 'Remind me about this' \
	'message and file reminders use the message action' "$reminder_help_url"
assert_contains "$work/reminders.html" '/remind [#channel] [what] [when]' \
	'channel reminder slash-command grammar' "$reminder_help_url"
assert_contains "$work/reminders.html" 'Channel reminders can’t be edited' \
	'channel reminders are delete-and-recreate' "$reminder_help_url"
assert_contains "$work/reminders.html" 'A message that is only visible to you will appear' \
	'/remind list is private to the caller' "$reminder_help_url"
assert_contains "$work/reminders.html" '9 a.m. in your time zone' \
	'date-only reminder default is local 9 AM' "$reminder_help_url"
assert_contains "$work/reminders.html" 'guests can only set reminders for themselves' \
	'guest reminder boundary' "$reminder_help_url"

for section in 'In progress' 'Archived' 'Completed' 'Show upcoming reminders'; do
	assert_contains "$work/later.html" "$section" "Later exposes $section" "$later_help_url"
done

assert_contains "$work/reminders-add.html" 'natural language description (Ex. "in 15 minutes," or "every Thursday")' \
	'reminders.add natural-language argument' "$reminder_api_url"
assert_contains "$work/reminders-add.html" 'No longer supported - reminders cannot be set for other users.' \
	'reminders.add user-token targeting retirement' "$reminder_api_url"
assert_contains "$work/reminders-add.html" 'Available options: daily , weekly , monthly , or yearly .' \
	'reminders.add recurrence object' "$reminder_api_url"
assert_contains "$work/reminders-add.html" 'have become degraded or useless' \
	'reminders API retirement state' "$reminder_api_url"
assert_contains "$work/later-api.html" 'There are no direct APIs for Save it for Later to integrate with.' \
	'current Later has no direct app API' "$later_api_url"

echo 'external Slack reminder and Later contract qualification passed'
