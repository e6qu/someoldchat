package sameoldchat.qualification;

import com.google.gson.Gson;
import com.google.gson.JsonObject;
import com.slack.api.Slack;
import com.slack.api.SlackConfig;
import com.slack.api.socket_mode.SocketModeClient;
import com.slack.api.socket_mode.request.EventsApiEnvelope;
import java.net.URI;
import java.net.URLEncoder;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.time.Duration;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicReference;

public final class SocketModeQualification {
    private SocketModeQualification() {}

    public static void main(String[] args) throws Exception {
        String token = env("SAMEOLDCHAT_APP_TOKEN", "xapp-test");
        String baseUrl = env("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/");
        String qualificationUrl = env("SAMEOLDCHAT_QUALIFICATION_URL", "http://127.0.0.1:18080");
        CountDownLatch received = new CountDownLatch(1);
        AtomicReference<Throwable> failure = new AtomicReference<>();
        AtomicReference<String> eventEnvelopeId = new AtomicReference<>();

        SlackConfig config = new SlackConfig();
        config.setMethodsEndpointUrlPrefix(baseUrl);
        config.setTokenExistenceVerificationEnabled(false);
        try (Slack slack = Slack.getInstance(config);
             SocketModeClient client = slack.socketMode(token, SocketModeClient.Backend.JavaWebSocket)) {
            client.setAutoReconnectOnCloseEnabled(false);
            client.setSessionMonitorEnabled(false);
            client.addWebSocketErrorListener(error -> {
                failure.compareAndSet(null, error);
                received.countDown();
            });
            client.addEventsApiEnvelopeListener(envelope ->
                    handleEvent(client, envelope, received, failure, eventEnvelopeId));
            client.connect();

            Qualification.require(received.await(5, TimeUnit.SECONDS), "Java Socket Mode event was not received");
            if (failure.get() != null) {
                throw new AssertionError("Java Socket Mode client failed", failure.get());
            }
            HttpClient http = HttpClient.newBuilder().connectTimeout(Duration.ofSeconds(5)).build();
            HttpRequest request = HttpRequest.newBuilder()
                    .uri(URI.create(qualificationUrl + "/qualification/socket-mode-response?envelope_id="
                            + URLEncoder.encode(eventEnvelopeId.get(), StandardCharsets.UTF_8)))
                    .timeout(Duration.ofSeconds(5))
                    .GET()
                    .build();
            HttpResponse<String> response = http.send(request, HttpResponse.BodyHandlers.ofString());
            Qualification.require(response.statusCode() == 200, "Java Socket Mode response status: " + response.statusCode());
            JsonObject payload = new Gson().fromJson(response.body(), JsonObject.class);
            Qualification.require("qualification_ack".equals(payload.get("response_action").getAsString()),
                    "Java Socket Mode response payload mismatch: " + response.body());
        }
        System.out.println("java-socket-mode qualification passed");
    }

    private static void handleEvent(
            SocketModeClient client,
            EventsApiEnvelope envelope,
            CountDownLatch received,
            AtomicReference<Throwable> failure,
            AtomicReference<String> eventEnvelopeId) {
        try {
            JsonObject event = envelope.getPayload().getAsJsonObject().getAsJsonObject("event");
            if (!"socket qualification event".equals(event.get("text").getAsString())) {
                JsonObject response = new JsonObject();
                response.addProperty("envelope_id", envelope.getEnvelopeId());
                client.sendSocketModeResponse(response.toString());
                return;
            }
            Qualification.require("message".equals(event.get("type").getAsString()), "Java Socket Mode event type mismatch");
            Qualification.require("C1".equals(event.get("channel").getAsString()), "Java Socket Mode channel mismatch");
            Qualification.require("U1".equals(event.get("user").getAsString()), "Java Socket Mode user mismatch");
            Qualification.require("socket qualification event".equals(event.get("text").getAsString()),
                    "Java Socket Mode text mismatch");
            Qualification.require(event.get("ts").getAsString().matches("\\d+\\.\\d{6}"),
                    "Java Socket Mode timestamp mismatch");
            Qualification.require(event.get("ts").getAsString().equals(event.get("event_ts").getAsString()),
                    "Java Socket Mode event timestamp mismatch");
            eventEnvelopeId.set(envelope.getEnvelopeId());
            JsonObject response = new JsonObject();
            response.addProperty("envelope_id", envelope.getEnvelopeId());
            JsonObject responsePayload = new JsonObject();
            responsePayload.addProperty("response_action", "qualification_ack");
            response.add("payload", responsePayload);
            client.sendSocketModeResponse(response.toString());
        } catch (Throwable error) {
            failure.compareAndSet(null, error);
        } finally {
            if (failure.get() != null) {
                received.countDown();
            }
        }
        received.countDown();
    }

    private static String env(String name, String defaultValue) {
        String value = System.getenv(name);
        return value == null || value.isBlank() ? defaultValue : value;
    }
}
