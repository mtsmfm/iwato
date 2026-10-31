# Iwato

Instructions for coding agents working on this repository.

- Architecture (what each file and package does): [docs/dev/architecture.md](docs/dev/architecture.md). Read the part you are changing before you change it, and update it in the same change
- Setup, building, CI and every test command: [docs/dev/development.md](docs/dev/development.md)
- Design decisions: [docs/adr/](docs/adr/README.md) (accepted ADRs are not rewritten; a new ADR supersedes one). Open work: [docs/TODO.md](docs/TODO.md)
- User docs: [docs/user/](docs/user/README.md), in English with a Japanese version in `docs/user/ja/` (keep both in step). Developer docs (ADRs, `docs/dev/`, this file) are in English

Follow OpenShell's behaviour and improve only what is really inconvenient. New resource names, labels and image names start with `iwato`.

## Safety

- Never write a string in the shape of an OpenShell placeholder (`openshell:` followed by `resolve:env`) into the repository: once it reaches an agent's conversation, the API refuses every later request. Build it instead (Go: `placeholderPrefix`). `TestNoPlaceholderLiterals` checks this
- Fake secrets in tests are assembled too when they look real (`"tskey" + "-auth-…"`): GitHub's secret scanning reports a whole Tailscale key. The same test checks it
- `PR_SET_DUMPABLE` must not be set to 0 in the sandbox daemon: OpenShell then blocks the network of all its descendants
- Do not replace the daemon's binary while a sandbox runs: OpenShell pins the hashes of the connecting process's and its parents' executables

## Comments and docs

- Write a code comment only when it is really needed: a reason, a constraint or a trap the code cannot show. Do not restate what the code says, and do not narrate history ("now", "used to", "before X")
- Keep comments and docs few, short and concise. When editing, prefer deleting or shortening to adding

## Setup

1. `mise install` (Go and Node, versions in `mise.toml`)
2. `python3 scripts/fetch-vendor.py` (fonts in `web/vendor`, not in the repository)
3. `go mod download`

