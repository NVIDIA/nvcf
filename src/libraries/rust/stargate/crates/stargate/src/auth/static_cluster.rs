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

//! Static per-cluster worker credentials.
//!
//! The router trusts a YAML worker auth file that binds SHA-256 hashes of
//! worker bearer tokens to cluster ids:
//!
//! ```yaml
//! clusters:
//!   spark-berlin-01:
//!     - sha256:<64 hex>
//!     - sha256:<64 hex>    # next credential during rotation
//! ```
//!
//! The initial load must succeed or the router does not start. The reload
//! task then re-reads the file every [`WORKER_AUTH_RELOAD_INTERVAL`] and
//! activates a valid changed set; a read or parse failure keeps the last good
//! set.

use std::collections::HashMap;
use std::fmt;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use anyhow::{Context, Result, bail, ensure};
use parking_lot::{Mutex, RwLock};
use serde_yaml_ng::{Mapping, Value};
use sha2::{Digest, Sha256};
use subtle::{Choice, ConditionallySelectable, ConstantTimeEq};
use tokio::io::AsyncReadExt;
use tokio_util::sync::CancellationToken;
use tracing::{debug, error, info};

use super::{AuthResult, WorkerAuthError, WorkerAuthenticator};

/// Interval between reads of the worker auth file after the initial load.
///
/// Kubelet projects a Secret update into the pod on a sync period of about a
/// minute, so a 30 second poll activates a rotated credential shortly after
/// it lands. It matches the TLS material poll interval.
pub const WORKER_AUTH_RELOAD_INTERVAL: Duration = Duration::from_secs(30);

/// Upper bound on the worker auth file size.
const MAX_WORKER_AUTH_FILE_BYTES: u64 = 1024 * 1024;

const SHA256_DIGEST_BYTES: usize = 32;

const HASH_PREFIX: &str = "sha256:";

const FORMAT_HINT: &str =
    "expected YAML of the form `clusters: {<clusterId>: [sha256:<64 hex>, ...]}`";

type Sha256Digest = [u8; SHA256_DIGEST_BYTES];

/// `outcome` label values for `worker_auth_reloads_total`.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WorkerAuthReloadOutcome {
    /// A valid, changed credential set was activated.
    Success,
    /// The file could not be read or parsed, so the last good set stays
    /// active. A file that keeps failing the same way counts once.
    Rejected,
}

impl WorkerAuthReloadOutcome {
    pub const ALL: [Self; 2] = [Self::Success, Self::Rejected];

    pub const fn as_str(self) -> &'static str {
        match self {
            Self::Success => "success",
            Self::Rejected => "rejected",
        }
    }
}

/// Validated contents of a worker auth file.
#[derive(PartialEq, Eq)]
pub struct CredentialSet {
    /// Cluster ids in ascending order.
    cluster_ids: Vec<String>,
    /// Every hash of every cluster, sorted so that two files listing the same
    /// bindings in a different order compare equal.
    credentials: Vec<Credential>,
}

#[derive(PartialEq, Eq, PartialOrd, Ord)]
struct Credential {
    /// Index into `CredentialSet::cluster_ids`.
    cluster: usize,
    digest: Sha256Digest,
}

/// Prints counts only; the hashes never reach logs.
impl fmt::Debug for CredentialSet {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("CredentialSet")
            .field("clusters", &self.cluster_count())
            .field("hashes", &self.hash_count())
            .finish()
    }
}

