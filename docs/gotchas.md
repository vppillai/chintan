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

**IDs are permanent labels, not an ordering.** Entries are grouped by category for reading; within a category the numbers run out of sequence, and some numbers are absent, because IDs are assigned when a gotcha is discovered and never reassigned afterwards. A gap is not a missing entry — it is an ID that was never used or whose entry was merged. **Never renumber, and never reuse a number**, in this section or in `docs/gotchas.md`: these IDs are cited from the spec body, from findings, and from commit messages, and a renumbering silently redirects every one of those references. Eighty-seven entries are recorded here; the next new one is `G-092`.

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
**Observed:** This produced a genuinely red `main` here, independently of the staged demonstration that found it. `make check` passed locally on a commit adding a new script; CI then failed the formatting gate on that same script, because the file was uncommitted locally (invisible) and committed in CI (visible). The two systems disagreed about the contents of the repository, which is not a category of disagreement anyone thinks to suspect.
**Action:** Use `git ls-files --cached --others --exclude-standard` — tracked, plus untracked, minus ignored. And demonstrate the check red by *adding a new file* that violates it, not by editing an existing one: editing a tracked file cannot expose this bug, so a red demonstration done that way looks successful while leaving the gap in place.
**Confidence:** verified (encountered building this pipeline)
**Refs:** §0.5A, I11, [[G-062]], docs/findings/F-0001-checks-demonstrated-red.md

#### G-064 — The account root user cannot assume any IAM role
**Assumption:** Create a role with a permissions boundary, trust the account principal, and an administrator can assume it to get short-lived boundary-limited credentials. This is the shape §9.5 prefers — "a dedicated role assumed via short-lived credentials".
**Reality:** AWS refuses outright: `AccessDenied: Roles may not be assumed by root accounts.` The trust policy is irrelevant; the restriction is on the caller. So on an account whose only credentials are root, and with no IAM Identity Center configured, **there is no path from root to boundary-limited credentials that does not pass through an IAM user.** An IAM user must be created, and it must hold a long-lived access key — the thing the role design was chosen to avoid.
**Symptom:** Bootstrap completes, every policy validates, the role exists with its boundary correctly attached — and the first `sts:AssumeRole` fails. Nothing about the error suggests the caller's identity type is the problem, so the trust policy is the first and second place anyone looks.
**Action:** Create a minimal IAM user whose only permission is `sts:AssumeRole` on the one role, with the boundary attached to it as well so it can never be granted more. The stored key then buys nothing except a bounded session, and the credentials doing actual work expire on their own. Scope the role's trust policy to that user specifically rather than to the account principal — naming the account root buys nothing, since root cannot use it.
**Confidence:** verified (encountered bootstrapping this account)
**Refs:** §9.5, §0.8 item 1, [[G-065]], docs/findings/F-0002-agent-boundary-bootstrap.md

#### G-065 — A permissions boundary containing a blanket deny can lock a principal out of the action it exists to perform
**Assumption:** A boundary's denies are a safety net. Adding one more deny narrows the ceiling and cannot break anything the principal legitimately needs.
**Reality:** A boundary applies to **every** principal it is attached to, and its denies override the grants. A `Deny sts:AssumeRole on "*"` written to stop the *role* chaining onward to a more privileged role also denied the *user* the single action it existed for — assuming that role — because both carry the same boundary. The guardrail blocked the only way in.
**Symptom:** `AccessDenied` on an action whose grant is plainly present in the identity policy, sitting right there in the console. Because a boundary is evaluated separately from the attached policies, the policy that is actually refusing is not the policy anyone is reading. Exactly G-052's shape: over-restriction surfacing mid-task as a confusing denial on something that should obviously be allowed.
**Action:** Scope a role-chaining deny with `NotResource` naming the permitted role, rather than `Resource: "*"`. More generally: after writing any boundary, prove it permits a real end-to-end operation as well as blocking the intended ones — both directions, as G-052 requires. A boundary tested only for what it blocks is half-tested.
**Confidence:** verified (encountered bootstrapping this account)
**Refs:** §9.5, [[G-052]], [[G-064]]

