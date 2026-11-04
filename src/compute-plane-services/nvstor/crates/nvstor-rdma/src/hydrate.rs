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

//! Hydrate one object from an nvstor object server into a local file.
//!
//! Built for model shards far larger than RAM: the object is fetched as
//! ranged GETs over several connections in parallel and each range is
//! `pwrite`n straight into a preallocated file. Nothing holds more than
//! one range per connection in memory. The transport is pluggable via
//! [`RangeSource`]: `tcp::hydrate_to_file` (S3-over-TCP) and
//! `s3_rdma::hydrate_to_file` (S3-over-RDMA, ranges land in registered
//! memory and are written to the file from there).
//!
//! Integrity and durability contract:
//! - Every range is checked against the server's CRC-32C, and every range
//!   is pinned to the generation reported by the initial stat. If the
//!   object is replaced mid-transfer the hydration fails with
//!   `GenerationChanged` instead of producing a file that mixes versions.
//! - Bytes land in `<dest>.partial`. Only after every range is verified
//!   and the file is `fdatasync`ed is it renamed to `<dest>` and the
//!   directory fsynced. The final name therefore exists only for a
//!   complete, durable, verified object: a crash at any point leaves at
//!   most a `.partial`, never a full-length file that merely looks
//!   hydrated. Callers (a model catalog) can treat "`dest` exists" as the
//!   completion marker.
//!
//! Failure handling: retryable range errors (corruption, transport) retry
//! the same range with backoff, at most `max_retries` times; a transport
//! error first replaces the worker's connection. A busy server (NO_SLOT)
//! is not an error: it is asked again after a pause without spending a
//! retry, until `busy_wait` has passed since its first busy answer in a
//! row. Ranges already written stay written, so a
//! dropped peer costs one range, not the shard.

use std::fs::{File, OpenOptions};
use std::io;
use std::os::unix::fs::FileExt;
use std::os::unix::io::AsRawFd;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::time::{Duration, Instant};

use crate::object::GetError;

/// One connection's view of the object server, as hydration needs it.
pub trait RangeSource {
    /// Largest range this source can fetch in one request.
    fn max_range(&self) -> usize;
    /// Object size and generation.
    fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError>;
    /// Fetch up to `len` bytes of `key` at `offset` from `generation`,
    /// CRC-verified. Returns the bytes, borrowed from the source's own
    /// buffer until the next call. Shorter than `len` only at end of
    /// object.
    fn fetch(
        &mut self,
        key: &str,
        offset: u64,
        generation: u128,
        len: usize,
    ) -> Result<&[u8], GetError>;
    /// Replace the underlying connection after a transport failure.
    fn reconnect(&mut self) -> Result<(), HydrateError>;
}

/// Tuning for hydration.
#[derive(Clone, Debug)]
pub struct HydrateOptions {
    /// Parallel connections (one worker thread each).
    pub connections: usize,
    /// Bytes per ranged GET; capped by the source's `max_range`.
    pub range_size: usize,
    /// Attempts per range after the first, for retryable errors.
    pub max_retries: u32,
    /// Delay before the first retry; doubles per attempt (capped, with
    /// jitter: `object::backoff`).
    pub retry_backoff: Duration,
    /// First pause after a busy answer; doubles per busy answer in a row
    /// (capped, with jitter: `object::backoff`). Spends no retry.
    pub busy_backoff: Duration,
    /// Longest a busy server is waited out, from its first busy answer in
    /// a row; past it the hydration fails with `GetError::Busy`.
    pub busy_wait: Duration,
}

impl Default for HydrateOptions {
    fn default() -> Self {
        Self {
            connections: 8,
            range_size: 16 * 1024 * 1024,
            max_retries: 5,
            retry_backoff: Duration::from_millis(50),
            busy_backoff: Duration::from_millis(50),
            busy_wait: Duration::from_secs(120),
        }
    }
}

