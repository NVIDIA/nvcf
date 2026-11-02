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

//! nvstor storage server — multi-protocol daemon.
//!
//! Serves storage via:
//! - HTTP :9101 — health/readiness/status endpoints
//! - S3 :9000, native wire :9100, NBD :10809, NFS :2049
//!
//! Lifecycle per SERVER_ORCHESTRATION.md:
//! 1. Parse config (TOML + env + CLI)
//! 2. Initialize storage (create dirs, verify permissions)
//! 3. Open metadata store (with schema migration if needed)
//! 4. Replay WAL from checkpoint
//! 5. Initialize servers
//! 6. Mark Running when all components healthy
//! 7. Graceful shutdown on SIGTERM

mod admin;
mod audit;
mod config;
mod health;
mod kms;
mod metrics;
mod wire;

use std::sync::atomic::AtomicBool;
use std::sync::Arc;

use clap::Parser;
use tokio::signal;

use config::ServerConfig;

#[derive(Parser)]
#[command(name = "nvstor-server", about = "nvstor distributed storage server")]
struct Cli {
    /// Path to TOML config file.
    #[arg(long)]
    config: Option<String>,

    /// Data directory (overrides config file).
    #[arg(long)]
    data_path: Option<String>,

    /// Bootstrap new cluster (self becomes initial leader).
    #[arg(long)]
    bootstrap: bool,
}