impl CredentialSet {
    /// Parses and validates a worker auth file.
    ///
    /// Rejects a file without a non-empty `clusters` map, an empty cluster id,
    /// a cluster without hashes, an entry other than `sha256:` followed by
    /// exactly 64 hex characters, and a hash listed more than once. Hex case
    /// is ignored. Errors name clusters and entry positions but never entry
    /// text, so a token pasted in place of its hash does not reach the logs.
    pub fn parse(bytes: &[u8]) -> Result<Self> {
        let document: Value = serde_yaml_ng::from_slice(bytes).context(FORMAT_HINT)?;
        let Value::Mapping(document) = document else {
            bail!("the file must be a YAML map; {FORMAT_HINT}");
        };
        let mut clusters = None;
        for (key, value) in document {
            match key.as_str() {
                Some("clusters") => clusters = Some(value),
                Some(other) => bail!("unexpected top-level key {other:?}; {FORMAT_HINT}"),
                None => bail!("top-level keys must be strings; {FORMAT_HINT}"),
            }
        }
        let clusters = match clusters {
            Some(Value::Mapping(clusters)) => clusters,
            Some(Value::Null) => Mapping::new(),
            Some(_) => bail!("clusters must map cluster ids to lists of hashes; {FORMAT_HINT}"),
            None => bail!("missing clusters; {FORMAT_HINT}"),
        };
        ensure!(
            !clusters.is_empty(),
            "clusters must list at least one cluster"
        );

        let mut named = Vec::with_capacity(clusters.len());
        for (key, hashes) in clusters {
            let Value::String(cluster_id) = key else {
                bail!(
                    "cluster ids must be strings; quote an id that YAML reads as a number or boolean"
                );
            };
            ensure!(
                !cluster_id.trim().is_empty(),
                "cluster id must not be empty"
            );
            let hashes = match hashes {
                Value::Sequence(hashes) => hashes,
                Value::Null => Vec::new(),
                _ => bail!("cluster {cluster_id:?} must map to a list of sha256:<64 hex> entries"),
            };
            ensure!(
                !hashes.is_empty(),
                "cluster {cluster_id:?} lists no credential hashes"
            );
            let digests = hashes
                .iter()
                .enumerate()
                .map(|(position, entry)| {
                    parse_hash_entry(entry).with_context(|| {
                        format!("cluster {cluster_id:?} entry {} is invalid", position + 1)
                    })
                })
                .collect::<Result<Vec<_>>>()?;
            named.push((cluster_id, digests));
        }
        named.sort_by(|left, right| left.0.cmp(&right.0));

        // One credential must identify exactly one cluster.
        let mut owners: HashMap<Sha256Digest, usize> = HashMap::new();
        let mut cluster_ids = Vec::with_capacity(named.len());
        let mut credentials = Vec::new();
        for (cluster, (cluster_id, digests)) in named.into_iter().enumerate() {
            for digest in digests {
                if let Some(owner) = owners.insert(digest, cluster) {
                    if owner == cluster {
                        bail!("cluster {cluster_id:?} lists one credential hash twice");
                    }
                    bail!(
                        "clusters {:?} and {cluster_id:?} list the same credential hash; a credential must identify one cluster",
                        cluster_ids[owner]
                    );
                }
                credentials.push(Credential { cluster, digest });
            }
            cluster_ids.push(cluster_id);
        }
        credentials.sort_unstable();
        Ok(Self {
            cluster_ids,
            credentials,
        })
    }

    pub fn cluster_count(&self) -> usize {
        self.cluster_ids.len()
    }

    pub fn hash_count(&self) -> usize {
        self.credentials.len()
    }

    /// Returns the cluster bound to the hash of `token`, if any.
    pub fn cluster_for_token(&self, token: &str) -> Option<&str> {
        let presented: Sha256Digest = Sha256::digest(token.as_bytes()).into();
        self.find_cluster(&presented, |presented, stored| presented.ct_eq(stored))
            .map(|cluster| self.cluster_ids[cluster].as_str())
    }

    /// Compares `presented` with every hash of every cluster and folds the
    /// results with constant-time selection. Neither the number of
    /// comparisons nor a branch depends on which entry matched or on how many
    /// bytes of a near miss agreed.
    fn find_cluster(
        &self,
        presented: &Sha256Digest,
        mut digests_equal: impl FnMut(&[u8], &[u8]) -> Choice,
    ) -> Option<usize> {
        // Zero means no match; otherwise the matching cluster index plus one.
        let mut matched = 0u64;
        for credential in &self.credentials {
            let candidate = credential.cluster as u64 + 1;
            matched.conditional_assign(
                &candidate,
                digests_equal(&presented[..], &credential.digest[..]),
            );
        }
        usize::try_from(matched).ok()?.checked_sub(1)
    }
}

