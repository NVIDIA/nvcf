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

//! Admission control — bounds concurrent in-flight requests.
//!
//! Both S3 transports used to `thread::spawn` per request with no cap. A
//! fast or pipelining client could therefore force the server to create
//! threads without limit: each one costs a stack (8 MiB of address space by
//! default), an fd or two, and a slot in whatever buffer the handler
//! allocates. The failure mode is not graceful — it is OOM, `EAGAIN` from
//! `pthread_create`, or fd exhaustion, all of which take down every tenant
//! on the node, not just the one that misbehaved.
//!
//! The fix is a shared permit pool. A dispatcher acquires a permit BEFORE
//! spawning the worker, so the number of live worker threads can never
//! exceed the limit. Acquiring blocks when the pool is empty, which is the
//! point: the dispatcher stops reading, the socket's receive buffer fills,
//! and TCP flow control pushes back on the client. Backpressure beats
//! rejection here because the client is already blocked waiting for its
//! response — failing the request would just make it retry and add load.
//!
//! Per-peer fairness falls out of the design: each peer has its own
//! dispatcher, so a saturated pool stalls whichever dispatchers are trying
//! to admit work rather than letting one peer monopolize the pool.
//!
//! [`Admission::execute`] runs admitted work on the admission's own worker
//! threads instead of a thread per request. Starting and ending a thread
//! maps and unmaps its stack under the process's memory-map lock and
//! shoots down TLBs on every CPU running one of its threads, so a thread
//! per request caps the request rate of a process whatever the request
//! size. Workers start on demand, at
//! most one per permit, and live until [`Admission::close`]. A job goes
//! straight to the worker that went idle last (LIFO), so the workers in use
//! stay as few as the concurrency needs and keep warm caches; waking idle
//! workers in turn through one shared condvar would spread a few concurrent
//! requests over many workers and lose throughput.

use std::collections::VecDeque;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Arc, Condvar, Mutex};

use tracing::warn;

type Job = Box<dyn FnOnce() + Send + 'static>;

/// Why [`Admission::execute`] did not take a job.
#[derive(Debug, PartialEq, Eq)]
pub enum ExecError {
    /// The admission is closed.
    Closed,
    /// No worker exists and none could be started.
    Spawn(String),
    /// No permit freed up within the bound ([`Admission::execute_within`]).
    Busy,
}

/// An idle worker's hand-off slot: `execute` puts a job in it and wakes
/// that worker alone.
#[derive(Default)]
struct Idle {
    job: Mutex<Option<Job>>,
    wake: Condvar,
}

/// Admitted jobs and the workers that run them.
#[derive(Default)]
struct Workers {
    /// Jobs no worker was free for; busy workers take them before idling.
    jobs: VecDeque<Job>,
    /// Workers alive.
    threads: usize,
    /// Idle workers, the most recently idle last.
    idle: Vec<Arc<Idle>>,
}

/// Default cap on concurrently-executing requests.
///
/// Sized off CPU count because these workers are a mix of storage I/O and
/// checksum/crypto CPU work; the floor keeps small machines from
/// serializing, and the ceiling keeps thread stacks bounded on large ones.
pub fn default_max_in_flight() -> usize {
    let cpus = std::thread::available_parallelism()
        .map(|n| n.get())
        .unwrap_or(4);
    (cpus * 4).clamp(32, 512)
}

/// A counting semaphore over in-flight requests.
///
/// Hand-rolled on `Mutex` + thread park rather than pulling in a
/// semaphore crate: the transports here are synchronous (`std::thread`),
/// so a blocking primitive is what fits, and the project keeps core-path
/// dependencies to zero. Permits are granted in arrival order: a
/// dispatcher that starts waiting first is served first, and a newly
/// arriving one never takes a freed permit ahead of a waiter (which
/// could otherwise run out its `execute_within` bound under plain
/// overload). Only the head waiter is woken, after the lock is dropped.
pub struct Admission {
    permits: Mutex<Permits>,
    closed: AtomicBool,
    capacity: usize,
    /// Number of times a caller had to wait for a permit. Exposed for
    /// metrics: sustained growth means the node is the bottleneck and
    /// should be scaled or have its cap raised.
    waits: AtomicUsize,
    workers: Mutex<Workers>,
    /// Test hook: make starting a worker thread fail.
    #[cfg(test)]
    fail_spawn: AtomicBool,
}

