import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { LOCAL_VERSION, config } from '@/config/env.ts';

import { VersionFootnote, describeVersion } from './VersionFootnote.tsx';

/**
 * The owner wanted the running version at the foot of the You screen, quiet.
 *
 * It is the first thing anyone asks for when a bug report arrives, so the bar
 * is that it says something true and can be copied — not that it is present.
 */
describe('the version footnote', () => {
  it('renders the build it is running', () => {
    render(<VersionFootnote />);
    expect(screen.getByText(config.version)).toBeInTheDocument();
  });

  it('says something honest when no version was injected', () => {
    // Every local build, and any CI job that forgot to export it. An empty line
    // or the word "undefined" at the bottom of the screen is worse than saying
    // plainly that this is not a released build.
    expect(config.version).toBe(LOCAL_VERSION);
    render(<VersionFootnote />);

    expect(screen.getByText(LOCAL_VERSION)).toBeInTheDocument();
    expect(screen.queryByText(/undefined/)).toBeNull();
  });

  it('is announced as a version rather than as a bare string', () => {
    render(<VersionFootnote />);
    expect(screen.getByText(/app version/i)).toBeInTheDocument();
  });
});

/**
 * CI passes `git describe --tags --always`. Shown raw, "v0.5.0-14-gabcdef"
 * reads like a version number with something wrong in it.
 */
describe('a git describe string is shown as a release plus a build', () => {
  it('shows a build exactly on a tag as the tag alone', () => {
    render(<VersionFootnote version="v0.5.0" />);
    const code = screen.getByText('v0.5.0');
    expect(code.tagName).toBe('CODE');
    expect(code.querySelector('.version-footnote__build')).toBeNull();
  });

  it('shows a build past a tag as the tag, then how far past and which commit, quieter', () => {
    render(<VersionFootnote version="v0.5.0-14-gabcdef" />);
    const code = screen.getByText(/v0\.5\.0/, { selector: 'code' });
    expect(code).toHaveTextContent('v0.5.0+14 (abcdef)');
    // The build detail is the muted half; the release is what the eye lands on.
    expect(code.querySelector('.version-footnote__build')).toHaveTextContent('+14 (abcdef)');
    expect(screen.queryByText(/gabcdef/)).toBeNull();
  });

  it('shows anything that is not a describe verbatim', () => {
    // A bare SHA (`--always` in a repo with no tags), the e2e pin, "unknown".
    for (const raw of ['abc1234', 'e2e-abc1234', 'unknown', LOCAL_VERSION]) {
      expect(describeVersion(raw)).toEqual({ release: raw, build: null });
    }
  });

  it('parses the shapes git describe produces', () => {
    expect(describeVersion('v0.5.0')).toEqual({ release: 'v0.5.0', build: null });
    expect(describeVersion('v0.5.0-14-gabcdef')).toEqual({
      release: 'v0.5.0',
      build: { ahead: 14, sha: 'abcdef', dirty: false },
    });
    // A pre-release tag with hyphens of its own keeps them.
    expect(describeVersion('v1.0.0-rc.1-3-g0123abc').release).toBe('v1.0.0-rc.1');
    expect(describeVersion('v0.5.0-2-gabc1234-dirty').build).toEqual({
      ahead: 2,
      sha: 'abc1234',
      dirty: true,
    });
  });
});
