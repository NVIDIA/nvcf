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

//! nvstor-peer: copy a directory from one node to another over RDMA.
//!
//!   nvstor-peer serve --root DIR [--export-as PREFIX] [--port P] [--listen ADDR] [--devices SEL] [--max-fetchers K]
//!                     [--slot-mib N] [--slots N] [--max-in-flight N] [--ready-file PATH]
//!   nvstor-peer fetch --source HOST:PORT [--source HOST:PORT ...] --dest DIR [--prefix SUBDIR]
//!                     [--connections N] [--range-mib N] [--devices SEL] [--no-direct]
//!                     [--max-manifest-mib N] [--wait-s S] [--client-name NAME] [--ready-file PATH]
//!                     [--no-attrs | --no-owner] [--preserve-setid]
//!                     [--json] [--then-serve [--port P] [--listen ADDR] [--max-fetchers K] [--slot-mib N] [--slots N]
//!                                   [--max-in-flight N]]
//!                     [--gpu N [--gpu-mapping default|pcie] [--gpu-ready-file PATH] [--gpu-check]]
//!   nvstor-peer --version
//!   nvstor-peer bench --source HOST:PORT [--source ...] [--prefix SUBDIR] [--connections N]
//!                     [--range-kib N] [--pattern seq|rand] [--warmup-s S] [--duration-s S]
//!                     [--devices SEL] [--max-manifest-mib N] [--client-name NAME] [--json]
//!
//! `serve` exports DIR read-only on every selected RDMA device (one rail
//! each; rail k's bootstrap listens on P + k), to at most K fetchers at
//! once; more are told BUSY in the bootstrap, before any QP exists.
//! `--export-as PREFIX` serves DIR as the directory PREFIX (what a
//! replica that fetched `--prefix PREFIX` into DIR does).
//!
//! `fetch` tries its sources (`--from` is an alias of `--source`; a name
//! that resolves to several addresses is several sources) in an order
//! shuffled by the client name, moves on from a busy or unreachable one,
//! backs off with jitter while all are busy, for at most `--wait-s`, and
//! continues from another source if the one in use fails mid-copy (same
//! size and content identity required). It spreads its connections over
//! the source's rails, each on the local NIC that reaches it best, and
//! copies SUBDIR (or everything) into DEST. One fetch at a time per DEST;
//! a second one fails at once. Files the transport cannot request (paths
//! too long for a key) are listed and the fetch exits nonzero after
//! copying everything else. `--then-serve` serves DEST after a complete
//! fetch, under SUBDIR, so the copy becomes a source for the next
//! fetchers. `--ready-file` is written once the copy is complete (and,
//! with `--then-serve` or for `serve`, being served).
//!
//! `fetch --gpu N` lands every range in GPU N's memory first (GPUDirect
//! RDMA), checks it there and places it at its file's offset in one GPU
//! buffer; the disk copy in DEST follows in the background, read back from
//! the GPU (so the GPU copy never waits for the disk), with the same
//! staging, fsync and rename as any fetch. Once every file is in
//! GPU memory, before the disk copy is done, `--gpu-ready-file` is written:
//! one JSON object with the buffer's CUDA IPC handle (`ipc_handle`, for
//! `cuIpcOpenMemHandle`) and each file's `path`, `offset` and `size`. When
//! the disk copy is complete the summary (or JSON line) is printed and the
//! process holds the GPU memory until it is stopped (SIGTERM), also when
//! the disk copy failed after the ready file was written (the JSON line
//! says so); without `--gpu-ready-file` nothing can open the memory and
//! the process exits. A fetch that cannot copy every listed file is
//! refused before any transfer.
//! `--gpu-check` compares every file's GPU copy with its disk copy first
//! (a mismatch exits 5). See `docs/design/fetch-gpu.md`.
//!
//! `fetch` gives every file and directory it creates the mode and mtime
//! the source lists, and the numeric owner and group too when it has
//! CAP_CHOWN, CAP_DAC_OVERRIDE and CAP_FOWNER (otherwise, or with
//! `--no-owner`, the files are the caller's and the summary names what
//! was missing). Set-user-ID and set-group-ID bits
//! are dropped unless `--preserve-setid`, and a directory's write for
//! others unless it is sticky. Empty directories are created. A source
//! that serves no attributes (an older nvstor-peer) is refused: upgrade
//! sources first. `--no-attrs` copies bytes only and leaves files 0600
//! and directories 0700. A source that is itself a copy made without
//! owners or modes says so, and they are not passed on.
//!
//! `fetch` exits 0 when the copy is complete, 2 on a usage error, 3 when
//! every source refused the credentials, 4 when no source could serve it
//! (busy, unreachable, gone, unusable manifest: try the next tier), 5 on a
//! data or integrity failure (CRC mismatch after retries, a file changed
//! on the source, sources disagree), 6 on a destination error (I/O, disk
//! full, permission, locked by another fetch), 1 otherwise
//! (`nvstor_peer::outcome`). With `--json` it prints exactly one JSON
//! object on stdout at the end, on failure too, and the human summary goes
//! to stderr. `serve` and `bench` exit 2 on a usage error, 1 on any other.
//! `--version` prints the version and the git commit it was built from.
//!
//! `bench` measures the fetch data path of a `serve` without a
//! destination: `--connections` workers read ranges of the files under
//! SUBDIR (`seq`: one shared cursor, file by file; `rand`: uniformly
//! random) for `--duration-s` after `--warmup-s`, each range RDMA-written
//! into a registered buffer and CRC-checked, then discarded. It prints
//! GiB/s, ranges/s, latency percentiles and this process's CPU time
//! (`--json`: one JSON object). Any failed range voids the run (exit 1).
//! With `--gpu N` each range lands in GPU N's memory instead (GPUDirect
//! RDMA: the NIC writes into the GPU through a dma-buf, `--gpu-mapping`
//! picks how), and is CRC-checked over a copy to the host unless
//! `--no-gpu-verify` (which measures the NIC-to-GPU path alone).
//!
//! SEL is `netns` (default: devices whose address is on an interface in
//! this network namespace, i.e. a pod's own VFs), `all`, or a list such
//! as `mlx5_0,mlx5_1`. Both sides need the same secret in
//! NVSTOR_PEER_SECRET: a connector must prove it in the bootstrap before
//! the server creates any QP for it, and every request is HMAC-signed with
//! it. The server's memory is never readable or writable by a peer.

use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::sync::Arc;
use std::time::{Duration, Instant};

use nvstor_peer::bench::{BenchOptions, BenchReport};
use nvstor_peer::fetch::{fetch_from, FetchOptions, FetchReport};
use nvstor_peer::fetch::{fetch_to_device, Layout, ToDevice};
use nvstor_peer::gpu::{GpuBuffer, GpuDevice, Mapping};
use nvstor_peer::outcome::GpuSummary;
use nvstor_peer::outcome::{json_str, ErrorKind, Summary, COMMIT, VERSION};
use nvstor_peer::rdma_sources::RdmaSources;
use nvstor_peer::server::{self, ServeOptions, Server, DEFAULT_MAX_FETCHERS};
use nvstor_peer::sessions;
use nvstor_rdma::auth::Credentials;
use nvstor_rdma::object::GetError;
use nvstor_rdma::rail::{DeviceSelect, Rails};
use nvstor_rdma::region::Landed;
use nvstor_rdma::region::RailRegion;
use nvstor_rdma::s3_rdma::RdmaSource;

