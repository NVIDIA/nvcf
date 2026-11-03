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

//! Server configuration — TOML file + environment variable + CLI overrides.
//!
//! Configuration priority: CLI args > env vars > config file > defaults.
//! Matches the config schema from SERVER_ORCHESTRATION.md.

use serde::Deserialize;

/// Complete server configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct ServerConfig {
    #[serde(default)]
    pub node: NodeConfig,

    #[serde(default)]
    pub cluster: ClusterConfig,

    #[serde(default)]
    pub storage: StorageConfig,

    #[serde(default)]
    pub network: NetworkConfig,

    #[serde(default)]
    pub nfs: NfsConfig,

    #[serde(default)]
    pub limits: LimitsConfig,

    #[serde(default)]
    pub tls: TlsConfig,

    #[serde(default)]
    pub auth: AuthConfig,

    #[serde(default)]
    pub kms: KmsConfig,

    #[serde(default)]
    pub logging: LoggingConfig,
}

impl Default for ServerConfig {
    fn default() -> Self {
        Self {
            node: NodeConfig::default(),
            cluster: ClusterConfig::default(),
            storage: StorageConfig::default(),
            network: NetworkConfig::default(),
            nfs: NfsConfig::default(),
            limits: LimitsConfig::default(),
            tls: TlsConfig::default(),
            auth: AuthConfig::default(),
            kms: KmsConfig::default(),
            logging: LoggingConfig::default(),
        }
    }
}

/// Node identity.
#[derive(Debug, Clone, Deserialize)]
pub struct NodeConfig {
    /// Node ID (unique within cluster). 0 = auto-assign.
    #[serde(default)]
    pub id: u64,
    /// Human-readable node name.
    #[serde(default = "default_node_name")]
    pub name: String,
}

impl Default for NodeConfig {
    fn default() -> Self {
        Self {
            id: 0,
            name: default_node_name(),
        }
    }
}

/// Cluster configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct ClusterConfig {
    /// Cluster name.
    #[serde(default = "default_cluster_name")]
    pub name: String,
    /// Bootstrap mode: true = new cluster, self is initial leader.
    #[serde(default)]
    pub bootstrap: bool,
    /// Seed peers for joining existing cluster.
    ///
    /// Each entry is either `"<addr>"` or `"<id>@<addr>"`. The explicit-ID
    /// form is recommended for production: every node then writes the same
    /// ID for the same peer, so cluster identity is unambiguous. Without it,
    /// the ID is derived from the address (stable hash), which is still
    /// consistent cluster-wide but not human-chosen.
    #[serde(default)]
    pub seeds: Vec<String>,
    /// This node's address as OTHER nodes reach it (host or host:port).
    ///
    /// Used to derive this node's own ID when `[node] id` is 0 (auto-assign),
    /// so peers and self agree on one identity. Defaults to the system
    /// hostname when unset.
    #[serde(default)]
    pub advertise_addr: Option<String>,
}

impl Default for ClusterConfig {
    fn default() -> Self {
        Self {
            name: default_cluster_name(),
            bootstrap: false,
            seeds: Vec::new(),
            advertise_addr: None,
        }
    }
}

/// Storage engine configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct StorageConfig {
    /// Base data directory.
    #[serde(default = "default_data_path")]
    pub data_path: String,
    /// WAL directory (defaults to {data_path}/wal).
    #[serde(default)]
    pub wal_path: Option<String>,
    /// Metadata directory (defaults to {data_path}/meta).
    #[serde(default)]
    pub metadata_path: Option<String>,
    /// Chunk size in bytes (default: 16 MiB).
    #[serde(default = "default_chunk_size")]
    pub chunk_size: usize,
    /// Enable O_DIRECT for I/O.
    #[serde(default)]
    pub direct_io: bool,
    /// Verify the IEEE CRC-32 chunk checksum on reads.
    #[serde(default = "default_true")]
    pub verify_checksums: bool,
    /// Enable background GC.
    #[serde(default)]
    pub gc_enabled: bool,
    /// GC sweep interval in seconds.
    #[serde(default = "default_gc_interval")]
    pub gc_interval_secs: u64,
    /// Durability mode for the S3-over-RDMA / S3-over-TCP fast path
    /// (PmObjectStore-backed checkpoint writes): "none" | "data" |
    /// "full". Controls whether a PUT ACK means the bytes are durable.
    ///   - "data" (default): fdatasync before ACK — the ACK is honest
    ///     against single-node power loss. Safe default.
    ///   - "full": fsync (data + file metadata) before ACK.
    ///   - "none": ACK on page-cache write only — fastest, but an
    ///     ACK'd write can be lost on power loss UNLESS a higher layer
    ///     (cross-node replication before ACK) provides durability.
    ///     Only select this when that layer exists or the data is
    ///     explicitly regenerable-cache.
    #[serde(default = "default_fast_path_sync")]
    pub fast_path_sync: String,
}

