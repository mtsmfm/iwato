/* Captures the launch token from the URL's hash and removes it from the URL
   before the application loads. */
(() => {
  'use strict';
  let token = null;
  let invalid = false;
  // The installed app opened through its protocol handler (manifest).
  const query = new URLSearchParams(location.search);
  if (query.has('launch')) {
    const match = /^web\+iwato:([a-f0-9]{64})$/.exec(query.get('launch'));
    if (match) token = match[1];
    query.delete('launch');
    const rest = query.toString();
    history.replaceState(null, '', location.pathname + (rest ? '?' + rest : '') + location.hash);
  }
  if (location.hash) {
    const values = new URLSearchParams(location.hash.slice(1));
    const supplied = values.get('token');
    if (supplied !== null) {
      invalid = !/^[a-fA-F0-9]{64}$/.test(supplied);
      if (!invalid) token = supplied;
    }
    history.replaceState(null, '', location.pathname + location.search);
  }
  window.IwatoLaunch = Object.freeze({
    consume() {
      const result = { token, invalid };
      token = null;
      invalid = false;
      delete window.IwatoLaunch;
      return result;
    }
  });
})();
