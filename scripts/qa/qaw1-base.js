// Shared helpers for the live QA pass. Plain node + playwright (no test runner),
// so each step script can be re-run on its own while the app is being poked.
const fs = require('fs');
const path = require('path');
// Playwright comes from a frontend checkout's node_modules (QA_PLAYWRIGHT names another); the app under test and
// where to write come from the environment — see README.md.
const { chromium, devices } = require(process.env.QA_PLAYWRIGHT || path.join(__dirname, '..', '..', 'frontend', 'node_modules', '@playwright', 'test'));

const HOME = process.env.QA_HOME || process.cwd();
const APP = process.env.APP || (() => { throw new Error('APP is not set: the Pages URL of the instance under test'); })();
const TOKENS = JSON.parse(fs.readFileSync(process.env.TOKENS || path.join(require('os').homedir(), 'review-tokens.json'), 'utf8'));

const PROFILES = {
  mobile: { ...devices['Pixel 7'], userAgent: devices['Pixel 7'].userAgent.replace('Pixel 7', 'Pixel 8 Pro'),
            viewport: { width: 412, height: 915 }, deviceScaleFactor: 2.625, isMobile: true, hasTouch: true },
  desktop: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } },
};

function makeLogger(profile, step) {
  const file = path.join(HOME, `${profile}-${step}.log`);
  fs.writeFileSync(file, '');
  const t0 = Date.now();
  return (msg) => {
    const line = `[+${((Date.now() - t0) / 1000).toFixed(1)}s] ${msg}`;
    console.log(line);
    fs.appendFileSync(file, line + '\n');
  };
}

async function launch(profile, step, { theme = null, seedCostNote = false } = {}) {
  const device = PROFILES[profile];
  if (!device) throw new Error(`unknown profile ${profile}`);
  const browser = await chromium.launch({ headless: true, args: ['--deny-permission-prompts'] });
  const context = await browser.newContext({ ...device, locale: 'en-GB', timezoneId: 'America/Los_Angeles',
    colorScheme: 'light', recordVideo: undefined });
  await context.addInitScript(({ tokens, theme, seedCostNote }) => {
    try {
      if (!localStorage.getItem('chintan.tokens.v2')) localStorage.setItem('chintan.tokens.v2', JSON.stringify(tokens));
      if (theme) localStorage.setItem('chintan.theme', theme);
      if (seedCostNote) localStorage.setItem('chintan.ask.cost-note-dismissed', '1');
    } catch (e) {}
  }, { tokens: TOKENS, theme, seedCostNote });
  const page = await context.newPage();
  const log = makeLogger(profile, step);
  const consoleErrors = [];
  page.on('console', (m) => { if (m.type() === 'error' || m.type() === 'warning') consoleErrors.push(`${m.type()}: ${m.text()}`); });
  page.on('pageerror', (e) => consoleErrors.push(`pageerror: ${e.message}`));
  page.on('requestfailed', (r) => consoleErrors.push(`requestfailed: ${r.method()} ${r.url()} ${r.failure()?.errorText}`));
  let n = 0;
  const shot = async (name, opts = {}) => {
    n += 1;
    const file = path.join(HOME, `${profile}-${step}-${String(n).padStart(2, '0')}-${name}.png`);
    await page.screenshot({ path: file, fullPage: opts.fullPage ?? false });
    log(`shot ${path.basename(file)}`);
    return file;
  };
  const cdp = await context.newCDPSession(page);
  return { browser, context, page, log, shot, cdp, consoleErrors };
}

async function touchSwipe(cdp, x, y, dx, dy = 0, steps = 12, stepMs = 16) {
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
  for (let i = 1; i <= steps; i++) {
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x + (dx * i) / steps, y: y + (dy * i) / steps }] });
    await new Promise((r) => setTimeout(r, stepMs));
  }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
}

async function touchHold(cdp, x, y, ms) {
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
  await new Promise((r) => setTimeout(r, ms));
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
}

async function center(locator) {
  const box = await locator.boundingBox();
  if (!box) throw new Error('no bounding box');
  return { x: box.x + box.width / 2, y: box.y + box.height / 2, box };
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

module.exports = { launch, touchSwipe, touchHold, center, sleep, APP, HOME, PROFILES, TOKENS };
