# Working with Third-Party Registries

This guide covers how to configure and manage registry credentials for function container images and Helm charts in self-hosted NVCF deployments.

## Overview

In NVCF, **third-party registries** refer to container registries used for hosting:

- **Function container images** - The containers that run your inference workloads
- **Helm charts** - Used for deploying helm chart functions

When a function is created or deployed, these credentials are used by different components:

1. **NVCF API** - Stores and manages registry credentials, validates that images exist during function creation. See [self-hosted-api](./api.md) for the full API specification.
2. **NVCA (Cluster Agent)** - Renders Helm charts or pod specs for container functions and handles deployment lifecycle. Generates image pull credentials based on the registry type.
3. **Worker init container** - Responsible for pulling the function container images during deployment.

### Supported Registries

NVCF supports the following container registries:

- **Amazon ECR** (Elastic Container Registry)
- **NVIDIA NGC** (nvcr.io)
- **Azure ACR** (Azure Container Registry)
- **VolcEngine CR** (Volcano Engine Container Registry)
- **JFrog Artifactory**
- **Harbor**

### Registry Credential Format

All registry credentials in NVCF use a base64-encoded `username:password` format:

```bash
echo -n "username:password" | base64
```

The specific username and password values depend on your registry type. See the sections below for registry-specific instructions.

## Bootstrap vs. Runtime Credentials

### Understanding Initial Bootstrap

When you first deploy the NVCF control plane, registry credentials are configured in the `secrets/<environment>-secrets.yaml` file under `api.accountBootstrap.registryCredentials`. Example using ECR:

```yaml
api:
  accountBootstrap:
    registryCredentials:
      - registryHostname: nvcr.io
        secret:
          name: nvcr-containers
          value: <BASE64_ENCODED_CREDENTIAL>
        artifactTypes: ["CONTAINER"]
        description: "NGC Container registry"
      - registryHostname: <account-id>.dkr.ecr.<region>.amazonaws.com
        secret:
          name: ecr-containers
          value: <BASE64_ENCODED_CREDENTIAL>
        artifactTypes: ["CONTAINER"]
        description: "ECR Container registry"
```

Example using Volcano Engine Container Registry:

```yaml
api:
  accountBootstrap:
    registryCredentials:
      - registryHostname: <registry-name>.cr.volces.com
        secret:
          name: vcr-containers
          value: <BASE64_ENCODED_CREDENTIAL>
        artifactTypes: ["CONTAINER", "HELM"]
        description: "Volcano Engine Container and Helm Registry"
```

These bootstrap credentials are loaded during the initial `helmfile sync` deployment and stored in the NVCF backend.

<Info>
The `registryHostname` must be the **registry base URL only** (e.g., `779846807323.dkr.ecr.us-west-2.amazonaws.com`), not including repository paths.

</Info>

### Managing Credentials After Deployment

After initial deployment, registry credentials can be managed dynamically using the **NVCF API** or **CLI** without redeploying the control plane:

- **Add** new registry credentials
- **List** existing credentials
- **Update** credentials (e.g., rotate secrets)
- **Delete** unused credentials

This is the recommended approach for credential rotation and adding or modifying registries post-deployment of the control plane.

### Credential Propagation Delay

Registry credential changes do not take effect for task creation immediately. Task processing caches each account's registry credentials with a time-to-live of about 5 minutes (`nvct.nvcf.cache-ttl`, default `PT5M` in the self-managed stack). After you add, update, or delete a credential, allow up to about 5 minutes for the change to apply to new tasks. The API and `nvcf-cli registry-credential list` and `get` reflect the change immediately, but task validation can keep using the previous value until the cached entry expires.

<Warning>
After you rotate or delete a credential, allow up to the cache TTL (about 5 minutes) for task creation to start using the new value. To apply the change immediately, restart the task service:

```bash
kubectl -n nvcf rollout restart deployment/nvct-api
```

</Warning>

## Adding AWS ECR Registry Credentials

AWS ECR requires **permanent IAM credentials** (Access Key ID + Secret Access Key). Temporary SSO/STS credentials will not work.

