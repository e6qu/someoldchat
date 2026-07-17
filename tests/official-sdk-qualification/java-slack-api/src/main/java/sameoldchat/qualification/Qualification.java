package sameoldchat.qualification;

import com.slack.api.Slack;
import com.slack.api.SlackConfig;
import com.slack.api.methods.MethodsClient;
import com.slack.api.methods.response.api.ApiTestResponse;
import com.slack.api.methods.response.auth.AuthTestResponse;
import com.slack.api.methods.response.chat.ChatPostMessageResponse;
import com.slack.api.methods.response.chat.ChatMeMessageResponse;
import com.slack.api.methods.response.chat.ChatGetPermalinkResponse;
import com.slack.api.methods.response.conversations.ConversationsHistoryResponse;
import com.slack.api.methods.response.conversations.ConversationsInfoResponse;
import com.slack.api.methods.response.conversations.ConversationsListResponse;
import com.slack.api.methods.response.conversations.ConversationsMembersResponse;
import com.slack.api.methods.response.conversations.ConversationsRepliesResponse;
import com.slack.api.methods.response.conversations.ConversationsCreateResponse;
import com.slack.api.methods.response.conversations.ConversationsRenameResponse;
import com.slack.api.methods.response.conversations.ConversationsSetTopicResponse;
import com.slack.api.methods.response.conversations.ConversationsSetPurposeResponse;
import com.slack.api.methods.response.conversations.ConversationsArchiveResponse;
import com.slack.api.methods.response.conversations.ConversationsUnarchiveResponse;
import com.slack.api.methods.response.conversations.ConversationsOpenResponse;
import com.slack.api.methods.response.conversations.ConversationsCloseResponse;
import com.slack.api.methods.response.conversations.ConversationsMarkResponse;
import com.slack.api.methods.response.users.UsersListResponse;
import com.slack.api.methods.response.users.UsersInfoResponse;
import com.slack.api.methods.response.users.profile.UsersProfileGetResponse;
import com.slack.api.methods.response.pins.PinsAddResponse;
import com.slack.api.methods.response.pins.PinsListResponse;
import com.slack.api.methods.response.pins.PinsRemoveResponse;
import com.slack.api.methods.response.reactions.ReactionsAddResponse;
import com.slack.api.methods.response.reactions.ReactionsGetResponse;
import com.slack.api.methods.response.reactions.ReactionsRemoveResponse;
import com.slack.api.methods.response.reactions.ReactionsListResponse;
import com.slack.api.methods.response.team.TeamInfoResponse;
import com.slack.api.methods.response.emoji.EmojiListResponse;
import com.slack.api.methods.response.users.UsersIdentityResponse;
import com.slack.api.methods.response.users.UsersConversationsResponse;
import com.slack.api.methods.response.users.UsersLookupByEmailResponse;
import com.slack.api.methods.response.users.UsersGetPresenceResponse;
import com.slack.api.methods.response.users.UsersSetPresenceResponse;
import com.slack.api.methods.response.users.profile.UsersProfileSetResponse;
import java.util.List;

public final class Qualification {
    private Qualification() {}

