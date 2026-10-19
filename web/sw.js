/* Push-only service worker: no caching, no fetch interception. */
'use strict';
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));
self.addEventListener('push', (event) => {
  let data = {};
  try { data = event.data ? event.data.json() : {}; } catch (_) { data = {}; }
  const text = (value, max) => (typeof value === 'string' ? value : '').slice(0, max);
  const options = { body: text(data.body, 300), icon: '/icon-192.png', badge: '/icon-192.png' };
  if (typeof data.tag === 'string' && data.tag) options.tag = text(data.tag, 64);
  options.data = { path: text(data.path, 200), local: text(data.local, 64) };
  const shown = self.registration.showNotification(text(data.title, 100) || 'Iwato', options);
  // The installed app's badge: proposals waiting for the owner, as of this push.
  const badge = Number.isInteger(data.badge) && data.badge >= 0 && typeof self.navigator.setAppBadge === 'function'
    ? (data.badge ? self.navigator.setAppBadge(data.badge) : self.navigator.clearAppBadge()).catch(() => {}) : null;
  event.waitUntil(Promise.all([shown, badge]));
});
// A click opens the sandbox (and its tab) the notification is about. A
// service worker registered on the PC's loopback origin opens the server's
// current port (it changes per launch); others open their own origin.
const loopback = /^http:\/\/(127\.0\.0\.1|localhost|\[::1\]):\d{1,5}$/;
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const data = event.notification.data || {};
  const path = /^\/\?[A-Za-z0-9=&%._-]{0,200}$/.test(data.path || '') ? data.path : '/';
  const origin = loopback.test(self.location.origin) && loopback.test(data.local || '') ? data.local : self.location.origin;
  const target = new URL(path, origin);
  event.waitUntil(self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((list) => {
    for (const client of list) {
      if (new URL(client.url).origin !== target.origin || !('focus' in client)) continue;
      client.postMessage({ type: 'open', search: target.search });
      return client.focus();
    }
    return self.clients.openWindow(target.href);
  }));
});
