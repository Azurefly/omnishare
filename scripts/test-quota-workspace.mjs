import assert from 'node:assert/strict'
import {once} from 'node:events'
import {createServer} from 'node:http'
import {join} from 'node:path'

export async function checkQuotaWorkspace(page, a, b, api, output) {
  let balance = 13.75
  let requests = 0
  const fixture = createServer((req, res) => {
    if (req.url !== '/user/balance' || req.method !== 'GET' || req.headers.authorization !== 'Bearer quota-test-fixture') {
      res.writeHead(403).end()
      return
    }
    requests++
    res.writeHead(200, {'Content-Type':'application/json'})
    res.end(JSON.stringify({balance, unit:'CNY'}))
  }).listen(0,'127.0.0.1')
  await once(fixture,'listening')
  try {
    await page.locator('.tab[data-tab="dashboard"]').click()
    await page.locator('#quotaOpen').click()
    await page.getByText('尚未连接额度账号。',{exact:true}).waitFor()
    await page.locator('#quotaConnect').click()
    assert.equal(await page.locator('#quotaProvider option').count(),20)
    await page.locator('#quotaProvider').selectOption('gemini')
    assert.equal(await page.locator('#quotaResourceLabel').innerText(),'Google Cloud 项目 ID')
    assert.ok(await page.locator('#quotaResource').evaluate(input => input.required))
    assert.ok(await page.locator('#quotaSession').isChecked())
    assert.ok(await page.locator('#quotaSessionField').isHidden())
    await page.locator('#quotaProvider').selectOption('xai')
    assert.equal(await page.locator('#quotaResourceLabel').innerText(),'xAI 团队 UUID')
    assert.ok(await page.locator('#quotaKey').isVisible())
    await page.locator('#quotaProvider').selectOption('codex')
    assert.ok(await page.locator('#quotaKeyField').isHidden())
    assert.ok(await page.locator('#quotaResourceField').isHidden())
    await page.locator('#quotaProvider').selectOption('balance')
    await page.locator('#quotaSite').fill('http://127.0.0.1:' + fixture.address().port)
    await page.locator('#quotaAlias').fill('额度演示')
    await page.locator('#quotaKey').fill('quota-test-fixture')
    await page.locator('#quotaSession').check()
    await page.locator('#quotaSubmit').click()
    const card = page.locator('.quota-account').filter({hasText:'额度演示'})
    await card.getByText('13.75 CNY',{exact:true}).waitFor()
    assert.equal(await page.locator('#quotaKey').inputValue(),'')
    const device = (await api(a,'/api/v1/peers/identity')).data.id
    const peerPath = '/api/v1/ai-quota?device=' + encodeURIComponent(device)
    assert.deepEqual((await api(b,peerPath)).data.accounts,[])
    await card.getByRole('button',{name:'共享给绑定设备'}).click()
    await page.locator('#confirmSubmitBtn').click()
    await card.getByRole('button',{name:'停止共享'}).waitFor()
    const shared = (await api(b,peerPath)).data.accounts
    assert.equal(shared.length,1)
    assert.equal(shared[0].alias,'额度演示')
    assert.equal(shared[0].reading.meters[0].value,13.75)
    assert.ok(!JSON.stringify(shared).includes('quota-test-fixture'))
    const observer = await page.context().newPage()
    try {
      await observer.goto(b)
      await observer.locator('.tab[data-tab="dashboard"]').click()
      await observer.locator('#quotaOpen').click()
      await observer.locator('#quotaDevice').selectOption(device)
      await observer.locator('.quota-account').getByText('13.75 CNY',{exact:true}).waitFor()
      assert.ok(await observer.locator('#quotaConnect').isHidden())
      assert.equal(await observer.locator('.quota-account button').count(),0)
    } finally {
      await observer.close()
    }
    assert.equal(requests,1)
    balance = 0
    await card.getByRole('button',{name:'刷新额度'}).click()
    await card.getByText('0 CNY',{exact:true}).waitFor()
    await page.screenshot({path:join(output,'ai-quota.png')})
    await card.getByRole('button',{name:'停止共享'}).click()
    await card.getByRole('button',{name:'共享给绑定设备'}).waitFor()
    assert.deepEqual((await api(b,peerPath)).data.accounts,[])
    await card.getByRole('button',{name:'移除',exact:true}).click()
    await page.locator('#confirmSubmitBtn').click()
    await page.getByText('尚未连接额度账号。',{exact:true}).waitFor()
    await page.locator('#quotaClose').click()
  } finally {
    fixture.closeAllConnections()
    await new Promise(resolve => fixture.close(resolve))
  }
}
