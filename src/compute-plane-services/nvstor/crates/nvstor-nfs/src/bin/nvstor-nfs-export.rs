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

//! nvstor-nfs-export — serve any directory over NFS v4.2.
//!
//! Usage:
//!   nvstor-nfs-export --allow-client 10.0.0.0/8 /var/lib/containers/committed/sha256:abc
//!   nvstor-nfs-export --allow-client fd00:1::/32 --fh-key /var/lib/nvstor/fh.key \
//!       --port 2049 --metrics-port 9101 /data/images/layer-1
//!   nvstor-nfs-export --allow-client fd00:1::/32 --no-tcp --rdma-port 20049 /models
//!
//! Client:
//!   mount -t nfs4 server:/  /mnt/layer
//!   curl http://server:9101/metrics
//!
//! This is a standalone NFS server that exports a real directory.
//! No nvstor storage engine needed — pure passthrough.
//!
//! Metrics: scrape `http://<host>:<metrics-port>/metrics` for Prometheus
//! exposition (op counters + per-category latency histograms + gauges).
//! Set `--metrics-port 0` to disable the HTTP endpoint.
//!
//! Access control: the client address list (`--allow-client`) decides who
//! may connect; each request is then checked against the caller's AUTH_SYS
//! uid, gid and supplementary groups with POSIX permission rules, root
//! squashed to `--anon-uid`/`--anon-gid` unless `--no-root-squash`.

use std::path::{Path, PathBuf};
use std::sync::Arc;

fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()),
        )
        .init();

    let args: Vec<String> = std::env::args().collect();
    if args.len() < 2 || args.iter().any(|arg| arg == "--help" || arg == "-h") {
        eprintln!(
            "Usage: {} --allow-client CIDR [--allow-client CIDR ...] [OPTIONS] <directory>",
            args[0]
        );
        eprintln!();
        eprintln!("Serve a directory over NFS v4.2.");
        eprintln!();
        eprintln!("Options:");
        eprintln!(
            "  --port PORT          NFS/TCP listen port (default: 2049, use 12049 for non-root)"
        );
        eprintln!("  --bind ADDR          Address to listen on for NFS/TCP, IPv4 or IPv6 (default: 0.0.0.0).");
        eprintln!(
            "                       Repeatable; an IPv6 address is bound IPv6-only, so give both"
        );
        eprintln!("                       0.0.0.0 and :: for every interface of both families");
        eprintln!(
            "  --no-tcp             Serve NFS/RDMA only: no TCP listener. Startup fails unless an"
        );
        eprintln!(
            "                       RDMA listener comes up, and the process exits non-zero if the"
        );
        eprintln!(
            "                       RDMA listener later fails (with TCP on, TCP keeps serving and"
        );
        eprintln!("                       the RDMA listener is re-created with backoff)");
        eprintln!("  --rdma-port PORT     NFS/RDMA port (default: auto-detect 20049, 0 disables)");
        eprintln!(
            "  --trunk-addr ADDR    An address of this server for the same export, one per rail;"
        );
        eprintln!("                       repeatable. Returned as fs_locations, so a Linux client (5.18+)");
        eprintln!(
            "                       mounting with trunkdiscovery adds one transport per address"
        );
        eprintln!(
            "                       (rails-mount.sh). Without it, fs_locations is not offered"
        );
        eprintln!(
            "  --rdma-pool-mib MIB  Registered zero-copy buffer pool per RDMA device, in MiB"
        );
        eprintln!("                       (default: 256 = 256 concurrent 1 MiB READs/WRITEs)");
        eprintln!(
            "  --metrics-port PORT  Prometheus /metrics HTTP port (default: 9101, 0 disables)"
        );
        eprintln!(
            "  --allow-client CIDR  Client address or prefix allowed to connect, over TCP and RDMA"
        );
        eprintln!(
            "                       (e.g. 10.0.0.0/8, fd00:1::/32). Repeatable; required. Each"
        );
        eprintln!(
            "                       pattern admits only its own address family: open the export"
        );
        eprintln!(
            "                       to everyone with --allow-client 0.0.0.0/0 --allow-client ::/0."
        );
        eprintln!(
            "                       Admitted clients' requests are then checked against their"
        );
        eprintln!(
            "                       AUTH_SYS uid, gid and groups with POSIX permission rules"
        );
        eprintln!(
            "  --no-root-squash     Let uid 0 (and gid 0) act as root: read and write every file"
        );
        eprintln!("                       (default: squash root to the anonymous uid/gid)");
        eprintln!("  --anon-uid UID       Uid that squashed root and AUTH_NONE callers run as (default 65534)");
        eprintln!("  --anon-gid GID       Gid that squashed root and AUTH_NONE callers run as (default 65534)");
        eprintln!(
            "  --read-write         Allow writes (default: read-only). New files, directories and"
        );
        eprintln!("                       symlinks are owned by the caller, which needs CAP_CHOWN when the");
        eprintln!("                       caller is not the server's own uid");
        eprintln!("  --fh-key FILE        Secret that seals NFS filehandles (32 bytes, mode 0600; created if");
        eprintln!("                       absent). Must lie outside the export. Without it a random key is");
        eprintln!("                       used and clients' filehandles go stale when the server restarts");
        eprintln!("  --pnfs-ds HOST       Advertise HOST (an address clients can reach) as a pNFS");
        eprintln!("                       file-layout data server on the NFS port (default: off;");
        eprintln!("                       ignored when NFS/RDMA is enabled)");
        eprintln!();
        eprintln!("Examples:");
        eprintln!("  sudo {} --allow-client 10.0.0.0/8 --fh-key /var/lib/nvstor/fh.key /var/lib/containers/committed/sha256:abc", args[0]);
        eprintln!(
            "  {} --allow-client 127.0.0.1 --port 12049 /tmp/test-export",
            args[0]
        );
        eprintln!(
            "  {} --allow-client fd00:1::/32 --no-tcp --rdma-port 20049 /models   # RDMA only",
            args[0]
        );
        eprintln!("  {} --allow-client 10.0.0.0/8 --bind 10.0.0.5 --bind fd00::5 /data   # TCP on two addresses", args[0]);
        eprintln!();
        eprintln!("Client:");
        eprintln!("  mount -t nfs4 server:/ /mnt/layer");
        eprintln!("  mount -t nfs4 -o port=12049 server:/ /mnt/layer");
        eprintln!("  mount -t nfs -o vers=4.1,proto=rdma,port=20049 server:/ /mnt/layer");
        eprintln!("  curl http://server:9101/metrics");
        std::process::exit(if args.len() < 2 { 1 } else { 0 });
    }

    let mut port = 2049u16;
    let mut rdma_port: Option<u16> = None;
    let mut rdma_pool_bytes = nvstor_nfs::server::DEFAULT_RDMA_POOL_BYTES;
    let mut metrics_port: u16 = 9101;
    let mut read_write = false;
    let mut pnfs_ds: Option<String> = None;
    let mut fh_key_path: Option<PathBuf> = None;
    let mut allowed_clients: Vec<String> = Vec::new();
    let mut root_squash = true;
    let mut anon_uid = nvstor_nfs::nfs4::ANON_ID;
    let mut anon_gid = nvstor_nfs::nfs4::ANON_ID;
    let mut binds: Vec<std::net::IpAddr> = Vec::new();
    let mut trunk_addrs: Vec<String> = Vec::new();
    let mut no_tcp = false;
    let mut dir_path = None;

    let mut i = 1;
    while i < args.len() {
        match args[i].as_str() {
            "--port" => {
                i += 1;
                port = args.get(i).and_then(|s| s.parse().ok()).unwrap_or(2049);
            }
            "--metrics-port" => {
                i += 1;
                metrics_port = args.get(i).and_then(|s| s.parse().ok()).unwrap_or(9101);
            }
            "--rdma-port" => {
                i += 1;
                rdma_port = Some(args.get(i).and_then(|s| s.parse().ok()).unwrap_or_else(|| {
                    eprintln!("Error: --rdma-port requires a port in 0..=65535");
                    std::process::exit(1);
                }));
            }
            "--rdma-pool-mib" => {
                i += 1;
                rdma_pool_bytes = args
                    .get(i)
                    .and_then(|s| s.parse::<usize>().ok())
                    .filter(|mib| (1..=1 << 20).contains(mib))
                    .map(|mib| mib << 20)
                    .unwrap_or_else(|| {
                        eprintln!("Error: --rdma-pool-mib requires a size in 1..=1048576 MiB");
                        std::process::exit(1);
                    });
            }
            "--read-write" | "--rw" => {
                read_write = true;
            }
            "--no-root-squash" => {
                root_squash = false;
            }
            "--anon-uid" | "--anon-gid" => {
                let flag = args[i].clone();
                i += 1;
                let id = args
                    .get(i)
                    .and_then(|s| s.parse::<u32>().ok())
                    .unwrap_or_else(|| {
                        eprintln!("Error: {flag} requires a numeric id in 0..=4294967295");
                        std::process::exit(1);
                    });
                if flag == "--anon-uid" {
                    anon_uid = id
                } else {
                    anon_gid = id
                }
            }
            "--bind" => {
                i += 1;
                binds.push(args.get(i).and_then(|a| parse_bind_addr(a)).unwrap_or_else(|| {
                    eprintln!("Error: --bind requires an IPv4 or IPv6 address (no port; see --port)");
                    std::process::exit(1);
                }));
            }
            "--no-tcp" => {
                no_tcp = true;
            }
            "--trunk-addr" => {
                i += 1;
                let addr = args
                    .get(i)
                    .and_then(|a| parse_bind_addr(a))
                    .unwrap_or_else(|| {
                        eprintln!("Error: --trunk-addr requires an IPv4 or IPv6 address (no port)");
                        std::process::exit(1);
                    });
                // Canonical text form: what the client's rpc_pton parses.
                let addr = addr.to_string();
                if trunk_addrs.contains(&addr) {
                    eprintln!("Error: --trunk-addr {addr} given twice");
                    std::process::exit(1);
                }
                trunk_addrs.push(addr);
            }
            "--allow-client" => {
                i += 1;
                let pattern = args.get(i).cloned().unwrap_or_default();
                if let Err(e) = nvstor_nfs::server::validate_client_pattern(&pattern) {
                    eprintln!("Error: --allow-client requires an IP address or CIDR prefix: {e}");
                    std::process::exit(1);
                }
                allowed_clients.push(pattern);
            }
            "--fh-key" => {
                i += 1;
                fh_key_path = Some(
                    args.get(i)
                        .filter(|p| !p.is_empty())
                        .map(PathBuf::from)
                        .unwrap_or_else(|| {
                            eprintln!("Error: --fh-key requires a file path");
                            std::process::exit(1);
                        }),
                );
            }
            "--pnfs-ds" => {
                i += 1;
                pnfs_ds = Some(
                    args.get(i)
                        .filter(|h| !h.is_empty())
                        .cloned()
                        .unwrap_or_else(|| {
                            eprintln!("Error: --pnfs-ds requires a host address clients can reach");
                            std::process::exit(1);
                        }),
                );
            }
            _ => dir_path = Some(PathBuf::from(&args[i])),
        }
        i += 1;
    }

    let dir = match dir_path {
        Some(d) => d,
        None => {
            eprintln!("Error: no directory specified");
            std::process::exit(1);
        }
    };

    if !dir.is_dir() {
        eprintln!("Error: {} is not a directory", dir.display());
        std::process::exit(1);
    }

    if no_tcp && !binds.is_empty() {
        eprintln!("Error: --bind configures the NFS/TCP listener, which --no-tcp disables");
        std::process::exit(1);
    }
    if no_tcp && pnfs_ds.is_some() {
        eprintln!("Error: --pnfs-ds advertises the NFS/TCP port, which --no-tcp disables");
        std::process::exit(1);
    }
    if binds.is_empty() {
        binds.push(std::net::Ipv4Addr::UNSPECIFIED.into());
    }

    if allowed_clients.is_empty() {
        eprintln!(
            "Error: no --allow-client given. Every client that can reach the NFS port (TCP or"
        );
        eprintln!(
            "RDMA) could read every file in the export with this server's privileges. Name the"
        );
        eprintln!(
            "clients, e.g. --allow-client 10.0.0.0/8 --allow-client fd00:1::/32, or open it to"
        );
        eprintln!("everyone explicitly with --allow-client 0.0.0.0/0 --allow-client ::/0.");
        std::process::exit(1);
    }

    tracing::info!(
        directory = %dir.display(),
        port,
        root_squash,
        anon_uid,
        anon_gid,
        "starting NFS v4.2 export"
    );
    if !root_squash {
        tracing::warn!(
            "--no-root-squash: clients sending uid 0 can read and change every file in the export"
        );
    }

    let fh_key = match &fh_key_path {
        Some(path) => load_fh_key(path, &dir).unwrap_or_else(|e| {
            eprintln!("Error: --fh-key {}: {e}", path.display());
            std::process::exit(1);
        }),
        None => {
            tracing::warn!("no --fh-key: filehandles are sealed with a random key and go stale when this server restarts");
            nvstor_nfs::passthrough::FhKey::random().unwrap_or_else(|e| {
                eprintln!("Error: generating a filehandle key: {e}");
                std::process::exit(1);
            })
        }
    };

    // Create passthrough VFS.
    let vfs = match nvstor_nfs::passthrough::PassthroughVfs::with_fh_key(&dir, fh_key) {
        Ok(mut v) => {
            if read_write {
                v.set_read_write();
            }
            if fh_key_path.is_some() && !v.restart_safe() {
                tracing::warn!("filehandles will not survive a restart despite --fh-key (see the warning above)");
            }
            Arc::new(v)
        }
        Err(e) => {
            eprintln!("Error creating VFS: {e}");
            std::process::exit(1);
        }
    };

    // Create NFS COMPOUND processor with passthrough backend.
    let processor = Arc::new(nvstor_nfs::nfs4::CompoundProcessor::new_passthrough(vfs));
    let metrics = processor.metrics_arc();
    // pNFS only when the operator names a data-server address clients can
    // reach. The address goes into every layout verbatim: advertising a
    // loopback address sends each remote client's I/O to its own host.
    processor.set_pnfs_ds_addr(pnfs_ds.map(|host| (host, port)));
    // Session trunking over the rails: clients mounting with
    // trunkdiscovery get these from fs_locations, one transport each.
    if !trunk_addrs.is_empty() {
        tracing::info!(addrs = ?trunk_addrs, "advertising trunk addresses (fs_locations)");
    }
    processor.set_trunk_addrs(trunk_addrs);

    // Create NFS server config.
    let config = nvstor_nfs::server::NfsServerConfig {
        max_connections: 1024,
        idle_timeout: std::time::Duration::from_secs(300),
        write_timeout: std::time::Duration::from_secs(60),
        exports: vec![nvstor_nfs::server::NfsExport {
            path: "/".to_string(),
            // Enforced at accept on both the TCP and the RDMA listener.
            allowed_clients,
            read_only: !read_write,
            // Applied to every COMPOUND, on TCP and RDMA alike.
            root_squash,
            anon_uid,
            anon_gid,
            sec: vec!["sys".to_string()],
        }],
        rdma_pool_bytes,
    };

    let server = nvstor_nfs::server::NfsServer::new(processor.clone(), config);

    // Run the server.
    let rt = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .expect("tokio runtime");
    rt.block_on(async {
        #[cfg(feature = "rdma")]
        let rdma_listener = nvstor_nfs::server::bind_rdma_listener(rdma_port).unwrap_or_else(|error| {
            eprintln!("Error: {error}"); std::process::exit(1);
        });
        #[cfg(feature = "rdma")]
        if rdma_listener.is_some() {
            // A TCP pNFS data-server address must not divert an RDMA mount's I/O.
            processor.set_pnfs_ds_addr(None);
        }
        #[cfg(feature = "rdma")]
        if no_tcp && rdma_listener.is_none() {
            eprintln!("Error: --no-tcp and no NFS/RDMA listener (no active RDMA device, or --rdma-port 0): nothing to serve");
            std::process::exit(1);
        }
        #[cfg(not(feature = "rdma"))]
        if rdma_port.is_some_and(|port| port != 0) || no_tcp {
            eprintln!("Error: this binary was built without the rdma feature");
            std::process::exit(1);
        }
        let listeners = if no_tcp {
            Vec::new()
        } else {
            nvstor_nfs::server::bind_tcp_listeners(&binds, port).unwrap_or_else(|e| {
                eprintln!("Error binding {e}");
                if port < 1024 {
                    eprintln!("Hint: ports below 1024 require root. Try --port 12049");
                }
                std::process::exit(1);
            })
        };

        // Spawn the metrics HTTP server. `--metrics-port 0` skips it.
        if metrics_port != 0 {
            let metrics_addr = format!("0.0.0.0:{metrics_port}");
            tokio::spawn(async move {
                if let Err(e) = nvstor_nfs::metrics_server::serve(&metrics_addr, metrics).await {
                    tracing::warn!(error = %e, "metrics: HTTP server exited");
                }
            });
        }

        if no_tcp {
            tracing::info!(directory = %dir.display(), "NFS v4.2 server listening on NFS/RDMA only (--no-tcp)");
        } else {
            let addrs: Vec<String> = binds.iter().map(|ip| std::net::SocketAddr::new(*ip, port).to_string()).collect();
            tracing::info!(addrs = %addrs.join(" "), directory = %dir.display(), "NFS v4.2 server listening");
            let host = hostname::get().map(|h| h.to_string_lossy().to_string()).unwrap_or_else(|_| "localhost".into());
            let opts = if port == 2049 { String::new() } else { format!("-o port={port} ") };
            tracing::info!("Client: mount -t nfs4 {opts}{host}:/ /mnt/target");
        }
        if metrics_port != 0 {
            tracing::info!(metrics_port, "Metrics: curl http://<host>:{metrics_port}/metrics");
        }

        let (stop_tx, stop_rx) = tokio::sync::watch::channel(false);
        let signal_tx = stop_tx.clone();
        tokio::spawn(async move {
            let mut term = match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
                Ok(term) => term,
                Err(error) => { tracing::error!(%error, "could not register SIGTERM handler"); signal_tx.send_replace(true); return; }
            };
            tokio::select! { _ = tokio::signal::ctrl_c() => {}, _ = term.recv() => {} }
            signal_tx.send_replace(true);
        });
        let (tcp_result, rdma_result) = tokio::join!(
            async {
                if listeners.is_empty() {
                    return Ok(());
                }
                let result = server.run_listeners_until_shutdown(listeners, stop_rx.clone()).await;
                stop_tx.send_replace(true);
                result
            },
            async {
                #[cfg(feature = "rdma")]
                if let Some(listener) = rdma_listener {
                    // A failed RDMA listener must not take NFS/TCP down: with
                    // TCP serving it is re-created with backoff. With --no-tcp
                    // the process has nothing left to serve and exits non-zero
                    // so its supervisor (the kubelet) restarts it.
                    let on_failure = if no_tcp {
                        nvstor_nfs::server::RdmaListenerFailure::Exit
                    } else {
                        nvstor_nfs::server::RdmaListenerFailure::Rebind
                    };
                    let port = rdma_port.unwrap_or(nvstor_nfs::server::DEFAULT_NFS_RDMA_PORT);
                    let result = server.run_rdma_supervised(listener, port, on_failure, stop_rx.clone()).await;
                    if result.is_err() {
                        stop_tx.send_replace(true);
                    }
                    return result;
                }
                Ok::<(), std::io::Error>(())
            }
        );
        if let Err(e) = tcp_result.and(rdma_result) {
            eprintln!("Server error: {e}");
            std::process::exit(1);
        }
    });
}

/// An address for --bind: "10.0.0.5", "::", "fd00::1" or "[fd00::1]".
fn parse_bind_addr(arg: &str) -> Option<std::net::IpAddr> {
    let bare = arg
        .strip_prefix('[')
        .and_then(|a| a.strip_suffix(']'))
        .unwrap_or(arg);
    bare.parse().ok()
}

/// Load (or create) the filehandle key at `path`, refusing a location inside
/// the export, where clients could read it.
fn load_fh_key(path: &Path, export: &Path) -> std::io::Result<nvstor_nfs::passthrough::FhKey> {
    let export = std::fs::canonicalize(export)?;
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    let name = path
        .file_name()
        .ok_or_else(|| std::io::Error::new(std::io::ErrorKind::InvalidInput, "not a file path"))?;
    let resolved = std::fs::canonicalize(parent)?.join(name);
    if resolved.starts_with(&export) {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            format!(
                "the key must not lie inside the export {}",
                export.display()
            ),
        ));
    }
    nvstor_nfs::passthrough::FhKey::load_or_create(&resolved)
}
