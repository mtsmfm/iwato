// The logins, in the settings: each browser signed in (sessions.go,
// GET /api/sessions), signing this one out, revoking another and signing
// every other one out.
(() => {
  'use strict';

  const t = (key, params) => window.IwatoI18n.t(key, params);
  const text = (v, max) => typeof v === 'string' ? v.slice(0, max) : '';

  // deviceName is a short name for a User-Agent: the browser and the OS.
  function deviceName(ua) {
    ua = text(ua, 300);
    const browser = /Edg(e|A|iOS)?\//.test(ua) ? 'Edge' : /OPR\//.test(ua) ? 'Opera' : /Firefox\/|FxiOS/.test(ua) ? 'Firefox'
      : /Chrome\/|CriOS/.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : '';
    const os = /iPhone/.test(ua) ? 'iPhone' : /iPad/.test(ua) ? 'iPad' : /Android/.test(ua) ? 'Android' : /Windows/.test(ua) ? 'Windows'
      : /Mac OS X|Macintosh/.test(ua) ? 'macOS' : /CrOS/.test(ua) ? 'ChromeOS' : /Linux/.test(ua) ? 'Linux' : '';
    if (browser && os) return t('sessions.device', { browser, os });
    return browser || os || (ua ? ua.slice(0, 60) : t('sessions.unknownDevice'));
  }

  const sessionView = (s) => s && /^[a-f0-9]{16}$/.test(s.id) ? { id: s.id, agent: text(s.agent, 300), created: text(s.created, 40), used: text(s.used, 40), current: s.current === true } : null;

  function make({ element, button, api, askConfirm, onEnded, onAuth }) {
    const bind = (node, key, params) => window.IwatoI18n.bind(node, key, params);
    const when = (iso) => Number.isFinite(Date.parse(iso)) ? window.IwatoI18n.date(iso, { dateStyle: 'medium', timeStyle: 'short' }) : '—';

    class SessionSection {
      constructor() {
        this.root = element('div', 'session-settings');
        this.list = element('div', 'volume-list session-list');
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        this.others = bind(button('button-disconnect', '', () => this.revokeOthers()), 'sessions.revokeOthers');
        this.actions = element('div', 'login-form');
        this.actions.hidden = true;
        this.actions.append(this.others);
        this.root.append(bind(element('h4', 'files-head'), 'sessions.title'), this.list, this.actions, this.status);
        this.sessions = [];
      }

      async refresh() {
        try {
          const data = await api('/api/sessions');
          this.sessions = (Array.isArray(data?.sessions) ? data.sessions : []).slice(0, 200).map(sessionView).filter(Boolean);
          this.render();
        } catch (error) {
          this.status.textContent = t('sessions.loadFailed', { message: error.message });
          if (error.status === 401 || error.status === 403) onAuth();
        }
      }

      render() {
        const rows = this.sessions.map((s) => {
          const row = element('div', 'volume-row files-key');
          const info = element('div', 'volume-info');
          const name = element('span', 'volume-path', deviceName(s.agent));
          if (s.agent) name.title = s.agent;
          info.append(name);
          const meta = t('sessions.meta', { created: when(s.created), used: window.IwatoFiles.ago(s.used) });
          info.append(element('span', 'panel-meta', s.current ? t('sessions.current') + ' · ' + meta : meta));
          const action = s.current
            ? button('button-subtle', t('sessions.logout'), () => this.logout())
            : button('button-disconnect', t('sessions.revoke'), () => this.revoke(s));
          if (!s.current) action.setAttribute('aria-label', t('sessions.revoke.label', { name: deviceName(s.agent) }));
          row.append(info, action);
          return row;
        });
        this.list.replaceChildren(...rows);
        this.actions.hidden = !this.sessions.some((s) => !s.current);
      }

      async run(question, ok, path, after) {
        if (this.busy || !await askConfirm(question, { ok, danger: true })) return;
        this.busy = true;
        this.status.textContent = '';
        try {
          const data = await api(path, { method: 'POST', body: '{}' });
          await after(data);
        } catch (error) {
          this.status.textContent = t('sessions.failed', { message: error.message });
          if (error.status === 401 || error.status === 403) onAuth();
        } finally {
          this.busy = false;
        }
      }

      logout() {
        return this.run(t('sessions.logout.confirm'), t('sessions.logout'), '/api/sessions/logout', () => onEnded());
      }

      revoke(s) {
        return this.run(t('sessions.revoke.confirm', { name: deviceName(s.agent) }), t('sessions.revoke'), '/api/sessions/' + s.id + '/revoke', () => this.refresh());
      }

      revokeOthers() {
        return this.run(t('sessions.revokeOthers.confirm'), t('sessions.revokeOthers'), '/api/sessions/revoke-others', async (data) => {
          await this.refresh();
          this.status.textContent = t('sessions.revokedOthers', { count: Number.isInteger(data?.revoked) ? data.revoked : 0 });
        });
      }
    }

    return { SessionSection };
  }

  window.IwatoSessions = { deviceName, make };
})();
