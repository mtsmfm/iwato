/* The approval's logic, pure (no DOM): the rules an opened proposal lists
   to narrow, the scope an approval sends, and its warnings (ADR 0022). */
(() => {
  'use strict';
  // The UI's strings (web/i18n.js, loaded first); the key where it is not.
  const t = (key, params) => window.IwatoI18n ? window.IwatoI18n.t(key, params) : key;
  const reads = ['GET', 'HEAD', 'OPTIONS'];
  const writeMethods = ['POST', 'PUT', 'PATCH', 'DELETE'];
  const isWild = (seg) => seg === '*' || seg === '**';
  const isRead = (m) => reads.includes(m);
  const allPaths = (p) => p === '/**' || p === '**';
  const presetMethods = { 'read-only': reads, 'read-write': [...reads, 'POST', 'PUT', 'PATCH'], full: [...reads, ...writeMethods] };

  // Inspecting requests suits HTTP(S) only: an endpoint OpenShell already
  // inspects, or one on a web port.
  function looksHTTP(endpoints) {
    return endpoints.length > 0 && endpoints.every((e) => e.protocol === 'rest' || (e.ports.length > 0 && e.ports.every((p) => [80, 443, 8080, 8443].includes(p))));
  }
  // Inspected: rest, and not tls: skip (OpenShell then cannot see the requests).
  const inspectedEndpoint = (e) => e.protocol === 'rest' && e.tls !== 'skip';

  // A rule's path for the owner: "/" alone is the root only, "/**" (and
  // OpenShell's "**") every path; they look alike.
  function pathLabel(path) {
    if (!path) return t('approve.path.none');
    if (path === '/') return t('approve.path.root');
    if (allPaths(path)) return t('approve.path.all', { path });
    return path;
  }

  // A click on segment index (0 is the first after "/") turns it to "*",
  // then "**" (which covers the rest), then back to what it was.
  function toggleSegment(path, original, index) {
    const segs = path.split('/').slice(1);
    const orig = original.split('/').slice(1);
    const cur = segs[index];
    if (cur === undefined) return path;
    if (cur === '*') return '/' + [...segs.slice(0, index), '**'].join('/');
    if (cur === '**') {
      const back = orig[index] !== undefined && !isWild(orig[index]) ? orig.slice(index) : ['*'];
      return '/' + [...segs.slice(0, index), ...back].join('/');
    }
    return '/' + [...segs.slice(0, index), '*', ...segs.slice(index + 1)].join('/');
  }

  // The rows an opened proposal lists, each a method and path on a host,
  // checked when the approval keeps it. An endpoint OpenShell does not
  // inspect (its own drafts, mostly) or whose only rule is a GET / nobody
  // saw (placeholder) lists the methods on every path, reads checked; an
  // uninspected one also gets a row to allow it as proposed (L4, ep its
  // index), checked when the port is not a web one. An access preset lists
  // its methods on every path.
  function reviewRows(item) {
    const rows = [];
    const add = (host, method, path, checked) => {
      if (!rows.some((r) => r.kind === 'rule' && r.host === host && r.method === method && r.path === path)) rows.push({ kind: 'rule', host, method, path, checked, write: !isRead(method) });
    };
    for (const [i, e] of (item.endpoints || []).entries()) {
      const host = e.host || '';
      const uninspected = !inspectedEndpoint(e);
      if (uninspected || item.placeholder || (!e.access && !(e.rules || []).length)) {
        const web = looksHTTP([e]);
        for (const m of ['GET', 'HEAD', ...writeMethods]) add(host, m, '/**', web && isRead(m));
        if (uninspected) rows.push({ kind: 'l4', host, ep: i, label: destParts([e]).map((d) => d.host + d.port)[0], checked: !web, write: true });
        continue;
      }
      for (const m of presetMethods[e.access] || []) add(host, m, '/**', true);
      for (const r of e.rules || []) add(host, r.method, r.path, true);
    }
    return rows;
  }

  // Whether the rows, as they are, approve the proposal as proposed: every
  // endpoint inspected and enforced with its own rules, all kept.
  function asProposed(item, rows) {
    const eps = item.endpoints || [];
    return eps.length > 0 && !item.placeholder && eps.every((e) => inspectedEndpoint(e) && e.enforcement === 'enforce' && !e.access && (e.rules || []).length) &&
      rows.every((r) => r.kind !== 'rule' || r.checked);
  }

  // The hosts whose every endpoint is allowed uninspected (their L4 rows
  // checked): their rule rows do not apply.
  function uninspectedHosts(item, rows) {
    const eps = item.endpoints || [];
    const on = new Set(rows.filter((r) => r.kind === 'l4' && r.checked).map((r) => r.ep));
    return new Set(eps.map((e) => e.host || '').filter((h) => eps.every((e, i) => (e.host || '') !== h || on.has(i))));
  }

  // The approval: {scope (null: nothing to send), error}. Every program
  // (ADR 0022); the checked rules per host and the endpoints whose L4 row is
  // checked (uninspected), or as proposed when nothing was narrowed or every
  // endpoint is allowed uninspected. grant is the token checkboxes: attach
  // (inherited; null when not shown) and offer.
  function reviewScope(item, rows, grant = {}) {
    const out = { scope: null, error: '' };
    const scope = { binaries: ['/**'] };
    if (grant.attach === true || grant.attach === false) scope.attach = grant.attach;
    if (grant.offer === true) scope.offer = true;
    const l4 = rows.filter((r) => r.kind === 'l4' && r.checked);
    const all = l4.length > 0 && l4.length === (item.endpoints || []).length;
    if (!all && !(l4.length === 0 && asProposed(item, rows))) {
      const skip = uninspectedHosts(item, rows);
      const picked = rows.filter((r) => r.kind === 'rule' && r.checked && !skip.has(r.host));
      if (l4.length) scope.uninspected = l4.map((r) => r.ep);
      if (!picked.length && !l4.length) {
        out.error = t('review.nothing');
        return out;
      }
      if (picked.length > 16) {
        out.error = t('review.tooMany', { count: picked.length });
        return out;
      }
      const hosts = new Set((item.endpoints || []).map((e) => e.host));
      if (picked.length) scope.rules = picked.map((r) => hosts.size > 1 ? { method: r.method, path: r.path, host: r.host } : { method: r.method, path: r.path });
    }
    out.scope = scope;
    return out;
  }

  // The writes the checked rows allow broadly: a write on every path, or
  // on a wildcard host.
  function broadWrites(rows) {
    return rows.filter((r) => r.kind === 'rule' && r.checked && r.write && (allPaths(r.path) || r.host.includes('*')))
      .map((r) => (r.method === '*' ? t('review.allMethods') : r.method) + ' ' + r.host + r.path);
  }

  const dayMs = 24 * 60 * 60 * 1000;
  // A host's check (host_check.go) as warnings: red (an exfiltration
  // service, the filtering resolver's block), amber (registered under 30
  // days ago), and the facts when neither.
  function hostWarnings(check, now = Date.now()) {
    const out = [];
    const host = check.host;
    if (check.exfil) out.push({ level: 'red', text: t('review.warn.exfil', { host, service: check.exfil }), detail: t('review.warn.exfil.detail') });
    if (check.filter === 'blocked') out.push({ level: 'red', text: t('review.warn.malware', { host }), detail: t('review.warn.malware.detail') });
    const days = check.registered ? Math.floor((now - check.registered * 1000) / dayMs) : -1;
    const date = check.registered ? new Date(check.registered * 1000).toISOString().slice(0, 10) : '';
    if (days >= 0 && days < 30) out.push({ level: 'amber', text: t('review.warn.new', { domain: check.domain || host, days }), detail: t('review.warn.new.detail', { date }) });
    if (!out.some((w) => w.level === 'red')) {
      const facts = [
        check.filter === 'clean' ? t('review.fact.filterClean') : check.filter === 'unknown' ? t('review.fact.filterUnknown') : '',
        days >= 365 ? t('review.fact.years', { years: Math.floor(days / 365), date }) : days >= 30 ? t('review.fact.days', { days, date }) : check.rdap === 'unknown' ? t('review.fact.ageUnknown') : '',
        t('review.fact.noExfil')
      ].filter(Boolean);
      out.push({ level: 'ok', text: t('review.ok', { host }), detail: facts.join(t('review.factSep')) });
    }
    return out;
  }

  // The rows' own warnings: allowed uninspected, broad writes.
  function rowWarnings(rows) {
    const out = [];
    if (rows.some((r) => r.kind === 'l4' && r.checked)) out.push({ level: 'amber', text: t('review.warn.l4'), detail: t('review.warn.l4.detail') });
    else if (rows.some((r) => r.kind === 'l4')) out.push({ level: 'amber', text: t('review.warn.draftL4'), detail: t('review.warn.draftL4.detail') });
    const broad = broadWrites(rows);
    if (broad.length && !rows.some((r) => r.kind === 'l4' && r.checked)) out.push({ level: 'amber', text: t('review.warn.broadWrite'), detail: broad.join(', ') });
    return out;
  }

  // The destinations for the owner: the host, and the ports unless just 443.
  const destParts = (endpoints) => endpoints.map((e) => ({ host: e.host || t('policy.anyHost'), port: e.ports.length && !(e.ports.length === 1 && e.ports[0] === 443) ? ':' + e.ports.join(',') : '' }));
  const ruleLabel = (r) => (r.method === '*' ? t('review.allMethods') : r.method) + ' ' + (r.path || t('approve.path.none'));

  // IPv4 a.b.c.d in an allowed_ips entry (an address or a CIDR).
  function ipIn(ip, entry) {
    const num = (s) => { const p = s.split('.').map(Number); return p.length === 4 && p.every((n) => Number.isInteger(n) && n >= 0 && n < 256) ? p.reduce((a, n) => a * 256 + n, 0) : null; };
    const [base, bits = '32'] = String(entry).split('/');
    const a = num(ip), b = num(base), n = Number(bits);
    if (a === null || b === null || !Number.isInteger(n) || n < 0 || n > 32) return false;
    const size = 2 ** (32 - n);
    return Math.floor(a / size) === Math.floor(b / size);
  }

  // Which of the server's host gateways (names and addresses that reach
  // the host's loopback, proposal_guard.go) an endpoint reaches: its host
  // (a glob too), or an allowed_ips entry with the address.
  function gatewayHit(host, allowedIps = [], gateways = []) {
    const h = String(host || '').toLowerCase().replace(/\.$/, '');
    const glob = (p, x) => new RegExp('^' + p.replace(/[.+^${}()|[\]\\?]/g, '\\$&').replace(/\*/g, '.*') + '$').test(x);
    for (const g of gateways) if (h === g || (h.includes('*') && glob(h, g))) return g;
    for (const g of gateways) if (/^\d+\.\d+\.\d+\.\d+$/.test(g) && allowedIps.some((e) => ipIn(g, e))) return g;
    return '';
  }

  // Endpoint settings that widen what passes, destinations that reach the
  // host's loopback, and Iwato's own port (error: that one cannot be
  // approved), one line each. wide: tls: skip lets everything through.
  function risks({ endpoints = [], wide = true, listenPort = 0, gateways = [] }) {
    const lines = [];
    let error = '';
    for (const e of endpoints) {
      const host = e.host || t('policy.anyHost');
      if (wide && e.tls === 'skip') lines.push(t('approve.risk.tlsSkip', { host }));
      if (e.allowEncodedSlash) lines.push(t('approve.risk.encodedSlash', { host }));
      if (e.websocketCredentialRewrite) lines.push(t('approve.risk.websocket', { host }));
      if (e.requestBodyCredentialRewrite) lines.push(t('approve.risk.body', { host }));
      if (e.allowUninspectedCredentials) lines.push(t('approve.risk.uninspected', { host }));
      const g = gatewayHit(e.host, e.allowedIps || [], gateways);
      if (g) lines.push(t('approve.risk.gateway', { host, gateway: g }));
      if (listenPort && (e.ports || []).includes(listenPort)) {
        error ||= t('approve.risk.iwatoPort', { host, port: listenPort });
        lines.push(error);
      }
    }
    return { lines, error };
  }

  // The whole proposal, line by line: the programs, then each endpoint
  // with its settings, rules, deny rules and allowed_ips.
  function details({ proposed = [], endpoints = [] }) {
    const out = [t('approve.detail.programs', { programs: proposed.join(', ') || '-' })];
    for (const e of endpoints) {
      const head = [(e.host || t('policy.anyHost')) + (e.ports.length ? ':' + e.ports.join(',') : '')];
      head.push(e.protocol ? 'protocol: ' + e.protocol : t('approve.detail.uninspected'));
      if (e.tls) head.push('tls: ' + e.tls);
      if (e.enforcement) head.push('enforcement: ' + e.enforcement);
      if (e.access) head.push('access: ' + e.access);
      if (e.path) head.push('path: ' + e.path);
      for (const [flag, name] of [['allowEncodedSlash', 'allow_encoded_slash'], ['websocketCredentialRewrite', 'websocket_credential_rewrite'], ['requestBodyCredentialRewrite', 'request_body_credential_rewrite'], ['allowUninspectedCredentials', 'allow_uninspected_credentials']]) {
        if (e[flag]) head.push(name);
      }
      if (e.credential) head.push(t('approve.detail.credential', { provider: e.credential }));
      out.push(head.join(' · '));
      for (const r of e.rules || []) out.push('  ' + t('approve.detail.allow', { rule: ruleLabel(r) }));
      for (const r of e.deny || []) out.push('  ' + t('approve.detail.deny', { rule: ruleLabel(r) }));
      if (e.allowedIps?.length) out.push('  allowed_ips: ' + e.allowedIps.join(', '));
    }
    return out;
  }

  window.IwatoApprove = { looksHTTP, pathLabel, toggleSegment, reviewRows, reviewScope, uninspectedHosts, broadWrites, hostWarnings, rowWarnings, destParts, ruleLabel, risks, details, gatewayHit };
})();
