# Allowlisting an Unsupported Registry

This guide is for customers deploying the `helm-nvcf-api` chart who need to use a container registry that is not in the NVCF API's built-in recognized list. Examples include `ghcr.io`, an internal corporate registry, or any third-party registry that NVCF does not yet natively recognize.

The procedure registers the registry hostname with the NVCF API as a custom registry. Custom registries are not subject to artifact or credential validation, so function creation no longer fails with `Missing CONTAINER registry for hostname '<host>'`. The same steps work for any registry hostname. Only the `HOSTNAME` value and the registry key (`GHCR`, `INTERNAL`, `MYREG`, etc.) change.

<Warning>
Container functions only. This guide covers allowlisting registries used for container function images. Allowlisting registries for Helm-chart functions is not supported today. Support is planned for a later release.

</Warning>

## When to use this

Use this only if all of the following are true:

- Your function image is hosted on a registry hostname that NVCF does not natively validate. You are seeing `Missing CONTAINER registry for hostname '<host>'` on function create.
- Your cluster (kubelet or the worker init container) can already pull from that registry.
- You accept that the NVCF API will not pre-validate that the image exists at function-create time. Any pull failure surfaces later as `ErrImagePull` on the workload pod.

If the registry you need is in the supported list (ECR, NGC, ACR, VolcEngine, Artifactory, Harbor), follow the standard registry guidance instead. See [Working with Third-Party Registries](./third-party-registries).

## Step 1: Apply the Helm env vars

Pick a short uppercase key for your registry. The key is used only as a config map key. The example below uses `GHCR` for `ghcr.io`. The two env vars register the hostname under the `container` artifact type as a custom registry.

```bash
# Customize for your environment.
KUBE_CONTEXT="<your-context>"
CHART_VERSION="<chart-version>"
ORG_PATH="<your-org>"

# Customize for the registry you want to allowlist.
REG_KEY="GHCR"
REG_HOSTNAME="ghcr.io"
REG_NAME="GitHub Container Registry"

helm upgrade api "oci://nvcr.io/${ORG_PATH}/helm-nvcf-api" \
  --version "${CHART_VERSION}" --namespace nvcf \
  --kube-context "${KUBE_CONTEXT}" \
  --reuse-values \
  --set-string "api.env.NVCF_REGISTRIES_RECOGNIZED_CONTAINER_${REG_KEY}_NAME=${REG_NAME}" \
  --set-string "api.env.NVCF_REGISTRIES_RECOGNIZED_CONTAINER_${REG_KEY}_HOSTNAME=${REG_HOSTNAME}"
```

`NAME` and `HOSTNAME` are both required by the schema.

## Step 2: Wait for the rollout

The NVCF API container is distroless, so a rollout is required for env changes to take effect. After each `helm upgrade`, force the new pod to schedule and wait for it to become ready:

```bash
kubectl delete pod -n nvcf \
  --kube-context "${KUBE_CONTEXT}" \
  -l app.kubernetes.io/name=helm-nvcf-api \
  --field-selector=status.phase=Running

kubectl rollout status deployment/nvcf-api -n nvcf \
  --kube-context "${KUBE_CONTEXT}" --timeout=120s
```

## Step 3: Verify the env vars are applied

Use `helm get values` because the API container has no shell:

```bash
helm get values api -n nvcf --kube-context "${KUBE_CONTEXT}" \
  | grep -A1 "${REG_KEY}"
```

You should see both env vars (`NAME` and `HOSTNAME`) under `api.env`.

## Step 4: Test that function creation now succeeds

Use the nvcf-cli to create a function pointing at an image on the new registry:

```bash
IMAGE_PATH="<your-namespace>/<your-image>:<tag>"

nvcf-cli function create \
  --name "registry-allowlist-test-$(date +%s)" \
  --image "${REG_HOSTNAME}/${IMAGE_PATH}" \
  --inference-url "/health" --inference-port 8080 \
  --health-uri "/health" --health-timeout PT30S
```

Expected: a 2xx response with a function id and version id. Before this change, the same call returned `400 Missing CONTAINER registry for hostname '${REG_HOSTNAME}'`.

The create call succeeds even if you have not registered a credential for `${REG_HOSTNAME}` in the NVCF credential store. The workload pod will fail later with `ErrImagePull` if the kubelet cannot pull the image; pulling is the cluster's responsibility, not NVCF's.

## Rolling back

To remove the registry, delete the entries from the chart's persisted values:

```bash
helm get values api -n nvcf --kube-context "${KUBE_CONTEXT}" -o yaml > values.yaml
# Edit values.yaml to remove the api.env.NVCF_REGISTRIES_RECOGNIZED_CONTAINER_${REG_KEY}_* keys
helm upgrade api "oci://nvcr.io/${ORG_PATH}/helm-nvcf-api" \
  --version "${CHART_VERSION}" --namespace nvcf \
  --kube-context "${KUBE_CONTEXT}" \
  -f values.yaml
```

Then repeat Step 2 (force rollout) and Step 3 (verify the env vars are gone). After rollback, function create against the registry returns the original `400 Missing CONTAINER registry for hostname` error.

## Important notes

This procedure does not pull the image. The cluster is responsible for that. The kubelet (or the worker init container) must already have a way to authenticate to the registry. Common paths are an auto-injected image pull secret on the workload namespace (for example via Kyverno), or a kubelet integration provided by the cloud (for example ECR, GCR, and ACR on CSP-managed clusters where node IAM lets the kubelet pull without an explicit secret).

The `REG_KEY` you choose is a Spring Boot map key. Use anything short and uppercase that is not already in use by a built-in registry (`DOCKER`, `NGC`, `ECR`, `ECR_PUBLIC`, `VOLCENGINE`, `ACR`).
