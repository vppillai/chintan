# Backup and restore

`chintanctl backup` and `restore` (`backend/cmd/chintanctl/backup.go`, `restore.go`) copy a tenant's data out of an instance and back in at full fidelity. The procedures below are written to be repeated as they stand; what each run found is `docs/history/ops-log.md`.

## What a backup holds

`backup --instance <name> [--environment <env>] [--tenant <sub>] --out <dir>` writes, under `<dir>`: `items.jsonl` (every DynamoDB item of the tenant, verbatim), `objects.jsonl` (one `{tenant_id, key, size, sha256}` per S3 object), `objects/<key>` (the bodies, byte for byte) and, last, `backup.json` — the header with both manifests' sha256, so its presence means the backup finished and `restore` refuses a directory without it. Without `--tenant` every tenant with objects in the bucket is taken — tenants are discovered from the `tenants/` prefixes (`enumerate.go`, `discoverTenants`), because the roles grant no `dynamodb:Scan`, so a tenant with rows and no objects is backed up only when named with `--tenant`. Each object record carries the object's tags, which `restore` puts back (the retention lifecycle rules match on them). The table name is derived from the instance and the bucket read from the stack's `ContentBucketName` output; `--table` and `--bucket` override both.

It does **not** hold the Cognito user pool: no users, passwords or passkeys, and no SSM parameters (provider keys, VAPID pair). The tenant id is the Cognito user's `sub` — `auth.Identity.TenantID` is set from the verified token's subject (`backend/internal/auth/verifier.go`; the type is `identity.go`), every DynamoDB key is `USER#<sub>` and every S3 key `tenants/<sub>/…` (README → Security → Tenancy) — so a backup is tied to the pool its tenant was created in. That is what the disaster procedure below is about.

Cadence, for a one-user instance: a backup before anything that writes a tenant wholesale — `chintanctl erase`, `reconcile --apply`, `regenerate --all --apply`, a deploy with `--allow-replacement` — and otherwise as often as the notes written since the last one would hurt to lose; the bucket's own versioning covers a note's superseded versions for the window in `docs/design/retention.md`, but not a deleted tenant. Keep the directory off the account (a laptop or external disk; it holds the owner's notes, so treat it as the notes), and keep the previous one until the new one has been verified as in step 5 below. Every command on this page runs on the Linux VM (`ssh orb`; GNU coreutils, so `sha256sum` and `perl` are there). `chintanctl` takes no profile flag; it reads the ambient AWS configuration, so `AWS_PROFILE=chintan` selects the agent role, which can read everything a backup needs, and the VM's default profile — the owner's — is what a restore, which writes, runs under.

## Back up and restore one tenant (same instance)

A restore writes the tenant back into the instance it names; there is no "into another tenant" mode, so a rehearsal is a round trip of the same tenant. Rehearse on staging's test tenant, never the owner's.

```bash
# Go is not on a non-interactive ssh session's PATH on the VM.
export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH GOTOOLCHAIN=auto
cd backend && go build -o /tmp/chintanctl ./cmd/chintanctl
C=/tmp/chintanctl; T=<tenant sub>

# 1. What the table and the bucket disagree about, before anything is written.
$C reconcile --instance dev --environment staging
# 2. Back up the one tenant. backup.json is written last: its presence means the backup finished.
$C backup  --instance dev --environment staging --tenant $T --out ~/backup-1
# 3. Dry run: hashes every local body against the manifest, lists what it would overwrite, writes nothing.
$C restore --instance dev --environment staging --in ~/backup-1
# 4. The write. --yes skips the typed confirmation for a tenant with live data in the target.
$C restore --instance dev --environment staging --in ~/backup-1 --apply --yes
# 5. Same findings as step 1; then a second backup must equal the first.
$C reconcile --instance dev --environment staging
$C backup  --instance dev --environment staging --tenant $T --out ~/backup-2
sha256sum ~/backup-{1,2}/items.jsonl ~/backup-{1,2}/objects.jsonl && diff -rq ~/backup-1/objects ~/backup-2/objects
```

Step 5 passes when `items.jsonl` and `objects.jsonl` hash the same and `diff` prints nothing; only `backup.json` differs, by its timestamp. A restore refuses outright when any body on disk disagrees with the manifest (`REFUSED: … do not match the manifest hash`), and asks for `yes` typed when the target already holds that tenant's data (`OVERWRITES live data`), which `--yes` answers (typing the tenant id is `erase`'s confirmation, not this one's). Restore re-puts every object, and the bucket invokes the worker on each `tenants/…/audio.*` create (`ContentBucket`'s `NotificationConfiguration`); the worker returns at once for a capture whose status is terminal (`pipeline.go`, `CaptureIsTerminal`) and re-runs any other — a capture that was mid-flight when the backup was taken is transcribed and filed again on restore, rewriting its rows and spending against the cap. Finish or delete in-flight captures before a backup you intend to restore.

