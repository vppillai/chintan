/**
 * A `Blob`'s bytes under jsdom, whose `Blob` implements neither
 * `arrayBuffer()` nor `text()`. `FileReader` it does implement.
 */
export function bytesOf(blob: Blob): Promise<Uint8Array> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      resolve(new Uint8Array(reader.result as ArrayBuffer));
    };
    reader.onerror = () => {
      reject(reader.error ?? new Error('read failed'));
    };
    reader.readAsArrayBuffer(blob);
  });
}
