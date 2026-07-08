<script setup>
import CacheHitRateChart from "@/components/charts/CacheHitRateChart.vue";
import Switch from "@/components/ui/Switch.vue";
import Tooltip from "@/components/ui/Tooltip.vue";
import { localized } from "@/i18n/runtime";
import { appState, saveIncludeCacheWriteInHitRate } from "@/state/appState";
import { formatCompactInteger, formatInteger } from "@/utils/numberFormat";
import { computed, ref } from "vue";

const emit = defineEmits(["refresh", "open-ad"]);

const L = {
  noData: localized("497c85690c4cc0fc", "No data"),
  sessionStats: localized("a1b2c3d4e5f60001", "Session Statistics"),
  refreshStats: localized("a1b2c3d4e5f60002", "Refresh Stats"),
  cacheHitRate: localized("a1b2c3d4e5f60003", "Cache Hit Rate"),
  conversationTurns: localized("a1b2c3d4e5f60004", "Conversation Turns"),
  tokenUsage: localized("a1b2c3d4e5f60005", "Token Usage"),
  costEstimate: localized("a1b2c3d4e5f60006", "Cost Estimate"),
  cacheRW: localized("a1b2c3d4e5f60007", "Cache R/W"),
  valid: localized("a1b2c3d4e5f60008", "Valid"),
  invalid: localized("a1b2c3d4e5f60009", "/ Invalid"),
  includeCacheWrites: localized("a1b2c3d4e5f6000a", "Include cache writes"),
  includeCacheWritesDesc: localized("a1b2c3d4e5f6000b", "Include cache writes in denominator"),
  reuseRateMode: localized("a1b2c3d4e5f6000c", "Currently showing by reuse rate"),
  defaultRateMode: localized("a1b2c3d4e5f6000d", "Currently showing by default hit rate"),
  current: localized("a1b2c3d4e5f6000e", "Current: {0}"),
  formula: localized("a1b2c3d4e5f6000f", "Formula: {0}"),
  defaultWithWrites: localized("a1b2c3d4e5f60010", "Default {0} / With writes {1}"),
  turnsSummary: localized("a1b2c3d4e5f60011", "Aggregated from turn summaries scanned from history."),
  totalTurns: localized("a1b2c3d4e5f60012", "Total turns: {0}"),
  validTurns: localized("a1b2c3d4e5f60013", "Valid turns: {0}"),
  invalidTurns: localized("a1b2c3d4e5f60014", "Invalid turns: {0}"),
  validRatio: localized("a1b2c3d4e5f60015", "Valid ratio: {0}"),
  tokensSummary: localized("a1b2c3d4e5f60016", "Total request tokens include both prompt and model output."),
  totalRequests: localized("a1b2c3d4e5f60017", "Total requests: {0}"),
  promptLabel: localized("a1b2c3d4e5f60018", "Prompt: {0}"),
  outputEst: localized("a1b2c3d4e5f60019", "Output (estimated): {0}"),
  nonCacheInput: localized("a1b2c3d4e5f6001a", "Non-cache input: {0}"),
  cacheReads: localized("a1b2c3d4e5f6001b", "Cache reads: {0}"),
  cacheWrites: localized("a1b2c3d4e5f6001c", "Cache writes: {0}"),
  cacheNote: localized("a1b2c3d4e5f6001d", "Cache read/write included in Prompt stats."),
  costSummary: localized("a1b2c3d4e5f6001e", "Estimated based on Claude Opus 4.7 pricing."),
  cacheStats: localized("a1b2c3d4e5f6001f", "Cache stats: {0} ({1})"),
  regularInput: localized("a1b2c3d4e5f60020", "Regular input: {0}"),
  modelOutput: localized("a1b2c3d4e5f60021", "Model output: {0}"),
  cacheRead: localized("a1b2c3d4e5f60022", "Cache reads: {0}"),
  cacheWrite: localized("a1b2c3d4e5f60023", "Cache writes: {0}"),
  total: localized("a1b2c3d4e5f60024", "Total: {0}"),
  saveFailed: localized("6309a3bb5ba4c714", "Save failed"),
  refreshing: localized("3af7e5489e61ea51", "Refreshing"),
};

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
  homeAd: {
    type: Object,
    default: null,
  },
  homeAds: {
    type: Array,
    default: () => [],
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
    return L.noData.toString();
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
  includeCacheWriteInHitRate.value ? L.includeCacheWrites.toString() : L.defaultRateMode.toString(),
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
  const formulaText = includeCacheWriteInHitRate.value
    ? "Cache reads / (Cache reads + Cache writes + Non-cache input)"
    : "Cache reads / (Cache reads + Non-cache input)";
  return [
    L.current.toString(formatRateLabel(selectedCacheHitRate.value)),
    L.formula.toString(formulaText),
    L.defaultWithWrites.toString(formatRateLabel(defaultCacheHitRate.value), formatRateLabel(cacheReuseRate.value)),
  ].join("\n");
});

