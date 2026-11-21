import test from 'node:test'
import assert from 'node:assert/strict'
import {readFile} from 'node:fs/promises'
const read = path => readFile(new URL('../src-tauri/' + path, import.meta.url), 'utf8')
const capability = async name => JSON.parse(await read('capabilities/' + name + '.json'))

test('the loopback workbench may only open the native update window or quit via Settings', async () => {
  const entry = await capability('loopback-update-entry')
  assert.equal(entry.local,false)
  assert.deepEqual(entry.windows,['main'])
  assert.deepEqual(entry.remote.urls,['http://127.0.0.1:*'])
  assert.deepEqual(entry.permissions,['open-update-window','quit-desktop-app'])
})
test('download and install permissions stay in the bundled updater', async () => {
  const entry = await capability('native-updates')
  assert.equal(entry.local,true)
  assert.equal(entry.remote,undefined)
  assert.deepEqual(entry.windows,['updates'])
  assert.deepEqual(entry.permissions,['native-update-operations'])
  const permissions = await read('permissions/desktop.toml')
  const opening = permissions.split('identifier = "open-update-window"')[1].split('[[permission]]')[0]
  assert.match(opening,/commands.allow = \["open_updates"\]/)
  assert.doesNotMatch(opening,/"(?:install_update|download_update|backend_stop)"/)
})
test('all app commands participate in the ACL and first-run startup remains local', async () => {
  const build=await read('build.rs')
  assert.match(build,/AppManifest::new\(\)\.commands/)
  for(const name of ['health','quit_desktop','desktop_settings','desktop_boot','backend_status','backend_start','backend_stop',
    'open_updates','updater_info','check_update','download_update','install_update']) assert.ok(build.includes('"'+name+'"'))
  const entry=await capability('desktop-bootstrap')
  assert.equal(entry.local,true);assert.equal(entry.remote,undefined)
  assert.deepEqual(entry.windows,['main'])
  assert.deepEqual(entry.permissions,['desktop-bootstrap','open-update-window'])
})
