import type { JsonValue } from "cursor-byok:plugin";

function assert(condition: unknown, message = "assertion failed"): asserts condition {
  if (!condition) throw new Error(message);
}
function equal(actual: unknown, expected: unknown): void {
  assert(JSON.stringify(actual) === JSON.stringify(expected), "unexpected worker result");
}

type Packet = Record<string, JsonValue>;
async function* packets(stream: ReadableStream<Uint8Array>): AsyncGenerator<Packet> {
  let buffered = "";
  for await (const chunk of stream.pipeThrough(new TextDecoderStream())) {
    buffered += chunk;
    let newline;
    while ((newline = buffered.indexOf("\n")) >= 0) {
      const line = buffered.slice(0, newline);
      buffered = buffered.slice(newline + 1);
      if (line) yield JSON.parse(line);
    }
  }
  assert(!buffered, "worker ended with an incomplete protocol packet");
}

Deno.test("actual sandboxed worker completes OAuth, discovery, refresh, and streaming lifecycle", async () => {
  const root = Deno.cwd();
  const sdk = `${root}/../../../src/plugin/sdk`;
  const child = new Deno.Command(Deno.execPath(), {
    args: [
      "run",
      "--quiet",
      "--no-config",
      "--no-lock",
      "--no-npm",
      "--no-remote",
      "--no-prompt",
      `--allow-read=${root},${sdk}`,
      `--import-map=${sdk}/import-map.json`,
      `${sdk}/worker.ts`,
      new URL("./main.ts", import.meta.url).href,
    ],
    stdin: "piped",
    stdout: "piped",
    stderr: "piped",
  }).spawn();
  const timeout = setTimeout(() => {
    try {
      child.kill("SIGKILL");
    } catch { /* Already exited. */ }
  }, 15_000);
  const stderr = new Response(child.stderr).text();
  const input = child.stdin.getWriter();
  const output = packets(child.stdout);
  let sequence = 0;
  let refreshes = 0;
  let streamClosed = false;
  const send = (packet: Packet) =>
    input.write(new TextEncoder().encode(JSON.stringify(packet) + "\n"));
  const upstreamEvents = [
    { type: "message_start", message: { usage: { input_tokens: 4, output_tokens: 0 } } },
    { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
    { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "Hello" } },
    { type: "content_block_stop", index: 0 },
    { type: "message_delta", delta: { stop_reason: "end_turn" }, usage: { output_tokens: 1 } },
    { type: "message_stop" },
  ];
  const http = (body: JsonValue) => ({ status: 200, headers: {}, body: JSON.stringify(body) });

  async function hostCall(packet: Packet): Promise<void> {
    const params = packet.params as Record<string, JsonValue>;
    let result: JsonValue;
    if (packet.method === "network.fetch") {
      if (params.url === "https://platform.claude.com/v1/oauth/token") {
        const body = JSON.parse(params.body as string);
        if (body.grant_type === "authorization_code") {
          equal(body.state, "host-state");
          equal(body.redirect_uri, "http://localhost:43210/callback");
          equal(body.code_verifier, "host-verifier");
        } else {
          equal(body.grant_type, "refresh_token");
          equal(body.refresh_token, "initial-refresh");
          refreshes++;
        }
        result = http({
          access_token: refreshes ? "rotated-access" : "initial-access",
          refresh_token: refreshes ? "rotated-refresh" : "initial-refresh",
          expires_in: 3600,
          scope: "user:profile user:inference",
        });
      } else if (params.url === "https://api.anthropic.com/api/oauth/profile") {
        result = http({
          account: { uuid: "account", email: "test@example.com" },
          organization: { uuid: "org" },
        });
      } else if (String(params.url).startsWith("https://api.anthropic.com/v1/models?")) {
        result = http({
          data: [{ id: "claude-test", display_name: "Test model" }],
          has_more: false,
        });
      } else throw new Error("unexpected auxiliary endpoint");
    } else if (packet.method === "network.stream.open") {
      equal(params.url, "https://api.anthropic.com/v1/messages");
      equal((params.headers as Packet).authorization, "Bearer rotated-access");
      equal(JSON.parse(params.body as string).model, "claude-test");
      result = { streamId: "stream-1", status: 200, headers: {} };
    } else if (packet.method === "network.stream.read") {
      equal(params.streamId, "stream-1");
      result = {
        lines: upstreamEvents.flatMap((event) => [`data: ${JSON.stringify(event)}`, ""]),
        done: true,
      };
    } else if (packet.method === "network.stream.close") {
      streamClosed = true;
      result = null;
    } else throw new Error("unexpected host method");
    await send({ type: "host_result", id: packet.id, result });
  }

  async function invoke(
    method: string,
    params: Packet,
  ): Promise<{ result: JsonValue; events: JsonValue[] }> {
    const id = `test-${++sequence}`;
    await send({ type: "request", id, method, params });
    const events: JsonValue[] = [];
    for (;;) {
      const item = await output.next();
      assert(!item.done, "worker exited before its terminal result");
      const packet = item.value;
      if (packet.type === "host_call") {
        await hostCall(packet);
      } else {
        equal(packet.id, id);
        if (packet.type === "event") events.push(packet.event);
        else {
          equal(packet.type, "result");
          assert(!packet.error, String(packet.error));
          return { result: packet.result, events };
        }
      }
    }
  }

  try {
    const common = { resourceType: "claude-account", methodId: "claude-subscription" };
    const begin = (await invoke("oauth.begin", {
      ...common,
      authorization: {
        redirectUri: "http://127.0.0.1:43210/callback",
        state: "host-state",
        codeChallenge: "host-challenge",
      },
    })).result as Packet;
    assert(String(begin.authorizationUrl).startsWith("https://claude.ai/oauth/authorize?"));
    const drafts = (await invoke("oauth.complete", {
      ...common,
      session: begin.session,
      authorization: {
        redirectUri: "http://127.0.0.1:43210/callback",
        code: "test-code",
        codeVerifier: "host-verifier",
      },
    })).result as Packet[];
    const account: Packet = { id: "resource-1", type: "claude-account", ...drafts[0] };
    equal(account.key, "claude:org:account");
    const views =
      (await invoke("resource.present", { resourceType: "claude-account", resources: [account] }))
        .result;
    assert(!JSON.stringify(views).includes("initial-access"));
    assert(!JSON.stringify(views).includes("initial-refresh"));
    const models = (await invoke("models.list", { providerId: "claude", resource: account }))
      .result as Packet[];
    equal(models[0].id, "claude-test");
    // Expiry triggers refresh inside provider.invoke, not a separate manually refreshed lifecycle.
    (account.privateData as Packet).expiresAtMs = Date.now() - 1;
    const call = await invoke("provider.invoke", {
      providerId: "claude",
      resource: account,
      model: models[0],
      request: {
        instructions: "Help with code.",
        messages: [{ role: "user", content: [{ type: "text", text: "Hello" }] }],
        tools: [],
        reasoning: { enabled: false, effort: null },
        latency: "standard",
        maxOutputTokens: 1024,
        cacheKey: "conversation",
      },
    });
    equal((call.result as Packet).status, "completed");
    const patch = (call.result as Packet).patch as Packet;
    equal((patch.privateData as Packet).refreshToken, "rotated-refresh");
    equal(patch.state, { status: "ready" });
    equal(refreshes, 1);
    assert(call.events.some((event) => (event as Packet).type === "text-delta"));
    equal(call.events.at(-1), { type: "done", reason: "stop" });
    // The close host call may trail the result; a subsequent request drains it as in the real host.
    await invoke("resource.present", {
      resourceType: "claude-account",
      resources: [{ ...account, ...patch }],
    });
    assert(streamClosed, "worker did not release its host stream");
  } finally {
    await input.close();
    const status = await child.status;
    clearTimeout(timeout);
    await output.return(undefined);
    assert(status.success, `worker failed: ${await stderr}`);
  }
});