const SECRET_ENV: &str = "NVSTOR_PEER_SECRET";
const USAGE: &str = "\
usage:
  nvstor-peer serve --root DIR [--export-as PREFIX] [--port 9890] [--listen ::] [--devices netns|all|DEV,...]
                    [--max-fetchers 4] [--slot-mib 16] [--slots 64] [--max-in-flight 256] [--ready-file PATH]
  nvstor-peer fetch --source HOST:PORT [--source HOST:PORT ...] --dest DIR [--prefix SUBDIR]
                    [--connections 32] [--range-mib 16] [--devices netns|all|DEV,...] [--no-direct]
                    [--max-manifest-mib 256] [--wait-s 1800] [--client-name NAME] [--ready-file PATH]
                    [--no-attrs | --no-owner] [--preserve-setid]
                    [--json] [--then-serve [--port 9890] [--listen ::] [--max-fetchers 4] [--slot-mib 16] [--slots 64]
                                  [--max-in-flight 256]]
                    [--gpu N [--gpu-mapping default|pcie] [--gpu-ready-file PATH] [--gpu-check]]
  nvstor-peer bench --source HOST:PORT [--source HOST:PORT ...] [--prefix SUBDIR] [--connections 32]
                    [--range-kib 16384] [--pattern seq|rand] [--warmup-s 3] [--duration-s 20]
                    [--devices netns|all|DEV,...] [--max-manifest-mib 256] [--client-name NAME] [--json]
                    [--gpu N [--gpu-mapping default|pcie] [--no-gpu-verify]]
  nvstor-peer --version
  (--from HOST:PORT is accepted as --source)
fetch exit codes: 0 complete, 2 usage, 3 credentials refused, 4 no usable source,
                  5 data/integrity, 6 destination, 1 other
both sides: NVSTOR_PEER_SECRET=<shared secret, at least 16 characters>";

fn main() -> ExitCode {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .with_writer(std::io::stderr)
        .init();
    let args: Vec<String> = std::env::args().skip(1).collect();
    ExitCode::from(run(&args))
}

/// Why a command failed, and so its exit code.
#[derive(Debug)]
struct Failure {
    kind: ErrorKind,
    message: String,
}

impl Failure {
    fn usage(message: impl Into<String>) -> Self {
        Self {
            kind: ErrorKind::Usage,
            message: message.into(),
        }
    }

    fn other(message: String) -> Self {
        Self {
            kind: ErrorKind::Other,
            message,
        }
    }
}

fn version_line() -> String {
    format!("nvstor-peer {VERSION} (commit {COMMIT})")
}

/// Run the command line; returns the exit code.
fn run(args: &[String]) -> u8 {
    let result = match args.first().map(String::as_str) {
        Some("--version") if args.len() == 1 => {
            println!("{}", version_line());
            Ok(())
        }
        Some("serve") => Flags::parse(&args[1..]).and_then(serve),
        Some("fetch") => return fetch_cmd(&args[1..]),
        Some("bench") => Flags::parse(&args[1..]).and_then(bench),
        _ => Err(Failure::usage(USAGE)),
    };
    match result {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("nvstor-peer: {}", e.message);
            e.kind.exit_code()
        }
    }
}

/// `--name value` pairs and bare `--flag`s.
struct Flags(Vec<(String, Option<String>)>);

impl Flags {
    const SWITCHES: &'static [&'static str] = &[
        "--no-direct",
        "--then-serve",
        "--json",
        "--no-attrs",
        "--no-owner",
        "--preserve-setid",
        "--no-gpu-verify",
        "--gpu-check",
    ];

    fn parse(args: &[String]) -> Result<Self, Failure> {
        let mut out = Vec::new();
        let mut it = args.iter();
        while let Some(a) = it.next() {
            if !a.starts_with("--") {
                return Err(Failure::usage(format!(
                    "unexpected argument {a:?}\n{USAGE}"
                )));
            }
            if Self::SWITCHES.contains(&a.as_str()) {
                out.push((a.clone(), None));
            } else {
                let v = it
                    .next()
                    .ok_or_else(|| Failure::usage(format!("{a} needs a value\n{USAGE}")))?;
                out.push((a.clone(), Some(v.clone())));
            }
        }
        Ok(Self(out))
    }

    fn take(&mut self, name: &str) -> Option<String> {
        let i = self.0.iter().position(|(n, _)| n == name)?;
        self.0.remove(i).1
    }

    /// Every value of a repeatable flag, in order.
    fn take_all(&mut self, name: &str) -> Vec<String> {
        let mut out = Vec::new();
        while let Some(v) = self.take(name) {
            out.push(v);
        }
        out
    }

    fn required(&mut self, name: &str) -> Result<String, Failure> {
        self.take(name)
            .ok_or_else(|| Failure::usage(format!("{name} is required\n{USAGE}")))
    }

    fn number<T: std::str::FromStr>(&mut self, name: &str, default: T) -> Result<T, Failure> {
        match self.take(name) {
            None => Ok(default),
            Some(v) => v
                .parse()
                .map_err(|_| Failure::usage(format!("{name}: not a number: {v:?}"))),
        }
    }

    fn switch(&mut self, name: &str) -> bool {
        match self.0.iter().position(|(n, _)| n == name) {
            Some(i) => {
                self.0.remove(i);
                true
            }
            None => false,
        }
    }

    fn devices(&mut self) -> Result<DeviceSelect, Failure> {
        self.take("--devices")
            .unwrap_or_else(|| "netns".into())
            .parse()
            .map_err(Failure::usage)
    }

    /// The serving flags shared by `serve` and `fetch --then-serve`.
    fn serve_options(
        &mut self,
        root: PathBuf,
        export_as: String,
        devices: DeviceSelect,
    ) -> Result<ServeOptions, Failure> {
        Ok(ServeOptions {
            root,
            export_as,
            port: self.number("--port", 9890)?,
            listen: match self.take("--listen") {
                None => std::net::Ipv6Addr::UNSPECIFIED.into(),
                Some(a) => a
                    .parse()
                    .map_err(|_| Failure::usage(format!("--listen: not an IP address: {a:?}")))?,
            },
            devices,
            slot_size: self
                .number::<usize>("--slot-mib", 16)?
                .checked_mul(1 << 20)
                .ok_or_else(|| Failure::usage("--slot-mib is too large"))?,
            slots: self.number("--slots", 64)?,
            max_in_flight: self.number("--max-in-flight", 256)?,
            max_fetchers: self.number("--max-fetchers", DEFAULT_MAX_FETCHERS)?,
        })
    }

    /// Error on anything not consumed (unknown or repeated flags).
    fn done(self) -> Result<(), Failure> {
        match self.0.first() {
            None => Ok(()),
            Some((n, _)) => Err(Failure::usage(format!(
                "unknown or repeated flag {n}\n{USAGE}"
            ))),
        }
    }
}

