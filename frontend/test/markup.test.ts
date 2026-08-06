/**
 * Structural assertions over index.html and the stylesheets.
 *
 * These are static checks, and that is a disclosed limitation rather than a choice:
 * containers/toolchain pins no headless browser, so scripts/checks/check-a11y.sh and
 * check-responsive.sh cannot run their real assertions yet and both gates are red on
 * that basis alone. A red gate does not catch a regression — it was already red — so
 * the rules that ARE checkable from source are asserted here as well, where they run
 * green today and turn red on a change.
 *
 * Every case below is a defect that shipped, not a hypothetical.
 */
import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

const HTML = readFileSync(new URL("../index.html", import.meta.url), "utf8");
const APP_CSS = readFileSync(new URL("../css/app.css", import.meta.url), "utf8");
const TOKENS_CSS = readFileSync(new URL("../css/tokens.css", import.meta.url), "utf8");
const SW_TS = readFileSync(new URL("../js/sw.ts", import.meta.url), "utf8");
const APP_TS = readFileSync(new URL("../js/app.ts", import.meta.url), "utf8");

const TAG = /<(\/?)([a-zA-Z][\w-]*)((?:"[^"]*"|'[^']*'|[^>"'])*?)(\/?)>/g;

interface Span {
    readonly start: number;
    readonly end: number;
}

/**
 * The span of the element whose opening tag matches `predicate`, from the start of its
 * opening tag to the end of its closing tag.
 *
 * A tag-matching scan rather than a DOM parse: there is no DOM in the toolchain, and
 * this markup is hand-written and well-formed. It tracks depth on the tag NAME, so a
 * nested element of the same name cannot end the span early.
 */
function elementSpan(html: string, predicate: (tag: string) => boolean): Span {
    TAG.lastIndex = 0;
    let match: RegExpExecArray | null;
    while ((match = TAG.exec(html)) !== null) {
        const [whole, closing, name, attrs, selfClosing] = match;
        if (closing === "/" || selfClosing === "/" || !predicate(attrs ?? "")) {
            continue;
        }
        const start = match.index;
        let depth = 1;
        let inner: RegExpExecArray | null;
        const scan = new RegExp(TAG.source, "g");
        scan.lastIndex = start + whole.length;
        while ((inner = scan.exec(html)) !== null) {
            if (inner[2] !== name || inner[4] === "/") {
                continue;
            }
            depth += inner[1] === "/" ? -1 : 1;
            if (depth === 0) {
                return { start, end: inner.index + inner[0].length };
            }
        }
        throw new Error(`unclosed <${name}> in index.html`);
    }
    throw new Error("no element in index.html matched the predicate");
}

function idsIn(html: string): readonly string[] {
    return [...html.matchAll(/\bid="([^"]+)"/g)].map((m) => m[1] as string);
}

/** CSS, HTML and line comments removed, so prose describing a rule is never read as one. */
function stripComments(source: string): string {
    return source
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .replace(/<!--[\s\S]*?-->/g, "")
        .replace(/^\s*\/\/.*$/gm, "");
}

test("every dynamic field of the verdict is inside its live region", () => {
    // The defect: the headline carried role="status" aria-live="polite" and the
    // reasons list sat outside it. A screen reader announced "This app and the API are
    // different builds." and never the specifics — which version, which instance,
    // which side is unstamped — and the user had to go hunting for a list they were
    // never told existed.
    //
    // Asserted generally, not on the two ids: EVERY id-bearing element in the verdict
    // section must be inside the live region, so a third dynamic field added outside
    // it fails here rather than being announced to nobody.
    const section = elementSpan(HTML, (attrs) => attrs.includes('id="verdict"'));
    const sectionHtml = HTML.slice(section.start, section.end);

    const live = elementSpan(sectionHtml, (attrs) => attrs.includes("aria-live"));
    const liveHtml = sectionHtml.slice(live.start, live.end);

    const sectionIds = idsIn(sectionHtml).filter((id) => id !== "verdict");
    expect(sectionIds.length).toBeGreaterThan(1);
    for (const id of sectionIds) {
        expect(idsIn(liveHtml)).toContain(id);
    }
});

test("the live region is polite, and announced as one statement", () => {
    const section = elementSpan(HTML, (attrs) => attrs.includes('id="verdict"'));
    const live = HTML.slice(section.start, section.end);
    expect(live).toContain('aria-live="polite"');
    // role="status" implies aria-atomic="true": the headline and the reasons are one
    // update, not a headline followed by list items arriving as interruptions.
    expect(live).toContain('role="status"');
});

test("no vh unit outside a comment, anywhere the app can name a length", () => {
    // vh does not account for the on-screen keyboard halving the viewport, so a
    // vh-based layout is correct in every desktop browser and wrong on the primary
    // device mid-typing (§4A.6). `[0-9]+vh` cannot match dvh/svh/lvh — the character
    // before "vh" is a letter — so the pattern needs no line-level exclusion, which is
    // what let `height: 100vh; min-height: 100dvh;` on one line slip past the check
    // script's `grep -v dvh`.
    for (const [name, source] of [
        ["app.css", APP_CSS],
        ["tokens.css", TOKENS_CSS],
        ["index.html", HTML],
    ] as const) {
        expect({ name, hits: stripComments(source).match(/[0-9]+(\.[0-9]+)?vh\b/g) ?? [] }).toEqual({ name, hits: [] });
    }
});

/**
 * The declaration block of one rule, by exact selector.
 *
 * Comments are stripped first, and not as a nicety: the comments in these files cite
 * CSS in prose (`body { display: flex }`), and a brace inside one ends the block scan
 * early — which made this helper silently report the last declaration missing.
 */
function rule(css: string, selector: string): string {
    const match = new RegExp(`(^|\\n)${selector.replace(".", "\\.")}\\s*\\{([^}]*)\\}`).exec(stripComments(css));
    if (match === null) {
        throw new Error(`no rule for selector ${selector}`);
    }
    return match[2] as string;
}

test("the footer is actually bottom-anchored, not just asked to be", () => {
    // `margin-top: auto` on the footer was inert: .shell was an auto-height flex
    // container and `body { min-height: 100dvh }` does not propagate to a child, so
    // there was no free space for the margin to consume and the footer sat directly
    // under the last section on a short page. The three declarations are ONE
    // mechanism; any of them alone does nothing, which is why they are asserted
    // together rather than as three independent facts.
    const body = rule(APP_CSS, "body");
    const shell = rule(APP_CSS, ".shell");
    const footer = rule(APP_CSS, ".footer");
    expect({
        bodyIsAColumnFlexContainer: /display:\s*flex/.test(body) && /flex-direction:\s*column/.test(body),
        shellFillsTheViewportHeight: /flex:\s*1/.test(shell),
        footerConsumesTheFreeSpace: /margin-top:\s*auto/.test(footer),
    }).toEqual({
        bodyIsAColumnFlexContainer: true,
        shellFillsTheViewportHeight: true,
        footerConsumesTheFreeSpace: true,
    });
});

test("the 'live' colour token means recording and nothing else (§4A.2)", () => {
    // A glance at a screen at 100km/h must answer "is it recording?" from colour
    // alone, so every other use of the hue erodes the one signal the driving case
    // depends on. There is no recording surface in Phase 0, which makes any reference
    // at all a decorative first use — the precedent the rule exists to prevent.
    expect(APP_CSS).not.toContain("--color-live");
    expect(APP_CSS).not.toContain("--palette-live");
    expect(HTML).not.toContain("--color-live");
    // In tokens.css the raw palette entry is mapped to the role token and referenced
    // nowhere else. Two occurrences: the definition and that one mapping.
    expect(TOKENS_CSS.match(/--palette-live/g)).toHaveLength(2);
});

test("index.html and the bundle agree about every element id", () => {
    // element() throws on a missing id, deliberately: a version page that silently
    // renders nothing is worse than one that errors, because the absence of a mismatch
    // warning reads as the absence of a mismatch. But the user-visible result of that
    // throw is a page frozen on "Checking the API version…", so app.ts now reports the
    // failure onto the page — and this test stops the disagreement happening at all,
    // which is better than either.
    //
    // Both directions: an id the bundle expects and the markup lacks breaks the page,
    // and an id the markup carries and nobody reads is a field that will never update.
    const referenced = new Set<string>([
        ...[...APP_TS.matchAll(/element\("([^"]+)"\)/g)].map((m) => m[1] as string),
        // The api-* fields are reached through a loop over an object literal, so they
        // appear only as keys. Every id in this markup is hyphenated or is an
        // element() argument, which is what makes this rule mechanical.
        ...[...APP_TS.matchAll(/"([a-z][a-z0-9]*(?:-[a-z0-9]+)+)"/g)].map((m) => m[1] as string),
        ...[...APP_TS.matchAll(/getElementById\("([^"]+)"\)/g)].map((m) => m[1] as string),
    ]);
    const present = new Set(idsIn(HTML));

    expect([...referenced].filter((id) => !present.has(id))).toEqual([]);
    expect([...present].filter((id) => !referenced.has(id))).toEqual([]);
});

test("the worker qualifies every cache lookup with its own cache name", () => {
    // A bare caches.match searches EVERY cache in the origin, so a sibling app on the
    // same GitHub Pages user site that had cached an identical URL would have its
    // response served as this app's asset.
    const lookups = SW_TS.match(/caches\.match\([^)]*\)/g) ?? [];
    expect(lookups.length).toBeGreaterThan(0);
    for (const lookup of lookups) {
        expect(lookup).toContain("cacheName");
    }
});
