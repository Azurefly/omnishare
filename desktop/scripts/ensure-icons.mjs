import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const desktopRoot = join(__dirname, '..');
const iconDir = join(desktopRoot, 'src-tauri', 'icons');
const iconPath = join(iconDir, 'icon.ico');

function buildIco() {
  const width = 16;
  const height = 16;
  const bytesPerPixel = 4;
  const xorSize = width * height * bytesPerPixel;
  const andStride = Math.ceil(width / 32) * 4;
  const andSize = andStride * height;
  const dibSize = 40 + xorSize + andSize;
  const totalSize = 6 + 16 + dibSize;
  const buffer = Buffer.alloc(totalSize);
  let offset = 0;

  // ICONDIR
  buffer.writeUInt16LE(0, offset); offset += 2; // reserved
  buffer.writeUInt16LE(1, offset); offset += 2; // icon type
  buffer.writeUInt16LE(1, offset); offset += 2; // image count

  // ICONDIRENTRY
  buffer.writeUInt8(width, offset++);
  buffer.writeUInt8(height, offset++);
  buffer.writeUInt8(0, offset++); // no palette
  buffer.writeUInt8(0, offset++); // reserved
  buffer.writeUInt16LE(1, offset); offset += 2; // color planes
  buffer.writeUInt16LE(32, offset); offset += 2; // bit count
  buffer.writeUInt32LE(dibSize, offset); offset += 4;
  buffer.writeUInt32LE(22, offset); offset += 4; // image offset

  // BITMAPINFOHEADER. ICO stores XOR + AND heights together.
  buffer.writeUInt32LE(40, offset); offset += 4;
  buffer.writeInt32LE(width, offset); offset += 4;
  buffer.writeInt32LE(height * 2, offset); offset += 4;
  buffer.writeUInt16LE(1, offset); offset += 2;
  buffer.writeUInt16LE(32, offset); offset += 2;
  buffer.writeUInt32LE(0, offset); offset += 4; // BI_RGB
  buffer.writeUInt32LE(xorSize, offset); offset += 4;
  buffer.writeInt32LE(0, offset); offset += 4;
  buffer.writeInt32LE(0, offset); offset += 4;
  buffer.writeUInt32LE(0, offset); offset += 4;
  buffer.writeUInt32LE(0, offset); offset += 4;

  // BGRA pixels, bottom-up. Use a simple blue/purple OmniShare placeholder.
  for (let y = height - 1; y >= 0; y--) {
    for (let x = 0; x < width; x++) {
      const cx = x - 7.5;
      const cy = y - 7.5;
      const distance = Math.sqrt(cx * cx + cy * cy);
      const inside = distance <= 7.3;
      const b = inside ? 220 : 20;
      const g = inside ? 120 + Math.max(0, 7 - distance) * 8 : 24;
      const r = inside ? 70 + x * 7 : 32;
      const a = inside ? 255 : 255;
      buffer.writeUInt8(Math.min(255, Math.round(b)), offset++);
      buffer.writeUInt8(Math.min(255, Math.round(g)), offset++);
      buffer.writeUInt8(Math.min(255, Math.round(r)), offset++);
      buffer.writeUInt8(a, offset++);
    }
  }

  // AND mask: all visible.
  offset += andSize;
  return buffer;
}

mkdirSync(iconDir, { recursive: true });
writeFileSync(iconPath, buildIco());
console.log(`Generated ${iconPath}`);
