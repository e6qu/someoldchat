package slack

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blockkit"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/slackemoji"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Handler struct {
	Messages      chatapi.Service
	Authenticator auth.Authenticator
	SocketMode    socketmode.Service
	SocketAuth    auth.Authenticator
	// Limiter enforces the Web API rate-limiting contract over every /api/
	// route when set. Production wiring sets it; a zero Handler serves
	// unlimited, which is what the package's own request-shaped tests and the
	// SDK qualification fixture rely on.
	Limiter *RateLimiter
}

var errAccessLogging = errors.New("access logging failed")

const oauthTokenLifetime = 12 * time.Hour

func NewHandler(messages chatapi.Service, authenticator auth.Authenticator) (Handler, error) {
	if messages == nil {
		return Handler{}, errors.New("Slack API requires a chat service")
	}
	if authenticator == nil {
		return Handler{}, errors.New("Slack API requires an authenticator")
	}
	return Handler{Messages: messages, Authenticator: authenticator}, nil
}

func (h Handler) Register(mux *http.ServeMux) {
	if h.Limiter != nil {
		// Every route registers on an inner mux and the limiter fronts the
		// whole /api/ subtree, so a limited request is answered before any
		// route handler — including the unknown-method catch-all — runs.
		inner := http.NewServeMux()
		unlimited := h
		unlimited.Limiter = nil
		unlimited.Register(inner)
		mux.Handle("/api/", h.Limiter.Middleware(inner))
		return
	}
	mux.HandleFunc("GET /api/api.test", h.apiTest)
	mux.HandleFunc("POST /api/api.test", h.apiTest)
	mux.HandleFunc("POST /api/blocks.validate", h.blocksValidate)
	mux.HandleFunc("POST /api/auth.test", h.authTest)
	mux.HandleFunc("GET /api/auth.test", h.authTest)
	mux.HandleFunc("POST /api/auth.teams.list", h.authTeamsList)
	mux.HandleFunc("GET /api/auth.teams.list", h.authTeamsList)
	mux.HandleFunc("GET /api/oauth.access", h.oauthAccess)
	mux.HandleFunc("POST /api/oauth.access", h.oauthAccess)
	mux.HandleFunc("GET /api/oauth.token", h.oauthAccess)
	mux.HandleFunc("POST /api/oauth.token", h.oauthAccess)
	mux.HandleFunc("GET /api/oauth.v2.access", h.oauthV2Access)
	mux.HandleFunc("POST /api/oauth.v2.access", h.oauthV2Access)
	mux.HandleFunc("GET /api/oauth.v2.exchange", h.oauthV2ExchangeToken)
	mux.HandleFunc("POST /api/oauth.v2.exchange", h.oauthV2ExchangeToken)
	mux.HandleFunc("GET /api/oauth.v2.user.access", h.oauthV2UserAccess)
	mux.HandleFunc("POST /api/oauth.v2.user.access", h.oauthV2UserAccess)
	mux.HandleFunc("GET /api/auth.revoke", h.authRevoke)
	mux.HandleFunc("POST /api/auth.revoke", h.authRevoke)
	mux.HandleFunc("GET /api/apps.permissions.info", h.appsPermissionsInfo)
	mux.HandleFunc("POST /api/apps.permissions.info", h.appsPermissionsInfo)
	mux.HandleFunc("GET /api/apps.permissions.scopes.list", h.appsPermissionsScopesList)
	mux.HandleFunc("POST /api/apps.permissions.scopes.list", h.appsPermissionsScopesList)
	mux.HandleFunc("GET /api/apps.permissions.resources.list", h.appsPermissionsResourcesList)
	mux.HandleFunc("POST /api/apps.permissions.resources.list", h.appsPermissionsResourcesList)
	mux.HandleFunc("GET /api/apps.permissions.users.list", h.appsPermissionsUsersList)
	mux.HandleFunc("POST /api/apps.permissions.users.list", h.appsPermissionsUsersList)
	mux.HandleFunc("GET /api/apps.permissions.request", h.appsPermissionsRequest)
	mux.HandleFunc("POST /api/apps.permissions.request", h.appsPermissionsRequest)
	mux.HandleFunc("GET /api/apps.permissions.users.request", h.appsPermissionsUsersRequest)
	mux.HandleFunc("POST /api/apps.permissions.users.request", h.appsPermissionsUsersRequest)
	mux.HandleFunc("GET /api/views.open", h.viewsOpen)
	mux.HandleFunc("POST /api/views.open", h.viewsOpen)
	mux.HandleFunc("GET /api/views.publish", h.viewsPublish)
	mux.HandleFunc("POST /api/views.publish", h.viewsPublish)
	mux.HandleFunc("GET /api/views.push", h.viewsPush)
	mux.HandleFunc("POST /api/views.push", h.viewsPush)
	mux.HandleFunc("GET /api/views.update", h.viewsUpdate)
	mux.HandleFunc("POST /api/views.update", h.viewsUpdate)
	mux.HandleFunc("GET /api/workflows.stepCompleted", h.workflowStepCompleted)
	mux.HandleFunc("POST /api/workflows.stepCompleted", h.workflowStepCompleted)
	mux.HandleFunc("GET /api/workflows.stepFailed", h.workflowStepFailed)
	mux.HandleFunc("POST /api/workflows.stepFailed", h.workflowStepFailed)
	mux.HandleFunc("GET /api/workflows.updateStep", h.workflowUpdateStep)
	mux.HandleFunc("POST /api/workflows.updateStep", h.workflowUpdateStep)
	mux.HandleFunc("POST /api/functions.completeSuccess", h.functionsCompleteSuccess)
	mux.HandleFunc("POST /api/functions.completeError", h.functionsCompleteError)
	mux.HandleFunc("POST /api/functions.distributions.permissions.add", h.functionsDistributionsPermissionsAdd)
	mux.HandleFunc("POST /api/functions.distributions.permissions.list", h.functionsDistributionsPermissionsList)
	mux.HandleFunc("POST /api/functions.distributions.permissions.remove", h.functionsDistributionsPermissionsRemove)
	mux.HandleFunc("POST /api/functions.distributions.permissions.set", h.functionsDistributionsPermissionsSet)
	mux.HandleFunc("POST /api/functions.workflows.steps.list", h.functionsWorkflowsStepsList)
	mux.HandleFunc("POST /api/workflows.featured.add", h.workflowsFeaturedAdd)
	mux.HandleFunc("POST /api/workflows.featured.list", h.workflowsFeaturedList)
	mux.HandleFunc("POST /api/workflows.featured.remove", h.workflowsFeaturedRemove)
	mux.HandleFunc("POST /api/workflows.featured.set", h.workflowsFeaturedSet)
	mux.HandleFunc("POST /api/workflows.triggers.permissions.add", h.workflowsTriggersPermissionsAdd)
	mux.HandleFunc("POST /api/workflows.triggers.permissions.list", h.workflowsTriggersPermissionsList)
	mux.HandleFunc("POST /api/workflows.triggers.permissions.remove", h.workflowsTriggersPermissionsRemove)
	mux.HandleFunc("POST /api/workflows.triggers.permissions.set", h.workflowsTriggersPermissionsSet)
	mux.HandleFunc("GET /api/dialog.open", h.dialogOpen)
	mux.HandleFunc("POST /api/dialog.open", h.dialogOpen)
	mux.HandleFunc("GET /api/apps.event.authorizations.list", h.appsEventAuthorizationsList)
	mux.HandleFunc("POST /api/apps.event.authorizations.list", h.appsEventAuthorizationsList)
	mux.HandleFunc("POST /api/apps.manifest.create", h.appsManifestCreate)
	mux.HandleFunc("POST /api/apps.manifest.delete", h.appsManifestDelete)
	mux.HandleFunc("POST /api/apps.manifest.export", h.appsManifestExport)
	mux.HandleFunc("POST /api/apps.manifest.update", h.appsManifestUpdate)
	mux.HandleFunc("POST /api/apps.manifest.validate", h.appsManifestValidate)
	mux.HandleFunc("POST /api/apps.uninstall", h.appsUninstall)
	mux.HandleFunc("GET /api/apps.uninstall", h.appsUninstall)
	mux.HandleFunc("POST /api/tooling.tokens.rotate", h.toolingTokensRotate)
	mux.HandleFunc("GET /api/team.info", h.teamInfo)
	mux.HandleFunc("POST /api/team.info", h.teamInfo)
	mux.HandleFunc("GET /api/team.preferences.list", h.teamPreferencesList)
	mux.HandleFunc("POST /api/team.preferences.list", h.teamPreferencesList)
	mux.HandleFunc("GET /api/rtm.connect", h.rtmConnect)
	mux.HandleFunc("POST /api/rtm.connect", h.rtmConnect)
	mux.HandleFunc("GET /api/rtm.start", h.rtmConnect)
	mux.HandleFunc("POST /api/rtm.start", h.rtmConnect)
	mux.HandleFunc("POST /api/apps.connections.open", h.appsConnectionsOpen)
	mux.HandleFunc("GET /api/apps.connections.open", h.appsConnectionsOpen)
	mux.HandleFunc("POST /api/apps.datastore.put", h.appsDatastorePut)
	mux.HandleFunc("POST /api/apps.datastore.update", h.appsDatastoreUpdate)
	mux.HandleFunc("POST /api/apps.datastore.get", h.appsDatastoreGet)
	mux.HandleFunc("POST /api/apps.datastore.query", h.appsDatastoreQuery)
	mux.HandleFunc("POST /api/apps.datastore.count", h.appsDatastoreCount)
	mux.HandleFunc("POST /api/apps.datastore.delete", h.appsDatastoreDelete)
	mux.HandleFunc("POST /api/apps.datastore.bulkPut", h.appsDatastoreBulkPut)
	mux.HandleFunc("POST /api/apps.datastore.bulkGet", h.appsDatastoreBulkGet)
	mux.HandleFunc("POST /api/apps.datastore.bulkDelete", h.appsDatastoreBulkDelete)
	mux.HandleFunc("GET /api/bots.info", h.botsInfo)
	mux.HandleFunc("POST /api/bots.info", h.botsInfo)
	mux.HandleFunc("GET /api/migration.exchange", h.migrationExchange)
	mux.HandleFunc("POST /api/migration.exchange", h.migrationExchange)
	mux.HandleFunc("GET /api/team.billableInfo", h.teamBillableInfo)
	mux.HandleFunc("POST /api/team.billableInfo", h.teamBillableInfo)
	mux.HandleFunc("GET /api/team.profile.get", h.teamProfileGet)
	mux.HandleFunc("POST /api/team.profile.get", h.teamProfileGet)
	mux.HandleFunc("GET /api/team.accessLogs", h.accessLogs)
	mux.HandleFunc("POST /api/team.accessLogs", h.accessLogs)
	mux.HandleFunc("GET /api/team.integrationLogs", h.integrationLogs)
	mux.HandleFunc("POST /api/team.integrationLogs", h.integrationLogs)
	mux.HandleFunc("GET /api/admin.users.list", h.adminUsersList)
	mux.HandleFunc("POST /api/admin.users.list", h.adminUsersList)
	mux.HandleFunc("POST /api/admin.users.remove", h.adminUsersRemove)
	mux.HandleFunc("POST /api/admin.users.session.invalidate", h.adminUsersSessionInvalidate)
	mux.HandleFunc("POST /api/admin.users.session.reset", h.adminUsersSessionReset)
	mux.HandleFunc("POST /api/admin.users.setAdmin", h.adminUsersSetAdmin)
	mux.HandleFunc("POST /api/admin.users.setOwner", h.adminUsersSetOwner)
	mux.HandleFunc("POST /api/admin.users.setRegular", h.adminUsersSetRegular)
	mux.HandleFunc("POST /api/admin.users.setExpiration", h.adminUsersSetExpiration)
	mux.HandleFunc("POST /api/admin.users.invite", h.adminUsersInvite)
	mux.HandleFunc("POST /api/admin.users.assign", h.adminUsersAssign)
	mux.HandleFunc("POST /api/admin.inviteRequests.approve", h.adminInviteRequestApprove)
	mux.HandleFunc("GET /api/admin.inviteRequests.approved.list", h.adminInviteRequestsApprovedList)
	mux.HandleFunc("POST /api/admin.inviteRequests.approved.list", h.adminInviteRequestsApprovedList)
	mux.HandleFunc("GET /api/admin.inviteRequests.denied.list", h.adminInviteRequestsDeniedList)
	mux.HandleFunc("POST /api/admin.inviteRequests.denied.list", h.adminInviteRequestsDeniedList)
	mux.HandleFunc("POST /api/admin.inviteRequests.deny", h.adminInviteRequestDeny)
	mux.HandleFunc("GET /api/admin.inviteRequests.list", h.adminInviteRequestsList)
	mux.HandleFunc("POST /api/admin.inviteRequests.list", h.adminInviteRequestsList)
	mux.HandleFunc("POST /api/admin.apps.approve", h.adminAppApprove)
	mux.HandleFunc("GET /api/admin.apps.approved.list", h.adminAppsApprovedList)
	mux.HandleFunc("POST /api/admin.apps.approved.list", h.adminAppsApprovedList)
	mux.HandleFunc("GET /api/admin.apps.requests.list", h.adminAppsRequestsList)
	mux.HandleFunc("POST /api/admin.apps.requests.list", h.adminAppsRequestsList)
	mux.HandleFunc("POST /api/admin.apps.restrict", h.adminAppRestrict)
	mux.HandleFunc("GET /api/admin.apps.restricted.list", h.adminAppsRestrictedList)
	mux.HandleFunc("POST /api/admin.apps.restricted.list", h.adminAppsRestrictedList)
	mux.HandleFunc("POST /api/admin.conversations.rename", h.adminConversationRename)
	mux.HandleFunc("POST /api/admin.conversations.create", h.adminConversationCreate)
	mux.HandleFunc("POST /api/admin.conversations.archive", h.adminConversationArchive)
	mux.HandleFunc("POST /api/admin.conversations.unarchive", h.adminConversationUnarchive)
	mux.HandleFunc("POST /api/admin.conversations.delete", h.adminConversationDelete)
	mux.HandleFunc("POST /api/admin.conversations.restrictAccess.addGroup", h.adminConversationAccessGroupAdd)
	mux.HandleFunc("GET /api/admin.conversations.restrictAccess.listGroups", h.adminConversationAccessGroupsList)
	mux.HandleFunc("POST /api/admin.conversations.restrictAccess.listGroups", h.adminConversationAccessGroupsList)
	mux.HandleFunc("POST /api/admin.conversations.restrictAccess.removeGroup", h.adminConversationAccessGroupRemove)
	mux.HandleFunc("POST /api/admin.conversations.invite", h.adminConversationInvite)
	mux.HandleFunc("POST /api/admin.conversations.convertToPrivate", h.adminConversationConvertToPrivate)
	mux.HandleFunc("GET /api/admin.conversations.getConversationPrefs", h.adminConversationGetPrefs)
	mux.HandleFunc("POST /api/admin.conversations.getConversationPrefs", h.adminConversationGetPrefs)
	mux.HandleFunc("POST /api/admin.conversations.setConversationPrefs", h.adminConversationSetPrefs)
	mux.HandleFunc("GET /api/admin.conversations.search", h.adminConversationSearch)
	mux.HandleFunc("POST /api/admin.conversations.search", h.adminConversationSearch)
	mux.HandleFunc("GET /api/admin.conversations.getTeams", h.adminConversationGetTeams)
	mux.HandleFunc("POST /api/admin.conversations.getTeams", h.adminConversationGetTeams)
	mux.HandleFunc("POST /api/admin.conversations.setTeams", h.adminConversationSetTeams)
	mux.HandleFunc("POST /api/admin.conversations.disconnectShared", h.adminConversationDisconnectShared)
	mux.HandleFunc("GET /api/admin.conversations.ekm.listOriginalConnectedChannelInfo", h.adminConnectedChannelInfo)
	mux.HandleFunc("POST /api/admin.conversations.ekm.listOriginalConnectedChannelInfo", h.adminConnectedChannelInfo)
	mux.HandleFunc("POST /api/admin.emoji.add", h.adminEmojiAdd)
	mux.HandleFunc("GET /api/admin.emoji.add", h.adminEmojiAdd)
	mux.HandleFunc("POST /api/admin.emoji.addAlias", h.adminEmojiAddAlias)
	mux.HandleFunc("GET /api/admin.emoji.addAlias", h.adminEmojiAddAlias)
	mux.HandleFunc("GET /api/admin.emoji.list", h.adminEmojiList)
	mux.HandleFunc("POST /api/admin.emoji.list", h.adminEmojiList)
	mux.HandleFunc("POST /api/admin.emoji.remove", h.adminEmojiRemove)
	mux.HandleFunc("GET /api/admin.emoji.remove", h.adminEmojiRemove)
	mux.HandleFunc("POST /api/admin.emoji.rename", h.adminEmojiRename)
	mux.HandleFunc("GET /api/admin.emoji.rename", h.adminEmojiRename)
	mux.HandleFunc("GET /api/emoji.list", h.emojiList)
	mux.HandleFunc("POST /api/emoji.list", h.emojiList)
	mux.HandleFunc("POST /api/chat.postMessage", h.postMessage)
	mux.HandleFunc("POST /api/chat.startStream", h.startMessageStream)
	mux.HandleFunc("POST /api/chat.appendStream", h.appendMessageStream)
	mux.HandleFunc("POST /api/chat.stopStream", h.stopMessageStream)
	mux.HandleFunc("POST /api/chat.unfurl", h.chatUnfurl)
	mux.HandleFunc("POST /api/chat.postEphemeral", h.postEphemeral)
	mux.HandleFunc("POST /api/chat.meMessage", h.meMessage)
	mux.HandleFunc("POST /api/chat.update", h.updateMessage)
	mux.HandleFunc("POST /api/chat.delete", h.deleteMessage)
	mux.HandleFunc("GET /api/chat.getPermalink", h.getPermalink)
	mux.HandleFunc("POST /api/chat.getPermalink", h.getPermalink)
	mux.HandleFunc("POST /api/chat.scheduleMessage", h.scheduleMessage)
	mux.HandleFunc("GET /api/chat.scheduledMessages.list", h.scheduledMessagesList)
	mux.HandleFunc("POST /api/chat.scheduledMessages.list", h.scheduledMessagesList)
	mux.HandleFunc("POST /api/chat.deleteScheduledMessage", h.deleteScheduledMessage)
	mux.HandleFunc("GET /api/conversations.history", h.history)
	mux.HandleFunc("POST /api/conversations.history", h.history)
	mux.HandleFunc("GET /api/conversations.replies", h.replies)
	mux.HandleFunc("POST /api/conversations.replies", h.replies)
	mux.HandleFunc("GET /api/conversations.info", h.conversationInfo)
	mux.HandleFunc("POST /api/conversations.info", h.conversationInfo)
	mux.HandleFunc("GET /api/users.info", h.userInfo)
	mux.HandleFunc("POST /api/users.info", h.userInfo)
	mux.HandleFunc("GET /api/users.identity", h.usersIdentity)
	mux.HandleFunc("POST /api/users.identity", h.usersIdentity)
	mux.HandleFunc("GET /api/users.lookupByEmail", h.lookupUserByEmail)
	mux.HandleFunc("POST /api/users.lookupByEmail", h.lookupUserByEmail)
	mux.HandleFunc("GET /api/users.getPresence", h.getPresence)
	mux.HandleFunc("POST /api/users.getPresence", h.getPresence)
	mux.HandleFunc("POST /api/users.setPresence", h.setPresence)
	mux.HandleFunc("GET /api/dnd.info", h.dndInfo)
	mux.HandleFunc("POST /api/dnd.info", h.dndInfo)
	mux.HandleFunc("POST /api/dnd.endDnd", h.dndEnd)
	mux.HandleFunc("POST /api/dnd.endSnooze", h.dndEndSnooze)
	mux.HandleFunc("POST /api/dnd.setSnooze", h.dndSetSnooze)
	mux.HandleFunc("GET /api/dnd.teamInfo", h.dndTeamInfo)
	mux.HandleFunc("POST /api/dnd.teamInfo", h.dndTeamInfo)
	mux.HandleFunc("GET /api/users.profile.get", h.getUserProfile)
	mux.HandleFunc("POST /api/users.profile.get", h.getUserProfile)
	mux.HandleFunc("GET /api/users.list", h.usersList)
	mux.HandleFunc("POST /api/users.list", h.usersList)
	mux.HandleFunc("POST /api/users.profile.set", h.setUserProfile)
	mux.HandleFunc("POST /api/users.deletePhoto", h.deleteUserPhoto)
	mux.HandleFunc("POST /api/users.setPhoto", h.setUserPhoto)
	mux.HandleFunc("POST /api/users.setActive", h.usersSetActive)
	mux.HandleFunc("GET /api/conversations.list", h.conversationsList)
	mux.HandleFunc("POST /api/conversations.list", h.conversationsList)
	mux.HandleFunc("GET /api/users.conversations", h.usersConversations)
	mux.HandleFunc("POST /api/users.conversations", h.usersConversations)
	mux.HandleFunc("GET /api/conversations.members", h.conversationMembers)
	mux.HandleFunc("POST /api/conversations.members", h.conversationMembers)
	mux.HandleFunc("POST /api/conversations.create", h.createConversation)
	mux.HandleFunc("POST /api/conversations.join", h.joinConversation)
	mux.HandleFunc("POST /api/conversations.invite", h.inviteConversation)
	mux.HandleFunc("POST /api/conversations.leave", h.leaveConversation)
	mux.HandleFunc("POST /api/conversations.kick", h.kickConversation)
	mux.HandleFunc("POST /api/conversations.rename", h.renameConversation)
	mux.HandleFunc("POST /api/conversations.setTopic", h.setConversationTopic)
	mux.HandleFunc("POST /api/conversations.setPurpose", h.setConversationPurpose)
	mux.HandleFunc("POST /api/conversations.archive", h.archiveConversation)
	mux.HandleFunc("POST /api/conversations.unarchive", h.unarchiveConversation)
	mux.HandleFunc("POST /api/conversations.close", h.closeConversation)
	mux.HandleFunc("POST /api/conversations.open", h.openConversation)
	mux.HandleFunc("POST /api/conversations.mark", h.markConversation)
	mux.HandleFunc("POST /api/reactions.add", h.addReaction)
	mux.HandleFunc("POST /api/reactions.remove", h.removeReaction)
	mux.HandleFunc("GET /api/reactions.get", h.getReactions)
	mux.HandleFunc("POST /api/reactions.get", h.getReactions)
	mux.HandleFunc("GET /api/reactions.list", h.listUserReactions)
	mux.HandleFunc("POST /api/reactions.list", h.listUserReactions)
	mux.HandleFunc("POST /api/pins.add", h.addPin)
	mux.HandleFunc("POST /api/pins.remove", h.removePin)
	mux.HandleFunc("GET /api/pins.list", h.listPins)
	mux.HandleFunc("POST /api/pins.list", h.listPins)
	mux.HandleFunc("POST /api/stars.add", h.addStar)
	mux.HandleFunc("GET /api/stars.list", h.listStars)
	mux.HandleFunc("POST /api/stars.list", h.listStars)
	mux.HandleFunc("POST /api/stars.remove", h.removeStar)
	mux.HandleFunc("POST /api/bookmarks.add", h.addBookmark)
	mux.HandleFunc("POST /api/bookmarks.edit", h.editBookmark)
	mux.HandleFunc("GET /api/bookmarks.list", h.listBookmarks)
	mux.HandleFunc("POST /api/bookmarks.list", h.listBookmarks)
	mux.HandleFunc("POST /api/bookmarks.remove", h.removeBookmark)
	mux.HandleFunc("POST /api/canvases.create", h.createCanvas)
	mux.HandleFunc("POST /api/canvases.edit", h.editCanvas)
	mux.HandleFunc("POST /api/canvases.delete", h.deleteCanvas)
	mux.HandleFunc("POST /api/canvases.access.set", h.setCanvasAccess)
	mux.HandleFunc("POST /api/canvases.access.delete", h.deleteCanvasAccess)
	mux.HandleFunc("POST /api/canvases.sections.lookup", h.lookupCanvasSections)
	mux.HandleFunc("POST /api/conversations.canvases.create", h.createConversationCanvas)
	mux.HandleFunc("POST /api/slackLists.create", h.createList)
	mux.HandleFunc("POST /api/slackLists.update", h.updateList)
	mux.HandleFunc("POST /api/slackLists.items.create", h.createListItem)
	mux.HandleFunc("POST /api/slackLists.items.info", h.listItemInfo)
	mux.HandleFunc("POST /api/slackLists.items.list", h.listItems)
	mux.HandleFunc("POST /api/slackLists.items.update", h.updateListItem)
	mux.HandleFunc("POST /api/slackLists.items.delete", h.deleteListItem)
	mux.HandleFunc("POST /api/slackLists.items.deleteMultiple", h.deleteListItems)
	mux.HandleFunc("POST /api/slackLists.access.set", h.setListAccess)
	mux.HandleFunc("POST /api/slackLists.access.delete", h.deleteListAccess)
	mux.HandleFunc("POST /api/slackLists.download.start", h.startListDownload)
	mux.HandleFunc("POST /api/slackLists.download.get", h.getListDownload)
	mux.HandleFunc("POST /api/entity.presentDetails", h.presentEntityDetails)
	mux.HandleFunc("POST /api/entity.presentComments", h.presentEntityComments)
	mux.HandleFunc("POST /api/entity.acknowledgeCommentAction", h.acknowledgeEntityCommentAction)
	mux.HandleFunc("GET /internal/slack-lists/download.csv", h.downloadListCSV)
	mux.HandleFunc("POST /api/reminders.add", h.addReminder)
	mux.HandleFunc("POST /api/reminders.complete", h.completeReminder)
	mux.HandleFunc("POST /api/reminders.delete", h.deleteReminder)
	mux.HandleFunc("GET /api/reminders.info", h.reminderInfo)
	mux.HandleFunc("POST /api/reminders.info", h.reminderInfo)
	mux.HandleFunc("GET /api/reminders.list", h.listReminders)
	mux.HandleFunc("POST /api/reminders.list", h.listReminders)
	mux.HandleFunc("POST /api/usergroups.create", h.createUserGroup)
	mux.HandleFunc("POST /api/usergroups.update", h.updateUserGroup)
	mux.HandleFunc("POST /api/usergroups.enable", h.enableUserGroup)
	mux.HandleFunc("POST /api/usergroups.disable", h.disableUserGroup)
	mux.HandleFunc("GET /api/usergroups.list", h.listUserGroups)
	mux.HandleFunc("POST /api/usergroups.list", h.listUserGroups)
	mux.HandleFunc("GET /api/usergroups.users.list", h.userGroupUsers)
	mux.HandleFunc("POST /api/usergroups.users.list", h.userGroupUsers)
	mux.HandleFunc("POST /api/usergroups.users.update", h.updateUserGroupUsers)
	mux.HandleFunc("POST /api/admin.usergroups.addChannels", h.adminUserGroupAddChannels)
	mux.HandleFunc("POST /api/admin.usergroups.addTeams", h.adminUserGroupAddTeams)
	mux.HandleFunc("POST /api/admin.usergroups.removeChannels", h.adminUserGroupRemoveChannels)
	mux.HandleFunc("GET /api/admin.usergroups.listChannels", h.adminUserGroupListChannels)
	mux.HandleFunc("POST /api/admin.usergroups.listChannels", h.adminUserGroupListChannels)
	mux.HandleFunc("GET /api/admin.teams.settings.info", h.adminTeamSettingsInfo)
	mux.HandleFunc("POST /api/admin.teams.settings.info", h.adminTeamSettingsInfo)
	mux.HandleFunc("POST /api/admin.teams.settings.setName", h.adminTeamSettingsSetName)
	mux.HandleFunc("POST /api/admin.teams.settings.setDescription", h.adminTeamSettingsSetDescription)
	mux.HandleFunc("POST /api/admin.teams.settings.setDiscoverability", h.adminTeamSettingsSetDiscoverability)
	mux.HandleFunc("POST /api/admin.teams.settings.setIcon", h.adminTeamSettingsSetIcon)
	mux.HandleFunc("GET /api/admin.teams.settings.setIcon", h.adminTeamSettingsSetIcon)
	mux.HandleFunc("POST /api/admin.teams.settings.setDefaultChannels", h.adminTeamSettingsSetDefaultChannels)
	mux.HandleFunc("GET /api/admin.teams.settings.setDefaultChannels", h.adminTeamSettingsSetDefaultChannels)
	mux.HandleFunc("GET /api/admin.teams.list", h.adminTeamsList)
	mux.HandleFunc("POST /api/admin.teams.list", h.adminTeamsList)
	mux.HandleFunc("POST /api/admin.teams.create", h.adminTeamsCreate)
	mux.HandleFunc("GET /api/admin.teams.admins.list", h.adminTeamsAdminsList)
	mux.HandleFunc("POST /api/admin.teams.admins.list", h.adminTeamsAdminsList)
	mux.HandleFunc("GET /api/admin.teams.owners.list", h.adminTeamsOwnersList)
	mux.HandleFunc("POST /api/admin.teams.owners.list", h.adminTeamsOwnersList)
	mux.HandleFunc("POST /api/calls.add", h.addCall)
	mux.HandleFunc("POST /api/calls.end", h.endCall)
	mux.HandleFunc("GET /api/calls.info", h.callInfo)
	mux.HandleFunc("POST /api/calls.info", h.callInfo)
	mux.HandleFunc("POST /api/calls.update", h.updateCall)
	mux.HandleFunc("POST /api/calls.participants.add", h.addCallParticipants)
	mux.HandleFunc("POST /api/calls.participants.remove", h.removeCallParticipants)
	mux.HandleFunc("GET /api/search.messages", h.searchMessages)
	mux.HandleFunc("POST /api/search.messages", h.searchMessages)
	mux.HandleFunc("GET /api/search.files", h.searchFiles)
	mux.HandleFunc("POST /api/search.files", h.searchFiles)
	mux.HandleFunc("GET /api/search.all", h.searchAll)
	mux.HandleFunc("POST /api/search.all", h.searchAll)
	mux.HandleFunc("GET /api/files.info", h.fileInfo)
	mux.HandleFunc("POST /api/files.info", h.fileInfo)
	mux.HandleFunc("POST /api/files.delete", h.deleteFile)
	mux.HandleFunc("POST /api/files.comments.delete", h.deleteFileComment)
	mux.HandleFunc("GET /api/files.list", h.filesList)
	mux.HandleFunc("POST /api/files.list", h.filesList)
	mux.HandleFunc("POST /api/files.upload", h.fileUpload)
	mux.HandleFunc("POST /api/files.remote.add", h.remoteFileAdd)
	mux.HandleFunc("GET /api/files.remote.info", h.remoteFileInfo)
	mux.HandleFunc("POST /api/files.remote.info", h.remoteFileInfo)
	mux.HandleFunc("GET /api/files.remote.list", h.remoteFilesList)
	mux.HandleFunc("POST /api/files.remote.list", h.remoteFilesList)
	mux.HandleFunc("POST /api/files.remote.remove", h.remoteFileRemove)
	mux.HandleFunc("GET /api/files.remote.share", h.remoteFileShare)
	mux.HandleFunc("POST /api/files.remote.share", h.remoteFileShare)
	mux.HandleFunc("POST /api/files.remote.update", h.remoteFileUpdate)
	mux.HandleFunc("POST /api/files.sharedPublicURL", h.shareFilePublic)
	mux.HandleFunc("GET /api/files.sharedPublicURL", h.shareFilePublic)
	mux.HandleFunc("POST /api/files.revokePublicURL", h.revokeFilePublic)
	mux.HandleFunc("GET /api/files/{file}", h.downloadFile)
	mux.HandleFunc("GET /files/public/{token}", h.downloadPublicFile)
	mux.HandleFunc("GET /users/{workspace}/{user}/photo/{token}", h.downloadUserPhoto)
	mux.HandleFunc("GET /api/openid.connect.token", h.openIDConnectToken)
	mux.HandleFunc("POST /api/openid.connect.token", h.openIDConnectToken)
	mux.HandleFunc("GET /api/openid.connect.userInfo", h.openIDConnectUserInfo)
	mux.HandleFunc("POST /api/openid.connect.userInfo", h.openIDConnectUserInfo)
	mux.HandleFunc("POST /services/{workspace}/{app}/{secret}", h.incomingWebhook)
	mux.HandleFunc("POST /services/triggers/{workspace}/{trigger}/{secret}", h.workflowTriggerWebhook)
	mux.HandleFunc("POST /internal/admin/incoming-webhooks/create", h.adminIncomingWebhookCreate)
	mux.HandleFunc("POST /internal/admin/incoming-webhooks/enable", h.adminIncomingWebhookEnable)
	mux.HandleFunc("GET /api/files.getUploadURLExternal", h.filesGetUploadURLExternal)
	mux.HandleFunc("POST /api/files.getUploadURLExternal", h.filesGetUploadURLExternal)
	mux.HandleFunc("POST /internal/files/external/{upload}", h.externalFileUpload)
	mux.HandleFunc("GET /api/files.completeUploadExternal", h.filesCompleteUploadExternal)
	mux.HandleFunc("POST /api/files.completeUploadExternal", h.filesCompleteUploadExternal)
	// Registered last and least specifically, so it claims only what nothing else
	// does: an unknown method name and a verb no route declares.
	mux.HandleFunc("/api/", h.unknownMethod)
}

func (h Handler) blocksValidate(w http.ResponseWriter, r *http.Request) {
	if _, err := h.authenticate(r, ""); err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	names := []string{"blocks", "message", "view"}
	selected := ""
	for _, name := range names {
		if strings.TrimSpace(fields[name]) == "" {
			continue
		}
		if selected != "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok": false, "error": "invalid_arguments",
				"response_metadata": map[string]any{"messages": []string{"must provide exactly one of `blocks`, `view`, or `message`"}},
			})
			return
		}
		selected = name
	}
	if selected == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": "invalid_arguments",
			"response_metadata": map[string]any{"messages": []string{"must provide exactly one of `blocks`, `view`, or `message`"}},
		})
		return
	}
	raw := json.RawMessage(fields[selected])
	var problems []blockkit.Error
	switch selected {
	case "blocks":
		problems, err = blockkit.ValidateBlocks(raw, "", 100)
	case "message":
		problems, err = blockkit.ValidateMessage(raw)
	case "view":
		problems, err = blockkit.ValidateView(raw)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": "invalid_arguments",
			"response_metadata": map[string]any{"messages": []string{err.Error()}},
		})
		return
	}
	if len(problems) != 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_" + selected, "errors": problems})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// unknownMethod answers anything under /api/ that no registered route claims.
//
// net/http.ServeMux answered a typo with a text/plain "404 page not found" and a
// wrong verb with a text/plain "Method Not Allowed", both at a non-200 status.
// No Slack SDK can parse either: every one of them decodes the body as JSON and
// keys on `ok`. Every response on /api/* has to be an envelope, so this is the
// catch-all the mux never had. It is registered without a method and without a
// trailing route, so it is the least specific pattern under /api/ and is reached
// only when nothing else matches.
//
// `unknown_method` is not in any of the pinned enums — the snapshot describes no
// routing failure at all — so it is recorded as a deviation. It is the name Slack
// itself uses for this case.
func (h Handler) unknownMethod(w http.ResponseWriter, _ *http.Request) {
	writeError(w, "unknown_method")
}

func (h *Handler) ConfigureSocketMode(service socketmode.Service, authenticator auth.Authenticator) {
	if h == nil {
		return
	}
	h.SocketMode = service
	h.SocketAuth = authenticator
}

func (h Handler) appsConnectionsOpen(w http.ResponseWriter, r *http.Request) {
	if h.SocketAuth == nil || h.SocketMode.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "socket_mode_unavailable"})
		return
	}
	// The four authentication outcomes are distinct everywhere else in this
	// transport; collapsing them here told an app holding a revoked token that it
	// had sent no credential, and a missing connections:write grant produced no
	// `needed`/`provided` at all.
	principal, err := h.SocketAuth.Authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if !principal.HasScope(auth.ScopeConnectionsWrite) {
		writeAuthError(w, missingScopeError{needed: auth.ScopeConnectionsWrite, provided: permissionScopes(principal)})
		return
	}
	if principal.AppID == "" {
		writeError(w, "invalid_auth")
		return
	}
	result, err := h.SocketMode.Open(r.Context(), principal.AppID)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": result.URL})
}

func (h Handler) appsDatastorePut(w http.ResponseWriter, r *http.Request) {
	h.appsDatastoreWrite(w, r, false, false)
}

func (h Handler) appsDatastoreUpdate(w http.ResponseWriter, r *http.Request) {
	h.appsDatastoreWrite(w, r, true, false)
}

func (h Handler) appsDatastoreBulkPut(w http.ResponseWriter, r *http.Request) {
	h.appsDatastoreWrite(w, r, false, true)
}

