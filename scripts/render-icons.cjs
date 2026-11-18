'use strict';
// Renders the app icons (web/icon-180/192/512.png) from web/icon.svg, the
// mark with the margin that Android's round mask needs, and the
// notifications' badge (web/badge-96.png) from web/badge.svg: Android keeps
// only a badge's alpha, so it is the mark's shape on transparency (the torii
// enlarged to read at 24 px). web/favicon.svg (the same mark cropped close,
// for tabs and the sidebar) is used as it is.
// Usage (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/render-icons.cjs
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const web = path.join(__dirname, '..', 'web');
const svg = fs.readFileSync(path.join(web, 'icon.svg'), 'utf8');
const badge = fs.readFileSync(path.join(web, 'badge.svg'), 'utf8');

(async () => {
  const browser = await chromium.launch();
  try {
    for (const [source, size, out] of [[svg, 180, 'icon-180.png'], [svg, 192, 'icon-192.png'], [svg, 512, 'icon-512.png'], [badge, 96, 'badge-96.png']]) {
      const page = await browser.newPage({ viewport: { width: size, height: size } });
      await page.setContent(`<style>*{margin:0}svg{display:block;width:${size}px;height:${size}px}</style>${source}`);
      await page.screenshot({ path: path.join(web, out), omitBackground: true });
      await page.close();
      console.log('web/' + out);
    }
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