fn secret() -> Result<String, Failure> {
    let s = std::env::var(SECRET_ENV)
        .map_err(|_| Failure::usage(format!("{SECRET_ENV} is not set")))?;
    if s.len() < 16 {
        return Err(Failure::usage(format!(
            "{SECRET_ENV} must be at least 16 characters"
        )));
    }
    Ok(s)
}

/// Replace `path` atomically with `text` (a readiness marker).
fn write_ready(path: &Path, text: &str) -> Result<(), String> {
    let tmp = path.with_extension("tmp");
    let write = || -> std::io::Result<()> {
        let mut f = std::fs::File::create(&tmp)?;
        f.write_all(text.as_bytes())?;
        f.sync_all()?;
        std::fs::rename(&tmp, path)
    };
    write().map_err(|e| format!("{}: {e}", path.display()))
}

/// Remove a readiness marker left by an earlier run.
fn clear_ready(path: Option<&Path>) -> Result<(), String> {
    let Some(p) = path else { return Ok(()) };
    match std::fs::remove_file(p) {
        Err(e) if e.kind() != std::io::ErrorKind::NotFound => Err(format!("{}: {e}", p.display())),
        _ => Ok(()),
    }
}

/// A line of human output: stdout, or stderr when stdout carries `--json`.
fn say(to_stderr: bool, line: &str) {
    if to_stderr {
        eprintln!("{line}");
    } else {
        println!("{line}");
    }
}

fn announce(s: &Server, to_stderr: bool) {
    for (k, (dev, rail)) in s.rails.iter().enumerate() {
        say(
            to_stderr,
            &format!(
                "nvstor-peer: rail {k} {dev} gid {} port {}",
                std::net::Ipv6Addr::from(rail.gid),
                rail.port
            ),
        );
    }
    let first_port = s.rails.first().map(|(_, r)| r.port).unwrap_or_default();
    let as_prefix = if s.export.mount().is_empty() {
        String::new()
    } else {
        format!(" as {}", s.export.mount())
    };
    say(to_stderr, &format!("nvstor-peer: serving {}{as_prefix} read-only on {} rails, bootstrap port {first_port}, \
                             at most {} fetchers at once",
                            s.export.root().display(), s.rails.len(), s.cap.max()));
}

/// Serve until killed.
fn serve_forever(
    s: Server,
    ready: Option<&Path>,
    ready_text: &str,
    to_stderr: bool,
) -> Result<(), String> {
    let s = serving(s, ready, ready_text, to_stderr)?;
    park_forever(s)
}

/// Announce `s` and write the ready file: serving has started.
fn serving(
    s: Server,
    ready: Option<&Path>,
    ready_text: &str,
    to_stderr: bool,
) -> Result<Server, String> {
    announce(&s, to_stderr);
    if let Some(p) = ready {
        write_ready(p, ready_text)?;
    }
    Ok(s)
}

/// Keep serving until killed.
fn park_forever<T>(_keep: T) -> ! {
    loop {
        std::thread::park();
    }
}

fn serve(mut f: Flags) -> Result<(), Failure> {
    let root = PathBuf::from(f.required("--root")?);
    let export_as = f.take("--export-as").unwrap_or_default();
    let devices = f.devices()?;
    let opts = f.serve_options(root, export_as, devices)?;
    let ready = f.take("--ready-file").map(PathBuf::from);
    f.done()?;
    let secret = secret()?;
    clear_ready(ready.as_deref()).map_err(Failure::other)?;
    let s = server::start(&opts, &secret).map_err(Failure::other)?;
    serve_forever(s, ready.as_deref(), "serving\n", false).map_err(Failure::other)
}

/// This host's name: the default client name, which seeds the source
/// order so fetchers spread over the sources.
fn hostname() -> String {
    let mut buf = [0u8; 256];
    // SAFETY: the pointer and length name `buf`.
    if unsafe { libc::gethostname(buf.as_mut_ptr().cast(), buf.len()) } != 0 {
        return String::new();
    }
    let n = buf.iter().position(|&b| b == 0).unwrap_or(buf.len());
    String::from_utf8_lossy(&buf[..n]).into_owned()
}

/// FNV-1a: a stable seed from the client name.
fn seed_of(name: &str) -> u64 {
    name.bytes().fold(0xcbf2_9ce4_8422_2325u64, |h, b| {
        (h ^ u64::from(b)).wrapping_mul(0x0100_0000_01b3)
    })
}

fn summary(r: &FetchReport, preserve_setid: bool, per_nic: &str) -> String {
    let secs = r.elapsed.as_secs_f64();
    let gib = r.bytes as f64 / (1u64 << 30) as f64;
    let setid = match (preserve_setid, r.setid_kept, r.setid_cleared) {
        (false, ..) => String::new(),
        (true, true, 0) => ", setid kept".into(),
        (true, true, n) => format!(", setid kept but cleared by the kernel on {n}"),
        (true, false, _) => ", setid dropped (owner not applied)".into(),
    };
    // A source rewritten mid-fetch: the copy holds the content the fetch
    // started from, finished from other sources.
    let changed = if r.changed_on.is_empty() {
        String::new()
    } else {
        format!("; files changed on {} during the fetch, finished from other sources with the original content",
                r.changed_on.join(", "))
    };
    format!("fetched {} files, {gib:.2} GiB in {secs:.1} s = {:.2} GiB/s; ranges {}, retries {}, reconnects {}, \
             failovers {}; source {}; content {}; attrs {}{setid}; connections per local NIC: {per_nic}{changed}",
            r.files, gib / secs.max(1e-9), r.ranges, r.retries, r.reconnects, r.failovers, r.source,
            r.content_digest, r.attributes.describe())
}

/// Refuse attribute switches that contradict each other, or that would
/// keep set-id bits on files the fetch owns.
fn check_attr_flags(attrs: bool, owners: bool, preserve_setid: bool) -> Result<(), String> {
    if !attrs && (preserve_setid || !owners) {
        return Err(format!("--no-attrs copies no attributes: --preserve-setid and --no-owner do not apply\n{USAGE}"));
    }
    if preserve_setid && !owners {
        return Err(format!(
            "--preserve-setid with --no-owner would leave set-id bits on files this fetch owns, \
                            handing out its privileges: drop one of them\n{USAGE}"
        ));
    }
    Ok(())
}

/// The end of a `fetch` run: what `--json` reports and, after a
/// complete copy with `--then-serve`, what to serve.
struct FetchEnd {
    summary: Summary,
    then_serve: Option<ThenServe>,
    /// `--gpu`: the GPU memory holding the files, held until the process
    /// is stopped.
    gpu: Option<GpuDevice>,
}

