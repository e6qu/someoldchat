import assert from "node:assert/strict";
import { App } from "@slack/bolt";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const token = process.env.SAMEOLDCHAT_API_TOKEN ?? "xoxb-test";
const app = new App({
  signingSecret: "qualification-signing",
  clientOptions: { slackApiUrl: apiUrl },
  authorize: async () => ({ botToken: token, teamId: "T1", userId: "U1" }),
});

let resolveReceived;
let rejectReceived;
const received = new Promise((resolve, reject) => {
  resolveReceived = resolve;
  rejectReceived = reject;
});
app.event("reaction_added", async ({ event, client }) => {
  try {
    assert.equal(event.item.channel, "C1");
    assert.equal(event.reaction, "wave");
    assert.equal(event.user, "U1");
    assert.equal((await client.api.test()).ok, true);
    resolveReceived();
  } catch (error) {
    rejectReceived(error);
  }
});

await app.start(19090);
try {
  const response = await fetch("http://127.0.0.1:18080/qualification/bolt-event", { method: "POST" });
  assert.equal(response.status, 204, await response.text());
  await Promise.race([
    received,
    new Promise((_, reject) => setTimeout(() => reject(new Error("Bolt did not receive the signed Events API request")), 3000)),
  ]);
} finally {
  await app.stop();
}
console.log("node-bolt qualification passed");
