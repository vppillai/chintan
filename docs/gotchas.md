# Gotchas

Surprises encountered — where reality differed from reasonable expectation.

**This file is a first-class deliverable, not a scratchpad** (§0.4). It outlives
this project: it is intended to be carried into future builds the same way
`vppillai/passbook`'s lessons were carried into this one.

Seeded at the start of Phase 0 from spec §13, which holds everything established
during design. **From that point this file is the living register and §13 is only
its starting inventory — do not maintain both.** Add to it whenever something
surprises you, including things that cost only twenty minutes; twenty minutes
multiplied across every future project is the actual saving.

Related registers, with distinct purposes (§0.4):

| Location | Contains |
|---|---|
| `docs/decisions/` | ADRs — choices made, alternatives considered, consequences accepted |
| `docs/findings/` | Empirical results answering a **pre-registered** question (§0.2) |
| `docs/gotchas.md` | This file — surprises found incidentally during build or test |

A refuted finding usually also produces a gotcha. Record both: the finding
documents the experiment, the gotcha documents the trap.

## Entry format

```markdown
#### G-0NN — one-line statement of the trap
**Assumption:** what a reasonable person would have believed
**Reality:** what is actually true
**Symptom:** how it presents — especially whether it survives testing
**Action:** what to do instead
**Confidence:** verified | documented | reported
**Refs:** spec sections, findings, or ADRs
```

The **Symptom** line earns its place: most entries here describe failures that
pass testing and fail in the real situation, and knowing how a trap *presents* is
usually what saves the time.

**Confidence levels:** `verified` — observed directly on real hardware or against a live API. `documented` — stated in official vendor documentation. `reported` — from secondary sources or prior experience; re-verify before relying on it. Promote entries as they are confirmed, and correct them when they turn out wrong. A register that is never corrected becomes folklore.

**IDs are permanent labels, not an ordering.** Entries are grouped by category for reading; within a category the numbers run out of sequence, and some numbers are absent, because IDs are assigned when a gotcha is discovered and never reassigned afterwards. A gap is not a missing entry — it is an ID that was never used or whose entry was merged. **Never renumber, and never reuse a number**, in this section or in `docs/gotchas.md`: these IDs are cited from the spec body, from findings, and from commit messages, and a renumbering silently redirects every one of those references. Fifty-nine entries are recorded here; the next new one is `G-064`.

### Mobile web / PWA

#### G-001 — Android Auto will not run a web app
**Assumption:** A mobile-friendly PWA can be surfaced on the car's head unit.
**Reality:** Android Auto is a curated interface, not a screen mirror. It enforces a strict app-category whitelist — media, navigation, POI, IoT, plus messaging/VoIP, games, weather. A PWA cannot appear at all, and a voice-notes app fits no permitted category even natively. A "browser" category was announced separately; that means a browser app ships, not that arbitrary web apps get a launcher tile, and it will almost certainly be parked-only.
**Symptom:** App never appears in the Android Auto launcher. No error, no explanation.
**Action:** Do not plan any in-car surface for a web app. Use voice launch for hands-free start, a messaging bot for text capture, or an offline recorder.
**Confidence:** documented
**Refs:** §14.1

#### G-002 — MediaRecorder stops when the tab backgrounds or the screen locks
**Assumption:** Starting a recording keeps recording.
**Reality:** Android Chrome suspends `MediaRecorder` when the tab is hidden or the screen locks. iOS Safari is worse.
**Symptom:** Recordings truncate around the screen-timeout interval. Usually about 60 seconds. Reproduces only on a real device with real screen timeout — never in a desktop browser with the tab focused.
**Action:** Acquire a Screen Wake Lock, keep the phone charging for long sessions, and treat "continuous background capture in a PWA" as unavailable. See G-003.
**Confidence:** reported

#### G-003 — Screen Wake Lock is silently released when the tab hides
**Assumption:** `navigator.wakeLock.request('screen')` holds until explicitly released.
**Reality:** The lock is dropped whenever the document becomes hidden, and is **not** automatically restored when it becomes visible again.
**Symptom:** Recording survives a brief interruption once, then dies on the second. Intermittent and hard to reproduce deliberately.
**Action:** Re-acquire the lock on every `visibilitychange` where the document becomes visible. Never assume a single request persists.
**Confidence:** documented
**Refs:** spec Phase 1

#### G-004 — Bluetooth pairing silently degrades the microphone to 8kHz
**Assumption:** `getUserMedia` uses the phone's microphone.
**Reality:** If the phone is paired to a car or headset, capture may route through the hands-free profile (HFP), which is narrowband. Speech recognition error rates rise sharply.
**Symptom:** Transcription quality collapses specifically in the car, and is fine everywhere else. Easy to misattribute to road noise.
**Action:** Pin the input with an explicit `deviceId` constraint and **measure the sample rate that actually arrives** — do not trust the constraint to have been honoured.
**Confidence:** reported
**Refs:** spec Phase 1