struct ThenServe {
    opts: ServeOptions,
    secret: String,
    ready: Option<PathBuf>,
    ready_text: String,
}

/// `fetch`: run it, report it (one JSON line with `--json`), serve the
/// copy with `--then-serve`. Returns the exit code.
fn fetch_cmd(args: &[String]) -> u8 {
    let json = args.iter().any(|a| a == "--json");
    let mut end = fetch_end(args, json);
    let gpu = end.gpu.take();
    // With --then-serve, serving starts before the report, so the one
    // JSON line says whether it did.
    let (summary, server) = then_serve(end, |t| {
        server::start(&t.opts, &t.secret)
            .and_then(|s| serving(s, t.ready.as_deref(), &t.ready_text, json))
    });
    if let Some((kind, message)) = &summary.failure {
        eprintln!("nvstor-peer: {message}");
        if *kind == ErrorKind::Usage {
            eprintln!("{USAGE}");
        }
    }
    if json {
        println!("{}", summary.to_json());
    }
    match (server, gpu) {
        (Some(s), gpu) => park_forever((s, gpu)),
        // Held even after a failure: the ready file told a consumer the
        // files were there. The JSON line says what failed.
        (None, Some(gpu)) => {
            eprintln!(
                "nvstor-peer: holding the copy in GPU {}'s memory until stopped{}",
                gpu.gpu(),
                if summary.ok() {
                    ""
                } else {
                    " (the disk copy failed)"
                }
            );
            park_forever(gpu)
        }
        (None, None) => summary.exit_code(),
    }
}

/// After a complete copy with `--then-serve`, start serving with `start`;
/// a failure to start (or to write the ready file) becomes the run's
/// failure, with the copy's counts kept. Returns the final summary and
/// the server to keep running.
fn then_serve<S>(
    end: FetchEnd,
    start: impl FnOnce(&ThenServe) -> Result<S, String>,
) -> (Summary, Option<S>) {
    let mut summary = end.summary;
    let Some(t) = end.then_serve.filter(|_| summary.ok()) else {
        return (summary, None);
    };
    match start(&t) {
        Ok(s) => (summary, Some(s)),
        Err(e) => {
            summary.failure = Some((
                ErrorKind::Other,
                format!("the copy is complete, but --then-serve failed: {e}"),
            ));
            (summary, None)
        }
    }
}

/// Parse and run a fetch; every failure, a usage error included, ends up
/// in the summary.
fn fetch_end(args: &[String], json: bool) -> FetchEnd {
    let started = Instant::now();
    Flags::parse(args)
        .and_then(|f| fetch(f, json, started))
        .unwrap_or_else(|e| {
            // The usage text, which flag errors carry after their first line,
            // goes to stderr, not into the JSON line.
            let message = match e.kind {
                ErrorKind::Usage => e.message.lines().next().unwrap_or_default().to_string(),
                _ => e.message,
            };
            FetchEnd {
                summary: Summary::failure(e.kind, message, started.elapsed()),
                then_serve: None,
                gpu: None,
            }
        })
}

