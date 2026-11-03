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

//! Verify the production RDMA/TCP object paths with independent client peers.

use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::Instant;

use nvstor_rdma::auth::Credentials;
use nvstor_rdma::device::RdmaDevice;
use nvstor_rdma::pool::MemoryPool;
use nvstor_rdma::s3_rdma::S3RdmaClient;
use nvstor_rdma::tcp::{S3TcpClient, TcpTransport};
use nvstor_rdma::transport::RdmaTransport;

enum Client {
    Rdma(S3RdmaClient, Arc<RdmaTransport>, u64),
    Tcp(S3TcpClient, Arc<TcpTransport>),
}

impl Client {
    fn connect(
        kind: &str,
        addr: &str,
        max_bytes: usize,
        creds: Credentials,
    ) -> Result<Self, String> {
        match kind {
            "rdma" => {
                let device = Arc::new(RdmaDevice::open_first_active()?);
                let pool = Arc::new(MemoryPool::new(&device, max_bytes.max(1), 2)?);
                let transport = Arc::new(RdmaTransport::new(device, pool));
                transport.set_credentials(creds);
                let peer = transport.connect(addr).map_err(|error| error.to_string())?;
                Ok(Self::Rdma(
                    S3RdmaClient::new(transport.clone(), peer),
                    transport,
                    peer,
                ))
            }
            "tcp" => {
                let transport = TcpTransport::new();
                transport.set_credentials(creds);
                let peer = transport.connect(addr).map_err(|error| error.to_string())?;
                Ok(Self::Tcp(
                    S3TcpClient::new(transport.clone(), peer),
                    transport,
                ))
            }
            _ => Err("--transport must be rdma or tcp".into()),
        }
    }

    fn put(&self, key: &str, data: &[u8]) -> Result<u64, String> {
        match self {
            Self::Rdma(client, ..) => client.put(key, data),
            Self::Tcp(client, ..) => client.put(key, data),
        }
    }

    fn get(&self, key: &str) -> Result<Vec<u8>, String> {
        match self {
            Self::Rdma(client, ..) => client.get(key),
            Self::Tcp(client, ..) => client.get(key),
        }
    }

    fn head(&self, key: &str) -> Result<u64, String> {
        match self {
            Self::Rdma(client, ..) => client.head(key),
            Self::Tcp(client, ..) => client.head(key),
        }
    }

    fn delete(&self, key: &str) -> Result<(), String> {
        match self {
            Self::Rdma(client, ..) => client.delete(key),
            Self::Tcp(client, ..) => client.delete(key),
        }
    }
}

impl Drop for Client {
    fn drop(&mut self) {
        match self {
            Self::Rdma(_, transport, peer) => {
                transport.disconnect(*peer);
                transport.shutdown();
            }
            Self::Tcp(_, transport) => transport.shutdown(),
        }
    }
}

#[derive(Default)]
struct Stats {
    put_us: Vec<u128>,
    get_us: Vec<u128>,
    bytes: u64,
}

fn payload(length: usize, worker: usize, cycle: usize, generation: usize) -> Vec<u8> {
    // Reproducible across separate write/verify invocations, different per object
    // and generation. Work occurs outside the timed request interval.
    let mut state = 0x9e3779b97f4a7c15_u64
        ^ ((worker as u64) << 40)
        ^ ((cycle as u64) << 8)
        ^ generation as u64;
    let mut data = vec![0; length];
    for chunk in data.chunks_mut(8) {
        state ^= state << 13;
        state ^= state >> 7;
        state ^= state << 17;
        chunk.copy_from_slice(&state.to_le_bytes()[..chunk.len()]);
    }
    data
}

