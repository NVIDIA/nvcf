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

### 1. Run the [build_zips.sh](../scripts/build_zips.sh) script to generate the zip files. A version must be provided as an argument; this will be used in the zip file name

```bash
# Example using `v1.0.5` as the version
nv_releases/scripts/build_zips.sh v1.0.5
```

### 2. After the script is finished, the compiled binaries will be in the [nv_releases/builds](../builds) directory

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

## Container releases

Use the component's [Bazel image build](../../README.md#build-with-bazel) for
local validation and the [monorepo release workflow](../../../../../docs/dev/github-release-process.md)
for source releases. The former local Docker retag/push walkthrough is retired
as a release procedure. The zip helper above is a local packaging utility; it
does not create a source release or publish a container.