    public static void main(String[] args) throws Exception {
        String token = env("SAMEOLDCHAT_API_TOKEN", "xoxb-test");
        String baseUrl = env("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/");

        SlackConfig config = new SlackConfig();
        config.setMethodsEndpointUrlPrefix(baseUrl);
        config.setTokenExistenceVerificationEnabled(false);
        try (Slack slack = Slack.getInstance(config)) {
            MethodsClient methods = slack.methods(token);

            ApiTestResponse api = methods.apiTest(com.slack.api.methods.request.api.ApiTestRequest.builder().build());
            require(api.isOk(), "api.test failed: " + api.getError());

            AuthTestResponse auth = methods.authTest(com.slack.api.methods.request.auth.AuthTestRequest.builder().build());
            require(auth.isOk(), "auth.test failed: " + auth.getError());
            require("T1".equals(auth.getTeamId()), "auth.test team_id mismatch");
            require("U1".equals(auth.getUserId()), "auth.test user_id mismatch");
            com.slack.api.methods.response.bots.BotsInfoResponse bot = methods.botsInfo(
                    com.slack.api.methods.request.bots.BotsInfoRequest.builder().bot("B1").build());
            require(bot.isOk() && bot.getBot() != null && "B1".equals(bot.getBot().getId()),
                    "bots.info failed: " + bot.getError());
            com.slack.api.methods.response.team.TeamAccessLogsResponse accessLogs = methods.teamAccessLogs(
                    com.slack.api.methods.request.team.TeamAccessLogsRequest.builder().count(1).build());
            require(accessLogs.isOk() && accessLogs.getLogins() != null, "team.accessLogs failed: " + accessLogs.getError());
            com.slack.api.methods.response.team.TeamBillableInfoResponse billableInfo = methods.teamBillableInfo(
                    com.slack.api.methods.request.team.TeamBillableInfoRequest.builder().user("U1").build());
            require(billableInfo.isOk() && billableInfo.getBillableInfo() != null
                            && billableInfo.getBillableInfo().containsKey("U1"),
                    "team.billableInfo failed: " + billableInfo.getError());
            com.slack.api.methods.response.team.TeamIntegrationLogsResponse integrationLogs = methods.teamIntegrationLogs(
                    com.slack.api.methods.request.team.TeamIntegrationLogsRequest.builder().count(1).build());
            require(integrationLogs.isOk(), "team.integrationLogs failed: " + integrationLogs.getError());
            com.slack.api.methods.response.migration.MigrationExchangeResponse migration = methods.migrationExchange(
                    com.slack.api.methods.request.migration.MigrationExchangeRequest.builder()
                            .users(java.util.List.of("U1")).build());
            require(migration.isOk() && "W1".equals(migration.getUserIdMap().get("U1")),
                    "migration.exchange failed: " + migration.getError());
            com.slack.api.methods.response.migration.MigrationExchangeResponse reverseMigration = methods.migrationExchange(
                    com.slack.api.methods.request.migration.MigrationExchangeRequest.builder()
                            .users(java.util.List.of("W1")).toOld(true).build());
            require(reverseMigration.isOk() && "U1".equals(reverseMigration.getUserIdMap().get("W1")),
                    "migration.exchange reverse failed: " + reverseMigration.getError());
            com.slack.api.methods.response.oauth.OAuthAccessResponse oauth = methods.oauthAccess(
                    com.slack.api.methods.request.oauth.OAuthAccessRequest.builder()
                            .clientId("qualification-client")
                            .clientSecret("qualification-secret")
                            .code("qualification-code")
                            .redirectUri("https://example.com/oauth")
                            .build());
            require(oauth.isOk() && oauth.getAccessToken() != null, "oauth.access failed: " + oauth.getError());
            com.slack.api.methods.response.oauth.OAuthV2AccessResponse oauthV2 = methods.oauthV2Access(
                    com.slack.api.methods.request.oauth.OAuthV2AccessRequest.builder()
                            .clientId("qualification-client")
                            .clientSecret("qualification-secret")
                            .code("qualification-v2-code")
                            .redirectUri("https://example.com/oauth")
                            .build());
            require(oauthV2.isOk() && oauthV2.getAuthedUser() != null
                            && "U1".equals(oauthV2.getAuthedUser().getId())
                            && oauthV2.getAuthedUser().getAccessToken() != null,
                    "oauth.v2.access failed: " + oauthV2.getError());
            com.slack.api.methods.response.oauth.OAuthTokenResponse oauthToken = methods.oauthToken(
                    com.slack.api.methods.request.oauth.OAuthTokenRequest.builder()
                            .clientId("qualification-client")
                            .clientSecret("qualification-secret")
                            .code("qualification-token-code")
                            .redirectUri("https://example.com/oauth")
                            .build());
            require(oauthToken.isOk() && oauthToken.getAccessToken() != null,
                    "oauth.token failed: " + oauthToken.getError());
            com.slack.api.methods.response.apps.event.authorizations.AppsEventAuthorizationsListResponse authorizations = methods.appsEventAuthorizationsList(
                    com.slack.api.methods.request.apps.event.authorizations.AppsEventAuthorizationsListRequest.builder()
                            .eventContext("qualification-event").build());
            require(authorizations.isOk() && authorizations.getAuthorizations() != null
                            && !authorizations.getAuthorizations().isEmpty(),
                    "apps.event.authorizations.list failed: " + authorizations.getError());
            com.slack.api.methods.response.admin.users.AdminUsersListResponse adminUsers = methods.adminUsersList(
                    com.slack.api.methods.request.admin.users.AdminUsersListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(adminUsers.isOk() && adminUsers.getUsers() != null
                            && adminUsers.getUsers().stream().anyMatch(user -> "U1".equals(user.getId())),
                    "admin.users.list failed: " + adminUsers.getError());
            com.slack.api.methods.response.admin.emoji.AdminEmojiListResponse adminEmoji = methods.adminEmojiList(
                    com.slack.api.methods.request.admin.emoji.AdminEmojiListRequest.builder().build());
            require(adminEmoji.isOk() && adminEmoji.getEmoji() != null,
                    "admin.emoji.list failed: " + adminEmoji.getError());
            com.slack.api.methods.response.admin.teams.AdminTeamsListResponse adminTeams = methods.adminTeamsList(
                    com.slack.api.methods.request.admin.teams.AdminTeamsListRequest.builder().limit(10).build());
            require(adminTeams.isOk() && adminTeams.getTeams() != null
                            && adminTeams.getTeams().stream().anyMatch(team -> "T1".equals(team.getId())),
                    "admin.teams.list failed: " + adminTeams.getError());
            require(methods.adminEmojiAdd(
                    com.slack.api.methods.request.admin.emoji.AdminEmojiAddRequest.builder()
                            .name("qualified").url("https://example.com/qualified.png").build()).isOk(), "admin.emoji.add failed");
            require(methods.adminEmojiAddAlias(
                    com.slack.api.methods.request.admin.emoji.AdminEmojiAddAliasRequest.builder()
                            .name("qualified-alias").aliasFor("qualified").build()).isOk(), "admin.emoji.addAlias failed");
            require(methods.adminEmojiRename(
                    com.slack.api.methods.request.admin.emoji.AdminEmojiRenameRequest.builder()
                            .name("qualified").newName("qualified-renamed").build()).isOk(), "admin.emoji.rename failed");
            require(methods.adminEmojiRemove(
                    com.slack.api.methods.request.admin.emoji.AdminEmojiRemoveRequest.builder()
                            .name("qualified-alias").build()).isOk(), "admin.emoji.remove alias failed");
            require(methods.adminEmojiRemove(
                    com.slack.api.methods.request.admin.emoji.AdminEmojiRemoveRequest.builder()
                            .name("qualified-renamed").build()).isOk(), "admin.emoji.remove failed");
            require(methods.adminConversationsRename(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsRenameRequest.builder()
                            .channelId("C2").name("renamed-lifecycle").build()).isOk(), "admin.conversations.rename failed");
            require(methods.adminConversationsArchive(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsArchiveRequest.builder()
                            .channelId("C2").build()).isOk(), "admin.conversations.archive failed");
            require(methods.adminConversationsUnarchive(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsUnarchiveRequest.builder()
                            .channelId("C2").build()).isOk(), "admin.conversations.unarchive failed");
            com.slack.api.methods.response.admin.teams.AdminTeamsAdminsListResponse adminTeamAdmins = methods.adminTeamsAdminsList(
                    com.slack.api.methods.request.admin.teams.AdminTeamsAdminsListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(adminTeamAdmins.isOk() && adminTeamAdmins.getAdminIds() != null
                            && adminTeamAdmins.getAdminIds().contains("U2"),
                    "admin.teams.admins.list failed: " + adminTeamAdmins.getError());
            com.slack.api.methods.response.admin.teams.owners.AdminTeamsOwnersListResponse adminTeamOwners = methods.adminTeamsOwnersList(
                    com.slack.api.methods.request.admin.teams.owners.AdminTeamsOwnersListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(adminTeamOwners.isOk() && adminTeamOwners.getOwnerIds() != null
                            && adminTeamOwners.getOwnerIds().contains("U1"),
                    "admin.teams.owners.list failed: " + adminTeamOwners.getError());
            com.slack.api.methods.response.admin.teams.AdminTeamsCreateResponse createdAdminTeam = methods.adminTeamsCreate(
                    com.slack.api.methods.request.admin.teams.AdminTeamsCreateRequest.builder()
                            .teamDomain("sdk-created-workspace").teamName("SDK Created Workspace")
                            .teamDescription("created by SDK qualification").teamDiscoverability("closed").build());
            require(createdAdminTeam.isOk() && createdAdminTeam.getTeam() != null,
                    "admin.teams.create failed: " + createdAdminTeam.getError());
            com.slack.api.methods.response.admin.teams.settings.AdminTeamsSettingsInfoResponse adminTeamSettings = methods.adminTeamsSettingsInfo(
                    com.slack.api.methods.request.admin.teams.settings.AdminTeamsSettingsInfoRequest.builder()
                            .teamId("T1").build());
            require(adminTeamSettings.isOk() && adminTeamSettings.getTeam() != null
                            && "T1".equals(adminTeamSettings.getTeam().getId())
                            && "test".equals(adminTeamSettings.getTeam().getName()),
                    "admin.teams.settings.info failed: " + adminTeamSettings.getError());
            require(methods.adminUsersSetAdmin(
                    com.slack.api.methods.request.admin.users.AdminUsersSetAdminRequest.builder()
                            .teamId("T1").userId("U2").build()).isOk(), "admin.users.setAdmin failed");
            require(methods.adminUsersSetOwner(
                    com.slack.api.methods.request.admin.users.AdminUsersSetOwnerRequest.builder()
                            .teamId("T1").userId("U2").build()).isOk(), "admin.users.setOwner failed");
            require(methods.adminUsersSetRegular(
                    com.slack.api.methods.request.admin.users.AdminUsersSetRegularRequest.builder()
                            .teamId("T1").userId("U2").build()).isOk(), "admin.users.setRegular failed");
            require(methods.adminTeamsSettingsSetName(
                    com.slack.api.methods.request.admin.teams.settings.AdminTeamsSettingsSetNameRequest.builder()
                            .teamId("T1").name("qualified-test").build()).isOk(), "admin.teams.settings.setName failed");
            require(methods.adminTeamsSettingsSetDescription(
                    com.slack.api.methods.request.admin.teams.settings.AdminTeamsSettingsSetDescriptionRequest.builder()
                            .teamId("T1").description("qualified description").build()).isOk(), "admin.teams.settings.setDescription failed");
            require(methods.adminTeamsSettingsSetDiscoverability(
                    com.slack.api.methods.request.admin.teams.settings.AdminTeamsSettingsSetDiscoverabilityRequest.builder()
                            .teamId("T1").discoverability("closed").build()).isOk(), "admin.teams.settings.setDiscoverability failed");
            require(methods.adminTeamsSettingsSetIcon(
                    com.slack.api.methods.request.admin.teams.settings.AdminTeamsSettingsSetIconRequest.builder()
                            .teamId("T1").imageUrl("https://example.com/qualified.png").build()).isOk(), "admin.teams.settings.setIcon failed");
            require(methods.adminTeamsSettingsSetDefaultChannels(
                    com.slack.api.methods.request.admin.teams.settings.AdminTeamsSettingsSetDefaultChannelsRequest.builder()
                            .teamId("T1").channelIds(java.util.List.of("C1")).build()).isOk(), "admin.teams.settings.setDefaultChannels failed");
            com.slack.api.methods.response.admin.invite_requests.AdminInviteRequestsListResponse inviteRequests = methods.adminInviteRequestsList(
                    com.slack.api.methods.request.admin.invite_requests.AdminInviteRequestsListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(inviteRequests.isOk() && inviteRequests.getInviteRequests() != null,
                    "admin.inviteRequests.list failed: " + inviteRequests.getError());
            require(methods.adminUsersInvite(
                    com.slack.api.methods.request.admin.users.AdminUsersInviteRequest.builder()
                            .teamId("T1").email("sdk-approve@example.com").channelIds(java.util.List.of("C1"))
                            .isRestricted(false).isUltraRestricted(false).build()).isOk(),
                    "admin.users.invite for approval failed");
            require(methods.adminUsersInvite(
                    com.slack.api.methods.request.admin.users.AdminUsersInviteRequest.builder()
                            .teamId("T1").email("sdk-deny@example.com").channelIds(java.util.List.of("C1"))
                            .isRestricted(false).isUltraRestricted(false).build()).isOk(),
                    "admin.users.invite for denial failed");
            com.slack.api.methods.response.admin.invite_requests.AdminInviteRequestsListResponse pendingInviteRequests =
                    methods.adminInviteRequestsList(
                            com.slack.api.methods.request.admin.invite_requests.AdminInviteRequestsListRequest.builder()
                                    .teamId("T1").limit(10).build());
            String approvalRequestId = pendingInviteRequests.getInviteRequests().stream()
                    .filter(request -> "sdk-approve@example.com".equals(request.getEmail()))
                    .map(com.slack.api.model.admin.InviteRequest::getId).findFirst().orElse("");
            String denialRequestId = pendingInviteRequests.getInviteRequests().stream()
                    .filter(request -> "sdk-deny@example.com".equals(request.getEmail()))
                    .map(com.slack.api.model.admin.InviteRequest::getId).findFirst().orElse("");
            require(!approvalRequestId.isEmpty() && !denialRequestId.isEmpty(),
                    "admin.inviteRequests.list did not return created requests");
            require(methods.adminInviteRequestsApprove(
                    com.slack.api.methods.request.admin.invite_requests.AdminInviteRequestsApproveRequest.builder()
                            .teamId("T1").inviteRequestId(approvalRequestId).build()).isOk(),
                    "admin.inviteRequests.approve failed");
            require(methods.adminInviteRequestsDeny(
                    com.slack.api.methods.request.admin.invite_requests.AdminInviteRequestsDenyRequest.builder()
                            .teamId("T1").inviteRequestId(denialRequestId).build()).isOk(),
                    "admin.inviteRequests.deny failed");
            com.slack.api.methods.response.admin.invite_requests.AdminInviteRequestsApprovedListResponse approvedInviteRequests = methods.adminInviteRequestsApprovedList(
                    com.slack.api.methods.request.admin.invite_requests.AdminInviteRequestsApprovedListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(approvedInviteRequests.isOk() && approvedInviteRequests.getApprovedRequests() != null,
                    "admin.inviteRequests.approved.list failed: " + approvedInviteRequests.getError());
            com.slack.api.methods.response.admin.invite_requests.AdminInviteRequestsDeniedListResponse deniedInviteRequests = methods.adminInviteRequestsDeniedList(
                    com.slack.api.methods.request.admin.invite_requests.AdminInviteRequestsDeniedListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(deniedInviteRequests.isOk() && deniedInviteRequests.getDeniedRequests() != null,
                    "admin.inviteRequests.denied.list failed: " + deniedInviteRequests.getError());
            com.slack.api.methods.response.admin.apps.AdminAppsApprovedListResponse approvedApps = methods.adminAppsApprovedList(
                    com.slack.api.methods.request.admin.apps.AdminAppsApprovedListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(approvedApps.isOk() && approvedApps.getApprovedApps() != null,
                    "admin.apps.approved.list failed: " + approvedApps.getError());
            com.slack.api.methods.response.admin.apps.AdminAppsRequestsListResponse appRequests = methods.adminAppsRequestsList(
                    com.slack.api.methods.request.admin.apps.AdminAppsRequestsListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(appRequests.isOk() && appRequests.getAppRequests() != null,
                    "admin.apps.requests.list failed: " + appRequests.getError());
            com.slack.api.methods.response.admin.apps.AdminAppsRestrictedListResponse restrictedApps = methods.adminAppsRestrictedList(
                    com.slack.api.methods.request.admin.apps.AdminAppsRestrictedListRequest.builder()
                            .teamId("T1").limit(10).build());
            require(restrictedApps.isOk() && restrictedApps.getRestrictedApps() != null,
                    "admin.apps.restricted.list failed: " + restrictedApps.getError());
            require(methods.appsPermissionsInfo(
                    com.slack.api.methods.request.apps.permissions.AppsPermissionsInfoRequest.builder().build()).isOk(),
                    "apps.permissions.info failed");
            require(methods.appsPermissionsScopesList(
                    com.slack.api.methods.request.apps.permissions.scopes.AppsPermissionsScopesListRequest.builder().build()).isOk(),
                    "apps.permissions.scopes.list failed");
            require(methods.appsPermissionsResourcesList(
                    com.slack.api.methods.request.apps.permissions.resources.AppsPermissionsResourcesListRequest.builder()
                            .limit(10).build()).isOk(),
                    "apps.permissions.resources.list failed");
            require(methods.appsPermissionsUsersList(
                    com.slack.api.methods.request.apps.permissions.users.AppsPermissionsUsersListRequest.builder()
                            .limit(10).build()).isOk(),
                    "apps.permissions.users.list failed");
            require(methods.appsPermissionsRequest(
                    com.slack.api.methods.request.apps.permissions.AppsPermissionsRequestRequest.builder()
                            .scopes(java.util.List.of("channels:read")).triggerId("permission-trigger").build()).isOk(),
                    "apps.permissions.request failed");
            require(methods.appsPermissionsUsersRequest(
                    com.slack.api.methods.request.apps.permissions.users.AppsPermissionsUsersRequestRequest.builder()
                            .scopes(java.util.List.of("channels:read")).triggerId("permission-user-trigger")
                            .user("U1").build()).isOk(),
                    "apps.permissions.users.request failed");
            require(methods.adminAppsApprove(
                    com.slack.api.methods.request.admin.apps.AdminAppsApproveRequest.builder()
                            .appId("A1").teamId("T1").build()).isOk(), "admin.apps.approve failed");
            require(methods.adminAppsRestrict(
                    com.slack.api.methods.request.admin.apps.AdminAppsRestrictRequest.builder()
                            .appId("A1").teamId("T1").build()).isOk(), "admin.apps.restrict failed");
            require(methods.adminConversationsInvite(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsInviteRequest.builder()
                            .channelId("C2").userIds(java.util.List.of("U2")).build()).isOk(), "admin.conversations.invite failed");
            com.slack.api.methods.response.admin.conversations.AdminConversationsSearchResponse searchedConversations = methods.adminConversationsSearch(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsSearchRequest.builder()
                            .query("general").limit(10).build());
            require(searchedConversations.isOk() && searchedConversations.getConversations() != null
                            && searchedConversations.getConversations().stream().anyMatch(conversation -> "C1".equals(conversation.getId())),
                    "admin.conversations.search failed: " + searchedConversations.getError());
            require(methods.adminConversationsSetConversationPrefs(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsSetConversationPrefsRequest.builder()
                            .channelId("C1").prefsAsString("{\"can_thread\":{\"type\":[\"everyone\"]},\"who_can_post\":{\"type\":[\"everyone\"]}}")
                            .build()).isOk(), "admin.conversations.setConversationPrefs failed");
            com.slack.api.methods.response.admin.conversations.AdminConversationsGetConversationPrefsResponse conversationPrefs = methods.adminConversationsGetConversationPrefs(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsGetConversationPrefsRequest.builder()
                            .channelId("C1").build());
            require(conversationPrefs.isOk() && conversationPrefs.getPrefs() != null,
                    "admin.conversations.getConversationPrefs failed: " + conversationPrefs.getError());
            com.slack.api.methods.response.admin.conversations.AdminConversationsGetTeamsResponse conversationTeams = methods.adminConversationsGetTeams(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsGetTeamsRequest.builder()
                            .channelId("C1").limit(10).build());
            require(conversationTeams.isOk() && conversationTeams.getTeamIds() != null
                            && conversationTeams.getTeamIds().contains("T1"),
                    "admin.conversations.getTeams failed: " + conversationTeams.getError());
            com.slack.api.methods.response.workflows.WorkflowsStepCompletedResponse completedStep = methods.workflowsStepCompleted(
                    com.slack.api.methods.request.workflows.WorkflowsStepCompletedRequest.builder()
                            .workflowStepExecuteId("qualification-execute")
                            .outputsAsString("{\"answer\":\"ok\"}")
                            .build());
            require(completedStep.isOk(), "workflows.stepCompleted failed: " + completedStep.getError());
            com.slack.api.methods.response.workflows.WorkflowsStepFailedResponse failedStep = methods.workflowsStepFailed(
                    com.slack.api.methods.request.workflows.WorkflowsStepFailedRequest.builder()
                            .workflowStepExecuteId("qualification-failed")
                            .error(java.util.Map.of("message", "qualification failure"))
                            .build());
            require(failedStep.isOk(), "workflows.stepFailed failed: " + failedStep.getError());
            com.slack.api.methods.response.workflows.WorkflowsUpdateStepResponse updatedStep = methods.workflowsUpdateStep(
                    com.slack.api.methods.request.workflows.WorkflowsUpdateStepRequest.builder()
                            .workflowStepEditId("qualification-edit")
                            .inputsAsString("{\"input\":{\"value\":\"qualification\"}}")
                            .outputsAsString("[{\"type\":\"text\",\"name\":\"answer\",\"label\":\"Answer\"}]")
                            .build());
            require(updatedStep.isOk(), "workflows.updateStep failed: " + updatedStep.getError());
            com.slack.api.methods.response.dialog.DialogOpenResponse openedDialog = methods.dialogOpen(
                    com.slack.api.methods.request.dialog.DialogOpenRequest.builder()
                            .triggerId("qualification-trigger")
                            .dialogAsString("{\"callback_id\":\"qualification-dialog\",\"title\":\"Qualification\",\"submit_label\":\"Submit\",\"elements\":[{\"type\":\"text\",\"name\":\"answer\",\"label\":\"Answer\"}]}")
                            .build());
            require(openedDialog.isOk(), "dialog.open failed: " + openedDialog.getError());
            com.slack.api.methods.response.views.ViewsOpenResponse openedView = methods.viewsOpen(
                    com.slack.api.methods.request.views.ViewsOpenRequest.builder()
                            .triggerId("qualification-trigger")
                            .viewAsString("{\"type\":\"modal\",\"callback_id\":\"qualification\",\"title\":{\"type\":\"plain_text\",\"text\":\"Qualification\"},\"blocks\":[]}")
                            .build());
            require(openedView.isOk() && openedView.getView() != null && openedView.getView().getId() != null,
                    "views.open failed: " + openedView.getError());
            com.slack.api.methods.response.views.ViewsPublishResponse publishedView = methods.viewsPublish(
                    com.slack.api.methods.request.views.ViewsPublishRequest.builder()
                            .userId("U1")
                            .viewAsString("{\"type\":\"home\",\"blocks\":[]}")
                            .build());
            require(publishedView.isOk(), "views.publish failed: " + publishedView.getError());
            com.slack.api.methods.response.views.ViewsPushResponse pushedView = methods.viewsPush(
                    com.slack.api.methods.request.views.ViewsPushRequest.builder()
                            .triggerId("qualification-trigger")
                            .viewAsString("{\"type\":\"modal\",\"callback_id\":\"qualification-pushed\",\"title\":{\"type\":\"plain_text\",\"text\":\"Pushed qualification\"},\"blocks\":[]}")
                            .build());
            require(pushedView.isOk(), "views.push failed: " + pushedView.getError());
            com.slack.api.methods.response.views.ViewsUpdateResponse updatedView = methods.viewsUpdate(
                    com.slack.api.methods.request.views.ViewsUpdateRequest.builder()
                            .viewId(openedView.getView().getId())
                            .viewAsString("{\"type\":\"modal\",\"callback_id\":\"qualification-updated\",\"title\":{\"type\":\"plain_text\",\"text\":\"Updated qualification\"},\"blocks\":[]}")
                            .build());
            require(updatedView.isOk(), "views.update failed: " + updatedView.getError());
            com.slack.api.methods.response.calls.CallsAddResponse addedCall = methods.callsAdd(
                    com.slack.api.methods.request.calls.CallsAddRequest.builder()
                            .externalUniqueId("qualification-call")
                            .externalDisplayId("qualification")
                            .joinUrl("https://example.com/call")
                            .desktopAppJoinUrl("https://example.com/call-desktop")
                            .title("Qualification call")
                            .dateStart((int) (System.currentTimeMillis() / 1000))
                            .build());
            require(addedCall.isOk() && addedCall.getCall() != null && addedCall.getCall().getId() != null,
                    "calls.add failed: " + addedCall.getError());
            String callId = addedCall.getCall().getId();
            com.slack.api.methods.response.calls.CallsInfoResponse callInfo = methods.callsInfo(
                    com.slack.api.methods.request.calls.CallsInfoRequest.builder().id(callId).build());
            require(callInfo.isOk(), "calls.info failed: " + callInfo.getError());
            com.slack.api.methods.response.calls.CallsUpdateResponse updatedCall = methods.callsUpdate(
                    com.slack.api.methods.request.calls.CallsUpdateRequest.builder()
                            .id(callId).title("Updated qualification call").build());
            require(updatedCall.isOk(), "calls.update failed: " + updatedCall.getError());
            java.util.List<com.slack.api.model.CallParticipant> participants = java.util.List.of(
                    com.slack.api.model.CallParticipant.builder().slackId("U2").build());
            com.slack.api.methods.response.calls.participants.CallsParticipantsAddResponse addedCallParticipant = methods.callsParticipantsAdd(
                    com.slack.api.methods.request.calls.participants.CallsParticipantsAddRequest.builder()
                            .id(callId).users(participants).build());
            require(addedCallParticipant.isOk(), "calls.participants.add failed: " + addedCallParticipant.getError());
            com.slack.api.methods.response.calls.participants.CallsParticipantsRemoveResponse removedCallParticipant = methods.callsParticipantsRemove(
                    com.slack.api.methods.request.calls.participants.CallsParticipantsRemoveRequest.builder()
                            .id(callId).users(participants).build());
            require(removedCallParticipant.isOk(), "calls.participants.remove failed: " + removedCallParticipant.getError());
            com.slack.api.methods.response.calls.CallsEndResponse endedCall = methods.callsEnd(
                    com.slack.api.methods.request.calls.CallsEndRequest.builder().id(callId).duration(30).build());
            require(endedCall.isOk(), "calls.end failed: " + endedCall.getError());

            ChatPostMessageResponse posted = methods.chatPostMessage(
                    com.slack.api.methods.request.chat.ChatPostMessageRequest.builder()
                            .channel("C1")
                            .text("java qualification")
                            .build());
            require(posted.isOk(), "chat.postMessage failed: " + posted.getError());
            require("C1".equals(posted.getChannel()), "chat.postMessage channel mismatch");
            require(posted.getTs() != null && !posted.getTs().isBlank(), "chat.postMessage ts missing");

            com.slack.api.methods.response.chat.ChatUpdateResponse updated = methods.chatUpdate(
                    com.slack.api.methods.request.chat.ChatUpdateRequest.builder()
                            .channel("C1")
                            .ts(posted.getTs())
                            .text("java qualification updated")
                            .build());
            require(updated.isOk(), "chat.update failed: " + updated.getError());
            com.slack.api.methods.response.chat.ChatDeleteResponse deleted = methods.chatDelete(
                    com.slack.api.methods.request.chat.ChatDeleteRequest.builder()
                            .channel("C1")
                            .ts(posted.getTs())
                            .build());
            require(deleted.isOk(), "chat.delete failed: " + deleted.getError());

            ConversationsInfoResponse conversation = methods.conversationsInfo(
                    com.slack.api.methods.request.conversations.ConversationsInfoRequest.builder()
                            .channel("C1")
                            .build());
            require(conversation.isOk() && conversation.getChannel() != null
                            && "C1".equals(conversation.getChannel().getId()), "conversations.info failed");
            ConversationsMembersResponse members = methods.conversationsMembers(
                    com.slack.api.methods.request.conversations.ConversationsMembersRequest.builder()
                            .channel("C1")
                            .limit(1)
                            .build());
            require(members.isOk() && members.getMembers() != null && members.getMembers().size() == 1
                            && "U1".equals(members.getMembers().get(0)), "conversations.members failed");
            ConversationsListResponse conversations = methods.conversationsList(
                    com.slack.api.methods.request.conversations.ConversationsListRequest.builder()
                            .limit(1)
                            .build());
            require(conversations.isOk() && conversations.getChannels() != null
                            && conversations.getChannels().size() == 1, "conversations.list failed");
            com.slack.api.methods.response.conversations.ConversationsJoinResponse joined = methods.conversationsJoin(
                    com.slack.api.methods.request.conversations.ConversationsJoinRequest.builder().channel("C2").build());
            require(joined.isOk() && joined.getChannel() != null && "C2".equals(joined.getChannel().getId()),
                    "conversations.join failed: " + joined.getError());
            com.slack.api.methods.response.conversations.ConversationsInviteResponse invited = methods.conversationsInvite(
                    com.slack.api.methods.request.conversations.ConversationsInviteRequest.builder()
                            .channel("C1").users(java.util.List.of("U2")).build());
            require(invited.isOk(), "conversations.invite failed: " + invited.getError());
            com.slack.api.methods.response.conversations.ConversationsKickResponse kicked = methods.conversationsKick(
                    com.slack.api.methods.request.conversations.ConversationsKickRequest.builder()
                            .channel("C1").user("U2").build());
            require(kicked.isOk(), "conversations.kick failed: " + kicked.getError());
            com.slack.api.methods.response.conversations.ConversationsLeaveResponse left = methods.conversationsLeave(
                    com.slack.api.methods.request.conversations.ConversationsLeaveRequest.builder().channel("C2").build());
            require(left.isOk(), "conversations.leave failed: " + left.getError());
            require(methods.adminConversationsConvertToPrivate(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsConvertToPrivateRequest.builder()
                            .channelId("C2").build()).isOk(), "admin.conversations.convertToPrivate failed");
            require(methods.adminConversationsDelete(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsDeleteRequest.builder()
                            .channelId("C2").build()).isOk(), "admin.conversations.delete failed");
            com.slack.api.methods.response.admin.conversations.AdminConversationsCreateResponse createdAdminConversation =
                    methods.adminConversationsCreate(
                            com.slack.api.methods.request.admin.conversations.AdminConversationsCreateRequest.builder()
                                    .name("sdk-admin-created").isPrivate(true).teamId("T1").build());
            require(createdAdminConversation.isOk() && createdAdminConversation.getChannelId() != null,
                    "admin.conversations.create failed");
            require(methods.adminConversationsDelete(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsDeleteRequest.builder()
                            .channelId(createdAdminConversation.getChannelId()).build()).isOk(),
                    "admin.conversations.delete for created conversation failed");
            com.slack.api.methods.response.admin.conversations.ekm.AdminConversationsEkmListOriginalConnectedChannelInfoResponse connectedChannelInfo =
                    methods.adminConversationsEkmListOriginalConnectedChannelInfo(
                            com.slack.api.methods.request.admin.conversations.ekm.AdminConversationsEkmListOriginalConnectedChannelInfoRequest.builder()
                                    .channelIds(java.util.List.of("C1")).limit(10).build());
            require(connectedChannelInfo.isOk(),
                    "admin.conversations.ekm.listOriginalConnectedChannelInfo failed");
            require(methods.adminConversationsDisconnectShared(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsDisconnectSharedRequest.builder()
                            .channelId("C1").leavingTeamIds(java.util.List.of("T1")).build()).isOk(),
                    "admin.conversations.disconnectShared failed");
            require(methods.adminConversationsSetTeams(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsSetTeamsRequest.builder()
                            .channelId("C1").orgChannel(false).targetTeamIds(java.util.List.of("T1")).build()).isOk(),
                    "admin.conversations.setTeams failed");
            com.slack.api.methods.response.admin.conversations.AdminConversationsCreateResponse restrictedConversation =
                    methods.adminConversationsCreate(
                            com.slack.api.methods.request.admin.conversations.AdminConversationsCreateRequest.builder()
                                    .name("sdk-restricted-private").isPrivate(true).teamId("T1").build());
            require(restrictedConversation.isOk() && restrictedConversation.getChannelId() != null,
                    "admin.conversations.create for access group failed");
            com.slack.api.methods.response.usergroups.UsergroupsCreateResponse accessGroup = methods.usergroupsCreate(
                    com.slack.api.methods.request.usergroups.UsergroupsCreateRequest.builder()
                            .name("SDK Access Group").handle("sdk-access-group").teamId("T1").build());
            require(accessGroup.isOk() && accessGroup.getUsergroup() != null && accessGroup.getUsergroup().getId() != null,
                    "usergroups.create for access group failed");
            String accessGroupId = accessGroup.getUsergroup().getId();
            require(methods.adminConversationsRestrictAccessAddGroup(
                    com.slack.api.methods.request.admin.conversations.restrict_access.AdminConversationsRestrictAccessAddGroupRequest.builder()
                            .channelId(restrictedConversation.getChannelId()).groupId(accessGroupId).teamId("T1").build()).isOk(),
                    "admin.conversations.restrictAccess.addGroup failed");
            com.slack.api.methods.response.admin.conversations.restrict_access.AdminConversationsRestrictAccessListGroupsResponse accessGroups =
                    methods.adminConversationsRestrictAccessListGroups(
                            com.slack.api.methods.request.admin.conversations.restrict_access.AdminConversationsRestrictAccessListGroupsRequest.builder()
                                    .channelId(restrictedConversation.getChannelId()).teamId("T1").build());
            require(accessGroups.isOk() && accessGroups.getGroupIds() != null && accessGroups.getGroupIds().size() == 1
                            && accessGroupId.equals(accessGroups.getGroupIds().get(0)),
                    "admin.conversations.restrictAccess.listGroups failed");
            require(methods.adminConversationsRestrictAccessRemoveGroup(
                    com.slack.api.methods.request.admin.conversations.restrict_access.AdminConversationsRestrictAccessRemoveGroupRequest.builder()
                            .channelId(restrictedConversation.getChannelId()).groupId(accessGroupId).teamId("T1").build()).isOk(),
                    "admin.conversations.restrictAccess.removeGroup failed");
            require(methods.adminConversationsDelete(
                    com.slack.api.methods.request.admin.conversations.AdminConversationsDeleteRequest.builder()
                            .channelId(restrictedConversation.getChannelId()).build()).isOk(),
                    "admin.conversations.delete for access group failed");
            require(methods.usergroupsDisable(
                    com.slack.api.methods.request.usergroups.UsergroupsDisableRequest.builder()
                            .usergroup(accessGroupId).teamId("T1").build()).isOk(),
                    "usergroups.disable for access group failed");
            com.slack.api.methods.response.files.FilesUploadResponse uploadedFile = methods.filesUpload(
                    com.slack.api.methods.request.files.FilesUploadRequest.builder()
                            .content("sdk upload")
                            .filename("sdk-upload.txt")
                            .title("SDK upload")
                            .build());
            require(uploadedFile.isOk() && uploadedFile.getFile() != null && uploadedFile.getFile().getId() != null,
                    "files.upload failed: " + uploadedFile.getError());
            com.slack.api.methods.response.files.FilesListResponse files = methods.filesList(
                    com.slack.api.methods.request.files.FilesListRequest.builder().count(10).build());
            require(files.isOk() && files.getFiles() != null && files.getFiles().size() == 2,
                    "files.list failed: " + files.getError());
            String fileId = uploadedFile.getFile().getId();
            String qualificationFileId = files.getFiles().stream()
                    .filter(file -> "qualification.txt".equals(file.getName()))
                    .map(file -> file.getId())
                    .findFirst()
                    .orElseThrow();
            require(methods.filesCommentsDelete(
                    com.slack.api.methods.request.files.comments.FilesCommentsDeleteRequest.builder()
                            .file(qualificationFileId).id("FC1").build()).isOk(),
                    "files.comments.delete failed");
            com.slack.api.methods.response.files.FilesInfoResponse fileInfo = methods.filesInfo(
                    com.slack.api.methods.request.files.FilesInfoRequest.builder().file(fileId).build());
            require(fileInfo.isOk() && fileInfo.getFile() != null && fileId.equals(fileInfo.getFile().getId()),
                    "files.info failed: " + fileInfo.getError());
            com.slack.api.methods.response.files.FilesSharedPublicURLResponse publicFile = methods.filesSharedPublicURL(
                    com.slack.api.methods.request.files.FilesSharedPublicURLRequest.builder().file(fileId).build());
            require(publicFile.isOk() && publicFile.getFile() != null
                            && publicFile.getFile().getPermalinkPublic() != null,
                    "files.sharedPublicURL failed: " + publicFile.getError());
            com.slack.api.methods.response.files.FilesRevokePublicURLResponse revokedPublicFile = methods.filesRevokePublicURL(
                    com.slack.api.methods.request.files.FilesRevokePublicURLRequest.builder().file(fileId).build());
            require(revokedPublicFile.isOk(), "files.revokePublicURL failed: " + revokedPublicFile.getError());
            com.slack.api.methods.response.files.FilesDeleteResponse deletedFile = methods.filesDelete(
                    com.slack.api.methods.request.files.FilesDeleteRequest.builder().file(fileId).build());
            require(deletedFile.isOk(), "files.delete failed: " + deletedFile.getError());
            com.slack.api.methods.response.files.remote.FilesRemoteAddResponse remoteFile = methods.filesRemoteAdd(
                    com.slack.api.methods.request.files.remote.FilesRemoteAddRequest.builder()
                            .externalId("remote-qualification")
                            .title("Remote qualification")
                            .filetype("text")
                            .externalUrl("https://example.com/qualification")
                            .build());
            require(remoteFile.isOk() && remoteFile.getFile() != null
                            && "remote-qualification".equals(remoteFile.getFile().getExternalId()),
                    "files.remote.add failed: " + remoteFile.getError());
            com.slack.api.methods.response.files.remote.FilesRemoteInfoResponse remoteInfo = methods.filesRemoteInfo(
                    com.slack.api.methods.request.files.remote.FilesRemoteInfoRequest.builder()
                            .externalId("remote-qualification").build());
            require(remoteInfo.isOk(), "files.remote.info failed: " + remoteInfo.getError());
            com.slack.api.methods.response.files.remote.FilesRemoteListResponse remoteList = methods.filesRemoteList(
                    com.slack.api.methods.request.files.remote.FilesRemoteListRequest.builder().limit(1).build());
            require(remoteList.isOk() && remoteList.getFiles() != null && remoteList.getFiles().size() == 1,
                    "files.remote.list failed: " + remoteList.getError());
            com.slack.api.methods.response.files.remote.FilesRemoteUpdateResponse remoteUpdate = methods.filesRemoteUpdate(
                    com.slack.api.methods.request.files.remote.FilesRemoteUpdateRequest.builder()
                            .externalId("remote-qualification").title("Updated remote qualification").build());
            require(remoteUpdate.isOk(), "files.remote.update failed: " + remoteUpdate.getError());
            com.slack.api.methods.response.files.remote.FilesRemoteShareResponse remoteShare = methods.filesRemoteShare(
                    com.slack.api.methods.request.files.remote.FilesRemoteShareRequest.builder()
                            .externalId("remote-qualification").channels(java.util.List.of("C1")).build());
            require(remoteShare.isOk() && remoteShare.getFile() != null
                            && remoteShare.getFile().getChannels() != null
                            && remoteShare.getFile().getChannels().equals(java.util.List.of("C1")),
                    "files.remote.share failed: " + remoteShare.getError());
            com.slack.api.methods.response.files.remote.FilesRemoteRemoveResponse remoteRemove = methods.filesRemoteRemove(
                    com.slack.api.methods.request.files.remote.FilesRemoteRemoveRequest.builder()
                            .externalId("remote-qualification").build());
            require(remoteRemove.isOk(), "files.remote.remove failed: " + remoteRemove.getError());
            com.slack.api.methods.response.chat.ChatScheduleMessageResponse scheduled = methods.chatScheduleMessage(
                    com.slack.api.methods.request.chat.ChatScheduleMessageRequest.builder()
                            .channel("C1")
                            .text("scheduled qualification")
                            .postAt((int) (System.currentTimeMillis() / 1000L) + 60)
                            .build());
            require(scheduled.isOk() && scheduled.getScheduledMessageId() != null,
                    "chat.scheduleMessage failed: " + scheduled.getError());
            com.slack.api.methods.response.chat.scheduled_messages.ChatScheduledMessagesListResponse scheduledList = methods.chatScheduledMessagesList(
                    com.slack.api.methods.request.chat.scheduled_messages.ChatScheduledMessagesListRequest.builder()
                            .channel("C1").limit(10).build());
            require(scheduledList.isOk() && scheduledList.getScheduledMessages() != null
                            && scheduledList.getScheduledMessages().size() == 1,
                    "chat.scheduledMessages.list failed: " + scheduledList.getError());
            com.slack.api.methods.response.chat.ChatDeleteScheduledMessageResponse deletedScheduled = methods.chatDeleteScheduledMessage(
                    com.slack.api.methods.request.chat.ChatDeleteScheduledMessageRequest.builder()
                            .channel("C1").scheduledMessageId(scheduled.getScheduledMessageId()).build());
            require(deletedScheduled.isOk(), "chat.deleteScheduledMessage failed: " + deletedScheduled.getError());
            com.slack.api.methods.response.dnd.DndInfoResponse dndInfo = methods.dndInfo(
                    com.slack.api.methods.request.dnd.DndInfoRequest.builder().build());
            require(dndInfo.isOk() && !dndInfo.isDndEnabled(), "dnd.info failed: " + dndInfo.getError());
            com.slack.api.methods.response.dnd.DndSetSnoozeResponse dndSnooze = methods.dndSetSnooze(
                    com.slack.api.methods.request.dnd.DndSetSnoozeRequest.builder().numMinutes(5).build());
            require(dndSnooze.isOk() && dndSnooze.isSnoozeEnabled(), "dnd.setSnooze failed: " + dndSnooze.getError());
            com.slack.api.methods.response.dnd.DndEndSnoozeResponse dndEndSnooze = methods.dndEndSnooze(
                    com.slack.api.methods.request.dnd.DndEndSnoozeRequest.builder().build());
            require(dndEndSnooze.isOk(), "dnd.endSnooze failed: " + dndEndSnooze.getError());
            com.slack.api.methods.response.dnd.DndEndDndResponse dndEnd = methods.dndEndDnd(
                    com.slack.api.methods.request.dnd.DndEndDndRequest.builder().build());
            require(dndEnd.isOk(), "dnd.endDnd failed: " + dndEnd.getError());
            com.slack.api.methods.response.dnd.DndTeamInfoResponse dndTeam = methods.dndTeamInfo(
                    com.slack.api.methods.request.dnd.DndTeamInfoRequest.builder().build());
            require(dndTeam.isOk(), "dnd.teamInfo failed: " + dndTeam.getError());
            com.slack.api.methods.response.rtm.RTMConnectResponse rtm = methods.rtmConnect(
                    com.slack.api.methods.request.rtm.RTMConnectRequest.builder().build());
            require(rtm.isOk() && rtm.getUrl() != null && rtm.getTeam() != null && "T1".equals(rtm.getTeam().getId())
                            && rtm.getSelf() != null && "U1".equals(rtm.getSelf().getId()),
                    "rtm.connect failed: " + rtm.getError());
            com.slack.api.methods.response.reminders.RemindersAddResponse reminder = methods.remindersAdd(
                    com.slack.api.methods.request.reminders.RemindersAddRequest.builder()
                            .text("reminder qualification")
                            .time(Long.toString(System.currentTimeMillis() / 1000L + 3600L))
                            .build());
            require(reminder.isOk() && reminder.getReminder() != null && reminder.getReminder().getId() != null,
                    "reminders.add failed: " + reminder.getError());
            String reminderId = reminder.getReminder().getId();
            com.slack.api.methods.response.reminders.RemindersListResponse reminders = methods.remindersList(
                    com.slack.api.methods.request.reminders.RemindersListRequest.builder().build());
            require(reminders.isOk() && reminders.getReminders() != null && reminders.getReminders().size() == 1,
                    "reminders.list failed: " + reminders.getError());
            com.slack.api.methods.response.reminders.RemindersInfoResponse reminderInfo = methods.remindersInfo(
                    com.slack.api.methods.request.reminders.RemindersInfoRequest.builder().reminder(reminderId).build());
            require(reminderInfo.isOk() && reminderInfo.getReminder() != null
                            && reminderId.equals(reminderInfo.getReminder().getId()),
                    "reminders.info failed: " + reminderInfo.getError());
            com.slack.api.methods.response.reminders.RemindersCompleteResponse completedReminder = methods.remindersComplete(
                    com.slack.api.methods.request.reminders.RemindersCompleteRequest.builder().reminder(reminderId).build());
            require(completedReminder.isOk(), "reminders.complete failed: " + completedReminder.getError());
            com.slack.api.methods.response.reminders.RemindersDeleteResponse deletedReminder = methods.remindersDelete(
                    com.slack.api.methods.request.reminders.RemindersDeleteRequest.builder().reminder(reminderId).build());
            require(deletedReminder.isOk(), "reminders.delete failed: " + deletedReminder.getError());
            com.slack.api.methods.response.usergroups.UsergroupsCreateResponse createdUsergroup = methods.usergroupsCreate(
                    com.slack.api.methods.request.usergroups.UsergroupsCreateRequest.builder()
                            .name("Qualification group").handle("qualification-group").description("SDK qualification").build());
            require(createdUsergroup.isOk() && createdUsergroup.getUsergroup() != null
                            && createdUsergroup.getUsergroup().getId() != null,
                    "usergroups.create failed: " + createdUsergroup.getError());
            String usergroupId = createdUsergroup.getUsergroup().getId();
            require(methods.adminUsergroupsAddChannels(
                    com.slack.api.methods.request.admin.usergroups.AdminUsergroupsAddChannelsRequest.builder()
                            .usergroupId(usergroupId).channelIds(java.util.List.of("C1")).build()).isOk(),
                    "admin.usergroups.addChannels failed");
            require(methods.adminUsergroupsAddTeams(
                    com.slack.api.methods.request.admin.usergroups.AdminUsergroupsAddTeamsRequest.builder()
                            .usergroupId(usergroupId).teamIds(java.util.List.of("T1")).build()).isOk(),
                    "admin.usergroups.addTeams failed");
            com.slack.api.methods.response.admin.usergroups.AdminUsergroupsListChannelsResponse adminUsergroupChannels =
                    methods.adminUsergroupsListChannels(
                            com.slack.api.methods.request.admin.usergroups.AdminUsergroupsListChannelsRequest.builder()
                                    .usergroupId(usergroupId).teamId("T1").build());
            require(adminUsergroupChannels.isOk() && adminUsergroupChannels.getChannels() != null
                            && adminUsergroupChannels.getChannels().size() == 1
                            && "C1".equals(adminUsergroupChannels.getChannels().get(0).getId()),
                    "admin.usergroups.listChannels failed");
            require(methods.adminUsergroupsRemoveChannels(
                    com.slack.api.methods.request.admin.usergroups.AdminUsergroupsRemoveChannelsRequest.builder()
                            .usergroupId(usergroupId).channelIds(java.util.List.of("C1")).build()).isOk(),
                    "admin.usergroups.removeChannels failed");
            com.slack.api.methods.response.usergroups.UsergroupsUpdateResponse updatedUsergroup = methods.usergroupsUpdate(
                    com.slack.api.methods.request.usergroups.UsergroupsUpdateRequest.builder()
                            .usergroup(usergroupId).name("Updated qualification group").build());
            require(updatedUsergroup.isOk(), "usergroups.update failed: " + updatedUsergroup.getError());
            com.slack.api.methods.response.usergroups.users.UsergroupsUsersUpdateResponse updatedUsergroupUsers = methods.usergroupsUsersUpdate(
                    com.slack.api.methods.request.usergroups.users.UsergroupsUsersUpdateRequest.builder()
                            .usergroup(usergroupId).users(java.util.List.of("U1")).build());
            require(updatedUsergroupUsers.isOk(), "usergroups.users.update failed: " + updatedUsergroupUsers.getError());
            com.slack.api.methods.response.usergroups.users.UsergroupsUsersListResponse usergroupUsers = methods.usergroupsUsersList(
                    com.slack.api.methods.request.usergroups.users.UsergroupsUsersListRequest.builder().usergroup(usergroupId).build());
            require(usergroupUsers.isOk() && usergroupUsers.getUsers() != null
                            && usergroupUsers.getUsers().equals(java.util.List.of("U1")),
                    "usergroups.users.list failed: " + usergroupUsers.getError());
            com.slack.api.methods.response.usergroups.UsergroupsListResponse usergroups = methods.usergroupsList(
                    com.slack.api.methods.request.usergroups.UsergroupsListRequest.builder().includeUsers(true).build());
            require(usergroups.isOk() && usergroups.getUsergroups() != null && usergroups.getUsergroups().size() == 1,
                    "usergroups.list failed: " + usergroups.getError());
            com.slack.api.methods.response.usergroups.UsergroupsDisableResponse disabledUsergroup = methods.usergroupsDisable(
                    com.slack.api.methods.request.usergroups.UsergroupsDisableRequest.builder().usergroup(usergroupId).build());
            require(disabledUsergroup.isOk(), "usergroups.disable failed: " + disabledUsergroup.getError());
            com.slack.api.methods.response.usergroups.UsergroupsEnableResponse enabledUsergroup = methods.usergroupsEnable(
                    com.slack.api.methods.request.usergroups.UsergroupsEnableRequest.builder().usergroup(usergroupId).build());
            require(enabledUsergroup.isOk(), "usergroups.enable failed: " + enabledUsergroup.getError());
            UsersInfoResponse user = methods.usersInfo(
                    com.slack.api.methods.request.users.UsersInfoRequest.builder().user("U1").build());
            require(user.isOk() && user.getUser() != null && "U1".equals(user.getUser().getId()), "users.info failed");
            UsersProfileGetResponse profile = methods.usersProfileGet(
                    com.slack.api.methods.request.users.profile.UsersProfileGetRequest.builder().user("U1").build());
            require(profile.isOk() && profile.getProfile() != null
                            && "alice".equals(profile.getProfile().getDisplayName()), "users.profile.get failed");
            java.io.File image = java.io.File.createTempFile("qualification", ".png");
            java.nio.file.Files.write(image.toPath(), "qualification-photo".getBytes(java.nio.charset.StandardCharsets.UTF_8));
            com.slack.api.methods.response.users.UsersSetPhotoResponse photo = methods.usersSetPhoto(
                    com.slack.api.methods.request.users.UsersSetPhotoRequest.builder().image(image).build());
            if (!image.delete()) {
                throw new IllegalStateException("could not delete temporary qualification image");
            }
            require(photo.isOk(), "users.setPhoto failed: " + photo.getError());
            com.slack.api.methods.response.users.UsersDeletePhotoResponse deletedPhoto = methods.usersDeletePhoto(
                    com.slack.api.methods.request.users.UsersDeletePhotoRequest.builder().build());
            require(deletedPhoto.isOk(), "users.deletePhoto failed: " + deletedPhoto.getError());

            ChatPostMessageResponse root = methods.chatPostMessage(
                    com.slack.api.methods.request.chat.ChatPostMessageRequest.builder()
                            .channel("C1")
                            .text("thread root")
                            .build());
            require(root.isOk(), "thread root failed: " + root.getError());
            com.slack.api.methods.response.chat.ChatUnfurlResponse unfurled = methods.chatUnfurl(
                    com.slack.api.methods.request.chat.ChatUnfurlRequest.builder()
                            .channel("C1")
                            .ts(root.getTs())
                            .rawUnfurls("{\"https://example.com/qualification\":{\"text\":\"unfurled\"}}")
                            .build());
            require(unfurled.isOk(), "chat.unfurl failed: " + unfurled.getError());
            ChatPostMessageResponse reply = methods.chatPostMessage(
                    com.slack.api.methods.request.chat.ChatPostMessageRequest.builder()
                            .channel("C1")
                            .text("thread reply")
                            .threadTs(root.getTs())
                            .build());
            require(reply.isOk(), "thread reply failed: " + reply.getError());
            ConversationsRepliesResponse replies = methods.conversationsReplies(
                    com.slack.api.methods.request.conversations.ConversationsRepliesRequest.builder()
                            .channel("C1")
                            .ts(root.getTs())
                            .limit(2)
                            .build());
            require(replies.isOk() && replies.getMessages() != null && replies.getMessages().size() == 2,
                    "conversations.replies failed");
            ReactionsAddResponse reaction = methods.reactionsAdd(
                    com.slack.api.methods.request.reactions.ReactionsAddRequest.builder()
                            .channel("C1")
                            .timestamp(root.getTs())
                            .name("thumbsup")
                            .build());
            require(reaction.isOk(), "reactions.add failed: " + reaction.getError());
            ReactionsGetResponse reactions = methods.reactionsGet(
                    com.slack.api.methods.request.reactions.ReactionsGetRequest.builder()
                            .channel("C1")
                            .timestamp(root.getTs())
                            .build());
            require(reactions.isOk() && reactions.getMessage() != null
                            && reactions.getMessage().getReactions() != null
                            && reactions.getMessage().getReactions().size() == 1, "reactions.get failed");
            PinsAddResponse pin = methods.pinsAdd(
                    com.slack.api.methods.request.pins.PinsAddRequest.builder()
                            .channel("C1")
                            .timestamp(root.getTs())
                            .build());
            require(pin.isOk(), "pins.add failed: " + pin.getError());
            PinsListResponse pins = methods.pinsList(
                    com.slack.api.methods.request.pins.PinsListRequest.builder().channel("C1").build());
            require(pins.isOk() && pins.getItems() != null && pins.getItems().size() == 1, "pins.list failed");
            PinsRemoveResponse pinRemoved = methods.pinsRemove(
                    com.slack.api.methods.request.pins.PinsRemoveRequest.builder()
                            .channel("C1")
                            .timestamp(root.getTs())
                            .build());
            require(pinRemoved.isOk(), "pins.remove failed: " + pinRemoved.getError());
            ReactionsRemoveResponse reactionRemoved = methods.reactionsRemove(
                    com.slack.api.methods.request.reactions.ReactionsRemoveRequest.builder()
                            .channel("C1")
                            .timestamp(root.getTs())
                            .name("thumbsup")
                            .build());
            require(reactionRemoved.isOk(), "reactions.remove failed: " + reactionRemoved.getError());

            ConversationsCreateResponse createdConversation = methods.conversationsCreate(
                    com.slack.api.methods.request.conversations.ConversationsCreateRequest.builder()
                            .name("qualification-tranche")
                            .build());
            require(createdConversation.isOk() && createdConversation.getChannel() != null,
                    "conversations.create failed: " + createdConversation.getError());
            String lifecycleChannel = createdConversation.getChannel().getId();
            ConversationsRenameResponse renamedConversation = methods.conversationsRename(
                    com.slack.api.methods.request.conversations.ConversationsRenameRequest.builder()
                            .channel(lifecycleChannel)
                            .name("qualification-renamed")
                            .build());
            require(renamedConversation.isOk(), "conversations.rename failed: " + renamedConversation.getError());
            ConversationsSetTopicResponse topic = methods.conversationsSetTopic(
                    com.slack.api.methods.request.conversations.ConversationsSetTopicRequest.builder()
                            .channel(lifecycleChannel)
                            .topic("qualification topic")
                            .build());
            require(topic.isOk(), "conversations.setTopic failed: " + topic.getError());
            ConversationsSetPurposeResponse purpose = methods.conversationsSetPurpose(
                    com.slack.api.methods.request.conversations.ConversationsSetPurposeRequest.builder()
                            .channel(lifecycleChannel)
                            .purpose("qualification purpose")
                            .build());
            require(purpose.isOk(), "conversations.setPurpose failed: " + purpose.getError());
            ConversationsArchiveResponse archived = methods.conversationsArchive(
                    com.slack.api.methods.request.conversations.ConversationsArchiveRequest.builder()
                            .channel(lifecycleChannel)
                            .build());
            require(archived.isOk(), "conversations.archive failed: " + archived.getError());
            ConversationsUnarchiveResponse unarchived = methods.conversationsUnarchive(
                    com.slack.api.methods.request.conversations.ConversationsUnarchiveRequest.builder()
                            .channel(lifecycleChannel)
                            .build());
            require(unarchived.isOk(), "conversations.unarchive failed: " + unarchived.getError());
            ConversationsInfoResponse lifecycleInfo = methods.conversationsInfo(
                    com.slack.api.methods.request.conversations.ConversationsInfoRequest.builder()
                            .channel(lifecycleChannel)
                            .build());
            require(lifecycleInfo.isOk() && lifecycleInfo.getChannel() != null
                            && "qualification-renamed".equals(lifecycleInfo.getChannel().getName())
                            && "qualification topic".equals(lifecycleInfo.getChannel().getTopic().getValue())
                            && "qualification purpose".equals(lifecycleInfo.getChannel().getPurpose().getValue()),
                    "conversation lifecycle state mismatch");

            ChatMeMessageResponse meMessage = methods.chatMeMessage(
                    com.slack.api.methods.request.chat.ChatMeMessageRequest.builder()
                            .channel("C1")
                            .text("qualification me message")
                            .build());
            require(meMessage.isOk(), "chat.meMessage failed: " + meMessage.getError());
            com.slack.api.methods.response.chat.ChatPostEphemeralResponse ephemeral = methods.chatPostEphemeral(
                    com.slack.api.methods.request.chat.ChatPostEphemeralRequest.builder()
                            .channel("C1").user("U1").text("ephemeral qualification").build());
            require(ephemeral.isOk() && ephemeral.getMessageTs() != null,
                    "chat.postEphemeral failed: " + ephemeral.getError());
            com.slack.api.methods.response.stars.StarsAddResponse starred = methods.starsAdd(
                    com.slack.api.methods.request.stars.StarsAddRequest.builder()
                            .channel("C1").timestamp(root.getTs()).build());
            require(starred.isOk(), "stars.add failed: " + starred.getError());
            com.slack.api.methods.response.stars.StarsListResponse stars = methods.starsList(
                    com.slack.api.methods.request.stars.StarsListRequest.builder().limit(10).build());
            require(stars.isOk() && stars.getItems() != null && stars.getItems().size() == 1,
                    "stars.list failed: " + stars.getError());
            com.slack.api.methods.response.stars.StarsRemoveResponse unstarred = methods.starsRemove(
                    com.slack.api.methods.request.stars.StarsRemoveRequest.builder()
                            .channel("C1").timestamp(root.getTs()).build());
            require(unstarred.isOk(), "stars.remove failed: " + unstarred.getError());
            ChatGetPermalinkResponse permalink = methods.chatGetPermalink(
                    com.slack.api.methods.request.chat.ChatGetPermalinkRequest.builder()
                            .channel("C1")
                            .messageTs(root.getTs())
                            .build());
            require(permalink.isOk() && permalink.getPermalink() != null && !permalink.getPermalink().isBlank(),
                    "chat.getPermalink failed");
            ReactionsListResponse userReactions = methods.reactionsList(
                    com.slack.api.methods.request.reactions.ReactionsListRequest.builder().limit(1).build());
            require(userReactions.isOk(), "reactions.list failed: " + userReactions.getError());
            TeamInfoResponse team = methods.teamInfo(
                    com.slack.api.methods.request.team.TeamInfoRequest.builder().build());
            require(team.isOk() && team.getTeam() != null && "T1".equals(team.getTeam().getId()), "team.info failed");
            com.slack.api.methods.response.team.profile.TeamProfileGetResponse teamProfile = methods.teamProfileGet(
                    com.slack.api.methods.request.team.profile.TeamProfileGetRequest.builder().build());
            require(teamProfile.isOk() && teamProfile.getProfile() != null
                            && teamProfile.getProfile().getFields() != null
                            && teamProfile.getProfile().getFields().isEmpty(),
                    "team.profile.get failed: " + teamProfile.getError());
            EmojiListResponse emoji = methods.emojiList(
                    com.slack.api.methods.request.emoji.EmojiListRequest.builder().build());
            require(emoji.isOk(), "emoji.list failed: " + emoji.getError());
            UsersIdentityResponse identityResult = methods.usersIdentity(
                    com.slack.api.methods.request.users.UsersIdentityRequest.builder().build());
            require(identityResult.isOk() && identityResult.getUser() != null
                            && "U1".equals(identityResult.getUser().getId()), "users.identity failed");
            UsersLookupByEmailResponse byEmail = methods.usersLookupByEmail(
                    com.slack.api.methods.request.users.UsersLookupByEmailRequest.builder()
                            .email("alice@example.com")
                            .build());
            require(byEmail.isOk() && byEmail.getUser() != null && "U1".equals(byEmail.getUser().getId()),
                    "users.lookupByEmail failed");
            UsersGetPresenceResponse presence = methods.usersGetPresence(
                    com.slack.api.methods.request.users.UsersGetPresenceRequest.builder().user("U1").build());
            require(presence.isOk(), "users.getPresence failed: " + presence.getError());
            UsersSetPresenceResponse setPresence = methods.usersSetPresence(
                    com.slack.api.methods.request.users.UsersSetPresenceRequest.builder().presence("away").build());
            require(setPresence.isOk(), "users.setPresence failed: " + setPresence.getError());
            com.slack.api.model.User.Profile profileRequest = new com.slack.api.model.User.Profile();
            profileRequest.setStatusText("qualification");
            profileRequest.setStatusEmoji(":wave:");
            UsersProfileSetResponse profileSet = methods.usersProfileSet(
                    com.slack.api.methods.request.users.profile.UsersProfileSetRequest.builder()
                            .profile(profileRequest)
                            .build());
            require(profileSet.isOk() && profileSet.getProfile() != null
                            && "qualification".equals(profileSet.getProfile().getStatusText()),
                    "users.profile.set failed");
            UsersConversationsResponse userConversations = methods.usersConversations(
                    com.slack.api.methods.request.users.UsersConversationsRequest.builder()
                            .user("U1")
                            .limit(1)
                            .build());
            require(userConversations.isOk() && userConversations.getChannels() != null
                            && userConversations.getChannels().size() == 1, "users.conversations failed");
            ConversationsOpenResponse direct = methods.conversationsOpen(
                    com.slack.api.methods.request.conversations.ConversationsOpenRequest.builder()
                            .users(List.of("U2"))
                            .build());
            require(direct.isOk() && direct.getChannel() != null, "conversations.open failed: " + direct.getError());
            ConversationsCloseResponse closed = methods.conversationsClose(
                    com.slack.api.methods.request.conversations.ConversationsCloseRequest.builder()
                            .channel(direct.getChannel().getId())
                            .build());
            require(closed.isOk(), "conversations.close failed: " + closed.getError());
            ConversationsMarkResponse marked = methods.conversationsMark(
                    com.slack.api.methods.request.conversations.ConversationsMarkRequest.builder()
                            .channel("C1")
                            .ts(root.getTs())
                            .build());
            require(marked.isOk(), "conversations.mark failed: " + marked.getError());

            ConversationsHistoryResponse history = methods.conversationsHistory(
                    com.slack.api.methods.request.conversations.ConversationsHistoryRequest.builder()
                            .channel("C1")
                            .limit(10)
                            .build());
            require(history.isOk(), "conversations.history failed: " + history.getError());
            require(history.getMessages() != null && history.getMessages().size() == 4, "history page mismatch");
            com.slack.api.methods.response.search.SearchMessagesResponse search = methods.searchMessages(
                    com.slack.api.methods.request.search.SearchMessagesRequest.builder().query("thread").build());
            require(search.isOk() && search.getMessages() != null
                            && search.getMessages().getMatches() != null
                            && search.getMessages().getMatches().size() >= 2,
                    "search.messages failed: " + search.getError());

            UsersListResponse users = methods.usersList(
                    com.slack.api.methods.request.users.UsersListRequest.builder().limit(10).build());
            require(users.isOk(), "users.list failed: " + users.getError());
            require(users.getMembers() != null && users.getMembers().size() == 2, "users page mismatch");
            require(users.getResponseMetadata() != null
                            && (users.getResponseMetadata().getNextCursor() == null
                                    || users.getResponseMetadata().getNextCursor().isBlank()),
                    "users cursor mismatch");
            require(methods.usersSetActive(
                    com.slack.api.methods.request.users.UsersSetActiveRequest.builder().build()).isOk(),
                    "users.setActive failed");
            require(methods.adminUsersAssign(
                    com.slack.api.methods.request.admin.users.AdminUsersAssignRequest.builder()
                            .teamId("T1").userId("U2").channelIds(java.util.List.of("C1"))
                            .isRestricted(false).isUltraRestricted(false).build()).isOk(),
                    "admin.users.assign failed");
            require(methods.adminUsersSetExpiration(
                    com.slack.api.methods.request.admin.users.AdminUsersSetExpirationRequest.builder()
                            .teamId("T1").userId("U2")
                            .expirationTs(java.time.Instant.now().getEpochSecond() + 3600).build()).isOk(),
                    "admin.users.setExpiration failed");
            require(methods.adminUsersSessionInvalidate(
                    com.slack.api.methods.request.admin.users.AdminUsersSessionInvalidateRequest.builder()
                            .teamId("T1").sessionId("qualification-session").build()).isOk(),
                    "admin.users.session.invalidate failed");
            require(methods.adminUsersSessionReset(
                    com.slack.api.methods.request.admin.users.AdminUsersSessionResetRequest.builder()
                            .userId("U2").build()).isOk(),
                    "admin.users.session.reset failed");
            require(methods.adminUsersRemove(
                    com.slack.api.methods.request.admin.users.AdminUsersRemoveRequest.builder()
                            .teamId("T1").userId("U2").build()).isOk(),
                    "admin.users.remove failed");

            ApiTestResponse synthetic = methods.apiTest(
                    com.slack.api.methods.request.api.ApiTestRequest.builder().error("synthetic").build());
            require(!synthetic.isOk() && "synthetic".equals(synthetic.getError()), "SDK error decoding failed");
            com.slack.api.methods.response.auth.AuthRevokeResponse revoked = methods.authRevoke(
                    com.slack.api.methods.request.auth.AuthRevokeRequest.builder().test(true).build());
            require(revoked.isOk() && !revoked.isRevoked(), "auth.revoke failed: " + revoked.getError());
            com.slack.api.methods.response.apps.AppsUninstallResponse uninstalled = methods.appsUninstall(
                    com.slack.api.methods.request.apps.AppsUninstallRequest.builder().clientId("client").build());
            require(uninstalled.isOk(), "apps.uninstall failed: " + uninstalled.getError());
        }
        System.out.println("java-slack-api qualification passed");
    }

    private static String env(String name, String defaultValue) {
        String value = System.getenv(name);
        return value == null || value.isBlank() ? defaultValue : value;
    }

    static void require(boolean condition, String message) {
        if (!condition) {
            throw new IllegalStateException(message);
        }
    }
}