/// Free permits and the waiters in line for them.
struct Permits {
    free: usize,
    waiters: VecDeque<(u64, std::thread::Thread)>,
    next_id: u64,
}

impl Permits {
    /// The waiters a permit is free for: the first `free` in line, in
    /// order (unpark them after dropping the lock). Empty, with no
    /// allocation, when nobody waits.
    fn to_wake(&self) -> Vec<std::thread::Thread> {
        self.waiters
            .iter()
            .take(self.free)
            .map(|(_, t)| t.clone())
            .collect()
    }

    /// Whether waiter `id` is among the first `free` in line, i.e. may
    /// take a permit now; FIFO: nobody behind the first `free` does.
    fn may_take(&self, id: u64) -> bool {
        self.waiters.iter().take(self.free).any(|(w, _)| *w == id)
    }

    fn leave(&mut self, id: u64) -> Vec<std::thread::Thread> {
        if let Some(i) = self.waiters.iter().position(|(w, _)| *w == id) {
            self.waiters.remove(i);
        }
        self.to_wake()
    }
}

fn unpark(ts: Vec<std::thread::Thread>) {
    for t in ts {
        t.unpark();
    }
}

impl Admission {
    pub fn new(capacity: usize) -> Arc<Self> {
        let capacity = capacity.max(1);
        Arc::new(Self {
            permits: Mutex::new(Permits {
                free: capacity,
                waiters: VecDeque::new(),
                next_id: 0,
            }),
            closed: AtomicBool::new(false),
            capacity,
            waits: AtomicUsize::new(0),
            workers: Mutex::new(Workers::default()),
            #[cfg(test)]
            fail_spawn: AtomicBool::new(false),
        })
    }

    /// Admit `job` (blocking until a permit is free, like [`acquire`]) and
    /// run it on a worker thread, which holds the permit until the job
    /// returns. A job that panics is logged; its worker carries on.
    ///
    /// Every queued or running job holds a permit, so at most `capacity`
    /// jobs exist and at most `capacity` workers are ever started.
    ///
    /// [`acquire`]: Self::acquire
    pub fn execute(self: &Arc<Self>, job: impl FnOnce() + Send + 'static) -> Result<(), ExecError> {
        let permit = self.acquire().ok_or(ExecError::Closed)?;
        self.run_admitted(permit, job, true)
    }

    /// [`Self::execute`], but wait at most `bound` for a permit: if none
    /// frees up, `job` is dropped unrun and the error is
    /// [`ExecError::Busy`], so the caller can answer its client "busy"
    /// instead of leaving the request unanswered for as long as the node
    /// stays saturated. Likewise, if no worker can be started for it, the
    /// job is refused ([`ExecError::Spawn`]) rather than queued behind
    /// running jobs of unbounded length.
    pub fn execute_within(
        self: &Arc<Self>,
        bound: std::time::Duration,
        job: impl FnOnce() + Send + 'static,
    ) -> Result<(), ExecError> {
        let permit = self.acquire_within(bound)?;
        self.run_admitted(permit, job, false)
    }

