# Search and Activity

## SEARCH-01 — Search the workspace

**Preconditions:** The member has an authenticated workspace session. The
workspace contains a mixture of visible and inaccessible public-channel,
private-channel, direct-message, file, member, canvas, and archived content.

**Entry and focus:** The member can activate the search control in the workspace
header or press `Command+G` on macOS and `Control+G` on Windows/Linux. The
shortcut focuses and selects the current search text without clearing a
composer or thread draft. `Escape` returns focus without mutating that draft.
Typing in Slack's search surface offers recent searches and relevant
people/channels/files; selecting a suggestion opens the same durable object as
submitting the equivalent query.

**Submit and results:** A non-empty natural-language query or supported modifier
opens a reloadable search URL with the query still editable and focus in the
search field. The result-type navigation contains Slack's current Messages,
Files, People, Channels, and Canvases types when the workspace supports those
objects. Switching type preserves the query and applicable filters. A message
hit identifies its author, destination, time, and matching content and opens
the containing conversation at the exact message. A file identifies its safe
name/title, type, uploader, destination, and time and opens its Slack file
surface or authorized download. People, channel, and canvas hits open the real
profile, conversation, or canvas.

Search authorization is evaluated before ranking, totals, pagination, and
suggestions. Content from an inaccessible private conversation, closed DM,
deleted object, or another workspace does not contribute a count or timing
side-channel. Empty results are distinct from a failed search. Refresh, Back,
Forward, and opening a hit in another tab preserve a useful route to the same
query; no URL contains a session, OAuth code, or token.

## SEARCH-02 — Refine and paginate results

**Query language:** Quoted text is a phrase, a leading `-` excludes a term or
modifier, and Slack modifiers can be combined. The current language includes
sender (`from:`), conversation (`in:`), participants (`with:`), mentions
(`to:`), dates (`before:`, `after:`, `on:`, and `during:`), saved/pinned/thread
state (`is:`), content (`has:` including links, files, reactions, and specific
emoji where offered), and file type (`type:`). Names and IDs resolve inside the
current workspace; ambiguous or unknown references do not silently broaden the
query. `hasmy::emoji:` means the current member reacted with that emoji, and an
asterisk after a partial word of at least three characters requests Slack's
prefix matching.

**Filters and ordering:** Result-type controls and Slack's visible filter UI
round-trip to the same query semantics as typed modifiers. The member can
filter sender, conversation, date, content, reaction, thread, and file type
where applicable, clear an individual refinement, and choose the ordering
Slack offers for that result type (including relevance and recency). The
selected type, scope, refinements, and order survive reload and pagination.
Screen-reader names and selected/current states expose the same filter state as
the visual controls.

**Pagination and change:** Every authorized match is reachable without
duplication or omission. A page boundary is deterministic for equal timestamps,
and totals never count an invisible object. New, edited, deleted, archived, or
newly inaccessible content may change a subsequent fresh search, but an
invalid/expired cursor or out-of-range legacy page produces a recoverable
validation state rather than restarting invisibly.

Malformed quoting/modifiers, an unknown filter value, no results, permission
change, deleted result, indexing delay, and backend failure are distinguishable.
The query and valid filters remain editable after a handled error. Search MUST
not claim zero results when it failed.

## SEARCH-03 — Search the current conversation

**Preconditions:** A channel, DM, group DM, or thread belonging to the member is
open. Its composer may contain an unsent draft.

`Command+F` on macOS and `Control+F` on Windows/Linux opens Slack search scoped
to the current conversation. The search field receives focus, the scope is
visible before the first submission, and the draft remains unchanged. The
route carries an opaque conversation identifier rather than relying on a
display name. Searching from a thread follows Slack's current thread versus
parent-conversation scope and labels that choice.

Only authorized messages/files in that destination contribute hits and totals.
All SEARCH-02 refinements and failure states still apply. A clearly named
control broadens the same query to the workspace without losing text or
applicable filters. Opening a hit positions and marks the exact message while
preserving the query and a route back to results. Browser-native Find remains
available through the browser's own menus even where Slack handles the
keystroke.

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
- Adding a member to a public or private channel creates one Invitations item
  atomically with the new membership. The item identifies the inviter, links to
  the real channel, survives restart, and is not duplicated by a repeated
  invite. Official Slack Help and all three official SDK qualifications cover
  the corresponding product/API boundary.
