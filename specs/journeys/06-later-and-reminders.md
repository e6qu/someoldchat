# Later and reminders

## LATER-01 — Save an item for later

From a message/file action or Slack's focused-message `A` shortcut, the member
can save the item. The save control changes state once, the Later navigation
reflects the item, and removing the save reverses only the member's saved
state.

Saving does not pin the message, notify the channel, duplicate the source, or
grant access the member does not otherwise have.

Current Later is a first-party Slack client feature, not a Slack app API.
Implementations MUST NOT write it through `stars.*`, infer it from
`stars.list`, or expose a private Later item to an app token. Slack's developer
changelog says legacy stars do not appear in Later and Later items are not
returned to apps.

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

## REMIND-02 — Create and manage a personal reminder

From Later, the member uses Slack's add control to create a personal reminder
with a date, time, and description. A date without a time defaults to 9:00 AM in
the member's time zone. The member can edit, complete, or delete the reminder
from Later. A reminder created from a message or file follows the same private
Later lifecycle while retaining its source.

Current Slack Help does not document `/remind me` as the personal-reminder entry
point. SameOldChat MUST NOT recreate that legacy command merely because the
deprecated `reminders.*` Web API still exists.

## REMIND-03 — Create and manage a channel reminder

In a conversation, Slack's `/remind [#channel] [what] [when]` built-in creates a
channel reminder. The target must be the current channel or another channel the
member is allowed to address. Guests can create personal reminders only. The
client previews or rejects ambiguous interpretations rather than guessing.
Unsupported recurrence, past dates, invalid targets, and permission failures
are handled errors.

`/remind list` returns a private list of channel reminders created by the
member. A channel reminder cannot be edited: Slack's documented journey is to
delete it and create a replacement. Repeating channel reminders can repeat by
day or calendar cadence, but not by arbitrary time intervals.

App-defined `/remind` MUST NOT intercept Slack's built-in command.

## REMIND-04 — Deliver reminders

At the due instant, one durable worker produces the Slack-compatible reminder
notification and Activity/Later projection once, including a link to any source
message. Delivery survives restart, hibernation, DST/time-zone changes, and
worker races. Recurrence schedules the next occurrence without duplicating the
current one.

Personal reminders appear in Later and produce the documented Later and
Activity badges when due. Channel reminders post to the target conversation.
Cancellation racing delivery has one outcome. Past, completed, and deleted
reminders do not fire.

## REMIND-API-01 — Preserve the deprecated app contract without conflating it
with Later

Slack began retiring `reminders.add`, `reminders.complete`,
`reminders.delete`, `reminders.info`, and `reminders.list` in March 2023 and
describes them as degraded or useless. They are a legacy app compatibility
surface, not the backing store for current Later.

Where retained, each method follows its current official request, response, and
error contract and is qualified through current official Node, Python, and Java
SDKs. SDK decoding is only `sdk-compatible` evidence. Natural-language parsing,
recurrence, token-type-dependent targeting, delivery, and live-Slack outcomes
require separate behavioral and differential evidence before the ledger can
claim more.

## Evidence

Implemented evidence:

- `make external-contract-qualification` fetches Slack's current official
  reminder, Later, and developer references and fails when the source no longer
  supports the journey's entry points, organization, privacy, time-zone,
  recurrence, editability, guest, retirement, or API-separation assertions.
- Playwright drives focused-message `A`, save/unsave, source navigation,
  In progress, Completed, Archived, restore, removal, and automated WCAG checks
  in Chromium, Firefox, and WebKit.
- Service, portable-store, SQL-migration, and local-versus-gRPC differential
  tests cover idempotency, exact-member isolation, pagination, state
  transitions, and content redaction after source access is lost.
- Current official Node, Python, and Java SDK qualification continues to
  exercise deprecated `stars.*` and `reminders.*` as separate app contracts.
  No SDK suite is cited as Later evidence because Slack exposes no current
  Later Web API.

Still required before the reminder journeys are complete:

- reminder presets/custom time, Later creation/editing, `/remind` channel
  parsing and private listing, durable delivery, Activity projection,
  recurrence, delete-and-recreate channel rescheduling, and upcoming-reminder
  filtering;
- deterministic-clock persistence tests for time zones, hibernation wake
  deadlines, worker races, recurrence, and terminal failures; and
- controlled live-Slack differential observations for organization, source
  loss, live reconciliation, and `/remind` parsing.

Sources checked 2026-07-29:

- [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later)
- [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder)
- [`reminders.add`](https://docs.slack.dev/reference/methods/reminders.add/)
- [`reminders.complete`](https://docs.slack.dev/reference/methods/reminders.complete/)
- [`reminders.delete`](https://docs.slack.dev/reference/methods/reminders.delete/)
- [`reminders.info`](https://docs.slack.dev/reference/methods/reminders.info/)
- [`reminders.list`](https://docs.slack.dev/reference/methods/reminders.list/)
- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
- [Slack developer changelog: It’s later already for stars and reminders](https://docs.slack.dev/changelog/2023-07-its-later-already-for-stars-and-reminders/)