const turnsTooltipContent = computed(() =>
  [
    L.turnsSummary.toString(),
    "",
    L.totalTurns.toString(formatMetricValue(props.metrics?.turnsTotal)),
    L.validTurns.toString(formatMetricValue(props.metrics?.validTurnsTotal)),
    L.invalidTurns.toString(formatMetricValue(props.metrics?.invalidTurnsTotal)),
    L.validRatio.toString(formatRateLabel(validTurnsRate.value)),
  ].join("\n"),
);

const tokensTooltipContent = computed(() =>
  [
    L.tokensSummary.toString(),
    "",
    L.totalRequests.toString(formatMetricValue(props.metrics?.requestTokensTotal)),
    L.promptLabel.toString(formatMetricValue(props.metrics?.promptTokensTotal)),
    L.outputEst.toString(formatMetricValue(completionTokensTotal.value)),
    L.nonCacheInput.toString(formatMetricValue(inputTokensTotal.value)),
    L.cacheReads.toString(formatMetricValue(cacheReadTokensTotal.value)),
    L.cacheWrites.toString(formatMetricValue(cacheWriteTokensTotal.value)),
    "",
    L.cacheNote.toString(),
  ].join("\n"),
);

const costTooltipContent = computed(() =>
  [
    L.costSummary.toString(),
    L.cacheStats.toString(selectedCacheRateModeLabel.value, formatRateLabel(selectedCacheHitRate.value)),
    "",
    `${L.regularInput.toString(formatMetricValue(inputTokensTotal.value))} × $${TOKEN_PRICE_PER_MILLION.input}/1M = ${formatUSD(estimatedTokenCost.value.input)}`,
    `${L.modelOutput.toString(formatMetricValue(completionTokensTotal.value))} × $${TOKEN_PRICE_PER_MILLION.output}/1M = ${formatUSD(estimatedTokenCost.value.output)}`,
    `${L.cacheRead.toString(formatMetricValue(cacheReadTokensTotal.value))} × $${TOKEN_PRICE_PER_MILLION.cacheRead}/1M = ${formatUSD(estimatedTokenCost.value.cacheRead)}`,
    `${L.cacheWrite.toString(formatMetricValue(cacheWriteTokensTotal.value))} × $${TOKEN_PRICE_PER_MILLION.cacheWrite}/1M = ${formatUSD(estimatedTokenCost.value.cacheWrite)}`,
    "",
    L.total.toString(formatUSD(estimatedTokenCost.value.total)),
  ].join("\n"),
);

function normalizeHomeAd(item, index) {
  const source = item && typeof item === "object" ? item : {};
  const title = typeof source.title === "string" ? source.title.trim() : "";
  if (!title) {
    return null;
  }
  return {
    id: typeof source.id === "string" && source.id.trim() ? source.id.trim() : String(index + 1),
    title,
    subtitle: typeof source.subtitle === "string" ? source.subtitle.trim() : "",
  };
}

