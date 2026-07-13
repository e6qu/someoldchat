package sameoldchat.qualification;

import com.slack.api.Slack;
import com.slack.api.SlackConfig;
import com.slack.api.bolt.App;
import com.slack.api.bolt.AppConfig;
import com.slack.api.bolt.request.RequestHeaders;
import com.slack.api.bolt.request.builtin.EventRequest;
import com.slack.api.bolt.response.Response;
import com.slack.api.model.event.MessageEvent;

import java.util.Map;

public final class BoltQualification {
    private BoltQualification() {}

    public static void main(String[] args) throws Exception {
        String token = env("SAMEOLDCHAT_API_TOKEN", "xoxb-test");
        String baseUrl = env("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/");

        SlackConfig slackConfig = new SlackConfig();
        slackConfig.setMethodsEndpointUrlPrefix(baseUrl);
        slackConfig.setTokenExistenceVerificationEnabled(false);
        try (Slack slack = Slack.getInstance(slackConfig)) {
            AppConfig appConfig = AppConfig.builder()
                    .slack(slack)
                    .singleTeamBotToken(token)
                    .signingSecret("qualification-only")
                    .requestVerificationEnabled(false)
                    .ignoringSelfEventsEnabled(false)
                    .build();
            App app = new App(appConfig);
            boolean[] received = {false};
            app.event(MessageEvent.class, (payload, context) -> {
                MessageEvent event = payload.getEvent();
                received[0] = "C1".equals(event.getChannel()) && "qualification event".equals(event.getText());
                Qualification.require(received[0], "Java Bolt event fields were not decoded");
                Qualification.require(
                        context.client().apiTest(com.slack.api.methods.request.api.ApiTestRequest.builder().build()).isOk(),
                        "Java Bolt context client failed");
                return context.ack();
            });

            String body = "{\"type\":\"event_callback\",\"team_id\":\"T1\","
                    + "\"api_app_id\":\"A1\",\"event_id\":\"Ev1\",\"event_time\":1,"
                    + "\"event\":{\"type\":\"message\",\"channel\":\"C1\","
                    + "\"user\":\"U2\",\"text\":\"qualification event\","
                    + "\"ts\":\"1.000000\",\"event_ts\":\"1.000000\"}}";
            EventRequest request = new EventRequest(body, new RequestHeaders(Map.of()));
            request.setSocketMode(true);
            Response response = app.run(request);
            Qualification.require(response.getStatusCode() == 200, "Java Bolt response was not acknowledged");
            Qualification.require(received[0], "Java Bolt listener did not run");
        }
        System.out.println("java-bolt qualification passed");
    }

    private static String env(String name, String defaultValue) {
        String value = System.getenv(name);
        return value == null || value.isBlank() ? defaultValue : value;
    }
}
