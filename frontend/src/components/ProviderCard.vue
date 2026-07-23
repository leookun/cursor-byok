<script setup>
import InputModal from "@/components/ui/InputModal.vue";
import { appState, persistUserConfig } from "@/state/appState";
import { computed, ref } from "vue";

const props = defineProps({
  provider: {
    type: Object,
    required: true,
    // { host, baseURL, type, keys: string[], models: [], key: string }
  },
  disabled: { type: Boolean, default: false },
});

const emit = defineEmits(["enter", "deleteAll"]);

const showAddKey = ref(false);
const newKeyValue = ref("");
const isEditingName = ref(false);
const editingName = ref("");
const showAllKeys = ref(false);

const allKeys = computed(() => {
  const keysFromProvider = props.provider.keys || [];
  const adapterKeys = (props.provider.models || [])
    .map((m) => m.apiKey)
    .filter(Boolean);
  const merged = [...new Set([...keysFromProvider, ...adapterKeys])];
  const seen = new Set();
  return merged.filter((k) => {
    const t = String(k || "").trim();
    if (!t || seen.has(t)) return false;
    seen.add(t);
    return true;
  });
});

function maskSecret(value) {
  const text = String(value || "").trim();
  if (!text) return "-";
  if (text.length <= 8)
    return `${"*".repeat(Math.max(text.length - 2, 0))}${text.slice(-2)}`;
  return `${text.slice(0, 4)}****${text.slice(-4)}`;
}

async function handleAddKey() {
  const key = newKeyValue.value.trim();
  if (!key) return;
  const keys = [...(props.provider.keys || [])];
  if (keys.includes(key)) {
    newKeyValue.value = "";
    showAddKey.value = false;
    return;
  }
  keys.push(key);
  await saveProviderKeys(keys);
  newKeyValue.value = "";
  showAddKey.value = false;
}

async function handleRemoveKey(index) {
  const keys = [...(props.provider.keys || [])];
  if (keys.length <= 1) return;
  keys.splice(index, 1);
  await saveProviderKeys(keys);
}

async function saveProviderKeys(newKeys) {
  // Update providers in appState
  const providers = Array.isArray(appState.providers) ? [...appState.providers] : [];
  const idx = providers.findIndex(
    (p) =>
      String(p?.baseURL || "").trim() === props.provider.baseURL &&
      String(p?.type || "").trim().toLowerCase() === props.provider.type,
  );
  if (idx >= 0) {
    providers[idx] = { ...providers[idx], apiKeys: newKeys, apiKey: newKeys[0] || "" };
  } else {
    providers.push({
      id: "",
      name: props.provider.host,
      type: props.provider.type,
      baseURL: props.provider.baseURL,
      apiKey: newKeys[0] || "",
      apiKeys: newKeys,
      models: [],
    });
  }
  appState.providers = providers;
  const result = await persistUserConfig();
  if (!result?.ok) {
    console.error("Failed to save provider keys:", result?.error);
  }
}

function startEditName() {
  editingName.value = props.provider.name || props.provider.host;
  isEditingName.value = true;
}

async function saveProviderName() {
  const newName = editingName.value.trim();
  isEditingName.value = false;
  // If name is empty or same as host, remove custom name (keep default host display).
  const providers = Array.isArray(appState.providers) ? [...appState.providers] : [];
  const idx = providers.findIndex(
    (p) =>
      String(p?.baseURL || "").trim() === props.provider.baseURL &&
      String(p?.type || "").trim().toLowerCase() === props.provider.type,
  );
  if (idx >= 0) {
    if (newName && newName !== props.provider.host) {
      providers[idx] = { ...providers[idx], name: newName };
    } else {
      // Clear custom name — let it fall back to host.
      providers[idx] = { ...providers[idx], name: "" };
    }
  } else {
    // Provider entry doesn't exist yet; create one with the name.
    if (newName && newName !== props.provider.host) {
      providers.push({
        id: "",
        name: newName,
        type: props.provider.type,
        baseURL: props.provider.baseURL,
        apiKey: props.provider.keys?.[0] || "",
        apiKeys: props.provider.keys || [],
        models: [],
      });
    }
  }
  appState.providers = providers;
  const result = await persistUserConfig();
  if (!result?.ok) {
    console.error("Failed to save provider name:", result?.error);
  }
}
</script>

