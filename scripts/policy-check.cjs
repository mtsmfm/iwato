'use strict';
// The policy tab in a real browser against `serve -fake`: the three lists
// (waiting, the effective policy with provider rules and rules from
// proposals, rejected), the opened proposal (narrowing, the token, the host
// checks' warnings, the diff, rejecting with a reason, reject-all), undoing
// a proposal, putting an approved proposal's rule back, the rules piece by
// piece, the token on an endpoint, adding a rule through the dialog, taking
// a method and path out, the history with its diffs and revert, a drift,
// and the policy as YAML. Saves
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
    check(pendingText.startsWith('承認待ち (6)') && pendingText.includes('エージェントの提案') && pendingText.includes('OpenShell の自動提案') && pendingText.includes('37 回') && pendingText.includes('iwato リポジトリにブランチを push する'), 'waiting: the agent\'s and OpenShell\'s proposals with their counts and intent');
    // The GitHub provider's initial rule (ADR 0022): a rule of the policy,
    // marked with the credential its endpoints carry.
    const prov = rule('provider_github_personal');
    await open('provider_github_personal');
    const provText = await prov.innerText();
    check((await prov.locator('.policy-src[data-source=provider-host]').innerText()) === 'トークン: github-personal' && provText.includes('api.github.com:443 /** · 検査 (遮断)'), 'the provider\'s initial rule is marked with its credential: ' + provText.slice(0, 200));
    const ghRule = rule('allow_github_example_com_443');
    check(await ghRule.locator('.policy-chunk').count() === 2 && await ghRule.getByRole('button', { name: '規則「allow_github_example_com_443」を外す' }).count() === 0, 'a rule from two proposals lists both, with 取り消す and no 外す');
    await open('allow_registry_npmjs_org_443');
    check((await rule('allow_registry_npmjs_org_443').innerText()).includes('/sandbox/.local/share/mise/installs/node/*/bin/node'), 'a proposal approved with a glob shows it');
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

    // Undo: the proposal goes back to waiting; the endpoint added after
    // goes with the rule.
    const dialog = page.locator('#confirm-panel');
    await rule('allow_registry_npmjs_org_443').locator('.policy-chunk').getByRole('button', { name: '取り消す' }).click();
    await dialog.waitFor();
    const undoText = await page.locator('#confirm-text').innerText();
    check(undoText.includes('規則ごと消えます') && undoText.match(/\bnpm\.pkg\.github\.com:443\b/) !== null, 'undoing says what goes with the rule');
    await page.screenshot({ path: path.join(dir, 'undo-confirm.png') });
    await page.click('#confirm-ok');
    await pending.locator('.review-item', { hasText: /^registry\.npmjs\.org/ }).waitFor();
    check(await rule('allow_registry_npmjs_org_443').count() === 0 && (await current.locator('.panel-meta .apply-badge').first().innerText()) === '変更済み · 反映待ち', 'the rule is gone, the proposal waits again, the policy shows 反映待ち');
    // Put back and undone: approved without its rule.
    await unlinked.getByRole('button', { name: '規則を戻して取り消す' }).click();
    await dialog.waitFor();
    await page.click('#confirm-ok');
    await pending.locator('.review-item', { hasText: /^mise-versions\.jdx\.dev/ }).waitFor();
    check(await unlinked.count() === 0 && await rule('allow_mise_versions_jdx_dev_443').count() === 0, 'the rule went back and out again: the proposal waits again');
    // A rejected proposal opens the same review; it goes as proposed.
    // A host (a regex, dots escaped) as a whole word, not a substring of
    // another host.
    const hostRE = (host) => new RegExp('^' + host.source + '(?![a-z0-9.-])');
    const openRejected = async (host) => {
      const row = rejectedList.locator('.proposal-item').filter({ has: page.locator('.proposal-where', { hasText: hostRE(host) }) });
      await row.getByRole('button', { name: '承認する', exact: true }).click();
      await row.locator('.review-host').filter({ hasText: hostRE(host) }).waitFor();
      await page.waitForFunction(() => { const b = document.querySelector('.terminal-card.selected .policy-rejected .review-detail'); return b && !b.querySelector('.review-warn[data-level=wait]') && b.querySelector('.review-diff-body').textContent; });
      return row.locator('.review-detail');
    };
    const asked = await openRejected(/mise-versions\.jdx\.dev/);
    check(await asked.locator('input:visible').count() === 0 && await asked.getByRole('button', { name: '却下', exact: true }).count() === 0, 'a rejected proposal has no checkboxes and no 却下');
    check(/^検査せずにすべての通信を通す \(L4\) — mise-versions\.jdx\.dev$/.test((await asked.locator('.review-rule').innerText()).trim()), 'its one row is the L4 it is approved as');
    check(/提案どおりにしか承認できない/.test(await asked.locator('.review-rules + .panel-meta').innerText()), 'a note says why it cannot be narrowed');
    check(/検査せずに許可します \(L4\)/.test(await asked.locator('.review-warn[data-level=amber]').first().innerText()), 'its L4 is warned');
    check(/^\s*host: mise-versions\.jdx\.dev$/m.test((await asked.locator('.review-diff-body').textContent()).replace(/^\+\s*-?/gm, '')), 'the preview shows the rule it adds');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the opened rejected proposal fits');
    await asked.screenshot({ path: path.join(dir, 'rejected-review.png') });
    const askedAnswer = page.waitForResponse((r) => r.url().endsWith('/proposals/approve'));
    await asked.getByRole('button', { name: '承認', exact: true }).click();
    check((await askedAnswer).status() === 200, 'it is approved from there');
    await rule('allow_mise_versions_jdx_dev_443').waitFor();
    check((await rejectedList.innerText()).startsWith('却下した提案 (1)'), 'it left the rejected list for the policy');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });
    await page.screenshot({ path: path.join(dir, 'lists-after.png'), fullPage: true });

    // The opened proposal (ADR 0022): beside the list on a PC, under it on
    // a phone. What it allows, narrowed by unchecking; the host checks;
    // the rule it adds as YAML.
    const reviewBox = pending.locator('.review-detail');
    const item = (host, also) => { const l = pending.locator('.review-item').filter({ hasText: hostRE(host) }); return also ? l.filter({ hasText: also }) : l; };
    const openItem = async (host, also) => { await item(host, also).click(); await reviewBox.locator('.review-host').filter({ hasText: hostRE(host) }).waitFor(); };
    const rows = () => reviewBox.locator('.review-rule').evaluateAll((els) => els.map((e) => (e.querySelector('input').checked ? '+ ' : '- ') + e.innerText.replace(/\s+/g, ' ').trim()));
    const fits = () => panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1 && document.documentElement.scrollWidth <= window.innerWidth + 1);
    const approve = async () => {
      const answer = page.waitForResponse((r) => r.url().endsWith('/proposals/approve'));
      await reviewBox.getByRole('button', { name: '承認', exact: true }).click();
      return (await answer).status();
    };
    const settled = () => page.waitForFunction(() => { const b = document.querySelector('.terminal-card.selected .review-detail'); return b && !b.querySelector('.review-warn[data-level=wait]'); });
    await openItem(/downloads\.claude\.ai/);
    await settled();
    check((await rows()).join(' | ') === '+ GET /claude-code-releases/2.1.294/manifest.json | + GET /claude-code-releases/2.1.294/linux-x64/claude', 'the rules seen, checked: ' + (await rows()).join(' | '));
    const reviewText = await reviewBox.innerText();
    check(reviewText.includes('OpenShell が止めた接続から作った下書き') && reviewText.includes('/sandbox/.local/share/claude/versions/2.1.293 (参考。許可はプログラムを問いません)') && reviewText.includes('あり (HTTP の要求を検査)'), 'the reason, the program as information, the inspection');
    const okWarn = await reviewBox.locator('.review-warn[data-level=ok]').innerText();
    check(okWarn.includes('危険ドメインのチェック: 問題なし') && okWarn.includes('1.1.1.2 の判定は問題なし') && okWarn.includes('登録から 18 年'), 'nothing found: the facts: ' + okWarn);
    await reviewBox.locator('.review-rule', { hasText: 'linux-x64' }).locator('input').uncheck();
    check((await reviewBox.locator('.review-rule', { hasText: 'linux-x64' }).getAttribute('data-off')) === 'true', 'an unchecked rule is struck through');
    await reviewBox.locator('.review-diff > summary').click();
    await page.waitForFunction(() => { const d = document.querySelector('.terminal-card.selected .review-diff-body'); return d && d.textContent.includes('manifest.json') && !d.textContent.includes('linux-x64'); });
    check((await reviewBox.locator('.review-diff-body').innerText()).includes('+   allow_downloads_claude_ai_443:'), 'the diff adds the narrowed rule');
    check(await fits(), 'the opened proposal does not scroll sideways');
    await reviewBox.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(dir, 'review.png') });
    check(await approve() === 200, 'approved');
    const raw = rule('allow_downloads_claude_ai_443');
    await raw.waitFor();
    await open('allow_downloads_claude_ai_443');
    const rawText = await raw.innerText();
    check(rawText.includes('GET /claude-code-releases/2.1.294/manifest.json') && !rawText.includes('linux-x64') && rawText.includes('すべてのプログラム'), 'the approved rule is for every program, with only the rule kept: ' + rawText);
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });

    // Not inspected, to where the attached GitHub provider's token goes
    // (uploads.github.com, bound in its initial rule): GET and HEAD to
    // start with; L4 would carry the credential uninspected, which OpenShell
    // refuses, so the preview says so and 承認 waits.
    await openItem(/uploads\.github\.com/);
    await settled();
    check((await rows()).join(' | ') === '+ GET /** (すべて) | + HEAD /** (すべて) | - POST /** (すべて) | - PUT /** (すべて) | - PATCH /** (すべて) | - DELETE /** (すべて) | - 検査せずにすべての通信を通す (L4) — uploads.github.com', 'an L4 proposal starts with GET and HEAD: ' + (await rows()).join(' | '));
    check((await reviewBox.locator('.review-warn[data-level=amber]').allInnerTexts()).some((w) => w.includes('提案は L4')), 'the L4 proposal is noted');
    await reviewBox.locator('.review-grant').first().waitFor();
    check(await reviewBox.locator('.review-grant input').first().isChecked() && (await reviewBox.locator('.review-grant').first().innerText()).includes('github-personal のトークンを付ける'), 'narrowed under the provider\'s endpoint, the token is on');
    await reviewBox.locator('.review-rule[data-kind=l4] input').check();
    check(await reviewBox.locator('.review-rule', { hasText: 'POST' }).locator('input').isDisabled(), 'L4 makes the methods moot');
    await reviewBox.locator('.review-error', { hasText: 'github-personal' }).waitFor();
    check(await reviewBox.getByRole('button', { name: '承認', exact: true }).isDisabled(), 'OpenShell would refuse it: 承認 waits');
    await reviewBox.screenshot({ path: path.join(dir, 'review-refused.png') });
    await reviewBox.locator('.review-rule[data-kind=l4] input').uncheck();
    await page.waitForFunction(() => { const b = document.querySelector('.terminal-card.selected .review-detail .review-error'); return b && b.hidden; });
    check(await approve() === 200, 'GET and HEAD go through');
    const uploads = rule('allow_uploads_github_com_443');
    await uploads.waitFor();
    await open('allow_uploads_github_com_443');
    check((await uploads.locator('.policy-src[data-source=provider-host]').innerText()) === 'トークン: github-personal', 'it carries the credential');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });

    // OpenShell's proposal for api.github.com whose only rule is a GET /
    // nobody saw: the methods on every path, reads checked.
    await openItem(/api\.github\.com/, 'OpenShell の自動提案');
    check((await rows()).slice(0, 3).join(' | ') === '+ GET /** (すべて) | + HEAD /** (すべて) | - POST /** (すべて)', 'a placeholder GET / opens as reads on every path');
    // In English, then in Japanese again.
    const relist = async (lang) => {
      await page.evaluate((l) => window.IwatoI18n.setPreference(l), lang);
      await Promise.all([page.waitForResponse((r) => r.url().endsWith('/proposals') && r.request().method() === 'GET'), panel.locator('[data-i18n="policy.refresh"]').click()]);
    };
    await relist('en');
    await reviewBox.getByRole('button', { name: 'Approve', exact: true }).waitFor();
    const enText = await reviewBox.innerText();
    check(enText.includes('What it allows') && enText.includes('(for reference: the approval is for every program)') && !/[぀-ヿ]/.test(enText), 'the opened proposal in English');
    await relist('ja');
    check(await approve() === 200, 'approved');
    const miseRule = rule('allow_api_github_com_443');
    await miseRule.waitFor();
    await open('allow_api_github_com_443');
    const miseText = await miseRule.innerText();
    check(miseText.includes('GET /** (すべて)') && miseText.includes('HEAD /** (すべて)') && !miseText.includes('GET / (ルートだけ)'), 'approved: GET and HEAD on every path, no rule on exactly /: ' + miseText);
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });
    // A rule by hand enforcing where the policy audits (GitHub Enterprise)
    // is refused by the server, naming the rules.
    await current.getByRole('button', { name: '規則を追加', exact: true }).click();
    await dialog.waitFor();
    check(await page.locator('#policy-rule').isVisible() && await page.locator('#policy-binaries').count() === 0, 'the add form asks no program (every program)');
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
    await page.fill('#policy-host', 'github.example.com');
    await page.fill('#policy-ports', '443');
    await page.check('#approve-scope-full');
    const addAnswer = page.waitForResponse((r) => r.url().endsWith('/policy/add'));
    await page.click('#confirm-ok');
    check((await addAnswer).status() === 409, 'an enforced rule where the policy audits is refused');
    const ambError = panel.locator('.panel-error', { hasText: 'この変更は OpenShell に拒否されます' });
    await ambError.waitFor();
    const ambText = await ambError.innerText();
    check(ambText.includes('規則 allow_github_example_com_443') && ambText.includes('監査 (audit)'), 'the refusal names the rules and what differs: ' + ambText);
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the refusal does not make the panel scroll sideways');
    await ambError.screenshot({ path: path.join(dir, 'edit-ambiguity.png') });

    // Writes seen (git push), under the GitHub provider's github.com/**:
    // its credential by default, said in the list.
    await openItem(/github\.com/);
    check((await rows()).join(' | ') === '+ GET /mtsmfm/iwato.git/info/refs | + POST /mtsmfm/iwato.git/git-receive-pack', 'writes seen: as seen');
    check(await reviewBox.locator('.review-grant input').first().isChecked() && (await item(/github\.com/).locator('.review-chip').allInnerTexts()).includes('トークン'), 'the token is on by default, marked in the list');
    check((await reviewBox.locator('.review-rule[data-write=true] .review-method').innerText()) === 'POST', 'a write is marked');
    await reviewBox.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(dir, 'review-push.png') });
    // Approving: the change shows 反映待ち until the sandbox loads it (the
    // fake takes 3 seconds, in place of the sandbox's settings poll, about
    // every 10 seconds), then 反映済み.
    check(await approve() === 200, 'approved');
    const github = rule('allow_github_com_443');
    const ruleBadge = github.locator('.apply-badge');
    await ruleBadge.waitFor();
    check(await ruleBadge.innerText() === '承認済み · 反映待ち' && await item(/git-remote-http/).count() === 0, 'an approval shows 反映待ち on its rule; the proposal left the waiting list');
    await page.screenshot({ path: path.join(dir, 'pending-badge.png'), fullPage: true });
    await github.locator('.apply-badge[data-state=applied]').waitFor({ timeout: 10000 });
    check(true, 'it turns 反映済み once the sandbox loaded it');
    await open('allow_github_com_443');
    check((await github.innerText()).includes('POST /mtsmfm/iwato.git/git-receive-pack'), 'the push was approved with the methods and paths seen');
    check(await github.locator('.policy-src[data-source=provider-host]').innerText() === 'トークン: github-personal', 'the approved rule carries the provider\'s credential');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });
    check(true, 'and fades away');
    // The token can come off an endpoint more specific than the provider's.
    await github.getByRole('button', { name: 'github-personal のトークンを外す' }).first().click();
    await dialog.waitFor();
    check((await page.locator('#confirm-text').innerText()).includes('トークンなしで送られます'), 'taking the token off asks first');
    await page.click('#confirm-ok');
    await github.getByRole('button', { name: 'github-personal のトークンを付ける' }).first().waitFor();
    await github.getByRole('button', { name: 'github-personal のトークンを付ける' }).first().click();
    await page.click('#confirm-ok');
    await page.waitForFunction(() => [...document.querySelectorAll('.terminal-card.selected .policy-binding')].every((b) => b.textContent.endsWith('外す')));
    check(true, 'and back on');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });

    // The host checks: a domain the filtering resolver blocks, registered
    // 6 days ago, and an exfiltration service. Warnings, not blocks.
    const term = card.locator('.card-tab', { hasText: 'ターミナル' });
    await term.click();
    const connect = card.getByRole('button', { name: /^接続/ });
    if (await connect.isVisible() && await connect.isEnabled()) await connect.click();
    await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
    for (const cmd of ['propose cdn-x7k2q.top', 'propose webhook.site']) {
      await card.locator('.iw-input').focus();
      await page.keyboard.type(cmd);
      await page.keyboard.press('Enter');
      await page.waitForTimeout(300);
    }
    await card.locator('.card-tab', { hasText: 'ポリシー' }).click();
    await item(/cdn-x7k2q\.top/).waitFor();
    await settled();
    const chipsOf = (text) => item(text).locator('.review-chip').evaluateAll((els) => els.map((e) => e.dataset.level + ' ' + e.textContent));
    await page.waitForFunction(() => [...document.querySelectorAll('.terminal-card.selected .review-item')].some((n) => /(^|[^a-z0-9.-])cdn-x7k2q\.top(?![a-z0-9.-])/.test(n.textContent) && n.textContent.includes('危険ドメイン')));
    check((await chipsOf(/cdn-x7k2q\.top/)).join(', ') === 'red 危険ドメイン, amber 新しいドメイン, amber 検査なし', 'the list marks the dangerous domain: ' + (await chipsOf(/cdn-x7k2q\.top/)).join(', '));
    await openItem(/cdn-x7k2q\.top/);
    await settled();
    const warns = await reviewBox.locator('.review-warn').evaluateAll((els) => els.map((e) => e.dataset.level + ' ' + e.innerText.replace(/\s+/g, ' ')));
    check(warns[0].startsWith('red ! cdn-x7k2q.top は危険なドメインです') && warns[1].startsWith('amber ▲ cdn-x7k2q.top は登録から 6 日のドメインです') && !warns.some((w) => w.startsWith('ok')), 'red, then amber: ' + warns.join(' / '));
    check(await reviewBox.getByRole('button', { name: '承認', exact: true }).isEnabled(), 'warnings never block');
    await reviewBox.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(dir, 'review-danger.png') });
    await openItem(/webhook\.site/);
    await settled();
    check(/webhook\.site は外部送信サービスです/.test(await reviewBox.locator('.review-warn[data-level=red]').innerText()), 'an exfiltration service is red');
    // Rejecting with a reason; then まとめて却下 asks in the page.
    await reviewBox.locator('.review-reason').fill('使いません');
    const rejectAnswer = page.waitForRequest((r) => r.url().endsWith('/proposals/reject'));
    await reviewBox.getByRole('button', { name: '却下', exact: true }).click();
    check(JSON.parse((await rejectAnswer).postData()).reason === '使いません', 'the reason goes with the rejection');
    await item(/webhook\.site/).waitFor({ state: 'detached' });
    const waitingNow = await pending.locator('.review-item').count();
    await pending.getByRole('button', { name: 'まとめて却下' }).click();
    await dialog.waitFor();
    check((await page.locator('#confirm-text').innerText()).includes('承認待ちの提案 ' + waitingNow + ' 件をすべて却下'), 'reject-all asks first, with the count');
    await page.screenshot({ path: path.join(dir, 'reject-all.png') });
    await page.click('#confirm-ok');
    await pending.getByText('承認待ちはありません。').waitFor();
    check(/cdn-x7k2q\.top/.test(await rejectedList.innerText()), 'all were rejected');
    // Rejected or waiting, a dangerous host is red.
    for (const [host, says] of [[/cdn-x7k2q\.top/, /^!\s*cdn-x7k2q\.top は危険なドメインです/], [/webhook\.site/, /^!\s*webhook\.site は外部送信サービスです/]]) {
      const box = await openRejected(host);
      check(says.test(await box.locator('.review-warn[data-level=red]').first().innerText()), 'a rejected proposal for a dangerous host shows the red warning: ' + host.source);
      await box.getByRole('button', { name: '閉じる', exact: true }).click();
      await box.waitFor({ state: 'detached' });
    }

    // A rule by hand: two ports, methods and paths, audit.
    await current.getByRole('button', { name: '規則を追加', exact: true }).click();
    await dialog.waitFor();
    check(await page.locator('#approve-scope-readwrite').isVisible(), 'the add dialog offers the access presets');
    await page.fill('#policy-rule', '');
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
    check(addedText.includes('files.example.org:443,8443 · 検査 (監査 · すべて通す)') && addedText.includes('POST /upload') && addedText.includes('すべてのプログラム'), 'the added rule shows as entered, for every program');

    // One method and path out, then an endpoint into an existing rule.
    await added.getByRole('button', { name: 'POST /upload を外す' }).click();
    await page.click('#confirm-ok');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .policy-current')?.innerText.includes('POST /upload'));
    check(!(await current.innerText()).includes('POST /upload') && (await added.innerText()).includes('GET /pub/**'), 'a method and path came out, the rest stayed');
    await open('allow_pypi_org_443');
    await rule('allow_pypi_org_443').getByRole('button', { name: '宛先を追加' }).click();
    check(await page.locator('#policy-rule').inputValue() === 'allow_pypi_org_443', 'adding to a rule names it');
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
    check((await page.locator('#confirm-text').innerText()).includes('「読み取りだけ」を同じ意味の規則'), 'the dialog says the preset becomes rules');
    await page.click('#confirm-ok');
    await ciRule.getByText('POST /api/builds').waitFor();
    const ciText = await ciRule.innerText();
    check(ciText.includes('GET ** (すべて)') && ciText.includes('OPTIONS ** (すべて)') && !ciText.includes('読み取りだけ') && await ciRule.locator('.policy-denied').count() === 0, 'a read-only endpoint now lists GET/HEAD/OPTIONS ** and the POST');
    // A denial on the provider's initial rule: allowed on that endpoint,
    // which keeps the credential.
    const provDenied = prov.locator('.policy-denied');
    await provDenied.locator('.policy-denied-item', { hasText: 'POST /repos/mtsmfm/iwato/releases' }).locator('input').check();
    await provDenied.getByRole('button', { name: '選んだものを許可に追加' }).click();
    await dialog.waitFor();
    await page.click('#confirm-ok');
    await prov.getByText('POST /repos/mtsmfm/iwato/releases').waitFor();
    check(await provDenied.count() === 0 && (await prov.locator('.policy-src[data-source=provider-host]').innerText()) === 'トークン: github-personal', 'the provider\'s endpoint allows it now, still with the credential');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });

    // The history: the policy repository's commits, each with its diff
    // and, but the first, 取り消す.
    await current.getByRole('button', { name: '履歴' }).click();
    const history = panel.locator('.policy-history');
    await history.locator('.policy-commit').first().waitFor();
    const commits = await history.locator('.policy-commit-what').allInnerTexts();
    check(commits.some((c) => c.startsWith('Approve proposal fake-git as rule allow_github_com_443')) && /^Start from the presets|^Import the policy/.test(commits.at(-1)) && await history.locator('.policy-commit').last().getByRole('button', { name: /を取り消す$/ }).count() === 0, 'the commits, newest first; the first cannot be undone: ' + commits.slice(-2).join(' / '));
    const presetCommit = history.locator('.policy-commit').filter({ hasText: 'Allow requests on' }).first();
    await presetCommit.getByRole('button', { name: /の差分を見る$/ }).click();
    await history.locator('.policy-commit-diff .diff-add').first().waitFor();
    check((await history.locator('.policy-commit-diff').innerText()).includes('+'), 'a commit shows its diff');
    await history.scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(dir, 'history.png'), fullPage: true });
    // Undo the method and path added to the provider's endpoint.
    await presetCommit.getByRole('button', { name: /を取り消す$/ }).click();
    await dialog.waitFor();
    check((await page.locator('#confirm-text').innerText()).includes('を取り消しますか'), 'reverting asks first');
    await page.click('#confirm-ok');
    await history.locator('.policy-commit-what').first().filter({ hasText: /^Revert / }).waitFor().catch(async () => console.log('error: ' + await panel.locator('.panel-error').innerText().catch(() => '')));
    await prov.locator('.policy-allow', { hasText: 'POST /repos/mtsmfm/iwato/releases' }).waitFor({ state: 'detached' });
    check(true, 'the revert is a commit of its own, and the rule is as before');
    await current.getByRole('button', { name: '履歴を閉じる' }).click();
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });

    // A change made outside Iwato: a banner with import and overwrite; the
    // policy screen's changes wait for it.
    await term.click();
    await card.locator('.iw-input').focus();
    await page.keyboard.type('drift');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(300);
    await page.keyboard.type('propose later.example');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(300);
    await card.locator('.card-tab', { hasText: 'ポリシー' }).click();
    const drift = panel.locator('.policy-drift');
    await drift.waitFor();
    check((await drift.innerText()).includes('Iwato の外でポリシーが変更されました。') && await drift.getByRole('button', { name: '取り込む' }).isVisible(), 'the drift banner shows in the header');
    await page.screenshot({ path: path.join(dir, 'drift.png') });
    await open('allow_pypi_org_443');
    await rule('allow_pypi_org_443').getByRole('button', { name: '宛先 mirror.example.org:443 を外す' }).click();
    await page.click('#confirm-ok');
    await panel.locator('.panel-error', { hasText: '取り込むか Iwato の版で上書きしてから' }).waitFor();
    check(true, 'a change waits for the drift to be settled');
    await drift.getByRole('button', { name: 'Iwato の版で上書き' }).click();
    await dialog.waitFor();
    await page.click('#confirm-cancel');
    await drift.getByRole('button', { name: '取り込む' }).click();
    await drift.waitFor({ state: 'hidden' });
    await rule('outside_change').waitFor();
    check(true, 'imported: the banner goes, the change outside is in the policy');
    await page.waitForFunction(() => !document.querySelector('.terminal-card.selected .apply-badge'), null, { timeout: 30000 });
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
    check(await presets.locator('.policy-preset', { hasText: '(mtsmfm/iwato)' }).waitFor({ state: 'detached', timeout: 10000 }).then(() => true, () => false), 'taking the preset out removes its rule');
    // The owner's presets as YAML: a rule a provider's initial rule makes
    // ambiguous is refused inline,
    // a good one is saved and listed, then deleted.
    await presets.getByRole('button', { name: '新しいプリセット' }).click();
    const yaml = presets.locator('.preset-yaml-editor');
    await yamlFill(yaml, 'id: check-tool\nlabel: 確認用\nnetwork_policies:\n  r:\n    endpoints:\n      - { host: github.com, port: 443, path: /**, protocol: rest, enforcement: enforce, access: read-only }\n    binaries: [/usr/bin/curl]\n');
    await presets.getByRole('button', { name: '保存' }).click();
    await presets.locator('.preset-editor .preset-error', { hasText: 'github.com:443' }).waitFor();
    check(true, 'the editor shows why a preset a provider\'s rule disagrees with is refused');
    check(await panel.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the YAML editor does not make the panel scroll sideways');
    await presets.locator('.preset-editor').screenshot({ path: path.join(dir, 'preset-editor.png') });
    // CodeMirror, highlighted, and what is typed is what is saved.
    check(await yaml.locator('.cm-editor .cm-lineNumbers').count() === 1 && await yaml.locator('.cm-editor').evaluate((el) => {
      const key = [...el.querySelectorAll('.cm-line span')].find((s) => s.textContent === 'id');
      return !!key && getComputedStyle(key).color !== getComputedStyle(key.closest('.cm-line')).color;
    }), 'the preset YAML is CodeMirror with line numbers and highlighting');
    await yamlFill(yaml, (await yamlValue(yaml)).replace('github.com', 'tools.example.com').replace('path: /**, ', ''));
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
    // rule the provider's makes ambiguous is refused, a rule is added after the summary, edits
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
    const withRule = (host, where = '') => loaded.replace('network_policies:\n', 'network_policies:\n  yaml_tool:\n    endpoints:\n      - { host: ' + host + ', port: 443, ' + where + 'protocol: rest, enforcement: enforce, access: read-only }\n    binaries: [/usr/bin/curl]\n');
    await yamlFill(yamlArea, withRule('api.github.com', 'path: /**, '));
    await yamlBox.getByRole('button', { name: '変更を確認' }).click();
    await yamlBox.locator('.preset-error', { hasText: 'provider_github_personal' }).waitFor();
    await page.screenshot({ path: path.join(dir, 'policy-yaml-provider.png') });
    check(true, 'a rule as specific as the provider\'s endpoint without its credential is refused, saying why');
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
    // experiment has a GitHub provider made before ADR 0022 (a profile of
    // its own): proposals there are plain approvals.
    if (phone) await page.click('#menu-button').catch(() => {});
    await page.locator('#sandbox-nav').getByText('experiment', { exact: true }).first().click();
    const exp = page.locator('.terminal-card.selected');
    await exp.locator('.card-title, h2, h3', { hasText: 'experiment' }).first().waitFor().catch(() => {});
    const expStart = exp.getByRole('button', { name: '起動' });
    if (await expStart.isVisible() && await expStart.isEnabled()) { await expStart.click(); await page.waitForTimeout(1500); }
    await exp.locator('.card-tab', { hasText: 'ポリシー' }).click();
    const expPending = exp.locator('.policy-pending');
    const expItem = (host) => expPending.locator('.review-item').filter({ hasText: hostRE(host) });
    const expReview = expPending.locator('.review-detail');
    await expItem(/api\.github\.com/).filter({ hasText: 'エージェントの提案' }).waitFor();
    await expItem(/api\.github\.com/).filter({ hasText: 'エージェントの提案' }).click();
    await expReview.locator('.review-host', { hasText: /^api\.github\.com/ }).waitFor();
    await page.waitForTimeout(800);
    check(await expReview.locator('.review-grant:visible').count() === 0, 'no credential to attach for a provider made before');
    // Over 128 rules: why it cannot be approved as proposed and what to do.
    await expItem(/api\.huge\.example/).click();
    await expReview.locator('.review-host', { hasText: /^api\.huge\.example/ }).waitFor();
    const hugeError = expReview.locator('.review-error');
    await hugeError.waitFor();
    const hugeText = await hugeError.innerText();
    check(hugeText.includes('提案どおりには承認できません。許可する条件 (メソッドとパス) が 128 個を超えています') && hugeText.includes('「ポリシー」タブの「YAML」') && await expReview.getByRole('button', { name: '承認', exact: true }).isDisabled(), 'over 128 rules: what was exceeded and what to do: ' + hugeText);
    await expReview.locator('.review-rule input').first().uncheck();
    check(/16 個までです \(いま 127 個\)/.test(await hugeError.innerText()), 'narrowed, the 16 limit says how many');
    await page.screenshot({ path: path.join(dir, 'review-huge.png') });
    // 100 rules over four endpoints, all kept: approved as proposed.
    await expItem(/api1\.many\.example/).click();
    await expReview.locator('.review-host', { hasText: /^api1\.many\.example/ }).waitFor();
    check(await expReview.locator('.review-rule').count() === 100 && await fits(), 'all 100 rules are rows');
    const manyApprove = expReview.getByRole('button', { name: '承認', exact: true });
    await manyApprove.scrollIntoViewIfNeeded();
    check(await manyApprove.isEnabled() && await inView(manyApprove), '承認 is reachable at the end');
    await page.screenshot({ path: path.join(dir, 'review-many.png') });
    await manyApprove.click();
    await exp.locator('.policy-current').getByText('allow_many_example').first().waitFor();
    check(true, 'the 100-rule proposal is approved');

    // A proposal raised while the policy tab is hidden: the badge, the title
    // and the bar follow the sandbox list, without reading the whole policy.
    await exp.locator('.card-tab', { hasText: 'ターミナル' }).click();
    const expConnect = exp.getByRole('button', { name: /^接続/ });
    if (await expConnect.isVisible() && await expConnect.isEnabled()) await expConnect.click();
    await exp.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
    const policyBadge = exp.locator('.card-tab', { hasText: 'ポリシー' }).locator('.tab-badge');
    const waitingBefore = Number(await policyBadge.textContent()) || 0;
    const titleBefore = Number((await page.title()).match(/^\((\d+)\)/)?.[1] || 0);
    const fullReads = [];
    page.on('request', (r) => { if (r.method() === 'GET' && r.url().endsWith('/sandboxes/experiment/proposals')) fullReads.push(r.url()); });
    await exp.locator('.iw-input').focus();
    await page.keyboard.type('propose new.example');
    await page.keyboard.press('Enter');
    await policyBadge.filter({ hasText: new RegExp('^' + (waitingBefore + 1) + '$') }).waitFor({ timeout: 15000 });
    check(await policyBadge.isVisible() && (await page.title()).startsWith('(' + (titleBefore + 1) + ') ') && (await exp.locator('.proposal-bar-text').innerText()).startsWith('提案 ' + (waitingBefore + 1) + ' 件'), 'a proposal raised with the policy tab hidden shows in the badge, the title and the bar');
    check(fullReads.length === 0, 'with the policy tab hidden the page does not read the whole policy: ' + fullReads.length);
    await exp.locator('.card-tab', { hasText: 'ポリシー' }).click();
    await expItem(/new\.example/).waitFor();
    check(fullReads.length > 0, 'showing the tab reads it, with the new proposal');

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
