(function recoverWorkbench(origin) {
  if (location.origin !== origin || !['/', '/index.html'].includes(location.pathname)) return
  function recover() {
    // A stopped sidecar can serve the document and then disappear before the
    // WebView gets its scripts. A running editor must retain its drafts.
    if (document.documentElement.dataset.workbenchReady !== '1') {
      location.reload()
    } else if (!window.__omnishareRuntimeBridge) {
      const script = document.createElement('script')
      script.src = '/runtime-bridge.js'
      document.head.appendChild(script)
    }
  }
  if (document.readyState === 'complete') recover()
  else window.addEventListener('load', recover, {once:true})
})
