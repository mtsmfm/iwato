/* Iwato's terminal. The session's terminal (internal/screen) is the only
   copy of its state; this draws what internal/termsync sends: the screen as
   rows of cells, the history as lines it fetches when they come into view.
   Nothing here parses terminal output. Text only ever goes to textContent. */
(function () {
  'use strict';
  // The UI's strings (web/i18n.js); the key itself where it is not loaded.
  const t = (key, params) => window.IwatoI18n ? window.IwatoI18n.t(key, params) : key;

  const OP = { RESET: 1, STYLE: 2, LINK: 3, RESIZE: 4, ALT: 5, SCROLL: 6, HISTFIRST: 7, ROW: 8, PATCH: 9, CURSOR: 10, MODES: 11, TITLE: 12, BELL: 13, CLIPBOARD: 14, OPEN: 15, LINES: 16 };
  const KIND = { hello: 0x76, input: 0x64, resize: 0x72, ack: 0x61, history: 0x67, frame: 0x46, page: 0x50 };
  const PAGE = 200; // history lines per request
  const MAX_LINES = 5000; // history lines kept in the page; older ones are fetched again
  const decoder = new TextDecoder('utf-8');
  const encoder = new TextEncoder();
  const FLAG = { bold: 1, dim: 2, italic: 4, blink: 8, inverse: 16, hidden: 32, strike: 64, overline: 128 };

  function uvarints(...values) {
    const out = [];
    for (let v of values) {
      while (v >= 0x80) { out.push((v % 128) | 0x80); v = Math.floor(v / 128); }
      out.push(v);
    }
    return out;
  }

  // A message for the server: its kind byte, then the payload.
  function message(kind, payload) {
    const out = new Uint8Array(1 + payload.length);
    out[0] = kind;
    out.set(payload, 1);
    return out;
  }

  class Reader {
    constructor(bytes) { this.b = bytes; this.i = 0; }
    get done() { return this.i >= this.b.length; }
    u() {
      let v = 0;
      let m = 1;
      for (;;) {
        if (this.i >= this.b.length) throw new Error('short message');
        const c = this.b[this.i++];
        v += (c & 0x7f) * m;
        if (c < 0x80) return v;
        m *= 128;
      }
    }
    byte() {
      if (this.i >= this.b.length) throw new Error('short message');
      return this.b[this.i++];
    }
    bytes() {
      const n = this.u();
      if (this.i + n > this.b.length) throw new Error('short message');
      const s = this.b.subarray(this.i, this.i + n);
      this.i += n;
      return s;
    }
    str() { return decoder.decode(this.bytes()); }
  }

  // A row's cells from screen.AppendRow's encoding: {t, w, s}; a wide cell
  // is followed by its right half {t: '', w: 0}.
  function decodeCells(bytes) {
    const r = new Reader(bytes);
    const cells = [];
    while (!r.done) {
      const s = r.u();
      const kind = r.byte();
      const text = decoder.decode(r.bytes());
      const w = kind & 3;
      const push = (t) => {
        cells.push({ t, w, s });
        if (w === 2) cells.push({ t: '', w: 0, s });
      };
      if (kind & 4) push(text);
      else for (const ch of text) push(ch);
    }
    return cells;
  }

  const blankCell = () => ({ t: ' ', w: 1, s: 0 });
  const blankCells = (n) => Array.from({ length: n }, blankCell);

  // Box drawing and block elements are stretched to the row's height so
  // that lines join across rows.
  const isBox = (t) => { const c = t.codePointAt(0); return c >= 0x2500 && c <= 0x257f; };

  // The cells a character takes: 0 for a combining mark, 2 for East Asian wide
  // and fullwidth, else 1. The server decides this for what it prints; only the
  // text an IME is composing, which the page draws itself, needs it here.
  const cellsOf = (t) => {
    const c = t.codePointAt(0);
    if ((c >= 0x300 && c <= 0x36f) || c === 0x200d || (c >= 0xfe00 && c <= 0xfe0f)) return 0;
    if ((c >= 0x1100 && c <= 0x115f) || (c >= 0x2e80 && c <= 0x303e) || (c >= 0x3041 && c <= 0x33ff) ||
      (c >= 0x3400 && c <= 0x4dbf) || (c >= 0x4e00 && c <= 0x9fff) || (c >= 0xa000 && c <= 0xa4cf) ||
      (c >= 0xac00 && c <= 0xd7a3) || (c >= 0xf900 && c <= 0xfaff) || (c >= 0xfe30 && c <= 0xfe6f) ||
      (c >= 0xff00 && c <= 0xff60) || (c >= 0xffe0 && c <= 0xffe6) || (c >= 0x1f300 && c <= 0x1f64f) ||
      (c >= 0x1f900 && c <= 0x1f9ff) || (c >= 0x20000 && c <= 0x3fffd)) return 2;
    return 1;
  };

  // Lays the composing text out as a terminal would print it from the cursor
  // (col, row): a character that does not fit in the row starts the next one
  // at column 0. The lines are shifted up when the last would fall below the
  // screen.
  function preeditLayout(text, col, row, cols, rows) {
    const lines = [{ row, col, chars: [] }];
    let x = col;
    for (const t of Array.from(text)) {
      const w = cellsOf(t);
      if (w === 0) {
        const last = lines[lines.length - 1].chars.at(-1);
        if (last) last.t += t;
        continue;
      }
      if (x > 0 && x + w > cols) {
        lines.push({ row: lines[lines.length - 1].row + 1, col: 0, chars: [] });
        x = 0;
      }
      lines[lines.length - 1].chars.push({ t, w });
      x += w;
    }
    const over = lines[lines.length - 1].row - (rows - 1);
    if (over > 0) for (const l of lines) l.row = Math.max(0, l.row - over);
    return lines.filter((l) => l.chars.length);
  }

  // Block elements (▀▐▛█▜▌, Claude Code's logo) are painted to fill the
  // cell instead of drawn with the font, whose glyphs do not meet across
  // cells and rows. Each is one mask of rectangles [x0, y0, x1, y1] in
  // eighths of the cell, drawn a little past the cell (styles.css) so that
  // neighbours overlap instead of leaving a hairline at fractional pixels.
  const blockShapes = (() => {
    const m = new Map();
    const UL = [0, 0, 4, 4], UR = [4, 0, 8, 4], LL = [0, 4, 4, 8], LR = [4, 4, 8, 8];
    m.set(0x2580, [[0, 0, 8, 4]]);
    for (let n = 1; n <= 8; n++) m.set(0x2580 + n, [[0, 8 - n, 8, 8]]);
    for (let n = 7; n >= 1; n--) m.set(0x2588 + 8 - n, [[0, 0, n, 8]]);
    m.set(0x2590, [[4, 0, 8, 8]]);
    m.set(0x2594, [[0, 0, 8, 1]]);
    m.set(0x2595, [[7, 0, 8, 8]]);
    [[LL], [LR], [UL], [UL, LL, LR], [UL, LR], [UL, UR, LL], [UL, UR, LR], [UR], [UR, LL], [UR, LL, LR]]
      .forEach((r, i) => m.set(0x2596 + i, r));
    // Shades: the whole cell, lighter.
    [[0x2591, .25], [0x2592, .5], [0x2593, .75]].forEach(([code, o]) => m.set(code, Object.assign([[0, 0, 8, 8]], { o })));
    const css = new Map();
    for (const [code, v] of m) {
      const path = v.map(([x0, y0, x1, y1]) => `M${x0} ${y0}H${x1}V${y1}H${x0}Z`).join('');
      const svg = `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 8 8' preserveAspectRatio='none' shape-rendering='crispEdges'><path d='${path}'${v.o ? ` fill-opacity='${v.o}'` : ''}/></svg>`;
      css.set(code, `--iw-blk:url("data:image/svg+xml,${encodeURIComponent(svg)}")`);
    }
    return css;
  })();

  let instances = 0;

  class IwatoTerminal {
    // options: theme, fontFamily, fontSize, lineHeight, and callbacks
    // send(bytes) for protocol messages, input(text) for the program
    // (typed text goes through mapInput(text) first), onLink(url, event),
    // onClipboard(text), onOpen(url), onBell(), onTitle(text), onCursor(),
    // onFiles(files) for files pasted without text or dropped.
    constructor(host, options) {
      this.options = options;
      this.id = ++instances;
      this.cols = 0;
      this.rows = 0;
      this.modes = { appCursor: false, appKeypad: false, bracketedPaste: false, focus: false, colorScheme: false, mouse: 0, encoding: 0 };
      this.cursor = { x: 0, y: 0, hidden: false, blink: true, shape: 0 };
      this.styles = new Map();
      this.links = new Map();
      this.grid = [];
      this.dirty = new Set();
      this.urlLines = new Set(); // history segments drawn since the last render
      this.alt = false;
      this.hist = { first: 0, next: 0, from: 0, requested: -1, segs: new Map() };
      this.frame = null;
      this.ackDue = 0;
      this.composing = false;
      this.focused = false;

      this.root = document.createElement('div');
      this.root.className = 'iw-term iw-t' + this.id;
      this.root.tabIndex = -1;
      this.more = document.createElement('div');
      this.more.className = 'iw-more';
      this.histEl = document.createElement('div');
      this.histEl.className = 'iw-hist';
      this.screen = document.createElement('div');
      this.screen.className = 'iw-screen';
      this.cursorEl = document.createElement('div');
      this.cursorEl.className = 'iw-cursor';
      this.input = document.createElement('textarea');
      this.input.className = 'iw-input';
      this.input.setAttribute('aria-label', t('term.input'));
      this.input.autocapitalize = 'off';
      this.input.autocomplete = 'off';
      this.input.spellcheck = false;
      this.input.setAttribute('autocorrect', 'off');
      // The text being composed (IME), drawn at the cursor. The input
      // itself stays invisible: it may still hold committed text.
      this.preedit = document.createElement('div');
      this.preedit.className = 'iw-preedit';
      this.preedit.hidden = true;
      this.screen.append(this.cursorEl, this.input, this.preedit);
      this.root.append(this.more, this.histEl, this.screen);
      this.styleSheet = document.createElement('style');
      this.root.append(this.styleSheet);
      // The selection, painted the height of whole rows (the browser's own
      // only covers the glyphs and leaves a line between rows).
      this.selEl = document.createElement('div');
      this.selEl.className = 'iw-sel';
      this.root.append(this.selEl);
      this.onSelection = () => this.drawSelection();
      document.addEventListener('selectionchange', this.onSelection);
      host.append(this.root);
      this.stick = true;
      this.stuckTop = 0;
      this.measure();
      this.attachInput();
      this.attachPointer();
      this.root.addEventListener('scroll', () => {
        // Scroll events come later: the one for keeping to the bottom may
        // arrive after more lines came in, and must not read as the owner
        // scrolling up.
        if (this.stick && Math.abs(this.root.scrollTop - this.stuckTop) < 1) { this.maybeFetch(); return; }
        this.stick = this.root.scrollTop + this.root.clientHeight >= this.root.scrollHeight - 4;
        this.maybeFetch();
      }, { passive: true });
    }

    // ------------------------------------------------------------ layout

    // Measures the cell from the font as it renders now. A terminal not laid
    // out (display:none) measures nothing and keeps its cell until fit runs
    // again once shown (a guess of 0.6em is wider than the font's half-width
    // glyphs and spreads boxed characters, the cursor and the composing
    // text apart). Returns whether the cell changed.
    measure() {
      const o = this.options;
      const probe = document.createElement('span');
      probe.style.cssText = 'position:absolute;visibility:hidden;white-space:pre;font-family:' + o.fontFamily + ';font-size:' + o.fontSize + 'px';
      probe.textContent = 'W'.repeat(100);
      this.root.append(probe);
      const box = probe.getBoundingClientRect();
      probe.remove();
      const cellW = box.width > 0 ? box.width / 100 : this.cellW || o.fontSize * 0.6;
      const cellH = Math.ceil(o.fontSize * o.lineHeight);
      const glyph = box.height > 0 ? box.height : this.glyphH || o.fontSize * 1.2;
      const s = this.root.style;
      s.fontFamily = o.fontFamily;
      s.fontSize = o.fontSize + 'px';
      s.color = o.theme.foreground;
      s.background = o.theme.background;
      if (cellW === this.cellW && cellH === this.cellH && glyph === this.glyphH) return false;
      this.cellW = cellW;
      this.cellH = cellH;
      this.glyphH = glyph;
      s.setProperty('--iw-cw', cellW + 'px');
      s.setProperty('--iw-ch', cellH + 'px');
      s.setProperty('--iw-box', String(Math.max(1, cellH / glyph)));
      // An inline background covers only the glyphs' height; padding
      // stretches it over the whole row, so colored rows have no gaps.
      s.setProperty('--iw-pad', Math.max(0, (cellH - glyph) / 2) + 'px');
      return true;
    }

    // Measures again and moves what is placed in pixels (the rows' boxes
    // follow the CSS variables by themselves).
    remeasure() {
      if (!this.measure()) return false;
      this.screen.style.width = this.cols * this.cellW + 'px';
      this.placeCursor();
      this.drawSelection();
      return true;
    }

    setFont(family) {
      this.options.fontFamily = family;
      this.remeasure();
    }

    // The size that fits the host.
    fit() {
      this.remeasure();
      const width = this.root.clientWidth;
      const height = this.root.clientHeight;
      return { cols: Math.max(2, Math.floor(width / this.cellW)), rows: Math.max(2, Math.floor(height / this.cellH)) };
    }

    // ------------------------------------------------------------ messages

    // The hello carries the size to attach at; without one (a terminal
    // that is not laid out, whose fit would be 2×2) it says 0×0 and the
    // session keeps its size.
    hello(size) {
      const { cols, rows } = size || { cols: 0, rows: 0 };
      return message(KIND.hello, uvarints(1, cols, rows));
    }

    resizeMessage(cols, rows) { return message(KIND.resize, uvarints(cols, rows)); }

    // Takes one message from the server (its kind byte and payload).
    receive(bytes) {
      const kind = bytes[0];
      const r = new Reader(bytes.subarray(1));
      if (kind === KIND.frame) {
        const seq = r.u();
        this.apply(r);
        this.ackDue = seq;
      } else if (kind === KIND.page) {
        this.apply(r);
      }
      this.schedule();
    }

    apply(r) {
      while (!r.done) {
        const op = r.byte();
        switch (op) {
          case OP.RESET: {
            this.cols = r.u(); this.rows = r.u();
            this.setAlt(r.u() === 1);
            this.styles.clear(); this.links.clear();
            this.styleSheet.textContent = '';
            this.hist.first = r.u(); this.hist.next = r.u();
            this.clearHistory();
            this.hist.requested = -1;
            this.blankGrid();
            break;
          }
          case OP.STYLE: {
            const id = r.u();
            const st = { fg: r.u(), bg: r.u(), ul: r.u(), flags: r.u(), underline: r.u(), link: r.u() };
            this.styles.set(id, st);
            this.defineStyle(id, st);
            break;
          }
          case OP.LINK: { const id = r.u(); this.links.set(id, r.str()); break; }
          case OP.RESIZE: this.cols = r.u(); this.rows = r.u(); this.blankGrid(); break;
          case OP.ALT: this.setAlt(r.byte() === 1); this.blankGrid(); break;
          case OP.SCROLL: this.scroll(r); break;
          case OP.HISTFIRST: this.dropBefore(r.u()); break;
          case OP.ROW: {
            const y = r.u(); const cont = r.u() === 1; const cells = decodeCells(r.bytes());
            while (cells.length < this.cols) cells.push(blankCell());
            this.grid[y] = { cells: cells.slice(0, this.cols), cont };
            this.dirty.add(y);
            break;
          }
          case OP.PATCH: {
            const y = r.u(); const x = r.u(); const cells = decodeCells(r.bytes());
            const row = this.grid[y];
            for (let i = 0; i < cells.length && x + i < row.cells.length; i++) row.cells[x + i] = cells[i];
            this.dirty.add(y);
            break;
          }
          case OP.CURSOR: {
            this.cursor.x = r.u(); this.cursor.y = r.u();
            const flags = r.u();
            this.cursor.hidden = !!(flags & 1); this.cursor.blink = !!(flags & 2);
            this.cursor.shape = r.u();
            this.cursorMoved = true;
            break;
          }
          case OP.MODES: {
            const f = r.u();
            Object.assign(this.modes, { appCursor: !!(f & 1), appKeypad: !!(f & 2), bracketedPaste: !!(f & 4), focus: !!(f & 8), colorScheme: !!(f & 16) });
            this.modes.mouse = r.u(); this.modes.encoding = r.u();
            this.root.classList.toggle('iw-mouse', this.modes.mouse !== 0);
            break;
          }
          case OP.TITLE: this.title = r.str(); this.options.onTitle?.(this.title); break;
          case OP.BELL: this.options.onBell?.(); break;
          case OP.CLIPBOARD: this.options.onClipboard?.(r.str()); break;
          case OP.OPEN: this.options.onOpen?.(r.str()); break;
          case OP.LINES: this.lines(r); break;
          default: throw new Error('unknown operation ' + op);
        }
      }
    }

    blankGrid() {
      this.grid = Array.from({ length: this.rows }, () => ({ cells: blankCells(this.cols), cont: false }));
      // Only the rows are replaced: moving the input would lose the focus.
      for (const el of this.rowEls || []) el.remove();
      this.rowEls = Array.from({ length: this.rows }, () => {
        const el = document.createElement('div');
        el.className = 'iw-row';
        this.screen.insertBefore(el, this.cursorEl);
        return el;
      });
      for (let y = 0; y < this.rows; y++) this.dirty.add(y);
      this.screen.style.width = this.cols * this.cellW + 'px';
    }

    setAlt(on) {
      this.alt = on;
      this.root.classList.toggle('iw-alt', on);
    }

    // ------------------------------------------------------------ history
    //
    // The history shown is lines (one .iw-line per logical line, one .iw-seg
    // per history id in it) and gaps (.iw-gap: ids not fetched yet, as tall
    // as their lines would be). Lines past the top of what is shown and
    // gaps are fetched when they come into view.

    clearHistory() {
      this.histEl.replaceChildren();
      this.hist.segs.clear();
      this.hist.from = this.hist.next;
      this.hist.requested = -1;
    }

    scroll(r) {
      const n = r.u();
      const m = r.u();
      const h = this.hist;
      const known = this.alt ? 0 : Math.min(n, this.rows);
      for (let i = 0; i < known; i++) this.appendLine(h.next + i, this.grid[i].cont, this.grid[i].cells);
      if (n > known) this.appendGap(h.next + known, h.next + n);
      for (let j = 0; j < m; j++) {
        const i = r.u(); const cont = r.byte() === 1; const cells = decodeCells(r.bytes());
        this.fillLine(h.next + i, cont, cells);
      }
      if (!this.alt) {
        this.grid.splice(0, known);
        for (let i = 0; i < known; i++) this.grid.push({ cells: blankCells(this.cols), cont: false });
        for (let y = 0; y < this.rows; y++) this.dirty.add(y);
      }
      if (h.from === h.next && !this.histEl.firstElementChild) h.from = h.next + n;
      h.next += n;
      this.trimHistory();
    }

    seg(id, cont, cells) {
      const el = document.createElement('span');
      el.className = 'iw-seg';
      el.dataset.id = String(id);
      el.dataset.cont = cont ? '1' : '';
      this.renderCells(trimCells(cells), el, false);
      this.hist.segs.set(id, el);
      this.urlLines.add(el); // replaced by its line once placed
      return el;
    }

    // Adds history line id at the bottom (after the lines shown).
    appendLine(id, cont, cells) {
      const el = this.seg(id, cont, cells);
      const last = this.histEl.lastElementChild;
      if (cont && last && last.classList.contains('iw-line')) last.append(el);
      else {
        const line = document.createElement('div');
        line.className = 'iw-line';
        line.append(el);
        this.histEl.append(line);
      }
      if (this.histEl.childElementCount === 1) this.hist.from = id;
    }

    gapElement(from, to) {
      const gap = document.createElement('div');
      gap.className = 'iw-gap';
      this.setGap(gap, from, to);
      return gap;
    }

    setGap(gap, from, to) {
      gap.dataset.from = String(from);
      gap.dataset.to = String(to);
      gap.style.height = (to - from) * this.cellH + 'px';
    }

    appendGap(from, to) {
      const last = this.histEl.lastElementChild;
      if (last && last.classList.contains('iw-gap') && Number(last.dataset.to) === from) this.setGap(last, Number(last.dataset.from), to);
      else this.histEl.append(this.gapElement(from, to));
      if (this.histEl.childElementCount === 1) this.hist.from = from;
    }

    fillLine(id, cont, cells) {
      const old = this.hist.segs.get(id);
      if (!old) return;
      old.replaceChildren();
      old.dataset.cont = cont ? '1' : '';
      this.renderCells(trimCells(cells), old, false);
      this.urlLines.add(old);
    }

    // A page of history lines: into a gap, or above the lines shown.
    lines(r) {
      const first = r.u();
      const count = r.u();
      const h = this.hist;
      const items = [];
      for (let i = 0; i < count; i++) items.push({ id: first + i, cont: r.byte() === 1, cells: decodeCells(r.bytes()) });
      h.requested = -1;
      const before = this.root.scrollHeight;
      const anchor = this.stick ? null : this.anchor();
      const above = items.filter((it) => it.id < h.from && !h.segs.has(it.id));
      if (above.length) this.histEl.prepend(this.block(above));
      for (const gap of [...this.histEl.querySelectorAll(':scope > .iw-gap')]) {
        const from = Number(gap.dataset.from);
        const to = Number(gap.dataset.to);
        const inside = items.filter((it) => it.id >= from && it.id < to && !h.segs.has(it.id));
        if (!inside.length) continue;
        const a = inside[0].id;
        const b = inside[inside.length - 1].id + 1;
        const after = b < to ? this.gapElement(b, to) : null;
        const block = this.block(inside);
        const last = block.lastChild;
        gap.after(block);
        if (after) last.after(after);
        if (a > from) this.setGap(gap, from, a); else gap.remove();
      }
      for (const it of items) if (h.segs.has(it.id) && !above.includes(it)) this.fillLine(it.id, it.cont, it.cells);
      this.recomputeFrom();
      if (anchor) this.restoreAnchor(anchor);
      else if (!this.stick) this.root.scrollTop += this.root.scrollHeight - before;
      this.updateMore();
      this.schedule();
    }

    // Lines for a page, as a fragment.
    block(items) {
      const frag = document.createDocumentFragment();
      let line = null;
      for (const it of items) {
        const el = this.seg(it.id, it.cont, it.cells);
        if (!(it.cont && line)) {
          line = document.createElement('div');
          line.className = 'iw-line';
          frag.append(line);
        }
        line.append(el);
      }
      return frag;
    }

    // The first history element in view and its offset, to keep it in place
    // while lines come in above it.
    anchor() {
      const top = this.root.getBoundingClientRect().top;
      for (const el of this.histEl.children) {
        const rect = el.getBoundingClientRect();
        if (rect.bottom > top) return { el, offset: rect.top - top };
      }
      return null;
    }

    restoreAnchor(a) {
      if (!a.el.isConnected) return;
      const top = this.root.getBoundingClientRect().top;
      this.root.scrollTop += a.el.getBoundingClientRect().top - top - a.offset;
    }

    recomputeFrom() {
      const h = this.hist;
      const top = this.histEl.firstElementChild;
      if (!top) h.from = h.next;
      else if (top.classList.contains('iw-gap')) h.from = Number(top.dataset.from);
      else h.from = Number(top.firstElementChild.dataset.id);
    }

    dropBefore(first) {
      const h = this.hist;
      h.first = first;
      for (const [id, el] of h.segs) {
        if (id >= first) continue;
        const line = el.parentElement;
        el.remove();
        h.segs.delete(id);
        if (line && !line.childElementCount) line.remove();
      }
      for (const gap of [...this.histEl.querySelectorAll(':scope > .iw-gap')]) {
        const from = Number(gap.dataset.from);
        const to = Number(gap.dataset.to);
        if (to <= first) gap.remove();
        else if (from < first) this.setGap(gap, first, to);
      }
      this.recomputeFrom();
      this.updateMore();
    }

    // Keeps at most MAX_LINES history lines in the page; older ones are
    // fetched again when shown.
    trimHistory() {
      const h = this.hist;
      if (h.segs.size <= MAX_LINES) return;
      const before = this.root.scrollHeight;
      while (h.segs.size > MAX_LINES * 0.9) {
        const top = this.histEl.firstElementChild;
        if (!top) break;
        if (top.classList.contains('iw-line')) for (const el of top.children) h.segs.delete(Number(el.dataset.id));
        top.remove();
      }
      this.recomputeFrom();
      if (!this.stick) this.root.scrollTop -= before - this.root.scrollHeight;
      this.updateMore();
    }

    request(before, count) {
      if (this.hist.requested !== -1) return;
      this.hist.requested = before;
      this.options.send(message(KIND.history, uvarints(before, Math.min(count, 1000))));
    }

    updateMore() {
      const h = this.hist;
      this.more.hidden = this.alt || h.from <= h.first;
      this.more.textContent = t('term.loadingHistory');
    }

    // Fetches the lines in view (or about to be): a gap, or those above the
    // top of what is shown.
    maybeFetch() {
      const h = this.hist;
      this.updateMore();
      if (this.alt || h.requested !== -1) return;
      const view = this.root.getBoundingClientRect();
      const margin = view.height;
      for (const gap of this.histEl.querySelectorAll(':scope > .iw-gap')) {
        const rect = gap.getBoundingClientRect();
        if (rect.bottom >= view.top - margin && rect.top <= view.bottom + margin) {
          const from = Number(gap.dataset.from);
          const to = Number(gap.dataset.to);
          // About two screens, from the end nearest the view.
          const count = Math.min(PAGE, Math.ceil(2 * view.height / this.cellH) + 1);
          if (rect.bottom <= view.bottom + margin) this.request(to, count);
          else this.request(Math.min(to, from + count), count);
          return;
        }
      }
      if (h.from > h.first && this.root.scrollTop <= margin) this.request(h.from, PAGE);
    }

    // ------------------------------------------------------------ drawing

    schedule() {
      if (this.frame !== null) return;
      this.frame = requestAnimationFrame(() => { this.frame = null; this.render(); });
    }

    render() {
      const drawn = this.dirty.size > 0;
      for (const y of this.dirty) {
        const el = this.rowEls?.[y];
        const row = this.grid[y];
        if (!el || !row) continue;
        el.replaceChildren();
        this.renderCells(row.cells, el, true);
      }
      if (drawn) { this.decorateScreenLinks(); if (this.hotGroup) this.lightLink(); }
      const histLines = new Set();
      for (const seg of this.urlLines) {
        const line = seg.closest?.('.iw-line') || seg;
        if (line.isConnected) histLines.add(line);
      }
      for (const line of histLines) decorateURLs([line]);
      this.urlLines.clear();
      this.dirty.clear();
      this.placeCursor();
      if (this.stick) this.keepBottom();
      if (this.selEl.firstChild) this.drawSelection();
      if (this.ackDue) {
        this.options.send(message(KIND.ack, uvarints(this.ackDue)));
        this.ackDue = 0;
      }
      this.maybeFetch();
      if (this.cursorMoved) { this.cursorMoved = false; this.options.onCursor?.(); }
    }

    // Lights up every piece of the hovered link.
    lightLink() {
      for (const p of this.root.querySelectorAll('.iw-url-hot')) p.classList.remove('iw-url-hot');
      const g = this.hotGroup;
      if (!g) return;
      // Every piece showing the URL: an application's hyperlinks to it and
      // the URL text found in the output.
      const url = g.slice(1);
      for (const p of this.root.querySelectorAll('[data-link]')) if (this.links.get(Number(p.dataset.link)) === url) p.classList.add('iw-url-hot');
      for (const p of this.root.querySelectorAll('.iw-url')) if (p.dataset.url === url) p.classList.add('iw-url-hot');
    }

    // Underlines the screen's URLs as links.js finds them: across rows the
    // terminal wrapped and rows an application wrapped itself at the edge
    // (Claude Code), also inside a frame. Every piece of one URL carries it
    // whole and lights up with the others.
    decorateScreenLinks() {
      if (!window.IwatoLinks || !this.rowEls) return;
      const rows = this.grid.map((row) => {
        const cells = [];
        row.cells.forEach((c, x) => { if (c.w !== 0) for (const ch of (c.t || ' ')) cells.push({ c: ch, x }); });
        return { cells, wrapped: !!row.cont };
      });
      const pieces = new Map(); // row → [{from, to, url, group}] in cells
      for (const link of window.IwatoLinks.wrappedLinks(rows, this.cols, 0)) {
        // Stable across redraws, so a hovered URL stays lit while the rest
        // of the screen changes.
        const group = link.start.y + ':' + link.start.x;
        for (let y = link.start.y; y <= link.end.y; y++) {
          const cells = rows[y].cells;
          let from = y === link.start.y ? link.start.x - 1 : -1;
          let to = y === link.end.y ? link.end.x - 1 : -1;
          if (from < 0) { const first = cells.find((c) => c.c !== ' ' && !'│┃║|'.includes(c.c)); if (!first) continue; from = first.x; }
          if (to < 0) { const last = [...cells].reverse().find((c) => c.c !== ' ' && !'│┃║|'.includes(c.c)); if (!last) continue; to = last.x; }
          if (!pieces.has(y)) pieces.set(y, []);
          pieces.get(y).push({ from, to, url: link.url, group });
        }
      }
      const touched = new Set([...pieces.keys(), ...(this.linkRows || [])]);
      for (const y of touched) {
        const el = this.rowEls[y];
        if (!el) continue;
        for (const u of [...el.querySelectorAll('.iw-url')]) u.replaceWith(...u.childNodes);
        el.normalize();
        const list = pieces.get(y);
        if (!list) continue;
        // Text offsets of the cells, as renderCells draws them.
        const offset = [];
        let at = 0;
        this.grid[y].cells.forEach((c, x) => { offset[x] = at; if (c.w !== 0) at += (c.t || ' ').length; });
        offset[this.grid[y].cells.length] = at;
        for (const p of list.reverse()) wrapText(el, offset[p.from], offset[p.to + 1] ?? at, p.url, p.group);
      }
      this.linkRows = [...pieces.keys()];
    }

    // Draws cells into parent: runs of a style in a span, ASCII as text,
    // other characters in boxes of their cell width (fonts differ in width).
    renderCells(cells, parent, grid) {
      let end = cells.length;
      if (grid) while (end > 0 && cells[end - 1].s === 0 && cells[end - 1].t === ' ') end--;
      let span = parent;
      let style = 0;
      let text = '';
      const flush = () => { if (text) { span.append(text); text = ''; } };
      for (let i = 0; i < end; i++) {
        const c = cells[i];
        if (c.w === 0) continue;
        if (c.s !== style) {
          flush();
          style = c.s;
          if (style === 0) span = parent;
          else {
            span = document.createElement('span');
            span.className = 'iw-s' + style;
            const link = this.styles.get(style)?.link;
            if (link) span.dataset.link = String(link);
            parent.append(span);
          }
        }
        const t = c.t || ' ';
        const code = t.charCodeAt(0);
        if (c.w === 1 && t.length === 1 && code >= 0x20 && code < 0x7f) { text += t; continue; }
        flush();
        const box = document.createElement('span');
        const block = t.length === 1 ? blockShapes.get(code) : undefined;
        box.className = (c.w === 2 ? 'iw-c2' : 'iw-c1') + (isBox(t) ? ' iw-box' : block ? ' iw-blk' : '');
        if (block) box.style.cssText = block;
        box.textContent = t;
        span.append(box);
      }
      flush();
    }

    // Draws the composing text from the cursor, wrapping at the row's end.
    drawPreedit() {
      const box = this.preedit;
      if (box.hidden) return;
      box.replaceChildren(...preeditLayout(this.preeditText || '', this.cursor.x, this.cursor.y, this.cols, this.rows).map((l) => {
        const line = document.createElement('div');
        line.className = 'iw-preedit-line';
        line.style.transform = 'translate(' + l.col * this.cellW + 'px,' + l.row * this.cellH + 'px)';
        for (const c of l.chars) {
          const cell = document.createElement('span');
          cell.className = c.w === 2 ? 'iw-c2' : 'iw-c1';
          cell.textContent = c.t;
          line.append(cell);
        }
        return line;
      }));
    }

    placeCursor() {
      const c = this.cursor;
      const el = this.cursorEl;
      const cell = this.grid[c.y]?.cells[c.x];
      const wide = cell && cell.w === 2 ? 2 : 1;
      const shape = c.shape || 5; // the default: a blinking bar
      el.hidden = c.hidden;
      el.className = 'iw-cursor ' + (shape <= 2 ? 'iw-block' : shape <= 4 ? 'iw-under' : 'iw-bar') + (c.blink && shape % 2 === 1 ? ' iw-blink' : '') + (this.focused ? '' : ' iw-blur');
      el.style.transform = 'translate(' + c.x * this.cellW + 'px,' + c.y * this.cellH + 'px)';
      el.style.width = (shape <= 2 ? wide * this.cellW : shape <= 4 ? wide * this.cellW : 2) + 'px';
      this.input.style.transform = el.style.transform;
      this.drawPreedit();
    }

    defineStyle(id, st) {
      const theme = this.options.theme;
      let fg = color(st.fg, theme);
      let bg = color(st.bg, theme);
      if (st.flags & FLAG.inverse) [fg, bg] = [bg || theme.background, fg || theme.foreground];
      const rules = [];
      if (fg) rules.push('color:' + fg);
      if (bg) rules.push('background:' + bg, 'padding-block:var(--iw-pad)', '-webkit-box-decoration-break:clone', 'box-decoration-break:clone');
      if (st.flags & FLAG.bold) rules.push('font-weight:bold');
      if (st.flags & FLAG.italic) rules.push('font-style:italic');
      if (st.flags & FLAG.dim) rules.push('opacity:.6');
      if (st.flags & FLAG.hidden) rules.push('color:transparent');
      const lines = [];
      if (st.underline) lines.push('underline');
      if (st.flags & FLAG.strike) lines.push('line-through');
      if (st.flags & FLAG.overline) lines.push('overline');
      if (lines.length) {
        rules.push('text-decoration-line:' + lines.join(' '));
        const styles = ['solid', 'solid', 'double', 'wavy', 'dotted', 'dashed'];
        if (st.underline) rules.push('text-decoration-style:' + (styles[st.underline] || 'solid'));
        if (st.ul) rules.push('text-decoration-color:' + color(st.ul, theme));
      }
      let css = '.iw-t' + this.id + ' .iw-s' + id + '{' + rules.join(';') + '}\n';
      // On the screen (rows never wrap) a background is a box the row's
      // height, whatever the fonts of its glyphs (Japanese, box drawing),
      // reaching half a pixel into the next row so no line shows between rows
      // at fractional device pixels; that row paints over it.
      if (bg) css += '.iw-t' + this.id + ' .iw-row .iw-s' + id + '{display:inline-block;vertical-align:top;height:calc(var(--iw-ch) + .5px);padding-block:0}\n';
      this.styleSheet.textContent += css;
    }

    // ------------------------------------------------------------ input

    sendInput(text) {
      if (text) this.options.input(text);
    }

    // Typing (and pasting) goes back to the bottom, as in other terminals;
    // mouse reports do not.
    typed(text) {
      this.toBottom();
      this.sendInput(this.options.mapInput ? this.options.mapInput(text) : text);
    }

    toBottom() {
      this.stick = true;
      this.keepBottom();
    }

    keepBottom() {
      this.root.scrollTop = this.root.scrollHeight;
      this.stuckTop = this.root.scrollTop;
    }

    // The files of a clipboard or a drop (DataTransfer).
    static files(data) {
      if (!data) return [];
      const files = [];
      for (const item of Array.from(data.items || [])) {
        const file = item.kind === 'file' ? item.getAsFile() : null;
        if (file) files.push(file);
      }
      return files.length ? files : Array.from(data.files || []);
    }

    // The input message for the program's input text.
    static inputMessage(text) { return message(KIND.input, encoder.encode(text)); }

    paste(text) {
      text = String(text).replace(/\r\n?|\n/g, '\r');
      if (this.modes.bracketedPaste) text = '\x1b[200~' + text.replace(/\x1b\[201~/g, '') + '\x1b[201~';
      this.toBottom();
      this.sendInput(text);
    }

    focus() { this.input.focus({ preventScroll: true }); }

    attachInput() {
      const input = this.input;
      input.addEventListener('focus', () => { this.focused = true; this.placeCursor(); if (this.modes.focus) this.sendInput('\x1b[I'); });
      input.addEventListener('blur', () => { this.focused = false; this.placeCursor(); if (this.modes.focus) this.sendInput('\x1b[O'); });
      // The IME commits a word when the next one starts: compositionend
      // then at once compositionstart, with the new word already in the
      // field. Only the committed text (the event's data) is sent, and the
      // field is never emptied during a composition (that would break it):
      // what was sent of it is counted in sent and dropped once idle.
      this.sent = 0;
      input.addEventListener('compositionstart', () => {
        this.composing = true;
        this.root.classList.add('iw-composing');
        this.preeditText = '';
        this.preedit.hidden = false;
        this.drawPreedit();
      });
      input.addEventListener('compositionupdate', (event) => { this.preeditText = event.data || ''; this.drawPreedit(); });
      input.addEventListener('compositionend', (event) => {
        this.composing = false;
        this.root.classList.remove('iw-composing');
        this.preedit.hidden = true;
        this.preeditText = '';
        this.preedit.replaceChildren();
        if (event.data) this.typed(event.data.replace(/\r\n?|\n/g, '\r'));
        this.sent = input.value.length;
        setTimeout(() => { if (!this.composing) this.flushInput(); }, 0);
      });
      input.addEventListener('input', (event) => { if (!this.composing && !event.isComposing) this.flushInput(); });
      input.addEventListener('keydown', (event) => this.keydown(event));
      // Text wins: copying from an office suite puts a picture of the
      // cells next to their text. Only a paste without text (a screenshot,
      // an image copied from a page) gives its files to the page.
      input.addEventListener('paste', (event) => {
        const text = event.clipboardData?.getData('text/plain');
        if (text) {
          event.preventDefault();
          this.paste(text);
          return;
        }
        const files = IwatoTerminal.files(event.clipboardData);
        if (!files.length || !this.options.onFiles) return;
        event.preventDefault();
        this.options.onFiles(files);
      });
      // Files dropped on the terminal (a local terminal pastes their paths).
      const dropping = (event) => !!this.options.onFiles && Array.from(event.dataTransfer?.types || []).includes('Files');
      this.root.addEventListener('dragover', (event) => {
        if (!dropping(event)) return;
        event.preventDefault();
        event.dataTransfer.dropEffect = 'copy';
      });
      this.root.addEventListener('drop', (event) => {
        if (!dropping(event)) return;
        event.preventDefault();
        const files = IwatoTerminal.files(event.dataTransfer);
        if (files.length) this.options.onFiles(files);
      });
      this.root.addEventListener('copy', (event) => {
        const text = this.selectionText();
        if (!text) return;
        event.preventDefault();
        event.clipboardData.setData('text/plain', text);
      });
    }

    // Sends what was typed into the field outside a composition.
    flushInput() {
      const value = this.input.value.slice(this.sent);
      this.input.value = '';
      this.sent = 0;
      if (value) this.typed(value.replace(/\r\n?|\n/g, '\r'));
    }

    drawSelection() {
      const sel = window.getSelection();
      const range = sel && sel.rangeCount && !sel.isCollapsed ? sel.getRangeAt(0) : null;
      if (!range || !range.intersectsNode(this.root)) {
        if (this.selEl.firstChild) this.selEl.replaceChildren();
        return;
      }
      const base = this.root.getBoundingClientRect();
      const ch = this.cellH;
      // The selected text's boxes, grown to their rows and joined per row.
      const bands = new Map();
      const walker = document.createTreeWalker(range.commonAncestorContainer, NodeFilter.SHOW_TEXT);
      const part = document.createRange();
      for (let n = walker.currentNode.nodeType === 3 ? walker.currentNode : walker.nextNode(); n; n = walker.nextNode()) {
        if (!range.intersectsNode(n) || !this.root.contains(n) || n.parentNode === this.styleSheet) continue;
        part.selectNodeContents(n);
        if (n === range.startContainer) part.setStart(n, range.startOffset);
        if (n === range.endContainer) part.setEnd(n, range.endOffset);
        // On the grid of its row (a history line may take several rows):
        // glyphs of other fonts (Japanese, box drawing) sit higher or lower.
        const line = n.parentElement?.closest('.iw-row, .iw-line');
        const lineTop = line ? line.getBoundingClientRect().top : null;
        for (const r of part.getClientRects()) {
          if (r.width < 0.5) continue;
          const mid = r.top + r.height / 2;
          const rowTop = lineTop === null ? mid - ch / 2 : lineTop + Math.floor((mid - lineTop) / ch) * ch;
          const top = Math.round((rowTop - base.top + this.root.scrollTop) * 2) / 2;
          if (!bands.has(top)) bands.set(top, []);
          bands.get(top).push([r.left - base.left, r.right - base.left]);
        }
      }
      const boxes = [];
      for (const [top, spans] of bands) {
        spans.sort((a, b) => a[0] - b[0]);
        let [from, to] = spans[0];
        for (const [a, b] of spans.slice(1)) {
          if (a <= to + 1) { to = Math.max(to, b); continue; }
          boxes.push([top, from, to]);
          [from, to] = [a, b];
        }
        boxes.push([top, from, to]);
      }
      this.selEl.replaceChildren(...boxes.map(([top, from, to]) => {
        const box = document.createElement('div');
        box.style.cssText = `top:${top}px;left:${from}px;width:${to - from}px;height:${ch}px`;
        return box;
      }));
    }

    selectionText() {
      const sel = window.getSelection();
      if (!sel || sel.isCollapsed || !this.root.contains(sel.anchorNode)) return '';
      return sel.toString().split('\n').map((l) => l.replace(/\s+$/, '')).join('\n');
    }

    keydown(event) {
      if (event.isComposing || this.composing) return;
      // With a Japanese IME on, Windows reports Ctrl+letter and the arrows
      // (with Home, End...) as keyCode 229 ("Process") although nothing is
      // being composed: send them from the physical key. Keys that may
      // commit a composition (Enter, Backspace...) are left to the IME.
      if (event.keyCode === 229) {
        if (/^(Arrow(Up|Down|Left|Right)|Home|End|PageUp|PageDown|Insert|Delete)$/.test(event.code || '')) {
          const seq = keySequence({ key: event.code, shiftKey: event.shiftKey, altKey: event.altKey, ctrlKey: event.ctrlKey, metaKey: event.metaKey }, this.modes);
          if (seq === null) return;
          event.preventDefault();
          this.typed(seq);
          return;
        }
        const letter = /^Key([A-Z])$/.exec(event.code || '');
        if (!(event.ctrlKey && !event.altKey && !event.metaKey && letter) || letter[1] === 'V' || letter[1] === 'C') return;
        event.preventDefault();
        this.typed(String.fromCharCode(letter[1].charCodeAt(0) - 64));
        return;
      }
      const ctrl = event.ctrlKey && !event.altKey && !event.metaKey;
      const key = event.key;
      // Copy while text is selected (Ctrl+Shift+C always copies).
      if (ctrl && key.toLowerCase() === 'c' && (event.shiftKey || this.selectionText())) {
        event.preventDefault();
        const text = this.selectionText();
        if (text) navigator.clipboard?.writeText(text).catch(() => {});
        window.getSelection()?.removeAllRanges();
        return;
      }
      if (ctrl && key.toLowerCase() === 'v' && !event.shiftKey) return; // the browser pastes
      const seq = keySequence(event, this.modes);
      if (seq === null) return;
      event.preventDefault();
      this.typed(seq);
    }

    // ------------------------------------------------------------ pointer

    cellAt(clientX, clientY) {
      const rect = this.screen.getBoundingClientRect();
      const col = Math.min(this.cols, Math.max(1, Math.floor((clientX - rect.left) / this.cellW) + 1));
      const row = Math.min(this.rows, Math.max(1, Math.floor((clientY - rect.top) / this.cellH) + 1));
      return { col, row };
    }

    mouseReport(button, col, row, release, event) {
      let b = button;
      if (event) b += (event.shiftKey ? 4 : 0) + (event.altKey ? 8 : 0) + (event.ctrlKey ? 16 : 0);
      if (this.modes.encoding === 1006) return '\x1b[<' + b + ';' + col + ';' + row + (release ? 'm' : 'M');
      if (release) b = 3 + (b & ~3 & ~64);
      if (col > 223 || row > 223) return '';
      return '\x1b[M' + String.fromCharCode(32 + b, 32 + col, 32 + row);
    }

    attachPointer() {
      let down = -1;
      const tracking = () => this.modes.mouse !== 0;
      this.screen.addEventListener('mousedown', (event) => {
        if (!tracking() || event.shiftKey) return;
        event.preventDefault();
        this.focus();
        down = event.button;
        const { col, row } = this.cellAt(event.clientX, event.clientY);
        this.sendInput(this.mouseReport(event.button, col, row, false, event));
      });
      window.addEventListener('mouseup', (event) => {
        if (down < 0) return;
        const { col, row } = this.cellAt(event.clientX, event.clientY);
        if (this.modes.mouse !== 9) this.sendInput(this.mouseReport(down, col, row, true, event));
        down = -1;
      });
      let lastCell = '';
      this.screen.addEventListener('mousemove', (event) => {
        const any = this.modes.mouse === 1003;
        if (!tracking() || !(any || (this.modes.mouse === 1002 && down >= 0))) return;
        const { col, row } = this.cellAt(event.clientX, event.clientY);
        if (lastCell === col + ',' + row) return;
        lastCell = col + ',' + row;
        this.sendInput(this.mouseReport((down >= 0 ? down : 3) + 32, col, row, false, event));
      });
      // The wheel scrolls the page's history, or goes to an app that reads the mouse.
      let carry = 0;
      this.root.addEventListener('wheel', (event) => {
        if (event.ctrlKey) return;
        if (!tracking()) return;
        event.preventDefault();
        if (event.shiftKey) {
          // The history, by hand: the root does not scroll on its own while
          // the app reads the mouse (see .iw-mouse), so that a wheel event
          // the browser stopped waiting for (a busy page) cannot scroll it.
          this.root.scrollTop += event.deltaMode === 1 ? event.deltaY * this.cellH : event.deltaY;
          return;
        }
        carry += event.deltaMode === 1 ? event.deltaY * 40 : event.deltaY;
        const { col, row } = this.cellAt(event.clientX, event.clientY);
        while (Math.abs(carry) >= 40) {
          this.sendInput(this.mouseReport(carry < 0 ? 64 : 65, col, row, false, event));
          carry -= Math.sign(carry) * 40;
        }
        if (Math.abs(event.deltaY) >= 100 || event.deltaMode === 1) carry = 0;
      }, { passive: false });
      // A swipe goes to an app that reads the mouse as wheel reports.
      let lastY = null;
      let swipe = 0;
      let cell = { col: 1, row: 1 };
      this.root.addEventListener('touchstart', (event) => {
        lastY = event.touches.length === 1 ? event.touches[0].clientY : null;
        if (lastY !== null) cell = this.cellAt(event.touches[0].clientX, lastY);
        swipe = 0;
      }, { passive: true });
      this.root.addEventListener('touchmove', (event) => {
        if (!tracking() || lastY === null || event.touches.length !== 1) return;
        event.preventDefault();
        const y = event.touches[0].clientY;
        swipe += y - lastY;
        lastY = y;
        // One report per row of finger travel: an app scrolls a few rows
        // for each, so the content keeps up with the finger.
        const step = Math.max(8, this.cellH);
        while (Math.abs(swipe) >= step) {
          this.sendInput(this.mouseReport(swipe > 0 ? 64 : 65, cell.col, cell.row, false, null));
          swipe -= Math.sign(swipe) * step;
        }
      }, { passive: false });
      // A click without a selection types into the terminal; links open.
      // The pieces of one wrapped URL light up together.
      // A hyperlink an application marked (OSC 8) is one link too, over
      // however many rows and spans it takes.
      const groupOf = (node) => {
        // Claude Code marks each row of a URL as its own hyperlink (one per
        // row, all to the same URL): the URL is the link.
        const link = node?.closest?.('[data-link]');
        const target = link && this.links.get(Number(link.dataset.link));
        if (target) return 'U' + target;
        const piece = node?.closest?.('.iw-url');
        return piece ? 'U' + piece.dataset.url : null;
      };
      const hot = (event, on) => {
        const next = on ? groupOf(event.target) : groupOf(event.relatedTarget);
        if (next === this.hotGroup) return;
        this.hotGroup = next;
        this.lightLink();
      };
      this.root.addEventListener('mouseover', (event) => hot(event, true));
      this.root.addEventListener('mouseout', (event) => hot(event, false));
      this.root.addEventListener('click', (event) => {
        const url = this.linkAt(event);
        if (url) { this.options.onLink?.(url, event); return; }
        if (!window.getSelection()?.toString()) this.focus();
      });
      // Selecting text leaves the focus on the terminal itself: focusing the
      // input would drop the selection. Copying keeps it; any other key goes
      // to the input as if typed there (the browser puts the key's text into
      // the element focused during keydown), so typing never goes nowhere.
      this.root.addEventListener('keydown', (event) => {
        if (event.target !== this.root || event.isComposing) return;
        if (/^(Control|Shift|Alt|Meta|OS|CapsLock|NumLock|ScrollLock)$/.test(event.key)) return;
        const copy = (event.ctrlKey || event.metaKey) && !event.altKey && (event.key.toLowerCase() === 'c' || event.key === 'Insert');
        if (copy && (event.shiftKey || event.metaKey || this.selectionText())) return; // the copy handler takes it
        this.focus();
        this.keydown(event);
      });
    }

    // The URL under a click: an OSC 8 link, or a URL in the text.
    linkAt(event) {
      // An application's hyperlink first: its row shows only part of it.
      const linked = event.target.closest?.('[data-link]');
      if (linked && this.links.has(Number(linked.dataset.link))) return this.links.get(Number(linked.dataset.link));
      const url = event.target.closest?.('.iw-url');
      if (url) return url.dataset.url;
      const pos = document.caretPositionFromPoint?.(event.clientX, event.clientY);
      const range = pos ? { node: pos.offsetNode, offset: pos.offset } : (() => {
        const r = document.caretRangeFromPoint?.(event.clientX, event.clientY);
        return r ? { node: r.startContainer, offset: r.startOffset } : null;
      })();
      if (!range || !range.node) return null;
      const block = (range.node.nodeType === 1 ? range.node : range.node.parentElement)?.closest('.iw-row, .iw-line');
      if (!block) return null;
      let offset = 0;
      const walker = document.createTreeWalker(block, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) {
        if (n === range.node) { offset += range.offset; break; }
        offset += n.textContent.length;
      }
      const text = block.textContent;
      for (const m of findURLs(text)) {
        if (offset >= m.index && offset <= m.index + m.url.length) return m.url;
      }
      return null;
    }

    // ------------------------------------------------------------ text

    // The history shown and the screen, as logical lines.
    textLines() {
      const out = [];
      if (!this.alt) for (const line of this.histEl.children) out.push(line.textContent.replace(/\s+$/, ''));
      for (const row of this.grid) {
        const text = row.cells.filter((c) => c.w !== 0).map((c) => c.t || ' ').join('').replace(/\s+$/, '');
        if (row.cont && out.length) out[out.length - 1] += text; else out.push(text);
      }
      while (out.length && out[out.length - 1] === '') out.pop();
      return out;
    }

    cursorRow() { return this.cursor.y; }

    dispose() {
      if (this.frame !== null) cancelAnimationFrame(this.frame);
      document.removeEventListener('selectionchange', this.onSelection);
      this.root.remove();
    }
  }

  function trimCells(cells) {
    let end = cells.length;
    while (end > 0 && cells[end - 1].s === 0 && cells[end - 1].t === ' ') end--;
    return cells.slice(0, end);
  }

  const paletteNames = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white',
    'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite'];

  // A screen.Color as CSS: '' for the default.
  function color(c, theme) {
    const kind = Math.floor(c / 0x1000000);
    const v = c % 0x1000000;
    if (kind === 2) return '#' + v.toString(16).padStart(6, '0');
    if (kind !== 1) return '';
    if (v < 16) return theme[paletteNames[v]];
    if (v < 232) {
      const n = v - 16;
      const level = (x) => (x ? 55 + 40 * x : 0);
      return 'rgb(' + level(Math.floor(n / 36)) + ',' + level(Math.floor(n / 6) % 6) + ',' + level(n % 6) + ')';
    }
    const g = 8 + 10 * (v - 232);
    return 'rgb(' + g + ',' + g + ',' + g + ')';
  }

  // URLs in the text look like links (a click with Ctrl or ⌘, or a tap,
  // opens them: see linkAt).
  const findURLs = (text) => window.IwatoLinks ? window.IwatoLinks.findURLs(text) : [];
  const touchOnly = typeof matchMedia === 'function' && matchMedia('(hover: none)').matches;
  const urlTitle = () => touchOnly ? '' : t('term.linkTitle', { key: /Mac/.test(navigator.platform || '') ? '⌘' : 'Ctrl' });

  // Wraps text offsets [from, to) of el in link spans (one per text node).
  function wrapText(el, from, to, url, group) {
    const nodes = [];
    let at = 0;
    const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) { nodes.push({ n, at }); at += n.length; }
    for (const { n, at: start } of nodes.reverse()) {
      const a = Math.max(from, start) - start;
      const b = Math.min(to, start + n.length) - start;
      if (a >= b) continue;
      if (b < n.length) n.splitText(b);
      const piece = a > 0 ? n.splitText(a) : n;
      const link = document.createElement('span');
      link.className = 'iw-url';
      link.dataset.url = url;
      if (group) link.dataset.group = String(group);
      if (urlTitle()) link.title = urlTitle();
      piece.replaceWith(link);
      link.append(piece);
    }
  }

  // Underlines the URLs in elements that hold one logical line (screen
  // rows joined by wrapping, or a history line's segments), also where a
  // URL crosses from one to the next. Each piece keeps the whole URL.
  function decorateURLs(els) {
    for (const el of els) {
      for (const u of [...el.querySelectorAll('.iw-url')]) u.replaceWith(...u.childNodes);
      el.normalize();
    }
    const nodes = [];
    let text = '';
    for (const el of els) {
      const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
      for (let n = walker.nextNode(); n; n = walker.nextNode()) { nodes.push({ n, at: text.length }); text += n.data; }
    }
    if (!text.includes('://')) return;
    const matches = findURLs(text).map((m) => ({ url: m.url, start: m.index, end: m.index + m.url.length }));
    // Backwards, so that splitting a text node keeps the offsets before it.
    for (const m of matches.reverse()) {
      for (const { n, at } of [...nodes].reverse()) {
        const from = Math.max(m.start, at) - at;
        const to = Math.min(m.end, at + n.length) - at;
        if (from >= to) continue;
        if (to < n.length) n.splitText(to);
        const piece = from > 0 ? n.splitText(from) : n;
        const link = document.createElement('span');
        link.className = 'iw-url';
        link.dataset.url = m.url;
        if (urlTitle()) link.title = urlTitle();
        piece.replaceWith(link);
        link.append(piece);
      }
    }
  }

  // The bytes a key sends, or null when the browser or the text input
  // handles it (printable characters come through the input event).
  function keySequence(event, modes) {
    const key = event.key;
    if (event.metaKey) return null;
    const mod = (event.shiftKey ? 1 : 0) + (event.altKey ? 2 : 0) + (event.ctrlKey ? 4 : 0);
    const cursorKeys = { ArrowUp: 'A', ArrowDown: 'B', ArrowRight: 'C', ArrowLeft: 'D', Home: 'H', End: 'F' };
    if (cursorKeys[key]) {
      if (mod) return '\x1b[1;' + (mod + 1) + cursorKeys[key];
      return (modes.appCursor ? '\x1bO' : '\x1b[') + cursorKeys[key];
    }
    const tilde = { Insert: 2, Delete: 3, PageUp: 5, PageDown: 6, F5: 15, F6: 17, F7: 18, F8: 19, F9: 20, F10: 21, F11: 23, F12: 24 };
    if (tilde[key]) return '\x1b[' + tilde[key] + (mod ? ';' + (mod + 1) : '') + '~';
    const fkeys = { F1: 'P', F2: 'Q', F3: 'R', F4: 'S' };
    if (fkeys[key]) return mod ? '\x1b[1;' + (mod + 1) + fkeys[key] : '\x1bO' + fkeys[key];
    switch (key) {
      case 'Enter': return event.altKey ? '\x1b\r' : '\r';
      case 'Backspace': return event.ctrlKey ? '\x08' : event.altKey ? '\x1b\x7f' : '\x7f';
      case 'Tab': return event.shiftKey ? '\x1b[Z' : event.ctrlKey ? null : '\t';
      case 'Escape': return '\x1b';
    }
    if (key.length !== 1) return null;
    if (event.ctrlKey && !event.altKey) {
      const c = key.toLowerCase();
      if (c >= 'a' && c <= 'z') return String.fromCharCode(c.charCodeAt(0) - 96);
      const special = { '@': '\x00', ' ': '\x00', 2: '\x00', '[': '\x1b', 3: '\x1b', '\\': '\x1c', 4: '\x1c', ']': '\x1d', 5: '\x1d', '^': '\x1e', 6: '\x1e', '_': '\x1f', '-': '\x1f', 7: '\x1f', '?': '\x7f', 8: '\x7f', '/': '\x1f' };
      return special[key] ?? null;
    }
    if (event.altKey && !event.ctrlKey) {
      // Option on a Mac types characters; elsewhere Alt is Meta.
      if (/Mac/.test(navigator.platform || '')) return null;
      return '\x1b' + key;
    }
    return null;
  }

  window.IwatoTerminal = IwatoTerminal;
  window.IwatoTerminal.keySequence = keySequence;
  window.IwatoTerminal.decodeCells = decodeCells;
  window.IwatoTerminal.preeditLayout = preeditLayout;
})();
