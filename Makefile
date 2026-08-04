# Chintan — the single task interface for dev, CI, and deploy.
#
# Every target here runs inside the toolchain container (containers/toolchain),
# re-executing itself through scripts/dev.sh. That is what makes the local
# command and the CI command the same command: CI runs `make check`, and so do
# you. §0.5A is explicit that a check outside CI is a habit rather than a check,
# and a check that runs against different tools in each place is worse — it is a
# habit that reports a number.
#
# `make check` is the complete §0.5A inventory. Every check in that table exists
# from Phase 0, including the ones whose subject arrives later: a check with
# nothing to inspect passes trivially and is never skipped or commented out, so
# the day the subject appears the check is already running. Each target below
# names the phase where it becomes active.
#
# Usage:
#   make help              list targets
#   make check             every gate CI runs, in the same image CI uses
#   make test              unit tests
#   make shell             an interactive shell in the toolchain image
#
# Escape hatch: CHINTAN_IN_CONTAINER=1 runs recipes against host tools. CI sets
# it because the job already runs inside the image. Setting it by hand means
# giving up the guarantee this file exists to provide.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := help

BACKEND_DIR := backend
FRONTEND_DIR := frontend
INSTANCE ?= dev

# Targets that run inside the container. Anything not listed here runs on the
# host, which is correct only for the handful of targets that drive the
# container itself.
CONTAINERISED := \
	build \
	build-frontend \
	build-lambda \
	check \
	check-a11y \
	check-admin-scripts \
	check-brand-strings \
	check-config \
	check-deps \
	check-extraction-fixtures \
	check-format \
	check-guardrails \
	check-lint \
	check-log-retention \
	check-no-alarms \
	check-no-vpc \
	check-prompt-integrity \
	check-resource-prefix \
	check-responsive \
	check-tenant-keys \
	check-trigger-additivity \
	check-typecheck \
	check-verify-corpus \
	check-wer \
	doctor \
	fmt \
	test \
	test-backend \
	test-scripts \
	tidy

ifneq ($(CHINTAN_IN_CONTAINER),1)

# Outside the container: forward each target into it. One rule per target rather
# than a catch-all `%`, so a mistyped target is an error instead of a silent
# container run that does nothing.
.PHONY: $(CONTAINERISED)
$(CONTAINERISED):
	@./scripts/dev.sh make CHINTAN_IN_CONTAINER=1 INSTANCE=$(INSTANCE) $@

else

# ---------------------------------------------------------------------------
# Inside the container: the real recipes.
# ---------------------------------------------------------------------------

.PHONY: check
check: check-lint check-format check-typecheck test check-config check-tenant-keys \
       check-log-retention check-no-vpc check-no-alarms check-resource-prefix \
       check-guardrails check-deps check-brand-strings check-admin-scripts \
       check-a11y check-responsive check-verify-corpus check-wer \
       check-extraction-fixtures check-prompt-integrity check-trigger-additivity
	@echo ""
	@echo "All §0.5A checks passed."

# --- Lint, format, typecheck (active: Phase 0) -----------------------------

.PHONY: check-lint
check-lint:
	@echo "==> go vet"
	@cd $(BACKEND_DIR) && go vet ./...
	@echo "==> shellcheck"
	@scripts/checks/lint-shell.sh

.PHONY: check-format
check-format:
	@echo "==> gofmt"
	@cd $(BACKEND_DIR) && unformatted=$$(gofmt -l .); \
		if [ -n "$$unformatted" ]; then \
			echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
		fi
	@echo "==> shfmt"
	@shfmt --diff --indent 4 --case-indent $$(scripts/checks/list-shell.sh)

.PHONY: check-typecheck
check-typecheck:
	@echo "==> go build (typecheck)"
	@cd $(BACKEND_DIR) && go build ./...
	@echo "==> frontend typecheck"
	@scripts/checks/typecheck-frontend.sh

.PHONY: fmt
fmt:
	@cd $(BACKEND_DIR) && gofmt -w .
	@shfmt --write --indent 4 --case-indent $$(scripts/checks/list-shell.sh)

.PHONY: tidy
tidy:
	@cd $(BACKEND_DIR) && go mod tidy

# --- Unit tests (active: Phase 0) -----------------------------------------

.PHONY: test
test: test-backend test-scripts

.PHONY: test-backend
test-backend:
	@echo "==> go test"
	@cd $(BACKEND_DIR) && go test -race ./...

# Admin-script tests, both --dry-run and --apply, against the fake-AWS harness
# (§11.5). Listed separately from test-backend because §0.5A treats it as its
# own gate: untested code mutating production data is the failure it prevents.
.PHONY: test-scripts check-admin-scripts
test-scripts check-admin-scripts:
	@echo "==> admin script tests (fake-AWS harness)"
	@scripts/test/harness.sh

# --- Config schema validation (active: Phase 0) ---------------------------
# Runs the same validator the Lambda runs at cold start, so CI and runtime
# cannot disagree about what a valid config is (§7.4, §11.2).

