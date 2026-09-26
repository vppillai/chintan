import { config } from '@/config/env.ts';

/**
 * The brand lockup: the app's name in the notes' serif, in ink, at the head
 * of every screen — the shell's banner elsewhere, the library's own heading
 * row on Home, where the banner is empty (round-3 T17). One component so the
 * two places cannot drift, and so the mark, when the logo decision lands
 * (R5-BR-L1), arrives in one place: it goes before the name inside this span.
 */
export function Wordmark() {
  return <span className="wordmark">{config.appName}</span>;
}
