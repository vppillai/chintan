# Log and data review — chintan-dev-prod, 2026-09-04

Window: the 24 hours ending 2026-09-04 ~19:40Z, which covers the ~45-capture
session the owner ran from 00:40Z. Sources, all read-only: CloudWatch Logs for
`/aws/lambda/chintan-worker-dev-prod` (833 events, 50 invocations) and
`/aws/lambda/chintan-api-dev-prod` (6,437 events, 1,302 invocations); a `Query`
of the tenant partition `USER#78e1c340-…` (156 items); a `ListObjectsV2` of
`tenants/78e1c340-…/` in the content bucket (321 objects); the two Lambda
configurations. The role cannot read CloudWatch metrics, so every number below is
computed from the log lines themselves (the JSON `provider usage` records, the
EMF `CaptureStageEntered` / `ProviderLatency` records, the `request` access-log
lines and the platform `REPORT` lines). The account id is redacted throughout.

## Headline

| | |
|---|---|
| Captures processed | **50** worker invocations, 50 captures, one invocation each — no Lambda retries, nothing dead-lettered, no `DuplicateDelivery`, no `AppendResumed…`, no `RouterContentDiscarded`, no `needs_target`, no `spend_capped`. 49 belong to the owner's tenant, 1 to the second tenant. |
| Final status | **50 / 50 `appended`**. Zero `failed`. |
| End-to-end (worker START → `appended`) | p50 **3.8 s**, p95 **8.8 s**, max **64.6 s**, min 1.6 s |
| S3 object landed → worker START | p50 **0.6 s**, p95 1.3 s, max 1.9 s |
| Stage: transcribe (incl. S3 writes) | p50 0.53 s, p95 0.87 s, max 1.2 s — Groq p50 0.41 s for p50 28 s of audio |
| Stage: route (19 captures without `note_id`) | p50 **2.4 s**, p95 **61 s**, max 63.1 s |
| Stage: cleanup | p50 2.3 s, p95 5.5 s, max 7.0 s |
| Stage: append (claim, S3 PUT, index refresh) | p50 0.19 s, p95 0.24 s |
| Worker cold starts | 8 of 50; init p50 226 ms |
| Provider cost | **$0.0408 for 2026-09-04** (48 captures, 40,791 µ$), $0.0008 on 09-03 (1 capture). Per capture p50 638 µ$, p95 1,709 µ$, max 2,176 µ$. By op: transcribe 20,230 µ$ (50%), cleanup 11,842 µ$ (29%), route 9,497 µ$ (23%). Matches the `INSTANCE / SPEND#` rows to the microdollar (40,791 and 778). |
| WARN / ERROR lines | **1 WARN**, 0 ERROR (worker); 0 in the API. |
| API requests | 1,302 in 24 h; 2 non-2xx (both 404, other tenant, see below). |

Verdict: the pipeline did what it is supposed to do on every capture. The
two things worth acting on are (1) MiniMax routing latency, which stalled at
~60 s twice and returned 529 once, and (2) the client-side upload, which is
where the seconds the owner notices actually go.

## 1. Per-capture timeline

Columns: seconds from S3 `LastModified` to worker START (`s3>st`), per-stage
seconds derived from consecutive `CaptureStageEntered` records, end-to-end from
START to `capture pipeline finished`, provider latencies from `ProviderLatency`
(`sttL`/`rteL`/`clnL`), cost in microdollars, final status. `–` in `route` means
the capture was started with a `note_id`, so no routing call was made.

