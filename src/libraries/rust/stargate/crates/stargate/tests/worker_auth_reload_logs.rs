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

//! Log contract of the worker auth file reload.
//!
//! This binary holds one test and installs a process-wide subscriber. A
//! thread-scoped capture inside a parallel test binary can miss events,
//! because other test threads reach the same callsites without a subscriber
//! and cache a "never" interest for them.

use std::io;
use std::path::Path;
use std::sync::{Arc, Mutex};

use sha2::Digest;
use stargate::auth::{StaticClusterAuthenticator, WorkerAuthReloadOutcome};

#[derive(Clone, Default)]
struct LogCapture(Arc<Mutex<Vec<u8>>>);

impl io::Write for LogCapture {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        self.0
            .lock()
            .expect("log buffer lock should not be poisoned")
            .extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

impl LogCapture {
    fn text(&self) -> String {
        let bytes = self
            .0
            .lock()
            .expect("log buffer lock should not be poisoned")
            .clone();
        String::from_utf8(bytes).expect("logs should be UTF-8")
    }
}

fn sha256_hex(token: &str) -> String {
    sha2::Sha256::digest(token.as_bytes())
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

fn auth_file(clusters: &[(&str, &[&str])]) -> String {
    let mut contents = String::from("clusters:\n");
    for (cluster_id, tokens) in clusters {
        contents.push_str(&format!("  {cluster_id}:\n"));
        for token in *tokens {
            contents.push_str(&format!("    - sha256:{}\n", sha256_hex(token)));
        }
    }
    contents
}

/// Replaces the file the way a Secret volume does.
fn replace(path: &Path, contents: &str) {
    let staged = path.with_extension("staged");
    std::fs::write(&staged, contents).expect("staged file should be writable");
    std::fs::rename(&staged, path).expect("staged file should replace the live file");
}

#[tokio::test]
async fn reload_logs_each_distinct_failure_once_and_never_logs_hashes() {
    let logs = LogCapture::default();
    let writer = logs.clone();
    tracing::subscriber::set_global_default(
        tracing_subscriber::fmt()
            .with_ansi(false)
            .without_time()
            .with_target(false)
            .with_max_level(tracing::Level::INFO)
            .with_writer(move || writer.clone())
            .finish(),
    )
    .expect("this binary installs the only global subscriber");

    let dir = tempfile::tempdir().expect("temp dir should be creatable");
    let path = dir.path().join("credentials.yaml");
    let bad = "clusters:\n  spark-a:\n    - sha256:not-hex\n";
    replace(&path, &auth_file(&[("spark-a", &["token-a"])]));
    let authenticator = StaticClusterAuthenticator::load(&path)
        .await
        .expect("initial load should succeed");

    let rejected = Some(WorkerAuthReloadOutcome::Rejected);
    replace(&path, bad);
    assert_eq!(authenticator.reload().await, rejected);
    assert_eq!(authenticator.reload().await, None);
    std::fs::remove_file(&path).expect("live file should be removable");
    assert_eq!(authenticator.reload().await, rejected);
    assert_eq!(authenticator.reload().await, None);
    replace(&path, &auth_file(&[("spark-a", &["token-a"])]));
    assert_eq!(authenticator.reload().await, None);
    replace(&path, bad);
    assert_eq!(authenticator.reload().await, rejected);
    replace(
        &path,
        &auth_file(&[
            ("spark-a", &["token-a", "token-a-next"]),
            ("spark-b", &["token-b"]),
        ]),
    );
    assert_eq!(
        authenticator.reload().await,
        Some(WorkerAuthReloadOutcome::Success)
    );

    let text = logs.text();
    let lines = |needle: &str| {
        text.lines()
            .filter(|line| line.contains(needle))
            .collect::<Vec<_>>()
    };
    let loaded = lines("worker auth file loaded");
    assert_eq!(loaded.len(), 1, "{text}");
    assert!(loaded[0].contains("clusters=1 hashes=1"), "{text}");
    let rejections = lines("worker auth file reload rejected");
    assert_eq!(
        rejections.len(),
        3,
        "each distinct failure logs once:\n{text}"
    );
    assert!(
        rejections.iter().all(|line| line.starts_with("ERROR")),
        "{text}"
    );
    assert_eq!(lines("is valid again").len(), 1, "{text}");
    let changed = lines("worker auth credential set changed");
    assert_eq!(changed.len(), 1, "{text}");
    assert!(changed[0].starts_with(" INFO"), "{text}");
    assert!(changed[0].contains("clusters=2 hashes=3"), "{text}");
    for token in ["token-a", "token-a-next", "token-b"] {
        assert!(!text.contains(&sha256_hex(token)), "hash in logs:\n{text}");
    }
}
