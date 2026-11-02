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

//! Node discovery and health monitoring via heartbeat protocol.
//!
//! Each node periodically sends heartbeats to all known peers.
//! Heartbeats include the node's current cluster map, enabling gossip-style
//! membership propagation. Missing heartbeats mark nodes as suspect → down.
//!
//! Protocol (TCP, binary):
//!   [magic:u32] [node_id:u64] [epoch:u64] [node_count:u16] [nodes...]
//!   Each node: [id:u64] [addr_len:u16] [addr] [status:u8] [rdma_port:u16]

use std::collections::HashMap;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use parking_lot::RwLock;
use tracing::{debug, info, warn};

use crate::node::{ClusterMap, NodeId, NodeStatus, StorageNode};

const HEARTBEAT_MAGIC: u32 = 0xBED20C01;
const HEARTBEAT_INTERVAL: Duration = Duration::from_secs(5);
const SUSPECT_AFTER: Duration = Duration::from_secs(15);
const DOWN_AFTER: Duration = Duration::from_secs(30);

/// Discovery service — manages cluster membership via heartbeats.
pub struct Discovery {
    local_id: NodeId,
    local_addr: String,
    local_rdma_port: u16,
    cluster: Arc<RwLock<ClusterMap>>,
    last_seen: Arc<RwLock<HashMap<NodeId, Instant>>>,
    seeds: Vec<String>,
    shutdown: Arc<std::sync::atomic::AtomicBool>,
}

impl Discovery {
    pub fn new(
        local_id: NodeId,
        local_addr: &str,
        local_rdma_port: u16,
        cluster: Arc<RwLock<ClusterMap>>,
        seeds: Vec<String>,
    ) -> Self {
        Self {
            local_id,
            local_addr: local_addr.to_string(),
            local_rdma_port,
            cluster,
            last_seen: Arc::new(RwLock::new(HashMap::new())),
            seeds,
            shutdown: Arc::new(std::sync::atomic::AtomicBool::new(false)),
        }
    }

    /// Start the discovery service (heartbeat sender + receiver).
    pub fn start(&self, port: u16) {
        // Start receiver.
        let cluster = Arc::clone(&self.cluster);
        let last_seen = Arc::clone(&self.last_seen);
        let local_id = self.local_id;
        let shutdown = Arc::clone(&self.shutdown);

        thread::spawn(move || {
            let listener = match TcpListener::bind(format!("0.0.0.0:{port}")) {
                Ok(l) => l,
                Err(e) => {
                    warn!(error = %e, "discovery: failed to bind port {port}");
                    return;
                }
            };
            listener.set_nonblocking(true).ok();
            info!(port, "discovery service listening");

            loop {
                if shutdown.load(std::sync::atomic::Ordering::Relaxed) {
                    break;
                }
                match listener.accept() {
                    Ok((stream, _)) => {
                        let c = Arc::clone(&cluster);
                        let ls = Arc::clone(&last_seen);
                        thread::spawn(move || {
                            handle_heartbeat(stream, &c, &ls, local_id);
                        });
                    }
                    Err(ref e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(50));
                    }
                    Err(_) => {
                        thread::sleep(Duration::from_millis(100));
                    }
                }
            }
        });

        // Start sender.
        let cluster = Arc::clone(&self.cluster);
        let last_seen = Arc::clone(&self.last_seen);
        let local_id = self.local_id;
        let local_addr = self.local_addr.clone();
        let local_rdma_port = self.local_rdma_port;
        let seeds = self.seeds.clone();
        let shutdown = Arc::clone(&self.shutdown);

        thread::spawn(move || {
            info!("discovery: heartbeat sender started");
            loop {
                if shutdown.load(std::sync::atomic::Ordering::Relaxed) {
                    break;
                }
                thread::sleep(HEARTBEAT_INTERVAL);

                // Collect all known peer addresses.
                let mut targets: Vec<String> = seeds.clone();
                {
                    let c = cluster.read();
                    for node in &c.nodes {
                        if node.id != local_id {
                            let host = node.address.split(':').next().unwrap_or(&node.address);
                            let addr = format!("{host}:{}", port);
                            if !targets.contains(&addr) {
                                targets.push(addr);
                            }
                        }
                    }
                }

                // Send heartbeat to all peers.
                let hb = build_heartbeat(local_id, &local_addr, local_rdma_port, &cluster.read());
                for target in &targets {
                    // Replace seed port with discovery port.
                    let host = target.split(':').next().unwrap_or(target);
                    let disc_addr = format!("{host}:{port}");
                    if let Err(e) = send_heartbeat(&disc_addr, &hb) {
                        debug!(target = %disc_addr, error = %e, "heartbeat send failed");
                    }
                }

                // Check for suspect/down nodes.
                check_node_health(&cluster, &last_seen);
            }
        });
    }

    pub fn shutdown(&self) {
        self.shutdown
            .store(true, std::sync::atomic::Ordering::Relaxed);
    }
}

fn build_heartbeat(
    local_id: NodeId,
    _local_addr: &str,
    _rdma_port: u16,
    cluster: &ClusterMap,
) -> Vec<u8> {
    let mut buf = Vec::new();
    buf.extend_from_slice(&HEARTBEAT_MAGIC.to_le_bytes());
    buf.extend_from_slice(&local_id.to_le_bytes());
    buf.extend_from_slice(&cluster.epoch.to_le_bytes());
    buf.extend_from_slice(&(cluster.nodes.len() as u16).to_le_bytes());

    for node in &cluster.nodes {
        buf.extend_from_slice(&node.id.to_le_bytes());
        let addr_bytes = node.address.as_bytes();
        buf.extend_from_slice(&(addr_bytes.len() as u16).to_le_bytes());
        buf.extend_from_slice(addr_bytes);
        buf.push(match node.status {
            NodeStatus::Active => 0,
            NodeStatus::Suspect => 1,
            NodeStatus::Down => 2,
            NodeStatus::Draining => 3,
        });
        buf.extend_from_slice(&node.rdma_port.to_le_bytes());
    }

    buf
}

