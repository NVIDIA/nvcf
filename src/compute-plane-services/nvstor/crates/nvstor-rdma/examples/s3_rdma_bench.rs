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

//! S3-over-RDMA cluster bench tool.
//!
//! Connects to an nvstor-server's RDMA transport, runs PUT/GET cycles
//! through `S3RdmaClient`, and reports throughput + latency.
//!
//!   ./target/release/examples/s3_rdma_bench \
//!       --addr <server-ip>:9877 --size-mb 16 --ops 1000 \
//!       --mode put --concurrency 32
//!
//! Modes:
//!   put     — alloc + persist; the server RDMA-READs the payload (checkpoint workload)
//!   get     — server RDMA-WRITEs into client RMR (training fanout)
//!   head    — control-plane only, no data movement (latency floor)
//!
//! Concurrency >1 spawns N worker threads that share a single
//! `RdmaTransport` + peer connection. Ops are evenly divided between
//! them. This models the real checkpoint workload where a single
//! GPU process pipelines many MPU parts at once.
//!
//! Throughput / latency are wall-clock around the client method call,
//! so they include the SEND/RECV round-trip + the bulk WRITE. Server
//! work (object_write / object_read) shows up in the wall clock too.

use std::sync::Arc;
use std::time::{Duration, Instant};

use nvstor_rdma::device::RdmaDevice;
use nvstor_rdma::pool::MemoryPool;
use nvstor_rdma::s3_rdma::S3RdmaClient;
use nvstor_rdma::transport::RdmaTransport;

fn arg(name: &str) -> Option<String> {
    let args: Vec<String> = std::env::args().collect();
    let prefix = format!("--{name}=");
    for a in &args {
        if let Some(v) = a.strip_prefix(&prefix) {
            return Some(v.to_string());
        }
    }
    // also support `--name value` form
    let mut iter = args.iter();
    while let Some(a) = iter.next() {
        if a == &format!("--{name}") {
            if let Some(v) = iter.next() {
                return Some(v.clone());
            }
        }
    }
    None
}

fn arg_or<T: std::str::FromStr>(name: &str, default: T) -> T {
    arg(name).and_then(|v| v.parse().ok()).unwrap_or(default)
}

