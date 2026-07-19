import assert from "node:assert/strict";
import { SocketModeClient } from "@slack/socket-mode";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const qualificationUrl = process.env.SAMEOLDCHAT_QUALIFICATION_URL ?? "http://127.0.0.1:18080";

const client = new SocketModeClient({
  appToken: process.env.SAMEOLDCHAT_APP_TOKEN ?? "xapp-test",
  autoReconnectEnabled: false,
  clientOptions: { slackApiUrl: apiUrl },
});

let eventReceived;
const event = new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error("Socket Mode event was not received")), 5000);
  client.once("error", reject);
  client.on("message", async ({ event: message, ack }) => {
    try {
      assert.equal(message.channel, "C1");
      assert.equal(message.user, "U1");
      assert.equal(message.text, "socket qualification event");
      eventReceived = message;
      await ack({ response_action: "qualification_ack" });
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
  await event;
  assert.deepEqual(eventReceived, {
    type: "message",
    channel: "C1",
    user: "U1",
    text: "socket qualification event",
    ts: "1.000000",
    event_ts: "1.000000",
  });

  const response = await fetch(`${qualificationUrl}/qualification/socket-mode-response?envelope_id=qualification-socket-event`);
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { response_action: "qualification_ack" });
} finally {
  await client.disconnect();
}

console.log("node-socket-mode qualification passed");
