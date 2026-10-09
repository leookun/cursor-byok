import { __getRegisteredPlugin, type JsonValue, type NetworkEventStream, type PluginContext } from "cursor-byok:plugin";
import type { ModelEvent, ProviderSupport } from "cursor-byok:provider";
import type { ResourceAddMethod, ResourceSupport } from "cursor-byok:resource";

if (Deno.args.length !== 1) throw new Error("plugin entry URL is required");
await import(Deno.args[0]);
const plugin = __getRegisteredPlugin();
const encoder = new TextEncoder();
const writer = Deno.stdout.writable.getWriter();
const pendingHost = new Map<string, { resolve(value: unknown): void; reject(error: Error): void }>();
const controllers = new Map<string, AbortController>();
let hostSequence = 0;
// 事件与最终结果共用一条串行写队列,保证顺序。
let writeQueue = Promise.resolve();

function send(value: unknown): Promise<void> {
  const operation = writeQueue.then(() => writer.write(encoder.encode(JSON.stringify(value) + "\n")));
  writeQueue = operation.catch(() => undefined);
  return operation;
}

function hostCall(requestId: string, method: string, params: unknown): Promise<unknown> {
  const id = `${requestId}:host:${++hostSequence}`;
  return new Promise((resolve, reject) => {
    pendingHost.set(id, { resolve, reject });
    void send({ type: "host_call", id, requestId, method, params });
  });
}

async function* streamLines(requestId: string, streamId: string): AsyncGenerator<string> {
  try {
    for (;;) {
      const chunk = await hostCall(requestId, "network.stream.read", { streamId }) as {
        lines: string[];
        done: boolean;
      };
      for (const line of chunk.lines) yield line;
      if (chunk.done) return;
    }
  } finally {
    void hostCall(requestId, "network.stream.close", { streamId }).catch(() => undefined);
  }
}

function contextFor(requestId: string, signal: AbortSignal): PluginContext {
  return {
    network: {
      fetch: (url, init = {}) => hostCall(requestId, "network.fetch", { url, ...init }) as ReturnType<PluginContext["network"]["fetch"]>,
      stream: async (url, init = {}): Promise<NetworkEventStream> => {
        const opened = await hostCall(requestId, "network.stream.open", { url, ...init }) as {
          streamId: string;
          status: number;
          headers: Record<string, string>;
        };
        return {
          status: opened.status,
          headers: opened.headers,
          lines: streamLines(requestId, opened.streamId),
        };
      },
    },
    signal,
  };
}

function provider(id: unknown): ProviderSupport {
  const found = plugin.providers.find((provider) => provider.id === id);
  if (!found) throw new Error(`unknown plugin provider: ${id}`);
  return found;
}

function resourceSupport(type: unknown): ResourceSupport {
  const found = (plugin.resources ?? []).find((resource) => resource.type === type);
  if (!found) throw new Error(`unknown plugin resource type: ${type}`);
  return found;
}

function addMethod(support: ResourceSupport, methodId: unknown): ResourceAddMethod {
  const found = (support.add ?? []).find((method) => method.id === methodId);
  if (!found) throw new Error(`unknown plugin add method: ${methodId}`);
  return found;
}

async function dispatch(message: { id: string; method: string; params?: JsonValue }) {
  const controller = new AbortController();
  controllers.set(message.id, controller);
  const context = contextFor(message.id, controller.signal);
  const params = (message.params ?? {}) as Record<string, JsonValue>;
  try {
    let result: unknown;
    switch (message.method) {
      case "provider.invoke": {
        const output = {
          emit: (event: ModelEvent) => void send({ type: "event", id: message.id, event }),
        };
        result = await provider(params.providerId).invoke(
          {
            model: params.model as never,
            resource: (params.resource ?? null) as never,
            request: params.request as never,
          },
          output,
          context,
        );
        break;
      }
      case "models.list": {
        const models = provider(params.providerId).models;
        if (!models) throw new Error(`plugin provider ${params.providerId} has no models`);
        result = await models.list({ resource: (params.resource ?? null) as never }, context);
        break;
      }
      case "resource.present": {
        const support = resourceSupport(params.resourceType);
        const resources = Array.isArray(params.resources) ? params.resources : [];
        result = resources.map((resource) => support.present(resource as never));
        break;
      }
      case "resource.prepare": {
        const support = resourceSupport(params.resourceType);
        result = await support.prepare?.(params.resource as never, (params.rejectedResource ?? null) as never, context) ?? null;
        break;
      }
      case "resource.refresh": {
        const support = resourceSupport(params.resourceType);
        if (!support.refresh) throw new Error(`resource ${params.resourceType} has no refresh`);
        result = await support.refresh(params.resource as never, context);
        break;
      }
      case "resource.action": {
        const support = resourceSupport(params.resourceType);
        const action = (support.actions ?? []).find((item) => item.id === params.actionId);
        if (!action) throw new Error(`resource ${params.resourceType} has no action ${params.actionId}`);
        result = await action.run(
          params.resource as never,
          params.input ?? null,
          context,
        );
        break;
      }
      case "resource.remove": {
        const support = resourceSupport(params.resourceType);
        await support.remove?.(params.resource as never, context);
        result = null;
        break;
      }
      case "oauth.begin": {
        const support = resourceSupport(params.resourceType);
        const method = addMethod(support, params.methodId);
        result = method.type === "oauth2.authorization-code" ? await method.begin(params.authorization as never, context) : await method.begin(context);
        break;
      }
      case "oauth.poll": {
        const support = resourceSupport(params.resourceType);
        const method = addMethod(support, params.methodId);
        if (method.type !== "oauth2.0") throw new Error(`add method ${method.id} does not support polling`);
        result = await method.poll(params.session ?? null, context);
        break;
      }
      case "oauth.complete": {
        const support = resourceSupport(params.resourceType);
        const method = addMethod(support, params.methodId);
        if (method.type !== "oauth2.authorization-code") {
          throw new Error(`add method ${method.id} does not support authorization-code completion`);
        }
        result = await method.complete(
          params.session ?? null,
          params.authorization as never,
          context,
        );
        break;
      }
      case "import.parse": {
        const support = resourceSupport(params.resourceType);
        if (!support.import) throw new Error(`resource ${params.resourceType} has no import`);
        const files = Array.isArray(params.files) ? params.files : [];
        result = await support.import.parse(files as never, context);
        break;
      }
      default:
        throw new Error(`unknown plugin method: ${message.method}`);
    }
    await send({ type: "result", id: message.id, result: result ?? null });
  } catch (error) {
    await send({ type: "result", id: message.id, error: error instanceof Error ? error.message : String(error) });
  } finally {
    controllers.delete(message.id);
  }
}

let buffered = "";
for await (const chunk of Deno.stdin.readable.pipeThrough(new TextDecoderStream())) {
  buffered += chunk;
  for (;;) {
    const newline = buffered.indexOf("\n");
    if (newline < 0) break;
    const line = buffered.slice(0, newline);
    buffered = buffered.slice(newline + 1);
    if (!line.trim()) continue;
    const message = JSON.parse(line);
    if (message.type === "request") {
      void dispatch(message);
    } else if (message.type === "cancel") {
      controllers.get(message.id)?.abort();
    } else if (message.type === "host_result") {
      const pending = pendingHost.get(message.id);
      if (!pending) continue;
      pendingHost.delete(message.id);
      pending.resolve(message.result);
    } else if (message.type === "host_error") {
      const pending = pendingHost.get(message.id);
      if (!pending) continue;
      pendingHost.delete(message.id);
      pending.reject(new Error(message.error));
    }
  }
}
