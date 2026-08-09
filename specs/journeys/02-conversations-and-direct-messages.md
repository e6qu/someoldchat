# Channels and direct messages

## CONV-01 — Browse, search, and join channels

A member can open the channel browser, distinguish channels already joined
from discoverable public channels, search/filter the list, inspect a channel,
and join when policy permits. Joining durably adds membership before the
channel becomes writable. Private, archived, externally shared, full,
restricted, and approval-gated channels have distinct labels and outcomes.

Empty search results MUST not look like an empty workspace. A channel that
becomes unavailable between discovery and join produces a handled result and
does not leave a phantom sidebar item.

A guest reaches a channel by being added to it, never by naming one. A
single-channel guest belongs to the one channel they were invited to and a
multi-channel guest to the ones members put them in; neither browses the
workspace, so neither joins by identifier. The refusal names the tier, because
`user_is_ultra_restricted` tells a caller the person is confined to one channel
and `user_is_restricted` does not.

## CONV-02 — Create a channel

**Target sequence:** Open create-channel, enter a valid name, optionally set a
description and privacy, review the consequence of private visibility, create,
then invite members. Guests do not create channels. Validation is inline and preserves input. Slack-compatible
normalization, reserved names, duplicate names, length limits, permissions,
default-channel policy, and concurrent creation are enforced by the backend.

The committed channel opens exactly once and appears in other clients through
the corresponding API/event projection.

## CONV-03 — Read and manage channel details

The channel heading opens details containing at least About, members, settings,
integrations/bookmarks where available, and the correct public/private/shared/
archived state. Authorized members can edit topic, description/purpose, and
name; invite or remove members; manage posting/notification settings; and copy
the channel identifier/link. Unauthorized controls are absent or disabled with
an explanation, while direct backend requests remain denied.

Inviting a workspace member works for both public and private channels and
commits membership, its durable event, and the recipient's Invitations Activity
item as one mutation. Repeating an ordinary `conversations.invite` for a member
already present returns Slack's `already_in_channel` error and does not create a
second event or Activity item. The API applies Slack's current bot/user and
public/private alternative scope matrix. A multi-user request is all-or-none by
default and returns Slack's per-user `errors` entries for missing users,
self-invites, and existing members. `force=true` invites only the valid subset.
The formal current argument contract limits one request to 100 user IDs.

## CONV-04 — Leave, archive, unarchive, and delete

- Leaving removes the member after Slack's applicable confirmation and moves
  them to a valid destination. It does not archive the channel.
- Archiving prevents new ordinary messages and clearly marks history as
  archived while preserving access Slack permits.
- Unarchiving restores the channel without silently restoring prior members or
  settings that Slack does not restore.
- General/default/special channels, last owners, shared channels, already
  archived channels, and insufficient roles follow Slack's distinct rules.
- Permanent deletion is an administrative journey with Slack's confirmation,
  retention, and audit consequences; archive MUST NOT masquerade as delete.

## DM-01 — Find and open a one-to-one DM

The DMs surface and People entry allow search by member identity, show recent
DMs, and open one canonical one-to-one conversation. Opening an existing DM
MUST not create duplicates. Deactivated, external, bot, self, restricted, and
unavailable users are represented according to Slack's behavior.

Acceptance sequence:

1. Open **DMs** from workspace navigation.
2. Search by a member's display or real name.
3. Select that member and start the conversation.
4. The canonical DM opens; repeating the sequence opens the same conversation
   identifier and history.
5. The conversation appears in Recent until the viewer closes it. Empty
   search results retain the new-message affordance and do not imply that the
   workspace has no members.

## DM-02 — Start and name a group DM

A member can select multiple people, with Slack's current participant limit,
review the chosen recipients, and start one conversation. Validation prevents
duplicates and unauthorized recipients. Slack's named group-DM affordance is
available where supported and the name is visible consistently to
participants.

Acceptance sequence:

1. Open **DMs** and select between two and eight other active members. The
   signed-in member counts toward Slack's nine-person total.
2. Duplicate selections collapse to one recipient; a tenth total participant,
   a deactivated member, a foreign-workspace identifier, or an empty
   selection is rejected without creating a conversation.
3. Optionally enter a group-DM name, then start the conversation.
4. Every participant sees one canonical conversation. A later rename updates
   that group DM without applying channel-name lowercasing or creating a new
   conversation.

## DM-03 — Add people to a DM

Adding people creates a new group DM rather than mutating the participant set
of the old DM. The initiator chooses the amount of prior history Slack offers
to include; the UI states that a new conversation will be created. Only the
selected eligible history is visible to new participants. Cancellation leaves
both conversations unchanged.

**Preconditions and entry points:** The actor is an active member of the source
DM. The conversation header opens details, the Members section exposes **Add
people**, and the control is absent when no eligible member can be added or the
nine-person total has been reached. A deactivated user, existing participant,
foreign-workspace identifier, or selection that would exceed nine people is
refused without creating or modifying a conversation.

Acceptance sequence:

1. Open a one-to-one or group DM, open its member details, and choose **Add
   people**.
2. Select eligible members, then choose **Next**. No state changes.
3. Choose one of Slack's offered **Include conversation history** options, then
   choose **Done**.
4. A keyboard- and screen-reader-readable confirmation names the new
   participants, the selected history consequence, and both-conversation
   notification behavior. No state changes on this step. **Cancel** returns to
   the source DM with both conversations unchanged.
