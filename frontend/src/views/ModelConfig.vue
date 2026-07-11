<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import ModelAdapterTestCard from "@/components/ModelAdapterTestCard.vue";
import ModelGroupModal from "@/components/ModelGroupModal.vue";
import { showModal } from "@/composables/useModal";
import { buildModelAdapterGroups } from "@/utils/modelAdapterGroups";
import {
  appState,
  activateModelAdapterGroup,
  createEmptyModelAdapter,
  deleteModelGroup,
  deleteModelAdapterAt,
  discoverAndAddModelAdapters,
  duplicateModelAdapterAt,
  getModelAdapterTestResultByID,
  openModelEditorWindow,
  reloadUserConfig,
  reorderModelGroups,
  runModelAdapterTest,
  saveModelGroup,
  startModelAdapterTest,
  toUserError,
  updateModelGroup,
} from "@/state/appState";
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";

const BATCH_TEST_CONCURRENCY = 10;

const typeTabs = [
  { label: "OpenAI", value: "openai", icon: "icon-[bxl--openai]" },
  { label: "Anthropic", value: "anthropic", icon: "icon-[logos--claude-icon]" },
];

const activeType = ref("openai");
const groupModalVisible = ref(false);
const editingGroup = ref(null);
const expandedGroupKeys = ref(new Set());
const discoveringGroupKey = ref("");
const activatingGroupID = ref("");
const draggingGroupKey = ref("");
const dragOverGroupKey = ref("");
const dragOverGroupPosition = ref("");
const batchTesting = ref(false);
const batchStopping = ref(false);
const batchTotal = ref(0);
const batchCompleted = ref(0);
const batchScopeKey = ref("");
const batchActiveCalls = new Set();
let batchStopRequested = false;

const filteredAdapters = computed(() =>
  appState.modelAdapters.filter((adapter) => adapter.type === activeType.value),
);
const filteredModelGroups = computed(() =>
  appState.modelGroups.filter((group) => group.type === activeType.value),
);
const filteredGroups = computed(() => buildModelAdapterGroups(filteredModelGroups.value, filteredAdapters.value));
const batchButtonText = computed(() => {
  if (batchStopping.value) {
    return "停止中...";
  }
  if (!batchTesting.value) {
    return "测试全部";
  }
  return `停止测试 ${batchCompleted.value}/${batchTotal.value}`;
});

watch(
  () => appState.modelGroups,
  (groups) => {
    if (groups.some((group) => group.type === activeType.value)) {
      return;
    }
    const fallback = typeTabs.find((tab) => groups.some((group) => group.type === tab.value));
    activeType.value = fallback?.value ?? "openai";
  },
  { deep: true, immediate: true },
);

async function showActionError(title, error) {
  await showModal({
    title,
    content: String(error || "服务错误").trim() || "服务错误",
  });
}

function maskSecret(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "-";
  }
  if (text.length <= 8) {
    return `${"*".repeat(Math.max(text.length - 2, 0))}${text.slice(-2)}`;
  }
  return `${text.slice(0, 4)}****${text.slice(-4)}`;
}

function typeLabel(type) {
  return type === "anthropic" ? "Anthropic" : "OpenAI";
}

