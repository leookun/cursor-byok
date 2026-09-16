import { formatCompactInteger, formatInteger } from "../../../shared/utils/numberFormat";
import { useAppStore } from "../../../shared/store/appStore";
import { Icon } from "../../../shared/ui/Icon";
import { useTooltip, type TooltipAnchor } from "../../../shared/ui/Tooltip";
import { informationOutlineIcon } from "../../../shared/ui/icons";
import { CacheHitRateChart } from "./CacheHitRateChart";
import styles from "./HomeMetrics.module.scss";

export type HomeMetricsData = {
  llmCalls: number;
  successfulCalls: number;
  failedCalls: number;
  tokenUsage: number;
  promptTokens: number;
  cacheReadTokens: number;
  cacheWriteTokens: number;
};

function formatMetricValue(value: number) {
  const full = formatInteger(value);
  const compact = formatCompactInteger(value);
  return full === compact ? full : `${full} (${compact})`;
}

function formatRate(value: number | null) {
  return value === null ? t("暂无数据") : `${(Math.max(0, Math.min(1, value)) * 100).toFixed(2)}%`;
}

function calculateRate(numerator: number, denominator: number) {
  return denominator > 0 ? numerator / denominator : null;
}

function priceTokens(tokens: number, pricePerMillion: number) {
  return (tokens / 1_000_000) * pricePerMillion;
}

function formatUSD(value: number) {
  return `$${value.toFixed(2)}`;
}

function elementAnchor(element: HTMLElement): TooltipAnchor {
  return {
    contextElement: element,
    getBoundingClientRect: () => element.getBoundingClientRect(),
  };
}

function InfoTooltip({ content }: { content: string }) {
  const { show, hide } = useTooltip();

  return <button
    type="button"
    className={styles.info}
    aria-label={t("查看说明")}
    onMouseEnter={(event) => show(elementAnchor(event.currentTarget), undefined, <div className={styles.tooltipText}>{content}</div>)}
    onMouseLeave={hide}
    onFocus={(event) => show(elementAnchor(event.currentTarget), undefined, <div className={styles.tooltipText}>{content}</div>)}
    onBlur={hide}
  ><Icon icon={informationOutlineIcon} size="1.1em" /></button>;
}

