import assert from "node:assert/strict";
import test from "node:test";

import {
  buildModelGroupCopy,
  buildUniqueModelGroupName,
  isCopiedModelGroupID,
} from "./modelGroupCopy.js";

const sourceGroup = {
  id: "grp_original",
  name: "生产渠道",
  type: "openai",
  baseURL: "https://provider.example/v1",
  apiKey: "fixture-key",
  openAIEndpoint: "/v1/responses",
  customHeadersEnabled: true,
  customHeadersJSON: '{"X-Tenant":"fixture-tenant"}',
};

const sourceAdapters = [
  {
    id: "adapter-original-a",
    groupID: "grp_original",
    displayName: "GPT 5",
    type: "openai",
    baseURL: sourceGroup.baseURL,
    apiKey: sourceGroup.apiKey,
    modelID: "gpt-5",
    reasoningEffort: "high",
    openAIEndpoint: sourceGroup.openAIEndpoint,
    customHeadersEnabled: sourceGroup.customHeadersEnabled,
    customHeadersJSON: sourceGroup.customHeadersJSON,
    contextWindowTokens: 200000,
  },
  {
    id: "adapter-original-b",
    groupID: "grp_original",
    displayName: "Qwen Max",
    type: "openai",
    baseURL: sourceGroup.baseURL,
    apiKey: sourceGroup.apiKey,
    modelID: "qwen-max",
    reasoningEffort: "medium",
    openAIEndpoint: sourceGroup.openAIEndpoint,
    customHeadersEnabled: sourceGroup.customHeadersEnabled,
    customHeadersJSON: sourceGroup.customHeadersJSON,
    maxCompletionTokens: 8192,
  },
];

test("复制分组会生成独立 ID 并完整复制模型配置", () => {
  const result = buildModelGroupCopy(
    sourceGroup,
    sourceAdapters,
    [sourceGroup],
    sourceAdapters,
    { idFactory: () => "grp_copy_test" },
  );

  assert.equal(result.group.id, "grp_copy_test");
  assert.equal(result.group.name, "生产渠道 - 副本");
  assert.equal(result.adapters.length, sourceAdapters.length);
  assert.deepEqual(result.adapters.map((item) => item.groupID), ["grp_copy_test", "grp_copy_test"]);
  assert.deepEqual(result.adapters.map((item) => item.modelID), ["gpt-5", "qwen-max"]);
  assert.deepEqual(result.adapters.map((item) => item.reasoningEffort), ["high", "medium"]);
  assert.equal(result.adapters.every((item) => item.id === ""), true);
  assert.equal(result.adapters.every((item) => item.displayName.endsWith(" - 副本")), true);
  assert.deepEqual(sourceGroup, {
    id: "grp_original",
    name: "生产渠道",
    type: "openai",
    baseURL: "https://provider.example/v1",
    apiKey: "fixture-key",
    openAIEndpoint: "/v1/responses",
    customHeadersEnabled: true,
    customHeadersJSON: '{"X-Tenant":"fixture-tenant"}',
  });
  assert.equal(isCopiedModelGroupID(result.group.id), true);
});

test("副本名称和模型显示名会按已有名称递增", () => {
  const groups = [
    { name: "生产渠道" },
    { name: "生产渠道 - 副本" },
  ];
  const adapters = [
    ...sourceAdapters,
    { displayName: "GPT 5 - 副本" },
  ];

  assert.equal(buildUniqueModelGroupName(groups, "生产渠道"), "生产渠道 - 副本 (2)");
  const result = buildModelGroupCopy(
    sourceGroup,
    sourceAdapters,
    groups,
    adapters,
    { idFactory: () => "grp_copy_test_2" },
  );
  assert.deepEqual(result.adapters.map((item) => item.displayName), ["GPT 5 - 副本 (2)", "Qwen Max - 副本"]);
});

test("源分组内重复显示名也会生成唯一副本名称", () => {
  const result = buildModelGroupCopy(
    sourceGroup,
    [sourceAdapters[0], { ...sourceAdapters[0], modelID: "gpt-5-mini" }],
    [sourceGroup],
    sourceAdapters,
    { idFactory: () => "grp_copy_test_3" },
  );

  assert.deepEqual(result.adapters.map((item) => item.displayName), ["GPT 5 - 副本", "GPT 5 - 副本 (2)"]);
});
