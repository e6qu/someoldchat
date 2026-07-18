import assert from "node:assert/strict";
import { App } from "@slack/bolt";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const token = process.env.SAMEOLDCHAT_API_TOKEN ?? "xoxb-test";
const app = new App({
  signingSecret: "qualification-only",
  clientOptions: { slackApiUrl: apiUrl },
  authorize: async () => ({ botToken: token, teamId: "T1", userId: "U1" }),
});

let received = false;
app.message(async ({ message, client }) => {
  received = true;
  assert.equal(message.channel, "C1");
  assert.equal(message.text, "qualification event");
  assert.equal((await client.api.test()).ok, true);
});

await app.processEvent({
  body: {
    type: "event_callback",
    team_id: "T1",
    api_app_id: "A1",
    event_id: "Ev1",
    event_time: 1,
    event: {
      type: "message",
      channel: "C1",
      user: "U1",
      text: "qualification event",
      ts: "1.000000",
      event_ts: "1.000000",
    },
  },
  ack: async () => {},
});
assert.equal(received, true);
console.log("node-bolt qualification passed");
