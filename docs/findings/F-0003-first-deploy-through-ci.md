# F-0003: Can CI deploy a working stack through the OIDC role, with no credentials held locally?
Date: 2026-08-05   Phase: 0   Status: **confirmed, after five failures**

## Question

§0.5A makes this the first slice of the project, ahead of any application code:

> Phase 0's first slice is a repository whose CI runs, gates, and deploys a hello-world
> through the real OIDC role — not a feature with CI added once there is something worth
> testing.

And §Phase 0 acceptance restates it as a gate: "The pipeline deployed a hello-world before
the first feature commit existed — verifiable from git history."

Pass criterion: a push to `main` runs every §0.5A check, and only if they pass, builds a
reproducible arm64 artifact, assumes the deployment role via OIDC with no stored AWS key,
deploys the stack, and proves the result serves a real request.

## Method

Five deploy attempts. Each failure is recorded below with what it looked like, because in
every case the error named something other than the cause.

## Result

**Confirmed.** `voicenotes-dev` is deployed and serving:

```
$ curl https://gjcz8ovcui.execute-api.ca-central-1.amazonaws.com/v1/health
{
  "version": "v0.1.0-alpha.1",
  "commit": "91ae6cc",
  "build_time": "2026-08-05T16:36:39Z",
  "stamped": true,
  "config_version": 1,
  "instance": "dev"
}
```

Verified from outside CI: an unknown path returns 404 rather than anything disclosing what
exists (§9.1), and `access-control-allow-origin` is the single configured origin, never a
wildcard (§10.6).

Cold-start numbers, recorded as the baseline the Phase 2 ONNX sizing gate will be measured
against (§4, G-061): **init 142ms, peak 32MB against a 256MB allocation**, artifact 3.4MB.
The headroom is what makes linking the ONNX Runtime C library into the worker plausible.

### The five failures

**1. `sts:AssumeRoleWithWebIdentity` refused (G-071).** The trust policy conditioned on
`sub` equal to `repo:vppillai/chintan:environment:production` — the form AWS documents,
every example shows, and `passbook`'s bootstrap uses *in this same account*. A temporary
step printing the presented claim gave the answer immediately:

```
presented sub: repo:vppillai@3634378/chintan@1323409209:environment:production
expected  sub: repo:vppillai/chintan:environment:production
```

GitHub now embeds the numeric owner and repository ids. Worse, IAM refuses a trust policy
that does not condition on `sub` or `job_workflow_ref` at all, so the immutable id claims
cannot simply replace it. The resolution is both: `StringEquals` on `repository_owner_id`,
`repository_id`, and `environment` for precision, plus a `StringLike` on `sub` with
wildcards exactly where the ids sit — which also matches the older format.

Two things made this expensive. The error names neither the expected nor the presented
value. And a sibling project's role using the old form still works, so the nearest
precedent actively misleads.

**2. prod deployed automatically.** Not a failure of the pipeline but of my reading of it:
the matrix covered every configured instance, so a merge to `main` deployed prod. §Phase 0
says "for `main` (checks, then deploy to **dev** through the OIDC role)". A push now
deploys dev; prod goes through `workflow_dispatch` with the instance validated against
`config/instances` so a typo fails instead of creating an empty stack.

**3. Circular dependency (G-072).** The data bucket's notification references the worker
function, the function references its role, and the role derived the bucket ARN with
`!GetAtt`. CloudFormation listed nine resources and named no edges. Constructing the ARN
with `!Sub` from instance, account, and region removes the edge and is exact rather than
approximate, because §6.2 fully determines the name. The tell I had already written and
not followed: `WorkerInvokePermission`'s `SourceArn` has to be constructed for the same
reason.

**4. The build produced `commit=unknown`.** git failed inside the container: the image
marked `/workspace` safe, but a CI checkout lives at `/__w/<repo>/<repo>` owned by a
different uid, so git refused it as dubious ownership — silently, falling back to a
placeholder.

Not cosmetic. The deploy embeds the commit in the artifact's S3 key, so two different
commits would both upload to `api-unknown.zip`, CloudFormation would see an unchanged key,
and the function would keep running the previous code while the deploy reported success.
It also pins the service worker cache token, which is exactly G-035. `build-lambda.sh` now
refuses to build rather than substituting a placeholder.

**5. `handler kind ptr is not func` (G-073).** The most instructive one. The cold start
logged:

```
"config loaded"  instance=dev config_version=1 active_stt=groq_whisper_turbo prompt_biasing=true
"api ready"      version=v0.1.0-alpha.1 commit=91ae6cc stamped=true
handler kind ptr is not func
```

Everything worked — config read from S3, version stamped, 142ms cold start — and
`lambda.Start` had been handed the adapter struct instead of a bound method value. Its
parameter is `any`, so it compiles, initialises, and reports ready; the type is checked at
the *first request*.

So the function looked healthy and returned 500 for everything, with a log that said
nothing about configuration or permissions — the two things anyone checks first.

## Consequence for the build

1. **The deploy smoke test earns its place.** Failure 5 produced a green CloudFormation
   deploy and a completely broken service. Without a request at the end of the deploy, CI
   would have reported success. It is three lines and it caught the subtlest failure of
   the five.

2. **Four of the five errors named something other than the cause.** The OIDC failure
   named authorisation, the cycle named nine resources, the version failure named nothing
   at all, and the handler failure came after a log line saying "ready". The pattern worth
   generalising: when an error does not name a value, print the value before theorising.
   That one temporary step resolved failure 1 in a single run after twelve retries had
   resolved nothing.

3. **Config-from-S3 works and is worth its cost.** §7.4 requires that a config change need
   no rebuild; the function reads it at cold start and `/v1/health` now reports the config
   version actually in force. One S3 GET per cold start, inside the 142ms.

4. **A cold-start baseline exists** for the Phase 2 native-ONNX gate to be measured
   against, which is the load-bearing unknown in the §4 runtime decision.

5. **No AWS credential is stored anywhere.** The pipeline assumes the role via OIDC; the
   agent's own session credentials are short-lived and cannot deploy — `deploy.sh` refuses
   to run outside CI unless given `--incident-response`.

6. **Still open:** the version is `v0.1.0-alpha.1`, a pre-release, because `v0.1.0` is the
   MVP at the end of Phase 1 (§0.5). The worker deploys but declines to process audio,
   deliberately: a pipeline that acknowledges work it cannot do is a lost capture that
   nothing reports (I2).
