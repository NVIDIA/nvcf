
# Test byoo-otel-collector locally

## Prepare configuration for otel-collector

Get the otelconfig from the nvcf-otelconfig repository (use your internal clone or artifact pipeline; path `byoo-otel-collector` under that repo).

## Generate accounts-secrets.json file

### Install consul-template

```bash
VERSION=0.39.1
wget -O /tmp/consul-template_${VERSION}_linux_amd64.zip \
  "https://releases.hashicorp.com/consul-template/${VERSION}/consul-template_${VERSION}_linux_amd64.zip"
unzip /tmp/consul-template_${VERSION}_linux_amd64.zip -d /tmp/
sudo mv /tmp/consul-template /usr/local/bin/

consul-template -v
```

### Login CDS NVault

```bash
export VAULT_ADDR=https://vault.example.invalid
export VAULT_NAMESPACE=gfn-cds
vault login -method=oidc -path=oidc role=namespace-reader
```

### Render the accounts-secrets.json.ctmpl template into accounts-secrets.json

```bash
consul-template -template="../accounts-secrets.json.ctmpl:accounts-secrets.json" -once
```

