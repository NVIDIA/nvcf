# License compliance

## Purpose

This guide covers how to stay compliant across the repo. That includes importing dependencies with compatible licenses, self-auditing source files for correct headers, keeping `NOTICE` up to date, and using `dependencies.md` for internal auditing of third-party dependencies and their license status.

## Repo model

- `LICENSE` at the repo root carries the Apache 2.0 license text for repo-owned code.
- Individual files can carry a different license. File-level notices still control those files.
- `NOTICE` at the repo root is generated. It is an index of third-party notice and license file paths gathered from imported trees and checked-in third-party code.
- `dependencies.md` is an internal auditing helper. It enumerates third-party dependencies across the repo and records their license labels so we can review whether they are compliant. It is not the shipped license and notice bundle.

## Compliance goals

- Keep repo-owned files under the intended license and with the correct header.
- Preserve upstream notices and license terms on imported or vendored code.
- Avoid bringing in third-party dependencies whose licenses are outside the repo's accepted policy.
- Keep the generated `NOTICE` file in sync with imported and checked-in third-party notice and license files.
- Maintain a current internal dependency inventory in `dependencies.md` for review and audit work.

## MPL 2.0 in this Apache 2.0 repo

MPL 2.0 is one important example of a compatible non-Apache license that appears in this repo. It is file-level copyleft, so this repo can remain Apache 2.0 overall while specific files remain MPL 2.0.

For MPL-covered files:

- Keep the original copyright and license notices on the file.
- Keep the `SPDX-License-Identifier: MPL-2.0` notice on the file if that is the file's license.
- If you modify an MPL-covered file, that file stays MPL-covered.
- If you create a new file that contains copied MPL-covered source, that new file is also MPL-covered.
- Keep the corresponding MPL license text available in the repo and in distributed source. In practice here, that means preserving upstream `LICENSE` files and keeping `NOTICE` current.
- If we distribute binaries or container images, recipients must be able to get the source form of the MPL-covered files, including our modifications to those files. The normal way to satisfy that is to make the exact corresponding source tree or release source bundle available.

When you modify an MPL-covered file, add the NVIDIA `Not a contribution` header immediately below the MPL license block:

```text
// Not a contribution
// Changes made by NVIDIA CORPORATION & AFFILIATES enabling <XYZ> or otherwise documented as
// NVIDIA-proprietary are not a contribution and subject to the following terms and conditions:
// <NVIDIA-proprietary license from NVIDIA Proprietary - Legal - Confluence>
```

Replace `<XYZ>` with a short description of the NVIDIA feature or behavior enabled by the change, for example `ESS telemetry`, `Vault template behavior`, or `compatibility fixes`. See `src/compute-plane-services/ess-agent/mpl-files-modified.md` for more example feature labels used on modified MPL files in this repo.

What MPL 2.0 does not require here:

- It does not force the whole repo to become MPL.
- It does not automatically change unrelated Apache-only files.
- It does not replace the need to preserve upstream notices and license files.

`MPL-2.0` is on the current dependency allowlist in `.allowed-licenses.txt`, but that does not remove MPL obligations. The repo also reports MPL and other weak-copyleft licenses for review before shipping.

## Standard header for repo-owned Apache files

Use the full Apache 2.0 header on new repo-owned files unless the file is generated or intentionally carries a different upstream license.

Use this form for `//` comment languages such as Go, Java, and Rust:

```text
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
```

Use this form for `#` comment files such as Python, shell, properties, YAML, and Helm templates:

```text
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
```

## When not to add the Apache header

Do not replace an upstream third-party or MPL header with an Apache header.

Instead:

- Preserve the upstream header on third-party source files.
- Preserve upstream `LICENSE`, `NOTICE`, and `COPYING` files.
- Keep copied or modified MPL files marked as MPL.
- If a non-Apache directory must be checked by the header validator, add an explicit allowed SPDX exception instead of relabeling the files.

The current CI wrapper uses this explicit exception:

```bash
./tools/ci/check-license-headers \
  --allow-spdx-dir "src/compute-plane-services/ess-agent:MPL-2.0"
```

## What the repo currently checks

### `./tools/ci/check-license-headers`

This is the repo's source-header check.

- It scans the whole repo, or only the paths you pass on the command line.
- It currently checks: Go, Python, Java, Rust, Java `.properties`, and Helm chart `.yaml`, `.yml`, and `.tpl` files.
- Helm templates are only checked under chart roots, meaning directories that contain `Chart.yaml`.
- It looks for the required Apache snippets within the first 24 lines of the file.
- The required Apache snippets are:
  - `SPDX-FileCopyrightText:`
  - `SPDX-License-Identifier: Apache-2.0`
  - `Licensed under the Apache License, Version 2.0`
- It can allow alternate SPDX identifiers per directory with `--allow-spdx-dir DIR:LICENSE_ID`.
- It skips generated files when the file looks generated and includes both `Code generated` and `DO NOT EDIT` in the first 30 lines.
- It skips common non-source and dependency directories including `vendor`, `node_modules`, virtualenv caches, and similar directories.
- It groups failures by synthetic import path from `imports.yaml` or by top-level repo directory to make reports actionable.

Useful commands:

```bash
<<<<<<< Updated upstream
./tools/ci/check-license-headers
./tools/ci/check-license-headers tools
./tools/ci/check-license-headers src/libraries/go/nvcf-go
=======
./tools/scripts/check-license-headers
./tools/scripts/check-license-headers tools
./tools/scripts/check-license-headers src/libraries/go/lib
>>>>>>> Stashed changes
```

### `./tools/scripts/collect-notices` and `./tools/scripts/update-license`

These scripts maintain the root `NOTICE`.