#### G-066 — A wildcard in the service position of an IAM action is invalid, so §9.5's ABAC snippet cannot be created
**Assumption:** `"Action": ["*:Create*", "*:Run*"]` denies every create-like action across every service — the form the spec's own §9.5 ABAC example uses.
**Reality:** IAM rejects the policy outright: `MalformedPolicyDocument: Action vendors (e.g., aws, ec2, etc.) must not contain wildcards.` A wildcard is permitted in the *action* part (`s3:Delete*`) and as the whole value (`"*"`), but never in the vendor part. §9.5 already warns its snippet is "a template, not a policy to paste unchanged" and gives two reasons; this is a third one it does not mention, and it blocks the bootstrap before either of the others can matter.
**Symptom:** Fails loudly at policy-creation time, which is the good outcome — the alternative would have been a deny that matched nothing while appearing to be in force. Nothing is created, so there is no half-configured state.
**Action:** Enumerate per-service action prefixes (`dynamodb:Delete*`, `s3:Put*`, `iam:Attach*`, …). This is more verbose but it is also what G-047 asks for — enumerate the services actually in use rather than presuming uniform coverage. Validate every policy document through IAM Access Analyzer before creating it; it catches this, and it catches non-existent service namespaces such as `emr:*`, `efs:*`, `neptune:*`, and `docdb:*`.
**Confidence:** verified (observed against the live IAM API)
**Refs:** §9.5, [[G-047]], docs/findings/F-0002-agent-boundary-bootstrap.md

#### G-067 — `aws:ResourceTag` authorization is unsupported for most services this project uses, so a tag-based deny is decorative
**Assumption:** An ABAC deny conditioned on `aws:ResourceTag/Project` protects every resource belonging to another project, as §9.5's `ModifyOnlyOwnedResources` statement intends.
**Reality:** IAM Access Analyzer reports the condition key as unsupported for authorization on **cloudformation, cognito-idp, dynamodb, events, iam, lambda, logs, resource-groups, s3, and ssm** — which is very nearly every service in use — and states plainly: "The actions for the listed services are not denied by this statement." The deny is present, reads as protective, and does nothing for those services. G-047 predicts the category; the extent is the surprise.
**Symptom:** None. The statement exists, the console shows it, and it never fires. Discoverable only by running the policy through a validator or by testing each denial.
**Action:** Treat **naming-prefix denies and resource-ARN scoping as the real control** — those are ARN-based and always supported. Keep the tag conditions as defence in depth for the services that do support them, but name the statement so it does not claim more than it delivers: a statement named as though it enforces something it cannot is how a guardrail gets trusted while absent (§9.8). Run every deny against the live API and assert it fires.
**Confidence:** verified (IAM Access Analyzer, and per-action testing against the live API)
**Refs:** §9.5, [[G-047]], [[G-066]]

#### G-068 — `ForAnyValue:StringNotEquals` on `aws:CalledVia` does not fire on a direct call, which is the case it was written for
**Assumption:** To require that an action only ever reach a resource through CloudFormation, deny it when `aws:CalledVia` is not CloudFormation: `ForAnyValue:StringNotEquals: {aws:CalledVia: [cloudformation.amazonaws.com]}`.
**Reality:** A **direct** API call carries no `aws:CalledVia` key at all — the key exists only when another service is calling on your behalf. `ForAnyValue:*` evaluates to **false** for an absent key, because there are no values for any of which to hold. So the deny is skipped on exactly the direct call it exists to block, and applies only when some *other* service is the caller. Precisely backwards. Use `ForAllValues:StringNotEquals`, which returns **true** for an absent key.
**Symptom:** None. The statement is present, names the right actions, and reads correctly. The direct call succeeds. Worse, `iam:simulate-principal-policy` agrees it is allowed, so a simulation-based test confirms the wrong answer with apparent authority.
**Action:** `ForAllValues` when an absent key must be treated as a violation; `ForAnyValue` when it must not. Then test the absent-key case explicitly — that is the case the operators differ on, and it is the one that matters. This is [[G-060]] mirrored: there a naive condition denied global services, here a naive condition exempted direct calls. Both come from not asking what happens when the key is missing.
**Confidence:** verified (observed against the live IAM API and the policy simulator)
**Refs:** §9.5, [[G-060]], [[G-067]], docs/findings/F-0002-agent-boundary-bootstrap.md

#### G-069 — `iam:simulate-principal-policy` populates no condition context, so conditional denies over-match and reads are worthless without it
**Assumption:** The policy simulator evaluates a request the way IAM would, so its verdict can be trusted as a non-destructive substitute for attempting the action.
**Reality:** The simulator supplies **no condition context of its own**. `aws:RequestedRegion`, `aws:RequestTag/*`, and `aws:CalledVia` are all absent unless passed with `--context-entries`. A region deny written as `StringNotEquals: {aws:RequestedRegion: <region>}` therefore matches *every* request in simulation, and the simulator reports `explicitDeny` for routine, obviously-permitted operations.
**Symptom:** Wholesale `explicitDeny` on a policy that demonstrably works against the live API — `s3:PutObject`, `dynamodb:PutItem`, `cloudformation:CreateStack` all denied in simulation minutes after a real deploy succeeded. Reads as a broken boundary and invites "fixing" a policy that is correct.
**Action:** Pass every condition key the policy references, and pass them per scenario — the same action must be simulated with `aws:CalledVia` present and absent to test both branches of a CloudFormation-only deny. Treat the simulator as a way to enumerate scenarios cheaply, and the live API as the authority.
**Confidence:** verified (encountered testing this boundary)
**Refs:** §9.5, [[G-068]], docs/findings/F-0002-agent-boundary-bootstrap.md