#### G-005 — Voice launch resolves by fuzzy name match, so the app name is a functional decision
**Assumption:** App naming is branding.
**Reality:** "Hey Google, open X" fuzzy-matches against installed app names. Anything containing "notes", "voice", "memo", or "recorder" loses to Google Keep or the stock recorder.
**Symptom:** Voice launch opens the wrong app, reproducibly, and only on devices where the competing app is installed.
**Action:** Choose two syllables, phonetically distinctive, not a dictionary word. Test in a moving car with road noise, on a device with the likely competitors installed. Changing the name is nearly free before install and annoying after.
**Confidence:** reported

#### G-006 — NFC tags only dispatch when the screen is unlocked
**Assumption:** Tapping an NFC tag can launch an app from a locked phone.
**Reality:** Android only looks for NFC tags while the screen is unlocked.
**Symptom:** Tag works perfectly when testing at a desk with the phone in hand; does nothing in the real scenario where the phone is locked in a pocket or cradle.
**Action:** Treat NFC as collapsing everything *after* unlock into one blind gesture, not as eliminating unlock. Voice launch remains the only genuinely hands-free path.
**Confidence:** documented
**Refs:** spec Phase 8

#### G-057 — GitHub Pages needs a paid plan for private repos, and publishes publicly regardless
**Assumption:** A private repository can serve a GitHub Pages site, and keeping the repository private keeps the site private.
**Reality:** Two separate limits. Pages from a private repository requires Pro or above — on the Free plan it is unavailable entirely. And source visibility is independent of site visibility: the published site is public in every plan except Enterprise Cloud with Pages access control, so the deployed HTML, JavaScript, and source maps are world-readable.
**Symptom:** Either Pages simply cannot be enabled, or it works and someone later assumes the frontend bundle is private because the repository is.
**Action:** Decide plan and visibility before building. Treat the frontend bundle as public in all cases — never ship a secret, key, or credential in it.
**Confidence:** documented
**Refs:** §10.6

#### G-058 — GitHub Pages terms prohibit hosting commercial SaaS
**Assumption:** Free static hosting that works for a personal tool will still work when the product is sold.
**Reality:** GitHub's documented Pages limits state it is not intended for, or allowed to be used as, free web hosting for an online business or commercial software as a service.
**Symptom:** Discovered at the point of monetisation, when hosting is the least interesting problem to be solving.
**Action:** Keep the frontend as plain static assets and the API's allowed origin config-driven, so migration to S3+CloudFront or similar is a deployment change rather than a rework. Migrate before charging.
**Confidence:** documented
**Refs:** §10.6, §8A

#### G-007 — Digital Asset Links must be at the domain root, which breaks GitHub Pages project sites
**Assumption:** Serving `.well-known/assetlinks.json` from the app's own repo is sufficient.
**Reality:** Verification reads `https://{domain}/.well-known/assetlinks.json` at the **domain root**. A GitHub Pages *project* site serves at `https://{user}.github.io/{repo}/`, so the well-known path resolves to the user-site repo instead.
**Symptom:** WebAPK link verification fails. NFC URL taps produce an app-disambiguation dialog rather than opening the app directly.
**Action:** Use a custom domain on the project repo, or serve the file from the `{user}.github.io` repo. Verify with `adb shell pm verify-app-links` — do not assume.
**Confidence:** documented
**Refs:** §10.6

#### G-008 — Web Bluetooth reconnection without a gesture is behind a Chrome flag
**Assumption:** A paired BLE device can be reconnected on page load.
**Reality:** `navigator.bluetooth.getDevices()` and `watchAdvertisements()` — the APIs that permit reconnecting to previously-permitted devices without a fresh user gesture — require `chrome://flags/#enable-web-bluetooth-new-permissions-backend`.
**Symptom:** `getDevices()` returns empty or is undefined, with no useful error.
**Action:** Acceptable for a personal deployment where you control the browser. Not shippable broadly. Feature-detect and report unavailable rather than failing.
**Confidence:** documented

#### G-009 — Web Bluetooth and media-key capture both require the page in the foreground
**Assumption:** A Bluetooth button can launch the app.
**Reality:** Both capture paths need the page already running and foregrounded. A BLE button cannot start an app that isn't open.
**Symptom:** Button works while testing with the app open; does nothing in the intended scenario.
**Action:** Divide the labour: NFC and voice for launching, Bluetooth for toggling an already-running app. Do not attempt to work around this.
**Confidence:** documented

#### G-059 — MediaRecorder's output sample rate is not something the page gets to choose
**Assumption:** Requesting 16kHz capture — the rate the STT model and the VAD both want — is a constraint you pass to `getUserMedia` or `MediaRecorder`.
**Reality:** The encoder follows the track's native rate, typically 48kHz on Android. A `sampleRate` constraint is advisory and widely ignored, and `MediaRecorder` exposes no rate option at all. Resampling requires routing through an `AudioContext` at the target rate, which is worth doing for the VAD's PCM path (Silero requires 16kHz) and pointless for the upload path (Whisper downsamples server-side anyway).
**Symptom:** Config says 16kHz, uploaded audio is 48kHz, and nothing errors. Either someone later "fixes" it by adding a transcode that costs compute and improves nothing, or a VAD fed the wrong rate produces silently wrong boundaries.
**Action:** Keep one sample-rate setting, scoped to the VAD path, and state in the config that the encoder has none. Assert the rate that actually arrives rather than the rate requested — the same discipline as G-004.
**Confidence:** reported
**Refs:** §Phase 1, §7.4

