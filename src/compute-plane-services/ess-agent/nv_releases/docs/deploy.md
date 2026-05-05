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
# Deployment
## Building Zip Files
### 1. Run the [build_zips.sh](../scripts/build_zips.sh) script to generate the zip files. A version must be provided as an argument; this will be used in the zip file name.
```bash
# Example using `v1.0.5` as the version
nv_releases/scripts/build_zips.sh v1.0.5
```

### 2. After the script is finished, the compiled binaries will be in the [nv_releases/builds](../builds) directory:
```text
darwin_amd64/
    ess-agent
darwin_arm64/
    ess-agent
darwin_universal/
    ess-agent
linux_amd64/
    ess-agent
linux_arm64/
    ess-agent
windows_amd64/
    ess-agent.exe
windows_arm64/
    ess-agent.exe
```
and the zips will be in the [nv_releases/zips](../zips) directory:
```text
ess-agent_v1.0.5_darwin_amd64.zip
ess-agent_v1.0.5_darwin_arm64.zip
ess-agent_v1.0.5_darwin_universal.zip
ess-agent_v1.0.5_linux_amd64.zip
ess-agent_v1.0.5_linux_arm64.zip
ess-agent_v1.0.5_windows_amd64.zip
ess-agent_v1.0.5_windows_arm64.zip
```
Note: these files are not to be committed to git and are meant to be blocked from being committed to git via the `.gitignore` files in each of those directories.

## Building Distroless Container Images
### Prerequisites
- Colima ([Github](https://github.com/abiosoft/colima))

### 1. Enable Containerd image store in Docker Engine

Enabling the Containerd image store will allow building and storing multi-arch images in you local image registry. More info can be found in the [Docker documentation](https://docs.docker.com/storage/containerd/).

Edit Colima's config file instead, typically found at `~/.colima/default/colima.yaml`. Add the following:
```yaml
docker:
  features:
    containerd-snapshotter: true
```

Once the settings have been modified, you will need to initialize a new Colima instance (normal restart will not work):
```bash
colima delete
colima start
```

To verify the Containerd image store is being used, run the following command:
```bash
docker info -f '{{ .DriverStatus }}'
```
The output should have this following:
```bash
[[driver-type io.containerd.snapshotter.v1]]
```

### 2. Set Docker Buildx builder instance to Colima
```bash
docker buildx create --use colima
```

Note: The `colima` node should have the platforms you require for your multi-arch image: 
```bash
NAME/NODE     DRIVER/ENDPOINT  STATUS   BUILDKIT             PLATFORMS
colima *      docker                                         
  colima      colima           running  v0.11.7+435cb77e369c linux/arm64, linux/amd64, linux/amd64/v2
```

### 3. Run [nv_releases/scripts/build_distroless.sh](..%2Fscripts%2Fbuild_distroless.sh)

This script will build the binaries, then subsequently build the multi-arch Distroless image. They will be tagged locally as `ess-agent/distroless-go-multi-arch`. 

Below is an example:
```bash
# Give version to build for as a command-line argument
# Below uses 1.0.5 as an example
nv_releases/scripts/build_distroless.sh 1.0.5
```

### 4. Tag and Deploy

Re-tag local image and deploy to desired image registry. 

Below is an example:
```bash
# In this example, image version 1.0.5 is to be deployed to URM:
docker tag ess-agent/distroless-go-multi-arch:1.0.5 urm.nvidia.com/<internal-docker-repo>/ess/ess-agent:1.0.5
docker login urm.nvidia.com
docker push urm.nvidia.com/<internal-docker-repo>/ess/ess-agent:1.0.5
```
