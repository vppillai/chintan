# F-0001: Does each §0.5A check actually fail when its subject is broken?
Date: 2026-08-04   Phase: 0   Status: **partial** — 11 of 21 checks demonstrated red

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

### Demonstrated red — deliberate break, 2026-08-04, after the templates landed

Each break applied to the real template, check run, break reverted, template
confirmed byte-identical afterwards (`git diff --stat` empty).

| Check | Break applied | Detected? |
|---|---|---|
| log retention | `RetentionInDays` removed from `ApiLogGroup` | yes |
| no Lambda in VPC | `VpcConfig` added to `ApiFunction` | yes |
| no alarms or SNS | an `AWS::CloudWatch::Alarm` added | yes |
| resource prefix | `RoleName` changed to `api-role-${InstanceName}` | yes |
| brand strings | `<h1>Chintan</h1>` in `frontend/index.html` | yes |
| **tenant-key helper (I11)** | a handler building `"TENANT#" + id` by hand | **NO — see below** |

Before running these, one thing was checked rather than assumed: that `yq` can
actually parse a CloudFormation template. Its short intrinsic tags (`!Sub`, `!Ref`,
`!GetAtt`) are not valid YAML tags to a strict parser, and a parser that silently
returned nothing would make all four template checks pass vacuously while appearing
to work. `yq` tolerates them and finds both functions and both log groups.

### The tenant-key check was vacuous

The most important check in Phase 0 — the one §Phase 0 acceptance names explicitly
("a static check fails the build if any DynamoDB or S3 key is constructed outside
the tenant-scoped helper") — **did not detect a deliberate violation.**

`tracked_files()` selected files with a bare `git ls-files`, which lists the index:
committed files only. The violating file was new, therefore untracked, therefore
invisible. So the check inspected everything except the work in progress, and
reported green on exactly the kind of change it exists to reject.

Two details make this worse than a simple bug:

1. **CI would have caught it, local would not.** A checkout tracks every file, so
   the remote signal was correct while the local one was wrong — the most confusing
   possible split, and the one most likely to be resolved by distrusting CI.
2. **Editing a tracked file cannot expose it.** Had the red demonstration been done
   by modifying an existing handler rather than adding a file, it would have passed
   and the gap would have survived, documented as verified.

Fixed to `git ls-files --cached --others --exclude-standard`. Re-demonstrated: the
same violation is now reported on both literals with the file and line. Recorded as
**G-063**.

### Not yet demonstrated red — no subject exists

Per §0.5A these are present, pass trivially, and each tests for its subject rather
than being hardcoded to succeed. Every one must be demonstrated red in the phase
that gives it a subject, and this table is the outstanding list.

| Check | Active | Demonstrate red by |
|---|---|---|
| typecheck (frontend) | 1 | a type error in `frontend/js` |
| accessibility | 1 | a sub-AA contrast pair in either theme |
| responsive | 1 | a `vh` unit, or content that scrolls the page at 320px |
| corpus integrity | 2 | mutating an L0 object so its hash diverges from the manifest |
| golden WER | 2 | regressing WER against the recorded baseline |
| extraction fixtures | 3 | changing an expected item kind |
| prompt integrity | 3 | shortening a `prompt` body |
| trigger additivity | 8 | touching `RecorderController` while adding an adapter |

## Consequence for the build

1. **Two checks were wrong in ways only a red run revealed.** The dependency scan
   would have reported a broken scanner as a security finding; the brand check would
   have been disabled or ignored within a week at 21 false positives per run. Both
   are the §0.5A argument in miniature — not that checks are absent, but that an
   unexercised check is believed.

2. **A self-test can pass without testing anything.** The fix generalises: any check
   asserting "X fails when broken" must also assert "X passes when not broken",
   or a universal failure reads as successful detection. Recorded as **G-062**.

3. **Two of the three most important checks were broken, and both were found only by
   deliberately breaking their subject.** The guardrails self-test and the I11
   tenant-key check both reported green while inspecting nothing relevant. Neither
   would have been discovered by reading the code, and neither showed any symptom.
   §0.5A's rule — "every check is demonstrated red before it is trusted" — is not a
   process nicety; it is the only thing that surfaced either of these. Recorded as
   **G-063**.

4. **How a check is broken determines what the demonstration proves.** G-063 was
   invisible to an edit of a tracked file and visible only to a *new* file. So the
   break has to resemble the real failure: for a check over a file set, add a file;
   for a check over content, edit content. A red demonstration that took the easier
   route would have certified the gap as verified.

5. **`make check` is green with 21 of 21 inventory rows wired.** Fourteen active on
   real subjects, seven dormant. Eleven have now been demonstrated red; the
   remaining ten are listed above against the phase that gives each a subject.

6. No spec assumption is affected. This finding concerns the pipeline, not the
   product design.
