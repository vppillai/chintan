# ADR 0001: No Android Auto integration

Date: 2026-08-04
Status: accepted

Summarises §14.1, recorded as an ADR because §Platform requires it explicitly.

## Context

The primary use context is hands-busy capture — driving, walking, in a workshop
(§1). The obvious inference is that the app should appear on the car's head unit.
It cannot, for two independent reasons:

1. **Android Auto is a curated interface, not a screen mirror.** It enforces a
   strict app-category whitelist: media, navigation, POI, IoT, plus messaging/VoIP,
   games, and weather. A capture-and-triage app fits none of them **even as a
   native app**.
2. **A PWA cannot surface in Android Auto at all**, regardless of category.

A "browser" category was announced separately. That means a browser app ships, not
that arbitrary web apps get a launcher tile, and it will almost certainly be
parked-only.

The failure mode is the reason this is worth an ADR: the app simply never appears
in the launcher. No error, no explanation, nothing to debug (G-001).

## Decision

**No Android Auto or CarPlay integration, and no architecture that anticipates
one.** It is an explicit non-goal (§2) — not deferred, not "later".

The driving path is instead three separate mechanisms, each addressing a different
moment:

| Mechanism | Role | Phase |
|---|---|---|
| **Voice launch** — "Hey Google, open Chintan" | Hands-free start. The only genuinely hands-free path. | 1 |
| **Telegram** | Text capture in the car. Android Auto's messaging reply transcribes on-device and sends **text**, so text-in is the actual driving path, not a degraded fallback (G-030). | 6 |
| **NFC tag** | One blind gesture after unlock. It does not eliminate unlock: Android only dispatches tags while the screen is unlocked (G-006). | 8 |
| **Offline recorder** | Covers every case where the phone is not available at all. | 6A |

## Consequences

- The app name becomes a **functional** decision rather than a branding one, because
  voice launch resolves by fuzzy name match. Anything containing "notes", "voice",
  "memo", or "recorder" loses to Google Keep or the stock recorder (G-005). This is
  why the name is Chintan, and why §0.8 item 2 requires testing it in a moving car
  before Phase 1.
- Capture in the car depends on the phone being unlocked, or on Telegram, or on the
  recorder. §Phase 8 documents that expectation in the UI rather than letting a user
  discover it at speed.
- Nothing in the codebase carries a car-specific surface. The capture face is
  designed for hands-busy use (§4A.1) but it is a web page, reached the way any
  other web page is.
