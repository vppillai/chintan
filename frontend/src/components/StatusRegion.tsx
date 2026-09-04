/**
 * The single polite live region.
 *
 * Capture pipeline progress and route announcements are written here so they
 * are spoken rather than merely drawn. It is always mounted — a live region
 * added to the DOM at the same time as its text is frequently not announced.
 */
export function StatusRegion({ message }: { message: string }) {
  return (
    <div
      className="visually-hidden"
      role="status"
      aria-live="polite"
      aria-atomic="true"
      data-testid="status-region"
    >
      {message}
    </div>
  );
}