#### G-070 — Testing a deny by attempting the action deletes the resource when the deny does not work
**Assumption:** The safe way to verify a guardrail is to attempt the forbidden action and check that it fails — §9.5's own gate says "attempt each, assert each fails."
**Reality:** That is exactly right for *creates* and for reads, and dangerous for *deletes*. If the deny does not work, the action succeeds, and for a delete the verification destroys the thing it was checking. Encountered here: a probe of the `Protected=true` deletion deny ran `s3api delete-bucket` against the live artifact bucket. The deny did not work — `aws:ResourceTag` is unsupported for S3 authorization ([[G-067]]) — so the bucket was deleted. It was empty, so nothing was lost, but the CloudFormation stack was left drifted, believing a resource that no longer existed.
**Symptom:** The test reports "NOT DENIED", which is the correct finding, arrived at by causing the damage the guardrail existed to prevent. On a resource holding data the finding would be unrecoverable.
**Action:** Verify destructive denies with `iam:simulate-principal-policy` (read-only) or against a throwaway resource created for the purpose — never against a live one. Keep attempt-the-action testing for creates, reads, and cross-project probes, where a working deny and a broken one differ only in an error message. And read [[G-069]] first: the simulator needs its condition context supplied or it will confirm whatever you feared.
**Confidence:** verified (encountered, with consequences, testing this boundary)
**Refs:** §9.5, §Phase 0 entry gate, [[G-067]], [[G-069]]

#### G-071 — GitHub now issues immutable OIDC subjects with numeric ids embedded, so the documented trust-policy form does not match
**Assumption:** An AWS role trusting GitHub Actions conditions on `token.actions.githubusercontent.com:sub` equal to `repo:OWNER/REPO:environment:NAME`. This is the form AWS documents, every example shows, and `vppillai/passbook`'s own bootstrap uses in the same account.
**Reality:** GitHub embeds the numeric owner and repository ids in the subject: `repo:vppillai@3634378/chintan@1323409209:environment:production`. The documented form no longer matches. And IAM **refuses** a GitHub OIDC trust policy that does not condition on `sub` or `job_workflow_ref` at all, so the individual immutable claims cannot simply replace it: *"Trust policy ... must evaluate, using StringEquals, StringLike or StringEqualsIgnoreCase, token.actions.githubusercontent.com:sub or token.actions.githubusercontent.com:job_workflow_ref."*
**Symptom:** `Could not assume role with OIDC: Not authorized to perform sts:AssumeRoleWithWebIdentity`, retried twelve times over two minutes. The error names neither the expected nor the presented subject, so the trust policy — which is correct by every published example — is the first and second place anyone looks. Worse in a shared account: a sibling project's role using the old form still works, so the precedent next door actively misleads.
**Action:** Condition on `repository_owner_id`, `repository_id`, and `environment` with `StringEquals` — these are immutable and survive a rename, which is why GitHub started embedding them — **plus** a `StringLike` on `sub` with wildcards placed exactly where the ids appear (`repo:owner*/repo*:environment:name`) to satisfy IAM's requirement. That pattern also matches the older format, since a trailing `*` matches empty. Resolve the ids from the API rather than hand-editing them. And when an OIDC assume fails, print the presented `sub` and `aud` before theorising: both are claims about which workflow is running, not secrets.
**Confidence:** verified (observed against live GitHub Actions and IAM)
**Refs:** §9.6, §10.1, docs/findings/F-0003-first-deploy-through-ci.md

#### G-072 — An S3 notification to a Lambda whose role references the bucket is a circular dependency
**Assumption:** A template can declare a bucket that notifies a function, and give that function's role permission on the bucket. Both directions are ordinary CloudFormation.
**Reality:** Together they are a cycle: bucket → (notification) → function → (role) → bucket ARN. CloudFormation rejects the whole template, and the error lists every resource in the cycle without naming a single edge — nine resources here, most of them uninvolved in the actual loop. Environment variables close the same loop: a `!Ref` to the bucket on the function is the same edge as the role's `!GetAtt`.
**Symptom:** `ValidationError: Circular dependency between resources: [DataBucket, WorkerFunction, ApiIntegration, ApiRole, WorkerInvokePermission, DefaultRoute, ApiFunction, WorkerRole, ApiInvokePermission]`. Reads as though the template is deeply tangled when one reference is at fault.
**Action:** Construct the bucket name and ARN with `!Sub` from the values that determine it — instance, account, region — instead of deriving them with `!Ref`/`!GetAtt`. This is exact rather than approximate when the name is deterministic, and it removes the dependency edge. The tell that this is the intended pattern: the `AWS::Lambda::Permission` for the notification *already* has to construct its `SourceArn` this way, for precisely the same reason.
**Confidence:** verified (encountered on the first deploy of this template)
**Refs:** §6.2, docs/findings/F-0003-first-deploy-through-ci.md

