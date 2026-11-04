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

//! Distributed chunk manager — async, parallel replication across nodes.
//!
//! Write path: place_chunk → local write + parallel async replication to remote nodes.
//! Read path: local first, then async fetch from remote.
//! ALL network I/O is async (tokio) — NO blocking std::net.

use std::collections::HashMap;
use std::sync::Arc;

use parking_lot::RwLock;
use tracing::{debug, info, warn};

use nvstor_core::error::{NvstorError, Result};
use nvstor_core::types::ChunkId;
use nvstor_storage::erasure::ErasureConfig;
use nvstor_storage::manager::ChunkManager;

#[cfg(feature = "rdma")]
use nvstor_rdma::rpc::RpcClient;
#[cfg(feature = "rdma")]
use nvstor_rdma::transport::RdmaTransport;

use crate::node::{ClusterMap, NodeId, NodeStatus};
use crate::placement;
use crate::policy::PlacementPolicy;

/// How long a node whose RDMA connect failed is left on the TCP path
/// before the next connect attempt.
#[cfg(feature = "rdma")]
const RDMA_RECONNECT_BACKOFF: std::time::Duration = std::time::Duration::from_secs(10);

/// Distributed chunk manager — RDMA-first, TCP RPC fallback.
///
/// Write path: local write, then each replica over RDMA with the
/// S3-over-RDMA REPLICATE verb (the replica RDMA-READs the bytes from this
/// node's pool), falling back to TCP RPC (PERSIST_INLINE).
/// Read path: local first, then TCP RPC FETCH_INLINE from remote nodes.
pub struct DistributedManager {
    /// Local node ID.
    local_node_id: NodeId,
    /// Local chunk manager for this node's storage.
    local: Arc<ChunkManager>,
    /// Cluster membership map.
    cluster: RwLock<ClusterMap>,
    /// Credential used to authenticate outgoing node-to-node RPC.
    /// Set by the server from config before any peer RPC is issued.
    #[cfg(feature = "rdma")]
    cluster_cred: RwLock<Option<nvstor_rdma::auth::Credentials>>,
    /// Default placement policy.
    default_policy: PlacementPolicy,
    #[cfg(any(feature = "rdma", test))]
    repair_cursor: parking_lot::Mutex<Option<String>>,
    /// HTTP endpoints for remote nodes (node_id → "host:port").
    remote_endpoints: RwLock<HashMap<NodeId, String>>,
    /// RDMA transport (None if RDMA unavailable).
    #[cfg(feature = "rdma")]
    rdma: Option<Arc<RdmaTransport>>,
    /// RDMA peer mapping: node_id → rdma peer_id.
    #[cfg(feature = "rdma")]
    rdma_peers: RwLock<HashMap<NodeId, u64>>,
    /// Each remote node's RDMA bootstrap address, for reconnecting.
    #[cfg(feature = "rdma")]
    rdma_addrs: RwLock<HashMap<NodeId, String>>,
    /// Last failed RDMA connect per node, so a down node costs one
    /// connect attempt per `RDMA_RECONNECT_BACKOFF`, not one per write.
    #[cfg(feature = "rdma")]
    rdma_failed: parking_lot::Mutex<HashMap<NodeId, std::time::Instant>>,
    /// RPC clients for persist/fetch commands: node_id → RpcClient.
    #[cfg(feature = "rdma")]
    rpc_clients: RwLock<HashMap<NodeId, Arc<RpcClient>>>,
}

impl DistributedManager {
    fn placement_targets_for(&self, object_id: &str) -> Result<Vec<NodeId>> {
        let chunk_id = ChunkId::from_content(object_id.as_bytes());
        let cluster = self.cluster.read();
        placement::place_chunk(&chunk_id.0, &self.default_policy, &cluster)
            .map(|p| p.nodes)
            .map_err(NvstorError::Internal)
    }

    /// Capacity-aware target selection for new writes. Excludes any
    /// node whose `free_bytes() < data_size` so 100 KB writes don't land on
    /// a node with 1 KB free. Reads stay on `placement_targets_for` so they
    /// still find data on nodes whose capacity has since dropped below the
    /// chunk size.
    fn placement_targets_for_write(&self, object_id: &str, data_size: u64) -> Result<Vec<NodeId>> {
        let chunk_id = ChunkId::from_content(object_id.as_bytes());
        let cluster = self.cluster.read();
        placement::place_chunk_for_write(&chunk_id.0, &self.default_policy, &cluster, data_size)
            .map(|p| p.nodes)
            .map_err(NvstorError::Internal)
    }

    pub fn new(
        local_node_id: NodeId,
        local: Arc<ChunkManager>,
        cluster: ClusterMap,
        default_policy: PlacementPolicy,
    ) -> Self {
        let mut endpoints = HashMap::new();
        for node in &cluster.nodes {
            if node.id != local_node_id {
                let ep = node
                    .s3_address
                    .trim_start_matches("http://")
                    .trim_start_matches("https://")
                    .to_string();
                endpoints.insert(node.id, ep);
            }
        }

        Self {
            local_node_id,
            local,
            cluster: RwLock::new(cluster),
            #[cfg(feature = "rdma")]
            cluster_cred: RwLock::new(None),
            default_policy,
            #[cfg(any(feature = "rdma", test))]
            repair_cursor: parking_lot::Mutex::new(None),
            remote_endpoints: RwLock::new(endpoints),
            #[cfg(feature = "rdma")]
            rdma: None,
            #[cfg(feature = "rdma")]
            rdma_peers: RwLock::new(HashMap::new()),
            #[cfg(feature = "rdma")]
            rdma_addrs: RwLock::new(HashMap::new()),
            #[cfg(feature = "rdma")]
            rdma_failed: parking_lot::Mutex::new(HashMap::new()),
            #[cfg(feature = "rdma")]
            rpc_clients: RwLock::new(HashMap::new()),
        }
    }

