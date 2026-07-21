<script setup>
import Button from "@/components/ui/Button.vue";
import Select from "@/components/ui/Select.vue";
import { fetchModelsFromProvider } from "@/services/clientApi";
import { createEmptyModelAdapter, normalizeModelAdapter } from "@/state/appState";
import { reactive, ref, watch } from "vue";

const modelTypeOptions = [
  { label: "openai", value: "openai", icon: "icon-[bxl--openai]" },
  { label: "anthropic", value: "anthropic", icon: "icon-[logos--claude-icon]" },
];

const props = defineProps({
  visible: { type: Boolean, default: false },
});

const emit = defineEmits(["cancel", "save"]);

const form = reactive({ baseURL: "", keys: [], type: "openai", name: "" });
const newKeyInput = ref("");
const fetchingModels = ref(false);
const fetchedModels = ref([]);
const selectedModels = ref(new Set());
const fetchError = ref("");

watch(
  () => props.visible,
  (v) => {
    if (v) {
      form.baseURL = "";
      form.keys = [];
      form.type = "openai";
      form.name = "";
      newKeyInput.value = "";
      fetchedModels.value = [];
      selectedModels.value = new Set();
      fetchError.value = "";
    }
  },
);

function addKey() {
  const key = newKeyInput.value.trim();
  if (!key || form.keys.includes(key)) {
    newKeyInput.value = "";
    return;
  }
  form.keys.push(key);
  newKeyInput.value = "";
}

function removeKey(index) {
  form.keys.splice(index, 1);
}

function maskSecret(value) {
  const text = String(value || "").trim();
  if (!text) return "-";
  if (text.length <= 8)
    return `${"*".repeat(Math.max(text.length - 2, 0))}${text.slice(-2)}`;
  return `${text.slice(0, 4)}****${text.slice(-4)}`;
}

async function handleFetchModels() {
  const baseURL = form.baseURL.trim();
  const apiKey = form.keys[0] || "";
  if (!baseURL || !apiKey) {
    fetchError.value = "请填写 baseURL 和至少一个 API Key";
    return;
  }
  fetchingModels.value = true;
  fetchError.value = "";
  fetchedModels.value = [];
  selectedModels.value = new Set();
  try {
    const res = await fetchModelsFromProvider(baseURL, apiKey, form.type);
    if (res.error) {
      fetchError.value = res.error;
    } else {
      fetchedModels.value = res.models || [];
      selectedModels.value = new Set(fetchedModels.value);
      if (fetchedModels.value.length === 0) {
        fetchError.value = "Provider 未返回任何模型";
      }
    }
  } catch (e) {
    fetchError.value = String(e?.message || e || "获取失败");
  } finally {
    fetchingModels.value = false;
  }
}

function toggleModel(id) {
  const s = new Set(selectedModels.value);
  s.has(id) ? s.delete(id) : s.add(id);
  selectedModels.value = s;
}

function toggleAll() {
  if (selectedModels.value.size === fetchedModels.value.length) {
    selectedModels.value = new Set();
  } else {
    selectedModels.value = new Set(fetchedModels.value);
  }
}

function handleSave() {
  const baseURL = form.baseURL.trim();
  const primaryKey = form.keys[0] || "";
  if (!baseURL || !primaryKey) return;

  const toAdd = fetchedModels.value.filter((m) => selectedModels.value.has(m));

  if (toAdd.length === 0 && fetchedModels.value.length > 0) return;

  const adapters = toAdd.length > 0
    ? toAdd.map((modelID) =>
        normalizeModelAdapter({
          ...createEmptyModelAdapter(),
          displayName: modelID,
          modelID,
          baseURL,
          apiKey: primaryKey,
          type: form.type,
        }),
      )
    : [normalizeModelAdapter({ ...createEmptyModelAdapter(), baseURL, apiKey: primaryKey, type: form.type })];

  // Attach provider-level multi-key info for the save handler.
  adapters.forEach((a) => { a._providerKeys = form.keys; a._providerName = form.name.trim(); });

  emit("save", adapters);
}

