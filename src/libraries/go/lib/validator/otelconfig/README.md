# Validator

This is a script that validates the byoo metrics and labels are correct.

## Usage

### Start Instance (optional)

Start a instance by horde or aws. The host name will be saved in `instance_info.env` file. The host name format is `${OWNER}-validator-$(date +%s)` e.g. `lachen-validator-1712575200`. In CI job, the host name will be `nvcf-byoo-validator-$CI_JOB_ID`.

```
./start_instance.sh --preferred-cloud-backend=none --output-file=instance_info.env
# --preferred-cloud-backend(optional): horde or aws, if not set or set to none, we will check and start horde first and then aws if horde is not available.
# --output-file(optional): output instance_info to instance_info.env file including ip, instance_id and cloud_backend. Now we support horde and aws.
```


### Setup MicroK8s Environment

* Setup Remote K8s(non-gfn) Environment
```
./validator/setup.sh --ip=10.176.221.36 --user=horde --password=<pwd> --env=k8s --kubeconfig_output_path=$(pwd) --cloud-backend=horde
```
* Setup Remote VM(gfn) Environment
    * This deploys gfn specific kube-state-metrics 
```
./validator/setup.sh --ip=10.176.221.36 --user=horde --password=<pwd> --env=vm --kubeconfig_output_path=$(pwd) --cloud-backend=horde
```
* (Or) Setup Environment on Local Machine
```
sudo ./validator/setup.sh --local --env=k8s --kubeconfig_output_path=$(pwd)
```
* A kubeconfig file called `.microk8s_kubeconfig.yaml` will be created under `$kubeconfig_output_path`

### Deploy Otel Collector for Validation
* Export the kubeconfig file created from previous step
```
export KUBECONFIG=$(pwd)/.microk8s_kubeconfig.yaml
```
* Deploy otel collector with vm-helm config
```
export COLLECTOR_CONFIG=vm-helm
helm upgrade --create-namespace --install --wait --timeout=60s \
    --namespace otel-collector-$COLLECTOR_CONFIG \
    opentelemetry-collector validator/charts/otel-collector/ \
    -f ./validator/charts/otel-collector/values-$COLLECTOR_CONFIG.yaml
```
* Deploy otel collector with vm-container config
```
export COLLECTOR_CONFIG=vm-container
helm upgrade --create-namespace --install --wait --timeout=60s \
    --namespace otel-collector-$COLLECTOR_CONFIG \
    opentelemetry-collector validator/charts/otel-collector/ \
    -f ./validator/charts/otel-collector/values-$COLLECTOR_CONFIG.yaml
```

### CI Validation Flow

* Start 2 Instances by Horde
  * 1 for GFN (VM) Instance
  * 1 for Non-GFN (k8s) Instance
* Install MicroK8s Environment on Instance
  * Use submodule to get packer and byoo-prometheus latest version
    * https://github.com/NVIDIA/nvcf/monitoring/nvcf-otelconfig/-/blob/main/.gitlab-ci-validator.yml?ref_type=heads#L14-15. Refer [doc](https://docs.gitlab.com/ci/runners/git_submodules/#use-git-submodules-in-cicd-jobs)
  * For Non-GFN (k8s)
    * Enable MicroK8s addons (DNS, Storage, GPU, Prometheus)
    * Deploy kube-state-metrics with helm chart
  * For GFN (VM)
    * Enable MicroK8s addons (DNS, Storage, GPU)
    * Install [byoo-prometheus](https://github.com/NVIDIA/nvcf/monitoring/byoo-prometheus)
    * Deploy kube-state-metrics, fetch [kube-state-metrics-overrides.yml](https://github.com/NVIDIA/devops/packer/-/blob/master/ubuntu22/ansible/roles/microk8s/templates/kube-state-metrics-overrides.yml.j2?ref_type=heads) from packer
* Deploy Otel Collector
  * GFN with vm-helm/vm-container values file
  * Non-GFN with k8s-helm/k8s-container values file
* Validate byoo metrics and labels from Grafana Cloud API
* Teardown instances
* Flow Diagram
  * ![CI Validation Flow](docs/valid_stage.png)
  * [Source](https://drive.google.com/file/d/1xK-WFZu6_nHbOuDZ2Lw8bGt5YSFvFnUB/view?usp=sharing)
