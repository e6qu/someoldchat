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
let eventEnvelopeID;
const event = new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error("Socket Mode event was not received")), 5000);
  client.once("error", reject);
  client.on("message", async ({ event: message, envelope_id, ack }) => {
    try {
      if (message.text !== "socket qualification event") {
        await ack();
        return;
      }
      assert.equal(message.channel, "C1");
      assert.equal(message.user, "U1");
      eventReceived = message;
      eventEnvelopeID = envelope_id;
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
  assert.equal(eventReceived.type, "message");
  assert.equal(eventReceived.channel, "C1");
  assert.equal(eventReceived.user, "U1");
  assert.equal(eventReceived.text, "socket qualification event");
  assert.match(eventReceived.ts, /^\d+\.\d{6}$/);
  assert.equal(eventReceived.event_ts, eventReceived.ts);
  assert.equal(typeof eventEnvelopeID, "string");

  const response = await fetch(`${qualificationUrl}/qualification/socket-mode-response?envelope_id=${encodeURIComponent(eventEnvelopeID)}`);
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { response_action: "qualification_ack" });
} finally {
  await client.disconnect();
}

const interactions = new SocketModeClient({
  appToken: process.env.SAMEOLDCHAT_INTERACTION_APP_TOKEN ?? "xapp-interactions",
  autoReconnectEnabled: false,
  clientOptions: { slackApiUrl: apiUrl },
});

function expectedDelivery(eventName, verify, responsePayload) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`Socket Mode ${eventName} envelope was not received`)), 5000);
    interactions.once(eventName, async ({ body, ack }) => {
      try {
        verify(body);
        await ack(responsePayload);
        clearTimeout(timer);
        resolve();
      } catch (error) {
        clearTimeout(timer);
        reject(error);
      }
    });
  });
}

async function waitForState(predicate, description) {
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    const response = await fetch(`${qualificationUrl}/qualification/interaction-state`);
    if (response.status !== 200) {
      throw new Error(`interaction state returned HTTP ${response.status}: ${await response.text()}`);
    }
    const state = await response.json();
    if (predicate(state)) return state;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`Socket Mode ${description} response was not applied`);
}

