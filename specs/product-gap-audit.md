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
| P1 | Saved and scheduled work | Deterministic deployed-worker browser delivery and live-Slack differential outcomes. Natural-language reminder parsing now also accepts spoken quantities and weeks (`in an hour`, `in a week`, `in 2 weeks`), a one-off named weekday (`on Friday`), and a named calendar date (`on July 4`, `on Dec 25`, rolling to next year once the date has passed); phrasings still unhandled include relative months and `next <weekday>`, whose week semantics are ambiguous. Month-end and leap-day recurrence is closed: a recurring reminder carries an immutable anchor and clamps a 31st or Feb-29th occurrence to each period's last day without drifting. The five deprecated app-facing `reminders.*` methods stay SDK-compatible and are not evidence for the first-party Later model. |
| P1 | Direct-message lifecycle | Slack does not publish its complete history-option list, so that inventory, Slack Connect conversion policy, and workspace-configurable restrictions stay differential gaps rather than inferred compatibility. |
| P1 | Notifications and presence | Automatic presence is activity-derived and follows Slack's ten-minute idle transition: an `/app/active` heartbeat advances the member's last-active watermark and both the web and Web API presence reads resolve `auto` to `away` once ten minutes elapse (`domain.Presence.CurrentAt`, `PresenceIdleAfter`) — the row previously listed this as a gap after it had shipped. Status projection now reaches the message timeline: a human author's current status emoji renders beside their name on every message, resolved to a glyph rather than the stored shortcode, with the status prose as its tooltip; an app message or one wearing a custom username carries none, because it is not a person. The People directory and profile card, which stored a shortcode and rendered it raw so members saw the colons, now resolve it through the same emoji renderer. What remains of projection is the conversation-details member panel, the DM header, and search results, none of which yet shows a member's status. Custom-emoji name validation is enforced — charset, length, existing-name collision, and now collision with a built-in emoji's name or alias, refused on add, alias, and rename. Image-byte validation (Slack's 128 KB square-image check) stays out of reach because this deployment stores an image URL rather than uploading bytes. Deeper Activity/VIP/invitation policy remains. |
| P2 | Calls and huddles | Huddle media is peer to peer with no media server, so a huddle is only as large as every participant can upload to every other. Reactions, captions and the huddle canvas are unimplemented. |
| P1 | App administration | Install-time incoming-webhook selection, scope explanation, distribution, and external-auth providers. Retained delivery-attempt history and metrics are closed: every acknowledgement and release records a bounded, newest-first outcome history per (app, surface), and the delivery-health surface reports the delivered/failed counts and success rate over the retained window — honest that it is a window, not an all-time ledger. App-level token *revocation* is closed: the app owner can revoke every token their app issued, and Lookup — already the check every authenticated request runs — refuses a revoked one. Granular token *inventory* (listing individual tokens with issue time and per-token revoke) still needs a public, non-secret token id the schema does not yet carry. |
| P2 | Canvases and lists | List views, comments and files. Canvases and lists are both searchable now — a Lists tab matches a list's name and description prose under the same folded index, visibility rule, and query grammar as the Canvases tab. |
| P1 | Media transport | Huddles now carry WebRTC audio, video and screen sharing, peer to peer. This row used to say they had none and that a peer-to-peer shortcut "would be a deviation dressed as a simplification"; that judgement was overturned when the media shipped, and the row outlived it. What remains is the ceiling the mesh imposes — every participant uploads to every other — which the Calls and huddles row above records. CALL-01 is not part of this: Slack supplies no media for third-party `calls.*` either, so rendering the call block is the whole journey and it exists. |
| P1 | Channel posting policy is stored but not enforced | `admin.conversations.setConversationPrefs` records `who_can_post` and `can_thread`, `getConversationPrefs` reports them, and nothing reads them again: an administrator restricting a channel to admins changes nothing, and any member still posts. The vocabulary is also unvalidated — `{"type":["adminn"]}` is accepted and stored. Both were reproduced. Enforcing it needs a vocabulary, and the pinned reference does not supply one: it types `type` as an unconstrained array of string. Guessing it would deny legitimate posts whenever the guess is wrong, which is a worse failure than the present permissiveness, so the vocabulary is a product decision rather than a coding task. |
| P2 | ~~A workflow step's stored payload is unreadable~~ — closed | `workflows.updateStep`, `workflows.stepCompleted` and `workflows.stepFailed` write a step's inputs, outputs and failure, and no method reports them back: `domain.WorkflowStep` appears nowhere on the chat seam, and neither the run, its interaction nor its activity carries the values. Closed: `WorkflowRunSteps` crosses the seam under the same workspace-membership authority a run itself is read under — a step belongs to its run, and Slack's run view is shareable across the workspace. An app that completes a step with outputs now reads back exactly what it stored, proven by a service test; the parity case compares the step's inputs and status as values across both compositions, not merely that the call was accepted, and refuses a stranger. |
| P2 | ~~Attaching a file sits beside the composer rather than inside it~~ — closed | Slack's plus button lives in the composer's own bottom row. Here the control carries its own multipart form, and HTML forbids a form inside a form, so it renders as a disclosure on the composer's edge instead. Closed, and the recorded reason was wrong. The obstacle was described as the multipart form, but the form was already never submitted natively while script is on: stageSelectedFiles builds FormData from it and fetches. So nothing about how an upload is posted had to change. The form stays as a hidden vehicle for the file input, and the control that opens it is now a plus button at the head of the composer's own toolbar, where Slack puts it. Two things went with the disclosure: the "Title (optional)" box, which Slack does not have and which only overrode a single file's title where the server already uses the filename, and the "No files selected" placeholder, whose paste-and-drop hint moved into the composer's hint line. |
| P2 | A session policy's mobile device check has nothing to check | `admin.users.session.setSettings` stores `mobile_device_check` and `getSettings` reports it. The duration and `desktop_app_browser_quit` beside it are now applied at both session-minting paths; this one is not, because there is no mobile client in this deployment for a device check to run on. It is named rather than counted as enforced, and it closes when a mobile client exists rather than before. |
| P2 | ~~External invite permission is an event and nothing else~~ — closed | `SetExternalInvitePermissions` writes `conversation.external_invite_permissions_set` carrying `can_invite` and changes no queryable state, so nothing could read back whether an organization may invite others and nothing enforced it. Closed: SetExternalInvitePermissions writes durable per-(conversation, team) state instead of re-writing the team set; ExternalInvitePermission reads it back across the seam with a parity case comparing the value; and InviteShared enforces it — a connected organization a host has restricted is refused with a classified permission error when it tries to invite another. Reaching that enforcement needed InviteShared to admit a connected organization at all, which the host-scoped membership check refused, so a shared channel's cross-workspace membership is verified directly for that path; the invitation record stays the host's, where it is stored and approved. can_invite is a boolean, so unlike channel-posting and auth policy this needed no vocabulary. |
| P2 | Workspace authentication policy is assigned but not applied | `admin.auth.policy.assignEntities` records entities against a policy and `ListAuthPolicyEntities` reports them; no sign-in path consults either. The policy vocabulary has the same problem as channel posting policy — the pinned reference does not define it — and here a wrong guess denies sign-in rather than posting, so it is recorded rather than attempted. |

