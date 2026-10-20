// The editor tab (エディタ): open a text file of the sandbox's workspace (the
// SFTP root: the home with the standard image), edit it, save it
// (editor.go). Files open as tabs; Ctrl+P (⌘P) opens one by a fuzzy match
// on the files under the chosen folder (remembered per sandbox), Ctrl+S
// (⌘S) saves; phones get the same as buttons. A tree of the folder (on the
// left on a PC, a drawer on a phone) reads one directory at a time as it is
// expanded, and shows the open file. A save names the version it
// was edited from: when the file changed meanwhile the server refuses it,
// and the page offers to reload or to overwrite. The text is CodeMirror 6
// (web/vendor/codemirror.js, built by scripts/build-codemirror.sh, loaded
// when the tab first shows): highlighting by the file's name, search,
// bracket matching, an undo history per open file. Without it (the bundle
// does not load, or no DOM in the unit tests) a textarea does the same
// minus those. An image (known by its name, or a file the server says is
// not text) opens as a read-only preview: an img of the raw route, which
// serves only images told by their content, under a sandboxing CSP.
(() => {
  'use strict';
  // The UI's strings (web/i18n.js), looked up when used.
  const t = (key, params) => window.IwatoI18n.t(key, params);

  // fuzzy matches query against a path as VS Code's quick open does, loosely:
  // the query's characters in order (spaces ignored, any case), scored
  // higher when they are consecutive, start a name or a word, or fall in the
  // file's name. null when they are not all there. positions are target's
  // indexes matched (to mark them).
  function fuzzy(query, target) {
    const q = String(query).replace(/\s+/g, '').toLowerCase();
    if (!q) return { score: 0, positions: [] };
    const t = target.toLowerCase();
    const base = target.lastIndexOf('/') + 1;
    const boundary = (j) => j === 0 || '/._- '.includes(target[j - 1]) || (target[j] !== t[j] && target[j - 1] === t[j - 1]);
    const from = (start) => {
      const positions = [];
      let score = 0;
      let last = -2;
      let i = start;
      for (let k = 0; k < q.length; k++) {
        const j = t.indexOf(q[k], i);
        if (j < 0) return null;
        score += 1 + (j === last + 1 ? 5 : 0) + (boundary(j) ? 4 : 0) + (j >= base ? 2 : 0);
        positions.push(j);
        last = j;
        i = j + 1;
      }
      return { score, positions };
    };
    let best = from(0);
    if (!best) return null;
    // Greedy from the start can take a poor first character: try a few
    // starts where a name or a word begins with it, the file's name first.
    const starts = [base];
    for (let j = t.indexOf(q[0]); j >= 0 && starts.length < 8; j = t.indexOf(q[0], j + 1)) if (j !== base && boundary(j)) starts.push(j);
    for (const s of starts) {
      if (t[s] !== q[0]) continue;
      const m = from(s);
      if (m && m.score > best.score) best = m;
    }
    best.score -= target.length / 1000;
    return best;
  }

  // hidden tells a path with a component that starts with a dot (.config/x,
  // a/.env): rg --files --hidden lists the home's many dotfiles, so they
  // rank below the rest.
  const hidden = (path) => path[0] === '.' || path.includes('/.');

  // asksHidden tells whether the query asks for one of path's hidden
  // components: a dot in the query followed by the start of one of them
  // (.env, .git/c, cfg .zsh, or a lone dot), not an extension like .js.
  function asksHidden(query, path) {
    const names = path.toLowerCase().split('/').filter((c) => c[0] === '.');
    if (!names.length) return false;
    for (const m of String(query).toLowerCase().matchAll(/\.([^./\s]*)/g)) if (names.some((c) => c.startsWith('.' + m[1]))) return true;
    return false;
  }

  // A hidden path's score loses this much unless the query asks for it:
  // more than a comparable match differs by, so the other file wins, while
  // hidden files still come up when nothing else matches.
  const hiddenPenalty = 10;
  const maxTabs = 5; // open tabs; the saved ones used longest ago close first

  // rank returns the best matches of query among paths (at most limit);
  // with no query, recent first, then the others in order with hidden
  // paths last.
  function rank(query, paths, recent = [], limit = 50) {
    if (!String(query).trim()) {
      const set = new Set(paths);
      const out = recent.filter((p) => set.has(p)).slice(0, limit);
      const seen = new Set(out);
      for (const later of [false, true]) {
        for (const p of paths) {
          if (out.length >= limit) break;
          if (hidden(p) === later && !seen.has(p)) { seen.add(p); out.push(p); }
        }
      }
      return out.map((path) => ({ path, positions: [] }));
    }
    const found = [];
    for (const path of paths) {
      const m = fuzzy(query, path);
      if (!m) continue;
      const score = hidden(path) && !asksHidden(query, path) ? m.score - hiddenPenalty : m.score;
      found.push({ path, score, positions: m.positions });
    }
    found.sort((a, b) => b.score - a.score || a.path.length - b.path.length || (a.path < b.path ? -1 : 1));
    return found.slice(0, limit);
  }

  // folderOf normalizes a folder typed in: relative to the workspace, no
  // leading ./ or ~/, no trailing slash ('' is the workspace).
  function folderOf(value) {
    let v = String(value || '').trim().replace(/\/+$/, '');
    v = v.replace(/^~\/?/, '').replace(/^(\.\/)+/, '');
    return v === '.' ? '' : v;
  }

  // lines splits a file's text by its line ending: a textarea turns CRLF
  // and a lone CR into LF, so the page edits LF text and saves it back with
  // the file's ending (an unchanged file saves byte for byte). A file that
  // mixes endings, or has a lone CR, is not opened: saving would change it.
  function lines(content) {
    if (!content.includes('\r')) return { eol: '\n', text: content };
    const rest = content.replace(/\r\n/g, '');
    if (rest.includes('\r') || rest.includes('\n')) return null;
    return { eol: '\r\n', text: content.replace(/\r\n/g, '\n') };
  }
  const mixedEnding = () => t('editor.mixedEnding');

  const mac = () => typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent || '');
  const coarse = () => typeof window !== 'undefined' && !!window.matchMedia?.('(pointer: coarse)').matches;
  const baseName = (p) => p.slice(p.lastIndexOf('/') + 1);
  const clock = () => window.IwatoI18n.time(Date.now(), { hour: '2-digit', minute: '2-digit' });
  const dirKey = (name) => 'iwato-editor-dir:' + name;
  const treeKey = (name) => 'iwato-editor-tree:' + name;
  const narrow = () => typeof window !== 'undefined' && !!window.matchMedia?.('(max-width: 720px)').matches;
  const join = (dir, name) => dir ? dir + '/' + name : name;
  // imageName: a name the preview opens without asking for text first (the
  // server decides by the content: editor.go editorImageType).
  const imageName = (p) => /\.(png|jpe?g|gif|webp|avif|svg|bmp|ico)$/i.test(p);
  const sizeText = (n) => n < 1024 ? n + ' B' : n < 1048576 ? (n / 1024).toFixed(1) + ' KB' : (n / 1048576).toFixed(1) + ' MB';
  // The tree's expanded folders per sandbox: in the page, and in the
  // browser's storage when it has one.
  const expandedMemory = new Map();
  const maxExpanded = 300;

  // treeEntries checks a directory listing from the server (editor.go):
  // names without a slash, the kinds it knows, at most 2000.
  function treeEntries(data) {
    const kinds = new Set(['dir', 'file', 'symlink', 'other']);
    const entries = (Array.isArray(data?.entries) ? data.entries : [])
      .filter((e) => e && typeof e.name === 'string' && e.name && e.name !== '.' && e.name !== '..' && !e.name.includes('/') && kinds.has(e.kind))
      .slice(0, 2000)
      .map((e) => ({ name: e.name, kind: e.kind, to: e.to === 'dir' || e.to === 'file' ? e.to : '', size: Number.isFinite(e.size) ? e.size : 0, heavy: e.heavy === true }));
    const total = Number.isFinite(data?.total) ? Math.max(data.total, entries.length) : entries.length;
    return { entries, total, truncated: data?.truncated === true };
  }

  // ancestors are the folders between dir (the tree's root) and the file
  // path, outermost first; null when path is not under dir.
  function ancestors(dir, path) {
    if (dir && !path.startsWith(dir + '/')) return null;
    const parts = (dir ? path.slice(dir.length + 1) : path).split('/');
    parts.pop();
    const out = [];
    let cur = dir;
    for (const part of parts) out.push(cur = join(cur, part));
    return out;
  }

  // languageOf names the highlighting for a path (a key of the bundle's
  // languages), '' for plain text.
  function languageOf(path) {
    const name = baseName(String(path || '')).toLowerCase();
    if (name === 'dockerfile' || name.startsWith('dockerfile.') || name.endsWith('.dockerfile') || name === 'containerfile') return 'dockerfile';
    if (['.bashrc', '.bash_profile', '.profile', '.zshrc', '.zprofile', '.envrc'].includes(name)) return 'shell';
    if (['gemfile', 'rakefile', 'guardfile', 'podfile', 'vagrantfile'].includes(name)) return 'ruby';
    if (name === 'go.mod' || name === 'go.sum' || name === 'go.work') return '';
    if (name === 'cargo.lock' || name === 'uv.lock' || name === 'poetry.lock' || name === 'pipfile') return 'toml';
    const ext = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1) : '';
    const byExt = {
      js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'jsx', ts: 'typescript', mts: 'typescript', cts: 'typescript', tsx: 'tsx',
      json: 'json', jsonc: 'json', webmanifest: 'json', html: 'html', htm: 'html', svg: 'html', xml: 'html', vue: 'html', css: 'css',
      md: 'markdown', markdown: 'markdown', py: 'python', pyi: 'python', go: 'go', rs: 'rust', yaml: 'yaml', yml: 'yaml',
      sh: 'shell', bash: 'shell', zsh: 'shell', sql: 'sql', rb: 'ruby', rake: 'ruby', gemspec: 'ruby', toml: 'toml'
    };
    return byExt[ext] || '';
  }

  // tabText is what Tab inserts: a tab in Go and Makefiles, two spaces
  // elsewhere.
  const tabText = (path) => /(\.go|Makefile|\.mk)$/.test(path || '') ? '\t' : '  ';

  // The CodeMirror bundle (web/vendor/codemirror.js), loaded once by the
  // first panel shown: the module, or null when it cannot be loaded.
  let codeMirror = null;
  function loadCodeMirror() {
    if (codeMirror) return codeMirror;
    if (typeof window !== 'undefined' && window.IwatoCodeMirror) return (codeMirror = Promise.resolve(window.IwatoCodeMirror));
    if (typeof document === 'undefined' || !document.createElement || !document.head) return (codeMirror = Promise.resolve(null));
    codeMirror = new Promise((resolve) => {
      const script = document.createElement('script');
      script.src = '/vendor/codemirror.js';
      script.addEventListener('load', () => resolve(window.IwatoCodeMirror || null));
      script.addEventListener('error', () => resolve(null));
      document.head.append(script);
    });
    return codeMirror;
  }

  // The search panel's and the folding's words (CodeMirror's phrases).
  const phrases = () => ({
    'Find': t('editor.cm.find'), 'Replace': t('editor.cm.replace'), 'next': t('editor.cm.next'), 'previous': t('editor.cm.previous'),
    'all': t('editor.cm.all'), 'match case': t('editor.cm.matchCase'), 'regexp': t('editor.cm.regexp'), 'by word': t('editor.cm.byWord'),
    'replace': t('editor.cm.replaceOne'), 'replace all': t('editor.cm.replaceAll'), 'close': t('common.close'),
    'Go to line': t('editor.cm.gotoLine'), 'go': t('editor.cm.go'), 'Fold line': t('editor.cm.fold'), 'Unfold line': t('editor.cm.unfold'),
    'folded code': t('editor.cm.folded')
  });

  // PlainText is the textarea the text falls back to. Tab indents (see
  // tabText) and Enter keeps the line's indent, as typed text (undo works).
  // Shift+Tab leaves the text area, so the keyboard can always get out.
  class PlainText {
    constructor(element, onEdit) {
      this.dom = element('textarea', 'editor-text');
      this.dom.spellcheck = false;
      for (const [k, v] of [['autocapitalize', 'off'], ['autocomplete', 'off'], ['autocorrect', 'off'], ['wrap', 'off'], ['aria-label', t('editor.content')]]) this.dom.setAttribute(k, v);
      this.onEdit = onEdit;
      this.dom.addEventListener('input', () => onEdit());
      this.dom.addEventListener('keydown', (event) => this.key(event));
    }
    get value() { return this.dom.value; }
    differs(content) { return this.dom.value !== content; }
    show(doc) {
      this.doc = doc;
      this.dom.value = doc.value;
      this.dom.setSelectionRange?.(doc.start, doc.end);
      this.dom.scrollTop = doc.scroll;
    }
    stash(doc) {
      doc.value = this.dom.value;
      doc.start = this.dom.selectionStart;
      doc.end = this.dom.selectionEnd;
      doc.scroll = this.dom.scrollTop;
    }
    clear() { this.doc = null; this.dom.value = ''; }
    focus() { this.dom.focus(); }
    refresh() {}
    key(event) {
      if (event.isComposing || event.ctrlKey || event.metaKey || event.altKey) return;
      const t = this.dom;
      let insert = null;
      if (event.key === 'Tab' && !event.shiftKey) {
        insert = tabText(this.doc?.path);
      } else if (event.key === 'Enter' && !event.shiftKey) {
        const before = t.value.slice(0, t.selectionStart);
        insert = '\n' + (before.slice(before.lastIndexOf('\n') + 1).match(/^[ \t]*/) || [''])[0];
        if (insert === '\n') return;
      } else {
        return;
      }
      event.preventDefault();
      if (!(typeof document !== 'undefined' && document.execCommand?.('insertText', false, insert))) {
        t.setRangeText(insert, t.selectionStart, t.selectionEnd, 'end');
        this.onEdit();
      }
    }
  }

  // The editor's colours and font: the page's tokens (styles.css), the
  // terminal's font. Made once.
  let themeMade = null;
  function codeTheme(CM) {
    if (!themeMade) themeMade = makeTheme(CM);
    return themeMade;
  }
  function makeTheme(CM) {
    return CM.EditorView.theme({
      '&': { color: 'var(--text)', backgroundColor: 'var(--terminal)', fontSize: '13px' },
      '&.cm-focused': { outline: 'none' },
      '.cm-scroller': { fontFamily: '"Moralerspace Neon HW",ui-monospace,SFMono-Regular,Consolas,monospace', lineHeight: '1.55' },
      '.cm-content': { caretColor: 'var(--green)', padding: '10px 0' },
      '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--green)' },
      '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': { backgroundColor: 'var(--code-selection)' },
      '.cm-activeLine': { backgroundColor: 'var(--code-active-line)' },
      '.cm-activeLineGutter': { backgroundColor: 'var(--code-active-line)', color: 'var(--text)' },
      '.cm-gutters': { backgroundColor: 'var(--terminal)', color: 'var(--code-gutter)', border: 'none', borderRight: '1px solid var(--border)' },
      '.cm-foldPlaceholder': { backgroundColor: 'var(--panel)', border: '1px solid var(--border)', color: 'var(--muted)' },
      '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': { backgroundColor: 'var(--code-bracket)', outline: '1px solid var(--code-bracket-border)' },
      '.cm-nonmatchingBracket, &.cm-focused .cm-nonmatchingBracket': { backgroundColor: 'var(--code-bad-bracket)' },
      '.cm-searchMatch': { backgroundColor: 'var(--code-match)', outline: '1px solid var(--code-match-border)' },
      '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--code-match-current)' },
      '.cm-selectionMatch': { backgroundColor: 'var(--code-selection-match)' },
      '.cm-panels': { backgroundColor: 'var(--panel)', color: 'var(--text)', fontFamily: 'var(--font)' },
      '.cm-panels.cm-panels-top': { borderBottom: '1px solid var(--border)' },
      '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border)' },
      '.cm-panel.cm-search, .cm-panel.cm-gotoLine': { padding: '6px 8px', fontSize: '12px' },
      '.cm-textfield': { backgroundColor: '#101418', color: 'var(--text)', border: '1px solid #39434a', borderRadius: '5px', padding: '3px 6px', font: '12px ui-monospace,SFMono-Regular,Consolas,monospace' },
      '.cm-button': { backgroundImage: 'none', backgroundColor: '#1d2229', color: '#c9d2df', border: '1px solid #343b46', borderRadius: '5px', padding: '3px 9px' },
      '.cm-button:active': { backgroundImage: 'none', backgroundColor: '#2a313a' },
      '.cm-panel.cm-search [name=close]': { color: 'var(--muted)' },
      '.cm-tooltip': { backgroundColor: 'var(--panel)', color: 'var(--text)', border: '1px solid var(--border)' }
    }, { dark: true });
  }

  // codeExtensions are what every CodeMirror text has: the basic setup
  // (line numbers, search, folding, history), wrapped lines, the words, the
  // highlighting of language (a key of the bundle's languages) and the theme.
  function codeExtensions(CM, { language = '', label = '', className = '' } = {}) {
    return [
      CM.basicSetup,
      CM.EditorState.phrases.of(phrases()),
      CM.EditorView.editorAttributes.of({ class: className }),
      CM.EditorView.contentAttributes.of({ spellcheck: 'false', autocorrect: 'off', autocapitalize: 'off', 'aria-label': label }),
      CM.EditorView.lineWrapping,
      CM.language(language),
      codeTheme(CM)
    ];
  }

  // CodeText is CodeMirror's view: one per panel, one state (text,
  // selection, undo history) per open file.
  class CodeText {
    constructor(CM, onEdit) {
      this.CM = CM;
      this.onEdit = onEdit;
      this.view = new CM.EditorView({ state: this.state('', '') });
      this.dom = this.view.dom;
    }
    state(text, path, selection) {
      const CM = this.CM;
      const tab = tabText(path);
      // Tab: indents a selection of lines, else inserts tabText.
      const indent = (view) => {
        const sel = view.state.selection;
        if (sel.ranges.some((r) => view.state.doc.lineAt(r.from).number !== view.state.doc.lineAt(r.to).number)) return CM.indentMore(view);
        view.dispatch(view.state.replaceSelection(tab), { scrollIntoView: true, userEvent: 'input' });
        return true;
      };
      return CM.EditorState.create({
        doc: text,
        selection,
        extensions: [
          CM.keymap.of([{ key: 'Tab', run: indent }]),
          CM.indentUnit.of(tab),
          CM.EditorState.tabSize.of(4),
          codeExtensions(CM, { language: languageOf(path), label: t('editor.content'), className: 'editor-text' }),
          CM.EditorView.updateListener.of((u) => { if (u.docChanged) this.onEdit(); })
        ]
      });
    }
    get value() { return this.view.state.doc.toString(); }
    differs(content) { return this.view.state.doc.length !== content.length || this.value !== content; }
    show(doc) {
      if (!doc.state) {
        const len = doc.value.length;
        doc.state = this.state(doc.value, doc.path, this.CM.EditorSelection.single(Math.min(doc.start || 0, len), Math.min(doc.end || 0, len)));
      }
      this.view.setState(doc.state);
      if (doc.snapshot) this.view.dispatch({ effects: doc.snapshot });
      else this.view.scrollDOM.scrollTop = doc.scroll || 0;
    }
    stash(doc) {
      doc.state = this.view.state;
      doc.value = this.view.state.doc.toString();
      doc.snapshot = this.view.scrollSnapshot?.();
      doc.scroll = this.view.scrollDOM.scrollTop;
    }
    clear() { this.view.setState(this.state('', '')); }
    focus() { this.view.focus(); }
    // refresh measures again once the panel shows (it was display:none).
    refresh() { this.view.requestMeasure(); }
  }

  // TextPane is the panel's text: CodeMirror once the bundle is there,
  // else the textarea. Hidden until a text file shows.
  class TextPane {
    constructor(element, onEdit) {
      this.element = element;
      this.onEdit = onEdit;
      this.box = element('div', 'editor-text-box');
      this.box.hidden = true;
      this.impl = null;
    }
    // ready makes the editor (once): CodeMirror when it loads.
    ready() {
      if (!this.loading) {
        this.loading = loadCodeMirror().then((CM) => {
          try {
            this.impl = CM ? new CodeText(CM, this.onEdit) : null;
          } catch (error) {
            if (typeof console !== 'undefined') console.error('editor: CodeMirror failed', error);
            this.impl = null;
          }
          if (!this.impl) this.impl = new PlainText(this.element, this.onEdit);
          this.box.append(this.impl.dom);
          return this.impl;
        });
      }
      return this.loading;
    }
    get hidden() { return this.box.hidden; }
    set hidden(v) { this.box.hidden = v; }
    get value() { return this.impl ? this.impl.value : ''; }
    get dom() { return this.impl?.dom; }
    differs(content) { return this.impl ? this.impl.differs(content) : content !== ''; }
    show(doc) { this.impl?.show(doc); }
    stash(doc) { this.impl?.stash(doc); }
    clear() { this.impl?.clear(); }
    focus() { this.impl ? this.impl.focus() : this.box.focus?.(); }
    refresh() { this.impl?.refresh(); }
  }

  // mount puts a code editor for a form (the YAML boxes) into container
  // (or nowhere: the caller places .dom): CodeMirror with the editor tab's
  // setup and colours, a textarea until the bundle loads or when it cannot.
  // onChange gets the text after each edit. Returns { dom, value,
  // setValue, focus, refresh (after being hidden or moved), destroy }.
  function mount(container, { value = '', language = '', readOnly = false, onChange = null, label = '', className = '' } = {}) {
    const box = document.createElement('div');
    box.className = 'code-field' + (readOnly ? ' code-readonly' : '') + (className ? ' ' + className : '');
    let view = null;
    let area = null;
    let gone = false;
    const changed = () => { if (onChange) onChange(handle.value); };
    const viewOf = (CM, text, from, to, focused) => {
      const len = text.length;
      const extensions = [
        CM.indentUnit.of('  '),
        codeExtensions(CM, { language, label, className: 'code-text' }),
        CM.EditorView.updateListener.of((u) => { if (u.docChanged) changed(); })
      ];
      // Read-only text keeps the keyboard (to select, search, copy) but no
      // on-screen keyboard.
      if (readOnly) extensions.push(CM.EditorState.readOnly.of(true), CM.EditorView.contentAttributes.of({ inputmode: 'none' }));
      const v = new CM.EditorView({ state: CM.EditorState.create({ doc: text, selection: CM.EditorSelection.single(Math.min(from, len), Math.min(to, len)), extensions }) });
      box.replaceChildren(v.dom);
      if (focused) v.focus();
      return v;
    };
    const CMNow = typeof window !== 'undefined' ? window.IwatoCodeMirror : null;
    if (CMNow) {
      try { view = viewOf(CMNow, value, 0, 0, false); } catch (error) { view = null; }
    }
    if (!view) {
      area = document.createElement('textarea');
      area.className = 'text-input code-text';
      area.value = value;
      area.rows = 16;
      area.spellcheck = false;
      area.readOnly = readOnly;
      for (const [k, v] of [['autocapitalize', 'off'], ['autocomplete', 'off'], ['autocorrect', 'off'], ['wrap', 'off'], ['aria-label', label]]) area.setAttribute(k, v);
      area.addEventListener('input', changed);
      box.replaceChildren(area);
      if (!CMNow) {
        loadCodeMirror().then((CM) => {
          if (!CM || gone || !area) return;
          try {
            const focused = typeof document !== 'undefined' && document.activeElement === area;
            view = viewOf(CM, area.value, area.selectionStart || 0, area.selectionEnd || 0, focused);
            area = null;
          } catch (error) {
            if (typeof console !== 'undefined') console.error('editor: CodeMirror failed', error);
            view = null;
          }
        });
      }
    }
    const handle = {
      dom: box,
      get value() { return view ? view.state.doc.toString() : area.value; },
      setValue(text) {
        text = String(text);
        if (view) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
        else area.value = text;
      },
      focus() { view ? view.focus() : area.focus(); },
      refresh() { view?.requestMeasure(); },
      destroy() {
        gone = true;
        view?.destroy();
        box.remove();
      }
    };
    if (container) container.append(box);
    return handle;
  }

  // panel makes the EditorPanel class with the page's helpers.
  function panel({ element, button, panelHeader, api, askConfirm }) {
    return class EditorPanel {
      // selected tells whether the panel's sandbox is the one shown.
      constructor(sandbox, selected) {
        this.sandbox = sandbox;
        this.selected = selected || (() => true);
        this.docs = []; // {path, content (as saved), version, value, start, end, scroll}; an image: {path, image, img, width, height, size, fit}
        this.current = null;
        this.recent = [];
        this.lists = {}; // folder -> {files, truncated, via, at}
        this.matches = [];
        this.active = 0;
        this.seq = 0;
        // Without a folder saved, the folder is ~/work once the sandbox says
        // it has one (loadRoot).
        this.dirSaved = false;
        try {
          const saved = localStorage.getItem(dirKey(sandbox.name));
          this.dirSaved = saved !== null;
          this.dir = folderOf(saved || '');
        } catch (_) { this.dir = ''; }
        // The tree: folder (relative to the workspace, '' for it) ->
        // {entries, total, truncated, error, loading, stale}.
        this.treeDirs = {};
        this.expanded = new Set(expandedMemory.get(sandbox.name) || []);
        if (!expandedMemory.has(sandbox.name)) {
          try {
            const saved = JSON.parse(localStorage.getItem(treeKey(sandbox.name)) || '[]');
            if (Array.isArray(saved)) for (const p of saved.slice(0, maxExpanded)) if (typeof p === 'string') this.expanded.add(p);
          } catch (_) { /* storage unavailable */ }
        }
        this.treeOpen = !narrow();
        const key = mac() ? '⌘' : 'Ctrl+';
        this.root = element('section', 'side-panel editor-panel');
        this.root.setAttribute('aria-label', t('tab.editor'));
        this.root.hidden = true;
        this.openButton = button('button-subtle', t('editor.open'), () => this.openQuick());
        this.openButton.title = t('editor.open.title', { key });
        this.saveButton = button('button-primary', t('common.save'), () => this.save());
        this.saveButton.title = t('editor.save.title', { key });
        this.closeButton = button('button-subtle', t('common.close'), () => this.onClose?.());

        const folder = element('form', 'editor-folder');
        this.treeButton = button('button-subtle editor-tree-toggle', t('editor.tree'), () => this.toggleTree());
        this.treeButton.title = t('editor.tree.title');
        folder.append(this.treeButton, element('span', 'files-label', t('editor.folder')));
        this.dirInput = element('input', 'text-input editor-dir');
        this.dirInput.type = 'text';
        this.dirInput.placeholder = t('editor.folder.placeholder');
        this.dirInput.setAttribute('aria-label', t('editor.folder.label'));
        this.dirInput.autocomplete = 'off';
        this.dirInput.spellcheck = false;
        this.dirInput.setAttribute('autocapitalize', 'off');
        this.dirInput.value = this.dir;
        this.reloadList = button('button-subtle', t('editor.reloadList'), () => this.loadList(true));
        this.reloadList.title = t('editor.reloadList.title');
        folder.append(this.dirInput, this.reloadList);
        folder.addEventListener('submit', (event) => { event.preventDefault(); this.setDir(this.dirInput.value); });
        this.dirInput.addEventListener('change', () => this.setDir(this.dirInput.value));

        this.tabs = element('div', 'editor-tabs');
        this.tabs.setAttribute('role', 'tablist');
        this.tabs.setAttribute('aria-label', t('editor.tabs'));

        this.conflict = element('div', 'editor-conflict');
        this.conflict.setAttribute('role', 'alert');
        this.conflict.hidden = true;
        this.conflictText = element('span', 'editor-conflict-text');
        this.conflict.append(this.conflictText,
          button('button-subtle', t('editor.conflict.reload'), () => this.reload()),
          button('button-primary', t('editor.conflict.overwrite'), () => this.save(true)),
          button('button-subtle', t('common.cancel'), () => { this.conflict.hidden = true; this.focusText(); }));

        this.text = new TextPane(element, () => this.edited());
        // The image preview: fit to the view, or 100 % (a click or the
        // button), read only.
        this.image = element('div', 'editor-image');
        this.image.hidden = true;
        const imageHead = element('div', 'editor-image-head');
        this.imageName = element('span', 'editor-image-name');
        this.imageMeta = element('span', 'panel-meta editor-image-meta');
        this.imageFit = button('button-subtle editor-image-fit', '100 %', () => this.toggleFit());
        this.imageText = button('button-subtle', t('editor.image.asText'), () => this.openAsText());
        this.imageText.title = t('editor.image.asText.title');
        imageHead.append(this.imageName, this.imageMeta, this.imageFit, this.imageText, button('button-subtle', t('common.reload'), () => this.reload()));
        this.imageBox = element('div', 'editor-image-box');
        this.image.append(imageHead, this.imageBox);
        this.empty = element('div', 'editor-empty');
        this.empty.append(element('strong', '', t('editor.empty')),
          element('p', '', coarse() ? t('editor.empty.touch') : t('editor.empty.keys', { key })));

        this.status = element('p', 'panel-meta editor-status');
        this.status.setAttribute('role', 'status');
        // A sandbox whose files are not the home (its daemon has no SFTP
        // server): recreating it gives the home.
        this.rootNote = element('p', 'panel-meta editor-root-note');
        this.rootNote.hidden = true;

        this.quick = element('div', 'editor-quick');
        this.quick.setAttribute('role', 'dialog');
        this.quick.setAttribute('aria-label', t('editor.open'));
        this.quick.hidden = true;
        this.quickInput = element('input', 'text-input editor-quick-input');
        this.quickInput.type = 'text';
        this.quickInput.placeholder = t('editor.quick.placeholder');
        this.quickInput.autocomplete = 'off';
        this.quickInput.spellcheck = false;
        this.quickInput.setAttribute('autocapitalize', 'off');
        this.quickInput.setAttribute('role', 'combobox');
        this.quickInput.setAttribute('aria-label', t('editor.quick.label'));
        this.quickInput.setAttribute('aria-expanded', 'true');
        this.quickList = element('ul', 'editor-quick-list');
        this.quickList.setAttribute('role', 'listbox');
        this.quickList.id = 'editor-quick-' + Math.random().toString(36).slice(2);
        this.quickInput.setAttribute('aria-controls', this.quickList.id);
        this.quickMeta = element('p', 'panel-meta editor-quick-meta');
        this.quickMeta.setAttribute('role', 'status');
        const quickClose = button('button-subtle', t('common.close'), () => this.closeQuick());
        const quickHead = element('div', 'editor-quick-head');
        quickHead.append(this.quickInput, quickClose);
        this.quick.append(quickHead, this.quickMeta, this.quickList);
        this.quickInput.addEventListener('input', () => { this.active = 0; this.renderQuick(); });
        this.quickInput.addEventListener('keydown', (event) => this.quickKey(event));

        const body = element('div', 'editor-body');
        body.append(this.text.box, this.image, this.empty, this.quick);
        this.tree = element('nav', 'editor-tree');
        this.tree.setAttribute('aria-label', t('editor.tree.label'));
        this.tree.id = 'editor-tree-' + Math.random().toString(36).slice(2);
        this.treeButton.setAttribute('aria-controls', this.tree.id);
        const treeHead = element('div', 'editor-tree-head');
        this.treeTitle = element('span', 'editor-tree-title');
        const treeRefresh = button('button-subtle editor-tree-refresh', t('editor.tree.refresh'), () => this.refreshTree());
        treeRefresh.title = t('editor.tree.refresh.title');
        const treeClose = button('button-subtle editor-tree-close', t('common.close'), () => this.toggleTree(false));
        treeHead.append(this.treeTitle, treeRefresh, treeClose);
        this.treeList = element('ul', 'editor-tree-list');
        this.tree.append(treeHead, this.treeList);
        this.tree.addEventListener('keydown', (event) => {
          if (event.key === 'Escape' && narrow()) { event.preventDefault(); this.toggleTree(false); }
        });
        this.main = element('div', 'editor-main');
        this.main.append(this.tree, body);
        this.root.append(panelHeader({ key: 'tab.editor' }, this.openButton, this.saveButton, this.closeButton), folder, this.rootNote, this.tabs, this.conflict, this.main, this.status);

        // Ctrl+P / Ctrl+S only while this panel shows and has the keyboard
        // (or nothing has it): never from the terminal or another card.
        this.onKey = (event) => {
          if (this.root.hidden || !this.selected() || !(event.ctrlKey || event.metaKey) || event.altKey) return;
          const k = String(event.key || '').toLowerCase();
          if (k !== 'p' && k !== 's') return;
          const at = typeof document !== 'undefined' ? document.activeElement : null;
          if (at && at !== document.body && !this.root.contains(at)) return;
          event.preventDefault();
          event.stopPropagation?.();
          if (k === 'p') this.openQuick();
          else this.save();
        };
        if (typeof document !== 'undefined') document.addEventListener('keydown', this.onKey, true);
        this.render();
      }

      show() {
        this.root.hidden = false;
        this.text.ready();
        this.text.refresh();
        this.render();
        if (!coarse()) this.focusTarget().focus();
      }

      // focusTarget is what has the keyboard: the text, the image's
      // button, or the open button.
      focusTarget() { return this.current ? (this.current.image ? this.imageFit : this.text) : this.openButton; }

      hide() {
        this.stash();
        this.closeQuick(false);
        this.root.hidden = true;
      }

      // unsaved tells whether a file has changes not saved (the page asks
      // before it is left).
      unsaved() { return this.docs.some((d) => this.dirty(d)); }

      dirty(doc) {
        if (doc.image) return false;
        return doc === this.current && !this.text.hidden ? this.text.differs(doc.content) : doc.value !== doc.content;
      }

      focusText() { if (this.current && !coarse()) this.focusTarget().focus(); }

      // stash keeps the text, the selection and the scroll (CodeMirror:
      // the whole state, with its undo history) in the current document.
      stash() {
        const d = this.current;
        if (!d || this.text.hidden) return;
        this.text.stash(d);
      }

      setDir(value) {
        const dir = folderOf(value);
        this.dirInput.value = dir;
        if (dir === this.dir) return;
        this.dir = dir;
        try { localStorage.setItem(dirKey(this.sandbox.name), dir); } catch (_) { /* storage unavailable */ }
        this.status.textContent = t('editor.folder.status', { dir: dir || t('editor.home') });
        if (!this.quick.hidden) this.loadList(false);
        this.renderTree();
      }

      // toggleTree shows or hides the tree (on a phone, a drawer over the
      // text).
      toggleTree(open = !this.treeOpen) {
        this.treeOpen = open;
        if (open && this.current) this.reveal(this.current.path);
        this.renderTree();
        if (open && narrow()) this.treeList.querySelector?.('.editor-tree-item')?.focus();
        else if (!open && narrow()) this.treeButton.focus();
      }

      saveExpanded() {
        const list = [...this.expanded].slice(-maxExpanded);
        this.expanded = new Set(list);
        expandedMemory.set(this.sandbox.name, list);
        try { localStorage.setItem(treeKey(this.sandbox.name), JSON.stringify(list)); } catch (_) { /* storage unavailable */ }
      }

      // refreshTree reads the folders again (those shown now; the others
      // when they are expanded).
      refreshTree() {
        for (const [dir, d] of Object.entries(this.treeDirs)) {
          if (d.error) delete this.treeDirs[dir];
          else d.stale = true;
        }
        this.status.textContent = t('editor.tree.refreshing');
        this.treeRefreshing = true;
        this.renderTree();
      }

      // loadDir reads one folder of the tree.
      async loadDir(dir) {
        const d = this.treeDirs[dir] || (this.treeDirs[dir] = {});
        if (d.loading) return;
        d.loading = true;
        try {
          const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/dir?path=' + encodeURIComponent(dir), { timeoutMs: 70000 });
          Object.assign(d, treeEntries(data), { error: '', stale: false });
        } catch (error) {
          Object.assign(d, { error: error.message || t('editor.tree.failed'), stale: false });
        } finally {
          d.loading = false;
        }
        if (this.treeDirs[dir] !== d) return;
        if (this.treeRefreshing && !Object.values(this.treeDirs).some((x) => x.loading)) {
          this.treeRefreshing = false;
          if (this.status.textContent === t('editor.tree.refreshing')) this.status.textContent = t('editor.tree.refreshed');
        }
        this.renderTree();
      }

      // reveal expands the folders down to path (a file), unless one is a
      // heavy folder (node_modules and the like), and scrolls to it.
      reveal(path) {
        const dirs = ancestors(this.dir, path);
        if (!dirs) return;
        let changed = false;
        let parent = this.dir;
        for (const p of dirs) {
          const name = p.slice(p.lastIndexOf('/') + 1);
          if (this.treeDirs[parent]?.entries?.find((e) => e.name === name)?.heavy && !this.expanded.has(p)) return;
          if (!this.expanded.has(p)) { this.expanded.add(p); changed = true; }
          parent = p;
        }
        if (changed) this.saveExpanded();
        this.revealPath = path;
      }

      renderTree() {
        this.main.dataset.tree = this.treeOpen ? 'open' : 'closed';
        this.treeButton.setAttribute('aria-expanded', String(this.treeOpen));
        this.tree.hidden = !this.treeOpen;
        if (!this.treeOpen || this.root.hidden) return;
        this.treeTitle.textContent = this.dir || t('editor.home');
        this.treeTitle.title = this.dir || t('editor.home');
        const at = typeof document !== 'undefined' ? document.activeElement : null;
        const focused = at && this.treeList.contains(at) ? at.dataset?.path : undefined;
        const rows = [];
        this.treeRows(this.dir, 0, rows);
        this.treeList.replaceChildren(...rows);
        const find = (p) => rows.map((li) => li.children[0]).find((b) => b && b.dataset.path === p);
        if (focused !== undefined) find(focused)?.focus();
        if (this.revealPath) {
          const row = find(this.revealPath);
          if (row) { row.scrollIntoView?.({ block: 'nearest' }); this.revealPath = null; }
        }
      }

      treeNote(text, depth, retry) {
        const li = element('li', 'editor-tree-note', text);
        li.style.paddingLeft = (8 + depth * 14) + 'px';
        if (retry) li.append(button('button-subtle', t('common.reload'), retry));
        return li;
      }

      treeRows(dir, depth, rows) {
        const d = this.treeDirs[dir];
        if (!d || d.stale) this.loadDir(dir);
        const shown = this.treeDirs[dir];
        if (!shown.entries && !shown.error) { rows.push(this.treeNote(t('common.loading'), depth)); return; }
        if (shown.error) {
          rows.push(this.treeNote(shown.error, depth, () => { delete this.treeDirs[dir]; this.renderTree(); }));
          return;
        }
        if (!shown.entries.length) rows.push(this.treeNote(t('editor.tree.empty'), depth));
        for (const e of shown.entries) {
          const p = join(dir, e.name);
          rows.push(this.treeRow(e, p, depth));
          if (e.kind === 'dir' && this.expanded.has(p)) this.treeRows(p, depth + 1, rows);
        }
        if (shown.truncated) rows.push(this.treeNote(t('editor.tree.truncated', { shown: shown.entries.length, total: shown.total }), depth));
      }

      treeRow(e, p, depth) {
        const li = element('li', 'editor-tree-row');
        const row = element('button', 'editor-tree-item');
        row.type = 'button';
        row.dataset.path = p;
        row.dataset.kind = e.kind === 'symlink' ? 'link-' + (e.to || 'none') : e.kind;
        if (e.heavy) row.dataset.heavy = 'true';
        row.style.paddingLeft = (8 + depth * 14) + 'px';
        const open = this.expanded.has(p);
        const icon = e.kind === 'dir' ? (open ? '▾' : '▸') : e.kind === 'symlink' ? '↪' : '';
        row.append(element('span', 'editor-tree-icon', icon), element('span', 'editor-tree-name', e.name));
        let title = p;
        if (e.kind === 'dir') {
          row.setAttribute('aria-expanded', String(open));
          if (e.heavy) title += t('editor.tree.heavy');
          row.addEventListener('click', () => {
            if (this.expanded.has(p)) this.expanded.delete(p);
            else this.expanded.add(p);
            this.saveExpanded();
            this.renderTree();
          });
        } else if (e.kind === 'file' || (e.kind === 'symlink' && e.to === 'file')) {
          if (e.kind === 'symlink') title += t('editor.tree.link');
          if (this.current?.path === p) row.setAttribute('aria-current', 'true');
          row.addEventListener('click', () => {
            if (narrow()) this.toggleTree(false);
            this.open(p);
          });
        } else {
          row.disabled = true;
          title += e.kind === 'symlink' ? (e.to === 'dir' ? t('editor.tree.dirLink') : t('editor.tree.brokenLink')) : t('editor.tree.notFile');
        }
        row.title = title;
        li.append(row);
        return li;
      }

      full(rel) { return this.dir ? this.dir + '/' + rel : rel; }

      // loadList reads the folder's files (the server keeps them briefly;
      // fresh asks it to read them again).
      async loadList(fresh) {
        const dir = this.dir;
        const seq = ++this.seq;
        this.quickMeta.textContent = t('editor.list.loading');
        if (fresh) this.status.textContent = t('editor.list.loading');
        try {
          const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/files?dir=' + encodeURIComponent(dir) + (fresh ? '&refresh=1' : ''), { timeoutMs: 70000 });
          const files = (Array.isArray(data.files) ? data.files : []).filter((f) => typeof f === 'string').slice(0, 50000);
          this.lists[dir] = { files, truncated: data.truncated === true, via: data.via === 'sftp' ? 'sftp' : 'rg', at: Date.now() };
          if (fresh) this.status.textContent = t('editor.list.count', { count: files.length });
        } catch (error) {
          if (seq === this.seq) this.quickMeta.textContent = error.message;
          if (fresh) this.status.textContent = error.message;
          return;
        }
        if (seq === this.seq && dir === this.dir) this.renderQuick();
      }

      openQuick() {
        if (this.root.hidden) return;
        this.stash();
        this.quick.hidden = false;
        this.quickInput.value = '';
        this.active = 0;
        this.quickInput.focus();
        const list = this.lists[this.dir];
        if (list) this.renderQuick();
        else this.quickList.replaceChildren();
        if (!list || Date.now() - list.at > 30000) this.loadList(false);
      }

      closeQuick(focus = true) {
        if (this.quick.hidden) return;
        this.quick.hidden = true;
        if (focus) this.current && !coarse() ? this.focusTarget().focus() : this.openButton.focus();
      }

      renderQuick() {
        const list = this.lists[this.dir];
        if (!list) return;
        const recent = this.recent.filter((p) => !this.dir || p.startsWith(this.dir + '/')).map((p) => this.dir ? p.slice(this.dir.length + 1) : p);
        this.matches = rank(this.quickInput.value, list.files, recent, 50);
        if (this.active >= this.matches.length) this.active = Math.max(0, this.matches.length - 1);
        const where = (this.dir || t('editor.home')) + ' · ' + t(list.truncated ? 'editor.list.truncated' : 'editor.list.files', { count: list.files.length }) + (list.via === 'sftp' ? t('editor.list.sftp') : '');
        this.quickMeta.textContent = this.matches.length ? where : t('editor.list.none', { where });
        this.quickList.replaceChildren(...this.matches.map((m, i) => {
          const li = element('li', 'editor-quick-item');
          li.setAttribute('role', 'option');
          li.setAttribute('aria-selected', String(i === this.active));
          li.id = this.quickList.id + '-' + i;
          const marks = new Set(m.positions);
          let run = '';
          let marked = false;
          const flush = () => { if (run) li.append(marked ? element('mark', '', run) : element('span', '', run)); run = ''; };
          for (let j = 0; j < m.path.length; j++) {
            if (marks.has(j) !== marked) { flush(); marked = !marked; }
            run += m.path[j];
          }
          flush();
          li.addEventListener('click', () => this.pick(i));
          return li;
        }));
        if (this.matches.length) this.quickInput.setAttribute('aria-activedescendant', this.quickList.id + '-' + this.active);
        else this.quickInput.removeAttribute('aria-activedescendant');
      }

      quickKey(event) {
        const n = this.matches.length;
        if (event.key === 'Escape') {
          event.preventDefault();
          event.stopPropagation?.();
          this.closeQuick();
        } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
          event.preventDefault();
          if (!n) return;
          this.active = (this.active + (event.key === 'ArrowDown' ? 1 : n - 1)) % n;
          this.renderQuick();
          this.quickList.children[this.active]?.scrollIntoView?.({ block: 'nearest' });
        } else if (event.key === 'Enter' && !event.isComposing) {
          event.preventDefault();
          if (n) this.pick(this.active);
        }
      }

      pick(i) {
        const m = this.matches[i];
        if (!m) return;
        this.closeQuick(false);
        this.open(this.full(m.path));
      }

      // open opens path as a tab: an image by its name, else as text, and
      // as an image when the server says it is not text.
      async open(path) {
        const known = this.docs.find((d) => d.path === path);
        if (known) { this.switchTo(known); return; }
        this.status.textContent = t('editor.opening', { path });
        try {
          let doc;
          if (imageName(path)) doc = await this.openImage(path);
          else {
            try {
              doc = await this.openText(path);
            } catch (error) {
              if (error.status !== 415) throw error;
              doc = await this.openImage(path);
            }
          }
          const again = this.docs.find((d) => d.path === doc.path);
          if (again) { this.switchTo(again); return; }
          this.docs.push(doc);
          this.switchTo(doc);
          // Keep a few open: the saved ones used longest ago go.
          const unused = (d) => { const i = this.recent.indexOf(d.path); return i < 0 ? Infinity : i; };
          const saved = this.docs.filter((d) => d !== doc && !this.dirty(d)).sort((a, b) => unused(b) - unused(a));
          while (this.docs.length > maxTabs && saved.length) this.docs.splice(this.docs.indexOf(saved.shift()), 1);
          this.renderTabs();
          this.status.textContent = doc.path + (doc.image ? t('editor.image.readOnly') : '');
        } catch (error) {
          this.status.textContent = path + ': ' + error.message;
          if (!coarse()) this.focusTarget().focus();
        }
      }

      async openText(path) {
        const ready = this.text.ready();
        const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/file?path=' + encodeURIComponent(path), { timeoutMs: 70000 });
        const split = lines(String(data.content ?? ''));
        if (!split) throw new Error(mixedEnding());
        await ready;
        return { path: typeof data.path === 'string' ? data.path : path, eol: split.eol, content: split.text, version: String(data.version || ''), value: split.text, start: 0, end: 0, scroll: 0 };
      }

      rawURL(path) {
        this.rawSeq = (this.rawSeq || 0) + 1;
        return '/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/raw?path=' + encodeURIComponent(path) + '&n=' + this.rawSeq;
      }

      // loadImage shows the raw route in an img (never a blob: URL of this
      // origin, so that the image opened by itself keeps the route's
      // sandboxing CSP). When it does not load, the route says why.
      async loadImage(path) {
        const img = element('img', 'editor-image-img');
        img.alt = baseName(path);
        const url = this.rawURL(path);
        const loaded = await new Promise((resolve) => {
          img.addEventListener('load', () => resolve(true));
          img.addEventListener('error', () => resolve(false));
          img.src = url;
        });
        if (!loaded) {
          await api(url, { timeoutMs: 70000 });
          throw new Error(t('editor.image.failed'));
        }
        img.addEventListener('click', () => this.toggleFit());
        let size = 0;
        try {
          const entry = typeof performance !== 'undefined' ? performance.getEntriesByName?.(img.src)?.pop() : null;
          if (entry?.decodedBodySize > 0) size = entry.decodedBodySize;
        } catch (_) { /* no resource timing */ }
        if (!size) {
          const i = path.lastIndexOf('/');
          size = this.treeDirs[i < 0 ? '' : path.slice(0, i)]?.entries?.find((e) => e.name === baseName(path))?.size || 0;
        }
        return { img, width: img.naturalWidth || 0, height: img.naturalHeight || 0, size };
      }

      async openImage(path) {
        return { path, image: true, fit: true, ...await this.loadImage(path), content: '', value: '', version: '', eol: '\n', start: 0, end: 0, scroll: 0 };
      }

      showImage(doc) {
        this.imageBox.replaceChildren(doc.img);
        this.image.dataset.fit = doc.fit ? 'fit' : 'actual';
        this.imageName.textContent = baseName(doc.path);
        this.imageName.title = doc.path;
        this.imageMeta.textContent = [doc.width && doc.height ? doc.width + ' × ' + doc.height + ' px' : '', doc.size ? sizeText(doc.size) : ''].filter(Boolean).join(' · ');
        this.imageFit.textContent = doc.fit ? '100 %' : t('editor.image.fit');
        this.imageFit.title = doc.fit ? t('editor.image.actual.title') : t('editor.image.fit.title');
        this.imageText.hidden = !/\.svg$/i.test(doc.path);
        this.image.hidden = false;
      }

      toggleFit() {
        const doc = this.current;
        if (!doc?.image) return;
        doc.fit = !doc.fit;
        this.showImage(doc);
      }

      // openAsText opens an SVG shown as an image as text instead.
      async openAsText() {
        const doc = this.current;
        if (!doc?.image) return;
        try {
          const text = await this.openText(doc.path);
          const i = this.docs.indexOf(doc);
          if (i < 0) return;
          this.docs[i] = text;
          if (this.current === doc) { this.current = null; this.switchTo(text); } else this.render();
          this.status.textContent = t('editor.openedAsText', { path: doc.path });
        } catch (error) {
          this.status.textContent = doc.path + ': ' + error.message;
        }
      }

      switchTo(doc) {
        if (doc !== this.current) this.stash();
        this.current = doc;
        this.conflict.hidden = true;
        this.recent = [doc.path, ...this.recent.filter((p) => p !== doc.path)].slice(0, 30);
        this.reveal(doc.path);
        if (doc.image) {
          this.text.hidden = true;
          this.showImage(doc);
        } else {
          this.image.hidden = true;
          this.imageBox.replaceChildren();
          this.text.hidden = false;
          this.text.show(doc);
        }
        this.render();
        this.focusText();
      }

      async closeDoc(doc) {
        if (this.dirty(doc) && !await askConfirm(t('editor.close.confirm', { name: baseName(doc.path) }), { ok: t('common.close'), danger: true })) return;
        const i = this.docs.indexOf(doc);
        if (i < 0) return;
        this.docs.splice(i, 1);
        if (doc === this.current) {
          this.current = null;
          const next = this.docs[Math.min(i, this.docs.length - 1)];
          if (next) { this.switchTo(next); return; }
          this.text.hidden = true;
          this.text.clear();
          this.image.hidden = true;
          this.imageBox.replaceChildren();
          this.conflict.hidden = true;
        }
        this.render();
      }

      edited() {
        if (!this.current) return;
        const was = this.current.dirtyShown;
        if (this.dirty(this.current) !== was) this.renderTabs();
      }

      async save(force = false) {
        const doc = this.current;
        if (!doc || this.saving) return;
        if (doc.image) { this.status.textContent = t('editor.image.noSave'); return; }
        if (this.sandbox.status && this.sandbox.status !== 'running') { this.status.textContent = t('editor.save.stopped'); return; }
        const value = this.text.value;
        this.saving = true;
        this.saveButton.disabled = true;
        this.status.textContent = t('editor.saving');
        try {
          const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/file', {
            method: 'POST', timeoutMs: 70000, body: JSON.stringify({ path: doc.path, content: doc.eol === '\r\n' ? value.replace(/\n/g, '\r\n') : value, base: doc.version, force })
          });
          doc.content = value;
          doc.version = String(data.version || '');
          this.conflict.hidden = true;
          this.status.textContent = t('editor.saved', { path: doc.path, time: clock() });
          if (doc === this.current) this.focusText();
        } catch (error) {
          if (error.status === 409 && error.changed) {
            this.conflictText.textContent = t('editor.conflict', { name: baseName(doc.path) });
            this.conflict.hidden = false;
            this.status.textContent = t('editor.notSaved');
          } else {
            this.status.textContent = t('editor.saveFailed', { message: error.message });
          }
        } finally {
          this.saving = false;
          this.saveButton.disabled = !!this.current?.image;
          this.renderTabs();
        }
      }

      // reload takes the file as it is now, dropping this page's changes.
      async reload() {
        const doc = this.current;
        if (!doc) return;
        if (doc.image) {
          this.status.textContent = t('editor.loading', { path: doc.path });
          try {
            Object.assign(doc, await this.loadImage(doc.path));
            if (doc === this.current) this.showImage(doc);
            this.status.textContent = t('editor.reloaded', { path: doc.path });
          } catch (error) {
            this.status.textContent = t('editor.reloadFailed', { message: error.message });
          }
          return;
        }
        try {
          const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/file?path=' + encodeURIComponent(doc.path), { timeoutMs: 70000 });
          const split = lines(String(data.content ?? ''));
          if (!split) throw new Error(mixedEnding());
          doc.eol = split.eol;
          doc.content = doc.value = split.text;
          doc.version = String(data.version || '');
          doc.start = doc.end = doc.scroll = 0;
          doc.state = doc.snapshot = null;
          this.conflict.hidden = true;
          if (doc === this.current) { this.current = null; this.switchTo(doc); }
          this.status.textContent = t('editor.reloaded', { path: doc.path });
        } catch (error) {
          this.status.textContent = t('editor.reloadFailed', { message: error.message });
        }
      }

      render() {
        this.empty.hidden = !!this.current;
        this.saveButton.disabled = !this.current || !!this.current.image || !!this.saving;
        this.renderTabs();
        // Started again: read the folders that failed (stopped) again.
        if (this.sandbox.status === 'running' && this.treeStatus && this.treeStatus !== 'running') {
          for (const [dir, d] of Object.entries(this.treeDirs)) if (d.error) delete this.treeDirs[dir];
        }
        this.treeStatus = this.sandbox.status;
        if (this.sandbox.status === 'running' && !this.rootAsked && !this.root.hidden) this.loadRoot();
        else if (this.sandbox.status !== 'running') this.rootAsked = false;
        this.renderTree();
        const stopped = t('editor.stopped');
        if (this.sandbox.status && this.sandbox.status !== 'running') this.status.textContent = stopped;
        else if (this.status.textContent === stopped) this.status.textContent = '';
      }

      // loadRoot asks where the files are (once per run of the sandbox):
      // ~/work becomes the folder when none is saved, and a root that is
      // not the home gets a note.
      async loadRoot() {
        this.rootAsked = true;
        try {
          const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/root', { timeoutMs: 70000 });
          if (this.disposed) return;
          const home = data.home !== false;
          this.rootNote.hidden = home;
          this.rootNote.textContent = home ? '' : t('editor.rootNote', { path: String(data.path || '').slice(0, 300) });
          if (!this.dirSaved && !this.dir && data.work === true) {
            this.dir = 'work';
            this.dirInput.value = 'work';
            if (!this.quick.hidden) this.loadList(false);
            this.renderTree();
          }
        } catch (_) {
          this.rootAsked = false;
        }
      }

      renderTabs() {
        this.tabs.hidden = !this.docs.length;
        this.tabs.replaceChildren(...this.docs.map((doc) => {
          const dirty = this.dirty(doc);
          doc.dirtyShown = dirty;
          const tab = element('span', 'editor-tab');
          tab.setAttribute('aria-selected', String(doc === this.current));
          tab.dataset.dirty = String(dirty);
          const select = button('editor-tab-select', baseName(doc.path), () => this.switchTo(doc));
          select.setAttribute('role', 'tab');
          select.setAttribute('aria-selected', String(doc === this.current));
          select.title = doc.path + (dirty ? t('editor.unsaved.title') : '');
          if (dirty) {
            const mark = element('span', 'editor-dirty', '●');
            mark.setAttribute('aria-label', t('editor.unsaved'));
            select.append(mark);
          }
          const close = button('editor-tab-close', '×', () => this.closeDoc(doc));
          close.title = t('editor.closeTab', { path: doc.path });
          close.setAttribute('aria-label', close.title);
          tab.append(select, close);
          return tab;
        }));
        // The current tab in view (the row scrolls sideways).
        const shown = this.tabs.querySelector?.('.editor-tab[aria-selected="true"]');
        if (shown?.getBoundingClientRect && this.tabs.getBoundingClientRect) {
          const tab = shown.getBoundingClientRect(), row = this.tabs.getBoundingClientRect();
          if (tab.left < row.left) this.tabs.scrollLeft += tab.left - row.left;
          else if (tab.right > row.right) this.tabs.scrollLeft += tab.right - row.right;
        }
      }

      dispose() {
        this.disposed = true;
        if (typeof document !== 'undefined') document.removeEventListener?.('keydown', this.onKey, true);
        this.root.remove();
      }
    };
  }

  window.IwatoEditor = { fuzzy, rank, folderOf, lines, treeEntries, ancestors, imageName, languageOf, panel };
  window.IwatoCode = { mount };
})();
