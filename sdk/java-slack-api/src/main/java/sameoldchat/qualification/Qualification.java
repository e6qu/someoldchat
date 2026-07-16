package sameoldchat.qualification;

import com.slack.api.Slack;
import com.slack.api.SlackConfig;
import com.slack.api.methods.MethodsClient;
import com.slack.api.methods.response.api.ApiTestResponse;
import com.slack.api.methods.response.auth.AuthTestResponse;
import com.slack.api.methods.response.chat.ChatPostMessageResponse;
import com.slack.api.methods.response.conversations.ConversationsHistoryResponse;
import com.slack.api.methods.response.conversations.ConversationsInfoResponse;
import com.slack.api.methods.response.conversations.ConversationsListResponse;
import com.slack.api.methods.response.conversations.ConversationsMembersResponse;
import com.slack.api.methods.response.users.UsersListResponse;
import com.slack.api.methods.response.users.UsersInfoResponse;
import com.slack.api.methods.response.users.profile.UsersProfileGetResponse;

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
            UsersInfoResponse user = methods.usersInfo(
                    com.slack.api.methods.request.users.UsersInfoRequest.builder().user("U1").build());
            require(user.isOk() && user.getUser() != null && "U1".equals(user.getUser().getId()), "users.info failed");
            UsersProfileGetResponse profile = methods.usersProfileGet(
                    com.slack.api.methods.request.users.profile.UsersProfileGetRequest.builder().user("U1").build());
            require(profile.isOk() && profile.getProfile() != null
                            && "alice".equals(profile.getProfile().getDisplayName()), "users.profile.get failed");

            ConversationsHistoryResponse history = methods.conversationsHistory(
                    com.slack.api.methods.request.conversations.ConversationsHistoryRequest.builder()
                            .channel("C1")
                            .limit(1)
                            .build());
            require(history.isOk(), "conversations.history failed: " + history.getError());
            require(history.getMessages() != null && history.getMessages().size() == 1, "history page mismatch");

            UsersListResponse users = methods.usersList(
                    com.slack.api.methods.request.users.UsersListRequest.builder().limit(1).build());
            require(users.isOk(), "users.list failed: " + users.getError());
            require(users.getMembers() != null && users.getMembers().size() == 1, "users page mismatch");
            require(users.getResponseMetadata() != null
                            && (users.getResponseMetadata().getNextCursor() == null
                                    || users.getResponseMetadata().getNextCursor().isBlank()),
                    "users cursor mismatch");

            ApiTestResponse synthetic = methods.apiTest(
                    com.slack.api.methods.request.api.ApiTestRequest.builder().error("synthetic").build());
            require(!synthetic.isOk() && "synthetic".equals(synthetic.getError()), "SDK error decoding failed");
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
