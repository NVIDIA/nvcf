# Quickstart: Local k3d Installation

Install a self-hosted NVCF stack on one local k3d cluster, register that
cluster with the control plane, and confirm that the local deployment is
healthy.

This quickstart uses a single k3d cluster named `ncp-local`, fake GPUs, and
local route hostnames. It is for local development and validation only. For a
remote deployment, or for separate control-plane and GPU clusters, use
[Helmfile Installation](./helmfile-installation.md) and
[Self-Managed Clusters](./cluster-management/self-managed.md).

Run the commands from the NVCF repository root unless a step says otherwise.
The `nvcf-cli self-hosted up` command runs on your workstation. It does not run
inside Kubernetes.

Clone the public repository before you start:

```bash
git clone https://github.com/nvidia/nvcf.git
cd nvcf
```

## Prerequisites

Before you start, install and prepare:

- Docker running on your workstation
- `k3d` v5.x or later
- `kubectl`
- `helm` >= 3.14
- `helmfile` >= 1.0. Use `helmfile` >= 1.5.0 with Helm 4.
- `helm-diff` plugin
- `nvcf-cli` on your `PATH`. See [Installation](./cli.md#installation) to
  build it from the repository or download it from NGC.
- An NGC API key with access to the NVCF chart and image registry
- The NGC organization and team slugs for that registry access

Use these install helpers if you do not already have the local tools.

<Accordion title="Install Docker">
Install Docker from the
[Docker installation guide](https://docs.docker.com/get-started/get-docker/).
After Docker starts, verify that the CLI can reach it:

```bash
docker version
```
</Accordion>

<Accordion title="Install k3d, kubectl, Helm, and Helmfile">
On macOS with Homebrew:

```bash
brew install k3d kubectl helm helmfile

k3d version
kubectl version --client
helm version
helmfile version
```

For other systems, use the official installation guides:
[k3d](https://k3d.io/stable/#installation),
[kubectl](https://kubernetes.io/docs/tasks/tools/),
[Helm](https://helm.sh/docs/intro/install/), and
[Helmfile](https://helmfile.readthedocs.io/en/stable/).
</Accordion>

<Accordion title="Install helm-diff">
Install the Helm plugin used by Helmfile:

```bash
if helm version --short | grep -q '^v4'; then
  helm plugin install https://github.com/databus23/helm-diff --verify=false
else
  helm plugin install https://github.com/databus23/helm-diff
fi

helm plugin list
```

See the [helm-diff installation instructions](https://github.com/databus23/helm-diff#install)
for offline or Helm 4 installation options.
</Accordion>

<Accordion title="Build nvcf-cli from this repository">
Run these commands from the NVCF repository root:

```bash
./setup.sh
bazel build //src/clis/nvcf-cli:nvcf-cli

mkdir -p "${HOME}/.local/bin"
install -m 0755 \
  bazel-bin/src/clis/nvcf-cli/nvcf-cli_/nvcf-cli \
  "${HOME}/.local/bin/nvcf-cli"

export PATH="${HOME}/.local/bin:${PATH}"
nvcf-cli version
```

For the packaged CLI release, see [Installation](./cli.md#download-from-ngc).
</Accordion>

`self-hosted up` defaults to `--env local` and supports only the single local
k3d layout. It requires a current `k3d-*` kube context.

## Step 1: Create the local k3d cluster

Export the registry credentials used by the local cluster bootstrap:

```bash
export NGC_API_KEY="<ngc-api-key>"
export SAMPLE_NGC_ORG="<ngc-org>"
export SAMPLE_NGC_TEAM="<ngc-team>"
```

Create the single-cluster local topology:

```bash
make -C tools/ncp-local-cluster build-and-deploy-cluster
kubectl config use-context k3d-ncp-local
kubectl config current-context
```

Expected output:

```text
k3d-ncp-local
```

<Info>
This creates a k3d cluster named `ncp-local`, installs the fake GPU operator,
the CSI SMB driver, and Envoy Gateway, and makes the local kube context
available to `kubectl` and `nvcf-cli`.
</Info>

## Step 2: Prepare local routing

Add these entries to `/etc/hosts` on the workstation running the CLI if they do
not already resolve:

```text
127.0.0.1 api.localhost
127.0.0.1 api-keys.localhost
127.0.0.1 sis.localhost
127.0.0.1 invocation.localhost
```

Use the local CLI configuration that points commands at the local routes:

```bash
export NVCF_CLI_CONFIG=deploy/stacks/self-managed/nvcf-cli-local.yaml
```

Log in to the NGC container registry:

```bash
helm registry login nvcr.io -u '$oauthtoken' -p "${NGC_API_KEY}"
```

<Info>
The local hostnames route CLI traffic through the local Envoy Gateway. The
local CLI configuration keeps later function commands on those local routes
instead of the hosted NVCF endpoints.
</Info>

## Step 3: Create the local stack secrets file

Create the local secrets files used by the control-plane and compute-plane stacks:

```bash
cp deploy/stacks/self-managed/secrets/secrets.yaml.template \
  deploy/stacks/self-managed/secrets/local-secrets.yaml

BASE64_CRED="$(printf '%s' "\$oauthtoken:${NGC_API_KEY}" | base64 | tr -d '\n')"
sed -i.bak "s|REPLACE_WITH_BASE64_DOCKER_CREDENTIAL|${BASE64_CRED}|g" \
  deploy/stacks/self-managed/secrets/local-secrets.yaml
rm deploy/stacks/self-managed/secrets/local-secrets.yaml.bak
```

<Note>
`local-secrets.yaml` is gitignored. Keep your NGC key out of committed files.
</Note>

## Step 4: Run the install

Check local tools and Kubernetes access:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" self-hosted check --pre
```

Run the local install:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" self-hosted up \
  --control-plane-stack=deploy/stacks/self-managed \
  --compute-plane-stack=deploy/stacks/nvcf-compute-plane \
  --env=local \
  --cluster-name=ncp-local \
  --nca-id=nvcf-default \
  --region=us-west-1 \
  --icms-url=http://sis.localhost:8080 \
  --refresh-token
```

Expected result: the final screen reports a successful install, a registered
cluster, and a healthy backend.

<Info>
The command installs the control plane, mints CLI authentication, registers
`ncp-local`, installs the compute-plane components, and waits for the
`NVCFBackend` health check to report healthy.
</Info>

## Step 5: Verify the install

Run the full self-hosted health checks:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" self-hosted check --all \
  --cluster-name=ncp-local
```

Inspect the local Kubernetes resources:

```bash
kubectl get pods -A
kubectl get httproute -A
kubectl get nvcfbackends -A
```

Expected result: the CLI checks do not report failed checks, and the
`ncp-local` `NVCFBackend` reports healthy.

## Step 6: Deploy and invoke a sample function

Create a function from the `load_tester_supreme` sample image in the registry
organization and team you exported earlier:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" function create \
  --name quickstart-load-tester-supreme \
  --image "nvcr.io/${SAMPLE_NGC_ORG}/${SAMPLE_NGC_TEAM}/load_tester_supreme:0.0.8" \
  --inference-url /echo \
  --inference-port 8000 \
  --health-uri /health \
  --health-port 8000 \
  --health-timeout PT30S
```

Deploy the function to the local fake GPU backend:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" function deploy create \
  --gpu H100 \
  --instance-type NCP.GPU.H100_8x \
  --backend ncp-local \
  --regions us-west-1 \
  --min-instances 1 \
  --max-instances 1 \
  --timeout 900
```

Generate an API key for invocation:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" api-key generate \
  --description quickstart-load-tester-supreme \
  --scopes invoke_function,list_functions,queue_details,list_functions_details
```

Invoke the sample function:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" function invoke \
  --request-body '{"message":"quickstart-echo","repeats":1}' \
  --timeout 120 \
  --poll-duration 5
```

Expected result: the invocation response contains `quickstart-echo`.

For other local fake GPU configurations, choose a `--gpu` and
`--instance-type` that match the discovered node labels and GPU count.

## Clean up

Remove the sample function deployment and function:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" function deploy remove
nvcf-cli --config "${NVCF_CLI_CONFIG}" function delete
```

Remove the compute-plane components:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" self-hosted uninstall \
  --compute-plane \
  --cluster-name ncp-local \
  --compute-plane-stack=deploy/stacks/nvcf-compute-plane
```

Remove the control plane:

```bash
nvcf-cli --config "${NVCF_CLI_CONFIG}" self-hosted uninstall \
  --control-plane \
  --control-plane-stack=deploy/stacks/self-managed
```

Destroy the local k3d cluster:

```bash
make -C tools/ncp-local-cluster destroy
```

## Troubleshooting

If the quickstart fails, start with [Troubleshooting](./troubleshooting.md).

Common local k3d issues:

- `sis.localhost` must resolve from the workstation running `nvcf-cli`.
- `kubectl config current-context` must print `k3d-ncp-local`.
- `lookup api-keys.nvcf.nvidia.com: no such host` means the CLI is using the
  hosted default endpoint. Run commands with
  `--config "${NVCF_CLI_CONFIG}"` from this quickstart.
- `node inotify limits below NVCA minimums` means the local k3d nodes need
  higher Linux `fs.inotify` limits. This does not change your macOS shell
  limits. From the repository root, apply
  `tools/ncp-local-cluster/apps/node-tuning/node-tuning.yaml`, wait for the
  `node-tuning` DaemonSet in the `kube-system` namespace, then rerun
  `nvcf-cli --config "${NVCF_CLI_CONFIG}" self-hosted check --pre`:

  ```bash
  kubectl apply -f tools/ncp-local-cluster/apps/node-tuning/node-tuning.yaml
  kubectl -n kube-system rollout status ds/node-tuning --timeout=5m
  ```

  For non-local clusters, see
  [Node inotify limits](./cluster-management/self-managed.md#node-inotify-limits).

## See Also

- [Local Development](../local-development) for local k3d variants and cleanup commands.
- [Helmfile Installation](./helmfile-installation.md) for remote or manual control-plane installs.
- [Self-Managed Clusters](./cluster-management/self-managed.md) for registering GPU clusters outside the local quickstart.
- `src/clis/nvcf-cli/examples/` in this repository for sample CLI input files.
