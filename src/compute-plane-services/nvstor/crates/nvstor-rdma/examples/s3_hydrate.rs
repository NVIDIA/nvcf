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

//! Node-to-node model hydration over S3-over-TCP.
//!
//! ```text
//! s3_hydrate get --addr HOST:PORT --key KEY --out PATH [--connections N] [--range-mib M]
//!                [--transport tcp|rdma]
//! s3_hydrate put --addr HOST:PORT --key KEY --in PATH
//! ```
//!
//! `get` fetches KEY as parallel ranged GETs into PATH (see
//! `nvstor_rdma::tcp::hydrate`): every range CRC-32C-verified and pinned
//! to one object generation, written to `PATH.partial`, fdatasync'ed,
//! then renamed — PATH exists only once the object is complete and
//! durable. Prints a one-line JSON report. `--transport rdma` dials the
//! server's `rdma_port` instead of `s3_tcp_port`, asks it for its rails
//! (one per NIC) and spreads the connections across them, each on the
//! local ACTIVE NIC that reaches that server NIC (`nvstor_rdma::rail`).
//! Each connection is its own QP set; ranges are RDMA-WRITTEN into a
//! registered slot of `--range-mib` and written to PATH from there. The
//! report's `rails` field counts connections per local NIC.
//!
//! `put` uploads PATH as KEY in one request, streamed from the file in
//! bounded chunks (the server streams it into storage the same way), with
//! the CRC-32C of the sent bytes checked against the server's.
//!
//! Credentials come from NVSTOR_ACCESS_KEY / NVSTOR_SECRET_KEY.

use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Instant;

use nvstor_rdma::auth::Credentials;
use nvstor_rdma::rail::Rails;
use nvstor_rdma::tcp::{hydrate_to_file, HydrateOptions, S3TcpClient, TcpTransport};

const USAGE: &str = "usage:
  s3_hydrate get --addr HOST:PORT --key KEY --out PATH [--connections N] [--range-mib M] [--transport tcp|rdma]
  s3_hydrate put --addr HOST:PORT --key KEY --in PATH
env: NVSTOR_ACCESS_KEY, NVSTOR_SECRET_KEY";

fn main() {
    if let Err(e) = run() {
        eprintln!("s3_hydrate: {e}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let argv: Vec<String> = std::env::args().skip(1).collect();
    let Some((mode, rest)) = argv.split_first() else {
        return Err(USAGE.into());
    };
    if mode == "--help" || mode == "-h" {
        println!("{USAGE}");
        return Ok(());
    }
    let allowed: &[&str] = match mode.as_str() {
        "get" => &[
            "--addr",
            "--key",
            "--out",
            "--connections",
            "--range-mib",
            "--transport",
        ],
        "put" => &["--addr", "--key", "--in"],
        other => return Err(format!("unknown mode {other:?}\n{USAGE}")),
    };
    if rest.len() % 2 != 0 {
        return Err(format!("arguments come in --name value pairs\n{USAGE}"));
    }
    let mut args = BTreeMap::new();
    for pair in rest.chunks(2) {
        if !allowed.contains(&pair[0].as_str())
            || args.insert(pair[0].as_str(), pair[1].clone()).is_some()
        {
            return Err(format!(
                "unknown or duplicate argument {}\n{USAGE}",
                pair[0]
            ));
        }
    }
    let required = |name: &str| {
        args.get(name)
            .cloned()
            .ok_or_else(|| format!("{name} is required\n{USAGE}"))
    };
    let number = |name: &str, default: usize| -> Result<usize, String> {
        args.get(name)
            .map(|v| v.parse().map_err(|_| format!("invalid {name}")))
            .unwrap_or(Ok(default))
    };
    let addr = required("--addr")?;
    let key = required("--key")?;
    let creds = Credentials::new(
        std::env::var("NVSTOR_ACCESS_KEY").map_err(|_| "NVSTOR_ACCESS_KEY is required")?,
        std::env::var("NVSTOR_SECRET_KEY").map_err(|_| "NVSTOR_SECRET_KEY is required")?,
    );

    match mode.as_str() {
        "get" => {
            let out = PathBuf::from(required("--out")?);
            let opts = HydrateOptions {
                connections: number("--connections", HydrateOptions::default().connections)?,
                range_size: number("--range-mib", 16)?.saturating_mul(1024 * 1024),
                ..HydrateOptions::default()
            };
            if !(1..=256).contains(&opts.connections) || opts.range_size == 0 {
                return Err("bounds: 1..256 connections, --range-mib >= 1".into());
            }
            let mut rails_used = String::new();
            let r = match args.get("--transport").map(String::as_str).unwrap_or("tcp") {
                "tcp" => hydrate_to_file(&addr, &creds, &key, &out, &opts),
                "rdma" => {
                    // Every connection may land on one NIC (when only one
                    // reaches the server), so each NIC's pool holds a slot
                    // per connection.
                    let rails = Rails::open(opts.range_size, opts.connections, &creds)?;
                    let r = nvstor_rdma::s3_rdma::hydrate_to_file(&rails, &addr, &key, &out, &opts);
                    let used: Vec<String> = rails
                        .opened()
                        .iter()
                        .map(|(name, n)| format!("{name:?}:{n}"))
                        .collect();
                    rails_used = format!(",\"rails\":{{{}}}", used.join(","));
                    rails.shutdown();
                    r
                }
                other => return Err(format!("unknown --transport {other:?}")),
            }
            .map_err(|e| e.to_string())?;
            let secs = r.elapsed.as_secs_f64();
            println!(
                "{{\"key\":{key:?},\"out\":{out:?},\"bytes\":{},\"generation\":\"{:032x}\",\"ranges\":{},\"retries\":{},\"reconnects\":{},\"seconds\":{secs:.3},\"sync_seconds\":{:.3},\"gib_per_s\":{:.3}{rails_used}}}",
                r.bytes, r.generation, r.ranges, r.retries, r.reconnects, r.sync.as_secs_f64(),
                r.bytes as f64 / (1u64 << 30) as f64 / secs.max(1e-9),
            );
        }
        "put" => {
            let path = required("--in")?;
            let mut file = std::fs::File::open(&path).map_err(|e| format!("open {path}: {e}"))?;
            let length = file
                .metadata()
                .map_err(|e| format!("stat {path}: {e}"))?
                .len();
            let transport = TcpTransport::new();
            transport.set_credentials(creds);
            let peer = transport
                .connect(&addr)
                .map_err(|e| format!("connect {addr}: {e}"))?;
            let client = S3TcpClient::new(Arc::clone(&transport), peer);
            let started = Instant::now();
            let n = client.put_from_reader(&key, length, &mut file)?;
            println!(
                "{{\"key\":{key:?},\"bytes\":{n},\"seconds\":{:.3}}}",
                started.elapsed().as_secs_f64()
            );
            transport.shutdown();
        }
        _ => unreachable!("mode validated above"),
    }
    Ok(())
}
