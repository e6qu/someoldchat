# Product gap audit

The evidence-backed boundary between SameOldChat's current product and the
Slack behavior it targets. A registered handler or a rendered control does not
count as complete unless the surrounding journey is usable.

Counts are derived, not written down here. `make compatibility-report` is the
authority on method and event coverage, `make journey-check` on journey and
browser-citation coverage, and `make sdk-qualification` on what the pinned
official SDKs actually exercise. Hand-maintained copies of those numbers went
stale silently twice, so this document no longer keeps any.

The normative [Slack user-journey catalog](journeys/README.md) records the full
target separately. A journey omitted from the working list below is still a gap
if the catalog has it; implementation MUST NOT narrow the target.

## First-party UI journeys

### Working end to end

Derived, not listed here: `make journey-check` reports which of the catalog's
journeys the browser suite cites, and the journey documents record what each
one covers. A list in this file would go stale the way the counts above did.

### Core Slack journeys still absent or incomplete

| Priority | Journey | Concrete gap |
| --- | --- | --- |
| P0 | Search depth | Semantic relevance ranking, the remaining modifier variants, visual baselines, and live-Slack differential outcomes. |
| P0 | Activity depth | VIP and section notifications, custom views, and pre-v107 history backfill. |
| P0 | Composer depth | Slack video source/device controls, thumbnails, transcripts, exact emoji ranking, and attachment reordering. Slack allows 1 GB per file; this deployment caps a request at 100 MiB. |
| P1 | Saved and scheduled work | Broader natural-language reminder parsing, month-end recurrence, deterministic deployed-worker browser delivery, and live-Slack differential outcomes. The five deprecated app-facing `reminders.*` methods stay SDK-compatible and are not evidence for the first-party Later model. |
| P1 | Direct-message lifecycle | Slack does not publish its complete history-option list, so that inventory, Slack Connect conversion policy, and workspace-configurable restrictions stay differential gaps rather than inferred compatibility. |
| P1 | Notifications and presence | Activity-derived automatic presence and Slack's ten-minute idle transition, status projection outside People and profile, workspace emoji validation, and deeper Activity/VIP/invitation policy. |
| P2 | Calls and huddles | Huddle media is peer to peer with no media server, so a huddle is only as large as every participant can upload to every other. Reactions, captions and the huddle canvas are unimplemented. |
| P1 | App administration | Install-time incoming-webhook selection, retained delivery-attempt history and metrics, scope explanation, token inventory and revocation, distribution, and external-auth providers. |
| P2 | Canvases and lists | List views, comments and files. Canvases are searchable; lists are not. |
| P1 | Media transport | Huddles now carry WebRTC audio, video and screen sharing, peer to peer. This row used to say they had none and that a peer-to-peer shortcut "would be a deviation dressed as a simplification"; that judgement was overturned when the media shipped, and the row outlived it. What remains is the ceiling the mesh imposes — every participant uploads to every other — which the Calls and huddles row above records. CALL-01 is not part of this: Slack supplies no media for third-party `calls.*` either, so rendering the call block is the whole journey and it exists. |
| P1 | Channel posting policy is stored but not enforced | `admin.conversations.setConversationPrefs` records `who_can_post` and `can_thread`, `getConversationPrefs` reports them, and nothing reads them again: an administrator restricting a channel to admins changes nothing, and any member still posts. The vocabulary is also unvalidated — `{"type":["adminn"]}` is accepted and stored. Both were reproduced. Enforcing it needs a vocabulary, and the pinned reference does not supply one: it types `type` as an unconstrained array of string. Guessing it would deny legitimate posts whenever the guess is wrong, which is a worse failure than the present permissiveness, so the vocabulary is a product decision rather than a coding task. |
| P2 | A workflow step's stored payload is unreadable | `workflows.updateStep`, `workflows.stepCompleted` and `workflows.stepFailed` write a step's inputs, outputs and failure, and no method reports them back: `domain.WorkflowStep` appears nowhere on the chat seam, and neither the run, its interaction nor its activity carries the values. An app that completes a step with outputs cannot read what it stored, and a differential case can compare only whether the call was accepted. Found while giving the three methods seam coverage. |
| P2 | Attaching a file sits beside the composer rather than inside it | Slack's plus button lives in the composer's own bottom row. Here the control carries its own multipart form, and HTML forbids a form inside a form, so it renders as a disclosure on the composer's edge instead. Closing it means either driving the upload from script through a hidden input, or folding the upload into the composer's own submit — a decision about how uploads are posted rather than a styling change. |
| P1 | A workspace's session duration is never applied | `admin.users.session.setSettings` stores a duration per member and `admin.users.session.getSettings` reports it; `internal/web/identity.go` mints every browser session at a hardcoded 24 hours, capped only by the identity provider's own expiry. An administrator who sets an hour gets a day. Unlike channel posting policy and workspace authentication policy this needs no vocabulary and is implementable: it wants a seam method for a member's own session settings, read by the sign-in path before it chooses the expiry. Found by the policy-enforcement gate in `tests/policy`, which is the first thing that gate reported. |
| P2 | External invite permission is an event and nothing else | `SetExternalInvitePermissions` writes `conversation.external_invite_permissions_set` carrying `can_invite` and changes no queryable state, so nothing can read back whether an organization may invite others and nothing enforces it. Found while giving the method seam coverage, which now projects the event because there is nothing else to project. |
| P2 | Workspace authentication policy is assigned but not applied | `admin.auth.policy.assignEntities` records entities against a policy and `ListAuthPolicyEntities` reports them; no sign-in path consults either. The policy vocabulary has the same problem as channel posting policy — the pinned reference does not define it — and here a wrong guess denies sign-in rather than posting, so it is recorded rather than attempted. |

