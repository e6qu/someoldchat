const runtimeURL = Deno.env.get("DENO_SLACK_RUNTIME_URL") ??
  "https://deno.land/x/deno_slack_runtime@1.1.3/mod.ts";

async function availablePort(): Promise<number> {
  const listener = Deno.listen({ hostname: "127.0.0.1", port: 0 });
  const port = (listener.addr as Deno.NetAddr).port;
  listener.close();
  return port;
}

async function waitForHealth(port: number): Promise<void> {
  let lastError = "runtime did not become healthy";
  for (let attempt = 0; attempt < 40; attempt++) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/health`);
      if (response.status === 200 && (await response.text()) === "OK") return;
      lastError = `health returned HTTP ${response.status}`;
    } catch (error) {
      lastError = String(error);
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(lastError);
}

async function waitForCompletion(executionID: string): Promise<Record<string, unknown>> {
  for (let attempt = 0; attempt < 40; attempt++) {
    if (completion?.function_execution_id === executionID) return completion;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  throw new Error(`function completion was not received: ${JSON.stringify(completion)}`);
}

const root = await Deno.makeTempDir({ prefix: "sameoldchat-deno-runtime-" });
const functions = `${root}/functions`;
await Deno.mkdir(functions);
await Deno.writeTextFile(
  `${functions}/qualification.js`,
  `export default ({ inputs }) => ({ completed: true, outputs: { echo: inputs.value } });\n`,
);
await Deno.writeTextFile(
  `${functions}/failure.js`,
  `export default () => ({ error: "qualification failure" });\n`,
);

const apiPort = await availablePort();
const runtimePort = await availablePort();
let completion: Record<string, unknown> | undefined;
const apiServer = Deno.serve({ hostname: "127.0.0.1", port: apiPort }, async (request) => {
  const pathname = new URL(request.url).pathname;
  if (!["/api/functions.completeSuccess", "/api/functions.completeError"].includes(pathname) || request.method !== "POST") {
    return new Response("not found", { status: 404 });
  }
  if (request.headers.get("content-type")?.startsWith("application/x-www-form-urlencoded")) {
    const form = await request.formData();
    completion = {
      function_execution_id: form.get("function_execution_id"),
      ...(pathname.endsWith("completeSuccess")
        ? { outputs: JSON.parse(String(form.get("outputs"))) }
        : { error: form.get("error") }),
    };
  } else {
    completion = await request.json();
  }
  return Response.json({ ok: true });
});

const childEnvironment = { SLACK_API_URL: `http://127.0.0.1:${apiPort}/api/` };
const command = new Deno.Command(Deno.execPath(), {
  args: ["run", "--quiet", "--allow-read", "--allow-net", runtimeURL, "-p", String(runtimePort)],
  cwd: root,
  stdout: "piped",
  stderr: "piped",
  env: childEnvironment,
});
const child = command.spawn();
try {
  await waitForHealth(runtimePort);
  const invoke = (callbackID: string, executionID: string) => fetch(`http://127.0.0.1:${runtimePort}/functions`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      body: {
        event: {
          type: "function_executed",
          function: { callback_id: callbackID },
          function_execution_id: executionID,
          inputs: { value: "qualified" },
          bot_access_token: "xoxb-test",
        },
      },
      context: {
        bot_access_token: "xoxb-test",
        team_id: "T1",
        variables: { SLACK_API_URL: `http://127.0.0.1:${apiPort}/api/` },
      },
    }),
  });
  const response = await invoke("qualification", "execution-1");
  if (response.status !== 200) throw new Error(`functions returned HTTP ${response.status}`);
  await response.text();
  const success = await waitForCompletion("execution-1");
  if ((success.outputs as Record<string, unknown>)?.echo !== "qualified") {
    throw new Error(`unexpected completion payload: ${JSON.stringify(completion)}`);
  }
  const failureResponse = await invoke("failure", "execution-2");
  if (failureResponse.status !== 200) throw new Error(`failure function returned HTTP ${failureResponse.status}`);
  await failureResponse.text();
  const failure = await waitForCompletion("execution-2");
  if (failure.error !== "qualification failure") {
    throw new Error(`unexpected error completion payload: ${JSON.stringify(completion)}`);
  }
} finally {
  child.kill("SIGTERM");
  await child.status;
  await apiServer.shutdown();
  await Deno.remove(root, { recursive: true });
}

console.log("deno-slack-runtime 1.1.3 qualification passed");
