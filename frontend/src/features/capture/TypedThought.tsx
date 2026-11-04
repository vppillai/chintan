import { useMutation, useQueryClient } from '@tanstack/react-query';
import { useId, useState } from 'react';

import { useApi } from '@/api/ApiProvider.tsx';
import { ApiError } from '@/api/problem.ts';
import { useOnline } from '@/hooks/useOnline.ts';

import { TargetChooser } from './TargetChooser.tsx';

/** The server's limit, so the field cannot ask for a refusal. */
const TEXT_MAX = 20_000;

/**
 * "Type instead": a thought typed where it would have been said, for a
 * meeting or a quiet room. `POST /v1/captures/text` makes the capture the
 * device inbox's text route makes — already transcribed — and the worker
 * routes, cleans and appends it as it does a recording, so it is filed by
 * the same rules and lands on Home's filing tray, or the chosen note's
 * banner, like one. The target is the recording's, carried over, and the
 * chooser is the same one. Sending needs the server: nothing here is kept
 * on the device, so offline the field says so instead.
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
  const [text, setText] = useState('');
  // One key per thought: a retry after a lost answer is the same capture.
  const [key] = useState(() => crypto.randomUUID());
  const send = useMutation({
    mutationFn: () => api.captureText({ text: text.trim(), note_id: noteId }, key),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['captures'] });
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
          setText(event.target.value);
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
            onDone(initialNoteId);
          }}
        >
          Cancel
        </button>
        <button type="submit" className="dialog__action dialog__action--primary" disabled={!ready}>
          {send.isPending ? 'Sending…' : 'Send'}
        </button>
      </div>
    </form>
  );
}