    /// Configure the cluster's cross-node RPC clients without RDMA hardware.
    ///
    /// Uses the same `nvstor_rdma::rpc::RpcClient` as `set_rdma_transport`,
    /// but only the inline TCP ops (`OP_PERSIST_INLINE`, `OP_FETCH_INLINE`,
    /// `OP_EXISTS`, plus bucket / delete) — which is exactly what the
    /// replication and repair paths use. Lets nvstor-server replicate to
    /// peers on hosts without RDMA hardware (containers, dev boxes, CI),
    /// closes the silent data-loss path where a build without RDMA did
    /// not replicate at all, and lets
    /// `crates/nvstor-placement/tests/cluster.rs` run without RDMA.
    #[cfg(feature = "rdma")]
    pub fn set_tcp_rpc_transport(&mut self) {
        let cluster = self.cluster.read().clone();
        let mut rpc = HashMap::new();
        for node in &cluster.nodes {
            if node.id != self.local_node_id && node.rdma_port > 0 {
                let host = node.address.split(':').next().unwrap_or(&node.address);
                let rpc_addr = format!("{host}:{}", node.rdma_port + 1);
                let client = RpcClient::new(&rpc_addr);
                if let Some(ref c) = *self.cluster_cred.read() {
                    client.set_cluster_credential(c.clone());
                }
                rpc.insert(node.id, Arc::new(client));
            }
        }
        *self.rpc_clients.write() = rpc;
    }

    /// Set the RDMA transport and connect to all cluster peers.
    #[cfg(feature = "rdma")]
    /// Set the credential used to authenticate node-to-node RPC.
    /// Must be called before peer RPC clients are built, or they will be
    /// refused by the remote node.
    pub fn set_cluster_credential(&self, creds: nvstor_rdma::auth::Credentials) {
        *self.cluster_cred.write() = Some(creds);
    }

    /// Replication pushes with the S3-over-RDMA REPLICATE verb: the
    /// replica RDMA-READs the bytes out of this node's pool, so the
    /// transport's pool must be registered with remote read access, and
    /// this node authenticates to its peers with the cluster credential
    /// (set it first with `set_cluster_credential`).
    #[cfg(feature = "rdma")]
    pub fn set_rdma_transport(&mut self, transport: Arc<RdmaTransport>) {
        if let Some(ref c) = *self.cluster_cred.read() {
            transport.set_credentials(c.clone());
        }
        if !transport.pool().access().remote_read() {
            warn!("RDMA pool is not readable by peers; replication will use TCP");
        }
        // Connect to all remote nodes that have RDMA ports configured.
        let cluster = self.cluster.read().clone();
        let mut peers = HashMap::new();
        let mut addrs = HashMap::new();

        for node in &cluster.nodes {
            if node.id != self.local_node_id && node.rdma_port > 0 {
                let host = node.address.split(':').next().unwrap_or(&node.address);
                let rdma_addr = format!("{host}:{}", node.rdma_port);
                addrs.insert(node.id, rdma_addr.clone());
                match transport.connect(&rdma_addr) {
                    Ok(peer_id) => {
                        info!(node_id = node.id, %rdma_addr, peer_id, "RDMA connected to node");
                        peers.insert(node.id, peer_id);
                    }
                    Err(e) => {
                        warn!(node_id = node.id, %rdma_addr, error = %e, "RDMA connect failed, will use HTTP");
                    }
                }
            }
        }

        // Create RPC clients for persist/fetch commands (rdma_port + 1).
        let mut rpc = HashMap::new();
        for node in &cluster.nodes {
            if node.id != self.local_node_id && node.rdma_port > 0 {
                let host = node.address.split(':').next().unwrap_or(&node.address);
                let rpc_addr = format!("{host}:{}", node.rdma_port + 1);
                let client = RpcClient::new(&rpc_addr);
                if let Some(ref c) = *self.cluster_cred.read() {
                    client.set_cluster_credential(c.clone());
                }
                rpc.insert(node.id, Arc::new(client));
            }
        }

        *self.rdma_peers.write() = peers;
        *self.rdma_addrs.write() = addrs;
        *self.rpc_clients.write() = rpc;
        self.rdma = Some(transport);
    }

    /// Update cluster membership.
    pub fn update_cluster(&self, cluster: ClusterMap) {
        let mut eps = self.remote_endpoints.write();
        eps.clear();
        for node in &cluster.nodes {
            if node.id != self.local_node_id {
                let ep = node
                    .s3_address
                    .trim_start_matches("http://")
                    .trim_start_matches("https://")
                    .to_string();
                eps.insert(node.id, ep);
            }
        }
        *self.cluster.write() = cluster;
    }