impl Default for StorageConfig {
    fn default() -> Self {
        Self {
            data_path: default_data_path(),
            wal_path: None,
            metadata_path: None,
            chunk_size: default_chunk_size(),
            direct_io: false,
            verify_checksums: true,
            gc_enabled: true,
            gc_interval_secs: default_gc_interval(),
            fast_path_sync: default_fast_path_sync(),
        }
    }
}

impl StorageConfig {
    /// Resolved WAL path.
    pub fn wal_dir(&self) -> String {
        self.wal_path
            .clone()
            .unwrap_or_else(|| format!("{}/wal", self.data_path))
    }

    /// Resolved metadata path.
    pub fn meta_dir(&self) -> String {
        self.metadata_path
            .clone()
            .unwrap_or_else(|| format!("{}/meta", self.data_path))
    }

    /// Resolved chunk data path.
    pub fn chunks_dir(&self) -> String {
        format!("{}/chunks", self.data_path)
    }

    /// Resolved position-mapped (block volume) data path.
    pub fn volumes_dir(&self) -> String {
        format!("{}/volumes", self.data_path)
    }
}

/// Network ports and transport configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct NetworkConfig {
    /// Native wire-protocol port.
    ///
    /// The field name remains `grpc_port` for config compatibility with older
    /// revisions of the project.
    #[serde(default = "default_grpc_port")]
    pub grpc_port: u16,
    /// HTTP health/metrics port.
    #[serde(default = "default_http_port")]
    pub http_port: u16,
    /// Raft consensus port.
    #[serde(default = "default_raft_port")]
    pub raft_port: u16,
    /// S3 HTTP gateway port.
    #[serde(default = "default_s3_port")]
    pub s3_port: u16,
    /// Base domain for virtual-host-style S3 addressing, e.g.
    /// "s3.example.com" or "127.0.0.1:9000".
    ///
    /// When set, a request whose `Host` is `<bucket>.<s3_domain>` is routed
    /// as a request for `<bucket>`, with the URI path treated as the object
    /// key. Path-style (`/{bucket}/{key}`) keeps working for requests whose
    /// Host is the bare domain.
    ///
    /// When unset, only path-style is supported — and a virtual-host client
    /// will have its key read as a bucket name. Set this if any
    /// client uses virtual-host addressing, which modern AWS SDKs do by
    /// default.
    #[serde(default)]
    pub s3_domain: Option<String>,
    /// NBD block device port.
    #[serde(default = "default_nbd_port")]
    pub nbd_port: u16,
    /// NFS server port.
    #[serde(default = "default_nfs_port")]
    pub nfs_port: u16,
    /// NFS RPC-over-RDMA port. Omitted = auto-enable 20049 on an active RDMA
    /// device; 0 disables it. An explicit nonzero port must bind successfully.
    #[serde(default)]
    pub nfs_rdma_port: Option<u16>,
    /// RDMA bootstrap port of rail 0; the node RPC uses `rdma_port + 1` and rail
    /// k >= 1 listens on `rdma_rail_port_base + k - 1`. RDMA is skipped when no
    /// RDMA device is present.
    #[serde(default = "default_rdma_port")]
    pub rdma_port: u16,
    /// First TCP bootstrap port for the second and later RDMA NICs
    /// ("rails"): rail k ≥ 1 listens on `base + k - 1`; rail 0 uses
    /// `rdma_port`. Clients learn the ports from the RAILS request, so
    /// only firewalls need to know them. Omitted = `rdma_port + 16`,
    /// clear of the RPC (`rdma_port + 1`) and S3-over-TCP ports.
    #[serde(default)]
    pub rdma_rail_port_base: Option<u16>,
    /// RDMA memory-pool slot size in MiB. Caps the per-op payload
    /// (S3-over-RDMA single PUT/GET, replication chunk). Default 16
    /// MiB matches typical S3 multipart-upload part size.
    #[serde(default = "default_rdma_pool_slot_mb")]
    pub rdma_pool_slot_mb: usize,
    /// RDMA memory-pool slot count. Caps concurrent in-flight ops per
    /// node. At 16 MiB × 256 slots = 4 GiB registered memory.
    #[serde(default = "default_rdma_pool_slots")]
    pub rdma_pool_slots: usize,
    /// S3-over-TCP port. Same callback shape as s3-over-RDMA but
    /// uses plain TCP — for clusters without ibverbs (nodes without an
    /// RDMA NIC, or whose pods are not given one; generic Ethernet). 0 disables.
    /// Defaults to 9879 (one above the RDMA RPC port to keep the
    /// numbering grouped).
    #[serde(default = "default_s3_tcp_port")]
    pub s3_tcp_port: u16,
    /// Let the S3-over-RDMA bootstrap listeners (`rdma_port` and the
    /// rail ports) take IPv6 connectors too: they bind `[::]` dual stack
    /// instead of `0.0.0.0`. Off by default, as for every other nvstor
    /// TCP listener: a host whose firewall has IPv4 rules only would
    /// otherwise expose these ports over IPv6.
    #[serde(default)]
    pub listen_ipv6: bool,
}

