# NVCF UI Addon

NVCF UI is an optional, customer-facing NVCF admin-panel UI. It is disabled by
default and available only in stack packages that include the NVCF UI addon.

## When to enable

Enable NVCF UI only when you need the admin-panel UI and your extracted stack
package includes the addon. If the package does not contain a `nvcf-ui` release
and `nvcfUi` route values, skip the addon until you use a package that includes
them.

## Enable the addon

Enable it in `environments/<env-name>.yaml`:

```yaml
addons:
  nvcfUi:
    enabled: true
```

## Create namespace and image pull secret

Create the namespace, and an image pull secret only if pulling from a private
registry:

```bash
kubectl create namespace nvcf-ui --dry-run=client -o yaml | kubectl apply -f -

# Only if pulling from a private registry (e.g., NGC nvcr.io)
export NGC_API_KEY="<your-key>"
kubectl create secret docker-registry nvcr-creds \
  --docker-server=nvcr.io \
  --docker-username='$oauthtoken' \
  --docker-password="$NGC_API_KEY" \
  --namespace=nvcf-ui \
  --dry-run=client -o yaml | kubectl apply -f -
```

The default route host is `nvcf-ui.<domain>` and the backend is
`nvcf-ui.nvcf-ui:8300`.

## Apply the service and route

After confirming the package includes the `nvcf-ui` release, preview and apply
the service and route:

```bash
HELMFILE_ENV=<env-name> helmfile --selector name=nvcf-ui template
HELMFILE_ENV=<env-name> helmfile --selector name=nvcf-ui sync
HELMFILE_ENV=<env-name> helmfile --selector release-group=ingress sync
```

## Verify

Verify the route only when the addon is present and enabled:

```bash
kubectl get deploy,svc -n nvcf-ui
kubectl get httproute -A | grep -i nvcf-ui
curl -i -H "Host: nvcf-ui.<domain>" "http://<gateway-address>/status"
```

## Security

The NVCF UI admin panel is currently unauthenticated. Do not expose it to the
public internet. Restrict access to a trusted network, VPN, or an authenticating
proxy in front of the `nvcf-ui` route.
