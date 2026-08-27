import type { ModelConnectivityResult } from "../../api";
import { Icon } from "../ui/Icon";
import { TooltipTrigger } from "../ui/TooltipTrigger";
import { informationOutlineIcon } from "../ui/icons";
import styles from "./CursorModelTestResult.module.scss";

export type CursorModelTestState =
  | { status: "success"; result: ModelConnectivityResult }
  | { status: "error"; error: string }
  | { status: "cancelled" };

export function CursorModelTestResult({ state, testing = false }: { state?: CursorModelTestState; testing?: boolean }) {
  if (testing) return <div className={`${styles.root} ${styles.testing}`}><span className={styles.summary}>{t("测试中…")}</span></div>;
  if (!state) return <div className={`${styles.root} ${styles.idle}`}><span className={styles.summary}>{t("未测试")}</span></div>;
  if (state.status === "cancelled") return <div className={`${styles.root} ${styles.idle}`}><span className={styles.summary}>{t("测试已取消")}</span></div>;

  const success = state.status === "success";
  const error = success ? "" : displayTestError(state.error);
  const summary = success
    ? t("速度：{speed} tokens/s", { speed: formatSpeed(state.result.tokens_per_second) })
    : t("错误：{error}", { error });
  const detail = success
    ? t("速度 {speed} tokens/s · 首字 {firstText} ms · 总耗时 {duration} ms · 输出 {tokens} tokens{estimated} · 返回：{output}", {
      speed: formatSpeed(state.result.tokens_per_second),
      firstText: state.result.first_text_ms ?? "--",
      duration: state.result.duration_ms,
      tokens: state.result.output_tokens,
      estimated: state.result.tokens_estimated ? t("（估算）") : "",
      output: state.result.output || "--",
    })
    : t("测试失败：{error}", { error });

  return <div className={`${styles.root} ${success ? styles.success : styles.error}`}>
    <span className={styles.summary}>{summary}</span>
    <TooltipTrigger label={detail}><button type="button" className={styles.details}>{t("查看详情")}<Icon icon={informationOutlineIcon} size="1.1em" /></button></TooltipTrigger>
  </div>;
}

function formatSpeed(value: number) {
  return Number.isFinite(value) ? value.toFixed(1) : "0.0";
}

function displayTestError(error: string) {
  const text = error.replace(/^provider error:\s*/i, "").trim();
  if (/quota exhausted on all accounts/i.test(text)) return t("所有账号额度已用尽");
  return text;
}