function handleCancel() {
  emit("cancel");
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-mask">
      <div
        v-show="visible"
        class="fixed inset-0 z-999 flex items-center justify-center bg-black/50 p-4"
        @click.self="handleCancel"
      >
        <Transition name="modal-content">
          <div
            v-show="visible"
            class="relative z-10 w-full max-w-[520px] overflow-hidden rounded-[8px] p-px shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            style="background: linear-gradient(to bottom, #656565 0%, #3A3A3A 10px, #3A3A3A 100%);"
            @click.stop
          >
            <div class="rounded-[7px] bg-[#292929] p-5">
              <h3 class="mb-4 text-base font-medium text-white">添加供应商</h3>

              <!-- Form -->
              <div class="flex flex-col gap-3">
                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">类型</span>
                  <Select v-model="form.type" :options="modelTypeOptions" />
                </label>

                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">baseURL</span>
                  <input
                    v-model="form.baseURL"
                    type="text"
                    placeholder="https://api.openai.com"
                    class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                  />
                </label>

                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">供应商名称 <span class="text-[#555]">（可选）</span></span>
                  <input
                    v-model="form.name"
                    type="text"
                    placeholder="自定义名称（留空则用域名）"
                    class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                  />
                </label>

                <!-- API Keys -->
                <div class="flex flex-col gap-1.5">
                  <span class="text-sm text-[#d4d4d4]">API Keys</span>
                  <!-- Existing keys -->
                  <div v-for="(k, i) in form.keys" :key="i" class="flex items-center gap-2">
                    <div class="flex h-9 flex-1 items-center rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3">
                      <span class="flex-1 truncate font-mono text-sm text-[#e5e5e5]">{{ maskSecret(k) }}</span>
                      <button type="button" class="ml-2 text-[#555] hover:text-[#f87171]" @click="removeKey(i)">
                        <span class="icon-[mdi--close] text-[14px]" />
                      </button>
                    </div>
                  </div>
                  <!-- Add key input -->
                  <div class="flex items-center gap-2">
                    <input
                      v-model="newKeyInput"
                      type="text"
                      placeholder="sk-..."
                      class="h-9 flex-1 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                      @keydown.enter.prevent="addKey"
                    />
                    <button
                      type="button"
                      class="center-row h-9 gap-1 rounded-[6px] border border-[#3f3f3f] bg-[#252525] px-3 text-sm text-[#a3a3a3] transition hover:border-[#10AD5D] hover:text-[#10AD5D]"
                      @click="addKey"
                    >
                      <span class="icon-[mdi--plus] text-[15px]" />
                    </button>
                  </div>
                </div>

                <!-- Fetch button -->
                <button
                  type="button"
                  :disabled="fetchingModels"
                  class="flex h-9 items-center justify-center gap-2 rounded-[6px] border border-[#10AD5D]/40 bg-[#10AD5D]/10 text-sm text-[#10AD5D] transition hover:bg-[#10AD5D]/20 disabled:opacity-50"
                  @click="handleFetchModels"
                >
                  <span
                    class="text-[15px]"
                    :class="fetchingModels ? 'icon-[mdi--loading] animate-spin' : 'icon-[mdi--cloud-download-outline]'"
                  />
                  {{ fetchingModels ? "获取中…" : "从 Provider 获取模型列表" }}
                </button>

                <!-- Error -->
                <p v-if="fetchError" class="text-xs text-[#f87171]">{{ fetchError }}</p>

                <!-- Model selector -->
                <div v-if="fetchedModels.length > 0" class="flex flex-col gap-2">
                  <div class="flex items-center justify-between">
                    <span class="text-xs text-[#a3a3a3]">
                      已选 {{ selectedModels.size }} / {{ fetchedModels.length }}
                    </span>
                    <button
                      type="button"
                      class="text-xs text-[#10AD5D] hover:underline"
                      @click="toggleAll"
                    >
                      {{ selectedModels.size === fetchedModels.length ? "取消全选" : "全选" }}
                    </button>
                  </div>
                  <div class="max-h-48 overflow-y-auto rounded-[6px] border border-[#3f3f3f] bg-[#1e1e1e]">
                    <label
                      v-for="m in fetchedModels"
                      :key="m"
                      class="flex cursor-pointer items-center gap-2.5 px-3 py-2 transition hover:bg-[#10AD5D]/10"
                    >
                      <input
                        type="checkbox"
                        :checked="selectedModels.has(m)"
                        class="size-3.5 accent-[#10AD5D]"
                        @change="toggleModel(m)"
                      />
                      <span class="truncate text-xs text-[#d4d4d4]">{{ m }}</span>
                    </label>
                  </div>
                </div>
              </div>

              <!-- Actions -->
              <div class="mt-5 flex justify-end gap-2">
                <Button variant="default" @click="handleCancel">取消</Button>
                <Button
                  variant="primary"
                  :disabled="!form.baseURL.trim() || form.keys.length === 0 || (fetchedModels.length > 0 && selectedModels.size === 0)"
                  @click="handleSave"
                >
                  {{ fetchedModels.length > 0 ? `添加 ${selectedModels.size} 个模型` : "添加供应商" }}
                </Button>
              </div>
            </div>
          </div>
        </Transition>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-mask-enter-active,
.modal-mask-leave-active {
  transition: opacity 0.2s ease;
}
.modal-mask-enter-from,
.modal-mask-leave-to {
  opacity: 0;
}
.modal-content-enter-active,
.modal-content-leave-active {
  transition: all 0.2s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.modal-content-enter-from,
.modal-content-leave-to {
  opacity: 0;
  transform: scale(0.92) translateY(-8px);
}
</style>
