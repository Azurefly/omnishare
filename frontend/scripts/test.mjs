import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'
const root=resolve(import.meta.dirname,'..')
const [html,js,runtimeBridge,manifest,sw,packageText]=await Promise.all(['web/index.html','web/app.js','web/runtime-bridge.js','web/manifest.webmanifest','web/service-worker.js','package.json'].map(p=>readFile(resolve(root,p),'utf8')))
const requiredIds=['notesPanel','filesPanel','padsPanel','sharesPanel','trashPanel','activityPanel','settingsDialog','authDialog','shareDialog','videoDialog','cfgAllowLAN','cfgListenAddress','cfgPublicBaseURL','cfgAllowedOrigins','noteMaxReads','editNoteBurn','editNoteMaxReads']
for(const id of requiredIds) if(!html.includes(`id="${id}"`)) throw new Error(`missing UI id ${id}`)
const routes=['/api/v1/dashboard','/api/v1/notes','/api/v1/files','/api/v1/pads','/api/v1/devices','/api/v1/shares','/api/v1/trash','/api/v1/audit','/api/v1/backup','/ticket']
for(const route of routes) if(!js.includes(route)) throw new Error(`missing route ${route}`)
for(const forbidden of ['localStorage','authorizedURL','?key=']) if(js.includes(forbidden)) throw new Error(`unsafe frontend pattern remains: ${forbidden}`)
if(!js.includes('sessionStorage'))throw new Error('access key is not session-scoped')
if(!js.includes("'X-OmniShare-Confirm'='CLEAR-ACCESS-KEY'")&&!js.includes("requestHeaders['X-OmniShare-Confirm']='CLEAR-ACCESS-KEY'"))throw new Error('access-key clearing confirmation missing')
if(!js.includes('state.activeUploads<3'))throw new Error('upload concurrency limit missing')

if(!js.includes('loadHealth(true)')||!js.includes('loadConfig(true)')||!js.includes('reportLoadError(err, strict)'))throw new Error('strict initialization failure reporting missing')
if(js.includes('state.config.data_dir')||js.includes('.data_dir'))throw new Error('private data directory is exposed in frontend')
if(!html.includes('路径未通过 API 暴露'))throw new Error('private storage path disclosure text missing')
if(!js.includes('omnishare.padDraft.'))throw new Error('conflict draft preservation missing')
if(!html.includes('<script src="/runtime-bridge.js"></script>'))throw new Error('runtime bridge must load before the application module')
if(html.indexOf('/runtime-bridge.js')>html.indexOf('/app.js'))throw new Error('runtime bridge must load before app.js')
for(const required of ['retryDelays','scheduleRecovery','collapseDevices','local-primary','后端服务暂不可用'])if(!runtimeBridge.includes(required))throw new Error(`runtime bridge contract missing ${required}`)
if(!runtimeBridge.includes("url.pathname.startsWith('/api/')"))throw new Error('runtime bridge must guard same-origin API requests')
if(!runtimeBridge.includes("url.pathname !== '/api/v1/devices'"))throw new Error('runtime bridge must aggregate device responses')
const parsed=JSON.parse(manifest);if(parsed.display!=='standalone'||!parsed.icons?.length)throw new Error('invalid PWA manifest')
const packageJSON=JSON.parse(packageText)
if(!sw.includes(`omnishare-v${packageJSON.version}`))throw new Error(`service worker cache version missing for ${packageJSON.version}`)
for(const sensitive of ["'/s/'","'/media/'","'/api/'","'/n/'"])if(sw.includes(sensitive))throw new Error(`service worker mentions sensitive route ${sensitive}`)
if(!sw.includes('STATIC_ASSETS.has(url.pathname)'))throw new Error('service worker is not static-allowlist based')
console.log('Frontend security, recovery and contract checks passed')
