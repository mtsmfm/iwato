(() => {
  'use strict';

  const $ = (id) => document.getElementById(id);
  // The UI's strings (web/i18n.js; how to add one: AGENTS.md).
  const { t, bind, bindAttribute } = window.IwatoI18n;
  const grid = $('terminal-grid');
  const cards = new Map();
  const state = { csrf: '', authorized: false, authenticating: false, fake: false, refreshing: null, disposed: false, selected: '' };
  const statusKeys = { 'not-created': 'status.notCreated', running: 'status.running', stopped: 'status.stopped', stopping: 'status.stopping', error: 'status.error', starting: 'status.starting' };
  const theme = {
    background: '#111419', foreground: '#d2dbe5', cursor: '#a1ddba', cursorAccent: '#111419',
    selectionBackground: '#4d75616b', black: '#29343c', red: '#f58b95', green: '#9bdab1',
    yellow: '#e6c28c', blue: '#8eb6e2', magenta: '#c6a3df', cyan: '#86c6cf', white: '#d2dbe5',
    brightBlack: '#748597', brightRed: '#ffabb3', brightGreen: '#b8f1c8', brightYellow: '#f9d7a3',
    brightBlue: '#b1d0f2', brightMagenta: '#e1bfee', brightCyan: '#abe3e8', brightWhite: '#f1f5fa'
  };
  // Moralerspace Neon HW (web/vendor/fonts, @font-face in styles.css) has the
  // Japanese and Nerd Font glyphs eza and starship print. The terminal
  // measures its cells with the font it opens with, so a terminal opened
  // before the font loads starts on the fallback and switches afterwards.
  const terminalFont = {
    fallback: '"Cascadia Code", "SFMono-Regular", Consolas, "Liberation Mono", monospace',
    // Moralerspace lacks Claude Code's spinner symbols (⏺ ✻ ✶ ✳ ✢); naming a
    // font that has them spares Chrome a search through every system font.
    family: '"Moralerspace Neon HW", "Cascadia Code", "SFMono-Regular", Consolas, "Liberation Mono", "Segoe UI Symbol", monospace',
    ready: false, loading: Promise.resolve()
  };
  // Debug options (/debug.js turns them on in debug builds only).
  const debugParams = new URLSearchParams(window.IwatoDebug === true ? location.search : '');
  // /?font=fallback keeps the fallback fonts (to compare rendering cost).
  if (debugParams.get('font') !== 'fallback' && typeof document.fonts?.load === 'function') {
    terminalFont.loading = Promise.all(['12px', 'bold 12px'].map((font) => document.fonts.load(font + ' "Moralerspace Neon HW"')))
      .then((faces) => { terminalFont.ready = faces.every((list) => list.length > 0); }, () => { /* not served: the fallback stays */ });
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }
  // Terminal sessions per sandbox: "main" (shown as 1) and numbered ones.
  const maxTerms = 8;
  const termNamePattern = /^[a-z0-9][a-z0-9-]{0,31}$/;
  const termLabel = (name) => name === 'main' ? '1' : name;
  // The shown terminal is remembered per sandbox (not a secret: sign-in
  // stays in the HttpOnly cookie, never in storage scripts can read).

  function savedTerm(sandbox) {
    try {
      const name = localStorage.getItem('iwato-term:' + sandbox);
      return name && termNamePattern.test(name) ? name : 'main';
    } catch (_) { return 'main'; }
  }
  function saveTerm(sandbox, name) {
    try { localStorage.setItem('iwato-term:' + sandbox, name); } catch (_) { /* storage unavailable */ }
  }
  // The editor and the terminal side by side (wide cards only), per
  // browser: on or off, and the editor's share of the width.
  const splitMinWidth = 900;
  const splitMinPane = 320;
  function savedSplit() {
    try {
      const saved = JSON.parse(localStorage.getItem('iwato-split') || '{}');
      const at = Number(saved.at);
      return { on: saved.on === true, at: at > 0 && at < 1 ? at : 0.5 };
    } catch (_) { return { on: false, at: 0.5 }; }
  }
  function saveSplit(split) {
    try { localStorage.setItem('iwato-split', JSON.stringify(split)); } catch (_) { /* storage unavailable */ }
  }
  // On a touch screen, focusing the terminal opens the on-screen keyboard.
  const touchScreen = () => matchMedia('(pointer: coarse)').matches;
  // Images pasted into a terminal (paste.go): the types and size taken.
  const pasteImageTypes = ['image/png', 'image/jpeg', 'image/gif', 'image/webp'];
  const maxPasteImage = 20 << 20;
  // A path as one shell word, with backslashes as a local terminal pastes a
  // dropped file (Claude Code takes those away again; quotes it would not).
  const shellWord = (path) => path.replace(/[^A-Za-z0-9_\/.,:@%+=\-\u0080-\uffff]/g, '\\$&');
  function button(className, label, action) {
    const node = element('button', 'button ' + className, label);
    node.type = 'button';
    node.addEventListener('click', action);
    return node;
  }
  // keepCode gives holder[key], a YAML editor (web/editor.js mount) made once
  // per owner, so a re-render puts the same one back (cursor and undo
  // stay). A new owner replaces it; no owner destroys it.
  function keepCode(holder, key, owner, options) {
    const old = holder[key];
    if (old && old.owner === owner) return old;
    old?.destroy();
    holder[key] = null;
    if (!owner) return null;
    const made = window.IwatoCode.mount(null, { language: 'yaml', ...options() });
    made.owner = owner;
    return (holder[key] = made);
  }
  // Confirmation in the page instead of a browser dialog. Resolves true only
  // for the confirm button; cancel, Esc, the backdrop, or asking again while
  // one is open answer false. The text is set as text, never as HTML.
  let pendingConfirm = null;
  let typingCard = null;
  // Where the keyboard was when the question came up; it goes back there
  // (making the page inert took it away), or to the terminal that had it
  // when that was a menu item that is gone now.
  let confirmReturn = null;
  function answerConfirm(value) {
    const done = pendingConfirm;
    if (!done) return;
    pendingConfirm = null;
    $('confirm-panel').hidden = true;
    $('app-shell').inert = false;
    const back = confirmReturn;
    confirmReturn = null;
    // Hiding the dialog leaves the focus on its button until the browser notices.
    if ($('confirm-panel').contains(document.activeElement)) document.activeElement.blur();
    if (back && back !== document.body && back.isConnected && back.checkVisibility?.() !== false) back.focus({ preventScroll: true });
    else typingCard?.restoreFocus();
    done(value);
  }
  // A change that lets a provider's credential go with a rule (the server's
  // 409 names the hosts): asked, with the provider's scope suggested where
  // it has one.
  function confirmProviderHosts(error) {
    return askConfirm(t('policy.providerHost.confirm', { message: error.message }) + (error.scoped ? t('policy.providerHost.scoped') : ''), { ok: t('policy.providerHost.ok'), danger: true });
  }

  function askConfirm(text, { ok = t('confirm.ok'), danger = false } = {}) {
    // One question at a time; the page behind is inert until it is answered.
    if (pendingConfirm) return Promise.resolve(false);
    $('confirm-text').textContent = text;
    $('confirm-ok').textContent = ok;
    $('confirm-ok').className = 'button ' + (danger ? 'button-disconnect' : 'button-primary');
    confirmReturn = document.activeElement;
    $('confirm-panel').hidden = false;
    $('app-shell').inert = true;
    // Cancel has the focus, so a stray Enter does not approve.
    $('confirm-cancel').focus();
    return new Promise((resolve) => { pendingConfirm = resolve; });
  }
  // The add and allow dialogs narrow an endpoint: an access preset, or
  // methods and paths.
  const scopeMethods = ['*', 'GET', 'HEAD', 'OPTIONS', 'POST', 'PUT', 'PATCH', 'DELETE'];
  function methodSelect(method) {
    const select = element('select');
    for (const m of scopeMethods) { const o = element('option', '', m === '*' ? t('policy.method.all') : m); o.value = m; select.append(o); }
    select.value = method;
    select.setAttribute('aria-label', t('policy.method'));
    return select;
  }
  function addScopeRule(method = '*', path = '') {
    const row = element('div', 'approve-rule');
    const select = methodSelect(method);
    const input = element('input', 'text-input');
    input.type = 'text';
    input.placeholder = '/path/** ';
    input.value = path;
    input.setAttribute('aria-label', t('policy.path'));
    const remove = button('button-subtle button-icon', '×', () => row.remove());
    remove.setAttribute('aria-label', t('policy.removeRow'));
    // The path's segments, to click into "*" and "**" (as in the approve dialog).
    const segs = element('div', 'approve-segs');
    let original = path;
    const renderSegs = (focus = -1) => {
      const value = String(input.value || '').trim();
      segs.replaceChildren();
      (value.startsWith('/') ? value.split('/').slice(1) : []).forEach((seg, k) => {
        if (!seg) return;
        const b = button('approve-seg', '/' + seg, () => {
          input.value = window.IwatoApprove.toggleSegment(String(input.value || '').trim(), original, k);
          renderSegs(k);
        });
        b.className = 'approve-seg';
        b.dataset.wild = String(seg === '*' || seg === '**');
        b.setAttribute('aria-label', t('approve.segment', { segment: seg }));
        segs.append(b);
        if (k === focus) b.focus();
      });
    };
    input.addEventListener('input', () => { original = String(input.value || '').trim(); renderSegs(); });
    renderSegs();
    row.append(select, input, remove, segs);
    $('approve-rules').append(row);
    return row;
  }
  const scopeRadios = ['all', 'read', 'readwrite', 'full', 'rules'].map((v) => $('approve-scope-' + v));
  let scopeSeed = [];
  function scopeChoice() { return scopeRadios.find((r) => r.checked)?.value || 'all'; }
  function showScopeRules() {
    const rules = scopeChoice() === 'rules';
    $('approve-rules').hidden = !rules;
    $('approve-add-rule').hidden = !rules;
    $('policy-audit').disabled = scopeChoice() === 'all';
    if (rules && !$('approve-rules').children.length) {
      // The methods and paths the audit log already names are the
      // starting point.
      if (scopeSeed.length) for (const r of scopeSeed) addScopeRule(r.method, r.path);
      else addScopeRule();
    }
  }
  for (const radio of scopeRadios) radio.addEventListener('change', showScopeRules);
  $('approve-add-rule').addEventListener('click', () => addScopeRule().querySelector?.('input')?.focus?.());
  // The narrowing serves two dialogs (data-mode): add (an endpoint, with
  // the access presets and audit) and allow (methods and paths only).
  // policyFormFor keeps an add's input for a retry after an error, until
  // another dialog opens.
  let policyFormFor = '';
  function resetScope(mode, choice = 'all', seed = []) {
    $('approve-scope').dataset.mode = mode;
    scopeSeed = seed;
    $('approve-rules').textContent = '';
    for (const radio of scopeRadios) radio.checked = radio.value === choice;
    $('policy-audit').checked = false;
    showScopeRules();
  }
  function scopeRules() {
    return [...$('approve-rules').children].map((row) => ({ method: row.children[0].value, path: String(row.children[1].value || '').trim() })).filter((r) => r.path);
  }

  // Approving a proposal: reads only or writes too (methods per path), on
  // the paths listed; under 詳細 the program (a glob such as claude-*), as
  // proposed and audit. The logic is web/approve.js; this is its form.
  // Resolves to null (cancelled) or {scope} (null scope: as proposed).
  const approveLogic = window.IwatoApprove;
  const pathLabel = approveLogic.pathLabel;
  const accessLabel = (access) => ({ 'read-only': t('policy.access.read'), 'read-write': t('policy.access.readwrite'), full: t('policy.access.full') })[access];
  const accessRadios = ['read', 'rules', 'all', 'audit'].map((v) => $('approve-access-' + v));
  let approving = null;
  const approveAccess = () => accessRadios.find((r) => r.checked)?.value || 'all';
  const readMethod = (m) => ['GET', 'HEAD', 'OPTIONS'].includes(m);
  function approveRows() {
    return [...$('approve-path-rows').children].map((row) => ({ method: row.method.value, path: String(row.input.value || '') }));
  }
  // The top of the approve dialogs: the destination, the agent's reason,
  // and warnings ([text, detail]).
  function showApproveHead(item, warnings = []) {
    const head = $('approve-head');
    head.replaceChildren(...approveLogic.destParts(item.endpoints || []).map((d) => {
      const line = element('p', 'approve-host', d.host);
      if (d.port) line.append(element('span', 'approve-port', d.port));
      return line;
    }));
    // Without endpoints, OpenShell's summary instead.
    if (!head.children.length && item.summary) head.append(element('p', 'approve-intent', item.summary));
    if (item.intent) head.append(element('p', 'approve-intent', t('policy.quote', { text: item.intent })));
    for (const [text, detail] of warnings) {
      head.append(element('p', 'approve-warn', text));
      if (detail) head.append(element('p', 'panel-meta approve-warn-detail', detail));
    }
    head.hidden = false;
  }
  function hideApproveHead() {
    $('approve-head').hidden = true;
    $('approve-grant').hidden = true;
    $('approve-risks').hidden = true;
    $('approve-proposal').hidden = true;
  }
  // The warnings in the main view (the proposal's programs, settings that
  // widen it, the host's loopback, Iwato's port), styled as the writes; and
  // the whole proposal under its own 詳細.
  function showApproveRisks(lines) {
    const box = $('approve-risks');
    box.replaceChildren(element('p', 'approve-writes-title', t('approve.risks.title')), ...lines.map((w) => element('p', 'approve-writes-line', w)));
    box.hidden = !lines.length;
  }
  function showApproveProposal(item) {
    const lines = approveLogic.details({ proposed: item.binaries || [], endpoints: item.endpoints || [] });
    $('approve-proposal-body').replaceChildren(...lines.map((l) => element('code', 'approve-proposal-line', l)));
    $('approve-proposal').open = false;
    $('approve-proposal').hidden = false;
  }
  // Each path's segments, to click into "*" and "**".
  function renderSegments(row, focus = -1) {
    const value = String(row.input.value || '').trim();
    const segs = value.startsWith('/') ? value.split('/').slice(1) : [];
    row.segs.replaceChildren();
    segs.forEach((seg, k) => {
      if (!seg) return;
      const b = button('approve-seg', '/' + seg, () => {
        row.input.value = approveLogic.toggleSegment(String(row.input.value || '').trim(), row.original, k);
        renderApprove();
        renderSegments(row, k);
      });
      b.className = 'approve-seg';
      b.dataset.wild = String(seg === '*' || seg === '**');
      b.setAttribute('aria-label', t('approve.segment', { segment: seg }));
      row.segs.append(b);
      if (k === focus) b.focus();
    });
  }
  // A path row: a read badge (reads only) or the method, coloured read or
  // write; the path; its segments.
  function addApprovePath(method = 'GET', path = '') {
    const row = element('div', 'approve-path');
    const line = element('div', 'approve-path-line');
    row.badge = element('span', 'approve-badge', t('approve.badge.read'));
    row.badge.dataset.kind = 'read';
    row.method = methodSelect(method);
    const mark = () => { row.method.dataset.kind = readMethod(row.method.value) ? 'read' : 'write'; };
    mark();
    row.method.addEventListener('change', () => { mark(); renderApprove(); });
    row.input = element('input', 'text-input');
    row.input.type = 'text';
    row.input.placeholder = '/path/**';
    row.input.value = path;
    row.input.spellcheck = false;
    row.input.setAttribute('aria-label', t('policy.path'));
    row.original = path;
    row.input.addEventListener('input', () => { row.original = String(row.input.value || '').trim(); renderApprove(); renderSegments(row); });
    const remove = button('button-subtle button-icon', '×', () => { row.remove(); renderApprove(); });
    remove.setAttribute('aria-label', t('approve.removePath'));
    row.segs = element('div', 'approve-segs');
    line.append(row.badge, row.method, row.input, remove);
    row.append(line, row.segs);
    $('approve-path-rows').append(row);
    renderSegments(row);
    return row;
  }
  function setApprovePaths(rules) {
    $('approve-path-rows').textContent = '';
    for (const r of rules) addApprovePath(r.method, r.path);
    renderApprove();
  }
  function renderApprove() {
    const s = approving;
    if (!s) return;
    const access = approveAccess();
    $('approve-form').dataset.access = access;
    const binary = String($('approve-binary').value || '').trim();
    for (const chip of $('approve-binary-chips').children) chip.setAttribute('aria-pressed', String(chip.dataset.path === binary));
    const check = $('approve-binary-check');
    let note = '', level = '';
    if (!binary) [note, level] = s.proposed.length ? [t('approve.binary.missing'), 'error'] : [t('approve.binary.none'), ''];
    else if (approveLogic.binaryError(binary)) [note, level] = [approveLogic.binaryError(binary), 'error'];
    else if (binary === '/**') [note, level] = [t('approve.binary.all'), 'warn'];
    else if (s.proposed[0]) {
      const hit = approveLogic.globMatch(binary, s.proposed[0]);
      [note, level] = [hit ? t('approve.binary.hit') : t('approve.binary.miss', { binary: s.proposed[0] }), hit ? '' : 'warn'];
    }
    if (s.proposed.length > 1 && binary !== s.proposed[0]) note += t('approve.binary.replaces', { count: s.proposed.length });
    check.textContent = note;
    check.dataset.state = level;
    const rows = approveRows();
    $('approve-seen').textContent = !rows.length && access === 'read' ? t(s.seen.length ? 'approve.seen.empty' : 'approve.seen.none')
      : rows.length && s.seen.length ? t('approve.seen') : '';
    const built = approveLogic.buildScope({ access, binary, proposed: s.proposed, rows });
    // A choice OpenShell would refuse (the radios are disabled; when all are, nothing can be approved).
    if (s.blocked[access]) built.error ||= s.blocked[access].chosen || t('approve.blocked.chosen', { choice: t('ambiguity.choice.' + access) });
    const risk = approveLogic.risks({ access, binary, proposed: s.proposed, endpoints: s.endpoints, listenPort: policyGuard.listenPort, gateways: policyGuard.gateways });
    showApproveRisks(risk.lines);
    built.error ||= risk.error;
    const args = { access, binary, proposed: s.proposed, endpoints: s.endpoints, rows, seen: s.seen, placeholder: s.placeholder };
    // The writes it allows, spelled out.
    const writes = approveLogic.writes(args);
    $('approve-writes').replaceChildren(element('p', 'approve-writes-title', t('approve.writes.title')), ...writes.map((w) => element('code', 'approve-writes-line', w)));
    $('approve-writes').hidden = !writes.length;
    $('approve-form').dataset.write = String(writes.length > 0);
    const sum = approveLogic.summary(args);
    const box = $('approve-summary');
    box.dataset.write = String(sum.write);
    box.replaceChildren(element('p', 'approve-sum-dest', sum.dest), element('p', 'approve-sum-access', sum.access), ...(sum.paths ? [element('p', 'approve-sum-paths', sum.paths)] : []), element('p', 'approve-sum-program', sum.program));
    $('approve-error').textContent = built.error;
    $('approve-error').hidden = !built.error;
    $('confirm-ok').disabled = Boolean(built.error);
    s.built = built;
  }
  $('approve-binary').addEventListener('input', renderApprove);
  for (const radio of accessRadios) radio.addEventListener('change', renderApprove);
  $('approve-add-path').addEventListener('click', () => { addApprovePath(approveAccess() === 'read' ? 'GET' : '*').input.focus?.(); renderApprove(); });
  async function askApprove(question, item, { asProposed = false, warnings = [] } = {}) {
    policyFormFor = '';
    showApproveHead(item, warnings);
    showApproveProposal(item);
    // What the page cannot show of the proposal: not as proposed.
    const unshown = item.unshown?.length ? t('approve.unshown', { parts: item.unshown.join(', ') }) : '';
    if (asProposed) {
      // Nothing to narrow: the plain question, with what it allows.
      $('approve-form').hidden = true;
      const args = { access: 'all', endpoints: item.endpoints || [], seen: item.rules || [], placeholder: item.placeholder === true };
      const risk = approveLogic.risks({ proposed: item.binaries || [], endpoints: item.endpoints || [], asProposed: true, listenPort: policyGuard.listenPort, gateways: policyGuard.gateways });
      const writes = approveLogic.writes(args);
      showApproveRisks([...(unshown ? [unshown] : []), risk.error, ...writes.map((w) => t('approve.risk.write', { what: w })), ...risk.lines.filter((l) => l !== risk.error)].filter(Boolean));
      $('confirm-ok').disabled = Boolean(unshown || risk.error);
      const yes = await askConfirm(question, { ok: t('approve.ok') });
      $('confirm-ok').disabled = false;
      hideApproveHead();
      return yes && !unshown && !risk.error ? { scope: null } : null;
    }
    const proposed = item.binaries || [];
    const endpoints = item.endpoints || [];
    const seen = item.rules || [];
    // Choices an endpoint already in the policy rules out (a provider
    // enforcing the same host, say): disabled, with why. As proposed is
    // ruled out too when the page cannot show all of the proposal.
    const blocked = { ...(item.blocked || {}) };
    if (unshown) blocked.all = { reason: unshown, chosen: unshown };
    const access = approveLogic.pickAccess(approveLogic.defaultAccess(seen, endpoints), blocked);
    approving = { proposed, endpoints, seen, built: null, blocked, placeholder: item.placeholder === true };
    // The candidates, narrow to wide; a path in the proposed folder shows
    // as …/ to keep them short. Versions as "*" by default, so that an
    // update of the program keeps the rule.
    const candidates = approveLogic.binaryCandidates(proposed[0] || '');
    $('approve-binary').value = candidates.find((c) => c.kind === 'version')?.path || proposed[0] || '';
    const dir = (proposed[0] || '').slice(0, (proposed[0] || '').lastIndexOf('/') + 1);
    $('approve-binary-chips').replaceChildren(...candidates.map((c) => {
      const chip = button('approve-chip', '', () => { $('approve-binary').value = c.path; renderApprove(); });
      chip.className = 'approve-chip';
      chip.dataset.path = c.path;
      chip.dataset.kind = c.kind;
      chip.title = c.path + ' (' + c.note + ')';
      chip.append(element('b', '', c.label), element('span', '', c.kind === 'all' ? c.path : dir && c.path.startsWith(dir) ? '…/' + c.path.slice(dir.length) : c.path));
      return chip;
    }));
    for (const radio of accessRadios) {
      radio.checked = radio.value === access;
      radio.disabled = Boolean(blocked[radio.value]);
      if (radio.parentElement) {
        radio.parentElement.dataset.blocked = String(radio.disabled);
        radio.parentElement.dataset.note = radio.disabled ? t('approve.blocked.tag') : '';
      }
    }
    // Blocked main choices get one line; why is under 詳細.
    const mainBlocked = ['read', 'rules'].filter((k) => blocked[k]);
    $('approve-blocked-line').textContent = mainBlocked.length ? t('approve.blocked.short', { choices: window.IwatoI18n.list(mainBlocked.map((k) => t('ambiguity.choice.quoted', { choice: t('ambiguity.choice.' + k) }))) }) : '';
    $('approve-blocked-line').hidden = !mainBlocked.length;
    $('approve-blocked').textContent = Object.values(blocked).map((b) => b.reason).join('\n');
    $('approve-blocked').hidden = !$('approve-blocked').textContent;
    $('approve-more').open = access === 'all' || access === 'audit' || mainBlocked.length > 0;
    // A GET / that OpenShell's proposal carries without anyone asking
    // for it (the server's placeholder) is no request seen: the list
    // starts empty (all paths), with an example to narrow.
    const hint = seen.length ? '' : approveLogic.pathHint(endpoints);
    const suggest = approveLogic.pathSuggestion(seen.map((r) => r.path));
    $('approve-path-suggest').replaceChildren(...[suggest && [t('approve.suggest'), suggest, () => setApprovePaths([{ method: access === 'rules' ? '*' : 'GET', path: suggest }])], hint && [t('approve.example'), hint, () => setApprovePaths([{ method: approveAccess() === 'rules' ? '*' : 'GET', path: hint }])], seen.length && ['', t('approve.allPaths'), () => setApprovePaths([])]].filter(Boolean).map(([label, path, act]) => {
      const chip = button('approve-chip', '', act);
      chip.className = 'approve-chip';
      if (label) chip.append(element('b', '', label));
      chip.append(element('span', '', path));
      return chip;
    }));
    const unique = access === 'read' ? seen.filter((r, i) => seen.findIndex((x) => x.path === r.path) === i) : seen;
    setApprovePaths(unique);
    $('approve-form').hidden = false;
    const ok = await askConfirm(question, { ok: t('approve.ok') });
    $('approve-form').hidden = true;
    hideApproveHead();
    $('confirm-ok').disabled = false;
    for (const radio of accessRadios) radio.disabled = false;
    const built = approving.built;
    approving = null;
    if (!ok || built.error) return null;
    return { scope: built.scope };
  }
  // A proposal that widens a provider's scope: what the scope gains, each
  // line a write or not.
  function showScopeGrant(item, ps) {
    const box = $('approve-grant');
    box.replaceChildren(element('p', 'approve-head', t('approve.grant.title', { provider: approveLogic.providerName(ps) })));
    for (const line of approveLogic.scopeLines(ps)) {
      const p = element('p', 'approve-grant-line');
      p.dataset.write = String(line.write);
      if (line.write) {
        const badge = element('span', 'approve-badge', t('approve.badge.write'));
        badge.dataset.kind = 'write';
        p.append(badge);
      }
      p.append(element('span', '', line.text));
      box.append(p);
    }
    if (item.rules?.length) box.append(element('p', 'panel-meta approve-grant-req', t('approve.grant.requests', { requests: item.rules.map((r) => (r.method === '*' ? t('policy.method.all') : r.method) + ' ' + r.path).join(', ') })));
    box.append(element('p', 'panel-meta', t('approve.grant.shared')));
    box.hidden = false;
  }
  // An endpoint for a new rule, or (rule given) for an existing one, which
  // keeps its programs. Resolves to the edit to post, or null.
  // prefill starts a new rule from requests an endpoint denied: its host,
  // ports, programs, and methods and paths.
  async function askPolicyAdd(text, key, rule, prefill) {
    const form = $('policy-form');
    if (policyFormFor !== key) {
      for (const id of ['policy-rule', 'policy-binaries', 'policy-host', 'policy-ports']) $(id).value = '';
      $('policy-any').checked = false;
      resetScope('add');
      if (prefill) {
        $('policy-host').value = prefill.host;
        $('policy-ports').value = prefill.ports.join(', ');
        $('policy-binaries').value = prefill.binaries.join('\n');
        $('policy-any').checked = !prefill.binaries.length;
        resetScope('add', 'rules', prefill.rules);
      }
    }
    policyFormFor = key;
    renderPolicyHostRisk();
    form.dataset.mode = rule ? 'endpoint' : 'rule';
    if (rule) $('policy-rule').value = rule;
    $('policy-rule').readOnly = Boolean(rule);
    form.hidden = false;
    $('approve-scope').hidden = false;
    const ok = await askConfirm(text, { ok: t('policy.add.ok') });
    form.hidden = true;
    $('approve-scope').hidden = true;
    if (!ok) return null;
    const edit = { rule: String($('policy-rule').value || '').trim(), host: String($('policy-host').value || '').trim(),
      ports: String($('policy-ports').value || '').split(/[\s,]+/).filter(Boolean).map(Number).filter((n) => Number.isInteger(n) && n >= 0) };
    if (!rule) {
      if ($('policy-any').checked) edit.anyBinary = true;
      else edit.binaries = String($('policy-binaries').value || '').split('\n').map((b) => b.trim()).filter(Boolean);
    }
    const choice = scopeChoice();
    const access = { read: 'read-only', readwrite: 'read-write', full: 'full' }[choice];
    if (access) edit.access = access;
    if (choice === 'rules') edit.rules = scopeRules();
    if (choice !== 'all' && $('policy-audit').checked) edit.audit = true;
    return edit;
  }
  function resetPolicyForm() { policyFormFor = ''; }
  // A host that reaches the host's loopback, or Iwato's own port (which
  // the server refuses), warned as the approve dialog does.
  function renderPolicyHostRisk() {
    const host = String($('policy-host').value || '').trim();
    const ports = String($('policy-ports').value || '').split(/[\s,]+/).filter(Boolean).map(Number);
    const risk = approveLogic.risks({ endpoints: [{ host, ports }], listenPort: policyGuard.listenPort, gateways: policyGuard.gateways });
    const box = $('policy-host-risk');
    box.replaceChildren(element('p', 'approve-writes-title', t('approve.risks.title')), ...risk.lines.map((w) => element('p', 'approve-writes-line', w)));
    box.hidden = !host || !risk.lines.length;
  }
  for (const id of ['policy-host', 'policy-ports']) $(id).addEventListener('input', renderPolicyHostRisk);
  // Methods and paths for an inspected endpoint; null when cancelled.
  async function askAllow(text, seed = [], okLabel = t('policy.add.ok')) {
    policyFormFor = '';
    resetScope('allow', 'rules', seed);
    $('approve-scope').hidden = false;
    const ok = await askConfirm(text, { ok: okLabel });
    $('approve-scope').hidden = true;
    return ok ? scopeRules() : null;
  }
  $('confirm-ok').addEventListener('click', () => answerConfirm(true));
  $('confirm-cancel').addEventListener('click', () => answerConfirm(false));
  // Only a click that both starts and ends on the backdrop closes: a text
  // selection dragged out of the dialog ends there too.
  let confirmPressed = null;
  $('confirm-panel').addEventListener('pointerdown', (event) => { confirmPressed = event.target; });
  $('confirm-panel').addEventListener('click', (event) => {
    const backdrop = $('confirm-panel');
    if (event.target === backdrop && confirmPressed === backdrop) answerConfirm(false);
    confirmPressed = null;
  });
  function showNotice(title, message) {
    // A script's title replaces the default's key.
    $('notice-title').removeAttribute('data-i18n');
    $('notice-title').textContent = title;
    $('notice-text').textContent = message;
    $('notice').hidden = false;
  }
  function clearNotice() { $('notice').hidden = true; }
  // The server's state in the sidebar: a key of web/i18n.js.
  function serverStatus(key, kind) {
    bind($('server-label'), key);
    $('server-state').dataset.state = kind;
  }
  // One sandbox is shown at a time; the sidebar (a chip row on phones) switches.
  function selectCard(card) {
    state.selected = card ? card.sandbox.name : '';
    for (const other of cards.values()) {
      other.root.classList.remove('selected');
      other.nav.classList.remove('active');
      other.renderTermTabs();
    }
    if (!card) return;
    card.root.classList.add('selected');
    card.nav.classList.add('active');
    card.armAutoConnect();
  }
  // On phones the global actions sit behind the ≡ button.
  function closeMenu() {
    $('sidebar').classList.remove('menu-open');
    $('menu-button').setAttribute('aria-expanded', 'false');
  }

  // Running sandboxes first; the rest below a separator. The nav is rebuilt
  // only when the order changes, so a focused entry keeps its focus.
  const navSeparator = element('div', 'nav-separator');
  navSeparator.setAttribute('role', 'separator');
  function orderNav() {
    if (!cards.size) return;
    const all = [...cards.values()];
    const running = all.filter((card) => card.sandbox.status === 'running').map((card) => card.nav);
    const rest = all.filter((card) => card.sandbox.status !== 'running').map((card) => card.nav);
    navSeparator.hidden = !running.length || !rest.length;
    const order = [...running, navSeparator, ...rest];
    const nav = $('sandbox-nav');
    if (nav.children.length !== order.length || order.some((node, i) => nav.children[i] !== node)) nav.replaceChildren(...order);
  }

  // The badges count only the agent's proposals: OpenShell drafts one from
  // every blocked connection (source 'auto'), which stay listed in the policy
  // tab but would keep the badges lit.
  function agentPending(snapshot) {
    return (snapshot?.pending || []).filter((p) => p.source === 'agent').length;
  }

  // The page title (and an installed app's badge) count the agent's
  // proposals waiting for the owner, as the pushes do (notifier.go).
  const baseTitle = document.title || 'Iwato';
  function renderPendingTotal() {
    const total = [...cards.values()].reduce((sum, card) => sum + agentPending(card.policy?.snapshot), 0);
    const title = (total ? '(' + total + ') ' : '') + baseTitle;
    if (document.title !== title) document.title = title;
    if (total === renderPendingTotal.last) return;
    renderPendingTotal.last = total;
    if (typeof navigator !== 'undefined' && typeof navigator.setAppBadge === 'function') {
      (total ? navigator.setAppBadge(total) : navigator.clearAppBadge()).catch(() => {});
    }
  }

  function summary() {
    orderNav();
    renderPendingTotal();
    $('connection-count').textContent = String([...cards.values()].filter((card) => card.connection === 'connected').length);
  }

  async function api(path, options = {}) {
    const { timeoutMs = 20000, ...requestOptions } = options;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const headers = { Accept: 'application/json', ...options.headers };
      if (['POST', 'PUT', 'DELETE'].includes(options.method)) {
        if (!headers['Content-Type']) headers['Content-Type'] = 'application/json';
        if (path !== '/api/session') headers['X-CSRF-Token'] = state.csrf;
      }
      const response = await fetch(path, {
        ...requestOptions, headers, credentials: 'same-origin', cache: 'no-store',
        redirect: 'error', signal: controller.signal
      });
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        // Details the page can act on (credential conflicts: the rules named).
        const conflicts = Array.isArray(data.conflicts) ? data.conflicts.filter((c) => c && typeof c.rule === 'string').slice(0, 20).map((c) => ({
          rule: text(c.rule, 200), host: text(c.host, 253), ports: (Array.isArray(c.ports) ? c.ports : []).filter(Number.isInteger).slice(0, 16), mode: text(c.mode, 32),
          provider: text(c.provider, 64), label: text(c.label, 100), profile: text(c.profile, 128), binaries: (Array.isArray(c.binaries) ? c.binaries : []).filter((b) => typeof b === 'string').slice(0, 16).map((b) => b.slice(0, 512))
        })) : null;
        // Endpoints OpenShell would find ambiguous (the rules named), and
        // the pairs of the gateway's own refusal.
        const ambiguities = Array.isArray(data.ambiguities) ? data.ambiguities.filter((a) => a && typeof a.rule === 'string').slice(0, 20).map(validAmbiguity) : null;
        const gateway = validGatewayAmbiguities(data.gatewayAmbiguities);
        const message = typeof data.error === 'string'
          ? window.IwatoRefusals.message(data.code, data.params, { conflicts, ambiguities, gateway }, data.error.slice(0, 1000))
          : t('app.httpError', { status: response.status });
        const error = new Error(message);
        error.status = response.status;
        if (typeof data.code === 'string') error.code = data.code.slice(0, 64);
        if (conflicts) error.conflicts = conflicts;
        if (ambiguities) error.ambiguities = ambiguities;
        if (typeof data.raw === 'string') error.raw = data.raw.slice(0, 4000);
        // The editor's save: the file changed since it was opened.
        if (data.changed === true) error.changed = true;
        // A change to confirm first (a provider's credential goes with it):
        // the retry carries the token; scoped: the provider's scope can say it.
        if (typeof data.confirm === 'string') { error.confirm = data.confirm.slice(0, 128); error.scoped = data.scoped === true; }
        // The endpoints that would carry a provider's credential beyond its scope.
        if (Array.isArray(data.beyond)) error.beyond = data.beyond.filter((b) => b && typeof b.rule === 'string').slice(0, 20).map((b) => ({
          rule: text(b.rule, 200), host: text(b.host, 253), ports: (Array.isArray(b.ports) ? b.ports : []).filter(Number.isInteger).slice(0, 16),
          binaries: (Array.isArray(b.binaries) ? b.binaries : []).filter((x) => typeof x === 'string').slice(0, 16).map((x) => x.slice(0, 512))
        }));
        // The rules a recreate could not put back.
        if (Array.isArray(data.failed)) error.failed = carryFailures(data.failed);
        throw error;
      }
      return data;
    } catch (error) {
      if (error.name === 'AbortError') throw new Error(t('app.timeout'));
      if (error instanceof TypeError) throw new Error(t('app.unreachable'));
      throw error;
    } finally {
      clearTimeout(timer);
    }
  }

  // An ambiguity as the server describes it (policy_prediction.go AmbiguityView).
  const ambiguityProvider = (p) => p && typeof p.name === 'string' ? { name: text(p.name, 100), label: text(p.label, 100), profile: text(p.profile, 128) } : null;
  const ambiguityDiffs = (v) => (Array.isArray(v) ? v : []).filter((d) => d && typeof d.field === 'string').slice(0, 10).map((d) => ({ field: text(d.field, 64), value: text(d.value, 300), withValue: text(d.withValue, 300) }));
  function validAmbiguity(a) {
    const strings = (v, max, len) => (Array.isArray(v) ? v : []).filter((x) => typeof x === 'string').slice(0, max).map((x) => x.slice(0, len));
    const provider = ambiguityProvider;
    return {
      rule: text(a.rule, 200), endpoint: text(a.endpoint, 600), provider: provider(a.provider), with: text(a.with, 200), withEndpoint: text(a.withEndpoint, 600), withProvider: provider(a.withProvider),
      diffs: ambiguityDiffs(a.diffs), details: strings(a.details, 10, 600), raw: text(a.raw, 2000), host: text(a.host, 253), endpointPorts: (Array.isArray(a.endpointPorts) ? a.endpointPorts : []).filter(Number.isInteger).slice(0, 16),
      binaries: strings(a.binaries, 16, 512), audited: a.audited === true, fix: a.fix === 'enforce' ? 'enforce' : '', access: text(a.access, 20),
      allows: (Array.isArray(a.allows) ? a.allows : []).filter((r) => r && typeof r.path === 'string').slice(0, 32).map((r) => ({ method: text(r.method, 10), path: text(r.path, 512) }))
    };
  }

  // The pairs of the gateway's ambiguity refusal (policy_prediction.go
  // GatewayAmbiguity); null when there are none.
  function validGatewayAmbiguities(list) {
    if (!Array.isArray(list)) return null;
    return list.filter((a) => a && typeof a.rule === 'string').slice(0, 20).map((a) => ({
      rule: text(a.rule, 200), provider: ambiguityProvider(a.provider), with: text(a.with, 200), withProvider: ambiguityProvider(a.withProvider), endpoint: text(a.endpoint, 600), diffs: ambiguityDiffs(a.diffs)
    }));
  }

  // An error with the gateway's own words folded away under it.
  function errorContent(node, message, raw) {
    node.textContent = message;
    if (!raw || !message) return;
    const details = element('details', 'error-raw');
    details.append(element('summary', '', t('app.rawMessage')), element('pre', '', raw));
    node.append(details);
  }

  function invalidateSession() {
    state.authorized = false;
    state.csrf = '';
    for (const card of cards.values()) {
      card.disconnect(t('card.authRequired'));
      card.policy.clear(t('app.authRequired'));
    }
    $('pair-button').hidden = true;
    $('log-button').hidden = true;
    $('volume-button').hidden = true;
    $('volume-panel').hidden = true;
    $('provider-button').hidden = true;
    $('provider-panel').hidden = true;
    $('settings-button').hidden = true;
    $('settings-panel').hidden = true;
    $('create-button').hidden = true;
    $('refresh-button').hidden = false;
    closePair();
    $('login-panel').hidden = false;
    serverStatus('server.authRequired', 'error');
    showNotice(t('notice.auth.title'), t('notice.auth.text'));
  }

  async function authenticate(manualToken = null) {
    if (state.authenticating || state.disposed) return;
    state.authenticating = true;
    $('refresh-button').disabled = true;
    let launch = manualToken ? { token: manualToken, invalid: false }
      : window.IwatoLaunch ? window.IwatoLaunch.consume() : { token: null, invalid: false };
    try {
      if (launch.invalid) throw new Error(t('login.badLaunch'));
      const session = launch.token
        ? await api('/api/session', { method: 'POST', body: JSON.stringify({ token: launch.token }) })
        : await api('/api/session');
      if (typeof session.csrf !== 'string' || !session.csrf || session.csrf.length > 1024) {
        throw new Error(t('app.sessionInvalid'));
      }
      launch.token = null;
      state.csrf = session.csrf;
      state.authorized = true;
      $('login-panel').hidden = true;
      clearNotice();
      serverStatus('server.connected', 'ready');
      ownerTools();
      reportPresence(true);
      await refreshSandboxes();
    } catch (error) {
      state.authorized = false;
      $('refresh-button').hidden = false;
      if (error.status === 401 || error.status === 403) $('login-panel').hidden = false;
      serverStatus('server.checkAuth', 'error');
      showNotice(t('login.cannotOpen'), error.status === 401 || error.status === 403
        ? (error.code === 'launch_token_invalid' ? error.message : t('login.authRequired'))
        : error.message);
      bind($('empty-title'), 'empty.waiting.title');
      bind($('empty-text'), 'empty.waiting.text');
      grid.setAttribute('aria-busy', 'false');
    } finally {
      launch.token = null;
      launch = null;
      state.authenticating = false;
      $('refresh-button').disabled = false;
    }
  }

  // One startup check (checks.go) in the page's words: its name, detail
  // and hint are keys with params.
  const checkLevels = ['ok', 'info', 'warn', 'fail'];
  function checkView(c) {
    if (!c || typeof c !== 'object') return null;
    const key = (v) => typeof v === 'string' && /^[a-z0-9_]{1,40}$/.test(v) ? v : '';
    const params = {};
    if (c.params && typeof c.params === 'object') {
      for (const [k, v] of Object.entries(c.params)) if (typeof v === 'string' || typeof v === 'number') params[k] = String(v).slice(0, 500);
    }
    const name = key(c.name), detail = key(c.detail), hint = key(c.hint);
    return {
      level: checkLevels.includes(c.level) ? c.level : 'info',
      name: name ? t('checks.name.' + name, params) : '',
      detail: detail ? t('checks.detail.' + detail, params) : '',
      hint: hint ? t('checks.hint.' + hint, params) : ''
    };
  }

  // The preparation's step or failure: the page's words for its code
  // (images.state.<code>), else the server's.
  function imagesMessage(images) {
    const fallback = typeof images.message === 'string' ? images.message.slice(0, 500) : '';
    const code = typeof images.code === 'string' ? images.code : '';
    if (!/^[a-z0-9_]{1,64}$/.test(code) || !window.IwatoI18n.has('images.state.' + code)) return fallback;
    const p = images.params && typeof images.params === 'object' ? images.params : {};
    const detail = typeof p.detail === 'string' ? window.IwatoI18n.serverError(images.detailCode, images.detailParams, p.detail.slice(0, 1000)) : '';
    const version = typeof p.version === 'string' ? p.version.slice(0, 40) : '';
    const seconds = Number.isInteger(p.seconds) && p.seconds >= 0 ? t('images.seconds', { seconds: p.seconds }) : '';
    return t('images.state.' + code, { detail, version }) + seconds;
  }

  // Images are prepared by serve in the background; starting a sandbox
  // waits until they are ready. Before that, while the engine is unusable,
  // the banner shows its failed check (engine) and checks again on request.
  function renderImages(images, engine) {
    const down = checkView(engine);
    const phase = down ? 'engine' : images && typeof images.phase === 'string' ? images.phase : 'ready';
    const ready = phase === 'ready';
    const changed = state.imagesReady !== ready, prepared = ready && state.imagesReady === false;
    state.imagesReady = ready;
    $('images-banner').hidden = ready;
    $('images-banner').classList.toggle('banner-error', !!down);
    $('images-banner').classList.toggle('banner-note', !down);
    $('images-recheck').hidden = !down;
    $('images-log').hidden = !!down;
    if (down) {
      $('images-title').textContent = t('checks.blocked', { name: down.name });
      $('images-text').textContent = [down.detail, down.hint, t('checks.waiting')].filter(Boolean).join('\n');
      $('images-retry').hidden = true;
    } else if (!ready) {
      const message = imagesMessage(images);
      const log = typeof images.log === 'string' ? images.log.slice(0, 500) : '';
      $('images-title').textContent = phase === 'failed' ? t('images.failed') : t('images.preparing');
      $('images-text').textContent = message + (log ? t('images.log', { log }) : '') + (phase === 'failed' ? '' : t('images.wait'));
      $('images-retry').hidden = phase !== 'failed';
    }
    if (changed) for (const card of cards.values()) card.updateButtons();
    // Once prepared, list the running sessions again, now with those a
    // restart would update.
    if (prepared) for (const card of cards.values()) card.loadTerms();
  }

  // The log view follows Iwato's log (/api/log) while it is open.
  const logView = { next: -1, timer: 0, lines: [] };
  function openLog() {
    $('log-panel').hidden = false;
    $('log-text').focus?.();
    pollLog();
  }
  function closeLog() {
    $('log-panel').hidden = true;
    clearTimeout(logView.timer);
  }
  async function pollLog() {
    clearTimeout(logView.timer);
    if ($('log-panel').hidden || !state.authorized) return;
    try {
      const data = await api('/api/log' + (logView.next >= 0 ? '?after=' + logView.next : ''));
      const lines = Array.isArray(data.lines) ? data.lines.filter((l) => typeof l === 'string') : [];
      if (typeof data.next === 'number') logView.next = data.next;
      if (lines.length) {
        const pre = $('log-text');
        const atBottom = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 40;
        logView.lines = logView.lines.concat(lines).slice(-2000);
        pre.textContent = logView.lines.join('\n');
        if (atBottom) pre.scrollTop = pre.scrollHeight;
      }
    } catch (error) {
      if (error.status === 401 || error.status === 403) { invalidateSession(); return; }
    }
    if (!$('log-panel').hidden) logView.timer = setTimeout(pollLog, 2000);
  }

  // The persisted paths' volumes (/api/volumes), kept ones included, by
  // sandbox. A leftover one can be removed; one in use cannot.
  const volumeView = { list: [] };
  async function loadVolumes() {
    const data = await api('/api/volumes');
    volumeView.list = (Array.isArray(data.volumes) ? data.volumes : []).filter((v) => v && typeof v.name === 'string').slice(0, 500).map((v) => ({
      name: text(v.name, 255), sandbox: text(v.sandbox, 64), path: text(v.path, 4096), usedBy: text(v.usedBy, 64), createdAt: text(v.createdAt, 64)
    }));
    return text(data.error, 500);
  }
  function openVolumes() {
    $('volume-panel').hidden = false;
    refreshVolumes();
  }
  async function refreshVolumes() {
    $('volume-status').textContent = t('nav.loading');
    try {
      const error = await loadVolumes();
      $('volume-status').textContent = error ? t('common.listFailed', { message: error }) : '';
      renderVolumes();
    } catch (error) {
      $('volume-status').textContent = t('common.listFailed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  function renderVolumes() {
    const groups = new Map();
    for (const v of volumeView.list) {
      if (!groups.has(v.sandbox)) groups.set(v.sandbox, []);
      groups.get(v.sandbox).push(v);
    }
    const nodes = [];
    for (const [sandbox, list] of groups) {
      const group = element('section', 'volume-group');
      const head = element('h3', 'volume-sandbox', sandbox || t('volumes.unknownSandbox'));
      if (sandbox && !cards.has(sandbox)) head.append(element('span', 'panel-meta', t('volumes.deleted')));
      group.append(head);
      for (const v of list) {
        const row = element('div', 'volume-row');
        const info = element('div', 'volume-info');
        const created = Date.parse(v.createdAt);
        info.append(element('span', 'volume-path' + (v.path ? '' : ' volume-unknown'), v.path || t('volumes.unknownPath')),
          element('span', 'panel-meta', v.name + (Number.isFinite(created) ? ' · ' + t('volumes.created', { time: window.IwatoI18n.date(created, { dateStyle: 'short', timeStyle: 'short' }) }) : '')));
        row.append(info, v.usedBy ? element('span', 'volume-used', t('volumes.usedBy', { sandbox: v.usedBy })) : button('button-disconnect', t('common.delete'), () => removeVolume(v)));
        group.append(row);
      }
      nodes.push(group);
    }
    if (!nodes.length) nodes.push(element('p', 'panel-meta', t('volumes.none')));
    $('volume-list').replaceChildren(...nodes);
  }
  async function removeVolume(v) {
    if (!state.authorized) return;
    const what = (v.sandbox || t('volumes.unknownSandbox')) + ' ' + (v.path || t('volumes.unknownPath'));
    if (!await askConfirm(t('volumes.delete.confirm', { name: v.name, what }), { ok: t('common.delete'), danger: true })) return;
    $('volume-status').textContent = t('delete.deleting');
    try {
      await api('/api/volumes/' + encodeURIComponent(v.name) + '/delete', { method: 'POST', body: '{}', timeoutMs: 70000 });
      await refreshVolumes();
    } catch (error) {
      $('volume-status').textContent = t('delete.failed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  // OpenShell's providers (/api/providers): the profiles Iwato offers and
  // the providers, never their values. Values are typed into password
  // inputs, sent once and cleared.
  const providerView = { profiles: [], list: [], error: '' };
  const providerNamePattern = /^[a-z][a-z0-9-]{0,62}$/;
  async function loadProviders() {
    const data = await api('/api/providers');
    // Iwato's profiles come in Japanese; the dictionary has other
    // languages (profile.<id>.*). Custom ones show as written.
    const localized = (p, field, value) => p.custom === true ? value : window.IwatoI18n.localized('profile.' + text(p.id, 128) + '.' + field, value);
    providerView.profiles = (Array.isArray(data.profiles) ? data.profiles : []).filter((p) => p && typeof p.id === 'string').slice(0, 80).map((p) => ({
      id: text(p.id, 128), label: localized(p, 'label', text(p.label, 100)), group: text(p.group, 64), note: localized(p, 'note', text(p.note, 500)), hint: localized(p, 'hint', text(p.hint, 500)), oauth: p.oauth === true, oauthFlow: text(p.oauthFlow, 32), guide: text(p.guide, 500),
      scoped: p.scoped === true, custom: p.custom === true,
      defaultName: typeof p.defaultName === 'string' && /^[a-z][a-z0-9-]{0,40}$/.test(p.defaultName) ? p.defaultName : '',
      credentials: p.oauth === true ? [] : (Array.isArray(p.credentials) ? p.credentials : []).filter((c) => c && typeof c.name === 'string').slice(0, 8).map((c) => ({ name: text(c.name, 64), label: localized(p, 'credential.' + text(c.name, 64), text(c.label, 100)) }))
    }));
    providerView.list = (Array.isArray(data.providers) ? data.providers : []).filter((p) => p && typeof p.name === 'string' && providerNamePattern.test(p.name)).slice(0, 200).map((p) => ({
      name: p.name, profile: text(p.profile, 128), createdAt: text(p.createdAt, 64),
      // kind: the profile the page groups it under (a provider with its
      // own profile, providers_scope.go, under the built-in one).
      kind: text(p.base || p.profile, 128), scope: providerScopeView(p.scope),
      keys: (Array.isArray(p.keys) ? p.keys : []).filter((k) => typeof k === 'string').slice(0, 16).map((k) => k.slice(0, 64)),
      attached: (Array.isArray(p.attached) ? p.attached : []).filter((s) => typeof s === 'string').slice(0, 64).map((s) => s.slice(0, 64)),
      applying: (Array.isArray(p.applying) ? p.applying : []).filter((a) => a && typeof a.sandbox === 'string').slice(0, 64).map((a) => ({ sandbox: a.sandbox.slice(0, 64), attach: a.attach === true, scope: a.scope === true, state: text(a.state, 32), reason: text(a.reason, 200) })),
      refresh: p.refresh && typeof p.refresh === 'object' ? {
        status: text(p.refresh.status, 64), recovery: text(p.refresh.recovery, 64), failureCode: text(p.refresh.failureCode, 64), error: text(p.refresh.error, 300),
        expiresAt: text(p.refresh.expiresAt, 64), nextRefreshAt: text(p.refresh.nextRefreshAt, 64), lastRefreshAt: text(p.refresh.lastRefreshAt, 64)
      } : null
    }));
    providerView.error = text(data.error, 500);
    return providerView.error;
  }
  // A provider's access scope (providers_scope.go): read-only or full
  // write, and for GitHub (modes with repos) writes to repositories and
  // GraphQL mutations, for Atlassian (mode tools) read tools plus named
  // tools; custom when the owner edited its profile as YAML.
  function providerScopeView(s) {
    if (!s || typeof s !== 'object') return null;
    const names = (list, n, max = 50) => (Array.isArray(list) ? list : []).filter((x) => typeof x === 'string').slice(0, max).map((x) => x.slice(0, n));
    const modes = (Array.isArray(s.modes) ? s.modes : []).filter((m) => ['read-only', 'repos', 'tools', 'full'].includes(m));
    if (!modes.length) modes.push('read-only', 'repos', 'full');
    return { mode: modes.includes(s.mode) ? s.mode : modes[0], modes, repos: names(s.repos, 160), mutations: names(s.mutations, 100), tools: names(s.tools, 128, 100), readTools: names(s.readTools, 128, 200), custom: s.custom === true, missing: s.missing === true };
  }
  function scopeSummary(s) {
    if (s.custom) return t('providers.scope.custom');
    const github = s.modes.includes('repos');
    const mcp = s.modes.includes('tools');
    const parts = [s.mode === 'full' ? t(github ? 'providers.scope.full' : mcp ? 'providers.scope.allTools' : 'providers.scope.write') : s.mode === 'repos' ? t('providers.scope.repos', { repos: s.repos.join(', ') }) : s.mode === 'tools' ? (s.tools.length ? t('providers.scope.tools', { tools: s.tools.join(', ') }) : t('providers.scope.readTools')) : t('providers.scope.read')];
    if (s.mode !== 'full' && s.mutations.length) parts.push(t('providers.scope.mutations', { mutations: s.mutations.join(', ') }));
    return t('providers.scope', { parts: parts.join(' · ') }) + (s.missing ? t('providers.scope.missing') : '');
  }
  // The name the server gives a provider of the profile when the owner
  // leaves the field empty (providers_names.go): its default, or with the
  // first free -2, -3, … . Another client can take it meanwhile; the
  // server then picks the next and answers with the name.
  function providerDefaultName(profile) {
    const base = profile?.defaultName;
    if (!base) return '';
    const taken = new Set(providerView.list.map((p) => p.name));
    for (let i = 1; i <= 100; i++) {
      const name = i === 1 ? base : base + '-' + i;
      if (!taken.has(name)) return name;
    }
    return '';
  }
  function profileLabel(id) {
    const p = providerView.profiles.find((x) => x.id === id);
    return p ? p.label : id;
  }
  function openProviders() {
    $('provider-panel').hidden = false;
    refreshProviders();
  }
  async function refreshProviders() {
    $('provider-status').textContent = t('nav.loading');
    try {
      const error = await loadProviders();
      $('provider-status').textContent = error ? t('common.listFailed', { message: error }) : '';
      renderProviders();
      renderProviderForm();
    } catch (error) {
      $('provider-status').textContent = t('common.listFailed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  // One password input per credential of the profile; none is ever filled in.
  function valueInputs(profile, holder) {
    const inputs = {};
    holder.replaceChildren(...(profile ? profile.credentials : []).map((c) => {
      const input = element('input', 'text-input');
      input.type = 'password';
      input.autocomplete = 'off';
      input.setAttribute('autocomplete', 'new-password');
      input.setAttribute('spellcheck', 'false');
      input.placeholder = c.label;
      input.setAttribute('aria-label', c.label);
      inputs[c.name] = input;
      return input;
    }));
    return inputs;
  }
  function collectValues(inputs) {
    const values = {};
    for (const [name, input] of Object.entries(inputs)) values[name] = String(input.value || '').trim();
    return values;
  }
  function clearValues(inputs) { for (const input of Object.values(inputs)) input.value = ''; }
  let providerFormInputs = {};
  function renderProviderForm() {
    const select = $('provider-profile');
    const current = select.value;
    // Under the server's headings (agents, source control, model hubs,
    // clouds): consecutive profiles of a group share an optgroup.
    const options = [];
    let group = null;
    for (const p of providerView.profiles) {
      const option = element('option', '', p.label);
      option.value = p.id;
      if (!p.group) { group = null; options.push(option); continue; }
      if (!group || group.dataset.group !== p.group) {
        group = element('optgroup');
        group.dataset.group = p.group;
        group.label = window.IwatoI18n.has('providers.group.' + p.group) ? t('providers.group.' + p.group) : p.group;
        options.push(group);
      }
      group.append(option);
    }
    select.replaceChildren(...options);
    if (providerView.profiles.some((p) => p.id === current)) select.value = current;
    // Preselect GitHub (a value to paste, not an OAuth sign-in).
    else if (providerView.profiles.length) select.value = (providerView.profiles.find((p) => p.id === 'iwato-github') || providerView.profiles.find((p) => !p.oauth) || providerView.profiles[0]).id;
    const profile = providerView.profiles.find((p) => p.id === select.value);
    $('provider-note').textContent = profile ? profile.note : '';
    $('provider-hint').textContent = profile ? profile.hint : '';
    $('provider-hint').hidden = !profile?.hint;
    providerFormInputs = valueInputs(profile, $('provider-values'));
    const generated = providerDefaultName(profile);
    if (generated) window.IwatoI18n.bindAttribute($('provider-name'), 'placeholder', 'providers.name.generated', { name: generated });
    else window.IwatoI18n.bindAttribute($('provider-name'), 'placeholder', 'providers.name.placeholder');
    // OAuth profiles take no value: Iwato asks Atlassian (or xAI) for one.
    window.IwatoI18n.bind($('provider-submit'), profile?.oauth ? connectKey(profile) : 'providers.add');
    window.IwatoI18n.bind($('provider-profile-yaml'), profile?.custom ? 'providers.yaml.edit' : 'providers.yaml.view');
  }
  const connectKey = (profile) => profile?.oauthFlow === 'xai' ? 'providers.connect.xai' : profile?.oauthFlow === 'claude' ? 'providers.connect.claude' : 'providers.connect.atlassian';
  // The gateway's refresh of an OAuth provider, in words.
  const shortTime = (iso) => { const t = Date.parse(iso); return Number.isFinite(t) ? window.IwatoI18n.date(t, { dateStyle: 'short', timeStyle: 'short' }) : ''; };
  function needsReauthorization(r) {
    return !!r && (r.status === 'reauthorization_required' || r.recovery === 'reauthorize' || r.status === 'not_configured');
  }
  function refreshText(r) {
    if (!r) return '';
    const code = r.failureCode ? ' (' + r.failureCode + ')' : '';
    if (needsReauthorization(r)) return t('providers.refresh.reauthorize', { code });
    const at = (key, iso) => shortTime(iso) ? t(key, { time: shortTime(iso) }) : '';
    switch (r.status) {
      case 'refreshed': case 'configured': return t('providers.refresh.ok') + at('providers.refresh.expires', r.expiresAt) + at('providers.refresh.next', r.nextRefreshAt);
      case 'error': return t('providers.refresh.error', { code }) + at('providers.refresh.expires', r.expiresAt);
      case 'configuration_required': return t('providers.refresh.configuration', { code }) + at('providers.refresh.expires', r.expiresAt);
      case 'investigation_required': return t('providers.refresh.investigation', { code }) + at('providers.refresh.expires', r.expiresAt);
      default: return t('providers.refresh.state', { status: r.status || t('providers.refresh.unknown') }) + (r.error ? ' (' + r.error + ')' : '');
    }
  }
  // An attach or detach a running sandbox has not applied yet (OpenShell's
  // supervisor picks it up on its next poll), or could not apply.
  function applyNote(a, withSandbox) {
    const what = (withSandbox ? a.sandbox + ': ' : '') + t(a.scope ? 'providers.change.scope' : a.attach ? 'providers.change.attach' : 'providers.change.detach');
    const failed = a.state === 'failed' || a.state === 'withheld';
    const note = element('span', 'panel-meta provider-applying', failed
      ? t('providers.apply.failed', { what, detail: a.state + (a.reason ? ', ' + a.reason : '') })
      : t('providers.apply.pending', { what }));
    note.dataset.state = failed ? 'failed' : 'pending';
    return note;
  }
  // A command to copy (the sandbox-side guide).
  function guideBox(intro, command) {
    const box = element('div', 'provider-guide');
    const code = element('code', 'provider-guide-command', command);
    const copy = button('button-subtle', t('common.copy'), () => {
      if (typeof navigator !== 'undefined' && navigator.clipboard) navigator.clipboard.writeText(command).then(() => { copy.textContent = t('common.copied'); }, () => {});
    });
    box.append(element('p', 'panel-meta', intro), code, copy);
    return box;
  }

  // A profile as YAML in OpenShell's format (/api/provider-profiles/ID):
  // Iwato's file as shipped, or a profile the gateway holds (imported with
  // OpenShell's CLI) rendered by Iwato. Read-only, with a copy button.
  let profileYAMLRequest = 0;
  const profileCode = {};
  function closeProfileYAML() {
    profileYAMLRequest++;
    keepCode(profileCode, 'code', null);
    $('provider-yaml').hidden = true;
    $('provider-yaml').replaceChildren();
  }
  async function showProfileYAML(id) {
    const box = $('provider-yaml');
    const seq = ++profileYAMLRequest;
    const head = element('div', 'log-head');
    const title = element('strong', 'provider-yaml-title', t('providers.yaml.title', { id }));
    const tools = element('div', 'provider-yaml-tools');
    const close = button('button-subtle', t('common.close'), () => closeProfileYAML());
    tools.append(close);
    head.append(title, tools);
    const status = element('p', 'panel-meta', t('nav.loading'));
    keepCode(profileCode, 'code', null);
    box.replaceChildren(head, status);
    box.hidden = false;
    if (typeof box.scrollIntoView === 'function') box.scrollIntoView({ block: 'nearest' });
    try {
      const data = await api('/api/provider-profiles/' + encodeURIComponent(id));
      if (seq !== profileYAMLRequest) return;
      const yaml = typeof data.yaml === 'string' ? data.yaml.slice(0, 200000) : '';
      const source = ['iwato', 'scope', 'custom'].includes(data.source) ? data.source : 'gateway';
      const tag = element('span', 'tag-source', t('providers.yaml.source.' + source));
      tag.dataset.source = source;
      title.append(tag);
      const view = keepCode(profileCode, 'code', {}, () => ({ value: yaml, readOnly: true, label: t('presets.files.yamlLabel', { file: id }), className: 'provider-yaml-view' }));
      const copy = button('button-subtle', t('common.copy'), () => {
        if (typeof navigator !== 'undefined' && navigator.clipboard) navigator.clipboard.writeText(yaml).then(() => { copy.textContent = t('common.copied'); }, () => { copy.textContent = t('common.copyFailed'); });
      });
      tools.replaceChildren(copy, close);
      box.replaceChildren(head, view.dom);
      view.refresh();
    } catch (error) {
      if (seq !== profileYAMLRequest) return;
      status.textContent = t('providers.yaml.failed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }

  // The scope editor of a provider with its own profile (providers_scope.go):
  // read-only or writes allowed; GitHub's also writes to listed
  // repositories and GraphQL mutations by name, Atlassian's read tools plus
  // write tools by name or every tool. The details are in its profile, as
  // YAML. It names the sandboxes the change reaches.
  function scopeEditor(p) {
    const s = p.scope;
    const github = s.modes.includes('repos');
    const mcp = s.modes.includes('tools');
    const form = element('form', 'stack-form provider-access-form');
    form.hidden = true;
    const modes = element('div', 'provider-access-modes');
    modes.setAttribute('role', 'radiogroup');
    modes.setAttribute('aria-label', t('providers.scope.label', { name: p.name }));
    const radios = {};
    const labels = github ? { 'read-only': t('providers.scope.mode.read'), repos: t('providers.scope.mode.repos'), full: t('providers.scope.mode.full') }
      : mcp ? { tools: t('providers.scope.mode.tools'), full: t('providers.scope.mode.allTools') }
        : { 'read-only': t('providers.scope.mode.readOnly'), full: t('providers.scope.mode.write') };
    for (const mode of s.modes) {
      const label = labels[mode];
      const label_ = element('label', 'provider-access-mode');
      const radio = element('input');
      radio.type = 'radio';
      radio.name = 'access-' + p.name;
      radio.value = mode;
      radio.checked = s.mode === mode;
      radio.disabled = s.custom;
      radios[mode] = radio;
      label_.append(radio, element('span', '', label));
      modes.append(label_);
    }
    const repos = element('textarea', 'text-input provider-access-repos');
    repos.rows = 3;
    repos.value = s.repos.join('\n');
    repos.placeholder = t('providers.scope.repos.placeholder');
    repos.setAttribute('aria-label', t('providers.scope.repos.label'));
    repos.setAttribute('spellcheck', 'false');
    repos.setAttribute('autocapitalize', 'off');
    const mutations = element('input', 'text-input provider-access-mutations');
    mutations.type = 'text';
    mutations.value = s.mutations.join(', ');
    mutations.placeholder = t('providers.scope.mutations.placeholder');
    mutations.setAttribute('aria-label', t('providers.scope.mutations.label'));
    mutations.setAttribute('spellcheck', 'false');
    mutations.setAttribute('autocapitalize', 'off');
    const tools = element('input', 'text-input provider-access-tools');
    tools.type = 'text';
    tools.value = s.tools.join(', ');
    tools.placeholder = t('providers.scope.tools.placeholder');
    tools.setAttribute('aria-label', t('providers.scope.tools.label'));
    tools.setAttribute('spellcheck', 'false');
    tools.setAttribute('autocapitalize', 'off');
    const sync = () => {
      const mode = Object.keys(radios).find((m) => radios[m].checked) || s.modes[0];
      repos.disabled = s.custom || mode !== 'repos';
      mutations.disabled = s.custom || mode === 'full';
      tools.disabled = s.custom || mode !== 'tools';
      return mode;
    };
    for (const radio of Object.values(radios)) radio.addEventListener('change', sync);
    sync();
    const status = element('p', 'panel-meta provider-access-status');
    status.setAttribute('role', 'status');
    const save = element('button', 'button button-primary', t('common.save'));
    save.type = 'submit';
    save.disabled = s.custom;
    const buttons = element('div', 'login-form');
    buttons.append(save, button('button-subtle provider-access-yaml', t('providers.scope.editYaml'), () => editScopeYAML(p)));
    if (s.custom) buttons.append(button('button-subtle provider-access-reset', t('providers.scope.reset'), () => postScope(p, { reset: true }, status)));
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      if (s.custom) return;
      const body = { mode: sync(), repos: String(repos.value || '').split('\n').map((r) => r.trim()).filter(Boolean), mutations: String(mutations.value || '').split(/[\s,]+/).filter(Boolean) };
      if (mcp) body.tools = String(tools.value || '').split(/[\s,]+/).filter(Boolean);
      postScope(p, body, status);
    });
    if (s.custom) form.append(element('p', 'panel-meta provider-access-custom', t('providers.scope.customNote')));
    form.append(modes);
    if (github) {
      form.append(repos,
        element('p', 'panel-meta', t('providers.scope.reposNote')),
        mutations,
        element('p', 'panel-meta provider-access-warning', t('providers.scope.mutationsWarning')),
        element('p', 'panel-meta', t('providers.scope.tokenNote')));
    }
    if (mcp) {
      form.append(tools, element('p', 'panel-meta provider-access-warning', t('providers.scope.toolsWarning')));
      if (s.readTools.length) form.append(element('p', 'panel-meta provider-access-read-tools', t('providers.scope.readToolsList', { tools: s.readTools.join(', ') })));
    }
    form.append(element('p', 'panel-meta provider-access-sandboxes', p.attached.length ? t('providers.scope.appliesTo', { sandboxes: p.attached.join(', ') }) : t('providers.scope.appliesToNone')),
      buttons, status);
    return form;
  }
  async function sendScope(p, body) {
    const result = await api('/api/providers/' + encodeURIComponent(p.name) + '/scope', { method: 'POST', body: JSON.stringify(body), timeoutMs: 70000 });
    const pending = Array.isArray(result?.pending) ? result.pending.filter((x) => typeof x === 'string').slice(0, 16).map((x) => x.slice(0, 64)) : [];
    await refreshProviders();
    $('provider-status').textContent = t('providers.scope.changed', { name: p.name }) + (pending.length ? t('providers.scope.pending', { sandboxes: pending.join(', ') }) : '');
  }
  async function postScope(p, body, status) {
    if (!state.authorized) return;
    status.textContent = t('providers.saving');
    try {
      await sendScope(p, body);
    } catch (error) {
      status.textContent = t('providers.changeFailed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  // A profile in a YAML editor, in the YAML box: save checks it on
  // the server (Iwato's and OpenShell's checks) and shows the refusal here.
  function openYAMLEditor({ title, note, yaml, save, extra = [] }) {
    profileYAMLRequest++;
    const box = $('provider-yaml');
    const head = element('div', 'log-head');
    const tools = element('div', 'provider-yaml-tools');
    tools.append(button('button-subtle', t('common.close'), () => closeProfileYAML()));
    head.append(element('strong', 'provider-yaml-title', title), tools);
    const area = keepCode(profileCode, 'code', {}, () => ({ value: yaml, label: title, className: 'provider-yaml-editor' }));
    const status = element('p', 'panel-meta provider-yaml-status');
    status.setAttribute('role', 'status');
    const submit = button('button-primary provider-yaml-save', t('common.save'), async () => {
      if (!state.authorized) return;
      submit.disabled = true;
      status.textContent = t('providers.saving');
      try {
        await save(String(area.value || ''));
        closeProfileYAML();
      } catch (error) {
        status.textContent = t('providers.saveFailed', { message: error.message });
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        submit.disabled = false;
      }
    });
    const actions = element('div', 'login-form');
    actions.append(submit, ...extra);
    box.replaceChildren(head, element('p', 'panel-meta', note), area.dom, actions, status);
    box.hidden = false;
    area.refresh();
    if (typeof box.scrollIntoView === 'function') box.scrollIntoView({ block: 'nearest' });
  }
  async function editScopeYAML(p) {
    try {
      const data = await api('/api/provider-profiles/' + encodeURIComponent(p.profile));
      openYAMLEditor({
        title: t('providers.yaml.title', { id: p.name }),
        note: t('providers.scope.yamlNote'),
        yaml: typeof data.yaml === 'string' ? data.yaml.slice(0, 200000) : '',
        save: (yaml) => sendScope(p, { yaml })
      });
    } catch (error) {
      $('provider-status').textContent = t('providers.yaml.failed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  // Custom profiles (providers_custom.go): a whole profile as YAML, kept as
  // <config folder>/profiles/ID.yaml.
  const customProfileTemplate = () => [
    '# ' + t('providers.custom.template.comment'),
    'id: my-api',
    'display_name: My API',
    'description: ' + t('providers.custom.template.description'),
    'category: other',
    'credentials:',
    '  - name: api_key',
    '    description: ' + t('providers.custom.template.credential'),
    '    env_vars: [MY_API_KEY]',
    '    required: true',
    '    auth_style: bearer',
    '    header_name: authorization',
    'endpoints:',
    '  - host: api.example.com',
    '    port: 443',
    '    protocol: rest',
    '    access: read-only',
    '    enforcement: enforce',
    'binaries:',
    '  - /usr/bin/curl',
    ''
  ].join('\n');
  async function saveCustomProfile(yaml) {
    const result = await api('/api/provider-profiles', { method: 'POST', body: JSON.stringify({ yaml }), timeoutMs: 70000 });
    const id = typeof result?.id === 'string' ? result.id.slice(0, 128) : '';
    await refreshProviders();
    if (id && providerView.profiles.some((x) => x.id === id)) { $('provider-profile').value = id; renderProviderForm(); }
    $('provider-form-status').textContent = t('providers.custom.saved', { id }) + (result?.file ? ' (' + text(result.file, 300) + ')' : '');
  }
  function addCustomProfile() {
    openYAMLEditor({
      title: t('providers.custom.addTitle'),
      note: t('providers.custom.addNote'),
      yaml: customProfileTemplate(), save: saveCustomProfile
    });
  }
  async function editCustomProfile(id) {
    try {
      const data = await api('/api/provider-profiles/' + encodeURIComponent(id));
      const remove = button('button-disconnect provider-custom-delete', t('providers.custom.delete'), async () => {
        if (!state.authorized) return;
        if (!await askConfirm(t('providers.custom.delete.confirm', { id }), { ok: t('common.delete'), danger: true })) return;
        try {
          await api('/api/provider-profiles/' + encodeURIComponent(id) + '/delete', { method: 'POST', body: '{}' });
          closeProfileYAML();
          await refreshProviders();
          $('provider-form-status').textContent = t('providers.custom.deleted', { id });
        } catch (error) {
          $('provider-form-status').textContent = t('delete.failed', { message: error.message });
          if (error.status === 401 || error.status === 403) invalidateSession();
        }
      });
      openYAMLEditor({
        title: t('providers.yaml.title', { id }),
        note: t('providers.custom.idNote'),
        yaml: typeof data.yaml === 'string' ? data.yaml.slice(0, 200000) : '', save: saveCustomProfile, extra: [remove]
      });
    } catch (error) {
      $('provider-form-status').textContent = t('providers.yaml.failed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }

  // Connecting an Atlassian provider (/api/oauth/atlassian): the scopes,
  // then the authorization in Atlassian's page, which comes back to
  // Iwato's own 127.0.0.1 listener or, from another device, as a pasted
  // URL. The page never sees a token; it polls the flow's state.
  const oauthFlow = { id: '', timer: 0 };
  function oauthBox(title) {
    const box = $('provider-oauth');
    box.hidden = false;
    const close = button('button-subtle', t('common.close'), () => endOAuth());
    const head = element('div', 'log-head');
    head.append(element('strong', '', title), close);
    box.replaceChildren(head);
    if (typeof box.scrollIntoView === 'function') box.scrollIntoView({ block: 'nearest' });
    return box;
  }
  function endOAuth() {
    if (oauthFlow.timer) clearTimeout(oauthFlow.timer);
    const id = oauthFlow.id;
    const base = oauthFlow.xai ? '/api/oauth/xai' : oauthFlow.claude ? '/api/oauth/claude' : '/api/oauth/atlassian';
    oauthFlow.id = ''; oauthFlow.timer = 0;
    $('provider-oauth').hidden = true;
    $('provider-oauth').replaceChildren();
    if (id && oauthFlow.waiting) api(base + '/flows/' + encodeURIComponent(id) + '/cancel', { method: 'POST', body: '{}' }).catch(() => {});
    oauthFlow.waiting = false;
    oauthFlow.xai = false;
    oauthFlow.claude = false;
  }
  async function beginOAuth(name, reconnect) {
    if (!state.authorized) return;
    endOAuth();
    const title = reconnect ? t('providers.oauth.reconnectTitle', { name }) : name ? t('providers.connect.atlassianNamed', { name }) : t('providers.connect.atlassian');
    let box = oauthBox(title);
    const status = element('p', 'panel-meta', t('providers.oauth.atlassian.loading'));
    status.setAttribute('role', 'status');
    box.append(status);
    let data;
    try {
      data = await api('/api/oauth/atlassian', { timeoutMs: 40000 });
    } catch (error) {
      status.textContent = t('providers.oauth.startFailed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
      return;
    }
    const scopes = (Array.isArray(data.scopes) ? data.scopes : []).filter((x) => x && typeof x.name === 'string').slice(0, 200).map((x) => ({ name: text(x.name, 128), checked: x.default === true }));
    box = oauthBox(title);
    const form = element('form', 'stack-form provider-oauth-form');
    const list = element('div', 'provider-scopes');
    const inputs = scopes.map((x) => {
      const label = element('label', 'provider-scope');
      const input = element('input', '');
      input.type = 'checkbox';
      input.checked = x.checked;
      input.value = x.name;
      // The refresh needs offline_access.
      if (x.name === 'offline_access') input.disabled = true;
      label.append(input, element('span', '', x.name));
      list.append(label);
      return input;
    });
    const extra = element('input', 'text-input');
    extra.type = 'text';
    extra.placeholder = t('providers.oauth.extraScopes.placeholder');
    extra.setAttribute('aria-label', t('providers.oauth.extraScopes'));
    extra.setAttribute('autocapitalize', 'off');
    extra.setAttribute('spellcheck', 'false');
    const go = element('button', 'button button-primary', t('providers.oauth.request'));
    go.type = 'submit';
    const actions = element('div', 'login-form');
    actions.append(go);
    const status2 = element('p', 'panel-meta', '');
    status2.setAttribute('role', 'status');
    form.append(list, extra, actions, status2);
    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      const chosen = inputs.filter((i) => i.checked).map((i) => i.value).concat(String(extra.value || '').split(/\s+/).filter(Boolean));
      go.disabled = true;
      status2.textContent = t('providers.oauth.registering');
      try {
        const flow = await api('/api/oauth/atlassian/start', { method: 'POST', body: JSON.stringify({ name, scopes: chosen, reconnect }), timeoutMs: 60000 });
        oauthFlow.id = text(flow.id, 64);
        renderFlow(flow, title);
      } catch (error) {
        status2.textContent = t('providers.oauth.startFailed', { message: error.message });
        go.disabled = false;
        if (error.status === 401 || error.status === 403) invalidateSession();
      }
    });
    box.append(form);
  }
  // A flow's message from the server, in the page's language when coded.
  const flowMessage = (flow) => window.IwatoI18n.serverError(flow.messageCode, flow.messageParams, text(flow.message, 300));
  // Only Atlassian's https page (or the fake's own) is offered to open.
  function authorizeLink(url) {
    url = text(url, 8192);
    return /^https:\/\/[^\s]+$/.test(url) || (url.startsWith(location.origin + '/fake/atlassian/') && !/\s/.test(url)) ? url : '';
  }
  function renderFlow(flow, title) {
    if (!oauthFlow.id || flow.id !== oauthFlow.id) return;
    const status = text(flow.status, 32);
    // A provider named by Iwato: the name is known once it is created.
    const named = typeof flow.name === 'string' && providerNamePattern.test(flow.name) ? flow.name : '';
    if (named && !flow.reconnect) title = t('providers.connect.atlassianNamed', { name: named });
    oauthFlow.waiting = status === 'waiting';
    const box = oauthBox(title);
    const message = element('p', 'panel-meta provider-oauth-status', '');
    message.setAttribute('role', 'status');
    if (status === 'waiting') {
      const url = authorizeLink(flow.authorizeURL);
      const redirect = text(flow.redirectURI, 100);
      message.textContent = t('providers.oauth.atlassian.waiting');
      const open = element('a', 'button button-primary provider-authorize', t('providers.oauth.atlassian.open'));
      open.href = url;
      open.target = '_blank';
      open.rel = 'noopener noreferrer';
      const copy = button('button-subtle', t('providers.oauth.copyURL'), () => { if (typeof navigator !== 'undefined' && navigator.clipboard) navigator.clipboard.writeText(url).then(() => { copy.textContent = t('common.copied'); }, () => {}); });
      const links = element('div', 'login-form');
      links.append(open, copy);
      const paste = element('form', 'login-form provider-paste');
      const input = element('input', 'text-input');
      input.type = 'url';
      input.placeholder = redirect + '?code=…';
      input.setAttribute('aria-label', t('providers.oauth.redirect'));
      input.setAttribute('autocapitalize', 'off');
      input.setAttribute('spellcheck', 'false');
      const send = element('button', 'button button-subtle', t('providers.oauth.send'));
      send.type = 'submit';
      paste.append(input, send);
      paste.addEventListener('submit', async (event) => {
        event.preventDefault();
        const value = String(input.value || '').trim();
        input.value = '';
        if (!value) return;
        try {
          renderFlow(await api('/api/oauth/atlassian/flows/' + encodeURIComponent(oauthFlow.id) + '/callback', { method: 'POST', body: JSON.stringify({ url: value }) }), title);
        } catch (error) {
          pasteStatus.textContent = t('providers.oauth.pasteFailed', { message: error.message });
        }
      });
      const pasteStatus = element('p', 'panel-meta', '');
      pasteStatus.setAttribute('role', 'status');
      const cancel = button('button-subtle', t('providers.oauth.cancel'), () => endOAuth());
      box.append(message, links, element('p', 'panel-meta', t('providers.oauth.pasteNote', { redirect })), paste, pasteStatus, cancel);
    } else if (status === 'exchanging') {
      message.textContent = t('providers.oauth.exchanging');
      box.append(message);
    } else if (status === 'done') {
      message.textContent = flowMessage(flow) + (flow.refresh ? refreshText({ status: text(flow.refresh.status, 64), recovery: text(flow.refresh.recovery, 64), failureCode: text(flow.refresh.failureCode, 64), expiresAt: text(flow.refresh.expiresAt, 64), nextRefreshAt: text(flow.refresh.nextRefreshAt, 64) }) : '');
      box.append(message);
      if (flow.warning) box.append(element('p', 'panel-meta provider-warning', window.IwatoI18n.serverError(flow.warningCode, flow.warningParams, text(flow.warning, 500))));
      box.append(guideBox(t('providers.oauth.guideAfter'), text(flow.guide, 500)));
      refreshProviders();
    } else {
      message.textContent = flowMessage(flow) || t('providers.oauth.ended');
      box.append(message);
    }
    if (oauthFlow.timer) clearTimeout(oauthFlow.timer);
    oauthFlow.timer = 0;
    if (status === 'waiting' || status === 'exchanging') {
      const id = oauthFlow.id;
      oauthFlow.timer = setTimeout(async () => {
        oauthFlow.timer = 0;
        if (oauthFlow.id !== id) return;
        try {
          renderFlow(await api('/api/oauth/atlassian/flows/' + encodeURIComponent(id)), title);
        } catch (error) {
          message.textContent = t('providers.oauth.stateFailed', { message: error.message });
        }
      }, 1500);
    }
  }
  // Connecting a Grok Build subscription (/api/oauth/xai): Iwato asks
  // auth.x.ai for a device code; the owner opens xAI's page (on this or any
  // device), enters or confirms the code and approves, while Iwato polls.
  // The page shows the code and polls the flow's state, never a token.
  async function beginXAI(name, reconnect) {
    if (!state.authorized) return;
    endOAuth();
    const title = reconnect ? t('providers.oauth.reconnectTitle', { name }) : name ? t('providers.connect.xaiNamed', { name }) : t('providers.connect.xai');
    const box = oauthBox(title);
    const status = element('p', 'panel-meta', t('providers.oauth.xai.loading'));
    status.setAttribute('role', 'status');
    box.append(status);
    try {
      const flow = await api('/api/oauth/xai/start', { method: 'POST', body: JSON.stringify({ name, reconnect }), timeoutMs: 60000 });
      oauthFlow.id = text(flow.id, 64);
      oauthFlow.xai = true;
      renderXAIFlow(flow, title);
    } catch (error) {
      status.textContent = t('providers.oauth.startFailed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  // Only xAI's https page (or the fake's own) is offered to open.
  function verificationLink(url) {
    url = text(url, 2048);
    return /^https:\/\/[^\s]+$/.test(url) || (url.startsWith(location.origin + '/fake/xai/') && !/\s/.test(url)) ? url : '';
  }
  function renderXAIFlow(flow, title) {
    if (!oauthFlow.id || flow.id !== oauthFlow.id) return;
    const status = text(flow.status, 32);
    const named = typeof flow.name === 'string' && providerNamePattern.test(flow.name) ? flow.name : '';
    if (named && !flow.reconnect) title = t('providers.connect.xaiNamed', { name: named });
    oauthFlow.waiting = status === 'waiting';
    const box = oauthBox(title);
    const message = element('p', 'panel-meta provider-oauth-status', '');
    message.setAttribute('role', 'status');
    if (status === 'waiting') {
      const code = /^[A-Za-z0-9-]{1,32}$/.test(String(flow.userCode || '')) ? flow.userCode : '';
      const url = verificationLink(flow.verificationURIComplete) || verificationLink(flow.verificationURI);
      message.textContent = t('providers.oauth.xai.waiting');
      const shown = element('p', 'provider-user-code', code);
      shown.setAttribute('aria-label', t('providers.oauth.xai.code'));
      const open = element('a', 'button button-primary provider-authorize', t('providers.oauth.xai.open'));
      open.href = url;
      open.target = '_blank';
      open.rel = 'noopener noreferrer';
      const copy = button('button-subtle', t('providers.oauth.xai.copyCode'), () => { if (typeof navigator !== 'undefined' && navigator.clipboard) navigator.clipboard.writeText(code).then(() => { copy.textContent = t('common.copied'); }, () => {}); });
      const links = element('div', 'login-form');
      links.append(open, copy);
      const where = verificationLink(flow.verificationURI);
      const cancel = button('button-subtle', t('providers.oauth.cancel'), () => endOAuth());
      box.append(message, shown, links, element('p', 'panel-meta', (where ? t('providers.oauth.xai.where', { url: where }) : '') + t('providers.oauth.xai.expires', { time: shortTime(text(flow.expiresAt, 64)) || t('providers.oauth.xai.soon') })), cancel);
    } else if (status === 'exchanging') {
      message.textContent = t('providers.oauth.exchanging');
      box.append(message);
    } else if (status === 'done') {
      message.textContent = flowMessage(flow) + (flow.refresh ? refreshText({ status: text(flow.refresh.status, 64), recovery: text(flow.refresh.recovery, 64), failureCode: text(flow.refresh.failureCode, 64), expiresAt: text(flow.refresh.expiresAt, 64), nextRefreshAt: text(flow.refresh.nextRefreshAt, 64) }) : '');
      box.append(message);
      if (flow.warning) box.append(element('p', 'panel-meta provider-warning', window.IwatoI18n.serverError(flow.warningCode, flow.warningParams, text(flow.warning, 500))));
      const hint = providerView.profiles.find((p) => p.oauthFlow === 'xai')?.hint;
      if (hint) box.append(element('p', 'panel-meta', hint));
      refreshProviders();
    } else {
      message.textContent = flowMessage(flow) || t('providers.oauth.ended');
      box.append(message);
    }
    if (oauthFlow.timer) clearTimeout(oauthFlow.timer);
    oauthFlow.timer = 0;
    if (status === 'waiting' || status === 'exchanging') {
      const id = oauthFlow.id;
      oauthFlow.timer = setTimeout(async () => {
        oauthFlow.timer = 0;
        if (oauthFlow.id !== id) return;
        try {
          renderXAIFlow(await api('/api/oauth/xai/flows/' + encodeURIComponent(id)), title);
        } catch (error) {
          message.textContent = t('providers.oauth.stateFailed', { message: error.message });
        }
      }, 2000);
    }
  }
  // Signing in to Claude Code (/api/oauth/claude) with Claude Code's own
  // client: on the PC Claude's page comes back to Iwato's loopback
  // listener; from a phone the second link ends on Claude's page, which
  // shows a code to paste here. The page polls the flow, never a token.
  async function beginClaude(name, reconnect) {
    if (!state.authorized) return;
    endOAuth();
    const title = reconnect ? t('providers.oauth.reconnectTitle', { name }) : name ? t('providers.connect.claudeNamed', { name }) : t('providers.connect.claude');
    const box = oauthBox(title);
    const status = element('p', 'panel-meta', t('providers.oauth.claude.loading'));
    status.setAttribute('role', 'status');
    box.append(status);
    try {
      const flow = await api('/api/oauth/claude/start', { method: 'POST', body: JSON.stringify({ name, reconnect }), timeoutMs: 60000 });
      oauthFlow.id = text(flow.id, 64);
      oauthFlow.claude = true;
      renderClaudeFlow(flow, title);
    } catch (error) {
      status.textContent = t('providers.oauth.startFailed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  // Only Claude's https pages (or the fake's own) are offered to open.
  function claudeLink(url) {
    url = text(url, 4096);
    return /^https:\/\/[^\s]+$/.test(url) || (url.startsWith(location.origin + '/fake/claude/') && !/\s/.test(url)) ? url : '';
  }
  function renderClaudeFlow(flow, title) {
    if (!oauthFlow.id || flow.id !== oauthFlow.id) return;
    const status = text(flow.status, 32);
    const named = typeof flow.name === 'string' && providerNamePattern.test(flow.name) ? flow.name : '';
    if (named && !flow.reconnect) title = t('providers.connect.claudeNamed', { name: named });
    oauthFlow.waiting = status === 'waiting';
    const box = oauthBox(title);
    const message = element('p', 'panel-meta provider-oauth-status', '');
    message.setAttribute('role', 'status');
    const link = (url, key, cls) => {
      const a = element('a', 'button ' + cls + ' provider-authorize', t(key));
      a.href = url;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      return a;
    };
    if (status === 'waiting') {
      message.textContent = t('providers.oauth.claude.waiting');
      const links = element('div', 'login-form');
      links.append(link(claudeLink(flow.loopbackURL), 'providers.oauth.claude.open', 'button-primary'), link(claudeLink(flow.manualURL), 'providers.oauth.claude.openManual', 'button-subtle'));
      const paste = element('form', 'login-form provider-paste');
      const input = element('input', 'text-input');
      input.type = 'text';
      input.placeholder = t('providers.oauth.claude.codePlaceholder');
      input.setAttribute('aria-label', t('providers.oauth.claude.code'));
      input.setAttribute('autocapitalize', 'off');
      input.setAttribute('autocomplete', 'off');
      input.setAttribute('spellcheck', 'false');
      const send = element('button', 'button button-subtle', t('providers.oauth.send'));
      send.type = 'submit';
      paste.append(input, send);
      const pasteStatus = element('p', 'panel-meta', '');
      pasteStatus.setAttribute('role', 'status');
      paste.addEventListener('submit', async (event) => {
        event.preventDefault();
        const value = String(input.value || '').trim();
        input.value = '';
        if (!value) return;
        try {
          renderClaudeFlow(await api('/api/oauth/claude/flows/' + encodeURIComponent(oauthFlow.id) + '/code', { method: 'POST', body: JSON.stringify({ code: value }) }), title);
        } catch (error) {
          pasteStatus.textContent = t('providers.oauth.pasteFailed', { message: error.message });
        }
      });
      const cancel = button('button-subtle', t('providers.oauth.cancel'), () => endOAuth());
      box.append(message, links, element('p', 'panel-meta', t('providers.oauth.claude.pasteNote')), paste, pasteStatus, cancel);
    } else if (status === 'exchanging') {
      message.textContent = t('providers.oauth.exchanging');
      box.append(message);
    } else if (status === 'done') {
      message.textContent = flowMessage(flow) + (flow.refresh ? refreshText({ status: text(flow.refresh.status, 64), recovery: text(flow.refresh.recovery, 64), failureCode: text(flow.refresh.failureCode, 64), expiresAt: text(flow.refresh.expiresAt, 64), nextRefreshAt: text(flow.refresh.nextRefreshAt, 64) }) : '');
      box.append(message);
      if (flow.warning) box.append(element('p', 'panel-meta provider-warning', window.IwatoI18n.serverError(flow.warningCode, flow.warningParams, text(flow.warning, 500))));
      const hint = providerView.profiles.find((p) => p.oauthFlow === 'claude')?.hint;
      if (hint) box.append(element('p', 'panel-meta', hint));
      refreshProviders();
    } else {
      message.textContent = flowMessage(flow) || t('providers.oauth.ended');
      box.append(message);
    }
    if (oauthFlow.timer) clearTimeout(oauthFlow.timer);
    oauthFlow.timer = 0;
    if (status === 'waiting' || status === 'exchanging') {
      const id = oauthFlow.id;
      oauthFlow.timer = setTimeout(async () => {
        oauthFlow.timer = 0;
        if (oauthFlow.id !== id) return;
        try {
          renderClaudeFlow(await api('/api/oauth/claude/flows/' + encodeURIComponent(id)), title);
        } catch (error) {
          message.textContent = t('providers.oauth.stateFailed', { message: error.message });
        }
      }, 1500);
    }
  }
  function renderProviders() {
    const nodes = [];
    for (const profile of providerView.profiles.concat([{ id: '', label: t('providers.other') }])) {
      const list = providerView.list.filter((p) => profile.id ? p.kind === profile.id : !providerView.profiles.some((x) => x.id === p.kind));
      if (!list.length) continue;
      const group = element('section', 'volume-group');
      const head = element('h3', 'volume-sandbox', profile.label);
      group.append(head);
      if (profile.guide) group.append(guideBox(t('providers.guide'), profile.guide));
      for (const p of list) {
        const row = element('div', 'volume-row provider-row');
        const info = element('div', 'volume-info');
        info.append(element('span', 'volume-path', p.name),
          element('span', 'panel-meta', (profile.id ? '' : p.profile + ' · ') + (p.keys.join(', ') || t('providers.noValues')) + ' · ' + (p.attached.length ? t('providers.attachedTo', { sandboxes: p.attached.join(', ') }) : t('providers.attachedNone'))));
        if (p.scope) info.append(element('span', 'panel-meta provider-access-summary', scopeSummary(p.scope)));
        else if (profile.scoped) info.append(element('span', 'panel-meta provider-access-summary', t('providers.scope.legacy')));
        for (const a of p.applying) info.append(applyNote(a, true));
        if (profile.oauth) {
          const r = element('span', 'panel-meta provider-refresh', refreshText(p.refresh));
          r.dataset.reauthorize = String(needsReauthorization(p.refresh));
          info.append(r);
        }
        const actions = element('div', 'provider-actions');
        if (profile.oauth) {
          if (!needsReauthorization(p.refresh)) actions.append(button('button-subtle', t('providers.rotate'), () => rotateProvider(p)));
          actions.append(button(needsReauthorization(p.refresh) ? 'button-primary' : 'button-subtle', t('nav.reconnect'), () => profile.oauthFlow === 'xai' ? beginXAI(p.name, true) : profile.oauthFlow === 'claude' ? beginClaude(p.name, true) : beginOAuth(p.name, true)));
        }
        const edit = element('form', 'login-form provider-edit');
        edit.hidden = true;
        const known = providerView.profiles.find((x) => x.id === p.kind);
        const inputs = valueInputs(known, edit);
        const save = element('button', 'button button-primary', t('common.save'));
        save.type = 'submit';
        edit.append(save);
        edit.addEventListener('submit', (event) => { event.preventDefault(); updateProvider(p, inputs, edit); });
        if (known && !known.oauth) actions.append(button('button-subtle', t('providers.changeValue'), () => { edit.hidden = !edit.hidden; if (edit.hidden) clearValues(inputs); }));
        const access = p.scope ? scopeEditor(p) : null;
        if (access) actions.append(button('button-subtle provider-access-button', t('providers.changeScope'), () => { access.hidden = !access.hidden; }));
        const remove = button('button-disconnect', t('common.delete'), () => deleteProvider(p));
        remove.disabled = p.attached.length > 0;
        if (remove.disabled) remove.title = t('providers.detachFirst');
        actions.append(remove);
        actions.append(button('button-subtle provider-yaml-button', t('providers.yaml.view'), () => showProfileYAML(p.profile)));
        row.append(info, actions);
        group.append(row, edit);
        if (access) group.append(access);
      }
      nodes.push(group);
    }
    if (!nodes.length) nodes.push(element('p', 'panel-meta', t('providers.none')));
    $('provider-list').replaceChildren(...nodes);
  }
  async function createProvider() {
    if (!state.authorized) return;
    const name = String($('provider-name').value || '').trim();
    // Empty: the server names it (providers_names.go).
    if (name && !providerNamePattern.test(name)) { $('provider-form-status').textContent = t('error.name_invalid', { max: 63 }); return; }
    const oauthProfile = providerView.profiles.find((p) => p.id === $('provider-profile').value);
    if (oauthProfile?.oauth) {
      if (name && providerView.list.some((p) => p.name === name)) { $('provider-form-status').textContent = t('providers.exists', { name }); return; }
      $('provider-form-status').textContent = '';
      $('provider-name').value = '';
      if (oauthProfile.oauthFlow === 'xai') beginXAI(name, false);
      else if (oauthProfile.oauthFlow === 'claude') beginClaude(name, false);
      else beginOAuth(name, false);
      return;
    }
    const values = collectValues(providerFormInputs);
    clearValues(providerFormInputs);
    if (Object.values(values).some((v) => !v)) { $('provider-form-status').textContent = t('providers.valueMissing'); return; }
    $('provider-submit').disabled = true;
    $('provider-form-status').textContent = t('providers.adding');
    try {
      const result = await api('/api/providers', { method: 'POST', body: JSON.stringify({ name, profile: $('provider-profile').value, values }) });
      const created = typeof result?.name === 'string' && providerNamePattern.test(result.name) ? result.name : name;
      $('provider-name').value = '';
      $('provider-form-status').textContent = created ? t('providers.addedNamed', { name: created }) : t('policy.added');
      await refreshProviders();
    } catch (error) {
      $('provider-form-status').textContent = t('providers.addFailed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    } finally {
      $('provider-submit').disabled = false;
    }
  }
  async function updateProvider(p, inputs, form) {
    if (!state.authorized) return;
    const values = collectValues(inputs);
    clearValues(inputs);
    if (Object.values(values).some((v) => !v)) { $('provider-status').textContent = t('providers.valueMissing'); return; }
    $('provider-status').textContent = t('providers.changing');
    try {
      await api('/api/providers/' + encodeURIComponent(p.name) + '/update', { method: 'POST', body: JSON.stringify({ values }) });
      form.hidden = true;
      $('provider-status').textContent = t('providers.valueChanged', { name: p.name }) + (p.attached.length ? t('providers.valueChanged.running') : '');
    } catch (error) {
      $('provider-status').textContent = t('providers.changeFailed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  // The gateway refreshes now: checks the grant (and Atlassian's rotation).
  async function rotateProvider(p) {
    if (!state.authorized) return;
    $('provider-status').textContent = t('providers.rotating', { name: p.name });
    try {
      await api('/api/providers/' + encodeURIComponent(p.name) + '/rotate', { method: 'POST', body: '{}', timeoutMs: 60000 });
      await refreshProviders();
      $('provider-status').textContent = t('providers.rotated', { name: p.name });
    } catch (error) {
      await refreshProviders();
      $('provider-status').textContent = t('providers.rotateFailed', { name: p.name, message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  async function deleteProvider(p) {
    if (!state.authorized) return;
    if (!await askConfirm(t('providers.delete.confirm', { name: p.name, profile: profileLabel(p.kind) }), { ok: t('common.delete'), danger: true })) return;
    $('provider-status').textContent = t('delete.deleting');
    try {
      await api('/api/providers/' + encodeURIComponent(p.name) + '/delete', { method: 'POST', body: '{}' });
      await refreshProviders();
    } catch (error) {
      $('provider-status').textContent = t('delete.failed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    }
  }
  // The create form's picker: per profile at most one provider (they set
  // the same variables), so a select each.
  function renderCreateProviders() {
    const holder = $('create-providers');
    const groups = providerView.profiles.map((profile) => ({ profile, list: providerView.list.filter((p) => p.kind === profile.id) })).filter((g) => g.list.length);
    holder.hidden = !groups.length;
    holder.replaceChildren(...(groups.length ? [element('p', 'panel-meta', t('create.providers'))] : []), ...groups.map(({ profile, list }) => {
      const label = element('label', 'create-provider');
      const select = element('select', 'text-input');
      select.dataset.profile = profile.id;
      select.setAttribute('aria-label', profile.label);
      const none = element('option', '', t('providers.none.option'));
      none.value = '';
      select.append(none, ...list.map((p) => { const o = element('option', '', p.name); o.value = p.name; return o; }));
      label.append(element('span', '', profile.label), select);
      return label;
    }));
  }
  // Network presets (presets.go): rule sets for the policy, Iwato's and
  // the owner's (YAML files in a folder next to iwato.json). The create
  // form puts them into the first policy; the policy tab into a sandbox's
  // later.
  let presetView = { presets: [], errors: [], folder: '' };
  // A new owner's preset starts from this.
  const presetTemplate = () => [
    'id: my-preset',
    'label: ' + t('presets.template.label'),
    'note: ' + t('presets.template.note'),
    'network_policies:',
    '  allow_example_com_443:',
    '    endpoints:',
    '      - { host: example.com, port: 443, protocol: rest, enforcement: enforce, access: read-only }',
    '    binaries: [/usr/bin/curl]',
    ''].join('\n');
  const builtinText = (p, field, value) => p.source === 'user' ? value : window.IwatoI18n.localized('preset.' + text(p.id, 60) + '.' + field, value);
  async function loadPresets() {
    const data = await api('/api/presets');
    const list = (value, max) => Array.isArray(value) ? value.slice(0, max) : [];
    presetView = {
      presets: list(data?.presets, 100).filter((p) => p && typeof p.id === 'string' && p.id).map((p) => ({
        // Iwato's presets come in Japanese; the dictionary has other languages.
        id: text(p.id, 60), label: builtinText(p, 'label', text(p.label, 200)), note: builtinText(p, 'note', text(p.note, 2000)), source: p.source === 'user' ? 'user' : 'builtin', file: text(p.file, 200),
        hosts: list(p.hosts, 60).filter((h) => typeof h === 'string').map((h) => text(h, 253)),
        providerHosts: list(p.providerHosts, 20).filter((h) => typeof h === 'string').map((h) => text(h, 300)),
        params: list(p.params, 4).filter((x) => x && typeof x.name === 'string').map((x) => ({ name: text(x.name, 20), label: text(x.label, 200), placeholder: text(x.placeholder, 100) }))
      })),
      errors: list(data?.errors, 50).filter((e) => e && typeof e.file === 'string').map((e) => ({ file: text(e.file, 300), error: text(e.error, 1000), code: e.code, params: e.params })),
      folder: text(data?.folder, 1000)
    };
  }
  // A preset's parameter inputs (one text box each).
  function presetParamInputs(preset) {
    return preset.params.map((x) => {
      const input = element('input', 'text-input preset-param');
      input.type = 'text';
      input.autocomplete = 'off';
      input.spellcheck = false;
      input.setAttribute('autocapitalize', 'off');
      input.maxLength = 200;
      input.placeholder = x.placeholder || x.label;
      input.setAttribute('aria-label', preset.label + ': ' + x.label);
      input.dataset.param = x.name;
      return input;
    });
  }
  // The preset with the values of its inputs: an ID, or {id, params}.
  function presetRef(preset, inputs) {
    if (!preset.params.length) return preset.id;
    const params = {};
    for (const input of inputs) params[input.dataset.param] = String(input.value || '').trim();
    return { id: preset.id, params };
  }
  function renderCreatePresets() {
    const holder = $('create-presets');
    const list = presetView.presets;
    holder.hidden = !list.length;
    holder.choices = [];
    holder.replaceChildren(...(list.length ? [element('p', 'panel-meta', t('create.presets'))] : []), ...list.map((preset) => {
      const row = element('div', 'create-preset');
      const label = element('label', 'create-provider');
      const box = document.createElement('input');
      box.type = 'checkbox';
      box.dataset.preset = preset.id;
      label.append(box, element('span', '', preset.label + (preset.source === 'user' ? t('presets.mine') : '')), element('span', 'panel-meta', preset.note));
      const inputs = presetParamInputs(preset);
      row.append(label, ...inputs);
      holder.choices.push({ box, ref: () => presetRef(preset, inputs) });
      return row;
    }));
  }
  function createPresetChoice() {
    return ($('create-presets').choices || []).filter((c) => c.box.checked).map((c) => c.ref());
  }
  function createProviderChoice() {
    const out = [];
    const walk = (node) => { if (node.tagName === 'select' || node.tagName === 'SELECT') { if (node.value) out.push(node.value); } (node.children ? [...node.children] : []).forEach(walk); };
    walk($('create-providers'));
    return out;
  }

  // Creating a sandbox under a name that left data behind picks up its
  // home; its other volumes stay unused: say so.
  function createHint() {
    const name = String($('create-name').value || '').trim();
    const left = volumeView.list.filter((v) => name && v.sandbox === name && !v.usedBy);
    const home = left.some((v) => v.path === '/home/dev');
    const others = left.filter((v) => v.path && v.path !== '/home/dev').map((v) => v.path);
    const unknown = left.filter((v) => !v.path).length;
    if (unknown) others.push(t('create.unknownPaths', { count: unknown }));
    const parts = [];
    if (home) parts.push(t('create.keepsHome'));
    if (others.length) parts.push(t('create.leavesOthers', { paths: others.join(', ') }));
    $('create-volumes').textContent = parts.length ? t('create.leftData', { name, parts: parts.join('') }) : '';
  }

  // 「再確認」: the checks run again; once the engine passes, the
  // preparation starts and the banner follows it.
  async function recheckEngine() {
    $('images-recheck').disabled = true;
    try { await api('/api/checks'); await refreshSandboxes(); } catch (error) {
      showNotice(t('notice.retryFailed'), error.message);
    } finally { $('images-recheck').disabled = false; }
  }

  async function retryImages() {
    $('images-retry').disabled = true;
    try { await api('/api/images/retry', { method: 'POST', body: '{}' }); await refreshSandboxes(); } catch (error) {
      showNotice(t('notice.retryFailed'), error.message);
    } finally { $('images-retry').disabled = false; }
  }

  // Accepts a pasted launch/pairing URL or a bare token.
  function parseToken(value) {
    const match = String(value || '').trim().match(/(?:#token=|^)([a-fA-F0-9]{64})$/);
    return match ? match[1].toLowerCase() : '';
  }

  function ownerTools() {
    $('create-button').hidden = false;
    $('pair-button').hidden = false;
    $('pair-button').disabled = false;
    $('log-button').hidden = false;
    $('volume-button').hidden = false;
    $('provider-button').hidden = false;
    $('settings-button').hidden = false;
    if (!state.pushChecked) {
      state.pushChecked = true;
      setupPush().catch(() => { $('push-button').hidden = true; });
    }
  }

  async function pairDevice() {
    if (!state.authorized) return;
    $('pair-button').disabled = true;
    try {
      const result = await api('/api/pair', { method: 'POST', body: '{}' });
      const url = typeof result.url === 'string' ? result.url.slice(0, 2048) : '';
      $('pair-url').textContent = url;
      // Only an inline PNG from our own server is shown (CSP: img-src data:).
      const qr = typeof result.qr === 'string' && /^data:image\/png;base64,[A-Za-z0-9+\/=]{1,200000}$/.test(result.qr) ? result.qr : '';
      if (qr) $('pair-qr').src = qr; else $('pair-qr').removeAttribute('src');
      $('pair-qr').hidden = !qr;
      $('pair-panel').hidden = !url;
    } catch (error) {
      showNotice(t('notice.pairFailed'), error.message);
      if (error.status === 401 || error.status === 403) invalidateSession();
    } finally {
      $('pair-button').disabled = false;
    }
  }

  function openCreate() {
    $('create-panel').hidden = false;
    $('create-status').textContent = '';
    $('create-name').focus?.();
    if (state.authorized) {
      loadVolumes().then(createHint, () => {});
      loadProviders().then(renderCreateProviders, () => {});
      loadPresets().then(renderCreatePresets, () => {});
    }
  }

  // 「GPU を使う」 only where a sandbox can get a GPU (not on a Docker
  // engine without an NVIDIA CDI device, such as macOS).
  function renderCreateGpu(available) {
    $('create-gpu-label').hidden = !available;
    $('create-gpu-note').hidden = !available;
    if (!available) $('create-gpu').checked = false;
  }

  async function createSandbox() {
    const name = String($('create-name').value || '').trim();
    if (!state.authorized) return;
    if (!/^[a-z][a-z0-9-]{0,31}$/.test(name)) { $('create-status').textContent = t('error.name_invalid', { max: 32 }); return; }
    $('create-submit').disabled = true;
    $('create-status').textContent = t('create.creating');
    try {
      const body = {
        name,
        image: String($('create-image').value || '').trim(),
        presets: createPresetChoice(),
        providers: createProviderChoice(),
        gpu: !$('create-gpu-label').hidden && $('create-gpu').checked
      };
      await api('/api/sandboxes', { method: 'POST', body: JSON.stringify(body) });
      $('create-name').value = '';
      $('create-image').value = '';
      $('create-gpu').checked = false;
      $('create-providers').replaceChildren();
      $('create-providers').hidden = true;
      $('create-presets').replaceChildren();
      $('create-presets').hidden = true;
      $('create-volumes').textContent = '';
      $('create-panel').hidden = true;
      await refreshSandboxes();
      // A new sandbox starts, and its terminal connects, right away.
      if (cards.has(name)) { selectCard(cards.get(name)); cards.get(name).start(); }
    } catch (error) {
      $('create-status').textContent = t('create.failed', { message: error.message });
      if (error.status === 401 || error.status === 403) invalidateSession();
    } finally {
      $('create-submit').disabled = false;
    }
  }

  function closePair() {
    $('pair-url').textContent = '';
    $('pair-qr').removeAttribute('src');
    $('pair-qr').hidden = true;
    $('pair-panel').hidden = true;
  }

  // Web Push needs a secure context, a service worker and PushManager. iOS
  // exposes PushManager only to web apps added to the home screen.
  function pushSupported() {
    return typeof navigator !== 'undefined' && navigator.serviceWorker && typeof window.PushManager !== 'undefined' &&
      typeof window.Notification !== 'undefined';
  }

  function vapidKey(base64url) {
    const padded = (base64url + '==='.slice((base64url.length + 3) % 4)).replace(/-/g, '+').replace(/_/g, '/');
    return Uint8Array.from(atob(padded), (c) => c.charCodeAt(0));
  }

  async function setupPush() {
    if (!pushSupported()) return;
    const info = await api('/api/push');
    if (!info.available || typeof info.publicKey !== 'string' || !/^[A-Za-z0-9_-]{80,100}$/.test(info.publicKey)) return;
    state.pushKey = info.publicKey;
    const registration = await navigator.serviceWorker.register('/sw.js', { scope: '/' });
    state.pushRegistration = registration;
    const existing = await registration.pushManager.getSubscription();
    // Once subscribed there is nothing left to do here; the server is told
    // again in case it lost the subscription.
    $('push-button').hidden = !!existing;
    if (existing) await saveSubscription(existing);
  }

  function saveSubscription(subscription) {
    const json = subscription.toJSON();
    return api('/api/push/subscribe', { method: 'POST', body: JSON.stringify({ endpoint: json.endpoint, keys: { p256dh: json.keys.p256dh, auth: json.keys.auth } }) });
  }

  async function pushAction() {
    const registration = state.pushRegistration;
    if (!registration || !state.authorized) return;
    $('push-button').disabled = true;
    try {
      let subscription = await registration.pushManager.getSubscription();
      if (!subscription) {
        if (await window.Notification.requestPermission() !== 'granted') throw new Error(t('push.denied'));
        subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: vapidKey(state.pushKey) });
      }
      await saveSubscription(subscription);
      $('push-button').hidden = true;
    } catch (error) {
      showNotice(t('notice.pushFailed'), error.message);
    } finally {
      $('push-button').disabled = false;
    }
  }

  function validateSandboxes(data) {
    if (!data || typeof data.fake !== 'boolean' || !Array.isArray(data.sandboxes)) throw new Error(t('app.badList'));
    const seen = new Set();
    return data.sandboxes.map((item) => {
      if (!item || typeof item.name !== 'string' || !item.name || item.name.length > 128 || seen.has(item.name)) throw new Error(t('app.badName'));
      seen.add(item.name);
      return {
        name: item.name,
        status: typeof item.status === 'string' ? item.status : 'error',
        detail: typeof item.detail === 'string' ? item.detail : '',
        restartPending: item.restartPending === true,
        gpu: item.gpu === true,
        // When it stops after its terminals ended (autostop.go).
        autoStopAt: Number.isFinite(item.autoStopAt) ? item.autoStopAt : 0,
        // Terminals that stopped changing (notifier.go): name → when.
        stopped: Object.fromEntries(Object.entries(item.stopped && typeof item.stopped === 'object' ? item.stopped : {})
          .filter(([name, at]) => termNamePattern.test(name) && Number.isFinite(at)).slice(0, 32))
      };
    });
  }

  // A notification click opens /?sandbox=NAME&term=TERM (or &tab=…): that sandbox
  // with the terminal session (attached) or tab shown. The service worker
  // sends the same to a page that is already open.
  function parseOpen(search) {
    const q = new URLSearchParams(search);
    const sandbox = q.get('sandbox') || '';
    if (!sandbox || sandbox.length > 128) return null;
    const term = q.get('term') || '';
    const tab = q.get('tab') || '';
    return { sandbox, term: termNamePattern.test(term) ? term : '', tab: tab === 'policy' ? tab : '' };
  }
  state.openRequest = parseOpen(location.search);
  if (state.openRequest || /[?&](sandbox|term|tab)=/.test(location.search)) {
    // Drop only the request; other options (?font) stay.
    const rest = new URLSearchParams(location.search);
    for (const key of ['sandbox', 'term', 'tab']) rest.delete(key);
    history.replaceState(null, '', location.pathname + (rest.toString() ? '?' + rest : ''));
  }
  // Applies the request once the sandboxes are listed; an unknown one is dropped.
  function applyOpen() {
    const want = state.openRequest;
    if (!want || !state.authorized) return;
    state.openRequest = null;
    const card = cards.get(want.sandbox);
    if (!card) return;
    selectCard(card);
    if (want.tab) card.selectTab(want.tab);
    else card.openTerm(want.term || card.termName);
  }
  if (typeof navigator !== 'undefined' && navigator.serviceWorker) {
    navigator.serviceWorker.addEventListener('message', (event) => {
      if (event.data?.type !== 'open' || typeof event.data.search !== 'string') return;
      state.openRequest = parseOpen(event.data.search);
      applyOpen();
    });
  }

  // Which server build this page talks to: version · commit · build time.
  function renderBuild(info) {
    if (!info || typeof info !== 'object') return;
    state.build = info;
    const version = typeof info.version === 'string' && /^[0-9A-Za-z.+-]{1,32}$/.test(info.version) ? info.version : '';
    const commit = typeof info.commit === 'string' && /^([0-9a-f]{7,40}(-dirty)?|unknown)$/.test(info.commit) ? info.commit : '';
    const built = typeof info.built === 'string' ? new Date(info.built) : null;
    const pad = (n) => String(n).padStart(2, '0');
    const when = built && !isNaN(built) ? built.getFullYear() + '-' + pad(built.getMonth() + 1) + '-' + pad(built.getDate()) + ' ' + pad(built.getHours()) + ':' + pad(built.getMinutes()) : '';
    const head = [version && 'v' + version, commit].filter(Boolean).join(' · ');
    const tail = [when && t('build.built', { when }), info.debug === true && t('build.debug')].filter(Boolean).join(' · ');
    // Two lines in the narrow sidebar, one in the phone menu.
    for (const [id, text] of [['server-build', [head, tail].filter(Boolean).join('\n')], ['menu-build', [head, tail].filter(Boolean).join(' · ')]]) {
      if ($(id).textContent !== text) $(id).textContent = text;
      $(id).title = t('build.title');
    }
  }

  // Updating Iwato (web/update.js): the sidebar's button when there is a new build.
  const updateUI = window.IwatoUpdate.make({ element, button, api, onAuth: () => invalidateSession(), onCheck: () => refreshSandboxes().catch(() => {}) });
  const updateDialog = new updateUI.UpdateDialog();
  $('update-button').addEventListener('click', () => updateDialog.open());
  function renderUpdate(info) {
    const next = window.IwatoUpdate.build(info);
    $('update-button').hidden = !next;
    if (next) bindAttribute($('update-button'), 'title', 'update.badgeTitle', { build: window.IwatoUpdate.label(next) });
  }

  async function refreshSandboxes() {
    if (!state.authorized || state.disposed) return;
    if (state.refreshing) return state.refreshing;
    state.refreshing = (async () => {
      try {
        const data = await api('/api/sandboxes');
        const sandboxes = validateSandboxes(data);
        if (state.disposed) return;
        state.fake = data.fake;
        $('fake-banner').hidden = !state.fake;
        state.lastImages = data.images; state.lastEngine = data.engine;
        renderImages(data.images, data.engine);
        state.gpu = data.gpu !== false;
        renderCreateGpu(state.gpu);
        renderBuild(data.server);
        renderUpdate(data.update);
        const names = new Set(sandboxes.map((item) => item.name));
        for (const [name, card] of cards) {
          if (!names.has(name)) { card.dispose(); cards.delete(name); }
        }
        for (const item of sandboxes) {
          if (!cards.has(item.name)) cards.set(item.name, new SandboxCard(item));
          else { cards.get(item.name).update(item); cards.get(item.name).autoConnect(); }
        }
        // A notification's sandbox is selected first, not connected after another.
        if (!cards.has(state.selected)) selectCard(cards.get(state.openRequest?.sandbox) || cards.values().next().value || null);
        applyOpen();
        if (!document.hidden) for (const card of cards.values()) { card.policy.refresh(); }
        $('empty-state').hidden = sandboxes.length > 0;
        if (!sandboxes.length) {
          bind($('empty-title'), 'empty.none.title');
          bind($('empty-text'), 'empty.none.text');
          if (!state.createShown) { state.createShown = true; openCreate(); }
        }
        const navEmpty = $('sandbox-nav').querySelector('.nav-empty');
        if (navEmpty) navEmpty.remove();
        summary();
        clearNotice();
        $('refresh-button').hidden = true;
        serverStatus(state.fake ? 'server.fake' : 'server.connected', 'ready');
      } catch (error) {
        if (error.status === 401 || error.status === 403) invalidateSession();
        else {
          serverStatus('server.checkResponse', 'error');
          $('refresh-button').hidden = false;
          showNotice(t('notice.refreshFailed'), t('notice.refreshFailedKeep', { message: error.message }));
          if (!cards.size) {
            bind($('empty-title'), 'empty.failed.title');
            bind($('empty-text'), 'empty.failed.text');
          }
        }
      } finally {
        state.refreshing = null;
        grid.setAttribute('aria-busy', 'false');
      }
    })();
    return state.refreshing;
  }

  // On a PC a link opens with Ctrl+click (Cmd+click on a Mac), as in other
  // terminals, so a plain click stays a click; touch screens open on tap.
  const linkClick = (event) => !!(event && (event.ctrlKey || event.metaKey)) ||
    (typeof matchMedia === 'function' && matchMedia('(hover: none)').matches);

  // Android opens an installed app's links in an in-app browser whatever
  // the target (Chromium issue 40745354); there the share sheet comes up
  // instead, where the owner picks a browser. Elsewhere: a new tab.
  const androidApp = () => /Android/.test(navigator.userAgent || '') && typeof navigator.share === 'function' &&
    typeof matchMedia === 'function' && matchMedia('(display-mode: standalone)').matches;

  function openLink(uri) {
    const url = window.IwatoLinks?.safeURL(uri);
    if (!url) return;
    const open = () => window.open(url, '_blank', 'noopener,noreferrer');
    if (!androidApp()) return open();
    // Closing the sheet opens nothing; a sheet that cannot open falls back.
    navigator.share({ url }).catch((error) => { if (error?.name !== 'AbortError') open(); });
  }

  // A link element on the page opens the same way.
  function linkAnchor(link, url) {
    link.href = url;
    link.target = '_blank';
    link.rel = 'noopener noreferrer';
    link.referrerPolicy = 'no-referrer';
    link.addEventListener('click', (event) => { if (androidApp()) { event.preventDefault(); openLink(url); } });
  }

  // URLs ({url}) and file paths ({path}: IwatoLinks.findPaths) in the
  // terminal's history and screen, newest first. Detects URLs that span
  // rows, including ones wrapped by applications (real newlines) rather
  // than by the terminal, by reading neighbour rows.
  function bufferLinks(terminal) {
    const lines = terminal.textLines();
    const rows = lines.map((line) => ({ cells: Array.from(line, (c, x) => ({ c, x })), wrapped: false }));
    const width = Math.max(1, ...lines.map((line) => line.length));
    const seen = new Set();
    const links = [];
    for (const link of window.IwatoLinks.wrappedLinks(rows, width, 1).reverse()) {
      const path = window.IwatoLinks.pathOf(link.url);
      const url = path ? '' : window.IwatoLinks.safeURL(link.url);
      if ((!url && !path) || seen.has(link.url)) continue;
      seen.add(link.url);
      links.push(path ? { path, text: link.url.slice(link.url.indexOf(':') + 1) } : { url });
    }
    return links;
  }

  function bufferText(terminal) { return terminal.textLines().join('\n') + '\n'; }

  const text = (value, max) => typeof value === 'string' ? value.slice(0, max) : '';
  // An endpoint's allowed_ips, as openshell_proposals.go says it
  // (allowedIPsLabel): OpenShell refuses internal addresses by default; with
  // allowed_ips the host must resolve into the list, internal ones included,
  // and a host-less endpoint lets any host that resolves into it through.
  const allowedIPsLabel = (host, ips) => host
    ? t('policy.allowedIps.host', { ips: ips.join(', ') })
    : t('policy.allowedIps.any', { ips: ips.join(', ') });
  // What the approve dialog may show of a proposal, and what it warns of:
  // the server's limits, the names and addresses that reach the host's
  // loopback, and Iwato's own port (the proposals list sends them).
  const policyGuard = { limits: { binaries: 8, endpoints: 20, ports: 16, rules: 32, deny: 16, allowedIps: 32 }, gateways: [], listenPort: 0 };
  const allowedIPsList = (value) => (Array.isArray(value) ? value.slice(0, policyGuard.limits.allowedIps) : []).filter((x) => typeof x === 'string' && x).map((x) => text(x, 64));
  const formatTime = (seconds) => Number.isFinite(seconds) ? window.IwatoI18n.date(seconds * 1000) : '';
  // Short: the time today, the date before (with the time when asked).
  const clockTime = (seconds, withDate = false) => {
    if (!Number.isFinite(seconds) || seconds <= 0) return '';
    const d = new Date(seconds * 1000), now = new Date();
    const pad = (n) => String(n).padStart(2, '0');
    const time = pad(d.getHours()) + ':' + pad(d.getMinutes());
    if (d.toDateString() === now.toDateString()) return time;
    return pad(d.getMonth() + 1) + '/' + pad(d.getDate()) + (withDate ? ' ' + time : '');
  };

  // title: the text, or { key } of web/i18n.js (follows a change of language).
  function panelHeader(title, ...controls) {
    const header = element('div', 'panel-header');
    const actions = element('div', 'panel-actions');
    actions.append(...controls);
    const heading = element('h3', 'panel-title');
    if (title && typeof title === 'object') bind(heading, title.key, title.params); else heading.textContent = title;
    header.append(heading, actions);
    return header;
  }

  // The sandbox's network policy as OpenShell keeps it, in three lists:
  // the proposals (OpenShell's draft chunks: the agent's, through Policy
  // Advisor, and OpenShell's own from blocked connections) waiting for the
  // owner; the effective policy, each rule with where it came from (a
  // provider, approved proposals, or by hand); and the rejected proposals.
  // Iwato keeps none of it.
  class PolicyPanel {
    constructor(sandbox) {
      this.sandbox = sandbox;
      this.snapshot = null;
      this.refreshing = null;
      this.mutating = false;
      this.disposed = false;
      this.generation = 0;
      // Rules opened on a phone (collapsed to a line there), and whether
      // the rejected list shows all.
      this.opened = new Set();
      this.allRejected = false;
      this.root = element('section', 'info-panel policy-panel');
      bindAttribute(this.root, 'aria-label', 'tab.policy');
      this.refreshButton = bind(button('button-subtle', '', () => this.refresh()), 'policy.refresh');
      this.status = element('p', 'panel-status', t('policy.checking'));
      this.status.setAttribute('role', 'status');
      this.pending = element('section', 'policy-sec policy-pending');
      this.error = element('div', 'panel-error');
      this.error.hidden = true;
      // What a decision did; an approval reaches the sandbox only at its
      // next settings poll (about every 10 seconds).
      this.done = element('p', 'panel-meta proposal-done');
      this.done.setAttribute('role', 'status');
      this.done.hidden = true;
      // The effective policy: its rules, editable piece by piece.
      this.current = element('section', 'policy-sec policy-current');
      // Its revisions (opened on demand), and one of them shown.
      this.history = element('div', 'policy-history');
      this.history.hidden = true;
      this.revisions = null;
      this.revision = null;
      // The network presets (opened on demand, shown under the rules):
      // the sandbox's, and the ones to put in.
      this.presets = element('div', 'policy-presets');
      this.presets.hidden = true;
      this.presetData = null;
      this.presetEditor = null;
      // The network policy as YAML, the other view (its own renders only).
      // Its edits stay until applied or reloaded, whichever view shows.
      this.yamlBox = element('div', 'policy-yaml');
      this.yamlEd = null;
      this.rejected = element('section', 'policy-sec policy-rejected');
      // The views, 一覧 (the lists) and YAML, switched in the header that
      // stays at the top while the lists scroll.
      this.list = element('div', 'policy-list');
      this.list.append(this.pending, this.current, this.history, this.rejected);
      const seg = (key) => {
        const b = button('policy-view-seg', '', () => this.showView(key));
        b.dataset.seg = key;
        b.mark = element('span', 'policy-view-mark');
        b.mark.hidden = true;
        b.append(bind(element('span'), key === 'list' ? 'policy.view.list' : 'policy.view.yaml'), b.mark);
        return b;
      };
      this.segments = { list: seg('list'), yaml: seg('yaml') };
      const group = element('div', 'policy-view-switch');
      group.setAttribute('role', 'group');
      bindAttribute(group, 'aria-label', 'policy.view.label');
      group.append(this.segments.list, this.segments.yaml);
      const row = element('div', 'policy-bar-row');
      row.append(group, this.refreshButton);
      this.bar = element('div', 'policy-bar');
      this.bar.append(row, this.status, this.error, this.done);
      this.root.append(this.bar, this.list, this.yamlBox);
      this.view = 'list';
      try { if (localStorage.getItem('iwato-policy-view') === 'yaml') this.view = 'yaml'; } catch (_) { /* storage unavailable */ }
      this.applyView();
      this.render();
    }

    // A proposal (pending, rejected or approved without its rule) as the
    // server sends it. Lists are cut at the server's limits (policyGuard);
    // one longer than that is marked unshown, which the server refuses
    // to approve as well.
    validateProposal(p) {
      const lim = policyGuard.limits;
      const unshown = Array.isArray(p.unshown) ? p.unshown.filter((x) => typeof x === 'string').slice(0, 20).map((x) => text(x, 100)) : [];
      const list = (value, max) => {
        if (!Array.isArray(value)) return [];
        if (value.length > max && !unshown.includes('> ' + max)) unshown.push('> ' + max);
        return value.slice(0, max);
      };
      const methodPaths = (value, max) => list(value, max).filter((r) => r && typeof r.path === 'string' && typeof r.method === 'string').map((r) => ({ method: text(r.method, 10), path: text(r.path, 512) }));
      return { id: p.id, digest: /^[0-9a-f]{64}$/.test(p.digest) ? p.digest : '', unshown, summary: text(p.summary, 600), createdAt: Number(p.createdAt), source: p.source === 'auto' ? 'auto' : 'agent', intent: text(p.intent, 600), notes: text(p.notes, 600), rule: text(p.rule, 200),
        hits: Number(p.hits) || 0, firstSeen: Number(p.firstSeen) || 0, lastSeen: Number(p.lastSeen) || 0,
        rules: methodPaths(p.rules, lim.rules),
        allowedIps: list(p.allowedIps, lim.endpoints).filter((x) => x && typeof x.host === 'string').map((x) => ({ host: text(x.host, 253), ips: allowedIPsList(x.ips) })).filter((x) => x.ips.length),
        binaries: list(p.binaries, lim.binaries + 1).filter((b) => typeof b === 'string' && b).map((b) => text(b, 512)),
        endpoints: list(p.endpoints, lim.endpoints).filter((e) => e && typeof e.host === 'string').map((e) => ({ host: text(e.host, 253), ports: list(e.ports, lim.ports).map(Number).filter((n) => Number.isInteger(n) && n > 0 && n < 65536), protocol: text(e.protocol, 20), access: text(e.access, 20), enforcement: text(e.enforcement, 20),
          tls: text(e.tls, 20), path: text(e.path, 512), rules: methodPaths(e.rules, lim.rules), deny: methodPaths(e.deny, lim.deny), allowedIps: allowedIPsList(e.allowedIps), credential: text(e.credential, 100),
          allowEncodedSlash: e.allowEncodedSlash === true, websocketCredentialRewrite: e.websocketCredentialRewrite === true, requestBodyCredentialRewrite: e.requestBodyCredentialRewrite === true, allowUninspectedCredentials: e.allowUninspectedCredentials === true })),
        decidedAt: Number(p.decidedAt) || 0, hitsAfter: Number.isInteger(p.hitsAfter) ? p.hitsAfter : -1, seenAfter: p.seenAfter === true,
        auto: ['superseded', 'covered'].includes(p.auto) ? p.auto : '', ruleExists: p.ruleExists === true, placeholder: p.placeholder === true,
        // The approve choices OpenShell would refuse (an endpoint already in
        // the policy, a provider's rule, has other settings), with why.
        blocked: Object.fromEntries(['read', 'rules', 'all', 'audit'].filter((k) => p.blocked && p.blocked[k] && typeof p.blocked[k].reason === 'string').map((k) => {
          const vs = list(p.blocked[k].ambiguities, 20).filter((a) => a && typeof a.rule === 'string').map(validAmbiguity);
          return [k, { reason: window.IwatoRefusals.blocked(k, vs, text(p.blocked[k].reason, 1500)) }];
        })),
        // On a provider's host (ADR 0006): approving widens its scope, or
        // (with a reason) adds the rule with the provider's token.
        providerScope: p.providerScope && typeof p.providerScope.provider === 'string' && p.providerScope.provider ? {
          provider: text(p.providerScope.provider, 100), label: text(p.providerScope.label, 100), reason: window.IwatoI18n.serverError(p.providerScope.reasonCode, p.providerScope.reasonParams, text(p.providerScope.reason, 600)), resolving: p.providerScope.resolving === true,
          repos: list(p.providerScope.repos, 50).filter((r) => typeof r === 'string').map((r) => text(r, 200)),
          mutations: list(p.providerScope.mutations, 50).filter((m) => typeof m === 'string').map((m) => text(m, 100)),
          tools: list(p.providerScope.tools, 100).filter((m) => typeof m === 'string').map((m) => text(m, 128)), write: p.providerScope.write === true } : null,
        scopeProvider: text(p.scopeProvider, 100) };
    }

    validate(data) {
      if (!data || typeof data !== 'object') throw new Error(t('policy.badData'));
      const list = (value, max = 50) => Array.isArray(value) ? value.slice(0, max) : [];
      // What the approve dialog shows of a proposal and warns of (proposal_guard.go).
      const lim = data.limits && typeof data.limits === 'object' ? data.limits : {};
      for (const k of Object.keys(policyGuard.limits)) if (Number.isInteger(lim[k]) && lim[k] > 0 && lim[k] <= 256) policyGuard.limits[k] = lim[k];
      policyGuard.gateways = list(data.hostGateways, 50).filter((g) => typeof g === 'string' && g).map((g) => text(g, 253).toLowerCase());
      policyGuard.listenPort = Number.isInteger(data.listenPort) && data.listenPort > 0 && data.listenPort < 65536 ? data.listenPort : 0;
      const proposals = (value, state, max) => list(value, max).filter((p) => p && /^[a-f0-9]{32}$/.test(p.id) && p.state === state).map((p) => this.validateProposal(p));
      return {
        available: data.available === true, error: text(data.error, 1000),
        pending: proposals(data.pending, 'pending-approval', 50),
        rejected: proposals(data.rejected, 'rejected', 200),
        unlinked: proposals(data.unlinked, 'approved', 50),
        policy: this.validatePolicy(data.policy),
        // Revisions approvals and edits made, until a while after the sandbox loaded them.
        changes: list(data.changes).filter((c) => c && Number(c.version) > 0).slice(0, 20)
          .map((c) => ({ version: Number(c.version), kind: c.kind === 'approve' ? 'approve' : 'edit', rule: text(c.rule, 200), summary: text(c.summary, 600) }))
      };
    }

    validatePolicy(policy) {
      if (!policy || typeof policy !== 'object') return null;
      const list = (value, max) => Array.isArray(value) ? value.slice(0, max) : [];
      const methodPaths = (value) => list(value, 32).filter((x) => x && typeof x.path === 'string').map((x) => ({ method: text(x.method, 10), path: text(x.path, 512) }));
      const strings = (value, max, len) => list(value, max).filter((b) => typeof b === 'string' && b).map((b) => text(b, len));
      return {
        version: Number(policy.version) || 0, active: Number(policy.active) || 0, loaded: policy.loaded === true, loadError: text(policy.loadError, 500),
        rules: list(policy.rules, 80).filter((r) => r && typeof r.name === 'string' && r.name).map((r) => ({
          name: text(r.name, 200),
          source: ['provider', 'proposal', 'hand'].includes(r.source) ? r.source : 'hand',
          provider: r.provider && typeof r.provider.name === 'string' ? { name: text(r.provider.name, 100), profile: text(r.provider.profile, 100), label: text(r.provider.label, 100) } : null,
          // Endpoints where an attached provider's credential goes.
          providerHosts: list(r.providerHosts, 20).filter((h) => h && typeof h.provider === 'string' && typeof h.host === 'string').map((h) => ({ provider: text(h.provider, 100), label: text(h.label, 100), host: text(h.host, 253) })),
          chunks: list(r.chunks, 20).filter((c) => c && /^[a-f0-9]{32}$/.test(c.id)).map((c) => ({ id: c.id, binary: text(c.binary, 512), binaries: strings(c.binaries, 8, 512),
            widened: c.widened === true, source: c.source === 'auto' ? 'auto' : 'agent', intent: text(c.intent, 600), decidedAt: Number(c.decidedAt) || 0,
            removesRule: c.removesRule === true, drops: strings(c.drops, 20, 300) })),
          binaries: list(r.binaries, 20).filter((b) => typeof b === 'string').map((b) => text(b, 300)),
          endpoints: list(r.endpoints, 20).filter((e) => e && typeof e.host === 'string').map((e) => ({
            host: text(e.host, 253), ports: list(e.ports, 16).map(Number).filter((n) => n > 0 && n < 65536), path: text(e.path, 512),
            inspect: text(e.inspect, 20), enforcement: text(e.enforcement, 20), access: text(e.access, 20),
            rules: methodPaths(e.rules), deny: methodPaths(e.deny), allowedIps: allowedIPsList(e.allowedIps),
            observed: list(e.observed, 50).filter((x) => x && typeof x.path === 'string').map((x) => ({ method: text(x.method, 10), path: text(x.path, 512), binary: text(x.binary, 512), count: Number(x.count) || 0, last: Number(x.last) || 0 })),
            denied: list(e.denied, 100).filter((x) => x && typeof x.path === 'string' && (x.kind === 'binary' ? typeof x.binary === 'string' && x.binary : x.path)).map((x) => ({ kind: x.kind === 'binary' ? 'binary' : 'request', method: text(x.method, 10), path: text(x.path, 512), binary: text(x.binary, 512), count: Number(x.count) || 0, last: Number(x.last) || 0, reason: text(x.reason, 300) }))
          }))
        }))
      };
    }

    // Where a change made in Iwato stands: the sandbox loads a new revision
    // at its settings poll (about every 10 seconds), a stopped one when it
    // starts.
    changeState(change, policy) {
      if (policy && policy.active >= change.version) return 'applied';
      if (policy && policy.loadError && policy.version >= change.version) return 'failed';
      if (this.sandbox.status !== 'running') return 'on-start';
      return 'pending';
    }

    // Whether a change waits for the sandbox to load it (the tab polls faster then).
    waiting() {
      const data = this.snapshot;
      return Boolean(data && data.changes.some((c) => this.changeState(c, data.policy) === 'pending'));
    }

    // Whether the tab follows closely: a change waits, or the owner acted
    // within the last half minute (what follows from it shows soon).
    following() {
      return this.waiting() || Date.now() - (this.actedAt || 0) < 30000;
    }

    changeBadge(change, policy) {
      const st = this.changeState(change, policy);
      const done = change.kind === 'approve' ? t('policy.change.approved') : t('policy.change.changed');
      const label = { pending: t('policy.change.pending', { done }), 'on-start': t('policy.change.onStart', { done }), applied: t('policy.change.applied'), failed: t('policy.change.failed', { error: policy?.loadError || '' }) }[st];
      const badge = element('span', 'apply-badge', label);
      badge.dataset.state = st;
      badge.title = t('policy.version', { version: change.version }) + ' · ' + label;
      if (st === 'applied') {
        // Shown green for a few seconds after this page saw it loaded, fading.
        this.appliedSeen = this.appliedSeen || new Map();
        if (!this.appliedSeen.has(change.version)) this.appliedSeen.set(change.version, Date.now());
        const age = Date.now() - this.appliedSeen.get(change.version);
        if (age > 9000) return null;
        badge.style.opacity = String(Math.max(0.3, 1 - age / 9000));
      }
      return badge;
    }

    render() {
      const data = this.snapshot;
      this.onChange?.();
      let label = t('policy.status.check');
      // The waiting state (and its colour) is for the agent's proposals,
      // as the badges (agentPending); OpenShell's automatic ones are noted.
      const agent = agentPending(data);
      const auto = (data?.pending.length || 0) - agent;
      if (data) {
        if (!data.available) label = data.error ? t('policy.status.unavailable') : t('policy.status.notReady');
        else if (agent) label = t('policy.status.waiting', { count: agent }) + (auto ? t('policy.status.alsoAuto', { count: auto }) : '');
        else if (auto) label = t('policy.status.onlyAuto', { count: auto });
        else label = t('policy.status.none');
        // Proposals come only from a running sandbox; the policy is kept
        // (and editable) while it is stopped, failed or being made.
        if (data.available && this.sandbox.status !== 'running') label += t('policy.status.stopped');
      }
      if (this.mutating) label = t('policy.status.sending');
      if (this.status.textContent !== label) this.status.textContent = label;
      this.root.dataset.state = agent ? 'pending-approval' : data?.available ? 'ready' : 'unavailable';
      this.renderPending(data);
      this.renderPolicy(data?.policy);
      this.renderRejected(data);
      if (this.focusing) {
        const node = [...this.current.querySelectorAll('.policy-rule')].find((n) => n.dataset.rule === this.focusing);
        if (node) {
          this.focusing = '';
          (node.querySelector('.policy-observed') || node).scrollIntoView({ block: 'center' });
        }
      }
      this.refreshButton.disabled = !state.authorized || this.mutating || this.disposed;
      if (this.view === 'yaml') this.renderYAML();
      this.renderSegments();
    }

    // 一覧 or YAML (remembered per browser). The YAML is read when first
    // shown; edits made earlier are kept.
    showView(view) {
      if (view !== 'list' && view !== 'yaml') return;
      if (view !== this.view) {
        this.view = view;
        try { localStorage.setItem('iwato-policy-view', view); } catch (_) { /* storage unavailable */ }
      }
      this.applyView();
      this.render();
    }

    applyView() {
      const yaml = this.view === 'yaml';
      this.root.dataset.policyView = this.view;
      this.list.hidden = yaml;
      this.yamlBox.hidden = !yaml;
      if (yaml && !this.yamlEd && !this.yamlLoading) this.loadYAML();
    }

    // The switch: which view shows, the agent's proposals waiting (on 一覧
    // while the YAML shows), and unsaved YAML edits.
    renderSegments() {
      const count = agentPending(this.snapshot);
      const dirty = this.yamlDirty();
      for (const [key, b] of Object.entries(this.segments)) {
        b.setAttribute('aria-pressed', String(this.view === key));
        const mark = b.mark;
        const label = key === 'list' ? (count && this.view === 'yaml' ? String(count) : '') : dirty ? t('policy.view.unsaved') : '';
        if (mark.textContent !== label) mark.textContent = label;
        mark.hidden = !label;
        mark.dataset.kind = key === 'list' ? 'count' : 'unsaved';
      }
      this.segments.list.title = count ? t('tab.policy.pending', { count }) : '';
    }

    yamlDirty() {
      const ed = this.yamlEd;
      return Boolean(ed && ed.version && ed.yaml !== ed.original);
    }

    // What a proposal allows, in one line: where, the methods and paths
    // seen, and the program that asked.
    proposalWhat(p) {
      const where = p.endpoints.map((e) => (e.host || t('policy.anyHost')) + (e.ports.length ? ':' + e.ports.join(',') : '')).join(', ') || p.summary || p.rule;
      const paths = p.rules.slice(0, 2).map((r) => (r.method === '*' ? t('policy.method.all') : r.method) + ' ' + r.path).join(', ') + (p.rules.length > 2 ? t('policy.more', { count: p.rules.length - 2 }) : '');
      const node = element('p', 'proposal-what');
      node.append(element('span', 'proposal-where', where + (paths ? ' ' + paths : '')));
      // Every program it names; more than one, or every program, as a warning.
      if (p.binaries.length) {
        const bin = element('span', 'proposal-bin', ' ← ' + p.binaries.join(', '));
        bin.dataset.warn = String(p.binaries.length > 1 || p.binaries.some(approveLogic.allPrograms));
        node.append(bin);
      }
      return node;
    }

    sourceTag(source) {
      const tag = element('span', 'policy-src', source === 'auto' ? t('policy.source.auto') : t('policy.source.agent'));
      tag.dataset.source = source;
      return tag;
    }

    proposalNode(p, kind) {
      const row = element('div', 'proposal-item');
      row.dataset.kind = kind;
      const meta = element('p', 'panel-meta proposal-meta');
      meta.append(this.sourceTag(p.source));
      const off = this.mutating || !state.authorized;
      const actions = element('div', 'panel-buttons');
      if (kind === 'pending') {
        meta.append(element('span', '', t('policy.hits', { count: p.hits }) + (p.firstSeen ? t('policy.firstSeen', { time: clockTime(p.firstSeen) }) : '') + (p.lastSeen ? t('policy.lastSeen', { time: clockTime(p.lastSeen) }) : '') + (p.rule ? t('policy.ruleOf', { rule: p.rule }) : '')));
        const widens = p.providerScope && !p.providerScope.reason;
        const approve = button('button-start', widens ? t('policy.widen') : t('policy.approve'), () => this.decide(p, 'approve'));
        approve.setAttribute('aria-label', widens ? t('policy.widen.label') : t('policy.approve.label'));
        const reject = button('button-disconnect', t('policy.reject'), () => this.decide(p, 'reject'));
        approve.disabled = reject.disabled = off || Boolean(p.providerScope?.resolving);
        actions.append(approve, reject);
      } else if (p.scopeProvider) {
        // Closed by Iwato after widening the provider's scope instead.
        meta.append(element('span', 'proposal-after', t('policy.closedByScope', { time: clockTime(p.decidedAt, true), provider: p.scopeProvider })));
      } else {
        let when = t('policy.rejectedAt', { time: clockTime(p.decidedAt, true) });
        if (p.auto) when += t('policy.autoRejected', { why: p.auto === 'covered' ? t('policy.autoRejected.covered') : t('policy.autoRejected.superseded') });
        if (p.hitsAfter > 0) when += t('policy.hitsAfter', { count: p.hitsAfter, time: clockTime(p.lastSeen) });
        else if (p.hitsAfter < 0 && p.seenAfter) when += t('policy.seenAfter', { time: clockTime(p.lastSeen) });
        const after = element('span', 'proposal-after', when);
        after.dataset.after = String(p.hitsAfter > 0 || p.seenAfter);
        meta.append(after);
        const approve = button('button-start', t('policy.approveRejected'), () => this.decide(p, 'approve', true));
        approve.disabled = off;
        actions.append(approve);
      }
      row.append(this.proposalWhat(p), meta);
      if (p.intent) row.append(element('p', 'proposal-intent', t('policy.quote', { text: p.intent })));
      if (p.notes) row.append(element('p', 'panel-meta proposal-notes', t('policy.notes', { notes: p.notes })));
      if (kind === 'pending' && p.providerScope) {
        const note = element('p', 'panel-meta proposal-provider', approveLogic.scopeNote(p.providerScope));
        note.dataset.kind = p.providerScope.resolving ? 'resolving' : p.providerScope.reason ? 'rule' : 'scope';
        row.append(note);
      }
      row.append(actions);
      return row;
    }

    renderPending(data) {
      this.pending.replaceChildren();
      if (!data?.available) return;
      this.pending.append(element('h4', 'policy-sec-title', t('policy.pending.title', { count: data.pending.length })));
      if (!data.pending.length) this.pending.append(element('p', 'panel-meta', t('policy.pending.none')));
      // The agent's proposals first, then those OpenShell drafted from
      // blocked connections under their own heading; each keeps its order.
      const agent = data.pending.filter((p) => p.source === 'agent');
      const auto = data.pending.filter((p) => p.source !== 'agent');
      for (const p of agent) this.pending.append(this.proposalNode(p, 'pending'));
      if (auto.length) this.pending.append(element('h5', 'policy-sub-title', t('policy.pending.auto', { count: auto.length })));
      for (const p of auto) this.pending.append(this.proposalNode(p, 'pending'));
    }

    renderRejected(data) {
      this.rejected.replaceChildren();
      if (!data?.available || !data.rejected.length) return;
      this.rejected.append(element('h4', 'policy-sec-title', t('policy.rejected.title', { count: data.rejected.length })));
      const shown = this.allRejected ? data.rejected : data.rejected.slice(0, 10);
      for (const p of shown) this.rejected.append(this.proposalNode(p, 'rejected'));
      if (data.rejected.length > 10) {
        this.rejected.append(button('button-subtle policy-more-rejected', this.allRejected ? t('policy.rejected.fewer') : t('policy.rejected.more', { count: data.rejected.length - 10 }), () => { this.allRejected = !this.allRejected; this.render(); }));
      }
    }

    renderPolicy(policy) {
      this.current.replaceChildren();
      if (!policy) return;
      const loadState = policy.loadError ? t('policy.load.failed', { error: policy.loadError }) : policy.loaded ? t('policy.load.loaded') : this.sandbox.status === 'running' ? t('policy.load.waiting') : t('policy.load.onStart');
      const head = element('div', 'policy-current-head');
      const add = button('button-subtle', t('policy.addRule'), () => this.addEndpoint(null));
      const history = button('button-subtle', this.history.hidden ? t('policy.history') : t('policy.history.close'), () => this.toggleHistory());
      const presets = button('button-subtle policy-presets-toggle', this.presets.hidden ? t('policy.presets') : t('policy.presets.close'), () => this.togglePresets());
      add.disabled = this.mutating || !state.authorized;
      const unlinked = this.snapshot?.unlinked || [];
      const title = element('h4', 'policy-sec-title policy-current-title', t('policy.current.title', { count: policy.rules.length }));
      head.append(title, add, presets, history);
      const meta = element('p', 'panel-meta', t('policy.version', { version: policy.version }) + ' · ' + loadState);
      // On a phone the rules fold to a line each: what waits, at a glance.
      const counts = element('p', 'panel-meta policy-counts');
      const waiting = (this.snapshot?.changes || []).filter((c) => ['pending', 'on-start'].includes(this.changeState(c, policy))).length;
      counts.textContent = [waiting && t('policy.counts.waiting', { count: waiting }), unlinked.length && t('policy.counts.unlinked', { count: unlinked.length })].filter(Boolean).join(' · ');
      counts.hidden = !counts.textContent;
      this.current.append(head, meta, counts);
      this.current.append(...this.ruleNodes(policy, true));
      for (const u of unlinked) this.current.append(this.unlinkedNode(u));
      // The presets (opened from the header) under the rules.
      this.current.append(this.presets);
      // A change whose rule is gone (or unknown) shows on the header.
      const names = new Set(policy.rules.map((r) => r.name));
      for (const c of this.snapshot?.changes || []) {
        if (names.has(c.rule)) continue;
        const badge = this.changeBadge(c, policy);
        if (badge) meta.append(badge);
      }
    }

    // An approved proposal whose rule was taken out by hand: OpenShell
    // keeps it approved, so the same request is not proposed again.
    unlinkedNode(u) {
      const row = element('div', 'policy-rule policy-unlinked');
      const head = element('div', 'policy-current-head');
      head.append(element('p', 'policy-rule-name', u.rule + ' — ' + (u.ruleExists ? t('policy.unlinked.noBinary') : t('policy.unlinked.noRule'))));
      const restore = button('button-subtle', t('policy.unlinked.restore'), () => this.undo(u, null, true));
      restore.disabled = this.mutating || !state.authorized;
      head.append(restore);
      const what = (u.binaries[0] || '') + ' → ' + u.endpoints.map((e) => e.host + ':' + e.ports.join(',')).join(', ');
      row.append(head, element('p', 'panel-meta policy-unlinked-text', t('policy.unlinked.text', { what, time: clockTime(u.decidedAt, true) })));
      return row;
    }

    // The rules as rows; editable ones have buttons to take each piece out
    // (the last of a kind goes with its endpoint or rule) and to add more.
    // A provider's rule is read-only; one from approved proposals is
    // undone proposal by proposal, not taken out.
    ruleNodes(policy, editable) {
      if (!policy.rules.length) return [element('p', 'panel-meta', t('policy.rules.none'))];
      const off = this.mutating || !state.authorized;
      // removeLabel: the remove button's accessible name.
      const line = (className, label, remove, removeLabel) => {
        const row = element('div', 'policy-line ' + className);
        row.append(element('span', '', label));
        if (remove) {
          const x = button('button-subtle button-icon', '×', remove);
          x.setAttribute('aria-label', removeLabel);
          x.disabled = off;
          row.append(x);
        }
        return row;
      };
      const base = (path) => path.slice(path.lastIndexOf('/') + 1);
      return (policy.rules || []).map((r) => {
        const edits = editable && r.source !== 'provider';
        const chunks = r.chunks || [];
        const row = element('div', 'policy-rule');
        row.dataset.rule = r.name;
        row.dataset.source = r.source || 'hand';
        row.dataset.open = String(this.opened.has(r.name));
        const head = element('div', 'policy-current-head');
        // The name, where it came from, and the latest change made to it
        // (until a while after it is loaded).
        const title = element('div', 'policy-rule-title');
        title.append(element('p', 'policy-rule-name', r.name));
        if (r.source === 'provider') {
          const tag = element('span', 'policy-src', r.provider ? t('policy.src.providerNamed', { name: r.provider.name + (r.provider.label ? ' (' + r.provider.label + ')' : '') }) : t('policy.src.provider'));
          tag.dataset.source = 'provider';
          title.append(tag);
        } else if (editable && !chunks.length) {
          const tag = element('span', 'policy-src', t('policy.src.hand'));
          tag.dataset.source = 'hand';
          title.append(tag);
        }
        // Endpoints a provider's credential reaches (ADR 0006: the provider's scope's).
        const hostProviders = [...new Set((r.providerHosts || []).map((h) => h.provider))];
        for (const name of hostProviders) {
          const tag = element('span', 'policy-src', t('policy.src.providerHost', { name }));
          tag.dataset.source = 'provider-host';
          tag.title = t('policy.src.providerHost.title', { name });
          title.append(tag);
        }
        const change = editable ? (this.snapshot?.changes || []).filter((c) => c.rule === r.name).at(-1) : null;
        const badge = change && this.changeBadge(change, policy);
        if (badge) title.append(badge);
        head.append(title);
        if (edits) {
          const addTo = button('button-subtle policy-addto', t('policy.addEndpoint'), () => this.addEndpoint(r));
          addTo.disabled = off;
          head.append(addTo);
          if (!chunks.length) {
            const remove = button('button-subtle', t('policy.remove'), () => this.edit('remove', { rule: r.name }, t('policy.remove.rule.confirm', { rule: r.name }), t('policy.remove.rule.effect')));
            remove.disabled = off;
            remove.setAttribute('aria-label', t('policy.remove.rule', { rule: r.name }));
            head.append(remove);
          }
        }
        if (editable) {
          // Folded on a phone: open it, or undo its one proposal from here.
          const open = button('button-subtle policy-open', this.opened.has(r.name) ? t('common.close') : t('policy.open'), () => {
            if (this.opened.has(r.name)) this.opened.delete(r.name); else this.opened.add(r.name);
            this.render();
          });
          open.setAttribute('aria-label', t(this.opened.has(r.name) ? 'policy.close.rule' : 'policy.open.rule', { rule: r.name }));
          head.append(open);
          if (chunks.length === 1) {
            const quick = button('button-subtle policy-quick', t('policy.undo'), () => this.undo(chunks[0], r, false));
            quick.disabled = off;
            quick.setAttribute('aria-label', t('policy.undo.rule', { rule: r.name }));
            head.append(quick);
          }
        }
        row.append(head);
        // The folded line (a phone shows it instead of the body).
        const hosts = r.endpoints.map((e) => e.host || t('policy.anyHost'));
        const audited = r.endpoints.filter((e) => e.enforcement === 'audit').reduce((n, e) => n + e.observed.length, 0);
        const denied = r.endpoints.reduce((n, e) => n + e.denied.length, 0);
        const fold = (r.binaries.length ? r.binaries.map(base).join(', ') : t('policy.allPrograms')) + ' → ' + hosts[0] + (hosts.length > 1 ? t('policy.more', { count: hosts.length - 1 }) : '') +
          (r.endpoints.some((e) => e.enforcement === 'audit') ? t('policy.fold.audit', { count: audited }) : '') + (denied ? t('policy.fold.denied', { count: denied }) : '') + (chunks.length ? (chunks.length > 1 ? t('policy.fold.proposals', { count: chunks.length }) : t('policy.fold.proposal')) : '');
        if (editable) row.append(element('p', 'panel-meta policy-rule-fold', fold));
        const body = element('div', 'policy-rule-body');
        if (r.source === 'provider') body.append(element('p', 'panel-meta policy-provider-note', t('policy.provider.note')));
        if (hostProviders.length) {
          const hosts = [...new Set(r.providerHosts.map((h) => h.host))].join(', ');
          const who = r.providerHosts.filter((h, i, a) => a.findIndex((x) => x.provider === h.provider) === i).map((h) => h.provider + (h.label ? ' (' + h.label + ')' : '')).join(t('common.listSep'));
          body.append(element('p', 'panel-meta policy-provider-host-note', t('policy.providerHost.note', { hosts, who })));
        }
        for (const c of chunks) {
          const chunk = element('div', 'policy-chunk');
          const what = c.widened ? t('policy.chunk.widened', { binary: c.binary, binaries: c.binaries.join(', ') }) : (c.binaries.join(', ') || c.binary);
          const from = element('span', 'policy-src', t('policy.src.proposal'));
          from.dataset.source = c.source;
          chunk.append(from, element('span', 'policy-chunk-text', what + (c.intent ? ' ' + t('policy.quote', { text: c.intent }) : '') + ' · ' + t('policy.chunk.approvedAt', { time: clockTime(c.decidedAt, true) })));
          if (edits) {
            const undo = button('button-subtle', t('policy.undo'), () => this.undo(c, r, false));
            undo.disabled = off;
            undo.setAttribute('aria-label', t('policy.undo.proposal', { binary: c.binary || c.binaries[0] || '' }));
            chunk.append(undo);
          }
          body.append(chunk);
        }
        // Programs from proposals show on their lines; the rest (by hand)
        // come out one by one.
        const claimed = new Set(chunks.flatMap((c) => [c.binary, ...c.binaries]));
        if (!r.binaries.length) body.append(line('policy-binary', t('policy.allPrograms')));
        for (const b of r.binaries) {
          if (claimed.has(b)) continue;
          body.append(line('policy-binary', b, edits && r.binaries.length > 1 && (() => this.edit('remove-binary', { rule: r.name, binary: b }, t('policy.remove.binary.confirm', { rule: r.name, binary: b }), t('policy.remove.binary.effect'))), t('policy.remove.binary', { binary: b })));
        }
        for (const e of r.endpoints) {
          const where = (e.host || t('policy.anyHost')) + (e.ports.length ? ':' + e.ports.join(',') : '') + (e.path ? ' ' + e.path : '');
          const target = { rule: r.name, host: e.host, ports: e.ports };
          const endpoint = element('div', 'policy-endpoint');
          endpoint.append(line('policy-host', where + (e.inspect ? t('policy.inspect', { mode: e.enforcement === 'audit' ? t('policy.inspect.audit') : t('policy.inspect.enforce') }) : t('policy.inspect.none')),
            edits && r.endpoints.length > 1 && (() => this.edit('remove-endpoint', target, t('policy.remove.endpoint.confirm', { rule: r.name, where }), t('policy.remove.endpoint.effect'))), t('policy.remove.endpoint', { where })));
          if (e.allowedIps.length) endpoint.append(line('policy-allowed-ips', allowedIPsLabel(e.host, e.allowedIps)));
          if (e.access) endpoint.append(line('policy-allow', accessLabel(e.access) || e.access));
          for (const x of e.rules) {
            const label = (x.method === '*' ? t('policy.method.all') : x.method) + ' ' + pathLabel(x.path);
            endpoint.append(line('policy-allow', label, edits && (e.rules.length > 1 || e.access) && (() => this.edit('remove-allow', { ...target, rules: [x] }, t('policy.remove.allow.confirm', { where, label }), t('policy.remove.allow.effect'))), t('policy.remove.allow', { label })));
          }
          for (const x of e.deny) endpoint.append(line('policy-deny', t('policy.deny', { what: (x.method === '*' ? t('policy.method.all') : x.method) + ' ' + pathLabel(x.path) })));
          if (edits && e.inspect) {
            const more = button('button-subtle policy-more', t('policy.addAllow'), () => this.addAllow(r, e));
            more.disabled = off;
            endpoint.append(more);
          }
          if (edits && e.enforcement === 'audit') endpoint.append(this.observedNode(r, e, off));
          if (editable && e.denied.length) endpoint.append(this.deniedNode(r, e, off));
          body.append(endpoint);
        }
        row.append(body);
        return row;
      });
    }

    // An audited endpoint lets everything through and only logs it: the
    // requests it logged, to enforce it with the chosen ones. They are
    // read from the sandbox's log through the gateway (its last 2000
    // lines), never kept.
    observedNode(rule, e, off) {
      const box = element('div', 'policy-observed');
      const where = e.host + ':' + e.ports.join(',');
      box.append(element('p', 'panel-meta', e.observed.length
        ? t('policy.observed') : t('policy.observed.none')));
      const picks = e.observed.map((x) => {
        const row = element('label', 'policy-observed-item');
        const check = document.createElement('input');
        check.type = 'checkbox';
        check.checked = true;
        check.disabled = off;
        row.append(check, element('span', '', x.method + ' ' + pathLabel(x.path)), element('span', 'panel-meta', '×' + x.count + (x.binary ? ' · ' + x.binary : '') + ' · ' + formatTime(Math.floor(x.last / 1000))));
        box.append(row);
        return { check, rule: { method: x.method, path: x.path } };
      });
      const enforce = button('button-start', t('policy.enforce'), async () => {
        if (this.mutating || !state.authorized) return;
        const seed = picks.filter((p) => p.check.checked).map((p) => p.rule);
        const rules = await askAllow(t('policy.enforce.confirm', { rule: rule.name, where }), seed, t('policy.enforce.ok'));
        if (rules?.length) this.send('enforce', { rule: rule.name, host: e.host, ports: e.ports, rules }, t('policy.enforce.done'));
      });
      enforce.disabled = off;
      box.append(enforce);
      return box;
    }

    // Requests the endpoint denied (and still would), and programs its rule
    // refused: OpenShell makes no proposal for a denied method or path
    // (the agent is to propose, and may not). Read from the sandbox's log
    // through the gateway (its last 2000 lines), never kept. A provider's
    // rule cannot change: a rule by hand for the same host takes them
    // instead.
    deniedNode(rule, e, off) {
      const box = element('div', 'policy-denied');
      const where = e.host + ':' + e.ports.join(',');
      const provider = rule.source === 'provider';
      box.append(element('p', 'panel-meta', t('policy.denied')));
      const reasonLabel = (reason) => {
        if (/ blocked by deny rule$/.test(reason)) return t('policy.denied.denyRule');
        if (/ not permitted by policy$/.test(reason)) return t('policy.denied.notAllowed');
        if (reason === 'no L7 endpoint path matched request') return t('policy.denied.outsidePath');
        return reason;
      };
      const picks = [];
      for (const x of e.denied) {
        const row = element('label', 'policy-denied-item');
        const what = element('div', 'policy-denied-what');
        if (x.kind === 'binary') {
          row.dataset.kind = 'binary';
          what.append(element('span', '', t('policy.program', { binary: x.binary })), element('span', 'panel-meta', '×' + x.count + ' · ' + formatTime(Math.floor(x.last / 1000))));
          what.append(element('span', 'panel-meta policy-denied-reason', t('policy.denied.binary')));
          row.append(what);
          box.append(row);
          continue;
        }
        const check = document.createElement('input');
        check.type = 'checkbox';
        const blocked = / blocked by deny rule$/.test(x.reason);
        check.disabled = off || blocked;
        what.append(element('span', '', x.method + ' ' + pathLabel(x.path)), element('span', 'panel-meta', '×' + x.count + (x.binary ? ' · ' + x.binary : '') + ' · ' + formatTime(Math.floor(x.last / 1000))));
        if (x.reason) what.append(element('span', 'panel-meta policy-denied-reason', reasonLabel(x.reason)));
        // Kept checked across the tab's re-reads.
        const key = [rule.name, e.host, e.ports.join(','), x.method, x.path, x.binary].join('\n');
        this.deniedPicked = this.deniedPicked || new Set();
        check.checked = !check.disabled && this.deniedPicked.has(key);
        check.addEventListener('change', () => { if (check.checked) this.deniedPicked.add(key); else this.deniedPicked.delete(key); });
        row.append(check, what);
        box.append(row);
        picks.push({ check, x });
      }
      if (!picks.length) return box;
      const chosen = () => picks.filter((p) => p.check.checked);
      const add = button('button-start', provider ? t('policy.addRule') : t('policy.denied.allow'), async () => {
        if (this.mutating || !state.authorized) return;
        const seed = chosen().map((p) => ({ method: p.x.method, path: p.x.path }));
        if (!seed.length) return;
        if (provider) {
          // The same host by hand: enforcing with methods and paths, as
          // the provider's rule does (OpenShell refuses two rules for one
          // host that inspect differently).
          const programs = [...new Set(chosen().map((p) => p.x.binary).filter(Boolean))];
          const edit = await askPolicyAdd(t('policy.denied.providerAdd', { rule: rule.name, where, sandbox: JSON.stringify(this.sandbox.name) }),
            this.sandbox.name + '\ndenied\n' + rule.name + '\n' + where + '\n' + seed.map((r) => r.method + ' ' + r.path).join('\n'), '',
            { host: e.host, ports: e.ports, binaries: programs.length ? programs : rule.binaries, rules: seed });
          if (edit) this.send('add', edit, t('policy.added'));
          return;
        }
        const preset = e.access ? t('policy.denied.preset', { access: accessLabel(e.access) || e.access, methods: ({ 'read-only': 'GET・HEAD・OPTIONS', 'read-write': 'GET・HEAD・OPTIONS・POST・PUT・PATCH', full: t('policy.access.full') }[e.access] || e.access) }) : '';
        const rules = await askAllow(t('policy.denied.allow.confirm', { rule: rule.name, where, preset }), seed);
        if (rules?.length) this.send('add-allow', { rule: rule.name, host: e.host, ports: e.ports, rules }, t('policy.added'));
      });
      if (provider) add.setAttribute('aria-label', t('policy.denied.addRule.label'));
      const sync = () => { add.disabled = off || !chosen().length; };
      for (const p of picks) p.check.addEventListener('change', sync);
      sync();
      box.append(add);
      return box;
    }

    async addEndpoint(rule) {
      if (this.mutating || !state.authorized) return;
      const text = (rule ? t('policy.addEndpoint.confirm', { rule: rule.name }) : t('policy.addRule.confirm')) + '\n\n' + t('policy.sandboxLine', { name: JSON.stringify(this.sandbox.name) });
      const edit = await askPolicyAdd(text, this.sandbox.name + '\n' + (rule ? rule.name : ''), rule?.name);
      if (edit) this.send('add', edit, t('policy.added'));
    }

    async addAllow(rule, e) {
      if (this.mutating || !state.authorized) return;
      const where = e.host + ':' + e.ports.join(',');
      const rules = await askAllow(t('policy.addAllow.confirm', { rule: rule.name, where }));
      if (rules) this.send('add-allow', { rule: rule.name, host: e.host, ports: e.ports, rules }, t('policy.added'));
    }

    async edit(op, body, question, effect) {
      if (this.mutating || !state.authorized) return;
      if (!(await askConfirm(question + '\n\n' + t('policy.sandboxLine', { name: JSON.stringify(this.sandbox.name) }) + '\n' + effect, { ok: t('policy.remove'), danger: true }))) return;
      this.send(op, body, t('policy.removed'));
    }

    // Undoing an approved proposal (chunk link c of rule r): OpenShell
    // takes its program out of the rule and puts it back to pending;
    // Iwato takes the glob it was widened to too. With restore (an
    // unlinked proposal c), its rule goes back first, so that the undo has
    // it to take out.
    async undo(c, r, restore) {
      if (this.mutating || !state.authorized) return;
      let question;
      if (restore) {
        question = t('policy.restore.confirm', { proposal: (c.binaries[0] || '') + ' → ' + c.endpoints.map((e) => e.host + ':' + e.ports.join(',')).join(', '), rule: c.rule, sandbox: JSON.stringify(this.sandbox.name) });
      } else {
        question = t('policy.undo.confirm', { rule: r.name, binary: c.binary || c.binaries[0], sandbox: JSON.stringify(this.sandbox.name), binaries: c.binaries.concat(c.binary).filter((b, i, a) => b && a.indexOf(b) === i && r.binaries.includes(b)).join(', ') });
        if (c.widened) question += t('policy.undo.widened', { binaries: c.binaries.join(', ') });
        if (c.removesRule) question += t('policy.undo.removesRule', { rule: r.name });
        if (c.removesRule && c.drops.length) question += t('policy.undo.drops', { endpoints: c.drops.join(', ') });
      }
      if (!(await askConfirm(question, { ok: restore ? t('policy.restore.ok') : t('policy.undo'), danger: !restore }))) return;
      this.post('/proposals/' + (restore ? 'restore' : 'undo'), { requestId: c.id }, restore ? t('policy.restore.done') : t('policy.undo.done'));
    }

    async send(op, body, doneText) {
      await this.post('/policy/' + op, body, doneText + t('policy.applySoon'), () => { if (op === 'add') resetPolicyForm(); });
    }

    // One owner action: the page waits for it, then reads the lists again.
    // Resolves to the error when the server refused (null otherwise).
    async post(path, body, doneText, succeeded) {
      if (this.disposed || this.mutating || !state.authorized) return null;
      let failed = null;
      this.mutating = true;
      this.generation += 1;
      this.showActionError('');
      this.render();
      try {
        const send = (b) => api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + path, { method: 'POST', body: JSON.stringify(b), timeoutMs: 40000 });
        let result;
        try {
          result = await send(body);
        } catch (error) {
          // A provider's credential would go with it: only once the owner says so.
          if (!error.confirm || !body || typeof body !== 'object' || !(await confirmProviderHosts(error))) throw error;
          result = await send({ ...body, confirm: error.confirm });
        }
        this.done.textContent = doneText;
        succeeded?.(result);
        this.done.hidden = false;
        clearTimeout(this.doneTimer);
        this.doneTimer = setTimeout(() => { this.done.hidden = true; }, 30000);
      } catch (error) {
        failed = error;
        this.showActionError(error.status === 400 || error.status === 409 ? error.message : t('policy.unknownResult', { message: error.message }), error.raw);
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        this.mutating = false;
        this.actedAt = Date.now();
        if (!this.disposed) { this.snapshot = null; this.render(); await this.refresh(); }
      }
      return failed;
    }

    togglePresets() {
      this.presets.hidden = !this.presets.hidden;
      this.presetData = null;
      this.render();
      this.renderPresets();
      if (!this.presets.hidden) this.loadPresets();
    }

    async loadPresets() {
      const epoch = this.generation;
      try {
        const [, mine] = await Promise.all([loadPresets(), api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/presets')]);
        if (this.disposed || epoch !== this.generation || this.presets.hidden) return;
        const list = Array.isArray(mine?.presets) ? mine.presets.slice(0, 50) : [];
        this.presetData = list.filter((p) => p && typeof p.id === 'string').map((p) => ({
          id: text(p.id, 60), label: text(p.label, 400), missing: p.missing === true,
          params: p.params && typeof p.params === 'object' ? Object.fromEntries(Object.entries(p.params).slice(0, 4).map(([k, v]) => [text(k, 20), text(v, 200)])) : null
        }));
      } catch (error) {
        if (this.disposed || this.presets.hidden) return;
        this.showActionError(t('presets.loadFailed', { message: error.message }));
      }
      this.renderPresets();
    }

    renderPresets() {
      this.presets.replaceChildren();
      if (this.presets.hidden) return;
      this.presets.append(element('h5', 'policy-sub-title', t('presets.title')));
      if (!this.presetData) { this.presets.append(element('p', 'panel-meta', t('common.loading'))); return; }
      const busy = this.mutating || !state.authorized;
      if (this.presetData.length) {
        for (const p of this.presetData) {
          const row = element('div', 'policy-preset');
          const remove = button('button-subtle', t('policy.remove'), () => this.removePreset(p));
          remove.disabled = busy;
          row.append(element('span', '', p.label + (p.missing ? t('presets.missing') : '')), remove);
          this.presets.append(row);
        }
      } else {
        this.presets.append(element('p', 'panel-meta', t('presets.none')));
      }
      const applied = (preset) => this.presetData.some((p) => p.id === preset.id && !preset.params.length);
      for (const preset of presetView.presets) {
        if (applied(preset)) continue;
        const row = element('div', 'policy-preset policy-preset-choice');
        const inputs = presetParamInputs(preset);
        const apply = button('button-subtle', t('presets.apply'), () => this.applyPreset(preset, inputs));
        apply.disabled = busy;
        const what = element('div', 'policy-preset-what');
        what.append(element('span', '', preset.label + (preset.source === 'user' ? t('presets.mineFile', { file: preset.file }) : '')), element('span', 'panel-meta', preset.note), element('span', 'panel-meta', t('presets.hosts', { hosts: preset.hosts.join(', ') })));
        if (preset.providerHosts.length) what.append(element('span', 'panel-meta preset-error', t('presets.providerHosts', { hosts: preset.providerHosts.join(', ') })));
        row.append(what, ...inputs, apply);
        this.presets.append(row);
      }
      for (const e of presetView.errors) this.presets.append(element('p', 'panel-meta preset-error', t('presets.unreadable', { file: e.file, error: window.IwatoI18n.serverError(e.code, e.params, e.error) })));
      this.renderPresetFiles();
    }

    // The owner's presets as YAML (files in the folder next to iwato.json).
    renderPresetFiles() {
      const ed = presetView.folder ? this.presetEditor : null;
      const area = keepCode(this, 'presetCode', ed, () => ({ value: ed.yaml, label: t('presets.files.yamlLabel', { file: ed.file || t('presets.files.new') }), className: 'preset-yaml-editor', onChange: (v) => { ed.yaml = v; } }));
      if (!presetView.folder) return;
      const box = element('div', 'preset-files');
      box.append(element('h5', 'policy-sub-title', t('presets.files.title')),
        element('p', 'panel-meta', t('presets.files.note', { folder: presetView.folder })));
      const files = [...new Set(presetView.presets.filter((p) => p.source === 'user').map((p) => p.file).concat(presetView.errors.map((e) => e.file)))].filter(Boolean);
      const busy = !state.authorized;
      for (const file of files) {
        const row = element('div', 'policy-preset');
        const edit = button('button-subtle', t('presets.files.edit'), () => this.openPresetFile(file));
        edit.setAttribute('aria-label', t('presets.files.editLabel', { file }));
        edit.disabled = busy;
        row.append(element('span', '', file), edit);
        box.append(row);
      }
      const add = button('button-subtle', t('presets.files.new'), () => { this.presetEditor = { file: '', yaml: presetTemplate(), error: '' }; this.renderPresets(); });
      add.disabled = busy;
      box.append(add);
      if (ed) {
        const form = element('div', 'preset-editor');
        const err = element('p', 'panel-meta preset-error', ed.error);
        err.setAttribute('role', 'alert');
        err.hidden = !ed.error;
        const actions = element('div', 'login-form');
        const save = button('button-primary', t('common.save'), () => this.savePresetFile());
        save.disabled = busy || ed.saving;
        actions.append(save);
        if (ed.file) actions.append(button('button-subtle', t('common.delete'), () => this.deletePresetFile(ed.file)));
        actions.append(button('button-subtle', t('common.close'), () => { this.presetEditor = null; this.renderPresets(); }));
        form.append(element('strong', '', ed.file || t('presets.files.new')), area.dom, err, actions);
        box.append(form);
      }
      this.presets.append(box);
      area?.refresh();
    }

    async openPresetFile(file) {
      try {
        const data = await api('/api/presets/user/' + encodeURIComponent(file));
        this.presetEditor = { file, yaml: text(data?.yaml, 300000), error: '' };
      } catch (error) {
        this.presetEditor = { file, yaml: '', error: t('presets.files.readFailed', { message: error.message }) };
      }
      this.renderPresets();
    }

    async savePresetFile() {
      const ed = this.presetEditor;
      if (!ed || ed.saving || !state.authorized) return;
      ed.saving = true;
      ed.error = '';
      this.renderPresets();
      try {
        await api('/api/presets/user' + (ed.file ? '/' + encodeURIComponent(ed.file) : ''), { method: ed.file ? 'PUT' : 'POST', body: JSON.stringify({ yaml: ed.yaml }) });
        this.presetEditor = null;
        await this.loadPresets();
        this.done.textContent = t('presets.files.saved');
        this.done.hidden = false;
        return;
      } catch (error) {
        ed.error = t('presets.files.saveFailed', { message: error.message });
      } finally {
        ed.saving = false;
      }
      this.renderPresets();
    }

    async deletePresetFile(file) {
      if (!state.authorized) return;
      if (!(await askConfirm(t('presets.files.delete.confirm', { file }), { ok: t('common.delete'), danger: true }))) return;
      try {
        await api('/api/presets/user/' + encodeURIComponent(file), { method: 'DELETE' });
        this.presetEditor = null;
      } catch (error) {
        if (this.presetEditor) this.presetEditor.error = t('presets.files.deleteFailed', { message: error.message });
      }
      await this.loadPresets();
    }

    async applyPreset(preset, inputs) {
      if (this.mutating || !state.authorized) return;
      const ref = presetRef(preset, inputs);
      const values = typeof ref === 'string' ? '' : ' (' + Object.values(ref.params).join(', ') + ')';
      if (!(await askConfirm(t('presets.apply.confirm', { preset: preset.label + values, sandbox: JSON.stringify(this.sandbox.name), hosts: preset.hosts.join(', ') }), { ok: t('presets.apply') }))) return;
      await this.post('/presets/apply', ref, t('presets.apply.done'));
      this.loadPresets();
    }

    async removePreset(p) {
      if (this.mutating || !state.authorized) return;
      if (!(await askConfirm(t('presets.remove.confirm', { preset: p.label, sandbox: JSON.stringify(this.sandbox.name) }), { ok: t('policy.remove'), danger: true }))) return;
      await this.post('/presets/remove', p.params ? { id: p.id, params: p.params } : p.id, t('presets.remove.done'), (result) => {
        if (result?.kept?.length) this.done.textContent = t('presets.remove.kept', { rules: result.kept.map((k) => text(k, 200)).join(', ') });
      });
      this.loadPresets();
    }

    // The latest revision's network_policies, to edit, and the rest read-only.
    async loadYAML() {
      const token = (this.yamlLoading = {});
      this.renderYAML();
      let ed;
      try {
        const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/policy/yaml');
        const yaml = text(data?.yaml, 300000);
        ed = { version: Number(data?.version) || 0, yaml, original: yaml, fixed: text(data?.fixed, 20000), error: '' };
      } catch (error) {
        ed = { version: 0, yaml: '', original: '', fixed: '', error: t('policy.yaml.loadFailed', { message: error.message }) };
      }
      if (this.disposed || this.yamlLoading !== token) return;
      this.yamlLoading = null;
      this.yamlEd = ed;
      this.renderYAML();
      this.renderSegments();
    }

    // Reads the YAML again, asking first when that drops edits.
    async reloadYAML() {
      if (this.yamlEd?.busy) return;
      if (this.yamlDirty() && !(await askConfirm(t('policy.yaml.reloadConfirm'), { ok: t('policy.yaml.reload') }))) return;
      this.yamlEd = null;
      this.loadYAML();
    }

    // The YAML view: its actions over the editor, the editor filling the
    // rest, the fixed settings folded under it. Built once per loaded YAML
    // (re-renders only update it, so the editor keeps the focus).
    renderYAML() {
      const ed = this.yamlEd;
      const area = keepCode(this, 'yamlCode', ed, () => ({ value: ed.yaml, readOnly: !ed.version, label: t('policy.yaml.label'), className: 'policy-yaml-editor', onChange: (v) => { ed.yaml = v; this.renderSegments(); this.renderYAMLState(); } }));
      const fixedCode = keepCode(this, 'yamlFixed', ed?.fixed ? ed : null, () => ({ value: ed.fixed, readOnly: true, label: t('policy.yaml.fixed') }));
      if (this.yamlBuilt !== (ed || this.yamlLoading)) {
        this.yamlBuilt = ed || this.yamlLoading;
        this.yamlBox.replaceChildren();
        this.yamlParts = null;
        if (!ed) {
          if (this.yamlLoading) this.yamlBox.append(element('p', 'panel-meta', t('common.loading')));
          return;
        }
        const head = element('div', 'policy-yaml-head');
        const title = element('h4', 'policy-current-title');
        const check = bind(button('button-primary', '', () => this.saveYAML()), 'policy.yaml.check');
        const reload = bind(button('button-subtle', '', () => this.reloadYAML()), 'policy.yaml.reload');
        head.append(title, check, reload);
        // 読み込み直す is right above it.
        const stale = element('p', 'panel-meta preset-error policy-yaml-stale');
        stale.setAttribute('role', 'alert');
        const staleText = stale;
        const err = element('p', 'panel-meta preset-error');
        err.setAttribute('role', 'alert');
        this.yamlBox.append(head, stale, err, area.dom);
        if (fixedCode) {
          const fixed = element('details', 'policy-yaml-fixed');
          fixed.append(bind(element('summary'), 'policy.yaml.fixed'), fixedCode.dom);
          // Measured once it shows.
          fixed.addEventListener('toggle', () => fixedCode.refresh());
          this.yamlBox.append(fixed);
        }
        this.yamlParts = { title, check, reload, stale, staleText, err };
      }
      this.renderYAMLState();
      if (!this.yamlBox.hidden && area && this.yamlCode === area) area.refresh();
    }

    renderYAMLState() {
      const ed = this.yamlEd;
      const parts = this.yamlParts;
      if (!ed || !parts) return;
      const title = ed.version ? t('policy.yaml.title', { version: ed.version }) : t('policy.yaml');
      if (parts.title.textContent !== title) parts.title.textContent = title;
      parts.check.disabled = !state.authorized || ed.busy || !ed.version;
      parts.reload.disabled = Boolean(ed.busy);
      if (parts.err.textContent !== ed.error) parts.err.textContent = ed.error;
      parts.err.hidden = !ed.error;
      // The policy moved on since it was read (an approval, another edit).
      const now = this.snapshot?.policy?.version || 0;
      const stale = Boolean(ed.version && now > ed.version && !ed.busy);
      if (stale && !this.yamlDirty() && this.yamlAutoFor !== now) { this.yamlAutoFor = now; this.yamlEd = null; this.loadYAML(); return; }
      const staleText = stale ? t('error.policy_yaml_stale', { loaded: ed.version, version: now }) : '';
      if (parts.staleText.textContent !== staleText) parts.staleText.textContent = staleText;
      parts.stale.hidden = !stale || ed.error === staleText;
    }

    // Checked first (the server says what changes), then applied once the
    // owner agrees; refused when the policy moved on since it was loaded.
    async saveYAML() {
      const ed = this.yamlEd;
      if (!ed || ed.busy || this.mutating || !state.authorized) return;
      const path = '/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/policy/yaml';
      let confirm = '';
      const send = (apply) => api(path, { method: 'POST', body: JSON.stringify(confirm ? { version: ed.version, yaml: ed.yaml, apply, confirm } : { version: ed.version, yaml: ed.yaml, apply }), timeoutMs: 40000 });
      ed.busy = true;
      ed.error = '';
      this.renderYAML();
      try {
        let res;
        try {
          res = await send(false);
        } catch (error) {
          if (!error.confirm || !(await confirmProviderHosts(error))) throw error;
          confirm = error.confirm;
          res = await send(false);
        }
        const rules = (list) => (Array.isArray(list) ? list.slice(0, 100) : []).map((r) => text(r?.name, 200) + (Array.isArray(r?.hosts) && r.hosts.length ? ' (' + r.hosts.slice(0, 8).map((h) => text(h, 270)).join(', ') + ')' : ''));
        const diff = res?.diff || {};
        const changes = [['added', diff.added], ['removed', diff.removed], ['changed', diff.changed]].filter(([, l]) => Array.isArray(l) && l.length)
          .map(([k, l]) => t('policy.yaml.' + k, { rules: rules(l).join(', ') })).join('\n');
        // Rules that reach the host's loopback (proposal_guard.go).
        const gateways = (Array.isArray(res?.gateways) ? res.gateways.slice(0, 20) : []).filter((g) => g && typeof g.rule === 'string')
          .map((g) => t('policy.yaml.gateway', { rule: text(g.rule, 200), host: text(g.host, 253) || t('policy.anyHost'), gateway: text(g.gateway, 253) })).join('\n');
        if (!(await askConfirm(t('policy.yaml.confirm', { sandbox: JSON.stringify(this.sandbox.name), changes }) + (gateways ? '\n\n' + gateways : ''), { ok: t('policy.yaml.apply') }))) return;
        this.mutating = true;
        const applied = await send(true);
        // The applied YAML is read again (its new version).
        this.yamlEd = null;
        ed.busy = false;
        this.loadYAML();
        // A change made elsewhere during the save may have been replaced.
        this.done.textContent = applied?.warningCode ? window.IwatoI18n.serverError(applied.warningCode, applied.params, text(applied.warning, 600)) : t('policy.yaml.done');
        this.done.hidden = false;
        clearTimeout(this.doneTimer);
        this.doneTimer = setTimeout(() => { this.done.hidden = true; }, 30000);
        this.actedAt = Date.now();
      } catch (error) {
        ed.error = error.message;
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        ed.busy = false;
        if (this.mutating) {
          this.mutating = false;
          if (!this.disposed) { this.snapshot = null; this.render(); this.refresh(); }
        }
        this.renderYAML();
        this.renderSegments();
      }
    }

    toggleHistory() {
      this.history.hidden = !this.history.hidden;
      this.revision = null;
      this.revisions = null;
      this.historyAt = this.snapshot?.policy?.version;
      if (!this.history.hidden) this.loadHistory();
      this.render();
      this.renderHistory();
    }

    // The latest revisions (as `openshell policy list` shows them).
    async loadHistory(version = 0) {
      const epoch = this.generation;
      const path = '/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/policy/history' + (version ? '/' + version : '');
      try {
        const data = await api(path);
        if (this.disposed || epoch !== this.generation || this.history.hidden) return;
        if (version) this.revision = { version, policy: this.validatePolicy(data) };
        else this.revisions = (Array.isArray(data?.revisions) ? data.revisions.slice(0, 50) : []).map((r) => ({ version: Number(r?.version) || 0, status: text(r?.status, 20), active: r?.active === true, createdAt: Number(r?.createdAt), loadError: text(r?.loadError, 500) }));
      } catch (error) {
        if (this.disposed || this.history.hidden) return;
        this.showActionError(t('policy.history.failed', { message: error.message }));
      }
      this.renderHistory();
    }

    renderHistory() {
      this.history.replaceChildren();
      if (this.history.hidden) return;
      const status = { pending: t('policy.history.pending'), loaded: t('policy.change.applied'), failed: t('policy.history.failedState'), superseded: t('policy.history.superseded') };
      this.history.append(element('h4', 'policy-current-title', t('policy.history.title')));
      if (!this.revisions) { this.history.append(element('p', 'panel-meta', t('common.loading'))); return; }
      for (const r of this.revisions) {
        const row = element('div', 'policy-current-head policy-revision');
        const shown = this.revision?.version === r.version;
        const label = t('policy.version', { version: r.version }) + ' · ' + (status[r.status] || r.status) + (r.active ? t('policy.history.active') : '') + ' · ' + formatTime(r.createdAt) + (r.loadError ? ' · ' + r.loadError : '');
        row.append(element('p', 'panel-meta', label), button('button-subtle', shown ? t('common.close') : t('policy.history.view'), () => {
          if (shown) { this.revision = null; this.renderHistory(); } else this.loadHistory(r.version);
        }));
        this.history.append(row);
        if (shown && this.revision.policy) {
          const view = element('div', 'policy-revision-rules');
          view.append(...this.ruleNodes(this.revision.policy, false));
          this.history.append(view);
        }
      }
    }

    // Status errors come with each refresh; an owner action's error stays
    // until the next action, or the refresh that follows it would erase it.
    showError(message) {
      this.statusError = message || '';
      this.paintError();
    }

    showActionError(message, raw = '') {
      this.actionError = message || '';
      this.actionRaw = message ? raw || '' : '';
      this.paintError();
    }

    paintError() {
      const message = [this.actionError, this.statusError].filter(Boolean).join('\n');
      const key = message + '\0' + (this.actionRaw || '');
      if (this.errorKey !== key) {
        this.errorKey = key;
        errorContent(this.error, message, this.actionRaw);
      }
      this.error.hidden = !message;
    }

    clear(message = '') {
      this.generation += 1;
      this.snapshot = null;
      this.showError(message);
      this.render();
    }

    async refresh() {
      if (this.disposed || !state.authorized || document.hidden || this.mutating) return;
      if (this.refreshing) return this.refreshing;
      const epoch = this.generation;
      this.refreshing = (async () => {
        try {
          const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/proposals');
          if (this.disposed || epoch !== this.generation) return;
          this.snapshot = this.validate(data);
          this.showError(this.snapshot.error);
          // An open history follows new revisions.
          const version = this.snapshot.policy?.version;
          if (!this.history.hidden && version !== this.historyAt) { this.historyAt = version; this.loadHistory(); }
        } catch (error) {
          if (this.disposed || epoch !== this.generation) return;
          this.clear(t('policy.loadFailed', { message: error.message }));
          if (error.status === 401 || error.status === 403) invalidateSession();
        } finally {
          this.refreshing = null;
          if (!this.disposed) this.render();
        }
      })();
      this.render();
      return this.refreshing;
    }

    // Approving asks first; the request carries the proposal's ID and the
    // digest of the version listed, so the server decides on the version
    // the owner was shown or refuses (proposal_changed): the page then
    // reads the list again and asks about the new version. A rejected
    // proposal is approved as proposed: OpenShell edits pending ones only.
    async decide(item, action, rejected = false) {
      const error = await this.decideOnce(item, action, rejected);
      if (error?.code !== 'proposal_changed' || this.disposed) return;
      const fresh = (rejected ? this.snapshot?.rejected : this.snapshot?.pending)?.find((p) => p.id === item.id);
      if (fresh && fresh.digest !== item.digest) await this.decideOnce(fresh, action, rejected);
    }

    async decideOnce(item, action, rejected) {
      if (this.mutating || this.refreshing || !state.authorized || !['approve', 'reject'].includes(action)) return;
      let scope = null;
      const ps = !rejected && action === 'approve' ? item.providerScope : null;
      if (ps && !ps.reason) {
        // The provider's scope instead of a policy rule (ADR 0006).
        policyFormFor = '';
        $('approve-form').hidden = true;
        showApproveHead(item);
        showScopeGrant(item, ps);
        const yes = await askConfirm(t('policy.scope.question', { sandbox: JSON.stringify(this.sandbox.name) }), { ok: t('policy.scope.ok') });
        hideApproveHead();
        if (!yes) return;
        return this.post('/proposals/scope', { requestId: item.id, digest: item.digest }, t('policy.scope.done'), (result) => {
          if (result && result.closed) this.done.textContent = t('policy.scope.closed');
        });
      }
      if (action === 'approve') {
        // Internal addresses spelled out, and a provider's token riding along.
        const warnings = item.allowedIps.map((x) => [t('policy.approve.internal', { host: x.host || t('policy.anyHost'), ips: allowedIPsLabel(x.host, x.ips) }), '']);
        if (ps) warnings.push([t('approve.providerWarning', { provider: approveLogic.providerName(ps) }), ps.reason ? t('approve.providerWarning.why', { reason: ps.reason }) : '']);
        const name = JSON.stringify(this.sandbox.name);
        const question = rejected ? t('policy.approve.rejectedQuestion', { name }) + '\n' + item.summary + '\n\n' + t('policy.approve.rejectedEffect') : t('policy.approve.question', { name });
        const answer = await askApprove(question, item, { asProposed: rejected, warnings });
        if (!answer) return;
        scope = answer.scope;
      }
      const body = { requestId: item.id, digest: item.digest };
      if (scope) body.scope = scope;
      return this.post('/proposals/' + action, body, action === 'approve' ? t('policy.approve.done') : t('policy.reject.done'));
    }

    // Opens a rule and brings it into view once listed (the provider panel
    // sends the owner here to enforce an audited endpoint).
    focusRule(name, note) {
      this.opened.add(name);
      this.focusing = name;
      if (this.view !== 'list') { this.view = 'list'; this.applyView(); }
      this.done.textContent = note;
      this.done.hidden = !note;
      clearTimeout(this.doneTimer);
      this.render();
    }

    dispose() {
      this.disposed = true;
      this.clear();
      for (const key of ['yamlCode', 'yamlFixed', 'presetCode']) keepCode(this, key, null);
    }
  }

  // URLs and file paths printed in the terminal (its history), newest
  // first. Links open only on an explicit tap, in a new tab without opener
  // or referrer; paths open in the editor (openPath).
  class LinksPanel {
    constructor(sandbox, terminalOf, openPath) {
      this.sandbox = sandbox;
      this.terminalOf = terminalOf;
      this.openPath = openPath;
      this.root = element('section', 'side-panel links-panel');
      bindAttribute(this.root, 'aria-label', 'links.label');
      this.root.hidden = true;
      this.refreshButton = bind(button('button-subtle', '', () => this.refresh()), 'common.reload');
      this.closeButton = bind(button('button-subtle', '', () => this.onClose()), 'common.close');
      this.list = element('ul', 'link-list');
      this.status = element('p', 'panel-meta', '');
      this.root.append(panelHeader({ key: 'links.title' }, this.refreshButton, this.closeButton), this.status, this.list);
    }

    show() { this.root.hidden = false; this.refresh(); }
    hide() { this.root.hidden = true; }

    refresh() {
      const terminal = this.terminalOf();
      this.list.replaceChildren();
      if (!terminal) { this.status.textContent = t('links.disconnected'); return; }
      {
        const links = bufferLinks(terminal).slice(0, 30);
        for (const { url, path, text } of links) {
          const item = element('li');
          let link;
          if (path) {
            link = element('button', 'terminal-link terminal-path', text);
            link.type = 'button';
            link.addEventListener('click', () => this.openPath(path));
            link.title = t('links.pathTitle');
          } else {
            link = element('a', 'terminal-link', url);
            linkAnchor(link, url);
          }
          item.append(link);
          this.list.append(item);
        }
        this.status.textContent = links.length ? (androidApp() ? t('links.share') : t('links.open')) : t('links.none');
      }
    }

    dispose() { this.root.remove(); }
  }

  // The files tab (web/files.js).
  const FilesPanel = window.IwatoFiles.panel({ element, button, panelHeader, api, askConfirm });
  // The editor tab (web/editor.js).
  const EditorPanel = window.IwatoEditor.panel({ element, button, panelHeader, api, askConfirm });

  // The standard image in the settings: where it comes from and since when,
  // and for a moving tag (edge) an update button (image_update.go). Iwato
  // pulls only a missing image by itself.
  const imagePhases = new Set(['pulling', 'updated', 'current', 'failed']);
  function imageView(d) {
    const u = d && typeof d.update === 'object' && d.update ? d.update : {};
    return {
      available: d?.available === true, image: text(d?.image, 300), source: d?.source === 'pull' ? 'pull' : 'build', moves: d?.moves === true, present: d?.present === true,
      created: shortTime(d?.created), at: shortTime(d?.at),
      update: { phase: imagePhases.has(u.phase) ? u.phase : '', message: text(u.message, 1000), detail: text(u.detail, 1000) }
    };
  }
  // pullImage starts a pull (or joins the one running) and waits for its
  // result, calling progress with the seconds so far.
  async function pullImage(progress) {
    await api('/api/images/update', { method: 'POST', body: '{}' });
    const started = Date.now();
    for (;;) {
      const info = imageView(await api('/api/images'));
      if (info.update.phase !== 'pulling') return info;
      progress?.(Math.round((Date.now() - started) / 1000));
      await new Promise((resolve) => setTimeout(resolve, 2000));
    }
  }
  // An update's result in the page's words (failed: with its cause).
  function imageUpdateText(u) {
    if (u.phase === 'failed') return u.detail ? t('settings.image.phase.failed', { detail: u.detail }) : u.message;
    return t('settings.image.phase.' + u.phase);
  }
  class ImageSettings {
    constructor() {
      this.root = element('div', 'image-settings');
      this.root.hidden = true;
      this.status = element('p', 'panel-meta');
      this.status.setAttribute('role', 'status');
    }

    async refresh() {
      try {
        this.data = imageView(await api('/api/images'));
        this.render();
        if (this.data.update.phase === 'pulling' && !this.busy) this.update();
      } catch (error) {
        if (error.status === 401 || error.status === 403) invalidateSession();
      }
    }

    render() {
      const d = this.data;
      this.root.hidden = !d.available;
      if (!d.available) return;
      const source = d.source === 'build' ? t('settings.image.built') : d.moves ? t('settings.image.edge') : t('settings.image.release');
      const times = d.present ? [d.created && t('settings.image.created', { time: d.created }), d.at && t(d.source === 'build' ? 'settings.image.builtAt' : 'settings.image.pulledAt', { time: d.at })].filter(Boolean).join(' · ') || t('settings.image.noTimes') : t('settings.image.missing');
      const nodes = [element('h4', 'files-head', t('settings.image.title')), element('p', 'panel-meta image-name', d.image), element('p', 'panel-meta', source), element('p', 'panel-meta image-times', times)];
      if (d.moves) {
        this.button = button('button-subtle', t('settings.image.update'), () => this.update());
        this.button.disabled = !!this.busy;
        nodes.push(element('div', 'login-form'));
        nodes.at(-1).append(this.button);
      } else if (d.source === 'pull') nodes.push(element('p', 'panel-meta', t('settings.image.latest')));
      if (!this.busy) this.status.textContent = d.update.phase && d.update.phase !== 'pulling' ? imageUpdateText(d.update) : '';
      nodes.push(this.status);
      this.root.replaceChildren(...nodes);
    }

    async update() {
      if (this.busy || !state.authorized) return;
      this.busy = true;
      if (this.button) this.button.disabled = true;
      this.status.textContent = t('image.pulling');
      try {
        const info = await pullImage((s) => { this.status.textContent = t('image.pullingFor', { seconds: s }); });
        this.busy = false;
        this.data = info;
        this.render();
      } catch (error) {
        this.status.textContent = t('settings.image.updateFailed', { message: error.message });
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        this.busy = false;
        if (this.button) this.button.disabled = false;
      }
    }
  }

  // The startup checks in the settings (GET /api/checks runs them again).
  class CheckSettings {
    constructor() {
      this.root = element('div', 'check-settings');
      this.list = element('ul', 'check-list');
      this.status = element('p', 'panel-meta');
      this.status.setAttribute('role', 'status');
      this.button = button('button-subtle', '', () => this.refresh());
      bind(this.button, 'checks.recheck');
      const head = element('h4', 'files-head');
      bind(head, 'settings.checks.title');
      const actions = element('div', 'login-form');
      actions.append(this.button);
      this.root.append(head, this.list, this.status, actions);
    }

    async refresh() {
      if (this.busy) return;
      this.busy = true;
      this.button.disabled = true;
      this.status.textContent = t('settings.checks.running');
      try {
        const data = await api('/api/checks');
        this.checks = Array.isArray(data?.checks) ? data.checks.slice(0, 50) : [];
        this.status.textContent = '';
        this.render();
        // The engine may have come back (or gone): the banner follows.
        refreshSandboxes().catch(() => {});
      } catch (error) {
        this.status.textContent = t('settings.checks.failed', { message: error.message });
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        this.busy = false;
        this.button.disabled = false;
      }
    }

    render() {
      const rows = [];
      for (const c of (this.checks || []).map(checkView).filter(Boolean)) {
        const row = element('li', 'check-row');
        row.dataset.level = c.level;
        const mark = element('span', 'check-mark', { ok: '✓', info: '·', warn: '!', fail: '✗' }[c.level]);
        mark.setAttribute('role', 'img');
        mark.setAttribute('aria-label', t('checks.level.' + c.level));
        const body = element('div', 'check-body');
        const line = element('p', 'check-line');
        line.append(element('strong', '', c.name), element('span', '', c.detail ? ': ' + c.detail : ''));
        body.append(line);
        if (c.hint) body.append(element('p', 'panel-meta check-hint', c.hint));
        row.append(mark, body);
        rows.push(row);
      }
      this.list.replaceChildren(...rows);
    }
  }

  // The sandboxes' time zone (/api/timezone): "" follows this PC's.
  class TimezoneSettings {
    constructor() {
      this.root = element('div', 'timezone-settings');
      this.title = element('h4', 'files-head');
      this.select = element('select', 'text-input timezone-select');
      this.select.id = 'timezone-select';
      this.status = element('p', 'panel-meta');
      this.status.setAttribute('role', 'status');
      this.select.addEventListener('change', () => this.save());
      this.root.append(this.title, this.select, this.status);
      this.data = { timezone: '', host: '' };
    }

    async refresh() {
      try {
        const d = await api('/api/timezone');
        this.data = { timezone: text(d?.timezone, 64), host: text(d?.host, 64) };
        this.render();
      } catch (error) {
        this.status.textContent = error.message;
        if (error.status === 401 || error.status === 403) invalidateSession();
      }
    }

    render() {
      bind(this.title, 'settings.timezone.title');
      this.select.setAttribute('aria-label', t('settings.timezone.title'));
      const option = (value, label) => { const o = element('option', '', label); o.value = value; return o; };
      let zones = [];
      try { zones = Intl.supportedValuesOf('timeZone'); } catch (_) { /* older browsers: the specials only */ }
      zones = zones.filter((z) => z !== 'UTC' && z !== 'Etc/UTC');
      const current = this.data.timezone;
      if (current && current !== 'UTC' && !zones.includes(current)) zones.unshift(current);
      const group = element('optgroup');
      group.label = t('settings.timezone.other');
      group.append(...zones.map((z) => option(z, z)));
      this.select.replaceChildren(option('', t('settings.timezone.host', { zone: this.data.host || '?' })), option('UTC', 'UTC'), group);
      this.select.value = current;
      if (this.saved) this.status.textContent = t('settings.timezone.saved');
    }

    async save() {
      const timezone = this.select.value;
      this.select.disabled = true;
      this.status.textContent = '';
      this.saved = false;
      try {
        await api('/api/timezone', { method: 'POST', body: JSON.stringify({ timezone }) });
        this.data.timezone = timezone;
        this.saved = true;
        this.status.textContent = t('settings.timezone.saved');
      } catch (error) {
        this.status.textContent = t('settings.timezone.failed', { message: error.message });
        this.select.value = this.data.timezone;
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        this.select.disabled = false;
      }
    }
  }

  // Port exposure: the settings and the Ports tab (web/ingress.js).
  const ingressUI = window.IwatoIngress.make({ element, button, panelHeader, api, askConfirm });
  let settingsPanel = null;
  let resetSection = null;
  let imageSettings = null;
  let checkSettings = null;
  let sessionSettings = null;
  let updateSettings = null;
  let timezoneSettings = null;
  function openSettings() {
    if (!settingsPanel) {
      imageSettings = new ImageSettings();
      checkSettings = new CheckSettings();
      updateSettings = new updateUI.UpdateSection(updateDialog);
      timezoneSettings = new TimezoneSettings();
      $('settings-body').append(window.IwatoI18n.languageSection(element), timezoneSettings.root, updateSettings.root, checkSettings.root, imageSettings.root);
      settingsPanel = new ingressUI.SettingsPanel($('settings-body'));
      settingsPanel.onAuth = invalidateSession;
      settingsPanel.onChange = () => { for (const card of cards.values()) if (card.root.dataset.tab === 'ports') card.ports.refresh(); };
      // The logins (web/sessions.js), then delete everything at the bottom (web/reset.js).
      sessionSettings = new (window.IwatoSessions.make({ element, button, api, askConfirm, onEnded: invalidateSession, onAuth: invalidateSession }).SessionSection)();
      $('settings-body').append(sessionSettings.root);
      resetSection = new (window.IwatoReset.make({ element, button, api, onEnded: invalidateSession, onAuth: invalidateSession }).ResetSection)();
      $('settings-body').append(resetSection.root);
    }
    $('settings-panel').hidden = false;
    updateSettings.refresh();
    timezoneSettings.refresh();
    checkSettings.refresh();
    imageSettings.refresh();
    settingsPanel.refresh();
    sessionSettings.refresh();
    closeMenu();
    $('settings-panel').scrollIntoView?.({ block: 'nearest' });
  }

  // Terminal history as plain text: select with the OS (long-press on
  // phones) or copy everything. Only textContent is used.
  class TextPanel {
    constructor(sandbox, terminalOf) {
      this.sandbox = sandbox;
      this.terminalOf = terminalOf;
      this.root = element('section', 'side-panel text-panel');
      bindAttribute(this.root, 'aria-label', 'text.label');
      this.root.hidden = true;
      this.copyButton = bind(button('button-primary', '', () => this.copyAll()), 'text.copyAll');
      this.refreshButton = bind(button('button-subtle', '', () => this.refresh()), 'common.reload');
      this.closeButton = bind(button('button-subtle', '', () => this.onClose()), 'common.close');
      this.status = element('p', 'panel-meta', '');
      this.pre = element('pre', 'file-text terminal-text');
      this.root.append(panelHeader({ key: 'text.title' }, this.copyButton, this.refreshButton, this.closeButton), this.status, this.pre);
    }

    show() { this.root.hidden = false; this.refresh(); }
    hide() { this.root.hidden = true; this.pre.textContent = ''; }

    refresh() {
      const terminal = this.terminalOf();
      if (!terminal) { this.pre.textContent = ''; this.status.textContent = t('text.disconnected'); return; }
      this.pre.textContent = bufferText(terminal).slice(-1048576);
      this.status.textContent = t('text.status');
      this.pre.scrollTop = this.pre.scrollHeight;
    }

    async copyAll() {
      const text = this.pre.textContent;
      if (!text) return;
      try {
        await navigator.clipboard.writeText(text);
        this.status.textContent = t('text.copied', { count: text.length });
      } catch (_) {
        this.status.textContent = t('text.copyFailed');
      }
    }

    dispose() { this.root.remove(); }
  }

  // Removing a sandbox requires typing its name. Data volumes are kept unless
  // the owner explicitly opts in; the sandbox leaves the configuration.
  class DeletePanel {
    constructor(card) {
      this.card = card;
      this.root = element('section', 'side-panel delete-panel');
      bindAttribute(this.root, 'aria-label', 'delete.label');
      this.root.hidden = true;
      this.note = bind(element('p', 'panel-meta'), 'delete.note');
      const purgeLabel = element('label', 'delete-purge');
      this.purge = element('input');
      this.purge.type = 'checkbox';
      purgeLabel.append(this.purge, bind(element('span'), 'delete.purge'));
      this.form = element('form', 'login-form');
      this.input = element('input', 'text-input');
      this.input.type = 'text';
      this.input.setAttribute('autocapitalize', 'off');
      bindAttribute(this.input, 'aria-label', 'delete.confirmLabel');
      this.submit = bind(element('button', 'button button-disconnect'), 'delete.submit');
      this.submit.type = 'submit';
      this.form.append(this.input, this.submit);
      this.form.addEventListener('submit', (event) => { event.preventDefault(); this.run(); });
      this.status = element('p', 'panel-meta', '');
      this.closeButton = bind(button('button-subtle', '', () => { this.close(); this.card.restoreFocus(); }), 'common.close');
      this.root.append(panelHeader({ key: 'delete.title' }, this.closeButton), this.note, purgeLabel, this.form, this.status);
    }

    toggle() {
      if (this.root.hidden) {
        this.input.placeholder = t('delete.confirmPlaceholder', { name: this.card.sandbox.name });
        this.root.hidden = false;
        if (!touchScreen()) this.input.focus();
      } else this.close();
    }

    close() {
      this.root.hidden = true;
      this.input.value = '';
      this.purge.checked = false;
      this.status.textContent = '';
    }

    async run() {
      const name = this.card.sandbox.name;
      if (!state.authorized || this.busy) return;
      if (String(this.input.value || '').trim() !== name) { this.status.textContent = t('delete.mismatch'); return; }
      this.busy = true;
      this.submit.disabled = true;
      this.status.textContent = t('delete.deleting');
      // Deleting gives up a start in progress (the server cancels it).
      this.card.deleting = true;
      this.card.disconnect(t('card.deleting'));
      try {
        await api('/api/sandboxes/' + encodeURIComponent(name) + '/delete', { method: 'POST', body: JSON.stringify({ confirm: name, purge: !!this.purge.checked }), timeoutMs: 310000 });
        this.close();
        await refreshSandboxes();
      } catch (error) {
        this.status.textContent = t('delete.failed', { message: error.message });
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        this.busy = false;
        this.card.deleting = false;
        this.submit.disabled = false;
      }
    }

    dispose() { this.root.remove(); }
  }

  // The rules a recreate (or the start after one) could not put back.
  function carryFailures(list) {
    // The reason in the page's language when the server named it.
    return (Array.isArray(list) ? list : []).filter((f) => f && typeof f.rule === 'string').slice(0, 50).map((f) => ({
      rule: text(f.rule, 200),
      error: window.IwatoRefusals.message(f.code, f.params, {
        ambiguities: Array.isArray(f.ambiguities) ? f.ambiguities.filter((a) => a && typeof a.rule === 'string').slice(0, 20).map(validAmbiguity) : null,
        gateway: validGatewayAmbiguities(f.gatewayAmbiguities)
      }, text(f.error, 1000))
    }));
  }
  function carryMessage(failed) {
    return t('recreate.notCarried', { rules: failed.map((f) => f.rule + ' — ' + f.error).join(' / ') });
  }

  // Recreating a sandbox (作り直す): OpenShell deletes it and Iwato creates
  // it again under the same name from the current image, with its volumes,
  // providers, network rules and settings (recreate.go). It asks for the
  // name, as a delete does, and says what is kept and what is lost.
  class RecreatePanel {
    constructor(card) {
      this.card = card;
      this.root = element('section', 'side-panel recreate-panel');
      bindAttribute(this.root, 'aria-label', 'recreate.label');
      this.root.hidden = true;
      this.note = bind(element('p', 'panel-meta'), 'recreate.note');
      const list = (title, items) => {
        const box = element('div', 'recreate-list');
        const ul = element('ul');
        for (const item of items) ul.append(bind(element('li'), item));
        box.append(bind(element('strong'), title), ul);
        return box;
      };
      this.kept = list('recreate.kept', ['recreate.kept.volumes', 'recreate.kept.providers', 'recreate.kept.rules', 'recreate.kept.settings']);
      this.lost = list('recreate.lost', ['recreate.lost.files', 'recreate.lost.proposals']);
      // A moving standard image (edge) can be pulled first (image_update.go).
      this.pull = element('label', 'scope-choice recreate-pull');
      this.pullBox = element('input');
      this.pullBox.type = 'checkbox';
      this.pull.append(this.pullBox, bind(element('span'), 'recreate.pull'));
      this.pull.hidden = true;
      this.gpu = element('label', 'scope-choice recreate-gpu');
      this.gpuBox = element('input');
      this.gpuBox.type = 'checkbox';
      this.gpu.append(this.gpuBox, bind(element('span'), 'create.gpu'));
      this.form = element('form', 'login-form');
      this.input = element('input', 'text-input');
      this.input.type = 'text';
      this.input.setAttribute('autocapitalize', 'off');
      bindAttribute(this.input, 'aria-label', 'delete.confirmLabel');
      this.submit = bind(element('button', 'button button-disconnect'), 'recreate.submit');
      this.submit.type = 'submit';
      this.form.append(this.input, this.submit);
      this.form.addEventListener('submit', (event) => { event.preventDefault(); this.run(); });
      this.status = element('p', 'panel-meta', '');
      this.status.setAttribute('role', 'status');
      this.closeButton = bind(button('button-subtle', '', () => { this.close(); this.card.restoreFocus(); }), 'common.close');
      this.root.append(panelHeader({ key: 'recreate.title' }, this.closeButton), this.note, this.kept, this.lost, this.pull, this.gpu, this.form, this.status);
    }

    toggle() {
      if (this.root.hidden) {
        this.input.placeholder = t('delete.confirmPlaceholder', { name: this.card.sandbox.name });
        // Shown without a GPU too while the sandbox has one, to turn it off.
        this.gpuBox.checked = this.card.sandbox.gpu === true;
        this.gpu.hidden = state.gpu === false && !this.gpuBox.checked;
        this.root.hidden = false;
        if (!touchScreen()) this.input.focus();
        api('/api/images').then((d) => { this.pull.hidden = !imageView(d).moves; }, () => {});
      } else this.close();
    }

    close() {
      if (this.busy) return;
      this.root.hidden = true;
      this.input.value = '';
      this.pullBox.checked = false;
      this.status.textContent = '';
    }

    async run() {
      const card = this.card;
      const name = card.sandbox.name;
      if (!state.authorized || this.busy) return;
      if (String(this.input.value || '').trim() !== name) { this.status.textContent = t('delete.mismatch'); return; }
      this.busy = true;
      this.submit.disabled = true;
      // The pull comes first; when it fails, nothing is recreated.
      if (!this.pull.hidden && this.pullBox.checked) {
        this.status.textContent = t('image.pulling');
        try {
          const info = await pullImage((s) => { this.status.textContent = t('image.pullingFor', { seconds: s }); });
          if (info.update.phase === 'failed') throw new Error(info.update.message);
        } catch (error) {
          this.status.textContent = t('recreate.notRecreated', { message: error.message });
          if (error.status === 401 || error.status === 403) invalidateSession();
          this.busy = false;
          this.submit.disabled = false;
          return;
        }
      }
      this.status.textContent = t('recreate.recreating');
      // Like a delete, it gives up a start in progress (the server cancels it).
      card.recreating = true;
      card.message.hidden = true;
      card.disconnect(t('card.recreating'));
      card.update(card.sandbox);
      let failed = [];
      try {
        const result = await api('/api/sandboxes/' + encodeURIComponent(name) + '/recreate', { method: 'POST', body: JSON.stringify(this.gpu.hidden ? { confirm: name } : { confirm: name, gpu: this.gpuBox.checked }), timeoutMs: 610000 });
        if (result.ok !== true) throw new Error(t('recreate.unknownResult'));
        failed = carryFailures(result.failed);
        this.busy = false;
        if (failed.length) this.status.textContent = t('recreate.doneExcept', { failures: carryMessage(failed) });
        else this.close();
        card.autoArmed = true;
      } catch (error) {
        failed = error.failed || [];
        this.status.textContent = t('recreate.failed', { message: error.message }) + (failed.length ? ' ' + carryMessage(failed) : '');
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        this.busy = false;
        card.recreating = false;
        this.submit.disabled = false;
      }
      await refreshSandboxes().catch(() => {});
      if (!card.disposed) { card.update(card.sandbox); card.autoConnect(); }
    }

    dispose() { this.root.remove(); }
  }

  // A sandbox's providers: attach and detach. A change reaches the
  // processes started afterwards (OpenShell puts the variables into new
  // processes only), so after one on a running sandbox the panel offers to
  // restart it now; あとで leaves the card marked 再起動待ち.
  class ProviderPanel {
    constructor(card) {
      this.card = card;
      this.root = element('section', 'side-panel provider-panel');
      bindAttribute(this.root, 'aria-label', 'card.providers.label');
      this.root.hidden = true;
      this.note = bind(element('p', 'panel-meta'), 'card.providers.note');
      this.status = element('p', 'panel-meta', '');
      this.status.setAttribute('role', 'status');
      this.list = element('div', 'volume-list');
      this.restartBox = element('div', 'restart-choice');
      this.restartBox.setAttribute('role', 'group');
      this.restartBox.hidden = true;
      this.restartText = element('p', 'panel-meta', '');
      this.restartNow = bind(button('button-start', '', () => this.restart()), 'card.providers.restartNow');
      this.restartLater = bind(button('button-subtle', '', () => { this.restartBox.hidden = true; }), 'card.providers.later');
      const choice = element('div', 'login-form');
      choice.append(this.restartNow, this.restartLater);
      this.restartBox.append(this.restartText, choice);
      // A rule that would carry the provider's credential uninspected:
      // named, with the fix (remove those endpoints, then attach).
      this.conflictBox = element('div', 'conflict-choice');
      this.conflictBox.setAttribute('role', 'group');
      this.conflictBox.hidden = true;
      this.closeButton = bind(button('button-subtle', '', () => { this.close(); this.card.restoreFocus(); }), 'common.close');
      this.root.append(panelHeader({ key: 'nav.providers' }, this.closeButton), this.note, this.restartBox, this.conflictBox, this.status, this.list);
    }

    toggle() { if (this.root.hidden) this.open(); else this.close(); }

    async open() {
      this.root.hidden = false;
      this.restartBox.hidden = true;
      await this.refresh();
    }

    close() {
      this.root.hidden = true;
      this.restartBox.hidden = true;
      this.status.textContent = '';
      if (this.applyTimer) clearTimeout(this.applyTimer);
      this.applyTimer = 0;
    }

    async refresh(done = '') {
      this.status.textContent = t('nav.loading');
      try {
        const error = await loadProviders();
        this.status.textContent = error ? t('common.listFailed', { message: error }) : done;
        this.render();
      } catch (error) {
        this.status.textContent = t('common.listFailed', { message: error.message });
        if (error.status === 401 || error.status === 403) invalidateSession();
      }
    }

    render() {
      const name = this.card.sandbox.name;
      const attached = providerView.list.filter((p) => p.attached.includes(name));
      const nodes = [];
      // Providers of profiles Iwato does not ship (imported with
      // OpenShell's CLI, or of a profile Iwato no longer has) go under
      // その他, so that they can still be detached.
      for (const profile of providerView.profiles.concat([{ id: '', label: t('providers.other') }])) {
        const list = providerView.list.filter((p) => profile.id ? p.kind === profile.id : !providerView.profiles.some((x) => x.id === p.kind));
        if (!list.length) continue;
        const group = element('section', 'volume-group');
        const head = element('h3', 'volume-sandbox', profile.label);
        group.append(head);
        // One per profile: they set the same variables (the server checks
        // the other ones' variables).
        const holder = profile.id ? attached.find((p) => p.kind === profile.id) : null;
        for (const p of list) {
          const row = element('div', 'volume-row provider-row');
          const on = p.attached.includes(name);
          row.dataset.attached = String(on);
          const info = element('div', 'volume-info');
          const head = element('span', 'volume-path', p.name);
          const applying = p.applying.find((a) => a.sandbox === name);
          if (applying && applying.state === 'pending') head.append(element('span', 'tag-applying', t('card.providers.applying')));
          info.append(head, element('span', 'panel-meta', on ? t('card.providers.attached') : holder ? t('card.providers.detachOther', { name: holder.name }) : (profile.id ? '' : p.profile + ' · ') + p.keys.join(', ')));
          if (applying && applying.state !== 'pending') info.append(applyNote(applying, false));
          if (profile.oauth && needsReauthorization(p.refresh)) info.append(element('span', 'panel-meta provider-refresh', t('card.providers.reauthorize')));
          const action = on ? button('button-disconnect', t('policy.remove'), () => this.change(p, false)) : button('button-subtle', t('card.providers.attach'), () => this.change(p, true));
          action.disabled = !state.authorized || (!on && !!holder);
          row.append(info, action);
          group.append(row);
        }
        if (profile.guide && holder) group.append(guideBox(t('card.providers.guide'), profile.guide));
        nodes.push(group);
      }
      if (!nodes.length) nodes.push(element('p', 'panel-meta', t('card.providers.none')));
      this.list.replaceChildren(...nodes);
      // While the sandbox applies a change, look again (反映待ち goes away).
      if (this.applyTimer) clearTimeout(this.applyTimer);
      this.applyTimer = 0;
      if (!this.root.hidden && providerView.list.some((p) => p.applying.some((a) => a.sandbox === name && a.state === 'pending'))) {
        this.applyTimer = setTimeout(async () => {
          this.applyTimer = 0;
          if (this.root.hidden) return;
          try { await loadProviders(); this.render(); } catch (_) { /* the next open reads it again */ }
        }, 3000);
      }
    }

    // Why the provider cannot be attached yet, with the fixes: endpoints
    // that would carry its credential uninspected (removed), and endpoints
    // its rule would make ambiguous (an audited one switched to enforce,
    // here with the requests it allows now or in the policy tab from the
    // ones it logged; or removed).
    showConflicts(p, conflicts = [], ambiguities = []) {
      const list = element('ul', 'conflict-list');
      for (const c of conflicts) {
        list.append(element('li', '', t('card.providers.conflict', { rule: c.rule, where: c.host + ':' + c.ports.join(','), mode: c.mode }) + (c.binaries.length ? t('card.providers.conflict.binaries', { binaries: c.binaries.join(', ') }) : '')));
      }
      for (const a of ambiguities) {
        const allows = a.access ? (accessLabel(a.access) || a.access) : a.allows.map((r) => (r.method === '*' ? t('policy.method.all') : r.method) + ' ' + pathLabel(r.path)).join(', ');
        const item = element('li', 'conflict-ambiguity', t('card.providers.ambiguity', { rule: a.rule, endpoint: a.endpoint, details: window.IwatoI18n.list(window.IwatoRefusals.details(a)) }) + (allows ? t('card.providers.allows', { allows }) : ''));
        item.dataset.rule = a.rule;
        list.append(item);
      }
      const choice = element('div', 'login-form');
      const enforceable = ambiguities.length > 0 && ambiguities.every((a) => a.fix === 'enforce');
      if (enforceable) {
        choice.append(button('button-start', t('card.providers.fix.enforce'), () => this.fix(p, 'enforce')));
        const audited = ambiguities.find((a) => a.audited);
        if (audited) {
          choice.append(button('button-subtle', t('card.providers.fix.fromLog'), () => {
            this.conflictBox.hidden = true;
            this.close();
            this.card.showPolicyRule(audited.rule, t('card.providers.fix.fromLog.note', { rule: audited.rule, host: audited.host, provider: p.name }));
          }));
        }
      }
      choice.append(button(enforceable ? 'button-subtle' : 'button-start', t('card.providers.fix.remove'), () => this.fix(p, 'remove')));
      choice.append(button('button-subtle', t('providers.oauth.cancel'), () => { this.conflictBox.hidden = true; }));
      const intro = [];
      if (conflicts.length) intro.push(t('card.providers.conflicts.intro', { name: p.name }));
      if (ambiguities.length) intro.push(t('card.providers.ambiguities.intro', { name: p.name }) + t(enforceable ? 'card.providers.ambiguities.enforce' : 'card.providers.ambiguities.remove'));
      this.conflictBox.replaceChildren(element('p', 'panel-meta', intro.join('\n')), list, choice);
      this.conflictBox.hidden = false;
    }

    // Endpoints that would carry its credential beyond its scope: removed
    // (the token says which), or attached anyway.
    showBeyond(p, error) {
      const beyond = error.beyond || [];
      const list = element('ul', 'conflict-list');
      for (const b of beyond) {
        list.append(element('li', '', t('card.providers.beyond', { rule: b.rule, where: b.host + ':' + b.ports.join(',') }) + (b.binaries.length ? t('card.providers.conflict.binaries', { binaries: b.binaries.join(', ') }) : '')));
      }
      const choice = element('div', 'login-form');
      choice.append(
        button('button-start', t('card.providers.fix.remove'), () => this.fix(p, 'remove', error.confirm)),
        button('button-disconnect', t('card.providers.fix.anyway'), () => this.change(p, true, error.confirm)),
        button('button-subtle', t('providers.oauth.cancel'), () => { this.conflictBox.hidden = true; }));
      this.conflictBox.replaceChildren(element('p', 'panel-meta', beyond.length ? t('card.providers.beyond.intro', { name: p.name }) : error.message), list, choice);
      this.conflictBox.hidden = false;
    }

    // A refused attach the panel offers fixes for: shown, true.
    showRefusal(p, error, confirm) {
      const fixable = error.conflicts?.length || error.ambiguities?.length;
      if (!fixable && !(error.confirm && !confirm)) return false;
      if (fixable) this.showConflicts(p, error.conflicts || [], error.ambiguities || []);
      else this.showBeyond(p, error);
      this.status.textContent = t('card.providers.notYet', { name: p.name });
      return true;
    }

    async fix(p, mode = 'remove', confirm = '') {
      const name = this.card.sandbox.name;
      if (!state.authorized || this.busy) return;
      this.conflictBox.hidden = true;
      this.status.textContent = mode === 'enforce' ? t('card.providers.enforcing') : t('card.providers.removing');
      try {
        await api('/api/sandboxes/' + encodeURIComponent(name) + '/providers/fix', { method: 'POST', body: JSON.stringify(confirm ? { provider: p.name, mode, confirm } : { provider: p.name, mode }), timeoutMs: 70000 });
      } catch (error) {
        this.status.textContent = t(mode === 'enforce' ? 'card.providers.enforceFailed' : 'card.providers.removeFailed', { message: error.message });
        if (error.status === 401 || error.status === 403) invalidateSession();
        return;
      }
      await this.change(p, true);
    }

    async change(p, attach, confirm = '') {
      const name = this.card.sandbox.name;
      if (!state.authorized || this.busy) return;
      this.busy = true;
      this.restartBox.hidden = true;
      this.conflictBox.hidden = true;
      this.status.textContent = t(attach ? 'card.providers.attaching' : 'card.providers.detaching', { name: p.name });
      try {
        const result = await api('/api/sandboxes/' + encodeURIComponent(name) + '/providers/' + (attach ? 'attach' : 'detach'), { method: 'POST', body: JSON.stringify(confirm ? { provider: p.name, confirm } : { provider: p.name }), timeoutMs: 70000 });
        const done = t(attach ? 'card.providers.attachedDone' : 'card.providers.detachedDone', { name: p.name }) + (result.restart === true || this.card.sandbox.status === 'running' ? '' : t('card.providers.nextStart'));
        if (result.restart === true) {
          this.restartText.textContent = t('card.providers.restartText');
          this.restartBox.hidden = false;
        }
        await refreshSandboxes();
        await this.refresh(done);
      } catch (error) {
        try {
          if (attach && this.showRefusal(p, error, confirm)) return;
        } catch (_) { /* the error as it came, below */ }
        errorContent(this.status, t(attach ? 'card.providers.attachFailed' : 'card.providers.removeFailed', { message: error.message }), error.raw);
        if (error.status === 401 || error.status === 403) invalidateSession();
      } finally {
        this.busy = false;
      }
    }

    async restart() {
      this.restartBox.hidden = true;
      this.status.textContent = t('card.providers.restarting');
      const failure = await this.card.restart();
      if (!failure) { this.status.textContent = t('card.providers.restarted'); return; }
      this.status.textContent = t('card.providers.restartFailed', { message: failure });
      // Still running with the change pending: the choice stays.
      if (this.card.sandbox.status === 'running' && this.card.sandbox.restartPending === true) this.restartBox.hidden = false;
    }

    dispose() { this.root.remove(); }
  }

  class SandboxCard {
    constructor(sandbox) {
      this.sandbox = sandbox;
      this.connection = 'disconnected';
      this.desired = false;
      this.ended = false; // the session exited or was refused (resume leaves it)
      this.disposed = false;
      this.starting = false;
      this.stopping = false;
      this.generation = 0;
      this.attempt = 0;
      this.socket = null;
      this.reconnectTimer = null;
      this.connectTimer = null;
      this.resizeFrame = null;
      this.revealFrame = null;
      this.heldHeight = 0;
      this.terminal = null;
      this.size = null;
      this.lastSize = '';
      // Terminal sessions of the sandbox; one is shown (and attached) at a time.
      this.termNames = ['main'];
      this.termMulti = false;
      // Sessions started by an older version: restarting them gets its fixes.
      this.termOutdated = [];
      // Sessions from before a provider change (they lack it).
      this.providerOutdated = [];
      // Terminals that only a restart of the sandbox renews (an older
      // terminal daemon, or main in a sandbox without one).
      this.sandboxRestart = ['main'];
      this.termName = savedTerm(sandbox.name);
      this.termNew = false;
      // When each stopped terminal stopped, as last seen here (stoppedUnseen).
      this.seenStops = {};
      this.split = savedSplit();
      this.splitFits = false;
      this.createElements();
      this.update(sandbox);
      this.setConnection('disconnected', t('conn.none'));
      // Side by side, the size follows the divider and the window once they
      // rest, not on every step.
      this.observer = new ResizeObserver(() => {
        if (this.root.dataset.split !== 'true') { this.scheduleFit(); return; }
        clearTimeout(this.splitFitTimer);
        this.splitFitTimer = setTimeout(() => this.scheduleFit(), 150);
      });
      this.observer.observe(this.host);
      this.cardObserver = new ResizeObserver(() => this.checkSplitFits());
      this.cardObserver.observe(this.root);
    }

    createElements() {
      this.root = element('article', 'terminal-card');
      this.root.setAttribute('aria-label', t('card.label', { name: this.sandbox.name }));
      const header = element('div', 'card-header');
      const titleWrap = element('div', 'card-title-wrap');
      const icon = element('span', 'sandbox-icon', '›_');
      icon.setAttribute('aria-hidden', 'true');
      const titleText = element('div');
      titleText.style.minWidth = '0';
      this.title = element('h2', 'card-title');
      titleText.append(this.title);
      titleWrap.append(icon, titleText);
      this.badge = element('span', 'badge');
      // A provider changed while it ran: restarting gives every terminal the change.
      this.restartBadge = bind(element('span', 'badge badge-restart'), 'card.restartPending');
      bindAttribute(this.restartBadge, 'title', 'card.restartPending.title');
      this.restartBadge.hidden = true;
      this.gpuBadge = element('span', 'badge badge-gpu', 'GPU');
      bindAttribute(this.gpuBadge, 'title', 'card.gpu.title');
      this.gpuBadge.hidden = true;
      header.append(titleWrap, this.badge, this.restartBadge, this.gpuBadge);
      this.detail = element('p', 'card-detail');
      const toolbar = element('div', 'card-toolbar');
      // Only the step the sandbox needs next stays in view; the rarer views
      // and actions sit in the ⋯ menu at the end of the tab row.
      const actions = element('div', 'card-actions');
      // An item that opens nothing in its place gives the keyboard back.
      const item = (label, run) => button('button-subtle card-menu-item', label, () => { this.closeActionMenu(); run(); this.restoreFocus(); });
      this.startButton = button('button-start', t('card.start'), () => this.start());
      this.stopButton = item(t('card.stop'), () => this.stop());
      this.connectButton = button('button-primary', t('card.connect'), () => this.connect());
      this.reconnectButton = bind(item('', () => this.connect()), 'card.reconnect');
      this.deleteButton = bind(item('', () => this.deletion.toggle()), 'card.delete');
      this.recreateButton = bind(item('', () => this.recreation.toggle()), 'card.recreate');
      this.providerButton = bind(item('', () => this.providers.toggle()), 'card.providers');
      this.moreButton = button('button-subtle button-icon', '⋯', () => { if (this.actionMenu.hidden) this.openActionMenu(); else { this.closeActionMenu(); this.restoreFocus(); } });
      bindAttribute(this.moreButton, 'aria-label', 'card.more');
      this.moreButton.setAttribute('aria-haspopup', 'menu');
      this.moreButton.setAttribute('aria-expanded', 'false');
      this.actionMenu = element('div', 'card-menu');
      this.actionMenu.setAttribute('role', 'menu');
      this.actionMenu.hidden = true;
      for (const [key, b] of Object.entries({ start: this.startButton, stop: this.stopButton, connect: this.connectButton, reconnect: this.reconnectButton, delete: this.deleteButton, recreate: this.recreateButton, providers: this.providerButton })) b.dataset.action = key;
      for (const b of [this.stopButton, this.reconnectButton, this.deleteButton, this.recreateButton, this.providerButton]) b.setAttribute('role', 'menuitem');
      // The rarer views open from the menu, first; the open one is marked
      // there and shows as the current tab.
      this.viewItems = {};
      for (const key of ['links', 'text', 'files']) {
        const view = bind(button('button-subtle card-menu-item card-menu-view', '', () => {
          this.closeActionMenu();
          this.selectTab(key);
          if (!touchScreen()) this.viewTab.focus();
        }), 'tab.' + key);
        view.setAttribute('role', 'menuitem');
        view.dataset.view = key;
        this.viewItems[key] = view;
        this.actionMenu.append(view);
      }
      const separator = element('div', 'card-menu-sep');
      separator.setAttribute('role', 'separator');
      this.actionMenu.append(separator, this.reconnectButton, this.providerButton, this.stopButton, this.recreateButton, this.deleteButton);
      actions.append(this.startButton, this.connectButton);
      toolbar.append(actions);
      const surface = element('div', 'terminal-surface');
      this.surface = surface;
      this.host = element('div', 'terminal-host');
      this.placeholder = element('div', 'terminal-placeholder');
      const mark = element('div', 'placeholder-mark', '›_');
      mark.setAttribute('aria-hidden', 'true');
      this.placeholderTitle = element('strong', '', t('card.placeholder.running.title'));
      this.placeholderText = element('p', '', t('card.placeholder.running.text'));
      this.placeholder.append(mark, this.placeholderTitle, this.placeholderText);
      surface.append(this.host, this.placeholder);
      const footer = element('div', 'card-footer');
      this.connectionLabel = element('div', 'connection-label');
      this.connectionLabel.setAttribute('role', 'status');
      const connectionDot = element('span', 'small-dot');
      connectionDot.setAttribute('aria-hidden', 'true');
      this.connectionText = element('span', 'connection-text');
      this.connectionLabel.append(connectionDot, this.connectionText);
      // The size shows only for a moment after it changes.
      this.dimensions = element('span', 'dimensions', '— × —');
      this.dimensions.hidden = true;
      this.fullscreenButton = bind(item('', () => this.toggleFullscreen()), 'card.fullscreen');
      bindAttribute(this.fullscreenButton, 'title', 'card.fullscreen.title');
      // iPhone Safari cannot show an element fullscreen; do not offer a dead button.
      this.fullscreenButton.hidden = !document.fullscreenEnabled;
      this.footer = footer;
      // The connection's state (when not connected) and a new size show
      // over the terminal, so they never change its size.
      this.pasteStatus = element('span', 'paste-status');
      this.pasteStatus.setAttribute('role', 'status');
      this.pasteStatus.hidden = true;
      surface.append(this.connectionLabel, this.dimensions, this.pasteStatus);
      footer.append(this.createKeyBar());
      this.fullscreenButton.setAttribute('role', 'menuitem');
      this.actionMenu.append(this.fullscreenButton);
      // The text stays selectable (to copy it); only the button closes it.
      this.message = element('div', 'card-message');
      this.message.setAttribute('role', 'alert');
      this.message.hidden = true;
      this.messageText = element('span', 'card-message-text');
      const closeMessage = element('button', 'card-message-close', '×');
      closeMessage.type = 'button';
      bindAttribute(closeMessage, 'title', 'common.close');
      bindAttribute(closeMessage, 'aria-label', 'common.close');
      closeMessage.addEventListener('click', () => { this.message.hidden = true; });
      this.message.append(this.messageText, closeMessage);
      // The terminal is the main view. Everything else takes its place, so
      // the page never scrolls: the most used ones as tabs, the rest from
      // the ⋯ menu at the end of the row; approvals get a badge while they wait.
      this.tabs = element('div', 'card-tabs');
      this.tabList = element('div', 'card-tab-list');
      this.tabList.setAttribute('role', 'tablist');
      this.tabButtons = {};
      for (const key of ['terminal', 'policy', 'editor', 'ports']) {
        const tab = button('card-tab', t('tab.' + key), () => this.chooseTab(key));
        tab.className = 'card-tab';
        tab.setAttribute('role', 'tab');
        tab.badge = element('span', 'tab-badge');
        tab.badge.hidden = true;
        tab.append(tab.badge);
        this.tabButtons[key] = tab;
        this.tabList.append(tab);
      }
      // The view a menu item opened (choosing it again reloads it).
      this.viewTab = button('card-tab', '', () => this.selectTab(this.root.dataset.tab));
      this.viewTab.className = 'card-tab';
      this.viewTab.setAttribute('role', 'tab');
      this.viewTab.setAttribute('aria-selected', 'true');
      this.viewTab.hidden = true;
      this.tabList.append(this.viewTab);
      // The editor and the terminal side by side, offered on wide cards.
      this.splitButton = button('button-subtle button-icon split-toggle', '', () => this.toggleSplit());
      this.splitButton.append(element('span', 'split-icon'));
      bindAttribute(this.splitButton, 'aria-label', 'card.split');
      bindAttribute(this.splitButton, 'title', 'card.split');
      this.splitButton.setAttribute('aria-pressed', String(this.split.on));
      this.splitButton.hidden = true;
      this.tabs.append(this.tabList, this.splitButton, this.moreButton, this.actionMenu);
      this.divider = element('div', 'split-divider');
      this.divider.setAttribute('role', 'separator');
      this.divider.setAttribute('aria-orientation', 'vertical');
      this.divider.setAttribute('aria-valuemin', '0');
      this.divider.setAttribute('aria-valuemax', '100');
      bindAttribute(this.divider, 'aria-label', 'card.split.divider');
      this.divider.tabIndex = 0;
      this.root.addEventListener('focusin', (event) => {
        if (this.surface.contains(event.target)) this.lastPane = 'terminal';
        else if (this.editor?.root.contains(event.target)) this.lastPane = 'editor';
      });
      this.divider.addEventListener('pointerdown', (event) => this.dragSplit(event));
      this.divider.addEventListener('keydown', (event) => {
        const step = event.key === 'ArrowLeft' ? -0.05 : event.key === 'ArrowRight' ? 0.05 : 0;
        if (!step) return;
        event.preventDefault();
        this.setSplitAt(this.split.at + step);
        saveSplit(this.split);
      });
      // The agent's proposals waiting, over whatever view is open.
      this.proposalBar = element('div', 'proposal-bar');
      this.proposalBar.setAttribute('role', 'status');
      this.proposalBar.hidden = true;
      this.proposalText = element('span', 'proposal-bar-text');
      this.proposalReview = bind(button('button-start', '', () => this.reviewProposals()), 'proposalBar.review');
      this.proposalLater = bind(button('button-subtle', '', () => { this.proposalsLater = new Set(this.waitingProposals().map((p) => p.id)); this.renderProposalBar(); }), 'proposalBar.later');
      this.proposalBar.append(this.proposalText, this.proposalReview, this.proposalLater);
      this.proposalsLater = new Set();
      this.termTabs = element('div', 'term-tabs');
      this.termTabs.setAttribute('role', 'tablist');
      bindAttribute(this.termTabs, 'aria-label', 'tab.terminal');
      this.termTabs.hidden = true;
      this.termNotice = element('div', 'term-notice');
      this.termNotice.setAttribute('role', 'status');
      this.termNoticeText = element('span', 'term-notice-text');
      this.termRestartButton = button('button-start', t('term.restart'), () => this.termNoticeAction());
      this.termNotice.append(this.termNoticeText, this.termRestartButton);
      this.termNotice.hidden = true;
      this.root.append(header, this.detail, this.tabs, toolbar, this.termTabs, surface, footer, this.message, this.termNotice, this.proposalBar, this.divider);
      this.setSplitAt(this.split.at);
      this.policy = new PolicyPanel(this.sandbox);
      this.policy.onChange = () => { this.renderTabs(); renderPendingTotal(); this.followPolicy(); };
      this.selectTab('terminal');
      this.links = new LinksPanel(this.sandbox, () => this.terminal, (found) => this.openPath(found));
      this.deletion = new DeletePanel(this);
      this.recreation = new RecreatePanel(this);
      this.providers = new ProviderPanel(this);
      this.text = new TextPanel(this.sandbox, () => this.terminal);
      this.files = new FilesPanel(this.sandbox);
      this.editor = new EditorPanel(this.sandbox, () => state.selected === this.sandbox.name);
      this.ports = new ingressUI.PortsPanel(this.sandbox, openSettings);
      for (const panel of [this.links, this.text, this.files, this.editor, this.ports]) panel.onClose = () => this.selectTab('terminal');
      // Closing the editor side by side leaves the terminal alone.
      this.editor.onClose = () => { if (this.root.dataset.split === 'true') this.toggleSplit(false, 'terminal'); else this.selectTab('terminal'); };
      this.root.append(this.policy.root, this.links.root, this.deletion.root, this.text.root, this.providers.root, this.files.root, this.editor.root, this.ports.root, this.recreation.root);
      grid.append(this.root);
      this.nav = button('nav-sandbox', '', () => {
        selectCard(this);
        this.loadTerms();
        this.selectTab('terminal');
        closeMenu();
        if (touchScreen()) return;
        if (this.terminal) this.terminal.focus();
        else this.connectButton.disabled ? this.startButton.focus() : this.connectButton.focus();
      });
      this.nav.className = 'nav-sandbox';
      this.navDot = element('span', 'small-dot');
      this.navDot.setAttribute('aria-hidden', 'true');
      this.navTitle = element('span', 'nav-name');
      const arrow = element('span', 'nav-arrow', '›');
      arrow.setAttribute('aria-hidden', 'true');
      this.navStop = element('span', 'stopped-mark');
      bindAttribute(this.navStop, 'title', 'nav.stopped');
      this.navStop.hidden = true;
      this.nav.append(this.navDot, this.navTitle, this.navStop, arrow);
      $('sandbox-nav').append(this.nav);
    }

    openActionMenu() {
      this.actionMenu.hidden = false;
      this.moreButton.setAttribute('aria-expanded', 'true');
      // Closes on the next click elsewhere (Esc: escapeTarget).
      this.menuDismiss = (event) => {
        if (!this.actionMenu.contains(event.target) && !this.moreButton.contains(event.target)) this.closeActionMenu();
      };
      document.addEventListener('pointerdown', this.menuDismiss, true);
    }

    closeActionMenu() {
      this.actionMenu.hidden = true;
      this.moreButton.setAttribute('aria-expanded', 'false');
      if (!this.menuDismiss) return;
      document.removeEventListener('pointerdown', this.menuDismiss, true);
      this.menuDismiss = null;
    }

    // The policy tab with one rule open and in view, and a note on top.
    showPolicyRule(rule, note) {
      this.selectTab('policy');
      this.policy.focusRule(rule, note);
    }

    // Side by side: the editor on the left (its file tree at the card's
    // edge, as in the tab), the terminal on the right; while the terminal
    // or the editor tab is chosen. The other tabs and views take its place.
    termShown() { return this.root.dataset.tab === 'terminal' || this.root.dataset.split === 'true'; }

    splitShown(key = this.root.dataset.tab) {
      return this.split.on && this.splitFits && this.sandbox.status !== 'not-created' && (key === 'terminal' || key === 'editor');
    }

    toggleSplit(on = !this.split.on, key) {
      this.split.on = on;
      saveSplit(this.split);
      this.splitButton.setAttribute('aria-pressed', String(on));
      const tab = this.root.dataset.tab;
      this.selectTab(key || (on && tab !== 'editor' ? 'terminal' : tab));
    }

    setSplitAt(at) {
      const width = this.root.clientWidth || 0;
      // Both panes keep a usable width (unknown before the card is laid out).
      const min = width > 2 * splitMinPane ? splitMinPane / width : 0.1;
      this.split.at = Math.min(1 - min, Math.max(min, at));
      this.root.style.setProperty('--split', (this.split.at * 100).toFixed(2) + '%');
      this.divider.setAttribute('aria-valuenow', String(Math.round(this.split.at * 100)));
    }

    dragSplit(event) {
      if (event.button !== 0) return;
      event.preventDefault();
      const divider = this.divider;
      divider.setPointerCapture?.(event.pointerId);
      this.root.classList.add('split-dragging');
      const move = (e) => {
        const box = this.root.getBoundingClientRect();
        if (box.width) this.setSplitAt((e.clientX - box.left) / box.width);
      };
      const end = () => {
        divider.removeEventListener('pointermove', move);
        divider.removeEventListener('pointerup', end);
        divider.removeEventListener('pointercancel', end);
        divider.removeEventListener('lostpointercapture', end);
        this.root.classList.remove('split-dragging');
        saveSplit(this.split);
      };
      divider.addEventListener('pointermove', move);
      divider.addEventListener('pointerup', end);
      divider.addEventListener('pointercancel', end);
      // Also when the divider goes away mid-drag (the card got narrow).
      divider.addEventListener('lostpointercapture', end);
    }

    // A card too narrow for both falls back to the tabs.
    checkSplitFits() {
      const fits = this.root.clientWidth >= splitMinWidth;
      if (fits) this.setSplitAt(this.split.at);
      if (fits === this.splitFits) return;
      this.splitFits = fits;
      this.splitButton.hidden = !fits;
      if (this.split.on) this.layoutTabs(this.root.dataset.tab);
    }

    // Shows what the chosen tab shows, without moving the keyboard.
    layoutTabs(key) {
      const split = this.splitShown(key);
      this.root.dataset.split = String(split);
      for (const [name, tab] of Object.entries(this.tabButtons)) tab.setAttribute('aria-selected', String(name === key || (split && (name === 'terminal' || name === 'editor'))));
      if (!this.editor) return split;
      if (split && this.editor.root.hidden) {
        // Shown beside the terminal, it does not take the keyboard.
        const active = document.activeElement;
        this.editor.show();
        if (document.activeElement !== active) {
          if (active && active !== document.body) active.focus();
          else document.activeElement?.blur?.();
        }
      } else if (!split && key !== 'editor' && !this.editor.root.hidden) this.editor.hide();
      if (split) this.scheduleFit();
      return split;
    }

    // A tab chosen by the owner. Side by side, its pane takes the keyboard
    // even from the other pane (a click does not focus a button in Safari
    // or Firefox on macOS, so the keyboard would stay where it was).
    chooseTab(key) {
      this.selectTab(key);
      if (!this.splitShown(key) || touchScreen()) return;
      if (key === 'editor') this.editor.focusTarget().focus();
      else if (this.terminal) this.terminal.focus();
    }

    selectTab(key) {
      const changed = this.root.dataset.tab !== key;
      this.root.dataset.tab = key;
      const split = this.layoutTabs(key);
      if (this.nav) this.renderTermTabs();
      for (const [name, view] of Object.entries(this.viewItems)) {
        if (name === key) view.setAttribute('aria-current', 'true');
        else view.removeAttribute('aria-current');
      }
      this.viewTab.hidden = !this.viewItems[key];
      if (this.viewItems[key]) this.viewTab.textContent = t('tab.' + key);
      if (this.proposalBar) this.renderProposalBar();
      const panels = { links: this.links, text: this.text, files: this.files, editor: this.editor, ports: this.ports };
      for (const [name, panel] of Object.entries(panels)) if (name !== key && !(split && name === 'editor') && panel && !panel.root.hidden) panel.hide();
      if (split && key === 'editor') {
        // The terminal stays; the editor gets the keyboard (as its tab does).
        if (!touchScreen()) this.editor.focusTarget().focus();
      } else if (key === 'terminal') {
        // Choosing the tab shown already gives the terminal the keyboard.
        if (!changed) { this.focusUnlessTouch(); return; }
        // The surface had no size while hidden; fit once it is laid out again.
        this.scheduleFit();
        this.focusUnlessTouch();
      } else if (panels[key]) {
        // Choosing the tab again reloads it (new terminal output).
        panels[key].show();
      } else if (changed) {
        this.policy.refresh();
      }
      this.policyShown = key === 'policy';
      this.followPolicy();
    }

    // The policy tab follows new proposals with the sandbox list, which
    // reads every card's policy every 10 s (each read makes the server
    // read the draft, the policy and the logs), and every 2 s while it
    // shows and a change waits for the sandbox to load it or the owner
    // just acted.
    followPolicy() {
      const fast = Boolean(this.policyShown && this.policy.following());
      if (fast === Boolean(this.policyTimer)) return;
      clearInterval(this.policyTimer);
      this.policyTimer = null;
      if (!fast) return;
      this.policyTimer = setInterval(() => {
        if (!document.hidden) this.policy.refresh();
        // Following ends with no read to notice it.
        this.followPolicy();
      }, 2000);
    }

    // Terminals that stopped changing (notifier.go) and that the owner has
    // not looked at since, here: the sandbox shown with that terminal, the
    // page visible. They are marked on the sidebar entry and their tabs.
    // While the owner uses some page no push tells it (presence.go).
    stoppedUnseen() {
      const stopped = this.sandbox.stopped || {};
      if (state.selected === this.sandbox.name && this.termShown() && !document.hidden && stopped[this.termName]) this.seenStops[this.termName] = stopped[this.termName];
      const unseen = Object.keys(stopped).filter((name) => this.seenStops[name] !== stopped[name]);
      this.navStop.hidden = !unseen.length;
      return unseen;
    }

    renderTermTabs() {
      const unseen = this.stoppedUnseen();
      const names = this.termNames.includes(this.termName) ? this.termNames : [...this.termNames, this.termName];
      this.termTabs.hidden = !this.termMulti || this.sandbox.status !== 'running';
      const nodes = names.map((name) => {
        const tab = element('span', 'term-tab');
        tab.setAttribute('aria-selected', String(name === this.termName));
        const label = termLabel(name);
        const select = button('term-select', label, () => this.switchTerm(name));
        select.setAttribute('role', 'tab');
        select.title = t('term.name', { name: label });
        if (this.termOutdated.includes(name)) {
          select.append(element('span', 'term-outdated', t('term.outdated')));
          select.title += t('term.outdated.title');
        }
        if (this.providerOutdated.includes(name)) {
          select.append(element('span', 'term-outdated term-provider', t('card.restartPending')));
          select.title += t('term.providerOutdated.title');
        }
        if (unseen.includes(name)) {
          select.append(element('span', 'stopped-mark'));
          select.title += t('term.stopped.title');
        }
        const close = button('term-close', '×', () => this.killTerm(name));
        close.title = t('term.close', { name: label });
        close.setAttribute('aria-label', close.title);
        close.disabled = !state.authorized;
        tab.append(select, close);
        return tab;
      });
      const add = button('term-add', '＋', () => this.newTerm());
      add.title = t('term.new');
      add.setAttribute('aria-label', add.title);
      add.disabled = !state.authorized || names.length >= maxTerms;
      this.termTabs.replaceChildren(...nodes, add);
      // A provider change outweighs a version note: it changes what works.
      const providerStale = this.providerOutdated.includes(this.termName);
      const whole = this.sandboxRestart.includes(this.termName);
      this.termNotice.hidden = this.sandbox.status !== 'running' || (!this.termOutdated.includes(this.termName) && !providerStale);
      this.termNotice.dataset.kind = providerStale ? 'provider' : 'version';
      const label = t('term.name', { name: termLabel(this.termName) });
      this.termNoticeText.textContent = !providerStale
        ? t(whole ? 'term.notice.outdatedSandbox' : 'term.notice.outdated', { name: label })
        : t(whole ? 'term.notice.providerSandbox' : 'term.notice.provider', { name: label });
      this.termRestartButton.textContent = whole ? t('term.restartSandbox') : t('term.restart');
      this.termRestartButton.disabled = !state.authorized;
    }

    // Lists the running sessions; false when that failed.
    async loadTerms() {
      if (!state.authorized || this.sandbox.status !== 'running') return false;
      try {
        const result = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/terminals');
        if (this.disposed || !Array.isArray(result.terminals)) return false;
        this.termNames = result.terminals.filter((name) => typeof name === 'string' && termNamePattern.test(name));
        this.termOutdated = Array.isArray(result.outdated) ? result.outdated.filter((name) => typeof name === 'string' && termNamePattern.test(name)) : [];
        this.providerOutdated = Array.isArray(result.providerOutdated) ? result.providerOutdated.filter((name) => typeof name === 'string' && termNamePattern.test(name)) : [];
        this.sandboxRestart = Array.isArray(result.restartSandbox) ? result.restartSandbox.filter((name) => typeof name === 'string' && termNamePattern.test(name)) : ['main'];
        this.termMulti = result.multi === true;
        this.renderTermTabs();
        return true;
      } catch (_) {
        return false;
      }
    }

    switchTerm(name, isNew = false) {
      this.termNew = isNew;
      if (name !== this.termName) {
        this.termName = name;
        if (name) saveTerm(this.sandbox.name, name);
        this.renderTermTabs();
        // Also after the shown session ended: the other one may be running.
        if (this.desired || this.ended) this.connect();
      }
      if (this.root.dataset.tab !== 'terminal') this.selectTab('terminal');
      else this.focusUnlessTouch();
    }

    // Shows a session and attaches it (a notification click asked for it).
    openTerm(name) {
      if (this.sandbox.status !== 'running') { this.selectTab('terminal'); return; }
      this.switchTerm(name);
      if (!this.desired) this.connect();
    }

    newTerm() {
      // The smallest free number (1 is main, when it has ended).
      const used = new Set(this.termNames);
      let n = 2;
      while (used.has(String(n))) n += 1;
      const name = used.has('main') ? String(n) : 'main';
      this.termNames = [...this.termNames, name];
      this.switchTerm(name, true);
      if (!this.desired) this.connect();
    }

    async killTerm(name) {
      if (!state.authorized) return;
      if (!(await askConfirm(t('term.close.confirm', { name: termLabel(name) }), { ok: t('term.close.ok'), danger: true }))) return;
      const current = name === this.termName;
      const index = this.termNames.indexOf(name);
      if (current) this.stopSocket();
      try {
        await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/terminals/' + encodeURIComponent(name) + '/kill', { method: 'POST', body: '{}' });
      } catch (error) {
        this.showError(t('term.close.failed', { message: error.message }));
        if (error.status === 401 || error.status === 403) invalidateSession();
      }
      await this.loadTerms();
      if (current && !this.disposed) this.showTermAfter(index);
    }

    // Ends the shown session and starts it again with the current version.
    async restartTerm() {
      const name = this.termName;
      if (!state.authorized) return;
      if (!(await askConfirm(t('term.restart.confirm', { name: termLabel(name) }), { ok: t('term.restart.ok'), danger: true }))) return;
      if (this.disposed || name !== this.termName) return;
      this.stopSocket();
      try {
        await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/terminals/' + encodeURIComponent(name) + '/kill', { method: 'POST', body: '{}' });
      } catch (error) {
        this.showError(t('term.restart.failed', { message: error.message }));
        if (error.status === 401 || error.status === 403) invalidateSession();
        return;
      }
      if (this.disposed || name !== this.termName) return;
      this.termOutdated = this.termOutdated.filter((n) => n !== name);
      this.providerOutdated = this.providerOutdated.filter((n) => n !== name);
      this.termNew = true;
      this.renderTermTabs();
      this.connect();
    }

    // After the shown session ended: show its neighbour, or main (which
    // attaching starts again).
    showTermAfter(index) {
      const rest = this.termNames.filter((name) => name !== this.termName);
      const next = rest[Math.min(Math.max(index - 1, 0), rest.length - 1)] || 'main';
      this.termName = '';
      this.switchTerm(next);
    }

    // The tabs' labels in the current language (each is the text before its badge).
    relabelTabs() {
      for (const [key, tab] of Object.entries(this.tabButtons)) {
        const label = tab.firstChild;
        if (label && label.nodeType === 3) label.nodeValue = t('tab.' + key);
      }
      if (this.viewItems[this.root.dataset.tab]) this.viewTab.textContent = t('tab.' + this.root.dataset.tab);
      this.renderProposalBar();
    }

    waitingProposals() {
      return (this.policy?.snapshot?.pending || []).filter((p) => p.source === 'agent');
    }

    // One line over the open view while the agent's proposals wait, until
    // あとで; a proposal that arrives after it brings the line back. Not on
    // the policy tab, which lists them.
    renderProposalBar() {
      const waiting = this.waitingProposals();
      const fresh = waiting.some((p) => !this.proposalsLater.has(p.id));
      this.proposalBar.hidden = !fresh || this.root.dataset.tab === 'policy';
      if (this.proposalBar.hidden) return;
      const p = waiting[0];
      const where = p.endpoints.map((e) => (e.host || t('policy.anyHost')) + (e.ports.length ? ':' + e.ports.join(',') : '')).join(', ') || p.summary || p.rule;
      const program = p.binaries[0] ? ' (' + p.binaries[0].split('/').pop() + (p.binaries.length > 1 ? t('policy.more', { count: p.binaries.length - 1 }) : '') + ')' : '';
      this.proposalText.textContent = t('proposalBar.text', { count: waiting.length, what: where + program });
      this.proposalText.title = this.proposalText.textContent;
      this.proposalReview.disabled = !state.authorized;
    }

    // One proposal opens its approval; several open the policy tab to choose.
    reviewProposals() {
      const waiting = this.waitingProposals();
      if (waiting.length === 1 && !waiting[0].providerScope?.resolving) this.policy.decide(waiting[0], 'approve');
      else { this.policy.showView('list'); this.selectTab('policy'); }
    }

    // The policy tab's badge counts the agent's proposals waiting for the
    // owner (not OpenShell's automatic ones; agentPending).
    renderTabs() {
      if (!this.policy) return;
      const count = agentPending(this.policy.snapshot);
      const tab = this.tabButtons.policy;
      const value = count ? String(count) : '';
      if (tab.badge.textContent !== value) tab.badge.textContent = value;
      tab.badge.hidden = !count;
      tab.title = count ? t('tab.policy.pending', { count }) : '';
      this.renderProposalBar();
    }

    // Keys a phone keyboard lacks. Buttons keep focus on the terminal so the
    // on-screen keyboard stays open; Ctrl applies to the next typed key.
    createKeyBar() {
      const bar = element('div', 'key-bar');
      bindAttribute(bar, 'aria-label', 'keys.label');
      const keys = [['Esc', '\x1b'], ['Tab', '\t'], ['⇧Tab', '\x1b[Z'], ['Ctrl', null], ['↑', '\x1b[A'], ['↓', '\x1b[B'], ['←', '\x1b[D'], ['→', '\x1b[C'],
        ['^C', '\x03'], ['^B', '\x02'], [t('keys.paste'), 'paste'], [t('keys.image'), 'image'], ['|', '|'], ['/', '/'], ['~', '~'], ['-', '-']];
      // 画像 picks images to paste (a phone has no screenshot to paste).
      const picker = element('input');
      picker.type = 'file';
      picker.accept = 'image/*';
      picker.multiple = true;
      picker.hidden = true;
      picker.setAttribute('aria-hidden', 'true');
      picker.addEventListener('change', () => {
        const files = Array.from(picker.files || []);
        picker.value = '';
        if (files.length) this.pasteImages(files);
      });
      bar.append(picker);
      for (const [label, sequence] of keys) {
        const key = button('key', label, () => {
          if (sequence === 'paste') { this.pasteFromClipboard(); return; }
          if (sequence === 'image') { if (this.desired && this.connection === 'connected') picker.click(); return; }
          if (sequence === null) { this.ctrlArmed = !this.ctrlArmed; key.setAttribute('aria-pressed', String(this.ctrlArmed)); return; }
          if (this.desired && this.connection === 'connected') this.send({ type: 'input', data: sequence });
          if (this.terminal) { this.terminal.toBottom(); this.terminal.focus(); }
        });
        key.className = 'key';
        key.addEventListener('pointerdown', (event) => event.preventDefault());
        if (sequence === null) { this.ctrlKey = key; key.setAttribute('aria-pressed', 'false'); }
        if (sequence === 'paste' || sequence === 'image') bind(key, 'keys.' + sequence);
        bar.append(key);
      }
      return bar;
    }

    // Pastes the clipboard through the terminal (bracketed paste when the
    // shell asks for it). Where the page may not read the clipboard, a box takes a manual paste.
    async pasteFromClipboard() {
      if (!this.terminal || !this.desired || this.connection !== 'connected') return;
      try {
        const text = await navigator.clipboard.readText();
        if (text) { this.terminal.paste(text); this.terminal.focus(); return; }
      } catch (_) {}
      this.showPasteBox();
    }

    // Images pasted or dropped on the terminal, or picked with 画像, go into
    // the sandbox (POST .../paste-image); their paths are then pasted as
    // text, each followed by a space: Claude Code takes an image file's
    // path as the image. One batch at a time, in order.
    pasteImages(files) {
      this.pasteQueue = (this.pasteQueue || Promise.resolve()).then(() => this.uploadImages(Array.from(files).slice(0, 10)));
      return this.pasteQueue;
    }

    async uploadImages(files) {
      const live = () => !this.disposed && this.terminal && this.desired && this.connection === 'connected';
      if (!live()) return;
      const images = files.filter((f) => pasteImageTypes.includes(f.type));
      if (images.length < files.length) this.showError(t('error.paste_type'));
      for (const [i, file] of images.entries()) {
        if (file.size > maxPasteImage) { this.showError(t('error.paste_too_large', { max: maxPasteImage >> 20 })); continue; }
        this.pasteStatus.textContent = images.length > 1 ? t('paste.sendingOf', { n: i + 1, count: images.length }) : t('paste.sending');
        this.pasteStatus.hidden = false;
        try {
          const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/paste-image', { method: 'POST', headers: { 'Content-Type': file.type }, body: file, timeoutMs: 120000 });
          if (typeof data.path !== 'string' || !data.path.startsWith('/') || data.path.length > 4096 || /[\x00-\x1f\x7f]/.test(data.path)) throw new Error(t('app.badResponse'));
          if (!live()) return;
          this.terminal.paste(shellWord(data.path));
          this.send({ type: 'input', data: ' ' });
        } catch (error) {
          this.showError(t('paste.failed', { message: error.message }));
          if (error.status === 401 || error.status === 403) invalidateSession();
          return;
        } finally {
          this.pasteStatus.hidden = true;
        }
      }
      this.focusUnlessTouch();
    }

    showPasteBox() {
      if (!this.pasteBox) {
        this.pasteBox = element('div', 'paste-box');
        const input = element('textarea', 'paste-input');
        bindAttribute(input, 'placeholder', 'paste.placeholder');
        bindAttribute(input, 'aria-label', 'paste.label');
        input.rows = 3;
        const sendButton = bind(button('button-start', '', () => {
          if (input.value && this.terminal && this.connection === 'connected') this.terminal.paste(input.value);
          input.value = '';
          this.pasteBox.hidden = true;
          if (this.terminal) this.terminal.focus();
        }), 'paste.send');
        const close = bind(button('button-subtle', '', () => { input.value = ''; this.pasteBox.hidden = true; this.focusUnlessTouch(); }), 'common.close');
        const actions = element('div', 'panel-buttons');
        actions.append(sendButton, close);
        this.pasteBox.append(input, actions);
        this.pasteInput = input;
        this.footer.append(this.pasteBox);
      }
      this.pasteBox.hidden = false;
      this.pasteInput.focus();
    }

    // Browsers handle Ctrl+W/T/N before the page. In fullscreen, the Keyboard
    // Lock API (Chromium) hands them to the terminal; holding Esc exits.
    async toggleFullscreen() {
      const surface = this.host.parentElement;
      if (document.fullscreenElement) { await document.exitFullscreen?.().catch(() => {}); return; }
      if (!surface?.requestFullscreen) return;
      try {
        await surface.requestFullscreen({ navigationUI: 'hide' });
        await navigator.keyboard?.lock?.();
      } catch (_) {}
      this.scheduleFit();
      if (this.terminal) this.terminal.focus();
    }

    applyCtrl(data) {
      if (!this.ctrlArmed || data.length !== 1) return data;
      this.ctrlArmed = false;
      this.ctrlKey?.setAttribute('aria-pressed', 'false');
      const code = data.toUpperCase().charCodeAt(0);
      return code >= 64 && code <= 95 ? String.fromCharCode(code - 64) : data;
    }

    update(sandbox) {
      this.sandbox = sandbox;
      this.policy.sandbox = sandbox;
      this.links.sandbox = sandbox;
      this.text.sandbox = sandbox;
      this.files.sandbox = sandbox;
      this.editor.sandbox = sandbox;
      this.ports.sandbox = sandbox;
      this.title.textContent = sandbox.name;
      this.title.title = sandbox.name;
      this.navTitle.textContent = sandbox.name;
      this.root.setAttribute('aria-label', t('card.label', { name: sandbox.name }));
      this.nav.setAttribute('aria-label', t('card.nav', { name: sandbox.name }));
      const currentStatus = this.starting ? 'starting' : this.stopping ? 'stopping' : sandbox.status;
      // A sandbox that is not running shows the start placeholder; once it
      // exists in OpenShell (stopped, failed, being made) its policy tab
      // stays, so a policy that keeps it from starting can be fixed.
      this.root.dataset.collapsed = String(sandbox.status !== 'running');
      this.root.dataset.created = String(sandbox.status !== 'not-created');
      if (this.editor && String(this.splitShown()) !== this.root.dataset.split) this.layoutTabs(this.root.dataset.tab);
      // The files tab stays too: a stopped sandbox's drive and credentials;
      // and the ports tab: its node stays; and the editor: its unsaved text.
      if (sandbox.status !== 'running' && (sandbox.status === 'not-created' || !['policy', 'files', 'editor', 'ports'].includes(this.root.dataset.tab)) && this.root.dataset.tab !== 'terminal') this.selectTab('terminal');
      if (this.root.dataset.tab === 'files' && this.filesShown !== sandbox.status) this.files.refresh();
      if (this.root.dataset.tab === 'ports' && this.filesShown !== sandbox.status) this.ports.refresh();
      if (!this.editor.root.hidden && this.filesShown !== sandbox.status) this.editor.render();
      this.filesShown = sandbox.status;
      this.badge.textContent = statusKeys[currentStatus] ? t(statusKeys[currentStatus]) : currentStatus;
      this.badge.dataset.state = currentStatus;
      const pending = sandbox.status === 'running' && sandbox.restartPending === true;
      if (pending !== !this.restartBadge.hidden && sandbox.status === 'running') this.termsLoaded = false;
      this.restartBadge.hidden = !pending;
      this.gpuBadge.hidden = sandbox.gpu !== true;
      if (!pending) this.providerOutdated = [];
      this.navDot.style.background = sandbox.status === 'running' ? '#8ddeba' : sandbox.status === 'error' ? '#f39b9b' : '#778497';
      // The terminals ended: when it stops, unless one opens.
      const stopIn = sandbox.status === 'running' && sandbox.autoStopAt ? Math.max(0, Math.round(sandbox.autoStopAt - Date.now() / 1000)) : -1;
      this.detail.textContent = sandbox.detail || (stopIn >= 0 ? t('autostop.pending', { seconds: stopIn }) : '');
      this.detail.title = this.detail.textContent;
      this.detail.dataset.state = sandbox.status;
      if (sandbox.status !== 'running') {
        this.termNames = ['main'];
        this.termMulti = false;
        this.termsLoaded = false;
        // Its last screen would look live: the placeholder shows instead.
        if (this.terminal && !this.desired) {
          this.terminal.dispose();
          this.terminal = null;
          this.placeholder.hidden = false;
        }
      } else if (!this.termsLoaded) {
        this.termsLoaded = true;
        this.loadTerms();
      }
      this.renderTermTabs();
      if (!this.terminal) {
        const key = 'card.placeholder.' + (['running', 'starting', 'error'].includes(sandbox.status) ? sandbox.status : 'stopped');
        this.placeholderTitle.textContent = t(key + '.title');
        this.placeholderText.textContent = t(key + '.text');
      }
      this.updateButtons();
    }

    updateButtons() {
      const busy = this.connection === 'connecting';
      const working = this.starting || this.stopping || !!this.recreating;
      // A start in progress (this page's, or another's the server reports)
      // can be given up with 停止.
      const starting = this.starting || this.sandbox.status === 'starting';
      this.startButton.disabled = !state.authorized || working || starting || this.sandbox.status === 'running' || this.desired || state.imagesReady === false;
      this.startButton.textContent = starting ? t('card.starting') : t('card.start');
      this.connectButton.disabled = !state.authorized || working || busy || this.sandbox.status !== 'running';
      for (const key of ['links', 'text']) this.viewItems[key].disabled = !state.authorized || this.sandbox.status !== 'running';
      this.deleteButton.disabled = !state.authorized || this.stopping || !!this.recreating;
      this.recreateButton.disabled = !state.authorized || this.stopping || !!this.recreating || state.imagesReady === false;
      this.recreateButton.title = state.imagesReady === false ? t('card.waitImages') : t('card.recreate.title');
      this.stopButton.disabled = !state.authorized || this.stopping || (!starting && (this.starting || this.sandbox.status !== 'running'));
      this.stopButton.textContent = this.stopping ? t('card.stopping') : starting ? t('card.cancelStart') : t('card.stop');
      this.stopButton.title = starting ? t('card.cancelStart.title') : t('card.stop.title');
      this.connectButton.textContent = busy ? t('card.connecting') : this.connection === 'reconnecting' ? t('card.reconnectNow') : t('card.connect');
      // While connected, reconnecting is a menu item; the button is for getting connected.
      this.connectButton.hidden = this.connection === 'connected' || (this.sandbox.status !== 'running' && !this.desired);
      this.startButton.hidden = this.sandbox.status === 'running' && !this.starting;
      this.reconnectButton.hidden = this.connection !== 'connected';
      this.reconnectButton.disabled = this.connectButton.disabled;
      this.startButton.title = state.imagesReady === false ? t('card.waitImages') : this.sandbox.status === 'running' ? t('card.start.running') : t('card.start.title');
      this.connectButton.title = this.reconnectButton.title = t('card.connect.title');
    }

    setConnection(kind, label) {
      this.connection = kind;
      // Being connected is the normal state: only the others are shown.
      this.connectionLabel.hidden = kind === 'connected';
      this.connectionLabel.dataset.state = kind;
      this.connectionText.textContent = label;
      this.connectionText.title = label;
      this.updateButtons();
      summary();
    }

    showError(message) {
      this.messageText.textContent = message;
      this.message.hidden = false;
    }

    // The notice's button: the terminal starts again, or the sandbox does
    // when only that renews it (sandboxRestart).
    termNoticeAction() {
      if (this.sandboxRestart.includes(this.termName)) return this.restart({ ask: true });
      return this.restartTerm();
    }

    // Stop then start: every terminal gets the sandbox's current providers.
    // Resolves to '' once it runs again, else to why not (null: not confirmed).
    async restart({ ask = false } = {}) {
      if (ask && !(await askConfirm(t('card.restart.confirm', { name: this.sandbox.name }), { ok: t('term.restart.ok'), danger: true }))) return null;
      if (this.stopping || this.starting || this.recreating) return t('card.restart.busy');
      if (this.sandbox.status !== 'running') return t('card.restart.notRunning');
      this.opError = '';
      if (!(await this.stop({ confirmed: true }))) return this.opError || t('card.restart.notStopped');
      // Stopped: start even when the list could not say so yet.
      await this.start({ force: true });
      return this.sandbox.status === 'running' ? '' : this.opError || t('card.restart.notStarted');
    }

    // Stop the sandbox only; start resumes the sandbox.
    // While a start is in progress, stop gives it up (the server cancels it).
    async stop({ confirmed = false } = {}) {
      const starting = this.starting || this.sandbox.status === 'starting';
      if (this.stopping || this.disposed || !state.authorized || (!starting && this.sandbox.status !== 'running')) return false;
      if (!confirmed && !(await askConfirm(t(starting ? 'card.cancelStart.confirm' : 'card.stop.confirm', { name: this.sandbox.name }),
        { ok: starting ? t('card.cancelStart.ok') : t('card.stop.ok'), danger: true }))) return false;
      this.stopping = true;
      this.canceling = starting;
      this.message.hidden = true;
      this.disconnect(t('card.stoppingNow'));
      this.update(this.sandbox);
      try {
        const result = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/stop', { method: 'POST', body: '{}', timeoutMs: 310000 });
        if (result.ok !== true) throw new Error(t('card.stop.unknownResult'));
        if (this.disposed) return false;
        if (result.canceled === true) this.showError(typeof result.detail === 'string' ? window.IwatoI18n.serverError(result.code, null, result.detail.slice(0, 1000)) : t('card.startCanceled'));
        if (state.refreshing) await state.refreshing;
        await refreshSandboxes();
        return true;
      } catch (error) {
        this.opError = error.message;
        if (!this.disposed) this.showError(t('card.stop.failed', { message: error.message }));
        if (error.status === 401 || error.status === 403) invalidateSession();
        return false;
      } finally {
        this.stopping = false;
        this.canceling = false;
        if (!this.disposed) this.update(this.sandbox);
      }
    }

    async start({ force = false } = {}) {
      if (this.starting || this.stopping || this.disposed || !state.authorized || (!force && this.sandbox.status === 'running')) return;
      this.starting = true;
      this.message.hidden = true;
      this.update(this.sandbox);
      try {
        const result = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/start', { method: 'POST', body: '{}', timeoutMs: 310000 });
        if (result.ok !== true) throw new Error(t('card.start.unknownResult'));
        if (this.disposed) return;
        // The start after a recreate that stopped short put its rules back.
        const failed = carryFailures(result.failed);
        if (failed.length) this.showError(t('card.startedExcept', { failures: carryMessage(failed) }));
        if (state.refreshing) await state.refreshing;
        this.autoArmed = true;
        await refreshSandboxes();
      } catch (error) {
        this.opError = error.message;
        // A start the owner gave up (停止 or 削除) is not an error to show.
        if (!this.disposed && !this.canceling && !this.deleting && !this.recreating) this.showError(t('card.start.failed', { message: error.message }));
        if (error.status === 401 || error.status === 403) invalidateSession();
        if (!this.disposed && !this.canceling) await refreshSandboxes().catch(() => {});
      } finally {
        this.starting = false;
        if (!this.disposed) { this.update(this.sandbox); this.autoConnect(); }
      }
    }

    // The session's screen drawn in the DOM (web/term.js, internal/termsync).
    ensureTerminal() {
      if (this.terminal) return;
      if (typeof window.IwatoTerminal !== 'function') throw new Error(t('term.scriptMissing'));
      let gesture = 0;
      const mark = () => { gesture = Date.now(); };
      this.host.addEventListener('pointerup', mark, true);
      this.host.addEventListener('keyup', mark, true);
      this.terminal = new window.IwatoTerminal(this.host, {
        theme, fontFamily: terminalFont.ready ? terminalFont.family : terminalFont.fallback, fontSize: 12, lineHeight: 1.25,
        send: (bytes) => this.sendRaw(bytes),
        input: (text) => { if (this.desired && this.connection === 'connected') this.send({ type: 'input', data: text }); },
        mapInput: (text) => this.applyCtrl(text),
        onLink: (url, event) => {
          if (!linkClick(event)) return;
          const found = window.IwatoLinks?.pathOf(url);
          if (found) this.openPath(found); else openLink(url);
        },
        // OSC 52 may only write the clipboard right after the owner's own
        // input, and links from programs in the sandbox (OSC 6464) open only then,
        // in the shown sandbox: output alone changes nothing.
        onClipboard: (text) => {
          if (Date.now() - gesture > 2000 || !navigator.clipboard) return;
          navigator.clipboard.writeText(text).catch(() => {});
        },
        onOpen: (url) => {
          if (state.selected !== this.sandbox.name || Date.now() - gesture > 5000) return;
          gesture = 0;
          openLink(url);
        },
        onCursor: () => this.scheduleReveal(),
        onFiles: (files) => this.pasteImages(files)
      });
      this.host.addEventListener('pointerdown', () => this.claimSize(), true);
      this.placeholder.hidden = true;
      if (!terminalFont.ready) terminalFont.loading.then(() => {
        if (!terminalFont.ready || !this.terminal || this.disposed) return;
        this.terminal.setFont(terminalFont.family);
        this.fit();
      });
    }

    // Opens a path printed in the terminal (IwatoLinks.findPaths) in the
    // editor: beside the terminal when side by side, else in its tab. The
    // server takes a relative path from the terminal's working directory
    // and says when it is not a file there.
    async openPath(found) {
      try {
        const data = await api('/api/sandboxes/' + encodeURIComponent(this.sandbox.name) + '/editor/resolve?term=' + encodeURIComponent(this.termName) + '&path=' + encodeURIComponent(found.path), { timeoutMs: 70000 });
        if (this.disposed) return;
        if (this.root.dataset.split !== 'true') this.selectTab('editor');
        await this.editor.open(String(data.path), found.line ? { line: found.line, col: found.col } : null);
      } catch (error) {
        if (!this.disposed) this.showError(t('term.path.failed', { path: found.path, message: error.message }));
      }
    }

    sendRaw(bytes) {
      if (!this.desired || !this.socket || this.socket.readyState !== WebSocket.OPEN) return;
      try { this.socket.send(bytes); } catch (_) { /* reconnecting */ }
    }

    // Automatic focus (tab switch, connect, reconnect) must not pop up the
    // on-screen keyboard; on touch screens the owner taps the terminal. Nor
    // take the keyboard from a field being typed in (the paste box, a
    // panel's) or from another part of the page (a reconnect while the
    // owner types a new sandbox's name).
    focusUnlessTouch() {
      if (!this.terminal || touchScreen()) return;
      const active = document.activeElement;
      if (active && active !== document.body && !this.surface.contains(active) && (!this.root.contains(active) || active.matches('input, textarea, select, [contenteditable]'))) return;
      this.terminal.focus();
    }

    // After a menu, panel or dialog of the card closed: the terminal gets
    // the keyboard back when it had it before (typingCard).
    restoreFocus() {
      if (this.disposed || pendingConfirm || !this.deletion.root.hidden || !this.recreation.root.hidden || !this.providers.root.hidden) return;
      // Side by side, the pane that had it.
      if (this.root.dataset.split === 'true' && this.lastPane === 'editor') { this.editor.focusText(); return; }
      if (typingCard !== this || !this.termShown()) return;
      this.focusUnlessTouch();
    }

    // Every browser on a sandbox shares one PTY, sized by whichever resized
    // last (internal/hostterm). The browser being used takes the size back
    // when it is touched or typed in (once until it loses focus): after the
    // phone set it, the PC would otherwise keep the phone's width. Not on mere
    // focus or showing the page: each width change makes Claude Code redraw
    // the screen at the new width while the scrollback keeps the old rows, so
    // glancing at the other device would repeat a screenful of the output.
    claimSize() {
      this.sizeClaimed = true;
      this.lastSize = '';
      this.scheduleFit();
    }

    releaseSize() {
      this.sizeClaimed = false;
    }

    // While the on-screen keyboard is open the terminal keeps its rows: the
    // host keeps the height it had and the surface shows the part with the
    // cursor. With fewer rows Claude Code redraws the last rows of its
    // output, and the rows the terminal pushed into the scrollback stay there
    // when the rows come back, so they would show twice in the history; and
    // every resize makes full-screen programs redraw.
    holdRows(on) {
      if (on && !this.heldHeight && this.terminal && this.host.clientHeight >= 20) {
        this.heldHeight = this.host.clientHeight;
        this.host.style.height = this.heldHeight + 'px';
      } else if (!on && this.heldHeight) {
        this.heldHeight = 0;
        this.host.style.height = '';
        this.host.style.transform = '';
      }
      this.scheduleReveal();
    }

    scheduleReveal() {
      if (!this.heldHeight || this.revealFrame !== null || this.disposed) return;
      this.revealFrame = requestAnimationFrame(() => { this.revealFrame = null; this.revealCursor(); });
    }

    // Shows the rows below the cursor too when they fit (Claude Code's input
    // box and status lines), else puts the cursor row at the bottom.
    revealCursor() {
      if (!this.heldHeight || !this.terminal) return;
      const style = getComputedStyle(this.surface);
      const visible = this.surface.clientHeight - parseFloat(style.paddingTop) - parseFloat(style.paddingBottom);
      const row = this.terminal.cursorRow();
      const cell = this.terminal.cellH;
      const max = Math.max(0, this.heldHeight - visible);
      const shift = row * cell >= max ? max : Math.max(0, (row + 1) * cell - visible);
      this.host.style.transform = shift ? 'translateY(' + -Math.round(shift) + 'px)' : '';
    }

    scheduleFit() {
      if (this.resizeFrame !== null || this.disposed) return;
      this.resizeFrame = requestAnimationFrame(() => { this.resizeFrame = null; this.fit(); });
    }

    fit() {
      if (!this.terminal || this.disposed || this.host.clientWidth < 20 || this.host.clientHeight < 20) return;
      this.size = this.terminal.fit();
      const { cols, rows } = this.termSize();
      const dims = cols + ' × ' + rows;
      if (this.dimensions.textContent !== dims) {
        const first = this.dimensions.textContent.startsWith('—');
        this.dimensions.textContent = dims;
        if (!first) {
          this.dimensions.hidden = false;
          clearTimeout(this.dimensionsTimer);
          this.dimensionsTimer = setTimeout(() => { this.dimensions.hidden = true; }, 1500);
        }
      }
      this.sendResize();
    }

    send(payload) {
      if (!this.desired || !this.socket || this.socket.readyState !== WebSocket.OPEN) return;
      if (payload.type === 'input' && !this.sizeClaimed) {
        // Typing without a touch first: take the size before the input.
        this.sizeClaimed = true;
        this.lastSize = '';
        this.sendResize();
      }
      try {
        if (this.socket.bufferedAmount > 1024 * 1024) {
          this.disconnect(t('conn.backlog'));
          this.showError(t('conn.backlog.error'));
          return;
        }
        // Terminal input and size go as termsync messages.
        if (payload.type === 'input') this.socket.send(window.IwatoTerminal.inputMessage(payload.data));
        else if (payload.type === 'resize') this.socket.send(this.terminal.resizeMessage(payload.cols, payload.rows));
      } catch (_) {
        this.showError(t('conn.inputFailed'));
      }
    }

    // The size the terminal shows (the size that fits; the server's frames
    // follow it).
    termSize() {
      return this.size || { cols: 0, rows: 0 };
    }

    // On a touch screen fewer rows at the same width wait until they last
    // (settled): the on-screen keyboard and the browser's bars change the
    // height for a moment, and each row change makes Claude Code redraw its
    // last rows while the rows a shrink pushed into the scrollback stay
    // there, so they would show twice in the history.
    sendResize(settled = false) {
      if (!this.terminal || this.connection !== 'connected') return;
      const { cols, rows } = this.termSize();
      if (!cols || !rows) return;
      const size = cols + ':' + rows;
      clearTimeout(this.shrinkTimer);
      if (size === this.lastSize) return;
      const [lastCols, lastRows] = this.lastSize.split(':').map(Number);
      if (!settled && touchScreen() && cols === lastCols && rows < lastRows) {
        this.shrinkTimer = setTimeout(() => this.sendResize(true), 1500);
        return;
      }
      this.lastSize = size;
      this.send({ type: 'resize', cols, rows });
    }

    stopSocket() {
      clearTimeout(this.reconnectTimer);
      clearTimeout(this.connectTimer);
      this.reconnectTimer = null;
      this.connectTimer = null;
      this.generation += 1;
      const old = this.socket;
      this.socket = null;
      if (old && (old.readyState === WebSocket.OPEN || old.readyState === WebSocket.CONNECTING)) old.close(1000, 'Browser detached');
    }

    // Showing a running sandbox that is not connected connects it, once per
    // time it is shown or started (a failed attach is not retried by polling).
    armAutoConnect() {
      this.autoArmed = true;
      this.autoConnect();
    }

    autoConnect() {
      if (!this.autoArmed) return;
      if (state.selected !== this.sandbox.name) { this.autoArmed = false; return; }
      if (this.disposed || !state.authorized || this.starting || this.stopping || this.sandbox.status !== 'running') return;
      this.autoArmed = false;
      if (!this.desired) this.connect();
    }

    // The page came back (shown, focused, online): the shown sandbox's
    // terminal, when it is not connected or connecting, connects now instead
    // of waiting out its backoff or for the owner to press 接続. Not after a
    // process that exited or a refused session.
    resume() {
      if (state.selected !== this.sandbox.name || this.ended || this.socket) return;
      if (this.disposed || !state.authorized || this.starting || this.stopping || this.sandbox.status !== 'running') return;
      this.attempt = 0;
      this.connect(true);
    }

    connect(isRetry = false) {
      if (!state.authorized || this.disposed || this.starting) return;
      this.ended = false;
      if (this.sandbox.status !== 'running') {
        this.disconnect(t('conn.needsStart'));
        return;
      }
      this.stopSocket();
      this.desired = true;
      if (!isRetry) { this.attempt = 0; this.selectTab('terminal'); }
      this.message.hidden = true;
      try { this.ensureTerminal(); } catch (error) {
        this.desired = false;
        this.setConnection('error', t('conn.initError'));
        this.showError(error.message);
        return;
      }
      const epoch = this.generation;
      const url = new URL('/ws/' + encodeURIComponent(this.sandbox.name), location.href);
      url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
      url.searchParams.set('csrf', state.csrf);
      url.searchParams.set('term', this.termName);
      if (this.termNew) url.searchParams.set('new', '1');
      let socket;
      try { socket = new WebSocket(url); } catch (_) {
        this.desired = false;
        this.setConnection('error', t('conn.error'));
        this.showError(t('conn.socketFailed'));
        return;
      }
      socket.binaryType = 'arraybuffer';
      this.socket = socket;
      this.setConnection('connecting', isRetry ? t('conn.reconnecting') : t('conn.connecting'));
      const current = () => !this.disposed && this.desired && epoch === this.generation && socket === this.socket;
      this.connectTimer = setTimeout(() => { if (current() && socket.readyState === WebSocket.CONNECTING) socket.close(); }, 12000);
      socket.onopen = () => {
        if (!current()) { socket.close(1000, 'Stale connection'); return; }
        clearTimeout(this.connectTimer);
        this.connectTimer = null;
        this.attempt = 0;
        this.lastSize = '';
        this.sizeClaimed = true; // the size is sent on connecting
        // The hello carries the size; the session is attached at it. A
        // terminal that is not laid out (the card on another tab, as when
        // a notification opens the policies) has no size and leaves the
        // session's: it sends its own once shown (fit).
        this.size = null;
        this.fit();
        socket.send(this.terminal.hello(this.size));
        this.lastSize = this.size ? this.size.cols + ':' + this.size.rows : '';
        this.listOnData = true;
        this.setConnection('connected', state.fake ? t('conn.connectedFake') : t('conn.connected'));
        this.fit();
        this.focusUnlessTouch();
      };
      socket.onmessage = (event) => {
        if (!current()) return;
        // Only binary terminal data is accepted. No HTML, JSON rendering, or eval.
        if (!(event.data instanceof ArrayBuffer)) return;
        const data = new Uint8Array(event.data);
        if (this.listOnData) {
          // The session runs now (attaching started it): list them all.
          this.listOnData = false;
          this.termNew = false;
          this.loadTerms();
        }
        try { this.terminal.receive(data); } catch (error) {
          this.disconnect(t('conn.renderError'));
          this.showError(t('conn.renderError.error', { message: error.message }));
        }
      };
      socket.onerror = () => {
        if (current()) this.showError(t('conn.unverified'));
      };
      socket.onclose = async (event) => {
        if (!current()) return;
        clearTimeout(this.connectTimer);
        this.connectTimer = null;
        this.socket = null;
        // A session other than main that ended (exit, closed elsewhere, or
        // could not be created) is not attached again: show another one.
        // Main too when the terminal daemon keeps it (it ends like the others).
        if (this.termName !== 'main' || !this.sandboxRestart.includes('main')) {
          const failedNew = this.termNew;
          const index = this.termNames.indexOf(this.termName);
          const listed = await this.loadTerms();
          if (this.disposed || !this.desired || epoch !== this.generation) return;
          if (listed && !this.termNames.includes(this.termName)) {
            if (failedNew) this.showError(t('term.tooMany', { max: maxTerms }));
            this.showTermAfter(index);
            return;
          }
        }
        // The server could not attach and says why (an old version
        // in the sandbox, a session that ended); retrying would not help.
        if (event.code === 4500) {
          this.desired = false;
          this.setConnection('error', t('conn.failed'));
          this.showError(t('conn.failed.error', { reason: event.reason || t('conn.failed.unknown') }));
          return;
        }
        if ([1000, 1008, 4401, 4403].includes(event.code)) {
          this.desired = false;
          // The process exited or the session was refused: coming back to
          // the page does not attach again (resume).
          this.ended = event.code !== 1008;
          this.setConnection(event.code === 1000 ? 'disconnected' : 'error', event.code === 1000 ? t('conn.closed') : t('conn.refused'));
          if (event.code !== 1000) this.showError(t('conn.refused.error'));
          refreshSandboxes();
          return;
        }
        this.attempt += 1;
        const delay = Math.min(30000, 1000 * 2 ** Math.min(this.attempt - 1, 5)) + Math.floor(Math.random() * 350);
        this.setConnection('reconnecting', t('conn.lost', { seconds: Math.ceil(delay / 1000) }));
        this.reconnectTimer = setTimeout(() => {
          this.reconnectTimer = null;
          if (!this.disposed && this.desired && epoch === this.generation) this.connect(true);
        }, delay);
      };
    }

    disconnect(label = t('conn.disconnected')) {
      this.desired = false;
      this.stopSocket();
      this.setConnection('disconnected', label);
      this.message.hidden = true;
    }

    dispose() {
      this.policyShown = false;
      clearInterval(this.policyTimer);
      this.policyTimer = null;
      this.disposed = true;
      this.policy.dispose();
      this.links.dispose();
      this.deletion.dispose();
      this.recreation.dispose();
      this.text.dispose();
      this.files.dispose();
      this.editor.dispose();
      this.ports.dispose();
      this.disconnect();
      this.observer.disconnect();
      this.cardObserver.disconnect();
      clearTimeout(this.splitFitTimer);
      if (this.resizeFrame !== null) cancelAnimationFrame(this.resizeFrame);
      if (this.revealFrame !== null) cancelAnimationFrame(this.revealFrame);
      if (this.terminal) this.terminal.dispose();
      this.root.remove();
      this.closeActionMenu();
      this.nav.remove();
    }
  }

  $('refresh-button').addEventListener('click', () => state.authorized ? refreshSandboxes() : authenticate());
  $('login-form').addEventListener('submit', (event) => {
    event.preventDefault();
    const token = parseToken($('login-input').value);
    $('login-input').value = '';
    if (!token) { showNotice(t('login.failed'), t('login.badFormat')); return; }
    authenticate(token);
  });
  $('pair-button').addEventListener('click', pairDevice);
  $('log-button').addEventListener('click', () => $('log-panel').hidden ? openLog() : closeLog());
  $('images-log').addEventListener('click', openLog);
  $('log-close').addEventListener('click', closeLog);
  $('images-retry').addEventListener('click', retryImages);
  $('images-recheck').addEventListener('click', recheckEngine);
  $('create-button').addEventListener('click', () => $('create-panel').hidden ? openCreate() : ($('create-panel').hidden = true));
  $('create-name').addEventListener('input', createHint);
  $('volume-button').addEventListener('click', () => $('volume-panel').hidden ? openVolumes() : ($('volume-panel').hidden = true));
  $('volume-close').addEventListener('click', () => { $('volume-panel').hidden = true; });
  $('provider-button').addEventListener('click', () => $('provider-panel').hidden ? openProviders() : ($('provider-panel').hidden = true));
  $('settings-button').addEventListener('click', () => $('settings-panel').hidden ? openSettings() : ($('settings-panel').hidden = true));
  $('settings-close').addEventListener('click', () => { $('settings-panel').hidden = true; });
  $('provider-close').addEventListener('click', () => { $('provider-panel').hidden = true; closeProfileYAML(); });
  $('provider-profile-yaml').addEventListener('click', () => {
    const id = $('provider-profile').value;
    if (!id) return;
    if (providerView.profiles.find((x) => x.id === id)?.custom) editCustomProfile(id);
    else showProfileYAML(id);
  });
  $('provider-custom-add').addEventListener('click', () => addCustomProfile());
  $('provider-profile').addEventListener('change', () => renderProviderForm());
  $('provider-form').addEventListener('submit', (event) => { event.preventDefault(); createProvider(); });
  $('create-close').addEventListener('click', () => { $('create-panel').hidden = true; });
  $('create-form').addEventListener('submit', (event) => { event.preventDefault(); createSandbox(); });
  $('pair-close').addEventListener('click', closePair);
  $('pair-copy').addEventListener('click', () => {
    const url = $('pair-url').textContent;
    if (url && typeof navigator !== 'undefined' && navigator.clipboard) navigator.clipboard.writeText(url).catch(() => {});
  });
  $('push-button').addEventListener('click', pushAction);
  $('menu-button').addEventListener('click', () => {
    const open = $('menu-button').getAttribute('aria-expanded') !== 'true';
    if (open) $('sidebar').classList.add('menu-open'); else closeMenu();
    $('menu-button').setAttribute('aria-expanded', String(open));
  });
  $('sidebar-actions').addEventListener('click', (event) => { if (event.target !== $('sidebar-actions')) closeMenu(); });
  $('notice').addEventListener('click', clearNotice);
  // Esc does what the topmost open overlay's close or cancel button does.
  // Dialogs and menus come first wherever the keyboard is; a panel only
  // when the keyboard is not in a terminal or the editor (their programs
  // use Esc), the one holding the focus first. Keys the focused element
  // took (a terminal, CodeMirror's search, the quick open) are left alone.
  function escapeTarget(at) {
    const shown = (node) => node && !node.hidden && node.isConnected !== false && node.checkVisibility?.() !== false;
    const card = cards.get(state.selected);
    const first = [
      [$('confirm-panel'), () => answerConfirm(false)],
      [updateDialog.root, () => updateDialog.close()],
      [resetSection?.dialog, () => resetSection.close()],
      ...[...cards.values()].map((c) => [c.actionMenu, () => { c.closeActionMenu(); c.restoreFocus(); }])
    ];
    if ($('menu-button').getAttribute('aria-expanded') === 'true') first.push([$('sidebar-actions'), closeMenu]);
    const top = first.find(([node]) => shown(node));
    if (top) return top;
    if (at && [...cards.values()].some((c) => c.surface.contains(at) || c.editor.root.contains(at))) return null;
    const panels = [[$('provider-yaml'), closeProfileYAML], [$('provider-oauth'), endOAuth]];
    if (card) for (const panel of [card.deletion, card.recreation, card.providers, card.links, card.text]) panels.push([panel.root, () => panel.closeButton.click()]);
    for (const id of ['create', 'pair', 'log', 'volume', 'provider', 'settings']) panels.push([$(id + '-panel'), () => $(id + '-close').click()]);
    const open = panels.filter(([node]) => shown(node));
    return open.find(([node]) => at && node.contains(at)) || open[0] || null;
  }
  document.addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || event.isComposing || event.defaultPrevented) return;
    const at = document.activeElement;
    const [node, close] = escapeTarget(at) || [];
    if (!close) return;
    event.preventDefault();
    // As a click on the close button, which takes the focus out first.
    if (at && node.contains(at)) at.blur?.();
    close();
  });
  // Pushes go out only while no page is open and in use (presence.go): the
  // page tells the server when the owner touches it (at most every 30 s),
  // when it is shown and when it is hidden or closed.
  const presence = { page: Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, '0')).join(''), active: false, sent: 0 };
  function reportPresence(active) {
    if (!state.authorized || state.disposed || (active && document.hidden)) return;
    const now = Date.now();
    if (active === presence.active && (!active || now - presence.sent < 30000)) return;
    presence.active = active;
    presence.sent = now;
    api('/api/presence', { method: 'POST', body: JSON.stringify({ page: presence.page, active }), keepalive: !active }).catch(() => {});
  }
  for (const name of ['keydown', 'pointerdown', 'wheel', 'paste']) document.addEventListener(name, () => reportPresence(true), { capture: true, passive: true });
  document.addEventListener('visibilitychange', () => {
    reportPresence(!document.hidden);
    for (const card of cards.values()) card.renderTermTabs();
    if (document.hidden) for (const card of cards.values()) card.releaseSize();
    else { cards.get(state.selected)?.resume(); refreshSandboxes(); }
    // A page back from the background may render with other font data.
    if (!document.hidden) for (const card of cards.values()) card.scheduleFit();
  });
  // Fit measures the cell again: a font that loaded or came back later
  // (a phone may drop a web font's data in the background).
  document.fonts?.addEventListener?.('loadingdone', () => { for (const card of cards.values()) card.scheduleFit(); });
  window.addEventListener('focus', () => cards.get(state.selected)?.resume());
  window.addEventListener('online', () => cards.get(state.selected)?.resume());
  // The card whose terminal had the keyboard last. The card's own controls
  // on the way (a tab, the menu, a panel's field) and the dialog do not
  // change it; another part of the page does.
  document.addEventListener('focusin', (event) => {
    const target = event.target;
    if ($('confirm-panel').contains(target)) return;
    const card = [...cards.values()].find((c) => c.root.contains(target));
    if (card && card.surface.contains(target)) typingCard = card;
    else if (!card) typingCard = null;
  });
  window.addEventListener('blur', () => { for (const card of cards.values()) card.releaseSize(); });
  const poll = setInterval(() => { if (!document.hidden) refreshSandboxes(); }, 10000);
  // Ctrl+W cannot be intercepted in a normal tab; ask before leaving while a
  // terminal is connected so an accidental close can be cancelled.
  window.addEventListener('beforeunload', (event) => {
    if (![...cards.values()].some((card) => card.connection === 'connected' || card.editor.unsaved())) return;
    event.preventDefault();
    event.returnValue = '';
  });
  // An on-screen keyboard covers the page instead of shrinking it, hiding the
  // prompt and the key bar. While typing in a terminal, the page fits the
  // visible area above the keyboard and shows only the terminal and its keys.
  // The keyboard is open when the visible area is much shorter than the
  // layout viewport (not merely pinch-zoomed).
  const updateKeyboardLayout = () => {
    const viewport = window.visualViewport;
    const open = viewport && viewport.scale < 1.05 && window.innerHeight - viewport.height > 120 ? viewport : null;
    const active = document.activeElement;
    const card = open && [...cards.values()].find((c) => c.surface.contains(active) || c.footer.contains(active)) || null;
    // Before the page shrinks: the terminal keeps the height it has now.
    for (const other of cards.values()) other.holdRows(other === card);
    const shell = $('app-shell');
    if (card) shell.classList.add('keyboard-open'); else shell.classList.remove('keyboard-open');
    const top = card ? Math.round(viewport.offsetTop || 0) + 'px' : '';
    const height = card ? Math.floor(viewport.height) + 'px' : '';
    if (shell.style.top === top && shell.style.height === height) return;
    shell.style.top = top;
    shell.style.height = height;
    // A fullscreen terminal is outside the page; size it the same way.
    for (const other of cards.values()) {
      other.surface.style.height = other === card && document.fullscreenElement === other.surface ? height : '';
      other.scheduleFit();
    }
  };
  window.visualViewport?.addEventListener('resize', updateKeyboardLayout);
  window.visualViewport?.addEventListener('scroll', updateKeyboardLayout);
  document.addEventListener('focusin', updateKeyboardLayout);
  document.addEventListener('focusout', () => setTimeout(updateKeyboardLayout, 0));
  document.addEventListener('fullscreenchange', () => {
    if (!document.fullscreenElement) navigator.keyboard?.unlock?.();
    for (const card of cards.values()) card.scheduleFit();
  });
  window.addEventListener('pagehide', () => {
    reportPresence(false);
    state.disposed = true;
    clearInterval(poll);
    for (const card of cards.values()) card.dispose();
  });
  window.addEventListener('pageshow', (event) => { if (event.persisted) location.reload(); });
  // A change of language (設定): the marked nodes change by themselves;
  // what the cards and the sidebar build from t() is drawn again here.
  window.IwatoI18n.onChange(() => {
    for (const card of cards.values()) { card.relabelTabs(); card.update(card.sandbox); card.renderTabs(); }
    renderBuild(state.build);
    if (state.lastImages !== undefined) renderImages(state.lastImages, state.lastEngine);
    checkSettings?.render();
    timezoneSettings?.render();
    if (!$('create-panel').hidden) createHint();
    if (!$('provider-panel').hidden) refreshProviders();
    if (!$('volume-panel').hidden) renderVolumes();
  });
  authenticate();
})();
