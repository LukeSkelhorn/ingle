// Ingle service worker: makes the board installable and caches static
// assets. Pages and data always come from the server (network-first) so
// the wall display never shows stale chores.
// The server fills in the cache name from a hash of the static files, so
// every build that changes them starts a fresh cache and drops the old one.
const CACHE = "ingle-static-{{VERSION}}";

self.addEventListener("install", () => self.skipWaiting());

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

// Static assets: from the cache if there, otherwise fetched and kept.
self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== "GET" || url.origin !== self.location.origin) return;
  if (!url.pathname.startsWith("/static/")) return;
  event.respondWith(
    caches.open(CACHE).then((c) =>
      c.match(event.request).then((hit) =>
        hit || fetch(event.request).then((res) => {
          if (res.ok) c.put(event.request, res.clone());
          return res;
        })
      )
    )
  );
});
