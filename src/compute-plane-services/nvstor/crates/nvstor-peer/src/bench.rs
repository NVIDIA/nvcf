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

//! Read benchmark of a `serve` peer: the fetch data path without the
//! destination.
//!
//! [`bench`] reads the manifest of a prefix, then runs `connections`
//! workers, each on its own source connection (over RDMA: its own QPs, on
//! the local NIC that reaches its server rail, with one registered slot),
//! fetching ranges of the listed files for a fixed time. Every range goes
//! through [`RangeSource::fetch`] exactly as in a fetch: RDMA-written by
//! the server into the worker's slot, then CRC-32C and generation checked.
//! Nothing is written anywhere, so the number is the transport and the
//! server, not the destination disk.
//!
//! - Ranges are numbered over the whole data set, file by file. `Seq`
//!   hands them out from one shared cursor (all workers interleaved over
//!   the files in order, as a fetch does, wrapping at the end); `Rand`
//!   picks uniformly from a per-worker splitmix64 stream.
//! - Connections are opened before a barrier; then `warmup` runs
//!   unmeasured, then the measured window of `duration`. Only operations
//!   that start and end inside the window count, and process CPU time
//!   (`getrusage`) is sampled at the window's edges.
//! - Latency goes into a per-worker log-linear histogram (16 steps per
//!   power of two, so a reported percentile is within 6.25 % of the true
//!   value), merged after the run. The measuring loop takes no lock and
//!   allocates nothing.
//! - Any error ends the run with that error: a rate measured over failed
//!   or retried ranges is not the rate of the path.

use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Barrier;
use std::time::{Duration, Instant};

use nvstor_rdma::hydrate::RangeSource;
use parking_lot::Mutex;

use crate::fetch::{fetch_manifest, FetchOptions, OpenError, SourceSet};
use crate::manifest::Entry;

/// Which range each operation reads.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Pattern {
    /// Successive ranges from one shared cursor, file by file.
    Seq,
    /// Uniformly random ranges.
    Rand,
}

impl std::str::FromStr for Pattern {
    type Err = String;
    fn from_str(s: &str) -> Result<Self, String> {
        match s {
            "seq" => Ok(Pattern::Seq),
            "rand" => Ok(Pattern::Rand),
            _ => Err(format!("pattern {s:?}: expected seq or rand")),
        }
    }
}

#[derive(Clone, Debug)]
pub struct BenchOptions {
    /// Parallel workers, one source connection each.
    pub connections: usize,
    /// Bytes per range: a multiple of 4096, at most the source's slot.
    pub range_size: usize,
    pub pattern: Pattern,
    /// Unmeasured time after the barrier.
    pub warmup: Duration,
    /// Measured window.
    pub duration: Duration,
    /// Seeds the random ranges (each worker derives its own stream).
    pub seed: u64,
    /// Largest manifest accepted from the server.
    pub max_manifest_bytes: u64,
}

impl Default for BenchOptions {
    fn default() -> Self {
        Self {
            connections: 32,
            range_size: 16 << 20,
            pattern: Pattern::Seq,
            warmup: Duration::from_secs(3),
            duration: Duration::from_secs(20),
            seed: 0,
            max_manifest_bytes: 256 << 20,
        }
    }
}

impl BenchOptions {
    pub fn check(&self) -> Result<(), String> {
        if self.connections == 0 {
            return Err("connections must be at least 1".into());
        }
        if self.range_size == 0 || !self.range_size.is_multiple_of(4096) {
            return Err(format!(
                "range size {} is not a positive multiple of 4096",
                self.range_size
            ));
        }
        if self.duration.is_zero() {
            return Err("duration must be positive".into());
        }
        Ok(())
    }
}

/// Latency at a few percentiles, from the merged histogram.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Latency {
    pub p50: Duration,
    pub p99: Duration,
    pub p999: Duration,
    pub max: Duration,
}

#[derive(Clone, Debug)]
pub struct BenchReport {
    /// The source measured.
    pub source: String,
    /// Files and bytes in the data set read from.
    pub files: usize,
    pub dataset_bytes: u64,
    pub connections: usize,
    pub range_size: usize,
    pub pattern: Pattern,
    /// Ranges and bytes completed inside the measured window.
    pub ops: u64,
    pub bytes: u64,
    /// The measured window.
    pub elapsed: Duration,
    pub latency: Latency,
    /// Process CPU time (user + system) over the measured window.
    pub cpu: Duration,
}

