# 0013. UI language: a dictionary in the page, error codes from the server

- Status: Accepted
- Date: 2026-10-17

## Context

The admin UI is Japanese only. Its strings are literals in `web/index.html` and the scripts (`web/app.js` alone has about 480 lines with Japanese), and the server's API errors are Japanese or English sentences in `{"error": "..."}` (`fail` and `reject` in `server.go`). The release needs an English UI (`docs/TODO.md`), and the strings cannot all move at once while other work changes the same screens.

The UI is embedded in the exe and uses no framework or build step, so a library such as i18next would be one more vendored dependency for little gain: the page needs lookup, `{name}` slots and a fallback, not plural rules or ICU formats.

Two ways were considered for the server's errors:

- Choose the language on the server (Accept-Language, or a server setting): every error site would need both languages, the server would have to know the page's choice (which lives in the browser), and the console, the log and the API would mix languages
- Send a stable code and parameters with the existing text, and let the page translate the codes it knows: the server keeps one language for logs, and each error can move when its screen does

## Decision

- The page's strings live in `web/i18n.js`: a Japanese and an English dictionary keyed by stable ids (`screen.part`), and `t(key, params)` with `{name}` slots. No plural forms: counts are written into the phrasing. Japanese has every key; a key missing in English shows the Japanese string while the strings move, and debug builds log it
- Static strings in `index.html` carry `data-i18n` (text) or `data-i18n-placeholder`, `-aria-label`, `-title` (attributes). Nodes the scripts make use `bind()`, which marks them the same way
- The language is chosen per browser: automatic from `navigator.languages` (the first Japanese or English entry; English when none), or fixed to 日本語 or English in the settings. The choice is kept in `localStorage` (`iwato-lang`); the server has no language setting. A change applies at once to the marked nodes; other strings change on their next render, without a reload
- API errors the page should translate are written with `failCode` / `rejectCode` (`apierror.go`): `{"error": "<Japanese text>", "code": "<snake_case>", "params": {...}}`. The page shows `error.<code>` from its dictionary when it has it, and the server's `error` text otherwise. Errors move to codes screen by screen; `fail` stays for the rest
- Language names are shown in their own language, and the settings section is titled 「言語 / Language」 so that it can be found in either language

## Consequences

- New strings go into the dictionary from now on; old ones move screen by screen with their errors. Until then an English page shows Japanese on screens not moved yet
- Codes are part of the API between the exe and its page (both ship together, so there is no version skew). `TestErrorCodesHaveStrings` checks that each code used in Go has a Japanese string
- The server's logs and console do not change; making the console and the startup checks (`checks.go`, `checks_docker.go`) English is separate work
- Dates and numbers are still formatted for `ja-JP` in places (`toLocaleString`); they move with their screens
- Browser checks that look for moved strings pin the browser's locale (`ja-JP`), since automatic follows Playwright's default `en-US`
