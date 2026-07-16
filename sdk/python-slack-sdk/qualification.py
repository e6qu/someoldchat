import os

from slack_sdk import WebClient
from slack_sdk.errors import SlackApiError


client = WebClient(
    token=os.environ.get("SAMEOLDCHAT_API_TOKEN", "xoxb-test"),
    base_url=os.environ.get("SAMEOLDCHAT_API_URL", "http://127.0.0.1:18080/api/"),
)

assert client.api_test()["ok"] is True
identity = client.auth_test()
assert identity["team_id"] == "T1"
assert identity["user_id"] == "U1"

posted = client.chat_postMessage(channel="C1", text="python sdk qualification")
assert posted["ok"] is True
assert posted["channel"] == "C1"

updated = client.chat_update(channel="C1", ts=posted["ts"], text="python sdk qualification updated")
assert updated["ok"] is True
deleted = client.chat_delete(channel="C1", ts=posted["ts"])
assert deleted["ok"] is True

conversation = client.conversations_info(channel="C1")
assert conversation["ok"] is True
assert conversation["channel"]["id"] == "C1"
members = client.conversations_members(channel="C1", limit=1)
assert members["ok"] is True
assert members["members"] == ["U1"]
conversations = client.conversations_list(limit=1)
assert conversations["ok"] is True
assert len(conversations["channels"]) == 1

user = client.users_info(user="U1")
assert user["ok"] is True
assert user["user"]["id"] == "U1"
profile = client.users_profile_get(user="U1")
assert profile["ok"] is True
assert profile["profile"]["display_name"] == "alice"

history = client.conversations_history(channel="C1", limit=1)
assert history["ok"] is True
assert len(history["messages"]) == 1
assert history["has_more"] is False

users = client.users_list(limit=1)
assert users["ok"] is True
assert len(users["members"]) == 1

try:
    client.api_test(error="synthetic")
except SlackApiError as error:
    assert error.response["ok"] is False
    assert error.response["error"] == "synthetic"
else:
    raise AssertionError("api.test error was not raised")

print("python-slack-sdk qualification passed")
