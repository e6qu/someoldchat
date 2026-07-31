package events

import "sort"

// Surface names one delivery surface an official Slack client can attach to.
//
// It exists so a topic that belongs on one surface and not another is one
// column of one row rather than a second table each transport re-implements.
// Two facts already need it: user.presence_changed is an RTM event and is not
// an Events API topic in the pinned AsyncAPI at all, and message.ephemeral is
// addressed to one recipient, so only a transport with a recipient may carry
// it. Both are expressed here instead of as a named-topic exception inside
// internal/socketmode and internal/realtime.
type Surface uint8

const (
	// SurfaceEventsAPI is the signed HTTP event_callback webhook.
	SurfaceEventsAPI Surface = 1 << iota
	// SurfaceSocketMode is the events_api envelope written to an app's socket.
	SurfaceSocketMode
	// SurfaceRTM is the bare inner event written to an RTM socket.
	SurfaceRTM
)

// everySurface is the ordinary case: an event an app may receive over the
// webhook and over Socket Mode, and that a user's RTM socket also carries.
const everySurface = SurfaceEventsAPI | SurfaceSocketMode | SurfaceRTM

// appSurfaces are the two surfaces an application is attached to. It is the
// answer for an event that is only meaningful to an installed app.
const appSurfaces = SurfaceEventsAPI | SurfaceSocketMode

// builder renders the Slack inner events one durable payload becomes. The
// result is a slice because the mapping is not one-to-one: a single
// conversation.members_invited record is N member_joined_channel events, and a
// topic that is not publishable is zero.
type builder func(Delivered) ([]Inner, error)

// slackRule is a topic's mapping onto the Slack event vocabulary. The zero
// value means "this topic is not publishable to any Slack client", which is the
// correct and deliberate answer for most of the product's topics: they describe
// product concepts (canvases, lists, admin actions, read state) that Slack has
// no event for. Exclusion is an answer, not a gap.
type slackRule struct {
	// eventType is the Slack event name, or "" when the topic maps to none.
	eventType string
	// surfaces are the surfaces this event may be written to.
	surfaces Surface
	// automatic marks an app-targeted event Slack dispatches because the app
	// owns the invoked resource rather than because the app subscribed to the
	// event name in its manifest.
	automatic bool
	// build renders the inner event. A rule with an eventType and no build is a
	// mapping whose inner shape this repository has not modelled yet — either
	// because the pinned snapshot does not settle it, or because the event needs
	// content the durable payload deliberately does not carry. Such a topic is
	// withheld rather than published in a shape nobody verified.
	build builder
}

// topicRule is one row of the single topic table. Collapsing the previously
// separate internal-topic and recipient-scoped sets into this table means a
// topic's whole meaning — who may receive it, which Slack event it is, and on
// what authority — is decided in exactly one place. Adding a topic without
// deciding its Slack mapping is then visible as a row that says so, instead of
// being invisible everywhere.
type topicRule struct {
	topic string
	// internal marks a record that exists only for an internal worker.
	internal bool
	// recipient marks a record addressed to exactly one user.
	recipient bool
	slack     slackRule
	// note records the authority for this row: why the topic maps to the event
	// it maps to, or why it maps to none. It is prose on purpose — the decisions
	// rest on the pinned AsyncAPI, on its two worked examples, on inference, or
	// on the absence of any pinned event, and those are not the same evidence.
	note string
}

// mapped builds a row for a topic whose Slack event name is settled but whose
// inner shape is not modelled here. It is deliberately distinct from an
// excluded row: "we know which event this is and cannot yet build it" and "this
// is not a Slack event" are different states, and collapsing them is how the
// second becomes an excuse for the first.
func mapped(eventType string, surfaces Surface) slackRule {
	return slackRule{eventType: eventType, surfaces: surfaces}
}

// translated builds a row for a topic this package can render today.
func translated(eventType string, surfaces Surface, build builder) slackRule {
	return slackRule{eventType: eventType, surfaces: surfaces, build: build}
}

func automatic(eventType string, surfaces Surface, build builder) slackRule {
	return slackRule{eventType: eventType, surfaces: surfaces, automatic: true, build: build}
}

