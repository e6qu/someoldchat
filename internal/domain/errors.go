package domain

import "errors"

// The application error vocabulary. These sentinels are what the chat service
// returns and what every adapter and transport matches with errors.Is; they
// live here rather than beside the implementation so an adapter can classify
// an error without linking the module that produced it. The gRPC transport
// carries each one across the process boundary by a stable key, so a renamed
// sentinel keeps its key.

var (
	ErrAppNotHosted          = errors.New("application is not Slack-hosted")
	ErrAppDatastoreNotFound  = errors.New("application datastore was not found")
	ErrInvalidDatastoreItem  = errors.New("application datastore item is invalid")
	ErrInvalidDatastoreQuery = errors.New("application datastore query is invalid")
)

var (
	ErrAppInteractionUnavailable = errors.New("application interaction is unavailable")
	ErrSlashCommandNotFound      = errors.New("slash command was not found")
	ErrSlashCommandInThread      = errors.New("slash commands cannot be invoked in threads")
	ErrInvalidAppResponse        = errors.New("application response is invalid")
	ErrInvalidTrigger            = errors.New("trigger_id is invalid")
	// ErrTriggerExchanged and ErrTriggerExpired name a trigger that existed
	// but was already used or outlived its three seconds; Slack reports each
	// outcome separately from an unknown trigger (ErrInvalidTrigger).
	ErrTriggerExchanged = errors.New("trigger_id was already exchanged")
	ErrTriggerExpired   = errors.New("trigger_id expired")
	// ErrViewPushLimit reports a push onto a modal stack that already holds
	// Slack's maximum of three views.
	ErrViewPushLimit = errors.New("modal view stack is full")

	// The response_url refusals Slack distinguishes, which HandleAppResponse
	// returns so the HTTP boundary can answer Slack's status and reason: 400
	// invalid_payload and no_text for a body that could never be applied —
	// refused before a use of the URL is spent — and 404 used_url and
	// expired_url for a URL that cannot be used any more. A URL this system
	// never issued is indistinguishable from one that expired and was purged.
	// They are distinct from ErrInvalidAppResponse, which still reports an
	// app's unusable acknowledgement body, so each keeps its identity across
	// the gRPC seam.
	ErrAppResponsePayloadInvalid = errors.New("response_url payload is not a valid message")
	ErrAppResponseNoText         = errors.New("response_url payload has no text, blocks, or attachments")
	ErrAppResponseURLUsed        = errors.New("response_url has no uses left")
	ErrAppResponseURLExpired     = errors.New("response_url has expired")
)

var (
	ErrAppConfigurationAuthentication = errors.New("app configuration token is invalid")
	ErrAppCredentialKeyUnavailable    = errors.New("application credential encryption key is unavailable")
	ErrInvalidAppManifest             = errors.New("app manifest is invalid")
	// ErrAppNotDistributable refuses public distribution for an app that has no
	// redirect URL: an install has nowhere to return to, so Slack does not let
	// distribution activate until one exists, and neither does this.
	ErrAppNotDistributable = errors.New("app cannot be distributed without a redirect URL")
	// ErrInvalidExternalAuthProvider is a malformed external OAuth provider
	// declaration: a name, client id, and https authorization and token URLs are
	// the minimum an app needs before a member can connect an account.
	ErrInvalidExternalAuthProvider = errors.New("external authentication provider is invalid")
	// ErrExternalAuthConnection is a connect flow that could not complete: the
	// state did not verify, or the provider refused the code exchange.
	ErrExternalAuthConnection = errors.New("external authentication connection failed")
)

var (
	// ErrInvalidAssistantThread is the single sentinel for a malformed
	// assistant write — an empty title, an empty status, no prompts, or more
	// prompts than a pane can offer.
	ErrInvalidAssistantThread = errors.New("invalid assistant thread state")
	// ErrAssistantThreadNotFound distinguishes "this thread has no assistant
	// state" from "this thread does not exist", which the client needs in order
	// to render nothing rather than an error.
	ErrAssistantThreadNotFound = errors.New("assistant thread state not found")
)

