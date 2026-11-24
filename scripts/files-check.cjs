'use strict';
// The files tab (ファイル) in a real browser against `serve -fake` with a
// tailnet origin: the WebDAV guide making an app password and a secret URL,
// used by a WebDAV client (this script's requests), revoking in the
// settings, and a stopped sandbox's folder. Saves screenshots. Usage
// (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/files-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-files-'));
const phone = process.argv.includes('--phone');
const failures = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) failures.push(what); };

// dav is a WebDAV request as Explorer or Finder makes it (no Origin, no
// cookie), straight to the loopback listener.
function dav(origin, method, p, { user, pass, body, headers = {} } = {}) {
  return new Promise((resolve, reject) => {
    const u = new URL(p, origin);
    const h = { ...headers };
    if (user) h.Authorization = 'Basic ' + Buffer.from(user + ':' + pass).toString('base64');
    if (method === 'PROPFIND' && !h.Depth) h.Depth = '1';
    const req = http.request({ host: u.hostname, port: u.port, path: u.pathname, method, headers: h, agent: false }, (res) => {
      let data = '';
      res.setEncoding('utf8');
      res.on('data', (d) => { data += d; });
      res.on('end', () => resolve({ status: res.statusCode, body: data }));
    });
    req.on('error', reject);
    req.end(body);
  });
}