<Info>
**Why SSO credentials don't work:** AWS SSO and assumed role credentials include a session token that must be passed alongside the access key and secret. The NVCF registry credential format (`ACCESS_KEY_ID:SECRET_ACCESS_KEY`) does not support session tokens, so temporary credentials will fail with `UnrecognizedClientException`.

</Info>

NVCF supports both **ECR Private** and **ECR Public** registries:

- **ECR Private**: `<account-id>.dkr.ecr.<region>.amazonaws.com`
- **ECR Public**: `public.ecr.aws`

### Step 1: Create an IAM User

```bash
# Create IAM user for NVCF
aws iam create-user --user-name nvcf-ecr-service-account
```

### Step 2: Create and Attach ECR Policy

Create a least privilege IAM policy based on your ECR type.

**For Private ECR:**

```bash
# Set your AWS account ID and region
AWS_ACCOUNT_ID="<aws-account-id>"
AWS_REGION="<aws-region>"
REPO_PREFIX="<repo-prefix>"

# Create the policy
```

<Note>
**About REPO_PREFIX:** ECR repository names can include path-like prefixes for organization. The `REPO_PREFIX` scopes the IAM policy to only allow access to repositories matching that prefix.

**Examples:**

- If your repositories are named `nvcf/echo-server`, `nvcf/triton-server`, etc., set `REPO_PREFIX="nvcf"`
- If your repositories are named `my-cluster/nvcf-api`, `my-cluster/nvcf-sis`, etc., set `REPO_PREFIX="my-cluster"`
- To allow access to all repositories in the account, set `REPO_PREFIX="*"` (less secure)

The resulting IAM resource ARN `arn:aws:ecr:<region>:<account>:repository/<prefix>/*` will match all repositories starting with that prefix.

</Note>

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

# Attach the policy to the user
aws iam attach-user-policy \
  --user-name nvcf-ecr-service-account \
  --policy-arn arn:aws:iam::${AWS_ACCOUNT_ID}:policy/NVCFECRPrivateAccess
```

**For Public ECR:**

<Note>
**About REPO_PREFIX for Public ECR:** ECR Public uses aliases instead of account-based paths. When you create a public repository, you choose an alias (e.g., `nvidia`, `my-company`). Images are then referenced as `public.ecr.aws/<alias>/<repo-name>:<tag>`.

Set `REPO_PREFIX` to your ECR Public alias to scope the policy to your repositories.

</Note>

```bash
# Set your AWS account ID and ECR Public alias
AWS_ACCOUNT_ID="<aws-account-id>"
REPO_PREFIX="<ecr-public-alias>"

# Create the policy
aws iam create-policy \
  --policy-name NVCFECRPublicAccess \
  --policy-document '{
    "Version": "2012-10-17",
    "Statement": [
      {
        "Effect": "Allow",
        "Action": [
          "ecr-public:GetAuthorizationToken",
          "sts:GetServiceBearerToken"
        ],
        "Resource": "*"
      },
      {
        "Effect": "Allow",
        "Action": "ecr-public:DescribeImages",
        "Resource": "arn:aws:ecr-public::'"${AWS_ACCOUNT_ID}"':repository/'"${REPO_PREFIX}"'/*"
      }
    ]
  }'

# Attach the policy to the user
aws iam attach-user-policy \
  --user-name nvcf-ecr-service-account \
  --policy-arn arn:aws:iam::${AWS_ACCOUNT_ID}:policy/NVCFECRPublicAccess
```

### Step 3: Create Access Keys

```bash
# Create access keys and save the output securely
aws iam create-access-key --user-name nvcf-ecr-service-account
```

Example response:

```json
{
  "AccessKey": {
    "UserName": "nvcf-ecr-service-account",
    "AccessKeyId": "<access-key-id>",
    "SecretAccessKey": "<secret-access-key>",
    "Status": "Active"
  }
}
```

<Warning>
**Save these credentials immediately!** The secret access key is only shown once. Store it securely (e.g., in a password manager or secrets vault).

</Warning>

### Step 4: Add ECR Credentials via CLI

Generate the base64-encoded credential and add it using the CLI.

**For Private ECR:**

```bash
# Generate base64 credential (format: ACCESS_KEY_ID:SECRET_ACCESS_KEY)
ACCESS_KEY_ID="<access-key-id>"
SECRET_ACCESS_KEY="<secret-access-key>"

