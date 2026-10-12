---
name: i18n
description: Implement and review Cursor BYOK desktop localization, including t() usage, locale catalogs, system-language selection, translation completeness, and language settings under apps/desktop.
---

# Desktop i18n

Keep localization organized as:

```text
apps/desktop/
├── plugins/static-i18n-plugin.ts
└── src/i18n/
    ├── I18nRoot.tsx
    ├── runtime.ts
    ├── store.ts
    ├── generated/catalog.json
    └── locales/
        ├── zh-CN.json
        ├── en-US.json
        ├── pt-BR.json
        └── ru-RU.json
```

## Author messages

- Use the global `t()` function with a static Simplified Chinese string literal: `t("保存")`.
- Do not import or locally declare `t`.
- Keep calls inside render paths when the result must update after a language change. Do not translate module-level UI constants.
- Use named placeholders with a static object: `t("第 {page} 页", { page })`.
- Preserve every placeholder name exactly in all translations.
- Wrap all user-visible labels, descriptions, tooltips, empty states, validation messages, and accessibility labels. Do not wrap protocol tokens, model IDs, URLs, or product names that should remain unchanged.

## Locale behavior

- `system` is the default preference.
- Resolve supported operating-system languages to their locale and fall back to `en-US` for every unsupported language.
- Persist only explicit locale selections. Removing the preference restores system-language behavior.
- Apply changes immediately and update the document `lang` attribute.

## Update catalogs

From `apps/desktop`:

1. Run `npm run i18n:scan` after adding or changing `t()` sources.
2. Translate every empty entry in `src/i18n/locales/en-US.json`.
3. Never edit `src/i18n/generated/catalog.json` or `zh-CN.json` manually; scanning owns them.
4. Run `npm run check`. A normal build must fail on a missing translation or mismatched placeholder.

When adding another locale, add it to the plugin's supported locales, runtime locale type and messages, locale resolver, settings options, and provide a complete locale JSON file.
