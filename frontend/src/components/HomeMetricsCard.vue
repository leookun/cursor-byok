<script setup>
import CacheHitRateChart from "@/components/charts/CacheHitRateChart.vue";
import Switch from "@/components/ui/Switch.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { appState, saveIncludeCacheWriteInHitRate } from "@/state/appState";
import { formatCompactInteger, formatInteger } from "@/utils/numberFormat";
import { computed, ref } from "vue";

const emit = defineEmits(["refresh"]);

const TOKEN_PRICE_PER_MILLION = {
  input: 5,
  output: 25,
  cacheRead: 0.5,
  cacheWrite: 6.25,
};

const props = defineProps({
  metrics: {
    type: Object,
    required: true,
  },
  loading: {
    type: Boolean,
    default: false,
  },
  error: {
    type: String,
    default: "",
  },
});

const homeMetricsConfigSaving = ref(false);
const homeMetricsConfigError = ref("");

function normalizeNumber(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return 0;
  }
  return Math.round(number);
}

function formatMetricValue(value) {
  const full = formatInteger(value);
  const compact = formatCompactInteger(value);
  return full === compact ? full : `${full} (${compact})`;
}

function formatRateLabel(value) {
  const rate = Number(value);
  if (!Number.isFinite(rate)) {
    return "暂无数据";
  }
  return `${(Math.max(0, Math.min(1, rate)) * 100).toFixed(2)}%`;
}

function calculateRate(numerator, denominator) {
  const top = normalizeNumber(numerator);
  const bottom = normalizeNumber(denominator);
  if (bottom <= 0) {
    return null;
  }
  return top / bottom;
}

function priceTokens(tokens, pricePerMillion) {
  return (normalizeNumber(tokens) / 1_000_000) * pricePerMillion;
}

function formatUSD(value) {
  const amount = Number(value);
  if (!Number.isFinite(amount)) {
    return "$0.00";
  }
  if (amount > 0 && amount < 0.01) {
    return "<$0.01";
  }
  return `$${amount.toFixed(2)}`;
}

const cacheReadTokensTotal = computed(() => normalizeNumber(props.metrics?.cacheReadTokens));
const cacheWriteTokensTotal = computed(() => normalizeNumber(props.metrics?.cacheWriteTokens));

const inputTokensTotal = computed(() => {
  const promptTokensTotal = normalizeNumber(props.metrics?.promptTokensTotal);
  return Math.max(0, promptTokensTotal - cacheReadTokensTotal.value - cacheWriteTokensTotal.value);
});

const defaultCacheHitRate = computed(() =>
  calculateRate(cacheReadTokensTotal.value, cacheReadTokensTotal.value + inputTokensTotal.value),
);

const cacheReuseRate = computed(() =>
  calculateRate(
    cacheReadTokensTotal.value,
    cacheReadTokensTotal.value + cacheWriteTokensTotal.value + inputTokensTotal.value,
  ),
);

const includeCacheWriteInHitRate = computed(() => appState.includeCacheWriteInHitRate);

const selectedCacheHitRate = computed(() =>
  includeCacheWriteInHitRate.value ? cacheReuseRate.value : defaultCacheHitRate.value,
);

const selectedCacheRateModeLabel = computed(() =>
  includeCacheWriteInHitRate.value ? "计入缓存创建" : "默认口径",
);

const validTurnsRate = computed(() => {
  const turnsTotal = normalizeNumber(props.metrics?.turnsTotal);
  if (turnsTotal <= 0) {
    return null;
  }
  return normalizeNumber(props.metrics?.validTurnsTotal) / turnsTotal;
});

const completionTokensTotal = computed(() => {
  const requestTokensTotal = normalizeNumber(props.metrics?.requestTokensTotal);
  const promptTokensTotal = normalizeNumber(props.metrics?.promptTokensTotal);
  return Math.max(0, requestTokensTotal - promptTokensTotal);
});

const estimatedTokenCost = computed(() => {
  const input = priceTokens(inputTokensTotal.value, TOKEN_PRICE_PER_MILLION.input);
  const output = priceTokens(completionTokensTotal.value, TOKEN_PRICE_PER_MILLION.output);
  const cacheRead = priceTokens(cacheReadTokensTotal.value, TOKEN_PRICE_PER_MILLION.cacheRead);
  const cacheWrite = priceTokens(cacheWriteTokensTotal.value, TOKEN_PRICE_PER_MILLION.cacheWrite);
  return {
    input,
    output,
    cacheRead,
    cacheWrite,
    total: input + output + cacheRead + cacheWrite,
  };
});

