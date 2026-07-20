package slack

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/socketmode"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	Messages      chatapi.Service
	Authenticator auth.Authenticator
	SocketMode    socketmode.Service
	SocketAuth    auth.Authenticator
}

var errAccessLogging = errors.New("access logging failed")

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
	mux.HandleFunc("GET /api/api.test", h.apiTest)
	mux.HandleFunc("POST /api/api.test", h.apiTest)
	mux.HandleFunc("POST /api/auth.test", h.authTest)
	mux.HandleFunc("GET /api/auth.test", h.authTest)
	mux.HandleFunc("GET /api/oauth.access", h.oauthAccess)
	mux.HandleFunc("POST /api/oauth.access", h.oauthAccess)
	mux.HandleFunc("GET /api/oauth.token", h.oauthAccess)
	mux.HandleFunc("POST /api/oauth.token", h.oauthAccess)
	mux.HandleFunc("GET /api/oauth.v2.access", h.oauthV2Access)
	mux.HandleFunc("POST /api/oauth.v2.access", h.oauthV2Access)
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
	mux.HandleFunc("GET /api/dialog.open", h.dialogOpen)
	mux.HandleFunc("POST /api/dialog.open", h.dialogOpen)
	mux.HandleFunc("GET /api/apps.event.authorizations.list", h.appsEventAuthorizationsList)
	mux.HandleFunc("POST /api/apps.event.authorizations.list", h.appsEventAuthorizationsList)
	mux.HandleFunc("POST /api/apps.uninstall", h.appsUninstall)
	mux.HandleFunc("GET /api/team.info", h.teamInfo)
	mux.HandleFunc("POST /api/team.info", h.teamInfo)
	mux.HandleFunc("GET /api/rtm.connect", h.rtmConnect)
	mux.HandleFunc("POST /api/rtm.connect", h.rtmConnect)
	mux.HandleFunc("POST /api/apps.connections.open", h.appsConnectionsOpen)
	mux.HandleFunc("GET /api/apps.connections.open", h.appsConnectionsOpen)
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
	principal, err := h.SocketAuth.Authenticate(r)
	if err != nil || !principal.HasScope(auth.ScopeConnectionsWrite) || principal.AppID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	result, err := h.SocketMode.Open(r.Context(), principal.AppID)
	if err != nil {
		code, reason := mapServiceError(err, "service_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": result.URL})
}

func (h Handler) apiTest(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	request, err := normalizeHistoryRequest(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.History(r.Context(), principal.WorkspaceID, principal.UserID, request.Channel, request.Page)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	result := make([]map[string]any, 0, len(page.Messages))
	for _, message := range page.Messages {
		result = append(result, messageResponse(message))
	}
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	request, err := normalizeHistoryRequest(fields)
	if err != nil || strings.TrimSpace(fields["ts"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.Replies(r.Context(), principal.WorkspaceID, principal.UserID, request.Channel, domain.MessageTimestamp(strings.TrimSpace(fields["ts"])), request.Page)
	if err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	result := make([]map[string]any, 0, len(page.Messages))
	for _, message := range page.Messages {
		result = append(result, messageResponse(message))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "messages": result, "has_more": page.HasMore, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}

type historyRequest struct {
	Channel domain.ConversationID
	Page    domain.PageRequest
}

func normalizeHistoryRequest(fields map[string]string) (historyRequest, error) {
	channel := strings.TrimSpace(fields["channel"])
	if channel == "" {
		return historyRequest{}, errors.New("channel is required")
	}
	limit := 100
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 200 {
			return historyRequest{}, errors.New("limit must be between 1 and 200")
		}
		limit = parsed
	}
	cursor := domain.Cursor(strings.TrimSpace(fields["cursor"]))
	if cursor != "" {
		if _, _, err := domain.DecodeMessageCursor(cursor); err != nil {
			return historyRequest{}, err
		}
	}
	return historyRequest{Channel: domain.ConversationID(channel), Page: domain.PageRequest{Limit: limit, Cursor: cursor}}, nil
}

func (h Handler) authTest(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	workspace, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	teamName := strings.TrimSpace(workspace.Name)
	if teamName == "" {
		teamName = string(workspace.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "url": "http://localhost/", "team": teamName, "team_id": workspace.ID, "user": string(principal.UserID), "user_id": principal.UserID})
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
	category := func(values []string) map[string]any {
		return map[string]any{"resources": resource([]string{}), "scopes": values}
	}
	result := map[string]any{
		"app_home": category([]string{}),
		"channel":  category([]string{}),
		"group":    category([]string{}),
		"im":       category([]string{}),
		"mpim":     category([]string{}),
		"team":     category(scopes),
	}
	result["team"] = map[string]any{"resources": resource([]string{string(workspaceID)}), "scopes": scopes}
	return result
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	limit := 100
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
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
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if strings.TrimSpace(fields["event_context"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "authorizations": []map[string]any{{
		"enterprise_id": "",
		"is_bot":        false,
		"team_id":       principal.WorkspaceID,
		"user_id":       principal.UserID,
	}}})
}

func (h Handler) appsPermissionsUsersList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	limit := 100
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
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
	h.requestAppPermissions(w, r, "")
}

func (h Handler) appsPermissionsUsersRequest(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	if strings.TrimSpace(fields["user"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	h.requestAppPermissionsWithFields(w, r, fields, domain.UserID(strings.TrimSpace(fields["user"])))
}

func (h Handler) requestAppPermissions(w http.ResponseWriter, r *http.Request, target domain.UserID) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	h.requestAppPermissionsWithFields(w, r, fields, target)
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RequestAppPermissions(r.Context(), principal.WorkspaceID, principal.UserID, target, scopes, triggerID); err != nil {
		code, reason := mapServiceError(err, "permission_request_failed")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) viewsOpen(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	value, err := h.Messages.OpenView(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["trigger_id"]), fields["view"])
	if err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "view": viewResponse(value)})
}

func (h Handler) viewsPublish(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	target := domain.UserID(strings.TrimSpace(fields["user_id"]))
	if target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.PublishView(r.Context(), principal.WorkspaceID, principal.UserID, target, fields["view"], strings.TrimSpace(fields["hash"]))
	if err != nil {
		code, reason := mapServiceError(err, "view_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "view": viewResponse(value)})
}

func (h Handler) viewsPush(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	value, err := h.Messages.PushView(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["trigger_id"]), fields["view"])
	if err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "view": viewResponse(value)})
}

func (h Handler) viewsUpdate(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	value, err := h.Messages.UpdateView(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["view_id"]), strings.TrimSpace(fields["external_id"]), fields["view"], strings.TrimSpace(fields["hash"]))
	if err != nil {
		code, reason := mapServiceError(err, "view_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "view": viewResponse(value)})
}

func viewResponse(value domain.View) map[string]any {
	result := make(map[string]any)
	if err := json.Unmarshal([]byte(value.Payload), &result); err != nil {
		panic(fmt.Sprintf("stored view payload is invalid: %v", err))
	}
	result["id"] = value.ID
	result["team_id"] = value.WorkspaceID
	result["hash"] = value.Hash
	result["root_view_id"] = value.RootViewID
	result["previous_view_id"] = value.PreviousViewID
	result["external_id"] = value.ExternalID
	return result
}

func (h Handler) workflowStepCompleted(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, auth.ScopeWorkflowStepsExecute)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.WorkflowStepCompleted(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["workflow_step_execute_id"]), fields["outputs"]); err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) workflowStepFailed(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, auth.ScopeWorkflowStepsExecute)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.WorkflowStepFailed(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["workflow_step_execute_id"]), fields["error"]); err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) workflowUpdateStep(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, auth.ScopeWorkflowStepsExecute)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.WorkflowUpdateStep(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["workflow_step_edit_id"]), fields["inputs"], fields["outputs"], fields["step_name"], fields["step_image_url"]); err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) functionsCompleteSuccess(w http.ResponseWriter, r *http.Request) {
	fields, ok := h.functionCompletionFields(w, r, "outputs")
	if !ok {
		return
	}
	var outputs map[string]json.RawMessage
	if err := json.Unmarshal([]byte(fields["outputs"]), &outputs); err != nil || outputs == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) functionsCompleteError(w http.ResponseWriter, r *http.Request) {
	_, ok := h.functionCompletionFields(w, r, "error")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) functionCompletionFields(w http.ResponseWriter, r *http.Request, requiredField string) (map[string]string, bool) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return nil, false
	}
	if _, err := h.authenticate(r, ""); err != nil {
		writeAuthError(w, err)
		return nil, false
	}
	if strings.TrimSpace(fields["function_execution_id"]) == "" || strings.TrimSpace(fields[requiredField]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return nil, false
	}
	return fields, true
}