## Disaster restore

The same-instance restore above is the answer while the stack stands: a tenant erased or damaged goes back under the same tenant id, because the pool that issued it is untouched. This section is for a stack that is gone. Its user pool, table and bucket are `Retain` with fixed names (`UserPool`, `DynamoDBTable` and `ContentBucket` in `infrastructure/template.yaml`), so deleting the stack by itself leaves all three, with every note still in them — and the rebuild cannot adopt them: it collides with their names and fails. Nothing in `chintanctl` remaps a tenant id; step 5 does it by hand.

Look first. The agent role can read all four (`AWS_PROFILE=chintan AWS_REGION=us-west-2`):

```bash
A=$(aws sts get-caller-identity --query Account --output text)
aws cloudformation describe-stacks --stack-name chintan-dev-prod --query 'Stacks[0].StackStatus' --output text
aws dynamodb describe-table --table-name chintan-dev-prod --query 'Table.TableStatus' --output text
aws s3api head-bucket --bucket chintan-content-dev-prod-$A
aws cognito-idp list-user-pools --max-results 60 --query "UserPools[?Name=='chintan-dev-prod'].Id" --output text
```

No stack, and the table and the bucket answer: start at step 1. No stack and nothing retained, with a backup directory in hand: start at step 3. Either way the steps use `$A` from above and `$C`, the `chintanctl` built as in the same-instance procedure (`export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH GOTOOLCHAIN=auto; (cd backend && go build -o /tmp/chintanctl ./cmd/chintanctl); C=/tmp/chintanctl`).

1. **Back up what the stack left behind**, by name, since no stack is left to read the bucket from (the table name is derived from the instance). Twice, and compare, as in step 5 of the procedure above; the restore dry run in between hashes every body against the manifest and writes nothing.
   ```bash
   BUCKET=chintan-content-dev-prod-$A
   $C backup  --instance dev --bucket $BUCKET --out ~/backup-retained
   $C restore --instance dev --bucket $BUCKET --in ~/backup-retained
   $C backup  --instance dev --bucket $BUCKET --out ~/backup-retained-2
   sha256sum ~/backup-retained{,-2}/items.jsonl ~/backup-retained{,-2}/objects.jsonl && diff -rq ~/backup-retained/objects ~/backup-retained-2/objects
   ```
   Copy `~/backup-retained` off the VM before going on: the next step deletes the only other copy of the notes. A backup holds the tenants' partitions only; the table's `INSTANCE` partition (the daily spend counter and the AWS-cost reading) is not in it and starts again in the rebuilt table.
2. **Clear the retained resources**, only once step 1 has passed. The script refuses while the stack exists, lists what it found, and deletes nothing without `--apply`; on `prod` it also wants the acknowledgement flag, and it asks for a typed phrase. It needs the owner's credentials (the VM's default profile): the agent role's boundary denies these deletes. The pool goes with it, and with the pool every sign-in and passkey.
   ```bash
   scripts/clean-instance-orphans.sh --instance dev --environment prod --region us-west-2
   scripts/clean-instance-orphans.sh --instance dev --environment prod --region us-west-2 --i-understand-this-deletes-data --apply
   ```