// topicRules is the single topic table.
//
// Provenance vocabulary used in the notes:
//
//   - "pinned topic": the event name is a topics key in
//     specs/upstream/slack-api-specs/events-api/slack_events_api_async_v1.json.
//   - "pinned example": the inner object appears as a worked example in
//     slack_common_event_wrapper_schema.json. Exactly two exist — a plain
//     message and a reaction_added — and they are the only inner field names
//     the pinned material settles at all. Every other pinned topic is
//     $ref: GenericEventWrapper, which constrains the inner event to
//     {type, event_ts} plus additionalProperties.
//   - "inference": the event name follows from the pinned set but the mapping
//     itself is not stated there.
//   - "not pinned": no event of that name exists in the snapshot.
//
// The table is a slice rather than a map so InternalTopics and
// RecipientScopedTopics stay in declaration order: both feed SQL IN predicates,
// and a set whose order depends on map iteration produces a different query
// text on every process start.
var topicRules = []topicRule{
	// ---- internal worker records -------------------------------------------
	{topic: FileBlobDeleteTopic, internal: true,
		note: "internal blob-cleanup work record; its payload is an object-storage key"},
	{topic: UserPhotoBlobDeleteTopic, internal: true,
		note: "internal blob-cleanup work record; its payload is an object-storage key"},

	// ---- messages ----------------------------------------------------------
	//
	// Identifier-only message records are withheld here. App delivery projects a
	// complete Slack message only after proving the installed bot is a member of
	// the conversation (service.PrepareAppEvent); workspace event streams retain
	// the content-free record and cannot leak private text.
	{topic: "message.created", slack: mapped("message", everySurface),
		note: "pinned topic and example; app delivery hydrates it only after conversation visibility is proved"},
	{topic: "message.changed", slack: mapped("message", everySurface),
		note: "pinned topic; the message_changed subtype value is not pinned, and the event needs both bodies"},
	{topic: "message.deleted", slack: mapped("message", everySurface),
		note: "the pinned message schema is exactly this variant (deleted_ts, subtype, hidden, previous_message), but previous_message needs the deleted text"},
	{topic: "message.unfurled", slack: mapped("message", everySurface),
		note: "pinned topic; the subtype and the attachment shape are not pinned"},
	{topic: EphemeralMessageTopic, recipient: true, slack: mapped("message", SurfaceRTM),
		note: "addressed to one user and carries that user's text, so only a transport with a recipient may carry it; the ephemeral subtype is not pinned"},
	{topic: "message.scheduled",
		note: "not pinned: a scheduled message produces a message event when it posts; the scheduling act has no Slack event"},
	{topic: "message.schedule_deleted",
		note: "not pinned: no Slack event for cancelling a scheduled message"},

	// ---- reactions, pins and stars -----------------------------------------
	//
	// These are the events that need no content lookup: the durable payload
	// already names the actor, the conversation and the item. See itemEvent for
	// what the pinned material does and does not settle about the inner shape.
	{topic: "reaction.added", slack: translated("reaction_added", everySurface, itemEvent("reaction_added", true)),
		note: "pinned topic; inner shape is the pinned reaction_added example {user, reaction, item{type,channel,ts}, item_user, event_ts} — item_user is omitted because the payload does not carry the item's author"},
	{topic: "reaction.removed", slack: translated("reaction_removed", everySurface, itemEvent("reaction_removed", true)),
		note: "pinned topic; inner shape mirrors the pinned reaction_added example"},
	{topic: "pin.added", slack: translated("pin_added", everySurface, itemEvent("pin_added", false)),
		note: "pinned topic; inner shape NOT settled by the pinned material — the field names are carried over from the pinned reaction_added example, which is the only inner event the snapshot works out"},
	{topic: "pin.removed", slack: translated("pin_removed", everySurface, itemEvent("pin_removed", false)),
		note: "pinned topic; inner shape NOT settled, as pin.added"},
	{topic: "star.added", recipient: true, slack: translated("star_added", everySurface, itemEvent("star_added", false)),
		note: "current Slack event reference: Events and RTM; recipient-scoped to the authenticated user authorization"},
	{topic: "star.removed", recipient: true, slack: translated("star_removed", everySurface, itemEvent("star_removed", false)),
		note: "current Slack event reference: Events and RTM; recipient-scoped to the authenticated user authorization"},

	// ---- conversation membership -------------------------------------------
	{topic: "conversation.member_added", slack: translated("member_joined_channel", everySurface, memberJoinedChannel),
		note: "pinned topic; a self-service join carries the joining user and channel"},
	{topic: "conversation.members_invited", slack: translated("member_joined_channel", everySurface, membersJoinedChannel),
		note: "pinned topic; one record fans out to one member_joined_channel per invited user. Slack also sends inviter, which the payload does not carry, so it is omitted rather than guessed"},
	{topic: "conversation.member_left", slack: translated("member_left_channel", everySurface, memberLeftChannel),
		note: "pinned topic; inner shape NOT settled — {user, channel} follow the naming of the pinned reaction_added example"},
	{topic: "conversation.member_kicked", slack: translated("member_left_channel", everySurface, memberLeftChannel),
		note: "inference: Slack has no removal event, and a removed member has left the channel. The distinction between leaving and being removed is lost, which is what the pinned vocabulary can express"},

	// ---- conversation lifecycle --------------------------------------------
	{topic: "conversation.created", slack: mapped("channel_created", everySurface),
		note: "pinned topic; needs the conversation object (name, created, creator) and its privacy, and the pinned set has no group_created, so a private channel has no pinned mapping"},
	{topic: "conversation.direct_created", slack: mapped("im_created", everySurface),
		note: "pinned topic; needs the channel object"},
	{topic: "conversation.renamed", slack: mapped("channel_rename", everySurface),
		note: "pinned topics channel_rename and group_rename; which one applies needs the conversation's privacy, and the new name is not in the payload"},
	{topic: "conversation.archived", slack: mapped("channel_archive", everySurface),
		note: "pinned topics channel_archive and group_archive; needs privacy, and Slack sends the acting user"},
	{topic: "conversation.unarchived", slack: mapped("channel_unarchive", everySurface),
		note: "pinned topics channel_unarchive and group_unarchive; needs privacy"},
	{topic: "conversation.deleted", slack: mapped("channel_deleted", everySurface),
		note: "pinned topic; withheld only because the channel_type / group split is unsettled"},
	{topic: "conversation.topic_changed",
		note: "not pinned as its own event: Slack emits this as a message with subtype channel_topic, and neither the subtype value nor the message shape is settled here"},
	{topic: "conversation.purpose_changed",
		note: "not pinned as its own event: Slack emits this as a message with subtype channel_purpose"},
	{topic: "conversation.read",
		note: "not pinned: Slack has no public event for conversations.mark"},
	{topic: "conversation.converted_to_private",
		note: "not pinned: channel-conversion events postdate the snapshot"},
	{topic: "conversation.archive_changed_by_admin",
		note: "not pinned: an administrative product concept"},
	{topic: "conversation.renamed_by_admin",
		note: "not pinned: an administrative product concept"},
	{topic: "conversation.preferences_changed",
		note: "not pinned: a product concept"},
	{topic: "conversation.access_group_added",
		note: "not pinned: a product concept"},
	{topic: "conversation.access_group_removed",
		note: "not pinned: a product concept"},
	{topic: "conversation.teams_changed",
		note: "not pinned: shared-channel events postdate the snapshot"},
	{topic: "conversation.shared_disconnected",
		note: "not pinned: shared-channel events postdate the snapshot"},

	// ---- emoji, user groups, users, workspace, files -----------------------
	{topic: "emoji.added", slack: mapped("emoji_changed", appSurfaces),
		note: "pinned topic; four topics collapse to one event and the add/remove/rename subtype values are not pinned"},
	{topic: "emoji.removed", slack: mapped("emoji_changed", appSurfaces),
		note: "pinned topic; subtype value not pinned"},
	{topic: "emoji.renamed", slack: mapped("emoji_changed", appSurfaces),
		note: "pinned topic; subtype value not pinned"},
	{topic: "emoji.alias_added", slack: mapped("emoji_changed", appSurfaces),
		note: "pinned topic; subtype value not pinned"},
	{topic: "usergroup.created", slack: mapped("subteam_created", everySurface),
		note: "pinned topic; needs the full subteam object"},
	{topic: "usergroup.updated", slack: mapped("subteam_updated", everySurface),
		note: "pinned topic; three topics collapse to one event and it needs the subteam object"},
	{topic: "usergroup.enabled_changed", slack: mapped("subteam_updated", everySurface),
		note: "pinned topic; needs the subteam object"},
	{topic: "usergroup.channels_changed", slack: mapped("subteam_updated", everySurface),
		note: "pinned topic; needs the subteam object"},
	{topic: "usergroup.users_changed", slack: mapped("subteam_members_changed", everySurface),
		note: "pinned topic; needs added and removed lists, and the payload carries the whole set"},
	{topic: "user.created", slack: mapped("team_join", everySurface),
		note: "pinned topic; needs the full user object"},
	{topic: "user.profile_changed", slack: mapped("user_change", everySurface),
		note: "pinned topic; needs the full user object"},
	{topic: "user.removed", slack: mapped("user_change", everySurface),
		note: "inference: Slack reports deletion as user_change with deleted true; needs the user object"},
	{topic: "user.dnd_snoozed", slack: mapped("dnd_updated", everySurface),
		note: "pinned topics dnd_updated and dnd_updated_user; which one applies is recipient-dependent, and the event needs a dnd_status object"},
	{topic: "user.dnd_snooze_ended", slack: mapped("dnd_updated", everySurface),
		note: "pinned topic; needs a dnd_status object"},
	{topic: "user.dnd_ended", slack: mapped("dnd_updated", everySurface),
		note: "pinned topic; needs a dnd_status object"},
	{topic: "user.presence_changed", slack: mapped("presence_change", SurfaceRTM),
		note: "presence_change is an RTM event and is NOT an Events API topic in the pinned AsyncAPI, so it must never reach the webhook or Socket Mode"},
	{topic: "user.assigned",
		note: "not pinned: an account-provisioning product concept"},
	{topic: "user.expiration_changed",
		note: "not pinned: an account-provisioning product concept"},
	{topic: "user.sessions_reset",
		note: "not pinned: tokens_revoked is about application tokens, not user sessions"},
	{topic: "user.sessions_revoked_by_oidc",
		note: "not pinned: tokens_revoked is about application tokens, not user sessions"},
	{topic: "workspace.name_changed", slack: mapped("team_rename", everySurface),
		note: "pinned topic; withheld only because the inner shape is unmodelled and the payload's field names are not the event's"},
	{topic: "workspace.created",
		note: "not pinned: no Slack event for workspace creation"},
	{topic: "workspace.description_changed",
		note: "not pinned: team_domain_change and email_domain_changed are different facts"},
	{topic: "workspace.discoverability_changed",
		note: "not pinned"},
	{topic: "workspace.icon_changed",
		note: "not pinned"},
	{topic: "workspace.default_channels_changed",
		note: "not pinned"},
	{topic: "workspace.role_changed",
		note: "not pinned: role administration has no Slack event"},
	{topic: "file.created", slack: mapped("file_created", everySurface),
		note: "pinned topic; app delivery hydrates the file object only when the installed bot can access it"},
	{topic: "file.shared", slack: mapped("file_shared", everySurface),
		note: "current Slack reference; delivery uses the immutable file/channel snapshot captured by the share transaction"},
	{topic: "file.unshared", slack: mapped("file_unshared", everySurface),
		note: "current Slack reference; delivery authorizes against the channel recorded before the final share is removed"},
	{topic: "file.public_shared", slack: mapped("file_public", everySurface),
		note: "pinned topic; needs the file object"},
	{topic: "file.public_revoked",
		note: "not pinned: file_unshared is a different fact"},
	{topic: "file.comment_deleted",
		note: "the snapshot still lists file_comment_deleted, but Slack retired file-comment events and specs/api-compatibility.md ranks current official documentation above the archived AsyncAPI, so the pinned file alone is not authority to emit it"},
	{topic: "remote_file.created",
		note: "not settled: plausibly file_created, but the pinned material does not say a remote file is a file"},
	{topic: "remote_file.updated",
		note: "not settled, as remote_file.created"},
	{topic: "remote_file.shared",
		note: "not settled, as remote_file.created"},
	{topic: "remote_file.removed",
		note: "not settled, as remote_file.created"},

	// ---- product concepts with no Slack event ------------------------------
	{topic: "app.requested", note: "not pinned: app administration has no Slack event; scope_granted and scope_denied are a different fact"},
	{topic: "app.approved", note: "not pinned: app administration has no Slack event"},
	{topic: "app.restricted", note: "not pinned: app administration has no Slack event"},
	{topic: "app.permissions_requested", note: "not pinned: app administration has no Slack event"},
	{topic: "bookmark.created", note: "not pinned: bookmarks postdate the snapshot"},
	{topic: "bookmark.updated", note: "not pinned: bookmarks postdate the snapshot"},
	{topic: "bookmark.removed", note: "not pinned: bookmarks postdate the snapshot"},
	{topic: "call.created", note: "not pinned: calls postdate the snapshot"},
	{topic: "call.updated", note: "not pinned: calls postdate the snapshot"},
	{topic: "call.ended", note: "not pinned: calls postdate the snapshot"},
	{topic: "call.participants_changed", note: "not pinned: calls postdate the snapshot"},
	{topic: "canvas.created", note: "not pinned: canvases postdate the snapshot"},
	{topic: "canvas.updated", note: "not pinned: canvases postdate the snapshot"},
	{topic: "canvas.deleted", note: "not pinned: canvases postdate the snapshot"},
	{topic: "canvas.create_reverted", note: "not pinned: canvases postdate the snapshot"},
	{topic: "canvas.access_set", note: "not pinned: canvases postdate the snapshot"},
	{topic: "canvas.access_deleted", note: "not pinned: canvases postdate the snapshot"},
	{topic: "list.created", note: "not pinned: lists postdate the snapshot"},
	{topic: "list.updated", note: "not pinned: lists postdate the snapshot"},
	{topic: "list.item.created", note: "not pinned: lists postdate the snapshot"},
	{topic: "list.item.updated", note: "not pinned: lists postdate the snapshot"},
	{topic: "list.items.deleted", note: "not pinned: lists postdate the snapshot"},
	{topic: "list.access.set", note: "not pinned: lists postdate the snapshot"},
	{topic: "list.access.deleted", note: "not pinned: lists postdate the snapshot"},
	{topic: "list.download.started", note: "not pinned: lists postdate the snapshot"},
	{topic: "dialog.opened", note: "an interaction payload, not an event; a different transport contract"},
	{topic: "app.home_opened", slack: translated("app_home_opened", appSurfaces, appHomeOpened),
		note: "current first-party app_home_opened reference and @slack/types AppHomeOpenedEvent; app-targeted because opening one app must never fan out to another installed app"},
	{topic: "view.opened", note: "an interaction payload, not an event; app_home_opened is a different fact"},
	{topic: "view.published", note: "an interaction payload, not an event"},
	{topic: "view.pushed", note: "an interaction payload, not an event"},
	{topic: "view.updated", note: "an interaction payload, not an event"},
	{topic: "function_executed", slack: automatic("function_executed", appSurfaces, functionExecuted),
		note: "current function_executed reference: dispatched automatically when a function owned by the target app runs; no event subscription or scope is required"},
	{topic: "workflow.step_configured", note: "not pinned: workflow_step_execute postdates the snapshot"},
	{topic: "workflow.step_completed", note: "not pinned: workflow_step_execute postdates the snapshot"},
	{topic: "workflow.step_failed", note: "not pinned: workflow_step_execute postdates the snapshot"},
	{topic: "saved_item.created", recipient: true,
		note: "current Later is private first-party state with no Slack app event; only the saved item's user may receive the internal live-update signal"},
	{topic: "saved_item.changed", recipient: true,
		note: "current Later is private first-party state with no Slack app event; only the saved item's user may receive the internal live-update signal"},
	{topic: "saved_item.removed", recipient: true,
		note: "current Later is private first-party state with no Slack app event; only the saved item's user may receive the internal live-update signal"},
	{topic: "reminder.created", recipient: true, note: "not pinned: reminders have no Slack event; reminder state is private to its target user"},
	{topic: "reminder.completed", recipient: true, note: "not pinned: reminders have no Slack event; reminder state is private to its target user"},
	{topic: "reminder.deleted", recipient: true, note: "not pinned: reminders have no Slack event; reminder state is private to its target user"},
	{topic: "invite_request.created", note: "not pinned: invite requests have no Slack event"},
	{topic: "invite_request.approved", note: "not pinned: invite requests have no Slack event"},
	{topic: "invite_request.denied", note: "not pinned: invite requests have no Slack event"},
	{topic: "incoming_webhook.enabled", note: "not pinned: integration administration has no Slack event"},
}

