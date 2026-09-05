import { Link } from 'react-router';

import { ROUTES } from '@/app/routes.ts';
import { Icon, type IconName } from '@/components/Icon.tsx';
import { config } from '@/config/env.ts';
import { describeVersion } from '@/features/settings/VersionFootnote.tsx';

export const REPOSITORY_URL = 'https://github.com/vppillai/chintan';
export const BACKLOG_URL = `${REPOSITORY_URL}/blob/main/docs/backlog.md`;
export const LICENSE_URL = `${REPOSITORY_URL}/blob/main/LICENSE`;

/**
 * The five things that happen to a recording, in the order they happen. The
 * strip is the one place the whole pipeline is drawn; the prose beneath it
 * says how the routing step decides.
 */
const STEPS: readonly { icon: IconName; title: string; detail: string }[] = [
  { icon: 'mic', title: 'Record', detail: 'Tap Record and speak. The audio uploads as you go.' },
  { icon: 'transcribe', title: 'Transcribe', detail: 'Speech becomes text, in the language you set.' },
  { icon: 'route', title: 'Route', detail: 'The router works out which note this belongs to.' },
  { icon: 'sparkle', title: 'Clean up', detail: 'Misheard words fixed; the wording tidied if you ask.' },
  { icon: 'append', title: 'Append', detail: 'The text lands in the note, the recording beneath it.' },
];

/** Where each kind of data lives, in the account that owns it. */
const STORES: readonly { what: string; where: string }[] = [
  { what: 'Sign-in', where: 'Cognito' },
  { what: 'Notes', where: 'DynamoDB' },
  { what: 'Recordings and transcripts', where: 'S3' },
];

/**
 * About — reached from You (backlog U8).
 *
 * A page, not a settings screen: the app's name and its one sentence as the
 * hero, a paragraph on what it does, then three sections a person can find by
 * their icons — how filing works, drawn as the five steps a recording takes;
 * where the data lives and for how long, with the spend-cap sentence; and the
 * licence, the code and the running build. Written for the person who was
 * handed the app and wants to know what happens to their voice before they
 * trust it with a thought; the links at the end are for the one who wants to
 * read the code or see what is coming.
 *
 * The prose keeps the notes' serif — this is still a page to read — and on a
 * wide screen each section's heading sits in a column to the left of its body,
 * so the three headings read down the page as a table of contents.
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

      <div className="about__hero">
        <h1 className="about__title">{config.appName}</h1>
        <p className="about__lede">{config.appDescription}</p>
        <p className="about__intro">
          Tap Record and talk — while walking, driving, washing up. {config.appName} transcribes
          the recording, works out which of your notes it belongs to, tidies the words into text
          you would have typed, and appends it to that note. The recording stays beneath the note
          as its source, so you can always hear what you actually said.
        </p>
      </div>

      <section className="about__section" aria-labelledby="about-filing">
        <div className="about__section-head">
          <span className="about__disc" aria-hidden="true">
            <Icon name="route" size={20} />
          </span>
          <h2 id="about-filing" className="about__heading">
            How filing works
          </h2>
        </div>
        <div className="about__body">
          <ol className="about__steps" aria-label="The five steps a recording takes">
            {STEPS.map((step, index) => (
              <li key={step.title} className="about__step">
                <span className="about__step-glyph" aria-hidden="true">
                  <Icon name={step.icon} size={20} />
                </span>
                <span className="about__step-title">
                  <span className="about__step-number numeric" aria-hidden="true">
                    {String(index + 1)}
                  </span>
                  {step.title}
                </span>
                <span className="about__step-detail">{step.detail}</span>
              </li>
            ))}
          </ol>
          <p>
            Say where it goes — &ldquo;add this to the roof note&rdquo; — and the router matches
            that against your notes&rsquo; titles, their other names and their tags. Or choose the
            note first: <em>Record into this</em> on a note, or the target picker on the recording
            screen, files straight there with no guessing. When the router is not sure, the
            recording waits at the top of your notes with its best guess until you pick a note or
            start a new one.
          </p>
          <p>
            Cleanup follows the note: <em>Faithful</em> fixes only what was clearly misheard,{' '}
            <em>Polished</em> tidies the wording as well, and a note marked verbatim is left
            exactly as spoken.
          </p>
        </div>
      </section>

      <section className="about__section" aria-labelledby="about-data">
        <div className="about__section-head">
          <span className="about__disc" aria-hidden="true">
            <Icon name="shield" size={20} />
          </span>
          <h2 id="about-data" className="about__heading">
            Your data
          </h2>
        </div>
        <div className="about__body">
          <dl className="about__stores">
            {STORES.map((store) => (
              <div key={store.what} className="about__store">
                <dt className="about__store-what">{store.what}</dt>
                <dd className="about__store-where">{store.where}</dd>
              </div>
            ))}
          </dl>
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
          <p>
            A daily spending cap on the transcription and cleanup providers protects against
            runaway costs; if it is ever reached, recordings say so and resume the next day.
          </p>
        </div>
      </section>

      <section className="about__section" aria-labelledby="about-source">
        <div className="about__section-head">
          <span className="about__disc" aria-hidden="true">
            <Icon name="code" size={20} />
          </span>
          <h2 id="about-source" className="about__heading">
            Privacy &amp; open source
          </h2>
        </div>
        <div className="about__body">
          <p>
            {config.appName} is open source under the{' '}
            <a className="text-link" href={LICENSE_URL} target="_blank" rel="noreferrer">
              MIT licence
            </a>
            . There are no analytics and no accounts of yours with anyone else: the app talks to
            your own AWS account and, through it, to the transcription and language-model
            providers described above.
          </p>
          <ul className="about__links" role="list">
            <li>
              <a className="about__link" href={REPOSITORY_URL} target="_blank" rel="noreferrer">
                Source on GitHub
                <Icon name="external" size={16} />
              </a>
            </li>
            <li>
              <a className="about__link" href={BACKLOG_URL} target="_blank" rel="noreferrer">
                What&rsquo;s next — the backlog
                <Icon name="external" size={16} />
              </a>
            </li>
          </ul>
          <p className="about__build">
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
        </div>
      </section>
    </div>
  );
}
