'use strict';
// The policy tab in a real browser against `serve -fake`: the three lists
// (waiting, the effective policy with provider rules and rules from
// proposals, rejected), undoing a proposal, putting an approved proposal's
// rule back, the rules piece by piece, adding a rule through the dialog,
// taking a method and path out, the history and the policy as YAML. Saves
// screenshots in a temporary directory (printed at the end). Usage
// (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/policy-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-policy-'));
const phone = process.argv.includes('--phone');
const failures = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) failures.push(what); };
// A YAML box (.code-field, CodeMirror once loaded): its text, and replacing
// it as an edit does.
const yamlValue = async (box) => { await box.locator('.cm-editor').waitFor(); return box.evaluate((el) => window.IwatoCodeMirror.EditorView.findFromDOM(el.querySelector('.cm-editor')).state.doc.toString()); };
const yamlFill = async (box, text) => { await box.locator('.cm-editor').waitFor(); await box.evaluate((el, text) => { const v = window.IwatoCodeMirror.EditorView.findFromDOM(el.querySelector('.cm-editor')); v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: text }, userEvent: 'input' }); }, text); };

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
    const card = page.locator('.terminal-card.selected');
    await card.waitFor();
    const start = card.getByRole('button', { name: '起動' });
    if (await start.isVisible() && await start.isEnabled()) { await start.click(); await page.waitForTimeout(1500); }
    await card.locator('.card-tab', { hasText: 'ポリシー' }).click();
    const panel = card.locator('.policy-panel');
    const current = panel.locator('.policy-current');
    await current.getByText('allow_api_example_com_443').waitFor();
    // A phone folds each rule to a line; open opens one (once).
    const rule = (name) => current.locator('.policy-rule').filter({ has: page.locator('.policy-rule-name', { hasText: new RegExp('^' + name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$') }) });
    const open = async (name) => { if (phone && await rule(name).getAttribute('data-open') !== 'true') await rule(name).getByRole('button', { name: '規則「' + name + '」を開く' }).click(); };
    await page.screenshot({ path: path.join(dir, 'policy.png'), fullPage: true });
    await open('allow_api_example_com_443');
    await open('allow_ci_tailnet_example_443');
    await open('allow_github_example_com_443');
    const shown = await current.innerText();
    check(shown.includes('版 2') && shown.includes('GET /v1/**') && shown.includes('拒否: DELETE /v1/**') && shown.includes('api.example.com:443,8443 · 検査 (遮断)'), 'the current policy lists rules, endpoints, and methods and paths');
    check(shown.includes('GET / (ルートだけ)') && shown.includes('拒否: DELETE /v1/**'), 'a rule on / alone says it is the root only');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the policy panel does not scroll sideways');
    const ci = rule('allow_ci_tailnet_example_443');
    check(await ci.locator('.policy-allowed-ips').innerText() === '解決先 IP を 100.64.0.0/10 に限定 (内部アドレスも許可)', 'an endpoint with allowed_ips shows the internal addresses it opens');
    await ci.screenshot({ path: path.join(dir, 'allowed-ips.png') });
    await open('allow_github_example_com_443');
    await current.locator('.policy-observed').first().screenshot({ path: path.join(dir, 'observed.png') });

    // The three lists, all OpenShell's.
    const pending = panel.locator('.policy-pending');
    const rejectedList = panel.locator('.policy-rejected');
    const pendingText = await pending.innerText();
    check(pendingText.startsWith('承認待ち (6)') && pendingText.includes('エージェントの提案') && pendingText.includes('OpenShell の自動提案') && pendingText.includes('37 回') && pendingText.includes('「iwato リポジトリにブランチを push する」'), 'waiting: the agent\'s and OpenShell\'s proposals with their counts and intent');
    const prov = rule('_provider_github_personal');
    await open('_provider_github_personal');
    const provText = await prov.innerText();
    check(provText.includes('プロバイダー github-personal (GitHub)') && provText.includes('外すにはプロバイダーを外す') && provText.includes('/usr/bin/gh') && await prov.locator('.policy-rule-body > :not(.policy-endpoint) button, .policy-endpoint > .policy-line button').count() === 0, 'a provider\'s rule is labelled and read-only');
    const ghRule = rule('allow_github_example_com_443');
    check(await ghRule.locator('.policy-chunk').count() === 2 && await ghRule.getByRole('button', { name: '規則「allow_github_example_com_443」を外す' }).count() === 0, 'a rule from two proposals lists both, with 取り消す and no 外す');
    await open('allow_registry_npmjs_org_443');
    check((await rule('allow_registry_npmjs_org_443').innerText()).includes('22.11.0/bin/node を /sandbox/.local/share/mise/installs/node/*/bin/node に広げて承認'), 'a proposal approved with a glob says so');
    const unlinked = current.locator('.policy-unlinked');
    check((await unlinked.innerText()).includes('allow_mise_versions_jdx_dev_443 — 規則がありません'), 'an approved proposal without its rule shows as a dashed row');
    const rejText = await rejectedList.innerText();
    check(rejText.startsWith('却下した提案 (2)') && rejText.includes('却下後も 2 回、最後は') && rejText.includes('OpenShell が自動で却下 (承認済みの提案と同じ宛先)'), 'rejected: the hits since, and OpenShell\'s own rejection');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1) && await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth + 1), 'the lists do not scroll sideways');
    // The header with the 一覧 / YAML switch stays at the top of the tab
    // however long the lists are.
    const seg = (key) => panel.locator('.policy-view-switch [data-seg=' + key + ']');
    const inView = async (loc) => { const box = await loc.boundingBox(); const vp = page.viewportSize(); return Boolean(box) && box.y >= 0 && box.y + box.height <= vp.height && box.x >= 0 && box.x + box.width <= vp.width; };
    check(await inView(seg('yaml')) && await seg('list').getAttribute('aria-pressed') === 'true', 'the YAML switch shows at the top of the tab');
    check(await panel.evaluate((el) => el.scrollHeight > el.clientHeight + 200), 'the lists are longer than the tab');
    await panel.evaluate((el) => { el.scrollTop = el.scrollHeight; });
    await page.waitForTimeout(100);
    check(await inView(seg('yaml')) && await panel.evaluate((el) => { const bar = el.querySelector('.policy-bar').getBoundingClientRect(); return Math.abs(bar.top - el.getBoundingClientRect().top) < 2; }), 'scrolled to the end, the YAML switch is still at the top');
    await page.screenshot({ path: path.join(dir, 'policy-scrolled.png') });
    await panel.evaluate((el) => { el.scrollTop = 0; });
    // The card scrolls inside the page: a tall window shows each list whole.
    const size = page.viewportSize();
    await page.setViewportSize({ width: size.width, height: 4000 });
    await page.waitForTimeout(200);
    await pending.screenshot({ path: path.join(dir, 'lists-pending.png') });
    await current.screenshot({ path: path.join(dir, 'lists-approved.png') });
    await rejectedList.screenshot({ path: path.join(dir, 'lists-rejected.png') });
    await page.setViewportSize(size);
    if (phone) {
      const pypi = rule('allow_pypi_org_443');
      await pypi.scrollIntoViewIfNeeded();
      check(await pypi.locator('.policy-rule-body').isHidden() && await pypi.locator('.policy-rule-fold').isVisible() && (await pypi.locator('.policy-rule-fold').innerText()).includes('python3.*, pip → pypi.org ほか 1'), 'a phone folds a rule to a line');
      check(await rule('allow_registry_npmjs_org_443').locator('.policy-quick').count() === 1, 'a folded rule from one proposal has 取り消す on its line');
      await page.evaluate(() => window.scrollTo(0, 0));
    }

    // Undo: the proposal goes back to waiting; the glob it was widened to
    // and the endpoint added after go with the rule.
    const dialog = page.locator('#confirm-panel');
    await rule('allow_registry_npmjs_org_443').locator('.policy-chunk').getByRole('button', { name: '取り消す' }).click();
    await dialog.waitFor();
    const undoText = await page.locator('#confirm-text').innerText();
    check(undoText.includes('広げたプログラム (/sandbox/.local/share/mise/installs/node/*/bin/node) も外します') && undoText.includes('規則ごと消えます') && undoText.includes('npm.pkg.github.com:443'), 'undoing says what goes with the rule');
    await page.screenshot({ path: path.join(dir, 'undo-confirm.png') });
    await page.click('#confirm-ok');
    await pending.locator('.proposal-item', { hasText: 'registry.npmjs.org' }).waitFor();
    check(await rule('allow_registry_npmjs_org_443').count() === 0 && (await current.locator('.panel-meta .apply-badge').first().innerText()) === '変更済み · 反映待ち', 'the rule is gone, the proposal waits again, the policy shows 反映待ち');
    // Put back and undone: approved without its rule.
    await unlinked.getByRole('button', { name: '規則を戻して取り消す' }).click();
    await dialog.waitFor();
    await page.click('#confirm-ok');
    await pending.locator('.proposal-item', { hasText: 'mise-versions.jdx.dev' }).waitFor();
    check(await unlinked.count() === 0 && await rule('allow_mise_versions_jdx_dev_443').count() === 0, 'the rule went back and out again: the proposal waits again');
    // A rejected proposal: approved as proposed.
    await rejectedList.locator('.proposal-item', { hasText: '/usr/bin/curl' }).getByRole('button', { name: '承認する' }).click();
    await dialog.waitFor();
    check(await page.locator('#approve-form').isHidden() && (await page.locator('#confirm-text').innerText()).includes('提案どおり'), 'a rejected proposal is approved as proposed');
    await page.click('#confirm-ok');
    await rule('allow_mise_versions_jdx_dev_443').waitFor();
    check((await rejectedList.innerText()).startsWith('却下した提案 (1)'), 'it left the rejected list for the policy');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });
    await page.screenshot({ path: path.join(dir, 'lists-after.png'), fullPage: true });

    // The approve dialog: a versioned program widened to a glob, reads only,
    // the paths seen narrowed by a segment click; a live summary.
    const claudeItem = panel.locator('.proposal-item', { hasText: 'downloads.claude.ai' });
    await claudeItem.getByRole('button', { name: '承認', exact: true }).click();
    await dialog.waitFor();
    const form = page.locator('#approve-form');
    check(await form.isVisible() && await page.locator('#approve-scope').isHidden() && await page.locator('#approve-access-read').isChecked(), 'the approve dialog starts with reads only');
    check(await page.locator('#approve-binary').inputValue() === '/sandbox/.local/share/claude/versions/*', 'the program field starts with the versions as *');
    const chips = await page.locator('#approve-binary-chips .approve-chip').evaluateAll((els) => els.map((e) => e.dataset.path));
    check(chips.join(' ') === '/sandbox/.local/share/claude/versions/2.1.293 /sandbox/.local/share/claude/versions/* /**', 'the candidates go narrow to wide (' + chips.join(' ') + ')');
    const rows = page.locator('#approve-path-rows .approve-path input');
    check(await rows.count() === 2 && await rows.first().inputValue() === '/claude-code-releases/2.1.294/manifest.json', 'the paths seen fill the list');
    const fits = async () => dialog.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1 && document.documentElement.scrollWidth <= window.innerWidth + 1);
    check(await fits(), 'the approve dialog does not scroll sideways');
    check((await page.locator('#approve-head .approve-host').innerText()) === 'downloads.claude.ai' && await page.locator('#approve-writes').isHidden() && !(await page.locator('#approve-more').evaluate((el) => el.open)), 'the destination leads; reads only shows no writes; 詳細 starts closed');
    await page.screenshot({ path: path.join(dir, 'approve-dialog.png') });
    await page.locator('#approve-more > summary').click();
    await page.locator('#approve-binary-chips .approve-chip[data-path="/sandbox/.local/share/claude/versions/*"]').click();
    await page.locator('#approve-path-rows .approve-path').first().locator('.approve-seg', { hasText: '/manifest.json' }).click();
    check(await rows.first().inputValue() === '/claude-code-releases/2.1.294/*', 'a segment click makes it *');
    await page.locator('#approve-path-rows .approve-path').nth(1).getByRole('button', { name: 'このパスを削除' }).click();
    const summary = (await page.locator('#approve-summary > p').evaluateAll((els) => els.map((e) => e.textContent))).join(' | ');
    check(summary === 'downloads.claude.ai | 読み取りだけ (GET・HEAD・OPTIONS) | /claude-code-releases/2.1.294/* | プログラム: /sandbox/.local/share/claude/versions/*', 'the summary says what it allows: ' + summary);
    check(await page.locator('#approve-binary-check').innerText().then((t) => t.startsWith('✓')), 'the glob matches the proposed program');
    check(await fits(), 'and still fits after the edits');
    await form.evaluate((el) => el.scrollIntoView({ block: 'start' }));
    await page.screenshot({ path: path.join(dir, 'approve-dialog-edited.png') });
    await dialog.locator('.login-body').evaluate((el) => { el.scrollTop = el.scrollHeight; });
    await page.screenshot({ path: path.join(dir, 'approve-dialog-bottom.png') });
    // A bad program holds the approval back.
    await page.fill('#approve-binary', 'claude');
    check(await page.locator('#confirm-ok').isDisabled() && await page.locator('#approve-error').isVisible(), 'a relative program cannot be approved');
    await page.locator('#approve-binary-chips .approve-chip[data-path="/sandbox/.local/share/claude/versions/*"]').click();
    await page.click('#confirm-ok');
    const raw = rule('allow_downloads_claude_ai_443');
    await raw.waitFor();
    await open('allow_downloads_claude_ai_443');
    const rawText = await raw.innerText();
    check(rawText.includes('2.1.293 を /sandbox/.local/share/claude/versions/* に広げて承認') && ['GET', 'HEAD', 'OPTIONS'].every((m) => rawText.includes(m + ' /claude-code-releases/2.1.294/*')) && !rawText.includes('linux-x64'), 'the approved rule has the glob and reads on the path, from its proposal');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });

    // Not inspected, to where the attached GitHub provider's token goes
    // (uploads.github.com): as proposed, or with only the program widened,
    // OpenShell would refuse it, so Iwato does, with the way out. Reads only
    // go through.
    const ghItem = panel.locator('.proposal-item', { hasText: 'uploads.github.com' });
    const approveOnce = async (prepare) => {
      await ghItem.getByRole('button', { name: '承認', exact: true }).click();
      await dialog.waitFor();
      await prepare();
      const answer = page.waitForResponse((r) => r.url().endsWith('/proposals/approve'));
      await page.click('#confirm-ok');
      return (await answer).status();
    };
    const refusal = panel.locator('.panel-error', { hasText: 'github-personal' });
    let status = await approveOnce(async () => {
      check(await page.locator('#approve-access-read').isChecked(), 'an uninspected HTTPS proposal starts with reads only');
      await page.locator('#approve-more > summary').click();
      await page.check('#approve-access-all');
      check((await page.locator('#approve-writes').innerText()).includes('すべてのメソッドとパス (検査しません)'), 'as proposed, uninspected: shown as writes');
    });
    await refusal.waitFor();
    check(status === 409 && (await refusal.innerText()).includes('「読み取りだけ」か「書き込みも許可」'), 'approving it as proposed is refused, with the way out');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the refusal does not make the panel scroll sideways');
    await refusal.screenshot({ path: path.join(dir, 'approve-conflict.png') });
    status = await approveOnce(async () => {
      await page.locator('#approve-more > summary').click();
      await page.locator('#approve-binary-chips .approve-chip[data-path="/usr/bin/*"]').click();
      await page.check('#approve-access-all');
    });
    await refusal.waitFor();
    check(status === 409, 'with only the program widened, still refused');
    status = await approveOnce(async () => {});
    const uploads = rule('allow_uploads_github_com_443');
    await uploads.waitFor();
    check(status === 200 && await refusal.count() === 0, 'reads only go through and clear the refusal');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });

    // OpenShell's proposal for api.github.com, where the attached GitHub
    // provider's rule enforces: 監査 would make the two disagree, which
    // OpenShell refuses, so the dialog disables it and says why.
    const miseItem = pending.locator('.proposal-item', { hasText: 'lib/mise/mise' });
    // In English first (the page words the reason), then in Japanese again.
    const relist = async (lang) => {
      await page.evaluate((l) => window.IwatoI18n.setPreference(l), lang);
      await Promise.all([page.waitForResponse((r) => r.url().endsWith('/proposals') && r.request().method() === 'GET'), panel.locator('[data-i18n="policy.refresh"]').click()]);
    };
    await relist('en');
    await miseItem.getByRole('button', { name: 'Approve', exact: true }).click();
    await dialog.waitFor();
    const enNote = await page.locator('#approve-blocked').textContent();
    check(enNote.startsWith('"Audit" is not available: api.github.com:443 has rule _provider_github_personal of provider github-personal (GitHub)') && enNote.includes('Mode: enforce in _provider_github_personal') && !/[\u3040-\u30ff]/.test(enNote), 'the reason in English: ' + enNote);
    await page.locator('#approve-more > summary').click();
    await page.screenshot({ path: path.join(dir, 'approve-blocked-en.png') });
    await page.click('#confirm-cancel');
    await relist('ja');
    await miseItem.getByRole('button', { name: '承認', exact: true }).click();
    await dialog.waitFor();
    const blockedNote = await page.locator('#approve-blocked').textContent();
    check(await page.locator('#approve-access-audit').isDisabled() && !(await page.locator('#approve-access-read').isDisabled()) && await page.locator('#approve-access-read').isChecked(), 'the dialog disables 監査 where the provider enforces');
    check(blockedNote.includes('「監査」は選べません') && blockedNote.includes('プロバイダー github-personal (GitHub) の規則 _provider_github_personal') && blockedNote.includes('遮断 (enforce)'), 'and names the provider\'s rule: ' + blockedNote);
    check(await fits(), 'the dialog with the reason does not scroll sideways');
    // Its only rule, GET /, was seen by no one (OpenShell copied it):
    // no request seen, the list empty (all paths), a hint to narrow.
    const miseSeen = await page.locator('#approve-seen').innerText();
    check(miseSeen === '見えた要求はありません · 空ならすべてのパス' && await page.locator('#approve-path-rows .approve-path').count() === 0, 'a placeholder GET / is no request seen: ' + miseSeen);
    check((await page.locator('#approve-path-suggest').innerText()).includes('/repos/*/*/**') && (await page.locator('#approve-summary > .approve-sum-paths').innerText()) === 'すべてのパス', 'reads on all paths, with a hint to narrow');
    await page.screenshot({ path: path.join(dir, 'approve-blocked.png') });
    await page.locator('#approve-more > summary').click();
    await page.locator('#approve-blocked').scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(dir, 'approve-blocked-details.png') });
    await page.click('#confirm-ok');
    const miseRule = rule('allow_api_github_com_443');
    await miseRule.waitFor();
    await open('allow_api_github_com_443');
    const miseText = await miseRule.innerText();
    check(miseText.includes('読み取りだけ') && !miseText.includes('GET /'), 'approved: reads on every path, no rule on exactly /');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });
    // A rule by hand auditing it is refused by the server, naming the provider.
    await current.getByRole('button', { name: '規則を追加', exact: true }).click();
    await dialog.waitFor();
    await page.fill('#policy-rule', 'curl_audit');
    let adds = 0;
    const countAdd = (r) => { if (r.url().endsWith('/policy/add')) adds++; };
    page.on('request', countAdd);
    await page.keyboard.press('Escape');
    await page.waitForTimeout(200);
    check(await dialog.isHidden() && adds === 0, 'Escape cancels the add-rule form, sending nothing');
    page.off('request', countAdd);
    await current.getByRole('button', { name: '規則を追加', exact: true }).click();
    await dialog.waitFor();
    check(await page.inputValue('#policy-rule') === 'curl_audit', 'as キャンセル, it keeps what was typed');
    await page.fill('#policy-binaries', '/usr/bin/curl');
    await page.fill('#policy-host', 'api.github.com');
    await page.fill('#policy-ports', '443');
    await page.check('#approve-scope-full');
    await page.check('#policy-audit');
    const addAnswer = page.waitForResponse((r) => r.url().endsWith('/policy/add'));
    await page.click('#confirm-ok');
    check((await addAnswer).status() === 409, 'an audited rule for the provider\'s host is refused');
    const ambError = panel.locator('.panel-error', { hasText: 'この変更は OpenShell に拒否されます' });
    await ambError.waitFor();
    const ambText = await ambError.innerText();
    check(ambText.includes('プロバイダー github-personal (GitHub) の規則 _provider_github_personal') && ambText.includes('監査 (audit)') && ambText.includes('プロバイダーの規則は変えられないので'), 'the refusal names the rules, the provider and the fix');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the refusal does not make the panel scroll sideways');
    await ambError.screenshot({ path: path.join(dir, 'edit-ambiguity.png') });

    // Writes seen (git push): methods and paths, as seen.
    const gitItem = pending.locator('.proposal-item', { hasText: 'git-remote-http' });
    await gitItem.getByRole('button', { name: '承認', exact: true }).click();
    await dialog.waitFor();
    check(await page.locator('#approve-access-rules').isChecked() && await page.locator('#approve-path-rows select').first().isVisible(), 'writes seen start with methods and paths');
    // dev's GitHub provider is the shared legacy one: a policy rule, warned.
    check((await page.locator('#approve-head').innerText()).includes('github-personal (GitHub) のトークンがこの通信に付きます') && await gitItem.locator('.proposal-provider[data-kind=rule]').count() === 1, 'a proposal on a legacy provider\'s host says why it stays a rule and that the token rides along');
    const gitChips = await page.locator('#approve-binary-chips .approve-chip').evaluateAll((els) => els.map((e) => e.dataset.path));
    check(gitChips.join(' ') === '/usr/lib/git-core/git-remote-http /usr/lib/git-core/* /**', 'no version: no version candidates');
    check(await page.locator('#approve-writes').isVisible() && (await page.locator('#approve-writes').innerText()).includes('POST /mtsmfm/iwato.git/git-receive-pack') && await page.locator('#approve-summary').getAttribute('data-write') === 'true', 'a write is spelled out and the summary is marked');
    check(await fits(), 'the push dialog does not scroll sideways');
    await page.screenshot({ path: path.join(dir, 'approve-dialog-push.png') });
    await dialog.locator('.login-body').evaluate((el) => { el.scrollTop = el.scrollHeight; });
    await page.screenshot({ path: path.join(dir, 'approve-dialog-push-bottom.png') });
    await dialog.locator('.login-body').evaluate((el) => { el.scrollTop = 0; });
    // Selecting text by dragging out of the dialog onto the backdrop keeps
    // it open; a click on the backdrop closes it.
    const textBox = await page.locator('#confirm-text').boundingBox();
    await page.mouse.move(textBox.x + textBox.width / 2, textBox.y + textBox.height / 2);
    await page.mouse.down();
    await page.mouse.move(3, 3, { steps: 5 });
    await page.mouse.up();
    check(await dialog.isVisible(), 'dragging a text selection out of the dialog keeps it open');
    await page.mouse.click(3, 3);
    check(await dialog.isHidden(), 'a click on the backdrop closes the dialog');
    await gitItem.getByRole('button', { name: '承認', exact: true }).click();
    await dialog.waitFor();
    await page.keyboard.press('Escape');
    check(await dialog.isHidden(), 'Escape closes the approve dialog');

    // Approving: the change shows 反映待ち until the sandbox loads it (the
    // fake takes 3 seconds, in place of the sandbox's settings poll, about
    // every 10 seconds), then 反映済み.
    await gitItem.getByRole('button', { name: '承認', exact: true }).click();
    await page.locator('#confirm-panel').waitFor();
    await page.click('#confirm-ok');
    const github = rule('allow_github_com_443');
    const ruleBadge = github.locator('.apply-badge');
    await ruleBadge.waitFor();
    check(await ruleBadge.innerText() === '承認済み · 反映待ち' && await pending.locator('.proposal-item', { hasText: 'git-remote-http' }).count() === 0, 'an approval shows 反映待ち on its rule; the proposal left the waiting list');
    await page.screenshot({ path: path.join(dir, 'pending-badge.png'), fullPage: true });
    await github.locator('.apply-badge[data-state=applied]').waitFor({ timeout: 10000 });
    check(true, 'it turns 反映済み once the sandbox loaded it');
    await open('allow_github_com_443');
    check((await github.innerText()).includes('POST /mtsmfm/iwato.git/git-receive-pack'), 'the push was approved with the methods and paths seen');
    check(await github.locator('.policy-src[data-source=provider-host]').innerText() === 'プロバイダー github-personal の宛先' && (await github.locator('.policy-provider-host-note').innerText()).includes('github.com には github-personal (GitHub) の認証情報が付きます'), 'a rule on a provider\'s host is marked as the provider\'s');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });
    check(true, 'and fades away');

    // A rule by hand: two ports, methods and paths, audit.
    await current.getByRole('button', { name: '規則を追加', exact: true }).click();
    await dialog.waitFor();
    check(await page.locator('#approve-form').isHidden() && await page.locator('#approve-scope-readwrite').isVisible(), 'the add dialog offers the access presets, not the approval\'s form');
    await page.fill('#policy-rule', '');
    await page.fill('#policy-binaries', '/usr/bin/curl\n/usr/bin/wget');
    await page.fill('#policy-host', 'files.example.org');
    await page.fill('#policy-ports', '443, 8443');
    await page.check('#approve-scope-rules');
    await page.locator('#approve-rules .approve-rule select').first().selectOption('GET');
    await page.locator('#approve-rules .approve-rule input').first().fill('/pub/**');
    await page.click('#approve-add-rule');
    await page.locator('#approve-rules .approve-rule select').nth(1).selectOption('POST');
    await page.locator('#approve-rules .approve-rule input').nth(1).fill('/upload');
    await page.check('#policy-audit');
    check(await dialog.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the add dialog fits its width');
    await page.screenshot({ path: path.join(dir, 'add-dialog.png') });
    await page.click('#confirm-ok');
    const added = rule('allow_files_example_org_443');
    await added.waitFor();
    await open('allow_files_example_org_443');
    const addedText = await added.innerText();
    check(addedText.includes('files.example.org:443,8443 · 検査 (監査 · すべて通す)') && addedText.includes('POST /upload') && addedText.includes('/usr/bin/wget'), 'the added rule shows as entered');

    // One method and path out, then an endpoint into an existing rule.
    await added.getByRole('button', { name: 'POST /upload を外す' }).click();
    await page.click('#confirm-ok');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .policy-current')?.innerText.includes('POST /upload'));
    check(!(await current.innerText()).includes('POST /upload') && (await added.innerText()).includes('GET /pub/**'), 'a method and path came out, the rest stayed');
    await open('allow_pypi_org_443');
    await rule('allow_pypi_org_443').getByRole('button', { name: '宛先を追加' }).click();
    check(await page.locator('#policy-binaries').isHidden() && await page.locator('#policy-rule').inputValue() === 'allow_pypi_org_443', 'adding to a rule keeps its programs');
    await page.fill('#policy-host', 'mirror.example.org');
    await page.fill('#policy-ports', '443');
    await page.check('#approve-scope-read');
    await page.click('#confirm-ok');
    await current.getByText('mirror.example.org:443 · 検査 (遮断)').waitFor();
    check(true, 'an endpoint joined the rule');

    // A bad entry is refused with the server's reason.
    await current.getByRole('button', { name: '規則を追加', exact: true }).click();
    await page.fill('#policy-host', 'https://bad');
    await page.fill('#policy-ports', '443');
    await page.click('#confirm-ok');
    await panel.locator('.panel-error').waitFor();
    check((await panel.locator('.panel-error').innerText()).includes('ホスト'), 'the server refuses a bad host and says why');

    // An audited endpoint shows the requests it let through; enforcing it
    // with some of them allows only those.
    const gh = rule('allow_github_example_com_443');
    await open('allow_github_example_com_443');
    await gh.locator('.policy-observed-item').first().waitFor();
    const observed = await gh.locator('.policy-observed-item').allInnerTexts();
    check(observed.length === 4 && observed.some((t) => t.includes('GET /api/v3/repos/mtsmfm/iwato/pulls') && t.includes('×2') && t.includes('gh')), 'an audited endpoint lists the requests it logged (' + observed.length + ')');
    await gh.locator('.policy-observed-item', { hasText: 'POST /api/v3/repos' }).locator('input').uncheck();
    await gh.getByRole('button', { name: '選んだものだけ許可して遮断に切り替え' }).click();
    await dialog.waitFor();
    const seeded = await page.locator('#approve-rules .approve-rule input').evaluateAll((els) => els.map((e) => e.value));
    check(seeded.length === 3 && seeded.includes('/api/v3/repos/mtsmfm/iwato/pulls') && seeded.includes('/api/v3/user') && await page.locator('#approve-scope-rules').isChecked(), 'the picked requests fill the dialog (' + seeded.join(' ') + ')');
    await page.screenshot({ path: path.join(dir, 'enforce-dialog.png') });
    await page.click('#confirm-ok');
    await gh.getByText('github.example.com:443 · 検査 (遮断)').waitFor();
    const enforced = await gh.innerText();
    check(enforced.includes('GET /api/v3/repos/mtsmfm/iwato/pulls') && !enforced.includes('POST /api/v3/repos') && !enforced.includes('GET / (ルートだけ)') && await gh.locator('.policy-observed').count() === 0, 'the endpoint now enforces only the picked requests');

    // Requests an enforced endpoint denied: OpenShell proposes nothing for
    // them, so they show under the endpoint (from the gateway's log), and
    // picked ones are allowed through the allow dialog.
    const api = rule('allow_api_example_com_443');
    await open('allow_api_example_com_443');
    const deniedBox = api.locator('.policy-denied');
    const deniedText = await deniedBox.innerText();
    check(deniedText.includes('止められたリクエスト') && deniedText.includes('PUT /v1/items/42') && deniedText.includes('×2 · /usr/bin/curl') && deniedText.includes('許可にないメソッドとパスです') && deniedText.includes('GET /v2/status'), 'an enforced endpoint lists the requests it denied, with the program and why: ' + deniedText);
    check(deniedText.includes('拒否の規則に当たりました') && await deniedBox.locator('.policy-denied-item', { hasText: 'DELETE /v1/items/7' }).locator('input').isDisabled(), 'a request a deny rule stopped cannot be picked');
    check(deniedText.includes('プログラム /usr/bin/wget') && await deniedBox.locator('.policy-denied-item[data-kind=binary] input').count() === 0, 'a program the rule does not name shows, without a checkbox');
    check(await deniedBox.getByRole('button', { name: '選んだものを許可に追加' }).isDisabled(), 'nothing picked: nothing to add');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the denied list does not make the panel scroll sideways');
    await deniedBox.screenshot({ path: path.join(dir, 'denied.png') });
    await deniedBox.locator('.policy-denied-item', { hasText: 'PUT /v1/items/42' }).locator('input').check();
    await deniedBox.getByRole('button', { name: '選んだものを許可に追加' }).click();
    await dialog.waitFor();
    const allowRow = page.locator('#approve-rules .approve-rule').first();
    check(await page.locator('#approve-rules .approve-rule').count() === 1 && await allowRow.locator('select').inputValue() === 'PUT' && await allowRow.locator('input').inputValue() === '/v1/items/42', 'the picked request fills the allow dialog');
    await allowRow.locator('.approve-seg', { hasText: '/42' }).click();
    check(await allowRow.locator('input').inputValue() === '/v1/items/*', 'a segment click widens the path');
    check(await fits(), 'the allow dialog fits its width');
    await page.screenshot({ path: path.join(dir, 'denied-allow-dialog.png') });
    await page.click('#confirm-ok');
    await api.getByText('PUT /v1/items/*').waitFor();
    await page.waitForFunction(() => ![...document.querySelectorAll('.terminal-card.selected .policy-denied-item')].some((n) => n.innerText.includes('PUT /v1/items/42')));
    check(!(await api.locator('.policy-denied').innerText()).includes('PUT /v1/items/42') && (await api.locator('.policy-denied').innerText()).includes('GET /v2/status'), 'allowed now: it leaves the denied list, the rest stay');
    // Read-only (an access preset): OpenShell turns it into its rules and adds the POST.
    const ciRule = rule('allow_ci_tailnet_example_443');
    await open('allow_ci_tailnet_example_443');
    await ciRule.locator('.policy-denied-item', { hasText: 'POST /api/builds' }).locator('input').check();
    await ciRule.getByRole('button', { name: '選んだものを許可に追加' }).click();
    await dialog.waitFor();
    check((await page.locator('#confirm-text').innerText()).includes('「読み取りだけ」は、OpenShell が同じ意味の規則'), 'the dialog says the preset becomes rules');
    await page.click('#confirm-ok');
    await ciRule.getByText('POST /api/builds').waitFor();
    const ciText = await ciRule.innerText();
    check(ciText.includes('GET ** (すべて)') && ciText.includes('OPTIONS ** (すべて)') && !ciText.includes('読み取りだけ') && await ciRule.locator('.policy-denied').count() === 0, 'a read-only endpoint now lists GET/HEAD/OPTIONS ** and the POST');
    // The provider's rule cannot change: a rule by hand for the same host.
    await open('_provider_github_personal');
    const provDenied = prov.locator('.policy-denied');
    await provDenied.locator('.policy-denied-item', { hasText: 'POST /repos/mtsmfm/iwato/releases' }).locator('input').check();
    await provDenied.getByRole('button', { name: '止められたリクエストから規則を追加' }).click();
    await dialog.waitFor();
    check((await page.locator('#confirm-text').innerText()).includes('プロバイダーの規則「_provider_github_personal」は変えられない') && await page.locator('#policy-host').inputValue() === 'api.github.com' && await page.locator('#policy-ports').inputValue() === '443' && await page.locator('#policy-binaries').inputValue() === '/usr/bin/gh' && await page.locator('#approve-scope-rules').isChecked() && !(await page.locator('#policy-audit').isChecked()), 'a provider\'s denial starts a rule by hand: same host, enforcing, the methods and paths');
    await page.fill('#policy-rule', 'gh_releases');
    await page.screenshot({ path: path.join(dir, 'denied-provider-dialog.png') });
    await page.click('#confirm-ok');
    // The provider's token goes with it: asked once more.
    await page.locator('#confirm-text', { hasText: 'このまま許可しますか' }).waitFor();
    check((await page.locator('#confirm-text').innerText()).includes('gh_releases: api.github.com:443 (github-personal)'), 'a rule on a provider\'s host asks first, naming the provider');
    await page.screenshot({ path: path.join(dir, 'denied-provider-confirm.png') });
    await page.click('#confirm-ok');
    const releases = rule('gh_releases');
    await releases.waitFor();
    await open('gh_releases');
    check((await releases.innerText()).includes('POST /repos/mtsmfm/iwato/releases') && await provDenied.count() === 0, 'the rule by hand allows it; the provider\'s list is empty');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });

    await current.getByRole('button', { name: '履歴' }).click();
    const history = panel.locator('.policy-history');
    await history.getByText('版 6').waitFor();
    await history.locator('.policy-revision', { hasText: '版 1 ·' }).getByRole('button', { name: '見る' }).click();
    await history.locator('.policy-revision-rules').waitFor();
    const old = await history.locator('.policy-revision-rules').innerText();
    check(old.includes('allow_pypi_org_443') && !old.includes('allow_api_example_com_443') && !old.includes('_provider_') && await history.locator('.policy-revision-rules button').count() === 0, 'an old revision shows read-only (without the providers\' rules, which no revision holds)');
    await history.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(dir, 'history.png'), fullPage: true });
    // Network presets: the fake's own one, with a parameter, goes in and
    // out again.
    await current.getByRole('button', { name: 'プリセット', exact: true }).click();
    const presets = panel.locator('.policy-presets');
    const writeRow = presets.locator('.policy-preset-choice', { hasText: '社内のミラーに書き込む' });
    await writeRow.waitFor();
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the presets do not make the panel scroll sideways');
    await writeRow.locator('.preset-param').fill('mtsmfm/iwato');
    await writeRow.getByRole('button', { name: '入れる' }).click();
    check((await page.locator('#confirm-text').innerText()).includes('mtsmfm/iwato'), 'putting a preset in asks first, with its parameter');
    await page.click('#confirm-ok');
    await rule('mirror_mtsmfm_iwato').waitFor();
    await presets.locator('.policy-preset', { hasText: '(mtsmfm/iwato)' }).waitFor();
    check(true, 'the preset\'s rule is in the policy and the preset is listed as in');
    await presets.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(dir, 'presets.png'), fullPage: true });
    await presets.locator('.policy-preset', { hasText: '(mtsmfm/iwato)' }).getByRole('button', { name: '外す' }).click();
    await page.click('#confirm-ok');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .policy-current')?.innerText.includes('mirror_mtsmfm_iwato'));
    check(await presets.locator('.policy-preset', { hasText: '(mtsmfm/iwato)' }).count() === 0, 'taking the preset out removes its rule');
    // The owner's presets as YAML: a provider's host is refused inline,
    // a good one is saved and listed, then deleted.
    await presets.getByRole('button', { name: '新しいプリセット' }).click();
    const yaml = presets.locator('.preset-yaml-editor');
    await yamlFill(yaml, 'id: check-tool\nlabel: 確認用\nnetwork_policies:\n  r:\n    endpoints:\n      - { host: github.com, port: 443, protocol: rest, enforcement: enforce, access: read-only }\n    binaries: [/usr/bin/curl]\n');
    await presets.getByRole('button', { name: '保存' }).click();
    await presets.locator('.preset-editor .preset-error', { hasText: 'github.com:443' }).waitFor();
    check(true, 'the editor shows why a preset on a provider\'s host is refused');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the YAML editor does not make the panel scroll sideways');
    await presets.locator('.preset-editor').screenshot({ path: path.join(dir, 'preset-editor.png') });
    // CodeMirror, highlighted, and what is typed is what is saved.
    check(await yaml.locator('.cm-editor .cm-lineNumbers').count() === 1 && await yaml.locator('.cm-editor').evaluate((el) => {
      const key = [...el.querySelectorAll('.cm-line span')].find((s) => s.textContent === 'id');
      return !!key && getComputedStyle(key).color !== getComputedStyle(key.closest('.cm-line')).color;
    }), 'the preset YAML is CodeMirror with line numbers and highlighting');
    await yamlFill(yaml, (await yamlValue(yaml)).replace('github.com', 'tools.example.com'));
    await yaml.locator('.cm-content').click();
    await page.keyboard.press('ControlOrMeta+End');
    await page.keyboard.type('# 手で入力\n');
    await presets.getByRole('button', { name: '保存' }).click();
    await presets.locator('.policy-preset-choice', { hasText: '確認用' }).waitFor();
    check(await presets.locator('.preset-editor').count() === 0, 'a saved preset is listed and the editor closes');
    await presets.getByRole('button', { name: 'check-tool.yaml の YAML を編集' }).click();
    const reopened = await yamlValue(presets.locator('.preset-yaml-editor'));
    check(reopened.includes('tools.example.com') && reopened.endsWith('# 手で入力\n'), 'the saved file opens again with what was typed');
    await presets.locator('.preset-editor').getByRole('button', { name: '削除' }).click();
    await page.click('#confirm-ok');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .policy-presets')?.innerText.includes('check-tool.yaml'));
    check(true, 'a deleted preset leaves the list');
    await current.getByRole('button', { name: 'プリセットを閉じる' }).click();
    // The policy as YAML: the view replaces the lists and fills the tab, a
    // provider's host is refused, a rule is added after the summary, edits
    // survive switching views, and a save against an older version is refused.
    await panel.evaluate((el) => { el.scrollTop = el.scrollHeight; });
    await seg('yaml').click();
    const yamlBox = panel.locator('.policy-yaml');
    const yamlArea = yamlBox.locator('.policy-yaml-editor');
    await yamlBox.locator('h4', { hasText: 'ポリシーの YAML (版' }).waitFor();
    await yamlArea.locator('.cm-editor').waitFor();
    check(await panel.locator('.policy-list').isHidden() && await seg('yaml').getAttribute('aria-pressed') === 'true', 'YAML replaces the lists');
    const edBox = await yamlArea.locator('.cm-editor').boundingBox();
    const panelBox = await panel.boundingBox();
    check(await inView(yamlBox.getByRole('button', { name: '変更を確認' })) && await inView(yamlBox.locator('.policy-yaml-head').getByRole('button', { name: '読み込み直す' })) && edBox.y >= panelBox.y && edBox.y < page.viewportSize().height - 150, 'the YAML\'s actions and editor are in view after switching');
    check(edBox.height > panelBox.height * 0.5 && await yamlBox.locator('details.policy-yaml-fixed').evaluate((el) => !el.open && el.getBoundingClientRect().top >= 0), 'the editor fills the tab, the fixed settings folded under it');
    check(await page.evaluate(() => localStorage.getItem('iwato-policy-view')) === 'yaml', 'the view is remembered');
    const waitingCount = await card.locator('.card-tab', { hasText: 'ポリシー' }).locator('.tab-badge').innerText().catch(() => '');
    check(await seg('list').locator('.policy-view-mark').isVisible() && (await seg('list').locator('.policy-view-mark').innerText()) === waitingCount.trim(), '一覧 shows how many proposals wait while the YAML shows');
    await page.screenshot({ path: path.join(dir, 'policy-yaml.png') });
    check(await yamlBox.getByRole('textbox', { name: 'ポリシーの YAML' }).count() === 1, 'the policy YAML is a labelled text box');
    const loaded = await yamlValue(yamlArea);
    check(loaded.startsWith('network_policies:') && loaded.includes('  allow_api_example_com_443:') && !loaded.includes('_provider_') && (await yamlBox.locator('details summary').innerText()).includes('作成時に決まる設定'), 'the YAML holds the rules, the fixed settings apart');
    await yamlBox.locator('details summary').click();
    check(await yamlBox.locator('.policy-yaml-fixed .cm-content').getAttribute('aria-readonly') === 'true', 'the fixed settings are read-only YAML');
    await yamlBox.locator('details summary').click();
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the policy YAML does not make the panel scroll sideways');
    const withRule = (host) => loaded.replace('network_policies:\n', 'network_policies:\n  yaml_tool:\n    endpoints:\n      - { host: ' + host + ', port: 443, protocol: rest, enforcement: enforce, access: read-only }\n    binaries: [/usr/bin/curl]\n');
    await yamlFill(yamlArea, withRule('api.github.com'));
    await yamlBox.getByRole('button', { name: '変更を確認' }).click();
    await page.locator('#confirm-text', { hasText: 'このまま許可しますか' }).waitFor();
    check((await page.locator('#confirm-text').innerText()).includes('yaml_tool: api.github.com:443 (github-personal)'), 'a rule on an attached provider\'s host asks first');
    await page.screenshot({ path: path.join(dir, 'policy-yaml-provider.png') });
    await page.click('#confirm-cancel');
    await yamlBox.locator('.preset-error', { hasText: 'api.github.com:443' }).waitFor();
    check(true, 'declined, it is not saved and says why');
    await yamlFill(yamlArea, withRule('tools.example.com'));
    // Unsaved edits survive switching to 一覧 and back.
    check(await seg('yaml').locator('.policy-view-mark').innerText() === '未保存', 'unsaved edits are marked on the YAML switch');
    await seg('list').click();
    await current.waitFor();
    check(await yamlBox.isHidden() && await seg('yaml').locator('.policy-view-mark').isVisible(), 'the lists show, the edits still marked unsaved');
    await seg('yaml').click();
    check((await yamlValue(yamlArea)) === withRule('tools.example.com'), 'unsaved edits survive a switch');
    await yamlBox.getByRole('button', { name: '変更を確認' }).click();
    await dialog.waitFor();
    check((await page.locator('#confirm-text').innerText()).includes('追加: yaml_tool (tools.example.com:443)'), 'saving shows what changes first');
    await page.screenshot({ path: path.join(dir, 'policy-yaml-confirm.png') });
    await page.click('#confirm-ok');
    await panel.locator('.proposal-done', { hasText: 'ポリシーを変えました' }).waitFor();
    await page.waitForFunction(() => { const el = document.querySelector('.terminal-card.selected .policy-yaml-editor .cm-editor'); return el && window.IwatoCodeMirror.EditorView.findFromDOM(el).state.doc.toString().includes('yaml_tool:'); });
    check(await yamlBox.isVisible() && await seg('yaml').locator('.policy-view-mark').isHidden(), 'applied, the YAML stays open with the new version');
    await seg('list').click();
    await rule('yaml_tool').waitFor();
    check(true, 'the applied rule is in the policy');
    // An edit pending while the policy changes elsewhere: warned, kept, and refused on save.
    await seg('yaml').click();
    await yamlFill(yamlArea, (await yamlValue(yamlArea)).replace('yaml_tool', 'yaml_tool2'));
    await seg('list').click();
    await open('yaml_tool');
    await rule('yaml_tool').getByRole('button', { name: '規則「yaml_tool」を外す' }).click();
    await page.click('#confirm-ok');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .policy-current')?.innerText.includes('yaml_tool'));
    await seg('yaml').click();
    await yamlBox.locator('.policy-yaml-stale', { hasText: '読み込んだ後でポリシーが変わりました' }).waitFor();
    check((await yamlValue(yamlArea)).includes('yaml_tool2'), 'a policy change underneath warns and keeps the edits');
    await page.screenshot({ path: path.join(dir, 'policy-yaml-stale.png') });
    await yamlBox.getByRole('button', { name: '変更を確認' }).click();
    await yamlBox.locator('p.preset-error:not(.policy-yaml-stale)', { hasText: '読み込んだ後でポリシーが変わりました' }).waitFor();
    check(true, 'a save against an older version is refused');
    await yamlBox.locator('.policy-yaml-head').getByRole('button', { name: '読み込み直す' }).click();
    await page.click('#confirm-ok');
    await page.waitForFunction(() => { const el = document.querySelector('.terminal-card.selected .policy-yaml-editor .cm-editor'); return el && !window.IwatoCodeMirror.EditorView.findFromDOM(el).state.doc.toString().includes('yaml_tool'); });
    const reloaded = await yamlValue(yamlArea);
    check(reloaded.startsWith('network_policies:') && !reloaded.includes('yaml_tool'), 'reloading puts the current policy in the editor');
    check(await yamlBox.locator('p.preset-error:not(.policy-yaml-stale)').isHidden() && await yamlBox.locator('.policy-yaml-stale').isHidden(), 'reloading reads the current policy');
    await seg('list').click();
    // A stopped sandbox keeps its policy tab: the rules can be fixed before starting it again.
    await card.getByRole('button', { name: 'その他の操作' }).click();
    await card.getByRole('menuitem', { name: '停止', exact: true }).click();
    await page.click('#confirm-ok');
    await card.locator('.badge', { hasText: '停止中' }).waitFor();
    await card.locator('.card-tab', { hasText: 'ポリシー' }).click();
    await panel.getByText('新しい提案は届きません', { exact: false }).waitFor();
    check(await panel.isVisible() && (await current.innerText()).includes('allow_files_example_org_443'), 'a stopped sandbox shows its policy');
    await rule('allow_files_example_org_443').getByRole('button', { name: '規則「allow_files_example_org_443」を外す' }).click();
    await page.click('#confirm-ok');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .policy-current')?.innerText.includes('allow_files_example_org_443'));
    check(true, 'a stopped sandbox\'s rule can be taken out');
    await page.screenshot({ path: path.join(dir, 'stopped-policy.png'), fullPage: true });
    // experiment has a GitHub provider of its own profile: its proposals
    // widen the provider's scope, and Iwato closes them once applied.
    if (phone) await page.click('#menu-button').catch(() => {});
    await page.locator('#sandbox-nav').getByText('experiment', { exact: true }).first().click();
    const exp = page.locator('.terminal-card.selected');
    await exp.locator('.card-title, h2, h3', { hasText: 'experiment' }).first().waitFor().catch(() => {});
    const expStart = exp.getByRole('button', { name: '起動' });
    if (await expStart.isVisible() && await expStart.isEnabled()) { await expStart.click(); await page.waitForTimeout(1500); }
    await exp.locator('.card-tab', { hasText: 'ポリシー' }).click();
    const expPending = exp.locator('.policy-pending');
    const prItem = expPending.locator('.proposal-item', { hasText: 'octo-org/octo-repo' });
    await prItem.waitFor();
    check((await prItem.locator('.proposal-provider[data-kind=scope]').innerText()).includes('github-work (GitHub) の宛先 · 承認するとプロバイダーの範囲を広げます (リポジトリ octo-org/octo-repo を追加)'), 'a proposal on a scoped provider\'s host offers to widen the scope');
    check((await expPending.locator('.proposal-item', { hasText: 'iwato リポジトリにブランチを push する' }).locator('.proposal-provider').innerText()).includes('もう範囲内'), 'a push the scope allows already says so');
    await prItem.getByRole('button', { name: 'プロバイダーの範囲を広げて承認' }).click();
    await dialog.waitFor();
    const scopeText = await page.locator('#approve-grant').innerText();
    check((await page.locator('#confirm-text').innerText()).includes('プロバイダーの範囲で許可しますか') && (await page.locator('#approve-head .approve-host').innerText()) === 'api.github.com' && scopeText.includes('書き込めるリポジトリに octo-org/octo-repo を加えます') && await page.locator('#approve-grant .approve-grant-line[data-write=true]').count() === 1 && await page.locator('#approve-form').isHidden() && await fits(), 'the dialog says what the scope gains, marked as a write, without the policy form');
    await page.screenshot({ path: path.join(dir, 'approve-scope.png') });
    await page.click('#confirm-ok');
    await prItem.locator('.proposal-provider[data-kind=resolving]').waitFor();
    check(await prItem.getByRole('button', { name: 'プロバイダーの範囲を広げて承認' }).isDisabled(), 'while the sandbox applies it, the proposal waits with its buttons off');
    await page.screenshot({ path: path.join(dir, 'approve-scope-resolving.png'), fullPage: true });
    await prItem.waitFor({ state: 'detached', timeout: 30000 });
    const expRejected = exp.locator('.policy-rejected');
    await expRejected.getByText('github-work の範囲を広げて閉じました', { exact: false }).waitFor();
    check(!(await exp.locator('.policy-current').innerText()).includes('allow_api_github_com_443'), 'closed after the sandbox applied the scope, with no policy rule');
    await page.screenshot({ path: path.join(dir, 'approve-scope-closed.png'), fullPage: true });

    if (!phone) {
      await page.setViewportSize({ width: 700, height: 860 });
      await page.waitForTimeout(200);
      check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'a narrow window does not scroll sideways');
      await page.screenshot({ path: path.join(dir, 'narrow.png'), fullPage: true });
    }
    console.log('screenshots in ' + dir);
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed'); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
