'use strict';
// The editor and the terminal side by side in a real browser against
// `serve -fake`: the toggle only on wide cards (none on phones), both panes
// shown with both tabs current, the policy and ports tabs taking their place and the
// split coming back, the terminal's columns following the divider with few
// resize messages, each pane keeping the keyboard and its shortcuts, the
// choice remembered, and the tabs again when the window gets narrow.
// Usage (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/split-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-split-'));
const phone = process.argv.includes('--phone');
const failures = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) failures.push(what); };

// A resize message: its kind byte, then cols and rows as uvarints.
function resizeCols(m) {
  let value = 0, shift = 0;
  for (let i = 1; i < m.length; i++) {
    value |= (m[i] & 0x7f) << shift;
    if (!(m[i] & 0x80)) return value;
    shift += 7;
  }
  return 0;
}

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
    const resizes = [];
    let typed = '';
    await page.routeWebSocket(/\/ws\//, (ws) => {
      const remote = ws.connectToServer();
      ws.onMessage((m) => {
        if (typeof m !== 'string' && m[0] === 0x72) resizes.push(resizeCols(m));
        if (typeof m !== 'string' && m[0] === 0x64) typed += m.subarray(1).toString();
        remote.send(m);
      });
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
    const toggle = card.locator('.split-toggle');
    const shot = (name) => page.screenshot({ path: path.join(dir, name) });

    if (phone) {
      check(!(await toggle.isVisible()), 'phones have no side-by-side toggle');
      await card.getByRole('tab', { name: 'エディタ' }).click();
      await page.waitForTimeout(300);
      check(await card.locator('.editor-panel').isVisible() && !(await card.locator('.terminal-surface').isVisible()), 'on a phone the editor tab replaces the terminal');
      return;
    }

    check(await toggle.isVisible(), 'a wide card offers the side-by-side toggle');
    check(await toggle.getAttribute('aria-label') === '並べて表示', 'the toggle is labelled');
    await toggle.click();
    await page.waitForTimeout(400);
    const editor = card.locator('.editor-panel');
    const surface = card.locator('.terminal-surface');
    const box = async (l) => l.boundingBox();
    const both = async () => await editor.isVisible() && await surface.isVisible();
    check(await both(), 'side by side shows the editor and the terminal');
    const eb = await box(editor), sb = await box(surface);
    check(eb && sb && eb.x + eb.width <= sb.x + 1 && Math.abs(eb.y - sb.y) < 60, 'the editor is on the left, the terminal on the right');
    const selected = await card.locator('.card-tab[aria-selected=true]').allTextContents();
    check(selected.some((s) => s.startsWith('ターミナル')) && selected.some((s) => s.startsWith('エディタ')), 'both tabs show as current');
    check(await page.evaluate(() => document.activeElement?.classList.contains('iw-input')), 'the terminal keeps the keyboard when the split opens');
    await shot('split.png');

    // The divider: the terminal follows it once it rests.
    await page.waitForTimeout(400);
    const before = resizes.length ? resizes[resizes.length - 1] : 0;
    const countBefore = resizes.length;
    const divider = card.locator('.split-divider');
    const db = await box(divider);
    await page.mouse.move(db.x + db.width / 2, db.y + db.height / 2);
    await page.mouse.down();
    for (let i = 1; i <= 20; i++) { await page.mouse.move(db.x + db.width / 2 - i * 10, db.y + db.height / 2); await page.waitForTimeout(16); }
    await page.mouse.up();
    await page.waitForTimeout(600);
    const after = resizes[resizes.length - 1];
    check(resizes.length > countBefore && after > before, 'dragging the divider widens the terminal (' + before + ' → ' + after + ' columns)');
    check(resizes.length - countBefore <= 3, 'dragging sends few resize messages (' + (resizes.length - countBefore) + ')');
    const saved = await page.evaluate(() => JSON.parse(localStorage.getItem('iwato-split')));
    check(saved && saved.on === true && saved.at < 0.5, 'the split and the divider are remembered');

    // Each pane keeps the keyboard and its shortcuts.
    await card.locator('.iw-screen').click({ position: { x: 100, y: 100 } });
    typed = '';
    await page.keyboard.type('xy');
    await page.waitForTimeout(200);
    check(typed.includes('xy'), 'typing in the clicked terminal goes to the terminal');
    await editor.locator('.editor-empty:visible, .editor-text:visible').first().click();
    await page.keyboard.press('Control+p');
    await page.waitForTimeout(300);
    check(await editor.locator('.editor-quick').isVisible(), 'Ctrl+P in the editor pane opens quick open');
    await page.keyboard.type('mngo');
    await page.waitForFunction(() => /main\.go$/.test(document.querySelector('.terminal-card.selected .editor-quick-item')?.textContent || ''));
    await page.keyboard.press('Enter');
    await editor.locator('.cm-content').waitFor();
    await page.waitForTimeout(300);
    typed = '';
    await page.keyboard.type('zq');
    await page.waitForTimeout(200);
    check((await editor.locator('.cm-content').textContent()).includes('zq') && !typed.includes('zq'), 'typing in the editor pane goes to the file');
    const cm = await box(editor.locator('.cm-editor')), dv = await box(divider);
    check(cm && dv && cm.x + cm.width <= dv.x + 2, 'the text fits the editor pane (' + Math.round(cm.x + cm.width) + ' ≤ ' + Math.round(dv.x) + ')');
    await card.locator('.iw-screen').click({ position: { x: 100, y: 100 } });
    typed = '';
    await page.keyboard.press('Control+p');
    await page.waitForTimeout(300);
    check(!(await editor.locator('.editor-quick').isVisible()) && typed.includes('\x10'), 'Ctrl+P in the terminal pane goes to the terminal');

    // Choosing a tab gives its pane the keyboard, even where a click does
    // not focus the button (Safari, Firefox on macOS: a plain click()).
    const tabClick = (name) => page.evaluate((name) => [...document.querySelectorAll('.terminal-card.selected .card-tab')].find((b) => b.textContent.startsWith(name)).click(), name);
    await editor.locator('.cm-content').click();
    await tabClick('ターミナル');
    await page.waitForTimeout(200);
    check(await page.evaluate(() => document.activeElement?.classList.contains('iw-input')), 'the terminal tab takes the keyboard from the editor');
    await tabClick('エディタ');
    await page.waitForTimeout(200);
    check(await page.evaluate(() => !!document.activeElement?.closest('.editor-panel')), 'the editor tab takes the keyboard from the terminal');

    // Another tab replaces the split; returning brings it back.
    await card.getByRole('tab', { name: /^ポリシー/ }).click();
    await page.waitForTimeout(300);
    check(!(await editor.isVisible()) && !(await surface.isVisible()) && await card.locator('.policy-panel').isVisible(), 'the policy tab replaces the split');
    // Its YAML view fills the tab there too.
    const policyPanel = card.locator('.policy-panel');
    await policyPanel.locator('[data-seg=yaml]').click();
    const yamlEditor = policyPanel.locator('.policy-yaml-editor .cm-editor');
    await yamlEditor.waitFor();
    const [ed, pp] = [await yamlEditor.boundingBox(), await policyPanel.boundingBox()];
    check(ed.y + ed.height <= pp.y + pp.height + 1 && ed.height > pp.height * 0.5 && ed.y + ed.height <= page.viewportSize().height, 'the policy YAML fills the tab in the split card');
    await shot('split-policy-yaml.png');
    await policyPanel.locator('[data-seg=list]').click();
    await card.getByRole('tab', { name: 'エディタ' }).click();
    await page.waitForTimeout(400);
    check(await both(), 'returning to the editor brings the split back');
    await card.getByRole('tab', { name: 'ポート' }).click();
    await page.waitForTimeout(300);
    check(!(await editor.isVisible()) && !(await surface.isVisible()) && await card.locator('.ports-panel').isVisible(), 'the ports tab replaces the split');
    await card.getByRole('tab', { name: 'ターミナル' }).click();
    await page.waitForTimeout(400);
    check(await both(), 'returning to the terminal brings the split back');

    // Remembered after a reload.
    await page.reload();
    await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
    await page.waitForTimeout(500);
    check(await both(), 'the split is still on after a reload');
    const ratio = await page.evaluate(() => { const c = document.querySelector('.terminal-card.selected'); return c.querySelector('.editor-panel').getBoundingClientRect().width / c.getBoundingClientRect().width; });
    check(Math.abs(ratio - saved.at) < 0.03, 'the divider is where it was left (' + ratio.toFixed(2) + ')');

    // A narrow window falls back to the tabs, and back again.
    await page.setViewportSize({ width: 820, height: 860 });
    await page.waitForTimeout(500);
    check(!(await toggle.isVisible()) && !(await both()), 'a narrow card falls back to the tabs');
    await page.setViewportSize({ width: 1280, height: 860 });
    await page.waitForTimeout(500);
    check(await toggle.isVisible() && await both(), 'widening brings the split back');

    await toggle.click();
    await page.waitForTimeout(300);
    check(!(await both()) && await toggle.getAttribute('aria-pressed') === 'false', 'the toggle turns the split off');
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed'); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
