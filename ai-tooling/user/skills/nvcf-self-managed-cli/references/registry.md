# Registry Reference

Detailed reference for managing registry credentials via `nvcf-cli`.

## Overview

Registry credentials allow NVCF to access private container registries for pulling images, Helm charts, models, and resources. All registry credential operations require the `manage_registry_credentials` scope (included in default API key scopes).

## Adding Credentials

### With username and password

The CLI encodes the credentials automatically:

```bash
nvcf-cli registry add \
  --hostname myregistry.example.com \
  --username myuser \
  --password mypass \
  --artifact-type CONTAINER \
  --description "My private Docker registry"
```

### With pre-encoded secret

Provide a base64-encoded `username:password` string:

```bash
nvcf-cli registry add \
  --hostname nvcr.io \
  --secret "<BASE64_ENCODED_CREDENTIAL>" \
  --artifact-type CONTAINER \
  --description "NGC Container Registry"
```

### Multiple artifact types

```bash
nvcf-cli registry add \
  --hostname myregistry.example.com \
  --username myuser \
  --password mypass \
  --artifact-type CONTAINER \
  --artifact-type MODEL \
  --tag "environment:prod" \
  --tag "team:ml" \
  --description "Production ML registry"
```

### Flags

| Flag | Required | Description |
|------|----------|-------------|
| `--hostname` | Yes | Registry hostname (e.g., `nvcr.io`, `myregistry.example.com`) |
| `--artifact-type` | Yes | Artifact types: `CONTAINER`, `HELM`, `MODEL`, `RESOURCE` (repeatable) |
| `--username` | One of username/password or secret | Registry username |
| `--password` | One of username/password or secret | Registry password |
| `--secret` | One of username/password or secret | Base64 encoded `username:password` |
| `--description` | No | Description of the credential |
| `--tag` | No | Tags for the credential (repeatable) |

## AWS ECR Credentials

Do not use temporary ECR auth tokens from `aws ecr get-login-password` as NVCF registry credentials. These tokens expire every 12 hours and will cause pull failures after expiration. Instead, create a dedicated IAM user with ECR read-only access and register its long-lived access keys.

### 1. Create an IAM policy for ECR read access

```bash
aws iam create-policy \
  --policy-name NVCFECRPrivateAccess \
  --policy-document '{
    "Version": "2012-10-17",
    "Statement": [
      {
        "Effect": "Allow",
        "Action": "ecr:GetAuthorizationToken",
        "Resource": "*"
      },
      {
        "Effect": "Allow",
        "Action": [
          "ecr:BatchGetImage",
          "ecr:GetDownloadUrlForLayer",
          "ecr:BatchCheckLayerAvailability",
          "ecr:DescribeImages"
        ],
        "Resource": "arn:aws:ecr:'"${AWS_REGION}"':'"${AWS_ACCOUNT_ID}"':repository/'"${REPO_PREFIX}"'/*"
      }
    ]
  }'
```

### 2. Create an IAM user and attach the policy

```bash
aws iam create-user --user-name nvcf-ecr-service-account

aws iam attach-user-policy \
  --user-name nvcf-ecr-service-account \
  --policy-arn "arn:aws:iam::${AWS_ACCOUNT_ID}:policy/NVCFECRPrivateAccess"
```

### 3. Generate long-lived access keys

```bash
aws iam create-access-key --user-name nvcf-ecr-service-account
```

Save the `AccessKeyId` and `SecretAccessKey` from the output.

### 4. Register the credential with nvcf-cli

Use the IAM access key ID as the username and the secret access key as the password. The hostname is the ECR registry endpoint for your account and region:

```bash
nvcf-cli registry add \
  --hostname <account-id>.dkr.ecr.<region>.amazonaws.com \
  --username <AccessKeyId> \
  --password <SecretAccessKey> \
  --artifact-type CONTAINER \
  --description "AWS ECR private registry"
```

## Listing Credentials

```bash
# List all credentials
nvcf-cli registry list

# Filter by artifact type
nvcf-cli registry list --artifact-type CONTAINER

# Filter by provisioner
nvcf-cli registry list --provisioned-by USER

# Combined filters
nvcf-cli registry list --artifact-type CONTAINER --artifact-type MODEL
```

### Filter Flags

| Flag | Values | Description |
|------|--------|-------------|
| `--artifact-type` | `CONTAINER`, `HELM`, `MODEL`, `RESOURCE` | Filter by artifact type (repeatable) |
| `--provisioned-by` | `SYSTEM`, `USER` | Filter by who created the credential |

## Getting Credential Details

```bash
nvcf-cli registry get <credential-id>
```

Displays hostname, artifact types, tags, description, creation time, and last update time.

## Updating Credentials

Update the password or add additional artifact types. Tags and description cannot be updated after creation. Artifact types are additive (added to existing types, not replaced).

```bash
# Update credentials
nvcf-cli registry update <credential-id> \
  --username myuser \
  --password newpass

# Add artifact types only
nvcf-cli registry update <credential-id> \
  --artifact-type HELM \
  --artifact-type MODEL

# Update both
nvcf-cli registry update <credential-id> \
  --username myuser \
  --password mypass \
  --artifact-type HELM
```

### Flags

| Flag | Description |
|------|-------------|
| `--username` | New registry username (must be provided with `--password`) |
| `--password` | New registry password (must be provided with `--username`) |
| `--artifact-type` | Artifact type(s) to add (repeatable, additive) |

## Deleting Credentials

```bash
# Delete with confirmation prompt
nvcf-cli registry delete <credential-id>

# Skip confirmation
nvcf-cli registry delete <credential-id> --force
```

Deletion is permanent. The credential will no longer be available for accessing the associated registry.

## Listing Recognized Registries

List the registry providers and endpoints officially supported by NVCF:

```bash
nvcf-cli registry list-recognized
```
