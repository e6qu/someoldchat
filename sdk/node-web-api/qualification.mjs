import assert from "node:assert/strict";
import { WebClient } from "@slack/web-api";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const token = process.env.SAMEOLDCHAT_API_TOKEN ?? "xoxb-test";
const client = new WebClient(token, { slackApiUrl: apiUrl });

const success = await client.api.test();
assert.equal(success.ok, true);

const identity = await client.auth.test();
assert.equal(identity.ok, true);
assert.equal(identity.team_id, "T1");
assert.equal(identity.user_id, "U1");

const posted = await client.chat.postMessage({ channel: "C1", text: "node sdk qualification" });
assert.equal(posted.ok, true);
assert.equal(posted.channel, "C1");
assert.equal(typeof posted.ts, "string");

const updated = await client.chat.update({ channel: "C1", ts: posted.ts, text: "node sdk qualification updated" });
assert.equal(updated.ok, true);
const deleted = await client.chat.delete({ channel: "C1", ts: posted.ts });
assert.equal(deleted.ok, true);

const conversation = await client.conversations.info({ channel: "C1" });
assert.equal(conversation.ok, true);
assert.equal(conversation.channel.id, "C1");
const members = await client.conversations.members({ channel: "C1", limit: 1 });
assert.equal(members.ok, true);
assert.deepEqual(members.members, ["U1"]);
const conversations = await client.conversations.list({ limit: 1 });
assert.equal(conversations.ok, true);
assert.equal(conversations.channels.length, 1);

const user = await client.users.info({ user: "U1" });
assert.equal(user.ok, true);
assert.equal(user.user.id, "U1");
const profile = await client.users.profile.get({ user: "U1" });
assert.equal(profile.ok, true);
assert.equal(profile.profile.display_name, "alice");

const history = await client.conversations.history({ channel: "C1", limit: 1 });
assert.equal(history.ok, true);
assert.equal(history.messages.length, 1);
assert.equal(history.has_more, false);

const users = await client.users.list({ limit: 1 });
assert.equal(users.ok, true);
assert.equal(users.members.length, 1);
assert.equal(users.response_metadata?.next_cursor ?? "", "");

await assert.rejects(
	client.api.test({ error: "synthetic" }),
	(error) => error?.data?.ok === false && error.data.error === "synthetic",
);

console.log("node-web-api qualification passed");