    /// Write an object with PARALLEL async replication across the cluster.
    ///
    /// 1. Determine placement (which nodes should hold this data).
    /// 2. Write to local + all remote nodes IN PARALLEL (tokio::join).
    /// 3. Return success after min_write_nodes confirm.
    pub async fn distributed_write(
        &self,
        object_id: &str,
        data: &[u8],
        content_type: Option<&str>,
    ) -> Result<WriteResult> {
        // Capacity-aware placement: exclude nodes that can't hold the data
        // Reads still use placement_targets_for (without size), so
        // they find data on nodes whose remaining capacity has since fallen
        // below the chunk size — this is the same set that was selected
        // here at write time, modulo capacity exclusions.
        let target_nodes = self.placement_targets_for_write(object_id, data.len() as u64)?;

        // EC path: encode data into shards, write each shard to a different node.
        if let crate::policy::PlacementStrategy::ErasureCoded {
            data_shards,
            parity_shards,
        } = &self.default_policy.strategy
        {
            return self
                .distributed_write_ec(
                    object_id,
                    data,
                    content_type,
                    *data_shards as usize,
                    *parity_shards as usize,
                    &target_nodes,
                )
                .await;
        }

        // Replication path: write full data to each target node.
        let mut handles = Vec::new();

        for node_id in &target_nodes {
            let nid = *node_id;
            if nid == self.local_node_id {
                let result = self.local.object_write(object_id, data, content_type);
                handles.push(tokio::spawn(async move {
                    match result {
                        Ok(_) => (nid, Ok(())),
                        Err(e) => (nid, Err(e.to_string())),
                    }
                }));
            } else {
                // Remote write: try RDMA first, fall back to TCP RPC.
                #[cfg(feature = "rdma")]
                let rdma_result = self.try_rdma_write(nid, object_id, data);
                #[cfg(not(feature = "rdma"))]
                let rdma_result: Option<std::result::Result<(), String>> = None;

                // Try RDMA first, then TCP RPC fallback.
                // Both are synchronous — wrap in spawn_blocking to avoid
                // blocking the tokio runtime.
                match rdma_result {
                    Some(Ok(())) => {
                        handles.push(tokio::spawn(async move { (nid, Ok(())) }));
                    }
                    _ => {
                        // TCP RPC fallback (no HTTP — removed due to ID mangling).
                        #[cfg(feature = "rdma")]
                        {
                            let rpc_client = self.rpc_clients.read().get(&nid).cloned();

                            let oid = object_id.to_string();
                            let data = data.to_vec();
                            handles.push(tokio::spawn(async move {
                                if let Some(client) = rpc_client {
                                    // spawn_blocking: RPC is synchronous TCP, must not block tokio.
                                    let result = tokio::task::spawn_blocking(move || {
                                        client.persist_with_data(&oid, &data)
                                    })
                                    .await;
                                    match result {
                                        Ok(Ok(())) => (nid, Ok(())),
                                        Ok(Err(e)) => (nid, Err(e)),
                                        Err(e) => (nid, Err(format!("spawn_blocking: {e}"))),
                                    }
                                } else {
                                    (nid, Err(format!("no RPC client for node {nid}")))
                                }
                            }));
                        }
                        #[cfg(not(feature = "rdma"))]
                        handles.push(tokio::spawn(async move {
                            (
                                nid,
                                Err(format!("node {nid}: replication requires the rdma feature")),
                            )
                        }));
                    }
                }
            }
        }

        // Collect results.
        let mut write_count = 0u32;
        let mut errors = Vec::new();

        for handle in handles {
            match handle.await {
                Ok((nid, Ok(()))) => {
                    write_count += 1;
                    debug!(node = nid, object_id, "replicate: OK");
                }
                Ok((nid, Err(e))) => {
                    warn!(node = nid, error = %e, "replicate: FAIL");
                    errors.push((nid, e));
                }
                Err(e) => {
                    warn!(error = %e, "replicate: task panicked");
                }
            }
        }

        let min_writes = self.default_policy.min_write_nodes;
        if write_count >= min_writes {
            Ok(WriteResult {
                nodes_written: write_count,
                total_nodes: target_nodes.len() as u32,
                errors,
            })
        } else {
            Err(NvstorError::Internal(format!(
                "insufficient writes: {write_count}/{min_writes} (errors: {:?})",
                errors
            )))
        }
    }

