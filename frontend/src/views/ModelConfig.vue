<script setup>
import AddProviderModal from "@/components/AddProviderModal.vue";
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import ModelAdapterTestCard from "@/components/ModelAdapterTestCard.vue";
import ProviderCard from "@/components/ProviderCard.vue";
import { showModal } from "@/composables/useModal";
import { fetchModelsFromProvider } from "@/services/clientApi";
import {
  appState,
  createEmptyModelAdapter,
  deleteModelAdapterAt,
  duplicateModelAdapterAt,
  getModelAdapterTestResultByID,
  normalizeModelAdapter,
  persistUserConfig,
  reloadUserConfig,
  startModelAdapterTest,
} from "@/state/appState";
import { groupAdaptersByProvider, providerKey } from "@/utils/providerGroup";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { useRouter } from "vue-router";

const router = useRouter();
const BATCH_TEST_CONCURRENCY = 10;

// ─── View state ──────────────────────────────────────────────────────────────
// 'providers' = 供应商列表视图 | 'detail' = 供应商下模型列表视图
const view = ref("providers");
const selectedProviderKey = ref(""); // providerKey = baseURL\ntype

// ─── Provider groups ──────────────────────────────────────────────────────────
const providers = computed(() => groupAdaptersByProvider(appState.modelAdapters, appState.providers));

const currentProvider = computed(() =>
  providers.value.find((p) => p.key === selectedProviderKey.value) ?? null,
);

// 当前供应商下的模型（在原始数组中的引用，保留 index）
const currentModels = computed(() => {
  if (!currentProvider.value) return [];
  const key = currentProvider.value.key;
  return appState.modelAdapters
    .map((adapter, index) => ({ adapter, index }))
    .filter(({ adapter }) => providerKey(adapter) === key);
});

// ─── AddProviderModal ─────────────────────────────────────────────────────────
const showAddProvider = ref(false);

async function handleAddProviderSave(newAdapters) {
  showAddProvider.value = false;
  if (!newAdapters?.length) return;

  // Extract provider-level keys from adapters (set by AddProviderModal).
  const providerKeys = newAdapters[0]?._providerKeys;
  const providerCustomName = newAdapters[0]?._providerName || "";
  const providerBaseURL = newAdapters[0]?.baseURL || "";
  const providerType = newAdapters[0]?.type || "openai";
  const providerHost = newAdapters[0]?.displayName?.split(" - ")[0] || providerBaseURL;

  // Add adapters (strip _providerKeys, _providerName).
  for (const adapter of newAdapters) {
    const { _providerKeys, _providerName, ...clean } = adapter;
    appState.modelAdapters.push(normalizeModelAdapter(clean));
  }

  // Persist provider-level keys to appState.providers.
  if (providerKeys?.length) {
    const providers = Array.isArray(appState.providers) ? [...appState.providers] : [];
    const existingIdx = providers.findIndex(
      (p) =>
        String(p?.baseURL || "").trim() === providerBaseURL &&
        String(p?.type || "").trim().toLowerCase() === providerType,
    );
    const providerEntry = {
      id: "",
      name: providerCustomName || providerHost,
      type: providerType,
      baseURL: providerBaseURL,
      apiKey: providerKeys[0] || "",
      apiKeys: providerKeys,
      models: [],
    };
    if (existingIdx >= 0) {
      // Merge keys.
      const existing = providers[existingIdx];
      const mergedKeys = [...new Set([...(existing.apiKeys || []), ...providerKeys])];
      providers[existingIdx] = { ...existing, apiKeys: mergedKeys, apiKey: mergedKeys[0] || "" };
    } else {
      providers.push(providerEntry);
    }
    appState.providers = providers;
  }

  const result = await persistUserConfig();
  if (!result?.ok) {
    await showActionError("保存失败", result?.error);
  }
}

// ─── Fetch models for current provider (detail view) ─────────────────────────
const fetchingModels = ref(false);

