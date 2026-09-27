import assert from "node:assert/strict";
import { App } from "@slack/bolt";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const token = process.env.SAMEOLDCHAT_API_TOKEN ?? "xoxb-test";
const app = new App({
  signingSecret: "qualification-signing",
  clientOptions: { slackApiUrl: apiUrl },
  authorize: async () => ({ botToken: token, teamId: "T1", userId: "U1" }),
});

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function within(promise, message) {
  return Promise.race([
    promise,
    new Promise((_, reject) => setTimeout(() => reject(new Error(message)), 3000)),
  ]);
}

const reactionReceived = deferred();
app.event("reaction_added", async ({ event, client }) => {
  try {
    assert.equal(event.item.channel, "C1");
    assert.equal(event.reaction, "wave");
    assert.equal(event.user, "U1");
    assert.equal((await client.api.test()).ok, true);
    reactionReceived.resolve();
  } catch (error) {
    reactionReceived.reject(error);
  }
});

// The unfurl round trip: a link on the app's unfurl domain arrives as
// link_shared, and the app answers it by unfurl_id and source, as a Bolt
// unfurl handler written against Slack does.
const linkUrl = "https://docs.qualification.example/spec/42";
const unfurled = deferred();
app.event("link_shared", async ({ event, client }) => {
  try {
    assert.equal(event.channel, "C1");
    assert.equal(event.source, "conversations_history");
    assert.equal(typeof event.unfurl_id, "string");
    assert.equal(typeof event.message_ts, "string");
    assert.equal(event.is_bot_user_member, true);
    assert.deepEqual(event.links, [{ domain: "qualification.example", url: linkUrl }]);
    const result = await client.chat.unfurl({
      unfurl_id: event.unfurl_id,
      source: event.source,
      unfurls: { [linkUrl]: { title: "Spec 42", text: "Unfurled by Bolt" } },
    });
    assert.equal(result.ok, true);
    unfurled.resolve(event.message_ts);
  } catch (error) {
    unfurled.reject(error);
  }
});

await app.start(19090);
try {
  const response = await fetch("http://127.0.0.1:18080/qualification/bolt-event", { method: "POST" });
  assert.equal(response.status, 204, await response.text());
  await within(reactionReceived.promise, "Bolt did not receive the signed Events API request");

  const posted = await app.client.chat.postMessage({ token, channel: "C1", text: `Spec: <${linkUrl}|spec 42>` });
  assert.equal(posted.ok, true);
  const delivered = await fetch("http://127.0.0.1:18080/qualification/bolt-deliver", { method: "POST" });
  assert.equal(delivered.status, 204, await delivered.text());
  const messageTs = await within(unfurled.promise, "Bolt did not receive link_shared for the posted link");
  assert.equal(messageTs, posted.ts);
  const history = await app.client.conversations.history({ token, channel: "C1", latest: posted.ts, inclusive: true, limit: 1 });
  assert.equal(history.ok, true);
  const attachment = history.messages[0].attachments?.find((candidate) => candidate.app_unfurl_url === linkUrl);
  assert.ok(attachment, JSON.stringify(history.messages[0]));
  assert.equal(attachment.is_app_unfurl, true);
  assert.equal(attachment.title, "Spec 42");
} finally {
  await app.stop();
}
console.log("node-bolt qualification passed");