// The agents.sessions.* refusals, each one a code the reference pages name.
var (
	// ErrInvalidAgentSession is a malformed session write: a title outside
	// 1-200 characters, an over-long username or an unusable icon_url, a
	// thread_ts that is not a thread root of the conversation, or an
	// initiator who is not in the conversation (invalid_arguments).
	ErrInvalidAgentSession = errors.New("agent session arguments are invalid")
	// ErrInvalidAgentSessionStatus is a status outside active, processing,
	// suspended and closed (invalid_status).
	ErrInvalidAgentSessionStatus = errors.New("agent session status is invalid")
	// ErrAgentSessionThreadRequired is a thread session named without its
	// thread root (thread_ts_required).
	ErrAgentSessionThreadRequired = errors.New("agent session thread_ts is required")
	// ErrAgentSessionThreadNotAllowed is a session channel's session named
	// with a thread; a code channel's session is the channel's own
	// (thread_ts_not_allowed).
	ErrAgentSessionThreadNotAllowed = errors.New("agent session thread_ts is not allowed in a session channel")
	// ErrAgentSessionNotFound is a thread no agent session exists for
	// (session_not_found). It is distinct from store.ErrNotFound, the answer
	// for a conversation the caller cannot see.
	ErrAgentSessionNotFound = errors.New("agent session not found")
	// ErrAgentSessionNotAgent is an app renaming a session it is not an agent
	// of (not_authorized).
	ErrAgentSessionNotAgent = errors.New("app is not an agent of the session")
	// ErrAgentSessionNotStoppable is a stop pressed on a session with no
	// processing agent whose app subscribes to agent_session_stopped — the
	// state in which the client shows no stop control at all.
	ErrAgentSessionNotStoppable = errors.New("agent session has no processing agent that can be stopped")
)

// ErrInvalidBlocks reports message blocks that are not valid Block Kit —
// Slack's invalid_blocks. It is distinct from ErrInvalidMessage (no_text),
// which reports a message with nothing to show.
var ErrInvalidBlocks = errors.New("blocks are not valid Block Kit")

var (
	// ErrInvalidSharedInvite refuses a malformed invitation.
	ErrInvalidSharedInvite = errors.New("shared invitation is invalid")
	// ErrExternalInviteNotPermitted refuses a connected organization that a host
	// has restricted from inviting further organizations into a conversation.
	ErrExternalInviteNotPermitted = errors.New("external invitations are not permitted for this organization")
	// ErrSharedInviteSettled refuses a decision on an invitation that already
	// has one. It is distinct from a malformed request: the caller did nothing
	// wrong, someone else simply got there first.
	ErrSharedInviteSettled = errors.New("shared invitation has already been settled")
	// ErrSlackConnectFull refuses the place rather than promising one that is
	// not there.
	ErrSlackConnectFull = errors.New("conversation already holds the maximum number of organizations")
)

var (
	ErrInvalidMessageStream       = errors.New("message stream arguments are invalid")
	ErrInvalidStreamChunks        = errors.New("message stream chunks are invalid")
	ErrMessageNotStreaming        = errors.New("message is not in streaming state")
	ErrMessageNotOwnedByApp       = errors.New("message is not owned by app")
	ErrMissingStreamRecipientTeam = errors.New("message stream recipient team is required")
	ErrMissingStreamRecipientUser = errors.New("message stream recipient user is required")
)

