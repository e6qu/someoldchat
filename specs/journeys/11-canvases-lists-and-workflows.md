# Canvases, lists, and workflows

## CANVAS-01 — Create, open, and add a canvas as a tab

A member can create a canvas from Files or the mobile Canvases browser. From a
channel or DM, the member can add an existing canvas as a tab or create a new
canvas and then review its access before sharing it into the conversation.
SameOldChat MUST NOT invent a second “channel canvas” object: the tab, Files
browser, shared link, message reference, and mobile Canvases entry resolve to
the same durable canvas and permission record.

The surface identifies title, owner, sharing state, last update, and edit/view
permission. Creation commits one durable canvas before it opens. Adding a tab
does not silently broaden access; the access review and resulting view/edit
rights match Slack. A missing, deleted, restored, inaccessible, or concurrently
reshared canvas has an explicit outcome and never renders a decorative editor.

## CANVAS-02 — Edit and collaborate on a canvas

Slack-supported document content—headings, paragraphs, lists, links, mentions,
files/media, code, tables and embeds where available—round-trips through the UI
and current canvas API without silent loss. Concurrent changes use Slack's
revision/conflict behavior; a stale save cannot overwrite newer content
silently. Autosave status is truthful and offline/reconnect retains recoverable
changes.

Comments, reactions, notifications, presence, history, and sharing follow
current Slack capability and workspace policy. Unsafe markup/URLs are
sanitized without changing visible safe content.

## LIST-01 — Create and browse a list

A member can create a list from Slack's available entry points/templates,
define fields and views, and add records/items. The rendered table/board views
are keyboard and screen-reader usable, retain filtering/sorting/grouping, and
show the same records as the API. Empty list, no filtered results, loading, and
failure are distinct.

## LIST-02 — Manage list items and assignments

Eligible members can edit fields, assign people, set dates, comment, attach
files, and complete/archive items according to Slack. Validation and concurrent
updates preserve user input and use durable version/conflict semantics. Changes
produce current notification/Activity/workflow effects without duplicate
messages.

## WORKFLOW-01 — Discover and run a workflow

The Workflows directory, shared links, buttons, and configured triggers expose
only published workflows available to the member. A link trigger renders
Slack's Start Workflow affordance and works only from Slack. Webhook, schedule,
list-item update, message-posted keyword, channel-join, and emoji-reaction
triggers start only after their documented condition occurs.

Before execution, inputs use Slack-compatible types, defaults, options,
validation, variables from the trigger and prior steps, and accessible labels.
Submitting creates one durable run with actor, workspace, app, conversation,
trigger, and published workflow-version context. Repeated submission or
delivery with the same idempotency identity does not duplicate the run.
Started, waiting, completed, and failed states remain honest after reload and
process restart.

## WORKFLOW-02 — Execute steps and functions

Slack, connector, and custom app functions run in the order shown in the
builder with typed inputs/outputs, least-privilege token context, retries or
backoff only where the contract permits, and durable activity records.

Slack's built-in steps are not app functions and MUST not require one. Sending
a message to a conversation, adding people to a channel, and creating a canvas
each dispatch to no app and wait for no person, so the run performs them and
carries itself on. A built-in step that describes a desired end state MUST
succeed when that state already holds — adding someone already in the channel
is not a failure, or a workflow on a schedule fails forever on its second run.
Every built-in step acts as the member who started the run, so a workflow can
do nothing its owner could not and no change is attributed to nobody.

Waiting for a set time is different in kind: the run is suspended on the clock
rather than on a person or an app. The instant it becomes due MUST be durable,
because a remaining duration would start again from whenever the process did
and an hour's wait across a deployment would become two. A resumed run MUST
behave exactly like one a person resumed — the same conditions, branches and
variables — and a wait that has been spent MUST NOT be resumed a second time,
however many replicas sweep for it. A wait MUST be positive and bounded, so a
mistyped one is refused when it is written rather than parking a run past any
horizon an operator would think to check.