#### G-073 — `lambda.Start` accepts a value of any type and fails at the first request, not at start-up
**Assumption:** Passing the wrong thing to `lambda.Start` is a compile error, or at worst a start-up failure.
**Reality:** Its parameter is `any`, so a handler struct compiles and initialises happily. The type is checked on the first invocation, which fails with `handler kind ptr is not func` — after the cold start has already run and logged whatever it logs.
**Symptom:** The deploy succeeds, the cold-start log shows config loaded and the process reporting ready, and then every request returns 500. Because start-up looked clean, the two things anyone checks first — configuration and IAM permissions — are both fine, and the log says nothing about the real cause.
**Action:** Pass a bound method value (`h.Handle`), not the receiver. Assert it in a test: `lambda.NewHandler` panics on a non-function, so a three-line test turns an in-production 500 into a build failure. And smoke-test a real endpoint at the end of every deploy — a stack that deploys while the function cannot serve is otherwise indistinguishable from a working one.
**Confidence:** verified (encountered on the first deploy)
**Refs:** §4, §0.6, docs/findings/F-0003-first-deploy-through-ci.md

#### G-074 — DynamoDB destroys the Go type of a whole number, and the in-memory fake cannot reproduce it
**Assumption:** The in-memory Repository fake is a faithful behavioural specification, so a test passing
against it means the DynamoDB adapter behaves the same way.
**Reality:** DynamoDB has one number type and carries it on the wire as a decimal string, so the Go
type of a number is not recoverable. float64(3) is stored as "3" and must come back as
either int64(3) or float64(3) — it cannot be both. Money forces the int64 choice (meter
asserts .(int64) on cost_micros directly, and float64 cannot represent every int64),
which means a float64 attribute whose value happens to be whole comes back as int64. The
same applies to element types: []string goes out as a list of strings and comes back as
[]any. The fake round-trips the exact Go value it was given, so it can never show
either.
**Symptom:** `v, ok := item.Attrs["quantity"].(float64)` yields (0, false) in production and (3,
true) in every test — a metering quantity or a confidence score silently reads as zero.
A `.([]string)` assertion on a stored list panics or fails only in production.
**Action:** Read number attributes through repository.AsInt64/AsFloat64, never with a direct type
assertion, and read list attributes as []any. Better still, make the fake normalise on
read the way the adapter does, so the divergence stops existing — see the open question
about that.
**Confidence:** verified (encountered building Phase 0)

#### G-075 — A DynamoDB TTL attribute written with its Go zero value expires the item immediately
**Assumption:** Writing the TTL field unconditionally is harmless: 0 means "no expiry", so the attribute
just says nothing.
**Reality:** The TTL attribute is an absolute epoch second. 0 is 1 January 1970, which is in the
past, so the item becomes eligible for TTL deletion the moment it is written. "No
expiry" is expressed by the attribute being absent, not by it being zero.
**Symptom:** Every mutable record — captures, items, threads — silently disappears within DynamoDB's
TTL deletion window (up to 48 hours) after being written. Nothing errors, nothing logs,
and the loss is delayed enough to look unrelated to the write.
**Action:** Only ever write a TTL attribute when the value is strictly positive. In this codebase
repository.marshalItem does that and refuses a negative TTL; anything that writes
DynamoDB items outside that adapter needs the same rule.
**Confidence:** verified (encountered building Phase 0)

#### G-076 — Writing one of a GSI's two key attributes produces no error and no index entry
**Assumption:** If GSI1PK or GSI1SK is wrong or missing, DynamoDB will reject the write or the index
will be obviously broken.
**Reality:** A sparse GSI projects an item only when it carries every index key attribute. An item
with GSI1PK and no GSI1SK is stored successfully, is readable by key, and never appears
in the index. There is no service error at any point.
**Symptom:** A capture is created, is fetchable by ID, and never appears in GET /v1/captures. The
user reports "my recording vanished" while the record is sitting in the table, and only
a describe-table plus an item dump distinguishes it from a genuinely lost write.
**Action:** Refuse the half-populated pair at the write boundary — repository.marshalItem errors
when exactly one of GSI1PK/GSI1SK is set. Any new sparse index needs the same
both-or-neither check on the write path, since there is nothing downstream that can
detect it.
**Confidence:** verified (encountered building Phase 0)