var (
	ErrInvalidMessage        = errors.New("message text and conversation are required")
	ErrInvalidTimestamp      = errors.New("message timestamp is invalid")
	ErrMessageNotOwned       = errors.New("message is not owned by user")
	ErrMessageAlreadyDeleted = errors.New("message is already deleted")
	// ErrThreadNotFound is a thread_ts that names no live message of the
	// conversation. It is not the conversation's absence: reporting it as
	// channel_not_found sent the caller to check a channel that exists.
	ErrThreadNotFound = errors.New("thread parent message not found")
	// ErrRecipientNotInConversation is an ephemeral message addressed to
	// someone who cannot read the conversation it would appear in.
	ErrRecipientNotInConversation = errors.New("ephemeral recipient is not in the conversation")
	// The message describes the class rather than one member of it. It used to
	// read "conversation name is invalid", which is right for a rejected name
	// and wrong for the other forty-odd sites that raise it — a foreign team
	// id, a kind that cannot carry the operation, a conversation that is not a
	// channel — each of which told the caller to go and look at a name.
	ErrInvalidConversation = errors.New("conversation is invalid for this operation")
	// ErrBarrieredFromMember is refusal by an information barrier. It is its
	// own error because "you may not reach this person" is a different fact
	// from a malformed request or a member who is not here, and an
	// administrator reading a support question needs to tell them apart.
	ErrBarrieredFromMember      = errors.New("an information barrier separates these members")
	ErrInvalidWorkspace         = errors.New("workspace settings are invalid")
	ErrInvalidConversationPrefs = errors.New("conversation preferences are invalid")
	// ErrConversationPostingRestricted is a member being refused a post their
	// membership allows but the channel's posting permissions do not. It is
	// distinct from ErrNotInConversation on purpose: the member can read the
	// channel and see the refusal is about permission, which is what Slack's
	// restricted_action reports.
	ErrConversationPostingRestricted = errors.New("channel posting is restricted")
	ErrInvalidReaction               = errors.New("reaction name is invalid")
	ErrBlobUnavailable               = errors.New("blob storage is unavailable")
	ErrInvalidFile                   = errors.New("file metadata is invalid")
	ErrInvalidSearch                 = errors.New("search query is invalid")
	ErrInvalidProfile                = errors.New("user profile is invalid")
	ErrInvalidProfileField           = errors.New("custom profile field definition is invalid")
	ErrInvalidScheduledStatus        = errors.New("scheduled status is invalid")
	ErrScheduledStatusLimit          = errors.New("five statuses are already scheduled")
	ErrInvalidPresence               = errors.New("user presence is invalid")
	ErrInvalidSnooze                 = errors.New("snooze duration must be between 1 and 1440 minutes")
	ErrInvalidReminder               = errors.New("reminder text, user, and time are required")
	ErrInvalidReminderRequest        = errors.New("reminder arguments are invalid")
	// ErrInvalidLaterReminder is ErrInvalidReminderRequest as a chat process
	// from before To-dos names it across the seam (its key is wire contract).
	// Nothing in this release returns it; the adapters that classify an
	// invalid reminder accept it beside ErrInvalidReminderRequest during a
	// rolling deployment, and it goes once no such process can be running.
	ErrInvalidLaterReminder     = errors.New("reminder arguments are invalid (reported by a chat process from before To-dos)")
	ErrInvalidTodo              = errors.New("to-do arguments are invalid")
	ErrInvalidActivitySavedView = errors.New("activity saved view arguments are invalid")
	ErrInvalidSidebarSection    = errors.New("sidebar section arguments are invalid")
	ErrReminderTimeInPast       = errors.New("reminder time is in the past")
	ErrReminderRecurring        = errors.New("a recurring reminder cannot be marked complete")
	ErrReminderOwnedByOther     = errors.New("a reminder can only be completed by the member it is for")
	// The sentinels below each carry one Slack error code their operation's
	// contract declares, so the handler can name the failure exactly rather
	// than folding it into a generic invalid or not-found answer.
	ErrSnoozeNotActive       = errors.New("no snooze is active")                          // dnd.endSnooze: snooze_not_active
	ErrSnoozeTooLong         = errors.New("a snooze may last at most 1440 minutes")       // dnd.setSnooze: too_long
	ErrReminderUnparseable   = errors.New("reminder time could not be parsed")            // reminders.add: cannot_parse
	ErrNotStarred            = errors.New("the item is not starred")                      // stars.remove: not_starred
	ErrUserGroupNameTaken    = errors.New("a user group with this name already exists")   // name_already_exists
	ErrUserGroupHandleTaken  = errors.New("a user group with this handle already exists") // handle_already_exists
	ErrInvalidUserGroupUsers = errors.New("a user group member is not in the workspace")  // usergroups.users.update: invalid_users
	ErrCannotUnfurlURL       = errors.New("the URL does not appear in the message")       // chat.unfurl: cannot_unfurl_url
	ErrScheduledTimeInPast   = errors.New("scheduled message time is in the past")
	ErrScheduledTimeTooFar   = errors.New("scheduled message time is more than 120 days away")
	ErrScheduledTooMany      = errors.New("too many messages are scheduled in the channel window")
	ErrInvalidUserGroup      = errors.New("user group name, handle, and members are invalid")
	ErrInvalidCall           = errors.New("call external id and join URL are required")
	ErrInvalidEphemeral      = errors.New("ephemeral message recipient, conversation, and text are required")
	ErrInvalidAccessLog      = errors.New("access log fields are invalid")
	ErrInvalidEmoji          = errors.New("custom emoji name or URL is invalid")
	ErrEmojiAlreadyExists    = errors.New("custom emoji already exists")
	// ErrInvalidEmojiImage is an uploaded custom emoji image that is not a
	// PNG, GIF, or JPEG of at most MaxCustomEmojiBytes, Slack's limit.
	ErrInvalidEmojiImage    = errors.New("custom emoji image must be a PNG, GIF, or JPEG of at most 128 KB")
	ErrInvalidRemoteFile    = errors.New("remote file metadata is invalid")
	ErrInvalidInviteRequest = errors.New("invite request is invalid")
	// ErrInvitationExpired is distinct from ErrInvalidInviteRequest because the
	// person reading it needs to know whether to ask for a new invitation or
	// to check which address they signed in with.
	ErrInvitationExpired = errors.New("invitation has expired")
	// ErrHuddleNotOwned refuses to end a huddle on everyone else's behalf.
	ErrHuddleNotOwned      = errors.New("huddle is not owned by this actor")
	ErrInvalidAppApproval  = errors.New("app approval is invalid")
	ErrInvalidView         = errors.New("view payload is invalid")
	ErrAppHomeNotEnabled   = errors.New("app home tab is not enabled")
	ErrInvalidList         = errors.New("list payload is invalid")
	ErrInvalidListTemplate = errors.New("list template payload is invalid")
	ErrInvalidEntity       = errors.New("entity payload is invalid")
	ErrInvalidWorkflowStep = errors.New("workflow step payload is invalid")
	// ErrFunctionUseRestricted refuses a builder who uses a function an
	// administrator has restricted to specific people or to the app's
	// collaborators. It rides the same restricted_action code the posting policy
	// uses, because it is the same kind of refusal: the actor may act in general
	// but not with this particular resource.
	ErrFunctionUseRestricted = errors.New("this function is restricted from you")
	// ErrTriggerTypeRestricted refuses a builder who creates a trigger of a type
	// an administrator has restricted. Same restricted_action shape as a function
	// restriction: the actor may build in general, but not with this trigger type.
	ErrTriggerTypeRestricted     = errors.New("this trigger type is restricted from you")
	ErrWorkflowPermissionDenied  = errors.New("workflow trigger is not available to this actor")
	ErrFunctionAccessDenied      = errors.New("actor does not have access to this function execution")
	ErrFunctionNotRunning        = errors.New("function execution is not running")
	ErrAutomationUserNotFound    = errors.New("automation permission user was not found")
	ErrAutomationChannelNotFound = errors.New("automation permission channel was not found")
	ErrAutomationTeamNotFound    = errors.New("automation permission workspace was not found")
	ErrAutomationOrgNotFound     = errors.New("automation permission organization was not found")
	ErrAutomationEntitiesEmpty   = errors.New("automation named entities cannot be empty")
	ErrWorkflowFunctionNotFound  = errors.New("workflow function was not found")
	ErrInvalidDialog             = errors.New("dialog payload is invalid")
	// ErrAppMissingActionURL is dialog.open for an app that could never
	// receive the dialog's submission: it has neither an interactivity
	// request URL nor Socket Mode.
	ErrAppMissingActionURL = errors.New("app has no interactivity request URL")
	ErrInvalidBot          = errors.New("bot identifier is required")
	ErrInvalidMigration    = errors.New("migration user identifiers are invalid")
	ErrInvalidOAuth        = errors.New("oauth authorization is invalid")
	ErrInvalidOAuthClient  = errors.New("oauth client is invalid")
	// ErrBadOAuthClientSecret is a known client presenting the wrong secret.
	// Slack reports it as bad_client_secret, distinct from an unknown
	// client's invalid_client_id.
	ErrBadOAuthClientSecret   = errors.New("oauth client secret is wrong")
	ErrOAuthAppMismatch       = errors.New("oauth client and token app do not match")
	ErrInvalidIntegrationLogs = errors.New("integration log arguments are invalid")
	ErrInvalidBookmark        = errors.New("bookmark title, type, and link are invalid")
	ErrInvalidCanvas          = errors.New("canvas content or access arguments are invalid")
	// ErrCanvasTooLarge is an edit that would make a canvas longer than
	// CanvasMarkdownLimit, or its collaborative text larger than
	// CanvasTextStateLimit.
	ErrCanvasTooLarge              = errors.New("canvas is larger than a canvas can be")
	ErrInvalidExternalUpload       = errors.New("external upload is invalid")
	ErrConversationAlreadyArchived = errors.New("conversation is already archived")
	ErrConversationNotArchived     = errors.New("conversation is not archived")
	ErrCannotArchiveDefault        = errors.New("required conversation cannot be archived")
	ErrCannotLeaveDefault          = errors.New("required conversation cannot be left")
	ErrCannotInviteSelf            = errors.New("a conversation member cannot invite themselves")
	// ErrConversationArchived refuses a change to an archived conversation that
	// is not its unarchiving: Slack answers is_archived for a topic, a purpose,
	// or a member leaving.
	ErrConversationArchived = errors.New("conversation is archived")
	// ErrConversationTextTooLong refuses a topic or purpose longer than
	// MaxConversationTextLength characters; Slack answers too_long.
	ErrConversationTextTooLong = errors.New("conversation topic or purpose is too long")
	// ErrCannotKickSelf refuses conversations.kick naming the caller, which
	// Slack answers cant_kick_self rather than treating it as a leave.
	ErrCannotKickSelf = errors.New("a member cannot remove themselves")
	// ErrCannotKickFromDefault refuses removing anyone from a required channel,
	// which nobody may leave; Slack answers cant_kick_from_general.
	ErrCannotKickFromDefault = errors.New("members cannot be removed from a required conversation")
	// ErrUserNotFound names the person an operation was about, where the
	// conversation it acts on is also a thing that could be missing. A single
	// store.ErrNotFound answered channel_not_found for a user who does not
	// exist, which sends the caller looking for the wrong identifier.
	ErrUserNotFound = errors.New("user was not found")

	// ErrNotWorkspaceAdmin refuses an administrative operation to an actor whose
	// durable workspace membership is not an administrator or an owner.
	//
	// It is distinct from store.ErrNotFound, which every administrative method
	// already returned for an actor who is not a member of the workspace at all.
	// Collapsing the two would tell an authenticated member that the workspace
	// does not exist, and would hide the real reason from the operator reading the
	// audit trail.
	ErrNotWorkspaceAdmin = errors.New("actor is not a workspace administrator")
	// ErrUserIsRestricted and ErrUserIsUltraRestricted are the two guest tiers
	// refusing an action Slack keeps away from guests. They are separate
	// sentinels because the pinned enums for conversations.join and
	// conversations.invite declare both codes: a caller told
	// user_is_ultra_restricted knows the person is confined to one channel,
	// which user_is_restricted does not say.
	ErrUserIsRestricted      = errors.New("actor is a guest")
	ErrUserIsUltraRestricted = errors.New("actor is a single-channel guest")

	// ErrNotInConversation refuses an operation whose Slack contract requires the
	// actor to be a member of the conversation it names.
	//
	// authorizeConversation checks membership only for a PRIVATE conversation,
	// because a public channel is readable by every member of the workspace. That
	// is right for a read and wrong for the ten operations whose pinned enums
	// declare `not_in_channel` — chat.postMessage, chat.meMessage,
	// chat.scheduleMessage, conversations.invite, conversations.kick,
	// conversations.leave, conversations.mark, conversations.rename,
	// conversations.setPurpose and conversations.setTopic — all of which act on
	// the channel as one of its members. Before this, a member could post into,
	// rename or set the topic of a public channel they had never joined, and
	// `not_in_channel` was declared by nine enums and produced nowhere in the
	// repository.
	//
	// It is distinct from store.ErrNotFound, which stays the answer for a channel
	// the actor cannot see at all: naming a public channel proves nothing about
	// the actor, so the refusal may say what it is.
	ErrNotInConversation = errors.New("actor is not a member of the conversation")

	// ErrLastWorkspaceOwner refused a change that would leave a workspace with
	// no owner. The primary owner made it unreachable: every workspace that
	// has an owner has a primary owner, who can be neither demoted nor removed.
	// It stays because its key is wire contract a rolling deployment still
	// answers with.
	ErrLastWorkspaceOwner = errors.New("workspace must retain an owner")

	// ErrPrimaryOwner refuses demoting or removing a workspace's primary
	// owner, Slack's cannot_modify_primary_owner. The primary owner hands the
	// role to another member first, which keeps every workspace that has an
	// owner able to appoint administrators.
	ErrPrimaryOwner = errors.New("the primary owner cannot be modified")
)

