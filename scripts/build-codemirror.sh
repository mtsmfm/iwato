#!/usr/bin/env bash
# Builds web/vendor/codemirror.js (the editor tab's CodeMirror 6, web/editor.js)
# from scripts/codemirror: the pinned packages of its package-lock.json
# (npm ci, no scripts run but esbuild's own) and entry.mjs, bundled and
# minified by esbuild into one script that sets window.IwatoCodeMirror.
# The built file is committed; run this after changing the packages or the
# entry, then update the hash in web/vendor/manifest.json (printed below).
# Needs Node (mise.toml) and the npm registry (npm_config_registry picks
# another one).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
src="$here/codemirror"
out="$here/../web/vendor/codemirror.js"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cp "$src/package.json" "$src/package-lock.json" "$src/entry.mjs" "$work/"
cd "$work"
npm ci --no-audit --no-fund --ignore-scripts
# esbuild's binary comes from its platform package (no install script run).
node node_modules/esbuild/bin/esbuild entry.mjs --bundle --minify --format=iife --global-name=IwatoCodeMirror \
  --target=es2020 --legal-comments=none --charset=utf8 --outfile=bundle.js
{
  printf '/*! CodeMirror 6 for Iwato (scripts/build-codemirror.sh). MIT licensed: web/vendor/LICENSE-codemirror.txt. Packages: '
  node -e 'const l=require("./package-lock.json").packages;console.log(Object.keys(l).filter(k=>k&&!/esbuild/.test(k)).map(k=>k.replace(/^node_modules\//,"")+"@"+l[k].version).join(", "))' | tr -d '\n'
  printf ' */\n'
  cat bundle.js
} > "$out"
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
let out = "Licenses of the packages bundled in web/vendor/codemirror.js (scripts/build-codemirror.sh).\n";
for (const [text, names] of groups) out += "\n" + "=".repeat(72) + "\n" + names.join(", ") + "\n\n" + text + "\n";
fs.writeFileSync(process.argv[1], out);
' "$here/../web/vendor/LICENSE-codemirror.txt"
echo "wrote $out ($(wc -c < "$out") bytes)"
sha256sum "$out"
