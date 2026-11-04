// The sandboxes' resources (resources.go, GET /api/resources): the row in
// the card's header, the Resources tab, the sidebar's lines and totals,
// and the settings' section. The numbers and their levels are worked out
// here without the DOM (tested by ui-behavior-tests.cjs).
(() => {
  'use strict';

  const t = (key, params) => window.IwatoI18n.t(key, params);
  const GiB = 1024 ** 3;
  const historyMs = 10 * 60 * 1000;
  const num = (v) => typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : null;
  const gb = (bytes) => (bytes / GiB).toFixed(1);
  // ratio 0..1 against the percentages of the settings.
  const level = (ratio, s) => ratio == null ? '' : ratio * 100 >= s.crit ? 'crit' : ratio * 100 >= s.warn ? 'warn' : '';

  function settingsView(d) {
    const pct = (v, def) => Number.isInteger(v) && v > 0 && v <= 100 ? v : def;
    return { hide: d?.hide === true, warn: pct(d?.warn, 80), crit: pct(d?.crit, 95), notify: d?.notify === true };
  }

  function usageView(r) {
    if (!r || typeof r !== 'object') return null;
    const mem = r.memory && typeof r.memory === 'object' ? r.memory : null;
    const gpu = r.gpu && typeof r.gpu === 'object' && num(r.gpu.total) ? r.gpu : null;
    const disk = r.disk && typeof r.disk === 'object' ? r.disk : null;
    return {
      running: r.running === true, at: num(r.at) || 0, cpu: num(r.cpu), cores: num(r.cores) || 0, cpuLimit: num(r.cpuLimit) || 0,
      memory: mem && num(mem.limit) ? { used: num(mem.used) || 0, current: num(mem.current) || 0, anon: num(mem.anon) || 0, cache: num(mem.cache) || 0, limit: mem.limit } : null,
      gpu: gpu ? { used: num(gpu.used) || 0, total: gpu.total, util: num(gpu.util) || 0, noPerProcess: gpu.noPerProcess === true } : null,
      disk: disk ? {
        total: num(disk.total) || 0, at: num(disk.at) || 0, measuring: disk.measuring === true,
        top: (Array.isArray(disk.top) ? disk.top : []).filter((f) => f && typeof f.path === 'string' && num(f.size) != null).slice(0, 16).map((f) => ({ path: f.path.slice(0, 300), size: f.size }))
      } : null
    };
  }

  // Store keeps the last answer and, per sandbox, the CPU and memory of the
  // last 10 minutes this page saw.
  class Store {
    constructor() { this.usage = new Map(); this.history = new Map(); this.host = { cpus: 0, memory: 0 }; this.settings = settingsView(null); this.loaded = false; }

    update(data, now = Date.now()) {
      this.loaded = true;
      this.settings = settingsView(data?.settings);
      this.host = { cpus: num(data?.host?.cpus) || 0, memory: num(data?.host?.memory) || 0 };
      const seen = new Set();
      for (const [name, raw] of Object.entries(data?.sandboxes && typeof data.sandboxes === 'object' ? data.sandboxes : {}).slice(0, 64)) {
        const u = usageView(raw);
        if (!u) continue;
        seen.add(name);
        this.usage.set(name, u);
        const h = this.history.get(name) || [];
        if (!u.running) { this.history.delete(name); continue; }
        if (u.at && u.memory && (!h.length || h[h.length - 1].at !== u.at)) h.push({ at: u.at, cpu: u.cpu, mem: u.memory.used });
        while (h.length && h[0].at < now - historyMs) h.shift();
        this.history.set(name, h);
      }
      for (const name of [...this.usage.keys()]) if (!seen.has(name)) { this.usage.delete(name); this.history.delete(name); }
    }

    get(name) { return this.usage.get(name) || null; }
  }

  // The header's metrics: running, CPU, memory and the GPU; stopped, the
  // home. short is the narrow header's value.
  // Room for the largest value (used = the limit), in ch, long and short:
  // the header's other items do not move as the numbers change.
  const widths = (limit) => [(gb(limit) + '/' + gb(limit) + ' GB').length, (gb(limit) + 'G').length];

  function headerMetrics(u, s) {
    if (!u) return [];
    if (!u.running) return u.disk && u.disk.at ? [{ key: 'home', label: t('res.home'), value: gb(u.disk.total) + ' GB', short: gb(u.disk.total) + 'G', ratio: null, level: '' }] : [];
    if (u.cpu == null && !u.memory) return [];
    const out = [];
    // CPU needs two reads: a dash holds its place until then.
    const cpu = u.cpu == null ? '–' : Math.round(u.cpu) + '%';
    out.push({ key: 'cpu', label: 'CPU', value: cpu, short: cpu, width: [4, 4], ratio: u.cpu == null ? 0 : u.cpu / 100, level: u.cpu == null ? '' : level(u.cpu / 100, s) });
    if (u.memory) {
      const r = u.memory.used / u.memory.limit;
      out.push({ key: 'memory', label: t('res.memory'), value: gb(u.memory.used) + '/' + gb(u.memory.limit) + ' GB', short: gb(u.memory.used) + 'G', width: widths(u.memory.limit), ratio: r, level: level(r, s) });
    }
    if (u.gpu) {
      const r = u.gpu.used / u.gpu.total;
      out.push({ key: 'gpu', label: 'GPU', value: gb(u.gpu.used) + '/' + gb(u.gpu.total) + ' GB', short: gb(u.gpu.used) + 'G', width: widths(u.gpu.total), ratio: r, level: level(r, s) });
    }
    return out;
  }

  // The running sandboxes together against the PC: cores used, memory
  // used, and the GPU (nvidia-smi gives the whole GPU's, so the largest).
  function totals(store, running) {
    const s = store.settings;
    let cores = 0, memory = 0, gpu = null, count = 0;
    for (const name of running) {
      const u = store.get(name);
      count++;
      if (!u?.running) continue;
      cores += u.cores;
      if (u.memory) memory += u.memory.used;
      if (u.gpu && (!gpu || u.gpu.used > gpu.used)) gpu = { used: u.gpu.used, total: u.gpu.total };
    }
    const { cpus, memory: hostMemory } = store.host;
    return {
      count,
      cpu: { used: cores, total: cpus, ratio: cpus ? cores / cpus : null, level: cpus ? level(cores / cpus, s) : '' },
      memory: { used: memory, total: hostMemory, ratio: hostMemory ? memory / hostMemory : null, level: hostMemory ? level(memory / hostMemory, s) : '' },
      gpu: gpu && { ...gpu, ratio: gpu.used / gpu.total, level: level(gpu.used / gpu.total, s) }
    };
  }

  // When a home was measured: minutes ago today, else the date.
  function measured(seconds, now = Date.now()) {
    const ago = Math.max(0, Math.round((now / 1000 - seconds) / 60));
    if (ago < 1) return t('res.ago.now');
    if (ago < 60) return t('res.ago.minutes', { minutes: ago });
    return window.IwatoI18n.date(seconds * 1000, { dateStyle: 'short', timeStyle: 'short' });
  }

  function make({ element, button, panelHeader, api, onAuth }) {
    const bar = (ratio, lvl, className = 'res-bar') => {
      const b = element('span', className);
      const fill = element('i', lvl);
      fill.style.width = Math.min(100, Math.max(0, (ratio || 0) * 100)).toFixed(0) + '%';
      b.append(fill);
      return b;
    };

    // The header's row: a button that opens the Resources tab.
    class Row {
      constructor(open) {
        this.root = element('button', 'res-row');
        this.root.type = 'button';
        this.root.hidden = true;
        this.root.addEventListener('click', open);
        this.shown = '';
      }

      update(u, s) {
        const metrics = s.hide ? [] : headerMetrics(u, s);
        const key = JSON.stringify(metrics) + t('res.open');
        this.root.hidden = !metrics.length;
        if (key === this.shown) return;
        this.shown = key;
        this.root.title = t('res.open');
        this.root.setAttribute('aria-label', t('res.open') + ': ' + metrics.map((m) => m.label + ' ' + m.value).join(', '));
        this.root.replaceChildren(...metrics.map((m) => {
          const node = element('span', 'res-metric res-' + m.key + (m.level ? ' ' + m.level : ''));
          const value = element('b');
          const long = element('span', 'res-long', m.value), short = element('span', 'res-short', m.short);
          if (m.width) { long.style.minWidth = m.width[0] + 'ch'; short.style.minWidth = m.width[1] + 'ch'; }
          value.append(long, short);
          node.append(element('span', 'res-label', m.label), value);
          if (m.ratio != null) node.append(bar(m.ratio, '', 'res-mini'));
          return node;
        }));
      }
    }

    const svgNS = 'http://www.w3.org/2000/svg';
    // A line over the last 10 minutes, scaled to max.
    function spark(points, max, color) {
      if (typeof document.createElementNS !== 'function' || points.length < 2) return element('div', 'res-spark-empty', t('res.collecting'));
      const w = 300, h = 60, p = 2, first = points[0].at, span = Math.max(1, points[points.length - 1].at - first);
      const x = (pt) => p + (pt.at - first) / span * (w - 2 * p);
      const y = (v) => h - p - Math.min(1, v / max) * (h - 2 * p);
      const line = points.map((pt, i) => (i ? 'L' : 'M') + x(pt).toFixed(1) + ' ' + y(pt.v).toFixed(1)).join(' ');
      const svg = document.createElementNS(svgNS, 'svg');
      svg.setAttribute('class', 'res-spark');
      svg.setAttribute('viewBox', '0 0 ' + w + ' ' + h);
      svg.setAttribute('preserveAspectRatio', 'none');
      svg.setAttribute('role', 'img');
      svg.setAttribute('aria-label', t('res.last10'));
      const path = (d, attrs) => { const e = document.createElementNS(svgNS, 'path'); e.setAttribute('d', d); for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v); return e; };
      svg.append(path(line + ' L ' + x(points[points.length - 1]).toFixed(1) + ' ' + h + ' L ' + p + ' ' + h + ' Z', { fill: color, 'fill-opacity': '.12' }),
        path(line, { fill: 'none', stroke: color, 'stroke-width': '1.6', 'vector-effect': 'non-scaling-stroke' }));
      return svg;
    }

    // The Resources tab.
    class Panel {
      constructor(sandbox, store) {
        this.sandbox = sandbox;
        this.store = store;
        this.root = element('section', 'side-panel res-panel');
        this.root.setAttribute('aria-label', t('tab.resources'));
        this.root.hidden = true;
        this.closeButton = button('button-subtle', t('common.close'), () => this.onClose?.());
        this.body = element('div', 'res-detail');
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        this.root.append(panelHeader({ key: 'res.title' }, this.closeButton), this.status, this.body);
      }

      show() { this.root.hidden = false; this.render(); }
      hide() { this.root.hidden = true; }

      section(title, note) {
        const sec = element('section', 'res-sec');
        const head = element('h4', 'res-head', title);
        if (note) head.append(element('small', '', note));
        sec.append(head);
        this.body.append(sec);
        return sec;
      }

      big(value, note) {
        const b = element('div', 'res-big', value);
        if (note) b.append(element('small', '', note));
        return b;
      }

      render() {
        if (this.root.hidden) return;
        const u = this.store.get(this.sandbox.name);
        const s = this.store.settings;
        this.body.replaceChildren();
        this.status.textContent = !u || (u.running && !u.memory && !u.disk) ? t('res.collecting') : u.running ? '' : t('res.stopped');
        if (!u) return;
        const hist = this.store.history.get(this.sandbox.name) || [];
        if (u.running && u.memory) {
          const cpu = this.section('CPU', t('res.cpu.note', { cores: u.cpuLimit ? +u.cpuLimit.toFixed(1) : '?' }));
          cpu.append(this.big(u.cpu == null ? '–' : Math.round(u.cpu) + '%', t('res.cpu.cores', { cores: u.cores.toFixed(1) })),
            spark(hist.filter((p) => p.cpu != null).map((p) => ({ at: p.at, v: p.cpu })), 100, '#8ddeba'));
          if (u.cpu != null) cpu.dataset.level = level(u.cpu / 100, s);
          const m = u.memory;
          const mem = this.section(t('res.memory'), t('res.memory.note', { limit: gb(m.limit) }));
          mem.dataset.level = level(m.used / m.limit, s);
          const used = Math.max(0, m.current - m.cache);
          const stack = element('div', 'res-stack');
          stack.setAttribute('aria-label', t('res.memory.breakdown'));
          for (const [v, cls] of [[used, 'res-used'], [m.cache, 'res-cache']]) {
            const part = element('i', cls);
            part.style.width = (Math.min(1, v / m.limit) * 100).toFixed(1) + '%';
            stack.append(part);
          }
          const legend = element('div', 'res-legend');
          legend.append(element('span', 'res-used', t('res.memory.used', { size: gb(used) })), element('span', 'res-cache', t('res.memory.cache', { size: gb(m.cache) })),
            element('span', 'res-free', t('res.memory.free', { size: gb(Math.max(0, m.limit - m.current)) })));
          mem.append(this.big(gb(m.used) + ' GB', t('res.memory.of', { limit: gb(m.limit) })),
            spark(hist.map((p) => ({ at: p.at, v: p.mem })), m.limit, '#8ec5f5'), stack, legend);
        }
        const d = u.disk;
        const home = this.section(t('res.home'), d && d.at ? t('res.home.measured', { when: measured(d.at) }) : '');
        if (d && d.at) {
          home.append(this.big(gb(d.total) + ' GB'));
          const table = element('table', 'res-folders');
          const largest = d.top[0]?.size || 1;
          for (const f of d.top) {
            const row = element('tr');
            const bars = element('td', 'res-folder-bar');
            bars.append(bar(f.size / largest, ''));
            row.append(element('td', 'res-folder', f.path), bars, element('td', 'res-size', gb(f.size) + ' GB'));
            table.append(row);
          }
          home.append(table);
        } else home.append(element('p', 'panel-meta', d?.measuring ? t('res.home.measuring') : t('res.home.none')));
        if (u.running) {
          const again = button('button-subtle', d?.measuring ? t('res.home.measuring') : t('res.home.again'), () => this.measure(again));
          again.disabled = !!d?.measuring;
          home.append(again);
        }
        if (u.running && u.gpu) {
          const g = u.gpu;
          const gpu = this.section(t('res.gpu'), t('res.gpu.wide'));
          gpu.dataset.level = level(g.used / g.total, s);
          gpu.append(this.big(gb(g.used) + ' GB', t('res.memory.of', { limit: gb(g.total) })), bar(g.used / g.total, level(g.used / g.total, s), 'res-bar res-bar-big'));
          if (g.noPerProcess) gpu.append(element('p', 'res-warn', t('res.gpu.wsl')));
        }
      }

      async measure(node) {
        node.disabled = true;
        try {
          await api('/api/resources/measure', { method: 'POST', body: JSON.stringify({ sandbox: this.sandbox.name }) });
          const u = this.store.get(this.sandbox.name);
          if (u) u.disk = { ...(u.disk || { total: 0, at: 0, top: [] }), measuring: true };
          this.render();
        } catch (error) {
          this.status.textContent = error.message;
          node.disabled = false;
          if (error.status === 401 || error.status === 403) onAuth();
        }
      }
    }

    // The sidebar's totals, against the PC.
    class Totals {
      constructor(root) {
        this.root = root;
        this.root.hidden = true;
      }

      update(store, running) {
        if (!store.loaded || store.settings.hide) { this.root.hidden = true; return; }
        const sum = totals(store, running);
        this.root.hidden = false;
        const line = (label, part, value) => {
          const row = element('div', 'res-total');
          row.append(element('span', '', label), bar(part.ratio, part.level), element('em', '', value));
          return row;
        };
        const rows = [element('h3', 'res-totals-head', t('res.totals', { count: sum.count })),
          line('CPU', sum.cpu, t('res.totals.cores', { used: sum.cpu.used.toFixed(1), total: sum.cpu.total || '?' })),
          line(t('res.memory'), sum.memory, gb(sum.memory.used) + ' / ' + (sum.memory.total ? gb(sum.memory.total) : '?') + ' GB')];
        if (sum.gpu) rows.push(line('GPU', sum.gpu, gb(sum.gpu.used) + ' / ' + gb(sum.gpu.total) + ' GB'));
        this.root.replaceChildren(...rows);
      }
    }

    // The settings' section: show or hide, the levels, the notification.
    class Settings {
      constructor(onChange) {
        this.onChange = onChange;
        this.root = element('div', 'res-settings');
        this.title = element('h4', 'files-head', t('res.settings.title'));
        const check = (key) => {
          const label = element('label', 'scope-choice');
          const box = element('input');
          box.type = 'checkbox';
          box.addEventListener('change', () => this.save());
          label.append(box, element('span', '', t(key)));
          return [label, box];
        };
        const number = (key) => {
          const label = element('label', 'res-level');
          const input = element('input', 'text-input');
          input.type = 'number';
          input.min = '10';
          input.max = '100';
          input.addEventListener('change', () => this.save());
          label.append(element('span', '', t(key)), input, element('span', '', '%'));
          return [label, input];
        };
        let showLabel, warnLabel, critLabel, notifyLabel;
        [showLabel, this.show] = check('res.settings.show');
        [warnLabel, this.warn] = number('res.settings.warn');
        [critLabel, this.crit] = number('res.settings.crit');
        [notifyLabel, this.notify] = check('res.settings.notify');
        this.status = element('p', 'panel-meta');
        this.status.setAttribute('role', 'status');
        const levels = element('div', 'res-levels');
        levels.append(warnLabel, critLabel);
        this.root.append(this.title, showLabel, levels, notifyLabel, element('p', 'panel-meta', t('res.settings.note')), this.status);
      }

      async refresh() {
        try { this.render(settingsView(await api('/api/resources/settings'))); } catch (error) {
          this.status.textContent = error.message;
          if (error.status === 401 || error.status === 403) onAuth();
        }
      }

      render(s) {
        this.saved = s;
        this.show.checked = !s.hide;
        this.warn.value = String(s.warn);
        this.crit.value = String(s.crit);
        this.notify.checked = s.notify;
      }

      async save() {
        const s = { hide: !this.show.checked, warn: Number(this.warn.value), crit: Number(this.crit.value), notify: this.notify.checked };
        this.status.textContent = '';
        try {
          await api('/api/resources/settings', { method: 'POST', body: JSON.stringify(s) });
          this.saved = s;
          this.status.textContent = t('res.settings.saved');
          this.onChange(s);
        } catch (error) {
          this.status.textContent = t('res.settings.failed', { message: error.message });
          if (this.saved) this.render(this.saved);
          if (error.status === 401 || error.status === 403) onAuth();
        }
      }
    }

    return { Row, Panel, Totals, Settings };
  }

  window.IwatoResources = { Store, headerMetrics, totals, level, measured, settingsView, make };
})();
