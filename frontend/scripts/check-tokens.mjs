#!/usr/bin/env bun
/**
 * Design-token lint (spec §5.6, master plan Global Constraints).
 *
 * "No literal colour or font size outside the token definitions, enforced by a
 * lint rule." This is that rule. Only `src/styles/tokens.css` may name a hue or
 * a type size; everything else must go through a custom property.
 *
 * Written by hand rather than pulled in as a stylelint dependency: the whole
 * rule is a hundred lines and adding a linter plus its plugin tree to enforce
 * three patterns is a worse trade than owning them.
 */

import { readdir, readFile } from 'node:fs/promises';
import { join, relative } from 'node:path';
import process from 'node:process';

const ROOT = new URL('..', import.meta.url).pathname;

/** The single file allowed to hold literals. */
const TOKEN_FILE = 'src/styles/tokens.css';

/** #abc, #aabbcc, #aabbccdd */
const HEX = /#(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})\b/i;
/** rgb()/hsl()/oklch()/… with literal channel values */
const FUNCTIONAL_COLOR = /\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch)\s*\(/i;
/** Named CSS colours that are easy to slip in. */
const NAMED_COLOR =
  /:\s*(?:white|black|red|green|blue|grey|gray|silver|orange|yellow|purple|navy|teal|maroon|olive|lime|aqua|fuchsia)\b/i;
/** A colour-ish property whose value is not a var(). */
const COLOR_PROPERTY =
  /(?:^|[\s;{])(?:color|background(?:-color)?|border(?:-[a-z]+)?-color|outline-color|fill|stroke|box-shadow|text-shadow)\s*:\s*([^;]+)/i;
/** A font-size declaration. */
const FONT_SIZE = /(?:^|[\s;{])font-size\s*:\s*([^;]+)/i;

/** Values that are not literals and are always acceptable. */
const NEUTRAL_VALUES = new Set([
  'inherit',
  'initial',
  'unset',
  'revert',
  'currentcolor',
  'transparent',
  'none',
  'auto',
]);

async function* walk(dir) {
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules' || entry.name === 'dist') continue;
    const full = join(dir, entry.name);
    if (entry.isDirectory()) yield* walk(full);
    else yield full;
  }
}

function usesToken(value) {
  const trimmed = value.trim().toLowerCase();
  if (trimmed.includes('var(--')) return true;
  return trimmed.split(/\s+/).every((part) => NEUTRAL_VALUES.has(part));
}

/** Strip /* … *\/ comments so prose about "a white screen" is not a violation. */
function stripComments(source) {
  return source.replace(/\/\*[\s\S]*?\*\//g, (match) => match.replace(/[^\n]/g, ' '));
}

const violations = [];

function report(file, index, label, detail) {
  violations.push(`${file}:${index + 1}  ${label}: ${detail.trim()}`);
}

for await (const path of walk(join(ROOT, 'src'))) {
  const file = relative(ROOT, path);
  if (file === TOKEN_FILE) continue;

  const isCss = file.endsWith('.css');
  const isSource = /\.tsx?$/.test(file);
  if (!isCss && !isSource) continue;

  const lines = stripComments(await readFile(path, 'utf8')).split('\n');

  lines.forEach((line, index) => {
    // SVG icon geometry carries `stroke="currentColor"` and `fill="none"`,
    // neither of which is a literal colour.
    if (/stroke=|fill="none"/.test(line)) return;

    if (HEX.test(line)) {
      report(file, index, 'literal colour', HEX.exec(line)[0]);
      return;
    }

    if (!isCss) return;

    if (FUNCTIONAL_COLOR.test(line)) {
      report(file, index, 'functional colour', FUNCTIONAL_COLOR.exec(line)[0]);
      return;
    }
    if (NAMED_COLOR.test(line)) {
      report(file, index, 'named colour', NAMED_COLOR.exec(line)[0]);
      return;
    }

    const colorMatch = COLOR_PROPERTY.exec(line);
    if (colorMatch && !usesToken(colorMatch[1])) {
      report(file, index, 'untokenised colour', colorMatch[0]);
      return;
    }

    const sizeMatch = FONT_SIZE.exec(line);
    if (sizeMatch && !usesToken(sizeMatch[1])) {
      report(file, index, 'untokenised font-size', sizeMatch[0]);
    }
  });
}

if (violations.length > 0) {
  console.error('Design token violations — use a custom property from tokens.css:\n');
  for (const violation of violations) console.error(`  ${violation}`);
  console.error(`\n${violations.length} violation(s).`);
  process.exit(1);
}

console.log(
  `check-tokens: no literal colours or font sizes outside ${TOKEN_FILE}`,
);
