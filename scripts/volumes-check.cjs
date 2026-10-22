'use strict';
// The volume panel in a real browser against `serve -fake`: the volumes by
// sandbox, removing a leftover one after the warning, and the note in the
// create form. Saves screenshots in a temporary directory (printed at the
// end). Usage (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/volumes-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-volumes-'));
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
    page.on('console', (m) => { if (m.type() === 'error') console.log('console: ' + m.text()); });
    await page.goto(url);
    await page.locator('.terminal-card.selected').waitFor();
    const menu = async (id) => {
      if (phone) await page.click('#menu-button');
      await page.click(id);
    };
    await menu('#volume-button');
    const panel = page.locator('#volume-panel');
    const group = (name) => panel.locator('.volume-group', { has: page.locator('.volume-sandbox', { hasText: name }) });
    await group('dev').waitFor();
    const names = await panel.locator('.volume-sandbox').allInnerTexts();
    check(names.length === 3 && names[0].startsWith('dev') && names[1].startsWith('old-app') && names[2].startsWith('scratch'), 'the volumes are grouped by sandbox (' + names.map((n) => n.replace(/\n/g, ' ')).join(', ') + ')');
    check((await group('dev').innerText()).includes('使用中: dev') && await group('dev').getByRole('button', { name: '削除' }).count() === 0, 'a volume in use cannot be removed');
    const old = await group('old-app').innerText();
    check(old.includes('削除済み') && old.includes('/sandbox/data') && old.includes('/sandbox/.cache') && /作成/.test(old), 'a deleted sandbox\'s volumes show their paths and when they were made');
    check((await group('scratch').innerText()).includes('パス不明'), 'a volume without the path label shows パス不明');
    check(await panel.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the panel does not scroll sideways');
    await page.screenshot({ path: path.join(dir, 'volumes.png') });

    await group('scratch').getByRole('button', { name: '削除' }).click();
    await page.locator('#confirm-panel').waitFor();
    const question = await page.locator('#confirm-text').innerText();
    check(question.includes('iwato-personal-scratch-persist-') && question.includes('元に戻せません'), 'the warning names the volume and says it cannot be undone');
    await page.screenshot({ path: path.join(dir, 'volume-confirm.png') });
    await page.click('#confirm-ok');
    await group('scratch').waitFor({ state: 'detached' });
    check(await panel.locator('.volume-group').count() === 2, 'the removed volume is gone');
    await group('old-app').getByRole('button', { name: '削除' }).first().click();
    await page.locator('#confirm-panel').waitFor();
    await page.keyboard.press('Escape');
    check(await page.locator('#confirm-panel').isHidden() && await panel.isVisible() && await panel.locator('.volume-group').count() === 2, 'Escape answers only the confirmation, as キャンセル');
    await page.keyboard.press('Escape');
    check(await panel.isHidden(), 'Escape then closes the panel');

    await menu('#create-button');
    await page.fill('#create-name', 'old-app');
    await page.locator('#create-volumes', { hasText: '/sandbox/data' }).waitFor();
    check((await page.locator('#create-volumes').innerText()).includes('/sandbox/.cache, /sandbox/data'), 'the create form names the leftover paths for the name');
    await page.screenshot({ path: path.join(dir, 'create-hint.png') });
    await page.fill('#create-name', 'other');
    check(await page.locator('#create-volumes').isHidden(), 'no note for a name without leftovers');
    console.log('screenshots in ' + dir);
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed'); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