/// What a successful hydration did.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct HydrateReport {
    pub bytes: u64,
    pub generation: u128,
    pub ranges: u64,
    pub retries: u64,
    pub reconnects: u64,
    /// Busy answers waited out (not retries).
    pub busy: u64,
    /// Total wall time, including `sync`.
    pub elapsed: Duration,
    /// Time spent in the final fdatasync + rename + directory fsync,
    /// i.e. waiting for the local disk rather than the network.
    pub sync: Duration,
}

#[derive(Debug)]
pub enum HydrateError {
    /// Could not open a connection to the server.
    Connect(String),
    /// A range failed for good (non-retryable, or retries exhausted).
    Get { offset: u64, error: GetError },
    /// Local file I/O failed.
    Io(io::Error),
    /// Invalid options.
    Invalid(String),
}

impl std::fmt::Display for HydrateError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            HydrateError::Connect(e) => write!(f, "hydrate: connect: {e}"),
            HydrateError::Get { offset, error } => write!(f, "hydrate: range at {offset}: {error}"),
            HydrateError::Io(e) => write!(f, "hydrate: local I/O: {e}"),
            HydrateError::Invalid(e) => write!(f, "hydrate: {e}"),
        }
    }
}

impl std::error::Error for HydrateError {}

impl From<io::Error> for HydrateError {
    fn from(e: io::Error) -> Self {
        HydrateError::Io(e)
    }
}

/// Fetch `key` into `dest` using sources made by `open` (called once per
/// connection, from that connection's worker thread).
///
/// On success `dest` holds the complete object, verified range by range
/// and durable on disk. On failure `dest` is untouched and the partial
/// file is removed.
pub fn hydrate_to_file<S, F>(
    open: F,
    key: &str,
    dest: &Path,
    opts: &HydrateOptions,
) -> Result<HydrateReport, HydrateError>
where
    S: RangeSource,
    F: Fn() -> Result<S, HydrateError> + Sync,
{
    if opts.connections == 0 || opts.range_size == 0 {
        return Err(HydrateError::Invalid(
            "connections and range_size must be > 0".into(),
        ));
    }
    let started = Instant::now();
    let mut first = open()?;
    let range_size = opts.range_size.min(first.max_range()) as u64;
    if range_size == 0 {
        return Err(HydrateError::Invalid(
            "source cannot fetch any bytes per range".into(),
        ));
    }
    let (size, generation, stat) = stat_with_retries(&mut first, key, opts)?;

    let partial = partial_path(dest);
    let file = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .open(&partial)?;
    let result = (|| {
        preallocate(&file, size)?;
        let job = Job {
            key,
            generation,
            size,
            range_size,
            file: &file,
            opts,
        };
        let stats = job.run(first, &open)?;
        let sync_started = Instant::now();
        file.sync_data()?;
        std::fs::rename(&partial, dest)?;
        sync_parent_dir(dest)?;
        Ok((stats, sync_started.elapsed()))
    })();
    let ((ranges, mut retries, mut reconnects, mut busy), sync) = match result {
        Ok(r) => r,
        Err(e) => {
            drop(file);
            let _ = std::fs::remove_file(&partial);
            return Err(e);
        }
    };
    retries += stat.retries;
    reconnects += stat.reconnects;
    busy += stat.busy;
    Ok(HydrateReport {
        bytes: size,
        generation,
        ranges,
        retries,
        reconnects,
        busy,
        elapsed: started.elapsed(),
        sync,
    })
}

/// Waiting out a busy server: a busy answer pauses (`opts.busy_backoff`,
/// doubling, jittered) without spending a retry, until `opts.busy_wait`
/// has passed since the first busy answer in a row.
#[derive(Default)]
struct BusyStreak {
    since: Option<Instant>,
    round: u32,
}

