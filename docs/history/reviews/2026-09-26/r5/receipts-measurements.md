# R5 receipts lens — live measurements (prod, 2026-09-26, test tenant claude-test)

All R5 fixtures purged afterwards: notes 0 left, captures 0 left, device revoked (r5.js purge → 204s).

## GET /v1/captures?status=all&limit=20 (the poll)
- 9,675 bytes, no Content-Encoding (gateway does not compress), no ETag, no Cache-Control; 146–163 ms from orb (3 runs).
- GET /v1/notes?state=active (page 1, 50 rows): 22,342 bytes, 222 ms. GET /v1/notes/{id}: 708 bytes.

## Prod API log, last 7 days (/aws/lambda/chintan-api-dev-prod, msg="request")
- 10,699 requests. By route: OPTIONS 3,024 · GET /v1/notes/{id} 2,508 · GET /v1/notes 1,995 · GET /v1/captures 545 · GET /v1/captures/{id} 498 · …/download 406 · GET /v1/settings 374.
- GET /v1/captures per day: 09-20 24 · 09-21 277 (QA day) · 09-22 1 · 09-24 80 · 09-25 63 · 09-26 100.

## Worker "capture pipeline finished", last 7 days (n=189)
- elapsed_ms p50 1,947 · p90 3,953 · max 48,819. Status: appended 183, needs_target 3, no_content 3.

## Owner tenant captures (DynamoDB scan, counts only)
- 52 rows: appended/client/app 18 · appended/router/device 17 · appended/router/app 14 · appended/user 1 · legacy 2.
- Today (day-0): 23 captures (17 device, 6 app); untargeted appended 17 into 3 notes, per note [13, 3, 1]; 6 targeted.
- day-5: 13 captures, 5 untargeted into 5 notes; 8 targeted.

## Home (Pixel 8 Pro profile 412×915, before fixtures)
filing section top 252, height 222: receipts 54+54, needs_target 98; first note row y=510; passkey nudge present.

## Home after 8 routed text captures (4 → Shopping list, 2 → Kitchen rebuild, 1 → Reading list, 1 needs_target)
- Current build: section 471 px; rows: Reading 54, Kitchen 54, Kitchen 54 (two receipts for one note), needs_target 150, needs_target 98; "4 more filed" (all four Shopping list receipts hidden — the busiest note is the invisible one); first note y=759; 2 note rows in the viewport.
- Mockup A (grouped): section 442 px; needs_target 150+98, then "Filed into Reading list" 54, "2 filed into Kitchen rebuild" 54, "4 filed into Shopping list" 54; nothing hidden; first note y=730.
- Mockup B (badges on note rows): section 256 px (only the two needs_target rows); first note y=544; 3 note rows in the viewport.
- Desktop 1280×800: current 418 px / A 390 px / B 204 px; first note 706 / 678 / 492.
- Pipeline time per fixture capture (created → appended): 0.4, 1.0, 1.7, 2.5, 3.6, 4.2, 4.8 s.
