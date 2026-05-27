# Vault Integration

This document describes the Vault integration points expected by the NVCF
Ratelimiter service. It uses placeholder values so the same guidance can apply
to different environments and secret stores.

## Prerequisites

- Access to the Vault namespace used for the deployment.
- A `vault` CLI that can authenticate to the selected Vault endpoint.
- Permission to read and write the Ratelimiter secret paths.
- Optional: `nk` or another NATS key tool if you need to rotate NATS keys.

## Environment

Set the endpoint, namespace, account, and region for the environment you are
operating:

```shell
export VAULT_ADDR="https://<vault_addr>"
export VAULT_NAMESPACE="<vault_namespace>"
export ACCOUNT_ID="<account_id>"
export REGION="<region>"
```

Then authenticate with the method configured for your Vault deployment:

```shell
vault login -method=<auth_method> <auth_arguments>
```

## Secret Paths

Use deployment-specific paths for the Ratelimiter secrets. The examples below
show the expected shape without naming a concrete internal environment:

```shell
export RATELIMITER_KV_PATH="/cloud/aws/${ACCOUNT_ID}/services/nvcf-ratelimiter/regions/all/kv"
export RATELIMITER_REGION_KV_PATH="/cloud/aws/${ACCOUNT_ID}/services/nvcf-ratelimiter/regions/${REGION}/kv"
```

The all-regions path commonly stores service-wide settings, such as telemetry
configuration and NATS credentials. The region path stores values that differ
by deployment region.

## Telemetry Secret

Store telemetry provider credentials under an `ext` path:

```shell
vault kv put "${RATELIMITER_KV_PATH}/ext/lightstep" api_key="<api_key>"
vault kv get "${RATELIMITER_KV_PATH}/ext/lightstep"
```

## NATS Keys

Generate a user key and store the public key and seed together:

```shell
nk -gen user -pubout

vault kv put "${RATELIMITER_KV_PATH}/nats" \
    nkey_pub="<nkey_pub>" \
    nkey_seed="<nkey_seed>"

vault kv get "${RATELIMITER_KV_PATH}/nats"
```

## Rendering Local Secrets

If your deployment uses Vault Agent templates, render them with the environment
specific agent config:

```shell
cd vault
vault agent -config="<agent_config>.hcl" -log-level=debug
```

This writes the rendered secret files under `vault/` according to the local
agent template configuration.
