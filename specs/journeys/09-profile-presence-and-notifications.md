# Profile, presence, and notifications

## PROFILE-01 — Inspect a member profile

Opening a member from a message, mention, People, DM, channel member list, or
search displays the same authorized identity: display/full name, pronouns,
title, local time, status, availability, image, contact fields, role/guest/bot/
external state, and profile fields configured by the workspace. Private fields
are not emitted in HTML, API, or app payloads merely because CSS hides them.

The profile offers Slack-applicable actions such as message, call/huddle,
copy member ID, and administrative controls.

## PROFILE-02 — Edit own profile and image

The member can change Slack-editable fields with inline limits and preview.
Save commits one profile update, refreshes every visible projection, and emits
the correct user-change event. Cancel and validation preserve the prior durable
profile. Image upload supports preview/crop where Slack does, exact file
validation, replacement, cache invalidation, and removal without exposing an
uncommitted image.

## STATUS-01 — Set and clear a status

The status picker supports emoji, text, Slack presets, and a clear time in the
member's time zone. The resulting status appears consistently in the profile,
messages, mentions, People, and API. Automatic expiry clears once despite
restart, hibernation, or time-zone/DST boundaries. Manual clear cancels expiry.
An over-limit, invalid custom emoji, or past expiry is handled in place.

## STATUS-02 — Represent availability and presence

Active/away and manual/automatic presence follow Slack's current semantics and
do not imply online activity the system cannot observe. Presence changes are
workspace/member isolated, rate-aware, and projected consistently to Web API,
events, and the UI. Deactivated users and bots have truthful states.

## NOTIFY-01 — Configure workspace notifications

The member can choose Slack's current notification trigger level, thread reply
behavior, keyword alerts, sound/appearance, mobile timing, email settings, and
other available preferences. The UI explains plan/platform limitations.
Changes persist per member/workspace and affect subsequent Activity, badges,
push/email/browser delivery, and notification events—not message visibility.

## NOTIFY-02 — Configure a conversation

Channel/DM details expose conversation-specific notification overrides,
including mute and Slack's available mention/every-message choices. The
effective setting and inherited workspace default are distinguishable.
Muting changes notification/unread emphasis according to Slack but does not
leave, hide, or deny the conversation.

## NOTIFY-03 — Snooze with Do Not Disturb

The member selects a preset/custom DND duration or notification schedule. The
UI shows the exact end time and active state, supports early resume, and
projects through Slack's DND API. Urgent sender overrides, where Slack offers
them, identify consequence and authorization. DND suppresses delivery—not
durable messages or later Activity—and expires once.

## NOTIFY-04 — Receive and reconcile notifications

Eligible events produce the correct Activity, badge, browser/push/email, and
sound behavior under the effective settings. The same event is not duplicated
across reconnect/replay. Clicking a notification opens the authorized object
and updates read state according to Slack. Permission loss redacts future
delivery and prevents stale notification content from reopening protected
data.

## Evidence

- Browser tests cover profile sources/edit/photo, status expiry, presence,
  workspace/conversation preferences, mute, DND/resume, and notification deep
  links.
- Deterministic-clock and persistence tests cover status/DND expiry through
  restart and hibernation.
- Official SDK tests exercise `users.profile.*`, `users.setPresence`, and
  `dnd.*` with user tokens and permission/error variants.
- Differential tests compare effective preference inheritance and event-to-
  notification routing in a dedicated Slack workspace.

Sources checked 2026-07-29:

- [Set your Slack status and availability](https://slack.com/help/articles/201864558-Set-your-Slack-status-and-availability)
- [Configure your Slack notifications](https://slack.com/help/articles/201355156-Configure-your-Slack-notifications)
- [Guide to Slack notifications](https://slack.com/help/articles/360025446073-Guide-to-Slack-notifications)
- [Pause notifications with Do Not Disturb](https://slack.com/help/articles/214908388-Pause-notifications-with-Do-Not-Disturb)