- `collect-notices` scans the whole repo tree.
- It collects repo `NOTICE`, `NOTICE.txt`, and `NOTICE.md` files outside checked-in third-party trees.
- It also walks checked-in third-party paths such as `vendor/`, `third_party/`, `third-party/`, `3rdparty/`, and `node_modules/`.
- Under those third-party paths it records `NOTICE`, `LICENSE`, `LICENCE`, and `COPYING` files, as long as they are non-empty and not source files with misleading names.
- The generated root `NOTICE` is excluded from the input scan so the tool does not re-ingest its own output.
- `update-license` is the simple wrapper that regenerates the root `NOTICE`.

Useful commands:

```bash
./tools/scripts/update-license
./tools/scripts/collect-notices --output /tmp/NOTICE
```

### `./tools/ci/check-license`

This is the root CI entrypoint for license hygiene.

It fails if:

- `LICENSE` is missing or empty.
- `NOTICE` is missing.
- `NOTICE` is out of sync with the generated output from `collect-notices`.
- source headers fail `check-license-headers`.
- a monorepo-native Go root contains `github.com/NVIDIA/...` module paths or imports in `go.mod`, `go.work`, or `.go` files.

Today it runs `check-license-headers` with the `ess-agent` MPL exception shown above.

### `./tools/ci/check-dependency-licenses`

This is the current dependency-license gate.

- It currently covers Go modules with checked-in `vendor/` directories.
- It runs `go-licenses check ./...` with the allowlist from `.allowed-licenses.txt`.
- It stores ratchet baselines under `tools/check-dependency-licenses-baselines/` so existing known issues do not block the repo, but new ones do.
- It can regenerate those baselines with `--update-baseline`.
- It independently scans `vendor/` trees for missing `LICENSE` or `COPYING` files.
- It prints an informational `Weak copyleft (review before shipping)` section for licenses including `MPL-2.0`, LGPL variants, CDDL, and EPL.

Useful commands:

```bash
./tools/ci/check-dependency-licenses
./tools/ci/check-dependency-licenses --list-modules
./tools/ci/check-dependency-licenses --update-baseline
```

Only update baselines with justification. A baseline update is a ratchet exception, not a fix.

### `.allowed-licenses.txt`

This file is the allowlist for `check-dependency-licenses`.

The current list includes:

- `Apache-2.0`
- `MIT`
- `BSD-2-Clause`
- `BSD-3-Clause`
- `BSD-4-Clause`
- `ISC`
- `Unlicense`
- `CC0-1.0`
- `Zlib`
- `PSF-2.0`
- `BlueOak-1.0.0`
- `MPL-2.0`

Allowlisted does not mean obligation-free. It only means the automated dependency gate permits the license class.

## CI jobs

The root `.gitlab-ci.yml` currently runs these compliance jobs in the `Prerequisites` stage:

- `check-license`: runs `./tools/ci/check-license`
- `check-dependency-licenses`: runs `./tools/ci/check-dependency-licenses`

These jobs run on merge requests and the default branch. There are also schedule and web-trigger cases in the root pipeline.

Imported projects keep their own source-level CI, but the umbrella repo adds these root-level compliance checks on top.

## Inventory and review helpers

### `go run ./tools/collect-dependencies`

This tool generates `dependencies.md`, the shared internal audit rollup across Go, Rust, Python, Java, and Helm.

Use it when:

- imported trees or dependency manifests changed
- you want a reviewable rollup of new third-party dependencies
- you need license hints across the monorepo without opening every subtree

Read [`tools/collect-dependencies/README.md`](tools/collect-dependencies/README.md) for caveats, network behavior, and environment flags. This tool is useful for review and open source review workflow, but it is not the same thing as the license bundle and it is not the main CI gate.

Use it to enumerate the repo's third-party dependencies and verify that the reported license labels stay within the repo's compliance expectations.

Run it when you add a third-party dependency to see whether it is new to the monorepo. Report new third-party dependencies in the OSRB NVBUG linked from [`tools/collect-dependencies/README.md`](tools/collect-dependencies/README.md).

These inventories are indicative. They do not replace legal review or per-component `NOTICE` and `LICENSE` bundles if your process requires them.

## Tests that back the tooling

The repo also has focused tests for the compliance scripts:

- `tools/scripts/test/test-check-license-headers`
- `tools/scripts/test/test-check-dependency-licenses`
- `tools/scripts/test/test-check-dependency-licenses-fallbacks`

If you change checker behavior, update the tests in the same change.

## Recommended workflow

1. Add the standard Apache header to every new repo-owned source or config file that should carry the repo default license.
2. If you import or copy third-party code, preserve its original header and license files. Do not relabel MPL or other upstream code as Apache.
3. Run `./tools/ci/check-license-headers` on the affected paths to self-audit source headers.
4. If a Go module's `vendor/` tree changed, run `./tools/ci/check-dependency-licenses` to catch new dependency license issues and review any weak-copyleft output.
5. Refresh `dependencies.md` with `go run ./tools/collect-dependencies` when you need the current internal audit inventory of third-party dependencies and license labels.
6. If imported repos or checked-in third-party notice or license files changed, run `./tools/scripts/update-license`.
7. Before shipping binaries or container images, confirm that required source availability, license texts, and notice materials are shipped or referenced correctly, including for MPL-covered files.

## Practical rules of thumb

- New NVIDIA-owned source file: add the Apache header.
- Vendored third-party file: keep the upstream header and license files.
- MPL file you modified: keep it MPL and add the NVIDIA `Not a contribution` header below the MPL license block.
- New file created by copying MPL source: mark that new file MPL.
- `NOTICE` changed because imports or vendored licenses changed: run `./tools/scripts/update-license`.
- New vendored dependency in a Go module: run `./tools/ci/check-dependency-licenses` and review the output, especially weak-copyleft lines.