---

### Speech-to-text and models

#### G-010 — Whisper's prompt is capped at roughly 224 tokens
**Assumption:** Domain vocabulary can be supplied to the decoder at whatever length is needed.
**Reality:** Whisper's decoder context is 448 tokens with roughly half reserved for output, giving ~224 tokens of prompt — about 60–90 terms listed tersely. No retrieval scheme raises this ceiling; it only determines which terms occupy it.
**Symptom:** Terms beyond the limit have no effect. Depending on the provider this may truncate silently rather than error, so the failure is invisible.
**Action:** Budget the prompt explicitly, split between always-on high-frequency terms and topically-retrieved ones. Confirm the provider's overflow behaviour rather than assuming truncation.
**Confidence:** documented
**Refs:** §Phase 4.1

#### G-011 — Whisper prompt conditioning is inconsistent and must be measured, not assumed
**Assumption:** Supplying domain terms in the prompt reliably biases the decode toward them.
**Reality:** Whisper's prompt conditioning is known to work inconsistently in practice, and a hosted provider may transform, truncate, or ignore it.
**Symptom:** Corrections appear to work on some terms and not others, with no obvious pattern. Very easy to mistake for a rule-selection bug.
**Action:** Measure before building anything on it. Include a control arm with an irrelevant prompt of the same length — otherwise an apparent improvement may just be an artifact of prompting at all.
**Confidence:** reported
**Refs:** §0.3 A1

#### G-012 — Short audio clips transcribe worse than long ones
**Assumption:** Transcribing each utterance separately is equivalent to transcribing them together, and gives finer timestamps.
**Reality:** Whisper is trained on 30-second windows. A 2-second fragment loses the surrounding context the decoder uses to disambiguate, and transcribes measurably worse than the same words inside a longer window.
**Symptom:** Aggressive VAD segmentation makes transcription quality *worse* while appearing to save money. Both effects are real; the quality loss is easier to miss.
**Action:** Accumulate utterances to roughly the model's native window, cutting only at silence boundaries.
**Confidence:** documented

#### G-013 — Per-request minimum billing punishes short clips
**Assumption:** Audio is billed by duration, so shorter requests cost proportionally less.
**Reality:** Groq bills a 10-second minimum per request regardless of actual length. A 2-second clip costs the same as a 10-second one.
**Symptom:** Bill is many times the estimate derived from total audio duration.
**Action:** Batch short clips. This reinforces G-012 — the same segmentation policy fixes both.
**Confidence:** documented

#### G-014 — Cheap recorders' built-in voice activation clips word onsets
**Assumption:** A device with VOX saves storage and does useful segmentation for free.
**Reality:** Inexpensive VOX implementations trigger on detected speech with no pre-roll buffer, cutting the beginning of the first word.
**Symptom:** Transcripts consistently miss or mangle the first word of each utterance. Reads as a model problem, not a hardware one.
**Action:** Disable device VOX, record continuously, and segment with a VAD that has a proper pre-roll buffer. This applies equally to any VAD implementation: clipped onsets are worse than no VAD at all.
**Confidence:** reported

#### G-015 — Offline recorders lose their clock when the battery dies
**Assumption:** File timestamps are usable for ordering and dating.
**Reality:** Cheap recorders have no backup cell for the RTC. It resets to epoch or a fixed date whenever the battery fully drains.
**Symptom:** Imported content dated 1970, or all files sharing one implausible timestamp. File *ordering* remains correct.
**Action:** Store declared and resolved timestamps separately. Flag implausible values, prompt once for an anchor, derive the rest from file order plus durations.
**Confidence:** reported
**Refs:** §5A.4

#### G-042 — STT providers are not drop-in replacements for each other
**Assumption:** A provider abstraction makes swapping speech-to-text engines a config change.
**Reality:** Providers differ in capabilities the pipeline depends on, not just endpoints. Sarvam's transcribe endpoint accepts `file`, `model`, `mode`, `language_code`, and `input_audio_codec` — with **no prompt or vocabulary-biasing parameter at all**, where Whisper has one. Any correction architecture built on decode-time biasing simply does not transfer.
**Symptom:** The swap "works" — transcripts come back, nothing errors — but learned corrections silently stop being applied. Failure is invisible because the pipeline has no reason to complain.
**Action:** Have adapters declare capabilities explicitly, and make the pipeline branch on them with a test covering the degraded path. Never let an absent capability fail silently.
**Confidence:** documented
**Refs:** §7.1