// rulesByTopic indexes the table. A duplicate row would make one of the two
// unreachable and silently change a topic's meaning, so it is a build-time
// panic rather than a lookup that happens to win.
var rulesByTopic = func() map[string]topicRule {
	index := make(map[string]topicRule, len(topicRules))
	for _, rule := range topicRules {
		if rule.topic == "" {
			panic("events: topic table has a row with no topic")
		}
		if _, exists := index[rule.topic]; exists {
			panic("events: topic table names " + rule.topic + " twice")
		}
		index[rule.topic] = rule
	}
	return index
}()

// ruleFor returns the row for a topic. A topic with no row is not a failure:
// it is publishable to no Slack client and restricted from nobody, which is the
// safe reading of "a producer added a topic and nobody decided what it means".
func ruleFor(topic string) topicRule {
	return rulesByTopic[topic]
}

// InternalTopic reports whether a topic carries internal worker records rather
// than events an app, a webhook or a browser may receive.
func InternalTopic(topic string) bool {
	return ruleFor(topic).internal
}

// InternalTopics is the same set in the form a SQL IN predicate needs. It
// returns a fresh slice in declaration order, so a storage query cannot
// reorder or truncate the set every other consumer reads.
func InternalTopics() []string {
	return selectTopics(func(rule topicRule) bool { return rule.internal })
}

