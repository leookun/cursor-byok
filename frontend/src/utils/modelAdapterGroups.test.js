import assert from "node:assert/strict";
import test from "node:test";

import {
  buildModelAdapterGroups,
  buildModelGroupBaseURL,
  modelAdaptersShareChannel,
} from "./modelAdapterGroups.js";

function adapter(overrides = {}) {
  return {
    type: "openai",
    baseURL: "https://provider.example/v1",
    apiKey: "shared-key",
    customHeadersEnabled: false,
    customHeadersJSON: "{}",
    modelID: "gpt-5",
    ...overrides,
  };
}

test("同一认证渠道的多个模型归入一个分组", () => {
  const groups = buildModelAdapterGroups([
    adapter(),
    adapter({ baseURL: "https://PROVIDER.example/v1/", modelID: "qwen3-max" }),
  ]);

  assert.equal(groups.length, 1);
  assert.equal(groups[0].adapters.length, 2);
  assert.equal(groups[0].key.includes("shared-key"), false);
});

test("同一地址的不同 API Key 不会误合并", () => {
  const groups = buildModelAdapterGroups([
    adapter({ apiKey: "account-a" }),
    adapter({ apiKey: "account-b" }),
  ]);

  assert.equal(groups.length, 2);
  assert.equal(modelAdaptersShareChannel(groups[0].adapters[0], groups[1].adapters[0]), false);
});

test("不同自定义认证 Header 不会误合并", () => {
  const groups = buildModelAdapterGroups([
    adapter({ customHeadersEnabled: true, customHeadersJSON: '{"X-Tenant":"a","X-Region":"cn"}' }),
    adapter({ customHeadersEnabled: true, customHeadersJSON: '{"X-Region":"cn","X-Tenant":"b"}' }),
  ]);

  assert.equal(groups.length, 2);
});

test("Header 字段顺序和名称大小写不同仍视为同一渠道", () => {
  const groups = buildModelAdapterGroups([
    adapter({ customHeadersEnabled: true, customHeadersJSON: '{"Authorization":"Bearer token","X-Region":"cn"}' }),
    adapter({ customHeadersEnabled: true, customHeadersJSON: '{"x-region":"cn","authorization":"Bearer token"}' }),
  ]);

  assert.equal(groups.length, 1);
});

test("后端 groupID 是前端分组与激活状态的权威标识", () => {
  const groups = buildModelAdapterGroups([
    adapter({ groupID: "grp_backend", apiKey: "account-a", modelID: "model-a" }),
    adapter({ groupID: "grp_backend", apiKey: "account-b", modelID: "model-b" }),
  ]);

  assert.equal(groups.length, 1);
  assert.equal(groups[0].key, "grp_backend");
  assert.equal(groups[0].groupID, "grp_backend");
  assert.equal(groups[0].adapters.length, 2);
});

test("显式空分组在没有模型时仍然显示", () => {
  const groups = buildModelAdapterGroups([{
    id: "grp_empty",
    name: "生产环境",
    type: "openai",
    baseURL: "https://provider.example/v1",
    apiKey: "secret",
  }], []);

  assert.equal(groups.length, 1);
  assert.equal(groups[0].name, "生产环境");
  assert.equal(groups[0].adapters.length, 0);
});

test("请求地址与端口组合为标准 baseURL", () => {
  assert.deepEqual(
    buildModelGroupBaseURL("api.example.com/v1", "8443"),
    { baseURL: "https://api.example.com:8443/v1", error: "" },
  );
  assert.equal(buildModelGroupBaseURL("https://api.example.com/v1", "70000").error.length > 0, true);
});
