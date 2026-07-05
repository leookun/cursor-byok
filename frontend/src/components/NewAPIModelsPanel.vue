<script setup>
import Button from "@/components/ui/Button.vue";
import Card from "@/components/ui/Card.vue";
import Input from "@/components/ui/Input.vue";
import Select from "@/components/ui/Select.vue";
import { useMessage } from "@/composables/useMessage";
import { showModal } from "@/composables/useModal";
import { newAPIGetModels, newAPIImportModels } from "@/services/clientApi";
import {
  OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  OPENAI_ENDPOINT_RESPONSES,
  reloadUserConfig,
  toUserError,
} from "@/state/appState";
import { computed, onMounted, ref } from "vue";

const props = defineProps({
  showBackButton: { type: Boolean, default: false },
  closeLabel: { type: String, default: "关闭" },
});
const emit = defineEmits(["close", "imported"]);
const message = useMessage();

const typeTabs = [
  { label: "OpenAI", value: "openai", icon: "icon-[bxl--openai]" },
  { label: "Anthropic", value: "anthropic", icon: "icon-[logos--claude-icon]" },
];

const openAIEndpointOptions = [
  { label: "/v1/responses", value: OPENAI_ENDPOINT_RESPONSES },
  { label: "/v1/chat/completions", value: OPENAI_ENDPOINT_CHAT_COMPLETIONS },
];

const DEFAULT_GROUP_SETTING = { type: "openai", endpoint: OPENAI_ENDPOINT_CHAT_COMPLETIONS };

const groups = ref([]);
const loading = ref(false);
const importing = ref(false);
const searchKeyword = ref("");
const selectedKeys = ref(new Set());
// key: group.tokenId, value: { type, endpoint }
const groupSettings = ref({});

const filteredGroups = computed(() => {
  const keyword = searchKeyword.value.trim().toLowerCase();
  if (!keyword) {
    return groups.value;
  }
  return groups.value
    .map((group) => {
      const tokenName = String(group.tokenName || "").toLowerCase();
      const matchesToken = tokenName.includes(keyword);
      const items = Array.isArray(group.models) ? group.models.filter((m) => {
        const id = String(m.id || "").toLowerCase();
        const owner = String(m.owned_by || "").toLowerCase();
        return matchesToken || id.includes(keyword) || owner.includes(keyword);
      }) : [];
      if (matchesToken && items.length === 0) {
        return { ...group, models: Array.isArray(group.models) ? group.models : [] };
      }
      return { ...group, models: items };
    })
    .filter((group) => group.error || (Array.isArray(group.models) && group.models.length > 0));
});

const selectedCount = computed(() => selectedKeys.value.size);
const hasSelection = computed(() => selectedKeys.value.size > 0);

function modelKey(model) {
  return `${model.tokenId}:${model.id}`;
}

function toggleSelect(model) {
  const key = modelKey(model);
  const next = new Set(selectedKeys.value);
  if (next.has(key)) {
    next.delete(key);
  } else {
    next.add(key);
  }
  selectedKeys.value = next;
}

function toggleGroup(group) {
  const items = Array.isArray(group.models) ? group.models : [];
  if (!items.length) return;
  const next = new Set(selectedKeys.value);
  const keys = items.map(modelKey);
  const allSelected = keys.every((key) => next.has(key));
  if (allSelected) {
    keys.forEach((key) => next.delete(key));
  } else {
    keys.forEach((key) => next.add(key));
  }
  selectedKeys.value = next;
}

function isGroupAllSelected(group) {
  const items = Array.isArray(group.models) ? group.models : [];
  return items.length > 0 && items.every((model) => selectedKeys.value.has(modelKey(model)));
}

function isSelected(model) {
  return selectedKeys.value.has(modelKey(model));
}

function getGroupSetting(group) {
  return groupSettings.value[group.tokenId] ?? DEFAULT_GROUP_SETTING;
}

function setGroupSetting(tokenId, patch) {
  const prev = groupSettings.value[tokenId] ?? DEFAULT_GROUP_SETTING;
  groupSettings.value = {
    ...groupSettings.value,
    [tokenId]: { ...prev, ...patch },
  };
}

async function fetchModels() {
  loading.value = true;
  try {
    const result = await newAPIGetModels();
    groups.value = Array.isArray(result) ? result : [];
  } catch (error) {
    await showModal({
      title: "获取模型列表失败",
      content: toUserError(error),
    });
    groups.value = [];
  } finally {
    loading.value = false;
  }
}

async function handleImport() {
  if (!selectedKeys.value.size) {
    message.error("请先选择要导入的模型");
    return;
  }
  const selected = [];
  for (const group of groups.value) {
    const setting = getGroupSetting(group);
    for (const model of group.models || []) {
      if (selectedKeys.value.has(modelKey(model))) {
        selected.push({
          ...model,
          type: setting.type,
          openAIEndpoint: setting.type === "openai" ? setting.endpoint : "",
        });
      }
    }
  }
  importing.value = true;
  try {
    const result = await newAPIImportModels({ models: selected });
    const msg = `已导入 ${result.imported} 个模型` + (result.skipped > 0 ? `，跳过 ${result.skipped} 个重复/失败项` : "");
    message.success(msg);
    await reloadUserConfig().catch(() => {});
    emit("imported", result);
    emit("close");
  } catch (error) {
    await showModal({
      title: "导入失败",
      content: toUserError(error),
    });
  } finally {
    importing.value = false;
  }
}

