/* Pure link detection for terminal rows; no DOM access. */
(() => {
  'use strict';
  const URL_CHARS = /[A-Za-z0-9\-._~:/?#[\]@!$&'()*+,;=%]/;
  const URL_RE = /https?:\/\/[A-Za-z0-9\-._~:/?#[\]@!$&'()*+,;=%]+/g;
  const FRAME = '│┃║|';

  // A row is {cells: [{c, x}], wrapped}: one entry per code unit with its
  // 0-based cell column (wide characters keep their first column).
  function lastContent(row) {
    let i = row.cells.length - 1;
    while (i >= 0 && row.cells[i].c === ' ') i--;
    return i;
  }

  // Applications such as Claude Code wrap long text themselves, so a URL that
  // runs into the right edge continues on the next row without the
  // terminal's own wrap flag. A right frame border plus one padding cell is allowed.
  function reachesEdge(row, cols) {
    let i = lastContent(row);
    if (i < 0) return false;
    if (FRAME.includes(row.cells[i].c)) {
      i--;
      if (i >= 0 && row.cells[i].c === ' ') i--;
      return i >= 0 && row.cells[i].c !== ' ' && row.cells[i].x >= cols - 4;
    }
    return row.cells[i].x >= cols - 2;
  }

  function trimURL(url) {
    for (;;) {
      const last = url.at(-1);
      if ('.,;:!?\'"'.includes(last)) { url = url.slice(0, -1); continue; }
      if (last === ')' && url.split('(').length < url.split(')').length) { url = url.slice(0, -1); continue; }
      if (last === ']' && url.split('[').length < url.split(']').length) { url = url.slice(0, -1); continue; }
      return url;
    }
  }

  // wrappedLinks returns [{url, start: {x, y}, end: {x, y}}] in 1-based,
  // inclusive cell coordinates. rows[0] is buffer line firstY.
  function wrappedLinks(rows, cols, firstY) {
    const links = [];
    let i = 0;
    while (i < rows.length) {
      // Build one logical line: text plus the cell/row of every code unit.
      let text = '';
      const pos = [];
      const push = (row, r, from) => {
        for (let k = from; k < row.cells.length; k++) { text += row.cells[k].c; pos.push({x: row.cells[k].x, r}); }
      };
      push(rows[i], i, 0);
      let j = i + 1;
      while (j < rows.length) {
        const prev = rows[j - 1], row = rows[j];
        if (row.wrapped) { text = text.replace(/ +$/, ''); pos.length = text.length; push(row, j, 0); j++; continue; }
        if (!reachesEdge(prev, cols)) break;
        // Join only when a URL ends at the edge and the next row continues it.
        const before = text.replace(/[ │┃║|]+$/, '');
        const tail = before.match(/https?:\/\/\S*$/);
        let k = 0;
        while (k < row.cells.length && (row.cells[k].c === ' ' || FRAME.includes(row.cells[k].c))) k++;
        if (!tail || k >= row.cells.length || !URL_CHARS.test(row.cells[k].c)) break;
        text = before; pos.length = before.length;
        push(row, j, k);
        j++;
      }
      for (const m of text.matchAll(URL_RE)) {
        const url = trimURL(m[0]);
        if (url.length < 12) continue;
        const a = pos[m.index], b = pos[m.index + url.length - 1];
        links.push({url, start: {x: a.x + 1, y: firstY + a.r}, end: {x: b.x + 1, y: firstY + b.r}});
      }
      i = j;
    }
    return links;
  }

  function safeURL(value) {
    if (typeof value !== 'string' || value.length > 4096 || /[\s\u0000-\u001f\u007f]/.test(value)) return '';
    try {
      const url = new URL(value);
      return (url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password ? url.href : '';
    } catch (_) { return ''; }
  }

  window.IwatoLinks = Object.freeze({wrappedLinks, safeURL});
})();
