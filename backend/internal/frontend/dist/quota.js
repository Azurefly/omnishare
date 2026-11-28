(() => {
  const $ = id => document.getElementById(id)
  const endpoint = '/api/v1/ai-quota'
  const localHost = ['localhost', '127.0.0.1', '[::1]'].includes(location.hostname)
  let api, askConfirm, withButtonBusy, providers = [], request = 0, saving = false, loadedAt = 0
  const lastRead = new Map()
  function element(tag, text, className) {
    const node = document.createElement(tag)
    if (text !== undefined) node.textContent = text
    if (className) node.className = className
    return node
  }
  function option(value, label) {
    const item = element('option', label)
    item.value = value
    return item
  }
  const time = seconds => new Date(seconds * 1000).toLocaleString()
  function valueOf(meter) {
    if (meter.unlimited) return '不限额'
    if (!Number.isFinite(meter.value)) return '暂无读数'
    const unit = meter.unit || ''
    const value = new Intl.NumberFormat(undefined, {maximumFractionDigits: 6}).format(meter.value)
    return value + (unit === '%' ? '% 剩余' : ' ' + unit)
  }
  function summarize(accounts) {
    const current = accounts.find(a => a.reading?.meters?.length && !a.problem)
    if (!current) {
      $('quotaSummary').textContent = accounts.length ? '已连接 ' + accounts.length + ' 个账号，打开查看状态' : '查看账号余额、订阅余量和重置时间'
      return
    }
    const meter = current.reading.meters[0]
    const expired = meter.reset_at && meter.reset_at * 1000 <= Date.now()
    $('quotaSummary').textContent = current.alias + ' · ' + meter.label + ' ' + (expired ? '待刷新' : valueOf(meter)) + ' · ' + time(current.reading.at)
  }
  async function summary() {
    if (!api || !localHost || Date.now() - loadedAt < 60000 || $('quotaDialog').open) return
    loadedAt = Date.now()
    try { summarize((await api(endpoint)).accounts) } catch { /* The dashboard keeps working while quota data is unavailable. */ }
  }
  function fields() {
    const source = providers.find(p => p.id === $('quotaProvider').value)
    if (!source) return
    $('quotaHint').textContent = source.hint
    $('quotaSiteField').hidden = !source.site
    $('quotaSite').required = source.site
    const resource = {gemini:'Google Cloud 项目 ID',xai:'xAI 团队 UUID'}[source.id]
    $('quotaResourceField').hidden = !resource
    $('quotaResource').required = Boolean(resource)
    $('quotaResourceLabel').textContent = resource || ''
    $('quotaKeyField').hidden = source.local
    $('quotaKey').required = !source.local
    $('quotaSessionField').hidden = source.local || source.id === 'gemini'
    if (source.id === 'gemini') $('quotaSession').checked = true
    $('quotaStorage').textContent = source.local ? '从本机官方客户端读取。新连接的账号仅在本机显示。' :
      ($('quotaSession').checked ? '退出 OmniShare 后会移除此账号。' : '密钥保存在本机系统凭据库。新连接的账号仅在本机显示。')
  }
  function hideForm() {
    $('quotaForm').reset()
    $('quotaForm').hidden = true
    $('quotaKey').value = ''
  }
  function json(method, body) {
    return {method, headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)}
  }
  function action(row, label, fn) {
    const button = element('button', label, 'button ghost small')
    button.type = 'button'
    button.addEventListener('click', async () => {
      try { await withButtonBusy(button, '处理中…', fn) }
      catch (error) { $('quotaStatus').textContent = error.message }
    })
    row.append(button)
  }
  function render(data) {
    const list = $('quotaAccounts')
    list.replaceChildren()
    if (!data.accounts.length) {
      list.append(element('p', data.local ? '尚未连接额度账号。' : '这台设备尚未共享额度账号。', 'muted'))
    }
    for (const account of data.accounts) {
      const card = element('article', undefined, 'quota-account')
      const title = element('div', undefined, 'quota-account-head')
      title.append(element('h3', account.alias))
      const provider = providers.find(p => p.id === account.provider)
      title.append(element('span', provider?.name || account.provider, 'muted'))
      card.append(title)
      if (account.reading) {
        const dl = element('dl', undefined, 'quota-meters')
        for (const m of account.reading.meters || []) {
          const metric = element('div')
          const expired = m.reset_at && m.reset_at * 1000 <= Date.now()
          metric.append(element('dt', m.label), element('dd', expired ? '待刷新' : valueOf(m)))
          if (!expired && m.unit === '%' && Number.isFinite(m.value)) {
            const bar = element('meter')
            bar.min = 0
            bar.max = Math.max(100,m.value)
            bar.low = 15
            bar.high = 50
            bar.optimum = bar.max
            bar.value = m.value
            bar.setAttribute('aria-label',m.label + '剩余比例')
            metric.append(bar)
          }
          if (m.reset_at) metric.append(element('small', '重置 ' + time(m.reset_at)))
          dl.append(metric)
        }
        card.append(dl, element('small', '上次读数 ' + time(account.reading.at), 'muted'))
      }
      if (account.problem) card.append(element('p', account.problem, 'quota-error'))
      if (data.local) {
        const actions = element('div', undefined, 'quota-actions')
        const base = endpoint + '/accounts/' + encodeURIComponent(account.id)
        if (!account.removing) action(actions, '刷新额度', async () => {
          try { await api(base + '/refresh', {method:'POST'}) } finally { await load() }
        })
        if (!['antigravity','kiro'].includes(account.provider) && account.reading) action(actions, account.shared ? '停止共享' : '共享给绑定设备', async () => {
          if (!account.shared && !await askConfirm('已绑定设备将能查看「' + account.alias + '」的额度和更新时间。', {title:'共享额度',confirmText:'共享额度'})) return
          await api(base + '/sharing', json('PUT', {shared:!account.shared}))
          await load()
        })
        action(actions, '移除', async () => {
          if (!await askConfirm('移除「' + account.alias + '」的额度连接和 OmniShare 为它保存的密钥？', {title:'移除额度账号',confirmText:'移除',danger:true})) return
          try { await api(base, {method:'DELETE'}) } finally { await load() }
        })
        card.append(actions)
      }
      list.append(card)
    }
  }
  async function load() {
    const seq = ++request
    const target = $('quotaDevice').value
    $('quotaStatus').textContent = '正在读取额度…'
    $('quotaConnect').disabled = true
    $('quotaAccounts').replaceChildren()
    try {
      const data = await api(endpoint + (target ? '?device=' + encodeURIComponent(target) : ''))
      if (seq !== request || !$('quotaDialog').open) return
      providers = data.providers
      const select = $('quotaDevice')
      select.replaceChildren(...data.devices.map((d,i) => option(i ? d.id : '', d.name + (i ? '' : ' · 本机'))))
      select.value = target
      const liveIds = new Set(data.devices.map(d => d.id))
      for (const id of lastRead.keys()) if (!liveIds.has(id)) lastRead.delete(id)
      if (target) lastRead.set(target,data)
      if (data.local) summarize(data.accounts)
      $('quotaConnect').hidden = !data.local
      $('quotaConnect').disabled = false
      $('quotaStatus').textContent = ''
      render(data)
    } catch (error) {
      if (seq !== request || !$('quotaDialog').open) return
      $('quotaConnect').hidden = true
      if (error.status === 404 || error.status === 403) lastRead.delete(target)
      const previous = lastRead.get(target)
      if (previous) render(previous)
      $('quotaStatus').textContent = error.message + (previous ? '。以下是本次打开窗口期间的上次读数。' : '')
    }
  }
  function bind(helpers) {
    if (!localHost) return
    api = helpers.api
    askConfirm = helpers.askConfirm
    withButtonBusy = helpers.withButtonBusy
    const dialog = $('quotaDialog')
    $('quotaCard').hidden = false
    $('quotaOpen').addEventListener('click', () => { dialog.showModal(); void load() })
    $('quotaClose').addEventListener('click', () => { if (!saving) dialog.close() })
    dialog.addEventListener('cancel', e => { if (saving) e.preventDefault() })
    dialog.addEventListener('close', () => { ++request; hideForm(); lastRead.clear(); $('quotaDevice').replaceChildren() })
    $('quotaDevice').addEventListener('change', () => { hideForm(); void load() })
    $('quotaConnect').addEventListener('click', () => {
      const select = $('quotaProvider')
      select.replaceChildren()
      for (const group of new Set(providers.map(p => p.group))) {
        const optgroup = element('optgroup'); optgroup.label = group
        for (const p of providers.filter(p => p.group === group)) optgroup.append(option(p.id,p.name))
        select.append(optgroup)
      }
      $('quotaForm').hidden = false
      $('quotaStatus').textContent = ''
      fields()
      select.focus()
    })
    $('quotaProvider').addEventListener('change', () => { $('quotaKey').value = ''; $('quotaSite').value = ''; $('quotaResource').value=''; $('quotaSession').checked=false; fields() })
    $('quotaSession').addEventListener('change',fields)
    $('quotaCancel').addEventListener('click', () => { hideForm(); $('quotaConnect').focus() })
    $('quotaForm').addEventListener('submit', async event => {
      event.preventDefault()
      if (saving) return
      const source = providers.find(p => p.id === $('quotaProvider').value)
      if (!source) return
      const body = {provider:source.id,alias:$('quotaAlias').value.trim(),site:source.site ? $('quotaSite').value.trim() : '',
        resource:$('quotaResource').required ? $('quotaResource').value.trim() : '',
        key:source.local ? '' : $('quotaKey').value,session_only:!source.local && $('quotaSession').checked}
      saving = true
      const controls = [...$('quotaForm').querySelectorAll('input,select,button'),$('quotaDevice'),$('quotaConnect'),$('quotaClose')]
      controls.forEach(c => c.disabled = true)
      $('quotaStatus').textContent = '正在向所选来源验证并查询额度…'
      try { await api(endpoint + '/accounts', json('POST',body)); hideForm(); await load() }
      catch (error) { $('quotaStatus').textContent = error.message }
      finally { body.key = ''; saving = false; controls.forEach(c => c.disabled = false) }
    })
  }
  window.OmniQuota = {bind,summary}
})()