```
capture                            worker START   s3>st   xscr  route  clean  appnd    e2e |  sttL  rteL  clnL | cost  status    notes
c_18d1eef7f15ea266_2f0b11c2f477ff7d 09-03T22:07:57     -    0.3   63.1    0.9    0.2   64.6 |   0.3  62.9   0.9 |  778  appended  cold 231ms; row purged (note deleted 22:10Z)
c_18d1f750d7a6145d_71a41e5a8d7b5f4f 09-04T00:40:57   1.2    0.5    2.1    1.2    0.3    4.1 |   0.4   2.0   1.1 |  643  appended  cold 253ms
c_18d1f77ba0c5d8e0_0452fb924c8702b4 09-04T00:44:03   0.3    0.4      -    3.1    0.2    3.8 |   0.3     -   3.0 |  471  appended  note_id given
c_18d1f78d5373b7b6_696af7c741e12f6d 09-04T00:45:25   0.1    0.6    1.3    2.0    0.2    4.1 |   0.5   1.2   1.9 | 1230  appended
c_18d1f7a530112116_2fe8d420d5f145da 09-04T00:47:09   1.2    0.6      -    2.5    0.2    3.3 |   0.5     -   2.5 |  507  appended  note_id given
c_18d1f7c63ace80f4_6e68c45af1551076 09-04T00:49:24   0.7    0.5      -    1.1    0.2    1.8 |   0.4     -   1.0 |  468  appended  note_id given
c_18d1f7df6a57ccd1_f3952c5691a9bb27 09-04T00:51:15   0.6    0.8      -    2.7    0.2    3.7 |   0.7     -   2.7 | 1135  appended  note_id given
c_18d1f7f75a26aaa9_929a847034ec140a 09-04T00:52:54   1.3    0.5      -    2.3    0.3    3.2 |   0.4     -   2.3 |  531  appended  note_id given
c_18d1f80a4a682993_16e51db117480ea0 09-04T00:54:11  -0.1    0.4      -    3.5    0.2    4.1 |   0.3     -   3.5 |  421  appended  note_id given
c_18d1f815920b5366_0623590aee4362f4 09-04T00:55:00   0.3    0.4    2.6    1.0    0.2    4.1 |   0.3   2.5   0.9 |  914  appended
c_18d1f82a2f3bc1d2_558a7caa7b58105c 09-04T00:56:30   0.0    0.7    4.2    0.8    0.2    5.8 |   0.5   4.1   0.7 | 1197  appended
c_18d1f836abfc8626_527dea98799dd731 09-04T00:57:40   0.7    0.6    2.6    3.8    0.2    7.2 |   0.5   2.5   3.7 | 1252  appended
c_18d1f84b26b27792_c144eac9c7fb9da0 09-04T00:58:53   1.0    0.7    1.7    1.4    0.2    4.1 |   0.6   1.7   1.4 | 1665  appended
c_18d1f85ec10d73f3_aac7fad8bee736fc 09-04T01:00:17   0.5    0.5      -    6.1    0.2    6.8 |   0.4     -   6.0 |  456  appended  note_id given
c_18d1f866af59b648_baa6132b6e867f87 09-04T01:00:50     -    0.3    0.6    2.4    0.2    3.4 |   0.2   0.4   2.3 |  618  appended  row purged (note deleted 01:20Z)
c_18d1f874af864616_b29bc5c2d36747a0 09-04T01:01:51   0.5    0.5    2.2    0.9    0.2    3.9 |   0.4   2.2   0.8 | 1130  appended
c_18d1f8822bcfb28d_00ba958a486ee6bd 09-04T01:02:47   0.8    0.5      -    1.0    0.2    1.6 |   0.3     -   0.9 |  378  appended  note_id given
c_18d1f897f9ca7f5e_d88c8d70bb05f623 09-04T01:04:27   0.2    0.9    2.4    3.1    0.2    6.6 |   0.8   2.3   3.0 | 2176  appended
c_18d1f8b1d785ad59_d12c1946b2ea28d7 09-04T01:06:12  -0.1    0.7   60.8    1.6    0.2   63.2 |   0.6  60.6   1.5 | 1623  appended
c_18d1f8c3f3ebdf9d_99a86ba2c11be3e8 09-04T01:07:28  -0.0    0.5    1.1    1.9    0.2    3.7 |   0.3   1.0   1.8 | 1007  appended
c_18d1f8e974c740be_0bb00cd8326814ef 09-04T01:10:10   0.9    0.8    4.5    2.7    0.2    8.2 |   0.6   4.4   2.6 | 1443  appended
c_18d1f8fa5fac2409_3b53f68f6f13206c 09-04T01:11:23   0.5    0.7    2.9    3.5    0.2    7.4 |   0.6   2.8   3.4 | 1515  appended
c_18d1f9362a26f861_3cf5482965e23a1e 09-04T01:15:39  -0.1    0.8      -    1.5    0.2    2.6 |   0.7     -   1.5 | 1367  appended  note_id given
c_18d1f950600afc47_8856421c72a50f48 09-04T01:17:32   0.5    0.8      -    3.3    0.2    4.3 |   0.7     -   3.2 |  954  appended  note_id given
c_18d1f97853fd7bc3_05706c1e8d7e4ea6 09-04T01:20:24   0.8    0.6      -    2.8    0.2    3.6 |   0.5     -   2.7 |  648  appended  note_id given
c_18d1f987bd2e0643_7621d6de49e40085 09-04T01:21:30   0.6    0.5      -    7.0    0.2    7.6 |   0.4     -   6.9 |  445  appended  note_id given
c_18d1f992e71476b5_4c9a62e73aa14873 09-04T01:22:17  -0.0    0.4      -    1.2    0.2    1.7 |   0.3     -   1.1 |  179  appended  note_id given
c_18d1f9a2047971db_2e33a98abbd46005 09-04T01:23:22  -0.0    0.4      -    2.9    0.2    3.5 |   0.3     -   2.8 |  538  appended  note_id given
c_18d1f9be1df97f44_ca2a2a8c777797e1 09-04T01:25:24   1.1    1.2      -    5.2    0.2    6.6 |   1.1     -   5.1 | 1745  appended  note_id given
c_18d1f9c78548212a_384cde7af98bbf7f 09-04T01:26:04   0.1    0.5      -    2.0    0.2    2.7 |   0.4     -   1.9 |  541  appended  note_id given
c_18d1f9cf8e78f1de_1b31acc5040103f3 09-04T01:26:39   1.1    0.4      -    1.4    0.2    2.0 |   0.3     -   1.4 |  256  appended  note_id given
c_18d1fa44e826bbbc_deafd1fc1e596333 09-04T01:35:03   1.4    0.5      -    2.4    0.2    3.1 |   0.4     -   2.4 |  452  appended  cold 213ms; note_id given
c_18d1fa63e9500927_5931cf6cf45afce7 09-04T01:37:16   1.6    0.8      -    3.7    0.2    4.8 |   0.7     -   3.7 | 1109  appended  cold 215ms; note_id given
c_18d1fa6d75d8acd8_b7cd34a67f6f8c42 09-04T01:37:56   1.0    0.6      -    1.7    0.2    2.5 |   0.5     -   1.7 |  599  appended  note_id given
c_18d1fa7fd12866f2_831f5705c7701887 09-04T01:39:16   0.4    0.4      -    1.0    0.2    1.6 |   0.3     -   0.9 |  479  appended  note_id given
c_18d1fa9573a72925_adc142dfe4330b96 09-04T01:40:48   0.7    0.5      -    1.3    0.2    2.0 |   0.4     -   1.3 |  321  appended  note_id given
c_18d1faae706b57ed_ffbe845a46a36d1e 09-04T01:42:36   1.1    0.4    2.0    1.0    0.2    3.6 |   0.3   1.9   1.0 |  606  appended
c_18d1fac97d0dea95_c9782950cb309ef9 09-04T01:44:32   0.4    0.6      -    3.8    0.2    4.7 |   0.6     -   3.7 |  741  appended  note_id given
c_18d1faeb88ede0e7_026604a80a8ca879 09-04T01:46:58   0.8    1.1      -    5.7    0.2    7.1 |   1.0     -   5.6 | 1801  appended  note_id given
c_18d1fbc034023da5_06bc4194a41ddb50 09-04T02:02:12   1.9    0.5    4.7    2.6    0.2    8.0 |   0.4   4.6   2.6 |  830  appended  cold 223ms
c_18d1fbdb00f0aa7b_bcb336dd5badbde1 09-04T02:04:07   0.6    0.7    5.9    2.4    0.2    9.2 |   0.5   5.8   2.3 | 1447  appended
c_18d1fbec5ee9e2a9_0c11d41ed3259c80 09-04T02:05:21   0.9    0.5      -    1.8    0.2    2.6 |   0.4     -   1.8 |  448  appended  note_id given
c_18d1fbf790d75f89_d6d4ccf7a9b5b8d8 09-04T02:06:09   0.7    0.6      -    1.8    0.2    2.6 |   0.5     -   1.7 |  632  appended  note_id given
c_18d1fc045ecf147c_8cbf2c3c3a8e4cfe 09-04T02:07:04   0.5    0.8      -    2.6    0.2    3.7 |   0.7     -   2.6 |  702  appended  note_id given
c_18d1fc0e62ddfe56_c8e7ca6f7e99303d 09-04T02:07:48   1.1    0.4      -    3.1    0.2    3.7 |   0.3     -   3.0 |  325  appended  note_id given
c_18d1fc1be542c0c5_a4d8ff624083fb36 09-04T02:08:46   1.1    0.7      -    3.7    0.2    4.6 |   0.6     -   3.6 |  849  appended  note_id given
c_18d1fcafeecf0042_80addc0ef22a09c3 09-04T02:19:21   0.9    0.5    1.0    2.3    0.2    4.0 |   0.4   0.8   2.2 |  392  appended  cold 220ms; WARN routing failed: llm status 529 → new note "Voice note 2026-09-04 02:19"
c_18d225b1318d203e_a32d8bca5fa249ae 09-04T14:50:46   0.4    0.6      -    1.4    0.3    2.2 |   0.5     -   1.3 |  546  appended  cold 229ms; note_id given
c_18d225b8e87a51fa_5b5d6b8a64178743 09-04T14:51:19   0.3    0.5      -    1.1    0.2    1.9 |   0.4     -   1.1 |  458  appended  note_id given
c_18d23519976a1f60_d3806ccd8d3bfaf3 09-04T19:33:08     -    0.3    1.2    1.2    0.2    3.0 |   0.2   1.1   1.1 |  601  appended  cold 231ms; OTHER TENANT (6871b3c0-…)
```

