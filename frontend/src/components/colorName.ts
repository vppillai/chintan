import type { NoteColor } from '@/api/schema.ts';

/**
 * A colour's name as the picker and the status line say it. A module of its
 * own so Home's row can announce a choice without loading the picker, which
 * arrives only when Colour… is opened.
 */
export function colorName(color: NoteColor): string {
  return color === 'none' ? 'No colour' : color.charAt(0).toUpperCase() + color.slice(1);
}