func (h Handler) appsDatastoreWrite(w http.ResponseWriter, r *http.Request, merge, bulk bool) {
	principal, fields, ok := h.appDatastoreWriteRequest(w, r)
	if !ok {
		return
	}
	datastore := strings.TrimSpace(fields["datastore"])
	if datastore == "" {
		writeError(w, "invalid_arguments")
		return
	}
	var items []string
	if bulk {
		var rawItems []json.RawMessage
		if json.Unmarshal([]byte(fields["items"]), &rawItems) != nil || len(rawItems) == 0 || len(rawItems) > 25 {
			writeError(w, "invalid_arguments")
			return
		}
		items = make([]string, len(rawItems))
		for index, item := range rawItems {
			if !jsonIsObject(item) {
				writeError(w, "invalid_arguments")
				return
			}
			items[index] = string(item)
		}
	} else {
		if !jsonIsObject(json.RawMessage(fields["item"])) {
			writeError(w, "invalid_arguments")
			return
		}
		items = []string{fields["item"]}
	}
	stored, err := h.Messages.PutAppDatastoreItems(
		r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, datastore, items, merge,
	)
	if err != nil {
		writeAppDatastoreError(w, err)
		return
	}
	if bulk {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "datastore": datastore, "failed_items": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "datastore": datastore, "item": json.RawMessage(stored[0])})
}

func (h Handler) appsDatastoreGet(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.appDatastoreReadRequest(w, r)
	if !ok {
		return
	}
	datastore, id := strings.TrimSpace(fields["datastore"]), strings.TrimSpace(fields["id"])
	if datastore == "" || id == "" {
		writeError(w, "invalid_arguments")
		return
	}
	items, err := h.Messages.GetAppDatastoreItems(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, datastore, []string{id})
	if err != nil {
		writeAppDatastoreError(w, err)
		return
	}
	item := any(map[string]any{})
	if len(items) != 0 {
		item = json.RawMessage(items[0])
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "datastore": datastore, "item": item})
}

func (h Handler) appsDatastoreBulkGet(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.appDatastoreReadRequest(w, r)
	if !ok {
		return
	}
	datastore, ids := strings.TrimSpace(fields["datastore"]), datastoreIDs(fields["ids"])
	if datastore == "" || len(ids) == 0 || len(ids) > 25 {
		writeError(w, "invalid_arguments")
		return
	}
	items, err := h.Messages.GetAppDatastoreItems(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, datastore, ids)
	if err != nil {
		writeAppDatastoreError(w, err)
		return
	}
	encoded := make([]json.RawMessage, len(items))
	for index, item := range items {
		encoded[index] = json.RawMessage(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "datastore": datastore, "items": encoded})
}

func (h Handler) appsDatastoreQuery(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.appDatastoreReadRequest(w, r)
	if !ok {
		return
	}
	datastore := strings.TrimSpace(fields["datastore"])
	limit := 100
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, "invalid_arguments")
			return
		}
		limit = parsed
	}
	if datastore == "" || limit < 1 || limit > 1000 {
		writeError(w, "invalid_arguments")
		return
	}
	page, err := h.Messages.QueryAppDatastoreItems(
		r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, datastore,
		domain.AppDatastoreQuery{
			Expression: fields["expression"], ExpressionAttributes: fields["expression_attributes"], ExpressionValues: fields["expression_values"],
			Page: domain.PageRequest{Limit: limit, Cursor: domain.Cursor(strings.TrimSpace(fields["cursor"]))},
		},
	)
	if err != nil {
		writeAppDatastoreError(w, err)
		return
	}
	items := make([]json.RawMessage, len(page.Items))
	for index, item := range page.Items {
		items[index] = json.RawMessage(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "datastore": datastore, "items": items,
		"response_metadata": map[string]string{"next_cursor": string(page.NextCursor)},
	})
}

func (h Handler) appsDatastoreCount(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.appDatastoreReadRequest(w, r)
	if !ok {
		return
	}
	datastore := strings.TrimSpace(fields["datastore"])
	if datastore == "" {
		writeError(w, "invalid_arguments")
		return
	}
	count, err := h.Messages.CountAppDatastoreItems(
		r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, datastore,
		domain.AppDatastoreQuery{
			Expression: fields["expression"], ExpressionAttributes: fields["expression_attributes"], ExpressionValues: fields["expression_values"],
		},
	)
	if err != nil {
		writeAppDatastoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "datastore": datastore, "count": count})
}

func (h Handler) appsDatastoreDelete(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.appDatastoreWriteRequest(w, r)
	if !ok {
		return
	}
	datastore, id := strings.TrimSpace(fields["datastore"]), strings.TrimSpace(fields["id"])
	if datastore == "" || id == "" {
		writeError(w, "invalid_arguments")
		return
	}
	if err := h.Messages.DeleteAppDatastoreItems(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, datastore, []string{id}); err != nil {
		writeAppDatastoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) appsDatastoreBulkDelete(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.appDatastoreWriteRequest(w, r)
	if !ok {
		return
	}
	datastore, ids := strings.TrimSpace(fields["datastore"]), datastoreIDs(fields["ids"])
	if datastore == "" || len(ids) == 0 || len(ids) > 25 {
		writeError(w, "invalid_arguments")
		return
	}
	if err := h.Messages.DeleteAppDatastoreItems(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, datastore, ids); err != nil {
		writeAppDatastoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "datastore": datastore, "failed_items": []any{}})
}

func (h Handler) appDatastoreReadRequest(w http.ResponseWriter, r *http.Request) (auth.Principal, map[string]string, bool) {
	principal, err := h.authenticate(r, auth.ScopeDatastoreRead)
	return h.decodeAppDatastoreRequest(w, r, principal, err)
}

func (h Handler) appDatastoreWriteRequest(w http.ResponseWriter, r *http.Request) (auth.Principal, map[string]string, bool) {
	principal, err := h.authenticate(r, auth.ScopeDatastoreWrite)
	return h.decodeAppDatastoreRequest(w, r, principal, err)
}

func (h Handler) decodeAppDatastoreRequest(w http.ResponseWriter, r *http.Request, principal auth.Principal, err error) (auth.Principal, map[string]string, bool) {
	if err != nil {
		writeAuthError(w, err)
		return auth.Principal{}, nil, false
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return auth.Principal{}, nil, false
	}
	requested := domain.AppID(strings.TrimSpace(fields["app_id"]))
	switch principal.TokenType {
	case "bot":
		if principal.AppID == "" || (requested != "" && requested != principal.AppID) {
			writeError(w, "invalid_app_id")
			return auth.Principal{}, nil, false
		}
	case "user":
		if requested == "" {
			writeError(w, "invalid_arguments")
			return auth.Principal{}, nil, false
		}
		principal.AppID = requested
	default:
		writeError(w, "not_allowed_token_type")
		return auth.Principal{}, nil, false
	}
	return principal, fields, true
}

func datastoreIDs(raw string) []string {
	values := strings.Split(raw, ",")
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func writeAppDatastoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrAppDatastoreNotFound):
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": "datastore_error",
			"errors": []map[string]string{{
				"code": "datastore_config_not_found", "message": "The datastore configuration could not be found", "pointer": "/datastores",
			}},
		})
	case errors.Is(err, service.ErrInvalidDatastoreItem):
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": "datastore_error",
			"errors": []map[string]string{{"code": "invalid_item", "message": err.Error(), "pointer": "/item"}},
		})
	case errors.Is(err, service.ErrInvalidDatastoreQuery), errors.Is(err, domain.ErrInvalidCursor), errors.Is(err, store.ErrInvalidArgument):
		writeError(w, "invalid_arguments")
	case errors.Is(err, service.ErrAppNotHosted):
		writeError(w, "app_not_hosted")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, "invalid_app_id")
	default:
		writeError(w, mapServiceError(err, "invalid_app_id"))
	}
}

func (h Handler) apiTest(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	errorName := strings.TrimSpace(fields["error"])
	if errorName != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": errorName, "args": map[string]string{"error": errorName}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) history(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := normalizeHistoryRequest(fields, "invalid_ts_oldest", "invalid_ts_latest")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// Slack history is newest-first. Reading the store in that direction also
	// makes the requested window one index seek instead of filtering the oldest
	// page of a long conversation and calling it history.
	request.Page.Descending = true
	page, err := h.Messages.History(r.Context(), principal.WorkspaceID, principal.UserID, request.Channel, request.Page)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	result := rangedMessages(page.Messages, request.Range)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": result, "has_more": page.HasMore, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}

func (h Handler) replies(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := normalizeHistoryRequest(fields, "invalid_arg_name", "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["ts"]) == "" {
		// /conversations.replies enumerates thread_not_found, not invalid_arguments.
		writeError(w, "thread_not_found")
		return
	}
	page, err := h.Messages.Replies(r.Context(), principal.WorkspaceID, principal.UserID, request.Channel, domain.MessageTimestamp(strings.TrimSpace(fields["ts"])), request.Page)
	if err != nil {
		writeError(w, mapServiceError(err, "thread_not_found"))
		return
	}
	result := rangedMessages(page.Messages, request.Range)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": result, "has_more": page.HasMore, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}

type historyRequest struct {
	Channel domain.ConversationID
	Page    domain.PageRequest
	Range   historyRange
}

// historyRange is the `oldest`/`latest`/`inclusive` window declared by both
// /conversations.history and /conversations.replies. All three arguments used to
// be dropped, so a range-limited request answered `"ok":true` with the channel's
// entire recent history — the caller received strictly more data than it asked
// for and had no way to tell.
//
// The store has no range-scanning API, so the window is applied at the wire
// boundary. Paging still works: `has_more` and `next_cursor` continue to describe
// the underlying scan, so a client that follows the cursor sees every message in
// the window, and a page may legitimately come back short or empty.
type historyRange struct {
	oldest    int64
	hasOldest bool
	latest    int64
	hasLatest bool
	inclusive bool
}

func (h historyRange) includes(timestamp string) bool {
	value, ok := parseSlackTimestamp(timestamp)
	if !ok {
		return true
	}
	if h.hasOldest && (value < h.oldest || (value == h.oldest && !h.inclusive)) {
		return false
	}
	if h.hasLatest && (value > h.latest || (value == h.latest && !h.inclusive)) {
		return false
	}
	return true
}

// normalizeHistoryRequest decodes the window /conversations.history and
// /conversations.replies share. The two operations do not share an enum:
// history declares invalid_ts_latest and invalid_ts_oldest, replies declares
// neither, so the caller supplies the code its own enum carries.
func normalizeHistoryRequest(fields map[string]string, invalidOldest, invalidLatest string) (historyRequest, error) {
	channel := strings.TrimSpace(fields["channel"])
	if channel == "" {
		// Both operations enumerate channel_not_found as the missing-channel error.
		return historyRequest{}, decodeFailure("channel_not_found", "channel is required")
	}
	limit, err := clampLimit(fields["limit"], 100, 200)
	if err != nil {
		return historyRequest{}, err
	}
	// Neither /conversations.history nor /conversations.replies declares
	// invalid_cursor; both declare invalid_arg_name.
	cursor, err := decodeMessageCursor(fields["cursor"], "invalid_arg_name")
	if err != nil {
		return historyRequest{}, err
	}
	window := historyRange{}
	if raw := strings.TrimSpace(fields["oldest"]); raw != "" {
		value, ok := parseSlackTimestamp(raw)
		if !ok {
			return historyRequest{}, decodeFailure(invalidOldest, "oldest is not a Slack timestamp")
		}
		window.oldest, window.hasOldest = value, true
	}
	if raw := strings.TrimSpace(fields["latest"]); raw != "" {
		value, ok := parseSlackTimestamp(raw)
		if !ok {
			return historyRequest{}, decodeFailure(invalidLatest, "latest is not a Slack timestamp")
		}
		window.latest, window.hasLatest = value, true
	}
	inclusive, err := parseBoolField(fields["inclusive"])
	if err != nil {
		return historyRequest{}, decodeFailure("invalid_arg_name", "inclusive must be a boolean")
	}
	window.inclusive = inclusive
	return historyRequest{Channel: domain.ConversationID(channel), Page: domain.PageRequest{Limit: limit, Cursor: cursor}, Range: window}, nil
}

func rangedMessages(messages []domain.Message, window historyRange) []map[string]any {
	result := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		if !window.includes(slackTimestamp(message.CreatedAt)) {
			continue
		}
		result = append(result, messageResponse(message))
	}
	return result
}

func (h Handler) authTest(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	workspace, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	teamName := strings.TrimSpace(workspace.Name)
	if teamName == "" {
		teamName = string(workspace.ID)
	}
	response := map[string]any{"ok": true, "url": "http://localhost/", "team": teamName, "team_id": workspace.ID, "user": string(principal.UserID), "user_id": principal.UserID}
	if principal.TokenType == "bot" {
		response["bot_id"] = principal.BotID
		response["is_enterprise_install"] = false
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) authTeamsList(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if principal.AppID == "" {
		writeError(w, "not_allowed_token_type")
		return
	}
	limit, err := clampLimit(fields["limit"], 100, 1000)
	if err != nil {
		writeError(w, "invalid_limit")
		return
	}
	cursor, err := decodeCursor(fields["cursor"], "invalid_cursor")
	if err != nil {
		writeError(w, "invalid_cursor")
		return
	}
	includeIcon, err := parseBoolField(fields["include_icon"])
	if err != nil {
		writeError(w, "invalid_arguments")
		return
	}
	page, err := h.Messages.AuthorizedAppWorkspaces(
		r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID,
		domain.PageRequest{Limit: limit, Cursor: cursor},
	)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	teams := make([]map[string]any, 0, len(page.Workspaces))
	for _, workspace := range page.Workspaces {
		team := map[string]any{"id": workspace.ID, "name": workspace.Name}
		if includeIcon {
			team["icon"] = map[string]any{
				"image_34": workspace.IconURL, "image_44": workspace.IconURL,
				"image_68": workspace.IconURL, "image_88": workspace.IconURL,
				"image_102": workspace.IconURL, "image_132": workspace.IconURL,
				"image_default": strings.TrimSpace(workspace.IconURL) == "",
			}
		}
		teams = append(teams, team)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "teams": teams,
		"response_metadata": map[string]any{"next_cursor": page.NextCursor},
	})
}

// foreignWorkspace reports the first workspace in values that is not the
// principal's own. Cross-workspace attachment is rejected at the wire boundary
// so that a service or store that only validates existence cannot be talked into
// a cross-tenant write.
func foreignWorkspace(values []domain.WorkspaceID, own domain.WorkspaceID) (domain.WorkspaceID, bool) {
	for _, value := range values {
		if value != own {
			return value, true
		}
	}
	return "", false
}

func permissionScopes(principal auth.Principal) []string {
	values := make([]string, 0, len(principal.Scopes))
	for scope := range principal.Scopes {
		values = append(values, string(scope))
	}
	sort.Strings(values)
	return values
}

func permissionInfo(workspaceID domain.WorkspaceID, scopes []string) map[string]any {
	resource := func(ids []string) map[string]any {
		return map[string]any{"ids": ids, "wildcard": false}
	}
	empty := func() map[string]any {
		return map[string]any{"resources": resource([]string{}), "scopes": []string{}}
	}
	return map[string]any{
		"app_home": empty(),
		"channel":  empty(),
		"group":    empty(),
		"im":       empty(),
		"mpim":     empty(),
		"team":     map[string]any{"resources": resource([]string{string(workspaceID)}), "scopes": scopes},
	}
}

func (h Handler) appsPermissionsScopesList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scopes": []map[string]any{{
		"app_home": []string{},
		"channel":  []string{},
		"group":    []string{},
		"im":       []string{},
		"mpim":     []string{},
		"team":     permissionScopes(principal),
		"user":     []string{},
	}}})
}

func (h Handler) appsPermissionsResourcesList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	limit, err := clampLimit(fields["limit"], 100, 100)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["cursor"]) != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resources": []map[string]string{}, "response_metadata": map[string]string{"next_cursor": ""}})
		return
	}
	resources := []map[string]string{}
	if limit > 0 {
		resources = append(resources, map[string]string{"id": string(principal.WorkspaceID), "type": "team"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resources": resources, "response_metadata": map[string]string{"next_cursor": ""}})
}

func (h Handler) appsEventAuthorizationsList(w http.ResponseWriter, r *http.Request) {
	// Slack requires an app-level token in the Authorization header because
	// this method can return installations across several user/bot tokens.
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		writeError(w, "not_authed")
		return
	}
	principal, err := h.authenticateApp(r, auth.ScopeAuthorizationsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	appID, sequence, eventID, err := events.ParseEventContext(fields["event_context"])
	if err != nil {
		writeError(w, "invalid_event_context")
		return
	}
	if domain.AppID(appID) != principal.AppID {
		writeError(w, "auth_mismatch")
		return
	}
	records, err := h.Messages.ListAppEventsAfter(r.Context(), principal.AppID, sequence-1, 1)
	if err != nil {
		writeError(w, mapServiceError(err, "internal_error"))
		return
	}
	if len(records) != 1 || records[0].Sequence != sequence || records[0].Event.ID != eventID {
		writeError(w, "invalid_event_context")
		return
	}
	authorizations := make([]map[string]any, 0, len(records[0].Event.Authorizations))
	for _, authorization := range records[0].Event.Authorizations {
		authorizations = append(authorizations, map[string]any{
			"enterprise_id":         authorization.EnterpriseID,
			"team_id":               authorization.TeamID,
			"user_id":               authorization.UserID,
			"is_bot":                authorization.IsBot,
			"is_enterprise_install": authorization.IsEnterpriseInstall,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "authorizations": authorizations})
}

func (h Handler) appsPermissionsUsersList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	limit, err := clampLimit(fields["limit"], 100, 100)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	resources := []map[string]any{}
	if strings.TrimSpace(fields["cursor"]) == "" && limit > 0 {
		resources = append(resources, map[string]any{"id": principal.UserID, "scopes": permissionScopes(principal)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resources": resources, "response_metadata": map[string]string{"next_cursor": ""}})
}

func parsePermissionScopes(raw string) []string {
	return domain.NormalizeScopes(strings.Fields(strings.ReplaceAll(raw, ",", " ")))
}

func (h Handler) appsPermissionsRequest(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	h.requestAppPermissionsWithFields(w, r, fields, "")
}

func (h Handler) appsPermissionsUsersRequest(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["user"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	h.requestAppPermissionsWithFields(w, r, fields, domain.UserID(strings.TrimSpace(fields["user"])))
}

func (h Handler) requestAppPermissionsWithFields(w http.ResponseWriter, r *http.Request, fields map[string]string, target domain.UserID) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	scopes := parsePermissionScopes(fields["scopes"])
	triggerID := strings.TrimSpace(fields["trigger_id"])
	if len(scopes) == 0 || triggerID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.RequestAppPermissions(r.Context(), principal.WorkspaceID, principal.UserID, target, scopes, triggerID); err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) viewsOpen(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	value, err := h.Messages.OpenView(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, strings.TrimSpace(fields["trigger_id"]), fields["view"])
	if err != nil {
		reason := mapServiceError(err, "invalid_arguments")
		if errors.Is(err, service.ErrInvalidTrigger) {
			reason = "invalid_trigger"
		}
		writeError(w, reason)
		return
	}
	writeViewResponse(w, value)
}

func (h Handler) viewsPublish(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	target := domain.UserID(strings.TrimSpace(fields["user_id"]))
	if target == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.PublishView(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, target, fields["view"], strings.TrimSpace(fields["hash"]))
	if err != nil {
		if errors.Is(err, service.ErrAppHomeNotEnabled) {
			writeError(w, "not_enabled")
			return
		}
		writeError(w, mapServiceError(err, "view_not_found"))
		return
	}
	writeViewResponse(w, value)
}

func (h Handler) viewsPush(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	value, err := h.Messages.PushView(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, strings.TrimSpace(fields["trigger_id"]), fields["view"])
	if err != nil {
		reason := mapServiceError(err, "invalid_arguments")
		if errors.Is(err, service.ErrInvalidTrigger) {
			reason = "invalid_trigger"
		}
		writeError(w, reason)
		return
	}
	writeViewResponse(w, value)
}

func (h Handler) viewsUpdate(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	value, err := h.Messages.UpdateView(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, strings.TrimSpace(fields["view_id"]), strings.TrimSpace(fields["external_id"]), fields["view"], strings.TrimSpace(fields["hash"]))
	if err != nil {
		writeError(w, mapServiceError(err, "view_not_found"))
		return
	}
	writeViewResponse(w, value)
}

// viewResponse renders a stored view. It used to panic when the stored payload was
// not a JSON object, turning a read of durable data into an unhandled HTTP 500
// with no `ok` field and killing the serving goroutine. A payload this handler
// cannot render is now a handled `invalid_view`.
func viewResponse(value domain.View) (map[string]any, error) {
	result := make(map[string]any)
	if err := json.Unmarshal([]byte(value.Payload), &result); err != nil {
		return nil, decodeFailure("invalid_view", "stored view payload is not a JSON object")
	}
	result["id"] = value.ID
	result["app_id"] = value.AppID
	result["team_id"] = value.WorkspaceID
	result["hash"] = value.Hash
	result["root_view_id"] = value.RootViewID
	result["previous_view_id"] = value.PreviousViewID
	result["external_id"] = value.ExternalID
	state := any(map[string]any{"values": map[string]any{}})
	if strings.TrimSpace(value.State) != "" {
		if err := json.Unmarshal([]byte(value.State), &state); err != nil {
			return nil, decodeFailure("invalid_view", "stored view state is not valid JSON")
		}
	}
	result["state"] = state
	return result, nil
}

func writeViewResponse(w http.ResponseWriter, value domain.View) {
	rendered, err := viewResponse(value)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "view": rendered})
}

func (h Handler) workflowStepCompleted(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeWorkflowStepsExecute)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.WorkflowStepCompleted(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["workflow_step_execute_id"]), fields["outputs"]); err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) workflowStepFailed(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeWorkflowStepsExecute)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.WorkflowStepFailed(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["workflow_step_execute_id"]), fields["error"]); err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) workflowUpdateStep(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeWorkflowStepsExecute)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.WorkflowUpdateStep(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["workflow_step_edit_id"]), fields["inputs"], fields["outputs"], fields["step_name"], fields["step_image_url"]); err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) functionsCompleteSuccess(w http.ResponseWriter, r *http.Request) {
	fields, principal, ok := h.functionCompletionFields(w, r, "outputs")
	if !ok {
		return
	}
	var outputs map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fields["outputs"]), &outputs); err != nil || outputs == nil {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.CompleteFunction(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, domain.WorkflowStepID(strings.TrimSpace(fields["function_execution_id"])), fields["outputs"], ""); err != nil {
		writeFunctionCompletionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) functionsCompleteError(w http.ResponseWriter, r *http.Request) {
	fields, principal, ok := h.functionCompletionFields(w, r, "error")
	if !ok {
		return
	}
	if err := h.Messages.CompleteFunction(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, domain.WorkflowStepID(strings.TrimSpace(fields["function_execution_id"])), "", fields["error"]); err != nil {
		writeFunctionCompletionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) functionCompletionFields(w http.ResponseWriter, r *http.Request, requiredField string) (map[string]string, auth.Principal, bool) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return nil, auth.Principal{}, false
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return nil, auth.Principal{}, false
	}
	if strings.TrimSpace(fields["function_execution_id"]) == "" || strings.TrimSpace(fields[requiredField]) == "" {
		writeError(w, "invalid_arg_name")
		return nil, auth.Principal{}, false
	}
	return fields, principal, true
}

func writeFunctionCompletionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrFunctionAccessDenied):
		writeError(w, "access_denied")
	case errors.Is(err, service.ErrFunctionNotRunning), errors.Is(err, store.ErrConflict):
		writeError(w, "execution_not_in_running_state")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, "function_execution_not_found")
	case errors.Is(err, service.ErrInvalidWorkflowStep), errors.Is(err, store.ErrInvalidArgument):
		writeError(w, "invalid_arguments")
	default:
		writeError(w, mapServiceError(err, "invalid_arguments"))
	}
}

func (h Handler) functionPermissionRequest(w http.ResponseWriter, r *http.Request) (map[string]string, auth.Principal, domain.AppID, bool) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return nil, auth.Principal{}, "", false
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return nil, auth.Principal{}, "", false
	}
	appID := principal.AppID
	if supplied := domain.AppID(strings.TrimSpace(fields["function_app_id"])); supplied != "" {
		if principal.AppID != "" && supplied != principal.AppID {
			writeError(w, "access_denied")
			return nil, auth.Principal{}, "", false
		}
		appID = supplied
	}
	if appID == "" || strings.TrimSpace(fields["function_id"]) == "" && strings.TrimSpace(fields["function_callback_id"]) == "" {
		writeError(w, "function_not_found")
		return nil, auth.Principal{}, "", false
	}
	return fields, principal, appID, true
}

func functionPermissionResponse(value domain.AutomationPermission, users []domain.User) map[string]any {
	response := map[string]any{"ok": true, "permission_type": value.PermissionType}
	renderedUsers := make([]map[string]any, 0, len(users))
	for _, user := range users {
		renderedUsers = append(renderedUsers, map[string]any{
			"user_id": user.ID, "username": user.Name, "email": user.Email,
		})
	}
	response["users"] = renderedUsers
	if len(value.TeamIDs) != 0 {
		response["team_ids"] = value.TeamIDs
	}
	if len(value.OrgIDs) != 0 {
		response["org_ids"] = value.OrgIDs
	}
	return response
}

func (h Handler) functionPermissionUsers(ctx context.Context, principal auth.Principal, value domain.AutomationPermission) ([]domain.User, error) {
	users := make([]domain.User, 0, len(value.UserIDs))
	for _, userID := range value.UserIDs {
		user, err := h.Messages.UserInfo(ctx, principal.WorkspaceID, principal.UserID, userID)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, nil
}

func writeFunctionPermissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, "function_not_found")
	case errors.Is(err, service.ErrAutomationUserNotFound):
		writeError(w, "user_not_found")
	case errors.Is(err, service.ErrAutomationChannelNotFound),
		errors.Is(err, service.ErrAutomationTeamNotFound),
		errors.Is(err, service.ErrAutomationOrgNotFound),
		errors.Is(err, service.ErrAutomationEntitiesEmpty):
		writeError(w, "invalid_named_entities")
	case errors.Is(err, service.ErrFunctionAccessDenied):
		writeError(w, "access_denied")
	case errors.Is(err, service.ErrInvalidWorkflowStep), errors.Is(err, store.ErrInvalidArgument):
		writeError(w, "invalid_arguments")
	default:
		writeError(w, mapServiceError(err, "invalid_arguments"))
	}
}

func (h Handler) functionsDistributionsPermissionsList(w http.ResponseWriter, r *http.Request) {
	fields, principal, appID, ok := h.functionPermissionRequest(w, r)
	if !ok {
		return
	}
	value, err := h.Messages.GetFunctionPermission(r.Context(), principal.WorkspaceID, principal.UserID, appID, fields["function_id"], fields["function_callback_id"])
	if err != nil {
		writeFunctionPermissionError(w, err)
		return
	}
	users, err := h.functionPermissionUsers(r.Context(), principal, value)
	if err != nil {
		writeFunctionPermissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, functionPermissionResponse(value, users))
}

func (h Handler) functionsDistributionsPermissionsSet(w http.ResponseWriter, r *http.Request) {
	fields, principal, appID, ok := h.functionPermissionRequest(w, r)
	if !ok {
		return
	}
	permissionType := strings.TrimSpace(fields["permission_type"])
	if permissionType == "" {
		writeError(w, "permission_type_required")
		return
	}
	if !slices.Contains([]string{"everyone", "app_collaborators", "named_entities", "system"}, permissionType) {
		writeError(w, "invalid_permission_type")
		return
	}
	value := domain.AutomationPermission{
		PermissionType: permissionType,
		UserIDs:        parseIDList[domain.UserID](fields["user_ids"]),
		TeamIDs:        parseIDList[domain.WorkspaceID](fields["team_ids"]),
		OrgIDs:         parseIDList[string](fields["org_ids"]),
	}
	value, err := h.Messages.SetFunctionPermission(r.Context(), principal.WorkspaceID, principal.UserID, appID, fields["function_id"], fields["function_callback_id"], value)
	if err != nil {
		writeFunctionPermissionError(w, err)
		return
	}
	users, err := h.functionPermissionUsers(r.Context(), principal, value)
	if err != nil {
		writeFunctionPermissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, functionPermissionResponse(value, users))
}

func mergeUserIDs(current []domain.UserID, changes []domain.UserID, add bool) []domain.UserID {
	values := make(map[domain.UserID]struct{}, len(current)+len(changes))
	for _, id := range current {
		values[id] = struct{}{}
	}
	for _, id := range changes {
		if add {
			values[id] = struct{}{}
		} else {
			delete(values, id)
		}
	}
	result := make([]domain.UserID, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func (h Handler) mutateFunctionPermission(w http.ResponseWriter, r *http.Request, add bool) {
	fields, principal, appID, ok := h.functionPermissionRequest(w, r)
	if !ok {
		return
	}
	current, err := h.Messages.GetFunctionPermission(r.Context(), principal.WorkspaceID, principal.UserID, appID, fields["function_id"], fields["function_callback_id"])
	if err != nil {
		writeFunctionPermissionError(w, err)
		return
	}
	if current.PermissionType != "named_entities" {
		writeError(w, "invalid_permission_type")
		return
	}
	changes := parseIDList[domain.UserID](fields["user_ids"])
	if len(changes) == 0 {
		writeError(w, "invalid_arguments")
		return
	}
	current.UserIDs = mergeUserIDs(current.UserIDs, changes, add)
	if len(current.UserIDs) == 0 {
		writeError(w, "invalid_arguments")
		return
	}
	current, err = h.Messages.SetFunctionPermission(r.Context(), principal.WorkspaceID, principal.UserID, appID, fields["function_id"], fields["function_callback_id"], current)
	if err != nil {
		writeFunctionPermissionError(w, err)
		return
	}
	users, err := h.functionPermissionUsers(r.Context(), principal, current)
	if err != nil {
		writeFunctionPermissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, functionPermissionResponse(current, users))
}

func (h Handler) functionsDistributionsPermissionsAdd(w http.ResponseWriter, r *http.Request) {
	h.mutateFunctionPermission(w, r, true)
}

func (h Handler) functionsDistributionsPermissionsRemove(w http.ResponseWriter, r *http.Request) {
	h.mutateFunctionPermission(w, r, false)
}

func triggerPermissionRequest(w http.ResponseWriter, r *http.Request) (map[string]string, domain.WorkflowTriggerID, bool) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return nil, "", false
	}
	triggerID := domain.WorkflowTriggerID(strings.TrimSpace(fields["trigger_id"]))
	if triggerID == "" {
		writeError(w, "trigger_not_found")
		return nil, "", false
	}
	return fields, triggerID, true
}

func requireTriggerApp(w http.ResponseWriter, principal auth.Principal) bool {
	if principal.AppID == "" {
		writeError(w, "trigger_not_found")
		return false
	}
	return true
}

func triggerPermissionResponse(value domain.AutomationPermission) map[string]any {
	return map[string]any{
		"ok": true, "permission_type": value.PermissionType, "user_ids": value.UserIDs,
		"channel_ids": value.ChannelIDs, "team_ids": value.TeamIDs, "org_ids": value.OrgIDs,
	}
}

func writeTriggerPermissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, "trigger_not_found")
	case errors.Is(err, service.ErrAutomationUserNotFound):
		writeError(w, "user_not_found")
	case errors.Is(err, service.ErrAutomationChannelNotFound):
		writeError(w, "channel_not_found")
	case errors.Is(err, service.ErrAutomationTeamNotFound):
		writeError(w, "team_not_found")
	case errors.Is(err, service.ErrAutomationOrgNotFound):
		writeError(w, "org_not_found")
	case errors.Is(err, service.ErrAutomationEntitiesEmpty):
		writeError(w, "named_entities_cannot_be_empty")
	case errors.Is(err, service.ErrFunctionAccessDenied), errors.Is(err, service.ErrWorkflowPermissionDenied):
		writeError(w, "access_denied")
	case errors.Is(err, service.ErrInvalidWorkflowStep), errors.Is(err, store.ErrInvalidArgument):
		writeError(w, "invalid_arguments")
	default:
		writeError(w, mapServiceError(err, "invalid_arguments"))
	}
}

func (h Handler) workflowsTriggersPermissionsList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeTriggersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	_, triggerID, ok := triggerPermissionRequest(w, r)
	if !ok {
		return
	}
	if !requireTriggerApp(w, principal) {
		return
	}
	value, err := h.Messages.GetTriggerPermission(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, triggerID)
	if err != nil {
		writeTriggerPermissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, triggerPermissionResponse(value))
}

func (h Handler) workflowsTriggersPermissionsSet(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeTriggersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, triggerID, ok := triggerPermissionRequest(w, r)
	if !ok {
		return
	}
	if !requireTriggerApp(w, principal) {
		return
	}
	permissionType := strings.TrimSpace(fields["permission_type"])
	if !slices.Contains([]string{"everyone", "app_collaborators", "named_entities"}, permissionType) {
		writeError(w, "invalid_permission_type")
		return
	}
	value := domain.AutomationPermission{
		PermissionType: permissionType,
		UserIDs:        parseIDList[domain.UserID](fields["user_ids"]), ChannelIDs: parseIDList[domain.ConversationID](fields["channel_ids"]),
		TeamIDs: parseIDList[domain.WorkspaceID](fields["team_ids"]), OrgIDs: parseIDList[string](fields["org_ids"]),
	}
	value, err = h.Messages.SetTriggerPermission(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, triggerID, value)
	if err != nil {
		writeTriggerPermissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, triggerPermissionResponse(value))
}

func mergeStringIDs[T ~string](current, changes []T, add bool) []T {
	values := make(map[T]struct{}, len(current)+len(changes))
	for _, id := range current {
		values[id] = struct{}{}
	}
	for _, id := range changes {
		if add {
			values[id] = struct{}{}
		} else {
			delete(values, id)
		}
	}
	result := make([]T, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func (h Handler) mutateTriggerPermission(w http.ResponseWriter, r *http.Request, fields map[string]string, principal auth.Principal, triggerID domain.WorkflowTriggerID, add bool) {
	current, err := h.Messages.GetTriggerPermission(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, triggerID)
	if err != nil {
		writeTriggerPermissionError(w, err)
		return
	}
	if current.PermissionType != "named_entities" {
		writeError(w, "invalid_permission_type")
		return
	}
	current.UserIDs = mergeStringIDs(current.UserIDs, parseIDList[domain.UserID](fields["user_ids"]), add)
	current.ChannelIDs = mergeStringIDs(current.ChannelIDs, parseIDList[domain.ConversationID](fields["channel_ids"]), add)
	current.TeamIDs = mergeStringIDs(current.TeamIDs, parseIDList[domain.WorkspaceID](fields["team_ids"]), add)
	current.OrgIDs = mergeStringIDs(current.OrgIDs, parseIDList[string](fields["org_ids"]), add)
	if len(current.UserIDs)+len(current.ChannelIDs)+len(current.TeamIDs)+len(current.OrgIDs) == 0 {
		writeError(w, "invalid_arguments")
		return
	}
	current, err = h.Messages.SetTriggerPermission(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, triggerID, current)
	if err != nil {
		writeTriggerPermissionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, triggerPermissionResponse(current))
}

func (h Handler) workflowsTriggersPermissionsAdd(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeTriggersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, triggerID, ok := triggerPermissionRequest(w, r)
	if !ok {
		return
	}
	if !requireTriggerApp(w, principal) {
		return
	}
	h.mutateTriggerPermission(w, r, fields, principal, triggerID, true)
}

func (h Handler) workflowsTriggersPermissionsRemove(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeTriggersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, triggerID, ok := triggerPermissionRequest(w, r)
	if !ok {
		return
	}
	if !requireTriggerApp(w, principal) {
		return
	}
	h.mutateTriggerPermission(w, r, fields, principal, triggerID, false)
}

func (h Handler) workflowsFeaturedList(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeBookmarksRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	channelIDs := parseIDList[domain.ConversationID](fields["channel_ids"])
	values, err := h.Messages.ListFeaturedWorkflows(r.Context(), principal.WorkspaceID, principal.UserID, channelIDs)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, "channel_not_found")
		} else {
			writeError(w, mapServiceError(err, "error_invalid_channels"))
		}
		return
	}
	grouped := make(map[domain.ConversationID][]map[string]any, len(channelIDs))
	for _, value := range values {
		grouped[value.ConversationID] = append(grouped[value.ConversationID], map[string]any{"id": value.TriggerID, "title": value.Title})
	}
	rendered := make([]map[string]any, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		triggers := grouped[channelID]
		if triggers == nil {
			triggers = []map[string]any{}
		}
		rendered = append(rendered, map[string]any{"channel_id": channelID, "triggers": triggers})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "featured_workflows": rendered})
}