onMounted(() => {
  void fetchModels();
});
</script>

<template>
  <div class="flex h-full min-h-0 flex-col gap-4 overflow-hidden text-[#e5e5e5]">
    <Card>
      <div class="flex flex-wrap items-center justify-between gap-4">
        <div class="flex items-center gap-3">
          <Button v-if="showBackButton" variant="default" @click="$emit('close')">
            <span class="icon-[mdi--arrow-left] text-[16px]"></span>
            <span>{{ closeLabel }}</span>
          </Button>
          <div>
            <div class="text-base font-medium text-white">导入 NewAPI 模型</div>
            <div class="text-sm text-[#a3a3a3]">按 API 令牌分组展示模型，避免不同令牌的模型权限混淆</div>
          </div>
        </div>
        <div class="flex items-center gap-3">
          <div class="text-sm text-[#a3a3a3]">已选 {{ selectedCount }} 个模型</div>
          <div class="w-[220px] max-w-full">
            <Input v-model="searchKeyword" placeholder="搜索模型名或令牌名..." />
          </div>
          <Button variant="primary" :disabled="!hasSelection || importing" @click="handleImport">
            {{ importing ? "导入中..." : "导入选中" }}
          </Button>
        </div>
      </div>
    </Card>

    <div class="flex-1 min-h-0 overflow-y-auto">
      <div v-if="loading" class="py-8 text-center text-sm text-[#8f8f8f]">正在加载模型列表...</div>
      <div v-else-if="filteredGroups.length === 0" class="py-8 text-center text-sm text-[#8f8f8f]">
        {{ searchKeyword ? "未找到匹配的模型或令牌" : "暂无可用模型" }}
      </div>
      <div v-else class="flex flex-col gap-3">
        <Card v-for="group in filteredGroups" :key="group.tokenId">
          <div class="flex flex-col gap-3">
            <div class="flex items-center justify-between gap-4">
              <div class="flex items-center gap-2 cursor-pointer" @click="toggleGroup(group)">
                <span
                  :class="isGroupAllSelected(group)
                    ? 'icon-[mdi--checkbox-multiple-marked]'
                    : 'icon-[mdi--checkbox-multiple-blank-outline]'"
                  class="text-[18px] text-[#10AD5D]"
                ></span>
                <span class="text-sm font-medium text-white">{{ group.tokenName || `令牌 #${group.tokenId}` }}</span>
                <span class="rounded-full border border-[#3f3f3f] px-2 py-0.5 text-xs text-[#a3a3a3]">{{ group.models?.length || 0 }} 个模型</span>
                <span
                  class="rounded-full px-2 py-0.5 text-xs"
                  :class="group.modelLimitsEnabled ? 'bg-[#2a2012] text-[#fbbf24]' : 'bg-[#1f2c20] text-[#86efac]'"
                >
                  {{ group.modelLimitsEnabled ? '已限制模型' : '未限制模型' }}
                </span>
              </div>
              <div class="text-xs text-[#8f8f8f]">Token ID: {{ group.tokenId }}</div>
            </div>

            <!-- 协议类型 + 端点选择器 -->
            <div class="flex flex-wrap items-center gap-3">
              <div class="center-row gap-2">
                <span class="text-xs text-[#8f8f8f]">协议</span>
                <button
                  v-for="tab in typeTabs"
                  :key="tab.value"
                  type="button"
                  class="center-row gap-1.5 rounded-[8px] border px-2.5 py-1.5 text-xs transition-colors duration-150"
                  :class="getGroupSetting(group).type === tab.value
                    ? 'border-[#1ca35a] bg-[#123322] text-white'
                    : 'border-[#343434] bg-[#252525] text-[#a3a3a3] hover:border-[#4a4a4a] hover:text-[#e5e5e5]'"
                  @click="setGroupSetting(group.tokenId, { type: tab.value })"
                >
                  <span :class="[tab.icon, 'text-[14px]']"></span>
                  <span>{{ tab.label }}</span>
                </button>
              </div>
              <div v-if="getGroupSetting(group).type === 'openai'" class="center-row gap-2">
                <span class="text-xs text-[#8f8f8f]">端点</span>
                <div class="w-[200px] max-w-full">
                  <Select
                    :model-value="getGroupSetting(group).endpoint"
                    :options="openAIEndpointOptions"
                    @update:model-value="setGroupSetting(group.tokenId, { endpoint: $event })"
                  />
                </div>
              </div>
            </div>

            <div v-if="group.error" class="rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]">
              此令牌拉取模型失败：{{ group.error }}
            </div>

            <div v-else class="grid grid-cols-1 gap-1 sm:grid-cols-2">
              <div
                v-for="model in group.models"
                :key="modelKey(model)"
                class="flex items-center gap-2 cursor-pointer rounded-[6px] px-2 py-2 hover:bg-[#1f1f1f]"
                @click="toggleSelect(model)"
              >
                <span
                  :class="isSelected(model) ? 'icon-[mdi--checkbox-marked]' : 'icon-[mdi--checkbox-blank-outline]'"
                  class="text-[16px] text-[#10AD5D]"
                ></span>
                <div class="min-w-0 flex-1">
                  <div class="truncate text-sm text-white">{{ model.id }}</div>
                  <div class="truncate text-xs text-[#8f8f8f]">{{ model.owned_by || 'other' }}</div>
                </div>
              </div>
            </div>
          </div>
        </Card>
      </div>
    </div>
  </div>
</template>
