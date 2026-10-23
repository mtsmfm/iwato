'use strict';
// The terminal (web/term.js) in a real browser against `serve -fake`: typing
// reaches the session, frames draw the screen, older history comes in pages
// as it scrolls into view, and the cell grid holds wide characters. Saves
// screenshots in a temporary directory (printed at the end). Usage
// (AGENTS.md):
//   cd ~/.cache/iwato-browser && NODE_PATH=$PWD/node_modules mise exec node@22 -- node <repo>/scripts/term-check.cjs [--phone]
const { spawn, execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const repo = path.join(__dirname, '..');
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'iwato-term-'));
const phone = process.argv.includes('--phone');
const failures = [];
const check = (ok, what) => { console.log((ok ? 'PASS ' : 'FAIL ') + what); if (!ok) failures.push(what); };

(async () => {
  // The cell measurements need the real font; it is not in git (a new
  // worktree has none), and without it the page falls back silently.
  if (!fs.existsSync(path.join(repo, 'web/vendor/fonts/moralerspace-neon-hw-2.0.0-regular.ttf'))) {
    console.log('FAIL web/vendor/fonts is missing: run python3 scripts/fetch-vendor.py (or copy web/vendor/fonts from another checkout)');
    process.exit(1);
  }
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
    const traffic = { sent: 0, received: 0, frames: 0, pages: 0 };
    let typed = ''; // the input messages' text
    page.on('websocket', (ws) => {
      ws.on('framesent', (f) => {
        traffic.sent += typeof f.payload === 'string' ? f.payload.length : f.payload.length;
        if (typeof f.payload !== 'string' && f.payload[0] === 0x64) typed += f.payload.subarray(1).toString();
      });
      ws.on('framereceived', (f) => {
        const p = f.payload;
        traffic.received += p.length;
        const kind = typeof p === 'string' ? p.charCodeAt(0) : p[0];
        if (kind === 0x46) traffic.frames++;
        if (kind === 0x50) traffic.pages++;
      });
    });
    await page.goto(url);
    const card = page.locator('.terminal-card.selected');
    await card.waitFor();
    const start = card.getByRole('button', { name: '起動' });
    if (await start.isVisible() && await start.isEnabled()) { await start.click(); await page.waitForTimeout(1500); }
    const connect = card.getByRole('button', { name: /^接続/ });
    if (await connect.isVisible() && await connect.isEnabled()) await connect.click();
    await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
    await card.locator('.iw-term').waitFor();
    await page.waitForTimeout(500);
    const screenText = () => card.locator('.iw-screen').innerText();
    check((await screenText()).includes('IWATO · FAKE'), 'the screen shows the session');
    const input = card.locator('.iw-input');
    await input.focus();
    await page.keyboard.type('echo こんにちは iwato');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    check((await screenText()).includes('こんにちは iwato'), 'typed text reached the session and came back');
    await page.keyboard.type('url');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    check((await card.locator('.iw-screen .iw-url').count()) > 0 && (await card.locator('.iw-screen .iw-url').first().innerText()).startsWith('https://example.com/'), 'a URL in the output looks like a link');
    check(await card.locator('.iw-screen .iw-url').evaluateAll((els) => els.every((e) => e.dataset.url === 'https://example.com/iwato/fake/a-very-long-path-that-wraps-across-terminal-lines?with=query&and=more-parameters')), 'every piece of a wrapped URL is underlined and opens the whole URL');
    await page.keyboard.type('framed');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    const framed = await card.locator('.iw-screen .iw-url').evaluateAll((els) => {
      const group = els.at(-1)?.dataset.group;
      const pieces = els.filter((e) => e.dataset.group === group);
      return pieces.length >= 2 && pieces.every((e) => e.dataset.url.startsWith('https://example.com/iwato/fake/a-very-long-path-that-wraps-across-terminal-lines?with=query&and=more-parameters&pad=xx') && e.dataset.url === pieces[0].dataset.url) && !pieces.some((e) => e.textContent.includes('│'));
    });
    check(framed, 'a URL an application wrapped inside a frame is one link');
    await card.locator('.iw-screen .iw-url').last().hover();
    check(await card.locator('.iw-screen .iw-url-hot').count() >= 2, 'hovering one piece lights up the whole URL');
    await input.focus();
    await page.keyboard.type('x');
    await page.waitForTimeout(300);
    check(await card.locator('.iw-screen .iw-url-hot').count() >= 2, 'the whole URL stays lit while the screen redraws');
    await page.keyboard.press('Backspace');
    await page.keyboard.type('osc8');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    await card.locator('.iw-screen [data-link]').first().hover();
    const osc8 = await card.locator('.iw-screen [data-link]').evaluateAll((els) => els.length >= 2 && els.every((e) => e.classList.contains('iw-url-hot')));
    check(osc8, 'an application\'s hyperlink over several rows lights up as one');
    const underlines = await card.locator('.iw-screen [data-link]').evaluateAll((els) => els.every((e) => { const cs = getComputedStyle(e); return cs.textDecorationLine.includes('underline') && cs.textDecorationColor === cs.color && [...e.querySelectorAll('.iw-url')].every((u) => getComputedStyle(u).textDecorationLine === 'none'); }));
    check(underlines, 'and every row\'s underline is lit in one color, with no second underline over it');
    // Every cell of a URL opens it, as Claude Code prints one: after
    // Japanese, in a frame, in two SGR runs (an OSC 8 hyperlink, then plain
    // text), with the cursor parked on it and a status row redrawn during
    // the press. The cells either side open nothing.
    await page.evaluate(() => { window.__opened = []; window.open = (u) => { window.__opened.push(u); return null; }; });
    await page.keyboard.type('cclink');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    const grid = await card.locator('.iw-term').evaluate((t) => {
      const r = t.querySelector('.iw-screen').getBoundingClientRect();
      return { left: r.left, top: r.top, cw: parseFloat(t.style.getPropertyValue('--iw-cw')), ch: parseFloat(t.style.getPropertyValue('--iw-ch')), rows: [...t.querySelectorAll('.iw-screen .iw-row')].map((e) => e.textContent) };
    });
    const ccURL = 'https://example.com/iwato/claude';
    for (const [what, mark] of [['an OSC 8 link', '詳細は '], ['a URL in the text', '参照: ']]) {
      const y = grid.rows.findIndex((r) => r.includes(mark));
      const text = grid.rows[y] || '';
      const col = [...text.slice(0, text.indexOf('https://'))].reduce((n, c) => n + (/[\u3000-\u9fff]/.test(c) ? 2 : 1), 0);
      const missed = [];
      for (let k = -1; k <= ccURL.length; k++) {
        for (const fy of [0.15, 0.5, 0.85]) {
          await page.evaluate(() => { window.__opened = []; });
          const x = grid.left + (col + k + 0.5) * grid.cw;
          const at = grid.top + (y + fy) * grid.ch;
          if (phone) await page.touchscreen.tap(x, at);
          else {
            await page.keyboard.down('Control');
            await page.mouse.move(x, at);
            await page.mouse.down();
            await page.waitForTimeout(60);
            await page.mouse.up();
            await page.keyboard.up('Control');
          }
          await page.waitForTimeout(phone ? 350 : 10);
          const opened = await page.evaluate(() => window.__opened);
          const inside = k >= 0 && k < ccURL.length;
          if (inside ? !(opened.length === 1 && opened[0] === ccURL) : opened.length) missed.push(k + '@' + fy);
        }
      }
      check(y >= 0 && missed.length === 0, 'every cell of ' + what + ' opens it, and only those' + (missed.length ? ' (wrong: ' + missed.join(' ') + ')' : ''));
    }
    await input.focus();
    await page.keyboard.press('Control+c');
    await page.waitForTimeout(200);
    // A file path opens in the editor at its line; one that is missing or
    // outside the home is plain text.
    {
      const { clickPath, editorAt, pathPoint, mod } = require('./path-links.cjs');
      await page.keyboard.type('echo see iwato/web/app.js:2 and iwato/nope.js here');
      await page.keyboard.press('Enter');
      await page.waitForTimeout(400);
      check(await card.locator('.iw-screen .iw-url[data-url^="iwato-path:"]').count() === 0, 'paths are not underlined while typing');
      if (!phone) {
        const hint = card.locator('.iw-hint > div');
        const hover = async (text) => {
          const at = await pathPoint(card, text);
          await page.mouse.move(at.x, at.y);
          const before = await hint.count();
          await page.keyboard.down(mod);
          await page.waitForTimeout(600);
          const held = { count: await hint.count(), pointer: await card.locator('.iw-term').evaluate((e) => getComputedStyle(e).cursor) };
          await page.keyboard.up(mod);
          await page.waitForTimeout(100);
          return { before, held, after: await hint.count() };
        };
        const file = await hover('iwato/web/app.js:2');
        check(file.before === 0 && file.held.count === 1 && file.held.pointer === 'pointer' && file.after === 0, 'a file\'s path is underlined only while ' + mod + ' is held over it (' + JSON.stringify(file) + ')');
        const missing = await hover('iwato/nope.js');
        check(missing.held.count === 0 && missing.held.pointer !== 'pointer', 'a missing file\'s path is not (' + JSON.stringify(missing) + ')');
        await input.focus();
      }
      await clickPath(page, card, '見て iwato/web/app.js:2:3 を開く', 'iwato/web/app.js:2:3', phone);
      const at = await editorAt(page);
      check(await card.getAttribute('data-tab') === 'editor' && at.text.includes("console.log('app')") && at.line === 2 && at.col === 3, 'a relative path (from ~/work) opens in the editor tab at its line and column (' + JSON.stringify({ line: at.line, col: at.col }) + ')');
      await card.getByRole('tab', { name: /^ターミナル/ }).click();
      const message = () => card.locator('.card-message-text').allInnerTexts().then((a) => a.join(' '));
      await clickPath(page, card, 'iwato/nope.js と /etc/hosts', 'iwato/nope.js', phone);
      await page.waitForTimeout(600);
      check(!/ファイルがありません|開け/.test(await message()) && await card.getAttribute('data-tab') === 'terminal', 'a click on a missing file does nothing');
      await clickPath(page, card, '/etc/hosts', '/etc/hosts', phone);
      await page.waitForTimeout(600);
      check(!/開け/.test(await message()) && await card.getAttribute('data-tab') === 'terminal', 'nor on a path outside the home');
      await card.getByRole('button', { name: 'その他の操作' }).click();
      await card.getByRole('menuitem', { name: 'リンク' }).click();
      const listed = card.locator('.links-panel .terminal-path', { hasText: 'iwato/web/app.js:2:3' });
      await listed.first().waitFor({ timeout: 5000 }).catch(() => {});
      check(await listed.count() > 0 && await card.locator('.links-panel .terminal-path', { hasText: 'nope.js' }).count() === 0, 'the links view lists the paths of files, not missing ones');
      await listed.first().click();
      const fromList = await editorAt(page);
      check(await card.getAttribute('data-tab') === 'editor' && fromList.line === 2, 'a path in the links view opens in the editor');
      await card.getByRole('tab', { name: /^ターミナル/ }).click();
      await input.focus();
    }
    // IME: typing the next word commits the previous one (compositionend,
    // then at once compositionstart with the new letter in the field).
    // "iwato [convert] wo [enter]" must send 岩戸を, not 岩戸wを.
    // Each step is a keystroke: the page runs its timers in between.
    const ime = async (firefox) => {
      // [type, the field's value, the composition's text]
      const steps = [
        ['start', ''], ['input', 'いわと', 'いわと'], ['input', '岩戸', '岩戸'],
        ['end', '岩戸'], ['start', ''], ['input', '岩戸w', 'w'],
        ['input', '岩戸を', 'を'], ['check', 'を'], ['end', 'を']];
      for (const [type, text, data] of steps) {
        if (type === 'check') {
          const shown = await card.locator('.iw-preedit').innerText();
          check(shown === text, 'IME: only the text being composed shows (' + JSON.stringify(shown) + ')');
          continue;
        }
        await input.evaluate((el, [type, text, data, firefox]) => {
          const fire = (e) => el.dispatchEvent(e);
          if (type === 'start') fire(new CompositionEvent('compositionstart', { data: '', bubbles: true }));
          if (type === 'input') {
            el.value = text;
            fire(new CompositionEvent('compositionupdate', { data, bubbles: true }));
            fire(new InputEvent('input', { data, isComposing: true, bubbles: true }));
          }
          if (type === 'end') {
            fire(new CompositionEvent('compositionend', { data: text, bubbles: true }));
            if (firefox) fire(new InputEvent('input', { data: text, isComposing: false, bubbles: true }));
          }
        }, [type, text, data, firefox]);
        // compositionend and the next compositionstart come in one keystroke.
        if (type !== 'end') await page.waitForTimeout(20);
      }
    };
    await page.keyboard.type('echo ');
    await ime(false);
    await page.waitForTimeout(50);
    await page.keyboard.type(' ');
    await ime(true);
    await page.waitForTimeout(50);
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    const echoed = (await screenText()).split('\n').filter((l) => l.startsWith('岩戸'));
    check(echoed.length > 0 && echoed.at(-1) === '岩戸を 岩戸を', 'IME: committing by typing on sends only the committed text (' + JSON.stringify(echoed.at(-1)) + ')');
    // With a Japanese IME on, Windows reports the arrows and Ctrl+letter
    // as keyCode 229 ("Process") outside a composition.
    const cdp = await context.newCDPSession(page);
    typed = '';
    for (const [code, modifiers] of [['ArrowUp', 0], ['ArrowDown', 0], ['KeyP', 2], ['KeyN', 2]]) {
      await cdp.send('Input.dispatchKeyEvent', { type: 'rawKeyDown', windowsVirtualKeyCode: 229, key: 'Process', code, modifiers });
      await cdp.send('Input.dispatchKeyEvent', { type: 'keyUp', windowsVirtualKeyCode: 229, key: 'Process', code, modifiers });
    }
    await page.waitForTimeout(200);
    check(typed === '\x1b[A\x1b[B\x10\x0e', 'IME on: the arrows and Ctrl+P/N reported as keyCode 229 reach the session (' + JSON.stringify(typed) + ')');
    // Esc is the program's; on the links tab it closes the tab as 閉じる does.
    typed = '';
    await page.keyboard.press('Escape');
    await page.waitForTimeout(200);
    check(typed === '\x1b', 'Escape reaches the session (' + JSON.stringify(typed) + ')');
    await card.getByRole('button', { name: 'その他の操作' }).click();
    await card.getByRole('menuitem', { name: 'リンク' }).click();
    await card.locator('.links-panel').waitFor();
    await page.keyboard.press('Escape');
    check(await card.locator('.links-panel').isHidden() && await card.locator('.iw-screen').isVisible(), 'Escape closes the links tab');
    await input.focus();
    await page.keyboard.press('Control+c');
    await page.keyboard.type('logo');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    // Block elements fill whole cells, so Claude Code's logo joins up.
    const blocks = await page.evaluate(() => {
      const t = document.querySelector('.terminal-card.selected .iw-term');
      const cw = parseFloat(t.style.getPropertyValue('--iw-cw'));
      const ch = parseFloat(t.style.getPropertyValue('--iw-ch'));
      const els = [...t.querySelectorAll('.iw-screen .iw-blk')];
      return els.length >= 30 && els.every((e) => { const r = e.getBoundingClientRect(); const cs = getComputedStyle(e, '::before'); return Math.abs(r.width - cw) < 0.5 && Math.abs(r.height - ch) < 0.5 && cs.content !== 'none' && parseFloat(cs.width) > cw; });
    });
    check(blocks, 'block elements (Claude Code\'s logo) fill their cells and overlap their neighbours');
    await page.screenshot({ path: path.join(dir, 'logo.png') });
    // A selection over several rows is painted the height of the rows, so
    // no line shows between them.
    const selection = await page.evaluate(() => {
      const t = document.querySelector('.terminal-card.selected .iw-term');
      const ch = parseFloat(t.style.getPropertyValue('--iw-ch'));
      const all = [...t.querySelectorAll('.iw-screen .iw-row')];
      const first = all.findIndex((r) => r.textContent.includes('Claude Code'));
      const rows = all.slice(first, first + 7);
      const range = document.createRange();
      range.setStart(rows[0].firstChild || rows[0], 0);
      range.setEnd(rows.at(-1), rows.at(-1).childNodes.length);
      getSelection().removeAllRanges();
      getSelection().addRange(range);
      return new Promise((resolve) => setTimeout(() => {
        const boxes = [...t.querySelectorAll('.iw-sel > div')].map((b) => b.getBoundingClientRect()).sort((a, b) => a.top - b.top);
        getSelection().removeAllRanges();
        resolve(boxes.length >= 7 && boxes.every((b) => Math.abs(b.height - ch) < 0.5) && boxes.slice(1).every((b, i) => Math.abs(b.top - boxes[i].bottom) < 0.5));
      }, 100));
    });
    check(selection, 'a selection fills whole rows with no line between them, also over Japanese and box drawing');
    await page.keyboard.type('diff');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    // Colored rows (Claude Code's diffs and its own selection) meet with no
    // line between them, also over Japanese and box drawing.
    const colored = await page.evaluate(() => {
      const t = document.querySelector('.terminal-card.selected .iw-term');
      const ch = parseFloat(t.style.getPropertyValue('--iw-ch'));
      const rows = [...t.querySelectorAll('.iw-screen .iw-row')].filter((r) => /^36[78] /.test(r.textContent));
      const spans = rows.map((r) => [...r.children].find((e) => getComputedStyle(e).backgroundColor !== 'rgba(0, 0, 0, 0)'));
      if (rows.length !== 3 || spans.some((s) => !s)) return false;
      return spans.every((s, i) => { const a = s.getBoundingClientRect(); const r = rows[i].getBoundingClientRect(); return Math.abs(a.top - r.top) < 0.5 && a.bottom >= r.bottom; });
    });
    check(colored, 'colored rows meet with no line between them');
    await page.keyboard.type('colors');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    await page.screenshot({ path: path.join(dir, 'colors.png') });
    // A wide character takes exactly two cells.
    const widths = await page.evaluate(() => {
      const t = document.querySelector('.terminal-card.selected .iw-term');
      const cw = parseFloat(t.style.getPropertyValue('--iw-cw'));
      const wide = t.querySelector('.iw-screen .iw-c2');
      return { cw, wide: wide ? wide.getBoundingClientRect().width : 0 };
    });
    check(Math.abs(widths.wide - 2 * widths.cw) < 0.5, 'a wide character is two cells wide (' + widths.wide.toFixed(2) + ' vs ' + (2 * widths.cw).toFixed(2) + ')');
    // Rows line up on the grid: every row is one cell high.
    const rows = await page.evaluate(() => [...document.querySelectorAll('.terminal-card.selected .iw-row')].map((r) => r.getBoundingClientRect().height));
    check(rows.length > 5 && rows.every((h) => Math.abs(h - rows[0]) < 0.01), 'rows are of one height (' + rows[0] + 'px × ' + rows.length + ')');
    await page.keyboard.type('lines');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(800);
    const before = { ...traffic };
    console.log('after "lines": received ' + traffic.received + ' B in ' + traffic.frames + ' frames, ' + traffic.pages + ' pages');
    // Scrolling to the top fetches the older lines in pages.
    // One screen at a time, as a person would.
    for (let i = 0; i < 60; i++) {
      const top = await card.locator('.iw-term').evaluate((t) => { t.scrollTop -= t.clientHeight; return t.scrollTop; });
      await page.waitForTimeout(120);
      if (top === 0 && !(await card.locator('.iw-gap').count()) && (await card.locator('.iw-more').isHidden())) break;
    }
    const hist = await card.locator('.iw-hist').innerText();
    check(hist.includes('001 履歴の行です'), 'the first history line was fetched by scrolling up');
    check(traffic.pages > before.pages, 'history came in pages (' + (traffic.pages - before.pages) + ' more)');
    await page.screenshot({ path: path.join(dir, 'history-top.png') });
    // Typing while scrolled up goes back to the very bottom, not only to
    // the cursor's row (Claude Code has status rows below its input box):
    // after clear the cursor is on the top row.
    await card.locator('.iw-term').evaluate((t) => { t.scrollTop = t.scrollHeight; });
    await input.focus();
    await page.keyboard.type('clear');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    await card.locator('.iw-term').evaluate((t) => { t.scrollTop = Math.max(0, t.scrollTop - 3 * t.clientHeight); });
    await page.waitForTimeout(300);
    await page.keyboard.type('echo bottom');
    await page.keyboard.press('Enter');
    await page.waitForTimeout(400);
    const atBottom = await card.locator('.iw-term').evaluate((t) => t.scrollTop + t.clientHeight >= t.scrollHeight - 4);
    check(atBottom && (await screenText()).includes('bottom'), 'typing while scrolled up goes back to the bottom');
    // Every line is there once, in order, with nothing left to fetch.
    const ids = await page.evaluate(() => [...document.querySelectorAll('.terminal-card.selected .iw-seg')].map((e) => Number(e.dataset.id)));
    const gaps = await page.evaluate(() => document.querySelectorAll('.terminal-card.selected .iw-gap').length);
    const breaks = ids.filter((id, i) => i && id !== ids[i - 1] + 1).map((id) => ids[ids.indexOf(id) - 1] + '->' + id);
    if (breaks.length) console.log('breaks: ' + breaks.join(' ') + ' first ' + ids[0]);
    check(ids.length > 250 && ids[0] === 0 && ids.every((id, i) => id === i) && gaps === 0, 'history lines 0..' + ids.at(-1) + ' in order (' + ids.length + ' lines, ' + gaps + ' gaps)');
    await page.screenshot({ path: path.join(dir, 'bottom.png') });
    // A narrower window wraps the history without asking the server.
    await page.setViewportSize(phone ? { width: 320, height: 700 } : { width: 700, height: 860 });
    await page.waitForTimeout(800);
    await page.screenshot({ path: path.join(dir, 'narrow.png') });
    const dims = await card.locator('.dimensions').innerText();
    check(/^\d+ × \d+$/.test(dims), 'resized to ' + dims);
    // The font arrives while the terminal is on another tab: the cell is
    // measured again once it shows, so boxed characters, the cursor and the
    // composing text keep to the glyphs' advance.
    {
      // Same context (the login); routing turns the HTTP cache off.
      const p = await context.newPage();
      p.on('pageerror', (e) => failures.push('page error: ' + e.message));
      let release;
      const held = new Promise((r) => { release = r; });
      await p.route('**/vendor/fonts/*', async (route) => { await held; await route.continue(); });
      await p.goto(url.replace(/#.*/, ''), { waitUntil: 'domcontentloaded' });
      const c = p.locator('.terminal-card.selected');
      const again = c.getByRole('button', { name: /^接続/ });
      await again.or(c.locator('.connection-label[data-state=connected]')).first().waitFor({ state: 'attached' });
      if (await again.isVisible() && await again.isEnabled()) await again.click();
      await c.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
      await p.waitForTimeout(300);
      await c.getByRole('tab', { name: 'ポリシー' }).click();
      release();
      await p.evaluate(() => document.fonts.ready);
      await p.waitForTimeout(500);
      await c.getByRole('tab', { name: 'ターミナル' }).click();
      await p.waitForTimeout(500);
      const m = await c.locator('.iw-term').evaluate((root) => {
        const probe = document.createElement('span');
        probe.style.cssText = 'position:absolute;white-space:pre';
        probe.textContent = 'W'.repeat(100);
        root.append(probe);
        const advance = probe.getBoundingClientRect().width / 100;
        probe.remove();
        const wide = root.querySelector('.iw-screen .iw-c2');
        return { font: getComputedStyle(root).fontFamily, cell: parseFloat(root.style.getPropertyValue('--iw-cw')), advance, wide: wide ? wide.getBoundingClientRect().width : 2 * advance };
      });
      check(m.font.startsWith('"Moralerspace') && Math.abs(m.cell - m.advance) < 0.01 && Math.abs(m.wide - 2 * m.advance) < 0.01, 'a font that loads while the terminal is hidden: cells match the glyphs (cell ' + m.cell + ', advance ' + m.advance + ', wide ' + m.wide + ')');
      await p.close();
    }
    // Ctrl-D ends a shell: an extra terminal gives way to main; main, the
    // last one, stops the sandbox (autostop.go), and the card shows it
    // stopped instead of the last screen.
    await card.getByRole('button', { name: '新しいターミナル' }).click();
    await page.waitForTimeout(1000);
    const tabs = () => card.locator('.term-tab').count();
    const opened = await tabs();
    await input.focus();
    await page.keyboard.press('Control+d');
    await page.waitForTimeout(1500);
    check(opened === 2 && await tabs() === 1 && await card.locator('.connection-label[data-state=connected]').count() === 1, 'Ctrl-D in an extra terminal shows main');
    await input.focus();
    await page.keyboard.press('Control+d');
    await card.locator('.badge[data-state=stopped]').waitFor({ timeout: 5000 }).catch(() => {});
    await page.waitForTimeout(500);
    check(await card.locator('.badge[data-state=stopped]').count() === 1 && await start.isVisible() && await card.locator('.iw-term').count() === 0 && await card.locator('.terminal-placeholder').isVisible(), 'Ctrl-D in the last terminal: the card shows the sandbox stopped, not the last screen');
    await page.screenshot({ path: path.join(dir, 'ended.png') });
    await start.click();
    await card.locator('.connection-label[data-state=connected]').waitFor({ state: 'attached' });
    await page.waitForTimeout(500);
    check((await screenText()).includes('IWATO · FAKE'), 'starting it again shows a new session');
    console.log('traffic: sent ' + traffic.sent + ' B, received ' + traffic.received + ' B, ' + traffic.frames + ' frames, ' + traffic.pages + ' pages');
    console.log('screenshots in ' + dir);
  } finally {
    await browser.close();
    server.kill();
  }
  if (failures.length) { console.log(failures.length + ' failed'); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
