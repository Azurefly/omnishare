import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { deflateSync } from 'node:zlib';

const __dirname = dirname(fileURLToPath(import.meta.url));
const desktopRoot = join(__dirname, '..');
const iconDir = join(desktopRoot, 'src-tauri', 'icons');

function pixel(x, y, size) {
  const center = (size - 1) / 2;
  const dx = x - center;
  const dy = y - center;
  const distance = Math.sqrt(dx * dx + dy * dy);
  const radius = size * 0.46;
  const inside = distance <= radius;
  const ring = Math.abs(distance - radius * 0.72) < size * 0.07;
  const slash = Math.abs((y - x) - size * 0.08) < size * 0.055 && x > size * 0.25 && x < size * 0.76;

  if (!inside) {
    return [18, 24, 38, 255];
  }

  if (ring || slash) {
    return [236, 245, 255, 255];
  }

  const t = Math.max(0, 1 - distance / radius);
  const r = Math.round(70 + x * 90 / size + t * 44);
  const g = Math.round(105 + y * 70 / size + t * 82);
  const b = Math.round(210 + t * 32);
  return [r, g, b, 255];
}

function crc32(buffer) {
  let crc = 0xffffffff;
  for (const byte of buffer) {
    crc ^= byte;
    for (let i = 0; i < 8; i++) {
      crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
    }
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function pngChunk(type, data = Buffer.alloc(0)) {
  const typeBuffer = Buffer.from(type, 'ascii');
  const chunk = Buffer.alloc(8 + data.length + 4);
  chunk.writeUInt32BE(data.length, 0);
  typeBuffer.copy(chunk, 4);
  data.copy(chunk, 8);
  chunk.writeUInt32BE(crc32(Buffer.concat([typeBuffer, data])), 8 + data.length);
  return chunk;
}

function buildPng(size) {
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header.writeUInt8(8, 8); // bit depth
  header.writeUInt8(6, 9); // RGBA
  header.writeUInt8(0, 10); // compression
  header.writeUInt8(0, 11); // filter
  header.writeUInt8(0, 12); // interlace

  const stride = 1 + size * 4;
  const raw = Buffer.alloc(stride * size);
  let offset = 0;
  for (let y = 0; y < size; y++) {
    raw.writeUInt8(0, offset++); // no filter
    for (let x = 0; x < size; x++) {
      const [r, g, b, a] = pixel(x, y, size);
      raw.writeUInt8(r, offset++);
      raw.writeUInt8(g, offset++);
      raw.writeUInt8(b, offset++);
      raw.writeUInt8(a, offset++);
    }
  }

  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    pngChunk('IHDR', header),
    pngChunk('IDAT', deflateSync(raw)),
    pngChunk('IEND')
  ]);
}

function buildIco() {
  const width = 32;
  const height = 32;
  const bytesPerPixel = 4;
  const xorSize = width * height * bytesPerPixel;
  const andStride = Math.ceil(width / 32) * 4;
  const andSize = andStride * height;
  const dibSize = 40 + xorSize + andSize;
  const totalSize = 6 + 16 + dibSize;
  const buffer = Buffer.alloc(totalSize);
  let offset = 0;

  buffer.writeUInt16LE(0, offset); offset += 2;
  buffer.writeUInt16LE(1, offset); offset += 2;
  buffer.writeUInt16LE(1, offset); offset += 2;

  buffer.writeUInt8(width, offset++);
  buffer.writeUInt8(height, offset++);
  buffer.writeUInt8(0, offset++);
  buffer.writeUInt8(0, offset++);
  buffer.writeUInt16LE(1, offset); offset += 2;
  buffer.writeUInt16LE(32, offset); offset += 2;
  buffer.writeUInt32LE(dibSize, offset); offset += 4;
  buffer.writeUInt32LE(22, offset); offset += 4;

  buffer.writeUInt32LE(40, offset); offset += 4;
  buffer.writeInt32LE(width, offset); offset += 4;
  buffer.writeInt32LE(height * 2, offset); offset += 4;
  buffer.writeUInt16LE(1, offset); offset += 2;
  buffer.writeUInt16LE(32, offset); offset += 2;
  buffer.writeUInt32LE(0, offset); offset += 4;
  buffer.writeUInt32LE(xorSize, offset); offset += 4;
  buffer.writeInt32LE(0, offset); offset += 4;
  buffer.writeInt32LE(0, offset); offset += 4;
  buffer.writeUInt32LE(0, offset); offset += 4;
  buffer.writeUInt32LE(0, offset); offset += 4;

  for (let y = height - 1; y >= 0; y--) {
    for (let x = 0; x < width; x++) {
      const [r, g, b, a] = pixel(x, y, width);
      buffer.writeUInt8(b, offset++);
      buffer.writeUInt8(g, offset++);
      buffer.writeUInt8(r, offset++);
      buffer.writeUInt8(a, offset++);
    }
  }

  offset += andSize;
  return buffer;
}

mkdirSync(iconDir, { recursive: true });

const assets = [
  ['icon.png', buildPng(128)],
  ['32x32.png', buildPng(32)],
  ['128x128.png', buildPng(128)],
  ['128x128@2x.png', buildPng(256)],
  ['icon.ico', buildIco()]
];

for (const [name, buffer] of assets) {
  const target = join(iconDir, name);
  writeFileSync(target, buffer);
  console.log(`Generated ${target}`);
}