var (
	// ErrInvalidRetentionDuration refuses a duration outside Slack's documented
	// range: an integer greater than zero and below 36500 days.
	ErrInvalidRetentionDuration = errors.New("retention duration is invalid")
	// ErrRetentionNotSupported refuses a conversation type Slack will not apply
	// a custom retention policy to.
	ErrRetentionNotSupported = errors.New("conversation type does not support a retention policy")
	// ErrInvalidWorkspacePolicy refuses a workspace policy naming a value the
	// product does not apply.
	ErrInvalidWorkspacePolicy = errors.New("workspace policy is invalid")
	// ErrPrivateChannelCreationRestricted refuses a member the workspace's
	// "who can create private channels" policy leaves out, whether they create
	// the channel or convert a group DM into one. It is Slack's
	// restricted_action: a team preference prevents the member, who may act in
	// general, from doing this.
	ErrPrivateChannelCreationRestricted = errors.New("a workspace policy restricts who can create private channels")
)

// ErrViewFilesInvalid reports a file_input value the element does not accept:
// a file that is not the submitting member's own upload, more files than
// max_files, or a type outside filetypes. The client checks the same
// constraints first; this is the authoritative check.
var ErrViewFilesInvalid = errors.New("view file input value is invalid")

// ErrInvalidTriggerConfig reports a trigger configuration that cannot execute:
// an unknown schedule frequency, an unbound channel or list, or a malformed
// JSON object. It is distinct from ErrInvalidWorkflowStep so the Slack HTTP
// boundary can keep its own error mapping per resource.
var ErrInvalidTriggerConfig = errors.New("workflow trigger configuration is invalid")