#[tokio::main]
async fn main() {
    let cli = Cli::parse();

    // 1. Load config: file → defaults, then CLI overrides.
    let mut cfg = load_config(&cli);
    if let Some(ref path) = cli.data_path {
        cfg.storage.data_path = path.clone();
    }
    if cli.bootstrap {
        cfg.cluster.bootstrap = true;
    }

    // Apply environment variable overrides.
    apply_env_overrides(&mut cfg);
    if let Err(error) = check_nfs_clients(&cfg.nfs.allow_clients) {
        eprintln!("error: {}: {error}", cfg.nfs.source());
        std::process::exit(1);
    }
    let node_id_was_explicit = cfg.node.id > 0 || cfg.cluster.advertise_addr.is_some();
    cfg.node.id = resolve_node_id(
        &cfg.node.name,
        cfg.node.id,
        cfg.cluster.advertise_addr.as_deref(),
    );

    // 2. Initialize logging.
    init_logging(&cfg.logging);

    // Identity must be agreed cluster-wide. If we're joining peers but
    // fell through to the name-ordinal/1 fallback, every such node picks the
    // same ID — placement and Raft would then attribute data and votes to the
    // wrong machine. Warn loudly with the fix.
    if !cfg.cluster.seeds.is_empty() && !node_id_was_explicit {
        tracing::warn!(
            node_id = cfg.node.id,
            node_name = %cfg.node.name,
            "node ID was inferred from the node name; in a multi-node cluster set \
             [node] id explicitly (with \"<id>@<addr>\" seeds) or set \
             [cluster] advertise_addr — inferred IDs can collide across nodes"
        );
    }

    tracing::info!(
        node_id = cfg.node.id,
        node_name = %cfg.node.name,
        cluster = %cfg.cluster.name,
        data_path = %cfg.storage.data_path,
        "starting nvstor-server"
    );

    // Resolve the in-flight request cap once. 0 = derive from CPU count.
    let max_in_flight = if cfg.limits.max_in_flight_requests > 0 {
        cfg.limits.max_in_flight_requests
    } else {
        nvstor_rdma::admission::default_max_in_flight()
    };
    tracing::info!(max_in_flight, "request admission limit");

    // 3. Initialize storage directories.
    for dir in [
        &cfg.storage.data_path,
        &cfg.storage.chunks_dir(),
        &cfg.storage.volumes_dir(),
        &cfg.storage.wal_dir(),
        &cfg.storage.meta_dir(),
    ] {
        std::fs::create_dir_all(dir).unwrap_or_else(|e| {
            tracing::error!(dir, error = %e, "failed to create directory");
            std::process::exit(1);
        });
    }

    // 4. Open metadata store.
    let meta_store =
        nvstor_metadata::MetadataStore::open(std::path::Path::new(&cfg.storage.meta_dir()))
            .unwrap_or_else(|e| {
                tracing::error!(error = %e, "failed to open metadata store");
                std::process::exit(1);
            });

    // 5. Initialize storage backend + ChunkManager.
    // io_uring is the preferred backend on Linux (O_DIRECT, async I/O).
    // Falls back to POSIX if io_uring is unavailable.
    let backend = if cfg.storage.direct_io {
        match nvstor_storage::backend::IoUringBackend::new(&cfg.storage.chunks_dir()) {
            Ok(b) => {
                tracing::info!("storage backend: io_uring (O_DIRECT, async)");
                Box::new(b) as Box<dyn nvstor_storage::backend::StoreBackend>
            }
            Err(e) => {
                tracing::warn!(error = %e, "io_uring unavailable, falling back to POSIX");
                Box::new(
                    nvstor_storage::backend::PosixBackend::new(&cfg.storage.chunks_dir())
                        .unwrap_or_else(|e| {
                            tracing::error!(error = %e, "failed to create storage backend");
                            std::process::exit(1);
                        }),
                ) as Box<dyn nvstor_storage::backend::StoreBackend>
            }
        }
    } else {
        tracing::info!("storage backend: POSIX");
        Box::new(
            nvstor_storage::backend::PosixBackend::new(&cfg.storage.chunks_dir()).unwrap_or_else(
                |e| {
                    tracing::error!(error = %e, "failed to create storage backend");
                    std::process::exit(1);
                },
            ),
        ) as Box<dyn nvstor_storage::backend::StoreBackend>
    };

    let meta_store = Arc::new(meta_store);
    // Hoisted above the RDMA/TCP transports: they now authenticate every
    // request and need the IAM store to resolve credentials, so it has
    // to exist before they are constructed.
    let iam_store = Arc::new(nvstor_iam::store::IamStore::new(meta_store.clone()));
    // Bootstrap credential for the authenticated transports, shared
    // with the S3 HTTP gateway below.
    let transport_config_cred: Option<(String, String)> =
        match (cfg.auth.access_key.clone(), cfg.auth.secret_key.clone()) {
            (Some(ak), Some(sk)) => Some((ak, sk)),
            _ => None,
        };
    let mgr_config = nvstor_storage::manager::ChunkManagerConfig {
        chunk_size: cfg.storage.chunk_size,
        verify_checksums: cfg.storage.verify_checksums,
        ..Default::default()
    };
    let mut mgr =
        nvstor_storage::manager::ChunkManager::open(backend, meta_store.clone(), mgr_config)
            .unwrap_or_else(|error| {
                tracing::error!(%error, "failed to reload legacy volume catalog");
                std::process::exit(1);
            });
    if let Err(e) = mgr.enable_wal(std::path::Path::new(&cfg.storage.wal_dir())) {
        tracing::error!(error = %e, "failed to enable WAL");
        std::process::exit(1);
    }
    let mgr = Arc::new(mgr);

    tracing::info!(
        chunk_size = cfg.storage.chunk_size,
        verify_checksums = cfg.storage.verify_checksums,
        "storage engine initialized"
    );

    // 6. Readiness flag + metrics.
    let ready = Arc::new(AtomicBool::new(false));
    let start_time = std::time::Instant::now();
    let server_metrics = Arc::new(metrics::Metrics::new());

    // 7. Start health/status/metrics HTTP server (health_state created below after distributed_mgr).
    let health_addr = format!("0.0.0.0:{}", cfg.network.http_port);
    let health_listener = tokio::net::TcpListener::bind(&health_addr)
        .await
        .unwrap_or_else(|e| {
            tracing::error!(%health_addr, error = %e, "failed to bind health port");
            std::process::exit(1);
        });
    tracing::info!(%health_addr, "health/status endpoint listening");

    // Stores flushed and services drained at shutdown; filled by 7a and 7b.
    let mut durable_stores: Vec<Arc<dyn nvstor_storage::pm::PmStore>> = Vec::new();
    let mut transport_shutdown: Vec<Box<dyn Fn() -> bool + Send>> = Vec::new();
    // 7a. Object fast paths: S3-over-RDMA and S3-over-TCP over one shared
    // PmObjectStore. These serve clients directly, so they run in
    // single-node mode as well as in a cluster; only cross-node RPC
    // (below) needs cluster membership.
    // Both transports must share lifecycle locks and cached handles.
    // Separate stores for one directory retain stale generations after rename.
    let pm_objects: Arc<nvstor_storage::PmObjectStore> = {
        let base = std::path::Path::new(&cfg.storage.data_path).join("pm_objects");
        let pm_inner: Arc<dyn nvstor_storage::PmStore> = if cfg.storage.direct_io {
            Arc::new(
                nvstor_storage::pm::iouring::IoUringPmStore::new(&base)
                    .expect("init shared IoUringPmStore for object transports"),
            )
        } else {
            Arc::new(
                nvstor_storage::pm::posix::PosixPmStore::new(&base)
                    .expect("init shared PosixPmStore for object transports"),
            )
        };
        {
            durable_stores.push(pm_inner.clone());
            Arc::new(nvstor_storage::PmObjectStore::new(pm_inner))
        }
    };

    // One RDMA transport per ACTIVE HCA ("rail"). S3-over-RDMA serves on
    // every rail; rail 0 listens on `rdma_port`.
    let rdma_rails = open_rdma_rails(
        &cfg,
        make_handshake_verifier(Arc::clone(&iam_store), transport_config_cred.clone()),
    );
    // In cluster mode, replication pushes leave from a dial-only transport
    // of their own on rail 0's HCA (see `open_replication_transport`), so
    // no client-facing pool is ever readable by a peer. A single node
    // registers no such pool.
    let cluster_mode = !cfg.cluster.seeds.is_empty() || cfg.cluster.bootstrap;
    let rdma_transport = if cluster_mode {
        rdma_rails
            .first()
            .and_then(|(t, _)| open_replication_transport(&cfg, t))
    } else {
        None
    };
    if let Some(t) = rdma_transport.clone() {
        // Drain stops new pushes and drops the QPs to the replicas.
        transport_shutdown.push(Box::new(move || {
            t.shutdown();
            true
        }));
    }
    if !rdma_rails.is_empty() {
        // S3-over-RDMA server, one per rail. Replicas push to this
        // node with REPLICATE over rail 0 like any other client, after
        // authenticating with the cluster credential; their requests are
        // handled here too.
        // S3-over-RDMA fast path. For the GPU checkpoint
        // workload (huge opaque blobs, no dedup value)
        // we route the bytes through PmStore — same
        // pwrite/io_uring path NBD and NFS use. The
        // CA-chunked ChunkManager path is reserved for
        // the regular S3 gateway (dedup, versioning,
        // lifecycle).
        tracing::info!(
            backend = if cfg.storage.direct_io {
                "io_uring"
            } else {
                "posix"
            },
            "S3-over-RDMA fast path: PmStore-backed object store"
        );

        let pmo_w = Arc::clone(&pm_objects);
        let fp_sync = fast_path_sync_mode(&cfg.storage.fast_path_sync);
        tracing::info!(mode = ?fp_sync,
            "S3-over-RDMA fast path durability (PUT ACK barrier)");
        let s3_write_fn: nvstor_rdma::s3_rdma::WriteFn =
            Arc::new(move |key, data| pmo_w.put(key, data, fp_sync).map_err(|e| e.to_string()));
        let s3_read_fn = object_read_fn(Arc::clone(&pm_objects));
        let pmo_d = Arc::clone(&pm_objects);
        let s3_delete_fn: nvstor_rdma::s3_rdma::DeleteFn =
            Arc::new(move |key| pmo_d.delete(key).map_err(|e| e.to_string()));
        let rail_info: Vec<nvstor_rdma::rail::RailInfo> = rdma_rails
            .iter()
            .map(|(t, port)| nvstor_rdma::rail::RailInfo {
                port: *port,
                gid: t.local_gid(),
            })
            .collect();
        // One in-flight cap for the node, however many rails serve it.
        let admission = nvstor_rdma::admission::Admission::new(max_in_flight);
        // Cluster replication arrives as REPLICATE from peers that
        // authenticated with the cluster credential, and lands in the
        // chunk store, as the TCP RPC fallback does.
        let replicate_fn: Option<nvstor_rdma::s3_rdma::WriteFn> = cluster_mode.then(|| {
            let mgr = mgr.clone();
            let f: nvstor_rdma::s3_rdma::WriteFn = Arc::new(move |oid, data| {
                mgr.object_write(oid, data, None)
                    .map(|_| data.len() as u64)
                    .map_err(|e| e.to_string())
            });
            f
        });
        for (rail, (transport, port)) in rdma_rails.iter().enumerate() {
            let mut s3_rdma_server = nvstor_rdma::s3_rdma::S3RdmaServer::with_admission(
                Arc::clone(&s3_write_fn),
                Arc::clone(&s3_read_fn),
                Arc::clone(&s3_delete_fn),
                Arc::clone(transport),
                Arc::clone(&admission),
            )
            .with_auth(make_transport_auth(
                Arc::clone(&iam_store),
                transport_config_cred.clone(),
            ))
            .with_rails(rail_info.clone());
            if let Some(ref f) = replicate_fn {
                s3_rdma_server = s3_rdma_server.with_replication(Arc::clone(f));
            }
            s3_rdma_server.start();
            let service = s3_rdma_server.clone();
            transport_shutdown.push(Box::new(move || {
                service.shutdown();
                service.drained()
            }));
            tracing::info!(rail, port, device = %transport.device().name,
                "S3-over-RDMA server registered on RDMA rail");
        }
    }

    // S3-over-TCP server. Independent of RDMA and of cluster mode —
    // gated only on `s3_tcp_port > 0`. Lets the same nvstor-server
    // binary serve the object protocol over either transport (or
    // both at once during a migration).
    //
    // Share the exact PM store with RDMA so overwrites invalidate the
    // same handle cache and use the same lifecycle locks.
    if cfg.network.s3_tcp_port > 0 {
        let pm_objects_tcp = Arc::clone(&pm_objects);

        let pmo_w = Arc::clone(&pm_objects_tcp);
        let fp_sync = fast_path_sync_mode(&cfg.storage.fast_path_sync);
        tracing::info!(mode = ?fp_sync,
            "S3-over-TCP fast path durability (PUT ACK barrier)");
        // PUT payloads stream from the socket into the store in bounded
        // chunks; a multi-GiB object never sits in server memory.
        let tcp_put_fn: nvstor_rdma::tcp::server::PutStreamFn =
            Arc::new(move |key, size, fill| pmo_w.put_from(key, size, fp_sync, fill));
        let tcp_read_fn = object_read_fn(Arc::clone(&pm_objects_tcp));
        let pmo_d = Arc::clone(&pm_objects_tcp);
        let tcp_delete_fn: nvstor_rdma::tcp::server::DeleteFn =
            Arc::new(move |key| pmo_d.delete(key).map_err(|e| e.to_string()));

        let tcp_transport = nvstor_rdma::tcp::TcpTransport::new();
        let tcp_server = nvstor_rdma::tcp::S3TcpServer::with_max_in_flight(
            tcp_put_fn,
            tcp_read_fn,
            tcp_delete_fn,
            Arc::clone(&tcp_transport),
            max_in_flight,
        )
        .with_auth(make_transport_auth(
            Arc::clone(&iam_store),
            transport_config_cred.clone(),
        ));
        tcp_server.start();
        let service = tcp_server.clone();
        transport_shutdown.push(Box::new(move || {
            service.shutdown();
            service.drained()
        }));
        match tcp_transport.listen(cfg.network.s3_tcp_port) {
            Ok(addr) => tracing::info!(
                port = cfg.network.s3_tcp_port,
                listen_addr = %addr,
                "S3-over-TCP server listening (PmStore-backed fast path)",
            ),
            Err(e) => tracing::warn!(error = %e, "s3-tcp listen failed"),
        }
    }

    // 7b. Distributed placement manager (before S3 state so it can be wired in).
    let distributed_mgr: Option<Arc<nvstor_placement::distributed::DistributedManager>> = if !cfg
        .cluster
        .seeds
        .is_empty()
        || cfg.cluster.bootstrap
    {
        let local_id = cfg.node.id;
        let mut cluster = nvstor_placement::node::ClusterMap::new();
        let local_address = cfg
            .cluster
            .advertise_addr
            .clone()
            .unwrap_or_else(|| format!("{}:{}", cfg.node.name, cfg.network.grpc_port));
        let mut local_node = nvstor_placement::node::StorageNode::new(local_id, &local_address);
        local_node.rdma_port = cfg.network.rdma_port;
        let local_host = local_address.split(':').next().unwrap_or(&local_address);
        local_node.s3_address = format!("http://{local_host}:{}", cfg.network.s3_port);
        cluster.add_node(local_node);
        for seed in cfg.cluster.seeds.iter() {
            // Derive the peer's ID from its ADDRESS, never from this
            // node's seed-list index. Index-derived IDs meant the same
            // machine had a different ID on every node, so placement
            // computed here targeted different machines than the same
            // object computed on a peer — replicas scattered and reads
            // missed. `parse_seed` also accepts an explicit "<id>@<addr>".
            let (seed_id, seed_addr) = nvstor_placement::node::parse_seed(seed);
            let host = seed_addr.split(':').next().unwrap_or(&seed_addr);
            if seed_id == local_id {
                tracing::warn!(
                    seed = %seed_addr, id = seed_id,
                    "seed resolves to this node's own ID; skipping (check [node] id / seeds)"
                );
                continue;
            }
            let mut node = nvstor_placement::node::StorageNode::new(seed_id, &seed_addr);
            node.s3_address = format!("http://{}:{}", host, cfg.network.s3_port);
            node.rdma_port = cfg.network.rdma_port;
            cluster.add_node(node);
        }
        // Auto-select placement policy based on cluster size.
        let node_count = cluster.node_count();
        let policy = if node_count >= 3 {
            nvstor_placement::policy::PlacementPolicy::replicated_3()
        } else {
            nvstor_placement::policy::PlacementPolicy::replicated_2()
        };
        tracing::info!(node_count, policy = %policy.name, "placement policy selected");

        let mut dm = nvstor_placement::distributed::DistributedManager::new(
            local_id,
            mgr.clone(),
            cluster,
            policy,
        );
        // Node-to-node RPC authenticates per connection. Set the
        // credential BEFORE any transport wiring builds peer RpcClients,
        // or they connect without one and the remote refuses them.
        if let Some((ref ak, ref sk)) = transport_config_cred {
            dm.set_cluster_credential(nvstor_rdma::auth::Credentials::new(ak, sk));
        } else {
            tracing::warn!("no [auth] credential: cross-node RPC will be refused by peers");
        }

        let mut cross_node_ready = false;

        // Cross-node RPC rides the node's RDMA transport when one
        // was opened above (fast path); otherwise the TCP fallback
        // below takes over.
        if let Some(transport) = rdma_transport.clone() {
            let rdma_port = cfg.network.rdma_port;
            // Start RPC server for persist/fetch.
            let persist_mgr = mgr.clone();
            let fetch_mgr = mgr.clone();
            let persist_fn: nvstor_rdma::rpc::PersistFn = Arc::new(move |oid, data| {
                persist_mgr
                    .object_write(oid, data, None)
                    .map(|_| ())
                    .map_err(|e| e.to_string())
            });
            let fetch_fn: nvstor_rdma::rpc::FetchFn =
                Arc::new(move |oid| fetch_mgr.object_read(oid).map_err(|e| e.to_string()));
            let mut rpc_server = nvstor_rdma::rpc::RpcServer::new(persist_fn, fetch_fn);
            // The RPC port serves raw chunk fetch by
            // object id; require the cluster credential.
            if let Some((ref ak, ref sk)) = transport_config_cred {
                rpc_server.set_cluster_credential(nvstor_rdma::auth::Credentials::new(ak, sk));
            }

            // Wire bucket and delete operations.
            let bucket_meta = meta_store.clone();
            rpc_server.set_bucket_put(Arc::new(move |name| {
                let now = std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_secs() as i64;
                let meta = nvstor_metadata::BucketMeta {
                    name: name.to_string(),
                    owner: "default".to_string(),
                    created_at: now,
                    region: "us-east-1".to_string(),
                    versioning: 0,
                };
                bucket_meta.put_bucket(&meta).map_err(|e| e.to_string())
            }));

            let bucket_del_meta = meta_store.clone();
            rpc_server.set_bucket_delete(Arc::new(move |name| {
                bucket_del_meta
                    .delete_bucket(name)
                    .map_err(|e| e.to_string())
            }));

            let delete_mgr = mgr.clone();
            rpc_server.set_delete(Arc::new(move |oid| {
                delete_mgr.object_delete(oid).map_err(|e| e.to_string())
            }));

            if let Err(e) = rpc_server.start(rdma_port + 1) {
                tracing::warn!(error = %e, "RDMA RPC server failed");
            } else {
                tracing::info!(port = rdma_port + 1, "RDMA RPC server listening");
            }

            transport_shutdown.push(Box::new(move || {
                rpc_server.shutdown();
                rpc_server.drained()
            }));

            dm.set_rdma_transport(transport);
            cross_node_ready = true;
        }

        // TCP fallback. Always tries — if the RDMA branch above
        // already populated rpc_clients, this is a no-op replacement
        // with the same client set; otherwise it stands up a TCP-only
        // RpcServer for peers and points DistributedManager at the same
        // peer addresses.
        if !cross_node_ready {
            let persist_mgr = mgr.clone();
            let persist_fn: nvstor_rdma::rpc::PersistFn = Arc::new(move |oid, data| {
                persist_mgr
                    .object_write(oid, data, None)
                    .map(|_| ())
                    .map_err(|e| e.to_string())
            });
            let fetch_mgr = mgr.clone();
            let fetch_fn: nvstor_rdma::rpc::FetchFn =
                Arc::new(move |oid| fetch_mgr.object_read(oid).map_err(|e| e.to_string()));
            let mut rpc_server = nvstor_rdma::rpc::RpcServer::new(persist_fn, fetch_fn);
            if let Some((ref ak, ref sk)) = transport_config_cred {
                rpc_server.set_cluster_credential(nvstor_rdma::auth::Credentials::new(ak, sk));
            }

            // Bucket + delete callbacks (same wiring as the RDMA path).
            let bucket_meta = meta_store.clone();
            rpc_server.set_bucket_put(Arc::new(move |name| {
                let now = std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .unwrap_or_default()
                    .as_secs() as i64;
                let meta = nvstor_metadata::BucketMeta {
                    name: name.to_string(),
                    owner: "default".to_string(),
                    created_at: now,
                    region: "us-east-1".to_string(),
                    versioning: 0,
                };
                bucket_meta.put_bucket(&meta).map_err(|e| e.to_string())
            }));
            let bucket_del_meta = meta_store.clone();
            rpc_server.set_bucket_delete(Arc::new(move |name| {
                bucket_del_meta
                    .delete_bucket(name)
                    .map_err(|e| e.to_string())
            }));
            let delete_mgr = mgr.clone();
            rpc_server.set_delete(Arc::new(move |oid| {
                delete_mgr.object_delete(oid).map_err(|e| e.to_string())
            }));

            let rpc_port = cfg.network.rdma_port + 1;
            if let Err(e) = rpc_server.start(rpc_port) {
                tracing::warn!(error = %e,
                        "TCP-only RPC server failed — replication will not work");
            } else {
                tracing::info!(port = rpc_port, "TCP-only RPC server listening");
                dm.set_tcp_rpc_transport();
                cross_node_ready = true;
                tracing::info!(
                    "cross-node RPC: TCP-only mode (no RDMA hardware). \
                         Replication / repair / EC ops use plain TCP."
                );
            }
            transport_shutdown.push(Box::new(move || {
                rpc_server.shutdown();
                rpc_server.drained()
            }));
        }
        // _ = cross_node_ready binding kept for future readiness gating.
        let _ = cross_node_ready;

        // Start node discovery (heartbeat gossip).
        let disc_cluster = Arc::new(parking_lot::RwLock::new(dm.cluster_map()));
        // Keep a handle: discovery owns the live map, and the propagation
        // task below feeds its updates back into the placement manager.
        let disc_cluster_handle = Arc::clone(&disc_cluster);
        let disc_seeds: Vec<String> = cfg
            .cluster
            .seeds
            .iter()
            .filter_map(|seed| {
                let (id, address) = nvstor_placement::node::parse_seed(seed);
                if id == local_id {
                    return None;
                }
                let host = address.split(':').next().unwrap_or(&address);
                Some(format!("{host}:9103"))
            })
            .collect();
        let discovery = nvstor_placement::discovery::Discovery::new(
            local_id,
            &local_address,
            cfg.network.rdma_port,
            disc_cluster,
            disc_seeds,
        );
        discovery.start(9103);
        transport_shutdown.push(Box::new(move || {
            discovery.shutdown();
            true
        }));
        tracing::info!(port = 9103, "node discovery service started");

        let dm = Arc::new(dm);

        // Discovery mutates its OWN cluster map (built from a clone of
        // the manager's), so without this the placement map was frozen at
        // boot — nodes marked down by heartbeat health checks stayed write
        // targets forever, and nodes that joined later were never used.
        // Propagate discovery's view into the manager whenever the gossip
        // epoch advances (epoch is bumped on every membership change).
        {
            let disc_view = Arc::clone(&disc_cluster_handle);
            let dm_sink = Arc::clone(&dm);
            tokio::spawn(async move {
                let mut applied_epoch = 0u64;
                loop {
                    tokio::time::sleep(std::time::Duration::from_secs(2)).await;
                    let snapshot = { disc_view.read().clone() };
                    if snapshot.epoch != applied_epoch {
                        applied_epoch = snapshot.epoch;
                        let active = snapshot.active_count();
                        let total = snapshot.node_count();
                        dm_sink.update_cluster(snapshot);
                        tracing::info!(
                            epoch = applied_epoch,
                            active,
                            total,
                            "placement map updated from discovery"
                        );
                    }
                }
            });
        }

        tracing::info!(nodes = dm.health().total_nodes, policy = %dm.health().policy, "distributed placement initialized");
        Some(dm)
    } else {
        None
    };

    // 8. S3 HTTP gateway.
    // Raft consensus (if cluster peers configured). Initialized HERE,
    // before the S3 AppState, so metadata mutations (bucket/manifest/
    // version) can be proposed through Raft for cross-node consistency
    // instead of writing only the coordinator's local RocksDB. The
    // MetadataStateMachine applies committed entries to each node's
    // MetadataStore, so followers converge.
    let raft_node: Option<Arc<nvstor_raft::node::RaftNode>> =
        if !cfg.cluster.seeds.is_empty() || cfg.cluster.bootstrap {
            let raft_dir = std::path::Path::new(&cfg.storage.data_path).join("raft");
            std::fs::create_dir_all(&raft_dir).unwrap_or_else(|e| {
                tracing::error!(error = %e, "failed to create raft directory");
                std::process::exit(1);
            });

            let raft_log = nvstor_raft::log::RaftLog::open(&raft_dir).unwrap_or_else(|e| {
                tracing::error!(error = %e, "failed to open raft log");
                std::process::exit(1);
            });

            // Raft peer identity must be address-derived, not seed-index
            // derived. The old `(i + 2)` scheme assumed "we are 1" and that every
            // node listed its seeds in the same order — when either held false,
            // two nodes disagreed about which machine a given node_id names, so
            // votes and AppendEntries were attributed to the wrong peer. Uses the
            // same mapping as placement, so a node has ONE identity cluster-wide.
            let peers: Vec<nvstor_raft::types::PeerConfig> = cfg
                .cluster
                .seeds
                .iter()
                .map(|entry| {
                    let (node_id, addr) = nvstor_placement::node::parse_seed(entry);
                    nvstor_raft::types::PeerConfig { node_id, addr }
                })
                .filter(|p| p.node_id != cfg.node.id)
                .collect();

            let raft_network = Arc::new(nvstor_raft::network::RaftNetwork::new(cfg.node.id, peers));

            let raft_config = nvstor_raft::types::RaftConfig {
                node_id: cfg.node.id,
                raft_port: cfg.network.raft_port,
                ..Default::default()
            };

            let sm = Box::new(nvstor_raft::state_machine::MetadataStateMachine::new(
                meta_store.clone(),
            ));
            let node = Arc::new(nvstor_raft::node::RaftNode::new(
                raft_config,
                raft_log,
                raft_network,
                sm,
            ));
            tracing::info!(
                node_id = cfg.node.id,
                raft_port = cfg.network.raft_port,
                peers = cfg.cluster.seeds.len(),
                "raft consensus initialized"
            );
            Some(node)
        } else {
            tracing::info!("raft consensus disabled (no cluster peers configured)");
            None
        };

    let kms_manager = cfg.kms.root_key_passphrase.as_deref().map(|passphrase| {
        let mut backend =
            nvstor_kms::backend::LocalBackend::from_passphrase(meta_store.clone(), passphrase);
        if let Some(raft) = raft_node.clone() {
            backend = kms::replicate(backend, meta_store.clone(), raft).unwrap_or_else(|error| {
                tracing::error!(%error, "cannot enable cluster KMS");
                std::process::exit(1);
            });
        }
        let manager = Arc::new(nvstor_kms::manager::KmsManager::new(Arc::new(backend)));
        // Cluster creation waits for the first leader request, after Raft starts running.
        if raft_node.is_none() {
            manager.ensure_default_key().unwrap_or_else(|e| {
                tracing::error!(error = %e, "failed to initialize default KMS key");
                std::process::exit(1);
            });
        }
        tracing::info!("KMS-backed server-side encryption enabled");
        manager
    });
    if kms_manager.is_none() {
        tracing::info!("KMS disabled (no root key passphrase configured)");
    }

    let persisted_user_count = iam_store.list_users().map(|users| users.len()).unwrap_or(0);
    let s3_state = nvstor_object::s3::AppState {
        manager: mgr.clone(),
        metadata: meta_store.clone(),
        quota: Arc::new(nvstor_object::quota::QuotaManager::unlimited()),
        raft: raft_node.clone(),
        iam: Some(iam_store.clone()),
        kms: kms_manager.clone(),
        distributed: distributed_mgr.clone(),
    };
    // Set up S3 authentication.
    let cred_store = Arc::new(nvstor_object::auth::CredentialStore::from_iam_store(
        &iam_store,
    ));
    if let (Some(ak), Some(sk)) = (&cfg.auth.access_key, &cfg.auth.secret_key) {
        cred_store.add(ak, sk);
        tracing::info!(access_key = %ak, "S3 SigV4 authentication enabled via config credentials");
    }
    if cred_store.is_anonymous() {
        tracing::warn!(
            "S3 authentication DISABLED (no config credentials or IAM users configured)"
        );
        // The s3-rdma / s3-tcp transports enforce auth unconditionally,
        // so with no credential anywhere they will reject every request.
        // Say so explicitly: otherwise the first symptom is an opaque
        // STATUS_UNAUTHORIZED from every client.
        tracing::warn!(
            "s3-rdma / s3-tcp will REJECT ALL requests: no [auth] access_key/secret_key \
             and no IAM users. Set [auth] credentials to bootstrap."
        );
    } else {
        tracing::info!(persisted_user_count, "IAM-backed credential store loaded");
    }

    let s3_metrics = server_metrics.clone();
    // Rewrite virtual-host-style requests (bucket in Host) into the
    // path-style form the router understands. Outermost layer so it runs
    // before auth and routing; no-op unless [network] s3_domain is set.
    let vhost_cfg = nvstor_object::s3::VirtualHostConfig::new(cfg.network.s3_domain.clone());
    match cfg.network.s3_domain {
        Some(ref d) => tracing::info!(s3_domain = %d,
            "S3 virtual-host addressing enabled"),
        None => tracing::info!(
            "S3 virtual-host addressing disabled (set [network] s3_domain to enable);              path-style only"),
    }
    let s3_router = nvstor_object::s3::router_with_virtual_host(s3_state, vhost_cfg)
        .layer(axum::middleware::from_fn(audit::audit_layer))
        .layer(axum::middleware::from_fn(
            nvstor_object::auth::auth_middleware,
        ))
        .layer(axum::extract::Extension(iam_store))
        .layer(axum::extract::Extension(cred_store))
        .layer(axum::middleware::from_fn(move |req, next| {
            metrics::s3_metrics_layer(Arc::clone(&s3_metrics), req, next)
        }))
        .layer(axum::extract::DefaultBodyLimit::max(
            cfg.limits.max_object_size as usize,
        ));
    let s3_addr = format!("0.0.0.0:{}", cfg.network.s3_port);
    let s3_tls_config = if let (Some(cert), Some(key)) = (&cfg.tls.cert_path, &cfg.tls.key_path) {
        tracing::info!(cert = %cert, key = %key, "S3 TLS enabled");
        Some(
            axum_server::tls_rustls::RustlsConfig::from_pem_file(cert, key)
                .await
                .unwrap_or_else(|e| {
                    tracing::error!(error = %e, "failed to load TLS certificates");
                    std::process::exit(1);
                }),
        )
    } else {
        tracing::info!("S3 TLS disabled (no cert/key in config)");
        None
    };
    let s3_listener = tokio::net::TcpListener::bind(&s3_addr)
        .await
        .unwrap_or_else(|e| {
            tracing::error!(%s3_addr, error = %e, "failed to bind S3 port");
            std::process::exit(1);
        });
    tracing::info!(%s3_addr, "S3 HTTP gateway listening");

    // 9. NBD block device server.
    //
    // Block volumes go through `PmStore`: one sparse file per
    // volume, mutable in place. A content-addressed chunk path would
    // amplify every small WRITE into a whole-chunk rewrite.
    //
    // When `storage.direct_io` is set we wire the
    // io_uring-backed PmStore. Same trait, same on-disk layout, but
    // pwrite/pread go through io_uring SQEs instead of libc syscalls
    // — and write+fsync ride one submit as linked SQEs. Falls back
    // to POSIX if io_uring init fails (older kernels).
    let pm_store: Arc<dyn nvstor_storage::pm::PmStore> = if cfg.storage.direct_io {
        match nvstor_storage::pm::iouring::IoUringPmStore::new(cfg.storage.volumes_dir()) {
            Ok(s) => {
                tracing::info!("PmStore backend: io_uring");
                Arc::new(s)
            }
            Err(e) => {
                tracing::warn!(error = %e,
                    "io_uring PmStore init failed, falling back to POSIX");
                Arc::new(
                    nvstor_storage::pm::posix::PosixPmStore::new(cfg.storage.volumes_dir())
                        .unwrap_or_else(|e| {
                            tracing::error!(error = %e, "failed to initialize PmStore for NBD");
                            std::process::exit(1);
                        }),
                )
            }
        }
    } else {
        tracing::info!("PmStore backend: POSIX");
        Arc::new(
            nvstor_storage::pm::posix::PosixPmStore::new(cfg.storage.volumes_dir()).unwrap_or_else(
                |e| {
                    tracing::error!(error = %e, "failed to initialize PmStore for NBD");
                    std::process::exit(1);
                },
            ),
        )
    };
    durable_stores.push(pm_store.clone());
    let nbd_config = nvstor_block::nbd::NbdConfig {
        listen_addr: format!("0.0.0.0:{}", cfg.network.nbd_port),
        max_connections: cfg.limits.max_connections,
    };
    let nbd_server = nvstor_block::nbd::NbdServer::new(pm_store.clone(), nbd_config);
    tracing::info!(
        nbd_port = cfg.network.nbd_port,
        "NBD server configured (PmStore backend)"
    );

    // 10. Wire protocol server (native client protocol).
    let wire_addr = format!("0.0.0.0:{}", cfg.network.grpc_port);
    let wire_listener = tokio::net::TcpListener::bind(&wire_addr)
        .await
        .unwrap_or_else(|e| {
            tracing::error!(%wire_addr, error = %e, "failed to bind wire port");
            std::process::exit(1);
        });
    tracing::info!(%wire_addr, "wire protocol listening");
    let wire_server = wire::WireServer::new(
        mgr.clone(),
        pm_store.clone(),
        cfg.limits.max_connections,
        std::time::Duration::from_secs(cfg.limits.request_timeout_secs),
        std::time::Duration::from_secs(cfg.limits.idle_timeout_secs),
    );

    // 10b. NFS v4.2 server (processor finalized after optional Raft init below).
    //
    // NFS file content goes through `pm_store` (the same
    // `PosixPmStore` instance that backs NBD volumes). One sparse file
    // per inode, pwrite/pread direct, no chunk indirection — drops the
    // O(filesize) read-modify-write that turned every NFS WRITE into a
    // re-hash of the whole file.
    let nfs_vfs = Arc::new(nvstor_nfs::vfs::Vfs::with_metadata_and_pm(
        mgr.clone(),
        Some(meta_store.clone()),
        Some(pm_store.clone()),
        "nfs/",
    ));
    let nfs_config = nvstor_nfs::server::NfsServerConfig {
        max_connections: cfg.limits.max_connections,
        exports: vec![nvstor_nfs::server::NfsExport {
            allowed_clients: cfg.nfs.allow_clients.clone(),
            ..Default::default()
        }],
        ..Default::default()
    };
    if cfg.nfs.allow_clients.is_empty() {
        tracing::warn!("NFS export admits loopback clients only: list the clients that may mount it in [nfs] allow_clients \
                        or NVSTOR_NFS_ALLOW_CLIENTS");
    } else if cfg.nfs.allow_clients.iter().any(|p| p == "*") {
        tracing::warn!(
            source = cfg.nfs.source(),
            "NFS export admits every client (\"*\"); NFS has no other authentication"
        );
    } else {
        tracing::info!(source = cfg.nfs.source(), clients = ?cfg.nfs.allow_clients,
                       "NFS export client allow-list (loopback only if listed)");
    }
    let nfs_addr = format!("0.0.0.0:{}", cfg.network.nfs_port);
    let nfs_rdma_listener = nvstor_nfs::server::bind_rdma_listener(cfg.network.nfs_rdma_port)
        .unwrap_or_else(|error| {
            tracing::error!(%error, "explicitly configured NFS/RDMA listener failed");
            std::process::exit(1);
        });
    let nfs_listener = match tokio::net::TcpListener::bind(&nfs_addr).await {
        Ok(l) => {
            tracing::info!(%nfs_addr, "NFS v4.2 server listening");
            Some(l)
        }
        Err(e) => {
            tracing::warn!(%nfs_addr, error = %e, "NFS port unavailable (non-fatal)");
            None
        }
    };

    // (Raft consensus is initialized earlier — before the S3 AppState —
    // so the S3 gateway can route metadata mutations through it. See
    // the `raft_node` binding above section 8.)

    // Re-create NFS processor with Raft node (if available).
    let nfs_processor = if let Some(ref raft) = raft_node {
        Arc::new(nvstor_nfs::nfs4::CompoundProcessor::with_raft(
            nfs_vfs.clone(),
            raft.clone(),
            meta_store.clone(),
        ))
    } else {
        Arc::new(nvstor_nfs::nfs4::CompoundProcessor::with_metadata(
            nfs_vfs.clone(),
            meta_store.clone(),
        ))
    };
    let nfs_metrics = Arc::new(nvstor_nfs::metrics::NfsMetrics::new());
    let nfs_server = nvstor_nfs::server::NfsServer::with_metrics(
        nfs_processor.clone(),
        nfs_config,
        nfs_metrics.clone(),
    );

    // Start Raft RPC listener if enabled.
    let raft_listener = if raft_node.is_some() {
        let raft_addr = format!("0.0.0.0:{}", cfg.network.raft_port);
        let listener = tokio::net::TcpListener::bind(&raft_addr)
            .await
            .unwrap_or_else(|e| {
                tracing::error!(%raft_addr, error = %e, "failed to bind raft port");
                std::process::exit(1);
            });
        tracing::info!(%raft_addr, "raft RPC listening");
        Some(listener)
    } else {
        None
    };

    // Create health state now that we have distributed_mgr.
    let health_state = Arc::new(health::HealthState {
        mgr: mgr.clone(),
        metrics: server_metrics.clone(),
        ready: ready.clone(),
        start_time,
        node_name: cfg.node.name.clone(),
        cluster_name: cfg.cluster.name.clone(),
        distributed: distributed_mgr.clone(),
        raft_enabled: raft_node.is_some(),
    });

    // 12. Mark ready.
    ready.store(true, std::sync::atomic::Ordering::Relaxed);
    server_metrics.node_ready.set(1);
    tracing::info!("nvstor-server ready");

    // 13. GC + repair tasks.
    let gc_mgr = mgr.clone();
    let gc_interval = cfg.storage.gc_interval_secs;
    let gc_enabled = cfg.storage.gc_enabled;
    let repair_dm = distributed_mgr.clone();

    // PmStore + NFS COMPOUND observability: poll the per-subsystem
    // atomic stats every 5 s and copy them into the prometheus
    // gauges. Counters in hot-path code stay lock-free; /metrics
    // scrapes get a recent (≤5s stale) snapshot rather than
    // blocking the scrape on a walk through the volume / connection
    // maps.
    let refresh_metrics = server_metrics.clone();
    let pm_stats = pm_store.stats();
    let nfs_stats = nfs_metrics.clone();
    tokio::spawn(async move {
        loop {
            tokio::time::sleep(std::time::Duration::from_secs(5)).await;
            refresh_metrics.update_pm_stats(&pm_stats);
            refresh_metrics.update_nfs_stats(&nfs_stats);
        }
    });

    // SIGTERM/SIGINT stop admission first. Keep Raft alive while HTTP requests drain.
    let (shutdown_tx, shutdown_rx) = tokio::sync::watch::channel(false);
    let (failure_tx, mut failure_rx) = tokio::sync::mpsc::unbounded_channel::<&str>();
    let tls_handle = axum_server::Handle::new();
    let http_stop = shutdown_rx.clone();
    let health_stop = shutdown_rx.clone();
    let servers = async {
        tokio::join!(
            async {
                let _ = axum::serve(health_listener, health::router(health_state))
                    .with_graceful_shutdown(wait_for_shutdown(health_stop))
                    .await;
                if !*shutdown_rx.borrow() {
                    let _ = failure_tx.send("health");
                }
            },
            async {
                if let Some(tls_cfg) = s3_tls_config {
                    match s3_listener.into_std() {
                        Ok(listener) => {
                            let _ = axum_server::from_tcp_rustls(listener, tls_cfg)
                                .handle(tls_handle.clone())
                                .serve(s3_router.into_make_service())
                                .await;
                        }
                        Err(error) => tracing::error!(%error, "S3 TLS listener failed"),
                    }
                } else {
                    let _ = axum::serve(s3_listener, s3_router)
                        .with_graceful_shutdown(wait_for_shutdown(http_stop))
                        .await;
                }
                if !*shutdown_rx.borrow() {
                    let _ = failure_tx.send("S3");
                }
            },
            async {
                if let Err(error) = nbd_server.run_until_shutdown(shutdown_rx.clone()).await {
                    tracing::error!(%error, "NBD server stopped");
                }
                if !*shutdown_rx.borrow() {
                    let _ = failure_tx.send("NBD");
                }
            },
            async {
                if let Some(listener) = nfs_listener {
                    let _ = nfs_server
                        .run_until_shutdown(listener, shutdown_rx.clone())
                        .await;
                    if !*shutdown_rx.borrow() {
                        let _ = failure_tx.send("NFS");
                    }
                }
            },
            wire_server.run_until_shutdown(wire_listener, shutdown_rx.clone()),
            async {
                if let Some(listener) = nfs_rdma_listener {
                    if let Err(error) = nfs_server
                        .run_rdma_until_shutdown(listener, shutdown_rx.clone())
                        .await
                    {
                        tracing::error!(%error, "NFS/RDMA server stopped");
                    }
                    if !*shutdown_rx.borrow() {
                        let _ = failure_tx.send("NFS/RDMA");
                    }
                }
            },
        );
    };
    tokio::pin!(servers);
    let background = async {
        tokio::select! {
        _ = async {
            if let (Some(node), Some(listener)) = (&raft_node, raft_listener) {
                let node_run = node.clone();
                let node_serve = node.clone();
                tokio::select! {
                    _ = node_run.run() => {},
                    _ = nvstor_raft::server::run(listener, node_serve) => {},
                }
            } else {
                // No Raft — just wait forever.
                std::future::pending::<()>().await;
            }
        } => {},
        // NFS lease expiration background task.
        _ = async {
            let nfs_proc = nfs_processor.clone();
            loop {
                tokio::time::sleep(std::time::Duration::from_secs(30)).await;
                if !*shutdown_rx.borrow() { nfs_proc.expire_leases(); }
            }
        } => {},
        _ = async {
            loop {
                tokio::time::sleep(std::time::Duration::from_secs(gc_interval)).await;
                if gc_enabled && !*shutdown_rx.borrow() {
                    // Update storage metrics.
                    if let Ok(stats) = gc_mgr.stats() {
                        server_metrics.update_storage_stats(&stats);
                    }
                    match gc_mgr.gc_sweep() {
                        Ok(report) => {
                            server_metrics.record_gc(&report);
                            if report.chunks_deleted > 0 {
                                tracing::info!(
                                    chunks_deleted = report.chunks_deleted,
                                    bytes_reclaimed = report.bytes_reclaimed,
                                    errors = report.errors,
                                    "gc sweep complete"
                                );
                            }
                        }
                        Err(e) => {
                            tracing::error!(error = %e, "gc sweep failed");
                        }
                    }

                    // Repair pass: re-replicate missing objects to remote nodes.
                    if let Some(ref dm) = repair_dm {
                        let (scanned, repaired, errors) = dm.repair_pass();
                        if repaired > 0 || errors > 0 {
                            tracing::info!(scanned, repaired, errors, "repair pass complete");
                        }
                    }
                }
            }
        } => {},
        }
    };
    tokio::pin!(background);
    let mut servers_finished = false;
    let mut background_finished = false;
    tokio::select! {
        _ = shutdown_signal() => tracing::info!("shutdown signal received"),
        service = failure_rx.recv() => tracing::error!(?service, "protocol service exited; shutting down"),
        _ = &mut servers => { servers_finished = true; tracing::warn!("protocol servers stopped"); },
        _ = &mut background => { background_finished = true; tracing::warn!("background service stopped"); },
    }
    ready.store(false, std::sync::atomic::Ordering::Release);
    server_metrics.node_ready.set(0);
    shutdown_tx.send_replace(true);
    tls_handle.graceful_shutdown(Some(std::time::Duration::from_secs(20)));
    let drain = async {
        let drain_transports = async {
            loop {
                let mut drained = true;
                for stop in &transport_shutdown {
                    drained &= stop();
                }
                if drained {
                    break;
                }
                tokio::time::sleep(std::time::Duration::from_millis(10)).await;
            }
        };
        tokio::join!(
            async {
                if !servers_finished {
                    (&mut servers).await;
                }
            },
            drain_transports
        );
    };
    tokio::pin!(drain);
    tokio::select! {
        result = tokio::time::timeout(std::time::Duration::from_secs(20), &mut drain) => {
            if result.is_err() { tracing::error!("shutdown drain deadline exceeded; outstanding requests may need retry"); }
        },
        _ = &mut background, if !background_finished => {
            tracing::error!("background services stopped during drain");
            if tokio::time::timeout(std::time::Duration::from_secs(20), &mut drain).await.is_err() {
                tracing::error!("shutdown drain deadline exceeded");
            }
        },
    }
    if let Err(e) = nfs_processor.persist_all_state() {
        tracing::error!(error = %e, "NFS state flush failed");
    }
    for store in durable_stores {
        if let Err(e) = store.flush_all() {
            tracing::error!(error = %e, "PM shutdown flush failed");
        }
    }
    if let Err(e) = mgr.flush() {
        tracing::error!(error = %e, "chunk shutdown flush failed");
    }
    if let Err(e) = meta_store.flush() {
        tracing::error!(error = %e, "metadata shutdown flush failed");
    }

    tracing::info!("nvstor-server stopped");
}

