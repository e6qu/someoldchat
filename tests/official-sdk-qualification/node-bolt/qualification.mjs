import assert from "node:assert/strict";
import { App } from "@slack/bolt";

const apiUrl = process.env.SAMEOLDCHAT_API_URL ?? "http://127.0.0.1:18080/api/";
const fixtureUrl = new URL("/", apiUrl).toString().replace(/\/$/, "");
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

function within(promise, milliseconds, description) {
  return Promise.race([
    promise,
    new Promise((_, reject) => setTimeout(() => reject(new Error(description)), milliseconds)),
  ]);
}

const received = deferred();
app.event("reaction_added", async ({ event, client }) => {
  try {
    assert.equal(event.item.channel, "C1");
    assert.equal(event.reaction, "wave");
    assert.equal(event.user, "U1");
    assert.equal((await client.api.test()).ok, true);
    received.resolve();
  } catch (error) {
    received.reject(error);
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

// A remote custom function in Slack's current shape: function_executed hands
// the listener an execution-scoped client, the button it posts routes back
// with function_data, and complete() inside the action handler finishes the
// execution with the execution's own token.
const posted = deferred();
const completed = deferred();
app.function("approval", async ({ inputs, client, event }) => {
  try {
    assert.equal(inputs.ticket, "INC-42");
    assert.match(event.bot_access_token, /^xwfp-/);
    const message = await client.chat.postMessage({
      channel: "C1",
      text: `Approve ${inputs.ticket}?`,
      blocks: [
        { type: "section", block_id: "ask", text: { type: "mrkdwn", text: `Approve *${inputs.ticket}*?` } },
        {
          type: "actions",
          block_id: "approval",
          elements: [{ type: "button", action_id: "approve_ticket", text: { type: "plain_text", text: "Approve" }, value: "approve" }],
        },
      ],
    });
    posted.resolve({ ts: message.ts, token: event.bot_access_token, execution: event.function_execution_id });
  } catch (error) {
    posted.reject(error);
  }
});
app.action("approve_ticket", async ({ ack, body, inputs, complete, context }) => {
  await ack();
  try {
    assert.equal(typeof complete, "function", "Bolt did not see a function-scoped action");
    assert.equal(body.function_data.function.callback_id, "approval");
    assert.equal(inputs.ticket, "INC-42");
    assert.equal(body.interactivity.interactor.id, "U1");
    assert.equal(body.interactivity.interactivity_pointer, body.trigger_id);
    await complete({ outputs: { decision: "approved" } });
    completed.resolve({ execution: context.functionExecutionId, token: body.bot_access_token });
  } catch (error) {
    completed.reject(error);
  }
});

async function fixture(path, init) {
  const response = await fetch(`${fixtureUrl}${path}`, init);
  const text = await response.text();
  assert.ok(response.ok, `${path}: ${response.status} ${text}`);
  return text === "" ? undefined : JSON.parse(text);
}

await app.start(19090);
try {
  await fixture("/qualification/bolt-event", { method: "POST" });
  await within(received.promise, 3000, "Bolt did not receive the signed Events API request");

  const shared = await app.client.chat.postMessage({ token, channel: "C1", text: `Spec: <${linkUrl}|spec 42>` });
  assert.equal(shared.ok, true);
  await fixture("/qualification/bolt-deliver", { method: "POST" });
  const messageTs = await within(unfurled.promise, 3000, "Bolt did not receive link_shared for the posted link");
  assert.equal(messageTs, shared.ts);
  const history = await app.client.conversations.history({ token, channel: "C1", latest: shared.ts, inclusive: true, limit: 1 });
  assert.equal(history.ok, true);
  const attachment = history.messages[0].attachments?.find((candidate) => candidate.app_unfurl_url === linkUrl);
  assert.ok(attachment, JSON.stringify(history.messages[0]));
  assert.equal(attachment.is_app_unfurl, true);
  assert.equal(attachment.title, "Spec 42");

  const { function_execution_id: execution } = await fixture("/qualification/bolt-function", { method: "POST" });
  const message = await within(posted.promise, 5000, "Bolt's function listener did not post its message");
  assert.equal(message.execution, execution);
  let state = await fixture(`/qualification/bolt-function-state?function_execution_id=${execution}&ts=${message.ts}`);
  assert.equal(state.status, "executing");
  assert.equal(state.message_function_execution_id, execution, "the execution token did not post the message");

  await fixture(`/qualification/bolt-function-click?ts=${message.ts}`, { method: "POST" });
  const done = await within(completed.promise, 5000, "Bolt's action listener did not complete the function");
  assert.equal(done.execution, execution);
  assert.equal(done.token, message.token);
  state = await fixture(`/qualification/bolt-function-state?function_execution_id=${execution}`);
  assert.equal(state.status, "completed");
  assert.deepEqual(JSON.parse(state.outputs), { decision: "approved" });
} finally {
  await app.stop();
}
console.log("node-bolt qualification passed");
