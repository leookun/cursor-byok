import type {
  LlmMessage,
  ProviderInvokeInput,
  ProviderOutput,
  ProviderResult,
  ProviderSupport,
} from "cursor-byok:provider";
import type { JsonValue, PluginContext } from "cursor-byok:plugin";
import { HttpError } from "cursor-byok:protocol/openai-chat";
import { resolveModelRoute } from "./model_routes.ts";
import {
  ANTIGRAVITY_CLIENT_HEADERS,
  ANTIGRAVITY_ENDPOINTS,
  ANTIGRAVITY_USER_AGENT,
  antigravityModels,
} from "./models.ts";
import {
  type AccountData,
  accountData,
  isTokenExpired,
  refreshAccount,
  RESOURCE_TYPE,
} from "./resources.ts";

function isQuotaHttpError(error: HttpError): boolean {
  if (error.status === 429) return true;
  const body = error.body.toLowerCase();
  return body.includes("resource_exhausted") ||
    body.includes("quota_exceeded") ||
    body.includes("quota_exhausted") ||
    body.includes("rate_limit_exceeded") ||
    body.includes("rate limit") ||
    body.includes("model_capacity_exhausted") ||
    body.includes("user rate limit exceeded") ||
    body.includes("too many requests");
}

function invalidResult(message: string, stateMessage: string): ProviderResult {
  return {
    status: "resource-error",
    message,
    patch: { state: { status: "invalid", message: stateMessage } },
  };
}

async function readBody(lines: AsyncIterable<string>): Promise<string> {
  const collected: string[] = [];
  for await (const line of lines) collected.push(line);
  return collected.join("\n");
}

function randomHex(length = 8): string {
  const array = new Uint8Array(Math.ceil(length / 2));
  crypto.getRandomValues(array);
  return Array.from(array, (byte) => byte.toString(16).padStart(2, "0")).join("").slice(0, length);
}

function generateRequestId(): string {
  return `agent/${Date.now()}/${randomHex(8)}`;
}

// Keys the CloudCode v1internal Schema proto rejects with "Cannot find field"
const UNSUPPORTED_SCHEMA_KEYS: Record<string, true> = {
  "$schema": true,
  "$ref": true,
  "$defs": true,
  "$comment": true,
  "examples": true,
  "unevaluatedProperties": true,
  "unevaluatedItems": true,
  "patternProperties": true,
  "propertyNames": true,
  "exclusiveMinimum": true,
  "exclusiveMaximum": true,
  "multipleOf": true,
  "dependencies": true,
  "dependentSchemas": true,
  "dependentRequired": true,
  "deprecated": true,
  "readOnly": true,
  "writeOnly": true,
  "x-mcp-header": true,
  "const": true,
  "default": true,
  "additionalProperties": true,
  "title": true,
  "format": true,
};

const PROTO_TYPE_MAP: Record<string, string> = {
  string: "STRING",
  number: "NUMBER",
  integer: "INTEGER",
  boolean: "BOOLEAN",
  array: "ARRAY",
  object: "OBJECT",
};

function enforceUppercaseTypes(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(enforceUppercaseTypes);
  if (value === null || typeof value !== "object") return value;
  const out: Record<string, unknown> = {};
  for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
    if (UNSUPPORTED_SCHEMA_KEYS[key]) continue;
    if (key === "type" && typeof child === "string") {
      out[key] = PROTO_TYPE_MAP[child.toLowerCase()] ?? child.toUpperCase();
    } else {
      out[key] = enforceUppercaseTypes(child);
    }
  }
  if (!out.type && out.properties) {
    out.type = "OBJECT";
  }
  return out;
}

function sanitizeSchema(value: unknown): unknown {
  if (!value || typeof value !== "object") {
    return { type: "OBJECT", properties: {} };
  }
  const clean = enforceUppercaseTypes(value) as Record<string, unknown>;
  if (!clean.type) clean.type = "OBJECT";
  return clean;
}