fn send_heartbeat(addr: &str, data: &[u8]) -> Result<(), String> {
    let mut stream = TcpStream::connect_timeout(
        &addr.parse().map_err(|e| format!("parse {addr}: {e}"))?,
        Duration::from_secs(2),
    )
    .map_err(|e| format!("connect {addr}: {e}"))?;

    stream.set_write_timeout(Some(Duration::from_secs(2))).ok();
    stream.write_all(data).map_err(|e| format!("write: {e}"))?;
    Ok(())
}

fn handle_heartbeat(
    mut stream: TcpStream,
    cluster: &RwLock<ClusterMap>,
    last_seen: &RwLock<HashMap<NodeId, Instant>>,
    local_id: NodeId,
) {
    stream.set_read_timeout(Some(Duration::from_secs(5))).ok();

    let mut magic_buf = [0u8; 4];
    if stream.read_exact(&mut magic_buf).is_err() {
        return;
    }
    if u32::from_le_bytes(magic_buf) != HEARTBEAT_MAGIC {
        return;
    }

    let mut id_buf = [0u8; 8];
    if stream.read_exact(&mut id_buf).is_err() {
        return;
    }
    let sender_id = u64::from_le_bytes(id_buf);

    let mut epoch_buf = [0u8; 8];
    if stream.read_exact(&mut epoch_buf).is_err() {
        return;
    }
    let sender_epoch = u64::from_le_bytes(epoch_buf);

    let mut count_buf = [0u8; 2];
    if stream.read_exact(&mut count_buf).is_err() {
        return;
    }
    let node_count = u16::from_le_bytes(count_buf) as usize;

    // Parse nodes from heartbeat.
    let mut remote_nodes = Vec::new();
    for _ in 0..node_count {
        let mut nid = [0u8; 8];
        if stream.read_exact(&mut nid).is_err() {
            break;
        }
        let node_id = u64::from_le_bytes(nid);

        let mut alen = [0u8; 2];
        if stream.read_exact(&mut alen).is_err() {
            break;
        }
        let addr_len = u16::from_le_bytes(alen) as usize;

        let mut addr_buf = vec![0u8; addr_len];
        if stream.read_exact(&mut addr_buf).is_err() {
            break;
        }
        let address = String::from_utf8_lossy(&addr_buf).to_string();

        let mut status = [0u8; 1];
        if stream.read_exact(&mut status).is_err() {
            break;
        }

        let mut rp = [0u8; 2];
        if stream.read_exact(&mut rp).is_err() {
            break;
        }
        let rdma_port = u16::from_le_bytes(rp);

        remote_nodes.push((node_id, address, status[0], rdma_port));
    }

    // Update last_seen for the sender.
    last_seen.write().insert(sender_id, Instant::now());

    // Merge: add any nodes we don't know about.
    let mut c = cluster.write();
    let known_ids: Vec<NodeId> = c.nodes.iter().map(|n| n.id).collect();

    for (nid, addr, _status, rdma_port) in &remote_nodes {
        if *nid != local_id && !known_ids.contains(nid) {
            let mut node = StorageNode::new(*nid, addr);
            node.rdma_port = *rdma_port;
            info!(node_id = nid, address = %addr, "discovery: new node joined");
            c.add_node(node);
            c.epoch = c.epoch.max(sender_epoch) + 1;
        }
    }

    // Mark sender as active if it was suspect/down.
    for node in &mut c.nodes {
        if node.id == sender_id && node.status != NodeStatus::Active {
            info!(node_id = sender_id, "discovery: node back online");
            node.status = NodeStatus::Active;
        }
    }
}

fn check_node_health(cluster: &RwLock<ClusterMap>, last_seen: &RwLock<HashMap<NodeId, Instant>>) {
    let now = Instant::now();
    let ls = last_seen.read();
    let mut c = cluster.write();
    let mut epoch_bump = false;

    for node in &mut c.nodes {
        if let Some(last) = ls.get(&node.id) {
            let elapsed = now.duration_since(*last);
            if elapsed > DOWN_AFTER && node.status != NodeStatus::Down {
                warn!(
                    node_id = node.id,
                    elapsed_secs = elapsed.as_secs(),
                    "discovery: node DOWN"
                );
                node.status = NodeStatus::Down;
                epoch_bump = true;
            } else if elapsed > SUSPECT_AFTER && node.status == NodeStatus::Active {
                warn!(
                    node_id = node.id,
                    elapsed_secs = elapsed.as_secs(),
                    "discovery: node SUSPECT"
                );
                node.status = NodeStatus::Suspect;
            }
        }
    }

    if epoch_bump {
        c.epoch += 1;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn heartbeat_roundtrip() {
        let mut cluster = ClusterMap::new();
        cluster.add_node(StorageNode::new(1, "10.0.0.1:9002"));
        cluster.add_node(StorageNode::new(2, "10.0.0.2:9002"));

        let hb = build_heartbeat(1, "10.0.0.1:9002", 9877, &cluster);
        assert!(hb.len() > 20); // Magic + id + epoch + count + 2 nodes
        assert_eq!(
            u32::from_le_bytes(hb[0..4].try_into().unwrap()),
            HEARTBEAT_MAGIC
        );
    }
}