// ErrWebhookTriggerSecret reports a webhook invocation whose path secret does
// not match the trigger's stored hash. The HTTP boundary answers it with the
// same indistinguishable 404 as an unknown trigger.
var ErrWebhookTriggerSecret = errors.New("webhook trigger secret does not match")

// The admin.usergroups.* organization methods name these failures with codes
// of their own.
var (
	// ErrUnparseableUserGroupFile is admin.usergroups.uploadUsers'
	// unable_to_parse_csv: the file is not a "member id, email" CSV.
	ErrUnparseableUserGroupFile = errors.New("the uploaded user CSV could not be parsed")
	// ErrNoValidUserGroupUsers is admin.usergroups.uploadUsers' no_valid_users:
	// the CSV names nobody who can join the group.
	ErrNoValidUserGroupUsers = errors.New("the uploaded user CSV names no user that can join")
	// ErrUserGroupNeedsHandle is admin.usergroups.update's
	// visible_group_needs_handle: a visible group's handle was removed.
	ErrUserGroupNeedsHandle = errors.New("a visible user group needs a handle")
)

// The app access control refusals of admin.apps.permissions.*,
// admin.apps.mcp.servers.permissions.* and apps.managed.permissions.set. Each
// is the cause one documented code names, so each crosses the gRPC seam as
// itself rather than as a generic invalid argument.
var (
	// ErrInvalidAppPermission is a malformed request: an argument the method
	// requires is missing or a list is longer than its documented maximum.
	ErrInvalidAppPermission = errors.New("app permission request is invalid")
	// ErrAppAccessListNotFound is an add or remove against an app nobody has set an
	// access control list on.
	ErrAppAccessListNotFound = errors.New("app has no access control list")
	// ErrAppPermissionType is a permission type that is not one of the
	// documented values, or one that does not take the entities the request
	// names: users and user groups belong to a named_entities list.
	ErrAppPermissionType = errors.New("permission type does not allow this change")
	// ErrChannelRestrictionMode is a channel restriction mode that is not one
	// of the documented values, or a channel list given to a mode without one.
	ErrChannelRestrictionMode = errors.New("channel restriction mode is invalid")
	// ErrChannelRestrictionIDsRequired is a listing mode given no channels.
	ErrChannelRestrictionIDsRequired = errors.New("channel restriction mode requires channel_ids")
	// ErrChannelRestrictionRequiresAppAccess is a channel restriction on an
	// app nobody may use, which can be used in no channel at all.
	ErrChannelRestrictionRequiresAppAccess = errors.New("channel restriction requires an app that somebody may use")
	// ErrNamedEntitiesEmpty is a named list that would name nobody.
	ErrNamedEntitiesEmpty = errors.New("named entities cannot be empty")
	// ErrNoValidNamedEntities is a named list none of whose entities exist.
	ErrNoValidNamedEntities = errors.New("none of the named entities is valid")
	// ErrTooManyNamedEntities is a named list longer than its documented maximum.
	ErrTooManyNamedEntities = errors.New("too many named entities")
	// ErrNamedUserGroupNotFound is a named user group that does not exist here.
	ErrNamedUserGroupNotFound = errors.New("named user group was not found")
	// ErrRestrictedChannelNotFound is a channel restriction naming a channel
	// that does not exist here.
	ErrRestrictedChannelNotFound = errors.New("restricted channel was not found")
	// ErrServerNotFound is a server the app's manifest does not declare.
	ErrServerNotFound = errors.New("MCP server was not found for this app")
	// ErrServerPermissionBroaderThanApp is a server rule that would admit somebody
	// the app-level access control list keeps out.
	ErrServerPermissionBroaderThanApp = errors.New("MCP server permission is broader than the app permission")
	// ErrServerPermissionOutOfAppScope is a server rule naming somebody the
	// app-level named list does not include.
	ErrServerPermissionOutOfAppScope = errors.New("MCP server permission names entities outside the app permission")
	// ErrAppNotManaged is an app no manager app manages.
	ErrAppNotManaged = errors.New("app is not managed by a manager app")
	// ErrAppUseRestricted is a member using an app its access control list
	// does not admit, or using it in a channel its channel restriction leaves
	// out.
	ErrAppUseRestricted = errors.New("app access control does not admit this use")
)

