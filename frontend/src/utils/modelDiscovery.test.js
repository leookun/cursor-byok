import assert from "node:assert/strict";
import test from "node:test";

import { buildDiscoveredModelAdditions, resolveCurrentDiscoveryTemplate } from "./modelDiscovery.js";

const template = {
  id: "existing-id",
  type: "openai",
  baseURL: "https://provider.example/v1",
  apiKey: "shared-key",
  customHeadersEnabled: true,
  customHeadersJSON: '{"X-Tenant":"a"}',
  openAIEndpoint: "/v1/responses",
  displayName: "Existing",
  tooltipData: "Existing",
  modelID: "existing-model",
};

test("模型发现只追加同渠道缺失模型并继承渠道配置", () => {
  const result = buildDiscoveredModelAdditions(
    [template, { ...template, apiKey: "other-key", modelID: "shared-model" }],
    template,
    [
      { id: "existing-model", displayName: "Existing" },
      { id: "shared-model", displayName: "Shared" },
      { id: "new-model", displayName: "New Model" },
    ],
  );

  assert.equal(result.discovered, 3);
  assert.equal(result.skipped, 1);
  assert.equal(result.additions.length, 2);
  assert.deepEqual(result.additions.map((item) => item.modelID), ["shared-model", "new-model"]);
  assert.equal(result.additions[0].apiKey, "shared-key");
  assert.equal(result.additions[0].customHeadersJSON, template.customHeadersJSON);
  assert.equal(result.additions[1].tooltipData, "New Model");
});

test("上游重复模型 ID 只追加一次", () => {
  const result = buildDiscoveredModelAdditions([], template, [
    { id: "new-model", displayName: "First" },
    { id: "new-model", displayName: "Second" },
  ]);

  assert.equal(result.discovered, 1);
  assert.equal(result.additions.length, 1);
  assert.equal(result.additions[0].displayName, "First");
});

test("保存前拒绝已删除或认证已变更的陈旧渠道", () => {
  assert.equal(resolveCurrentDiscoveryTemplate([], template), null);
  assert.equal(resolveCurrentDiscoveryTemplate([{ ...template, apiKey: "changed-key" }], template), null);

  const sibling = { ...template, id: "sibling-id", modelID: "other-model" };
  assert.equal(resolveCurrentDiscoveryTemplate([sibling], template), sibling);
});