5. Select **Confirm** to create the new group DM.
6. The old DM retains its participant set. The new canonical participant set
   receives only the selected history range, and both relevant conversations
   receive Slack's participant notification.

The public Slack help source requires a selectable history choice but does not
enumerate every option label or range. Those exact choices MUST be captured by
a controlled live-Slack differential fixture before SameOldChat claims its
two current choices—no history and all history—are the complete Slack option
set. The absence of a public `conversations.*` method for this client journey
MUST NOT be filled by inventing one.

## DM-04 — Close and reopen a DM

Closing removes the DM from the member's current sidebar/recent view without
deleting its history or removing it for other participants. Search, a new
message, or a participant action can reopen the same conversation. Unread and
draft state follow Slack's observed rules.

Acceptance sequence:

1. Close a one-to-one or group DM from conversation details.
2. The DM disappears only from that member's current navigation. Membership,
   history, files, read state, drafts, and every other participant's
   navigation remain intact.
3. A second Web API `conversations.close` returns `ok`, `no_op`, and
   `already_closed`; `conversations.leave` remains unsupported for IM/MPIM
   channel types.
4. Selecting the same one-to-one member or exact group participant set reopens
   the original identifier. A new message from a participant also restores it
   to current navigation.

## DM-05 — Convert a group DM to a private channel

Eligible members can review Slack's conversion consequence, choose a valid
channel name, and convert the group DM. History and eligible participants move
according to Slack policy; the result is private. Permission, guest, Slack
Connect, name-conflict, and concurrent-conversion failures are explicit and
atomic.

**Preconditions and entry points:** From the group-DM header, open details and
the Settings section. Slack permits all members and Multi-Channel Guests by
default; workspace owners/admins may restrict private-channel creation and
conversion involving external people. A one-to-one DM and a Single-Channel
Guest never receive a usable conversion action.

Acceptance sequence:

1. From a group DM's Settings, choose **Change to a private channel**.
2. Review that existing messages and files will be visible to members added
   after conversion, enter a valid unique channel name, and confirm.
3. One atomic mutation retains history, files, and current eligible members;
   changes the conversation to a private channel; and posts the conversion
   notification.
4. A one-to-one DM, disallowed guest/external conversion, invalid or
   conflicting name, and concurrent second conversion fail without a
   half-renamed or half-converted conversation.

The successful transition preserves the conversation identifier, current
membership, message timestamps and authors, file visibility, drafts, and read
state. It clears DM-only open/closed navigation state, becomes discoverable as
a private channel, and cannot be converted a second time.

## Evidence

- A workspace administrator can change a channel between public and private
  from the channel itself, in both directions.
- Browser journeys cover browse/join/create/details/leave/archive plus the
  dedicated DM surface at desktop and narrow widths. Handler/service/store/API
  journeys cover one-to-one/group open, naming, close, idempotent close,
  canonical reopen, reviewed add-people with no/all-history choices, automatic
  notices, and in-place private-channel conversion.
- API and event tests prove that `conversations.*`, membership, history, and
  emitted events agree with the UI.
- Current official Node, Python, and Java SDKs prove canonical exact-member
  group-DM opening. Add-history and conversion are first-party Slack journeys,
  not invented Web API methods. Controlled live differential fixtures still
  need to record participant/history presentation because help text does not
  fully specify every option and transition.
## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| CONV-01 | [Join a channel](https://slack.com/help/articles/205239967-Join-a-channel) | Slack lets eligible members discover, preview, and join channels. |
| CONV-02 | [What is a channel?](https://slack.com/help/articles/360017938993-What-is-a-channel) | Slack distinguishes created public and private channel spaces. |
| CONV-03 | [What is a channel?](https://slack.com/help/articles/360017938993-What-is-a-channel) | Channel headers and details expose membership and conversation context. |
| CONV-04 | [Archive or delete a channel](https://slack.com/help/articles/213185307-Archive-or-delete-a-channel) | Archive, unarchive, and permanent deletion have different consequences. |
| DM-01 | [Understand direct messages](https://slack.com/help/articles/212281468-Understand-direct-messages) | Slack provides a searchable DMs surface and canonical direct conversations. |
| DM-02 | [Understand direct messages](https://slack.com/help/articles/212281468-Understand-direct-messages) | Group DMs support up to nine people and may have a name. |
| DM-03 | [Add people to a direct message](https://slack.com/help/articles/1500002969782-Add-people-to-a-direct-message) | Adding people creates a new group DM with chosen history visibility. |
| DM-04 | [Understand direct messages](https://slack.com/help/articles/212281468-Understand-direct-messages) | DMs remain discoverable from Slack's dedicated searchable surface. |
| DM-05 | [Convert a group DM to a private channel](https://slack.com/help/articles/217555437-Convert-a-group-direct-message-to-a-private-channel) | Eligible group DMs can become private channels with preserved history. |

Sources checked 2026-07-30:

- [What is a channel?](https://slack.com/help/articles/360017938993-What-is-a-channel)
- [Join a channel](https://slack.com/help/articles/205239967-Join-a-channel)
- [Archive or delete a channel](https://slack.com/help/articles/213185307-Archive-or-delete-a-channel)
- [Understand direct messages](https://slack.com/help/articles/212281468-Understand-direct-messages)
- [Add people to a direct message](https://slack.com/help/articles/1500002969782-Add-people-to-a-direct-message)
- [Convert a group direct message to a private channel](https://slack.com/help/articles/217555437-Convert-a-group-direct-message-to-a-private-channel)
- [`conversations.close`](https://docs.slack.dev/reference/methods/conversations.close/)
