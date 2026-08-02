import { cp, mkdir, rm } from 'node:fs/promises'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
const root=resolve(dirname(fileURLToPath(import.meta.url)),'..','..')
const source=resolve(root,'frontend','web')
const target=resolve(root,'backend','internal','frontend','dist')
await rm(target,{recursive:true,force:true});await mkdir(target,{recursive:true});await cp(source,target,{recursive:true})
console.log(`Frontend copied to ${target}`)
