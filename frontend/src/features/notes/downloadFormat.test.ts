import { afterEach, describe, expect, it } from 'vitest';

import { downloadFormat, needsConversion, setDownloadFormat, withExtension } from './downloadFormat.ts';

afterEach(() => {
  localStorage.clear();
});

describe('the download format', () => {
  it('is as recorded until MP4 is chosen, and remembers the choice on the device', () => {
    expect(downloadFormat()).toBe('original');
    setDownloadFormat('mp4');
    expect(downloadFormat()).toBe('mp4');
    setDownloadFormat('original');
    expect(downloadFormat()).toBe('original');
  });

  it('converts only what is not already MP4, and renames what it converts', () => {
    expect(needsConversion('roof-repair-20260806-0912.webm', 'mp4')).toBe(true);
    expect(needsConversion('roof-repair-20260806-0912.mp3', 'mp4')).toBe(true);
    expect(needsConversion('from-watch.m4a', 'mp4')).toBe(false);
    expect(needsConversion('roof-repair-20260806-0912.webm', 'original')).toBe(false);
    expect(withExtension('roof-repair-20260806-0912.webm', 'm4a')).toBe('roof-repair-20260806-0912.m4a');
    expect(withExtension('a.b.webm', 'mp4')).toBe('a.b.mp4');
  });
});
