import assert from 'node:assert/strict'
import {readFile} from 'node:fs/promises'
import test from 'node:test'
import vm from 'node:vm'

const source = (await readFile(new URL('../web/app.js', import.meta.url),'utf8')).replace(/\nstart\(\)\s*$/, '\n')
function appWith(fetcher) {
  const elements = new Map(), requests = [], details = [], downloads = []
  const element = selector => {
    if (!elements.has(selector)) elements.set(selector, {
      innerHTML:'',textContent:'',value:'',hidden:false,disabled:false,src:'',open:false,
      classList:{toggle(){},add(){},remove(){}},
      querySelectorAll: query => query.includes('[open]') ? details.filter(d=>d.open) : details,
      showModal(){this.open=true},close(){this.open=false},play:async()=>{},
    })
    return elements.get(selector)
  }
  const context = vm.createContext({URL,URLSearchParams,AbortController,Blob,FormData,TextEncoder,btoa,
    location:new URL('http://100.92.203.77:8084/'),history:{replaceState(){}},
    sessionStorage:{getItem:()=>null},window:{},console,setTimeout:()=>1,clearTimeout(){},
    document:{querySelector:element,querySelectorAll:()=>[],body:{appendChild(){}},
      createElement:()=>({click(){downloads.push(this.href)},remove(){}})},
    fetch:async(url,init={})=>{requests.push({url,method:init.method||'GET'});return fetcher?fetcher(url,init):Response.json({code:0,data:[]})}
  })
  vm.runInContext(source,context)
  const app=vm.runInContext('({state,renderDevices,displayDevices,deviceRouteLabel,deviceAddresses,renderFiles,loadFiles,openDownload,playFile,updateUploadSummary,fileAvailability})',context)
  return {app,element,requests,details,downloads}
}
const local={id:'node-aaaaaaaaaaaaaaaaaaaa',hostname:'OmniShare',is_local:true,online:true,bound:true,
 url:'http://192.168.31.200:8084',addresses:['http://100.92.203.77:8084','http://[fd7a:115c:a1e0::2637:cb4e]:8084','http://192.168.64.1:8084','http://192.168.216.1:8084','http://192.168.31.200:8084','http://172.30.96.1:8084']}
const remote={id:'node-bbbbbbbbbbbbbbbbbbbb',hostname:'OmniShare',bound:true,online:true,url:'http://100.64.162.25:8081',addresses:['http://100.64.162.25:8081','http://192.168.31.99:8081']}

test('one identity has one compact card, all alternate routes are collapsed',()=>{
 const {app,element,requests}=appWith();app.state.devices=[local,remote,{...remote,bound:false,url:'http://192.168.31.99:8081'}];app.renderDevices()
 const html=element('#deviceList').innerHTML
 assert.equal((html.match(/class="device device-compact"/g)||[]).length,2)
 const visible=html.replace(/<details[\s\S]*?<\/details>/g,'')
 assert.match(visible,/100\.92\.203\.77:8084/);assert.match(visible,/100\.64\.162\.25:8081/)
 for(const ip of ['192.168.64.1','192.168.216.1','192.168.31.200','172.30.96.1','fd7a:115c','192.168.31.99']) assert.ok(!visible.includes(ip),ip+' exposed outside details')
 assert.match(html,/6 个地址/);assert.match(html,/2 个地址/);assert.doesNotMatch(html,/<details[^>]*\sopen(?:[\s=>])/)
 assert.equal(requests.length,0)
})
test('same names do not merge distinct machines; live bound identity wins over an alias',()=>{
 const {app}=appWith();const list=app.displayDevices([local,{...remote,online:false,bound:false},remote])
 assert.equal(list.length,2);assert.equal(list[1].bound,true);assert.equal(list[1].online,true)
 assert.equal(list[1].addresses.length,2)
})
test('route classifications handle the complete Tailscale range, not all 100.* addresses',()=>{
 const {app}=appWith()
 assert.equal(app.deviceRouteLabel('http://100.127.255.254:8081'),'Tailscale')
 assert.equal(app.deviceRouteLabel('http://100.128.1.2:8081'),'局域网')
 assert.equal(app.deviceRouteLabel('http://[fd7a:115c:a1e0::2]:8081'),'Tailscale')
 assert.equal(app.deviceRouteLabel('http://127.0.0.1:8081'),'本机')
})
test('expanded connection details survive periodic device refresh',()=>{
 const {app,details}=appWith();app.state.devices=[local,remote];app.renderDevices()
 details.push({dataset:{deviceId:remote.id},open:true},{dataset:{deviceId:local.id},open:false})
 app.state.devices=[local,{...remote,online:false,connection_error:'断开'}];app.renderDevices()
 assert.equal(details[0].open,true);assert.equal(details[1].open,false)
})
test('file polling reads only the metadata endpoint, with no ticket, preview, or upload',async()=>{
 const file={id:remote.id+'~f-one',file_name:'远端电影.mp4',file_size:5e9,mime_type:'video/mp4',is_video:true,source_online:true,source_node_name:'B'}
 const {app,element,requests}=appWith(()=>Response.json({code:0,data:[file]},{headers:{'X-Total-Count':'1'}}))
 for(let n=0;n<3;n++)await app.loadFiles()
 assert.equal(requests.length,3)
 assert.ok(requests.every(r=>r.method==='GET'&&r.url.startsWith('/api/v1/files?')))
 assert.match(element('#filesList').innerHTML,/远端索引 · 按需读取/)
 assert.doesNotMatch(element('#filesList').innerHTML,/<(?:img|video|source)\b/)
 assert.equal(app.state.uploadQueue.length,0)
 app.updateUploadSummary();assert.equal(element('#uploadActivityPanel').hidden,true)
})
test('only explicit download or playback requests a content ticket',async()=>{
 const {app,requests,downloads,element}=appWith(()=>Response.json({code:0,data:{url:'/peer-media/test-ticket'}}))
 const file={id:remote.id+'~f-one',file_name:'文件.mp4',is_video:true}
 app.state.files=[file];app.renderFiles();assert.equal(requests.length,0)
 await app.openDownload(file);assert.equal(requests.length,1);assert.equal(requests[0].method,'POST');assert.match(requests[0].url,/\/ticket$/)
 assert.deepEqual(downloads,['/peer-media/test-ticket'])
 await app.playFile(file);assert.equal(requests.length,2);assert.equal(element('#videoPlayer').src,'/peer-media/test-ticket')
})
test('offline remote indexes cannot imply a locally downloaded copy',()=>{
 const {app,element}=appWith();app.state.files=[{id:remote.id+'~f-one',file_name:'offline.mp4',source_online:false,is_video:true}];app.renderFiles()
 assert.match(element('#filesList').innerHTML,/远端索引 · 来源离线/)
 assert.match(element('#filesList').innerHTML,/download-file" disabled/)
 assert.match(element('#filesList').innerHTML,/play-file" disabled/)
})
test('the service worker caches only static application assets, never peer content',async()=>{
 const text=await readFile(new URL('../web/service-worker.js',import.meta.url),'utf8')
 const assets=vm.runInNewContext(text.match(/const STATIC_ASSETS=([^\n]+)/)[1],{Set})
 for(const path of ['/api/v1/files','/media/file','/peer-media/ticket','/api/v1/files/id/download'])assert.equal(assets.has(path),false)
 assert.match(text,/!STATIC_ASSETS\.has\(url\.pathname\)/)
})
