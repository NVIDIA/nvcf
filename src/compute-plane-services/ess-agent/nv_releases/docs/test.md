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
# Contents
 - [Testing the ESS agent with Mockoon](#testing-the-ess-agent-container-with-mockoon)

## Testing the ESS Agent Container with Mockoon

1. Download [Mockoon](https://github.com/mockoon/mockoon)

2. Import the following config as a new Mockoon environment: [mockoon-config.json](..%2Ftest%2Fmockoon%2Fmockoon-config.json)

3. In new Mockoon environment, start API server

4. Build Distroless container (follow [deploy.md](deploy.md) for further instructions)

5. Run the following command to run the container against Mockoon (see replacements below):
```bash
# Replace <REPO_ROOT_DIR> with the path to the repository's root directory on your local machine
# Replace <VERSION> with the image tag to run
docker run --rm -it \
-e "ESS_AGENT_INIT=true" \
-e "SECRET_PATH=functions/ca713143-76d6-4afe-beba-fb23923446f6/secrets" \
--mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/configs,target=/ess-agent/file/configs \
--mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/templates,target=/ess-agent/file/templates \
--mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/secrets,target=/ess-agent/file/secrets \
ess-agent/distroless-go-multi-arch:<VERSION> \
-config=/ess-agent/file/configs/config-docker.hcl
```
The rendered secrets should be found in the [nv_releases/test/secrets](..%2Ftest%2Fsecrets) directory.

### Commands

### Run local tag against Mockoon:

Init container
```bash
docker run --rm -it \
    -e "ESS_AGENT_INIT=true" \
    -e "SECRET_PATH=functions/ca713143-76d6-4afe-beba-fb23923446f6/secrets" \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/configs,target=/ess-agent/file/configs \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/templates,target=/ess-agent/file/templates \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/secrets,target=/ess-agent/file/secrets \
    ess-agent/distroless-go-multi-arch:<VERSION> \
    -config=/ess-agent/file/configs/config-docker.hcl
```

Sidecar container
```bash
docker run --rm -it \
    -e "SECRET_PATH=functions/ca713143-76d6-4afe-beba-fb23923446f6/secrets" \
    -p 9103:9103 \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/configs,target=/ess-agent/file/configs \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/templates,target=/ess-agent/file/templates \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/secrets,target=/ess-agent/file/secrets \
    ess-agent/distroless-go-multi-arch:<VERSION> \
    -config=/ess-agent/file/configs/config-with-non-tls-telemetry-docker.hcl
```

### Run URM tag against Mockoon:
```bash
docker run --rm -it \
    -e "ESS_AGENT_INIT=true" \
    -e "SECRET_PATH=functions/ca713143-76d6-4afe-beba-fb23923446f6/secrets" \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/configs,target=/ess-agent/file/configs \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/templates,target=/ess-agent/file/templates \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/secrets,target=/ess-agent/file/secrets \
    urm.nvidia.com/<internal-docker-repo>/ess/ess-agent:<VERSION> \
    -config=/ess-agent/file/configs/config-docker.hcl
```

### Run nvcr.io tag against Mockoon:
```bash
docker run --rm -it \
    -e "ESS_AGENT_INIT=true" \
    -e "SECRET_PATH=functions/ca713143-76d6-4afe-beba-fb23923446f6/secrets" \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/configs,target=/ess-agent/file/configs \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/templates,target=/ess-agent/file/templates \
    --mount type=bind,source=<REPO_ROOT_DIR>/nv_releases/test/secrets,target=/ess-agent/file/secrets \
    urm.nvidia.com/<internal-docker-repo>/ess/ess-agent::<VERSION> \
    -config=/ess-agent/file/configs/config-docker.hcl
```


### Run integration tests

Pre-requisite: Please start the Mockoon server as shown above.

Note: some tests may require `kubectl` and a local Kubernetes cluster. If using Colima, do the following:
```
# Start (or restart) Colima with the --kubernetes argument
colima start --kubernetes

# Install kubectl via Homebrew
brew install kubectl
```
For more information on initial Colima setup, see the [deployment docs](deploy.md#prerequisites).

#### Build and run all tests
```
make integration-test
```

#### Run specific test by number
```
make integration-test TEST=01  # Run timing drift test
make integration-test TEST=02  # Run SIGTERM handling test
```

#### Skip build and run tests
```
make integration-test SKIP_BUILD=true              # Run all tests without rebuild
make integration-test SKIP_BUILD=true TEST=02      # Run specific test without rebuild
```

#### Available tests
Path: nv_release/test/integration-tests

- `01` - Timing drift test
- `02` - SIGTERM handling test (requires Kubernetes cluster)
