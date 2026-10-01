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

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use parking_lot::Mutex;
use sha2::{Digest, Sha256};
use subtle::ConstantTimeEq;
use tokio_util::sync::CancellationToken;

use super::*;
use crate::auth::RegistrationAuthFailure;

const TOKEN_A: &str = "cluster-a-token";
const TOKEN_A_NEXT: &str = "cluster-a-next-token";
const TOKEN_B: &str = "cluster-b-token";

fn sha256_hex(token: &str) -> String {
    Sha256::digest(token.as_bytes())
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

fn alternate_case(hex: &str) -> String {
    hex.chars()
        .enumerate()
        .map(|(index, character)| {
            if index % 2 == 0 {
                character.to_ascii_uppercase()
            } else {
                character
            }
        })
        .collect()
}

/// Renders a worker auth file binding each cluster to the hashes of its tokens.
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

fn one_hash(cluster_id: &str, entry: &str) -> String {
    format!("clusters:\n  {cluster_id}:\n    - {entry}\n")
}

fn parse(contents: &str) -> Result<CredentialSet> {
    CredentialSet::parse(contents.as_bytes())
}

#[test]
fn parser_accepts_valid_files() {
    let a = sha256_hex(TOKEN_A);
    let a_next = sha256_hex(TOKEN_A_NEXT);
    for (case, contents, clusters, hashes) in [
        (
            "one cluster with one hash",
            auth_file(&[("spark-a", &[TOKEN_A])]),
            1,
            1,
        ),
        (
            "two hashes per cluster during rotation",
            auth_file(&[
                ("spark-a", &[TOKEN_A, TOKEN_A_NEXT]),
                ("spark-b", &[TOKEN_B]),
            ]),
            2,
            3,
        ),
        (
            "mixed-case hex",
            one_hash("spark-a", &format!("sha256:{}", alternate_case(&a))),
            1,
            1,
        ),
        (
            "flow style with quoted entries",
            format!(r#"{{"clusters": {{"spark-a": ["sha256:{a}", 'sha256:{a_next}']}}}}"#),
            1,
            2,
        ),
        (
            "comments",
            format!(
                "# rotation in progress\nclusters:\n  spark-a:\n    - sha256:{a}  # current\n    - sha256:{a_next}  # next\n"
            ),
            1,
            2,
        ),
        (
            "quoted numeric cluster id",
            format!("clusters:\n  \"01\":\n    - sha256:{a}\n"),
            1,
            1,
        ),
    ] {
        let set = parse(&contents).unwrap_or_else(|error| panic!("{case}: {error:#}"));
        assert_eq!(
            (set.cluster_count(), set.hash_count()),
            (clusters, hashes),
            "{case}"
        );
    }
}

#[test]
fn parser_ignores_hex_case_and_entry_order() {
    let a = sha256_hex(TOKEN_A);
    let a_next = sha256_hex(TOKEN_A_NEXT);
    let lower = parse(&one_hash("spark-a", &format!("sha256:{a}"))).expect("lowercase parses");
    let upper = parse(&one_hash(
        "spark-a",
        &format!("sha256:{}", a.to_uppercase()),
    ))
    .expect("uppercase parses");
    let mixed = parse(&one_hash(
        "spark-a",
        &format!("sha256:{}", alternate_case(&a)),
    ))
    .expect("mixed case parses");
    assert_eq!(lower, upper);
    assert_eq!(lower, mixed);
    assert_eq!(mixed.cluster_for_token(TOKEN_A), Some("spark-a"));

    let forward = parse(&auth_file(&[
        ("spark-a", &[TOKEN_A, TOKEN_A_NEXT]),
        ("spark-b", &[TOKEN_B]),
    ]))
    .expect("forward order parses");
    let reversed = parse(&format!(
        "clusters:\n  spark-b:\n    - sha256:{}\n  spark-a:\n    - sha256:{a_next}\n    - sha256:{a}\n",
        sha256_hex(TOKEN_B)
    ))
    .expect("reversed order parses");
    assert_eq!(forward, reversed);
}

#[test]
fn parser_rejects_invalid_files() {
    let a = sha256_hex(TOKEN_A);
    let b = sha256_hex(TOKEN_B);
    let valid = auth_file(&[("spark-a", &[TOKEN_A])]);
    for (case, contents, expected) in [
        ("empty file", String::new(), "the file must be a YAML map"),
        (
            "YAML syntax error",
            "clusters: [unclosed".to_owned(),
            "expected YAML of the form",
        ),
        (
            "two documents",
            format!("{valid}---\n{valid}"),
            "more than one document",
        ),
        (
            "top-level list",
            format!("- sha256:{a}\n"),
            "the file must be a YAML map",
        ),
        ("missing clusters", "{}".to_owned(), "missing clusters"),
        (
            "misspelled clusters",
            format!("cluster:\n  spark-a:\n    - sha256:{a}\n"),
            "unexpected top-level key \"cluster\"",
        ),
        (
            "unknown top-level key",
            format!("{valid}version: 1\n"),
            "unexpected top-level key \"version\"",
        ),
        (
            "clusters is a list",
            "clusters:\n  - spark-a\n".to_owned(),
            "clusters must map cluster ids to lists of hashes",
        ),
        (
            "empty clusters map",
            "clusters: {}\n".to_owned(),
            "clusters must list at least one cluster",
        ),
        (
            "null clusters",
            "clusters:\n".to_owned(),
            "clusters must list at least one cluster",
        ),
        (
            "empty cluster id",
            format!("clusters:\n  \"\":\n    - sha256:{a}\n"),
            "cluster id must not be empty",
        ),
        (
            "blank cluster id",
            format!("clusters:\n  \"  \":\n    - sha256:{a}\n"),
            "cluster id must not be empty",
        ),
        (
            "numeric cluster id",
            format!("clusters:\n  1:\n    - sha256:{a}\n"),
            "cluster ids must be strings",
        ),
        (
            "duplicate cluster id",
            format!("clusters:\n  spark-a:\n    - sha256:{a}\n  spark-a:\n    - sha256:{b}\n"),
            "duplicate entry",
        ),
        (
            "empty hash list",
            "clusters:\n  spark-a: []\n".to_owned(),
            "cluster \"spark-a\" lists no credential hashes",
        ),
        (
            "null hash list",
            "clusters:\n  spark-a:\n".to_owned(),
            "cluster \"spark-a\" lists no credential hashes",
        ),
        (
            "previous JSON format",
            format!(r#"{{"clusters": {{"spark-a": "{a}"}}}}"#),
            "cluster \"spark-a\" must map to a list of sha256:<64 hex> entries",
        ),
        (
            "bare hex without prefix",
            one_hash("spark-a", &a),
            "cluster \"spark-a\" entry 1 is invalid: expected the sha256: prefix",
        ),
        (
            "uppercase prefix",
            one_hash("spark-a", &format!("SHA256:{a}")),
            "expected the sha256: prefix",
        ),
        (
            "other algorithm",
            one_hash("spark-a", &format!("sha512:{a}")),
            "expected the sha256: prefix",
        ),
        (
            "63 hex characters",
            one_hash("spark-a", &format!("sha256:{}", &a[..63])),
            "exactly 64 hex characters, got 63",
        ),
        (
            "65 hex characters",
            one_hash("spark-a", &format!("sha256:{a}0")),
            "exactly 64 hex characters, got 65",
        ),
        (
            "non-hex characters",
            one_hash("spark-a", &format!("sha256:{}", "z".repeat(64))),
            "expected hexadecimal characters",
        ),
        (
            "number entry",
            one_hash("spark-a", "12345"),
            "expected a string of the form sha256:<64 hex>",
        ),
        (
            "second entry invalid",
            format!(
                "clusters:\n  spark-a:\n    - sha256:{a}\n    - sha256:{}\n",
                &b[..10]
            ),
            "cluster \"spark-a\" entry 2 is invalid",
        ),
        (
            "hash under two clusters",
            format!("clusters:\n  spark-a:\n    - sha256:{a}\n  spark-b:\n    - sha256:{a}\n"),
            "clusters \"spark-a\" and \"spark-b\" list the same credential hash",
        ),
        (
            "hash under two clusters in different case",
            format!(
                "clusters:\n  spark-a:\n    - sha256:{a}\n  spark-b:\n    - sha256:{}\n",
                a.to_uppercase()
            ),
            "clusters \"spark-a\" and \"spark-b\" list the same credential hash",
        ),
        (
            "hash twice in one cluster",
            format!("clusters:\n  spark-a:\n    - sha256:{a}\n    - sha256:{a}\n"),
            "cluster \"spark-a\" lists one credential hash twice",
        ),
    ] {
        let error = parse(&contents).expect_err(case);
        let message = format!("{error:#}");
        assert!(message.contains(expected), "{case}: {message}");
    }
}

#[test]
fn parser_errors_never_echo_entries() {
    let pasted_token = "raw-cluster-token-pasted-by-mistake";
    for contents in [
        one_hash("spark-a", pasted_token),
        one_hash("spark-a", &format!("sha256:{pasted_token}")),
        format!("clusters:\n  spark-a: {pasted_token}\n"),
    ] {
        let message = format!(
            "{:#}",
            parse(&contents).expect_err("entry must be rejected")
        );
        assert!(!message.contains(pasted_token), "{message}");
    }
}

#[test]
fn credential_set_debug_prints_counts_only() {
    let set = parse(&auth_file(&[
        ("spark-a", &[TOKEN_A, TOKEN_A_NEXT]),
        ("spark-b", &[TOKEN_B]),
    ]))
    .expect("valid file");
    let debug = format!("{set:?}");
    assert_eq!(debug, "CredentialSet { clusters: 2, hashes: 3 }");
}

#[test]
fn matching_compares_every_hash_of_every_cluster() {
    let set = parse(&auth_file(&[
        ("spark-a", &[TOKEN_A, TOKEN_A_NEXT]),
        ("spark-b", &[TOKEN_B]),
    ]))
    .expect("valid file");
    for (token, expected) in [
        (TOKEN_A, Some("spark-a")),
        (TOKEN_A_NEXT, Some("spark-a")),
        (TOKEN_B, Some("spark-b")),
        ("unknown-token", None),
    ] {
        let presented: Sha256Digest = Sha256::digest(token.as_bytes()).into();
        let mut comparisons = 0;
        let matched = set.find_cluster(&presented, |presented, stored| {
            comparisons += 1;
            presented.ct_eq(stored)
        });
        assert_eq!(
            matched.map(|cluster| set.cluster_ids[cluster].as_str()),
            expected,
            "{token}"
        );
        assert_eq!(
            comparisons,
            set.hash_count(),
            "{token}: every hash of every cluster must be compared"
        );
        assert_eq!(set.cluster_for_token(token), expected, "{token}");
    }
}

/// A worker auth file in a temporary directory, replaced the way a Secret
/// volume replaces it: write elsewhere, then rename over the live path.
struct AuthFile {
    dir: tempfile::TempDir,
    path: PathBuf,
}

impl AuthFile {
    fn new(contents: &str) -> Self {
        let dir = tempfile::tempdir().expect("temp dir should be creatable");
        let path = dir.path().join("credentials.yaml");
        let file = Self { dir, path };
        file.replace(contents);
        file
    }

    fn replace(&self, contents: &str) {
        let staged = self.dir.path().join("staged.yaml");
        std::fs::write(&staged, contents).expect("staged file should be writable");
        std::fs::rename(&staged, &self.path).expect("staged file should replace the live file");
    }

    fn remove(&self) {
        std::fs::remove_file(&self.path).expect("live file should be removable");
    }

    async fn load(&self) -> StaticClusterAuthenticator {
        StaticClusterAuthenticator::load(&self.path)
            .await
            .expect("initial load should succeed")
    }
}

async fn cluster_for(authenticator: &StaticClusterAuthenticator, token: &str) -> Option<String> {
    let result = authenticator.authenticate(Some(token)).await.ok()?;
    assert_eq!(result.routing_key, None);
    result.cluster_id
}

#[tokio::test]
async fn authenticate_returns_the_cluster_of_every_listed_hash() {
    let file = AuthFile::new(&auth_file(&[
        ("spark-a", &[TOKEN_A, TOKEN_A_NEXT]),
        ("spark-b", &[TOKEN_B]),
    ]));
    let authenticator = file.load().await;
    for (token, cluster_id) in [
        (TOKEN_A, "spark-a"),
        (TOKEN_A_NEXT, "spark-a"),
        (TOKEN_B, "spark-b"),
    ] {
        assert_eq!(
            cluster_for(&authenticator, token).await.as_deref(),
            Some(cluster_id),
            "{token}"
        );
    }
}

#[tokio::test]
async fn authenticate_classifies_missing_and_unknown_tokens() {
    let file = AuthFile::new(&auth_file(&[("spark-a", &[TOKEN_A])]));
    let authenticator = file.load().await;
    for (token, expected) in [
        (None, RegistrationAuthFailure::MissingToken),
        (Some(""), RegistrationAuthFailure::MissingToken),
        (
            Some("wrong-token"),
            RegistrationAuthFailure::UnknownCredential,
        ),
    ] {
        let error = authenticator
            .authenticate(token)
            .await
            .err()
            .expect("authentication should fail");
        assert_eq!(
            RegistrationAuthFailure::classify(&error, token),
            Some(expected),
            "{error:#}"
        );
        if let Some(token) = token.filter(|token| !token.is_empty()) {
            assert!(!format!("{error:#}").contains(token), "{error:#}");
        }
    }
}

#[tokio::test]
async fn load_fails_naming_the_path_for_missing_or_invalid_files() {
    let file = AuthFile::new("clusters: {}\n");
    let oversized = format!(
        "{}{}",
        auth_file(&[("spark-a", &[TOKEN_A])]),
        "#".repeat(MAX_WORKER_AUTH_FILE_BYTES as usize)
    );
    for (case, contents, expected) in [
        ("missing file", None, "cannot open the file"),
        (
            "invalid file",
            Some("clusters: {}\n".to_owned()),
            "clusters must list at least one cluster",
        ),
        (
            "oversized file",
            Some(oversized),
            "the file exceeds 1048576 bytes",
        ),
    ] {
        match contents {
            Some(contents) => file.replace(&contents),
            None => file.remove(),
        }
        let error = StaticClusterAuthenticator::load(&file.path)
            .await
            .err()
            .expect(case);
        let message = format!("{error:#}");
        assert!(
            message.contains(&format!(
                "failed to load worker auth file {}",
                file.path.display()
            )),
            "{case}: {message}"
        );
        assert!(message.contains(expected), "{case}: {message}");
    }
}

#[tokio::test]
async fn reload_activates_a_changed_set_without_restart() {
    let file = AuthFile::new(&auth_file(&[("spark-a", &[TOKEN_A])]));
    let authenticator = file.load().await;
    assert_eq!(cluster_for(&authenticator, TOKEN_A_NEXT).await, None);

    // Rotation step 1: list the next credential next to the current one.
    file.replace(&auth_file(&[("spark-a", &[TOKEN_A, TOKEN_A_NEXT])]));
    assert_eq!(
        authenticator.reload().await,
        Some(WorkerAuthReloadOutcome::Success)
    );
    for token in [TOKEN_A, TOKEN_A_NEXT] {
        assert_eq!(
            cluster_for(&authenticator, token).await.as_deref(),
            Some("spark-a")
        );
    }

    // Rotation step 3: remove the old credential.
    file.replace(&auth_file(&[("spark-a", &[TOKEN_A_NEXT])]));
    assert_eq!(
        authenticator.reload().await,
        Some(WorkerAuthReloadOutcome::Success)
    );
    assert_eq!(cluster_for(&authenticator, TOKEN_A).await, None);
    assert_eq!(
        cluster_for(&authenticator, TOKEN_A_NEXT).await.as_deref(),
        Some("spark-a")
    );

    // An unchanged file counts nothing.
    assert_eq!(authenticator.reload().await, None);
}

#[tokio::test]
async fn reload_keeps_the_last_good_set_and_counts_each_failure_once() {
    // The log text of this sequence is asserted in
    // tests/worker_auth_reload_logs.rs, which owns its process-wide subscriber.
    let file = AuthFile::new(&auth_file(&[("spark-a", &[TOKEN_A])]));
    let authenticator = file.load().await;

    let rejected = Some(WorkerAuthReloadOutcome::Rejected);
    file.replace(&one_hash("spark-a", "sha256:not-hex"));
    assert_eq!(authenticator.reload().await, rejected);
    assert_eq!(authenticator.reload().await, None, "a repeat stays quiet");
    assert_eq!(
        cluster_for(&authenticator, TOKEN_A).await.as_deref(),
        Some("spark-a"),
        "the last good set stays active"
    );

    file.remove();
    assert_eq!(authenticator.reload().await, rejected, "a new failure logs");
    assert_eq!(authenticator.reload().await, None);
    assert_eq!(
        cluster_for(&authenticator, TOKEN_A).await.as_deref(),
        Some("spark-a")
    );

    file.replace(&auth_file(&[("spark-a", &[TOKEN_A])]));
    assert_eq!(
        authenticator.reload().await,
        None,
        "recovery to the same set"
    );
    file.replace(&one_hash("spark-a", "sha256:not-hex"));
    assert_eq!(
        authenticator.reload().await,
        rejected,
        "a failure after recovery logs again"
    );

    file.replace(&auth_file(&[
        ("spark-a", &[TOKEN_A, TOKEN_A_NEXT]),
        ("spark-b", &[TOKEN_B]),
    ]));
    assert_eq!(
        authenticator.reload().await,
        Some(WorkerAuthReloadOutcome::Success)
    );
}

#[tokio::test]
async fn reload_task_polls_until_shutdown() {
    let file = AuthFile::new(&auth_file(&[("spark-a", &[TOKEN_A])]));
    let authenticator = Arc::new(
        file.load()
            .await
            .with_reload_interval(Duration::from_millis(10)),
    );
    let as_trait_object: Arc<dyn WorkerAuthenticator> = authenticator.clone();
    let task = as_trait_object
        .reload_task()
        .expect("the static authenticator reloads its file");
    let outcomes = Arc::new(Mutex::new(Vec::new()));
    let shutdown = CancellationToken::new();
    let handle = tokio::spawn(task.run(shutdown.clone(), {
        let outcomes = outcomes.clone();
        move |outcome| outcomes.lock().push(outcome)
    }));

    file.replace(&auth_file(&[("spark-a", &[TOKEN_B])]));
    wait_until(|| outcomes.lock().contains(&WorkerAuthReloadOutcome::Success)).await;
    assert_eq!(
        cluster_for(&authenticator, TOKEN_B).await.as_deref(),
        Some("spark-a")
    );
    assert_eq!(cluster_for(&authenticator, TOKEN_A).await, None);

    file.replace("not: [valid");
    wait_until(|| outcomes.lock().contains(&WorkerAuthReloadOutcome::Rejected)).await;
    assert_eq!(
        cluster_for(&authenticator, TOKEN_B).await.as_deref(),
        Some("spark-a")
    );

    shutdown.cancel();
    handle
        .await
        .expect("reload task should not panic")
        .expect("reload task should stop cleanly");
    assert_eq!(
        *outcomes.lock(),
        [
            WorkerAuthReloadOutcome::Success,
            WorkerAuthReloadOutcome::Rejected
        ]
    );
}

#[tokio::test]
async fn reload_task_rejects_a_zero_interval() {
    let file = AuthFile::new(&auth_file(&[("spark-a", &[TOKEN_A])]));
    let authenticator = Arc::new(file.load().await.with_reload_interval(Duration::ZERO));
    let error = authenticator
        .reload_task()
        .expect("the static authenticator reloads its file")
        .run(CancellationToken::new(), |_| {})
        .await
        .expect_err("a zero interval must be rejected");
    assert!(
        format!("{error:#}").contains("must be positive"),
        "{error:#}"
    );
}

async fn wait_until(mut condition: impl FnMut() -> bool) {
    tokio::time::timeout(Duration::from_secs(5), async {
        while !condition() {
            tokio::time::sleep(Duration::from_millis(5)).await;
        }
    })
    .await
    .expect("condition should hold within five seconds");
}
