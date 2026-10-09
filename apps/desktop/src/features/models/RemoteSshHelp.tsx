import { useState } from "react";
import { api } from "../../shared/api";
import controls from "../../shared/ui/Controls.module.scss";
import { Modal } from "../../shared/ui/Modal";
import styles from "./CursorSettings.module.scss";
import helpStyles from "./RemoteSshHelp.module.scss";

const remoteSettings = '{\n  "cursorAgentHost.remoteInferenceRoute": "always"\n}';

export function RemoteSshHelp({ onClose }: { onClose: () => void }) {
  const [copyState, setCopyState] = useState<"idle" | "copying" | "copied" | "failed">("idle");
  const copySettings = async () => {
    setCopyState("copying");
    try {
      await api.copyCursorText(remoteSettings);
      setCopyState("copied");
    } catch {
      setCopyState("failed");
    }
  };

  return <Modal open title={t("Remote SSH 设置指南")} onClose={onClose} closeLabel={t("关闭")}>
    <div className={`${styles.editor} ${helpStyles.content}`}>
      <p>{t("需要 Cursor 3.21.16 或更高版本。保持本机 cursor-byok 运行，并先完成本地 CA 和模型配置。")}</p>
      <ol className={styles.editor}>
        <li>{t("连接 SSH 主机，在 Cursor 设置中选择 Remote [SSH: 主机]，打开该主机的远程设置 JSON。每个 SSH 主机需单独设置。")}</li>
        <li>
          <p>{t("合并以下设置，保留已有配置。不要只修改本地 User 设置或工作区设置。")}</p>
          <pre className={styles.command}><code>{remoteSettings}</code></pre>
        </li>
        <li>{t("保存后，在命令面板运行 Developer: Reload Window（重新加载窗口）。")}</li>
        <li>{t("新建对话，手动选择已配置的 BYOK 模型并发送请求；不要继续旧对话或选择 Auto。")}</li>
      </ol>
      <div>
        <button type="button" className={controls.secondary} disabled={copyState === "copying"} aria-busy={copyState === "copying"} onClick={() => void copySettings()}>
          {copyState === "copying" ? t("复制中…") : t("复制远程设置")}
        </button>
        <p role="status" aria-live="polite">
          {copyState === "copied" && t("设置已复制。请粘贴到该 SSH 主机的远程设置中；尚未自动应用。")}
          {copyState === "failed" && t("复制失败。请手动选择并复制上方 JSON，或在桌面应用中重试。")}
        </p>
      </div>
      <p>{t("该设置让远程推理经 Cursor 客户端回到本机 cursor-byok，无需 SSH 隧道，也无需在远程主机安装本地 CA。工具仍在 SSH 工作区执行。")}</p>
      <p>{t("本地接管状态只反映本机代理配置，不能验证 SSH 是否可用。请分别测试 Agent、工具调用、MCP、Skills、Tab 和提交信息生成。")}</p>
      <p>{t("若出现连接 127.0.0.1 失败，远程进程的 localhost 指向远程主机，不是本机；请先检查远程设置是否生效。证书错误需根据报错进程和目标地址单独诊断，不要关闭 TLS 验证或复制 CA 私钥。")}</p>
    </div>
  </Modal>;
}
