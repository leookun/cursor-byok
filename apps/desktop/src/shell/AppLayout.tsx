import { useCallback, useEffect, useRef, useState } from "react";
import type { IconifyIcon } from "@iconify/react/offline";
import KeepAliveRouteOutlet from "keepalive-for-react-router";
import { NavLink, useLocation } from "react-router-dom";
import cursorIconUrl from "../shared/assets/icons/cursor.svg";
import { api } from "../shared/api";
import { AdMenu } from "./ads/AdMenu";
import { FloatingAd } from "./ads/FloatingAd";
import { loadCachedAds, saveCachedAds } from "./ads/cache";
import { AdActionType, type AdAction, type AdSlot } from "./ads/types";
import { PageLayout } from "./layout/PageLayout";
import { Card } from "../shared/ui/Card";
import { ConfirmDialog } from "../shared/ui/ConfirmDialog";
import controls from "../shared/ui/Controls.module.scss";
import { Icon } from "../shared/ui/Icon";
import { TooltipTrigger } from "../shared/ui/TooltipTrigger";
import { flatColorAboutIcon, flatColorAreaChartIcon, flatColorCrystalOscillatorIcon, flatColorSalesPerformanceIcon, flatColorSettingsIcon, refreshIcon } from "../shared/ui/icons";
import { useMessage } from "../shared/ui/message";
import { VirtualList } from "../shared/virtual/VirtualList";
import { useI18n } from "../i18n/store";
import { appStore, useAppStore } from "../shared/store/appStore";
import { useUpdateStore } from "../shared/store/updateStore";
import styles from "./AppLayout.module.scss";
import { PageActionsTarget } from "./PageActions";

type MenuItem =
  | { kind: "page"; path: string; label: string; icon: IconifyIcon | string }
  | { kind: "external"; id: string; label: string; icon: IconifyIcon | string }
  | { kind: "group"; label: string };

const keptAlivePages = ["/", "/calls", "/settings", "/harness/cursor", "/aliases", "/plugins"];
const readAdStorageKey = "cursor-byok:read-ad-ids";
const dismissedAdStorageKey = "cursor-byok:dismissed-ad-ids";
const tutorialReadStorageKey = "cursor-byok:tutorial-read";
const tutorialUrl = "https://docs.leokun.cn";

function loadStoredAdIds(key: string): Set<string> {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key) ?? "[]");
    return new Set(Array.isArray(value) ? value.filter((id): id is string => typeof id === "string") : []);
  } catch {
    return new Set();
  }
}

