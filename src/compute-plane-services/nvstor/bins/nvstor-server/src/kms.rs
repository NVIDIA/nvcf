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

//! Production adapter between synchronous key operations and replicated Raft mutations.
use nvstor_kms::backend::LocalBackend;
use nvstor_metadata::MetadataStore;
use nvstor_raft::node::RaftNode;
use std::sync::Arc;

pub fn replicate(
    backend: LocalBackend,
    metadata: Arc<MetadataStore>,
    raft: Arc<RaftNode>,
) -> Result<LocalBackend, String> {
    let runtime = tokio::runtime::Handle::current();
    let coordinator = Arc::new(move |mutation: nvstor_kms::backend::KeyMutation| {
        let entry = nvstor_raft::types::LogEntryData::ConditionalInlineBatch {
            check_key: mutation.check_key,
            expected: mutation.expected,
            puts: mutation.puts.clone(),
            deletes: mutation.deletes.clone(),
        };
        match tokio::task::block_in_place(|| runtime.block_on(raft.propose(entry))) {
            nvstor_raft::types::ProposeResult::Committed { .. } => {}
            other => return Err(format!("replicate KMS mutation: {other:?}")),
        }
        for (key, value) in mutation.puts {
            if metadata.get_inline(&key).map_err(|e| e.to_string())? != Some(value) {
                return Err("concurrent KMS mutation; retry".into());
            }
        }
        for key in mutation.deletes {
            if metadata
                .get_inline(&key)
                .map_err(|e| e.to_string())?
                .is_some()
            {
                return Err("concurrent KMS mutation; retry".into());
            }
        }
        Ok(())
    });
    backend.with_coordinator(coordinator)
}

#[cfg(test)]
mod tests {
    use super::*;
    use nvstor_kms::backend::KmsBackend;
    use nvstor_kms::manager::KmsManager;
    use nvstor_kms::types::SseMode;
    use nvstor_raft::types::{PeerConfig, RaftConfig};
    use nvstor_raft::{log::RaftLog, network::RaftNetwork, state_machine::MetadataStateMachine};
    use std::time::Duration;

    #[tokio::test(flavor = "multi_thread", worker_threads = 4)]
    async fn replicated_keys_survive_leader_loss_and_rotate_on_new_leader() {
        let temp = tempfile::TempDir::new().unwrap();
        let mut listeners = Vec::new();
        let mut addresses = Vec::new();
        for _ in 0..3 {
            let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
            addresses.push(listener.local_addr().unwrap().to_string());
            listeners.push(listener);
        }
        let mut nodes = Vec::new();
        let mut backends = Vec::new();
        let mut tasks = Vec::new();
        for (index, listener) in listeners.into_iter().enumerate() {
            let id = index as u64 + 1;
            let metadata =
                Arc::new(MetadataStore::open(&temp.path().join(format!("meta-{id}"))).unwrap());
            let peers = addresses
                .iter()
                .enumerate()
                .filter(|(i, _)| *i != index)
                .map(|(i, addr)| PeerConfig {
                    node_id: i as u64 + 1,
                    addr: addr.clone(),
                })
                .collect();
            let config = RaftConfig {
                node_id: id,
                election_timeout_min_ms: 300,
                election_timeout_max_ms: 700,
                heartbeat_ms: 50,
                ..Default::default()
            };
            let node = Arc::new(RaftNode::new(
                config,
                RaftLog::open(&temp.path().join(format!("raft-{id}"))).unwrap(),
                Arc::new(RaftNetwork::new(id, peers)),
                Box::new(MetadataStateMachine::new(metadata.clone())),
            ));
            let backend = Arc::new(
                replicate(
                    LocalBackend::from_passphrase(metadata.clone(), "shared-test-root"),
                    metadata,
                    node.clone(),
                )
                .unwrap(),
            );
            backends.push(backend);
            let ticking = node.clone();
            let serving = node.clone();
            tasks.push((
                tokio::spawn(async move {
                    ticking.run().await;
                }),
                tokio::spawn(async move {
                    let _ = nvstor_raft::server::run(listener, serving).await;
                }),
            ));
            nodes.push(node);
        }
        let leader = tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                if let Some(index) = nodes.iter().position(|node| node.is_leader()) {
                    break index;
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .unwrap();
        let manager = KmsManager::new(backends[leader].clone());
        manager
            .create_key("network-key", "integration", None)
            .unwrap();
        let (ciphertext, envelope) = manager
            .encrypt(
                b"survives leader loss",
                &SseMode::SseKms {
                    key_id: "network-key".into(),
                },
            )
            .unwrap();
        tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                if backends.iter().all(|backend| {
                    backend
                        .get_key_material("network-key", 1)
                        .unwrap()
                        .is_some()
                }) {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .unwrap();
        for backend in &backends {
            assert_eq!(
                KmsManager::new(backend.clone())
                    .decrypt(&ciphertext, &envelope)
                    .unwrap(),
                b"survives leader loss"
            );
        }
        nodes[leader].stop();
        tasks[leader].0.abort();
        tasks[leader].1.abort();
        let next = tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                if let Some(index) = nodes
                    .iter()
                    .enumerate()
                    .position(|(i, node)| i != leader && node.is_leader())
                {
                    break index;
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .unwrap();
        KmsManager::new(backends[next].clone())
            .rotate_key("network-key")
            .unwrap();
        tokio::time::timeout(Duration::from_secs(10), async {
            loop {
                if backends
                    .iter()
                    .enumerate()
                    .filter(|(i, _)| *i != leader)
                    .all(|(_, backend)| {
                        backend
                            .get_key_material("network-key", 2)
                            .unwrap()
                            .is_some()
                    })
                {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(20)).await;
            }
        })
        .await
        .unwrap();
        for (i, backend) in backends.iter().enumerate() {
            if i != leader {
                assert_eq!(
                    KmsManager::new(backend.clone())
                        .decrypt(&ciphertext, &envelope)
                        .unwrap(),
                    b"survives leader loss"
                );
            }
        }
        for node in nodes {
            node.stop();
        }
        for (tick, server) in tasks {
            tick.abort();
            server.abort();
            let _ = tick.await;
            let _ = server.await;
        }
    }
}
