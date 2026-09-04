import { mkdirSync, writeFileSync } from 'node:fs';
import process from 'node:process';

import type { ConsoleMessage, Page, Request, Response, TestInfo } from '@playwright/test';

import { SHOTS } from '../playwright.config.ts';

export const CREDS = {
  user: process.env['QA_USER'] ?? '',
  pass: process.env['QA_PASS'] ?? '',
};

export function requireCreds(): void {
  if (!CREDS.user || !CREDS.pass) {
    throw new Error('Set QA_USER and QA_PASS in the environment (the throwaway test account).');
  }
}

/** Screenshot at CSS-pixel scale, named `<project>-<name>.png`. */
export async function shot(page: Page, info: TestInfo, name: string, fullPage = false): Promise<string> {
  mkdirSync(SHOTS, { recursive: true });
  const file = `${SHOTS}/${info.project.name}-${name}.png`;
  await page.screenshot({ path: file, scale: 'css', fullPage, animations: 'disabled' });
  return file;
}

export interface Recorder {
  console: string[];
  requests: { method: string; url: string; status: number | null; ms: number; type: string; size: number; body?: string; fromSw?: boolean }[];
  failures: string[];
  dump(info: TestInfo, name: string): void;
  apiCalls(method?: string, pathPart?: string): Recorder['requests'];
}

/** Records console output and every request the page makes. */
export function record(page: Page): Recorder {
  const started = new Map<Request, number>();
  const rec: Recorder = {
    console: [],
    requests: [],
    failures: [],
    dump(info, name) {
      mkdirSync(SHOTS, { recursive: true });
      writeFileSync(
        `${SHOTS}/${info.project.name}-${name}.log.json`,
        JSON.stringify({ console: rec.console, failures: rec.failures, requests: rec.requests }, null, 1),
      );
    },
    apiCalls(method, pathPart) {
      return rec.requests.filter(
        (r) =>
          r.url.includes('execute-api') &&
          (!method || r.method === method) &&
          (!pathPart || r.url.includes(pathPart)),
      );
    },
  };
  page.on('console', (msg: ConsoleMessage) => {
    rec.console.push(`[${msg.type()}] ${msg.text()}`);
  });
  page.on('pageerror', (err) => {
    rec.console.push(`[pageerror] ${err.message}`);
  });
  page.on('request', (req) => started.set(req, Date.now()));
  page.on('requestfailed', (req) => {
    rec.failures.push(`${req.method()} ${req.url()} — ${req.failure()?.errorText ?? '?'}`);
    rec.requests.push({
      method: req.method(),
      url: req.url(),
      status: null,
      ms: Date.now() - (started.get(req) ?? Date.now()),
      type: req.resourceType(),
      size: 0,
    });
  });
  page.on('response', (res: Response) => {
    const req = res.request();
    const api = res.url().includes('execute-api');
    void res.body().then(
      (body) => {
        rec.requests.push({
          method: req.method(),
          url: res.url(),
          status: res.status(),
          ms: Date.now() - (started.get(req) ?? Date.now()),
          type: req.resourceType(),
          size: body.byteLength,
          ...(api && req.method() !== 'GET' ? { body: req.postData() ?? '' } : {}),
          ...(api ? { fromSw: res.fromServiceWorker() } : {}),
        });
      },
      () => {
        rec.requests.push({
          method: req.method(),
          url: res.url(),
          status: res.status(),
          ms: Date.now() - (started.get(req) ?? Date.now()),
          type: req.resourceType(),
          size: -1,
        });
      },
    );
  });
  return rec;
}

/** The whole visible text of the page, for the log. */
export async function text(page: Page): Promise<string> {
  return page.locator('body').innerText();
}

export function note(info: TestInfo, message: string): void {
  info.annotations.push({ type: 'observation', description: message });
  console.log(`  [${info.project.name}] ${message}`);
}

export const sleep = (ms: number): Promise<void> => new Promise((r) => setTimeout(r, ms));

/** Wait for the library to have finished its first load (rows, or an empty state). */
export async function libraryReady(page: Page): Promise<void> {
  await page.getByRole('heading', { name: 'Notes', level: 1 }).waitFor();
  await page
    .locator('.note-row, .screen__empty, .screen__count:has-text("Saved on this device")')
    .first()
    .waitFor({ timeout: 30_000 });
}