fn parse_hash_entry(entry: &Value) -> Result<Sha256Digest> {
    let Value::String(entry) = entry else {
        bail!("expected a string of the form {HASH_PREFIX}<64 hex>");
    };
    let hex = entry
        .strip_prefix(HASH_PREFIX)
        .with_context(|| format!("expected the {HASH_PREFIX} prefix"))?
        .as_bytes();
    ensure!(
        hex.len() == SHA256_DIGEST_BYTES * 2,
        "expected {HASH_PREFIX} followed by exactly {} hex characters, got {}",
        SHA256_DIGEST_BYTES * 2,
        hex.len()
    );
    let mut digest = [0u8; SHA256_DIGEST_BYTES];
    for (byte, pair) in digest.iter_mut().zip(hex.chunks_exact(2)) {
        *byte = (hex_nibble(pair[0])? << 4) | hex_nibble(pair[1])?;
    }
    Ok(digest)
}

fn hex_nibble(character: u8) -> Result<u8> {
    match character {
        b'0'..=b'9' => Ok(character - b'0'),
        b'a'..=b'f' => Ok(character - b'a' + 10),
        b'A'..=b'F' => Ok(character - b'A' + 10),
        _ => bail!("expected hexadecimal characters after {HASH_PREFIX}"),
    }
}

async fn read_credential_set(path: &Path) -> Result<CredentialSet> {
    let file = tokio::fs::File::open(path)
        .await
        .context("cannot open the file")?;
    let mut bytes = Vec::new();
    file.take(MAX_WORKER_AUTH_FILE_BYTES + 1)
        .read_to_end(&mut bytes)
        .await
        .context("cannot read the file")?;
    ensure!(
        bytes.len() as u64 <= MAX_WORKER_AUTH_FILE_BYTES,
        "the file exceeds {MAX_WORKER_AUTH_FILE_BYTES} bytes"
    );
    CredentialSet::parse(&bytes)
}

/// Authenticates workers against the per-cluster credential hashes of a
/// worker auth file.
///
/// A token whose SHA-256 matches a listed hash authenticates as that hash's
/// cluster, with no routing key. [`StaticClusterAuthenticator::load`] must
/// succeed for the router to start. Afterwards the reload task re-reads the
/// file on an interval and swaps a valid changed set in atomically, so
/// credential rotation needs no restart; a missing or invalid file keeps the
/// last good set. Kubernetes Secret volumes publish an update by swapping a
/// symlink to a new directory, so a read sees either the whole old file or
/// the whole new one.
pub struct StaticClusterAuthenticator {
    path: PathBuf,
    active: RwLock<Arc<CredentialSet>>,
    reload_interval: Duration,
    /// Last reload failure. A file that stays broken logs and counts once.
    last_reload_error: Mutex<Option<String>>,
}

impl StaticClusterAuthenticator {
    /// Loads the initial credential set. Fails, naming the path, when the
    /// file is missing, unreadable, oversized, or invalid.
    pub async fn load(path: impl Into<PathBuf>) -> Result<Self> {
        let path = path.into();
        let credentials = read_credential_set(&path)
            .await
            .with_context(|| format!("failed to load worker auth file {}", path.display()))?;
        info!(
            path = %path.display(),
            clusters = credentials.cluster_count(),
            hashes = credentials.hash_count(),
            "worker auth file loaded"
        );
        Ok(Self {
            path,
            active: RwLock::new(Arc::new(credentials)),
            reload_interval: WORKER_AUTH_RELOAD_INTERVAL,
            last_reload_error: Mutex::new(None),
        })
    }