| P1 | A mutation's refresh can be discarded, leaving the timeline permanently stale | A forced refresh's correct response is discarded at the apply step and nothing retries, leaving a region stale. Seen three times on CI, never reproduced locally in six repeats across three engines. Two hypotheses have been refuted by evidence rather than abandoned. A forced refresh is now protected from being superseded by a background one; whether that is the cause is unproven, and CI traces are kept so the next occurrence adds evidence. |
| P1 | A mutation's control could stay disabled for ever when its view refresh never settled | Distinct from the row above, and found by reading a CI failure rather than by inference. `[CONV-02 NAV-04]` failed asserting a duplicate channel name reports "already exists"; the network trace held **one** `POST /app/conversation/create` where the test makes two, so the second click issued no request at all — no error, no feedback, nothing. `data-discarded-refreshes` was `0`, which refutes the refresh-discard row above as the cause. The submit handler disabled the control, and on a 204 returned `refresh(true)` and re-enabled only after the whole chain settled, so a refresh that never settled left the control dead. Re-enabling now happens when the response has been handled, because by then the server has done the work. Whether a hanging refresh is itself the underlying fault is still unproven; what is proven is that the UI could no longer act when one occurred. |
| P1 | ~~A read-then-write transaction can be refused SQLITE_BUSY that no timeout absorbs~~ — closed | SQLite's `busy_timeout` waits out a writer that holds the lock, but a deferred transaction that takes a read snapshot and then asks for the write lock is refused **immediately** — waiting could only deadlock, since the writer it would wait for needs the snapshot this transaction holds. The remedies are `BEGIN IMMEDIATE` or retrying the whole transaction, and `underContention` is the second. Only 14 of the store's ~200 transaction sites use it. `SetThreadFollowed` is fixed because a CI failure named it: following 250 thread roots surfaced `database is locked (5) (SQLITE_BUSY)` under the race detector, which widens the window rather than creating it. Every writer with the same read-then-write shape has the same exposure. Closed by beginning every transaction IMMEDIATE (`_txlock=immediate`), which takes the write lock before the snapshot so `busy_timeout` can wait instead of the upgrade being refused outright. The first attempt retried the whole transaction instead, and that was worse than the disease: it converted an immediate failure into a spin of up to the full busy timeout, taking `internal/store/sqlstore` from 24 seconds to 508 on CI while passing locally, where there is no contention to trigger it. IMMEDIATE serialises writers — this package costs about twice as long locally — which is the price of the guarantee. |
| P2 | ~~A workflow run can never be queued, and the activity view counts them anyway~~ — closed | `domain.WorkflowRunQueued` is declared, and `runWorkflow` creates every run already `running`. The only other mentions in the tree are the memory and SQL stores counting queued runs into the Workflow Activity summary, so that view reports a "Queued" figure that is structurally always zero. Either runs should be queued before they are picked up — which is what the state is for, and what a worker-backed execution model would need — or the state and its counter should go. Closed by removing the state, on the evidence that this deployment runs workflows synchronously: every trigger, interactive and automatic, funnels through `runWorkflow`, which creates the run already running, and there is no worker that would pick a queued run up. The `WorkflowRunQueued` constant, the `Queued` field on the activity summary, both stores' counters, the web display, and the proto field (reserved, not reused) are gone together. Queuing would be a distributed-execution change this deployment does not have and was not asked for; a state nothing produces, shown to an administrator as a number, is a false affordance. |
| P2 | Browser-citation ceiling | Nine journeys have no browser citation, each for a structural reason recorded in its own journey: two are observable only at an app endpoint the harness does not host, one needs a worker sharing a durable store, one restricts itself to SDK evidence, and five need an identity provider or media. `browserGapCeiling` holds the count so it cannot grow silently. |
| P2 | WebKit crashes navigating away from the workspace page, twice now | `[THREAD-01 THREAD-02]` showed a thread's root where its reply belonged, and `[NAV-05]` failed with "WebKit encountered an internal error". One event, not two: the trace shows four consecutive requests — the reply, a draft, a typing signal and an activity ping — completing with status `-1`, which is a connection that never finished, and the page frozen at the state before them. `data-discarded-refreshes` was `0`, so the refresh-discard row above is refuted as the cause for a second time. 233 of 236 passed on that run and the whole suite passes locally. A second occurrence followed on the next run, and it says something: both were `[NAV-05]`, both at `page.goto` of a permalink, and the second one's own request completed with `-1` as well — so the navigation never left the starting page. The crash is at navigation time rather than in the page being opened, which points at tearing down `/app` — the heaviest page in the suite, carrying WebRTC, media and notification code — rather than at anything the permalink route serves. That is a hypothesis and is written as one: no server response was involved either time, Chromium and Firefox navigate the same route without complaint, and 234 of 236 pass on the run. A focused probe now runs exactly that — sixty times through a real permalink, resolved to the deep-linked `/app?before=…&channel=Cdev#message-…` the failing navigation used, each confirmed to render a real timeline (status 200, 60/60), then away to a static page — and local WebKit does not crash. That is a negative result and is reported as one: it does not clear CI WebKit, which is a different build on a different OS under the accumulated state of 235 prior tests, but it removes the simplest explanation. The teardown of `/app` alone, in isolation, is not enough to reproduce it here, so the cause needs the CI environment or the load or both. The probe lives in `tests/browser/probes`, outside the gated suite, and is run by hand against a WebKit fixture; it stays as the reproduction to reach for when the crash recurs. |
| P2 | `[REMIND-01]` failed once on WebKit and has not repeated | Clicking "In 20 minutes" did not navigate within 30 s. One CI occurrence; passed on re-run and passes locally. Recorded rather than dismissed, because a single unexplained navigation stall is what two later-reproducible races looked like at first. |
| P2 | ~~The channel header shows no member count~~ — closed | Slack's header carries the member count beside the topic, and clicking it opens the member list. This header shows the title, the topic and whether you have joined, with secondary actions behind one overflow control, but no count. Closed: `ConversationMemberCount` crosses the seam with the conversation-membership guard, the stores count exactly what the member list lists (the parity case asserts the two agree), and the header renders it as a link into the member list. A DM shows none, because its header is the person. |
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

Authorization is now qualified from two directions rather than one. The matrix
in `tests/authorization` drives every operation as seven tiers, and the
guard-mutation gate in `tests/mutation` deletes every guard in front of an
operation and requires a suite to notice. The second exists because the first
cannot check itself: it reported ninety operations as authorization-tested that
stayed green with no authorization at all. Two ceilings record what is left —
`refusalDoesNotDistinguishTheHolder` for operations whose refusal cannot be told
apart from a missing object, and `survivingGuardCeiling` for operations nothing
notices the loss of. Both only shrink, and one fixture object per operation is
what shrinks them.

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