#### G-043 — Timestamp granularity and sync/async shape differ between STT providers
**Assumption:** Transcription APIs return comparable structures.
**Reality:** Whisper returns segment and word timestamps from one synchronous call at any supported file size. Sarvam returns word-level arrays, caps synchronous REST at roughly 30 seconds of audio, and requires a five-step Batch API (initiate, upload, start, poll, download) beyond that.
**Symptom:** Alignment breaks, or long files fail with an opaque error while short test clips pass.
**Action:** Normalise to a common internal shape at the adapter boundary. Segments are derivable from word timestamps but not the reverse, so store the finest granularity available.
**Confidence:** documented

#### G-044 — STT pricing varies by an order of magnitude between providers
**Assumption:** Speech-to-text is a commodity priced within a narrow band.
**Reality:** Groq's Whisper turbo is ~$0.04/hour. Sarvam is roughly ₹30/hour to Rs 1.5/min depending on API and tier — approximately 9× to 27× more.
**Symptom:** A provider switch made on accuracy grounds multiplies the largest variable cost, discovered on the next invoice.
**Action:** Record `cost_per_hour_usd` in provider config and meter actual spend per provider. Evaluate accuracy and cost together — a more accurate provider may still pay for itself through reduced cleanup and correction load, but that must be measured rather than assumed in either direction.
**Confidence:** documented

#### G-045 — Published STT accuracy figures do not describe accented or code-mixed speech
**Assumption:** A model's headline word error rate predicts performance for your speaker.
**Reality:** Published figures are dominated by US and UK English. Error rates on Indian-accented English are materially higher, and mid-sentence code-switching degrades general-purpose models badly.
**Symptom:** Real-world accuracy falls well short of benchmarks, and the correction rule store grows much faster than planned — which in turn strains a bounded prompt budget.
**Action:** Benchmark on the actual speaker's voice from the beginning. Build the golden fixture set from their recordings, including code-switched samples if that occurs naturally. Treat a provider specialising in the relevant accent or language family as a serious candidate despite higher list price.
**Confidence:** reported
**Refs:** §1.2

---

### AWS

#### G-016 — The GitHub OIDC provider is account-global and singleton
**Assumption:** Each project's bootstrap stack creates its own OIDC provider.
**Reality:** `arn:aws:iam::{account}:oidc-provider/token.actions.githubusercontent.com` can exist only once per account. A second project declaring `AWS::IAM::OIDCProvider` fails.
**Symptom:** Bootstrap stack fails with "provider already exists" — but only in accounts that already host another project using GitHub Actions OIDC. Works perfectly in a clean account, so it survives testing and fails in production.
**Action:** Guard creation behind a CloudFormation `Condition`, detect existence in a preflight script, and reference the ARN constructed from account ID rather than `!GetAtt`.
**Confidence:** documented
**Refs:** §10.3

#### G-017 — IAM role names are account-global
**Assumption:** Resource names only need to be unique within a stack.
**Reality:** IAM role names are unique per account. A generic `RoleName` collides across projects.
**Symptom:** Stack creation fails on role creation, only in shared accounts.
**Action:** Prefix every IAM role name with the project. Mandatory, not stylistic.
**Confidence:** documented

#### G-018 — NAT Gateway is the largest avoidable cost in serverless AWS
**Assumption:** Putting Lambda in a VPC is a reasonable default for security.
**Reality:** A Lambda in a VPC needing internet access requires a NAT Gateway at roughly $32/month standing, regardless of traffic.
**Symptom:** A near-zero-cost serverless project bills $30+/month with no obvious driver.
**Action:** Do not place Lambda in a VPC unless something genuinely requires VPC networking. Most serverless applications do not.
**Confidence:** documented

#### G-019 — Secrets Manager costs more than a whole small stack
**Assumption:** Secrets Manager is the correct home for API keys.
**Reality:** $0.40 per secret per month, plus request charges. Three provider keys cost $1.20/month — more than everything else in a small serverless app combined.
**Symptom:** Secrets are the single largest line item on the bill.
**Action:** Use SSM Parameter Store Standard with `SecureString` under the AWS-managed key `alias/aws/ssm`, which is free. Reserve Secrets Manager for cases genuinely needing automatic rotation.
**Confidence:** documented

#### G-020 — A customer-managed KMS key has a standing monthly charge
**Assumption:** Encryption at rest with a CMK is free or negligible.
**Reality:** ~$1/month per CMK plus per-request charges. In a sub-$5 stack this is a significant fraction.
**Symptom:** Persistent monthly charge unrelated to usage volume.
**Action:** Use AWS-managed keys (`SSEEnabled`, `AES256`) unless a customer-managed key is actually required. Keep a `kms_key_id` indirection so switching later is a provisioning change, not a re-encryption. **Note the tradeoff:** crypto-shredding for data erasure requires a customer key.
**Confidence:** documented

#### G-021 — Object deletion does not reach backups, versions, or PITR snapshots
**Assumption:** Deleting objects deletes the data.
**Reality:** S3 versioning, DynamoDB PITR, and backups retain copies after an object-level delete. Only destroying the encryption key (crypto-shredding) makes data genuinely unrecoverable immediately.
**Symptom:** A "completed" erasure request leaves recoverable data. Discovered during audit, not during testing.
**Action:** Design erasure around key destruction where a customer-managed key exists. Where it does not, erasure means object deletion plus waiting out retention windows — state this honestly rather than overclaiming.
**Confidence:** documented