impl NetworkConfig {
    /// Address the RDMA bootstrap listeners bind; see `listen_ipv6`.
    pub fn rdma_listen_addr(&self) -> std::net::IpAddr {
        if self.listen_ipv6 {
            std::net::IpAddr::V6(std::net::Ipv6Addr::UNSPECIFIED)
        } else {
            std::net::IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED)
        }
    }

    /// Bootstrap port of RDMA rail 1; see `rdma_rail_port_base`.
    pub fn rdma_rail_port_base(&self) -> u16 {
        self.rdma_rail_port_base
            .unwrap_or_else(|| self.rdma_port.saturating_add(16))
    }
}

impl Default for NetworkConfig {
    fn default() -> Self {
        Self {
            grpc_port: default_grpc_port(),
            http_port: default_http_port(),
            raft_port: default_raft_port(),
            s3_port: default_s3_port(),
            s3_domain: None,
            nbd_port: default_nbd_port(),
            nfs_port: default_nfs_port(),
            nfs_rdma_port: None,
            rdma_port: default_rdma_port(),
            rdma_rail_port_base: None,
            rdma_pool_slot_mb: default_rdma_pool_slot_mb(),
            rdma_pool_slots: default_rdma_pool_slots(),
            s3_tcp_port: default_s3_tcp_port(),
            listen_ipv6: false,
        }
    }
}

/// Connection and request limits.
#[derive(Debug, Clone, Deserialize)]
pub struct LimitsConfig {
    /// Maximum concurrent connections per protocol.
    #[serde(default = "default_max_connections")]
    pub max_connections: usize,
    /// Request timeout in seconds.
    #[serde(default = "default_request_timeout")]
    pub request_timeout_secs: u64,
    /// Idle connection timeout in seconds.
    #[serde(default = "default_idle_timeout")]
    pub idle_timeout_secs: u64,
    /// Maximum S3 object size in bytes (default: 5 GiB).
    #[serde(default = "default_max_object_size")]
    pub max_object_size: u64,
    /// Maximum requests executing concurrently on the s3-rdma / s3-tcp
    /// transports. Bounds worker threads and their buffers so a fast or
    /// pipelining client applies backpressure instead of exhausting
    /// threads and memory. 0 = derive from CPU count.
    #[serde(default)]
    pub max_in_flight_requests: usize,
}

impl Default for LimitsConfig {
    fn default() -> Self {
        Self {
            max_connections: default_max_connections(),
            request_timeout_secs: default_request_timeout(),
            idle_timeout_secs: default_idle_timeout(),
            max_object_size: default_max_object_size(),
            max_in_flight_requests: 0, // 0 = derive from CPU count
        }
    }
}

