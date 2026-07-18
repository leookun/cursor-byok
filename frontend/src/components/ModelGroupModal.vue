<script setup>
import Button from "@/components/ui/Button.vue";
import Input from "@/components/ui/Input.vue";
import Select from "@/components/ui/Select.vue";
import { buildModelGroupBaseURL } from "@/utils/modelAdapterGroups";
import { reactive, watch } from "vue";

const props = defineProps({
  visible: { type: Boolean, default: false },
  type: { type: String, default: "openai" },
  group: { type: Object, default: null },
  saving: { type: Boolean, default: false },
});

const emit = defineEmits(["cancel", "save"]);
const openAIEndpointOptions = [
  { label: "/v1/responses", value: "/v1/responses", icon: "icon-[mdi--api]" },
  { label: "/v1/chat/completions", value: "/v1/chat/completions", icon: "icon-[mdi--message-text-outline]" },
  { label: "自定义路径（请求地址填写完整接口）", value: "/custom", icon: "icon-[mdi--pencil-outline]" },
];

const draft = reactive({
  name: "",
  apiKey: "",
  address: "",
  openAIEndpoint: "/v1/responses",
  customHeadersEnabled: false,
  customHeadersJSON: "{}",
});
const errors = reactive({ form: "" });

function resetDraft() {
  const source = props.group && typeof props.group === "object" ? props.group : null;
  Object.assign(draft, {
    name: String(source?.name || ""),
    apiKey: String(source?.apiKey || ""),
    address: String(source?.baseURL || ""),
    openAIEndpoint: String(source?.openAIEndpoint || "/v1/responses"),
    customHeadersEnabled: Boolean(source?.customHeadersEnabled),
    customHeadersJSON: String(source?.customHeadersJSON || "{}"),
  });
  errors.form = "";
}

watch(() => props.visible, (visible) => {
  if (visible) {
    resetDraft();
  }
});

watch(() => props.group, () => {
  if (props.visible) {
    resetDraft();
  }
});

function handleSave() {
  const name = String(draft.name || "").trim();
  const apiKey = String(draft.apiKey || "").trim();
  if (!name) {
    errors.form = "分组名字不能为空";
    return;
  }
  if (!apiKey) {
    errors.form = "分组 Key 不能为空";
    return;
  }
  const endpoint = buildModelGroupBaseURL(draft.address);
  if (endpoint.error) {
    errors.form = endpoint.error;
    return;
  }
  const customHeadersJSON = String(draft.customHeadersJSON || "").trim();
  if (draft.customHeadersEnabled) {
    if (!customHeadersJSON) {
      errors.form = "自定义请求头 JSON 不能为空";
      return;
    }
    let headers;
    try {
      headers = JSON.parse(customHeadersJSON);
    } catch {
      errors.form = "自定义请求头必须是合法 JSON 对象";
      return;
    }
    if (!headers || typeof headers !== "object" || Array.isArray(headers)) {
      errors.form = "自定义请求头必须是 JSON 对象";
      return;
    }
    for (const [key, value] of Object.entries(headers)) {
      if (!String(key || "").trim()) {
        errors.form = "自定义请求头名称不能为空";
        return;
      }
      if (typeof value !== "string") {
        errors.form = `自定义请求头 ${key} 的值必须是字符串`;
        return;
      }
    }
  }
  errors.form = "";
  emit("save", {
    id: String(props.group?.groupID || props.group?.id || ""),
    name,
    type: props.type,
    baseURL: endpoint.baseURL,
    apiKey,
    openAIEndpoint: props.type === "openai" ? draft.openAIEndpoint : "",
    customHeadersEnabled: draft.customHeadersEnabled,
    customHeadersJSON: draft.customHeadersEnabled ? customHeadersJSON : "{}",
  });
}
</script>