    /// EC write: encode data into k+m shards, distribute each shard to a different node.
    async fn distributed_write_ec(
        &self,
        object_id: &str,
        data: &[u8],
        content_type: Option<&str>,
        k: usize,
        m: usize,
        target_nodes: &[NodeId],
    ) -> Result<WriteResult> {
        // Encode data into shards.
        let ec = ErasureConfig::new(k, m)?;
        let (shards, shard_info) = ec.encode(data)?;

        info!(
            object_id,
            k,
            m,
            shard_size = shard_info.shard_size,
            total_shards = shards.len(),
            "EC encode complete"
        );

        // Store shard metadata alongside the object so we can reconstruct later.
        // Key: {object_id}.__ec_meta__
        let ec_meta_key = format!("{object_id}.__ec_meta__");
        let ec_meta = serde_json::json!({
            "original_len": shard_info.original_len,
            "shard_size": shard_info.shard_size,
            "data_shards": shard_info.data_shards,
            "parity_shards": shard_info.parity_shards,
            "node_map": target_nodes.iter().enumerate()
                .map(|(i, nid)| (i, *nid))
                .collect::<Vec<(usize, u64)>>(),
        });
        let ec_meta_bytes =
            serde_json::to_vec(&ec_meta).map_err(|e| NvstorError::Internal(e.to_string()))?;
        self.local
            .object_write(&ec_meta_key, &ec_meta_bytes, Some("application/json"))?;

        // Distribute shards: each shard goes to a different node.
        let mut handles = Vec::new();
        for (shard_idx, shard_data) in shards.iter().enumerate() {
            let nid = if shard_idx < target_nodes.len() {
                target_nodes[shard_idx]
            } else {
                // More shards than nodes — wrap around (shouldn't happen with correct config).
                target_nodes[shard_idx % target_nodes.len()]
            };

            let shard_key = format!("{object_id}.__shard_{shard_idx}__");

            if nid == self.local_node_id {
                let result = self
                    .local
                    .object_write(&shard_key, shard_data, content_type);
                handles.push(tokio::spawn(async move {
                    match result {
                        Ok(_) => (nid, Ok(())),
                        Err(e) => (nid, Err(e.to_string())),
                    }
                }));
            } else {
                // Remote shard write via RPC.
                #[cfg(feature = "rdma")]
                let rpc_result = self.try_rdma_write(nid, &shard_key, shard_data);
                #[cfg(not(feature = "rdma"))]
                let rpc_result: Option<std::result::Result<(), String>> = None;

                match rpc_result {
                    Some(Ok(())) => {
                        handles.push(tokio::spawn(async move { (nid, Ok(())) }));
                    }
                    _ => {
                        #[cfg(feature = "rdma")]
                        {
                            let rpc_client = self.rpc_clients.read().get(&nid).cloned();

                            let sk = shard_key.clone();
                            let sd = shard_data.clone();
                            handles.push(tokio::spawn(async move {
                                if let Some(client) = rpc_client {
                                    let result = tokio::task::spawn_blocking(move || {
                                        client.persist_with_data(&sk, &sd)
                                    })
                                    .await;
                                    match result {
                                        Ok(Ok(())) => (nid, Ok(())),
                                        Ok(Err(e)) => (nid, Err(e)),
                                        Err(e) => (nid, Err(format!("spawn_blocking: {e}"))),
                                    }
                                } else {
                                    (nid, Err(format!("no RPC client for node {nid}")))
                                }
                            }));
                        }
                        #[cfg(not(feature = "rdma"))]
                        handles.push(tokio::spawn(async move {
                            (
                                nid,
                                Err(format!("node {nid}: replication requires the rdma feature")),
                            )
                        }));
                    }
                }
            }
        }

        // Collect results.
        let mut write_count = 0u32;
        let mut errors = Vec::new();
        for handle in handles {
            match handle.await {
                Ok((nid, Ok(()))) => {
                    write_count += 1;
                    debug!(node = nid, object_id, "EC shard write OK");
                }
                Ok((nid, Err(e))) => {
                    warn!(node = nid, error = %e, "EC shard write FAIL");
                    errors.push((nid, e));
                }
                Err(e) => {
                    warn!(error = %e, "EC shard task panicked");
                }
            }
        }

        // EC requires all k data shards written. Parity can tolerate m failures.
        let min_writes = k as u32; // At minimum, all data shards must succeed.
        if write_count >= min_writes {
            Ok(WriteResult {
                nodes_written: write_count,
                total_nodes: shards.len() as u32,
                errors,
            })
        } else {
            Err(NvstorError::Internal(format!(
                "EC write failed: {write_count}/{} shards written (errors: {:?})",
                shards.len(),
                errors
            )))
        }
    }

    /// Read an object, trying local first then remote nodes (async).
    pub async fn distributed_read(&self, object_id: &str) -> Result<Vec<u8>> {
        // Check if this object is EC-encoded.
        let ec_meta_key = format!("{object_id}.__ec_meta__");
        if let Ok(meta_bytes) = self.local.object_read(&ec_meta_key) {
            return self.distributed_read_ec(object_id, &meta_bytes).await;
        }

        // 1. Try local first (fast path — this node holds the data).
        if let Ok(data) = self.local.object_read(object_id) {
            return Ok(data);
        }

        // 2. Compute placement targets — ask ONLY those nodes, not all.
        // This is O(replica_count), not O(cluster_size).
        let target_nodes = self.placement_targets_for(object_id).unwrap_or_default();

        for node_id in &target_nodes {
            if *node_id == self.local_node_id {
                continue; // Already tried local.
            }

            // Fetch from placement target via RPC.
            #[cfg(feature = "rdma")]
            {
                let clients = self.rpc_clients.read();
                if let Some(client) = clients.get(node_id) {
                    match client.fetch_inline(object_id) {
                        Ok(data) => {
                            debug!(node = node_id, object_id, "RPC read OK");
                            return Ok(data);
                        }
                        Err(e) => {
                            warn!(node = node_id, object_id, error = %e, "RPC read failed, trying next node");
                        }
                    }
                } else {
                    debug!(node = node_id, "no RPC client for this node");
                }
            }
        }

        Err(NvstorError::NotFound)
    }

    /// EC read: fetch shards from nodes, decode with Reed-Solomon.
    async fn distributed_read_ec(&self, object_id: &str, meta_bytes: &[u8]) -> Result<Vec<u8>> {
        let meta: serde_json::Value = serde_json::from_slice(meta_bytes)
            .map_err(|e| NvstorError::Internal(format!("EC meta parse: {e}")))?;

        let k = meta["data_shards"]
            .as_u64()
            .ok_or(NvstorError::Internal("missing data_shards".into()))? as usize;
        let m = meta["parity_shards"]
            .as_u64()
            .ok_or(NvstorError::Internal("missing parity_shards".into()))? as usize;
        let original_len = meta["original_len"]
            .as_u64()
            .ok_or(NvstorError::Internal("missing original_len".into()))?
            as usize;
        let shard_size = meta["shard_size"]
            .as_u64()
            .ok_or(NvstorError::Internal("missing shard_size".into()))?
            as usize;
        let total = k + m;

        // Fetch all shards (try local first, then remote).
        let mut shards: Vec<Option<Vec<u8>>> = vec![None; total];
        let mut fetched = 0usize;

        for i in 0..total {
            let shard_key = format!("{object_id}.__shard_{i}__");

            // Try local.
            if let Ok(data) = self.local.object_read(&shard_key) {
                shards[i] = Some(data);
                fetched += 1;
                if fetched >= k {
                    break; // Have enough to decode.
                }
                continue;
            }

            // Try remote nodes via RPC fetch_inline.
            #[cfg(feature = "rdma")]
            {
                let clients = self.rpc_clients.read();
                for (nid, client) in clients.iter() {
                    match client.fetch_inline(&shard_key) {
                        Ok(data) => {
                            debug!(node = nid, shard = i, "EC shard fetched via RPC");
                            shards[i] = Some(data);
                            fetched += 1;
                            break;
                        }
                        Err(e) => {
                            debug!(node = nid, error = %e, "EC shard fetch failed");
                        }
                    }
                }
            }

            if fetched >= k {
                break;
            }
        }

        if fetched < k {
            return Err(NvstorError::Internal(format!(
                "EC read: only {fetched}/{k} shards available, cannot reconstruct"
            )));
        }

        // Decode.
        let ec = ErasureConfig::new(k, m)?;
        let shard_info = nvstor_storage::erasure::ShardInfo {
            original_len,
            shard_size,
            data_shards: k,
            parity_shards: m,
        };
        let data = ec.decode(&mut shards, &shard_info)?;

        info!(object_id, k, m, fetched, "EC decode complete");
        Ok(data)
    }