#### G-077 — An empty DynamoDB Query page with a LastEvaluatedKey is normal, not the end of the results
**Assumption:** Pagination ends when a page comes back with no items, so `if len(page.Items) == 0 {
break }` is a safe loop condition.
**Reality:** DynamoDB stops reading at the 1MB boundary, not at an item boundary, so a page can
legitimately contain zero items and still carry a LastEvaluatedKey with more results
behind it. The only correct termination condition is an absent LastEvaluatedKey.
**Symptom:** A query silently returns fewer records than exist. For usage records this makes
MonthTotal and DayTotal under-count, so the §10.5.9 daily spend breaker computes a
plausible figure that is too low and the cap stops holding — no error, no log, just a
number that is wrong in the safe-looking direction.
**Action:** Loop on `len(page.LastEvaluatedKey) != 0` and never on page emptiness, and return no
partial result when a page fails. Both are asserted in
TestQueryPrefixPaginatesToExhaustion and
TestQueryPrefixReturnsNothingRatherThanAPartialResultOnError.
**Confidence:** verified (encountered building Phase 0)

#### G-078 — A conditional PutItem is not retry-safe: the SDK's own retry turns your committed write into a conflict
**Assumption:** A conditional write is idempotent, so letting the AWS SDK retry it is harmless.
**Reality:** If the PutItem commits and its response is lost (network blip, 5xx, throttle), the
standard retryer re-sends the identical request; the condition now fails against the
item it just created, and the caller sees ConditionalCheckFailedException. A reservation
record with nothing attempt-specific in it is then indistinguishable from another
caller's reservation — the creator reads back its own row and concludes someone else
holds the key.
**Symptom:** One specific idempotency key answers 409 Conflict on every attempt for the whole TTL
(24h) while every other request works. Looks like a stuck worker or a client bug; there
is no self-heal and no log line, because from the server's point of view the code did
the right thing.
**Action:** Put something per-attempt in the row — a 128-bit random token minted once per logical
call — and treat "stored token == mine" as proof of authorship, returning "new" rather
than "in flight". Fail the call if the token cannot be generated; never substitute a
fixed value, because then every attempt recognises every row as its own. See
backend/internal/idem (Begin, attrAttempt) and
TestACommittedReservationIsRecognisedByTheAttemptThatWroteIt.
**Confidence:** verified (encountered building Phase 0)

#### G-079 — A job-level `permissions` block replaces the workflow-level one, so a scope granted once is not granted everywhere
**Assumption:** The workflow-level `permissions:` block is a floor every job inherits, and a job that
declares its own adds to it. So `pages: write` on the job that deploys is enough for a
Pages workflow.
**Reality:** A job-level block REPLACES the workflow-level block entirely. A job that declares none
inherits the workflow block verbatim; a job that declares one gets exactly what it lists
and nothing more. In a two-job Pages workflow the build job runs configure-pages and
upload, and with no `pages` scope it cannot even READ the Pages site — GET
/repos/{owner}/{repo}/pages returns 403 "Resource not accessible by integration".
Compounding it: `actions/configure-pages` with `enablement: true` can never work with
GITHUB_TOKEN at all. Its action.yml states the input needs `administration:write` and
`pages:write`, and `administration` is not a grantable GITHUB_TOKEN scope, so the create
call can only 403.
**Symptom:** The build job fails at configure-pages, deploy never runs, and nothing is ever published
— while the workflow's own text reads as though the permission was granted, because the
word appears on the other job. It fails identically when Pages HAS been enabled by hand,
since with `pages: none` even the metadata read 403s. `enablement: true` makes the
failure look like a Pages-configuration problem rather than a token-scope one.
**Action:** Restate every scope a job needs on that job, including the ones the workflow already
grants, and comment the workflow-level block as a default for jobs added later rather
than as an inherited floor. Do not use `enablement: true` with GITHUB_TOKEN: enable
Pages once by hand or by one authenticated POST, and keep configure-pages purely as the
precondition check that fails with an actionable message when it is off. Assert the
shape mechanically — `yq -e '.jobs.<job>.permissions.pages'` — because this defect is
invisible from reading the file.
**Confidence:** verified (encountered building Phase 0)

#### G-080 — float64 to int64 conversion is architecture-dependent in Go, and the direction differs between CI and Lambda
**Assumption:** An out-of-range float64 converted to int64 either wraps or is caught by the surrounding
validation, and CI on x86 exercises the same behaviour as production.
**Reality:** The result is implementation-defined. arm64 saturates to math.MaxInt64; amd64 wraps to
math.MinInt64. §10.1 mandates Architectures: [arm64] for the Lambdas, while developer
machines and x86 CI runners do the opposite — so a money boundary with no range check
silently produces an unreachable ceiling in production and a rejected value everywhere
it would be noticed.
**Symptom:** A cap or limit derived from a config float (e.g. a mistyped `daily_spend_usd: 2.0e15`)
deploys successfully and never fires; the same config fails validation locally, so the
discrepancy is read as a local environment problem. No error, no log line, and the
provider bill is the first signal.
**Action:** Range-check the float *input* before converting, never the converted result — the
saturated value is indistinguishable from a legitimate one on the architecture that
produced it. Test the boundary by asserting a refusal (architecture-independent), not by
asserting the converted number.
**Confidence:** verified (encountered building Phase 0)

