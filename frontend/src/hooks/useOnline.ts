import { useSyncExternalStore } from 'react';

function subscribe(onChange: () => void): () => void {
  window.addEventListener('online', onChange);
  window.addEventListener('offline', onChange);
  return () => {
    window.removeEventListener('online', onChange);
    window.removeEventListener('offline', onChange);
  };
}

function snapshot(): boolean {
  return typeof navigator === 'undefined' ? true : navigator.onLine;
}

/**
 * Connectivity, as the browser understands it.
 *
 * `navigator.onLine` only means "there is a network interface" — a captive
 * portal reports online. It is enough to decide whether to *attempt* a flush,
 * which is all it is used for; the authority on whether a request worked is
 * the request.
 */
export function useOnline(): boolean {
  return useSyncExternalStore(subscribe, snapshot, () => true);
}
