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

//! Embed the git commit this build came from as `NVSTOR_PEER_COMMIT`, and
//! the version it reports as `NVSTOR_PEER_VERSION` (`nvstor-peer
//! --version`, the `--json` line).
//!
//! The version is the release version given in `NVSTOR_PEER_RELEASE` when
//! that is a SemVer version (the Bazel build stamps it from the release
//! tag; an unreleased build stamps a non-version, `mr-<sha>`), else the
//! crate version.
//!
//! The commit comes from, in order: the `NVSTOR_PEER_COMMIT` environment
//! variable (a container build, where the repository is mounted without
//! a usable `.git`: a worktree's `.git` file names a host path; the
//! cross-build scripts set it, with `-dirty` for uncommitted changes),
//! then `git rev-parse HEAD`, then "unknown".

use std::path::{Path, PathBuf};
use std::process::Command;

fn git(args: &[&str]) -> Option<String> {
    let out = Command::new("git").args(args).output().ok()?;
    if !out.status.success() {
        return None;
    }
    let s = String::from_utf8(out.stdout).ok()?.trim().to_string();
    (!s.is_empty()).then_some(s)
}

/// A commit as given from outside: 7 to 40 lowercase hex digits,
/// optionally followed by `-dirty`.
fn valid_commit(c: &str) -> bool {
    let hex = c.strip_suffix("-dirty").unwrap_or(c);
    (7..=40).contains(&hex.len())
        && hex
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

/// A SemVer version without build metadata: MAJOR.MINOR.PATCH, each a
/// number without leading zeros, optionally followed by `-` and dot-separated
/// prerelease identifiers of ASCII letters, digits and `-`.
fn valid_version(v: &str) -> bool {
    let (core, pre) = match v.split_once('-') {
        Some((core, pre)) => (core, Some(pre)),
        None => (v, None),
    };
    let numbers: Vec<&str> = core.split('.').collect();
    let number = |n: &&str| {
        !n.is_empty()
            && n.bytes().all(|b| b.is_ascii_digit())
            && (n.len() == 1 || !n.starts_with('0'))
    };
    numbers.len() == 3
        && numbers.iter().all(number)
        && pre.is_none_or(|p| {
            p.split('.').all(|id| {
                !id.is_empty() && id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
            })
        })
}

fn main() {
    println!("cargo:rerun-if-env-changed=NVSTOR_PEER_RELEASE");
    let version = std::env::var("NVSTOR_PEER_RELEASE")
        .ok()
        .filter(|v| valid_version(v))
        // Read when the script runs, not compiled in: Cargo (and the Bazel
        // build, from Cargo.toml) set the package's CARGO_PKG_VERSION for the
        // run, while the script binary's own may differ.
        .or_else(|| std::env::var("CARGO_PKG_VERSION").ok())
        .unwrap_or_else(|| "unknown".to_string());
    println!("cargo:rustc-env=NVSTOR_PEER_VERSION={version}");

    println!("cargo:rerun-if-env-changed=NVSTOR_PEER_COMMIT");
    println!("cargo:rerun-if-env-changed=GIT_DIR");
    let given = std::env::var("NVSTOR_PEER_COMMIT")
        .ok()
        .filter(|c| !c.is_empty());
    if let Some(c) = &given {
        if !valid_commit(c) {
            println!("cargo:warning=NVSTOR_PEER_COMMIT={c:?} is not a commit (7-40 hex digits, optional -dirty); ignored");
        }
    }
    let commit = given
        .filter(|c| valid_commit(c))
        .or_else(|| git(&["rev-parse", "HEAD"]).filter(|c| c.len() == 40 && valid_commit(c)))
        .unwrap_or_else(|| "unknown".to_string());
    println!("cargo:rustc-env=NVSTOR_PEER_COMMIT={commit}");
    // Rebuild when HEAD moves: HEAD itself (a branch switch or detached
    // checkout), the branch it points at, and packed refs. In a worktree
    // HEAD lives in the worktree's git dir and refs in the common one.
    let dir = |args: &[&str]| git(args).map(PathBuf::from);
    if let Some(git_dir) = dir(&["rev-parse", "--absolute-git-dir"]) {
        let common = dir(&["rev-parse", "--path-format=absolute", "--git-common-dir"])
            .unwrap_or_else(|| git_dir.clone());
        let watch = |p: &Path| {
            if p.exists() {
                println!("cargo:rerun-if-changed={}", p.display());
            }
        };
        watch(&git_dir.join("HEAD"));
        watch(&common.join("packed-refs"));
        if let Some(head_ref) = git(&["symbolic-ref", "-q", "HEAD"]) {
            watch(&git_dir.join(&head_ref));
            watch(&common.join(&head_ref));
        }
    }
}
