'use strict';
// Port exposure in a real browser against `serve -fake`: the ポート tab with
// the local provider (the default: *.localhost links, opened in the
// browser: the ingress's page, never the admin UI), the settings (設定:
// Tailscale chosen, owners prefilled, the setup snippets copied, the
// secret saved and shown as a hint), the ポート tab with the node and the
// ports as links, and a stopped sandbox; also the settings' standard
// image (「標準イメージを更新」) and the recreate dialog's
// 「最新のイメージを取得してから作り直す」; the settings' language
// (English without a reload, remembered by the browser, back to automatic);
// the settings' startup checks and time zone; Iwato's update (the settings, the sidebar's
// 「更新」, the dialog naming the sandboxes that restart, the reload on the
// new build); then the settings' 「すべて消す」
// (everything goes, the login ends, the console's new URL logs in again);
// last, `serve -fake -fake-engine-down`: the banner of the failed engine
// check, and 「再確認」.
// Saves screenshots. Usage
// (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/ingress-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-ingress-'));
const phone = process.argv.includes('--phone');
const failures = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) failures.push(what); };

(async () => {
  execFileSync('go', ['build', '-tags', 'uitest', '-o', path.join(dir, 'iwato'), '.'], { cwd: repo, stdio: 'inherit' });
  const server = spawn(path.join(dir, 'iwato'), ['serve', '-fake', '-listen', '127.0.0.1:0', '-public-origin', 'https://box.tail0000.ts.net'], { cwd: dir });
  const url = await new Promise((resolve, reject) => {
    let out = '';
    server.stdout.on('data', (d) => { out += d; const m = out.match(/(http:\/\/127\.0\.0\.1:\d+\/)(#token=[0-9a-f]+)/); if (m) resolve(m[1] + m[2]); });
    server.on('exit', () => reject(new Error('serve -fake exited: ' + out)));
  });
  const origin = new URL(url).origin;
  let serverOut = '';
  server.stdout.on('data', (d) => { serverOut += d; });
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
    const noSideScroll = (loc) => loc.evaluate((el) => el.scrollWidth <= el.clientWidth + 1);
    const start = card.getByRole('button', { name: '起動' });
    if (await start.isVisible() && await start.isEnabled()) await start.click();
    await card.locator('.badge[data-state=running]').waitFor();

    // The default: this PC's *.localhost links.
    const port = new URL(url).port;
    await card.getByRole('tab', { name: 'ポート' }).click();
    const panel = card.locator('.ports-panel');
    await panel.getByText('この PC で公開中').waitFor();
    await panel.locator('.port-link').first().waitFor({ timeout: 20000 });
    const local = await panel.locator('.port-link').evaluateAll((els) => els.map((a) => a.href));
    check(local.includes('http://5173.dev.localhost:' + port + '/') && local.includes('http://3000.dev.localhost:' + port + '/'), 'ports link to *.localhost (' + local.join(' ') + ')');
    check(await noSideScroll(panel), 'the tab does not scroll sideways (local)');
    await shot('ports-local.png');
    // Opened in the browser: the ingress (the fake's sandbox runs nothing,
    // so its page), not the admin UI, and not with the admin's cookie.
    const [tab] = await Promise.all([context.waitForEvent('page'), panel.locator('.port-link', { hasText: '//5173.' }).click()]);
    await tab.waitForLoadState();
    check(tab.url() === 'http://5173.dev.localhost:' + port + '/', 'the link opens in a new tab (' + tab.url() + ')');
    const text = await tab.locator('body').innerText();
    check(text.includes('接続できません') && text.includes('dev:5173'), 'the ingress answers on *.localhost (' + text.slice(0, 80).replace(/\n/g, ' ') + ')');
    check(await tab.evaluate(() => isSecureContext), '*.localhost is a secure context');
    await tab.screenshot({ path: path.join(dir, 'local-page.png') });
    await tab.goto('http://5173.dev.localhost:' + port + '/api/sandboxes');
    const api = await tab.locator('body').innerText();
    check(!api.includes('"sandboxes"') && api.includes('接続できません'), 'the admin API is not served on a published host');
    const cookies = await context.cookies('http://5173.dev.localhost:' + port + '/');
    check(cookies.length === 0 && (await context.cookies(origin)).length === 1, 'the admin cookie stays on 127.0.0.1');
    await tab.close();
    const probe = await context.newPage();
    const res = await probe.goto('http://3000.nosuch.localhost:' + port + '/');
    check(res.status() === 403, 'an unknown sandbox\'s host gets the admin UI\'s Host check (' + res.status() + ')');
    await probe.close();

    // Tailscale is opt-in in the settings.
    await panel.locator('.ingress-settings-link').click();
    const settings = page.locator('#settings-panel');
    await settings.getByLabel('この PC だけ (既定)').waitFor();
    check(await settings.getByLabel('この PC だけ (既定)').isChecked(), 'local is chosen by default');
    check(!(await settings.locator('.ingress-settings textarea').isVisible()), 'the Tailscale settings are hidden while local is chosen');
    await shot('settings-local.png');
    await settings.getByLabel('Tailscale (スマホからも)').check();
    await settings.locator('.ingress-settings textarea').waitFor();
    check(await settings.locator('textarea').inputValue() === 'owner@github', 'owners are prefilled with this PC\'s Tailscale user');
    check(await settings.locator('details.ingress-steps').evaluate((d) => d.open), 'the setup steps are open while no secret is set');
    await settings.locator('.ingress-snippet').first().getByRole('button', { name: /コピー/ }).click();
    check((await page.evaluate(() => navigator.clipboard.readText())).includes('"tag:iwato": ["autogroup:admin"]'), 'the tagOwners snippet is copied');
    // The standard image: since when, and the update on request.
    const image = settings.locator('.image-settings');
    check(/作成: .+ · 取得: .+/.test(await image.locator('.image-times').innerText()), 'the image shows when it was made and pulled');
    await image.getByRole('button', { name: '標準イメージを更新' }).click();
    await image.getByText(/最新のイメージを取得しています/).waitFor();
    await image.getByText('新しいイメージを取得しました。これから作る・作り直すサンドボックスで使います').waitFor({ timeout: 10000 });
    check(await image.getByRole('button', { name: '標準イメージを更新' }).isEnabled(), 'the update can be asked again');
    // The startup checks: each with its state, detail and fix.
    const checks = settings.locator('.check-settings');
    await checks.locator('.check-row').first().waitFor();
    check(await checks.locator('h4').innerText() === '起動時の確認' && await checks.locator('.check-row').count() === 5, 'the startup checks are listed');
    const warn = await checks.locator('.check-row[data-level=warn]').innerText();
    check(warn.includes('docker buildx がありません') && warn.includes('brew install docker-buildx'), 'a warning shows its detail and its fix');
    await checks.getByRole('button', { name: '再確認' }).click();
    await checks.locator('button:enabled', { hasText: '再確認' }).waitFor({ timeout: 10000 });
    check(await checks.locator('.check-row').count() === 5, 'the checks can run again');
    // The logins: this browser, with its sign-out.
    const logins = settings.locator('.session-settings');
    await logins.locator('.files-key').first().waitFor();
    check((await logins.locator('.files-key').first().innerText()).includes('この端末') && await logins.getByRole('button', { name: 'ログアウト' }).count() === 1, 'the logins list this browser with its sign-out');
    // The time zone: this PC's by default, else a zone of the list.
    const zone = settings.locator('.timezone-settings');
    const zoneSelect = settings.getByLabel('タイムゾーン');
    await zone.locator('option[value=""]').waitFor({ state: 'attached' });
    check(await zone.locator('h4').innerText() === 'タイムゾーン' && /^この PC に合わせる \(.+\)$/.test(await zone.locator('option[value=""]').textContent()) && await zoneSelect.inputValue() === '', 'the time zone follows this PC by default');
    await zoneSelect.selectOption('Asia/Tokyo');
    await zone.getByText('保存しました。新しく開くターミナルから使われます。').waitFor();
    check((await page.evaluate(() => fetch('/api/timezone').then((r) => r.json()))).timezone === 'Asia/Tokyo', 'the chosen zone is saved');
    // Iwato's update: the fake's next edge build changes the gateway.
    const update = settings.locator('.update-settings');
    await update.getByText(/^新しい版: v[\d.]+ · 2222222$/).waitFor();
    check((await update.innerText()).includes('この版: v') && (await update.innerText()).includes('1111111'), 'the settings show this build and the new one');
    check(await page.locator('#update-button').isVisible(), 'the sidebar shows 更新');
    await update.getByRole('button', { name: '更新', exact: true }).click();
    const updateDialog = page.locator('.update-dialog');
    await updateDialog.waitFor();
    check((await updateDialog.innerText()).includes('ファイルは残ります') && (await updateDialog.locator('li').allInnerTexts()).join(',') === 'dev', 'a gateway change names the running sandboxes that restart');
    check(await updateDialog.locator('.update-recreate').isVisible(), 'a gateway change says sandboxes keep their supervisor until recreated');
    check(await noSideScroll(updateDialog.locator('.update-body')), 'the update dialog does not scroll sideways');
    await shot('update-dialog.png');
    await updateDialog.getByRole('button', { name: 'キャンセル' }).click();
    check(await updateDialog.isHidden(), 'cancel closes the update dialog');
    await update.getByRole('button', { name: '更新', exact: true }).click();
    await updateDialog.waitFor();
    await page.keyboard.press('Escape');
    check(await updateDialog.isHidden() && await settings.isVisible(), 'Escape closes only the update dialog');
    check(await noSideScroll(settings.locator('.login-body')), 'the settings do not scroll sideways');
    await shot('settings.png');
    await settings.locator('details.ingress-steps').scrollIntoViewIfNeeded();
    await shot('settings-steps.png');

    // The secret: saved, then only the hint.
    await settings.getByLabel('OAuth クライアントのシークレット').fill('tskey' + '-client-kFake-0123456789abcdefWXYZ');
    await settings.locator('.ingress-secret').getByRole('button', { name: '保存', exact: true }).click();
    await settings.getByText('設定済み (tskey-client-…WXYZ)').waitFor();
    check(!(await settings.innerText()).includes('0123456789abcdef'), 'the secret is not shown again');
    check(await settings.getByLabel('Tailscale (スマホからも)').isChecked(), 'the unsaved choice stays after the secret is saved');
    await settings.locator('.ingress-form').getByRole('button', { name: '保存' }).click();
    await settings.locator('.ingress-state[data-state=on]').waitFor();
    await settings.locator('.ingress-nodes').getByText('接続中').first().waitFor({ timeout: 20000 }).catch(() => {});
    await settings.locator('.login-body').evaluate((el) => { el.scrollTop = 0; });
    await shot('settings-on.png');
    await page.keyboard.press('Escape');
    check(await settings.isHidden(), 'Escape closes the settings');

    // The tab: the node and the ports.
    await panel.locator('.port-link').first().waitFor({ timeout: 20000 });
    const links = await panel.locator('.port-link').evaluateAll((els) => els.map((a) => a.href));
    check(new Set(links).has('https://iwato-dev.tail0000.ts.net:5173/') && new Set(links).has('https://iwato-dev.tail0000.ts.net:3000/'), 'ports link to the node (' + links.join(' ') + ')');
    check(await panel.locator('.ingress-dns').innerText().then((t) => t.includes('iwato-dev.tail0000.ts.net')), 'the node\'s name shows');
    check(await panel.getByText('::1 だけで待っています').count() === 1, 'a ::1-only server is noted');
    check(await noSideScroll(panel), 'the tab does not scroll sideways');
    await panel.locator('.port-row').first().getByRole('button', { name: /コピー/ }).click();
    check((await page.evaluate(() => navigator.clipboard.readText())).startsWith('https://iwato-dev.tail0000.ts.net:'), 'コピー copies the URL');
    await shot('ports.png');

    // Stopped: the tab stays; the node too.
    await card.getByRole('button', { name: 'その他の操作' }).click();
    await card.getByRole('menuitem', { name: '停止', exact: true }).click();
    await page.locator('#confirm-panel').waitFor({ timeout: 2000 }).then(() => page.click('#confirm-ok'), () => {});
    await card.locator('.badge[data-state=stopped]').waitFor();
    await panel.getByText(/停止中です/).waitFor();
    check(await panel.isVisible(), 'the ポート tab stays for a stopped sandbox');
    check(await panel.locator('.ingress-state').innerText().then((t) => t.includes('接続中')), 'the node stays connected while stopped');
    await shot('ports-stopped.png');

    // The fake's dev runs an older supervisor: its badge opens the recreate panel.
    const recreateBadge = card.locator('.badge-recreate');
    check(await recreateBadge.isVisible() && (await recreateBadge.innerText()) === '作り直しで更新', 'a sandbox with an older supervisor shows 作り直しで更新');
    check(await page.locator('#sandbox-nav .recreate-mark:not([hidden])').count() === 1, 'the sidebar marks it too');
    await shot('recreate-to-update.png');
    // Recreate, pulling the latest image first.
    await recreateBadge.click();
    const recreate = card.locator('.recreate-panel');
    const pull = recreate.getByLabel('最新のイメージを取得してから作り直す');
    await pull.waitFor();
    await pull.check();
    const gpu = recreate.getByLabel('GPU を使う');
    check(await gpu.isVisible() && !(await gpu.isChecked()), 'the recreate dialog offers the GPU, preset to the sandbox\'s');
    await gpu.check();
    check(await noSideScroll(recreate), 'the recreate dialog does not scroll sideways');
    await shot('recreate-pull.png');
    await recreate.getByLabel('確認のためサンドボックス名を入力').fill('dev');
    await recreate.getByRole('button', { name: '作り直す', exact: true }).click();
    await recreate.getByText(/最新のイメージを取得しています/).waitFor();
    await card.locator('.badge[data-state=running]').waitFor({ timeout: 20000 });
    check(await recreate.isHidden(), 'the recreate finished after the pull');
    await card.locator('.res-row .res-gpu').waitFor({ timeout: 15000 });
    check(await recreateBadge.isHidden() && await page.locator('#sandbox-nav .recreate-mark:not([hidden])').count() === 0, 'the recreated sandbox is not marked');

    // The language (web/i18n.js): English at once, remembered, then automatic (ja-JP here).
    const openSettings = async (p) => {
      if (phone) await p.click('#menu-button');
      await p.click('#settings-button');
      await p.locator('#settings-panel .language-select').waitFor();
    };
    await openSettings(page);
    const language = settings.locator('.language-settings');
    await settings.getByLabel('表示する言語').selectOption('en');
    await settings.locator('.log-head strong', { hasText: 'Settings' }).waitFor();
    check(await page.evaluate(() => document.documentElement.lang) === 'en', 'the page is marked English');
    check(await page.locator('#settings-close').innerText() === 'Close', 'static strings change without a reload');
    check(await page.locator('#login-submit').evaluate((b) => b.textContent) === 'Sign in', 'the login panel is English');
    check(await page.locator('#login-input').getAttribute('placeholder') === 'https://…/#token=… or the token', 'placeholders change too');
    check(await settings.getByLabel('Display language').inputValue() === 'en', 'the choice shows');
    check(await noSideScroll(settings.locator('.login-body')), 'the settings do not scroll sideways (English)');
    check(await settings.locator('.timezone-settings h4').innerText() === 'Time zone' && (await settings.locator('.timezone-settings option[value=""]').textContent()).startsWith('Match this PC (') && await settings.getByLabel('Time zone').inputValue() === 'Asia/Tokyo', 'the time zone section is English and keeps the choice');
    check(await settings.locator('.check-settings h4').innerText() === 'Startup checks' && (await settings.locator('.check-settings').innerText()).includes('docker buildx is missing'), 'the startup checks are English');
    await language.scrollIntoViewIfNeeded();
    await shot('settings-language-en.png');
    const again = await context.newPage();
    await again.goto(origin + '/');
    await again.locator('.terminal-card.selected').waitFor();
    check(await again.evaluate(() => localStorage.getItem('iwato-lang')) === 'en' && await again.locator('#settings-button').evaluate((b) => b.textContent) === 'Settings', 'a new page keeps English');
    {
      const card = again.locator('.terminal-card.selected');
      const tabs = await card.locator('.card-tab:not([hidden])').evaluateAll((list) => list.map((b) => b.firstChild.nodeValue));
      check(tabs.join(',') === 'Terminal,Policy,Editor,Ports', 'the card\'s tabs are English: ' + tabs.join(','));
      check(await again.locator('#create-button').evaluate((b) => b.textContent) === 'Create a sandbox' && await again.locator('#server-label').innerText() !== '' && !/[\u3040-\u30ff]/.test(await again.locator('#server-label').innerText()), 'the sidebar is English');
      const badge = await card.locator('.badge').first().innerText();
      check(['Running', 'Stopped', 'Not created'].includes(badge), 'the state badge is English: ' + badge);
      await again.screenshot({ path: path.join(dir, 'card-en.png') });
      await card.locator('.card-tab').nth(1).click();
      const policy = card.locator('.policy-panel');
      await policy.locator('.policy-current-title').waitFor();
      const policyText = await policy.innerText();
      check(/^Approved list \(\d+ rules\)$/.test(await policy.locator('.policy-current-title').innerText()) && !/[\u3040-\u30ff]/.test(await policy.locator('.policy-bar').innerText()), 'the policy tab is English');
      check(!/承認|規則|提案/.test(policyText), 'no Japanese left in the policy tab: ' + (policyText.match(/.*[\u3040-\u30ff\u4e00-\u9fff].*/) || [''])[0]);
      await again.screenshot({ path: path.join(dir, 'policy-en.png') });
      await card.locator('.card-tab').nth(0).click();
      if (phone) await again.click('#menu-button');
      await again.click('#provider-button');
      await again.locator('#provider-list .provider-row').first().waitFor();
      const providersText = await again.locator('#provider-panel').innerText();
      check(!/[\u3040-\u30ff]/.test(providersText.replace(/「[^」]*」/g, '')), 'the providers panel is English: ' + (providersText.match(/.*[\u3040-\u30ff].*/) || [''])[0]);
      await again.screenshot({ path: path.join(dir, 'providers-en.png'), fullPage: true });
      await again.click('#provider-close');
    }
    await openSettings(again);
    await again.getByLabel('Display language').selectOption('auto');
    await again.locator('#settings-panel .log-head strong', { hasText: '設定' }).waitFor();
    check(await again.evaluate(() => localStorage.getItem('iwato-lang')) === null && await again.locator('.language-select option[value=auto]').innerText() === '自動 (日本語)', 'automatic follows the browser (ja-JP) and forgets the choice');
    await again.close();
    await settings.getByLabel('Display language').selectOption('auto');
    await settings.locator('.log-head strong', { hasText: '設定' }).waitFor();
    await page.click('#settings-close');

    // The update from the sidebar: the progress, then the page reloads on the new build.
    await page.click('#update-button');
    await updateDialog.waitFor();
    const reloaded = page.waitForEvent('load', { timeout: 30000 });
    await updateDialog.getByRole('button', { name: '更新', exact: true }).click();
    await updateDialog.getByText('ダウンロードしています…').waitFor();
    await shot('update-progress.png');
    await reloaded;
    await page.locator('.terminal-card.selected').waitFor();
    await page.waitForFunction(() => document.getElementById('server-build').textContent.includes('2222222'), null, { timeout: 10000 }).catch(() => {});
    check(await page.locator('#update-button').isHidden() && (await page.locator('#server-build').textContent()).includes('2222222'), 'after the update the page shows the new build and no 更新');

    // A pull request's build: the list, the confirmation with its head
    // commit and warnings, the install, then back to edge.
    await page.evaluate(() => document.getElementById('settings-button').click());
    const updateAgain = settings.locator('.update-settings');
    await updateAgain.getByRole('button', { name: 'PR のビルドを試す' }).click();
    const prs = updateAgain.locator('.update-pr');
    await prs.first().waitFor();
    check((await prs.allInnerTexts()).join('\n').includes('#12 Terminal: faster scrolling') && await prs.count() === 2, 'the open pull requests\' builds are listed');
    check(await noSideScroll(settings.locator('.login-body')), 'the pull request list does not scroll sideways');
    await shot('update-prs.png');
    await prs.first().getByRole('button', { name: '入れる' }).click();
    await updateDialog.waitFor();
    const prText = await updateDialog.innerText();
    check(prText.includes('PR #12 のビルドを入れる') && prText.includes('4'.repeat(40)) && prText.includes('マージされていない') && prText.includes('PC での操作'), 'the confirmation shows the head commit and the warnings');
    check(await updateDialog.locator('.update-recreate').count() === 0, 'a build that keeps the gateway says nothing about recreating');
    check(await noSideScroll(updateDialog.locator('.update-body')), 'the pull request dialog does not scroll sideways');
    await shot('update-pr-dialog.png');
    await page.keyboard.press('Escape');
    check(await updateDialog.isHidden() && await settings.isVisible(), 'Escape closes the pull request dialog');
    await prs.first().getByRole('button', { name: '入れる' }).click();
    await updateDialog.waitFor();
    const toPR = page.waitForEvent('load', { timeout: 30000 });
    await updateDialog.getByRole('button', { name: '入れる', exact: true }).click();
    await toPR;
    await page.locator('.terminal-card.selected').waitFor();
    await page.waitForFunction(() => document.getElementById('server-build').textContent.includes('PR #12'), null, { timeout: 10000 }).catch(() => {});
    check((await page.locator('#server-build').textContent()).includes('PR #12 · '), 'the sidebar names the pull request that runs');
    await page.evaluate(() => document.getElementById('settings-button').click());
    await updateAgain.getByText('PR #12 をお試し中').waitFor();
    await shot('update-trying.png');
    await updateAgain.getByRole('button', { name: 'edge に戻す' }).click();
    await updateDialog.waitFor();
    check((await updateDialog.innerText()).includes('→ v') && (await updateDialog.innerText()).includes('2222222'), 'back to edge names the edge build');
    const toEdge = page.waitForEvent('load', { timeout: 30000 });
    await updateDialog.getByRole('button', { name: 'edge に戻す', exact: true }).click();
    await toEdge;
    await page.locator('.terminal-card.selected').waitFor();
    await page.waitForFunction(() => !document.getElementById('server-build').textContent.includes('PR #'), null, { timeout: 10000 }).catch(() => {});
    const backBuild = await page.locator('#server-build').textContent();
    check(!backBuild.includes('PR #') && backBuild.includes('2222222'), 'back on edge, older than the pull request build');

    // Last: 「すべて消す」 at the bottom of the settings.
    await page.evaluate(() => document.getElementById('settings-button').click());
    const resetSection = settings.locator('.reset-settings');
    await resetSection.scrollIntoViewIfNeeded();
    check(await page.evaluate(() => document.getElementById('settings-body').lastElementChild.className === 'reset-settings'), 'すべて消す is the last section of the settings');
    await shot('settings-reset.png');
    await resetSection.getByRole('button', { name: 'すべて消す' }).click();
    const dialog = page.locator('.reset-dialog');
    await dialog.waitFor();
    check(await noSideScroll(dialog.locator('.reset-body')), 'the reset dialog does not scroll sideways');
    check((await dialog.innerText()).includes('VHDX') && (await dialog.locator('.reset-kept').innerText()).includes('presets/'), 'the dialog says what stays');
    await shot('reset-dialog.png');
    await page.keyboard.press('Escape');
    check(await dialog.isHidden() && await page.locator('#settings-panel').isVisible(), 'Escape closes only the reset dialog');
    await resetSection.getByRole('button', { name: 'すべて消す' }).click();
    const go = dialog.getByRole('button', { name: 'すべて消す', exact: true });
    check(await go.isDisabled(), 'すべて消す needs the word');
    await dialog.getByLabel('確認のため reset と入力').fill('reset');
    await go.click();
    await dialog.getByText('すべて消しました').waitFor({ timeout: 30000 });
    await shot('reset-done.png');
    const tokens = [...serverOut.matchAll(/(http:\/\/127\.0\.0\.1:\d+\/#token=[0-9a-f]+)/g)].map((m) => m[1]);
    const fresh = tokens.at(-1);
    check(!!fresh && fresh !== url, 'the console shows a new login URL');
    check(await page.evaluate(() => fetch('/api/sandboxes').then((r) => r.status)) === 401, 'the browser\'s login ended');
    await dialog.getByRole('button', { name: '閉じる' }).click();
    await page.locator('#login-panel').waitFor();
    check(await dialog.isHidden(), 'closing the dialog shows the login');
    await page.goto('about:blank');
    await page.goto(fresh);
    await page.locator('#empty-state').waitFor({ timeout: 10000 });
    check(await page.locator('.terminal-card').count() === 0, 'the new login sees no sandboxes');
    await shot('reset-after.png');

    // Without a usable engine (-fake-engine-down) the page shows the failed
    // check with its fix, and goes on after 「再確認」.
    const down = spawn(path.join(dir, 'iwato'), ['serve', '-fake', '-fake-engine-down', '-listen', '127.0.0.1:0'], { cwd: dir });
    try {
      let downOut = '';
      const downURL = await new Promise((resolve, reject) => {
        down.stdout.on('data', (d) => { downOut += d; const m = downOut.match(/(http:\/\/127\.0\.0\.1:\d+\/)(#token=[0-9a-f]+)/); if (m) resolve(m[1] + m[2]); });
        down.on('exit', () => reject(new Error('serve -fake -fake-engine-down exited: ' + downOut)));
      });
      check(/Docker is not usable: see the browser/.test(downOut), 'the console says to look at the browser');
      await page.goto(downURL);
      const banner = page.locator('#images-banner');
      await banner.getByText('Docker を使えません').waitFor({ timeout: 10000 });
      const bannerText = await banner.innerText();
      check(bannerText.includes('エンジンが動いていません') && bannerText.includes('colima start'), 'the banner shows the failed check and its fix');
      const downCard = page.locator('.terminal-card.selected');
      await downCard.waitFor();
      check(await downCard.getByRole('button', { name: '起動' }).isDisabled(), 'starting waits for the engine');
      check(await noSideScroll(page.locator('#main-content')), 'the banner does not scroll sideways');
      await shot('engine-down.png');
      await banner.getByRole('button', { name: '再確認' }).click();
      await banner.waitFor({ state: 'hidden', timeout: 10000 });
      check(await downCard.getByRole('button', { name: '起動' }).isEnabled(), 'once the engine passes, starting is possible');
      await shot('engine-up.png');
    } finally {
      down.kill();
    }
    console.log('screenshots in ' + dir);
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed'); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
