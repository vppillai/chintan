import { NoteRow } from '@/components/NoteRow.tsx';
import { PLACEHOLDER_NOTES } from '@/data/placeholderNotes.ts';

export function NotesScreen() {
  return (
    <div className="screen">
      <header className="screen__header">
        <h1>Notes</h1>
        <p className="screen__count">
          <span className="numeric">{PLACEHOLDER_NOTES.length}</span> notes
        </p>
      </header>

      <ul className="note-list" role="list">
        {PLACEHOLDER_NOTES.map((note) => (
          <li key={note.id}>
            <NoteRow note={note} />
          </li>
        ))}
      </ul>
    </div>
  );
}
