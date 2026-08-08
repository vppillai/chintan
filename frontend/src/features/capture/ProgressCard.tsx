import { useNavigate } from 'react-router';

import { useRetryCapture, usePendingCaptures } from '@/api/queries.ts';
import type { CaptureStatus, CaptureWire } from '@/api/schema.ts';
import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';

/**
 * The capture progress card.
 *
 * Backed by `GET /v1/captures?status=pending` rather than a JavaScript
 * variable, so it survives navigation, reload, and app restart. v1 held the
 * in-flight capture id in a module-level field; a refresh stranded the audio
 * with no UI anywhere able to find it again.
 *
 * The stage labels are honest about where the pipeline actually is. There is
 * no determinate bar, because the client cannot know how long transcription
 * will take and a bar that sits at 100% reads as broken.
 */

const STAGES: { status: CaptureStatus; label: string }[] = [
  { status: 'uploaded', label: 'Queued' },
  { status: 'transcribing', label: 'Transcribing' },
  { status: 'routing', label: 'Filing' },
  { status: 'cleaning', label: 'Cleaning up' },
  { status: 'appending', label: 'Saving' },
];

function stageIndex(status: CaptureStatus): number {
  const index = STAGES.findIndex((stage) => stage.status === status);
  return index === -1 ? STAGES.length : index;
}

function describe(capture: CaptureWire): string {
  switch (capture.status) {
    case 'appended':
      return 'Filed';
    case 'needs_target':
      return 'Which note should this go in?';
    case 'no_content':
      return 'Nothing to save from that recording';
    case 'spend_capped':
      return 'Daily spending cap reached';
    case 'failed':
      return capture.error ?? 'That capture did not finish';
    default:
      return STAGES[stageIndex(capture.status)]?.label ?? 'Working';
  }
}

export function ProgressCard() {
  const navigate = useNavigate();
  const { data } = usePendingCaptures();
  const retry = useRetryCapture();

  const captures = data?.items ?? [];
  if (captures.length === 0) return null;

  return (
    <section className="progress-stack" aria-label="Captures in progress">
      {captures.map((capture) => (
        <CaptureCard
          key={capture.id}
          capture={capture}
          onOpen={() => {
            if (capture.note_id) void navigate(ROUTES.note(capture.note_id));
          }}
          onRetry={() => retry.mutate(capture.id)}
          retrying={retry.isPending && retry.variables === capture.id}
        />
      ))}
    </section>
  );
}

interface CaptureCardProps {
  capture: CaptureWire;
  onOpen: () => void;
  onRetry: () => void;
  retrying: boolean;
}

function CaptureCard({ capture, onOpen, onRetry, retrying }: CaptureCardProps) {
  const failed = capture.status === 'failed' || capture.status === 'spend_capped';
  const done = capture.status === 'appended';
  const current = stageIndex(capture.status);

  return (
    <article className="progress-card" data-status={capture.status}>
      <div className="progress-card__body">
        <p className="progress-card__label" role="status" aria-live="polite">
          {describe(capture)}
        </p>

        {!failed && !done && (
          <ol className="progress-card__stages" aria-label="Pipeline stage">
            {STAGES.map((stage, index) => (
              <li
                key={stage.status}
                className="progress-card__stage"
                data-state={index < current ? 'done' : index === current ? 'active' : 'todo'}
              >
                <span className="visually-hidden">
                  {stage.label}
                  {index < current ? ' complete' : index === current ? ' in progress' : ' pending'}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>

      <div className="progress-card__actions">
        {done && capture.note_id && (
          <button type="button" className="progress-card__action" onClick={onOpen}>
            <span>Open</span>
            <Icon name="back" size={16} className="progress-card__open-icon" />
          </button>
        )}

        {/*
          A real Retry, wired to POST /v1/captures/{id}/retry. In v1 the client
          method existed and was called from nowhere, so a failed capture was a
          dead end with a toast.
        */}
        {failed && (
          <button
            type="button"
            className="progress-card__action"
            onClick={onRetry}
            disabled={retrying}
          >
            <span>{retrying ? 'Retrying…' : 'Retry'}</span>
          </button>
        )}
      </div>
    </article>
  );
}
