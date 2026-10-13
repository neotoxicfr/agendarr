// Agendarr service worker. The server stamps the release in place of
// __VERSION__, so every release starts from fresh caches.
//   shell   /static/…      cache first (URLs carry ?v=<release>)
//   posters /img/…         stale-while-revalidate, at most 300 kept
//   data    /api/…         network first, last good answer offline, 20 kept
//   pages                  network first, /offline when the network is gone
const VERSION = "__VERSION__";
const SHELL = `shell-${VERSION}`;
const IMG = `img-${VERSION}`;
const API = `api-${VERSION}`;
const PRECACHE = ["/offline", `/static/css/app.css?v=${VERSION}`, `/static/js/app.js?v=${VERSION}`,
  `/static/js/calendar.js?v=${VERSION}`, `/static/js/formats.js?v=${VERSION}`, `/static/icons/icon.svg?v=${VERSION}`];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(SHELL).then((c) => c.addAll(PRECACHE)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  const keep = [SHELL, IMG, API];
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => !keep.includes(k)).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

// trim keeps the newest max entries of a cache (keys come back in insertion order).
async function trim(name, max) {
  const c = await caches.open(name);
  const keys = await c.keys();
  await Promise.all(keys.slice(0, Math.max(0, keys.length - max)).map((k) => c.delete(k)));
}

async function put(name, req, res, max) {
  const c = await caches.open(name);
  await c.delete(req); // re-insert so the entry counts as newest
  await c.put(req, res);
  await trim(name, max);
}

self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  // Signing out (a POST navigation) drops cached calendar data, so the next
  // person on a shared device never sees it offline.
  if (url.origin === location.origin && url.pathname === "/logout") { e.waitUntil(caches.delete(API)); return; }
  if (req.method !== "GET" || url.origin !== location.origin) return;
  // Never cache auth flows or the iCal feed.
  if (url.pathname.startsWith("/auth/") || url.pathname.startsWith("/ics/")) return;

  if (url.pathname.startsWith("/static/")) {
    e.respondWith(caches.match(req).then((hit) => hit || fetch(req).then((res) => {
      if (res.ok) { const copy = res.clone(); caches.open(SHELL).then((c) => c.put(req, copy)); }
      return res;
    })));
    return;
  }
  if (url.pathname.startsWith("/img/")) {
    // Serve the cached poster at once, refresh it behind (a poster changed
    // in Radarr shows up on the next visit).
    e.respondWith(caches.open(IMG).then((c) => c.match(req)).then((hit) => {
      const net = fetch(req).then((res) => {
        if (res.ok) e.waitUntil(put(IMG, req, res.clone(), 300));
        return res;
      });
      if (hit) { e.waitUntil(net.catch(() => {})); return hit; }
      return net;
    }));
    return;
  }
  if (url.pathname.startsWith("/api/")) {
    e.respondWith(fetch(req).then((res) => {
      if (res.ok) e.waitUntil(put(API, req, res.clone(), 20));
      return res;
    }).catch(() => caches.match(req).then((hit) => hit || Response.error())));
    return;
  }
  if (req.mode === "navigate") {
    e.respondWith(fetch(req).catch(() => caches.match("/offline")));
  }
});

// Web Push: a release someone cares about became available or was upgraded.
self.addEventListener("push", (e) => {
  let msg = {};
  try { msg = e.data ? e.data.json() : {}; } catch { /* not ours */ }
  e.waitUntil(Promise.all([
    self.registration.showNotification(msg.title || "Agendarr", {
      body: msg.body || "", tag: msg.tag, icon: msg.icon || "/static/icons/icon-192.png", badge: "/static/icons/icon-192.png",
      data: { url: msg.url || "/" },
    }),
    self.clients.matchAll({ type: "window" }).then((list) => list.forEach((c) => c.postMessage("agendarr:notification"))),
  ]));
});

self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const url = new URL((e.notification.data && e.notification.data.url) || "/", self.location.origin);
  if (url.origin !== self.location.origin) return;
  e.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((list) => {
    const open = list.find((c) => new URL(c.url).origin === url.origin);
    return open ? open.focus().then((c) => c.navigate(url.href)) : self.clients.openWindow(url.href);
  }));
});
