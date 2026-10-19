// The files tab (ファイル): a sandbox's workspace (the home with the standard
// image) as a network drive. Iwato serves WebDAV at /dav/ (files.go); this
// shows the addresses for each OS, short steps, and the credentials: app
// passwords (user name = ID) and this PC's secret URLs. A secret shows once,
// as text, and is never stored here.
(() => {
  'use strict';
  // The UI's strings (web/i18n.js), looked up when used.
  const t = (key, params) => window.IwatoI18n.t(key, params);

  // davURL is a sandbox's WebDAV folder (token: a secret URL's).
  function davURL(origin, name, token) {
    return String(origin).replace(/\/+$/, '') + '/dav/' + (token ? token + '/' : '') + (name ? encodeURIComponent(name) + '/' : '');
  }

  // uncPath is the same folder as Windows Explorer writes it:
  // \\host@SSL\dav\name (https), \\127.0.0.1@17600\dav\token\name (http).
  function uncPath(origin, name, token) {
    let u;
    try { u = new URL(origin); } catch (_) { return ''; }
    const https = u.protocol === 'https:';
    let host = u.hostname + (https ? '@SSL' : '');
    if (u.port) host += '@' + u.port;
    else if (!https) host += '@80';
    return '\\\\' + host + '\\dav' + (token ? '\\' + token : '') + (name ? '\\' + name : '');
  }

  // ago tells how long ago a time was, roughly.
  function ago(iso, now = Date.now()) {
    const at = Date.parse(iso || '');
    if (!Number.isFinite(at)) return t('files.ago.unused');
    const s = Math.max(0, (now - at) / 1000);
    if (s < 60) return t('files.ago.now');
    if (s < 3600) return t('files.ago.minutes', { count: Math.floor(s / 60) });
    if (s < 86400) return t('files.ago.hours', { count: Math.floor(s / 3600) });
    return t('files.ago.days', { count: Math.floor(s / 86400) });
  }

  const text = (v, max) => typeof v === 'string' ? v.slice(0, max) : '';

  // keyView keeps what the page shows of a credential from the server.
  function keyView(k) {
    return {
      id: text(k?.id, 64), name: text(k?.name, 80), kind: k?.kind === 'url' ? 'url' : 'password',
      sandbox: text(k?.sandbox, 64), readOnly: k?.readOnly === true, lastUsed: text(k?.lastUsed, 64), lastClient: text(k?.lastClient, 120)
    };
  }

  // panel makes the FilesPanel class with the page's helpers.
  function panel({ element, button, panelHeader, api, askConfirm }) {
    async function copy(value, label) {
      try {
        if (typeof navigator === 'undefined' || !navigator.clipboard) throw new Error('no clipboard');
        await navigator.clipboard.writeText(value);
        label.textContent = t('files.copied');
      } catch (_) {
        label.textContent = t('files.copyFailed');
      }
    }
    // A read-only value with a copy button.
    function copyRow(title, value) {
      const row = element('div', 'files-row');
      const name = element('span', 'files-label', title);
      const input = element('input', 'text-input files-value');
      input.type = 'text';
      input.readOnly = true;
      input.value = value;
      input.setAttribute('aria-label', title);
      input.addEventListener('focus', () => input.select?.());
      const status = element('span', 'files-copied');
      status.setAttribute('role', 'status');
      const b = button('button-subtle', t('common.copy'), () => copy(value, status));
      b.setAttribute('aria-label', t('files.copyLabel', { what: title }));
      row.append(name, input, b, status);
      return row;
    }
    function steps(title, items, note) {
      const d = element('details', 'files-steps');
      d.append(element('summary', '', title));
      const ol = element('ol');
      for (const s of items) ol.append(element('li', '', s));
      d.append(ol);
      if (note) d.append(element('p', 'panel-meta', note));
      return d;
    }
    function radios(name, legend, choices) {
      const box = element('div', 'files-choice');
      box.setAttribute('role', 'radiogroup');
      box.setAttribute('aria-label', legend);
      box.append(element('span', 'files-legend', legend));
      const inputs = [];
      for (const [value, label, checked] of choices) {
        const l = element('label', 'scope-choice');
        const i = element('input');
        i.type = 'radio';
        i.name = name;
        i.value = value;
        i.checked = !!checked;
        inputs.push(i);
        l.append(i, element('span', '', label));
        box.append(l);
      }
      box.value = () => inputs.find((i) => i.checked)?.value || choices[0][0];
      return box;
    }

    return class FilesPanel {
      constructor(sandbox) {
        this.sandbox = sandbox;
        this.seq = 0;
        this.root = element('section', 'side-panel files-panel');
        this.root.setAttribute('aria-label', t('tab.files'));
        this.root.hidden = true;
        this.refreshButton = button('button-subtle', t('common.reload'), () => this.refresh());
        this.closeButton = button('button-subtle', t('common.close'), () => this.onClose?.());
        this.state = element('p', 'files-state');
        this.state.setAttribute('role', 'status');
        // Where the files are: the home, or the workspace without the
        // daemon's SFTP server (recreating gives the home).
        this.rootNote = element('p', 'panel-meta files-root-note');
        this.rootNote.hidden = true;
        this.filesRoot = null;
        this.where = element('div', 'files-where');
        this.made = element('div', 'files-made');
        this.made.hidden = true;
        this.made.setAttribute('role', 'status');
        this.list = element('div', 'volume-list files-keys');
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        this.form = this.createForm();
        const keysHead = element('h4', 'files-head', t('files.keys.title'));
        this.root.append(panelHeader({ key: 'tab.files' }, this.refreshButton, this.closeButton), this.state, this.rootNote, this.where, keysHead,
          element('p', 'panel-meta', t('files.keys.note')), this.made, this.list, this.form, this.status);
      }

      createForm() {
        const form = element('form', 'stack-form files-form');
        form.append(element('strong', '', t('files.create')));
        this.nameInput = element('input', 'text-input');
        this.nameInput.type = 'text';
        this.nameInput.maxLength = 40;
        this.nameInput.placeholder = t('files.device.placeholder');
        this.nameInput.setAttribute('aria-label', t('files.device'));
        this.kind = radios('files-kind-' + Math.random().toString(36).slice(2), t('files.kind'), [
          ['password', t('files.kind.password'), true],
          ['url', t('files.kind.url')]
        ]);
        this.scope = radios('files-scope-' + Math.random().toString(36).slice(2), t('files.scope'), [['this', t('files.scope.this'), true], ['all', t('files.scope.all')]]);
        this.perm = radios('files-perm-' + Math.random().toString(36).slice(2), t('files.perm'), [['rw', t('files.perm.rw'), true], ['ro', t('files.perm.ro')]]);
        this.submit = element('button', 'button button-primary', t('files.create'));
        this.submit.type = 'submit';
        const actions = element('div', 'login-form');
        actions.append(this.submit);
        form.append(this.nameInput, this.kind, this.scope, this.perm, actions);
        form.addEventListener('submit', (event) => { event.preventDefault(); this.create(); });
        return form;
      }

      show() { this.root.hidden = false; this.refresh(); }
      hide() { this.root.hidden = true; this.made.hidden = true; this.made.replaceChildren(); }

      async refresh() {
        const seq = ++this.seq;
        try {
          const data = await api('/api/files');
          if (seq !== this.seq) return;
          this.info = { publicOrigin: text(data.publicOrigin, 300), localOrigin: text(data.localOrigin, 300), available: data.available !== false };
          this.keys = (Array.isArray(data.keys) ? data.keys : []).slice(0, 100).map(keyView);
          this.status.textContent = '';
          this.render();
        } catch (error) {
          if (seq === this.seq) this.status.textContent = error.message;
        }
        this.loadRoot(seq);
      }

      // loadRoot asks where a running sandbox's files are (the editor's
      // route) and notes when that is not the home.
      async loadRoot(seq) {
        if (this.sandbox.status !== 'running' || !this.info?.available) { this.filesRoot = null; this.renderRoot(); return; }
        try {
          const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/root');
          if (seq !== this.seq) return;
          this.filesRoot = { path: text(data.path, 300), home: data.home !== false };
        } catch (_) {
          if (seq === this.seq) this.filesRoot = null;
        }
        this.renderRoot();
      }

      renderRoot() {
        const r = this.filesRoot;
        this.rootNote.hidden = !r || r.home || this.sandbox.status !== 'running';
        this.rootNote.textContent = r && !r.home ? t('files.rootNote', { path: r.path }) : '';
      }

      render() {
        const name = this.sandbox.name;
        const running = this.sandbox.status === 'running';
        this.state.dataset.state = running ? 'running' : 'stopped';
        this.state.textContent = !this.info.available ? t('files.state.unavailable')
          : running ? t('files.state.running') : t('files.state.stopped');
        const where = [];
        const pub = this.info.publicOrigin;
        if (pub) {
          where.push(element('h4', 'files-head', t('files.where.title')));
          where.push(copyRow('Windows', davURL(pub, name)), copyRow(t('files.where.windowsPath'), uncPath(pub, name)), copyRow('Mac', davURL(pub, name)), copyRow(t('files.where.phone'), davURL(pub, name)));
          where.push(element('p', 'panel-meta', t('files.where.note')));
          where.push(
            steps(t('files.steps.windows'), [t('files.steps.windows.1'), t('files.steps.windows.2'), t('files.steps.windows.3')], t('files.steps.windows.note')),
            steps(t('files.steps.mac'), [t('files.steps.mac.1'), t('files.steps.mac.2'), t('files.steps.mac.3')]),
            steps(t('files.steps.phone'), [t('files.steps.phone.1'), t('files.steps.phone.2'), t('files.steps.phone.3')], t('files.steps.phone.note')));
        } else {
          where.push(element('p', 'panel-meta files-note', t('files.where.localOnly')));
        }
        where.push(steps(t('files.steps.local'), [t('files.steps.local.1'), t('files.steps.local.2'), t('files.steps.local.3')], t('files.steps.local.note')));
        this.where.replaceChildren(...where);
        this.renderKeys();
      }

      renderKeys() {
        const rows = this.keys.map((k) => {
          const row = element('div', 'volume-row files-key');
          const info = element('div', 'volume-info');
          info.append(element('span', 'volume-path', k.name));
          const scope = k.sandbox ? k.sandbox : t('files.key.all');
          const meta = (k.kind === 'url' ? t('files.key.url') : 'ID ' + k.id) + ' · ' + scope + ' · ' + (k.readOnly ? t('files.perm.ro') : t('files.perm.rw')) + ' · ' + ago(k.lastUsed);
          const metaNode = element('span', 'panel-meta', meta);
          if (k.lastClient) metaNode.title = t('files.key.lastClient', { client: k.lastClient });
          info.append(metaNode);
          const revoke = button('button-disconnect', t('files.revoke'), () => this.revoke(k));
          revoke.setAttribute('aria-label', t('files.revoke.label', { name: k.name }));
          row.append(info, revoke);
          return row;
        });
        if (!rows.length) rows.push(element('p', 'panel-meta', t('files.keys.none')));
        this.list.replaceChildren(...rows);
      }

      async create() {
        const name = this.nameInput.value.trim();
        if (!name) { this.status.textContent = t('files.device.missing'); this.nameInput.focus(); return; }
        const kind = this.kind.value();
        const body = { name, kind, sandbox: this.scope.value() === 'this' ? this.sandbox.name : '', readOnly: this.perm.value() === 'ro' };
        this.submit.disabled = true;
        try {
          const made = await api('/api/files/keys', { method: 'POST', body: JSON.stringify(body) });
          this.showMade(keyView(made.key), text(made.secret, 200));
          this.nameInput.value = '';
          this.status.textContent = '';
          await this.refresh();
        } catch (error) {
          this.status.textContent = error.message;
        } finally {
          this.submit.disabled = false;
        }
      }

      // The secret, once: the page keeps it only in these inputs.
      showMade(key, secret) {
        const name = this.sandbox.name;
        const parts = [element('strong', '', t('files.made', { name: key.name }))];
        if (key.kind === 'url') {
          const local = this.info?.localOrigin || location.origin;
          parts.push(copyRow(t('files.where.windowsPath'), uncPath(local, name, secret)), copyRow('URL', davURL(local, name, secret)));
          if (!key.sandbox) parts.push(element('p', 'panel-meta', t('files.made.all', { path: uncPath(local, '', secret) })));
        } else {
          parts.push(copyRow(t('files.made.user'), key.id), copyRow(t('files.made.password'), secret));
          if (this.info?.publicOrigin) parts.push(copyRow('URL', davURL(this.info.publicOrigin, name)));
        }
        const close = button('button-subtle', t('common.close'), () => { this.made.hidden = true; this.made.replaceChildren(); });
        parts.push(close);
        this.made.replaceChildren(...parts);
        this.made.hidden = false;
      }

      async revoke(k) {
        if (!await askConfirm(t(k.kind === 'url' ? 'files.revoke.confirmURL' : 'files.revoke.confirmPassword', { name: k.name }), { ok: t('files.revoke'), danger: true })) return;
        try {
          await api('/api/files/keys/' + encodeURIComponent(k.id) + '/revoke', { method: 'POST' });
          await this.refresh();
        } catch (error) {
          this.status.textContent = error.message;
        }
      }

      dispose() { this.root.remove(); }
    };
  }

  window.IwatoFiles = { davURL, uncPath, ago, panel };
})();