function convertToCloudCodeContents(
  instructions: string,
  messages: LlmMessage[],
): {
  contents: Array<{ role: string; parts: Array<Record<string, unknown>> }>;
  systemInstruction?: { role: string; parts: Array<{ text: string }> };
} {
  const rawContents: Array<{ role: string; parts: Array<Record<string, unknown>> }> = [];
  let systemText = instructions || "";

  for (const msg of messages) {
    if (msg.role === "system") {
      const txt = msg.content
        .map((p) => (p.type === "text" ? p.text : ""))
        .filter(Boolean)
        .join("\n");
      if (txt) {
        systemText += (systemText ? "\n\n" : "") + txt;
      }
      continue;
    }

    if (msg.role === "assistant") {
      const parts: Array<Record<string, unknown>> = [];
      if (msg.text) {
        parts.push({ text: msg.text });
      }

      const replayVal = msg.replayState?.providerKind === "antigravity"
        ? (msg.replayState.value as Record<string, unknown> | null)
        : null;
      const sig = typeof replayVal?.thoughtSignature === "string"
        ? replayVal.thoughtSignature
        : null;

      for (const call of msg.toolCalls) {
        parts.push({
          functionCall: {
            name: call.name,
            args: typeof call.arguments === "object" && call.arguments !== null
              ? call.arguments
              : {},
          },
          thoughtSignature: sig || "skip_thought_signature_validator",
        });
      }

      if (parts.length > 0) {
        rawContents.push({ role: "model", parts });
      }
    } else if (msg.role === "tool") {
      rawContents.push({
        role: "user",
        parts: [
          {
            functionResponse: {
              name: msg.name || "function",
              response: { result: msg.content },
            },
          },
        ],
      });
    } else if (msg.role === "user") {
      const parts: Array<Record<string, unknown>> = [];
      for (const p of msg.content) {
        if (p.type === "text") {
          if (p.text) parts.push({ text: p.text });
        } else if (p.type === "image") {
          parts.push({
            inlineData: {
              mimeType: p.mediaType,
              data: p.dataBase64,
            },
          });
        }
      }
      if (parts.length === 0) {
        parts.push({ text: " " });
      }
      rawContents.push({ role: "user", parts });
    }
  }

  // Merge consecutive same-role messages so contents strictly alternate user -> model -> user -> model
  const contents: Array<{ role: string; parts: Array<Record<string, unknown>> }> = [];
  for (const item of rawContents) {
    if (item.parts.length === 0) continue;
    const last = contents[contents.length - 1];
    if (last && last.role === item.role) {
      last.parts.push(...item.parts);
    } else {
      contents.push(item);
    }
  }

  if (contents.length > 0 && contents[0].role !== "user") {
    contents.unshift({ role: "user", parts: [{ text: " " }] });
  }

  return {
    contents,
    ...(systemText.trim()
      ? { systemInstruction: { role: "system", parts: [{ text: systemText.trim() }] } }
      : {}),
  };
}