async fn wait_for_shutdown(mut receiver: tokio::sync::watch::Receiver<bool>) {
    let _ = receiver.wait_for(|stopping| *stopping).await;
}

async fn shutdown_signal() {
    #[cfg(unix)]
    {
        let mut terminate = signal::unix::signal(signal::unix::SignalKind::terminate())
            .expect("install SIGTERM handler");
        tokio::select! { _ = signal::ctrl_c() => {}, _ = terminate.recv() => {} }
    }
    #[cfg(not(unix))]
    {
        let _ = signal::ctrl_c().await;
    }
}

/// Map the `[storage] fast_path_sync` config string to a SyncMode for
/// the PmObjectStore-backed s3-rdma/s3-tcp checkpoint path. Unknown
/// values fall back to the safe default (Data = fdatasync-before-ACK).
fn fast_path_sync_mode(s: &str) -> nvstor_storage::SyncMode {
    match s.trim().to_ascii_lowercase().as_str() {
        "none" => nvstor_storage::SyncMode::None,
        "full" => nvstor_storage::SyncMode::Full,
        "data" => nvstor_storage::SyncMode::Data,
        other => {
            tracing::warn!(value = %other,
                "unknown fast_path_sync; defaulting to 'data' (fdatasync before ACK)");
            nvstor_storage::SyncMode::Data
        }
    }
}

