/**
 * The format recordings download in: as recorded (WebM from a browser, M4A
 * or MP3 from a shortcut) or MP4. A per-device convenience, so it lives in
 * `localStorage` rather than the server's settings: the choice is about what
 * this device's player or the app it hands files to can open. Set on You →
 * Your data; read by the Recordings tab for one file and for the zip alike.
 */

export type DownloadFormat = 'original' | 'mp4';

const KEY = 'chintan.downloadFormat';

export function downloadFormat(): DownloadFormat {
  try {
    return localStorage.getItem(KEY) === 'mp4' ? 'mp4' : 'original';
  } catch {
    return 'original';
  }
}

export function setDownloadFormat(format: DownloadFormat): void {
  try {
    localStorage.setItem(KEY, format);
  } catch {
    // Private mode or blocked storage: the choice lasts this page only.
  }
}

/** Whether a file named `filename` must be converted to be MP4: M4A and MP4 already are. */
export function needsConversion(filename: string, format: DownloadFormat): boolean {
  return format === 'mp4' && !/\.(m4a|mp4)$/i.test(filename);
}

/** `filename` with its extension swapped for `extension`. */
export function withExtension(filename: string, extension: string): string {
  return `${filename.replace(/\.[^./]+$/, '')}.${extension}`;
}
