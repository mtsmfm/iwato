'use strict';
// The providers in a real browser against `serve -fake`: the panel (list
// without values, add, change, delete; Atlassian's OAuth connection and
// 再接続 through the fake's authorization page, pasting the redirect on a
// phone), the create form's picker, a refused attach that names the
// uninspected rule (or one beyond the provider's scope) and its fix, a
// sandbox's attach/detach with the restart
// choice, and 再起動待ち on the card and its terminals, after a reload too.
// Also creating a provider without a name (Iwato names it), and the
// profiles as YAML (Iwato's files and a gateway profile). Saves
// screenshots in a temporary directory (printed at the end). Usage
// (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/providers-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-providers-'));
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
    const menu = async (id) => {
      if (phone) await page.click('#menu-button');
      await page.click(id);
    };
    const shot = (name) => page.screenshot({ path: path.join(dir, name) });

    // The global panel.
    await menu('#provider-button');
    const panel = page.locator('#provider-panel');
    const group = (label) => panel.locator('.volume-group', { has: page.locator('.volume-sandbox', { hasText: label }) });
    await group('GitHub').waitFor();
    check((await group('GitHub').innerText()).includes('github-personal') && (await group('GitHub').innerText()).includes('GH_TOKEN, GITHUB_TOKEN'), 'GitHub providers are listed with their variables');
    check(await panel.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the panel does not scroll sideways');
    await page.fill('#provider-name', 'github-new');
    const value = panel.locator('#provider-values input');
    check(await value.getAttribute('type') === 'password', 'the value is a password input');
    await value.fill('ghp_SECRET_VALUE');
    await shot('providers-panel.png');
    await page.click('#provider-submit');
    await group('GitHub').locator('.provider-row', { hasText: 'github-new' }).waitFor();
    check(await value.inputValue() === '' && !(await panel.innerText()).includes('SECRET'), 'the value is cleared and never shown');
    const row = group('GitHub').locator('.provider-row', { hasText: 'github-new' });
    await row.getByRole('button', { name: '値を変更' }).click();
    await panel.locator('.provider-edit:visible input').fill('ghp_OTHER');
    await panel.locator('.provider-edit:visible').getByRole('button', { name: '保存' }).click();
    await page.locator('#provider-status', { hasText: 'github-new の値を変更しました' }).waitFor();
    check(true, 'a value is changed');
    check(await group('GitHub').locator('.provider-row', { hasText: 'github-personal' }).getByRole('button', { name: '削除' }).isDisabled(), 'a provider in use cannot be deleted');
    await row.getByRole('button', { name: '削除' }).click();
    await page.click('#confirm-ok');
    await row.waitFor({ state: 'detached' });
    check(true, 'an unused provider is deleted after the warning');

    // No name: the placeholder says what Iwato makes; the result names it.
    const byName = (g, name) => group(g).locator('.provider-row', { has: page.locator('.volume-path', { hasText: new RegExp('^' + name + '$') }) });
    check(await page.getAttribute('#provider-name', 'placeholder') === '名前 (省略すると github)', 'the placeholder shows the generated name');
    check(await page.locator('#provider-name-hint').innerText() === '名前はあとから変えられません。', 'the hint says names cannot be changed');
    await page.fill('#provider-name', '');
    await value.fill('ghp_UNNAMED');
    await shot('providers-unnamed.png');
    await page.click('#provider-submit');
    await page.locator('#provider-form-status', { hasText: 'github を追加しました。' }).waitFor();
    await byName('GitHub', 'github').waitFor();
    check(await page.getAttribute('#provider-name', 'placeholder') === '名前 (省略すると github-2)', 'the placeholder moves on to github-2');
    await byName('GitHub', 'github').getByRole('button', { name: '削除' }).click();
    await page.click('#confirm-ok');
    await byName('GitHub', 'github').waitFor({ state: 'detached' });
    check(true, 'a provider created without a name is named github');

    // A profile as YAML: Iwato's file as shipped (marked so), and a
    // profile imported with OpenShell's CLI read from the gateway and
    // rendered; read-only monospace, copied as it is.
    const yamlBox = panel.locator('#provider-yaml');
    await byName('GitHub', 'github-personal').getByRole('button', { name: 'プロファイルを YAML で見る' }).click();
    const view = yamlBox.locator('.provider-yaml-view');
    const shippedYAML = await yamlValue(view);
    check(shippedYAML.startsWith("# Iwato's GitHub provider profile.") && shippedYAML.includes('\nid: iwato-github\n') && shippedYAML.includes('      - allow: { method: POST, path: "/**/git-upload-pack" }'), 'the shipped YAML is shown verbatim, comments included');
    check((await yamlBox.locator('.tag-source').innerText()) === 'Iwato 同梱', 'it is marked as the shipped file');
    check(/mono|Consolas/i.test(await view.locator('.cm-scroller').evaluate((el) => getComputedStyle(el).fontFamily)) && await view.locator('.cm-content').getAttribute('aria-readonly') === 'true', 'the YAML is read-only monospace');
    check(await panel.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'long YAML lines scroll inside the block, not the panel');
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await yamlBox.getByRole('button', { name: 'コピー' }).click();
    await yamlBox.getByRole('button', { name: 'コピーしました' }).waitFor();
    check(await page.evaluate(() => navigator.clipboard.readText()) === (await page.evaluate(() => fetch('/api/provider-profiles/iwato-github').then((r) => r.json()))).yaml, 'コピー copies the file exactly');
    await yamlBox.scrollIntoViewIfNeeded();
    await shot('profile-yaml-shipped.png');
    await group('その他').locator('.provider-row', { hasText: 'openai' }).getByRole('button', { name: 'プロファイルを YAML で見る' }).click();
    await yamlBox.locator('.tag-source[data-source=gateway]').waitFor();
    const gatewayYAML = await yamlValue(yamlBox.locator('.provider-yaml-view'));
    check(gatewayYAML.startsWith('id: openai\n') && gatewayYAML.includes('inference_capable: true') && gatewayYAML.includes('    access: read-write') && !gatewayYAML.includes('source:'), 'a CLI-imported profile is rendered from the gateway in OpenShell\'s format');
    check((await yamlBox.locator('.tag-source').innerText()) === 'ゲートウェイから', 'it is marked as rendered from the gateway');
    await yamlBox.scrollIntoViewIfNeeded();
    await shot('profile-yaml-gateway.png');
    // A provider of the setup-token type Iwato no longer ships.
    const retired = byName('その他', 'claude-token');
    check((await retired.innerText()).includes('iwato-claude-for-claude-token · CLAUDE_CODE_OAUTH_TOKEN'), 'a setup-token provider is listed under その他 with its profile');
    await retired.getByRole('button', { name: '削除' }).click();
    await page.click('#confirm-ok');
    await retired.waitFor({ state: 'detached' });
    check(true, 'it can be deleted');
    // The add form's profile list.
    await page.selectOption('#provider-profile', 'iwato-openrouter');
    await page.click('#provider-profile-yaml');
    await page.waitForFunction(() => { const el = document.querySelector('#provider-yaml .provider-yaml-view .cm-editor'); return !!el && window.IwatoCodeMirror.EditorView.findFromDOM(el).state.doc.toString().includes('\nid: iwato-openrouter\n'); });
    check((await yamlBox.locator('.tag-source').innerText()) === 'Iwato 同梱', 'the add form shows the chosen profile as YAML');
    await yamlBox.locator('.cm-content').click();
    await page.keyboard.press('Escape');
    check(!(await yamlBox.isVisible()) && await panel.isVisible(), 'Escape in the YAML closes only the YAML');
    await page.selectOption('#provider-profile', 'iwato-github');

    // The type selector groups the profiles (agents, source control,
    // model hubs, clouds); a model hub's hint and its fake provider.
    const kinds = await page.locator('#provider-profile optgroup').evaluateAll((gs) => gs.map((g) => g.label + ':' + [...g.querySelectorAll('option')].map((o) => o.value).join('+')));
    check(kinds.join(' / ') === 'エージェント・推論:iwato-claude-subscription+iwato-grok-subscription+iwato-openrouter+iwato-atlassian / ソース管理:iwato-github / モデル配布:iwato-huggingface+iwato-civitai / 生成・GPU クラウド:iwato-comfy-cloud+iwato-vastai+iwato-runpod+iwato-modal / ホスティング・デプロイ:iwato-cloudflare', 'the type selector groups the profiles (' + kinds.join(' / ') + ')');
    check(!(await page.isVisible('#provider-hint')), 'GitHub has no usage hint');
    await page.selectOption('#provider-profile', 'iwato-huggingface');
    check((await page.locator('#provider-hint').innerText()).includes('HF_TOKEN') && (await page.locator('#provider-note').innerText()).includes('HUGGING_FACE_HUB_TOKEN'), 'Hugging Face shows its variables and a usage hint');
    check(await page.getAttribute('#provider-name', 'placeholder') === '名前 (省略すると huggingface-2)', 'the fake already has a huggingface provider');
    await shot('providers-huggingface.png');
    await page.click('#provider-profile-yaml');
    await page.waitForFunction(() => { const el = document.querySelector('#provider-yaml .provider-yaml-view .cm-editor'); return !!el && window.IwatoCodeMirror.EditorView.findFromDOM(el).state.doc.toString().includes('\nid: iwato-huggingface\n'); });
    check((await yamlValue(yamlBox.locator('.provider-yaml-view'))).includes('HUGGING_FACE_HUB_TOKEN'), 'the Hugging Face profile is shown as YAML');
    await yamlBox.getByRole('button', { name: '閉じる' }).click();
    check((await group('Hugging Face').innerText()).includes('HF_TOKEN, HUGGING_FACE_HUB_TOKEN'), 'the fake Hugging Face provider is listed with its variables');
    await page.selectOption('#provider-profile', 'iwato-github');

    // A GitHub provider's scope (providers_scope.go): the simple editor
    // with an inline refusal, the YAML editor (custom, reset), and the
    // shared iwato-github provider from before the scopes.
    check((await byName('GitHub', 'github-personal').innerText()).includes('範囲を決められるようになる前'), 'the shared iwato-github provider says it stays read-only');
    const work = byName('GitHub', 'github-work');
    check((await work.innerText()).includes('範囲: 書き込み: mtsmfm/iwato'), 'the scope is summed up on the row');
    await work.getByRole('button', { name: '範囲を変更' }).click();
    const access = panel.locator('.provider-access-form:visible');
    await access.waitFor();
    check(await access.locator('input[value=repos]').isChecked() && !(await access.locator('.provider-access-repos').isDisabled()), 'the editor opens on the current scope');
    await access.locator('.provider-access-repos').fill('mtsmfm/iwato\nocto-org/*');
    await access.locator('.provider-access-mutations').fill('createPullRequest');
    await access.getByRole('button', { name: '保存' }).click();
    await access.locator('.provider-access-status', { hasText: 'OWNER/REPO' }).waitFor();
    check(true, 'a glob in a repository is refused in the form');
    await access.locator('.provider-access-repos').fill('mtsmfm/iwato\nocto-org/octo-repo');
    await access.scrollIntoViewIfNeeded();
    await shot('provider-scope.png');
    check(await panel.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the scope editor does not scroll the panel sideways');
    await access.locator('input[value=full]').check();
    check(await access.locator('.provider-access-repos').isDisabled() && await access.locator('.provider-access-mutations').isDisabled(), 'full write takes no repositories or mutations');
    await access.locator('input[value=repos]').check();
    await access.getByRole('button', { name: '保存' }).click();
    await page.locator('#provider-status', { hasText: 'github-work の範囲を変更しました' }).waitFor();
    check((await work.innerText()).includes('octo-org/octo-repo') && (await work.innerText()).includes('createPullRequest'), 'the saved scope is shown');
    await work.getByRole('button', { name: '範囲を変更' }).click();
    await panel.locator('.provider-access-form:visible').getByRole('button', { name: 'YAML で編集' }).click();
    const editor = yamlBox.locator('.provider-yaml-editor');
    const generated = await yamlValue(editor);
    check(generated.includes('id: iwato-github-for-github-work') && generated.includes('/octo-org/octo-repo.git/git-receive-pack') && generated.includes('fields: [createPullRequest]'), 'the YAML editor shows the rendered profile');
    check(/mono|Consolas/i.test(await editor.locator('.cm-scroller').evaluate((el) => getComputedStyle(el).fontFamily)) && await editor.locator('.cm-content').getAttribute('aria-readonly') === null, 'the YAML editor is monospace and editable');
    await yamlBox.scrollIntoViewIfNeeded();
    await shot('provider-scope-yaml.png');
    await yamlFill(editor, generated.replace('id: iwato-github-for-github-work', 'id: other'));
    await yamlBox.getByRole('button', { name: '保存' }).click();
    await yamlBox.locator('.provider-yaml-status', { hasText: 'id は' }).waitFor();
    check(await yamlBox.isVisible(), 'a refused YAML stays in the editor with the reason');
    await yamlFill(editor, '# mine\n' + generated);
    await yamlBox.getByRole('button', { name: '保存' }).click();
    await yamlBox.waitFor({ state: 'hidden' });
    await work.locator('.provider-access-summary', { hasText: 'YAML で編集したプロファイル' }).waitFor();
    await work.getByRole('button', { name: '範囲を変更' }).click();
    const customForm = panel.locator('.provider-access-form:visible');
    check(await customForm.locator('input[value=repos]').isDisabled(), 'a custom profile disables the simple editor');
    await customForm.getByRole('button', { name: '簡単な設定に戻す' }).click();
    await work.locator('.provider-access-summary', { hasText: '書き込み: mtsmfm/iwato, octo-org/octo-repo' }).waitFor();
    check(true, '簡単な設定に戻す goes back to the simple scope');

    // Another type: 読み取りだけ or 書き込みも許可, with the sandboxes it
    // applies to; one from before the scopes says so.
    check((await byName('Claude Code', 'claude').innerText()).includes('範囲を決められるようになる前'), 'a Claude Code provider from before the scopes says so');
    const router = byName('OpenRouter', 'openrouter');
    check((await router.innerText()).includes('範囲: 読み取りだけ'), 'an OpenRouter provider of its own profile is read-only at first');
    await router.getByRole('button', { name: '範囲を変更' }).click();
    const routerForm = panel.locator('.provider-access-form:visible');
    await routerForm.waitFor();
    const modeLabels = await routerForm.locator('.provider-access-mode').allInnerTexts();
    check(modeLabels.map((l) => l.trim()).join('/') === '読み取りだけ/書き込みも許可' && await routerForm.locator('.provider-access-repos').count() === 0, 'its editor has 読み取りだけ and 書き込みも許可 only (' + modeLabels.join('/') + ')');
    check((await routerForm.locator('.provider-access-sandboxes').innerText()) === 'どのサンドボックスにも付いていません', 'the editor says where the scope applies');
    await routerForm.locator('input[value=full]').check();
    await routerForm.scrollIntoViewIfNeeded();
    await shot('provider-scope-write.png');
    await routerForm.getByRole('button', { name: '保存' }).click();
    await router.locator('.provider-access-summary', { hasText: '範囲: 書き込みも許可' }).waitFor();
    check(true, '書き込みも許可 is saved and summed up');

    // A custom profile: added as YAML in the page (refusals inline), then
    // offered in the type selector under カスタム.
    await page.click('#provider-custom-add');
    const template = await yamlValue(editor);
    await yamlFill(editor, template.replace('id: my-api', 'id: iwato-mine'));
    await yamlBox.getByRole('button', { name: '保存' }).click();
    await yamlBox.locator('.provider-yaml-status', { hasText: 'iwato- で始まる' }).waitFor();
    check(true, 'a custom profile cannot take an iwato- id');
    await yamlFill(editor, template);
    await yamlBox.getByRole('button', { name: '保存' }).click();
    await yamlBox.waitFor({ state: 'hidden' });
    check(await page.inputValue('#provider-profile') === 'my-api' && (await page.locator('#provider-profile optgroup[label=カスタム] option').allInnerTexts()).includes('My API'), 'the custom profile is offered under カスタム');
    check((await page.innerText('#provider-profile-yaml')) === 'プロファイルを YAML で編集', 'a custom profile is edited, not just shown');
    await page.selectOption('#provider-profile', 'iwato-github');

    // Atlassian: the gateway's refresh state, connecting by OAuth (the
    // fake's authorization page), 再接続. On a phone the redirect to
    // 127.0.0.1 cannot load, so the owner pastes its URL.
    const atl = group('Atlassian');
    await atl.waitFor();
    check((await atl.locator('.provider-guide-command').innerText()).includes("--header 'Authorization: Bearer ${ATLASSIAN_ACCESS_TOKEN}'"), 'the MCP command is shown with single quotes');
    const oss = atl.locator('.provider-row', { hasText: 'atlassian-oss' });
    check((await oss.innerText()).includes('再認可が必要です'), 'a revoked grant asks for 再接続');
    check(await oss.getByRole('button', { name: '値を変更' }).count() === 0, 'a refreshed credential has no 値を変更');
    check((await atl.locator('.provider-row', { hasText: 'atlassian-work' }).innerText()).includes('ゲートウェイが更新しています'), 'a healthy grant shows its refresh times');
    await atl.scrollIntoViewIfNeeded();
    await shot('atlassian-list.png');
    const authorize = async (expect) => {
      const box = panel.locator('#provider-oauth');
      await box.locator('.provider-scope').first().waitFor();
      check(await box.locator('input[value=offline_access]').isDisabled() && await box.locator('input[value=offline_access]').isChecked(), 'offline_access is always asked');
      await shot(expect + '-scopes.png');
      await box.getByRole('button', { name: '許可を求める' }).click();
      const link = box.getByRole('link', { name: 'Atlassian で許可する' });
      await link.waitFor();
      check(await panel.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the flow does not scroll the panel sideways');
      await shot(expect + '-waiting.png');
      const popupP = page.waitForEvent('popup');
      await link.click();
      const popup = await popupP;
      await popup.locator('#approve').waitFor();
      if (phone) {
        // The phone's browser fails to load 127.0.0.1: its address is pasted.
        const back = await popup.locator('#approve').getAttribute('href');
        await popup.close();
        await box.locator('.provider-paste input').fill(back);
        await box.locator('.provider-paste').getByRole('button', { name: '送る' }).click();
      } else {
        await popup.click('#approve');
        await popup.locator('text=Iwato の画面に戻って').waitFor();
        await popup.screenshot({ path: path.join(dir, expect + '-callback.png') });
        await popup.close();
      }
      await box.locator('.provider-oauth-status', { hasText: expect === 'atlassian-reconnect' ? '再接続しました' : '接続しました' }).waitFor({ timeout: 15000 });
      check((await box.innerText()).includes('ゲートウェイが更新しています') && (await box.locator('.provider-guide-command').innerText()).includes('ATLASSIAN_ACCESS_TOKEN'), expect + ': done, refreshed, with the sandbox command');
      check(!/fake-(access|refresh)/.test(await page.content()), expect + ': no token reaches the page');
      await shot(expect + '-done.png');
    };
    await page.selectOption('#provider-profile', 'iwato-atlassian');
    check(await page.locator('#provider-values input').count() === 0 && (await page.locator('#provider-submit').innerText()) === 'Atlassian に接続', 'the Atlassian profile takes no value');
    check(await page.getAttribute('#provider-name', 'placeholder') === '名前 (省略すると atlassian)', 'the Atlassian placeholder shows the generated name');
    // Connected without a name: Iwato names it atlassian.
    await page.fill('#provider-name', '');
    await page.click('#provider-submit');
    await authorize('atlassian-connect');
    check(await panel.locator('#provider-oauth strong').first().innerText() === 'Atlassian に接続: atlassian', 'the flow names the created provider');
    await byName('Atlassian', 'atlassian').waitFor();
    check((await byName('Atlassian', 'atlassian').innerText()).includes('ゲートウェイが更新しています'), 'the new provider is listed with its refresh');
    // Its scope: read tools at first, write tools by name, or every tool.
    {
      const atlNew = byName('Atlassian', 'atlassian');
      check((await atlNew.innerText()).includes('範囲: ツールは読み取りだけ'), 'a new Atlassian provider allows the read tools only');
      await atlNew.getByRole('button', { name: '範囲を変更' }).click();
      const form = panel.locator('.provider-access-form:visible');
      await form.waitFor();
      const labels = (await form.locator('.provider-access-mode').allInnerTexts()).map((l) => l.trim());
      check(labels.join('/') === '読み取りのツールと、指定したツール/すべてのツール' && (await form.locator('.provider-access-read-tools').innerText()).includes('getJiraIssue'), 'its editor has the tool modes and lists the read tools (' + labels.join('/') + ')');
      await form.locator('.provider-access-tools').fill('createJiraIssue, bad*name');
      await form.getByRole('button', { name: '保存' }).click();
      await form.locator('.provider-access-status', { hasText: 'MCP のツールの名前ではありません' }).waitFor();
      check(true, 'a tool name with a glob is refused');
      await form.locator('.provider-access-tools').fill('createJiraIssue');
      await form.scrollIntoViewIfNeeded();
      await shot('provider-scope-tools.png');
      check(await panel.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the tool editor does not scroll the panel sideways');
      await form.getByRole('button', { name: '保存' }).click();
      await atlNew.locator('.provider-access-summary', { hasText: '範囲: ツール: createJiraIssue' }).waitFor();
      check(true, 'a named write tool is saved and summed up');
    }
    await oss.getByRole('button', { name: '再接続' }).click();
    await authorize('atlassian-reconnect');
    await page.waitForFunction(() => !document.querySelector('#provider-list').innerText.includes('再認可が必要'));
    check(true, '再接続 restores the revoked grant');
    await panel.locator('#provider-oauth').getByRole('button', { name: '閉じる' }).click();

    // Claude Code's sign-in: on the PC the fake's page comes back to the
    // loopback listener; on a phone the second link ends on a page that
    // shows CODE#STATE, which is pasted.
    await page.selectOption('#provider-profile', 'iwato-claude-subscription');
    check((await page.locator('#provider-submit').innerText()) === 'Claude にログイン' && (await page.innerText('#provider-note')).includes('api.anthropic.com'), 'the Claude sign-in profile takes no value');
    await page.click('#provider-submit');
    {
      const box = panel.locator('#provider-oauth');
      const link = box.getByRole('link', { name: phone ? 'スマホなどでログイン (コードを貼る)' : 'Claude でログイン' });
      await link.waitFor();
      check(await panel.locator('.login-body').evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the Claude flow does not scroll the panel sideways');
      await shot('claude-waiting.png');
      const popupP = page.waitForEvent('popup');
      await link.click();
      const popup = await popupP;
      await popup.click('#approve');
      if (phone) {
        const code = await popup.locator('#code').innerText();
        await popup.close();
        await box.locator('.provider-paste input').fill(code);
        await box.locator('.provider-paste').getByRole('button', { name: '送る' }).click();
      } else {
        await popup.locator('text=Iwato の画面に戻って').waitFor();
        await popup.close();
      }
      await box.locator('.provider-oauth-status', { hasText: '接続しました' }).waitFor({ timeout: 15000 });
      check((await box.innerText()).includes('ゲートウェイが更新しています') && !/fake-(access|refresh)/.test(await page.content()), 'Claude: done, refreshed, no token on the page');
      await shot('claude-done.png');
      await byName('Claude Code (Claude でログイン)', 'claude-subscription').waitFor();
      check(true, 'the Claude provider is listed');
      await box.getByRole('button', { name: '閉じる' }).click();
    }
    await page.keyboard.press('Escape');
    check(await panel.isHidden(), 'Escape closes the providers');

    // The create form's picker.
    await menu('#create-button');
    await page.locator('#create-providers select[data-profile=iwato-github]').waitFor();
    const options = await page.locator('#create-providers select[data-profile=iwato-github]').locator('option').allInnerTexts();
    check(options.join(',') === 'なし,github-personal,github-work', 'the create form offers one GitHub provider at most (' + options.join(', ') + ')');
    await page.locator('#create-providers select[data-profile=iwato-github]').selectOption('github-work');
    await shot('create-providers.png');
    await page.keyboard.press('Escape');
    check(await page.locator('#create-panel').isHidden(), 'Escape closes the create form');

    // A running sandbox: attach, the restart choice, 再起動待ち.
    const start = card.getByRole('button', { name: '起動' });
    if (await start.isVisible() && await start.isEnabled()) { await start.click(); }
    await card.locator('.badge[data-state=running]').waitFor();
    const openPanel = async () => {
      await card.getByRole('button', { name: 'その他の操作' }).click();
      await card.getByRole('menuitem', { name: 'プロバイダー' }).click();
    };
    await openPanel();
    const side = card.locator('.provider-panel');
    await side.locator('.provider-row').first().waitFor();
    check(await side.locator('.provider-row', { hasText: 'github-work' }).getByRole('button', { name: '付ける' }).isDisabled(), 'a second GitHub provider cannot be attached');
    // A provider of a profile Iwato does not ship (OpenShell's CLI, or
    // one Iwato removed) is under その他 and can be attached and detached.
    const other = side.locator('.volume-group', { has: page.locator('h3', { hasText: 'その他' }) }).locator('.provider-row', { hasText: 'openai' });
    await other.getByRole('button', { name: '付ける' }).click();
    await side.locator('.restart-choice').waitFor({ timeout: 3000 });
    await side.getByRole('button', { name: 'あとで' }).click();
    await other.getByRole('button', { name: '外す' }).click();
    await side.locator('.restart-choice').waitFor({ timeout: 3000 });
    await side.getByRole('button', { name: 'あとで' }).click();
    await other.getByRole('button', { name: '付ける' }).waitFor();
    check(true, 'a provider of another profile is attached and detached from the card');
    // The fake policy allows api.anthropic.com uninspected (L4): the
    // attach is refused with the rule named; the fix removes it.
    await side.locator('.provider-row', { hasText: /^claude(?!-)/ }).getByRole('button', { name: '付ける' }).click();
    const conflict = side.locator('.conflict-choice');
    await conflict.waitFor();
    check((await conflict.innerText()).includes('規則 allow_api_anthropic_com_443: api.anthropic.com:443 (L4-only)'), 'an uninspected rule for the credential is named');
    check(await side.locator('.provider-row', { hasText: /^claude(?!-)/ }).getByRole('button', { name: '付ける' }).isVisible(), 'nothing is attached before the fix');
    await shot('attach-conflict.png');
    const attachedAt = Date.now();
    await conflict.getByRole('button', { name: 'この宛先を規則から外して付ける' }).click();
    await side.locator('.restart-choice').waitFor({ timeout: 3000 });
    check(Date.now() - attachedAt < 3000, 'attaching answers without waiting for the sandbox (' + (Date.now() - attachedAt) + ' ms)');
    check((await side.innerText()).includes('claude を付けました'), 'attaching says so');
    const applyingTag = side.locator('.provider-row', { hasText: /^claude(?!-)/ }).locator('.tag-applying');
    check(await applyingTag.isVisible() && (await applyingTag.innerText()) === '反映待ち', 'the card shows 反映待ち until the sandbox applies it');
    await shot('attach-restart.png');
    await applyingTag.waitFor({ state: 'detached', timeout: 15000 });
    check(true, '反映待ち goes once the sandbox applied it');
    await shot('attach-applied.png');
    await side.getByRole('button', { name: 'あとで' }).click();
    const badge = card.locator('.badge-restart');
    check(await badge.isVisible(), 'あとで leaves the card marked 再起動待ち');
    await page.keyboard.press('Escape');
    check(await side.isHidden(), 'Escape closes the card\'s providers');
    await card.locator('.term-tab .term-provider').first().waitFor();
    check(await card.locator('.term-notice[data-kind=provider]').isVisible(), 'the main terminal says it lacks the change');
    await shot('restart-pending.png');
    // A new terminal has the change.
    await card.getByRole('button', { name: '新しいターミナル' }).click();
    await page.waitForTimeout(800);
    check(await card.locator('.term-tab').nth(1).locator('.term-provider').count() === 0, 'a terminal opened after the change is not marked');

    await page.reload();
    await card.waitFor();
    await card.locator('.badge-restart').waitFor();
    check(true, '再起動待ち survives a reload');
    await shot('restart-pending-reload.png');

    // 今すぐ再起動: a stop the server refuses is said in the panel, with
    // its error, and the choice stays.
    await openPanel();
    await side.locator('.provider-row', { hasText: /^claude(?!-)/ }).getByRole('button', { name: '外す' }).click();
    await side.locator('.restart-choice').waitFor();
    await page.route('**/api/sandboxes/*/stop', (route) => route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'fake stop refused' }) }), { times: 1 });
    await side.getByRole('button', { name: '今すぐ再起動' }).click();
    await side.locator('.panel-meta', { hasText: '再起動できませんでした: fake stop refused' }).waitFor({ timeout: 15000 });
    check(await side.locator('.restart-choice').isVisible() && await badge.isVisible(), 'a failed restart says why and keeps the restart choice');
    await shot('restart-failed.png');
    // Then it goes through stopping and starting to running.
    await card.locator('.badge').first().evaluate((el) => {
      window.badgeStates = [];
      new MutationObserver(() => window.badgeStates.push(el.dataset.state)).observe(el, { attributes: true, attributeFilter: ['data-state'] });
    });
    await side.getByRole('button', { name: '今すぐ再起動' }).click();
    await side.locator('.panel-meta', { hasText: '再起動しました' }).waitFor({ timeout: 15000 });
    await badge.waitFor({ state: 'hidden' });
    const states = (await page.evaluate(() => window.badgeStates)).filter((v, i, a) => v !== a[i - 1]);
    const order = ['stopping', 'starting', 'running'].map((v) => states.indexOf(v));
    check(order.every((v, i) => v >= 0 && (i === 0 || v > order[i - 1])), '今すぐ再起動 goes through stopping, starting, running (' + states.join(' → ') + ')');
    check(!(await badge.isVisible()) && await card.locator('.badge[data-state=running]').isVisible(), '今すぐ再起動 restarts and clears 再起動待ち');
    await shot('restarted.png');
    await side.getByRole('button', { name: '閉じる' }).click();
    await card.getByRole('button', { name: '新しいターミナル' }).click();
    await page.waitForTimeout(800);
    check(await card.locator('.term-tab .term-provider').count() === 0 && !(await card.locator('.term-notice[data-kind=provider]').isVisible()), 'after the restart no terminal lacks the change');

    // An endpoint the provider's rule would make ambiguous: with GitHub
    // detached, a proposal for api.github.com approved in audit mode (a
    // rule by hand there is the provider's, refused); attaching GitHub
    // (whose rule enforces there) is refused with the rule named and the fixes.
    if (!(await side.isVisible())) await openPanel();
    await side.locator('.provider-row', { hasText: 'github-personal' }).getByRole('button', { name: '外す' }).click();
    await side.locator('.restart-choice').waitFor();
    await side.getByRole('button', { name: 'あとで' }).click();
    await side.getByRole('button', { name: '閉じる' }).click();
    await card.locator('.card-tab', { hasText: 'ポリシー' }).click();
    const policy = card.locator('.policy-panel');
    const miseGh = policy.locator('.policy-pending .proposal-item', { hasText: 'api.github.com' }).filter({ hasText: '/usr/local/lib/mise/mise' }).first();
    await miseGh.getByRole('button', { name: '承認', exact: true }).click();
    await page.locator('#approve-more').evaluate((el) => { el.open = true; });
    await page.check('#approve-access-audit');
    await page.click('#confirm-ok');
    await policy.locator('.policy-rule[data-rule=allow_api_github_com_443]').waitFor();
    await openPanel();
    await side.locator('.provider-row', { hasText: 'github-personal' }).getByRole('button', { name: '付ける' }).click();
    await conflict.waitFor();
    const ambText = await conflict.innerText();
    check(ambText.includes('規則 allow_api_github_com_443: api.github.com:443') && ambText.includes('検査のモード: allow_api_github_com_443 は 監査 (audit)、_provider_github_personal は 遮断 (enforce)'), 'an ambiguous endpoint is named with what differs: ' + ambText);
    // In English (attached again: the list is worded when shown), then in Japanese again.
    await page.evaluate(() => window.IwatoI18n.setPreference('en'));
    await side.locator('.provider-row', { hasText: 'github-personal' }).getByRole('button', { name: 'Attach' }).click();
    await conflict.getByRole('button', { name: 'Switch to enforce with the current allowances, and attach' }).waitFor();
    const ambEn = await conflict.innerText();
    check(ambEn.includes('Rule allow_api_github_com_443: api.github.com:443 — Mode: audit in allow_api_github_com_443, enforce in _provider_github_personal'), 'and in English: ' + ambEn);
    await page.evaluate(() => window.IwatoI18n.setPreference('ja'));
    await side.locator('.provider-row', { hasText: 'github-personal' }).getByRole('button', { name: '付ける' }).click();
    await conflict.getByRole('button', { name: '今の許可のまま遮断に切り替えて付ける' }).waitFor();
    check(await conflict.getByRole('button', { name: '今の許可のまま遮断に切り替えて付ける' }).isVisible() && await conflict.getByRole('button', { name: '記録から選んで遮断に切り替える (ポリシーのタブ)' }).isVisible() && await conflict.getByRole('button', { name: 'この宛先を規則から外して付ける' }).isVisible(), 'it offers to enforce (as is, or from the log) or to remove');
    check(await side.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the conflict does not scroll the panel sideways');
    await conflict.scrollIntoViewIfNeeded();
    await shot('attach-ambiguity.png');
    // From the log: the policy tab, the rule open, what to do next.
    await conflict.getByRole('button', { name: '記録から選んで遮断に切り替える (ポリシーのタブ)' }).click();
    const done = policy.locator('.proposal-done');
    await done.waitFor();
    check((await done.innerText()).includes('プロバイダー github-personal をもう一度付けてください') && await policy.locator('.policy-rule[data-rule=allow_api_github_com_443] .policy-observed').isVisible(), 'the policy tab opens the audited rule with its log and says what to do');
    await shot('attach-ambiguity-policy.png');
    // As is: switched to enforce, then attached.
    await openPanel();
    await side.locator('.provider-row', { hasText: 'github-personal' }).getByRole('button', { name: '付ける' }).click();
    await conflict.getByRole('button', { name: '今の許可のまま遮断に切り替えて付ける' }).click();
    await side.locator('.panel-meta', { hasText: 'github-personal を付けました' }).waitFor();
    check(await side.locator('.provider-row', { hasText: 'github-personal' }).getByRole('button', { name: '外す' }).isVisible(), 'switched to enforce, the provider is attached');
    await side.getByRole('button', { name: '閉じる' }).click();
    await card.locator('.card-tab', { hasText: 'ポリシー' }).click();
    await policy.locator('.policy-rule[data-rule=allow_api_github_com_443]', { hasText: 'api.github.com:443 · 検査 (遮断)' }).waitFor();
    check(true, 'the rule now enforces, keeping its reads');
    await shot('attach-ambiguity-done.png');

    // A rule approved before for RunPod's GraphQL (POST /graphql, REST)
    // would carry the key beyond the provider's read-only scope: the
    // attach names the rule and why, and offers to remove it or attach anyway.
    const sandboxName = (await card.locator('.card-title').innerText()).trim();
    const added = await page.evaluate(async (name) => {
      const { csrf } = await (await fetch('/api/session')).json();
      // A provider's host: confirmed (the token of the first answer).
      const add = (confirm) => fetch('/api/sandboxes/' + name + '/policy/add', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
        body: JSON.stringify({ rule: 'allow_api_runpod_io_443', binaries: ['/usr/bin/python3'], host: 'api.runpod.io', ports: [443], rules: [{ method: 'POST', path: '/graphql' }], confirm }) });
      const r = await add((await (await add('')).json()).confirm);
      return r.status + ' ' + (await r.text());
    }, sandboxName);
    check(added.startsWith('200'), 'a REST rule for POST api.runpod.io/graphql is added (' + added + ')');
    await openPanel();
    const runpod = side.locator('.provider-row', { hasText: /^runpod/ });
    await runpod.getByRole('button', { name: '付ける' }).click();
    await conflict.waitFor({ timeout: 5000 }).catch(() => {});
    const beyondText = await conflict.isVisible() ? await conflict.innerText() : '';
    check(beyondText.includes('規則 allow_api_runpod_io_443: api.runpod.io:443') && beyondText.includes('runpod'), 'a rule reaching beyond RunPod\'s scope is named with why: ' + beyondText);
    check(await conflict.getByRole('button', { name: 'この宛先を規則から外して付ける' }).isVisible() && await conflict.getByRole('button', { name: 'このまま付ける' }).isVisible(), 'it offers to remove the rule or to attach anyway');
    check(await side.evaluate((el) => el.scrollWidth <= el.clientWidth + 1), 'the list does not scroll the panel sideways');
    await conflict.scrollIntoViewIfNeeded();
    await shot('attach-beyond.png');
    await conflict.getByRole('button', { name: 'この宛先を規則から外して付ける' }).click();
    await side.locator('.panel-meta', { hasText: 'runpod を付けました' }).waitFor();
    check(await runpod.getByRole('button', { name: '外す' }).isVisible(), 'with the rule removed, RunPod is attached');
    await side.getByRole('button', { name: '閉じる' }).click();
    await card.locator('.card-tab', { hasText: 'ポリシー' }).click();
    await policy.locator('.policy-rule[data-rule=allow_api_runpod_io_443]').waitFor({ state: 'detached' });
    check(true, 'the rule is gone from the policy');
    console.log('screenshots in ' + dir);
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed'); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
