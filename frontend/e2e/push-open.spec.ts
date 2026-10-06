import { expect, test } from './fixtures.ts';

/**
 * Opening a note cold from a notification (docs/design/push.md, "The
 * service worker"): the document is the precached shell and the first thing
 * it does is `GET /v1/notes/{id}`. On a phone woken by the tap that first
 * request can fail or hang before it reaches the server, which has had the
 * note since before the push was sent. The screen must get there on its
 * own, with nothing pressed.
 */

test('a first read that failed on the way out is asked again, and the note appears', async ({
  page,
  api,
}) => {
  // The client retries a network failure three times on its own, so the
  // first four attempts fail as a dead link fails them; the fifth is answered.
  api.dropNoteGets = 4;
  await page.goto('/notes/roof-repair');

  // Not asserted on the way: "Not on this device" stands only between the
  // dropped read's failure and the next rung of the ladder, which re-reads a
  // query with no data and so clears its error — a window a fast machine
  // closes before the locator looks.
  await expect(page.getByRole('textbox', { name: 'Note body' })).toHaveValue(
    /Ridge tiles on the south slope/,
    { timeout: 10_000 },
  );
  expect(api.requests.filter((r) => r.url === '/v1/notes/roof-repair').length).toBeGreaterThanOrEqual(5);
});

test('a token refresh that never answers is given up on, and the note still appears', async ({
  page,
  api,
}) => {
  // The token set is hours stale, as it is after a day in a pocket, so the
  // first request must refresh first; the token endpoint never answers.
  await page.addInitScript(() => {
    localStorage.setItem(
      'chintan.tokens.v2',
      JSON.stringify({
        idToken: 'e2e-id-token',
        accessToken: 'e2e-access-token',
        refreshToken: 'e2e-refresh-token',
        expiresAt: Date.now() - 8 * 3_600_000,
        tokenType: 'Bearer',
      }),
    );
  });
  api.auth.stallToken = true;
  await page.goto('/notes/roof-repair');

  // Nothing reaches the API while the refresh is in the air; the screen
  // says so once its patience runs out rather than standing on "Loading…".
  await expect(page.getByText(/the server hasn’t answered yet/i)).toBeVisible({ timeout: 10_000 });
  expect(api.requests.some((r) => r.url.startsWith('/v1/notes'))).toBe(false);

  // The refresh is bounded (`REFRESH_TIMEOUT_MS`); the request then goes out
  // with the token it has, and the stub, like a server whose token is still
  // good, answers it.
  await expect(page.getByRole('textbox', { name: 'Note body' })).toHaveValue(
    /Ridge tiles on the south slope/,
    { timeout: 25_000 },
  );
  // At least the one the first request waited on. The count floats: the
  // token set is still stale after a timed-out refresh and `stallToken`
  // still stalls, so every query that starts after the first refresh gave
  // up — the captures poll, a ladder tick — owes a refresh of its own and
  // asks again. That is the client's rule (a due refresh before each
  // request), not this test's.
  expect(api.auth.token.length).toBeGreaterThanOrEqual(1);
});