const cacheTooltipContent = computed(() => {
  const formula = includeCacheWriteInHitRate.value
    ? "缓存读取 /（缓存读取 + 缓存创建 + 非缓存输入）"
    : "缓存读取 /（缓存读取 + 非缓存输入）";
  return [
    `当前：${formatRateLabel(selectedCacheHitRate.value)}`,
    `公式：${formula}`,
    `默认 ${formatRateLabel(defaultCacheHitRate.value)} / 计入创建 ${formatRateLabel(cacheReuseRate.value)}`,
  ].join("\n");
});

const turnsTooltipContent = computed(() =>
  [
    "按历史记录里扫描到的回合 summary 汇总。",
    "",
    `总轮次：${formatMetricValue(props.metrics?.turnsTotal)}`,
    `有效轮次：${formatMetricValue(props.metrics?.validTurnsTotal)}`,
    `异常轮次：${formatMetricValue(props.metrics?.invalidTurnsTotal)}`,
    `有效占比：${formatRateLabel(validTurnsRate.value)}`,
  ].join("\n"),
);

const tokensTooltipContent = computed(() =>
  [
    "总请求 Token 包含 Prompt 和模型输出。",
    "",
    `总请求：${formatMetricValue(props.metrics?.requestTokensTotal)}`,
    `Prompt：${formatMetricValue(props.metrics?.promptTokensTotal)}`,
    `输出推算：${formatMetricValue(completionTokensTotal.value)}`,
    `非缓存输入：${formatMetricValue(inputTokensTotal.value)}`,
    `缓存读取：${formatMetricValue(cacheReadTokensTotal.value)}`,
    `缓存写入：${formatMetricValue(cacheWriteTokensTotal.value)}`,
    "",
    "缓存读写已计入 Prompt 侧统计。",
  ].join("\n"),
);

const costTooltipContent = computed(() =>
  [
    "按 Claude Opus 4.7 价格估算。",
    `缓存统计策略：${selectedCacheRateModeLabel.value}（${formatRateLabel(selectedCacheHitRate.value)}）`,
    "",
    `普通输入：${formatMetricValue(inputTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.input}/1M = ${formatUSD(estimatedTokenCost.value.input)}`,
    `模型输出：${formatMetricValue(completionTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.output}/1M = ${formatUSD(estimatedTokenCost.value.output)}`,
    `缓存读取：${formatMetricValue(cacheReadTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.cacheRead}/1M = ${formatUSD(estimatedTokenCost.value.cacheRead)}`,
    `缓存写入：${formatMetricValue(cacheWriteTokensTotal.value)} × $${TOKEN_PRICE_PER_MILLION.cacheWrite}/1M = ${formatUSD(estimatedTokenCost.value.cacheWrite)}`,
    "",
    `合计：${formatUSD(estimatedTokenCost.value.total)}`,
  ].join("\n"),
);

async function toggleIncludeCacheWriteInHitRate(value) {
  const nextValue = Boolean(value);
  homeMetricsConfigSaving.value = true;
  homeMetricsConfigError.value = "";
  try {
    const result = await saveIncludeCacheWriteInHitRate(nextValue);
    if (!result?.ok) {
      homeMetricsConfigError.value = result?.error || "保存失败";
    }
  } catch (error) {
    homeMetricsConfigError.value = error?.message || "保存失败";
  } finally {
    homeMetricsConfigSaving.value = false;
  }
}

</script>

