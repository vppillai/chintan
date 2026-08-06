/**
 * The version-drift verdict (§0.6, §Phase 0 acceptance).
 *
 * §Phase 0 acceptance: "GET /v1/health returns the deployed API version and build
 * SHA, and **the frontend surfaces a deliberate mismatch rather than hiding it**."
 *
 * Kept as a pure function over two plain records, separate from any DOM, for one
 * reason: this is the only logic on this surface that can be wrong in a way nobody
 * notices. A rendering bug is visible; a comparison that quietly answers "match"
 * is the guesswork the endpoint exists to remove.
 */
import type { BuildInfo } from "./build";
import { stamped } from "./build";
import { clip } from "./health";
import type { ApiHealth, HealthResult } from "./health";

/**
 * Four outcomes, not two. "Cannot tell" is reported as its own state rather than
 * being folded into either match or mismatch: an unstamped build compared against
 * anything produces a meaningless answer, and reporting that answer confidently is
 * worse than reporting nothing (G-036).
 */
export type DriftState = "match" | "mismatch" | "unverifiable" | "unreachable";

export interface Verdict {
    readonly state: DriftState;
    /** One line, stated plainly. This is the sentence someone reads at a glance. */
    readonly headline: string;
    /** The specifics behind the headline. Every difference found, never just the first. */
    readonly reasons: readonly string[];
}

/**
 * Compares this bundle against the API's reported build.
 *
 * Precedence is deliberate: a CONCRETE difference outranks "cannot verify", which
 * outranks agreement. Ordering it the other way round would let an unstamped side
 * mask a real difference — the exact failure this check exists to catch.
 *
 * What makes a difference concrete is the subtle part, and getting it wrong is how
 * `unverifiable` became unreachable in the first version of this function. The two
 * dimensions do not behave the same way:
 *
 *   - **Instance** is always comparable. Both sides report a configured name and
 *     neither is a placeholder, so a disagreement is a real difference whatever the
 *     stamping.
 *   - **Version** is comparable only when BOTH sides are stamped. An unstamped
 *     build reports the literal "unstamped" (build-frontend.sh and version.go both
 *     do this deliberately, G-036), and "unstamped" differs from every real tag —
 *     so comparing the strings first reports a confident `mismatch` for a build
 *     that carries no version information at all. That is precisely the "reporting
 *     an answer confidently is worse than reporting nothing" failure the state
 *     exists to prevent, and it is the ordinary case for a fresh fork or a shallow
 *     clone.
 *
 * So the version strings are compared only behind the stamping test, and the
 * stamping caveats are appended to whatever verdict results — a real instance
 * mismatch still reports `mismatch`, and still says out loud that the versions were
 * not compared.
 */
export function compare(build: BuildInfo, result: HealthResult): Verdict {
    if (!result.ok) {
        return {
            state: "unreachable",
            headline: "The API did not answer, so drift cannot be checked.",
            reasons: [
                `Request to ${build.apiBaseUrl} failed: ${result.detail}.`,
                "A blocked cross-origin request looks identical to an unreachable API here; the API's allowed origin is set per instance in config (§10.6).",
            ],
        };
    }

    const api: ApiHealth = result.health;

    // Clipped here, not in health.ts, because the reason is the DOM and not the
    // parse: app.ts puts these strings on the page. Clipping before the comparison
    // would make two versions differing only past the limit compare equal and
    // report `match`, which is the wrong way round for a check whose whole job is
    // spotting a difference (§9.7 still applies — nothing untrusted reaches the DOM
    // unclipped, it just happens at the boundary that motivates it).
    const apiVersion = clip(api.version);
    const apiInstance = clip(api.instance);
    const apiCommit = clip(api.commit);

    const differences: string[] = [];
    const caveats: string[] = [];

    if (api.instance !== build.instance) {
        // Worse than a version mismatch and easier to miss: pointing a frontend at
        // another instance's API presents as data that is simply missing, not as an
        // error. Compared unconditionally — an instance name is never a placeholder,
        // so this dimension does not depend on either side being stamped.
        differences.push(`This app was built for instance ${build.instance}; the API reports ${apiInstance}.`);
    }

    const buildStamped = stamped(build);
    if (!buildStamped) {
        caveats.push("This app carries no version information: it was built without git tags or history (G-036).");
    }
    if (!api.stamped) {
        caveats.push("The API reports itself unstamped, so its version string is a placeholder rather than a release.");
    }
    const versionsComparable = buildStamped && api.stamped;

    if (versionsComparable && api.version !== build.version) {
        differences.push(`This app is ${build.version}; the API is ${apiVersion}.`);
    }

    if (differences.length > 0) {
        return {
            state: "mismatch",
            headline: "This app and the API are different builds.",
            reasons: [...differences, ...caveats],
        };
    }

    if (!versionsComparable) {
        return {
            state: "unverifiable",
            headline: "One side is an unstamped build, so the versions cannot be compared.",
            reasons: caveats,
        };
    }

    const annotations: string[] = [];
    if (api.commit !== build.commit) {
        // NOT a mismatch. The frontend and the API deploy from separate workflows
        // with separate path filters, so a frontend-only change legitimately
        // redeploys this app and not the API — same tag, different commit. Stated
        // anyway, because it is the difference that explains why two deploys of one
        // tag are not byte-identical (the reasoning behind G-035).
        annotations.push(`Same version, different commits: this app ${build.commit}, the API ${apiCommit}.`);
    }

    return {
        state: "match",
        headline: `This app and the API both report ${build.version}.`,
        reasons: annotations,
    };
}
