import { useCallback, useEffect, useState, type RefObject } from 'react';

/**
 * Inline audio playback.
 *
 * The audio element is owned here and never leaves the page. v1 handed the
 * presigned S3 URL to `window.open`, which on desktop downloads the file and on
 * mobile navigates out of the app — taking any unsaved edit with it.
 */

export interface PlayerState {
  playing: boolean;
  currentTime: number;
  duration: number;
  ready: boolean;
  error: string | null;
}

export interface Player extends PlayerState {
  toggle: () => void;
  seek: (seconds: number) => void;
  /** Seeks and starts playing — what tapping a transcript line does. */
  seekAndPlay: (seconds: number) => void;
}

/**
 * The audio element ref is owned by the caller and passed in, rather than
 * returned from here. Handing a ref back inside the result object makes every
 * read of that object a ref access as far as the React lint rules are
 * concerned, which is both noisy and a fair point: a hook's return value should
 * be values, not handles.
 */
export function usePlayer(
  src: string | null,
  audioRef: RefObject<HTMLAudioElement | null>,
): Player {
  const [state, setState] = useState<PlayerState>({
    playing: false,
    currentTime: 0,
    duration: 0,
    ready: false,
    error: null,
  });

  // Subscribing to the element's own events rather than polling keeps the
  // playhead honest when the browser stalls, buffers, or ends the track.
  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;

    const onTime = () => {
      setState((previous) => ({ ...previous, currentTime: audio.currentTime }));
    };
    const onLoaded = () => {
      setState((previous) => ({
        ...previous,
        // A stream with no container duration reports Infinity.
        duration: Number.isFinite(audio.duration) ? audio.duration : 0,
        ready: true,
        error: null,
      }));
    };
    const onPlay = () => {
      setState((previous) => ({ ...previous, playing: true }));
    };
    const onPause = () => {
      setState((previous) => ({ ...previous, playing: false }));
    };
    const onEnded = () => {
      setState((previous) => ({ ...previous, playing: false, currentTime: 0 }));
    };
    const onError = () => {
      setState((previous) => ({
        ...previous,
        playing: false,
        // A presigned URL outlives its expiry by minutes, not hours, so this is
        // a reachable state on a screen left open.
        error: 'This recording could not be played. It may have expired — reopen the note.',
      }));
    };

    audio.addEventListener('timeupdate', onTime);
    audio.addEventListener('loadedmetadata', onLoaded);
    audio.addEventListener('durationchange', onLoaded);
    audio.addEventListener('play', onPlay);
    audio.addEventListener('pause', onPause);
    audio.addEventListener('ended', onEnded);
    audio.addEventListener('error', onError);

    return () => {
      audio.removeEventListener('timeupdate', onTime);
      audio.removeEventListener('loadedmetadata', onLoaded);
      audio.removeEventListener('durationchange', onLoaded);
      audio.removeEventListener('play', onPlay);
      audio.removeEventListener('pause', onPause);
      audio.removeEventListener('ended', onEnded);
      audio.removeEventListener('error', onError);
    };
  }, [src, audioRef]);

  const toggle = useCallback(() => {
    const audio = audioRef.current;
    if (!audio) return;
    if (audio.paused) {
      void audio.play().catch(() => {
        setState((previous) => ({ ...previous, error: 'Playback was blocked.' }));
      });
    } else {
      audio.pause();
    }
  }, [audioRef]);

  const seek = useCallback((seconds: number) => {
    const audio = audioRef.current;
    if (!audio) return;
    const bounded = Math.max(0, Number.isFinite(audio.duration) ? Math.min(seconds, audio.duration) : seconds);
    audio.currentTime = bounded;
    // Updated optimistically so a tapped transcript line highlights instantly
    // rather than on the next `timeupdate`, which can be 250ms away.
    setState((previous) => ({ ...previous, currentTime: bounded }));
  }, [audioRef]);

  const seekAndPlay = useCallback(
    (seconds: number) => {
      seek(seconds);
      const audio = audioRef.current;
      if (audio?.paused) {
        void audio.play().catch(() => {
          /* Autoplay policy; the seek still happened. */
        });
      }
    },
    [seek, audioRef],
  );

  return { ...state, toggle, seek, seekAndPlay };
}