func (h Handler) dialogOpen(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.OpenDialog(r.Context(), principal.WorkspaceID, principal.UserID, strings.TrimSpace(fields["trigger_id"]), fields["dialog"]); err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if _, err := h.authenticate(r, ""); err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(fields["token"])
	}
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authed"})
		return
	}
	if err := h.Messages.RevokeToken(r.Context(), token); err != nil {
		code, reason := mapServiceError(err, "token_revocation_failed")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) authRevoke(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(fields["token"])
	}
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authed"})
		return
	}
	if _, err := h.Authenticator.Authenticate(r); err != nil {
		writeAuthError(w, err)
		return
	}
	test, err := parseBoolField(fields["test"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if !test {
		if err := h.Messages.RevokeToken(r.Context(), token); err != nil {
			code, reason := mapServiceError(err, "invalid_auth")
			writeJSON(w, code, map[string]any{"ok": false, "error": reason})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": !test})
}

func (h Handler) oauthAccess(w http.ResponseWriter, r *http.Request) {
	h.oauthExchange(w, r, false)
}

func (h Handler) oauthV2Access(w http.ResponseWriter, r *http.Request) {
	h.oauthExchange(w, r, true)
}

func (h Handler) oauthExchange(w http.ResponseWriter, r *http.Request, v2 bool) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	clientID, clientSecret := strings.TrimSpace(fields["client_id"]), strings.TrimSpace(fields["client_secret"])
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		if clientID != "" && clientID != basicID || clientSecret != "" && clientSecret != basicSecret {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_client_id"})
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
			reason := "invalid_grant_type"
			if grantType == "refresh_token" {
				reason = "invalid_refresh_token"
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
			return
		}
	}
	if v2 && strings.TrimSpace(fields["code"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_code"})
		return
	}
	token, err := h.Messages.OAuthExchange(r.Context(), clientID, clientSecret, fields["code"], fields["redirect_uri"])
	if err != nil {
		reason := "invalid_code"
		if errors.Is(err, service.ErrInvalidOAuthClient) {
			reason = "invalid_client_id"
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
		return
	}
	response := map[string]any{"ok": true, "access_token": token.AccessToken, "app_id": token.AppID, "team_id": token.WorkspaceID, "scope": strings.Join(token.Scopes, ","), "token_type": token.TokenType}
	if !v2 {
		response["team_name"] = ""
	} else {
		// This implementation issues a user grant only. Slack's v2 user-only
		// response keeps the token under authed_user; a bot token must never be
		// fabricated by copying the user token into the top-level fields.
		delete(response, "access_token")
		delete(response, "team_id")
		delete(response, "scope")
		delete(response, "token_type")
		response["team"] = map[string]any{"id": token.WorkspaceID}
		response["enterprise"] = nil
		response["is_enterprise_install"] = false
		response["authed_user"] = map[string]any{"id": token.UserID, "access_token": token.AccessToken, "scope": strings.Join(token.Scopes, ","), "token_type": "user"}
	}
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) botsInfo(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
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
		code, reason := mapServiceError(err, "bot_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bot": map[string]any{"id": value.ID, "app_id": value.AppID, "user_id": value.UserID, "name": value.Name, "deleted": value.Deleted, "updated": value.UpdatedAt.Unix(), "icons": map[string]string{"image_36": value.Image36, "image_48": value.Image48, "image_72": value.Image72}}})
}

func (h Handler) migrationExchange(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	principal, err := h.authenticate(r, auth.ScopeTokensBasic)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_team"})
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
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
	}
	value, err := h.Messages.MigrationExchange(r.Context(), principal.WorkspaceID, principal.UserID, ids, toOld)
	if err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	domainName := team.Domain
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "team": map[string]any{"id": team.ID, "name": team.Name, "domain": domainName}})
}