function formatHost(value) {
  const text = String(value || "").trim();
  if (!text) {
    return "-";
  }
  try {
    const parsed = new URL(text);
    return parsed.host || text;
  } catch {
    return text.replace(/^https?:\/\//, "");
  }
}

function uniqueValues(adapters, key) {
  return Array.from(new Set(adapters.map((adapter) => String(adapter?.[key] || "").trim())));
}

function formatGroupCredential(group) {
  if (String(group.apiKey || "").trim()) {
    return maskSecret(group.apiKey);
  }
  const keys = uniqueValues(group.adapters, "apiKey").filter(Boolean);
  if (keys.length === 0) {
    return "未配置密钥";
  }
  if (keys.length === 1) {
    return maskSecret(keys[0]);
  }
  return `${keys.length} 个访问密钥`;
}

function isGroupExpanded(key) {
  return expandedGroupKeys.value.has(key);
}

function toggleGroup(key) {
  const next = new Set(expandedGroupKeys.value);
  if (next.has(key)) {
    next.delete(key);
  } else {
    next.add(key);
  }
  expandedGroupKeys.value = next;
}

function createModelForGroup(group) {
  const empty = createEmptyModelAdapter();
  const template = group.adapters[0] || empty;
  return {
    ...empty,
    type: group.type,
    baseURL: group.baseURL,
    apiKey: group.apiKey || template.apiKey,
    openAIEndpoint: group.openAIEndpoint || template.openAIEndpoint,
    customHeadersEnabled: group.customHeadersEnabled ?? template.customHeadersEnabled,
    customHeadersJSON: group.customHeadersJSON || template.customHeadersJSON,
  };
}

async function handleSaveGroup(group) {
  const targetGroupID = editingGroup.value?.groupID || "";
  const result = targetGroupID
    ? await updateModelGroup(targetGroupID, group)
    : await saveModelGroup(group);
  if (!result.ok) {
    await showActionError(targetGroupID ? "编辑分组失败" : "添加分组失败", result.error);
    return;
  }
  groupModalVisible.value = false;
  editingGroup.value = null;
}

function openAddGroup() {
  editingGroup.value = null;
  groupModalVisible.value = true;
}

function openEditGroup(group) {
  editingGroup.value = group;
  groupModalVisible.value = true;
}

function closeGroupModal() {
  groupModalVisible.value = false;
  editingGroup.value = null;
}

async function handleDeleteGroup(group) {
  if (!group?.groupID || appState.configSaving) {
    return;
  }
  const confirmed = await showModal({
    title: "删除分组",
    content: `确定删除分组“${group.name || formatHost(group.baseURL)}”吗？该分组下的 ${group.adapters.length} 个模型将同时删除。`,
    confirmText: "删除",
    cancelText: "取消",
  });
  if (!confirmed) {
    return;
  }
  const result = await deleteModelGroup(group.groupID);
  if (!result.ok) {
    await showActionError("删除分组失败", result.error);
  }
}

async function openEditor(index = -1, preset = null) {
  const adapter = index >= 0
    ? appState.modelAdapters[index]
    : preset || {
        ...createEmptyModelAdapter(),
        type: activeType.value,
      };
  try {
    await openModelEditorWindow(index, adapter);
  } catch (error) {
    await showActionError("打开失败", toUserError(error));
  }
}

async function openGroupModelEditor(group) {
  await openEditor(-1, createModelForGroup(group));
}

function isActiveGroup(group) {
  return Boolean(group.groupID) && appState.activeModelGroupID === group.groupID;
}

function canDragGroup(group) {
  return Boolean(group?.groupID) && !appState.configSaving && !batchTesting.value && !discoveringGroupKey.value;
}

function resetGroupDragState() {
  draggingGroupKey.value = "";
  dragOverGroupKey.value = "";
  dragOverGroupPosition.value = "";
}

function handleGroupDragStart(event, group) {
  if (!canDragGroup(group)) {
    event.preventDefault();
    return;
  }
  draggingGroupKey.value = group.key;
  event.dataTransfer.effectAllowed = "move";
  event.dataTransfer.setData("text/plain", group.groupID);
}

function handleGroupDragOver(event, group) {
  if (!canDragGroup(group) || !draggingGroupKey.value || draggingGroupKey.value === group.key) {
    return;
  }
  event.preventDefault();
  event.dataTransfer.dropEffect = "move";
  dragOverGroupKey.value = group.key;
  const rect = event.currentTarget.getBoundingClientRect();
  dragOverGroupPosition.value = event.clientY > rect.top + rect.height / 2 ? "after" : "before";
}

function handleGroupDragLeave(event, group) {
  if (event.currentTarget?.contains?.(event.relatedTarget)) {
    return;
  }
  if (dragOverGroupKey.value === group.key) {
    dragOverGroupKey.value = "";
    dragOverGroupPosition.value = "";
  }
}

async function handleGroupDrop(event, targetGroup) {
  event.preventDefault();
  const sourceGroupID = event.dataTransfer.getData("text/plain");
  const insertAfterTarget = dragOverGroupPosition.value === "after";
  const sourceIndex = filteredGroups.value.findIndex((group) => group.groupID === sourceGroupID);
  const targetIndex = filteredGroups.value.findIndex((group) => group.groupID === targetGroup.groupID);
  resetGroupDragState();
  if (sourceIndex < 0 || targetIndex < 0 || sourceIndex === targetIndex) {
    return;
  }

  const orderedGroupIDs = filteredGroups.value.map((group) => group.groupID).filter(Boolean);
  const [sourceID] = orderedGroupIDs.splice(sourceIndex, 1);
  const nextTargetIndex = orderedGroupIDs.indexOf(targetGroup.groupID);
  if (nextTargetIndex < 0) {
    return;
  }
  orderedGroupIDs.splice(nextTargetIndex + (insertAfterTarget ? 1 : 0), 0, sourceID);

  const result = await reorderModelGroups(orderedGroupIDs, activeType.value);
  if (!result.ok) {
    await reloadUserConfig({ modelAdaptersOnly: true }).catch(() => { });
    await showActionError("分组排序失败", result.error);
  }
}

async function handleActivateGroup(group) {
  if (!group.groupID || activatingGroupID.value || isActiveGroup(group)) {
    return;
  }
  activatingGroupID.value = group.groupID;
  try {
    const result = await activateModelAdapterGroup(group.groupID);
    if (!result.ok) {
      await showActionError("使用分组失败", result.error);
    }
  } catch (error) {
    await showActionError("使用分组失败", toUserError(error));
  } finally {
    activatingGroupID.value = "";
  }
}

async function handleDiscoverGroupModels(group) {
  if (discoveringGroupKey.value || !group.groupID) {
    return;
  }
  discoveringGroupKey.value = group.key;
  let result;
  try {
    result = await discoverAndAddModelAdapters(group);
    if (!result.ok) {
      await showActionError("获取模型失败", result.error);
      return;
    }
    const next = new Set(expandedGroupKeys.value);
    next.add(group.key);
    expandedGroupKeys.value = next;
  } catch (error) {
    await showActionError("获取模型失败", toUserError(error));
    return;
  } finally {
    discoveringGroupKey.value = "";
  }
  await showModal({
    title: "获取模型完成",
    content: `上游返回 ${result.discovered} 个模型，新增 ${result.added} 个，跳过 ${result.skipped} 个已存在模型。`,
  });
}

async function handleDeleteModelAdapter(index) {
  const target = appState.modelAdapters[index];
  if (!target) {
    await showActionError("删除失败", "模型配置不存在，无法删除");
    return;
  }
  const result = await deleteModelAdapterAt(index);
  if (!result.ok) {
    await showActionError("删除失败", result.error);
  }
}

async function handleDuplicateModelAdapter(index) {
  const target = appState.modelAdapters[index];
  if (!target) {
    await showActionError("复制失败", "模型配置不存在，无法复制");
    return;
  }
  const result = await duplicateModelAdapterAt(index);
  if (!result.ok) {
    await showActionError("复制失败", result.error);
  }
}

function getAdapterTestResult(adapter) {
  return getModelAdapterTestResultByID(adapter?.id);
}

function isAdapterTesting(adapter) {
  return getAdapterTestResult(adapter)?.status === "running";
}

async function handleTestModelAdapter(adapter) {
  try {
    await runModelAdapterTest(adapter);
  } catch (_error) {
    // 失败结果会通过事件同步到界面，这里不再额外弹窗打断用户。
  }
}

function isCancelError(error) {
  return String(error?.name || "").trim() === "CancelError";
}

async function stopBatchTesting() {
  if (!batchTesting.value || batchStopping.value) {
    return;
  }
  batchStopRequested = true;
  batchStopping.value = true;
  const activeCalls = Array.from(batchActiveCalls);
  await Promise.allSettled(
    activeCalls.map((call) => (typeof call?.cancel === "function" ? call.cancel("batch-stop") : undefined)),
  );
}

async function runModelAdapterBatch(adapters, scopeKey) {
  if (batchTesting.value) {
    if (batchScopeKey.value === scopeKey) {
      await stopBatchTesting();
    }
    return;
  }
  const targets = adapters.slice();
  if (targets.length === 0) {
    return;
  }
  batchScopeKey.value = scopeKey;
  batchStopRequested = false;
  batchTesting.value = true;
  batchStopping.value = false;
  batchTotal.value = targets.length;
  batchCompleted.value = 0;
  let nextIndex = 0;
  try {
    const workers = Array.from({ length: Math.min(BATCH_TEST_CONCURRENCY, targets.length) }, async () => {
      while (!batchStopRequested) {
        const currentIndex = nextIndex;
        nextIndex += 1;
        if (currentIndex >= targets.length) {
          return;
        }
        const adapter = targets[currentIndex];
        const call = startModelAdapterTest(adapter);
        batchActiveCalls.add(call);
        try {
          await call;
        } catch (error) {
          if (!isCancelError(error) && !batchStopRequested) {
            // 单个失败结果由卡片自行展示，这里继续后续测试。
          }
        } finally {
          batchActiveCalls.delete(call);
          batchCompleted.value += 1;
        }
      }
    });
    await Promise.allSettled(workers);
  } finally {
    batchActiveCalls.clear();
    batchStopRequested = false;
    batchTesting.value = false;
    batchStopping.value = false;
    batchScopeKey.value = "";
  }
}

async function handleTestAllModelAdapters() {
  if (batchTesting.value) {
    await stopBatchTesting();
    return;
  }
  await runModelAdapterBatch(filteredAdapters.value, `all:${activeType.value}`);
}

async function handleTestModelGroup(group) {
  await runModelAdapterBatch(group.adapters, group.key);
}

function groupTestButtonText(group) {
  if (batchTesting.value && batchScopeKey.value === group.key) {
    return batchStopping.value ? "停止中..." : `停止 ${batchCompleted.value}/${batchTotal.value}`;
  }
  return "测试分组";
}

onMounted(async () => {
  await reloadUserConfig({ modelAdaptersOnly: true }).catch(() => { });
});

onBeforeUnmount(() => {
  void stopBatchTesting();
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col p-4 pt-0 text-[#e5e5e5] overflow-hidden">
    <div class="shrink-0 pb-4">
      <div class="flex items-center justify-between gap-4">
        <div class="center-row gap-2">
          <button
            v-for="tab in typeTabs"
            :key="tab.value"
            type="button"
            class="center-row gap-2 rounded-[8px] border px-3 py-2 text-sm transition-colors duration-150"
            :class="activeType === tab.value
              ? 'border-[#1ca35a] bg-[#123322] text-white'
              : 'border-[#343434] bg-[#252525] text-[#a3a3a3] hover:border-[#4a4a4a] hover:text-[#e5e5e5]'"
            @click="activeType = tab.value"
          >
            <span :class="[tab.icon, 'text-[16px]']"></span>
            <span>{{ tab.label }}</span>
          </button>
        </div>
        <div class="center-row gap-2">
          <Button
            variant="default"
            :disabled="appState.configSaving || batchTesting"
            @click="openAddGroup"
          >
            添加分组
          </Button>
          <Button
            variant="default"
            :disabled="appState.configSaving || (!batchTesting && filteredAdapters.length === 0)"
            @click="handleTestAllModelAdapters"
          >
            {{ batchButtonText }}
          </Button>
          <Button variant="primary" :disabled="appState.configSaving || batchTesting" @click="openEditor()">新增模型</Button>
        </div>
      </div>
    </div>

    <div class="min-h-0 flex-1">
      <div v-if="filteredGroups.length === 0"
        class="flex h-full min-h-[220px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] px-4 text-sm text-[#a3a3a3]">
        当前还没有配置任何 {{ typeLabel(activeType) }} 分组。
      </div>

      <div v-else class="h-full min-h-0 overflow-y-auto pr-1">
        <div class="flex flex-col gap-3 pb-1">
          <Card
            v-for="group in filteredGroups"
            :key="group.key"
            :class="[
              draggingGroupKey === group.key ? 'opacity-60' : '',
              dragOverGroupKey === group.key ? 'ring-2 ring-[#10AD5D]/70' : '',
              dragOverGroupKey === group.key && dragOverGroupPosition === 'before' ? 'ring-offset-2 ring-offset-[#10AD5D]/30' : '',
              dragOverGroupKey === group.key && dragOverGroupPosition === 'after' ? 'shadow-[0_8px_0_0_rgba(16,173,93,0.35)]' : '',
            ]"
            @dragover="handleGroupDragOver($event, group)"
            @dragleave="handleGroupDragLeave($event, group)"
            @drop="handleGroupDrop($event, group)"
          >
            <div class="flex flex-col gap-3">
              <div class="flex flex-wrap items-center justify-between gap-3">
                <div class="flex min-w-0 items-center gap-3">
                  <button
                    type="button"
                    class="center-row h-9 w-9 shrink-0 cursor-grab rounded-[8px] border border-[#3f3f3f] bg-[#232323] text-[#8f8f8f] transition-colors duration-150 hover:border-[#4a4a4a] hover:text-white active:cursor-grabbing disabled:cursor-not-allowed disabled:opacity-50"
                    :disabled="!canDragGroup(group)"
                    :draggable="canDragGroup(group)"
                    title="拖拽排序"
                    @dragstart.stop="handleGroupDragStart($event, group)"
                    @dragend="resetGroupDragState"
                  >
                    <span class="icon-[mdi--drag-vertical] text-[18px]"></span>
                  </button>
                  <div class="center-row h-9 w-9 shrink-0 rounded-[8px] border border-[#3f3f3f] bg-[#232323]">
                    <span class="icon-[bxl--openai] text-[18px] !text-white" v-if="group.type === 'openai'"></span>
                    <span class="icon-[logos--claude-icon] text-[18px]" v-else></span>
                  </div>
                  <div class="min-w-0">
                    <div class="flex flex-wrap items-center gap-2">
                      <div class="truncate text-base font-medium text-white">{{ group.name || formatHost(group.baseURL) }}</div>
                      <span class="rounded-[999px] border border-[#3f3f3f] px-2 py-0.5 text-[11px] text-[#cfcfcf]">
                        {{ group.adapters.length }} 个模型
                      </span>
                      <span class="rounded-[999px] border border-[#3f3f3f] px-2 py-0.5 text-[11px] text-[#8f8f8f]">
                        {{ formatGroupCredential(group) }}
                      </span>
                    </div>
                    <div class="mt-1 max-w-[520px] truncate text-xs text-[#737373]" :title="group.baseURL">
                      {{ group.baseURL }}
                    </div>
                  </div>
                </div>
                <div class="center-row flex-wrap gap-2">
                  <Button
                    :variant="isActiveGroup(group) ? 'primary' : 'default'"
                    :disabled="appState.configSaving || Boolean(activatingGroupID) || !group.groupID || isActiveGroup(group)"
                    @click="handleActivateGroup(group)"
                  >
                    {{ activatingGroupID === group.groupID ? "切换中..." : (isActiveGroup(group) ? "当前分组" : "使用当前分组") }}
                  </Button>
                  <Button
                    variant="default"
                    :disabled="appState.configSaving || group.adapters.length === 0 || (batchTesting && batchScopeKey !== group.key)"
                    @click="handleTestModelGroup(group)"
                  >
                    {{ groupTestButtonText(group) }}
                  </Button>
                  <Button
                    variant="default"
                    :disabled="appState.configSaving || batchTesting || Boolean(discoveringGroupKey)"
                    @click="handleDiscoverGroupModels(group)"
                  >
                    {{ discoveringGroupKey === group.key ? "获取中..." : "获取全部模型" }}
                  </Button>
                  <Button variant="default" :disabled="appState.configSaving || batchTesting" @click="openGroupModelEditor(group)">
                    添加模型
                  </Button>
                  <Button
                    variant="default"
                    :disabled="appState.configSaving || batchTesting || Boolean(discoveringGroupKey)"
                    @click="openEditGroup(group)"
                  >
                    编辑分组
                  </Button>
                  <Button
                    variant="text"
                    :disabled="appState.configSaving || batchTesting || Boolean(discoveringGroupKey)"
                    @click="handleDeleteGroup(group)"
                  >
                    删除分组
                  </Button>
                  <Button variant="text" @click="toggleGroup(group.key)">
                    {{ isGroupExpanded(group.key) ? "收起" : "展开" }}
                    <span
                      class="ml-1 text-[14px] transition-transform"
                      :class="isGroupExpanded(group.key) ? 'icon-[mdi--chevron-up]' : 'icon-[mdi--chevron-down]'"
                    ></span>
                  </Button>
                </div>
              </div>

              <div v-if="isGroupExpanded(group.key)" class="border-t border-[#3a3a3a]">
                <div
                  v-for="(adapter, index) in group.adapters"
                  :key="adapter.id || `${adapter.baseURL}-${adapter.modelID}-${index}`"
                  class="flex flex-col gap-3 border-b border-[#343434] py-3 last:border-b-0 last:pb-0 md:flex-row md:items-center"
                >
                  <div class="min-w-0 flex-[1.2]">
                    <div class="truncate text-sm font-medium text-white">{{ adapter.displayName }}</div>
                    <div class="mt-1 truncate text-xs text-[#8f8f8f]">{{ adapter.modelID }}</div>
                    <div v-if="adapter.type === 'openai'" class="mt-0.5 truncate text-[11px] text-[#666]">
                      {{ adapter.openAIEndpoint || "/v1/responses" }}
                    </div>
                  </div>
                  <div class="min-w-[120px] flex-[0.7] rounded-[8px] bg-[#232323] px-3 py-2">
                    <div class="text-[10px] uppercase tracking-[0.08em] text-[#666]">API Key</div>
                    <div class="mt-1 truncate text-xs text-[#d4d4d4]">{{ maskSecret(adapter.apiKey) }}</div>
                  </div>
                  <div class="min-w-[180px] flex-1">
                    <ModelAdapterTestCard
                      compact
                      title="测试"
                      empty-text="未测试"
                      :result="getAdapterTestResult(adapter)"
                    />
                  </div>
                  <div class="center-row shrink-0 flex-wrap justify-end gap-2">
                    <Button
                      variant="default"
                      :disabled="appState.configSaving || batchTesting || isAdapterTesting(adapter)"
                      @click="handleTestModelAdapter(adapter)"
                    >
                      {{ isAdapterTesting(adapter) ? "测试中..." : "测试" }}
                    </Button>
                    <Button variant="default" :disabled="appState.configSaving" @click="openEditor(appState.modelAdapters.indexOf(adapter))">编辑</Button>
                    <Button variant="default" :disabled="appState.configSaving" @click="handleDuplicateModelAdapter(appState.modelAdapters.indexOf(adapter))">复制</Button>
                    <Button variant="text" :disabled="appState.configSaving"
                      @click="handleDeleteModelAdapter(appState.modelAdapters.indexOf(adapter))">删除</Button>
                  </div>
                </div>
              </div>
            </div>
          </Card>
        </div>
      </div>
    </div>

    <ModelGroupModal
      :visible="groupModalVisible"
      :type="activeType"
      :group="editingGroup"
      :saving="appState.configSaving"
      @cancel="closeGroupModal"
      @save="handleSaveGroup"
    />
  </div>
</template>
