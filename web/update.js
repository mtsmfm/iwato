// Updating Iwato itself (update.go): the settings' section, the sidebar's
// button, the pull requests' builds to try and the way back to edge, and
// the dialog that confirms, follows the download and the restart, and
// reloads the page once the new server answers.
(() => {
  'use strict';

  const t = (key, params) => window.IwatoI18n.t(key, params);
  const text = (v, max) => typeof v === 'string' ? v.slice(0, max) : '';
  const build = (b) => b && typeof b === 'object' && (/^[0-9A-Za-z.+-]{1,32}$/.test(b.version) || /^[0-9a-f]{7,40}$/.test(b.commit)) ? {
    version: /^[0-9A-Za-z.+-]{1,32}$/.test(b.version) ? b.version : '',
    commit: /^[0-9a-f]{7,40}(-dirty)?$/.test(b.commit) ? b.commit : '',
    built: text(b.built, 40),
    pr: Number.isInteger(b.pr) && b.pr > 0 ? b.pr : 0,
    title: text(b.title, 300),
    gateway: b.gateway === true
  } : null;
  // label: "v0.1.0 · abc1234", "PR #12 · v0.1.0 · abc1234".
  const label = (b) => b ? [b.pr && 'PR #' + b.pr, b.version && 'v' + b.version, b.commit.slice(0, 7)].filter(Boolean).join(' · ') : '';
  const names = (list) => (Array.isArray(list) ? list : []).filter((n) => typeof n === 'string' && /^[a-z0-9-]{1,63}$/.test(n)).slice(0, 100);
  const phases = new Set(['downloading', 'restarting', 'failed']);
  const params = (p) => {
    const out = {};
    for (const [k, v] of Object.entries(p && typeof p === 'object' ? p : {}).slice(0, 4)) if (typeof v === 'string') out[k] = v.slice(0, 500);
    return out;
  };
  function view(d) {
    return {
      enabled: d?.enabled === true,
      channel: ['edge', 'release', 'pr'].includes(d?.channel) ? d.channel : '',
      current: build(d?.current) || { version: '', commit: '', built: '', pr: 0, title: '' },
      latest: build(d?.latest),
      edge: build(d?.edge),
      switch: d?.switch === true,
      gone: d?.gone === true,
      supported: d?.supported === true,
      gateway: d?.gateway === true,
      restart: names(d?.restart),
      recreate: d?.recreate === true,
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

      // target: another release's build to switch to ({tag, build,
      // restart}: edge, or pr-N from the list); none: this channel's update.
      async open(target) {
        if (this.busy) return;
        try {
          this.data = view(await api('/api/update'));
        } catch (error) {
          authFailed(error);
          return;
        }
        this.target = target || null;
        if (!(target ? target.build : this.data.latest) || !this.data.supported) return;
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
        const d = this.data, target = this.target;
        const to = target ? target.build : d.latest;
        const restart = target ? names(target.restart) : d.restart;
        const gateway = target ? to.gateway : d.gateway;
        const restarts = gateway && restart.length > 0;
        const pr = target && target.tag !== 'edge';
        const title = pr ? t('update.pr.dialogTitle', { pr: to.pr }) : target ? t('update.edge.dialogTitle') : t('update.dialog.title');
        const nodes = [element('strong', '', title),
          element('p', 'update-versions', t('update.dialog.versions', { from: label(d.current), to: label(to) }))];
        if (pr) {
          // The owner should know exactly which commit runs.
          if (to.title) nodes.push(element('p', 'update-pr-title', to.title));
          nodes.push(element('p', 'panel-meta update-commit', t('update.pr.head', { commit: to.commit })));
          for (const key of ['update.pr.unmerged', 'update.pr.remote']) nodes.push(element('p', 'update-warning', t(key)));
        }
        if (restarts) {
          nodes.push(element('p', 'update-warning', t('update.pr.restarts') + ' ' + t('update.dialog.gateway')));
          const ul = element('ul', 'reset-list update-restart');
          for (const name of restart) ul.append(element('li', '', name));
          nodes.push(ul);
        } else nodes.push(element('p', 'panel-meta', t('update.dialog.keep')));
        if (gateway && (target ? target.recreate === true : d.recreate)) nodes.push(element('p', 'update-warning update-recreate', t('update.dialog.recreate')));
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        const ok = pr ? t('update.pr.install') : target ? t('update.edge.back') : t('update.apply');
        this.ok = button(restarts || pr ? 'button-disconnect' : 'button-primary', ok, () => this.run());
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
          const target = this.target;
          if (target) await api('/api/update/switch', { method: 'POST', body: JSON.stringify({ tag: target.tag, commit: target.build.commit }) });
          else await api('/api/update/apply', { method: 'POST', body: JSON.stringify({ commit: this.data.latest.commit }) });
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
        const pr = d.channel === 'pr' ? d.current.pr : 0;
        const nodes = [bind(element('h4', 'files-head'), 'update.title'), element('p', 'panel-meta', t('update.current', { build: label(d.current) }))];
        if (pr) {
          nodes.push(element('p', 'update-warning update-trying', t('update.pr.trying', { pr })));
          if (d.current.title) nodes.push(element('p', 'panel-meta update-pr-title', d.current.title));
          if (d.gone) nodes.push(element('p', 'panel-meta', t('update.pr.gone', { pr })));
        }
        const row = element('div', 'login-form');
        if (d.latest) {
          nodes.push(element('p', 'panel-meta update-latest', pr ? t('update.pr.newer', { pr, build: label(d.latest) }) : t('update.latest', { build: label(d.latest) })));
          if (d.supported) row.append(button('button-primary', t('update.apply'), () => this.dialog.open()));
          else nodes.push(element('p', 'panel-meta', t('update.unsupported')));
        } else if (!d.gone) nodes.push(element('p', 'panel-meta', t('update.upToDate')));
        if (pr && d.edge && d.supported) row.append(button('button-subtle', t('update.edge.back'), () => this.dialog.open({ tag: 'edge', build: d.edge, restart: d.restart, recreate: d.recreate })));
        const when = Number.isFinite(Date.parse(d.checked)) ? window.IwatoI18n.date(d.checked, { dateStyle: 'short', timeStyle: 'short' }) : '';
        nodes.push(element('p', 'panel-meta', d.checkFailed ? t('update.checkFailed', { message: d.checkFailed }) : when ? t('update.checked', { time: when }) : ''));
        this.checkButton = button('button-subtle', t('update.check'), () => this.check());
        this.checkButton.disabled = !!this.busy;
        row.append(this.checkButton);
        if (d.switch && d.supported) {
          this.prsButton = button('button-subtle', t('update.pr.try'), () => this.togglePRs());
          this.prsButton.setAttribute('aria-expanded', String(!!this.prs));
          row.append(this.prsButton);
        }
        nodes.push(row, this.status);
        if (this.prs && d.switch) nodes.push(this.prs);
        this.root.replaceChildren(...nodes);
      }

      // The open pull requests' builds (GET /api/update/prs), each with
      // its head commit, to install one.
      async togglePRs() {
        if (this.prs) {
          this.prs = null;
          this.render();
          return;
        }
        this.prs = element('div', 'update-prs');
        this.prs.append(element('p', 'panel-meta', t('update.pr.loading')));
        this.render();
        const box = this.prs;
        let list;
        try {
          list = await api('/api/update/prs', { timeoutMs: 45000 });
        } catch (error) {
          box.replaceChildren(element('p', 'panel-meta', t('update.pr.listFailed', { message: error.message })));
          authFailed(error);
          return;
        }
        const builds = (Array.isArray(list?.builds) ? list.builds : []).map(build).filter((b) => b && b.pr && /^[0-9a-f]{40}$/.test(b.commit)).slice(0, 20);
        const restart = names(list?.restart), recreate = list?.recreate === true;
        if (!builds.length) {
          box.replaceChildren(element('p', 'panel-meta', t('update.pr.none')));
          return;
        }
        const ul = element('ul', 'update-pr-list');
        for (const b of builds) {
          const li = element('li', 'update-pr');
          const text = element('div', 'update-pr-text');
          text.append(element('strong', '', '#' + b.pr + ' ' + b.title));
          const when = Number.isFinite(Date.parse(b.built)) ? window.IwatoI18n.date(b.built, { dateStyle: 'short', timeStyle: 'short' }) : '';
          text.append(element('span', 'panel-meta', [b.commit.slice(0, 7), when && t('update.pr.built', { time: when })].filter(Boolean).join(' · ')));
          const running = this.data.current.commit && b.commit.startsWith(this.data.current.commit);
          const install = button('button-subtle', running ? t('update.pr.running') : t('update.pr.install'), () => this.dialog.open({ tag: 'pr-' + b.pr, build: b, restart, recreate }));
          install.disabled = !!running;
          li.append(text, install);
          ul.append(li);
        }
        box.replaceChildren(ul);
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
