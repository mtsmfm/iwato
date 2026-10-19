// Port exposure (ingress.go): the settings (this PC only, the default, or
// Tailscale) and a sandbox's Ports tab (the ports it listens on, as links;
// with Tailscale its node).
// Secrets are typed into a password input, sent once and cleared; the
// page only ever shows the server's hint (prefix and last 4 characters).
(() => {
  'use strict';

  // The UI's strings (web/i18n.js), looked up when used: this file loads first.
  const t = (key, params) => window.IwatoI18n.t(key, params);
  const text = (v, max) => typeof v === 'string' ? v.slice(0, max) : '';

  // tagOwnersSnippet is the tailnet policy line the owner adds once.
  function tagOwnersSnippet(tag) {
    return '"tagOwners": {\n  "' + tag + '": ["autogroup:admin"]\n}';
  }
  // grantsSnippet narrows who reaches the nodes (only when the default
  // "everyone → everything" grant is removed).
  function grantsSnippet(tag) {
    return '"grants": [\n  {"src": ["autogroup:member"], "dst": ["' + tag + '"], "ip": ["*"]}\n]';
  }

  // parseOwners reads the owners textarea: one login per line (commas and
  // spaces separate too), without repeats.
  function parseOwners(value) {
    const seen = new Set();
    const out = [];
    for (const o of String(value || '').split(/[\s,]+/)) {
      if (!o || seen.has(o.toLowerCase())) continue;
      seen.add(o.toLowerCase());
      out.push(o);
    }
    return out;
  }

  const stateKeys = { connected: 'ingress.state.connected', starting: 'ingress.state.starting', error: 'ingress.state.error', off: 'ingress.state.off', on: 'ingress.state.on', 'needs-secret': 'ingress.state.needsSecret' };
  const stateLabel = (state) => stateKeys[state] ? t(stateKeys[state]) : state;
  // A status's message: the page's words for a known code, else the server's.
  const statusMessage = (s) => s.code && window.IwatoI18n.has('ingress.status.' + s.code) ? t('ingress.status.' + s.code, s.params) : s.message;
  const providers = ['local', 'tailscale', 'off'];
  const provider = (v) => providers.includes(v) ? v : 'local';
  // Link URLs the page accepts: a tailnet node's https, this PC's *.localhost.
  const linkURL = (u) => /^https:\/\/[a-z0-9.-]+(:\d+)?\/$/.test(u || '') || /^http:\/\/\d{1,5}\.[a-z][a-z0-9-]{0,31}\.localhost:\d{1,5}\/$/.test(u || '') ? u : '';

  function statusView(s) {
    const params = {};
    for (const [k, v] of Object.entries(s?.params && typeof s.params === 'object' ? s.params : {}).slice(0, 4)) if (typeof v === 'string') params[k] = v.slice(0, 300);
    return { state: text(s?.state, 32) || 'off', dnsName: text(s?.dnsName, 253), message: text(s?.message, 500), code: text(s?.code, 32), params };
  }

  // settingsView keeps what the page shows of GET /api/ingress.
  function settingsView(d) {
    const list = (v, n) => (Array.isArray(v) ? v : []).filter((x) => typeof x === 'string').slice(0, n).map((x) => x.slice(0, 200));
    const secret = d?.secret || {};
    return {
      provider: provider(d?.provider), listen: text(d?.listen, 64), tag: text(d?.tag, 64), hostnamePrefix: text(d?.hostnamePrefix, 31), owners: list(d?.owners, 20),
      defaults: { tag: text(d?.defaults?.tag, 64) || 'tag:iwato', hostnamePrefix: text(d?.defaults?.hostnamePrefix, 31) },
      suggested: list(d?.suggestedOwners, 5), secretEnv: text(d?.secretEnv, 64) || 'IWATO_TS_AUTHKEY',
      secret: { set: secret.set === true, source: text(secret.source, 16), kind: text(secret.kind, 16), hint: text(secret.hint, 64), storage: text(secret.storage, 16), error: text(secret.error, 300) },
      status: statusView(d?.status),
      nodes: (Array.isArray(d?.nodes) ? d.nodes : []).slice(0, 32).map((n) => ({ sandbox: text(n?.sandbox, 64), hostname: text(n?.hostname, 64), ...statusView(n) }))
    };
  }

  // portsView keeps what the page shows of GET /api/sandboxes/NAME/ports.
  function portsView(d) {
    return {
      overall: statusView(d?.overall), provider: provider(d?.provider), node: statusView(d?.node), hostname: text(d?.hostname, 64), running: d?.running === true,
      error: d?.error && window.IwatoI18n ? window.IwatoI18n.serverError(d.errorCode, d.errorParams, text(d.error, 300)) : text(d?.error, 300),
      ports: (Array.isArray(d?.ports) ? d.ports : []).filter((p) => Number.isInteger(p?.port) && p.port > 0 && p.port < 65536).slice(0, 200).map((p) => ({
        port: p.port, url: linkURL(p.url), addresses: (Array.isArray(p.addresses) ? p.addresses : []).filter((a) => typeof a === 'string').slice(0, 4).map((a) => a.slice(0, 40)),
        ipv6Only: p.ipv6Only === true, disabled: p.disabled === true, tcpOnly: p.tcpOnly === true
      }))
    };
  }

  // tools are the page's helpers (app.js).
  function make({ element, button, panelHeader, api, askConfirm }) {
    async function copy(value, label) {
      try {
        if (typeof navigator === 'undefined' || !navigator.clipboard) throw new Error('no clipboard');
        await navigator.clipboard.writeText(value);
        label.textContent = t('ingress.copied');
      } catch (_) {
        label.textContent = t('ingress.copyFailed');
      }
    }
    function copyButton(value, what) {
      const status = element('span', 'files-copied');
      status.setAttribute('role', 'status');
      const b = button('button-subtle', t('common.copy'), () => copy(value, status));
      b.setAttribute('aria-label', t('ingress.copyWhat', { what }));
      return [b, status];
    }
    function snippet(title, value) {
      const box = element('div', 'ingress-snippet');
      const head = element('div', 'ingress-snippet-head');
      head.append(element('span', '', title), ...copyButton(value, title));
      const pre = element('pre', 'ingress-code', value);
      pre.tabIndex = 0;
      box.append(head, pre);
      return box;
    }
    function dot(state) {
      const d = element('span', 'ingress-dot');
      d.dataset.state = state;
      d.setAttribute('aria-hidden', 'true');
      return d;
    }
    function field(label, input, hint) {
      const l = element('label', 'ingress-field');
      l.append(element('span', 'ingress-label', label), input);
      if (hint) l.append(element('span', 'panel-meta', hint));
      return l;
    }
    function textInput(placeholder, label) {
      const i = element('input', 'text-input');
      i.type = 'text';
      i.autocomplete = 'off';
      i.spellcheck = false;
      i.setAttribute('autocapitalize', 'off');
      i.placeholder = placeholder;
      i.setAttribute('aria-label', label);
      return i;
    }

    // The settings: rendered into root, a panel of the page.
    class SettingsPanel {
      constructor(root) {
        this.root = root;
        this.seq = 0;
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        this.body = element('div', 'ingress-settings');
        root.append(this.body, this.status);
      }

      async refresh() {
        const seq = ++this.seq;
        try {
          const data = settingsView(await api('/api/ingress'));
          if (seq !== this.seq) return;
          this.data = data;
          this.render();
        } catch (error) {
          if (seq === this.seq) this.status.textContent = t('ingress.loadFailed', { message: error.message });
          if (error.status === 401 || error.status === 403) this.onAuth?.();
        }
      }

      render() {
        const d = this.data;
        const nodes = [];
        nodes.push(element('h4', 'files-head', t('ingress.title')));
        const head = element('p', 'ingress-state');
        head.dataset.state = d.status.state;
        head.append(dot(d.status.state), element('strong', '', stateLabel(d.status.state)), element('span', '', ' ' + statusMessage(d.status)));
        nodes.push(head);

        const form = element('form', 'stack-form ingress-form');
        const choices = element('fieldset', 'ingress-providers');
        choices.append(element('legend', 'ingress-label', t('ingress.how')));
        this.choice = {};
        const choice = (value, label, hint) => {
          const l = element('label', 'scope-choice ingress-provider');
          const r = element('input');
          r.type = 'radio';
          r.name = 'ingress-provider';
          r.value = value;
          // A choice not saved yet survives the secret's save (a refresh).
          r.checked = (this.pending || d.provider) === value;
          r.addEventListener('change', () => { this.pending = this.chosen(); this.showTailscale(); });
          this.choice[value] = r;
          const words = element('span', 'ingress-provider-text');
          words.append(element('span', '', label), element('span', 'panel-meta', hint));
          l.append(r, words);
          choices.append(l);
        };
        const port = (d.listen.match(/:(\d+)$/) || [])[1] || 'PORT';
        choice('local', t('ingress.local'), t('ingress.local.hint', { port }));
        choice('tailscale', t('ingress.tailscale'), t('ingress.tailscale.hint', { prefix: d.hostnamePrefix || '' }));
        choice('off', t('ingress.off'), t('ingress.off.hint'));
        this.owners = element('textarea', 'text-input');
        this.owners.rows = 2;
        this.owners.spellcheck = false;
        this.owners.setAttribute('autocapitalize', 'off');
        this.owners.placeholder = t('ingress.owners.placeholder');
        this.owners.setAttribute('aria-label', t('ingress.owners.label'));
        // Prefilled with who opened the page (tailscale serve) or this PC's user.
        this.owners.value = (d.owners.length ? d.owners : d.suggested).join('\n');
        const ownersField = field(t('ingress.owners'), this.owners, t('ingress.owners.hint'));
        if (d.suggested.length) {
          const chips = element('div', 'ingress-chips');
          chips.append(element('span', 'panel-meta', t('ingress.suggested')));
          for (const s of d.suggested) chips.append(button('button-subtle', s, () => { const list = parseOwners(this.owners.value); if (!list.includes(s)) this.owners.value = list.concat([s]).join('\n'); }));
          ownersField.append(chips);
        }
        this.tag = textInput(d.defaults.tag, 'tag');
        this.tag.value = d.tag;
        this.prefix = textInput(d.defaults.hostnamePrefix, t('ingress.prefix'));
        this.prefix.value = d.hostnamePrefix;
        const row = element('div', 'ingress-row');
        row.append(field('tag', this.tag, t('ingress.tag.hint')), field(t('ingress.prefix'), this.prefix, t('ingress.prefix.hint')));
        this.tsFields = element('div', 'stack-form ingress-tailscale');
        this.tsFields.append(ownersField, row);
        this.save = element('button', 'button button-primary', t('common.save'));
        this.save.type = 'submit';
        const actions = element('div', 'login-form');
        actions.append(this.save);
        form.append(choices, this.tsFields, actions);
        form.addEventListener('submit', (event) => { event.preventDefault(); this.saveSettings(); });
        nodes.push(form);

        // Tailscale's secret, nodes and steps: only while it is chosen.
        this.tsExtra = element('div', 'stack-form ingress-tailscale');
        this.tsExtra.append(this.secretBlock());
        if (d.nodes.length) this.tsExtra.append(this.nodeList());
        this.tsExtra.append(this.setupSteps());
        this.tsExtra.append(element('p', 'panel-meta', t('ingress.ctNote')));
        nodes.push(this.tsExtra);
        this.body.replaceChildren(...nodes);
        this.showTailscale();
      }

      chosen() {
        return providers.find((p) => this.choice[p]?.checked) || this.pending || this.data.provider;
      }

      showTailscale() {
        const on = this.chosen() === 'tailscale';
        this.tsFields.hidden = !on;
        this.tsExtra.hidden = !on;
      }

      secretBlock() {
        const d = this.data;
        const box = element('div', 'stack-form ingress-secret');
        box.append(element('strong', '', t('ingress.secret')));
        let state;
        if (d.secret.error) state = d.secret.error;
        else if (d.secret.source === 'env') state = t('ingress.secret.env', { env: d.secretEnv, hint: d.secret.hint });
        else if (d.secret.set) state = t('ingress.secret.set', { hint: d.secret.hint }) + (d.secret.kind === 'authkey' ? ' auth key' : '');
        else state = t('ingress.secret.unset');
        const p = element('p', 'ingress-secret-state', state);
        p.dataset.set = String(d.secret.set);
        box.append(p);
        if (d.secret.source !== 'env') {
          this.secretInput = element('input', 'text-input');
          this.secretInput.type = 'password';
          this.secretInput.autocomplete = 'off';
          this.secretInput.placeholder = d.secret.set ? t('ingress.secret.replaceHint') : 'tskey-client-…';
          this.secretInput.setAttribute('aria-label', t('ingress.secret'));
          const actions = element('div', 'login-form');
          actions.append(button('button-primary', d.secret.set ? t('ingress.secret.replace') : t('common.save'), () => this.saveSecret()));
          if (d.secret.set) actions.append(button('button-disconnect', t('common.delete'), () => this.deleteSecret()));
          box.append(this.secretInput, actions);
        }
        return box;
      }

      nodeList() {
        const box = element('div', 'volume-list ingress-nodes');
        box.append(element('strong', '', t('ingress.nodes')));
        for (const n of this.data.nodes) {
          const row = element('div', 'volume-row');
          const info = element('div', 'volume-info');
          const name = element('span', 'volume-path');
          name.append(dot(n.state), element('span', '', n.sandbox));
          const message = statusMessage(n);
          info.append(name, element('span', 'panel-meta', stateLabel(n.state) + ' · ' + (n.dnsName || n.hostname) + (message ? ' · ' + message : '')));
          row.append(info);
          box.append(row);
        }
        return box;
      }

      setupSteps() {
        const d = this.data;
        const tag = d.tag || d.defaults.tag;
        const steps = element('details', 'files-steps ingress-steps');
        steps.open = !d.secret.set;
        steps.append(element('summary', '', t('ingress.steps')));
        const ol = element('ol');
        const li = (...parts) => { const l = element('li'); l.append(...parts.map((x) => typeof x === 'string' ? element('span', '', x) : x)); ol.append(l); return l; };
        li(t('ingress.step.dns'));
        li(element('span', '', t('ingress.step.tag')), snippet('tagOwners', tagOwnersSnippet(tag)));
        li(t('ingress.step.oauth', { tag }));
        li(t('ingress.step.secret'));
        steps.append(ol);
        steps.append(element('p', 'panel-meta', t('ingress.step.authkey', { tag })));
        steps.append(element('p', 'panel-meta', t('ingress.step.grants', { tag })), snippet('grants', grantsSnippet(tag)));
        return steps;
      }

      async saveSettings() {
        const body = { provider: this.chosen(), tag: this.tag.value.trim() || this.data.defaults.tag, hostnamePrefix: this.prefix.value.trim(), owners: parseOwners(this.owners.value) };
        this.save.disabled = true;
        try {
          await api('/api/ingress', { method: 'POST', body: JSON.stringify(body) });
          this.pending = null;
          this.status.textContent = t('ingress.saved');
          await this.refresh();
          this.onChange?.();
        } catch (error) {
          this.status.textContent = t('ingress.saveFailed', { message: error.message });
        } finally {
          this.save.disabled = false;
        }
      }

      async saveSecret() {
        const value = this.secretInput.value.trim();
        this.secretInput.value = '';
        if (!value) { this.status.textContent = t('ingress.secret.empty'); return; }
        try {
          await api('/api/ingress/secret', { method: 'POST', body: JSON.stringify({ secret: value }) });
          this.status.textContent = t('ingress.secret.saved');
          await this.refresh();
          this.onChange?.();
        } catch (error) {
          this.status.textContent = t('ingress.saveFailed', { message: error.message });
        }
      }

      async deleteSecret() {
        if (!await askConfirm(t('ingress.secret.delete.confirm'), { ok: t('common.delete'), danger: true })) return;
        try {
          await api('/api/ingress/secret/delete', { method: 'POST', body: '{}' });
          this.status.textContent = t('ingress.secret.deleted');
          await this.refresh();
          this.onChange?.();
        } catch (error) {
          this.status.textContent = t('ingress.deleteFailed', { message: error.message });
        }
      }
    }

    // A sandbox's Ports tab.
    class PortsPanel {
      constructor(sandbox, openSettings) {
        this.sandbox = sandbox;
        this.openSettings = openSettings;
        this.seq = 0;
        this.root = element('section', 'side-panel ports-panel');
        window.IwatoI18n.bindAttribute(this.root, 'aria-label', 'tab.ports');
        this.root.hidden = true;
        this.refreshButton = window.IwatoI18n.bind(button('button-subtle', '', () => this.refresh()), 'common.reload');
        this.closeButton = window.IwatoI18n.bind(button('button-subtle', '', () => this.onClose?.()), 'common.close');
        this.state = element('div', 'ingress-node');
        this.state.setAttribute('role', 'status');
        this.list = element('div', 'volume-list ports-list');
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        this.root.append(panelHeader({ key: 'tab.ports' }, this.refreshButton, this.closeButton), this.state, this.list, this.status);
      }

      show() {
        this.root.hidden = false;
        this.refresh();
        clearInterval(this.timer);
        // Display only: the ports are read again every 5 s while shown.
        this.timer = setInterval(() => { if (!document.hidden && !this.root.hidden) this.refresh(); }, 5000);
      }

      hide() {
        this.root.hidden = true;
        clearInterval(this.timer);
        this.timer = null;
      }

      async refresh() {
        const seq = ++this.seq;
        try {
          const data = portsView(await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/ports', { timeoutMs: 15000 }));
          if (seq !== this.seq) return;
          this.data = data;
          this.status.textContent = data.error;
          this.render();
        } catch (error) {
          if (seq === this.seq) this.status.textContent = error.message;
        }
      }

      render() {
        const d = this.data;
        const nodes = [];
        const line = element('p', 'ingress-state');
        const local = d.provider === 'local';
        if (d.overall.state === 'off' || d.overall.state === 'needs-secret') {
          line.dataset.state = d.overall.state;
          line.append(dot('off'), element('span', '', d.overall.state === 'off' ? t('ports.off') : statusMessage(d.overall)));
          nodes.push(line, button('button-primary', t('ports.openSettings'), () => this.openSettings?.()));
        } else if (local) {
          line.dataset.state = d.overall.state;
          line.append(dot(d.overall.state === 'on' ? 'connected' : d.overall.state), element('strong', '', t('ports.local')), element('span', 'ingress-dns', ' *.' + this.sandbox.name + '.localhost'));
          const hint = element('p', 'panel-meta', t('ports.local.hint.before'));
          const open = element('a', 'ingress-settings-link', t('settings.title'));
          open.href = '#';
          open.addEventListener('click', (event) => { event.preventDefault(); this.openSettings?.(); });
          hint.append(open, element('span', '', t('ports.local.hint.after')));
          nodes.push(line, hint);
          if (d.node.state === 'error') nodes.push(element('p', 'panel-meta files-note', statusMessage(d.node)));
        } else {
          line.dataset.state = d.node.state;
          line.append(dot(d.node.state), element('strong', '', 'Tailscale ' + stateLabel(d.node.state)), element('span', 'ingress-dns', ' ' + (d.node.dnsName || d.hostname)));
          nodes.push(line);
          if (d.node.message) nodes.push(element('p', 'panel-meta', statusMessage(d.node)));
          if (d.overall.state === 'error') nodes.push(element('p', 'panel-meta files-note', statusMessage(d.overall)));
        }
        this.state.replaceChildren(...nodes);
        const rows = [];
        if (!d.running) {
          rows.push(element('p', 'panel-meta', local ? t('ports.stopped.local') : t('ports.stopped.tailscale')));
        } else {
          for (const p of d.ports) rows.push(this.portRow(p));
          if (!d.ports.length) rows.push(element('p', 'panel-meta', t('ports.none')));
        }
        if (local) rows.push(element('p', 'panel-meta', t('ports.note.local')));
        this.list.replaceChildren(...rows);
      }

      portRow(p) {
        const row = element('div', 'volume-row port-row');
        const info = element('div', 'volume-info');
        const head = element('span', 'volume-path');
        head.append(element('span', 'port-number', ':' + p.port), element('span', 'panel-meta port-addr', p.addresses.join(' ')));
        info.append(head);
        if (p.disabled) info.append(element('span', 'panel-meta', t('ports.disabled')));
        else if (p.tcpOnly) info.append(element('span', 'panel-meta', t('ports.tcpOnly')));
        else if (p.url) {
          const a = element('a', 'port-link', p.url);
          a.href = p.url;
          a.target = '_blank';
          a.rel = 'noopener noreferrer';
          info.append(a);
        } else info.append(element('span', 'panel-meta', this.data.provider === 'local' ? t('ports.noURL') : t('ports.noURLYet')));
        if (p.ipv6Only) info.append(element('span', 'panel-meta', t('ports.ipv6Only')));
        row.append(info);
        if (p.url && !p.disabled) row.append(...copyButton(p.url, t('ports.url', { port: p.port })));
        return row;
      }

      dispose() { this.hide(); this.root.remove(); }
    }

    return { SettingsPanel, PortsPanel };
  }

  window.IwatoIngress = { tagOwnersSnippet, grantsSnippet, parseOwners, settingsView, portsView, make };
})();