<template>
  <div>
    <div class="flex flex-col gap-4">
      <div class="flex items-center justify-between gap-4 h-[42px]">
        <div class="flex flex-col gap-1 w-[200px] shrink-0">
          <h2 class="text-[14px] font-medium text-white/80">会话统计</h2>
        </div>
        <div
          class="flex-1 center-row justify-end shrink-0 gap-2 text-xs text-[#6f6f6f] pr-4 w-[200px]"
        >
          <span>刷新统计</span>
          <button
            type="button"
            class="center-row justify-center h-[24px] w-[24px] rounded-[6px] border border-[#3b3b3b] bg-[#242424] text-[#9d9d9d] transition-colors duration-150 hover:border-[#4c4c4c] hover:text-white disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="loading"
            :title="loading ? '刷新中' : '刷新统计'"
            @click="emit('refresh')"
          >
            <span
              class="icon-[mdi--refresh] text-[14px]"
              :class="{ '!animate-spin': loading }"
            ></span>
          </button>
        </div>
      </div>

      <div
        class="mt-[-4px] grid grid-cols-4 gap-0 overflow-hidden rounded-[8px] border border-[#343434] bg-[#242424] h-[130px]"
      >
        <div class="min-w-0 px-4 py-4 flex flex-col justify-between">
          <div class="center-row justify-start gap-1 text-xs text-[#7f7f7f]">
            <span>缓存命中率</span>
            <Tooltip>
              <div class="w-[280px] space-y-3">
                <div class="border-b border-[#343434] pb-3">
                  <Switch
                    compact
                    label="计入缓存创建"
                    description="开启后把缓存创建纳入分母"
                    enabled-text="当前按复用率口径显示"
                    disabled-text="当前按默认命中率口径显示"
                    :enabled="includeCacheWriteInHitRate"
                    :busy="homeMetricsConfigSaving"
                    :disabled="homeMetricsConfigSaving"
                    @change="toggleIncludeCacheWriteInHitRate"
                  />
                </div>
                <div class="whitespace-pre-wrap">{{ cacheTooltipContent }}</div>
                <div v-if="homeMetricsConfigError" class="text-[11px] text-[#f87171]">
                  {{ homeMetricsConfigError }}
                </div>
              </div>
            </Tooltip>
          </div>
          <CacheHitRateChart :rate="selectedCacheHitRate" />
        </div>

        <div
          class="min-w-0 border-l border-[#343434] px-4 py-4 flex flex-col justify-between"
        >
          <div class="center-row justify-start gap-1 text-xs text-[#7f7f7f]">
            <span>对话轮次</span>
            <Tooltip :content="turnsTooltipContent" />
          </div>
          <div>
            <div
              class="text-[30px] leading-none text-white"
              style="font-family: var(--font-num)"
              :title="formatInteger(metrics.turnsTotal)"
            >
              {{ formatCompactInteger(metrics.turnsTotal) }}
            </div>
            <div class="mt-3 text-xs leading-5 text-[#8c8c8c]">
              有效
              <span :title="formatInteger(metrics.validTurnsTotal)">
                {{ formatCompactInteger(metrics.validTurnsTotal) }}
              </span>
              / 异常
              <span :title="formatInteger(metrics.invalidTurnsTotal)">
                {{ formatCompactInteger(metrics.invalidTurnsTotal) }}
              </span>
            </div>
          </div>
        </div>

        <div
          class="min-w-0 border-l border-[#343434] px-4 py-4 flex flex-col justify-between"
        >
          <div class="center-row justify-start gap-1 text-xs text-[#7f7f7f]">
            <span>Token 消耗</span>
            <Tooltip :content="tokensTooltipContent" />
          </div>
          <div>
            <div
              class="truncate text-[30px] leading-none text-white"
              style="font-family: var(--font-num)"
              :title="formatInteger(metrics.requestTokensTotal)"
            >
              {{ formatCompactInteger(metrics.requestTokensTotal) }}
            </div>
            <div class="mt-3 text-xs leading-5 text-[#8c8c8c]">
              Prompt
              <span :title="formatInteger(metrics.promptTokensTotal)">
                {{ formatCompactInteger(metrics.promptTokensTotal) }}
              </span>
            </div>
          </div>
        </div>

        <div
          class="min-w-0 border-l border-[#343434] px-4 py-4 flex flex-col justify-between"
        >
          <div class="center-row justify-start gap-1 text-xs text-[#7f7f7f]">
            <span>价值估算</span>
            <Tooltip :content="costTooltipContent" />
          </div>
          <div>
            <div
              class="truncate text-[30px] leading-none text-white"
              style="font-family: var(--font-num)"
              :title="formatUSD(estimatedTokenCost.total)"
            >
              {{ formatUSD(estimatedTokenCost.total) }}
            </div>
            <div class="mt-3 text-xs leading-5 text-[#8c8c8c]">
              缓存读写
              <span :title="formatUSD(estimatedTokenCost.cacheRead + estimatedTokenCost.cacheWrite)">
                {{ formatUSD(estimatedTokenCost.cacheRead + estimatedTokenCost.cacheWrite) }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped></style>
