import type { CallDetail } from "../../shared/api";
import { JsonEditor } from "../../shared/ui/JsonEditor";
import { Tabs, type TabItem } from "../../shared/ui/Tabs";
import styles from "./CallDetails.module.scss";

const show = (value: string | number | null) => value ?? "-";
const timing = (value: number | null) => value == null ? "-" : `${value} ms`;

export function CallDetails({ detail }: { detail: CallDetail }) {
  const { call, request, response_chunks: chunks, cursor_trace: cursorTrace } = detail;
  const responseBody = chunks.map((chunk) => chunk.data).join("");
  const responseBytes = chunks.reduce((total, chunk) => total + chunk.byte_count, 0);
  const fields: Array<[string, string | number]> = [
    ["Call ID", call.call_id],
    [t("调用类型"), call.call_kind === "cursor_official" ? t("Cursor 官方") : "LLM"],
    [t("路由"), call.route === "cursor_official" ? t("Cursor 官方") : "BYOK"],
    ["Run ID", call.run_id],
    ["Conversation ID", call.conversation_id],
    [t("上游调用序号"), call.provider_call_index],
    ["Model Hash", show(call.model_hash)],
    [t("别名 ID"), show(call.alias_id)],
    [t("别名名称"), show(call.alias_name)],
    [t("别名目标 ID"), show(call.alias_target_id)],
    [t("别名切换次数"), call.alias_switch_count],
    [t("上游类型"), call.provider_type],
    [t("上游地址"), call.provider_url],
    [t("最终请求类型"), call.request_type],
    [t("最终请求地址"), call.request_url],
    ["Model ID", call.model_id],
    [t("显示名称"), call.display_name],
    [t("思考强度"), show(call.reasoning_effort)],
    ["Fast", call.fast == null ? "-" : call.fast ? t("是") : t("否")],
    [t("状态"), call.status],
    ["Finish Reason", show(call.finish_reason)],
    ["HTTP Status", show(call.http_status)],
    ["Created At", `${call.created_at_ms} · ${new Date(call.created_at_ms).toLocaleString()}`],
    [t("耗时"), timing(call.duration_ms)],
    ["TTFB", timing(call.ttfb_ms)],
    ["TTFR", timing(call.ttfr_ms)],
    ["TTFT", timing(call.ttft_ms)],
    ["Input Token", show(call.input_tokens)],
    ["Output Token", show(call.output_tokens)],
    ["Total Token", show(call.total_tokens)],
    ["Cache Read Token", show(call.cache_read_tokens)],
    ["Cache Write Token", show(call.cache_write_tokens)],
    ["Reasoning Token", show(call.reasoning_tokens)],
    [t("消息数"), call.message_count],
    [t("工具数"), call.tool_count],
    [t("详细记录"), call.detailed ? t("是") : t("否")],
    ["Error Kind", show(call.error_kind)],
    ["Error Message", show(call.error_message)],
  ];

  const tabs: TabItem[] = [
    { value: "call", label: t("调用信息"), content: <section>
      <dl className={styles.details}>{fields.map(([label, value]) => <div key={label}><dt>{label}</dt><dd><code>{value}</code></dd></div>)}</dl>
    </section> },
    { value: "request", label: t("请求"), content: <section>
      {request ? <>
        <div className={styles.meta}>{t("字节数")}：{request.byte_count}</div>
        <h4>{t("请求头")}</h4><JsonEditor ariaLabel={t("请求头")} value={JSON.stringify(request.headers)} readOnly />
        <h4>{t("请求体")}</h4><JsonEditor ariaLabel={t("请求体")} value={JSON.stringify(request.body)} readOnly detail />
      </> : <div className={styles.empty}>{t("未记录请求内容，请开启详细记录后重试。")}</div>}
    </section> },
    { value: "response", label: t("响应流"), content: <section>
      {chunks.length > 0 ? <>
        <div className={styles.meta}>{t("分块数")}：{chunks.length} · {t("字节数")}：{responseBytes}</div>
        <JsonEditor ariaLabel={t("响应流")} value={responseBody} readOnly detail />
      </> : <div className={styles.empty}>{t("未记录响应内容，请开启详细记录后重试。")}</div>}
    </section> },
  ];

  if (cursorTrace) tabs.push({ value: "cursor-trace", label: t("Cursor 追踪"), content: <section>
      <div className={styles.meta}>
        Request ID：{cursorTrace.trace.request_id} · {t("工件数")}：{cursorTrace.artifacts.length}
      </div>
      <JsonEditor
        ariaLabel={t("Cursor 追踪")}
        value={JSON.stringify({ trace: cursorTrace.trace, artifacts: cursorTrace.artifacts })}
        readOnly
        detail
      />
    </section> });

  return <div className={styles.root}><Tabs items={tabs} /></div>;
}