fn main() -> Result<(), String> {
    // Bench is meant to be `cargo run --example`d on the cluster
    // without any extra deps. Errors print to stderr via the `?`
    // bubble-up at the end of main.

    let addr = arg("addr").ok_or("--addr=<ip:port> required (the nvstor-server rdma_port)")?;
    let size_mb: usize = arg_or("size-mb", 16);
    let ops: usize = arg_or("ops", 100);
    let concurrency: usize = arg_or::<usize>("concurrency", 1).max(1);
    let mode = arg_or::<String>("mode", "put".into());
    let key_prefix = arg_or::<String>("key-prefix", "s3rdma-bench".into());

    // Pool slot size = payload size. Slot count = concurrency * 2 so
    // each worker has two slots in flight (one in ALLOC, one in
    // PERSIST drain). Cap at 256 slots to bound registered memory.
    let payload_size = size_mb * 1024 * 1024;
    let slot_count = (concurrency * 2).clamp(8, 256);

    let dev =
        Arc::new(RdmaDevice::open_first_active().map_err(|e| format!("open_first_active: {e}"))?);
    println!("client RDMA device: {} (port {})", dev.name, dev.port);

    let pool = Arc::new(
        MemoryPool::new(&dev, payload_size, slot_count).map_err(|e| format!("pool: {e}"))?,
    );
    let transport = Arc::new(RdmaTransport::new(dev, pool));
    transport.set_credentials(bench_credentials());

    let peer_id = transport
        .connect(&addr)
        .map_err(|e| format!("connect {addr}: {e}"))?;
    println!("connected to {addr} as peer_id={peer_id}");
    std::thread::sleep(Duration::from_millis(200));

    let payload = Arc::new(vec![0xABu8; payload_size]);
    println!(
        "mode={mode}, ops={ops}, payload={} MiB ({} bytes), concurrency={concurrency}",
        payload_size / (1024 * 1024),
        payload_size,
    );

    let ops_per_worker = ops / concurrency;
    let total_ops = ops_per_worker * concurrency;
    if total_ops != ops {
        eprintln!("note: rounded ops {ops} → {total_ops} for clean per-worker split");
    }

    // Pre-populate for GET mode (single-threaded so we don't double-
    // count writes in the bench timer below).
    if mode == "get" {
        let warmup_client = S3RdmaClient::new(Arc::clone(&transport), peer_id);
        for i in 0..total_ops {
            let key = format!("{key_prefix}/get-{i}");
            let _ = warmup_client.put(&key, &payload)?;
        }
    }

    let start = Instant::now();
    let mut handles = Vec::with_capacity(concurrency);
    for w in 0..concurrency {
        let transport = Arc::clone(&transport);
        let payload = Arc::clone(&payload);
        let key_prefix = key_prefix.clone();
        let mode = mode.clone();
        let handle = std::thread::spawn(move || -> Result<(Vec<u128>, u64), String> {
            let client = S3RdmaClient::new(transport, peer_id);
            let mut lat = Vec::with_capacity(ops_per_worker);
            let mut bytes: u64 = 0;
            let base = w * ops_per_worker;
            for i in 0..ops_per_worker {
                let idx = base + i;
                let key = format!("{key_prefix}/{mode}-{idx}");
                let t0 = Instant::now();
                let n = match mode.as_str() {
                    "put" => client.put(&key, &payload)?,
                    "get" => {
                        let d = client.get(&key)?;
                        d.len() as u64
                    }
                    "head" => {
                        client.put(&key, &payload[..16])?;
                        let _ = client.head(&key)?;
                        0
                    }
                    other => return Err(format!("unknown mode: {other}")),
                };
                lat.push(t0.elapsed().as_micros());
                bytes += n;
            }
            Ok((lat, bytes))
        });
        handles.push(handle);
    }

    let mut latencies_us: Vec<u128> = Vec::with_capacity(total_ops);
    let mut bytes_total: u64 = 0;
    for h in handles {
        let (lat, bytes) = h.join().map_err(|_| "worker panicked")??;
        latencies_us.extend(lat);
        bytes_total += bytes;
    }
    let elapsed = start.elapsed();

    latencies_us.sort_unstable();
    let p50 = latencies_us[latencies_us.len() / 2];
    let p99 = latencies_us[(latencies_us.len() * 99) / 100];
    let p999 = latencies_us[(latencies_us.len() * 999) / 1000];
    let avg_us = latencies_us.iter().sum::<u128>() / latencies_us.len() as u128;

    println!();
    println!("=== {mode} bench result ===");
    println!("  wall time:   {:.3}s", elapsed.as_secs_f64());
    println!("  ops:         {} ({}× workers)", total_ops, concurrency);
    println!(
        "  ops/sec:     {:.0}",
        total_ops as f64 / elapsed.as_secs_f64()
    );
    if bytes_total > 0 {
        let mib = bytes_total as f64 / (1024.0 * 1024.0);
        let mib_s = mib / elapsed.as_secs_f64();
        println!("  data:        {:.1} MiB total", mib);
        println!(
            "  throughput:  {:.1} MiB/s   ({:.3} GiB/s)",
            mib_s,
            mib_s / 1024.0
        );
    }
    println!("  latency µs:  avg={avg_us}  p50={p50}  p99={p99}  p999={p999}");

    Ok(())
}

/// Credentials for the signed transport. The server enforces auth, so
/// a bench run must present a valid IAM key or every request is rejected.
/// Read from the environment so the bench does not carry a secret in-tree.
fn bench_credentials() -> nvstor_rdma::auth::Credentials {
    let access_key = std::env::var("NVSTOR_ACCESS_KEY").unwrap_or_else(|_| {
        eprintln!("NVSTOR_ACCESS_KEY not set — the server will reject every request");
        String::new()
    });
    let secret_key = std::env::var("NVSTOR_SECRET_KEY").unwrap_or_default();
    nvstor_rdma::auth::Credentials::new(access_key, secret_key)
}
