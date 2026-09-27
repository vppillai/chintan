import { expect, test } from './fixtures.ts';

/**
 * The Notifications card on You, against an instance with no VAPID key pair —
 * which is every fresh deploy until the owner runs `scripts/vapid-keys.sh`
 * (docs/design/push.md). The stub answers `GET /v1/push/key` with the
 * contract's 404, and the card must say so rather than offer a switch that
 * cannot work, whatever this browser supports.
 */
test('You says notifications are not set up when the instance has no key', async ({
  page,
  api,
}) => {
  await page.goto('/settings');

  const card = page.getByRole('region', { name: 'Notifications' });
  await expect(card).toContainText(/not set up on this instance yet/i);
  await expect(card).toContainText('scripts/vapid-keys.sh');
  await expect(card.getByRole('switch')).toHaveCount(0);

  // Asked once, and nothing was subscribed or listed for an instance that
  // cannot send.
  expect(api.requests.filter((r) => r.url === '/v1/push/key')).toHaveLength(1);
  expect(api.requests.some((r) => r.url.startsWith('/v1/push/subscriptions'))).toBe(false);
});
