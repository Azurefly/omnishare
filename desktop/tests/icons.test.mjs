import assert from 'node:assert/strict';
import { readFileSync, mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { inflateSync } from 'node:zlib';
import test from 'node:test';
import { buildPng, buildIcns, buildIco, renderRgba, renderSvg, crc32, generateIcons } from '../scripts/ensure-icons.mjs';
const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
function inspectPng(png) {
  assert.equal(png.subarray(0,8).toString('hex'), '89504e470d0a1a0a');
  let offset = 8, size, height, ended = false; const data = [];
  while (offset < png.length) {
    const length = png.readUInt32BE(offset), type = png.toString('ascii', offset+4, offset+8);
    assert.ok(offset+length+12 <= png.length);
    assert.equal(png.readUInt32BE(offset+length+8), crc32(png.subarray(offset+4,offset+length+8)));
    const bytes = png.subarray(offset+8,offset+8+length);
    if (type === 'IHDR') { size = bytes.readUInt32BE(0); height = bytes.readUInt32BE(4); assert.equal(bytes[8], 8); assert.equal(bytes[9], 6); }
    if (type === 'IDAT') data.push(bytes);
    if (type === 'IEND') ended = true;
    offset += length+12;
  }
  assert.ok(ended); assert.equal(offset, png.length); assert.equal(size, height);
  const raw = inflateSync(Buffer.concat(data)); assert.equal(raw.length, size * (size * 4+1));
  return { size, raw };
}
for (const size of [16,32,44,128,256,512,1024]) {
  test(`${size}px PNG is valid RGBA with transparent corners and antialiased edges`, () => {
    const {raw} = inspectPng(buildPng(size)); assert.equal(raw[4], 0);
    const rgba = renderRgba(size); const alphas = [];
    for (let i=3;i<rgba.length;i+=4) alphas.push(rgba[i]);
    assert.ok(alphas.includes(255)); assert.ok(alphas.some(a => a > 0 && a < 255));
    assert.equal(rgba[(Math.floor(size/2)*size+Math.floor(size/2))*4+3],255);
  });
}
test('macOS icon includes the complete 16–1024px/Retina layer set', () => {
  const icns = buildIcns(); assert.equal(icns.toString('ascii',0,4),'icns'); assert.equal(icns.readUInt32BE(4),icns.length);
  const seen = {}; let offset = 8;
  while (offset < icns.length) {
    const name = icns.toString('ascii',offset,offset+4), length = icns.readUInt32BE(offset+4);
    assert.ok(length > 8); assert.ok(!seen[name]); seen[name] = inspectPng(icns.subarray(offset+8,offset+length)).size; offset += length;
  }
  assert.equal(offset,icns.length);
  assert.deepEqual(seen,{icp4:16,icp5:32,icp6:64,ic07:128,ic08:256,ic09:512,ic10:1024,ic11:32,ic12:64,ic13:256,ic14:512});
});
test('Windows ICO contains bounded native layers, including 256px', () => {
  const ico = buildIco(); assert.equal(ico.readUInt16LE(0),0); assert.equal(ico.readUInt16LE(2),1); assert.equal(ico.readUInt16LE(4),7);
  let end = 6+7*16;
  [16,24,32,48,64,128,256].forEach((size,i) => {
    const e=6+i*16, length=ico.readUInt32LE(e+8), offset=ico.readUInt32LE(e+12);
    assert.equal(offset,end); assert.equal(ico[e]||256,size); assert.equal(inspectPng(ico.subarray(offset,offset+length)).size,size); end+=length;
  });
  assert.equal(end,ico.length);
});
test('menu-bar template is black with alpha only and has no opaque square', () => {
  const rgba=renderRgba(44,{template:true}); assert.equal(rgba.length,44*44*4);
  let visible=0;
  for(let i=0;i<rgba.length;i+=4){ assert.deepEqual([...rgba.subarray(i,i+3)],[0,0,0]); if(rgba[i+3]) visible++; }
  assert.ok(visible>150 && visible<44*44/2); assert.equal(rgba[3],0);
});
test('published SVG uses the same approved link geometry and transparent canvas', () => {
  const svg=renderSvg(); assert.match(svg, /rotate\(45\)/); assert.match(svg, /mask id="link"/); assert.doesNotMatch(svg, /<circle|<image|<script/);
  assert.equal(readFileSync(join(root,'docs/assets/omnishare-icon.svg'),'utf8'),svg);
});
test('all build outputs are reproducible, overwriting stale assets', () => {
  const directory=mkdtempSync(join(tmpdir(),'omnishare-icons-'));
  try { const names=generateIcons(directory), first=names.map(name=>readFileSync(join(directory,name)));
    assert.ok(names.includes('icon.icns')&&names.includes('tray-template.rgba')); generateIcons(directory);
    names.forEach((name,i)=>assert.deepEqual(readFileSync(join(directory,name)),first[i]));
  } finally {rmSync(directory,{recursive:true,force:true});}
});
test('invalid icon dimensions fail instead of producing corrupt packages', () => {
  for(const size of [0,-1,15,1025,32.5,NaN]) assert.throws(()=>renderRgba(size),RangeError);
});