- New DMs and MPIM messages, explicit mentions, replies to a thread root
  authored or followed by the member, replies in channels configured to follow
  every thread, all-new-post channel notifications, exact channel-keyword
  matches, reactions to the member's messages, applicable app-authored
  notifications, and delivered personal reminders create one idempotent item
  per recipient. Overlapping filters share one triage record.
- Direct and enabled user-group mentions use Slack's stable user/subteam
  transport references. An enabled group expands atomically to its current
  members; a public-channel mention can reach an active workspace member who
  has not joined, while private channels and DMs remain membership- and
  access-group-fenced. Disabled groups do not create mention Activity.
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
  read and hides the item, while restore preserves that read state. The UI
  exposes per-item and bulk mark-unread as the inverse durable operation.
  Message-backed items expose the same searchable/category/skin-tone reaction
  picker as the composer. Live source events refetch the current filtered
  Activity projection, deduplicate through durable item IDs, preserve selected
  items, focused controls, scroll position, and the exact active filters, and
  announce reconnect/update state without moving the item being triaged.
- Search parses quoted/excluded terms and current sender, conversation, date,
  thread, saved, file, pin, reaction, and file-type
  modifiers into a typed domain request. Message and file search apply
  conversation visibility before totals and pagination in memory, SQLite,
  dqlite, and PostgreSQL through the shared store contract; folded persisted
  columns preserve Unicode-insensitive matching after SQL reopen. File listing,
  search, metadata, and download use the same public/private visibility rule.
- The local and generated gRPC compositions carry typed query, ordering,
  pagination, file totals, and viewer identity, with differential coverage for
  message and file results. Slack Web API `search.messages`, `search.files`,
  and legacy combined `search.all` require user tokens, enforce documented
  count/page/order inputs, and are invoked and decoded by the pinned official
  Node, Python, and Java SDKs.
- Following a result arrives at the message it names. Two mechanisms carry it
  and neither is obvious: the permalink's window cursor ends just after the hit,
  so the hit is the last message in the window, and the fragment focuses it
  because a message carries `tabindex="-1"`. A browser journey pins both, so a
  change to either is caught rather than silently moving the reader to the wrong
  end of the right window. The arrival is announced and briefly marked.
- Results mark the terms that matched. Marking is threaded into the one branch
  of the Slack-markup renderer that emits literal prose rather than applied to
  its output, because a substitution over finished HTML would put tags inside
  attributes, split entities, and mark the `a` in an anchor. A reference's
  visible label is marked and its target never is. Two consequences are
  asserted rather than hidden: a term split by a formatting boundary is left
  unmarked, and a term whose fold changes byte length is left unmarked because
  the span could no longer be mapped back without corrupting a character.
  Message results also render their body rather than their raw source, so
  mentions, emoji and formatting read as they do in the conversation.
- People and Channels results come from the store with a query and a cursor.
  They used to be produced by filtering a member directory and channel list the
  handler paged into memory in full on every search request, including a
  Messages search that read neither: correct on a small workspace, unbounded
  work per request on any other, and unpageable by construction. Channel search
  is the member conversation listing with a query, so its visibility rule is the
  sidebar's rather than a second copy that could drift; a browser journey
  creates a matching public and private channel from an installed app and
  asserts the session finds one and not the other. The from:/in: pickers are now
  bounded to one page, with the existing typeahead as the way to reach the rest.
- Canvas search is answered from the prose inside the stored document rather
  than from the JSON it is stored as, so a term matching a structural key finds
  nothing and a heading a member can see is findable. The SQL profile keeps a
  folded column written on every canvas write and backfilled on migration; the
  memory profile folds on read. Cross-profile qualification drives both to the
  same matches, the same exclusions, and the same silence for a reader with no
  grant — the visibility rule is the canvas directory's, so a search cannot
  disclose a title the directory would withhold.