<template>
  <div
    class="group relative flex cursor-pointer flex-col gap-3 rounded-[10px] border border-[#343434] bg-[#242424] p-4 transition-all duration-150 hover:border-[#10AD5D]/60 hover:bg-[#272727]"
    @click="emit('enter', provider)"
  >
    <!-- Header: name + type badge -->
    <div class="flex items-start justify-between gap-2">
      <div class="min-w-0 flex-1">
        <div v-if="!isEditingName" class="flex items-center gap-1.5">
          <div class="truncate text-base font-semibold text-white">
            {{ provider.name || provider.host }}
          </div>
          <button
            type="button"
            class="shrink-0 rounded p-0.5 text-[#555] opacity-0 transition hover:text-[#10AD5D] group-hover:opacity-100"
            title="编辑供应商名称"
            @click.stop="startEditName"
          >
            <span class="icon-[mdi--pencil] text-[12px]" />
          </button>
        </div>
        <input
          v-else
          v-model="editingName"
          type="text"
          class="w-full rounded-[4px] border border-[#10AD5D] bg-[#1c1c1c] px-1.5 py-0.5 text-base font-semibold text-white outline-none"
          @click.stop
          @blur="saveProviderName"
          @keydown.enter.prevent="saveProviderName"
        />
        <div class="mt-0.5 truncate text-xs text-[#737373]">
          {{ provider.baseURL }}
        </div>
      </div>
      <span
        class="center-row shrink-0 gap-1 rounded-[999px] border border-[#3f3f3f] px-[7px] py-[3px] text-[11px] font-medium text-[#cfcfcf]"
      >
        <span
          class="text-[13px] !text-white"
          :class="provider.type === 'anthropic' ? 'icon-[logos--claude-icon]' : 'icon-[bxl--openai]'"
        />
        <span>{{ provider.type === "anthropic" ? "Anthropic" : "OpenAI" }}</span>
      </span>
    </div>

    <!-- API Keys section -->
    <div class="flex flex-col gap-1.5">
      <div class="flex items-center justify-between">
        <span class="text-[11px] uppercase tracking-[0.08em] text-[#555]">
          API Keys ({{ allKeys.length }})
        </span>
        <button
          type="button"
          :disabled="disabled"
          class="center-row gap-1 rounded-[4px] px-1.5 py-0.5 text-[11px] text-[#10AD5D] opacity-0 transition hover:bg-[#10AD5D]/10 group-hover:opacity-100 disabled:pointer-events-none"
          title="添加 API Key"
          @click.stop="showAddKey = true"
        >
          <span class="icon-[mdi--plus] text-[13px]" />
          <span>添加</span>
        </button>
      </div>

      <!-- Collapsed: show first key + count -->
      <div
        v-if="!showAllKeys && allKeys.length > 0"
        class="flex cursor-pointer items-center gap-2 rounded-[5px] bg-[#1c1c1c] px-2.5 py-1.5"
        @click.stop="showAllKeys = true"
      >
        <span class="flex-1 truncate font-mono text-xs text-[#d4d4d4]">{{
          maskSecret(allKeys[0])
        }}</span>
        <span v-if="allKeys.length > 1" class="text-[10px] text-[#10AD5D]"
          >+{{ allKeys.length - 1 }}</span
        >
        <span class="icon-[mdi--chevron-down] text-[12px] text-[#555]" />
      </div>

      <!-- Expanded: show all keys -->
      <div v-if="showAllKeys" class="flex flex-col gap-1">
        <div
          v-for="(k, i) in allKeys"
          :key="i"
          class="flex items-center justify-between gap-2 rounded-[5px] bg-[#1c1c1c] px-2.5 py-1.5"
        >
          <span class="truncate font-mono text-xs text-[#d4d4d4]">{{
            maskSecret(k)
          }}</span>
          <button
            v-if="allKeys.length > 1"
            type="button"
            :disabled="disabled"
            class="shrink-0 rounded p-0.5 text-[#555] transition hover:bg-[#3a1212] hover:text-[#f87171]"
            title="移除该 Key"
            @click.stop="handleRemoveKey(i)"
          >
            <span class="icon-[mdi--close] text-[13px]" />
          </button>
        </div>
        <!-- Collapse button -->
        <button
          type="button"
          class="self-end rounded-[4px] px-1.5 py-0.5 text-[10px] text-[#555] hover:text-[#a3a3a3]"
          @click.stop="showAllKeys = false"
        >
          <span class="icon-[mdi--chevron-up] text-[11px]" /> 收起
        </button>
      </div>
    </div>

    <!-- Models preview -->
    <div class="flex items-end justify-between gap-2">
      <div class="flex min-w-0 flex-1 flex-wrap gap-1">
        <span
          v-for="m in provider.models.slice(0, 4)"
          :key="m.id"
          class="truncate rounded-[4px] bg-[#2e2e2e] px-1.5 py-0.5 text-[11px] text-[#8f8f8f]"
          :title="m.modelID"
        >
          {{ m.displayName || m.modelID }}
        </span>
        <span
          v-if="provider.models.length > 4"
          class="rounded-[4px] bg-[#2e2e2e] px-1.5 py-0.5 text-[11px] text-[#666]"
        >
          +{{ provider.models.length - 4 }}
        </span>
      </div>
    </div>

    <!-- Bottom: delete + enter -->
    <div class="flex items-center justify-end gap-1 border-t border-[#343434] pt-3">
      <button
        type="button"
        :disabled="disabled"
        class="rounded-[6px] p-1.5 text-[#555] opacity-0 transition group-hover:opacity-100 hover:bg-[#3a1212] hover:text-[#f87171] disabled:pointer-events-none"
        title="删除该供应商所有模型"
        @click.stop="emit('deleteAll', provider)"
      >
        <span class="icon-[mdi--trash-can-outline] text-[14px]" />
      </button>
      <span class="icon-[mdi--chevron-right] text-[18px] text-[#555] transition group-hover:text-[#10AD5D]" />
    </div>

    <!-- Add Key Modal -->
    <InputModal
      v-model:visible="showAddKey"
      v-model="newKeyValue"
      title="添加 API Key"
      placeholder="sk-..."
      @confirm="handleAddKey"
      @cancel="newKeyValue = ''"
    />
  </div>
</template>
