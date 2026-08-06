/**
 * Build-time facts about this bundle.
 *
 * Every value is a placeholder that scripts/build-frontend.sh substitutes from
 * `git describe` and the instance config. Three rules from §0.6 are load-bearing
 * here, and backend/internal/version/version.go implements the identical split on
 * the Go side — the two must agree, because the whole purpose of this page is
 * comparing them:
 *
 *   - No checked-in VERSION file (G-037). `passbook` carried one, it had to be
 *     hand-synced with tags, and it drifted — still reading v2.6.0 at the v2.7.0
 *     release. A tag cannot drift from itself, so the value is injected at build
 *     time from the tag.
 *   - The DISPLAYED version is the bare tag. `version` below is what the user
 *     sees, and it must stay clean.
 *   - **The service-worker cache token — `{tag}-{short-sha}` — is deliberately
 *     absent from this module.** It lives only in sw.ts, and only ever reaches
 *     sw.js. Adding it here would put it one careless template expression away
 *     from the version footer, and G-035's fix depends on the two being separate
 *     concerns: cache identity must track CONTENT (every deploy), display must
 *     track the TAG. build-frontend.sh asserts that the token appears in sw.js
 *     and in nothing else.
 *
 * An unstamped build reports "unstamped"/"unknown" rather than a plausible-looking
 * 0.0.0, so a build that lost its git history is obvious instead of quietly wrong
 * (G-036: `git describe` is resolved during the build, so tagging afterwards does
 * not reach the artifact — tag before deploying).
 */
export interface BuildInfo {
    /** The bare git tag. What a human is shown (§0.6). */
    readonly version: string;
    /** Short SHA. Needed because a tag alone cannot distinguish two deploys. */
    readonly commit: string;
    /** RFC3339 UTC, supplied by the build so nothing here reads a clock. */
    readonly buildTime: string;
    /**
     * Which instance this bundle was built against. Compared with the API's
     * reported instance: a frontend talking to the wrong instance's API presents
     * as data that is simply missing, which is the most expensive deployment
     * failure to diagnose (see the `instance` field of the health response, and
     * the instance branch of drift.ts's compare).
     */
    readonly instance: string;
    /**
     * Base URL of the API, resolved at build time — never a literal in source
     * (I5, §7). It differs per instance, and the value the frontend was built
     * with is exactly what a drift check needs to state out loud.
     */
    readonly apiBaseUrl: string;
}

export const BUILD: BuildInfo = {
    version: "{{VERSION}}",
    commit: "{{COMMIT}}",
    buildTime: "{{BUILD_TIME}}",
    instance: "{{INSTANCE}}",
    apiBaseUrl: "{{API_BASE_URL}}",
};

/** What version.Stamped() reports on the Go side, applied to this bundle. */
export function stamped(build: BuildInfo): boolean {
    return build.version !== "unstamped" && build.commit !== "unknown";
}
