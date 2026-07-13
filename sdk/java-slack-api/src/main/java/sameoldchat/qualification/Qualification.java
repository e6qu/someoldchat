package sameoldchat.qualification;

import com.slack.api.Slack;
import com.slack.api.SlackConfig;
import com.slack.api.methods.MethodsClient;
import com.slack.api.methods.response.api.ApiTestResponse;
import com.slack.api.methods.response.auth.AuthTestResponse;
import com.slack.api.methods.response.chat.ChatPostMessageResponse;
import com.slack.api.methods.response.conversations.ConversationsHistoryResponse;
import com.slack.api.methods.response.users.UsersListResponse;

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
