<script setup>
import Button from "@/components/ui/Button.vue";
import Select from "@/components/ui/Select.vue";
import { fetchProviderModels } from "@/services/clientApi";
import {
  appState,
  buildAdapterFromModelEntry,
  collectExistingModelKeys,
  importModelAdapters,
  toUserError,
} from "@/state/appState";
import { formatModelHost, maskSecret } from "@/utils/modelAdapterFormat.js";
import { computed, ref, watch } from "vue";

const props = defineProps({
  visible: { type: Boolean, default: false },
  type: { type: String, default: "openai" },
});

const emit = defineEmits(["update:visible", "imported"]);

const sourceID = ref("");
const entries = ref([]);
const selected = ref(new Set());
const keyword = ref("");
const fetching = ref(false);
const importing = ref(false);
const errorText = ref("");

// fetchSeq 用作拉取请求的版本号：切换来源或重置时自增，使进行中的旧请求结果被丢弃，避免脏写。
let fetchSeq = 0;

// sourceKeyOf 生成来源适配器的稳定键，优先用 id，缺失时回退到 baseURL+modelID 组合。
function sourceKeyOf(adapter) {
  return adapter.id || `${adapter.baseURL}-${adapter.modelID}`;
}

// 同类型的已配置适配器，作为拉取来源候选。
const sourceAdapters = computed(() =>
  appState.modelAdapters.filter((adapter) => adapter.type === props.type),
);

const sourceOptions = computed(() =>
  sourceAdapters.value.map((adapter) => ({
    label: adapter.displayName || adapter.modelID || adapter.baseURL,
    value: sourceKeyOf(adapter),
  })),
);

const currentSource = computed(() => {
  const target = sourceID.value;
  return sourceAdapters.value.find((adapter) => sourceKeyOf(adapter) === target) || null;
});

// 按搜索词过滤后的条目（id / 显示名 / 归属方均参与匹配）。
const filteredEntries = computed(() => {
  const text = keyword.value.trim().toLowerCase();
  if (!text) {
    return entries.value;
  }
  return entries.value.filter((entry) =>
    [entry.id, entry.displayName, entry.ownedBy]
      .filter(Boolean)
      .some((value) => value.toLowerCase().includes(text)),
  );
});

// 当前可见且尚未存在的条目，即可被勾选导入的集合。
const selectableEntries = computed(() => filteredEntries.value.filter((entry) => !entry.existing));

const allSelectableSelected = computed(
  () => selectableEntries.value.length > 0
    && selectableEntries.value.every((entry) => selected.value.has(entry.id)),
);

const selectedCount = computed(() => selected.value.size);
const newCount = computed(() => entries.value.filter((entry) => !entry.existing).length);
const existingCount = computed(() => entries.value.length - newCount.value);

function resetState() {
  fetchSeq += 1;
  entries.value = [];
  selected.value = new Set();
  keyword.value = "";
  errorText.value = "";
  fetching.value = false;
  importing.value = false;
}

watch(
  () => props.visible,
  (open) => {
    if (!open) {
      return;
    }
    resetState();
    const first = sourceAdapters.value[0];
    sourceID.value = first ? sourceKeyOf(first) : "";
  },
);

watch(sourceID, () => {
  fetchSeq += 1;
  entries.value = [];
  selected.value = new Set();
  keyword.value = "";
  errorText.value = "";
});

async function handleFetch() {
  const source = currentSource.value;
  if (!source || fetching.value) {
    return;
  }
  fetching.value = true;
  errorText.value = "";
  entries.value = [];
  selected.value = new Set();
  const seq = (fetchSeq += 1);
  try {
    const result = await fetchProviderModels(source);
    if (seq !== fetchSeq) {
      return;
    }
    const raw = Array.isArray(result)
      ? result
      : Array.isArray(result?.data)
        ? result.data
        : [];
    const mapped = raw
      .map((entry) => ({
        id: String(entry?.id ?? "").trim(),
        displayName: String(entry?.displayName ?? "").trim(),
        ownedBy: String(entry?.ownedBy ?? "").trim(),
      }))
      .filter((entry) => entry.id);
    const existingKeys = collectExistingModelKeys(source, appState.modelAdapters);
    const withFlag = mapped.map((entry) => ({
      ...entry,
      existing: existingKeys.has(entry.id),
    }));
    entries.value = withFlag;
    const next = new Set();
    withFlag.forEach((entry) => {
      if (!entry.existing) {
        next.add(entry.id);
      }
    });
    selected.value = next;
    if (withFlag.length === 0) {
      errorText.value = "接口未返回任何模型";
    }
  } catch (error) {
    errorText.value = toUserError(error);
  } finally {
    fetching.value = false;
  }
}