// The admin.conversations.bulkSetProperties refusals, one per code its
// reference declares for a property or a channel list that cannot be applied.
var (
	// ErrInvalidConversationProperty is a property argument that is not a JSON
	// object naming one property with a value of that property's type
	// (invalid_arguments).
	ErrInvalidConversationProperty = errors.New("channel property is not a valid JSON object")
	// ErrTooManyConversationProperties is an object naming more than one
	// property; only one may be updated at a time (too_many_properties).
	ErrTooManyConversationProperties = errors.New("only one channel property can be updated at a time")
	// ErrConversationPropertyNotAllowed names a property that may not be
	// updated (property_not_allowed).
	ErrConversationPropertyNotAllowed = errors.New("channel property is not allowed to be updated")
	// ErrNoValidChannels is a request none of whose channels is a channel of
	// this workspace (no_valid_channels).
	ErrNoValidChannels = errors.New("none of the channels is valid")
)

// The oauth.v2.beginShortTokenRotation and completeShortTokenRotation
// refusals that are about the token being rotated rather than about the
// client presenting it.
var (
	// ErrTokenTypeNotRotatable is a credential that is not an xoxp user token
	// (not_allowed_token_type).
	ErrTokenTypeNotRotatable = errors.New("only a user token can have its short secret rotated")
	// ErrTokenSecretTooLong is a token whose secret is already the full
	// 32 characters (token_too_long).
	ErrTokenSecretTooLong = errors.New("token secret is not short enough to be rotated")
	// ErrShortTokenRotationNotFound is a completion with no pending rotation
	// for the token, or one begun more than ten minutes ago
	// (rotation_not_found).
	ErrShortTokenRotationNotFound = errors.New("no pending short token rotation was found")
	// ErrShortTokenRotationMismatch is a completion naming a new token other
	// than the one the pending rotation issued (invalid_token).
	ErrShortTokenRotationMismatch = errors.New("new token does not match the pending rotation")
)