(async () => {
  execFileSync('go', ['build', '-tags', 'uitest', '-o', path.join(dir, 'iwato'), '.'], { cwd: repo, stdio: 'inherit' });
  const server = spawn(path.join(dir, 'iwato'), ['serve', '-fake', '-listen', '127.0.0.1:0', '-public-origin', 'https://box.tail0000.ts.net'], { cwd: dir });
  const url = await new Promise((resolve, reject) => {
    let out = '';
    server.stdout.on('data', (d) => { out += d; const m = out.match(/(http:\/\/127\.0\.0\.1:\d+\/)(#token=[0-9a-f]+)/); if (m) resolve(m[1] + m[2]); });
    server.on('exit', () => reject(new Error('serve -fake exited: ' + out)));
  });
  const origin = new URL(url).origin;
  const browser = await chromium.launch();
  try {
    const context = await browser.newContext(phone
      ? { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 2, locale: 'ja-JP' }
      : { viewport: { width: 1280, height: 860 }, locale: 'ja-JP' });
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
    const page = await context.newPage();
    page.on('pageerror', (e) => failures.push('page error: ' + e.message));
    page.on('console', (m) => { if (m.type() === 'error') console.log('console: ' + m.text()); });
    await page.goto(url);
    const card = page.locator('.terminal-card.selected');
    await card.waitFor();
    const shot = (name) => page.screenshot({ path: path.join(dir, name), fullPage: false });
    const start = card.getByRole('button', { name: '起動' });
    if (await start.isVisible() && await start.isEnabled()) await start.click();
    await card.locator('.badge[data-state=running]').waitFor();

    await card.getByRole('button', { name: 'その他の操作' }).click();
    await card.getByRole('menuitem', { name: 'ファイル' }).click();
    check(await card.getByRole('tab', { name: 'ファイル' }).getAttribute('aria-selected') === 'true', 'the open view shows as the current tab');
    const panel = card.locator('.files-panel');
    await panel.locator('.files-offer').waitFor();
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the panel does not scroll sideways');
    await shot('files.png');

    // The guide, for a phone: an app password at the tailnet's address,
    // shown once, then used by a WebDAV client; the guide sees the use.
    const dlg = page.locator('.guide-dialog');
    await panel.getByRole('button', { name: 'ガイドを始める' }).click();
    await dlg.waitFor();
    await shot('files-guide-device.png');
    await dlg.getByText('スマホ・別の PC').click();
    await dlg.getByRole('button', { name: '次へ' }).click();
    await dlg.getByRole('textbox').fill('仕事の PC');
    await shot('files-guide-name.png');
    await dlg.getByRole('button', { name: '次へ' }).click();
    await dlg.locator('.files-value').first().waitFor();
    const [address, id, secret] = await dlg.locator('.files-value').evaluateAll((els) => els.map((e) => e.value));
    check(address === 'https://box.tail0000.ts.net/dav/', 'a phone gets the tailnet address of every sandbox (' + address + ')');
    check(/^iwato-[a-z2-7]{6}$/.test(id) && /^[a-z2-7]{52}$/.test(secret), 'the ID and the password show once');
    check(await dlg.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the guide does not scroll sideways');
    await dlg.locator('.files-row').nth(2).getByRole('button', { name: /コピー/ }).click();
    check(await page.evaluate(() => navigator.clipboard.readText()) === secret, 'コピー copies the value');
    await shot('files-password.png');
    await dlg.getByRole('button', { name: '次へ' }).click();
    let r = await dav(origin, 'PROPFIND', '/dav/dev/', { user: id, pass: secret });
    check(r.status === 207 && r.body.includes('/dav/dev/README.md'), 'the app password lists the workspace (PROPFIND ' + r.status + ')');
    r = await dav(origin, 'PUT', '/dav/dev/from-explorer.txt', { user: id, pass: secret, body: 'hello' });
    const got = await dav(origin, 'GET', '/dav/dev/from-explorer.txt', { user: id, pass: secret });
    check(r.status === 201 && got.body === 'hello', 'a file is written and read back');
    r = await dav(origin, 'PROPFIND', '/dav/dev/', { user: id, pass: secret + 'x' });
    check(r.status === 401, 'a wrong password is refused');
    await dlg.locator('.guide-status[data-state=ok]').waitFor({ timeout: 10000 });
    check((await dlg.innerText()).includes('「仕事の PC」が使われました'), 'the guide sees the first use');
    await shot('files-guide-used.png');
    await dlg.getByRole('button', { name: '完了' }).click();
    await dlg.waitFor({ state: 'detached' });
    check(!(await page.locator('body').innerText()).includes(secret), 'closing forgets the password');
    await panel.locator('.files-key').first().waitFor();
    await shot('files-devices.png');

    // Windows on this PC, read only, this sandbox only: a secret URL.
    await panel.getByRole('button', { name: '別の端末も追加' }).click();
    await dlg.waitFor();
    await dlg.getByRole('button', { name: '次へ' }).click();
    await dlg.getByText('読み取り専用').click();
    await dlg.getByText('詳しい設定').click();
    await dlg.getByText('dev だけに限る').click();
    await dlg.getByRole('button', { name: '次へ' }).click();
    await dlg.locator('.files-value').first().waitFor();
    const [unc] = await dlg.locator('.files-value').evaluateAll((els) => els.map((e) => e.value));
    const token = (unc.match(/\\dav\\([a-z2-7]+)\\dev$/) || [])[1];
    check(unc.startsWith('\\\\127.0.0.1@') && !!token, 'Windows gets an Explorer path with the secret (' + unc + ')');
    const local = '/dav/' + token + '/dev/';
    r = await dav(origin, 'PROPFIND', local);
    check(r.status === 207 && r.body.includes('from-explorer.txt'), 'the secret URL lists without a password');
    r = await dav(origin, 'PUT', local + 'x.txt', { body: 'x' });
    check(r.status === 403, 'a read-only URL refuses a write');
    r = await dav(origin, 'PROPFIND', local, { headers: { 'X-Forwarded-For': '100.64.0.2' } });
    check(r.status === 403, 'the secret URL is refused when relayed (tailscale serve)');
    await shot('files-url.png');
    await dlg.getByRole('button', { name: 'やめる' }).click();

    // The settings list every key; revoking stops it at once.
    if (phone) await page.click('#menu-button');
    await page.click('#settings-button');
    const rows = page.locator('#settings-body .files-settings .files-key');
    await rows.nth(1).waitFor({ timeout: 5000 }).catch(() => {});
    check(await rows.count() === 2, 'both credentials are listed in the settings (' + await rows.count() + ')');
    await rows.filter({ hasText: '仕事の PC' }).getByRole('button', { name: /取り消す/ }).click();
    await page.locator('#confirm-panel').waitFor();
    await page.click('#confirm-ok');
    await rows.filter({ hasText: '仕事の PC' }).waitFor({ state: 'detached' });
    r = await dav(origin, 'PROPFIND', '/dav/dev/', { user: id, pass: secret });
    check(r.status === 401, 'a revoked password is refused at once');
    await page.locator('#settings-body .files-settings').scrollIntoViewIfNeeded();
    await shot('files-list.png');
    await page.locator('#settings-panel').getByRole('button', { name: '閉じる' }).first().click();

    // Stopped: the tab stays; the folder holds the note only.
    if (phone) {
      await card.getByRole('button', { name: 'その他の操作' }).click();
    } else {
      await card.getByRole('button', { name: 'その他の操作' }).click();
    }
    await card.getByRole('menuitem', { name: '停止', exact: true }).click();
    await page.locator('#confirm-panel').waitFor({ timeout: 2000 }).then(() => page.click('#confirm-ok'), () => {});
    await card.locator('.badge[data-state=stopped]').waitFor();
    await panel.locator('.files-state[data-state=stopped]').waitFor();
    check(await panel.isVisible(), 'the files tab stays for a stopped sandbox');
    r = await dav(origin, 'PROPFIND', local);
    check(r.status === 207 && r.body.includes(encodeURIComponent('停止中です.txt')) && !r.body.includes('README'), 'a stopped sandbox shows 停止中です.txt only');
    await panel.locator('.files-state').scrollIntoViewIfNeeded();
    await shot('files-stopped.png');
    console.log('screenshots in ' + dir);
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed'); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
