'use strict';
// The files tab (ファイル) in a real browser against `serve -fake` with a
// tailnet origin: the per-OS URLs, an app password and a secret URL made
// in the page and then used by a WebDAV client (this script's requests),
// revoking, and a stopped sandbox's folder. Saves screenshots. Usage
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
    await panel.locator('.files-value').first().waitFor();
    const values = await panel.locator('.files-where .files-value').evaluateAll((els) => els.map((e) => e.value));
    check(values[0] === 'https://box.tail0000.ts.net/dav/dev/' && values[1] === '\\\\box.tail0000.ts.net@SSL\\dav\\dev', 'Windows gets the tailnet URL and the Explorer path (' + values.slice(0, 2).join(' , ') + ')');
    check(values.length === 4, 'Windows, its path, Mac and phone each have a row');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the panel does not scroll sideways');
    await panel.locator('.files-where .files-row').nth(1).getByRole('button', { name: /コピー/ }).click();
    check(await page.evaluate(() => navigator.clipboard.readText()) === values[1], 'コピー copies the value');
    await shot('files.png');
    await panel.locator('summary', { hasText: 'Windows の手順' }).click();
    await panel.locator('summary', { hasText: 'Windows の手順' }).scrollIntoViewIfNeeded();
    await shot('files-steps.png');

    // An app password: shown once, then used by a WebDAV client.
    await panel.getByPlaceholder('端末の名前').fill('仕事の PC');
    await panel.getByRole('button', { name: '作る', exact: true }).click();
    const made = panel.locator('.files-made');
    await made.waitFor();
    const [id, secret] = await made.locator('.files-value').evaluateAll((els) => els.map((e) => e.value));
    check(/^iwato-[a-z2-7]{6}$/.test(id) && /^[a-z2-7]{52}$/.test(secret), 'the ID and the password show once');
    await made.scrollIntoViewIfNeeded();
    await shot('files-password.png');
    let r = await dav(origin, 'PROPFIND', '/dav/dev/', { user: id, pass: secret });
    check(r.status === 207 && r.body.includes('/dav/dev/README.md'), 'the app password lists the workspace (PROPFIND ' + r.status + ')');
    r = await dav(origin, 'PUT', '/dav/dev/from-explorer.txt', { user: id, pass: secret, body: 'hello' });
    const got = await dav(origin, 'GET', '/dav/dev/from-explorer.txt', { user: id, pass: secret });
    check(r.status === 201 && got.body === 'hello', 'a file is written and read back');
    r = await dav(origin, 'PROPFIND', '/dav/dev/', { user: id, pass: secret + 'x' });
    check(r.status === 401, 'a wrong password is refused');
    await made.getByRole('button', { name: '閉じる' }).click();
    check(!(await panel.innerText()).includes(secret), 'closing forgets the password');

    // A secret URL for this PC, read-only.
    await panel.getByPlaceholder('端末の名前').fill('この PC');
    await panel.getByText('この PC 用の URL (http://127.0.0.1').click();
    await panel.getByText('読み取り専用').click();
    await panel.getByRole('button', { name: '作る', exact: true }).click();
    await made.waitFor();
    const [unc, local] = await made.locator('.files-value').evaluateAll((els) => els.map((e) => e.value));
    check(unc.startsWith('\\\\127.0.0.1@') && unc.endsWith('\\dev') && local.startsWith(origin + '/dav/'), 'the secret URL shows as an Explorer path and a URL');
    r = await dav(origin, 'PROPFIND', new URL(local).pathname);
    check(r.status === 207 && r.body.includes('from-explorer.txt'), 'the secret URL lists without a password');
    r = await dav(origin, 'PUT', new URL(local).pathname + 'x.txt', { body: 'x' });
    check(r.status === 403, 'a read-only URL refuses a write');
    r = await dav(origin, 'PROPFIND', new URL(local).pathname, { headers: { 'X-Forwarded-For': '100.64.0.2' } });
    check(r.status === 403, 'the secret URL is refused when relayed (tailscale serve)');
    await made.scrollIntoViewIfNeeded();
    await shot('files-url.png');
    await made.getByRole('button', { name: '閉じる' }).click();

    // The list, and revoking.
    const rows = panel.locator('.files-key');
    check(await rows.count() === 2, 'both credentials are listed');
    await rows.filter({ hasText: '仕事の PC' }).getByRole('button', { name: /取り消す/ }).click();
    await page.locator('#confirm-panel').waitFor();
    await page.click('#confirm-ok');
    await rows.filter({ hasText: '仕事の PC' }).waitFor({ state: 'detached' });
    r = await dav(origin, 'PROPFIND', '/dav/dev/', { user: id, pass: secret });
    check(r.status === 401, 'a revoked password is refused at once');
    await panel.locator('.files-keys').scrollIntoViewIfNeeded();
    await shot('files-list.png');

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
    r = await dav(origin, 'PROPFIND', new URL(local).pathname);
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