#### G-022 — CloudWatch alarms and AWS Budgets have small account-wide free allowances
**Assumption:** Adding alarms and a budget per project is free.
**Reality:** 10 alarms free per **account**, then ~$0.20/month each. 2 budgets free per account, then ~$0.02/day. Both allowances are shared across every project.
**Symptom:** Monitoring costs appear only after the sixth project, and are attributed to whichever project crossed the line.
**Action:** Prefer one account-level budget filtered by cost-allocation tag over per-project alarms. An alarm with no confirmed SNS subscription also emails into the void — worse than no alarm, because it looks like coverage.
**Confidence:** documented

#### G-023 — Cost allocation tags must be activated manually and do not backfill
**Assumption:** Tagging resources produces per-project cost data.
**Reality:** Tags must be explicitly activated as cost allocation tags in the Billing console, and only apply from activation onward. Historical data is not recoverable.
**Symptom:** Cost Explorer shows no breakdown by project tag despite everything being tagged correctly.
**Action:** Activate on day one, before deploying anything.
**Confidence:** documented

#### G-024 — Audio bytes cannot transit API Gateway or Lambda payloads
**Assumption:** Uploads can be posted to an API endpoint.
**Reality:** API Gateway caps payloads at 10MB; Lambda synchronous payloads at 6MB.
**Symptom:** Short recordings upload fine; longer ones fail with opaque 413 or invocation errors. Passes testing with short clips.
**Action:** Presigned S3 PUT from the client, S3 event to trigger processing. Pass the provider a presigned GET URL rather than moving bytes.
**Confidence:** documented

#### G-025 — OpenSearch Serverless has a minimum-capacity floor
**Assumption:** A serverless search service scales to zero cost at zero usage.
**Reality:** OpenSearch Serverless bills a minimum OCU allocation continuously. For a personal-scale corpus this dwarfs every other cost combined.
**Symptom:** Vector search becomes the entire bill.
**Action:** At personal scale (tens of thousands of vectors), store embeddings as a packed blob and brute-force cosine in-process. If that stops working, prefer pgvector on Aurora Serverless v2 scaled to zero.
**Confidence:** reported

#### G-026 — S3 Intelligent-Tiering charges per object monitored
**Assumption:** Intelligent-Tiering is a free optimisation.
**Reality:** A monitoring charge applies per 1,000 objects per month. With many small objects this can exceed the storage savings.
**Symptom:** Storage costs rise after enabling a cost optimisation.
**Action:** For many small objects, use Standard with explicit lifecycle rules instead.
**Confidence:** documented

#### G-027 — AWS Free Tier changed for accounts created after 15 July 2025
**Assumption:** Free tier works the way it did when your account was created.
**Reality:** Accounts created on or after 2025-07-15 use a credit-based Free Plan rather than the legacy 12-month tier. Always-Free allowances (Lambda 1M requests, DynamoDB 25GB) persist for everyone, but S3's allowance is documented as legacy-tier only — new accounts draw S3 from the credit pool.
**Symptom:** A project documented as "$0/month" bills a new contributor after credits lapse.
**Action:** Never publish an unqualified "$0/month" figure. State which allowances are Always-Free and which depend on account age.
**Confidence:** documented
**Refs:** §10.4

#### G-028 — Always-Free allowances are shared across all projects in an account
**Assumption:** Each project gets its own free tier.
**Reality:** The 1M Lambda requests and 25GB DynamoDB are account-wide, shared with every other project in the account.
**Symptom:** A project that fits comfortably in the free tier alone starts billing once deployed alongside others.
**Action:** Budget against total account usage, not per-project usage.
**Confidence:** documented

#### G-061 — An in-process vector index has to fit in the function it runs in
**Assumption:** Avoiding a vector database means the cost question is settled and the sizing question does not arise.
**Reality:** Brute-force cosine needs the whole matrix resident. At 1536 float32 dimensions each row is ~6KB, so 50,000 rows is ~307MB — past a 256MB function allocation and past the 512MB default `/tmp`. The approach is still right at this scale; it simply has a memory floor that grows linearly with the corpus and is invisible until crossed.
**Symptom:** Search works for months, then requests start failing as the corpus grows. An out-of-memory kill surfaces as a timeout or an opaque invocation error, not as "too much data", so it is diagnosed slowly and often blamed on the query path.
**Action:** Compute rows × dimensions × 4 bytes against the function's memory and ephemeral storage before building, size the function deliberately, and make the loader refuse loudly when the matrix exceeds its allocation instead of being killed mid-request.
**Confidence:** reported
**Refs:** §Phase 5, I7

---

### Third-party APIs

#### G-029 — Telegram Bot API caps file downloads at 20MB
**Assumption:** Any voice message sent to a bot can be retrieved.
**Reality:** The Bot API `getFile` download path is limited to 20MB.
**Symptom:** Long voice messages fail to download, with a generic error.
**Action:** Check size before download and reply with a clear message rather than failing silently.
**Confidence:** documented

