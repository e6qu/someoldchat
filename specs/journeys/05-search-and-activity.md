# Search and Activity

## SEARCH-01 — Search the workspace

**Entry points:** The top search control and Slack's global
`Command/Control+G` shortcut.

The search surface accepts natural terms and Slack's supported modifiers,
suggests recent searches and relevant people/channels/files as the member
types, and never reveals a private object the member cannot discover. Submitting
opens a results view without losing the originating conversation or draft.

Results are grouped into Slack's current types, including Messages, Files,
People, Channels, and Canvases when available. Each result exposes enough
author, destination, date, and content context to disambiguate it and opens the
real object/permalink.

## SEARCH-02 — Refine and paginate results

The member can filter by result type and Slack-supported facets/modifiers such
as sender, conversation, date, mentions, reactions, pins, links, threads, and
file type; sort where Slack offers it; clear filters individually; and move
through every result without duplication or omission. The URL preserves a
shareable/reloadable query without containing credentials.

Malformed modifiers, no results, permission changes, deleted results, an
expired cursor, indexing delay, and backend failure are distinct. Search MUST
not claim zero results when it failed.

## SEARCH-03 — Search the current conversation

`Command/Control+F` searches the open conversation or thread according to
Slack's current client behavior. The destination scope is explicit and can be
broadened. Opening a hit positions and marks the exact message while preserving
the query and a route back to results.

## ACTIVITY-01 — Review notifications in Activity

**Entry points:** Activity in navigation and
`Command/Control+Shift+M`.

Activity aggregates the member's current Slack notification events, including
DMs, mentions, thread replies, channel notifications, reactions, invitations,
apps, reminders, and VIP activity as applicable. Dense and detailed layouts
preserve the same items and state. Each item identifies actor, source,
destination, time, unread/cleared state, and enough content to decide what to
do.

Selecting an item opens its message/thread/object and marks or preserves read
state exactly as Slack does. The member can return without losing Activity
position and filters.

## ACTIVITY-02 — Filter and organize Activity

The member can use Slack's current filters—DMs, Mentions, Threads, Channels,
Reactions, Invitations, Apps, Reminders, VIP, and cleared items where
available—and custom Activity views when the plan supports them. Filter counts,
empty states, URLs, and item ordering derive from the same notification state,
not independent mock lists.

## ACTIVITY-03 — Triage Activity

Slack-supported item and bulk actions include mark read/unread, clear/restore,
reply, react, open in context, and filter-specific actions. Up/Down moves
between items, `Enter` opens, and current Slack Activity shortcuts such as
`C`/`R` apply only inside Activity. Clearing is not deletion: the source
message remains and the cleared item is recoverable where Slack permits.

New live events update counts without stealing focus or moving the currently
triaged item. Reconnect/replay MUST not duplicate Activity.

## Evidence

- Seed one authorized and one unauthorized object of every result/notification
  class, then prove the authorized set through browser, Web API, events, and
  permission changes.
- Run search syntax/results through current official SDK response models.
- Execute global/current search and full Activity keyboard triage in Chromium,
  Firefox, and WebKit, including zero/error/loading/reconnect states.
- Differential fixtures normalize rank and timestamps but compare object
  classes, filters, actions, visibility, and navigation.

Sources checked 2026-07-29:

- [Search in Slack](https://slack.com/help/articles/202528808-Search-in-Slack)
- [Get your work done from Activity](https://slack.com/help/articles/19693583638803-Get-your-work-done-from-the-Activity-view)
- [Introducing the new Activity view](https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack)
- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