Negative `s3>st` values are S3's one-second `LastModified` granularity.

### What happened that was not the happy path

1. **Two routing calls stalled at ~60 s** — `c_18d1eef7f15ea266` (62.9 s,
   22:09Z on 09-03) and `c_18d1f8b1d785ad59` (60.6 s, 01:06Z). Both returned a
   normal, correct answer with ordinary token counts (1,817 in / 33 out and
   ~1,300 in / ~120 out). The prompt was not the cause: there were 5–7 candidate
   notes, `thinking` is already `{"type":"disabled"}`, and the other 16 routing
   calls took 0.4–5.8 s on the same prompt shape. This is provider-side queueing
   at MiniMax. The pipeline handled it correctly (no timeout, no retry, correct
   result), but the user watched "routing" for a minute.
2. **One MiniMax 529 (overloaded)** at 02:19:23Z on the routing call for
   `c_18d1fcafeecf0042`. The pipeline did exactly what `route()` documents:
   logged a WARN, kept the dictation, and filed it into a new note titled
   `Voice note 2026-09-04 02:19`. The capture is `appended` and nothing was
   lost, but the owner got an auto-titled note instead of the destination they
   dictated — and there was no retry of the routing call before falling back.
   529 is not classified as rate-limited (`IsRateLimited` is 429 only), so the
   `ProviderRateLimited` counter did not move; nothing alarmed, which is right.
