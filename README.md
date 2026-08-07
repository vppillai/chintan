# Chintan

Chintan is a personal, mobile-friendly PWA for voice brain dumps: you speak unstructured thoughts while driving or walking, and the system transcribes, lightly cleans up the text, and appends it to the right note. It uses serverless AWS (Cognito, Lambda, DynamoDB, S3) with a static frontend on GitHub Pages, and follows a passbook-style deploy model so each instance gets its own isolated stack.

See the [design spec](docs/superpowers/specs/2026-08-06-chintan-design.md) for architecture, data model, and security requirements.

## Quick start

1. Clone this repo.
2. Run `./scripts/setup.sh` to bootstrap shared AWS resources and GitHub OIDC.
3. Add or edit an instance config under `config/instances/` (e.g. `dev.yaml`) and push; CI deploys the stack.
4. Open the PWA at `https://<owner>.github.io/<repo>/<instance>/` (for example, `https://vppillai.github.io/chintan/dev/`).

## Security

This is a **public repository**. Never commit secrets, API keys, or `.env` files. Provider credentials live in AWS SSM Parameter Store; the frontend gets only Cognito and API endpoints at deploy time.

## Teardown

To remove Chintan resources from your AWS account, run `./scripts/teardown.sh`.