function toggleSelect(id) {
  const next = new Set(selected.value);
  if (next.has(id)) {
    next.delete(id);
  } else {
    next.add(id);
  }
  selected.value = next;
}

function toggleSelectAll() {
  const selectable = selectableEntries.value;
  const next = new Set(selected.value);
  if (allSelectableSelected.value) {
    selectable.forEach((entry) => next.delete(entry.id));
  } else {
    selectable.forEach((entry) => next.add(entry.id));
  }
  selected.value = next;
}

async function handleConfirm() {
  const source = currentSource.value;
  if (!source || importing.value || selectedCount.value === 0) {
    return;
  }
  importing.value = true;
  errorText.value = "";
  try {
    const chosen = entries.value.filter((entry) => selected.value.has(entry.id) && !entry.existing);
    const adapters = chosen.map((entry) => buildAdapterFromModelEntry(source, entry));
    const result = await importModelAdapters(adapters);
    if (!result.ok) {
      errorText.value = result.error || "导入失败";
      return;
    }
    emit("imported");
    emit("update:visible", false);
  } catch (error) {
    errorText.value = toUserError(error);
  } finally {
    importing.value = false;
  }
}

function handleCancel() {
  emit("update:visible", false);
}
</script>

<template>
  <Teleport to="body">
    <Transition name="fetch-mask">
      <div
        v-if="visible"
        class="fixed inset-0 z-999 flex items-center justify-center bg-black/50 p-4"
        @click.self="handleCancel"
      >
        <Transition name="fetch-content">
          <div
            v-if="visible"
            class="relative z-10 flex w-full max-w-[580px] flex-col overflow-hidden rounded-[8px] p-px shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            style="background: linear-gradient(to bottom, #656565 0%, #3A3A3A 10px, #3A3A3A 100%);"
            @click.stop
          >
            <div class="flex max-h-[80vh] flex-col rounded-[7px] bg-[#292929]">
              <div class="flex shrink-0 items-center justify-between px-5 pb-3 pt-4">
                <h3 class="text-base font-medium text-white">从接口拉取模型</h3>
                <button
                  type="button"
                  class="text-[#8f8f8f] transition-colors hover:text-[#e5e5e5]"
                  @click="handleCancel"
                >
                  <span class="icon-[mdi--close] text-[18px]"></span>
                </button>
              </div>

              <div class="flex shrink-0 flex-col gap-3 px-5">
                <div class="flex items-center gap-2">
                  <span class="w-16 shrink-0 text-sm text-[#a3a3a3]">来源接口</span>
                  <div class="min-w-0 flex-1">
                    <Select
                      v-model="sourceID"
                      :options="sourceOptions"
                      placeholder="选择已配置的模型"
                    />
                  </div>
                  <Button
                    variant="primary"
                    :disabled="!currentSource || fetching"
                    @click="handleFetch"
                  >
                    <span v-if="fetching" class="icon-[mdi--loading] animate-spin text-[14px]"></span>
                    <span v-else class="icon-[mdi--refresh] text-[14px]"></span>
                    <span>{{ fetching ? "拉取中" : "拉取" }}</span>
                  </Button>
                </div>

                <div v-if="currentSource" class="truncate text-xs text-[#737373]">
                  {{ formatModelHost(currentSource.baseURL) }} · {{ maskSecret(currentSource.apiKey) }}
                </div>

                <div v-if="entries.length > 0" class="flex items-center gap-3">
                  <div class="relative flex-1">
                    <span class="icon-[mdi--magnify] pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[14px] text-[#7b7b7b]"></span>
                    <input
                      v-model="keyword"
                      type="text"
                      placeholder="搜索模型 id / 名称..."
                      class="h-8 w-full rounded-[6px] border border-[#3f3f3f] bg-[#1f1f1f] pl-8 pr-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                    />
                  </div>
                  <label class="center-row shrink-0 cursor-pointer gap-1.5 text-xs text-[#d4d4d4]">
                    <input
                      type="checkbox"
                      class="size-4 accent-[#10AD5D]"
                      :checked="allSelectableSelected"
                      :disabled="selectableEntries.length === 0"
                      @change="toggleSelectAll"
                    />
                    <span>全选新项</span>
                  </label>
                </div>
              </div>

              <div class="min-h-0 flex-1 px-5 pt-3">
                <div
                  v-if="entries.length === 0 && !fetching && !errorText"
                  class="flex h-full min-h-[180px] items-center justify-center rounded-[8px] border border-dashed border-[#3a3a3a] bg-[#232323] text-sm text-[#737373]"
                >
                  选择来源接口后点击「拉取」获取模型清单
                </div>

                <div
                  v-else-if="entries.length === 0 && fetching"
                  class="flex h-full min-h-[180px] items-center justify-center text-sm text-[#737373]"
                >
                  拉取中...
                </div>

                <div v-else class="h-full max-h-[44vh] overflow-y-auto rounded-[8px] border border-[#343434] bg-[#1f1f1f]">
                  <label
                    v-for="entry in filteredEntries"
                    :key="entry.id"
                    class="flex cursor-pointer items-center gap-2.5 border-b border-[#2a2a2a] px-3 py-2 text-sm transition-colors last:border-b-0"
                    :class="entry.existing ? 'cursor-not-allowed opacity-50' : 'hover:bg-[#262626]'"
                  >
                    <input
                      type="checkbox"
                      class="size-4 accent-[#10AD5D]"
                      :checked="selected.has(entry.id)"
                      :disabled="entry.existing"
                      @change="toggleSelect(entry.id)"
                    />
                    <span class="min-w-0 flex-1 truncate text-[#e5e5e5]" :title="entry.id">{{ entry.id }}</span>
                    <span v-if="entry.ownedBy" class="shrink-0 text-xs text-[#737373]">{{ entry.ownedBy }}</span>
                    <span
                      v-if="entry.existing"
                      class="shrink-0 rounded-[999px] border border-[#3f3f3f] px-2 py-[2px] text-[10px] text-[#9a9a9a]"
                    >
                      已存在
                    </span>
                  </label>
                  <div v-if="filteredEntries.length === 0" class="px-3 py-6 text-center text-sm text-[#737373]">
                    没有匹配的模型
                  </div>
                </div>
              </div>

              <div class="flex shrink-0 flex-col gap-2 px-5 pb-4 pt-3">
                <div
                  v-if="errorText"
                  class="rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]"
                >
                  {{ errorText }}
                </div>
                <div class="flex items-center justify-between">
                  <span class="text-xs text-[#737373]">
                    <template v-if="entries.length > 0">
                      共 {{ entries.length }} 个 · 新增 {{ newCount }} · 已存在 {{ existingCount }} · 已选 {{ selectedCount }}
                    </template>
                  </span>
                  <div class="flex items-center gap-2">
                    <Button variant="default" :disabled="importing" @click="handleCancel">取消</Button>
                    <Button
                      variant="primary"
                      :disabled="importing || selectedCount === 0"
                      @click="handleConfirm"
                    >
                      {{ importing ? "导入中..." : `批量新增 (${selectedCount})` }}
                    </Button>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.fetch-mask-enter-active,
.fetch-mask-leave-active {
  transition: opacity 0.25s ease, backdrop-filter 0.25s ease;
}
.fetch-mask-enter-from,
.fetch-mask-leave-to {
  opacity: 0;
  backdrop-filter: blur(0);
}
.fetch-content-enter-active,
.fetch-content-leave-active {
  transition: all 0.25s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.fetch-content-enter-from,
.fetch-content-leave-to {
  opacity: 0;
  transform: scale(0.96) translateY(-10px);
}
</style>
