# Chintan

Chintan is a personal, mobile-friendly PWA for voice brain dumps: you speak unstructured thoughts while driving or walking, and the system transcribes, lightly cleans up the text, and appends it to the right note. It uses serverless AWS (Cognito, Lambda, DynamoDB, S3) with a static frontend on GitHub Pages, and follows a passbook-style deploy model so each instance gets its own isolated stack.

Uses **MiniMax-M3** (cleanup) + **Groq** (speech-to-text) as defaults, with API keys stored securely in AWS SSM Parameter Store.

See the [design spec](docs/superpowers/specs/2026-08-06-chintan-design.md) for architecture, data model, and security requirements.

## Deploy Your Own

### Prerequisites

- AWS CLI configured with appropriate permissions
- GitHub repository (can be your fork of this repo)
- API keys for:
  - **Groq** (speech-to-text) - Get from [console.groq.com](https://console.groq.com/)
  - **MiniMax** (text cleanup) - Get from [api.minimax.io](https://api.minimax.io/) or use OpenAI-compatible endpoint

### 1. Bootstrap AWS Resources

Run the setup script to create shared infrastructure:

```bash
./scripts/setup.sh
```

This creates:
- IAM roles for GitHub OIDC
- S3 bucket for Terraform state
- Basic AWS configuration

### 2. Configure API Keys

Store your API keys in AWS SSM Parameter Store. Use SecureString parameters:

```bash
# Groq API key (for speech-to-text)
aws ssm put-parameter \
  --name "/chintan/<instance>/groq_api_key" \
  --value "your-groq-api-key-here" \
  --type "SecureString"

# LLM API key (for text cleanup - MiniMax or OpenAI-compatible)
aws ssm put-parameter \
  --name "/chintan/<instance>/llm_api_key" \
  --value "your-minimax-or-openai-key-here" \
  --type "SecureString"
```

Replace `<instance>` with your instance name (e.g., `dev`, `prod`).

### 3. Create Instance Configuration

Create a YAML file under `config/instances/` (e.g., `config/instances/dev.yaml`):

```yaml
instance: dev
aws_region: us-east-1
allowed_origin: https://yourusername.github.io/chintan/dev
# Optional: customize models
llm_base_url: https://api.minimax.io/v1  # or OpenAI-compatible endpoint
llm_model: MiniMax-M3                    # or your preferred model
```

### 4. Deploy

Push your changes to GitHub. The CI/CD pipeline will automatically deploy your instance.

### 5. Create First User

After deployment, create your first Cognito user:

```bash
# Get your User Pool ID from the AWS Console or Terraform output
aws cognito-idp admin-create-user \
  --user-pool-id <your-user-pool-id> \
  --username <your-email> \
  --user-attributes Name=email,Value=<your-email> Name=email_verified,Value=true \
  --temporary-password TempPass123! \
  --message-action SUPPRESS
```

The user will need to change their password on first login.

### 6. Access Your App

Open your PWA at: `https://<username>.github.io/<repo>/<instance>/`

For example: `https://vppillai.github.io/chintan/dev/`

## Local Development

For local development, you can use environment variables instead of SSM:

```bash
# Create .env file (DO NOT commit this)
cat > .env << EOF
GROQ_API_KEY=your-groq-key-here
LLM_API_KEY=your-minimax-key-here
LLM_BASE_URL=https://api.minimax.io/v1
LLM_MODEL=MiniMax-M3
EOF

# The application prefers local env vars over SSM in development
```

## Security

This is a **public repository**. Security practices:

- ✅ **API keys**: Stored in AWS SSM Parameter Store, never in code
- ✅ **Frontend config**: Only contains public endpoints and Cognito client ID
- ✅ **Authentication**: Uses AWS Cognito with proper JWT validation
- ✅ **CORS**: Restricted to your GitHub Pages domain
- ❌ **Never commit**: Real API keys, `.env` files, or secrets to version control

The frontend is a static site with no server-side secrets. All sensitive data stays in your AWS account.

## Spend Tracking

Monitor and control costs with AWS tagging and budgets:

### 1. Cost Allocation Tags

All resources are tagged with:
```
Project = chintan
Instance = <your-instance>
Environment = <prod|dev|staging>
```

### 2. Set Up AWS Budget

Create a budget to monitor spend:

```bash
# Example: $10/month budget for your instance
aws budgets create-budget \
  --account-id $(aws sts get-caller-identity --query Account --output text) \
  --budget '{
    "BudgetName": "Chintan-Dev-Monthly",
    "BudgetLimit": {"Amount": "10.00", "Unit": "USD"},
    "TimeUnit": "MONTHLY",
    "BudgetType": "COST",
    "CostFilters": {
      "TagKey": ["Project", "Instance"],
      "TagValue": ["chintan", "dev"]
    }
  }'
```

### 3. Expected Costs

Typical monthly costs (per instance):
- **Lambda**: $0.20-2.00 (depending on usage)
- **DynamoDB**: $0.25-1.00 (5GB free tier)
- **S3**: $0.50-5.00 (depending on audio storage)
- **Cognito**: $0.00-0.55 (50,000 MAUs free)
- **CloudFront**: $0.00-1.00 (1TB free tier)

Total: **$1-10/month** for light to moderate usage.

## Teardown

To completely remove all Chintan resources from your AWS account:

```bash
./scripts/teardown.sh
```

This will:
1. Destroy all instance stacks
2. Remove SSM parameters
3. Delete S3 buckets (including stored audio/notes)
4. Remove IAM roles and policies

**⚠️ Warning**: This is irreversible and will delete all your notes and audio recordings.
