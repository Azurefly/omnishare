import test from 'node:test'
import assert from 'node:assert/strict'
import {createUpdateController} from '../frontend/updates.mjs'
function fixture(handler) {
  const calls=[],states=[]
  const app=createUpdateController({invoke:async(name,args)=>{calls.push([name,args]);return handler(name,args)},channel:f=>({onmessage:f}),render:s=>states.push(s)})
  return {app,calls,states}
}
const available={current_version:'0.2.0-rc.2',version:'0.2.0-rc.3',preview:true,notes:'<script>bad()</script>',installable:true,reason:'ready'}
test('check never downloads or installs, notes stay plain data',async()=>{
 const {app,calls}=fixture(()=>available);await app.check(true)
 assert.deepEqual(calls.map(c=>c[0]),['check_update']);assert.equal(app.getState().notes,available.notes)
 assert.equal(await app.install(),false)
})
test('download verifies before offering explicit install consent',async()=>{
 const {app,calls}=fixture((n,a)=>{if(n==='check_update')return available;if(n==='download_update'){a.onEvent.onmessage({event:'downloading',received:5,total:5});a.onEvent.onmessage({event:'verifying'})}})
 await app.check();await app.download();assert.equal(app.getState().phase,'ready')
 assert.equal(await app.install(),false);app.consent(true);await app.install()
 assert.deepEqual(calls.map(c=>c[0]),['check_update','download_update','install_update'])
 assert.deepEqual(calls[2][1],{confirmed:true})
})
test('bad signatures and network failures never reach install',async()=>{
 const {app,calls}=fixture(n=>{if(n==='check_update')return available;throw Error('signature mismatch')})
 await app.check();await app.download();app.consent(true);assert.equal(await app.install(),false)
 assert.equal(app.getState().phase,'error');assert.equal(calls.length,2)
})
test('unsigned/unconfigured releases cannot be installed',async()=>{
 const {app,calls}=fixture(()=>({...available,installable:false}));await app.check()
 assert.equal(await app.download(),false);assert.equal(await app.install(),false);assert.equal(calls.length,1)
})
test('concurrent clicks are single-flight',async()=>{
 let finish;const {app,calls}=fixture(()=>new Promise(r=>finish=r))
 const first=app.check();assert.equal(await app.check(),false);assert.equal(calls.length,1)
 finish(available);await first;assert.equal(app.getState().phase,'available')
})
test('failed recheck clears old candidate instead of reporting up to date',async()=>{
 let count=0;const {app}=fixture(()=>{if(count++===0)return available;throw Error('rate limited')})
 await app.check();await app.check();assert.equal(app.getState().version,null)
 assert.equal(app.getState().phase,'error');assert.equal(app.getState().installable,false)
})