ECR_SECRET=$(echo -n "${ACCESS_KEY_ID}:${SECRET_ACCESS_KEY}" | base64 -w 0)

# Add the registry credential for Private ECR
./nvcf-cli registry-credential add \
  --hostname <account-id>.dkr.ecr.<region>.amazonaws.com \
  --secret "${ECR_SECRET}" \
  --artifact-type CONTAINER \
  --description "ECR Private Registry"
```

**For Public ECR:**

```bash
# Generate base64 credential (format: ACCESS_KEY_ID:SECRET_ACCESS_KEY)
ACCESS_KEY_ID="<access-key-id>"
SECRET_ACCESS_KEY="<secret-access-key>"

ECR_SECRET=$(echo -n "${ACCESS_KEY_ID}:${SECRET_ACCESS_KEY}" | base64 -w 0)

# Add the registry credential for Public ECR
./nvcf-cli registry-credential add \
  --hostname public.ecr.aws \
  --secret "${ECR_SECRET}" \
  --artifact-type CONTAINER \
  --description "ECR Public Registry"
```

### Cleanup IAM Resources (if needed)

To remove the IAM user and associated resources:

```bash
AWS_ACCOUNT_ID="<aws-account-id>"

# List and delete access keys
aws iam list-access-keys --user-name nvcf-ecr-service-account
aws iam delete-access-key --user-name nvcf-ecr-service-account --access-key-id <access-key-id>

# Detach and delete policy (for private ECR)
aws iam detach-user-policy \
  --user-name nvcf-ecr-service-account \
  --policy-arn arn:aws:iam::${AWS_ACCOUNT_ID}:policy/NVCFECRPrivateAccess
aws iam delete-policy \
  --policy-arn arn:aws:iam::${AWS_ACCOUNT_ID}:policy/NVCFECRPrivateAccess

# Detach and delete policy (for public ECR)
aws iam detach-user-policy \
  --user-name nvcf-ecr-service-account \
  --policy-arn arn:aws:iam::${AWS_ACCOUNT_ID}:policy/NVCFECRPublicAccess
aws iam delete-policy \
  --policy-arn arn:aws:iam::${AWS_ACCOUNT_ID}:policy/NVCFECRPublicAccess

# Delete user
aws iam delete-user --user-name nvcf-ecr-service-account
```

## Adding NGC Registry Credentials

NGC (NVIDIA GPU Cloud) uses API keys for authentication.

### Step 1: Generate NGC API Key

1. Navigate to [https://ngc.nvidia.com/setup/api-key](https://ngc.nvidia.com/setup/api-key) to generate a new API key or use an existing one if you have one saved.
2. Copy the key (format: `nvapi-xxxxxxxxxxxx`)

### Step 2: Add NGC Credentials via CLI

For NGC, the username is always `$oauthtoken`:

```bash
# Generate base64 credential (format: $oauthtoken:NGC_API_KEY)
NGC_API_KEY="<nvapi-xxxxxxxxxxxx>"

NGC_SECRET=$(echo -n '$oauthtoken:'"${NGC_API_KEY}" | base64 -w 0)

# Add the registry credential for containers
./nvcf-cli registry-credential add \
  --hostname nvcr.io \
  --secret "${NGC_SECRET}" \
  --artifact-type CONTAINER \
  --description "NGC Container Registry"

# Add the registry credential for Helm charts (if needed)
./nvcf-cli registry-credential add \
  --hostname helm.ngc.nvidia.com \
  --secret "${NGC_SECRET}" \
  --artifact-type HELM \
  --description "NGC Helm Registry"
```

## Adding Volcano Engine Container Registry Credentials

Volcano Engine Container Registry (VCR) uses access keys for authentication.

### Step 1: Get Volcano Engine Access Key

1. Login to the Volcano Engine Console
2. Go to the **Access Key** management page: [https://console.volcengine.com/iam/keymanage](https://console.volcengine.com/iam/keymanage).
3. Copy the **Access Key ID** and **Secret Access Key**. (Click on **Create Access Key** if you don't have one already.)

### Step 2: Add VCR Credentials via CLI

```bash
# Generate base64 credential (format: ACCESS_KEY_ID:SECRET_ACCESS_KEY)
ACCESS_KEY_ID="<access-key-id>"
SECRET_ACCESS_KEY="<secret-access-key>"