#### G-030 — Android Auto messaging replies send text, not audio
**Assumption:** Replying to a message by voice in the car sends a voice message.
**Reality:** Android Auto's messaging reply transcribes on-device and sends text.
**Symptom:** A bot expecting audio receives text and does nothing.
**Action:** Accept text input as a first-class path, not a fallback. For in-car capture this is the *primary* path, not a degraded one.
**Confidence:** reported

### Agent access and security

#### G-046 — IAM permissions to create roles are a privilege escalation path
**Assumption:** Restricting an agent's own policy is sufficient to cap what it can do.
**Reality:** A principal that can create IAM roles and attach policies can create a more privileged role and use it. Deploying Lambda requires exactly this permission, so it cannot simply be removed.
**Symptom:** No symptom until it is exploited or a mistake compounds. The restriction appears to be working.
**Action:** Use a permissions boundary, attached to the agent principal *and* required on every role it creates. This caps effective privilege regardless of what policies get attached.
**Confidence:** documented
**Refs:** §9.5

#### G-047 — Tag-based access control does not cover every service or action
**Assumption:** ABAC conditions on `aws:RequestTag` and `aws:ResourceTag` protect everything uniformly.
**Reality:** Coverage varies by service. Some do not support tag-on-create, some do not support tag-based authorization for particular actions, and a condition on an unsupported action silently authorizes nothing — or everything, depending on how the statement is written.
**Symptom:** A resource is created untagged despite a deny that should have prevented it, or a deny blocks a legitimate action unexpectedly.
**Action:** Enumerate the services actually used and verify coverage per action rather than assuming. Report gaps explicitly so they are known and can be covered by naming-prefix denies instead.
**Confidence:** documented

#### G-048 — Write access to CI workflows is equivalent to access to deployment credentials
**Assumption:** Granting an agent repository write access is meaningfully less than granting it deployment access.
**Reality:** CI workflows run with credentials to deploy. Anything that can modify a workflow file can cause those credentials to be used or exfiltrated on the next run.
**Symptom:** None until abused. Looks like ordinary repository access.
**Action:** Require human review on `.github/workflows/**` via CODEOWNERS. The agent may propose workflow changes but must not merge them alone. The same reasoning applies to infrastructure definitions.
**Confidence:** reported
**Refs:** §9.6

#### G-049 — Classic GitHub tokens are org-wide regardless of intent
**Assumption:** A token created for one repository is limited to that repository.
**Reality:** Classic personal access tokens grant their scopes across every repository the user can access, including private ones in other organisations.
**Symptom:** None visible. The blast radius is invisible until something goes wrong in an unrelated repository.
**Action:** Use fine-grained tokens scoped to the single repository, with the minimum permission set. Never issue a classic token to an automated principal.
**Confidence:** documented

#### G-050 — An application that ingests untrusted content exposes its builder to prompt injection
**Assumption:** Prompt injection is a risk to the product's LLM pipeline, not to the agent building it.
**Reality:** An agent developing this system reads transcripts, imported audio, forwarded messages, web pages, and documentation — all of which can carry instructions. An agent holding cloud credentials is a far more valuable target than the product pipeline.
**Symptom:** Anomalous actions with no corresponding human instruction. Often indistinguishable from a mistake.
**Action:** Rely on the permissions boundary rather than on the agent recognising injection. Injected text cannot grant privileges the principal lacks. Keep credentials out of any context the agent processes — reference secrets by path, never by value.
**Confidence:** reported
**Refs:** §9.7

#### G-060 — A region-scoped deny blocks IAM, because global services have no region
**Assumption:** Denying every region except the deployment region confines an agent geographically without side effects.
**Reality:** `aws:RequestedRegion` is not present for global services. IAM, STS, CloudFront, Route 53, Budgets, and Organizations calls do not carry the key, so a `StringNotEquals` deny across `*` matches them and denies them. Deploying anything requires `iam:CreateRole`, so the boundary blocks its own first deploy. The mirror-image trap is writing the condition so that absent keys pass, which quietly exempts every global service from the control you thought you had.
**Symptom:** `AccessDenied` on `iam:CreateRole` during a deploy that has nothing to do with regions. The region deny is the last place anyone looks, because the failing action is not regional.
**Action:** Scope the region condition to regional services, or exempt global service prefixes explicitly with a comment saying why. Prove a real deploy succeeds *and* an out-of-region create fails — both directions, as in G-052.
**Confidence:** reported
**Refs:** §9.5, Phase 0 entry gate

---

### Process and design

#### G-035 — A service worker cache keyed on version tag ships new markup against old JavaScript
**Assumption:** Keying service worker cache names on the release version is sufficient to invalidate caches on deploy.
**Reality:** Caches must rotate on every *deploy*, not every *tag*. A deploy without a fresh tag produces a byte-identical `sw.js`, so the browser detects no worker update and install/activate never runs — installed PWAs keep serving previously-cached assets indefinitely. Meanwhile `index.html`, served stale-while-revalidate, *does* pick up the new markup.
**Symptom:** New HTML runs against old JavaScript, in installed PWAs only. The user cannot clear it: no update toast fires, because no update was detected. Never reproduces in a normal browser tab.
**Action:** Build the cache token from `{tag}-{short-sha}` so cache identity tracks content rather than release. Keep the clean tag for display; substitute the token only into `sw.js`.
**Confidence:** verified (encountered in `vppillai/passbook`)
**Refs:** §0.6