    /// The RDMA peer for `node_id`, connecting (again) if there is none.
    /// A failed connect is not retried for `RDMA_RECONNECT_BACKOFF`.
    #[cfg(feature = "rdma")]
    fn rdma_peer(&self, node_id: NodeId) -> Option<(Arc<RdmaTransport>, u64)> {
        let transport = self.rdma.as_ref()?;
        if let Some(&peer) = self.rdma_peers.read().get(&node_id) {
            return Some((Arc::clone(transport), peer));
        }
        let addr = self.rdma_addrs.read().get(&node_id)?.clone();
        {
            let failed = self.rdma_failed.lock();
            if failed
                .get(&node_id)
                .is_some_and(|t| t.elapsed() < RDMA_RECONNECT_BACKOFF)
            {
                return None;
            }
        }
        match transport.connect(&addr) {
            Ok(peer) => {
                info!(node_id, %addr, peer, "RDMA reconnected to node");
                self.rdma_failed.lock().remove(&node_id);
                self.rdma_peers.write().insert(node_id, peer);
                Some((Arc::clone(transport), peer))
            }
            Err(e) => {
                debug!(node_id, %addr, error = %e, "RDMA reconnect failed");
                self.rdma_failed
                    .lock()
                    .insert(node_id, std::time::Instant::now());
                None
            }
        }
    }

    /// Replicate `data` to a remote node over RDMA with the S3-over-RDMA
    /// REPLICATE verb: the bytes sit in one of this node's pool slots and
    /// the replica RDMA-READs them into a slot it reserved, checks their
    /// CRC-32C, and stores them. No pool address or rkey of the replica
    /// is involved, so concurrent replications cannot clobber each other
    /// or anything else in the replica's pool. `None` when RDMA cannot
    /// carry this write (no transport or peer, or larger than a slot);
    /// the caller then uses the TCP RPC.
    #[cfg(feature = "rdma")]
    fn try_rdma_write(
        &self,
        node_id: NodeId,
        object_id: &str,
        data: &[u8],
    ) -> Option<std::result::Result<(), String>> {
        let (transport, peer_id) = self.rdma_peer(node_id)?;
        if data.len() > transport.pool().slot_size() || !transport.pool().access().remote_read() {
            return None;
        }
        let client = nvstor_rdma::s3_rdma::S3RdmaClient::new(Arc::clone(&transport), peer_id);
        let r = client.replicate(object_id, data);
        if replicate_breaks_peer(&r) {
            warn!(node_id, object_id, error = ?r.as_ref().err(), "RDMA replicate failed; dropping the connection");
            self.rdma_peers.write().remove(&node_id);
            transport.disconnect(peer_id);
        }
        match r {
            Ok(_) => {
                debug!(node_id, object_id, len = data.len(), "RDMA replicate OK");
                Some(Ok(()))
            }
            Err(e) => {
                // Busy or broken, this write goes over the TCP RPC.
                debug!(node_id, object_id, error = %e, "RDMA replicate not done; using the TCP RPC");
                Some(Err(e.to_string()))
            }
        }
    }

    /// Replicate bucket creation to all remote nodes.
    #[cfg(feature = "rdma")]
    pub fn replicate_bucket_put(&self, bucket_name: &str) {
        let clients = self.rpc_clients.read();
        for (node_id, client) in clients.iter() {
            match client.bucket_put(bucket_name) {
                Ok(()) => info!(node_id, bucket_name, "bucket replicated"),
                Err(e) => warn!(node_id, bucket_name, error = %e, "bucket replication failed"),
            }
        }
    }

    /// Replicate bucket deletion to all remote nodes.
    #[cfg(feature = "rdma")]
    pub fn replicate_bucket_delete(&self, bucket_name: &str) {
        let clients = self.rpc_clients.read();
        for (node_id, client) in clients.iter() {
            match client.bucket_delete(bucket_name) {
                Ok(()) => info!(node_id, bucket_name, "bucket delete replicated"),
                Err(e) => {
                    warn!(node_id, bucket_name, error = %e, "bucket delete replication failed")
                }
            }
        }
    }

    /// Replicate object deletion to all remote nodes.
    #[cfg(feature = "rdma")]
    pub fn replicate_delete(&self, object_id: &str) {
        let clients = self.rpc_clients.read();
        for (node_id, client) in clients.iter() {
            match client.delete(object_id) {
                Ok(()) => debug!(node_id, object_id, "delete replicated"),
                Err(e) => warn!(node_id, object_id, error = %e, "delete replication failed"),
            }
        }
    }