3. **Three worker-log captures have no DynamoDB row today.** Two are the
   owner's and were cascade-deleted with their notes: `c_18d1eef7f15ea266`
   (its note was in the 26-note purge at 22:10:54–59Z) and `c_18d1f866af59b648`
   (purged at 01:20:49Z). Both `POST /v1/notes/purge` calls show in the access
   log as `POST /v1/notes 200` — see finding 6. The third,
   `c_18d23519976a1f60` at 19:33Z, belongs to the second tenant and its row is
   present in that tenant's partition (53 items).
4. **Two 404s** at 19:33:13Z (`GET /v1/captures…`), both from the second
   tenant, five seconds after its capture appended. The route label collapses
   `/v1/captures/{id}` and `/v1/captures/{id}/download` to `/v1/captures`, so
   the log cannot say which; the capture row exists, so the likeliest reading
   is a `download?kind=peaks` for a capture whose client never uploaded peaks
   (this capture has no `peaks.json` in S3). That is the `has_peaks` bug fixed
   in this branch (section 2).

### Routing latency and the recommended fix

Route latency is bimodal: p50 2.4 s, but p95 61 s because 2 of 18 calls hit a
~60 s provider stall. Cleanup — same model, same endpoint, larger output —
never exceeded 7 s across 50 calls, so the stall is specific to whatever queue
the short routing completion lands in, not to this account's throughput.

