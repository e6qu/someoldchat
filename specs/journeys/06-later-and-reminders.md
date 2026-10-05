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
Activity badges when due. Slackbot posts channel reminders to the target
conversation, and posts a `reminders.add` reminder into the member's Slackbot
direct message. Slackbot is `USLACKBOT` in every workspace, as on Slack.
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
recurrence, token-type-dependent targeting, and delivery need behavioral tests
against those references before the ledger can claim more. The references
decide the targeting rule: a user token cannot set another member's reminder
(`cannot_add_others`), while a bot token may set a one-time reminder for one.
They also decide the reminder object: only a non-recurring reminder carries
`time` and `complete_ts`
([`reminders.list`](https://docs.slack.dev/reference/methods/reminders.list/)).

`reminders.complete` implements the error contract Slack documents: a recurring
reminder answers `cannot_complete_recurring` and another member's reminder
answers `cannot_complete_others`, each told apart from a reminder that does not
exist rather than collapsed into `not_found`. `reminders.add` creates a
recurring reminder from a recurring phrase in `time` ("every Thursday") or from
the `recurrence` argument, and the completion guard keeps such a reminder from
being marked done as if it were a one-off.

## Evidence

- REMIND-04 and REMIND-API-01 are not browser journeys, for two different
  reasons. Reminder delivery is performed by `cmd/worker`, which the browser
  harness does not run and could not usefully run: its servers use `-store
  memory`, so a separate worker process would share no state with them.
- `make external-contract-qualification` fetches Slack's current official
  reminder, Later, and developer references and fails when the source no
  longer supports the journey's entry points, organization, privacy, time-
  zone, recurrence, editability, guest, retirement, or API-separation
  assertions.
- Playwright drives focused-message `A`, save/unsave, source navigation, In
  progress, Completed, Archived, restore, removal, and automated WCAG checks
  in Chromium, Firefox, and WebKit. It also drives focused-message `M`,
  reminder presets and custom editing, personal completion/deletion, named
  weekday `/remind` recurrence, private `/remind list`, and channel-reminder
  deletion in all three engines.
- Service, portable-store, SQL-migration, and local-versus-gRPC differential
  tests cover idempotency, exact-member isolation, pagination, state
  transitions, guest restrictions, content redaction after source access is
  lost, worker lease races, retry idempotency, terminal failures, DST-safe
  recurrence, Activity/source projection, durable badge acknowledgement, and
  the minimum scheduled-message/reminder wake deadline.
- Current official Node, Python, and Java SDK qualification continues to
  exercise deprecated `stars.*` and `reminders.*` as separate app contracts.
  No SDK suite is cited as Later evidence because Slack exposes no current
  Later Web API.
- Later organization (In progress, Archived, and Completed, with reminders
  and newly saved items in In progress) follows
  [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later),
  as do source navigation, removal, and the due-reminder badges asserted by
  `make external-contract-qualification`. The `/remind` grammar is judged
  against the examples in
  [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder);
  a phrasing the parser does not read is refused rather than guessed, and the
  `reminders.add` ledger entry lists the phrasings it reads.
- Product choices where Slack publishes nothing: a monthly reminder anchored
  on the 29th–31st fires on the last day of a shorter month and returns to its
  anchored day afterwards (`TestNextReminderDueMonthlyClampsToMonthEndWithoutDrifting`).
  This holds from the first occurrence: `/remind … every month` (or
  `reminders.add` with that phrase) set on such a day after its time has
  passed first falls on the next month's last day, and the series keeps the
  day it was set on, so October 31st recurs on November 30th and then
  December 31st, and "every year" set on February 29th falls on the 28th in
  common years (`TestEveryMonthPhraseClampsFirstOccurrenceAndKeepsItsDay`).
  The anchor travels beside the due instant through the service and the gRPC
  seam;
  deleting a Later reminder while delivery holds its lease answers not found,
  so delivery and deletion have one outcome
  (`TestLaterReminderCannotBeDeletedWhileDeliveryOwnsTheLease`); and a
  `reminders.add` reminder deleted before delivery claims it is never
  delivered.
- Still missing: browser delivery evidence driven by a deterministic deployed
  worker clock rather than only the real UI plus deterministic service/web
  tests.
## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| LATER-01 | [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later) | Slack saves and removes private Later items from source actions. |
| LATER-02 | [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later) | Later exposes In progress, Archived, Completed, and upcoming reminder organization. |
| LATER-03 | [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later) | Later items can be completed, restored, archived, and removed. |
| REMIND-01 | [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder) | Message and file actions create private reminders associated with the source. |
| REMIND-02 | [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder) | Personal reminders are created and managed from Later in local time. |
| REMIND-03 | [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder) | The built-in remind command creates and privately lists channel reminders. |
| REMIND-04 | [Introducing the new Activity view](https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack/) | Due personal reminders appear in Activity while retaining their Later lifecycle. |
| REMIND-API-01 | [Stars and reminders changelog](https://docs.slack.dev/changelog/2023-07-its-later-already-for-stars-and-reminders/) | Deprecated reminder APIs are separate from current Later and remain degraded. |

Sources checked 2026-07-30:

- [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later)
- [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder)
- [`reminders.add`](https://docs.slack.dev/reference/methods/reminders.add/)
- [`reminders.complete`](https://docs.slack.dev/reference/methods/reminders.complete/)
- [`reminders.delete`](https://docs.slack.dev/reference/methods/reminders.delete/)
- [`reminders.info`](https://docs.slack.dev/reference/methods/reminders.info/)
- [`reminders.list`](https://docs.slack.dev/reference/methods/reminders.list/)
- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
- [Introducing the new Activity view](https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack/)
- [Slack developer changelog: It’s later already for stars and reminders](https://docs.slack.dev/changelog/2023-07-its-later-already-for-stars-and-reminders/)