3. **Rebuild the stack, then the frontend, in that order.** The bundle on Pages is compiled against the old stack's API URL, pool and client id, so the app cannot sign in until it is rebuilt against the new stack's outputs.
   - The pipeline, from the Mac: `gh workflow run deploy-backend.yaml`. It deploys staging, smoke-tests it, creates prod with every parameter the configs declare, and Deploy Frontend follows on its own.
   - When the pipeline cannot run, on the VM in a mirror of the tree (it needs the AWS CLI and Go, and a mirror has no git remote, hence `--repo`): `export PATH=/usr/local/go/bin:$PATH; scripts/bootstrap.sh --instance dev --region us-west-2 --origin https://<owner>.github.io --repo <owner>/<repo> --apply`, then `gh workflow run deploy-frontend.yaml` from the Mac. `bootstrap.sh` passes neither the passkey relying party, the alarm address nor the site path (`scripts/ci-deploy-stack.sh` does), so passkeys are off and the alarms e-mail nobody until the next Deploy Backend run.

   The provider keys and the VAPID pair live in SSM outside the stack and survive its deletion; `scripts/doctor.sh --instance dev --region us-west-2` lists what is missing, and `scripts/vapid-keys.sh --instance dev --region us-west-2 --apply` writes a missing pair. A new VAPID pair means every browser re-subscribes afterwards (**You → Notifications** off and on).
4. **Recreate the user and read the new tenant id.** A rebuilt pool issues a new `sub` to the same e-mail, so the id in the backup matches nothing in it.
   ```bash
   scripts/invite-user.sh --instance dev --email you@example.com --apply
   POOL=$(aws cloudformation describe-stacks --stack-name chintan-dev-prod --query "Stacks[0].Outputs[?OutputKey=='UserPoolId'].OutputValue" --output text)
   NEW=$(aws cognito-idp admin-get-user --user-pool-id "$POOL" --username you@example.com --query "UserAttributes[?Name=='sub'].Value | [0]" --output text)
   OLD=$(jq -r '.tenants[0]' ~/backup-retained/backup.json)   # or the backup directory you started with
   ```
5. **Re-map the backup to the new id.** The old sub is in every item's `pk` (`USER#<sub>`) and `gsi1pk` (`TENANT#<sub>#NOTE#…`, `dynamo_captures.go`), in the top-level key attributes (`audio_key`, `raw_key`, `routed_key`, `clean_key`, `segments_key`, `peaks_key` on a capture; `s3_markdown_key`, `s3_meta_key` on a note), in the usage rows' `gsi1sk` (`TENANT#<sub>`, `usage.go`) and inside the `data` blobs, in every `objects.jsonl` line, in the `objects/` path and in the header's `tenants`; the header's manifest hashes must then be recomputed, or `restore` refuses the directory. A plain substitution covers all of those because the sub is one opaque string; this assumes a single-tenant backup (one `tenants` entry). Work on a copy, on the VM (`perl -pi -e` rather than `sed -i`, whose in-place flag differs between GNU and the Mac's BSD sed).
   ```bash
   cp -r ~/backup-retained ~/backup-remap && B=~/backup-remap
   perl -pi -e "s/\Q$OLD\E/$NEW/g" "$B/items.jsonl" "$B/objects.jsonl"
   mv "$B/objects/tenants/$OLD" "$B/objects/tenants/$NEW"
   jq --arg t "$NEW" --arg i "$(sha256sum "$B/items.jsonl" | cut -d' ' -f1)" --arg o "$(sha256sum "$B/objects.jsonl" | cut -d' ' -f1)" \
      '.tenants = [$t] | .items_sha256 = $i | .objects_sha256 = $o' "$B/backup.json" > "$B/backup.json.new" && mv "$B/backup.json.new" "$B/backup.json"
   grep -c "$OLD" "$B/items.jsonl" "$B/objects.jsonl"   # both 0
   ```
   The object bodies are untouched (a note's `note.md` and a recording's files do not contain the tenant id), so their per-object hashes still match.
6. **Restore** into the rebuilt stack, dry run first: `$C restore --instance dev --in $B`, then the same with `--apply`. The target is empty, so no overwrite prompt appears. Every restored `audio.*` object invokes the worker once; captures with a terminal status return at once, any other is re-run (see above).
7. **Verify**: `$C reconcile --instance dev` reports nothing beyond what the backup's source reported; sign in and open a note with recordings. Device keys are rows in the table and come back with the tenant, so a watch or shortcut keeps working; passkeys do not survive a new pool and are added again from **You → Passkeys**, once a Deploy Backend run has passed the relying party if the stack was rebuilt with `bootstrap.sh`.

History: `docs/history/ops-log.md` holds each rehearsal and the account's dated state.