/// A comma-separated client list (NVSTOR_NFS_ALLOW_CLIENTS); `None` when
/// it names nothing, so a blank variable (an unset template value) leaves
/// the config file's list in force instead of locking every client out.
fn parse_client_list(v: &str) -> Option<Vec<String>> {
    let clients: Vec<String> = v
        .split(',')
        .map(str::trim)
        .filter(|p| !p.is_empty())
        .map(String::from)
        .collect();
    (!clients.is_empty()).then_some(clients)
}

/// Every NFS client pattern must parse: a malformed one would match no
/// client, so the export would silently refuse the hosts it was meant for.
fn check_nfs_clients(patterns: &[String]) -> Result<(), String> {
    patterns
        .iter()
        .try_for_each(|p| nvstor_nfs::server::validate_client_pattern(p))
}

fn load_config(cli: &Cli) -> ServerConfig {
    if let Some(ref path) = cli.config {
        match std::fs::read_to_string(path) {
            Ok(contents) => match toml::from_str(&contents) {
                Ok(cfg) => return cfg,
                Err(e) => {
                    eprintln!("error: failed to parse config {path}: {e}");
                    std::process::exit(1);
                }
            },
            Err(e) => {
                eprintln!("error: failed to read config {path}: {e}");
                std::process::exit(1);
            }
        }
    }
    ServerConfig::default()
}