    /// Background repair pass: scan local manifests, check remote replicas exist,
    /// re-replicate missing ones.
    /// Returns (scanned, repaired, errors).
    #[cfg(feature = "rdma")]
    pub fn repair_pass(&self) -> (u64, u64, u64) {
        let mut scanned = 0u64;
        let mut repaired = 0u64;
        let mut errors = 0u64;

        let manifests = match self.next_repair_page(1000) {
            Ok(manifests) => manifests,
            Err(error) => {
                warn!(%error, "repair: failed to list manifests");
                return (0, 0, 1);
            }
        };
        // Do not hold the membership lock across network I/O.
        let clients = self.rpc_clients.read().clone();
        if clients.is_empty() {
            return (0, 0, 0); // No remote nodes to repair to.
        }

        for manifest in &manifests {
            scanned += 1;

            let targets = match self.placement_targets_for(&manifest.object_id) {
                Ok(targets) => targets,
                Err(error) => {
                    warn!(%error, "repair: cannot determine replica targets");
                    errors += 1;
                    continue;
                }
            };
            for node_id in targets.iter().filter(|&&id| id != self.local_node_id) {
                let Some(client) = clients.get(node_id) else {
                    errors += 1;
                    continue;
                };
                match client.exists(&manifest.object_id) {
                    Ok(true) => {} // Remote has it — good.
                    Ok(false) => {
                        // Remote is missing this object — re-replicate.
                        info!(
                            node_id,
                            object_id = %manifest.object_id,
                            "repair: re-replicating missing object"
                        );
                        match self.local.object_read(&manifest.object_id) {
                            Ok(data) => {
                                match client.persist_with_data(&manifest.object_id, &data) {
                                    Ok(()) => repaired += 1,
                                    Err(e) => {
                                        warn!(node_id, error = %e, "repair: persist failed");
                                        errors += 1;
                                    }
                                }
                            }
                            Err(e) => {
                                warn!(object_id = %manifest.object_id, error = %e, "repair: local read failed");
                                errors += 1;
                            }
                        }
                    }
                    Err(e) => {
                        // RPC error — node might be down.
                        debug!(node_id, error = %e, "repair: exists check failed");
                        errors += 1;
                    }
                }
            }
        }

        if repaired > 0 || errors > 0 {
            info!(scanned, repaired, errors, "repair pass complete");
        }
        (scanned, repaired, errors)
    }

    #[cfg(any(feature = "rdma", test))]
    fn next_repair_page(&self, limit: u32) -> Result<Vec<nvstor_core::types::ObjectManifest>> {
        let mut cursor = self.repair_cursor.lock();
        let (manifests, truncated) = self.local.object_list("", cursor.as_deref(), limit)?;
        *cursor = if truncated {
            manifests.last().map(|manifest| manifest.object_id.clone())
        } else {
            None
        };
        Ok(manifests)
    }

    /// Repair pass — without the `rdma` feature there are no RPC clients,
    /// so nothing to do. Returns 0/0/0 and logs once per call so operators
    /// running this build know replicas can't be repaired.
    #[cfg(not(feature = "rdma"))]
    pub fn repair_pass(&self) -> (u64, u64, u64) {
        warn!(
            "repair_pass called on a build without the `rdma` feature — \
             cross-node repair is disabled. Rebuild with `--features rdma` \
             to enable."
        );
        (0, 0, 0)
    }

    /// Replicate bucket creation to all remote nodes.
    ///
    /// **Without the `rdma` feature this is a NOOP.** The TCP fallback
    /// path lives in `nvstor-rdma::rpc::RpcClient`; without that crate
    /// linked in, this build cannot fan out cluster mutations. We log a
    /// loud warning per call so operators running `--no-default-features`
    /// see the issue immediately instead of discovering replica
    /// divergence later (data-loss risk).
    #[cfg(not(feature = "rdma"))]
    pub fn replicate_bucket_put(&self, bucket_name: &str) {
        warn!(
            bucket_name,
            "replicate_bucket_put: rdma feature disabled — bucket NOT \
             replicated to peers. Replicas will diverge. Rebuild with \
             `--features rdma`."
        );
    }
    #[cfg(not(feature = "rdma"))]
    pub fn replicate_bucket_delete(&self, bucket_name: &str) {
        warn!(
            bucket_name,
            "replicate_bucket_delete: rdma feature disabled — bucket \
             delete NOT replicated to peers. Replicas will diverge. \
             Rebuild with `--features rdma`."
        );
    }
    #[cfg(not(feature = "rdma"))]
    pub fn replicate_delete(&self, object_id: &str) {
        warn!(
            object_id,
            "replicate_delete: rdma feature disabled — object delete \
             NOT replicated to peers. Replicas will diverge. Rebuild \
             with `--features rdma`."
        );
    }

    /// Get the local ChunkManager.
    pub fn local_manager(&self) -> &Arc<ChunkManager> {
        &self.local
    }

    /// Get cluster map.
    pub fn cluster_map(&self) -> ClusterMap {
        self.cluster.read().clone()
    }

    /// Mark a node as down.
    pub fn mark_node_down(&self, node_id: NodeId) {
        let mut cluster = self.cluster.write();
        for node in &mut cluster.nodes {
            if node.id == node_id {
                node.status = NodeStatus::Down;
                info!(node_id, "node marked down");
            }
        }
        cluster.epoch += 1;
    }

    /// Cluster health summary.
    pub fn health(&self) -> ClusterHealth {
        let cluster = self.cluster.read();
        ClusterHealth {
            total_nodes: cluster.node_count(),
            active_nodes: cluster.active_count(),
            epoch: cluster.epoch,
            policy: self.default_policy.name.clone(),
        }
    }
}

