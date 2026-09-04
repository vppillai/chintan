import { config } from '@/config/env.ts';

/**
 * What is running, at the very bottom and deliberately quiet.
 *
 * The first thing anyone needs when a bug report comes in, so it is real text
 * inside a `<code>` — selectable and copyable — rather than decoration.
 *
 * CI passes `git describe --tags --always`: `v0.5.0` when the build is exactly
 * a release, `v0.5.0-14-gabcdef` when it is fourteen commits past one. The raw
 * form was shown as-is, and "v0.5.0-14-gabcdef" reads like a version number
 * with something wrong in it. A release shows as its tag alone; anything past
 * a tag shows as `v0.5.0` and then, quieter, `+14 (abcdef)` — the release it
 * is built on, how far past it, and which commit. A string that is not a
 * describe output (a bare SHA, `local build`, the e2e pin) is shown verbatim.
 */

export interface BuildLabel {
  /** The nearest release tag, or the whole string when it is not a describe. */
  release: string;
  /** Commits past the tag, and the commit — absent when exactly on a tag. */
  build: { ahead: number; sha: string; dirty: boolean } | null;
}

/** `<tag>-<n>-g<sha>[-dirty]`, as `git describe` writes it. */
const DESCRIBE = /^(.+?)-(\d+)-g([0-9a-f]{4,40})(-dirty)?$/i;

export function describeVersion(raw: string): BuildLabel {
  const match = DESCRIBE.exec(raw.trim());
  if (!match) return { release: raw, build: null };
  const [, release = raw, ahead = '0', sha = '', dirty] = match;
  return {
    release,
    build: { ahead: Number(ahead), sha, dirty: Boolean(dirty) },
  };
}

export function VersionFootnote({ version = config.version }: { version?: string }) {
  const { release, build } = describeVersion(version);
  return (
    <p className="version-footnote">
      <span className="visually-hidden">App version </span>
      <code>
        {release}
        {build && (
          <span className="version-footnote__build">
            {`+${build.ahead} (${build.sha}${build.dirty ? ', dirty' : ''})`}
          </span>
        )}
      </code>
    </p>
  );
}