fn apply_env_overrides(cfg: &mut ServerConfig) {
    if let Ok(name) = std::env::var("POD_NAME") {
        cfg.node.name = name;
    }
    if let Ok(address) = std::env::var("NVSTOR_ADVERTISE_ADDR") {
        if !address.trim().is_empty() {
            cfg.cluster.advertise_addr = Some(address);
        }
    }
    if let Ok(v) = std::env::var("NVSTOR_DATA_PATH") {
        cfg.storage.data_path = v;
    }
    if let Ok(v) = std::env::var("NVSTOR_NODE_ID") {
        if let Ok(id) = v.parse() {
            cfg.node.id = id;
        }
    }
    if let Ok(v) = std::env::var("NVSTOR_GRPC_PORT") {
        if let Ok(p) = v.parse() {
            cfg.network.grpc_port = p;
        }
    }
    if let Ok(v) = std::env::var("NVSTOR_HTTP_PORT") {
        if let Ok(p) = v.parse() {
            cfg.network.http_port = p;
        }
    }
    if let Ok(v) = std::env::var("NVSTOR_S3_PORT") {
        if let Ok(p) = v.parse() {
            cfg.network.s3_port = p;
        }
    }
    if let Ok(v) = std::env::var("NVSTOR_NBD_PORT") {
        if let Ok(p) = v.parse() {
            cfg.network.nbd_port = p;
        }
    }
    if let Ok(v) = std::env::var("NVSTOR_NFS_PORT") {
        if let Ok(p) = v.parse() {
            cfg.network.nfs_port = p;
        }
    }
    if let Ok(v) = std::env::var("NVSTOR_NFS_RDMA_PORT") {
        cfg.network.nfs_rdma_port = Some(v.parse().unwrap_or_else(|error| {
            tracing::error!(%error, "NVSTOR_NFS_RDMA_PORT must be a valid port number");
            std::process::exit(1);
        }));
    }
    if let Some(clients) = std::env::var("NVSTOR_NFS_ALLOW_CLIENTS")
        .ok()
        .as_deref()
        .and_then(parse_client_list)
    {
        cfg.nfs.allow_clients = clients;
        cfg.nfs.allow_clients_source = Some("NVSTOR_NFS_ALLOW_CLIENTS");
    }
    if let Ok(v) = std::env::var("NVSTOR_ACCESS_KEY") {
        cfg.auth.access_key = Some(v);
    }
    if let Ok(v) = std::env::var("NVSTOR_SECRET_KEY") {
        cfg.auth.secret_key = Some(v);
    }
    if let Ok(v) = std::env::var("NVSTOR_KMS_ROOT_PASSPHRASE") {
        cfg.kms.root_key_passphrase = Some(v);
    }
}

