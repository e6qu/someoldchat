import os

from slack_bolt import App
from slack_bolt.authorization import AuthorizeResult
from slack_bolt.request import BoltRequest
from slack_sdk import WebClient


token = os.environ.get("SAMEOLDCHAT_API_TOKEN", "xoxb-test")
base_url = os.environ.get("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/")


def authorize(**_kwargs):
    return AuthorizeResult(
        enterprise_id=None,
        team_id="T1",
        bot_id="B1",
        bot_user_id="U1",
        bot_token=token,
    )


app = App(
    client=WebClient(token=token, base_url=base_url),
    authorize=authorize,
    signing_secret="qualification-only",
)

received = False


@app.event("message")
def handle_message(event, client):
    global received
    received = True
    assert event["channel"] == "C1"
    assert event["text"] == "qualification event"
    assert client.api_test()["ok"] is True


response = app.dispatch(
    BoltRequest(
        body={
            "type": "event_callback",
            "team_id": "T1",
            "api_app_id": "A1",
            "event_id": "Ev1",
            "event_time": 1,
            "event": {
                "type": "message",
                "channel": "C1",
                "user": "U2",
                "text": "qualification event",
                "ts": "1.000000",
                "event_ts": "1.000000",
            },
        },
        mode="socket_mode",
    )
)
assert response.status == 200
assert received is True, response.body
print("python-bolt qualification passed")
