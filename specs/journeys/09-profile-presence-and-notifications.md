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

1. From the profile control, the member opens **Update your status**. The
   dialog/form exposes status text, an emoji, workspace status suggestions, and
   **Remove status after**. Choosing a suggestion fills editable text and emoji;
   it does not save until the member activates **Save**.
2. Status text is plain text of at most 100 characters. Emoji is a workspace
   emoji name. When text is non-empty and emoji is omitted, clients present
   Slack's default speech-balloon status emoji rather than inventing an invalid
   name. The clear time is interpreted in the member's local time and serialized
   as the `status_expiration` Unix timestamp.
3. Save commits text, emoji, and expiry as one profile mutation. The resulting
   status appears beside the member's name wherever Slack exposes identity; the
   full text and localized clear time are available in the profile. Refresh,
   reconnect, process restart, and a switch between local/distributed storage
   preserve the same value.
4. At the deadline, one worker clears text, emoji, and expiry atomically and
   emits one profile-change event. Competing workers, a repeated poll, restart,
   hibernation, or daylight-saving transition cannot clear a replacement status
   or emit the expiry twice. The lifecycle wake deadline includes this expiry.
5. **Clear status** explicitly submits empty text and emoji and cancels the
   pending expiry. Editing the status replaces the old deadline. Cancel or an
   inline validation failure preserves the prior durable profile.
6. Over-limit text, malformed/non-workspace emoji, a negative or past API
   expiration, or an invalid local date is handled in place. A handled Web API
   rejection is Slack JSON (`ok:false`, `error:"invalid_profile"`), not HTTP
   500.

## STATUS-02 — Represent availability and presence

1. A member sees an availability dot and an accessible Active/Away label beside
   every visible member identity. Color is supplementary; screen readers receive
   the state in text.
2. Selecting **Away** stores Slack's manual `away` presence for the signed-in
   member. Selecting **Active (automatic)** stores `auto`; it does not permanently
   store `active`, because Slack's `users.setPresence` accepts only `auto` and
   `away`.
3. `users.getPresence` returns the effective `active` or `away` value, while
   `users.setPresence` changes only the calling user and returns `{ok:true}`.
   Invalid values return `invalid_presence`. The durable manual value and its
   change event remain workspace/member isolated across memory, SQL, and gRPC.
4. Automatic presence may report active only when the service has truthful
   client-activity evidence. Until automatic ten-minute inactivity detection is
   implemented, the UI labels the control as automatic and MUST NOT claim that a
   green dot proves a live connection. Deactivated members and bots do not
   receive fabricated human activity.

## STATUS-03 — Schedule statuses in advance

1. **Update your status → Duration → Choose a start and end time** lets a member
   define editable status text/emoji plus future local start and end instants.
2. Save creates one of at most five scheduled statuses. It does not replace the
   current status before its start. The profile menu lists upcoming statuses in
   chronological order with localized times.
3. Before start, the member can open an upcoming status, edit every field, or
   delete it. At start it becomes the current status once; at end it clears only
   if it is still the active scheduled status. Manual replacement cannot later
   be erased by a stale scheduled end.
4. Starts/ends and the five-item limit survive restart and hibernation, publish
   the lifecycle wake deadline, and remain correct across time-zone/DST changes.
   Slack exposes this as a first-party client journey, not a fabricated Web API
   method.

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

The member selects a preset/custom DND duration or notification schedule. A
schedule names the days and the hours notifications are allowed, is read in the
member's own time zone rather than the server's, and suppresses delivery outside
its window; a window whose end precedes its start runs overnight and belongs to
the day it began on. A schedule covering no day, or a window of zero length, is
refused rather than saved, because either would silence the member with nothing
on the page to explain it. The
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

## What this deployment delivers, and what it does not

A desktop notification needs three separate yeses, and this product owns two of
them: the stored preference, and Do Not Disturb not being active. The third —
the browser's own permission — belongs to the browser, and the preferences page
reports what the browser actually says rather than folding all three into one
silent "off". Its text comes from the timeline the client fetched under its own
session, never from the event frame, because the durable payloads carry
identifiers and no content by design.

Unimplemented, and named as such on the page rather than rendered as a control
that does nothing:

- **Push to a mobile device.** There is no mobile application and no push
  service.
- **E-mail.** This deployment sends no mail at all, which is also why an
  invitation link has to be handed over by an administrator.