    /// Run `job` (holding `permit`) on an idle worker or a new one. If no
    /// worker can be started, it waits for a running one when
    /// `queue_if_no_worker`, and is refused otherwise.
    fn run_admitted(
        self: &Arc<Self>,
        permit: Permit,
        job: impl FnOnce() + Send + 'static,
        queue_if_no_worker: bool,
    ) -> Result<(), ExecError> {
        let job: Job = Box::new(move || {
            let _permit = permit;
            job();
        });
        let mut w = self.workers.lock().unwrap_or_else(|e| e.into_inner());
        if self.closed.load(Ordering::Relaxed) {
            // Closed after the permit was taken: workers may be gone.
            return Err(ExecError::Closed);
        }
        if let Some(idle) = w.idle.pop() {
            // Hand over before releasing `workers`: `close` sets `closed`
            // and empties the idle list under that lock, so a worker that
            // sees `closed` with an empty slot is never one popped here.
            *idle.job.lock().unwrap_or_else(|e| e.into_inner()) = Some(job);
            drop(w);
            idle.wake.notify_one();
            return Ok(());
        }
        if w.threads >= self.capacity {
            // A worker's permit is returned when its job ends, just before
            // it re-locks `workers` to go idle; a job admitted in that gap
            // lands here, and that worker takes it before going idle.
            w.jobs.push_back(job);
            return Ok(());
        }
        let pool = Arc::clone(self);
        let (tx, rx) = std::sync::mpsc::sync_channel::<Job>(1);
        match self.spawn_worker(move || pool.work(rx.recv().ok())) {
            Ok(_) => {
                w.threads += 1;
                // The receiver is alive until the new worker has taken it.
                if let Err(std::sync::mpsc::SendError(job)) = tx.send(job) {
                    w.threads -= 1;
                    w.jobs.push_back(job);
                }
                Ok(())
            }
            Err(e) if w.threads == 0 || !queue_if_no_worker => Err(ExecError::Spawn(e.to_string())),
            Err(e) => {
                warn!(error = %e, threads = w.threads,
                      "cannot start another worker; the job waits for a running one");
                w.jobs.push_back(job);
                Ok(())
            }
        }
    }

    fn spawn_worker(
        &self,
        f: impl FnOnce() + Send + 'static,
    ) -> std::io::Result<std::thread::JoinHandle<()>> {
        #[cfg(test)]
        if self.fail_spawn.load(Ordering::Relaxed) {
            return Err(std::io::Error::other("test: thread start refused"));
        }
        std::thread::Builder::new()
            .name("rdma-worker".into())
            .spawn(f)
    }

    /// Workers alive (started and not yet exited after `close`).
    pub fn worker_threads(&self) -> usize {
        self.workers
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .threads
    }

    fn work(&self, first: Option<Job>) {
        let me = Arc::new(Idle::default());
        let mut next = first;
        loop {
            if let Some(job) = next.take() {
                if std::panic::catch_unwind(std::panic::AssertUnwindSafe(job)).is_err() {
                    warn!("an admitted job panicked; its permit is returned and its worker carries on");
                }
            }
            let mut w = self.workers.lock().unwrap_or_else(|e| e.into_inner());
            if let Some(job) = w.jobs.pop_front() {
                next = Some(job);
                continue;
            }
            if self.closed.load(Ordering::Relaxed) {
                w.threads -= 1;
                return;
            }
            w.idle.push(Arc::clone(&me));
            drop(w);
            let mut slot = me.job.lock().unwrap_or_else(|e| e.into_inner());
            loop {
                if let Some(job) = slot.take() {
                    next = Some(job);
                    break;
                }
                // `close` sets `closed` and takes every idle worker off the
                // list under one `workers` lock, and `execute` fills a slot
                // under that same lock, so nothing can be handed to this
                // one any more: an empty slot after close means exit.
                if self.closed.load(Ordering::Relaxed) {
                    drop(slot);
                    let mut w = self.workers.lock().unwrap_or_else(|e| e.into_inner());
                    w.threads -= 1;
                    return;
                }
                slot = me.wake.wait(slot).unwrap_or_else(|e| e.into_inner());
            }
        }
    }

    /// Acquire a permit, waiting at most `bound` for one. The clock is
    /// read only if the caller has to wait.
    pub fn acquire_within(
        self: &Arc<Self>,
        bound: std::time::Duration,
    ) -> Result<Permit, ExecError> {
        self.acquire_inner(Some(bound))
    }

    /// Acquire a permit, blocking until one is free.
    ///
    /// Call this in the dispatcher BEFORE spawning the worker — acquiring
    /// inside the worker would cap concurrency but not thread count, which
    /// is the resource that actually runs out.
    pub fn acquire(self: &Arc<Self>) -> Option<Permit> {
        self.acquire_inner(None).ok()
    }

