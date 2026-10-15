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

# Application settings

The function autoscaler loads settings from an optional YAML, JSON, or TOML
file and then applies environment-variable overrides. Environment variables
have the highest precedence.

## Configuration file

Pass a settings file with `--config` or the `CONFIG` environment variable:

```bash
cargo run --bin server -- --config crates/server/resources/settings-local.yaml
```

The bundled `resources/settings-local.yaml` shows the supported structure.
Settings omitted from a file use their Rust defaults when the corresponding
settings type defines one.

## Environment variables

Use uppercase field names and `__` between nested keys. For example:

```bash
SCALING__LOOKBACK_SECONDS=90 \
SCALING__SCALE_TO_ZERO_IDLE_TIMEOUT_SECONDS=300 \
SERVER__PORT=8083 \
cargo run --bin server
```

These values map to `scaling.lookback_seconds`,
`scaling.scale_to_zero_idle_timeout_seconds`, and `server.port`. Environment
variables override values loaded from the configuration file.

The command-line interface selects the configuration file; it does not accept
arbitrary setting overrides. Use environment variables for per-run overrides.