fn fetch(mut f: Flags, json: bool, started: Instant) -> Result<FetchEnd, Failure> {
    let mut specs = f.take_all("--source");
    specs.extend(f.take_all("--from"));
    if specs.is_empty() {
        return Err(Failure::usage(format!("--source is required\n{USAGE}")));
    }
    let dest = PathBuf::from(f.required("--dest")?);
    let prefix = f
        .take("--prefix")
        .unwrap_or_default()
        .trim_end_matches('/')
        .to_string();
    let connections: usize = f.number("--connections", 32)?;
    let range = f
        .number::<usize>("--range-mib", 16)?
        .checked_mul(1 << 20)
        .ok_or_else(|| Failure::usage("--range-mib is too large"))?;
    let devices = f.devices()?;
    let direct_io = !f.switch("--no-direct");
    let attrs = !f.switch("--no-attrs");
    let owners = !f.switch("--no-owner");
    let preserve_setid = f.switch("--preserve-setid");
    check_attr_flags(attrs, owners, preserve_setid).map_err(Failure::usage)?;
    let max_manifest_mib: u64 = f.number("--max-manifest-mib", 256)?;
    if max_manifest_mib > u64::MAX >> 20 {
        return Err(Failure::usage(format!(
            "--max-manifest-mib {max_manifest_mib} is too large"
        )));
    }
    let wait = Duration::from_secs(f.number("--wait-s", 1800)?);
    let client_name = f.take("--client-name").unwrap_or_else(hostname);
    let ready = f.take("--ready-file").map(PathBuf::from);
    let gpu: Option<u32> = f
        .take("--gpu")
        .map(|v| {
            v.parse()
                .map_err(|_| Failure::usage(format!("--gpu: not a GPU index: {v:?}")))
        })
        .transpose()?;
    let mapping: Option<Mapping> = f
        .take("--gpu-mapping")
        .map(|v| v.parse().map_err(Failure::usage))
        .transpose()?;
    let gpu_ready = f.take("--gpu-ready-file").map(PathBuf::from);
    let gpu_check = f.switch("--gpu-check");
    if gpu.is_none() && (mapping.is_some() || gpu_ready.is_some() || gpu_check) {
        return Err(Failure::usage(format!(
            "--gpu-mapping, --gpu-ready-file and --gpu-check need --gpu\n{USAGE}"
        )));
    }
    f.switch("--json");
    let then_serve = f.switch("--then-serve");
    let serve_opts = if then_serve {
        Some(f.serve_options(dest.clone(), prefix.clone(), devices.clone())?)
    } else {
        for flag in [
            "--port",
            "--listen",
            "--max-fetchers",
            "--slot-mib",
            "--slots",
            "--max-in-flight",
        ] {
            if f.0.iter().any(|(n, _)| n == flag) {
                return Err(Failure::usage(format!(
                    "{flag} is a serving flag: use it with --then-serve\n{USAGE}"
                )));
            }
        }
        None
    };
    f.done()?;
    for s in &specs {
        nvstor_peer::rdma_sources::check_spec(s).map_err(Failure::usage)?;
    }
    let secret = secret()?;
    clear_ready(ready.as_deref()).map_err(Failure::other)?;
    clear_ready(gpu_ready.as_deref()).map_err(Failure::other)?;
    // One session for this whole fetch: every connection, on every rail
    // and every source, presents it, so each source counts it once.
    let session =
        sessions::new_session_id().map_err(|e| Failure::other(format!("session id: {e}")))?;
    let creds = Credentials::new(sessions::session_key(&session), &secret);

    // Each NIC may end up carrying every connection (when only one
    // reaches the server), plus the anchor and a failover's spare: size
    // every pool for it.
    let rails =
        Rails::open_selected(&devices, range, connections + 2, &creds).map_err(Failure::other)?;
    nvstor_rdma::rail::warn_strict_rp_filter(&rails.netdevs());
    let sources = RdmaSources::new(Arc::clone(&rails), specs, &prefix).map_err(Failure::usage)?;
    let opts = FetchOptions {
        connections,
        range_size: range,
        max_retries: 5,
        retry_backoff: Duration::from_millis(50),
        direct_io,
        max_manifest_bytes: max_manifest_mib << 20,
        wait,
        seed: seed_of(&client_name),
        attrs,
        owners,
        preserve_setid,
        ..FetchOptions::default()
    };
    let (result, device) = match gpu {
        None => (fetch_from(&sources, &prefix, &dest, &opts), None),
        Some(n) => {
            let ready_hook =
                |d: &GpuDevice, layout: &Layout, after: Duration| -> Result<(), String> {
                    match &gpu_ready {
                        Some(p) => write_ready(p, &gpu_ready_json(d, layout, after)?),
                        None => Ok(()),
                    }
                };
            let land = |s: &mut RdmaSource,
                        d: &GpuDevice,
                        slot: usize,
                        key: &str,
                        at: u64,
                        generation: u128,
                        len: usize|
             -> Result<Landed, GetError> {
                let dest = d
                    .slot(slot)
                    .ok_or_else(|| GetError::Transport(format!("no GPU landing slot {slot}")))?;
                s.fetch_to(key, at, generation, len, &dest)
            };
            let mapping = mapping.unwrap_or(Mapping::Default);
            let to = ToDevice {
                make: Box::new(|l: &Layout| GpuDevice::new(&rails, n, mapping, l)),
                land: &land,
                ready: &ready_hook,
            };
            fetch_to_device(&sources, &prefix, &dest, &opts, to)
        }
    };
    let per_nic: Vec<String> = rails
        .opened()
        .iter()
        .map(|(d, n)| format!("{d}={n}"))
        .collect();
    sources.close();
    rails.shutdown();
    let mut outcome = Summary::of(&result, started.elapsed());
    // Once the GPU copy was reported ready in `--gpu-ready-file` it is
    // held whatever happens to the disk copy: a consumer may be using it.
    // Without a ready file nothing can open it: the run ends (`hold`).
    let gpu = device.map(|(dr, d)| {
        outcome.gpu = Some(GpuSummary {
            device: d.gpu(),
            bytes: dr.layout.len(),
            ready_seconds: dr.ready_after.as_secs_f64(),
        });
        say(
            json,
            &format!(
                "nvstor-peer: every file was in GPU {}'s memory after {:.2} s",
                d.gpu(),
                dr.ready_after.as_secs_f64()
            ),
        );
        d
    });
    let hold = |gpu: Option<GpuDevice>| gpu.filter(|_| gpu_ready.is_some());
    let Ok(r) = result else {
        return Ok(FetchEnd {
            summary: outcome,
            then_serve: None,
            gpu: hold(gpu),
        });
    };
    say(
        json,
        &format!(
            "nvstor-peer: {}",
            summary(&r, preserve_setid, &per_nic.join(" "))
        ),
    );
    if let (Some(d), true) = (&gpu, gpu_check) {
        match check_gpu_copy(d, &dest) {
            Ok(()) => say(json, "nvstor-peer: the GPU copy matches the disk copy"),
            Err(e) => {
                outcome.failure = Some((ErrorKind::Integrity, e));
                return Ok(FetchEnd {
                    summary: outcome,
                    then_serve: None,
                    gpu: hold(gpu),
                });
            }
        }
    }
    let ready_text = format!(
        "content {}\nfiles {}\nbytes {}\n",
        r.content_digest, r.files, r.bytes
    );
    let then_serve = match serve_opts {
        None => {
            if let Some(p) = &ready {
                if let Err(e) = write_ready(p, &ready_text) {
                    // The copy is complete; the caller was told it would
                    // find the ready file.
                    outcome.failure = Some((ErrorKind::Other, e));
                }
            }
            None
        }
        Some(opts) => Some(ThenServe {
            opts,
            secret,
            ready,
            ready_text,
        }),
    };
    Ok(FetchEnd {
        summary: outcome,
        then_serve,
        gpu: hold(gpu),
    })
}

/// What `--gpu-ready-file` holds: one JSON object, one line.
fn gpu_ready_json(d: &GpuDevice, layout: &Layout, after: Duration) -> Result<String, String> {
    let handle = d.buffer().ipc_handle()?;
    let hex: String = handle.0.iter().map(|b| format!("{b:02x}")).collect();
    let files: Vec<String> = layout
        .files
        .iter()
        .map(|p| {
            format!(
                "{{\"path\":{},\"offset\":{},\"size\":{}}}",
                json_str(&p.path),
                p.offset,
                p.size
            )
        })
        .collect();
    Ok(format!("{{\"gpu\":{},\"ipc_handle\":\"{hex}\",\"bytes\":{},\"files_bytes\":{},\"ready_ns\":{},\"files\":[{}]}}\n",
               d.gpu(), layout.len(), layout.files_len, after.as_nanos(), files.join(",")))
}

/// `--gpu-check`: every file's GPU copy, read back, against its copy
/// under `dest`.
fn check_gpu_copy(d: &GpuDevice, dest: &Path) -> Result<(), String> {
    use std::io::Read;
    const CHUNK: usize = 16 << 20;
    let mut gpu_bytes = d.buffer().pinned(CHUNK)?;
    let mut disk = vec![0u8; CHUNK];
    for p in &d.layout().files {
        let path = dest.join(&p.path);
        let mut f = std::fs::File::open(&path).map_err(|e| format!("{}: {e}", path.display()))?;
        let mut done = 0u64;
        while done < p.size {
            let n = usize::try_from(p.size - done).map_or(CHUNK, |left| left.min(CHUNK));
            f.read_exact(&mut disk[..n])
                .map_err(|e| format!("{}: {e}", path.display()))?;
            let at = usize::try_from(p.offset + done).map_err(|_| format!("{}: offset", p.path))?;
            d.buffer().copy_to_host(at, &mut gpu_bytes[..n])?;
            if disk[..n] != gpu_bytes[..n] {
                let first = disk[..n]
                    .iter()
                    .zip(&gpu_bytes[..n])
                    .position(|(a, b)| a != b)
                    .unwrap_or(0);
                return Err(format!(
                    "{}: the GPU copy differs from the disk copy at byte {}",
                    p.path,
                    done + first as u64
                ));
            }
            done += n as u64;
        }
    }
    Ok(())
}

fn bench_summary(r: &BenchReport, per_nic: &str, into: &str) -> String {
    let us = |d: Duration| d.as_secs_f64() * 1e6;
    format!("bench {:?} into {into}, {} connections, {} KiB ranges, {:.1} s: {:.2} GiB/s, {:.0} ranges/s; \
             latency p50 {:.0} us, p99 {:.0} us, p99.9 {:.0} us, max {:.0} us; CPU {:.1} s ({:.2} GiB per CPU-s); \
             {} files, {:.2} GiB; source {}; connections per local NIC: {per_nic}",
            r.pattern, r.connections, r.range_size >> 10, r.elapsed.as_secs_f64(), r.gib_per_s(), r.ops_per_s(),
            us(r.latency.p50), us(r.latency.p99), us(r.latency.p999), us(r.latency.max),
            r.cpu.as_secs_f64(), r.gib_per_cpu_s(), r.files, r.dataset_bytes as f64 / (1u64 << 30) as f64, r.source)
}