func (h Handler) rtmConnect(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	team, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	connection, err := h.Messages.CreateRTMConnection(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "service_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if _, err := h.authenticate(r, auth.ScopeTeamRead); err != nil {
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	target := domain.UserID(strings.TrimSpace(fields["user"]))
	value, err := h.Messages.TeamBillableInfo(r.Context(), principal.WorkspaceID, principal.UserID, target)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	limit, page := 100, 1
	if raw := strings.TrimSpace(fields["count"]); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err != nil || limit < 1 || limit > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if raw := strings.TrimSpace(fields["page"]); raw != "" {
		page, err = strconv.Atoi(raw)
	}
	if err != nil || page < 1 || page > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	before := time.Time{}
	if raw := strings.TrimSpace(fields["before"]); raw != "" {
		seconds, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || seconds <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
		before = time.Unix(seconds, 0).UTC()
	}
	values, hasMore, err := h.Messages.ListAccessLogs(r.Context(), principal.WorkspaceID, principal.UserID, before, limit, page)
	if err != nil {
		code, reason := mapServiceError(err, "access_logs_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		return
	}
	principal, err := h.authenticate(r, auth.ScopeAdmin)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	count, page := 100, 1
	if raw := strings.TrimSpace(fields["count"]); raw != "" {
		count, err = strconv.Atoi(raw)
	}
	if err != nil || count < 1 || count > 1000 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if raw := strings.TrimSpace(fields["page"]); raw != "" {
		page, err = strconv.Atoi(raw)
	}
	if err != nil || page < 1 || page > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.IntegrationLogs(r.Context(), principal.WorkspaceID, principal.UserID, fields["app_id"], fields["change_type"], fields["service_id"], fields["user"], count, page)
	if err != nil {
		code, reason := mapServiceError(err, "integration_logs_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	request, err := decodeListRequest(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		code, reason := mapServiceError(err, "users_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	users := make([]map[string]any, 0, len(page.Users))
	for _, user := range page.Users {
		users = append(users, userResponse(user))
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	teamID, targetID := strings.TrimSpace(fields["team_id"]), domain.UserID(strings.TrimSpace(fields["user_id"]))
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RemoveUser(r.Context(), principal.WorkspaceID, principal.UserID, targetID); err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if teamID := strings.TrimSpace(fields["team_id"]); teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || strings.TrimSpace(fields["session_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RevokeSession(r.Context(), strings.TrimSpace(fields["session_id"])); err != nil {
		code, reason := mapServiceError(err, "session_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	teamID, targetID := strings.TrimSpace(fields["team_id"]), domain.UserID(strings.TrimSpace(fields["user_id"]))
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.ResetUserSessions(r.Context(), principal.WorkspaceID, principal.UserID, targetID); err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	teamID, targetID, rawExpiration := strings.TrimSpace(fields["team_id"]), domain.UserID(strings.TrimSpace(fields["user_id"])), strings.TrimSpace(fields["expiration_ts"])
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" || rawExpiration == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	seconds, err := strconv.ParseInt(rawExpiration, 10, 64)
	if err != nil || seconds < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	expiration := time.Time{}
	if seconds != 0 {
		expiration = time.Unix(seconds, 0).UTC()
	}
	if err := h.Messages.SetUserExpiration(r.Context(), principal.WorkspaceID, principal.UserID, targetID, expiration); err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || strings.TrimSpace(fields["email"]) == "" || strings.TrimSpace(fields["channel_ids"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	channels := parseConversationIDs(fields["channel_ids"])
	resend, restricted, ultraRestricted, err := parseOptionalBooleans(fields, "resend", "is_restricted", "is_ultra_restricted")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	var expiration time.Time
	if raw := strings.TrimSpace(fields["guest_expiration_ts"]); raw != "" {
		seconds, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || seconds <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
		expiration = time.Unix(seconds, 0).UTC()
	}
	err = h.Messages.AdminInviteUser(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"], channels, fields["custom_message"], fields["real_name"], resend, restricted, ultraRestricted, expiration)
	if err != nil {
		code, reason := mapServiceError(err, "invite_failed")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	targetID := domain.UserID(strings.TrimSpace(fields["user_id"]))
	teamID := strings.TrimSpace(fields["team_id"])
	if err != nil || teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	channels := []domain.ConversationID{}
	if strings.TrimSpace(fields["channel_ids"]) != "" {
		channels = parseConversationIDs(fields["channel_ids"])
	}
	if err := h.Messages.AdminAssignUser(r.Context(), principal.WorkspaceID, principal.UserID, targetID, channels); err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	teamID := strings.TrimSpace(fields["team_id"])
	id := domain.InviteRequestID(strings.TrimSpace(fields["invite_request_id"]))
	if err != nil || teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if approve {
		err = h.Messages.AdminApproveInviteRequest(r.Context(), principal.WorkspaceID, principal.UserID, id)
	} else {
		err = h.Messages.AdminDenyInviteRequest(r.Context(), principal.WorkspaceID, principal.UserID, id)
	}
	if err != nil {
		code, reason := mapServiceError(err, "invite_request_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	teamID := strings.TrimSpace(fields["team_id"])
	if err != nil || (teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.AdminListInviteRequests(r.Context(), principal.WorkspaceID, principal.UserID, status, request)
	if err != nil {
		code, reason := mapServiceError(err, "invite_requests_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	requestID := domain.AppRequestID(strings.TrimSpace(fields["request_id"]))
	if teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID || appID == "" && requestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if approve {
		err = h.Messages.AdminApproveApp(r.Context(), principal.WorkspaceID, principal.UserID, appID, requestID)
	} else {
		err = h.Messages.AdminRestrictApp(r.Context(), principal.WorkspaceID, principal.UserID, appID, requestID)
	}
	if err != nil {
		code, reason := mapServiceError(err, "app_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	teamID := strings.TrimSpace(fields["team_id"])
	if (teamID != "" && domain.WorkspaceID(teamID) != principal.WorkspaceID) || strings.TrimSpace(fields["enterprise_id"]) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.AdminListApps(r.Context(), principal.WorkspaceID, principal.UserID, status, request)
	if err != nil {
		code, reason := mapServiceError(err, "apps_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	teamID, targetID := strings.TrimSpace(fields["team_id"]), domain.UserID(strings.TrimSpace(fields["user_id"]))
	if teamID == "" || domain.WorkspaceID(teamID) != principal.WorkspaceID || targetID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.SetUserRole(r.Context(), principal.WorkspaceID, principal.UserID, targetID, role); err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel, name := domain.ConversationID(strings.TrimSpace(fields["channel_id"])), strings.TrimSpace(fields["name"])
	if err != nil || channel == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.AdminRenameConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel, name)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["name"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	private, err := parseBoolField(fields["is_private"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.CreateConversation(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], private)
	if err != nil {
		code, reason := mapServiceError(err, "name_taken")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel_id": conversation.ID, "channel": conversationResponse(conversation)})
}

func (h Handler) adminConversationArchive(w http.ResponseWriter, r *http.Request) {
	h.adminSetConversationArchived(w, r, true)
}

func (h Handler) adminConversationUnarchive(w http.ResponseWriter, r *http.Request) {
	h.adminSetConversationArchived(w, r, false)
}

func (h Handler) adminConversationDelete(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if err != nil || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.AdminDeleteConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel); err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	conversationID := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	groupID := domain.UserGroupID(strings.TrimSpace(fields["group_id"]))
	if err != nil || conversationID == "" || groupID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if add {
		err = h.Messages.AdminAddConversationAccessGroup(r.Context(), principal.WorkspaceID, principal.UserID, conversationID, groupID)
	} else {
		err = h.Messages.AdminRemoveConversationAccessGroup(r.Context(), principal.WorkspaceID, principal.UserID, conversationID, groupID)
	}
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	conversationID := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if err != nil || conversationID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	groups, err := h.Messages.AdminListConversationAccessGroups(r.Context(), principal.WorkspaceID, principal.UserID, conversationID)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	values := make([]string, 0, len(groups))
	for _, groupID := range groups {
		values = append(values, string(groupID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "group_ids": values})
}

func (h Handler) adminSetConversationArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if err != nil || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.AdminSetConversationArchived(r.Context(), principal.WorkspaceID, principal.UserID, channel, archived)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) adminConversationInvite(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminConversationsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	usersField := strings.TrimSpace(fields["users"])
	if usersField == "" {
		usersField = strings.TrimSpace(fields["user_ids"])
	}
	if err != nil || channel == "" || usersField == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	users := parseCallUsers(usersField)
	conversation, err := h.Messages.AdminInviteConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, channel, users)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if err != nil || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.AdminConvertConversationToPrivate(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil || strings.TrimSpace(fields["query"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.AdminSearchConversations(r.Context(), principal.WorkspaceID, principal.UserID, fields["query"], request)
	if err != nil {
		code, reason := mapServiceError(err, "search_failed")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["channel_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	teams, hasMore, nextCursor, err := h.Messages.AdminConversationTeams(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel_id"])), request)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["channel_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	orgChannel, err := parseBoolField(fields["org_channel"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
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
	if err := h.Messages.AdminSetConversationTeams(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel_id"])), teams, orgChannel); err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["channel_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	teams := make([]domain.WorkspaceID, 0)
	for _, raw := range strings.Split(fields["leaving_team_ids"], ",") {
		if value := strings.TrimSpace(raw); value != "" {
			teams = append(teams, domain.WorkspaceID(value))
		}
	}
	if err := h.Messages.AdminDisconnectSharedConversation(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel_id"])), teams); err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
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
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if err != nil || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	prefs, err := h.Messages.AdminGetConversationPrefs(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if err != nil || channel == "" || strings.TrimSpace(fields["prefs"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	var payload conversationPrefsPayload
	if err := json.Unmarshal([]byte(fields["prefs"]), &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	prefs := conversationPrefsFromPayload(channel, payload)
	if _, err := h.Messages.AdminSetConversationPrefs(r.Context(), principal.WorkspaceID, principal.UserID, channel, prefs); err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	principal, err := h.authenticate(r, auth.ScopeEmojiRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	values, err := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "emoji_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "emoji": emojiResponse(values)})
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

func (h Handler) adminEmojiList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminEmojiWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	values, err := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "emoji_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "emoji": emojiResponse(values)})
}

func (h Handler) adminEmojiAdd(w http.ResponseWriter, r *http.Request) {
	principal, fields, ok := h.adminEmojiFields(w, r)
	if !ok {
		return
	}
	if err := h.Messages.AdminAddEmoji(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], fields["url"]); err != nil {
		code, reason := mapServiceError(err, "emoji_add_failed")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		code, reason := mapServiceError(err, "emoji_add_failed")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		code, reason := mapServiceError(err, "emoji_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		code, reason := mapServiceError(err, "emoji_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (h Handler) adminEmojiFields(w http.ResponseWriter, r *http.Request) (auth.Principal, map[string]string, bool) {
	principal, err := h.authenticate(r, auth.ScopeAdminEmojiWrite)
	if err != nil {
		writeAuthError(w, err)
		return auth.Principal{}, nil, false
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return auth.Principal{}, nil, false
	}
	if strings.TrimSpace(fields["name"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return auth.Principal{}, nil, false
	}
	return principal, fields, true
}

func (h Handler) conversationInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	conversationID := strings.TrimSpace(fields["channel"])
	if err != nil || conversationID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(conversationID))
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) userInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, requested)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": userResponse(user)})
}

func (h Handler) usersIdentity(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeIdentityBasic)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "invalid_auth")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	team, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "invalid_auth")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["email"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	user, err := h.Messages.UserByEmail(r.Context(), principal.WorkspaceID, principal.UserID, fields["email"])
	if err != nil {
		code, reason := mapServiceError(err, "users_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": userResponse(user)})
}

func (h Handler) usersList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	request, err := decodeListRequest(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	members := make([]map[string]any, 0, len(page.Users))
	for _, user := range page.Users {
		members = append(members, userResponse(user))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "members": members, "cache_ts": time.Now().Unix(), "response_metadata": map[string]any{"next_cursor": page.NextCursor}, "has_more": page.HasMore})
}

func decodeConversationListFields(fields map[string]string) (domain.ConversationListRequest, error) {
	limit := 100
	var err error
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			return domain.ConversationListRequest{}, errors.New("limit must be between 1 and 1000")
		}
	}
	cursor := domain.Cursor(strings.TrimSpace(fields["cursor"]))
	if _, err := domain.DecodeListCursor(cursor); err != nil {
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
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	requested := domain.UserID(strings.TrimSpace(fields["user"]))
	if requested == "" {
		requested = principal.UserID
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, requested)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": profileResponse(user)})
}

func (h Handler) getPresence(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	requested := domain.UserID(strings.TrimSpace(fields["user"]))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if requested == "" {
		requested = principal.UserID
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, requested)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	presence := domain.Presence(strings.TrimSpace(fields["presence"]))
	if err != nil || (presence != domain.PresenceAuto && presence != domain.PresenceAway) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_presence"})
		return
	}
	if _, err := h.Messages.SetUserPresence(r.Context(), principal.WorkspaceID, principal.UserID, presence); err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	requested := domain.UserID(strings.TrimSpace(fields["user"]))
	value, err := h.Messages.DoNotDisturbInfo(r.Context(), principal.WorkspaceID, principal.UserID, requested)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err := h.Messages.EndDND(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		code, reason := mapServiceError(err, "dnd_not_active")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	value, err := h.Messages.EndSnooze(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "dnd_not_active")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	minutes, err := strconv.ParseInt(strings.TrimSpace(fields["num_minutes"]), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.SetSnooze(r.Context(), principal.WorkspaceID, principal.UserID, minutes)
	if err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	response := dndResponse(value, time.Now().UTC())
	writeJSON(w, http.StatusOK, response)
}

func (h Handler) dndTeamInfo(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeDNDRead)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	requested := make([]domain.UserID, 0)
	seen := make(map[domain.UserID]struct{})
	if raw := strings.TrimSpace(fields["users"]); raw != "" {
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
				return
			}
			userID := domain.UserID(item)
			if _, exists := seen[userID]; !exists {
				seen[userID] = struct{}{}
				requested = append(requested, userID)
			}
		}
	} else {
		page, listErr := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 1000})
		if listErr != nil {
			code, reason := mapServiceError(listErr, "team_not_found")
			writeJSON(w, code, map[string]any{"ok": false, "error": reason})
			return
		}
		for _, user := range page.Users {
			if _, exists := seen[user.ID]; !exists {
				seen[user.ID] = struct{}{}
				requested = append(requested, user.ID)
			}
		}
	}
	sort.Slice(requested, func(left, right int) bool { return requested[left] < requested[right] })
	users := make(map[string]any, len(requested))
	now := time.Now().UTC()
	for _, requestedID := range requested {
		value, infoErr := h.Messages.DoNotDisturbInfo(r.Context(), principal.WorkspaceID, principal.UserID, requestedID)
		if infoErr != nil {
			code, reason := mapServiceError(infoErr, "user_not_found")
			writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["profile"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	profileFields, err := decodeProfileJSON(fields["profile"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	profile := current.Profile
	for name, value := range profileFields {
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
	user, err := h.Messages.SetUserProfile(r.Context(), principal.WorkspaceID, principal.UserID, profile)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profile": userResponse(user)["profile"]})
}

func (h Handler) deleteUserPhoto(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersProfileWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := h.Messages.DeleteUserPhoto(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) setUserPhoto(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersProfileWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	temporary, _, _, mimeType, err := spoolUpload(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	stat, err := temporary.Stat()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "upload_failed"})
		return
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "upload_failed"})
		return
	}
	// slack-api-client 1.49.0 labels this multipart part imageData/*.
	// Preserve that official client behavior while applying the image contract
	// enforced by the message service.
	if mimeType == "imageData/*" {
		mimeType = "image/png"
	}
	user, err := h.Messages.SetUserPhoto(r.Context(), principal.WorkspaceID, principal.UserID, mimeType, stat.Size(), temporary)
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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

func userResponse(user domain.User) map[string]any {
	return map[string]any{
		"id": user.ID, "team_id": user.WorkspaceID, "name": user.Name, "real_name": user.RealName, "deleted": user.Deleted, "profile": profileResponse(user),
	}
}

func profileResponse(user domain.User) map[string]any {
	return map[string]any{
		"display_name": user.Profile.DisplayName, "display_name_normalized": user.Profile.DisplayName, "email": user.Email,
		"real_name": user.RealName, "real_name_normalized": user.RealName,
		"status_text": user.Profile.StatusText, "status_emoji": user.Profile.StatusEmoji,
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	request, err := decodeConversationListFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if allowMember {
		request.MemberUserID = domain.UserID(strings.TrimSpace(fields["user"]))
	}
	page, err := h.Messages.Conversations(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.ConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, channel, request)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["name"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	private, err := parseBoolField(fields["is_private"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.CreateConversation(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], private)
	if err != nil {
		code, reason := mapServiceError(err, "name_taken")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) joinConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	conversationID := strings.TrimSpace(fields["channel"])
	if err != nil || conversationID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.JoinConversation(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(conversationID))
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) inviteConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if err != nil || channel == "" || strings.TrimSpace(fields["users"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	users := make([]domain.UserID, 0)
	seen := make(map[domain.UserID]struct{})
	for _, raw := range strings.Split(fields["users"], ",") {
		user := domain.UserID(strings.TrimSpace(raw))
		if user == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
		if _, exists := seen[user]; exists {
			continue
		}
		seen[user] = struct{}{}
		users = append(users, user)
	}
	conversation, err := h.Messages.InviteConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, channel, users)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) leaveConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil || strings.TrimSpace(fields["channel"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if err := h.Messages.LeaveConversation(r.Context(), principal.WorkspaceID, principal.UserID, conversation); err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	target := domain.UserID(strings.TrimSpace(fields["user"]))
	if err != nil || channel == "" || target == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.KickConversationMember(r.Context(), principal.WorkspaceID, principal.UserID, channel, target); err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	name := fields["name"]
	if err != nil || channel == "" || strings.TrimSpace(name) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.RenameConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel, name)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if err != nil || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if _, present := fields["topic"]; !present {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.SetConversationTopic(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["topic"])
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if err != nil || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if _, present := fields["purpose"]; !present {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.SetConversationPurpose(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["purpose"])
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) archiveConversation(w http.ResponseWriter, r *http.Request) {
	h.setConversationArchived(w, r, true)
}

func (h Handler) unarchiveConversation(w http.ResponseWriter, r *http.Request) {
	h.setConversationArchived(w, r, false)
}

func (h Handler) closeConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if err != nil || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	if !conversation.IsDirect && !conversation.IsGroupDirect {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "method_not_supported_for_channel_type"})
		return
	}
	if err := h.Messages.LeaveConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel); err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) setConversationArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if err != nil || channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	_, err = h.Messages.SetConversationArchived(r.Context(), principal.WorkspaceID, principal.UserID, channel, archived)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) openConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil || strings.TrimSpace(fields["users"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	users := make([]domain.UserID, 0)
	seen := make(map[domain.UserID]struct{})
	for _, raw := range strings.Split(fields["users"], ",") {
		user := domain.UserID(strings.TrimSpace(raw))
		if user == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
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
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": conversationResponse(conversation)})
}

func (h Handler) markConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	channel, timestamp := strings.TrimSpace(fields["channel"]), strings.TrimSpace(fields["ts"])
	if err != nil || channel == "" || timestamp == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	cursor, err := h.Messages.MarkRead(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(channel), domain.MessageTimestamp(timestamp))
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel, timestamp, name, err := normalizeReactionFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.AddReaction(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp, name); err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel, timestamp, name, err := normalizeReactionFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RemoveReaction(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp, name); err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	limit := 200
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
	}
	cursor := domain.Cursor(strings.TrimSpace(fields["cursor"]))
	reactions, next, hasMore, err := h.Messages.Reactions(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp, domain.PageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	requested := strings.TrimSpace(fields["user"])
	if requested != "" && requested != string(principal.UserID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "not_authorized"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.UserReactions(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	bookmark, err := h.Messages.AddBookmark(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["title"], fields["type"], fields["link"], fields["emoji"], fields["entity_id"], fields["access_level"], fields["parent_id"])
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	id := domain.BookmarkID(strings.TrimSpace(fields["bookmark_id"]))
	if channel == "" || id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	_, titleSet := fields["title"]
	_, linkSet := fields["link"]
	_, emojiSet := fields["emoji"]
	bookmark, err := h.Messages.EditBookmark(r.Context(), principal.WorkspaceID, principal.UserID, channel, id, domain.BookmarkUpdate{Title: fields["title"], Link: fields["link"], Emoji: fields["emoji"], SetTitle: titleSet, SetLink: linkSet, SetEmoji: emojiSet})
	if err != nil {
		code, reason := mapServiceError(err, "bookmark_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	if channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	bookmarks, err := h.Messages.Bookmarks(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel_id"]))
	id := domain.BookmarkID(strings.TrimSpace(fields["bookmark_id"]))
	if channel == "" || id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RemoveBookmark(r.Context(), principal.WorkspaceID, principal.UserID, channel, id); err != nil {
		code, reason := mapServiceError(err, "bookmark_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.AddPin(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RemovePin(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	if channel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	limit := 100
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
	}
	pins, next, hasMore, err := h.Messages.Pins(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.PageRequest{Limit: limit, Cursor: domain.Cursor(strings.TrimSpace(fields["cursor"]))})
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil || strings.TrimSpace(fields["file"]) != "" || strings.TrimSpace(fields["file_comment"]) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.AddStar(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel, timestamp, err := normalizeReactionTarget(fields)
	if err != nil || strings.TrimSpace(fields["file"]) != "" || strings.TrimSpace(fields["file_comment"]) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RemoveStar(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	limit := 100
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
	}
	items, _, more, err := h.Messages.Stars(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: limit, Cursor: domain.Cursor(strings.TrimSpace(fields["cursor"]))})
	if err != nil {
		code, reason := mapServiceError(err, "stars_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, map[string]any{"type": "message", "channel": item.Conversation, "date_create": item.CreatedAt.Unix(), "message": messageResponse(item.Message)})
	}
	paging := map[string]any{"page": 1, "total": len(result), "per_page": limit}
	if more {
		paging["spill"] = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": result, "paging": paging})
}

func reminderResponse(reminder domain.Reminder) map[string]any {
	response := map[string]any{"id": reminder.ID, "creator": reminder.Creator, "user": reminder.User, "text": reminder.Text, "time": reminder.Time.Unix(), "recurring": reminder.Recurring}
	if !reminder.CompleteAt.IsZero() {
		response["complete_ts"] = reminder.CompleteAt.Unix()
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	canvas, err := h.Messages.CreateCanvas(r.Context(), principal.WorkspaceID, principal.UserID, fields["title"], fields["document_content"], domain.ConversationID(strings.TrimSpace(fields["channel_id"])))
	if err != nil {
		code, reason := mapServiceError(err, "canvas_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" || strings.TrimSpace(fields["changes"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.EditCanvas(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"])), fields["changes"]); err != nil {
		code, reason := mapServiceError(err, "canvas_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.DeleteCanvas(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"]))); err != nil {
		code, reason := mapServiceError(err, "canvas_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" || strings.TrimSpace(fields["access_level"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.SetCanvasAccess(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"])), strings.TrimSpace(fields["access_level"]), parseConversationIDs(fields["channel_ids"]), parseCanvasUsers(fields["user_ids"])); err != nil {
		code, reason := mapServiceError(err, "canvas_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.DeleteCanvasAccess(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"])), parseConversationIDs(fields["channel_ids"]), parseCanvasUsers(fields["user_ids"])); err != nil {
		code, reason := mapServiceError(err, "canvas_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if strings.TrimSpace(fields["canvas_id"]) == "" || strings.TrimSpace(fields["criteria"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	sections, err := h.Messages.LookupCanvasSections(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(fields["canvas_id"])), fields["criteria"])
	if err != nil {
		code, reason := mapServiceError(err, "canvas_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	result := make([]map[string]string, 0, len(sections))
	for _, section := range sections {
		result = append(result, map[string]string{"id": section.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sections": result})
}

func parseCanvasUsers(raw string) []domain.UserID {
	values := strings.Split(raw, ",")
	result := make([]domain.UserID, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, domain.UserID(value))
		}
	}
	return result
}

func (h Handler) addReminder(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemindersWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	textValue := strings.TrimSpace(fields["text"])
	seconds, err := strconv.ParseInt(strings.TrimSpace(fields["time"]), 10, 64)
	if textValue == "" || err != nil || seconds <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	reminder, err := h.Messages.AddReminder(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserID(strings.TrimSpace(fields["user"])), textValue, time.Unix(seconds, 0).UTC())
	if err != nil {
		code, reason := mapServiceError(err, "user_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reminder": reminderResponse(reminder)})
}

func reminderIDFields(w http.ResponseWriter, r *http.Request) (map[string]string, domain.ReminderID, bool) {
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return nil, "", false
	}
	id := domain.ReminderID(strings.TrimSpace(fields["reminder"]))
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
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
		code, reason := mapServiceError(err, "reminder_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		code, reason := mapServiceError(err, "reminder_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		code, reason := mapServiceError(err, "reminder_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.Reminders(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		code, reason := mapServiceError(err, "reminders_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	query := strings.TrimSpace(fields["query"])
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	limit := 100
	if raw := strings.TrimSpace(fields["count"]); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
	}
	cursor := domain.Cursor(strings.TrimSpace(fields["cursor"]))
	if cursor != "" {
		if _, _, err := domain.DecodeMessageCursor(cursor); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
	}
	page, err := h.Messages.Search(r.Context(), principal.WorkspaceID, principal.UserID, query, domain.PageRequest{Limit: limit, Cursor: cursor})
	if err != nil {
		code, reason := mapServiceError(err, "search_failed")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	matches := make([]map[string]any, 0, len(page.Messages))
	for _, message := range page.Messages {
		match := messageResponse(message)
		match["channel_id"] = message.Conversation
		matches = append(matches, match)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "query": query, "messages": map[string]any{"matches": matches, "total": len(matches), "pagination": map[string]any{"page": 1, "per_page": limit, "page_count": 1, "total_count": len(matches)}}, "has_more": page.HasMore, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}

func (h Handler) remoteFileAdd(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeRemoteFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil || strings.TrimSpace(fields["external_id"]) == "" || strings.TrimSpace(fields["title"]) == "" || strings.TrimSpace(fields["external_url"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.AddRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID, domain.RemoteFile{ExternalID: fields["external_id"], Title: fields["title"], FileType: fields["filetype"], ExternalURL: fields["external_url"], PreviewImage: fields["preview_image"], IndexableContents: fields["indexable_file_contents"]})
	if err != nil {
		code, reason := mapServiceError(err, "remote_file_not_found")
		if errors.Is(err, store.ErrAlreadyExists) {
			code, reason = http.StatusBadRequest, "remote_file_already_exists"
		}
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	lookup, lookupErr := remoteFileLookup(fields)
	if err != nil || lookupErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.RemoteFileInfo(r.Context(), principal.WorkspaceID, principal.UserID, lookup)
	if err != nil {
		code, reason := mapServiceError(err, "remote_file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.RemoteFiles(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		code, reason := mapServiceError(err, "remote_files_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	lookup, lookupErr := remoteFileLookup(fields)
	if err != nil || lookupErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RemoveRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID, lookup); err != nil {
		code, reason := mapServiceError(err, "remote_file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	lookup, lookupErr := remoteFileLookup(fields)
	channels := parseConversationIDs(fields["channels"])
	if err != nil || lookupErr != nil || len(channels) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.ShareRemoteFile(r.Context(), principal.WorkspaceID, principal.UserID, lookup, channels)
	if err != nil {
		code, reason := mapServiceError(err, "remote_file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	lookup, lookupErr := remoteFileLookup(fields)
	if err != nil || lookupErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
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
		code, reason := mapServiceError(err, "remote_file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	if fileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	file, err := h.Messages.FileInfo(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		code, reason := mapServiceError(err, "file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	if fileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.DeleteFile(r.Context(), principal.WorkspaceID, principal.UserID, fileID); err != nil {
		code, reason := mapServiceError(err, "file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	commentID := domain.FileCommentID(strings.TrimSpace(fields["id"]))
	if err != nil || fileID == "" || commentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.DeleteFileComment(r.Context(), principal.WorkspaceID, principal.UserID, fileID, commentID); err != nil {
		code, reason := mapServiceError(err, "comment_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	if fileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	file, err := h.Messages.ShareFilePublic(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		code, reason := mapServiceError(err, "file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	fileID := domain.FileID(strings.TrimSpace(fields["file"]))
	if fileID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	file, err := h.Messages.RevokeFilePublic(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		code, reason := mapServiceError(err, "file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	request, err := decodeListRequest(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.Files(r.Context(), principal.WorkspaceID, principal.UserID, request)
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	files := make([]map[string]any, 0, len(page.Files))
	for _, file := range page.Files {
		files = append(files, fileResponse(file))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "files": files, "paging": map[string]any{"count": len(files), "total": len(files), "page": 1, "pages": 1}, "has_more": page.HasMore, "response_metadata": map[string]string{"next_cursor": string(page.NextCursor)}})
}

const maxUploadBytes = 100 << 20

func (h Handler) fileUpload(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	temporary, fields, filename, mimeType, err := spoolUpload(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Close(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "upload_failed"})
		return
	}
	source, err := os.Open(temporary.Name())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "upload_failed"})
		return
	}
	defer source.Close()
	stat, err := source.Stat()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "upload_failed"})
		return
	}
	title := strings.TrimSpace(fields["title"])
	if title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	file, err := h.Messages.UploadFile(r.Context(), principal.WorkspaceID, principal.UserID, filename, title, mimeType, stat.Size(), source)
	if err != nil {
		code, reason := mapServiceError(err, "file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	file, source, err := h.Messages.OpenFile(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		code, reason := mapServiceError(err, "file_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	defer source.Close()
	w.Header().Set("Content-Type", file.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(file.Name))
	_, _ = io.Copy(w, source)
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
	w.Header().Set("Content-Type", file.MIMEType)
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(file.Name))
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
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = io.Copy(w, source)
}

func spoolUpload(w http.ResponseWriter, r *http.Request) (*os.File, map[string]string, string, string, error) {
	temporary, err := os.CreateTemp("", "sameoldchat-upload-*")
	if err != nil {
		return nil, nil, "", "", err
	}
	cleanup := func(uploadErr error) (*os.File, map[string]string, string, string, error) {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return nil, nil, "", "", uploadErr
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
	for _, unsupported := range []string{"initial_comment", "channels", "thread_ts"} {
		if strings.TrimSpace(fields[unsupported]) != "" {
			return cleanup(errors.New("file sharing fields are not supported"))
		}
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		return cleanup(err)
	}
	return temporary, fields, filename, mimeType, nil
}

func readUploadField(part *multipart.Part) (string, error) {
	value, err := io.ReadAll(io.LimitReader(part, 1<<20+1))
	if err != nil {
		return "", err
	}
	if len(value) > 1<<20 {
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
	result := map[string]any{
		"id": file.ID, "name": file.Name, "title": file.Title, "mimetype": file.MIMEType,
		"size": file.Size, "created": file.CreatedAt.Unix(), "timestamp": file.CreatedAt.Unix(),
		"user": file.Uploader, "is_public": file.PublicToken != "", "team_id": file.WorkspaceID,
	}
	if file.PublicToken != "" {
		result["permalink_public"] = "/files/public/" + file.PublicToken
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
		return "", "", "", errors.New("name is required")
	}
	return channel, timestamp, name, nil
}

func normalizeReactionTarget(fields map[string]string) (domain.ConversationID, domain.MessageTimestamp, error) {
	channel := strings.TrimSpace(fields["channel"])
	timestamp := strings.TrimSpace(fields["timestamp"])
	if channel == "" || timestamp == "" {
		return "", "", errors.New("channel and timestamp are required")
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

func decodeListRequest(w http.ResponseWriter, r *http.Request) (domain.PageRequest, error) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return domain.PageRequest{}, err
	}
	return decodeListRequestFields(fields)
}

func decodeListRequestFields(fields map[string]string) (domain.PageRequest, error) {
	limit := 100
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		parsed, err := strconv.Atoi(raw)
		limit = parsed
		if err != nil || limit < 1 || limit > 200 {
			return domain.PageRequest{}, errors.New("limit must be between 1 and 200")
		}
	}
	cursor := domain.Cursor(strings.TrimSpace(fields["cursor"]))
	if _, err := domain.DecodeListCursor(cursor); err != nil {
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	message, err := h.postMessageValue(r, principal, fields)
	if err != nil {
		code, reason := postMessageError(err)
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	ts := slackTimestamp(message.CreatedAt)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": message.Conversation, "ts": ts, "message": messageResponse(message)})
}

func (h Handler) chatUnfurl(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	var rawUnfurls map[string]json.RawMessage
	if strings.TrimSpace(fields["unfurls"]) == "" || json.Unmarshal([]byte(fields["unfurls"]), &rawUnfurls) != nil || rawUnfurls == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	unfurls := make(map[string]string, len(rawUnfurls))
	for key, raw := range rawUnfurls {
		unfurls[key] = string(raw)
	}
	message, err := h.Messages.Unfurl(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel"])), domain.MessageTimestamp(strings.TrimSpace(fields["ts"])), unfurls)
	if err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	message, err := h.postMessageValue(r, principal, fields)
	if err != nil {
		code, reason := postMessageError(err)
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	if strings.TrimSpace(fields["attachments"]) != "" || strings.TrimSpace(fields["blocks"]) != "" || strings.TrimSpace(fields["channel"]) == "" || strings.TrimSpace(fields["user"]) == "" || strings.TrimSpace(fields["text"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.PostEphemeral(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel"])), domain.UserID(strings.TrimSpace(fields["user"])), fields["text"])
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message_ts": value.Timestamp})
}

func (h Handler) postMessageValue(r *http.Request, principal auth.Principal, fields map[string]string) (domain.Message, error) {
	return h.Messages.Post(
		r.Context(),
		principal.WorkspaceID,
		principal.UserID,
		domain.ConversationID(strings.TrimSpace(fields["channel"])),
		fields["text"],
		domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"])),
		strings.TrimSpace(r.Header.Get("Idempotency-Key")),
	)
}

func postMessageError(err error) (int, string) {
	reason := "service_unavailable"
	if errors.Is(err, service.ErrInvalidMessage) {
		reason = "invalid_arguments"
	}
	if errors.Is(err, store.ErrNotFound) {
		reason = "channel_not_found"
	}
	return mapServiceError(err, reason)
}

func (h Handler) updateMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	conversation, timestamp, text := strings.TrimSpace(fields["channel"]), strings.TrimSpace(fields["ts"]), fields["text"]
	if conversation == "" || timestamp == "" || strings.TrimSpace(text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	message, err := h.Messages.Update(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(conversation), domain.MessageTimestamp(timestamp), text)
	if err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	ts := slackTimestamp(message.CreatedAt)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": message.Conversation, "ts": ts, "text": message.Text, "message": messageResponse(message)})
}

func (h Handler) deleteMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	conversation, timestamp := strings.TrimSpace(fields["channel"]), strings.TrimSpace(fields["ts"])
	if conversation == "" || timestamp == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	message, err := h.Messages.Delete(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(conversation), domain.MessageTimestamp(timestamp))
	if err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": message.Conversation, "ts": slackTimestamp(message.CreatedAt)})
}

func scheduledMessageResponse(value domain.ScheduledMessage) map[string]any {
	return map[string]any{"id": value.ID, "channel_id": value.Channel, "post_at": value.PostAt.Unix(), "date_created": value.CreatedAt.Unix(), "text": value.Text}
}

func (h Handler) scheduleMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	textValue := strings.TrimSpace(fields["text"])
	postAt, err := strconv.ParseInt(strings.TrimSpace(fields["post_at"]), 10, 64)
	unsupportedBoolean := func(name string) bool {
		raw := strings.TrimSpace(fields[name])
		if raw == "" {
			return false
		}
		value, parseErr := parseBoolField(raw)
		return parseErr != nil || value
	}
	unsupported := fields["attachments"] != "" || fields["blocks"] != "" || fields["thread_ts"] != "" || fields["parse"] != "" || unsupportedBoolean("reply_broadcast") || unsupportedBoolean("as_user") || unsupportedBoolean("link_names") || unsupportedBoolean("unfurl_links") || unsupportedBoolean("unfurl_media")
	if channel == "" || textValue == "" || err != nil || postAt <= 0 || unsupported {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.ScheduleMessage(r.Context(), principal.WorkspaceID, principal.UserID, channel, textValue, time.Unix(postAt, 0).UTC())
	if err != nil {
		code, reason := mapServiceError(err, "channel_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": value.Channel, "post_at": value.PostAt.Unix(), "scheduled_message_id": value.ID, "message": map[string]any{"type": "message", "text": value.Text, "user": principal.UserID, "team": principal.WorkspaceID}})
}

func (h Handler) scheduledMessagesList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	limit := 100
	if raw := strings.TrimSpace(fields["limit"]); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 1000 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
	}
	page, err := h.Messages.ScheduledMessages(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationID(strings.TrimSpace(fields["channel"])), domain.PageRequest{Limit: limit, Cursor: domain.Cursor(strings.TrimSpace(fields["cursor"]))})
	if err != nil {
		code, reason := mapServiceError(err, "scheduled_messages_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	id := domain.ScheduledMessageID(strings.TrimSpace(fields["scheduled_message_id"]))
	if channel == "" || id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.DeleteScheduledMessage(r.Context(), principal.WorkspaceID, principal.UserID, channel, id); err != nil {
		code, reason := mapServiceError(err, "scheduled_message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func userGroupResponse(value domain.UserGroup, includeUsers bool) map[string]any {
	users := make([]string, 0, len(value.Users))
	for _, user := range value.Users {
		users = append(users, string(user))
	}
	result := map[string]any{"id": value.ID, "team_id": value.WorkspaceID, "is_usergroup": true, "is_subteam": false, "name": value.Name, "description": value.Description, "handle": value.Handle, "is_external": false, "date_create": value.CreatedAt.Unix(), "date_update": value.UpdatedAt.Unix(), "date_delete": int64(0), "auto_provision": false, "enterprise_subteam_id": "", "created_by": value.Creator, "updated_by": value.UpdatedBy, "user_count": len(users)}
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	value, err := operation(principal, fields)
	if err != nil {
		code, reason := mapServiceError(err, "usergroup_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.ListUserGroups(r.Context(), principal.WorkspaceID, principal.UserID, fields["include_disabled"] == "true", request)
	if err != nil {
		code, reason := mapServiceError(err, "usergroups_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	items := make([]map[string]any, 0, len(page.Groups))
	for _, value := range page.Groups {
		items = append(items, userGroupResponse(value, fields["include_users"] == "true"))
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	values, err := h.Messages.UserGroupUsers(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(strings.TrimSpace(fields["usergroup"])))
	if err != nil {
		code, reason := mapServiceError(err, "usergroup_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	raw := strings.Split(fields["users"], ",")
	users := make([]domain.UserID, 0, len(raw))
	for _, value := range raw {
		if strings.TrimSpace(value) != "" {
			users = append(users, domain.UserID(strings.TrimSpace(value)))
		}
	}
	value, err := h.Messages.SetUserGroupUsers(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(strings.TrimSpace(fields["usergroup"])), users)
	if err != nil {
		code, reason := mapServiceError(err, "usergroup_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "usergroup": userGroupResponse(value, true)})
}

func parseConversationIDs(raw string) []domain.ConversationID {
	if strings.HasPrefix(strings.TrimSpace(raw), "[") {
		var values []string
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return nil
		}
		result := make([]domain.ConversationID, 0, len(values))
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				result = append(result, domain.ConversationID(value))
			}
		}
		return result
	}
	parts := strings.Split(raw, ",")
	result := make([]domain.ConversationID, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, domain.ConversationID(value))
		}
	}
	return result
}

func (h Handler) adminUserGroupAddChannels(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeAdminUserGroupsWrite)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channels := parseConversationIDs(fields["channel_ids"])
	groupID := strings.TrimSpace(fields["usergroup"])
	if groupID == "" {
		groupID = strings.TrimSpace(fields["usergroup_id"])
	}
	if len(channels) == 0 || groupID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.AddUserGroupChannels(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(groupID), channels); err != nil {
		code, reason := mapServiceError(err, "usergroup_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.AdminAddUserGroupTeams(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(groupID), teams); err != nil {
		code, reason := mapServiceError(err, "usergroup_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	channels := parseConversationIDs(fields["channel_ids"])
	groupID := strings.TrimSpace(fields["usergroup"])
	if groupID == "" {
		groupID = strings.TrimSpace(fields["usergroup_id"])
	}
	if len(channels) == 0 || groupID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.RemoveUserGroupChannels(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(groupID), channels); err != nil {
		code, reason := mapServiceError(err, "usergroup_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	groupID := strings.TrimSpace(fields["usergroup"])
	if groupID == "" {
		groupID = strings.TrimSpace(fields["usergroup_id"])
	}
	if groupID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	channels, err := h.Messages.UserGroupChannels(r.Context(), principal.WorkspaceID, principal.UserID, domain.UserGroupID(groupID))
	if err != nil {
		code, reason := mapServiceError(err, "usergroup_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["name"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.AdminSetWorkspaceName(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"])
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.AdminSetWorkspaceDescription(r.Context(), principal.WorkspaceID, principal.UserID, fields["description"])
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.AdminSetWorkspaceDiscoverability(r.Context(), principal.WorkspaceID, principal.UserID, domain.WorkspaceDiscoverability(strings.TrimSpace(fields["discoverability"])))
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["image_url"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.AdminSetWorkspaceIcon(r.Context(), principal.WorkspaceID, principal.UserID, fields["image_url"])
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	channels := parseConversationIDs(fields["channel_ids"])
	if len(channels) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.AdminSetWorkspaceDefaultChannels(r.Context(), principal.WorkspaceID, principal.UserID, channels)
	if err != nil {
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	discoverability := domain.WorkspaceDiscoverability(strings.TrimSpace(fields["team_discoverability"]))
	value, err := h.Messages.AdminCreateWorkspace(r.Context(), principal.WorkspaceID, principal.UserID, fields["team_domain"], fields["team_name"], fields["team_description"], discoverability)
	if err != nil {
		code, reason := mapServiceError(err, "team_creation_failed")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		code, reason := mapServiceError(err, "team_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	request, err := decodeListRequestFields(fields)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.AdminTeamUsers(r.Context(), principal.WorkspaceID, principal.UserID, role, request)
	if err != nil {
		code, reason := mapServiceError(err, "users_unavailable")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	started := time.Time{}
	if raw := strings.TrimSpace(fields["date_start"]); raw != "" {
		seconds, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || seconds <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
			return
		}
		started = time.Unix(seconds, 0).UTC()
	}
	value, err := h.Messages.AddCall(r.Context(), principal.WorkspaceID, principal.UserID, fields["external_unique_id"], fields["external_display_id"], fields["join_url"], fields["desktop_app_join_url"], fields["title"], started, parseCallUsers(fields["users"]))
	if err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	duration := int64(0)
	if strings.TrimSpace(fields["duration"]) != "" {
		duration, err = strconv.ParseInt(strings.TrimSpace(fields["duration"]), 10, 64)
	}
	if err != nil || strings.TrimSpace(fields["id"]) == "" || duration < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.EndCall(r.Context(), principal.WorkspaceID, principal.UserID, domain.CallID(strings.TrimSpace(fields["id"])), duration); err != nil {
		code, reason := mapServiceError(err, "call_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.GetCall(r.Context(), principal.WorkspaceID, principal.UserID, domain.CallID(strings.TrimSpace(fields["id"])))
	if err != nil {
		code, reason := mapServiceError(err, "call_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	value, err := h.Messages.UpdateCall(r.Context(), principal.WorkspaceID, principal.UserID, domain.CallID(strings.TrimSpace(fields["id"])), fields["title"], fields["join_url"], fields["desktop_app_join_url"])
	if err != nil {
		code, reason := mapServiceError(err, "call_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["id"]) == "" || strings.TrimSpace(fields["users"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
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
		code, reason := mapServiceError(err, "call_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	timestamp := domain.MessageTimestamp(strings.TrimSpace(fields["message_ts"]))
	if err != nil || channel == "" || timestamp == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	permalink, err := h.Messages.Permalink(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp)
	if err != nil {
		code, reason := mapServiceError(err, "message_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": channel, "permalink": permalink})
}

func messageResponse(message domain.Message) map[string]any {
	result := map[string]any{"type": "message", "user": message.AuthorID, "text": message.Text, "ts": slackTimestamp(message.CreatedAt), "thread_ts": message.ThreadTimestamp}
	if len(message.Unfurls) > 0 {
		unfurls := make(map[string]json.RawMessage, len(message.Unfurls))
		for key, raw := range message.Unfurls {
			unfurls[key] = json.RawMessage(raw)
		}
		result["unfurls"] = unfurls
	}
	return result
}

func mapServiceError(err error, notFoundReason string) (int, string) {
	if errors.Is(err, store.ErrNotFound) || status.Code(err) == codes.NotFound {
		return http.StatusNotFound, notFoundReason
	}
	if errors.Is(err, service.ErrInvalidMessage) || errors.Is(err, service.ErrInvalidTimestamp) || errors.Is(err, service.ErrInvalidConversation) || errors.Is(err, service.ErrInvalidReaction) || errors.Is(err, service.ErrInvalidFile) || errors.Is(err, service.ErrInvalidProfile) || errors.Is(err, service.ErrInvalidSnooze) || errors.Is(err, service.ErrInvalidCall) || errors.Is(err, service.ErrInvalidUserGroup) || errors.Is(err, service.ErrInvalidEphemeral) || errors.Is(err, service.ErrInvalidEmoji) || errors.Is(err, service.ErrInvalidView) || errors.Is(err, service.ErrInvalidDialog) || errors.Is(err, service.ErrInvalidBot) || errors.Is(err, service.ErrInvalidConversationPrefs) || errors.Is(err, service.ErrInvalidRemoteFile) || errors.Is(err, service.ErrInvalidInviteRequest) || errors.Is(err, service.ErrInvalidAppApproval) || errors.Is(err, service.ErrInvalidIntegrationLogs) || errors.Is(err, service.ErrInvalidOAuth) || errors.Is(err, service.ErrInvalidOAuthClient) || errors.Is(err, service.ErrInvalidBookmark) || errors.Is(err, store.ErrInvalidConversationType) || errors.Is(err, store.ErrInvalidAppApproval) || status.Code(err) == codes.InvalidArgument || errors.Is(err, service.ErrInvalidCanvas) {
		return http.StatusBadRequest, "invalid_arguments"
	}
	if errors.Is(err, service.ErrEmojiAlreadyExists) || status.Code(err) == codes.AlreadyExists {
		return http.StatusBadRequest, "emoji_already_exists"
	}
	if errors.Is(err, service.ErrMessageNotOwned) || status.Code(err) == codes.PermissionDenied {
		return http.StatusForbidden, "not_authorized"
	}
	if errors.Is(err, service.ErrMessageAlreadyDeleted) || status.Code(err) == codes.FailedPrecondition {
		return http.StatusBadRequest, "message_not_found"
	}
	if status.Code(err) == codes.Aborted {
		return http.StatusConflict, "hash_conflict"
	}
	if errors.Is(err, service.ErrInvalidPresence) {
		return http.StatusBadRequest, "invalid_presence"
	}
	if errors.Is(err, service.ErrBlobUnavailable) {
		return http.StatusServiceUnavailable, "file_storage_unavailable"
	}
	if errors.Is(err, store.ErrAlreadyExists) {
		return http.StatusBadRequest, "already_reacted"
	}
	if errors.Is(err, store.ErrConflict) {
		return http.StatusConflict, "hash_conflict"
	}
	if errors.Is(err, store.ErrBookmarkLimit) {
		return http.StatusBadRequest, "too_many_bookmarks"
	}
	return http.StatusServiceUnavailable, "service_unavailable"
}

func parseOptionalBooleans(fields map[string]string, names ...string) (bool, bool, bool, error) {
	if len(names) != 3 {
		return false, false, false, errors.New("three boolean fields are required")
	}
	values := make([]bool, len(names))
	for index, name := range names {
		raw := strings.TrimSpace(fields[name])
		if raw == "" {
			continue
		}
		switch strings.ToLower(raw) {
		case "1", "true":
			values[index] = true
		case "0", "false":
		default:
			return false, false, false, errors.New("invalid boolean")
		}
	}
	return values[0], values[1], values[2], nil
}

const maxRequestBody = 4 << 20

func decodeProfileJSON(raw string) (map[string]string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return nil, errors.New("profile must be a JSON object")
	}
	values := make(map[string]string, len(fields))
	for name, value := range fields {
		switch name {
		case "display_name", "status_text", "status_emoji", "image_24", "image_32", "image_48", "image_72", "image_192", "image_512", "image_1024":
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return nil, fmt.Errorf("profile field %s must be a string", name)
			}
			values[name] = text
		case "always_active", "is_custom_image":
			var boolean bool
			if err := json.Unmarshal(value, &boolean); err != nil {
				return nil, fmt.Errorf("profile field %s must be a boolean", name)
			}
		default:
			return nil, fmt.Errorf("unsupported profile field %s", name)
		}
	}
	if len(values) == 0 {
		return nil, errors.New("profile must contain at least one supported field")
	}
	return values, nil
}

func decodeFields(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	fields := make(map[string]string)
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	if contentType == "application/json" {
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxRequestBody))
		start, err := decoder.Token()
		if err == io.EOF {
			return fields, nil
		}
		if err != nil {
			return nil, err
		}
		if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
			return nil, errors.New("JSON request must be an object")
		}
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errors.New("JSON object field name is invalid")
			}
			if _, exists := seen[name]; exists {
				return nil, errors.New("request contains duplicate JSON field")
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
			return nil, errors.New("JSON request object is invalid")
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return nil, errors.New("request contains multiple JSON values")
			}
			return nil, err
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
		for name, values := range r.MultipartForm.Value {
			if len(values) == 0 {
				return nil, errors.New("form fields must occur once")
			}
			for _, value := range values[1:] {
				if value != values[0] {
					return nil, errors.New("form fields must not contain conflicting values")
				}
			}
			value, err := normalizeListFieldValue(name, values[0])
			if err != nil {
				return nil, err
			}
			fields[name] = value
		}
		return fields, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	for name, values := range r.Form {
		if len(values) == 0 {
			return nil, errors.New("form fields must occur once")
		}
		for _, value := range values[1:] {
			if value != values[0] {
				return nil, errors.New("form fields must not contain conflicting values")
			}
		}
		value, err := normalizeListFieldValue(name, values[0])
		if err != nil {
			return nil, err
		}
		fields[name] = value
	}
	return fields, nil
}

func normalizeJSONScalar(value json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		return text, nil
	}
	var scalar any
	if err := json.Unmarshal(value, &scalar); err != nil {
		return "", errors.New("request fields must be scalar values")
	}
	switch scalar := scalar.(type) {
	case bool:
		return strconv.FormatBool(scalar), nil
	case float64:
		return strconv.FormatFloat(scalar, 'f', -1, 64), nil
	default:
		return "", errors.New("request fields must be scalar values")
	}
}

func normalizeJSONField(name string, value json.RawMessage) (string, error) {
	if isListField(name) {
		return normalizeJSONListField(value)
	}
	if name != "profile" && name != "unfurls" && name != "metadata" && name != "user_auth_blocks" && name != "view" && name != "outputs" && name != "error" && name != "inputs" && name != "dialog" && name != "prefs" && name != "document_content" && name != "changes" && name != "criteria" {
		return normalizeJSONScalar(value)
	}
	if name == "unfurls" || name == "metadata" || name == "user_auth_blocks" || name == "view" || name == "outputs" || name == "error" || name == "inputs" || name == "dialog" || name == "prefs" || name == "document_content" || name == "changes" || name == "criteria" {
		var structured any
		if err := json.Unmarshal(value, &structured); err != nil || structured == nil {
			return "", fmt.Errorf("%s must be structured JSON", name)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, value); err != nil {
			return "", err
		}
		return compact.String(), nil
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(value, &profile); err != nil || profile == nil {
		return "", errors.New("profile must be a JSON object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, value); err != nil {
		return "", err
	}
	return compact.String(), nil
}

func isListField(name string) bool {
	switch name {
	case "channel_ids", "leaving_team_ids", "target_team_ids", "team_ids", "user_ids":
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
		return "", errors.New("list fields must be strings or arrays of strings")
	}
	var values []string
	if err := json.Unmarshal(value, &values); err == nil {
		for index, item := range values {
			values[index] = strings.TrimSpace(item)
			if values[index] == "" {
				return "", errors.New("list fields must contain non-empty strings")
			}
		}
		return strings.Join(values, ","), nil
	}
	var scalar string
	if err := json.Unmarshal(value, &scalar); err != nil {
		return "", errors.New("list fields must be strings or arrays of strings")
	}
	return scalar, nil
}

func slackTimestamp(value time.Time) string {
	return fmt.Sprintf("%d.%06d", value.Unix(), value.Nanosecond()/1000)
}

func (h Handler) authenticate(r *http.Request, scope auth.Scope) (auth.Principal, error) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		return auth.Principal{}, err
	}
	if scope != "" && !principal.HasScope(scope) {
		return auth.Principal{}, auth.ErrMissingScope
	}
	if err := h.Messages.RecordAccess(r.Context(), principal.WorkspaceID, principal.UserID, r.RemoteAddr, r.UserAgent()); err != nil {
		return auth.Principal{}, fmt.Errorf("%w: %v", errAccessLogging, err)
	}
	return principal, nil
}

func writeAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, errAccessLogging) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "access_logging_unavailable"})
		return
	}
	if errors.Is(err, auth.ErrMissingScope) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "missing_scope"})
		return
	}
	writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "not_authed"})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_form_data"})
		return
	}
	includeCopied, err := parseListBoolean(fields, "include_copied_list_records")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	todoMode, err := parseListBoolean(fields, "todo_mode")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.CreateList(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], fields["description_blocks"], fields["schema"], domain.ListID(strings.TrimSpace(fields["copy_from_list_id"])), includeCopied, todoMode)
	if err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	todoMode, err := parseListBoolean(fields, "todo_mode")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.UpdateList(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["id"])), fields["name"], fields["description_blocks"], todoMode, strings.TrimSpace(fields["todo_mode"]) != "")
	if err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["list_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.CreateListItem(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), domain.ListItemID(strings.TrimSpace(fields["parent_item_id"])), fields["initial_fields"])
	if err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["list_id"]) == "" || strings.TrimSpace(fields["id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.GetListItem(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), domain.ListItemID(strings.TrimSpace(fields["id"])))
	if err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["list_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	request, err := pageRequest(fields["limit"], fields["cursor"])
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	archived, err := parseListBoolean(fields, "archived")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	page, err := h.Messages.ListItems(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), request, archived)
	if err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["list_id"]) == "" || strings.TrimSpace(fields["cells"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	values, err := h.Messages.UpdateListCells(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), fields["cells"])
	if err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["list_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	ids := parseListItemIDs(fields["id"])
	if multiple {
		ids = parseListItemIDs(fields["ids"])
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	if err := h.Messages.DeleteListItems(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), ids); err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["list_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	channels := parseListConversationIDs(fields["channel_ids"])
	users := parseListUserIDs(fields["user_ids"])
	if set {
		err = h.Messages.SetListAccess(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), fields["access_level"], channels, users)
	} else {
		err = h.Messages.DeleteListAccess(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), channels, users)
	}
	if err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["list_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	includeArchived, err := parseListBoolean(fields, "include_archived")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.StartListDownload(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListID(strings.TrimSpace(fields["list_id"])), includeArchived)
	if err != nil {
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	download, err := h.Messages.GetListDownload(r.Context(), principal.WorkspaceID, principal.UserID, jobID)
	if err != nil || download.ListID != listID {
		if err == nil {
			err = store.ErrNotFound
		}
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s.csv", listID))
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
	if err != nil || strings.TrimSpace(fields["list_id"]) == "" || strings.TrimSpace(fields["job_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	value, err := h.Messages.GetListDownload(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListDownloadID(strings.TrimSpace(fields["job_id"])))
	if err != nil || value.ListID != domain.ListID(strings.TrimSpace(fields["list_id"])) {
		if err == nil {
			err = store.ErrNotFound
		}
		code, reason := mapServiceError(err, "list_not_found")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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

func parseListBoolean(fields map[string]string, name string) (bool, error) {
	value := strings.TrimSpace(fields[name])
	if value == "" {
		return false, nil
	}
	if value == "1" || strings.EqualFold(value, "true") {
		return true, nil
	}
	if value == "0" || strings.EqualFold(value, "false") {
		return false, nil
	}
	return false, errors.New("invalid boolean")
}

func pageRequest(limit, cursor string) (domain.PageRequest, error) {
	if strings.TrimSpace(limit) == "" {
		return domain.PageRequest{Limit: 100, Cursor: domain.Cursor(cursor)}, nil
	}
	value, err := strconv.Atoi(limit)
	if err != nil || value <= 0 || value > 1000 {
		return domain.PageRequest{}, err
	}
	return domain.PageRequest{Limit: value, Cursor: domain.Cursor(cursor)}, nil
}

func parseListItemIDs(raw string) []domain.ListItemID {
	parts := strings.Split(raw, ",")
	result := make([]domain.ListItemID, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, domain.ListItemID(value))
		}
	}
	return result
}

func parseListConversationIDs(raw string) []domain.ConversationID {
	parts := strings.Split(raw, ",")
	result := make([]domain.ConversationID, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, domain.ConversationID(value))
		}
	}
	return result
}

func parseListUserIDs(raw string) []domain.UserID {
	parts := strings.Split(raw, ",")
	result := make([]domain.UserID, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, domain.UserID(value))
		}
	}
	return result
}

func (h Handler) presentEntityDetails(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		writeAuthError(w, err)
		return
	}
	fields, err := decodeFields(w, r)
	if err != nil || strings.TrimSpace(fields["trigger_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	userAuthRequired, err := parseListBoolean(fields, "user_auth_required")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	err = h.Messages.PresentEntityDetails(r.Context(), principal.WorkspaceID, principal.UserID, fields["trigger_id"], fields["metadata"], userAuthRequired, fields["user_auth_url"], fields["error"])
	if err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["trigger_id"]) == "" || strings.TrimSpace(fields["comments"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	canPostComment, err := parseListBoolean(fields, "can_post_comment")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	userAuthRequired, err := parseListBoolean(fields, "user_auth_required")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	err = h.Messages.PresentEntityComments(r.Context(), principal.WorkspaceID, principal.UserID, fields["trigger_id"], fields["comments"], fields["cursor"], canPostComment, fields["delete_action_id"], userAuthRequired, fields["user_auth_url"], fields["error"])
	if err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
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
	if err != nil || strings.TrimSpace(fields["trigger_id"]) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_arguments"})
		return
	}
	err = h.Messages.AcknowledgeEntityCommentAction(r.Context(), principal.WorkspaceID, principal.UserID, fields["trigger_id"], fields["comment"], fields["error"])
	if err != nil {
		code, reason := mapServiceError(err, "invalid_arguments")
		writeJSON(w, code, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h Handler) openIDConnectToken(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	clientID, clientSecret := strings.TrimSpace(fields["client_id"]), strings.TrimSpace(fields["client_secret"])
	if basicID, basicSecret, ok := r.BasicAuth(); ok {
		if clientID != "" && clientID != basicID || clientSecret != "" && clientSecret != basicSecret {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_client"})
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
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "access_token": token.AccessToken, "token_type": token.TokenType, "id_token": token.IDToken, "refresh_token": token.RefreshToken})
}

func (h Handler) openIDConnectUserInfo(w http.ResponseWriter, r *http.Request) {
	fields, err := decodeFields(w, r)
	if err != nil {
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		token = strings.TrimSpace(fields["token"])
	}
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid_auth"})
		return
	}
	value, err := h.Messages.OpenIDConnectUserInfo(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid_auth"})
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
