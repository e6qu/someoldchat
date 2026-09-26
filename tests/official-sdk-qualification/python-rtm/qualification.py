import os
import re
import threading
import time
import urllib.parse
import urllib.request

from slack_sdk.rtm_v2 import RTMClient

# slack_sdk.rtm_v2 is the RTM client python-slack-sdk ships today. It speaks
# the WebSocket through slack_sdk's own builtin connection (no third-party
# WebSocket library), resolves the socket through rtm.connect, and learns its
# own bot through auth.test. It then drops every event whose bot_id is that
# bot's: the listener below sees messages other identities posted, and never
# the app's own posts.
#
# That filter is why this suite exists. The server used to name the token
# app's bot on every message a user token issued to that app posted, so the
# client silently dropped a person's message as its own. The suite posts both
# shapes and requires exactly the person's message to reach the listener, and
# the bot's own post, sent first on the same socket, never to.

api_url = os.environ.get("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/")
bot_token = os.environ.get("SAMEOLDCHAT_API_TOKEN", "xoxb-test")
user_token = os.environ.get("SAMEOLDCHAT_USER_TOKEN", "xoxp-reminder-qualification")

received = threading.Event()
hello = threading.Event()
seen = []
errors = []

rtm = RTMClient(token=bot_token, base_url=api_url, auto_reconnect_enabled=False)


@rtm.on("hello")
def on_hello(client, event):
    hello.set()


@rtm.on("message")
def on_message(client, event):
    text = event.get("text")
    seen.append(text)
    if text == "socket qualification event":
        errors.append(f"RTM replayed an event that predates the connection: {event}")
        received.set()
        return
    if text != "python rtm qualification event":
        return
    try:
        assert event["type"] == "message", event
        assert event["channel"] == "C1", event
        assert event["user"] == "U1", event
        assert event["channel_type"] == "channel", event
        assert re.fullmatch(r"\d+\.\d{6}", event["ts"]), event
        assert event["event_ts"] == event["ts"], event
        # A person's post names no bot, even when the token that made it was
        # issued to an app.
        assert "bot_id" not in event, event
    except AssertionError as error:
        errors.append(str(error))
    received.set()


def post(token, text):
    request = urllib.request.Request(
        urllib.parse.urljoin(api_url, "chat.postMessage"),
        data=urllib.parse.urlencode({"channel": "C1", "text": text}).encode(),
        headers={"authorization": f"Bearer {token}"},
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        body = response.read().decode()
    assert '"ok":true' in body, body


try:
    rtm.connect()
    assert hello.wait(5), "RTM hello was not received"
    assert rtm.bot_id == "B1", rtm.bot_id
    post(bot_token, "python rtm own bot event")
    post(user_token, "python rtm qualification event")
    assert received.wait(5), f"RTM message was not received; saw {seen}"
    assert not errors, errors
    time.sleep(0.5)  # let a wrongly delivered own-bot frame reach the listener
    assert "python rtm own bot event" not in seen, f"the client's own bot post reached its listener: {seen}"
finally:
    rtm.close()

print("python-rtm qualification passed")
