const CACHE = "scrimshaw-reader-v2";

self.addEventListener("install", event => {
  event.waitUntil(caches.open(CACHE).then(cache => cache.addAll(["/", "/manifest.webmanifest", "/app.css", "/app.js"])));
  self.skipWaiting();
});

self.addEventListener("activate", event => event.waitUntil(
  caches.keys().then(keys => Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k)))).then(() => self.clients.claim())
));

self.addEventListener("fetch", event => {
  if (event.request.method !== "GET") return;
  const url = new URL(event.request.url);
  if (url.origin !== self.location.origin) return;
  const cacheable = url.pathname === "/" || url.pathname.startsWith("/items/") ||
    url.pathname === "/app.css" || url.pathname === "/app.js" || url.pathname === "/manifest.webmanifest";
  event.respondWith(fetch(event.request).then(response => {
    if (cacheable && response.ok) {
      const copy = response.clone();
      caches.open(CACHE).then(cache => cache.put(event.request, copy));
    }
    return response;
  }).catch(() => caches.match(event.request)));
});
