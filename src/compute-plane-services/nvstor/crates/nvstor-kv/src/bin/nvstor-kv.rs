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

//! `nvstor-kv`: the KV block server, and a benchmark client that loads
//! blocks into GPU memory laid out as vLLM lays out a hybrid-attention model's KV cache.
//!
//!   nvstor-kv serve [--port 9900] [--devices netns] [--slots 4096]
//!                   [--slot-bytes 3080192] [--max-per-identity 256]
//!                   [--no-verify] [--ready-file PATH]
//!     Serves on every selected NIC, rail i on port + i. Writes, once all
//!     rails listen, one line per rail to the ready file: "rail port gid".
//!
//!   nvstor-kv bench --server HOST [--port 9900] --rails N [--devices netns]
//!                   --gpu N [--connections 16] [--batch 8] [--blocks 2000]
//!                   [--warmup-s 3] [--duration-s 30] [--verify] [--json]
//!     PUTs `blocks` distinct blocks (each one block of the example layout: 99 pages of
//!     36,864 and 8,448 bytes), then for `duration` seconds each of
//!     `connections` connections GETs batches of `batch` random blocks
//!     into its own landing slots on the GPU. Connection c uses server
//!     rail c % N. `--verify` reads every landed block back and checks its
//!     block check. Reports blocks/s, GiB/s and per-batch latency.
//!
//! Both need NVSTOR_KV_SECRET (16+ characters); the access key is
//! NVSTOR_KV_ACCESS_KEY (default "kv").

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use nvstor_kv::client::{connect_rail, kv_rails, KvClient, RailExchange};
use nvstor_kv::server::{
    block_check, kv_transport, ConnectionCap, KvAuthContext, KvAuthFn, KvServer, KvServerConfig,
    Shared,
};
use nvstor_peer::gpu::{GpuBuffer, Mapping};
use nvstor_rdma::auth::{verify_signature, AuthError, Credentials};
use nvstor_rdma::bootstrap::{HandshakeAuth, HandshakeVerifier, PeerRole};
use nvstor_rdma::device::Access;
use nvstor_rdma::kv_proto::{
    GetEntry, Key, Layout, PutEntry, TensorRun, NAMESPACE_LEN, STATUS_EXISTS, STATUS_OK,
};
use nvstor_rdma::rail::{open_devices, DeviceSelect};
use nvstor_rdma::region::RailRegion;
use nvstor_storage::kv_cache::{KvCache, KvCacheConfig};

const SECRET_ENV: &str = "NVSTOR_KV_SECRET";
const KEY_ENV: &str = "NVSTOR_KV_ACCESS_KEY";
const NAMESPACE: [u8; NAMESPACE_LEN] = *b"nvstor-kv-bench-";

/// The example KV block layout (a hybrid-attention model as vLLM allocates it): 78 pages of 36,864 B, 21 of 8,448 B.
fn glm_pages() -> Vec<u32> {
    (0..99)
        .map(|i| if i < 78 { 36_864 } else { 8_448 })
        .collect()
}

struct Args(Vec<String>);

impl Args {
    fn take(&mut self, flag: &str) -> Option<String> {
        let i = self.0.iter().position(|a| a == flag)?;
        self.0.remove(i);
        (i < self.0.len()).then(|| self.0.remove(i))
    }
    fn number<T: std::str::FromStr>(&mut self, flag: &str, default: T) -> Result<T, String> {
        match self.take(flag) {
            None => Ok(default),
            Some(v) => v
                .parse()
                .map_err(|_| format!("{flag}: not a number: {v:?}")),
        }
    }
    fn switch(&mut self, flag: &str) -> bool {
        let found = self.0.iter().position(|a| a == flag);
        if let Some(i) = found {
            self.0.remove(i);
        }
        found.is_some()
    }
    fn done(self) -> Result<(), String> {
        if self.0.is_empty() {
            Ok(())
        } else {
            Err(format!("unknown arguments: {:?}", self.0))
        }
    }
}

