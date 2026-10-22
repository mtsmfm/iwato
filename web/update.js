// Updating Iwato itself (update.go): the settings' section, the sidebar's
// button, and the dialog that confirms, follows the download and the
// restart, and reloads the page once the new server answers.
(() => {
  'use strict';

  const t = (key, params) => window.IwatoI18n.t(key, params);
  const text = (v, max) => typeof v === 'string' ? v.slice(0, max) : '';
  const build = (b) => b && typeof b === 'object' && (/^[0-9A-Za-z.+-]{1,32}$/.test(b.version) || /^[0-9a-f]{7,40}$/.test(b.commit)) ? {
    version: /^[0-9A-Za-z.+-]{1,32}$/.test(b.version) ? b.version : '',
    commit: /^[0-9a-f]{7,40}(-dirty)?$/.test(b.commit) ? b.commit : '',
    built: text(b.built, 40)
  } : null;
  // label: "v0.1.0 · abc1234".
  const label = (b) => b ? [b.version && 'v' + b.version, b.commit.slice(0, 7)].filter(Boolean).join(' · ') : '';
  const phases = new Set(['downloading', 'restarting', 'failed']);
  const params = (p) => {
    const out = {};
    for (const [k, v] of Object.entries(p && typeof p === 'object' ? p : {}).slice(0, 4)) if (typeof v === 'string') out[k] = v.slice(0, 500);
    return out;
  };
  function view(d) {
    return {
      enabled: d?.enabled === true,
      current: build(d?.current) || { version: '', commit: '', built: '' },
      latest: build(d?.latest),
      supported: d?.supported === true,
      gateway: d?.gateway === true,
      restart: (Array.isArray(d?.restart) ? d.restart : []).filter((n) => typeof n === 'string' && /^[a-z0-9-]{1,63}$/.test(n)).slice(0, 100),
      checked: text(d?.checked, 40),
      checkFailed: text(d?.checkFailed, 500),
      phase: phases.has(d?.phase) ? d.phase : '',
      code: text(d?.code, 64), params: params(d?.params), message: text(d?.message, 1000)
    };
  }
  const failure = (v) => window.IwatoI18n.serverError(v.code, v.params, v.message);
  const same = (a, b) => a.version === b.version && a.commit === b.commit;

  function make({ element, button, api, onAuth, onCheck }) {
    const bind = (node, key, params) => window.IwatoI18n.bind(node, key, params);
    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
    const authFailed = (error) => { if (error.status === 401 || error.status === 403) { onAuth(); return true; } return false; };

    class UpdateDialog {
      constructor() {
        this.root = element('section', 'banner banner-pair confirm-panel update-dialog');
        this.root.setAttribute('role', 'dialog');
        this.root.setAttribute('aria-modal', 'true');
        window.IwatoI18n.bindAttribute(this.root, 'aria-label', 'update.dialog.title');
        this.root.hidden = true;
        this.body = element('div', 'login-body update-body');
        this.root.append(this.body);
      }

      async open() {
        if (this.busy) return;
        try {
          this.data = view(await api('/api/update'));
        } catch (error) {
          authFailed(error);
          return;
        }
        if (!this.data.latest || !this.data.supported) return;
        const host = document.body || this.root;
        if (!host.contains(this.root)) host.append(this.root);
        this.render();
        this.root.hidden = false;
        this.cancel.focus();
      }

      close() {
        if (this.busy) return;
        this.root.hidden = true;
      }

      render() {
        const d = this.data;
        const restarts = d.gateway && d.restart.length > 0;
        const nodes = [element('strong', '', t('update.dialog.title')),
          element('p', 'update-versions', t('update.dialog.versions', { from: label(d.current), to: label(d.latest) }))];
        if (restarts) {
          nodes.push(element('p', 'update-warning', t('update.dialog.gateway')));
          const ul = element('ul', 'reset-list update-restart');
          for (const name of d.restart) ul.append(element('li', '', name));
          nodes.push(ul);
        } else nodes.push(element('p', 'panel-meta', t('update.dialog.keep')));
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        this.ok = button(restarts ? 'button-disconnect' : 'button-primary', t('update.apply'), () => this.run());
        this.cancel = button('button-subtle', t('common.cancel'), () => this.close());
        const row = element('div', 'login-form');
        row.append(this.ok, this.cancel);
        nodes.push(row, this.status);
        this.body.replaceChildren(...nodes);
      }

      async run() {
        if (this.busy) return;
        this.busy = true;
        this.ok.disabled = this.cancel.disabled = true;
        const before = this.data.current;
        this.status.textContent = t('update.phase.downloading');
        try {
          await api('/api/update/apply', { method: 'POST', body: JSON.stringify({ commit: this.data.latest.commit }) });
        } catch (error) {
          this.fail(error.message);
          authFailed(error);
          return;
        }
        // The server goes away while it restarts; the same login comes back with it.
        const started = Date.now();
        while (Date.now() - started < 5 * 60 * 1000) {
          await sleep(1500);
          let v;
          try {
            v = view(await api('/api/update', { timeoutMs: 5000 }));
          } catch (error) {
            if (authFailed(error)) { this.busy = false; this.root.hidden = true; return; }
            this.status.textContent = t('update.waiting', { seconds: Math.round((Date.now() - started) / 1000) });
            continue;
          }
          if (!same(v.current, before)) {
            this.status.textContent = t('update.done');
            location.reload();
            return;
          }
          if (v.phase === 'failed') return this.fail(failure(v));
          this.status.textContent = t(v.phase === 'restarting' ? 'update.phase.restarting' : 'update.phase.downloading');
        }
        this.fail(t('update.timeout'));
      }

      fail(message) {
        this.busy = false;
        this.ok.disabled = this.cancel.disabled = false;
        this.status.textContent = t('update.failed', { message });
      }
    }

    // In the settings: this build, the new one, and checking now.
    class UpdateSection {
      constructor(dialog) {
        this.dialog = dialog;
        this.root = element('div', 'update-settings');
        this.root.hidden = true;
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
      }

      async refresh() {
        try {
          this.data = view(await api('/api/update'));
          this.render();
        } catch (error) {
          authFailed(error);
        }
      }

      render() {
        const d = this.data;
        this.root.hidden = !d.enabled;
        if (!d.enabled) return;
        const nodes = [bind(element('h4', 'files-head'), 'update.title'), element('p', 'panel-meta', t('update.current', { build: label(d.current) }))];
        const row = element('div', 'login-form');
        if (d.latest) {
          nodes.push(element('p', 'panel-meta update-latest', t('update.latest', { build: label(d.latest) })));
          if (d.supported) row.append(button('button-primary', t('update.apply'), () => this.dialog.open()));
          else nodes.push(element('p', 'panel-meta', t('update.unsupported')));
        } else nodes.push(element('p', 'panel-meta', t('update.upToDate')));
        const when = Number.isFinite(Date.parse(d.checked)) ? window.IwatoI18n.date(d.checked, { dateStyle: 'short', timeStyle: 'short' }) : '';
        nodes.push(element('p', 'panel-meta', d.checkFailed ? t('update.checkFailed', { message: d.checkFailed }) : when ? t('update.checked', { time: when }) : ''));
        this.checkButton = button('button-subtle', t('update.check'), () => this.check());
        this.checkButton.disabled = !!this.busy;
        row.append(this.checkButton);
        nodes.push(row, this.status);
        this.root.replaceChildren(...nodes);
      }

      async check() {
        if (this.busy) return;
        this.busy = true;
        this.checkButton.disabled = true;
        this.status.textContent = t('update.checking');
        try {
          this.data = view(await api('/api/update/check', { method: 'POST', body: '{}', timeoutMs: 45000 }));
          this.status.textContent = '';
          onCheck?.();
        } catch (error) {
          this.status.textContent = t('update.checkFailed', { message: error.message });
          authFailed(error);
        } finally {
          this.busy = false;
          if (this.data) this.render();
        }
      }
    }

    return { UpdateDialog, UpdateSection };
  }

  window.IwatoUpdate = { view, build, label, make };
})();