/// TLS configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct TlsConfig {
    /// Path to PEM certificate file. If unset, TLS is disabled.
    #[serde(default)]
    pub cert_path: Option<String>,
    /// Path to PEM private key file.
    #[serde(default)]
    pub key_path: Option<String>,
}

impl Default for TlsConfig {
    fn default() -> Self {
        Self {
            cert_path: None,
            key_path: None,
        }
    }
}

/// NFS export configuration.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct NfsConfig {
    /// Clients that may connect to the NFS export (TCP and RDMA): addresses
    /// ("10.0.0.5", "fd00::1"), prefixes ("10.0.0.0/8") or "*" for every
    /// client. NFS has no other authentication (AUTH_SYS ids are claimed
    /// by the client), so empty, the default, admits loopback clients only.
    /// A non-empty list admits exactly what it names: list 127.0.0.1 (or
    /// ::1) too when local clients mount.
    #[serde(default)]
    pub allow_clients: Vec<String>,
    /// Where `allow_clients` came from, for messages: this section, or
    /// the NVSTOR_NFS_ALLOW_CLIENTS environment variable.
    #[serde(skip)]
    pub allow_clients_source: Option<&'static str>,
}

impl NfsConfig {
    /// Where the client list came from.
    pub fn source(&self) -> &'static str {
        self.allow_clients_source.unwrap_or("[nfs] allow_clients")
    }
}

/// Authentication configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct AuthConfig {
    /// S3 access key ID. If unset, auth is disabled (anonymous mode).
    #[serde(default)]
    pub access_key: Option<String>,
    /// S3 secret access key.
    #[serde(default)]
    pub secret_key: Option<String>,
}

impl Default for AuthConfig {
    fn default() -> Self {
        Self {
            access_key: None,
            secret_key: None,
        }
    }
}

/// KMS configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct KmsConfig {
    /// Root passphrase used to protect persisted master keys.
    /// If unset, KMS-backed server-side encryption is disabled.
    #[serde(default)]
    pub root_key_passphrase: Option<String>,
}

impl Default for KmsConfig {
    fn default() -> Self {
        Self {
            root_key_passphrase: None,
        }
    }
}

/// Logging configuration.
#[derive(Debug, Clone, Deserialize)]
pub struct LoggingConfig {
    /// Output logs as JSON (for structured log aggregation).
    #[serde(default)]
    pub json: bool,
    /// Log level filter (e.g., "info", "debug", "nvstor=debug,tower=warn").
    #[serde(default = "default_log_level")]
    pub level: String,
}

impl Default for LoggingConfig {
    fn default() -> Self {
        Self {
            json: false,
            level: default_log_level(),
        }
    }
}

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