| P1 | A mutation's refresh can be discarded, leaving the timeline permanently stale | A forced refresh's correct response is discarded at the apply step and nothing retries, leaving a region stale. Seen three times on CI, never reproduced locally in six repeats across three engines. Two hypotheses have been refuted by evidence rather than abandoned. A forced refresh is now protected from being superseded by a background one; whether that is the cause is unproven, and CI traces are kept so the next occurrence adds evidence. |
| P2 | Browser-citation ceiling | Nine journeys have no browser citation, each for a structural reason recorded in its own journey: two are observable only at an app endpoint the harness does not host, one needs a worker sharing a durable store, one restricts itself to SDK evidence, and five need an identity provider or media. `browserGapCeiling` holds the count so it cannot grow silently. |
| P2 | `[REMIND-01]` failed once on WebKit and has not repeated | Clicking "In 20 minutes" did not navigate within 30 s. One CI occurrence; passed on re-run and passes locally. Recorded rather than dismissed, because a single unexplained navigation stall is what two later-reproducible races looked like at first. |
| P2 | Client breadth | Performance budgets, screenshot differential, manual assistive-technology coverage, and live-Slack comparison. |

## Web API and app-platform gaps

`make compatibility-report` lists the unimplemented current Web API methods and
their namespaces. A table here said 74 across seven namespaces for two releases
after nine of them were implemented, so it is gone rather than corrected.

App-platform work must remain dependency-ordered:

1. finish retained authorization and attempt history on top of the bot/user
   HTTP/Socket subscription, immutable message create/change/delete, and file
   create/share projections now in place. `file_unshared` production is done:
   deleting the last live message carrying a file retracts the file's share
   into that conversation and journals the event, in one transaction;
2. implement workflow execution and durable activity records;
3. implement external OAuth provider configuration, browser consent, encrypted
   refresh/access tokens, and `apps.auth.external.*`/connection callbacks as one
   journey;
4. extend the implemented datastore query/count expression evaluator and
   documented scan semantics only when a current Slack contract or controlled
   differential establishes additional operators;
5. add app icon storage/rendering and delivery-health/token administration;
6. then expand assistant, Slack Connect, and Enterprise administration.

## Qualification gaps

Official SDK qualification proves, method by method, that a genuine client
issued a request and parsed the response. It does not prove live Slack
equivalence.

Ledger `evidence:` entries are typed and resolved by `contractcheck`: a Go test
or cross-profile contract that exists, a journey ID the browser suite cites, a
file inside the tree its kind names, or an official Slack URL. The gate closes
the mechanical half — deletions, renames, moves, mislabelled kinds. It does not
claim a target *proves* its method; no checker can read a test and decide that,
so that judgement stays with the reviewer. Implementation files are admissible
only in a downgrade audit, where "here is the code that shows the claim was
overstated" is a real citation; as operation evidence a method would prove
itself with the thing being judged.

Claims at `sdk-compatible` or above that carry no method-level evidence must
not inherit it from the aggregate green suite. The count is in the report.

The remaining layers:

1. pin per-method argument, response and error schemas, not only the method
   index;
2. run response fixtures through the Node, Python and Java SDK types where they
   expose the method;
3. add manual screen-reader, zoom, reduced-motion and keyboard-only
   qualification beside the automated three-engine accessibility gate;
4. maintain visual baselines for desktop and narrow layouts;
5. run opt-in differential tests against a live Slack sandbox and promote only
   observed methods to `verified-against-slack`.
