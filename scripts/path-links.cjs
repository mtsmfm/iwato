'use strict';
// Shared by term-check, editor-check and split-check: a file path printed in
// the fake terminal opens in the editor (Ctrl+click, ⌘ on a Mac; a tap on
// a phone).
const mod = process.platform === 'darwin' ? 'Meta' : 'Control';

// pathPoint is the middle of the last place the screen shows text.
async function pathPoint(card, text) {
  const handle = await card.locator('.iw-screen').elementHandle();
  return handle.evaluate((screen, text) => {
    const row = [...screen.querySelectorAll('.iw-row')].reverse().find((r) => r.textContent.includes(text));
    const at = row.textContent.lastIndexOf(text);
    const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
    let offset = 0;
    const range = document.createRange();
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      if (offset <= at && at < offset + n.length) range.setStart(n, at - offset);
      if (offset < at + text.length && at + text.length <= offset + n.length) { range.setEnd(n, at + text.length - offset); break; }
      offset += n.length;
    }
    const r = range.getBoundingClientRect();
    return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
  }, text);
}

// clickPath prints text with the fake shell's echo and clicks link (the
// path as printed, :LINE included; paths are not underlined, so it clicks
// where the text is).
async function clickPath(page, card, text, link, phone) {
  await card.locator('.iw-input').focus();
  await page.keyboard.type('echo ' + text);
  await page.keyboard.press('Enter');
  await card.locator('.iw-screen').evaluate((s, text) => new Promise((resolve) => {
    const done = () => [...s.querySelectorAll('.iw-row')].some((r) => r.textContent.trim() === text) ? resolve() : requestAnimationFrame(done);
    done();
  }), text);
  const { x, y } = await pathPoint(card, link);
  if (phone) await page.touchscreen.tap(x, y);
  else {
    await page.keyboard.down(mod);
    await page.mouse.click(x, y);
    await page.keyboard.up(mod);
  }
}

// editorAt is the editor's text and its cursor (1-based line and column),
// once its CodeMirror shows text.
async function editorAt(page) {
  await page.waitForFunction(() => document.querySelector('.terminal-card.selected .editor-panel .cm-editor')?.offsetParent);
  return page.evaluate(() => {
    const el = document.querySelector('.terminal-card.selected .editor-panel .cm-editor');
    const v = window.IwatoCodeMirror.EditorView.findFromDOM(el);
    const head = v.state.selection.main.head;
    const line = v.state.doc.lineAt(head);
    return { text: v.state.doc.toString(), line: line.number, col: head - line.from + 1 };
  });
}

module.exports = { clickPath, editorAt, pathPoint, mod };