Inside an Iwato sandbox this needs network proposals (GET only; the list is in [development.md](docs/dev/development.md#developing-inside-an-iwato-sandbox)).

## Branches and parallel work

- One change is one branch off `main`, merged through a pull request. Do not push to `main`; the maintainer merges
- Changes made in parallel (several branches or agents at once) touch separate files. Work on the same screen starts after the earlier change is merged
- Merge conflicts happen most at the end of `web/styles.css` and in the element id list in `scripts/ui-behavior-tests.cjs`; keep both sides' additions

## Tests

Run locally only what you touched and what is related; leave the rest to CI (which runs `go test ./...` and `go test -tags uitest ./...`, and the exe build on every push, the e2e when Go code or the images change, but not the UI tests or browser checks).

- Go: `go test ./<pkg>` or `-run` for the touched and related packages. Tests that use the fake backend (`*_fake_test.go`) need `-tags uitest`
- UI: `node scripts/ui-behavior-tests.cjs`, plus the browser checks of the touched screens, at PC width and `--phone`. They build Iwato with `-tags uitest` and drive `serve -fake`, the fake backend made for them. Terminal → `term-check`, `focus-check`, `keyboard-rows-check`; policy → `policy-check`; providers → `providers-check`; files → `files-check`; editor → `editor-check`; editor and terminal side by side → `split-check`; volumes → `volumes-check`; resources (the header, the sidebar's totals) → `resources-check`; publishing, settings, images, language, reset → `ingress-check`. Run them with:

      cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/CHECK.cjs [--phone]

- After changing the on-screen keyboard handling or the page layout, also run `keyboard-rows-check` in a real browser (the terminal's rows must not change when the keyboard opens)
- e2e: `MODE=tls internal/openshell/wslc/e2e.sh`, only when you touch the driver, the exchanges with OpenShell or the images, and only in a disposable Linux machine or container with passwordless sudo (it writes system paths). About 2 minutes, one run per machine at a time (a lock): give background runs a long timeout. `KEEP=1` keeps the work directory on failure
- Write unit tests only where the logic is complex and a test is really worth it. After adding one, check it does not repeat what existing tests cover, and keep only the valuable ones

## When the work is done (opening a PR)

1. Tests: run locally what you touched and what is related (above), plus `go vet ./...` and `go vet -tags uitest ./...`. CI runs `go test ./...`, `go test -tags uitest ./...` and the exe build after the push
2. Review the diff once more for bugs and security problems before committing
3. Open a pull request to `main`. Its description lists the changes, the tests run and the checks not run (real hardware, browser checks, e2e, …)
4. When asked, build the exe with `scripts/build-exe.sh` (into `dist/`). `web/` is embedded, so UI-only changes need it too. `--local-images` to try `sandboximage/` changes before main, `--debug` only while debugging ([development.md](docs/dev/development.md#building-the-exe))

Inside an Iwato sandbox: pushing and creating the PR each need a network proposal, and GitHub's GraphQL is read-only there, so create the PR with `gh api -X POST /repos/OWNER/REPO/pulls -f base=main -f head=BRANCH -f title=... -F body=@FILE` instead of `gh pr create`.

## UI language (web/i18n.js)

The page's strings live in the Japanese and English dictionaries of `web/i18n.js` (`ja` and `en`) and are looked up with `t('key', {name: value})` (ADR 0013). New strings go into the dictionary from the start. Strings of screens not moved yet move, per screen, when you touch that screen.

- Keys are stable ids `screen.part` (`login.submit`), never the Japanese text. Japanese has every key; English adds the same key (without it the Japanese shows, and debug builds log it). Slots are `{name}`. No plural forms: write the count into the phrasing (「{count} 件」 / "Items: {count}")
- Strings in `index.html` get `data-i18n="key"` (text) or `data-i18n-placeholder`, `data-i18n-aria-label`, `data-i18n-title`, `data-i18n-alt` (attributes), keeping the Japanese text in place. Elements scripts make use `IwatoI18n.bind(node, key, params)` (attributes: `bindAttribute(node, name, key, params)`; panel titles: `panelHeader({ key })`) so they change at once when the language changes; strings built with `t()` change on the next render (no reload; `app.js`'s `onChange` redraws the cards and the sidebar). When a script rewrites an element that has `data-i18n`, it rewrites it with `bind` too (otherwise a language change puts the original key back). Dates and times: `IwatoI18n.date(value, Intl options)` and `time()`. Shared words: `common.close` and the like. Lists in error params are `[]string` (the page joins them: `IwatoI18n.list`). Text the server sends only in Japanese (Iwato's provider profiles and presets) is translated with English-only keys (`profile.*`, `preset.*`) through `IwatoI18n.localized`
- The language is per browser: automatic (the first Japanese or English in `navigator.languages`; English when neither) or the choice in the settings' 「言語 / Language」 (`localStorage` `iwato-lang`). The server has no setting
- API errors the page should translate are written with `failCode(w, status, "code", params, "Japanese text")` (also `rejectCode`), with `error.code` added to `web/i18n.js` (`TestErrorCodesHaveStrings` checks it). The page translates codes it knows and shows the server's text otherwise (`api()`'s `error.message`; the code is `error.code`). Never put secrets in params. Errors made deep down use `errCode(code, params, text)` and are written with `failErr` (or `failGateway`/`failCheck`), which sends the code when the error is not wrapped in more text. Text that also goes to the console or the log is English, with a code the page translates (the preparation's state, the reset's warnings; [architecture.md](docs/dev/architecture.md#web-ui)). OpenShell's refusals (ambiguities, credential conflicts) are worded by `web/refusals.js` from the codes and pairs
- Tests: `t()` and choosing the language are in `node scripts/ui-behavior-tests.cjs` (the `i18n:` items); switching the language in a browser is at the end of `ingress-check.cjs`. Browser checks set `locale: 'ja-JP'` on the context (Playwright's default is en-US, which shows the English strings)
