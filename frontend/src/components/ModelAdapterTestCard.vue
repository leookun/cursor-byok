<script setup>
import { computed } from "vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { formatDuration } from "@/state/appState";

const props = defineProps({
  result: {
    type: Object,
    default: null,
  },
  stale: {
    type: Boolean,
    default: false,
  },
  compact: {
    type: Boolean,
    default: false,
  },
  showMetrics: {
    type: Boolean,
    default: false,
  },
  title: {
    type: String,
    default: "模型测试",
  },
  emptyText: {
    type: String,
    default: "尚未测试",
  },
});

const normalizedStatus = computed(() => {
  const status = String(props.result?.status || "").trim().toLowerCase();
  return ["running", "success", "error"].includes(status) ? status : "idle";
});

const summaryText = computed(() => {
  const text = String(props.result?.summaryText || "").trim();
  if (text) {
    return text;
  }
  if (normalizedStatus.value === "running") {
    return "测试中...";
  }
  if (normalizedStatus.value === "error") {
    return "测试失败,请查看原始信息";
  }
  return props.emptyText;
});

const rawResponseText = computed(() => {
  const raw = String(props.result?.rawResponse || "").trim();
  if (raw) {
    return raw;
  }
  if (normalizedStatus.value === "error") {
    return String(props.result?.error || "").trim();
  }
  return "";
});

const panelClass = computed(() => {
  if (props.stale) {
    return "border-[#6b5b1e] bg-[#2c2612]";
  }
  if (normalizedStatus.value === "running") {
    return "border-[#164e63] bg-[#0b2530]";
  }
  if (normalizedStatus.value === "error") {
    return "border-[#4b1d1d] bg-[#2a1313]";
  }
  if (normalizedStatus.value === "success" && props.result?.tokensEstimated) {
    return "border-[#5a4314] bg-[#2f2612]";
  }
  if (normalizedStatus.value === "success") {
    return "border-[#14532d] bg-[#102418]";
  }
  return "border-[#343434] bg-[#232323]";
});

const summaryClass = computed(() => {
  if (props.stale) {
    return "text-[#f6d77a]";
  }
  if (normalizedStatus.value === "running") {
    return "text-[#67e8f9]";
  }
  if (normalizedStatus.value === "error") {
    return "text-[#fca5a5]";
  }
  if (normalizedStatus.value === "success" && props.result?.tokensEstimated) {
    return "text-[#fcd34d]";
  }
  if (normalizedStatus.value === "success") {
    return "text-[#86efac]";
  }
  return "text-[#a3a3a3]";
});
</script>

<template>
  <div class="rounded-[8px] border-none px-3 py-3" :class="panelClass">
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-1.5">
          <div
            :class="compact ? 'text-[11px] uppercase tracking-[0.08em] text-white/80' : 'text-[11px] uppercase tracking-[0.08em] text-white/80'"
          >
            {{ title }}
          </div>
          <div v-if="rawResponseText" class="center-row gap-1 text-[11px] text-white/80">
            <span>原始返回</span>
            <Tooltip :content="rawResponseText" copyable />
          </div>
        </div>
        <div class="mt-1 text-sm leading-relaxed" :class="summaryClass">
          {{ summaryText }}
        </div>
      </div>

    </div>

    <div v-if="stale" class="mt-2 text-xs text-[#f6d77a]">
      配置已变更，请重新测试
    </div>

    <div
      v-if="showMetrics && normalizedStatus === 'success'"
      class="mt-3 grid grid-cols-1 gap-2 md:grid-cols-2"
    >
      <div class="rounded-[8px] bg-black/20 px-3 py-2">
        <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">总耗时</div>
        <div class="mt-1 text-sm text-white/80">{{ formatDuration(result?.totalDurationMS) }}</div>
      </div>
      <div class="rounded-[8px] bg-black/20 px-3 py-2">
        <div class="text-[11px] uppercase tracking-[0.08em] text-[#666]">输出 Token</div>
        <div class="mt-1 text-sm text-white/80">{{ result?.outputTokens ?? 0 }}</div>
      </div>
    </div>

    <div
      v-if="normalizedStatus === 'success' && result?.tokensEstimated"
      class="mt-2 text-xs text-white/80"
    >
      输出 Token 为估算值
    </div>
  </div>
</template>