#### G-036 — Version tags are resolved at build time, so tagging after merge does not reach the artifact
**Assumption:** Tagging a released commit updates what the deployed app reports.
**Reality:** CI resolves `git describe` during the build. A tag pushed after the deploy workflow ran does not retroactively change the built artifact.
**Symptom:** Deployed app reports the previous version, or `dev`, despite the tag existing on the right commit.
**Action:** Tag before deploying, or re-run the deploy workflow after tagging.
**Confidence:** verified (encountered in `vppillai/passbook`)
**Refs:** §0.6

#### G-037 — A checked-in VERSION file drifts from the tags it duplicates
**Assumption:** A version file in the repo is a convenient, explicit source of truth.
**Reality:** It must be hand-synced with tags, and it will not be. In `passbook` it still read `v2.6.0` at the `v2.7.0` release, and was never actually read once the first tag landed.
**Symptom:** Displayed version silently lags the real release.
**Action:** Derive from `git describe --tags`. Keep a file only as an optional fallback for forks with no tags, never as the primary source.
**Confidence:** verified (encountered in `vppillai/passbook`)


#### G-038 — Immutability requirements collide with right-to-erasure
**Assumption:** "Never delete X" and "delete everything on request" can both be invariants.
**Reality:** They conflict directly and the conflict must be resolved deliberately, not discovered when the first erasure request arrives.
**Symptom:** Erasure implementation is blocked by an invariant, and someone weakens the invariant under time pressure.
**Action:** Scope immutability to "never deleted by application code," and carve out a single separately-permissioned erasure path that writes an audit record before executing.
**Confidence:** reported

#### G-039 — A correction rule store degrades in precision as it grows
**Assumption:** More learned corrections means better output.
**Reality:** Phonetic keys carry no semantics. A rule that is correct in one context misfires in another, rules collide, and stale rules keep firing after their terminology dies. These get worse with scale, silently.
**Symptom:** Corrections that used to be right start being wrong, in contexts unrelated to where they were learned.
**Action:** Make rules topic-conditional, and track per-rule precision using the user's own subsequent edits as ground truth — if a correction is reverted, the rule was wrong. Auto-demote below a threshold.
**Confidence:** reported
**Refs:** spec Phase 4

#### G-040 — Uniform processing destroys content types that need preserving
**Assumption:** A cleanup-and-summarise pass improves all captured content.
**Reality:** Some content exists to be used verbatim later. Summarising it destroys the artifact while appearing to succeed.
**Symptom:** Not noticed for months, then discovered when the content is needed in full and only a summary remains. Unrecoverable if the original was not retained.
**Action:** Classify content by kind and drive processing from it. Retain originals regardless. Make the "never summarise this kind" rule a test, not a prompt instruction.
**Confidence:** reported
**Refs:** §3A.3

#### G-051 — Components validated in isolation still fail at the seams
**Assumption:** If each dependency has been proven to work, the pipeline built from them will work.
**Reality:** Most integration failures live at the boundaries — format mismatches, auth propagation, payload size limits, timeout interactions, encoding assumptions. Every component can pass its own spike while the assembly fails.
**Symptom:** Individual spikes all succeeded, but the first end-to-end run fails, and the cause sits between two things that were each verified.
**Action:** Include an end-to-end smoke test in every phase's entry gate, exercising each seam once before real features are built on it.
**Confidence:** reported
**Refs:** §0.2

#### G-052 — Restrictive IAM policies block legitimate work as often as they prevent damage
**Assumption:** The risk in a tight permissions boundary is that it might be too permissive.
**Reality:** Over-restriction is at least as common, and far more expensive in wasted time — it surfaces mid-task as a confusing `AccessDenied` on an action that should obviously be allowed, often through several layers of tooling that obscure the real cause.
**Symptom:** Deploys fail with permission errors on routine operations. Time is lost diagnosing the application before suspecting the policy.
**Action:** Prove the boundary permits a real deploy *and* blocks the intended actions, both directions, before any feature work depends on it.
**Confidence:** reported
**Refs:** §9.5, Phase 0 entry gate

#### G-055 — A configurable brand name is still expensive to change once users have installed
**Assumption:** Parameterising the display name makes rebranding a config edit.
**Reality:** The string is configurable; the consequences are not. A manifest name change re-mints the WebAPK, and voice launch is trained muscle memory — a user who has said "open X" for six months will experience the rename as the app being broken, not renamed. The new name also has to re-clear fuzzy-match testing against whatever else is installed (G-005).
**Symptom:** After a rebrand, voice launch silently opens a different app, and installed users report the app "disappearing."
**Action:** Parameterise it anyway — hardcoding is worse — but choose deliberately, and treat a post-launch rename as a migration with user communication, not a config edit.
**Confidence:** reported
**Refs:** §7.3

