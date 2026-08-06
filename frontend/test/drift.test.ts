/**
 * The drift verdict (§0.6, §Phase 0 acceptance).
 *
 * Table-driven, and every case asserts the REASON as well as the state — a verdict
 * whose state is right and whose sentence is wrong is still a wrong answer on the one
 * surface whose entire job is telling someone what differs.
 *
 * The defect these were written for: `unverifiable` was unreachable unless BOTH sides
 * were unstamped. Because an unstamped build reports the literal "unstamped" and that
 * string differs from every real tag, a single-sided unstamped build took the version
 * branch and was reported as a confident `mismatch` — which drift.ts's own docstring
 * calls worse than reporting nothing (G-036). The ordinary way to reach it is a fresh
 * fork or a shallow clone, which build-frontend.sh tolerates by design.
 */
import { expect, test } from "bun:test";
import type { BuildInfo } from "../js/build";
import { compare } from "../js/drift";
import type { DriftState, Verdict } from "../js/drift";
import type { ApiHealth, HealthResult } from "../js/health";

function build(over: Partial<BuildInfo> = {}): BuildInfo {
    return {
        version: "v0.2.0",
        commit: "abc1234",
        buildTime: "2026-08-05T00:00:00Z",
        instance: "dev",
        apiBaseUrl: "https://api.example.invalid",
        ...over,
    };
}

function health(over: Partial<ApiHealth> = {}): HealthResult {
    return {
        ok: true,
        health: {
            version: "v0.2.0",
            commit: "abc1234",
            build_time: "2026-08-05T00:00:00Z",
            stamped: true,
            config_version: 1,
            instance: "dev",
            ...over,
        },
    };
}

interface Case {
    readonly name: string;
    readonly verdict: Verdict;
    readonly state: DriftState;
    /** Substrings every one of which must appear somewhere in the reasons. */
    readonly says: readonly string[];
    /** Substrings none of which may appear. */
    readonly silentOn?: readonly string[];
    readonly reasonCount?: number;
}

const cases: readonly Case[] = [
    {
        name: "identical builds agree, and say nothing further",
        verdict: compare(build(), health()),
        state: "match",
        says: [],
        reasonCount: 0,
    },
    {
        name: "same tag, different commit is a match with the commit stated",
        verdict: compare(build(), health({ commit: "def5678" })),
        state: "match",
        says: ["abc1234", "def5678"],
        reasonCount: 1,
    },
    {
        name: "two stamped builds with different tags are a mismatch",
        verdict: compare(build(), health({ version: "v0.1.0" })),
        state: "mismatch",
        says: ["v0.2.0", "v0.1.0"],
    },
    {
        name: "a different instance is a mismatch even though the versions agree",
        verdict: compare(build(), health({ instance: "prod" })),
        state: "mismatch",
        says: ["dev", "prod"],
    },
    {
        // THE REGRESSION. A frontend built from a clone with no tags, pointed at the
        // tagged dev API. Nothing was compared, so nothing may be asserted.
        name: "an unstamped frontend against a stamped API is unverifiable, not a mismatch",
        verdict: compare(build({ version: "unstamped", commit: "unknown" }), health()),
        state: "unverifiable",
        says: ["no version information"],
        silentOn: ["different builds"],
    },
    {
        name: "an unstamped API against a stamped frontend is unverifiable",
        verdict: compare(build(), health({ version: "unstamped", stamped: false })),
        state: "unverifiable",
        says: ["placeholder"],
        silentOn: ["different builds"],
    },
    {
        name: "both sides unstamped states both facts",
        verdict: compare(build({ version: "unstamped", commit: "unknown" }), health({ version: "unstamped", stamped: false })),
        state: "unverifiable",
        says: ["no version information", "placeholder"],
        reasonCount: 2,
    },
    {
        // A concrete difference outranks "cannot verify" — but the caveat is still
        // stated, because the reader must not conclude the versions were compared.
        name: "an instance mismatch on an unstamped build is still a mismatch, with the caveat",
        verdict: compare(build({ version: "unstamped", commit: "unknown" }), health({ instance: "prod" })),
        state: "mismatch",
        says: ["prod", "no version information"],
    },
    {
        name: "an unreachable API is its own state and names the endpoint",
        verdict: compare(build(), { ok: false, detail: "TimeoutError" }),
        state: "unreachable",
        says: ["https://api.example.invalid", "TimeoutError"],
    },
];

for (const c of cases) {
    test(c.name, () => {
        expect(c.verdict.state).toBe(c.state);
        const text = [c.verdict.headline, ...c.verdict.reasons].join("\n");
        for (const fragment of c.says) {
            expect(text).toContain(fragment);
        }
        for (const fragment of c.silentOn ?? []) {
            expect(text).not.toContain(fragment);
        }
        if (c.reasonCount !== undefined) {
            expect(c.verdict.reasons).toHaveLength(c.reasonCount);
        }
        // A headline is one sentence someone reads at a glance, in every state.
        expect(c.verdict.headline.length).toBeGreaterThan(0);
    });
}

test("versions differing only past the display limit are a mismatch, not a match", () => {
    // The sanitiser used to clip every field to 64 characters BEFORE the comparison
    // ran, so two versions agreeing on their first 64 characters compared equal and
    // reported `match`: the display concern was silently answering the question this
    // module exists to ask. Clipping now happens where the value reaches the DOM.
    const long = "v0.2.0-".padEnd(70, "x");
    const verdict = compare(build({ version: `${long}A` }), health({ version: `${long}B` }));
    expect(verdict.state).toBe("mismatch");
});

test("a reason string stays bounded even when the API returns a wall of text", () => {
    // The other half of moving the clip: reasons are rendered, so an unbounded API
    // value must not reach the page through them (§9.7, §4A.6 at 320px).
    const verdict = compare(build(), health({ version: "v".repeat(5000) }));
    expect(verdict.state).toBe("mismatch");
    for (const reason of verdict.reasons) {
        expect(reason.length).toBeLessThan(400);
    }
});
