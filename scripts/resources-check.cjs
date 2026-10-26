'use strict';
// The resources in a real browser against `serve -fake` (fake_resources.go):
// the header's row on one line (short values and no scrollbar on a phone),
// the Resources tab's sections, the sidebar's line and totals, a stopped
// sandbox's home, the volumes' sizes and the requests while idle. Saves
// screenshots in a temporary directory (printed at the end). Usage (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/resources-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-resources-'));
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
    const requests = [];
    page.on('request', (r) => requests.push({ at: Date.now(), path: new URL(r.url()).pathname }));
    const shot = (name) => page.screenshot({ path: path.join(dir, name) });
    await page.goto(url);
    const nav = (name) => page.locator('#sandbox-nav .nav-sandbox', { has: page.locator('.nav-name', { hasText: new RegExp('^' + name + '$') }) });
    const card = page.locator('.terminal-card.selected');
    const start = async (name) => {
      await nav(name).click();
      await card.locator('.card-title', { hasText: name }).waitFor();
      await card.getByRole('button', { name: '起動' }).click();
      await card.locator('.badge[data-state=running]').waitFor({ timeout: 20000 });
    };
    // experiment has the GPU (fake_serve.go); dev is shown last.
    await start('experiment');
    await start('dev');
    const row = card.locator('.res-row');
    await row.locator('.res-cpu', { hasText: /\d+%/ }).waitFor({ timeout: 15000 });
    const header = card.locator('.card-header');
    const headerBox = await header.boundingBox();
    const tops = await header.evaluate((h) => [...h.children].filter((c) => c.offsetParent).map((c) => Math.round(c.getBoundingClientRect().top + c.getBoundingClientRect().height / 2)));
    check(Math.max(...tops) - Math.min(...tops) <= 2 && headerBox.height < 48, 'the header stays one line (' + headerBox.height + 'px, centres ' + tops.join(',') + ')');
    const shown = await row.evaluate((r) => ({ long: getComputedStyle(r.querySelector('.res-long')).display, mini: r.querySelector('.res-mini') && getComputedStyle(r.querySelector('.res-mini')).display, bar: r.offsetHeight - r.clientHeight - 2 * parseFloat(getComputedStyle(r).borderTopWidth), text: r.innerText.replace(/\n/g, ' ') }));
    if (phone) check(shown.long === 'none' && shown.mini === 'none' && shown.bar === 0, 'narrow: short values, no bars, no scrollbar (' + shown.text + ')');
    else check(shown.long !== 'none' && shown.mini !== 'none' && /CPU \d+%/.test(shown.text) && /メモリ \d+\.\d\/8\.0 GB/.test(shown.text), 'wide: CPU and memory with bars (' + shown.text + ')');
    await shot('header.png');
    const lefts = new Set();
    for (let i = 0; i < 4; i++) { lefts.add(Math.round(await card.locator('.card-header .badge[data-state=running]').evaluate((b) => b.getBoundingClientRect().left))); await page.waitForTimeout(4500); }
    check(lefts.size === 1, 'the state badge stays put as the numbers change (' + [...lefts].join(',') + ')');

    await row.click();
    const panel = card.locator('.res-panel');
    await panel.locator('.res-spark').first().waitFor({ timeout: 15000 });
    const heads = await panel.locator('.res-head').evaluateAll((list) => list.map((h) => h.firstChild.textContent));
    check(heads.join(',') === 'CPU,メモリ,ホーム', 'the Resources tab shows CPU, memory and the home (' + heads.join(',') + ')');
    check(await panel.locator('.res-legend span').count() === 3, 'the memory breakdown: used, cache, free');
    await panel.locator('.res-folders tr').first().waitFor({ timeout: 15000 });
    check((await panel.locator('.res-folder').allInnerTexts()).includes('~/work/iwato'), 'the home\'s largest folders');
    check(await panel.evaluate((p) => p.scrollWidth <= p.clientWidth + 1), 'the tab does not scroll sideways');
    await shot('tab.png');

    await nav('experiment').click();
    await card.locator('.res-row .res-gpu').waitFor({ timeout: 15000 });
    await card.locator('.res-row').click();
    await card.locator('.res-panel .res-warn').waitFor({ timeout: 15000 });
    check((await card.locator('.res-panel .res-head').last().innerText()).includes('GPU 全体'), 'the GPU is labelled GPU-wide, with the WSL note');
    await shot('gpu.png');

    if (!phone) {
      const totals = page.locator('#res-totals');
      check(await totals.isVisible() && (await totals.innerText()).includes('合計（動作中 2 台）') && /\/ 16 コア/.test(await totals.innerText()) && /\/ 32\.0 GB/.test(await totals.innerText()), 'the sidebar totals against the PC (' + (await totals.innerText()).replace(/\n/g, ' ') + ')');
      check(await page.locator('#sandbox-nav .nav-res').count() === 0, 'no resource line under the sandbox names');
      // Idle: the page polls; count a minute's requests.
      await page.waitForTimeout(2000);
      const from = Date.now();
      await page.waitForTimeout(60000);
      const minute = requests.filter((r) => r.at >= from);
      const by = {};
      for (const r of minute) by[r.path] = (by[r.path] || 0) + 1;
      console.log('requests in a minute at idle: ' + JSON.stringify(by));
      check((by['/api/resources'] || 0) <= 16, 'the resources are read at most every 4 seconds');
    }

    // The settings: a low amber turns the memory amber; hiding hides the row.
    const settings = async () => { if (phone) await page.click('#menu-button'); await page.click('#settings-button'); await page.locator('.res-settings').waitFor(); };
    await settings();
    const section = page.locator('.res-settings');
    await section.getByLabel('赤にする割合').fill('99');
    await section.getByLabel('赤にする割合').dispatchEvent('change');
    await section.getByLabel('黄にする割合').fill('10');
    await section.getByLabel('黄にする割合').dispatchEvent('change');
    await section.getByText('保存しました。').waitFor();
    await page.click('#settings-close');
    await card.locator('.res-row .res-memory.warn').waitFor({ timeout: 15000 });
    check(true, 'the levels follow the settings');
    await settings();
    await section.getByLabel(/カードとサイドバーに/).uncheck();
    await section.getByText('保存しました。').waitFor();
    await page.click('#settings-close');
    await card.locator('.res-row').waitFor({ state: 'hidden' });
    check(await page.locator('#res-totals').isHidden(), 'hidden: no row and no totals');
    await settings();
    await section.getByLabel(/カードとサイドバーに/).check();
    await page.waitForTimeout(300);
    await page.click('#settings-close');
    await card.locator('.res-row').waitFor();

    // Stopped: only the home, in the header and the sidebar.
    await nav('dev').click();
    await card.getByRole('button', { name: 'その他の操作' }).click();
    await card.getByRole('menuitem', { name: '停止', exact: true }).click();
    await page.locator('#confirm-panel').waitFor({ timeout: 2000 }).then(() => page.click('#confirm-ok'), () => {});
    await card.locator('.badge[data-state=stopped]').waitFor();
    await card.locator('.res-row .res-home').waitFor({ timeout: 15000 });
    check(await card.locator('.res-row .res-metric').count() === 1, 'a stopped sandbox shows only its home');
    await shot('stopped.png');

    if (phone) await page.click('#menu-button');
    await page.click('#volume-button');
    const size = page.locator('#volume-panel .volume-group', { has: page.locator('.volume-sandbox', { hasText: /^dev/ }) }).locator('.volume-size').first();
    await size.waitFor();
    check(/^\d+\.\d GB$/.test(await size.innerText()), 'the volumes show the home\'s size (' + await size.innerText() + ')');
    await shot('volumes.png');
    console.log('screenshots in ' + dir);
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) {
    console.log('\n' + failures.length + ' failed');
    process.exitCode = 1;
  }
})().catch((e) => { console.error(e); process.exitCode = 1; });
