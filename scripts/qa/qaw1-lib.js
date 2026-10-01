// Live-QA step helpers on top of qaw1-base.js: a signed-in page with the fake microphone, PASS/FAIL results,
// every API exchange recorded, a contrast audit and the note-row menu helpers. See README.md.
const fs = require('fs');
const path = require('path');
const { chromium } = require(process.env.QA_PLAYWRIGHT || path.join(__dirname, '..', '..', 'frontend', 'node_modules', '@playwright', 'test'));
const base = require('./qaw1-base.js');

const HOME = base.HOME;
const APP = base.APP;
const API = process.env.API || (() => { throw new Error('API is not set: the API endpoint of the instance under test'); })();
const TOKENS = base.TOKENS;
const IDS_FILE = path.join(HOME, 'qaw1-ids.json');
const IDS = fs.existsSync(IDS_FILE) ? JSON.parse(fs.readFileSync(IDS_FILE, 'utf8')) : {};
const SPEECH_WAV = process.env.QA_SPEECH_WAV; // required for mic: true; checked in launch()
const sleep = base.sleep;
const ts = () => new Date().toISOString().slice(11, 19);

function saveIds(patch) {
  const cur = fs.existsSync(IDS_FILE) ? JSON.parse(fs.readFileSync(IDS_FILE, 'utf8')) : {};
  fs.writeFileSync(IDS_FILE, JSON.stringify({ ...cur, ...patch }, null, 1));
}

