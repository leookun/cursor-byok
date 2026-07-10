import assert from "node:assert/strict";
import test from "node:test";

import { redactForLog } from "./redactForLog.js";

test("递归脱敏模型配置与序列化编辑器参数", () => {
  const redacted = redactForLog({
    modelAdapters: [{ apiKey: "sk-secret", modelID: "gpt-5", customHeadersJSON: '{"Authorization":"Bearer secret"}' }],
    adapterJSON: '{"apiKey":"sk-secret"}',
  });

  assert.deepEqual(redacted, {
    modelAdapters: [{ apiKey: "[REDACTED]", modelID: "gpt-5", customHeadersJSON: "[REDACTED]" }],
    adapterJSON: "[REDACTED]",
  });
});

test("错误日志仅保留名称与消息", () => {
  const error = new Error("request failed");
  error.apiKey = "sk-secret";

  assert.deepEqual(redactForLog(error), { name: "Error", message: "request failed" });
});
