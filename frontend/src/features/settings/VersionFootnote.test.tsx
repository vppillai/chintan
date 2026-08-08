import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { LOCAL_VERSION, config } from '@/config/env.ts';

import { VersionFootnote } from './SettingsScreen.tsx';

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