// RecipientScoped reports whether a topic's payload is addressed to exactly one
// user. Such a record carries that user's content — an ephemeral message
// carries its text, blocks and attachments — so a consumer that cannot scope
// delivery to a recipient must not receive it at all.
func RecipientScoped(topic string) bool {
	return ruleFor(topic).recipient
}

// RecipientScopedTopics is the same set, for a consumer that has to enumerate
// it.
func RecipientScopedTopics() []string {
	return selectTopics(func(rule topicRule) bool { return rule.recipient })
}

func selectTopics(match func(topicRule) bool) []string {
	selected := make([]string, 0, 2)
	for _, rule := range topicRules {
		if match(rule) {
			selected = append(selected, rule.topic)
		}
	}
	return selected
}

// SlackEventType reports the Slack event name a topic maps to. The second
// result is false when the topic maps to no Slack event, which is the answer
// for most of this product's topics.
//
// A true result does not promise the event can be produced today: a mapping
// whose inner shape is unmodelled reports its name here and is still withheld
// by SlackInner. That separation is deliberate — a compatibility report needs
// to distinguish "not a Slack event" from "a Slack event we cannot build yet".
func SlackEventType(topic string) (string, bool) {
	eventType := ruleFor(topic).slack.eventType
	return eventType, eventType != ""
}

// SlackTranslatable reports whether a topic can be rendered as a Slack inner
// event on a surface today.
func SlackTranslatable(topic string, surface Surface) bool {
	rule := ruleFor(topic).slack
	return rule.build != nil && rule.surfaces&surface != 0
}

// AutomaticSlackEvent reports whether Slack dispatches this event because the
// target app owns the invoked resource, independent of manifest event
// subscriptions. Keeping the decision in the topic table prevents HTTP Events
// API and Socket Mode from acquiring different delivery rules.
func AutomaticSlackEvent(eventType string) bool {
	for _, rule := range topicRules {
		if rule.slack.eventType == eventType && rule.slack.automatic {
			return true
		}
	}
	return false
}

// SlackTopics lists every topic the table knows, in sorted order, for a report
// that has to enumerate the decisions rather than sample them.
func SlackTopics() []string {
	topics := make([]string, 0, len(topicRules))
	for _, rule := range topicRules {
		topics = append(topics, rule.topic)
	}
	sort.Strings(topics)
	return topics
}