fn credentials() -> Result<Credentials, String> {
    let secret = std::env::var(SECRET_ENV).map_err(|_| format!("{SECRET_ENV} is not set"))?;
    if secret.len() < 16 {
        return Err(format!("{SECRET_ENV} must be at least 16 characters"));
    }
    Ok(Credentials::new(
        std::env::var(KEY_ENV).unwrap_or_else(|_| "kv".into()),
        secret,
    ))
}

fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
        .with_writer(std::io::stderr)
        .init();
    let mut argv: Vec<String> = std::env::args().skip(1).collect();
    let cmd = if argv.is_empty() {
        String::new()
    } else {
        argv.remove(0)
    };
    let r = match cmd.as_str() {
        "serve" => serve(Args(argv)),
        "bench" => bench(Args(argv)),
        _ => Err("usage: nvstor-kv serve|bench ... (see the source header)".into()),
    };
    if let Err(e) = r {
        eprintln!("nvstor-kv: {e}");
        std::process::exit(1);
    }
}

fn serve(mut a: Args) -> Result<(), String> {
    let port: u16 = a.number("--port", 9900)?;
    let select: DeviceSelect = a
        .take("--devices")
        .unwrap_or_else(|| "netns".into())
        .parse()?;
    let slots: usize = a.number("--slots", 4096)?;
    let slot_bytes: usize = a.number("--slot-bytes", 3_080_192)?;
    let max_per_identity: usize = a.number("--max-per-identity", 256)?;
    let verify = !a.switch("--no-verify");
    let ready = a.take("--ready-file");
    a.done()?;
    let creds = credentials()?;
    let devices = open_devices(&select)?;
    if devices.is_empty() {
        return Err("no RDMA device selected".into());
    }
    let cache = KvCache::new(KvCacheConfig {
        slot_bytes,
        slots,
        shards: 64,
        max_pending: Duration::from_secs(10),
    })
    .map_err(|e| format!("KV cache: {e}"))?;
    let (key, secret) = (creds.access_key.clone(), creds.secret_key.clone());
    let verifier: HandshakeVerifier = {
        let (key, secret) = (key.clone(), secret.clone());
        Arc::new(move |h: &HandshakeAuth| {
            if h.access_key == key && h.verify(&secret) {
                PeerRole::Client
            } else {
                PeerRole::Denied
            }
        })
    };
    // One tenant: the bench's key may use every namespace.
    let auth: KvAuthFn = Arc::new(move |c: &KvAuthContext| {
        if c.header.access_key != key {
            return Err(AuthError::UnknownKey);
        }
        if !verify_signature(
            &secret,
            c.header.timestamp,
            c.header.nonce,
            c.body,
            &c.header.signature,
        ) {
            return Err(AuthError::BadSignature);
        }
        Ok(key.clone())
    });
    let cap = ConnectionCap::new(max_per_identity);
    let mut transports = Vec::new();
    let mut gids = Vec::new();
    for (i, dev) in devices.into_iter().enumerate() {
        let dev = Arc::new(dev);
        // SAFETY: every ibv_gid variant is 16 plain bytes.
        gids.push(
            dev.gid(i32::from(dev.roce_gid_index()))
                .map(|g| hex(&unsafe { g.raw }))
                .unwrap_or_default(),
        );
        let rail_port = port.checked_add(i as u16).ok_or("port + rail overflows")?;
        transports.push((
            Arc::new(kv_transport(dev, Arc::clone(&verifier), &cap)?),
            rail_port,
        ));
    }
    let shared = Shared::new(
        cache,
        auth,
        KvServerConfig {
            verify_puts: verify,
            ..KvServerConfig::default()
        },
    )
    .map_err(|e| e.to_string())?;
    let (server, addrs) = KvServer::start(shared, transports)?;
    let lines: Vec<String> = addrs
        .iter()
        .zip(&gids)
        .enumerate()
        .map(|(i, (addr, gid))| format!("{i} {} {gid}", addr.port()))
        .collect();
    eprintln!(
        "nvstor-kv: serving {} rails: {}",
        lines.len(),
        lines.join("; ")
    );
    if let Some(path) = ready {
        let tmp = format!("{path}.tmp");
        std::fs::write(&tmp, lines.join("\n") + "\n").map_err(|e| format!("{tmp}: {e}"))?;
        std::fs::rename(&tmp, &path).map_err(|e| format!("{path}: {e}"))?;
    }
    // Serve until killed.
    loop {
        std::thread::sleep(Duration::from_secs(60));
        eprintln!(
            "nvstor-kv: {} sessions, {:?}",
            server.sessions(),
            server.cache().stats()
        );
    }
}

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// A block key for index `i` of the bench's data set.
fn key_of(i: u64) -> Key {
    let mut k = [0u8; 36];
    k[..8].copy_from_slice(&i.to_le_bytes());
    k[8..].copy_from_slice(b"nvstor-kv-bench-block-key---");
    Key::new(&k).expect("invariant: 36-byte key")
}

