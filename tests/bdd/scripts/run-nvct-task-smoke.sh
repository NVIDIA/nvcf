#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Launches a minimal NVCT task through the local Tasks API and waits
# until the task reaches COMPLETED. The script mints a short-lived
# NVCT-scoped API key from the nvcf-cli admin token so secrets do not
# appear in BDD command logs.

set -euo pipefail

for tool in curl jq; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "required tool not found: $tool" >&2
    exit 127
  fi
done

STATE_PATH="${NVCT_BDD_STATE_PATH:-${HOME}/.nvcf-cli.nvcf-cli-local.state}"
if [[ ! -f "$STATE_PATH" ]]; then
  echo "nvcf-cli state file not found: $STATE_PATH" >&2
  exit 1
fi

ADMIN_TOKEN="$(jq -r '.token // empty' "$STATE_PATH")"
if [[ -z "$ADMIN_TOKEN" ]]; then
  echo "nvcf-cli state file does not contain a token" >&2
  exit 1
fi

API_KEYS_URL="${NVCT_BDD_API_KEYS_URL:-http://api-keys.localhost:8080/v1/keys}"
API_KEYS_HOST="${NVCT_BDD_API_KEYS_HOST:-api-keys.localhost}"
API_KEYS_SERVICE_ID="${NVCT_BDD_API_KEYS_SERVICE_ID:-nvidia-cloud-tasks-ncp-service-id-nvcttasks}"
API_KEYS_ISSUER_SERVICE="${NVCT_BDD_API_KEYS_ISSUER_SERVICE:-nvct-api}"
API_KEYS_OWNER_ID="${NVCT_BDD_API_KEYS_OWNER_ID:-svc@nvct-api.local}"
TASKS_URL="${NVCT_BDD_TASKS_URL:-http://tasks.localhost:8080/v1/nvct/tasks}"
TASKS_HOST="${NVCT_BDD_TASKS_HOST:-tasks.localhost}"
TASK_NAME="${NVCT_BDD_TASK_NAME:-bdd-nvct-task-smoke}"
if [[ -n "${NVCT_BDD_TASK_IMAGE:-}" ]]; then
  TASK_IMAGE="$NVCT_BDD_TASK_IMAGE"
else
  : "${SAMPLE_NGC_ORG:?SAMPLE_NGC_ORG must be set when NVCT_BDD_TASK_IMAGE is unset}"
  : "${SAMPLE_NGC_TEAM:?SAMPLE_NGC_TEAM must be set when NVCT_BDD_TASK_IMAGE is unset}"
  TASK_IMAGE_TAG="${NVCT_BDD_TASK_IMAGE_TAG:-local}"
  TASK_IMAGE="nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/task-simple-sample:${TASK_IMAGE_TAG}"
fi
TASK_GPU="${NVCT_BDD_TASK_GPU:-H100}"
TASK_INSTANCE_TYPE="${NVCT_BDD_TASK_INSTANCE_TYPE:-NCP.GPU.H100_8x}"
TASK_BACKEND="${NVCT_BDD_TASK_BACKEND:-ncp-local-compute-1}"
TASK_TIMEOUT_SECONDS="${NVCT_BDD_TASK_TIMEOUT_SECONDS:-900}"
TASK_POLL_SECONDS="${NVCT_BDD_TASK_POLL_SECONDS:-10}"
TASK_NUM_RESULTS="${NVCT_BDD_TASK_NUM_RESULTS:-1}"
TASK_DELAY_MINUTES="${NVCT_BDD_TASK_DELAY_MINUTES:-0}"
TASK_FILE_SIZE_BYTES="${NVCT_BDD_TASK_FILE_SIZE_BYTES:-8192}"
TASK_INCLUDE_METADATA="${NVCT_BDD_TASK_INCLUDE_METADATA:-false}"

if ! [[ "$TASK_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || [[ "$TASK_TIMEOUT_SECONDS" -eq 0 ]]; then
  echo "NVCT_BDD_TASK_TIMEOUT_SECONDS must be a positive integer" >&2
  exit 64
fi
if ! [[ "$TASK_POLL_SECONDS" =~ ^[0-9]+$ ]] || [[ "$TASK_POLL_SECONDS" -eq 0 ]]; then
  echo "NVCT_BDD_TASK_POLL_SECONDS must be a positive integer" >&2
  exit 64
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

key_request="$tmpdir/create-api-key.json"
key_response="$tmpdir/create-api-key-response.json"
request_body="$tmpdir/create-task.json"
create_response="$tmpdir/create-task-response.json"
task_response="$tmpdir/task-response.json"

expires_at_utc() {
  if date -u -d '+1 day' '+%Y-%m-%dT%H:%M:%S.000Z' >/dev/null 2>&1; then
    date -u -d '+1 day' '+%Y-%m-%dT%H:%M:%S.000Z'
  else
    date -u -v+1d '+%Y-%m-%dT%H:%M:%S.000Z'
  fi
}

jq -n \
  --arg description "$TASK_NAME" \
  --arg expiresAt "$(expires_at_utc)" \
  --arg serviceID "$API_KEYS_SERVICE_ID" \
  '{
    description: $description,
    expires_at: $expiresAt,
    authorizations: {
      policies: [
        {
          aud: $serviceID,
          auds: [$serviceID],
          product: "nv-cloud-tasks",
          resources: [
            {id: "*", type: "account-tasks"}
          ],
          scopes: [
            "launch_task",
            "task_details",
            "list_tasks"
          ]
        }
      ]
    },
    audience_service_ids: [$serviceID]
  }' > "$key_request"

if ! http_code="$(curl -sS -o "$key_response" -w "%{http_code}" \
  -X POST "$API_KEYS_URL" \
  -H "Host: ${API_KEYS_HOST}" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" \
  -H "Key-Issuer-Service: ${API_KEYS_ISSUER_SERVICE}" \
  -H "Key-Issuer-Id: ${API_KEYS_SERVICE_ID}" \
  -H "Key-Owner-Id: ${API_KEYS_OWNER_ID}" \
  -H "Content-Type: application/json" \
  --data-binary "@${key_request}")"; then
  echo "failed to generate NVCT task API key" >&2
  exit 1
