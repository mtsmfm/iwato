'use strict';
// Password managers offer to fill every field: they ignore autocomplete=off
// and honour only their own opt-outs. Every field but a password one gets
// them, including fields added later (outside the terminals,
// which redraw on every frame; term.js marks its input).
(() => {
  const OPT_OUT = [['data-1p-ignore', ''], ['data-lpignore', 'true'], ['data-bwignore', ''], ['data-form-type', 'other']];
  const FIELDS = 'input:not([type=password]):not([type=hidden]),textarea,[contenteditable=""],[contenteditable=true]';
  const mark = (el) => { for (const [k, v] of OPT_OUT) if (!el.hasAttribute(k)) el.setAttribute(k, v); };
  const scan = (node) => {
    if (node.nodeType !== 1) return;
    if (node.matches(FIELDS)) mark(node);
    if (node.firstElementChild) for (const el of node.querySelectorAll(FIELDS)) mark(el);
  };
  window.IwatoAutofill = { mark };
  const start = () => {
    scan(document.body);
    new MutationObserver((records) => {
      for (const r of records) {
        if (r.target.closest?.('.iw-term')) continue;
        for (const n of r.addedNodes) scan(n);
      }
    }).observe(document.body, { childList: true, subtree: true });
  };
  if (document.body) start(); else document.addEventListener('DOMContentLoaded', start);
})();