Prompt size is not the lever. The system prompt is ~900 tokens and the
candidate list was 5–7 notes; input tokens p50 1,248 / max 1,817. Output tokens
p50 118 / max 278 — and that *is* the dominant cost in the normal case, because
the router echoes the transcript back as `content` (transcript minus spoken
instructions), so output scales with the recording. At MiniMax's observed
~30–40 tok/s, 118 tokens is ~3 s; that is where the 2.4 s median comes from.

Recommended, in order of payoff:

1. **Bound the routing call and retry once before falling back.** Wrap
   `Router.Route` in `context.WithTimeout(ctx, 15*time.Second)`; on timeout or
   5xx/529, retry once; only then take the existing "keep the dictation in a
   new note" path. Routing is a convenience (the code already says so), so a
   capped wait is the right trade — a 60 s stall becomes a 15 s one, and the
   529 becomes a retry instead of a mis-filed note. Cleanup must **not** get
   the fallback (there is nothing to fall back to), but a 60 s per-attempt
   timeout plus one retry is safe there too; the 840 s client timeout is only
   the last line.
2. **Stop echoing the transcript.** Ask the router for the character span (or
   the word count) of the spoken instruction to remove instead of `content`.
   Output drops from ~120 tokens to ~30, routing p50 from ~2.4 s to ~1 s, route
   cost by ~70 %, and `contentDerivedFrom` becomes a range check. This also
   removes the `RouterContentDiscarded` failure modes entirely.
3. **Set `max_tokens`** (e.g. 512 for routing) so a runaway completion is
   bounded; it does not shorten a normal call but caps the worst case.

Not recommended: a different model for routing. The latency spread is a
provider queue, not model speed, and a second endpoint is a second key, a
second price row and a second failure mode for a 2 s median.

## 2. DynamoDB vs S3 reconciliation (tenant 78e1c340-…)

Partition contents: 63 `CAPTURE#`, 7 `NOTE#`, 81 `IDEM#` (24 h TTL), 5 legacy
per-tenant `SPEND#` rows from August (harmless; TTL-bounded). Bucket prefix:
321 objects — 63 `audio.webm`, 63 `raw.txt`, 60 `clean.txt`, 47
`segments.json`, 47 `peaks.json`, 27 `routed.txt`, 7 `note.md`, 7 `meta.json`.

- **Every key named by every capture row exists in S3** (63/63; `audio`, `raw`,
  `routed`, `clean`, `segments`, `peaks` where set). Zero `missing_object`.
- **Every note row's `note.md` and `meta.json` exist** (7/7).
- **Zero S3 objects without an owning row.** In particular the notes the owner
  deleted today (`POST /v1/notes/purge` at 22:10Z and 01:20Z) left nothing
  behind: their `note.md`/`meta.json` and every capture object (`audio`, `raw`,
  `routed`, `clean`, `segments`, `peaks`) are gone and the capture rows are
  gone. The cascade works. The three notes archived today (`Conversation note`,
  `Quick dictation feature`, `Voice note 2026-09-04 02:19`) still have their
  objects, as they should until their 2026-10-04 purge date.
