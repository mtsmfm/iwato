'use strict';
// The editor tab (エディタ) in a real browser against `serve -fake`: a folder
// (remembered), quick open with a fuzzy match (Ctrl+P, arrows, Enter, Esc;
// the 「ファイルを開く」 button on phones), editing with the unsaved mark,
// saving (Ctrl+S or 「保存」), a save refused because the file changed
// meanwhile (overwrite, reload), the folder's tree (beside the text, a
// drawer on phones), a binary file refused, images previewed (PNG, SVG,
// WebP; HTML named .png refused), audio and video played (Range), Ctrl+P left to the terminal, and a
// stopped sandbox. The text is CodeMirror: Go and JavaScript highlighted,
// Ctrl+F's search, no CSP violations. A Markdown preview. Saves
// screenshots. Usage
// (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/editor-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-editor-'));
const phone = process.argv.includes('--phone');
const failures = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) failures.push(what); };

// mark finds the element under scope (CSS) whose text is text (a button,
// a tab, an option) and gives it a selector to click.
let marks = 0;
async function mark(page, scope, text, tags = 'button, [role=tab], [role=option], li') {
  const id = 'm' + (++marks);
  const found = await page.evaluate(([scope, text, tags, id]) => {
    const root = document.querySelector(scope);
    const el = root && [...root.querySelectorAll(tags)].find((e) => e.offsetParent !== null && (e.getAttribute('aria-label') === text || e.textContent.trim() === text));
    if (el) el.dataset.check = id;
    return !!el;
  }, [scope, text, tags, id]);
  if (!found) throw new Error('not found: ' + text + ' in ' + scope);
  return '[data-check="' + id + '"]';
}
const click = async (page, scope, text, tags) => page.click(await mark(page, scope, text, tags));

