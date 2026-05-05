# Deploying via Helm

## SETUP

1. Setup Secrets and ConfigMaps
   1. NGC setup
      - Make sure you have access to the `nvcf-internal/nvcf-dev` NGC org/team.
      - If you don't already have one, generate a Personal API Key.  See https://docs.nvidia.com/ngc/gpu-cloud/ngc-user-guide/#ngc-api-keys
      - Set `$NGC_TOKEN` to your Personal API Key
   1. Need Secret to pull docker container in K8s
      - External Container (NGC)
      - Replace `$NAMESPACE` with namespace name
      ```
      kubectl create secret docker-registry dockerReg \
      --docker-server=https://nvcr.io \
      --docker-username=$oauthtoken \
      --docker-password="$NGC_TOKEN" \
      --docker-email="user@nvidia.com" \
      --namespace=$NAMESPACE
      ```
   1. Need Secret to fetch helm sub-charts from NGC
      ```
      helm repo add nvcf-dev https://helm.ngc.nvidia.com/nvcf-internal/nvcf-dev \
      --username '$oauthtoken' \
      --password $NGC_TOKEN
      ```

1. Run Helm install
   1. Run in directory helm
   1. Fetch helm sub-charts
      ```
      helm dependencies update
      ```
   1. Run helm to install
      - Default deploy
        - `helm install nvcf-invocation-service . --set-string app.image="nvcr.io/nvcf-internal/nvcf-dev/nvcf-invocation-service" --create-namespace --namespace $NAMESPACE`
      - If you get an error that they exist already, run: `helm delete nvcf-invocation-service -n $NAMESPACE`
  
1. Access services
   - `kubectl port-forward -n $NAMESPACE deployment/app 50053:50053`

[Test Service with Binary](https://github.com/grpc-ecosystem/grpc-health-probe/?tab=readme-ov-file)