fi

case "$http_code" in
  200|201) ;;
  *)
    echo "generate NVCT task API key failed with HTTP $http_code" >&2
    jq . "$key_response" >&2 || cat "$key_response" >&2
    exit 1
    ;;
esac

API_KEY="$(jq -r '.value // empty' "$key_response")"
if [[ -z "$API_KEY" ]]; then
  echo "generate NVCT task API key response did not include value" >&2
  jq . "$key_response" >&2 || cat "$key_response" >&2
  exit 1
fi

jq -n \
  --arg name "$TASK_NAME" \
  --arg image "$TASK_IMAGE" \
  --arg gpu "$TASK_GPU" \
  --arg instanceType "$TASK_INSTANCE_TYPE" \
  --arg backend "$TASK_BACKEND" \
  --arg numResults "$TASK_NUM_RESULTS" \
  --arg delayMinutes "$TASK_DELAY_MINUTES" \
  --arg fileSizeBytes "$TASK_FILE_SIZE_BYTES" \
  --arg includeMetadata "$TASK_INCLUDE_METADATA" \
  '{
    name: $name,
    containerImage: $image,
    containerEnvironment: [
      {key: "NUM_OF_RESULTS", value: $numResults},
      {key: "DELAY_BETWEEN_RESULTS_IN_MINUTES", value: $delayMinutes},
      {key: "FILE_SIZE_BYTES", value: $fileSizeBytes},
      {key: "INCLUDE_METADATA", value: $includeMetadata}
    ],
    gpuSpecification: {
      gpu: $gpu,
      instanceType: $instanceType,
      backend: $backend
    },
    resultHandlingStrategy: "NONE",
    maxRuntimeDuration: "PT10M",
    maxQueuedDuration: "PT10M",
    terminationGracePeriodDuration: "PT1M"
  }' > "$request_body"

if ! http_code="$(curl -sS -o "$create_response" -w "%{http_code}" \
  -X POST "$TASKS_URL" \
  -H "Host: ${TASKS_HOST}" \
  -H "Authorization: Bearer ${API_KEY}" \
  -H "Content-Type: application/json" \
  --data-binary "@${request_body}")"; then
  echo "failed to create NVCT task" >&2
  exit 1
fi

case "$http_code" in
  200|201) ;;
  *)
    echo "create NVCT task failed with HTTP $http_code" >&2
    jq . "$create_response" >&2 || cat "$create_response" >&2
    exit 1
    ;;
esac

task_id="$(jq -r '.task.id // empty' "$create_response")"
if [[ -z "$task_id" ]]; then
  echo "create NVCT task response did not include task.id" >&2
  jq . "$create_response" >&2 || cat "$create_response" >&2
  exit 1
fi
echo "Created NVCT task $TASK_NAME ($task_id)"

deadline=$(( $(date +%s) + TASK_TIMEOUT_SECONDS ))
while [[ $(date +%s) -lt "$deadline" ]]; do
  if ! http_code="$(curl -sS -o "$task_response" -w "%{http_code}" \
    -H "Host: ${TASKS_HOST}" \
    -H "Authorization: Bearer ${API_KEY}" \
    "$TASKS_URL/$task_id")"; then
    echo "failed to get NVCT task status for $task_id" >&2
    exit 1
  fi

  if [[ "$http_code" != "200" ]]; then
    echo "get NVCT task failed with HTTP $http_code" >&2
    jq . "$task_response" >&2 || cat "$task_response" >&2
    exit 1
  fi

  status="$(jq -r '.task.status // empty' "$task_response")"
  percent_complete="$(jq -r '.task.percentComplete // empty' "$task_response")"
  if [[ -n "$percent_complete" && "$percent_complete" != "null" ]]; then
    echo "Task $TASK_NAME status: $status percentComplete=$percent_complete"
  else
    echo "Task $TASK_NAME status: $status"
  fi

  case "$status" in
    COMPLETED)
      exit 0
      ;;
    ERRORED|CANCELED|EXCEEDED_MAX_RUNTIME_DURATION|EXCEEDED_MAX_QUEUED_DURATION)
      jq . "$task_response" >&2 || cat "$task_response" >&2
      exit 1
      ;;
    QUEUED|LAUNCHED|RUNNING|"")
      sleep "$TASK_POLL_SECONDS"
      ;;
    *)
      echo "unexpected NVCT task status: $status" >&2
      jq . "$task_response" >&2 || cat "$task_response" >&2
      exit 1
      ;;
  esac
done

echo "timed out after ${TASK_TIMEOUT_SECONDS}s waiting for NVCT task $task_id to complete" >&2
jq . "$task_response" >&2 || cat "$task_response" >&2
exit 1
