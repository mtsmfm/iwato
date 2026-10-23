'use strict';
// Shared by term-check, editor-check and split-check: a file path printed in
// the fake terminal opens in the editor (Ctrl+click, ⌘ on a Mac; a tap on
// a phone).
const mod = process.platform === 'darwin' ? 'Meta' : 'Control';

// clickPath prints text with the fake shell's echo and clicks the link of
// link (the path as printed, :LINE included).
async function clickPath(page, card, text, link, phone) {
  await card.locator('.iw-input').focus();
  await page.keyboard.type('echo ' + text);
  await page.keyboard.press('Enter');
  const piece = card.locator(`.iw-screen .iw-url[data-url="iwato-path:${link}"]`).last();
  await piece.waitFor();
  if (phone) await piece.tap();
  else await piece.click({ modifiers: [mod] });
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

module.exports = { clickPath, editorAt };
