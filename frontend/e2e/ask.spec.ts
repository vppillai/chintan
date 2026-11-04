import { expect, test } from './fixtures.ts';

/**
 * Ask from Home. `/v1/ask` is answered per test: the stub has no Ask of its
 * own, and the two cases here are what the client has to survive, not the
 * happy path the unit tests already walk.
 */

test('a 2xx that is not an Ask row fails the question, and Home still draws', async ({ page }) => {
  await page.route('**/v1/ask', (route) =>
    route.fulfill({ status: 202, contentType: 'application/json', body: '{"items":[]}' }),
  );
  await page.goto('/?mode=ask');
  await page.getByRole('searchbox').first().fill('What did Ellis quote?');
  await page.keyboard.press('Enter');

  await expect(page.getByText('The answer could not be fetched. Try again.')).toBeVisible();
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Notes' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'This screen could not be drawn' })).toHaveCount(0);
});

test('offline, a question fails at once and says it needs a connection', async ({ page, context, api }) => {
  await page.goto('/?mode=ask');
  await expect(page.getByRole('searchbox').first()).toBeVisible();
  await context.setOffline(true);
  await page.getByRole('searchbox').first().fill('What did Ellis quote?');
  await page.keyboard.press('Enter');

  await expect(page.getByText('Ask needs a connection.')).toBeVisible();
  expect(api.requests.filter((r) => r.url === '/v1/ask')).toHaveLength(0);
  await context.setOffline(false);
});
