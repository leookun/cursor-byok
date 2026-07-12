<script setup>
import Button from "@/components/ui/Button.vue";
import Select from "@/components/ui/Select.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import {
  ANTHROPIC_THINKING_EFFORT_DEFAULT,
  createEmptyModelAdapter,
  normalizeModelAdapter,
  OPENAI_ENDPOINT_CHAT_COMPLETIONS,
  OPENAI_ENDPOINT_CUSTOM,
  OPENAI_ENDPOINT_RESPONSES,
  OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
} from "@/state/appState";
import { computed, reactive, watch } from "vue";

const modelTypeOptions = [
  { label: "openai", value: "openai", icon: "icon-[bxl--openai]" },
  { label: "anthropic", value: "anthropic", icon: "icon-[logos--claude-icon]" },
];

const reasoningEffortOptions = [
  { label: "低", value: "low", icon: "icon-[mdi--head-outline]" },
  { label: "中", value: "medium", icon: "icon-[mdi--head-lightbulb-outline]" },
  { label: "高", value: "high", icon: "icon-[mdi--brain]" },
  { label: "极高", value: "xhigh", icon: "icon-[mdi--head-cog-outline]" },
];

const anthropicThinkingEffortOptions = [
  { label: "低", value: "low", icon: "icon-[mdi--head-outline]" },
  { label: "中", value: "medium", icon: "icon-[mdi--head-lightbulb-outline]" },
  { label: "高", value: "high", icon: "icon-[mdi--brain]" },
  { label: "极高", value: "xhigh", icon: "icon-[mdi--head-cog-outline]" },
  { label: "最大", value: "max", icon: "icon-[mdi--brain]" },
];

const openAIEndpointOptions = [
  { label: "/v1/responses", value: OPENAI_ENDPOINT_RESPONSES, icon: "icon-[mdi--api]" },
  { label: "/v1/chat/completions", value: OPENAI_ENDPOINT_CHAT_COMPLETIONS, icon: "icon-[mdi--message-text-outline]" },
  { label: "自定义路径", value: OPENAI_ENDPOINT_CUSTOM, icon: "icon-[mdi--pencil-outline]" },
];

const fieldTips = {
  openAIExtraParams: "开启后会把 JSON 对象覆盖到 OpenAI 请求体。同名字段以这里为准。OpenAI service_tier 支持 auto、default、flex、scale、priority；priority 可用于高优先级/Fast 类场景。",
};

const props = defineProps({
  visible: { type: Boolean, default: false },
  title: { type: String, default: "模型配置" },
  adapter: {
    type: Object,
    default: () => createEmptyModelAdapter(),
  },
  errorMessage: { type: String, default: "" },
});

const emit = defineEmits(["cancel", "save"]);

const draft = reactive(createEmptyModelAdapter());

function createOptionalPositiveIntegerModel(key) {
  return computed({
    get() {
      return draft[key] > 0 ? String(draft[key]) : "";
    },
    set(value) {
      const text = String(value || "").trim();
      draft[key] = /^\d+$/.test(text) && Number(text) > 0 ? Number(text) : 0;
    },
  });
}

const maxCompletionTokensInput = createOptionalPositiveIntegerModel("maxCompletionTokens");
const anthropicMaxTokensInput = createOptionalPositiveIntegerModel("anthropicMaxTokens");
const contextWindowTokensInput = createOptionalPositiveIntegerModel("contextWindowTokens");

function ensureOpenAIExtraParamsJSON() {
  if (!String(draft.openAIExtraParamsJSON || "").trim()) {
    draft.openAIExtraParamsJSON = OPENAI_EXTRA_PARAMS_DEFAULT_JSON;
  }
}

function ensureAnthropicThinkingEffort() {
  if (!String(draft.anthropicThinkingEffort || "").trim()) {
    draft.anthropicThinkingEffort = ANTHROPIC_THINKING_EFFORT_DEFAULT;
  }
}

function syncDraft() {
  Object.assign(draft, normalizeModelAdapter(props.adapter));
  if (!draft.type) {
    draft.type = "openai";
  }
}

watch(() => props.visible, (visible) => {
  if (visible) {
    syncDraft();
  }
}, { immediate: true });

watch(() => props.adapter, () => {
  if (props.visible) {
    syncDraft();
  }
});

watch(() => draft.type, (type) => {
  if (type === "openai" && !draft.openAIEndpoint) {
    draft.openAIEndpoint = OPENAI_ENDPOINT_RESPONSES;
  } else if (type === "anthropic") {
    ensureAnthropicThinkingEffort();
  }
});

watch(() => draft.openAIExtraParamsEnabled, (enabled) => {
  if (enabled) {
    ensureOpenAIExtraParamsJSON();
  }
});

function handleCancel() {
  emit("cancel");
}