/// Resolve this node's cluster identity.
///
/// Every node must agree on which ID names which machine; otherwise placement
/// and Raft attribute data and votes to the wrong peer. Resolution order:
///
/// 1. Explicit `[node] id` — authoritative. Pair with `"<id>@<addr>"` seed
///    entries so peers write the same ID for this node.
/// 2. `[cluster] advertise_addr` — derive via the SAME address→ID hash peers
///    use for a bare `"<addr>"` seed, so both sides agree without config.
///    The string must match what peers list (FQDN vs short name matters).
/// 3. StatefulSet ordinal from the node name (`nvstor-server-2` → 3): stable
///    and unique across a k8s StatefulSet.
/// 4. Fallback 1 — only safe single-node. Every node lands on 1, so in a
///    cluster this collides; the caller warns.
/// Build the request verifier for the s3-rdma / s3-tcp transports.
///
/// This is the bridge between the transport layer — which knows how to
/// check an HMAC but nothing about identity — and IAM, which owns
/// credentials and policy. Keeping it here means `nvstor-rdma` never
/// depends on `nvstor-iam`.
///
/// Order matters: resolve the key, verify the signature, THEN authorize.
/// Authorizing before verifying would let an unauthenticated caller probe
/// which keys a principal is allowed to touch.
/// Open one RDMA transport ("rail") per HCA with an ACTIVE port, each
/// with its own memory pool and listener. Rail 0 listens on `rdma_port`
/// and its HCA carries cluster replication (`open_replication_transport`)
/// and RPC (`rdma_port + 1`);
/// rail k ≥ 1 listens on `rdma_rail_port_base + k - 1`. The configured
/// pool slots are a per-node total, split evenly across rails. Empty
/// (logged) when there is no usable RDMA hardware; callers fall back to
/// TCP.
///
/// Port 1 of each HCA must be ACTIVE: boxes with two NICs (e.g. mlx5_0 +
/// mlx5_1) sometimes have only one cabled, and a QP on the unlinked one
/// fails RTR with EINVAL.
///
/// Every rail authenticates connectors with `verifier` in the bootstrap.
fn open_rdma_rails(
    cfg: &config::ServerConfig,
    verifier: nvstor_rdma::bootstrap::HandshakeVerifier,
) -> Vec<(Arc<nvstor_rdma::transport::RdmaTransport>, u16)> {
    if !nvstor_rdma::transport::RdmaTransport::is_available() {
        tracing::info!("no RDMA hardware detected, object and cluster traffic use TCP");
        return Vec::new();
    }
    let devices = nvstor_rdma::device::RdmaDevice::open_all();
    if devices.is_empty() {
        tracing::info!("RDMA not available: no device with an ACTIVE port");
        return Vec::new();
    }
    let pool_slot_bytes = cfg.network.rdma_pool_slot_mb * 1024 * 1024;
    let slots_per_rail = cfg.network.rdma_pool_slots.div_ceil(devices.len()).max(1);
    let rail_base = cfg.network.rdma_rail_port_base();
    tracing::info!(
        rails = devices.len(),
        slot_mb = cfg.network.rdma_pool_slot_mb,
        slots_per_rail,
        total_mb = cfg.network.rdma_pool_slot_mb * slots_per_rail * devices.len(),
        "RDMA memory pool configuration (rails; cluster mode adds the replication pool)",
    );
    let mut rails = Vec::with_capacity(devices.len());
    for dev in devices {
        let dev = Arc::new(dev);
        let name = dev.name.clone();
        // Rail pools are never touched by peers: GET pushes out of them
        // and PUT pulls into them, both initiated here. They are LOCAL,
        // so no rkey of theirs is valid on any QP. Replication pushes,
        // which a replica pulls with RDMA READ, use a separate pool
        // (`open_replication_transport`).
        let access = nvstor_rdma::device::Access::LOCAL;
        let pool = match nvstor_rdma::pool::MemoryPool::with_access(
            &dev,
            pool_slot_bytes,
            slots_per_rail,
            access,
        ) {
            Ok(pool) => pool,
            Err(e) => {
                tracing::warn!(device = %name, error = %e, "RDMA pool failed; rail skipped");
                continue;
            }
        };
        let transport = Arc::new(nvstor_rdma::transport::RdmaTransport::new(
            dev,
            Arc::new(pool),
        ));
        // Authenticate every connector before it gets a QP.
        transport.set_verifier(Arc::clone(&verifier));
        let port = if rails.is_empty() {
            cfg.network.rdma_port
        } else {
            match u16::try_from(usize::from(rail_base) + rails.len() - 1) {
                Ok(p) => p,
                Err(_) => {
                    tracing::warn!(device = %name, base = rail_base, "rail port out of range; rail skipped");
                    continue;
                }
            }
        };
        match transport.listen_on(cfg.network.rdma_listen_addr(), port) {
            Ok(_) => {
                tracing::info!(device = %name, rail = rails.len(), port, "RDMA rail listening")
            }
            // Rail 0 is kept even when its listener fails: rail numbering
            // and the rail ports clients were configured with stay as
            // they are, and replication dials out over its HCA.
            Err(e) if rails.is_empty() => {
                tracing::warn!(device = %name, port, error = %e, "RDMA listen failed")
            }
            Err(e) => {
                tracing::warn!(device = %name, port, error = %e, "RDMA rail listen failed; rail skipped");
                transport.shutdown();
                continue;
            }
        }
        rails.push((transport, port));
    }
    rails
}