/// One connection's landing pool: `batch` slots laid out as vLLM lays out
/// its KV cache: per tensor, `batch` pages back to back.
struct Landing {
    /// Offset of the pool in the GPU buffer.
    base: usize,
    batch: u32,
    pages: Vec<u32>,
}

impl Landing {
    fn bytes(&self) -> usize {
        self.pages.iter().map(|&p| p as usize).sum::<usize>() * self.batch as usize
    }
    fn layout(&self, iova: u64, rkey: u32) -> Layout {
        let mut at = (iova + self.base as u64, Vec::new());
        for &p in &self.pages {
            at.1.push(TensorRun {
                base: at.0,
                stride: u64::from(p),
                page: p,
            });
            at.0 += u64::from(p) * u64::from(self.batch);
        }
        Layout {
            layout_id: 1,
            rkey,
            num_blocks: self.batch,
            tensors: at.1,
        }
    }
    /// (offset in the GPU buffer, length) of slot `s`'s pages, in order.
    fn pieces(&self, s: u32) -> Vec<(usize, usize)> {
        let mut out = Vec::with_capacity(self.pages.len());
        let mut t_base = self.base;
        for &p in &self.pages {
            out.push((t_base + p as usize * s as usize, p as usize));
            t_base += p as usize * self.batch as usize;
        }
        out
    }
}

fn read_block(gpu: &GpuBuffer, landing: &Landing, slot: u32) -> Result<Vec<u8>, String> {
    let mut out = Vec::new();
    for (at, len) in landing.pieces(slot) {
        let start = out.len();
        out.resize(start + len, 0);
        gpu.copy_to_host(at, &mut out[start..])?;
    }
    Ok(out)
}

struct Totals {
    blocks: AtomicU64,
    bytes: AtomicU64,
    misses: AtomicU64,
    verified: AtomicU64,
    bad: AtomicU64,
}

