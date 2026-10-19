// Delete everything, at the bottom of the settings: deletes everything
// Iwato made (reset.go, POST /api/reset) after the owner typed the word.
// Every login ends with it: the page then asks for the console's new URL.
(() => {
  'use strict';

  // The UI's strings (web/i18n.js), looked up when used: this file loads first.
  const t = (key, params) => window.IwatoI18n.t(key, params);

  const word = 'reset';
  const text = (v, max) => typeof v === 'string' ? v.slice(0, max) : '';
  // ready: the typed confirmation (spaces around it are forgiven).
  const ready = (value) => String(value || '').trim() === word;
  // resultView is the server's answer: what it could not do, as text (the
  // page's words for a known code, reset.warning.<code>, else the server's).
  const resultView = (d) => {
    const texts = Array.isArray(d?.warnings) ? d.warnings : [], codes = Array.isArray(d?.warningCodes) && d.warningCodes.length === texts.length ? d.warningCodes : [];
    return {
      warnings: texts.slice(0, 50).map((w, i) => {
        if (typeof w !== 'string') return null;
        const c = codes[i], code = text(c?.code, 32);
        return code && window.IwatoI18n?.has('reset.warning.' + code) ? t('reset.warning.' + code, params(c.params, 1000)) : w.slice(0, 1000);
      }).filter((w) => w !== null)
    };
  };
  // String params only, cut to max.
  const params = (p, max) => {
    const out = {};
    for (const [k, v] of Object.entries(p && typeof p === 'object' ? p : {}).slice(0, 4)) if (typeof v === 'string') out[k] = v.slice(0, max);
    return out;
  };
  // The step: the page's words for a known code (reset.step.<code>), else the server's.
  const stepText = (d) => {
    const step = text(d?.step, 300), code = text(d?.code, 32);
    if (!code || !window.IwatoI18n?.has('reset.step.' + code)) return step;
    return t('reset.step.' + code, params(d?.params, 100));
  };
  const progressView = (d) => ({ running: d?.running === true, step: stepText(d), seconds: Number.isInteger(d?.seconds) && d.seconds >= 0 ? d.seconds : 0 });

  function make({ element, button, api, onEnded, onAuth }) {
    function list(items) {
      const ul = element('ul', 'reset-list');
      for (const item of items) ul.append(element('li', '', item));
      return ul;
    }

    class ResetSection {
      constructor() {
        this.root = element('div', 'reset-settings');
        this.open = window.IwatoI18n.bind(button('button-disconnect', '', () => this.show()), 'reset.open');
        const row = element('div', 'login-form');
        row.append(this.open);
        this.root.append(window.IwatoI18n.bind(element('h4', 'files-head'), 'reset.title'),
          window.IwatoI18n.bind(element('p', 'panel-meta'), 'reset.intro'), row);
        this.dialog = element('section', 'banner banner-pair confirm-panel reset-dialog');
        this.dialog.setAttribute('role', 'dialog');
        this.dialog.setAttribute('aria-modal', 'true');
        window.IwatoI18n.bindAttribute(this.dialog, 'aria-label', 'reset.title');
        this.dialog.hidden = true;
        this.body = element('div', 'login-body reset-body');
        this.dialog.append(this.body);
        this.dialog.addEventListener('keydown', (event) => { if (event.key === 'Escape') this.close(); });
      }

      show() {
        if (this.busy) return;
        // Outside the settings, so that the result stays when the page
        // signs out behind it.
        const host = document.body || this.root;
        if (!host.contains(this.dialog)) host.append(this.dialog);
        this.renderForm();
        this.dialog.hidden = false;
        // On a phone the keyboard would cover the list.
        if (matchMedia('(pointer: coarse)').matches) this.cancel.focus();
        else this.input.focus();
      }

      close() {
        if (this.busy) return;
        this.dialog.hidden = true;
        if (this.ended) { this.ended = false; onEnded(); }
      }

      renderForm() {
        this.owner = element('input');
        this.owner.type = 'checkbox';
        const ownerLabel = element('label', 'reset-owner');
        ownerLabel.append(this.owner, element('span', '', t('reset.ownerFiles')));
        this.keptNote = element('p', 'panel-meta reset-kept');
        const renderKept = () => { this.keptNote.textContent = t('reset.kept', { kept: t('reset.keptList') + (this.owner.checked ? '' : t('common.listSep') + t('reset.ownerKept')) }); };
        this.owner.addEventListener('change', renderKept);
        renderKept();
        this.input = element('input', 'text-input');
        this.input.type = 'text';
        this.input.autocomplete = 'off';
        this.input.setAttribute('autocapitalize', 'off');
        this.input.setAttribute('spellcheck', 'false');
        this.input.placeholder = t('reset.confirmPlaceholder', { word });
        this.input.setAttribute('aria-label', t('reset.confirmLabel', { word }));
        this.submit = element('button', 'button button-disconnect', t('reset.title'));
        this.submit.type = 'submit';
        this.submit.disabled = true;
        this.input.addEventListener('input', () => { this.submit.disabled = this.busy || !ready(this.input.value); });
        this.cancel = button('button-subtle', t('common.cancel'), () => this.close());
        const form = element('form', 'stack-form');
        const row = element('div', 'login-form');
        row.append(this.submit, this.cancel);
        form.append(this.input, row);
        form.addEventListener('submit', (event) => { event.preventDefault(); this.run(); });
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        this.body.replaceChildren(element('strong', '', t('reset.title')),
          element('p', 'panel-meta', t('reset.removes')), list(['reset.removed.sandboxes', 'reset.removed.gateway', 'reset.removed.images', 'reset.removed.secrets'].map((k) => t(k))), ownerLabel, this.keptNote,
          element('p', 'panel-meta reset-login', t('reset.loginNote')),
          form, this.status);
      }

      async run() {
        if (this.busy || !ready(this.input.value)) {
          if (!this.busy) this.status.textContent = t('reset.typeWord', { word });
          return;
        }
        this.busy = true;
        for (const node of [this.submit, this.cancel, this.input, this.owner]) node.disabled = true;
        this.status.textContent = t('reset.deleting');
        const started = Date.now();
        // The progress comes from GET /api/reset while the logins last.
        let polling = true;
        const poll = async () => {
          while (polling) {
            await new Promise((resolve) => setTimeout(resolve, 2000));
            if (!polling) return;
            try {
              const p = progressView(await api('/api/reset'));
              if (polling && p.running) this.status.textContent = t('reset.progress', { step: p.step, seconds: p.seconds });
            } catch (_) { /* the logins are gone near the end */ }
          }
        };
        poll();
        try {
          const result = resultView(await api('/api/reset', { method: 'POST', body: JSON.stringify({ confirm: word, ownerFiles: !!this.owner.checked }), timeoutMs: 31 * 60 * 1000 }));
          polling = false;
          this.busy = false;
          this.ended = true;
          this.renderDone(result, Math.round((Date.now() - started) / 1000));
        } catch (error) {
          polling = false;
          this.busy = false;
          this.status.textContent = t('reset.failed', { message: error.message });
          for (const node of [this.cancel, this.input, this.owner]) node.disabled = false;
          this.submit.disabled = !ready(this.input.value);
          if (error.status === 401 || error.status === 403) onAuth();
        }
      }

      renderDone(result, seconds) {
        const nodes = [element('strong', '', t('reset.done')),
          element('p', 'panel-meta', t('reset.done.text', { seconds })),
          element('p', 'reset-next', t('reset.done.next'))];
        if (result.warnings.length) {
          nodes.push(element('p', 'panel-meta', t('reset.warnings')), list(result.warnings));
        }
        const row = element('div', 'login-form');
        this.closeButton = button('button-primary', t('common.close'), () => this.close());
        row.append(this.closeButton);
        nodes.push(row);
        this.body.replaceChildren(...nodes);
        this.closeButton.focus();
      }
    }

    return { ResetSection };
  }

  window.IwatoReset = { word, ready, resultView, progressView, make };
})();
