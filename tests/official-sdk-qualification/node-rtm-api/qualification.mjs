import assert from "node:assert/strict";
import { RTMClient } from "@slack/rtm-api";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const token = process.env.SAMEOLDCHAT_API_TOKEN ?? "xoxb-test";

const client = new RTMClient(token, {
  slackApiUrl: apiUrl,
  autoReconnect: false,
  useRtmConnect: true,
});

// The RTM socket carries events from the moment it opens; Slack does not
// replay history onto it. So this qualification posts its own event *after*
// the socket is up. It used to assert on a message the fixture had already
// seeded at boot, which meant it only passed against a server that replayed
// the whole journal to every new reader — the exact defect this now guards.
const event = new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error("RTM event was not received")), 5000);
  client.on("message", (message) => {
    // Anything the fixture seeded at boot predates this socket. Receiving one
    // means the server replayed its journal to a new reader instead of opening
    // at the connection's cursor.
    if (message.text === "socket qualification event") {
      clearTimeout(timer);
      reject(new Error(`RTM replayed an event that predates the connection: ${message.text}`));
      return;
    }
    if (message.type !== "message" || message.text !== "rtm qualification event") {
      return;
    }
    try {
      assert.equal(message.channel, "C1");
      assert.equal(message.user, "U1");
      assert.match(message.ts, /^\d+\.\d{6}$/);
      assert.equal(message.event_ts, message.ts);
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

  const posted = await fetch(new URL("chat.postMessage", apiUrl), {
    method: "POST",
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/x-www-form-urlencoded",
    },
    body: new URLSearchParams({ channel: "C1", text: "rtm qualification event" }),
  });
  assert.equal(posted.status, 200);
  assert.equal((await posted.json()).ok, true);

  await event;
} finally {
  await client.disconnect();
}

console.log("node-rtm-api qualification passed");