impl BusyStreak {
    /// After a busy answer: pause and return true (ask again), or false
    /// once the busy deadline has passed.
    fn wait(&mut self, opts: &HydrateOptions) -> bool {
        let since = *self.since.get_or_insert_with(Instant::now);
        let waited = since.elapsed();
        if waited >= opts.busy_wait {
            return false;
        }
        self.round += 1;
        std::thread::sleep(
            crate::object::backoff(opts.busy_backoff, self.round).min(opts.busy_wait - waited),
        );
        true
    }

    /// Any other answer ends the streak.
    fn reset(&mut self) {
        *self = Self::default();
    }
}

/// What the initial stat cost.
#[derive(Default)]
struct StatCost {
    retries: u64,
    reconnects: u64,
    busy: u64,
}

/// The initial stat, retried like a range: a corrupt answer is asked
/// again after a backoff, a transport error first replaces the
/// connection, at most `opts.max_retries` times; a busy server is waited
/// out ([`BusyStreak`]) without spending retries. Returns (size,
/// generation, cost).
fn stat_with_retries<S: RangeSource>(
    src: &mut S,
    key: &str,
    opts: &HydrateOptions,
) -> Result<(u64, u128, StatCost), HydrateError> {
    let mut attempt = 0u32;
    let mut cost = StatCost::default();
    let mut busy = BusyStreak::default();
    loop {
        match src.stat(key) {
            Ok((size, generation)) => {
                cost.retries = u64::from(attempt);
                return Ok((size, generation, cost));
            }
            Err(GetError::Busy) => {
                if !busy.wait(opts) {
                    return Err(HydrateError::Get {
                        offset: 0,
                        error: GetError::Busy,
                    });
                }
                cost.busy += 1;
            }
            Err(error) if error.is_retryable() && attempt < opts.max_retries => {
                busy.reset();
                attempt += 1;
                std::thread::sleep(crate::object::backoff(opts.retry_backoff, attempt));
                if matches!(error, GetError::Transport(_)) {
                    src.reconnect()?;
                    cost.reconnects += 1;
                }
            }
            Err(error) => return Err(HydrateError::Get { offset: 0, error }),
        }
    }
}

/// Shared state of one hydration: the ranges to fetch and the counters.
struct Job<'a> {
    key: &'a str,
    generation: u128,
    size: u64,
    range_size: u64,
    file: &'a File,
    opts: &'a HydrateOptions,
}

#[derive(Default)]
struct Counters {
    next: AtomicU64,
    failed: AtomicBool,
    ranges: AtomicU64,
    retries: AtomicU64,
    reconnects: AtomicU64,
    busy: AtomicU64,
}

