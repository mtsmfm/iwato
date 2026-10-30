/* The approve dialog's logic, pure (no DOM): program candidates, the access
   a proposal starts with, path segments, and the scope an approval sends. */
(() => {
  'use strict';
  // The UI's strings (web/i18n.js, loaded first); the key where it is not.
  const t = (key, params) => window.IwatoI18n ? window.IwatoI18n.t(key, params) : key;
  const reads = ['GET', 'HEAD', 'OPTIONS'];
  const isWild = (seg) => seg === '*' || seg === '**';

  // OpenShell (sandbox-policy.rego) compares a program path as it is, and a
  // path with "*" as glob.match(pattern, ["/"], path): "*" and "?" stay
  // within a segment, "**" crosses them. It checks the process and every
  // ancestor, so the programs it starts are covered too.
  function globMatch(pattern, path) {
    if (!pattern.includes('*')) return pattern === path;
    const re = pattern.split(/(\*\*|\*|\?)/).map((x) => x === '**' ? '.*' : x === '*' ? '[^/]*' : x === '?' ? '[^/]' : x.replace(/[.+^${}()|[\]\\]/g, '\\$&')).join('');
    return new RegExp('^' + re + '$').test(path);
  }

  // As the server accepts (openshell_proposals.go validBinary): absolute,
  // printable, no spaces, no empty segment.
  function binaryError(b) {
    if (!b.startsWith('/') || b === '/' || b.length > 512 || /[\s\p{C}\p{Z}]/u.test(b) || b.slice(1).split('/').some((s) => s === '')) {
      return t('approve.binary.invalid');
    }
    return '';
  }

  // A version: 2.1.293, 3.12 (with -rc1, +build and the like), or a hash of
  // 7+ hex digits with a digit in it.
  const versionRE = /\d+(?:\.\d+)+(?:[-+](?:rc|alpha|beta|pre|dev|post|nightly|build)\.?\d*)?/gi;
  const hexRE = /(?<![0-9A-Za-z])(?=[0-9a-f]*\d)[0-9a-f]{7,}(?![0-9A-Za-z])/g;

  // One segment with its versions as "*". A version glued to a name keeps
  // its first number (python3.12 -> python3.*: python3 is the name).
  function starVersions(seg) {
    const letter = (c) => /[A-Za-z]/.test(c || '');
    // A lone "v" marks a version (node-v22.1.0) rather than a name.
    const glued = (at) => letter(seg[at - 1]) && !(/v/i.test(seg[at - 1]) && !letter(seg[at - 2]));
    return seg.replace(versionRE, (v, at) => glued(at) ? v.split('.')[0] + '.*' : '*').replace(hexRE, '*');
  }

  // Candidates for the program, narrow to wide: as proposed, its versions
  // as "*", the name and "*", its folder, every program. Versions only
  // when the path has one.
  function binaryCandidates(path) {
    const out = [];
    const add = (kind, value, label, note) => { if (value && !out.some((c) => c.path === value)) out.push({ kind, path: value, label, note }); };
    if (!path || binaryError(path)) return [{ kind: 'all', path: '/**', label: t('approve.cand.all'), note: t('approve.cand.all.note') }];
    add('exact', path, t('approve.cand.exact'), t('approve.cand.exact.note'));
    const segs = path.split('/');
    const narrow = segs.map(starVersions).join('/');
    if (narrow !== path) {
      add('version', narrow, t('approve.cand.version'), t('approve.cand.version.note'));
      // The wide one cuts each versioned segment where its version starts.
      const wide = segs.map((seg, i) => {
        versionRE.lastIndex = 0;
        const m = versionRE.exec(seg);
        return m && m.index > 0 ? seg.slice(0, m.index) + '*' : segs.map(starVersions)[i];
      }).join('/');
      add('name', wide, t('approve.cand.name'), t('approve.cand.name.note'));
    }
    const dir = path.slice(0, path.lastIndexOf('/'));
    add('folder', (dir || '') + '/*', t('approve.cand.folder'), t('approve.cand.folder.note', { dir: dir || '/' }));
    add('all', '/**', t('approve.cand.all'), t('approve.cand.all.note'));
    return out;
  }

  // Inspecting requests suits HTTP(S) only: an endpoint OpenShell already
  // inspects, or one on a web port.
  function looksHTTP(endpoints) {
    return endpoints.length > 0 && endpoints.every((e) => e.protocol === 'rest' || (e.ports.length > 0 && e.ports.every((p) => [80, 443, 8080, 8443].includes(p))));
  }

  // Reads only, unless writes were seen (then those methods and paths), or
  // the traffic may not be HTTP (then as proposed).
  function defaultAccess(rules, endpoints) {
    if (rules.some((r) => !reads.includes(r.method))) return 'rules';
    return looksHTTP(endpoints) ? 'read' : 'all';
  }

  // The access to start with: the default unless OpenShell would refuse
  // it (blocked: the server's Blocked), then the first choice it takes, in
  // the dialog's order; '' when it takes none.
  function pickAccess(access, blocked = {}) {
    if (!blocked[access]) return access;
    return ['read', 'rules', 'all', 'audit'].find((k) => !blocked[k]) || '';
  }

  // A rule's path for the owner: "/" alone is the root only, "/**" (and
  // OpenShell's "**") every path; they look alike.
  function pathLabel(path) {
    if (!path) return t('approve.path.none');
    if (path === '/') return t('approve.path.root');
    if (path === '/**' || path === '**') return t('approve.path.all', { path });
    return path;
  }

  // A path to narrow reads to when no request was seen, for hosts whose
  // paths are known: GitHub's API and git over HTTPS (owner and repository).
  function pathHint(endpoints) {
    const hosts = endpoints.map((e) => String(e.host || '').toLowerCase());
    if (hosts.length && hosts.every((h) => h === 'api.github.com')) return '/repos/*/*/**';
    if (hosts.length && hosts.every((h) => h === 'github.com')) return '/*/*/**';
    return '';
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

  // A narrowed path for several seen ones: their common folder and "**".
  function pathSuggestion(paths) {
    const list = paths.filter((p) => p.startsWith('/')).map((p) => p.split('/').slice(1));
    if (list.length < 2) return '';
    const common = [];
    for (let i = 0; list.every((s) => i < s.length - 1 && s[i] === list[0][i] && !isWild(s[i])); i++) common.push(list[0][i]);
    return common.length ? '/' + common.join('/') + '/**' : '';
  }

  const pathError = (p) => (!p.startsWith('/') && p !== '**') || p.length > 512 || /[\s\x7f]/.test(p) ? t('approve.path.invalid', { path: JSON.stringify(p) }) : '';

  // The approval: {scope (null: as proposed), rules (what OpenShell will
  // allow, expanded), error}. Read-only with paths allows GET, HEAD and
  // OPTIONS on each; the server expands it the same way. The program is
  // always sent: the server applies exactly it, never the proposal's (a
  // scope naming only the proposal's own programs approves as proposed).
  function buildScope({ access, binary, proposed = [], rows = [], tips = {} }) {
    const out = { scope: null, rules: [], error: '' };
    const b = String(binary || '').trim();
    const scope = {};
    if (b) {
      out.error = binaryError(b);
      scope.binaries = [b];
    } else if (proposed.length) out.error = t('approve.binary.missing');
    const filled = rows.map((r) => ({ method: r.method, path: String(r.path || '').trim() })).filter((r) => r.path);
    if (access === 'audit') scope.audit = true;
    if (access === 'read') {
      scope.readOnly = true;
      const paths = [...new Set(filled.map((r) => r.path))];
      if (!paths.some((p) => p === '**' || p === '/**') && paths.length) {
        scope.rules = paths.map((path) => ({ method: '', path }));
        out.rules = paths.flatMap((path) => reads.map((method) => ({ method, path })));
      } else out.rules = [{ method: 'GET・HEAD・OPTIONS', path: '**' }];
    }
    if (access === 'rules') {
      const seen = new Set();
      scope.rules = filled.filter((r) => !seen.has(r.method + ' ' + r.path) && seen.add(r.method + ' ' + r.path));
      out.rules = scope.rules;
      if (!scope.rules.length) out.error ||= t('approve.rules.missing');
    }
    const entered = scope.rules?.length || 0;
    // Over 16: how to get under it, or past it as proposed.
    if (entered > 16) out.error ||= [t(access === 'read' ? 'approve.tooManyPaths' : 'approve.tooManyRules', { count: entered }), tips.suggest && t('approve.tooMany.suggest'), access === 'read' && t('approve.tooMany.allPaths'), tips.asProposed && t('approve.tooMany.asProposed')].filter(Boolean).join(t('ambiguity.sentenceSep'));
    for (const r of scope.rules || []) out.error ||= pathError(r.path);
    if (Object.keys(scope).length) out.scope = scope;
    return out;
  }

  // The destinations for the owner: the host, and the ports unless just 443.
  const destParts = (endpoints) => endpoints.map((e) => ({ host: e.host || t('policy.anyHost'), port: e.ports.length && !(e.ports.length === 1 && e.ports[0] === 443) ? ':' + e.ports.join(',') : '' }));
  const destLabel = (endpoints) => destParts(endpoints).map((d) => d.host + d.port).join(', ') || t('approve.dest');
  const ruleLabel = (r) => (r.method === '*' ? t('policy.access.full') : r.method) + ' ' + (r.path || t('approve.path.none'));
  // Inspected: rest, and not tls: skip (OpenShell then cannot see the requests).
  const inspected = (endpoints) => endpoints.length > 0 && endpoints.every((e) => e.protocol === 'rest' && e.tls !== 'skip');
  // The rules as proposed: each endpoint's (path-less ones too), else the
  // flat list.
  const askedRules = (endpoints, seen) => endpoints.some((e) => e.rules?.length) ? endpoints.flatMap((e) => e.rules || []) : seen;

  // As proposed, an inspected endpoint that is not enforced (OpenShell's
  // default is audit, or the server did not say) lets every request through.
  const audited = (endpoints) => endpoints.some((e) => e.enforcement !== 'enforce');

  // The writes the approval allows, one line each (empty: reads only). As
  // proposed: an uninspected or audited endpoint lets everything through,
  // an access preset adds its methods on every path, and a proposal whose
  // reach cannot be told (no rules, no preset) is warned as a write.
  function writes({ access, binary, endpoints = [], rows = [], proposed = [], seen = [], placeholder = false }) {
    if (access === 'audit') return [t('approve.writes.audit')];
    if (access === 'all') {
      if (!inspected(endpoints)) return [t('approve.writes.everything')];
      if (audited(endpoints)) return [t('approve.writes.audit')];
      const presets = endpoints.map((e) => e.access).filter(Boolean);
      const out = [];
      if (presets.some((a) => !['read-only', 'read-write', 'full'].includes(a))) out.push(t('approve.writes.unknown'));
      else if (presets.includes('full')) out.push(t('approve.writes.full'));
      else if (presets.includes('read-write')) out.push(t('approve.writes.readwrite'));
      const asked = askedRules(endpoints, seen);
      if (!placeholder && !asked.length && !presets.length) out.push(t('approve.writes.unknown'));
      return out.concat(placeholder ? [] : asked.filter((r) => !reads.includes(r.method)).map(ruleLabel));
    }
    if (access !== 'rules') return [];
    return buildScope({ access, binary, proposed, rows }).rules.filter((r) => !reads.includes(r.method)).map(ruleLabel);
  }

  // What the approval will allow, line by line: the destination, reads or
  // writes, the paths, and the program last.
  function summary({ access, binary, endpoints = [], rows = [], proposed = [], seen = [], placeholder = false }) {
    const b = String(binary || '').trim();
    const built = buildScope({ access, binary, proposed, rows });
    const write = writes({ access, binary, endpoints, rows, proposed, seen, placeholder }).length > 0;
    const out = { dest: destLabel(endpoints), access: '', paths: '', write,
      program: b === '/**' ? t('approve.summary.allPrograms') : t('approve.summary.program', { program: b || '-' }) };
    if (access === 'read') {
      const paths = built.scope?.rules?.map((r) => r.path) || [];
      out.access = t('approve.summary.read');
      out.paths = paths.length ? paths.join(', ') : t('approve.summary.allPaths');
    } else if (access === 'rules') {
      out.access = write ? t('approve.summary.write') : t('approve.summary.readMethods');
      out.paths = built.rules.length ? built.rules.map(ruleLabel).join(', ') : t('approve.summary.rulesNone');
    } else if (access === 'audit') {
      out.access = t('approve.summary.audit');
    } else if (!inspected(endpoints)) {
      out.access = t('approve.summary.everything');
    } else if (audited(endpoints)) {
      out.access = t('approve.summary.asProposedAudit');
    } else if (placeholder) {
      // As proposed with the placeholder: exactly GET / (no request seen asked for it).
      out.access = t('approve.summary.asProposed');
      out.paths = 'GET ' + pathLabel('/');
    } else {
      out.access = t('approve.summary.asProposed');
      out.paths = askedRules(endpoints, seen).map(ruleLabel).join(', ');
    }
    return out;
  }

  // A program glob that matches any program (/**, /**/*, …).
  const allPrograms = (b) => ['/usr/bin/x', '/a/b/c/d/e/f', '/sandbox/.local/bin/x'].every((x) => globMatch(b, x));

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

  // Warnings for the top of the approve dialog, each a line: the proposal's
  // programs when more than one or every program, settings that widen an
  // endpoint, and destinations that reach the host's loopback or Iwato's
  // own port (error: that one cannot be approved). asProposed: the
  // approval keeps the proposal's programs and endpoints as they are.
  function risks({ access, binary, proposed = [], endpoints = [], asProposed = false, listenPort = 0, gateways = [] }) {
    const lines = [];
    let error = '';
    const b = String(binary || '').trim();
    const programs = proposed.join(', ');
    if (asProposed) {
      if (proposed.length > 1) lines.push(t('approve.risk.programs', { count: proposed.length, programs }));
      if (proposed.some(allPrograms)) lines.push(t('approve.risk.allPrograms'));
    } else {
      if (proposed.length > 1) lines.push(t('approve.risk.programs.narrowed', { count: proposed.length, programs, binary: b || '-' }));
      if (proposed.some(allPrograms) && !(b && allPrograms(b))) lines.push(t('approve.risk.allPrograms.named'));
      if (b && allPrograms(b)) lines.push(t('approve.binary.all'));
    }
    const wide = asProposed || access === 'all';
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

  // The whole proposal, line by line, for 詳細: the programs, then each
  // endpoint with its settings, rules, deny rules and allowed_ips.
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

  window.IwatoApprove = { globMatch, binaryError, binaryCandidates, looksHTTP, defaultAccess, pickAccess, pathLabel, pathHint, toggleSegment, pathSuggestion, buildScope, destParts, writes, summary, risks, details, gatewayHit, allPrograms };
})();
