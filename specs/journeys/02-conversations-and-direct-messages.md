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

## CONV-02 — Create a channel

**Target sequence:** Open create-channel, enter a valid name, optionally set a
description and privacy, review the consequence of private visibility, create,
then invite members. Validation is inline and preserves input. Slack-compatible
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

## DM-02 — Start and name a group DM

A member can select multiple people, with Slack's current participant limit,
review the chosen recipients, and start one conversation. Validation prevents
duplicates and unauthorized recipients. Slack's named group-DM affordance is
available where supported and the name is visible consistently to
participants.

## DM-03 — Add people to a DM

Adding people creates a new group DM rather than mutating the participant set
of the old DM. The initiator chooses the amount of prior history Slack offers
to include; the UI states that a new conversation will be created. Only the
selected eligible history is visible to new participants. Cancellation leaves
both conversations unchanged.

## DM-04 — Close and reopen a DM

Closing removes the DM from the member's current sidebar/recent view without
deleting its history or removing it for other participants. Search, a new
message, or a participant action can reopen the same conversation. Unread and
draft state follow Slack's observed rules.

## DM-05 — Convert a group DM to a private channel

Eligible members can review Slack's conversion consequence, choose a valid
channel name, and convert the group DM. History and eligible participants move
according to Slack policy; the result is private. Permission, guest, Slack
Connect, name-conflict, and concurrent-conversion failures are explicit and
atomic.

## Evidence

- Browser journeys cover browse/join/create/details/leave/archive and
  one-to-one/group/add/close/convert DM paths at desktop and narrow widths.
- API and event tests prove that `conversations.*`, membership, history, and
  emitted events agree with the UI.
- Differential fixtures record participant/history behavior because help text
  alone does not fully specify every DM transition.

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

Sources checked 2026-07-29:

- [What is a channel?](https://slack.com/help/articles/360017938993-What-is-a-channel)
- [Join a channel](https://slack.com/help/articles/205239967-Join-a-channel)
- [Archive or delete a channel](https://slack.com/help/articles/213185307-Archive-or-delete-a-channel)
- [Understand direct messages](https://slack.com/help/articles/212281468-Understand-direct-messages)
- [Add people to a direct message](https://slack.com/help/articles/1500002969782-Add-people-to-a-direct-message)
- [Convert a group direct message to a private channel](https://slack.com/help/articles/217555437-Convert-a-group-direct-message-to-a-private-channel)
