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

**Entry points:** Activity in navigation; `Command+Shift+M` on the macOS
desktop app or `Control+Shift+M` on Windows/Linux desktop; and Slack web's
navigation-tab shortcut, `Control+3` on macOS or `Control+Shift+3` on
Windows/Linux.

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
between items, `Enter` replies to the focused message, `X` selects or
unselects it for bulk actions, and `C`/`R` clear or mark read only inside
Activity. Clearing is not deletion: the source message remains and the cleared
item is recoverable where Slack permits.

New live events update counts without stealing focus or moving the currently
triaged item. Reconnect/replay MUST not duplicate Activity.

## Evidence

Implemented evidence:

- `make external-contract-qualification` checks the current 2026 Activity
  source for typed filters, dense/detailed layouts, read-versus-clear
  semantics, recoverable cleared items, bulk actions, and keyboard triage
  rather than relying on the legacy Activity article.
- Activity items and per-member presentation state are durable in memory,
  SQLite, dqlite, and PostgreSQL through the shared SQL implementation. The
  local and gRPC compositions expose the same typed, paginated contract.
- New DMs and MPIM messages, explicit mentions, replies to a thread root
  authored by the member, reactions to the member's messages, applicable
  app-authored notifications, and delivered personal reminders create one
  idempotent item per recipient. Overlapping filters share one triage record.
- Browser qualification creates and OAuth-installs a real app, exchanges its
  authorization code, joins the public channel with Slack's current bot
  `channels:join` scope, and posts overlapping app/mention notifications. It
  covers typed/cleared/unread filters, persisted layout, empty states,
  accessibility, source-thread navigation, read-cursor reconciliation, and
  the documented navigation shortcut in Chromium, Firefox, and WebKit. Web,
  repository, migration, converter-property, and differential tests cover
  item hydration, filter overlap, pagination, read/clear/restore, reaction
  removal, source authorization, and persistence.
- Activity-local Up/Down, Enter, `X`, `C`, and `R` are exercised against those
  real items; Enter opens the correct thread reply composer. Clearing marks
  read and hides the item, while restore preserves that read state.

Known gaps, which MUST NOT be reported as full Activity compatibility:

- invitation, VIP, all-new-post channel/section notifications, following a
  thread the member did not author, and custom saved views depend on notification
  preferences or product models not yet implemented; those filters are not
  rendered as empty fake controls;
- the first-party UI does not yet expose mark-unread or inline react actions,
  and Activity does not yet merge live SSE updates while preserving focus;
- schema version 107 creates the durable Activity store but does not backfill
  notification history created by an older release. Existing source messages
  remain available through conversation/search history; Activity begins with
  notification-producing mutations committed after the upgrade;
- search still lacks Slack's modifier parser, result-type grouping, current
  conversation scope, suggestions, and official-SDK/differential evidence;
- controlled live-Slack comparison and visual baselines remain required.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| SEARCH-01 | [Search in Slack](https://slack.com/help/articles/202528808-Search-in-Slack) | Search offers suggestions and Messages, Files, People, Channels, and Canvases result types. |
| SEARCH-02 | [Search in Slack](https://slack.com/help/articles/202528808-Search-in-Slack) | Slack supports modifiers plus result filtering and sorting. |
| SEARCH-03 | [Search in Slack](https://slack.com/help/articles/202528808-Search-in-Slack) | Command or Control F searches the current conversation. |
| ACTIVITY-01 | [Get work done from Activity](https://slack.com/help/articles/19693583638803-Get-your-work-done-from-the-Activity-view) | Activity aggregates recent messages and notifications with dense and detailed layouts. |
| ACTIVITY-02 | [Introducing the new Activity view](https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack) | Activity provides typed filters, custom views, and plan-dependent channel or section filters. |
| ACTIVITY-03 | [Navigate Slack with your keyboard](https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard) | Activity supports Up/Down navigation, Enter to reply, X selection, C clear, and R mark-read actions. |

Sources checked 2026-07-30:

- [Search in Slack](https://slack.com/help/articles/202528808-Search-in-Slack)
- [Get your work done from Activity](https://slack.com/help/articles/19693583638803-Get-your-work-done-from-the-Activity-view)
- [Introducing the new Activity view](https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack)
- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