impl Job<'_> {
    /// Run the worker pool. Returns (ranges, retries, reconnects, busy).
    fn run<S, F>(&self, first: S, open: &F) -> Result<(u64, u64, u64, u64), HydrateError>
    where
        S: RangeSource,
        F: Fn() -> Result<S, HydrateError> + Sync,
    {
        let total_ranges = self.size.div_ceil(self.range_size);
        let workers = (self.opts.connections as u64).min(total_ranges.max(1)) as usize;
        let c = Counters::default();
        std::thread::scope(|scope| {
            let mut handles = Vec::with_capacity(workers.saturating_sub(1));
            for _ in 1..workers {
                handles.push(scope.spawn(|| {
                    let r = open().and_then(|src| self.work(src, &c));
                    if r.is_err() {
                        c.failed.store(true, Ordering::Relaxed);
                    }
                    r
                }));
            }
            let mut outcome = self.work(first, &c);
            if outcome.is_err() {
                c.failed.store(true, Ordering::Relaxed);
            }
            for h in handles {
                let r = h
                    .join()
                    .unwrap_or_else(|_| Err(HydrateError::Invalid("worker panicked".into())));
                if outcome.is_ok() {
                    outcome = r;
                }
            }
            outcome
        })?;
        Ok((
            c.ranges.into_inner(),
            c.retries.into_inner(),
            c.reconnects.into_inner(),
            c.busy.into_inner(),
        ))
    }

    /// One worker: claim ranges until none are left (or another worker
    /// failed), fetching each into the source's buffer and writing it.
    fn work<S: RangeSource>(&self, mut src: S, c: &Counters) -> Result<(), HydrateError> {
        loop {
            if c.failed.load(Ordering::Relaxed) {
                return Ok(());
            }
            let offset = c.next.fetch_add(self.range_size, Ordering::Relaxed);
            if offset >= self.size {
                return Ok(());
            }
            let want = (self.size - offset).min(self.range_size) as usize;
            let mut filled = 0usize;
            let mut attempt = 0u32;
            let mut busy = BusyStreak::default();
            while filled < want {
                let at = offset + filled as u64;
                match src.fetch(self.key, at, self.generation, want - filled) {
                    Ok([]) => {
                        // The stat promised more bytes than the object
                        // has, yet the generation is unchanged.
                        return Err(HydrateError::Get {
                            offset: at,
                            error: GetError::Transport(format!(
                                "object ended at {at}, expected {} bytes",
                                self.size
                            )),
                        });
                    }
                    Ok(bytes) => {
                        self.file.write_all_at(bytes, at)?;
                        filled += bytes.len();
                        busy.reset();
                    }
                    Err(GetError::Busy) => {
                        if !busy.wait(self.opts) {
                            return Err(HydrateError::Get {
                                offset: at,
                                error: GetError::Busy,
                            });
                        }
                        c.busy.fetch_add(1, Ordering::Relaxed);
                    }
                    Err(error) if error.is_retryable() && attempt < self.opts.max_retries => {
                        busy.reset();
                        attempt += 1;
                        c.retries.fetch_add(1, Ordering::Relaxed);
                        std::thread::sleep(crate::object::backoff(
                            self.opts.retry_backoff,
                            attempt,
                        ));
                        if matches!(error, GetError::Transport(_)) {
                            src.reconnect()?;
                            c.reconnects.fetch_add(1, Ordering::Relaxed);
                        }
                    }
                    Err(error) => return Err(HydrateError::Get { offset: at, error }),
                }
            }
            c.ranges.fetch_add(1, Ordering::Relaxed);
        }
    }
}

pub(crate) fn partial_path(dest: &Path) -> PathBuf {
    let mut name = dest
        .file_name()
        .map(|n| n.to_os_string())
        .unwrap_or_default();
    name.push(".partial");
    dest.with_file_name(name)
}

/// Reserve the blocks up front so ENOSPC surfaces before any transfer
/// and the file is laid out contiguously. Filesystems without fallocate
/// get a sparse `set_len` instead.
fn preallocate(file: &File, size: u64) -> io::Result<()> {
    if size == 0 {
        return Ok(());
    }
    let len = libc::off_t::try_from(size)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "object too large for off_t"))?;
    // SAFETY: valid open fd, mode 0 (allocate + extend), in-range length.
    let rc = unsafe { libc::fallocate(file.as_raw_fd(), 0, 0, len) };
    if rc == 0 {
        return Ok(());
    }
    let err = io::Error::last_os_error();
    match err.raw_os_error() {
        Some(libc::EOPNOTSUPP) | Some(libc::ENOSYS) => file.set_len(size),
        _ => Err(err),
    }
}

