// agendarr service worker.
// App shell: cache first. Calendar data (/api/): network first, falling back to
// the last good response so the calendar still opens offline. Pages: network
// first, falling back to /offline.
const VERSION = "agendarr-v1";
const SHELL = ["/offline", "/static/css/app.css", "/static/js/app.js", "/static/icons/icon.svg"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(VERSION).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== VERSION).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== "GET" || url.origin !== location.origin) return;
  // Never cache auth flows or the iCal feed.
  if (url.pathname.startsWith("/auth/") || url.pathname.startsWith("/ics/")) return;

  if (url.pathname.startsWith("/static/") || url.pathname.startsWith("/img/")) {
    e.respondWith(
      caches.match(req).then((hit) => hit || fetch(req).then((res) => {
        if (res.ok) { const copy = res.clone(); caches.open(VERSION).then((c) => c.put(req, copy)); }
        return res;
      })),
    );
    return;
  }
  if (url.pathname.startsWith("/api/")) {
    e.respondWith(
      fetch(req).then((res) => {
        if (res.ok) { const copy = res.clone(); caches.open(VERSION).then((c) => c.put(req, copy)); }
        return res;
      }).catch(() => caches.match(req).then((hit) => hit || Response.error())),
    );
    return;
  }
  if (req.mode === "navigate") {
    e.respondWith(fetch(req).catch(() => caches.match("/offline")));
  }
});
