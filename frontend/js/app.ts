/**
 * Entry point for the Phase 0 surface: display this build's version and the API's,
 * and flag a mismatch (§0.6).
 *
 * No framework, by decision (docs/decisions/0004-frontend-stack.md), and the
 * reasoning binds here even though this page has no latency requirement of its
 * own: the capture face must be interactive inside a 2-second trigger-to-recording
 * budget (§11A.5, §4A.1), and a framework adopted for a convenient version page is
 * a framework that is already in the bundle by the time that budget matters. So:
 * vanilla DOM, one fetch, and everything on the boot path is synchronous.
 *
 * Two rules this module does not bend:
 *   - Text reaches the DOM through textContent, never innerHTML. The API response
 *     is content crossing a trust boundary (§9.7); innerHTML is the injection sink
 *     and there is no reason to open it for a version string.
 *   - Nothing is registered, cached, or fetched before first paint except the
 *     health check itself. The service worker registration waits for `load`.
 */
import { BUILD } from "./build";
import { compare } from "./drift";
import type { Verdict } from "./drift";
import { clip, fetchHealth } from "./health";
import type { HealthResult } from "./health";

/**
 * Fails loudly on a missing element instead of returning null and no-oping. A
 * version page that silently renders nothing is worse than one that errors: the
 * absence of a mismatch warning would be read as the absence of a mismatch.
 */
function element(id: string): HTMLElement {
    const found = document.getElementById(id);
    if (found === null) {
        throw new Error(`app: no element with id '${id}' — index.html and this bundle disagree`);
    }
    return found;
}

const EM_DASH = "—";

/**
 * The API-side facts, keyed by element id.
 *
 * clip() here rather than in health.ts: this is the boundary that motivates it. A
 * proxy error page or a truncated body renders as a wall of text that destroys the
 * layout on a 320px screen (§9.7, §4A.6), and the comparison in drift.ts must see
 * the full strings — see clip()'s own comment.
 */
function renderApi(result: HealthResult): void {
    const fields: Record<string, string> = result.ok
        ? {
              "api-version": clip(result.health.version),
              "api-commit": clip(result.health.commit),
              "api-build-time": clip(result.health.build_time),
              "api-instance": clip(result.health.instance),
              "api-config-version": String(result.health.config_version),
          }
        : {
              "api-version": EM_DASH,
              "api-commit": EM_DASH,
              "api-build-time": EM_DASH,
              "api-instance": EM_DASH,
              "api-config-version": EM_DASH,
          };

    for (const [id, value] of Object.entries(fields)) {
        element(id).textContent = value;
    }
}

function renderVerdict(verdict: Verdict): void {
    // data-state drives the marker colour in css/app.css. `pending` is the only
    // attention hue in this system: §4A.2 reserves `live` for recording, so a
    // mismatch is never red however much it wants to be.
    element("verdict").dataset.state = verdict.state;
    element("verdict-line").textContent = verdict.headline;

    const list = element("verdict-reasons");
    list.replaceChildren();
    for (const reason of verdict.reasons) {
        const item = document.createElement("li");
        item.textContent = reason;
        list.append(item);
    }
}

async function check(button: HTMLButtonElement): Promise<void> {
    button.disabled = true;
    element("verdict").dataset.state = "checking";
    element("verdict-line").textContent = "Checking the API version…";
    try {
        const result = await fetchHealth(BUILD.apiBaseUrl);
        renderApi(result);
        renderVerdict(compare(BUILD, result));
    } finally {
        // In `finally` so a thrown render bug cannot leave the only control on the
        // page permanently disabled.
        button.disabled = false;
    }
}

function registerServiceWorker(): void {
    // A service worker needs a secure context. Skipping the registration on
    // http:// keeps the console clean when the bundle is opened from a file or a
    // plain local server, rather than logging a failure that looks like a bug.
    if (!("serviceWorker" in navigator) || !window.isSecureContext) {
        return;
    }
    // Relative path AND relative scope. GitHub Pages serves a project site under
    // /{repo}/, so "/sw.js" resolves to the user site and registers nothing
    // (§10.6, G-007).
    void navigator.serviceWorker.register("./sw.js", { scope: "./" }).catch(() => {
        // Nothing here is load-bearing for Phase 0: without a worker the app is an
        // ordinary page. Swallowed rather than surfaced so a registration failure
        // does not masquerade as a version problem on the one surface whose job is
        // reporting version problems.
    });
}

/**
 * Reports a failure of this module itself, on the page.
 *
 * element() throws when index.html and this bundle disagree, and it should — but an
 * uncaught throw at module scope leaves the page reading "Checking the API version…"
 * forever, which is precisely the failure element()'s own comment says it exists to
 * prevent: the absence of a warning read as the absence of a mismatch. Failing loudly
 * has to mean loudly HERE, not only in a console nobody has open.
 *
 * Written with getElementById directly and null-checked, because the one situation
 * this runs in is the situation where an element is missing.
 */
function reportBootFailure(error: unknown): void {
    const message = error instanceof Error ? error.message : "this page and its bundle disagree";
    const line = document.getElementById("verdict-line");
    if (line !== null) {
        line.textContent = message;
    }
    const verdict = document.getElementById("verdict");
    if (verdict !== null) {
        // `unverifiable`, not an error state: §4A.2 reserves `live` for recording and
        // this system has no error colour by design. Nothing was compared, which is
        // exactly what unverifiable means.
        verdict.dataset.state = "unverifiable";
    }
}

// Once at boot, and thereafter only when asked. Deliberately not on a timer: every
// call is an API Gateway request against a tenantless, unmetered endpoint, and a
// polling interval on a page someone leaves open would generate unbounded requests
// that no metering event accounts for (I12) and no spend cap sees (§10.5.9). Drift
// does not change while the page sits still; a deploy is what changes it, and a
// reload follows a deploy anyway.
try {
    const button = element("recheck") as HTMLButtonElement;
    button.addEventListener("click", () => {
        check(button).catch(reportBootFailure);
    });
    check(button).catch(reportBootFailure);

    // After `load`, never before: the worker's install step fetches the whole shell,
    // and doing that during boot would have it competing with first paint. On the
    // capture path that competition is the 2-second budget (§11A.5); establishing the
    // habit here costs nothing.
    window.addEventListener("load", registerServiceWorker);
} catch (error) {
    reportBootFailure(error);
    // Rethrown: the page now says what happened, and the console still carries the
    // stack for whoever is looking at it.
    throw error;
}
