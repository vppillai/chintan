import { useEffect, useState } from 'react';

/**
 * A value that follows its input only once the input has held still.
 *
 * For the one thing in the app that should not react to every keystroke: the
 * server search. Typing "flashing" at a normal pace sent eight
 * `GET /v1/search` requests whose answers landed out of order (QA D8). The
 * instant search over the device's corpus keeps reading the live value; the
 * server is asked for the word, not for each letter of it.
 */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [settled, setSettled] = useState(value);

  useEffect(() => {
    const timer = setTimeout(() => {
      setSettled(value);
    }, delayMs);
    return () => {
      clearTimeout(timer);
    };
  }, [value, delayMs]);

  return settled;
}