    fn acquire_inner(
        self: &Arc<Self>,
        bound: Option<std::time::Duration>,
    ) -> Result<Permit, ExecError> {
        let mut p = self.permits.lock().unwrap_or_else(|e| e.into_inner());
        if self.closed.load(Ordering::Relaxed) {
            return Err(ExecError::Closed);
        }
        // Uncontended: nobody in line and a permit free. No clock read.
        if p.waiters.is_empty() && p.free > 0 {
            p.free -= 1;
            return Ok(Permit {
                admission: Arc::clone(self),
            });
        }
        self.waits.fetch_add(1, Ordering::Relaxed);
        let id = p.next_id;
        p.next_id += 1;
        p.waiters.push_back((id, std::thread::current()));
        let mut deadline: Option<std::time::Instant> = None;
        loop {
            if self.closed.load(Ordering::Relaxed) {
                let next = p.leave(id);
                drop(p);
                unpark(next);
                return Err(ExecError::Closed);
            }
            if p.may_take(id) {
                p.free -= 1;
                if let Some(i) = p.waiters.iter().position(|(w, _)| *w == id) {
                    p.waiters.remove(i);
                }
                // Whoever is now within the first `free` was woken when
                // its permit was released; nothing to wake here.
                drop(p);
                return Ok(Permit {
                    admission: Arc::clone(self),
                });
            }
            match bound {
                None => {
                    drop(p);
                    std::thread::park();
                }
                Some(b) => {
                    let now = std::time::Instant::now();
                    let d = *deadline.get_or_insert(now + b);
                    if now >= d {
                        let next = p.leave(id);
                        drop(p);
                        unpark(next);
                        return Err(ExecError::Busy);
                    }
                    drop(p);
                    std::thread::park_timeout(d - now);
                }
            }
            p = self.permits.lock().unwrap_or_else(|e| e.into_inner());
        }
    }

    /// Dispatchers waiting for a permit.
    pub fn waiting(&self) -> usize {
        self.permits
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .waiters
            .len()
    }

    /// Refuse new work. Jobs already admitted still run; idle workers exit,
    /// and busy ones exit once the queue is empty.
    pub fn close(&self) {
        // `closed` is set and the idle list emptied under one `workers`
        // lock, the lock `execute` holds while it checks `closed` and fills
        // an idle worker's slot (see `execute`). Lock order: workers, then
        // permits; nothing takes them the other way round.
        let idle = {
            let mut w = self.workers.lock().unwrap_or_else(|e| e.into_inner());
            let waiters: Vec<std::thread::Thread> = {
                let p = self.permits.lock().unwrap_or_else(|e| e.into_inner());
                self.closed.store(true, Ordering::Relaxed);
                p.waiters.iter().map(|(_, t)| t.clone()).collect()
            };
            for t in waiters {
                t.unpark();
            }
            std::mem::take(&mut w.idle)
        };
        for i in idle {
            let _slot = i.job.lock().unwrap_or_else(|e| e.into_inner());
            i.wake.notify_one();
        }
    }

    pub fn drained(&self) -> bool {
        self.available_permits() == self.capacity
    }

    /// Total permits configured.
    pub fn capacity(&self) -> usize {
        self.capacity
    }

    /// Permits currently available.
    pub fn available_permits(&self) -> usize {
        self.permits.lock().unwrap_or_else(|e| e.into_inner()).free
    }

    /// How many acquisitions have had to block.
    pub fn wait_count(&self) -> usize {
        self.waits.load(Ordering::Relaxed)
    }

    fn release(&self) {
        let next = {
            let mut p = self.permits.lock().unwrap_or_else(|e| e.into_inner());
            p.free += 1;
            // Wake every waiter a permit is now free for (at most `free`,
            // first in line first), not one at a time.
            p.to_wake()
        };
        unpark(next);
    }
}

/// Returns its permit to the pool on drop.
///
/// Drop-based release is what makes this safe against handler panics: a
/// worker that unwinds still returns its permit, so a repeatedly-panicking
/// request path degrades throughput instead of permanently leaking the
/// pool down to zero and wedging the server.
pub struct Permit {
    admission: Arc<Admission>,
}

