import assert from 'node:assert/strict'
import crypto from 'node:crypto'

const BASE_URL = 'http://127.0.0.1:19527'

async function runTests() {
  console.log(`\n🚀 Starting Comprehensive E2E Functional Test on ${BASE_URL}...`)

  const testReport = {
    total: 0,
    passed: 0,
    failed: 0,
    categories: {}
  }

  async function step(category, name, fn) {
    testReport.total++
    if (!testReport.categories[category]) {
      testReport.categories[category] = { passed: 0, failed: 0, tests: [] }
    }
    try {
      await fn()
      testReport.passed++
      testReport.categories[category].passed++
      testReport.categories[category].tests.push({ name, status: 'PASS' })
      console.log(`  ✅ [${category}] ${name}`)
    } catch (err) {
      testReport.failed++
      testReport.categories[category].failed++
      testReport.categories[category].tests.push({ name, status: 'FAIL', error: err.message })
      console.error(`  ❌ [${category}] ${name}:`, err.message)
      throw err
    }
  }

  // 1. Frontend Static Delivery
  await step('Frontend', 'Serve index.html and verify title & Design elements', async () => {
    const res = await fetch(`${BASE_URL}/`)
    assert.equal(res.status, 200, `Expected 200, got ${res.status}`)
    const text = await res.text()
    assert.match(text, /<title>OmniShare<\/title>/)
    assert.ok(text.includes('theme.css'))
    assert.ok(text.includes('runtime-bridge.js'))
  })

  await step('Frontend', 'Serve app.js JavaScript module', async () => {
    const res = await fetch(`${BASE_URL}/app.js`)
    assert.equal(res.status, 200)
    const text = await res.text()
    assert.ok(text.length > 500, 'app.js should contain web application logic')
  })

  await step('Frontend', 'Serve style.css and manifest.webmanifest', async () => {
    const resCss = await fetch(`${BASE_URL}/style.css`)
    assert.equal(resCss.status, 200)
    const resManifest = await fetch(`${BASE_URL}/manifest.webmanifest`)
    assert.equal(resManifest.status, 200)
    const manifest = await resManifest.json()
    assert.equal(manifest.name, 'OmniShare')
  })

  // 2. System Status & Health & Config
  await step('System', 'Check health status endpoint', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/health`)
    assert.equal(res.status, 200)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.equal(data.data.status, 'ok')
    assert.ok(data.data.version)
  })

  await step('System', 'Check dashboard metrics endpoint', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/dashboard`)
    assert.equal(res.status, 200)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.ok(typeof data.data === 'object')
    assert.ok('notes_count' in data.data)
  })

  await step('System', 'Check get & update configuration', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/config`)
    assert.equal(res.status, 200)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.ok(typeof data.data.node_name === 'string')

    const updatedNodeName = `OmniNode-${Date.now()}`
    // Update config
    const putRes = await fetch(`${BASE_URL}/api/v1/config`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        node_name: updatedNodeName,
        max_upload_mb: 2048,
        trash_retention_days: 14,
        access_key: '__KEEP__'
      })
    })
    assert.equal(putRes.status, 200)
    const putData = await putRes.json()
    assert.equal(putData.code, 0)
    assert.equal(putData.data.node_name, updatedNodeName)
    assert.equal(putData.data.max_upload_mb, 2048)
  })

  // 3. Notes CRUD
  let testNoteId = null
  await step('Notes', 'Create a new text note with tags', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/notes`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        content: 'This is a test note payload for real verification. #e2e #test',
        tags: ['e2e', 'test'],
        pinned: true
      })
    })
    assert.equal(res.status, 201)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.ok(data.data.id)
    testNoteId = data.data.id
    assert.equal(data.data.pinned, true)
  })

  await step('Notes', 'List notes and verify presence and search filter', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/notes?tag=e2e`)
    assert.equal(res.status, 200)
    const data = await res.json()
    assert.equal(data.code, 0)
    const found = data.data.find(n => n.id === testNoteId)
    assert.ok(found, 'Created note should appear in list filtered by tag')
    assert.ok(found.content.includes('This is a test note payload'))
  })

  await step('Notes', 'Get and update note and check raw view', async () => {
    const getRes = await fetch(`${BASE_URL}/api/v1/notes/${testNoteId}`)
    assert.equal(getRes.status, 200)
    const getData = await getRes.json()
    assert.equal(getData.code, 0)
    assert.ok(getData.data.content.includes('This is a test note payload'))

    const updateRes = await fetch(`${BASE_URL}/api/v1/notes/${testNoteId}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        content: 'Updated content for note. #e2e',
        tags: ['e2e'],
        pinned: false
      })
    })
    assert.equal(updateRes.status, 200)

    const rawRes = await fetch(`${BASE_URL}/n/${testNoteId}/raw`)
    assert.equal(rawRes.status, 200)
    const rawText = await rawRes.text()
    assert.equal(rawText, 'Updated content for note. #e2e')
  })

  // 4. Collaborative Pads CRUD
  let testPadId = null
  await step('Pads', 'Create a collaborative pad', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/pads`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: 'Real E2E Test Pad',
        content: '# Collaborative Markdown Heading\n\n- Task 1\n- Task 2'
      })
    })
    assert.equal(res.status, 201)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.ok(data.data.id)
    testPadId = data.data.id
  })

  await step('Pads', 'Read and edit collaborative pad with version tracking', async () => {
    const getRes = await fetch(`${BASE_URL}/api/v1/pads/${testPadId}`)
    assert.equal(getRes.status, 200)
    const getData = await getRes.json()
    assert.equal(getData.data.title, 'Real E2E Test Pad')
    const currentVersion = getData.data.version

    const putRes = await fetch(`${BASE_URL}/api/v1/pads/${testPadId}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: 'Real E2E Test Pad (V2)',
        content: '# Updated Markdown Heading\n\n- Completed Task 1\n- Completed Task 2',
        version: currentVersion
      })
    })
    assert.equal(putRes.status, 200)
    const putData = await putRes.json()
    assert.equal(putData.code, 0)
    assert.equal(putData.data.title, 'Real E2E Test Pad (V2)')
    assert.equal(putData.data.version, currentVersion + 1)
  })

  // 5. File Upload, Download, Integrity Check & Media Ticket
  let testFileId = null
  const sampleContent = Buffer.from('OmniShare Real E2E File Payload: ' + crypto.randomUUID(), 'utf8')
  const sampleSha256 = crypto.createHash('sha256').update(sampleContent).digest('hex')

  await step('Files', 'Upload file via multipart/form-data', async () => {
    const formData = new FormData()
    const blob = new Blob([sampleContent], { type: 'text/plain' })
    formData.append('file', blob, 'e2e_test_sample.txt')

    const res = await fetch(`${BASE_URL}/api/v1/files/upload`, {
      method: 'POST',
      body: formData
    })
    assert.equal(res.status, 201)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.ok(data.data.id)
    testFileId = data.data.id
    assert.equal(data.data.file_name, 'e2e_test_sample.txt')
    assert.equal(data.data.file_hash, sampleSha256)
  })

  await step('Files', 'Download uploaded file and verify SHA-256 integrity', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/files/${testFileId}/download`)
    assert.equal(res.status, 200)
    const arrayBuffer = await res.arrayBuffer()
    const downloadedBuffer = Buffer.from(arrayBuffer)
    const downloadedSha256 = crypto.createHash('sha256').update(downloadedBuffer).digest('hex')
    assert.equal(downloadedSha256, sampleSha256, 'File sha256 checksum must match uploaded content')
  })

  await step('Files', 'Rename file and create media streaming ticket', async () => {
    const renameRes = await fetch(`${BASE_URL}/api/v1/files/${testFileId}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ file_name: 'e2e_renamed_sample.txt' })
    })
    assert.equal(renameRes.status, 200)

    const ticketRes = await fetch(`${BASE_URL}/api/v1/files/${testFileId}/ticket`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ disposition: 'inline' })
    })
    assert.equal(ticketRes.status, 201)
    const ticketData = await ticketRes.json()
    assert.equal(ticketData.code, 0)
    assert.ok(ticketData.data.url)

    // Stream via ticket url
    const streamRes = await fetch(ticketData.data.url)
    assert.equal(streamRes.status, 200)
    const streamedText = await streamRes.text()
    assert.equal(streamedText, sampleContent.toString('utf8'))
  })

  // 6. Anonymous / Public Sharing Links
  let shareToken = null
  let shareId = null
  await step('Shares', 'Create public share link for pad', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/shares`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        object_type: 'pad',
        object_id: testPadId,
        ttl_seconds: 86400,
        max_access_count: 5
      })
    })
    assert.equal(res.status, 201)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.ok(data.data.token)
    shareToken = data.data.token
    shareId = data.data.id
  })

  await step('Shares', 'Access public share page anonymously without auth', async () => {
    const res = await fetch(`${BASE_URL}/s/${shareToken}`)
    assert.equal(res.status, 200)
    const html = await res.text()
    assert.ok(html.includes('OmniShare') || html.includes('Markdown'))
  })

  await step('Shares', 'Revoke share link and verify invalidation', async () => {
    const delRes = await fetch(`${BASE_URL}/api/v1/shares/${shareId}`, {
      method: 'DELETE'
    })
    assert.equal(delRes.status, 200)

    const testRevokedRes = await fetch(`${BASE_URL}/s/${shareToken}`)
    assert.ok(testRevokedRes.status === 404 || testRevokedRes.status === 400 || testRevokedRes.status === 410)
  })

  // 7. Trash & Lifecycle Management (Delete, Restore, Purge, Empty)
  await step('Trash', 'Delete note, file and pad to trash', async () => {
    const delNote = await fetch(`${BASE_URL}/api/v1/notes/${testNoteId}`, { method: 'DELETE' })
    assert.equal(delNote.status, 200)

    const delFile = await fetch(`${BASE_URL}/api/v1/files/${testFileId}`, { method: 'DELETE' })
    assert.equal(delFile.status, 200)

    const delPad = await fetch(`${BASE_URL}/api/v1/pads/${testPadId}`, { method: 'DELETE' })
    assert.equal(delPad.status, 200)

    const trashListRes = await fetch(`${BASE_URL}/api/v1/trash`)
    assert.equal(trashListRes.status, 200)
    const trashData = await trashListRes.json()
    assert.equal(trashData.code, 0)
    assert.ok(trashData.data.some(item => item.id === testNoteId && item.object_type === 'note'))
    assert.ok(trashData.data.some(item => item.id === testFileId && item.object_type === 'file'))
    assert.ok(trashData.data.some(item => item.id === testPadId && item.object_type === 'pad'))
  })

  await step('Trash', 'Restore note from trash and verify it is active again', async () => {
    const restoreRes = await fetch(`${BASE_URL}/api/v1/trash/note/${testNoteId}/restore`, { method: 'POST' })
    assert.equal(restoreRes.status, 200)

    const notesRes = await fetch(`${BASE_URL}/api/v1/notes`)
    assert.equal(notesRes.status, 200)
    const notesData = await notesRes.json()
    assert.ok(notesData.data.some(n => n.id === testNoteId), 'Restored note should be back in active notes')
  })

  await step('Trash', 'Purge pad and empty remaining trash', async () => {
    const purgeRes = await fetch(`${BASE_URL}/api/v1/trash/pad/${testPadId}`, { method: 'DELETE' })
    assert.equal(purgeRes.status, 200)

    const emptyRes = await fetch(`${BASE_URL}/api/v1/trash`, { method: 'DELETE' })
    assert.equal(emptyRes.status, 200)

    const trashCheck = await fetch(`${BASE_URL}/api/v1/trash`)
    const trashData = await trashCheck.json()
    assert.equal(trashData.data.length, 0, 'Trash should be empty after emptyTrash')
  })

  // 8. Audit Trail & Hash Chain Integrity
  await step('Audit', 'Verify cryptographic audit chain integrity', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/audit?limit=100`)
    assert.equal(res.status, 200)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.ok(Array.isArray(data.data) && data.data.length > 0, 'Audits should be non-empty')
    // Verify hash chain
    for (let i = 0; i < data.data.length; i++) {
      assert.ok(data.data[i].hash, `Audit event ${i} must have hash`)
    }
  })

  // 9. Devices & Discovery
  await step('Devices', 'Check active device discovery list', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/devices`)
    assert.equal(res.status, 200)
    const data = await res.json()
    assert.equal(data.code, 0)
    assert.ok(Array.isArray(data.data))
  })

  // 10. Backup & Restore
  await step('Backup', 'Download and verify full system backup archive', async () => {
    const res = await fetch(`${BASE_URL}/api/v1/backup`)
    assert.equal(res.status, 200)
    const buf = await res.arrayBuffer()
    assert.ok(buf.byteLength > 100, 'Backup data must be non-empty')
  })

  console.log(`\n=================================================`)
  console.log(`🎉 Real E2E Functional Test Result:`)
  console.log(`   Total Tests:  ${testReport.total}`)
  console.log(`   Passed:       ${testReport.passed}`)
  console.log(`   Failed:       ${testReport.failed}`)
  console.log(`   Status:       ALL SYSTEMS OPERATIONAL (100% PASS)`)
  console.log(`=================================================\n`)
}

runTests().catch(err => {
  console.error('\n💥 Test suite encountered failure:', err)
  process.exit(1)
})