fn default_node_name() -> String {
    hostname::get()
        .ok()
        .and_then(|h| h.into_string().ok())
        .unwrap_or_else(|| "nvstor-node".into())
}
fn default_cluster_name() -> String {
    "nvstor-cluster".into()
}
fn default_data_path() -> String {
    "/var/lib/nvstor/data".into()
}
fn default_chunk_size() -> usize {
    nvstor_core::DEFAULT_CHUNK_SIZE
}
fn default_true() -> bool {
    true
}
fn default_gc_interval() -> u64 {
    300
}
/// Fast-path durability defaults to "data" (fdatasync before ACK) so an
/// acknowledged checkpoint write is honest against single-node power
/// loss out of the box. Operators with replication-before-ACK or
/// regenerable-cache semantics can lower it to "none" for max speed.
fn default_fast_path_sync() -> String {
    "data".into()
}
fn default_grpc_port() -> u16 {
    nvstor_core::DEFAULT_GRPC_PORT
}
fn default_http_port() -> u16 {
    nvstor_core::DEFAULT_HTTP_PORT
}
fn default_raft_port() -> u16 {
    nvstor_core::DEFAULT_RAFT_PORT
}
fn default_s3_port() -> u16 {
    9000
}
fn default_nbd_port() -> u16 {
    nvstor_core::DEFAULT_NBD_PORT
}
fn default_nfs_port() -> u16 {
    nvstor_core::DEFAULT_NFS_PORT
}
fn default_rdma_port() -> u16 {
    9877
}
fn default_rdma_pool_slot_mb() -> usize {
    16
}
fn default_rdma_pool_slots() -> usize {
    256
}
fn default_s3_tcp_port() -> u16 {
    9879
}
fn default_max_connections() -> usize {
    10_000
}
fn default_request_timeout() -> u64 {
    30
}
fn default_idle_timeout() -> u64 {
    300
}
fn default_max_object_size() -> u64 {
    5 * 1024 * 1024 * 1024
} // 5 GiB
fn default_log_level() -> String {
    "info".into()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_default_config() {
        let cfg = ServerConfig::default();
        assert_eq!(cfg.network.grpc_port, 9100);
        assert_eq!(cfg.network.http_port, 9101);
        assert_eq!(cfg.network.raft_port, 9102);
        assert_eq!(cfg.network.nbd_port, 10809);
        assert_eq!(cfg.network.nfs_port, 2049);
        assert!(
            !cfg.network.listen_ipv6,
            "RDMA listeners are IPv4-only unless asked"
        );
        assert_eq!(
            cfg.network.rdma_listen_addr(),
            std::net::IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED)
        );
        assert_eq!(cfg.storage.chunk_size, 16 * 1024 * 1024);
        assert!(cfg.storage.verify_checksums);
        assert!(
            cfg.nfs.allow_clients.is_empty(),
            "the NFS export admits loopback clients only by default"
        );
    }

    #[test]
    fn nfs_allow_clients_parse() {
        let cfg: ServerConfig =
            toml::from_str("[nfs]\nallow_clients = [\"10.0.0.0/8\", \"fd00::/8\"]\n")
                .expect("parse");
        assert_eq!(
            cfg.nfs.allow_clients,
            vec!["10.0.0.0/8".to_string(), "fd00::/8".to_string()]
        );
    }

    #[test]
    fn test_toml_parsing() {
        let toml_str = r#"
[node]
id = 1
name = "nvstor-1"

[storage]
data_path = "/data/nvstor"
chunk_size = 4194304
gc_enabled = true

[network]
grpc_port = 9200
s3_port = 9300

[auth]
access_key = "AKID"
secret_key = "SECRET"

[kms]
root_key_passphrase = "kms-root-passphrase"

[logging]
json = true
level = "debug"
"#;
        let cfg: ServerConfig = toml::from_str(toml_str).unwrap();
        assert_eq!(cfg.node.id, 1);
        assert_eq!(cfg.node.name, "nvstor-1");
        assert_eq!(cfg.storage.data_path, "/data/nvstor");
        assert_eq!(cfg.storage.chunk_size, 4194304);
        assert!(cfg.storage.gc_enabled);
        assert_eq!(cfg.network.grpc_port, 9200);
        assert_eq!(cfg.network.s3_port, 9300);
        assert_eq!(cfg.auth.access_key.as_deref(), Some("AKID"));
        assert_eq!(
            cfg.kms.root_key_passphrase.as_deref(),
            Some("kms-root-passphrase")
        );
        assert!(cfg.logging.json);
        assert!(!cfg.network.listen_ipv6);
    }

    #[test]
    fn listen_ipv6_makes_rdma_listeners_dual_stack() {
        let cfg: NetworkConfig = toml::from_str("listen_ipv6 = true").expect("parse");
        assert!(cfg.listen_ipv6);
        assert_eq!(
            cfg.rdma_listen_addr(),
            std::net::IpAddr::V6(std::net::Ipv6Addr::UNSPECIFIED)
        );
        let cfg: NetworkConfig = toml::from_str("").expect("parse");
        assert_eq!(
            cfg.rdma_listen_addr(),
            std::net::IpAddr::V4(std::net::Ipv4Addr::UNSPECIFIED)
        );
    }

    #[test]
    fn test_storage_paths() {
        let cfg = StorageConfig {
            data_path: "/data".into(),
            wal_path: None,
            metadata_path: Some("/meta".into()),
            ..Default::default()
        };
        assert_eq!(cfg.wal_dir(), "/data/wal");
        assert_eq!(cfg.meta_dir(), "/meta");
        assert_eq!(cfg.chunks_dir(), "/data/chunks");
    }

    #[test]
    fn test_env_override_pattern() {
        // Verify the config can be deserialized from a minimal TOML
        let cfg: ServerConfig = toml::from_str("").unwrap();
        assert_eq!(cfg.network.grpc_port, 9100); // default
    }
}