const panelSel = '.terminal-card.selected .editor-panel';
const textSel = panelSel + ' .editor-text';
const quickSel = panelSel + ' .editor-quick';
const mod = process.platform === 'darwin' ? 'Meta' : 'Control';

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
    // The text is CodeMirror (web/vendor/codemirror.js): read and place
    // the cursor through its view. CSP violations are failures.
    await context.addInitScript(() => {
      const view = (sel) => { const el = document.querySelector(sel); return el && window.IwatoCodeMirror?.EditorView.findFromDOM(el); };
      window.__editorText = (sel) => view(sel)?.state.doc.toString() ?? '';
      window.__editorFocus = (sel) => view(sel)?.focus();
      window.__editorEnd = (sel) => { const v = view(sel); v.focus(); v.dispatch({ selection: { anchor: v.state.doc.length } }); };
      window.__csp = [];
      document.addEventListener('securitypolicyviolation', (e) => window.__csp.push(e.violatedDirective + ' ' + e.blockedURI));
    });
    const page = await context.newPage();
    page.on('pageerror', (e) => failures.push('page error: ' + e.message));
    page.on('console', (m) => { if (m.type() === 'error') console.log('console: ' + m.text()); });
    await page.goto(url);
    await page.waitForSelector('.terminal-card.selected');
    const shot = (name) => page.screenshot({ path: path.join(dir, name) });
    const status = () => page.evaluate((sel) => document.querySelector(sel).textContent, panelSel + ' .editor-status');
    // Another editor (another tab or device): the API with the session.
    const external = (p, content) => page.evaluate(async ([p, content]) => {
      const { csrf } = await (await fetch('/api/session')).json();
      const cur = await (await fetch('/api/sandboxes/dev/editor/file?path=' + encodeURIComponent(p))).json();
      const r = await fetch('/api/sandboxes/dev/editor/file', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body: JSON.stringify({ path: p, content, base: cur.version }) });
      return r.status;
    }, [p, content]);
    const read = (p) => page.evaluate(async (p) => (await (await fetch('/api/sandboxes/dev/editor/file?path=' + encodeURIComponent(p))).json()).content, p);

    if (await page.evaluate(() => [...document.querySelectorAll('.terminal-card.selected button')].some((b) => b.textContent.trim() === '起動' && b.offsetParent && !b.disabled))) {
      await click(page, '.terminal-card.selected', '起動');
    }
    await page.waitForSelector('.terminal-card.selected .badge[data-state=running]');

    await click(page, '.terminal-card.selected .card-tabs', 'エディタ');
    await page.waitForSelector(panelSel + ' .editor-empty', { state: 'visible' });
    await shot('editor-empty.png');

    // The folder: typed, then remembered for the sandbox.
    await page.fill(panelSel + ' .editor-dir', './work/iwato/');
    await page.keyboard.press('Enter');
    check(await page.evaluate(() => localStorage.getItem('iwato-editor-dir:dev')) === 'work/iwato', 'the folder is normalized and remembered (work/iwato)');

    // Quick open: Ctrl+P on a PC, the button on a phone.
    if (phone) await click(page, panelSel, 'ファイルを開く');
    else await page.keyboard.press(mod + '+P');
    await page.waitForSelector(quickSel, { state: 'visible' });
    await page.waitForSelector(quickSel + ' .editor-quick-item');
    const all = await page.evaluate((sel) => [...document.querySelectorAll(sel)].map((e) => e.textContent), quickSel + ' .editor-quick-item');
    check(all.includes('main.go') && all.includes('web/app.js') && !all.some((f) => f.includes('node_modules')), 'the list has the folder\'s files, without node_modules (' + all.join(', ') + ')');
    check(await page.evaluate((sel) => document.activeElement === document.querySelector(sel), quickSel + ' input'), 'the quick open input has the keyboard');
    await page.keyboard.type('wapjs');
    await page.waitForFunction((sel) => document.querySelector(sel)?.textContent === 'web/app.js', quickSel + ' .editor-quick-item');
    check(true, 'a fuzzy query (wapjs) puts web/app.js first');
    await page.fill(quickSel + ' input', 'mngo');
    await page.waitForFunction((sel) => document.querySelector(sel)?.textContent === 'main.go', quickSel + ' .editor-quick-item');
    await shot('editor-quick.png');
    if (phone) {
      await click(page, quickSel, 'main.go', 'li');
    } else {
      await page.keyboard.press('ArrowDown');
      const second = await page.evaluate((sel) => document.querySelector(sel)?.getAttribute('aria-selected'), quickSel + ' .editor-quick-item:nth-child(2)');
      check(second === 'true' || await page.evaluate((sel) => document.querySelectorAll(sel).length === 1, quickSel + ' .editor-quick-item'), 'ArrowDown moves the choice');
      await page.keyboard.press('ArrowUp');
      await page.keyboard.press('Escape');
      await page.waitForSelector(quickSel, { state: 'hidden' });
      check(true, 'Escape closes the quick open');
      await page.keyboard.press(mod + '+P');
      await page.waitForSelector(quickSel, { state: 'visible' });
      await page.keyboard.type('mngo');
      await page.waitForFunction((sel) => document.querySelector(sel)?.textContent === 'main.go', quickSel + ' .editor-quick-item');
      await page.keyboard.press('Enter');
    }
    await page.waitForSelector(textSel, { state: 'visible' });
    await page.waitForFunction((sel) => __editorText(sel).includes('こんにちは'), textSel);
    check(true, 'main.go opens in the editor');
    // Highlighting: Go's keyword package in the keyword colour.
    const colored = (sel, word) => page.evaluate(([sel, word]) => {
      const content = document.querySelector(sel + ' .cm-content');
      const base = getComputedStyle(content).color;
      const span = [...content.querySelectorAll('span')].find((s) => s.textContent === word);
      const want = getComputedStyle(document.documentElement).getPropertyValue('--code-keyword').trim();
      return { color: span ? getComputedStyle(span).color : null, base, want };
    }, [sel, word]);
    const goColor = await colored(textSel, 'package');
    check(goColor.color && goColor.color !== goColor.base, 'Go is highlighted (' + JSON.stringify(goColor) + ')');
    if (!phone) {
      // Search: Ctrl+F opens CodeMirror's panel (in the page's language),
      // Escape closes it and leaves a panel of the page (the log) open.
      await page.click('#settings-button');
      await page.click('#log-button');
      await page.evaluate((sel) => __editorFocus(sel), textSel);
      await page.keyboard.press(mod + '+F');
      await page.waitForSelector(panelSel + ' .cm-search', { state: 'visible' });
      check(await page.evaluate((sel) => document.querySelector(sel + ' .cm-search input[name=search]').placeholder === '検索', panelSel), 'Ctrl+F opens the search, in Japanese');
      await page.keyboard.type('Println');
      await page.keyboard.press('Enter');
      check(await page.evaluate((sel) => !!document.querySelector(sel + ' .cm-searchMatch'), panelSel), 'the search marks the match');
      await page.keyboard.press('Escape');
      await page.waitForSelector(panelSel + ' .cm-search', { state: 'detached' });
      check(await page.isVisible('#log-panel'), 'Escape closes the search, not the log');
      await page.keyboard.press('Escape');
      check(await page.isVisible('#log-panel'), 'Escape in the editor leaves the log open');
      await page.click('#log-close');
    }
    check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'the page does not scroll sideways');

    // The tree: beside the text on a PC, a drawer from the button on a phone.
    const treeSel = panelSel + ' .editor-tree';
    const item = (p) => treeSel + ' .editor-tree-item[data-path="' + p + '"]';
    if (phone) {
      check(await page.evaluate((sel) => document.querySelector(sel).hidden, treeSel), 'the tree is hidden at first on a phone');
      await click(page, panelSel, 'ツリー');
    }
    await page.waitForSelector(item('work/iwato/main.go'), { state: 'visible' });
    check(await page.evaluate((sel) => document.querySelector(sel).getAttribute('aria-current') === 'true', item('work/iwato/main.go')), 'the tree marks the file opened by quick open');
    const names = await page.evaluate((sel) => [...document.querySelectorAll(sel + ' .editor-tree-list > li > .editor-tree-item')].filter((b) => !b.dataset.path.slice('work/iwato/'.length).includes('/')).map((b) => b.dataset.path.slice('work/iwato/'.length)), treeSel);
    check(names.indexOf('.git') < names.indexOf('docs') && names.indexOf('web') < names.indexOf('main.go') && names.includes('LINK.md'), 'folders come first, dotfiles shown (' + names.join(', ') + ')');
    check(await page.evaluate((sel) => document.querySelector(sel).dataset.heavy === 'true', item('work/iwato/node_modules')), 'node_modules is marked heavy');
    check(await page.evaluate((sel) => document.querySelector(sel).disabled, item('work/iwato/broken')), 'a broken link cannot be opened');
    const box = await page.evaluate(([t, x]) => { const a = document.querySelector(t).getBoundingClientRect(); const b = document.querySelector(x).getBoundingClientRect(); return { treeRight: a.right, textLeft: b.left, treeLeft: a.left }; }, [treeSel, textSel]);
    check(phone ? box.treeLeft <= box.textLeft + 1 : box.treeRight <= box.textLeft + 1, (phone ? 'the drawer lies over the text' : 'the tree is on the left of the text') + ' (' + JSON.stringify(box) + ')');
    await shot('editor-tree.png');
    await page.click(item('work/iwato/docs'));
    await page.waitForSelector(item('work/iwato/docs/editor.md'), { state: 'visible' });
    check(await page.evaluate(() => JSON.parse(localStorage.getItem('iwato-editor-tree:dev') || '[]').includes('work/iwato/docs')), 'the expanded folder is remembered');
    await page.click(item('work/iwato/docs/editor.md'));
    await page.waitForFunction((sel) => __editorText(sel) === '# エディタ\n', textSel);
    check(true, 'a click in the tree opens the file');
    if (phone) {
      check(await page.evaluate((sel) => document.querySelector(sel).hidden, treeSel), 'picking a file closes the drawer');
      await click(page, panelSel, 'ツリー');
    }
    check(await page.evaluate((sel) => document.querySelector(sel).getAttribute('aria-current') === 'true', item('work/iwato/docs/editor.md')), 'the tree marks the file opened from it');
    await click(page, treeSel, '更新');
    await page.waitForFunction((sel) => /ツリーを読み直しました/.test(document.querySelector(sel).textContent), panelSel + ' .editor-status');
    check(true, 'the tree reads its folders again');
    check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'the tree does not make the page scroll sideways');
    await click(page, phone ? treeSel : panelSel + ' .editor-folder', phone ? '閉じる' : 'ツリー');
    await page.waitForSelector(treeSel, { state: 'hidden' });
    check(true, 'the tree can be hidden');
    if (!phone) await click(page, panelSel + ' .editor-folder', 'ツリー');
    await click(page, panelSel + ' .editor-tabs', 'main.go');
    await page.waitForFunction((sel) => __editorText(sel).includes('こんにちは'), textSel);

    // Edit: the tab shows the unsaved mark.
    if (phone) await page.click(textSel);
    await page.evaluate((sel) => __editorEnd(sel), textSel);
    await page.keyboard.type('// 編集しました');
    await page.keyboard.press('Enter');
    await page.waitForSelector(panelSel + ' .editor-tab[data-dirty=true]');
    check(true, 'an edit marks the file unsaved');
    await page.keyboard.type(' ' + 'x'.repeat(400));
    check(await page.evaluate((sel) => { const s = document.querySelector(sel + ' .cm-scroller'); return s.scrollWidth <= s.clientWidth + 1; }, textSel), 'a long line wraps instead of scrolling sideways');
    for (let i = 0; i < 401; i++) await page.keyboard.press('Backspace');
    await shot('editor-edited.png');

    // Save: Ctrl+S on a PC, the button on a phone.
    if (phone) await click(page, panelSel, '保存');
    else await page.keyboard.press(mod + '+S');
    await page.waitForFunction((sel) => /保存しました/.test(document.querySelector(sel).textContent), panelSel + ' .editor-status');
    check(await page.evaluate((sel) => !document.querySelector(sel), panelSel + ' .editor-tab[data-dirty=true]'), 'saving clears the unsaved mark');
    check((await read('work/iwato/main.go')).endsWith('}\n// 編集しました\n'), 'the sandbox has the saved text');
    await shot('editor-saved.png');

    // Changed meanwhile: the save is refused; overwrite.
    check(await external('work/iwato/main.go', 'package main // other\n') === 200, 'another editor saves the file');
    await page.evaluate((sel) => __editorFocus(sel), textSel);
    await page.keyboard.type('x');
    if (phone) await click(page, panelSel, '保存');
    else await page.keyboard.press(mod + '+S');
    await page.waitForSelector(panelSel + ' .editor-conflict', { state: 'visible' });
    check(await read('work/iwato/main.go') === 'package main // other\n', 'a save over another change is refused');
    await shot('editor-conflict.png');
    await click(page, panelSel + ' .editor-conflict', '上書き保存');
    await page.waitForSelector(panelSel + ' .editor-conflict', { state: 'hidden' });
    check((await read('work/iwato/main.go')).endsWith('// 編集しました\nx'), '上書き保存 writes this page\'s text');

    // Changed again: reload takes the other text.
    await external('work/iwato/main.go', 'package main // third\n');
    await page.evaluate((sel) => __editorFocus(sel), textSel);
    await page.keyboard.type('y');
    if (phone) await click(page, panelSel, '保存');
    else await page.keyboard.press(mod + '+S');
    await page.waitForSelector(panelSel + ' .editor-conflict', { state: 'visible' });
    await click(page, panelSel + ' .editor-conflict', '読み込み直す');
    await page.waitForFunction((sel) => __editorText(sel) === 'package main // third\n', textSel);
    check(await page.evaluate((sel) => !document.querySelector(sel), panelSel + ' .editor-tab[data-dirty=true]'), '読み込み直す takes the file as it is, not unsaved');

    // A binary file, from the home (no rg in the fake: the SFTP walk).
    await page.fill(panelSel + ' .editor-dir', '');
    await page.keyboard.press('Enter');
    if (phone) await click(page, panelSel, 'ファイルを開く');
    else await page.keyboard.press(mod + '+P');
    await page.waitForSelector(quickSel, { state: 'visible' });
    await page.fill(quickSel + ' input', 'bintool');
    await page.waitForFunction((sel) => document.querySelector(sel)?.textContent === 'bin/tool', quickSel + ' .editor-quick-item');
    check(/rg がない/.test(await page.evaluate((sel) => document.querySelector(sel).textContent, quickSel + ' .editor-quick-meta')), 'without rg the list says it walked the files');
    if (phone) await click(page, quickSel, 'bin/tool', 'li');
    else await page.keyboard.press('Enter');
    await page.waitForFunction((sel) => /バイナリ/.test(document.querySelector(sel).textContent), panelSel + ' .editor-status');
    check(true, 'a binary file is refused with a message');
    check(await page.evaluate((sel) => ![...document.querySelectorAll(sel)].some((tab) => /^tool/.test(tab.textContent)), panelSel + ' .editor-tab'), 'the refused file opens no tab');

    // A CRLF file opens unchanged, and saves with its CRLF line endings.
    if (phone) await click(page, panelSel, 'ファイルを開く');
    else await page.keyboard.press(mod + '+P');
    await page.waitForSelector(quickSel, { state: 'visible' });
    await page.fill(quickSel + ' input', 'windowstxt');
    await page.waitForFunction((sel) => document.querySelector(sel)?.textContent === 'work/iwato/windows.txt', quickSel + ' .editor-quick-item');
    if (phone) await click(page, quickSel, 'work/iwato/windows.txt', 'li');
    else await page.keyboard.press('Enter');
    await page.waitForFunction((sel) => __editorText(sel).startsWith('CRLF'), textSel);
    check(await page.evaluate((sel) => !document.querySelector(sel), panelSel + ' .editor-tab[data-dirty=true]'), 'a CRLF file opens without the unsaved mark');
    await page.evaluate((sel) => __editorEnd(sel), textSel);
    await page.keyboard.type('3');
    if (phone) await click(page, panelSel, '保存');
    else await page.keyboard.press(mod + '+S');
    await page.waitForFunction((sel) => /保存しました/.test(document.querySelector(sel).textContent), panelSel + ' .editor-status');
    check(await read('work/iwato/windows.txt') === 'CRLF の行\r\n2 行目\r\n3', 'a CRLF file saves with CRLF');
    await click(page, panelSel + ' .editor-tabs', 'work/iwato/windows.txt を閉じる');
    await click(page, panelSel + ' .editor-tabs', 'main.go');
    await page.waitForFunction((sel) => __editorText(sel) === 'package main // third\n', textSel);

    // Ctrl+P belongs to the terminal while it shows.
    if (!phone) {
      await click(page, '.terminal-card.selected .card-tabs', 'ターミナル');
      await page.waitForSelector(panelSel, { state: 'hidden' });
      await page.keyboard.press(mod + '+P');
      await page.waitForTimeout(300);
      check(await page.evaluate((sel) => document.querySelector(sel).hidden, quickSel), 'Ctrl+P in the terminal leaves the editor alone');
      await click(page, '.terminal-card.selected .card-tabs', 'エディタ');
      await page.waitForSelector(textSel, { state: 'visible' });
      check(await page.evaluate((sel) => __editorText(sel) === 'package main // third\n', textSel), 'the open file stays while another tab shows');
    }

    // Images: a read-only preview (fit, 100 %), marked in the tree; HTML
    // named .png refused; an SVG's script never runs; the raw route's headers.
    const imageSel = panelSel + ' .editor-image';
    const openQuick = async (query, want) => {
      if (phone) await click(page, panelSel, 'ファイルを開く');
      else await page.keyboard.press(mod + '+P');
      await page.waitForSelector(quickSel, { state: 'visible' });
      await page.fill(quickSel + ' input', query);
      await page.waitForFunction(([sel, want]) => document.querySelector(sel)?.textContent === want, [quickSel + ' .editor-quick-item', want]);
      if (phone) await click(page, quickSel, want, 'li');
      else await page.keyboard.press('Enter');
    };
    await openQuick('iconpng', 'work/iwato/web/icon.png');
    await page.waitForSelector(imageSel + ' img', { state: 'visible' });
    const icon = await page.evaluate(([p, t]) => ({ w: document.querySelector(p + ' img').naturalWidth, meta: document.querySelector(p + ' .editor-image-meta').textContent, text: document.querySelector(t).offsetParent === null }), [imageSel, textSel]);
    check(icon.w === 48 && /48 × 32 px/.test(icon.meta) && icon.text, 'a PNG opens as an image (' + JSON.stringify(icon) + ')');
    check(await page.evaluate((sel) => [...document.querySelectorAll(sel + ' button')].find((b) => b.textContent.trim() === '保存').disabled, panelSel), 'saving is off for an image');
    const fits = await page.evaluate((sel) => { const r = document.querySelector(sel + ' .editor-image-box').getBoundingClientRect(); const i = document.querySelector(sel + ' img').getBoundingClientRect(); return i.width <= r.width + 1 && i.height <= r.height + 1; }, imageSel);
    check(fits, 'the image fits in the view');
    await page.click(imageSel + ' img');
    check(await page.evaluate((sel) => document.querySelector(sel).dataset.fit === 'actual', imageSel), 'a click shows it at 100 %');
    await click(page, imageSel, '全体を表示');
    if (phone) await click(page, panelSel, 'ツリー');
    await page.waitForSelector(item('work/iwato/web/icon.png'), { state: 'visible' });
    check(await page.evaluate((sel) => document.querySelector(sel).getAttribute('aria-current') === 'true', item('work/iwato/web/icon.png')), 'the tree marks the image');
    await shot('editor-image.png');
    await page.click(item('work/iwato/web/logo.svg'));
    await page.waitForFunction((sel) => document.querySelector(sel + ' img')?.alt === 'logo.svg' && document.querySelector(sel + ' img').complete, imageSel);
    check(await page.evaluate(() => document.title !== 'ran'), 'the SVG\'s script does not run');
    if (phone) await click(page, panelSel, 'ツリー');
    await page.click(item('work/iwato/web/dot.webp'));
    await page.waitForFunction((sel) => document.querySelector(sel + ' img')?.alt === 'dot.webp' && document.querySelector(sel + ' img').naturalWidth === 1, imageSel);
    check(true, 'a WebP opens');
    const tabsBefore = await page.evaluate((sel) => document.querySelectorAll(sel).length, panelSel + ' .editor-tab');
    await openQuick('fakepng', 'work/iwato/web/fake.png');
    await page.waitForFunction((sel) => /fake\.png: .*バイナリ/.test(document.querySelector(sel).textContent), panelSel + ' .editor-status');
    check(await page.evaluate((sel) => document.querySelectorAll(sel).length, panelSel + ' .editor-tab') === tabsBefore, 'HTML named .png is refused, no tab');
    check(await page.evaluate((sel) => { const row = document.querySelector(sel + ' .editor-tabs').getBoundingClientRect(); const tab = document.querySelector(sel + ' .editor-tab[aria-selected="true"]')?.getBoundingClientRect(); return !!tab && tab.left >= row.left - 1 && tab.right <= row.right + 1; }, panelSel), 'the current tab is in view');
    for (const name of ['icon.png', 'logo.svg', 'dot.webp']) await click(page, panelSel + ' .editor-tabs', 'work/iwato/web/' + name + ' を閉じる');
    // Audio and video: a player of the media route (Range), play only, a
    // download link; one the browser cannot play says so.
    await openQuick('tonewav', 'work/iwato/media/tone.wav');
    await page.waitForFunction((sel) => document.querySelector(sel + ' audio')?.readyState >= 1, imageSel);
    const tone = await page.evaluate((sel) => { const d = document.querySelector(sel + ' a.button'); const a = document.querySelector(sel + ' audio'); return { controls: a.controls, duration: a.duration, download: d.offsetParent !== null && d.download === 'tone.wav', save: [...document.querySelectorAll('.terminal-card.selected .editor-panel button')].find((b) => b.textContent.trim() === '保存').disabled }; }, imageSel);
    check(tone.controls && Math.abs(tone.duration - 0.5) < 0.05 && tone.download && tone.save, 'a WAV opens in an audio player, play only, with a download link (' + JSON.stringify(tone) + ')');
    await openQuick('clipmp4', 'work/iwato/media/clip.mp4');
    await page.waitForFunction((sel) => document.querySelector(sel + ' video')?.readyState >= 1, imageSel);
    const clip = await page.evaluate((sel) => { const box = document.querySelector(sel + ' .editor-image-box').getBoundingClientRect(); const v = document.querySelector(sel + ' video'); const r = v.getBoundingClientRect(); return { w: v.videoWidth, meta: document.querySelector(sel + ' .editor-image-meta').textContent, fits: r.width <= box.width + 1 && r.height <= box.height + 1, note: document.querySelector(sel + ' .editor-media-note').offsetParent !== null }; }, imageSel);
    check(clip.w === 64 && /64 × 36 px/.test(clip.meta) && clip.fits && !clip.note, 'an MP4 opens in a video player that fits the view (' + JSON.stringify(clip) + ')');
    await shot('editor-video.png');
    await openQuick('brokenmp4', 'work/iwato/media/broken.mp4');
    await page.waitForFunction((sel) => document.querySelector(sel + ' video')?.getAttribute('aria-label') === 'broken.mp4' && document.querySelector(sel + ' .editor-media-note').offsetParent !== null, imageSel);
    check(true, 'a file the browser cannot play opens with a note');
    for (const name of ['tone.wav', 'clip.mp4', 'broken.mp4']) await click(page, panelSel + ' .editor-tabs', 'work/iwato/media/' + name + ' を閉じる');
    // JavaScript is highlighted too (a string).
    await openQuick('wapjs', 'work/iwato/web/app.js');
    await page.waitForFunction((sel) => __editorText(sel).startsWith("'use strict'"), textSel);
    const js = await page.evaluate((sel) => {
      const content = document.querySelector(sel + ' .cm-content');
      const span = [...content.querySelectorAll('span')].find((s) => s.textContent === "'use strict'");
      return { color: span ? getComputedStyle(span).color : null, base: getComputedStyle(content).color };
    }, textSel);
    check(js.color && js.color !== js.base, 'JavaScript is highlighted (' + JSON.stringify(js) + ')');
    await click(page, panelSel + ' .editor-tabs', 'work/iwato/web/app.js を閉じる');
    await click(page, panelSel + ' .editor-tabs', 'main.go');
    await page.waitForFunction((sel) => __editorText(sel) === 'package main // third\n', textSel);

    // Markdown: a preview in a sandboxed iframe (instead of the text, or
    // beside it on a PC), GFM rendered, the file's raw HTML inert, a
    // relative image through the raw route, http(s) links in a new tab.
    await openQuick('docspreview', 'work/iwato/docs/preview.md');
    await page.waitForFunction((sel) => __editorText(sel).startsWith('# プレビュー'), textSel);
    const viewSel = panelSel + ' .editor-view';
    const previewSel = panelSel + ' iframe.editor-preview';
    check(await page.evaluate((sel) => document.querySelector(sel).offsetParent !== null, viewSel), 'a Markdown file has the view switch');
    await click(page, viewSel, 'プレビュー');
    await page.waitForSelector(previewSel, { state: 'visible' });
    const frame = await (await page.$(previewSel)).contentFrame();
    await frame.waitForSelector('table');
    const md = await frame.evaluate(() => ({
      cells: [...document.querySelectorAll('td')].map((td) => td.textContent).join(','),
      tasks: document.querySelectorAll('input[type=checkbox]').length,
      del: !!document.querySelector('del'),
      code: document.querySelector('pre code')?.textContent,
      links: [...document.querySelectorAll('a')].map((a) => a.href + ' ' + a.target + ' ' + a.rel),
      scripts: document.querySelectorAll('script').length,
      wide: document.documentElement.scrollWidth <= innerWidth + 1
    }));
    check(md.cells === 'a,1,b,2' && md.tasks === 2 && md.del && md.code === 'func main() {}\n', 'the preview renders GFM: a table, task lists, strikethrough, code (' + JSON.stringify(md) + ')');
    check(md.links.length === 2 && md.links.every((l) => /^https:\/\/example\.com\/\S* _blank noopener noreferrer$/.test(l)), 'only http(s) links are links, opening a new tab (' + md.links.join(' | ') + ')');
    const sandbox = await page.evaluate((sel) => document.querySelector(sel).getAttribute('sandbox'), previewSel);
    check(!/allow-scripts|allow-same-origin/.test(sandbox), 'the iframe has neither allow-scripts nor allow-same-origin (' + sandbox + ')');
    await frame.waitForSelector('img[alt=アイコン]');
    check(await frame.evaluate(() => document.querySelector('img[alt=アイコン]').naturalWidth === 48), 'a relative image shows through the raw route as data:');
    const imgs = await frame.evaluate(() => [...document.querySelectorAll('img')].map((i) => i.getAttribute('src').slice(0, 15) + (i.getAttribute('onerror') ? ' onerror' : '')));
    const alts = await frame.evaluate(() => [...document.querySelectorAll('.alt')].map((a) => a.textContent).join(','));
    check(md.scripts === 0 && imgs.length === 1 && imgs.every((i) => i.startsWith('data:image/') && !i.includes('onerror')), 'the raw HTML is text, no img with onerror (' + imgs.join(', ') + ')');
    check(alts === '外部の画像', 'an external image shows only its alt text (' + alts + ')');
    await page.waitForTimeout(300);
    check(await page.evaluate(() => window.__pwned === undefined), 'no script of the file ran');
    check(await page.evaluate(([t]) => document.querySelector(t).offsetParent === null, [textSel]), 'the preview takes the text\'s place');
    check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1) && md.wide, 'the preview does not scroll sideways');
    await context.route('https://example.com/**', (r) => r.fulfill({ contentType: 'text/plain', body: 'example' }));
    const [popup] = await Promise.all([context.waitForEvent('page'), frame.click('a[href="https://example.com/docs"]')]);
    await popup.waitForLoadState();
    check(popup.url() === 'https://example.com/docs' && await popup.evaluate(() => window.opener === null), 'a link opens in a new tab without an opener (' + popup.url() + ')');
    await popup.close();
    await shot('editor-markdown.png');
    if (phone) {
      check(await page.evaluate((sel) => [...document.querySelectorAll(sel + ' button')].every((b) => b.textContent !== '両方' || b.offsetParent === null), viewSel), 'a phone has no side-by-side view');
    } else {
      await click(page, viewSel, '両方');
      await page.waitForSelector(textSel, { state: 'visible' });
      const both = await page.evaluate(([t, f]) => { const a = document.querySelector(t).getBoundingClientRect(); const b = document.querySelector(f).getBoundingClientRect(); return { textRight: a.right, previewLeft: b.left, width: b.width }; }, [textSel, previewSel]);
      check(both.textRight <= both.previewLeft + 1 && both.width > 200, 'both: the preview beside the text (' + JSON.stringify(both) + ')');
      await page.evaluate((sel) => __editorEnd(sel), textSel);
      await page.keyboard.type('\n## 追加した見出し\n');
      await frame.waitForFunction(() => [...document.querySelectorAll('h2')].some((h) => h.textContent === '追加した見出し'));
      check(true, 'the preview follows the typing');
      check(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'both views do not scroll the page sideways');
      await shot('editor-markdown-both.png');
      for (let i = 0; i < '\n## 追加した見出し\n'.length; i++) await page.keyboard.press('Backspace');
    }
    await click(page, viewSel, 'テキスト');
    await page.waitForSelector(previewSel, { state: 'hidden' });
    check(await page.evaluate(() => localStorage.getItem('iwato-editor-view')) === 'text', 'the view is remembered');
    await click(page, panelSel + ' .editor-tabs', 'work/iwato/docs/preview.md を閉じる');
    await click(page, panelSel + ' .editor-tabs', 'main.go');
    await page.waitForFunction((sel) => __editorText(sel) === 'package main // third\n', textSel);
    check(await page.evaluate((sel) => document.querySelector(sel).offsetParent === null, viewSel), 'other files have no view switch');

    // A path printed in the terminal, relative to where cd went, opens here
    // at its line and column: a new file, and one open already.
    {
      const { clickPath, editorAt } = require('./path-links.cjs');
      const card = page.locator('.terminal-card.selected');
      await click(page, '.terminal-card.selected .card-tabs', 'ターミナル');
      await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
      await card.locator('.iw-input').focus();
      await page.keyboard.type('cd iwato');
      await page.keyboard.press('Enter');
      await clickPath(page, card, 'no ./web/nope.css:1', './web/nope.css:1', phone);
      await page.waitForTimeout(600);
      check(await card.getAttribute('data-tab') === 'terminal' && !/開け/.test(await card.locator('.card-message-text').allInnerTexts().then((a) => a.join(' '))), 'a missing file\'s path does nothing');
      await clickPath(page, card, 'ok ./web/styles.css:1:6', './web/styles.css:1:6', phone);
      let at = await editorAt(page);
      check(at.text.startsWith('body {') && at.line === 1 && at.col === 6, 'a path from the terminal opens at its line and column, from where cd went (' + JSON.stringify({ line: at.line, col: at.col }) + ')');
      await click(page, '.terminal-card.selected .card-tabs', 'ターミナル');
      await clickPath(page, card, 'main.go: ./main.go:1:9', './main.go:1:9', phone);
      await page.waitForFunction(() => document.querySelector('.terminal-card.selected')?.dataset.tab === 'editor');
      at = await editorAt(page);
      check(at.text === 'package main // third\n' && at.line === 1 && at.col === 9, 'a file open already keeps its edits and gets the cursor (' + JSON.stringify({ line: at.line, col: at.col }) + ')');
      // The cursor back at the start, where the next step types.
      await click(page, '.terminal-card.selected .card-tabs', 'ターミナル');
      await clickPath(page, card, './main.go:1:1', './main.go:1:1', phone);
      await page.waitForFunction(() => document.querySelector('.terminal-card.selected')?.dataset.tab === 'editor');
    }

    // Stopped: the tab and the text stay; saving says why it cannot.
    await page.evaluate((sel) => __editorFocus(sel), textSel);
    await page.keyboard.type('z');
    await click(page, '.terminal-card.selected', 'その他の操作');
    await click(page, '.terminal-card.selected', '停止', '[role=menuitem]');
    await page.waitForSelector('#confirm-panel', { state: 'visible', timeout: 2000 }).then(() => page.click('#confirm-ok'), () => {});
    await page.waitForSelector('.terminal-card.selected .badge[data-state=stopped]');
    await page.waitForFunction((sel) => /停止中/.test(document.querySelector(sel).textContent), panelSel + ' .editor-status');
    check(await page.evaluate((sel) => !document.querySelector(sel).hidden, panelSel), 'the editor tab stays for a stopped sandbox');
    const kept = await page.evaluate((sel) => __editorText(sel), textSel);
    check(kept.startsWith('z') || kept.endsWith('z'), 'the unsaved text stays (' + JSON.stringify(kept.slice(-30)) + ')');
    await click(page, panelSel, '保存');
    await page.waitForFunction((sel) => /起動すると保存/.test(document.querySelector(sel).textContent), panelSel + ' .editor-status');
    check(true, 'saving a stopped sandbox says to start it');
    await shot('editor-stopped.png');
    const csp = await page.evaluate(() => window.__csp);
    check(!csp.length, 'no CSP violations (' + csp.join(', ') + ')');
    console.log('screenshots in ' + dir);
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed:\n' + failures.join('\n')); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
