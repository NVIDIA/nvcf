# Deploying byoo-otel-collector on a Local MicroK8s Cluster

This assumes there is a MicroK8s cluster running locally or a [BYOC NVCF dev](https://nvb.nvidia.com/nvcf-dev) setup, and we want to deploy `byoo-otel-collector` to the cluster. These instructions also assume you are running commands from the `test/local/microk8s` directory.

Prerequisite:
```bash
# Enable required microk8s addons
microk8s install && microk8s enable gpu
microk8s enable helm

# Install kube-state-metrics
# Note: The `kube-state-metrics` installation is required for collecting Kubernetes metrics about container states, resource requests/limits, and other pod-related information.
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update
helm install kube-state-metrics prometheus-community/kube-state-metrics --namespace monitoring --create-namespace
```

This will deploy `kube-state-metrics`, which exposes metrics at `kube-state-metrics.monitoring:8080`. The OpenTelemetry collector is already configured to scrape metrics from this endpoint.

1. Use a [sample BYOO Pod configuration](pod.yaml). This Pod pulls an image from your team's container registry for byoo-otel-collector. To ensure that the container image can be pulled, perform

```bash
docker login registry.example.invalid:5005
```

and verify that the image can be pulled with this command
```bash
docker pull registry.example.invalid:5005/example-org/byoo-otel-collector:<image_tag>
```

2. Create a Kubernetes Secret.

```bash
microk8s kubectl create secret docker-registry gitlab-secret --from-file=.dockerconfigjson=$HOME/.docker/config.json
```

3. Create `accounts-secrets.json` with Consul-Template. 

    a. To install Consul-Template, set the `ARCH` variable based on your system:
    - For Linux (amd64): `ARCH=linux_amd64`
    - For Linux (arm64): `ARCH=linux_arm64`
    - For macOS (Intel): `ARCH=darwin_amd64`
    - For macOS (Apple Silicon): `ARCH=darwin_arm64`

    Run these commands:
    ```bash
    VERSION=0.39.1
    ARCH=<choose appropriate ARCH>
    wget -O /tmp/consul-template_${VERSION}_${ARCH}.zip \
    "https://releases.hashicorp.com/consul-template/${VERSION}/consul-template_${VERSION}_${ARCH}.zip"
    unzip /tmp/consul-template_${VERSION}_${ARCH}.zip -d /tmp/
    sudo mv /tmp/consul-template /usr/local/bin/

    consul-template -v
    ```

    b. Login to NVault

    Login to a NVault namespace you own; for example, `gfn-cds` for CDS Team.
    ```bash
    export VAULT_ADDR=https://vault.example.invalid
    export VAULT_NAMESPACE=<your_vault_namespace>
    vault login -method=oidc -path=oidc role=namespace-reader
    ```

    c. Render the accounts-secrets.json.ctmpl template into accounts-secrets.json

    ```bash
    consul-template -template="../accounts-secrets.json.ctmpl:accounts-secrets.json" -once
    ```

    These steps will output an `accounts-secrets.json` that will be used to create a ConfigMap.

    Retrieve the grafana-cloud-nvcf API key by running:

    ```bash
    vault kv get kv/grafana-cloud/nvidiacloudfunctions/access-policies/stack-919931-integration/token/stack-919931-integration-otlp-byoo
    ```

    Edit the outputted `accounts-secrets.json` to use 
    ```json
    "grafana-cloud-nvcf": {
        "instanceId": "919931",
        "apiKey": "<retrieved-Vault-API-key>"
    },
    ```

    For more information, please refer to the `Grafana cloud account for testing` section in this [Testing BYOO document](https://docs.google.com/document/d/1WN11BJIsQ4mzHpZImmcRlKCiticIAVyDyIAozsW68u0/edit?tab=t.ykrjtlri612j#heading=h.ldtpviweb8mz).

4. If a rendered OTelConfig [config.yaml](config.yaml) already exists, skip this step. Otherwise, render an OtelConfig.

    a. Clone the NVCF nvcf-otelconfig repository (internal), which holds the logic for OTelConfig creation.

    b. Run the following commands to use `testdata/create/main.go` in that repository to transform a sample telemetry JSON response into an OTel Config:

    ```bash
    go run testdata/create/main.go testdata/input1.json testdata/output vm container function
    ```

    This will output a file with a name format of `config.function_container.yaml`.

    Edit the below section of the outputted config file to use the grafana-cloud-nvcf authentication:

    ```yaml
    basicauth/GRAFANA_CLOUD-Grafana_prd-metrics:
    client_auth:
      password: ${file:/etc/byoo-otel-collector/secrets/grafana-cloud-nvcf-apiKey}
      username: ${file:/etc/byoo-otel-collector/secrets/grafana-cloud-nvcf-instanceId}
    ```

5. Create the configMaps required by the Pod - [otel-config](pod.yaml#L47) and [accounts-secrets](pod.yaml#L50).

```bash
# Creates otel-config configMap
microk8s kubectl create configmap otel-config --from-file=config.yaml=<path_to_rendered_otelconfig>

# Creates accounts-secrets configMap
microk8s kubectl create configmap accounts-secrets --from-file=accounts-secrets.json
```

6. Deploy the `byoo-otel-collector` Pod.

```bash
microk8s kubectl apply -f pod.yaml
```

A successfully deployed `byoo-otel-collector` will display these logs (in K9s):

![IMAGE_DESCRIPTION](../../images/localByooOtelCollector.png)

7. Send test POST curl

```bash
# Portforward onto port 4358 (for metrics)
microk8s kubectl port-forward pod/byoo-otel-collector 4358:4358

# Run POST command
sh test-grafana-cloud-metrics-check.sh
```

The following returned response indicates that the metrics were accepted and that no error message was returned.
```bash
{"partialSuccess":{}}
```

8. Cleanup

```bash
# Delete the pod
microk8s kubectl delete pod byoo-otel-collector

# Delete the configmaps
microk8s kubectl delete configmap otel-config
microk8s kubectl delete configmap accounts-secrets

# Delete the docker registry secret
microk8s kubectl delete secret gitlab-secret
```

Note: If you need to modify the config.yaml to remove the Grafana exporter section, you should edit the file and remove these sections:
- The `otlphttp/GRAFANA_CLOUD-Grafana_prd-metrics` from the exporters section
- The `basicauth/GRAFANA_CLOUD-Grafana_prd-metrics` from the extensions section
- Any references to these exporters in the pipelines section