fn verify(client: &Client, key: &str, expected: &[u8], stats: &mut Stats) -> Result<(), String> {
    if client.head(key)? != expected.len() as u64 {
        return Err(format!("HEAD size mismatch: {key}"));
    }
    let begin = Instant::now();
    let actual = client.get(key)?;
    stats.get_us.push(begin.elapsed().as_micros());
    if actual != expected {
        return Err(format!(
            "GET contents mismatch: {key}, expected {} bytes, received {}",
            expected.len(),
            actual.len()
        ));
    }
    stats.bytes += actual.len() as u64;
    Ok(())
}

fn missing(client: &Client, key: &str) -> Result<(), String> {
    match client.get(key) {
        Err(error) if error == "GET: not found" || error == "GET failed: status=1" => Ok(()),
        Err(error) => Err(format!("GET after DELETE did not return NotFound: {error}")),
        Ok(_) => Err(format!("deleted object remains readable: {key}")),
    }
}

fn percentile(values: &mut [u128], percent: usize) -> u128 {
    values.sort_unstable();
    values
        .get(values.len().saturating_sub(1) * percent / 100)
        .copied()
        .unwrap_or(0)
}

fn main() -> Result<(), String> {
    let raw: Vec<_> = std::env::args().skip(1).collect();
    if raw.iter().any(|arg| arg == "--help") {
        println!(
            "s3_stress --transport rdma|tcp --addr HOST:PORT [--workers 4] [--cycles 100]\n\
            [--max-bytes 1048576] [--prefix RUN_ID] [--phase lifecycle|write|verify]\n\
            [--verify-transport tcp|rdma --verify-addr HOST:PORT]\n\
            Requires NVSTOR_ACCESS_KEY and NVSTOR_SECRET_KEY. RDMA never falls back.\n\
            write retains objects; verify rechecks them after restart using identical arguments.\n\
            The two verification endpoints must serve the same PM namespace."
        );
        return Ok(());
    }
    if raw.len() % 2 != 0 {
        return Err("arguments must be --name value pairs; see --help".into());
    }
    let mut args = BTreeMap::new();
    for pair in raw.chunks_exact(2) {
        if ![
            "--transport",
            "--addr",
            "--workers",
            "--cycles",
            "--max-bytes",
            "--prefix",
            "--phase",
            "--verify-transport",
            "--verify-addr",
        ]
        .contains(&pair[0].as_str())
            || args.insert(pair[0].clone(), pair[1].clone()).is_some()
        {
            return Err(format!("unknown or duplicate argument: {}", pair[0]));
        }
    }
    let required = |name: &str| {
        args.get(name)
            .cloned()
            .ok_or_else(|| format!("{name} is required"))
    };
    let number = |name: &str, default: usize| -> Result<usize, String> {
        args.get(name)
            .map(|value| value.parse().map_err(|_| format!("invalid {name}")))
            .unwrap_or(Ok(default))
    };
    let kind = required("--transport")?;
    let addr = required("--addr")?;
    let workers = number("--workers", 4)?;
    let cycles = number("--cycles", 100)?;
    let max_bytes = number("--max-bytes", 1024 * 1024)?;
    let phase = args
        .get("--phase")
        .cloned()
        .unwrap_or_else(|| "lifecycle".into());
    if !["rdma", "tcp"].contains(&kind.as_str())
        || !["lifecycle", "write", "verify"].contains(&phase.as_str())
    {
        return Err("invalid transport or phase; see --help".into());
    }
    if !(1..=64).contains(&workers)
        || !(1..=10000).contains(&cycles)
        || !(1..=64 * 1024 * 1024).contains(&max_bytes)
        || workers * max_bytes * 2 > 512 * 1024 * 1024
    {
        return Err("bounds: 1..64 workers, 1..10000 cycles, 1..64 MiB max-bytes, at most 512 MiB client pool".into());
    }
    if phase != "lifecycle" && !args.contains_key("--prefix") {
        return Err(
            "write/verify require a stable --prefix; use identical sizes and worker counts".into(),
        );
    }
    let prefix = args.get("--prefix").cloned().unwrap_or_else(|| {
        format!(
            "stress-{}-{}",
            std::process::id(),
            nvstor_rdma::auth::now_secs()
        )
    });
    let cross = match (args.get("--verify-transport"), args.get("--verify-addr")) {
        (Some(kind), Some(addr)) if ["rdma", "tcp"].contains(&kind.as_str()) => {
            Some((kind.clone(), addr.clone()))
        }
        (None, None) => None,
        _ => return Err("provide both valid --verify-transport and --verify-addr".into()),
    };
    let creds = Credentials::new(
        std::env::var("NVSTOR_ACCESS_KEY").map_err(|_| "NVSTOR_ACCESS_KEY is required")?,
        std::env::var("NVSTOR_SECRET_KEY").map_err(|_| "NVSTOR_SECRET_KEY is required")?,
    );
    println!("transport={kind} phase={phase} prefix={prefix:?} workers={workers} cycles={cycles} max_bytes={max_bytes}");
    let started = Instant::now();
    let mut handles = Vec::new();
    for worker in 0..workers {
        let (kind, addr, phase, prefix, cross, creds) = (
            kind.clone(),
            addr.clone(),
            phase.clone(),
            prefix.clone(),
            cross.clone(),
            creds.clone(),
        );
        handles.push(std::thread::spawn(move || -> Result<Stats, String> {
            let client = Client::connect(&kind, &addr, max_bytes, creds.clone())?;
            let alternate = cross
                .map(|(kind, addr)| Client::connect(&kind, &addr, max_bytes, creds))
                .transpose()?;
            let mut stats = Stats::default();
            let sizes = [0, 1, 4095, 4096, 65536, max_bytes];
            for cycle in 0..cycles {
                let key = format!("{prefix}/worker-{worker}/object-{cycle}");
                for generation in 0..2 {
                    if phase == "verify" && generation == 0 {
                        continue;
                    }
                    let length = sizes[(cycle + generation * 3) % sizes.len()].min(max_bytes);
                    let expected = payload(length, worker, cycle, generation);
                    if phase != "verify" {
                        let begin = Instant::now();
                        let written = client.put(&key, &expected)?;
                        stats.put_us.push(begin.elapsed().as_micros());
                        if written != expected.len() as u64 {
                            return Err(format!("PUT length mismatch: {key}"));
                        }
                        stats.bytes += written;
                    }
                    verify(&client, &key, &expected, &mut stats)?;
                    if let Some(other) = &alternate {
                        verify(other, &key, &expected, &mut stats)?;
                    }
                }
                if phase == "lifecycle" {
                    client.delete(&key)?;
                    missing(&client, &key)?;
                    if let Some(other) = &alternate {
                        missing(other, &key)?;
                    }
                }
            }
            Ok(stats)
        }));
    }
    let mut stats = Stats::default();
    let mut errors = Vec::new();
    for (worker, handle) in handles.into_iter().enumerate() {
        match handle
            .join()
            .map_err(|_| "worker panicked".to_string())
            .and_then(|result| result)
        {
            Ok(mut result) => {
                stats.put_us.append(&mut result.put_us);
                stats.get_us.append(&mut result.get_us);
                stats.bytes += result.bytes;
            }
            Err(error) => errors.push(format!("worker {worker}: {error}")),
        }
    }
    if !errors.is_empty() {
        return Err(errors.join("\n"));
    }
    let seconds = started.elapsed().as_secs_f64();
    let put_p99 = percentile(&mut stats.put_us, 99);
    let get_p99 = percentile(&mut stats.get_us, 99);
    println!("RESULT {{\"verified_objects\":{},\"put_requests\":{},\"get_requests\":{},\"put_p99_us\":{},\"get_p99_us\":{},\"verified_bytes\":{},\"wall_seconds\":{:.3}}}",
        workers * cycles, stats.put_us.len(), stats.get_us.len(), put_p99, get_p99, stats.bytes, seconds);
    Ok(())
}
