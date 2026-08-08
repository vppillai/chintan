import { useQuery } from '@tanstack/react-query';
import { useId, useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router';

import { useApi } from '@/api/ApiProvider.tsx';
import { queryKeys } from '@/api/queries.ts';
import type { SearchHitWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { useOnline } from '@/hooks/useOnline.ts';

import { rankLocal, type MergedHit } from './localSearch.ts';

/**
 * Search.
 *
 * Two sources, deliberately layered. The cached corpus is filtered locally on
 * every keystroke so results appear before the network answers and search keeps
 * working offline; `GET /v1/search` then refines and extends them, because it
 * can see transcript text the client never downloaded.
 *
 * The query lives in the URL, so a search is shareable, survives reload, and is
 * reachable by Back.
 */
export function SearchScreen() {
  const [params, setParams] = useSearchParams();
  const navigate = useNavigate();
  const api = useApi();
  const online = useOnline();
  const inputId = useId();
  const resultsId = useId();

  const query = params.get('q') ?? '';
  const trimmed = query.trim();

  // The corpus the library already loaded. No extra request, and it is what
  // makes the first keystroke feel instant.
  const { data: corpus } = useQuery({
    queryKey: queryKeys.notes({ state: 'active' }),
    queryFn: () => api.listNotes({ state: 'active', limit: 200 }),
    staleTime: 60_000,
  });

  const local = useMemo(
    () => rankLocal(corpus?.items ?? [], trimmed),
    [corpus, trimmed],
  );

  const server = useQuery({
    queryKey: queryKeys.search(trimmed),
    queryFn: () => api.search(trimmed),
    enabled: trimmed.length > 0 && online,
    staleTime: 30_000,
    retry: false,
  });

  const merged = useMemo(
    () => mergeResults(local, server.data?.items ?? []),
    [local, server.data],
  );

  const usingCachedOnly = !online || (server.isError && merged.length > 0);

  return (
    <div className="screen">
      <header className="screen__header">
        <h1>Search</h1>
      </header>

      <form
        className="search-form"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
        }}
      >
        <label className="visually-hidden" htmlFor={inputId}>
          Search notes
        </label>
        <input
          id={inputId}
          className="search-input"
          type="search"
          value={query}
          placeholder="Search notes"
          autoComplete="off"
          aria-controls={resultsId}
          onChange={(event) => {
            const next = event.target.value;
            // Replace, not push: typing must not turn Back into a
            // character-by-character undo.
            setParams(next ? { q: next } : {}, { replace: true });
          }}
        />
      </form>

      <p className="screen__count" aria-live="polite">
        {!trimmed
          ? 'Type to search your notes.'
          : `${merged.length} ${merged.length === 1 ? 'result' : 'results'}${
              server.isFetching ? ' so far…' : ''
            }`}
      </p>

      {trimmed && usingCachedOnly && (
        <p className="search-offline" role="status">
          {online
            ? 'Showing cached results — the server search did not respond.'
            : 'Searching offline — cached notes only. Transcripts are not included.'}
        </p>
      )}

      {trimmed && merged.length === 0 && !server.isFetching && (
        <p className="screen__empty">
          Nothing matches &ldquo;{trimmed}&rdquo;.
          {!online && ' You are offline, so only cached notes were searched.'}
        </p>
      )}

      <ul id={resultsId} className="note-list" role="list">
        {merged.map((hit) => (
          <li key={hit.noteId}>
            <button
              type="button"
              className="note-row"
              onClick={() => {
                void navigate(ROUTES.note(hit.noteId));
              }}
            >
              <span className="note-row__title">{hit.title}</span>
              {hit.excerpt && (
                <Excerpt text={hit.excerpt} term={trimmed} />
              )}
              {hit.matchedIn.length > 0 && (
                <span className="note-row__meta">
                  matched in {hit.matchedIn.join(', ')}
                </span>
              )}
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** Renders the match in situ, with the hit marked rather than stripped out. */
function Excerpt({ text, term }: { text: string; term: string }) {
  const index = term ? text.toLowerCase().indexOf(term.toLowerCase()) : -1;
  if (index === -1) return <span className="note-row__snippet">{text}</span>;

  return (
    <span className="note-row__snippet">
      {text.slice(0, index)}
      <mark className="search-hit">{text.slice(index, index + term.length)}</mark>
      {text.slice(index + term.length)}
    </span>
  );
}

/**
 * Local results first — they are already on screen and reordering under the
 * user's finger is worse than a stable list — then anything only the server
 * found, which in practice means transcript matches.
 */
export function mergeResults(
  local: readonly MergedHit[],
  remote: readonly SearchHitWire[],
): MergedHit[] {
  const byId = new Map<string, MergedHit>();

  for (const hit of local) byId.set(hit.noteId, hit);

  for (const hit of remote) {
    const existing = byId.get(hit.note_id);
    if (existing) {
      // Keep the local excerpt (already rendered) but take the server's richer
      // field list, which can include `transcript`.
      byId.set(hit.note_id, {
        ...existing,
        matchedIn: Array.from(new Set([...existing.matchedIn, ...(hit.matched_in ?? [])])),
      });
      continue;
    }
    byId.set(hit.note_id, {
      noteId: hit.note_id,
      title: hit.title,
      excerpt: hit.excerpt ?? '',
      matchedIn: hit.matched_in ?? [],
    });
  }

  return Array.from(byId.values());
}
