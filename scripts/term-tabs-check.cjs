'use strict';
// The terminal tabs in a real browser against `serve -fake`: they are
// numbered by place (the order the terminals started), not by name, so
// closing one renumbers those after it and a new one comes last, also after
// a reload; Alt+N picks the Nth. Saves screenshots in a temporary directory
// (printed at the end). Closing shows on the tab until it is gone. Usage (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/term-tabs-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-term-tabs-'));
const phone = process.argv.includes('--phone');
const failures = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) failures.push(what); };

(async () => {
  execFileSync('go', ['build', '-tags', 'uitest', '-o', path.join(dir, 'iwato'), '.'], { cwd: repo, stdio: 'inherit' });
  const server = spawn(path.join(dir, 'iwato'), ['serve', '-fake', '-listen', '127.0.0.1:0'], { cwd: dir });
  const url = await new Promise((resolve, reject) => {
    let out = '';
    server.stdout.on('data', (d) => { out += d; const m = out.match(/(http:\/\/127\.0\.0\.1:\d+\/)(#token=[0-9a-f]+)/); if (m) resolve(m[1] + m[2]); });
    server.on('exit', () => reject(new Error('serve -fake exited: ' + out)));
  });
  const browser = await chromium.launch();
  try {
    const context = await browser.newContext(phone
      ? { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2, locale: 'ja-JP' }
      : { viewport: { width: 1280, height: 860 }, locale: 'ja-JP' });
    const page = await context.newPage();
    page.on('pageerror', (e) => failures.push('page error: ' + e.message));
    const shot = (name) => page.screenshot({ path: path.join(dir, name) });
    await page.goto(url);
    const card = page.locator('.terminal-card.selected');
    await page.locator('#sandbox-nav .nav-sandbox', { has: page.locator('.nav-name', { hasText: /^dev$/ }) }).click();
    await card.locator('.card-title', { hasText: 'dev' }).waitFor();
    const startButton = card.getByRole('button', { name: '起動' });
    if (await startButton.isVisible()) await startButton.click();
    await card.locator('.badge[data-state=running]').waitFor({ timeout: 20000 });
    const tabs = () => card.locator('.term-select').evaluateAll((list) => list.map((b) => b.firstChild.textContent));
    const selected = () => card.locator('.term-tab[aria-selected=true] .term-select').evaluate((b) => b.firstChild.textContent);
    const until = async (want, what) => {
      let got;
      for (let i = 0; i < 50; i++) {
        got = (await tabs()).join(',');
        if (got === want) break;
        await page.waitForTimeout(200);
      }
      check(got === want, what + ' (' + got + ')');
    };
    const add = async () => { await card.locator('.term-add').click(); };
    // A terminal's text tells it apart: what is typed into it.
    const mark = async (text) => {
      await card.locator('.terminal-host').click();
      await page.keyboard.type('echo ' + text + '\n');
      await card.locator('.terminal-host', { hasText: text }).first().waitFor({ timeout: 10000 });
    };

    await mark('first-terminal');
    await add();
    await until('1,2', 'a second terminal is 2');
    await mark('second-terminal');
    await shot('two.png');

    // Close 1: it shows it is closing until the list comes back; then the
    // second becomes 1. The fake ends at once: hold the request a moment.
    await page.route('**/terminals/*/kill', async (route) => { await new Promise((r) => setTimeout(r, 1500)); await route.continue(); });
    await card.locator('.term-tab').first().locator('.term-close').click();
    await page.locator('#confirm-ok').click();
    const closing = card.locator('.term-tab.closing');
    await closing.waitFor({ timeout: 2000 });
    check(await closing.locator('.term-spinner').count() === 1 && await closing.locator('.term-close').isDisabled() && (await closing.locator('.term-select').evaluate((b) => b.firstChild.textContent)) === '1', 'the tab being closed shows a spinner and cannot be pressed');
    await shot('closing.png');
    await until('1', 'closing 1 leaves one tab, numbered 1');
    check(await card.locator('.term-tab.closing').count() === 0, 'no tab is left closing');
    await page.unroute('**/terminals/*/kill');
    check(await card.locator('.terminal-host', { hasText: 'second-terminal' }).count() > 0, 'the one left is the second terminal');
    await shot('closed.png');

    // A new one comes after it, as 2, also after a reload.
    await add();
    await until('1,2', 'a new terminal is 2');
    check(await selected() === '2', 'the new terminal is shown');
    await page.reload();
    await card.locator('.badge[data-state=running]').waitFor({ timeout: 20000 });
    await until('1,2', 'after a reload the order stays');
    if (!phone) {
      await page.keyboard.press('Alt+1');
      await card.locator('.terminal-host', { hasText: 'second-terminal' }).first().waitFor({ timeout: 10000 });
      check(await selected() === '1', 'Alt+1 shows tab 1, the second terminal');
    }
    await shot('reopened.png');
  } finally {
    await browser.close();
    server.kill();
    console.log('screenshots in ' + dir);
  }
  if (failures.length) {
    console.log('FAILED: ' + failures.length);
    process.exit(1);
  }
})().catch((error) => { console.error(error); process.exit(1); });
