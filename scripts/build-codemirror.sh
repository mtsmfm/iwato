#!/usr/bin/env bash
# Builds the editor tab's vendored scripts (web/editor.js) from
# scripts/codemirror: the pinned packages of its package-lock.json (npm ci,
# no scripts run but esbuild's own), bundled and minified by esbuild into
# web/vendor/codemirror.js (CodeMirror 6, entry.mjs, window.IwatoCodeMirror)
# and web/vendor/markdown.js (marked, markdown.mjs, window.IwatoMarkdown).
# The built files are committed; run this after changing the packages or an
# entry, then update the hashes in web/vendor/manifest.json (printed below).
# Needs Node (mise.toml) and the npm registry (npm_config_registry picks
# another one).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
src="$here/codemirror"
vendor="$here/../web/vendor"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp "$src/package.json" "$src/package-lock.json" "$src/entry.mjs" "$src/markdown.mjs" "$work/"
cd "$work"
npm ci --no-audit --no-fund --ignore-scripts
# bundle ENTRY GLOBAL OUT TITLE PACKAGES: PACKAGES is a regexp of the lock's
# packages named in the file's first line.
bundle() {
  # esbuild's binary comes from its platform package (no install script run).
  node node_modules/esbuild/bin/esbuild "$1" --bundle --minify --format=iife --global-name="$2" \
    --target=es2020 --legal-comments=none --charset=utf8 --outfile=bundle.js
  {
    printf '/*! %s for Iwato (scripts/build-codemirror.sh). MIT licensed: web/vendor/LICENSE-codemirror.txt. Packages: ' "$4"
    node -e 'const l=require("./package-lock.json").packages,re=new RegExp(process.argv[1]);console.log(Object.keys(l).filter(k=>k&&!/esbuild/.test(k)&&re.test(k)).map(k=>k.replace(/^node_modules\//,"")+"@"+l[k].version).join(", "))' "$5" | tr -d '\n'
    printf ' */\n'
    cat bundle.js
  } > "$vendor/$3"
  echo "wrote $vendor/$3 ($(wc -c < "$vendor/$3") bytes)"
}
bundle entry.mjs IwatoCodeMirror codemirror.js 'CodeMirror 6' '^(?!node_modules/marked$)'
bundle markdown.mjs IwatoMarkdown markdown.js 'marked' '^node_modules/marked$'
# The license notices of the bundled packages, grouped by text.
node -e '
const fs = require("fs"), l = require("./package-lock.json").packages, groups = new Map();
for (const k of Object.keys(l).filter((k) => k && !/esbuild/.test(k))) {
  const dir = k, name = k.replace(/^node_modules\//, "") + "@" + l[k].version;
  const file = fs.readdirSync(dir).find((f) => /^licen[cs]e/i.test(f));
  const text = fs.readFileSync(dir + "/" + file, "utf8").trim();
  if (!groups.has(text)) groups.set(text, []);
  groups.get(text).push(name);
}
let out = "Licenses of the packages bundled in web/vendor/codemirror.js and markdown.js (scripts/build-codemirror.sh).\n";
for (const [text, names] of groups) out += "\n" + "=".repeat(72) + "\n" + names.join(", ") + "\n\n" + text + "\n";
fs.writeFileSync(process.argv[1], out);
' "$vendor/LICENSE-codemirror.txt"
cd "$vendor" && sha256sum codemirror.js markdown.js
