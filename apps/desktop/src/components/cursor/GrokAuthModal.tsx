import { useEffect, useRef, useState } from "react";
import { api, type GrokDeviceCodeResponse } from "../../api";
import { Button } from "../ui/Button";
import { Modal } from "../ui/Modal";
import { Icon } from "../ui/Icon";
import { copyIcon, checkIcon } from "../ui/icons";
import { useMessage } from "../ui/message";
import styles from "./GrokAuthModal.module.scss";

type GrokAuthModalProps = {
  open: boolean;
  onClose: () => void;
  onSuccess: (accessToken: string, refreshToken?: string | null) => void;
};

export function GrokAuthModal({ open, onClose, onSuccess }: GrokAuthModalProps) {
  const message = useMessage();
  const [loading, setLoading] = useState(false);
  const [deviceData, setDeviceData] = useState<GrokDeviceCodeResponse | null>(null);
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
      const res = await api.startGrokDeviceAuth();
      setDeviceData(res);
      setPollStatus("polling");
      // Try copying user code to clipboard automatically
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
        const res = await api.pollGrokDeviceAuth(deviceData.device_code);
        if (res.status === "success" && res.access_token) {
          setPollStatus("success");
          message(t("Grok 账号授权成功！"));
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
        } else if (res.status === "error") {
          setPollStatus("error");
          setErrorMessage(res.error_message || t("授权发生错误。"));
          return;
        }
      } catch (err) {
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
    if (!deviceData) return;
    try {
      await api.copyCursorText(deviceData.user_code);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      try {
        await navigator.clipboard.writeText(deviceData.user_code);
        setCopied(true);
        setTimeout(() => setCopied(false), 2000);
      } catch {
        message(t("复制失败，请手动复制"));
      }
    }
  };

  const handleOpenBrowser = () => {
    if (!deviceData) return;
    const url = deviceData.verification_uri_complete || deviceData.verification_uri;
    void api.openExternalUrl(url).catch(() => {
      window.open(url, "_blank");
    });
  };

  return (
    <Modal
      open={open}
      title={t("Grok (xAI) 官方账号授权登录")}
      onClose={onClose}
      closeLabel={t("关闭")}
    >
      <div className={styles.modalContent}>
        <div className={styles.banner}>
          <span>
            {t("使用 X Premium+ 或 SuperGrok 订阅账号登录，获取 Grok 官方模型额度，无需单独购买 API Key。")}
          </span>
        </div>

        {loading && (
          <div className={styles.statusText}>
            <span className={styles.spinner} />
            <span>{t("正在向 xAI 申请授权代码…")}</span>
          </div>
        )}

        {deviceData && (
          <>
            <div className={styles.steps}>
              <div className={styles.stepItem}>
                <span className={styles.stepNum}>1</span>
                <span className={styles.stepText}>{t("复制下方的 8 位设备验证码：")}</span>
              </div>
            </div>

            <div className={styles.codeBox}>
              <span className={styles.userCode}>{deviceData.user_code}</span>
              <Button onClick={handleCopy}>
                <Icon icon={copied ? checkIcon : copyIcon} />
                <span>{copied ? t("已复制") : t("复制验证码")}</span>
              </Button>
            </div>

            <div className={styles.steps}>
              <div className={styles.stepItem}>
                <span className={styles.stepNum}>2</span>
                <span className={styles.stepText}>
                  {t("点击下方按钮打开 xAI 官方授权页面，登录并输入验证码确认授权：")}
                </span>
              </div>
            </div>

            <div className={styles.actions}>
              <Button variant="primary" onClick={handleOpenBrowser}>
                {t("打开 xAI 授权网页")}
              </Button>

              <div className={styles.statusText}>
                {pollStatus === "polling" && (
                  <span className={styles.statusPending}>
                    <span className={styles.spinner} />
                    {t("等待网页端确认授权中…")}
                  </span>
                )}
                {pollStatus === "success" && (
                  <span className={styles.statusSuccess}>
                    ✓ {t("授权成功！正在应用配置…")}
                  </span>
                )}
              </div>
            </div>
          </>
        )}

        {(pollStatus === "error" || pollStatus === "expired") && (
          <div className={styles.actions}>
            <span className={`${styles.statusText} ${styles.statusError}`}>
              {errorMessage}
            </span>
            <Button onClick={startAuth}>{t("重新获取")}</Button>
          </div>
        )}
      </div>
    </Modal>
  );
}