Waiting until a named date is the same suspension arrived at differently: the
instant is fixed rather than measured from when the run reached the step. It
MUST be resolved once, when the step is written, from the wall-clock time and
zone the author chose — resolving it per run would move the moment whenever
that zone's offset changed in between. A named instant already past MUST be
treated as due rather than as an error, because the wait it describes has
already been satisfied. A
workflow whose every step is built-in MUST therefore complete without anything
external moving it. The message MAY quote the run's inputs and earlier steps'
outputs with the same variable grammar the builder uses for input mapping; a
reference that resolves to nothing MUST be left visible rather than blanked, so
the author can see which reference was wrong. A step that cannot deliver fails
the run and records why, rather than failing the request that started it.
Variables carry earlier output without stringly typed reinterpretation.
Buttons can pause execution until an eligible person clicks, and branches
route only through the selected condition. A failed, cancelled, or waiting
step does not fabricate later success. Idempotency prevents duplicate external
effects after delivery retry, worker restart, or Socket Mode reconnect.

Function distribution, external authentication, hosted datastores, interactivity,
and event triggers use the same installed app identity and scope model described
by `APP-*`. A `function_executed` callback identifies a workflow execution with
Slack's `Wx` identifier, the function execution with `Fx`, the complete
function definition and inputs, and the scoped bot access token when Slack
issues one. Completion APIs transition only the currently executing function.

## WORKFLOW-03 — Create, publish, manage, and unpublish

Authorized members can open Tools or Automations, create or copy a workflow,
choose its trigger, search the step library, configure Slack, connector, and
custom steps, insert variables and buttons, edit/duplicate/reorder steps,
validate, and publish after reviewing details and permissions. They can set
the title, description, icon, workflow managers, who can find/use/copy it, and
whether connected external organizations may run it.

Workflow managers can inspect activity, update the workflow, change the
trigger where Slack permits, unpublish, republish, and delete. Owners and
admins can filter the workflow dashboard and join as a manager, unpublish in
bulk, export published workflows to CSV, and export form responses where their
plan permits it. Unpublishing makes the workflow unavailable but retains its
attributable run history.

Draft changes do not alter the published version until publish succeeds.
Existing run history remains attributable to the version executed.

Permissions, plan restrictions, removed functions, schema drift, revoked app
tokens, trigger conflicts, and concurrent publishing are explicit.

## WORKFLOW-04 — Route runs with conditional steps

A step can be gated by a condition comparing a variable — a run input
(`inputs.<name>`) or an earlier step's output (`steps.<id>.outputs.<name>`) —
with equals, does not equal, contains, is greater than, or is less than a
value. Execution skips a step whose condition fails and completes the run when
no remaining step's condition holds, so a workflow branches without a separate
editor. Conditions only read earlier steps, so a forward or self reference is a
definition error caught at save.

## WORKFLOW-05 — Collect input with interactive steps

A form step parks a run until a workspace member fills in and submits the
configured fields, and a button step parks it until a member clicks the
confirmation. The run view renders the pending form or button to any member,
and submitting or clicking completes the step through the same advance path as
a function completion, carrying the form's values forward as the step's outputs
for later steps and conditions to read.

## Evidence

- Who a canvas is shared with is now visible and changeable from the canvas
  itself. Anyone who may open it sees the list: they can already see who
  commented on it and who edited it, so who else may open it is not a further
  secret, and someone about to share a canvas needs to know it is not already
  shared. Changing the list belongs to the owner alone, which is the rule the
  grant already enforced — write access is not enough to hand a canvas to
  somebody else — so a reader is told that rather than shown a control that
  would be refused. The owner heads the list even when nothing is shared,
  because "shared with nobody" and "shared with everyone" must not render the
  same. A channel's own canvas tab appears as a grant and is not offered for
  removal: revoking it would leave the channel with a tab pointing at a document
  nobody in it may open. A canvas that is not this workspace's canvas answers
  the sharing read as missing rather than as unshared, on both storage profiles.
- A list item can be deleted, separately from being completed. Completing hides
  an item and can be undone; deleting cannot, so it is its own control saying so
  rather than a second meaning for the same button, and an item added by mistake
  no longer has to stay in the list forever with a line through it. Deleting a
  batch is all or nothing on every profile: a batch naming one item that is not
  there removes none of them, because a half-applied delete would leave a member
  unable to say what they had removed. An assignment already announced in
  Activity survives the item it pointed at and reports that its source has gone
  — the news that somebody gave you work is still true after the work is
  deleted, and a row that vanished would leave the member wondering what they
  had been told.
