self.addEventListener('install', event => {
  event.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', event => {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('push', event => {
  const payload = event.data.json();
  event.waitUntil(self.registration.showNotification(payload.title, payload.options));
});

self.addEventListener('notificationclick', event => {
  event.notification.close();
  const session = event.notification.data?.session;
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({type: 'window', includeUncontrolled: true});
    const client = windows.find(client => new URL(client.url).pathname === '/');
    if (client) {
      await client.focus();
      if (session) client.postMessage({type: 'notification.clicked', session});
      return;
    }
    const url = new URL('/', self.location.origin);
    if (session) {
      url.searchParams.set('namespace', session.namespace);
      url.searchParams.set('session', session.name);
      url.searchParams.set('uid', session.uid || '');
    }
    await self.clients.openWindow(url.href);
  })());
});
