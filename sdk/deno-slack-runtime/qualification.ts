const runtimeURL = "https://deno.land/x/deno_slack_runtime@1.1.3/mod.ts";

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

const root = await Deno.makeTempDir({ prefix: "sameoldchat-deno-runtime-" });
const functions = `${root}/functions`;
await Deno.mkdir(functions);
await Deno.writeTextFile(
  `${functions}/qualification.js`,
  `export default ({ inputs }) => ({ completed: true, outputs: { echo: inputs.value } });\n`,
);

const apiPort = await availablePort();
const runtimePort = await availablePort();
let completion: Record<string, unknown> | undefined;
const apiServer = Deno.serve({ hostname: "127.0.0.1", port: apiPort }, async (request) => {
  if (new URL(request.url).pathname !== "/api/functions.completeSuccess" || request.method !== "POST") {
    return new Response("not found", { status: 404 });
  }
  completion = await request.json();
  return Response.json({ ok: true });
});

const command = new Deno.Command(Deno.execPath(), {
  args: ["run", "--quiet", "--allow-read", "--allow-net", runtimeURL, "-p", String(runtimePort)],
  cwd: root,
  stdout: "piped",
  stderr: "piped",
  env: { ...Deno.env.toObject(), SLACK_API_URL: `http://127.0.0.1:${apiPort}/api` },
});
const child = command.spawn();
try {
  await waitForHealth(runtimePort);
  const response = await fetch(`http://127.0.0.1:${runtimePort}/functions`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      body: {
        event: {
          type: "function_executed",
          function: { callback_id: "qualification" },
          function_execution_id: "execution-1",
          inputs: { value: "qualified" },
          bot_access_token: "xoxb-test",
        },
      },
      context: { bot_access_token: "xoxb-test", team_id: "T1", variables: {} },
    }),
  });
  if (response.status !== 200) throw new Error(`functions returned HTTP ${response.status}`);
  await response.text();
  if (completion?.function_execution_id !== "execution-1" || (completion.outputs as Record<string, unknown>)?.echo !== "qualified") {
    throw new Error(`unexpected completion payload: ${JSON.stringify(completion)}`);
  }
} finally {
  child.kill("SIGTERM");
  await child.status;
  await apiServer.shutdown();
  await Deno.remove(root, { recursive: true });
}

console.log("deno-slack-runtime 1.1.3 qualification passed");