- A conversation's own canvas is reachable from the conversation. It is a
  different thing from a canvas shared into the channel: a conversation has
  exactly one, membership is what grants access to it, and leaving takes that
  access away without anybody revoking a grant. Opening the link when there is
  no canvas says so instead of creating one, because a canvas appearing because
  somebody followed a link would put an edit in the conversation's history that
  nobody made. A second creation is not the member's mistake — somebody else
  made it first — so it arrives at the canvas that exists rather than at an
  error. The sharing surface shows the conversation's grant and does not offer
  to revoke it: without it the conversation would have a canvas nobody in it
  could open.
- The sharing surface is one surface. A list carries the same grant model a
  canvas does, so it answers the same two questions the same way: anyone who may
  open it sees who else may, and only its owner changes that. Writing it twice
  would have been two chances to disagree about who may see a grant, which is
  the question the whole model exists to answer.
- A canvas section can be commented on. Read access is enough to add one:
  commenting is taking part in a document rather than editing it, and a canvas
  shared for review that only its editors could discuss would defeat the reason
  it was shared. Deleting a comment belongs to its author alone — an editor who
  could remove what others said about their own document would make the comments
  worth less than silence — and a stranger's attempt answers exactly as a
  missing comment does, so it cannot be used to learn that one exists. The
  anchor is a record of what was being discussed rather than a foreign key: a
  comment survives its section being rewritten or removed, which is usually why
  it went, and the client says the section has gone rather than pointing at
  nothing.
- A canvas keeps the revision each edit replaced, written in the same
  transaction as the edit so a canvas cannot change without the record of what
  it was. A revision records the state it *superseded* rather than the one that
  arrived, which is what lets the history be read backwards; it is numbered by
  the version the canvas held while that content was current, so restoring
  revision 3 puts revision 3's content back rather than making every reader
  subtract one. Restoring is an ordinary edit — the current content becomes a
  revision of its own, so restoring the wrong one is itself undoable and the
  version keeps counting forward. History is readable exactly when the canvas
  is, by the canvas's own visibility rule rather than a second one, and a
  deleted canvas takes its history with it. The count is bounded, and the
  oldest rows are dropped by the write that creates one rather than by a worker.
- A list declares typed columns — text, number, date, select, checkbox, person —
  and an item is checked against them. Each type is a promise about what a cell
  contains, so a value the column cannot mean is refused rather than stored for
  the next reader to discover. A cell naming an undeclared column is refused
  too, because it would be invisible to every reader of the list. A missing cell
  is not an error: a row with a blank status is an ordinary row, and demanding
  every column would make a list unusable exactly while it is being filled in.
  A list created without a schema is given one primary text column so the client
  has something to show; that substitution is the product's convenience rather
  than the member's declaration, so it is not enforced and such lists stay
  free-form. A column can be declared from the list page as well as through the
  API. Declaring one appends rather than rewriting the schema, because every
  cell references a column by key and a wholesale rewrite would let one edit
  orphan every value in the list; the key is minted from the name and made
  unique, so two columns called "Status" are survivable rather than a
  collision. Declaring a column on a list already used free-form does not
  strand its items: a cell the item already held is accepted, because the member
  did not introduce an invisible cell but merely did not delete one, while a
  newly invented column is still refused. Removing a column is not offered — it
  would have to remove that value from every item, which is a deletion worth
  asking for deliberately — and is recorded as absent.
- A list item carries an assignee and a due date as columns of their own rather
  than cells inside the free-form fields, because the product asks both
  questions itself — who is this for, is it late — and a value buried in JSON
  cannot answer them without every reader parsing it. The assignee is told in
  Activity by the same machinery a canvas share uses, and only when it is news:
  a due date moved on work someone already holds is not an assignment, and
  nobody is told about their own. Assigning to a member who cannot open the list
  is refused, so the picker offers only members who can — a control that always
  fails is worse than no control. An archived item is never overdue.


- The first-party HTTP journey creates a durable canvas, reloads it, atomically
  changes its title and body, creates a durable to-do list and item, completes
  and restores that item, and then reopens both directory views. It does not
  assert decorative cards.
