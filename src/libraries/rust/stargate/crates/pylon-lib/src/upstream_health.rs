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

use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

/// Upstream health paths probed in order. Dynamo-style engines serve `/health`;
/// OpenAI-style NIM images serve `/v1/health/ready` and no `/health`.
pub const DEFAULT_UPSTREAM_HEALTH_PATHS: [&str; 2] = ["/health", "/v1/health/ready"];

const UNRESOLVED: usize = usize::MAX;

#[derive(Clone, Debug)]
pub struct UpstreamHealthPaths {
    candidates: Arc<Vec<String>>,
    resolved: Arc<AtomicUsize>,
}

impl Default for UpstreamHealthPaths {
    fn default() -> Self {
        Self::new(DEFAULT_UPSTREAM_HEALTH_PATHS)
    }
}

impl UpstreamHealthPaths {
    pub fn new<I, P>(paths: I) -> Self
    where
        I: IntoIterator<Item = P>,
        P: AsRef<str>,
    {
        let mut candidates = Vec::new();
        for path in paths {
            let path = normalize_path(path.as_ref());
            if !path.is_empty() && !candidates.contains(&path) {
                candidates.push(path);
            }
        }
        if candidates.is_empty() {
            candidates = DEFAULT_UPSTREAM_HEALTH_PATHS
                .iter()
                .map(|path| (*path).to_string())
                .collect();
        }
        Self {
            candidates: Arc::new(candidates),
            resolved: Arc::new(AtomicUsize::new(UNRESOLVED)),
        }
    }

    pub fn resolved_path(&self) -> Option<String> {
        self.resolved_index()
            .map(|index| self.candidates[index].clone())
    }

    /// Path a health probe should use right now: the resolved one, or the first
    /// candidate before any probe has succeeded.
    pub fn probe_path(&self) -> &str {
        let index = self.resolved_index().unwrap_or(0);
        &self.candidates[index]
    }

    pub(crate) fn probe_order(&self) -> Vec<(usize, &str)> {
        let resolved = self.resolved_index();
        let mut order: Vec<(usize, &str)> = resolved
            .map(|index| vec![(index, self.candidates[index].as_str())])
            .unwrap_or_default();
        order.extend(
            self.candidates
                .iter()
                .enumerate()
                .filter(|(index, _)| Some(*index) != resolved)
                .map(|(index, path)| (index, path.as_str())),
        );
        order
    }

    pub(crate) fn mark_resolved(&self, index: usize) {
        self.resolved.store(index, Ordering::Relaxed);
    }

    fn resolved_index(&self) -> Option<usize> {
        match self.resolved.load(Ordering::Relaxed) {
            UNRESOLVED => None,
            index => Some(index),
        }
    }
}

fn normalize_path(path: &str) -> String {
    let path = path.trim();
    if path.is_empty() {
        return String::new();
    }
    if path.starts_with('/') {
        path.to_string()
    } else {
        format!("/{path}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn normalizes_and_deduplicates_candidates() {
        let paths = UpstreamHealthPaths::new([" health ", "/health", "v1/health/ready", ""]);

        assert_eq!(
            paths.probe_order(),
            vec![(0, "/health"), (1, "/v1/health/ready")]
        );
    }

    #[test]
    fn falls_back_to_the_defaults_when_no_path_is_configured() {
        let paths = UpstreamHealthPaths::new(Vec::<String>::new());

        assert_eq!(paths.probe_path(), "/health");
    }

    #[test]
    fn probes_the_resolved_path_first() {
        let paths = UpstreamHealthPaths::default();
        paths.mark_resolved(1);

        assert_eq!(
            paths.probe_order(),
            vec![(1, "/v1/health/ready"), (0, "/health")]
        );
        assert_eq!(paths.probe_path(), "/v1/health/ready");
    }
}
