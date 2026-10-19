'use strict';
// Does opening the on-screen keyboard leave the terminal rows alone? Fewer
// rows make Claude Code redraw its last rows while the terminal keeps the
// rows it pushed into the scrollback, so they show twice once the keyboard
// closes (web/app.js holdRows). Runs `serve -fake` and a phone-sized
// Chromium whose visualViewport is replaced to open and close a keyboard,
// then checks that no resize reaches the server, the rows stay and the
// cursor shows above the keyboard. Usage (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/keyboard-rows-check.cjs
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-keyboard-'));
const failures = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) failures.push(what); };

(async () => {
  execFileSync('go', ['build', '-tags', 'uitest', '-o', path.join(dir, 'iwato'), '.'], { cwd: repo, stdio: 'inherit' });
  const server = spawn(path.join(dir, 'iwato'), ['serve', '-fake', '-listen', '127.0.0.1:0'], { cwd: dir });
  const url = await new Promise((resolve, reject) => {
    let out = '';
    server.stdout.on('data', (d) => { out += d; const m = out.match(/http:\/\/127\.0\.0\.1:\d+\/#token=[0-9a-f]+/); if (m) resolve(m[0]); });
    server.on('exit', () => reject(new Error('serve -fake exited: ' + out)));
  });
  const browser = await chromium.launch();
  try {
    const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2, locale: 'ja-JP' });
    // A visual viewport the test moves: the keyboard covers the page's bottom.
    await context.addInitScript(() => {
      const listeners = {};
      const vv = { scale: 1, offsetTop: 0, offsetLeft: 0, width: innerWidth, height: innerHeight,
        addEventListener(name, fn) { (listeners[name] = listeners[name] || []).push(fn); }, removeEventListener() {} };
      Object.defineProperty(window, 'visualViewport', { value: vv });
      window.setKeyboard = (px) => { vv.height = innerHeight - px; for (const fn of listeners.resize || []) fn(); };
    });
    const page = await context.newPage();
    page.on('pageerror', (e) => failures.push('page error: ' + e.message));
    const resizes = [];
    // Resize messages (internal/termsync): 'r', then uvarint cols and rows.
    const uvarints = (b) => { const out = []; let v = 0, m = 1; for (const x of b) { v += (x & 0x7f) * m; m *= 128; if (x < 0x80) { out.push(v); v = 0; m = 1; } } return out; };
    page.on('websocket', (ws) => ws.on('framesent', (f) => { const p = f.payload; if (typeof p !== 'string' && p[0] === 0x72) { const [cols, rows] = uvarints(p.subarray(1)); resizes.push(cols + 'x' + rows); } }));
    await page.goto(url);
    const card = page.locator('.terminal-card.selected');
    await card.waitFor();
    const start = card.getByRole('button', { name: '起動' });
    if (await start.isVisible() && await start.isEnabled()) { await start.click(); await page.waitForTimeout(1500); }
    // A running sandbox connects when it is shown.
    const connect = card.getByRole('button', { name: /^接続/ });
    if (await connect.isVisible() && await connect.isEnabled()) await connect.click();
    await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
    await page.waitForTimeout(500);
    const textarea = card.locator('.iw-input');
    await textarea.focus();
    const settle = () => page.waitForTimeout(400);
    const size = () => card.locator('.dimensions').innerText();
    // The cursor's row.
    const cursorShown = () => page.evaluate(() => {
      const t = document.querySelector('.terminal-card.selected .iw-cursor').getBoundingClientRect();
      const s = document.querySelector('.terminal-card.selected .terminal-surface').getBoundingClientRect();
      const bottom = visualViewport.offsetTop + visualViewport.height;
      return t.top >= s.top - 1 && t.top + t.height <= Math.min(s.bottom, bottom) + 1;
    });
    const typeLine = async (text) => { await page.keyboard.type(text); await page.keyboard.press('Enter'); };

    // A prompt on the last row (the screen full of output), then near the top (after clear).
    for (let i = 1; i <= 60; i++) await typeLine('echo line ' + i);
    await settle();
    const before = await size();
    const sent = resizes.length;
    for (const [label, prepare] of [['a prompt on the last row', async () => {}], ['a prompt near the top', () => typeLine('clear')]]) {
      await page.evaluate(() => setKeyboard(0)); await prepare(); await settle();
      await page.evaluate(() => setKeyboard(380)); await settle();
      check(await page.evaluate(() => document.getElementById('app-shell').classList.contains('keyboard-open')), label + ': the keyboard layout is on');
      check(await size() === before, label + ': the keyboard keeps the rows (' + before + ', now ' + await size() + ')');
      check(await cursorShown(), label + ': the cursor shows above the keyboard');
      await typeLine('echo typed'); await settle();
      check(await cursorShown(), label + ': the cursor still shows after typing');
      await page.evaluate(() => setKeyboard(0)); await settle();
      check(await size() === before, label + ': closing the keyboard keeps the rows');
    }
    check(resizes.length === sent || resizes.slice(sent).every((s) => s === before.replace(/\s/g, '').replace('×', 'x')), 'no other size reached the server (sent: ' + (resizes.slice(sent).join(', ') || 'none') + ')');
    await page.screenshot({ path: path.join(dir, 'after.png') });
  } finally {
    await browser.close();
    server.kill();
  }
  fs.rmSync(dir, { recursive: true, force: true });
  if (failures.length) { console.log(failures.length + ' failed'); process.exitCode = 1; } else console.log('keyboard keeps the terminal rows');
})().catch((e) => { console.error(e); process.exitCode = 1; });
