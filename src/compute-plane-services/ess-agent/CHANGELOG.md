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
# ESS Agent Releases

## Current Release

### v1.1.0

**Released**: 2026-01-30

#### Fixes

- fix(cve): resolve cve in go-jose (CVE-2024-28180, BDSA-2023-3257 ) [KAZD-10650]
- fix(cve): resolve cve in google.golang.org/protobuf [KAZD-10647]


#### Chore/Build

- build(distroless): upgrade distroless version from 3.1.9 to 4.0.1 [KAZD-10649]
- build(go): upgrade go version from 1.23.10 to 1.25.6 [KAZD-10636]

### v1.0.5

**Released**: 2025-06-26

#### Features

- feat(config): change default kill-signal to SIGTERM [KAZD-9337]
- feat(templates): stop processing template on client error if configured so [KAZD-9303]

#### Chore/Build

- chore(upgrade): update consul/api dep to v1.32.1 [KAZD-9320]
- chore(build): upgrade go to 1.23.10


## Previous Releases

### v1.0.4

**Released**: 2025-05-15

#### Fixes

- fix(leases): correctly fixes secret lease time drift [KAZD-9048]

### v1.0.3

**Released**: 2025-04-28

#### Warning
All clients should skip using v1.0.3 and use v1.0.4 which corrects a new issue introduced in 1.0.3 regarding output of agent templates.

#### Fixes

- fix(leases): fix secret lease time drift [KAZD-9048]



### v1.0.2

**Released**: 2024-12-20

#### Fixes

- fix(init): revert behavior of ESS_AGENT_INIT to check for presence and value = `true` [KAZD-8180]
- fix(log): use namespace of agent for log message instead of server response headers [KAZD-8168]


### v1.0.1

**Released**: 2024-11-25

#### Features

- feat(metrics): add `ess_agent_id` label to metrics and logs [KAZD-7886]
- feat(metrics): add `ess_templates_request_total` metric to track all api requests to ESS service [KAZD-7945]
- feat(exit_on_client_error): only exit agent on template when 40X response happens on the first attempt to render template [KAZD-7945]

#### Chore/Build
- build(docker): upgrade go distroless docker image to 3.1.3 to resolve libssl CVEs [KAZD-7891]

<br/>


### v1.0.0

**Released**: 2024-09-11

#### Features

- feat(template): skip first template render if render under 1 minutes [KAZD-7111]
- feat(metrics): add telemetry support under ess-agent [KAZD-7110]
- feat(env): add support for the init env [KAZD-7107]
