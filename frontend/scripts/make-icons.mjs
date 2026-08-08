#!/usr/bin/env bun
/**
 * Generates the PWA icon PNGs.
 *
 * Written rather than committed as binaries so the mark can be changed in one
 * place and the maskable safe zone stays correct by construction: Android
 * crops a maskable icon to an arbitrary shape and only the central 80% circle
 * is guaranteed visible, so the glyph is drawn inside 60% of the canvas with
 * the ground bled to every edge.
 *
 * PNGs are emitted by hand — a minimal encoder is ~60 lines and beats adding an
 * image library to the build for four files.
 */
import { deflateSync } from 'node:zlib';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

const OUT = new URL('../public/', import.meta.url).pathname;

/** Ink & Paper ground and ink, matching tokens.css. */
const GROUND = [0xfb, 0xf9, 0xf4];
const INK = [0x1a, 0x19, 0x17];
const ACCENT = [0xc2, 0x41, 0x0c];

function crc32(buffer) {
  let crc = ~0;
  for (const byte of buffer) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) {
      crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
    }
  }
  return ~crc >>> 0;
}

function chunk(type, data) {
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length);
  const typed = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(typed));
  return Buffer.concat([length, typed, crc]);
}

function png(width, height, rgba) {
  const header = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8;    // bit depth
  ihdr[9] = 6;    // RGBA
  const raw = Buffer.alloc((width * 4 + 1) * height);
  for (let y = 0; y < height; y += 1) {
    raw[y * (width * 4 + 1)] = 0; // no filter
    rgba.copy(raw, y * (width * 4 + 1) + 1, y * width * 4, (y + 1) * width * 4);
  }
  return Buffer.concat([
    header,
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

/** A microphone glyph: rounded capsule, stand, base. */
function draw(size, { maskable }) {
  const pixels = Buffer.alloc(size * size * 4);
  const set = (x, y, [r, g, b]) => {
    if (x < 0 || y < 0 || x >= size || y >= size) return;
    const offset = (y * size + x) * 4;
    pixels[offset] = r;
    pixels[offset + 1] = g;
    pixels[offset + 2] = b;
    pixels[offset + 3] = 255;
  };

  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) set(x, y, GROUND);
  }

  // Maskable icons keep the glyph inside the guaranteed-visible centre.
  const scale = maskable ? 0.6 : 0.78;
  const cx = size / 2;
  const cy = size / 2;
  const glyph = size * scale;

  const capsuleW = glyph * 0.30;
  const capsuleH = glyph * 0.50;
  const capsuleTop = cy - glyph * 0.38;
  const radius = capsuleW / 2;

  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      const dx = x - cx;
      const localY = y - capsuleTop;
      const inBody =
        Math.abs(dx) <= radius && localY >= radius && localY <= capsuleH - radius;
      const inTop = Math.hypot(dx, localY - radius) <= radius;
      const inBottom = Math.hypot(dx, localY - (capsuleH - radius)) <= radius;
      if (inBody || inTop || inBottom) set(x, y, INK);
    }
  }

  // The cradle arc, in the accent — the record signal.
  const arcR = glyph * 0.30;
  const arcThickness = Math.max(2, glyph * 0.045);
  const arcCy = capsuleTop + capsuleH * 0.62;
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      const dx = x - cx;
      const dy = y - arcCy;
      if (dy < 0) continue;
      const distance = Math.hypot(dx, dy);
      if (Math.abs(distance - arcR) <= arcThickness / 2) set(x, y, ACCENT);
    }
  }

  // Stand and base.
  const standTop = arcCy + arcR;
  const standW = Math.max(2, glyph * 0.05);
  for (let y = standTop; y < standTop + glyph * 0.14; y += 1) {
    for (let x = cx - standW / 2; x <= cx + standW / 2; x += 1) {
      set(Math.round(x), Math.round(y), INK);
    }
  }
  const baseW = glyph * 0.34;
  const baseH = Math.max(2, glyph * 0.055);
  for (let y = standTop + glyph * 0.14; y < standTop + glyph * 0.14 + baseH; y += 1) {
    for (let x = cx - baseW / 2; x <= cx + baseW / 2; x += 1) {
      set(Math.round(x), Math.round(y), INK);
    }
  }

  return png(size, size, pixels);
}

const targets = [
  ['icon-192.png', 192, { maskable: false }],
  ['icon-512.png', 512, { maskable: false }],
  ['icon-maskable-192.png', 192, { maskable: true }],
  ['icon-maskable-512.png', 512, { maskable: true }],
  ['apple-touch-icon.png', 180, { maskable: false }],
];

for (const [name, size, options] of targets) {
  writeFileSync(join(OUT, name), draw(size, options));
  console.log(`wrote public/${name} (${size}x${size})`);
}