fn bench(mut a: Args) -> Result<(), String> {
    let host = a.take("--server").ok_or("--server is required")?;
    let port: u16 = a.number("--port", 9900)?;
    let server_rails: usize = a.number("--rails", 0)?;
    let select: DeviceSelect = a
        .take("--devices")
        .unwrap_or_else(|| "netns".into())
        .parse()?;
    let gpu_index: u32 = a
        .take("--gpu")
        .ok_or("--gpu is required")?
        .parse()
        .map_err(|_| "--gpu: not a GPU index")?;
    let connections: usize = a.number("--connections", 16)?;
    let batch: u32 = a.number("--batch", 8)?;
    let blocks: u64 = a.number("--blocks", 2000)?;
    let warmup = Duration::from_secs(a.number("--warmup-s", 3)?);
    let duration = Duration::from_secs(a.number("--duration-s", 30)?);
    let verify = a.switch("--verify");
    let json = a.switch("--json");
    a.done()?;
    if server_rails == 0 || connections == 0 || batch == 0 || blocks == 0 {
        return Err("--rails, --connections, --batch and --blocks must be positive".into());
    }
    let creds = credentials()?;
    let rails = kv_rails(open_devices(&select)?, &creds)?;
    let pages = glm_pages();
    let block_bytes: usize = pages.iter().map(|&p| p as usize).sum();
    let landings: Vec<Landing> = (0..connections)
        .map(|c| Landing {
            base: c * block_bytes * batch as usize,
            batch,
            pages: pages.clone(),
        })
        .collect();
    let total = landings.iter().map(Landing::bytes).sum::<usize>();
    let gpu = Arc::new(GpuBuffer::alloc(gpu_index, total, Mapping::Default)?);
    let fd = gpu.dmabuf()?;
    // The server writes GETs into the pool and reads PUTs from it.
    let region = RailRegion::dmabuf_with(
        &rails,
        fd,
        0,
        total,
        gpu.device_ptr(),
        Arc::clone(&gpu) as Arc<dyn Send + Sync>,
        Access::REMOTE_READ_WRITE,
    )?;
    // Random contents: the PUT phase stores what is in the slots.
    let mut seed = 0x9E37_79B9_7F4A_7C15u64;
    let fill: Vec<u8> = (0..total)
        .map(|_| {
            seed ^= seed << 13;
            seed ^= seed >> 7;
            seed ^= seed << 17;
            seed as u8
        })
        .collect();
    gpu.copy_from_host(&fill, 0)?;

    let mut clients: Vec<KvClient<RailExchange>> = Vec::with_capacity(connections);
    for (c, landing) in landings.iter().enumerate() {
        let rail = c % server_rails;
        let addr = format!("{host}:{}", port + rail as u16);
        let link =
            connect_rail(&rails, &addr, rail, &[]).map_err(|e| format!("connect {addr}: {e}"))?;
        let rkey = region
            .rkey(link.rail())
            .ok_or("no rkey for the connection's NIC")?;
        let mut client = KvClient::new(link, creds.clone());
        client
            .register_layout(landing.layout(gpu.device_ptr(), rkey))
            .map_err(|e| e.to_string())?;
        clients.push(client);
    }

    // PUT: block i from slot i % batch of connection i % connections.
    let put_start = Instant::now();
    for (c, client) in clients.iter_mut().enumerate() {
        let mine: Vec<u64> = (0..blocks)
            .filter(|i| *i as usize % connections == c)
            .collect();
        for chunk in mine.chunks(batch as usize) {
            let entries = chunk
                .iter()
                .enumerate()
                .map(|(s, &i)| {
                    let payload: Vec<u8> = landings[c]
                        .pieces(s as u32)
                        .iter()
                        .flat_map(|&(at, len)| fill[at..at + len].to_vec())
                        .collect();
                    PutEntry {
                        key: key_of(i),
                        block: s as u32,
                        check: block_check(&NAMESPACE, key_of(i).as_bytes(), &payload),
                    }
                })
                .collect();
            let statuses = client
                .put(NAMESPACE, 1, 10_000, entries)
                .map_err(|e| e.to_string())?;
            if let Some(bad) = statuses
                .iter()
                .find(|&&s| s != STATUS_OK && s != STATUS_EXISTS)
            {
                return Err(format!("PUT refused a block: status {bad}"));
            }
        }
    }
    let put_secs = put_start.elapsed().as_secs_f64();

    // GET: every connection on its own thread, batches of random blocks.
    let totals = Arc::new(Totals {
        blocks: AtomicU64::new(0),
        bytes: AtomicU64::new(0),
        misses: AtomicU64::new(0),
        verified: AtomicU64::new(0),
        bad: AtomicU64::new(0),
    });
    let measuring = Arc::new(AtomicBool::new(false));
    let start = Instant::now();
    let (t0, t1) = (start + warmup, start + warmup + duration);
    let landings = Arc::new(landings);
    let threads: Vec<_> = clients
        .into_iter()
        .enumerate()
        .map(|(c, mut client)| {
            let (gpu, totals, measuring, landings) = (
                Arc::clone(&gpu),
                Arc::clone(&totals),
                Arc::clone(&measuring),
                Arc::clone(&landings),
            );
            std::thread::spawn(move || -> Result<Vec<u64>, String> {
                let mut rng = 0x2545_F491_4F6C_DD1Du64 ^ (c as u64 + 1).wrapping_mul(0x9E37_79B9);
                let mut lat = Vec::new();
                loop {
                    let now = Instant::now();
                    if now >= t1 {
                        return Ok(lat);
                    }
                    let entries: Vec<GetEntry> = (0..batch)
                        .map(|s| {
                            rng ^= rng << 13;
                            rng ^= rng >> 7;
                            rng ^= rng << 17;
                            GetEntry {
                                key: key_of(rng % blocks),
                                block: s,
                            }
                        })
                        .collect();
                    let keys: Vec<Key> = entries.iter().map(|e| e.key).collect();
                    let results = client
                        .get(NAMESPACE, 1, 1000, entries)
                        .map_err(|e| e.to_string())?;
                    if verify {
                        // Once per batch: the reply came after every WRITE completed.
                        gpu.make_visible()?;
                    }
                    let mut ok = 0u64;
                    for (s, r) in results.iter().enumerate() {
                        if r.status != STATUS_OK {
                            totals.misses.fetch_add(1, Ordering::Relaxed);
                            continue;
                        }
                        ok += 1;
                        if verify {
                            let landed = read_block(&gpu, &landings[c], s as u32)?;
                            if block_check(&NAMESPACE, keys[s].as_bytes(), &landed) == r.check {
                                totals.verified.fetch_add(1, Ordering::Relaxed);
                            } else {
                                totals.bad.fetch_add(1, Ordering::Relaxed);
                            }
                        }
                    }
                    let end = Instant::now();
                    if now >= t0 && end <= t1 {
                        measuring.store(true, Ordering::Relaxed);
                        totals.blocks.fetch_add(ok, Ordering::Relaxed);
                        totals
                            .bytes
                            .fetch_add(ok * block_bytes as u64, Ordering::Relaxed);
                        lat.push((end - now).as_micros() as u64);
                    }
                }
            })
        })
        .collect();
    let mut lat = Vec::new();
    for t in threads {
        lat.extend(t.join().map_err(|_| "a bench thread panicked")??);
    }
    lat.sort_unstable();
    let pct = |q: f64| {
        lat.get(((lat.len() as f64 * q) as usize).min(lat.len().saturating_sub(1)))
            .copied()
            .unwrap_or(0)
    };
    let secs = duration.as_secs_f64();
    let (n, bytes) = (
        totals.blocks.load(Ordering::Relaxed),
        totals.bytes.load(Ordering::Relaxed),
    );
    let line = format!(
        "{{\"gpu\":{gpu_index},\"connections\":{connections},\"batch\":{batch},\"blocks\":{blocks},\"block_bytes\":{block_bytes},\
\"put_s\":{put_secs:.2},\"loaded\":{n},\"blocks_per_s\":{:.1},\"gib_per_s\":{:.3},\"batch_p50_us\":{},\"batch_p99_us\":{},\
\"batch_max_us\":{},\"misses\":{},\"verify\":{verify},\"verified\":{},\"bad\":{},\"nics\":\"{}\"}}",
        n as f64 / secs, bytes as f64 / secs / (1u64 << 30) as f64, pct(0.50), pct(0.99), lat.last().copied().unwrap_or(0),
        totals.misses.load(Ordering::Relaxed), totals.verified.load(Ordering::Relaxed), totals.bad.load(Ordering::Relaxed),
        rails.opened().iter().map(|(d, k)| format!("{d}={k}")).collect::<Vec<_>>().join(","));
    if json {
        println!("{line}");
    } else {
        eprintln!("nvstor-kv bench: {line}");
    }
    if totals.bad.load(Ordering::Relaxed) > 0 {
        return Err("blocks failed their check".into());
    }
    drop(region);
    Ok(())
}