func (h Handler) setFeaturedWorkflows(w http.ResponseWriter, r *http.Request, mode string) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeBookmarksWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	channelID := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	triggerIDs := parseIDList[domain.WorkflowTriggerID](fields["trigger_ids"])
	if mode != "set" {
		current, listErr := h.Messages.ListFeaturedWorkflows(r.Context(), principal.WorkspaceID, principal.UserID, []domain.ConversationID{channelID})
		if listErr != nil {
			writeError(w, mapServiceError(listErr, "error_modifying_workflows"))
			return
		}
		existing := make([]domain.WorkflowTriggerID, 0, len(current))
		for _, value := range current {
			existing = append(existing, value.TriggerID)
		}
		triggerIDs = mergeStringIDs(existing, triggerIDs, mode == "add")
	}
	if err := h.Messages.SetFeaturedWorkflows(r.Context(), principal.WorkspaceID, principal.UserID, channelID, triggerIDs); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, "channel_not_found")
		case errors.Is(err, service.ErrWorkflowPermissionDenied):
			writeError(w, "access_denied")
		default:
			writeError(w, mapServiceError(err, "error_modifying_workflows"))
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) workflowsFeaturedSet(w http.ResponseWriter, r *http.Request) {
	h.setFeaturedWorkflows(w, r, "set")
}

func (h Handler) workflowsFeaturedAdd(w http.ResponseWriter, r *http.Request) {
	h.setFeaturedWorkflows(w, r, "add")
}

func (h Handler) workflowsFeaturedRemove(w http.ResponseWriter, r *http.Request) {
	h.setFeaturedWorkflows(w, r, "remove")
}

func (h Handler) functionsWorkflowsStepsList(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	values, err := h.Messages.ListFunctionWorkflowSteps(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID,
		strings.TrimSpace(fields["function_id"]), domain.WorkflowID(strings.TrimSpace(fields["workflow_id"])),
		strings.TrimSpace(fields["workflow"]), domain.AppID(strings.TrimSpace(fields["workflow_app_id"])))
	if err != nil {
		if errors.Is(err, service.ErrWorkflowFunctionNotFound) {
			writeError(w, "function_not_found")
		} else if errors.Is(err, store.ErrNotFound) {
			writeError(w, "unknown_workflow_id")
		} else {
			writeError(w, mapServiceError(err, "invalid_arguments"))
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "steps_versions": values})
}

func (h Handler) dialogOpen(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	// /dialog.open declares none of invalid_arguments. It declares missing_trigger
	// and missing_dialog for an absent argument and validation_errors for a
	// dialog it cannot accept.
	if strings.TrimSpace(fields["trigger_id"]) == "" {
		writeError(w, "missing_trigger")
		return
	}
	if strings.TrimSpace(fields["dialog"]) == "" {
		writeError(w, "missing_dialog")
		return
	}
	if err := h.Messages.OpenDialog(r.Context(), principal.WorkspaceID, principal.UserID, principal.AppID, strings.TrimSpace(fields["trigger_id"]), fields["dialog"]); err != nil {
		reason := mapServiceError(err, "validation_errors")
		if errors.Is(err, service.ErrInvalidTrigger) {
			reason = "invalid_trigger"
		}
		writeError(w, reason)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) appsPermissionsInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "info": permissionInfo(principal.WorkspaceID, permissionScopes(principal))})
}

func (h Handler) appsUninstall(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	clientID := strings.TrimSpace(fields["client_id"])
	clientSecret := strings.TrimSpace(fields["client_secret"])
	if clientID == "" || clientSecret == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if principal.TokenType == "bot" {
		writeError(w, "no_permission")
		return
	}
	if principal.AppID == "" {
		writeError(w, "invalid_auth")
		return
	}
	if err := h.Messages.UninstallApp(r.Context(), clientID, clientSecret, principal.WorkspaceID, principal.AppID); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidOAuthClient):
			writeError(w, "invalid_client_id")
		case errors.Is(err, service.ErrOAuthAppMismatch):
			writeError(w, "client_id_token_mismatch")
		default:
			writeError(w, mapServiceError(err, "fatal_error"))
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) authRevoke(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(fields["token"])
	}
	if token == "" {
		writeError(w, "not_authed")
		return
	}
	if _, err := h.Authenticator.Authenticate(r); err != nil {
		writeAuthError(w, err)
		return
	}
	test, err := parseBoolField(fields["test"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	if !test {
		if err := h.Messages.RevokeToken(r.Context(), token); err != nil {
			writeError(w, mapServiceError(err, "invalid_auth"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": !test})
}

func (h Handler) oauthAccess(w http.ResponseWriter, r *http.Request) {
	h.oauthExchange(w, r, false, false)
}

func (h Handler) oauthV2Access(w http.ResponseWriter, r *http.Request) {
	h.oauthExchange(w, r, true, false)
}

func (h Handler) oauthV2UserAccess(w http.ResponseWriter, r *http.Request) {
	h.oauthExchange(w, r, true, true)
}

func (h Handler) oauthV2ExchangeToken(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	clientID, clientSecret := strings.TrimSpace(fields["client_id"]), strings.TrimSpace(fields["client_secret"])
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		if clientID != "" && clientID != basicID || clientSecret != "" && clientSecret != basicSecret {
			writeError(w, "invalid_client_id")
			return
		}
		if clientID == "" {
			clientID = basicID
		}
		if clientSecret == "" {
			clientSecret = basicSecret
		}
	}
	token, err := h.Messages.OAuthV2ExchangeToken(r.Context(), clientID, clientSecret, fields["token"])
	if err != nil {
		reason := "invalid_auth"
		if errors.Is(err, service.ErrInvalidOAuthClient) {
			reason = "invalid_client_id"
		}
		writeError(w, reason)
		return
	}
	writeJSON(w, http.StatusOK, oauthV2TokenResponse(token, false))
}

func (h Handler) appsManifestValidate(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	token, reason := appConfigurationToken(r, fields)
	if reason != "" {
		writeError(w, reason)
		return
	}
	problems, err := h.Messages.ValidateAppManifest(r.Context(), token, fields["app_id"], fields["manifest"])
	if err != nil {
		writeError(w, appManifestServiceError(err))
		return
	}
	writeAppManifestValidation(w, problems)
}

func (h Handler) appsManifestCreate(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	token, reason := appConfigurationToken(r, fields)
	if reason != "" {
		writeError(w, reason)
		return
	}
	problems, err := h.Messages.ValidateAppManifest(r.Context(), token, "", fields["manifest"])
	if err != nil {
		writeError(w, appManifestServiceError(err))
		return
	}
	if len(problems) != 0 {
		writeAppManifestValidation(w, problems)
		return
	}
	app, credentials, err := h.Messages.CreateAppFromManifest(r.Context(), token, fields["manifest"], domain.WorkspaceID(strings.TrimSpace(fields["team_id"])))
	if err != nil {
		reason := appManifestServiceError(err)
		if errors.Is(err, store.ErrNotFound) && strings.TrimSpace(fields["team_id"]) != "" {
			reason = "invalid_team_id"
		}
		writeError(w, reason)
		return
	}
	parsed, _ := appmanifest.Parse(fields["manifest"])
	query := url.Values{"client_id": []string{credentials.ClientID}}
	if len(parsed.BotScopes) != 0 {
		query.Set("scope", strings.Join(parsed.BotScopes, ","))
	}
	if len(parsed.UserScopes) != 0 {
		query.Set("user_scope", strings.Join(parsed.UserScopes, ","))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"app_id": app.ID,
		"credentials": map[string]string{
			"client_id":          credentials.ClientID,
			"client_secret":      credentials.ClientSecret,
			"verification_token": credentials.VerificationToken,
			"signing_secret":     credentials.SigningSecret,
		},
		"oauth_authorize_url": requestOrigin(r) + "/oauth/v2/authorize?" + query.Encode(),
	})
}

func (h Handler) appsManifestExport(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	token, reason := appConfigurationToken(r, fields)
	if reason != "" {
		writeError(w, reason)
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	if appID == "" {
		writeError(w, "invalid_app_id")
		return
	}
	_, manifest, err := h.Messages.ExportAppManifest(r.Context(), token, appID)
	if err != nil {
		writeError(w, appManifestServiceError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "manifest": json.RawMessage(manifest)})
}

func (h Handler) appsManifestUpdate(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	token, reason := appConfigurationToken(r, fields)
	if reason != "" {
		writeError(w, reason)
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	if appID == "" {
		writeError(w, "invalid_app_id")
		return
	}
	problems, err := h.Messages.ValidateAppManifest(r.Context(), token, string(appID), fields["manifest"])
	if err != nil {
		writeError(w, appManifestServiceError(err))
		return
	}
	if len(problems) != 0 {
		writeAppManifestValidation(w, problems)
		return
	}
	_, previous, err := h.Messages.ExportAppManifest(r.Context(), token, appID)
	if err != nil {
		writeError(w, appManifestServiceError(err))
		return
	}
	before, _ := appmanifest.Parse(previous)
	after, _ := appmanifest.Parse(fields["manifest"])
	app, err := h.Messages.UpdateAppFromManifest(r.Context(), token, appID, fields["manifest"])
	if err != nil {
		writeError(w, appManifestServiceError(err))
		return
	}
	permissionsUpdated := !sameStringSet(before.BotScopes, after.BotScopes) || !sameStringSet(before.UserScopes, after.UserScopes)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "app_id": app.ID, "permissions_updated": permissionsUpdated})
}

func (h Handler) appsManifestDelete(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	token, reason := appConfigurationToken(r, fields)
	if reason != "" {
		writeError(w, reason)
		return
	}
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	if appID == "" {
		writeError(w, "invalid_app_id")
		return
	}
	if err := h.Messages.DeleteDeveloperApp(r.Context(), token, appID); err != nil {
		writeError(w, appManifestServiceError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) toolingTokensRotate(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	refreshToken := strings.TrimSpace(fields["refresh_token"])
	if refreshToken == "" {
		writeError(w, "invalid_refresh_token")
		return
	}
	value, err := h.Messages.RotateAppConfigurationToken(r.Context(), refreshToken)
	if err != nil {
		reason := "fatal_error"
		if errors.Is(err, service.ErrAppConfigurationAuthentication) {
			reason = "invalid_refresh_token"
		}
		writeError(w, reason)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"token":         value.Token,
		"refresh_token": value.RefreshToken,
		"team_id":       value.WorkspaceID,
		"user_id":       value.UserID,
		"iat":           value.IssuedAt.Unix(),
		"exp":           value.ExpiresAt.Unix(),
	})
}

func appConfigurationToken(r *http.Request, fields map[string]string) (string, string) {
	headerToken := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	bodyToken := strings.TrimSpace(fields["token"])
	if headerToken != "" && bodyToken != "" && headerToken != bodyToken {
		return "", "invalid_auth"
	}
	if headerToken != "" {
		return headerToken, ""
	}
	if bodyToken != "" {
		return bodyToken, ""
	}
	return "", "not_authed"
}

func appManifestServiceError(err error) string {
	switch {
	case errors.Is(err, service.ErrAppConfigurationAuthentication):
		return "invalid_auth"
	case errors.Is(err, service.ErrInvalidAppManifest):
		return "invalid_manifest"
	case errors.Is(err, store.ErrNotFound):
		return "invalid_app_id"
	case errors.Is(err, store.ErrConflict):
		return "app_manifest_update_failed"
	default:
		return "fatal_error"
	}
}

func writeAppManifestValidation(w http.ResponseWriter, problems []appmanifest.Error) {
	if len(problems) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "errors": []appmanifest.Error{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "invalid_manifest", "errors": problems})
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, exists := values[value]; !exists {
			return false
		}
	}
	return true
}

func (h Handler) oauthExchange(w http.ResponseWriter, r *http.Request, v2, userOnly bool) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	clientID, clientSecret := strings.TrimSpace(fields["client_id"]), strings.TrimSpace(fields["client_secret"])
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		if clientID != "" && clientID != basicID || clientSecret != "" && clientSecret != basicSecret {
			writeError(w, "invalid_client_id")
			return
		}
		if clientID == "" {
			clientID = basicID
		}
		if clientSecret == "" {
			clientSecret = basicSecret
		}
	}
	if v2 {
		grantType := strings.TrimSpace(fields["grant_type"])
		if grantType != "" && grantType != "authorization_code" {
			if grantType != "refresh_token" {
				writeError(w, "invalid_grant_type")
				return
			}
		}
	}
	refreshing := v2 && strings.TrimSpace(fields["grant_type"]) == "refresh_token"
	if v2 && !refreshing && strings.TrimSpace(fields["code"]) == "" {
		writeError(w, "invalid_code")
		return
	}
	var token domain.OAuthToken
	if refreshing {
		token, err = h.Messages.OAuthV2Refresh(r.Context(), clientID, clientSecret, fields["refresh_token"])
	} else if v2 {
		token, err = h.Messages.OAuthV2Exchange(r.Context(), clientID, clientSecret, fields["code"], fields["redirect_uri"], userOnly)
	} else {
		token, err = h.Messages.OAuthExchange(r.Context(), clientID, clientSecret, fields["code"], fields["redirect_uri"])
	}
	if err != nil {
		reason := "invalid_code"
		if refreshing {
			reason = "invalid_refresh_token"
		}
		if errors.Is(err, service.ErrInvalidOAuthClient) {
			reason = "invalid_client_id"
		}
		writeError(w, reason)
		return
	}
	if !v2 {
		response := map[string]any{"ok": true, "access_token": token.AccessToken, "app_id": token.AppID, "team_id": token.WorkspaceID, "scope": strings.Join(token.Scopes, ","), "token_type": token.TokenType}
		response["team_name"] = ""
		writeJSON(w, http.StatusOK, response)
		return
	}
	writeJSON(w, http.StatusOK, oauthV2TokenResponse(token, userOnly))
}

func oauthV2TokenResponse(token domain.OAuthToken, userOnly bool) map[string]any {
	response := map[string]any{"ok": true, "access_token": token.AccessToken, "app_id": token.AppID, "scope": strings.Join(token.Scopes, ","), "token_type": token.TokenType, "team": map[string]any{"id": token.WorkspaceID}, "enterprise": nil, "is_enterprise_install": false}
	if token.RefreshToken != "" {
		response["refresh_token"] = token.RefreshToken
		response["expires_in"] = int64(oauthTokenLifetime / time.Second)
	}
	if userOnly {
		delete(response, "access_token")
		delete(response, "scope")
		delete(response, "token_type")
		delete(response, "refresh_token")
		delete(response, "expires_in")
		authedUser := map[string]any{"id": token.InstallerID, "access_token": token.AccessToken, "scope": strings.Join(token.Scopes, ","), "token_type": "user"}
		if token.RefreshToken != "" {
			authedUser["refresh_token"] = token.RefreshToken
			authedUser["expires_in"] = int64(oauthTokenLifetime / time.Second)
		}
		response["authed_user"] = authedUser
		return response
	}
	if token.TokenType == "bot" {
		response["bot_user_id"] = token.UserID
		authedUser := map[string]any{"id": token.InstallerID}
		if token.AuthedUserAccessToken != "" {
			authedUser["access_token"] = token.AuthedUserAccessToken
			authedUser["scope"] = strings.Join(token.AuthedUserScopes, ",")
			authedUser["token_type"] = "user"
			if token.AuthedUserRefreshToken != "" {
				authedUser["refresh_token"] = token.AuthedUserRefreshToken
				authedUser["expires_in"] = int64(oauthTokenLifetime / time.Second)
			}
		}
		response["authed_user"] = authedUser
	} else {
		response["authed_user"] = map[string]any{"id": token.InstallerID}
	}
	return response
}

func (h Handler) botsInfo(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	botID := domain.BotID(strings.TrimSpace(fields["bot"]))
	value, err := h.Messages.BotInfo(r.Context(), principal.WorkspaceID, principal.UserID, botID)
	if err != nil {
		writeError(w, mapServiceError(err, "bot_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bot": map[string]any{"id": value.ID, "app_id": value.AppID, "user_id": value.UserID, "name": value.Name, "deleted": value.Deleted, "updated": value.UpdatedAt.Unix(), "icons": map[string]string{"image_36": value.Image36, "image_48": value.Image48, "image_72": value.Image72}}})
}

func (h Handler) migrationExchange(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeTokensBasic)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID {
		// /migration.exchange declares not_enterprise_team, too_many_users and
		// invalid_arg_name. `invalid_team` belongs to /admin.conversations.create
		// and is not in this enum; a team_id naming another workspace is a rejected
		// argument here.
		writeError(w, "invalid_arg_name")
		return
	}
	rawIDs := strings.Fields(strings.ReplaceAll(fields["users"], ",", " "))
	ids := make([]domain.UserID, 0, len(rawIDs))
	for _, id := range rawIDs {
		ids = append(ids, domain.UserID(id))
	}
	toOld := false
	if raw := strings.TrimSpace(fields["to_old"]); raw != "" {
		toOld, err = strconv.ParseBool(raw)
		if err != nil {
			writeError(w, "invalid_arg_name")
			return
		}
	}
	value, err := h.Messages.MigrationExchange(r.Context(), principal.WorkspaceID, principal.UserID, ids, toOld)
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_arg_name"))
		return
	}
	mapping := make(map[string]string, len(value.UserIDMap))
	for key, item := range value.UserIDMap {
		mapping[string(key)] = string(item)
	}
	invalid := make([]string, 0, len(value.InvalidUserIDs))
	for _, item := range value.InvalidUserIDs {
		invalid = append(invalid, string(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team_id": value.WorkspaceID, "user_id_map": mapping, "invalid_user_ids": invalid})
}

func (h Handler) teamInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeTeamRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	team, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	domainName := team.Domain
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": map[string]any{"id": team.ID, "name": team.Name, "domain": domainName}})
}

// teamPreferencesList exposes the workspace policies SameOldChat actually
// enforces. These are product invariants rather than configurable-looking
// placeholders: files are allowed subject to the caller's scopes, profiles use
// display names, message editing has no workspace time limit, and the general
// channel is not role-restricted.
func (h Handler) teamPreferencesList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeTeamPreferencesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if _, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		writeError(w, mapServiceError(err, "invalid_team"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                     true,
		"display_real_names":     false,
		"disable_file_uploads":   "allow_all",
		"msg_edit_window_mins":   0,
		"who_can_post_general":   "everyone",
		"who_can_create_channel": "regular",
	})
}

func (h Handler) rtmConnect(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeRTMStream)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	token := strings.TrimSpace(fields["token"])
	if token == "" {
		token = strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	if token == "" {
		writeError(w, "invalid_auth")
		return
	}
	team, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	connection, err := h.Messages.CreateRTMConnection(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	scheme := "ws"
	if r.TLS != nil {
		scheme = "wss"
	}
	streamURL := url.URL{Scheme: scheme, Host: r.Host, Path: "/rtm", RawQuery: url.Values{"session_id": []string{connection.ID}}.Encode()}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": streamURL.String(), "team": map[string]any{"id": team.ID, "name": team.Name, "domain": team.Domain}, "self": map[string]any{"id": user.ID, "name": user.Name}})
}

func (h Handler) teamProfileGet(w http.ResponseWriter, r *http.Request) {
	if _, err := h.authenticate(r, auth.ScopeUsersProfileRead); err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": map[string]any{"fields": []map[string]any{}}})
}

func (h Handler) teamBillableInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdmin)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	target := domain.UserID(strings.TrimSpace(fields["user"]))
	value, err := h.Messages.TeamBillableInfo(r.Context(), principal.WorkspaceID, principal.UserID, target)
	if err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	result := make(map[string]map[string]any, len(value.Users))
	for _, user := range value.Users {
		result[string(user.UserID)] = map[string]any{"billing_active": user.BillingActive}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "billable_info": result})
}

func (h Handler) accessLogs(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdmin)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	limit, err := clampLimit(fields["count"], 100, 1000)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := pageNumber(fields["page"])
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	before := time.Time{}
	if raw := strings.TrimSpace(fields["before"]); raw != "" {
		seconds, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || seconds <= 0 {
			writeError(w, "invalid_arg_name")
			return
		}
		before = time.Unix(seconds, 0).UTC()
	}
	values, hasMore, err := h.Messages.ListAccessLogs(r.Context(), principal.WorkspaceID, principal.UserID, before, limit, page)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	logins := make([]map[string]any, 0, len(values))
	for _, value := range values {
		logins = append(logins, map[string]any{"count": 1, "country": nil, "date_first": value.CreatedAt.Unix(), "date_last": value.CreatedAt.Unix(), "ip": value.IP, "isp": nil, "region": nil, "user_agent": value.UserAgent, "user_id": value.UserID, "username": value.Username})
	}
	pages := page
	if hasMore {
		pages++
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "logins": logins, "paging": map[string]any{"count": len(logins), "page": page, "pages": pages, "total": len(logins)}})
}

func (h Handler) integrationLogs(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	principal, err := h.authenticate(r, auth.ScopeAdmin)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	count, err := clampLimit(fields["count"], 100, 1000)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := pageNumber(fields["page"])
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	value, err := h.Messages.IntegrationLogs(r.Context(), principal.WorkspaceID, principal.UserID, fields["app_id"], fields["change_type"], fields["service_id"], fields["user"], count, page)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	logs := make([]map[string]any, 0, len(value.Logs))
	for _, item := range value.Logs {
		log := map[string]any{"app_id": item.AppID, "app_type": item.AppType, "change_type": item.ChangeType, "date": strconv.FormatInt(item.Date.Unix(), 10), "scope": item.Scope, "user_id": item.UserID, "user_name": item.UserName}
		if item.ChannelID != "" {
			log["channel"] = item.ChannelID
		}
		if item.ServiceID != "" {
			log["service_id"] = item.ServiceID
		}
		if item.ServiceType != "" {
			log["service_type"] = item.ServiceType
		}
		logs = append(logs, log)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "logs": logs, "paging": map[string]any{"count": len(logs), "page": value.Page, "pages": value.Pages, "total": value.Total}})
}