impl BenchReport {
    pub fn gib_per_s(&self) -> f64 {
        self.bytes as f64 / (1u64 << 30) as f64 / self.elapsed.as_secs_f64().max(1e-9)
    }

    pub fn ops_per_s(&self) -> f64 {
        self.ops as f64 / self.elapsed.as_secs_f64().max(1e-9)
    }

    /// GiB moved per CPU-second of this process.
    pub fn gib_per_cpu_s(&self) -> f64 {
        self.bytes as f64 / (1u64 << 30) as f64 / self.cpu.as_secs_f64().max(1e-9)
    }
}

#[derive(Debug)]
pub enum BenchError {
    Options(String),
    /// No source could be opened.
    Open(String),
    Manifest(String),
    /// The prefix lists no non-empty file.
    Empty,
    /// A worker's range failed; the run is void.
    Range {
        worker: usize,
        path: String,
        offset: u64,
        error: String,
    },
}

impl std::fmt::Display for BenchError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            BenchError::Options(e) => write!(f, "options: {e}"),
            BenchError::Open(e) => write!(f, "no source could be opened: {e}"),
            BenchError::Manifest(e) => write!(f, "manifest: {e}"),
            BenchError::Empty => write!(f, "the prefix lists no non-empty file"),
            BenchError::Range {
                worker,
                path,
                offset,
                error,
            } => write!(f, "worker {worker}: {path} at {offset}: {error}"),
        }
    }
}

impl std::error::Error for BenchError {}

/// The data set as numbered ranges: range `i` of the set is range
/// `i - first[f]` of file `f`, where `first[f] <= i < first[f + 1]`.
struct Ranges<'e> {
    files: Vec<&'e Entry>,
    /// Index of each file's first range; one extra entry holds the total.
    first: Vec<u64>,
    range: u64,
}

impl<'e> Ranges<'e> {
    fn new(entries: &'e [Entry], range: usize) -> Self {
        let range = range as u64;
        let files: Vec<&Entry> = entries.iter().filter(|e| e.size > 0).collect();
        let mut first = Vec::with_capacity(files.len() + 1);
        let mut n = 0u64;
        for e in &files {
            first.push(n);
            n += e.size.div_ceil(range);
        }
        first.push(n);
        Self {
            files,
            first,
            range,
        }
    }

    fn total(&self) -> u64 {
        self.first.last().copied().unwrap_or(0)
    }

    /// (file, offset, length) of range `i` (taken modulo the total).
    fn get(&self, i: u64) -> (&'e Entry, u64, usize) {
        let i = i % self.total();
        // The last file whose first range is <= i.
        let f = self.first.partition_point(|&s| s <= i) - 1;
        let e = self.files[f];
        let offset = (i - self.first[f]) * self.range;
        let len = (e.size - offset).min(self.range) as usize;
        (e, offset, len)
    }
}

