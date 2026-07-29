# Canvases, lists, and workflows

## CANVAS-01 — Create and open a canvas

A member can create a standalone canvas or open a conversation canvas where
Slack and workspace policy allow. The surface identifies title, owner/location,
sharing state, last update, and edit/view permission. Creation commits one
durable canvas and opens the real editable/view-only object; a message or
channel canvas link resolves to the same object.

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

The shortcuts browser, links, buttons, and configured triggers expose only
published workflows available to the member. Before execution, inputs use
Slack-compatible types, defaults, options, validation, and accessible labels.
Submitting creates one durable run with actor/workspace/channel context and an
honest started/completed/failed state.

## WORKFLOW-02 — Execute steps and functions

Built-in and app functions run in declared order with typed inputs/outputs,
least-privilege token context, retries/backoff only where the contract permits,
and durable activity records. A failed or cancelled step does not fabricate
later success. Idempotency prevents duplicate external effects after delivery
retry, worker restart, or Socket Mode reconnect.

Function distribution, external authentication, hosted datastores, interactivity,
and event triggers use the same installed app identity and scope model described
by `APP-*`.

## WORKFLOW-03 — Create, publish, manage, and unpublish

Authorized members can create/edit a workflow, choose a trigger, configure
steps, validate, test with synthetic/declared data, publish, inspect activity,
update, disable, and unpublish. Draft changes do not alter the published
version until publish succeeds. Existing run history remains attributable to
the version executed.

Permissions, plan restrictions, removed functions, schema drift, revoked app
tokens, trigger conflicts, and concurrent publishing are explicit.

## Evidence

- Browser tests edit real structured objects and verify their API representation
  rather than asserting decorative cards.
- Current official SDK/CLI clients exercise canvases, lists, functions,
  workflows, triggers, hosted datastores, external auth, and activity methods
  they expose.
- Persistence tests cover versioning, execution idempotency, retries, restart,
  cancellation, and app/token isolation.
- Differential fixtures compare live Slack object schemas, supported controls,
  and run-state transitions.

Sources checked 2026-07-29:

- [Use a canvas in Slack](https://slack.com/help/articles/203950418-Use-a-canvas-in-Slack)
- [Use lists in Slack](https://slack.com/help/articles/27452748828179-Use-lists-in-Slack)
- [Automations: Workflows in Slack](https://slack.com/help/articles/360035692513-Guide-to-Workflow-Builder)
- [Slack platform automation](https://docs.slack.dev/automation/)