const slash = expectedDelivery("slash_commands", (body) => {
  assert.equal(body.api_app_id, "A2");
  assert.equal(body.team_id, "T1");
  assert.equal(body.channel_id, "C1");
  assert.equal(body.user_id, "U1");
  assert.equal(body.command, "/sdk-deploy");
  assert.equal(body.text, "production");
  assert.match(body.trigger_id, /^trigger_/);
  assert.match(body.response_url, /^http:\/\/127\.0\.0\.1:18080\/app-response\//);
}, { text: "SDK deployment queued" });

try {
  const connection = await interactions.start();
  assert.equal(connection.ok, true);
  let response = await fetch(`${qualificationUrl}/qualification/socket-slash`, { method: "POST" });
  assert.equal(response.status, 204, await response.text());
  await slash;
  const slashState = await waitForState(
    (state) => state.ephemeral_count === 1 && state.ephemeral_text === "SDK deployment queued",
    "slash command",
  );
  assert.equal(slashState.message_text, "SDK deployment");

  const shortcut = expectedDelivery("interactive", (body) => {
    assert.equal(body.type, "shortcut");
    assert.equal(body.api_app_id, "A2");
    assert.equal(body.callback_id, "create_sdk_deployment");
    assert.equal(body.team.id, "T1");
    assert.equal(body.user.id, "U1");
    assert.match(body.trigger_id, /^trigger_/);
    assert.equal(body.channel, undefined);
    assert.equal(body.message, undefined);
    assert.equal(body.response_url, undefined);
  }, { text: "shortcut acknowledgements are not message responses" });
  response = await fetch(`${qualificationUrl}/qualification/socket-shortcut`, { method: "POST" });
  assert.equal(response.status, 204, await response.text());
  await shortcut;
  const shortcutState = await waitForState(
    (state) => state.ephemeral_count === 1,
    "shortcut",
  );
  assert.equal(shortcutState.ephemeral_text, "SDK deployment queued");
  assert.equal(shortcutState.message_text, "SDK deployment");

  const invalidModal = expectedDelivery("interactive", (body) => {
    assert.equal(body.type, "view_submission");
    assert.equal(body.api_app_id, "A2");
    assert.equal(body.team.id, "T1");
    assert.equal(body.user.id, "U1");
    assert.equal(body.view.type, "modal");
    assert.equal(body.view.title.text, "SDK modal");
    assert.equal(body.view.state.values.release_name.name.type, "plain_text_input");
    assert.equal(body.view.state.values.release_name.name.value, "bad");
  }, { response_action: "errors", errors: { release_name: "Use the full release name" } });
  response = await fetch(`${qualificationUrl}/qualification/socket-modal?value=bad`, { method: "POST" });
  assert.equal(response.status, 204, await response.text());
  await invalidModal;
  const invalidModalState = await waitForState(
    (state) => state.modal_open === true && state.modal_errors?.release_name === "Use the full release name",
    "modal validation",
  );
  assert.match(invalidModalState.modal_state, /"value":"bad"/);

  const validModal = expectedDelivery("interactive", (body) => {
    assert.equal(body.type, "view_submission");
    assert.equal(body.view.state.values.release_name.name.value, "SDK July launch");
  }, {});
  response = await fetch(`${qualificationUrl}/qualification/socket-modal?value=${encodeURIComponent("SDK July launch")}`, { method: "POST" });
  assert.equal(response.status, 204, await response.text());
  await validModal;
  await waitForState(
    (state) => state.modal_open === false,
    "modal close",
  );

  const block = expectedDelivery("interactive", (body) => {
    assert.equal(body.type, "block_actions");
    assert.equal(body.api_app_id, "A2");
    assert.equal(body.team.id, "T1");
    assert.equal(body.channel.id, "C1");
    assert.equal(body.actions.length, 1);
    assert.equal(body.actions[0].type, "button");
    assert.equal(body.actions[0].action_id, "open_build");
    assert.equal(body.actions[0].block_id, "qualification");
    assert.equal(body.actions[0].value, "842");
  }, { replace_original: true, text: "SDK deployment opened" });
  response = await fetch(`${qualificationUrl}/qualification/socket-block`, { method: "POST" });
  assert.equal(response.status, 204, await response.text());
  await block;
  const blockState = await waitForState(
    (state) => state.message_text === "SDK deployment opened",
    "block action",
  );
  assert.equal(blockState.ephemeral_count, 1);

  const options = expectedDelivery("interactive", (body) => {
    assert.equal(body.type, "block_suggestion");
    assert.equal(body.api_app_id, "A2");
    assert.equal(body.team.id, "T1");
    assert.equal(body.user.id, "U1");
    assert.equal(body.block_id, "project");
    assert.equal(body.action_id, "project_select");
    assert.equal(body.value, "prod");
    assert.equal(body.container.type, "message");
    assert.equal(body.container.channel_id, "C1");
    assert.equal(body.message.text, "Choose a project");
  }, {
    option_groups: [{
      label: { type: "plain_text", text: "Projects" },
      options: [{
        text: { type: "plain_text", text: "Production API" },
        value: "api-prod",
        description: { type: "plain_text", text: "Primary service" },
      }],
    }],
  });
  response = await fetch(`${qualificationUrl}/qualification/socket-options`, { method: "POST" });
  if (response.status !== 200) {
    throw new Error(`Socket Mode options returned HTTP ${response.status}: ${await response.text()}`);
  }
  await options;
  assert.deepEqual(await response.json(), [{
    Text: "Production API",
    Value: "api-prod",
    Description: "Primary service",
    Group: "Projects",
  }]);
} finally {
  await interactions.disconnect();
}

console.log("node-socket-mode qualification passed");