async function handleFetchModelsForCurrent() {
  const provider = currentProvider.value;
  if (!provider || fetchingModels.value) return;
  const key = provider.keys?.[0] || "";
  if (!key || !provider.baseURL) {
    await showActionError("无法获取模型", "该供应商缺少 API Key 或 baseURL");
    return;
  }
  fetchingModels.value = true;
  let fetchedModels = [];
  let fetchError = "";
  try {
    const res = await fetchModelsFromProvider(provider.baseURL, key, provider.type);
    if (res.error) {
      fetchError = res.error;
    } else {
      fetchedModels = res.models || [];
    }
  } catch (e) {
    fetchError = String(e?.message || e || "获取失败");
  } finally {
    fetchingModels.value = false;
  }
  if (fetchError) {
    await showActionError("获取模型失败", fetchError);
    return;
  }
  if (!fetchedModels.length) {
    await showActionError("获取模型", "Provider 未返回任何模型");
    return;
  }
  // Filter out models already in the provider.
  const existing = new Set(
    currentModels.value.map(({ adapter }) => adapter.modelID),
  );
  const toAdd = fetchedModels.filter((m) => !existing.has(m));
  if (!toAdd.length) {
    await showActionError("获取模型", "所有模型均已存在，无需添加");
    return;
  }
  const confirmed = await showModal({
    title: `获取到 ${fetchedModels.length} 个模型（${toAdd.length} 个新）`,
    content: `将添加以下模型到 ${provider.host}：\n${toAdd.slice(0, 8).join(", ")}${toAdd.length > 8 ? "…" : ""}`,
    confirmText: `添加 ${toAdd.length} 个模型`,
    cancelText: "取消",
  });
  if (!confirmed) return;

  for (const modelID of toAdd) {
    appState.modelAdapters.push(
      normalizeModelAdapter({
        ...createEmptyModelAdapter(),
        displayName: modelID,
        modelID,
        baseURL: provider.baseURL,
        apiKey: key,
        type: provider.type,
      }),
    );
  }
  const result = await persistUserConfig();
  if (!result?.ok) {
    await showActionError("添加失败", result?.error);
  }
}

// ─── Navigation ───────────────────────────────────────────────────────────────
function enterProvider(provider) {
  selectedProviderKey.value = provider.key;
  view.value = "detail";
}

function backToProviders() {
  view.value = "providers";
  selectedProviderKey.value = "";
}

// ─── Batch delete provider ────────────────────────────────────────────────────
async function handleDeleteProvider(provider) {
  const confirmed = await showModal({
    title: `删除供应商 ${provider.host}？`,
    content: `将删除该供应商下的 ${provider.models.length} 个模型配置，此操作不可撤销。`,
    confirmText: "删除",
    cancelText: "取消",
  });
  if (!confirmed) return;

  // ⚠️ 必须同时删除 appState.providers 中对应的 provider entry。
  // 后端 NormalizeConfig 第 281 行用 FlattenProvidersToAdapters(providers) 重新派生
  // modelAdapters —— providers 是落盘源，modelAdapters 只是 runtime 派生数据。
  // 只删 modelAdapters 不删 providers，后端 normalize 会把 adapters 全部重建回来，
  // 表现为"删除按钮没生效"。
  const providerBaseURL = String(provider.baseURL || "").trim();
  const providerType = String(provider.type || "").trim().toLowerCase();
  appState.providers = (appState.providers || []).filter(
    (p) =>
      !(
        String(p?.baseURL || "").trim() === providerBaseURL &&
        String(p?.type || "").trim().toLowerCase() === providerType
      ),
  );

  const toDelete = appState.modelAdapters
    .map((a, i) => ({ a, i }))
    .filter(({ a }) => providerKey(a) === provider.key)
    .map(({ i }) => i)
    .sort((a, b) => b - a);

  for (const idx of toDelete) {
    appState.modelAdapters.splice(idx, 1);
  }
  const result = await persistUserConfig();
  if (!result?.ok) {
    await showActionError("删除失败", result?.error);
    await reloadUserConfig({ modelAdaptersOnly: true });
  }
}

// ─── Single adapter operations ────────────────────────────────────────────────
async function showActionError(title, error) {
  await showModal({ title, content: String(error || "服务错误").trim() || "服务错误" });
}

function openEditor(index = -1) {
  const adapter = index >= 0
    ? appState.modelAdapters[index]
    : { ...createEmptyModelAdapter(), baseURL: currentProvider.value?.baseURL ?? "", apiKey: currentProvider.value?.keys?.[0] ?? currentProvider.value?.apiKey ?? "", type: currentProvider.value?.type ?? "openai" };
  router.push({ path: "/model-editor", query: { index }, state: { adapterJSON: JSON.stringify(adapter) } });
}

