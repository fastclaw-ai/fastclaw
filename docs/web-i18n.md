# Web UI internationalization (i18n)

The web UI uses [next-intl](https://next-intl.dev) in **client-only mode**: no
`[locale]` route segment, no middleware, no `next.config` plugin. This keeps
the static export (`output: 'export'`, embedded into the Go binary via
`go:embed`) byte-for-byte identical in structure — i18n adds a provider and
two JSON dictionaries, nothing else.

## How locale is resolved

Resolution happens client-side in `web/src/i18n/locale-provider.tsx`, in
priority order:

1. **User preference** — persisted in `localStorage` under `fastclaw-locale`.
   Set via the language switcher in the sidebar user menu, or on the login
   screen.
2. **Browser language** — `navigator.languages` matched by primary subtag
   (`zh`, `zh-Hans-CN`, … → `zh-CN`).
3. **Build-time default** — `NEXT_PUBLIC_DEFAULT_LOCALE`, inlined at
   `next build` time. This also decides the language of the prerendered HTML,
   i.e. what users see on first paint.

## Configuring the default language

The default is `en`. To ship a build that renders Chinese by default:

```sh
NEXT_PUBLIC_DEFAULT_LOCALE=zh-CN make build-web
NEXT_PUBLIC_DEFAULT_LOCALE=zh-CN make build
```

or put it in `web/.env.local` (git-ignored) for local dev:

```
NEXT_PUBLIC_DEFAULT_LOCALE=zh-CN
```

Because the variable is inlined at build time, changing it requires a
rebuild — the Go binary serves whatever the export was built with.

## File layout

```
web/messages/en.json           source-of-truth dictionary
web/messages/zh-CN.json        Simplified Chinese
web/src/i18n/config.ts         locale list, labels, default resolution
web/src/i18n/locale-provider.tsx  NextIntlClientProvider + persistence
web/src/i18n/types.d.ts        typed message keys (typos fail `next build`)
```

## Adding / translating strings

1. Add the key to `web/messages/en.json` (namespaced by surface: `login`,
   `nav`, `agents`, … shared verbs go in `common`).
2. Add the same key to every other dictionary (`zh-CN.json`).
3. In the component:

   ```tsx
   import { useTranslations } from "next-intl";

   const t = useTranslations("agents");
   <h2>{t("title")}</h2>
   <p>{t("ownerLabel", { name })}</p>          // interpolation
   <p>{t.rich("deleteConfirm", {               // embedded markup
     id,
     strong: (chunks) => <strong>{chunks}</strong>,
   })}</p>
   ```

Message keys are type-checked against `en.json` (see `types.d.ts`), so a
typo'd or missing key is a compile error, not a runtime fallback.

## Adding a locale

1. Create `web/messages/<locale>.json` (copy `en.json`, translate).
2. Add the locale to `LOCALES` and `LOCALE_LABELS` in `web/src/i18n/config.ts`.
3. Register the dictionary in the `MESSAGES` map in
   `web/src/i18n/locale-provider.tsx`.

## Known boundaries

- **Server-originated strings** (API error messages from the Go backend,
  agent/system content) are not translated by this layer; the UI falls back
  to a translated generic message only when the API returns no error string.
- Both dictionaries are bundled statically. That is deliberate: there is no
  origin server to lazy-load from in a `go:embed` static export, and it keeps
  locale switching instant. Revisit only if dictionary weight becomes a
  problem (hundreds of KB).
- Extraction is incremental. `login`, main navigation, and the agents page
  are done; remaining pages still render their hardcoded English until their
  strings are moved into the dictionaries.
