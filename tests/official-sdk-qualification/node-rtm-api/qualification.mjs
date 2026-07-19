import assert from "node:assert/strict";
import { RTMClient } from "@slack/rtm-api";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const token = process.env.SAMEOLDCHAT_API_TOKEN ?? "xoxb-test";

const client = new RTMClient(token, {
  slackApiUrl: apiUrl,
  autoReconnect: false,
  useRtmConnect: true,
});

const event = new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error("RTM event was not received")), 5000);
  client.on("message", (message) => {
    if (message.type !== "message" || message.text !== "rtm qualification event") {
      return;
    }
    try {
      assert.equal(message.channel, "C1");
      assert.equal(message.user, "U1");
      assert.equal(message.ts, "2.000000");
      assert.equal(message.event_ts, "2.000000");
      clearTimeout(timer);
      resolve();
    } catch (error) {
      clearTimeout(timer);
      reject(error);
    }
  });
});

try {
  const connection = await client.start();
  assert.equal(connection.ok, true);
  assert.equal(client.activeTeamId, "T1");
  assert.equal(client.activeUserId, "U1");
  await event;
} finally {
  await client.disconnect();
}

console.log("node-rtm-api qualification passed");