/// splitmix64.
fn mix(mut x: u64) -> u64 {
    x = x.wrapping_add(0x9E37_79B9_7F4A_7C15);
    x = (x ^ (x >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    x = (x ^ (x >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    x ^ (x >> 31)
}

/// Log-linear latency histogram over nanoseconds: values below 16 are
/// exact; above, each power of two is split into 16 steps.
const SUB_BITS: u32 = 4;
const SUB: usize = 1 << SUB_BITS;
const BUCKETS: usize = (64 - SUB_BITS as usize + 1) * SUB;

struct Histogram {
    counts: Box<[u64; BUCKETS]>,
    max: u64,
}

impl Histogram {
    fn new() -> Self {
        Self {
            counts: Box::new([0; BUCKETS]),
            max: 0,
        }
    }

    fn index(v: u64) -> usize {
        if v < SUB as u64 {
            return v as usize;
        }
        let exp = 63 - v.leading_zeros(); // >= SUB_BITS
        let sub = (v >> (exp - SUB_BITS)) as usize & (SUB - 1);
        (exp - SUB_BITS + 1) as usize * SUB + sub
    }

    /// The smallest value that falls in bucket `i`.
    fn lower(i: usize) -> u64 {
        if i < SUB {
            return i as u64;
        }
        let exp = (i / SUB) as u32 + SUB_BITS - 1;
        (1u64 << exp) | ((i % SUB) as u64) << (exp - SUB_BITS)
    }

    fn record(&mut self, ns: u64) {
        self.counts[Self::index(ns)] += 1;
        self.max = self.max.max(ns);
    }

    fn merge(&mut self, o: &Histogram) {
        for (a, b) in self.counts.iter_mut().zip(o.counts.iter()) {
            *a += b;
        }
        self.max = self.max.max(o.max);
    }

    /// The lower edge of the bucket holding the `q`-quantile, capped at
    /// the largest value seen.
    fn quantile(&self, q: f64) -> u64 {
        let total: u64 = self.counts.iter().sum();
        if total == 0 {
            return 0;
        }
        let rank = ((q * total as f64).ceil() as u64).clamp(1, total);
        let mut seen = 0u64;
        for (i, &c) in self.counts.iter().enumerate() {
            seen += c;
            if seen >= rank {
                return Self::lower(i).min(self.max);
            }
        }
        self.max
    }
}

/// User + system CPU time of this process.
fn process_cpu() -> Duration {
    // SAFETY: `ru` is a valid out-parameter for getrusage.
    let mut ru: libc::rusage = unsafe { std::mem::zeroed() };
    if unsafe { libc::getrusage(libc::RUSAGE_SELF, &mut ru) } != 0 {
        return Duration::ZERO;
    }
    let tv = |t: libc::timeval| Duration::new(t.tv_sec as u64, t.tv_usec as u32 * 1000);
    tv(ru.ru_utime) + tv(ru.ru_stime)
}

/// The first candidate that opens, and its connection.
fn first_source<C: SourceSet>(set: &C) -> Result<(String, C::Source), BenchError> {
    let mut last = String::from("no candidates (names did not resolve)");
    for name in set.candidates() {
        match set.open(&name) {
            Ok(s) => return Ok((name, s)),
            Err(OpenError::Busy(e)) => last = format!("{name}: busy: {e}"),
            Err(OpenError::Refused(e)) => last = format!("{name}: refused: {e}"),
            Err(OpenError::Failed(e)) => last = format!("{name}: {e}"),
        }
    }
    Err(BenchError::Open(last))
}

struct Worker {
    hist: Histogram,
    ops: u64,
    bytes: u64,
}

/// Read ranges under `prefix` from the first source of `set` that opens,
/// as described in the module docs.
pub fn bench<C: SourceSet>(
    set: &C,
    prefix: &str,
    opts: &BenchOptions,
) -> Result<BenchReport, BenchError> {
    bench_with(set, prefix, opts, |_, _| {
        Ok(|src: &mut C::Source, path: &str, offset, generation, len| {
            src.fetch(path, offset, generation, len).map(|b| b.len())
        })
    })
}

/// [`bench`], with each worker's operation made by `op(worker,
/// range_size)` before the clock starts: it reads one range (path,
/// offset, generation, length) through the source and returns the bytes
/// read, checked. A failure to make an operation voids the run.
pub fn bench_with<C, Op, Make>(
    set: &C,
    prefix: &str,
    opts: &BenchOptions,
    op: Make,
) -> Result<BenchReport, BenchError>
where
    C: SourceSet,
    Op: FnMut(
        &mut C::Source,
        &str,
        u64,
        u128,
        usize,
    ) -> Result<usize, nvstor_rdma::object::GetError>,
    Make: Fn(usize, usize) -> Result<Op, String> + Sync,
{
    opts.check().map_err(BenchError::Options)?;
    let (source, mut first) = first_source(set)?;
    let fopts = FetchOptions {
        max_manifest_bytes: opts.max_manifest_bytes,
        ..FetchOptions::default()
    };
    let (retries, reconnects) = (AtomicU64::new(0), AtomicU64::new(0));
    let listing = fetch_manifest(&mut first, prefix, &fopts, &retries, &reconnects)
        .map_err(|e| BenchError::Manifest(e.to_string()))?;
    let entries: Vec<Entry> = listing.files().cloned().collect();
    let range_size = opts.range_size.min(first.max_range());
    if range_size == 0 {
        return Err(BenchError::Options("the source takes no range".into()));
    }
    let ranges = Ranges::new(&entries, range_size);
    if ranges.total() == 0 {
        return Err(BenchError::Empty);
    }
    // Workers open their own connections (a source belongs to the thread
    // that opened it); the manifest's is not one of them.
    drop(first);

    let cursor = AtomicU64::new(0);
    let stop = AtomicBool::new(false);
    let failed: Mutex<Option<BenchError>> = Mutex::new(None);
    // `connected`: every worker has its connection (or failed to); `go`:
    // the clock has started.
    let connected = Barrier::new(opts.connections + 1);
    let go = Barrier::new(opts.connections + 1);
    let start = Mutex::new(None::<Instant>);
    let mut cpu = Duration::ZERO;
    let mut window = (Instant::now(), Instant::now());

    let workers = std::thread::scope(|scope| {
        let handles: Vec<_> = (0..opts.connections)
            .map(|w| {
                let (ranges, cursor, stop, failed, connected, go, start, source, op) = (
                    &ranges, &cursor, &stop, &failed, &connected, &go, &start, &source, &op,
                );
                scope.spawn(move || {
                    let mut me = Worker {
                        hist: Histogram::new(),
                        ops: 0,
                        bytes: 0,
                    };
                    let mut rng = mix(opts.seed ^ mix(w as u64 + 1));
                    // Connect and set up before the barrier, so neither is measured.
                    let opened = set
                        .open(source)
                        .map_err(|e| format!("{source}: {e}"))
                        .and_then(|s| op(w, range_size).map(|o| (s, o)));
                    connected.wait();
                    go.wait();
                    let (mut src, mut read) = match opened {
                        Ok(s) => s,
                        Err(e) => {
                            failed.lock().get_or_insert(BenchError::Open(e));
                            stop.store(true, Ordering::Relaxed);
                            return me;
                        }
                    };
                    let Some(t0) = *start.lock() else { return me };
                    let (measure_from, measure_to) =
                        (t0 + opts.warmup, t0 + opts.warmup + opts.duration);
                    while !stop.load(Ordering::Relaxed) {
                        let i = match opts.pattern {
                            Pattern::Seq => cursor.fetch_add(1, Ordering::Relaxed),
                            Pattern::Rand => {
                                rng = mix(rng);
                                rng
                            }
                        };
                        let (e, offset, len) = ranges.get(i);
                        let began = Instant::now();
                        if began >= measure_to {
                            break;
                        }
                        let got = match read(&mut src, &e.path, offset, e.generation, len) {
                            Ok(n) => n,
                            Err(error) => {
                                failed.lock().get_or_insert(BenchError::Range {
                                    worker: w,
                                    path: e.path.clone(),
                                    offset,
                                    error: error.to_string(),
                                });
                                stop.store(true, Ordering::Relaxed);
                                break;
                            }
                        };
                        let ended = Instant::now();
                        if began >= measure_from && ended <= measure_to {
                            me.hist.record(
                                u64::try_from((ended - began).as_nanos()).unwrap_or(u64::MAX),
                            );
                            me.ops += 1;
                            me.bytes += got as u64;
                        }
                    }
                    me
                })
            })
            .collect();

        connected.wait();
        // Every connection is up (or failed): start the clock.
        let t0 = Instant::now();
        *start.lock() = Some(t0);
        go.wait();
        window = (t0 + opts.warmup, t0 + opts.warmup + opts.duration);
        // Wait for `until`, but end early once a worker has voided the run.
        let wait = |until: Instant| {
            while !stop.load(Ordering::Relaxed) {
                let left = until.saturating_duration_since(Instant::now());
                if left.is_zero() {
                    return;
                }
                std::thread::sleep(left.min(Duration::from_millis(10)));
            }
        };
        wait(window.0);
        let cpu0 = process_cpu();
        wait(window.1);
        cpu = process_cpu().saturating_sub(cpu0);
        stop.store(true, Ordering::Relaxed);
        handles
            .into_iter()
            .map(|h| h.join().ok())
            .collect::<Vec<Option<Worker>>>()
    });

    if let Some(e) = failed.into_inner() {
        return Err(e);
    }
    let workers: Vec<Worker> = workers
        .into_iter()
        .enumerate()
        .map(|(w, r)| {
            r.ok_or_else(|| BenchError::Range {
                worker: w,
                path: String::new(),
                offset: 0,
                error: "worker thread panicked".into(),
            })
        })
        .collect::<Result<_, _>>()?;
    let mut hist = Histogram::new();
    let (mut ops, mut bytes) = (0u64, 0u64);
    for w in &workers {
        hist.merge(&w.hist);
        ops += w.ops;
        bytes += w.bytes;
    }
    let ns = Duration::from_nanos;
    Ok(BenchReport {
        source,
        files: ranges.files.len(),
        dataset_bytes: ranges.files.iter().map(|e| e.size).sum(),
        connections: opts.connections,
        range_size,
        pattern: opts.pattern,
        ops,
        bytes,
        elapsed: window.1 - window.0,
        latency: Latency {
            p50: ns(hist.quantile(0.50)),
            p99: ns(hist.quantile(0.99)),
            p999: ns(hist.quantile(0.999)),
            max: ns(hist.max),
        },
        cpu,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::manifest;
    use nvstor_rdma::hydrate::HydrateError;
    use nvstor_rdma::object::GetError;
    use std::sync::Arc;

    fn entry(path: &str, size: u64) -> Entry {
        Entry {
            path: path.into(),
            size,
            generation: 7,
            content: 7,
            ..Entry::default()
        }
    }

    #[test]
    fn ranges_number_the_data_set_file_by_file() {
        let es = [
            entry("a", 10_000),
            entry("empty", 0),
            entry("b", 4096),
            entry("c", 8193),
        ];
        let r = Ranges::new(&es, 4096);
        // a: 3 ranges, b: 1, c: 3; the empty file has none.
        assert_eq!(r.total(), 7);
        let got: Vec<(String, u64, usize)> = (0..7)
            .map(|i| {
                let (e, o, l) = r.get(i);
                (e.path.clone(), o, l)
            })
            .collect();
        assert_eq!(
            got,
            [
                ("a".into(), 0, 4096),
                ("a".into(), 4096, 4096),
                ("a".into(), 8192, 1808),
                ("b".into(), 0, 4096),
                ("c".into(), 0, 4096),
                ("c".into(), 4096, 4096),
                ("c".into(), 8192, 1),
            ]
        );
        // Wraps.
        assert_eq!(r.get(7).0.path, "a");
        assert_eq!(r.get(7 + 3).0.path, "b");
        assert_eq!(Ranges::new(&[entry("z", 0)], 4096).total(), 0);
    }

    #[test]
    fn histogram_buckets_and_quantiles() {
        // Every value maps into a bucket whose lower edge is at most it and
        // within 1/16 of it.
        for v in [
            0u64,
            1,
            15,
            16,
            17,
            31,
            32,
            1000,
            123_456,
            9_999_999_999,
            u64::MAX / 3,
            u64::MAX,
        ] {
            let i = Histogram::index(v);
            assert!(i < BUCKETS, "{v}");
            let lo = Histogram::lower(i);
            assert!(lo <= v, "{v}: lower {lo}");
            assert!(v - lo <= v / 16, "{v}: lower {lo} too far");
        }
        // Buckets are ordered.
        for i in 1..BUCKETS - 1 {
            assert!(Histogram::lower(i) < Histogram::lower(i + 1), "{i}");
        }
        let mut h = Histogram::new();
        for v in 1..=1000u64 {
            h.record(v * 1000);
        }
        let near = |q: f64, want: u64| {
            let got = h.quantile(q);
            assert!(
                got <= want && want - got <= want / 16,
                "q{q}: {got} vs {want}"
            );
        };
        near(0.5, 500_000);
        near(0.99, 990_000);
        near(0.999, 999_000);
        assert_eq!(h.max, 1_000_000);
        let mut other = Histogram::new();
        other.record(5_000_000);
        h.merge(&other);
        assert_eq!(h.max, 5_000_000);
        assert_eq!(Histogram::new().quantile(0.5), 0);
    }

    #[test]
    fn options_are_checked() {
        assert!(BenchOptions::default().check().is_ok());
        for bad in [
            BenchOptions {
                connections: 0,
                ..BenchOptions::default()
            },
            BenchOptions {
                range_size: 0,
                ..BenchOptions::default()
            },
            BenchOptions {
                range_size: 4097,
                ..BenchOptions::default()
            },
            BenchOptions {
                duration: Duration::ZERO,
                ..BenchOptions::default()
            },
        ] {
            assert!(bad.check().is_err(), "{bad:?}");
        }
        assert_eq!("seq".parse::<Pattern>(), Ok(Pattern::Seq));
        assert_eq!("rand".parse::<Pattern>(), Ok(Pattern::Rand));
        assert!("random".parse::<Pattern>().is_err());
    }

    /// An in-memory export: the manifest under its key, files under theirs.
    struct Mem {
        manifest: Vec<u8>,
        files: Vec<(String, Vec<u8>)>,
        max: usize,
        /// Fail a file range read after this many successful ones.
        fail_after: Option<u64>,
        reads: Arc<AtomicU64>,
        buf: Vec<u8>,
    }

    impl RangeSource for Mem {
        fn max_range(&self) -> usize {
            self.max
        }
        fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
            if key == manifest::key("") {
                return Ok((self.manifest.len() as u64, 1));
            }
            Err(GetError::NotFound)
        }
        fn fetch(
            &mut self,
            key: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            let data = if key == manifest::key("") {
                assert_eq!(generation, 1);
                &self.manifest
            } else {
                assert_eq!(generation, 7);
                let n = self.reads.fetch_add(1, Ordering::Relaxed);
                if self.fail_after.is_some_and(|k| n >= k) {
                    return Err(GetError::Corrupt {
                        expected: 1,
                        actual: 2,
                    });
                }
                &self
                    .files
                    .iter()
                    .find(|(p, _)| p == key)
                    .ok_or(GetError::NotFound)?
                    .1
            };
            let start = (offset as usize).min(data.len());
            let end = (start + len.min(self.max)).min(data.len());
            self.buf.clear();
            self.buf.extend_from_slice(&data[start..end]);
            Ok(&self.buf)
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            Ok(())
        }
    }

    struct MemSet {
        files: Vec<(String, Vec<u8>)>,
        fail_after: Option<u64>,
        busy: bool,
        opened: AtomicU64,
        reads: Arc<AtomicU64>,
    }

    impl MemSet {
        fn new(files: &[(&str, usize)]) -> Self {
            Self {
                files: files
                    .iter()
                    .map(|&(p, n)| (p.to_string(), vec![0xab; n]))
                    .collect(),
                fail_after: None,
                busy: false,
                opened: AtomicU64::new(0),
                reads: Arc::new(AtomicU64::new(0)),
            }
        }
    }

    impl SourceSet for MemSet {
        type Source = Mem;
        fn candidates(&self) -> Vec<String> {
            vec!["mem:1".into()]
        }
        fn open(&self, _: &str) -> Result<Mem, OpenError> {
            if self.busy {
                return Err(OpenError::Busy("full".into()));
            }
            self.opened.fetch_add(1, Ordering::Relaxed);
            let entries: Vec<Entry> = self
                .files
                .iter()
                .map(|(p, d)| entry(p, d.len() as u64))
                .collect();
            Ok(Mem {
                manifest: manifest::encode_v2(&entries),
                files: self.files.clone(),
                max: 1 << 20,
                fail_after: self.fail_after,
                reads: Arc::clone(&self.reads),
                buf: Vec::new(),
            })
        }
    }

    fn quick(pattern: Pattern) -> BenchOptions {
        BenchOptions {
            connections: 4,
            range_size: 64 << 10,
            pattern,
            warmup: Duration::from_millis(20),
            duration: Duration::from_millis(150),
            ..BenchOptions::default()
        }
    }

    #[test]
    fn bench_reads_ranges_for_the_window() {
        for pattern in [Pattern::Seq, Pattern::Rand] {
            let set = MemSet::new(&[("a", 1 << 20), ("b", 300_000), ("empty", 0)]);
            let r = bench(&set, "", &quick(pattern)).expect("bench");
            // The manifest's connection, then one per worker.
            assert_eq!(set.opened.load(Ordering::Relaxed), 1 + 4);
            assert_eq!((r.files, r.dataset_bytes), (2, (1 << 20) + 300_000));
            assert_eq!(
                (r.connections, r.range_size, r.pattern),
                (4, 64 << 10, pattern)
            );
            assert!(r.ops > 0 && r.bytes > 0, "{r:?}");
            assert!(r.bytes <= r.ops * (64 << 10));
            assert_eq!(r.elapsed, Duration::from_millis(150));
            assert!(
                r.latency.p50 <= r.latency.p99
                    && r.latency.p99 <= r.latency.p999
                    && r.latency.p999 <= r.latency.max,
                "{:?}",
                r.latency
            );
            assert!(r.gib_per_s() > 0.0 && r.ops_per_s() > 0.0);
            assert_eq!(r.source, "mem:1");
        }
    }

    #[test]
    fn range_is_capped_by_the_source() {
        let set = MemSet::new(&[("a", 4 << 20)]);
        let opts = BenchOptions {
            range_size: 16 << 20,
            ..quick(Pattern::Seq)
        };
        let r = bench(&set, "", &opts).expect("bench");
        assert_eq!(r.range_size, 1 << 20);
    }

    #[test]
    fn a_failed_range_voids_the_run() {
        let mut set = MemSet::new(&[("a", 1 << 20)]);
        set.fail_after = Some(10);
        match bench(&set, "", &quick(Pattern::Seq)) {
            Err(BenchError::Range { path, error, .. }) => {
                assert_eq!(path, "a");
                assert_eq!(
                    error,
                    GetError::Corrupt {
                        expected: 1,
                        actual: 2
                    }
                    .to_string()
                );
            }
            other => panic!("expected a range error, got {other:?}"),
        }
    }

    #[test]
    fn bench_with_runs_each_workers_own_operation() {
        let set = MemSet::new(&[("a", 1 << 20)]);
        let (made, reads) = (AtomicU64::new(0), AtomicU64::new(0));
        let r = bench_with(&set, "", &quick(Pattern::Seq), |w, range| {
            assert!(w < 4 && range == 64 << 10);
            made.fetch_add(1, Ordering::Relaxed);
            let reads = &reads;
            Ok(move |src: &mut Mem, path: &str, offset, generation, len| {
                reads.fetch_add(1, Ordering::Relaxed);
                src.fetch(path, offset, generation, len).map(|b| b.len())
            })
        })
        .expect("bench");
        assert_eq!(made.load(Ordering::Relaxed), 4);
        // Every measured range went through the workers' own operations.
        assert!(r.ops > 0 && reads.load(Ordering::Relaxed) >= r.ops, "{r:?}");
    }

    #[test]
    fn a_worker_that_cannot_set_up_voids_the_run() {
        let set = MemSet::new(&[("a", 1 << 20)]);
        let r = bench_with(&set, "", &quick(Pattern::Seq), |w, _| {
            if w == 2 {
                Err("no GPU memory".to_string())
            } else {
                Ok(|src: &mut Mem, path: &str, offset, generation, len| {
                    src.fetch(path, offset, generation, len).map(|b| b.len())
                })
            }
        });
        assert!(matches!(r, Err(BenchError::Open(e)) if e == "no GPU memory"));
    }

    #[test]
    fn busy_empty_and_bad_options_fail_before_measuring() {
        let mut busy = MemSet::new(&[("a", 4096)]);
        busy.busy = true;
        assert!(
            matches!(bench(&busy, "", &quick(Pattern::Seq)), Err(BenchError::Open(e)) if e.contains("busy"))
        );
        let empty = MemSet::new(&[("z", 0)]);
        assert!(matches!(
            bench(&empty, "", &quick(Pattern::Seq)),
            Err(BenchError::Empty)
        ));
        let none = MemSet::new(&[]);
        assert!(matches!(
            bench(&none, "", &quick(Pattern::Seq)),
            Err(BenchError::Empty)
        ));
        let bad = BenchOptions {
            connections: 0,
            ..quick(Pattern::Seq)
        };
        assert!(matches!(
            bench(&MemSet::new(&[("a", 4096)]), "", &bad),
            Err(BenchError::Options(_))
        ));
        assert_eq!(
            MemSet::new(&[("a", 4096)]).opened.load(Ordering::Relaxed),
            0
        );
    }
}