fn sync_parent_dir(path: &Path) -> io::Result<()> {
    let parent = match path.parent() {
        Some(p) if !p.as_os_str().is_empty() => p,
        _ => Path::new("."),
    };
    File::open(parent)?.sync_all()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// In-memory source: serves `data`, optionally failing chosen fetches.
    struct Fake {
        data: std::sync::Arc<Vec<u8>>,
        generation: u128,
        max: usize,
        /// Errors to return on successive fetches (front first).
        script: std::sync::Arc<parking_lot::Mutex<Vec<GetError>>>,
        /// Errors to return on successive stats (front first).
        stat_script: std::sync::Arc<parking_lot::Mutex<Vec<GetError>>>,
        reconnects: std::sync::Arc<AtomicU64>,
    }

    impl RangeSource for Fake {
        fn max_range(&self) -> usize {
            self.max
        }
        fn stat(&mut self, _: &str) -> Result<(u64, u128), GetError> {
            let mut s = self.stat_script.lock();
            if !s.is_empty() {
                return Err(s.remove(0));
            }
            Ok((self.data.len() as u64, self.generation))
        }
        fn fetch(
            &mut self,
            _: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            if let Some(e) = {
                let mut s = self.script.lock();
                if s.is_empty() {
                    None
                } else {
                    Some(s.remove(0))
                }
            } {
                return Err(e);
            }
            assert_eq!(generation, self.generation);
            let start = (offset as usize).min(self.data.len());
            let end = (start + len.min(self.max)).min(self.data.len());
            Ok(&self.data[start..end])
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            self.reconnects.fetch_add(1, Ordering::Relaxed);
            Ok(())
        }
    }

    fn fake(
        data: Vec<u8>,
        max: usize,
        script: Vec<GetError>,
    ) -> impl Fn() -> Result<Fake, HydrateError> + Sync {
        fake_stat(data, max, script, vec![])
    }

    fn fake_stat(
        data: Vec<u8>,
        max: usize,
        script: Vec<GetError>,
        stat_script: Vec<GetError>,
    ) -> impl Fn() -> Result<Fake, HydrateError> + Sync {
        let data = std::sync::Arc::new(data);
        let script = std::sync::Arc::new(parking_lot::Mutex::new(script));
        let stat_script = std::sync::Arc::new(parking_lot::Mutex::new(stat_script));
        let reconnects = std::sync::Arc::new(AtomicU64::new(0));
        move || {
            Ok(Fake {
                data: data.clone(),
                generation: 42,
                max,
                script: script.clone(),
                stat_script: stat_script.clone(),
                reconnects: reconnects.clone(),
            })
        }
    }

    /// `f`'s result, which must come within `limit` (a test that would
    /// otherwise hang fails instead).
    fn within<T: Send + 'static>(limit: Duration, f: impl FnOnce() -> T + Send + 'static) -> T {
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let _ = tx.send(f());
        });
        rx.recv_timeout(limit).expect("finished in time")
    }

    // A busy server answers the initial
    // stat NO_SLOT (Busy); hydration waits it out instead of aborting, and
    // spends no retry on it: ten busy answers with max_retries 3.
    #[test]
    fn a_busy_stat_is_waited_out_without_spending_retries() {
        let data = vec![9u8; 5000];
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("o");
        let r = hydrate_to_file(
            fake_stat(data.clone(), 4096, vec![], vec![GetError::Busy; 10]),
            "k",
            &dest,
            &opts(1, 4096),
        )
        .expect("hydrated after ten busy stats");
        assert_eq!((r.retries, r.busy), (0, 10));
        assert_eq!(std::fs::read(&dest).expect("read"), data);
        // Bounded by busy_wait: a server that stays busy fails the hydration.
        let dest2 = dir.path().join("o2");
        let mut o = opts(1, 4096);
        o.busy_wait = Duration::from_millis(50);
        let err = within(Duration::from_secs(10), move || {
            hydrate_to_file(
                fake_stat(data, 4096, vec![], vec![GetError::Busy; 1_000_000]),
                "k",
                &dest2,
                &o,
            )
        })
        .unwrap_err();
        assert!(
            matches!(
                err,
                HydrateError::Get {
                    offset: 0,
                    error: GetError::Busy
                }
            ),
            "{err}"
        );
    }

    // Busy ranges spend no retries either; only busy_wait
    // bounds them.
    #[test]
    fn busy_ranges_spend_no_retries_and_are_bounded_by_busy_wait() {
        let data = vec![5u8; 10_000];
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("o");
        let r = hydrate_to_file(
            fake(data.clone(), 4096, vec![GetError::Busy; 10]),
            "k",
            &dest,
            &opts(1, 4096),
        )
        .expect("busy ranges waited out");
        assert_eq!((r.retries, r.busy), (0, 10));
        assert_eq!(std::fs::read(&dest).expect("read"), data);
        let mut o = opts(1, 4096);
        o.busy_wait = Duration::from_millis(50);
        let dest2 = dir.path().join("o2");
        let err = within(Duration::from_secs(10), move || {
            hydrate_to_file(
                fake(data, 4096, vec![GetError::Busy; 1_000_000]),
                "k",
                &dest2,
                &o,
            )
        })
        .unwrap_err();
        assert!(
            matches!(
                err,
                HydrateError::Get {
                    error: GetError::Busy,
                    ..
                }
            ),
            "{err}"
        );
    }

    fn opts(connections: usize, range_size: usize) -> HydrateOptions {
        HydrateOptions {
            connections,
            range_size,
            max_retries: 3,
            retry_backoff: Duration::from_millis(1),
            busy_backoff: Duration::from_millis(1),
            busy_wait: Duration::from_secs(10),
        }
    }

    #[test]
    fn source_shorter_than_range_is_reassembled() {
        // max_range 1000 < range_size 4096: every range takes several fetches.
        let data: Vec<u8> = (0..20_000u32).map(|i| i as u8).collect();
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("o");
        let r = hydrate_to_file(fake(data.clone(), 1000, vec![]), "k", &dest, &opts(3, 4096))
            .expect("ok");
        assert_eq!((r.bytes, r.ranges, r.generation), (20_000, 20, 42));
        assert_eq!(std::fs::read(&dest).expect("read"), data);
    }

    #[test]
    fn retryable_errors_retry_and_transport_errors_reconnect() {
        let data = vec![7u8; 10_000];
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("o");
        let script = vec![
            GetError::Busy,
            GetError::Corrupt {
                expected: 1,
                actual: 2,
            },
            GetError::Transport("reset".into()),
        ];
        let r = hydrate_to_file(fake(data.clone(), 4096, script), "k", &dest, &opts(1, 4096))
            .expect("ok");
        assert_eq!((r.retries, r.reconnects, r.busy), (2, 1, 1));
        assert_eq!(std::fs::read(&dest).expect("read"), data);
    }

    #[test]
    fn retries_are_bounded() {
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("o");
        let script = vec![
            GetError::Corrupt {
                expected: 1,
                actual: 2
            };
            10
        ];
        let err = hydrate_to_file(fake(vec![1; 100], 4096, script), "k", &dest, &opts(1, 4096))
            .unwrap_err();
        assert!(
            matches!(
                err,
                HydrateError::Get {
                    error: GetError::Corrupt { .. },
                    ..
                }
            ),
            "{err}"
        );
        assert!(!dest.exists() && !partial_path(&dest).exists());
    }

    #[test]
    fn final_errors_do_not_retry() {
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("o");
        let err = hydrate_to_file(
            fake(vec![1; 100], 4096, vec![GetError::GenerationChanged]),
            "k",
            &dest,
            &opts(1, 4096),
        )
        .unwrap_err();
        assert!(matches!(
            err,
            HydrateError::Get {
                error: GetError::GenerationChanged,
                ..
            }
        ));
        assert!(!dest.exists() && !partial_path(&dest).exists());
    }

    #[test]
    fn rejects_zero_connections_or_range() {
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("o");
        assert!(matches!(
            hydrate_to_file(fake(vec![], 1, vec![]), "k", &dest, &opts(0, 1)),
            Err(HydrateError::Invalid(_))
        ));
        assert!(matches!(
            hydrate_to_file(fake(vec![], 1, vec![]), "k", &dest, &opts(1, 0)),
            Err(HydrateError::Invalid(_))
        ));
    }

    #[test]
    fn partial_path_appends_suffix() {
        assert_eq!(
            partial_path(Path::new("/m/a.bin")),
            PathBuf::from("/m/a.bin.partial")
        );
        assert_eq!(partial_path(Path::new("a")), PathBuf::from("a.partial"));
    }
}