// HTTP replication functions removed — all replication uses RPC
// (PERSIST_INLINE / FETCH_INLINE). The old HTTP path had an object ID
// mangling bug (__replicate__ prefix doubling) and blocked the tokio runtime.

/// Result of a distributed write.
#[derive(Debug)]
pub struct WriteResult {
    pub nodes_written: u32,
    pub total_nodes: u32,
    pub errors: Vec<(NodeId, String)>,
}

/// Cluster health summary.
#[derive(Debug, Clone, serde::Serialize)]
pub struct ClusterHealth {
    pub total_nodes: usize,
    pub active_nodes: usize,
    pub epoch: u64,
    pub policy: String,
}

/// Whether a REPLICATE outcome means the RDMA connection is unusable. A
/// busy replica (NO_SLOT) is healthy: its write goes over the TCP RPC and
/// the connection is kept; any other failure drops it.
#[cfg(feature = "rdma")]
fn replicate_breaks_peer(r: &std::result::Result<u64, nvstor_rdma::s3_rdma::PushError>) -> bool {
    matches!(r, Err(nvstor_rdma::s3_rdma::PushError::Failed(_)))
}

#[cfg(test)]
mod tests {
    // A busy replica is not a broken one.
    #[cfg(feature = "rdma")]
    #[test]
    fn a_busy_replica_keeps_its_rdma_connection() {
        use nvstor_rdma::s3_rdma::PushError;
        assert!(!replicate_breaks_peer(&Err(PushError::Busy(
            "ALLOC: server busy (NO_SLOT)".into()
        ))));
        assert!(replicate_breaks_peer(&Err(PushError::Failed(
            "WC error".into()
        ))));
        assert!(!replicate_breaks_peer(&Ok(4096)));
    }

    use super::*;
    use crate::node::StorageNode;
    use nvstor_metadata::MetadataStore;
    use nvstor_storage::backend::PosixBackend;
    use nvstor_storage::manager::ChunkManagerConfig;

    fn setup() -> (tempfile::TempDir, Arc<ChunkManager>) {
        let tmp = tempfile::TempDir::new().unwrap();
        let data_dir = tmp.path().join("data");
        let meta_dir = tmp.path().join("meta");
        let backend = Box::new(PosixBackend::new(&data_dir).unwrap());
        let metadata = Arc::new(MetadataStore::open(&meta_dir).unwrap());
        let mgr = Arc::new(ChunkManager::new(
            backend,
            metadata,
            ChunkManagerConfig {
                disk_usage_threshold: 1.0,
                ..ChunkManagerConfig::default()
            },
        ));
        (tmp, mgr)
    }

    #[test]
    fn repair_cursor_visits_all_objects_and_wraps() {
        let (_tmp, mgr) = setup();
        for index in 0..1003 {
            mgr.object_write(&format!("object/{index:04}"), b"value", None)
                .unwrap();
        }
        let dm = DistributedManager::new(1, mgr, single_node_cluster(), PlacementPolicy::single());
        let first = dm.next_repair_page(1000).unwrap();
        let second = dm.next_repair_page(1000).unwrap();
        assert_eq!(first.len(), 1000);
        assert_eq!(second.len(), 3);
        assert_eq!(second[0].object_id, "object/1000");
        assert_eq!(
            dm.next_repair_page(1000).unwrap()[0].object_id,
            first[0].object_id
        );
    }

    fn single_node_cluster() -> ClusterMap {
        let mut map = ClusterMap::new();
        map.add_node(StorageNode::new(1, "127.0.0.1:9002"));
        map
    }

    fn test_cluster() -> ClusterMap {
        let mut map = ClusterMap::new();
        map.add_node(StorageNode::new(1, "10.0.0.1:9002"));
        map.add_node(StorageNode::new(2, "10.0.0.2:9002"));
        map.add_node(StorageNode::new(3, "10.0.0.3:9002"));
        map
    }

    #[tokio::test]
    async fn local_write_read() {
        let (_tmp, mgr) = setup();
        let dm = DistributedManager::new(1, mgr, single_node_cluster(), PlacementPolicy::single());

        let result = dm
            .distributed_write("test/obj1", b"hello distributed", None)
            .await
            .unwrap();
        assert_eq!(result.nodes_written, 1);

        let data = dm.distributed_read("test/obj1").await.unwrap();
        assert_eq!(data, b"hello distributed");
    }

    #[tokio::test]
    async fn write_single_policy() {
        let (_tmp, mgr) = setup();
        let dm = DistributedManager::new(1, mgr, single_node_cluster(), PlacementPolicy::single());

        let result = dm
            .distributed_write("test/s", b"single", None)
            .await
            .unwrap();
        assert_eq!(result.nodes_written, 1);
        let data = dm.distributed_read("test/s").await.unwrap();
        assert_eq!(data, b"single");
    }

    #[test]
    fn health_check() {
        let (_tmp, mgr) = setup();
        let dm = DistributedManager::new(1, mgr, test_cluster(), PlacementPolicy::replicated_3());
        let health = dm.health();
        assert_eq!(health.total_nodes, 3);
        assert_eq!(health.active_nodes, 3);
    }

    #[test]
    fn mark_node_down() {
        let (_tmp, mgr) = setup();
        let dm = DistributedManager::new(1, mgr, test_cluster(), PlacementPolicy::replicated_3());
        dm.mark_node_down(2);
        assert_eq!(dm.health().active_nodes, 2);
    }

