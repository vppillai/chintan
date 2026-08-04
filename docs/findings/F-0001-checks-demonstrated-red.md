# F-0001: Does each §0.5A check actually fail when its subject is broken?
Date: 2026-08-04   Phase: 0   Status: **partial**

## Question

§0.5A requires that "every check is demonstrated red before it is trusted. Break the
thing deliberately, watch CI fail, revert. An untested check is worse than no check,
because it is believed." §Phase 0 acceptance restates it as a gate: "Every check has
been demonstrated red, with the demonstration recorded. **A check never observed
failing is not counted as present.**"

Pass criterion, per check: with its subject deliberately broken, the check exits
non-zero and its message names the actual problem. A check that fails for an
unrelated reason does not count as demonstrated — that is a check that always
fails, which proves nothing about detection.

## Method

Two sources of evidence, and the distinction matters:

- **Observed during construction.** Several checks went red on their own while the
  code was being written, which is stronger evidence than a staged break: the
  failure was not arranged to succeed.
- **Deliberate break.** The remainder require a subject to exist before it can be
  broken, and will be demonstrated in the phase that creates it.

All runs were inside the toolchain image (ADR 0005), so these are the same
executions CI performs.

## Result

### Demonstrated red — observed during construction

| Check | How it went red | Was the message correct? |
|---|---|---|
| **config schema** | 30+ negative unit tests, each mutating the real `dev.yaml`: a deleted required threshold, a task naming an absent model, a typo'd key, inverted VAD hysteresis, a prompt confidence at or below the general bar, a wildcard origin, `telegram` in `triggers.enabled`, an inline secret, a positive `min_avg_logprob`. Each asserts the *reason*, not merely that it failed. | Yes — each test asserts on the substring naming the constraint |
| **format** | shfmt rejected `containers/toolchain/*.sh` and `refresh-checksums.sh` on the first full run | Yes — printed the exact diff |
| **lint** | shellcheck found SC2164 (unguarded `cd`), SC2126, SC2155, SC2015, and SC1091 across nine scripts | Yes |
| **dependency scan** | `govulncheck` exited non-zero from the repository root ("no go.mod file") | **No — and this was itself the finding.** The message read "govulncheck reported reachable vulnerabilities", conflating a scanner that could not run with a scanner that found something. Fixed to distinguish exit 3 (vulnerabilities) from any other non-zero (could not complete), because a scan that never ran must not be reported as clean. |
| **brand strings** | Reported 21 violations on its first run, all false positives — comments, the Go module path, the toolchain image name | **No.** The check was broader than §7.3's rule, which is about *user-visible* strings. Narrowed to string literals in source plus frontend markup, with comments and module paths stripped. The limitation that remains (line-oriented, so a multi-line raw string could hide one) is stated in the script rather than left implicit. |
| **admin scripts** | Harness found 4 failures: a missing `--help` on `build-lambda.sh`, `yq` emitting `---` document separators as `secret_ref` values across multiple files, and duplicate file listings from the `tracked_files` fallback | Yes |
| **guardrails self-test** | See below — the most useful failure of the session | Yes, after being fixed |

### The `guardrails-check.sh --self-test` result

This one is worth recording in full, because it is an instance of exactly the failure
§0.5A describes.

The self-test constructs a tree with `CODEOWNERS` removed and asserts the script
fails. It reported **pass** — while never actually exercising its premise. Two bugs
combined:

1. `common.sh` recomputed `REPO_ROOT` from `BASH_SOURCE` unconditionally, so the
   self-test's attempt to point the script at a doctored tree was silently ignored.
   The inner run was inspecting the *real* repository.
2. An unrelated bug (the `yq` separator issue above) made every invocation fail. So
   the self-test's "the script failed, therefore it detects the removal" inference
   held for entirely the wrong reason.

Fixing bug 2 exposed bug 1: the self-test flipped to failing, revealing it had never
tested anything.

Both are fixed. `CHINTAN_REPO_ROOT` is now an explicit, documented override, and the
self-test additionally **verifies the control case** — that the script passes on the
undoctored tree — before concluding anything from a failure on the doctored one. A
check that fails everywhere is not evidence of detection.

### Not yet demonstrated red — no subject exists

Per §0.5A these are present, pass trivially, and each tests for its subject rather
than being hardcoded to succeed. Every one must be demonstrated red in the phase
that gives it a subject, and this table is the outstanding list.

| Check | Active | Demonstrate red by |
|---|---|---|
| log retention | 0 | removing `RetentionInDays` from a log group in `template.yaml` |
| no Lambda in VPC | 0 | adding a `VpcConfig` to a function |
| no alarms or SNS | 0 | adding an `AWS::CloudWatch::Alarm` |
| resource prefix | 0 | renaming a role without the `voicenotes-` prefix |
| tenant-key helper | 0 | writing `"TENANT#" + id` in a handler |
| typecheck (frontend) | 1 | a type error in `frontend/js` |
| accessibility | 1 | a sub-AA contrast pair in either theme |
| responsive | 1 | a `vh` unit, or content that scrolls the page at 320px |
| corpus integrity | 2 | mutating an L0 object so its hash diverges from the manifest |
| golden WER | 2 | regressing WER against the recorded baseline |
| extraction fixtures | 3 | changing an expected item kind |
| prompt integrity | 3 | shortening a `prompt` body |
| trigger additivity | 8 | touching `RecorderController` while adding an adapter |

The infrastructure checks (rows 1–4) become demonstrable as soon as
`infrastructure/template.yaml` exists, which is the next slice.

## Consequence for the build

1. **Two checks were wrong in ways only a red run revealed.** The dependency scan
   would have reported a broken scanner as a security finding; the brand check would
   have been disabled or ignored within a week at 21 false positives per run. Both
   are the §0.5A argument in miniature — not that checks are absent, but that an
   unexercised check is believed.

2. **A self-test can pass without testing anything.** The fix generalises: any check
   asserting "X fails when broken" must also assert "X passes when not broken",
   or a universal failure reads as successful detection. Recorded as **G-062** in
   `docs/gotchas.md`.

3. **`make check` is green with 21 of 21 inventory rows wired.** Fourteen are active
   and passing on real subjects; seven are dormant, each testing for its subject.

4. No spec assumption is affected. This finding concerns the pipeline, not the
   product design.
