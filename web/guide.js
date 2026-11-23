// A step-by-step guide in a dialog (the Tailscale and WebDAV setups): N / M,
// a progress bar, each step's where (a link), what and, when it is not
// obvious, why; back, next and quit. A step is {key, when(ctx), render(body,
// ctx, nav)}: render fills body and may return a check run on next (true or
// a promise of it moves on); when(ctx) false leaves the step out.
(() => {
  const t = (key, params) => window.IwatoI18n.t(key, params);

  function make({ element, button }) {
    function link(url, label) {
      const a = element('a', 'port-link guide-link', label || url.replace(/^https:\/\//, ''));
      a.href = url;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      return a;
    }

    // rows: [[labelKey, node | string | [nodes]], ...] as a definition list.
    function section(rows) {
      const dl = element('dl', 'guide-section');
      for (const [key, value] of rows) {
        const dd = element('dd');
        for (const v of [].concat(value)) dd.append(typeof v === 'string' ? element('p', '', v) : v);
        dl.append(element('dt', '', t(key)), dd);
      }
      return dl;
    }

    function why(text) {
      const p = element('p', 'guide-why');
      p.append(element('strong', '', t('guide.why') + ' '), element('span', '', text));
      return p;
    }

    // status shows a wait (spinner dot), ok or error line.
    function status() {
      const box = element('div', 'guide-status');
      box.hidden = true;
      return {
        box,
        set(state, ...nodes) {
          box.hidden = false;
          box.dataset.state = state;
          box.replaceChildren(element('span', 'small-dot'), ...nodes.map((n) => typeof n === 'string' ? element('span', '', n) : n));
        },
        clear() { box.hidden = true; box.replaceChildren(); }
      };
    }

    function run({ titleKey, steps, ctx = {}, onDone, onClose }) {
      const dialog = element('section', 'banner banner-pair confirm-panel guide-dialog');
      dialog.setAttribute('role', 'dialog');
      dialog.setAttribute('aria-modal', 'true');
      dialog.setAttribute('aria-label', t(titleKey));
      const inner = element('div', 'login-body guide-body');
      dialog.append(inner);
      (document.body || document.documentElement).append(dialog);
      let index = 0;
      let busy = false;
      let closed = false;
      const shown = () => steps.filter((s) => !s.when || s.when(ctx));
      const close = (done) => {
        if (closed) return;
        closed = true;
        document.removeEventListener('keydown', onKey, true);
        dialog.remove();
        if (done) onDone?.(ctx);
        onClose?.(ctx);
      };
      const onKey = (event) => { if (event.key === 'Escape' && !busy) { event.preventDefault(); close(false); } };
      document.addEventListener('keydown', onKey, true);

      function draw() {
        const list = shown();
        const step = list[index];
        const last = index === list.length - 1;
        const head = element('div', 'guide-head');
        const count = element('span', 'panel-meta guide-count');
        head.append(element('h3', 'guide-title', t(titleKey)), count);
        const bar = element('div', 'guide-bar');
        const fill = element('i');
        bar.append(fill);
        const recount = () => {
          const n = shown().length;
          count.textContent = (index + 1) + ' / ' + n;
          fill.style.width = ((index + 1) / n * 100) + '%';
        };
        recount();
        const body = element('div', 'guide-step');
        body.append(element('h4', 'guide-step-title', t(step.key + '.title')));
        const foot = element('div', 'guide-foot');
        const quit = button('button-subtle', t('guide.quit'), () => close(false));
        const back = button('button-subtle', t('guide.back'), () => { if (busy) return; index--; draw(); });
        back.disabled = index === 0;
        const next = button('button-primary', t(last ? 'guide.done' : 'guide.next'), async () => {
          if (busy || next.disabled) return;
          busy = true;
          back.disabled = quit.disabled = true;
          let ok = false;
          try { ok = await (check ? check() : true); } finally {
            busy = false;
            if (!closed) { back.disabled = index === 0; quit.disabled = false; }
          }
          if (!ok || closed) return;
          // A step's check may change which steps follow (when).
          if (index >= shown().length - 1) close(true);
          else { index++; draw(); }
        });
        const nav = {
          setNext(enabled, labelKey) { next.disabled = !enabled; if (labelKey) next.textContent = t(labelKey); },
          next: () => next.click(),
          // recount: a check changed which steps follow.
          recount,
          close: () => close(false)
        };
        foot.append(quit, element('span', 'guide-grow'), back, next);
        inner.replaceChildren(head, bar, body, foot);
        const check = step.render(body, ctx, nav);
        const coarse = typeof matchMedia === 'function' && matchMedia('(pointer: coarse)').matches;
        if (!coarse) (body.querySelector?.('input, textarea') || next).focus?.();
      }
      draw();
      return { close: () => close(false) };
    }

    return { run, link, section, why, status };
  }

  window.IwatoGuide = { make };
})();
