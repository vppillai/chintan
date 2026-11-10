/**
 * A tapped notification, opened without replacing the document.
 *
 * The worker used to `navigate()` the open window to the note. That is a
 * document navigation: the app reloads, and a window that was recording
 * (a phone keeps recording with the screen locked) or uploading lost the
 * take in the air. So the worker asks the window to route there itself, over
 * a channel the window answers on, and navigates only a window that does not
 * answer: one with no shell mounted (signed out), or another deploy of the
 * app on the same origin, which the URL is not for.
 *
 * Both halves live here, the worker's (`openFromNotification`) and the
 * window's (`pathToOpen`), so the message has one definition and the worker's
 * half can be tested without a worker.
 */

/** How long a focused window has to answer before it is navigated instead. */
const OPEN_ANSWER_MS = 3_000;

interface OpenMessage {
  type: 'OPEN';
  url: string;
}

/** The slice of `WindowClient` the open needs. */
interface OpenableWindow {
  focus(): Promise<unknown>;
  navigate(url: string): Promise<unknown>;
  postMessage(message: OpenMessage, transfer: Transferable[]): void;
}

/** The slice of `Clients` the open needs. */
interface OpenableClients {
  matchAll(options: { type: 'window'; includeUncontrolled: true }): Promise<readonly OpenableWindow[]>;
  openWindow(url: string): Promise<unknown>;
}

function ask(client: OpenableWindow, url: string, waitMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    const channel = new MessageChannel();
    const settle = (answered: boolean): void => {
      clearTimeout(timer);
      channel.port1.close();
      resolve(answered);
    };
    const timer = setTimeout(() => {
      settle(false);
    }, waitMs);
    channel.port1.onmessage = () => {
      settle(true);
    };
    client.postMessage({ type: 'OPEN', url }, [channel.port2]);
  });
}

/** The worker's half: the `notificationclick` handler's whole decision. */
export async function openFromNotification(
  clients: OpenableClients,
  url: string,
  waitMs: number = OPEN_ANSWER_MS,
): Promise<void> {
  const windows = await clients.matchAll({ type: 'window', includeUncontrolled: true });
  const open = windows[0];
  if (open) {
    // `navigate` rejects for a window this worker does not control (the
    // match included uncontrolled ones on purpose, so a tab mid-install
    // is still found); a new window is the fallback, not a dead tap.
    try {
      await open.focus();
      if (await ask(open, url, waitMs)) return;
      await open.navigate(url);
      return;
    } catch {
      /* fall through to a new window */
    }
  }
  await clients.openWindow(url);
}

/**
 * The window's half: the in-app path an `OPEN` message asks for, or null
 * when the message is something else or its URL lies outside this app's base
 * (`import.meta.env.BASE_URL`, slash and all).
 */
export function pathToOpen(data: unknown, base: string, origin: string): string | null {
  const message = data as Partial<OpenMessage> | null;
  if (message?.type !== 'OPEN' || typeof message.url !== 'string') return null;
  let url: URL;
  try {
    url = new URL(message.url);
  } catch {
    return null;
  }
  if (url.origin !== origin || !url.pathname.startsWith(base)) return null;
  return `/${url.pathname.slice(base.length)}${url.search}`;
}
