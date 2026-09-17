import { useSyncExternalStore } from "react";
import { setRuntimeLocale, type Locale } from "./runtime";

export type LocalePreference = "system" | Locale;

type I18nSnapshot = {
  preference: LocalePreference;
  locale: Locale;
};

const storageKey = "cursor-byok.locale";
const listeners = new Set<() => void>();
let initialized = false;
let snapshot: I18nSnapshot = { preference: "system", locale: "en-US" };

function isLocale(value: string | null): value is Locale {
  return value === "zh-CN" || value === "en-US" || value === "pt-BR";
}

export function resolveSystemLocale(
  languages: readonly string[] = navigator.languages.length
    ? navigator.languages
    : [navigator.language],
): Locale {
  const normalized = languages[0]?.toLowerCase() ?? "";
  if (normalized === "zh" || normalized.startsWith("zh-")) return "zh-CN";
  if (normalized === "pt" || normalized.startsWith("pt-")) return "pt-BR";
  if (normalized === "en" || normalized.startsWith("en-")) return "en-US";
  return "en-US";
}

function readPreference(): LocalePreference {
  try {
    const saved = localStorage.getItem(storageKey);
    return isLocale(saved) ? saved : "system";
  } catch {
    return "system";
  }
}

function applyPreference(preference: LocalePreference, notify: boolean) {
  const locale = preference === "system" ? resolveSystemLocale() : preference;
  const changed = snapshot.preference !== preference || snapshot.locale !== locale;
  snapshot = { preference, locale };
  setRuntimeLocale(locale);
  document.documentElement.lang = locale;
  if (notify && changed) listeners.forEach((listener) => listener());
}

export function initializeI18n() {
  if (initialized) return;
  initialized = true;
  applyPreference(readPreference(), false);
  window.addEventListener("languagechange", () => {
    if (snapshot.preference === "system") applyPreference("system", true);
  });
}

export function setLocalePreference(preference: LocalePreference) {
  try {
    if (preference === "system") localStorage.removeItem(storageKey);
    else localStorage.setItem(storageKey, preference);
  } catch {
    // The preference still applies for the current session when storage is unavailable.
  }
  applyPreference(preference, true);
}

export function useI18n(): I18nSnapshot {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => snapshot,
  );
}
