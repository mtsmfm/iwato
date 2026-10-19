'use strict';
// Renders the app icons (web/icon-180/192/512.png) from web/icon.svg, the
// mark with the margin that Android's round mask needs. web/favicon.svg (the
// same mark cropped close, for tabs and the sidebar) is used as it is.
// Usage (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/render-icons.cjs
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');

const web = path.join(__dirname, '..', 'web');
const svg = fs.readFileSync(path.join(web, 'icon.svg'), 'utf8');

(async () => {
  const browser = await chromium.launch();
  try {
    for (const size of [180, 192, 512]) {
      const page = await browser.newPage({ viewport: { width: size, height: size } });
      await page.setContent(`<style>*{margin:0}svg{display:block;width:${size}px;height:${size}px}</style>${svg}`);
      await page.screenshot({ path: path.join(web, `icon-${size}.png`), omitBackground: true });
      await page.close();
      console.log(`web/icon-${size}.png`);
    }
  } finally {
    await browser.close();
  }
})().catch((e) => { console.error(e); process.exit(1); });
