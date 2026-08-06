/**
 * Client for GET /v1/health (§6.6, §0.6).
 *
 * The endpoint exists for one job: the frontend and the backend deploy through
 * separate workflows and can drift, so the app fetches this, compares it against
 * its own build, and flags a mismatch. Without the endpoint that check cannot
 * exist and drift gets diagnosed by guesswork (§0.6).
 */

/**
 * The wire shape, snake_case exactly as backend/internal/handler/health.go
 * marshals it. Kept in the JSON's own casing rather than translated to camelCase:
 * this is a contract with another artifact, and a renaming layer is one more place
 * the two can silently disagree.
 */
export interface ApiHealth {
    readonly version: string;
    readonly commit: string;
    readonly build_time: string;
    readonly stamped: boolean;
    readonly config_version: number;
    readonly instance: string;
}

/** Success carries the payload; failure carries a reason worth displaying. */
export type HealthResult =
    | { readonly ok: true; readonly health: ApiHealth }
    | { readonly ok: false; readonly detail: string };

/**
 * A request that never returns is indistinguishable from a page that is broken.
 * Five seconds is long enough for a Lambda cold start (measured at 142ms init on
 * this stack, so the budget is dominated by network) and short enough that a
 * driver-facing surface never sits on "checking" indefinitely.
 */
const TIMEOUT_MS = 5000;

/**
 * Values echoed into the DOM are clipped. The API is ours, but §9.7 treats content
 * crossing a trust boundary as untrusted on principle, and the practical failure is
 * duller than an attack: a proxy error page or a truncated body renders as a wall of
 * text that destroys the layout on a 320px screen.
 */
const MAX_FIELD_CHARS = 64;

/** Path is versioned from the first commit (I15). Never construct an unversioned one. */
const HEALTH_PATH = "/v1/health";

/**
 * Clips one field for display.
 *
 * Exported and applied AT THE DOM, by app.ts and by the reason strings drift.ts
 * builds — deliberately not applied to the parsed record before it is compared.
 * Clipping first would make two version strings differing only past the limit
 * compare equal and report `match`: the sanitiser would be silently answering the
 * question the drift check exists to ask. Unreachable with git tags, but the
 * ordering is what makes it unreachable rather than luck.
 */
export function clip(value: string): string {
    return value.length > MAX_FIELD_CHARS ? `${value.slice(0, MAX_FIELD_CHARS)}…` : value;
}

/**
 * Narrows an untyped parse. A missing field must present as a failed check rather
 * than as `undefined` rendered into the page — which reads as "the API has no
 * version", a materially different and wrong conclusion.
 */
function isApiHealth(value: unknown): value is ApiHealth {
    if (typeof value !== "object" || value === null) {
        return false;
    }
    const v = value as Record<string, unknown>;
    return (
        typeof v.version === "string" &&
        typeof v.commit === "string" &&
        typeof v.build_time === "string" &&
        typeof v.stamped === "boolean" &&
        typeof v.config_version === "number" &&
        typeof v.instance === "string"
    );
}

/**
 * Fetches the API's build identity.
 *
 * No credentials, by design: the endpoint is unauthenticated and returns no user
 * data, which is what keeps it compatible with I10 — and sending credentials
 * cross-origin would require the API to relax CORS from the single configured
 * origin (§10.6) to support a request that needs none.
 *
 * `cache: "no-store"` because a cached health response defeats the only purpose
 * this endpoint has. The API sets no-store too; this is the client half of the
 * same decision, and the service worker additionally never intercepts a
 * cross-origin request.
 */
export async function fetchHealth(baseUrl: string): Promise<HealthResult> {
    const url = `${baseUrl.replace(/\/+$/, "")}${HEALTH_PATH}`;
    try {
        const response = await fetch(url, {
            method: "GET",
            cache: "no-store",
            credentials: "omit",
            redirect: "error",
            signal: AbortSignal.timeout(TIMEOUT_MS),
        });
        if (!response.ok) {
            return { ok: false, detail: `HTTP ${response.status}` };
        }
        const parsed: unknown = await response.json();
        if (!isApiHealth(parsed)) {
            return { ok: false, detail: "the response is not a health payload" };
        }
        // Returned as parsed. Clipping happens where the values reach the DOM —
        // see clip() above for why doing it here was the wrong order.
        return { ok: true, health: parsed };
    } catch (error) {
        // A CORS rejection, a DNS failure, and a timeout are indistinguishable
        // here by design of the fetch API — the browser reports all three as an
        // opaque TypeError. Say what is known rather than guessing, because a
        // CORS mismatch presents as an authentication failure and wastes a
        // disproportionate amount of time (§10.6).
        const detail = error instanceof Error ? error.name : "unknown error";
        return { ok: false, detail: clip(detail) };
    }
}