/// The transport cluster replication dials out over, on rail 0's HCA. A
/// replica pulls a push with RDMA READ through the QP this node dialed,
/// so the pushed slot must be registered REMOTE_READ, and that QP grants
/// remote read on the whole MR. Its pool is therefore its own: it only
/// ever holds replication pushes, which every cluster peer is entitled to
/// store anyway, and never a client's GET or PUT slot. The transport
/// never listens; accepted connections (and S3 clients) stay on the rail
/// transports, whose pools are LOCAL. `None` (logged) if the pool cannot
/// be registered; replication then uses the TCP RPC fallback.
fn open_replication_transport(
    cfg: &config::ServerConfig,
    rail0: &Arc<nvstor_rdma::transport::RdmaTransport>,
) -> Option<Arc<nvstor_rdma::transport::RdmaTransport>> {
    let slot_bytes = cfg.network.rdma_pool_slot_mb * 1024 * 1024;
    let dev = Arc::clone(rail0.device());
    match nvstor_rdma::pool::MemoryPool::with_access(
        &dev,
        slot_bytes,
        replication_push_slots(cfg.network.rdma_pool_slots),
        nvstor_rdma::device::Access::REMOTE_READ,
    ) {
        Ok(pool) => Some(Arc::new(nvstor_rdma::transport::RdmaTransport::new(
            dev,
            Arc::new(pool),
        ))),
        Err(e) => {
            tracing::warn!(device = %dev.name, error = %e, "RDMA replication pool failed; replication uses TCP");
            None
        }
    }
}

/// Replication pushes in flight at once per node (one slot each): a
/// quarter of the configured rail slots, at most 64 (1 GiB of 16 MiB
/// slots). A push waits up to 5 s for a slot, so a burst beyond this
/// queues rather than fails.
fn replication_push_slots(rail_slots: usize) -> usize {
    (rail_slots / 4).clamp(1, 64)
}

/// Ranged, generation-pinned read callback for both object transports:
/// bytes land straight in the transport's buffer (RDMA pool slot or TCP
/// staging buffer), never in an intermediate whole-object Vec.
fn object_read_fn(store: Arc<nvstor_storage::PmObjectStore>) -> nvstor_rdma::object::ReadFn {
    Arc::new(move |key, offset, buf| {
        let r = store.read_at(key, offset, buf)?;
        Ok(nvstor_rdma::object::ObjectRead {
            len: r.len,
            size: r.size,
            generation: r.generation.0,
        })
    })
}

/// Decide who may open an RDMA connection (the bootstrap handshake, before
/// any QP exists). The `[auth]` config credential is the cluster
/// credential: its holders are cluster peers and may replicate. An enabled
/// IAM user is an object client. Anyone else is refused.
fn make_handshake_verifier(
    iam: Arc<nvstor_iam::store::IamStore>,
    config_cred: Option<(String, String)>,
) -> nvstor_rdma::bootstrap::HandshakeVerifier {
    use nvstor_rdma::bootstrap::PeerRole;
    Arc::new(move |auth: &nvstor_rdma::bootstrap::HandshakeAuth| {
        if let Some((ref ak, ref sk)) = config_cred {
            if auth.access_key == ak.as_str() {
                return if auth.verify(sk) {
                    PeerRole::Cluster
                } else {
                    PeerRole::Denied
                };
            }
        }
        match iam.get_user(auth.access_key) {
            Ok(Some(user)) if user.enabled && auth.verify(&user.secret_key) => PeerRole::Client,
            _ => PeerRole::Denied,
        }
    })
}

fn make_transport_auth(
    iam: Arc<nvstor_iam::store::IamStore>,
    config_cred: Option<(String, String)>,
) -> nvstor_rdma::auth::AuthFn {
    use nvstor_rdma::auth::AuthError;

    Arc::new(move |ctx: &nvstor_rdma::auth::AuthContext| {
        // Bootstrap path: the `[auth]` config credential works before any
        // IAM user exists. Without this, enforce-by-default would make a
        // freshly-installed cluster unusable — nothing could authenticate,
        // including the tooling needed to create the first IAM user. Same
        // credential the S3 HTTP gateway accepts, so one config entry
        // unlocks every protocol.
        if let Some((ref ak, ref sk)) = config_cred {
            if ctx.header.access_key == *ak {
                return if nvstor_rdma::auth::verify_signature(
                    sk,
                    ctx.header.timestamp,
                    ctx.header.nonce,
                    ctx.body,
                    &ctx.header.signature,
                ) {
                    // The config credential is the operator identity: it
                    // predates IAM and is not policy-scoped.
                    Ok("config-credential".to_string())
                } else {
                    Err(AuthError::BadSignature)
                };
            }
        }

        let user = iam
            .get_user(&ctx.header.access_key)
            .map_err(|_| AuthError::UnknownKey)?
            .ok_or(AuthError::UnknownKey)?;
        if !user.enabled {
            return Err(AuthError::UnknownKey);
        }

        if !nvstor_rdma::auth::verify_signature(
            &user.secret_key,
            ctx.header.timestamp,
            ctx.header.nonce,
            ctx.body,
            &ctx.header.signature,
        ) {
            return Err(AuthError::BadSignature);
        }

        // Authenticated. Now authorize the specific action + object.
        //
        // A user with no policies attached is allowed, matching the S3 HTTP
        // gateway's behaviour — the two paths must agree, or the same
        // credential would work over HTTP and fail over RDMA.
        let policy_docs = iam.resolve_policies(&user);
        if !policy_docs.is_empty() {
            // Transport keys are "bucket/key"; split so policy can scope to
            // a bucket or an individual object exactly as it does on HTTP.
            let (bucket, object) = match ctx.key.split_once('/') {
                Some((b, k)) => (b, Some(k)),
                None if ctx.key.is_empty() => ("*", None),
                None => (ctx.key, None),
            };
            let resource = if bucket == "*" {
                "*".to_string()
            } else {
                nvstor_iam::policy::s3_resource_arn(bucket, object)
            };
            let decision = nvstor_iam::policy::evaluate(
                &policy_docs,
                &nvstor_iam::types::AccessRequest {
                    action: ctx.action.to_string(),
                    resource,
                },
            );
            if decision != nvstor_iam::types::AccessDecision::Allow {
                return Err(AuthError::Denied);
            }
        }

        Ok(user.user_id)
    })
}