fn bench_json(r: &BenchReport, per_nic: &[(String, usize)], into: &str) -> String {
    let ns = |d: Duration| d.as_nanos();
    let nics: Vec<String> = per_nic
        .iter()
        .map(|(d, n)| format!("{}:{n}", json_str(d)))
        .collect();
    format!("{{\"into\":{},\"pattern\":\"{}\",\"connections\":{},\"range_bytes\":{},\"elapsed_ns\":{},\"ops\":{},\"bytes\":{},\
             \"gib_per_s\":{:.4},\"ops_per_s\":{:.1},\"p50_ns\":{},\"p99_ns\":{},\"p999_ns\":{},\"max_ns\":{},\
             \"cpu_ns\":{},\"gib_per_cpu_s\":{:.4},\"files\":{},\"dataset_bytes\":{},\"source\":{},\
             \"connections_per_nic\":{{{}}}}}",
            json_str(into),
            match r.pattern { nvstor_peer::bench::Pattern::Seq => "seq", nvstor_peer::bench::Pattern::Rand => "rand" },
            r.connections, r.range_size, ns(r.elapsed), r.ops, r.bytes, r.gib_per_s(), r.ops_per_s(),
            ns(r.latency.p50), ns(r.latency.p99), ns(r.latency.p999), ns(r.latency.max),
            ns(r.cpu), r.gib_per_cpu_s(), r.files, r.dataset_bytes, json_str(&r.source), nics.join(","))
}

fn bench(mut f: Flags) -> Result<(), Failure> {
    let mut specs = f.take_all("--source");
    specs.extend(f.take_all("--from"));
    if specs.is_empty() {
        return Err(Failure::usage(format!("--source is required\n{USAGE}")));
    }
    let prefix = f
        .take("--prefix")
        .unwrap_or_default()
        .trim_end_matches('/')
        .to_string();
    let connections: usize = f.number("--connections", 32)?;
    let range = f
        .number::<usize>("--range-kib", 16 << 10)?
        .checked_mul(1 << 10)
        .ok_or_else(|| Failure::usage("--range-kib is too large"))?;
    let pattern = f
        .take("--pattern")
        .unwrap_or_else(|| "seq".into())
        .parse()
        .map_err(Failure::usage)?;
    let warmup = Duration::from_secs(f.number("--warmup-s", 3)?);
    let duration = Duration::from_secs(f.number("--duration-s", 20)?);
    let devices = f.devices()?;
    let max_manifest_mib: u64 = f.number("--max-manifest-mib", 256)?;
    if max_manifest_mib > u64::MAX >> 20 {
        return Err(Failure::usage(format!(
            "--max-manifest-mib {max_manifest_mib} is too large"
        )));
    }
    let client_name = f.take("--client-name").unwrap_or_else(hostname);
    let json = f.switch("--json");
    let gpu: Option<u32> = f
        .take("--gpu")
        .map(|v| {
            v.parse()
                .map_err(|_| Failure::usage(format!("--gpu: not a GPU index: {v:?}")))
        })
        .transpose()?;
    let mapping: Option<Mapping> = f
        .take("--gpu-mapping")
        .map(|v| v.parse().map_err(Failure::usage))
        .transpose()?;
    let gpu_verify = !f.switch("--no-gpu-verify");
    f.done()?;
    if gpu.is_none() && (mapping.is_some() || !gpu_verify) {
        return Err(Failure::usage(format!(
            "--gpu-mapping and --no-gpu-verify need --gpu\n{USAGE}"
        )));
    }
    for s in &specs {
        nvstor_peer::rdma_sources::check_spec(s).map_err(Failure::usage)?;
    }
    let opts = BenchOptions {
        connections,
        range_size: range,
        pattern,
        warmup,
        duration,
        seed: seed_of(&client_name),
        max_manifest_bytes: max_manifest_mib << 20,
    };
    opts.check().map_err(Failure::usage)?;
    let secret = secret()?;
    // One session, as for a fetch: the source counts this bench once.
    let session =
        sessions::new_session_id().map_err(|e| Failure::other(format!("session id: {e}")))?;
    let creds = Credentials::new(sessions::session_key(&session), &secret);
    // Per NIC: every worker, the anchor and the manifest's connection.
    let rails =
        Rails::open_selected(&devices, range, connections + 2, &creds).map_err(Failure::other)?;
    nvstor_rdma::rail::warn_strict_rp_filter(&rails.netdevs());
    let sources = RdmaSources::new(Arc::clone(&rails), specs, &prefix).map_err(Failure::usage)?;
    let (result, into) = match gpu {
        None => (
            nvstor_peer::bench::bench(&sources, &prefix, &opts),
            "host memory".to_string(),
        ),
        Some(n) => {
            let mapping = mapping.unwrap_or(Mapping::Default);
            let into = format!(
                "GPU {n} ({mapping:?} mapping, {})",
                if gpu_verify {
                    "CRC-checked over a copy"
                } else {
                    "unchecked"
                }
            );
            (
                bench_gpu(&sources, &rails, &prefix, &opts, n, mapping, gpu_verify),
                into,
            )
        }
    };
    let per_nic = rails.opened();
    sources.close();
    rails.shutdown();
    let r = result.map_err(|e| Failure::other(e.to_string()))?;
    if json {
        println!("{}", bench_json(&r, &per_nic, &into));
    } else {
        let nics: Vec<String> = per_nic.iter().map(|(d, n)| format!("{d}={n}")).collect();
        println!("nvstor-peer: {}", bench_summary(&r, &nics.join(" "), &into));
    }
    Ok(())
}

