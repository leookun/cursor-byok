---
name: i18n-requirements
description: Use when adding or changing frontend UI text, locale support, translation JSON, the static i18n scanner, language selection, or native tray labels in this repository; keeps source messages, generated catalogs, translations, and runtime locale registration consistent.
---

# I18n Requirements

## Source Messages

- Treat `zh-CN` as the only source locale.
- Write user-visible frontend text as Chinese source literals in scanned files under `frontend/src/`.
- Do not branch on locale or hard-code English, Japanese, Russian, or other translated UI text in components or state modules.
- Let `frontend/plugins/static-i18n-plugin.js` replace source literals with runtime helpers. Do not hand-write generated message IDs in application code.
- Keep internal matching tokens out of the catalog. Use a regex for Chinese protocol/error matching instead of a user-visible string literal when the text is not intended for display.
- Do not place ordinary user-visible source messages under `frontend/src/i18n/`; the scanner excludes that directory. Native language names in `LOCALE_OPTIONS` are an intentional exception.

## Generated Catalogs

- Treat `frontend/src/i18n/generated/catalog.json` and the source-locale entries as scanner output. Do not manually edit catalog references or message IDs.
- Run `npm run build` from `frontend/` after changing UI text. The build must run with `--scan` and update every locale file.
- Preserve every placeholder exactly across locales, including `{0}`, `{1}`, newlines, and formula fragments such as `${1}`.
- Provide a non-empty translation for every catalog key in every non-source locale. Do not rely on the Chinese fallback for completed locale support.

## Adding A Locale

Update all of these integration points together:

- `SUPPORTED_LOCALES` in `frontend/plugins/static-i18n-plugin.js`.
- `SUPPORTED_LOCALES` and `LOCALE_OPTIONS` in `frontend/src/i18n/config.js`.
- The locale JSON import, `localeMessages`, and primary-language mapping in `frontend/src/i18n/runtime.js`.
- `frontend/src/i18n/locales/<locale>.json` with the complete catalog key set.
- Native tray labels in `internal/app/runner.go`.

## Verification

After the scan build:

1. Confirm `npm run build` succeeds.
2. Confirm every locale JSON has the same keys as `catalog.json`.
3. Confirm non-source locale files contain no empty values.
4. Confirm translated placeholders match the source entry placeholders.
5. Run the build twice when scanner behavior changed and confirm generated files are stable.