fn resolve_node_id(node_name: &str, configured_id: u64, advertise_addr: Option<&str>) -> u64 {
    if configured_id > 0 {
        return configured_id;
    }

    if let Some(addr) = advertise_addr.map(str::trim).filter(|a| !a.is_empty()) {
        return nvstor_placement::node::node_id_for_address(addr);
    }

    node_name
        .rsplit('-')
        .next()
        .and_then(|suffix| suffix.parse::<u64>().ok())
        .map(|ordinal| ordinal + 1)
        .unwrap_or(1)
}

fn init_logging(cfg: &config::LoggingConfig) {
    let env_filter = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new(&cfg.level));

    if cfg.json {
        tracing_subscriber::fmt()
            .json()
            .with_env_filter(env_filter)
            .init();
    } else {
        tracing_subscriber::fmt().with_env_filter(env_filter).init();
    }
}

#[cfg(test)]
mod tests {
    use super::{
        check_nfs_clients, fast_path_sync_mode, parse_client_list, replication_push_slots,
        resolve_node_id,
    };
    use nvstor_storage::SyncMode;

    #[test]
    fn nfs_client_patterns_are_checked_at_startup() {
        let ok: Vec<String> = ["10.0.0.0/8", "fd00::1", "*"]
            .iter()
            .map(|p| p.to_string())
            .collect();
        assert!(check_nfs_clients(&ok).is_ok());
        assert!(check_nfs_clients(&[]).is_ok(), "empty: loopback only");
        for bad in ["10.0.0.0/33", "nonsense", "fd00::/129"] {
            assert!(check_nfs_clients(&[bad.to_string()]).is_err(), "{bad}");
        }
    }

    #[test]
    fn the_nfs_client_variable_is_a_trimmed_comma_list_and_blank_is_unset() {
        assert_eq!(
            parse_client_list(" 10.0.0.0/8 , fd00::1,"),
            Some(vec!["10.0.0.0/8".into(), "fd00::1".into()])
        );
        assert_eq!(parse_client_list("*"), Some(vec!["*".into()]));
        for blank in ["", " ", ",", " , ,"] {
            assert_eq!(
                parse_client_list(blank),
                None,
                "{blank:?} leaves the file's list"
            );
        }
    }

    #[test]
    fn replication_pool_is_a_quarter_of_the_rail_slots_capped() {
        assert_eq!(replication_push_slots(256), 64);
        assert_eq!(replication_push_slots(4096), 64);
        assert_eq!(replication_push_slots(16), 4);
        assert_eq!(replication_push_slots(1), 1);
    }

    // No client-facing pool may be readable by a peer. Every rail
    // pool is LOCAL; only the dial-only replication pool is REMOTE_READ,
    // and it is a different pool from rail 0's. Needs an RDMA device.
    #[test]
    #[ignore = "needs RDMA hardware"]
    fn only_the_replication_pool_is_remotely_readable() {
        let mut cfg = super::config::ServerConfig::default();
        cfg.network.rdma_port = 39_877;
        cfg.network.rdma_rail_port_base = Some(39_893);
        let verifier: nvstor_rdma::bootstrap::HandshakeVerifier =
            std::sync::Arc::new(|_| nvstor_rdma::bootstrap::PeerRole::Denied);
        let rails = super::open_rdma_rails(&cfg, verifier);
        assert!(!rails.is_empty(), "no RDMA rail opened");
        for (t, _) in &rails {
            assert_eq!(
                t.pool().access().remote_bits(),
                0,
                "a rail pool is remotely accessible"
            );
        }
        let repl =
            super::open_replication_transport(&cfg, &rails[0].0).expect("replication transport");
        assert!(repl.pool().access().remote_read());
        assert!(!std::ptr::eq(repl.pool(), rails[0].0.pool()));
        for (t, _) in &rails {
            t.shutdown();
        }
    }

    #[test]
    fn fast_path_sync_mode_maps_values() {
        assert_eq!(fast_path_sync_mode("none"), SyncMode::None);
        assert_eq!(fast_path_sync_mode("data"), SyncMode::Data);
        assert_eq!(fast_path_sync_mode("full"), SyncMode::Full);
        // Case/space-insensitive.
        assert_eq!(fast_path_sync_mode("  FULL "), SyncMode::Full);
        // Unknown → safe default (Data), never None.
        assert_eq!(fast_path_sync_mode("garbage"), SyncMode::Data);
    }

    #[test]
    fn fast_path_default_is_durable() {
        // The shipped default must be durable-before-ACK, not None.
        assert_eq!(
            fast_path_sync_mode(&super::config::StorageConfig::default().fast_path_sync),
            SyncMode::Data,
        );
    }

    #[test]
    fn statefulset_addresses_have_identical_local_and_peer_ids() {
        let seeds: Vec<_> = (0..3)
            .map(|index| {
                format!(
            "nvstor-server-{index}.nvstor-server-headless.nvstor-storage.svc.cluster.local:9102"
        )
            })
            .collect();
        let mut identities = std::collections::HashSet::new();
        for (index, seed) in seeds.iter().enumerate() {
            let advertise = seed.replace(":9102", ":9100");
            let local = resolve_node_id(&format!("nvstor-server-{index}"), 0, Some(&advertise));
            assert_eq!(local, nvstor_placement::node::parse_seed(seed).0);
            assert!(identities.insert(local));
            assert_eq!(
                seeds
                    .iter()
                    .filter(|seed| nvstor_placement::node::parse_seed(seed).0 != local)
                    .count(),
                2
            );
        }
    }

    #[test]
    fn resolve_node_id_prefers_explicit_value() {
        assert_eq!(resolve_node_id("nvstor-server-0", 7, None), 7);
        // Explicit id outranks advertise_addr.
        assert_eq!(
            resolve_node_id("nvstor-server-0", 7, Some("10.0.0.1:9002")),
            7
        );
    }

    #[test]
    fn resolve_node_id_uses_statefulset_ordinal() {
        assert_eq!(resolve_node_id("nvstor-server-0", 0, None), 1);
        assert_eq!(resolve_node_id("nvstor-server-2", 0, None), 3);
    }

    #[test]
    fn resolve_node_id_falls_back_to_one() {
        assert_eq!(resolve_node_id("nvstor-dev", 0, None), 1);
    }

    // The shipped bare-metal configs must parse into the REAL schema and
    // carry explicit, mutually-consistent identity. They previously set
    // `[cluster] node_id`, which is not a field in this schema — so it was
    // silently ignored and all three nodes fell through to the inferred-id
    // path and collided.
    #[test]
    fn shipped_bare_metal_configs_have_consistent_identity() {
        let mut seen: Vec<(u64, String)> = Vec::new();
        for (path, expect_id) in [
            ("../../deploy/bare-metal/node1.toml", 1u64),
            ("../../deploy/bare-metal/node2.toml", 2),
            ("../../deploy/bare-metal/node3.toml", 3),
        ] {
            let text = std::fs::read_to_string(path).unwrap_or_else(|e| panic!("read {path}: {e}"));
            let cfg: super::config::ServerConfig =
                toml::from_str(&text).unwrap_or_else(|e| panic!("parse {path}: {e}"));

            assert_eq!(cfg.node.id, expect_id, "{path}: [node] id must be explicit");
            // Explicit id set → resolve_node_id returns it unchanged.
            assert_eq!(
                resolve_node_id(
                    &cfg.node.name,
                    cfg.node.id,
                    cfg.cluster.advertise_addr.as_deref()
                ),
                expect_id
            );

            for entry in &cfg.cluster.seeds {
                let (id, addr) = nvstor_placement::node::parse_seed(entry);
                assert!(
                    id < 1000,
                    "{path}: seed {entry} should carry an explicit id"
                );
                assert_ne!(id, cfg.node.id, "{path}: seed {entry} must not be self");
                seen.push((id, addr));
            }
        }

        // Every node's view of a given peer address must agree on its id.
        for (id_a, addr_a) in &seen {
            for (id_b, addr_b) in &seen {
                if addr_a == addr_b {
                    assert_eq!(id_a, id_b, "address {addr_a} has conflicting ids");
                } else {
                    assert_ne!(id_a, id_b, "distinct addresses share id {id_a}");
                }
            }
        }
    }

    // With advertise_addr set, this node derives the SAME id a peer
    // derives from the matching bare seed entry — the two sides agree on one
    // identity without any explicit id config.
    #[test]
    fn resolve_node_id_matches_peer_derivation_via_advertise_addr() {
        let addr = "192.0.2.10:9002";
        let mine = resolve_node_id("nvstor-dev", 0, Some(addr));
        let (peer_view, _) = nvstor_placement::node::parse_seed(addr);
        assert_eq!(
            mine, peer_view,
            "self and peer must agree on this node's id"
        );

        // Port differences don't change identity (same machine).
        assert_eq!(
            resolve_node_id("nvstor-dev", 0, Some("192.0.2.10:9000")),
            mine
        );

        // Two different machines get two different ids (no fallback collision).
        assert_ne!(
            resolve_node_id("nvstor-dev", 0, Some("192.0.2.20:9002")),
            mine
        );
    }
}
