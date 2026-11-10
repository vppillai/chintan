import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useId, useRef, useState } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { ConfirmDialog } from '@/components/ConfirmDialog.tsx';
import { useOnline } from '@/hooks/useOnline.ts';

import { TargetChooser } from './TargetChooser.tsx';

/** The server's limit, so the field cannot ask for a refusal. */
const TEXT_MAX = 20_000;

/**
 * Where the thought is kept while it is being typed: the tab's own storage,
 * under the app's prefix so sign-out clears it (`signOut.ts`). Back, an app
 * switch that discards the page, or leaving to find a connection brings it
 * back when Type instead is opened again; only Send and a confirmed Discard
 * remove it.
 */
export const TYPED_THOUGHT_KEY = 'chintan.typedThought';

/** Storage denied is a draft that lasts as long as the screen, as it always did. */
function draft(text?: string): string {
  try {
    if (text === undefined) return sessionStorage.getItem(TYPED_THOUGHT_KEY) ?? '';
    if (text === '') sessionStorage.removeItem(TYPED_THOUGHT_KEY);
    else sessionStorage.setItem(TYPED_THOUGHT_KEY, text);
  } catch {
    /* Nothing to do. */
  }
  return text ?? '';
}

/**
 * "Type instead": a thought typed where it would have been said, for a
 * meeting or a quiet room. `POST /v1/captures/text` makes the capture the
 * device inbox's text route makes — already transcribed — and the worker
 * routes, cleans and appends it as it does a recording, so it is filed by
 * the same rules and lands on Home's filing tray, or the chosen note's
 * banner, like one. The target is the recording's, carried over, and the
 * chooser is the same one. Sending needs the server, so offline the field
 * says so; the text waits in the tab's storage until it is sent or discarded.
 */
export function TypedThought({
  initialNoteId,
  onDone,
}: {
  initialNoteId: string | null;
  /** Leave the screen, to the note the thought went to, or Home. */
  onDone: (noteId: string | null) => void;
}) {
  const api = useApi();
  const queryClient = useQueryClient();
  const online = useOnline();
  const fieldId = useId();
  const [noteId, setNoteId] = useState(initialNoteId);
  const [text, setText] = useState(() => draft());
  const [confirming, setConfirming] = useState(false);
  /*
   * One key per request body: a retry after a lost answer is the same
   * capture, but an edited thought, another note, or a request the server
   * has already refused (a 4xx is stored against its key and replayed) is a
   * new one — reusing the key would replay the refusal or be refused as a
   * different request on the same key.
   */
  const sent = useRef<{ body: string; key: string } | null>(null);
  const send = useMutation({
    mutationFn: () => {
      const body = { text: text.trim(), note_id: noteId };
      const signature = JSON.stringify(body);
      if (sent.current?.body !== signature) sent.current = { body: signature, key: crypto.randomUUID() };
      return api.captureText(body, sent.current.key);
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status >= 400 && error.status < 500) sent.current = null;
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
      draft('');
      onDone(noteId);
    },
  });
  const ready = text.trim() !== '' && online && !send.isPending;

  return (
    <form
      className="typed-thought"
      onSubmit={(event) => {
        event.preventDefault();
        if (ready) send.mutate();
      }}
    >
      <TargetChooser noteId={noteId} onChoose={setNoteId} disabled={send.isPending} />
      <label className="visually-hidden" htmlFor={fieldId}>
        Your thought
      </label>
      <textarea
        id={fieldId}
        className="typed-thought__field"
        value={text}
        rows={5}
        maxLength={TEXT_MAX}
        placeholder="Type it as you would say it"
        // The person chose to type: the field is what they came for.
        // eslint-disable-next-line jsx-a11y/no-autofocus
        autoFocus
        onChange={(event) => {
          setText(draft(event.target.value));
        }}
      />
      {!online && <p className="capture__warning">Typing a thought needs a connection.</p>}
      {send.isError && (
        <p className="capture__warning" role="alert">
          {send.error instanceof ApiError ? send.error.userMessage : 'That did not go through. Try again.'}
        </p>
      )}
      <div className="typed-thought__actions">
        <button
          type="button"
          className="dialog__action"
          disabled={send.isPending}
          onClick={() => {
            // A typed paragraph is asked about, as a recording of any length is.
            if (text.trim() !== '') setConfirming(true);
            else onDone(initialNoteId);
          }}
        >
          Cancel
        </button>
        <button type="submit" className="dialog__action dialog__action--primary" disabled={!ready}>
          {send.isPending ? 'Sending…' : 'Send'}
        </button>
      </div>
      <ConfirmDialog
        open={confirming}
        title="Discard what you typed?"
        body="It has not been sent, and it is not saved anywhere else."
        confirmLabel="Discard"
        cancelLabel="Keep"
        destructive
        onCancel={() => {
          setConfirming(false);
        }}
        onConfirm={() => {
          draft('');
          onDone(initialNoteId);
        }}
      />
    </form>
  );
}
