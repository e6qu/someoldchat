import json
import os
import threading
from urllib.request import urlopen

from slack_sdk.socket_mode import SocketModeClient
from slack_sdk.socket_mode.response import SocketModeResponse
from slack_sdk.web import WebClient


api_url = os.environ.get("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/")
qualification_url = os.environ.get("SAMEOLDCHAT_QUALIFICATION_URL", "http://127.0.0.1:18080")
received = threading.Event()
errors = []


def handle_request(client, request):
    try:
        assert request.type == "events_api", request.type
        assert request.payload["event"] == {
            "type": "message",
            "channel": "C1",
            "user": "U1",
            "text": "socket qualification event",
            "ts": "1.000000",
            "event_ts": "1.000000",
        }, request.payload
        client.send_socket_mode_response(
            SocketModeResponse(
                envelope_id=request.envelope_id,
                payload={"response_action": "qualification_ack"},
            )
        )
    except Exception as error:
        errors.append(error)
    finally:
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
        f"{qualification_url}/qualification/socket-mode-response?envelope_id=qualification-socket-event",
        timeout=5,
    ) as response:
        assert response.status == 200, response.status
        assert json.load(response) == {"response_action": "qualification_ack"}
finally:
    client.close()

print("python-socket-mode qualification passed")
