# Later and reminders

## LATER-01 — Save an item for later

From a message/file action or Slack's focused-message `A` shortcut, the member
can save the item. The save control changes state once, the Later navigation
reflects the item, and the action synchronizes with Slack's saved-item API
contract. Removing the save reverses only the member's saved state.

Saving does not pin the message, notify the channel, duplicate the source, or
grant access the member does not otherwise have.

## LATER-02 — Review Later

Later presents Slack's current In progress, Archived, and Completed organization
and available filters such as upcoming reminders. Each item preserves source
identity, destination, time, content preview, reminder/due state, and an action
to jump to the source. Missing or no-longer-accessible sources remain
intelligible without leaking content.

The surface distinguishes an empty list from loading and failure. Pagination
does not duplicate or skip items, and a live change from another client
reconciles in place.

## LATER-03 — Organize and complete Later work

The member can mark an item complete, move it back to in progress,
archive/unarchive, and perform Slack-supported source actions. Completed and
archived are not deletion. Bulk or list transitions are exact-member scoped,
idempotent, and preserve the source object.

## REMIND-01 — Set a reminder from a message

From the message menu or focused-message `M` shortcut, the member selects one of
Slack's suggested times or a custom date/time. The UI confirms the exact local
time and associates the reminder with the message and member. It neither posts
to the channel nor schedules a duplicate message.

## REMIND-02 — Create a personal or channel reminder

Slack's `/remind` built-in parses supported person/channel targets, text, dates,
times, and recurrence. The client previews or confirms ambiguous interpretations
rather than guessing. Permissions, unsupported recurrence, past dates, invalid
targets, and workspace limits return Slack-compatible handled errors.

App-defined `/remind` MUST NOT intercept Slack's built-in command.

## REMIND-03 — Deliver and manage reminders

At the due instant, one durable worker produces the Slack-compatible reminder
notification and Activity/Later projection once, including a link to any source
message. Delivery survives restart, hibernation, DST/time-zone changes, and
worker races. Recurrence schedules the next occurrence without duplicating the
current one.

The member can list, complete, delete, and reschedule reminders through Slack's
available UI and API. Cancellation racing delivery has one outcome. Past,
completed, and deleted reminders do not fire.

## Evidence

- Browser tests cover save/unsave, all Later states, reminder presets/custom
  time, `/remind` parsing, delivery, complete/archive/restore, and inaccessible
  sources.
- Current official SDKs exercise `stars.*` or Slack's current saved-item
  surface as applicable and `reminders.*`, with exact-member token isolation.
- Deterministic-clock persistence tests cover recurrence, time zones,
  hibernation wake deadlines, worker races, and terminal failures.
- Differential tests record current Slack Later organization and `/remind`
  parsing because these are not completely described by the archived API spec.

Sources checked 2026-07-29:

- [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later)
- [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder)
- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