/// `bench --gpu`: each worker owns one range-sized slice of a buffer on
/// GPU `gpu`, registered on every NIC, and the server writes its ranges
/// there. With `verify`, every range is made visible to the GPU, copied
/// back into the worker's pinned host buffer on its own stream and
/// CRC-checked, inside the measured operation.
fn bench_gpu(
    sources: &RdmaSources,
    rails: &Arc<Rails>,
    prefix: &str,
    opts: &BenchOptions,
    gpu: u32,
    mapping: Mapping,
    verify: bool,
) -> Result<BenchReport, nvstor_peer::bench::BenchError> {
    use nvstor_peer::bench::BenchError;
    use nvstor_rdma::object::GetError;
    let len = opts
        .connections
        .checked_mul(opts.range_size)
        .ok_or_else(|| BenchError::Options("connections x range size overflows".into()))?;
    let buf = Arc::new(GpuBuffer::alloc(gpu, len, mapping).map_err(BenchError::Open)?);
    let fd = buf.dmabuf().map_err(BenchError::Open)?;
    // The region holds the buffer: the NICs lose access before it is freed.
    let region = RailRegion::dmabuf(
        rails,
        fd,
        0,
        buf.len(),
        buf.device_ptr(),
        Arc::clone(&buf) as Arc<dyn Send + Sync>,
    )
    .map_err(BenchError::Open)?;
    let region = &region;
    nvstor_peer::bench::bench_with(sources, prefix, opts, |w, range| {
        let at = w * opts.range_size;
        let slice = region
            .slice(at, range)
            .ok_or_else(|| format!("worker {w}: no room for its slice"))?;
        let mut host = if verify {
            Some(buf.pinned(range)?)
        } else {
            None
        };
        let buf = Arc::clone(&buf);
        Ok(
            move |src: &mut nvstor_rdma::s3_rdma::RdmaSource,
                  path: &str,
                  offset,
                  generation,
                  len| {
                let landed = src.fetch_to(path, offset, generation, len, &slice)?;
                let n = landed.read.len;
                if let Some(host) = host.as_mut() {
                    buf.make_visible().map_err(GetError::Transport)?;
                    buf.copy_to_host(at, &mut host[..n])
                        .map_err(GetError::Transport)?;
                    landed.verify(&host[..n])?;
                }
                Ok(n)
            },
        )
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use nvstor_peer::bench::{Latency, Pattern};
    use nvstor_peer::fetch::Applied;

    fn report(attributes: nvstor_peer::fetch::Attributes) -> FetchReport {
        FetchReport {
            files: 3,
            bytes: 1 << 30,
            ranges: 64,
            retries: 0,
            reconnects: 0,
            source: "[fd00::1]:9890".into(),
            failovers: 0,
            changed_on: vec![],
            content_digest: "ab".into(),
            attributes,
            setid_kept: attributes.owners == Applied::Yes,
            setid_cleared: 0,
            elapsed: Duration::from_secs(1),
        }
    }

    #[test]
    fn the_summary_says_which_attributes_were_applied() {
        use nvstor_peer::fetch::Attributes;
        let a = |modes, owners| report(Attributes { modes, owners });
        for (r, setid, want) in [
            (
                a(Applied::Yes, Applied::Yes),
                true,
                "attrs mode+mtime+owner, setid kept;",
            ),
            (
                a(Applied::Yes, Applied::NoCapability("CAP_CHOWN")),
                false,
                "attrs mode+mtime (owner kept: no CAP_CHOWN);",
            ),
            (
                a(Applied::Yes, Applied::NoCapability("CAP_DAC_OVERRIDE")),
                false,
                "attrs mode+mtime (owner kept: no CAP_DAC_OVERRIDE);",
            ),
            (
                a(Applied::Yes, Applied::NoCapability("CAP_CHOWN")),
                true,
                "attrs mode+mtime (owner kept: no CAP_CHOWN), setid dropped (owner not applied);",
            ),
            (
                a(Applied::Yes, Applied::Off),
                false,
                "attrs mode+mtime (owner kept: --no-owner);",
            ),
            (
                a(Applied::Yes, Applied::NotAuthoritative),
                false,
                "attrs mode+mtime (owner kept: owner not authoritative at source);",
            ),
            (
                a(Applied::Off, Applied::Off),
                false,
                "attrs none (--no-attrs; files 0600, directories 0700);",
            ),
            (
                a(Applied::NotAuthoritative, Applied::NotAuthoritative),
                false,
                "attrs none (not authoritative at source; files 0600, directories 0700);",
            ),
        ] {
            let s = summary(&r, setid, "mlx5_0=4");
            assert!(s.contains(want), "{want} not in {s}");
        }
        let cleared = FetchReport {
            setid_cleared: 2,
            ..a(Applied::Yes, Applied::Yes)
        };
        let s = summary(&cleared, true, "mlx5_0=4");
        assert!(
            s.contains("setid kept but cleared by the kernel on 2;"),
            "{s}"
        );
    }

    #[test]
    fn contradicting_attribute_switches_are_refused() {
        assert!(check_attr_flags(true, true, true).is_ok());
        assert!(check_attr_flags(true, false, false).is_ok());
        assert!(check_attr_flags(false, true, false).is_ok());
        let e = check_attr_flags(true, false, true).expect_err("--no-owner --preserve-setid");
        assert!(e.contains("--preserve-setid with --no-owner"), "{e}");
        assert!(check_attr_flags(false, true, true).is_err());
        assert!(check_attr_flags(false, false, false).is_err());
    }

    #[test]
    fn attribute_switches_parse() {
        let args = |v: &[&str]| v.iter().map(|x| x.to_string()).collect::<Vec<_>>();
        let mut f = Flags::parse(&args(&[
            "--no-attrs",
            "--dest",
            "d",
            "--preserve-setid",
            "--no-owner",
        ]))
        .expect("parse");
        assert!(f.switch("--no-attrs") && f.switch("--preserve-setid") && f.switch("--no-owner"));
        assert_eq!(f.take("--dest").as_deref(), Some("d"));
        f.done().expect("all consumed");
    }

    fn args(a: &[&str]) -> Vec<String> {
        a.iter().map(|s| s.to_string()).collect()
    }

    #[test]
    fn version_is_a_top_level_flag() {
        assert!(version_line().starts_with(&format!("nvstor-peer {VERSION} (commit ")));
        assert!(version_line().ends_with(&format!("{COMMIT})")));
        assert_eq!(run(&args(&["--version"])), 0);
        assert_eq!(
            run(&args(&["--version", "extra"])),
            2,
            "nothing may follow --version"
        );
        assert_eq!(run(&args(&[])), 2);
        assert_eq!(run(&args(&["frobnicate"])), 2);
    }

    #[test]
    fn usage_errors_exit_2_for_every_command() {
        assert_eq!(run(&args(&["serve"])), 2, "--root is required");
        assert_eq!(run(&args(&["serve", "--root"])), 2, "needs a value");
        assert_eq!(run(&args(&["bench", "--source", "nohostport"])), 2);
        assert_eq!(
            run(&args(&["fetch", "--dest", "/x"])),
            2,
            "--source is required"
        );
        assert_eq!(
            run(&args(&[
                "fetch", "--source", "h:1", "--dest", "/x", "--bogus", "1"
            ])),
            2
        );
        assert_eq!(
            run(&args(&[
                "fetch", "--source", "h:1", "--dest", "/x", "--port", "1"
            ])),
            2
        );
    }

    /// The JSON contract holds for failures before any copy: a usage
    /// error is one JSON line with exit code 2 and kind "usage".
    #[test]
    fn json_is_a_switch_and_reports_usage_errors() {
        let f = Flags::parse(&args(&["--json", "--dest", "/x"])).expect("parse");
        assert_eq!(
            f.0[0],
            ("--json".to_string(), None),
            "--json takes no value"
        );
        let end = fetch_end(&args(&["--json", "--dest", "/x"]), true);
        assert!(end.then_serve.is_none());
        let line = end.summary.to_json();
        assert!(
            line.starts_with("{\"ok\":false,\"exit_code\":2,") && !line.contains('\n'),
            "{line}"
        );
        assert!(
            line.contains("\"error\":\"--source is required\",\"error_kind\":\"usage\""),
            "the message without the usage text: {line}"
        );
        assert!(
            line.contains(&format!("\"commit\":{}", json_str(COMMIT))),
            "{line}"
        );
        // A bad --source spec is a usage error too, found before any RDMA.
        let end = fetch_end(
            &args(&["--json", "--source", "nohostport", "--dest", "/x"]),
            true,
        );
        assert_eq!(end.summary.exit_code(), 2);
    }

    fn complete() -> Summary {
        let mut r = report(nvstor_peer::fetch::Attributes {
            modes: Applied::Yes,
            owners: Applied::Yes,
        });
        r.files = 2;
        r.bytes = 10;
        Summary::complete(&r)
    }

    fn plan() -> ThenServe {
        ThenServe {
            opts: ServeOptions {
                root: "/r".into(),
                export_as: String::new(),
                port: 0,
                listen: std::net::Ipv6Addr::UNSPECIFIED.into(),
                devices: DeviceSelect::AllActive,
                slot_size: 1 << 20,
                slots: 1,
                max_in_flight: 1,
                max_fetchers: 1,
            },
            secret: "0123456789abcdef".into(),
            ready: None,
            ready_text: String::new(),
        }
    }

    // --then-serve: the JSON line is decided after serving starts. A
    // server that cannot start makes the run a failure (exit 1, counts
    // kept), never an "ok" line followed by a failed exit.
    #[test]
    fn then_serve_failure_is_part_of_the_report() {
        let (s, server) = then_serve(
            FetchEnd {
                summary: complete(),
                then_serve: Some(plan()),
                gpu: None,
            },
            |_| -> Result<(), String> { Err("bind: address in use".into()) },
        );
        assert!(server.is_none());
        assert_eq!(s.exit_code(), 1);
        let line = s.to_json();
        assert!(
            line.starts_with("{\"ok\":false,\"exit_code\":1,")
                && line.contains("address in use")
                && line.contains("\"files\":2"),
            "{line}"
        );
        // Started: the report stays ok and the server is kept.
        let (s, server) = then_serve(
            FetchEnd {
                summary: complete(),
                then_serve: Some(plan()),
                gpu: None,
            },
            |_| Ok(7),
        );
        assert_eq!((s.exit_code(), server), (0, Some(7)));
        // A failed copy never starts serving.
        let failed = Summary::failure(ErrorKind::Unavailable, "x".into(), Duration::ZERO);
        let (s, server) = then_serve(
            FetchEnd {
                summary: failed,
                then_serve: Some(plan()),
                gpu: None,
            },
            |_| -> Result<u8, String> { panic!("must not serve a failed copy") },
        );
        assert_eq!((s.exit_code(), server), (4, None));
        // Without --then-serve nothing starts.
        let (s, server) = then_serve(
            FetchEnd {
                summary: complete(),
                then_serve: None,
                gpu: None,
            },
            |_| -> Result<u8, String> { panic!("not asked to serve") },
        );
        assert_eq!((s.exit_code(), server), (0, None));
    }

    #[test]
    fn listen_defaults_to_dual_stack_and_can_be_restricted() {
        let mut f = Flags::parse(&args(&[])).expect("parse");
        let o = f
            .serve_options("/r".into(), String::new(), DeviceSelect::AllActive)
            .expect("options");
        assert_eq!(
            o.listen,
            std::net::IpAddr::V6(std::net::Ipv6Addr::UNSPECIFIED)
        );
        for (given, want) in [
            ("10.0.0.7", "10.0.0.7"),
            ("0.0.0.0", "0.0.0.0"),
            ("fd00::7", "fd00::7"),
        ] {
            let mut f = Flags::parse(&args(&["--listen", given])).expect("parse");
            let o = f
                .serve_options("/r".into(), String::new(), DeviceSelect::AllActive)
                .expect("options");
            assert_eq!(o.listen.to_string(), want);
        }
        let mut f = Flags::parse(&args(&["--listen", "host.example"])).expect("parse");
        assert!(f
            .serve_options("/r".into(), String::new(), DeviceSelect::AllActive)
            .is_err());
        // A serving flag: fetch takes it only with --then-serve.
        assert_eq!(
            run(&args(&[
                "fetch", "--source", "h:1", "--dest", "/x", "--listen", "::"
            ])),
            2
        );
    }

    #[test]
    fn json_strings_are_escaped() {
        assert_eq!(json_str("mlx5_0"), "\"mlx5_0\"");
        assert_eq!(json_str("a\"b\\c\nd"), "\"a\\\"b\\\\c\\u000ad\"");
    }

    #[test]
    fn bench_json_is_one_object_with_every_field() {
        let r = BenchReport {
            source: "[fd00::1]:9890".into(),
            files: 2,
            dataset_bytes: 3 << 30,
            connections: 8,
            range_size: 1 << 20,
            pattern: Pattern::Rand,
            ops: 1000,
            bytes: 1000 << 20,
            elapsed: Duration::from_secs(2),
            latency: Latency {
                p50: Duration::from_micros(100),
                p99: Duration::from_micros(200),
                p999: Duration::from_micros(300),
                max: Duration::from_micros(400),
            },
            cpu: Duration::from_millis(1500),
        };
        let j = bench_json(
            &r,
            &[("rocep1\"s0".into(), 4), ("rocep2s0".into(), 4)],
            "GPU 0",
        );
        assert!(
            j.starts_with('{') && j.ends_with('}') && !j.contains('\n'),
            "{j}"
        );
        for field in [
            "\"pattern\":\"rand\"",
            "\"connections\":8",
            "\"range_bytes\":1048576",
            "\"ops\":1000",
            "\"elapsed_ns\":2000000000",
            "\"p50_ns\":100000",
            "\"max_ns\":400000",
            "\"cpu_ns\":1500000000",
            "\"source\":\"[fd00::1]:9890\"",
            "\"connections_per_nic\":{\"rocep1\\\"s0\":4,\"rocep2s0\":4}",
            "\"gib_per_s\":0.4883",
            "\"into\":\"GPU 0\"",
        ] {
            assert!(j.contains(field), "{field} missing from {j}");
        }
        // Balanced braces outside strings.
        let (mut depth, mut in_str, mut esc) = (0i32, false, false);
        for c in j.chars() {
            match (in_str, esc, c) {
                (true, true, _) => esc = false,
                (true, false, '\\') => esc = true,
                (true, false, '"') => in_str = false,
                (false, _, '"') => in_str = true,
                (false, _, '{') => depth += 1,
                (false, _, '}') => depth -= 1,
                _ => {}
            }
        }
        assert_eq!((depth, in_str), (0, false));
    }
}