- The first-party search surface provides real Messages, Files, Canvases,
  People, and Channels results, durable per-member recent searches, and visibility-aware
  typeahead links to real people, channels, and hosted files. It also provides
  URL-backed sender/conversation/date/content/order filters, authenticated file
  links, explicit current-conversation scope, and `Command/Control+F`.
  Three-engine browser qualification covers the shortcut, recent-search and
  keyboard typeahead selection, typed tabs, persisted order, real hosted-file
  search, accessibility automation, and real-object navigation. The shared
  persistence qualification runs recent-search privacy, ordering, and
  deduplication against memory, SQLite, dqlite, and PostgreSQL; migration/reopen
  and local/generated-gRPC tests cover the remaining boundaries.

Known gaps, which MUST NOT be reported as full Activity compatibility:

- Slack Connect and canvas-share invitations, VIP and section notifications,
  and custom saved views depend on product models not yet implemented. Internal
  public/private channel additions are implemented in Invitations; unsupported
  invitation types are not fabricated;
- schema version 107 creates the durable Activity store but does not backfill
  notification history created by an older release. Existing source messages
  remain available through conversation/search history; Activity begins with
  notification-producing mutations committed after the upgrade;
- search does not yet provide Slack's semantic relevance scoring or
  highlighting — a canvas result carries a leading snippet rather than a marked
  match, because a substring search for the term would mark the wrong span
  whenever the match was in the title — every `to:`/link/specific emoji/`hasmy:` modifier,
  prefix `*`, section-valued `in:`, Slack's natural month/year date forms,
  participant-accurate `with:` thread semantics, or history on both sides of an
  opened hit — the result window ends at the message it names, so a reader
  cannot scroll forward into newer messages without navigating again;
- the Slack APIs retain documented compatibility deviations for relevance
  scoring, highlight markers, cursor pagination on file/combined legacy
  results, match projection detail, and full tier rate limiting;
- controlled live-Slack comparison and visual baselines remain required.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| SEARCH-01 | [Search in Slack](https://slack.com/help/articles/202528808-Search-in-Slack) | Search offers suggestions and Messages, Files, People, Channels, and Canvases result types. |
| SEARCH-02 | [`search.messages`](https://docs.slack.dev/reference/methods/search.messages/) | Slack's API specifies query, count, page/cursor, relevance/timestamp sort, and ascending/descending order inputs. |
| SEARCH-03 | [Search in Slack](https://slack.com/help/articles/202528808-Search-in-Slack) | Command or Control F searches the current conversation and the search can be broadened. |
| ACTIVITY-01 | [Get work done from Activity](https://slack.com/help/articles/19693583638803-Get-your-work-done-from-the-Activity-view) | Activity aggregates recent messages and notifications with dense and detailed layouts. |
| ACTIVITY-02 | [Introducing the new Activity view](https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack) | Activity provides typed filters, custom views, and plan-dependent channel or section filters. |
| ACTIVITY-03 | [Navigate Slack with your keyboard](https://slack.com/help/articles/115003340723-Navigate-Slack-with-your-keyboard) | Activity supports Up/Down navigation, Enter to reply, X selection, C clear, and R mark-read actions. |

Sources checked 2026-07-31:

- [Search in Slack](https://slack.com/help/articles/202528808-Search-in-Slack)
- [Slack updates and changes](https://slack.com/help/articles/115004846068-Slack-updates-and-changes/)
- [`search.all` method](https://docs.slack.dev/reference/methods/search.all/)
- [`search.files` method](https://docs.slack.dev/reference/methods/search.files/)
- [`search.messages` method](https://docs.slack.dev/reference/methods/search.messages/)
- [Get your work done from Activity](https://slack.com/help/articles/19693583638803-Get-your-work-done-from-the-Activity-view)
- [Introducing the new Activity view](https://slack.com/help/articles/46751260742035-Introducing-the-new-Activity-view-in-Slack)
- [Slack keyboard shortcuts and commands](https://slack.com/help/articles/201374536-Slack-keyboard-shortcuts-and-commands)
