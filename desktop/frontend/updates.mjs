// This page is bundled with the app. Release notes are always plain text.
export function createUpdateController({ invoke, channel, render, checkTimeoutMs = 35000 }) {
  // A lost native reply must never leave version checks disabled indefinitely.
  // Only read-only checks use this UI timeout; installs must not be treated as
  // cancelled while the native installer might still be changing files.
  const boundedCheck = async promise => {
    let timer
    try {
      return await Promise.race([promise, new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error('检查更新超时，请检查网络后重试')), checkTimeoutMs)
      })])
    } finally { clearTimeout(timer) }
  }
  let view = {phase:'idle', current_version:'', preview:false, notes:'', reason:'', version:null, installable:false, consent:false}
  let busy = false
  let operation = 0
  const emit = patch => { view = {...view, ...patch}; render({...view, busy}); return view }
  const run = async (phase, work) => {
    if (busy) return false
    busy = true; operation++; emit({phase, consent:false})
    try { await work(); return true }
    catch (e) { emit({phase:'error', reason:String(e), installable:false, consent:false}); return false }
    finally { busy = false; emit({}) }
  }
  return {
    async initialize() {
      try { emit(await boundedCheck(invoke('updater_info'))); return true }
      catch (e) { emit({phase:'error',reason:String(e)}); return false }
    },
    check(preview = view.preview) {
      return run('checking', async () => {
        emit({version:null, notes:'', installable:false, preview})
        const result = await boundedCheck(invoke('check_update', {preview}))
        emit({...result, phase:result.version ? 'available' : 'current'})
      })
    },
    download() {
      if (view.phase !== 'available' || !view.installable) return Promise.resolve(false)
      return run('downloading', async () => {
        const active = operation
        emit({received:0, total:0})
        const onEvent = channel(data => {
          if (operation !== active || !busy || !['downloading','verifying'].includes(view.phase)) return
          if (data.event === 'downloading') emit({received:data.received,total:data.total})
          else if (data.event === 'verifying') emit({phase:'verifying'})
        })
        await invoke('download_update', {onEvent})
        emit({phase:'ready', reason:'更新包签名和版本已验证。保存编辑内容后确认安装。'})
      })
    },
    consent(value) { if (!busy && view.phase === 'ready') emit({consent:!!value}) },
    install() {
      if (view.phase !== 'ready' || !view.consent) return Promise.resolve(false)
      return run('installing', async () => {
        await invoke('install_update', {confirmed:true})
        emit({phase:'installed', reason:'安装完成，正在重新启动。'})
      })
    },
    getState: () => ({...view,busy}),
  }
}

if (typeof document !== 'undefined') {
  const el = id => document.getElementById(id)
  const labels = {checking:'正在检查新版本…',downloading:'正在下载更新…',verifying:'正在验证更新包签名…',installing:'正在关闭本机后端并安装更新，请勿强行关机…'}
  const controller = createUpdateController({
    invoke:(command,args) => window.__TAURI__.core.invoke(command,args),
    channel:listener => {const channel=new window.__TAURI__.core.Channel();channel.onmessage=listener;return channel},
    render(state) {
      el('current').textContent=state.current_version||'—'
      el('available').textContent=state.version?` → ${state.version}`:''
      el('preview').checked=state.preview;el('preview').disabled=state.busy
      el('status').textContent=state.phase==='downloading'&&state.total?`正在下载更新… ${Math.min(100,Math.floor((state.received||0)*100/state.total))}%`:labels[state.phase]||state.reason
      el('check').disabled=state.busy
      el('notes').hidden=!state.notes;el('notes').textContent=state.notes||''
      el('download').hidden=state.phase!=='available'||!state.installable
      el('download').disabled=state.busy
      el('progress').hidden=!['downloading','verifying'].includes(state.phase)
      if(state.total){el('progress').max=state.total;el('progress').value=state.received||0}else el('progress').removeAttribute('value')
      el('consentRow').hidden=state.phase!=='ready';el('consent').checked=state.consent
      el('install').hidden=state.phase!=='ready';el('install').disabled=!state.consent||state.busy
    }
  })
  el('check').onclick=()=>controller.check(el('preview').checked)
  el('preview').onchange=()=>controller.check(el('preview').checked)
  el('download').onclick=()=>controller.download()
  el('consent').onchange=()=>controller.consent(el('consent').checked)
  el('install').onclick=()=>controller.install()
  controller.initialize().then(ok=>{if(ok)controller.check()})
}
