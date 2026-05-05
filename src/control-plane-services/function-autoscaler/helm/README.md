<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Deploying via Helm

## SETUP

1. Setup Secrets and ConfigMaps
   1. NGC setup
      - Make sure you are a member of NGC org 'nvidian' and team 'omniverse'.  See https://docs.nvidia.com/ngc/gpu-cloud/ngc-private-registry-user-guide/#ngc-existing-account-setup
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
      helm repo add nvidian https://helm.ngc.nvidia.com/nvidian/omniverse \
      --username '$oauthtoken' \
      --password $NGC_TOKEN
      ```

1. Run Helm install
   1. Run in directory helm
   1. Add chart repo if you haven't already
      ```
      helm repo add ngc https://helm.ngc.nvidia.com/${NGC_ORG}/${NGC_TEAM} --username '$oauthtoken' --password $NGC_API_KEY
      ```
   1. Fetch helm sub-charts
      ```
      helm dependencies update
      ```
   1. Run helm to install
      - Default deploy
        - `helm install nvcf-autoscaler-service . -set-string app.image=" $DOCKER_REGISTRY_URL/$NGC_ORG/$NGC_TEAM/nvcf-autoscaler-service" --create-namespace --namespace $NAMESPACE`
          - Enviroment Variables needed
            - NGC_ORG: "shhh2i6mga69"
            - NGC_TEAM: "farm"
            - DOCKER_REGISTRY_URL: "nvcr.io"
            - APP_NAME: "nvcf-autoscaler-service"
      - If you get an error that they exist already, run: `helm delete nvcf-autoscaler-service -n $NAMESPACE`
  
1. Access services
   - `kubectl port-forward -n $NAMESPACE deployment/app 50053:50053`

1. Helm chart values.
   - See [values]((values.md))

[Test Service with Binary](https://github.com/grpc-ecosystem/grpc-health-probe/?tab=readme-ov-file)