export function HomeMetrics({ data, refreshVersion = 0 }: { data: HomeMetricsData; refreshVersion?: number }) {
  const { pricing } = useAppStore();
  const inputTokens = Math.max(0, data.promptTokens - data.cacheReadTokens - data.cacheWriteTokens);
  const outputTokens = Math.max(0, data.tokenUsage - data.promptTokens);
  const defaultCacheHitRate = calculateRate(data.cacheReadTokens, data.cacheReadTokens + inputTokens);
  const cacheReuseRate = calculateRate(
    data.cacheReadTokens,
    data.cacheReadTokens + data.cacheWriteTokens + inputTokens,
  );
  const successfulCallRate = calculateRate(data.successfulCalls, data.llmCalls);
  const costs = {
    input: priceTokens(inputTokens, pricing.input_per_million),
    output: priceTokens(outputTokens, pricing.output_per_million),
    cacheRead: priceTokens(data.cacheReadTokens, pricing.cache_read_per_million),
    cacheWrite: priceTokens(data.cacheWriteTokens, pricing.cache_write_per_million),
  };
  const totalCost = costs.input + costs.output + costs.cacheRead + costs.cacheWrite;
  const cacheCost = costs.cacheRead + costs.cacheWrite;
  const cacheTooltip = [
    t("当前：{rate}", { rate: formatRate(defaultCacheHitRate) }),
    t("公式：缓存读取 /（缓存读取 + 非缓存输入）"),
    t("默认 {defaultRate} / 计入创建 {reuseRate}", {
      defaultRate: formatRate(defaultCacheHitRate),
      reuseRate: formatRate(cacheReuseRate),
    }),
  ].join("\n");
  const callsTooltip = [
    t("按历史 LLM 调用记录汇总，进行中的调用不计入。"),
    "",
    t("总调用：{count}", { count: formatMetricValue(data.llmCalls) }),
    t("成功调用：{count}", { count: formatMetricValue(data.successfulCalls) }),
    t("异常调用：{count}", { count: formatMetricValue(data.failedCalls) }),
    t("成功占比：{rate}", { rate: formatRate(successfulCallRate) }),
  ].join("\n");
  const tokensTooltip = [
    t("总请求 Token 包含提示词和模型输出。"),
    "",
    t("总请求：{tokens}", { tokens: formatMetricValue(data.tokenUsage) }),
    t("提示词：{tokens}", { tokens: formatMetricValue(data.promptTokens) }),
    t("输出推算：{tokens}", { tokens: formatMetricValue(outputTokens) }),
    t("非缓存输入：{tokens}", { tokens: formatMetricValue(inputTokens) }),
    t("缓存读取：{tokens}", { tokens: formatMetricValue(data.cacheReadTokens) }),
    t("缓存写入：{tokens}", { tokens: formatMetricValue(data.cacheWriteTokens) }),
    "",
    t("缓存读写已计入提示词侧统计。"),
  ].join("\n");
  const costTooltip = [
    t("按配置的 Token 价格估算。"),
    t("缓存统计策略：默认口径（{rate}）", { rate: formatRate(defaultCacheHitRate) }),
    "",
    t("普通输入：{tokens} × ${price}/1M = {cost}", {
      tokens: formatMetricValue(inputTokens),
      price: pricing.input_per_million,
      cost: formatUSD(costs.input),
    }),
    t("模型输出：{tokens} × ${price}/1M = {cost}", {
      tokens: formatMetricValue(outputTokens),
      price: pricing.output_per_million,
      cost: formatUSD(costs.output),
    }),
    t("缓存读取：{tokens} × ${price}/1M = {cost}", {
      tokens: formatMetricValue(data.cacheReadTokens),
      price: pricing.cache_read_per_million,
      cost: formatUSD(costs.cacheRead),
    }),
    t("缓存写入：{tokens} × ${price}/1M = {cost}", {
      tokens: formatMetricValue(data.cacheWriteTokens),
      price: pricing.cache_write_per_million,
      cost: formatUSD(costs.cacheWrite),
    }),
    "",
    t("合计：{cost}", { cost: formatUSD(totalCost) }),
  ].join("\n");

  return <div className={styles.scroller}>
    <section className={styles.root} aria-label={t("调用统计")}>
      <article className={styles.metric}>
        <div className={styles.label}>{t("缓存命中率")}<InfoTooltip content={cacheTooltip} /></div>
        <CacheHitRateChart rate={defaultCacheHitRate ?? 0} animationKey={refreshVersion} />
      </article>
      <article className={styles.metric}>
        <div className={styles.label}>{t("LLM 调用")}<InfoTooltip content={callsTooltip} /></div>
        <div className={styles.body}>
          <div className={styles.value} title={formatInteger(data.llmCalls)}>{formatCompactInteger(data.llmCalls)}</div>
          <div className={styles.secondary}>{t("成功 {successful} / 异常 {failed}", {
            successful: formatCompactInteger(data.successfulCalls),
            failed: formatCompactInteger(data.failedCalls),
          })}</div>
        </div>
      </article>
      <article className={styles.metric}>
        <div className={styles.label}>{t("Token 消耗")}<InfoTooltip content={tokensTooltip} /></div>
        <div className={styles.body}>
          <div className={styles.value} title={formatInteger(data.tokenUsage)}>{formatCompactInteger(data.tokenUsage)}</div>
          <div className={styles.secondary}>{t("提示词 {tokens}", { tokens: formatCompactInteger(data.promptTokens) })}</div>
        </div>
      </article>
      <article className={styles.metric}>
        <div className={styles.label}>{t("价值估算")}<InfoTooltip content={costTooltip} /></div>
        <div className={styles.body}>
          <div className={styles.value} title={formatUSD(totalCost)}>{formatUSD(totalCost)}</div>
          <div className={styles.secondary}>{t("缓存读写 {cost}", { cost: formatUSD(cacheCost) })}</div>
        </div>
      </article>
    </section>
  </div>;
}