REGISTRY_NAME="<registry-name>"
VCR_SECRET=$(echo -n "${ACCESS_KEY_ID}:${SECRET_ACCESS_KEY}" | base64 -w 0)

# Add the registry credential for containers and Helm charts
./nvcf-cli registry-credential add \
  --hostname ${REGISTRY_NAME}.cr.volces.com \
  --secret "${VCR_SECRET}" \
  --artifact-type CONTAINER,HELM \
  --description "Volcano Engine Container and Helm Registry"
```

## Listing Registry Credentials

To view all configured registry credentials:

```bash
./nvcf-cli registry-credential list
```

## Deleting Registry Credentials

To remove a registry credential:

```bash
# Delete by credential ID
./nvcf-cli registry-credential delete <credential-id>
```

## How NVCF Matches Registries to Images

When you create or deploy a function, NVCF automatically matches the `containerImage` path to the appropriate registry credentials based on the **hostname**.

**Example: ECR Private**

```json
{
  "name": "my-function",
  "containerImage": "779846807323.dkr.ecr.us-west-2.amazonaws.com/my-repo/my-image:v1.0"
}
```

NVCF extracts the hostname `779846807323.dkr.ecr.us-west-2.amazonaws.com` and looks for a matching registry credential.

**Example: ECR Public**

```json
{
  "name": "my-function",
  "containerImage": "public.ecr.aws/my-alias/my-image:v1.0"
}
```

NVCF extracts the hostname `public.ecr.aws` and looks for a matching registry credential.

If credentials are found, they are used to:

1. Validate the image exists (during function creation)
2. Pull the image (during function deployment)

## Troubleshooting

### Incorrect registryHostname Format

The `registryHostname` in your credentials must **exactly match** the hostname portion of your container image path. Do not include repository paths in the hostname.

**ECR Private:**

- Correct: `779846807323.dkr.ecr.us-west-2.amazonaws.com`
- Incorrect: `779846807323.dkr.ecr.us-west-2.amazonaws.com/my-repo`

**ECR Public:**

- Correct: `public.ecr.aws`
- Incorrect: `public.ecr.aws/my-alias`

**NGC:**

- Correct: `nvcr.io`
- Incorrect: `nvcr.io/nvidia/pytorch`

### UnrecognizedClientException

**Error:**

```json
{
  "detail": "{\"__type\":\"UnrecognizedClientException\",\"message\":\"The security token included in the request is invalid.\"}"
}
```

**Cause:** This AWS-specific error indicates a malformed or invalid security token. Common causes include:

- Using temporary AWS credentials (SSO, STS assumed role) that include a session token, which NVCF's credential format does not support
- Incorrectly formatted credentials (e.g., wrong base64 encoding, missing or extra characters)
- Expired or revoked access keys

**Solution:**

1. Verify you are using permanent IAM user credentials (Access Key ID + Secret Access Key), not temporary credentials
2. Re-generate the base64-encoded credential ensuring the format is exactly `ACCESS_KEY_ID:SECRET_ACCESS_KEY` with no trailing newlines etc.
3. If using SSO/assumed roles, create a dedicated IAM user instead. See [ecr-registry-setup](./third-party-registries.md)

### Registry Not Found

**Error:**

```text
No registry credential found for hostname: example.dkr.ecr.us-west-2.amazonaws.com
```

**Cause:** The hostname in your `containerImage` doesn't match any configured registry credential.

**Solution:**

1. List your credentials with `./nvcf-cli registry-credential list`
2. Verify the hostname matches exactly (no trailing slashes, no repository paths)
3. Add the missing registry credential if needed

### Authentication Failed

**Error:**

```text
authentication required / unauthorized
```

**Cause:** The credentials are malformed or the password/key is incorrect.

**Solution:**

1. Verify the credential format is `username:password` base64-encoded
2. For ECR: Use `ACCESS_KEY_ID:SECRET_ACCESS_KEY`
3. For NGC: Use `$oauthtoken:NGC_API_KEY`
4. Re-add the credential with correct values
