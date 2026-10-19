'use strict';
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const root = require('node:path').join(process.cwd(), 'web') + '/';
const source = fs.readFileSync(root + 'app.js', 'utf8');
const bootstrap = fs.readFileSync(root + 'bootstrap.js', 'utf8');
const linksSource = fs.readFileSync(root + 'links.js', 'utf8');
const approveSource = fs.readFileSync(root + 'approve.js', 'utf8');
const filesSource = fs.readFileSync(root + 'files.js', 'utf8');
const ingressSource = fs.readFileSync(root + 'ingress.js', 'utf8');
const editorSource = fs.readFileSync(root + 'editor.js', 'utf8');
const i18nSource = fs.readFileSync(root + 'i18n.js', 'utf8');
const resetSource = fs.readFileSync(root + 'reset.js', 'utf8');
const sessionsSource = fs.readFileSync(root + 'sessions.js', 'utf8');
const refusalsSource = fs.readFileSync(root + 'refusals.js', 'utf8');
class Element {
  constructor(tag = 'div') {
    Object.assign(this, {tagName: tag, children: [], hidden: false, disabled: false, className: '', dataset: {}, style: {setProperty(key, value) { this[key] = value; }}, attributes: {}, listeners: {}, textWrites: 0, clientWidth: 520, clientHeight: 320, offsetHeight: 60});
    this.classList = {
      add: c => { this.className += ' ' + c; },
      remove: c => { this.className = this.className.split(' ').filter(v => v !== c).join(' '); },
      toggle: c => { const add = !this.className.split(' ').includes(c); add ? this.classList.add(c) : this.classList.remove(c); return add; }
    };
  }
  get textContent() { return this.text || ''; }
  set textContent(value) { this.text = value; this.textWrites++; }
  append(...nodes) { for (const node of nodes) { node.parent = this; this.children.push(node); } }
  replaceChildren(...nodes) { this.children = []; this.append(...nodes); }
  // As in a browser, placeholder and title reflect their attributes.
  setAttribute(key, value) { this.attributes[key] = String(value); if (key === 'placeholder' || key === 'title') this[key] = String(value); }
  getAttribute(key) { return this.attributes[key] ?? null; }
  removeAttribute(key) { delete this.attributes[key]; }
  addEventListener(key, callback) { this.listeners[key] = callback; }
  getBoundingClientRect() { return {left: 0, top: 0, width: 800, height: 480}; }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter(node => node !== this); }
  querySelector(selector) { return this.children.find(node => node.className.split(' ').includes(selector.slice(1))) || null; }
  focus() { this.focused = true; }
  contains(node) { for (; node; node = node.parent) if (node === this) return true; return false; }
  scrollIntoView() {}
  click() { const event = {prevented: false, preventDefault() { this.prevented = true; }}; if (!this.disabled) this.listeners.click?.(event); return event; }
  set innerHTML(_) { throw new Error('UNSAFE HTML WRITE'); }
}
const flush = async () => { for (let i = 0; i < 8; i++) await new Promise(setImmediate); };
const deferred = () => { let resolve; const promise = new Promise(done => { resolve = done; }); return {promise, resolve}; };
const response = (data, status = 200) => ({ok: status >= 200 && status < 300, status, json: async () => data});
async function environment({touch = false, fullscreen = true, noSandboxes = false, token = true, fake = true, status = 'not-created', unauthorized = false, savedTerm = null, search = '', terms = null, push = false, androidApp = false, languages = ['ja-JP']} = {}) {
  const ids = ['terminal-grid', 'notice-title', 'notice-text', 'notice', 'server-label', 'server-state', 'connection-count', 'empty-title', 'empty-text', 'empty-state', 'refresh-button', 'fake-banner', 'sandbox-nav', 'menu-button', 'sidebar', 'sidebar-actions', 'app-shell',
    'push-button', 'pair-button', 'login-panel', 'login-form', 'login-input', 'pair-panel', 'pair-url', 'pair-qr', 'pair-copy', 'pair-close',
    'create-button', 'create-panel', 'create-form', 'create-name', 'create-submit', 'create-close', 'create-status', 'create-image', 'create-persist-note', 'log-button', 'images-log', 'approve-scope', 'approve-scope-all', 'approve-scope-read', 'approve-scope-rules', 'approve-scope-readwrite', 'approve-scope-full', 'policy-form', 'policy-rule', 'policy-binaries', 'policy-any', 'policy-host', 'policy-ports', 'policy-audit', 'approve-rules', 'approve-add-rule', 'log-close', 'log-panel', 'log-text', 'images-banner', 'images-title', 'images-text', 'images-retry', 'images-recheck', 'server-build', 'menu-build', 'confirm-panel', 'confirm-text', 'confirm-ok', 'confirm-cancel', 'volume-button', 'volume-panel', 'volume-close', 'volume-status', 'volume-list', 'create-volumes',
    'provider-button', 'provider-panel', 'provider-close', 'provider-status', 'provider-list', 'provider-form', 'provider-profile', 'provider-note', 'provider-hint', 'provider-name', 'provider-values', 'provider-submit', 'provider-form-status', 'create-providers', 'create-presets', 'create-gpu', 'create-gpu-label', 'create-gpu-note', 'provider-oauth', 'provider-yaml', 'provider-profile-yaml', 'provider-custom-add',
    'settings-button', 'settings-panel', 'settings-body', 'settings-close', 'approve-form', 'approve-binary', 'approve-binary-chips', 'approve-binary-check', 'approve-access-read', 'approve-access-rules', 'approve-access-all', 'approve-access-audit', 'approve-blocked', 'approve-paths', 'approve-seen', 'approve-path-rows', 'approve-add-path', 'approve-path-suggest', 'approve-summary', 'approve-error', 'approve-head', 'approve-grant', 'approve-blocked-line', 'approve-writes', 'approve-more', 'approve-risks', 'approve-proposal', 'approve-proposal-body', 'policy-host-risk'];
  const els = Object.fromEntries(ids.map(id => [id, new Element()]));
  for (const id of ids) { const m = id.match(/^approve-(?:scope|access)-(\w+)$/); if (m) els[id].value = m[1]; }
  els['terminal-grid'].append(els['empty-state']); els.notice.hidden = true; els['fake-banner'].hidden = true;
  for (const id of ['push-button', 'pair-button', 'login-panel', 'pair-panel', 'create-button', 'create-panel', 'images-banner', 'images-retry', 'images-recheck', 'confirm-panel', 'volume-button', 'volume-panel', 'provider-button', 'provider-panel', 'create-providers', 'provider-oauth', 'provider-yaml', 'settings-button', 'settings-panel', 'create-presets']) els[id].hidden = true;
  const shared = []; let shareFailure = null;
  const requests = [], sockets = [], terminals = [], timeouts = new Map(), intervals = [], confirmations = [];
  let sequence = 0, confirmResult = false, confirmHandler = null, failStart = false, termHandler = terms ? () => response({terminals: terms, multi: true}) : null, sandboxHandler = null, pasteHandler = null, proposalHandler = null, startHandler = null, stopHandler = null, volumeHandler = null, providerHandler = null, filesHandler = null, ingressHandler = null, imagesHandler = null, resetHandler = null; const created = []; const copied = []; const swListeners = {};
  // The page asks in its own confirmation panel; answer it as the owner would.
  let confirmShown = false;
  Object.defineProperty(els['confirm-panel'], 'hidden', {get: () => !confirmShown, set: value => {
    const opened = !value && !confirmShown; confirmShown = !value;
    if (opened) queueMicrotask(() => { const text = els['confirm-text'].textContent; confirmations.push(text); const yes = confirmHandler ? confirmHandler(text) : confirmResult; els[yes ? 'confirm-ok' : 'confirm-cancel'].click(); });
  }});
  const sandbox = {name: 'api', status, detail: '<script>unsafe()</script>'};
  class Socket {
    static OPEN = 1; static CONNECTING = 0;
    constructor(url) { this.url = String(url); this.readyState = 0; this.bufferedAmount = 0; this.sent = []; sockets.push(this); }
    send(data) { this.sent.push(JSON.parse(data)); }
    close(code = 1006) { this.readyState = 3; this.onclose?.({code}); }
    open() { this.readyState = 1; this.onopen?.(); }
    receive(text) { const bytes = Buffer.from(text); this.onmessage?.({data: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength)}); }
  }
  // Stands in for web/term.js. Its protocol messages are JSON here (the
  // page sends them as they are), so the tests read what the page sent.
  class IwatoTerminal {
    static inputMessage(data) { return JSON.stringify({type: 'input', data}); }
    constructor(host, options) { this.host = host; this.options = options; this.cols = 80; this.rows = 24; this.output = ''; this.received = []; this.cellH = 16; terminals.push(this); }
    fit() { this.rows = win.fitRows?.(this.host) || 24; return {cols: this.cols, rows: this.rows}; }
    hello() { const {cols, rows} = this.fit(); return JSON.stringify({type: 'hello', cols, rows}); }
    resizeMessage(cols, rows) { return JSON.stringify({type: 'resize', cols, rows}); }
    // The server's frames; here the text the screen shows.
    receive(bytes) { this.received.push(bytes); this.output += Buffer.from(bytes).toString(); }
    textLines() { const lines = this.output.split(/\r?\n/); while (lines.length && lines.at(-1) === '') lines.pop(); return lines; }
    // Typing: the text goes through mapInput (Ctrl from the key bar), then to the page.
    input(text) { this.options.input(this.options.mapInput ? this.options.mapInput(text) : text); }
    paste(text) { this.options.input(text.replace(/\r?\n/g, '\r')); }
    cursorRow() { return this.cursorY ?? 0; }
    cursorMove() { this.options.onCursor?.(); }
    setFont(family) { this.options.fontFamily = family; }
    toBottom() { this.bottomed = (this.bottomed || 0) + 1; }
    focus() { this.focusCount = (this.focusCount || 0) + 1; } dispose() { this.disposed = true; }
  }
  const location = {hash: token ? '#token=' + 'a'.repeat(64) : '', pathname: '/', search, href: 'http://127.0.0.1:8765/', protocol: 'http:', reload() {}};
  const history = {replaceState(_a, _b, path) { location.hash = ''; location.search = path.includes('?') ? path.slice(path.indexOf('?')) : ''; location.href = 'http://127.0.0.1:8765' + path; }};
  // fitRows (set by a test) stands in for the layout: the rows that fit the terminal host.
  const win = {PushManager: push ? class {} : undefined, Notification: push ? class {} : undefined, IwatoTerminal, listeners: {}, opened: [], scrolled: [], innerHeight: 800, scrollBy(x, y) { this.scrolled.push(y); },
    visualViewport: {height: 800, scale: 1, offsetTop: 0, listeners: {}, addEventListener(n, fn) { this.listeners[n] = fn; }}, open(url) { this.opened.push(url); }, addEventListener(n, fn) { this.listeners[n] = fn; }, confirm() { throw Error('browser dialogs must not be used'); }, alert() { throw Error('browser dialogs must not be used'); }, prompt() { throw Error('browser dialogs must not be used'); }};
  const document = {getElementById: id => els[id], createElement: tag => new Element(tag), hidden: false, focused: true, hasFocus() { return this.focused; }, fullscreenEnabled: fullscreen, listeners: {}, addEventListener(n, fn) { this.listeners[n] = this.listeners[n] || fn; this.allListeners = this.allListeners || {}; (this.allListeners[n] = this.allListeners[n] || []).push(fn); }, removeEventListener(n, fn) { if (this.allListeners?.[n]) this.allListeners[n] = this.allListeners[n].filter((f) => f !== fn); }};
  // Only the shown terminal may be stored; secrets never are.
  const stored = new Map(savedTerm ? [['iwato-term:api', savedTerm]] : []);
  const storage = {setItem(key, value) { if (!(/^iwato-term:[a-z0-9-]+$/.test(key) && /^[a-z0-9-]+$/.test(value) || key === 'iwato-lang' && /^(ja|en)$/.test(value))) throw Error('Browser storage must hold no secrets: ' + key); stored.set(key, String(value)); }, getItem: key => stored.has(key) ? stored.get(key) : null, removeItem: key => { stored.delete(key); }};
  const context = vm.createContext({
    // push: a browser with Web Push, already subscribed.
    // androidApp: installed (standalone) on an Android phone; share() fails with shareFailure (an error name) when set.
    navigator: {languages, userAgent: androidApp ? 'Mozilla/5.0 (Linux; Android 15) Chrome/141' : 'Mozilla/5.0 (X11; Linux x86_64)', share: async (data) => { shared.push(data); if (shareFailure) throw Object.assign(new Error(shareFailure), {name: shareFailure}); }, serviceWorker: {addEventListener(n, fn) { swListeners[n] = fn; }, register: async () => ({pushManager: {getSubscription: async () => ({toJSON: () => ({endpoint: 'https://fcm.googleapis.com/fcm/send/x', keys: {p256dh: 'p', auth: 'a'}})})}})}, clipboard: {writeText: async (text) => { copied.push(text); }, readText: async () => { throw new Error('denied'); }}},
    window: win, document, location, history, URL, URLSearchParams, AbortController, ArrayBuffer, Uint8Array, Map, Set, Error, TypeError, console, localStorage: storage, sessionStorage: {getItem() { throw Error('sessionStorage must not be used'); }, setItem() { throw Error('sessionStorage must not be used'); }}, atob, TextDecoder, Blob, crypto: require('node:crypto').webcrypto,
    matchMedia: query => ({matches: (touch || androidApp) && query.includes('coarse') || androidApp && /hover: none|display-mode: standalone/.test(query)}), getComputedStyle: () => ({paddingTop: '12px', paddingBottom: '7px'}), ResizeObserver: class {observe() {} disconnect() {}}, WebSocket: Socket,
    setTimeout: (fn, ms) => { const id = ++sequence; timeouts.set(id, {fn, ms}); return id; }, clearTimeout: id => timeouts.delete(id),
    setInterval: (fn, ms) => { intervals.push({fn, ms}); return intervals.length; }, clearInterval: (id) => { if (intervals[id - 1]) intervals[id - 1].cleared = true; },
    requestAnimationFrame: fn => { queueMicrotask(fn); return 1; }, cancelAnimationFrame() {},
    fetch: async (path, opt) => {
      requests.push({path, ...opt});
      if (unauthorized) return response({error: 'no session'}, 401);
      if (path === '/api/session') return response({csrf: 'csrf123'});
      if (path === '/api/sandboxes' && opt?.method === 'POST') { created.push(JSON.parse(opt.body)); return response({ok: true}); }
      if (path === '/api/sandboxes') return sandboxHandler ? sandboxHandler() : response({fake, sandboxes: noSandboxes ? created.map(w => ({name: w.name, status: w.status || 'not-created', detail: ''})) : [{...sandbox}], server: {version: '0.3.0', commit: '98cb328-dirty', built: '2026-10-04T03:04:00Z'}});
      if (path.endsWith('/paste-image')) return pasteHandler ? pasteHandler(path, opt) : response({path: '/home/dev/.cache/iwato/paste/20261010T000000Z-0123abcd.png'});
      if (path === '/api/presets') return response({presets: [{id: 'packages', label: 'パッケージのダウンロード', note: 'npm など', source: 'builtin', hosts: ['registry.npmjs.org'], params: []}, {id: 'user:mirror', label: 'ミラーに書き込む', note: 'push', source: 'user', file: 'mirror.yaml', hosts: ['git.example.com'], params: [{name: 'repo', label: 'リポジトリ', placeholder: 'OWNER/REPO'}]}], errors: [{file: 'bad.yaml', error: 'field x not found'}, {file: 'odd.yaml', error: 'server text', code: 'preset_param_unknown', params: {rule: 'r', name: 'x'}}], folder: 'C:\\iwato\\presets'});
      if (/\/api\/sandboxes\/[^/]+\/presets$/.test(path)) return response({presets: [{id: 'packages', label: 'パッケージのダウンロード'}]});
      if (/\/presets\/(apply|remove)$/.test(path)) return response({ok: true, kept: path.endsWith('remove') ? ['allow_x_443'] : undefined});
      if (path.includes('/proposals') || path.includes('/policy/')) return proposalHandler ? proposalHandler(path, opt) : response({available: false, pending: [], recent: []});
      if (path.startsWith('/api/ingress') || path.endsWith('/ports')) return ingressHandler ? ingressHandler(path, opt) : response(path.endsWith('/ports') ? {overall: {state: 'off'}, node: {state: 'off'}, running: false, ports: []} : {enabled: false, owners: [], secret: {set: false, storage: 'file'}, status: {state: 'off'}, nodes: []});
      if (path.startsWith('/api/files')) return filesHandler ? filesHandler(path, opt) : response({keys: [], publicOrigin: '', localOrigin: 'http://127.0.0.1:8765', available: true});
      if (path.startsWith('/api/volumes')) return volumeHandler ? volumeHandler(path, opt) : response({volumes: []});
      if (path.startsWith('/api/provider') || path.startsWith('/api/oauth/') || path.includes('/providers/')) return providerHandler ? providerHandler(path, opt) : response({profiles: [], providers: []});
      if (path.startsWith('/api/log')) return path.includes('after=') ? response({lines: ['gateway: <b>ready</b>'], next: 3}) : response({lines: ['WSLc のドライバを起動しています…', 'building iwato-sandbox'], next: 2});
      if (path === '/api/push') return response(push ? {available: true, publicKey: 'B'.repeat(87)} : {available: false});
      if (path === '/api/push/subscribe') return response({ok: true});
      if (path === '/api/presence') return response({ok: true});
      if (path === '/api/pair') return response({url: 'http://127.0.0.1:8765/#token=' + 'b'.repeat(64), qr: 'data:image/png;base64,iVBORw0KGgo='});
      if (path.includes('/terminals')) return termHandler ? termHandler(path, opt) : response({terminals: ['main'], multi: false});
      if (path === '/api/reset') return resetHandler ? resetHandler(path, opt) : response({running: false});
      if (path === '/api/images' || path === '/api/images/update') return imagesHandler ? imagesHandler(path, opt) : response({available: false});
      if (path === '/api/checks') return response({checks: [{level: 'ok', name: 'docker', detail: 'docker_version_context', params: {version: '28.3.0', api: '1.51', context: 'colima'}}, {level: 'warn', name: 'buildx', detail: 'buildx_missing', hint: 'buildx_install'}, {level: 'ok', name: 'config', detail: 'config', params: {path: '/x/iwato.json', count: 1}}]});
      if (path.endsWith('/delete')) { sandbox.status = 'not-created'; return response({ok: true}); }
      if (path.endsWith('/recreate')) { sandbox.status = 'running'; return response({ok: true, failed: [{rule: 'allow_x_443', error: 'refused <b>'}]}); }
      if (path.endsWith('/stop')) { if (stopHandler) return stopHandler(path); sandbox.status = 'stopped'; return response({ok: true}); }
      if (path.endsWith('/start')) {
        const made = created.find((w) => path === '/api/sandboxes/' + w.name + '/start'); if (made) made.status = 'running';
        if (startHandler) return startHandler(path);
        if (failStart) return response({error: '<img src=x onerror=alert(2)> failed'}, 500);
        sandbox.status = 'running'; return response({ok: true});
      }
      throw Error('unexpected API ' + path);
    }
  });
  vm.runInContext(bootstrap, context); assert.equal(location.hash, ''); vm.runInContext(linksSource, context); vm.runInContext(approveSource, context); vm.runInContext(refusalsSource, context); vm.runInContext(filesSource, context); vm.runInContext(editorSource, context); vm.runInContext(ingressSource, context); vm.runInContext(resetSource, context); vm.runInContext(sessionsSource, context); vm.runInContext(i18nSource, context); vm.runInContext(source, context); await flush();
  const card = () => els['terminal-grid'].children.find(node => node.className.split(' ').includes('terminal-card'));
  return {
    shared, els, requests, sockets, stored, swListeners, terminals, timeouts, intervals, confirmations, sandbox, context, win, location, document, card, copied,
    // The card's actions in a fixed order, wherever they sit (the rare ones are in the ⋯ menu).
    buttons: () => { const found = {}; const walk = (n) => { if (n.dataset?.action) found[n.dataset.action] = n; n.children.forEach(walk); }; walk(card()); return ['start', 'stop', 'connect', 'disconnect', 'delete', 'update', 'reconnect', 'recreate'].map((k) => found[k]); },
    setShareFailure: value => { shareFailure = value; }, setFailStart: value => { failStart = value; }, setTermHandler: handler => { termHandler = handler; }, setConfirm: value => { confirmResult = value; }, setConfirmHandler: handler => { confirmHandler = handler; },
    setSandboxHandler: handler => { sandboxHandler = handler; }, setPasteHandler: handler => { pasteHandler = handler; }, setVolumeHandler: handler => { volumeHandler = handler; }, setProviderHandler: handler => { providerHandler = handler; }, setFilesHandler: handler => { filesHandler = handler; }, setIngressHandler: handler => { ingressHandler = handler; }, setImagesHandler: handler => { imagesHandler = handler; }, setResetHandler: handler => { resetHandler = handler; },
    setProposalHandler: handler => { proposalHandler = handler; }, setStartHandler: handler => { startHandler = handler; }, setStopHandler: handler => { stopHandler = handler; }, setUnauthorized: value => { unauthorized = value; },
    policy: () => card().children.find(node => node.className === 'info-panel policy-panel'),
    card: () => card(),
    side: name => card().children.find(node => node.className === 'side-panel ' + name),
    keys: () => card().children[6].children[0].children,
    tickPoll: async () => { intervals[0].fn(); await flush(); },
    tickReconnect: async () => { const entry = [...timeouts.entries()].find(([, timer]) => timer.ms < 31000); assert(entry, 'reconnect timer'); timeouts.delete(entry[0]); entry[1].fn(); await flush(); }
  };
}
const mutations = env => env.requests.filter(request => /\/proposals\/(approve|reject|scope)$/.test(request.path));
const proposalReads = env => env.requests.filter(request => request.path.endsWith('/proposals'));
(async () => {
  let tests = 0; const ok = name => { tests++; console.log('PASS ' + name); };
  const env = await environment();
  assert.equal(env.requests[0].path, '/api/session'); assert.equal(env.requests[0].method, 'POST'); assert.equal(JSON.parse(env.requests[0].body).token, 'a'.repeat(64));
  assert.equal(env.context.window.IwatoLaunch, undefined); assert.equal(env.els['fake-banner'].hidden, false); assert.equal(env.sockets.length, 0); assert.equal(env.requests.filter(r => r.path.endsWith('/start')).length, 0);
  assert.equal(mutations(env).length, 0); assert.equal(env.policy().children[1].children[0].children.length, 0, 'no proposals to decide');
  assert.match(env.els['server-build'].textContent, /^v0\.3\.0 · 98cb328-dirty\n2026-10-0[34] \d\d:\d4 ビルド$/); assert.equal(env.els['menu-build'].textContent, env.els['server-build'].textContent.replace('\n', ' · '));
  ok('fragment stripped; bootstrap consumed; no automatic start, attach or approval');
  assert.equal(env.card().children[0].children[0].children[1].children[0].textContent, 'api'); assert(env.card().children[1].textContent.includes('<script>'));
  ok('sandbox metadata remains literal text without HTML writes');
  let [start, , connect] = env.buttons(); assert.equal(connect.disabled, true); start.click(); await flush();
  assert.equal(env.requests.find(r => r.path.endsWith('/start')).headers['X-CSRF-Token'], 'csrf123'); assert.equal(start.disabled, true); assert.equal(mutations(env).length, 0);
  assert.equal(env.sockets.length, 1, 'the shown sandbox connects once it runs'); assert(env.sockets[0].url.includes('/ws/api?csrf=csrf123'));
  ok('explicit start sends CSRF and connects the shown sandbox; it approves nothing');
  env.sockets[0].open(); assert.deepEqual(env.sockets[0].sent[0], {type: 'hello', cols: 80, rows: 24}, 'the hello comes first and carries the size');
  assert.doesNotMatch(env.sockets[0].url, /[?&]v=/);
  env.sockets[0].receive('日本語 <script>alert(1)</script>\r\n'); assert.equal(env.terminals[0].output, '日本語 <script>alert(1)</script>\r\n');
  env.terminals[0].options.onClipboard('unasked'); await flush(); assert.equal(env.copied.length, 0); assert.equal(env.els['connection-count'].textContent, '1');
  ok('binary messages go only to the terminal; unprompted OSC52 ignored; connected count');
  { const [st, stop, conn, disc, del, , reconn] = env.buttons(); const menu = reconn.parent; const more = menu.parent.children.find((n) => n.attributes['aria-label'] === 'その他の操作');
    assert.equal(conn.hidden, true); assert.equal(st.hidden, true); assert.equal(reconn.hidden, false); assert.equal(disc, undefined, 'no disconnect item');
    assert.deepEqual([stop, del].map((b) => b.parent), [menu, menu]); assert.equal(menu.hidden, true);
    more.click(); assert.equal(menu.hidden, false); assert.equal(more.getAttribute('aria-expanded'), 'true');
    const keydowns = () => env.document.allListeners.keydown.length; const before = keydowns(); [...env.document.allListeners.keydown].forEach((fn) => fn({type: 'keydown', key: 'Escape'})); assert.equal(menu.hidden, true, 'Escape closes the menu'); assert.equal(keydowns(), before - 1, 'and stops listening'); }
  ok('while connected only ⋯ stays in view; reconnect, stop and delete are in its menu');
  const terminalSent = () => env.sockets[0].sent.filter((m) => m.type !== 'focus');
  env.terminals[0].input('echo 日本語\r'); assert.deepEqual(terminalSent(), [{type: 'hello', cols: 80, rows: 24}, {type: 'input', data: 'echo 日本語\r'}]);
  ok('the hello sizes the session; input follows as termsync messages');
  const sentBeforeFocus = terminalSent().length; env.win.listeners.blur(); env.win.listeners.focus(); await flush();
  env.document.hidden = false; env.document.listeners.visibilitychange(); await flush();
  assert.equal(terminalSent().length, sentBeforeFocus, 'focusing the window or showing the tab leaves the shared size alone (each width change repeats Claude Code\'s screen)');
  env.terminals[0].input('x'); env.terminals[0].input('y');
  assert.deepEqual(terminalSent().slice(sentBeforeFocus), [{type: 'resize', cols: 80, rows: 24}, {type: 'input', data: 'x'}, {type: 'input', data: 'y'}], 'typing after the window was left takes the size back first, once');
  ok('the browser being used takes the shared terminal size back');
  { const presence = () => env.requests.filter((r) => r.path === '/api/presence'); const reports = () => presence().map((r) => JSON.parse(r.body));
    const D = vm.runInContext('Date', env.context); const realNow = D.now; let clock = realNow.call(D); D.now = () => clock;
    const touch = (name) => env.document.allListeners[name].forEach((fn) => fn({type: name}));
    const [first] = reports(); assert.equal(reports().length, 1); assert.equal(first.active, true, 'a page shown at login is in use'); assert.match(first.page, /^[0-9a-f]{32}$/);
    touch('pointerdown'); touch('keydown'); clock += 29000; touch('paste'); assert.equal(reports().length, 1, 'touches within 30 s are not reported again');
    clock += 2000; touch('wheel'); assert.equal(reports().length, 2, 'a touch after 30 s is');
    env.document.hidden = true; env.document.listeners.visibilitychange(); assert.deepEqual(reports().at(-1), {page: first.page, active: false}, 'hidden: no longer in use, at once'); assert.equal(presence().at(-1).keepalive, true);
    env.document.listeners.visibilitychange(); touch('keydown'); assert.equal(reports().length, 3, 'reported once');
    env.document.hidden = false; env.document.listeners.visibilitychange(); await flush(); assert.deepEqual(reports().at(-1), {page: first.page, active: true}, 'shown again: in use at once');
    assert(presence().every((r) => r.method === 'POST' && r.headers['X-CSRF-Token'] === 'csrf123'));
    assert(env.sockets[0].sent.every((m) => m.type !== 'focus'), 'no per-terminal focus reports any more');
    D.now = realNow; }
  ok('the page reports whether the owner uses it (touched in 30 s steps, shown, hidden), so pushes wait until nobody does');
  env.sockets[0].close(1006); assert(env.card().children[5].children[2].children[1].textContent.includes('再接続')); assert.equal(env.card().children[5].children[2].hidden, false); await env.tickReconnect(); assert.equal(env.sockets.length, 2);
  env.sockets[1].open(); assert.equal(env.els['connection-count'].textContent, '1');
  ok('abnormal close reconnects');
  { const exp = await environment({status: 'running'}); const stale = exp.sockets.at(-1); assert(stale); exp.setUnauthorized(true); await exp.tickPoll(); stale.open(); assert.equal(stale.readyState, 3); assert.equal([...exp.timeouts.values()].filter(t => t.ms < 31000).length, 0); }
  ok('a session that expires while connecting invalidates late callbacks and cancels reconnect');
  connect.click(); const policy = env.sockets.at(-1); policy.open(); policy.close(1008); await flush(); assert.equal([...env.timeouts.values()].filter(t => t.ms < 31000).length, 0); assert.equal(env.card().children[7].hidden, false);
  ok('policy close stops reconnect and displays useful error');
  const reload = await environment({token: false, status: 'running', fake: false}); assert.equal(reload.requests[0].path, '/api/session'); assert.equal(reload.requests[0].method, undefined); assert.equal(reload.els['fake-banner'].hidden, true); assert(reload.requests.every(r => r.credentials === 'same-origin' && r.redirect === 'error' && r.cache === 'no-store'));
  ok('tokenless reload uses cookie session; all fetches same-origin without redirects or caching');
  const reject = await environment({unauthorized: true}); assert.equal(reject.sockets.length, 0); assert.equal(reject.els.notice.hidden, false); assert.equal(reject.els['server-state'].dataset.state, 'error');
  ok('unauthorized session shows recoverable state and never opens socket');
  const failed = await environment(); failed.setFailStart(true); failed.buttons()[0].click(); await flush(); assert.equal(failed.card().children[7].hidden, false); assert(failed.card().children[7].children[0].textContent.includes('<img src=x')); failed.card().children[7].children[1].listeners.click(); assert.equal(failed.card().children[7].hidden, true, 'the close button closes the error'); assert.equal(failed.buttons()[0].disabled, false);
  ok('start errors remain literal text; retry controls restored');

  const proposal = {id: 'a'.repeat(32), digest: 'd'.repeat(64), state: 'pending-approval', summary: '/usr/bin/git → github.com:443 <img src=x onerror=alert(1)>', createdAt: 1791036300};
  const proposalItem = {available: true, pending: [proposal, {id: 'bad', state: 'pending-approval', summary: 'x'}], recent: [{decision: 'rejected', host: 'evil.example', summary: '/usr/bin/curl → evil.example:443 <b>x</b>', at: 1}, {decision: 'approved', host: 'pypi.org', summary: '', at: 2}]};
  const policyRefresh = (env) => env.policy().children[0].children[0].children[1];
  const nodes = (n) => [n, ...n.children.flatMap(nodes)];
  const texts = (n) => nodes(n).map((x) => x.textContent).filter(Boolean).join('\n');
  const find = (n, label) => nodes(n).find((x) => x.tagName === 'button' && (x.textContent === label || x.attributes['aria-label'] === label));
  // The panel: 0 the header (0 the switch and refresh, 1 status, 2 error, 3 done), 1 the lists (0 waiting, 1 the policy, 2 history, 3 rejected), 2 the YAML.
  const pendingItems = (env) => env.policy().children[1].children[0].children.filter((n) => n.className === 'proposal-item');
  const approveFirst = async (env) => { find(pendingItems(env)[0], '承認').click(); await flush(); };
  const eg = await environment({fake: false}); eg.setProposalHandler((path) => /\/(approve|reject)$/.test(path) ? response({ok: true}) : response(proposalItem)); await eg.tickPoll();
  const egRow = () => pendingItems(eg)[0];
  assert.equal(pendingItems(eg).length, 1, 'malformed proposals are dropped'); assert.match(texts(egRow()), /<img/); assert.match(eg.policy().children[0].children[1].textContent, /ポリシーの提案が承認を待って/);
  find(egRow(), '承認').click(); await flush(); assert.equal(mutations(eg).length, 0, 'a cancelled confirmation sends nothing');
  assert.match(eg.confirmations.at(-1), /"api"/); assert.match(texts(eg.els['approve-head']), /github\.com:443/);
  eg.setConfirm(true); find(egRow(), '承認').click(); await flush();
  assert.equal(mutations(eg).length, 1); const approveReq = mutations(eg)[0]; assert.equal(approveReq.path, '/api/sandboxes/api/proposals/approve'); assert.equal(approveReq.method, 'POST');
  assert.deepEqual(JSON.parse(approveReq.body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64)}, 'the request ID and the digest shown: no permanent flag'); assert.equal(approveReq.headers['X-CSRF-Token'], 'csrf123');
  assert.equal(eg.policy().children[0].children[3].hidden, false); assert.match(eg.policy().children[0].children[3].textContent, /10 秒ほど/, 'an approval says it takes a moment to reach the sandbox');
  eg.card().children[2].children[0].children[1].click(); await flush(); assert(eg.intervals.some((i) => i.ms === 2000 && !i.cleared), 'the policy tab follows closely just after the owner acted');
  ok('the policy tab approves a proposal after a confirmation, posting its request ID and digest with CSRF');
  {
  // The approve dialog: the program (a glob from the candidates), the access
  // (reads only by default) and the paths seen, in one approval.
  const claude = '/sandbox/.claude/downloads/claude-2.1.293-linux-x64';
  const rawProposal = {...proposal, binaries: [claude, 7], endpoints: [{host: 'raw.githubusercontent.com', ports: [443, 'x'], protocol: 'rest'}, 'bad'],
    rules: [{method: 'GET', path: '/anthropics/claude-code/main/install.sh'}, {method: 'GET', path: '/anthropics/claude-code/main/CHANGELOG.md'}, {method: 'HEAD', path: '/anthropics/claude-code/main/install.sh'}]};
  const approveEnv = async (pending) => { const env = await environment({fake: false}); env.setProposalHandler((path) => /\/(approve|reject)$/.test(path) ? response({ok: true}) : response({...proposalItem, pending: [pending]})); await env.tickPoll(); return env; };
  const narrow = await approveEnv(rawProposal);
  const pathRows = () => narrow.els['approve-path-rows'].children;
  let shown = null;
  narrow.setConfirmHandler(() => {
    const e = narrow.els;
    shown = {read: e['approve-access-read'].checked, binary: e['approve-binary'].value, chips: e['approve-binary-chips'].children.map((c) => c.dataset.path), labels: e['approve-binary-chips'].children.map((c) => c.children[1].textContent),
      rows: pathRows().map((r) => r.input.value), seen: e['approve-seen'].textContent, summary: e['approve-summary'].children.map((c) => c.textContent), check: e['approve-binary-check'].textContent, form: e['approve-form'].hidden, access: e['approve-form'].dataset.access};
    e['approve-binary-chips'].children[1].click();
    shown.picked = [e['approve-binary'].value, e['approve-binary-chips'].children.map((c) => c.attributes['aria-pressed'])];
    // Segment clicks: install.sh -> * -> ** -> back.
    const segs = () => pathRows()[0].segs.children;
    assert.deepEqual(segs().map((b) => b.textContent), ['/anthropics', '/claude-code', '/main', '/install.sh']);
    segs()[3].click(); shown.star = pathRows()[0].input.value;
    segs()[3].click(); shown.twice = pathRows()[0].input.value;
    segs()[3].click(); shown.back = pathRows()[0].input.value;
    segs()[3].click();
    shown.after = e['approve-summary'].children.map((c) => c.textContent);
    return true;
  });
  await approveFirst(narrow);
  assert.equal(shown.form, false); assert.equal(shown.read, true, 'reads only by default'); assert.equal(shown.access, 'read'); assert.equal(shown.binary, '/sandbox/.claude/downloads/claude-*-linux-x64', 'versions as * by default');
  assert.deepEqual(shown.chips, [claude, '/sandbox/.claude/downloads/claude-*-linux-x64', '/sandbox/.claude/downloads/claude-*', '/sandbox/.claude/downloads/*', '/**']);
  assert.deepEqual(shown.labels, ['…/claude-2.1.293-linux-x64', '…/claude-*-linux-x64', '…/claude-*', '…/*', '/**'], 'the candidates are short');
  assert.deepEqual(shown.rows, ['/anthropics/claude-code/main/install.sh', '/anthropics/claude-code/main/CHANGELOG.md'], 'the paths seen, once each');
  assert.equal(shown.seen, '見えた要求から');
  assert.match(shown.check, /^✓ 提案したプログラムに当たります/);
  assert.deepEqual(shown.summary, ['raw.githubusercontent.com', '読み取りだけ (GET・HEAD・OPTIONS)', '/anthropics/claude-code/main/install.sh, /anthropics/claude-code/main/CHANGELOG.md', 'プログラム: /sandbox/.claude/downloads/claude-*-linux-x64'], 'the destination first, the program last');
  assert.deepEqual(shown.picked, ['/sandbox/.claude/downloads/claude-*-linux-x64', ['false', 'true', 'false', 'false', 'false']]);
  assert.equal(shown.star, '/anthropics/claude-code/main/*'); assert.equal(shown.twice, '/anthropics/claude-code/main/**'); assert.equal(shown.back, '/anthropics/claude-code/main/install.sh');
  assert.deepEqual(shown.after.slice(2), ['/anthropics/claude-code/main/*, /anthropics/claude-code/main/CHANGELOG.md', 'プログラム: /sandbox/.claude/downloads/claude-*-linux-x64'], 'the summary follows the edits');
  assert.deepEqual(JSON.parse(mutations(narrow)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64), scope: {binaries: ['/sandbox/.claude/downloads/claude-*-linux-x64'], readOnly: true,
    rules: [{method: '', path: '/anthropics/claude-code/main/*'}, {method: '', path: '/anthropics/claude-code/main/CHANGELOG.md'}]}}, 'the program glob, reads only, on the paths');
  assert.equal(narrow.els['approve-form'].hidden, true); assert.equal(narrow.els['confirm-ok'].disabled, false);
  ok('the approve dialog widens the program, keeps reads only and narrows the paths by segment, with a live summary');

  // Writes seen: methods and paths, as seen; an invalid program holds the approval back.
  const push = await approveEnv({...proposal, binaries: ['/usr/bin/git'], endpoints: [{host: 'github.com', ports: [443]}], rules: [{method: 'GET', path: '/o/r.git/info/refs'}, {method: 'POST', path: '/o/r.git/git-receive-pack'}]});
  let pushShown = null;
  push.setConfirmHandler(() => {
    const e = push.els;
    pushShown = {writes: [e['approve-writes'].hidden, texts(e['approve-writes'])], summary: [e['approve-summary'].dataset.write, e['approve-summary'].children[1].textContent], rules: e['approve-access-rules'].checked, chips: e['approve-binary-chips'].children.map((c) => c.dataset.path), rows: push.els['approve-path-rows'].children.map((r) => r.method.value + ' ' + r.input.value)};
    e['approve-binary'].value = 'git'; e['approve-binary'].listeners.input();
    pushShown.blocked = [e['confirm-ok'].disabled, e['approve-error'].hidden, e['approve-error'].textContent, e['approve-binary-check'].dataset.state];
    return true;
  });
  await approveFirst(push);
  assert.equal(pushShown.rules, true, 'writes seen: methods and paths');
  assert.deepEqual(pushShown.writes, [false, '書き込みを許可します\nPOST /o/r.git/git-receive-pack'], 'the writes spelled out'); assert.deepEqual(pushShown.summary, ['true', '書き込みも許可']);
  assert.deepEqual(pushShown.chips, ['/usr/bin/git', '/usr/bin/*', '/**'], 'no version, no version candidates');
  assert.deepEqual(pushShown.rows, ['GET /o/r.git/info/refs', 'POST /o/r.git/git-receive-pack']);
  assert.equal(pushShown.blocked[0], true); assert.equal(pushShown.blocked[1], false); assert.match(pushShown.blocked[2], /絶対パス/); assert.equal(pushShown.blocked[3], 'error');
  assert.equal(mutations(push).length, 0, 'not sent while the program is invalid'); assert.equal(push.els['confirm-panel'].hidden, false);
  push.els['approve-binary'].value = '/usr/bin/git'; push.els['approve-binary'].listeners.input();
  push.els['confirm-ok'].click(); await flush();
  assert.deepEqual(JSON.parse(mutations(push)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64), scope: {binaries: ['/usr/bin/git'], rules: [{method: 'GET', path: '/o/r.git/info/refs'}, {method: 'POST', path: '/o/r.git/git-receive-pack'}]}}, 'the program is always sent');
  ok('writes seen start with their methods and paths; an invalid program blocks the approval');

  // On a provider's host (ADR 0006): widening its scope posts only the
  // request ID to /scope; one it cannot widen approves as a rule, warned.
  const scoped = {...proposal, binaries: ['/usr/bin/git'], endpoints: [{host: 'github.com', ports: [443], protocol: 'rest'}], rules: [{method: 'POST', path: '/o/r.git/git-receive-pack'}],
    providerScope: {provider: 'gh-work', label: 'GitHub', repos: ['o/r', 7], mutations: []}};
  const scopeEnv = await environment({fake: false}); scopeEnv.setProposalHandler((path) => /\/(approve|reject|scope)$/.test(path) ? response({ok: true, closed: false}) : response({...proposalItem, pending: [scoped]})); await scopeEnv.tickPoll();
  const scopeRow = pendingItems(scopeEnv)[0];
  assert.match(texts(scopeRow), /gh-work \(GitHub\) の宛先 · 承認するとプロバイダーの範囲を広げます \(リポジトリ o\/r を追加\)/);
  let scopeForm = null;
  let scopeGrant = null;
  scopeEnv.setConfirmHandler(() => { scopeForm = scopeEnv.els['approve-form'].hidden; scopeGrant = [scopeEnv.els['approve-grant'].hidden, texts(scopeEnv.els['approve-grant']), scopeEnv.els['approve-grant'].children[1].dataset.write, texts(scopeEnv.els['approve-head'])]; return true; });
  find(scopeRow, '範囲を広げる').click(); await flush();
  assert.equal(scopeForm, true, 'no policy form'); assert.match(scopeEnv.confirmations.at(-1), /プロバイダーの範囲で許可しますか/);
  assert.equal(scopeGrant[0], false); assert.match(scopeGrant[1], /gh-work \(GitHub\) の範囲に加えるもの\n書き込み\n書き込めるリポジトリに o\/r を加えます/); assert.equal(scopeGrant[2], 'true', 'a repository is a write'); assert.match(scopeGrant[1], /要求: POST \/o\/r\.git\/git-receive-pack/); assert.match(scopeGrant[3], /^github\.com/);
  assert.equal(scopeEnv.els['approve-grant'].hidden, true, 'hidden after the dialog');
  assert.equal(mutations(scopeEnv)[0].path, '/api/sandboxes/api/proposals/scope'); assert.deepEqual(JSON.parse(mutations(scopeEnv)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64)});
  const ruleEnv = await approveEnv({...scoped, providerScope: {provider: 'gh-old', label: 'GitHub', reason: '範囲を設定できるようになる前に作った'}});
  ruleEnv.setConfirm(true); await approveFirst(ruleEnv);
  assert.match(texts(ruleEnv.els['approve-head']), /gh-old \(GitHub\) のトークンがこの通信に付きます\nプロバイダーの範囲にできない理由: 範囲を設定できるようになる前に作った/);
  assert.equal(mutations(ruleEnv)[0].path, '/api/sandboxes/api/proposals/approve');
  const waitEnv = await approveEnv({...scoped, providerScope: {...scoped.providerScope, resolving: true}});
  assert.equal(find(pendingItems(waitEnv)[0], '範囲を広げる').disabled, true, 'closing: no second decision'); assert.match(texts(pendingItems(waitEnv)[0]), /範囲を広げました/);
  ok('a proposal on a provider\'s host widens its scope, or approves as a rule with the token warning');

  // Nothing seen: an empty list (all paths) and the audit hint; audit approves to record.
  const seedEnv = await approveEnv({...proposal, binaries: ['/usr/bin/curl'], endpoints: [{host: 'api.example.com', ports: [443]}]});
  let seenText = '', seedRows = -1;
  seedEnv.setConfirmHandler(() => {
    const e = seedEnv.els; seenText = e['approve-seen'].textContent; seedRows = e['approve-path-rows'].children.length;
    e['approve-access-read'].checked = false; e['approve-access-audit'].checked = true; e['approve-access-audit'].listeners.change();
    return true;
  });
  await approveFirst(seedEnv);
  assert.equal(seedRows, 0); assert.equal(seenText, '見えた要求はありません · 空ならすべてのパス');
  assert.deepEqual(JSON.parse(mutations(seedEnv)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64), scope: {binaries: ['/usr/bin/curl'], audit: true}}, 'audit approves without blocking');
  // Not HTTP(S) by its port: as proposed, which sends nothing more.
  const ssh = await approveEnv({...proposal, binaries: ['/usr/bin/ssh'], endpoints: [{host: 'github.com', ports: [22]}]});
  ssh.setConfirmHandler(() => ssh.els['approve-access-all'].checked);
  await approveFirst(ssh);
  assert.deepEqual(JSON.parse(mutations(ssh)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64), scope: {binaries: ['/usr/bin/ssh']}});
  ok('no paths seen: all paths and a hint to audit; a non-HTTP port starts as proposed');

  // Choices OpenShell would refuse (an endpoint already in the policy, a
  // provider's, has other settings): disabled, with the server's reason;
  // forced, the approval is held back.
  const reason = '「監査」は選べません: api.github.com:443 には プロバイダー github-personal (GitHub) の規則 _provider_github_personal があり、設定が食い違います';
  const ghBlocked = await approveEnv({...proposal, binaries: ['/usr/bin/gh'], endpoints: [{host: 'api.github.com', ports: [443], protocol: 'rest'}], blocked: {audit: {reason}, bogus: {reason: 'x'}, read: {reason: 7}}});
  let blockedShown = null;
  ghBlocked.setConfirmHandler(() => {
    const e = ghBlocked.els;
    blockedShown = {disabled: ['read', 'rules', 'all', 'audit'].map((k) => e['approve-access-' + k].disabled), read: e['approve-access-read'].checked, note: e['approve-blocked'].textContent, noteHidden: e['approve-blocked'].hidden, line: e['approve-blocked-line'].hidden};
    e['approve-access-read'].checked = false; e['approve-access-audit'].checked = true; e['approve-access-audit'].listeners.change();
    blockedShown.forced = [e['confirm-ok'].disabled, e['approve-error'].textContent];
    e['approve-access-audit'].checked = false; e['approve-access-read'].checked = true; e['approve-access-read'].listeners.change();
    return true;
  });
  await approveFirst(ghBlocked);
  assert.deepEqual(blockedShown.disabled, [false, false, false, true], 'only the blocked choice is disabled (malformed ones dropped)');
  assert.equal(blockedShown.read, true); assert.equal(blockedShown.note, reason); assert.equal(blockedShown.noteHidden, false);
  assert.deepEqual(blockedShown.forced, [true, '「監査」は既存の規則と食い違うため選べません'], 'a blocked choice cannot be approved, with a one-line reason'); assert.equal(blockedShown.line, true, 'audit blocked: no line in the main view');
  assert.deepEqual(JSON.parse(mutations(ghBlocked)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64), scope: {binaries: ['/usr/bin/gh'], readOnly: true}});
  assert.equal(ghBlocked.els['approve-access-audit'].disabled, false, 'the radios are enabled again after the dialog');
  // The default blocked: the first choice that works.
  const auditDefault = await approveEnv({...proposal, binaries: ['/usr/bin/gh'], endpoints: [{host: 'api.github.com', ports: [443], protocol: 'rest'}], blocked: {read: {reason: 'r'}, rules: {reason: 'r'}}});
  let startedWith = '';
  auditDefault.setConfirmHandler(() => { startedWith = ['read', 'rules', 'all', 'audit'].find((k) => auditDefault.els['approve-access-' + k].checked); return false; });
  await approveFirst(auditDefault);
  assert.equal(startedWith, 'all');
  // A refusal from the gateway: in Japanese, its own words folded away.
  const refused = await environment({fake: false});
  refused.setProposalHandler((path) => /\/approve$/.test(path) ? response({error: 'OpenShell が拒否しました: api.github.com:443 で …', raw: "network endpoint ambiguity validation failed:; network policies '_provider_github' …"}, 409) : response({...proposalItem, pending: [{...proposal, endpoints: [{host: 'github.com', ports: [22]}]}]}));
  await refused.tickPoll();
  refused.setConfirm(true); await approveFirst(refused);
  const errorBox = refused.policy().children[0].children[2];
  assert.equal(errorBox.hidden, false); assert.match(errorBox.textContent, /^OpenShell が拒否しました/);
  const raw = errorBox.children.find((c) => c.className === 'error-raw');
  assert.ok(raw, 'the raw text is in a details toggle'); assert.equal(raw.children[0].textContent, 'OpenShell のメッセージ (原文)'); assert.match(raw.children[1].textContent, /^network endpoint ambiguity validation failed/);
  ok('approve choices OpenShell would refuse are disabled with the reason; a gateway refusal shows in Japanese with its raw text folded');

  // OpenShell's proposal whose only rule is a GET / nobody asked for (the
  // server's placeholder): no request seen, an empty list (all paths),
  // a hint to narrow; never a rule on exactly / unless typed.
  const mise = await approveEnv({...proposal, binaries: ['/usr/local/lib/mise/mise'], endpoints: [{host: 'api.github.com', ports: [443], protocol: 'rest', enforcement: 'enforce'}], rules: [], placeholder: true});
  let miseShown = null;
  mise.setConfirmHandler(() => {
    const e = mise.els;
    miseShown = {seen: e['approve-seen'].textContent, rows: e['approve-path-rows'].children.length, read: e['approve-access-read'].checked, summary: e['approve-summary'].children.map((c) => c.textContent),
      chips: e['approve-path-suggest'].children.map((c) => c.children.map((x) => x.textContent).join(' '))};
    e['approve-access-read'].checked = false; e['approve-access-all'].checked = true; e['approve-access-all'].listeners.change();
    miseShown.all = [e['approve-summary'].children.map((c) => c.textContent), e['approve-writes'].hidden];
    e['approve-access-all'].checked = false; e['approve-access-read'].checked = true; e['approve-access-read'].listeners.change();
    e['approve-path-suggest'].children[0].click();
    miseShown.hinted = e['approve-path-rows'].children.map((r) => r.method.value + ' ' + r.input.value);
    return true;
  });
  await approveFirst(mise);
  assert.equal(miseShown.seen, '見えた要求はありません · 空ならすべてのパス');
  assert.equal(miseShown.rows, 0, 'the list starts empty'); assert.equal(miseShown.read, true);
  assert.deepEqual(miseShown.summary.slice(1, 3), ['読み取りだけ (GET・HEAD・OPTIONS)', 'すべてのパス']);
  assert.deepEqual(miseShown.chips, ['例 /repos/*/*/**']);
  assert.deepEqual(miseShown.all, [['api.github.com', '提案どおり', 'GET / (ルートだけ)', 'プログラム: /usr/local/lib/mise/mise'], true], 'as proposed says it is the root only, no write');
  assert.deepEqual(miseShown.hinted, ['GET /repos/*/*/**']);
  assert.deepEqual(JSON.parse(mutations(mise)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64), scope: {binaries: ['/usr/local/lib/mise/mise'], readOnly: true, rules: [{method: '', path: '/repos/*/*/**'}]}});
  // Approved untouched: reads on all paths, no rule on /.
  const mise2 = await approveEnv({...proposal, binaries: ['/usr/local/lib/mise/mise'], endpoints: [{host: 'api.github.com', ports: [443], protocol: 'rest'}], rules: [], placeholder: true});
  mise2.setConfirm(true); await approveFirst(mise2);
  assert.deepEqual(JSON.parse(mutations(mise2)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64), scope: {binaries: ['/usr/local/lib/mise/mise'], readOnly: true}});
  const box2 = {window: {}, navigator: {languages: ['ja']}}; vm.runInNewContext(i18nSource, box2); vm.runInNewContext(approveSource, box2);
  assert.equal(box2.window.IwatoApprove.pathHint([{host: 'github.com'}]), '/*/*/**'); assert.equal(box2.window.IwatoApprove.pathHint([{host: 'example.com'}]), '');
  // As proposed on an endpoint whose access preset is full: the dialog marks the writes; the rule goes as proposed.
  const fullEnv = await approveEnv({...proposal, binaries: ['/usr/bin/gh'], endpoints: [{host: 'api.example.com', ports: [443], protocol: 'rest', enforcement: 'enforce', access: 'full'}], rules: [{method: 'GET', path: '/a'}]});
  let fullShown = null;
  fullEnv.setConfirmHandler(() => {
    const e = fullEnv.els;
    e['approve-access-read'].checked = false; e['approve-access-all'].checked = true; e['approve-access-all'].listeners.change();
    fullShown = [e['approve-writes'].hidden, texts(e['approve-writes']), e['approve-summary'].dataset.write, e['approve-form'].dataset.write];
    return true;
  });
  await approveFirst(fullEnv);
  assert.deepEqual(fullShown, [false, '書き込みを許可します\nすべてのメソッド (すべてのパス)', 'true', 'true'], 'access full as proposed is a write');
  assert.deepEqual(JSON.parse(mutations(fullEnv)[0].body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64), scope: {binaries: ['/usr/bin/gh']}}, 'as proposed, with its program (the server approves it unchanged)');
  ok('a placeholder GET / is no request seen: all paths, a hint to narrow, never exactly / unless typed');
  // Several programs, one of them every program: warned in the main view
  // with every program listed, and only the program field's is sent.
  const multi = await approveEnv({...proposal, binaries: ['/usr/bin/gh', '/**'], endpoints: [{host: 'api.github.com', ports: [443], protocol: 'rest', enforcement: 'enforce', allowEncodedSlash: true, rules: [{method: 'POST', path: ''}]}], rules: [{method: 'GET', path: '/a'}]});
  let multiShown = null;
  multi.setConfirmHandler(() => { const e = multi.els; multiShown = [e['approve-risks'].hidden, texts(e['approve-risks']), e['approve-binary'].value, texts(e['approve-proposal-body']), e['approve-proposal'].hidden]; return true; });
  await approveFirst(multi);
  assert.equal(multiShown[0], false, 'the warnings are in the main view');
  assert.match(multiShown[1], /提案は 2 個のプログラムを挙げています \(\/usr\/bin\/gh, \/\*\*\)。承認で許可するのは \/usr\/bin\/gh だけです/);
  assert.match(multiShown[1], /提案はすべてのプログラム \(\/\*\*\) を挙げています/); assert.match(multiShown[1], /api\.github\.com: allow_encoded_slash/);
  assert.equal(multiShown[2], '/usr/bin/gh'); assert.equal(multiShown[4], false);
  assert.match(multiShown[3], /プログラム: \/usr\/bin\/gh, \/\*\*/); assert.match(multiShown[3], /allow_encoded_slash/); assert.match(multiShown[3], /許可 POST \(パスの指定なし\)/, 'a path-less rule is shown');
  assert.deepEqual(JSON.parse(mutations(multi)[0].body).scope.binaries, ['/usr/bin/gh'], 'the program field only, never the proposal\'s');
  ok('a proposal naming several programs or every program warns of them in the main view, lists all under its details, and sends only the chosen program');
  // As proposed (a rejected one) with every program, a host gateway and
  // Iwato's own port: warned, and the port cannot be approved.
  {
    const gw = await environment({fake: false});
    const item = {...proposal, id: 'b'.repeat(32), state: 'rejected', binaries: ['/**'], endpoints: [{host: 'host.docker.internal', ports: [17600]}]};
    gw.setProposalHandler(() => response({...proposalItem, pending: [], rejected: [item], hostGateways: ['host.docker.internal'], listenPort: 17600}));
    await gw.tickPoll();
    let gwShown = null;
    gw.setConfirmHandler(() => { gwShown = [texts(gw.els['approve-risks']), gw.els['confirm-ok'].disabled]; return true; });
    const rejSec = gw.policy().children[1].children[3];
    find(rejSec, '承認する').click(); await flush();
    assert.match(gwShown[0], /すべてのプログラム \(\/\*\*\) に許可します/); assert.match(gwShown[0], /host\.docker\.internal は Iwato が動くマシンのループバックに届きます/);
    assert.match(gwShown[0], /ポート 17600 は Iwato 自身のポートです/); assert.match(gwShown[0], /書き込み: すべてのメソッドとパス/);
    assert.equal(gwShown[1], true, 'Iwato\'s port cannot be approved');
    assert.equal(mutations(gw).filter((r) => /approve$/.test(r.path)).length, 0);
  }
  ok('as proposed, every program, a host gateway and Iwato\'s port are warned, and Iwato\'s port is not approved');
  // A proposal that changed since it was listed: the server refuses with
  // proposal_changed, the page reads the list again and asks about the new
  // version, whose digest it sends.
  {
    let digest = 'd'.repeat(64);
    const ch = await environment({fake: false});
    ch.setProposalHandler((path, opt) => {
      if (/\/approve$/.test(path)) {
        if (JSON.parse(opt.body).digest !== digest) return response({error: 'changed', code: 'proposal_changed'}, 409);
        return response({ok: true});
      }
      return response({...proposalItem, pending: [{...proposal, digest, binaries: ['/usr/bin/git'], endpoints: [{host: 'github.com', ports: [443]}]}]});
    });
    await ch.tickPoll();
    digest = 'e'.repeat(64);
    let asked = 0;
    ch.setConfirmHandler(() => { asked++; return true; });
    await approveFirst(ch); await flush();
    const sent = mutations(ch).filter((r) => /approve$/.test(r.path)).map((r) => JSON.parse(r.body).digest);
    assert.deepEqual(sent, ['d'.repeat(64), 'e'.repeat(64)]); assert.equal(asked, 2, 'asked again about the new version');
  }
  ok('a changed proposal is refused, read again and asked about again with its new digest');

  {
  // web/approve.js: candidates, the expansion and the summary.
  // With the page's dictionary (Japanese), as the page loads it.
  const box = {window: {}, navigator: {languages: ['ja']}}; vm.runInNewContext(i18nSource, box); vm.runInNewContext(approveSource, box); const A = box.window.IwatoApprove;
  const paths = (p) => JSON.parse(JSON.stringify(A.binaryCandidates(p).map((c) => c.path)));
  assert.deepEqual(paths('/usr/bin/python3.12'), ['/usr/bin/python3.12', '/usr/bin/python3.*', '/usr/bin/python*', '/usr/bin/*', '/**']);
  assert.deepEqual(paths('/home/dev/.local/share/claude/versions/2.1.293'), ['/home/dev/.local/share/claude/versions/2.1.293', '/home/dev/.local/share/claude/versions/*', '/**'], 'a version as the name: versions/*, which is also the folder');
  assert.deepEqual(paths('/opt/node-v22.1.0-rc1/bin/node'), ['/opt/node-v22.1.0-rc1/bin/node', '/opt/node-v*/bin/node', '/opt/node-v22.1.0-rc1/bin/*', '/**'], 'v22.1.0-rc1 is a version');
  assert.deepEqual(paths('/nix/store/0a1b2c3d4e5f-curl/bin/curl'), ['/nix/store/0a1b2c3d4e5f-curl/bin/curl', '/nix/store/*-curl/bin/curl', '/nix/store/0a1b2c3d4e5f-curl/bin/*', '/**'], 'a hash');
  assert.deepEqual(paths('/usr/bin/x86_64-linux-gnu-gcc'), ['/usr/bin/x86_64-linux-gnu-gcc', '/usr/bin/*', '/**'], 'x86_64 is no version');
  assert.deepEqual(paths('/usr/bin/deadbeef'), ['/usr/bin/deadbeef', '/usr/bin/*', '/**'], 'hex letters alone are a word');
  assert.deepEqual(paths(''), ['/**']); assert.deepEqual(paths('/bin'), ['/bin', '/*', '/**']);
  assert.equal(A.binaryCandidates('/usr/bin/python3.12').find((c) => c.kind === 'all').label, 'すべてのプログラム');
  // glob.match(p, ["/"], path): * and ? stay in a segment, ** crosses.
  assert(A.globMatch('/sandbox/.claude/downloads/claude-*-linux-x64', '/sandbox/.claude/downloads/claude-2.1.300-linux-x64'));
  assert(!A.globMatch('/sandbox/.claude/downloads/*', '/sandbox/.claude/downloads/sub/x'));
  // The access to start with when OpenShell would refuse some; the paths as the owner reads them.
  assert.equal(A.pickAccess('read', {}), 'read'); assert.equal(A.pickAccess('audit', {audit: {}}), 'read'); assert.equal(A.pickAccess('read', {read: {}, rules: {}}), 'all');
  assert.equal(A.pickAccess('read', {read: {}, rules: {}, all: {}, audit: {}}), '');
  assert.equal(A.pathLabel('/'), '/ (ルートだけ)'); assert.equal(A.pathLabel('/**'), '/** (すべて)'); assert.equal(A.pathLabel('**'), '** (すべて)'); assert.equal(A.pathLabel('/v1/**'), '/v1/**');
  assert(A.globMatch('/sandbox/**', '/sandbox/.claude/downloads/sub/x')); assert(A.globMatch('/**', '/usr/bin/curl'));
  assert(!A.globMatch('/usr/bin/git', '/usr/bin/gitk')); assert(!A.globMatch('/usr/bin/python3.?', '/usr/bin/python3.9'), 'without * OpenShell compares the path as it is'); assert(A.globMatch('/usr/bin/py*3.?', '/usr/bin/python3.9')); assert(!A.globMatch('/usr/bin/a.c*', '/usr/bin/abc'), 'a dot is literal');
  for (const bad of ['claude', '/', '/a//b', '/a/', '/a b', '/a\u200bb', '/a\tb']) assert(A.binaryError(bad), bad);
  assert.equal(A.binaryError('/opt/日本語/x'), '');
  assert.equal(A.defaultAccess([{method: 'GET', path: '/'}], [{host: 'h', ports: [443]}]), 'read');
  assert.equal(A.defaultAccess([{method: '*', path: '/'}], [{host: 'h', ports: [443]}]), 'rules', 'any method may write');
  assert.equal(A.defaultAccess([], [{host: 'h', ports: [5432], protocol: 'rest'}]), 'read', 'an inspected endpoint is HTTP');
  assert.equal(A.defaultAccess([], [{host: 'h', ports: [5432]}]), 'all');
  assert.equal(A.toggleSegment('/a/b/c', '/a/b/c', 0), '/*/b/c'); assert.equal(A.toggleSegment('/*/b/c', '/a/b/c', 0), '/**'); assert.equal(A.toggleSegment('/**', '/a/b/c', 0), '/a/b/c');
  assert.equal(A.toggleSegment('/a/**', '/a/**', 1), '/a/*', 'a ** typed by hand turns into *');
  assert.equal(A.pathSuggestion(['/o/r/main/a.sh', '/o/r/main/b/c.md']), '/o/r/main/**'); assert.equal(A.pathSuggestion(['/a', '/b']), ''); assert.equal(A.pathSuggestion(['/o/a']), '');
  const build = (o) => JSON.parse(JSON.stringify(A.buildScope({binary: '/usr/bin/curl', proposed: ['/usr/bin/curl'], ...o})));
  assert.deepEqual(build({access: 'all'}), {scope: {binaries: ['/usr/bin/curl']}, rules: [], error: ''}, 'as proposed sends the program (the server never keeps the proposal\'s)');
  assert.deepEqual(build({access: 'read'}).scope, {binaries: ['/usr/bin/curl'], readOnly: true});
  const expanded = build({access: 'read', rows: [{method: 'POST', path: ' /a '}, {method: 'GET', path: '/a'}, {method: 'GET', path: '/b/**'}, {method: 'GET', path: ''}]});
  assert.deepEqual(expanded.scope, {binaries: ['/usr/bin/curl'], readOnly: true, rules: [{method: '', path: '/a'}, {method: '', path: '/b/**'}]});
  assert.deepEqual(expanded.rules.map((r) => r.method + ' ' + r.path), ['GET /a', 'HEAD /a', 'OPTIONS /a', 'GET /b/**', 'HEAD /b/**', 'OPTIONS /b/**'], 'read-only x paths');
  assert.deepEqual(build({access: 'read', rows: [{path: '/a'}, {path: '/**'}]}).scope, {binaries: ['/usr/bin/curl'], readOnly: true}, '/** is every path');
  const many = build({access: 'read', rows: Array.from({length: 17}, (_, i) => ({path: '/p' + i}))});
  assert.match(many.error, /パスは 16 個までです \(いま 17 個\)/); assert.equal(many.rules.length, 51);
  assert.match(build({access: 'rules', rows: []}).error, /1 つ以上/); assert.match(build({access: 'rules', rows: [{method: 'GET', path: 'x'}]}).error, /\/ で始まり/);
  assert.deepEqual(build({access: 'all', binary: '/usr/bin/*'}).scope, {binaries: ['/usr/bin/*']}, 'a program alone');
  assert.match(build({access: 'all', binary: ''}).error, /プログラムを入力/);
  assert.equal(build({access: 'all', binary: '', proposed: []}).scope, null);
  const sum = (o) => A.summary({binary: '/usr/bin/curl', proposed: ['/usr/bin/curl'], endpoints: [{host: 'a.example', ports: [443, 8443]}], ...o});
  const line = (o) => { const x = sum(o); return [x.dest, x.access, x.paths, x.program, x.write].join(' | '); };
  assert.equal(line({access: 'all'}), 'a.example:443,8443 | 提案どおり: 検査せず、すべての通信 |  | プログラム: /usr/bin/curl | true', 'as proposed, uninspected: everything, a write');
  const rest = (more) => [{host: 'a.example', ports: [443], protocol: 'rest', enforcement: 'enforce', ...more}];
  assert.equal(line({access: 'all', endpoints: rest(), seen: [{method: 'GET', path: '/a'}]}), 'a.example | 提案どおり | GET /a | プログラム: /usr/bin/curl | false', 'as proposed, inspected and enforced: the proposed rules');
  // As proposed, what widens the rule beyond its rules counts as writes; what cannot be told is warned.
  const asProposed = (endpoints, more) => [...A.writes({access: 'all', binary: '/usr/bin/curl', proposed: ['/usr/bin/curl'], endpoints, ...more})];
  assert.deepEqual(asProposed(rest({access: 'full'})), ['すべてのメソッド (すべてのパス)'], 'access full, no rules');
  assert.deepEqual(asProposed(rest({access: 'read-write'}), {seen: [{method: 'GET', path: '/a'}]}), ['POST・PUT・PATCH (すべてのパス)']);
  assert.deepEqual(asProposed(rest({access: 'read-only'}), {seen: [{method: 'GET', path: '/a'}]}), [], 'read-only stays green');
  assert.deepEqual(asProposed(rest({access: 'odd'}), {seen: [{method: 'GET', path: '/a'}]}), ['提案が許可する範囲を確かめられません (書き込みを含むかもしれません)']);
  assert.deepEqual(asProposed(rest()), ['提案が許可する範囲を確かめられません (書き込みを含むかもしれません)'], 'no rules and no preset: unknown');
  assert.deepEqual(asProposed(rest({enforcement: 'audit'}), {seen: [{method: 'GET', path: '/a'}]}), ['すべての要求 (止めずに通して記録)'], 'audit lets everything through');
  assert.deepEqual(asProposed(rest({enforcement: ''}), {seen: [{method: 'GET', path: '/a'}]}), ['すべての要求 (止めずに通して記録)'], 'enforcement not reported: treated as audit');
  assert.deepEqual(asProposed(rest({access: 'full'}), {placeholder: true}), ['すべてのメソッド (すべてのパス)'], 'the placeholder does not hide a preset');
  assert.deepEqual(asProposed(rest(), {placeholder: true}), [], 'the placeholder alone is GET / only');
  const audSum = sum({access: 'all', endpoints: rest({enforcement: 'audit'}), seen: [{method: 'GET', path: '/a'}]});
  assert.deepEqual([audSum.access, audSum.paths, audSum.write], ['提案どおり: 監査 (すべての要求を通して記録)', '', true]);
  assert.equal(line({access: 'read', binary: '/**'}), 'a.example:443,8443 | 読み取りだけ (GET・HEAD・OPTIONS) | すべてのパス | プログラム: すべて | false');
  assert.equal(line({access: 'rules', rows: [{method: '*', path: '/x/**'}, {method: 'POST', path: '/y'}]}), 'a.example:443,8443 | 書き込みも許可 | すべてのメソッド /x/**, POST /y | プログラム: /usr/bin/curl | true');
  assert.deepEqual([...A.writes({access: 'rules', binary: '/usr/bin/curl', proposed: ['/usr/bin/curl'], rows: [{method: 'GET', path: '/a'}, {method: 'POST', path: '/y'}]})], ['POST /y'], 'only the writes');
  assert.equal(sum({access: 'rules', rows: [{method: 'HEAD', path: '/a'}]}).access, '読み取りだけ (指定したメソッド)');
  assert.equal(line({access: 'audit'}), 'a.example:443,8443 | 監査: すべての要求を通して記録 |  | プログラム: /usr/bin/curl | true');
  const ps = {provider: 'gh', label: 'GitHub', repos: [], mutations: ['createPullRequest']};
  assert.deepEqual([...A.scopeLines(ps).map((l) => l.text + ' ' + l.write)], ['GraphQL のミューテーション createPullRequest を許可します。OpenShell は引数を見ないので、トークンが届くすべてのリポジトリに効きます true']);
  assert.match(A.scopeLines({...ps, mutations: []})[0].text, /もうこの提案を許可/); assert.equal(A.scopeLines({...ps, mutations: []})[0].write, false); assert.match(A.scopeNote({...ps, mutations: []}), /もう範囲内/);
  const hf = {provider: 'hf', label: 'Hugging Face', repos: [], mutations: [], write: true};
  assert.deepEqual([...A.scopeLines(hf).map((l) => l.text)], ['範囲を「書き込みも許可」にします (hf (Hugging Face) の宛先へのすべての要求)']); assert.equal(A.scopeNote(hf), 'hf (Hugging Face) の宛先 · 承認すると範囲を「書き込みも許可」にします');
  const atl = {provider: 'at', label: 'Atlassian', repos: [], mutations: [], tools: ['createJiraIssue']};
  assert.match(A.scopeLines(atl)[0].text, /MCP のツール createJiraIssue を許可します/); assert.match(A.scopeNote(atl), /ツール createJiraIssue を追加/);
  ok('approve.js: program candidates narrow to wide, glob semantics, read-only x paths, the 16 limit, the summary');
  box.window.IwatoI18n.setPreference('en');
  assert.equal(line({access: 'read', rows: [{method: 'GET', path: '/a/**'}]}), 'a.example:443,8443 | Read only (GET, HEAD, OPTIONS) | /a/** | Program: /usr/bin/curl | false');
  assert.equal(A.binaryCandidates('/usr/bin/curl')[0].label, 'As is'); assert.equal(A.pathLabel('/'), '/ (the root only)');
  ok('approve.js in English');
  }
  }
  const internal = {...proposalItem, pending: [{...proposal, allowedIps: [{host: 'ci.tailnet.example', ips: ['100.64.0.0/10', 7]}, {host: '', ips: ['10.0.0.0/8']}, {host: 'x.example', ips: []}, 'bad', {ips: ['1.2.3.4']}]}]};
  const intEnv = await environment({fake: false}); intEnv.setProposalHandler(() => response(internal)); await intEnv.tickPoll();
  await approveFirst(intEnv);
  const asked = intEnv.els['approve-head'].children.filter((c) => c.className === 'approve-warn').map((c) => c.textContent).join('\n');
  assert.equal(asked, 'ci.tailnet.example: 解決先 IP を 100.64.0.0/10 に限定 (内部アドレスも許可)\nすべてのホスト: ホストを問わず、解決先 IP が 10.0.0.0/8 なら許可 (内部アドレスも)', 'internal addresses are warned; malformed entries are dropped');
  ok('the approve dialog spells out the internal addresses (allowed_ips) a proposal opens');
  const withPolicy = {...proposalItem, policy: {version: 3, loaded: false, rules: [{name: 'git', binaries: ['/usr/bin/git'], endpoints: [{host: 'github.com', ports: [443], inspect: 'rest', enforcement: 'enforce', rules: [{method: 'GET', path: '/o/r.git/**'}, {method: '*', path: '/o/r.git/info/**'}], deny: [{method: 'DELETE', path: '/**'}], allowedIps: ['100.64.0.0/10', 42, '']}]},
    {name: 'api', binaries: ['/usr/bin/curl', '/usr/bin/wget'], endpoints: [{host: 'api.example.com', ports: [443, 8443], inspect: 'rest', enforcement: 'audit', access: 'read-only', rules: []}, {host: 'example.com', ports: [80], inspect: '', rules: []}]}]}};
  const pol = await environment({fake: false, status: 'running'}); pol.setProposalHandler((path) => /\/policy\/[a-z-]+$/.test(path) && !path.endsWith('/history') ? response({ok: true}) : path.endsWith('/history') ? response({revisions: [{version: 3, status: 'pending', createdAt: 9}, {version: 2, status: 'loaded', active: true, createdAt: 8}]}) : path.endsWith('/history/2') ? response({version: 2, loaded: true, rules: [{name: 'old', binaries: ['/usr/bin/ssh'], endpoints: [{host: 'old.example', ports: [22]}]}]}) : response(withPolicy));
  await pol.tickPoll();
  const current = pol.policy().children[1].children[1];
  const listed = texts(current);
  for (const want of ['版 3 · サンドボックスへの反映待ち', 'git', '/usr/bin/git', 'github.com:443 · 検査 (遮断)', 'GET /o/r.git/**', 'すべて /o/r.git/info/**', '拒否: DELETE /** (すべて)', '解決先 IP を 100.64.0.0/10 に限定 (内部アドレスも許可)', 'api.example.com:443,8443 · 検査 (監査 · すべて通す)', '読み取りだけ', 'example.com:80 · すべてのメソッドとパス']) assert(listed.split('\n').includes(want), want + ' in\n' + listed);
  // The last of a kind goes with its endpoint or rule: no × for it.
  assert.equal(find(current, 'プログラム /usr/bin/git を外す'), undefined); assert.equal(find(current, '宛先 github.com:443 を外す'), undefined);
  assert(find(current, 'プログラム /usr/bin/curl を外す') && find(current, '宛先 example.com:80 を外す'));
  const posted = (op) => pol.requests.filter((r) => r.path === '/api/sandboxes/api/policy/' + op);
  find(current, '規則「git」を外す').click(); await flush(); assert.equal(posted('remove').length, 0, 'removing asks first');
  pol.setConfirm(true); find(pol.policy().children[1].children[1], '規則「git」を外す').click(); await flush();
  assert.deepEqual(JSON.parse(posted('remove')[0].body), {rule: 'git'}); assert.equal(posted('remove')[0].headers['X-CSRF-Token'], 'csrf123');
  find(pol.policy().children[1].children[1], 'GET /o/r.git/** を外す').click(); await flush();
  assert.deepEqual(JSON.parse(posted('remove-allow')[0].body), {rule: 'git', host: 'github.com', ports: [443], rules: [{method: 'GET', path: '/o/r.git/**'}]});
  find(pol.policy().children[1].children[1], '宛先 api.example.com:443,8443 を外す').click(); await flush();
  assert.deepEqual(JSON.parse(posted('remove-endpoint')[0].body), {rule: 'api', host: 'api.example.com', ports: [443, 8443]});
  find(pol.policy().children[1].children[1], 'プログラム /usr/bin/wget を外す').click(); await flush();
  assert.deepEqual(JSON.parse(posted('remove-binary')[0].body), {rule: 'api', binary: '/usr/bin/wget'});
  assert.match(pol.policy().children[0].children[3].textContent, /外しました。.*10 秒ほど/);
  ok('the policy tab lists the rules piece by piece and takes out a rule, a program, an endpoint, or a method and path after a confirmation');
  // Adding: the endpoint form, then the approve dialog's narrowing.
  pol.setConfirmHandler((question) => {
    if (!/規則を追加します/.test(question)) return true;
    assert.equal(pol.els['policy-form'].hidden, false); assert.equal(pol.els['approve-scope'].dataset.mode, 'add'); assert.equal(pol.els['policy-form'].dataset.mode, 'rule');
    pol.els['policy-binaries'].value = ' /usr/bin/curl \n\n'; pol.els['policy-host'].value = ' api.example.org '; pol.els['policy-ports'].value = '443, 8443';
    pol.els['approve-scope-all'].checked = false; pol.els['approve-scope-readwrite'].checked = true; pol.els['policy-audit'].checked = true;
    return true;
  });
  find(pol.policy().children[1].children[1], '規則を追加').click(); await flush();
  assert.deepEqual(JSON.parse(posted('add')[0].body), {rule: '', host: 'api.example.org', ports: [443, 8443], binaries: ['/usr/bin/curl'], access: 'read-write', audit: true});
  assert.equal(pol.els['policy-form'].hidden, true); assert.equal(pol.els['approve-scope'].hidden, true);
  pol.setConfirmHandler((question) => {
    assert.match(question, /規則「git」に宛先を追加します/); assert.equal(pol.els['policy-form'].dataset.mode, 'endpoint'); assert.equal(pol.els['policy-rule'].value, 'git');
    pol.els['policy-host'].value = 'cdn.example.org'; pol.els['policy-ports'].value = '443';
    pol.els['approve-scope-all'].checked = false; pol.els['approve-scope-rules'].checked = true; pol.els['approve-scope-rules'].listeners.change();
    const row = pol.els['approve-rules'].children[0]; row.children[0].value = 'GET'; row.children[1].value = '/assets/**';
    return true;
  });
  find(pol.policy().children[1].children[1], '宛先を追加').click(); await flush();
  assert.deepEqual(JSON.parse(posted('add')[1].body), {rule: 'git', host: 'cdn.example.org', ports: [443], rules: [{method: 'GET', path: '/assets/**'}]}, 'an existing rule keeps its programs');
  pol.setConfirmHandler((question) => {
    assert.match(question, /github\.com:443 に、許可するメソッドとパスを追加/); assert.equal(pol.els['approve-scope'].dataset.mode, 'allow'); assert.equal(pol.els['policy-form'].hidden, true);
    const row = pol.els['approve-rules'].children[0]; row.children[0].value = 'POST'; row.children[1].value = ' /o/r.git/git-receive-pack ';
    return true;
  });
  find(pol.policy().children[1].children[1], 'メソッドとパスを追加').click(); await flush();
  assert.deepEqual(JSON.parse(posted('add-allow')[0].body), {rule: 'git', host: 'github.com', ports: [443], rules: [{method: 'POST', path: '/o/r.git/git-receive-pack'}]});
  ok('adding a rule, an endpoint of a rule, or methods and paths posts what the dialog says');
  {
  // Requests an inspected endpoint denied (no proposal comes of them):
  // picked ones go into the allow dialog; a provider's rule takes a rule
  // by hand for the same host instead.
  const denied = {...proposalItem, policy: {version: 4, loaded: true, active: 4, rules: [
    {name: 'ci', source: 'hand', binaries: ['/usr/bin/curl'], endpoints: [{host: 'ci.example', ports: [443], inspect: 'rest', enforcement: 'enforce', access: 'read-only', rules: [], denied: [
      {method: 'POST', path: '/api/builds', binary: '/usr/bin/curl', count: 3, last: 1700000000000, reason: 'POST /api/builds not permitted by policy'},
      {method: 'DELETE', path: '/api/x', count: 1, last: 1700000000000, reason: 'DELETE /api/x blocked by deny rule'},
      {kind: 'binary', method: '', path: '', binary: '/usr/bin/wget', count: 2, last: 1700000000000, reason: "binary '/usr/bin/wget' not allowed in policy 'ci'"},
      {method: 'GET', path: 7}, 'bad']}]},
    {name: '_provider_github', source: 'provider', binaries: ['/usr/bin/gh', '/usr/bin/git'], endpoints: [{host: 'api.github.com', ports: [443], inspect: 'rest', enforcement: 'enforce', access: 'read-only', rules: [], denied: [
      {method: 'POST', path: '/repos/o/r/releases', binary: '/usr/bin/gh', count: 1, last: 1700000000000, reason: 'POST /repos/o/r/releases not permitted by policy'}]}]}]}};
  const den = await environment({fake: false, status: 'running'});
  den.setProposalHandler((path) => /\/policy\/[a-z-]+$/.test(path) ? response({ok: true}) : response(denied));
  await den.tickPoll();
  const cur = () => den.policy().children[1].children[1];
  const shown = texts(cur());
  for (const want of ['止められたリクエスト:', 'POST /api/builds', '×3 · /usr/bin/curl · ', '許可にないメソッドとパスです', '拒否の規則に当たりました (許可を足しても通りません)', 'プログラム /usr/bin/wget', 'POST /repos/o/r/releases']) assert(shown.includes(want), want + ' in\n' + shown);
  const items = nodes(cur()).filter((x) => x.className === 'policy-denied-item');
  assert.equal(items.length, 4, 'malformed entries are dropped');
  const boxes = items.map((x) => x.children.find((c) => c.type === 'checkbox'));
  assert.equal(boxes[1].disabled, true, 'a deny rule wins: no allow helps'); assert.equal(boxes[2], undefined, 'a program refused has no checkbox');
  const addBtn = find(cur(), '選んだものを許可に追加'); assert.equal(addBtn.disabled, true, 'nothing picked yet');
  boxes[0].checked = true; boxes[0].listeners.change(); assert.equal(addBtn.disabled, false);
  let seeded;
  den.setConfirmHandler((question) => {
    assert.match(question, /規則「ci」の宛先 ci\.example:443 に、止められたメソッドとパスを許可として追加/);
    assert.match(question, /「読み取りだけ」は、OpenShell が同じ意味の規則 \(すべてのパスへの GET・HEAD・OPTIONS\) に置き換えてから足します/);
    assert.equal(den.els['approve-scope'].dataset.mode, 'allow');
    const row = den.els['approve-rules'].children[0];
    seeded = [row.children[0].value, row.children[1].value];
    // The segments click into * and **.
    const segs = row.children[3].children.map((b) => b.textContent);
    assert.deepEqual(segs, ['/api', '/builds']);
    row.children[3].children[1].click(); assert.equal(row.children[1].value, '/api/*');
    return true;
  });
  addBtn.click(); await flush();
  assert.deepEqual(seeded, ['POST', '/api/builds']);
  const postedTo = (op) => den.requests.filter((r) => r.path === '/api/sandboxes/api/policy/' + op);
  assert.deepEqual(JSON.parse(postedTo('add-allow')[0].body), {rule: 'ci', host: 'ci.example', ports: [443], rules: [{method: 'POST', path: '/api/*'}]});
  // The provider's: a new rule by hand, enforcing with the methods and paths.
  const provBoxes = nodes(cur()).filter((x) => x.className === 'policy-denied-item').map((x) => x.children.find((c) => c.type === 'checkbox'));
  provBoxes[3].checked = true; provBoxes[3].listeners.change();
  assert.equal(find(cur(), '選んだものを許可に追加').disabled, true, 'the hand rule\'s button follows its own picks');
  den.setConfirmHandler((question) => {
    assert.match(question, /プロバイダーの規則「_provider_github」は変えられないので、同じ宛先 api\.github\.com:443 に規則を追加します/);
    assert.match(question, /遮断モードでメソッドとパスを指定/);
    assert.equal(den.els['policy-form'].dataset.mode, 'rule'); assert.equal(den.els['policy-host'].value, 'api.github.com'); assert.equal(den.els['policy-ports'].value, '443');
    assert.equal(den.els['policy-binaries'].value, '/usr/bin/gh'); assert.equal(den.els['approve-scope-rules'].checked, true); assert.equal(den.els['policy-audit'].checked, false);
    return true;
  });
  den.els['approve-rules'].children = []; // the stand-in's textContent = '' keeps children
  const provAdd = find(cur(), '止められたリクエストから規則を追加'); assert.equal(provAdd.textContent, '規則を追加');
  provAdd.click(); await flush();
  assert.deepEqual(JSON.parse(postedTo('add').at(-1).body), {rule: '', host: 'api.github.com', ports: [443], binaries: ['/usr/bin/gh'], rules: [{method: 'POST', path: '/repos/o/r/releases'}]});
  ok('denied requests list under their endpoint; picked ones go into add-allow, a provider\'s into a new rule by hand');
  }
  find(pol.policy().children[1].children[1], '履歴').click(); await flush();
  const hist = pol.policy().children[1].children[2]; assert.equal(hist.hidden, false);
  assert.match(texts(hist), /版 3 · 反映待ち/); assert.match(texts(hist), /版 2 · 反映済み \(使用中\)/);
  find(hist, '見る').click(); await flush();
  assert.equal(pol.requests.at(-1).path, '/api/sandboxes/api/policy/history/3');
  find(hist, '閉じる').click(); await flush();
  nodes(hist).filter((x) => x.textContent === '見る')[1].click(); await flush();
  assert.match(texts(hist), /old\.example:22/); assert.equal(find(hist, '規則「old」を外す'), undefined, 'an old revision is read-only');
  ok('the history lists the revisions and shows one read-only');
  find(pol.policy().children[1].children[1], 'プリセット').click(); await flush(); await flush();
  const presetBox = () => nodes(pol.policy().children[1].children[1]).find((x) => x.className === 'policy-presets');
  assert.match(texts(presetBox()), /パッケージのダウンロード/); assert.match(texts(presetBox()), /読めないプリセット bad\.yaml: field x not found/); assert.match(texts(presetBox()), /読めないプリセット odd\.yaml: 規則 r: \$\{x\} という params はありません/, 'a coded file error is worded by the page');
  assert.equal(nodes(presetBox()).filter((x) => x.textContent === '入れる').length, 1, 'a preset without parameters that is in is not offered again');
  pol.setConfirmHandler(null); pol.setConfirm(true);
  nodes(presetBox()).find((x) => x.dataset?.param === 'repo').value = ' mtsmfm/iwato ';
  find(presetBox(), '入れる').click(); await flush(); await flush();
  assert.deepEqual(JSON.parse(pol.requests.find((r) => r.path === '/api/sandboxes/api/presets/apply').body), {id: 'user:mirror', params: {repo: 'mtsmfm/iwato'}});
  find(presetBox(), '外す').click(); await flush(); await flush();
  assert.deepEqual(JSON.parse(pol.requests.find((r) => r.path === '/api/sandboxes/api/presets/remove').body), 'packages');
  assert.match(pol.policy().children[0].children[3].textContent, /allow_x_443 は残しました/);
  ok('the policy tab puts a preset in with its parameters and takes one out, naming the rules it left');
  const confirmsBefore = eg.confirmations.length; find(egRow(), '却下').click(); await flush();
  assert.equal(eg.confirmations.length, confirmsBefore, 'rejecting needs no confirmation'); assert.equal(mutations(eg).at(-1).path, '/api/sandboxes/api/proposals/reject'); assert.deepEqual(JSON.parse(mutations(eg).at(-1).body), {requestId: 'a'.repeat(32), digest: 'd'.repeat(64)});
  ok('rejecting is one click');
  const staleEg = await environment({fake: false}); staleEg.setConfirm(true); staleEg.setProposalHandler((path) => path.endsWith('/approve') ? response({error: 'この依頼は決定済みです'}, 409) : response(proposalItem)); await staleEg.tickPoll();
  await approveFirst(staleEg);
  assert.equal(mutations(staleEg).length, 1); assert.equal(staleEg.policy().children[0].children[2].hidden, false); assert.match(staleEg.policy().children[0].children[2].textContent, /決定済み/);
  await staleEg.tickPoll(); assert.match(staleEg.policy().children[0].children[2].textContent, /決定済み/, 'the error stays across refreshes'); assert.equal(staleEg.confirmations.length, 1, 'nothing is resent');
  ok('a refused decision shows the server\'s reason, keeps it across refreshes and is not resent');
  const pendingRead = deferred(); const guarded = await environment({fake: false}); guarded.setProposalHandler(() => pendingRead.promise);
  const guardedBefore = proposalReads(guarded).length; policyRefresh(guarded).click(); policyRefresh(guarded).click(); await guarded.tickPoll(); assert.equal(proposalReads(guarded).length, guardedBefore + 1);
  pendingRead.resolve(response(proposalItem)); await flush(); assert.equal(pendingItems(guarded).length, 1);
  ok('concurrent proposal reads coalesce');
  const mutationWait = deferred(); const busy = await environment({fake: false}); busy.setConfirm(true); busy.setProposalHandler((path) => path.endsWith('/approve') ? mutationWait.promise : response(proposalItem)); await busy.tickPoll();
  await approveFirst(busy); const busyReads = proposalReads(busy).length;
  assert.equal(find(pendingItems(busy)[0], '却下').disabled, true); find(pendingItems(busy)[0], '却下').click(); policyRefresh(busy).click(); await busy.tickPoll();
  assert.equal(proposalReads(busy).length, busyReads, 'no reads while a decision is in flight'); assert.equal(mutations(busy).length, 1);
  mutationWait.resolve(response({ok: true})); await flush(); assert(proposalReads(busy).length > busyReads);
  ok('an in-flight decision blocks repeated decisions and polling reads until it completes');
  const hidden = await environment({fake: false}); assert.equal(hidden.intervals[0].ms, 10000); const visibleRequests = hidden.requests.length; hidden.document.hidden = true; await hidden.tickPoll(); policyRefresh(hidden).click(); await flush(); assert.equal(hidden.requests.length, visibleRequests); hidden.document.hidden = false; hidden.document.listeners.visibilitychange(); await flush(); assert(proposalReads(hidden).length > 1); assert.equal(mutations(hidden).length, 0);
  ok('10-second polling and manual status reads pause while hidden; visibility recovery remains read-only');
  const errors = await environment({fake: false}); errors.setProposalHandler(() => response({error: '<script>bad status</script>'}, 503)); await errors.tickPoll(); const errorNode = errors.policy().children[0].children[2];
  assert.match(errorNode.textContent, /<script>/); const writes = errorNode.textWrites; await errors.tickPoll(); await errors.tickPoll(); assert.equal(errorNode.textWrites, writes); assert.equal(mutations(errors).length, 0);
  ok('repeated status failures remain literal text and are not re-announced');
  const authLost = await environment({fake: false}); authLost.setProposalHandler(() => response({error: 'expired session'}, 401)); policyRefresh(authLost).click(); await flush(); assert.equal(policyRefresh(authLost).disabled, true); assert.equal(authLost.els['server-state'].dataset.state, 'error');
  ok('session invalidation disables owner actions');
  const lateRead = deferred(); const gone = await environment({fake: false}); gone.setProposalHandler(() => lateRead.promise); policyRefresh(gone).click(); const goneRoot = gone.policy(); gone.win.listeners.pagehide(); lateRead.resolve(response(proposalItem)); await flush(); assert.equal(goneRoot.children[1].children[0].children.length, 0);
  ok('a late status response after page disposal shows nothing to approve');
  const listWait = deferred(); const listGuard = await environment(); const readsBefore = listGuard.requests.filter(r => r.path === '/api/sandboxes').length; listGuard.setSandboxHandler(() => listWait.promise); listGuard.els['refresh-button'].click(); await listGuard.tickPoll(); await listGuard.tickPoll(); assert.equal(listGuard.requests.filter(r => r.path === '/api/sandboxes').length, readsBefore + 1); listWait.resolve(response({fake: true, sandboxes: [{...listGuard.sandbox}]})); await flush();
  ok('sandbox refresh guard prevents overlapping global refreshes');
  const retry = await environment(); assert.equal(retry.els['refresh-button'].hidden, true, 'no retry button while the list loads fine');
  retry.setSandboxHandler(() => response({error: 'down'}, 500)); await retry.tickPoll(); assert.equal(retry.els['refresh-button'].hidden, false);
  retry.setSandboxHandler(() => response({fake: true, sandboxes: [{...retry.sandbox}]})); retry.els['refresh-button'].click(); await flush(); assert.equal(retry.els['refresh-button'].hidden, true);
  ok('the retry button appears only after the list or session fails');

  const login = await environment({token: false, unauthorized: true}); assert.equal(login.els['login-panel'].hidden, false);
  login.setUnauthorized(false); login.els['login-input'].value = '  https://pc.example.ts.net/#token=' + 'C'.repeat(64) + ' ';
  login.els['login-form'].listeners.submit({preventDefault() {}}); await flush();
  const manual = login.requests.filter(r => r.path === '/api/session' && r.method === 'POST');
  assert.equal(manual.length, 1); assert.equal(JSON.parse(manual[0].body).token, 'c'.repeat(64)); assert.equal(login.els['login-panel'].hidden, true); assert.equal(login.els['login-input'].value, '');
  login.els['login-input'].value = 'not a token'; login.els['login-form'].listeners.submit({preventDefault() {}}); await flush(); assert.equal(login.requests.filter(r => r.path === '/api/session' && r.method === 'POST').length, 1);
  ok('phone login accepts a pasted pairing URL and rejects malformed tokens without a request');
  const pair = await environment(); assert.equal(pair.els['pair-button'].hidden, false); pair.els['pair-button'].click(); await flush();
  const pairReq = pair.requests.find(r => r.path === '/api/pair'); assert.equal(pairReq.method, 'POST'); assert.equal(pairReq.headers['X-CSRF-Token'], 'csrf123');
  assert.match(pair.els['pair-url'].textContent, /#token=b{64}$/); assert.equal(pair.els['pair-panel'].hidden, false); assert.equal(pair.els['pair-qr'].src, 'data:image/png;base64,iVBORw0KGgo='); assert.equal(pair.els['pair-qr'].hidden, false); pair.els['pair-close'].click(); assert.equal(pair.els['pair-qr'].hidden, true); assert.equal(pair.els['pair-url'].textContent, ''); assert.equal(pair.els['pair-panel'].hidden, true);
  ok('pairing issues a link only through an authenticated CSRF POST and can be dismissed');
  const tabbed = await environment({fake: false}); tabbed.setProposalHandler(() => response({...proposalItem, pending: [{...proposal, id: 'c'.repeat(32), source: 'auto', summary: '/usr/bin/curl → auto.example:443'}, ...proposalItem.pending]})); await tabbed.tickPoll();
  const [termTab, policyTab, , portsTab, viewTab] = tabbed.card().children[2].children[0].children; assert.deepEqual(tabbed.card().children[2].children[0].children.map((t) => t.textContent), ['ターミナル', 'ポリシー', 'エディタ', 'ポート', '']); assert.equal(viewTab.hidden, true);
  { const views = nodes(tabbed.card()).filter((n) => n.dataset?.view); assert.deepEqual(views.map((n) => [n.textContent, n.getAttribute('role'), n.parent.className]), [['リンク', 'menuitem', 'card-menu'], ['テキスト', 'menuitem', 'card-menu'], ['ファイル', 'menuitem', 'card-menu']], 'the rarer views are in the ⋯ menu');
    views[2].click(); assert.equal(tabbed.card().dataset.tab, 'files'); assert.equal(viewTab.hidden, false); assert.equal(viewTab.textContent, 'ファイル', 'the open one shows as the current tab'); assert.equal(views[2].getAttribute('aria-current'), 'true'); assert.equal(termTab.getAttribute('aria-selected'), 'false');
    termTab.click(); assert.equal(viewTab.hidden, true); assert.equal(views[2].getAttribute('aria-current'), null);
    portsTab.click(); assert.equal(tabbed.card().dataset.tab, 'ports'); assert.equal(portsTab.getAttribute('aria-selected'), 'true'); assert.equal(viewTab.hidden, true, 'a tab of the row is not repeated as the view tab');
    termTab.click(); }
  { const bar = tabbed.card().children.find((n) => n.className === 'proposal-bar'); assert.equal(bar.hidden, false); assert.equal(bar.attributes.role, 'status'); assert.equal(bar.children[0].textContent, '提案 1 件: /usr/bin/git → github.com:443 <img src=x onerror=alert(1)>', 'literal text');
    bar.children[2].click(); assert.equal(bar.hidden, true, 'あとで hides it'); await tabbed.tickPoll(); assert.equal(bar.hidden, true, 'until a new proposal arrives'); }
  ok('the tab row has the terminal, the policy, the editor and the ports; the other views open from the ⋯ menu; a line tells about waiting proposals');
  assert.equal(tabbed.card().dataset.tab, 'terminal'); assert.equal(termTab.getAttribute('aria-selected'), 'true');
  assert.equal(policyTab.children[0].hidden, false); assert.equal(policyTab.children[0].textContent, '1', 'OpenShell\'s automatic proposals are not counted'); assert.equal(tabbed.document.title, '(1) Iwato');
  assert.deepEqual(tabbed.policy().children[1].children[0].children.map((n) => n.className), ['policy-sec-title', 'proposal-item', 'policy-sub-title', 'proposal-item'], 'the agent\'s proposals first, then OpenShell\'s under their own heading'); assert.match(texts(pendingItems(tabbed)[1]), /auto\.example/);
  const readsBeforeTab = proposalReads(tabbed).length; policyTab.click(); await flush(); assert.equal(tabbed.card().dataset.tab, 'policy'); assert.equal(policyTab.getAttribute('aria-selected'), 'true'); assert.equal(termTab.getAttribute('aria-selected'), 'false'); assert(proposalReads(tabbed).length > readsBeforeTab);
  assert.equal(tabbed.intervals.filter((i) => !i.cleared).length, 1, 'the policy tab follows proposals with the sandbox list (every 10 seconds) while nothing waits');
  const readsFollowing = proposalReads(tabbed).length; await tabbed.tickPoll(); assert(proposalReads(tabbed).length > readsFollowing);
  termTab.click(); assert.equal(tabbed.card().dataset.tab, 'terminal');
  tabbed.setProposalHandler(() => response({...proposalItem, pending: [{...proposal, source: 'auto'}]})); await tabbed.tickPoll(); assert.equal(policyTab.children[0].hidden, true); assert.notEqual(tabbed.policy().dataset.state, 'pending-approval', 'only automatic proposals are not the waiting state'); assert.match(tabbed.policy().children[0].children[1].textContent, /自動提案 1 件/);
  tabbed.setProposalHandler(() => response({...proposalItem, pending: []})); await tabbed.tickPoll(); assert.equal(policyTab.children[0].hidden, true); assert.equal(tabbed.document.title, 'Iwato');
  ok('proposals live in the card\'s policy tab; its badge and the page title count the agent\'s ones waiting for the owner');
  const side = await environment({fake: false, status: 'running'}); side.sockets.at(-1).open(); const tab = (name) => nodes(side.card()).find((t) => t.dataset?.view && t.textContent.startsWith(name)); tab('リンク').click(); await flush();
  const links = side.side('links-panel'); assert.equal(links.hidden, false); assert.match(links.children[1].textContent, /見つかりません/); assert.equal(links.children[2].children.length, 0);
  assert.equal(side.card().dataset.tab, 'links', 'a panel takes the place of the terminal'); side.buttons()[6].click(); await flush(); side.sockets.at(-1).open(); assert.equal(side.card().dataset.tab, 'terminal', 'reconnecting shows the terminal'); assert.equal(links.hidden, true);
  side.terminals.at(-1).receive(Buffer.from('old https://example.com/old\r\nsee https://example.com/a javascript:alert(1)\r\nhttps://user:pw@example.com/ https://example.com/old (dup)\r\nline1\r\n<script>x</script>\r\n'));
  tab('リンク').click(); const anchors = links.children[2].children.map(li => li.children[0]);
  assert.deepEqual(anchors.map(a => a.href), ['https://example.com/old', 'https://example.com/a']); assert.equal(anchors[0].rel, 'noopener noreferrer'); assert.equal(anchors[0].target, '_blank');
  ok('link list comes from the terminal history: newest first, unique, only http(s) without credentials, new tab without opener');
  tab('テキスト').click(); await flush(); assert.equal(links.hidden, true, 'one panel at a time'); const textPanel = side.side('text-panel'); assert.equal(textPanel.hidden, false); assert.match(textPanel.children[2].textContent, /\nline1\n<script>x<\/script>\n$/);
  ok('terminal text view shows the terminal history as literal text');
  side.buttons()[2].click(); await flush(); side.sockets.at(-1).open();
  const keys = side.keys(); const label = k => keys.find(b => b.textContent === k);
  label('Esc').click(); label('Ctrl').click(); assert.equal(label('Ctrl').getAttribute('aria-pressed'), 'true'); side.terminals.at(-1).input('c');
  const inputs = side.sockets.at(-1).sent.filter(m => m.type === 'input').map(m => m.data); assert.deepEqual(inputs, ['\x1b', '\x03']); assert.equal(label('Ctrl').getAttribute('aria-pressed'), 'false');
  ok('phone key bar sends escape sequences and applies Ctrl to the next key only');
  const pasteTerm = side.terminals.at(-1);
  const findClass = (node, name) => node.className?.split?.(' ').includes(name) ? node : (node.children || []).map(c => findClass(c, name)).find(Boolean);
  const copiedBefore = side.copied.length; pasteTerm.options.onClipboard('unasked'); await flush(); assert.equal(side.copied.length, copiedBefore, 'OSC 52 without the owner\'s input writes nothing');
  findClass(side.card(), 'terminal-host').listeners.keyup(); pasteTerm.options.onClipboard('日本語 copy'); await flush(); assert.deepEqual(side.copied.slice(copiedBefore), ['日本語 copy']);
  label('貼付').click(); await flush(); const pasteBox = findClass(side.card(), 'paste-box'); assert(pasteBox && !pasteBox.hidden, 'paste box shown when the clipboard cannot be read');
  pasteBox.children[0].value = 'a\nb'; pasteBox.children[1].children[0].click(); assert.equal(side.sockets.at(-1).sent.filter(m => m.type === 'input').at(-1).data, 'a\rb'); assert.equal(pasteBox.hidden, true);
  ok('OSC 52 copies only right after the owner\'s input; the paste key falls back to a manual paste box');
  { const lastInputs = (n) => side.sockets.at(-1).sent.filter(m => m.type === 'input').slice(-n).map(m => m.data);
    const uploads = () => side.requests.filter(r => r.path.endsWith('/paste-image'));
    const errorText = () => findClass(side.card(), 'card-message-text').textContent;
    const png = {type: 'image/png', size: 2048, name: 'shot.png'};
    await pasteTerm.options.onFiles([png, {type: 'application/pdf', size: 10, name: 'a.pdf'}]); await flush();
    assert.equal(uploads().length, 1, 'only the image is uploaded'); const up = uploads()[0];
    assert.equal(up.path, '/api/sandboxes/api/paste-image'); assert.equal(up.method, 'POST'); assert.equal(up.body, png); assert.equal(up.headers['Content-Type'], 'image/png'); assert.equal(up.headers['X-CSRF-Token'], 'csrf123');
    assert.deepEqual(lastInputs(2), ['/home/dev/.cache/iwato/paste/20261010T000000Z-0123abcd.png', ' '], 'the path is pasted, then a space');
    assert.match(errorText(), /画像だけ/);
    side.setPasteHandler(() => response({path: "/sandbox/my pics/it's$(x).png"})); await pasteTerm.options.onFiles([png]); await flush();
    assert.deepEqual(lastInputs(2), ["/sandbox/my\\ pics/it\\'s\\$\\(x\\).png", ' '], 'a path with spaces or quotes is one shell word');
    side.setPasteHandler(() => response({path: 'relative\x1b[201~.png'})); const sentBefore = lastInputs(99).length; await pasteTerm.options.onFiles([png]); await flush();
    assert.equal(lastInputs(99).length, sentBefore, 'a path that is not absolute plain text is not pasted'); assert.match(errorText(), /正しくありません/);
    side.setPasteHandler(() => response({error: '画像が大きすぎます (20 MB まで)'}, 413)); await pasteTerm.options.onFiles([png]); await flush(); assert.match(errorText(), /画像を貼り付けられませんでした: 画像が大きすぎます/);
    const count = uploads().length; await pasteTerm.options.onFiles([{type: 'image/gif', size: 30 << 20, name: 'big.gif'}]); await flush();
    assert.equal(uploads().length, count, 'a too large image is not sent'); assert.match(errorText(), /大きすぎます/);
    side.setPasteHandler(null);
    const picker = keys.find(n => n.tagName === 'input'); assert.equal(picker.type, 'file'); assert.equal(picker.accept, 'image/*'); assert.equal(picker.hidden, true);
    label('画像').click(); picker.files = [png]; picker.value = 'C:\\fakepath\\shot.png'; picker.listeners.change(); await flush();
    assert.equal(uploads().length, count + 1, '画像 picks images to paste'); assert.equal(picker.value, '', 'the same image can be picked again');
    assert.deepEqual(lastInputs(2), ['/home/dev/.cache/iwato/paste/20261010T000000Z-0123abcd.png', ' ']); }
  ok('an image pasted, dropped or picked (画像) is uploaded and its path pasted as one shell word with a space; wrong types, sizes and answers show an error');
  const {wrappedLinks, safeURL} = side.win.IwatoLinks;
  const row2 = (t, wrapped = false) => { const cells = []; let x = 0; for (const ch of t) { cells.push({c: ch, x}); x += /[\u3000-\u9fff]/.test(ch) ? 2 : 1; } return {cells, wrapped}; };
  const long = 'https://example.com/aaaaaaaaaa/bbbbbbbbbb/cccccccccc?x=1';
  const found = wrappedLinks([row2('日本語 ' + long.slice(0, 23)), row2('  ' + long.slice(23, 51)), row2('  ' + long.slice(51) + ' done')], 30, 10);
  assert.equal(found.length, 1); assert.equal(found[0].url, long); assert.deepEqual(JSON.parse(JSON.stringify(found[0].start)), {x: 8, y: 10}); assert.deepEqual(JSON.parse(JSON.stringify(found[0].end)), {x: 7, y: 12});
  assert.equal(wrappedLinks([row2('see https://a.example/x.'), row2('next')], 30, 1)[0].url, 'https://a.example/x');
  assert.equal(safeURL('javascript:alert(1)'), ''); assert.equal(safeURL('https://u:p@x.example/'), ''); assert.equal(safeURL('https://x.example/a b'), '');
  ok('wrapped link detection maps wide characters and app-wrapped rows to cell ranges');
  const lk = await environment({fake: false, status: 'running'}); lk.buttons()[2].click(); await flush(); lk.sockets.at(-1).open(); const lkTerm = lk.terminals.at(-1);
  lkTerm.options.onLink('https://a.example/plain', {ctrlKey: false}); assert.equal(lk.win.opened.length, 0);
  lkTerm.options.onLink('https://a.example/ctrl', {ctrlKey: true}); assert.equal(lk.win.opened.at(-1), 'https://a.example/ctrl');
  lkTerm.options.onLink('javascript:alert(1)', {ctrlKey: true}); assert.equal(lk.win.opened.length, 1, 'only http(s)');
  ok('on a PC links open with Ctrl+click');
  const lkHost = findClass(lk.card(), 'terminal-host');
  { const before = lk.win.opened.length; const appOpens = (url) => lkTerm.options.onOpen(url);
    appOpens('https://a.example/codex'); assert.equal(lk.win.opened.length, before, 'output alone opens nothing');
    lkHost.listeners.pointerup({}); appOpens('javascript:alert(1)'); assert.equal(lk.win.opened.length, before, 'only http(s)');
    lkHost.listeners.pointerup({}); appOpens('https://a.example/codex'); assert.equal(lk.win.opened.at(-1), 'https://a.example/codex');
    appOpens('https://a.example/again'); assert.equal(lk.win.opened.length, before + 1, 'one click opens one link'); }
  ok('a link a program in dev opens (Codex on a click, BROWSER) opens in a new tab right after the owner\'s own input');
  {
  const app = await environment({fake: false, status: 'running', androidApp: true}); app.sockets.at(-1).open(); const appTerm = app.terminals.at(-1);
  appTerm.options.onLink('https://a.example/tap', {}); await flush();
  assert.deepEqual(JSON.parse(JSON.stringify(app.shared)), [{url: 'https://a.example/tap'}], 'the installed app on Android offers the share sheet (its links would open in an in-app browser)'); assert.equal(app.win.opened.length, 0);
  app.setShareFailure('AbortError'); appTerm.options.onLink('https://a.example/cancel', {}); await flush();
  assert.equal(app.win.opened.length, 0, 'closing the sheet opens nothing');
  app.setShareFailure('NotAllowedError'); appTerm.options.onLink('https://a.example/fail', {}); await flush();
  assert.deepEqual(app.win.opened, ['https://a.example/fail'], 'a sheet that cannot open falls back to opening the link');
  appTerm.options.onLink('javascript:alert(1)', {}); await flush(); assert.equal(app.shared.length, 3, 'unsafe links are not shared either');
  assert.equal(lk.shared.length, 0, 'a PC never shows the share sheet');
  ok('the installed app on Android opens links through the share sheet, where the owner picks a browser');
  }
  { const before = lk.terminals.length; lk.sockets.at(-1).close(1006); await lk.tickReconnect(); lk.sockets.at(-1).open();
    assert.equal(lk.terminals.length, before, 'the terminal is kept'); assert.equal(lk.sockets.at(-1).sent[0].type, 'hello', 'and attached again with a hello'); }
  ok('reconnecting attaches the same terminal again');


  const del = await environment({fake: false, status: 'running'}); const delButton = del.buttons()[4]; assert.equal(delButton.textContent, '削除'); delButton.click();
  const delPanel = del.side('delete-panel'); assert.equal(delPanel.hidden, false); const [, , purgeLabel, delForm, delStatus] = delPanel.children; const [delInput] = delForm.children;
  delInput.value = 'wrong'; delForm.listeners.submit({preventDefault() {}}); await flush(); assert.equal(del.requests.filter(r => r.path.endsWith('/delete')).length, 0); assert.match(delStatus.textContent, /一致しません/);
  purgeLabel.children[0].checked = true; delInput.value = 'api'; delForm.listeners.submit({preventDefault() {}}); await flush();
  const delReq = del.requests.find(r => r.path === '/api/sandboxes/api/delete'); assert.deepEqual(JSON.parse(delReq.body), {confirm: 'api', purge: true}); assert.equal(delReq.headers['X-CSRF-Token'], 'csrf123');
  assert.equal(delPanel.hidden, true); assert.equal(del.card().children[0].children[1].textContent, '未作成');
  ok('sandbox deletion requires typing the name, sends purge only when chosen, and refreshes the list');
  const rec = await environment({fake: false, status: 'running'}); const recButton = rec.buttons()[7]; assert.equal(recButton.textContent, '作り直す'); assert.equal(recButton.disabled, false); recButton.click();
  const recPanel = rec.side('recreate-panel'); assert.equal(recPanel.hidden, false); const [, , kept, lost, recPull, recForm, recStatus] = recPanel.children; assert.equal(recPull.hidden, true, 'no pull option for a tag that does not move'); const [recInput] = recForm.children;
  const items = box => box.children[1].children.map(li => li.textContent).join(' ');
  assert.match(items(kept), /ボリューム.*\/home\/dev/); assert.match(items(kept), /プロバイダー/); assert.match(items(kept), /ポリシーの規則/); assert.match(items(kept), /作成時の設定: イメージ・通信のプリセット/); assert.match(items(kept), /ホームの外のパスは引き継がず/); assert.match(items(lost), /ボリューム以外/); assert.match(items(lost), /提案.*履歴/);
  recInput.value = 'wrong'; recForm.listeners.submit({preventDefault() {}}); await flush(); assert.equal(rec.requests.filter(r => r.path.endsWith('/recreate')).length, 0); assert.match(recStatus.textContent, /一致しません/);
  recInput.value = 'api'; recForm.listeners.submit({preventDefault() {}}); await flush();
  const recReq = rec.requests.find(r => r.path === '/api/sandboxes/api/recreate'); assert.deepEqual(JSON.parse(recReq.body), {confirm: 'api'}); assert.equal(recReq.headers['X-CSRF-Token'], 'csrf123');
  assert.equal(recPanel.hidden, false, 'the panel stays open to show the rules not carried over'); assert.match(recStatus.textContent, /作り直しました。.*allow_x_443 — refused <b>/);
  ok('recreating a sandbox requires typing the name, says what is kept and lost, and reports the rules not carried over');
  {
  // 最新のイメージを取得してから作り直す: the pull goes first; a failed one recreates nothing.
  const rp = await environment({fake: false, status: 'running'}); let phase = 'updated'; const order = [];
  rp.setImagesHandler((path, opt) => { if (opt?.method === 'POST') { order.push('pull'); return response({ok: true}); } return response({available: true, image: 'ghcr.io/mtsmfm/iwato-sandbox:edge', source: 'pull', moves: true, present: true, update: order.length ? {phase, message: phase === 'failed' ? 'pull <b>failed</b>' : '新しいイメージを取得しました'} : {}}); });
  rp.buttons()[7].click(); await flush();
  const rpPanel = rp.side('recreate-panel'); const [, , , , pullLabel, rpForm, rpStatus] = rpPanel.children;
  assert.equal(pullLabel.hidden, false); assert.equal(pullLabel.children[1].textContent, '最新のイメージを取得してから作り直す');
  phase = 'failed'; pullLabel.children[0].checked = true; rpForm.children[0].value = 'api'; rpForm.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(order, ['pull']); assert.equal(rp.requests.filter(r => r.path.endsWith('/recreate')).length, 0, 'a failed pull recreates nothing'); assert.match(rpStatus.textContent, /作り直していません。pull <b>failed<\/b>/);
  phase = 'updated'; rpForm.listeners.submit({preventDefault() {}}); await flush();
  const pullAt = rp.requests.findLastIndex(r => r.path === '/api/images/update'); const recAt = rp.requests.findIndex(r => r.path.endsWith('/recreate'));
  assert(pullAt >= 0 && recAt > pullAt, 'pulled before recreating'); assert.equal(rp.requests[pullAt].headers['X-CSRF-Token'], 'csrf123');
  ok('recreate can pull the latest image first, and stops when the pull fails');
  }
  const st = await environment({fake: false, status: 'running'}); const stopReqs = () => st.requests.filter(r => r.path.endsWith('/stop'));
  assert.equal(st.buttons()[1].textContent, '停止'); assert.equal(st.buttons()[1].disabled, false); st.buttons()[1].click(); await flush(); assert.equal(stopReqs().length, 0); assert.match(st.confirmations.at(-1), /起動」で再開/);
  st.setConfirm(true); st.buttons()[1].click(); st.buttons()[1].click(); await flush(); assert.equal(stopReqs().length, 1); assert.equal(stopReqs()[0].method, 'POST'); assert.equal(stopReqs()[0].headers['X-CSRF-Token'], 'csrf123');
  assert.equal(st.card().children[0].children[1].textContent, '停止中'); assert.equal(st.buttons()[1].disabled, true); assert.equal(st.buttons()[0].disabled, false);
  ok('stop asks for confirmation, sends one CSRF POST, and leaves the sandbox startable');
  const fold = await environment({fake: false}); assert.equal(fold.card().dataset.collapsed, 'true'); fold.setConfirm(true); fold.buttons()[0].click(); await flush(); assert.equal(fold.card().dataset.collapsed, 'false');
  ok('a sandbox that is not running folds to its toolbar and unfolds once started');

  const fresh = await environment({noSandboxes: true}); assert.equal(fresh.els['create-panel'].hidden, false); assert.match(fresh.els['empty-text'].textContent, /サンドボックスを作成/);
  fresh.els['create-name'].value = 'Bad ID'; fresh.els['create-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.equal(fresh.requests.filter(r => r.path === '/api/sandboxes' && r.method === 'POST').length, 0); assert.match(fresh.els['create-status'].textContent, /英小文字/);
  fresh.els['create-name'].value = ' web '; fresh.els['create-form'].listeners.submit({preventDefault() {}}); await flush();
  const createReq = fresh.requests.find(r => r.path === '/api/sandboxes' && r.method === 'POST'); assert.deepEqual(JSON.parse(createReq.body), {name: 'web', image: '', presets: [], providers: []}); assert.equal(createReq.headers['X-CSRF-Token'], 'csrf123');
  assert.equal(fresh.els['create-panel'].hidden, true); assert.equal(fresh.els['empty-state'].hidden, true); assert.equal(fresh.card().children[0].children[0].children[1].children[0].textContent, 'web');
  assert(fresh.requests.some((r) => r.path === '/api/sandboxes/web/start' && r.method === 'POST'), 'a new sandbox starts right away'); assert(fresh.sockets.some((ws) => /\/ws\/web/.test(ws.url)), 'and its terminal connects');
  ok('first sandbox is created explicitly from the UI with name validation and CSRF');
  fresh.els['create-name'].value = 'app'; fresh.els['create-form'].listeners.submit({preventDefault() {}}); await flush();
  const shown = fresh.els['terminal-grid'].children.filter((n) => n.className.split(' ').includes('selected'));
  assert.equal(shown.length, 1); assert.equal(shown[0].children[0].children[0].children[1].children[0].textContent, 'app');
  ok('a newly created sandbox is the one shown');

  const logs = await environment({fake: false}); assert.equal(logs.els['log-button'].hidden, false); logs.els['log-panel'].hidden = true;
  logs.els['log-button'].listeners.click(); await flush();
  assert.equal(logs.els['log-panel'].hidden, false); assert.equal(logs.els['log-text'].textContent, 'WSLc のドライバを起動しています…\nbuilding iwato-sandbox');
  const follow = [...logs.timeouts.values()].find((t) => t.ms === 2000); assert(follow, 'the open log view follows the log'); follow.fn(); await flush();
  assert(logs.requests.some((r) => r.path === '/api/log?after=2')); assert.match(logs.els['log-text'].textContent, /gateway: <b>ready<\/b>$/, 'log lines stay literal text');
  logs.els['log-close'].listeners.click(); assert.equal(logs.els['log-panel'].hidden, true);
  ok('the log view shows what Iwato logs and follows it while open');

  {
  const vol = await environment({fake: false}); assert.equal(vol.els['volume-button'].hidden, false);
  let vols = [{name: 'iwato-personal-api-persist-aaaaaaaa', sandbox: 'api', path: '/sandbox/work', usedBy: 'api', createdAt: '2026-10-01T02:03:04Z'},
    {name: 'iwato-personal-old-persist-bbbbbbbb', sandbox: 'old', path: '/sandbox/<b>data</b>', createdAt: '2026-09-01T00:00:00Z'},
    {name: 'iwato-personal-old-persist-cccccccc', sandbox: 'old', path: ''}];
  const removed = [];
  vol.setVolumeHandler((path, opt) => {
    if (opt?.method === 'POST') { const name = decodeURIComponent(path.split('/')[3]); removed.push({name, csrf: opt.headers['X-CSRF-Token']}); vols = vols.filter((v) => v.name !== name); return response({ok: true}); }
    return response({volumes: vols});
  });
  vol.els['volume-button'].listeners.click(); await flush();
  assert.equal(vol.els['volume-panel'].hidden, false); assert.equal(vol.els['volume-status'].textContent, '');
  const groups = vol.els['volume-list'].children; assert.equal(groups.length, 2, 'grouped by sandbox');
  const [api, old] = groups;
  assert.equal(api.children[0].textContent, 'api'); assert.equal(api.children[0].children.length, 0, 'a configured sandbox is not marked deleted');
  assert.equal(api.children[1].children[1].textContent, '使用中: api', 'a volume in use has no remove button');
  assert.match(api.children[1].children[0].children[1].textContent, /^iwato-personal-api-persist-aaaaaaaa · .*作成$/);
  assert.equal(old.children[0].children[0].textContent, '削除済み');
  assert.equal(old.children[1].children[0].children[0].textContent, '/sandbox/<b>data</b>', 'paths stay literal text');
  assert.equal(old.children[2].children[0].children[0].textContent, 'パス不明'); assert.match(old.children[2].children[0].children[0].className, /volume-unknown/);
  vol.setConfirm(false); old.children[2].children[1].click(); await flush();
  assert.equal(removed.length, 0, 'cancel removes nothing');
  assert.match(vol.confirmations.at(-1), /iwato-personal-old-persist-cccccccc \(old パス不明\).*元に戻せません/); assert.match(vol.els['confirm-ok'].className, /button-disconnect/);
  vol.setConfirm(true); old.children[1].children[1].click(); await flush();
  assert.deepEqual(removed, [{name: 'iwato-personal-old-persist-bbbbbbbb', csrf: 'csrf123'}]);
  assert.equal(vol.els['volume-list'].children[1].children.length, 2, 'the list is read again');
  vol.setVolumeHandler((path, opt) => opt?.method === 'POST' ? response({error: 'サンドボックス old が使っています。先にサンドボックスを削除してください'}, 409) : response({volumes: vols}));
  vol.els['volume-list'].children[1].children[1].children[1].click(); await flush();
  assert.match(vol.els['volume-status'].textContent, /^削除できませんでした: サンドボックス old が使っています/);
  vol.els['volume-close'].listeners.click(); assert.equal(vol.els['volume-panel'].hidden, true);
  ok('the volume panel lists by sandbox and removes a leftover one after a warning, with CSRF');

  vol.setVolumeHandler(() => response({volumes: vols.concat([{name: 'iwato-personal-old-persist-dddddddd', sandbox: 'old', path: '/sandbox/data'}])}));
  vol.els['create-button'].listeners.click(); await flush();
  vol.els['create-name'].value = 'old'; vol.els['create-name'].listeners.input(); 
  assert.equal(vol.els['create-volumes'].textContent, '「old」の残っているデータ: ホームの外のデータ (/sandbox/data, パス不明 1 件) は引き継がれず、ボリュームに残ります。');
  vol.els['create-name'].value = 'api'; vol.els['create-name'].listeners.input();
  assert.equal(vol.els['create-volumes'].textContent, '', 'volumes in use are not leftovers');
  ok('creating under a name with leftover volumes names their paths');
  }

  const sandbox = await environment({noSandboxes: true});
  sandbox.els['create-name'].value = 'box'; sandbox.els['create-image'].value = ' ubuntu:24.04 ';
  sandbox.els['create-form'].listeners.submit({preventDefault() {}}); await flush();
  const sandboxReq = sandbox.requests.find(r => r.path === '/api/sandboxes' && r.method === 'POST');
  assert.deepEqual(JSON.parse(sandboxReq.body), {name: 'box', image: 'ubuntu:24.04', presets: [], providers: []});
  assert.equal(sandbox.els['create-image'].value, '');
  ok('a sandbox is created with its image (no paths to persist: only the home does)');

  const noGpu = await environment({noSandboxes: true});
  noGpu.setSandboxHandler(() => response({fake: true, sandboxes: [], gpu: false})); await noGpu.tickPoll();
  assert.equal(noGpu.els['create-gpu-label'].hidden, true); assert.equal(noGpu.els['create-gpu-note'].hidden, true);
  noGpu.els['create-gpu'].checked = true; noGpu.els['create-name'].value = 'box'; noGpu.els['create-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.equal(JSON.parse(noGpu.requests.find(r => r.path === '/api/sandboxes' && r.method === 'POST').body).gpu, false);
  ok('the GPU choice is hidden, and not sent, where no GPU is available');

  const img = await environment({status: 'not-created'}); let imageState = {phase: 'building', current: 'dev', message: 'イメージをビルドしています (dev)…', log: 'C:\\iwato\\image-build.log'};
  img.setSandboxHandler(() => response({fake: false, sandboxes: [{...img.sandbox}], images: imageState})); await img.tickPoll();
  assert.equal(img.els['images-banner'].hidden, false); assert.match(img.els['images-text'].textContent, /dev/); assert.equal(img.buttons()[0].disabled, true); assert.equal(img.els['images-retry'].hidden, true);
  imageState = {phase: 'failed', message: 'build failed'}; await img.tickPoll(); assert.equal(img.els['images-retry'].hidden, false); assert.match(img.els['images-title'].textContent, /できませんでした/);
  imageState = {phase: 'ready'}; await img.tickPoll(); assert.equal(img.els['images-banner'].hidden, true); assert.equal(img.buttons()[0].disabled, false);
  ok('image preparation banner blocks start until ready and offers retry on failure');

  const eng = await environment({status: 'not-created'}); let engineCheck = {level: 'fail', name: 'docker', detail: 'docker_down_error', hint: 'docker_start', params: {error: 'Cannot connect <b>'}};
  eng.setSandboxHandler(() => response({fake: false, sandboxes: [{...eng.sandbox}], images: {phase: 'engine'}, engine: engineCheck})); await eng.tickPoll();
  assert.equal(eng.els['images-banner'].hidden, false); assert.equal(eng.els['images-title'].textContent, 'Docker を使えません');
  assert.equal(eng.els['images-text'].textContent, 'エンジンが動いていません: Cannot connect <b>\nColima なら `colima start`、Docker Desktop・OrbStack ならアプリを起動します。別のエンジンを使うときは `docker context use` で選びます\n使えるようになると、そのまま準備を始めます。');
  assert.equal(eng.els['images-recheck'].hidden, false); assert.equal(eng.els['images-retry'].hidden, true); assert.equal(eng.buttons()[0].disabled, true, 'no start without the engine');
  engineCheck = undefined; eng.els['images-recheck'].listeners.click(); await flush();
  assert(eng.requests.some(r => r.path === '/api/checks'), 'checks again on request');
  assert.match(eng.els['images-title'].textContent, /準備しています/); assert.equal(eng.els['images-recheck'].hidden, true);
  ok('without the engine the banner names the failed check with its hint and checks again on request');


  const leave = await environment({status: 'running'}); leave.buttons()[2].click(); await flush();
  const idle = {prevented: false, preventDefault() { this.prevented = true; }}; leave.win.listeners.beforeunload(idle); assert.equal(idle.prevented, false);
  leave.sockets.at(-1).open(); const connectedLeave = {prevented: false, preventDefault() { this.prevented = true; }}; leave.win.listeners.beforeunload(connectedLeave); assert.equal(connectedLeave.prevented, true);
  ok('closing the tab (e.g. Ctrl+W) asks for confirmation only while a terminal is connected');

  const kb = await environment({status: 'running'}); kb.buttons()[2].click(); await flush(); kb.sockets.at(-1).open();
  const kbSurface = kb.card().children[5], kbViewport = kb.win.visualViewport, shell = kb.els['app-shell'];
  assert(kbSurface.className.includes('terminal-surface'));
  kb.document.activeElement = kbSurface.children[0]; kbViewport.height = 450; kbViewport.offsetTop = 100; kbViewport.listeners.resize();
  assert.equal(shell.style.height, '450px'); assert.equal(shell.style.top, '100px'); assert(shell.className.includes('keyboard-open'));
  kbViewport.offsetTop = 40; kbViewport.listeners.scroll(); assert.equal(shell.style.top, '40px', 'follows the visible area as it pans');
  kbViewport.scale = 2; kbViewport.listeners.resize(); assert.equal(shell.style.height, '', 'pinch zoom is not a keyboard'); assert(!shell.className.includes('keyboard-open'));
  kbViewport.scale = 1; kb.document.activeElement = kb.els['refresh-button']; kbViewport.listeners.resize(); assert.equal(shell.style.height, '', 'only while typing in the terminal');
  kb.document.activeElement = kbSurface.children[0]; kbViewport.listeners.resize(); assert.equal(shell.style.height, '450px');
  kbViewport.height = 800; kbViewport.offsetTop = 0; kbViewport.listeners.resize(); assert.equal(shell.style.height, ''); assert.equal(shell.style.top, '');
  ok('the on-screen keyboard fits the page to the visible area and shows only the terminal and its keys, then restores');

  {
  // A phone: the terminal host is 640px high (40 rows of 16px) until the
  // keyboard leaves 300px for the terminal surface (12px + 7px padding).
  const rows = await environment({status: 'running'}); const surface = rows.card().children[5], host = surface.children[0], viewport = rows.win.visualViewport, shell = rows.els['app-shell'];
  Object.defineProperty(surface, 'clientHeight', {get: () => shell.style.height ? 300 : 659});
  Object.defineProperty(host, 'clientHeight', {get: () => parseFloat(host.style.height) || surface.clientHeight - 19});
  rows.win.fitRows = (h) => Math.floor(h.clientHeight / 16);
  rows.buttons()[2].click(); await flush(); const socket = rows.sockets.at(-1); socket.open(); await flush();
  const term = rows.terminals.at(-1); const resizes = () => socket.sent.filter((m) => m.type === 'resize' || m.type === 'hello');
  // Sizes sent to the PTY (the same size again changes nothing there).
  const sizes = () => [...new Set(resizes().map((m) => m.cols + 'x' + m.rows))];
  assert.deepEqual(resizes(), [{type: 'hello', cols: 80, rows: 40}]);
  // Claude Code: the cursor is in the input box, above its status lines.
  term.cursorY = 33;
  rows.document.activeElement = host; viewport.height = 450; viewport.offsetTop = 100; viewport.listeners.resize(); await flush();
  assert.equal(shell.style.height, '450px');
  assert.equal(term.rows, 40, 'the keyboard keeps the rows'); assert.deepEqual(sizes(), ['80x40'], 'no resize reaches the PTY: Claude Code would redraw and leave duplicated rows in the scrollback');
  assert.equal(host.style.height, '640px');
  assert.equal(host.style.transform, 'translateY(-359px)', 'the bottom rows (input box and status lines) show above the keyboard');
  rows.win.listeners.focus(); host.listeners.pointerdown?.(); await flush();
  assert.deepEqual(sizes(), ['80x40'], 'taking the size back while the keyboard is open keeps the rows too');
  term.cursorY = 20; term.cursorMove(); await flush();
  assert.equal(host.style.transform, 'translateY(-55px)', 'a cursor higher up stays visible: its row at the bottom');
  term.cursorY = 2; term.cursorMove(); await flush();
  assert.equal(host.style.transform, '', 'a cursor near the top: the top rows');
  viewport.height = 800; viewport.offsetTop = 0; viewport.listeners.resize(); await flush();
  assert.equal(host.style.height, ''); assert.equal(host.style.transform, '');
  assert.equal(term.rows, 40); assert.deepEqual(sizes(), ['80x40'], 'closing the keyboard sends no resize either');
  term.cursorMove(); await flush(); assert.equal(host.style.transform, '', 'without the keyboard the terminal is not moved');
  rows.win.fitRows = () => 30; host.listeners.pointerdown(); await flush();
  assert.deepEqual(resizes().at(-1), {type: 'resize', cols: 80, rows: 30}, 'other size changes still resize the PTY');
  ok('the on-screen keyboard keeps the terminal rows (no duplicated rows from a Claude Code redraw) and shows the cursor above it');
  }

  {
  // A phone whose host loses 4 rows for a moment (the keyboard or the
  // browser's bars moving): the PTY keeps its rows unless the change lasts.
  const brief = await environment({status: 'running', touch: true, fullscreen: false}); brief.win.fitRows = () => 37;
  brief.buttons()[2].click(); await flush(); const socket = brief.sockets.at(-1); socket.open(); await flush();
  const sizes = () => socket.sent.filter((m) => m.type === 'resize' || m.type === 'hello').map((m) => m.cols + 'x' + m.rows);
  const relayout = async (rows) => { brief.win.fitRows = () => rows; brief.document.listeners.fullscreenchange(); await flush(); };
  const waiting = () => [...brief.timeouts.entries()].filter(([, t]) => String(t.fn).includes('sendResize'));
  const settle = () => { const [entry] = waiting(); assert(entry, 'waits for the rows to last'); brief.timeouts.delete(entry[0]); entry[1].fn(); };
  await relayout(33); await relayout(37);
  assert.deepEqual(sizes(), ['80x37'], 'fewer rows for a moment do not reach the PTY');
  assert.equal(waiting().length, 0);
  await relayout(33); settle();
  assert.deepEqual(sizes(), ['80x37', '80x33'], 'fewer rows that last do');
  await relayout(37); assert.deepEqual(sizes(), ['80x37', '80x33', '80x37'], 'more rows go at once');
  ok('a touch screen sends fewer rows only once they last (a moment of fewer rows would duplicate rows in the scrollback)');
  }

  const two = await environment({fake: false, status: 'running'});
  two.setSandboxHandler(() => response({fake: false, sandboxes: ['api', 'web'].map((id) => ({name: id, status: 'running', detail: ''}))})); await two.tickPoll();
  const twoCards = () => two.els['terminal-grid'].children.filter((n) => n.className.split(' ').includes('terminal-card')); const isSelected = (n) => n.className.split(' ').includes('selected');
  assert.equal(twoCards().length, 2); assert.deepEqual(twoCards().map(isSelected), [true, false], 'one sandbox is shown at a time');
  const navItems = () => two.els['sandbox-nav'].children.map((n) => n.className === 'nav-separator' ? (n.hidden ? null : '|') : n.children[1].textContent).filter(Boolean);
  assert.deepEqual(navItems(), ['api', 'web'], 'no separator while every sandbox runs');
  two.els['sandbox-nav'].children.at(-2).click(); assert.deepEqual(twoCards().map(isSelected), [false, true], 'the sidebar switches it');
  two.setSandboxHandler(() => response({fake: false, sandboxes: [{name: 'api', status: 'stopped', detail: ''}, {name: 'web', status: 'running', detail: ''}]})); await two.tickPoll();
  assert.deepEqual(navItems(), ['web', '|', 'api'], 'running sandboxes first, the stopped one below a separator');
  two.setSandboxHandler(() => response({fake: false, sandboxes: [{name: 'api', status: 'running', detail: ''}]})); await two.tickPoll(); assert.deepEqual(twoCards().map(isSelected), [true], 'a removed selection falls back to the first');
  two.els['menu-button'].click(); assert(two.els.sidebar.className.includes('menu-open')); assert.equal(two.els['menu-button'].getAttribute('aria-expanded'), 'true');
  two.els['sidebar-actions'].listeners.click({target: two.els['pair-button']}); assert(!two.els.sidebar.className.includes('menu-open'), 'choosing an action closes the menu');
  ok('one sandbox at a time: the sidebar switches, a removed one falls back, and the phone menu closes after an action');
  const auto = await environment({fake: false, status: 'running'});
  auto.setSandboxHandler(() => response({fake: false, sandboxes: ['api', 'web'].map((id) => ({name: id, status: 'running', detail: ''}))})); await auto.tickPoll();
  const autoNav = (id) => auto.els['sandbox-nav'].children.find((n) => n.children[1]?.textContent === id);
  const wsOf = (socket) => socket.url.match(/\/ws\/(\w+)/)[1];
  assert.deepEqual(auto.sockets.map(wsOf), ['api'], 'the running sandbox shown at load connects');
  autoNav('web').click(); await flush(); assert.deepEqual(auto.sockets.map(wsOf), ['api', 'web'], 'showing another running one connects it');
  auto.sockets[1].close(1000); await flush(); await auto.tickPoll(); await auto.tickPoll(); assert.equal(auto.sockets.length, 2, 'a closed session is not reopened by polling');
  autoNav('api').click(); await flush(); assert.equal(auto.sockets.length, 2, 'an already connected sandbox is left as it is');
  autoNav('web').click(); await flush(); assert.equal(auto.sockets.length, 3, 'web reconnects when shown again');
  ok('showing a running sandbox connects it, once per showing');
  { const back = await environment({fake: false, status: 'running'}); const pending = () => [...back.timeouts.values()].filter((t) => t.ms > 1000 && t.ms < 31000 && t.ms !== 12000); // not the connect timeout
    const come = () => { back.document.hidden = false; back.document.listeners.visibilitychange(); back.win.listeners.focus(); back.win.listeners.online(); };
    back.sockets[0].open(); come(); await flush(); assert.equal(back.sockets.length, 1, 'a connected terminal is left as it is');
    for (let i = 0; i < 4; i++) { back.sockets.at(-1).close(1006); await back.tickReconnect(); } back.sockets.at(-1).close(1013);
    assert.equal(back.sockets.length, 5); assert(pending()[0].ms >= 16000, 'backing off');
    come(); await flush(); assert.equal(back.sockets.length, 6, 'coming back connects now, once for the burst of events'); assert.equal(pending().length, 0, 'and cancels the backoff');
    back.sockets.at(-1).close(1006); assert(pending()[0].ms < 1400, 'the next backoff starts over');
    come(); await flush(); back.sockets.at(-1).close(4500); come(); await flush(); assert.equal(back.sockets.length, 8, 'an attach that failed is tried again');
    back.sockets.at(-1).close(1000); come(); await flush(); assert.equal(back.sockets.length, 8, 'not after the process exited'); }
  ok('coming back to the page (shown, focused, online) connects the shown terminal now, but not after an exit');

  const desk = await environment({status: 'running'}); desk.buttons()[2].click(); await flush(); desk.sockets.at(-1).open();
  assert.equal(desk.terminals.at(-1).focusCount, 1, 'a mouse/keyboard device focuses the terminal on connect');
  const phone = await environment({status: 'running', touch: true, fullscreen: false}); phone.buttons()[2].click(); await flush(); phone.sockets.at(-1).open();
  assert.equal(phone.terminals.at(-1).focusCount, undefined, 'connecting must not pop up the on-screen keyboard');
  const fullscreenItem = (env) => { let found; const walk = (n) => { if (n.textContent === '全画面' && n.className?.includes('card-menu-item')) found = n; n.children.forEach(walk); }; walk(env.card().children[2]); return found; };
  assert.equal(fullscreenItem(phone).hidden, true, 'no fullscreen where the browser cannot'); assert.equal(fullscreenItem(desk).hidden, false, 'fullscreen sits in the ⋯ menu');
  assert.equal(phone.card().children[5].children[2].hidden, true, 'being connected needs no label'); assert.equal(phone.card().children[5].children[3].hidden, true, 'nor the size');
  phone.keys().find(b => b.textContent === '⇧Tab').click(); assert.deepEqual(phone.sockets.at(-1).sent.filter(m => m.type === 'input').map(m => m.data), ['\x1b[Z']);
  ok('touch screens: no automatic terminal focus, Shift+Tab key, and no fullscreen button where unsupported');

  {
  const old = await environment({status: 'running'}); await flush();
  assert.equal(old.card().children[4].hidden, true, 'no terminal tabs where the image has only main');
  let names = ['main', '2'];
  const multi = await environment(); multi.setTermHandler((path, opt) => {
    if (opt?.method === 'POST') { names = names.filter(n => !path.endsWith('/terminals/' + n + '/kill')); return response({ok: true}); }
    return response({terminals: names, multi: true});
  });
  multi.buttons()[0].click(); await flush();
  const termTabs = multi.card().children[4]; const tabs = () => termTabs.children; const tab = label => tabs().find(t => t.children?.[0]?.textContent === label);
  assert.equal(termTabs.hidden, false); assert.deepEqual(tabs().map(t => t.children.length ? t.children[0].textContent : t.textContent), ['1', '2', '＋']);
  assert.equal(tab('1').attributes['aria-selected'], 'true');
  multi.buttons()[2].click(); await flush(); assert.match(multi.sockets.at(-1).url, /[?&]term=main(&|$)/); assert.doesNotMatch(multi.sockets.at(-1).url, /new=/);
  multi.sockets.at(-1).open();
  tab('2').children[0].click(); await flush(); assert.equal(multi.sockets.at(-2).readyState, 3, 'the other session is detached'); assert.match(multi.sockets.at(-1).url, /term=2(&|$)/); assert.doesNotMatch(multi.sockets.at(-1).url, /new=/);
  tabs().at(-1).click(); await flush(); const created = multi.sockets.at(-1); assert.match(created.url, /term=3&new=1$/); assert.deepEqual(tabs().map(t => t.children[0]?.textContent), ['1', '2', '3', undefined]);
  names = ['main', '2', '3']; created.open(); created.receive('$ '); await flush(); assert.deepEqual(tabs().map(t => t.children[0]?.textContent), ['1', '2', '3', undefined]);
  created.close(1006); await flush(); await multi.tickReconnect(); assert.doesNotMatch(multi.sockets.at(-1).url, /new=/, 'a dropped connection does not start an ended session again');
  multi.sockets.at(-1).open();
  names = ['main', '2']; multi.sockets.at(-1).close(1000); await flush();
  assert.match(multi.sockets.at(-1).url, /term=2(&|$)/, 'after exit the neighbouring session is shown'); assert.equal(tab('2').attributes['aria-selected'], 'true'); assert.equal(tab('3'), undefined);
  multi.sockets.at(-1).open(); multi.setConfirm(true); tab('2').children[1].click(); await flush();
  const kill = multi.requests.find(r => r.path === '/api/sandboxes/api/terminals/2/kill'); assert.equal(kill.method, 'POST'); assert.equal(kill.headers['X-CSRF-Token'], 'csrf123');
  assert.match(multi.confirmations.at(-1), /ターミナル 2 を終了/); assert.match(multi.sockets.at(-1).url, /term=main(&|$)/); assert.deepEqual(tabs().map(t => t.children[0]?.textContent), ['1', undefined]);
  assert.deepEqual([...multi.stored.entries()], [['iwato-term:api', 'main']], 'only the shown terminal is stored');
  const opened = await environment({status: 'running', search: '?term=2&sandbox=api', terms: ['main', '2']}); await flush();
  assert.match(opened.sockets.at(-1)?.url || '', /term=2(&|$)/, 'a notification click opens and attaches its terminal'); assert.equal(opened.location.search, '', 'the request leaves the URL');
  opened.swListeners.message({data: {type: 'open', search: '?term=main&sandbox=api'}}); await flush(); assert.match(opened.sockets.at(-1).url, /term=main(&|$)/, 'an open page switches when the notification is clicked');
  opened.swListeners.message({data: {type: 'open', search: '?tab=policy&sandbox=api'}}); await flush(); assert.equal(opened.card().dataset.tab, 'policy');
  const debug = await environment({status: 'running', search: '?font=fallback&term=2&sandbox=api', terms: ['main', '2']}); await flush(); assert.equal(debug.location.search, '?font=fallback', 'debug options stay in the URL');
  const unknown = await environment({status: 'running', search: '?term=2&sandbox=other'}); await flush(); assert.equal(unknown.sockets.length, 1); assert.match(unknown.sockets[0].url, /\/ws\/api\?.*term=main/, 'an unknown sandbox attaches nothing; the shown one connects its own session');
  const again = await environment({savedTerm: '3'}); again.setTermHandler(() => response({terminals: ['main', '3'], multi: true}));
  again.buttons()[0].click(); await flush(); again.buttons()[2].click(); await flush();
  assert.match(again.sockets.at(-1).url, /term=3(&|$)/, 'reopening the page shows the terminal shown last');
  ok('terminal tabs: switch attaches another session, + creates one, exit and × end it and show a neighbour');
  {
  let names = ['main', '2'], outdated = ['2'];
  const up = await environment({savedTerm: '2'}); up.setTermHandler((path, opt) => {
    if (opt?.method === 'POST') { names = names.filter(n => !path.endsWith('/terminals/' + n + '/kill')); outdated = outdated.filter(n => !path.endsWith('/terminals/' + n + '/kill')); return response({ok: true}); }
    return response({terminals: names, outdated, multi: true});
  });
  up.buttons()[0].click(); await flush(); up.buttons()[2].click(); await flush();
  const notice = up.card().children[8], termTabs = up.card().children[4]; const tab = label => termTabs.children.find(t => t.children?.[0]?.textContent?.startsWith(label));
  assert.equal(notice.hidden, false, 'the shown session runs an older version'); assert.match(notice.children[0].textContent, /ターミナル 2 は古いバージョン/);
  assert.equal(tab('2').children[0].children.at(-1).textContent, '更新'); assert.equal(tab('1').children[0].children.length, 0);
  const before = up.sockets.at(-1); before.open();
  notice.children[1].click(); await flush(); assert.match(up.confirmations.at(-1), /ターミナル 2 を再起動/); assert.equal(up.requests.filter(r => r.path.endsWith('/kill')).length, 0, 'declined: nothing ends');
  up.setConfirm(true); notice.children[1].click(); await flush();
  const kill = up.requests.find(r => r.path === '/api/sandboxes/api/terminals/2/kill'); assert.equal(kill.method, 'POST'); assert.equal(kill.headers['X-CSRF-Token'], 'csrf123');
  assert.equal(before.readyState, 3); assert.match(up.sockets.at(-1).url, /term=2&new=1$/, 'the session starts again'); assert.equal(notice.hidden, true);
  names = ['main', '2']; up.sockets.at(-1).open(); up.sockets.at(-1).receive('$ '); await flush(); assert.equal(notice.hidden, true); assert.equal(tab('2').children[0].children.length, 0);
  tab('1').children[0].click(); await flush(); assert.equal(notice.hidden, true, 'an up-to-date session shows no notice');
  }
  {
  const prep = await environment({status: 'running', terms: ['main']}); let imageState = {phase: 'checking', current: 'runtime', message: 'イメージを確認しています…'};
  prep.setSandboxHandler(() => response({fake: false, sandboxes: [{...prep.sandbox}], images: imageState})); await prep.tickPoll();
  prep.setTermHandler(() => response({terminals: ['main'], outdated: ['main'], multi: true})); assert.equal(prep.card().children[8].hidden, true);
  imageState = {phase: 'ready'}; await prep.tickPoll(); await flush();
  assert.equal(prep.card().children[8].hidden, false, 'sessions are listed again once the images are ready');
  }
  ok('a session running an older version is marked, and its notice restarts it after a confirmation');
  }

  {
  // web/term.js's keydown. With a Japanese IME on, Windows reports the
  // arrows and Ctrl+letter as keyCode 229 ("Process") outside a composition.
  const sandbox = {window: {}, navigator: {platform: 'Win32'}, TextDecoder, TextEncoder};
  vm.runInNewContext(fs.readFileSync(root + 'term.js', 'utf8'), sandbox);
  const Term = sandbox.window.IwatoTerminal;
  const press = (init, {appCursor = false, composing = false} = {}) => {
    const sent = [];
    const term = Object.assign(Object.create(Term.prototype), {modes: {appCursor}, composing, typed: (text) => sent.push(text)});
    const event = {keyCode: 0, key: '', code: '', isComposing: false, shiftKey: false, altKey: false, ctrlKey: false, metaKey: false, prevented: false, preventDefault() { this.prevented = true; }, ...init};
    term.keydown(event);
    return {sent: sent.join(''), prevented: event.prevented};
  };
  const process = (code, more) => ({keyCode: 229, key: 'Process', code, ...more});
  assert.deepEqual(press({keyCode: 38, key: 'ArrowUp', code: 'ArrowUp'}), {sent: '\x1b[A', prevented: true});
  assert.deepEqual(press(process('ArrowUp')), {sent: '\x1b[A', prevented: true}, 'an arrow reported as 229 goes to the session');
  assert.deepEqual(press(process('ArrowDown')), {sent: '\x1b[B', prevented: true});
  assert.equal(press(process('ArrowUp'), {appCursor: true}).sent, '\x1bOA', 'in application cursor mode');
  assert.equal(press(process('ArrowLeft', {shiftKey: true})).sent, '\x1b[1;2D', 'with a modifier');
  assert.equal(press(process('KeyP', {ctrlKey: true})).sent, '\x10');
  assert.equal(press(process('KeyN', {ctrlKey: true})).sent, '\x0e');
  assert.deepEqual(press(process('KeyA')), {sent: '', prevented: false}, 'a letter starts a composition');
  assert.deepEqual(press(process('Enter')), {sent: '', prevented: false}, 'Enter may commit one');
  assert.deepEqual(press(process('ArrowDown', {isComposing: true})), {sent: '', prevented: false}, 'while composing the arrows choose candidates');
  assert.deepEqual(press(process('ArrowDown'), {composing: true}), {sent: '', prevented: false});
  ok('the arrows and Ctrl+letter go to the session when the IME reports them as keyCode 229');

  { // Pasting and dropping: text wins; files go to onFiles.
    const listening = () => ({listeners: {}, addEventListener(n, fn) { this.listeners[n] = fn; }});
    const pasted = [], filed = [];
    const term = Object.assign(Object.create(Term.prototype), {input: listening(), root: listening(), options: {onFiles: (f) => filed.push([...f].map(x => x.name))}, paste: (t) => pasted.push(t)});
    term.attachInput();
    const event = (init) => ({prevented: false, preventDefault() { this.prevented = true; }, ...init});
    const file = (name, type) => ({kind: 'file', getAsFile: () => ({name, type})});
    const office = event({clipboardData: {getData: (t) => t === 'text/plain' ? 'A1\tB1' : '', items: [file('cells.png', 'image/png')], files: []}});
    term.input.listeners.paste(office); assert.deepEqual(pasted, ['A1\tB1']); assert.equal(filed.length, 0, 'text and a picture of it: the text'); assert(office.prevented);
    const shot = event({clipboardData: {getData: () => '', items: [{kind: 'string'}, file('image.png', 'image/png')], files: []}});
    term.input.listeners.paste(shot); assert.deepEqual(filed, [['image.png']]); assert(shot.prevented);
    const nothing = event({clipboardData: {getData: () => '', items: [], files: []}}); term.input.listeners.paste(nothing); assert.equal(nothing.prevented, false);
    const over = event({dataTransfer: {types: ['Files'], dropEffect: 'none'}}); term.root.listeners.dragover(over); assert(over.prevented); assert.equal(over.dataTransfer.dropEffect, 'copy');
    const textDrag = event({dataTransfer: {types: ['text/plain']}}); term.root.listeners.dragover(textDrag); assert.equal(textDrag.prevented, false, 'dragged text is left to the browser');
    term.root.listeners.drop(event({dataTransfer: {types: ['Files'], items: [], files: [{name: 'a.png', type: 'image/png'}, {name: 'b.jpg', type: 'image/jpeg'}]}}));
    assert.deepEqual(filed.at(-1), ['a.png', 'b.jpg']); }
  ok('a paste with text sends the text; one with only files (a screenshot) and a drop of files give them to the page');

  // The text an IME is composing wraps like terminal output: it continues at
  // column 0 of the next row, a wide character never straddles the edge, and
  // the lines move up when they would fall below the screen.
  // Plain arrays: the ones from the vm context have another prototype.
  const flat = (lines) => [...lines].map((l) => [l.row, l.col, [...l.chars].map((c) => c.t).join('')]);
  const layout = Term.preeditLayout;
  assert.deepEqual(flat(layout('abc', 2, 0, 10, 5)), [[0, 2, 'abc']], 'fits in the row');
  assert.deepEqual(flat(layout('abcdef', 7, 1, 10, 5)), [[1, 7, 'abc'], [2, 0, 'def']], 'wraps at the edge');
  assert.deepEqual(flat(layout('あいう', 7, 0, 10, 5)), [[0, 7, 'あ'], [1, 0, 'いう']], 'a wide character wraps whole');
  assert.deepEqual(flat(layout('あ', 8, 0, 10, 5)), [[0, 8, 'あ']], 'a wide character fills the last two cells');
  assert.deepEqual(flat(layout('あ', 9, 0, 10, 5)), [[1, 0, 'あ']], 'and moves on when one cell is left');
  assert.deepEqual(flat(layout('あいうえお', 0, 0, 4, 5)), [[0, 0, 'あい'], [1, 0, 'うえ'], [2, 0, 'お']], 'many rows');
  assert.deepEqual(flat(layout('abcdef', 7, 4, 10, 5)), [[3, 7, 'abc'], [4, 0, 'def']], 'moved up from below the screen');
  assert.deepEqual(flat(layout('éx', 0, 0, 10, 5)), [[0, 0, 'éx']], 'a combining mark joins its character');
  assert.equal(JSON.stringify(layout('あa', 0, 0, 10, 5)[0].chars), '[{"t":"あ","w":2},{"t":"a","w":1}]', 'cells of a wide and a narrow character');
  assert.equal(layout('', 3, 3, 10, 5).length, 0, 'nothing to draw');
  ok('composing text wraps at the end of the row like terminal output');
  }

  {
  // Provider scopes: the simple editor, the YAML editor (custom, reset), the shared GitHub profile's note, custom profiles.
  const find = (node, pred) => { if (pred(node)) return node; for (const c of node.children) { const f = find(c, pred); if (f) return f; } return null; };
  const byText = (node, t) => find(node, (n) => n.tagName === 'button' && n.textContent === t);
  const profiles = [{id: 'iwato-github', label: 'GitHub', group: 'ソース管理', scoped: true, credentials: [{name: 'token', label: 'トークン'}]},
    {id: 'iwato-huggingface', label: 'Hugging Face', group: 'モデル配布', scoped: true, credentials: [{name: 'token', label: 'トークン'}]},
    {id: 'my-api', label: 'My API', group: 'カスタム', custom: true, credentials: [{name: 'api_key', label: 'API キー'}]}];
  let scope = {mode: 'repos', repos: ['o/r'], mutations: [], custom: false};
  const posts = [];
  let reply = null;
  const handler = (path, opt) => {
    if (opt?.method === 'POST') {
      posts.push({path, body: JSON.parse(opt.body)});
      if (reply) { const r = reply; reply = null; return r; }
      return response({ok: true, pending: ['api'], id: 'my-api'});
    }
    if (path.startsWith('/api/provider-profiles/')) return response({id: 'x', source: 'scope', base: 'iwato-github', yaml: 'id: iwato-github-for-github-work\n'});
    return response({profiles, providers: [{name: 'github-personal', profile: 'iwato-github', keys: ['GH_TOKEN'], attached: []},
      {name: 'github-work', profile: 'iwato-github-for-github-work', base: 'iwato-github', scope, keys: ['GH_TOKEN'], attached: ['api'], applying: [{sandbox: 'api', attach: true, scope: true, state: 'pending'}]},
      {name: 'huggingface', profile: 'iwato-huggingface', keys: ['HF_TOKEN'], attached: []},
      {name: 'hf', profile: 'iwato-huggingface-for-hf', base: 'iwato-huggingface', scope: {mode: 'read-only', modes: ['read-only', 'full'], repos: [], mutations: []}, keys: ['HF_TOKEN'], attached: ['api', 'dev']}]});
  };
  const pv = await environment({fake: false, status: 'running'}); pv.setProviderHandler(handler);
  pv.els['provider-button'].listeners.click(); await flush();
  const group = pv.els['provider-list'].children[0];
  const [, personal, , work, , access] = group.children;
  assert.match(personal.children[0].children[2].textContent, /範囲: 読み取りだけ \(範囲を決められるようになる前/);
  assert.equal(byText(personal, '範囲を変更'), null, 'the shared iwato-github provider has no scope editor');
  assert.equal(work.children[0].children[2].textContent, '範囲: 書き込み: o/r');
  assert.match(work.children[0].children[3].textContent, /api: 範囲の変更の反映待ち/);
  assert.equal(access.hidden, true); byText(work, '範囲を変更').click(); assert.equal(access.hidden, false);
  const radios = access.children[0].children.map((l) => l.children[0]);
  const repos = access.children[1], mutations = access.children[3];
  assert.deepEqual(radios.map((r) => r.checked), [false, true, false]); assert.equal(repos.value, 'o/r'); assert.equal(repos.disabled, false);
  radios[1].checked = false; radios[2].checked = true; radios[2].listeners.change();
  assert.equal(repos.disabled, true); assert.equal(mutations.disabled, true, 'full write takes no mutations');
  radios[2].checked = false; radios[1].checked = true; radios[1].listeners.change();
  repos.value = 'o/r\n a/b \n'; mutations.value = 'createPullRequest, addComment';
  access.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts[0], {path: '/api/providers/github-work/scope', body: {mode: 'repos', repos: ['o/r', 'a/b'], mutations: ['createPullRequest', 'addComment']}});
  assert.match(pv.els['provider-status'].textContent, /github-work の範囲を変更しました。.*\(api\).*反映待ち/);
  // A refusal stays in the form.
  const access2 = pv.els['provider-list'].children[0].children[5];
  reply = response({error: 'リポジトリは OWNER/REPO の形で'}, 400);
  access2.listeners.submit({preventDefault() {}}); await flush();
  assert.match(access2.children.at(-1).textContent, /変更できませんでした: リポジトリは OWNER\/REPO/);
  // The YAML editor (a textarea without CodeMirror): the profile, saved as the provider's.
  byText(access2, 'YAML で編集').click(); await flush();
  const box = pv.els['provider-yaml']; assert.equal(box.hidden, false);
  const area = box.children[2].children[0]; assert.equal(box.children[2].className, 'code-field provider-yaml-editor'); assert.equal(area.tagName, 'textarea'); assert.equal(area.value, 'id: iwato-github-for-github-work\n');
  area.value = 'id: iwato-github-for-github-work\n# mine\n';
  reply = response({error: 'credentials は iwato-github のままに'}, 400);
  byText(box, '保存').click(); await flush();
  assert.match(box.children.at(-1).textContent, /保存できませんでした: credentials は/); assert.equal(box.hidden, false, 'a refusal keeps the editor open');
  byText(box, '保存').click(); await flush();
  assert.deepEqual(posts.at(-1), {path: '/api/providers/github-work/scope', body: {yaml: 'id: iwato-github-for-github-work\n# mine\n'}}); assert.equal(box.hidden, true);
  // Custom: the simple editor is off, with a reset.
  scope = {mode: 'repos', repos: ['o/r'], mutations: [], custom: true};
  pv.els['provider-close'].listeners.click(); pv.els['provider-button'].listeners.click(); await flush();
  const custom = pv.els['provider-list'].children[0].children[5];
  assert.equal(pv.els['provider-list'].children[0].children[3].children[0].children[2].textContent, '範囲: YAML で編集したプロファイル');
  assert.equal(custom.children[1].children[0].children[0].disabled, true); assert.equal(find(custom, (n) => n.tagName === 'button' && n.type === 'submit').disabled, true);
  byText(custom, '簡単な設定に戻す').click(); await flush();
  assert.deepEqual(posts.at(-1).body, {reset: true});
  // Another type: read-only or writes allowed, where it applies.
  const hfGroup = pv.els['provider-list'].children[1];
  const [, legacyHF, , hf, , hfAccess] = hfGroup.children;
  assert.match(legacyHF.children[0].children[2].textContent, /範囲を決められるようになる前/); assert.equal(byText(legacyHF, '範囲を変更'), null);
  assert.equal(hf.children[0].children[2].textContent, '範囲: 読み取りだけ');
  const hfRadios = hfAccess.children[0].children;
  assert.deepEqual(hfRadios.map((l) => l.children[1].textContent), ['読み取りだけ', '書き込みも許可']);
  assert.equal(find(hfAccess, (n) => n.className === 'text-input provider-access-repos'), null, 'no repositories outside GitHub');
  assert.equal(hfAccess.children[1].textContent, '反映するサンドボックス: api, dev');
  hfRadios[0].children[0].checked = false; hfRadios[1].children[0].checked = true; hfRadios[1].children[0].listeners.change();
  hfAccess.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts.at(-1), {path: '/api/providers/hf/scope', body: {mode: 'full', repos: [], mutations: []}});
  ok('a provider\'s scope: the simple editor, inline refusals, the YAML editor, custom and reset, 反映待ち');

  // Custom profiles: added as YAML (refusals inline), edited from the selector.
  pv.els['provider-custom-add'].listeners.click();
  assert.match(box.children[2].children[0].value, /^id: my-api$/m); assert.doesNotMatch(box.children[2].children[0].value, /resolve:env/);
  reply = response({error: 'iwato- で始まる id は'}, 400);
  byText(box, '保存').click(); await flush();
  assert.match(box.children.at(-1).textContent, /保存できませんでした: iwato- で始まる/);
  byText(box, '保存').click(); await flush();
  assert.equal(posts.at(-1).path, '/api/provider-profiles'); assert.match(posts.at(-1).body.yaml, /display_name: My API/);
  assert.equal(pv.els['provider-profile'].value, 'my-api', 'the saved profile is selected');
  assert.equal(pv.els['provider-profile-yaml'].textContent, 'プロファイルを YAML で編集');
  pv.els['provider-profile-yaml'].listeners.click(); await flush();
  assert.equal(box.hidden, false); assert.ok(byText(box, 'プロファイルを削除'));
  ok('custom profiles are added and edited as YAML, refusals shown in the editor');
  }

  {
  // Providers: the global panel, the create form's picker, a card's attach/detach and the restart marks.
  const profiles = [{id: 'iwato-github', label: 'GitHub', group: 'ソース管理', note: 'GH_TOKEN と GITHUB_TOKEN', credentials: [{name: 'token', label: 'トークン'}]},
    {id: 'iwato-claude', label: 'Claude Code (セットアップトークン)', group: 'エージェント・推論', note: 'メモ: <b>x</b>', credentials: [{name: 'oauth_token', label: 'トークン (claude setup-token)'}]},
    {id: 'iwato-huggingface', label: 'Hugging Face', group: 'モデル配布', note: 'HF_TOKEN', hint: 'hf auth whoami <i>', credentials: [{name: 'token', label: 'アクセストークン'}]},
    {id: 'iwato-civitai', label: 'Civitai', group: 'モデル配布', note: 'CIVITAI_TOKEN', credentials: [{name: 'api_key', label: 'API キー'}]},
    {id: 'other-type', label: 'Other', note: '', credentials: []}];
  let provs = [{name: 'github-personal', profile: 'iwato-github', keys: ['GH_TOKEN', 'GITHUB_TOKEN'], attached: ['api']}, {name: 'github-work', profile: 'iwato-github', keys: ['GH_TOKEN', 'GITHUB_TOKEN'], attached: []},
    {name: 'claude', profile: 'iwato-claude', keys: ['CLAUDE_CODE_OAUTH_TOKEN'], attached: []}];
  const posts = [];
  let changeResult = {ok: true, restart: true};
  const handler = (path, opt) => {
    if (opt?.method === 'POST') {
      posts.push({path, body: JSON.parse(opt.body), csrf: opt.headers['X-CSRF-Token']});
      if (path.endsWith('/providers/attach')) { const n = JSON.parse(opt.body).provider; provs = provs.map((p) => p.name === n ? {...p, attached: [...p.attached, 'api']} : p); return response(changeResult); }
      if (path.endsWith('/providers/detach')) { const n = JSON.parse(opt.body).provider; provs = provs.map((p) => p.name === n ? {...p, attached: p.attached.filter((s) => s !== 'api')} : p); return response(changeResult); }
      if (path.endsWith('/delete')) { provs = provs.filter((p) => '/api/providers/' + p.name + '/delete' !== path); return response({ok: true}); }
      if (path === '/api/providers') { const b = JSON.parse(opt.body); provs = provs.concat([{name: b.name, profile: b.profile, keys: ['GH_TOKEN', 'GITHUB_TOKEN'], attached: []}]); }
      return response({ok: true});
    }
    return response({profiles, providers: provs});
  };
  const pv = await environment({fake: false, status: 'running'}); pv.setProviderHandler(handler);
  pv.setTermHandler(() => response({terminals: ['main', '2', '3'], multi: true, outdated: [], providerOutdated: pv.sandbox.restartPending ? ['main', '2'] : []}));
  assert.equal(pv.els['provider-button'].hidden, false);
  pv.els['provider-button'].listeners.click(); await flush();
  assert.equal(pv.els['provider-panel'].hidden, false); assert.equal(pv.els['provider-status'].textContent, '');
  const [ghGroup, clGroup] = pv.els['provider-list'].children;
  assert.equal(ghGroup.children[0].textContent, 'GitHub'); assert.equal(clGroup.children[0].children.length, 0, 'no marks on the profile');
  const personal = ghGroup.children[1];
  assert.equal(personal.children[0].children[0].textContent, 'github-personal'); assert.match(personal.children[0].children[1].textContent, /GH_TOKEN, GITHUB_TOKEN · 付けているサンドボックス: api/);
  assert.equal(personal.children[1].children[1].disabled, true, 'a provider in use cannot be deleted');
  // Create: one password input per credential; the value is sent once and cleared.
  assert.equal(pv.els['provider-profile'].value, 'iwato-github'); const valueInput = pv.els['provider-values'].children[0];
  assert.equal(valueInput.type, 'password'); assert.equal(valueInput.placeholder, 'トークン');
  pv.els['provider-name'].value = 'Bad'; pv.els['provider-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.equal(posts.length, 0); assert.match(pv.els['provider-form-status'].textContent, /英小文字/);
  pv.els['provider-name'].value = 'github-new'; valueInput.value = ' ghp_SECRET ';
  pv.els['provider-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts[0], {path: '/api/providers', body: {name: 'github-new', profile: 'iwato-github', values: {token: 'ghp_SECRET'}}, csrf: 'csrf123'});
  assert.equal(valueInput.value, '', 'the value is cleared'); assert.equal(pv.els['provider-name'].value, '');
  assert.equal(pv.els['provider-list'].children[0].children.length, 1 + 3 * 2, 'the list is read again (rows and their hidden edit forms)');
  // The type selector: the server's groups as optgroups, in its order.
  const kinds = pv.els['provider-profile'].children.map((g) => g.tagName === 'optgroup' ? g.label + ':' + g.children.map((o) => o.value).join('+') : g.value);
  assert.deepEqual(kinds, ['ソース管理:iwato-github', 'エージェント・推論:iwato-claude', 'モデル配布:iwato-huggingface+iwato-civitai', 'other-type']);
  assert.equal(pv.els['provider-profile'].children[1].children[0].textContent, 'Claude Code (セットアップトークン)');
  assert.equal(pv.els['provider-hint'].hidden, true, 'no hint for GitHub');
  pv.els['provider-profile'].value = 'iwato-huggingface'; pv.els['provider-profile'].listeners.change();
  assert.equal(pv.els['provider-hint'].hidden, false); assert.equal(pv.els['provider-hint'].textContent, 'hf auth whoami <i>', 'hints stay literal text');
  assert.equal(pv.els['provider-values'].children[0].placeholder, 'アクセストークン');
  pv.els['provider-profile'].value = 'iwato-github'; pv.els['provider-profile'].listeners.change();
  pv.els['provider-profile'].value = 'iwato-claude'; pv.els['provider-profile'].listeners.change();
  assert.equal(pv.els['provider-note'].textContent, 'メモ: <b>x</b>', 'notes stay literal text');
  // Update: only the values.
  const work = pv.els['provider-list'].children[0].children[3], workEdit = pv.els['provider-list'].children[0].children[4];
  assert.equal(work.children[0].children[0].textContent, 'github-work'); assert.equal(workEdit.hidden, true);
  work.children[1].children[0].click(); assert.equal(workEdit.hidden, false);
  workEdit.children[0].value = 'ghp_OTHER'; workEdit.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts[1], {path: '/api/providers/github-work/update', body: {values: {token: 'ghp_OTHER'}}, csrf: 'csrf123'});
  assert.equal(workEdit.children[0].value, ''); assert.equal(workEdit.hidden, true); assert.match(pv.els['provider-status'].textContent, /github-work の値を変更しました/);
  pv.setConfirm(true); work.children[1].children[1].click(); await flush();
  assert.match(pv.confirmations.at(-1), /github-work \(GitHub\) を削除します/); assert.equal(posts[2].path, '/api/providers/github-work/delete');
  pv.els['provider-close'].listeners.click(); assert.equal(pv.els['provider-panel'].hidden, true);
  ok('the provider panel lists without values, adds with a password input, changes values and deletes');

  // The create form: one select per profile.
  pv.els['create-button'].listeners.click(); await flush();
  const picker = pv.els['create-providers']; assert.equal(picker.hidden, false);
  const selects = picker.children.slice(1).map((label) => label.children[1]);
  assert.deepEqual(selects.map((s) => s.dataset.profile), ['iwato-github', 'iwato-claude']);
  assert.deepEqual(selects[0].children.map((o) => o.value), ['', 'github-personal', 'github-new'], 'at most one GitHub provider: a select');
  selects[0].value = 'github-new'; selects[1].value = 'claude';
  pv.els['create-name'].value = 'box'; pv.els['create-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(pv.requests.filter((r) => r.path === '/api/sandboxes' && r.method === 'POST').map((r) => JSON.parse(r.body).providers), [['github-new', 'claude']]);
  ok('the create form picks at most one provider per profile');

  // A card: attach, then restart now or later.
  const menuItem = (card, key) => { let found; const walk = (n) => { if (n.dataset?.action === key) found = n; n.children.forEach(walk); }; walk(card); return found; };
  menuItem(pv.card(), 'providers').click(); await flush();
  const panel = pv.side('provider-panel'); assert.equal(panel.hidden, false);
  const [, note, restartBox, , status, list] = panel.children;
  assert.match(note.textContent, /再起動します/);
  const ghRows = list.children[0].children.slice(1);
  assert.equal(ghRows[0].dataset.attached, 'true'); assert.equal(ghRows[0].children[1].textContent, '外す');
  assert.equal(ghRows[1].children[1].textContent, '付ける'); assert.equal(ghRows[1].children[1].disabled, true, 'a second GitHub provider cannot be attached');
  assert.match(ghRows[1].children[0].children[1].textContent, /github-personal を外すと付けられます/);
  pv.sandbox.restartPending = true;
  list.children[1].children[1].children[1].click(); await flush();
  assert.deepEqual(posts.at(-1), {path: '/api/sandboxes/api/providers/attach', body: {provider: 'claude'}, csrf: 'csrf123'});
  assert.equal(restartBox.hidden, false, 'a change on a running sandbox offers a restart'); assert.match(status.textContent, /claude を付けました/);
  assert.equal(pv.card().children[0].children[2].hidden, false, 'the card shows 再起動待ち'); assert.equal(pv.card().children[0].children[2].textContent, '再起動待ち');
  restartBox.children[1].children[1].click(); assert.equal(restartBox.hidden, true, 'あとで keeps the mark only');
  assert.equal(pv.card().children[0].children[2].hidden, false);
  assert.equal(pv.requests.filter((r) => /\/(stop|start)$/.test(r.path)).length, 0, 'nothing restarted');
  ok('attaching on a running sandbox offers a restart; あとで leaves the card marked 再起動待ち');

  // Terminals from before the change are marked; main's notice restarts the sandbox.
  const tabs = pv.card().children[4].children;
  assert.deepEqual(tabs.slice(0, 3).map((t) => t.children[0].children.map((c) => c.textContent)), [['再起動待ち'], ['再起動待ち'], []]);
  const notice = pv.card().children[8]; assert.equal(notice.hidden, false); assert.equal(notice.dataset.kind, 'provider');
  assert.match(notice.children[0].textContent, /サンドボックスを再起動すると反映/); assert.equal(notice.children[1].textContent, 'サンドボックスを再起動');
  pv.setConfirm(true); notice.children[1].click(); await flush();
  assert.match(pv.confirmations.at(-1), /「api」を再起動しますか/);
  const lifecycle = pv.requests.filter((r) => /\/(stop|start)$/.test(r.path)).map((r) => r.path);
  assert.deepEqual(lifecycle, ['/api/sandboxes/api/stop', '/api/sandboxes/api/start'], 'restart is stop, then start');
  ok('terminals from before a provider change are marked 再起動待ち; main restarts the sandbox');

  // 今すぐ再起動 right after a change, and the mark goes once it is restarted.
  pv.sandbox.restartPending = true; pv.sandbox.status = 'running'; changeResult = {ok: true, restart: true};
  assert.equal(pv.side('provider-panel').hidden, false, 'the panel stays open');
  pv.side('provider-panel').children[5].children[1].children[1].children[1].click(); await flush();
  assert.deepEqual(posts.at(-1).path, '/api/sandboxes/api/providers/detach');
  const box = pv.side('provider-panel').children[2]; assert.equal(box.hidden, false);
  pv.setSandboxHandler(() => response({fake: false, sandboxes: [{name: 'api', status: pv.sandbox.status, detail: ''}], server: {version: '0.3.0'}}));
  box.children[1].children[0].click(); await flush();
  assert.deepEqual(pv.requests.filter((r) => /\/(stop|start)$/.test(r.path)).map((r) => r.path).slice(2), ['/api/sandboxes/api/stop', '/api/sandboxes/api/start'], 'no second confirmation: the owner chose it');
  assert.equal(pv.card().children[0].children[2].hidden, true, 'the mark is gone after the restart');
  ok('今すぐ再起動 restarts the sandbox and clears 再起動待ち');
  }

  {
  // OpenShell refused the configuration: the start ends with its reason, and the card shows it whole.
  const reason = "OpenShell がサンドボックスの設定を受け付けません: credentialed endpoint 'api.anthropic.com:443' in rule 'allow_api_anthropic_com_443' uses L4-only";
  const ff = await environment({fake: false, status: 'stopped'});
  ff.setStartHandler(() => { ff.sandbox.status = 'error'; ff.sandbox.detail = reason; return response({error: reason}, 500); });
  ff.buttons()[0].click(); await flush();
  assert.match(ff.card().children[7].children[0].textContent, /^起動できませんでした: OpenShell がサンドボックスの設定を受け付けません/);
  assert.equal(ff.card().children[1].textContent, reason, 'the card shows the reason once the start failed');
  assert.equal(ff.card().children[1].dataset.state, 'error');
  assert.equal(ff.card().children[0].children[1].textContent, 'エラー');
  assert.equal(ff.buttons()[0].disabled, false, 'the owner can start again after fixing it');
  ok('a start OpenShell refuses ends with its reason, shown on the card');
  }

  {
  // Another page's start: the server reports it; this page shows 起動中 and can give it up.
  const other = await environment({fake: false, status: 'starting'});
  other.sandbox.detail = 'Pulling image';
  await other.tickPoll();
  let [st, stop] = other.buttons();
  assert.equal(other.card().children[0].children[1].textContent, '起動中');
  assert.equal(st.disabled, true); assert.equal(st.textContent, '起動中…');
  assert.equal(stop.disabled, false); assert.equal(stop.textContent, '起動を中止');
  assert.equal(other.card().children[1].textContent, 'Pulling image');
  other.setStopHandler(() => response({ok: true, canceled: true, detail: '起動を待つのをやめました。OpenShell は準備中のサンドボックスを止められません。'}));
  other.setConfirm(true); stop.click(); await flush();
  assert.match(other.confirmations.at(-1), /起動を中止しますか/);
  assert(other.requests.some((r) => r.path === '/api/sandboxes/api/stop' && r.method === 'POST'));
  assert.match(other.card().children[7].children[0].textContent, /^起動を待つのをやめました/);
  ok('a start in progress elsewhere shows 起動中; 起動を中止 gives it up');
  }

  {
  // This page's start, given up: the start's own error is not shown.
  const own = await environment({fake: false, status: 'stopped'});
  const pending = deferred();
  own.setStartHandler(() => pending.promise);
  own.setStopHandler(() => { pending.resolve(response({error: '起動を待つのをやめました'}, 500)); return response({ok: true, canceled: true, detail: '起動を待つのをやめました。'}); });
  const [st, stop] = own.buttons();
  st.click(); await flush();
  assert.equal(stop.disabled, false); assert.equal(stop.textContent, '起動を中止'); assert.equal(own.buttons()[4].disabled, false, 'delete stays available');
  own.setConfirm(true); stop.click(); await flush();
  assert.equal(own.card().children[7].children[0].textContent, '起動を待つのをやめました。');
  assert.equal(st.disabled, false); assert.equal(st.textContent, '起動');
  ok('giving up this page\'s own start shows no start error');
  }

  {
  // The three lists, all from OpenShell: waiting (source, count, times,
  // intent), the effective policy (a provider's rule read-only, rules from
  // proposals undone proposal by proposal, by hand taken out), an approved
  // proposal without its rule, and the rejected ones (newest 10, then more).
  const nodes = (n) => [n, ...n.children.flatMap(nodes)];
  const texts = (n) => nodes(n).map((x) => x.textContent).filter(Boolean).join('\n');
  const find = (n, label) => nodes(n).find((x) => x.tagName === 'button' && (x.textContent === label || x.attributes['aria-label'] === label));
  const id = (c) => c.repeat(32);
  const now = Math.floor(Date.now() / 1000);
  const rejected = Array.from({length: 12}, (_, i) => ({id: String(i + 10).repeat(16), digest: String(i % 10).repeat(64), state: 'rejected', source: 'auto', summary: 'r' + i, binaries: ['/usr/bin/curl'], endpoints: [{host: 'r' + i + '.example', ports: [443]}], decidedAt: now - 3600 * (i + 1), lastSeen: now - 60, hits: 3, hitsAfter: i === 0 ? 2 : -1, seenAfter: i === 0, auto: i === 1 ? 'covered' : ''}));
  const data = {available: true, changes: [],
    pending: [{id: id('a'), state: 'pending-approval', source: 'agent', intent: 'clone <b>iwato</b>', summary: 's', rule: 'allow_github_com_443', hits: 3, firstSeen: now - 240, lastSeen: now - 60, binaries: ['/usr/lib/git-core/git-remote-http'], endpoints: [{host: 'github.com', ports: [443], protocol: 'rest'}], rules: [{method: 'GET', path: '/'}]},
      {id: id('b'), state: 'pending-approval', source: 'auto', intent: 'ignored? no: shown only for agents', summary: 's2', hits: 37, firstSeen: now - 9000, lastSeen: now - 30, binaries: ['/sandbox/claude'], endpoints: [{host: 'http-intake.logs.example', ports: [443]}]}],
    rejected,
    unlinked: [{id: id('c'), state: 'approved', source: 'auto', summary: 'm', rule: 'allow_mise', binaries: ['/usr/local/bin/mise'], endpoints: [{host: 'mise.jdx.dev', ports: [443]}], decidedAt: now - 86400 * 3, ruleExists: false}],
    policy: {version: 23, active: 23, loaded: true, rules: [
      {name: 'allow_api', source: 'proposal', binaries: ['/usr/bin/gh', '/usr/bin/git'], endpoints: [{host: 'api.github.com', ports: [443]}],
        chunks: [{id: id('d'), binary: '/usr/bin/gh', binaries: ['/usr/bin/gh'], source: 'agent', intent: 'PR を見る', decidedAt: now - 86400 * 2, removesRule: false}, {id: id('e'), binary: '/usr/bin/git', binaries: ['/usr/bin/git'], source: 'auto', decidedAt: now - 86400, removesRule: false}]},
      {name: 'allow_npm', source: 'proposal', binaries: ['/n/*/bin/node'], endpoints: [{host: 'registry.npmjs.org', ports: [443]}, {host: 'npm.pkg.github.com', ports: [443]}],
        chunks: [{id: id('f'), binary: '/n/22/bin/node', binaries: ['/n/*/bin/node'], widened: true, source: 'auto', decidedAt: now - 300, removesRule: true, drops: ['npm.pkg.github.com:443']}]},
      {name: 'allow_pypi', source: 'hand', binaries: ['/usr/bin/python3.*'], endpoints: [{host: 'pypi.org', ports: [443]}]},
      {name: '_provider_github_personal', source: 'provider', provider: {name: 'github-personal', profile: 'iwato-github', label: 'GitHub'}, binaries: ['/usr/bin/gh', '/usr/bin/git'], endpoints: [{host: 'github.com', ports: [443], path: '/**', inspect: 'rest', enforcement: 'enforce', rules: [{method: 'GET', path: '**'}]}, {host: 'api.github.com', ports: [443]}]}]}};
  const lists = await environment({fake: false, status: 'running'});
  lists.setProposalHandler((path) => /\/proposals\/[a-z]+$/.test(path) ? response({ok: true}) : response(data));
  await lists.tickPoll();
  const [pend, cur, rej] = [0, 1, 3].map((i) => lists.policy().children[1].children[i]);
  const items = pend.children.filter((n) => n.className === 'proposal-item');
  assert.match(texts(pend), /^承認待ち \(2\)/);
  assert.match(texts(items[0]), /github\.com:443 GET \//); assert.match(texts(items[0]), / ← \/usr\/lib\/git-core\/git-remote-http/);
  assert.match(texts(items[0]), /エージェントの提案/); assert.match(texts(items[0]), /3 回 · 初回 \d\d:\d\d · 最後 \d\d:\d\d · 規則 allow_github_com_443/); assert.match(texts(items[0]), /「clone <b>iwato<\/b>」/);
  assert.match(texts(items[1]), /OpenShell の自動提案/); assert.match(texts(items[1]), /37 回/);
  ok('waiting proposals show the source, OpenShell\'s count and times, and the agent\'s intent as literal text');
  const rule = (name) => cur.children.find((n) => n.className === 'policy-rule' && n.children[0].children[0].children[0].textContent === name);
  // A provider's rule: labelled, read-only.
  const prov = rule('_provider_github_personal');
  assert.match(texts(prov), /プロバイダー github-personal \(GitHub\)/); assert.match(texts(prov), /外すにはプロバイダーを外す/); assert.match(texts(prov), /github\.com:443 \/\*\* · 検査 \(遮断\)/);
  assert(!nodes(prov).some((n) => n.tagName === 'button' && !['開く'].includes(n.textContent)), 'a provider rule has no edit buttons');
  // From proposals: no 外す, one 取り消す per proposal; the glob the approval widened to.
  const api = rule('allow_api');
  assert.equal(find(api, '外す'), undefined); assert.equal(find(api, '規則「allow_api」を外す'), undefined);
  assert.match(texts(api), /提案から\n\/usr\/bin\/gh 「PR を見る」 · \d\d\/\d\d \d\d:\d\d 承認/);
  assert.equal(find(api, 'プログラム /usr/bin/gh を外す'), undefined, 'a proposal\'s program comes out with 取り消す only');
  assert.match(texts(rule('allow_npm')), /\/n\/22\/bin\/node を \/n\/\*\/bin\/node に広げて承認/);
  assert(find(rule('allow_pypi'), '規則「allow_pypi」を外す') && /手で追加/.test(texts(rule('allow_pypi'))), 'by hand: 外す as before');
  lists.setConfirm(false); find(api, '提案 /usr/bin/gh を取り消す').click(); await flush();
  assert.equal(lists.requests.filter((r) => r.path.endsWith('/proposals/undo')).length, 0, 'undoing asks first');
  assert.match(lists.confirmations.at(-1), /承認待ちに戻り、規則からプログラム \/usr\/bin\/gh が外れます/); assert.doesNotMatch(lists.confirmations.at(-1), /規則ごと/);
  lists.setConfirm(true);
  find(rule('allow_npm'), '提案 /n/22/bin/node を取り消す').click(); await flush();
  const asked = lists.confirmations.at(-1);
  assert.match(asked, /広げたプログラム \(\/n\/\*\/bin\/node\) も外します/); assert.match(asked, /規則ごと消えます/); assert.match(asked, /一緒に消えます: npm\.pkg\.github\.com:443/);
  const undo = lists.requests.filter((r) => r.path === '/api/sandboxes/api/proposals/undo');
  assert.equal(undo.length, 1); assert.deepEqual(JSON.parse(undo[0].body), {requestId: id('f')}); assert.equal(undo[0].headers['X-CSRF-Token'], 'csrf123');
  assert.match(lists.policy().children[0].children[3].textContent, /取り消しました。提案は承認待ちに戻りました/);
  ok('the policy labels provider rules read-only, undoes a proposal (listing the endpoints that go with the rule), and takes out rules by hand');
  // Approved without its rule: put back and undone.
  const cur2 = lists.policy().children[1].children[1];
  const unl = cur2.children.find((n) => n.className === 'policy-rule policy-unlinked');
  assert.match(texts(unl), /allow_mise — 規則がありません/); assert.match(texts(unl), /このままだと同じ通信の提案は OpenShell から出ません/);
  find(unl, '規則を戻して取り消す').click(); await flush();
  assert.match(lists.confirmations.at(-1), /規則「allow_mise」を戻してから取り消し/);
  assert.deepEqual(JSON.parse(lists.requests.filter((r) => r.path === '/api/sandboxes/api/proposals/restore')[0].body), {requestId: id('c')});
  assert.match(texts(lists.policy().children[1].children[1]), /規則なし 1/, 'the folded header counts it');
  ok('an approved proposal without its rule offers to put the rule back and undo');
  // Rejected: newest 10, then the rest; hits since; OpenShell's own.
  const rejItems = () => lists.policy().children[1].children[3].children.filter((n) => n.className === 'proposal-item');
  assert.equal(rejItems().length, 10); assert.match(texts(lists.policy().children[1].children[3]), /^却下した提案 \(12\)/);
  assert.match(texts(rejItems()[0]), /に却下 · 却下後も 2 回、最後は \d\d:\d\d/); assert.match(texts(rejItems()[1]), /OpenShell が自動で却下 \(承認済みの提案と同じ宛先\)/); assert.doesNotMatch(texts(rejItems()[2]), /却下後も/);
  find(lists.policy().children[1].children[3], 'もっと見る (ほか 2 件)').click(); await flush();
  assert.equal(rejItems().length, 12);
  let formShown = null;
  lists.setConfirmHandler((q) => { formShown = lists.els['approve-form'].hidden; return /提案どおり/.test(q); });
  find(rejItems()[0], '承認する').click(); await flush();
  assert.equal(formShown, true, 'a rejected proposal is approved as proposed: no narrowing form');
  const appr = lists.requests.filter((r) => r.path === '/api/sandboxes/api/proposals/approve');
  assert.deepEqual(JSON.parse(appr.at(-1).body), {requestId: rejected[0].id, digest: rejected[0].digest});
  ok('the rejected list shows the newest 10 then more, the hits since, OpenShell\'s own rejections, and approves as proposed');
  }

  {
  // A stopped (or failed) sandbox keeps its policy tab: the policy can be fixed before starting.
  const pol = await environment({fake: false, status: 'stopped'});
  pol.setProposalHandler(() => response({available: true, pending: [], recent: [], policy: {version: 3, loaded: true, rules: [{name: 'allow_api_anthropic_com_443', binaries: [], endpoints: [{host: 'api.anthropic.com', ports: [443]}]}]}}));
  assert.equal(pol.card().dataset.created, 'true'); assert.equal(pol.card().dataset.collapsed, 'true');
  const tabs = pol.card().children[2].children[0].children;
  assert.equal(tabs[1].disabled, false); assert.equal(nodes(pol.card()).find((n) => n.dataset?.view === 'links').disabled, true, 'links need a running terminal');
  tabs[1].click(); await flush();
  assert.equal(pol.card().dataset.tab, 'policy');
  await pol.tickPoll();
  assert.equal(pol.card().dataset.tab, 'policy', 'a refresh keeps the policy tab of a stopped sandbox');
  assert.match(pol.policy().children[0].children[1].textContent, /新しい提案は届きません。ポリシーの確認と編集はできます/);
  assert(JSON.stringify(pol.policy().children[1].children[1].children.map(function walk(n) { return [n.textContent, n.children.map(walk)]; })).includes('allow_api_anthropic_com_443'), 'the current policy shows');
  pol.sandbox.status = 'error'; await pol.tickPoll();
  assert.equal(pol.card().dataset.tab, 'policy', 'and of a failed one');
  pol.sandbox.status = 'not-created'; await pol.tickPoll();
  assert.equal(pol.card().dataset.created, 'false'); assert.equal(pol.card().dataset.tab, 'terminal', 'not made yet: no policy tab');
  ok('the policy tab stays for a sandbox that exists but is not running');
  }

  {
  // An approval or edit shows 反映待ち until the sandbox loads its revision, then 反映済み.
  const ap = await environment({fake: false, status: 'running'});
  const nodes = (n) => [n, ...n.children.flatMap(nodes)];
  let policy = {version: 6, active: 4, loaded: false, rules: [{name: 'github', binaries: ['/usr/bin/git'], endpoints: [{host: 'github.com', ports: [443]}]}]};
  const changes = [{version: 5, kind: 'approve', rule: 'github', summary: '/usr/bin/git → github.com:443'}, {version: 6, kind: 'edit', rule: 'gone'}];
  ap.setProposalHandler(() => response({available: true, pending: [], recent: [{decision: 'approved', host: 'github.com', summary: '/usr/bin/git → github.com:443', at: 9}], policy, changes}));
  const badges = () => { const all = []; const walk = (n) => { if (n.className === 'apply-badge') all.push(n); n.children.forEach(walk); }; walk(ap.policy()); return all; };
  ap.card().children[2].children[0].children[1].click(); await flush();
  assert.deepEqual(badges().map((b) => [b.parent.className, b.textContent, b.dataset.state]), [
    ['panel-meta', '変更済み · 反映待ち', 'pending'], ['policy-rule-title', '承認済み · 反映待ち', 'pending']]);
  assert.equal(nodes(ap.policy()).find((n) => n.className === 'panel-meta policy-counts').textContent, '反映待ち 2', 'the folded list says what waits');
  const live = () => ap.intervals.filter((i) => !i.cleared && i.ms === 2000).map((i) => i.ms);
  assert.deepEqual(live(), [2000], 'the tab polls every 2 seconds while a change waits');
  policy = {...policy, active: 6, loaded: true};
  await ap.intervals.find((i) => !i.cleared && i.ms === 2000).fn(); await flush();
  assert.deepEqual(badges().map((b) => [b.textContent, b.dataset.state]), [['反映済み', 'applied'], ['反映済み', 'applied']]);
  assert.deepEqual(live(), [], 'and with the sandbox list once it is loaded');
  policy = {...policy, active: 4, loaded: false, loadError: 'invalid rule'};
  await ap.tickPoll();
  assert.equal(badges()[0].textContent, '反映できませんでした: invalid rule'); assert.equal(badges()[0].dataset.state, 'failed');
  policy = {...policy, loadError: ''}; ap.sandbox.status = 'stopped';
  await ap.tickPoll();
  assert.equal(badges()[1].textContent, '承認済み · 起動時に反映'); assert.match(texts(ap.policy().children[1].children[1]), /版 6 · 起動時に反映/);
  assert.deepEqual(live(), [], 'a stopped sandbox loads it when it starts: no faster polling');
  ok('a change shows 反映待ち until the sandbox loads it, then 反映済み; 起動時に反映 while stopped; the load error');
  }

  {
  // Atlassian: connected by OAuth (no value), the gateway's refresh state, 再接続, the flow with a pasted callback.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const byText = (root, t) => walk(root, (n) => n.textContent === t)[0];
  const profiles = [{id: 'iwato-github', label: 'GitHub', note: '', credentials: [{name: 'token', label: 'トークン'}]},
    {id: 'iwato-atlassian', label: 'Atlassian (MCP)', note: 'ATLASSIAN_ACCESS_TOKEN', oauth: true, guide: "claude mcp add --transport http atlassian https://mcp.atlassian.com/v2/mcp --header 'Authorization: Bearer ${ATLASSIAN_ACCESS_TOKEN}'", credentials: [{name: 'access_token', label: 'x'}]}];
  const provs = [{name: 'atlassian-work', profile: 'iwato-atlassian', keys: ['ATLASSIAN_ACCESS_TOKEN'], attached: ['api'], refresh: {status: 'refreshed', expiresAt: '2026-10-08T05:00:00Z', nextRefreshAt: '2026-10-08T04:55:00Z'}},
    {name: 'atlassian-oss', profile: 'iwato-atlassian', keys: ['ATLASSIAN_ACCESS_TOKEN'], attached: [], refresh: {status: 'reauthorization_required', recovery: 'reauthorize', failureCode: 'oauth_invalid_grant'}}];
  const posts = []; let flow = null; let authorizeURL = 'https://auth.atlassian.com/authorize?client_id=c&state=s';
  const handler = (path, opt) => {
    if (path === '/api/oauth/atlassian') return response({scopes: [{name: 'offline_access', default: true}, {name: 'read:me', default: true}, {name: 'read:rovo:agent-interface', default: false}]});
    if (opt?.method === 'POST') {
      const body = JSON.parse(opt.body); posts.push({path, body, csrf: opt.headers['X-CSRF-Token']});
      if (path === '/api/oauth/atlassian/start') { flow = {id: 'f'.repeat(32), name: body.name, status: 'waiting', authorizeURL, redirectURI: 'http://127.0.0.1:50123/callback', guide: profiles[1].guide}; return response(flow); }
      if (path.endsWith('/callback')) { flow = {...flow, status: 'exchanging', authorizeURL: ''}; return response(flow); }
      if (path.endsWith('/cancel')) { flow = {...flow, status: 'canceled'}; return response(flow); }
      return response({ok: true});
    }
    if (path.startsWith('/api/oauth/atlassian/flows/')) return response(flow);
    return response({profiles, providers: provs});
  };
  const at = await environment({fake: false, status: 'running'}); at.setProviderHandler(handler);
  at.els['provider-button'].listeners.click(); await flush();
  const group = at.els['provider-list'].children[0];
  assert.equal(group.children[0].textContent, 'Atlassian (MCP)');
  const guide = group.children[1]; assert.equal(guide.className, 'provider-guide'); assert.equal(guide.children[1].textContent, profiles[1].guide, 'the MCP command with single quotes');
  const [workRow, , ossRow] = group.children.slice(2);
  assert.match(workRow.children[0].children[2].textContent, /ゲートウェイが更新しています · 期限/); assert.equal(workRow.children[0].children[2].dataset.reauthorize, 'false');
  assert.match(ossRow.children[0].children[2].textContent, /再認可が必要です \(oauth_invalid_grant\)/); assert.equal(ossRow.children[0].children[2].dataset.reauthorize, 'true');
  assert.deepEqual(ossRow.children[1].children.map((b) => b.textContent), ['再接続', '削除', 'プロファイルを YAML で見る'], 'no 値を変更 for a refreshed credential');
  assert.match(ossRow.children[1].children[0].className, /button-primary/);
  assert.deepEqual(workRow.children[1].children.map((b) => b.textContent), ['今すぐ更新', '再接続', '削除', 'プロファイルを YAML で見る']);
  workRow.children[1].children[0].click(); await flush();
  assert.equal(posts.at(-1).path, '/api/providers/atlassian-work/rotate'); assert.match(at.els['provider-status'].textContent, /atlassian-work のアクセストークンをゲートウェイが更新しました/);
  // The form: no value input; the button connects.
  at.els['provider-profile'].value = 'iwato-atlassian'; at.els['provider-profile'].listeners.change();
  assert.equal(at.els['provider-values'].children.length, 0); assert.equal(at.els['provider-submit'].textContent, 'Atlassian に接続');
  at.els['provider-name'].value = 'atlassian-work'; at.els['provider-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.match(at.els['provider-form-status'].textContent, /もうあります/); assert.equal(at.els['provider-oauth'].hidden, true);
  at.els['provider-name'].value = 'atlassian-new'; at.els['provider-form'].listeners.submit({preventDefault() {}}); await flush();
  const box = at.els['provider-oauth']; assert.equal(box.hidden, false);
  assert.equal(box.children[0].children[0].textContent, 'Atlassian に接続: atlassian-new');
  const form = box.children[1]; const checks = walk(form, (n) => n.type === 'checkbox');
  assert.deepEqual(checks.map((c) => [c.value, c.checked, c.disabled]), [['offline_access', true, true], ['read:me', true, false], ['read:rovo:agent-interface', false, false]]);
  checks[2].checked = true; walk(form, (n) => n.placeholder?.startsWith('ほかのスコープ'))[0].value = ' write:jira:agent-interface ';
  form.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts.at(-1), {path: '/api/oauth/atlassian/start', body: {name: 'atlassian-new', scopes: ['offline_access', 'read:me', 'read:rovo:agent-interface', 'write:jira:agent-interface'], reconnect: false}, csrf: 'csrf123'});
  // Waiting: a link to Atlassian, a paste box for a phone.
  const link = walk(box, (n) => n.tagName === 'a')[0];
  assert.equal(link.href, authorizeURL); assert.equal(link.target, '_blank'); assert.equal(link.rel, 'noopener noreferrer');
  assert.ok(byText(box, 'スマホなど別の端末で許可したときは、許可のあとに開けなかったページのアドレス (http://127.0.0.1:50123/callback?…) をコピーして、ここに貼り付けてください。'));
  const paste = walk(box, (n) => n.className === 'login-form provider-paste')[0];
  paste.children[0].value = 'http://127.0.0.1:50123/callback?code=abc&state=s'; paste.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts.at(-1).body, {url: 'http://127.0.0.1:50123/callback?code=abc&state=s'}); assert.equal(paste.children[0].value, '', 'the pasted URL is cleared');
  assert.match(walk(box, (n) => /provider-oauth-status/.test(n.className))[0].textContent, /ゲートウェイに渡しています/);
  // The page polls until the flow ends; done shows the sandbox-side command.
  flow = {...flow, status: 'done', message: '接続しました。', refresh: {status: 'refreshed', expiresAt: '2026-10-08T05:00:00Z'}};
  const listed = at.requests.filter((r) => r.path === '/api/providers').length;
  const poll = [...at.timeouts.entries()].find(([, t]) => t.ms === 1500); assert.ok(poll, 'polls the flow'); at.timeouts.delete(poll[0]); poll[1].fn(); await flush();
  assert.match(walk(box, (n) => /provider-oauth-status/.test(n.className))[0].textContent, /^接続しました。ゲートウェイが更新しています/);
  assert.equal(walk(box, (n) => n.className === 'provider-guide-command')[0].textContent, profiles[1].guide);
  assert.equal(at.requests.filter((r) => r.path === '/api/providers').length, listed + 1, 'the list is read again');
  ok('Atlassian: OAuth instead of a value, scopes, the paste fallback, polling and the sandbox command');

  // 再接続 starts a reconnecting flow; a non-https authorization URL is not offered; closing cancels a waiting flow.
  authorizeURL = 'javascript:alert(1)';
  const ossNow = at.els['provider-list'].children[0].children[4];
  ossNow.children[1].children[0].click(); await flush();
  assert.equal(box.children[0].children[0].textContent, '再接続: atlassian-oss');
  box.children[1].listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts.at(-1).body, {name: 'atlassian-oss', scopes: ['offline_access', 'read:me'], reconnect: true});
  assert.equal(walk(box, (n) => n.tagName === 'a')[0].href, '', 'only an https authorization URL is linked');
  box.children[0].children[1].click(); await flush();
  assert.equal(box.hidden, true); assert.match(posts.at(-1).path, /\/flows\/f{32}\/cancel$/);
  ok('再接続 reconnects the same provider; closing cancels the waiting flow');

  // The card's panel: the command where an Atlassian provider is attached, reauthorization noted.
  const menuItem = (card, key) => { let found; const w = (n) => { if (n.dataset?.action === key) found = n; n.children.forEach(w); }; w(card); return found; };
  menuItem(at.card(), 'providers').click(); await flush();
  const side = at.side('provider-panel'); const atlGroup = side.children[5].children.find((g) => g.children[0].textContent === 'Atlassian (MCP)');
  assert.equal(atlGroup.children.at(-1).className, 'provider-guide');
  assert.match(atlGroup.children[2].children[0].children[2].textContent, /再認可が必要/);
  assert.equal(atlGroup.children[2].children[1].disabled, true, 'one Atlassian provider per sandbox');
  ok('the card panel shows the MCP command for an attached Atlassian provider');
  }

  {
  // The name is optional: the placeholder shows what the server will make (the profile's default, or the first free -N); the result names the provider.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const profiles = [{id: 'iwato-github', label: 'GitHub', note: '', defaultName: 'github', credentials: [{name: 'token', label: 'トークン'}]},
    {id: 'iwato-claude', label: 'Claude', note: '', defaultName: 'claude', credentials: [{name: 'oauth_token', label: 'トークン'}]},
    {id: 'iwato-atlassian', label: 'Atlassian (MCP)', note: '', oauth: true, defaultName: 'atlassian', guide: 'claude mcp add', credentials: []},
    {id: 'iwato-odd', label: 'Odd', note: '', defaultName: 'Bad Name', credentials: [{name: 'token', label: 'x'}]}];
  let provs = [{name: 'claude', profile: 'iwato-claude', keys: [], attached: []}, {name: 'claude-2', profile: 'iwato-claude', keys: [], attached: []}, {name: 'atlassian', profile: 'iwato-atlassian', keys: [], attached: []}];
  const posts = []; let flow = null;
  const handler = (path, opt) => {
    if (path === '/api/oauth/atlassian') return response({scopes: [{name: 'offline_access', default: true}]});
    if (opt?.method === 'POST') {
      const body = JSON.parse(opt.body); posts.push({path, body});
      if (path === '/api/providers') {
        const base = profiles.find((p) => p.id === body.profile).defaultName; let name = body.name; for (let i = 1; !name; i++) { const n = i === 1 ? base : base + '-' + i; if (!provs.some((p) => p.name === n)) name = n; }
        provs = provs.concat([{name, profile: body.profile, keys: [], attached: []}]); return response({ok: true, name});
      }
      if (path === '/api/oauth/atlassian/start') { flow = {id: 'f'.repeat(32), name: body.name, status: 'waiting', authorizeURL: 'https://auth.atlassian.com/authorize?x', redirectURI: 'http://127.0.0.1:50123/callback'}; return response(flow); }
      return response({ok: true});
    }
    if (path.startsWith('/api/oauth/atlassian/flows/')) return response(flow);
    return response({profiles, providers: provs});
  };
  const nm = await environment({fake: false, status: 'running'}); nm.setProviderHandler(handler);
  nm.els['provider-button'].listeners.click(); await flush();
  assert.equal(nm.els['provider-name'].placeholder, '名前 (省略すると github)');
  nm.els['provider-name'].value = '  '; nm.els['provider-values'].children[0].value = 'ghp_x';
  nm.els['provider-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts.at(-1), {path: '/api/providers', body: {name: '', profile: 'iwato-github', values: {token: 'ghp_x'}}});
  assert.equal(nm.els['provider-form-status'].textContent, 'github を追加しました。', 'the name the server chose');
  assert.equal(nm.els['provider-name'].placeholder, '名前 (省略すると github-2)', 'the next one after the list is read again');
  nm.els['provider-profile'].value = 'iwato-claude'; nm.els['provider-profile'].listeners.change();
  assert.equal(nm.els['provider-name'].placeholder, '名前 (省略すると claude-3)');
  nm.els['provider-profile'].value = 'iwato-odd'; nm.els['provider-profile'].listeners.change();
  assert.equal(nm.els['provider-name'].placeholder, '名前 (例: github-personal)', 'an invalid default from the server is not shown');
  // Atlassian: an empty name starts the flow; the title names the provider once created.
  nm.els['provider-profile'].value = 'iwato-atlassian'; nm.els['provider-profile'].listeners.change();
  assert.equal(nm.els['provider-name'].placeholder, '名前 (省略すると atlassian-2)');
  nm.els['provider-name'].value = ''; nm.els['provider-form'].listeners.submit({preventDefault() {}}); await flush();
  const box = nm.els['provider-oauth']; assert.equal(box.hidden, false); assert.equal(box.children[0].children[0].textContent, 'Atlassian に接続');
  box.children[1].listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts.at(-1), {path: '/api/oauth/atlassian/start', body: {name: '', scopes: ['offline_access'], reconnect: false}});
  assert.equal(box.children[0].children[0].textContent, 'Atlassian に接続');
  flow = {...flow, name: 'atlassian-2', status: 'done', message: 'atlassian-2 として接続しました。', guide: 'claude mcp add'};
  const poll = [...nm.timeouts.entries()].find(([, t]) => t.ms === 1500); nm.timeouts.delete(poll[0]); poll[1].fn(); await flush();
  assert.equal(box.children[0].children[0].textContent, 'Atlassian に接続: atlassian-2');
  assert.match(walk(box, (n) => /provider-oauth-status/.test(n.className))[0].textContent, /^atlassian-2 として接続しました。/);
  ok('the provider name is optional: the placeholder shows the generated one, the result and the flow name it');
  }

  {
  // Attach answers at once; the card's panel shows 反映待ち until the running sandbox applied it, and a failure with its reason.
  const profiles = [{id: 'iwato-claude', label: 'Claude Code', note: '', credentials: [{name: 'oauth_token', label: 't'}]}, {id: 'iwato-github', label: 'GitHub', note: '', credentials: [{name: 'token', label: 't'}]}];
  let applying = [{sandbox: 'api', attach: true, state: 'pending'}];
  let ghApplying = [{sandbox: 'api', attach: false, state: 'failed', reason: 'supervisor_rejected'}];
  const handler = (path, opt) => {
    if (opt?.method === 'POST') return response({ok: true, restart: true});
    return response({profiles, providers: [{name: 'claude', profile: 'iwato-claude', keys: ['CLAUDE_CODE_OAUTH_TOKEN'], attached: ['api'], applying}, {name: 'github-work', profile: 'iwato-github', keys: ['GH_TOKEN'], attached: [], applying: ghApplying}]});
  };
  const ap = await environment({fake: false, status: 'running'}); ap.setProviderHandler(handler);
  const menuItem = (card, key) => { let found; const w = (n) => { if (n.dataset?.action === key) found = n; n.children.forEach(w); }; w(card); return found; };
  menuItem(ap.card(), 'providers').click(); await flush();
  const list = () => ap.side('provider-panel').children[5];
  const claudeRow = () => list().children[0].children[1];
  assert.equal(claudeRow().children[0].children[0].children[0].textContent, '反映待ち');
  const ghRow = list().children[1].children[1];
  assert.match(ghRow.children[0].children[2].textContent, /外す変更をサンドボックスが反映できませんでした \(failed, supervisor_rejected\)/); assert.equal(ghRow.children[0].children[2].dataset.state, 'failed');
  const timer = [...ap.timeouts.entries()].find(([, t]) => t.ms === 3000); assert.ok(timer, 'looks again while a change is pending');
  applying = []; ap.timeouts.delete(timer[0]); timer[1].fn(); await flush();
  assert.equal(claudeRow().children[0].children[0].children.length, 0, '反映待ち goes once applied');
  assert.equal([...ap.timeouts.values()].filter((t) => t.ms === 3000).length, 0, 'no more polling');
  // The global panel names the sandbox.
  ap.els['provider-button'].listeners.click(); await flush();
  const gh = ap.els['provider-list'].children[1].children[1];
  assert.match(gh.children[0].children[2].textContent, /^api: 外す変更をサンドボックスが反映できませんでした/);
  ok('反映待ち on the card until the sandbox applies a change; a failure keeps its reason');
  }

  {
  // A rule that would carry the credential uninspected: the attach is refused with the rules named; the fix removes them and attaches.
  const profiles = [{id: 'iwato-claude', label: 'Claude Code', note: '', credentials: [{name: 'oauth_token', label: 't'}]}];
  let attached = []; let conflict = true; const posts = [];
  const handler = (path, opt) => {
    if (opt?.method === 'POST') {
      posts.push(path);
      if (path.endsWith('/providers/attach')) {
        if (conflict) return response({error: 'claude の値が送られる宛先を…', conflicts: [{rule: 'allow_api_anthropic_com_443', host: 'api.anthropic.com', ports: [443], mode: 'L4-only', provider: 'claude', binaries: ['/usr/bin/curl']}]}, 409);
        attached = ['api']; return response({ok: true, restart: true});
      }
      if (path.endsWith('/providers/fix')) { conflict = false; return response({ok: true, removed: []}); }
    }
    return response({profiles, providers: [{name: 'claude', profile: 'iwato-claude', keys: ['CLAUDE_CODE_OAUTH_TOKEN'], attached, applying: []}]});
  };
  const cf = await environment({fake: false, status: 'running'}); cf.setProviderHandler(handler);
  const menuItem = (card, key) => { let found; const w = (n) => { if (n.dataset?.action === key) found = n; n.children.forEach(w); }; w(card); return found; };
  menuItem(cf.card(), 'providers').click(); await flush();
  const panel = cf.side('provider-panel'); const [, , restartBox, conflictBox, status, list] = panel.children;
  list.children[0].children[1].children[1].click(); await flush();
  assert.equal(conflictBox.hidden, false); assert.equal(restartBox.hidden, true); assert.match(status.textContent, /claude はまだ付けていません/);
  assert.equal(conflictBox.children[1].children[0].textContent, '規則 allow_api_anthropic_com_443: api.anthropic.com:443 (L4-only) · /usr/bin/curl はこの宛先に出られなくなります');
  conflictBox.children[2].children[0].click(); await flush();
  assert.deepEqual(posts, ['/api/sandboxes/api/providers/attach', '/api/sandboxes/api/providers/fix', '/api/sandboxes/api/providers/attach'], 'fix, then attach again');
  assert.equal(conflictBox.hidden, true); assert.equal(restartBox.hidden, false); assert.match(status.textContent, /claude を付けました/);
  ok('a conflicting uninspected rule is named; the fix removes it and attaches');
  }

  {
  // An endpoint the provider's rule would make ambiguous: named with what
  // differs and what it allows now; switched to enforce as is, or removed.
  const profiles = [{id: 'iwato-github', label: 'GitHub', note: '', credentials: [{name: 'token', label: 't'}]}];
  let attached = []; let conflict = true; const posts = [];
  const amb = {rule: 'gh', endpoint: 'api.github.com:443', with: '_provider_github_personal', withProvider: {name: 'github-personal', label: 'GitHub'}, details: ['検査のモード: gh は 監査 (audit)、_provider_github_personal は 遮断 (enforce)'],
    host: 'api.github.com', endpointPorts: [443], binaries: ['/usr/bin/gh'], audited: true, fix: 'enforce', allows: [{method: 'GET', path: '/'}, {method: 'GET', path: '/**'}]};
  let ambiguities = [amb];
  const handler = (path, opt) => {
    if (opt?.method === 'POST') {
      posts.push([path, JSON.parse(opt.body)]);
      if (path.endsWith('/providers/attach')) {
        if (conflict) return response({error: 'github-personal を付けると OpenShell に拒否されます…', ambiguities}, 409);
        attached = ['api']; return response({ok: true, restart: false});
      }
      if (path.endsWith('/providers/fix')) { conflict = false; return response({ok: true}); }
    }
    return response({profiles, providers: [{name: 'github-personal', profile: 'iwato-github', keys: ['GH_TOKEN'], attached, applying: []}]});
  };
  const af = await environment({fake: false, status: 'running'}); af.setProviderHandler(handler);
  const menuItem = (card, key) => { let found; const w = (n) => { if (n.dataset?.action === key) found = n; n.children.forEach(w); }; w(card); return found; };
  menuItem(af.card(), 'providers').click(); await flush();
  const panel = af.side('provider-panel'); const [, , , conflictBox, status, list] = panel.children;
  list.children[0].children[1].children[1].click(); await flush();
  assert.equal(conflictBox.hidden, false); assert.match(status.textContent, /github-personal はまだ付けていません/);
  assert.equal(conflictBox.children[1].children[0].textContent, '規則 gh: api.github.com:443 — 検査のモード: gh は 監査 (audit)、_provider_github_personal は 遮断 (enforce) · 今の許可: GET / (ルートだけ), GET /** (すべて)');
  assert.deepEqual(conflictBox.children[2].children.map((b) => b.textContent), ['今の許可のまま遮断に切り替えて付ける', '記録から選んで遮断に切り替える (ポリシーのタブ)', 'この宛先を規則から外して付ける', 'やめる']);
  conflictBox.children[2].children[0].click(); await flush();
  assert.deepEqual(posts.map((p) => p[0].split('/').pop() + (p[1].mode ? ':' + p[1].mode : '')), ['attach', 'fix:enforce', 'attach'], 'enforce, then attach again');
  assert.match(status.textContent, /github-personal を付けました/);
  // Not fixable by enforcing (another protocol): only removing.
  attached = []; conflict = true; posts.length = 0; ambiguities = [{...amb, fix: '', audited: false, details: ['検査の種類 (protocol): gh は graphql、_provider_github_personal は rest'], allows: [], access: 'read-only'}];
  menuItem(af.card(), 'providers').click(); await flush(); menuItem(af.card(), 'providers').click(); await flush();
  list.children[0].children[1].children[1].click(); await flush();
  assert.deepEqual(conflictBox.children[2].children.map((b) => b.textContent), ['この宛先を規則から外して付ける', 'やめる']);
  assert.match(conflictBox.children[1].children[0].textContent, /今の許可: 読み取りだけ$/);
  conflictBox.children[2].children[0].click(); await flush();
  assert.deepEqual(posts.map((p) => p[0].split('/').pop() + (p[1].mode ? ':' + p[1].mode : '')), ['attach', 'fix:remove', 'attach']);
  ok('an endpoint the provider would make ambiguous is named; it is switched to enforce or removed, then the provider attached');
  }

  {
  // A profile as YAML: Iwato's shipped file or a gateway profile rendered, read-only, with copy; the newest request wins.
  const profiles = [{id: 'iwato-github', label: 'GitHub', note: '', credentials: [{name: 'token', label: 'トークン'}]}];
  const provs = [{name: 'github-personal', profile: 'iwato-github', keys: ['GH_TOKEN'], attached: []}, {name: 'openai', profile: 'openai', keys: ['OPENAI_API_KEY'], attached: []}];
  const shipped = '# comment <b>kept</b>\nid: iwato-github\n';
  const gets = []; const pending = {};
  const yv = await environment({fake: false, status: 'running'});
  yv.setProviderHandler((path, opt) => {
    if (path.startsWith('/api/provider-profiles/')) {
      gets.push(path);
      if (path.endsWith('/iwato-github')) return response({id: 'iwato-github', source: 'iwato', file: 'profiles/iwato-github.yaml', yaml: shipped});
      if (path.endsWith('/openai')) { pending.openai = deferred(); return pending.openai.promise.then(() => response({id: 'openai', source: 'gateway', scope: 'platform', yaml: 'id: openai\n'})); }
      return response({error: 'ゲートウェイにプロファイル nope がありません'}, 404);
    }
    return response({profiles, providers: provs});
  });
  yv.els['provider-button'].listeners.click(); await flush();
  const box = yv.els['provider-yaml']; assert.equal(box.hidden, true);
  const [ghGroup, otherGroup] = yv.els['provider-list'].children;
  assert.equal(otherGroup.children[0].textContent, 'その他');
  const yamlButton = (row) => row.children[1].children.find((b) => b.textContent === 'プロファイルを YAML で見る');
  yamlButton(ghGroup.children[1]).click(); await flush();
  assert.equal(gets.at(-1), '/api/provider-profiles/iwato-github'); assert.equal(box.hidden, false);
  const [head, pre] = box.children;
  assert.equal(pre.className, 'code-field code-readonly provider-yaml-view'); assert.equal(pre.children[0].value, shipped, 'the shipped file verbatim, as text'); assert.equal(pre.children[0].readOnly, true);
  assert.equal(head.children[0].children[0].dataset.source, 'iwato'); assert.equal(head.children[0].children[0].textContent, 'Iwato 同梱');

  head.children[1].children[0].click(); await flush();
  assert.equal(head.children[1].children[0].textContent, 'コピーしました'); assert.deepEqual(yv.copied.at(-1), shipped);
  // A gateway profile, then the form's button while it loads: the later answer wins.
  yamlButton(otherGroup.children[1]).click(); await flush();
  assert.equal(gets.at(-1), '/api/provider-profiles/openai'); assert.equal(box.children[1].textContent, '読み込み中…');
  yv.els['provider-profile-yaml'].listeners.click(); await flush();
  pending.openai.resolve(); await flush();
  assert.equal(box.children[1].children[0].value, shipped, 'a stale answer does not replace the newer one');
  yamlButton(otherGroup.children[1]).click(); await flush(); pending.openai.resolve(); await flush();
  assert.equal(box.children[0].children[0].children[0].dataset.source, 'gateway'); assert.equal(box.children[0].children[0].children[0].textContent, 'ゲートウェイから');
  assert.equal(box.children[1].children[0].value, 'id: openai\n');
  // Errors are text; closing empties the box, as does closing the panel.
  provs.push({name: 'gone', profile: 'nope', keys: [], attached: []});
  yv.els['provider-close'].listeners.click(); assert.equal(box.hidden, true); assert.equal(box.children.length, 0);
  yv.els['provider-button'].listeners.click(); await flush();
  const gone = yv.els['provider-list'].children[1].children.find((n) => n.children?.[0]?.children?.[0]?.textContent === 'gone');
  yamlButton(gone).click(); await flush();
  assert.match(box.children[1].textContent, /YAML を取得できません: ゲートウェイにプロファイル nope がありません/);
  box.children[0].children[1].children[0].click(); assert.equal(box.hidden, true);
  ok('a profile is shown as YAML: the shipped file or the gateway rendering, marked, with copy');
  }


  {
  // The editor's quick open: a fuzzy match that prefers the file's name and
  // consecutive characters, recent files first without a query.
  const box = {window: {}, navigator: {languages: ['ja']}}; vm.runInNewContext(i18nSource, box); vm.runInNewContext(editorSource, box); const E = box.window.IwatoEditor;
  const paths = ['work/iwato/web/app.js', 'work/iwato/approve_test.go', 'apps/main.go', 'work/iwato/web/approve.js', 'README.md'];
  assert.equal(E.rank('appjs', paths)[0].path, 'work/iwato/web/app.js');
  assert.equal(E.rank('appr', paths)[0].path, 'work/iwato/web/approve.js');
  assert.deepEqual([...E.rank('wwa', paths).map((m) => m.path).slice(0, 2)], ['work/iwato/web/app.js', 'work/iwato/web/approve.js']);
  assert.equal(E.fuzzy('zzz', 'README.md'), null);
  assert.deepEqual([...E.fuzzy('rd', 'README.md').positions], [0, 3]);
  assert.deepEqual([...E.rank('', paths, ['README.md'], 2).map((m) => m.path)], ['README.md', 'work/iwato/web/app.js']);
  // Hidden paths (any component starting with a dot) rank below a comparable
  // match, unless the query's dot starts one of them; and last without a query.
  const dots = ['.vscode/settings.json', 'work/iwato/settings.json', 'a/.env', 'env.go', '.vscode/app.js', '.git/config', 'config.go', '.bashrc'];
  assert.equal(E.rank('settings', dots)[0].path, 'work/iwato/settings.json');
  assert.equal(E.rank('env', dots)[0].path, 'env.go');
  assert.equal(E.rank('.env', dots)[0].path, 'a/.env');
  assert.equal(E.rank('.git/c', dots)[0].path, '.git/config');
  assert.equal(E.rank('appjs', dots)[0].path, '.vscode/app.js');
  assert.equal(E.rank('.js', [...dots, 'web/app.js'])[0].path, 'web/app.js');
  assert.deepEqual([...E.rank('', dots, ['.bashrc'], 4).map((m) => m.path)], ['.bashrc', 'work/iwato/settings.json', 'env.go', 'config.go']);
  for (const [typed, want] of [['./work/iwato/', 'work/iwato'], ['~/work', 'work'], ['~', ''], ['.', ''], ['  src ', 'src']]) assert.equal(E.folderOf(typed), want);
  assert.deepEqual({...E.lines('a\r\nb\r\n')}, {eol: '\r\n', text: 'a\nb\n'});
  assert.deepEqual({...E.lines('a\nb')}, {eol: '\n', text: 'a\nb'});
  assert.equal(E.lines('a\r\nb\n'), null); assert.equal(E.lines('a\rb'), null);
  ok('the editor keeps a CRLF file\'s endings and refuses mixed ones');
  for (const [file, want] of [['a/main.go', 'go'], ['x.TSX', 'tsx'], ['web/app.js', 'javascript'], ['Dockerfile', 'dockerfile'], ['build.Dockerfile', 'dockerfile'], ['.bashrc', 'shell'], ['Gemfile', 'ruby'], ['Cargo.lock', 'toml'], ['go.mod', ''], ['notes.txt', ''], ['Makefile', ''], ['icon.svg', 'html']]) assert.equal(E.languageOf(file), want, file);
  ok('the editor picks the highlighting by the file name');
  ok('the editor quick open ranks a fuzzy match by the file name, hidden paths lower, and keeps recent files first');
  }

  {
  // The editor's tree: one folder read at a time, directories expanded on a
  // click and remembered, files opened and marked, a file opened elsewhere
  // revealed (not inside a heavy folder), links, the cap, errors, refresh,
  // and a drawer on a phone.
  const store = {}; let narrowWidth = false;
  const box = {window: {matchMedia: (q) => ({matches: q.includes('max-width') && narrowWidth})}, navigator: {languages: ['ja']}, localStorage: {getItem: (k) => store[k] ?? null, setItem: (k, v) => { store[k] = String(v); }}};
  vm.runInNewContext(i18nSource, box); vm.runInNewContext(editorSource, box); const E = box.window.IwatoEditor;
  assert.deepEqual([...E.ancestors('work', 'work/a/b/c.md')], ['work/a', 'work/a/b']);
  assert.equal(E.ancestors('work', 'other/x'), null); assert.deepEqual([...E.ancestors('', 'x.md')], []);
  assert.deepEqual(E.treeEntries({entries: [{name: '../x', kind: 'file'}, {name: 'a', kind: 'weird'}, {name: 'ok', kind: 'file', size: 3}], total: 1}).entries.map((e) => e.name), ['ok']);
  const fsTree = {
    'work/iwato': {entries: [{name: 'docs', kind: 'dir'}, {name: 'node_modules', kind: 'dir', heavy: true}, {name: 'web', kind: 'dir'}, {name: 'LINK.md', kind: 'symlink', to: 'file'}, {name: 'README.md', kind: 'file', size: 9}, {name: 'broken', kind: 'symlink'}, {name: 'dlink', kind: 'symlink', to: 'dir'}], total: 9, truncated: true},
    'work/iwato/docs': {entries: [{name: 'editor.md', kind: 'file'}], total: 1},
    'work/iwato/web': {entries: [{name: 'app.js', kind: 'file'}], total: 1},
    'work/iwato/node_modules': {entries: [{name: 'x.js', kind: 'file'}], total: 1},
    '': {entries: [{name: 'notes.md', kind: 'file'}], total: 1}
  };
  const calls = []; let failDir = null;
  let rootAnswer = {path: '/home/dev', home: true, work: true};
  const api = async (url) => {
    if (url.endsWith('/editor/root')) return rootAnswer;
    calls.push(url);
    const m = url.match(/\/editor\/(dir|file)\?path=(.*)$/); const p = decodeURIComponent(m[2]);
    if (m[1] === 'file') return {path: p, content: 'text of ' + p, version: 'v'};
    if (p === failDir) { const e = new Error('サンドボックスは停止しています。起動すると開けます'); e.status = 409; throw e; }
    return fsTree[p] || {entries: [], total: 0};
  };
  const element = (tag, className, text) => { const n = new Element(tag); if (className) n.className = className; if (text !== undefined) n.textContent = text; return n; };
  const button = (cls, label, action) => { const n = element('button', 'button ' + cls, label); n.addEventListener('click', action); return n; };
  const panelHeader = (title, ...controls) => { const h = element('div', 'panel-header'); h.append(element('h3', 'panel-title', title), ...controls); return h; };
  const Panel = E.panel({element, button, panelHeader, api, askConfirm: async () => true});
  store['iwato-editor-dir:dev'] = 'work/iwato';
  const sandbox = {name: 'dev', status: 'running'};
  const p = new Panel(sandbox);
  assert.equal(p.tree.hidden, false, 'the tree shows by default on a PC');
  assert.equal(calls.length, 0, 'nothing is read while the tab is hidden');
  p.show(); await flush();
  const rows = (panel = p) => panel.treeList.children.map((li) => li.className === 'editor-tree-row' ? li.children[0] : li);
  const row = (path, panel = p) => rows(panel).find((r) => r.dataset?.path === path);
  const label = (r) => r.dataset?.path ? r.children[0].textContent + r.children[1].textContent : r.textContent;
  assert.deepEqual(calls, ['/api/sandboxes/dev/editor/dir?path=work%2Fiwato']);
  {
    // Without a saved folder, ~/work when the sandbox has it; a root that is
    // not the home gets the note.
    const before = calls.length;
    const q = new Panel({name: 'box', status: 'running'});
    q.show(); await flush();
    assert.equal(q.dir, 'work', 'without a saved folder, the editor opens ~/work');
    assert.equal(q.rootNote.hidden, true);
    q.dispose();
    rootAnswer = {path: '/sandbox', home: false, work: false};
    const r = new Panel({name: 'other', status: 'running'});
    r.show(); await flush();
    assert.equal(r.dir, ''); assert.equal(r.rootNote.hidden, false); assert.match(r.rootNote.textContent, /\/sandbox/);
    r.dispose();
    rootAnswer = {path: '/home/dev', home: true, work: true};
    calls.splice(before);
  }
  assert.deepEqual(rows().map(label), ['▸docs', '▸node_modules', '▸web', '↪LINK.md', 'README.md', '↪broken', '↪dlink', '多すぎるため 7 個まで表示しています (全部で 9 個)。クイックオープンで探せます']);
  assert.equal(row('work/iwato/broken').disabled, true); assert.equal(row('work/iwato/dlink').disabled, true); assert.equal(row('work/iwato/LINK.md').disabled, false);
  assert.equal(row('work/iwato/node_modules').dataset.heavy, 'true');
  row('work/iwato/docs').click(); await flush();
  assert.equal(row('work/iwato/docs').getAttribute('aria-expanded'), 'true');
  assert.equal(row('work/iwato/docs/editor.md').style.paddingLeft, '22px');
  assert.deepEqual(JSON.parse(store['iwato-editor-tree:dev']), ['work/iwato/docs']);
  row('work/iwato/README.md').click(); await flush();
  assert(calls.includes('/api/sandboxes/dev/editor/file?path=work%2Fiwato%2FREADME.md'));
  assert.equal(row('work/iwato/README.md').getAttribute('aria-current'), 'true');
  // Opened as quick open does: the tree expands down to it.
  await p.open('work/iwato/web/app.js'); await flush();
  assert.equal(row('work/iwato/web/app.js').getAttribute('aria-current'), 'true');
  assert.equal(row('work/iwato/README.md').getAttribute('aria-current'), null);
  await p.open('work/iwato/node_modules/x.js'); await flush();
  assert.equal(row('work/iwato/node_modules').getAttribute('aria-expanded'), 'false', 'a heavy folder is not expanded by itself');
  // Refresh reads the folders shown again.
  const before = calls.length; p.refreshTree(); await flush();
  assert.deepEqual(calls.slice(before).sort(), ['/api/sandboxes/dev/editor/dir?path=work%2Fiwato', '/api/sandboxes/dev/editor/dir?path=work%2Fiwato%2Fdocs', '/api/sandboxes/dev/editor/dir?path=work%2Fiwato%2Fweb']);
  // A failed folder says why and can be read again.
  row('work/iwato/web').click(); failDir = 'work/iwato/web'; p.refreshTree(); await flush();
  row('work/iwato/web').click(); await flush();
  const note = rows().find((r) => r.className === 'editor-tree-note' && /停止/.test(r.textContent));
  assert(note, 'the error shows under the folder'); failDir = null;
  note.children[0].click(); await flush();
  assert(row('work/iwato/web/app.js'), 'read again');
  // Another panel for the sandbox keeps the expanded folders.
  const q = new Panel(sandbox); q.show(); await flush();
  assert.equal(row('work/iwato/docs', q).getAttribute('aria-expanded'), 'true'); assert(row('work/iwato/web/app.js', q));
  q.dispose();
  // A phone: hidden until the button, closed again once a file is picked.
  narrowWidth = true;
  rootAnswer = {path: '/home/dev', home: true, work: false};
  const r = new Panel({name: 'phone', status: 'running'}); r.show(); await flush();
  assert.equal(r.tree.hidden, true);
  assert.equal(r.main.dataset.tree, 'closed');
  r.treeButton.click(); await flush();
  assert.equal(r.tree.hidden, false); assert.equal(r.treeButton.getAttribute('aria-expanded'), 'true');
  assert.deepEqual(calls.slice(-1), ['/api/sandboxes/phone/editor/dir?path=']);
  row('notes.md', r).click(); await flush();
  assert.equal(r.tree.hidden, true, 'picking a file closes the drawer');
  assert.equal(r.current?.path, 'notes.md');
  r.dispose(); p.dispose();
  ok('the editor tree reads folders lazily, remembers them, opens and reveals files, and is a drawer on a phone');
  }

  {
  // The editor's image preview: an image by its name (no text read), a file
  // the server says is not text tried as an image, read only, fit or 100 %,
  // a non-image binary refused without a tab, an SVG opened as text.
  const box = {window: {}, navigator: {languages: ['ja']}, performance: {getEntriesByName: () => [{decodedBodySize: 3072}]}};
  vm.runInNewContext(i18nSource, box); vm.runInNewContext(editorSource, box); const E = box.window.IwatoEditor;
  assert.equal(E.imageName('a/B.PNG'), true); assert.equal(E.imageName('a.svg'), true); assert.equal(E.imageName('png'), false);
  const calls = []; const imgs = [];
  const fail = (status, message) => { const e = new Error(message); e.status = status; return e; };
  const api = async (url, options = {}) => {
    calls.push((options.method || 'GET') + ' ' + url);
    const m = url.match(/\/editor\/(dir|file|raw)\?path=([^&]*)/); const p = m && decodeURIComponent(m[2]);
    if (m?.[1] === 'dir') return {entries: [], total: 0};
    if (m?.[1] === 'raw') throw fail(415, 'テキストでも画像でもない (バイナリの) ファイルは開けません');
    if (p === 'bin/tool' || p === 'shot') throw fail(415, 'テキストではない (バイナリの) ファイルは開けません');
    return {path: p, content: 'text of ' + p, version: 'v'};
  };
  const element = (tag, className, text) => { const n = new Element(tag); if (className) n.className = className; if (text !== undefined) n.textContent = text; if (tag === 'img') imgs.push(n); return n; };
  const button = (cls, label, action) => { const n = element('button', 'button ' + cls, label); n.addEventListener('click', action); return n; };
  const panelHeader = (title, ...controls) => { const h = element('div', 'panel-header'); h.append(element('h3', 'panel-title', title), ...controls); return h; };
  const Panel = E.panel({element, button, panelHeader, api, askConfirm: async () => true});
  const p = new Panel({name: 'dev', status: 'running'}); p.show(); await flush();
  const settle = async (ok = true) => { await flush(); const img = imgs[imgs.length - 1]; if (ok) { img.naturalWidth = 48; img.naturalHeight = 32; img.listeners.load(); } else img.listeners.error(); await flush(); return img; };
  let opening = p.open('web/icon.png'); const icon = await settle(); await opening;
  assert(!calls.some((c) => c.includes('/editor/file')), 'an image by its name is not read as text');
  assert.equal(icon.src, '/api/sandboxes/dev/editor/raw?path=web%2Ficon.png&n=1');
  assert.equal(p.current.image, true); assert.equal(p.text.hidden, true); assert.equal(p.image.hidden, false);
  assert.equal(p.imageBox.children[0], icon); assert.equal(p.imageMeta.textContent, '48 × 32 px · 3.0 KB');
  assert.equal(p.image.dataset.fit, 'fit'); assert.equal(p.imageText.hidden, true);
  assert.equal(p.saveButton.disabled, true); assert.equal(p.unsaved(), false);
  await p.save(); assert(!calls.some((c) => c.startsWith('POST')), 'an image is not saved'); assert.match(p.status.textContent, /保存はできません/);
  p.imageFit.click(); assert.equal(p.image.dataset.fit, 'actual'); assert.equal(p.imageFit.textContent, '全体を表示');
  icon.click(); assert.equal(p.image.dataset.fit, 'fit');
  // Reload reads it again (a new URL).
  opening = p.reload(); const again = await settle(); await opening;
  assert.equal(again.src, '/api/sandboxes/dev/editor/raw?path=web%2Ficon.png&n=2'); assert.equal(p.imageBox.children[0], again);
  // A text file, then back to the image.
  await p.open('README.md'); assert.equal(p.text.hidden, false); assert.equal(p.image.hidden, true); assert.equal(p.text.value, 'text of README.md'); assert.equal(p.saveButton.disabled, false);
  p.switchTo(p.docs[0]); assert.equal(p.text.hidden, true); assert.equal(p.image.hidden, false);
  // Not text by its content: tried as an image.
  opening = p.open('shot'); await settle(); await opening;
  assert.equal(p.current.path, 'shot'); assert.equal(p.current.image, true);
  // Neither text nor image: refused, no tab.
  const tabs = p.docs.length;
  opening = p.open('bin/tool'); await settle(false); await opening;
  assert.equal(p.docs.length, tabs); assert.match(p.status.textContent, /bin\/tool: テキストでも画像でもない \(バイナリ/);
  assert(calls.includes('GET /api/sandboxes/dev/editor/raw?path=bin%2Ftool&n=4'), 'the route says why it did not load');
  // An SVG can be opened as text instead.
  opening = p.open('logo.svg'); await settle(); await opening;
  assert.equal(p.imageText.hidden, false);
  await p.openAsText(); assert.equal(p.current.image, undefined); assert.equal(p.text.value, 'text of logo.svg'); assert.equal(p.docs.filter((d) => d.path === 'logo.svg').length, 1);
  // Closing the last tabs hides the preview.
  for (const d of [...p.docs]) await p.closeDoc(d);
  assert.equal(p.image.hidden, true); assert.equal(p.text.hidden, true); assert.equal(p.empty.hidden, false);
  p.dispose();
  ok('the editor previews images read only (by name or when not text), refuses other binaries, and opens an SVG as text');
  }

  {
  // Files: the addresses each OS takes (pure helpers).
  const box = {window: {}, URL, navigator: {languages: ['ja']}}; vm.runInNewContext(i18nSource, box); vm.runInNewContext(filesSource, box); const F = box.window.IwatoFiles;
  assert.equal(F.davURL('https://box.tail0000.ts.net', 'dev'), 'https://box.tail0000.ts.net/dav/dev/');
  assert.equal(F.davURL('http://127.0.0.1:17600/', 'dev', 'tok'), 'http://127.0.0.1:17600/dav/tok/dev/');
  assert.equal(F.uncPath('https://box.tail0000.ts.net', 'dev'), '\\\\box.tail0000.ts.net@SSL\\dav\\dev');
  assert.equal(F.uncPath('https://box.tail0000.ts.net:8443', 'dev'), '\\\\box.tail0000.ts.net@SSL@8443\\dav\\dev');
  assert.equal(F.uncPath('http://127.0.0.1:17600', 'dev', 'tok'), '\\\\127.0.0.1@17600\\dav\\tok\\dev');
  assert.equal(F.uncPath('http://127.0.0.1:17600', '', 'tok'), '\\\\127.0.0.1@17600\\dav\\tok');
  assert.equal(F.ago('', 0), '未使用'); assert.equal(F.ago('2026-01-01T00:00:00Z', Date.parse('2026-01-01T00:03:30Z')), '3 分前');
  ok('files: WebDAV URLs and Windows paths for the tailnet and this PC');
  }

  {
  // Files tab: URLs per OS with copy, credentials made (shown once), revoked; a stopped sandbox keeps the tab.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const keys = [{id: 'iwato-k3f9x2', name: '<img src=x onerror=alert(1)>', kind: 'password', sandbox: 'api', readOnly: false, lastUsed: ''}];
  const posts = [];
  const fe = await environment({fake: false, status: 'running'});
  fe.setFilesHandler((path, opt) => {
    if (opt?.method === 'POST') {
      posts.push({path, body: opt.body ? JSON.parse(opt.body) : null, csrf: opt.headers['X-CSRF-Token']});
      if (path === '/api/files/keys') { const b = JSON.parse(opt.body); const key = {id: 'iwato-new123', name: b.name, kind: b.kind, sandbox: b.sandbox, readOnly: b.readOnly}; keys.push(key); return response({key, secret: 's'.repeat(52)}); }
      keys.splice(0, 1); return response({ok: true});
    }
    return response({keys, publicOrigin: 'https://box.tail0000.ts.net', localOrigin: 'http://127.0.0.1:17600', available: true});
  });
  const tab = walk(fe.card(), (n) => n.dataset?.view === 'files')[0];
  assert(tab, 'a files tab'); tab.click(); await flush();
  const panel = fe.side('files-panel'); assert.equal(panel.hidden, false); assert.equal(fe.card().dataset.tab, 'files');
  const values = () => walk(panel, (n) => n.className === 'text-input files-value').map((n) => n.value);
  assert.deepEqual(values(), ['https://box.tail0000.ts.net/dav/api/', '\\\\box.tail0000.ts.net@SSL\\dav\\api', 'https://box.tail0000.ts.net/dav/api/', 'https://box.tail0000.ts.net/dav/api/']);
  assert.match(walk(panel, (n) => n.className === 'files-state')[0].textContent, /ネットワークドライブ/);
  walk(panel, (n) => n.textContent === 'コピー')[1].click(); await flush();
  assert.equal(fe.copied.at(-1), '\\\\box.tail0000.ts.net@SSL\\dav\\api');
  const keyRow = walk(panel, (n) => n.className === 'volume-row files-key')[0];
  assert.equal(keyRow.children[0].children[0].textContent, keys[0].name, 'a name is text');
  assert.match(keyRow.children[0].children[1].textContent, /ID iwato-k3f9x2 · api · 読み書き · 未使用/);
  // Make an app password for all sandboxes, read-only: the value shows once.
  const form = walk(panel, (n) => n.className === 'stack-form files-form')[0];
  const [, nameInput, , scope, perm] = form.children;
  nameInput.value = ' iPhone ';
  scope.children[2].children[0].checked = true; scope.children[1].children[0].checked = false;
  perm.children[2].children[0].checked = true; perm.children[1].children[0].checked = false;
  form.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts[0], {path: '/api/files/keys', body: {name: 'iPhone', kind: 'password', sandbox: '', readOnly: true}, csrf: 'csrf123'});
  let made = walk(panel, (n) => n.className === 'files-made')[0];
  assert.equal(made.hidden, false);
  assert.deepEqual(walk(made, (n) => n.className === 'text-input files-value').map((n) => n.value), ['iwato-new123', 's'.repeat(52), 'https://box.tail0000.ts.net/dav/api/']);
  assert.equal(nameInput.value, '', 'the form empties');
  // A secret URL for this PC: the Windows path with the token.
  nameInput.value = 'この PC'; form.children[2].children[2].children[0].checked = true; form.children[2].children[1].children[0].checked = false;
  scope.children[1].children[0].checked = true; scope.children[2].children[0].checked = false;
  form.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts[1].body, {name: 'この PC', kind: 'url', sandbox: 'api', readOnly: true});
  made = walk(panel, (n) => n.className === 'files-made')[0];
  assert.deepEqual(walk(made, (n) => n.className === 'text-input files-value').map((n) => n.value), ['\\\\127.0.0.1@17600\\dav\\' + 's'.repeat(52) + '\\api', 'http://127.0.0.1:17600/dav/' + 's'.repeat(52) + '/api/']);
  // Closing the tab forgets the value.
  walk(panel, (n) => n.textContent === '閉じる' && n.className.includes('button-subtle')).at(-1).click();
  assert.equal(made.hidden, true); assert.equal(made.children.length, 0);
  // Revoke asks first.
  fe.setConfirm(true);
  walk(panel, (n) => n.textContent === '取り消す')[0].click(); await flush();
  assert.match(fe.confirmations.at(-1), /を取り消しますか/);
  assert.equal(posts[2].path, '/api/files/keys/iwato-k3f9x2/revoke');
  // Stopped: the tab stays and says so.
  fe.sandbox.status = 'stopped'; await fe.tickPoll();
  assert.equal(fe.card().dataset.tab, 'files', 'the files tab stays for a stopped sandbox');
  assert.match(walk(panel, (n) => n.className === 'files-state')[0].textContent, /停止中/);
  ok('files tab: per-OS URLs with copy, a credential shown once, revoke with confirmation, stopped state');
  }

  {
  // Without the tailnet (no -public-origin): only this PC's secret URL is offered.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const fl = await environment({fake: false, status: 'stopped'});
  const tab = walk(fl.card(), (n) => n.dataset?.view === 'files')[0];
  tab.click(); await flush();
  const panel = fl.side('files-panel');
  assert.equal(walk(panel, (n) => n.className === 'text-input files-value').length, 0);
  assert(walk(panel, (n) => /tailnet に公開していない/.test(n.textContent)).length === 1);
  ok('files tab without the tailnet points to this PC\'s secret URL');
  }
  {
  // Ingress: the helpers (snippets, owners, sanitized views).
  const box = {window: {}}; vm.runInNewContext(ingressSource, box); const I = box.window.IwatoIngress;
  assert.equal(I.tagOwnersSnippet('tag:iwato'), '"tagOwners": {\n  "tag:iwato": ["autogroup:admin"]\n}');
  assert.match(I.grantsSnippet('tag:x'), /"dst": \["tag:x"\]/);
  assert.deepEqual([...I.parseOwners(' alice@github\nbob@github, ALICE@github  ')], ['alice@github', 'bob@github']);
  const v = I.portsView({ports: [{port: 5173, url: 'javascript:alert(1)'}, {port: 3000, url: 'https://iwato-dev.tail0000.ts.net:3000/'}, {port: 0}, {port: 'x'},
    {port: 8000, url: 'http://8000.dev.localhost:17600/'}, {port: 8001, url: 'http://8001.dev.evil.example:17600/'}, {port: 8002, url: 'http://evil.example/'}]});
  assert.deepEqual(v.ports.map((p) => [p.port, p.url]), [[5173, ''], [3000, 'https://iwato-dev.tail0000.ts.net:3000/'], [8000, 'http://8000.dev.localhost:17600/'], [8001, ''], [8002, '']], 'only https node URLs and this PC\'s *.localhost become links');
  assert.equal(I.settingsView({}).provider, 'local', 'local by default');
  assert.equal(I.settingsView({provider: 'cloud'}).provider, 'local');
  ok('ingress: tagOwners and grants snippets, owners parsing, only https and *.localhost links kept');
  }

  {
  // 設定: the Tailscale settings, prefilled owners, the secret sent once, setup steps with copy.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const posts = [];
  let settings = {provider: 'local', listen: '127.0.0.1:17600', tag: 'tag:iwato', hostnamePrefix: 'iwato-', owners: [], defaults: {tag: 'tag:iwato', hostnamePrefix: 'iwato-'}, suggestedOwners: ['alice@github'], secretEnv: 'IWATO_TS_AUTHKEY',
    secret: {set: false, storage: 'dpapi'}, status: {state: 'off', message: '公開していません'}, nodes: [{sandbox: 'api', hostname: 'iwato-api', state: 'off'}]};
  const se = await environment({fake: false, status: 'running'});
  se.setIngressHandler((path, opt) => {
    if (opt?.method === 'POST') {
      const body = JSON.parse(opt.body); posts.push({path, body, csrf: opt.headers['X-CSRF-Token']});
      if (path === '/api/ingress/secret') settings = {...settings, secret: {set: true, source: 'file', kind: 'oauth', hint: 'tskey-client-…WXYZ', storage: 'dpapi'}};
      if (path === '/api/ingress') settings = {...settings, provider: body.provider, owners: body.owners, status: {state: 'on', message: 'Tailscale で公開しています'}};
      return response({ok: true});
    }
    return response(settings);
  });
  assert.equal(se.els['settings-button'].hidden, false, 'the owner sees 設定');
  se.els['settings-button'].click(); await flush();
  assert.equal(se.els['settings-panel'].hidden, false);
  const body = se.els['settings-body'];
  const radios = walk(body, (n) => n.type === 'radio');
  assert.deepEqual(radios.map((r) => [r.value, r.checked]), [['local', true], ['tailscale', false], ['off', false]], 'local is chosen');
  assert(walk(body, (n) => /http:\/\/ポート\.サンドボックス\.localhost:17600\//.test(n.textContent || '')).length === 1, 'the local URL form with the port');
  const tsParts = walk(body, (n) => n.className === 'stack-form ingress-tailscale');
  assert.equal(tsParts.length, 2); assert(tsParts.every((n) => n.hidden), 'the Tailscale settings are hidden while local is chosen');
  radios[0].checked = false; radios[1].checked = true; radios[1].listeners.change(); 
  assert(tsParts.every((n) => !n.hidden), 'choosing Tailscale shows its settings');
  const owners = walk(body, (n) => n.attributes['aria-label'] === '通すログイン名')[0];
  assert.equal(owners.value, 'alice@github', 'owners are prefilled with the suggestion');
  const steps = walk(body, (n) => n.className === 'files-steps ingress-steps')[0];
  assert.equal(steps.open, true, 'setup steps open while no secret is set');
  assert.equal(walk(steps, (n) => n.className === 'ingress-code')[0].textContent, '"tagOwners": {\n  "tag:iwato": ["autogroup:admin"]\n}');
  walk(steps, (n) => n.textContent === 'コピー')[0].click(); await flush();
  assert.equal(se.copied.at(-1), '"tagOwners": {\n  "tag:iwato": ["autogroup:admin"]\n}');
  // The secret: sent once, the input emptied, only the hint shown.
  const secretInput = walk(body, (n) => n.type === 'password')[0];
  secretInput.value = ' tskey-client-kXYZ-secretvalueWXYZ ';
  walk(body, (n) => n.textContent === '保存' && n.className === 'button button-primary' && n.type === 'button')[0].click(); await flush();
  assert.deepEqual(posts[0], {path: '/api/ingress/secret', body: {secret: 'tskey-client-kXYZ-secretvalueWXYZ'}, csrf: 'csrf123'});
  assert.equal(secretInput.value, '');
  assert(walk(body, (n) => n.textContent === '設定済み (tskey-client-…WXYZ)').length === 1);
  assert(!walk(body, (n) => /secretvalue/.test(n.textContent || '') || /secretvalue/.test(n.value || '')).length, 'the secret is not kept in the page');
  // Tailscale with owners: the choice survived the secret's refresh.
  assert.equal(walk(body, (n) => n.type === 'radio' && n.value === 'tailscale')[0].checked, true, 'the unsaved choice stays');
  walk(body, (n) => n.attributes['aria-label'] === '通すログイン名')[0].value = 'alice@github\nbob@github';
  walk(body, (n) => n.className === 'stack-form ingress-form')[0].listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts[1].body, {provider: 'tailscale', tag: 'tag:iwato', hostnamePrefix: 'iwato-', owners: ['alice@github', 'bob@github']});
  assert(walk(body, (n) => n.className === 'stack-form ingress-tailscale').every((n) => !n.hidden), 'saved Tailscale shows its settings');
  assert.equal(walk(body, (n) => n.className === 'ingress-state')[0].dataset.state, 'on');
  assert.equal(walk(body, (n) => n.className === 'files-steps ingress-steps')[0].open, false, 'steps fold once a secret is set');
  se.els['settings-close'].click(); assert.equal(se.els['settings-panel'].hidden, true);
  ok('設定: local by default, Tailscale opt-in with prefilled owners, the secret sent once and shown as a hint, setup snippets with copy');
  }

  {
  // 設定: the standard image, since when, and 「標準イメージを更新」 for a moving tag only.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const im = await environment({fake: false, status: 'running'}); let info = {available: true, image: 'ghcr.io/mtsmfm/iwato-sandbox:edge', source: 'pull', moves: true, present: true, created: '2026-10-15T03:04:05.123Z', at: '2026-10-16T01:00:00Z', update: {}};
  const posts = [];
  im.setImagesHandler((path, opt) => { if (opt?.method === 'POST') { posts.push(opt.headers['X-CSRF-Token']); info = {...info, update: {phase: 'updated', message: '新しいイメージを取得しました'}}; return response({ok: true}); } return response(info); });
  im.els['settings-button'].click(); await flush();
  const body = im.els['settings-body']; const section = body.children.find((n) => n.className === 'image-settings');
  assert(section && !section.hidden, 'the image section shows'); assert(body.children.indexOf(section) < body.children.findIndex((n) => n.className === 'ingress-settings'), 'before the port settings');
  assert(walk(section, (n) => n.textContent === 'ghcr.io/mtsmfm/iwato-sandbox:edge').length); assert.match(walk(section, (n) => n.className === 'panel-meta image-times')[0].textContent, /^作成: .+ · 取得: .+/);
  walk(section, (n) => n.textContent === '標準イメージを更新')[0].click(); await flush();
  assert.deepEqual(posts, ['csrf123']); assert(walk(section, (n) => n.textContent === '新しいイメージを取得しました。これから作る・作り直すサンドボックスで使います').length, 'the result shows in the page\'s words');
  const rel = await environment({fake: false, status: 'running'});
  rel.setImagesHandler(() => response({available: true, image: 'ghcr.io/mtsmfm/iwato-sandbox:0.3.0', source: 'pull', moves: false, present: true, update: {}}));
  rel.els['settings-button'].click(); await flush();
  const relSection = rel.els['settings-body'].children.find((n) => n.className === 'image-settings');
  assert.equal(walk(relSection, (n) => n.textContent === '標準イメージを更新').length, 0); assert(walk(relSection, (n) => n.textContent === '最新です').length);
  ok('設定: the standard image with its times; 標準イメージを更新 only for a moving tag (a release says 最新です)');
  }

  {
  // ポート tab: the node, the ports as links (copy), refreshed every 5 s while shown; a stopped sandbox keeps the tab.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  let reads = 0, running = true;
  const pe = await environment({fake: false, status: 'running'});
  pe.setIngressHandler((path) => {
    if (path.endsWith('/ports')) {
      reads++;
      return response({overall: {state: 'on'}, provider: 'tailscale', node: {state: 'connected', dnsName: 'iwato-api.tail0000.ts.net'}, hostname: 'iwato-api', running,
        ports: running ? [{port: 5173, addresses: ['127.0.0.1', '::1'], url: 'https://iwato-api.tail0000.ts.net:5173/'}, {port: 4000, addresses: ['::1'], ipv6Only: true, url: 'https://iwato-api.tail0000.ts.net:4000/'}, {port: 9229, addresses: ['127.0.0.1'], disabled: true}, {port: 5432, addresses: ['<b>x</b>']}] : []});
    }
    return response({enabled: true, owners: [], secret: {set: true}, status: {state: 'on'}, nodes: []});
  });
  const tab = walk(pe.card(), (n) => n.className === 'card-tab' && n.textContent === 'ポート')[0];
  assert(tab, 'a ポート tab'); tab.click(); await flush();
  const panel = pe.side('ports-panel'); assert.equal(panel.hidden, false); assert.equal(pe.card().dataset.tab, 'ports');
  assert.equal(pe.requests.filter((r) => r.path === '/api/sandboxes/api/ports').length, 1);
  const links = walk(panel, (n) => n.className === 'port-link');
  assert.deepEqual(links.map((a) => [a.href, a.target, a.rel]), [['https://iwato-api.tail0000.ts.net:5173/', '_blank', 'noopener noreferrer'], ['https://iwato-api.tail0000.ts.net:4000/', '_blank', 'noopener noreferrer']]);
  assert(walk(panel, (n) => /::1 だけで待っています/.test(n.textContent || '')).length === 1);
  assert(walk(panel, (n) => n.textContent === 'このポートは公開しない設定です').length === 1);
  assert(walk(panel, (n) => n.textContent === '<b>x</b>').length === 1, 'addresses are text');
  assert.match(walk(panel, (n) => n.className === 'ingress-dns')[0].textContent, /iwato-api\.tail0000\.ts\.net/);
  walk(panel, (n) => n.textContent === 'コピー')[0].click(); await flush();
  assert.equal(pe.copied.at(-1), 'https://iwato-api.tail0000.ts.net:5173/');
  const timer = pe.intervals.filter((t) => t.ms === 5000 && !t.cleared).at(-1);
  assert(timer, 'refreshes every 5 s while shown'); const before = reads; timer.fn(); await flush(); assert.equal(reads, before + 1);
  pe.document.hidden = true; timer.fn(); await flush(); assert.equal(reads, before + 1, 'not while the page is hidden'); pe.document.hidden = false;
  // Stopped: the tab stays and says so.
  running = false; pe.sandbox.status = 'stopped'; await pe.tickPoll();
  assert.equal(pe.card().dataset.tab, 'ports');
  assert(walk(panel, (n) => /停止中です/.test(n.textContent || '')).length === 1);
  // Leaving the tab stops the refresh.
  walk(pe.card(), (n) => n.className === 'card-tab' && n.textContent === 'ターミナル')[0].click();
  assert.equal(timer.cleared, true);
  ok('ポート tab: node state, ports as links with copy, ::1-only and disabled notes, 5 s refresh while visible, stopped state');
  }

  {
  // ポート with the local provider (the default): *.localhost links, TCP ports explained, a way to Tailscale.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const le = await environment({fake: false, status: 'running'});
  le.setIngressHandler((path) => path.endsWith('/ports')
    ? response({overall: {state: 'on'}, provider: 'local', node: {state: 'connected', dnsName: 'api.localhost'}, running: true,
      ports: [{port: 5173, addresses: ['127.0.0.1'], url: 'http://5173.api.localhost:17600/'}, {port: 5432, addresses: ['127.0.0.1'], tcpOnly: true}]})
    : response({provider: 'local', owners: [], secret: {set: false}, status: {state: 'on'}, nodes: []}));
  walk(le.card(), (n) => n.className === 'card-tab' && n.textContent === 'ポート')[0].click(); await flush();
  const panel = le.side('ports-panel');
  assert.deepEqual(walk(panel, (n) => n.className === 'port-link').map((a) => a.href), ['http://5173.api.localhost:17600/']);
  assert(walk(panel, (n) => n.textContent === 'この PC で公開中').length === 1);
  assert(walk(panel, (n) => /TCP \(mode tcp\) の設定です/.test(n.textContent || '')).length === 1, 'a TCP port says it cannot open here');
  assert(walk(panel, (n) => /認証はありません/.test(n.textContent || '')).length === 1);
  walk(panel, (n) => n.className === 'ingress-settings-link')[0].click(); await flush();
  assert.equal(le.els['settings-panel'].hidden, false, 'the settings open for Tailscale');
  ok('ポート tab with the local provider: *.localhost links, TCP ports explained, settings for Tailscale');
  }

  {
  // ポート without the ingress: an invitation to the settings.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const off = await environment({fake: false, status: 'running'});
  walk(off.card(), (n) => n.className === 'card-tab' && n.textContent === 'ポート')[0].click(); await flush();
  const panel = off.side('ports-panel');
  assert(walk(panel, (n) => n.textContent === 'ポートを公開していません。').length === 1);
  walk(panel, (n) => n.textContent === '設定を開く')[0].click(); await flush();
  assert.equal(off.els['settings-panel'].hidden, false);
  ok('ポート tab without the ingress opens the settings');
  }
  {
  // A terminal that stopped (the server's list says when) is marked on the
  // sidebar entry and its tab until the owner views it here.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const st = await environment({status: 'running', terms: ['main', '2']}); await flush();
  const nav = st.els['sandbox-nav'].children.find((n) => n.className.includes('nav-sandbox'));
  const navMarked = () => !walk(nav, (n) => n.className === 'stopped-mark')[0].hidden;
  const tabMarked = (name) => walk(st.card(), (n) => n.className.split(' ').includes('term-select') && n.title.startsWith('ターミナル ' + name)).some((n) => n.children.some((c) => c.className === 'stopped-mark'));
  assert(!navMarked());
  st.sandbox.stopped = {main: 100, 2: 100, 'Bad!': 5}; await st.tickPoll();
  assert(navMarked() && tabMarked('2') && !tabMarked('main'), 'the terminal shown is seen; the other is marked');
  walk(st.card(), (n) => n.className.split(' ').includes('term-select') && n.title.startsWith('ターミナル 2'))[0].click(); await flush();
  assert(!navMarked() && !tabMarked('2'), 'viewing it clears the mark');
  walk(st.card(), (n) => n.className.split(' ').includes('card-tab') && n.textContent === 'ポリシー')[0].click(); await flush();
  st.sandbox.stopped = {main: 100, 2: 200}; await st.tickPoll();
  assert(navMarked() && tabMarked('2'), 'stopped again while another tab shows');
  walk(st.card(), (n) => n.className.split(' ').includes('card-tab') && n.textContent === 'ターミナル')[0].click(); await flush();
  assert(!navMarked(), 'back on the terminal');
  ok('a stopped terminal is marked on the sidebar and its tab until viewed');
  }
  {
  // web/i18n.js on its own: t(), the fallback to Japanese, language detection.
  const warnings = [];
  const load = ({stored = null, storage = true, debug = false, languages = []} = {}) => {
    const box = {window: {IwatoDebug: debug}, navigator: {languages}, console: {warn: (m) => warnings.push(m), error() {}},
      localStorage: storage ? {getItem: () => stored, setItem(_k, v) { stored = v; }, removeItem() { stored = null; }} : {getItem() { throw Error('blocked'); }, setItem() { throw Error('blocked'); }, removeItem() { throw Error('blocked'); }}};
    vm.runInNewContext(i18nSource, box); return box.window.IwatoI18n;
  };
  const I = load({languages: ['ja-JP']});
  assert.equal(I.language(), 'ja');
  assert.equal(I.t('settings.language.auto', {language: '日本語'}), '自動 (日本語)');
  assert.equal(I.t('settings.language.auto', {}), '自動 ({language})', 'a missing param stays visible');
  assert.equal(I.t('error.name_invalid', {max: 32}, 'en'), 'The name must start with a lowercase letter and have at most 32 lowercase letters, digits and hyphens.');
  assert.equal(I.t('no.such.key'), 'no.such.key');
  for (const [langs, want] of [[['ja-JP'], 'ja'], [['ja'], 'ja'], [['en-US', 'ja'], 'en'], [['fr-FR', 'ja-JP'], 'ja'], [['zh-CN'], 'en'], [[], 'en']]) assert.equal(I.detect(langs), want, langs.join(','));
  assert.equal(load({languages: ['de', 'en-GB']}).language(), 'en');
  assert.equal(load({languages: ['en-US'], stored: 'ja'}).language(), 'ja', 'the saved choice wins over the browser');
  assert.equal(load({languages: ['ja'], stored: 'bogus'}).preference(), 'auto');
  ok('i18n: interpolation, unknown keys, detection from navigator.languages and the saved choice');
  const D = load({languages: ['en'], debug: true});
  D.dictionaries.ja['test.only_ja'] = 'だけ {x}';
  assert.equal(D.t('test.only_ja', {x: 1}), 'だけ 1', 'a key without English falls back to Japanese');
  D.t('test.only_ja'); assert.deepEqual(warnings, ['i18n: no en string for test.only_ja'], 'debug builds log it once');
  load({languages: ['en']}).t('nothing.here'); assert.equal(warnings.length, 1, 'release builds log nothing');
  assert.equal(D.serverError('name_invalid', {max: 63, evil: {toString() { throw Error('x'); }}}, 'サーバーの文'), 'The name must start with a lowercase letter and have at most 63 lowercase letters, digits and hyphens.');
  assert.equal(D.serverError('not_known', {}, 'サーバーの文'), 'サーバーの文', 'unknown codes show the server\'s text');
  assert.equal(D.serverError(undefined, undefined, 'サーバーの文'), 'サーバーの文');
  const B = load({languages: ['ja'], storage: false});
  assert.equal(B.preference(), 'auto'); B.setPreference('en');
  assert.equal(B.language(), 'en'); assert.equal(B.preference(), 'en', 'without storage the choice lasts for the page');
  assert.equal(I.serverError('scope_many_providers', {providers: ['a', 'b']}, 'x'), '複数のプロバイダー (a、b) の宛先に当たります', 'a list param is joined in Japanese');
  assert.equal(D.serverError('scope_many_providers', {providers: ['a', 'b']}, 'x'), 'It reaches the destinations of several providers (a, b)', 'and in English');
  assert.equal(D.localized('profile.iwato-github.credential.token', 'トークン'), 'Token'); assert.equal(I.localized('profile.iwato-github.credential.token', 'トークン'), 'トークン', 'Japanese keeps the server\'s text');
  ok('i18n: Japanese fallback, debug logging, server error codes, blocked storage');
  }
  {
  // web/refusals.js: OpenShell's refusals worded from the server's codes and pairs, as the server words them in Japanese.
  const load = (languages) => { const box = {window: {}, navigator: {languages}}; vm.runInNewContext(i18nSource, box); vm.runInNewContext(refusalsSource, box); return box.window.IwatoRefusals; };
  const J = load(['ja']), E = load(['en']);
  const a = {rule: '_provider_github', provider: {name: 'github', label: 'GitHub', profile: 'iwato-github'}, with: 'allow_x', endpoint: 'api.github.com:443', diffs: [{field: 'enforcement', value: 'enforce', withValue: 'audit'}], details: ['サーバーの文'], fix: ''};
  assert.equal(J.message('ambiguous_edit', null, {ambiguities: [a]}, 'x'), 'この変更は OpenShell に拒否されます: api.github.com:443 で プロバイダー github (GitHub) の規則 _provider_github と 規則 allow_x の設定が食い違います (検査のモード: _provider_github は 遮断 (enforce)、allow_x は 監査 (audit))。OpenShell は、同じ宛先 (ホスト・ポート・パス) に当たる規則どうしで、検査のしかたがそろっていることを求めます (network endpoint ambiguity)。プロバイダーの規則は変えられないので、検査のモードを 遮断 (enforce) にそろえてください。プロバイダーのプログラムには、プロバイダーの規則が既に許可しています。');
  assert.equal(E.message('ambiguous_edit', null, {ambiguities: [a]}, 'x'), 'OpenShell would refuse this change: at api.github.com:443, rule _provider_github of provider github (GitHub) and rule allow_x disagree (Mode: enforce in _provider_github, audit in allow_x). OpenShell requires rules for the same destination (host, port, path) to inspect it the same way (network endpoint ambiguity). The provider\'s rule cannot change: set Mode to enforce. The provider\'s rule already allows the provider\'s programs.');
  assert.match(E.message('ambiguous_approve', {choices: ['read', 'nope']}, {ambiguities: [a]}, 'x'), /It can be approved with "Read only"\.$/);
  assert.equal(J.message('ambiguous_edit', null, {ambiguities: []}, 'サーバーの文'), 'サーバーの文', 'without the pairs, the server\'s text');
  assert.equal(J.message('provider_fix_remove_failed', {rule: 'r', host: 'h', detail: 'raw'}, {gateway: [a]}, 'x'), '規則 r の h を外せませんでした: OpenShell が拒否しました: api.github.com:443 で プロバイダー github (GitHub) の規則 _provider_github と 規則 allow_x の設定が食い違います (検査のモード: _provider_github は 遮断 (enforce)、allow_x は 監査 (audit))。同じ宛先 (ホスト・ポート・パス) に当たる規則どうしで、検査のしかたがそろっている必要があります。承認一覧で、プロバイダーではない方の規則の宛先をもう一方に合わせる (監査なら「遮断に切り替え」) か、外してください。', 'the gateway\'s refusal as the detail');
  assert.match(E.message('gateway_ambiguous', null, {gateway: []}, 'x'), /^OpenShell refused it: rules for the same destination/);
  const c = {rule: 'allow_api', host: 'api.github.com', ports: [443], mode: 'L4-only', provider: 'gh', label: 'GitHub', profile: 'iwato-github'};
  assert.match(E.message('credential_uninspected', null, {conflicts: [c]}, 'x'), /^This would allow the destinations that get the value of gh \(GitHub\), attached to this sandbox, uninspected: rule allow_api: api\.github\.com:443 \(L4-only\)\./);
  assert.match(E.message('provider_attach_refused', {provider: 'gh'}, {conflicts: [c], ambiguities: [a]}, 'x'), /^The policy allows .* OpenShell would refuse attaching gh: at api\.github\.com:443/);
  assert.equal(J.message('ambiguous_scope', {sandbox: 'dev'}, {ambiguities: [a]}, 'x'), 'サンドボックス dev のポリシーと食い違うので変えられません: api.github.com:443 で プロバイダー github (GitHub) の規則 _provider_github と 規則 allow_x の設定が食い違います (検査のモード: _provider_github は 遮断 (enforce)、allow_x は 監査 (audit))。OpenShell は、同じ宛先 (ホスト・ポート・パス) に当たる規則どうしで、検査のしかたがそろっていることを求めます (network endpoint ambiguity)。', 'a scope change: no fix, as the server words it');
  assert.match(E.message('ambiguous_scope', {sandbox: 'dev'}, {ambiguities: [a]}, 'x'), /^Cannot change it: it would disagree with the policy of sandbox dev: at api\.github\.com:443, .*\(network endpoint ambiguity\)\.$/);
  assert.match(E.message('provider_scope_conflict', {sandbox: 'dev'}, {conflicts: [c]}, 'x'), /^Sandbox dev: The policy allows the destinations that get the value of gh \(GitHub\) uninspected: rule allow_api: api\.github\.com:443 \(L4-only\)\./);
  assert.equal(J.message('provider_scope_conflict', {sandbox: 'dev'}, {conflicts: []}, 'サーバーの文'), 'サーバーの文');
  assert.equal(J.blocked('audit', [a], 'x'), '「監査」は選べません: api.github.com:443 には プロバイダー github (GitHub) の規則 _provider_github があり、設定が食い違います (検査のモード: _provider_github は 遮断 (enforce)、allow_x は 監査 (audit))。OpenShell は同じ宛先の規則どうしで検査のしかたがそろっていることを求めます。');
  assert.deepEqual([...J.details({...a, diffs: []})], ['サーバーの文'], 'no diffs: the server\'s details');
  ok('refusals.js: ambiguities, the gateway\'s refusal, credential conflicts and blocked choices, in Japanese and English');
  }
  {
  // The example screens: login in the browser's language, the settings'
  // language section switching and remembering it.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const en = await environment({languages: ['en-US']});
  en.els['login-input'].value = 'not a token'; en.els['login-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.equal(en.els['notice-title'].textContent, 'Cannot sign in'); assert.equal(en.els['notice-text'].textContent, 'This is not a valid link or token.');
  const lg = await environment();
  lg.els['settings-button'].click(); await flush();
  const select = walk(lg.els['settings-body'], (n) => n.className === 'text-input language-select')[0];
  assert.equal(select.value, 'auto'); assert.equal(select.attributes['aria-label'], '表示する言語');
  assert.deepEqual(select.children.map((o) => [o.value, o.textContent]), [['auto', '自動 (日本語)'], ['ja', '日本語'], ['en', 'English']]);
  select.value = 'en'; select.listeners.change(); await flush();
  assert.equal(lg.stored.get('iwato-lang'), 'en'); assert.equal(lg.context.window.IwatoI18n.language(), 'en');
  assert.equal(select.attributes['aria-label'], 'Display language'); assert.equal(select.children[0].textContent, 'Automatic (日本語)', 'automatic names the browser\'s language');
  lg.els['login-input'].value = 'x'; lg.els['login-form'].listeners.submit({preventDefault() {}}); await flush();
  assert.equal(lg.els['notice-title'].textContent, 'Cannot sign in');
  select.value = 'auto'; select.listeners.change(); await flush();
  assert(!lg.stored.has('iwato-lang'), 'automatic forgets the choice'); assert.equal(lg.context.window.IwatoI18n.language(), 'ja');
  ok('login follows the browser language; the settings switch it, remember it and go back to automatic');
  }
  {
  // English: the sidebar, a card and a coded server error; switching back
  // to Japanese redraws the card without a reload.
  const en = await environment({languages: ['en-US'], fake: false});
  const card = en.card(), [start] = en.buttons();
  assert.equal(en.els['server-label'].textContent, 'Connected to the local server');
  assert.equal(card.children[0].children[1].textContent, 'Not created'); assert.equal(start.textContent, 'Start');
  assert.equal(card.children[2].children[0].children[0].textContent, 'Terminal'); assert.equal(card.attributes['aria-label'], 'api terminal');
  en.setStartHandler(() => response({error: '起動処理が進行中です', code: 'busy_start'}, 409));
  start.click(); await flush();
  assert.equal(card.children[7].children[0].textContent, 'Could not start: The sandbox is starting.', 'the code is translated');
  en.setStartHandler(() => response({error: 'サーバーの文', code: 'not_known'}, 500));
  start.click(); await flush();
  assert.equal(card.children[7].children[0].textContent, 'Could not start: サーバーの文', 'an unknown code shows the server\'s text');
  en.context.window.IwatoI18n.setPreference('ja'); await flush();
  assert.equal(card.children[0].children[1].textContent, '未作成'); assert.equal(start.textContent, '起動');
  ok('English: the sidebar, the card and coded errors; Japanese again without a reload');
  }
  {
  // 設定の「すべて消す」: the dialog says what goes and what stays, needs
  // the typed word, sends it with CSRF, and ends the login when closed.
  const walk = (n, pred, out = []) => { if (pred(n)) out.push(n); n.children.forEach((c) => walk(c, pred, out)); return out; };
  const box = {window: {}}; vm.runInNewContext(resetSource, box); const R = box.window.IwatoReset;
  assert(R.ready(' reset ') && !R.ready('Reset') && !R.ready(''));
  assert.deepEqual(R.resultView({warnings: ['a', 3, null, 'b'.repeat(2000)]}).warnings.map((w) => w.length), [1, 1000]);
  const boxEn = {window: {}, navigator: {languages: ['en']}}; vm.runInNewContext(i18nSource, boxEn); vm.runInNewContext(resetSource, boxEn);
  const coded = {warnings: ['could not delete volume v: busy', 'x'], warningCodes: [{code: 'volume', params: {name: 'v', detail: 'busy'}}, {code: 'unknown'}]};
  assert.deepEqual([...boxEn.window.IwatoReset.resultView(coded).warnings], ['Could not delete volume v: busy', 'x'], 'a coded warning in the page\'s words, others as sent');
  const re = await environment({fake: false, status: 'running'});
  const posts = [];
  const answer = deferred();
  re.setResetHandler((path, opt) => { if (opt?.method === 'POST') { posts.push({body: JSON.parse(opt.body), csrf: opt.headers['X-CSRF-Token']}); return answer.promise; } return response({running: true, step: 'ゲートウェイを削除しています…', seconds: 3}); });
  re.els['settings-button'].click(); await flush();
  const body = re.els['settings-body'];
  const section = body.children.at(-1);
  assert.equal(section.className, 'reset-settings', 'the reset is the last section of the settings');
  walk(section, (n) => n.tagName === 'button' && n.textContent === 'すべて消す')[0].click(); await flush();
  const dialog = walk(section, (n) => n.className.includes('reset-dialog'))[0];
  assert(dialog && !dialog.hidden, 'the dialog opens');
  const all = walk(dialog, () => true).map((n) => n.textContent || '').join('\n');
  assert(/サンドボックス/.test(all) && /ゲートウェイ/.test(all) && /VHDX/.test(all) && /Iwato のプログラム/.test(all), 'what goes and what stays');
  const kept = walk(dialog, (n) => n.className.includes('reset-kept'))[0];
  assert(/presets\//.test(kept.textContent), 'the owner\'s files stay by default');
  const owner = walk(dialog, (n) => n.type === 'checkbox')[0];
  owner.checked = true; owner.listeners.change();
  assert(!/presets\//.test(kept.textContent), 'checked: they go too');
  const input = walk(dialog, (n) => n.type === 'text')[0];
  const submit = walk(dialog, (n) => n.type === 'submit')[0];
  const form = walk(dialog, (n) => n.tagName === 'form')[0];
  assert.equal(submit.disabled, true);
  input.value = 'rest'; input.listeners.input(); assert.equal(submit.disabled, true);
  form.listeners.submit({preventDefault() {}}); await flush();
  assert.equal(posts.length, 0, 'nothing is sent without the word');
  input.value = 'reset'; input.listeners.input(); assert.equal(submit.disabled, false);
  form.listeners.submit({preventDefault() {}}); await flush();
  assert.deepEqual(posts, [{body: {confirm: 'reset', ownerFiles: true}, csrf: 'csrf123'}]);
  assert(submit.disabled && walk(dialog, (n) => n.textContent === 'キャンセル')[0].disabled, 'no second reset, no cancel while it runs');
  answer.resolve(response({ok: true, warnings: ['ノード iwato-api をログアウトできませんでした']})); await flush();
  assert(walk(dialog, (n) => /新しい URL/.test(n.textContent || '')).length === 1, 'says to open the console\'s new URL');
  assert(walk(dialog, (n) => n.textContent === 'ノード iwato-api をログアウトできませんでした').length === 1, 'what could not be done');
  assert.equal(re.els['login-panel'].hidden, true);
  walk(dialog, (n) => n.textContent === '閉じる')[0].click(); await flush();
  assert(dialog.hidden && !re.els['login-panel'].hidden && re.els['settings-panel'].hidden, 'closing ends the session on the page');
  ok('すべて消す: the typed word, what stays, the request with CSRF, the end of the login');
  }
  console.log('\n' + tests + ' UI behavior tests passed. These are VM unit tests, not rendered-browser validation.');
})().catch(error => { console.error(error); process.exitCode = 1; });
