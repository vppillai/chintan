/**
 * Placeholder library content so the shell is demonstrable.
 *
 * The API client, IndexedDB corpus, and TanStack Query wiring are later phases
 * (spec §5.1, §5.5). This module exists only so the sheet, the note rows, and
 * search have something real-shaped to render, and is deleted when the client
 * lands.
 */
export interface PlaceholderNote {
  id: string;
  title: string;
  snippet: string;
  updatedAt: string;
  captureCount: number;
  tags: readonly string[];
}

export const PLACEHOLDER_NOTES: readonly PlaceholderNote[] = [
  {
    id: 'roof-repair',
    title: 'Roof repair',
    snippet:
      'Ridge tiles on the south slope have slipped. Get two quotes before the autumn rain.',
    updatedAt: '2026-08-06T09:14:00.000Z',
    captureCount: 4,
    tags: ['house'],
  },
  {
    id: 'reading-list',
    title: 'Reading list',
    snippet:
      'Seeing Like a State, then the Vitruvius translation someone recommended on the walk.',
    updatedAt: '2026-08-04T18:02:00.000Z',
    captureCount: 11,
    tags: ['books'],
  },
  {
    id: 'q3-planning',
    title: 'Q3 planning',
    snippet:
      'The async pipeline has to land before anything user-visible; everything else queues behind it.',
    updatedAt: '2026-08-02T07:41:00.000Z',
    captureCount: 7,
    tags: ['work'],
  },
  {
    id: 'garden',
    title: 'Garden',
    snippet: 'Move the rosemary before it woodens. The bed by the wall stays dry all summer.',
    updatedAt: '2026-07-29T16:20:00.000Z',
    captureCount: 2,
    tags: ['house', 'seasonal'],
  },
  {
    id: 'car-service',
    title: 'Car service',
    snippet: 'Rear pads at 3mm. Booked for the 22nd; ask about the intermittent dash warning.',
    updatedAt: '2026-07-21T11:55:00.000Z',
    captureCount: 3,
    tags: ['admin'],
  },
];

export function findPlaceholderNote(id: string): PlaceholderNote | undefined {
  return PLACEHOLDER_NOTES.find((note) => note.id === id);
}

export function searchPlaceholderNotes(query: string): readonly PlaceholderNote[] {
  const needle = query.trim().toLowerCase();
  if (!needle) return [];
  return PLACEHOLDER_NOTES.filter((note) =>
    `${note.title} ${note.snippet} ${note.tags.join(' ')}`.toLowerCase().includes(needle),
  );
}
