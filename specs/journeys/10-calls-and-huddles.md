# Calls and huddles

## HUDDLE-01 — Start a huddle

An eligible member can start a huddle from a channel or DM. The control names
the conversation and whether participants will be notified. Starting creates
one active call/huddle record and visible conversation event/state; concurrent
starts converge on the same active huddle or return Slack's conflict outcome.
Posting permission and huddle permission are checked independently.

Microphone/camera/device permission is requested only when needed. Denial,
missing device, unsupported browser, network failure, plan/policy restriction,
and an already active huddle are explicit and do not fabricate a connected
state.

## HUDDLE-02 — Join and participate

The active-huddle affordance identifies participants and join state. Joining
connects the authenticated eligible member, exposes mute/unmute and Slack's
current audio/video, screen-share, reactions, thread, canvas, and caption
features where supported, and gives continuous accessible state feedback.

Device selection, permission revocation, reconnect, handoff, participant
arrival/departure, moderator action, and degraded media reconcile without
duplicating participants. Muted/video/screen-sharing state MUST match the media
session rather than an optimistic button alone.

## HUDDLE-03 — Invite and notify participants

Slack-supported invitations target eligible workspace/external participants,
respect notification policy and DND, and identify the source conversation.
Unauthorized/private conversation membership is not disclosed. Accept, decline,
expired invitation, already joined, and revoked access have distinct outcomes.

## HUDDLE-04 — Leave and end

Leaving disconnects only the member unless Slack's end-for-everyone action is
authorized and chosen. Ending terminates the shared call exactly once, updates
conversation state and API/event projections, and prevents stale clients from
appearing connected. Unexpected browser/process loss eventually removes stale
presence without ending an active call owned by others.

## CALL-01 — Integrate external calls

Apps using Slack's Calls API can add, update, inspect, and end call metadata,
participants, and join links. SameOldChat renders these as external call
objects and never claims to carry media it does not host. App ownership,
workspace visibility, participant identity, URL safety, and event projection
match the current API contract.

## What this deployment implements, and what it does not

The huddle lifecycle is implemented and durable: one active huddle per
conversation, with concurrent starts converging on it through an atomic upsert
rather than a read-then-create; join and leave as single-participant moves;
the last participant to leave ending it in the same transaction; and ending it
for everyone reserved to the person who started it or a workspace
administrator, because it removes everyone else. Conversation membership is the
authority throughout, checked independently of posting permission — `calls.add`
checks neither, because an app-registered call has no conversation to check
against.

**There is no media transport of any kind.** No WebRTC, no audio, no video, no
screen sharing, no device permission request — because there is no device to
request. The surface says so wherever it offers a control, so nobody presses
"Join huddle" expecting sound. HUDDLE-02's audio, video, screen sharing and
reactions are therefore unimplemented, and HUDDLE-01's device-permission
outcomes cannot arise here.

The alternative — hiding the surface until WebRTC exists — would leave the
lifecycle unreachable and untestable, and would not make the missing media any
more present.

## Evidence

- Real browser/media qualification uses synthetic devices in Chromium,
  Firefox, and WebKit where supported and records explicit unsupported
  boundaries.
- Multi-client tests prove join/leave/reconnect/invite/end state and
  accessible mute/video/share announcements.
- Official SDKs exercise `calls.*` with app ownership and error variants.
- A live Slack sandbox records conversation events and metadata behavior;
  media quality comparison is separately bounded and MUST not be inferred from
  API success.
## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| HUDDLE-01 | [Use huddles in Slack](https://slack.com/help/articles/4402059015315-Use-huddles-in-Slack) | Eligible members start a huddle from a channel or direct message. |
| HUDDLE-02 | [Use huddles in Slack](https://slack.com/help/articles/4402059015315-Use-huddles-in-Slack) | Huddles provide audio, video, screen sharing, reactions, and a dedicated thread. |
| HUDDLE-03 | [Use huddles in Slack](https://slack.com/help/articles/4402059015315-Use-huddles-in-Slack) | Conversation members and explicitly invited people can join active huddles. |
| HUDDLE-04 | [Use huddles in Slack](https://slack.com/help/articles/4402059015315-Use-huddles-in-Slack) | Leaving and ending a huddle are distinct member and shared actions. |
| CALL-01 | [Calls API](https://docs.slack.dev/apis/web-api/using-the-calls-api/) | Apps register external call metadata and participants without Slack carrying media. |

Sources checked 2026-07-29:

- [Use huddles in Slack](https://slack.com/help/articles/4402059015315-Use-huddles-in-Slack)
- [Calls API](https://docs.slack.dev/apis/web-api/using-the-calls-api/)