async function toggleIncludeCacheWriteInHitRate(value) {
  const nextValue = Boolean(value);
  homeMetricsConfigSaving.value = true;
  homeMetricsConfigError.value = "";
  try {
    const result = await saveIncludeCacheWriteInHitRate(nextValue);
    if (!result?.ok) {
      homeMetricsConfigError.value = result?.error || L.saveFailed.toString();
    }
  } catch (error) {
    homeMetricsConfigError.value = error?.message || L.saveFailed.toString();
  } finally {
    homeMetricsConfigSaving.value = false;
  }
}

const normalizedHomeAds = computed(() => {
  const list = Array.isArray(props.homeAds) && props.homeAds.length > 0 ? props.homeAds : [props.homeAd];
  return list.map(normalizeHomeAd).filter(Boolean);
});

const hasHomeAd = computed(() => normalizedHomeAds.value.length > 0);
</script>

<template>
  <div>
    <div class="flex flex-col gap-4">
      <div class="flex items-center justify-between gap-4 h-[42px]">
        <div v-if="!hasHomeAd" class="flex flex-col gap-1 w-[200px] shrink-0">
          <h2 class="text-[14px] font-medium text-white/80">{{ L.sessionStats }}</h2>
        </div>
        <div v-else class="grid min-w-0  grid-cols-3 gap-2 shrink-0">
          <div
            v-for="ad in normalizedHomeAds"
            :key="ad.id"
            style="font-family: var(--font-num)"
            class="center-row h-[42px] min-w-0 cursor-pointer gap-[8px] rounded-[6px] border border-[#343434] bg-[#242424] px-[8px] pr-[10px] text-left transition-colors duration-150 hover:border-[#4a4a4a] hover:bg-[#2a2a2a] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-amber-400/50"
            role="button"
            tabindex="0"
            :title="ad.subtitle ? `${ad.title}\n${ad.subtitle}` : ad.title"
            @click="emit('open-ad', ad.id)"
            @keydown.enter.prevent="emit('open-ad', ad.id)"
            @keydown.space.prevent="emit('open-ad', ad.id)"
          >
            <div
              class="center-row h-[20px] w-[20px] shrink-0 justify-center text-[20px] text-amber-400"
            >
              <span class="icon-[cil--badge]"></span>
            </div>
            <div class="min-w-0 flex-1">
              <div class="truncate text-[13px] font-medium leading-[16px] text-white">
                {{ ad.title }}
              </div>
              <div
                v-if="ad.subtitle"
                class="mt-[2px] center-row min-w-0 gap-[2px] text-[11px] leading-[12px] text-[#8A8A8A]"
              >
                <span class="truncate">{{ ad.subtitle }}</span>
              </div>
            </div>
          </div>
        </div>
        <div
          class="flex-1 center-row justify-end shrink-0 gap-2 text-xs text-[#6f6f6f] pr-4 w-[200px]"
        >
          <span>{{ L.refreshStats }}</span>
          <button
            type="button"
            class="center-row justify-center h-[24px] w-[24px] rounded-[6px] border border-[#3b3b3b] bg-[#242424] text-[#9d9d9d] transition-colors duration-150 hover:border-[#4c4c4c] hover:text-white disabled:cursor-not-allowed disabled:opacity-60"
            :disabled="loading"
            :title="loading ? L.refreshing.toString() : L.refreshStats.toString()"
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
            <span>{{ L.cacheHitRate }}</span>
            <Tooltip>
              <div class="w-[280px] space-y-3">
                <div class="border-b border-[#343434] pb-3">
                  <Switch
                    compact
                    :label="L.includeCacheWrites"
                    :description="L.includeCacheWritesDesc"
                    :enabled-text="L.reuseRateMode"
                    :disabled-text="L.defaultRateMode"
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
            <span>{{ L.conversationTurns }}</span>
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
              {{ L.valid }}
              <span :title="formatInteger(metrics.validTurnsTotal)">
                {{ formatCompactInteger(metrics.validTurnsTotal) }}
              </span>
              {{ L.invalid }}
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
            <span>{{ L.tokenUsage }}</span>
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
            <span>{{ L.costEstimate }}</span>
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
              {{ L.cacheRW }}
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