#### G-056 — Naming infrastructure after the brand turns a rebrand into a migration
**Assumption:** Resource names, tags, and parameter paths should match the product name for consistency.
**Reality:** DynamoDB tables cannot be renamed, IAM role names are effectively immutable, and cost-allocation tags do not backfill (G-023). If the infrastructure namespace is the brand, any rebrand requires recreating and migrating everything, or living with a permanent mismatch.
**Symptom:** A marketing decision blocks on an infrastructure migration nobody budgeted for.
**Action:** Keep a separate, descriptive, frozen system identifier that users never see. The brand can then change freely.
**Confidence:** reported
**Refs:** §7.3

#### G-053 — Subagents violate constraints they were never shown
**Assumption:** Giving a subagent its specific task and the relevant section is sufficient context.
**Reality:** Constraints that apply across a whole system — immutability rules, tenant scoping, key-construction helpers, encoding assumptions — are invisible from inside a narrow slice. A subagent cannot know which global rules apply to its task until it is already mid-task and has made an assumption.
**Symptom:** Work returns looking correct and passing its own tests, while quietly breaking a cross-cutting rule. Found much later, often by an integrity check rather than a test.
**Action:** Include the full invariant list in every brief, not the subset that appears relevant. Back it with mechanical checks so compliance does not depend on the subagent noticing.
**Confidence:** reported
**Refs:** §0.7.4

#### G-054 — Stripping rationale from a brief invites the constraint to be optimised away
**Assumption:** A subagent needs the requirement, not the reasoning; the reasoning is context bloat.
**Reality:** A requirement without its justification reads as an arbitrary constant. A competent agent will improve it — tightening a buffer, shortening a window, simplifying an interface — because nothing indicates the value was chosen against a constraint it cannot see.
**Symptom:** A parameter drifts to a "cleaner" value and quality regresses in a way that is hard to attribute.
**Action:** Carry the inline rationale into the brief verbatim. It is cheaper than the regression.
**Confidence:** reported
**Refs:** §0.7.4

#### G-062 — A self-test that asserts "the check fails when broken" can pass without testing anything
**Assumption:** A check that verifies its own failure path is trustworthy, because it demonstrably fails when the guardrail is removed.
**Reality:** "It failed" and "it failed *for this reason*" are different claims. If anything else in the check is also broken, the inner run fails for the unrelated reason and the self-test reads that as successful detection. Encountered here: `guardrails-check.sh --self-test` reported pass while its `REPO_ROOT` override was being silently ignored, so the doctored tree was never inspected at all — the real repository was, and it happened to be failing on an unrelated bug.
**Symptom:** The self-test passes. Fixing an unrelated bug elsewhere makes it start failing, which looks like a regression and is actually the first honest result it has ever produced.
**Action:** Any check asserting "X fails when broken" must also assert **"X passes when not broken"** — verify the control case before drawing a conclusion from the failure case. Prove the boundary in both directions, which is the same discipline G-052 requires of IAM policies and A1's spike design requires of the irrelevant-prompt control arm.
**Confidence:** verified (encountered building this pipeline)
**Refs:** §0.5A, docs/findings/F-0001-checks-demonstrated-red.md

#### G-063 — `git ls-files` lists only committed files, so a check built on it cannot see the change being checked
**Assumption:** Selecting files with `git ls-files` gives a check the repository's contents while excluding generated artifacts and ignored paths — the right file set, cheaply.
**Reality:** A bare `git ls-files` lists the **index**, which means committed files only. A newly added file is untracked and therefore invisible. So a static check that iterates it inspects everything *except* the work in progress — and reports green on the very change it exists to reject. Encountered here on `check-tenant-keys.sh`, the I11 enforcement check: a new handler building `"TENANT#" + id` by hand was not detected at all. CI still catches it after the commit, because a checkout tracks everything, which makes the local signal wrong while the remote one is right — the most confusing possible split.
**Symptom:** The check passes locally on a change that violates it, then fails in CI after commit — or passes in both, if the violating file was committed in the same change that added the check. On an empty repository with no commits at all, `ls-files` returns nothing and **every** file-iterating check passes vacuously.
**Action:** Use `git ls-files --cached --others --exclude-standard` — tracked, plus untracked, minus ignored. And demonstrate the check red by *adding a new file* that violates it, not by editing an existing one: editing a tracked file cannot expose this bug, so a red demonstration done that way looks successful while leaving the gap in place.
**Confidence:** verified (encountered building this pipeline)
**Refs:** §0.5A, I11, [[G-062]], docs/findings/F-0001-checks-demonstrated-red.md

#### G-041 — Manual sync steps decay
**Assumption:** A daily manual step is acceptable if the value is high.
**Reality:** Daily manual steps erode within weeks unless they are close to frictionless.
**Symptom:** A feature is used enthusiastically for two weeks and then abandoned; the device gathers dust.
**Action:** Budget the interaction cost explicitly. If import takes more than one tap, expect it to stop happening. This applies to review and triage queues equally.
**Confidence:** reported
