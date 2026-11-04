# AGENTS.md - NVCF PKI chart

This native Helm chart provisions cluster-scoped PKI resources. It renders no
resources unless `clusterIssuer.enabled=true`.

## Validate

Run from the repository root:

```bash
helm lint deploy/helm/nvcf-pki
helm template nvcf-pki deploy/helm/nvcf-pki >/dev/null
deploy/helm/nvcf-pki/scripts/check-render.sh
git diff --check
```

## Style

Follow the repository root `AGENTS.md` for repository-wide code style and
public snapshot rules.

- Use two-space indentation in YAML.
- Keep Helm templates opt-in, validate required values with `required`, and
  quote rendered string values.
- Write validation scripts for POSIX `sh`. Use `set -eu` and clean temporary
  files with a trap.

## Retained issuer lifecycle

The chart marks the `ClusterIssuer` with Helm's `keep` resource policy. Helm
does not delete it when an upgrade, rollback, or uninstall removes it from the
rendered release. Helm also stops managing the retained object.

- Setting `clusterIssuer.enabled=false` or uninstalling retains the current
  issuer.
- Changing `clusterIssuer.name` creates the new issuer and retains the previous
  issuer.
- A rollback can retain an issuer that is absent from the restored release.

Before disabling the issuer or uninstalling the chart, reconfigure or remove
every certificate consumer. Roll out replacement consumers and verify their
data paths first.

When changing `clusterIssuer.name`, apply the upgrade and verify that the new
issuer is ready before moving certificate consumers to it.

After an upgrade, rollback, or uninstall:

1. List active and retained issuers:

   ```bash
   kubectl get clusterissuers
   ```

2. List certificate issuer references:

   ```bash
   kubectl get certificates --all-namespaces \
     -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,ISSUER-KIND:.spec.issuerRef.kind,ISSUER-NAME:.spec.issuerRef.name
   ```

3. Set `issuer_name` to the previous `clusterIssuer.name`. Delete the retained
   issuer only when no active certificate references it:

   ```bash
   issuer_name=nvcf-openbao-pki
   kubectl delete clusterissuer "${issuer_name}"
   ```
