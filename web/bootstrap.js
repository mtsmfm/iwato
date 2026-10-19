/* Captures the launch token from the URL's hash and removes it from the URL
   before the application loads. */
(() => {
  'use strict';
  let token = null;
  let invalid = false;
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
