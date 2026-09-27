import json
import os
import threading
import time
import urllib.request

from slack_bolt import App
from slack_bolt.authorization import AuthorizeResult
from slack_bolt.request import BoltRequest
from slack_sdk import WebClient

# The fixture enforces Slack's rate-limiting contract, as production does, so
# these clients retry a 429 after its Retry-After the way a real app is
# configured to. Without the handler the pinned client surfaces the first 429.
from slack_sdk.http_retry.builtin_handlers import RateLimitErrorRetryHandler
from slack_sdk.http_retry import default_retry_handlers


class WebClient(WebClient):
    def __init__(self, *args, **kwargs):
        kwargs.setdefault("retry_handlers", default_retry_handlers() + [RateLimitErrorRetryHandler(max_retry_count=10)])
        super().__init__(*args, **kwargs)


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

# A remote custom function over signed HTTP, in Slack's current shape:
# function_executed hands the listener an execution-scoped client, the button
# it posts routes back with function_data, and complete() inside the action
# listener finishes the execution with the execution's own token.
fixture_url = base_url.rsplit("/api/", 1)[0]
function_app = App(
    client=WebClient(token=token, base_url=base_url),
    authorize=authorize,
    signing_secret="qualification-signing",
)
posted = {}
completed = {}
failures = []


@function_app.function("approval")
def run_approval(inputs, client, event):
    try:
        assert inputs["ticket"] == "INC-42"
        assert event["bot_access_token"].startswith("xwfp-")
        message = client.chat_postMessage(
            channel="C1",
            text="Approve " + inputs["ticket"] + "?",
            blocks=[
                {
                    "type": "actions",
                    "block_id": "approval",
                    "elements": [
                        {"type": "button", "action_id": "approve_ticket", "text": {"type": "plain_text", "text": "Approve"}, "value": "approve"}
                    ],
                }
            ],
        )
        posted.update(ts=message["ts"], execution=event["function_execution_id"], token=event["bot_access_token"])
    except Exception as error:  # surfaced by the main thread
        failures.append(error)


@function_app.action("approve_ticket")
def approve(ack, body, inputs, complete, context):
    ack()
    try:
        assert body["function_data"]["function"]["callback_id"] == "approval"
        assert inputs["ticket"] == "INC-42"
        assert body["interactivity"]["interactivity_pointer"] == body["trigger_id"]
        complete(outputs={"decision": "approved"})
        completed.update(execution=context.function_execution_id, token=body["bot_access_token"])
    except Exception as error:  # surfaced by the main thread
        failures.append(error)


def fixture(path, method="GET"):
    request = urllib.request.Request(fixture_url + path, method=method, data=b"" if method == "POST" else None)
    with urllib.request.urlopen(request, timeout=10) as reply:
        text = reply.read().decode()
    return json.loads(text) if text else None


def wait_for(values, description):
    deadline = time.monotonic() + 5
    while not values and not failures and time.monotonic() < deadline:
        time.sleep(0.05)
    if failures:
        raise failures[0]
    assert values, description


threading.Thread(target=lambda: function_app.start(port=19090, http_server_logger_enabled=False), daemon=True).start()
time.sleep(0.5)
execution = fixture("/qualification/bolt-function", "POST")["function_execution_id"]
wait_for(posted, "Bolt's function listener did not post its message")
assert posted["execution"] == execution
state = fixture("/qualification/bolt-function-state?function_execution_id=" + execution + "&ts=" + posted["ts"])
assert state["status"] == "executing", state
assert state["message_function_execution_id"] == execution, state
fixture("/qualification/bolt-function-click?ts=" + posted["ts"], "POST")
wait_for(completed, "Bolt's action listener did not complete the function")
assert completed == {"execution": execution, "token": posted["token"]}, completed
state = fixture("/qualification/bolt-function-state?function_execution_id=" + execution)
assert state["status"] == "completed", state
assert json.loads(state["outputs"]) == {"decision": "approved"}, state
print("python-bolt qualification passed")