<template>
  <Teleport to="body">
    <Transition name="modal-mask">
      <div
        v-show="visible"
        class="fixed inset-0 z-999 flex items-center justify-center bg-black/50 p-4"
        @click.self="!saving && emit('cancel')"
      >
        <Transition name="modal-content">
          <div
            v-show="visible"
            class="relative z-10 w-full max-w-[520px] overflow-hidden rounded-[8px] p-px shadow-[0_25px_50px_-12px_rgba(0,0,0,0.6)]"
            style="background: linear-gradient(to bottom, #656565 0%, #3A3A3A 10px, #3A3A3A 100%);"
            @click.stop
          >
            <div class="max-h-[90vh] overflow-y-auto rounded-[7px] bg-[#292929] p-5">
              <h3 class="mb-4 text-base font-medium text-white">
                {{ group ? "编辑" : "添加" }} {{ type === "anthropic" ? "Anthropic" : "OpenAI" }} 分组
              </h3>

              <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">分组名字</span>
                  <Input v-model="draft.name" :disabled="saving" placeholder="例如：生产环境" />
                </label>
                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">分组 Key</span>
                  <Input
                    v-model="draft.apiKey"
                    type="password"
                    allow-visibility-toggle
                    :disabled="saving"
                    placeholder="上游 API Key"
                  />
                </label>
              </div>

              <div class="mt-3">
                <label class="flex flex-col gap-1">
                  <span class="text-sm text-[#d4d4d4]">请求地址</span>
                  <Input v-model="draft.address" :disabled="saving" placeholder="https://api.example.com/v1" />
                </label>
              </div>

              <label class="mt-3 flex flex-col gap-1">
                <span class="text-sm text-[#d4d4d4]">接口请求</span>
                <Select
                  v-if="type === 'openai'"
                  v-model="draft.openAIEndpoint"
                  :options="openAIEndpointOptions"
                  :disabled="saving"
                />
                <div
                  v-else
                  class="flex h-9 items-center rounded-[6px] border border-[#3f3f3f] bg-[#232323] px-3 text-sm text-[#a3a3a3]"
                >
                  /v1/messages
                </div>
              </label>

              <div class="mt-3 rounded-[8px] border border-[#3a3a3a] bg-[#252525] p-3">
                <div class="flex items-center justify-between gap-3">
                  <div>
                    <div class="text-sm text-[#d4d4d4]">自定义请求头 JSON</div>
                    <div class="mt-1 text-xs text-[#737373]">请求模型列表和调用模型时都会携带，字段值必须是字符串。</div>
                  </div>
                  <label class="flex shrink-0 items-center gap-2 text-xs text-[#d4d4d4]">
                    <input
                      v-model="draft.customHeadersEnabled"
                      type="checkbox"
                      class="size-4 accent-[#10AD5D]"
                      :disabled="saving"
                    />
                    <span>启用</span>
                  </label>
                </div>
                <textarea
                  v-if="draft.customHeadersEnabled"
                  v-model="draft.customHeadersJSON"
                  rows="5"
                  spellcheck="false"
                  :disabled="saving"
                  placeholder='{"X-API-Key":"your-key","X-Tenant":"tenant-a"}'
                  class="mt-3 min-h-[120px] w-full resize-y rounded-[6px] border border-[#3f3f3f] bg-[#1f1f1f] px-3 py-2 font-mono text-xs text-[#e5e5e5] outline-none focus:border-[#10AD5D] disabled:cursor-not-allowed disabled:opacity-60"
                />
              </div>

              <div
                v-if="errors.form"
                class="mt-4 rounded-[8px] border border-[#4b1d1d] bg-[#2a1313] px-3 py-2 text-sm text-[#fca5a5]"
              >
                {{ errors.form }}
              </div>

              <div class="mt-5 flex justify-end gap-2">
                <Button variant="default" :disabled="saving" @click="emit('cancel')">取消</Button>
                <Button variant="primary" :disabled="saving" @click="handleSave">
                  {{ saving ? "保存中..." : (group ? "保存修改" : "保存分组") }}
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
  transition: opacity 0.25s ease, backdrop-filter 0.25s ease;
}
.modal-mask-enter-from,
.modal-mask-leave-to {
  opacity: 0;
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
