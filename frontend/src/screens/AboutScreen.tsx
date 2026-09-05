import { Link } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { Icon } from '@/components/Icon.tsx';
import { config } from '@/config/env.ts';
import { describeVersion } from '@/features/settings/VersionFootnote.tsx';

export const REPOSITORY_URL = 'https://github.com/vppillai/chintan';
export const BACKLOG_URL = `${REPOSITORY_URL}/blob/main/docs/backlog.md`;

/**
 * About Chintan — reached from You (backlog U8).
 *
 * Four short sections: what it does, how filing decides where a recording
 * goes, where the data lives and for how long, and what is running. Written
 * for the person who was handed the app and wants to know what happens to
 * their voice before they trust it with a thought; the links at the end are
 * for the one who wants to read the code or see what is coming.
 *
 * Plain prose rather than a feature list, in the same serif the notes use:
 * this is a page to read, not a settings screen.
 */
export function AboutScreen() {
  const { release, build } = describeVersion(config.version);

  return (
    <div className="screen about">
      <header className="screen__header screen__header--detail">
        <Link to={ROUTES.settings} className="back-link">
          <Icon name="back" size={18} />
          <span className="visually-hidden">Back to </span>You
        </Link>
      </header>

      <h1 className="about__title">About Chintan</h1>
      <p className="about__lede">Speak a thought. It files itself.</p>

      <section className="about__section" aria-labelledby="about-does">
        <h2 id="about-does" className="about__heading">
          What it does
        </h2>
        <p>
          Tap Record and talk — while walking, driving, washing up. Chintan transcribes the
          recording, works out which of your notes it belongs to, tidies the words into text you
          would have typed, and appends it to that note. The recording stays beneath the note as
          its source, so you can always hear what you actually said.
        </p>
      </section>

      <section className="about__section" aria-labelledby="about-filing">
        <h2 id="about-filing" className="about__heading">
          How filing works
        </h2>
        <p>
          Say where it goes — &ldquo;add this to the roof note&rdquo; — and the router matches that
          against your notes&rsquo; titles, their other names and their tags. Or choose the note
          first: <em>Record into this</em> on a note, or the target picker on the recording
          screen, files straight there with no guessing. When the router is not sure, the
          recording waits at the top of your notes with its best guess until you pick a note or
          start a new one.
        </p>
        <p>
          Cleanup follows the note: <em>Faithful</em> fixes only what was clearly misheard,{' '}
          <em>Polished</em> tidies the wording as well, and a note marked verbatim is left exactly
          as spoken.
        </p>
        <p>
          A daily spending cap on the transcription and cleanup providers protects against runaway
          costs; if it is ever reached, recordings say so and resume the next day.
        </p>
      </section>

      <section className="about__section" aria-labelledby="about-data">
        <h2 id="about-data" className="about__heading">
          Where your data lives
        </h2>
        <p>
          In your own AWS account and nowhere else: sign-in in Cognito, notes in DynamoDB,
          recordings and transcripts in S3. Each recording is sent once to the transcription
          provider and its text once to a language model for cleanup; neither is asked to keep
          anything. This device keeps a copy of your notes so you can read and search them with
          no connection, and clears it when you sign out.
        </p>
        <p>
          Recordings are kept for as long as <em>Keep recordings for</em> on You says —
          indefinitely unless you set a limit — and an archived note, with its recordings and
          transcripts, is deleted thirty days after you archive it.
        </p>
      </section>

      <section className="about__section" aria-labelledby="about-version">
        <h2 id="about-version" className="about__heading">
          This build
        </h2>
        <p>
          <span className="visually-hidden">App version </span>
          <code className="about__version">
            {release}
            {build && (
              <span className="version-footnote__build">
                {`+${String(build.ahead)} (${build.sha}${build.dirty ? ', dirty' : ''})`}
              </span>
            )}
          </code>
          {config.instance !== 'dev' && <> on the {config.instance} instance</>}
        </p>
        <ul className="about__links" role="list">
          <li>
            <a className="text-link" href={REPOSITORY_URL} target="_blank" rel="noreferrer">
              Source on GitHub
            </a>
          </li>
          <li>
            <a className="text-link" href={BACKLOG_URL} target="_blank" rel="noreferrer">
              What&rsquo;s next — the backlog
            </a>
          </li>
        </ul>
      </section>
    </div>
  );
}
