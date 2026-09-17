import zhCN from "./locales/zh-CN.json";
import enUS from "./locales/en-US.json";
import ptBR from "./locales/pt-BR.json";

export type Locale = "zh-CN" | "en-US" | "pt-BR";
export type CommitPromptLocale = "zh-CN" | "en-US";

export function commitPromptLocale(locale: Locale): CommitPromptLocale {
  return locale === "zh-CN" ? "zh-CN" : "en-US";
}

export type TranslationValue = string | number;
export type TranslationParams = Readonly<Record<string, TranslationValue>>;

const sourceMessages = zhCN as Record<string, string>;
const localeMessages: Record<Locale, Record<string, string>> = {
  "zh-CN": sourceMessages,
  "en-US": enUS as Record<string, string>,
  "pt-BR": ptBR as Record<string, string>,
};

let currentMessages: Record<string, string> = {};

export function setRuntimeLocale(locale: Locale) {
  const target = localeMessages[locale];
  currentMessages = {};
  for (const [id, source] of Object.entries(sourceMessages)) {
    currentMessages[source] = target[id] || source;
  }
}

export function t(source: string, params?: TranslationParams): string {
  const translated = currentMessages[source] || source;
  if (!params) return translated;
  return translated.replace(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g, (_match, name: string) => {
    if (!Object.prototype.hasOwnProperty.call(params, name)) {
      throw new Error(`Missing i18n parameter: ${name}`);
    }
    return String(params[name]);
  });
}