- **14 captures point at notes that no longer exist** — all from 2026-08-08
  (`c_124893…`, `c_13a470…`, `c_43a149…`, `c_5ffabd…`, `c_6ee359…`,
  `c_7cab14…`, `c_83b087…`, `c_a2f5e8…`, `c_ac5aef…`, `c_ad06b0…`, `c_bbae65…`,
  `c_bbca33…`, `c_c5936b…`, `c_f27027…`), each with 3–4 objects still in S3.
  Their notes (`note_1786…`) were deleted in August by code that did not yet
  cascade through GSI1. `chintanctl reconcile` does not flag them today (it
  only compares rows to objects, and these rows exist). They cost cents, but
  they are unreachable through the UI and should go: extend `reconcile` with a
  `dangling_capture` finding (capture row whose `note_id` has no `NOTE#` row)
  and let `--apply` delete the row and its objects. Not done in this branch.
- **`has_peaks` was optimistic.** `captureOf` derived `has_peaks` from
  `PeaksKey != ""`, and `BeginCapture` sets `PeaksKey` when it *issues* the
  presigned URL. Today 47 of 47 recent captures do have `peaks.json`, so the
  owner never saw the bug, but the second tenant's capture at 19:33Z has no
  `peaks.json` and was reported `has_peaks: true` — which is the shape of the
  two 404s above. Fixed in this branch: once a capture reaches a terminal
  status the worker HEADs `peaks.json` once and, if the bucket has no such
  object, clears `peaks_key` on the row — so `has_peaks` keeps its cheap
  derivation and GSI1's projection (which carries `peaks_key` and cannot grow
  a new attribute without an index rebuild) stays truthful. The cascade
  delete unlinks the derived peaks key regardless, so a late peaks upload
  cannot outlive its capture. Captures from before this change keep the
  optimistic answer until the worker next touches them. `has_segments` needed
  no change: `SegmentsKey` is set only after the worker's own successful PUT.

## 3. Upload latency — where the seconds go

The owner: "even very small recordings take a few seconds to upload". The
server-side numbers say the API is not where the time is.

| Step | Measured | Notes |
|---|---|---|
| `POST /v1/captures` (server) | **p50 21 ms**, p95 44 ms, max 62 ms, n=50 | Never cold: a CORS preflight or a list poll had already warmed the function. |
| API Lambda cold start | `Init Duration` p50 **72 ms** (109 of 1,302 invocations) | 512 MB arm64, 7.6 MB zip. Negligible. The *first request* on a cold container is ~260 ms (JWKS fetch + first DynamoDB TLS), visible on `GET /v1/notes|captures` cold p50 260 ms vs warm 4–12 ms. |
| `POST` → audio `LastModified` in S3 (client PUT, incl. presign round trip) | **p50 1.6 s**, p95 10.8 s, max 19.3 s, n=47 | This is the variable. See below. |
| Peaks PUT | lands ~1.0 s after the audio | Sequential after the audio PUT in `uploader.ts`. |
| S3 event → worker START | p50 0.6 s, p95 1.3 s | S3 notification + Lambda dispatch. Not controllable. |
| Worker pipeline | p50 3.8 s (with `note_id`), 4–9 s when routed | Provider-bound (section 1). |
| UI poll | `GET /v1/captures` every **4 s** while anything is pending | So the "Filed" state appears 0–4 s after `appended`. |

The client PUT is uncorrelated with size: the ten slow uploads (5.6–19.3 s)
were 67–250 KB files at 24 kbps, no larger than the 1 s ones, and all fell in
the first twenty minutes of the session (00:44–01:04Z); from 01:35Z onwards
every PUT landed within 0.8–2.1 s. That pattern — a bad stretch of time, not a
bad size — is the network or the device, not the backend. Server-side there is
no visibility into it: S3 server access logging is off on the content bucket,
so the only fact we have is the object's `LastModified`. `putPresigned` retries
up to four times with jittered backoff (0.5·2ⁿ s, capped 8 s) and has **no
per-attempt timeout**, so one hung connection is waited on until the browser
gives up; a first attempt that failed fast followed by two backoffs is
consistent with the 6–12 s cases.

What the user perceives as "upload" is: PUT (1–2 s good network) + S3→worker
(0.6 s) + pipeline (2–9 s) + poll granularity (0–4 s) ≈ **4–15 s** before the
row flips to Filed, of which the actual upload is the smallest part on a good
connection.

