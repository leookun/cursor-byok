import type {
  LlmMessage,
  LlmRequest,
  LlmTool,
  ProviderInvokeInput,
  ProviderOutput,
  ProviderResult,
  ProviderSupport,
} from "cursor-byok:provider";
import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import { HttpError, streamOpenAiChat } from "cursor-byok:protocol/openai-chat";
import {
  HttpError as ResponsesHttpError,
  streamOpenAiResponses,
} from "cursor-byok:protocol/openai-responses";
import { isFreeChatModel, isResponsesModel, opencodeModels } from "./models.ts";

// OpenCode Zen 免费层无需凭证。
const CHAT_URL = "https://opencode.ai/zen/v1/chat/completions";
const OPENCODE_UA = "opencode/1.18.31";
const OPENCODE_CLIENT = "desktop";
const SESSION_PREFIX = "ses_";
const REQUEST_PREFIX = "msg_";
const RESPONSES_URL = "https://opencode.ai/zen/v1/responses";

// 缺少客户端指纹会被免费层拒绝。
const FINGERPRINT_TOOLS = ["bash", "glob", "grep", "read"];
const FINGERPRINT_DESCRIPTION =
  "OpenCode built-in tool; currently unavailable and must not be used.";

// 5xx 仅在响应流开始前重试一次。
const RETRYABLE_5XX_DELAY_MS = 1000;
const RETRYABLE_5XX_MAX_ATTEMPTS = 2;

function isQuotaHttpError(error: { status: number; body: string }): boolean {
  if (error.status === 429) return true;
  const body = error.body.toLowerCase();
  return body.includes("freetier") ||
    body.includes("freeusage") ||
    body.includes("insufficient_quota") ||
    body.includes("quota_exceeded") ||
    body.includes("usage_limit");
}

function ensureFingerprintTools(tools: LlmTool[]): LlmTool[] {
  const existing = new Set(tools.map((tool) => tool.name));
  const missing = FINGERPRINT_TOOLS.filter((name) => !existing.has(name));
  if (missing.length === 0) return tools;
  return [
    ...tools,
    ...missing.map((name) => ({
      name,
      description: FINGERPRINT_DESCRIPTION,
      parameters: { type: "object", properties: {} },
    })),
  ];
}

function randomPart(): string {
  const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  let out = "";
  for (let i = 0; i < 26; i++) {
    out += charset[Math.floor(Math.random() * charset.length)];
  }
  return out;
}

async function sha256Bytes(value: string): Promise<Uint8Array> {
  const bytes = new TextEncoder().encode(value);
  return new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
}

async function digestHex(value: string): Promise<string> {
  const bytes = await sha256Bytes(value);
  return Array.from(bytes)
    .slice(0, 6)
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}

async function deterministicBase62(value: string, length: number): Promise<string> {
  const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  let out = "";
  let counter = 0;
  while (out.length < length) {
    const bytes = await sha256Bytes(`${value}:${counter}`);
    for (const byte of bytes) {
      if (out.length >= length) break;
      out += charset[byte % charset.length];
    }
    counter++;
  }
  return out;
}

/** 稳定派生会话 ID,避免重试创建新配额会话。 */
async function sessionId(seed: string | null): Promise<string> {
  if (seed === null) {
    return `${SESSION_PREFIX}${randomPart().slice(0, 12)}${randomPart().slice(0, 14)}`;
  }
  const digest = await digestHex(`opencode-session:${seed}`);
  const suffix = await deterministicBase62(`opencode-session-suffix:${seed}`, 14);
  return `${SESSION_PREFIX}${digest}${suffix}`;
}

/** 稳定派生请求 ID,保持同一请求的重试幂等。 */
async function requestId(
  session: string,
  seed: string,
): Promise<string> {
  const digest = await digestHex(`opencode-req\0${session}\0${seed}`);
  const suffix = await deterministicBase62(`opencode-req-suffix:${session}:${seed}`, 14);
  return `${REQUEST_PREFIX}${digest}${suffix}`;
}

function lastUserText(messages: readonly LlmMessage[]): string {
  for (let index = messages.length - 1; index >= 0; index--) {
    const message = messages[index];
    if (message?.role !== "user") continue;
    const text = message.content
      .map((part) => part.type === "text" ? part.text : "")
      .join(" ")
      .trim();
    if (text) return text.slice(-600);
  }
  return "";
}
function sleep(ms: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, ms);
  return promise;
}

async function headers(
  cacheKey: string | null,
  modelId: string,
  request: LlmRequest,
): Promise<Record<string, string>> {
  const userText = lastUserText(request.messages);
  const seed = cacheKey ?? (userText ? `${modelId}:${userText}` : null);
  const session = await sessionId(seed);
  return {
    authorization: "Bearer public",
    "user-agent": OPENCODE_UA,
    "x-opencode-client": OPENCODE_CLIENT,
    "x-opencode-session": session,
    "x-opencode-request": await requestId(session, userText || seed || "unseeded"),

    "x-opencode-project": "global",
  };
}

async function callOnce(
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<void> {
  // Muse 免费档只接受 Responses auto tool choice,max effort 会返回 400。
  if (isResponsesModel(input.model.id)) {
    const effort = input.request.reasoning.effort === "max"
      ? "xhigh"
      : input.request.reasoning.effort;
    await streamOpenAiResponses(
      {
        url: RESPONSES_URL,
        model: input.model.id,
        request: {
          ...input.request,
          reasoning: { ...input.request.reasoning, effort },
          tools: ensureFingerprintTools(input.request.tools),
        },
        extraBody: {
          tool_choice: "auto" as JsonValue,
        },
        headers: await headers(input.request.cacheKey, input.model.id, input.request),
      },
      output,
      context,
    );
    return;
  }
  await streamOpenAiChat(
    {
      url: CHAT_URL,
      model: input.model.id,
      request: {
        ...input.request,
        tools: ensureFingerprintTools(input.request.tools),
      },
      extraBody: {
        // 无调用方工具时禁止选择指纹占位工具。
        tool_choice: input.request.tools.length === 0 ? "none" : "auto",
      },
      headers: await headers(input.request.cacheKey, input.model.id, input.request),
    },
    output,
    context,
  );
}

async function invoke(
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<ProviderResult> {
  if (!isFreeChatModel(input.model.id) && !isResponsesModel(input.model.id)) {
    return { status: "request-error", message: `model ${input.model.id} is not a free model` };
  }
  let attempt = 0;
  while (true) {
    try {
      await callOnce(input, output, context);
      return { status: "completed" };
    } catch (error) {
      if (error instanceof HttpError || error instanceof ResponsesHttpError) {
        if (isQuotaHttpError(error)) {
          return { status: "request-error", message: error.message };
        }
        // 仅首字节前重试,避免流式响应重复产生下游调用。
        if (error.status >= 500 && attempt + 1 < RETRYABLE_5XX_MAX_ATTEMPTS) {
          attempt++;
          await sleep(RETRYABLE_5XX_DELAY_MS * attempt);
          continue;
        }
        return { status: "request-error", message: error.message };
      }
      const message = error instanceof Error ? error.message : String(error);
      return { status: "request-error", message };
    }
  }
}

export const opencodeProvider: ProviderSupport = {
  id: "opencode",
  displayName: {
    "en-US": "OpenCode Free",
    "zh-CN": "OpenCode Free",
  },
  description: {
    "en-US":
      "OpenCode Zen free tier through the official chat endpoint; no account or API key required.",
    "zh-CN": "通过官方 OpenCode Zen 免费层直连,无需账号或 API Key。",
  },
  providerType: "opencode",
  models: opencodeModels,
  invoke,
};
