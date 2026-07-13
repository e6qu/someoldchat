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