    /// Replaces [`WORKER_AUTH_RELOAD_INTERVAL`], for tests.
    pub fn with_reload_interval(mut self, reload_interval: Duration) -> Self {
        self.reload_interval = reload_interval;
        self
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    /// Returns the active credential set.
    pub fn credentials(&self) -> Arc<CredentialSet> {
        Arc::clone(&self.active.read())
    }

    /// Re-reads the file once and activates it when it is valid and changed.
    ///
    /// Returns the outcome to count, or `None` when there is nothing new: the
    /// file is valid but unchanged, or fails exactly as the previous attempt
    /// did.
    pub async fn reload(&self) -> Option<WorkerAuthReloadOutcome> {
        match read_credential_set(&self.path).await {
            Ok(candidate) => {
                let recovered = self.last_reload_error.lock().take().is_some();
                if **self.active.read() == candidate {
                    if recovered {
                        info!(
                            path = %self.path.display(),
                            "worker auth file is valid again; the active credential set is unchanged"
                        );
                    }
                    return None;
                }
                let (clusters, hashes) = (candidate.cluster_count(), candidate.hash_count());
                *self.active.write() = Arc::new(candidate);
                info!(
                    path = %self.path.display(),
                    clusters,
                    hashes,
                    "worker auth credential set changed"
                );
                Some(WorkerAuthReloadOutcome::Success)
            }
            Err(error) => {
                let message = format!("{error:#}");
                let mut last_reload_error = self.last_reload_error.lock();
                if last_reload_error.as_deref() == Some(message.as_str()) {
                    return None;
                }
                error!(
                    path = %self.path.display(),
                    error = %message,
                    "worker auth file reload rejected; keeping the last good credential set"
                );
                *last_reload_error = Some(message);
                Some(WorkerAuthReloadOutcome::Rejected)
            }
        }
    }
}

#[async_trait::async_trait]
impl WorkerAuthenticator for StaticClusterAuthenticator {
    async fn authenticate(&self, token: Option<&str>) -> Result<AuthResult> {
        let token = token
            .filter(|token| !token.is_empty())
            .ok_or(WorkerAuthError::MissingToken)?;
        let credentials = self.credentials();
        let cluster_id = credentials
            .cluster_for_token(token)
            .ok_or(WorkerAuthError::UnknownCredential)?;
        debug!(
            cluster_id,
            "worker authenticated with static cluster credential"
        );
        Ok(AuthResult {
            routing_key: None,
            cluster_id: Some(cluster_id.to_owned()),
        })
    }

    fn reload_task(self: Arc<Self>) -> Option<WorkerAuthReloadTask> {
        Some(WorkerAuthReloadTask {
            poll_interval: self.reload_interval,
            authenticator: self,
        })
    }
}

/// Keeps a [`StaticClusterAuthenticator`] in step with its file.
///
/// The runtime runs it in its critical task group, the way it runs the TLS
/// material reload.
pub struct WorkerAuthReloadTask {
    authenticator: Arc<StaticClusterAuthenticator>,
    poll_interval: Duration,
}

impl WorkerAuthReloadTask {
    /// Runs until `shutdown` is cancelled, reporting each counted outcome to
    /// `observe`.
    pub async fn run<Observe>(self, shutdown: CancellationToken, mut observe: Observe) -> Result<()>
    where
        Observe: FnMut(WorkerAuthReloadOutcome),
    {
        ensure!(
            !self.poll_interval.is_zero(),
            "worker auth reload interval must be positive"
        );
        let mut poll = tokio::time::interval(self.poll_interval);
        poll.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        // The first tick completes immediately, and the initial set is
        // already active, so consume it rather than reloading on entry.
        poll.tick().await;
        loop {
            tokio::select! {
                () = shutdown.cancelled() => return Ok(()),
                _ = poll.tick() => {
                    if let Some(outcome) = self.authenticator.reload().await {
                        observe(outcome);
                    }
                }
            }
        }
    }
}

#[cfg(test)]
mod tests;
