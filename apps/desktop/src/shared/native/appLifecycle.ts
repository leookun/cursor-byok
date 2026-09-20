import { getVersion, setDockVisibility } from "@tauri-apps/api/app";
import { invoke, isTauri } from "@tauri-apps/api/core";
import { disable, enable, isEnabled } from "@tauri-apps/plugin-autostart";
import { relaunch } from "@tauri-apps/plugin-process";
import { check, type Update } from "@tauri-apps/plugin-updater";
import { api, type DesktopSettings } from "../api";
import { desktopPlatform } from "./platform";

export function hasNativeAppLifecycle(): boolean {
  return isTauri();
}

export function hasDockVisibilitySetting(): boolean {
  return hasNativeAppLifecycle() && desktopPlatform() === "macos";
}

export async function currentAppVersion(): Promise<string> {
  return hasNativeAppLifecycle() ? getVersion() : "dev";
}

export async function readAutostart(): Promise<boolean> {
  return isEnabled();
}

export async function writeAutostart(enabled: boolean): Promise<void> {
  await (enabled ? enable() : disable());
}

export async function readDesktopSettings(): Promise<DesktopSettings> {
  return api.desktopSettings();
}

export async function writeSilentStart(silentStart: boolean): Promise<void> {
  const settings = await readDesktopSettings();
  await api.setDesktopSettings({ ...settings, silent_start: silentStart });
}

export async function writeDockIconVisibility(visible: boolean): Promise<void> {
  const settings = await readDesktopSettings();
  await setDockVisibility(visible);
  try {
    await api.setDesktopSettings({ ...settings, show_dock_icon: visible });
  } catch (cause) {
    await setDockVisibility(settings.show_dock_icon).catch(() => {});
    throw cause;
  }
}

export type AppUpdate = {
  version: string;
  install(): Promise<void>;
  close(): Promise<void>;
};

type PortableUpdateInfo = {
  version: string;
};

export async function checkForUpdate(): Promise<AppUpdate | null> {
  if (import.meta.env.VITE_APP_UPDATES_ENABLED === "false") return null;
  if (desktopPlatform() === "windows") {
    const update = await invoke<PortableUpdateInfo | null>("check_portable_update");
    if (!update) return null;
    return {
      version: update.version,
      install: () => invoke("install_portable_update", { expectedVersion: update.version }),
      close: async () => {},
    };
  }

  const update: Update | null = await check();
  if (!update) return null;
  return {
    version: update.version,
    install: async () => {
      await update.downloadAndInstall();
      await relaunch();
    },
    close: () => update.close(),
  };
}

export async function installUpdate(update: AppUpdate): Promise<void> {
  await update.install();
}
