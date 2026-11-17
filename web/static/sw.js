// The server fills in {{VERSION}} with a hash of the static files.
const CACHE = "ingle-static-{{VERSION}}";

self.addEventListener("install", () => self.skipWaiting());

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

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
