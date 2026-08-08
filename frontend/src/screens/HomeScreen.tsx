import { RecordButton } from '@/components/RecordButton.tsx';

/**
 * Record-first home (spec §5.2): a large centred record target with nothing
 * competing for attention. Everything else in the app lives behind the strip.
 */
export function HomeScreen() {
  return (
    <div className="home">
      <h1 className="visually-hidden">Chintan</h1>
      <RecordButton variant="hero" />
      <p className="home__hint">Speak a thought. It files itself.</p>
    </div>
  );
}