#### G-081 — sync.Mutex ignores context deadlines: blocking is not refusing
**Assumption:** A fail-closed check that holds a mutex across a slow dependency degrades safely —
waiters block, and a blocked waiter admits nothing, so the degenerate case is still
closed.
**Reality:** sync.Mutex is not context-aware. A goroutine waiting on it ignores its own deadline, so
it produces no error, no refusal value, no log line, and no return to its caller until
the lock is free. A process-global lock also couples unrelated tenants or requests to
one slow dependency.
**Symptom:** A storage latency spike turns into a Lambda timeout for the whole SQS batch. Because the
guard emitted nothing for the waiters, the incident presents as an unexplained worker
timeout rather than as the guard's own "could not determine state" refusal — the
opposite of the diagnosability the log line exists to provide.
**Action:** Serialise with a 1-buffered channel acquired via `select` on `ctx.Done()`, scoped per
tenant rather than per process, and return the same explicit refusal (with the context
error as the cause) that the dependency's own failure would produce. Attempt the
non-blocking send first so a free gate beats an already-cancelled context
deterministically. Assert it with a test that holds the gate and passes a dead context.
**Confidence:** verified (encountered building Phase 0)

#### G-082 — Bash 5.2 expands `&` in a ${var//pat/rep} replacement, so a config value replaces a placeholder with itself
**Assumption:** `${content//"{{KEY}}"/$value}` is literal text substitution, and quoting the pattern is
the only care required.
**Reality:** Bash 5.2 enables `patsub_replacement` by default: an unquoted `&` in the REPLACEMENT
expands to the text that matched the pattern. A value containing `&` — `Capture & find
your thinking.` is an ordinary tagline — replaces `{{KEY}}` with `{{KEY}}`. The failure
then surfaces at whatever assertion catches surviving placeholders, which names the
TEMPLATE file, so the operator is sent to look at index.html for a fault in a YAML
config. Any escaper written as `${s//&/&amp;}` also corrupts its own output.
**Symptom:** `make build` goes red with "unsubstituted placeholders remain in: dist/index.html" for a
config edit that is entirely legitimate, and the diagnostic points at the wrong file.
The same value works on bash 5.1.
**Action:** `shopt -u patsub_replacement` at the top of the script, and PROBE that it took effect
(`x="x"; x="${x//x/&y}"` must equal `&y`) — `shopt -u` on an option the shell lacks is
itself an error, and a silent failure produces a wrong artifact. Then make any assertion
message name both candidate causes, because "the template referenced an unknown key" and
"a value substituted the placeholder into itself" look identical in the output and live
in different files.
**Confidence:** verified (encountered building Phase 0)

#### G-083 — CacheStorage is partitioned by origin, not by service-worker scope, so a cache sweep reaches other applications
**Assumption:** A service worker's `caches` is scoped to that worker, so deleting every cache whose name
is not the current one is a safe way to evict the previous deploy.
**Reality:** CacheStorage is partitioned by ORIGIN. `caches.keys()` inside a worker registered at
/myapp/ returns the cache names of every application served from that origin, and
`caches.delete(name)` will delete them. On a GitHub Pages user site — one origin serving
every project site an account publishes — that is a dozen other applications.
`caches.match(request)` without `{cacheName}` searches all of them too, so a foreign
entry for an identical URL can be served as this app's asset. This is the shared-origin
reasoning behind relative paths (G-007), applied to storage rather than to URLs, and it
is easy to reason carefully about IndexedDB while walking straight through this.
**Symptom:** A sibling PWA on the same account stops working offline and silently refetches or fails
on its next load, every time the other app deploys, with nothing in either application
to explain it. Symmetrically, any sibling doing the same sweep deletes this app's shell.
Never reproduces on a dedicated domain.
**Action:** Namespace the cache name with something unique to the app within the origin, and derive
it rather than hardcode it — `ServiceWorkerRegistration.scope` is unique per project
site by construction and cannot drift from where the worker is registered (a hardcoded
app name would also breach §7.3). Sweep only names carrying that prefix, and pass
`{cacheName}` to every `caches.match`. Put the naming and the sweep predicate in a plain
module so they can be unit-tested: with no headless browser there is otherwise no way to
exercise a destructive routine that runs on every deploy.
**Confidence:** verified (encountered building Phase 0)

