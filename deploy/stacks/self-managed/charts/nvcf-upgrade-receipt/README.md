# Upgrade receipt

This bundled chart records the installed stack version after the final
Helmfile stage succeeds. Its post-install and post-upgrade hooks create or
update the `nvcf-upgrade-receipt` ConfigMap in `nvcf`.

The helper comes from `docker.io/alpine/k8s`. Its pinned version is in
`values.yaml`. The stack's global NVCF image settings do not change this
third-party repository. To use a mirror, add these values to the selected
stack environment:

```yaml
upgradeReceipt:
  image:
    registry: registry.example.com
    repository: mirrored/alpine-k8s
```

The chart's tag stays in effect unless `upgradeReceipt.image.tag` is set.
Qualify any replacement helper against the target Kubernetes version. The
image pull secrets still come from `global.imagePullSecrets`.

The release inventory includes both this local chart, pinned to the stack
commit, and the rendered helper image as required control-plane artifacts.
Different helper versions used by other hooks remain separate inventory
entries. Add their explicit manifest classifications when refreshing the
catalog from the next published stack inventory. Existing released catalogs
and bundles are unchanged by a source update.

## Validation

Run the render and real inventory checks from the repository root:

```bash
bash deploy/stacks/self-managed/tests/upgrade-receipt-wiring.sh
NVCF_RECEIPT_INVENTORY_E2E=1 go test -C tools/docs-version-sync \
  -run 'TestReceiptInventoryIncludesBundledChartAndDefaultHookImage' -count=1
```

For hook execution, create a fresh isolated k3d cluster whose name begins with
`nvcf-receipt-`, then set an isolated kubeconfig and a new artifact directory:

```bash
KUBECONFIG=/path/to/isolated/kubeconfig \
NVCF_RECEIPT_LIVE_CONTEXT=k3d-nvcf-receipt-example \
NVCF_RECEIPT_LIVE_ARTIFACTS=/path/to/new/receipt-results \
  bash deploy/stacks/self-managed/tests/upgrade-receipt-live.sh
```

The test pulls the default public helper, runs the install hook, replaces the
recorded version with a sentinel, and runs the upgrade hook. It checks that
the receipt is updated in place and retains the cluster and evidence. This
tests the receipt in isolation; a complete stack release still requires its
full install and upgrade qualification.