function handleSave() {
  emit("save", normalizeModelAdapter(draft));
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
            class="relative z-10 w-full max-w-[560px] overflow-hidden rounded-[8px] p-px shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            style="background: linear-gradient(to bottom, #656565 0%, #3A3A3A 10px, #3A3A3A 100%);"
            @click.stop
          >
            <div class="rounded-[7px] bg-[#292929] p-5">
              <h3 class="mb-4 text-base font-medium text-white">{{ title }}</h3>

              <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">显示名称</span>
                  <input
                    v-model="draft.displayName"
                    type="text"
                    class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                  />
                </label>

                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">ModelID</span>
                  <input
                    v-model="draft.modelID"
                    type="text"
                    class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                  />
                </label>

                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">类型</span>
                  <Select
                    v-model="draft.type"
                    :options="modelTypeOptions"
                  />
                </label>

                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">API Key</span>
                  <input
                    v-model="draft.apiKey"
                    type="text"
                    class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                  />
                </label>
              </div>

              <label class="mt-3 flex flex-col gap-1">
                <span class="text-sm text-[#d4d4d4]">baseURL</span>
                <input
                  v-model="draft.baseURL"
                  type="text"
                  class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                />
              </label>

              <label class="mt-3 flex flex-col gap-1">
                <span class="text-sm text-[#d4d4d4]">context_window_tokens</span>
                <input
                  v-model="contextWindowTokensInput"
                  type="text"
                  inputmode="numeric"
                  placeholder="留空时默认 200000"
                  class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                />
              </label>

              <div v-if="draft.type === 'openai'" class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">reasoning_effort</span>
                  <Select
                    v-model="draft.reasoningEffort"
                    :options="reasoningEffortOptions"
                  />
                </label>

                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">max token</span>
                  <input
                    v-model="maxCompletionTokensInput"
                    type="text"
                    inputmode="numeric"
                    placeholder="留空时默认 65536"
                    class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                  />
                </label>

                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">endpoint</span>
                  <Select
                    v-model="draft.openAIEndpoint"
                    :options="openAIEndpointOptions"
                  />
                </label>
              </div>

              <div v-if="draft.type === 'openai'" class="mt-3 rounded-[8px] border border-[#343434] bg-[#252525] p-3">
                <div class="flex items-center justify-between gap-3">
                  <span class="flex items-center gap-1.5 text-sm text-[#d4d4d4]">
                    <Tooltip :content="fieldTips.openAIExtraParams" />
                    <span>额外参数 JSON</span>
                  </span>
                  <label class="flex items-center gap-2 text-xs text-[#d4d4d4]">
                    <input
                      v-model="draft.openAIExtraParamsEnabled"
                      type="checkbox"
                      class="size-4 accent-[#10AD5D]"
                    />
                    <span>启用</span>
                  </label>
                </div>
                <textarea
                  v-if="draft.openAIExtraParamsEnabled"
                  v-model="draft.openAIExtraParamsJSON"
                  rows="5"
                  spellcheck="false"
                  class="mt-3 min-h-[120px] w-full resize-none rounded-[6px] border border-[#3f3f3f] bg-[#1f1f1f] px-3 py-2 font-mono text-xs text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                />
              </div>

              <div v-if="draft.type === 'anthropic'" class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">max_tokens</span>
                  <input
                    v-model="anthropicMaxTokensInput"
                    type="text"
                    inputmode="numeric"
                    placeholder="留空时默认 65536"
                    class="h-9 rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                  />
                </label>

                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">thinking effort</span>
                  <Select
                    v-model="draft.anthropicThinkingEffort"
                    :options="anthropicThinkingEffortOptions"
                  />
                </label>
              </div>

              <label class="mt-3 flex flex-col gap-1">
                <span class="text-sm text-[#d4d4d4]">tooltipData</span>
                <textarea
                  v-model="draft.tooltipData"
                  rows="5"
                  class="min-h-[120px] resize-none rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 py-2 text-sm text-[#e5e5e5] outline-none focus:border-[#10AD5D]"
                />
              </label>

              <div
                v-if="errorMessage"
                class="mt-4 rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]"
              >
                {{ errorMessage }}
              </div>

              <div class="mt-5 flex justify-end gap-2">
                <Button variant="default" @click="handleCancel">取消</Button>
                <Button variant="primary" @click="handleSave">保存</Button>
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
  transition: opacity 0.25s ease, backdrop-filter 0.25s ease;
}
.modal-mask-enter-from,
.modal-mask-leave-to {
  opacity: 0;
  backdrop-filter: blur(0);
}

.modal-content-enter-active,
.modal-content-leave-active {
  transition: all 0.25s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.modal-content-enter-from,
.modal-content-leave-to {
  opacity: 0;
  transform: scale(0.9) translateY(-10px);
}
</style>