export function AppLayout() {
  const { busy, cursorHarness } = useAppStore();
  const { availableVersion } = useUpdateStore();
  const { locale } = useI18n();
  const message = useMessage();
  const location = useLocation();
  const [leftActionTarget, setLeftActionTarget] = useState<HTMLDivElement | null>(null);
  const [rightActionTarget, setRightActionTarget] = useState<HTMLDivElement | null>(null);
  const [ads, setAds] = useState<AdSlot[]>(() => loadCachedAds());
  const [activeAd, setActiveAd] = useState<AdSlot | null>(null);
  const [dismissCandidate, setDismissCandidate] = useState<AdSlot | null>(null);
  const [dismissReason, setDismissReason] = useState("");
  const [confirmTutorial, setConfirmTutorial] = useState(false);
  const [tutorialRead, setTutorialRead] = useState(() => {
    try {
      return localStorage.getItem(tutorialReadStorageKey) === "true";
    } catch {
      return false;
    }
  });
  const [readAdIds, setReadAdIds] = useState(() => loadStoredAdIds(readAdStorageKey));
  const [dismissedAdIds, setDismissedAdIds] = useState(() => loadStoredAdIds(dismissedAdStorageKey));
  const dismissedAdIdsRef = useRef(dismissedAdIds);
  dismissedAdIdsRef.current = dismissedAdIds;
  const adTriggers = useRef(new Map<string, HTMLButtonElement>());
  const closeAd = useCallback(() => setActiveAd(null), []);
  const visibleAds = ads.filter((ad) => ad.enabled && ad.placement === "menu" && !dismissedAdIds.has(ad.id));
  const menuItems: MenuItem[] = [
    { kind: "page", path: "/", label: t("数据概览"), icon: flatColorAreaChartIcon },
    { kind: "page", path: "/calls", label: t("调用详细"), icon: flatColorSalesPerformanceIcon },
    { kind: "group", label: t("模型配置") },
    { kind: "page", path: "/harness/cursor", label: "Cursor", icon: cursorIconUrl },
    { kind: "page", path: "/aliases", label: t("模型别名"), icon: flatColorCrystalOscillatorIcon },
    { kind: "group", label: t("设置") },
    { kind: "page", path: "/plugins", label: t("插件配置"), icon: flatColorCrystalOscillatorIcon },
    { kind: "page", path: "/settings", label: t("系统设置"), icon: flatColorSettingsIcon },
    { kind: "external", id: "tutorial", label: t("使用教程"), icon: flatColorAboutIcon },
  ];

  const openTutorial = useCallback(() => {
    setConfirmTutorial(false);
    void api.openExternalUrl(tutorialUrl)
      .then(() => {
        setTutorialRead(true);
        try {
          localStorage.setItem(tutorialReadStorageKey, "true");
        } catch {
          // Read state remains valid for the current session when storage is unavailable.
        }
      })
      .catch((cause) => message(cause instanceof Error ? cause.message : String(cause)));
  }, [message]);

  useEffect(() => {
    let disposed = false;
    let pending = false;
    let lastRequestedAt = 0;
    setAds(loadCachedAds());
    setActiveAd(null);
    const refreshAds = () => {
      const now = Date.now();
      if (pending || now - lastRequestedAt < 10_000) return;
      pending = true;
      lastRequestedAt = now;
      void api.ads(dismissedAdIdsRef.current, locale)
        .then((runtime) => {
          saveCachedAds(runtime.slots);
          if (disposed) return;
          setAds(runtime.slots);
          setActiveAd((current) => current
            ? runtime.slots.find((ad) => ad.id === current.id) ?? null
            : null);
        })
        .catch(() => {
          // Keep the current ads and local cache available when refreshing fails.
        })
        .finally(() => { pending = false; });
    };
    const refreshVisibleAds = () => {
      if (document.visibilityState === "visible") refreshAds();
    };

    refreshAds();
    window.addEventListener("focus", refreshAds);
    document.addEventListener("visibilitychange", refreshVisibleAds);
    return () => {
      disposed = true;
      window.removeEventListener("focus", refreshAds);
      document.removeEventListener("visibilitychange", refreshVisibleAds);
    };
  }, [locale]);

  const openAd = useCallback((ad: AdSlot) => {
    setReadAdIds((current) => {
      if (current.has(ad.id)) return current;
      const next = new Set(current).add(ad.id);
      try {
        localStorage.setItem(readAdStorageKey, JSON.stringify([...next]));
      } catch {
        // Read state remains valid for the current session when storage is unavailable.
      }
      return next;
    });
    setActiveAd((current) => current?.id === ad.id ? null : ad);
  }, []);

  const performAdAction = useCallback((action: AdAction) => {
    if (action.type !== AdActionType.OpenBrowser) return;
    void api.openExternalUrl(action.url)
      .catch((cause) => message(cause instanceof Error ? cause.message : String(cause)));
  }, [message]);

  const openDismissAd = useCallback((ad: AdSlot) => {
    setDismissReason("");
    setDismissCandidate(ad);
  }, []);

  const closeDismissAd = useCallback(() => {
    setDismissCandidate(null);
    setDismissReason("");
  }, []);

  const dismissAd = useCallback(() => {
    if (!dismissCandidate) return;
    const dismissedId = dismissCandidate.id;
    const reason = dismissReason.trim();
    setDismissedAdIds((current) => {
      const next = new Set(current).add(dismissedId);
      try {
        localStorage.setItem(dismissedAdStorageKey, JSON.stringify([...next]));
      } catch {
        // Dismissed state remains valid for the current session when storage is unavailable.
      }
      return next;
    });
    setActiveAd((current) => current?.id === dismissedId ? null : current);
    setDismissCandidate(null);
    setDismissReason("");
    void api.dismissAd(dismissedId, reason).catch(() => {
      // Dismissal is intentionally fire-and-forget so reporting never blocks the UI.
    });
  }, [dismissCandidate, dismissReason]);

  return <PageLayout className={styles.root}>
    <Card as="aside" className={styles.menuCard}>
      <nav className={styles.navigation} aria-label={t("主菜单")}>
        <VirtualList
          items={menuItems}
          itemKey={(item) => item.kind === "group" ? `group-${item.label}` : item.kind === "external" ? `external-${item.id}` : item.path}
          estimatedItemHeight={36}
          itemGap={3}
          className={`${styles.navigationList} scroll-shadow-bottom`}
        >
          {(item) => item.kind === "group"
          ? <div className={styles.navigationGroup} key={`group-${item.label}`}>{item.label}</div>
          : item.kind === "external"
          ? <div className={styles.navigationRow} key={item.id}>
            <button
              type="button"
              aria-label={`${item.label}${tutorialRead ? "" : `，${t("未读")}`}`}
              onClick={() => setConfirmTutorial(true)}
            >
              {typeof item.icon === "string"
                ? <Icon src={item.icon} size="1.3em" />
                : <Icon icon={item.icon} size="1.3em" />}
              <span>{item.label}</span>
              {!tutorialRead && <span className={styles.menuIndicatorDot} aria-hidden="true" />}
            </button>
          </div>
          : <div className={styles.navigationRow} key={item.path}>
            <NavLink to={item.path} end={item.path === "/"}>
              {typeof item.icon === "string"
                ? <Icon src={item.icon} size="1.3em" />
                : <Icon icon={item.icon} size="1.3em" />}
              <span>{item.label}</span>
              {item.path === "/harness/cursor" && cursorHarness && <span
                className={styles.menuStatusTag}
                data-taken={cursorHarness.settings_applied || undefined}
              >
                {cursorHarness.settings_applied ? t("已接管") : t("未接管")}
              </span>}
              {item.path === "/settings" && availableVersion && <span className={styles.menuIndicatorDot} aria-hidden="true" />}
            </NavLink>
          </div>}
        </VirtualList>
        <AdMenu ads={visibleAds} activeAdId={activeAd?.id} dismissingAdId={dismissCandidate?.id} readAdIds={readAdIds} triggerRefs={adTriggers} onOpen={openAd} onDismiss={openDismissAd} />
      </nav>
    </Card>
    {activeAd && <FloatingAd ad={activeAd} trigger={adTriggers.current.get(activeAd.id) ?? null} onClose={closeAd} onAction={performAdAction} />}
    <ConfirmDialog
      id="open-tutorial-dialog"
      open={confirmTutorial}
      title={t("打开使用教程？")}
      cancelLabel={t("取消")}
      confirmLabel={t("打开教程")}
      onCancel={() => setConfirmTutorial(false)}
      onConfirm={openTutorial}
    >
      <p>{t("将在系统浏览器中打开使用教程，是否继续？")}</p>
    </ConfirmDialog>
    <ConfirmDialog
      id="dismiss-ad-dialog"
      open={dismissCandidate !== null}
      title={t("不再显示此广告")}
      cancelLabel={t("取消")}
      confirmLabel={t("确认")}
      onCancel={closeDismissAd}
      onConfirm={() => void dismissAd()}
    >
      <p>{t("你确认不想再看到此广告吗？")}</p>
      <label className={styles.dismissReason}>
        <span>{t("原因（可选）")}</span>
        <textarea
          value={dismissReason}
          maxLength={2000}
          rows={4}
          placeholder={t("可以告诉我们原因")}
          onChange={(event) => setDismissReason(event.target.value)}
        />
      </label>
    </ConfirmDialog>
    <main className={styles.content}>
      <div className={styles.actionRegion}>
        <Card className={styles.actions}>
          <div ref={setLeftActionTarget} className={styles.pageActions} />
          {location.pathname !== "/" && <TooltipTrigger label={t("刷新")}><button className={controls.iconButton} aria-label={t("刷新")} disabled={busy} onClick={() => void appStore.refresh()}>
            <Icon className={busy ? controls.spin : ""} icon={refreshIcon} size="1.1em" />
          </button></TooltipTrigger>}
          <div ref={setRightActionTarget} className={styles.pageActions} />
        </Card>
      </div>
      <PageActionsTarget.Provider value={{ left: leftActionTarget, right: rightActionTarget }}>
        <KeepAliveRouteOutlet
          activeCacheKey={location.pathname}
          include={keptAlivePages}
          max={keptAlivePages.length}
          enableActivity
          containerClassName={styles.keepAliveContainer}
          cacheNodeClassName={styles.keepAlivePage}
        />
      </PageActionsTarget.Provider>
    </main>
  </PageLayout>;
}
