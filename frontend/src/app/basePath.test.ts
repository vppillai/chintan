import { describe, expect, it } from 'vitest';

import { routerBasename, scopedEntryUrl } from './basePath.ts';

describe('the router lives at the deploy base, slash included', () => {
  it('keeps the trailing slash the manifest and the worker are scoped to', () => {
    expect(routerBasename('/chintan/dev/')).toBe('/chintan/dev/');
    expect(routerBasename('/')).toBe('/');
    expect(routerBasename('')).toBe('/');
  });
});

describe('an entry at the scope path without its slash is moved inside the scope', () => {
  const at = (pathname: string, search = '', hash = '') => ({ pathname, search, hash });

  it('adds the slash and keeps the query and hash', () => {
    expect(scopedEntryUrl('/chintan/dev/', at('/chintan/dev', '?view=archived'))).toBe(
      '/chintan/dev/?view=archived',
    );
    expect(scopedEntryUrl('/chintan/dev/', at('/chintan/dev', '?q=roof', '#main'))).toBe(
      '/chintan/dev/?q=roof#main',
    );
  });

  it('leaves every URL already inside the scope alone', () => {
    expect(scopedEntryUrl('/chintan/dev/', at('/chintan/dev/'))).toBeNull();
    expect(scopedEntryUrl('/chintan/dev/', at('/chintan/dev/notes/roof-repair'))).toBeNull();
  });

  it('does nothing for a root deploy, or a sibling path that merely shares a prefix', () => {
    expect(scopedEntryUrl('/', at('/'))).toBeNull();
    expect(scopedEntryUrl('/chintan/dev/', at('/chintan/development'))).toBeNull();
  });
});