Recommendations:

- **Client `putPresigned`: add a per-attempt timeout** (e.g. 20 s for audio,
  5 s for peaks) via `AbortSignal.timeout`, and surface the attempt number in
  the coarse progress so a retry is visible rather than a frozen bar.
- **Fire the peaks PUT concurrently with the audio PUT.** It is independent
  and lands ~1 s later today; concurrent, it costs nothing extra.
- **Poll faster while a capture is in flight, then back off**: 1.5 s for the
  first 15 s, then 4 s. Today's fixed 4 s adds a median 2 s to a median 4 s
  pipeline. Cost is a handful of extra `GET /v1/captures` at 4 ms each.
- **API Lambda memory/arch: leave it.** Init is 72 ms and `POST` is 21 ms;
  there is nothing to buy. Provisioned concurrency would remove the ~260 ms
  first-request cost on a cold container but is the only lever for that, and
  it is not wanted for cost reasons — and it is not where the seconds are.
- The presigns are already generated in one `POST` (audio and peaks, 21 ms
  total); there is no round trip to save there.
- Optional, cheap, and the only way to see the client PUT from the server
  side: enable S3 server access logging (or request metrics) on the content
  bucket for a week. Then turn it off.

## 4. Other observations

5. **Notes list is fetched a lot.** 291 `GET /v1/notes` and 93 `GET /v1/tags`
   in 24 h, often 2–3 at the same millisecond (three simultaneous
   `OPTIONS`+`GET /v1/captures` at 00:41:09.26Z, two `GET /v1/notes` 2 ms
   apart at 00:44:03Z). Several mounted components run the same query. React
   Query dedupes within one client only if the keys match; worth checking the
   library and note screens share `queryKeys.notes()`. Cost today is nil (4–12
   ms each) but it is the pattern that becomes a bill.
6. **The access log's `route` collapses paths to two segments.** `Correlate`
   is outside the mux, so `r.Pattern` is empty there and `routePattern` falls
   back to `/v1/notes`, `/v1/captures`. Consequences seen in this review:
   `POST /v1/notes/purge` is logged as `POST /v1/notes 200` (create returns
   201, which is how it was told apart), `GET /v1/captures/{id}/download` is
   indistinguishable from the list, and the two 404s cannot be attributed. The
   `instrument` wrapper already knows the registered pattern; stashing it in
   the request context (`routeKey`) *before* the handler runs and reading it
   in `Correlate` fixes this in a few lines. Not done in this branch.
7. **`routed.txt` is written for every routed capture even when nothing was
   removed** (27 objects, most byte-identical to `raw.txt`). Harmless; a
   future cleanup could skip the PUT when `decision.Content == transcript`.
8. **The 22:10:59Z purge took 3.4 s** for 26 notes — about 130 ms per note
   (GSI1 query, up to six deletes, row delete). Well inside the 29 s API
   ceiling for the 100-note batch cap; noted as the number to watch.
9. **Worker memory**: 34–40 MB used of 2,048 MB. The allocation is for CPU
   share on the transcription pipe and multipart stream, not for memory, and
   the billed duration is provider wait; halving it would roughly halve the
   worker's compute bill (≈ $0.002/day today) at no measurable risk. Low
   priority.
10. **Spend accounting is exact**: the sum of `cost_micros` in the log equals
    the `SPEND#` counter for both days, so estimate/reconcile round-trips are
    balanced. Transcription is half of all cost at $0.04/audio-hour.

## Method notes

- Stage durations are gaps between consecutive `CaptureStageEntered` EMF
  records within one invocation (and to `capture pipeline finished` for the
  last stage), so "transcribe" includes the raw/segments PUTs and "append"
  includes the claim, the conditional note PUT, the index refresh and the
  completion write.
- Provider latency is the breaker's own `ProviderLatency` record.
- The join to S3 uses the capture id in the object key; `LastModified` is
  second-granular.
- Invocations were grouped per log stream by `START`/`REPORT` pairs; the
  `Init Duration` field of `REPORT` identifies cold starts.
