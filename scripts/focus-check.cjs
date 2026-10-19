'use strict';
// The terminal keeps the keyboard (desktop) in a real browser against
// `serve -fake`: through the page's polling, after a text selection, and
// after the menu, a panel or a dialog closes; and a reconnect never takes
// it from a field being typed in. Records every focus change (focusout with
// relatedTarget and the stack) and prints them when a check fails. Usage
// (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/focus-check.cjs
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-focus-'));
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
    const context = await browser.newContext({ viewport: { width: 1280, height: 860 }, locale: 'ja-JP' });
    const page = await context.newPage();
    page.on('pageerror', (e) => failures.push('page error: ' + e.message));
    // Every focus change, with what took it and from where.
    await page.addInitScript(() => {
      const log = window.focusLog = [];
      const name = (el) => !el ? 'null' : el === document.body ? 'body' : el.tagName.toLowerCase() + (el.id ? '#' + el.id : '') + (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).join('.') : '');
      const t0 = Date.now();
      const stack = () => new Error().stack.split('\n').slice(3, 6).map((l) => l.trim()).join(' < ');
      document.addEventListener('focusout', (e) => log.push((Date.now() - t0) + 'ms focusout ' + name(e.target) + ' -> ' + name(e.relatedTarget) + ' ' + stack()), true);
      for (const method of ['focus', 'blur']) {
        const original = HTMLElement.prototype[method];
        HTMLElement.prototype[method] = function (...args) { log.push((Date.now() - t0) + 'ms ' + method + '() ' + name(this) + ' ' + stack()); return original.apply(this, args); };
      }
      new MutationObserver((records) => {
        for (const r of records) for (const n of r.removedNodes) if (n.nodeType === 1 && (n.matches('.iw-input') || n.querySelector('.iw-input'))) log.push((Date.now() - t0) + 'ms removed ' + name(n) + ' holding the terminal input');
      }).observe(document, { childList: true, subtree: true });
    });
    let typed = '';
    let socketRoute = null;
    // The terminal's socket goes through the test, which can drop it.
    await page.routeWebSocket(/\/ws\//, (ws) => {
      const remote = ws.connectToServer();
      socketRoute = ws;
      ws.onMessage((m) => { if (typeof m !== 'string' && m[0] === 0x64) typed += m.subarray(1).toString(); remote.send(m); });
      remote.onMessage((m) => ws.send(m));
    });
    await page.goto(url);
    const card = page.locator('.terminal-card.selected');
    await card.waitFor();
    const start = card.getByRole('button', { name: '起動' });
    if (await start.isVisible() && await start.isEnabled()) { await start.click(); await page.waitForTimeout(1500); }
    const connect = card.getByRole('button', { name: /^接続/ });
    if (await connect.isVisible() && await connect.isEnabled()) await connect.click();
    await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
    await page.waitForTimeout(500);
    const active = () => page.evaluate(() => { const a = document.activeElement; return a === document.body ? 'body' : a ? a.tagName.toLowerCase() + '.' + a.className : 'null'; });
    const onTerminal = async () => (await active()) === 'textarea.iw-input';
    const screen = card.locator('.iw-screen');
    const more = card.locator('[aria-label="その他の操作"]');

    await card.locator('.iw-input').focus();
    // The sandbox list is polled every 10 s; output comes in meanwhile.
    for (let i = 0; i < 3; i++) { await page.keyboard.type('echo tick' + i); await page.keyboard.press('Enter'); await page.waitForTimeout(4000); }
    check(await onTerminal(), 'the terminal keeps the keyboard through polling and output (12 s)');

    // A selection leaves the focus on the terminal itself; typing still reaches the session.
    const box = await screen.boundingBox();
    await page.mouse.move(box.x + 10, box.y + 5);
    await page.mouse.down();
    await page.mouse.move(box.x + 200, box.y + 40, { steps: 5 });
    await page.mouse.up();
    const selected = await page.evaluate(() => String(getSelection()));
    check(selected.length > 0, 'a drag selects text in the terminal');
    typed = '';
    await page.keyboard.press('Control+c');
    await page.waitForTimeout(100);
    check(!typed.includes('\x03') && (await page.evaluate(() => String(getSelection()))) === selected, 'Ctrl+C with a selection copies it and sends nothing');
    await page.keyboard.type('echo after-select');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    check(typed.includes('echo after-select\r') && await onTerminal(), 'typing after selecting text reaches the session (' + JSON.stringify(typed) + ')');
    await page.mouse.move(box.x + 10, box.y + 5);
    await page.mouse.down();
    await page.mouse.move(box.x + 200, box.y + 40, { steps: 5 });
    await page.mouse.up();
    typed = '';
    await page.keyboard.press('ArrowUp');
    await page.waitForTimeout(100);
    check(typed === '\x1b[A' && await onTerminal(), 'a key the input handles itself (↑) after a selection reaches the session');

    // Choosing the tab already shown.
    await card.getByRole('tab', { name: /ターミナル/ }).click();
    check(await onTerminal(), 'clicking the terminal tab gives the terminal the keyboard');
    await card.getByRole('tab', { name: /ポリシー/ }).click();
    await page.waitForTimeout(3500);
    await card.getByRole('tab', { name: /ターミナル/ }).click();
    check(await onTerminal(), 'back from the policy tab, the terminal has the keyboard');

    // The menu, closed with Escape or ⋯.
    await more.click();
    await page.keyboard.press('Escape');
    check(await onTerminal(), 'Escape on the menu gives the keyboard back');
    await more.click();
    await more.click();
    check(await onTerminal(), 'closing the menu with ⋯ gives the keyboard back');

    // A panel from the menu.
    await more.click();
    await card.locator('[data-action=providers]').click();
    await page.waitForTimeout(800);
    await card.locator('.provider-panel').getByRole('button', { name: '閉じる' }).click();
    check(await onTerminal(), 'closing the providers panel gives the keyboard back');
    await more.click();
    await card.locator('[data-action=delete]').click();
    check((await active()) === 'input.text-input', 'the delete panel takes the keyboard for the name');
    await card.locator('.delete-panel').getByRole('button', { name: '閉じる' }).click();
    check(await onTerminal(), 'closing the delete panel gives the keyboard back');

    // The confirmation dialog: cancel, Escape, the backdrop.
    for (const [how, answer] of [['キャンセル', () => page.locator('#confirm-cancel').click()], ['Escape', () => page.keyboard.press('Escape')], ['the backdrop', () => page.locator('#confirm-panel').click({ position: { x: 5, y: 5 } })]]) {
      await more.click();
      await card.locator('[data-action=stop]').click();
      await page.locator('#confirm-panel').waitFor();
      await answer();
      await page.waitForTimeout(100);
      check(await page.locator('#confirm-panel').isHidden() && await onTerminal(), 'closing the confirmation with ' + how + ' gives the keyboard back');
    }

    // A reconnect gives the keyboard to the terminal only when nothing else has it.
    await page.locator('#create-button').click();
    await page.locator('#create-name').focus();
    socketRoute.close({ code: 4000, reason: 'test' });
    await card.locator('.connection-label[data-state=reconnecting]').waitFor({ state: 'attached' });
    await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached', timeout: 10000 });
    await page.waitForTimeout(300);
    check(await page.evaluate(() => document.activeElement?.id === 'create-name'), 'a reconnect leaves the keyboard in the field being typed in (' + await active() + ')');
    await page.locator('#create-close').click();
    await card.locator('.iw-screen').click({ position: { x: 300, y: 300 } });
    socketRoute.close({ code: 4000, reason: 'test' });
    await card.locator('.connection-label[data-state=reconnecting]').waitFor({ state: 'attached' });
    await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached', timeout: 10000 });
    await page.waitForTimeout(300);
    check(await onTerminal(), 'after a reconnect the terminal has the keyboard');

    if (failures.length) console.log((await page.evaluate(() => window.focusLog)).join('\n'));
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed'); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
