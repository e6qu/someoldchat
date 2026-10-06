# Saved, To-dos and reminders

Slack replaced Later with two surfaces: "Saved items have moved out of Later
(which is now To-dos)." Saved messages and files live in the **Saved** section
of Home; work with an optional due date lives in the **To-dos** tab, where
"Reminders attach a due date to your to-dos." The journey IDs below keep their
LATER-* names because executable evidence cites them; what they describe is
the current model.

## LATER-01 — Add an item to Saved

From a message or file's More actions, Slack's "Add to saved" (on mobile, "Save
for later"), the hover toolbar's bookmark, or the focused-message `A` shortcut,
the member saves the item. The control changes state once, and removing the
save ("Remove from saved items") reverses only the member's saved state.
"Your saved items are only visible to you, and you can access them from the
Saved section of Home."

Saving does not pin the message, notify the channel, duplicate the source, or
grant access the member does not otherwise have. A saved item has no state of
its own: Slack moved the In progress, Archived and Completed organization out
of saved items, so an item is saved or it is not.

Saved items are a first-party Slack client feature, not a Slack app API.
Implementations MUST NOT write them through `stars.*`, infer them from
`stars.list`, or expose a private saved item to an app token. Slack's developer
changelog says legacy stars do not appear in Later and "There are no direct
APIs for Save it for Later to integrate with".

## LATER-02 — Review Saved and To-dos

Saved, a section of Home's sidebar, lists the member's saved items newest
first. Each item preserves its source identity, conversation, author, time and
a content preview, and "Open in Home" shows "the rest of the conversation or
thread where it was originally shared", scrolled to the item. A source that is
missing or no longer readable remains in the list without leaking content.

To-dos, a tab in the rail, lists the member's open to-dos with their due dates
and a Done section. The filter offers Slack's three reminder groups — "Overdue
to-dos with reminders in the past, Upcoming reminders with due dates in the
future, and No reminder scheduled for to-dos with no reminder" — and "Sort by"
offers "Due date, Earliest date created, or Latest date created". A to-do made
from a message links to it.

Both surfaces distinguish an empty list from loading and failure. Pagination
does not duplicate or skip items under any sort or filter, and a live change
from another client reconciles in place.

## LATER-03 — Clean up Saved and finish To-dos

From Saved the member clears one item with its clear icon, and the clean-up
action "remove[s] all saved items or move[s] specific items to To-dos". Moving
an item is one step: it leaves Saved and becomes a to-do linked to its message.
In To-dos the member marks a to-do done and back, edits it, and deletes it.
Every transition is exact-member scoped and idempotent, and preserves the
source object.

## REMIND-01 — Set a reminder from a message

From the message menu ("Remind me about this") or the focused-message `M`
shortcut, the member selects one of Slack's suggested times or Custom. This
creates a to-do with a reminder, linked to the message and named after it. The
UI confirms the exact local time, and nothing is posted to the channel or
scheduled as a message.

## REMIND-02 — Add and manage a to-do and its reminder

"Click the To-dos tab in your sidebar. Click Add To-do … Enter the details of
your to-do and click Add reminder. Choose a time from the list, or select Custom
to pick your own. Click Add." A to-do may have no reminder. A reminder set
for a day — To-dos' "Tomorrow" and "Next week", a Custom date without a time,
or a `/remind` phrase such as "tomorrow" or "every Tuesday" — is due at the
member's default reminder time in their time zone: 9 a.m. unless they changed
it under Preferences, Notifications, "Set a default time for reminder
notifications" (`TestDefaultReminderTimeMovesRemindersSetForADay`).
`reminders.add` reads its phrases the same way. Slack names the drop-down but
not its entries; offering every half hour of the day is our choice. "Hover over a
to-do and click Edit reminder. To choose a new due date, select a new time or
click Custom. To remove a reminder from a to-do, click Clear due date."

Current Slack Help does not document `/remind me` as the personal-reminder entry
point. SameOldChat MUST NOT recreate that legacy command merely because the
deprecated `reminders.*` Web API still exists.

## REMIND-03 — Create and manage a channel reminder

In a conversation, Slack's `/remind [#channel] [what] [when]` built-in creates a
channel reminder. The target must be the current channel or another channel the
member is allowed to address. Guests can create to-dos with reminders only. The
client previews or rejects ambiguous interpretations rather than guessing.
Unsupported recurrence, past dates, invalid targets, and permission failures
are handled errors.

`/remind list` returns a private list of channel reminders created by the
member. A channel reminder cannot be edited: Slack's documented journey is to
delete it and create a replacement. Repeating channel reminders can repeat by
day or calendar cadence, but not by arbitrary time intervals. A channel
reminder is not a to-do and never appears in To-dos.

App-defined `/remind` MUST NOT intercept Slack's built-in command.

## REMIND-04 — Deliver reminders

At the due instant, one durable worker produces the reminder notification and
its Activity projection once, including a link to any source message. Delivery
survives restart, hibernation, DST/time-zone changes, and worker races.
Recurrence schedules the next occurrence without duplicating the current one.

"When you set a reminder, you'll receive a notification and see a badge on the
To-dos and Activity tabs when the date and time arrives." A to-do's reminder
coming due does not finish the to-do: it is overdue until the member marks it
done. Slackbot posts channel reminders to the target conversation, and posts a
`reminders.add` reminder into the member's Slackbot direct message. Slackbot is
`USLACKBOT` in every workspace, as on Slack. Cancellation racing delivery has
one outcome. Past, done, and deleted reminders do not fire.

## REMIND-API-01 — Preserve the deprecated app contract without conflating it
with To-dos

Slack began retiring `reminders.add`, `reminders.complete`,
`reminders.delete`, `reminders.info`, and `reminders.list` in March 2023 and
describes them as degraded or useless. They are a legacy app compatibility
surface, not the backing store for To-dos or Saved.

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
  reminder, saved-items, and developer references and fails when the source no
  longer supports the journey's entry points, organization, privacy, time-
  zone, recurrence, editability, guest, retirement, or API-separation
  assertions.
- Playwright drives Add to saved (focused-message `A`; the More actions item
  is asserted in place and order by the message-actions journey), Saved in
  Home, Open in Home, the clear icon, Move to To-dos from an item's menu, the
  clean-up actions (move selected to To-dos, remove all), the `/app/later`
  redirect, and automated WCAG checks. It also drives focused-message `M`
  ("Remind me about this"), Add To-do with a reminder, the reminder filter
  and sort, Edit reminder with Custom, Clear due date, renaming, Done, the
  Done tab, deletion, named weekday `/remind` recurrence, private
  `/remind list`, and the channel reminder's Delete action.
- Service, portable-store (`tests/persistence-qualification`: memory and
  SQLite here, dqlite and PostgreSQL in CI), SQL-migration, and local-versus-
  gRPC differential tests cover idempotency, exact-member isolation, paging
  under every sort and filter, the reminder groups, done/not done, edits that
  keep or restart delivery, guest restrictions, content redaction after source
  access is lost, worker lease races, retry idempotency, terminal failures,
  DST-safe recurrence, Activity/source projection, durable badge
  acknowledgement, and the minimum scheduled-message/reminder wake deadline.
- Schema 218 moves Later's records: a personal Later reminder becomes a to-do
  titled with its text, keeping its source message, due date, recurrence,
  delivery state and completion; a channel reminder stays a channel reminder;
  a saved item Later showed as Completed becomes a completed to-do linked to
  its message; an In progress or Archived saved item with an open to-do for the
  same message leaves Saved (the to-do is where it lives); every other In
  progress or Archived item stays in Saved
  (`TestSchema218MovesLaterIntoTodosAndSaved`).
- Current official Node, Python, and Java SDK qualification continues to
  exercise deprecated `stars.*` and `reminders.*` as separate app contracts.
  No SDK suite is cited as Saved or To-dos evidence because Slack exposes no
  app API for them.
- Product choices where Slack publishes nothing:
  - Archived Later items move to Saved: Slack's new model has no archive, and
    keeping the bookmark loses nothing the member chose to keep.
  - A to-do made from a message without a typed title ("Remind me about this",
    moving a saved item to To-dos, or a Completed Later item) is titled with
    the message's first line, shortened to 150 characters, or "Saved message"
    when the message has no text of its own, such as a file.
  - To-dos' Done section is a tab beside the open list, and "Mark done" is a
    check button on each row; Slack's article describes the reminder filter
    and sort but not where finished to-dos go.
  - Overdue means a reminder at or before the present; a to-do with no
    reminder sorts after every dated one under "Due date"; ties are broken by
    the to-do's identifier, so pages never repeat or skip.
  - Opening To-dos or Activity clears the due-reminder badge on both, which is
    one fact about the member's to-dos.
  - "Add to saved" is offered both in More actions, as Slack documents, and as
    the hover toolbar's bookmark the product already had; `Command/Control+
    Shift+S` opens Saved.
  - A to-do's title is at most 3,000 characters and its details at most 4,000.
  - The clean-up action's "move specific items to To-dos" is a selection mode
    of Saved (a checkbox on each item); "remove all" removes every saved item
    of the member's at once.
  - A monthly reminder anchored on the 29th–31st fires on the last day of a
    shorter month and returns to its anchored day afterwards
    (`TestNextReminderDueMonthlyClampsToMonthEndWithoutDrifting`). This holds
    from the first occurrence: `/remind … every month` (or `reminders.add`
    with that phrase) set on such a day after its time has passed first falls
    on the next month's last day, and the series keeps the day it was set on,
    so October 31st recurs on November 30th and then December 31st, and
    "every year" set on February 29th falls on the 28th in common years
    (`TestEveryMonthPhraseClampsFirstOccurrenceAndKeepsItsDay`). The anchor
    travels beside the due instant through the service and the gRPC seam.
  - Deleting a to-do or channel reminder while delivery holds its lease
    answers not found, so delivery and deletion have one outcome
    (`TestTodoCannotBeDeletedWhileDeliveryOwnsTheLease`); a `reminders.add`
    reminder deleted before delivery claims it is never delivered.
- Still missing: browser delivery evidence driven by a deterministic deployed
  worker clock rather than only the real UI plus deterministic service/web
  tests.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| LATER-01 | [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later) | Saved items are private, added from More actions, and found in the Saved section of Home. |
| LATER-02 | [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later) | Saved items open in Home in context; To-dos filter by overdue, upcoming and none and sort by due or created date. |
| LATER-03 | [Save messages and files for later](https://slack.com/help/articles/360042650274-Save-messages-and-files-for-later) | Saved items are cleared one at a time, all at once, or moved to To-dos. |
| REMIND-01 | [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder) | Message and file actions create a to-do with a reminder linked to the source. |
| REMIND-02 | [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder) | To-dos are added from the To-dos tab with a preset or custom reminder, edited, and cleared of their due date. |
| REMIND-03 | [Set a reminder](https://slack.com/help/articles/208423427-Set-a-reminder) | The built-in remind command creates and privately lists channel reminders. |
| REMIND-04 | [Introducing the new Activity view](https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack/) | Due reminders appear in Activity and badge To-dos and Activity without finishing the to-do. |
| REMIND-API-01 | [Stars and reminders changelog](https://docs.slack.dev/changelog/2023-07-its-later-already-for-stars-and-reminders/) | Deprecated reminder APIs are separate from To-dos and Saved and remain degraded. |

Sources checked 2026-10-05:

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