- **Sounds.** Pausing and the notification schedule are the only delivery
  controls; there is no sound to configure because none is played.

## Evidence

- Workspace trigger level, exact case-insensitive channel keywords, Activity
  channel/reminder inclusion, channel overrides/mute, follow-every-thread, and
  individual thread follows are durable per member. Memory, SQLite reopen,
  generated gRPC converter-property, and local-versus-gRPC differential tests
  cover inheritance and routing.
- The first-party UI exposes the workspace settings, an exceptions list,
  channel controls, follow/unfollow in the thread pane, and preset/custom DND
  with early resume. DND reuses the Slack-compatible `dnd.*` service model and
  suppresses delivery state without deleting messages or Activity.
- `make external-contract-qualification` checks Slack's current workspace
  trigger, keyword, Activity inclusion, conversation override, exception-list,
  and follow-every-thread text before this behavior can remain claimed.
- Deterministic-clock and persistence tests cover current-status and DND
  expiry through restart and hibernation.
- Future status scheduling is a typed first-party journey across the browser,
  service, generated gRPC seam, memory and SQL repositories, and lifecycle
  worker. Tests cover the five-item limit, create/edit/cancel, chronological
  reads, SQLite reopen, stale-revision rejection, competing workers, missed
  windows, and the scheduled start wake deadline. It deliberately does not add
  a fictional Slack Web API method.
- Official SDK tests exercise `users.profile.*`, `users.setPresence`, and
  `dnd.*` with user tokens and permission/error variants.
- Controlled live-Slack comparison for advance status scheduling remains
  required. Browser/push/email/sound delivery, per-platform timing and
  appearance, notification schedules, group-DM overrides, urgent sender
  override, VIPs, and notification deep-link reconciliation are explicit gaps;
  the web UI does not claim those controls.
## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| PROFILE-01 | [Set status and availability](https://slack.com/help/articles/201864558-Set-your-Slack-status-and-availability) | Slack exposes status and availability anywhere member identity is shown. |
| PROFILE-02 | [Set status and availability](https://slack.com/help/articles/201864558-Set-your-Slack-status-and-availability) | Members edit their own visible profile and status information. |
| STATUS-01 | [Set status and availability](https://slack.com/help/articles/201864558-Set-your-Slack-status-and-availability) | Status text, emoji, suggestions, and a clear time are user-selectable. |
| STATUS-02 | [Set status and availability](https://slack.com/help/articles/201864558-Set-your-Slack-status-and-availability) | Slack distinguishes automatically inferred and manually selected active or away availability. |
| STATUS-03 | [Set status and availability](https://slack.com/help/articles/201864558-Set-your-Slack-status-and-availability) | Slack permits up to five future statuses with editable start/end times and pre-start management. |
| NOTIFY-01 | [Configure Slack notifications](https://slack.com/help/articles/201355156-Configure-your-Slack-notifications) | Workspace notification triggers, keywords, timing, sound, and delivery are member preferences. |
| NOTIFY-02 | [Manage notifications for specific channels and direct messages](https://slack.com/help/articles/360056534254-Manage-notifications-for-specific-channels-and-direct-messages) | Channels and group DMs support all-post/mention choices, mute, following every thread, and a reviewable exceptions list. |
| NOTIFY-03 | [Pause Slack notifications](https://slack.com/help/articles/214908388-Pause-notifications-with-Do-Not-Disturb) | Members pause, resume, schedule, and urgently override notifications under Slack's rules. |
| NOTIFY-04 | [Guide to Slack notifications](https://slack.com/help/articles/360025446073-Guide-to-Slack-notifications) | Eligible events route to badges and configured desktop, mobile, and email notifications. |

Sources checked 2026-07-30:

- [Set your Slack status and availability](https://slack.com/help/articles/201864558-Set-your-Slack-status-and-availability)
- [Configure your Slack notifications](https://slack.com/help/articles/201355156-Configure-your-Slack-notifications)
- [Guide to Slack notifications](https://slack.com/help/articles/360025446073-Guide-to-Slack-notifications)
- [Manage notifications for specific channels and direct messages](https://slack.com/help/articles/360056534254-Manage-notifications-for-specific-channels-and-direct-messages)
- [Pause notifications with Do Not Disturb](https://slack.com/help/articles/214908388-Pause-notifications-with-Do-Not-Disturb)