#### G-084 — A validation error that quotes the rejected value publishes it twice
**Assumption:** A check that refuses PII or content protects the store, so the check itself is on the
safe side of §9.2.
**Reality:** The rejection message is written to CloudWatch by whoever logs it and returned to the
caller, who may put it in an HTTP body. In internal/audit the actor-email and
action-shape checks used %q and Record logged the message with slog.Any, so the email
the check refuses to store and a 10KB transcript passed where an action belongs were
both written to CloudWatch at WARN — by the check whose entire purpose was keeping them
out.
**Symptom:** WARN lines whose byte size tracks the size of the input; a real email address or
transcript prose visible in log search; a 10KB error string in an HTTP 500 body. Also a
CloudWatch ingestion bill at $0.50/GB from a path that was supposed to be identifiers
only.
**Action:** A message about a rejected value DESCRIBES it: field name, byte length, and which rule
failed. Never the bytes. Bound a field's length BEFORE any regexp so the over-bound case
has its own length-only message. Return the rejection as a typed error carrying
field/rule and log those attributes plus logging.Redacted — never format the message
into the log line, so a %q added later cannot leak. Cover every field in the leak test,
not just the one that was hardened first.
**Confidence:** verified (encountered building Phase 0)

#### G-085 — A gate that returns a verdict is bypassable; a gate that owns the call is not
**Assumption:** A `Check(...) error` called before each provider call is enough, since the convention is
to return on error.
**Reality:** The failure mode is not forgetting the call, it is `if err != nil { log.Warn(...) }`
followed by the provider call anyway — written under deadline pressure by someone
treating an unreadable ledger as a transient nuisance. Passing the provider call to the
breaker as a closure makes the bypass unwriteable, which is the same reasoning I4
applies to cleanup patches: a structural guarantee where an instruction is not enough.
**Symptom:** The check is present in the diff, the tests pass, and the cap is silently inoperative on
exactly the path that matters — a storage fault, when the whole system is misbehaving.
**Action:** Where a control must not be bypassed, have it own the guarded operation. If a
verdict-only entry point is also needed (a pre-flight refusal at a request boundary),
name and document it as a forecast that grants nothing, and keep the guarded path the
only one that can reach the provider.
**Confidence:** verified (encountered building Phase 0)

#### G-086 — Crypto-shredding is not complete when the erasure operation returns
**Assumption:** Scheduling the tenant's CMK for deletion makes the data unrecoverable immediately, so
the erasure request can be reported complete.
**Reality:** `ScheduleKeyDeletion` enforces a pending-deletion window of 7-30 days. The key is
unusable during it (so data cannot be read), but the deletion is cancellable by anyone
with `kms:CancelKeyDeletion` — the data is not yet destroyed. §9.3's "unrecoverable
immediately" is true operationally and not true legally.
**Symptom:** An erasure attestation dated before the key was actually destroyed. Discovered when
someone asks for the destruction timestamp, or when a cancelled deletion silently
restores a tenant's corpus.
**Action:** Report the pending-deletion window as part of the erasure result and treat erasure as
two-phase: scheduled, then destroyed. `kmsref.Ref.ErasureCaveat()` returns a non-empty
caveat even for the shreddable case so a report has nothing to omit by accident.
**Confidence:** verified (encountered building Phase 0)

#### G-087 — Repointing a tenant at a CMK does not re-encrypt what it already wrote, so crypto-shredding is bounded in time
**Assumption:** Once a tenant's kms_key_id names a customer-managed key, destroying that key erases that
tenant's data.
**Reality:** S3 objects keep the encryption they were written with. Every object written while the
tenant pointed at alias/aws/s3 is SSE-S3 under a key the account cannot destroy, and it
survives the CMK's destruction untouched. DynamoDB is worse: encryption is table-level,
so items — and their PITR window and table backups — are under the table's key
regardless of what the tenant record says.
**Symptom:** An erasure report states the data is unrecoverable, the key is scheduled for deletion,
and the pre-repoint audio and transcripts remain readable. The surviving objects are
indistinguishable from the shredded ones, so it is found during an audit, not during
testing (compare G-021).
**Action:** Record the repoint instant on the tenant (kms_key_id_since) in the same update as
kms_key_id, and treat its absence as "every object predates the key". A shredding claim
may only cover all objects when the repoint is no later than created_at. State the
DynamoDB exclusion separately and gate it on the table's own key, never on the tenant's.
backend/internal/kmsref's ErasureScope is the one place that composes this claim.
**Confidence:** verified (encountered building Phase 0)

