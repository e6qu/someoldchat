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

Gaps are measured against Slack's published sources: help articles, method and
event references, Block Kit references, the pinned OpenAPI document, and the
official SDKs and their fixtures. A live Slack workspace is never needed to
close a gap. Where Slack publishes nothing, the behavior is a product choice
recorded in the journey document or ledger, not a row here.

## First-party UI journeys

| Priority | Journey | Concrete gap |
| --- | --- | --- |
| P0 | Search depth | **Semantic relevance ranking** needs an embedding model this deployment does not host; results are lexically scored and deterministically ordered instead. **Visual baselines** of this client's own desktop and narrow result layouts are not yet captured. |
| P0 | Activity depth | Activity items are not backfilled for history written before schema v107; this is an accepted data-migration limitation (a fresh store has nothing to backfill). |
| P0 | Composer depth | Slack's video source/device controls and transcripts are absent. Slack allows 1 GB per file; this deployment caps a request at 100 MiB. |
| P1 | Workspace shell | Privacy & visibility offers Slack Connect discoverability and hidden people but no blocked-invitation list, and Language & region sets the time zone and the language, but English is the only catalog: the localization system (docs/localization.md) is in place and the Preferences dialog's tabs and Language & region panel are on it, while the rest of the web client's text is still written in its markup and moves onto the catalog surface by surface; Accessibility offers zoom, link underlining, reduced motion, and announcing incoming messages but not Slack's screen-reader message-verbosity options. Zoom scales the page with CSS `zoom`, so media queries still see the window's own width: at 150% on a narrow window the desktop layout is kept until the window itself is narrow, where browser zoom would reflow sooner. `Control+N` is reserved by browsers, so New message is reached from the compose button and the Create menu. In Playwright's WebKit a drag that starts in a custom section never fires `dragstart` (unconfirmed in Safari itself). |
| P1 | Composer | Slack lets owners and admins turn off the `@channel`/`@here`/`@everyone` confirmation ([Manage who can notify a channel or workspace](https://slack.com/help/articles/115004855143-Manage-who-can-notify-a-channel-or-workspace)); the confirmation itself follows that article's at-least-six-members threshold, but there is no workspace setting to turn it off, and no browser test drives it. `/giphy` needs a third-party GIF service this deployment does not host. |
| P1 | Saved and scheduled work | Deterministic deployed-worker browser delivery. The reminder phrase `next <weekday>` is deliberately unhandled because its week semantics are ambiguous. The five deprecated app-facing `reminders.*` methods are not evidence for the first-party Later model. |
| P1 | Direct-message lifecycle | Slack lets workspace owners restrict who creates private channels and, on paid plans, who converts a Slack Connect group DM to a private channel ([Manage settings and permissions for Slack Connect direct messages](https://slack.com/help/articles/360060326994-Manage-settings-and-permissions-for-Slack-Connect-direct-messages)). Group-DM conversion applies neither setting; it refuses only Single-Channel Guests. |
| P1 | Notifications and presence | Deeper Activity and invitation policy remains. An uploaded custom emoji is held to Slack's 128 KB and image-type rules and is shrunk to fit 128 pixels in its own format, animation kept; an emoji added by image URL is stored as given, the contract `admin.emoji.add` has. |
| P2 | Calls and huddles | Captions need speech-to-text this deployment does not host. |
| P2 | Session policy mobile device check | `admin.users.session.setSettings` stores `mobile_device_check` and `getSettings` reports it, but there is no mobile client for a device check to run on. It is named rather than counted as enforced, and closes when a mobile client exists. |
| P2 | `email_password` auth policy in an SSO-only workspace | The policy is enforced at the external identity-provider callback, the only sign-in this deployment completes: a bound member is refused SSO with a 403 and no session. There is no email-and-password sign-in subsystem, so such a member has no in-product alternative; an administrator should not assign the policy in an SSO-only workspace. |
| P2 | WebKit crashes navigating away from `/app` | Twice on CI, `[NAV-05]` failed at `page.goto` of a permalink with "WebKit encountered an internal error", at teardown of `/app`. It has never reproduced locally (the hand-run probe in `tests/browser/probes` passes 60/60), so it is not proven fixed. What `/app` left to the browser at teardown is now released through one pagehide path (`pageLifecycleScript`): its live stream is closed, its timers cleared, a huddle's connection, tracks and audio context closed, and the draft keepalive POST that every navigation used to start is sent only for a draft that changed. `tests/browser/specs/teardown.spec.mjs` holds those from the browser's own objects in every engine. The row stays until the crash has not recurred on CI. |
| P2 | Past stale-timeline CI failures are unattributed | The region refresh now keeps every want until it is served (per-region single flight, deadline and retry, focus deferral with a wake-up; see `progressiveEnhancementScript` in `internal/web/handler.go`), which removes each mechanism found that could leave a region stale, and the `[MSG-01 RESILIENCE-01]` browser journeys reproduce and guard them. The three CI occurrences, and the `[CONV-02 NAV-04]` control left disabled, were never reproduced locally, so which defect caused them is inferred rather than shown. A recurrence would show in the page's `data-refresh-timeouts` or `data-discarded-refreshes` counters in the retained trace. |
| P2 | Huddle media forwarding can time out on CI | Once on CI (`make test-huddle-media`, PR 307), `TestRoomForwardsCameraAndScreenAsDistinctStreams` waited its full 120 s with the room holding only the receiver (`peers=1`): the publisher had joined and been removed, which the SFU does when a peer's connection fails, so the publisher's ICE never completed on the runner. The re-run passed and it does not reproduce locally. The cause, a runner network limit or a handshake race in `internal/huddlesfu`, is unproven; the room's `debugState` output is what to keep from the next occurrence. |
| P2 | Browser-citation ceiling | Six journeys have no browser citation (APP-06, AUTH-02, AUTH-05, CONNECT-02, REMIND-04, REMIND-API-01), each for a structural reason recorded in its journey document. HUDDLE-03 and HUDDLE-04 left the list when the suite gained a second signed-in member. `browserGapCeiling` in `cmd/journeycheck` holds the count so it cannot grow silently. |
| P2 | Client breadth | Performance budgets, visual baselines of this client's own layouts, and manual assistive-technology coverage. |

## Web API and app-platform gaps

Every current Web API method has a registered handler; `make compatibility-report`
lists the evidence level and known-deviation count for each. The remaining
app-platform work, in dependency order, is tracked in
[Slack app platform compatibility](slack-app-platform.md#measured-remaining-gaps):
the reference-page rate-limit tiers of the 43 methods the pinned SDK tier
table predates, manifest sections that are stored but not executed,
cross-app and connector workflow functions, and then assistant, Slack
Connect, and Enterprise administration depth. The datastore query/count
evaluator is extended only when a current Slack contract establishes
additional operators.

## Qualification gaps

Official SDK qualification proves, method by method, that a genuine client
issued a request and parsed the response. It does not prove that the response
carries everything the method's published reference specifies.

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
5. promote a method to `conforms-to-published-source` only when tests assert
   everything its reference, the pinned OpenAPI document, and the pinned SDK
   fixtures specify for it, including its rate-limit tier.