async function api(method, p, body, headers = {}) {
  const res = await fetch(API + p, {
    method,
    headers: { Authorization: `Bearer ${TOKENS.idToken}`, 'Content-Type': 'application/json', ...headers },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json = null;
  try { json = text ? JSON.parse(text) : null; } catch { json = text; }
  return { status: res.status, body: json };
}

async function launch(step, { profile = 'mobile', theme = 'ink', mic = false, viewport = null } = {}) {
  const device = base.PROFILES[profile];
  if (!device) throw new Error(`unknown profile ${profile}`);
  if (mic && !SPEECH_WAV) throw new Error('QA_SPEECH_WAV is not set: the clip the fake microphone plays');
  const args = mic
    ? ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream', `--use-file-for-fake-audio-capture=${SPEECH_WAV}`, '--autoplay-policy=no-user-gesture-required']
    : ['--deny-permission-prompts'];
  const browser = await chromium.launch({ headless: true, args });
  const context = await browser.newContext({ ...device, ...(viewport ? { viewport } : {}), locale: 'en-GB', timezoneId: 'America/Los_Angeles', colorScheme: theme === 'nocturne' ? 'dark' : 'light' });
  if (mic) await context.grantPermissions(['microphone'], { origin: new URL(APP).origin });
  await context.addInitScript(({ tokens, theme }) => {
    try {
      if (!localStorage.getItem('chintan.tokens.v2')) localStorage.setItem('chintan.tokens.v2', JSON.stringify(tokens));
      localStorage.setItem('chintan.theme', theme);
      localStorage.setItem('chintan.ask.cost-note-dismissed', '1');
    } catch (e) {}
  }, { tokens: TOKENS, theme });
  const page = await context.newPage();
  const tag = `${step}-${profile}-${theme}`;
  const logFile = path.join(HOME, `qaw1-${tag}.log`);
  fs.writeFileSync(logFile, '');
  const t0 = Date.now();
  const log = (msg) => { const line = `[${ts()} +${((Date.now() - t0) / 1000).toFixed(1)}s] ${msg}`; console.log(line); fs.appendFileSync(logFile, line + '\n'); };
  const errors = [];
  const warnings = [];
  const reqs = [];
  page.on('console', (m) => { if (m.type() === 'error') errors.push(`console.error: ${m.text().slice(0, 300)}`); else if (m.type() === 'warning') warnings.push(`warning: ${m.text().slice(0, 200)}`); });
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message.slice(0, 300)}`));
  page.on('requestfailed', (r) => { warnings.push(`requestfailed: ${r.method()} ${r.url().split('?')[0].replace(API, '')} ${r.failure()?.errorText}`); });
  page.on('response', async (r) => {
    const u = r.url();
    if (!u.startsWith(API)) { if (r.status() >= 400) warnings.push(`http ${r.status()} (non-API): ${r.request().method()} ${u.split('?')[0].slice(0, 160)}`); return; }
    const req = r.request();
    let resBody = null;
    try { let t = await r.text(); t = t.replace(/"key":"ck_[^"]*"/g, '"key":"<redacted>"'); resBody = t.length > 700 ? t.slice(0, 700) + '…' : t; } catch {}
    const entry = { t: +((Date.now() - t0) / 1000).toFixed(2), method: req.method(), path: u.replace(API, ''), status: r.status(), reqBody: req.postData() ?? null, resBody };
    reqs.push(entry);
    if (r.status() >= 400) errors.push(`http ${r.status()}: ${req.method()} ${u.replace(API, '').split('?')[0]} ${resBody ? resBody.slice(0, 160) : ''}`);
  });
  let n = 0;
  const shot = async (name, opts = {}) => {
    n += 1;
    const file = path.join(HOME, `qaw1-${tag}-${String(n).padStart(2, '0')}-${name}.png`);
    await page.screenshot({ path: file, fullPage: opts.fullPage ?? false });
    log(`shot ${path.basename(file)}`);
    return path.basename(file);
  };
  const cdp = await context.newCDPSession(page);
  const results = [];
  const check = (id, name, ok, detail = '') => { results.push({ id, name, ok: !!ok, detail: String(detail).slice(0, 900) }); log(`${ok ? 'PASS' : 'FAIL'} [${id}] ${name} — ${String(detail).slice(0, 500)}`); };
  const info = (id, name, detail = '') => { results.push({ id, name, ok: null, detail: String(detail).slice(0, 900) }); log(`INFO [${id}] ${name} — ${String(detail).slice(0, 500)}`); };
  const done = async () => {
    log('console errors: ' + JSON.stringify(errors));
    log('warnings: ' + JSON.stringify(warnings.slice(0, 12)));
    fs.writeFileSync(path.join(HOME, `qaw1-${tag}-reqs.json`), JSON.stringify(reqs, null, 1));
    fs.writeFileSync(path.join(HOME, `qaw1-${tag}-results.json`), JSON.stringify({ results, errors, warnings }, null, 1));
    console.log('RESULTS ' + JSON.stringify(results));
    await browser.close();
  };
  return { browser, context, page, log, shot, cdp, errors, warnings, reqs, results, check, info, done, tag };
}

async function touchStart(cdp, x, y) { await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] }); }
async function touchMoveTo(cdp, x0, y0, x1, y1, steps = 12, stepMs = 30) {
  for (let i = 1; i <= steps; i++) {
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: x0 + ((x1 - x0) * i) / steps, y: y0 + ((y1 - y0) * i) / steps }] });
    await sleep(stepMs);
  }
}
async function touchEnd(cdp) { await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] }); }
async function touchHold(cdp, x, y, ms) { await touchStart(cdp, x, y); await sleep(ms); await touchEnd(cdp); }
async function center(locator) { const box = await locator.boundingBox(); if (!box) throw new Error('no bounding box'); return { x: box.x + box.width / 2, y: box.y + box.height / 2, box }; }

/** Contrast helpers installed in the page as window.__qa. */
const AUDIT = `(() => {
  const parse = (c) => { const m = c.match(/rgba?\\(([^)]+)\\)/); if (!m) return null; const p = m[1].split(/[\\s,\\/]+/).filter(Boolean).map(Number); return { r: p[0], g: p[1], b: p[2], a: p.length > 3 ? p[3] : 1 }; };
  const lum = ({ r, g, b }) => { const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
  const ratio = (a, b) => { const l1 = lum(a), l2 = lum(b); return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05); };
  const blend = (top, under) => ({ r: top.r * top.a + under.r * (1 - top.a), g: top.g * top.a + under.g * (1 - top.a), b: top.b * top.a + under.b * (1 - top.a), a: 1 });
  const effectiveBg = (el) => { let layers = []; let node = el; while (node && node.nodeType === 1) { const c = parse(getComputedStyle(node).backgroundColor); if (c && c.a > 0) { layers.push(c); if (c.a >= 1) break; } node = node.parentElement; } let bg = { r: 255, g: 255, b: 255, a: 1 }; for (let i = layers.length - 1; i >= 0; i--) bg = blend(layers[i], bg); return bg; };
  const hex = (c) => '#' + [c.r, c.g, c.b].map((v) => Math.round(v).toString(16).padStart(2, '0')).join('');
  const colorOn = (colorStr, el) => { const fg0 = parse(colorStr); const bg = effectiveBg(el); const fg = fg0.a < 1 ? blend(fg0, bg) : fg0; return { ratio: +ratio(fg, bg).toFixed(2), fg: hex(fg), bg: hex(bg) }; };
  const resolveVar = (name, host) => { const d = document.createElement('span'); d.style.color = 'var(' + name + ')'; (host || document.body).appendChild(d); const c = getComputedStyle(d).color; d.remove(); return c; };
  window.__qa = { parse, colorOn, effectiveBg, hex, resolveVar };
})()`;
async function audit(page) { await page.evaluate(AUDIT); }

const pinnedTitles = (page) => page.locator('.note-group--pinned .note-row__title').allInnerTexts().then((a) => a.map((t) => t.trim()));
const rowWrap = (page, title) => page.locator('.note-row-wrap', { has: page.locator('.note-row__title', { hasText: title }) }).first();
async function openRowMenu(page, title, mobile) {
  const trig = rowWrap(page, title).locator('.overflow__trigger');
  if (mobile) await trig.tap();
  else {
    // The ⋮ is revealed by the row's hover/focus on a fine pointer: hover the row first.
    await rowWrap(page, title).locator('.note-row').hover();
    await sleep(150);
    await trig.click({ force: true });
  }
  await page.waitForSelector('[role="menu"]', { timeout: 5000 });
  const items = await page.locator('[role="menuitem"]').evaluateAll((els) => els.map((e) => ({ label: e.textContent.trim(), disabled: e.disabled || e.getAttribute('aria-disabled') === 'true' })));
  return items;
}
async function pickMenu(page, label, mobile) {
  const item = page.locator('[role="menuitem"]', { hasText: new RegExp(`^${label}$`) });
  if (mobile) await item.tap(); else await item.click();
}
async function closeMenu(page) { await page.keyboard.press('Escape'); await sleep(150); }
const labels = (m) => m.map((i) => i.label).join('|');

module.exports = { HOME, APP, API, TOKENS, IDS, IDS_FILE, saveIds, api, launch, audit, sleep, ts, touchStart, touchMoveTo, touchEnd, touchHold, center, pinnedTitles, rowWrap, openRowMenu, pickMenu, closeMenu, labels };