    #[test]
    fn update_cluster() {
        let (_tmp, mgr) = setup();
        let dm = DistributedManager::new(1, mgr, test_cluster(), PlacementPolicy::replicated_3());
        let mut new_cluster = test_cluster();
        new_cluster.add_node(StorageNode::new(4, "10.0.0.4:9002"));
        dm.update_cluster(new_cluster);
        assert_eq!(dm.health().total_nodes, 4);
    }

    #[test]
    fn placement_targets_match_read_hashing() {
        let (_tmp, mgr) = setup();
        let dm = DistributedManager::new(1, mgr, test_cluster(), PlacementPolicy::replicated_3());

        let object_id = "bucket/object";
        let targets = dm.placement_targets_for(object_id).unwrap();

        let chunk_id = ChunkId::from_content(object_id.as_bytes());
        let expected = placement::place_chunk(
            &chunk_id.0,
            &PlacementPolicy::replicated_3(),
            &test_cluster(),
        )
        .unwrap()
        .nodes;

        assert_eq!(targets, expected);
    }

    // Builder for erasure-coding placement tests; kept warning-free
    // while no test uses it.
    #[allow(dead_code)]
    fn ec_cluster() -> ClusterMap {
        let mut map = ClusterMap::new();
        for i in 1..=6 {
            map.add_node(StorageNode::new(i, &format!("10.0.0.{i}:9002")));
        }
        map
    }

    #[tokio::test]
    async fn ec_write_creates_shards() {
        // Use single-node cluster so all shards go to local node.
        let (_tmp, mgr) = setup();
        let dm = DistributedManager::new(
            1,
            mgr.clone(),
            single_node_cluster(),
            PlacementPolicy::ec_4_2(),
        );

        let data = b"erasure coded data for testing the EC write path end to end";
        // EC 4+2 needs 6 nodes but we only have 1 — placement will only place
        // on node 1 (the only one). All 6 shards go to local node.
        let result = dm.distributed_write("ec-test/obj1", data, None).await;

        // Placement may fail if not enough nodes — check gracefully.
        match result {
            Ok(r) => {
                assert!(r.nodes_written >= 1);
            }
            Err(e) => {
                // Expected: insufficient nodes for EC 4+2.
                // Expected: not enough nodes for EC 4+2 on single-node cluster.
                assert!(
                    e.to_string().contains("not enough")
                        || e.to_string().contains("insufficient")
                        || e.to_string().contains("EC write failed"),
                    "unexpected error: {e}"
                );
                return;
            }
        }

        // Verify EC metadata was stored.
        let meta = mgr.object_read("ec-test/obj1.__ec_meta__").unwrap();
        let meta_json: serde_json::Value = serde_json::from_slice(&meta).unwrap();
        assert_eq!(meta_json["data_shards"], 4);
        assert_eq!(meta_json["parity_shards"], 2);
        assert_eq!(meta_json["original_len"], data.len());

        // Verify shards were written locally.
        for i in 0..6 {
            let shard = mgr.object_read(&format!("ec-test/obj1.__shard_{i}__"));
            assert!(shard.is_ok(), "shard {i} should exist");
        }
    }

    #[tokio::test]
    async fn ec_write_read_roundtrip() {
        // Single-node cluster: all shards go to local node.
        // Use single() policy instead of ec_4_2() to avoid placement rejection.
        // Instead, test the EC path directly by calling distributed_write_ec.
        let (_tmp, mgr) = setup();
        let mut cluster = ClusterMap::new();
        cluster.add_node(StorageNode::new(1, "127.0.0.1:9002"));

        let dm = DistributedManager::new(
            1,
            mgr.clone(),
            cluster,
            PlacementPolicy::single(), // Will be overridden by direct EC call.
        );

        let data = b"EC roundtrip: this data will be encoded, sharded, stored, fetched, decoded, and compared";
        let target_nodes = vec![1u64; 6]; // All shards to node 1.

        // Write via EC path directly.
        let result = dm
            .distributed_write_ec("ec-rt/obj1", data, None, 4, 2, &target_nodes)
            .await
            .unwrap();
        assert_eq!(result.nodes_written, 6);

        // Read via EC path.
        let recovered = dm.distributed_read("ec-rt/obj1").await.unwrap();
        assert_eq!(&recovered, data, "EC roundtrip data mismatch");
    }

    #[test]
    fn ec_encode_decode_roundtrip() {
        // Verify the codec works independently of distributed write.
        let data = b"hello erasure coding! this data will be split into 4 data + 2 parity shards";
        let ec = nvstor_storage::erasure::ErasureConfig::default_4_2();
        let (shards, info) = ec.encode(data).unwrap();
        assert_eq!(shards.len(), 6);
        assert_eq!(info.data_shards, 4);
        assert_eq!(info.parity_shards, 2);
        assert_eq!(info.original_len, data.len());

        // Decode with all shards present.
        let mut shard_opts: Vec<Option<Vec<u8>>> = shards.into_iter().map(Some).collect();
        let recovered = ec.decode(&mut shard_opts, &info).unwrap();
        assert_eq!(&recovered, data);

        // Decode with 2 missing (max tolerance for 4+2).
        let ec2 = nvstor_storage::erasure::ErasureConfig::default_4_2();
        let (shards2, info2) = ec2.encode(data).unwrap();
        let mut shard_opts2: Vec<Option<Vec<u8>>> = shards2.into_iter().map(Some).collect();
        shard_opts2[0] = None; // Missing shard 0
        shard_opts2[3] = None; // Missing shard 3
        let recovered2 = ec2.decode(&mut shard_opts2, &info2).unwrap();
        assert_eq!(&recovered2, data);
    }
}