impl Drop for Permit {
    fn drop(&mut self) {
        self.admission.release();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn closing_wakes_waiters_and_waits_for_active_work() {
        let admission = Admission::new(1);
        let held = admission.acquire().unwrap();
        let other = admission.clone();
        let waiter = std::thread::spawn(move || other.acquire().is_none());
        admission.close();
        assert!(waiter.join().unwrap());
        assert!(!admission.drained());
        drop(held);
        assert!(admission.drained());
        assert!(admission.acquire().is_none());
    }

    // A saturated admission answers Busy within its
    // bound instead of blocking the dispatcher, and the job is not run.
    #[test]
    fn execute_within_is_busy_when_saturated() {
        let adm = Admission::new(1);
        let held = adm.acquire().expect("permit");
        let ran = Arc::new(AtomicBool::new(false));
        let r = Arc::clone(&ran);
        let started = std::time::Instant::now();
        assert_eq!(
            adm.execute_within(Duration::from_millis(50), move || r
                .store(true, Ordering::SeqCst)),
            Err(ExecError::Busy)
        );
        assert!(started.elapsed() < Duration::from_secs(2));
        assert!(!ran.load(Ordering::SeqCst), "a refused job never runs");
        assert_eq!(adm.available_permits(), 0, "the refusal took no permit");
        drop(held);
        let (tx, rx) = std::sync::mpsc::channel();
        adm.execute_within(Duration::from_secs(1), move || {
            let _ = tx.send(());
        })
        .expect("admitted");
        rx.recv_timeout(Duration::from_secs(5)).expect("job ran");
        adm.close();
        assert_eq!(
            adm.execute_within(Duration::from_millis(10), || {}),
            Err(ExecError::Closed)
        );
    }

    // Permits go to waiters in arrival order; a newly
    // arriving dispatcher does not take a freed permit ahead of one that
    // was already waiting.
    #[test]
    fn permits_are_granted_in_arrival_order() {
        let adm = Admission::new(1);
        let held = adm.acquire().expect("permit");
        let (tx, rx) = std::sync::mpsc::channel();
        let first = {
            let adm = Arc::clone(&adm);
            std::thread::spawn(move || {
                let p = adm.acquire_within(Duration::from_secs(5));
                let _ = tx.send(p.is_ok());
                p.map(|p| {
                    std::thread::sleep(Duration::from_millis(50));
                    drop(p);
                })
            })
        };
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while adm.waiting() != 1 {
            assert!(
                std::time::Instant::now() < deadline,
                "the first waiter never queued"
            );
            std::thread::sleep(Duration::from_millis(1));
        }
        drop(held);
        // Arrives after the release, before the waiter has run: it must
        // queue behind the waiter, not take the permit.
        assert_eq!(
            adm.acquire_within(Duration::ZERO).err(),
            Some(ExecError::Busy)
        );
        assert!(
            rx.recv_timeout(Duration::from_secs(5))
                .expect("first waiter answered"),
            "the waiter got the permit"
        );
        first.join().expect("join").expect("permit");
        assert_eq!(adm.available_permits(), 1);
    }

    // With every permit held and as many waiters,
    // releasing them all at once (before any waiter runs) grants every
    // waiter promptly, unbounded `acquire` included (tcp/server.rs,
    // rpc.rs): nobody is stranded behind a single wake.
    #[test]
    fn releasing_many_permits_at_once_wakes_as_many_waiters() {
        const N: usize = 4;
        let adm = Admission::new(N);
        let held: Vec<Permit> = (0..N).map(|_| adm.acquire().expect("permit")).collect();
        let (tx, rx) = std::sync::mpsc::channel();
        let ready = Arc::new(std::sync::Barrier::new(N + 1));
        let waiters: Vec<_> = (0..N)
            .map(|i| {
                let (adm, tx, ready) = (Arc::clone(&adm), tx.clone(), Arc::clone(&ready));
                std::thread::spawn(move || {
                    ready.wait();
                    // Half wait unbounded, half with a long bound.
                    let p = if i % 2 == 0 {
                        adm.acquire()
                    } else {
                        adm.acquire_within(Duration::from_secs(30)).ok()
                    };
                    let _ = tx.send(p.is_some());
                    p
                })
            })
            .collect();
        ready.wait();
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while adm.waiting() != N {
            assert!(std::time::Instant::now() < deadline, "waiters never queued");
            std::thread::sleep(Duration::from_millis(1));
        }
        drop(held);
        for _ in 0..N {
            assert!(rx
                .recv_timeout(Duration::from_secs(2))
                .expect("every waiter granted promptly"));
        }
        for w in waiters {
            drop(w.join().expect("join"));
        }
        assert_eq!(adm.available_permits(), N);
    }

    // A job that no worker can be started for is refused
    // by execute_within (the dispatcher answers NO_SLOT) instead of being
    // queued behind running jobs of unbounded length.
    #[test]
    fn execute_within_refuses_when_no_worker_can_start() {
        let adm = Admission::new(4);
        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();
        adm.execute(move || {
            let _ = release_rx.recv();
        })
        .expect("first job runs");
        assert_eq!(adm.worker_threads(), 1);
        adm.fail_spawn.store(true, Ordering::Relaxed);
        let ran = Arc::new(AtomicBool::new(false));
        let r = Arc::clone(&ran);
        let refused = adm.execute_within(Duration::from_millis(50), move || {
            r.store(true, Ordering::SeqCst)
        });
        assert!(matches!(refused, Err(ExecError::Spawn(_))), "{refused:?}");
        drop(release_tx);
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while adm.available_permits() != 4 {
            assert!(std::time::Instant::now() < deadline, "permits not returned");
            std::thread::sleep(Duration::from_millis(1));
        }
        std::thread::sleep(Duration::from_millis(20));
        assert!(
            !ran.load(Ordering::SeqCst),
            "the refused job was never queued"
        );
        adm.close();
    }

    #[test]
    fn permits_are_bounded_and_returned() {
        let adm = Admission::new(2);
        assert_eq!(adm.available_permits(), 2);

        let p1 = adm.acquire();
        let p2 = adm.acquire();
        assert_eq!(adm.available_permits(), 0);

        drop(p1);
        assert_eq!(adm.available_permits(), 1);
        drop(p2);
        assert_eq!(adm.available_permits(), 2);
    }

    #[test]
    fn acquire_blocks_until_a_permit_is_released() {
        let adm = Admission::new(1);
        let held = adm.acquire();

        let adm2 = Arc::clone(&adm);
        let waiter = std::thread::spawn(move || {
            let _p = adm2.acquire(); // must block until `held` drops
            std::time::Instant::now()
        });

        std::thread::sleep(Duration::from_millis(100));
        let released_at = std::time::Instant::now();
        drop(held);

        let acquired_at = waiter.join().expect("waiter");
        assert!(
            acquired_at >= released_at,
            "acquire returned before the permit was released — not bounded"
        );
        assert!(
            adm.wait_count() >= 1,
            "the blocked acquire should be counted"
        );
    }

    // The cap must hold under a burst far larger than capacity — this
    // is the "fast client pipelines requests" case that used to spawn
    // threads without limit.
    #[test]
    fn concurrency_never_exceeds_capacity_under_burst() {
        let adm = Admission::new(4);
        let live = Arc::new(AtomicUsize::new(0));
        let peak = Arc::new(AtomicUsize::new(0));

        let mut threads = Vec::new();
        for _ in 0..64 {
            let adm = Arc::clone(&adm);
            let live = Arc::clone(&live);
            let peak = Arc::clone(&peak);
            threads.push(std::thread::spawn(move || {
                let _permit = adm.acquire();
                let now = live.fetch_add(1, Ordering::SeqCst) + 1;
                peak.fetch_max(now, Ordering::SeqCst);
                std::thread::sleep(Duration::from_millis(2));
                live.fetch_sub(1, Ordering::SeqCst);
            }));
        }
        for t in threads {
            t.join().expect("worker");
        }

        assert!(
            peak.load(Ordering::SeqCst) <= 4,
            "peak concurrency {} exceeded capacity 4",
            peak.load(Ordering::SeqCst)
        );
        assert_eq!(adm.available_permits(), 4, "all permits returned");
    }

    /// Wait (up to 5 s) for `cond`.
    fn eventually(cond: impl Fn() -> bool) -> bool {
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while !cond() {
            if std::time::Instant::now() > deadline {
                return false;
            }
            std::thread::sleep(Duration::from_millis(1));
        }
        true
    }

    // Step 2: many jobs, few threads. 2,000 jobs through a cap of 8 run on
    // at most 8 worker threads, never more than 8 at once, and every
    // permit comes back.
    #[test]
    fn execute_reuses_at_most_capacity_workers() {
        let adm = Admission::new(8);
        let live = Arc::new(AtomicUsize::new(0));
        let peak = Arc::new(AtomicUsize::new(0));
        let done = Arc::new(AtomicUsize::new(0));
        let names = Arc::new(Mutex::new(std::collections::HashSet::new()));
        for _ in 0..2000 {
            let (live, peak, done, names) = (
                Arc::clone(&live),
                Arc::clone(&peak),
                Arc::clone(&done),
                Arc::clone(&names),
            );
            adm.execute(move || {
                let now = live.fetch_add(1, Ordering::SeqCst) + 1;
                peak.fetch_max(now, Ordering::SeqCst);
                names.lock().unwrap().insert(std::thread::current().id());
                std::thread::yield_now();
                live.fetch_sub(1, Ordering::SeqCst);
                done.fetch_add(1, Ordering::SeqCst);
            })
            .expect("execute");
        }
        assert!(
            eventually(|| done.load(Ordering::SeqCst) == 2000),
            "not every job ran"
        );
        assert!(
            peak.load(Ordering::SeqCst) <= 8,
            "peak {}",
            peak.load(Ordering::SeqCst)
        );
        assert!(
            names.lock().unwrap().len() <= 8,
            "{} threads ran jobs",
            names.lock().unwrap().len()
        );
        assert!(adm.worker_threads() <= 8);
        assert!(eventually(|| adm.drained()), "permits not returned");
    }

    // A full pool blocks the caller (backpressure), it does not queue
    // without bound.
    #[test]
    fn execute_blocks_while_every_permit_is_taken() {
        let adm = Admission::new(1);
        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();
        adm.execute(move || {
            let _ = release_rx.recv();
        })
        .expect("first job");
        let adm2 = Arc::clone(&adm);
        let second = std::thread::spawn(move || {
            adm2.execute(|| {}).expect("second job");
            std::time::Instant::now()
        });
        std::thread::sleep(Duration::from_millis(100));
        let released_at = std::time::Instant::now();
        release_tx.send(()).expect("release");
        assert!(
            second.join().expect("join") >= released_at,
            "admitted before a permit was free"
        );
        assert!(adm.wait_count() >= 1);
    }

    // LIFO hand-off: with 8 workers started, one submitter's jobs, one at
    // a time, all run on the worker that went idle last, not in turn.
    #[test]
    fn sequential_jobs_stay_on_the_most_recently_idle_worker() {
        let adm = Admission::new(8);
        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();
        let release_rx = Arc::new(Mutex::new(release_rx));
        for _ in 0..8 {
            let rx = Arc::clone(&release_rx);
            adm.execute(move || {
                let _ = rx.lock().unwrap().recv();
            })
            .expect("grow");
        }
        assert!(eventually(|| adm.worker_threads() == 8));
        for _ in 0..8 {
            release_tx.send(()).expect("release");
        }
        assert!(eventually(|| adm.drained()));
        let names = Arc::new(Mutex::new(std::collections::HashSet::new()));
        for _ in 0..200 {
            let (tx, rx) = std::sync::mpsc::sync_channel::<()>(1);
            let names = Arc::clone(&names);
            adm.execute(move || {
                names.lock().unwrap().insert(std::thread::current().id());
                let _ = tx.send(());
            })
            .expect("execute");
            rx.recv().expect("job ran");
            // Let the worker get back on the idle list before the next one.
            assert!(eventually(|| adm.workers.lock().unwrap().idle.len() == 8));
        }
        assert_eq!(
            names.lock().unwrap().len(),
            1,
            "jobs were spread over workers"
        );
        assert_eq!(adm.worker_threads(), 8);
    }

    #[test]
    fn a_panicking_job_returns_its_permit_and_keeps_its_worker() {
        let adm = Admission::new(1);
        adm.execute(|| panic!("handler blew up")).expect("execute");
        assert!(
            eventually(|| adm.drained()),
            "a panicking job leaked its permit"
        );
        let ran = Arc::new(AtomicBool::new(false));
        let r = Arc::clone(&ran);
        adm.execute(move || r.store(true, Ordering::SeqCst))
            .expect("execute after panic");
        assert!(eventually(|| ran.load(Ordering::SeqCst)));
        assert_eq!(adm.worker_threads(), 1, "the worker survived the panic");
    }

    #[test]
    fn close_refuses_new_jobs_finishes_admitted_ones_and_stops_workers() {
        let adm = Admission::new(4);
        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();
        let finished = Arc::new(AtomicBool::new(false));
        let f = Arc::clone(&finished);
        adm.execute(move || {
            let _ = release_rx.recv();
            f.store(true, Ordering::SeqCst);
        })
        .expect("execute");
        adm.close();
        assert_eq!(adm.execute(|| {}), Err(ExecError::Closed));
        release_tx.send(()).expect("release");
        assert!(
            eventually(|| finished.load(Ordering::SeqCst)),
            "an admitted job was dropped"
        );
        assert!(
            eventually(|| adm.worker_threads() == 0),
            "workers outlived close"
        );
        assert!(adm.drained());
    }

    // A job `execute` accepted runs even when `close` races the hand-off
    // to an idle worker: the worker must not see "closed, slot empty" and
    // exit while the job is on its way to its slot.
    #[test]
    fn every_accepted_job_runs_when_close_races_the_hand_off() {
        for _ in 0..300 {
            let adm = Admission::new(8);
            // Eight idle workers, so submissions take the hand-off path:
            // the warm-up jobs wait for each other, so each needs its own.
            let all = Arc::new(std::sync::Barrier::new(8));
            for _ in 0..8 {
                let all = Arc::clone(&all);
                adm.execute(move || {
                    all.wait();
                })
                .expect("warm up");
            }
            assert!(eventually(|| adm.workers.lock().unwrap().idle.len() == 8));
            let ran = Arc::new(AtomicUsize::new(0));
            let submitters: Vec<_> = (0..4)
                .map(|_| {
                    let (adm, ran) = (Arc::clone(&adm), Arc::clone(&ran));
                    std::thread::spawn(move || {
                        let mut accepted = 0;
                        for _ in 0..16 {
                            let r = Arc::clone(&ran);
                            match adm.execute(move || {
                                r.fetch_add(1, Ordering::SeqCst);
                            }) {
                                Ok(()) => accepted += 1,
                                Err(_) => break,
                            }
                        }
                        accepted
                    })
                })
                .collect();
            adm.close();
            let accepted: usize = submitters
                .into_iter()
                .map(|h| h.join().expect("join"))
                .sum();
            assert!(
                eventually(|| adm.worker_threads() == 0),
                "workers outlived close"
            );
            assert_eq!(
                ran.load(Ordering::SeqCst),
                accepted,
                "an accepted job never ran"
            );
            assert!(adm.drained());
        }
    }

    #[test]
    fn permit_is_returned_even_if_the_worker_panics() {
        let adm = Admission::new(1);
        let adm2 = Arc::clone(&adm);
        let _ = std::thread::spawn(move || {
            let _permit = adm2.acquire();
            panic!("handler blew up");
        })
        .join();

        assert_eq!(
            adm.available_permits(),
            1,
            "a panicking handler must not leak its permit"
        );
    }
}
