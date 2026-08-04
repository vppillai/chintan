# ADR 0002: Go on ARM64 Lambda

Date: 2026-08-04
Status: accepted

Records the Phase 0 runtime decision. §4 states this is "decided, not open" and
requires it recorded as the Phase 0 ADR; §14.4 lists it under "settled, and not
open despite appearances". This ADR exists so the reasoning is available when
someone is tempted to re-litigate it per component, which §4 also forbids.

## Context

Two competing runtimes for the Lambda functions: Node (which the frontend
toolchain would share) and Go (which the reference implementation
`vppillai/passbook` already uses).

## Decision

**Go, ARM64 (Graviton), `provided.al2023`.** Two functions, one per execution
profile, each internally routed — a "Lambdalith" per profile, never one function
per endpoint:

| Function | Memory | Timeout | Invoked by |
|---|---|---|---|
| sync API | 256MB | 10s | API Gateway, all HTTP routes |
| async worker | higher (§10.2) | longer | S3 events and schedules; not externally reachable |

Reasons, in the order they matter:

1. **ARM64 is ~20% cheaper per GB-second**, and Go's cold start and memory
   footprint let a small allocation suffice where Node would need several times
   more. In a stack targeting ~$1/month, that compounds.
2. **It matches the existing passbook toolchain and operator skillset**, whose
   patterns are normative here (§10.1).
3. **One function per endpoint is rejected outright** — it multiplies cold starts
   and duplicated init for no benefit (§4).

## Consequences

**The one non-obvious cost, stated plainly.** Phase 2 runs Silero VAD server-side
through the ONNX Runtime **C API**, linked into the Go worker, with the shared
library shipped in the artifact or a layer. §4 calls this "the one place the Go
decision has a non-obvious cost, and it is cheaper to prove than to discover",
which is why the Phase 2 entry gate validates the ARM64 build in a real deploy and
records the binding choice as its own ADR.

If that cannot be made to work, the alternatives are a separate Node worker for
VAD only, or server-side segmentation by the energy-based fallback. Both are design
changes rather than details, and both would be escalated rather than chosen
silently (§0.2 rule 4).

A second consequence, deliberately accepted: **`provided.al2023` with CGO enabled**
is what the ONNX C API requires, and CGO forfeits the fully static binary Phase 0
enjoys. The toolchain image (ADR 0005) is where that cross-compile setup will
live, which is part of why that image exists.

Rejected: **Node on ARM64.** It would share a language with the frontend, which is
a real ergonomic benefit. It loses on cold start and memory at the allocation sizes
this budget wants, it diverges from passbook, and — with no framework in the
frontend (ADR 0004) — the shared-language benefit is smaller than it first appears.