async function streamCloudCode(
  accessToken: string,
  projectId: string,
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<void> {
  if (context.signal.aborted) throw context.signal.reason;
  const route = resolveModelRoute(input.model, input.request.reasoning.effort);
  const actualModel = route.id;
  const maxOutputTokens = Math.min(
    input.request.maxOutputTokens ?? route.maxOutputTokens,
    route.maxOutputTokens,
  );
  const thinkingBudget = Math.min(route.thinkingBudget, Math.max(0, maxOutputTokens - 1));
  const { contents, systemInstruction } = convertToCloudCodeContents(
    input.request.instructions,
    input.request.messages,
  );

  const tools = input.request.tools && input.request.tools.length > 0
    ? [
      {
        functionDeclarations: input.request.tools.map((t) => ({
          name: t.name,
          description: t.description || "",
          parameters: sanitizeSchema(t.parameters),
        })),
      },
    ]
    : undefined;

  const toolConfig = tools
    ? {
      functionCallingConfig: { mode: "AUTO" },
    }
    : undefined;

  if (!projectId) {
    throw new Error("Antigravity account is missing its project ID");
  }

  const payload = {
    project: projectId,
    model: actualModel,
    userAgent: "antigravity",
    requestType: "agent",
    requestId: generateRequestId(),
    enabledCreditTypes: ["GOOGLE_ONE_AI"],
    request: {
      contents,
      ...(systemInstruction ? { systemInstruction } : {}),
      ...(tools ? { tools } : {}),
      ...(toolConfig ? { toolConfig } : {}),
      generationConfig: {
        maxOutputTokens,
        ...(thinkingBudget > 0
          ? { thinkingConfig: { thinkingBudget, includeThoughts: true } }
          : {}),
      },
    },
  };

  const headers: Record<string, string> = {
    authorization: `Bearer ${accessToken}`,
    "content-type": "application/json",
    accept: "text/event-stream",
    "user-agent": ANTIGRAVITY_USER_AGENT,
    ...ANTIGRAVITY_CLIENT_HEADERS,
  };
  if (actualModel.toLowerCase().includes("claude")) {
    headers["anthropic-beta"] =
      "claude-code-20250219,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14";
  }

  let lastError: Error | null = null;
  let hasEmittedAnyChunk = false;

  for (const endpoint of ANTIGRAVITY_ENDPOINTS) {
    context.signal.throwIfAborted();
    if (hasEmittedAnyChunk) break;

    try {
      const response = await context.network.stream(
        `${endpoint}/v1internal:streamGenerateContent?alt=sse`,
        {
          method: "POST",
          headers,
          body: JSON.stringify(payload),
        },
      );

      if (response.status < 200 || response.status >= 300) {
        const errorBody = await readBody(response.lines);
        lastError = new HttpError(response.status, errorBody);
        if (
          response.status === 503 || response.status === 502 || response.status === 504 ||
          response.status === 404
        ) {
          continue;
        }
        throw lastError;
      }

      let textStarted = false;
      let thinkingStarted = false;
      let doneEmitted = false;
      let hasTools = false;
      let toolIndex = 0;
      let lastThoughtSignature: string | null = null;
      let finalUsage: {
        inputTokens: number | null;
        outputTokens: number | null;
        totalTokens: number | null;
        cacheReadTokens: number | null;
      } | null = null;

      for await (const line of response.lines) {
        if (!line.startsWith("data:")) continue;
        const raw = line.slice(5).trim();
        if (!raw || raw === "[DONE]") break;

        let json: Record<string, unknown>;
        try {
          json = JSON.parse(raw) as Record<string, unknown>;
        } catch {
          continue;
        }

        const resp = (json.response as Record<string, unknown> | undefined) ?? json;
        if (!resp) continue;
        const streamError = (resp.error ?? json.error) as { code?: number } | undefined;
        if (streamError) throw new HttpError(streamError.code ?? 502, JSON.stringify(streamError));

        const usage = resp.usageMetadata as Record<string, number> | undefined;
        if (usage) {
          finalUsage = {
            inputTokens: typeof usage.promptTokenCount === "number" ? usage.promptTokenCount : null,
            outputTokens: typeof usage.candidatesTokenCount === "number"
              ? usage.candidatesTokenCount
              : null,
            totalTokens: typeof usage.totalTokenCount === "number" ? usage.totalTokenCount : null,
            cacheReadTokens: typeof usage.cachedContentTokenCount === "number"
              ? usage.cachedContentTokenCount
              : null,
          };
        }

        const candidates = resp.candidates as Array<Record<string, unknown>> | undefined;
        const candidate = candidates?.[0];
        const content = candidate?.content as Record<string, unknown> | undefined;
        const parts = content?.parts as Array<Record<string, unknown>> | undefined;

        if (parts) {
          for (const part of parts) {
            const sig = typeof part.thoughtSignature === "string" ? part.thoughtSignature : null;
            if (sig) {
              lastThoughtSignature = sig;
            }

            const isThought = part.thought === true;
            const textPart = typeof part.text === "string" ? part.text : null;

            if (isThought && textPart) {
              const cleanThought = textPart.replace(/<\/?think>/gi, "");
              if (cleanThought) {
                hasEmittedAnyChunk = true;
                if (!thinkingStarted) {
                  thinkingStarted = true;
                  output.emit({ type: "thinking-start" });
                }
                output.emit({ type: "thinking-delta", text: cleanThought });
              }
            } else if (textPart) {
              if (thinkingStarted) {
                thinkingStarted = false;
                output.emit({ type: "thinking-end" });
              }
              const cleanText = textPart.replace(/<\/?think>/gi, "");
              if (cleanText) {
                hasEmittedAnyChunk = true;
                if (!textStarted) {
                  textStarted = true;
                  output.emit({ type: "text-start" });
                }
                output.emit({ type: "text-delta", text: cleanText });
              }
            }

            const fnCall = part.functionCall as { name: string; args: unknown } | undefined;
            if (fnCall) {
              hasEmittedAnyChunk = true;
              if (thinkingStarted) {
                thinkingStarted = false;
                output.emit({ type: "thinking-end" });
              }
              if (textStarted) {
                textStarted = false;
                output.emit({ type: "text-end" });
              }
              hasTools = true;
              const currentIdx = toolIndex++;
              const callId = `call_${Date.now()}_${currentIdx}`;
              output.emit({
                type: "tool-call-start",
                index: currentIdx,
                callId,
                name: fnCall.name,
              });
              const argsStr = typeof fnCall.args === "string"
                ? fnCall.args
                : JSON.stringify(fnCall.args || {});
              output.emit({
                type: "tool-call-arguments-delta",
                index: currentIdx,
                delta: argsStr,
              });
              output.emit({
                type: "tool-call-end",
                index: currentIdx,
              });
            }
          }
        }

        const finishReason = typeof candidate?.finishReason === "string"
          ? candidate.finishReason
          : null;
        if (finishReason) {
          if (thinkingStarted) {
            thinkingStarted = false;
            output.emit({ type: "thinking-end" });
          }
          if (textStarted) {
            textStarted = false;
            output.emit({ type: "text-end" });
          }
          if (lastThoughtSignature) {
            output.emit({
              type: "replay-state",
              providerKind: "antigravity",
              value: { thoughtSignature: lastThoughtSignature },
            });
            lastThoughtSignature = null;
          }
          if (finalUsage) {
            output.emit({
              type: "usage",
              usage: {
                inputTokens: finalUsage.inputTokens,
                outputTokens: finalUsage.outputTokens,
                totalTokens: finalUsage.totalTokens,
                cacheReadTokens: finalUsage.cacheReadTokens,
                cacheWriteTokens: null,
                reasoningTokens: null,
              },
            });
            finalUsage = null;
          }
          const isTool = finishReason === "STOP" &&
            (hasTools || parts?.some((p) => p.functionCall));
          output.emit({
            type: "done",
            reason: isTool ? "tool-use" : "stop",
          });
          doneEmitted = true;
          break;
        }
      }

      if (thinkingStarted) {
        output.emit({ type: "thinking-end" });
      }
      if (textStarted) {
        output.emit({ type: "text-end" });
      }
      if (lastThoughtSignature) {
        output.emit({
          type: "replay-state",
          providerKind: "antigravity",
          value: { thoughtSignature: lastThoughtSignature },
        });
      }
      if (finalUsage) {
        output.emit({
          type: "usage",
          usage: {
            inputTokens: finalUsage.inputTokens,
            outputTokens: finalUsage.outputTokens,
            totalTokens: finalUsage.totalTokens,
            cacheReadTokens: finalUsage.cacheReadTokens,
            cacheWriteTokens: null,
            reasoningTokens: null,
          },
        });
      }
      if (!doneEmitted) {
        output.emit({
          type: "done",
          reason: hasTools ? "tool-use" : "stop",
        });
      }
      return;
    } catch (err) {
      lastError = err instanceof Error ? err : new Error(String(err));
      if (
        hasEmittedAnyChunk || context.signal.aborted ||
        (lastError instanceof HttpError && lastError.status < 500 &&
          lastError.status !== 429 && lastError.status !== 404)
      ) {
        throw lastError;
      }
    }
  }

  if (lastError) throw lastError;
}

async function invoke(
  input: ProviderInvokeInput,
  output: ProviderOutput,
  context: PluginContext,
): Promise<ProviderResult> {
  if (!input.resource) {
    return {
      status: "request-error",
      message: "Add a Google Antigravity account or API key before calling Antigravity",
    };
  }
  let data: AccountData;
  try {
    data = accountData(input.resource);
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    return invalidResult(message, message);
  }

  let patchData: AccountData | null = null;
  let emitted = false;
  const guardedOutput: ProviderOutput = {
    emit(event) {
      emitted = true;
      output.emit(event);
    },
  };

  // Auto-refresh token if expired or close to expiration (skew 5 mins)
  if (data.refreshToken && isTokenExpired(data)) {
    try {
      const refreshed = await refreshAccount(input.resource, context);
      if (refreshed.privateData) {
        data = refreshed.privateData as unknown as AccountData;
        patchData = data;
      }
    } catch {
      // Continue with existing token
    }
  }

  const projectId = data.projectId;
  if (!projectId) {
    return {
      status: "request-error",
      message: "Antigravity account is missing its project ID",
    };
  }

  try {
    await streamCloudCode(
      data.accessToken,
      projectId,
      input,
      guardedOutput,
      context,
    );
    return patchData
      ? {
        status: "completed",
        patch: { privateData: patchData as unknown as JsonValue, state: { status: "ready" } },
      }
      : { status: "completed" };
  } catch (error) {
    if (error instanceof HttpError) {
      if (
        !emitted && (error.status === 401 || error.status === 403) && data.refreshToken &&
        !isQuotaHttpError(error)
      ) {
        try {
          const refreshed = await refreshAccount(input.resource, context);
          if (refreshed.privateData) {
            const freshData = refreshed.privateData as unknown as AccountData;
            const freshProj = freshData.projectId ?? projectId;
            await streamCloudCode(
              freshData.accessToken,
              freshProj,
              input,
              guardedOutput,
              context,
            );
            return {
              status: "completed",
              patch: { privateData: freshData as unknown as JsonValue, state: { status: "ready" } },
            };
          }
        } catch {
          // Failed refresh
        }
      }
      // A model/endpoint capacity error does not invalidate the entire account.
      return { status: "request-error", message: error.message };
    }
    const message = error instanceof Error ? error.message : String(error);
    return { status: "request-error", message };
  }
}

export const antigravityProvider: ProviderSupport = {
  id: "antigravity",
  displayName: {
    "en-US": "Google Antigravity",
    "zh-CN": "Google Antigravity",
  },
  description: {
    "en-US": "Google Antigravity / Gemini model access with hybrid reasoning & agent tools.",
    "zh-CN": "通过 Google Antigravity / Gemini API 使用混合推理与 Agent 工具。",
  },
  providerType: "google",
  resourceType: RESOURCE_TYPE,
  models: antigravityModels,
  invoke,
};
