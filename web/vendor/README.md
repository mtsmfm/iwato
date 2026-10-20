# Vendored web assets

## Terminal font

- Moralerspace **2.0.0** (`MoralerspaceNeonHW-Regular.ttf` and `-Bold.ttf` from `MoralerspaceHW_v2.0.0.zip`, saved as `fonts/moralerspace-neon-hw-2.0.0-*.ttf`) — https://github.com/yuru7/moralerspace/releases/tag/v2.0.0

A pinned stable release. The font is the terminal's (`web/term.js`): half-width 1 : full-width 2 Japanese with Nerd Font icons (eza, starship). The production page makes no CDN, font service, analytics, or other third-party resource requests. The font and its original license notice are served from the same embedded filesystem as the application.

`manifest.json` records the font archive SHA-256 and SHA-256 hashes of the vendored files. `LICENSE-moralerspace.txt` must stay with redistributions.

To reproduce, download the pinned font release zip and extract the two Neon HW files. `scripts/fetch-vendor.py` does that and checks every hash.

## CodeMirror 6 (the editor tab)

`codemirror.js` is CodeMirror 6 with its Lezer grammars and legacy modes, bundled and minified into one script (it sets `window.IwatoCodeMirror`) for `web/editor.js`, which loads it the first time an editor panel shows. Unlike the font, the built file is committed. All packages are MIT licensed (Marijn Haverbeke and others); their notices are in `LICENSE-codemirror.txt`, which must stay with redistributions. The exact versions are in `manifest.json` (`scripts`) and in the first line of the file.

`markdown.js` is marked (MIT), bundled the same way from `scripts/codemirror/markdown.mjs` (it sets `window.IwatoMarkdown`) for the editor's Markdown preview, loaded the first time a preview shows; its notice is in `LICENSE-codemirror.txt` too.

To reproduce, run `scripts/build-codemirror.sh`: `npm ci` of the pinned `scripts/codemirror/package-lock.json` (no install scripts), then esbuild on `scripts/codemirror/entry.mjs`. The same lock gives the same files (their SHA-256 are in `manifest.json`). To update, change the versions in `scripts/codemirror/` (`npm install --save-exact` there), build, and update the hash. `fetch-vendor.py` does not touch it.
