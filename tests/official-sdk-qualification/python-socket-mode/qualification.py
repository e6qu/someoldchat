import json
import os
import re
import threading
from urllib.request import urlopen

from slack_sdk.socket_mode import SocketModeClient
from slack_sdk.socket_mode.response import SocketModeResponse
from slack_sdk.web import WebClient

# The fixture enforces Slack's rate-limiting contract, as production does, so
# these clients retry a 429 after its Retry-After the way a real app is
# configured to. Without the handler the pinned client surfaces the first 429.
from slack_sdk.http_retry.builtin_handlers import RateLimitErrorRetryHandler
from slack_sdk.http_retry import default_retry_handlers


class WebClient(WebClient):
    def __init__(self, *args, **kwargs):
        kwargs.setdefault("retry_handlers", default_retry_handlers() + [RateLimitErrorRetryHandler(max_retry_count=10)])
        super().__init__(*args, **kwargs)


api_url = os.environ.get("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/")
qualification_url = os.environ.get("SAMEOLDCHAT_QUALIFICATION_URL", "http://127.0.0.1:18080")
received = threading.Event()
errors = []
event_envelope_ids = []


def handle_request(client, request):
    try:
        assert request.type == "events_api", request.type
        event = request.payload["event"]
        if event.get("text") != "socket qualification event":
            client.send_socket_mode_response(
                SocketModeResponse(envelope_id=request.envelope_id)
            )
            return
        assert event["type"] == "message", event
        assert event["channel"] == "C1", event
        assert event["user"] == "U1", event
        assert event["text"] == "socket qualification event", event
        assert re.fullmatch(r"\d+\.\d{6}", event["ts"]), event
        assert event["event_ts"] == event["ts"], event
        expected = {
            "type": "message",
            "channel": "C1",
            # Slack names the conversation's type on every message event;
            # Bolt's Assistant and channel_type filters key on it.
            "channel_type": "channel",
            "user": "U1",
            "text": "socket qualification event",
            "ts": event["ts"],
            "event_ts": event["ts"],
        }
        assert event == expected, request.payload
        event_envelope_ids.append(request.envelope_id)
        client.send_socket_mode_response(
            SocketModeResponse(
                envelope_id=request.envelope_id,
                payload={"response_action": "qualification_ack"},
            )
        )
    except Exception as error:
        errors.append(error)
        received.set()
    else:
        received.set()


client = SocketModeClient(
    app_token=os.environ.get("SAMEOLDCHAT_APP_TOKEN", "xapp-test"),
    web_client=WebClient(token="xoxb-test", base_url=api_url),
    auto_reconnect_enabled=False,
    ping_interval=1,
)
client.socket_mode_request_listeners.append(handle_request)
try:
    client.connect()
    assert received.wait(5), "Socket Mode event was not received"
    if errors:
        raise errors[0]
    with urlopen(
        f"{qualification_url}/qualification/socket-mode-response?envelope_id={event_envelope_ids[0]}",
        timeout=5,
    ) as response:
        assert response.status == 200, response.status
        assert json.load(response) == {"response_action": "qualification_ack"}
finally:
    client.close()

print("python-socket-mode qualification passed")
