// OmniShare's approved blue link mark. The same geometry produces the README
// SVG, every packaged icon size, and macOS's monochrome template. No network,
// native image tools, or optional image dependencies are required at build time.
import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { deflateSync } from 'node:zlib';

const here = dirname(fileURLToPath(import.meta.url));
const ROOT = join(here, '..');
export const DESIGN = Object.freeze({
  canvas: 1024, inset: 64, tile: 896, radius: 200,
  outerRadius: 148, outerHalfLine: 146,
  innerRadius: 78, innerHalfLine: 140,
  barRadius: 31, barHalfLine: 70,
});
const clamp = (value, lo = 0, hi = 1) => Math.max(lo, Math.min(hi, value));
const mix = (a, b, t) => a.map((value, i) => value * (1 - t) + b[i] * t);
const coverage = (distance, pixelWidth) => clamp(0.5 - distance / pixelWidth);
function tileDistance(x, y) {
  const half = DESIGN.tile / 2;
  const qx = Math.abs(x - 512) - (half - DESIGN.radius);
  const qy = Math.abs(y - 512) - (half - DESIGN.radius);
  return Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) + Math.min(Math.max(qx, qy), 0) - DESIGN.radius;
}
function capsuleDistance(x, y, radius, halfLine) {
  return Math.hypot(x, Math.max(Math.abs(y) - halfLine, 0)) - radius;
}
function markDistance(x, y) {
  // A 45-degree link with an inset capsule. Not a circle crossed by a slash.
  const dx = x - 512, dy = y - 512;
  const u = (dx + dy) / Math.SQRT2, v = (dy - dx) / Math.SQRT2;
  const outer = capsuleDistance(u, v, DESIGN.outerRadius, DESIGN.outerHalfLine);
  const inner = capsuleDistance(u, v, DESIGN.innerRadius, DESIGN.innerHalfLine);
  const bar = capsuleDistance(u, v, DESIGN.barRadius, DESIGN.barHalfLine);
  return Math.min(Math.max(outer, -inner), bar);
}
function background(x, y) {
  const t = clamp((0.58 * (x - 64) + 0.72 * (y - 64)) / 896);
  const base = mix([10, 165, 245], [49, 35, 209], t);
  const glow = 0.10 * Math.exp(-((x - 280) ** 2 + (y - 125) ** 2) / 180000);
  return mix(base, [197, 240, 255], glow);
}
function sample(x, y, pixelWidth, template) {
  if (template) {
    // Enlarge the isolated glyph for a crisp 22pt / 44px menu-bar template.
    const alpha = coverage(markDistance(512 + (x - 512) / 1.48, 512 + (y - 512) / 1.48), pixelWidth / 1.48);
    return [0, 0, 0, Math.round(alpha * 255)];
  }
  const distance = tileDistance(x, y);
  const alpha = coverage(distance, pixelWidth);
  if (!alpha) return [0, 0, 0, 0];
  let color = background(x, y);
  const edge = coverage(Math.abs(distance + 3) - 2, pixelWidth) * 0.28;
  color = mix(color, [220, 241, 255], edge);
  const mark = coverage(markDistance(x, y), pixelWidth);
  color = mix(color, mix([255, 255, 255], [213, 226, 255], clamp((y - 220) / 650)), mark);
  return [...color.map(value => Math.round(clamp(value, 0, 255))), Math.round(alpha * 255)];
}
export function renderRgba(size, { template = false } = {}) {
  if (!Number.isInteger(size) || size < 16 || size > 1024) throw new RangeError('Icon size must be 16–1024 pixels');
  const bytes = Buffer.alloc(size * size * 4);
  const scale = DESIGN.canvas / size;
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) bytes.set(sample((x + 0.5) * scale, (y + 0.5) * scale, scale, template), (y * size + x) * 4);
  }
  return bytes;
}
export function renderSvg() {
  const d = DESIGN;
  const capsule = (r, h, fill) => `<rect x="${-r}" y="${-h-r}" width="${r*2}" height="${(h+r)*2}" rx="${r}" fill="${fill}"/>`;
  return `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024" role="img" aria-label="OmniShare — connected workspace">
  <defs>
    <linearGradient id="blue" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#0aa5f5"/><stop offset="1" stop-color="#3123d1"/></linearGradient>
    <linearGradient id="white" x1="0" y1="0" x2="0" y2="1"><stop stop-color="#fff"/><stop offset="1" stop-color="#d5e2ff"/></linearGradient>
    <mask id="link"><g transform="translate(512 512) rotate(45)">${capsule(d.outerRadius,d.outerHalfLine,'white')}${capsule(d.innerRadius,d.innerHalfLine,'black')}${capsule(d.barRadius,d.barHalfLine,'white')}</g></mask>
  </defs>
  <rect x="64" y="64" width="896" height="896" rx="200" fill="url(#blue)"/>
  <rect x="67" y="67" width="890" height="890" rx="197" fill="none" stroke="#dcf1ff" stroke-opacity=".28" stroke-width="4"/>
  <rect width="1024" height="1024" fill="url(#white)" mask="url(#link)"/>
</svg>\n`;
}
export function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let i = 0; i < 8; i++) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}
function pngChunk(type, data = Buffer.alloc(0)) {
  const name = Buffer.from(type, 'ascii');
  const chunk = Buffer.alloc(12 + data.length);
  chunk.writeUInt32BE(data.length, 0); name.copy(chunk, 4); data.copy(chunk, 8);
  chunk.writeUInt32BE(crc32(Buffer.concat([name, data])), 8 + data.length);
  return chunk;
}
export function buildPng(size, options = {}) {
  const header = Buffer.alloc(13); header.writeUInt32BE(size, 0); header.writeUInt32BE(size, 4);
  header[8] = 8; header[9] = 6;
  const rgba = renderRgba(size, options), stride = 1 + size * 4, raw = Buffer.alloc(stride * size);
  for (let y = 0; y < size; y++) rgba.copy(raw, y * stride + 1, y * size * 4, (y + 1) * size * 4);
  return Buffer.concat([Buffer.from('89504e470d0a1a0a', 'hex'), pngChunk('IHDR', header), pngChunk('IDAT', deflateSync(raw)), pngChunk('IEND')]);
}
export function buildIcns() {
  const layers = [['icp4',16],['icp5',32],['icp6',64],['ic07',128],['ic08',256],['ic09',512],['ic10',1024],['ic11',32],['ic12',64],['ic13',256],['ic14',512]];
  const pngs = new Map(), chunks = layers.map(([name, size]) => {
    if (!pngs.has(size)) pngs.set(size, buildPng(size));
    const png = pngs.get(size), chunk = Buffer.alloc(8 + png.length);
    chunk.write(name, 0, 4, 'ascii'); chunk.writeUInt32BE(chunk.length, 4); png.copy(chunk, 8); return chunk;
  });
  const header = Buffer.alloc(8); header.write('icns'); header.writeUInt32BE(8 + chunks.reduce((n, chunk) => n + chunk.length, 0), 4);
  return Buffer.concat([header, ...chunks]);
}
export function buildIco() {
  // Modern Windows reads PNG-compressed ICO entries, including the 256px shell
  // layer. Keep several native sizes rather than upscaling one 32px bitmap.
  const sizes = [16, 24, 32, 48, 64, 128, 256], pngs = sizes.map(size => buildPng(size));
  const header = Buffer.alloc(6 + sizes.length * 16); header.writeUInt16LE(1, 2); header.writeUInt16LE(sizes.length, 4);
  let offset = header.length;
  sizes.forEach((size, index) => {
    const entry = 6 + index * 16; header[entry] = size % 256; header[entry+1] = size % 256;
    header.writeUInt16LE(1, entry+4); header.writeUInt16LE(32, entry+6);
    header.writeUInt32LE(pngs[index].length, entry+8); header.writeUInt32LE(offset, entry+12); offset += pngs[index].length;
  });
  return Buffer.concat([header, ...pngs]);
}
export function generateIcons(directory) {
  mkdirSync(directory, { recursive: true });
  const files = new Map([
    ['icon.png', buildPng(1024)], ['32x32.png', buildPng(32)], ['128x128.png', buildPng(128)],
    ['128x128@2x.png', buildPng(256)], ['icon.icns', buildIcns()], ['icon.ico', buildIco()],
    ['tray-template.png', buildPng(44, { template: true })], ['tray-template.rgba', renderRgba(44, { template: true })],
    ['icon.svg', Buffer.from(renderSvg())],
  ]);
  for (const [name, bytes] of files) writeFileSync(join(directory, name), bytes);
  return [...files.keys()];
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const directory = process.argv[2] ? resolve(process.argv[2]) : join(ROOT, 'src-tauri/icons');
  for (const name of generateIcons(directory)) console.log(`Generated ${join(directory, name)}`);
}
