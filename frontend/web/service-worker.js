const CACHE='omnishare-v1.0.1-rc.6-file-workspace'
const STATIC_ASSETS=new Set(['/','/index.html','/style.css','/theme.css','/theme.js','/runtime-bridge.js','/app.js','/manifest.webmanifest','/icon.svg'])
self.addEventListener('install',event=>event.waitUntil(caches.open(CACHE).then(cache=>cache.addAll([...STATIC_ASSETS])).then(()=>self.skipWaiting())))
self.addEventListener('activate', event => event.waitUntil(
  caches.keys().then(keys => Promise.all(keys.filter(key => key.startsWith('omnishare-v') && key !== CACHE)
    .map(key => caches.delete(key)))).then(() => self.clients.claim())
))
self.addEventListener('fetch',event=>{
  if(event.request.method!=='GET')return
  const url=new URL(event.request.url)
  if(url.origin!==self.location.origin||!STATIC_ASSETS.has(url.pathname))return
  event.respondWith((async () => {
    try {
      const response = await fetch(event.request, {cache:'no-cache'})
      if (response.ok && response.type === 'basic' && !url.search) {
        const copy = response.clone()
        event.waitUntil(caches.open(CACHE).then(cache => cache.put(url.pathname, copy)).catch(() => {}))
      }
      return response
    } catch {
      const cache = await caches.open(CACHE)
      const stored = await cache.match(url.pathname)
      if (stored) return stored
      if (event.request.mode === 'navigate') return await cache.match('/') || Response.error()
      return Response.error()
    }
  })())
})
