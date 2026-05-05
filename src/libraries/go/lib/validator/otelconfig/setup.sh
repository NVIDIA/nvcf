#!/bin/bash
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0


# Parse command line arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --ip=*)
      ip_address="${1#*=}"
      shift
      ;;
    --user=*)
      user="${1#*=}"
      shift
      ;;
    --password=*)
      ssh_password="${1#*=}"
      shift
      ;;
    --kubeconfig_output_path=*)
      kubeconfig_output_path="${1#*=}"
      shift
      ;;
    --env=*)
      env="${1#*=}"
      shift
      ;;
    --local)
      run_mode="local"
      shift
      ;;
    --cloud-backend=*)
      cloud_backend="${1#*=}"
      shift
      ;;      
    --aws-key-file-path=*)
      aws_key_file_path="${1#*=}"
      shift
      ;;            
    *)
      echo "Unknown parameter: $1"
      echo "Usage: $0 [--ip=IP_ADDRESS] [--user=USERNAME] [--password=SSH_PASSWORD] [--kubeconfig_output_path=KUBECONFIG_OUTPUT_PATH] [--env=ENV(k8s|vm)] [--local] [--cloud-backend=INSTANCE_TYPE(horde|aws)]"
      exit 1
      ;;
  esac
done

# Set script directory
SCRIPT_DIR=$(realpath $(dirname "$0"))
KUBECONFIG=$kubeconfig_output_path/.microk8s_kubeconfig.yaml


enable_microk8s_addons() {
  microk8s_cmd="${ssh_cmd} \"chmod +x $REMOTE_DIR/setup-microk8s.sh && sudo bash $REMOTE_DIR/setup-microk8s.sh $env\""
  eval $microk8s_cmd
  ret=$?
  if [ $ret -ne 0 ]; then
    echo "===== Failed to setup MicroK8s on $ip_address ====="
    return 1
  fi
}

# Function to perform helm upgrade/install
perform_helm_install() {
  helm upgrade --install --create-namespace --wait --timeout=300s \
    --namespace $KSM_NAMESPACE \
    -f $SCRIPT_DIR/charts/kube-state-metrics/values-$env.yaml \
    kube-state-metrics $SCRIPT_DIR/charts/kube-state-metrics
}


byoo_prometheus_install() {
  pushd $SCRIPT_DIR/byoo-prometheus/charts/nvcf-byoo-prometheus
  kubectl config current-context
  helm list
  kubectl version
  sed -i 's|https://helm.ngc.nvidia.com/nv-ngc-devops|https://prometheus-community.github.io/helm-charts|g' Chart.yaml
  helm repo update
  helm dependency build
  helm install nvcf-byoo-prometheus --wait -n monitoring -f values-horde-microk8s-byoc.yaml .

  ret=$?
  if [ $ret -ne 0 ]; then
    echo "===== Failed to install byoo-prometheus on $ip_address ====="
    popd
    return 1
  fi
  popd  
}

# scp the setup script to the remote machine
scp_setup_script() {
  if [[ $cloud_backend == "horde" ]]; then
      sshpass -p "$ssh_password" scp "$SCRIPT_DIR/setup-microk8s.sh" $user@$ip_address:$REMOTE_DIR/
  elif [[ $cloud_backend == "aws" ]]; then
      scp -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -i $aws_key_file_path $SCRIPT_DIR/setup-microk8s.sh $user@$ip_address:$REMOTE_DIR/
  fi
}

gen_kube_config() {
  gen_ssh_cmd="microk8s config"
  if [[ $cloud_backend == "aws" ]]; then
    gen_ssh_cmd="sudo microk8s config"
  fi
  get_kube_config_cmd="${ssh_cmd} \"${gen_ssh_cmd}\""
  echo "===== Running command: $get_kube_config_cmd ====="
  eval $get_kube_config_cmd > $KUBECONFIG
}

# General retry function
retry_action() {
  local action=$1
  local retry_count=0
  local MAX_RETRIES=$2

  until $action; do
    retry_count=$((retry_count + 1))
    if [ $retry_count -ge $MAX_RETRIES ]; then
      echo "Action '$action' failed after $MAX_RETRIES attempts."
      exit 1
    fi
    echo "Action '$action' failed. Retrying ($retry_count/$MAX_RETRIES)..."
    sleep 10  # Wait for 10 seconds before retrying
  done
}




if [ "$run_mode" == "local" ]; then
  echo "===== Setting up MicroK8s locally====="
  chmod +x $SCRIPT_DIR/setup-microk8s.sh && sudo bash $SCRIPT_DIR/setup-microk8s.sh
  
  echo "===== Saving kubeconfig from $ip_address to $KUBECONFIG ====="
  microk8s config > $KUBECONFIG
else

  if [[ $cloud_backend == "horde" ]]; then
      user="horde"
      ssh_cmd="sshpass -p ${ssh_password} ssh ${user}@${ip_address}"
  elif [[ $cloud_backend == "aws" ]]; then
      user="ubuntu"
      ssh_cmd="ssh -i $aws_key_file_path  -o 'StrictHostKeyChecking no' $user@$ip_address"
  else
      echo "Unknown cloud backend: $cloud_backend"
      exit 1
  fi

  REMOTE_DIR="/home/$user"

  echo "===== Copying setup script to $REMOTE_DIR on $ip_address ====="
  retry_action scp_setup_script 3

  echo "===== Setting up MicroK8s on $ip_address ====="
  retry_action enable_microk8s_addons 3

  echo "===== Saving kubeconfig from $ip_address to $KUBECONFIG ====="
  retry_action gen_kube_config 3

fi


export KUBECONFIG=$KUBECONFIG
KSM_NAMESPACE="monitoring"  
kubectl create namespace $KSM_NAMESPACE




if [ "$env" == "k8s" ]; then
  echo "===== Deploying nvcf-byoo-prometheus on $env instance ====="
  retry_action byoo_prometheus_install 3
else
  sed "s/{{ ksm_namespace }}/$KSM_NAMESPACE/g" $SCRIPT_DIR/packer/ubuntu22/ansible/roles/microk8s/templates/kube-state-metrics-overrides.yml.j2 >> kube-state-metrics-overrides.yml
  yq eval '. as $item ireduce({}; . * {"kube-state-metrics": $item})' kube-state-metrics-overrides.yml > $SCRIPT_DIR/charts/kube-state-metrics/values-$env.yaml
fi

echo "===== Deploying kube-state-metrics with $env values file ====="
retry_action perform_helm_install 3