.PHONY: check-config
check-config:
	@echo "==> config schema validation"
	@cd $(BACKEND_DIR) && go run ./cmd/chintanctl config validate ../config/instances

# --- Static invariant checks (active: Phase 0) ----------------------------

.PHONY: check-tenant-keys
check-tenant-keys:
	@echo "==> tenant-key helper enforcement (I11)"
	@scripts/checks/check-tenant-keys.sh

.PHONY: check-log-retention
check-log-retention:
	@echo "==> explicit log retention on every log group (§10.1)"
	@scripts/checks/check-log-retention.sh

.PHONY: check-no-vpc
check-no-vpc:
	@echo "==> no Lambda attached to a VPC (G-018)"
	@scripts/checks/check-no-vpc.sh

.PHONY: check-no-alarms
check-no-alarms:
	@echo "==> no CloudWatch alarm or SNS topic (G-022)"
	@scripts/checks/check-no-alarms.sh

.PHONY: check-resource-prefix
check-resource-prefix:
	@echo "==> project prefix on every named resource (G-017)"
	@scripts/checks/check-resource-prefix.sh

.PHONY: check-brand-strings
check-brand-strings:
	@echo "==> no hardcoded user-visible strings (§7.3)"
	@scripts/checks/check-brand-strings.sh

.PHONY: check-guardrails
check-guardrails:
	@echo "==> guardrails-check.sh (§9.8)"
	@scripts/guardrails-check.sh --json

.PHONY: check-deps
check-deps:
	@echo "==> dependency scan (fail on high severity)"
	@scripts/checks/check-deps.sh

# --- Interface checks (active: Phase 1) -----------------------------------

.PHONY: check-a11y
check-a11y:
	@echo "==> accessibility and contrast, both capture faces (§4A.7)"
	@scripts/checks/check-a11y.sh

.PHONY: check-responsive
check-responsive:
	@echo "==> responsive at 320px and 1440px, no horizontal scroll (§4A.6)"
	@scripts/checks/check-responsive.sh

# --- Corpus checks (active: Phase 2) -------------------------------------

.PHONY: check-verify-corpus
check-verify-corpus:
	@echo "==> verify.sh against seeded fixtures (§11.6)"
	@scripts/verify.sh --json --fixtures

.PHONY: check-wer
check-wer:
	@echo "==> golden-fixture WER regression (§12)"
	@scripts/checks/check-wer.sh

# --- Extraction checks (active: Phase 3) ---------------------------------

.PHONY: check-extraction-fixtures
check-extraction-fixtures:
	@echo "==> extraction fixture assertions (§11A.8)"
	@scripts/checks/check-extraction-fixtures.sh

.PHONY: check-prompt-integrity
check-prompt-integrity:
	@echo "==> prompt integrity (§3A.3, A4)"
	@scripts/checks/check-prompt-integrity.sh

# --- Trigger additivity (active: Phase 8) --------------------------------

.PHONY: check-trigger-additivity
check-trigger-additivity:
	@echo "==> trigger-additivity diff check (§5.2 rule 2)"
	@scripts/checks/check-trigger-additivity.sh

# --- Build ---------------------------------------------------------------

.PHONY: build
build: build-lambda build-frontend

# Reproducible arm64 artifact (§0.6, §Phase 0). -trimpath and an explicit
# ldflags version make the zip a function of the commit rather than of the
# machine. Version comes from git describe, never a checked-in file (G-037).
.PHONY: build-lambda
build-lambda:
	@scripts/build-lambda.sh

.PHONY: build-frontend
build-frontend:
	@scripts/build-frontend.sh

.PHONY: doctor
doctor:
	@scripts/doctor.sh

endif

# ---------------------------------------------------------------------------
# Host-side targets: these drive the container and so cannot run inside it.
# ---------------------------------------------------------------------------

.PHONY: shell
shell:
	@./scripts/dev.sh

.PHONY: toolchain-tag
toolchain-tag:
	@./scripts/dev.sh --tag

.PHONY: toolchain-build
toolchain-build:
	@./scripts/dev.sh --build

# Recompute the pinned sha256 for every tool in versions.env. Run this when
# bumping a version; commit the version and the hashes in one change so a pin
# can never reference a binary nobody hashed.
.PHONY: toolchain-checksums
toolchain-checksums:
	@./containers/toolchain/refresh-checksums.sh

.PHONY: help
help:
	@echo "Chintan — every target runs in the toolchain container (containers/toolchain)."
	@echo ""
	@echo "  make check                every §0.5A gate, in the image CI uses"
	@echo "  make test                 unit tests + admin script tests"
	@echo "  make fmt                  format Go and shell"
	@echo "  make build                Lambda artifact + frontend bundle"
	@echo "  make doctor               validate prerequisites (§Phase 0)"
	@echo "  make shell                interactive shell in the toolchain image"
	@echo ""
	@echo "  make toolchain-tag        print the content-addressed image tag"
	@echo "  make toolchain-build      build the image locally"
	@echo "  make toolchain-checksums  re-pin tool hashes after a version bump"
	@echo ""
	@echo "Individual checks:"
	@printf '  %s\n' $(filter check-%,$(CONTAINERISED))
