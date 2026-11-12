# Translations

The web interface uses JSON message catalogs. English is the source language and the fallback for a
missing translation; users never see a raw message key merely because a translation is absent.

## Language coverage

- **Complete:** English (`en`), Greek (`el`), Russian (`ru`), Turkish (`tr`).
- **Partial:** Spanish (`es`), French (`fr`), German (`de`). Missing strings fall back to English.

CLI output, `--help` and application-error message text remain English. Right-to-left layouts are
not currently supported.

## Choose a language

The workspace default is `[ui] language` in `<workspace>/config.toml`. Set it during initialization:

```bash
repairflow init --language el
```

`repairflow init --interactive` also asks for a language. Each browser can override the workspace
default in **Settings → Language**. This choice is stored in a browser cookie, not in the account.

Language precedence is: browser cookie → workspace default → English. An unknown configured language
logs a warning and falls back without stopping the server.

## Catalog format

Embedded catalogs live in `internal/i18n/catalogs/<code>.json`:

```json
{
  "language": "el",
  "name": "Ελληνικά",
  "messages": {
    "nav.customers": "Πελάτες",
    "dashboard.waiting.one": "{0} αναμένει",
    "dashboard.waiting.other": "{0} αναμένουν"
  }
}
```

- `language` is the primary language tag; regional forms such as `el-GR` normalize to it.
- `name` is the native language name shown in the picker.
- Keys use `area.thing` names. Reuse existing keys rather than inventing parallel labels.
- Placeholders are positional: `{0}`, `{1}`. Reorder them when grammar requires it, but do not
  rename or remove them.
- Plural messages use CLDR suffixes: `.one`, `.few`, `.many` and `.other`. `other` is mandatory;
  include only the additional categories the language uses.

English, Greek, Spanish and German use `one`/`other`. French treats 0–1 as `one`. Russian uses
`one`/`few`/`many`/`other`. Turkish uses only `other`.

Keep labels concise and preserve the intended action. The interface must remain usable at 360 px
width. Translate the domain term **repair** consistently; do not reintroduce the retired term
“ticket” in user-facing text.

## Test or override a translation locally

Create `<workspace>/locales/<code>.json` with the same structure. It can override selected strings
in an embedded catalog or add a new language without rebuilding RepairFlow.

A malformed override is reported at startup and ignored; the embedded catalogs remain available.
This is the recommended way for a reviewer to test wording on their own machine.

## Contribute a translation

1. Copy `internal/i18n/catalogs/en.json` to `<code>.json`.
2. Set `language` and the native `name`, then translate the message values.
3. Run the catalog tests:

   ```bash
   go test -count=1 ./internal/i18n/
   ```

4. Start a disposable demo and review login, navigation, settings, dashboard, customer and repair
   pages, validation states and the error page at 360 px and 1280 px.
5. Ask a native speaker to review the result, especially destructive actions, retention warnings and
   other safety-relevant labels.

The tests enforce complete key coverage for `en`, `el`, `ru` and `tr`. Partial catalogs may omit
keys intentionally. If a translated page still contains hardcoded English text, move that text into
the catalogs rather than adding a template-specific workaround.
