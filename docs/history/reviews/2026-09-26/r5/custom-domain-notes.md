# README changes for the custom-domain provision

Three edits to README.md, in the order they appear in the file.

## 1. "Deploy your own", the line after the sequence

Replace

> Then open `https://<owner>.github.io/<repo>/<site_path>/` — `https://<owner>.github.io/chintan/dev/` for the shipped config.

with

> Then open `https://<owner>.github.io/<repo>/<site_path>/` — `https://<owner>.github.io/chintan/dev/` for the shipped config, or `https://<app_host>/<site_path>/` once a custom domain is configured (Configure → Custom domain).

## 2. "Configure → Instance configuration": three rows appended to the field table, then a new subsection before "User preferences"

Rows (after `refresh_token_validity_days`):

| Field | Default | Meaning |
|---|---|---|
| `api_host` | none | Custom hostname for this stack's API, e.g. `api.example.com` (staging: `api-staging.example.com`). A bare lowercase hostname. The stack requests a free, DNS-validated ACM certificate for it in its own region, fronts the HTTP API with it, and its `ApiEndpoint` output — so the bundle's API URL and the Devices card — becomes `https://<api_host>`. See Custom domain. |
| `app_host` | none | The GitHub Pages custom domain the bundles are served from, e.g. `app.example.com`. One per Pages site: every config sets the same value or none does. Sets every stack's Cognito callback URLs and CORS origin, and moves the bundles to the site root (`/<site_path>/`). |
| `dns_zone_id` | none | A Route 53 hosted zone holding `api_host`. Set, the stack writes the certificate's validation record and the API's alias itself. Refused without `api_host`. |

New subsection:

### Custom domain

Nothing here needs a domain. When you have one, the three fields above move the API and the app onto it, and everything downstream follows in one deploy: the bundle's API URL, the Devices card and the recipes it prints, the CORS origin, the Cognito callback URLs. Without them every stack is exactly what it is today — the custom-domain resources in `infrastructure/template.yaml` (`ApiCertificate`, `ApiDomainName`, `ApiMapping`, `ApiAliasRecord`) are conditional on `api_host`, and `scripts/check-custom-domain-dormant.sh` fails CI if one stops being so.

The sign-in page keeps its `amazoncognito.com` address, deliberately. A Cognito custom domain is CloudFront-fronted and needs its certificate in **us-east-1**, which a us-west-2 stack cannot request; changing the domain *replaces* `UserPoolDomain`, a stateful resource `scripts/deploy.sh` refuses without `--allow-replacement`; and the passkey relying-party id is that domain, so every registered passkey would stop working. Three constraints for a page seen once a month.

**What it costs.** Nothing on AWS: ACM public certificates and API Gateway custom domains are free, and GitHub Pages custom domains are free. A Route 53 hosted zone, if you want one, is $0.50 a month; the templates never create one, because a zone outlives any stack and is a decision rather than a resource. The domain itself is the registrar's fee.

**Order of operations**, once per domain.

1. **Pages first.** Add `app_host` to *every* instance config and re-run `scripts/setup.sh --region us-west-2 --apply` — it is idempotent, and this is the step that tells GitHub the domain (a site published from a workflow ignores any `CNAME` file in the artifact, so the repository setting is the only place it can live); or set **Settings → Pages → Custom domain** by hand. At the DNS provider: `CNAME app.example.com → <owner>.github.io`. GitHub recommends verifying the domain beforehand (**your profile → Settings → Pages → Add a verified domain**) so a dangling CNAME can never be claimed by someone else.
2. **Then the API.** Add `api_host` — and `dns_zone_id` if the name lives in Route 53 — to each config, and push. `Deploy Backend` reaches staging first.
   - *With* `dns_zone_id`: the certificate validates and the alias record lands in a few minutes. Nothing to do.
   - *Without*: the stack pauses on `ApiCertificate` until the validation CNAME exists. Read it from the stack events —
     `aws cloudformation describe-stack-events --stack-name chintan-dev-staging --query "StackEvents[?LogicalResourceId=='ApiCertificate'].ResourceStatusReason" --output text`
     prints `Content of DNS Record is: {Name: _….api-staging.example.com, Type: CNAME, Value: _….acm-validations.aws.}` — and create it. When the stack finishes, create `CNAME api-staging.example.com → <the stack's ApiDomainTarget output>`. The deploy's smoke test on the domain retries for about a minute and, if the record is not resolving yet, fails naming it; add the record and re-run the workflow. Approve production and do the same for `api.example.com`.
     ACM issues the same validation record for the same name in the same account every time, so a stack rebuilt later validates against the record already there.
3. **Reinstall and sign in.** `Deploy Frontend` follows the backend and publishes to `https://app.example.com/dev/` (the root redirects there; staging is `/dev-staging/`). The old `github.io` URL redirects too, but an installed PWA is bound to its origin and its service worker cannot follow a redirect: remove the old icon, open the new URL, install, sign in. Once GitHub has issued the Pages certificate — up to a day — turn on **Enforce HTTPS**.

Two things to know. The custom domain deploys only through the `chintan-cfn-deploy` service role (`CFN_DEPLOY_ROLE_ARN`, set by `setup.sh`): the CI role's permissions boundary lists no ACM or Route 53, on purpose. And `scripts/bootstrap.sh`, the hand-run recovery deploy, takes `--site-base https://app.example.com` for a custom-domain instance, because `--origin` alone spells the Pages form of the callback URL.

## 3. "Security", the CORS bullet

Replace

> **CORS** is restricted to your Pages origin.

with

> **CORS** is restricted to one origin: your Pages origin, or `app_host` once a custom domain is configured.

## 4. "Connect a device", the `API=` comment

Replace `# the stack's ApiEndpoint output` with `# the stack's ApiEndpoint output — https://api.example.com behind a custom domain`.
