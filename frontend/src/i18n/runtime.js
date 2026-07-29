import { computed, ref } from "vue";
import { Events } from "@wailsio/runtime";
import {
  DEFAULT_LOCALE,
  LOCALE_OPTIONS,
  LOCALE_STORAGE_KEY,
  LOCALE_STORAGE_SOURCE_KEY,
  SOURCE_LOCALE,
  SUPPORTED_LOCALES,
} from "@/i18n/config";
import zhCNMessages from "@/i18n/locales/zh-CN.json";
import enUSMessages from "@/i18n/locales/en-US.json";
import jaJPMessages from "@/i18n/locales/ja-JP.json";
import ruRUMessages from "@/i18n/locales/ru-RU.json";

const localeMessages = {
  "zh-CN": zhCNMessages,
  "en-US": enUSMessages,
  "ja-JP": jaJPMessages,
  "ru-RU": ruRUMessages,
};

const languageLocaleMap = {
  zh: "zh-CN",
  en: "en-US",
  ja: "ja-JP",
  ru: "ru-RU",
};

function isSupportedLocale(locale) {
  return SUPPORTED_LOCALES.includes(locale);
}

function matchSupportedLocale(locale) {
  const normalized = String(locale || "").trim().replace(/_/g, "-");
  if (!normalized) {
    return "";
  }

  const lowered = normalized.toLowerCase();
  const exactMatch = SUPPORTED_LOCALES.find((supportedLocale) => supportedLocale.toLowerCase() === lowered);
  if (exactMatch) {
    return exactMatch;
  }

  const primaryLanguage = lowered.split("-")[0];
  return languageLocaleMap[primaryLanguage] || "";
}

function getSystemLocaleCandidates() {
  const candidates = [];

  if (typeof navigator !== "undefined") {
    if (Array.isArray(navigator.languages)) {
      candidates.push(...navigator.languages);
    }
    candidates.push(navigator.language);
  }

  if (typeof Intl !== "undefined" && typeof Intl.DateTimeFormat === "function") {
    candidates.push(Intl.DateTimeFormat().resolvedOptions()?.locale);
  }

  return candidates;
}

function resolveSystemLocale() {
  for (const candidate of getSystemLocaleCandidates()) {
    const matchedLocale = matchSupportedLocale(candidate);
    if (matchedLocale) {
      return matchedLocale;
    }
  }

  return DEFAULT_LOCALE;
}

function resolveInitialLocale() {
  if (typeof window === "undefined" || typeof window.localStorage === "undefined") {
    return resolveSystemLocale();
  }

  const storedLocale = window.localStorage.getItem(LOCALE_STORAGE_KEY);
  const storedSource = window.localStorage.getItem(LOCALE_STORAGE_SOURCE_KEY);
  if (storedSource === "manual") {
    return matchSupportedLocale(storedLocale) || resolveSystemLocale();
  }

  window.localStorage.removeItem(LOCALE_STORAGE_KEY);
  window.localStorage.removeItem(LOCALE_STORAGE_SOURCE_KEY);
  return resolveSystemLocale();
}

function applyLocaleToDocument(locale) {
  if (typeof document !== "undefined") {
    document.documentElement.lang = locale;
  }
}

function persistManualLocale(locale) {
  if (typeof window === "undefined" || typeof window.localStorage === "undefined") {
    return;
  }

  window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
  window.localStorage.setItem(LOCALE_STORAGE_SOURCE_KEY, "manual");
}

function resolveMessage(id, fallback) {
  const activeMessages = localeMessages[currentLocale.value] || {};
  const sourceMessages = localeMessages[SOURCE_LOCALE] || {};
  return activeMessages[id] || sourceMessages[id] || fallback || "";
}

function interpolateMessage(template, args = []) {
  return template.replace(/\{(\d+)\}/g, (_match, index) => {
    const value = args[Number(index)];
    return value == null ? "" : String(value);
  });
}

class LocalizedText extends String {
  constructor(id, fallback, args = null) {
    super(fallback);
    this.id = id;
    this.fallback = fallback;
    this.args = args;
  }

  toString() {
    const text = resolveMessage(this.id, this.fallback);
    return Array.isArray(this.args) ? interpolateMessage(text, this.args) : text;
  }

  valueOf() {
    return this.toString();
  }

  toJSON() {
    return this.toString();
  }

  [Symbol.toPrimitive]() {
    return this.toString();
  }
}

const currentLocale = ref(resolveInitialLocale());
applyLocaleToDocument(currentLocale.value);
Events.Emit("locale:changed", currentLocale.value);

const localizedCache = new Map();

export function getLocale() {
  return currentLocale.value;
}

export function setLocale(locale) {
  const nextLocale = matchSupportedLocale(locale) || DEFAULT_LOCALE;
  currentLocale.value = nextLocale;
  persistManualLocale(nextLocale);
  applyLocaleToDocument(nextLocale);
  Events.Emit("locale:changed", nextLocale);
  return nextLocale;
}

export function useLocale() {
  return {
    locale: currentLocale,
    localeOptions: LOCALE_OPTIONS,
    currentLocale: computed(() => currentLocale.value),
    setLocale,
  };
}

export function localized(id, fallback) {
  const cacheKey = `${id}:${fallback}`;
  if (!localizedCache.has(cacheKey)) {
    localizedCache.set(cacheKey, new LocalizedText(id, fallback));
  }
  return localizedCache.get(cacheKey);
}

export function localizedTemplate(id, fallback, args = []) {
  return new LocalizedText(id, fallback, args);
}

export function installI18nRuntime(app) {
  app.config.globalProperties.$ls = localized;
  app.config.globalProperties.$lt = localizedTemplate;
}
