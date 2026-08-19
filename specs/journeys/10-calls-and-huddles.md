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

Reactions are implemented: a participant sends one of the huddle's quick emoji
and every participant sees it float briefly and fade. The reaction is ephemeral
— broadcast to the huddle and never stored — validated against the same
standard-or-custom emoji rule as a message reaction, and refused to anyone who
is not in the running huddle. It rides the huddle's own live event without
refreshing the media session, alongside the WebRTC signalling the same live
stream carries. Captions require speech-to-text this deployment does not host
and remain a bounded gap rather than a fabricated control.

## HUDDLE-03 — Invite and notify participants

A member already in a huddle can pull in a specific person who has not looked.
The invitee must be a member of the conversation the huddle belongs to — the
same eligibility joining enforces — so an invitation never offers a door the
invitee could not already have opened, and never discloses a private
conversation to somebody outside it. Inviting yourself, somebody already in the
huddle, or a member who is not in the huddle inviting on its behalf are refused
rather than sending a notification about nothing.

The invitation is a notification and not an admission: the invitee still joins
through the ordinary join, so nothing about it bypasses the membership the join
checks. It reaches them two ways from one durable write — the recipient-scoped
event stream, so a client can surface it live, and an Activity invitation item,
so a member who was not looking finds it later. It carries no media; the invitee
opens their own connection when they join.

External participants and DND/notification-policy routing are not implemented and
are named here rather than approximated: this deployment's huddle is inside one
workspace, and the notification policy that would gate an invitation is the same
unimplemented surface recorded under Notifications and presence.

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

**Media is relayed by an in-process selective forwarding unit.** Joining opens
the member's microphone and connects their browser — with a single
WebRTC connection — to the server itself, which runs the SFU inside the same
binary as one more listener rather than a second service. The browser publishes
its microphone, its camera and its screen — each on its own lane — once; the SFU
forwards each participant's media to everyone else. That is one upload per
participant however large the huddle, not the mesh's one upload per other
participant, and the media flows through a server the browser can always reach
instead of a direct peer path that fails behind NAT. The SFU's forwarded media
is grouped by participant: their microphone and camera share one stream, whose
id is that participant, so they land on that participant's tile, while a screen
share is a second stream the SFU marks with the id the browser declared for it,
so it opens its own presenter tile beside the camera rather than replacing it.
Tiles are labelled with the participant's display name.

Negotiation has two halves. The browser publishes with a single offer POSTed to
the SFU, which answers. Thereafter the SFU drives renegotiation: when the set of
tracks a browser should receive changes, the SFU sends it an offer as a
recipient-scoped signal stamped from the SFU, and the browser answers over the
signal endpoint. The SFU answers with host candidates, which reach it on the
same network or host; a deployment behind NAT sets a public address the SFU
advertises as a one-to-one mapping and a single UDP port to expose
(`-huddle-public-ip`, `-huddle-udp-port`), and hands the browser the ICE servers
to use (`-huddle-ice-servers`), so one known port and one address are all an
operator opens.

Presence is live. Each participant broadcasts its microphone, camera and
screen-share state, so every tile carries a muted or camera-off badge, the
loudest speaker's tile is ringed (a Web Audio level meter over each stream), and
a participant sharing their screen opens a dedicated presenter tile — the
screen's own forwarded stream — that fills the grid while their camera keeps
playing in the filmstrip alongside everyone else. Screen sharing is relayed like
any other video, offered where the browser provides `getDisplayMedia`; a sharer
sends their camera and their screen at once, on two lanes, rather than trading
one for the other. Reactions are
implemented — a participant sends one of the huddle's quick emoji and every
participant sees it float and fade — and the huddle canvas is the channel's own
canvas, offered from the huddle bar. Captions remain unimplemented because they
need speech-to-text this deployment hosts nowhere.

The forwarding path is covered by in-process loopback tests — two real pion peer
connections stand in for browsers over the actual offer/answer/candidate
exchange. One proves a published track is forwarded to the other with real RTP
flowing; a second proves a participant's camera and screen are forwarded at once
as two distinct streams, each carrying RTP, so a subscriber can show a screen
share beside the sharer's camera rather than in place of it. Bounded gaps,
recorded rather than hidden: two browsers forwarding to each other end to end is
still not covered by the browser suite, because the harness authenticates one
session, so it drives one browser into a huddle and not two. What the browser
suite covers is everything one browser decides — the microphone opening, the
connection to the SFU establishing, the track muting, the camera starting,
sharing the screen as a second presenter tile while the camera keeps playing,
and its own presence broadcast returning to badge its tile.

## Evidence

- Real browser/media qualification uses synthetic devices in Chromium,
  Firefox, and WebKit where supported and records explicit unsupported
  boundaries.
- Multi-client tests prove join/leave/reconnect/end state and accessible
  mute/video/share announcements. Inviting a specific member (HUDDLE-03) is
  proven at the seam and the service rather than in the browser suite, for the
  same reason the peer-to-peer handshake is: the harness authenticates one
  session and cannot arrange the second member an invitation needs. The seam
  parity suite drives the invitation across both compositions with every
  refusal — self, already-joined, an outsider to the conversation, and a
  non-participant inviting — and a service test reads the invitation back
  through the invitee's Activity and confirms nobody else is told.
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