async function handleDeleteAdapter(index) {
  const result = await deleteModelAdapterAt(index);
  if (!result.ok) await showActionError("删除失败", result.error);
  // if provider is now empty, go back
  if (!currentModels.value.length) backToProviders();
}

async function handleDuplicateAdapter(index) {
  const result = await duplicateModelAdapterAt(index);
  if (!result.ok) await showActionError("复制失败", result.error);
}

function getAdapterTestResult(adapter) {
  return getModelAdapterTestResultByID(adapter?.id);
}
function isAdapterTesting(adapter) {
  return getAdapterTestResult(adapter)?.status === "running";
}
async function handleTestAdapter(adapter) {
  try { await startModelAdapterTest(adapter); } catch { /* shown in card */ }
}

// ─── Batch test (per provider in detail view) ────────────────────────────────
const batchTesting = ref(false);
const batchStopping = ref(false);
const batchTotal = ref(0);
const batchCompleted = ref(0);
let batchStopRequested = false;
let batchAbortController = null;

const batchButtonText = computed(() => {
  if (batchStopping.value) return "停止中...";
  if (!batchTesting.value) return `测试全部 (${currentModels.value.length})`;
  return `停止测试 ${batchCompleted.value}/${batchTotal.value}`;
});

async function stopBatchTesting() {
  if (!batchTesting.value || batchStopping.value) return;
  batchStopRequested = true;
  batchStopping.value = true;
  batchAbortController?.abort();
  batchAbortController = null;
}

async function handleTestAllCurrentAdapters() {
  if (batchTesting.value) { await stopBatchTesting(); return; }
  const adapters = currentModels.value.map(({ adapter }) => adapter);
  if (!adapters.length) return;
  batchStopRequested = false;
  batchAbortController = new AbortController();
  batchTesting.value = true;
  batchStopping.value = false;
  batchTotal.value = adapters.length;
  batchCompleted.value = 0;
  let nextIndex = 0;
  try {
    const workers = Array.from({ length: Math.min(BATCH_TEST_CONCURRENCY, adapters.length) }, async () => {
      while (!batchStopRequested && !batchAbortController?.signal.aborted) {
        const ci = nextIndex++;
        if (ci >= adapters.length) return;
        try { await startModelAdapterTest(adapters[ci]); } catch { /* continue */ }
        batchCompleted.value++;
      }
    });
    await Promise.allSettled(workers);
  } finally {
    batchAbortController = null;
    batchStopRequested = false;
    batchTesting.value = false;
    batchStopping.value = false;
  }
}

onMounted(async () => { await reloadUserConfig({ modelAdaptersOnly: true }).catch(() => {}); });