- Current official Node and Python Slack SDK clients exercise workflow function
  completion, function distribution permissions, trigger permissions, featured
  workflows, and workflow-step listing through the real HTTP boundary.
- Shared event-contract and HTTP delivery tests prove that an app-owned
  `function_executed` callback is translated and dispatched through Events API
  without a manifest event subscription, while remaining isolated from every
  other app and from RTM.
- Shared memory, SQLite, PostgreSQL, and dqlite persistence qualification covers
  workflow revisions, triggers, permissions, featured workflows, execution
  idempotency, step advancement, terminal state, staged-change discard, and
  run/step cancellation on unpublish. SQLite additionally closes
  and reopens the database before asserting the workflow and run.
- The first-party Playwright journey creates and installs a remote-function
  app, creates a two-step draft, publishes it, creates a link trigger, starts
  one durable execution, reloads the run, stages and discards edits, unpublishes
  and observes the cancelled run, creates a webhook trigger, invokes
  its secret URL over real HTTP, observes the indistinguishable 404 for a wrong
  secret, and checks accessibility.
- Deterministic service, scheduler, memory, SQLite, PostgreSQL, and dqlite
  evidence fires scheduled triggers from a durable compare-and-set queue and
  message, reaction, join, and list triggers from a durable event cursor;
  wall-clock recurrence is qualified by injected poll instants rather than by
  sleeping in CI.
- Differential fixtures compare live Slack object schemas, supported controls,
  and run-state transitions.

## Current SameOldChat boundary

CANVAS-01 and the basic persistence portion of CANVAS-02 now have a real
workspace surface and access-filtered memory/SQL/gRPC reads. Owner edits of a
single Markdown section commit title and body as one compare-and-swap revision.
Structured canvases render read-only rather than being silently flattened.
Comments, revision history, and sharing review are now built: the sharing
surface names everyone who may open a canvas, and only its owner is offered the
controls that change that. A conversation now reaches its own canvas from the
conversation itself, and creating one is a deliberate act rather than a side
effect of following the link. Collaborative cursors, autosave/offline recovery,
and full rich block editing remain gaps.

LIST-01 and the basic completion portion of LIST-02 now have a persisted
directory, to-do creation, item creation, and complete/restore flow. Typed columns,
assignments, due dates, and sharing review are now built — a list carries the
same grants a canvas does, so it reaches the same sharing surface. An item can be deleted for good, which
completing it deliberately does not do. Templates, views, filters, sorting,
comments, attachments, and full notification/workflow effects remain gaps.

WORKFLOW-01 through WORKFLOW-03 now have a real core slice: a developer-app
owner can create a durable draft from owned remote app functions, configure ordered
steps and a JSON input schema, publish or unpublish, stage edits over a live
published revision while runs keep pinning the published version, create and enable or
disable link, shortcut, scheduled, webhook, message, reaction, join, and list
triggers, start one idempotent durable run, and reopen
its exact state. Scheduled triggers fire from a durable next-occurrence queue
with hourly, daily, weekly, and monthly calendar recurrence evaluated in the
configured time zone; webhook triggers run on an unauthenticated POST to a
secret URL whose stored form is hash plus credential-key ciphertext; message,
reaction, join, and first-party list record triggers fire from a durable
per-workspace event cursor with exactly-once run idempotency per source event.
The local and generated gRPC seams expose the same workflow,
trigger, run, permission, featured-workflow, step-list, and completion
operations. App-owned function executions are automatically dispatched through
Events API or Socket Mode without a manifest event subscription, matching
Slack's no-scope delivery contract. Unpublishing cancels every running run and
executing step atomically in the disabling transaction, stamping both with the
`workflow_unpublished` error so late completions are rejected. Staged edits
can be discarded from the builder: the head reverts to the published revision,
the staged revision rows are pruned, and the next update publishes from the
realigned version. While staged edits exist, the builder labels each step
against the published revision positionally — added, changed, or removed — and
lists the steps that no longer appear in the head, so the owner sees exactly
what publishing would change. A workflow can be copied into a new draft and
deleted, with deletion cancelling every running execution in the same
transaction that removes the workflow and its derived records. The owner's
builder shows a run activity dashboard counting runs by status and listing the
newest first. Scheduled triggers accept named weekdays and an explicit
month-end day. A step can be gated by a condition over the run's inputs or an
earlier step's outputs; each step's inputs can be mapped from those same
variables; and form and button steps pause a run until a workspace member
submits the form or clicks the confirmation, resuming through the same advance
path as a function completion. Run views are workspace-shareable so a member
can open an interactive run; steps reorder in place; a workflow carries an icon
through its revisions and views; a published workflow's trigger is locked to
enable/disable; and the owner exports run history and submitted form fields as
CSV. The owner and workspace administrators can name managers who edit, publish,
and delete the workflow alongside the owner. A function_executed callback
carries the app's `bot_access_token`, sealed at OAuth exchange and opened only
at delivery.

