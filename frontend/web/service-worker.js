const CACHE='omnishare-v1.3.0-rc1'
const STATIC_ASSETS=new Set(['/','/index.html','/style.css','/app.js','/manifest.webmanifest','/icon.svg'])
self.addEventListener('install',event=>event.waitUntil(caches.open(CACHE).then(cache=>cache.addAll([...STATIC_ASSETS])).then(()=>self.skipWaiting())))
self.addEventListener('activate',event=>event.waitUntil(caches.keys().then(keys=>Promise.all(keys.filter(key=>key!==CACHE).map(key=>caches.delete(key)))).then(()=>self.clients.claim())))
self.addEventListener('fetch',event=>{
  if(event.request.method!=='GET')return
  const url=new URL(event.request.url)
  if(url.origin!==self.location.origin||!STATIC_ASSETS.has(url.pathname))return
  event.respondWith(fetch(event.request,{cache:'no-cache'}).then(response=>{
    if(response.ok&&response.type==='basic')caches.open(CACHE).then(cache=>cache.put(event.request,response.clone()))
    return response
  }).catch(()=>caches.match(event.request).then(response=>response||caches.match('/'))))
})
