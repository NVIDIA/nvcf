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

use std::env;
use std::path::Path;

fn main() {
    rerun_if_git_head_changes();
    built::write_built_file().expect("failed to collect Stargate build metadata");
}

fn rerun_if_git_head_changes() {
    let manifest_dir = env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR should be set");
    let Ok(repository) = git2::Repository::discover(manifest_dir) else {
        return;
    };

    rerun_if_exists(repository.path().join("HEAD"));
    rerun_if_exists(repository.commondir().join("packed-refs"));
    if let Ok(head) = repository.head()
        && let Some(reference) = head.name().filter(|name| name.starts_with("refs/"))
    {
        rerun_if_exists(repository.commondir().join(reference));
    }
}

fn rerun_if_exists(path: impl AsRef<Path>) {
    let path = path.as_ref();
    if path.exists() {
        println!("cargo::rerun-if-changed={}", path.display());
    }
}