This is not full Slack Workflow Builder parity. find/use/copy
permissions, plan/admin policy, Slack built-in and connector functions,
templates and AI creation, asynchronous CSV export at scale,
enforcement of typed workflow/function input and output schemas, multi-org
permissions, exact rate limits, and controlled live-Slack outcomes remain
verified gaps.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| CANVAS-01 | [Use a canvas in Slack](https://slack.com/help/articles/203950418-Use-a-canvas-in-Slack) | Slack creates canvases and adds an existing or new canvas as a conversation tab after access review. |
| CANVAS-02 | [Use a canvas in Slack](https://slack.com/help/articles/203950418-Use-a-canvas-in-Slack) | Canvas content supports collaborative editing, comments, and sharing controls. |
| LIST-01 | [Use lists in Slack](https://slack.com/help/articles/27452748828179-Use-lists-in-Slack) | Slack lists expose templates, fields, items, filters, and multiple views. |
| LIST-02 | [Use lists in Slack](https://slack.com/help/articles/27452748828179-Use-lists-in-Slack) | List items support assignment, dates, fields, discussion, and completion. |
| WORKFLOW-01 | [Build a workflow](https://slack.com/help/articles/17542172840595-Build-a-workflow--Create-a-workflow-in-Slack/) | Link, webhook, schedule, list, message, join, and reaction triggers start published workflows under their documented conditions. |
| WORKFLOW-02 | [function_executed](https://docs.slack.dev/reference/events/function_executed/) | Function callbacks carry `Wx`/`Fx` identities, function/input snapshots, and the applicable bot access token. |
| WORKFLOW-03 | [Build a workflow](https://slack.com/help/articles/17542172840595-Build-a-workflow--Create-a-workflow-in-Slack/) | The builder configures triggers, ordered steps, variables, buttons, managers, access, metadata, and publication. |
| WORKFLOW-04 | [Build a workflow](https://slack.com/help/articles/17542172840595-Build-a-workflow--Create-a-workflow-in-Slack/) | A step runs only when its condition over a run input or an earlier step's output holds. |
| WORKFLOW-05 | [Build a workflow](https://slack.com/help/articles/17542172840595-Build-a-workflow--Create-a-workflow-in-Slack/) | Form and button steps pause a run for a member's input and resume on submit or click. |

Sources checked 2026-07-31:

- [Use a canvas in Slack](https://slack.com/help/articles/203950418-Use-a-canvas-in-Slack)
- [Use lists in Slack](https://slack.com/help/articles/27452748828179-Use-lists-in-Slack)
- [Build a workflow: Create a workflow in Slack](https://slack.com/help/articles/17542172840595-Build-a-workflow--Create-a-workflow-in-Slack/)
- [Edit and manage your workflows](https://slack.com/help/articles/360054495774-Edit-and-manage-your-workflows)
- [Manage workflows in Slack](https://slack.com/help/articles/15363614064275-Manage-workflows-in-Slack)
- [function_executed event](https://docs.slack.dev/reference/events/function_executed/)
- [functions.workflows.steps.list](https://docs.slack.dev/reference/methods/functions.workflows.steps.list/)
- [functions.workflows.steps.responses.export](https://docs.slack.dev/reference/methods/functions.workflows.steps.responses.export/)
- [Slack platform automation](https://docs.slack.dev/automation/)