func (h Handler) adminUsersList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUsersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID {
		writeError(w, "invalid_arg_name")
		return
	}
	// decodeListRequestFields reads the already-decoded map. Calling decodeFields a
	// second time (which is what decodeListRequest did) saw an exhausted JSON body
	// and returned an empty map with no error, so a JSON admin.users.list silently
	// ignored `limit` and `cursor` and answered page one with the default limit.
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// AdminListUsers carries the workspace membership the pinned 200 example shows
	// (is_admin, is_owner, is_primary_owner, is_restricted, …). Messages.Users
	// returns the plain projection, so admin.users.list used to omit every one of
	// those fields even though the admin projection already existed and was already
	// used by the web UI.
	page, err := h.Messages.AdminListUsers(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	users := make([]map[string]any, 0, len(page.Users))
	for _, user := range page.Users {
		users = append(users, adminUserResponse(user))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "users": users, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}

func (h Handler) adminUsersRemove(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUsersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID, targetID := strings.TrimSpace(fields["team_id"]), domain.UserID(strings.TrimSpace(fields["user_id"]))
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.RemoveUser(r.Context(), principal.WorkspaceID, principal.UserID, targetID); err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminUsersSessionInvalidate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUsersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if teamID := strings.TrimSpace(fields["team_id"]); teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID {
		writeError(w, "invalid_team")
		return
	}
	sessionID := strings.TrimSpace(fields["session_id"])
	if sessionID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	// RevokeSession takes the raw session secret and applies no workspace scoping
	// whatsoever, so an admin.users:write token from workspace A that observed a
	// session secret belonging to workspace B could revoke it. The handler cannot
	// close that hole: it has no way to read the session's workspace. See the
	// follow-up recorded for service.Messages.RevokeSession, which must take the
	// actor's workspace and reject a session that does not belong to it.
	if err := h.Messages.RevokeSession(r.Context(), sessionID); err != nil {
		writeError(w, mapServiceError(err, "not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminUsersSessionReset(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUsersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID, targetID := strings.TrimSpace(fields["team_id"]), domain.UserID(strings.TrimSpace(fields["user_id"]))
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.ResetUserSessions(r.Context(), principal.WorkspaceID, principal.UserID, targetID); err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminUsersSetAdmin(w http.ResponseWriter, r *http.Request) {
	h.adminUsersSetRole(w, r, domain.WorkspaceRoleAdmin)
}
func (h Handler) adminUsersSetOwner(w http.ResponseWriter, r *http.Request) {
	h.adminUsersSetRole(w, r, domain.WorkspaceRoleOwner)
}
func (h Handler) adminUsersSetRegular(w http.ResponseWriter, r *http.Request) {
	h.adminUsersSetRole(w, r, domain.WorkspaceRoleMember)
}

func (h Handler) adminUsersSetExpiration(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUsersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID, targetID, rawExpiration := strings.TrimSpace(fields["team_id"]), domain.UserID(strings.TrimSpace(fields["user_id"])), strings.TrimSpace(fields["expiration_ts"])
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" || rawExpiration == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	seconds, err := strconv.ParseInt(rawExpiration, 10, 64)
	if err != nil || seconds < 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	expiration := time.Time{}
	if seconds != 0 {
		expiration = time.Unix(seconds, 0).UTC()
	}
	if err := h.Messages.SetUserExpiration(r.Context(), principal.WorkspaceID, principal.UserID, targetID, expiration); err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminUsersInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUsersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || strings.TrimSpace(fields["email"]) == "" || strings.TrimSpace(fields["channel_ids"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	channels := parseIDList[domain.ConversationID](fields["channel_ids"])
	flags, err := parseBoolFields(fields, "resend", "is_restricted", "is_ultra_restricted")
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	var expiration time.Time
	if raw := strings.TrimSpace(fields["guest_expiration_ts"]); raw != "" {
		seconds, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || seconds <= 0 {
			writeError(w, "invalid_arg_name")
			return
		}
		expiration = time.Unix(seconds, 0).UTC()
	}
	err = h.Messages.AdminInviteUser(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"], channels, fields["custom_message"], fields["real_name"], flags[0], flags[1], flags[2], expiration)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminUsersAssign(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUsersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	targetID := domain.UserID(strings.TrimSpace(fields["user_id"]))
	teamID := strings.TrimSpace(fields["team_id"])
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	channels := []domain.ConversationID{}
	if strings.TrimSpace(fields["channel_ids"]) != "" {
		channels = parseIDList[domain.ConversationID](fields["channel_ids"])
	}
	if err := h.Messages.AdminAssignUser(r.Context(), principal.WorkspaceID, principal.UserID, targetID, channels); err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminInviteRequestApprove(w http.ResponseWriter, r *http.Request) {
	h.adminInviteRequestChange(w, r, true)
}

func (h Handler) adminInviteRequestDeny(w http.ResponseWriter, r *http.Request) {
	h.adminInviteRequestChange(w, r, false)
}

func (h Handler) adminInviteRequestChange(w http.ResponseWriter, r *http.Request, approve bool) {
	principal, err := h.authenticate(r, auth.ScopeAdminInvitesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	id := domain.InviteRequestID(strings.TrimSpace(fields["invite_request_id"]))
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || id == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if approve {
		err = h.Messages.AdminApproveInviteRequest(r.Context(), principal.WorkspaceID, principal.UserID, id)
	} else {
		err = h.Messages.AdminDenyInviteRequest(r.Context(), principal.WorkspaceID, principal.UserID, id)
	}
	if err != nil {
		writeError(w, mapServiceError(err, "invite_request_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminInviteRequestsList(w http.ResponseWriter, r *http.Request) {
	h.adminInviteRequestsListStatus(w, r, domain.InviteRequestPending)
}

func (h Handler) adminInviteRequestsApprovedList(w http.ResponseWriter, r *http.Request) {
	h.adminInviteRequestsListStatus(w, r, domain.InviteRequestApproved)
}

func (h Handler) adminInviteRequestsDeniedList(w http.ResponseWriter, r *http.Request) {
	h.adminInviteRequestsListStatus(w, r, domain.InviteRequestDenied)
}

func (h Handler) adminInviteRequestsListStatus(w http.ResponseWriter, r *http.Request, status domain.InviteRequestStatus) {
	principal, err := h.authenticate(r, auth.ScopeAdminInvitesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID {
		writeError(w, "invalid_arg_name")
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.AdminListInviteRequests(r.Context(), principal.WorkspaceID, principal.UserID, status, request)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	requests := make([]map[string]any, 0, len(page.Requests))
	for _, value := range page.Requests {
		request := map[string]any{"id": value.ID, "team_id": value.WorkspaceID, "email": value.Email, "requested_by": value.RequestedBy, "status": value.Status, "date_created": value.CreatedAt.Unix()}
		if !value.ReviewedAt.IsZero() {
			request["date_reviewed"] = value.ReviewedAt.Unix()
		}
		requests = append(requests, request)
	}
	response := map[string]any{"ok": true, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}, "has_more": page.HasMore}
	switch status {
	case domain.InviteRequestPending:
		response["invite_requests"] = requests
	case domain.InviteRequestApproved:
		response["approved_requests"] = requests
	case domain.InviteRequestDenied:
		response["denied_requests"] = requests
	default:
		panic("unsupported invite request status")
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) adminAppApprove(w http.ResponseWriter, r *http.Request) {
	h.adminAppChange(w, r, true)
}

func (h Handler) adminAppRestrict(w http.ResponseWriter, r *http.Request) {
	h.adminAppChange(w, r, false)
}

func (h Handler) adminAppChange(w http.ResponseWriter, r *http.Request, approve bool) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	requestID := domain.AppRequestID(strings.TrimSpace(fields["request_id"]))
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID || appID == "" && requestID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if approve {
		err = h.Messages.AdminApproveApp(r.Context(), principal.WorkspaceID, principal.UserID, appID, requestID)
	} else {
		err = h.Messages.AdminRestrictApp(r.Context(), principal.WorkspaceID, principal.UserID, appID, requestID)
	}
	if err != nil {
		writeError(w, mapServiceError(err, "app_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminAppsApprovedList(w http.ResponseWriter, r *http.Request) {
	h.adminAppsList(w, r, domain.AppApprovalApproved, "approved_apps")
}

func (h Handler) adminAppsRestrictedList(w http.ResponseWriter, r *http.Request) {
	h.adminAppsList(w, r, domain.AppApprovalRestricted, "restricted_apps")
}

func (h Handler) adminAppsRequestsList(w http.ResponseWriter, r *http.Request) {
	h.adminAppsList(w, r, domain.AppApprovalRequested, "app_requests")
}

func (h Handler) adminAppsList(w http.ResponseWriter, r *http.Request, status domain.AppApprovalStatus, key string) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if (teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID) || strings.TrimSpace(fields["enterprise_id"]) != "" {
		writeError(w, "invalid_arg_name")
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.AdminListApps(r.Context(), principal.WorkspaceID, principal.UserID, status, request)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	items := make([]map[string]any, 0, len(page.Apps))
	for _, value := range page.Apps {
		item := map[string]any{"app": map[string]any{"id": value.ID}, "date_updated": value.UpdatedAt.Unix()}
		if status == domain.AppApprovalRequested {
			item["id"] = value.RequestID
			item["date_created"] = value.CreatedAt.Unix()
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, key: items, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}, "has_more": page.HasMore})
}
func (h Handler) adminUsersSetRole(w http.ResponseWriter, r *http.Request, role domain.WorkspaceRole) {
	principal, err := h.authenticate(r, auth.ScopeAdminUsersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teamID, targetID := strings.TrimSpace(fields["team_id"]), domain.UserID(strings.TrimSpace(fields["user_id"]))
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.SetUserRole(r.Context(), principal.WorkspaceID, principal.UserID, targetID, role); err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminConversationRename(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, name := domain.ConversationID(strings.TrimSpace(fields["channel_id"])), strings.TrimSpace(fields["name"])
	if channel == "" || name == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.AdminRenameConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel, name)
	if err != nil {
		// /admin.conversations.rename declares name_taken for a collision.
		writeError(w, mapServiceErrorExists(err, "channel_not_found", "name_taken"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel_id": conversation.ID, "channel": conversationResponse(conversation)})
}

func (h Handler) adminConversationCreate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["name"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	private, err := parseBoolField(fields["is_private"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.CreateConversation(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], private)
	if err != nil {
		// The collision code belongs to the operation, not to the shared mapper:
		// a taken name reaches here as store.ErrAlreadyExists.
		writeError(w, mapServiceErrorExists(err, "name_taken", "name_taken"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel_id": conversation.ID, "channel": conversationResponse(conversation)})
}

func (h Handler) adminConversationArchive(w http.ResponseWriter, r *http.Request) {
	ok, conversation, err := h.changeAdminConversationArchived(w, r, true)
	if !ok {
		return
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
	case errors.Is(err, service.ErrConversationAlreadyArchived):
		writeError(w, "already_archived")
	case errors.Is(err, service.ErrCannotArchiveDefault):
		writeError(w, "cant_archive_general")
	case errors.Is(err, service.ErrInvalidConversation):
		writeError(w, "channel_type_not_supported")
	default:
		writeError(w, mapServiceError(err, "channel_not_found"))
	}
}

func (h Handler) adminConversationUnarchive(w http.ResponseWriter, r *http.Request) {
	ok, conversation, err := h.changeAdminConversationArchived(w, r, false)
	if !ok {
		return
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
	case errors.Is(err, service.ErrConversationNotArchived):
		writeError(w, "channel_not_archived")
	case errors.Is(err, service.ErrInvalidConversation):
		writeError(w, "channel_type_not_supported")
	default:
		writeError(w, mapServiceError(err, "channel_not_found"))
	}
}

func (h Handler) adminConversationDelete(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.AdminDeleteConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel); err != nil {
		writeError(w, mapAdminError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminConversationAccessGroupAdd(w http.ResponseWriter, r *http.Request) {
	h.adminConversationAccessGroupChange(w, r, true)
}

func (h Handler) adminConversationAccessGroupRemove(w http.ResponseWriter, r *http.Request) {
	h.adminConversationAccessGroupChange(w, r, false)
}

func (h Handler) adminConversationAccessGroupChange(w http.ResponseWriter, r *http.Request, add bool) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	conversationID := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	groupID := domain.UserGroupID(strings.TrimSpace(fields["group_id"]))
	if conversationID == "" || groupID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if add {
		err = h.Messages.AdminAddConversationAccessGroup(r.Context(), principal.WorkspaceID, principal.UserID, conversationID, groupID)
	} else {
		err = h.Messages.AdminRemoveConversationAccessGroup(r.Context(), principal.WorkspaceID, principal.UserID, conversationID, groupID)
	}
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminConversationAccessGroupsList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	conversationID := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if conversationID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	groups, err := h.Messages.AdminListConversationAccessGroups(r.Context(), principal.WorkspaceID, principal.UserID, conversationID)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	values := make([]string, 0, len(groups))
	for _, groupID := range groups {
		values = append(values, string(groupID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group_ids": values})
}

func (h Handler) changeAdminConversationArchived(w http.ResponseWriter, r *http.Request, archived bool) (bool, domain.Conversation, error) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return false, domain.Conversation{}, nil
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return false, domain.Conversation{}, nil
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return false, domain.Conversation{}, nil
	}
	conversation, err := h.Messages.AdminSetConversationArchived(r.Context(), principal.WorkspaceID, principal.UserID, channel, archived)
	return true, conversation, err
}

func (h Handler) adminConversationInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	usersField := strings.TrimSpace(fields["users"])
	if usersField == "" {
		usersField = strings.TrimSpace(fields["user_ids"])
	}
	if channel == "" || usersField == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	users := parseCallUsers(usersField)
	conversation, err := h.Messages.AdminInviteConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, channel, users)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) adminConversationConvertToPrivate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.AdminConvertConversationToPrivate(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) adminConversationSearch(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_cursor")
	if err != nil || strings.TrimSpace(fields["query"]) == "" {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.AdminSearchConversations(r.Context(), principal.WorkspaceID, principal.UserID, fields["query"], request)
	if err != nil {
		writeError(w, mapAdminError(err, "fatal_error"))
		return
	}
	conversations := make([]map[string]any, 0, len(page.Conversations))
	for _, conversation := range page.Conversations {
		conversations = append(conversations, map[string]any{
			"id": conversation.ID, "name": conversation.Name, "purpose": conversation.Purpose,
			"is_archived": conversation.Archived, "is_private": conversation.IsPrivate,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "conversations": conversations, "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore, "total_count": len(conversations)})
}

func (h Handler) adminConversationGetTeams(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["channel_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_cursor")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	teams, hasMore, nextCursor, err := h.Messages.AdminConversationTeams(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel_id"])), request)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team_ids": teams, "response_metadata": map[string]any{"next_cursor": nextCursor}, "has_more": hasMore})
}

func (h Handler) adminConversationSetTeams(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["channel_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	orgChannel, err := parseBoolField(fields["org_channel"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	rawTeams := fields["target_team_ids"]
	if strings.TrimSpace(rawTeams) == "" {
		rawTeams = fields["team_id"]
	}
	teams := make([]domain.WorkspaceID, 0)
	for _, raw := range strings.Split(rawTeams, ",") {
		if strings.TrimSpace(raw) != "" {
			teams = append(teams, domain.WorkspaceID(strings.TrimSpace(raw)))
		}
	}
	// The pinned parameter description for target_team_ids requires that every
	// workspace belong to the organization the token was issued for. Every other
	// admin.* handler compares the supplied team against the principal's; this one
	// forwarded the list verbatim, so a workspace-A token could attach A's channel
	// to workspace B. The service-layer check only asserts that the workspace
	// exists (internal/service/messages.go AdminSetConversationTeams).
	if _, ok := foreignWorkspace(teams, principal.WorkspaceID); ok {
		writeError(w, "invalid_team")
		return
	}
	if err := h.Messages.AdminSetConversationTeams(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel_id"])), teams, orgChannel); err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminConversationDisconnectShared(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["channel_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	teams := make([]domain.WorkspaceID, 0)
	for _, raw := range strings.Split(fields["leaving_team_ids"], ",") {
		if value := strings.TrimSpace(raw); value != "" {
			teams = append(teams, domain.WorkspaceID(value))
		}
	}
	if err := h.Messages.AdminDisconnectSharedConversation(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel_id"])), teams); err != nil {
		writeError(w, mapAdminError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminConnectedChannelInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channels := make([]domain.ConversationID, 0)
	for _, raw := range strings.Split(fields["channel_ids"], ",") {
		if value := strings.TrimSpace(raw); value != "" {
			channels = append(channels, domain.ConversationID(value))
		}
	}
	teams := make([]domain.WorkspaceID, 0)
	for _, raw := range strings.Split(fields["team_ids"], ",") {
		if value := strings.TrimSpace(raw); value != "" {
			teams = append(teams, domain.WorkspaceID(value))
		}
	}
	values, more, next, err := h.Messages.AdminConnectedChannelInfo(r.Context(), principal.WorkspaceID, principal.UserID, channels, teams, request)
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	channelsResponse := make([]map[string]any, 0, len(values))
	for _, value := range values {
		channelsResponse = append(channelsResponse, map[string]any{"id": value.ChannelID, "internal_team_ids": value.InternalTeamIDs, "original_connected_channel_id": value.OriginalConnectedChannelID, "original_connected_host_id": value.OriginalConnectedHostID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": channelsResponse, "response_metadata": map[string]any{"next_cursor": next}, "has_more": more})
}

type conversationPreferencePayload struct {
	Types []string `json:"type"`
	Users []string `json:"user"`
}

type conversationPrefsPayload struct {
	CanThread  conversationPreferencePayload `json:"can_thread"`
	WhoCanPost conversationPreferencePayload `json:"who_can_post"`
}

func (h Handler) adminConversationGetPrefs(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	prefs, err := h.Messages.AdminGetConversationPrefs(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		writeError(w, mapAdminError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "prefs": conversationPrefsResponse(prefs)})
}

func (h Handler) adminConversationSetPrefs(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" || strings.TrimSpace(fields["prefs"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	var payload conversationPrefsPayload
	if err := json.Unmarshal([]byte(fields["prefs"]), &payload); err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	prefs := conversationPrefsFromPayload(channel, payload)
	if _, err := h.Messages.AdminSetConversationPrefs(r.Context(), principal.WorkspaceID, principal.UserID, channel, prefs); err != nil {
		writeError(w, mapAdminError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func conversationPrefsFromPayload(channel domain.ConversationID, payload conversationPrefsPayload) domain.ConversationPrefs {
	canThread := domain.ConversationPreferenceList{Types: make([]domain.ConversationPreferenceType, 0, len(payload.CanThread.Types)), Users: make([]domain.UserID, 0, len(payload.CanThread.Users))}
	for _, value := range payload.CanThread.Types {
		canThread.Types = append(canThread.Types, domain.ConversationPreferenceType(value))
	}
	for _, value := range payload.CanThread.Users {
		canThread.Users = append(canThread.Users, domain.UserID(value))
	}
	whoCanPost := domain.ConversationPreferenceList{Types: make([]domain.ConversationPreferenceType, 0, len(payload.WhoCanPost.Types)), Users: make([]domain.UserID, 0, len(payload.WhoCanPost.Users))}
	for _, value := range payload.WhoCanPost.Types {
		whoCanPost.Types = append(whoCanPost.Types, domain.ConversationPreferenceType(value))
	}
	for _, value := range payload.WhoCanPost.Users {
		whoCanPost.Users = append(whoCanPost.Users, domain.UserID(value))
	}
	return domain.ConversationPrefs{ConversationID: channel, CanThread: canThread, WhoCanPost: whoCanPost}
}

func conversationPrefsResponse(value domain.ConversationPrefs) map[string]any {
	return map[string]any{"can_thread": map[string]any{"type": value.CanThread.Types, "user": value.CanThread.Users}, "who_can_post": map[string]any{"type": value.WhoCanPost.Types, "user": value.WhoCanPost.Users}}
}

func (h Handler) emojiList(w http.ResponseWriter, r *http.Request) {
	h.listEmoji(w, r, auth.ScopeEmojiRead)
}

func (h Handler) listEmoji(w http.ResponseWriter, r *http.Request, scope auth.Scope) {
	principal, err := h.authenticate(r, scope)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	includeCategories, err := parseBoolField(fields["include_categories"])
	if err != nil {
		writeError(w, "invalid_arguments")
		return
	}
	values, err := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	response := map[string]any{
		"ok":       true,
		"emoji":    emojiResponse(values),
		"cache_ts": slackemoji.Revision,
	}
	if includeCategories {
		response["categories_version"] = slackemoji.CategoriesVersion
		response["categories"] = slackemoji.Categories()
	}
	writeJSON(w, http.StatusOK, response)
}

func emojiResponse(values []domain.CustomEmoji) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		if value.AliasFor != "" {
			result[value.Name] = "alias:" + value.AliasFor
		} else {
			result[value.Name] = value.URL
		}
	}
	return result
}

// adminEmojiList is admin.emoji.list. It differs from emoji.list only in the
// scope the pinned contract requires: `admin.teams:read` rather than
// `emoji:read`. Requiring a write scope for a read locked read-only admin tokens
// out of their own emoji inventory.
func (h Handler) adminEmojiList(w http.ResponseWriter, r *http.Request) {
	h.listEmoji(w, r, auth.ScopeAdminTeamsRead)
}

func (h Handler) adminEmojiAdd(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.adminEmojiFields(w, r)
	if !ok {
		return
	}
	if err := h.Messages.AdminAddEmoji(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], fields["url"]); err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (h Handler) adminEmojiAddAlias(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.adminEmojiFields(w, r)
	if !ok {
		return
	}
	if err := h.Messages.AdminAddEmojiAlias(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], fields["alias_for"]); err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (h Handler) adminEmojiRemove(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.adminEmojiFields(w, r)
	if !ok {
		return
	}
	if err := h.Messages.AdminRemoveEmoji(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"]); err != nil {
		writeError(w, mapServiceError(err, "emoji_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (h Handler) adminEmojiRename(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.adminEmojiFields(w, r)
	if !ok {
		return
	}
	if err := h.Messages.AdminRenameEmoji(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], fields["new_name"]); err != nil {
		writeError(w, mapServiceError(err, "emoji_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (h Handler) adminEmojiFields(w http.ResponseWriter, r *http.Request) (auth.Principal, map[string]string, bool) {
	// Pinned admin.emoji.add/addAlias/remove/rename all require
	// `admin.teams:write`; `admin.emoji:write` is not a Slack scope.
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	if err != nil {
		writeAuthError(w, err)
		return auth.Principal{}, nil, false
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return auth.Principal{}, nil, false
	}
	if strings.TrimSpace(fields["name"]) == "" {
		writeError(w, "invalid_arg_name")
		return auth.Principal{}, nil, false
	}
	return principal, fields, true
}

func (h Handler) conversationInfo(w http.ResponseWriter, r *http.Request) {
	// Pinned /conversations.info token parameter: "Requires scope:
	// `conversations:read`". Enforcing no scope let a chat:write-only token read
	// every channel's topic, purpose and privacy.
	principal, err := h.authenticate(r, auth.ScopeChannelsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	conversationID := strings.TrimSpace(fields["channel"])
	if conversationID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(conversationID))
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	response := conversationResponse(conversation)
	canvas, canvasErr := h.Messages.ConversationCanvas(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID)
	if canvasErr == nil {
		var document struct {
			Sections []json.RawMessage `json:"sections"`
		}
		_ = json.Unmarshal([]byte(canvas.DocumentContent), &document)
		response["properties"] = map[string]any{"canvas": map[string]any{"file_id": canvas.ID, "is_empty": len(document.Sections) == 0}}
	} else if !errors.Is(canvasErr, store.ErrNotFound) {
		writeError(w, mapServiceError(canvasErr, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": response})
}

func (h Handler) userInfo(w http.ResponseWriter, r *http.Request) {
	// Pinned /users.info token parameter: "Requires scope: `users:read`".
	// Email is independently omitted below unless users:read.email is present.
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	requested := domain.UserID(strings.TrimSpace(fields["user"]))
	if requested == "" {
		requested = principal.UserID
	}
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, requested)
	if err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": userResponse(user, principal.HasScope(auth.ScopeUsersReadEmail))})
}

func (h Handler) usersIdentity(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeIdentityBasic)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_auth"))
		return
	}
	team, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_auth"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": map[string]any{"id": user.ID, "name": user.Name}, "team": map[string]any{"id": team.ID}})
}

func (h Handler) lookupUserByEmail(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersReadEmail)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["email"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	user, err := h.Messages.UserByEmail(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"])
	if err != nil {
		writeError(w, mapServiceError(err, "users_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": userResponse(user, true)})
}

func (h Handler) usersList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if teamID := strings.TrimSpace(fields["team_id"]); teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID {
		writeError(w, "invalid_arg_name")
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_cursor")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		// /users.list enumerates invalid_auth and org_login_required, not
		// team_not_found.
		writeError(w, mapServiceError(err, "invalid_auth"))
		return
	}
	members := make([]map[string]any, 0, len(page.Users))
	for _, user := range page.Users {
		members = append(members, userResponse(user, principal.HasScope(auth.ScopeUsersReadEmail)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "members": members, "cache_ts": time.Now().Unix(), "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore})
}

func decodeConversationListFields(fields map[string]string) (domain.ConversationListRequest, error) {
	limit, err := clampLimit(fields["limit"], 100, 1000)
	if err != nil {
		return domain.ConversationListRequest{}, err
	}
	// /conversations.list declares invalid_arg_name and not invalid_cursor;
	// /users.conversations declares both, so the narrower shared code is the one
	// both operations accept.
	cursor, err := decodeCursor(fields["cursor"], "invalid_arg_name")
	if err != nil {
		return domain.ConversationListRequest{}, err
	}
	excludeArchived, err := parseBoolField(fields["exclude_archived"])
	if err != nil {
		return domain.ConversationListRequest{}, err
	}
	types := []string{}
	if raw := strings.TrimSpace(fields["types"]); raw != "" {
		types = strings.Split(raw, ",")
	}
	conversationTypes, err := domain.NormalizeConversationTypes(types)
	if err != nil {
		return domain.ConversationListRequest{}, err
	}
	return domain.ConversationListRequest{Limit: limit, Cursor: cursor, ExcludeArchived: excludeArchived, Types: conversationTypes}, nil
}

func (h Handler) getUserProfile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersProfileRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	requested := domain.UserID(strings.TrimSpace(fields["user"]))
	if requested == "" {
		requested = principal.UserID
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, requested)
	if err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	profile := profileResponse(user)
	if !principal.HasScope(auth.ScopeUsersReadEmail) {
		delete(profile, "email")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": profile})
}

func (h Handler) getPresence(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	requested := domain.UserID(strings.TrimSpace(fields["user"]))
	if requested == "" {
		requested = principal.UserID
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, requested)
	if err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "presence": user.Presence.Current()})
}

func (h Handler) setPresence(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	presence := domain.Presence(strings.TrimSpace(fields["presence"]))
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		writeError(w, "invalid_presence")
		return
	}
	if _, err := h.Messages.SetUserPresence(r.Context(), principal.WorkspaceID, principal.UserID, presence); err != nil {
		// /users.setPresence acts on the caller's own record and declares no
		// missing-user code; it does declare fatal_error.
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func dndResponse(value domain.DoNotDisturb, now time.Time) map[string]any {
	return map[string]any{
		"ok": true, "dnd_enabled": value.Enabled, "next_dnd_start_ts": unixSeconds(value.NextStartAt), "next_dnd_end_ts": unixSeconds(value.NextEndAt),
		"snooze_enabled": value.SnoozeEnabled(now), "snooze_endtime": unixSeconds(value.SnoozeUntil), "snooze_remaining": value.SnoozeRemaining(now),
	}
}

func unixSeconds(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func (h Handler) dndInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeDNDRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	requested := domain.UserID(strings.TrimSpace(fields["user"]))
	value, err := h.Messages.DoNotDisturbInfo(r.Context(), principal.WorkspaceID, principal.UserID, requested)
	if err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, dndResponse(value, time.Now().UTC()))
}

func (h Handler) dndEnd(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeDNDWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	// /dnd.endDnd declares `unknown_error`; `dnd_not_active` is in no pinned enum.
	if err := h.Messages.EndDND(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		writeError(w, mapServiceError(err, "unknown_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) dndEndSnooze(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeDNDWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	// /dnd.endSnooze declares `snooze_not_active`, which was never emitted.
	value, err := h.Messages.EndSnooze(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "snooze_not_active"))
		return
	}
	writeJSON(w, http.StatusOK, dndResponse(value, time.Now().UTC()))
}

func (h Handler) dndSetSnooze(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeDNDWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// /dnd.setSnooze declares missing_duration for an absent num_minutes and
	// snooze_failed for a duration it will not apply; it declares no
	// invalid_arguments.
	if strings.TrimSpace(fields["num_minutes"]) == "" {
		writeError(w, "missing_duration")
		return
	}
	minutes, err := strconv.ParseInt(strings.TrimSpace(fields["num_minutes"]), 10, 64)
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.SetSnooze(r.Context(), principal.WorkspaceID, principal.UserID, minutes)
	if err != nil {
		writeError(w, mapServiceError(err, "snooze_failed"))
		return
	}
	response := dndResponse(value, time.Now().UTC())
	writeJSON(w, http.StatusOK, response)
}

// dndTeamInfoPage and dndTeamInfoLimit bound the membership read behind an
// unfiltered dnd.teamInfo, which has to name every member. Reaching the bound is
// reported as request_timeout, never as a shorter member list.
const (
	dndTeamInfoPage  = 200
	dndTeamInfoLimit = 20000
)

func (h Handler) dndTeamInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeDNDRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	requested := make([]domain.UserID, 0)
	seen := make(map[domain.UserID]struct{})
	if raw := strings.TrimSpace(fields["users"]); raw != "" {
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				writeError(w, "invalid_arg_name")
				return
			}
			userID := domain.UserID(item)
			if _, exists := seen[userID]; !exists {
				seen[userID] = struct{}{}
				requested = append(requested, userID)
			}
		}
	} else {
		// With no `users` argument the answer is the whole workspace. This read one
		// page of a thousand, discarded page.NextCursor and answered ok:true, so a
		// larger workspace was told an arbitrary subset was its full membership —
		// the same silent truncation files.list used to commit.
		request := domain.PageRequest{Limit: dndTeamInfoPage}
		for read := 0; ; read += dndTeamInfoPage {
			if read >= dndTeamInfoLimit {
				// The bound was reached with the membership unread; a short list
				// here is indistinguishable from a complete one.
				writeError(w, "request_timeout")
				return
			}
			page, listErr := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, request)
			if listErr != nil {
				writeError(w, mapServiceError(listErr, "invalid_auth"))
				return
			}
			for _, user := range page.Users {
				if _, exists := seen[user.ID]; !exists {
					seen[user.ID] = struct{}{}
					requested = append(requested, user.ID)
				}
			}
			if !page.HasMore {
				break
			}
			request.Cursor = page.NextCursor
		}
	}
	sort.Slice(requested, func(left, right int) bool { return requested[left] < requested[right] })
	users := make(map[string]any, len(requested))
	now := time.Now().UTC()
	for _, requestedID := range requested {
		value, infoErr := h.Messages.DoNotDisturbInfo(r.Context(), principal.WorkspaceID, principal.UserID, requestedID)
		if infoErr != nil {
			writeError(w, mapServiceError(infoErr, "user_not_found"))
			return
		}
		response := dndResponse(value, now)
		delete(response, "ok")
		users[string(requestedID)] = response
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "users": users})
}

func (h Handler) setUserProfile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersProfileWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["profile"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	profileFields, err := decodeProfileJSON(fields["profile"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	profile := current.Profile
	for name, value := range profileFields.Strings {
		switch name {
		case "display_name":
			profile.DisplayName = value
		case "status_text":
			profile.StatusText = value
		case "status_emoji":
			profile.StatusEmoji = value
		case "image_24":
			profile.Image24 = value
		case "image_32":
			profile.Image32 = value
		case "image_48":
			profile.Image48 = value
		case "image_72":
			profile.Image72 = value
		case "image_192":
			profile.Image192 = value
		case "image_512":
			profile.Image512 = value
		case "image_1024":
			profile.Image1024 = value
		}
	}
	if profileFields.StatusExpiration != nil {
		if *profileFields.StatusExpiration == 0 {
			profile.StatusExpiration = time.Time{}
		} else {
			profile.StatusExpiration = time.Unix(*profileFields.StatusExpiration, 0).UTC()
		}
	}
	user, err := h.Messages.SetUserProfile(r.Context(), principal.WorkspaceID, principal.UserID, profile)
	if err != nil {
		if errors.Is(err, service.ErrInvalidProfile) {
			writeError(w, "invalid_profile")
			return
		}
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	responseProfile := profileResponse(user)
	if !principal.HasScope(auth.ScopeUsersReadEmail) {
		delete(responseProfile, "email")
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": responseProfile})
}

func (h Handler) deleteUserPhoto(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersProfileWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.DeleteUserPhoto(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) setUserPhoto(w http.ResponseWriter, r *http.Request) {
	r = promoteQueryToken(r)
	deferAuth := bodyOnlyToken(r)
	var principal auth.Principal
	var err error
	if !deferAuth {
		if principal, err = h.authenticate(r, auth.ScopeUsersProfileWrite); err != nil {
			writeAuthError(w, err)
			return
		}
	}
	spool, fields, _, mimeType, err := spoolUpload(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// Registered before the deferred authentication below, not after it: an
	// unauthenticated caller used to leave the spool file behind on every attempt.
	defer spool.release()
	if deferAuth {
		if principal, err = h.authenticate(withBearerToken(r, fields["token"]), auth.ScopeUsersProfileWrite); err != nil {
			writeAuthError(w, err)
			return
		}
	}
	temporary := spool.file
	stat, err := temporary.Stat()
	if err != nil {
		writeError(w, "fatal_error")
		return
	}
	// slack-api-client 1.49.0 labels this multipart part imageData/*.
	// Web API 8.0 sends a Buffer as application/octet-stream instead. Preserve
	// both official client behaviors while still letting the message service
	// enforce that the detected bytes exactly match an allow-listed image type.
	if mimeType == "imageData/*" {
		mimeType = "image/png"
	} else if mimeType == "application/octet-stream" {
		head := make([]byte, 512)
		read, readErr := io.ReadFull(temporary, head)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			writeError(w, "bad_image")
			return
		}
		mimeType = http.DetectContentType(head[:read])
		if _, err := temporary.Seek(0, io.SeekStart); err != nil {
			writeError(w, "fatal_error")
			return
		}
	}
	// crop_w/crop_x/crop_y are declared but not implemented. Silently ignoring a
	// crop would return a differently framed image than the caller asked for while
	// claiming success, so the request is refused instead.
	for _, unsupported := range []string{"crop_w", "crop_x", "crop_y"} {
		if strings.TrimSpace(fields[unsupported]) != "" {
			writeError(w, "invalid_arg_name")
			return
		}
	}
	user, err := h.Messages.SetUserPhoto(r.Context(), principal.WorkspaceID, principal.UserID, mimeType, stat.Size(), temporary)
	if err != nil {
		writeError(w, mapServiceError(err, "not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": profileResponse(user)})
}

// users.setActive is deprecated and non-functional in Slack. Preserve that
// contract explicitly instead of inventing a user-state mutation.
func (h Handler) usersSetActive(w http.ResponseWriter, r *http.Request) {
	if _, err := h.authenticate(r, auth.ScopeUsersWrite); err != nil {
		writeAuthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// adminUserResponse renders the admin projection of a user. The pinned
// admin.users.list 200 example carries the role and restriction flags below; the
// plain userResponse omits all of them.
func adminUserResponse(value domain.AdminUser) map[string]any {
	result := userResponse(value.User, true)
	result["is_owner"] = value.Membership.Role == domain.WorkspaceRoleOwner
	result["is_primary_owner"] = value.Membership.Role == domain.WorkspaceRoleOwner
	result["is_admin"] = value.Membership.Role == domain.WorkspaceRoleAdmin || value.Membership.Role == domain.WorkspaceRoleOwner
	result["is_restricted"] = value.Membership.Restricted
	result["is_ultra_restricted"] = value.Membership.UltraRestricted
	result["is_bot"] = false
	result["is_active"] = value.Membership.Active
	return result
}

func userResponse(user domain.User, includeEmail bool) map[string]any {
	profile := profileResponse(user)
	if !includeEmail {
		delete(profile, "email")
	}
	return map[string]any{
		"id": user.ID, "team_id": user.WorkspaceID, "name": user.Name, "real_name": user.RealName, "deleted": user.Deleted, "profile": profile,
	}
}

func profileResponse(user domain.User) map[string]any {
	return map[string]any{
		"display_name": user.Profile.DisplayName, "display_name_normalized": user.Profile.DisplayName, "email": user.Email,
		"real_name": user.RealName, "real_name_normalized": user.RealName,
		"status_text": user.Profile.StatusText, "status_emoji": user.Profile.StatusEmoji, "status_expiration": unixSeconds(user.Profile.StatusExpiration),
		"image_24": user.Profile.Image24, "image_32": user.Profile.Image32, "image_48": user.Profile.Image48, "image_72": user.Profile.Image72,
		"image_192": user.Profile.Image192, "image_512": user.Profile.Image512, "image_1024": user.Profile.Image1024,
		"team": user.WorkspaceID, "user_id": user.ID,
	}
}

func conversationResponse(conversation domain.Conversation) map[string]any {
	return map[string]any{"id": conversation.ID, "name": conversation.Name, "topic": map[string]any{"value": conversation.Topic}, "purpose": map[string]any{"value": conversation.Purpose}, "is_archived": conversation.Archived, "is_private": conversation.IsPrivate, "is_channel": !conversation.IsPrivate && !conversation.IsDirect && !conversation.IsGroupDirect, "is_im": conversation.IsDirect, "is_mpim": conversation.IsGroupDirect, "is_member": true, "team_id": conversation.WorkspaceID}
}

func (h Handler) conversationsList(w http.ResponseWriter, r *http.Request) {
	h.listConversations(w, r, false)
}

func (h Handler) usersConversations(w http.ResponseWriter, r *http.Request) {
	h.listConversations(w, r, true)
}

func (h Handler) listConversations(w http.ResponseWriter, r *http.Request, allowMember bool) {
	principal, err := h.authenticate(r, auth.ScopeChannelsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := decodeConversationListFields(fields)
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	if allowMember {
		request.MemberUserID = domain.UserID(strings.TrimSpace(fields["user"]))
	}
	page, err := h.Messages.Conversations(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	channels := make([]map[string]any, 0, len(page.Conversations))
	for _, conversation := range page.Conversations {
		channels = append(channels, conversationResponse(conversation))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": channels, "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore})
}

func (h Handler) conversationMembers(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_cursor")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.ConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, channel, request)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	members := make([]string, 0, len(page.Users))
	for _, user := range page.Users {
		members = append(members, string(user.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "members": members, "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore})
}

func (h Handler) createConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["name"]) == "" {
		// /conversations.create enumerates invalid_name_required, not invalid_arguments.
		writeError(w, "invalid_name_required")
		return
	}
	private, err := parseBoolField(fields["is_private"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.CreateConversation(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], private)
	if err != nil {
		// The collision code belongs to the operation, not to the shared mapper:
		// a taken name reaches here as store.ErrAlreadyExists.
		writeError(w, mapServiceErrorExists(err, "name_taken", "name_taken"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) joinConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticateConversationJoin(r, auth.ScopeChannelsJoin, auth.ScopeChannelsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	conversationID := strings.TrimSpace(fields["channel"])
	if conversationID == "" {
		writeError(w, "channel_not_found")
		return
	}
	conversation, err := h.Messages.JoinConversation(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(conversationID))
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) authenticateConversationJoin(r *http.Request, botScope, userScope auth.Scope) (auth.Principal, error) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		return auth.Principal{}, err
	}
	// Slack grants this method differently by credential type: bot tokens use
	// channels:join, while user tokens use channels:write. Treating it like the
	// other conversation mutators and requiring channels:manage made an
	// official bot installation unable to join any public channel.
	needed := userScope
	if principal.TokenType == "bot" || principal.BotID != "" {
		needed = botScope
	}
	if !principal.HasScope(needed) {
		return auth.Principal{}, missingScopeError{needed: needed, provided: permissionScopes(principal)}
	}
	return principal, nil
}

func (h Handler) inviteConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	allInviteScopes := []auth.Scope{
		auth.ScopeChannelsManage, auth.ScopeChannelsWrite, auth.ScopeChannelsWriteInvites,
		auth.ScopeGroupsWrite, auth.ScopeGroupsWriteInvites, auth.ScopeIMWrite, auth.ScopeMPIMWrite,
	}
	if !principalHasAnyScope(principal, allInviteScopes...) {
		writeAuthError(w, missingScopeError{needed: auth.ScopeChannelsManage, provided: permissionScopes(principal)})
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "channel_not_found")
		return
	}
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	required := []auth.Scope{auth.ScopeChannelsWrite}
	switch {
	case conversation.IsDirect:
		required = []auth.Scope{auth.ScopeIMWrite}
	case conversation.IsGroupDirect:
		required = []auth.Scope{auth.ScopeMPIMWrite}
	case conversation.IsPrivate:
		required = []auth.Scope{auth.ScopeGroupsWrite, auth.ScopeGroupsWriteInvites}
	case principal.TokenType == "bot" || principal.BotID != "":
		required = []auth.Scope{auth.ScopeChannelsManage, auth.ScopeChannelsWriteInvites}
	default:
		required = []auth.Scope{auth.ScopeChannelsWrite, auth.ScopeChannelsWriteInvites}
	}
	if !principalHasAnyScope(principal, required...) {
		writeAuthError(w, missingScopeError{needed: required[0], provided: permissionScopes(principal)})
		return
	}
	if conversation.Archived {
		writeError(w, "is_archived")
		return
	}
	if conversation.IsDirect || conversation.IsGroupDirect {
		writeError(w, "method_not_supported_for_channel_type")
		return
	}
	if strings.TrimSpace(fields["users"]) == "" {
		// /conversations.invite enumerates no_user for a missing users argument.
		writeError(w, "no_user")
		return
	}
	rawUsers := strings.Split(fields["users"], ",")
	if len(rawUsers) > 100 {
		writeError(w, "too_many_users")
		return
	}
	users := make([]domain.UserID, 0)
	seen := make(map[domain.UserID]struct{})
	for _, raw := range rawUsers {
		user := domain.UserID(strings.TrimSpace(raw))
		if user == "" {
			writeError(w, "invalid_array_arg")
			return
		}
		if _, exists := seen[user]; exists {
			continue
		}
		seen[user] = struct{}{}
		users = append(users, user)
	}
	force, err := parseBoolField(fields["force"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	valid, failures, err := h.conversationInviteCandidates(r, principal, channel, users)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	if len(failures) != 0 && (!force || len(valid) == 0) {
		items := make([]map[string]any, 0, len(failures))
		for _, failure := range failures {
			items = append(items, map[string]any{
				"user": failure.UserID, "ok": false, "error": failure.Reason,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "error": failures[0].Reason, "errors": items,
		})
		return
	}
	conversation, err = h.Messages.InviteConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, channel, valid)
	if err != nil {
		writeError(w, mapServiceErrorExists(err, "channel_not_found", "already_in_channel"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

type conversationInviteFailure struct {
	UserID domain.UserID
	Reason string
}

func (h Handler) conversationInviteCandidates(r *http.Request, principal auth.Principal, channel domain.ConversationID, users []domain.UserID) ([]domain.UserID, []conversationInviteFailure, error) {
	members := make(map[domain.UserID]struct{})
	request := domain.PageRequest{Limit: 200}
	for {
		page, err := h.Messages.ConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, channel, request)
		if err != nil {
			return nil, nil, err
		}
		for _, user := range page.Users {
			members[user.ID] = struct{}{}
		}
		if !page.HasMore {
			break
		}
		request.Cursor = page.NextCursor
	}

	valid := make([]domain.UserID, 0, len(users))
	failures := make([]conversationInviteFailure, 0)
	for _, targetID := range users {
		reason := ""
		switch {
		case targetID == principal.UserID:
			reason = "cant_invite_self"
		default:
			if _, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, targetID); err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					return nil, nil, err
				}
				reason = "user_not_found"
			} else if _, exists := members[targetID]; exists {
				reason = "already_in_channel"
			}
		}
		if reason != "" {
			failures = append(failures, conversationInviteFailure{UserID: targetID, Reason: reason})
			continue
		}
		valid = append(valid, targetID)
	}
	return valid, failures, nil
}

func principalHasAnyScope(principal auth.Principal, scopes ...auth.Scope) bool {
	for _, scope := range scopes {
		if principal.HasScope(scope) {
			return true
		}
	}
	return false
}

func (h Handler) leaveConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["channel"]) == "" {
		writeError(w, "channel_not_found")
		return
	}
	conversation := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	info, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, conversation)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	if info.IsDirect || info.IsGroupDirect {
		writeError(w, "method_not_supported_for_channel_type")
		return
	}
	if err := h.Messages.LeaveConversation(r.Context(), principal.WorkspaceID, principal.UserID, conversation); err != nil {
		if errors.Is(err, service.ErrCannotLeaveDefault) {
			writeError(w, "cant_leave_general")
			return
		}
		if errors.Is(err, service.ErrInvalidConversation) {
			writeError(w, "is_archived")
			return
		}
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversation})
}

func (h Handler) kickConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	target := domain.UserID(strings.TrimSpace(fields["user"]))
	if channel == "" {
		writeError(w, "channel_not_found")
		return
	}
	if target == "" {
		writeError(w, "user_not_found")
		return
	}
	if err := h.Messages.KickConversationMember(r.Context(), principal.WorkspaceID, principal.UserID, channel, target); err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": channel})
}

func (h Handler) renameConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	name := fields["name"]
	if channel == "" {
		writeError(w, "channel_not_found")
		return
	}
	if strings.TrimSpace(name) == "" {
		writeError(w, "invalid_name_required")
		return
	}
	info, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	if info.IsDirect || info.IsGroupDirect {
		writeError(w, "method_not_supported_for_channel_type")
		return
	}
	conversation, err := h.Messages.RenameConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel, name)
	if err != nil {
		writeError(w, mapServiceErrorExists(err, "channel_not_found", "name_taken"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) setConversationTopic(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "channel_not_found")
		return
	}
	if _, present := fields["topic"]; !present {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.SetConversationTopic(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["topic"])
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) setConversationPurpose(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "channel_not_found")
		return
	}
	if _, present := fields["purpose"]; !present {
		writeError(w, "invalid_arg_name")
		return
	}
	conversation, err := h.Messages.SetConversationPurpose(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["purpose"])
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) archiveConversation(w http.ResponseWriter, r *http.Request) {
	ok, err := h.changeConversationArchived(w, r, true)
	if !ok {
		return
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case errors.Is(err, service.ErrConversationAlreadyArchived):
		writeError(w, "already_archived")
	case errors.Is(err, service.ErrCannotArchiveDefault):
		writeError(w, "cant_archive_general")
	case errors.Is(err, service.ErrInvalidConversation):
		writeError(w, "method_not_supported_for_channel_type")
	default:
		writeError(w, mapServiceError(err, "channel_not_found"))
	}
}

func (h Handler) unarchiveConversation(w http.ResponseWriter, r *http.Request) {
	ok, err := h.changeConversationArchived(w, r, false)
	if !ok {
		return
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case errors.Is(err, service.ErrConversationNotArchived):
		writeError(w, "not_archived")
	case errors.Is(err, service.ErrInvalidConversation):
		writeError(w, "method_not_supported_for_channel_type")
	default:
		writeError(w, mapServiceError(err, "channel_not_found"))
	}
}

func (h Handler) closeConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "channel_not_found")
		return
	}
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	if !conversation.IsDirect && !conversation.IsGroupDirect {
		writeError(w, "method_not_supported_for_channel_type")
		return
	}
	if err := h.Messages.LeaveConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "no_op": true, "already_closed": true})
			return
		}
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) changeConversationArchived(w http.ResponseWriter, r *http.Request, archived bool) (bool, error) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return false, nil
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return false, nil
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "channel_not_found")
		return false, nil
	}
	_, err = h.Messages.SetConversationArchived(r.Context(), principal.WorkspaceID, principal.UserID, channel, archived)
	return true, err
}

func (h Handler) openConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["users"]) == "" {
		// /conversations.open enumerates users_list_not_supplied.
		writeError(w, "users_list_not_supplied")
		return
	}
	users := make([]domain.UserID, 0)
	seen := make(map[domain.UserID]struct{})
	for _, raw := range strings.Split(fields["users"], ",") {
		user := domain.UserID(strings.TrimSpace(raw))
		if user == "" {
			writeError(w, "invalid_array_arg")
			return
		}
		if _, exists := seen[user]; exists {
			continue
		}
		seen[user] = struct{}{}
		users = append(users, user)
	}
	conversation, err := h.Messages.OpenConversation(r.Context(), principal.WorkspaceID, principal.UserID, users)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) markConversation(w http.ResponseWriter, r *http.Request) {
	// conversations.mark moves the caller's read cursor, so the pinned token
	// parameter requires `conversations:write`, not a history read scope.
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, timestamp := strings.TrimSpace(fields["channel"]), strings.TrimSpace(fields["ts"])
	if channel == "" {
		writeError(w, "channel_not_found")
		return
	}
	if timestamp == "" {
		// /conversations.mark enumerates invalid_timestamp.
		writeError(w, "invalid_timestamp")
		return
	}
	cursor, err := h.Messages.MarkRead(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(channel), domain.MessageTimestamp(timestamp))
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": cursor.Conversation, "ts": cursor.LastRead})
}

func (h Handler) addReaction(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeReactionsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, timestamp, name, err := normalizeReactionFields(fields)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := h.Messages.AddReaction(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp, name); err != nil {
		// /reactions.add is the one pinned enum that declares already_reacted.
		writeError(w, mapServiceErrorExists(err, "message_not_found", "already_reacted"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) removeReaction(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeReactionsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, timestamp, name, err := normalizeReactionFields(fields)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := h.Messages.RemoveReaction(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp, name); err != nil {
		writeError(w, mapServiceError(err, "message_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) getReactions(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeReactionsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	limit, err := clampLimit(fields["limit"], 200, 200)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	cursor, err := decodeCursor(fields["cursor"], "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	reactions, next, hasMore, err := h.Messages.Reactions(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp, domain.PageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, mapServiceError(err, "message_not_found"))
		return
	}
	grouped := make(map[string]map[string]any)
	order := make([]string, 0)
	for _, reaction := range reactions {
		entry, exists := grouped[reaction.Name]
		if !exists {
			entry = map[string]any{"name": reaction.Name, "count": 0, "users": []domain.UserID{}}
			grouped[reaction.Name] = entry
			order = append(order, reaction.Name)
		}
		entry["count"] = entry["count"].(int) + 1
		entry["users"] = append(entry["users"].([]domain.UserID), reaction.UserID)
	}
	result := make([]map[string]any, 0, len(order))
	for _, name := range order {
		result = append(result, grouped[name])
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": map[string]any{"reactions": result}, "has_more": hasMore, "response_metadata": map[string]string{"next_cursor": string(next)}})
}

func (h Handler) listUserReactions(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeReactionsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	requested := strings.TrimSpace(fields["user"])
	if requested != "" && requested != string(principal.UserID) {
		// /reactions.list declares no_permission; `not_authorized` belongs to
		// /conversations.rename and is not in this enum.
		writeError(w, "no_permission")
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.UserReactions(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		// /reactions.list declares user_not_found, not team_not_found.
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		message := messageResponse(item.Message)
		message["reactions"] = []map[string]any{{"name": item.Reaction.Name, "count": 1, "users": []string{string(item.Reaction.UserID)}}}
		items = append(items, map[string]any{"type": "message", "channel": item.Conversation, "message": message})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore})
}

func (h Handler) addBookmark(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeBookmarksWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	bookmark, err := h.Messages.AddBookmark(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["title"], fields["type"], fields["link"], fields["emoji"], fields["entity_id"], fields["access_level"], fields["parent_id"])
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bookmark": bookmarkResponse(bookmark)})
}

func (h Handler) editBookmark(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeBookmarksWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	id := domain.BookmarkID(strings.TrimSpace(fields["bookmark_id"]))
	if channel == "" || id == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	_, titleSet := fields["title"]
	_, linkSet := fields["link"]
	_, emojiSet := fields["emoji"]
	bookmark, err := h.Messages.EditBookmark(r.Context(), principal.WorkspaceID, principal.UserID, channel, id, domain.BookmarkUpdate{Title: fields["title"], Link: fields["link"], Emoji: fields["emoji"], SetTitle: titleSet, SetLink: linkSet, SetEmoji: emojiSet})
	if err != nil {
		writeError(w, mapServiceError(err, "bookmark_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bookmark": bookmarkResponse(bookmark)})
}

func (h Handler) listBookmarks(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeBookmarksRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	bookmarks, err := h.Messages.Bookmarks(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	items := make([]map[string]any, 0, len(bookmarks))
	for _, bookmark := range bookmarks {
		items = append(items, bookmarkResponse(bookmark))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bookmarks": items})
}

func (h Handler) removeBookmark(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeBookmarksWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	id := domain.BookmarkID(strings.TrimSpace(fields["bookmark_id"]))
	if channel == "" || id == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.RemoveBookmark(r.Context(), principal.WorkspaceID, principal.UserID, channel, id); err != nil {
		writeError(w, mapServiceError(err, "bookmark_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func bookmarkResponse(bookmark domain.Bookmark) map[string]any {
	return map[string]any{"id": bookmark.ID, "channel_id": bookmark.Conversation, "title": bookmark.Title, "type": bookmark.Type, "link": bookmark.Link, "emoji": bookmark.Emoji, "entity_id": bookmark.EntityID, "access_level": bookmark.AccessLevel, "parent_id": bookmark.ParentID, "date_created": bookmark.CreatedAt.Unix(), "date_updated": bookmark.UpdatedAt.Unix(), "last_updated_by_user_id": bookmark.UpdatedBy}
}

func (h Handler) addPin(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopePinsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := h.Messages.AddPin(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		// /pins.add declares already_pinned and does not declare already_reacted.
		writeError(w, mapServiceErrorExists(err, "message_not_found", "already_pinned"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) removePin(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopePinsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := h.Messages.RemovePin(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		writeError(w, mapServiceError(err, "message_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) listPins(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopePinsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeError(w, "channel_not_found")
		return
	}
	limit, err := clampLimit(fields["limit"], 100, 200)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	cursor, err := decodeCursor(fields["cursor"], "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	pins, next, hasMore, err := h.Messages.Pins(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.PageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	items := make([]map[string]any, 0, len(pins))
	for _, pin := range pins {
		items = append(items, map[string]any{"type": "message", "channel": channel, "message": map[string]any{"id": pin.Message}, "created": pin.CreatedAt.Unix(), "created_by": pin.UserID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "has_more": hasMore, "response_metadata": map[string]string{"next_cursor": string(next)}})
}

func (h Handler) addStar(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeStarsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// This system stars messages only. stars.add/remove enumerate file_not_found
	// and file_comment_not_found for the file forms, so a request naming a file is
	// rejected with the code for the thing that cannot be found rather than with a
	// generic argument error.
	if strings.TrimSpace(fields["file"]) != "" {
		writeError(w, "file_not_found")
		return
	}
	if strings.TrimSpace(fields["file_comment"]) != "" {
		writeError(w, "file_comment_not_found")
		return
	}
	if err := h.Messages.AddStar(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		// /stars.add declares already_starred.
		writeError(w, mapServiceErrorExists(err, "message_not_found", "already_starred"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) removeStar(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeStarsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// This system stars messages only. stars.add/remove enumerate file_not_found
	// and file_comment_not_found for the file forms, so a request naming a file is
	// rejected with the code for the thing that cannot be found rather than with a
	// generic argument error.
	if strings.TrimSpace(fields["file"]) != "" {
		writeError(w, "file_not_found")
		return
	}
	if strings.TrimSpace(fields["file_comment"]) != "" {
		writeError(w, "file_comment_not_found")
		return
	}
	if err := h.Messages.RemoveStar(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		writeError(w, mapServiceError(err, "message_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) listStars(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeStarsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	limit, err := clampLimit(fields["limit"], 100, 1000)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	cursor, err := decodeCursor(fields["cursor"], "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	items, next, more, err := h.Messages.Stars(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{"type": "message", "channel": item.Conversation, "date_create": item.CreatedAt.Unix(), "message": messageResponse(item.Message)})
	}
	// The cursor the store returns is the only way to reach page two. It used to be
	// discarded and replaced by an invented `spill` key, so a workspace with more
	// stars than one page could never be read in full.
	paging := map[string]any{"page": 1, "total": len(result), "per_page": limit}
	body := map[string]any{"ok": true, "items": result, "paging": paging}
	if more {
		body["response_metadata"] = map[string]string{"next_cursor": string(next)}
	}
	writeJSON(w, http.StatusOK, body)
}

func reminderResponse(reminder domain.Reminder) map[string]any {
	response := map[string]any{"id": reminder.ID, "creator": reminder.Creator, "user": reminder.User, "text": reminder.Text, "time": reminder.Time.Unix(), "recurring": reminder.Recurring}
	if !reminder.CompleteAt.IsZero() {
		response["complete_ts"] = reminder.CompleteAt.Unix()
	} else if !reminder.Recurring {
		// Slack's current reminder object includes complete_ts=0 for an active
		// non-recurring reminder. Omitting the field happened to decode in the
		// SDKs, but it was not the documented wire object those SDKs model.
		response["complete_ts"] = int64(0)
	}
	return response
}

func (h Handler) createCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	canvas, err := h.Messages.CreateCanvas(r.Context(), principal.WorkspaceID, principal.UserID, fields["title"], fields["document_content"], domain.ConversationID(strings.TrimSpace(fields["channel_id"])))
	if err != nil {
		writeError(w, mapServiceError(err, "canvas_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canvas_id": canvas.ID})
}

func (h Handler) createConversationCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channelID := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channelID == "" {
		writeError(w, "invalid_arguments")
		return
	}
	canvas, err := h.Messages.CreateConversationCanvas(r.Context(), principal.WorkspaceID, principal.UserID, channelID, fields["title"], fields["document_content"])
	if err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, "channel_canvas_already_exists")
			return
		}
		writeError(w, mapServiceError(err, "channel_canvas_creation_failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canvas_id": canvas.ID})
}

func (h Handler) editCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" || strings.TrimSpace(fields["changes"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.EditCanvas(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"])), fields["changes"]); err != nil {
		writeError(w, mapServiceError(err, "canvas_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) deleteCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.DeleteCanvas(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"]))); err != nil {
		writeError(w, mapServiceError(err, "canvas_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) setCanvasAccess(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" || strings.TrimSpace(fields["access_level"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.SetCanvasAccess(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"])), strings.TrimSpace(fields["access_level"]), parseIDList[domain.ConversationID](fields["channel_ids"]), parseIDList[domain.UserID](fields["user_ids"])); err != nil {
		writeError(w, mapServiceError(err, "canvas_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) deleteCanvasAccess(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.DeleteCanvasAccess(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"])), parseIDList[domain.ConversationID](fields["channel_ids"]), parseIDList[domain.UserID](fields["user_ids"])); err != nil {
		writeError(w, mapServiceError(err, "canvas_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) lookupCanvasSections(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" || strings.TrimSpace(fields["criteria"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	sections, err := h.Messages.LookupCanvasSections(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"])), fields["criteria"])
	if err != nil {
		writeError(w, mapServiceError(err, "canvas_not_found"))
		return
	}
	result := make([]map[string]string, 0, len(sections))
	for _, section := range sections {
		result = append(result, map[string]string{"id": section.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sections": result})
}

func (h Handler) addReminder(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemindersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	textValue := strings.TrimSpace(fields["text"])
	if textValue == "" {
		// /reminders.add enumerates cannot_parse for a time or text it cannot read.
		writeError(w, "cannot_parse")
		return
	}
	when, err := reminderTime(fields["time"], time.Now().UTC())
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	targetID := domain.UserID(strings.TrimSpace(fields["user"]))
	// Slack marks the user argument no longer supported for user tokens. Its
	// current page separately notes a bot-token exception, so preserve that
	// explicit token distinction instead of accepting the obsolete path for
	// every credential.
	if targetID != "" && targetID != principal.UserID && principal.TokenType != "bot" {
		writeError(w, "cannot_add_others")
		return
	}
	reminder, err := h.Messages.AddReminder(r.Context(), principal.WorkspaceID, principal.UserID, targetID, textValue, when)
	if err != nil {
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reminder": reminderResponse(reminder)})
}

func reminderIDFields(w http.ResponseWriter, r *http.Request) (map[string]string, domain.ReminderID, bool) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return nil, "", false
	}
	id := domain.ReminderID(strings.TrimSpace(fields["reminder"]))
	if id == "" {
		// reminders.complete/delete/info all enumerate not_found.
		writeError(w, "not_found")
		return nil, "", false
	}
	return fields, id, true
}

func (h Handler) completeReminder(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemindersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	_, id, ok := reminderIDFields(w, r)
	if !ok {
		return
	}
	if err := h.Messages.CompleteReminder(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		writeError(w, mapServiceError(err, "not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) deleteReminder(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemindersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	_, id, ok := reminderIDFields(w, r)
	if !ok {
		return
	}
	if err := h.Messages.DeleteReminder(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		writeError(w, mapServiceError(err, "not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) reminderInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemindersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	_, id, ok := reminderIDFields(w, r)
	if !ok {
		return
	}
	reminder, err := h.Messages.ReminderInfo(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		writeError(w, mapServiceError(err, "not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reminder": reminderResponse(reminder)})
}

func (h Handler) listReminders(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemindersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.Reminders(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	result := make([]map[string]any, 0, len(page.Reminders))
	for _, reminder := range page.Reminders {
		result = append(result, reminderResponse(reminder))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reminders": result, "has_more": page.HasMore, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}

func (h Handler) searchMessages(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeSearchRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if principal.TokenType == "bot" || principal.BotID != "" {
		writeError(w, "not_allowed_token_type")
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	arguments, err := decodeSearchArguments(fields, true)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.searchMessagePage(r.Context(), principal, arguments)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	response, err := h.searchMessageEnvelope(r.Context(), principal, arguments, page)
	if err != nil {
		writeError(w, "fatal_error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "query": arguments.query, "messages": response,
		"has_more": page.HasMore, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)},
	})
}

func (h Handler) searchFiles(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeSearchRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if principal.TokenType == "bot" || principal.BotID != "" {
		writeError(w, "not_allowed_token_type")
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	arguments, err := decodeSearchArguments(fields, false)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.SearchFiles(r.Context(), principal.WorkspaceID, principal.UserID, domain.FileSearchRequest{
		Query: arguments.query, Count: arguments.count, Page: arguments.page,
		Sort: arguments.sort, Direction: arguments.direction,
	})
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "query": arguments.query, "files": searchFileEnvelope(arguments, page)})
}

func (h Handler) searchAll(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeSearchRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if principal.TokenType == "bot" || principal.BotID != "" {
		writeError(w, "not_allowed_token_type")
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	arguments, err := decodeSearchArguments(fields, false)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	messagePage, err := h.searchMessagePage(r.Context(), principal, arguments)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	messageEnvelope, err := h.searchMessageEnvelope(r.Context(), principal, arguments, messagePage)
	if err != nil {
		writeError(w, "fatal_error")
		return
	}
	filePage, err := h.Messages.SearchFiles(r.Context(), principal.WorkspaceID, principal.UserID, domain.FileSearchRequest{
		Query: arguments.query, Count: arguments.count, Page: arguments.page,
		Sort: arguments.sort, Direction: arguments.direction,
	})
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "query": arguments.query,
		"messages": messageEnvelope, "files": searchFileEnvelope(arguments, filePage),
	})
}

type searchArguments struct {
	query       string
	count, page int
	cursor      domain.Cursor
	sort        domain.SearchSort
	direction   domain.SearchDirection
	highlight   bool
}

func decodeSearchArguments(fields map[string]string, allowCursor bool) (searchArguments, error) {
	query := strings.TrimSpace(fields["query"])
	if query == "" {
		return searchArguments{}, decodeFailure("no_query", "query is required")
	}
	count, err := clampLimit(fields["count"], 20, 100)
	if err != nil {
		return searchArguments{}, err
	}
	page, err := pageNumber(fields["page"])
	if err != nil || page > 100 {
		return searchArguments{}, decodeFailure("invalid_arg_name", "page must be between 1 and 100")
	}
	sortOrder, direction, err := domain.NormalizeSearchOrder(fields["sort"], fields["sort_dir"])
	if err != nil {
		return searchArguments{}, decodeFailure("invalid_arg_name", err.Error())
	}
	highlight, err := parseBoolField(fields["highlight"])
	if err != nil {
		return searchArguments{}, decodeFailure("invalid_arg_name", "highlight must be a boolean")
	}
	var cursor domain.Cursor
	rawCursor := strings.TrimSpace(fields["cursor"])
	if rawCursor == "*" {
		rawCursor = ""
	}
	if rawCursor != "" && !allowCursor {
		return searchArguments{}, decodeFailure("invalid_arg_name", "cursor is not supported by this search method")
	}
	if allowCursor {
		cursor, err = decodeMessageCursor(rawCursor, "invalid_arg_name")
		if err != nil {
			return searchArguments{}, err
		}
	}
	return searchArguments{query: query, count: count, page: page, cursor: cursor, sort: sortOrder, direction: direction, highlight: highlight}, nil
}

func (h Handler) searchMessagePage(ctx context.Context, principal auth.Principal, arguments searchArguments) (domain.MessagePage, error) {
	cursor := arguments.cursor
	var page domain.MessagePage
	var err error
	for current := 1; current <= arguments.page; current++ {
		page, err = h.Messages.SearchMessages(ctx, principal.WorkspaceID, principal.UserID, domain.MessageSearchRequest{
			Query: arguments.query, Sort: arguments.sort, Direction: arguments.direction,
			Page: domain.PageRequest{Limit: arguments.count, Cursor: cursor},
		})
		if err != nil || current == arguments.page {
			break
		}
		if !page.HasMore {
			page.Messages = nil
			page.NextCursor = ""
			break
		}
		cursor = page.NextCursor
	}
	return page, err
}

func (h Handler) searchMessageEnvelope(ctx context.Context, principal auth.Principal, arguments searchArguments, page domain.MessagePage) (map[string]any, error) {
	matches := make([]map[string]any, 0, len(page.Messages))
	for _, message := range page.Messages {
		match := messageResponse(message)
		conversation, infoErr := h.Messages.ConversationInfo(ctx, principal.WorkspaceID, principal.UserID, message.Conversation)
		author, userErr := h.Messages.UserInfo(ctx, principal.WorkspaceID, principal.UserID, message.AuthorID)
		permalink, linkErr := h.Messages.Permalink(ctx, principal.WorkspaceID, principal.UserID, message.Conversation, domain.NewMessageTimestamp(message.CreatedAt))
		if infoErr != nil || userErr != nil || linkErr != nil {
			return nil, errors.New("search result hydration failed")
		}
		match["channel"] = conversationResponse(conversation)
		match["team"] = principal.WorkspaceID
		match["username"] = author.Name
		match["permalink"] = permalink
		matches = append(matches, match)
	}
	pageCount := 0
	if page.Total > 0 {
		pageCount = (page.Total + arguments.count - 1) / arguments.count
	}
	first, last := 0, 0
	if len(matches) > 0 {
		first = (arguments.page-1)*arguments.count + 1
		last = first + len(matches) - 1
	}
	pagination := map[string]any{"first": first, "last": last, "page": arguments.page, "per_page": arguments.count, "page_count": pageCount, "total_count": page.Total}
	paging := map[string]any{"count": arguments.count, "page": arguments.page, "pages": pageCount, "total": page.Total}
	return map[string]any{"matches": matches, "total": page.Total, "pagination": pagination, "paging": paging}, nil
}

func searchFileEnvelope(arguments searchArguments, page domain.FilePage) map[string]any {
	matches := make([]map[string]any, 0, len(page.Files))
	for _, file := range page.Files {
		matches = append(matches, fileResponse(file))
	}
	pageCount := 0
	if page.Total > 0 {
		pageCount = (page.Total + arguments.count - 1) / arguments.count
	}
	first, last := 0, 0
	if len(matches) > 0 {
		first = (arguments.page-1)*arguments.count + 1
		last = first + len(matches) - 1
	}
	return map[string]any{
		"matches": matches, "total": page.Total,
		"pagination": map[string]any{"first": first, "last": last, "page": arguments.page, "per_page": arguments.count, "page_count": pageCount, "total_count": page.Total},
		"paging":     map[string]any{"count": arguments.count, "page": arguments.page, "pages": pageCount, "total": page.Total},
	}
}

func (h Handler) remoteFileAdd(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["external_id"]) == "" || strings.TrimSpace(fields["title"]) == "" || strings.TrimSpace(fields["external_url"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.AddRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID, domain.RemoteFile{ExternalID: fields["external_id"], Title: fields["title"], FileType: fields["filetype"], ExternalURL: fields["external_url"], PreviewImage: fields["preview_image"], IndexableContents: fields["indexable_file_contents"]})
	if err != nil {
		reason := mapServiceError(err, "remote_file_not_found")
		if errors.Is(err, store.ErrAlreadyExists) {
			reason = "remote_file_already_exists"
		}
		writeError(w, reason)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": remoteFileResponse(value)})
}

func (h Handler) remoteFileInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	lookup, lookupErr := remoteFileLookup(fields)
	if lookupErr != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.RemoteFileInfo(r.Context(), principal.WorkspaceID, principal.UserID, lookup)
	if err != nil {
		writeError(w, mapServiceError(err, "remote_file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": remoteFileResponse(value)})
}

func (h Handler) remoteFilesList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.RemoteFiles(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		writeError(w, mapServiceError(err, "remote_files_unavailable"))
		return
	}
	files := make([]map[string]any, 0, len(page.Files))
	for _, value := range page.Files {
		files = append(files, remoteFileResponse(value))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": files, "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore})
}

func (h Handler) remoteFileRemove(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	lookup, lookupErr := remoteFileLookup(fields)
	if lookupErr != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.RemoveRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID, lookup); err != nil {
		writeError(w, mapServiceError(err, "remote_file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) remoteFileShare(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesShare)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	lookup, lookupErr := remoteFileLookup(fields)
	channels := parseIDList[domain.ConversationID](fields["channels"])
	if lookupErr != nil || len(channels) == 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.ShareRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID, lookup, channels)
	if err != nil {
		writeError(w, mapServiceError(err, "remote_file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": remoteFileResponse(value)})
}

func (h Handler) remoteFileUpdate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	lookup, lookupErr := remoteFileLookup(fields)
	if lookupErr != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	update := domain.RemoteFileUpdate{Lookup: lookup}
	if value, ok := fields["title"]; ok {
		update.SetTitle, update.Title = true, value
	}
	if value, ok := fields["filetype"]; ok {
		update.SetFileType, update.FileType = true, value
	}
	if value, ok := fields["external_url"]; ok {
		update.SetExternalURL, update.ExternalURL = true, value
	}
	if value, ok := fields["preview_image"]; ok {
		update.SetPreviewImage, update.PreviewImage = true, value
	}
	if value, ok := fields["indexable_file_contents"]; ok {
		update.SetIndexableData, update.IndexableContents = true, value
	}
	value, err := h.Messages.UpdateRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID, update)
	if err != nil {
		writeError(w, mapServiceError(err, "remote_file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": remoteFileResponse(value)})
}

func remoteFileLookup(fields map[string]string) (domain.RemoteFileLookup, error) {
	lookup := domain.RemoteFileLookup{ID: domain.FileID(strings.TrimSpace(fields["file"])), ExternalID: strings.TrimSpace(fields["external_id"])}
	if !lookup.Valid() {
		return domain.RemoteFileLookup{}, errors.New("exactly one remote file identifier is required")
	}
	return lookup, nil
}

func remoteFileResponse(value domain.RemoteFile) map[string]any {
	channels := make([]string, 0, len(value.SharedChannels))
	for _, channel := range value.SharedChannels {
		channels = append(channels, string(channel))
	}
	return map[string]any{"id": value.ID, "external_id": value.ExternalID, "title": value.Title, "filetype": value.FileType, "external_url": value.ExternalURL, "preview_image": value.PreviewImage, "indexable_file_contents": value.IndexableContents, "created": value.CreatedAt.Unix(), "deleted": value.Deleted, "channels": channels}
}

func (h Handler) fileInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	if fileID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	file, err := h.Messages.FileInfo(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": fileResponse(file)})
}

func (h Handler) deleteFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	if fileID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.DeleteFile(r.Context(), principal.WorkspaceID, principal.UserID, fileID); err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) deleteFileComment(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	commentID := domain.FileCommentID(strings.TrimSpace(fields["id"]))
	if fileID == "" || commentID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.DeleteFileComment(r.Context(), principal.WorkspaceID, principal.UserID, fileID, commentID); err != nil {
		writeError(w, mapServiceError(err, "comment_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) shareFilePublic(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	if fileID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	file, err := h.Messages.ShareFilePublic(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": fileResponse(file), "permalink_public": "/files/public/" + file.PublicToken})
}

func (h Handler) revokeFilePublic(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	if fileID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	file, err := h.Messages.RevokeFilePublic(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": fileResponse(file)})
}

func (h Handler) filesList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// files.list declares user, channel, ts_from, ts_to, types, count, page and
	// show_files_hidden_by_limit. None of them was implemented: the only decode
	// was limit/cursor, so `?channel=C1` returned every file the principal could
	// see across every channel with `"ok":true`.
	//
	// Answering a scoped request with unscoped data is the defect, but refusing a
	// documented parameter is not the remedy — `count` is how the official SDK
	// paginates this method, and rejecting it broke that suite. Every declared
	// parameter is honoured here instead.
	filter, err := decodeFileFilter(fields)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// /files.list declares user_not_found. A filter naming a user this workspace
	// does not have is a wrong request, and answering it with an empty list told
	// the caller the user simply owns no files.
	if filter.user != "" {
		if _, infoErr := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserID(filter.user)); infoErr != nil {
			writeError(w, mapServiceError(infoErr, "user_not_found"))
			return
		}
	}
	window, err := h.scanFiles(r.Context(), principal, filter)
	if err != nil {
		var bound decodeError
		if errors.As(err, &bound) {
			writeError(w, bound.code)
			return
		}
		// /files.list declares user_not_found and no file-not-found code: the only
		// referent this read can fail to find is the principal's own workspace
		// membership.
		writeError(w, mapServiceError(err, "user_not_found"))
		return
	}
	files := make([]map[string]any, 0, len(window.files))
	for _, file := range window.files {
		files = append(files, fileResponse(file))
	}
	pages := (window.total + filter.count - 1) / filter.count
	if pages == 0 {
		pages = 1
	}
	// The pinned 200 schema is additionalProperties:false over exactly
	// {ok, files, paging}. `has_more` and `response_metadata` used to be emitted
	// here; neither is declared, and `has_more:true` next to a hard-coded empty
	// `next_cursor` described a page no caller could ever reach. `paging.count`
	// is the requested page size, as the operation's own pinned example shows
	// (`count: 100` beside two returned files).
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": files, "paging": map[string]any{"count": filter.count, "page": filter.page, "pages": pages, "total": window.total}})
}

// fileFilterWindow is the complete answer to one filtered files.list: the
// requested page and the true size of the filtered collection.
type fileFilterWindow struct {
	files []domain.File
	total int
}

// errFileScanIncomplete reports that the scan behind a filtered files.list did
// not reach the end of the collection, so no complete answer exists to send.
// /files.list declares request_timeout and no other code that can describe an
// unfinished read.
var errFileScanIncomplete = decodeFailure("request_timeout", "files.list could not read the whole collection in time")

// scanFiles reads the workspace's files in pages and applies the filter above the
// repository, which has no filtered read of its own.
//
// It keeps only the requested page plus a running count, so the memory cost is
// proportional to `count` rather than to the workspace. That is what makes the
// answer complete: `paging.total` and `paging.pages` describe the whole filtered
// collection instead of whichever prefix happened to be scanned, and a file that
// matches only past the first window is still returned.
//
// The scan used to stop after a fixed 20,000 stored rows and answer
// request_timeout. Because `paging.total` describes the whole collection the scan
// always ran from row one, so at 20,001 files *every* call failed — a bare
// files.list, a `count=1` first page, and a filter that legitimately matched
// nothing alike — for every caller in the workspace, permanently. Any principal
// holding files:write could put the workspace there by uploading 20,001 one-byte
// files, and ordinary use reaches it on its own. A ceiling a user can reach is not
// a budget; it replaced a wrong answer with no answer.
//
// So the collection is now read to its end, and what is bounded is the resources
// one call may spend rather than the size of the workspace it may describe:
//
//   - fileScanBudget bounds wall-clock time, on top of whatever deadline the
//     caller's own context carries. Reaching it means the read genuinely did not
//     finish, which is what request_timeout says, and it degrades with load
//     instead of failing at a magic row count;
//   - a repository that reports another page while handing back a cursor it has
//     already given is stopped rather than followed forever;
//   - only `count` files are retained, whatever the size of the collection.
//
// This is the most a transport can do above a repository that cannot narrow the
// read. The repair that removes the traversal is a filtered, counted read in the
// store — ListFiles with user/channel/created-range/type predicates plus a
// COUNT — recorded as the follow-up this method is waiting on.
func (h Handler) scanFiles(ctx context.Context, principal auth.Principal, filter fileFilter) (fileFilterWindow, error) {
	first := (filter.page - 1) * filter.count
	last := first + filter.count
	window := fileFilterWindow{files: make([]domain.File, 0, filter.count)}
	scan := domain.PageRequest{Limit: fileFilterScanPage}
	ctx, cancel := context.WithTimeout(ctx, fileScanBudget)
	defer cancel()
	seen := make(map[domain.Cursor]struct{})
	for {
		if ctx.Err() != nil {
			return fileFilterWindow{}, errFileScanIncomplete
		}
		page, err := h.Messages.Files(ctx, principal.WorkspaceID, principal.UserID, scan)
		if err != nil {
			if ctx.Err() != nil {
				return fileFilterWindow{}, errFileScanIncomplete
			}
			return fileFilterWindow{}, err
		}
		for _, file := range page.Files {
			if !filter.matches(file) {
				continue
			}
			if window.total >= first && window.total < last {
				window.files = append(window.files, file)
			}
			window.total++
		}
		if !page.HasMore {
			return window, nil
		}
		if _, repeated := seen[page.NextCursor]; page.NextCursor == "" || repeated {
			// The repository claims another page and points at a place it has
			// already served. Following it is an infinite loop, and calling it a
			// complete read would report the collection as smaller than it is.
			return fileFilterWindow{}, errFileScanIncomplete
		}
		seen[page.NextCursor] = struct{}{}
		scan.Cursor = page.NextCursor
	}
}

// fileFilterScanPage is how much of the collection one repository read returns,
// and fileScanBudget is how long the whole traversal behind one files.list may
// take. Neither is a ceiling on what the answer may describe: the budget bounds
// the work a single request can cost this process, which nothing else does —
// cmd/server deliberately sets no WriteTimeout, and Go does not cancel a request
// context on one, so a caller that has hung up cannot be relied on to end the
// read.
const (
	fileFilterScanPage = 200
	fileScanBudget     = 15 * time.Second
)

// fileFilter carries every parameter /files.list declares.
type fileFilter struct {
	user    string
	channel string
	// tsFrom and tsTo are whole microseconds since the epoch. The pinned
	// parameters are `type: number` and both bounds are documented inclusive, so
	// truncating the fraction dropped a file at 100.5 from `ts_to=100.9`.
	tsFrom  int64
	hasFrom bool
	tsTo    int64
	hasTo   bool
	types   []string
	count   int
	page    int
}

func decodeFileFilter(fields map[string]string) (fileFilter, error) {
	filter := fileFilter{user: strings.TrimSpace(fields["user"]), channel: strings.TrimSpace(fields["channel"])}
	var err error
	if filter.count, err = clampLimit(fields["count"], 100, 1000); err != nil {
		return fileFilter{}, err
	}
	if filter.page, err = pageNumber(fields["page"]); err != nil {
		return fileFilter{}, err
	}
	if filter.tsFrom, filter.hasFrom, err = optionalEpoch(fields["ts_from"]); err != nil {
		return fileFilter{}, err
	}
	if filter.tsTo, filter.hasTo, err = optionalEpoch(fields["ts_to"]); err != nil {
		return fileFilter{}, err
	}
	// show_files_hidden_by_limit selects truncated placeholders for files this
	// deployment never hides, so the answer is the same either way — but it is a
	// declared boolean, and accepting `show_files_hidden_by_limit=perhaps`
	// silently would hide a caller's mistake.
	if _, err := parseBoolFields(fields, "show_files_hidden_by_limit"); err != nil {
		return fileFilter{}, decodeFailure("invalid_arg_name", "show_files_hidden_by_limit must be a boolean")
	}
	// unknown_type appears in exactly one enum in the whole pinned snapshot, and
	// it is this operation's. Matching nothing for an unrecognised name answered
	// ok:true with an empty list, which is the answer for "no such file" and not
	// for "no such type" — and `types=images,bogus` hid the mistake entirely.
	for _, value := range strings.Split(fields["types"], ",") {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if !slackFileTypeIsDeclared(value) {
			return fileFilter{}, decodeFailure("unknown_type", value+" is not a file type")
		}
		filter.types = append(filter.types, value)
	}
	return filter, nil
}

// optionalEpoch reads a `type: number` epoch-seconds bound as whole microseconds.
// It shares parseSlackTimestamp's fixed-point reader, which is also the basis of
// bad_timestamp, so a negative or overflowing value is refused here exactly as it
// is on every other timestamp path in this transport.
func optionalEpoch(raw string) (int64, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, nil
	}
	// A bare fraction is a legal JSON number for these parameters, but never a
	// legal Slack `ts`, so it is normalised here rather than in the shared reader.
	if strings.HasPrefix(raw, ".") {
		raw = "0" + raw
	}
	micros, ok := parseSlackTimestamp(raw)
	if !ok {
		return 0, false, decodeFailure("invalid_arg_name", "timestamp filters are non-negative epoch seconds")
	}
	return micros, true, nil
}

func (f fileFilter) matches(file domain.File) bool {
	if f.user != "" && string(file.Uploader) != f.user {
		return false
	}
	if f.channel != "" {
		shared := false
		for _, conversation := range file.SharedChannels {
			if string(conversation) == f.channel {
				shared = true
				break
			}
		}
		if !shared {
			return false
		}
	}
	// Both bounds are declared inclusive.
	created := file.CreatedAt.UnixMicro()
	if f.hasFrom && created < f.tsFrom {
		return false
	}
	if f.hasTo && created > f.tsTo {
		return false
	}
	if len(f.types) > 0 && !slackFileTypeMatches(f.types, file) {
		return false
	}
	return true
}

// slackFileTypeIsDeclared reports whether a `types` name is one the pinned
// operation names. It is the closed set the description enumerates
// ("types=spaces,snippets", default "all"); anything else is unknown_type.
func slackFileTypeIsDeclared(value string) bool {
	switch value {
	case "all", "spaces", "snippets", "images", "gdocs", "zips", "pdfs":
		return true
	default:
		return false
	}
}

// slackFileTypeMatches maps the file-type filter onto the stored media type.
// "all" matches everything; the remaining names follow the published grouping.
// Every name reaching it is one slackFileTypeIsDeclared accepted.
func slackFileTypeMatches(types []string, file domain.File) bool {
	mime := strings.ToLower(file.MIMEType)
	for _, value := range types {
		switch value {
		case "all":
			return true
		case "images":
			if strings.HasPrefix(mime, "image/") {
				return true
			}
		case "videos":
			if strings.HasPrefix(mime, "video/") {
				return true
			}
		case "pdfs":
			if mime == "application/pdf" {
				return true
			}
		case "zips":
			// An archive is an ordinary upload this system stores, so `zips` used
			// to answer an empty list for a workspace full of them.
			if mime == "application/zip" || mime == "application/x-zip-compressed" {
				return true
			}
		case "spaces", "gdocs", "snippets":
			// Slack Posts, Google Docs and snippets are authored inside Slack, not
			// uploaded, and this system produces none of them. `snippets` used to
			// match every text/* upload, which returned ordinary .txt and .csv
			// files for a filter that asks for something else entirely.
		}
	}
	return false
}

const maxUploadBytes = 100 << 20

// maxUploadFieldBytes, maxUploadFields and maxUploadRequestBytes bound an upload
// request as a whole.
//
// Only the per-field ceiling used to exist, and a per-part ceiling bounds nothing
// while the number of parts is unbounded: a multipart body of repeated one-mebibyte
// fields was read into the `fields` map without limit, and neither the multipart
// reader nor `r.Body` was capped, so a 136 MiB request allocated several hundred
// mebibytes of heap. Both upload routes accept a token in the body, so that whole
// cost was payable before any credential had been checked.
const (
	maxUploadFieldBytes = 1 << 20
	maxUploadFields     = 32
	// The file itself may reach maxUploadBytes; the remainder covers the declared
	// fields at their own ceiling plus multipart framing.
	maxUploadRequestBytes = maxUploadBytes + (maxUploadFields+1)*maxUploadFieldBytes
)

func (h Handler) fileUpload(w http.ResponseWriter, r *http.Request) {
	r = promoteQueryToken(r)
	deferAuth := bodyOnlyToken(r)
	var principal auth.Principal
	var err error
	if !deferAuth {
		if principal, err = h.authenticate(r, auth.ScopeFilesWrite); err != nil {
			writeAuthError(w, err)
			return
		}
	}
	spool, fields, filename, mimeType, err := spoolUpload(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// The spool file is removed on every exit from here on. It used to be
	// registered only after the deferred authentication below, so an unauthenticated
	// caller left a file of up to maxUploadBytes on disk on every attempt and
	// nothing ever reclaimed it.
	defer spool.release()
	if deferAuth {
		if principal, err = h.authenticate(withBearerToken(r, fields["token"]), auth.ScopeFilesWrite); err != nil {
			writeAuthError(w, err)
			return
		}
	}
	// files.upload declares channels, initial_comment and thread_ts, and
	// files.completeUploadExternal implements exactly that sharing behaviour, but
	// this path does not route through it. Rejecting is correct only because the
	// alternative — accepting and dropping the arguments — would report success for
	// a file that was never shared. /files.upload enumerates `invalid_channel`,
	// which is the closest declared code for a sharing request it cannot honour.
	//
	// The check lives here and not in spoolUpload because spoolUpload is shared
	// with users.setPhoto, whose enum declares bad_image, too_large and not_found
	// and no invalid_channel at all — so `POST /api/users.setPhoto` carrying a
	// `channels` field was answered with a code that operation does not declare.
	for _, unsupported := range []string{"initial_comment", "channels", "thread_ts"} {
		if strings.TrimSpace(fields[unsupported]) != "" {
			writeError(w, "invalid_channel")
			return
		}
	}
	// A spool-file failure is a server-side failure. `upload_failed` is in no pinned
	// enum; /users.setPhoto declares `fatal_error` and /files.upload declares no
	// server-side code at all, so the pinned spec does not settle files.upload and
	// `fatal_error` is used for both to keep the two siblings consistent.
	source := spool.file
	stat, err := source.Stat()
	if err != nil {
		writeError(w, "fatal_error")
		return
	}
	title := strings.TrimSpace(fields["title"])
	if title == "" {
		title = filename
	}
	file, err := h.Messages.UploadFile(r.Context(), principal.WorkspaceID, principal.UserID, filename, title, mimeType, stat.Size(), source)
	if err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": fileResponse(file)})
}

func (h Handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fileID := domain.FileID(strings.TrimSpace(r.PathValue("file")))
	if fileID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	file, source, err := h.Messages.OpenFile(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return
	}
	defer source.Close()
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	blobHeaders(w, file.MIMEType, file.Name)
	_, _ = io.Copy(w, source)
}

// capabilityHeaders protect a download whose URL is itself the credential.
//
// Both public download routes carry an unguessable token in the path, so the URL
// is a bearer capability: anything that retains it can replay the download. They
// answered with neither header, so a shared or intermediary cache was free to
// store the body under that URL, and any HTML the file is embedded in leaked the
// whole capability onward in the Referer of every outbound link.
func capabilityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// renderableBlobTypes is the closed set of content types this transport is
// willing to name for bytes it did not produce. Every member is a raster image
// format that a browser paints as an image and can never interpret as a
// document, so naming one cannot make an upload run as script on this origin.
// image/svg+xml is deliberately absent: an SVG is an XML document that carries
// <script> and executes when it is rendered at a top-level URL.
var renderableBlobTypes = map[string]struct{}{
	"image/png":  {},
	"image/jpeg": {},
	"image/gif":  {},
	"image/webp": {},
	"image/bmp":  {},
}

// blobHeaders is the header contract of every response in this transport that
// carries bytes out of storage.
//
// Every byte it serves is attacker-supplied: /files.upload and /users.setPhoto
// take both the bytes and their declared type from the request, and nothing below
// this transport verifies that the two agree. Serving the declared type — or, on
// the photo route, the type http.DetectContentType read back out of those same
// bytes — meant that a member could upload an HTML document labelled image/png
// and have the public, unauthenticated capability URL answer
// `200 text/html`. The document then ran on the application's own origin, read
// the CSRF token out of /app and satisfied both halves of the cross-site defence
// honestly: full session takeover, reachable by any member.
//
// So the type is not taken from the request and is not sniffed. It is chosen
// here, from renderableBlobTypes, and everything else is application/octet-stream:
//
//   - Content-Type is a type this system chose, never one it read;
//   - X-Content-Type-Options stops a browser from sniffing its way back to a
//     document type;
//   - Content-Disposition: attachment guarantees a non-image is never rendered at
//     all, and an allow-listed image is served inline so avatars and previews
//     still display;
//   - Content-Security-Policy leaves a rendered document with no capability
//     whatsoever, so the defence still holds if a future browser disregards the
//     three headers above.
//
// This stands alone: it is correct even when the stored bytes are hostile.
// Refusing hostile bytes at the door belongs to the upload path — see the
// follow-up recorded on service.SetUserPhoto — and neither fix substitutes for
// the other.
func blobHeaders(w http.ResponseWriter, declared, filename string) {
	contentType := "application/octet-stream"
	disposition := "attachment"
	if mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(declared)); err == nil {
		if _, ok := renderableBlobTypes[strings.ToLower(mediaType)]; ok {
			contentType, disposition = strings.ToLower(mediaType), "inline"
		}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	if strings.TrimSpace(filename) == "" {
		filename = "download"
	}
	w.Header().Set("Content-Disposition", disposition+"; filename="+strconv.Quote(filepath.Base(filename)))
}

func (h Handler) downloadPublicFile(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" {
		http.NotFound(w, r)
		return
	}
	file, source, err := h.Messages.OpenPublicFile(r.Context(), token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer source.Close()
	capabilityHeaders(w)
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	blobHeaders(w, file.MIMEType, file.Name)
	if _, err := io.Copy(w, source); err != nil {
		return
	}
}

func (h Handler) downloadUserPhoto(w http.ResponseWriter, r *http.Request) {
	workspaceID := domain.WorkspaceID(strings.TrimSpace(r.PathValue("workspace")))
	userID := domain.UserID(strings.TrimSpace(r.PathValue("user")))
	token := strings.TrimSpace(r.PathValue("token"))
	if workspaceID == "" || userID == "" || token == "" {
		http.NotFound(w, r)
		return
	}
	_, source, err := h.Messages.OpenUserPhoto(r.Context(), workspaceID, userID, token)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer source.Close()
	// This always answered application/octet-stream, so a browser downloaded an
	// avatar as a binary blob instead of rendering it. It then answered
	// http.DetectContentType of the stored bytes, which is how an HTML document
	// uploaded as image/png came back as text/html on a public URL. The sniffed
	// value is now a candidate, never the answer: blobHeaders serves it only if it
	// is one of the image types this transport chose to name, and makes the
	// response inert either way. domain.User carries no MIME type and
	// OpenUserPhoto does not return the blob's, so the candidate has to be read
	// from the leading bytes; see the follow-up recorded for OpenUserPhoto.
	prefix := make([]byte, 512)
	read, readErr := io.ReadFull(source, prefix)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		http.NotFound(w, r)
		return
	}
	prefix = prefix[:read]
	capabilityHeaders(w)
	blobHeaders(w, http.DetectContentType(prefix), "photo")
	if _, err := w.Write(prefix); err != nil {
		return
	}
	_, _ = io.Copy(w, source)
}

// bodyOnlyToken reports that the only place this request can carry its token is
// the multipart body. auth.Stored.Authenticate falls back to r.FormValue, which
// calls ParseMultipartForm and consumes the stream; r.MultipartReader() then fails
// with "http: multipart handled by ParseMultipartForm" and the uploaded bytes are
// gone. The pinned /files.upload and /users.setPhoto both declare `token` as a
// formData parameter, so this placement has to work: the upload is spooled first
// and the token taken from the decoded fields.
//
// Reading the body before authenticating is unavoidable for this placement; the
// spool is bounded by maxUploadBytes, which is the same bound an authenticated
// upload already has.
func bodyOnlyToken(r *http.Request) bool {
	if strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) != "" {
		return false
	}
	return strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0])) == "multipart/form-data"
}

// promoteQueryToken moves a URL-query token into the Authorization header so the
// authenticator never reaches r.FormValue, which would consume a multipart body.
//
// It runs before the body is read, so the copy keeps the body. It used to share
// withBearerToken, which empties it: `POST /api/files.upload?token=…` carrying a
// multipart file was answered `invalid_form_data`, because the upload had been
// discarded before the multipart reader ever saw it.
func promoteQueryToken(r *http.Request) *http.Request {
	if strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) != "" {
		return r
	}
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		return r
	}
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	return clone
}

// withBearerToken returns r, or a shallow copy carrying token as a bearer header.
// The copy's body is emptied because its only caller is the deferred
// authentication that runs after the body has already been spooled.
func withBearerToken(r *http.Request, token string) *http.Request {
	token = strings.TrimSpace(token)
	if token == "" || strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) != "" {
		return r
	}
	clone := r.Clone(r.Context())
	clone.Body = http.NoBody
	clone.Header.Set("Authorization", "Bearer "+token)
	return clone
}

// uploadSpool owns the temporary file behind one upload. release removes it, and
// both callers register the release on the statement immediately after
// spoolUpload returns, before any other exit can be taken.
type uploadSpool struct{ file *os.File }

func (s uploadSpool) release() {
	if s.file == nil {
		return
	}
	_ = s.file.Close()
	_ = os.Remove(s.file.Name())
}

func spoolUpload(w http.ResponseWriter, r *http.Request) (uploadSpool, map[string]string, string, string, error) {
	// The request is bounded as a whole before a byte of it is read. Both routes
	// accept the token in the body, so everything below runs for an anonymous
	// caller, and the per-part ceilings alone bounded neither the heap nor the
	// bytes read.
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadRequestBytes)
	temporary, err := os.CreateTemp("", "sameoldchat-upload-*")
	if err != nil {
		return uploadSpool{}, nil, "", "", err
	}
	spool := uploadSpool{file: temporary}
	cleanup := func(uploadErr error) (uploadSpool, map[string]string, string, string, error) {
		spool.release()
		return uploadSpool{}, nil, "", "", uploadErr
	}
	fields := make(map[string]string)
	filename := ""
	mimeType := ""
	seen := make(map[string]struct{})
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if contentType == "multipart/form-data" {
		reader, err := r.MultipartReader()
		if err != nil {
			return cleanup(err)
		}
		for {
			part, nextErr := reader.NextPart()
			if errors.Is(nextErr, io.EOF) {
				break
			}
			if nextErr != nil {
				return cleanup(nextErr)
			}
			name := part.FormName()
			if name == "" {
				part.Close()
				return cleanup(errors.New("multipart field name is required"))
			}
			if _, exists := seen[name]; exists {
				part.Close()
				return cleanup(errors.New("duplicate multipart field"))
			}
			if len(seen) >= maxUploadFields {
				part.Close()
				return cleanup(decodeFailure("invalid_form_data", "upload declares too many fields"))
			}
			seen[name] = struct{}{}
			if name == "file" || name == "image" {
				if part.FileName() == "" {
					part.Close()
					return cleanup(errors.New("file filename is required"))
				}
				filename = filepath.Base(part.FileName())
				mimeType = strings.TrimSpace(part.Header.Get("Content-Type"))
				if mimeType == "application/octet-stream" {
					mimeType = ""
				}
				if err := copyUploadPart(temporary, part); err != nil {
					part.Close()
					return cleanup(err)
				}
				part.Close()
				continue
			}
			value, err := readUploadField(part)
			part.Close()
			if err != nil {
				return cleanup(err)
			}
			fields[name] = value
		}
	} else {
		decoded, err := decodeFields(w, r)
		if err != nil {
			return cleanup(err)
		}
		fields = decoded
		content := fields["content"]
		if content == "" {
			return cleanup(errors.New("content or file is required"))
		}
		if int64(len(content)) > maxUploadBytes {
			return cleanup(errors.New("upload exceeds size limit"))
		}
		if _, err := io.WriteString(temporary, content); err != nil {
			return cleanup(err)
		}
	}
	if fields["content"] != "" && filename != "" {
		return cleanup(errors.New("content and file are mutually exclusive"))
	}
	if filename == "" {
		filename = filepath.Base(strings.TrimSpace(fields["filename"]))
	}
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		return cleanup(errors.New("filename is required"))
	}
	fieldMIME := strings.TrimSpace(fields["mime_type"])
	if fieldMIME != "" && mimeType != "" && fieldMIME != mimeType {
		return cleanup(errors.New("mime type fields disagree"))
	}
	if mimeType == "" {
		mimeType = fieldMIME
	}
	if mimeType == "" && filename != "" {
		mimeType = "application/octet-stream"
	}
	if mimeType == "" {
		return cleanup(errors.New("mime type is required"))
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return cleanup(err)
	}
	return spool, fields, filename, mimeType, nil
}

func readUploadField(part *multipart.Part) (string, error) {
	value, err := io.ReadAll(io.LimitReader(part, maxUploadFieldBytes+1))
	if err != nil {
		return "", err
	}
	if len(value) > maxUploadFieldBytes {
		return "", errors.New("multipart field exceeds size limit")
	}
	return string(value), nil
}

func copyUploadPart(destination *os.File, source io.Reader) error {
	start, err := destination.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	written, err := io.CopyN(destination, source, maxUploadBytes-start+1)
	if err == nil || written > maxUploadBytes {
		return errors.New("upload exceeds size limit")
	}
	if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func fileResponse(file domain.File) map[string]any {
	fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(file.Name)), ".")
	result := map[string]any{
		"id": file.ID, "name": file.Name, "title": file.Title, "mimetype": file.MIMEType,
		"size": file.Size, "created": file.CreatedAt.Unix(), "timestamp": file.CreatedAt.Unix(),
		"user": file.Uploader, "is_public": file.PublicToken != "", "team_id": file.WorkspaceID,
		"filetype": fileType, "pretty_type": strings.ToUpper(fileType), "mode": "hosted",
		"is_external": false, "external_type": "", "public_url_shared": file.PublicToken != "",
		"editable": false, "display_as_bot": false,
	}
	if !file.Deleted {
		result["url_private"] = "/api/files/" + url.PathEscape(string(file.ID))
		result["url_private_download"] = "/api/files/" + url.PathEscape(string(file.ID))
	}
	if file.PublicToken != "" {
		result["permalink_public"] = "/files/public/" + file.PublicToken
	}
	if len(file.SharedChannels) > 0 {
		result["channels"] = file.SharedChannels
	}
	return result
}

func normalizeReactionFields(fields map[string]string) (domain.ConversationID, domain.MessageTimestamp, string, error) {
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		return "", "", "", err
	}
	name := strings.TrimSpace(fields["name"])
	if name == "" {
		// reactions.add/remove enumerate invalid_name for a missing emoji name.
		return "", "", "", decodeFailure("invalid_name", "name is required")
	}
	return channel, timestamp, name, nil
}

func normalizeReactionTarget(fields map[string]string) (domain.ConversationID, domain.MessageTimestamp, error) {
	channel := strings.TrimSpace(fields["channel"])
	timestamp := strings.TrimSpace(fields["timestamp"])
	// reactions.*, pins.* and stars.* all enumerate no_item_specified for a
	// missing item and bad_timestamp for one that is not a Slack timestamp.
	if channel == "" || timestamp == "" {
		return "", "", decodeFailure("no_item_specified", "channel and timestamp are required")
	}
	if _, ok := parseSlackTimestamp(timestamp); !ok {
		return "", "", decodeFailure("bad_timestamp", "timestamp is not a Slack timestamp")
	}
	return domain.ConversationID(channel), domain.MessageTimestamp(timestamp), nil
}

func parseBoolField(value string) (bool, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "false" || value == "0" {
		return false, nil
	}
	if value == "true" || value == "1" {
		return true, nil
	}
	return false, errors.New("boolean field is invalid")
}

// decodeCursor validates a wire list cursor and names the failure with the code
// the calling operation declares.
//
// A shared decoder cannot pick the code: `invalid_cursor` appears in only 6 of
// the 99 pinned enums, so emitting it everywhere leaked a code most operations
// forbid, while the endpoints that skipped validation altogether let
// domain.ErrInvalidCursor reach the service mapper — which names service.Err*
// and store.Err* only — so `?cursor=!!!!` was answered `fatal_error`, a handled
// input error presented as a server fault.
func decodeCursor(raw, invalidReason string) (domain.Cursor, error) {
	cursor := domain.Cursor(strings.TrimSpace(raw))
	if _, err := domain.DecodeListCursor(cursor); err != nil {
		return "", decodeFailure(invalidReason, "cursor is not a list cursor")
	}
	return cursor, nil
}

// decodeMessageCursor is decodeCursor for the keyset cursor the message-ordered
// collections mint.
func decodeMessageCursor(raw, invalidReason string) (domain.Cursor, error) {
	cursor := domain.Cursor(strings.TrimSpace(raw))
	if cursor == "" {
		return "", nil
	}
	if _, _, err := domain.DecodeMessageCursor(cursor); err != nil {
		return "", decodeFailure(invalidReason, "cursor is not a message cursor")
	}
	return cursor, nil
}

func decodeListRequestFields(fields map[string]string, invalidCursorReason string) (domain.PageRequest, error) {
	limit, err := clampLimit(fields["limit"], 100, 200)
	if err != nil {
		return domain.PageRequest{}, err
	}
	cursor, err := decodeCursor(fields["cursor"], invalidCursorReason)
	if err != nil {
		return domain.PageRequest{}, err
	}
	return domain.PageRequest{Limit: limit, Cursor: cursor}, nil
}

func (h Handler) postMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	message, err := h.postMessageValue(r, principal, fields, "")
	if err != nil {
		writeError(w, postMessageError(err))
		return
	}
	ts := slackTimestamp(message.CreatedAt)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": message.Conversation, "ts": ts, "message": messageResponse(message)})
}

func (h Handler) chatUnfurl(w http.ResponseWriter, r *http.Request) {
	// Pinned /chat.unfurl token parameter: "Requires scope: `links:write`". It used
	// to accept `chat:write`, which is the scope for posting a message, not for
	// attaching link previews to someone else's.
	principal, err := h.authenticate(r, auth.ScopeLinksWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// /chat.unfurl declares missing_unfurls for an absent `unfurls` and
	// invalid_arg_name only for a malformed one; the two used to be collapsed.
	if strings.TrimSpace(fields["unfurls"]) == "" {
		writeError(w, "missing_unfurls")
		return
	}
	var rawUnfurls map[string]json.RawMessage
	if json.Unmarshal([]byte(fields["unfurls"]), &rawUnfurls) != nil || rawUnfurls == nil {
		writeError(w, "invalid_arg_name")
		return
	}
	unfurls := make(map[string]string, len(rawUnfurls))
	for key, raw := range rawUnfurls {
		unfurls[key] = string(raw)
	}
	message, err := h.Messages.Unfurl(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel"])), domain.MessageTimestamp(strings.TrimSpace(fields["ts"])), unfurls)
	if err != nil {
		// /chat.unfurl declares cannot_unfurl_url; message_not_found is not in its
		// enum.
		writeError(w, mapServiceError(err, "cannot_unfurl_url"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": messageResponse(message)})
}

func (h Handler) meMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	message, err := h.postMessageValue(r, principal, fields, domain.MessageSubtypeMeMessage)
	if err != nil {
		writeError(w, postMessageError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": message.Conversation, "ts": slackTimestamp(message.CreatedAt)})
}

func (h Handler) postEphemeral(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := checkMessageLength(fields); err != nil {
		writeDecodeError(w, err)
		return
	}
	blocks, blockErr := domain.NormalizeBlocks([]byte(fields["blocks"]))
	attachments, attachmentErr := domain.NormalizeAttachments([]byte(fields["attachments"]))
	if blockErr != nil || attachmentErr != nil || strings.TrimSpace(fields["channel"]) == "" || strings.TrimSpace(fields["user"]) == "" || (strings.TrimSpace(fields["text"]) == "" && blocks == "" && attachments == "") {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.PostEphemeralWithBlocksAndAttachments(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel"])), domain.UserID(strings.TrimSpace(fields["user"])), fields["text"], blocks, attachments, principal.AppID)
	if err != nil {
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	response := map[string]any{"ok": true, "message_ts": value.Timestamp}
	if value.Blocks != "" {
		response["message"] = map[string]any{"text": value.Text, "blocks": json.RawMessage(value.Blocks)}
	}
	if value.Attachments != "" && value.Attachments != "[]" {
		if response["message"] == nil {
			response["message"] = map[string]any{"text": value.Text}
		}
		response["message"].(map[string]any)["attachments"] = json.RawMessage(value.Attachments)
	}
	writeJSON(w, http.StatusOK, response)
}

// maxMessageTextRunes and maxMessageBodyBytes bound one stored message.
//
// `msg_too_long` is declared by every operation that writes a message —
// chat.postMessage, chat.postEphemeral, chat.update, chat.scheduleMessage and
// chat.meMessage — and was emitted by none of them, because no length was ever
// checked. An oversized body is not merely a large write: it is re-served in full
// by every later conversations.history, conversations.replies and search.messages
// read of that channel, so a single unbounded post turns a 51-byte GET into a
// response of hundreds of mebibytes.
//
// 40,000 characters is Slack's documented ceiling for a message body, and it is
// what msg_too_long names. The byte ceiling for `blocks`/`attachments` is derived
// from the same published limits — 50 blocks at 3,000 characters per text object
// is 150,000 characters — leaving room for the JSON framing around them without
// admitting an unbounded structured body.
const (
	maxMessageTextRunes = service.MaxMessageTextRunes
	maxMessageBodyBytes = service.MaxMessageBodyBytes
)

// checkMessageLength refuses a message body above the ceiling with the code the
// writing operations declare.
func checkMessageLength(fields map[string]string) error {
	if utf8.RuneCountInString(fields["text"]) > maxMessageTextRunes {
		return decodeFailure("msg_too_long", "message text exceeds the maximum length")
	}
	if len(fields["blocks"])+len(fields["attachments"]) > maxMessageBodyBytes {
		return decodeFailure("msg_too_long", "message blocks and attachments exceed the maximum length")
	}
	return nil
}

func (h Handler) postMessageValue(r *http.Request, principal auth.Principal, fields map[string]string, subtype domain.MessageSubtype) (domain.Message, error) {
	// The service rejects an empty channel as ErrInvalidMessage, which
	// postMessageError renames `no_text` — so a request with text and no channel
	// was told its text was missing. Every sibling (normalizeHistoryRequest,
	// listPins, closeConversation) validates the channel in the handler, and
	// /chat.postMessage and /chat.meMessage both declare channel_not_found.
	if strings.TrimSpace(fields["channel"]) == "" {
		return domain.Message{}, decodeFailure("channel_not_found", "channel is required")
	}
	if err := checkMessageLength(fields); err != nil {
		return domain.Message{}, err
	}
	markdownText := fields["markdown_text"]
	if markdownText != "" {
		if fields["text"] != "" || strings.TrimSpace(fields["blocks"]) != "" {
			return domain.Message{}, decodeFailure("markdown_text_conflict", "markdown_text cannot be combined with text or blocks")
		}
		if utf8.RuneCountInString(markdownText) > 12000 {
			return domain.Message{}, decodeFailure("msg_too_long", "markdown_text exceeds the maximum length")
		}
	}
	if strings.TrimSpace(fields["metadata"]) != "" && principal.AppID == "" {
		return domain.Message{}, decodeFailure("metadata_must_be_sent_from_app", "message metadata requires an app token")
	}
	parse := strings.TrimSpace(fields["parse"])
	if parse != "" && parse != "none" && parse != "full" {
		return domain.Message{}, decodeFailure("invalid_arguments", "parse must be none or full")
	}
	optionalBool := func(name string) (*bool, error) {
		raw := strings.TrimSpace(fields[name])
		if raw == "" {
			return nil, nil
		}
		value, parseErr := parseBoolField(raw)
		if parseErr != nil {
			return nil, decodeFailure("invalid_arguments", name+" must be boolean")
		}
		return &value, nil
	}
	replyBroadcast, err := optionalBool("reply_broadcast")
	if err != nil {
		return domain.Message{}, err
	}
	linkNames, err := optionalBool("link_names")
	if err != nil {
		return domain.Message{}, err
	}
	mrkdwn, err := optionalBool("mrkdwn")
	if err != nil {
		return domain.Message{}, err
	}
	unfurlLinks, err := optionalBool("unfurl_links")
	if err != nil {
		return domain.Message{}, err
	}
	unfurlMedia, err := optionalBool("unfurl_media")
	if err != nil {
		return domain.Message{}, err
	}
	asUser, err := optionalBool("as_user")
	if err != nil {
		return domain.Message{}, err
	}
	if asUser != nil && *asUser {
		return domain.Message{}, decodeFailure("as_user_not_supported", "as_user is only available to classic apps")
	}
	username := strings.TrimSpace(fields["username"])
	iconEmoji := strings.TrimSpace(fields["icon_emoji"])
	iconURL := strings.TrimSpace(fields["icon_url"])
	if username != "" || iconEmoji != "" || iconURL != "" {
		if principal.AppID == "" || !principal.HasScope(auth.ScopeChatWriteCustomize) {
			return domain.Message{}, decodeFailure("missing_scope", "message customization requires chat:write.customize")
		}
	}
	if iconEmoji != "" {
		iconURL = ""
	}
	blocks, err := domain.NormalizeBlocks([]byte(fields["blocks"]))
	if err != nil {
		return domain.Message{}, service.ErrInvalidMessage
	}
	attachments, err := domain.NormalizeAttachments([]byte(fields["attachments"]))
	if err != nil {
		return domain.Message{}, service.ErrInvalidMessage
	}
	text := fields["text"]
	if markdownText != "" {
		text = markdownText
	}
	return h.Messages.PostMessageAs(
		r.Context(),
		principal.WorkspaceID,
		principal.UserID,
		domain.MessagePostRequest{
			Conversation: domain.ConversationID(strings.TrimSpace(fields["channel"])),
			Text:         text, Blocks: blocks, Attachments: attachments, Metadata: fields["metadata"],
			ThreadTimestamp: domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"])),
			IdempotencyKey:  strings.TrimSpace(r.Header.Get("Idempotency-Key")), AppID: principal.AppID,
			MarkdownText: markdownText != "", ReplyBroadcast: replyBroadcast != nil && *replyBroadcast,
			Parse: parse, MrkdwnDisabled: mrkdwn != nil && !*mrkdwn, LinkNames: linkNames != nil && *linkNames,
			UnfurlLinks: unfurlLinks, UnfurlMedia: unfurlMedia,
			Username: username, IconEmoji: iconEmoji, IconURL: iconURL, Subtype: subtype,
		},
	)
}

// postMessageError names the failure of a message mutation. `chat.postMessage`
// and its siblings enumerate `channel_not_found`, `is_archived`, `no_text`,
// `msg_too_long` and `invalid_blocks`; none of them enumerate
// `invalid_arguments`, so a rejected message body is reported as `no_text` when
// it carries no renderable content and `invalid_blocks` when the supplied
// blocks or attachments are unusable.
func postMessageError(err error) string {
	// A refusal raised by the handler's own decoding already names the code its
	// operation declares (msg_too_long, channel_not_found); renaming it here would
	// discard the reason the request was refused.
	var typed decodeError
	if errors.As(err, &typed) {
		return typed.code
	}
	if errors.Is(err, store.ErrNotFound) {
		return "channel_not_found"
	}
	if errors.Is(err, service.ErrConversationAlreadyArchived) {
		return "is_archived"
	}
	if errors.Is(err, service.ErrInvalidMessage) {
		return "no_text"
	}
	return mapServiceError(err, "channel_not_found")
}

func (h Handler) updateMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := checkMessageLength(fields); err != nil {
		writeDecodeError(w, err)
		return
	}
	conversation, timestamp := strings.TrimSpace(fields["channel"]), strings.TrimSpace(fields["ts"])
	text, hasText := fields["text"]
	rawBlocks, hasBlocks := fields["blocks"]
	rawAttachments, hasAttachments := fields["attachments"]
	blocks, blockErr := domain.NormalizeBlocks([]byte(rawBlocks))
	attachments, attachmentErr := domain.NormalizeAttachments([]byte(rawAttachments))
	if conversation == "" || timestamp == "" || (!hasText && !hasBlocks && !hasAttachments) || blockErr != nil || attachmentErr != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	patch := domain.MessagePatch{}
	if hasText {
		patch.Text = &text
	}
	if hasBlocks {
		patch.Blocks = &blocks
	}
	if hasAttachments {
		patch.Attachments = &attachments
	}
	message, err := h.Messages.UpdateMessage(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(conversation), domain.MessageTimestamp(timestamp), patch)
	if err != nil {
		writeError(w, mapServiceError(err, "message_not_found"))
		return
	}
	ts := slackTimestamp(message.CreatedAt)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": message.Conversation, "ts": ts, "text": message.Text, "message": messageResponse(message)})
}

func (h Handler) startMessageStream(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.messageStreamRequest(w, r)
	if !ok {
		return
	}
	channel := strings.TrimSpace(fields["channel"])
	threadTimestamp := strings.TrimSpace(fields["thread_ts"])
	if channel == "" || threadTimestamp == "" {
		writeError(w, "invalid_arguments")
		return
	}
	message, err := h.Messages.StartMessageStream(r.Context(), principal.WorkspaceID, principal.UserID, domain.MessageStreamStart{
		Conversation: domain.ConversationID(channel), ThreadTimestamp: domain.MessageTimestamp(threadTimestamp),
		AppID: principal.AppID, BotID: principal.BotID, RecipientTeamID: domain.WorkspaceID(strings.TrimSpace(fields["recipient_team_id"])),
		RecipientUserID: domain.UserID(strings.TrimSpace(fields["recipient_user_id"])),
		MarkdownText:    fields["markdown_text"], Chunks: fields["chunks"],
		TaskDisplayMode: fields["task_display_mode"], Username: fields["username"],
		IconEmoji: fields["icon_emoji"], IconURL: fields["icon_url"],
	})
	if err != nil {
		writeError(w, messageStreamError(err))
		return
	}
	writeJSON(w, http.StatusOK, messageStreamResponse(message, false))
}

func (h Handler) appendMessageStream(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.messageStreamRequest(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(fields["channel"]) == "" || strings.TrimSpace(fields["ts"]) == "" ||
		(fields["markdown_text"] == "" && strings.TrimSpace(fields["chunks"]) == "") {
		writeError(w, "invalid_arguments")
		return
	}
	message, err := h.Messages.AppendMessageStream(r.Context(), principal.WorkspaceID, principal.UserID, domain.MessageStreamMutation{
		Conversation: domain.ConversationID(strings.TrimSpace(fields["channel"])),
		Timestamp:    domain.MessageTimestamp(strings.TrimSpace(fields["ts"])), AppID: principal.AppID,
		MarkdownText: fields["markdown_text"], Chunks: fields["chunks"],
	})
	if err != nil {
		writeError(w, messageStreamError(err))
		return
	}
	writeJSON(w, http.StatusOK, messageStreamResponse(message, false))
}

func (h Handler) stopMessageStream(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.messageStreamRequest(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(fields["channel"]) == "" || strings.TrimSpace(fields["ts"]) == "" {
		writeError(w, "invalid_arguments")
		return
	}
	message, err := h.Messages.StopMessageStream(r.Context(), principal.WorkspaceID, principal.UserID, domain.MessageStreamMutation{
		Conversation: domain.ConversationID(strings.TrimSpace(fields["channel"])),
		Timestamp:    domain.MessageTimestamp(strings.TrimSpace(fields["ts"])), AppID: principal.AppID,
		MarkdownText: fields["markdown_text"], Chunks: fields["chunks"], Blocks: fields["blocks"], Metadata: fields["metadata"],
	})
	if err != nil {
		writeError(w, messageStreamError(err))
		return
	}
	writeJSON(w, http.StatusOK, messageStreamResponse(message, true))
}

func messageStreamResponse(message domain.Message, includeMessage bool) map[string]any {
	response := map[string]any{"ok": true, "channel": message.Conversation, "ts": slackTimestamp(message.CreatedAt)}
	var state domain.MessageStreamState
	if json.Unmarshal([]byte(message.StreamState), &state) == nil && len(state.Warnings) != 0 {
		response["warning"] = strings.Join(state.Warnings, ",")
		response["response_metadata"] = map[string]any{"warnings": state.Warnings}
	}
	if includeMessage {
		response["message"] = messageResponse(message)
	}
	return response
}

func (h Handler) messageStreamRequest(w http.ResponseWriter, r *http.Request) (auth.Principal, map[string]string, bool) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return auth.Principal{}, nil, false
	}
	if principal.TokenType != "bot" || principal.AppID == "" {
		writeError(w, "not_allowed_token_type")
		return auth.Principal{}, nil, false
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return auth.Principal{}, nil, false
	}
	if utf8.RuneCountInString(fields["markdown_text"]) > service.MaxStreamMarkdownRunes {
		writeError(w, "msg_too_long")
		return auth.Principal{}, nil, false
	}
	return principal, fields, true
}

func messageStreamError(err error) string {
	switch {
	case errors.Is(err, service.ErrMissingStreamRecipientTeam):
		return "missing_recipient_team_id"
	case errors.Is(err, service.ErrMissingStreamRecipientUser):
		return "missing_recipient_user_id"
	case errors.Is(err, service.ErrInvalidStreamChunks):
		return "invalid_chunks"
	case errors.Is(err, service.ErrMessageNotStreaming):
		return "message_not_in_streaming_state"
	case errors.Is(err, service.ErrMessageNotOwnedByApp):
		return "message_not_owned_by_app"
	case errors.Is(err, service.ErrInvalidTimestamp), errors.Is(err, service.ErrInvalidMessageStream):
		return "invalid_arguments"
	case errors.Is(err, service.ErrNotInConversation):
		return "not_in_channel"
	case errors.Is(err, store.ErrNotFound):
		return "message_not_found"
	default:
		return mapServiceError(err, "channel_not_found")
	}
}

func (h Handler) deleteMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	conversation, timestamp := strings.TrimSpace(fields["channel"]), strings.TrimSpace(fields["ts"])
	if conversation == "" || timestamp == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	message, err := h.Messages.Delete(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(conversation), domain.MessageTimestamp(timestamp))
	if err != nil {
		writeError(w, mapServiceError(err, "message_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": message.Conversation, "ts": slackTimestamp(message.CreatedAt)})
}

func scheduledMessageResponse(value domain.ScheduledMessage) map[string]any {
	response := map[string]any{"id": value.ID, "channel_id": value.Channel, "post_at": value.PostAt.Unix(), "date_created": value.CreatedAt.Unix(), "text": value.Text}
	if value.ThreadTimestamp != "" {
		response["thread_ts"] = value.ThreadTimestamp
	}
	return addScheduledRichContent(response, value)
}

func scheduledMessagePayloadResponse(value domain.ScheduledMessage) map[string]any {
	response := map[string]any{"text": value.Text, "type": "delayed_message"}
	if value.BotID != "" {
		response["bot_id"] = value.BotID
		response["subtype"] = "bot_message"
	}
	if value.ThreadTimestamp != "" {
		response["thread_ts"] = value.ThreadTimestamp
	}
	return addScheduledRichContent(response, value)
}

func addScheduledRichContent(response map[string]any, value domain.ScheduledMessage) map[string]any {
	if value.Blocks != "" {
		response["blocks"] = json.RawMessage(value.Blocks)
	}
	if value.Attachments != "" && value.Attachments != "[]" {
		response["attachments"] = json.RawMessage(value.Attachments)
	}
	return response
}

func (h Handler) scheduleMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := checkMessageLength(fields); err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	textValue := strings.TrimSpace(fields["text"])
	markdownText := fields["markdown_text"]
	if markdownText != "" {
		if fields["text"] != "" || strings.TrimSpace(fields["blocks"]) != "" {
			writeError(w, "markdown_text_conflict")
			return
		}
		if utf8.RuneCountInString(markdownText) > 12000 {
			writeError(w, "msg_too_long")
			return
		}
		textValue = markdownText
	}
	if strings.TrimSpace(fields["metadata"]) != "" && principal.AppID == "" {
		writeError(w, "metadata_must_be_sent_from_app")
		return
	}
	blocks, blockErr := domain.NormalizeBlocks([]byte(fields["blocks"]))
	attachments, attachmentErr := domain.NormalizeAttachments([]byte(fields["attachments"]))
	postAt, err := strconv.ParseInt(strings.TrimSpace(fields["post_at"]), 10, 64)
	optionalBoolean := func(name string) (*bool, bool) {
		raw := strings.TrimSpace(fields[name])
		if raw == "" {
			return nil, true
		}
		value, parseErr := parseBoolField(raw)
		return &value, parseErr == nil
	}
	replyBroadcast, replyBroadcastOK := optionalBoolean("reply_broadcast")
	asUser, asUserOK := optionalBoolean("as_user")
	linkNames, linkNamesOK := optionalBoolean("link_names")
	unfurlLinks, unfurlLinksOK := optionalBoolean("unfurl_links")
	unfurlMedia, unfurlMediaOK := optionalBoolean("unfurl_media")
	parse := strings.TrimSpace(fields["parse"])
	if channel == "" || (textValue == "" && blocks == "" && attachments == "") || blockErr != nil || attachmentErr != nil || err != nil || postAt <= 0 ||
		!replyBroadcastOK || !asUserOK || !linkNamesOK || !unfurlLinksOK || !unfurlMediaOK ||
		(parse != "" && parse != "none" && parse != "full") || (asUser != nil && *asUser) {
		writeError(w, "invalid_arguments")
		return
	}
	state := domain.MessageStreamState{
		MarkdownText: markdownText != "", ReplyBroadcast: replyBroadcast != nil && *replyBroadcast,
		Parse: parse, LinkNames: linkNames != nil && *linkNames, UnfurlLinks: unfurlLinks, UnfurlMedia: unfurlMedia,
	}
	encodedState, err := json.Marshal(state)
	if err != nil {
		writeError(w, "internal_error")
		return
	}
	streamState := ""
	if string(encodedState) != "{}" {
		streamState = string(encodedState)
	}
	value, err := h.Messages.ScheduleMessageAs(r.Context(), principal.WorkspaceID, principal.UserID, domain.ScheduledMessageRequest{
		Channel: channel, Text: textValue, Blocks: blocks, Attachments: attachments,
		Metadata: fields["metadata"], StreamState: streamState,
		ThreadTimestamp: domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"])),
		PostAt:          time.Unix(postAt, 0).UTC(), AppID: principal.AppID, BotID: principal.BotID,
		CredentialHash: principal.CredentialHash,
	})
	if err != nil {
		if errors.Is(err, service.ErrScheduledTimeInPast) {
			writeError(w, "time_in_past")
			return
		}
		if errors.Is(err, service.ErrScheduledTimeTooFar) {
			writeError(w, "time_too_far")
			return
		}
		if errors.Is(err, service.ErrScheduledTooMany) {
			writeError(w, "restricted_too_many")
			return
		}
		if errors.Is(err, service.ErrConversationAlreadyArchived) {
			writeError(w, "is_archived")
			return
		}
		writeError(w, mapServiceError(err, "channel_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": value.Channel, "post_at": strconv.FormatInt(value.PostAt.Unix(), 10), "scheduled_message_id": value.ID, "message": scheduledMessagePayloadResponse(value)})
}

func (h Handler) scheduledMessagesList(w http.ResponseWriter, r *http.Request) {
	// /chat.scheduledMessages.list declares `security: [{slackAuth: ["none"]}]`
	// and its token parameter reads "Requires scope: `none`". Enforcing chat:write
	// refused a token that may read scheduled messages but not write them.
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	limit, err := clampLimit(fields["limit"], 100, 1000)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	cursor, err := decodeCursor(fields["cursor"], "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	parseRange := func(name string) (time.Time, error) {
		raw := strings.TrimSpace(fields[name])
		if raw == "" {
			return time.Time{}, nil
		}
		seconds, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || seconds < 0 {
			return time.Time{}, errors.New("invalid scheduled-message range")
		}
		return time.Unix(seconds, 0).UTC(), nil
	}
	oldest, oldestErr := parseRange("oldest")
	latest, latestErr := parseRange("latest")
	if oldestErr != nil || latestErr != nil || (!oldest.IsZero() && !latest.IsZero() && !oldest.Before(latest)) {
		writeError(w, "invalid_arguments")
		return
	}
	page, err := h.Messages.ScheduledMessagesForCredential(r.Context(), principal.WorkspaceID, principal.UserID, domain.ScheduledMessageQuery{
		CredentialHash: principal.CredentialHash,
		Channel:        domain.ConversationID(strings.TrimSpace(fields["channel"])),
		Oldest:         oldest,
		Latest:         latest,
		Page:           domain.PageRequest{Limit: limit, Cursor: cursor},
	})
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, value := range page.Items {
		items = append(items, scheduledMessageResponse(value))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scheduled_messages": items, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}

func (h Handler) deleteScheduledMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	id := domain.ScheduledMessageID(strings.TrimSpace(fields["scheduled_message_id"]))
	if channel == "" || id == "" {
		writeError(w, "invalid_arguments")
		return
	}
	if err := h.Messages.DeleteScheduledMessageForCredential(r.Context(), principal.WorkspaceID, principal.UserID, principal.CredentialHash, channel, id); err != nil {
		writeError(w, mapServiceError(err, "invalid_scheduled_message_id"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func userGroupResponse(value domain.UserGroup, includeUsers bool) map[string]any {
	users := make([]string, 0, len(value.Users))
	for _, user := range value.Users {
		users = append(users, string(user))
	}
	result := map[string]any{"id": value.ID, "team_id": value.WorkspaceID, "is_usergroup": true, "is_subteam": true, "name": value.Name, "description": value.Description, "handle": value.Handle, "is_external": false, "date_create": value.CreatedAt.Unix(), "date_update": value.UpdatedAt.Unix(), "date_delete": int64(0), "auto_provision": false, "enterprise_subteam_id": "", "created_by": value.Creator, "updated_by": value.UpdatedBy, "user_count": len(users)}
	if !value.DeletedAt.IsZero() {
		result["date_delete"] = value.DeletedAt.Unix()
	}
	if includeUsers {
		result["users"] = users
	}
	return result
}

func (h Handler) createUserGroup(w http.ResponseWriter, r *http.Request) {
	h.mutateUserGroup(w, r, func(p auth.Principal, f map[string]string) (domain.UserGroup, error) {
		return h.Messages.CreateUserGroup(r.Context(), p.WorkspaceID, p.UserID, f["name"], f["handle"], f["description"])
	})
}
func (h Handler) updateUserGroup(w http.ResponseWriter, r *http.Request) {
	h.mutateUserGroup(w, r, func(p auth.Principal, f map[string]string) (domain.UserGroup, error) {
		return h.Messages.UpdateUserGroup(r.Context(), p.WorkspaceID, p.UserID, domain.UserGroupID(strings.TrimSpace(f["usergroup"])), f["name"], f["handle"], f["description"])
	})
}
func (h Handler) enableUserGroup(w http.ResponseWriter, r *http.Request) {
	h.toggleUserGroup(w, r, true)
}
func (h Handler) disableUserGroup(w http.ResponseWriter, r *http.Request) {
	h.toggleUserGroup(w, r, false)
}
func (h Handler) toggleUserGroup(w http.ResponseWriter, r *http.Request, enabled bool) {
	h.mutateUserGroup(w, r, func(p auth.Principal, f map[string]string) (domain.UserGroup, error) {
		return h.Messages.SetUserGroupEnabled(r.Context(), p.WorkspaceID, p.UserID, domain.UserGroupID(strings.TrimSpace(f["usergroup"])), enabled)
	})
}
func (h Handler) mutateUserGroup(w http.ResponseWriter, r *http.Request, operation func(auth.Principal, map[string]string) (domain.UserGroup, error)) {
	principal, err := h.authenticate(r, auth.ScopeUserGroupsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	value, err := operation(principal, fields)
	if err != nil {
		writeError(w, mapServiceError(err, "usergroup_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "usergroup": userGroupResponse(value, true)})
}
func (h Handler) listUserGroups(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUserGroupsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// These were raw `== "true"` comparisons, so the documented boolean form
	// `include_users=1` was silently read as false and the `users` array was omitted
	// from a response that reported success.
	includeDisabled, err := parseBoolField(fields["include_disabled"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	includeUsers, err := parseBoolField(fields["include_users"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	page, err := h.Messages.ListUserGroups(r.Context(), principal.WorkspaceID, principal.UserID, includeDisabled, request)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	items := make([]map[string]any, 0, len(page.Groups))
	for _, value := range page.Groups {
		items = append(items, userGroupResponse(value, includeUsers))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "usergroups": items, "has_more": page.HasMore, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}
func (h Handler) userGroupUsers(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUserGroupsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	group := domain.UserGroupID(strings.TrimSpace(fields["usergroup"]))
	if group == "" {
		// `usergroup` is required:true; forwarding "" made the store answer for no
		// group at all.
		writeError(w, "invalid_arg_name")
		return
	}
	values, err := h.Messages.UserGroupUsers(r.Context(), principal.WorkspaceID, principal.UserID, group)
	if err != nil {
		writeError(w, mapServiceError(err, "usergroup_not_found"))
		return
	}
	users := make([]string, 0, len(values))
	for _, value := range values {
		users = append(users, string(value))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "users": users})
}
func (h Handler) updateUserGroupUsers(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUserGroupsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	// Both `usergroup` and `users` are required:true in the pinned contract. An
	// absent `users` used to be read as the empty list, so a request that omitted
	// the mandatory argument removed every member and answered `"ok":true`. Absent
	// is distinguished from present-and-empty so that deliberately emptying a group
	// remains possible.
	group := domain.UserGroupID(strings.TrimSpace(fields["usergroup"]))
	if group == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	raw, present := fields["users"]
	if !present {
		writeError(w, "invalid_arg_name")
		return
	}
	users := make([]domain.UserID, 0)
	for _, value := range strings.Split(raw, ",") {
		if strings.TrimSpace(value) != "" {
			users = append(users, domain.UserID(strings.TrimSpace(value)))
		}
	}
	value, err := h.Messages.SetUserGroupUsers(r.Context(), principal.WorkspaceID, principal.UserID, group, users)
	if err != nil {
		writeError(w, mapServiceError(err, "usergroup_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "usergroup": userGroupResponse(value, true)})
}

func (h Handler) adminUserGroupAddChannels(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channels := parseIDList[domain.ConversationID](fields["channel_ids"])
	groupID := strings.TrimSpace(fields["usergroup"])
	if groupID == "" {
		groupID = strings.TrimSpace(fields["usergroup_id"])
	}
	if len(channels) == 0 || groupID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.AddUserGroupChannels(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(groupID), channels); err != nil {
		writeError(w, mapServiceError(err, "usergroup_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminUserGroupAddTeams(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	groupID := strings.TrimSpace(fields["usergroup_id"])
	if groupID == "" {
		groupID = strings.TrimSpace(fields["usergroup"])
	}
	parts := strings.Split(fields["team_ids"], ",")
	teams := make([]domain.WorkspaceID, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			teams = append(teams, domain.WorkspaceID(value))
		}
	}
	if groupID == "" || len(teams) == 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	// Same organization constraint as admin.conversations.setTeams. The service
	// enforces it too; naming it here keeps the client-visible code stable and the
	// two admin.*Teams handlers from diverging again.
	if _, ok := foreignWorkspace(teams, principal.WorkspaceID); ok {
		writeError(w, "invalid_team")
		return
	}
	if err := h.Messages.AdminAddUserGroupTeams(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(groupID), teams); err != nil {
		writeError(w, mapServiceError(err, "usergroup_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) adminUserGroupRemoveChannels(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channels := parseIDList[domain.ConversationID](fields["channel_ids"])
	groupID := strings.TrimSpace(fields["usergroup"])
	if groupID == "" {
		groupID = strings.TrimSpace(fields["usergroup_id"])
	}
	if len(channels) == 0 || groupID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.RemoveUserGroupChannels(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(groupID), channels); err != nil {
		writeError(w, mapServiceError(err, "usergroup_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (h Handler) adminUserGroupListChannels(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	groupID := strings.TrimSpace(fields["usergroup"])
	if groupID == "" {
		groupID = strings.TrimSpace(fields["usergroup_id"])
	}
	if groupID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	channels, err := h.Messages.UserGroupChannels(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(groupID))
	if err != nil {
		writeError(w, mapServiceError(err, "usergroup_not_found"))
		return
	}
	values := make([]string, 0, len(channels))
	channelObjects := make([]map[string]any, 0, len(channels))
	for _, channel := range channels {
		values = append(values, string(channel))
		channelObjects = append(channelObjects, map[string]any{"id": channel})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel_ids": values, "channels": channelObjects})
}

func (h Handler) adminTeamSettingsInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	value, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": workspaceSettingsResponse(value)})
}

func (h Handler) adminTeamSettingsSetName(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["name"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.AdminSetWorkspaceName(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"])
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": map[string]any{"id": value.ID, "name": value.Name, "description": value.Description}})
}

func (h Handler) adminTeamSettingsSetDescription(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.AdminSetWorkspaceDescription(r.Context(), principal.WorkspaceID, principal.UserID, fields["description"])
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": map[string]any{"id": value.ID, "name": value.Name, "description": value.Description, "discoverability": value.Discoverability}})
}

func (h Handler) adminTeamSettingsSetDiscoverability(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.AdminSetWorkspaceDiscoverability(r.Context(), principal.WorkspaceID, principal.UserID, domain.WorkspaceDiscoverability(strings.TrimSpace(fields["discoverability"])))
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": map[string]any{"id": value.ID, "name": value.Name, "description": value.Description, "discoverability": value.Discoverability, "icon_url": value.IconURL}})
}

func (h Handler) adminTeamSettingsSetIcon(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["image_url"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.AdminSetWorkspaceIcon(r.Context(), principal.WorkspaceID, principal.UserID, fields["image_url"])
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": workspaceSettingsResponse(value)})
}

func (h Handler) adminTeamSettingsSetDefaultChannels(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	channels := parseIDList[domain.ConversationID](fields["channel_ids"])
	if len(channels) == 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.AdminSetWorkspaceDefaultChannels(r.Context(), principal.WorkspaceID, principal.UserID, channels)
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": workspaceSettingsResponse(value)})
}

func workspaceSettingsResponse(value domain.Workspace) map[string]any {
	channels := make([]string, 0, len(value.DefaultChannelIDs))
	for _, channel := range value.DefaultChannelIDs {
		channels = append(channels, string(channel))
	}
	return map[string]any{"id": value.ID, "name": value.Name, "description": value.Description, "discoverability": value.Discoverability, "icon_url": value.IconURL, "default_channels": channels}
}

func (h Handler) adminTeamsCreate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	discoverability := domain.WorkspaceDiscoverability(strings.TrimSpace(fields["team_discoverability"]))
	value, err := h.Messages.AdminCreateWorkspace(r.Context(), principal.WorkspaceID, principal.UserID, fields["team_domain"], fields["team_name"], fields["team_description"], discoverability)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": value.ID})
}

func (h Handler) adminTeamsList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	value, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "teams": []map[string]any{{"id": value.ID, "name": value.Name}}})
}

func (h Handler) adminTeamsAdminsList(w http.ResponseWriter, r *http.Request) {
	h.adminTeamsRoleList(w, r, domain.WorkspaceRoleAdmin)
}
func (h Handler) adminTeamsOwnersList(w http.ResponseWriter, r *http.Request) {
	h.adminTeamsRoleList(w, r, domain.WorkspaceRoleOwner)
}
func (h Handler) adminTeamsRoleList(w http.ResponseWriter, r *http.Request, role domain.WorkspaceRole) {
	principal, err := h.authenticate(r, auth.ScopeAdminTeamsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	page, err := h.Messages.AdminTeamUsers(r.Context(), principal.WorkspaceID, principal.UserID, role, request)
	if err != nil {
		writeError(w, mapServiceError(err, "fatal_error"))
		return
	}
	ids := make([]string, 0, len(page.Users))
	for _, value := range page.Users {
		ids = append(ids, string(value.ID))
	}
	response := map[string]any{"ok": true, "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore}
	if role == domain.WorkspaceRoleAdmin {
		response["admin_ids"] = ids
	} else if role == domain.WorkspaceRoleOwner {
		response["owner_ids"] = ids
	} else {
		panic("unsupported workspace role")
	}
	writeJSON(w, http.StatusOK, response)
}

func parseCallUsers(raw string) []domain.UserID {
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		var participants []struct {
			SlackID    string `json:"slack_id"`
			ExternalID string `json:"external_id"`
		}
		if err := json.Unmarshal([]byte(raw), &participants); err != nil {
			return nil
		}
		result := make([]domain.UserID, 0, len(participants))
		for _, participant := range participants {
			id := strings.TrimSpace(participant.SlackID)
			if id == "" {
				id = strings.TrimSpace(participant.ExternalID)
			}
			if id != "" {
				result = append(result, domain.UserID(id))
			}
		}
		return result
	}
	parts := strings.Split(raw, ",")
	result := make([]domain.UserID, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, domain.UserID(value))
		}
	}
	return result
}

func callResponse(value domain.Call) map[string]any {
	users := make([]string, 0, len(value.Participants))
	for _, user := range value.Participants {
		users = append(users, string(user))
	}
	result := map[string]any{"id": value.ID, "external_unique_id": value.ExternalUniqueID, "external_display_id": value.ExternalDisplayID, "join_url": value.JoinURL, "desktop_app_join_url": value.DesktopAppJoinURL, "title": value.Title, "created_by": value.CreatedBy, "date_start": value.StartedAt.Unix(), "users": users}
	if !value.EndedAt.IsZero() {
		result["date_end"] = value.EndedAt.Unix()
		result["duration"] = value.DurationSeconds
	}
	return result
}

func (h Handler) addCall(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCallsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	started := time.Time{}
	if raw := strings.TrimSpace(fields["date_start"]); raw != "" {
		seconds, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || seconds <= 0 {
			writeError(w, "invalid_arg_name")
			return
		}
		started = time.Unix(seconds, 0).UTC()
	}
	value, err := h.Messages.AddCall(r.Context(), principal.WorkspaceID, principal.UserID, fields["external_unique_id"], fields["external_display_id"], fields["join_url"], fields["desktop_app_join_url"], fields["title"], started, parseCallUsers(fields["users"]))
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": callResponse(value)})
}
func (h Handler) endCall(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCallsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	duration := int64(0)
	if strings.TrimSpace(fields["duration"]) != "" {
		duration, err = strconv.ParseInt(strings.TrimSpace(fields["duration"]), 10, 64)
	}
	if err != nil || strings.TrimSpace(fields["id"]) == "" || duration < 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.EndCall(r.Context(), principal.WorkspaceID, principal.UserID, domain.CallID(strings.TrimSpace(fields["id"])), duration); err != nil {
		writeError(w, mapServiceError(err, "call_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (h Handler) callInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCallsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.GetCall(r.Context(), principal.WorkspaceID, principal.UserID, domain.CallID(strings.TrimSpace(fields["id"])))
	if err != nil {
		writeError(w, mapServiceError(err, "call_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": callResponse(value)})
}
func (h Handler) updateCall(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCallsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	id := domain.CallID(strings.TrimSpace(fields["id"]))
	if id == "" {
		// endCall, callInfo and calls.participants.* all require a non-empty id;
		// calls.update forwarded "".
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.UpdateCall(r.Context(), principal.WorkspaceID, principal.UserID, id, fields["title"], fields["join_url"], fields["desktop_app_join_url"])
	if err != nil {
		writeError(w, mapServiceError(err, "call_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "call": callResponse(value)})
}
func (h Handler) addCallParticipants(w http.ResponseWriter, r *http.Request) {
	h.changeCallParticipantsHTTP(w, r, true)
}
func (h Handler) removeCallParticipants(w http.ResponseWriter, r *http.Request) {
	h.changeCallParticipantsHTTP(w, r, false)
}
func (h Handler) changeCallParticipantsHTTP(w http.ResponseWriter, r *http.Request, add bool) {
	principal, err := h.authenticate(r, auth.ScopeCallsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["id"]) == "" || strings.TrimSpace(fields["users"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	id := domain.CallID(strings.TrimSpace(fields["id"]))
	users := parseCallUsers(fields["users"])
	if add {
		err = h.Messages.AddCallParticipants(r.Context(), principal.WorkspaceID, principal.UserID, id, users)
	} else {
		err = h.Messages.RemoveCallParticipants(r.Context(), principal.WorkspaceID, principal.UserID, id, users)
	}
	if err != nil {
		writeError(w, mapServiceError(err, "call_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) getPermalink(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	timestamp := domain.MessageTimestamp(strings.TrimSpace(fields["message_ts"]))
	if channel == "" || timestamp == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	permalink, err := h.Messages.Permalink(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp)
	if err != nil {
		writeError(w, mapServiceError(err, "message_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": channel, "permalink": permalink})
}

func messageResponse(message domain.Message) map[string]any {
	result := map[string]any{"type": "message", "user": message.AuthorID, "text": message.Text, "ts": slackTimestamp(message.CreatedAt)}
	// Slack's message object reports an edit through `edited`, and clients
	// render "(edited)" from it. It was absent from every method that returns
	// a message, so no caller could tell an edited message from an untouched
	// one. The durable subtype travels for the same reason; the subtypes
	// derived from other state (file_share below, thread_broadcast from the
	// stream projection) keep overriding it, because they are computed facts
	// about this same message.
	if !message.EditedAt.IsZero() {
		result["edited"] = map[string]any{"user": string(message.EditedBy), "ts": slackTimestamp(message.EditedAt)}
	}
	if message.Subtype != "" {
		result["subtype"] = string(message.Subtype)
	}
	if len(message.Files) > 0 {
		files := make([]map[string]any, 0, len(message.Files))
		for _, file := range message.Files {
			files = append(files, fileResponse(file))
		}
		result["subtype"] = "file_share"
		result["upload"] = true
		result["files"] = files
	}
	if message.AppID != "" {
		result["app_id"] = message.AppID
	}
	var stream domain.MessageStreamState
	if json.Unmarshal([]byte(message.StreamState), &stream) == nil {
		if stream.BotID != "" {
			result["bot_id"] = stream.BotID
		}
		if stream.Username != "" {
			result["username"] = stream.Username
		}
		if stream.IconEmoji != "" {
			result["icons"] = map[string]string{"emoji": stream.IconEmoji}
		} else if stream.IconURL != "" {
			result["icons"] = map[string]string{"image_48": stream.IconURL}
		}
		if stream.ReplyBroadcast {
			result["subtype"] = "thread_broadcast"
			result["reply_broadcast"] = true
		}
	}
	// `thread_ts` used to be emitted unconditionally, so a non-threaded message
	// serialised as `"thread_ts": ""`, which the strictly typed SDK models (Java
	// Message.threadTs, the Deno typed responses) parse as a timestamp.
	if message.ThreadTimestamp != "" {
		result["thread_ts"] = message.ThreadTimestamp
	}
	if message.Blocks != "" {
		result["blocks"] = json.RawMessage(message.Blocks)
	}
	if len(message.Unfurls) > 0 {
		unfurls := make(map[string]json.RawMessage, len(message.Unfurls))
		for key, raw := range message.Unfurls {
			unfurls[key] = json.RawMessage(raw)
		}
		result["unfurls"] = unfurls
	}
	if message.Attachments != "" && message.Attachments != "[]" {
		result["attachments"] = json.RawMessage(message.Attachments)
	}
	if message.Metadata != "" {
		result["metadata"] = json.RawMessage(message.Metadata)
	}
	return result
}

// mapServiceError names a handled service failure with an error code the
// operation's pinned `default` enum declares. Every Slack Web API failure is a
// handled failure, so the caller pairs this with writeError, which answers HTTP
// 200; the status code is deliberately not part of the result so that a handled
// error can never be reported as a transport failure.
//
// notFound names the missing referent (`channel_not_found`, `user_not_found`,
// …). Callers that need a specific argument-rejection code use
// mapServiceErrorNamed; the default is `invalid_arg_name`, which the pinned
// snapshot declares for every operation that declares an error enum at all
// except admin.conversations.* and reactions.get.
// mapAdminError names a failure from an admin.* method whose pinned enum declares
// `not_an_admin`: admin.conversations.delete, .disconnectShared,
// .getConversationPrefs, .setConversationPrefs and .search. An authorization
// failure on those operations is a role failure, and reporting it as the generic
// `no_permission` hides which grant is missing.
//
// Two failures reach it: service.ErrNotWorkspaceAdmin, the role denial every
// admin.* method raises for an actor whose durable membership is not an
// administrator or owner, and service.ErrMessageNotOwned, an ownership denial.
// Both land in the permission branch of mapServiceErrorNamed below.
func mapAdminError(err error, notFoundReason string) string {
	if reason := mapServiceError(err, notFoundReason); reason != "no_permission" {
		return reason
	}
	return "not_an_admin"
}

func mapServiceError(err error, notFoundReason string) string {
	return mapServiceErrorNamed(err, notFoundReason, "invalid_arg_name", "")
}

// mapServiceErrorExists names a failure for an operation whose own pinned enum
// declares a collision code. A collision code is always operation-specific —
// `already_reacted`, `already_pinned`, `already_starred` and `name_taken` each
// appear in one to five of the 99 pinned enums, never in all of them — so the
// shared mapper cannot guess one, and guessing `already_reacted` for every
// caller reported a reaction failure from pins.add, bookmarks, usergroups, emoji,
// invite requests, OAuth clients and external identities alike.
func mapServiceErrorExists(err error, notFoundReason, existsReason string) string {
	return mapServiceErrorNamed(err, notFoundReason, "invalid_arg_name", existsReason)
}

// mapServiceErrorNamed names a failure from the chat service.
//
// Classification is by domain sentinel only. It used to also test the gRPC
// status code, which was a workaround for a transport that dropped sentinels on
// the wire; internal/modules/chat/transport/grpc now carries the sentinel key in
// a status detail and restores it exactly, so errors.Is is true in both
// compositions. The status-code tests were not merely redundant — a code is
// coarser than a sentinel, so they misclassified:
//
//   - codes.AlreadyExists is store.ErrAlreadyExists as well as
//     service.ErrEmojiAlreadyExists, so a duplicate reaction was reported as
//     `emoji_already_exists`, a code no pinned operation declares for
//     reactions.add, instead of the `already_reacted` its enum does declare;
//   - codes.Aborted is store.ErrConflict, store.ErrLeaseConflict and
//     store.ErrIdempotencyConflict, so the code test shadowed the
//     ErrIdempotencyConflict branch below and answered `hash_conflict` where the
//     idempotency contract requires `rate_limited`.
func mapServiceErrorNamed(err error, notFoundReason, invalidReason, existsReason string) string {
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, service.ErrSlashCommandNotFound) || errors.Is(err, service.ErrWebhookTriggerSecret) {
		return notFoundReason
	}
	if errors.Is(err, store.ErrScheduledMessageLimit) || errors.Is(err, service.ErrScheduledTooMany) || errors.Is(err, store.ErrScheduledStatusLimit) || errors.Is(err, service.ErrScheduledStatusLimit) {
		return "restricted_too_many"
	}
	if errors.Is(err, service.ErrInvalidMessage) || errors.Is(err, service.ErrInvalidTimestamp) || errors.Is(err, service.ErrInvalidConversation) || errors.Is(err, service.ErrInvalidReaction) || errors.Is(err, service.ErrInvalidFile) || errors.Is(err, service.ErrInvalidProfile) || errors.Is(err, service.ErrInvalidScheduledStatus) || errors.Is(err, service.ErrInvalidSnooze) || errors.Is(err, service.ErrInvalidCall) || errors.Is(err, service.ErrInvalidUserGroup) || errors.Is(err, service.ErrInvalidEphemeral) || errors.Is(err, service.ErrInvalidEmoji) || errors.Is(err, service.ErrInvalidView) || errors.Is(err, service.ErrInvalidDialog) || errors.Is(err, service.ErrInvalidBot) || errors.Is(err, service.ErrInvalidConversationPrefs) || errors.Is(err, service.ErrInvalidRemoteFile) || errors.Is(err, service.ErrInvalidInviteRequest) || errors.Is(err, service.ErrInvalidAppApproval) || errors.Is(err, service.ErrInvalidIntegrationLogs) || errors.Is(err, service.ErrInvalidOAuth) || errors.Is(err, service.ErrInvalidOAuthClient) || errors.Is(err, service.ErrInvalidBookmark) || errors.Is(err, store.ErrInvalidConversationType) || errors.Is(err, store.ErrInvalidAppApproval) || errors.Is(err, service.ErrInvalidCanvas) || errors.Is(err, service.ErrInvalidList) || errors.Is(err, service.ErrInvalidEntity) || errors.Is(err, service.ErrInvalidExternalUpload) || errors.Is(err, store.ErrInvalidArgument) || errors.Is(err, service.ErrInvalidAccessLog) || errors.Is(err, service.ErrInvalidMigration) || errors.Is(err, service.ErrInvalidReminder) || errors.Is(err, service.ErrInvalidLaterReminder) || errors.Is(err, service.ErrReminderTimeInPast) || errors.Is(err, service.ErrInvalidSearch) || errors.Is(err, service.ErrInvalidWorkflowStep) || errors.Is(err, service.ErrInvalidTriggerConfig) || errors.Is(err, service.ErrInvalidWorkspace) || errors.Is(err, service.ErrInvalidAppResponse) || errors.Is(err, service.ErrInvalidTrigger) || errors.Is(err, service.ErrSlashCommandInThread) {
		return invalidReason
	}
	if errors.Is(err, service.ErrAppInteractionUnavailable) {
		return "fatal_error"
	}
	if errors.Is(err, service.ErrEmojiAlreadyExists) {
		return "emoji_already_exists"
	}
	// service.ErrNotWorkspaceAdmin is the role denial raised by every admin.*
	// method. It belongs in the permission branch so mapAdminError can name it
	// `not_an_admin` on the five operations whose pinned enum declares that code,
	// while every other operation reports `no_permission`, which 66 pinned enums
	// declare and which is the closest code those operations do declare.
	if errors.Is(err, service.ErrMessageNotOwned) || errors.Is(err, service.ErrNotWorkspaceAdmin) {
		return "no_permission"
	}
	// A refusal to leave the workspace ownerless is not a permission failure —
	// the actor holds the authority — so it must not be reported as one, or an
	// administrator is told they lack a right they actually have.
	if errors.Is(err, service.ErrLastWorkspaceOwner) {
		return "cant_delete_primary_owner"
	}
	if errors.Is(err, service.ErrMessageAlreadyDeleted) {
		return "message_not_found"
	}
	if errors.Is(err, service.ErrInvalidPresence) {
		return "invalid_presence"
	}
	if errors.Is(err, service.ErrBlobUnavailable) {
		return "file_storage_unavailable"
	}
	// A cursor that reaches the store unvalidated is still a client argument, not
	// a server fault. Every route validates its own cursor now; this keeps a
	// future one from falling through to `fatal_error`.
	if errors.Is(err, domain.ErrInvalidCursor) {
		return invalidReason
	}
	// A membership requirement the pinned contract states: 9 enums declare
	// not_in_channel, and it was reachable from no route.
	if errors.Is(err, service.ErrNotInConversation) {
		return "not_in_channel"
	}
	if errors.Is(err, service.ErrCannotInviteSelf) {
		return "cant_invite_self"
	}
	if errors.Is(err, store.ErrAlreadyExists) {
		if existsReason != "" {
			return existsReason
		}
		// The caller named no collision code, so this operation's enum declares
		// none. Reporting the collision as a rejected argument is the closest
		// code such an operation does declare; naming another operation's
		// collision code would not be.
		return invalidReason
	}
	if errors.Is(err, store.ErrConflict) {
		return "hash_conflict"
	}
	if errors.Is(err, store.ErrBookmarkLimit) {
		return "too_many_bookmarks"
	}
	if errors.Is(err, store.ErrInvalidInviteRequest) {
		return invalidReason
	}
	// An Idempotency-Key replayed with a different body is a permanently
	// unsatisfiable request: the recorded body will never match. It used to be
	// reported as `rate_limited`, which is the one Slack code whose handling is
	// defined by the HTTP layer rather than the body — python-slack-sdk's
	// RateLimitErrorRetryHandler, node-slack-sdk's rateLimitedErrorRetryHandler
	// and the Java SDK all key on status 429 and Retry-After. Emitted at 200 with
	// no header it told every SDK either to retry forever or to treat a caller
	// mistake as an opaque failure. `rate_limited` is reserved for a real limiter
	// answering 429 with Retry-After; the conflict is named as what it is, a
	// request argument that contradicts a previous one.
	if errors.Is(err, store.ErrIdempotencyConflict) {
		return "invalid_arg_name"
	}
	// apps.connections.open is absent from the pinned snapshot; the Socket Mode
	// connection limit reuses the recorded Socket Mode deviation code.
	if errors.Is(err, store.ErrSocketModeConnectionLimit) {
		return "socket_mode_unavailable"
	}
	// Both of these are storage-engine outcomes the layer below is expected to
	// absorb: the service retries a taken message microsecond on the next one,
	// and the SQL repositories retry a serialization failure, a deadlock victim
	// or a lost leader under their contention loop. Reaching the transport means
	// the retry was exhausted or the path has none, so the request was well formed
	// and the server did not complete it. `internal_error` is the pinned name for
	// exactly that, and keeping it distinct from `fatal_error` is the point of
	// these sentinels: an operator reading the wire can tell a classified engine
	// failure from an outcome nothing has classified. `rate_limited` is not used
	// here — it is the one Slack code whose handling is defined by HTTP 429 and
	// Retry-After, and this transport answers 200.
	if errors.Is(err, store.ErrMessageTimestampTaken) || errors.Is(err, store.ErrTransient) {
		return "internal_error"
	}
	if errors.Is(err, service.ErrAppCredentialKeyUnavailable) {
		return "fatal_error"
	}
	return "fatal_error"
}

// parseBoolFields reads several optional booleans through the one boolean parser
// in this package. It replaces parseOptionalBooleans, which hard-coded an arity
// of exactly three and re-implemented parseBoolField with a different accepted
// set.
func parseBoolFields(fields map[string]string, names ...string) ([]bool, error) {
	values := make([]bool, len(names))
	for index, name := range names {
		value, err := parseBoolField(fields[name])
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return values, nil
}

const maxRequestBody = 4 << 20

type decodedProfile struct {
	Strings          map[string]string
	StatusExpiration *int64
}

func decodeProfileJSON(raw string) (decodedProfile, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return decodedProfile{}, errors.New("profile must be a JSON object")
	}
	values := make(map[string]string, len(fields))
	result := decodedProfile{Strings: values}
	acknowledged := 0
	for name, value := range fields {
		switch name {
		case "display_name", "status_text", "status_emoji", "image_24", "image_32", "image_48", "image_72", "image_192", "image_512", "image_1024":
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return decodedProfile{}, fmt.Errorf("profile field %s must be a string", name)
			}
			values[name] = text
		case "status_expiration":
			var expiration int64
			if err := json.Unmarshal(value, &expiration); err != nil || expiration < 0 {
				return decodedProfile{}, errors.New("profile field status_expiration must be a non-negative integer")
			}
			result.StatusExpiration = &expiration
		case "always_active", "is_custom_image":
			// Parsed for validation only: neither is settable through this API. They
			// used to be dropped from `values`, so `profile={"always_active":true}`
			// fell through to "must contain at least one supported field" and the
			// request was rejected as if the field were unknown.
			var boolean bool
			if err := json.Unmarshal(value, &boolean); err != nil {
				return decodedProfile{}, fmt.Errorf("profile field %s must be a boolean", name)
			}
			acknowledged++
		default:
			return decodedProfile{}, fmt.Errorf("unsupported profile field %s", name)
		}
	}
	if len(values) == 0 && result.StatusExpiration == nil && acknowledged == 0 {
		return decodedProfile{}, errors.New("profile must contain at least one supported field")
	}
	return result, nil
}

// decodeError carries the Slack error code the pinned contract declares for a
// request-decoding failure. Before this existed every caller either wrote the
// blanket `invalid_form_data` or — in eighteen places — returned without writing
// a body at all, which answered HTTP 200 with zero bytes and left an SDK unable
// to tell success from failure.
type decodeError struct {
	code   string
	detail string
}

func (e decodeError) Error() string { return e.detail }

func decodeFailure(code, detail string) error { return decodeError{code: code, detail: detail} }

// decodeErrorCode names a decode failure. `invalid_form_data` is the fallback
// because the pinned snapshot declares it for every operation that declares an
// error enum at all.
func decodeErrorCode(err error) string {
	var typed decodeError
	if errors.As(err, &typed) {
		return typed.code
	}
	var syntax *json.SyntaxError
	var unmarshal *json.UnmarshalTypeError
	if errors.As(err, &syntax) || errors.As(err, &unmarshal) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "invalid_json"
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return "request_timeout"
	}
	return "invalid_form_data"
}

// writeDecodeError is the single exit for a request that could not be decoded.
func writeDecodeError(w http.ResponseWriter, err error) {
	writeError(w, decodeErrorCode(err))
}

// requestCharset rejects a charset the decoder cannot honour. `invalid_charset`
// is declared by 87 of the 174 pinned operations and was never emitted.
func requestCharset(header string) error {
	for _, parameter := range strings.Split(header, ";")[1:] {
		name, value, found := strings.Cut(parameter, "=")
		if !found || strings.ToLower(strings.TrimSpace(name)) != "charset" {
			continue
		}
		switch strings.ToLower(strings.Trim(strings.TrimSpace(value), `"`)) {
		case "", "utf-8", "utf8", "us-ascii", "ascii":
		default:
			return decodeFailure("invalid_charset", "unsupported charset "+value)
		}
	}
	return nil
}

// decodeFields reads one request's arguments from the URL query and the body.
//
// The query is read for every encoding, and the body overrides it. Both the JSON
// and the multipart branches used to return before r.ParseForm ever ran, and
// r.MultipartForm.Value deliberately excludes the URL query, so those two
// encodings dropped every query-string argument: `POST /api/chat.postMessage
// ?channel=C1` with a JSON body carrying only `text` was answered `no_text`,
// naming an argument that was present because the one that was missing had been
// discarded. Splitting arguments between the query and the payload is legal and
// common — the pinned snapshot places `token` `in: query` for over a hundred
// operations and `in: formData` for files.upload and users.setPhoto — so the four
// encodings have to agree.
//
// The form branch reads r.PostForm rather than r.Form for the same reason: r.Form
// merges the query into the body, so the identical argument in both places was
// reported as a conflicting duplicate rather than resolved by the same precedence
// every other encoding uses.
func decodeFields(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	fields := make(map[string]string)
	if err := collectFormValues(fields, r.URL.Query()); err != nil {
		return nil, err
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	header := r.Header.Get("Content-Type")
	if err := requestCharset(header); err != nil {
		return nil, err
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(header, ";", 2)[0]))
	if contentType == "application/json" {
		body, err := decodeJSONFields(r.Body)
		if err != nil {
			// Every raw encoding/json failure is a malformed document; `invalid_json` is
			// the code the pinned enums declare for it. Returning the bare error left the
			// blanket `invalid_form_data` pointing the caller at the wrong encoding.
			var typed decodeError
			if errors.As(err, &typed) {
				return nil, err
			}
			return nil, decodeFailure("invalid_json", err.Error())
		}
		for name, value := range body {
			fields[name] = value
		}
		return fields, nil
	}
	if contentType == "multipart/form-data" {
		if err := r.ParseMultipartForm(maxRequestBody); err != nil {
			return nil, err
		}
		if r.MultipartForm == nil {
			return fields, nil
		}
		if err := collectFormValues(fields, r.MultipartForm.Value); err != nil {
			return nil, err
		}
		return fields, nil
	}
	// Go's parsePostForm treats an absent or unrecognised Content-Type as
	// application/octet-stream and reads nothing without reporting an error, so a
	// POST body would silently decode as no parameters at all. The pinned enums
	// reserve `missing_post_type` and `invalid_post_type` for exactly this. A
	// POST that carries no payload at all is legitimate (a bearer token in the
	// header is enough for many methods) and must not be rejected, so the check
	// only fires once a byte of body exists.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		body := bufio.NewReader(r.Body)
		r.Body = readCloser{Reader: body, Closer: r.Body}
		if _, err := body.Peek(1); err == nil {
			if contentType == "" {
				return nil, decodeFailure("missing_post_type", "a POST payload must declare a Content-Type")
			}
			if contentType != "application/x-www-form-urlencoded" {
				return nil, decodeFailure("invalid_post_type", "unsupported Content-Type "+contentType)
			}
		}
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	if err := collectFormValues(fields, r.PostForm); err != nil {
		return nil, err
	}
	return fields, nil
}

// readCloser rebuilds an io.ReadCloser after the body has been wrapped for a
// lookahead, so ParseForm still reads every byte and Close still reaches the
// original body.
type readCloser struct {
	io.Reader
	io.Closer
}

func decodeJSONFields(body io.Reader) (map[string]string, error) {
	fields := make(map[string]string)
	decoder := json.NewDecoder(io.LimitReader(body, maxRequestBody))
	start, err := decoder.Token()
	if err == io.EOF {
		return fields, nil
	}
	if err != nil {
		return nil, err
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return nil, decodeFailure("json_not_object", "JSON request must be an object")
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := key.(string)
		if !ok {
			return nil, decodeFailure("invalid_json", "JSON object field name is invalid")
		}
		if _, exists := seen[name]; exists {
			return nil, decodeFailure("invalid_json", "request contains duplicate JSON field")
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[name], err = normalizeJSONField(name, value)
		if err != nil {
			return nil, err
		}
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return nil, decodeFailure("invalid_json", "JSON request object is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, decodeFailure("invalid_json", "request contains multiple JSON values")
		}
		return nil, err
	}
	return fields, nil
}

func collectFormValues(fields map[string]string, source map[string][]string) error {
	for name, values := range source {
		if len(values) == 0 {
			return decodeFailure("invalid_form_data", "form fields must occur once")
		}
		for _, value := range values[1:] {
			if value != values[0] {
				return decodeFailure("invalid_form_data", "form fields must not contain conflicting values")
			}
		}
		value, err := normalizeListFieldValue(name, values[0])
		if err != nil {
			return err
		}
		fields[name] = value
	}
	return nil
}

func normalizeJSONScalar(value json.RawMessage) (string, error) {
	// A JSON null is not a value. json.Unmarshal writes nothing into a string for
	// it and reports no error, so `{"error":null}` used to decode to the empty
	// string and read as "the argument was not sent" — which silently discards an
	// argument /workflows.stepFailed declares required, and answers ok:true on
	// api.test for a request that named an error.
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", decodeFailure("invalid_arg_name", "a JSON null is not an argument value")
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text, nil
	}
	var scalar any
	if err := json.Unmarshal(value, &scalar); err != nil {
		return "", decodeFailure("invalid_arg_name", "request fields must be scalar values")
	}
	switch scalar := scalar.(type) {
	case bool:
		return strconv.FormatBool(scalar), nil
	case float64:
		return strconv.FormatFloat(scalar, 'f', -1, 64), nil
	case []any:
		// 83 of the 99 pinned enums declare invalid_array_arg for precisely an
		// array supplied where a scalar argument belongs.
		return "", decodeFailure("invalid_array_arg", "request field must not be an array")
	default:
		return "", decodeFailure("invalid_arg_name", "request fields must be scalar values")
	}
}

// isStructuredField names the arguments whose JSON value is forwarded verbatim
// rather than flattened to a scalar. This used to be two hand-maintained lists,
// one negated and one positive, differing only by "profile" — so adding a name to
// one and not the other silently changed how a value decoded.
//
// `error` is absent because it has no single shape. Exactly two operations in the
// pinned snapshot take an `error` argument: /api.test declares it `type: string`,
// and /workflows.stepFailed declares it `type: string`, `required: true`, with a
// description that reads "A JSON-based object with a `message` property". So the
// same argument name carries a bare string on one operation and a JSON object on
// the other, and the name alone cannot decide. Treating it as structured JSON
// made `{"error":"my_error"}` echo back the quoted `"\"my_error\""` while the
// equivalent form-encoded request echoed `my_error`; flattening it always broke
// workflows.stepFailed for every official SDK. It is decided by the value's own
// shape instead — see normalizeJSONField.
func isStructuredField(name string) bool {
	switch name {
	case "blocks", "attachments", "chunks", "files", "unfurls", "metadata", "message", "user_auth_blocks", "view", "outputs", "inputs", "dialog", "prefs", "document_content", "changes", "criteria", "description_blocks", "schema", "initial_fields", "cells", "comments", "comment", "item", "items", "expression_attributes", "expression_values":
		return true
	default:
		return false
	}
}

func normalizeJSONField(name string, value json.RawMessage) (string, error) {
	switch {
	case isListField(name):
		return normalizeJSONListField(value)
	case isStructuredField(name):
		var structured any
		if err := json.Unmarshal(value, &structured); err != nil || structured == nil {
			return "", decodeFailure("invalid_json", name+" must be structured JSON")
		}
	case name == "profile":
		var profile map[string]json.RawMessage
		if err := json.Unmarshal(value, &profile); err != nil || profile == nil {
			return "", decodeFailure("json_not_object", "profile must be a JSON object")
		}
	case name == "error" && jsonIsObject(value):
		// An object is forwarded verbatim, which is what workflows.stepFailed
		// needs; anything else is flattened, which keeps the form-encoded and
		// JSON-encoded forms of the scalar case identical.
	default:
		return normalizeJSONScalar(value)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return "", decodeFailure("invalid_json", "field is not valid JSON")
	}
	return compact.String(), nil
}

// jsonIsObject reports whether a raw JSON value is an object, the one shape that
// cannot survive being flattened into a form value and that a declared `error`
// argument may legitimately carry.
//
// It used to accept an array as well, so `{"error":[1]}` was forwarded verbatim
// and echoed back as the error code `[1]`. Both operations declare `error` as
// `type: string`, and 83 of the 99 pinned enums declare invalid_array_arg for
// exactly the case of an array where a scalar belongs.
func jsonIsObject(value json.RawMessage) bool {
	for _, b := range value {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

func isListField(name string) bool {
	switch name {
	case "channel_ids", "leaving_team_ids", "target_team_ids", "team_ids", "user_ids", "ids":
		return true
	default:
		return false
	}
}

func normalizeListFieldValue(name string, value string) (string, error) {
	if !isListField(name) {
		return value, nil
	}
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || (trimmed[0] != '[' && trimmed[0] != '"') {
		return value, nil
	}
	return normalizeJSONListField(json.RawMessage(trimmed))
}

func normalizeJSONListField(value json.RawMessage) (string, error) {
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return "", decodeFailure("invalid_array_arg", "list fields must be strings or arrays of strings")
	}
	var values []string
	if err := json.Unmarshal(value, &values); err == nil {
		for index, item := range values {
			values[index] = strings.TrimSpace(item)
			if values[index] == "" {
				return "", decodeFailure("invalid_array_arg", "list fields must contain non-empty strings")
			}
		}
		return strings.Join(values, ","), nil
	}
	var scalar string
	if err := json.Unmarshal(value, &scalar); err != nil {
		return "", decodeFailure("invalid_array_arg", "list fields must be strings or arrays of strings")
	}
	return scalar, nil
}

// parseIDList reads a comma-separated list or a JSON array of strings into a
// typed ID slice. It replaces five near-identical splitters, only two of which
// understood the JSON-array form — which is why slackLists.access.set accepted
// `channel_ids=["C1"]` while canvases.access.set silently produced one bogus id.
func parseIDList[T ~string](raw string) []T {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "[") {
		var values []string
		if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
			return nil
		}
		result := make([]T, 0, len(values))
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				result = append(result, T(value))
			}
		}
		return result
	}
	parts := strings.Split(raw, ",")
	result := make([]T, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, T(value))
		}
	}
	return result
}

func slackTimestamp(value time.Time) string {
	return fmt.Sprintf("%d.%06d", value.Unix(), value.Nanosecond()/1000)
}

// missingScopeError carries the scope the operation requires and the scopes the
// token actually holds. The pinned `default` response schema declares `needed`
// and `provided` next to `error` precisely so a client can repair the grant, so
// dropping them would leave `missing_scope` unactionable.
type missingScopeError struct {
	needed   auth.Scope
	provided []string
}

func (e missingScopeError) Error() string {
	return fmt.Sprintf("missing scope %s", e.needed)
}

func (e missingScopeError) Unwrap() error { return auth.ErrMissingScope }

// accessLogLimits mirror the durable column bounds enforced by
// service.Messages.RecordAccess. A client controls both values through request
// headers, so the handler truncates rather than letting a long header turn an
// authenticated read into a dependency failure.
const (
	maxAccessLogIP        = 128
	maxAccessLogUserAgent = 1024
)

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func (h Handler) authenticate(r *http.Request, scope auth.Scope) (auth.Principal, error) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		return auth.Principal{}, err
	}
	if scope != "" && !principal.HasScope(scope) {
		return auth.Principal{}, missingScopeError{needed: scope, provided: permissionScopes(principal)}
	}
	if err := h.Messages.RecordAccess(r.Context(), principal.WorkspaceID, principal.UserID, truncate(r.RemoteAddr, maxAccessLogIP), truncate(r.UserAgent(), maxAccessLogUserAgent)); err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %v", errAccessLogging, err)
	}
	return principal, nil
}

func (h Handler) authenticateApp(r *http.Request, scope auth.Scope) (auth.Principal, error) {
	if h.SocketAuth == nil {
		return auth.Principal{}, auth.ErrInvalidToken
	}
	principal, err := h.SocketAuth.Authenticate(r)
	if err != nil {
		return auth.Principal{}, err
	}
	if principal.AppID == "" {
		return auth.Principal{}, auth.ErrInvalidToken
	}
	if scope != "" && !principal.HasScope(scope) {
		return auth.Principal{}, missingScopeError{needed: scope, provided: permissionScopes(principal)}
	}
	return principal, nil
}

func writeAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, errAccessLogging) {
		writeError(w, "fatal_error")
		return
	}
	// A credential store that did not answer is server trouble, not an
	// authentication outcome. Answering `invalid_auth` here made official
	// clients discard their token and re-authenticate during every store
	// outage; `fatal_error` is the vocabulary's server-side failure and tells
	// them to retry with the credential they already hold.
	if errors.Is(err, auth.ErrCredentialStoreUnavailable) {
		writeError(w, "fatal_error")
		return
	}
	var missing missingScopeError
	if errors.As(err, &missing) {
		body := map[string]any{"ok": false, "error": "missing_scope", "needed": string(missing.needed)}
		body["provided"] = strings.Join(missing.provided, ",")
		writeJSON(w, http.StatusOK, body)
		return
	}
	// Authentication outcomes are distinct members of Slack's error vocabulary:
	// `not_authed` (87 operations), `invalid_auth` (88),
	// `token_revoked` (52), `token_expired`, and `account_inactive` (86) — so collapsing them onto
	// `not_authed` told a client holding a stale or withdrawn token that it had
	// sent no credential at all. The specific sentinels are tested before the
	// class they wrap.
	//
	// Caveat recorded rather than hidden: the snapshot's enums are per-operation
	// and `token_revoked` is absent from 122 of them, so a revoked token on such
	// an operation now answers a code that operation does not enumerate. The
	// snapshot is demonstrably incomplete here (`account_inactive` and
	// `invalid_auth` appear on operations that omit `token_revoked` even though
	// all three describe the same credential check), and naming the real cause is
	// worth more to a caller than a code that is in the enum but wrong.
	switch {
	case errors.Is(err, auth.ErrTokenRevoked):
		writeError(w, "token_revoked")
	case errors.Is(err, auth.ErrTokenExpired):
		writeError(w, "token_expired")
	case errors.Is(err, auth.ErrAccountInactive):
		writeError(w, "account_inactive")
	case errors.Is(err, auth.ErrInvalidToken):
		writeError(w, "invalid_auth")
	default:
		writeError(w, "not_authed")
	}
}

// writeError answers a handled failure. Every Slack Web API failure is signalled
// with HTTP 200 and `{"ok":false,"error":…}`: the pinned contract declares only
// a `200` and a `default` response whose recorded examples are plain envelopes,
// and every official SDK keys its retry and rate-limit logic off the status
// code, so reporting a handled rejection as 4xx/5xx makes clients retry a
// request that can never succeed. AGENTS.md forbids the same thing from the
// other direction.
func writeError(w http.ResponseWriter, reason string) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": reason})
}

// writeJSON answers every one of this transport's routes.
//
// The charset is explicit because encoding/json emits UTF-8 and a client that
// guesses reads a multi-byte display name wrong. nosniff is here because nothing
// on this surface carried it: /api.test reflects a caller-supplied `error` value
// straight back into the body, so the whole surface was one Content-Type mistake
// away from a document rendered on this origin. It is one header on one helper,
// and it removes the class rather than the instance.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h Handler) createList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	includeCopied, err := parseBoolField(fields["include_copied_list_records"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	todoMode, err := parseBoolField(fields["todo_mode"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.CreateList(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], fields["description_blocks"], fields["schema"], domain.ListID(strings.TrimSpace(fields["copy_from_list_id"])), includeCopied, todoMode)
	if err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "list": listResponse(value)})
}

func (h Handler) updateList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	todoMode, err := parseBoolField(fields["todo_mode"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.UpdateList(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["id"])), fields["name"], fields["description_blocks"], todoMode, strings.TrimSpace(fields["todo_mode"]) != "")
	if err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "list": listResponse(value)})
}

func (h Handler) createListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["list_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.CreateListItem(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), domain.ListItemID(strings.TrimSpace(fields["parent_item_id"])), fields["initial_fields"])
	if err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": listItemResponse(value)})
}

func (h Handler) listItemInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["list_id"]) == "" || strings.TrimSpace(fields["id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.GetListItem(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), domain.ListItemID(strings.TrimSpace(fields["id"])))
	if err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": listItemResponse(value)})
}

func (h Handler) listItems(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["list_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	// slackLists.items.list used to be the one list decoder that skipped cursor
	// validation entirely (a tampered cursor reached the store and was reported as
	// list_not_found) and the one clamping `limit` to 1000 where every sibling
	// clamps to 200. It reads the shared decoder now, and pageRequest is gone.
	request, err := decodeListRequestFields(fields, "invalid_arg_name")
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	archived, err := parseBoolField(fields["archived"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	page, err := h.Messages.ListItems(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), request, archived)
	if err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, value := range page.Items {
		items = append(items, listItemResponse(value))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items, "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore})
}

func (h Handler) updateListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["list_id"]) == "" || strings.TrimSpace(fields["cells"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	values, err := h.Messages.UpdateListCells(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), fields["cells"])
	if err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		items = append(items, listItemResponse(value))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

func (h Handler) deleteListItem(w http.ResponseWriter, r *http.Request) {
	h.deleteListItemsWithScope(w, r, auth.ScopeListsWrite, false)
}

func (h Handler) deleteListItems(w http.ResponseWriter, r *http.Request) {
	h.deleteListItemsWithScope(w, r, auth.ScopeListsWrite, true)
}

func (h Handler) deleteListItemsWithScope(w http.ResponseWriter, r *http.Request, scope auth.Scope, multiple bool) {
	principal, err := h.authenticate(r, scope)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["list_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	// slackLists.items.delete takes one id and slackLists.items.deleteMultiple takes
	// a list. The single form used to be split on commas as well, so `id=R1,R2`
	// deleted two rows through the single-delete method, and the split of `id` was
	// pure waste on the deleteMultiple path.
	var ids []domain.ListItemID
	if multiple {
		ids = parseIDList[domain.ListItemID](fields["ids"])
	} else if id := strings.TrimSpace(fields["id"]); id != "" {
		if strings.Contains(id, ",") {
			writeError(w, "invalid_arg_name")
			return
		}
		ids = []domain.ListItemID{domain.ListItemID(id)}
	}
	if len(ids) == 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.DeleteListItems(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), ids); err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) setListAccess(w http.ResponseWriter, r *http.Request) {
	h.changeListAccess(w, r, true)
}

func (h Handler) deleteListAccess(w http.ResponseWriter, r *http.Request) {
	h.changeListAccess(w, r, false)
}

func (h Handler) changeListAccess(w http.ResponseWriter, r *http.Request, set bool) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["list_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	channels := parseIDList[domain.ConversationID](fields["channel_ids"])
	users := parseIDList[domain.UserID](fields["user_ids"])
	if set {
		err = h.Messages.SetListAccess(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), fields["access_level"], channels, users)
	} else {
		err = h.Messages.DeleteListAccess(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), channels, users)
	}
	if err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) startListDownload(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["list_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	includeArchived, err := parseBoolField(fields["include_archived"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.StartListDownload(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), includeArchived)
	if err != nil {
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job_id": value.ID})
}

func (h Handler) downloadListCSV(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	listID := domain.ListID(strings.TrimSpace(r.URL.Query().Get("list_id")))
	jobID := domain.ListDownloadID(strings.TrimSpace(r.URL.Query().Get("job_id")))
	if listID == "" || jobID == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	download, err := h.Messages.GetListDownload(r.Context(), principal.WorkspaceID, principal.UserID, jobID)
	if err != nil || download.ListID != listID {
		if err == nil {
			err = store.ErrNotFound
		}
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// Quoted, because an unquoted filename parameter ends at the first space or
	// separator: an id carrying one truncated the header a receiving client read.
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(string(listID)+".csv"))
	writer := csv.NewWriter(w)
	if err := writer.Write([]string{"item_id", "fields"}); err != nil {
		return
	}
	cursor := domain.Cursor("")
	for {
		page, err := h.Messages.ListItems(r.Context(), principal.WorkspaceID, principal.UserID, listID, domain.PageRequest{Limit: 100, Cursor: cursor}, download.IncludeArchived)
		if err != nil {
			return
		}
		for _, item := range page.Items {
			if err := writer.Write([]string{string(item.ID), item.Fields}); err != nil {
				return
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil || !page.HasMore {
			return
		}
		cursor = page.NextCursor
	}
}

func (h Handler) getListDownload(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["list_id"]) == "" || strings.TrimSpace(fields["job_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	value, err := h.Messages.GetListDownload(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListDownloadID(strings.TrimSpace(fields["job_id"])))
	if err != nil || value.ListID != domain.ListID(strings.TrimSpace(fields["list_id"])) {
		if err == nil {
			err = store.ErrNotFound
		}
		writeError(w, mapServiceError(err, "list_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": value.Status, "download_url": value.URL})
}

func listResponse(value domain.List) map[string]any {
	return map[string]any{"id": value.ID, "name": value.Name, "description_blocks": json.RawMessage(value.DescriptionBlocks), "schema": json.RawMessage(value.Schema), "todo_mode": value.TodoMode, "date_created": value.CreatedAt.Unix()}
}

func listItemResponse(value domain.ListItem) map[string]any {
	return map[string]any{"id": value.ID, "list_id": value.ListID, "fields": json.RawMessage(value.Fields), "date_created": value.CreatedAt.Unix(), "created_by": value.CreatedBy, "updated_by": value.UpdatedBy, "archived": value.Archived}
}

// clampLimit normalizes a wire `limit`. Slack clamps a limit above a method's
// documented maximum instead of rejecting it, so only a value that is not a
// positive integer is an error. This replaces twelve separate limit parsers with
// five different ceilings, one of which (pageRequest) returned a nil error for an
// out-of-range value and handed Limit: 0 to the store — the store then answered
// with a bare errors.New that reached the client as a 503.
//
// The pinned snapshot declares no universal code for a rejected argument *value*;
// `invalid_arg_name` is the closest code it declares for every operation that
// declares an enum at all, so it is used here.
func clampLimit(raw string, fallback, maximum int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, decodeFailure("invalid_arg_name", "limit must be a positive integer")
	}
	if value > maximum {
		return maximum, nil
	}
	return value, nil
}

// maxPageNumber bounds a wire `page`. It exists so the offset a page describes
// cannot overflow the arithmetic that computes it, here or in the services that
// take a page number directly; it is far past any collection this system can
// hold, so no caller reaches it by paginating.
const maxPageNumber = 1_000_000

// pageNumber reads a wire `page`, which is an offset and not a limit.
//
// It is deliberately not clampLimit. Clamping silently answered `"ok":true` with
// page 100's files under `"page":100` for every page above the ceiling, so a
// caller paginating a collection of 20,000 files at count=1 — a collection this
// same response described as 20,000 pages — was served the same page forever with
// nothing on the wire saying so. A limit above a maximum has an obvious best
// answer and Slack clamps it; an offset does not, so an unusable one is refused
// with the `invalid_arg_name` these operations declare for a rejected argument.
func pageNumber(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 1, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maxPageNumber {
		return 0, decodeFailure("invalid_arg_name", "page must be a positive integer no greater than "+strconv.Itoa(maxPageNumber))
	}
	return value, nil
}

// parseSlackTimestamp reads a Slack `ts` ("seconds.microseconds") into whole
// microseconds since the epoch. It is the comparison basis for the history range
// filter and the validity test behind `bad_timestamp`.
func parseSlackTimestamp(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	whole, fraction, _ := strings.Cut(raw, ".")
	seconds, err := strconv.ParseInt(whole, 10, 64)
	// The result is scaled to microseconds, so a value near math.MaxInt64 would wrap
	// to a negative instant and compare as older than everything.
	if err != nil || seconds < 0 || seconds > maxTimestampSeconds {
		return 0, false
	}
	if len(fraction) > 6 {
		return 0, false
	}
	micros := int64(0)
	if fraction != "" {
		value, err := strconv.ParseInt(fraction, 10, 64)
		if err != nil || value < 0 {
			return 0, false
		}
		for i := len(fraction); i < 6; i++ {
			value *= 10
		}
		micros = value
	}
	return seconds*1000000 + micros, true
}

// reminderTime reads the pinned /reminders.add `time` argument: "the Unix
// timestamp (up to five years from now), the number of seconds until the reminder
// (if within 24 hours), or a natural language description of the time". The
// relative form used to be read as an absolute epoch, so `time=300` created a
// reminder dated 1970-01-01T00:05:00Z, and a natural-language value was reported
// as a generic argument error rather than the enumerated `cannot_parse`.
func reminderTime(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, decodeFailure("cannot_parse", "time must be a Unix timestamp or a number of seconds")
	}
	if seconds <= 0 {
		return time.Time{}, decodeFailure("cannot_parse", "time must be positive")
	}
	if seconds < secondsPerDay {
		return now.Add(time.Duration(seconds) * time.Second), nil
	}
	when := time.Unix(seconds, 0).UTC()
	if when.After(now.AddDate(5, 0, 0)) {
		return time.Time{}, decodeFailure("cannot_parse", "time must be within five years")
	}
	return when, nil
}

const secondsPerDay = 24 * 60 * 60

// maxTimestampSeconds is the largest `ts` whose microsecond scaling fits in int64.
const maxTimestampSeconds = math.MaxInt64 / 1000000

func (h Handler) presentEntityDetails(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["trigger_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	userAuthRequired, err := parseBoolField(fields["user_auth_required"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	err = h.Messages.PresentEntityDetails(r.Context(), principal.WorkspaceID, principal.UserID, fields["trigger_id"], fields["metadata"], userAuthRequired, fields["user_auth_url"], fields["error"])
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) presentEntityComments(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["trigger_id"]) == "" || strings.TrimSpace(fields["comments"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	canPostComment, err := parseBoolField(fields["can_post_comment"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	userAuthRequired, err := parseBoolField(fields["user_auth_required"])
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	err = h.Messages.PresentEntityComments(r.Context(), principal.WorkspaceID, principal.UserID, fields["trigger_id"], fields["comments"], fields["cursor"], canPostComment, fields["delete_action_id"], userAuthRequired, fields["user_auth_url"], fields["error"])
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) acknowledgeEntityCommentAction(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if strings.TrimSpace(fields["trigger_id"]) == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	err = h.Messages.AcknowledgeEntityCommentAction(r.Context(), principal.WorkspaceID, principal.UserID, fields["trigger_id"], fields["comment"], fields["error"])
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// queryCredentialNames are the parameters that must never be read from a URL
// query on the OpenID Connect endpoints. A query string is retained by access
// logs, proxy logs, browser history and the Referer header of any subsequent
// navigation, so a credential placed there is disclosed for as long as those
// records live.
//
// oauth.access and oauth.v2.access are deliberately excluded: the pinned contract
// declares `client_secret` and `code` as `in: query` parameters of both
// (specs/upstream/slack-api-specs/web-api/slack_web_openapi_v2.json), so
// rejecting them there would break the documented contract. The openid.connect.*
// endpoints are absent from that snapshot and are governed by RFC 6749 / RFC 6750
// instead.
var queryCredentialNames = []string{"client_secret", "code", "code_verifier", "refresh_token", "token"}

func queryCarriesCredential(r *http.Request) bool {
	query := r.URL.Query()
	for _, name := range queryCredentialNames {
		if _, present := query[name]; present {
			return true
		}
	}
	return false
}

func (h Handler) openIDConnectToken(w http.ResponseWriter, r *http.Request) {
	// openid.connect.token is not in the pinned Slack snapshot, so RFC 6749 §3.2 is
	// the governing contract: "The client MUST use the HTTP POST method when making
	// access token requests." A GET carried the client secret, the authorization
	// code and the PKCE verifier in the URL, where they reach access logs, proxy
	// logs and the Referer header of any subsequent navigation.
	if r.Method != http.MethodPost {
		writeError(w, "invalid_request")
		return
	}
	// Requiring POST is not enough on its own: ParseForm merges the URL query into
	// r.Form, so a POST could still carry the secret, the code or the PKCE verifier
	// in the query string and have it honoured.
	if queryCarriesCredential(r) {
		writeError(w, "invalid_request")
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	clientID, clientSecret := strings.TrimSpace(fields["client_id"]), strings.TrimSpace(fields["client_secret"])
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		if clientID != "" && clientID != basicID || clientSecret != "" && clientSecret != basicSecret {
			writeError(w, "invalid_client")
			return
		}
		if clientID == "" {
			clientID = basicID
		}
		if clientSecret == "" {
			clientSecret = basicSecret
		}
	}
	token, err := h.Messages.OpenIDConnectToken(r.Context(), clientID, clientSecret, fields["code"], fields["redirect_uri"], fields["grant_type"], fields["refresh_token"], fields["code_verifier"])
	if err != nil {
		reason := "invalid_grant"
		if errors.Is(err, service.ErrInvalidOAuthClient) {
			reason = "invalid_client"
		} else if strings.TrimSpace(fields["grant_type"]) != "" && strings.TrimSpace(fields["grant_type"]) != "authorization_code" && strings.TrimSpace(fields["grant_type"]) != "refresh_token" {
			reason = "unsupported_grant_type"
		}
		writeError(w, reason)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "access_token": token.AccessToken, "token_type": token.TokenType, "id_token": token.IDToken, "refresh_token": token.RefreshToken})
}

func (h Handler) openIDConnectUserInfo(w http.ResponseWriter, r *http.Request) {
	// OpenID Connect Core §5.3.1 requires the UserInfo request to present its
	// access token as a bearer credential, and RFC 6750 §2.3 warns that the URI
	// query form "SHOULD NOT be used" because the URL is recorded by every proxy,
	// access log and browser history along the way. GET stays registered — §5.3.1
	// requires it — but the credential has to arrive in the header or the body.
	if queryCarriesCredential(r) {
		writeError(w, "invalid_request")
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(fields["token"])
	}
	if token == "" {
		writeError(w, "invalid_auth")
		return
	}
	value, err := h.Messages.OpenIDConnectUserInfo(r.Context(), token)
	if err != nil {
		writeError(w, "invalid_auth")
		return
	}
	response := map[string]any{"ok": true, "sub": value.Subject, "https://slack.com/user_id": value.UserID, "https://slack.com/team_id": value.WorkspaceID, "email": value.Email, "email_verified": value.EmailVerified, "name": value.Name, "given_name": value.GivenName, "family_name": value.FamilyName, "locale": value.Locale, "picture": value.Picture, "https://slack.com/team_name": value.TeamName, "https://slack.com/team_domain": value.TeamDomain, "https://slack.com/team_image_default": value.TeamImageDefault}
	if value.DateEmailVerified != 0 {
		response["date_email_verified"] = value.DateEmailVerified
	}
	for size, image := range value.UserImages {
		response["https://slack.com/user_image_"+size] = image
	}
	for size, image := range value.TeamImages {
		response["https://slack.com/team_image_"+size] = image
	}
	writeJSON(w, http.StatusOK, response)
}

type incomingWebhookPayload struct {
	Text        string          `json:"text"`
	ThreadTS    string          `json:"thread_ts"`
	Blocks      json.RawMessage `json:"blocks"`
	Attachments json.RawMessage `json:"attachments"`
}

func (h Handler) incomingWebhook(w http.ResponseWriter, r *http.Request) {
	workspaceID := domain.WorkspaceID(r.PathValue("workspace"))
	appID := domain.AppID(r.PathValue("app"))
	secret := r.PathValue("secret")
	var payload incomingWebhookPayload
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil || (payload.Text == "" && len(payload.Blocks) == 0 && len(payload.Attachments) == 0) || (len(payload.Blocks) > 0 && !json.Valid(payload.Blocks)) || (len(payload.Attachments) > 0 && !json.Valid(payload.Attachments)) {
		writeIncomingWebhookError(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	blocks, err := domain.NormalizeBlocks(payload.Blocks)
	if err != nil {
		writeIncomingWebhookError(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	attachments, err := domain.NormalizeAttachments(payload.Attachments)
	if err != nil {
		writeIncomingWebhookError(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	// Slack's incoming-webhook response body is the literal string "ok" and carries
	// no message, so the posted message is intentionally not part of the response.
	if _, err := h.Messages.PostIncomingWebhookWithAttachments(r.Context(), workspaceID, appID, secret, payload.Text, blocks, attachments, domain.MessageTimestamp(payload.ThreadTS), r.Header.Get("Idempotency-Key")); err != nil {
		// Incoming webhooks are not Web API methods: the pinned contract for
		// hooks.slack.com is a plain-text body with a non-200 status. An unknown
		// workspace, app, secret, or a disabled hook is indistinguishable to the
		// caller by design, so all of them answer 404 `no_team`.
		if errors.Is(err, service.ErrConversationAlreadyArchived) {
			writeIncomingWebhookError(w, http.StatusGone, "channel_is_archived")
			return
		}
		reason := mapServiceError(err, "no_team")
		status := http.StatusBadRequest
		if reason == "no_team" {
			status = http.StatusNotFound
		} else {
			reason = "invalid_payload"
		}
		writeIncomingWebhookError(w, status, reason)
		return
	}
	writePlain(w, http.StatusOK, "ok")
}

// workflowTriggerWebhook receives the external POST that fires a webhook
// trigger. Like an incoming webhook, the path secret is the whole credential:
// an unknown workspace, trigger, or secret, a disabled trigger, and an
// unpublished workflow all answer the same plain-text 404 `no_team`, and a
// successful fire answers the literal string "ok". The posted JSON object
// becomes the run's inputs.
func (h Handler) workflowTriggerWebhook(w http.ResponseWriter, r *http.Request) {
	workspaceID := domain.WorkspaceID(r.PathValue("workspace"))
	triggerID := domain.WorkflowTriggerID(r.PathValue("trigger"))
	secret := r.PathValue("secret")
	inputs := "{}"
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeIncomingWebhookError(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
			writeIncomingWebhookError(w, http.StatusBadRequest, "invalid_payload")
			return
		}
		inputs = trimmed
	}
	if _, err := h.Messages.RunWebhookTrigger(r.Context(), workspaceID, triggerID, secret, inputs); err != nil {
		if errors.Is(err, service.ErrWebhookTriggerSecret) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
			writeIncomingWebhookError(w, http.StatusNotFound, "no_team")
			return
		}
		writeIncomingWebhookError(w, http.StatusBadRequest, "invalid_payload")
		return
	}
	writePlain(w, http.StatusOK, "ok")
}

// writeIncomingWebhookError is distinct from writePlain so the source-level
// Slack error-contract gate can identify the argument that is an error code.
// Incoming webhooks intentionally return plain text rather than a Web API JSON
// envelope.
func writeIncomingWebhookError(w http.ResponseWriter, status int, code string) {
	writePlain(w, status, code)
}

func writePlain(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, value)
}

func (h Handler) adminIncomingWebhookCreate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if fields["app_id"] == "" || fields["channel_id"] == "" || fields["bot_user_id"] == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	webhook, secret, err := h.Messages.AdminCreateIncomingWebhook(r.Context(), principal.WorkspaceID, principal.UserID, domain.AppID(fields["app_id"]), domain.ConversationID(fields["channel_id"]), domain.UserID(fields["bot_user_id"]))
	if err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "incoming_webhook": map[string]any{"id": webhook.ID, "channel_id": webhook.ConversationID, "url": "https://hooks.slack.com/services/" + string(webhook.WorkspaceID) + "/" + string(webhook.AppID) + "/" + secret}})
}

func (h Handler) adminIncomingWebhookEnable(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminAppsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeDecodeError(w, err)
		return
	}
	if fields["webhook_id"] == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	// This accepted only the literals "true"/"false", diverging from every other
	// boolean in this file, which also accept 1/0 and any casing.
	raw, present := fields["enabled"]
	if !present {
		writeError(w, "invalid_arg_name")
		return
	}
	enabled, err := parseBoolField(raw)
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	if err := h.Messages.AdminSetIncomingWebhookEnabled(r.Context(), principal.WorkspaceID, principal.UserID, domain.IncomingWebhookID(fields["webhook_id"]), enabled); err != nil {
		writeError(w, mapServiceError(err, "invalid_arguments"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) filesGetUploadURLExternal(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	name := strings.TrimSpace(fields["filename"])
	if name == "" {
		writeError(w, "invalid_arg_name")
		return
	}
	size, err := strconv.ParseInt(strings.TrimSpace(fields["length"]), 10, 64)
	if err != nil || size <= 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	mimeType := strings.TrimSpace(fields["mime_type"])
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	upload, err := h.Messages.CreateExternalUpload(r.Context(), principal.WorkspaceID, principal.UserID, name, mimeType, size, 15*time.Minute)
	if err != nil {
		writeError(w, mapServiceError(err, "team_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "upload_url": externalUploadURL(r, upload.ID), "file_id": upload.ID})
}

func (h Handler) externalFileUpload(w http.ResponseWriter, r *http.Request) {
	id := domain.ExternalUploadID(strings.TrimSpace(r.PathValue("upload")))
	if id == "" || r.ContentLength < 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	source := io.Reader(r.Body)
	size := r.ContentLength
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mediaType == "multipart/form-data" {
		reader, err := r.MultipartReader()
		if err != nil {
			writeError(w, "invalid_arg_name")
			return
		}
		var bodyPart *multipart.Part
		for {
			part, partErr := reader.NextPart()
			if errors.Is(partErr, io.EOF) {
				break
			}
			if partErr != nil {
				writeError(w, "invalid_arg_name")
				return
			}
			if part.FormName() == "body" {
				bodyPart = part
				break
			}
			_ = part.Close()
		}
		if bodyPart == nil {
			writeError(w, "invalid_arg_name")
			return
		}
		defer bodyPart.Close()
		source = bodyPart
		// multipart.Part deliberately does not expose a length. The durable
		// upload ticket supplies it and the blob adapter enforces it exactly.
		size = -1
	}
	if err := h.Messages.UploadExternalFile(r.Context(), id, size, source); err != nil {
		writeError(w, mapServiceError(err, "file_not_found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) filesCompleteUploadExternal(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeError(w, "invalid_arg_name")
		return
	}
	completions := make([]domain.ExternalUploadCompletion, 0, 1)
	if uploadID := strings.TrimSpace(fields["upload_id"]); uploadID != "" {
		completions = append(completions, domain.ExternalUploadCompletion{ID: domain.ExternalUploadID(uploadID), Title: fields["title"]})
	} else {
		var entries []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		raw := strings.TrimSpace(fields["files"])
		if raw == "" || json.Unmarshal([]byte(raw), &entries) != nil || len(entries) == 0 {
			writeError(w, "invalid_arg_name")
			return
		}
		for _, entry := range entries {
			completions = append(completions, domain.ExternalUploadCompletion{ID: domain.ExternalUploadID(strings.TrimSpace(entry.ID)), Title: strings.TrimSpace(entry.Title)})
		}
	}
	if len(completions) == 0 {
		writeError(w, "invalid_arg_name")
		return
	}
	if fields["title"] != "" && len(completions) == 1 {
		completions[0].Title = fields["title"]
	}
	channels := parseIDList[domain.ConversationID](fields["channels"])
	if channel := strings.TrimSpace(fields["channel_id"]); channel != "" {
		channels = append(channels, domain.ConversationID(channel))
	}
	files, err := h.Messages.CompleteExternalUploads(r.Context(), principal.WorkspaceID, principal.UserID, completions, channels, fields["initial_comment"], fields["blocks"], domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"])))
	if err != nil {
		if errors.Is(err, service.ErrConversationAlreadyArchived) {
			// The current method reference does not enumerate chat.postMessage's
			// is_archived code. It names a channel that cannot accept the
			// resulting file-share message as posting_to_channel_denied.
			writeError(w, "posting_to_channel_denied")
			return
		}
		writeError(w, mapServiceError(err, "file_not_found"))
		return
	}
	responses := make([]map[string]any, 0, len(files))
	for _, file := range files {
		responses = append(responses, fileResponse(file))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": responses})
}

func externalUploadURL(r *http.Request, id domain.ExternalUploadID) string {
	scheme := strings.TrimSpace(strings.SplitN(r.Header.Get("X-Forwarded-Proto"), ",", 2)[0])
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + r.Host + "/internal/files/external/" + url.PathEscape(string(id))
}
