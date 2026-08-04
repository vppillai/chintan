# ADR 0005: One container image for dev, CI, and deploy

Date: 2026-08-04
Status: accepted

## Context

§0.5A makes a claim the project has to be able to keep:

> Local-only verification does not exist. If a check is not in CI it is not a
> check — it is a habit, and habits decay (G-041). "It passes on my machine" is not
> a claim this project accepts, from a human or an agent.

Forbidding the claim is not the same as making it checkable. A check that runs
under Go 1.25 locally and Go 1.24 in CI, or against one shellcheck version here and
another there, can pass in one place and fail in the other — and when it does, the
argument is about environments rather than about the code. That is the same class
of problem as G-041: not a rule being broken, a rule quietly ceasing to mean
anything.

Phase 2 also puts a specific, concrete cost on environment drift. The server-side
VAD links the ONNX Runtime **C API** into the Go worker (§4), which means a
cross-compile toolchain and a shared library shipped in the artifact. §4 calls this
"the one place the Go decision has a non-obvious cost", and the §Phase 2 gate has
to measure artifact size, cold start, and peak memory against the real Lambda
limits. Those measurements are meaningless if the thing being measured was built
against whatever happened to be installed.

## Decision

**Every build, test, lint, and deploy runs inside one image**,
`containers/toolchain`, and `make` is the only interface to it.

1. **`make <target>` re-executes itself inside the container** via
   `scripts/dev.sh`, unless `CHINTAN_IN_CONTAINER=1` — which CI sets because the
   job already runs in the image. So the local command and the CI command are
   literally the same command, and `make check` is what CI runs.

2. **The image tag is a hash of `containers/toolchain/`.** A developer and a CI job
   that agree on the tag are provably running the same tools. A toolchain change
   produces a new tag, so a stale image cannot be silently reused after a version
   bump, and every check re-runs against the new tools in the same change that
   bumped them.

3. **Every tool is pinned by version and sha256, for both architectures**, in
   `versions.env`. The base image is pinned by its multi-arch *index* digest, so
   the identical reference resolves on an arm64 workstation and an amd64 runner. A
   version bumped without refreshing its checksum fails the image build rather than
   installing an unverified binary.

4. **CI builds the image once and publishes it to GHCR**, keyed on that tag. Every
   subsequent job runs `container: <that reference>`. An unchanged toolchain is
   built once, ever.

## Consequences

Accepted:

- **A container runtime is now required to work on this project.** `make` fails
  with an explanatory message rather than silently falling back to host tools,
  because a silent fallback reintroduces exactly the drift this removes.
- **First run pays an image build** (~2 minutes). Subsequent runs pull or reuse.
- **The image is a CODEOWNERS-protected path.** It is not in §9.6's list; it was
  added for the same reason the workflow directory is there (G-048). A change to
  the image changes what every check executes, so an unreviewed toolchain change
  can neutralise every gate at once while the pipeline still reports green. That
  is a larger blast radius than most code changes, not a smaller one.

Gained:

- The Phase 2 ONNX cross-compile toolchain has one place to live, and its
  measurements will be reproducible.
- Supply-chain pinning is a side effect rather than a separate initiative: the
  dependency-scan gate (§0.5A) now has a fixed toolchain to scan, and
  `refresh-checksums.sh --check` detects an upstream artifact re-cut under an
  unchanged version tag, which is a supply-chain event rather than a build failure.
- The reproducible artifact requirement in §Phase 0 becomes straightforward. With
  a pinned compiler, `-trimpath`, and `CGO_ENABLED=0`, the same commit produces
  the same bytes, so the zip is a function of the source rather than of the
  machine.

Rejected alternatives:

- **`setup-go` and friends in CI, host tools locally.** This is what passbook does
  and it is adequate there. It reproduces the drift in the one project whose spec
  forbids the claim that drift makes possible, and it gives the Phase 2 native
  build nowhere to live.
- **Nix.** Stronger reproducibility guarantees; a substantially larger thing to
  learn and to keep working, for a single-maintainer project. A pinned Debian base
  with sha256-verified tools reaches the level this project needs.
- **A devcontainer only.** Solves the editor, not CI. The point is that both use
  the same image, and a devcontainer that CI does not use is a fourth environment
  rather than one fewer.
