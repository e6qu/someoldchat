# Product gap audit

The maintained inventory of known gaps between SameOldChat and the Slack
behavior it targets. A registered handler or a rendered control does not count
as complete unless the surrounding journey is usable. When a gap closes, delete
its row; the change history lives in version control.

Coverage figures come from the gates, not from this file:
`make compatibility-report` for method and event coverage, `make journey-check`
for journey and browser-citation coverage, and `make sdk-qualification` for
what the pinned official SDKs exercise; the
[project status](../PLAN.md#status) records their current figures.

The normative [Slack user-journey catalog](journeys/README.md) records the full
target. A journey missing from this list is still a gap if the catalog has it;
implementation MUST NOT narrow the target.

## First-party UI journeys

| Priority | Journey | Concrete gap |
| --- | --- | --- |
| P0 | Search depth | **Semantic relevance ranking** needs an embedding model this deployment does not host; results are lexically scored and deterministically ordered instead. **Visual baselines** and **live-Slack differential outcomes** need the Slack sandbox. |
| P0 | Activity depth | Activity items are not backfilled for history written before schema v107; this is an accepted data-migration limitation (a fresh store has nothing to backfill). |
| P0 | Composer depth | Slack's video source/device controls and transcripts are absent. Slack allows 1 GB per file; this deployment caps a request at 100 MiB. |
| P1 | Workspace shell | Privacy & visibility offers Slack Connect discoverability and hidden people but no blocked-invitation list, and Language & region sets the time zone and the language, but English is the only catalog: the localization system (docs/localization.md) is in place and the Preferences dialog's tabs and Language & region panel are on it, while the rest of the web client's text is still written in its markup and moves onto the catalog surface by surface; Accessibility offers zoom, link underlining, reduced motion, and announcing incoming messages but not Slack's screen-reader message-verbosity options. Zoom scales the page with CSS `zoom`, so media queries still see the window's own width: at 150% on a narrow window the desktop layout is kept until the window itself is narrow, where browser zoom would reflow sooner. `Control+N` is reserved by browsers, so New message is reached from the compose button and the Create menu. In Playwright's WebKit a drag that starts in a custom section never fires `dragstart` (unconfirmed in Safari itself). |
| P1 | Composer | Slack's broadcast-confirmation threshold is unpublished, so "more than six members" needs a live differential. `/giphy` needs a third-party GIF service this deployment does not host. |
| P1 | Message surface | A Threads card's reply form sends in place but is plain text: mention suggestions, formatting, saved drafts and attachments need the full thread composer, which opening the thread provides, because each card would need its own mention directory, draft, and typing region. |
| P1 | Secondary views | The canvas is not one contiguous rich-text document: each block is its own contenteditable, and inline mentions in a canvas stay plain text. |
| P1 | Saved and scheduled work | Deterministic deployed-worker browser delivery and live-Slack differential outcomes. The reminder phrase `next <weekday>` is deliberately unhandled because its week semantics are ambiguous. The five deprecated app-facing `reminders.*` methods are not evidence for the first-party Later model. |
| P1 | Direct-message lifecycle | Slack does not publish its complete history-option list, so that inventory, Slack Connect conversion policy, and workspace-configurable restrictions stay differential gaps rather than inferred compatibility. |
| P1 | Notifications and presence | Deeper Activity and invitation policy remains. An uploaded custom emoji is held to Slack's 128 KB and image-type rules but is not resized; an emoji added by image URL is stored as given, the contract `admin.emoji.add` has. |
| P2 | Calls and huddles | Captions need speech-to-text this deployment does not host. |
| P2 | Session policy mobile device check | `admin.users.session.setSettings` stores `mobile_device_check` and `getSettings` reports it, but there is no mobile client for a device check to run on. It is named rather than counted as enforced, and closes when a mobile client exists. |
| P2 | `email_password` auth policy in an SSO-only workspace | The policy is enforced at the external identity-provider callback, the only sign-in this deployment completes: a bound member is refused SSO with a 403 and no session. There is no email-and-password sign-in subsystem, so such a member has no in-product alternative; an administrator should not assign the policy in an SSO-only workspace. |
| P1 | A mutation's refresh can be discarded, leaving the timeline stale | A forced refresh's correct response is discarded at the apply step and nothing retries, leaving a region stale. Seen three times on CI, never reproduced locally. Two hypotheses have been refuted. A forced refresh is now protected from being superseded by a background one; whether that is the cause is unproven, and CI traces are kept for the next occurrence. Separately, a control that waited on a refresh that never settled could stay disabled (a `[CONV-02 NAV-04]` CI failure with `data-discarded-refreshes` at `0`); controls now re-enable once the response is handled, but whether a hanging refresh is itself the underlying fault is unproven. |
| P2 | WebKit crashes navigating away from `/app` | Twice on CI, `[NAV-05]` failed at `page.goto` of a permalink with "WebKit encountered an internal error"; the navigation's own request completed with status `-1`, so the crash is at teardown of `/app` (the heaviest page, carrying WebRTC, media, and notification code) rather than in the page being opened. That is a hypothesis: Chromium and Firefox navigate the same route cleanly, and a local probe (`tests/browser/probes`, run by hand, 60/60) does not reproduce it, so the cause needs the CI WebKit build, the suite's accumulated load, or both. |
| P2 | Huddle media forwarding can time out on CI | Once on CI (`make test-huddle-media`, PR 307), `TestRoomForwardsCameraAndScreenAsDistinctStreams` waited its full 120 s with the room holding only the receiver (`peers=1`): the publisher had joined and been removed, which the SFU does when a peer's connection fails, so the publisher's ICE never completed on the runner. The re-run passed and it does not reproduce locally. The cause, a runner network limit or a handshake race in `internal/huddlesfu`, is unproven; the room's `debugState` output is what to keep from the next occurrence. |
| P2 | Browser-citation ceiling | Eight journeys have no browser citation (APP-06, AUTH-02, AUTH-05, CONNECT-02, HUDDLE-03, HUDDLE-04, REMIND-04, REMIND-API-01), each for a structural reason recorded in its journey document. `browserGapCeiling` in `cmd/journeycheck` holds the count so it cannot grow silently. |
| P2 | Client breadth | Performance budgets, screenshot differential, manual assistive-technology coverage, and live-Slack comparison. |

## Web API and app-platform gaps

Every current Web API method has a registered handler; `make compatibility-report`
lists the evidence level and known-deviation count for each. The remaining
app-platform work, in dependency order, is tracked in
[Slack app platform compatibility](slack-app-platform.md#measured-remaining-gaps):
controlled HTTP/Socket differential coverage, manifest sections that are stored
but not executed, cross-app and connector workflow functions, and then
assistant, Slack Connect, and Enterprise administration depth. The datastore
query/count evaluator is extended only when a current Slack contract or
controlled differential establishes additional operators.

## Qualification gaps

Official SDK qualification proves, method by method, that a genuine client
issued a request and parsed the response. It does not prove live Slack
equivalence.

Ledger `evidence:` entries are typed and resolved by `contractcheck`: a Go test
or cross-profile contract that exists, a journey ID the browser suite cites, a
file inside the tree its kind names, or an official Slack URL. The gate closes
the mechanical half (deletions, renames, moves, mislabelled kinds). It does not
claim a target *proves* its method; that judgement stays with the reviewer.
Implementation files are admissible only in a downgrade audit, where the code
is the subject of the correction; as operation evidence a method would prove
itself with the thing being judged.

Claims at `sdk-compatible` or above that carry no method-level evidence must
not inherit it from the aggregate green suite.

Authorization is qualified from two directions. The matrix in
`tests/authorization` drives every operation as seven tiers, and the
guard-mutation gate in `tests/mutation` deletes every guard in front of an
operation and requires a suite to notice, because a matrix alone can pass with
no authorization at all. Two ceilings record what is left:
`refusalDoesNotDistinguishTheHolder` for operations whose refusal cannot be
told apart from a missing object, and `survivingGuardCeiling` for operations
nothing notices the loss of. Both only shrink, and one fixture object per
operation is what shrinks them.

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
