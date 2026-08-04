# Chintan toolchain image — the only environment this project is built, tested,
# linted, or deployed in.
#
# §0.5A: "If a check is not in CI it is not a check — it is a habit, and habits
# decay." A check in CI is only trustworthy if the environment it runs in is the
# one the author saw, so there is exactly one image and dev, CI, and the deploy
# job all run inside it. `make` re-executes itself in here automatically, which
# means the local command and the CI command are the same command.
#
# Every tool is pinned by version *and* sha256, for both architectures: an
# arm64 workstation and an amd64 GitHub runner resolve the same base index
# digest and verify the same hashes. Versions live in versions.env, not here,
# so bumping one is a one-line reviewable diff.
#
# Deliberately absent:
#   - A headless browser. The accessibility and contrast checks (§0.5A) have no
#     subject until Phase 1 builds a capture face; adding ~400MB of Chromium to
#     every CI job before then is cost without coverage. Phase 1 adds it here.
#   - The ONNX Runtime C libraries. Phase 2 links them into the Go worker and
#     will add the cross-compile toolchain to this image — noted now because it
#     is the one place the Go decision has a non-obvious cost (§4), and this
#     file is where that cost lands.
#   - Python. The config schema validator is a Go subcommand (`chintanctl
#     config validate`), which is the same code the Lambda runs at cold start —
#     one implementation, per §11.2, rather than a second validator in CI that
#     can disagree with the first.

# hadolint ignore=DL3006
ARG BASE_IMAGE=golang@sha256:d41da62c5fa95d88bac8ea732451c00f76aefc5ba4931b694879f4ae15676db8
FROM ${BASE_IMAGE}

ARG AWSCLI_VERSION
ARG AWSCLI_SHA256_arm64
ARG AWSCLI_SHA256_amd64
ARG YQ_VERSION
ARG YQ_SHA256_arm64
ARG YQ_SHA256_amd64
ARG SHELLCHECK_VERSION
ARG SHELLCHECK_SHA256_arm64
ARG SHELLCHECK_SHA256_amd64
ARG SHFMT_VERSION
ARG SHFMT_SHA256_arm64
ARG SHFMT_SHA256_amd64
ARG BUN_VERSION
ARG BUN_SHA256_arm64
ARG BUN_SHA256_amd64

SHELL ["/bin/bash", "-euo", "pipefail", "-c"]

# Distro packages: only what the pinned installers below need, plus zip (the
# Lambda artifact) and jq (script JSON handling, --json output everywhere).
# --no-install-recommends keeps this from pulling a desktop's worth of deps.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        jq \
        unzip \
        xz-utils \
        zip \
    && rm -rf /var/lib/apt/lists/*

# One helper for every pinned download: resolve the arch-specific URL and hash,
# fetch, and verify before the bytes are allowed to become an executable. A
# version bumped without its checksum fails here rather than silently
# installing something nobody reviewed.
COPY install-pinned.sh /tmp/install-pinned.sh
RUN chmod +x /tmp/install-pinned.sh

RUN /tmp/install-pinned.sh yq "${YQ_VERSION}" "${YQ_SHA256_arm64}" "${YQ_SHA256_amd64}"
RUN /tmp/install-pinned.sh shfmt "${SHFMT_VERSION}" "${SHFMT_SHA256_arm64}" "${SHFMT_SHA256_amd64}"
RUN /tmp/install-pinned.sh shellcheck "${SHELLCHECK_VERSION}" "${SHELLCHECK_SHA256_arm64}" "${SHELLCHECK_SHA256_amd64}"
RUN /tmp/install-pinned.sh bun "${BUN_VERSION}" "${BUN_SHA256_arm64}" "${BUN_SHA256_amd64}"
RUN /tmp/install-pinned.sh awscli "${AWSCLI_VERSION}" "${AWSCLI_SHA256_arm64}" "${AWSCLI_SHA256_amd64}" \
    && rm -f /tmp/install-pinned.sh

# Go build and module caches live on a mounted volume in dev (see scripts/dev.sh)
# and in the Actions cache in CI, so they are writable by whatever uid runs.
# A UTF-8 locale is not optional here: this project's scripts and comments cite
# spec sections with § throughout, and a tool writing them to a non-UTF-8 stdout
# fails with an encoding error rather than a useful message (shellcheck does
# exactly this). Set it in the image so no caller has to remember.
ENV LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    GOFLAGS=-buildvcs=false \
    GOPATH=/go \
    GOCACHE=/go/cache \
    GOTOOLCHAIN=local \
    BUN_INSTALL=/usr/local \
    PATH=/usr/local/bin:/go/bin:/usr/local/go/bin:$PATH

# The image runs as whatever uid the caller has, so that files written into the
# bind-mounted worktree are owned by the developer rather than by root. That
# means HOME may not exist and the Go caches must be world-writable.
RUN mkdir -p /go/cache /go/pkg /workspace \
    && chmod -R 0777 /go \
    && git config --system --add safe.directory /workspace

WORKDIR /workspace

# Fail the build if any tool is missing or unrunnable, so a broken image is
# caught at build time rather than by a confusing failure in a CI job.
RUN set -x \
    && go version \
    && aws --version \
    && yq --version \
    && shellcheck --version \
    && shfmt --version \
    && bun --version \
    && jq --version \
    && zip -v > /dev/null

LABEL org.opencontainers.image.title="chintan-toolchain" \
      org.opencontainers.image.description="Build, test, and deploy environment for Chintan. Identical in dev, CI, and deploy." \
      org.opencontainers.image.source="https://github.com/vppillai/chintan"

CMD ["/bin/bash"]