#### G-088 — DynamoDB encryption is table-level, so a per-tenant kms_key_id cannot be honoured on a shared table
**Assumption:** Once a CMK exists, a per-tenant `kms_key_id` covers both S3 objects and DynamoDB records
for that tenant.
**Reality:** S3 SSE is per object (a write can name any key), but DynamoDB SSE is configured on the
table. Every record in one table is under one key regardless of what the tenant record
says, so per-tenant keys require a table per key (or client-side encryption of
attributes).
**Symptom:** Destroying a tenant's CMK shreds its S3 content and none of its DynamoDB records —
items, threads, sessions, rules survive in PITR and backups. Nothing in the write path
errors, because DynamoDB writes pass no key.
**Action:** Check the tenant's key reference against the table's configured key on the DynamoDB
write path and refuse a mismatch rather than writing silently
(`kmsref.Ref.CheckDynamoPut`). Decide table-per-key vs client-side attribute encryption
*before* provisioning the first per-tenant CMK, not after.
**Confidence:** verified (encountered building Phase 0)

#### G-089 — Passing an AWS-managed alias as an SSE-KMS key ID converts free encryption into billed encryption
**Assumption:** If the tenant record says the key is `alias/aws/s3`, the write should pass that alias to
S3 as the SSE-KMS key ID — it is, after all, the resolved key.
**Reality:** S3 accepts it: the object is then encrypted with SSE-KMS under the AWS-managed S3 key,
which bills per KMS request, instead of SSE-S3 (`AES256`), which is free. Protection at
rest is identical (I8's own wording), so nothing about the data changes — only the bill.
**Symptom:** KMS request charges appear on a stack whose cost model says "KMS: none in personal
phase, $0.00" (§10.7), scaling with audio segment count — the highest-volume write in
the system — with no configuration change to point at.
**Action:** Treat an AWS-managed key reference as the *record* of which service-default key is in
use, not as a value to pass. `kmsref.Ref.ForS3Put` returns `AES256` with an empty
`KMSKeyID` for that case and a test asserts the key ID stays empty.
**Confidence:** verified (encountered building Phase 0)

#### G-090 — A discarded type assertion in a totaliser converts a data-shape problem into free spend
**Assumption:** `if c, ok := attrs["cost_micros"].(int64); ok { total += c }` is defensive: a malformed
record is skipped and the rest still total correctly.
**Reality:** It is fail-open. The daily spend breaker refuses on a read *error* but trusts any total
it receives, so a record counted as zero raises the effective cap and a whole day
reading as zero removes the cap entirely. The same shape appeared three times in
internal/meter: cost_micros, the ts filter, and a `len(day) < 7` window check that let
DayTotal("2026-08") return (0, nil). The two Repository implementations also disagree on
the Go type of a whole number, so the cost variant would have passed every CI run
against the in-memory fake and read as zero against DynamoDB.
**Symptom:** No error, no log line, no failed test. A provider invoice weeks later that exceeds the
configured daily cap many times over, with a usage report that looks plausible but low.
**Action:** In any function whose output gates spend or access, a value that cannot be read is an
error, never a zero. Read stored numbers through repository.AsInt64 / AsFloat64 rather
than a direct assertion, and validate a date window by parsing it — a length check
accepts strings that match no record and resolve to zero.
**Confidence:** verified (encountered building Phase 0)

#### G-091 — A Phase 0 frontend activates the Phase 1 interface gates, which have no browser to run in
**Assumption:** Shipping a minimal Phase 0 frontend is additive: the Phase 1 accessibility and
responsive checks stay dormant until Phase 1 builds the capture face.
**Reality:** check-a11y.sh and check-responsive.sh are gated on `[ -f frontend/index.html ]`, not on
a phase marker. The moment any index.html exists they both run for real, and both fail
on `command -v chromium` because containers/toolchain deliberately omits Chromium until
Phase 1 (~400MB on every CI job is cost without coverage). The static halves of both
checks — the vh-vs-dvh scan and the reserved-`live`-token rule — do pass.
**Symptom:** `make check` goes red on two legs with 'frontend/index.html exists but the toolchain
image has no headless browser', on a change that added no CSS bug. Because pages.yaml
gates on checks.yaml, the Pages deploy never runs.
**Action:** Treat 'a frontend surface exists' and not 'Phase 1 has started' as the trigger for
adding a headless browser to containers/toolchain and implementing the axe/contrast and
320px/1440px assertions. Do not rename the source file to keep the gate dormant, and do
not relax the browser requirement — the check's own message says it must not pass
trivially once there is a surface to test.
**Confidence:** verified (encountered building Phase 0)

#### G-041 — Manual sync steps decay
**Assumption:** A daily manual step is acceptable if the value is high.
**Reality:** Daily manual steps erode within weeks unless they are close to frictionless.
**Symptom:** A feature is used enthusiastically for two weeks and then abandoned; the device gathers dust.
**Action:** Budget the interaction cost explicitly. If import takes more than one tap, expect it to stop happening. This applies to review and triage queues equally.
**Confidence:** reported
