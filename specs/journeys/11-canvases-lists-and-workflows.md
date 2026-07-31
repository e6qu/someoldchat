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

## Evidence

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
  idempotency, step advancement, and terminal state. SQLite additionally closes
  and reopens the database before asserting the workflow and run.
- The first-party Playwright journey creates and installs a remote-function
  app, creates a two-step draft, publishes it, creates a link trigger, starts
  one durable execution, reloads the run, creates a webhook trigger, invokes
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
Structured canvases render read-only rather than being silently flattened;
collaborative cursors, comments, history, autosave/offline recovery, full rich
block editing, tab attachment, and sharing review remain gaps.

LIST-01 and the basic completion portion of LIST-02 now have a persisted
directory, to-do creation, item creation, and complete/restore flow. Custom
schemas, templates, views, filters, sorting, assignments, dates, comments,
attachments, item deletion, and full
notification/workflow effects remain gaps.

WORKFLOW-01 through WORKFLOW-03 now have a real core slice: a developer-app
owner can create a durable draft from owned remote app functions, configure ordered
steps and a JSON input schema, publish or unpublish, create and enable or
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
Slack's no-scope delivery contract.

This is not full Slack Workflow Builder parity. Workflow managers, find/use/copy
permissions, plan/admin policy, Slack built-in and connector functions, typed
variable mapping, form and button steps, branches, templates and AI creation,
icons, copy/delete, drag reordering, trigger-change rules, schedule frequency
variants beyond hourly/daily/weekly/monthly (named weekdays, month-end
semantics), trigger inputs wired to step variables, activity dashboards, async
workflow and form-response CSV export, staged edits that leave an older published
revision live, cancellation of already-running executions on unpublish,
enforcement of typed workflow/function input and output schemas, multi-org
permissions, exact rate limits, and controlled live-Slack outcomes remain
verified gaps. Current callback snapshots
also omit `function_executed.bot_access_token`: installation secrets are stored
only as hashes, so the product must introduce a secure retrievable execution
credential design rather than fabricate the field.

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
