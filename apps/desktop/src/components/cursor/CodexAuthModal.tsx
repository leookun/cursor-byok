import { useEffect, useRef, useState } from "react";
import { api, type CodexDeviceCodeResponse } from "../../api";
import { Button } from "../ui/Button";
import { Modal } from "../ui/Modal";
import { Icon } from "../ui/Icon";
import { copyIcon, checkIcon } from "../ui/icons";
import { useMessage } from "../ui/message";
import styles from "./GrokAuthModal.module.scss";

type CodexAuthModalProps = {
  open: boolean;
  onClose: () => void;
  onSuccess: (accessToken: string, refreshToken?: string | null) => void;
};

function isCodexAuthorizationPending(errorMessage: string | null | undefined): boolean {
  const message = (errorMessage || "").toLowerCase();
  return (
    message.includes("authorization is pending") ||
    message.includes("authorization_pending") ||
    message.includes("device authorization is pending")
  );
}

export function CodexAuthModal({ open, onClose, onSuccess }: CodexAuthModalProps) {
  const message = useMessage();
  const [loading, setLoading] = useState(false);
  const [deviceData, setDeviceData] = useState<CodexDeviceCodeResponse | null>(null);
  const [pollStatus, setPollStatus] = useState<"idle" | "polling" | "success" | "error" | "expired">("idle");
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const pollTimerRef = useRef<number | null>(null);
  const activeRef = useRef(false);

  const cleanup = () => {
    if (pollTimerRef.current !== null) {
      window.clearTimeout(pollTimerRef.current);
      pollTimerRef.current = null;
    }
  };

  const startAuth = async () => {
    cleanup();
    setLoading(true);
    setErrorMessage(null);
    setPollStatus("idle");
    setDeviceData(null);
    try {
      const res = await api.startCodexDeviceAuth();
      setDeviceData(res);
      setPollStatus("polling");
      try {
        await api.copyCursorText(res.user_code);
        setCopied(true);
      } catch {
        // clipboard copy fallback
      }
    } catch (err) {
      setPollStatus("error");
      setErrorMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    activeRef.current = open;
    if (open) {
      void startAuth();
    } else {
      cleanup();
    }
    return () => {
      activeRef.current = false;
      cleanup();
    };
  }, [open]);

  // Polling loop
  useEffect(() => {
    if (!open || pollStatus !== "polling" || !deviceData) return;

    let stopped = false;
    const intervalMs = Math.max(deviceData.interval || 5, 5) * 1000;

    const poll = async () => {
      if (stopped || !activeRef.current) return;
      try {
        const res = await api.pollCodexDeviceAuth(deviceData.device_code, deviceData.user_code);
        if (res.status === "success" && res.access_token) {
          setPollStatus("success");
          message(t("ChatGPT / Codex 账号授权成功！"));
          onSuccess(res.access_token, res.refresh_token);
          setTimeout(() => {
            if (activeRef.current) onClose();
          }, 1200);
          return;
        } else if (res.status === "expired") {
          setPollStatus("expired");
          setErrorMessage(t("授权码已过期，请重新获取。"));
          return;
        } else if (res.status === "access_denied") {
          setPollStatus("error");
          setErrorMessage(t("用户拒绝了授权请求。"));
          return;
        } else if (res.status === "error" && !isCodexAuthorizationPending(res.error_message)) {
          setPollStatus("error");
          setErrorMessage(res.error_message || t("授权发生错误。"));
          return;
        }
      } catch {
        // Network errors during polling will retry on next tick
      }

      if (!stopped && activeRef.current) {
        pollTimerRef.current = window.setTimeout(poll, intervalMs);
      }
    };

    pollTimerRef.current = window.setTimeout(poll, intervalMs);

    return () => {
      stopped = true;
      cleanup();
    };
  }, [open, pollStatus, deviceData]);

  const handleCopy = async () => {
    if (!deviceData?.user_code) return;
    try {
      await api.copyCursorText(deviceData.user_code);
      setCopied(true);
      message(t("已复制验证码：{code}", { code: deviceData.user_code }));
      setTimeout(() => setCopied(false), 2000);
    } catch (cause) {
      message(cause instanceof Error ? cause.message : String(cause));
    }
  };

  const handleOpenBrowser = () => {
    const url = deviceData?.verification_uri_complete
      || deviceData?.verification_uri
      || "https://auth.openai.com/codex/device";
    void api.openExternalUrl(url).catch(() => {
      window.open(url, "_blank");
    });
  };

  return (
    <Modal
      open={open}
      title={t("ChatGPT / OpenAI Codex 官方账号授权")}
      onClose={onClose}
      busy={loading}
    >
      <div className={styles.container}>
        {loading && (
          <div className={styles.loadingWrap}>
            <span className={styles.spinner} />
            <p>{t("正在向 OpenAI 请求授权码…")}</p>
          </div>
        )}

        {errorMessage && (
          <div className={styles.errorBanner}>
            <span>{errorMessage}</span>
            <div style={{ display: "flex", gap: "8px", marginTop: "6px" }}>
              <Button size="small" variant="secondary" onClick={() => void api.openExternalUrl("https://chatgpt.com/#settings/Security")}>
                {t("⚙️ 打开 ChatGPT 安全设置")}
              </Button>
              <Button size="small" onClick={() => void startAuth()}>{t("重试")}</Button>
            </div>
          </div>
        )}

        {deviceData && pollStatus === "polling" && (
          <div className={styles.content}>
            <p className={styles.instruction}>
              {t("点击下方按钮打开 OpenAI 授权页面，登录并输入以下验证码：")}
            </p>

            <div className={styles.codeBox} onClick={() => void handleCopy()}>
              <span className={styles.userCode}>{deviceData.user_code}</span>
              <Button size="small" variant="secondary" onClick={(e) => { e.stopPropagation(); void handleCopy(); }}>
                <Icon icon={copied ? checkIcon : copyIcon} size="1em" />
                {copied ? t("已复制") : t("复制验证码")}
              </Button>
            </div>

            <div className={styles.actions}>
              <Button variant="primary" onClick={() => void handleOpenBrowser()}>
                {t("🔗 打开 OpenAI 授权页面")}
              </Button>
            </div>

            <div className={styles.pollingNotice}>
              <span className={styles.dot} />
              <span>{t("等待在浏览器中完成授权…")}</span>
            </div>
          </div>
        )}

        {pollStatus === "success" && (
          <div className={styles.successWrap}>
            <Icon icon={checkIcon} size="2.5em" className={styles.successIcon} />
            <p>{t("授权成功！正在应用配置…")}</p>
          </div>
        )}
      </div>
    </Modal>
  );
}
