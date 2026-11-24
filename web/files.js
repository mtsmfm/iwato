// The files (ファイル): a sandbox's workspace (the home with the standard
// image) as a network drive. Iwato serves WebDAV at /dav/ (files.go). The
// files tab offers the WebDAV guide and lists the devices that see this
// sandbox; the settings list every credential with its revoke (KeysSection).
// The guide makes the credential: an app password (user name = ID), or for
// Windows on this PC a secret URL (Windows sends no Basic authentication
// over http). A secret shows once, as text, and is never stored here.
(() => {
  'use strict';
  // The UI's strings (web/i18n.js), looked up when used.
  const t = (key, params) => window.IwatoI18n.t(key, params);

  // davURL is a sandbox's WebDAV folder (token: a secret URL's; no name:
  // the list of the sandboxes the credential sees).
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

  function filesView(data) {
    return {
      info: { publicOrigin: text(data?.publicOrigin, 300), localOrigin: text(data?.localOrigin, 300), available: data?.available !== false },
      keys: (Array.isArray(data?.keys) ? data.keys : []).slice(0, 100).map(keyView)
    };
  }

  // keyMeta is a credential's scope, access and last use, for the lists.
  const keyMeta = (k) => [k.sandbox || t('files.key.all'), k.readOnly ? t('files.perm.ro') : t('files.perm.rw'), ago(k.lastUsed)].join(' · ');

  // make gives the files tab (FilesPanel), the settings' list (KeysSection)
  // and the guide, with the page's helpers. startTailscale opens the
  // Tailscale guide (another device needs the tailnet's address).
  function make({ element, button, panelHeader, api, askConfirm, startTailscale }) {
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
    // choice is a radio with a title and a line under it.
    function choice(name, checked, title, hint, onPick) {
      const label = element('label', 'scope-choice');
      const radio = element('input');
      radio.type = 'radio';
      radio.name = name;
      radio.checked = checked;
      radio.addEventListener('change', () => { if (radio.checked) onPick(); });
      const words = element('span', 'ingress-provider-text');
      words.append(element('span', '', title));
      if (hint) words.append(element('span', 'panel-meta', hint));
      label.append(radio, words);
      return label;
    }

    // guide runs the WebDAV guide: the device, its name and access (and,
    // from a sandbox, whether only that one), the values to enter, then
    // waits for the credential's first use. sandbox: the tab's sandbox, or
    // '' from the settings.
    async function guide({ sandbox = '', onChange } = {}) {
      const g = window.IwatoGuide.make({ element, button });
      const { info } = filesView(await api('/api/files'));
      const local = info.localOrigin || location.origin;
      const ctx = { device: 'win', name: '', readOnly: false, limit: false, made: null, closed: false };
      const errorLine = () => { const p = element('p', 'guide-error'); p.setAttribute('role', 'alert'); return p; };
      const wanted = (c) => JSON.stringify({ name: c.name, kind: c.device === 'win' ? 'url' : 'password', sandbox: c.limit ? sandbox : '', readOnly: c.readOnly });
      const steps = [
        { key: 'dav.guide.device', render(body, c, nav) {
          const name = 'dav-device-' + Math.random().toString(36).slice(2);
          const box = element('div', 'stack-form guide-choices');
          box.append(
            choice(name, c.device === 'win', t('dav.guide.device.win'), t('dav.guide.device.win.hint'), () => { c.device = 'win'; }),
            choice(name, c.device === 'mac', t('dav.guide.device.mac'), t('dav.guide.device.mac.hint'), () => { c.device = 'mac'; }),
            choice(name, c.device === 'phone', t('dav.guide.device.phone'), info.publicOrigin ? t('dav.guide.device.phone.hint') : t('dav.guide.device.phone.needsTailscale'), () => { c.device = 'phone'; }));
          body.append(box);
          return () => {
            if (c.device !== 'phone' || info.publicOrigin) return true;
            nav.close();
            startTailscale?.();
            return false;
          };
        } },
        { key: 'dav.guide.name', render(body, c) {
          const input = element('input', 'text-input');
          input.type = 'text';
          input.maxLength = 40;
          input.value = c.name || t(c.device === 'phone' ? 'dav.guide.name.phone' : 'dav.guide.name.pc');
          const label = element('label', 'ingress-field');
          label.append(element('span', 'ingress-label', t('dav.guide.name.label')), input);
          const name = 'dav-perm-' + Math.random().toString(36).slice(2);
          const perms = element('div', 'stack-form guide-choices');
          perms.append(
            choice(name, !c.readOnly, t('files.perm.rw'), t('dav.guide.name.rw.hint'), () => { c.readOnly = false; }),
            choice(name, c.readOnly, t('files.perm.ro'), t('dav.guide.name.ro.hint'), () => { c.readOnly = true; }));
          const line = errorLine();
          body.append(label, perms);
          if (sandbox) {
            const more = element('details', 'guide-more');
            more.append(element('summary', '', t('dav.guide.name.more')));
            const only = element('label', 'scope-choice');
            const box = element('input');
            box.type = 'checkbox';
            box.checked = c.limit;
            box.addEventListener('change', () => { c.limit = box.checked; });
            only.append(box, element('span', '', t('dav.guide.name.only', { name: sandbox })));
            more.append(only);
            body.append(more);
          }
          body.append(line);
          // The credential is made here, once; going back and changing the
          // choices replaces it.
          return async () => {
            c.name = input.value.trim();
            if (!c.name) { line.textContent = t('files.device.missing'); input.focus?.(); return false; }
            if (c.made && c.made.wanted === wanted(c)) return true;
            try {
              const old = c.made;
              const made = await api('/api/files/keys', { method: 'POST', body: wanted(c) });
              c.made = { key: keyView(made.key), secret: text(made.secret, 200), wanted: wanted(c) };
              if (old) await api('/api/files/keys/' + encodeURIComponent(old.key.id) + '/revoke', { method: 'POST' }).catch(() => {});
              onChange?.();
              return true;
            } catch (error) {
              line.textContent = error.message;
              return false;
            }
          };
        } },
        { key: 'dav.guide.connect', render(body, c) {
          const { key, secret } = c.made;
          const root = key.sandbox;
          let rows;
          if (c.device === 'win') {
            rows = [['guide.where', t('dav.guide.connect.win.where')], ['guide.what', [t('dav.guide.connect.win.what'), copyRow(t('dav.guide.connect.folder'), uncPath(local, root, secret))]]];
          } else if (c.device === 'mac') {
            rows = [['guide.where', t('dav.guide.connect.mac.where')], ['guide.what', [t('dav.guide.connect.mac.what'), copyRow(t('dav.guide.connect.address'), davURL(local, root)), copyRow(t('files.made.user'), key.id), copyRow(t('files.made.password'), secret)]]];
          } else {
            rows = [['guide.where', t('dav.guide.connect.phone.where')], ['guide.what', [t('dav.guide.connect.phone.what'), copyRow(t('dav.guide.connect.address'), davURL(info.publicOrigin, root)), copyRow(t('files.made.user'), key.id), copyRow(t('files.made.password'), secret)]]];
          }
          body.append(g.section(rows), element('p', 'panel-meta', root ? t('dav.guide.connect.one', { name: root }) : t('dav.guide.connect.all')));
          if (c.device === 'win') body.append(element('p', 'panel-meta', t('dav.guide.connect.win.limit')));
          body.append(element('p', 'panel-meta', t('dav.guide.connect.once')));
        } },
        { key: 'dav.guide.check', render(body, c, nav) {
          const st = g.status();
          st.set('wait', t('dav.guide.check.waiting'));
          body.append(st.box, element('p', 'panel-meta', t('dav.guide.check.note')));
          nav.setNext(false);
          const id = c.made.key.id;
          const seen = (k) => { st.set('ok', t('dav.guide.check.used', { name: k.name, client: k.lastClient || '—' })); nav.setNext(true); };
          const poll = c.poll = (c.poll || 0) + 1;
          (async () => {
            while (!c.closed && c.poll === poll) {
              try {
                const k = filesView(await api('/api/files')).keys.find((x) => x.id === id);
                if (k?.lastUsed && c.poll === poll) { seen(k); return; }
              } catch (_) { /* tried again */ }
              await new Promise((r) => setTimeout(r, 3000));
            }
          })();
        } }
      ];
      return g.run({ titleKey: 'dav.guide.title', steps, ctx, onDone: () => onChange?.(), onClose: (c) => { c.closed = true; } });
    }

    // A credential's row: its name, scope, access, last use; revoke in the settings.
    function keyRow(k, revoke) {
      const row = element('div', 'volume-row files-key');
      const info = element('div', 'volume-info');
      info.append(element('span', 'volume-path', k.name));
      const metaNode = element('span', 'panel-meta', (k.kind === 'url' ? t('files.key.url') : 'ID ' + k.id) + ' · ' + keyMeta(k));
      if (k.lastClient) metaNode.title = t('files.key.lastClient', { client: k.lastClient });
      info.append(metaNode);
      row.append(info);
      if (revoke) {
        const b = button('button-disconnect', t('files.revoke'), () => revoke(k));
        b.setAttribute('aria-label', t('files.revoke.label', { name: k.name }));
        row.append(b);
      }
      return row;
    }

    class FilesPanel {
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
        this.body = element('div', 'files-body');
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        this.root.append(panelHeader({ key: 'tab.files' }, this.refreshButton, this.closeButton), this.state, this.rootNote, this.body, this.status);
      }

      show() { this.root.hidden = false; this.refresh(); }
      hide() { this.root.hidden = true; }

      async refresh() {
        const seq = ++this.seq;
        try {
          const v = filesView(await api('/api/files'));
          if (seq !== this.seq) return;
          this.info = v.info;
          this.keys = v.keys;
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

      startGuide() {
        guide({ sandbox: this.sandbox.name, onChange: () => this.refresh() }).catch((error) => { this.status.textContent = error.message; });
      }

      render() {
        const running = this.sandbox.status === 'running';
        this.state.dataset.state = running ? 'running' : 'stopped';
        this.state.textContent = !this.info.available ? t('files.state.unavailable')
          : running ? t('files.state.running') : t('files.state.stopped');
        if (!this.info.available) { this.body.replaceChildren(); return; }
        const mine = this.keys.filter((k) => !k.sandbox || k.sandbox === this.sandbox.name);
        const nodes = [];
        if (!mine.length) {
          const offer = element('div', 'files-offer');
          const words = element('div', 'login-body');
          words.append(element('strong', '', t('files.offer.title')), element('p', 'panel-meta', t('files.offer.text')));
          offer.append(words, button('button-primary', t('files.offer.start'), () => this.startGuide()));
          nodes.push(offer);
        } else {
          nodes.push(element('h4', 'files-head', t('files.devices.title')));
          const list = element('div', 'volume-list files-keys');
          list.append(...mine.map((k) => keyRow(k)));
          const row = element('div', 'login-form');
          row.append(button('button-subtle', t('files.devices.add'), () => this.startGuide()));
          nodes.push(list, row);
        }
        nodes.push(element('p', 'panel-meta', t('files.devices.note')));
        this.body.replaceChildren(...nodes);
      }

      dispose() { this.root.remove(); }
    }

    // The settings' list of every credential, with revoke and the guide.
    class KeysSection {
      constructor() {
        this.root = element('div', 'files-settings');
        this.list = element('div', 'volume-list files-keys');
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        const row = element('div', 'login-form');
        row.append(button('button-subtle', t('files.keys.add'), () => guide({ onChange: () => this.refresh() }).catch((error) => { this.status.textContent = error.message; })));
        this.root.append(element('h4', 'files-head', t('files.keys.title')), element('p', 'panel-meta', t('files.keys.note')), this.list, row, this.status);
      }

      async refresh() {
        try {
          const { keys } = filesView(await api('/api/files'));
          this.list.replaceChildren(...(keys.length ? keys.map((k) => keyRow(k, (x) => this.revoke(x))) : [element('p', 'panel-meta', t('files.keys.none'))]));
          this.status.textContent = '';
        } catch (error) {
          this.status.textContent = error.message;
        }
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
    }

    return { FilesPanel, KeysSection, guide };
  }

  window.IwatoFiles = { davURL, uncPath, ago, make };
})();
