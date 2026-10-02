import { useNavigate } from 'react-router';

import { useCreateNote } from '@/api/queries.ts';
import type { NoteKind } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { OverflowMenu } from '@/components/OverflowMenu.tsx';
import { NEW_NOTE_TITLE, type NewNoteState } from '@/features/notes/newNote.ts';
import { useOnline } from '@/hooks/useOnline.ts';

/**
 * A note without a recording: the + at the right of Home's header opens a
 * two-item menu, Note or Checklist, and the pick is one `POST /v1/notes`
 * with a placeholder title and the kind (`newNote.ts`), after which the note
 * screen opens with the title selected. The menu's own ⋮ semantics (`OverflowMenu`)
 * through a custom trigger, so the + is a 44 px disc like the row menus and
 * not a second record control: the disc on the tab bar is still the way a
 * note usually begins. Offline the server cannot mint an id to open, and
 * the edit queue carries only edits to notes that exist, so the + says so
 * rather than queueing a note nobody can open; a refused create says the
 * fixed sentence and keeps the + for another try.
 */
export function NewNote({ onNotice }: { onNotice: (sentence: string | null) => void }) {
  const online = useOnline();
  const navigate = useNavigate();
  const create = useCreateNote();

  const start = (kind: NoteKind): void => {
    if (!online) {
      onNotice('New note needs a connection.');
      return;
    }
    onNotice(null);
    void create.mutateAsync({ title: NEW_NOTE_TITLE[kind], kind }).then(
      (created) => {
        void navigate(ROUTES.note(created.id), { state: { focusTitle: true } satisfies NewNoteState });
      },
      () => {
        onNotice('The note could not be created. Try again.');
      },
    );
  };

  return (
    <OverflowMenu
      label="New note"
      items={[
        { label: 'Note', onSelect: () => start('note'), disabled: create.isPending },
        { label: 'Checklist', onSelect: () => start('checklist'), disabled: create.isPending },
      ]}
      trigger={(props) => (
        <button {...props} className="overflow__trigger" aria-busy={create.isPending}>
          <Icon name="plus" size={22} />
        </button>
      )}
    />
  );
}