onBeforeUnmount(() => {
  if (batchTesting.value && !batchStopping.value) {
    batchStopRequested = true;
    batchAbortController?.abort();
  }
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col p-4 pt-0 text-[#e5e5e5] overflow-hidden">

    <!-- ── Header bar ─────────────────────────────────────────── -->
    <div class="shrink-0 pb-4">
      <div class="flex items-center justify-between gap-4">
        <!-- Left: breadcrumb (detail view only) -->
        <div class="center-row gap-2">
          <template v-if="view === 'detail' && currentProvider">
            <button
              type="button"
              class="center-row gap-1.5 rounded-[8px] border border-[#343434] bg-[#252525] px-3 py-2 text-sm text-[#a3a3a3] transition hover:border-[#4a4a4a] hover:text-white"
              @click="backToProviders"
            >
              <span class="icon-[mdi--arrow-left] text-[15px]" />
              <span>供应商</span>
            </button>
            <span class="text-[#555]">/</span>
            <span class="text-sm font-medium text-white">{{ currentProvider.host }}</span>
            <span class="rounded-[999px] bg-[#10AD5D]/15 px-2 py-0.5 text-xs text-[#10AD5D]">
              {{ currentModels.length }} 个模型
            </span>
          </template>
        </div>

        <!-- Right: actions -->
        <div class="center-row gap-2">
          <template v-if="view === 'detail' && currentProvider">
            <Button
              variant="default"
              :disabled="fetchingModels || !currentProvider.keys?.length"
              @click="handleFetchModelsForCurrent"
            >
              <span
                class="icon-[mdi--cloud-download-outline] mr-1 text-[15px]"
                :class="fetchingModels ? 'animate-spin' : ''"
              />
              {{ fetchingModels ? "获取中…" : "获取模型" }}
            </Button>
            <Button
              variant="default"
              :disabled="batchTesting && batchStopping || currentModels.length === 0"
              @click="handleTestAllCurrentAdapters"
            >
              {{ batchButtonText }}
            </Button>
            <Button variant="primary" :disabled="appState.configSaving" @click="openEditor()">
              新增模型
            </Button>
          </template>
          <template v-else>
            <Button variant="primary" :disabled="appState.configSaving" @click="showAddProvider = true">
              <span class="icon-[mdi--plus] mr-1 text-[15px]" />
              添加供应商
            </Button>
          </template>
        </div>
      </div>
    </div>

    <!-- ── Provider list view ─────────────────────────────────── -->
    <div v-if="view === 'providers'" class="min-h-0 flex-1 overflow-y-auto pr-1">
      <div v-if="providers.length === 0"
        class="flex h-full min-h-[220px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] px-4 text-sm text-[#a3a3a3]"
      >
        尚未配置任何模型。点击「添加供应商」开始。
      </div>
      <div v-else class="grid gap-3 pb-1 [grid-template-columns:repeat(auto-fill,minmax(260px,1fr))]">
        <ProviderCard
          v-for="provider in providers"
          :key="provider.key"
          :provider="provider"
          :disabled="appState.configSaving"
          @enter="enterProvider"
          @delete-all="handleDeleteProvider"
        />
      </div>
    </div>

    <!-- ── Provider detail view ───────────────────────────────── -->
    <div v-else-if="view === 'detail' && currentProvider" class="min-h-0 flex-1 overflow-y-auto pr-1">
      <div v-if="currentModels.length === 0"
        class="flex h-full min-h-[220px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] px-4 text-sm text-[#a3a3a3]"
      >
        该供应商下暂无模型。
      </div>
      <div v-else class="grid gap-3 pb-1 [grid-template-columns:repeat(auto-fill,minmax(250px,1fr))]">
        <Card
          v-for="{ adapter, index } in currentModels"
          :key="adapter.id || `${adapter.baseURL}-${adapter.modelID}-${index}`"
        >
          <div class="flex h-full min-h-[154px] flex-col justify-between gap-3">
            <div class="flex flex-col gap-2.5">
              <!-- Name + type badge -->
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0 flex-1">
                  <div class="truncate text-base font-medium text-white">{{ adapter.displayName }}</div>
                  <div class="mt-1 truncate text-sm text-[#8f8f8f]">{{ adapter.modelID }}</div>
                  <div v-if="adapter.type === 'openai'" class="mt-0.5 truncate text-xs text-[#737373]">
                    {{ adapter.openAIEndpoint || "/v1/responses" }}
                  </div>
                </div>
                <span class="center-row shrink-0 gap-1 rounded-[999px] border border-[#3f3f3f] px-[7px] py-[4px] text-[11px] font-medium text-[#cfcfcf]">
                  <span class="icon-[bxl--openai] text-[14px] !text-white" v-if="adapter.type === 'openai'" />
                  <span class="icon-[logos--claude-icon] text-[14px]" v-else />
                  <span>{{ adapter.type === 'anthropic' ? 'Anthropic' : 'OpenAI' }}</span>
                </span>
              </div>

              <!-- Test result -->
              <ModelAdapterTestCard
                compact
                title="测试"
                empty-text="未测试"
                :result="getAdapterTestResult(adapter)"
              />
            </div>

            <!-- Actions -->
            <div class="center-row flex-wrap justify-end gap-2 border-t border-[#343434] pt-3">
              <Button
                variant="default"
                :disabled="appState.configSaving || batchTesting || isAdapterTesting(adapter)"
                @click="handleTestAdapter(adapter)"
              >
                {{ isAdapterTesting(adapter) ? "测试中..." : "测试" }}
              </Button>
              <Button variant="default" :disabled="appState.configSaving" @click="openEditor(index)">编辑</Button>
              <Button variant="default" :disabled="appState.configSaving" @click="handleDuplicateAdapter(index)">复制</Button>
              <Button variant="text" :disabled="appState.configSaving" @click="handleDeleteAdapter(index)">删除</Button>
            </div>
          </div>
        </Card>
      </div>
    </div>

    <!-- ── Add Provider Modal ─────────────────────────────────── -->
    <AddProviderModal
      :visible="showAddProvider"
      @cancel="showAddProvider = false"
      @save="handleAddProviderSave"
    />
  </div>
</template>