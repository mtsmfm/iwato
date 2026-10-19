/* OpenShell's refusals in the page's language, pure (no DOM): endpoint
   ambiguities, credentials that would ride uninspected, and the gateway's
   own ambiguity refusal. The server sends a code with the pairs and
   conflicts (policy_prediction.go, credential_conflicts.go) and its
   Japanese text, which is shown when the page cannot word them. */
(() => {
  'use strict';
  const I = () => window.IwatoI18n;
  const t = (key, params) => I() ? I().t(key, params) : key;
  const has = (key) => I() ? I().has(key) : false;
  const list = (items) => I() ? I().list(items) : items.join(', ');
  const sentences = (items) => items.join(t('ambiguity.sentenceSep'));

  // A provider as "name (label)", the label of Iwato's profiles translated.
  function providerLabel(p) {
    const label = p.profile && I() ? I().localized('profile.' + p.profile + '.label', p.label) : p.label;
    return label ? p.name + ' (' + label + ')' : p.name;
  }
  const owner = (rule, p) => p ? t('ambiguity.owner.provider', { rule, provider: providerLabel(p) }) : t('ambiguity.owner.rule', { rule });

  const fieldLabel = (f) => has('ambiguity.field.' + f) ? t('ambiguity.field.' + f) : f;
  // A value token of policy_prediction.go fieldValue.
  function valueLabel(f, v) {
    if (f === 'enforcement' && (v === 'enforce' || v === 'audit')) return t('ambiguity.value.' + v);
    if (f === 'tls' && v === 'skip') return t('ambiguity.value.tlsSkip');
    if (f === 'tls' && v === 'default') return t('ambiguity.value.tlsDefault');
    if (v) return v;
    if (f === 'protocol') return t('ambiguity.value.noInspect');
    if (f === 'allowed_ips' || f === 'transparent_tcp_eligible') return t('ambiguity.value.unset');
    if (f === 'credential_binding.provider') return t('ambiguity.value.none');
    return t('ambiguity.value.other');
  }

  // What the two endpoints of a pair disagree on (the server's Japanese
  // when it sent no diffs).
  function details(a) {
    if (!a.diffs || !a.diffs.length) return a.details || [];
    return a.diffs.map((d) => t('ambiguity.detail', { field: fieldLabel(d.field), rule: a.rule, value: valueLabel(d.field, d.value), with: a.with, withValue: valueLabel(d.field, d.withValue) }));
  }
  const pair = (a) => t('ambiguity.pair', { endpoint: a.endpoint, owner: owner(a.rule, a.provider), withOwner: owner(a.with, a.withProvider), details: list(details(a)) });
  const wants = (a) => list((a.diffs || []).map((d) => t('ambiguity.want', { field: fieldLabel(d.field), value: valueLabel(d.field, d.value) })));

  const choices = ['read', 'rules', 'all', 'audit'];
  // What to do instead, per refused change.
  function fix(code, params, vs) {
    switch (code) {
      case 'ambiguous_approve': {
        const ok = (Array.isArray(params?.choices) ? params.choices : []).filter((c) => choices.includes(c));
        return ok.length ? t('ambiguity.fix.approve', { choices: list(ok.map((c) => t('ambiguity.choice.quoted', { choice: t('ambiguity.choice.' + c) }))) }) : t('ambiguity.fix.approveNone');
      }
      case 'ambiguous_attach': return t(vs.every((a) => a.fix === 'enforce') ? 'ambiguity.fix.attachEnforce' : 'ambiguity.fix.attach');
      case 'ambiguous_providers': return t('ambiguity.fix.oneProvider');
      case 'ambiguous_presets': return t('ambiguity.fix.removeOne');
      case 'ambiguous_scope': return '';
    }
    const a = vs[0];
    return a.provider ? t('ambiguity.fix.provider', { wants: wants(a) }) : t('ambiguity.fix.rule', { wants: wants(a), rule: a.rule });
  }

  // Lead params: strings only (a preset, a rule, a provider).
  function plain(params) {
    const out = {};
    if (params && typeof params === 'object') for (const [k, v] of Object.entries(params).slice(0, 16)) if (typeof v === 'string' || typeof v === 'number') out[k] = String(v).slice(0, 300);
    return out;
  }
  const ambiguityMessage = (code, params, vs) =>
    t('ambiguity.message', { lead: t('error.' + code, plain(params)), pairs: sentences(vs.map(pair)), why: t('ambiguity.why'), fix: fix(code, params, vs) }).trim();

  function credentialText(attach, cs) {
    const c = cs[0];
    const endpoints = list(cs.map((x) => t('credential.endpoint', { rule: x.rule, where: x.host + ':' + x.ports.join(','), mode: x.mode })));
    return t(attach ? 'credential.attach' : 'error.credential_uninspected', { provider: providerLabel({ name: c.provider, label: c.label, profile: c.profile }), endpoints });
  }

  // The gateway's ambiguity refusal, from its pairs.
  const gatewayText = (pairs) => pairs.length ? t('ambiguity.gateway', { pairs: sentences(pairs.map(pair)) }) : t('error.gateway_ambiguous');

  // The text of a refusal: code and params as the server sent them, with
  // what came with them (ambiguities, conflicts, the gateway's pairs);
  // fallback (the server's text) for what the page cannot word.
  function message(code, params, parts, fallback) {
    const vs = parts?.ambiguities || [], cs = parts?.conflicts || [], gateway = parts?.gateway;
    if (typeof code !== 'string') return fallback;
    if (Array.isArray(gateway)) {
      if (code === 'gateway_ambiguous') return gatewayText(gateway);
      // The gateway's refusal is the detail of what failed.
      if (params && typeof params.detail === 'string') params = { ...params, detail: gatewayText(gateway) };
    }
    // These codes are worded from what came with them, or not at all.
    if (/^ambiguous_[a-z_]+$/.test(code)) return has('error.' + code) && vs.length ? ambiguityMessage(code, params, vs) : fallback;
    if (code === 'provider_attach_refused') {
      if (!cs.length && !vs.length) return fallback;
      return [cs.length ? credentialText(true, cs) : '', vs.length ? ambiguityMessage('ambiguous_attach', params, vs) : ''].filter(Boolean).join(' ');
    }
    if (code === 'provider_scope_conflict') return cs.length ? t('error.provider_scope_conflict', { sandbox: plain(params).sandbox || '', message: credentialText(true, cs) }) : fallback;
    if (code === 'credential_uninspected') return cs.length ? credentialText(false, cs) : fallback;
    if (code === 'credential_uninspected_presets') return cs.length ? t('error.credential_uninspected_presets', { message: credentialText(false, cs) }) : fallback;
    // What the sandbox is busy with, in the page's language.
    if (code === 'reset_sandbox_busy' && /^busy_[a-z_]+$/.test(plain(params).op || '') && has('error.' + params.op)) params = { ...params, busy: t('error.' + params.op).replace(/[。.]$/, '') };
    return I() ? I().serverError(code, params, fallback) : fallback;
  }

  // Why an approve choice cannot be taken.
  function blocked(choice, vs, fallback) {
    if (!vs.length || !choices.includes(choice)) return fallback;
    const parts = vs.map((a) => t('ambiguity.blockedPair', { endpoint: a.endpoint, owner: owner(a.rule, a.provider), details: list(details(a)) }));
    return t('ambiguity.blocked', { choice: t('ambiguity.choice.' + choice), pairs: sentences(parts) });
  }

  window.IwatoRefusals = Object.freeze({ message, blocked, details });
})();
