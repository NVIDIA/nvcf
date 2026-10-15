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

use std::process::Command;

use anyhow::{Context, bail};

pub(super) struct ImageRefs {
    pub(super) stargate: String,
    pub(super) mock_dynamo: String,
    pub(super) pylon: String,
}

pub(super) fn resolve_image_refs() -> anyhow::Result<ImageRefs> {
    Ok(ImageRefs {
        stargate: resolve_image_with_override("STARGATE_BENCH_STARGATE_IMAGE", "stargate-dev")?,
        mock_dynamo: resolve_image_with_override(
            "STARGATE_BENCH_MOCK_DYNAMO_IMAGE",
            "mock-dynamo-dev",
        )?,
        pylon: resolve_image_with_override("STARGATE_BENCH_PYLON_IMAGE", "pylon-dev")?,
    })
}

fn resolve_image_with_override(env_var: &str, name: &str) -> anyhow::Result<String> {
    if let Ok(image) = std::env::var(env_var) {
        let image = image.trim();
        if !image.is_empty() {
            return Ok(image.to_string());
        }
    }

    resolve_image(env_var, name)
}

fn resolve_image(env_var: &str, name: &str) -> anyhow::Result<String> {
    let output = Command::new("docker")
        .arg("images")
        .arg("--format")
        .arg("{{.Repository}}:{{.Tag}}")
        .output()
        .with_context(|| format!("failed to query docker images for {name}"))?;
    if !output.status.success() {
        bail!("docker images failed while resolving {name}");
    }
    let stdout = String::from_utf8_lossy(&output.stdout);
    stdout
        .lines()
        .find(|line| tilt_image_matches(line, name))
        .map(str::to_owned)
        .ok_or_else(|| {
            anyhow::anyhow!(
                "failed to resolve benchmark image for {name}; set {env_var} to a cluster-visible image reference before running Kubernetes benchmarks"
            )
        })
}

pub(super) fn tilt_image_matches(image: &str, name: &str) -> bool {
    let Some((repository, tag)) = image.rsplit_once(':') else {
        return false;
    };
    if repository != name && !repository.ends_with(&format!("/{name}")) {
        return false;
    }
    tag.starts_with("tilt-")
}
