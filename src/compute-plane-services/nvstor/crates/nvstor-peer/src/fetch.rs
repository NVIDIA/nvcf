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

//! Copy a peer export's directory tree into a local directory.
//!
//! The manifest is fetched first; then every file's ranges go into one
//! queue that a fixed set of workers drain, each over its own
//! long-lived source (one connection, one registered buffer). Thousands
//! of small files therefore cost no connection setup, and a few large
//! ones still spread over every worker.
//!
//! Contract per file, as in `nvstor_rdma::hydrate`: every range is
//! pinned to the generation in the manifest and checked by the transport
//! (CRC-32C over RDMA), and only a complete file is given its
//! attributes, synced and renamed to its final name. Files already
//! renamed are complete; on failure nothing else is left behind.
//! Directories are given their attributes and fsynced at the end.
//!
//! The destination is written as a tree someone else may also write to
//! (a shared model cache, another container on the node):
//!
//! - Bytes are staged in a private directory, `dest/.nvstor-peer-staging-
//!   <random>` (mode 0700), one file per target named by its index, so a
//!   staging file can never be another manifest entry, and two fetches
//!   never share one. The staging directory is removed on success and on
//!   any error, including one while files are being created; a fetch
//!   that crashed leaves one behind, which the next fetch into the same
//!   `dest` removes.
//! - A fetch holds `flock(LOCK_EX)` on `dest` and a second fetch into it
//!   fails at once instead of racing the first to the final names.
//! - Nothing under `dest` is resolved through a symlink: directories are
//!   created and opened with `openat2(RESOLVE_BENEATH |
//!   RESOLVE_NO_SYMLINKS)` relative to `dest`, and a file is put in place
//!   with `renameat`, which replaces whatever name is there without
//!   writing through it. An existing entry that is neither a regular
//!   file (where a file goes) nor a directory (where a directory goes)
//!   fails the fetch before any transfer.
//! - A file whose path the transport cannot carry (longer than
//!   `max_key_len`) or that uses the staging name is reported per file in
//!   [`FetchError::Incomplete`]; everything else is still fetched.
//!
//! Sources ([`SourceSet`]): a fetch may name several nodes that serve the
//! same tree (the original and replicas that finished their own copy).
//! It tries them in an order shuffled by `FetchOptions::seed` (so many
//! fetchers spread over the list), moves on when one answers BUSY (its
//! fetcher cap is full) or cannot be reached, and backs off with jitter
//! while every one is busy, for at most `FetchOptions::wait`. When the
//! source in use fails mid-copy (its retries are spent), the fetch picks
//! another, fetches its manifest, and checks that every file not yet
//! complete has the same size and `content` identity there; it then goes
//! on with the remaining ranges, pinned to the new source's generations.
//! Files already renamed into place stay. A source whose content differs
//! is never used ([`FetchError::Mismatch`] when none is left).
//!
//! After the copy, the files fetched are recorded with their content
//! identity in `dest` (`crate::content`), so the destination can be
//! served to the next fetchers under the same identity.
//!
//! Attributes (`FetchOptions::attrs`, on by default; see
//! `crate::manifest` for what a manifest carries):
//!
//! - The first source is asked for its version 3 listing
//!   (`manifest::key_v3`). One without it (an older nvstor-peer, whose
//!   version 2 listing under `manifest::key` still answers) is refused by
//!   name, and the fetch moves on to another source or fails. With
//!   `attrs` off a version 3 listing is still preferred (it lists empty
//!   directories) and a version 2 one taken. A failover replacement is
//!   asked for version 2, which every source serves: it must match sizes
//!   and content identities only, and attributes always come from the
//!   first source's listing, so one listing's attributes go on the whole
//!   tree.
//! - Staging files are created 0600 and directories 0700, whatever is
//!   applied: a file is private until it is complete, and stays so when
//!   no mode is applied (`attrs` off, or a source that is a copy without
//!   modes; the caller sets modes then).
//! - When a file is complete, on its open descriptor and before the
//!   rename: `futimens` with the listed mtime (the atime is left as the
//!   time of the copy: the manifest does not carry one) and `fchmod` to
//!   the listed mode without set-id bits, while the caller still owns the
//!   file (so neither needs CAP_FOWNER); then `fchown` to the listed
//!   numeric owner and group, when owners are applied (it changes ctime,
//!   not mtime); then, only when set-id bits are kept, `fchmod` again
//!   with them (the chown cleared them; this needs CAP_FOWNER, and a
//!   refusal fails the fetch naming `--preserve-setid`); then `fsync` (so
//!   the attributes are as durable as the bytes) and the rename. Any of
//!   these failing fails the file: it never appears in place with other
//!   attributes. A chown the system refuses (EPERM, EINVAL: a user
//!   namespace without the ids mapped, NFS root_squash) ends the fetch at
//!   once with an error naming `--no-owner`.
//! - Owners are applied when `FetchOptions::owners`, the process has
//!   CAP_CHOWN, CAP_DAC_OVERRIDE and CAP_FOWNER in its effective set (not
//!   merely uid 0; read with capget(2); [`OWNER_CAPABILITIES`]: a
//!   directory's end record and final mode come after its chown), every
//!   uid and gid the source lists is mapped in this user namespace, and
//!   the source's
//!   listing says its owners are the tree's own. Modes likewise need the
//!   source's authority over them. [`FetchReport::attributes`] says what
//!   was applied and, if not, why.
//! - Modes are hardened: set-user-ID and set-group-ID bits are dropped,
//!   from files and directories, unless `FetchOptions::preserve_setid`
//!   and the listed owner is applied to that very file (never on a file
//!   the copier owns: 04755 of uid 1000 must not become set-user-ID
//!   root); `preserve_setid` without `owners` is refused. Write for
//!   others is dropped from a directory without the sticky bit.
//! - Every directory the manifest lists is created, empty ones included.
//!   An existing one of the caller's without owner rwx (a 0555 directory
//!   of an earlier copy) gets it for the copy. After every file is in
//!   place, each listed directory gets its owner, mode and mtime (or, when
//!   none are applied, an opened-up one its old mode back), deepest first,
//!   on a descriptor opened beneath `dest` without following symlinks,
//!   and is fsynced: no rename into it moves its mtime afterwards, and a
//!   mode without search or write permission applies only once nothing
//!   below it is opened again. A directory that fails here does not stop
//!   the pass; the first error fails the fetch once every other directory
//!   has its attributes. `dest` itself keeps its attributes. A
//!   directory that cannot be created or opened (EACCES, a name the
//!   filesystem refuses) is reported per entry with everything below it,
//!   and the copy goes on; so is a path longer than PATH_MAX or with a
//!   name longer than NAME_MAX.
//! - After a failed fetch the same directory pass runs, as far as it
//!   can: the directories created so far get their listed attributes,
//!   opened-up ones their old mode. Files already renamed are complete,
//!   with their attributes; nothing else of the copy is left.
//! - Every fetch rewrites every file (there is no skip of files already
//!   in place), so a copy fetched again gets the source's attributes
//!   back, even where they were changed locally.
//! - The fetch records what it applied (`crate::content`), so a copy
//!   served with `--then-serve` lists its attributes with the authority
//!   they have: a copy made without owners does not hand the copier's uid
//!   to the next fetcher as the origin's. Authority is the AND of every
//!   record from a directory up to `/` (`content::authority_above`), and
//!   any error on the way gives none; the fetch writes its own record
//!   with the same rule, so a fetch into a directory of a reduced copy
//!   never raises it. The record goes into DEST and into every directory
//!   of the copy that has something to say (before anything is renamed
//!   into it, and at the end before its mtime is set), so any directory
//!   of a copy served on its own carries it. A directory's record is the
//!   fetch's AND every record at or above it, its own earlier one
//!   included; only a directory this fetch created may end higher than
//!   it started. Start records are synced (in parallel) before any
//!   rename, end records after the final pass: after a crash a record is
//!   never higher than its final value. Without the lock DEST is never
//!   taken for empty, no directory it made is taken for all its own (so
//!   no record ends higher than it started), and no stale temporary is
//!   removed. A directory's end record is written after its owner and
//!   mode and before its mtime (`finish_dir`): a crash or a refused
//!   chown leaves its start record.
//! - A directory whose chown fails is put back to 0700. Set-id bits are
//!   read back after they are set: [`FetchReport::setid_cleared`] counts
//!   those the kernel cleared without an error (S_ISGID, no CAP_FSETID).
//! - A transport error that outlasts the retries on a listing is a failed
//!   open of that source (tried again in later rounds), never a sign of
//!   an older source or a refused manifest.
//!
//! Writes whose offset, length and buffer are 4 KiB-aligned use O_DIRECT
//! (the source's registered buffer goes straight to the device); the
//! tail of each file, or a filesystem without O_DIRECT, is written
//! buffered.

use std::collections::{HashMap, HashSet};
use std::ffi::CString;
use std::fs::File;
use std::io;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::fs::FileExt;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use nvstor_rdma::hydrate::{HydrateError, RangeSource};
use nvstor_rdma::object::GetError;
use nvstor_rdma::region::Landed;
use parking_lot::{Mutex, RwLock};
use tracing::{debug, warn};

use crate::content::{self, RESERVED_PREFIX};
use crate::dirfd;
use crate::manifest::{self, Attrs, Authority, Entry, Manifest};

const ALIGN: usize = 4096;
/// Times the manifest is refetched when the export changes while it is
/// being read.
const MANIFEST_ATTEMPTS: u32 = 3;
/// Name prefix of a fetch's private staging directory under `dest`. An
/// export never lists directories with it, and a manifest entry with a
/// component starting with it is refused per file.
pub const STAGING_PREFIX: &str = ".nvstor-peer-staging-";
/// Failed files named in an [`FetchError::Incomplete`] message.
const FAILED_SHOWN: usize = 8;

#[derive(Clone, Debug)]
pub struct FetchOptions {
    /// Parallel workers, one source (connection) each.
    pub connections: usize,
    /// Bytes per range; a multiple of 4096. Capped by the source.
    pub range_size: usize,
    /// Attempts per range after the first, for retryable errors and
    /// failed reconnects alike.
    pub max_retries: u32,
    /// Delay before the first retry; doubles per attempt.
    pub retry_backoff: Duration,
    /// Write aligned ranges with O_DIRECT.
    pub direct_io: bool,
    /// Largest manifest accepted from the server. The size comes from
    /// the server, so it is checked before anything is allocated for it.
    pub max_manifest_bytes: u64,
    /// Longest key the source can request. Files with longer paths are
    /// not requested and are reported per file.
    pub max_key_len: usize,
    /// Longest a fetch waits for a source to take it, at the start or
    /// after losing one, while every source is busy or unreachable.
    pub wait: Duration,
    /// Shuffles the source order and the backoff jitter; give every
    /// fetcher its own (the CLI hashes the host name) so they spread.
    pub seed: u64,
    /// First pause after a round in which no source took the fetch;
    /// doubles per round up to `max_busy_backoff`, with jitter.
    pub busy_backoff: Duration,
    pub max_busy_backoff: Duration,
    /// Give every file and directory the mode and mtime from the
    /// manifest, and the owner and group too when `owners` and the
    /// process has every one of [`OWNER_CAPABILITIES`] (see the module
    /// docs). A source that serves
    /// no version 3 manifest (an older nvstor-peer) is then refused. Off:
    /// bytes only; files stay 0600 and created directories 0700, the copy
    /// time, the caller's ownership.
    pub attrs: bool,
    /// With `attrs`: also owners and groups (when the process may chown).
    /// Off: mode and mtime only, the caller's ownership.
    pub owners: bool,
    /// Keep set-user-ID and set-group-ID bits, of files and directories,
    /// where the listed owner is applied too (dropped by default: a copy
    /// should not hand out privileges or group inheritance on its own, and
    /// never on a file the copier owns). Needs `owners`.
    pub preserve_setid: bool,
}

impl Default for FetchOptions {
    fn default() -> Self {
        Self {
            connections: 32,
            range_size: 16 << 20,
            max_retries: 5,
            retry_backoff: Duration::from_millis(50),
            direct_io: true,
            max_manifest_bytes: 256 << 20,
            max_key_len: nvstor_rdma::s3_rdma_proto::MAX_KEY_LEN,
            wait: Duration::from_secs(1800),
            seed: 0,
            busy_backoff: Duration::from_millis(250),
            max_busy_backoff: Duration::from_secs(5),
            attrs: true,
            owners: true,
            preserve_setid: false,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FetchReport {
    /// Files fetched (renamed into place).
    pub files: usize,
    pub bytes: u64,
    pub ranges: u64,
    pub retries: u64,
    pub reconnects: u64,
    /// The source the copy started from.
    pub source: String,
    /// Times the fetch moved to another source mid-copy.
    pub failovers: u64,
    /// Sources dropped because a file changed on them mid-copy: the copy
    /// was finished from others holding the content it started from.
    pub changed_on: Vec<String>,
    /// `manifest::content_digest` of the manifest copied: equal for every
    /// complete copy of the same tree, whichever source served it.
    pub content_digest: String,
    /// Which attributes the copy was given.
    pub attributes: Attributes,
    /// Set-id bits were kept (`preserve_setid` and owners applied).
    pub setid_kept: bool,
    /// Files and directories whose set-id bits the kernel cleared anyway
    /// (checked with `fstat` after setting them).
    pub setid_cleared: u64,
    /// Whole fetch, manifest and final syncs included.
    pub elapsed: Duration,
}

/// Whether a fetch gave the tree one kind of attribute, and if not, why.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Applied {
    Yes,
    /// Turned off: `--no-attrs` (modes), `--no-owner` (owners).
    Off,
    /// The source's listing says its tree does not really have them (it
    /// was copied without them), so they are not passed on.
    NotAuthoritative,
    /// Owners only: the process lacks this capability, one of those
    /// giving files and directories to other users takes
    /// ([`OWNER_CAPABILITIES`]).
    NoCapability(&'static str),
    /// Owners only: a uid or gid the source lists has no mapping in this
    /// process's user namespace (a chown to it would fail).
    IdsNotMapped,
}

/// The attributes a fetch gives the files and directories it creates.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Attributes {
    /// Mode and mtime. When not applied, files stay 0600 and created
    /// directories 0700.
    pub modes: Applied,
    /// Owner and group. Never applied without `modes`.
    pub owners: Applied,
}

impl Attributes {
    /// For the summary line.
    pub fn describe(self) -> String {
        const PRIVATE: &str = "files 0600, directories 0700";
        match (self.modes, self.owners) {
            (Applied::Yes, Applied::Yes) => "mode+mtime+owner".into(),
            (Applied::Yes, owners) => format!(
                "mode+mtime (owner kept: {})",
                match owners {
                    Applied::Off => "--no-owner".to_string(),
                    Applied::NotAuthoritative => "owner not authoritative at source".to_string(),
                    Applied::NoCapability(cap) => format!("no {cap}"),
                    Applied::IdsNotMapped => "ids not mapped in this user namespace".to_string(),
                    Applied::Yes => String::new(),
                }
            ),
            (Applied::NotAuthoritative, _) => {
                format!("none (not authoritative at source; {PRIVATE})")
            }
            _ => format!("none (--no-attrs; {PRIVATE})"),
        }
    }

    /// What the tree has after this fetch, as recorded for the next
    /// fetcher (`crate::content`).
    pub fn authority(self) -> Authority {
        Authority {
            modes: self.modes == Applied::Yes,
            owners: self.owners == Applied::Yes,
        }
    }
}

/// What giving files and directories to their listed owners takes, by
/// capability bit: the chown itself, and, for a directory whose end
/// record and final mode come after its chown (`finish_dir`), writing
/// into and chmod-ing a directory that is no longer this process's. Root
/// has all three.
pub const OWNER_CAPABILITIES: [(u32, &str); 3] =
    [(0, "CAP_CHOWN"), (1, "CAP_DAC_OVERRIDE"), (3, "CAP_FOWNER")];

/// Whether this process may apply owners, decided up front: every one of
/// [`OWNER_CAPABILITIES`] in its effective set (read with capget(2)), not
/// a uid of 0, which says nothing about a root without capabilities.
/// `Err` names the first one missing; owners are then not applied, and
/// nothing fails for it.
fn owner_capability() -> Result<(), &'static str> {
    // Tests decide it themselves, so they mean the same as root and as
    // any other user.
    #[cfg(test)]
    match tests::OWNER_CAPS.with(|c| c.get()) {
        tests::Caps::Missing => return Err(OWNER_CAPABILITIES[0].1),
        tests::Caps::Present => return Ok(()),
        tests::Caps::Real => {}
    }
    owner_capability_from(effective_capabilities())
}

/// The decision of [`owner_capability`] from the effective set (`None`
/// when it could not be read: no owners).
fn owner_capability_from(caps: Option<u64>) -> Result<(), &'static str> {
    let caps = caps.ok_or("readable capabilities")?;
    match OWNER_CAPABILITIES
        .iter()
        .find(|(bit, _)| caps & (1u64 << bit) == 0)
    {
        Some((_, name)) => Err(name),
        None => Ok(()),
    }
}

/// This thread's effective capability set, from capget(2) (version 3:
/// two 32-bit words); `None` if the call fails.
fn effective_capabilities() -> Option<u64> {
    #[repr(C)]
    struct Header {
        version: u32,
        pid: libc::c_int,
    }
    #[repr(C)]
    #[derive(Clone, Copy)]
    struct Data {
        effective: u32,
        permitted: u32,
        inheritable: u32,
    }
    const VERSION_3: u32 = 0x2008_0522;
    let mut header = Header {
        version: VERSION_3,
        pid: 0,
    };
    let mut data = [Data {
        effective: 0,
        permitted: 0,
        inheritable: 0,
    }; 2];
    // SAFETY: a version 3 header and the two data words it fills.
    let r = unsafe {
        libc::syscall(
            libc::SYS_capget,
            &mut header as *mut Header,
            data.as_mut_ptr(),
        )
    };
    (r == 0).then(|| u64::from(data[0].effective) | (u64::from(data[1].effective) << 32))
}

/// Whether every uid and every gid the listing names is mapped in this
/// process's user namespace (`/proc/self/uid_map`, `gid_map`). A chown to
/// an unmapped id fails with EINVAL; finding that up front turns owners
/// off instead of failing the fetch. Maps that cannot be read count as
/// not mapping anything.
fn ids_mapped(entries: &[Entry]) -> bool {
    let read = |name: &str| -> Option<String> {
        #[cfg(test)]
        if let Some(text) = tests::ID_MAP.with(|m| m.borrow().clone()) {
            return Some(text);
        }
        std::fs::read_to_string(format!("/proc/self/{name}")).ok()
    };
    let (Some(uids), Some(gids)) = (read("uid_map"), read("gid_map")) else {
        return false;
    };
    let mut seen = std::collections::HashSet::new();
    entries.iter().all(|e| {
        !seen.insert((e.attrs.uid, e.attrs.gid))
            || (id_in_map(&uids, e.attrs.uid) && id_in_map(&gids, e.attrs.gid))
    })
}

/// `id` falls in one of the map's `inside outside count` ranges.
fn id_in_map(map: &str, id: u32) -> bool {
    map.lines().any(|line| {
        let mut f = line.split_whitespace().map(|x| x.parse::<u64>());
        match (f.next(), f.next(), f.next()) {
            (Some(Ok(inside)), Some(Ok(_)), Some(Ok(count))) => {
                (inside..inside.saturating_add(count)).contains(&u64::from(id))
            }
            _ => false,
        }
    })
}

/// How attributes are applied, fixed for the whole fetch.
#[derive(Clone, Copy, Debug)]
struct Apply {
    attributes: Attributes,
    preserve_setid: bool,
}

impl Apply {
    /// What a fetch with `opts` gives the tree from a source whose listing
    /// has `listing` authority (`None`: it has no attributes at all).
    fn new(opts: &FetchOptions, listing: Option<Authority>, entries: &[Entry]) -> Self {
        let modes = match listing {
            _ if !opts.attrs => Applied::Off,
            Some(a) if a.modes => Applied::Yes,
            _ => Applied::NotAuthoritative,
        };
        let owners = match listing {
            _ if modes != Applied::Yes => modes,
            _ if !opts.owners => Applied::Off,
            Some(a) if !a.owners => Applied::NotAuthoritative,
            _ => match owner_capability() {
                Err(cap) => Applied::NoCapability(cap),
                Ok(()) if !ids_mapped(entries) => Applied::IdsNotMapped,
                Ok(()) => Applied::Yes,
            },
        };
        Self {
            attributes: Attributes { modes, owners },
            preserve_setid: opts.preserve_setid,
        }
    }

    fn on(self) -> bool {
        self.attributes.modes == Applied::Yes
    }

    /// Whether set-id bits are kept: asked for, and the listed owner is
    /// applied too. A set-user-ID file is only as safe as its owner: kept
    /// on a file the copier owns (no CAP_CHOWN, `--no-owner`, or owners
    /// the source does not vouch for), 04755 of uid 1000 would become
    /// set-user-ID of the copier, root included.
    fn keeps_setid(self) -> bool {
        self.preserve_setid && self.attributes.owners == Applied::Yes
    }

    /// The mode to give, without set-id bits (see [`Apply::steps`] for
    /// when they come back): a directory's write for others is dropped
    /// unless it is sticky (world-writable without the sticky bit lets
    /// anyone delete anyone's files).
    fn base_mode(listed: u32, is_dir: bool) -> u32 {
        let mut mode = listed & manifest::MODE_BITS & !(libc::S_ISUID | libc::S_ISGID);
        if is_dir && mode & libc::S_ISVTX == 0 {
            mode &= !libc::S_IWOTH;
        }
        mode
    }

    /// What giving `a` takes, in order: the mtime and the mode (without
    /// set-id bits) while the caller still owns the file, so neither needs
    /// CAP_FOWNER; then the owner (a chown changes ctime, not mtime); then,
    /// only when they are kept, the set-id bits, after the chown (which
    /// would clear them).
    fn steps(self, a: &Attrs, is_dir: bool) -> Vec<Step> {
        let base = Self::base_mode(a.mode, is_dir);
        let mut steps = vec![Step::Mtime(a.mtime_sec, a.mtime_nsec), Step::Mode(base)];
        if self.attributes.owners == Applied::Yes {
            steps.push(Step::Owner(a.uid, a.gid));
        }
        let setid = a.mode & (libc::S_ISUID | libc::S_ISGID);
        if self.keeps_setid() && setid != 0 {
            steps.push(Step::SetidMode(base | setid));
        }
        steps
    }

    /// Give the open file or directory `fd` the attributes `a`
    /// ([`Apply::steps`]). Only called when modes are applied. A chown the
    /// system refuses (EPERM, EINVAL: a user namespace without the ids
    /// mapped, NFS root_squash) is reported as such, naming `--no-owner`;
    /// set-id bits it may not set (no CAP_FOWNER), naming
    /// `--preserve-setid`.
    ///
    /// Returns whether set-id bits that were to be kept did not stick: the
    /// kernel clears S_ISGID without a word for a process without
    /// CAP_FSETID when the group is not one of its own.
    fn to_fd(self, fd: &impl AsRawFd, a: &Attrs, is_dir: bool) -> io::Result<bool> {
        let raw = fd.as_raw_fd();
        let mut cleared = false;
        let check = |r: libc::c_int| {
            if r == 0 {
                Ok(())
            } else {
                Err(io::Error::last_os_error())
            }
        };
        for step in self.steps(a, is_dir) {
            match step {
                Step::Mtime(sec, nsec) => {
                    let times = [libc::timespec { tv_sec: 0, tv_nsec: libc::UTIME_OMIT },
                                 libc::timespec { tv_sec: sec, tv_nsec: i64::from(nsec) }];
                    // SAFETY: valid descriptor and two timespecs.
                    check(unsafe { libc::futimens(raw, times.as_ptr()) })?;
                }
                // SAFETY: valid descriptor.
                Step::Mode(mode) => check(unsafe { libc::fchmod(raw, mode) })?,
                // SAFETY: valid descriptor.
                Step::Owner(uid, gid) => check(unsafe { libc::fchown(raw, uid, gid) }).map_err(|e| {
                    match e.raw_os_error() {
                        Some(libc::EPERM | libc::EINVAL) => io::Error::new(e.kind(), format!(
                            "cannot give it owner {uid}:{gid} ({e}): this process may not set owners here (a \
                             user namespace without those ids mapped, NFS root_squash); fetch with --no-owner")),
                        _ => e,
                    }
                })?,
                // SAFETY: valid descriptor.
                Step::SetidMode(mode) => {
                    // SAFETY: valid descriptor.
                    check(unsafe { libc::fchmod(raw, mode) }).map_err(|e| match e.raw_os_error() {
                        Some(libc::EPERM) => io::Error::new(e.kind(), format!(
                            "cannot keep its set-id bits ({mode:o}, {e}): that needs CAP_FOWNER once the file \
                             is someone else's; fetch without --preserve-setid")),
                        _ => e,
                    })?;
                    #[cfg(test)]
                    tests::kernel_clears_setid(raw);
                    // What stuck, not what was asked for.
                    // SAFETY: `st` is written by fstat before it is read.
                    let mut st: libc::stat = unsafe { std::mem::zeroed() };
                    // SAFETY: valid descriptor and out pointer.
                    check(unsafe { libc::fstat(raw, &mut st) })?;
                    let want = mode & (libc::S_ISUID | libc::S_ISGID);
                    cleared |= st.st_mode & want != want;
                }
            }
        }
        Ok(cleared)
    }
}

/// One change [`Apply::to_fd`] makes, in the order it makes them.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Step {
    Mtime(i64, u32),
    Mode(u32),
    Owner(u32, u32),
    SetidMode(u32),
}

#[derive(Debug)]
pub enum FetchError {
    /// Could not open or reopen a source.
    Connect(String),
    /// The manifest could not be fetched or is malformed.
    Manifest(String),
    /// A range failed for good.
    Get {
        path: String,
        offset: u64,
        error: GetError,
    },
    /// A file changed on the source during the fetch.
    Changed {
        path: String,
    },
    /// Local file I/O failed.
    Io {
        path: PathBuf,
        error: io::Error,
    },
    /// Another fetch is writing into the same destination.
    Locked {
        dest: PathBuf,
    },
    /// No source took the fetch within `FetchOptions::wait`: every one
    /// was busy (at its fetcher cap) or unreachable.
    Unavailable(String),
    /// The sources left do not serve the same content as the one the
    /// copy started from.
    Mismatch(String),
    /// Every source refused this fetch's credentials in the bootstrap
    /// (wrong `NVSTOR_PEER_SECRET`); see [`OpenError::Refused`].
    Refused(String),
    /// The sources ran out because data from them failed its CRC-32C
    /// check after every retry, or their files were shorter than their
    /// manifests said.
    Corrupt(String),
    /// A source's file ended before the size its manifest gave. Reported
    /// per source (it is retired, as for bad data); a fetch whose sources
    /// all fail this way ends with [`FetchError::Corrupt`].
    Truncated {
        path: String,
        offset: u64,
        size: u64,
    },
    /// Every other file was fetched (`report`), but these could not be:
    /// (manifest path, reason).
    Incomplete {
        report: Box<FetchReport>,
        failed: Vec<(String, String)>,
    },
    /// The device a fetch lands in ([`fetch_to_device`]) failed: memory,
    /// a copy, or its disk writer.
    Device(String),
    /// A fetch into a device was refused before any transfer: some files
    /// cannot be fetched (paths the transport cannot request), and a
    /// device copy is all or nothing.
    NotAll(String),
    Invalid(String),
}

impl std::fmt::Display for FetchError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            FetchError::Connect(e) => write!(f, "connect: {e}"),
            FetchError::Manifest(e) => write!(f, "manifest: {e}"),
            FetchError::Get {
                path,
                offset,
                error,
            } => write!(f, "{path} at {offset}: {error}"),
            FetchError::Changed { path } => {
                write!(f, "{path} changed on the source during the fetch")
            }
            FetchError::Io { path, error } => write!(f, "{}: {error}", path.display()),
            FetchError::Locked { dest } => write!(
                f,
                "{}: another nvstor-peer fetch is writing into this directory",
                dest.display()
            ),
            FetchError::Unavailable(e) => write!(f, "no source available: {e}"),
            FetchError::Mismatch(e) => write!(f, "sources disagree on the content: {e}"),
            FetchError::Refused(e) => {
                write!(f, "every source refused this fetch's credentials: {e}")
            }
            FetchError::Corrupt(e) => write!(f, "data failed its checksum after every retry: {e}"),
            FetchError::Truncated { path, offset, size } => write!(
                f,
                "{path}: the source's file ended at {offset}, its manifest says {size} bytes"
            ),
            FetchError::Incomplete { report, failed } => {
                write!(
                    f,
                    "{} files could not be fetched ({} were):",
                    failed.len(),
                    report.files
                )?;
                for (path, why) in failed.iter().take(FAILED_SHOWN) {
                    write!(f, "\n  {path}: {why}")?;
                }
                if failed.len() > FAILED_SHOWN {
                    write!(f, "\n  and {} more", failed.len() - FAILED_SHOWN)?;
                }
                Ok(())
            }
            FetchError::Device(e) => write!(f, "device: {e}"),
            FetchError::NotAll(e) => write!(f, "{e}"),
            FetchError::Invalid(e) => write!(f, "{e}"),
        }
    }
}

impl std::error::Error for FetchError {}

fn connect_err(e: HydrateError) -> FetchError {
    FetchError::Connect(e.to_string())
}

fn io_err(path: &Path) -> impl FnOnce(io::Error) -> FetchError + '_ {
    move |error| FetchError::Io {
        path: path.to_path_buf(),
        error,
    }
}

/// One file of the fetch.
struct Target {
    key: String,
    size: u64,
    /// The first source's generation for it.
    generation: u128,
    /// Identity of the bytes (the manifest's `content`), the same on
    /// every source that may serve this file.
    content: u128,
    /// Path under `dest`.
    rel: String,
    /// From the manifest the copy started from (never a failover
    /// source's), applied before the rename.
    attrs: Attrs,
    /// Name of its file in the staging directory.
    staging: CString,
    /// Ranges not yet written; the worker that writes the last one
    /// finalizes the file.
    remaining: AtomicU64,
    /// Renamed into place.
    done: AtomicBool,
    /// Writes from a source view older than this epoch are refused: the
    /// file is being refetched whole from a newer source because an
    /// older one served it inconsistently (shorter than its manifest).
    min_epoch: AtomicU64,
    /// Held shared by every write, exclusively while `min_epoch` is
    /// raised, so no write from an older view lands after the refetch.
    rewrite: RwLock<()>,
    /// Held by the one scanner refetching it whole
    /// (`Shared::refetch_written_by`): one staging file and one finalize at
    /// a time among scanners. The worker whose current file it is may
    /// refetch it at the same time (same bytes from a view at or above the
    /// bar; wasted work, not a second staging file or finalize).
    refetching: Mutex<()>,
    /// Its set-id bits were found cleared by the kernel and counted:
    /// a file fetched again is counted once.
    setid_counted: AtomicBool,
    /// Sources that wrote any of its bytes, one bit per lineage (one per
    /// server; lineages from 63 on share the last bit, which can only
    /// cause an extra refetch).
    writers: AtomicU64,
}

/// Tests: key -> pause; a write of that file waits this long after it is
/// admitted, as a slow disk would. Each test uses its own file names.
#[cfg(test)]
pub(crate) static PAUSE_WRITE: Mutex<Vec<(String, Duration)>> = Mutex::new(Vec::new());

#[cfg(test)]
fn pause_write(key: &str) {
    let pause = PAUSE_WRITE
        .lock()
        .iter()
        .find(|(k, _)| k == key)
        .map(|(_, d)| *d);
    if let Some(d) = pause {
        std::thread::sleep(d);
    }
}

/// Tests: key -> pause; a scanner holding that file's refetch lock waits
/// this long after its last check of the bar and before letting go, so a
/// test can raise the bar exactly in that window.
#[cfg(test)]
pub(crate) static PAUSE_BEFORE_UNLOCK: Mutex<Vec<(String, Duration)>> = Mutex::new(Vec::new());

#[cfg(test)]
fn pause_before_unlock(key: &str) {
    let pause = PAUSE_BEFORE_UNLOCK
        .lock()
        .iter()
        .find(|(k, _)| k == key)
        .map(|(_, d)| *d);
    if let Some(d) = pause {
        std::thread::sleep(d);
    }
}

/// The bit of `Target::writers` for source lineage `lineage`.
fn lineage_bit(lineage: u64) -> u64 {
    1 << lineage.min(63)
}

impl Target {
    /// Permission to write ranges served by the source view `epoch`,
    /// held for the write: `None` once the file is being refetched from a
    /// newer view (see [`Target::refetch_from`]).
    fn write_guard(&self, epoch: u64) -> Option<parking_lot::RwLockReadGuard<'_, ()>> {
        let shared = self.rewrite.read();
        (epoch >= self.min_epoch.load(Ordering::Acquire)).then_some(shared)
    }

    /// Refuse writes from views older than `epoch` from now on. Waits for
    /// writes in progress, so none of them lands after the refetch.
    ///
    /// Returns whether this call raised the bar: the caller that did
    /// claims the refetch from `epoch`; any other leaves it to that one.
    fn refetch_from(&self, epoch: u64) -> bool {
        let _exclusive = self.rewrite.write();
        self.min_epoch.fetch_max(epoch, Ordering::AcqRel) < epoch
    }

    /// (parent directory under `dest`, file name).
    fn split(&self) -> (&str, &str) {
        self.rel.rsplit_once('/').unwrap_or(("", &self.rel))
    }
}

/// One range of one file.
struct Work {
    target: usize,
    offset: u64,
    len: u64,
}

/// The fetch's private staging directory under `dest`. Dropping it
/// removes it with whatever is still in it: the files not yet renamed
/// into place.
struct Staging<'a> {
    dest: &'a OwnedFd,
    name: String,
    dir: OwnedFd,
}

impl<'a> Staging<'a> {
    fn create(dest: &'a OwnedFd, dest_path: &Path) -> Result<Self, FetchError> {
        let path = |name: &str| dest_path.join(name);
        loop {
            let mut r = [0u8; 8];
            // SAFETY: the pointer and length name `r`.
            if unsafe { libc::getrandom(r.as_mut_ptr().cast(), r.len(), 0) } != r.len() as isize {
                return Err(io_err(dest_path)(io::Error::last_os_error()));
            }
            let name = format!("{STAGING_PREFIX}{:016x}", u64::from_le_bytes(r));
            let c = dirfd::cstring(name.as_bytes()).map_err(io_err(dest_path))?;
            match dirfd::mkdir_new_at(dest, &c, 0o700) {
                Ok(()) => {}
                Err(e) if e.raw_os_error() == Some(libc::EEXIST) => continue,
                Err(e) => return Err(io_err(&path(&name))(e)),
            }
            let dir = dirfd::openat2(
                dest,
                &name,
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW,
                libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS,
            );
            return match dir {
                Ok(dir) => Ok(Self { dest, name, dir }),
                Err(e) => {
                    let _ = dirfd::unlink_at(dest, &c, libc::AT_REMOVEDIR);
                    Err(io_err(&path(&name))(e))
                }
            };
        }
    }

    /// Open a staging file (never through a symlink).
    fn open(&self, name: &CString, flags: i32) -> io::Result<File> {
        let name = name
            .to_str()
            .map_err(|_| io::Error::from(io::ErrorKind::InvalidInput))?;
        let fd = dirfd::openat2(
            &self.dir,
            name,
            flags | libc::O_NOFOLLOW,
            libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS,
        )?;
        Ok(File::from(fd))
    }

    /// Create a staging file that must not exist yet, with `mode`.
    fn create_file(&self, name: &CString, mode: libc::mode_t) -> io::Result<File> {
        // SAFETY: valid dirfd and C string; the descriptor is owned below.
        let fd = unsafe {
            libc::openat(
                self.dir.as_raw_fd(),
                name.as_ptr(),
                libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC,
                mode as libc::c_uint,
            )
        };
        if fd < 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: `fd` is a fresh descriptor nothing else owns.
        Ok(File::from(unsafe { OwnedFd::from_raw_fd(fd) }))
    }
}

impl Drop for Staging<'_> {
    fn drop(&mut self) {
        if let Err(e) = dirfd::remove_flat_dir(self.dest, &self.name) {
            if e.raw_os_error() != Some(libc::ENOENT) {
                warn!(staging = %self.name, error = %e, "could not remove the staging directory");
            }
        }
    }
}

/// Take `flock(LOCK_EX)` on `dest`. Returns whether it is held: a
/// filesystem without flock on directories (NFS) gets a warning, and
/// the fetch still cannot corrupt another one's files (staging is
/// private), but nothing stops two from racing to the final names.
fn lock(dest: &OwnedFd, dest_path: &Path) -> Result<bool, FetchError> {
    #[cfg(test)]
    if tests::NO_LOCK.with(|n| n.get()) {
        return Ok(false);
    }
    // SAFETY: valid descriptor; the lock lives as long as it does.
    if unsafe { libc::flock(dest.as_raw_fd(), libc::LOCK_EX | libc::LOCK_NB) } == 0 {
        return Ok(true);
    }
    let e = io::Error::last_os_error();
    match e.raw_os_error() {
        Some(libc::EWOULDBLOCK) => Err(FetchError::Locked {
            dest: dest_path.to_path_buf(),
        }),
        Some(libc::EBADF | libc::ENOLCK | libc::EOPNOTSUPP | libc::EINVAL) => {
            warn!(dest = %dest_path.display(), error = %e,
                  "cannot lock the destination; a concurrent fetch into it would not be detected");
            Ok(false)
        }
        _ => Err(io_err(dest_path)(e)),
    }
}

/// Remove staging directories left by fetches that died. Only called
/// with the lock held, so none of them belongs to a live fetch.
fn remove_stale_staging(dest: &OwnedFd, dest_path: &Path) -> Result<(), FetchError> {
    for name in dirfd::names(dest).map_err(io_err(dest_path))? {
        let Ok(n) = name.to_str() else { continue };
        if !n.starts_with(STAGING_PREFIX) {
            continue;
        }
        if let Ok(Some(st)) = dirfd::stat_at(dest, &name) {
            if dirfd::is_dir(&st) {
                match dirfd::remove_flat_dir(dest, n) {
                    Ok(()) => warn!(dest = %dest_path.display(), staging = n,
                                    "removed the staging directory of an interrupted fetch"),
                    Err(e) => warn!(dest = %dest_path.display(), staging = n, error = %e,
                                    "cannot remove the staging directory of an interrupted fetch"),
                }
            }
        }
    }
    Ok(())
}

/// Why a source could not be opened.
#[derive(Debug)]
pub enum OpenError {
    /// It answered BUSY: it is serving as many fetchers as it takes.
    /// Not a failure; it is tried again after a pause.
    Busy(String),
    /// It refused the connection in the bootstrap: bad credentials (or
    /// it could not set the connection up). Final for that source: not
    /// retried.
    Refused(String),
    /// Anything else (unreachable, broken).
    Failed(HydrateError),
}

impl std::fmt::Display for OpenError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            OpenError::Busy(e) => write!(f, "busy: {e}"),
            OpenError::Refused(e) => write!(f, "refused: {e}"),
            OpenError::Failed(e) => write!(f, "{e}"),
        }
    }
}

/// Interchangeable nodes serving the same tree.
pub trait SourceSet: Sync {
    type Source: RangeSource;
    /// Every source to consider now, by name. Asked again on every
    /// selection round, so a list that grows (DNS of replicas that became
    /// ready) is picked up.
    fn candidates(&self) -> Vec<String>;
    /// Open one connection (one worker's source) to `source`.
    fn open(&self, source: &str) -> Result<Self::Source, OpenError>;
    /// The fetch stopped using `source` (it failed, or was found to serve
    /// other content): drop anything still held for it.
    fn release(&self, _source: &str) {}
    /// The identity of the server `source` reached when it was last
    /// opened, if known: names with one identity are one server (e.g. its
    /// IPv4 and IPv6 addresses); another identity at a name is another
    /// server (e.g. a reused pod address).
    fn identity(&self, _source: &str) -> Option<String> {
        None
    }
    /// Whether `source` answers at all within `timeout` (a cheap check
    /// before reopening a dead source, so a black-holed address costs
    /// `timeout`, not a full dial).
    fn reachable(&self, _source: &str, _timeout: Duration) -> bool {
        true
    }
    /// The generation of what the set holds for `source` now (0: nothing,
    /// or not tracked); a new open makes a new generation.
    fn anchor_generation(&self, _source: &str) -> u64 {
        0
    }
    /// Release `source` only if what is held for it is still `generation`:
    /// a newer one (the name was revived and opened again since it was
    /// given up) is kept.
    fn release_generation(&self, source: &str, generation: u64) {
        if self.anchor_generation(source) == generation {
            self.release(source);
        }
    }
}

/// A single source made by a function; what [`fetch_dir`] fetches from.
struct One<F>(F);

impl<S, F> SourceSet for One<F>
where
    S: RangeSource,
    F: Fn() -> Result<S, HydrateError> + Sync,
{
    type Source = S;
    fn candidates(&self) -> Vec<String> {
        vec!["source".into()]
    }
    fn open(&self, _: &str) -> Result<S, OpenError> {
        (self.0)().map_err(OpenError::Failed)
    }
}

/// A source as one worker sees it: its name and, per target, the
/// generation the target's ranges are pinned to there.
struct View {
    /// Order in which views were made: a file refetched because of a bad
    /// source refuses writes from views older than its bar.
    epoch: u64,
    /// The server behind the view: one per server for the fetch, shared
    /// by every view of it under any of its names (a revived name, being
    /// another server, gets another). Bad data bans the lineage, and the
    /// files any of its views wrote are refetched.
    lineage: u64,
    name: String,
    generations: Vec<u128>,
}

/// One worker's connection.
struct Conn<S> {
    src: S,
    view: Arc<View>,
}

/// Which listing to ask a source for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Want {
    /// Version 3 (attributes); a source without one is refused.
    V3,
    /// Version 3, or version 2 from an older source.
    V3OrV2,
    /// Version 2, which every source serves: for a failover, which needs
    /// sizes and identities only (attributes come from the first
    /// source's listing).
    V2,
}

/// Why a source is out of the running for the rest of the fetch.
#[derive(Clone)]
enum Dead {
    /// It failed (mid-copy, or every open did).
    Failed(String),
    /// Its manifest could not be had or was refused.
    Manifest(String),
    /// It serves other content than the copy started from.
    Mismatch(String),
    /// It refused this fetch's credentials.
    Refused(String),
    /// Its data failed the CRC check after every retry, or a file ended
    /// before its own manifest said: never tried again in this fetch,
    /// whatever server its address reaches later; the bytes it wrote are
    /// fetched again elsewhere.
    Corrupt(String),
}

#[derive(Default)]
struct SelState {
    /// The source workers connect to by default; `None` while one must
    /// be chosen (after it failed).
    current: Option<Arc<View>>,
    next_epoch: u64,
    dead: HashMap<String, Dead>,
    /// Consecutive failed opens per source (BUSY not counted).
    failures: HashMap<String, u32>,
    /// When each dead source died and the server identity it had then
    /// (`SourceSet::identity`): a name whose address now reaches another
    /// server is tried again (`reprobe`).
    dead_at: HashMap<String, (Option<String>, Instant)>,
    /// Sources taken out because a file changed there mid-copy, with the
    /// file: if no other source can finish the copy, it fails as Changed.
    changed: Vec<(String, String)>,
    /// Times each dead name was revived (`reprobe`): at most
    /// `MAX_REVIVALS` per fetch.
    revivals: HashMap<String, u32>,
    /// The lineage of each server (`View::lineage`), by
    /// `Sources::server_key`: names of one server (its IPv4 and IPv6
    /// addresses) share it, and a name revived on another server gets
    /// that server's.
    lineages: HashMap<String, u64>,
    next_lineage: u64,
    /// For each lineage retired for bad data: the first epoch a view made
    /// after it was retired gets (the bar for files refetched because of
    /// it). Recorded once, at the first retirement, so a later report of
    /// the same source (another worker still connected to one of its
    /// views) does not raise the bar above views already made since.
    bad_floor: HashMap<u64, u64>,
    /// Every report of bad data ("name: reason"), kept apart from `dead`:
    /// one seen through a stale view (the server that used to answer at
    /// a revived name) marks no name dead, and the fetch must still end
    /// as an integrity failure, naming it, if it cannot finish.
    bad_data: Vec<String>,
    /// Names given up on whose anchors are still to be released, outside
    /// the lock (`Sources::unlock`), with the anchor generation given up.
    to_release: Vec<(String, u64)>,
    /// Sources whose manifest matched in this fetch, with their
    /// generations: never read again. A file that changes there since
    /// fails its range with a generation change, which takes the source
    /// out of the running (`Sources::changed`).
    validated: HashMap<String, Arc<View>>,
    /// Busy sources: not chosen for moved work before `until`; never dead
    /// and never released for being busy.
    cooling: HashMap<String, Cool>,
    /// Sources whose manifest a worker is reading and checking now (outside
    /// the lock); nobody else validates or selects them meanwhile.
    validating: std::collections::HashSet<String>,
    /// A worker is selecting a new current source (outside the lock);
    /// others wait for it rather than select too.
    selecting: bool,
    /// Set once selection has failed for good: later callers fail fast,
    /// with the same error, so the fetch ends with one kind of error
    /// whichever worker reports first.
    gave_up: Option<GaveUp>,
}

impl SelState {
    /// Some source served bad data in this fetch.
    fn had_bad_data(&self) -> bool {
        !self.bad_data.is_empty() || self.dead.values().any(|d| matches!(d, Dead::Corrupt(_)))
    }

    /// The lineage of server `key` (`Sources::server_key`), given now if
    /// it has none.
    fn lineage(&mut self, key: String) -> u64 {
        let next = self.next_lineage;
        let l = *self.lineages.entry(key).or_insert(next);
        if l == next {
            self.next_lineage += 1;
        }
        l
    }

    /// A new view of `name`, whose server is `key` (the next epoch, the
    /// server's lineage).
    fn new_view(&mut self, key: String, name: String, generations: Vec<u128>) -> Arc<View> {
        let lineage = self.lineage(key);
        let view = Arc::new(View {
            epoch: self.next_epoch,
            lineage,
            name,
            generations,
        });
        self.next_epoch += 1;
        view
    }

    /// A newer view of the same server as `v`: same lineage, next epoch.
    fn renew(&mut self, v: &View) -> Arc<View> {
        let view = Arc::new(View {
            epoch: self.next_epoch,
            lineage: v.lineage,
            name: v.name.clone(),
            generations: v.generations.clone(),
        });
        self.next_epoch += 1;
        view
    }
}

/// How to rebuild the error selection ended with: its variant and text.
type GaveUp = (fn(String) -> FetchError, String);

fn gave_up(e: &FetchError) -> GaveUp {
    match e {
        FetchError::Connect(m) => (FetchError::Connect, m.clone()),
        FetchError::Manifest(m) => (FetchError::Manifest, m.clone()),
        FetchError::Unavailable(m) => (FetchError::Unavailable, m.clone()),
        FetchError::Mismatch(m) => (FetchError::Mismatch, m.clone()),
        FetchError::Refused(m) => (FetchError::Refused, m.clone()),
        FetchError::Corrupt(m) => (FetchError::Corrupt, m.clone()),
        FetchError::Invalid(m) => (FetchError::Invalid, m.clone()),
        FetchError::Device(m) => (FetchError::Device, m.clone()),
        FetchError::NotAll(m) => (FetchError::NotAll, m.clone()),
        // Selection ends with none of these; keep the text.
        FetchError::Get { .. }
        | FetchError::Changed { .. }
        | FetchError::Io { .. }
        | FetchError::Locked { .. }
        | FetchError::Incomplete { .. }
        | FetchError::Truncated { .. } => (FetchError::Connect, e.to_string()),
    }
}

/// A busy source's cooling pause.
struct Cool {
    until: Instant,
    /// The last pause, for the next one (decorrelated jitter); cleared by
    /// progress from that source.
    last: Duration,
}

/// Why [`Sources::open_retrying`] gave up.
enum OpenFail {
    /// Busy (at its fetcher cap).
    Busy,
    Failed(String),
}

/// A claim in the shared state (a worker selecting, or checking one
/// source), cleared under the lock when dropped: on every return and on
/// unwind, so a panic cannot leave it set and stall the other workers.
struct Claim<'s> {
    state: &'s Mutex<SelState>,
    of: ClaimOf,
}

enum ClaimOf {
    Selecting,
    Validating(String),
}

impl Drop for Claim<'_> {
    fn drop(&mut self) {
        let mut st = self.state.lock();
        match &self.of {
            ClaimOf::Selecting => st.selecting = false,
            ClaimOf::Validating(n) => {
                st.validating.remove(n);
            }
        }
    }
}

/// What [`Sources::select`] found.
enum Selected<S, T> {
    /// A newly checked source: its connection, name, and what `accept`
    /// made of its manifest.
    New(S, String, T),
    /// Another worker validated a source meanwhile: use that one.
    Validated,
}

/// What [`Sources::attach`] got a worker.
enum Attached<S> {
    Conn(Conn<S>),
    /// The source to use answered BUSY (fetcher cap): back off, keep
    /// everything.
    Busy(String),
}

/// Most times a dead name is revived (its address reached another
/// server) in one fetch.
const MAX_REVIVALS: u32 = 3;
/// Most dead names reprobed per selection round.
const REPROBES_PER_ROUND: usize = 2;
/// How long a reprobe waits for a dead name's address to answer.
const REPROBE_DIAL: Duration = Duration::from_secs(1);

/// Where [`Sources::alternate`] sends a worker off a busy source.
enum Alt<S> {
    /// A source it already has a connection to.
    Held(String),
    /// A new connection.
    New(Conn<S>),
}

/// Pauses after busy answers, with decorrelated jitter: each pause is
/// uniform between `base` and three times the last one, capped. Threads
/// draw from their own random stream, so workers refused together come
/// back at different times; pauses grow toward the cap while the busy
/// answers continue and start over after progress.
struct Backoff {
    base: Duration,
    cap: Duration,
    last: Duration,
}

impl Backoff {
    fn new(opts: &FetchOptions) -> Self {
        let base = opts.busy_backoff.max(Duration::from_micros(100));
        Self {
            base,
            cap: opts.max_busy_backoff.max(base),
            last: base,
        }
    }

    fn next(&mut self) -> Duration {
        self.last = decorrelated(self.base, self.cap, self.last);
        self.last
    }

    fn reset(&mut self) {
        self.last = self.base;
    }
}

/// Uniform in [base, min(cap, 3 * last)].
fn decorrelated(base: Duration, cap: Duration, last: Duration) -> Duration {
    let hi = last.saturating_mul(3).min(cap).max(base);
    base + (hi - base).mul_f64(nvstor_rdma::object::unit_random())
}

/// Chooses sources for a fetch and moves work between them.
struct Sources<'a, C: SourceSet> {
    set: &'a C,
    prefix: &'a str,
    opts: &'a FetchOptions,
    retries: &'a AtomicU64,
    reconnects: &'a AtomicU64,
    state: Mutex<SelState>,
    rng: Mutex<u64>,
    failovers: AtomicU64,
    /// Lineages retired for bad data: no write served by any of their
    /// views lands (see `Shared::write`). Inserted under the state lock,
    /// read by writers.
    banned: RwLock<HashSet<u64>>,
    /// When the fetch started.
    started: Instant,
    /// Nanoseconds after `started` of the last completed range (any
    /// worker, any source): busy sources are waited for at most
    /// `opts.wait` past it.
    progress: AtomicU64,
    /// Entries in `SelState::cooling`, so the success path only takes the
    /// lock when some source is cooling.
    cooling: AtomicUsize,
    /// Sources marked dead, revived, or given fresh views so far: a worker
    /// compares it at each range boundary and, when it moved, drops its
    /// connections to views no longer validated.
    dead_count: AtomicU64,
}

/// splitmix64: a small, well-mixed generator for shuffling and jitter.
fn mix(mut x: u64) -> u64 {
    x = x.wrapping_add(0x9E37_79B9_7F4A_7C15);
    x = (x ^ (x >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    x = (x ^ (x >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    x ^ (x >> 31)
}

/// FNV-1a of `s`.
fn hash_str(s: &str) -> u64 {
    s.bytes().fold(0xcbf2_9ce4_8422_2325u64, |h, b| {
        (h ^ u64::from(b)).wrapping_mul(0x0100_0000_01b3)
    })
}

/// `names` in this fetcher's order: by a hash of the seed and the name,
/// so each fetcher has its own order, the order of a name does not
/// depend on what else is in the list, and duplicates collapse.
pub fn source_order(seed: u64, mut names: Vec<String>) -> Vec<String> {
    names.sort_by_key(|n| (mix(seed ^ hash_str(n)), n.clone()));
    names.dedup();
    names
}

impl<'a, C: SourceSet> Sources<'a, C> {
    fn new(
        set: &'a C,
        prefix: &'a str,
        opts: &'a FetchOptions,
        retries: &'a AtomicU64,
        reconnects: &'a AtomicU64,
    ) -> Self {
        Self {
            set,
            prefix,
            opts,
            retries,
            reconnects,
            state: Mutex::new(SelState::default()),
            rng: Mutex::new(mix(opts.seed ^ 0x5eed)),
            failovers: AtomicU64::new(0),
            banned: RwLock::new(HashSet::new()),
            started: Instant::now(),
            progress: AtomicU64::new(0),
            cooling: AtomicUsize::new(0),
            dead_count: AtomicU64::new(0),
        }
    }

    /// A range was completed (or the first source was chosen): the busy
    /// bound starts again.
    fn progress(&self) {
        let now = u64::try_from(self.started.elapsed().as_nanos()).unwrap_or(u64::MAX);
        self.progress.fetch_max(now, Ordering::Relaxed);
    }

    /// How long busy sources are still waited for: until `opts.wait` after
    /// the last completed range. `None`: no bound (a `wait` past what an
    /// `Instant` can hold).
    fn stall_left(&self) -> Option<Duration> {
        let deadline = self
            .started
            .checked_add(Duration::from_nanos(self.progress.load(Ordering::Relaxed)))?
            .checked_add(self.opts.wait)?;
        Some(deadline.saturating_duration_since(Instant::now()))
    }

    /// No range has completed for `opts.wait`.
    fn stalled(&self) -> bool {
        self.stall_left().is_some_and(|d| d.is_zero())
    }

    /// The error once no range has completed for `opts.wait`. A source
    /// that served bad data is why the copy cannot finish intact, whatever
    /// the others are doing now (as in `select`).
    fn stall_error(&self) -> FetchError {
        let st = self.state.lock();
        if st.had_bad_data() {
            return Self::exhausted(&st);
        }
        drop(st);
        FetchError::Unavailable(format!(
            "no range completed for {:.1} s: every eligible source answered busy (no free slot or worker) \
             or none could be reached", self.opts.wait.as_secs_f64()))
    }

    /// Which server `name` reaches, for lineages: its identity when the
    /// set knows it (names of one server share it), else the name.
    fn server_key(&self, name: &str) -> String {
        self.set
            .identity(name)
            .map_or_else(|| format!("name:{name}"), |id| format!("id:{id}"))
    }

    /// `view` is of a server that no longer answers at its name: the name
    /// was revived on another server since. Only revival makes views
    /// stale; a live name's failures always count.
    fn is_stale(&self, st: &SelState, view: &View) -> bool {
        st.revivals.contains_key(&view.name)
            && self
                .lineage_now(st, &view.name)
                .is_some_and(|l| l != view.lineage)
    }

    /// The lineage of the server `name` reaches now, if it has one.
    fn lineage_now(&self, st: &SelState, name: &str) -> Option<u64> {
        st.lineages.get(&self.server_key(name)).copied()
    }

    /// `name` reaches a server that served bad data in this fetch (under
    /// this or another of its names): never a source again.
    fn banned_server(&self, st: &SelState, name: &str) -> bool {
        self.lineage_now(st, name)
            .is_some_and(|l| st.bad_floor.contains_key(&l))
    }

    /// A write served by `view` was refused. If its server was retired
    /// for bad data (that retirement finished: `bad_floor` is recorded in
    /// the same critical section as the deaths, with the real reason),
    /// `view`'s name goes too, so a view of a banned server is never
    /// handed out again. A ban still being recorded is left to the
    /// retirement recording it.
    ///
    /// Returns false while that ban is still being recorded: the caller
    /// pauses, rather than reconnect to the same view at once.
    fn retire_banned(&self, view: &View) -> bool {
        if !self.banned.read().contains(&view.lineage) {
            return true;
        }
        if self.state.lock().bad_floor.contains_key(&view.lineage) {
            self.retire_as(
                view,
                Dead::Corrupt("a server that served bad data under another name".into()),
            );
            return true;
        }
        false
    }

    /// A newly checked `name` is not usable after all: it died meanwhile
    /// (a worker retired it), or it turned out to be another name of a
    /// server that served bad data (its identity is learned when it is
    /// opened), which takes it out as Corrupt.
    fn unusable_after_check(&self, st: &mut SelState, name: &str) -> bool {
        if self.banned_server(st, name) && !st.dead.contains_key(name) {
            warn!(
                source = name,
                "another name of a server that served bad data; not used"
            );
            self.mark_dead(
                st,
                name,
                Dead::Corrupt("another name of a server that served bad data".into()),
            );
        }
        st.dead.contains_key(name)
    }

    /// Bytes came from `name`: its busy pauses start over. Cooling entries
    /// that ended more than `max_busy_backoff` ago are dropped too, so
    /// the success path stops taking the lock once no source has been
    /// busy for a while.
    fn served(&self, name: &str) {
        if self.cooling.load(Ordering::Relaxed) == 0 {
            return;
        }
        let mut st = self.state.lock();
        let stale = Instant::now().checked_sub(self.opts.max_busy_backoff);
        let before = st.cooling.len();
        st.cooling
            .retain(|n, c| n != name && stale.is_none_or(|t| c.until > t));
        self.cooling
            .fetch_sub(before - st.cooling.len(), Ordering::Relaxed);
    }

    /// `name` answered busy: its next cooling pause, longer the more busy
    /// answers in a row (decorrelated jitter, capped). Nothing is
    /// released: a busy source keeps its anchor and its connections.
    fn cool(&self, name: &str) {
        let mut st = self.state.lock();
        self.cool_locked(&mut st, name);
    }

    fn cool_locked(&self, st: &mut SelState, name: &str) {
        if st.dead.contains_key(name) {
            return;
        }
        let base = self.opts.busy_backoff.max(Duration::from_micros(100));
        let cap = self.opts.max_busy_backoff.max(base);
        let last = st.cooling.get(name).map_or(base, |c| c.last);
        let pause = decorrelated(base, cap, last);
        if st
            .cooling
            .insert(
                name.to_string(),
                Cool {
                    until: Instant::now() + pause,
                    last: pause,
                },
            )
            .is_none()
        {
            self.cooling.fetch_add(1, Ordering::Relaxed);
        }
        debug!(source = name, ?pause, "source busy; cooling");
    }

    /// `name` is out of the running for the rest of the fetch: it leaves
    /// `validated`, `cooling` and `current`, and is released.
    fn mark_dead(&self, st: &mut SelState, name: &str, why: Dead) {
        if st.dead.contains_key(name) {
            return;
        }
        st.validated.remove(name);
        if st.cooling.remove(name).is_some() {
            self.cooling.fetch_sub(1, Ordering::Relaxed);
        }
        if st.current.as_ref().is_some_and(|c| c.name == name) {
            st.current = None;
        }
        st.dead.insert(name.to_string(), why);
        st.dead_at
            .insert(name.to_string(), (self.set.identity(name), Instant::now()));
        self.dead_count.fetch_add(1, Ordering::Relaxed);
        // Released by `unlock`, outside the lock: dropping an anchor tears
        // down a connection.
        st.to_release
            .push((name.to_string(), self.set.anchor_generation(name)));
    }

    /// Drop the state lock, then release the anchors of the sources given
    /// up on meanwhile.
    fn unlock(&self, mut st: parking_lot::MutexGuard<'_, SelState>) {
        let names = std::mem::take(&mut st.to_release);
        drop(st);
        for (n, generation) in names {
            self.set.release_generation(&n, generation);
        }
    }

    /// Drop the connections in `conns` to sources the fetch no longer uses
    /// as they were validated: dead ones, and older views of a revived
    /// name (another server at the same address). Only when a source died
    /// or was revived since the worker last looked (`seen`): one relaxed
    /// load otherwise.
    fn prune(&self, seen: &mut u64, conns: &mut HashMap<String, Conn<C::Source>>) {
        let now = self.dead_count.load(Ordering::Relaxed);
        if now == *seen {
            return;
        }
        *seen = now;
        let st = self.state.lock();
        conns.retain(|n, c| st.validated.get(n).is_some_and(|v| v.epoch == c.view.epoch));
    }

    /// Whether `name` is out of the running.
    fn is_dead(&self, name: &str) -> bool {
        self.state.lock().dead.contains_key(name)
    }

    /// How long a dead source with a known server identity stays untried
    /// before its name is opened again to see whether another server took
    /// its address.
    fn reprobe_after(&self) -> Duration {
        self.opts
            .max_busy_backoff
            .saturating_mul(2)
            .max(Duration::from_millis(1))
    }

    /// The busy budget of one manifest check: a few busy rounds, at most
    /// `max_busy_backoff` (and the stall bound); past it the source cools
    /// and selection moves on to the next candidate.
    fn check_budget(&self) -> Duration {
        self.opts
            .max_busy_backoff
            .max(self.opts.busy_backoff)
            .min(self.stall_left().unwrap_or(Duration::MAX))
    }

    /// Pause after round `round` in which no source took the fetch:
    /// exponential, capped, with jitter in [half, full].
    fn pause(&self, round: u32) -> Duration {
        let full = self
            .opts
            .busy_backoff
            .saturating_mul(1u32 << round.min(16))
            .min(self.opts.max_busy_backoff);
        let r = {
            let mut s = self.rng.lock();
            *s = mix(*s);
            *s
        };
        let half = full / 2;
        half + Duration::from_nanos(r % (full.saturating_sub(half).as_nanos() as u64).max(1))
    }

    /// The error once no live source is left.
    fn exhausted(st: &SelState) -> FetchError {
        let list = |pick: fn(&Dead) -> Option<&String>| -> Vec<String> {
            let mut v: Vec<String> = st
                .dead
                .iter()
                .filter_map(|(n, d)| pick(d).map(|w| format!("{n}: {w}")))
                .collect();
            v.sort();
            v
        };
        let mismatched = list(|d| {
            if let Dead::Mismatch(w) = d {
                Some(w)
            } else {
                None
            }
        });
        let manifest = list(|d| {
            if let Dead::Manifest(w) = d {
                Some(w)
            } else {
                None
            }
        });
        let failed = list(|d| {
            if let Dead::Failed(w) = d {
                Some(w)
            } else {
                None
            }
        });
        let refused = list(|d| {
            if let Dead::Refused(w) = d {
                Some(w)
            } else {
                None
            }
        });
        let mut corrupt = list(|d| {
            if let Dead::Corrupt(w) = d {
                Some(w)
            } else {
                None
            }
        });
        if corrupt.is_empty() {
            corrupt = st.bad_data.clone();
        }
        if !mismatched.is_empty() {
            let mut all = mismatched;
            all.extend(corrupt);
            all.extend(failed);
            all.extend(refused);
            all.extend(manifest);
            FetchError::Mismatch(all.join("; "))
        } else if !corrupt.is_empty() {
            // Bad data took a source out: that, not the sources lost
            // otherwise, is why the copy cannot finish intact.
            let mut all = corrupt;
            all.extend(failed);
            all.extend(refused);
            all.extend(manifest);
            FetchError::Corrupt(all.join("; "))
        } else if failed.is_empty() && manifest.is_empty() {
            FetchError::Refused(refused.join("; "))
        } else if failed.is_empty() && refused.is_empty() {
            FetchError::Manifest(manifest.join("; "))
        } else {
            let mut all = failed;
            all.extend(refused);
            all.extend(manifest);
            FetchError::Connect(all.join("; "))
        }
    }

    /// A failed open of `name`: out of the running after
    /// `max_retries + 1` in a row.
    /// Returns whether it is now out (its release is then queued).
    fn open_failed(&self, st: &mut SelState, name: &str, why: String) -> bool {
        let n = st.failures.entry(name.to_string()).or_insert(0);
        *n += 1;
        warn!(source = name, error = %why, attempt = *n, "cannot open source");
        if *n > self.opts.max_retries {
            self.mark_dead(st, name, Dead::Failed(why));
            return true;
        }
        false
    }

    /// A failed open or check of `name`: counted, and whatever the set
    /// holds for it (a half-made anchor) released once: by the queue if
    /// it is now out, directly otherwise, so it holds no place on that
    /// server while the fetch is elsewhere.
    fn open_failed_release(&self, name: &str, why: String) {
        let mut st = self.state.lock();
        let dead = self.open_failed(&mut st, name, why);
        self.unlock(st);
        if !dead {
            self.set.release(name);
        }
    }

    /// Open `name` and check its listing (the one `want` asks for): its
    /// connection and what `accept` made of the listing. `Err(None)`:
    /// busy or briefly unreachable (cooled or counted); `Err(Some(dead))`:
    /// out. Runs outside the state lock; the caller has claimed `name` in
    /// `validating`.
    fn check<T>(
        &self,
        name: &str,
        opened: Option<C::Source>,
        want: Want,
        accept: &mut impl FnMut(Manifest) -> Result<T, Dead>,
    ) -> Result<(C::Source, T), Option<Dead>> {
        let opened = match opened {
            Some(s) => Ok(s),
            None => self.set.open(name),
        };
        let mut src = match opened {
            Ok(s) => s,
            Err(OpenError::Busy(_)) => {
                self.cool(name);
                return Err(None);
            }
            Err(OpenError::Refused(why)) => {
                // Retrying cannot fix credentials: dead for this fetch.
                warn!(source = %name, error = %why, "source refused this fetch's credentials");
                return Err(Some(Dead::Refused(why)));
            }
            Err(OpenError::Failed(e)) => {
                self.open_failed_release(name, e.to_string());
                return Err(None);
            }
        };
        // A short busy budget: a busy source must not hold the check (and
        // with it failover to an idle one) for the whole stall bound.
        let bound = self.check_budget();
        match self.listing(&mut src, want, bound) {
            Ok(listing) => match accept(listing) {
                Ok(t) => Ok((src, t)),
                Err(dead) => {
                    match &dead {
                        Dead::Mismatch(why) => warn!(source = %name, error = %why,
                                                     "source serves other content; not using it"),
                        Dead::Manifest(why) | Dead::Failed(why) => {
                            warn!(source = %name, error = %why,
                                                                         "source's manifest refused")
                        }
                        Dead::Refused(why) | Dead::Corrupt(why) => {
                            warn!(source = %name, error = %why,
                                                                         "source not used")
                        }
                    }
                    Err(Some(dead))
                }
            },
            Err(FetchError::Unavailable(_)) => {
                self.cool(name);
                Err(None)
            }
            Err(FetchError::Manifest(why)) => {
                warn!(source = %name, error = %why, "source's manifest refused");
                Err(Some(Dead::Manifest(why)))
            }
            Err(e) => {
                drop(src);
                self.open_failed_release(name, e.to_string());
                Err(None)
            }
        }
    }

    /// Find a source that takes the fetch and whose manifest `accept`
    /// approves (`Err` says why it is out of the running). Tries every
    /// live, uncooled source not validated or being validated, in this
    /// fetcher's order per round, pausing between rounds, until
    /// `opts.wait` after the last completed range. A source whose
    /// listing stays busy past a short budget cools and the next one is
    /// tried. A dead source with a known identity is opened again after a
    /// while: if its address now reaches another server, it is a new
    /// candidate. With `yield_to_validated`, returns
    /// [`Selected::Validated`] as soon as another worker has validated a
    /// live source. The state lock is held only to choose and to record
    /// outcomes; opens, listing reads and pauses run outside it.
    fn select<T>(
        &self,
        yield_to_validated: bool,
        want: Want,
        mut accept: impl FnMut(Manifest) -> Result<T, Dead>,
    ) -> Result<Selected<C::Source, T>, FetchError> {
        let mut round = 0u32;
        loop {
            let all = source_order(self.opts.seed, self.set.candidates());
            let (eligible, reprobe, mut busy) = {
                let st = self.state.lock();
                if yield_to_validated && st.validated.keys().any(|n| !st.dead.contains_key(n)) {
                    return Ok(Selected::Validated);
                }
                let now = Instant::now();
                let reprobe: Vec<String> = all
                    .iter()
                    .filter(|n| {
                        st.dead
                            .get(*n)
                            .is_some_and(|d| !matches!(d, Dead::Corrupt(_)))
                            && !st.validating.contains(*n)
                            && st.revivals.get(*n).copied().unwrap_or(0) < MAX_REVIVALS
                    })
                    .filter(|n| {
                        st.dead_at.get(*n).is_some_and(|(id, at)| {
                            id.is_some()
                                && now.saturating_duration_since(*at) >= self.reprobe_after()
                        })
                    })
                    .take(REPROBES_PER_ROUND)
                    .cloned()
                    .collect();
                let live: Vec<&String> = all.iter().filter(|n| !st.dead.contains_key(*n)).collect();
                if !all.is_empty() && live.is_empty() && reprobe.is_empty() {
                    return Err(Self::exhausted(&st));
                }
                let busy: Vec<String> = live
                    .iter()
                    .filter(|n| st.cooling.get(**n).is_some_and(|c| c.until > now))
                    .map(|n| n.to_string())
                    .collect();
                let eligible: Vec<String> = live
                    .into_iter()
                    .filter(|n| st.cooling.get(*n).is_none_or(|c| c.until <= now))
                    .filter(|n| !st.validating.contains(*n) && !st.validated.contains_key(*n))
                    .cloned()
                    .collect();
                (eligible, reprobe, busy)
            };
            for (name, again) in eligible
                .into_iter()
                .map(|n| (n, false))
                .chain(reprobe.into_iter().map(|n| (n, true)))
            {
                let claim = {
                    let mut st = self.state.lock();
                    let gone = if again {
                        !st.dead.contains_key(&name)
                    } else {
                        st.dead.contains_key(&name)
                    };
                    if gone
                        || st.validated.contains_key(&name)
                        || !st.validating.insert(name.clone())
                    {
                        continue;
                    }
                    Claim {
                        state: &self.state,
                        of: ClaimOf::Validating(name.clone()),
                    }
                };
                let opened = if again {
                    // Reprobes are a side job: never past the busy bound.
                    if self.stalled() {
                        continue;
                    }
                    match self.reprobe(&name) {
                        Some(src) => Some(src),
                        None => continue,
                    }
                } else {
                    None
                };
                let r = self.check(&name, opened, want, &mut accept);
                drop(claim);
                let mut st = self.state.lock();
                match r {
                    Ok((src, t)) => {
                        if !self.unusable_after_check(&mut st, &name) {
                            st.failures.remove(&name);
                            return Ok(Selected::New(src, name, t));
                        }
                        // Torn down outside the lock.
                        self.unlock(st);
                        drop(src);
                        continue;
                    }
                    Err(None) => {
                        if st.cooling.contains_key(&name) {
                            busy.push(name);
                        }
                    }
                    Err(Some(dead)) => self.mark_dead(&mut st, &name, dead),
                }
                self.unlock(st);
            }
            let left = self.stall_left();
            if left.is_some_and(|d| d.is_zero()) {
                // A source that served bad data (a CRC failure, or files
                // shorter than its manifest) is why the copy cannot finish
                // intact, whatever the others are doing now. A source
                // that serves other content served nothing bad: that
                // alone stays "no usable source".
                {
                    let st = self.state.lock();
                    if st.had_bad_data() {
                        return Err(Self::exhausted(&st));
                    }
                }
                let what = if all.is_empty() {
                    "no source name resolves".to_string()
                } else if busy.is_empty() {
                    "every source failed to open or is being checked".to_string()
                } else {
                    format!("every source answered busy: {}", busy.join(", "))
                };
                return Err(FetchError::Unavailable(format!(
                    "no source took the fetch within {:.1} s; {what}",
                    self.opts.wait.as_secs_f64()
                )));
            }
            if !busy.is_empty() {
                tracing::info!(
                    busy = busy.len(),
                    round,
                    "every source that answered is busy; backing off"
                );
            }
            // Until the round pause, or the first busy source's cooling
            // ends if that is sooner. Outside the lock.
            let mut pause = self.pause(round).min(left.unwrap_or(Duration::MAX));
            let now = Instant::now();
            if let Some(next) = self
                .state
                .lock()
                .cooling
                .values()
                .map(|c| c.until)
                .filter(|t| *t > now)
                .min()
            {
                pause = pause.min(next - now).max(Duration::from_millis(1));
            }
            std::thread::sleep(pause);
            round += 1;
        }
    }

    /// The listing of the prefix `want` asks `src` for (see [`Want`]),
    /// waiting out busy answers for at most `busy_wait`.
    fn listing(
        &self,
        src: &mut C::Source,
        want: Want,
        busy_wait: Duration,
    ) -> Result<Manifest, FetchError> {
        // One budget for both reads: a check waits out busy answers for
        // at most `busy_wait` in all.
        let mut budget = BusyBudget::new(self.opts, busy_wait);
        let get = |src: &mut C::Source, key: &str, budget: &mut BusyBudget| {
            fetch_listing_within(src, key, self.opts, self.retries, self.reconnects, budget)
        };
        let v2 = |src: &mut C::Source, budget: &mut BusyBudget| {
            get(src, &manifest::key(self.prefix), budget)?.ok_or_else(|| {
                FetchError::Manifest(format!(
                    "{:?}: no such directory on the source",
                    self.prefix
                ))
            })
        };
        if want == Want::V2 {
            return v2(src, &mut budget);
        }
        if let Some(m) = get(src, &manifest::key_v3(self.prefix), &mut budget)? {
            return Ok(m);
        }
        // No version 3 listing: an older nvstor-peer, if it has a
        // version 2 one; otherwise the prefix does not exist. (A busy
        // version 3 read is Unavailable above, never taken for "none".)
        let old = v2(src, &mut budget)?;
        if want == Want::V3OrV2 {
            return Ok(old);
        }
        Err(FetchError::Manifest(
            "serves manifest version 2 only (an older nvstor-peer, without file modes, mtimes or owners): \
             upgrade the source first; --no-attrs would copy the bytes only, leaving files 0600 and \
             directories 0700".into()))
    }

    /// Open the dead source `name` again: if it now reaches a server with
    /// another identity than the one that died (a reused address), it is
    /// alive again (counted in `revivals`; workers' connections to the old
    /// server are dropped at their next range) and its connection is
    /// returned; otherwise it stays dead and is tried again after
    /// `reprobe_after`. An unreachable address is given up on after
    /// `REPROBE_DIAL`, not the full dial timeout. Teardown runs outside
    /// the lock.
    fn reprobe(&self, name: &str) -> Option<C::Source> {
        let old = self
            .state
            .lock()
            .dead_at
            .get(name)
            .and_then(|(id, _)| id.clone());
        // Never past the busy bound, even within one reprobe.
        let dial = REPROBE_DIAL.min(self.stall_left().unwrap_or(Duration::MAX));
        let src = if !dial.is_zero() && self.set.reachable(name, dial) {
            self.set.open(name).ok()
        } else {
            None
        };
        let now_id = src.as_ref().and_then(|_| self.set.identity(name));
        let mut st = self.state.lock();
        match (src, now_id) {
            (Some(src), Some(id)) if Some(&id) != old.as_ref() => {
                tracing::info!(
                    source = name,
                    "another server answers at a dead source's address; trying it"
                );
                st.dead.remove(name);
                st.dead_at.remove(name);
                st.failures.remove(name);
                *st.revivals.entry(name.to_string()).or_insert(0) += 1;
                // Another server, so another lineage (`server_key`), given
                // now: views of the old one are stale from here, also
                // while the new one is being checked.
                let key = self.server_key(name);
                st.lineage(key);
                drop(st);
                self.dead_count.fetch_add(1, Ordering::Relaxed);
                Some(src)
            }
            (src, _) => {
                if let Some(e) = st.dead_at.get_mut(name) {
                    e.1 = Instant::now();
                }
                drop(st);
                drop(src);
                self.set.release(name);
                None
            }
        }
    }

    /// The bar for files refetched because lineage `bad` served bad data:
    /// the first epoch made after it was retired (recorded then, see
    /// `SelState::bad_floor`), so every view made since passes and the
    /// views that existed then do not.
    fn bar_after(&self, bad: u64) -> u64 {
        let st = self.state.lock();
        st.bad_floor.get(&bad).copied().unwrap_or(st.next_epoch)
    }

    /// The first source: its connection, its name and its manifest. With
    /// `opts.attrs`, a source without a version 3 listing (an older
    /// nvstor-peer) is refused: the copy would silently lose every
    /// mode, mtime and owner. Without, a version 3 listing is still
    /// preferred (it lists empty directories).
    fn first(&self) -> Result<(C::Source, String, Manifest), FetchError> {
        let want = if self.opts.attrs {
            Want::V3
        } else {
            Want::V3OrV2
        };
        match self.select(false, want, Ok) {
            Ok(Selected::New(src, name, listing)) => Ok((src, name, listing)),
            Ok(Selected::Validated) => Err(FetchError::Invalid(
                "no source was validated before the first".into(),
            )),
            Err(e) => {
                self.state.lock().gave_up = Some(gave_up(&e));
                Err(e)
            }
        }
    }

    /// Make the source the copy starts from current for the workers. The
    /// busy bound starts here: time spent choosing it does not count.
    fn start(&self, name: String, generations: Vec<u128>) -> Arc<View> {
        let mut st = self.state.lock();
        let view = st.new_view(self.server_key(&name), name, generations);
        st.current = Some(Arc::clone(&view));
        st.validated.insert(view.name.clone(), Arc::clone(&view));
        drop(st);
        self.progress();
        view
    }

    /// Open `name` under the retry policy. BUSY (the source is at its
    /// fetcher cap) is not a failure and spends no attempt; it is
    /// returned at once ([`OpenFail::Busy`]).
    fn open_retrying(&self, name: &str) -> Result<C::Source, OpenFail> {
        let mut attempt = 0u32;
        loop {
            match self.set.open(name) {
                Ok(s) => return Ok(s),
                Err(OpenError::Busy(e)) => {
                    debug!(source = name, error = %e, "source busy");
                    return Err(OpenFail::Busy);
                }
                Err(OpenError::Refused(e)) => {
                    return Err(OpenFail::Failed(format!("refused: {e}")))
                }
                Err(OpenError::Failed(e)) if attempt < self.opts.max_retries => {
                    attempt += 1;
                    self.retries.fetch_add(1, Ordering::Relaxed);
                    warn!(source = name, error = %e, attempt, "connect failed; retrying");
                    std::thread::sleep(nvstor_rdma::object::backoff(
                        self.opts.retry_backoff,
                        attempt,
                    ));
                }
                Err(OpenError::Failed(e)) => return Err(OpenFail::Failed(e.to_string())),
            }
        }
    }

    /// A connection for a worker: to the current source; when there is
    /// none (it failed), to an already validated live source (its
    /// manifest is not read again) or else to a newly selected one whose
    /// manifest matches every target not yet complete (one worker selects
    /// at a time, outside the lock; the others wait for it). Every pass
    /// after the first checks the busy bound.
    fn attach(&self, targets: &[Target]) -> Result<Attached<C::Source>, FetchError> {
        let mut first_pass = true;
        loop {
            // A fetch already given up on ends with that error, whatever
            // this worker's own wait says.
            if let Some((make, why)) = &self.state.lock().gave_up {
                return Err(make(why.clone()));
            }
            if !first_pass && self.stalled() {
                return Err(self.stall_error());
            }
            first_pass = false;
            let mut st = self.state.lock();
            if let Some((make, why)) = &st.gave_up {
                return Err(make(why.clone()));
            }
            if st
                .current
                .as_ref()
                .is_some_and(|c| st.dead.contains_key(&c.name))
            {
                st.current = None;
            }
            if st.current.is_none() {
                let reuse = st
                    .validated
                    .values()
                    .filter(|v| {
                        !st.dead.contains_key(&v.name) && !st.bad_floor.contains_key(&v.lineage)
                    })
                    .min_by_key(|v| v.epoch)
                    .cloned();
                if let Some(v) = reuse {
                    // A fresh epoch for it: a file refetched after a bad
                    // source refuses writes from views older than the
                    // refetch (`Target::refetch_from`), and this view must
                    // not be one of them. Connections to its older view
                    // are dropped at the next range (`prune`).
                    let v = st.renew(&v);
                    st.validated.insert(v.name.clone(), Arc::clone(&v));
                    st.current = Some(Arc::clone(&v));
                    self.dead_count.fetch_add(1, Ordering::Relaxed);
                    self.failovers.fetch_add(1, Ordering::Relaxed);
                    tracing::info!(source = %v.name, "continuing the copy from another validated source");
                }
            }
            if let Some(view) = st.current.clone() {
                // Open outside the lock, so workers connect in parallel.
                drop(st);
                match self.open_retrying(&view.name) {
                    Ok(src) => return Ok(Attached::Conn(Conn { src, view })),
                    Err(OpenFail::Busy) => {
                        self.cool(&view.name);
                        return Ok(Attached::Busy(view.name.clone()));
                    }
                    Err(OpenFail::Failed(why)) => self.retire(&view, why),
                }
                continue;
            }
            if st.selecting {
                // Another worker is choosing the next source: wait for it
                // (outside the lock) and look again. A check of some other
                // source in progress does not hold this worker: selection
                // skips that source and tries the others.
                drop(st);
                std::thread::sleep(self.opts.busy_backoff.max(Duration::from_millis(1)));
                continue;
            }
            st.selecting = true;
            let claim = Claim {
                state: &self.state,
                of: ClaimOf::Selecting,
            };
            drop(st);
            let r = self.select(true, Want::V2, |m| {
                matching_generations(targets, &m.entries).map_err(Dead::Mismatch)
            });
            drop(claim);
            let mut st = self.state.lock();
            match r {
                Ok(Selected::Validated) => continue,
                Ok(Selected::New(src, name, generations)) => {
                    // It may have died since select let go of the lock.
                    if self.unusable_after_check(&mut st, &name) {
                        self.unlock(st);
                        drop(src);
                        continue;
                    }
                    let view = st.new_view(self.server_key(&name), name, generations);
                    st.current = Some(Arc::clone(&view));
                    st.validated.insert(view.name.clone(), Arc::clone(&view));
                    self.failovers.fetch_add(1, Ordering::Relaxed);
                    tracing::info!(source = %view.name, "continuing the copy from another source");
                    return Ok(Attached::Conn(Conn { src, view }));
                }
                Err(e) => {
                    st.gave_up = Some(gave_up(&e));
                    return Err(e);
                }
            }
        }
    }

    /// Somewhere other than `busy` for a worker whose source answered
    /// busy, without giving `busy` up: a validated, live, uncooled source
    /// the worker already has a connection to (`held`), or one it can
    /// open; failing that, one neither validated nor being validated,
    /// checked now outside the lock (its manifest must match every target
    /// not yet complete). `None`: nowhere else now.
    fn alternate(
        &self,
        targets: &[Target],
        busy: &str,
        held: &HashMap<String, Conn<C::Source>>,
    ) -> Option<Alt<C::Source>> {
        // Resolve names before taking the lock (it may be DNS).
        let candidates = source_order(self.opts.seed, self.set.candidates());
        let now = Instant::now();
        let mut st = self.state.lock();
        let usable = |st: &SelState, n: &str| {
            n != busy
                && !st.dead.contains_key(n)
                && st.cooling.get(n).is_none_or(|c| c.until <= now)
        };
        let mut validated: Vec<Arc<View>> = st
            .validated
            .values()
            .filter(|v| usable(&st, &v.name) && !st.bad_floor.contains_key(&v.lineage))
            .cloned()
            .collect();
        validated.sort_by_key(|v| v.epoch);
        if let Some(v) = validated
            .iter()
            .find(|v| held.get(&v.name).is_some_and(|c| c.view.epoch == v.epoch))
        {
            return Some(Alt::Held(v.name.clone()));
        }
        if let Some(v) = validated.into_iter().next() {
            drop(st);
            return match self.open_retrying(&v.name) {
                Ok(src) => Some(Alt::New(Conn { src, view: v })),
                Err(OpenFail::Busy) => {
                    self.cool(&v.name);
                    None
                }
                Err(OpenFail::Failed(why)) => {
                    self.retire(&v, why);
                    None
                }
            };
        }
        if st.selecting || !st.validating.is_empty() {
            return None;
        }
        let name = candidates
            .into_iter()
            .find(|n| usable(&st, n) && !st.validated.contains_key(n))?;
        st.validating.insert(name.clone());
        let claim = Claim {
            state: &self.state,
            of: ClaimOf::Validating(name.clone()),
        };
        drop(st);
        let r = self.check(&name, None, Want::V2, &mut |m: Manifest| {
            matching_generations(targets, &m.entries).map_err(Dead::Mismatch)
        });
        drop(claim);
        let mut st = self.state.lock();
        match r {
            // Validated or dropped by someone else meanwhile: not twice.
            Ok(_)
                if st.validated.contains_key(&name)
                    || self.unusable_after_check(&mut st, &name) =>
            {
                self.unlock(st);
                None
            }
            Ok((src, generations)) => {
                let view = st.new_view(self.server_key(&name), name, generations);
                st.validated.insert(view.name.clone(), Arc::clone(&view));
                st.failures.remove(&view.name);
                self.failovers.fetch_add(1, Ordering::Relaxed);
                tracing::info!(source = %view.name, busy, "moving work off a busy source");
                Some(Alt::New(Conn { src, view }))
            }
            Err(None) => None,
            Err(Some(dead)) => {
                self.mark_dead(&mut st, &name, dead);
                self.unlock(st);
                None
            }
        }
    }

    /// `view`'s source failed (retries spent): out of the running for the
    /// rest of the fetch (whichever view of it failed), released; if it
    /// was the current one, the next caller picks another.
    fn retire(&self, view: &View, why: String) {
        self.retire_as(view, Dead::Failed(why));
    }

    /// [`Self::retire`] for a given reason. A view of a name that was
    /// revived (another server now answers there) that is not that
    /// server's validated view is stale: its failure says nothing about
    /// the new server and is ignored.
    ///
    /// Bad data (`Dead::Corrupt`) is handled first and always: the view is
    /// banned before anyone scans for what it wrote (from here on no write
    /// it serves lands, `Shared::write`), and it is recorded for the source
    /// even when another worker retired it first for another reason, so
    /// the outcome does not depend on which worker got there first.
    fn retire_as(&self, view: &View, why: Dead) {
        let corrupt = matches!(why, Dead::Corrupt(_));
        if corrupt {
            self.banned.write().insert(view.lineage);
            std::sync::atomic::fence(Ordering::SeqCst);
        }
        // Resolved before the lock (it may be DNS): the server's other
        // names go with it on bad data.
        let names = if corrupt {
            self.set.candidates()
        } else {
            Vec::new()
        };
        let mut st = self.state.lock();
        if matches!(why, Dead::Corrupt(_)) && !st.bad_floor.contains_key(&view.lineage) {
            let floor = st.next_epoch;
            st.bad_floor.insert(view.lineage, floor);
            // Files refetched because of this view refuse writes from
            // views older than `floor`, whichever source they are of. Every
            // live validated view (the current one too, and alternates a
            // worker moved to) gets a fresh epoch at or past it, so the
            // refetch can be served; connections to the old views are
            // dropped at the next range (`prune`) or at their first refused
            // write.
            // In the order they were made, so failover still prefers the
            // source validated first.
            let mut live: Vec<Arc<View>> = st
                .validated
                .values()
                .filter(|v| v.lineage != view.lineage && !st.dead.contains_key(&v.name))
                .cloned()
                .collect();
            live.sort_by_key(|v| v.epoch);
            for v in live {
                let fresh = st.renew(&v);
                if st.current.as_ref().is_some_and(|c| c.name == fresh.name) {
                    st.current = Some(Arc::clone(&fresh));
                }
                st.validated.insert(fresh.name.clone(), fresh);
            }
            self.dead_count.fetch_add(1, Ordering::Relaxed);
        }
        let stale = self.is_stale(&st, view);
        let text = match &why {
            Dead::Failed(w)
            | Dead::Corrupt(w)
            | Dead::Manifest(w)
            | Dead::Mismatch(w)
            | Dead::Refused(w) => w.clone(),
        };
        if corrupt {
            let report = format!("{}: {text}", view.name);
            if !st.bad_data.contains(&report) {
                st.bad_data.push(report);
            }
        }
        // Bad data takes out every name still reaching the server (its
        // other addresses share its lineage, so they are banned already),
        // also when it was seen through a stale view; anything else only
        // the name that failed, and only if the view is not stale.
        let mut out = if stale {
            Vec::new()
        } else {
            vec![view.name.clone()]
        };
        if corrupt {
            let others: Vec<String> = names
                .into_iter()
                .chain(st.validated.keys().cloned())
                .filter(|n| *n != view.name && self.lineage_now(&st, n) == Some(view.lineage))
                .collect();
            out.extend(others);
            out.sort();
            out.dedup();
        }
        for n in &out {
            if !st.dead.contains_key(n) {
                warn!(source = %n, error = %text, "source failed mid-copy; moving to another");
                self.mark_dead(&mut st, n, why.clone());
            } else if corrupt
                && !matches!(st.dead.get(n), Some(Dead::Corrupt(_) | Dead::Mismatch(_)))
            {
                warn!(source = %n, error = %text, "a retired source also served bad data");
                st.dead.insert(n.clone(), why.clone());
            }
        }
        self.unlock(st);
    }

    /// A file changed on `view`'s source during the fetch (its generation
    /// moved): that source no longer serves the content the copy is made
    /// of. Out of the running if another live source is left (the range
    /// moves there, its content identity checked on validation) — true;
    /// false if none is, and the fetch fails as Changed.
    fn changed(&self, view: &View, path: &str) -> bool {
        let names = self.set.candidates();
        let id = self.set.identity(&view.name);
        // Names of the same server (its IPv4 and IPv6 addresses) are not
        // another source: the file changed there too.
        let same = |n: &str| n == view.name || id.is_some() && self.set.identity(n) == id;
        let mut st = self.state.lock();
        if self.is_stale(&st, view) {
            // A view of a server no longer at that name (revived since):
            // what it saw says nothing about the one there now.
            return true;
        }
        let others = names.iter().any(|n| !same(n) && !st.dead.contains_key(n));
        st.changed.push((view.name.clone(), path.to_string()));
        if !others {
            return false;
        }
        warn!(source = %view.name, path, "a file changed on this source; moving to another");
        for n in names
            .iter()
            .filter(|n| same(n))
            .chain(std::iter::once(&view.name))
        {
            self.mark_dead(
                &mut st,
                n,
                Dead::Mismatch(format!("{path} changed on this source during the fetch")),
            );
        }
        self.unlock(st);
        true
    }

    /// A failure of the whole fetch after a file changed on some source:
    /// if the other sources could not finish the copy either, the fetch
    /// failed because the file changed, and says so.
    fn explain(&self, e: FetchError) -> FetchError {
        let first = self.state.lock().changed.first().cloned();
        match (e, first) {
            // No source was left at all (every other one is out too):
            // taking out the changed one is what left none.
            (
                e @ (FetchError::Connect(_) | FetchError::Mismatch(_) | FetchError::Manifest(_)),
                Some((source, path)),
            ) => {
                warn!(source, path = %path, error = %e, "no other source could finish the copy");
                FetchError::Changed { path }
            }
            // Sources were left but did not finish (busy): that is the
            // failure; the change is mentioned.
            (FetchError::Unavailable(m), Some((source, path))) => FetchError::Unavailable(format!(
                "{m} (earlier, {path} changed on {source}, which was taken out)"
            )),
            (e, _) => e,
        }
    }

    /// Sources taken out because a file changed there mid-copy.
    fn changed_sources(&self) -> Vec<String> {
        let mut v: Vec<String> = self
            .state
            .lock()
            .changed
            .iter()
            .map(|(s, _)| s.clone())
            .collect();
        v.dedup();
        v
    }
}

impl<C: SourceSet> Drop for Sources<'_, C> {
    fn drop(&mut self) {
        let st = self.state.lock();
        self.unlock(st);
    }
}

/// The generations, per target, of a new source whose manifest lists
/// every target not yet complete with the same size and content; an
/// error naming what differs otherwise. Generations are per node and
/// never compared.
fn matching_generations(targets: &[Target], entries: &[Entry]) -> Result<Vec<u128>, String> {
    let by_path: HashMap<&str, &Entry> = entries
        .iter()
        .filter(|e| e.is_file())
        .map(|e| (e.path.as_str(), e))
        .collect();
    let mut differ = Vec::new();
    let mut generations = Vec::with_capacity(targets.len());
    for t in targets {
        if t.done.load(Ordering::Acquire) {
            // A finished file only matters if it must be fetched again
            // (an inconsistent source wrote it): this view can serve it
            // only with the same size and content, else 0 (it cannot).
            generations.push(match by_path.get(t.key.as_str()) {
                Some(e) if e.size == t.size && e.content == t.content => e.generation,
                _ => 0,
            });
            continue;
        }
        match by_path.get(t.key.as_str()) {
            Some(e) if e.size == t.size && e.content == t.content => generations.push(e.generation),
            Some(e) => {
                differ.push(format!(
                    "{} (size {} vs {}, content {:032x} vs {:032x})",
                    t.key, e.size, t.size, e.content, t.content
                ));
                generations.push(0);
            }
            None => {
                differ.push(format!("{} (missing)", t.key));
                generations.push(0);
            }
        }
    }
    if differ.is_empty() {
        return Ok(generations);
    }
    let n = differ.len();
    differ.truncate(FAILED_SHOWN);
    Err(format!("{n} files differ: {}", differ.join(", ")))
}

/// Where a fetch into a device ([`fetch_to_device`]) puts everything: one
/// buffer holding every file at `offset` (4 KiB aligned, in manifest
/// order), then, from the next 2 MiB boundary (so the slots alone can be
/// registered for the NICs whatever the host page size), one landing slot
/// of `slot_len` bytes per worker.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Layout {
    pub files: Vec<Placed>,
    /// Bytes the files take, alignment included: slot 0 starts here, on a
    /// [`DEVICE_SLOTS_ALIGN`] boundary.
    pub files_len: u64,
    pub slots: usize,
    pub slot_len: usize,
}

/// One file in a [`Layout`]: its path under the destination.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Placed {
    pub path: String,
    pub offset: u64,
    pub size: u64,
}

/// Alignment of every file in a [`Layout`].
pub const DEVICE_ALIGN: u64 = 4096;

/// Alignment of a [`Layout`]'s slot area.
pub const DEVICE_SLOTS_ALIGN: u64 = 2 << 20;

impl Layout {
    fn new(targets: &[Target], slots: usize, slot_len: usize) -> Result<Self, FetchError> {
        let mut files = Vec::with_capacity(targets.len());
        let mut at = 0u64;
        for t in targets {
            files.push(Placed {
                path: t.rel.clone(),
                offset: at,
                size: t.size,
            });
            at = at
                .checked_add(t.size)
                .and_then(|e| e.checked_next_multiple_of(DEVICE_ALIGN))
                .ok_or_else(|| {
                    FetchError::Invalid("the files do not fit in a 64-bit device buffer".into())
                })?;
        }
        let at = at
            .checked_next_multiple_of(DEVICE_SLOTS_ALIGN)
            .ok_or_else(|| {
                FetchError::Invalid("the files do not fit in a 64-bit device buffer".into())
            })?;
        let layout = Layout {
            files,
            files_len: at,
            slots,
            slot_len,
        };
        (slot_len as u64)
            .checked_mul(slots as u64)
            .and_then(|s| s.checked_add(at))
            .ok_or_else(|| {
                FetchError::Invalid("the files do not fit in a 64-bit device buffer".into())
            })?;
        Ok(layout)
    }

    /// Bytes the device buffer needs: the files, then the slots.
    pub fn len(&self) -> u64 {
        self.files_len + self.slots as u64 * self.slot_len as u64
    }

    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    /// Where landing slot `slot` starts.
    pub fn slot_offset(&self, slot: usize) -> u64 {
        self.files_len + slot as u64 * self.slot_len as u64
    }
}

/// Host memory a range is read back into (to be checked, and to be written
/// to disk): 4 KiB aligned (it is written with O_DIRECT), at least the
/// slot length.
pub type HostBuffer = Box<dyn std::ops::DerefMut<Target = [u8]> + Send>;

/// Memory outside the host that a fetch lands ranges in (GPU memory):
/// a buffer laid out as a [`Layout`]. Ranges land in a worker's slot by
/// the fetch's `land` function and are then read back, checked and placed
/// at their file's offset through this.
pub trait Device: Sync {
    /// Make the writes the NICs completed so far visible to
    /// [`Device::read_slot`] and [`Device::place`].
    fn make_visible(&self) -> Result<(), String>;
    /// Copy the first `out.len()` bytes of landing slot `slot` into `out`.
    fn read_slot(&self, slot: usize, out: &mut [u8]) -> Result<(), String>;
    /// Copy `bytes` (a range already read back and checked) to `offset` in
    /// the buffer: what is placed is exactly what was checked, whatever
    /// reaches the slot afterwards.
    fn place(&self, bytes: &[u8], offset: u64) -> Result<(), String>;
    /// Copy `out.len()` bytes from `offset` in the buffer (placed bytes)
    /// into `out`: the disk writers read the files from the device.
    fn read(&self, offset: u64, out: &mut [u8]) -> Result<(), String>;
    /// A host buffer of `len` bytes (see [`HostBuffer`]).
    fn host_buffer(&self, len: usize) -> Result<HostBuffer, String>;
}

/// Lands one range in a device slot: (source, device, slot, key, offset,
/// generation, length) -> the range, generation checked, bytes unchecked.
pub type Land<'a, S, D> =
    dyn Fn(&mut S, &D, usize, &str, u64, u128, usize) -> Result<Landed, GetError> + Sync + 'a;

/// Makes a fetch's device for its [`Layout`].
pub type MakeDevice<'a, D> = Box<dyn FnOnce(&Layout) -> Result<D, String> + 'a>;

/// [`Land`] with the device bound: (source, slot, key, offset, generation,
/// length).
type LandSlot<'a, S> =
    dyn Fn(&mut S, usize, &str, u64, u128, usize) -> Result<Landed, GetError> + Sync + 'a;

/// What [`fetch_to_device`] needs besides a plain fetch.
pub struct ToDevice<'a, S, D> {
    /// Makes the device once the layout is known (after the manifest).
    pub make: MakeDevice<'a, D>,
    pub land: &'a Land<'a, S, D>,
    /// Called once every range is in the device, before the disk copy is
    /// complete, with the time since the fetch started. An error fails
    /// the fetch ([`FetchError::Device`]).
    pub ready: &'a (dyn Fn(&D, &Layout, Duration) -> Result<(), String> + Sync + 'a),
}

/// The device side of a completed [`fetch_to_device`].
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DeviceReport {
    pub layout: Layout,
    /// From the start of the fetch to every range in the device.
    pub ready_after: Duration,
}

/// A plain fetch has no device.
enum NoDevice {}

impl Device for NoDevice {
    fn make_visible(&self) -> Result<(), String> {
        match *self {}
    }
    fn read_slot(&self, _: usize, _: &mut [u8]) -> Result<(), String> {
        match *self {}
    }
    fn place(&self, _: &[u8], _: u64) -> Result<(), String> {
        match *self {}
    }
    fn read(&self, _: u64, _: &mut [u8]) -> Result<(), String> {
        match *self {}
    }
    fn host_buffer(&self, _: usize) -> Result<HostBuffer, String> {
        match *self {}
    }
}

/// A fetch's device, as the workers use it.
struct DevicePath<'a, S> {
    device: &'a dyn Device,
    land: &'a LandSlot<'a, S>,
    /// Each target's offset in the device buffer.
    offsets: Vec<u64>,
    slot_len: usize,
}

/// A worker's link to its disk writer.
struct Pipe {
    /// The worker's landing slot.
    slot: usize,
    /// Unbounded: a job is a few words, one per placed piece of a range
    /// (several when a source's buffer is smaller than the range, more
    /// after a refetch) plus one per counted range and refetch. The bytes
    /// wait in the device, not in host memory, so the device copy never
    /// waits for the disk.
    jobs: std::sync::mpsc::Sender<Job>,
    /// Each range is read back into it and checked.
    buf: std::cell::RefCell<HostBuffer>,
}

impl Pipe {
    fn send(&self, job: Job) -> Result<(), FetchError> {
        self.jobs
            .send(job)
            .map_err(|_| FetchError::Device("the disk writer stopped".into()))
    }

    /// Wait until the writer has done every job queued so far.
    fn flush(&self) -> Result<(), FetchError> {
        let (done, wait) = std::sync::mpsc::sync_channel(1);
        self.send(Job::Flush(done))?;
        wait.recv()
            .map_err(|_| FetchError::Device("the disk writer stopped".into()))
    }
}

/// Work for a disk writer, done in order.
enum Job {
    /// Write the `len` placed bytes at `at` of the target, read from the
    /// device, unless the file is being fetched again from a view newer
    /// than `epoch`.
    Write {
        target: usize,
        at: u64,
        len: usize,
        epoch: u64,
    },
    /// One range of the target is complete: its write is queued before.
    Count(usize),
    /// Answer once every earlier job is done.
    Flush(std::sync::mpsc::SyncSender<()>),
}

struct Shared<'a, C: SourceSet> {
    targets: &'a [Target],
    work: &'a [Work],
    dest: &'a OwnedFd,
    dest_path: &'a Path,
    staging: &'a Staging<'a>,
    sources: &'a Sources<'a, C>,
    next: AtomicUsize,
    failed: AtomicBool,
    error: Mutex<Option<FetchError>>,
    ranges: AtomicU64,
    retries: &'a AtomicU64,
    reconnects: &'a AtomicU64,
    /// Cleared after the first O_DIRECT open the filesystem refuses.
    direct: AtomicBool,
    /// Files renamed into place, for the content record: (path under
    /// `dest`, size, local generation, content).
    recorded: Mutex<Vec<Entry>>,
    apply: Apply,
    /// Files and directories whose set-id bits did not stick.
    setid_cleared: &'a AtomicU64,
    opts: &'a FetchOptions,
    device: Option<DevicePath<'a, C::Source>>,
}

/// Fetch everything under `prefix` (relative to the export root; empty
/// for all of it) into `dest`, using sources made by `open` (called once
/// per worker, from that worker's thread). Paths below `prefix` are
/// recreated under `dest`; existing files of the same names are replaced.
pub fn fetch_dir<S, F>(
    open: F,
    prefix: &str,
    dest: &Path,
    opts: &FetchOptions,
) -> Result<FetchReport, FetchError>
where
    S: RangeSource,
    F: Fn() -> Result<S, HydrateError> + Sync,
{
    fetch_from(&One(open), prefix, dest, opts)
}

/// [`fetch_dir`] from whichever of `sources` takes the fetch, moving to
/// another when the one in use fails (see the module docs).
pub fn fetch_from<C: SourceSet>(
    sources: &C,
    prefix: &str,
    dest: &Path,
    opts: &FetchOptions,
) -> Result<FetchReport, FetchError> {
    fetch_impl::<C, NoDevice>(sources, prefix, dest, opts, None, &mut None)
}

/// [`fetch_from`], with every range landing in a device (GPU memory)
/// first: once the manifest is read, `to.make` makes the device for the
/// [`Layout`]; each range lands in a worker's slot (`to.land`), is read
/// back into a host buffer and CRC-checked, placed at its file's offset,
/// and written to `dest` by the worker's disk writer in the background,
/// read back from the device (which holds it): the device copy never
/// waits for the disk. `to.ready` is called once every range is in the device, before
/// the disk copy is complete. Returns when the disk copy is complete (or
/// has failed), as `fetch_from` does, and the device once `to.ready`
/// succeeded: it holds every file whatever happens to the disk copy
/// after that, so a consumer that was told the files are there keeps
/// them. A fetch that cannot copy every listed file (paths the transport
/// cannot request) is refused before anything is transferred: a device
/// copy is all or nothing.
pub fn fetch_to_device<C: SourceSet, D: Device>(
    sources: &C,
    prefix: &str,
    dest: &Path,
    opts: &FetchOptions,
    to: ToDevice<'_, C::Source, D>,
) -> (Result<FetchReport, FetchError>, Option<(DeviceReport, D)>) {
    let mut kept = None;
    let r = fetch_impl(sources, prefix, dest, opts, Some(to), &mut kept);
    (r, kept)
}

/// The fetch; with a device, `kept` gets it once it is ready.
fn fetch_impl<C: SourceSet, D: Device>(
    sources: &C,
    prefix: &str,
    dest: &Path,
    opts: &FetchOptions,
    to: Option<ToDevice<'_, C::Source, D>>,
    kept: &mut Option<(DeviceReport, D)>,
) -> Result<FetchReport, FetchError> {
    if opts.connections == 0 || opts.range_size == 0 || !opts.range_size.is_multiple_of(ALIGN) {
        return Err(FetchError::Invalid(format!(
            "connections must be > 0 and range_size a nonzero multiple of {ALIGN}"
        )));
    }
    if opts.preserve_setid && !(opts.attrs && opts.owners) {
        return Err(FetchError::Invalid(
            "preserve_setid needs owners applied: set-id bits on files the copier owns would hand out its \
             privileges".into()));
    }
    let prefix = prefix.trim_end_matches('/');
    if !prefix.is_empty() && !manifest::is_safe_relative(prefix) {
        return Err(FetchError::Invalid(format!(
            "prefix {prefix:?} is not a plain relative path"
        )));
    }
    if manifest::key_v3(prefix).len() > opts.max_key_len {
        return Err(FetchError::Invalid(format!(
            "prefix {prefix:?} is too long: the manifest key must fit in {} bytes",
            opts.max_key_len
        )));
    }
    let started = Instant::now();
    let (retries, reconnects) = (AtomicU64::new(0), AtomicU64::new(0));
    let chooser = Sources::new(sources, prefix, opts, &retries, &reconnects);
    let (first, source, listing) = chooser.first()?;
    let apply = Apply::new(
        opts,
        listing.has_attrs().then_some(listing.authority),
        &listing.entries,
    );
    if !listing.has_attrs() {
        warn!(source = %source, version = listing.version,
              "the source serves no attributes; copying bytes only (files 0600, directories 0700)");
    }
    let entries = listing.entries;
    let range_size = opts.range_size.min(first.max_range() / ALIGN * ALIGN) as u64;
    if range_size == 0 {
        return Err(FetchError::Invalid(format!(
            "source buffer is smaller than {ALIGN} bytes"
        )));
    }
    let plan = plan(&entries, prefix, opts.max_key_len)?;
    let content_digest = manifest::content_digest(&entries);

    std::fs::create_dir_all(dest).map_err(io_err(dest))?;
    let dest_fd = dirfd::open_dir(dest).map_err(io_err(dest))?;
    let locked = lock(&dest_fd, dest)?;
    if locked {
        remove_stale_staging(&dest_fd, dest)?;
        content::remove_stale_temps(&dest_fd).map_err(io_err(dest))?;
    }
    // Before anything is renamed: never more authority than what was
    // here and what this fetch applies (see `crate::content`). At the end
    // the record is this fetch's own only if DEST held nothing else.
    let authority = apply.attributes.authority();
    // Without the lock another fetch may be filling DEST right now: never
    // take it for empty.
    let fresh = locked
        && dirfd::names(&dest_fd)
            .map_err(io_err(dest))?
            .iter()
            .all(|n| n.to_str().is_ok_and(|n| n.starts_with(RESERVED_PREFIX)));
    // Everything that holds over DEST from above counts too: a fetch into
    // a directory of a copy made without owners has none to give.
    let start = content::authority_above(&dest_fd).and(authority);
    content::record_authority(&dest_fd, start).map_err(io_err(&dest.join(content::ATTRS_FILE)))?;
    // Dropped (removing what is left in it) on every return below.
    let staging = Staging::create(&dest_fd, dest)?;
    let mut prepared = Prepared::default();
    let setid_cleared = AtomicU64::new(0);
    let run = (|| -> Result<u64, FetchError> {
        prepare(
            &plan,
            prefix,
            &dest_fd,
            dest,
            &staging,
            Records { start, locked },
            &mut prepared,
        )?;
        if to.is_some() {
            let mut cannot = plan.failed.iter().chain(&prepared.failed);
            if let Some((path, why)) = cannot.next() {
                return Err(FetchError::NotAll(format!(
                    "{} files cannot be fetched ({path}: {why}): a fetch into a device copies everything or nothing",
                    1 + cannot.count())));
            }
        }
        // The start records (and their directory entries) are durable
        // before anything is renamed in: a crash leaves them, never the
        // higher record from before over files of this fetch.
        content::sync_all(&std::mem::take(&mut prepared.unsynced)).map_err(io_err(dest))?;
        let targets = &prepared.targets;
        let view = chooser.start(
            source.clone(),
            targets.iter().map(|t| t.generation).collect(),
        );
        let mut work = Vec::new();
        for (i, t) in targets.iter().enumerate() {
            let mut offset = 0;
            while offset < t.size {
                let len = (t.size - offset).min(range_size);
                work.push(Work {
                    target: i,
                    offset,
                    len,
                });
                offset += len;
            }
            // Set before any worker starts, so the last range's decrement
            // finds the true count.
            t.remaining
                .store(t.size.div_ceil(range_size), Ordering::Relaxed);
        }
        let workers = opts.connections.min(work.len());
        // With a device: made for the layout, one slot, disk writer and two
        // host buffers (the worker's and its writer's) per worker.
        let made = match to {
            None => None,
            Some(ToDevice { make, land, ready }) => {
                // One slot even with nothing to fetch: a device always has one.
                let layout = Layout::new(targets, workers.max(1), range_size as usize)?;
                let device = make(&layout).map_err(FetchError::Device)?;
                Some((device, layout, land, ready))
            }
        };
        let land_slot = made.as_ref().map(|(device, _, land, _)| {
            move |s: &mut C::Source,
                  slot: usize,
                  key: &str,
                  at: u64,
                  generation: u128,
                  len: usize| { land(s, device, slot, key, at, generation, len) }
        });
        let mut pipes = Vec::new();
        let mut writers = Vec::new();
        if let Some((device, ..)) = &made {
            let host_buffer = || {
                let buf = device
                    .host_buffer(range_size as usize)
                    .map_err(FetchError::Device)?;
                if buf.len() < range_size as usize || !(buf.as_ptr() as usize).is_multiple_of(ALIGN)
                {
                    return Err(FetchError::Device(format!(
                        "the device's host buffers must be {ALIGN}-byte aligned and {range_size} bytes")));
                }
                Ok(buf)
            };
            for slot in 0..workers {
                let (jobs, jobs_rx) = std::sync::mpsc::channel();
                writers.push((jobs_rx, host_buffer()?));
                pipes.push(Pipe {
                    slot,
                    jobs,
                    buf: std::cell::RefCell::new(host_buffer()?),
                });
            }
        }
        let state = Shared {
            targets,
            work: &work,
            dest: &dest_fd,
            dest_path: dest,
            staging: &staging,
            sources: &chooser,
            next: AtomicUsize::new(0),
            failed: AtomicBool::new(false),
            error: Mutex::new(None),
            ranges: AtomicU64::new(0),
            retries: &retries,
            reconnects: &reconnects,
            direct: AtomicBool::new(opts.direct_io),
            recorded: Mutex::new(Vec::new()),
            apply,
            setid_cleared: &setid_cleared,
            opts,
            device: made
                .as_ref()
                .zip(land_slot.as_ref())
                .map(|((device, layout, ..), land)| DevicePath {
                    device: device as &dyn Device,
                    land: land as &LandSlot<C::Source>,
                    offsets: layout.files.iter().map(|p| p.offset).collect(),
                    slot_len: layout.slot_len,
                }),
        };
        for t in targets.iter().filter(|t| t.size == 0) {
            state.finalize(t)?;
        }
        let mut ready_after = None;
        let shared = &state;
        std::thread::scope(|scope| {
            // Each ends once its worker is gone and its queue is written.
            for (jobs, buf) in writers {
                scope.spawn(move || shared.write_behind(jobs, buf));
            }
            let mut pipes = pipes.into_iter();
            if workers > 0 {
                let mine = pipes.next();
                let others: Vec<_> = (1..workers)
                    .map(|_| {
                        let pipe = pipes.next();
                        scope.spawn(move || shared.run(None, pipe))
                    })
                    .collect();
                shared.run(Some(Conn { src: first, view }), mine);
                for h in others {
                    if let Err(panic) = h.join() {
                        std::panic::resume_unwind(panic);
                    }
                }
            } else {
                drop(first);
            }
            // Every range is in the device; the disk writers may still be
            // writing.
            if let Some((device, layout, _, ready)) = &made {
                if !shared.failed.load(Ordering::Relaxed) {
                    let after = started.elapsed();
                    match ready(device, layout, after) {
                        Ok(()) => ready_after = Some(after),
                        Err(e) => shared.fail(FetchError::Device(e)),
                    }
                }
            }
        });
        // Whatever was renamed into place is complete: record it even when
        // the fetch as a whole failed.
        let recorded = std::mem::take(&mut *shared.recorded.lock());
        let error = shared.error.lock().take();
        let ranges = shared.ranges.load(Ordering::Relaxed);
        // The device was ready: the caller keeps it, whatever follows.
        drop(state);
        match (made, ready_after) {
            (Some((device, layout, ..)), Some(ready_after)) => {
                *kept = Some((
                    DeviceReport {
                        layout,
                        ready_after,
                    },
                    device,
                ))
            }
            (Some(_), None) if error.is_none() => {
                return Err(FetchError::Device(
                    "invariant: no failure, but the device was never ready".into(),
                ));
            }
            _ => {}
        }
        let record = if recorded.is_empty() {
            Ok(())
        } else {
            content::record(&dest_fd, &recorded)
        };
        if let Some(e) = error {
            return Err(chooser.explain(e));
        }
        record.map_err(io_err(&dest.join(content::CONTENT_FILE)))?;
        Ok(ranges)
    })();
    drop(staging);
    let ranges = match run {
        Ok(r) => r,
        Err(e) => {
            // The directories made or opened up so far still get their
            // attributes (or their old mode back), as far as possible.
            if let Err((d, error)) = finish_dirs(
                &plan,
                &prepared,
                &dest_fd,
                apply,
                &setid_cleared,
                None,
                &mut Vec::new(),
            ) {
                warn!(dir = %dest.join(d).display(), %error, "after a failed fetch");
            }
            return Err(e);
        }
    };
    let after = authority.and(if fresh {
        content::authority_above_parent(&dest_fd)
    } else {
        content::authority_above(&dest_fd)
    });
    let mut unsynced = Vec::new();
    let finished = finish_dirs(
        &plan,
        &prepared,
        &dest_fd,
        apply,
        &setid_cleared,
        Some(authority),
        &mut unsynced,
    );
    // The end records' bytes (their renames are durable with each
    // directory's fsync above): until this, a crash leaves a directory its
    // start record or an unreadable one, which reads as no authority.
    content::sync_all(&unsynced).map_err(io_err(dest))?;
    finished.map_err(|(d, error)| FetchError::Io {
        path: dest.join(d),
        error,
    })?;
    content::record_authority(&dest_fd, after).map_err(io_err(&dest.join(content::ATTRS_FILE)))?;
    let targets = &prepared.targets;
    let report = FetchReport {
        files: targets.len(),
        bytes: targets.iter().map(|t| t.size).sum(),
        ranges,
        retries: retries.load(Ordering::Relaxed),
        reconnects: reconnects.load(Ordering::Relaxed),
        source,
        failovers: chooser.failovers.load(Ordering::Relaxed),
        changed_on: chooser.changed_sources(),
        content_digest,
        attributes: apply.attributes,
        setid_kept: apply.keeps_setid(),
        setid_cleared: setid_cleared.load(Ordering::Relaxed),
        elapsed: started.elapsed(),
    };
    let mut failed = plan.failed;
    failed.append(&mut prepared.failed);
    if failed.is_empty() {
        Ok(report)
    } else {
        Err(FetchError::Incomplete {
            report: Box::new(report),
            failed,
        })
    }
}

/// Why one attempt-with-retries gave up.
enum Fail {
    Get(GetError),
    Connect(HydrateError),
    /// The caller's own handling of the bytes failed.
    Local(FetchError),
}

/// Run `op` against `src` under the retry policy: a retryable error
/// costs one attempt and a backoff (doubling per attempt); a transport
/// error also replaces the connection first, and a reconnect that fails
/// costs an attempt and a backoff of its own instead of ending the
/// fetch. Gives up after `max_retries` attempts. A busy server (NO_SLOT)
/// is not a failure and spends no attempt: `Busy` is returned at once, so
/// the caller can back off or move the work to another source. `op` returns the
/// transport's verdict outside and its own result inside.
fn with_retries<S: RangeSource, T>(
    src: &mut S,
    opts: &FetchOptions,
    retries: &AtomicU64,
    reconnects: &AtomicU64,
    mut op: impl FnMut(&mut S) -> Result<Result<T, FetchError>, GetError>,
) -> Result<T, Fail> {
    let mut attempt = 0u32;
    let mut broken = false;
    let backoff = |attempt: &mut u32| {
        *attempt += 1;
        retries.fetch_add(1, Ordering::Relaxed);
        std::thread::sleep(nvstor_rdma::object::backoff(opts.retry_backoff, *attempt));
    };
    loop {
        if broken {
            match src.reconnect() {
                Ok(()) => {
                    reconnects.fetch_add(1, Ordering::Relaxed);
                }
                Err(e) if attempt < opts.max_retries => {
                    warn!(error = %e, attempt, "reconnect failed; retrying");
                    backoff(&mut attempt);
                    continue;
                }
                Err(e) => return Err(Fail::Connect(e)),
            }
        }
        match op(src) {
            Ok(r) => return r.map_err(Fail::Local),
            // Busy spends no attempt and is not waited out here: the
            // caller backs off on the same connection or moves the work
            // to another source (`Shared::on_busy`).
            Err(GetError::Busy) => return Err(Fail::Get(GetError::Busy)),
            Err(e) if e.is_retryable() && attempt < opts.max_retries => {
                broken = matches!(e, GetError::Transport(_));
                backoff(&mut attempt);
            }
            Err(e) => return Err(Fail::Get(e)),
        }
    }
}

/// Fetch and decode the manifest of `prefix`, refetching if the export
/// changes while it is read. A busy answer is asked again on the same
/// connection after a pause (decorrelated jitter) for up to `opts.wait`
/// from the first busy answer.
pub(crate) fn fetch_manifest<S: RangeSource>(
    src: &mut S,
    prefix: &str,
    opts: &FetchOptions,
    retries: &AtomicU64,
    reconnects: &AtomicU64,
) -> Result<Manifest, FetchError> {
    fetch_listing_within(
        src,
        &manifest::key(prefix),
        opts,
        retries,
        reconnects,
        &mut BusyBudget::new(opts, opts.wait),
    )?
    .ok_or_else(|| FetchError::Manifest(format!("{prefix:?}: no such directory on the source")))
}

/// How long busy answers are waited out: `wait` from the first busy
/// answer, shared by every read made under it (one check's version 3 and
/// version 2 listing reads together wait at most one budget).
struct BusyBudget {
    backoff: Backoff,
    since: Option<Instant>,
    wait: Duration,
}

impl BusyBudget {
    fn new(opts: &FetchOptions, wait: Duration) -> Self {
        Self {
            backoff: Backoff::new(opts),
            since: None,
            wait,
        }
    }
}

/// Run `op` again after a pause while it answers busy, within the
/// budget.
// FetchError / Fail are large across this module; boxing them is a
// separate change.
#[allow(clippy::result_large_err)]
fn busy_retry<T>(
    budget: &mut BusyBudget,
    mut op: impl FnMut() -> Result<T, Fail>,
) -> Result<T, Fail> {
    loop {
        match op() {
            Err(Fail::Get(GetError::Busy)) => {
                let since = *budget.since.get_or_insert_with(Instant::now);
                let left = budget.wait.saturating_sub(since.elapsed());
                if left.is_zero() {
                    return Err(Fail::Get(GetError::Busy));
                }
                std::thread::sleep(budget.backoff.next().min(left));
            }
            r => return r,
        }
    }
}

/// Fetch and decode the listing under `key` (either version), refetching
/// if the export changes while it is read; `None` if the source has no
/// such key (an older source asked for version 3, or no such prefix).
/// Busy answers are asked again on the same connection after a pause
/// (decorrelated jitter) for at most `busy_wait` from the first one.
// FetchError / Fail are large across this module; boxing them is a
// separate change.
#[allow(clippy::result_large_err)]
fn fetch_listing_within<S: RangeSource>(
    src: &mut S,
    key: &str,
    opts: &FetchOptions,
    retries: &AtomicU64,
    reconnects: &AtomicU64,
    budget: &mut BusyBudget,
) -> Result<Option<Manifest>, FetchError> {
    let busy_wait = budget.wait;
    // A retryable error that outlasted the retries says the source is
    // unwell, not what its listing is: a failed open (tried again later),
    // never a refusal that takes the source out for good. Busy past
    // `busy_wait` is neither: the source is busy.
    let fail = |f: Fail| match f {
        Fail::Get(GetError::Busy) => FetchError::Unavailable(format!(
            "the listing read was answered busy (no free slot or worker) for {:.1} s",
            busy_wait.as_secs_f64()
        )),
        Fail::Get(e) if e.is_retryable() => FetchError::Connect(format!("{key}: {e}")),
        Fail::Get(e) => FetchError::Manifest(e.to_string()),
        Fail::Connect(e) => connect_err(e),
        Fail::Local(e) => e,
    };
    for _ in 0..MANIFEST_ATTEMPTS {
        let stat = busy_retry(budget, || {
            with_retries(src, opts, retries, reconnects, |s| s.stat(key).map(Ok))
        });
        let (size, generation) = match stat {
            Ok(x) => x,
            Err(Fail::Get(GetError::NotFound)) => return Ok(None),
            Err(f) => return Err(fail(f)),
        };
        // The size is the server's word: bound it before allocating.
        if size > opts.max_manifest_bytes {
            return Err(FetchError::Manifest(format!(
                "the server reports a {size}-byte manifest, over the {}-byte limit",
                opts.max_manifest_bytes
            )));
        }
        let cap = usize::try_from(size).map_err(|_| {
            FetchError::Manifest(format!("{size}-byte manifest exceeds the address space"))
        })?;
        let mut bytes = Vec::new();
        bytes.try_reserve_exact(cap).map_err(|e| {
            FetchError::Manifest(format!("cannot buffer a {size}-byte manifest: {e}"))
        })?;
        let mut changed = false;
        while (bytes.len() as u64) < size {
            let want = (size - bytes.len() as u64).min(src.max_range() as u64) as usize;
            let at = bytes.len() as u64;
            let got = busy_retry(budget, || {
                with_retries(src, opts, retries, reconnects, |s| {
                    s.fetch(key, at, generation, want).map(|b| {
                        if b.is_empty() {
                            return Err(FetchError::Manifest(format!(
                                "ended at {at} of {size} bytes"
                            )));
                        }
                        bytes.extend_from_slice(&b[..b.len().min(want)]);
                        Ok(())
                    })
                })
            });
            match got {
                Ok(()) => {}
                Err(Fail::Get(GetError::GenerationChanged)) => {
                    changed = true;
                    break;
                }
                Err(f) => return Err(fail(f)),
            }
        }
        if !changed {
            return manifest::decode(&bytes)
                .map(Some)
                .map_err(FetchError::Manifest);
        }
    }
    Err(FetchError::Manifest(format!(
        "the export kept changing; gave up after {MANIFEST_ATTEMPTS} listings"
    )))
}

/// Longest path below the destination a manifest entry may have: what
/// `openat2` takes (PATH_MAX with its NUL).
const MAX_REL_PATH: usize = libc::PATH_MAX as usize - 1;
/// Longest name of one component (NAME_MAX).
const MAX_NAME: usize = 255;

/// The manifest mapped onto `dest`, checked as a whole before anything
/// is created. Paths are slices of the manifest's own, so its size bounds
/// the plan's: no ancestor path is copied.
struct Plan<'e> {
    /// (entry, path under `dest`), in manifest order.
    files: Vec<(&'e Entry, &'e str)>,
    /// Every directory under `dest` the copy has ("" is `dest`), deepest
    /// first, so each is synced (and given its attributes) after its
    /// children; with the attributes the manifest lists for it, if it
    /// lists it.
    dirs: Vec<(&'e str, Option<Attrs>)>,
    /// Entries that cannot be fetched: (manifest path, reason).
    failed: Vec<(String, String)>,
}

/// A node of the tree a plan is built in.
enum Node<'e> {
    /// A directory: listed (with attributes) or only a parent of entries.
    Dir(Option<Attrs>),
    File(&'e Entry),
}

/// Map `entries` onto `dest`. Built as a tree keyed by (parent, name),
/// so each entry costs its own path's length, however deep it is and
/// however many parents it shares or does not. A path longer than
/// [`MAX_REL_PATH`], or with a name longer than [`MAX_NAME`], cannot
/// exist under `dest` and is refused per entry, like a file key longer
/// than the transport carries or a name nvstor-peer reserves.
fn plan<'e>(
    entries: &'e [Entry],
    prefix: &str,
    max_key_len: usize,
) -> Result<Plan<'e>, FetchError> {
    // (path under dest, depth, node); 0 is dest itself.
    let mut nodes: Vec<(&'e str, usize, Node<'e>)> = vec![("", 0, Node::Dir(None))];
    let mut index: HashMap<(usize, &'e str), usize> = HashMap::new();
    let mut files = Vec::new();
    let mut failed = Vec::new();
    let conflict = |e: &Entry| {
        FetchError::Manifest(format!(
            "{} is listed both as a file and as a directory",
            e.path
        ))
    };
    for e in entries {
        let rel = if prefix.is_empty() {
            e.path.as_str()
        } else {
            e.path
                .strip_prefix(prefix)
                .and_then(|r| r.strip_prefix('/'))
                .ok_or_else(|| {
                    FetchError::Manifest(format!(
                        "{} is outside the requested prefix {prefix}",
                        e.path
                    ))
                })?
        };
        if !manifest::is_safe_relative(rel) {
            return Err(FetchError::Manifest(format!(
                "bad or duplicate path {}",
                e.path
            )));
        }
        let refuse = if e.is_file() && e.path.len() > max_key_len {
            Some(format!(
                "path is {} bytes; the transport carries keys of up to {max_key_len}",
                e.path.len()
            ))
        } else if rel.len() > MAX_REL_PATH {
            Some(format!(
                "path is {} bytes; at most {MAX_REL_PATH} can be created",
                rel.len()
            ))
        } else if rel.split('/').any(|c| c.len() > MAX_NAME) {
            Some(format!("a name is longer than {MAX_NAME} bytes"))
        } else if rel.split('/').any(|c| c.starts_with(RESERVED_PREFIX)) {
            Some(format!(
                "names starting with {RESERVED_PREFIX} are reserved for nvstor-peer"
            ))
        } else {
            None
        };
        if let Some(why) = refuse {
            failed.push((e.path.clone(), why));
            continue;
        }
        let mut parent = 0usize;
        let mut at = 0usize;
        let mut names = rel.split('/').peekable();
        while let Some(name) = names.next() {
            at += name.len();
            let here = &rel[..at];
            at += 1;
            let depth = nodes[parent].1 + 1;
            let last = names.peek().is_none();
            match index.get(&(parent, name)).copied() {
                Some(id) if !last => match nodes[id].2 {
                    Node::Dir(_) => parent = id,
                    Node::File(f) => return Err(conflict(f)),
                },
                Some(id) => match (&mut nodes[id].2, e.is_file()) {
                    (Node::Dir(attrs @ None), false) => *attrs = Some(e.attrs),
                    (Node::Dir(None), true)
                    | (Node::File(_), false)
                    | (Node::Dir(Some(_)), true) => return Err(conflict(e)),
                    _ => {
                        return Err(FetchError::Manifest(format!(
                            "bad or duplicate path {}",
                            e.path
                        )))
                    }
                },
                None => {
                    let node = if !last {
                        Node::Dir(None)
                    } else if e.is_file() {
                        Node::File(e)
                    } else {
                        Node::Dir(Some(e.attrs))
                    };
                    index.insert((parent, name), nodes.len());
                    nodes.push((here, depth, node));
                    if last && e.is_file() {
                        files.push((e, here));
                    }
                    parent = nodes.len() - 1;
                }
            }
        }
    }
    let mut dirs: Vec<(usize, &'e str, Option<Attrs>)> = nodes
        .iter()
        .filter_map(|(rel, depth, n)| match n {
            Node::Dir(a) => Some((*depth, *rel, *a)),
            Node::File(_) => None,
        })
        .collect();
    dirs.sort_by_key(|d| std::cmp::Reverse(d.0));
    Ok(Plan {
        files,
        dirs: dirs.into_iter().map(|(_, rel, a)| (rel, a)).collect(),
        failed,
    })
}

/// What [`prepare`] needs for the directories' records.
#[derive(Clone, Copy, Debug)]
struct Records {
    /// What the fetch gives AND what holds over DEST.
    start: Authority,
    /// DEST's lock is held: stale temporaries may be removed.
    locked: bool,
}

/// What a directory of the copy says about itself.
#[derive(Clone, Copy, Debug)]
struct DirRecord {
    /// This fetch made it: everything in it is this fetch's.
    created: bool,
    /// What holds for it while the copy runs: the fetch's start authority
    /// AND every record at or above it (its own earlier one included).
    start: Authority,
    /// Whether it has a record file now (absence means full authority).
    written: bool,
}

/// What [`prepare`] made, for the rest of the fetch and for the
/// directory pass at the end, which also runs after a failure.
#[derive(Default)]
struct Prepared<'e> {
    targets: Vec<Target>,
    /// Directories and files that could not be created: (manifest path,
    /// reason). The copy goes on without them.
    failed: Vec<(String, String)>,
    /// Directories the copy skips (they, or a parent, could not be
    /// created, opened or given their record), and why.
    missing: HashMap<&'e str, String>,
    /// Per directory of the copy: its record (see [`DirRecord`]).
    records: HashMap<&'e str, DirRecord>,
    /// Records written unsynced, to be synced together.
    unsynced: Vec<OwnedFd>,
    /// The caller's own directories that lacked owner rwx and were given
    /// it for the copy: their mode before.
    relaxed: HashMap<&'e str, u32>,
}

/// Create the directories and the preallocated staging files. Hard
/// errors (a symlink or a file where a directory goes) end the fetch; a
/// directory that cannot be made or opened goes to `failed` with
/// everything below it, and the copy goes on. On error the caller drops
/// `staging`, which removes every file made so far, and runs the
/// directory pass over what `prepared` holds.
///
/// Directories are created 0700 and staging files 0600: private until
/// the copy is complete, and private afterwards when no mode is applied.
/// An existing directory of the caller's without owner rwx (a 0555
/// directory of an earlier copy) gets it for the copy; the pass at the
/// end gives it its listed mode, or its old one back.
fn prepare<'e>(
    plan: &Plan<'e>,
    prefix: &str,
    dest: &OwnedFd,
    dest_path: &Path,
    staging: &Staging,
    records: Records,
    prepared: &mut Prepared<'e>,
) -> Result<(), FetchError> {
    let Records { start, locked } = records;
    let key = |rel: &str| {
        if prefix.is_empty() {
            rel.to_string()
        } else {
            format!("{prefix}/{rel}")
        }
    };
    // Shallowest first, so each parent exists before its children.
    for &(d, listed) in plan.dirs.iter().rev().filter(|(d, _)| !d.is_empty()) {
        let (parent, name) = d.rsplit_once('/').unwrap_or(("", d));
        let path = dest_path.join(d);
        let skip = |prepared: &mut Prepared<'e>, why: String| {
            if listed.is_some() {
                prepared.failed.push((key(d), why.clone()));
            }
            prepared.missing.insert(d, why);
        };
        if let Some(why) = prepared.missing.get(parent) {
            let why = format!("its parent directory {parent}: {why}");
            skip(prepared, why);
            continue;
        }
        let parent_fd = dirfd::open_dir_beneath(dest, parent).map_err(io_err(&path))?;
        let created = match dirfd::cstring(name.as_bytes())
            .and_then(|c| dirfd::mkdir_made_at(&parent_fd, &c, 0o700))
        {
            Ok(created) => created,
            Err(e) => {
                skip(prepared, format!("cannot create the directory: {e}"));
                continue;
            }
        };
        let fd = match dirfd::open_dir_beneath(dest, d) {
            Ok(fd) => fd,
            Err(e)
                if matches!(
                    e.raw_os_error(),
                    Some(libc::ENOTDIR | libc::ELOOP | libc::EXDEV)
                ) =>
            {
                return Err(FetchError::Io {
                    error: io::Error::new(
                        e.kind(),
                        format!("not a directory (symlinks are not followed): {e}"),
                    ),
                    path,
                });
            }
            Err(e) => {
                skip(prepared, format!("cannot open the directory: {e}"));
                continue;
            }
        };
        let relaxed = match open_up(&fd) {
            Ok(old) => old,
            Err(e) => {
                skip(
                    prepared,
                    format!("cannot give the directory owner rwx: {e}"),
                );
                continue;
            }
        };
        // The copy describes itself in every directory, before anything
        // is renamed into it, so a directory served on its own (a bind
        // mount, a subPath) still carries what holds for it: what this
        // fetch gives AND every record at or above it, its own earlier
        // one included (files of an earlier copy stay in it). No file for
        // full authority in a directory that had none: absence says that.
        let existing = if created {
            None
        } else {
            Some(content::read_record(&fd).unwrap_or(Some(Authority::NONE)))
        };
        let start_here = start.and(content::authority_above(&fd));
        let had = existing.flatten().is_some();
        let write = had || start_here != Authority::FULL;
        let recorded = if write {
            let stale = if locked {
                content::remove_stale_temps(&fd)
            } else {
                Ok(())
            };
            stale
                .and_then(|()| content::record_authority_unsynced(&fd, start_here))
                .map(|f| prepared.unsynced.push(OwnedFd::from(f)))
        } else {
            Ok(())
        };
        if let Err(e) = recorded {
            // Not used: give an opened-up directory its mode back now.
            if let Some(old) = relaxed {
                // SAFETY: valid descriptor.
                if unsafe { libc::fchmod(fd.as_raw_fd(), old) } != 0 {
                    warn!(dir = %path.display(), error = %io::Error::last_os_error(),
                          "cannot give a skipped directory its mode back");
                }
            }
            skip(
                prepared,
                format!("cannot write its {}: {e}", content::ATTRS_FILE),
            );
            continue;
        }
        if let Some(old) = relaxed {
            prepared.relaxed.insert(d, old);
        }
        if write {
            prepared.unsynced.push(fd);
        }
        // Without the lock another fetch may be renaming its files into a
        // directory this one made: then it is not all this fetch's, and
        // its end record may not rise above its start one.
        prepared.records.insert(
            d,
            DirRecord {
                created: created && locked,
                start: start_here,
                written: write,
            },
        );
    }
    for (e, rel) in &plan.files {
        let path = dest_path.join(rel);
        let (parent, name) = rel.rsplit_once('/').unwrap_or(("", rel));
        if let Some(why) = prepared.missing.get(parent) {
            prepared
                .failed
                .push((e.path.clone(), format!("its directory {parent}: {why}")));
            continue;
        }
        let parent_fd = dirfd::open_dir_beneath(dest, parent).map_err(io_err(&path))?;
        let existing = dirfd::stat_at(
            &parent_fd,
            &dirfd::cstring(name.as_bytes()).map_err(io_err(&path))?,
        )
        .map_err(io_err(&path))?;
        if existing.is_some_and(|st| !dirfd::is_reg(&st)) {
            return Err(FetchError::Io {
                path,
                error: io::Error::new(
                    io::ErrorKind::AlreadyExists,
                    "exists and is not a regular file",
                ),
            });
        }
        let i = prepared.targets.len();
        let staged = dirfd::cstring(i.to_string().as_bytes()).map_err(io_err(&path))?;
        let file = staging.create_file(&staged, 0o600).map_err(io_err(&path))?;
        preallocate(&file, e.size).map_err(io_err(&path))?;
        prepared.targets.push(Target {
            key: e.path.clone(),
            size: e.size,
            generation: e.generation,
            content: e.content,
            rel: rel.to_string(),
            attrs: e.attrs,
            staging: staged,
            remaining: AtomicU64::new(0),
            done: AtomicBool::new(false),
            min_epoch: AtomicU64::new(0),
            rewrite: RwLock::new(()),
            writers: AtomicU64::new(0),
            setid_counted: AtomicBool::new(false),
            refetching: Mutex::new(()),
        });
    }
    Ok(())
}

/// Give a directory of the caller's that lacks owner rwx that much;
/// its mode before, if it changed it.
fn open_up(dir: &OwnedFd) -> io::Result<Option<u32>> {
    let st = dirfd::fstat(dir)?;
    // SAFETY: geteuid has no arguments and cannot fail.
    if st.st_uid != unsafe { libc::geteuid() } || st.st_mode & 0o700 == 0o700 {
        return Ok(None);
    }
    let old = st.st_mode & manifest::MODE_BITS;
    // SAFETY: valid descriptor.
    if unsafe { libc::fchmod(dir.as_raw_fd(), old | 0o700) } != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(Some(old))
}

/// After every file is in place: give each directory the manifest lists
/// its attributes (or, when none are applied, give one [`prepare`] opened
/// up its old mode back) and sync every directory of the copy, deepest
/// first, each on one descriptor opened beneath `dest` without following
/// symlinks. Deepest first, so a child's entries are final before its
/// parent's mtime is set, and a parent's mode (0555, or one without
/// search permission) applies only once nothing below it is opened
/// again. An error does not stop the pass: every other directory still
/// gets its attributes, and the first error is returned.
///
/// `applied`: what the fetch gave the files, for each directory's end
/// record (written before its mtime is set, since writing it moves the
/// mtime): `applied` itself in a directory this fetch created (all in it
/// is this fetch's), otherwise `applied` AND its start record AND every
/// record at or above it, so files an earlier copy left there keep what
/// was said of them. Written only when it differs from what the
/// directory says already, unsynced; the files are returned for the
/// caller to sync. `None` after a failed fetch, which leaves the start
/// records. A record that cannot be written does not stop the rest of the
/// directory's pass; its error is returned with the others'.
fn finish_dirs(
    plan: &Plan,
    prepared: &Prepared,
    dest: &OwnedFd,
    apply: Apply,
    setid_cleared: &AtomicU64,
    applied: Option<Authority>,
    unsynced: &mut Vec<OwnedFd>,
) -> Result<(), (String, io::Error)> {
    let mut first = None;
    for &(d, listed) in &plan.dirs {
        if prepared.missing.contains_key(d) {
            continue;
        }
        let done = dirfd::open_dir_beneath(dest, d).and_then(|fd| {
            // (record it has now, record it ends with) when they differ.
            let change = match (applied, prepared.records.get(d)) {
                (Some(applied), Some(r)) => {
                    let end = if r.created {
                        applied
                    } else {
                        applied.and(r.start).and(content::authority_above(&fd))
                    };
                    let now = if r.written { r.start } else { Authority::FULL };
                    (end != now).then_some((now, end))
                }
                _ => None,
            };
            let mut record_err = None;
            match (listed, prepared.relaxed.get(d)) {
                (Some(a), _) if apply.on() => match finish_dir(apply, &fd, &a, change.map(|c| c.1), unsynced) {
                    Ok((cleared, e)) => {
                        if cleared {
                            setid_cleared.fetch_add(1, Ordering::Relaxed);
                        }
                        record_err = e;
                    }
                    Err(e) => {
                        // Not given its owner or mode: private again, and
                        // what it said before this fetch's end record.
                        #[cfg(test)]
                        let change = if tests::CRASH_AFTER_ERROR.with(|c| c.get()) { None } else { change };
                        if let Some((now, _)) = change {
                            put_back(d, &fd, now, unsynced);
                        }
                        // SAFETY: valid descriptor.
                        let reset = if unsafe { libc::fchmod(fd.as_raw_fd(), 0o700) } == 0 {
                            Ok(())
                        } else {
                            // Its own errno, before anything else runs.
                            Err(io::Error::last_os_error())
                        };
                        #[cfg(test)]
                        let reset = if tests::FAIL_RESET.with(|f| f.get()) {
                            Err(io::Error::from_raw_os_error(libc::EROFS))
                        } else {
                            reset
                        };
                        // Its entries as they are now (the record put back
                        // above) survive a crash too.
                        // SAFETY: valid descriptor.
                        if unsafe { libc::fsync(fd.as_raw_fd()) } != 0 {
                            tracing::error!(dir = d, error = %io::Error::last_os_error(),
                                            "cannot sync a directory whose attributes failed");
                        }
                        if let Err(r) = reset {
                            tracing::error!(dir = d, error = %r, "a directory left with its listed mode, \
                                            owned by this fetch: it could not be made private again");
                            return Err(io::Error::new(e.kind(), format!(
                                "{e}; and it could not be made private again ({r}): check its mode")));
                        }
                        return Err(e);
                    }
                },
                (_, relaxed) => {
                    // No attributes to give: the end record (while it is
                    // still writable), then its old mode back.
                    if let Some((_, end)) = change {
                        record_err = write_end_record(&fd, end, unsynced).err();
                    }
                    if let Some(&old) = relaxed {
                        // SAFETY: valid descriptor.
                        if unsafe { libc::fchmod(fd.as_raw_fd(), old) } != 0 {
                            return Err(io::Error::last_os_error());
                        }
                    }
                }
            }
            File::from(fd).sync_all()?;
            record_err.map_or(Ok(()), Err)
        });
        if let Err(e) = done {
            first.get_or_insert((d.to_string(), e));
        }
    }
    first.map_or(Ok(()), Err)
}

fn write_end_record(fd: &OwnedFd, end: Authority, unsynced: &mut Vec<OwnedFd>) -> io::Result<()> {
    content::record_authority_unsynced(fd, end)
        .map(|f| unsynced.push(OwnedFd::from(f)))
        .map_err(|e| {
            io::Error::new(
                e.kind(),
                format!("cannot write its {}: {e}", content::ATTRS_FILE),
            )
        })
}

/// One change [`finish_dir`] makes to a directory, in the order it makes
/// them ([`dir_steps`]).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum DirStep {
    Owner(u32, u32),
    /// The mode while the end record is still to be written (owner rwx
    /// added: a final 0555 would refuse the record).
    InterimMode(u32),
    Record(Authority),
    /// The final mode, set-id bits included when kept.
    FinalMode(u32),
    Mtime(i64, u32),
}

/// The order a directory is given its attributes and end record: the
/// owner first, while the directory is still 0700 and the fetch's own (so
/// the fetcher's group never holds the listed group bits, even across a
/// crash; owners are applied only with CAP_FOWNER, which the chmods after
/// it need); then the mode with owner rwx; then the end record, once the
/// owner and mode are in place (a crash or a refused chown before it
/// leaves the start record); then the final mode; the mtime last, since
/// writing the record moves it.
fn dir_steps(apply: Apply, a: &Attrs, record: Option<Authority>) -> Vec<DirStep> {
    let base = Apply::base_mode(a.mode, true);
    let interim = if record.is_some() { base | 0o700 } else { base };
    let setid = a.mode & (libc::S_ISUID | libc::S_ISGID);
    let last = if apply.keeps_setid() && setid != 0 {
        base | setid
    } else {
        base
    };
    let mut steps = Vec::with_capacity(5);
    if apply.attributes.owners == Applied::Yes {
        steps.push(DirStep::Owner(a.uid, a.gid));
    }
    steps.push(DirStep::InterimMode(interim));
    if let Some(r) = record {
        steps.push(DirStep::Record(r));
    }
    if last != interim || last & (libc::S_ISUID | libc::S_ISGID) != 0 {
        steps.push(DirStep::FinalMode(last));
    }
    steps.push(DirStep::Mtime(a.mtime_sec, a.mtime_nsec));
    steps
}

/// Give the directory `fd` the attributes `a` and the end record `record`
/// ([`dir_steps`]). Returns whether kept set-id bits did not stick and
/// the record's error, if only that failed; an attribute error is
/// returned as such, and the caller puts the start record back.
fn finish_dir(
    apply: Apply,
    fd: &OwnedFd,
    a: &Attrs,
    record: Option<Authority>,
    unsynced: &mut Vec<OwnedFd>,
) -> io::Result<(bool, Option<io::Error>)> {
    let raw = fd.as_raw_fd();
    let check = |r: libc::c_int| {
        if r == 0 {
            Ok(())
        } else {
            Err(io::Error::last_os_error())
        }
    };
    let (mut cleared, mut record_err) = (false, None);
    for step in dir_steps(apply, a, record) {
        match step {
            // SAFETY: valid descriptor.
            DirStep::Owner(uid, gid) => check(unsafe { libc::fchown(raw, uid, gid) }).map_err(|e| {
                match e.raw_os_error() {
                    Some(libc::EPERM | libc::EINVAL) => io::Error::new(e.kind(), format!(
                        "cannot give it owner {uid}:{gid} ({e}): this process may not set owners here (a user \
                         namespace without those ids mapped, NFS root_squash); fetch with --no-owner")),
                    _ => e,
                }
            })?,
            // SAFETY: valid descriptor.
            DirStep::InterimMode(mode) => check(unsafe { libc::fchmod(raw, mode) })?,
            DirStep::Record(r) => record_err = write_end_record(fd, r, unsynced).err(),
            DirStep::FinalMode(mode) => {
                // SAFETY: valid descriptor.
                check(unsafe { libc::fchmod(raw, mode) }).map_err(|e| match e.raw_os_error() {
                    Some(libc::EPERM) => io::Error::new(e.kind(), format!(
                        "cannot give it mode {mode:o} ({e}): that needs CAP_FOWNER once it is someone else's")),
                    _ => e,
                })?;
                let setid = mode & (libc::S_ISUID | libc::S_ISGID);
                if setid != 0 {
                    #[cfg(test)]
                    tests::kernel_clears_setid(raw);
                    // SAFETY: `st` is written by fstat before it is read.
                    let mut st: libc::stat = unsafe { std::mem::zeroed() };
                    // SAFETY: valid descriptor and out pointer.
                    check(unsafe { libc::fstat(raw, &mut st) })?;
                    cleared = st.st_mode & setid != setid;
                }
            }
            DirStep::Mtime(sec, nsec) => {
                #[cfg(test)]
                if tests::FAIL_FINAL_MODE.with(|f| f.get()) {
                    return Err(io::Error::from_raw_os_error(libc::EIO));
                }
                let times = [libc::timespec { tv_sec: 0, tv_nsec: libc::UTIME_OMIT },
                             libc::timespec { tv_sec: sec, tv_nsec: i64::from(nsec) }];
                // SAFETY: valid descriptor and two timespecs.
                check(unsafe { libc::futimens(raw, times.as_ptr()) })?;
            }
        }
    }
    Ok((cleared, record_err))
}

/// Put a directory's start record back over the end record after its
/// attributes failed. If a new record cannot be renamed in, the existing
/// file is rewritten in place; if that fails too and the start record is
/// full authority, the record is removed (its absence says that); a lower
/// start record that can be neither written nor rewritten is logged as an
/// error (removing the file would claim full authority).
fn put_back(d: &str, fd: &OwnedFd, start: Authority, unsynced: &mut Vec<OwnedFd>) {
    #[cfg(test)]
    let failing = tests::FAIL_PUT_BACK.with(|f| f.get());
    #[cfg(not(test))]
    let failing = false;
    let renamed = if failing {
        Err(io::Error::other("put back (test)"))
    } else {
        content::record_authority_unsynced(fd, start)
    };
    let e = match renamed {
        Ok(f) => return unsynced.push(OwnedFd::from(f)),
        Err(e) => e,
    };
    let e2 = match content::rewrite_record_in_place(fd, start) {
        Ok(()) => return,
        Err(e2) => e2,
    };
    if start == Authority::FULL {
        let removed = dirfd::cstring(content::ATTRS_FILE.as_bytes())
            .and_then(|n| dirfd::unlink_at(fd, &n, 0));
        match removed {
            Ok(()) => return,
            Err(e) if e.raw_os_error() == Some(libc::ENOENT) => return,
            Err(e3) => tracing::error!(dir = d, error = %e3, "cannot remove the end record either"),
        }
    }
    tracing::error!(dir = d, rename = %e, in_place = %e2,
                    "cannot put back the start record of a directory whose attributes failed: \
                     its record may claim more than it has");
}

/// One worker's connections, by source (kept while the source is busy),
/// the one it uses now, and its busy backoff.
struct Worker<S> {
    conns: HashMap<String, Conn<S>>,
    active: Option<String>,
    backoff: Backoff,
    /// `Sources::dead_count` when this worker last dropped connections to
    /// dead sources.
    dead_seen: u64,
    /// Fetching into a device: this worker's slot and disk writer.
    pipe: Option<Pipe>,
}

impl<C: SourceSet> Shared<'_, C> {
    fn fail(&self, e: FetchError) {
        self.failed.store(true, Ordering::Relaxed);
        let mut slot = self.error.lock();
        if slot.is_none() {
            *slot = Some(e);
        }
    }

    fn run(&self, first: Option<Conn<C::Source>>, pipe: Option<Pipe>) {
        let mut wk = Worker {
            conns: HashMap::new(),
            active: None,
            backoff: Backoff::new(self.opts),
            dead_seen: 0,
            pipe,
        };
        if let Some(c) = first {
            wk.active = Some(c.view.name.clone());
            wk.conns.insert(c.view.name.clone(), c);
        }
        loop {
            if self.failed.load(Ordering::Relaxed) {
                return;
            }
            let i = self.next.fetch_add(1, Ordering::Relaxed);
            let Some(w) = self.work.get(i) else { return };
            if let Err(e) = self.range(&mut wk, w) {
                self.fail(e);
                return;
            }
        }
    }

    /// One work item: fill it, then count it (the file is renamed into
    /// place when its last range is counted).
    fn range(&self, wk: &mut Worker<C::Source>, w: &Work) -> Result<(), FetchError> {
        // At each range boundary: connections to sources marked dead
        // since (by any worker) are dropped, so nobody keeps reading from
        // a source the fetch gave up on.
        self.sources.prune(&mut wk.dead_seen, &mut wk.conns);
        if wk
            .active
            .as_ref()
            .is_some_and(|n| !wk.conns.contains_key(n))
        {
            wk.active = None;
        }
        self.fill(wk, w.target, w.offset, w.len)?;
        if self.failed.load(Ordering::Relaxed) {
            return Ok(());
        }
        self.sources.progress();
        self.ranges.fetch_add(1, Ordering::Relaxed);
        self.count(wk, w.target)
    }

    /// One range of `target` is written: the last one renames the file
    /// into place. Fetching into a device, the worker's disk writer counts
    /// it, after the range's queued bytes.
    fn count(&self, wk: &Worker<C::Source>, target: usize) -> Result<(), FetchError> {
        match &wk.pipe {
            Some(p) => p.send(Job::Count(target)),
            None => self.count_now(target),
        }
    }

    fn count_now(&self, target: usize) -> Result<(), FetchError> {
        let t = &self.targets[target];
        if t.remaining.fetch_sub(1, Ordering::AcqRel) == 1 {
            self.finalize(t)?;
        }
        Ok(())
    }

    /// Rename `target` into place once everything this worker wrote of
    /// it is on disk: its writer is waited for, then the rename is done
    /// here, as without a device (so a later round of the same refetch
    /// starts from a file that is really in place).
    fn finalize_after_writes(
        &self,
        wk: &Worker<C::Source>,
        target: usize,
    ) -> Result<(), FetchError> {
        if let Some(p) = &wk.pipe {
            p.flush()?;
            if self.failed.load(Ordering::Relaxed) {
                return Ok(());
            }
        }
        self.finalize(&self.targets[target])
    }

    /// A worker's disk writer: its jobs, in order, until the worker is
    /// gone, reading each range from the device into `buf`. After a
    /// failure it keeps taking jobs (doing none), so a worker waiting for
    /// a flush is answered.
    fn write_behind(&self, jobs: std::sync::mpsc::Receiver<Job>, mut buf: HostBuffer) {
        for job in jobs {
            let failed = self.failed.load(Ordering::Relaxed);
            let r = match job {
                Job::Write {
                    target,
                    at,
                    len,
                    epoch,
                } if !failed => self.write_placed(target, at, len, epoch, &mut buf),
                Job::Write { .. } => Ok(()),
                Job::Count(target) if !failed => self.count_now(target),
                Job::Count(_) => Ok(()),
                Job::Flush(done) => {
                    // The worker may have stopped waiting (it failed).
                    let _ = done.send(());
                    Ok(())
                }
            };
            if let Err(e) = r {
                self.fail(e);
            }
        }
    }

    /// A placed range onto the disk, read back from the device. Refused
    /// only when the file is being fetched again whole from a view newer
    /// than `epoch`, which rewrites it; not for a ban of its source since
    /// it was placed (bad data elsewhere does not make these checked bytes
    /// bad, and the range is no longer the worker's to fetch again).
    fn write_placed(
        &self,
        target: usize,
        at: u64,
        len: usize,
        epoch: u64,
        buf: &mut [u8],
    ) -> Result<(), FetchError> {
        let t = &self.targets[target];
        let dev = self.device.as_ref().ok_or_else(|| {
            FetchError::Device("invariant: a device write without a device".into())
        })?;
        if buf.len() < len {
            return Err(FetchError::Device(format!(
                "a {len}-byte range is larger than the host buffer"
            )));
        }
        let Some(_writing) = t.write_guard(epoch) else {
            return Ok(());
        };
        dev.device
            .read(dev.offsets[target] + at, &mut buf[..len])
            .map_err(FetchError::Device)?;
        #[cfg(test)]
        pause_write(&t.key);
        self.write_bytes(t, &buf[..len], at)
            .map_err(io_err(&self.dest_path.join(&t.rel)))
    }

    /// One range into the device: land it in the worker's slot, read it
    /// back into the worker's host buffer and check its CRC-32C (bad data
    /// is `GetError::Corrupt`, handled as for a host fetch), place those
    /// bytes at its file's offset, and queue its disk write. `Ok(Ok(0))`:
    /// refused, as [`Self::write`] refuses.
    #[allow(clippy::too_many_arguments)]
    fn land(
        &self,
        dev: &DevicePath<C::Source>,
        pipe: &Pipe,
        s: &mut C::Source,
        target: usize,
        at: u64,
        generation: u128,
        request: usize,
        room: usize,
        epoch: u64,
        lineage: u64,
    ) -> Result<Result<usize, FetchError>, GetError> {
        let t = &self.targets[target];
        let landed = (dev.land)(s, pipe.slot, &t.key, at, generation, request)?;
        let n = landed.read.len;
        if n == 0 {
            return Ok(Err(FetchError::Truncated {
                path: t.key.clone(),
                offset: at,
                size: t.size,
            }));
        }
        let mut buf = pipe.buf.borrow_mut();
        if buf.len() < n {
            return Ok(Err(FetchError::Device(format!(
                "a {n}-byte range is larger than the host buffer"
            ))));
        }
        let read = dev
            .device
            .make_visible()
            .and_then(|()| dev.device.read_slot(pipe.slot, &mut buf[..n]));
        if let Err(e) = read {
            return Ok(Err(FetchError::Device(e)));
        }
        landed.verify(&buf[..n])?;
        let n = n.min(room);
        Ok(self
            .place(dev, pipe, target, &buf[..n], at, epoch, lineage)
            .map(|placed| if placed { n } else { 0 }))
    }

    /// Place the checked `bytes` at `at` of `target` in the device, then
    /// queue the range's disk write. Under the same guard and ban check as
    /// [`Self::write`], so a range from a view the fetch has given up on
    /// is neither placed nor written (`Ok(false)`).
    #[allow(clippy::too_many_arguments)]
    fn place(
        &self,
        dev: &DevicePath<C::Source>,
        pipe: &Pipe,
        target: usize,
        bytes: &[u8],
        at: u64,
        epoch: u64,
        lineage: u64,
    ) -> Result<bool, FetchError> {
        let t = &self.targets[target];
        {
            let Some(_placing) = t.write_guard(epoch) else {
                return Ok(false);
            };
            t.writers.fetch_or(lineage_bit(lineage), Ordering::SeqCst);
            std::sync::atomic::fence(Ordering::SeqCst);
            if self.sources.banned.read().contains(&lineage) {
                return Ok(false);
            }
            dev.device
                .place(bytes, dev.offsets[target] + at)
                .map_err(FetchError::Device)?;
        }
        pipe.send(Job::Write {
            target,
            at,
            len: bytes.len(),
            epoch,
        })?;
        Ok(true)
    }

    /// Write `len` bytes of target `target` at `offset`, moving to another
    /// source when this one is spent, busy or inconsistent.
    fn fill(
        &self,
        wk: &mut Worker<C::Source>,
        target: usize,
        offset: u64,
        len: u64,
    ) -> Result<(), FetchError> {
        let t = &self.targets[target];
        let want = len as usize;
        let mut filled = 0usize;
        while filled < want {
            if self.failed.load(Ordering::Relaxed) {
                return Ok(());
            }
            let name = match wk.active.clone().filter(|n| wk.conns.contains_key(n)) {
                Some(n) => n,
                None => match self.sources.attach(self.targets)? {
                    Attached::Conn(c) => {
                        let n = c.view.name.clone();
                        wk.conns.insert(n.clone(), c);
                        wk.active = Some(n.clone());
                        n
                    }
                    Attached::Busy(n) => {
                        self.on_busy(wk, &n)?;
                        continue;
                    }
                },
            };
            let c = wk
                .conns
                .get_mut(&name)
                .expect("invariant: the active name is a held connection");
            let at = offset + filled as u64;
            let (epoch, lineage) = (c.view.epoch, c.view.lineage);
            let generation = c.view.generations[target];
            if generation == 0 {
                // This view's source does not serve this (finished) file
                // with the same content: it cannot refetch it. Another
                // one may; none left ends the fetch as a mismatch.
                let why = format!("{}: not served with the same size and content", t.key);
                if let Some(c) = wk.conns.remove(&name) {
                    self.sources.retire_as(&c.view, Dead::Mismatch(why));
                }
                wk.active = None;
                continue;
            }
            // Ask for whole blocks, so the source can read the file tail
            // with O_DIRECT too; it returns only the bytes the file has.
            let request = (want - filled).div_ceil(ALIGN) * ALIGN;
            let request = request.min(c.src.max_range());
            let got = match (&self.device, &wk.pipe) {
                (Some(dev), Some(pipe)) => {
                    let request = request.min(dev.slot_len);
                    with_retries(&mut c.src, self.opts, self.retries, self.reconnects, |s| {
                        self.land(
                            dev,
                            pipe,
                            s,
                            target,
                            at,
                            generation,
                            request,
                            want - filled,
                            epoch,
                            lineage,
                        )
                    })
                }
                _ => with_retries(&mut c.src, self.opts, self.retries, self.reconnects, |s| {
                    s.fetch(&t.key, at, generation, request).map(|bytes| {
                        if bytes.is_empty() {
                            return Err(FetchError::Truncated {
                                path: t.key.clone(),
                                offset: at,
                                size: t.size,
                            });
                        }
                        let n = bytes.len().min(want - filled);
                        // 0: refused (the view is older than the file's
                        // refetch, or it was retired for bad data).
                        Ok(if self.write(t, &bytes[..n], at, epoch, lineage)? {
                            n
                        } else {
                            0
                        })
                    })
                }),
            };
            match got {
                // Refused: this range goes on from a newer view. A view of a
                // server that served bad data (another of its names, say)
                // is retired as such, so it is never handed out again.
                Ok(0) => {
                    if let Some(c) = wk.conns.remove(&name) {
                        if !self.sources.retire_banned(&c.view) {
                            std::thread::sleep(
                                self.opts.busy_backoff.max(Duration::from_millis(1)),
                            );
                        }
                    }
                    wk.active = None;
                }
                Ok(n) => {
                    filled += n;
                    wk.backoff.reset();
                    self.sources.served(&name);
                }
                // Busy is load, not a broken connection: keep it (and the
                // source's place), back off and ask again, or move this
                // range to another eligible source meanwhile.
                Err(Fail::Get(GetError::Busy)) => self.on_busy(wk, &name)?,
                // This source is spent (its retries and reconnects are
                // used up), or it served bad data: go on from another one.
                // The bytes written so far stay unless it served bad data.
                Err(
                    f @ (Fail::Connect(_)
                    | Fail::Get(GetError::Transport(_) | GetError::Corrupt { .. })
                    | Fail::Local(FetchError::Truncated { .. })),
                ) => {
                    let why = |e: &dyn std::fmt::Display| format!("{} at {at}: {e}", t.key);
                    let truncated = matches!(f, Fail::Local(FetchError::Truncated { .. }));
                    let dead = match &f {
                        Fail::Get(e @ GetError::Corrupt { .. }) => Dead::Corrupt(why(e)),
                        Fail::Local(e @ FetchError::Truncated { .. }) => Dead::Corrupt(why(e)),
                        Fail::Get(e) => Dead::Failed(why(e)),
                        Fail::Connect(e) => Dead::Failed(why(e)),
                        Fail::Local(e) => Dead::Failed(why(e)),
                    };
                    let bad = wk.conns.get(&name).map(|c| c.view.lineage);
                    if let Some(c) = wk.conns.remove(&name) {
                        // Bad data: banned, recorded, never revived.
                        self.sources.retire_as(&c.view, dead);
                    }
                    wk.active = None;
                    if truncated {
                        // A source whose file is shorter than its own
                        // manifest says served inconsistently: every file
                        // it wrote any bytes of, this one included, is
                        // fetched again whole from views newer than any
                        // that existed when it was retired, each by the
                        // one worker that claims it.
                        let bar = bad.map_or_else(
                            || self.sources.bar_after(u64::MAX),
                            |l| self.sources.bar_after(l),
                        );
                        self.refetch_written_by(wk, bad, bar, target)
                            .map_err(|e| *e)?;
                        if t.refetch_from(bar) {
                            // Its own range is not counted yet: it cannot
                            // finish under us. Again whole if another bad
                            // source shows up meanwhile.
                            loop {
                                let seen = t.min_epoch.load(Ordering::Acquire);
                                self.fill(wk, target, 0, t.size)?;
                                if t.min_epoch.load(Ordering::Acquire) == seen {
                                    return Ok(());
                                }
                            }
                        }
                        // Another worker refetches the whole file: finish
                        // this range from the new source.
                    }
                }
                // The file changed on this source: if another source is
                // left, this one is out and the range goes on there (its
                // content identity is checked when it is validated);
                // otherwise the fetch fails as Changed.
                Err(Fail::Get(GetError::GenerationChanged)) => {
                    let Some(c) = wk.conns.remove(&name) else {
                        return Err(FetchError::Changed {
                            path: t.key.clone(),
                        });
                    };
                    if !self.sources.changed(&c.view, &t.key) {
                        return Err(FetchError::Changed {
                            path: t.key.clone(),
                        });
                    }
                    wk.active = None;
                }
                Err(Fail::Get(error)) => {
                    return Err(FetchError::Get {
                        path: t.key.clone(),
                        offset: at,
                        error,
                    })
                }
                Err(Fail::Local(e)) => return Err(e),
            }
        }
        Ok(())
    }

    /// Fetch again, whole, from the source now current (view `next_epoch`)
    /// every file other than `current` that lineage `bad` wrote to, each by
    /// the worker that claims it ([`Target::refetch_from`]); later writes
    /// from older views to those files are refused. A file still in
    /// progress is held open (its range count raised) while it is
    /// refetched; a finished one is fetched again beside itself and
    /// replaces itself.
    /// (The error is boxed: `FetchError` is large.)
    fn refetch_written_by(
        &self,
        wk: &mut Worker<C::Source>,
        bad: Option<u64>,
        next_epoch: u64,
        current: usize,
    ) -> Result<(), Box<FetchError>> {
        let bit = bad.map_or(0, lineage_bit);
        for (i, t) in self.targets.iter().enumerate() {
            if i == current
                || t.writers.load(Ordering::SeqCst) & bit == 0
                || !t.refetch_from(next_epoch)
            {
                continue;
            }
            // Pairs with the fence after the holder lets go (below): a
            // store-buffering hand-off (the raise, then a look at the lock;
            // the unlock, then a look at the bar) needs a SeqCst fence on
            // each side, or on aarch64 both looks can see the old value.
            std::sync::atomic::fence(Ordering::SeqCst);
            // One scanner refetches a file at a time. A claimer that
            // finds it busy leaves it to the holder, which re-checks the
            // bar after letting go (below), so a raise the holder missed
            // is never lost.
            loop {
                let Some(refetching) = t.refetching.try_lock() else {
                    break;
                };
                warn!(file = %t.key, "fetching the whole file again from another source");
                let mut bar;
                loop {
                    bar = t.min_epoch.load(Ordering::SeqCst);
                    // Each round: hold the file open if it is still in
                    // progress (its last range may be this worker's own,
                    // further up the stack, so never wait for it to finish
                    // when it can be held).
                    let held = t
                        .remaining
                        .fetch_update(Ordering::AcqRel, Ordering::Acquire, |r| {
                            (r > 0).then_some(r + 1)
                        })
                        .is_ok();
                    if held {
                        self.fill(wk, i, 0, t.size)?;
                        self.count(wk, i)?;
                    } else {
                        // Finished, or being renamed into place right now.
                        while !t.done.load(Ordering::Acquire) {
                            if self.failed.load(Ordering::Relaxed) {
                                return Ok(());
                            }
                            std::thread::sleep(std::time::Duration::from_millis(1));
                        }
                        let path = self.dest_path.join(&t.rel);
                        // Private until finalize gives it its attributes,
                        // as when it was first staged.
                        let file = self
                            .staging
                            .create_file(&t.staging, 0o600)
                            .map_err(io_err(&path))?;
                        preallocate(&file, t.size).map_err(io_err(&path))?;
                        drop(file);
                        self.fill(wk, i, 0, t.size)?;
                        self.finalize_after_writes(wk, i)?;
                    }
                    // A later bad source raised the bar while this ran:
                    // its bytes may be in there, so again.
                    if t.min_epoch.load(Ordering::SeqCst) == bar {
                        break;
                    }
                }
                #[cfg(test)]
                pause_before_unlock(&t.key);
                drop(refetching);
                std::sync::atomic::fence(Ordering::SeqCst);
                // A raise between the last check and letting go was left
                // to us by a claimer whose try_lock failed: take it again.
                if t.min_epoch.load(Ordering::SeqCst) == bar {
                    break;
                }
            }
        }
        Ok(())
    }

    /// `name` answered busy to this worker. Cool it (nothing is released),
    /// then move the worker to another eligible source if there is one,
    /// or pause (decorrelated jitter, per worker) and ask `name` again.
    /// Ends the fetch once no range has completed for `opts.wait`.
    #[allow(clippy::result_large_err)] // FetchError is large module-wide (see fetch_manifest_within)
    fn on_busy(&self, wk: &mut Worker<C::Source>, name: &str) -> Result<(), FetchError> {
        self.sources.cool(name);
        // Another eligible source first: an idle one takes the range even
        // when no wait is allowed (`--wait-s 0`).
        match self.sources.alternate(self.targets, name, &wk.conns) {
            Some(Alt::Held(n)) => wk.active = Some(n),
            Some(Alt::New(c)) => {
                let n = c.view.name.clone();
                wk.conns.insert(n.clone(), c);
                wk.active = Some(n);
            }
            None => {
                let left = self.sources.stall_left();
                if left.is_some_and(|d| d.is_zero()) {
                    return Err(self.sources.stall_error());
                }
                if self.sources.is_dead(name) {
                    // Not back to a source the fetch gave up on.
                    wk.conns.remove(name);
                }
                wk.active = Some(name.to_string()).filter(|n| wk.conns.contains_key(n));
                std::thread::sleep(wk.backoff.next().min(left.unwrap_or(Duration::MAX)));
            }
        }
        Ok(())
    }

    /// Write `bytes` at `at`, unless the file is being refetched from a
    /// view newer than `epoch` (then `Ok(false)`).
    /// Writes served by a view retired for bad data are refused too. The
    /// view is marked as a writer of the file before the check and the
    /// bytes: a scan after the view is banned either sees the mark (and
    /// refetches, waiting for this write) or this write sees the ban.
    fn write(
        &self,
        t: &Target,
        bytes: &[u8],
        at: u64,
        epoch: u64,
        lineage: u64,
    ) -> Result<bool, FetchError> {
        let Some(_writing) = t.write_guard(epoch) else {
            return Ok(false);
        };
        t.writers.fetch_or(lineage_bit(lineage), Ordering::SeqCst);
        std::sync::atomic::fence(Ordering::SeqCst);
        if self.sources.banned.read().contains(&lineage) {
            return Ok(false);
        }
        #[cfg(test)]
        pause_write(&t.key);
        self.write_bytes(t, bytes, at)
            .map_err(io_err(&self.dest_path.join(&t.rel)))?;
        Ok(true)
    }

    fn write_bytes(&self, t: &Target, bytes: &[u8], at: u64) -> io::Result<()> {
        let aligned = at.is_multiple_of(ALIGN as u64)
            && bytes.len().is_multiple_of(ALIGN)
            && (bytes.as_ptr() as usize).is_multiple_of(ALIGN);
        if aligned && self.direct.load(Ordering::Relaxed) {
            match self
                .staging
                .open(&t.staging, libc::O_WRONLY | libc::O_DIRECT)
            {
                Ok(f) => return f.write_all_at(bytes, at),
                Err(e) if e.raw_os_error() == Some(libc::EINVAL) => {
                    self.direct.store(false, Ordering::Relaxed);
                }
                Err(e) => return Err(e),
            }
        }
        self.staging
            .open(&t.staging, libc::O_WRONLY)?
            .write_all_at(bytes, at)
    }

    /// Durable and visible under the final name. `renameat` replaces
    /// whatever is at the name (a symlink included) without following it.
    /// Also used to replace a finished file fetched again: its later
    /// record is the one `content::record` keeps (entries merge by path,
    /// the last one wins).
    fn finalize(&self, t: &Target) -> Result<(), FetchError> {
        let path = self.dest_path.join(&t.rel);
        let f = self
            .staging
            .open(&t.staging, libc::O_WRONLY)
            .map_err(io_err(&path))?;
        if self.apply.on() {
            // On the descriptor, before the name exists: the file never
            // appears in place with other attributes. fsync, not
            // fdatasync, so the new mode and mtime are as durable as the
            // bytes.
            if self
                .apply
                .to_fd(&f, &t.attrs, false)
                .map_err(io_err(&path))?
                && !t.setid_counted.swap(true, Ordering::AcqRel)
            {
                self.setid_cleared.fetch_add(1, Ordering::Relaxed);
            }
            f.sync_all().map_err(io_err(&path))?;
        } else {
            f.sync_data().map_err(io_err(&path))?;
        }
        let (parent, name) = t.split();
        let parent_fd = dirfd::open_dir_beneath(self.dest, parent).map_err(io_err(&path))?;
        let name = dirfd::cstring(name.as_bytes()).map_err(io_err(&path))?;
        dirfd::rename_at(&self.staging.dir, &t.staging, &parent_fd, &name)
            .map_err(io_err(&path))?;
        // The generation after the rename (which changes ctime), from the
        // descriptor, so it is this file's whatever happens to the name.
        // Recorded before the file counts as done.
        let st = dirfd::fstat(&OwnedFd::from(f)).map_err(io_err(&path))?;
        self.recorded.lock().push(Entry {
            path: t.rel.clone(),
            size: t.size,
            generation: crate::export::generation_of(&st),
            content: t.content,
            ..Entry::default()
        });
        t.done.store(true, Ordering::Release);
        Ok(())
    }
}

/// Reserve the blocks so ENOSPC shows before any transfer; filesystems
/// without fallocate get a sparse `set_len`.
fn preallocate(file: &File, size: u64) -> io::Result<()> {
    if size == 0 {
        return Ok(());
    }
    let len = libc::off_t::try_from(size)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "file too large for off_t"))?;
    // SAFETY: valid open fd, mode 0 (allocate + extend), in-range length.
    if unsafe { libc::fallocate(file.as_raw_fd(), 0, 0, len) } == 0 {
        return Ok(());
    }
    let err = io::Error::last_os_error();
    match err.raw_os_error() {
        Some(libc::EOPNOTSUPP) | Some(libc::ENOSYS) => file.set_len(size),
        _ => Err(err),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::export::DirExport;
    use std::sync::Arc;

    /// Longest key the in-process source accepts, like the RDMA wire
    /// codec (an over-long key fails to encode: a transport error).
    const TEST_KEY_LEN: usize = 192;

    /// In-process stand-in for an RDMA source: reads through `DirExport`
    /// into a 4 KiB-aligned buffer with the same generation pinning the
    /// server applies, and returns scripted outcomes first
    /// (`None` passes that fetch through).
    struct Local {
        export: Arc<DirExport>,
        buf: Vec<u8>,
        off: usize,
        max: usize,
        script: Arc<Mutex<Vec<Option<GetError>>>>,
        /// Reconnects that fail before one succeeds.
        reconnect_failures: Arc<Mutex<u32>>,
    }

    impl RangeSource for Local {
        fn max_range(&self) -> usize {
            self.max
        }
        fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
            if key.len() > TEST_KEY_LEN {
                return Err(GetError::Transport("key longer than MAX_KEY_LEN".into()));
            }
            let r = self
                .export
                .read(key, 0, &mut [])
                .map_err(|_| GetError::NotFound)?;
            Ok((r.size, r.generation))
        }
        fn fetch(
            &mut self,
            key: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            if let Some(Some(e)) = {
                let mut s = self.script.lock();
                (!s.is_empty()).then(|| s.remove(0))
            } {
                return Err(e);
            }
            if key.len() > TEST_KEY_LEN {
                return Err(GetError::Transport("key longer than MAX_KEY_LEN".into()));
            }
            let len = len.min(self.max);
            let buf = &mut self.buf[self.off..self.off + len];
            let r = self
                .export
                .read(key, offset, buf)
                .map_err(|_| GetError::NotFound)?;
            if r.generation != generation {
                return Err(GetError::GenerationChanged);
            }
            Ok(&self.buf[self.off..self.off + r.len])
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            let mut left = self.reconnect_failures.lock();
            if *left > 0 {
                *left -= 1;
                return Err(HydrateError::Connect("rail down".into()));
            }
            Ok(())
        }
    }

    fn source(
        export: &Arc<DirExport>,
        max: usize,
        script: Vec<GetError>,
    ) -> impl Fn() -> Result<Local, HydrateError> + Sync {
        source_with(export, max, script.into_iter().map(Some).collect(), 0)
    }

    fn source_with(
        export: &Arc<DirExport>,
        max: usize,
        script: Vec<Option<GetError>>,
        reconnect_failures: u32,
    ) -> impl Fn() -> Result<Local, HydrateError> + Sync {
        let export = Arc::clone(export);
        let script = Arc::new(Mutex::new(script));
        let reconnect_failures = Arc::new(Mutex::new(reconnect_failures));
        move || {
            let buf = vec![0u8; max + ALIGN];
            let off = (ALIGN - buf.as_ptr() as usize % ALIGN) % ALIGN;
            Ok(Local {
                export: Arc::clone(&export),
                buf,
                off,
                max,
                script: Arc::clone(&script),
                reconnect_failures: Arc::clone(&reconnect_failures),
            })
        }
    }

    fn opts(connections: usize, range: usize) -> FetchOptions {
        FetchOptions {
            connections,
            range_size: range,
            max_retries: 3,
            retry_backoff: Duration::from_millis(1),
            direct_io: true,
            max_key_len: TEST_KEY_LEN,
            busy_backoff: Duration::from_millis(1),
            max_busy_backoff: Duration::from_millis(4),
            // Finite, so a regression that never gets a source
            // back fails a test instead of hanging the run.
            wait: Duration::from_secs(20),
            ..FetchOptions::default()
        }
    }

    /// A checkpoint-shaped tree: many small files, a few multi-range
    /// files with unaligned tails, an empty file, nested directories.
    fn source_tree() -> (tempfile::TempDir, Arc<DirExport>) {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let r = d.path().join("export");
        for i in 0..40 {
            let p = r.join(format!("ckpt/criu/img-{i}.img"));
            std::fs::create_dir_all(p.parent().expect("parent")).expect("mkdir");
            std::fs::write(&p, vec![i as u8; 100 + i * 37]).expect("write");
        }
        for (name, len) in [
            ("a.bin", 5 * 65536 + 4096),
            ("b.bin", 3 * 65536 + 123),
            ("c.bin", 65536),
        ] {
            let data: Vec<u8> = (0..len).map(|i| (i * 7 % 253) as u8).collect();
            std::fs::create_dir_all(r.join("ckpt/chunks")).expect("mkdir");
            std::fs::write(r.join("ckpt/chunks").join(name), data).expect("write");
        }
        std::fs::write(r.join("ckpt/empty"), b"").expect("write");
        std::fs::write(r.join("other.txt"), b"not under ckpt").expect("write");
        let e = DirExport::open(&r).expect("export");
        (d, e)
    }

    fn assert_same_tree(src: &Path, dst: &Path) {
        let mut a = Vec::new();
        walk(src, src, &mut a);
        let mut b = Vec::new();
        walk(dst, dst, &mut b);
        assert_eq!(
            a.iter().map(|x| &x.0).collect::<Vec<_>>(),
            b.iter().map(|x| &x.0).collect::<Vec<_>>()
        );
        assert_eq!(a, b);
        assert_no_staging(dst);
    }

    fn assert_no_staging(dst: &Path) {
        let Ok(items) = std::fs::read_dir(dst) else {
            return;
        };
        let left: Vec<_> = items
            .map(|e| e.expect("entry").file_name())
            .filter(|n| n.to_string_lossy().starts_with(STAGING_PREFIX))
            .collect();
        assert!(
            left.is_empty(),
            "staging left in {}: {left:?}",
            dst.display()
        );
    }

    /// Every file under `dir` but nvstor-peer's own (the content record).
    fn walk(root: &Path, dir: &Path, out: &mut Vec<(PathBuf, Vec<u8>)>) {
        let mut items: Vec<_> = std::fs::read_dir(dir)
            .expect("read_dir")
            .map(|e| e.expect("entry").path())
            .filter(|p| {
                !p.file_name()
                    .is_some_and(|n| n.to_string_lossy().starts_with(RESERVED_PREFIX))
            })
            .collect();
        items.sort();
        for p in items {
            if p.is_dir() {
                walk(root, &p, out);
            } else {
                out.push((
                    p.strip_prefix(root).expect("rel").to_path_buf(),
                    std::fs::read(&p).expect("read"),
                ));
            }
        }
    }

    #[test]
    fn copies_a_tree_with_many_files_and_multi_range_files() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let r =
            fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(6, 65536)).expect("fetch");
        assert_same_tree(&d.path().join("export/ckpt"), &dest);
        assert_eq!(r.files, 44);
        // 40 small + a: 6 ranges, b: 4, c: 1; the empty file has none.
        assert_eq!(r.ranges, 40 + 6 + 4 + 1);
        assert_eq!((r.retries, r.reconnects), (0, 0));
        assert!(!dest.join("../other.txt").exists());
    }

    #[test]
    fn whole_export_and_a_source_buffer_smaller_than_the_range() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        // 16 KiB source buffer, 64 KiB ranges: every range takes 4 fetches.
        fetch_dir(source(&e, 16384, vec![]), "", &dest, &opts(3, 65536)).expect("fetch");
        assert_same_tree(&d.path().join("export"), &dest);
        // A second fetch over a complete copy replaces it in place.
        fetch_dir(source(&e, 16384, vec![]), "", &dest, &opts(3, 65536)).expect("refetch");
        assert_same_tree(&d.path().join("export"), &dest);
    }

    #[test]
    fn retries_and_reconnects_then_succeeds() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let script = vec![
            GetError::Busy,
            GetError::Transport("reset".into()),
            GetError::Corrupt {
                expected: 1,
                actual: 2,
            },
        ];
        let r = fetch_dir(
            source(&e, 65536, script),
            "ckpt/chunks",
            &dest,
            &opts(1, 65536),
        )
        .expect("fetch");
        // The busy answer is not a retry.
        assert_eq!((r.retries, r.reconnects), (2, 1));
        assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
    }

    // A busy source is never a failed one. Far more
    // busy answers than `max_retries` (3) neither end the fetch nor drop
    // the only source, on the manifest or on a range; only `wait` bounds
    // them, and then the fetch ends Unavailable.
    #[test]
    fn busy_answers_spend_no_retries_and_never_drop_the_source() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            for (n, script) in [
                vec![Some(GetError::Busy); 20],
                std::iter::once(None)
                    .chain(std::iter::repeat_n(Some(GetError::Busy), 20))
                    .collect(),
            ]
            .into_iter()
            .enumerate()
            {
                let dest = d.path().join(format!("dest{n}"));
                let r = fetch_dir(
                    source_with(&e, 65536, script, 0),
                    "ckpt/chunks",
                    &dest,
                    &opts(1, 65536),
                )
                .expect("busy is waited out, not counted");
                assert_eq!(
                    (r.retries, r.reconnects, r.failovers),
                    (0, 0, 0),
                    "case {n}"
                );
                assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
            }
            // Busy for longer than `wait`: Unavailable, not a dead source.
            let mut o = opts(1, 65536);
            o.wait = Duration::from_millis(100);
            for (n, script) in [
                vec![Some(GetError::Busy); 100_000],
                std::iter::once(None)
                    .chain(std::iter::repeat_n(Some(GetError::Busy), 100_000))
                    .collect(),
            ]
            .into_iter()
            .enumerate()
            {
                let started = Instant::now();
                let err = fetch_dir(
                    source_with(&e, 65536, script, 0),
                    "ckpt/chunks",
                    &d.path().join(format!("never{n}")),
                    &o,
                )
                .expect_err("busy past wait");
                assert!(
                    matches!(err, FetchError::Unavailable(ref m) if m.contains("busy")),
                    "case {n}: {err}"
                );
                assert!(started.elapsed() < Duration::from_secs(5));
            }
        });
    }

    #[test]
    fn a_failed_reconnect_uses_the_retry_budget_instead_of_ending_the_fetch() {
        // One failed reconnect aborted the fetch with retries left.
        let (d, e) = source_tree();
        let flap = || Some(GetError::Transport("link flap".into()));
        // The first fetch is the manifest's, the next ones are ranges.
        for (n, script) in [vec![flap()], vec![None, None, flap()]]
            .into_iter()
            .enumerate()
        {
            let dest = d.path().join(format!("dest{n}"));
            let src = source_with(&e, 65536, script, 2);
            let r = fetch_dir(src, "ckpt/chunks", &dest, &opts(1, 65536)).expect("fetch");
            // The error, then two failed reconnects: three attempts, one
            // reconnect that worked.
            assert_eq!((r.retries, r.reconnects), (3, 1), "case {n}");
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
        }

        // A rail that stays down still ends the fetch once the budget is
        // spent, and leaves nothing behind.
        let dest = d.path().join("down");
        let src = source_with(&e, 65536, vec![None, None, flap()], u32::MAX);
        let err =
            fetch_dir(src, "ckpt/chunks", &dest, &opts(1, 65536)).expect_err("rail stays down");
        assert!(matches!(err, FetchError::Connect(_)), "{err}");
        let mut files = Vec::new();
        walk(&dest, &dest, &mut files);
        // Ranges are in file order: a.bin was not finished, so nothing
        // was renamed into place.
        assert!(files.is_empty(), "{files:?}");
        assert_no_staging(&dest);
    }

    #[test]
    fn a_file_changed_mid_fetch_fails_and_leaves_no_partials() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let src = source(&e, 65536, vec![]);
        let manifest = {
            let mut s = src().expect("src");
            fetch_manifest(
                &mut s,
                "ckpt/chunks",
                &opts(1, 65536),
                &AtomicU64::new(0),
                &AtomicU64::new(0),
            )
            .expect("m")
        };
        assert_eq!(manifest.files().count(), 3);
        std::thread::sleep(Duration::from_millis(10));
        let changing = d.path().join("export/ckpt/chunks/b.bin");
        let wrapped = || {
            let s = src()?;
            // Rewrite b.bin after the manifest is read: its generation in
            // the manifest no longer matches.
            Ok(Rewriter {
                inner: s,
                path: changing.clone(),
                done: false,
            })
        };
        let err = fetch_dir(wrapped, "ckpt/chunks", &dest, &opts(2, 65536)).expect_err("must fail");
        assert!(
            matches!(err, FetchError::Changed { ref path } if path == "ckpt/chunks/b.bin"),
            "{err}"
        );
        let mut left = Vec::new();
        walk(&dest, &dest, &mut left);
        assert!(
            left.iter().all(|(p, _)| p != Path::new("b.bin")),
            "{left:?}"
        );
        assert_no_staging(&dest);
    }

    /// Rewrites one file right after the manifest has been fetched.
    struct Rewriter {
        inner: Local,
        path: PathBuf,
        done: bool,
    }
    impl RangeSource for Rewriter {
        fn max_range(&self) -> usize {
            self.inner.max_range()
        }
        fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
            self.inner.stat(key)
        }
        fn fetch(
            &mut self,
            key: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            if !self.done && manifest::prefix_of(key).is_none() {
                self.done = true;
                std::fs::write(&self.path, b"rewritten").expect("rewrite");
            }
            self.inner.fetch(key, offset, generation, len)
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            Ok(())
        }
    }

    /// Calls `before` ahead of every data fetch (not the manifest's).
    struct Hooked<F> {
        inner: Local,
        before: Arc<F>,
    }
    impl<F: Fn(&str, u64) + Send + Sync> RangeSource for Hooked<F> {
        fn max_range(&self) -> usize {
            self.inner.max_range()
        }
        fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
            self.inner.stat(key)
        }
        fn fetch(
            &mut self,
            key: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            if manifest::prefix_of(key).is_none() {
                (self.before)(key, offset);
            }
            self.inner.fetch(key, offset, generation, len)
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            Ok(())
        }
    }

    fn hooked<F: Fn(&str, u64) + Send + Sync>(
        export: &Arc<DirExport>,
        max: usize,
        before: F,
    ) -> impl Fn() -> Result<Hooked<F>, HydrateError> + Sync {
        let inner = source(export, max, vec![]);
        let before = Arc::new(before);
        move || {
            Ok(Hooked {
                inner: inner()?,
                before: Arc::clone(&before),
            })
        }
    }

    #[test]
    fn entries_named_like_staging_files_do_not_collide() {
        // `m` staged in `m.partial`, which was also an entry; the
        // other target's finalize swapped the inode under `m` and a mix
        // of both files was renamed into place as `m`.
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let r = d.path().join("export");
        std::fs::create_dir_all(r.join("sub")).expect("mkdir");
        std::fs::write(r.join("m"), vec![0xAA; 4 * 65536]).expect("write");
        std::fs::write(r.join("m.partial"), vec![0xBB; 3 * 65536]).expect("write");
        std::fs::write(r.join("m.partial.partial"), vec![0xCC; 100]).expect("write");
        std::fs::write(r.join("sub/x"), vec![0xDD; 2 * 65536]).expect("write");
        std::fs::write(r.join("sub/x.partial"), vec![0xEE; 10]).expect("write");
        let e = DirExport::open(&r).expect("export");
        let dest = d.path().join("dest");
        // Slow down `m`'s later ranges so the other files finish first.
        let src = hooked(&e, 65536, |key, offset| {
            if (key == "m" || key == "sub/x") && offset > 0 {
                std::thread::sleep(Duration::from_millis(30));
            }
        });
        let report = fetch_dir(src, "", &dest, &opts(4, 65536)).expect("fetch");
        assert_eq!(report.files, 5);
        assert_same_tree(&r, &dest);
    }

    #[test]
    fn a_second_fetch_into_the_same_dest_fails_fast() {
        // A second fetch truncated the first one's staging file and
        // the first renamed a file with zeroed ranges into place.
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let (entered_tx, entered_rx) = std::sync::mpsc::channel::<()>();
        let (release_tx, release_rx) = std::sync::mpsc::channel::<()>();
        let entered = Mutex::new(Some(entered_tx));
        let release = Mutex::new(release_rx);
        // The first data fetch of fetch A waits until B has run.
        let src = hooked(&e, 65536, move |_, _| {
            if let Some(tx) = entered.lock().take() {
                let _ = tx.send(());
                let _ = release.lock().recv();
            }
        });
        std::thread::scope(|s| {
            let a = s.spawn(|| fetch_dir(src, "ckpt", &dest, &opts(1, 65536)));
            entered_rx.recv().expect("fetch A started");
            let b = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(4, 65536));
            assert!(matches!(b, Err(FetchError::Locked { .. })), "{b:?}");
            release_tx.send(()).expect("release");
            a.join().expect("join").expect("fetch A");
        });
        assert_same_tree(&d.path().join("export/ckpt"), &dest);
    }

    #[test]
    fn a_crashed_fetchs_staging_directory_is_removed() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let stale = dest.join(format!("{STAGING_PREFIX}0123456789abcdef"));
        std::fs::create_dir_all(&stale).expect("mkdir");
        std::fs::write(stale.join("0"), vec![1u8; 4096]).expect("write");
        fetch_dir(
            source(&e, 65536, vec![]),
            "ckpt/chunks",
            &dest,
            &opts(2, 65536),
        )
        .expect("fetch");
        assert!(!stale.exists());
        assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
    }

    #[test]
    fn errors_while_preparing_leave_no_staging_files() {
        // An error in prepare() left every .partial created so far,
        // each fully preallocated.
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        // other.txt is the last entry: every other file is staged first.
        std::fs::create_dir_all(dest.join("other.txt")).expect("mkdir");
        let err = fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(2, 65536))
            .expect_err("must fail");
        assert!(
            matches!(err, FetchError::Io { ref path, .. } if path.ends_with("other.txt")),
            "{err}"
        );
        let mut files = Vec::new();
        walk(&dest, &dest, &mut files);
        assert!(files.is_empty(), "{files:?}");
        assert_no_staging(&dest);
    }

    #[test]
    fn symlinks_and_hardlinks_in_dest_are_never_written_through() {
        // Create+truncate and rename by path followed symlinks
        // already in dest and wrote outside it.
        let (d, e) = source_tree();
        let outside = d.path().join("outside");
        std::fs::create_dir_all(&outside).expect("mkdir");
        std::fs::write(outside.join("victim"), b"keep me").expect("write");

        // A symlinked directory where the tree needs a directory.
        let dest = d.path().join("dest1");
        std::fs::create_dir_all(&dest).expect("mkdir");
        std::os::unix::fs::symlink(&outside, dest.join("ckpt")).expect("symlink");
        let err = fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(2, 65536))
            .expect_err("symlinked dir");
        assert!(matches!(err, FetchError::Io { .. }), "{err}");
        let names: Vec<_> = std::fs::read_dir(&outside)
            .expect("ls")
            .map(|x| x.expect("e").file_name())
            .collect();
        assert_eq!(names, ["victim"]);
        assert_no_staging(&dest);

        // A symlink where a file goes.
        let dest = d.path().join("dest2");
        std::fs::create_dir_all(&dest).expect("mkdir");
        std::os::unix::fs::symlink(outside.join("victim"), dest.join("other.txt"))
            .expect("symlink");
        let err = fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(2, 65536))
            .expect_err("symlinked file");
        assert!(
            matches!(err, FetchError::Io { ref path, .. } if path.ends_with("other.txt")),
            "{err}"
        );
        assert_no_staging(&dest);

        // A hardlink where a file goes: the name is replaced, the other
        // link keeps its bytes.
        let dest = d.path().join("dest3");
        std::fs::create_dir_all(&dest).expect("mkdir");
        std::fs::hard_link(outside.join("victim"), dest.join("other.txt")).expect("link");
        fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(2, 65536)).expect("hardlink");
        assert_same_tree(&d.path().join("export"), &dest);
        assert_eq!(
            std::fs::read(outside.join("victim")).expect("read"),
            b"keep me"
        );
    }

    #[test]
    fn paths_too_long_for_the_transport_fail_per_file() {
        // One path over the key limit failed the whole fetch.
        let (d, e) = source_tree();
        let long_dir = format!("ckpt/{}", "d".repeat(100));
        let long = format!("{long_dir}/{}", "f".repeat(100));
        std::fs::create_dir_all(d.path().join("export").join(&long_dir)).expect("mkdir");
        std::fs::write(d.path().join("export").join(&long), b"unreachable").expect("write");
        let dest = d.path().join("dest");
        let err = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(4, 65536))
            .expect_err("incomplete");
        let FetchError::Incomplete { report, failed } = &err else {
            panic!("{err}")
        };
        assert_eq!(report.files, 44);
        assert_eq!(failed.len(), 1);
        assert_eq!(failed[0].0, long);
        assert!(err.to_string().contains(&long), "{err}");
        // Everything else is there; the long file and its directory are not.
        std::fs::remove_dir_all(d.path().join("export").join(&long_dir)).expect("rm");
        assert_same_tree(&d.path().join("export/ckpt"), &dest);

        // A prefix whose manifest key cannot be sent is refused up front.
        let prefix = "p".repeat(TEST_KEY_LEN);
        assert!(matches!(
            fetch_dir(source(&e, 65536, vec![]), &prefix, &dest, &opts(1, 65536)),
            Err(FetchError::Invalid(_))
        ));
    }

    #[test]
    fn reserved_names_are_neither_served_nor_created() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let r = d.path().join("export");
        std::fs::create_dir_all(&r).expect("mkdir");
        std::fs::write(r.join("keep"), b"k").expect("write");
        std::fs::write(r.join(format!("{STAGING_PREFIX}x")), b"s").expect("write");
        std::fs::write(r.join(content::CONTENT_FILE), b"not this one").expect("write");
        let e = DirExport::open(&r).expect("export");
        let dest = d.path().join("dest");
        let report =
            fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(1, 65536)).expect("fetch");
        assert_eq!(report.files, 1);
        assert_eq!(std::fs::read(dest.join("keep")).expect("keep"), b"k");
        assert!(!dest.join(format!("{STAGING_PREFIX}x")).exists());
        // A server that lists one anyway gets it refused per file.
        let entries = vec![
            Entry {
                path: "a".into(),
                size: 1,
                generation: 1,
                content: 1,
                ..Entry::default()
            },
            Entry {
                path: format!("d/{}", content::CONTENT_FILE),
                size: 1,
                generation: 1,
                content: 1,
                ..Entry::default()
            },
        ];
        let p = plan(&entries, "", 192).expect("plan");
        assert_eq!(p.files.len(), 1);
        assert_eq!(p.failed.len(), 1);
    }

    /// Answers the manifest stat with a scripted size.
    struct Liar {
        inner: Local,
        size: u64,
    }
    impl RangeSource for Liar {
        fn max_range(&self) -> usize {
            self.inner.max_range()
        }
        fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
            self.inner.stat(key).map(|(_, g)| (self.size, g))
        }
        fn fetch(
            &mut self,
            key: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            self.inner.fetch(key, offset, generation, len)
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            Ok(())
        }
    }

    #[test]
    fn manifest_size_from_the_server_is_bounded() {
        // The server's size went straight into Vec::with_capacity.
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let src = source(&e, 65536, vec![]);
        for size in [u64::MAX, (256 << 20) + 1] {
            let liar = || {
                Ok(Liar {
                    inner: src()?,
                    size,
                })
            };
            let err = fetch_dir(liar, "", &dest, &opts(1, 65536)).expect_err("too big");
            assert!(
                matches!(err, FetchError::Manifest(ref m) if m.contains("limit")),
                "{err}"
            );
        }
        let mut small = opts(1, 65536);
        small.max_manifest_bytes = 64;
        let err =
            fetch_dir(source(&e, 65536, vec![]), "", &dest, &small).expect_err("over the cap");
        assert!(matches!(err, FetchError::Manifest(_)), "{err}");
        assert!(!dest.exists());
    }

    #[test]
    fn a_manifest_listing_a_path_as_file_and_directory_is_refused() {
        let entries = vec![
            Entry {
                path: "a".into(),
                size: 1,
                generation: 1,
                content: 1,
                ..Entry::default()
            },
            Entry {
                path: "a/b".into(),
                size: 1,
                generation: 1,
                content: 1,
                ..Entry::default()
            },
        ];
        assert!(matches!(
            plan(&entries, "", 192),
            Err(FetchError::Manifest(_))
        ));
        let dup = vec![entries[0].clone(), entries[0].clone()];
        assert!(matches!(plan(&dup, "", 192), Err(FetchError::Manifest(_))));
    }

    #[test]
    fn rejects_bad_options_and_prefixes() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        for o in [opts(0, 65536), opts(1, 0), opts(1, 1000)] {
            assert!(matches!(
                fetch_dir(source(&e, 65536, vec![]), "", &dest, &o),
                Err(FetchError::Invalid(_))
            ));
        }
        for p in ["../x", "/abs", "a/../b"] {
            assert!(
                matches!(
                    fetch_dir(source(&e, 65536, vec![]), p, &dest, &opts(1, 65536)),
                    Err(FetchError::Invalid(_))
                ),
                "{p}"
            );
        }
        assert!(matches!(
            fetch_dir(source(&e, 65536, vec![]), "missing", &dest, &opts(1, 65536)),
            Err(FetchError::Manifest(_))
        ));
    }

    /// Run a test body that must finish within `limit`: a regression that
    /// would make it wait forever fails it instead of hanging the run.
    fn within(limit: Duration, body: impl FnOnce() + Send + 'static) {
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            body();
            let _ = tx.send(());
        });
        match rx.recv_timeout(limit) {
            Ok(()) => {}
            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => panic!("the test body failed"),
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {
                panic!("did not finish within {limit:?}")
            }
        }
    }

    // ----- several sources -----

    /// (source, its data fetches served before, path, bytes).
    type ChangeAfter = (String, usize, PathBuf, Vec<u8>);

    #[derive(Default)]
    struct FleetState {
        /// BUSY answers left per source (u32::MAX: always busy).
        busy: Mutex<HashMap<String, u32>>,
        /// Sources that are gone: opens, fetches and reconnects fail.
        down: Mutex<std::collections::HashSet<String>>,
        /// (source, data fetches it serves before it dies).
        kill: Mutex<HashMap<String, usize>>,
        /// Per source: data fetches it serves before every later one comes
        /// back empty (a file shorter than its manifest: inconsistent).
        truncate: Mutex<HashMap<String, usize>>,
        /// Delay of a source's first open.
        slow_first_open: Mutex<HashMap<String, Duration>>,
        /// A source goes down after this many busy answers.
        down_after_busy: Mutex<HashMap<String, u32>>,
        /// (source, its data fetches served before, path, bytes): a file
        /// rewritten on that source mid-copy.
        change_after: Mutex<Option<ChangeAfter>>,
        /// Server identity per name (`SourceSet::identity`).
        identity: Mutex<HashMap<String, String>>,
        /// Opening these panics (a bug in the open path).
        panic_on_open: Mutex<std::collections::HashSet<String>>,
        /// Resolving names takes this long (slow DNS).
        slow_candidates: Mutex<Option<Duration>>,
        /// (source, which of its opens): that one connection fails every
        /// request and every reconnect; others to the source work.
        broken_conn: Mutex<Option<(String, usize)>>,
        /// (source, data fetches logged when it was released).
        released_at: Mutex<Vec<(String, usize)>>,
        /// Addresses that never answer: `reachable` takes its whole timeout.
        black_hole: Mutex<std::collections::HashSet<String>>,
        /// Releasing an anchor takes this long (connection teardown).
        slow_release: Mutex<Option<Duration>>,
        /// (source, busy until): its version 3 listing stat answers busy
        /// until then; after it, it answers (or, with `old`, NOT_FOUND).
        v3_busy_until: Mutex<HashMap<String, Instant>>,
        /// Sources whose version 2 listing stat always answers busy.
        v2_busy: Mutex<std::collections::HashSet<String>>,
        /// Data fetches served: (source, key, offset).
        log: Mutex<Vec<(String, String, u64)>>,
        /// (source, opens served before it turns busy, BUSY answers then).
        busy_after: Mutex<Option<(String, usize, u32)>>,
        /// Per source: (data fetches served before its fetches turn busy,
        /// busy answers then; u32::MAX: forever).
        busy_fetches: Mutex<HashMap<String, (usize, u32)>>,
        /// Like `busy_fetches`, but only data fetches turn busy; the
        /// manifest is always served.
        busy_ranges: Mutex<HashMap<String, (usize, u32)>>,
        /// Every busy answer: (source, thread, when).
        busy_log: Mutex<Vec<(String, std::thread::ThreadId, Instant)>>,
        /// (path, bytes) written over a file at the first busy answer.
        change_on_busy: Mutex<Option<(PathBuf, Vec<u8>)>>,
        opens: Mutex<Vec<String>>,
        released: Mutex<Vec<String>>,
        /// Sources that serve no version 3 manifest (an older nvstor-peer).
        old: Mutex<std::collections::HashSet<String>>,
        /// Transport errors left on version 3 listing stats, per source.
        flaky_v3: Mutex<HashMap<String, u32>>,
    }

    /// In-process sources by name, each an export, with scripted BUSY
    /// answers and deaths.
    struct Fleet {
        exports: Vec<(String, Arc<DirExport>)>,
        state: Arc<FleetState>,
    }

    impl Fleet {
        fn new(exports: &[(&str, &Arc<DirExport>)]) -> Self {
            Self {
                exports: exports
                    .iter()
                    .map(|(n, e)| (n.to_string(), Arc::clone(e)))
                    .collect(),
                state: Arc::new(FleetState::default()),
            }
        }
        fn busy(self, name: &str, times: u32) -> Self {
            self.state.busy.lock().insert(name.into(), times);
            self
        }
        fn down(self, name: &str) -> Self {
            self.state.down.lock().insert(name.into());
            self
        }
        fn flaky_v3(self, name: &str, errors: u32) -> Self {
            self.state.flaky_v3.lock().insert(name.into(), errors);
            self
        }
        fn old(self, name: &str) -> Self {
            self.state.old.lock().insert(name.into());
            self
        }
        fn busy_after(self, name: &str, opens: usize, times: u32) -> Self {
            *self.state.busy_after.lock() = Some((name.into(), opens, times));
            self
        }
        fn identity(self, name: &str, id: &str) -> Self {
            self.state.identity.lock().insert(name.into(), id.into());
            self
        }
        fn broken_conn(self, name: &str, nth_open: usize) -> Self {
            *self.state.broken_conn.lock() = Some((name.into(), nth_open));
            self
        }
        fn slow_first_open(self, name: &str, delay: Duration) -> Self {
            self.state.slow_first_open.lock().insert(name.into(), delay);
            self
        }
        fn down_after_busy(self, name: &str, answers: u32) -> Self {
            self.state
                .down_after_busy
                .lock()
                .insert(name.into(), answers);
            self
        }
        fn change_after(self, name: &str, fetches: usize, path: PathBuf, bytes: &[u8]) -> Self {
            *self.state.change_after.lock() = Some((name.into(), fetches, path, bytes.to_vec()));
            self
        }
        fn busy_ranges(self, name: &str, after: usize, times: u32) -> Self {
            self.state
                .busy_ranges
                .lock()
                .insert(name.into(), (after, times));
            self
        }
        fn change_on_busy(self, path: PathBuf, bytes: &[u8]) -> Self {
            *self.state.change_on_busy.lock() = Some((path, bytes.to_vec()));
            self
        }
        fn opens_of(&self, name: &str) -> usize {
            self.state
                .opens
                .lock()
                .iter()
                .filter(|n| *n == name)
                .count()
        }
        fn busy_fetches(self, name: &str, after: usize, times: u32) -> Self {
            self.state
                .busy_fetches
                .lock()
                .insert(name.into(), (after, times));
            self
        }
        fn kill_after(self, name: &str, fetches: usize) -> Self {
            self.state.kill.lock().insert(name.into(), fetches);
            self
        }
        fn truncate_after(self, name: &str, fetches: usize) -> Self {
            self.state.truncate.lock().insert(name.into(), fetches);
            self
        }
        fn served_by(&self, name: &str) -> Vec<(String, u64)> {
            self.state
                .log
                .lock()
                .iter()
                .filter(|(n, ..)| n == name)
                .map(|(_, k, o)| (k.clone(), *o))
                .collect()
        }
    }

    struct FleetSource {
        name: String,
        state: Arc<FleetState>,
        inner: Local,
        broken: bool,
    }

    impl FleetSource {
        fn alive(&self) -> Result<(), GetError> {
            if self.broken || self.state.down.lock().contains(&self.name) {
                return Err(GetError::Transport(format!("{} is down", self.name)));
            }
            Ok(())
        }
    }

    impl RangeSource for FleetSource {
        fn max_range(&self) -> usize {
            self.inner.max_range()
        }
        fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
            self.alive()?;
            if is_v3(key)
                && self
                    .state
                    .v3_busy_until
                    .lock()
                    .get(&self.name)
                    .is_some_and(|t| Instant::now() < *t)
            {
                return Err(GetError::Busy);
            }
            if !is_v3(key)
                && manifest::prefix_of(key).is_some()
                && self.state.v2_busy.lock().contains(&self.name)
            {
                return Err(GetError::Busy);
            }
            if self.state.old.lock().contains(&self.name) && is_v3(key) {
                return Err(GetError::NotFound);
            }
            if is_v3(key) {
                if let Some(left) = self
                    .state
                    .flaky_v3
                    .lock()
                    .get_mut(&self.name)
                    .filter(|n| **n > 0)
                {
                    *left -= 1;
                    return Err(GetError::Transport("link flap".into()));
                }
            }
            self.inner.stat(key)
        }
        fn fetch(
            &mut self,
            key: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            self.alive()?;
            if self.state.old.lock().contains(&self.name) && is_v3(key) {
                return Err(GetError::NotFound);
            }
            let data = manifest::prefix_of(key).is_none();
            let served = self
                .state
                .log
                .lock()
                .iter()
                .filter(|(n, ..)| *n == self.name)
                .count();
            let mut busy = false;
            for (map, applies) in [
                (&self.state.busy_fetches, true),
                (&self.state.busy_ranges, data),
            ] {
                if let Some((after, times)) = map.lock().get_mut(&self.name) {
                    if applies && served >= *after && *times > 0 {
                        if *times != u32::MAX {
                            *times -= 1;
                        }
                        busy = true;
                    }
                }
            }
            if busy {
                self.state.busy_log.lock().push((
                    self.name.clone(),
                    std::thread::current().id(),
                    Instant::now(),
                ));
                if let Some(left) = self.state.down_after_busy.lock().get_mut(&self.name) {
                    *left = left.saturating_sub(1);
                    if *left == 0 {
                        self.state.down.lock().insert(self.name.clone());
                    }
                }
                if let Some((path, bytes)) = self.state.change_on_busy.lock().take() {
                    std::thread::sleep(Duration::from_millis(20));
                    std::fs::write(path, bytes).expect("rewrite");
                }
                return Err(GetError::Busy);
            }
            if manifest::prefix_of(key).is_none() {
                {
                    let mut change = self.state.change_after.lock();
                    if change
                        .as_ref()
                        .is_some_and(|(n, after, ..)| *n == self.name && served >= *after)
                    {
                        if let Some((_, _, path, bytes)) = change.take() {
                            std::fs::write(path, bytes).expect("rewrite");
                        }
                    }
                }
                if self
                    .state
                    .truncate
                    .lock()
                    .get(&self.name)
                    .is_some_and(|after| served >= *after)
                {
                    return Ok(&[]);
                }
                let mut kill = self.state.kill.lock();
                if let Some(left) = kill.get_mut(&self.name) {
                    if *left == 0 {
                        self.state.down.lock().insert(self.name.clone());
                        kill.remove(&self.name);
                        return Err(GetError::Transport(format!("{} died", self.name)));
                    }
                    *left -= 1;
                }
                self.state
                    .log
                    .lock()
                    .push((self.name.clone(), key.to_string(), offset));
            }
            self.inner.fetch(key, offset, generation, len)
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            self.alive()
                .map_err(|e| HydrateError::Connect(e.to_string()))
        }
    }

    impl SourceSet for Fleet {
        type Source = FleetSource;
        fn candidates(&self) -> Vec<String> {
            let slow = *self.state.slow_candidates.lock();
            if let Some(d) = slow {
                std::thread::sleep(d);
            }
            self.exports.iter().map(|(n, _)| n.clone()).collect()
        }
        fn identity(&self, name: &str) -> Option<String> {
            self.state.identity.lock().get(name).cloned()
        }
        fn anchor_generation(&self, name: &str) -> u64 {
            self.opens_of(name) as u64
        }
        fn reachable(&self, name: &str, timeout: Duration) -> bool {
            if self.state.black_hole.lock().contains(name) {
                std::thread::sleep(timeout);
                return false;
            }
            true
        }
        fn open(&self, name: &str) -> Result<FleetSource, OpenError> {
            if self.state.panic_on_open.lock().contains(name) {
                panic!("test: opening {name} panics");
            }
            let slow = self.state.slow_first_open.lock().remove(name);
            if let Some(d) = slow {
                std::thread::sleep(d);
            }
            if self.state.down.lock().contains(name) {
                return Err(OpenError::Failed(HydrateError::Connect(format!(
                    "{name}: connection refused"
                ))));
            }
            if let Some(left) = self.state.busy.lock().get_mut(name) {
                if *left > 0 {
                    if *left != u32::MAX {
                        *left -= 1;
                    }
                    return Err(OpenError::Busy(format!("{name} is at its fetcher cap")));
                }
            }
            if let Some((victim, opens, times)) = self.state.busy_after.lock().as_mut() {
                let served = self
                    .state
                    .opens
                    .lock()
                    .iter()
                    .filter(|n| *n == name)
                    .count();
                if victim == name && served >= *opens && *times > 0 {
                    *times -= 1;
                    return Err(OpenError::Busy(format!("{name} gave the session away")));
                }
            }
            let (_, e) = self
                .exports
                .iter()
                .find(|(n, _)| n == name)
                .ok_or_else(|| {
                    OpenError::Failed(HydrateError::Connect(format!("{name}: unknown")))
                })?;
            let nth = {
                let mut opens = self.state.opens.lock();
                let nth = opens.iter().filter(|n| *n == name).count();
                opens.push(name.to_string());
                nth
            };
            let broken = self
                .state
                .broken_conn
                .lock()
                .as_ref()
                .is_some_and(|(n, k)| n == name && *k == nth);
            let inner = source(e, 65536, vec![])().map_err(OpenError::Failed)?;
            Ok(FleetSource {
                name: name.to_string(),
                state: Arc::clone(&self.state),
                inner,
                broken,
            })
        }
        fn release(&self, name: &str) {
            let slow = *self.state.slow_release.lock();
            if let Some(d) = slow {
                std::thread::sleep(d);
            }
            let logged = self.state.log.lock().len();
            self.state
                .released_at
                .lock()
                .push((name.to_string(), logged));
            self.state.released.lock().push(name.to_string());
        }
    }

    /// A version 3 listing key: what an older export answers NOT_FOUND.
    fn is_v3(key: &str) -> bool {
        manifest::listing_of(key).is_some_and(|(v, _)| v == manifest::VERSION)
    }

    /// A seed under which `first` is tried before every other name.
    fn seed_putting_first(first: &str, names: &[&str]) -> u64 {
        let names: Vec<String> = names.iter().map(|n| n.to_string()).collect();
        (0..)
            .find(|&s| source_order(s, names.clone())[0] == first)
            .expect("some seed")
    }

    fn multi_opts(connections: usize, seed: u64) -> FetchOptions {
        FetchOptions {
            seed,
            busy_backoff: Duration::from_millis(1),
            max_busy_backoff: Duration::from_millis(4),
            wait: Duration::from_secs(10),
            ..opts(connections, 65536)
        }
    }

    /// A replica of `export`'s tree, made the way a replica is made: a
    /// fetch into a directory, which records the content identities.
    fn replica_of(export: &Arc<DirExport>, dir: &Path) -> Arc<DirExport> {
        fetch_dir(source(export, 65536, vec![]), "", dir, &opts(4, 65536)).expect("replica fetch");
        DirExport::open(dir).expect("replica export")
    }

    #[test]
    fn the_source_order_is_per_client_and_stable() {
        let names: Vec<String> = (0..20).map(|i| format!("10.0.0.{i}:9890")).collect();
        let a = source_order(1, names.clone());
        assert_eq!(
            a,
            source_order(1, names.iter().rev().cloned().collect()),
            "input order does not matter"
        );
        assert_ne!(
            a,
            source_order(2, names.clone()),
            "another client, another order"
        );
        // A name keeps its place relative to the others when the list grows.
        let mut more = names.clone();
        more.push("10.0.0.99:9890".into());
        let b: Vec<String> = source_order(1, more)
            .into_iter()
            .filter(|n| n != "10.0.0.99:9890")
            .collect();
        assert_eq!(a, b);
        // First choices spread over the list across clients.
        let firsts: std::collections::HashSet<String> = (0..200)
            .map(|s| source_order(s, names.clone())[0].clone())
            .collect();
        assert!(
            firsts.len() >= 15,
            "first choices cluster: {}",
            firsts.len()
        );
        let dup = source_order(1, vec!["x".into(), "x".into()]);
        assert_eq!(dup, ["x"]);
    }

    #[test]
    fn a_busy_source_is_skipped_for_the_next_one() {
        let (d, e) = source_tree();
        let replica = replica_of(&e, &d.path().join("replica"));
        let seed = seed_putting_first("origin", &["origin", "replica"]);
        let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)]).busy("origin", u32::MAX);
        let dest = d.path().join("dest");
        let r = fetch_from(&fleet, "ckpt", &dest, &multi_opts(4, seed)).expect("fetch");
        assert_eq!(r.source, "replica");
        assert_eq!(r.failovers, 0);
        assert_same_tree(&d.path().join("export/ckpt"), &dest);
        assert!(fleet.served_by("origin").is_empty());
        // The same content digest as a copy straight from the origin.
        let direct = fetch_dir(
            source(&e, 65536, vec![]),
            "ckpt",
            &d.path().join("direct"),
            &opts(2, 65536),
        )
        .expect("direct");
        assert_eq!(r.content_digest, direct.content_digest);
    }

    #[test]
    fn when_every_source_is_busy_the_fetch_backs_off_and_retries() {
        let (d, e) = source_tree();
        let fleet = Fleet::new(&[("a", &e), ("b", &e)])
            .busy("a", 3)
            .busy("b", u32::MAX);
        let dest = d.path().join("dest");
        let r = fetch_from(&fleet, "ckpt/chunks", &dest, &multi_opts(2, 7)).expect("fetch");
        assert_eq!(r.source, "a");
        assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);

        // Busy for longer than the fetch will wait: Unavailable, and
        // nothing is created.
        let fleet = Fleet::new(&[("a", &e), ("b", &e)])
            .busy("a", u32::MAX)
            .busy("b", u32::MAX);
        let dest = d.path().join("never");
        let mut o = multi_opts(2, 7);
        o.wait = Duration::from_millis(100);
        let started = Instant::now();
        let err = fetch_from(&fleet, "ckpt", &dest, &o).expect_err("all busy");
        assert!(
            matches!(err, FetchError::Unavailable(ref m) if m.contains("busy")),
            "{err}"
        );
        assert!(started.elapsed() < Duration::from_secs(5));
        assert!(!dest.exists());
    }

    // A worker reconnecting to the source in use (attach)
    // that answers BUSY backs off and tries again; it does not take the
    // source out of the running (with one source, that ended the fetch).
    #[test]
    fn a_busy_reconnect_backs_off_instead_of_dropping_the_source() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            // The first open is the selection's; every worker's open after it
            // is refused BUSY, 10 times in all (more than max_retries).
            let fleet = Fleet::new(&[("a", &e)]).busy_after("a", 1, 10);
            let dest = d.path().join("dest");
            let r = fetch_from(&fleet, "ckpt", &dest, &multi_opts(4, 0))
                .expect("waited out the BUSY answers");
            assert_eq!((r.source.as_str(), r.failovers), ("a", 0));
            assert_same_tree(&d.path().join("export/ckpt"), &dest);
            // BUSY past `wait`: Unavailable.
            let fleet = Fleet::new(&[("a", &e)]).busy_after("a", 1, u32::MAX);
            let mut o = multi_opts(4, 0);
            o.wait = Duration::from_millis(100);
            let err = fetch_from(&fleet, "ckpt", &d.path().join("never"), &o)
                .expect_err("busy past wait");
            assert!(
                matches!(err, FetchError::Unavailable(ref m) if m.contains("busy")),
                "{err}"
            );
        });
    }

    // A source that answers busy mid-copy hands its work to
    // another eligible source at once (well under `wait`), without being
    // marked dead; when every eligible source is busy the fetch waits,
    // then ends Unavailable; a source that stops being busy is used again.
    #[test]
    fn a_busy_source_hands_its_work_to_an_idle_one() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            // The origin serves 8 ranges, then is busy forever.
            let fleet = Arc::new(
                Fleet::new(&[("origin", &e), ("replica", &replica)]).busy_fetches(
                    "origin",
                    8,
                    u32::MAX,
                ),
            );
            let dest = d.path().join("dest");
            let mut o = multi_opts(1, seed);
            o.wait = Duration::from_secs(1800);
            let (tx, rx) = std::sync::mpsc::channel();
            let (f, dst) = (Arc::clone(&fleet), dest.clone());
            let started = Instant::now();
            std::thread::spawn(move || {
                let _ = tx.send(fetch_from(&*f, "ckpt/chunks", &dst, &o));
            });
            let r = rx
                .recv_timeout(Duration::from_secs(20))
                .expect("finished well under wait")
                .expect("fetch");
            assert!(started.elapsed() < Duration::from_secs(20));
            assert_eq!((r.source.as_str(), r.failovers), ("origin", 1));
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
            assert!(
                !fleet.served_by("replica").is_empty(),
                "the rest came from the replica"
            );
        });
    }

    #[test]
    fn when_every_eligible_source_is_busy_mid_copy_the_fetch_waits_then_ends() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                .busy_fetches("origin", 4, u32::MAX)
                .busy_fetches("replica", 0, u32::MAX);
            let mut o = multi_opts(2, seed);
            o.wait = Duration::from_millis(300);
            let started = Instant::now();
            let err = fetch_from(&fleet, "ckpt/chunks", &d.path().join("never"), &o)
                .expect_err("all busy");
            assert!(
                matches!(err, FetchError::Unavailable(ref m) if m.contains("busy")),
                "{err}"
            );
            assert!(
                started.elapsed() >= Duration::from_millis(300),
                "it waited for a source"
            );
            assert!(started.elapsed() < Duration::from_secs(10));
        });
    }

    #[test]
    fn a_source_that_stops_being_busy_is_used_again() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            // One source, busy for 6 answers mid-copy (more than max_retries),
            // then free: the copy completes from it, not counted a failover.
            let fleet = Fleet::new(&[("a", &e)]).busy_fetches("a", 3, 6);
            let dest = d.path().join("dest");
            let r = fetch_from(&fleet, "ckpt/chunks", &dest, &multi_opts(2, 0)).expect("fetch");
            assert_eq!((r.source.as_str(), r.failovers), ("a", 0));
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
            assert!(fleet.served_by("a").len() > 3, "used again after it cooled");
        });
    }

    // Ranges answered busy forever
    // while the manifest is served. The fetch ends Unavailable about
    // `wait` after the last progress, opens nothing again, releases
    // nothing (no livelock of reopen + manifest re-read).
    #[test]
    fn ranges_busy_forever_end_unavailable_without_releasing_anything() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]).busy_ranges("a", 0, u32::MAX);
            let mut o = multi_opts(4, 0);
            o.wait = Duration::from_millis(300);
            let started = Instant::now();
            let err = fetch_from(&fleet, "ckpt/chunks", &d.path().join("never"), &o)
                .expect_err("busy forever");
            assert!(
                matches!(err, FetchError::Unavailable(ref m) if m.contains("no range completed")),
                "{err}"
            );
            assert!(
                started.elapsed() < Duration::from_millis(300) + Duration::from_secs(3),
                "{:?}",
                started.elapsed()
            );
            assert!(
                fleet.state.released.lock().is_empty(),
                "released: {:?}",
                fleet.state.released.lock()
            );
            assert!(
                fleet.opens_of("a") <= 4,
                "one connection per worker: {} opens",
                fleet.opens_of("a")
            );
        });
    }

    // A worker's busy pauses grow while the busy answers
    // continue, up to max_busy_backoff, and never past it.
    #[test]
    fn busy_pauses_escalate_to_the_cap() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]).busy_ranges("a", 0, u32::MAX);
            let mut o = multi_opts(1, 0);
            o.busy_backoff = Duration::from_millis(2);
            o.max_busy_backoff = Duration::from_millis(40);
            o.wait = Duration::from_millis(1500);
            let _ = fetch_from(&fleet, "ckpt/chunks", &d.path().join("never"), &o)
                .expect_err("busy forever");
            let times: Vec<Instant> = fleet.state.busy_log.lock().iter().map(|x| x.2).collect();
            let gaps: Vec<Duration> = times.windows(2).map(|w| w[1] - w[0]).collect();
            let max = gaps.iter().max().copied().unwrap_or_default();
            assert!(
                max >= Duration::from_millis(20),
                "pauses never grew: {gaps:?}"
            );
            // Scheduling delay on a loaded host can stretch a few gaps; the
            // pauses themselves never pass the cap.
            let over = gaps
                .iter()
                .filter(|g| **g > Duration::from_millis(40 + 30))
                .count();
            assert!(over * 10 <= gaps.len(), "past the cap: {gaps:?}");
        });
    }

    // Work moves off a source busy on ranges to an idle one
    // without giving up the busy one (its anchor stays).
    #[test]
    fn work_moves_to_an_idle_source_and_the_busy_one_is_kept() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)]).busy_ranges(
                "origin",
                6,
                u32::MAX,
            );
            let dest = d.path().join("dest");
            let mut o = multi_opts(4, seed);
            o.wait = Duration::from_secs(1800);
            let r = fetch_from(&fleet, "ckpt/chunks", &dest, &o).expect("fetch");
            assert_eq!(r.source, "origin");
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
            assert!(!fleet.served_by("replica").is_empty());
            assert!(
                fleet.state.released.lock().is_empty(),
                "released: {:?}",
                fleet.state.released.lock()
            );
        });
    }

    // A source validated earlier (as a busy source's alternate) becomes
    // the replacement when another source is caught serving
    // inconsistently: files refetched after that refuse writes from views
    // older than the refetch, so the reused source gets a fresh view (a
    // new epoch) and the copy completes instead of refusing it forever.
    #[test]
    fn a_source_validated_earlier_replaces_an_inconsistent_one() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                .busy_ranges("origin", 1, 3)
                .truncate_after("origin", 3);
            let dest = d.path().join("dest");
            let mut o = multi_opts(4, seed);
            o.wait = Duration::from_secs(1800);
            let r = fetch_from(&fleet, "ckpt/chunks", &dest, &o).expect("fetch");
            assert_eq!(r.source, "origin");
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
            assert!(!fleet.served_by("replica").is_empty());
        });
    }

    /// Host memory standing in for GPU memory: ranges land in a slot as
    /// the NIC would write them, then go through the same read-back,
    /// check and place as on a GPU.
    struct MemDevice {
        mem: Mutex<Vec<u8>>,
        layout: Layout,
        flushes: AtomicU64,
        /// Landings to damage after their CRC is taken (bad data on the wire).
        damage: AtomicU64,
        fail_place: bool,
    }

    impl MemDevice {
        fn new(layout: &Layout) -> Self {
            Self {
                mem: Mutex::new(vec![0xee; layout.len() as usize]),
                layout: layout.clone(),
                flushes: AtomicU64::new(0),
                damage: AtomicU64::new(0),
                fail_place: false,
            }
        }
        fn file(&self, p: &Placed) -> Vec<u8> {
            self.mem.lock()[p.offset as usize..(p.offset + p.size) as usize].to_vec()
        }
    }

    /// A 4 KiB-aligned host buffer.
    struct Aligned {
        v: Vec<u8>,
        off: usize,
        len: usize,
    }
    impl std::ops::Deref for Aligned {
        type Target = [u8];
        fn deref(&self) -> &[u8] {
            &self.v[self.off..self.off + self.len]
        }
    }
    impl std::ops::DerefMut for Aligned {
        fn deref_mut(&mut self) -> &mut [u8] {
            &mut self.v[self.off..self.off + self.len]
        }
    }

    impl Device for MemDevice {
        fn make_visible(&self) -> Result<(), String> {
            self.flushes.fetch_add(1, Ordering::Relaxed);
            Ok(())
        }
        fn read_slot(&self, slot: usize, out: &mut [u8]) -> Result<(), String> {
            let at = self.layout.slot_offset(slot) as usize;
            out.copy_from_slice(&self.mem.lock()[at..at + out.len()]);
            Ok(())
        }
        fn place(&self, bytes: &[u8], offset: u64) -> Result<(), String> {
            if self.fail_place {
                return Err("copy engine fault".into());
            }
            assert!(
                offset + bytes.len() as u64 <= self.layout.files_len,
                "placed past the files"
            );
            let at = offset as usize;
            self.mem.lock()[at..at + bytes.len()].copy_from_slice(bytes);
            Ok(())
        }
        fn read(&self, offset: u64, out: &mut [u8]) -> Result<(), String> {
            let at = offset as usize;
            out.copy_from_slice(&self.mem.lock()[at..at + out.len()]);
            Ok(())
        }
        fn host_buffer(&self, len: usize) -> Result<HostBuffer, String> {
            let v = vec![0u8; len + ALIGN];
            let off = (ALIGN - v.as_ptr() as usize % ALIGN) % ALIGN;
            Ok(Box::new(Aligned { v, off, len }))
        }
    }

    /// Lands a range by fetching it and writing it into the slot, with the
    /// CRC-32C of what the source sent.
    fn land_mem<S: RangeSource>(
        s: &mut S,
        dev: &MemDevice,
        slot: usize,
        key: &str,
        at: u64,
        generation: u128,
        len: usize,
    ) -> Result<Landed, GetError> {
        assert!(
            len <= dev.layout.slot_len,
            "{len} > slot {}",
            dev.layout.slot_len
        );
        let bytes = s.fetch(key, at, generation, len)?;
        let crc = nvstor_core::checksum::crc32c(bytes);
        let n = bytes.len();
        let base = dev.layout.slot_offset(slot) as usize;
        let mut mem = dev.mem.lock();
        mem[base..base + n].copy_from_slice(bytes);
        if n > 0
            && dev
                .damage
                .fetch_update(Ordering::AcqRel, Ordering::Acquire, |d| d.checked_sub(1))
                .is_ok()
        {
            mem[base] ^= 0xff;
        }
        Ok(Landed {
            read: nvstor_rdma::object::ObjectRead {
                len: n,
                size: 0,
                generation,
            },
            crc32c: crc,
        })
    }

    /// [`fetch_to_device`] as one result; a complete fetch must return
    /// its device.
    fn device_fetch<C: SourceSet, D: Device>(
        sources: &C,
        prefix: &str,
        dest: &Path,
        opts: &FetchOptions,
        to: ToDevice<'_, C::Source, D>,
    ) -> Result<(FetchReport, DeviceReport, D), FetchError> {
        match fetch_to_device(sources, prefix, dest, opts, to) {
            (Ok(r), Some((dr, d))) => Ok((r, dr, d)),
            (Ok(_), None) => panic!("a complete fetch into a device returned no device"),
            (Err(e), _) => Err(e),
        }
    }

    fn assert_device_holds(dev: &MemDevice, layout: &Layout, src: &Path) {
        for p in &layout.files {
            assert_eq!(p.offset % DEVICE_ALIGN, 0, "{}", p.path);
            assert_eq!(
                dev.file(p),
                std::fs::read(src.join(&p.path)).expect("source"),
                "{}",
                p.path
            );
        }
    }

    #[test]
    fn a_device_fetch_puts_every_file_in_the_device_and_on_disk() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let readies = AtomicU64::new(0);
        let ready = |dev: &MemDevice, layout: &Layout, _: Duration| {
            // At this point every file is in the device.
            assert_device_holds(dev, layout, &d.path().join("export/ckpt"));
            readies.fetch_add(1, Ordering::Relaxed);
            Ok(())
        };
        let to = ToDevice {
            make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
            land: &land_mem::<Local>,
            ready: &ready,
        };
        let (r, dr, dev) = device_fetch(
            &One(source(&e, 65536, vec![])),
            "ckpt",
            &dest,
            &opts(6, 65536),
            to,
        )
        .expect("fetch");
        assert_eq!(readies.load(Ordering::Relaxed), 1);
        assert_same_tree(&d.path().join("export/ckpt"), &dest);
        assert_device_holds(&dev, &dr.layout, &d.path().join("export/ckpt"));
        assert_eq!((r.files, dr.layout.files.len()), (44, 44));
        assert_eq!(r.ranges, 40 + 6 + 4 + 1);
        assert_eq!((dr.layout.slots, dr.layout.slot_len), (6, 65536));
        assert_eq!(dr.layout.files_len % DEVICE_SLOTS_ALIGN, 0);
        assert!(dr
            .layout
            .files
            .iter()
            .all(|p| p.offset + p.size <= dr.layout.files_len));
        assert!(dr.ready_after <= r.elapsed);
        // Every range was made visible before it was read back.
        assert!(dev.flushes.load(Ordering::Relaxed) >= r.ranges);
    }

    #[test]
    fn a_device_fetch_with_a_source_smaller_than_the_range() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let to = ToDevice {
            make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
            land: &land_mem::<Local>,
            ready: &|_: &MemDevice, _: &Layout, _: Duration| Ok(()),
        };
        // 16 KiB source buffer, 64 KiB ranges, one host buffer per worker.
        let (_, dr, dev) = device_fetch(
            &One(source(&e, 16384, vec![])),
            "",
            &dest,
            &opts(3, 65536),
            to,
        )
        .expect("fetch");
        assert_same_tree(&d.path().join("export"), &dest);
        assert_device_holds(&dev, &dr.layout, &d.path().join("export"));
    }

    #[test]
    fn bad_data_in_the_device_is_caught_and_fetched_again() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let to = ToDevice {
            make: Box::new(|l: &Layout| {
                let m = MemDevice::new(l);
                m.damage.store(2, Ordering::Relaxed);
                Ok(m)
            }),
            land: &land_mem::<Local>,
            ready: &|_: &MemDevice, _: &Layout, _: Duration| Ok(()),
        };
        let (r, dr, dev) = device_fetch(
            &One(source(&e, 65536, vec![])),
            "ckpt/chunks",
            &dest,
            &opts(1, 65536),
            to,
        )
        .expect("fetch");
        assert_eq!(r.retries, 2);
        assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
        assert_device_holds(&dev, &dr.layout, &d.path().join("export/ckpt/chunks"));
    }

    #[test]
    fn the_device_is_ready_before_a_slow_disk_is_done() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        PAUSE_WRITE
            .lock()
            .push(("ckpt/chunks/c.bin".into(), Duration::from_millis(400)));
        let ready_at = Mutex::new(None);
        let ready = |_: &MemDevice, _: &Layout, _: Duration| {
            *ready_at.lock() = Some(Instant::now());
            Ok(())
        };
        let to = ToDevice {
            make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
            land: &land_mem::<Local>,
            ready: &ready,
        };
        let r = device_fetch(
            &One(source(&e, 65536, vec![])),
            "ckpt/chunks",
            &dest,
            &opts(2, 65536),
            to,
        );
        let done = Instant::now();
        PAUSE_WRITE.lock().retain(|(k, _)| k != "ckpt/chunks/c.bin");
        r.expect("fetch");
        let ready = ready_at.lock().expect("ready was called");
        assert!(
            done.duration_since(ready) >= Duration::from_millis(300),
            "{:?}",
            done.duration_since(ready)
        );
        assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
    }

    #[test]
    fn a_device_fault_fails_the_fetch_and_leaves_no_staging() {
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        let to = ToDevice {
            make: Box::new(|l: &Layout| {
                let mut m = MemDevice::new(l);
                m.fail_place = true;
                Ok(m)
            }),
            land: &land_mem::<Local>,
            ready: &|_: &MemDevice, _: &Layout, _: Duration| -> Result<(), String> {
                panic!("never ready")
            },
        };
        let err = device_fetch(
            &One(source(&e, 65536, vec![])),
            "ckpt",
            &dest,
            &opts(4, 65536),
            to,
        )
        .err()
        .expect("the device fails");
        assert!(
            matches!(err, FetchError::Device(ref m) if m.contains("copy engine")),
            "{err}"
        );
        assert_no_staging(&dest);

        let to = ToDevice {
            make: Box::new(|_: &Layout| Err::<MemDevice, _>("no GPU memory".to_string())),
            land: &land_mem::<Local>,
            ready: &|_: &MemDevice, _: &Layout, _: Duration| Ok(()),
        };
        let err = device_fetch(
            &One(source(&e, 65536, vec![])),
            "ckpt",
            &dest,
            &opts(4, 65536),
            to,
        )
        .err()
        .expect("no device");
        assert!(
            matches!(err, FetchError::Device(ref m) if m == "no GPU memory"),
            "{err}"
        );

        let to = ToDevice {
            make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
            land: &land_mem::<Local>,
            ready: &|_: &MemDevice, _: &Layout, _: Duration| {
                Err("ready file: read-only".to_string())
            },
        };
        let err = device_fetch(
            &One(source(&e, 65536, vec![])),
            "ckpt",
            &dest,
            &opts(4, 65536),
            to,
        )
        .err()
        .expect("the ready hook fails");
        assert!(
            matches!(err, FetchError::Device(ref m) if m.contains("read-only")),
            "{err}"
        );
        assert_no_staging(&dest);
    }

    // An inconsistent source (a file shorter than its manifest) makes the
    // fetch refetch every file it wrote: in the device as on disk, with
    // nothing it placed or queued surviving.
    #[test]
    fn a_device_fetch_replaces_what_an_inconsistent_source_placed() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                .busy_ranges("origin", 1, 3)
                .truncate_after("origin", 3);
            let dest = d.path().join("dest");
            let mut o = multi_opts(4, seed);
            o.wait = Duration::from_secs(1800);
            let to = ToDevice {
                make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
                land: &land_mem::<FleetSource>,
                ready: &|_: &MemDevice, _: &Layout, _: Duration| Ok(()),
            };
            let (r, dr, dev) = device_fetch(&fleet, "ckpt/chunks", &dest, &o, to).expect("fetch");
            assert_eq!(r.source, "origin");
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
            assert_device_holds(&dev, &dr.layout, &d.path().join("export/ckpt/chunks"));
            assert!(!fleet.served_by("replica").is_empty());
        });
    }

    fn land_corrupting(
        s: &mut FleetSource,
        dev: &MemDevice,
        slot: usize,
        key: &str,
        at: u64,
        generation: u128,
        len: usize,
    ) -> Result<Landed, GetError> {
        let mut l = land_mem(s, dev, slot, key, at, generation, len)?;
        if s.name == "origin" && key.ends_with("chunks/a.bin") && at == 4 * 65536 {
            l.crc32c ^= 1;
        }
        Ok(l)
    }

    // A range is placed and queued from
    // a source that is then banned for bad data elsewhere (one range of
    // a.bin keeps failing its CRC from origin) before its disk write. The
    // queued bytes were checked: they must still reach the disk, not be
    // dropped while the file is renamed into place with a hole.
    #[test]
    fn a_ban_after_a_range_was_placed_does_not_drop_its_disk_write() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)]);
            PAUSE_WRITE
                .lock()
                .push(("ckpt/chunks/a.bin".into(), Duration::from_millis(150)));
            let dest = d.path().join("dest");
            let to = ToDevice {
                make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
                land: &land_corrupting,
                ready: &|_: &MemDevice, _: &Layout, _: Duration| Ok(()),
            };
            let r = device_fetch(&fleet, "ckpt/chunks", &dest, &multi_opts(2, seed), to);
            PAUSE_WRITE.lock().retain(|(k, _)| k != "ckpt/chunks/a.bin");
            let (_, dr, dev) = r.expect("fetch");
            assert_device_holds(&dev, &dr.layout, &d.path().join("export/ckpt/chunks"));
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
        });
    }

    // Finding 3: once the device was reported ready, a later failure of
    // the disk copy still hands the device back, holding every file.
    #[test]
    fn the_device_is_kept_when_the_disk_copy_fails_after_it_was_ready() {
        // SAFETY: geteuid has no preconditions.
        if unsafe { libc::geteuid() } == 0 {
            return; // root writes read-only files: nothing to fail
        }
        let (d, e) = source_tree();
        let dest = d.path().join("dest");
        PAUSE_WRITE
            .lock()
            .push(("ckpt/chunks/b.bin".into(), Duration::from_millis(300)));
        let dest_for_hook = dest.clone();
        let ready = move |_: &MemDevice, _: &Layout, _: Duration| -> Result<(), String> {
            // The staged files become read-only: the writes and renames
            // still queued fail.
            for entry in std::fs::read_dir(&dest_for_hook).map_err(|e| e.to_string())? {
                let p = entry.map_err(|e| e.to_string())?.path();
                if p.file_name()
                    .is_some_and(|n| n.to_string_lossy().starts_with(STAGING_PREFIX))
                {
                    for f in std::fs::read_dir(&p).map_err(|e| e.to_string())? {
                        let f = f.map_err(|e| e.to_string())?.path();
                        std::fs::set_permissions(
                            &f,
                            std::os::unix::fs::PermissionsExt::from_mode(0o400),
                        )
                        .map_err(|e| e.to_string())?;
                    }
                }
            }
            Ok(())
        };
        let to = ToDevice {
            make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
            land: &land_mem::<Local>,
            ready: &ready,
        };
        let (r, kept) = fetch_to_device(
            &One(source(&e, 65536, vec![])),
            "ckpt/chunks",
            &dest,
            &opts(2, 65536),
            to,
        );
        PAUSE_WRITE.lock().retain(|(k, _)| k != "ckpt/chunks/b.bin");
        let err = r.expect_err("the disk copy fails");
        assert!(matches!(err, FetchError::Io { .. }), "{err}");
        let (dr, dev) = kept.expect("the device is kept after it was ready");
        assert_device_holds(&dev, &dr.layout, &d.path().join("export/ckpt/chunks"));
        assert_no_staging(&dest);
    }

    #[test]
    fn a_device_fetch_that_cannot_copy_every_file_is_refused_before_any_transfer() {
        let (d, e) = source_tree();
        let long = d.path().join("export/ckpt").join("x".repeat(TEST_KEY_LEN));
        std::fs::write(&long, b"too long a key").expect("write");
        let dest = d.path().join("dest");
        let landed = AtomicU64::new(0);
        let land = |s: &mut Local,
                    dev: &MemDevice,
                    slot: usize,
                    key: &str,
                    at: u64,
                    g: u128,
                    len: usize| {
            landed.fetch_add(1, Ordering::Relaxed);
            land_mem(s, dev, slot, key, at, g, len)
        };
        let to = ToDevice {
            make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
            land: &land,
            ready: &|_: &MemDevice, _: &Layout, _: Duration| Ok(()),
        };
        let (r, kept) = fetch_to_device(
            &One(source(&e, 65536, vec![])),
            "ckpt",
            &dest,
            &opts(4, 65536),
            to,
        );
        let err = r.expect_err("refused");
        assert!(
            matches!(err, FetchError::NotAll(ref m) if m.contains("cannot be fetched")),
            "{err}"
        );
        assert!(kept.is_none());
        assert_eq!(landed.load(Ordering::Relaxed), 0, "nothing was transferred");
        // A plain fetch of the same copies everything else.
        let plain = fetch_dir(
            source(&e, 65536, vec![]),
            "ckpt",
            &d.path().join("plain"),
            &opts(4, 65536),
        );
        assert!(matches!(plain, Err(FetchError::Incomplete { .. })));
    }

    #[test]
    fn a_device_fetch_of_only_empty_files() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        std::fs::create_dir_all(d.path().join("export/e")).expect("mkdir");
        std::fs::write(d.path().join("export/e/a"), b"").expect("write");
        std::fs::write(d.path().join("export/e/b"), b"").expect("write");
        let e = DirExport::open(&d.path().join("export")).expect("export");
        let dest = d.path().join("dest");
        let to = ToDevice {
            make: Box::new(|l: &Layout| Ok(MemDevice::new(l))),
            land: &land_mem::<Local>,
            ready: &|_: &MemDevice, _: &Layout, _: Duration| Ok(()),
        };
        let (r, dr, _) = device_fetch(
            &One(source(&e, 65536, vec![])),
            "e",
            &dest,
            &opts(4, 65536),
            to,
        )
        .expect("fetch");
        assert_eq!((r.files, r.ranges, dr.layout.slots), (2, 0, 1));
        assert_same_tree(&d.path().join("export/e"), &dest);
    }

    // The inconsistent source is an alternate a worker
    // moved to while the current one was busy. The current source's view
    // predates the bar of the files refetched because of it, so it gets a
    // fresh epoch; otherwise its every write to them is refused and the
    // workers reconnect to it forever.
    #[test]
    fn an_inconsistent_alternate_leaves_the_current_source_usable() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                .busy_ranges("origin", 1, 3)
                .truncate_after("replica", 1);
            let dest = d.path().join("dest");
            let mut o = multi_opts(4, seed);
            o.wait = Duration::from_secs(1800);
            let r = fetch_from(&fleet, "ckpt/chunks", &dest, &o).expect("fetch");
            assert_eq!(r.source, "origin");
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
            assert!(
                fleet.opens_of("origin") < 200,
                "{} opens of origin",
                fleet.opens_of("origin")
            );
        });
    }

    // Bad data, then the only other source busy until the
    // wait runs out: an integrity failure (exit 5), not "unavailable",
    // whichever path the worker that reports first took.
    #[test]
    fn bad_data_then_a_busy_stall_is_an_integrity_failure() {
        within(Duration::from_secs(60), || {
            for _ in 0..5 {
                let (d, e) = source_tree();
                let replica = replica_of(&e, &d.path().join("replica"));
                let seed = seed_putting_first("origin", &["origin", "replica"]);
                let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                    .truncate_after("origin", 2)
                    .busy_ranges("replica", 0, u32::MAX);
                let mut o = multi_opts(4, seed);
                o.wait = Duration::from_millis(500);
                let err = fetch_from(&fleet, "ckpt/chunks", &d.path().join("dest"), &o)
                    .expect_err("cannot finish");
                assert!(matches!(err, FetchError::Corrupt(_)), "{err}");
            }
        });
    }

    // While every worker is busy on every source, the
    // fetch keeps its place (anchor) on each: nothing is released, so a
    // fetcher cap cannot give it away.
    #[test]
    fn the_session_place_is_kept_while_every_worker_is_busy() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                .busy_ranges("origin", 2, u32::MAX)
                .busy_ranges("replica", 0, u32::MAX);
            let mut o = multi_opts(8, seed);
            o.wait = Duration::from_millis(300);
            let err = fetch_from(&fleet, "ckpt/chunks", &d.path().join("never"), &o)
                .expect_err("all busy");
            assert!(matches!(err, FetchError::Unavailable(_)), "{err}");
            assert!(
                fleet.opens_of("replica") >= 1,
                "the idle-looking replica was tried"
            );
            assert!(
                fleet.state.released.lock().is_empty(),
                "released: {:?}",
                fleet.state.released.lock()
            );
        });
    }

    // A manifest read (bench, selection) waits out busy
    // answers on its connection; the error says what happened.
    #[test]
    fn a_busy_manifest_read_is_retried() {
        let (_d, e) = source_tree();
        let mut s = source_with(&e, 65536, vec![Some(GetError::Busy); 5], 0)().expect("src");
        let m = fetch_manifest(
            &mut s,
            "ckpt/chunks",
            &opts(1, 65536),
            &AtomicU64::new(0),
            &AtomicU64::new(0),
        )
        .expect("manifest after 5 busy answers");
        assert_eq!(m.entries.len(), 3);
        let mut s =
            source_with(&e, 65536, vec![Some(GetError::Busy); 1_000_000], 0)().expect("src");
        let mut o = opts(1, 65536);
        o.wait = Duration::from_millis(100);
        let err = fetch_manifest(
            &mut s,
            "ckpt/chunks",
            &o,
            &AtomicU64::new(0),
            &AtomicU64::new(0),
        )
        .expect_err("busy past wait");
        assert!(
            matches!(err, FetchError::Unavailable(ref m) if m.contains("listing read was answered busy")),
            "{err}"
        );
    }

    // Workers refused together do not come back
    // together: their retry times spread (per-worker decorrelated jitter).
    #[test]
    fn busy_workers_retry_at_spread_times() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]).busy_ranges("a", 0, u32::MAX);
            let mut o = multi_opts(8, 0);
            o.busy_backoff = Duration::from_millis(4);
            o.max_busy_backoff = Duration::from_millis(64);
            o.wait = Duration::from_millis(600);
            let _ = fetch_from(&fleet, "ckpt/chunks", &d.path().join("never"), &o)
                .expect_err("busy forever");
            let log = fleet.state.busy_log.lock().clone();
            let mut per: HashMap<std::thread::ThreadId, Vec<Instant>> = HashMap::new();
            for (_, t, at) in log {
                per.entry(t).or_default().push(at);
            }
            let k = 4;
            let kth: Vec<Instant> = per.values().filter(|v| v.len() > k).map(|v| v[k]).collect();
            assert!(
                kth.len() >= 4,
                "too few workers retried {k} times: {}",
                kth.len()
            );
            let spread = *kth.iter().max().expect("some") - *kth.iter().min().expect("some");
            assert!(
                spread >= Duration::from_millis(8),
                "workers retried in lockstep: spread {spread:?}"
            );
        });
    }

    // A file that changes on the source during a busy
    // spell is reported Changed (its ranges' generation), not as a
    // content mismatch from a re-read manifest.
    #[test]
    fn a_file_changed_during_a_busy_spell_is_changed() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let changing = d.path().join("export/ckpt/chunks/a.bin");
            let fleet = Fleet::new(&[("a", &e)])
                .busy_ranges("a", 2, 3)
                .change_on_busy(changing, b"rewritten");
            let err = fetch_from(
                &fleet,
                "ckpt/chunks",
                &d.path().join("dest"),
                &multi_opts(1, 0),
            )
            .expect_err("changed");
            assert!(
                matches!(err, FetchError::Changed { ref path } if path == "ckpt/chunks/a.bin"),
                "{err}"
            );
        });
    }

    // The replica's first open
    // is slow, the origin goes down while a worker is still checking the
    // replica, then the replica goes down too. The fetch must end (no
    // second validation of one source, no endless reopen of a dead one).
    #[test]
    fn a_source_checked_twice_cannot_hang_the_fetch() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                .busy_ranges("origin", 4, u32::MAX)
                .down_after_busy("origin", 3)
                .slow_first_open("replica", Duration::from_millis(300))
                .kill_after("replica", 2);
            let mut o = multi_opts(4, seed);
            o.wait = Duration::from_secs(3);
            let started = Instant::now();
            let err = fetch_from(&fleet, "ckpt", &d.path().join("dest"), &o)
                .expect_err("every source goes down");
            assert!(
                matches!(err, FetchError::Connect(_) | FetchError::Unavailable(_)),
                "{err}"
            );
            assert!(
                started.elapsed() < Duration::from_secs(3) + Duration::from_secs(5),
                "{:?}",
                started.elapsed()
            );
        });
    }

    // Time spent choosing the first source does
    // not count against the busy bound.
    #[test]
    fn time_before_the_first_range_does_not_count_against_the_busy_bound() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)])
                .slow_first_open("a", Duration::from_millis(950))
                .busy_ranges("a", 0, 40);
            let mut o = multi_opts(1, 0);
            o.wait = Duration::from_secs(1);
            let dest = d.path().join("dest");
            fetch_from(&fleet, "ckpt/chunks", &dest, &o)
                .expect("40 transient busy answers waited out");
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
        });
    }

    // An unbounded --wait-s (u64::MAX seconds) does not
    // overflow the deadline.
    #[test]
    fn an_unbounded_wait_does_not_overflow() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]).busy_ranges("a", 0, 3);
            let mut o = multi_opts(2, 0);
            o.wait = Duration::from_secs(u64::MAX);
            let dest = d.path().join("dest");
            fetch_from(&fleet, "ckpt/chunks", &dest, &o).expect("fetch");
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
        });
    }

    // Cooling entries leave with their source (dead)
    // and expire after `max_busy_backoff`, so the success path stops
    // taking the lock.
    #[test]
    fn cooling_entries_are_cleared() {
        let (_d, e) = source_tree();
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]);
        let mut o = multi_opts(1, 0);
        o.busy_backoff = Duration::from_millis(1);
        o.max_busy_backoff = Duration::from_millis(5);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        s.cool("a");
        s.cool("b");
        assert_eq!(s.cooling.load(Ordering::Relaxed), 2);
        s.retire(
            &View {
                epoch: 0,
                lineage: 0,
                name: "a".into(),
                generations: vec![],
            },
            "gone".into(),
        );
        assert_eq!(
            s.cooling.load(Ordering::Relaxed),
            1,
            "a dead source's entry leaves"
        );
        std::thread::sleep(Duration::from_millis(30));
        s.served("x");
        assert_eq!(
            s.cooling.load(Ordering::Relaxed),
            0,
            "an expired entry is dropped"
        );
        assert!(s.state.lock().cooling.is_empty());
    }

    fn view(name: &str, epoch: u64) -> Arc<View> {
        Arc::new(View {
            epoch,
            lineage: epoch,
            name: name.into(),
            generations: vec![],
        })
    }

    // The pieces of the replica's-first-open case, one by one (the
    // end-to-end test above no longer reaches these states because each
    // piece blocks it):
    // an alternate whose source was validated by someone else while it
    // was checking it drops its result instead of validating it twice.
    #[test]
    fn an_alternate_validated_meanwhile_is_not_validated_twice() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e), ("b", &e)])
                .slow_first_open("b", Duration::from_millis(300));
            let o = multi_opts(1, 0);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            std::thread::scope(|scope| {
                let h = scope.spawn(|| s.alternate(&[], "a", &HashMap::new()).is_some());
                std::thread::sleep(Duration::from_millis(100));
                s.state.lock().validated.insert("b".into(), view("b", 77));
                assert!(!h.join().expect("join"), "the late validation is dropped");
            });
            assert_eq!(
                s.state.lock().validated.get("b").map(|v| v.epoch),
                Some(77),
                "the first one stands"
            );
        });
    }

    // Selection skips a source another worker is checking, and one
    // already validated (its manifest is never read twice).
    #[test]
    fn selection_skips_sources_being_or_already_validated() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]);
            let mut o = multi_opts(1, 0);
            o.wait = Duration::from_millis(100);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            s.state.lock().validating.insert("a".into());
            assert!(
                matches!(
                    s.select(false, Want::V3OrV2, Ok),
                    Err(FetchError::Unavailable(_))
                ),
                "a is being checked"
            );
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            s.state.lock().validated.insert("a".into(), view("a", 0));
            assert!(
                matches!(
                    s.select(false, Want::V3OrV2, Ok),
                    Err(FetchError::Unavailable(_))
                ),
                "a is validated already"
            );
            assert_eq!(
                fleet.opens_of("a"),
                0,
                "never opened for a second manifest read"
            );
        });
    }

    // A dead current source is not reopened: attach looks elsewhere (here:
    // nowhere, so it ends at once) instead of looping on it.
    #[test]
    fn attach_does_not_reopen_a_dead_current_source() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]).down("a");
            let mut o = multi_opts(1, 0);
            o.wait = Duration::from_secs(10);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            {
                let mut st = s.state.lock();
                st.current = Some(view("a", 0));
                st.dead.insert("a".into(), Dead::Failed("down".into()));
            }
            let started = Instant::now();
            assert!(s.attach(&[]).is_err());
            assert!(
                started.elapsed() < Duration::from_secs(2),
                "{:?}",
                started.elapsed()
            );
        });
    }

    // Selection does not hold the state lock while it
    // pauses between rounds (or reads a manifest): other workers can
    // cool, serve and retire meanwhile.
    #[test]
    fn selection_pauses_without_the_state_lock() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]).busy("a", u32::MAX);
            let mut o = multi_opts(1, 0);
            o.busy_backoff = Duration::from_millis(200);
            o.max_busy_backoff = Duration::from_millis(400);
            o.wait = Duration::from_millis(1500);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            std::thread::scope(|scope| {
                let h = scope.spawn(|| s.select(false, Want::V3OrV2, Ok).is_err());
                std::thread::sleep(Duration::from_millis(100));
                for _ in 0..5 {
                    let t = Instant::now();
                    drop(s.state.lock());
                    assert!(
                        t.elapsed() < Duration::from_millis(50),
                        "the lock was held for {:?}",
                        t.elapsed()
                    );
                    std::thread::sleep(Duration::from_millis(60));
                }
                assert!(h.join().expect("join"), "always busy: Unavailable");
            });
        });
    }

    // Whichever view of a source failed, the source is out.
    #[test]
    fn retire_does_not_depend_on_the_view_epoch() {
        let (_d, e) = source_tree();
        let fleet = Fleet::new(&[("a", &e)]);
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        s.state.lock().validated.insert("a".into(), view("a", 5));
        s.retire(&view("a", 3), "failed".into());
        assert!(s.state.lock().dead.contains_key("a"));
        assert!(!s.state.lock().validated.contains_key("a"));
    }

    // A worker waiting for another one's selection honours the busy
    // bound: a selection that never finishes cannot hold it forever.
    #[test]
    fn attach_waiting_for_a_selection_honours_the_busy_bound() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]);
            let mut o = multi_opts(1, 0);
            o.wait = Duration::from_millis(100);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            // Another worker is selecting and never finishes.
            s.state.lock().selecting = true;
            assert!(matches!(s.attach(&[]), Err(FetchError::Unavailable(_))));
        });
    }

    // An alternate's check of a source whose manifest reads answer busy
    // does not block failover to an idle source.
    #[test]
    fn a_slow_alternate_check_does_not_block_failover() {
        within(Duration::from_secs(60), || {
            let (d, e) = source_tree();
            let c = replica_of(&e, &d.path().join("c"));
            let dd = replica_of(&e, &d.path().join("d"));
            let names: Vec<String> = ["origin", "c", "d"].iter().map(|n| n.to_string()).collect();
            let seed = (0..)
                .find(|&s| source_order(s, names.clone()) == names)
                .expect("seed");
            let fleet = Fleet::new(&[("origin", &e), ("c", &c), ("d", &dd)])
                .busy_ranges("origin", 3, u32::MAX)
                .down_after_busy("origin", 3)
                .busy_fetches("c", 0, u32::MAX);
            let mut o = multi_opts(4, seed);
            o.wait = Duration::from_secs(3);
            let started = Instant::now();
            let r = fetch_from(&fleet, "ckpt/chunks", &d.path().join("dest"), &o);
            eprintln!(
                "result after {:?}: {:?}",
                started.elapsed(),
                r.as_ref().map(|r| r.failovers).map_err(|e| e.to_string())
            );
            eprintln!("d served {}", fleet.served_by("d").len());
            r.expect("idle source d should take the copy");
        });
    }

    // A source whose manifest reads answer busy holds a
    // selection only for a short busy budget, not the whole stall bound:
    // the idle source after it is tried promptly.
    #[test]
    fn a_busy_manifest_does_not_hold_selection() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("c", &["c", "d"]);
            let fleet = Fleet::new(&[("c", &e), ("d", &replica)]).busy_fetches("c", 0, u32::MAX);
            let mut o = multi_opts(2, seed);
            o.wait = Duration::from_secs(3);
            let started = Instant::now();
            let r = fetch_from(&fleet, "ckpt/chunks", &d.path().join("dest"), &o).expect("from d");
            assert_eq!(r.source, "d");
            assert!(
                started.elapsed() < Duration::from_millis(1500),
                "{:?}",
                started.elapsed()
            );
        });
    }

    // A worker that needs a new source does not wait on a
    // check of some other source: it selects among the rest.
    #[test]
    fn attach_does_not_wait_on_a_check_of_another_source() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let seed = seed_putting_first("c", &["c", "d"]);
            let fleet = Fleet::new(&[("c", &e), ("d", &e)]);
            let mut o = multi_opts(1, seed);
            o.wait = Duration::from_secs(3);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            s.state.lock().validating.insert("c".into());
            let started = Instant::now();
            match s.attach(&[]) {
                Ok(Attached::Conn(conn)) => assert_eq!(conn.view.name, "d"),
                Ok(Attached::Busy(n)) => panic!("busy {n}"),
                Err(e) => panic!("{e}"),
            }
            assert!(
                started.elapsed() < Duration::from_secs(1),
                "{:?}",
                started.elapsed()
            );
        });
    }

    // The names of one server leave together when a file
    // changes there, and do not count as another source.
    #[test]
    fn a_change_takes_out_every_name_of_the_server() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a4", &e), ("a6", &e), ("b", &e)])
            .identity("a4", "srvA")
            .identity("a6", "srvA")
            .identity("b", "srvB");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        assert!(s.changed(&view("a4", 0), "f"), "b is another server");
        assert!(s.is_dead("a4") && s.is_dead("a6") && !s.is_dead("b"));
        let fleet = Fleet::new(&[("a4", &e), ("a6", &e)])
            .identity("a4", "srvA")
            .identity("a6", "srvA");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        assert!(
            !s.changed(&view("a4", 0), "f"),
            "a6 is the same server: no other source"
        );
    }

    // A claim (selecting, checking a source) is cleared
    // even when the code holding it panics.
    #[test]
    fn claims_are_cleared_on_panic() {
        let (_d, e) = source_tree();
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]);
        fleet.state.panic_on_open.lock().insert("b".into());
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        let alt = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
            s.alternate(&[], "a", &HashMap::new())
        }));
        assert!(alt.is_err(), "the open panicked");
        assert!(
            s.state.lock().validating.is_empty(),
            "the check's claim was cleared"
        );
        fleet.state.panic_on_open.lock().insert("a".into());
        let at = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| s.attach(&[]).is_ok()));
        assert!(at.is_err(), "the selection panicked");
        let st = s.state.lock();
        assert!(
            !st.selecting && st.validating.is_empty(),
            "the selection's claims were cleared"
        );
    }

    // Names are resolved outside the state lock: slow
    // DNS in a worker looking for an alternate does not stall the others.
    #[test]
    fn alternate_resolves_names_outside_the_lock() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]);
            *fleet.state.slow_candidates.lock() = Some(Duration::from_millis(300));
            let o = multi_opts(1, 0);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            std::thread::scope(|scope| {
                let h = scope.spawn(|| s.alternate(&[], "a", &HashMap::new()).is_none());
                std::thread::sleep(Duration::from_millis(100));
                let t = Instant::now();
                drop(s.state.lock());
                assert!(
                    t.elapsed() < Duration::from_millis(100),
                    "the lock was held for {:?}",
                    t.elapsed()
                );
                assert!(h.join().expect("join"));
            });
        });
    }

    // Once a source is marked dead (here: one connection
    // to it fails for good), the other workers stop reading from it at
    // their next range boundary.
    #[test]
    fn workers_leave_a_dead_source_at_the_next_range() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            // The second connection to the origin (a worker's own) fails
            // every request; the others keep working.
            let fleet =
                Fleet::new(&[("origin", &e), ("replica", &replica)]).broken_conn("origin", 1);
            let dest = d.path().join("dest");
            let mut o = multi_opts(4, seed);
            o.range_size = 4096;
            fetch_from(&fleet, "ckpt", &dest, &o).expect("fetch");
            assert_same_tree(&d.path().join("export/ckpt"), &dest);
            let at = fleet
                .state
                .released_at
                .lock()
                .iter()
                .find(|(n, _)| n == "origin")
                .map(|x| x.1)
                .expect("the origin was given up");
            let after = fleet.state.log.lock()[at..]
                .iter()
                .filter(|(n, ..)| n == "origin")
                .count();
            assert!(
                after <= 4,
                "{after} ranges read from the origin after it was given up"
            );
        });
    }

    // Two names of one server (IPv4 and IPv6) are one
    // source: a file changed there is Changed, not a move to the "other".
    #[test]
    fn names_of_one_server_are_one_source() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let seed = seed_putting_first("a4", &["a4", "a6"]);
            let path = d.path().join("export/ckpt/chunks/b.bin");
            let same = std::fs::read(&path).expect("read");
            // Rewritten with the same bytes: the generation moves, the
            // content does not; a6 would validate and "finish" the copy.
            let fleet = Fleet::new(&[("a4", &e), ("a6", &e)])
                .identity("a4", "srvA")
                .identity("a6", "srvA")
                .change_after("a4", 7, path, &same);
            let err = fetch_from(
                &fleet,
                "ckpt/chunks",
                &d.path().join("dest"),
                &multi_opts(1, seed),
            )
            .expect_err("the only server changed");
            assert!(
                matches!(err, FetchError::Changed { ref path } if path == "ckpt/chunks/b.bin"),
                "{err}"
            );
        });
    }

    // When the sources left after a change cannot finish
    // the copy, the fetch reports the change, not their failure.
    #[test]
    fn a_change_is_the_reason_when_no_other_source_can_finish() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let path = d.path().join("export/ckpt/chunks/b.bin");
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                .change_after("origin", 7, path, b"rewritten on the origin")
                .down("replica");
            let err = fetch_from(
                &fleet,
                "ckpt/chunks",
                &d.path().join("dest"),
                &multi_opts(1, seed),
            )
            .expect_err("the replica is down");
            assert!(
                matches!(err, FetchError::Changed { ref path } if path == "ckpt/chunks/b.bin"),
                "{err}"
            );
        });
    }

    // A dead name whose address now reaches another
    // server (a reused pod address) is tried again; the same server stays
    // dead.
    #[test]
    fn a_new_server_at_a_dead_address_is_tried_again() {
        let (_d, e) = source_tree();
        let mut o = multi_opts(1, 0);
        o.wait = Duration::from_millis(300);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        for (now, alive) in [("srv2", true), ("srv1", false)] {
            let fleet = Fleet::new(&[("a", &e)]).identity("a", now);
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            {
                let mut st = s.state.lock();
                st.dead.insert("a".into(), Dead::Failed("down".into()));
                st.dead_at.insert(
                    "a".into(),
                    (Some("srv1".into()), Instant::now() - Duration::from_secs(1)),
                );
            }
            let got = s.select(false, Want::V3OrV2, Ok);
            assert_eq!(
                matches!(got, Ok(Selected::New(_, ref n, _)) if n == "a"),
                alive,
                "identity now {now}"
            );
        }
    }

    fn dead_long_ago(s: &Sources<'_, Fleet>, name: &str, why: Dead, identity: &str) {
        let mut st = s.state.lock();
        st.dead.insert(name.into(), why);
        st.dead_at.insert(
            name.into(),
            (
                Some(identity.into()),
                Instant::now() - Duration::from_secs(1),
            ),
        );
    }

    // A source that died serving corrupt bytes is never
    // revived, and a name is revived at most MAX_REVIVALS times.
    #[test]
    fn integrity_failures_are_never_revived_and_revivals_are_capped() {
        let (_d, e) = source_tree();
        let mut o = multi_opts(1, 0);
        o.wait = Duration::from_millis(200);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e)]).identity("a", "srv2");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        dead_long_ago(&s, "a", Dead::Corrupt("crc".into()), "srv1");
        assert!(
            s.select(false, Want::V3OrV2, Ok).is_err(),
            "corrupt: never revived"
        );
        assert_eq!(fleet.opens_of("a"), 0);
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        dead_long_ago(&s, "a", Dead::Failed("down".into()), "srv1");
        s.state.lock().revivals.insert("a".into(), MAX_REVIVALS);
        assert!(
            s.select(false, Want::V3OrV2, Ok).is_err(),
            "revived MAX_REVIVALS times already"
        );
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        dead_long_ago(&s, "a", Dead::Failed("down".into()), "srv1");
        let before = s.dead_count.load(Ordering::Relaxed);
        assert!(
            matches!(s.select(false, Want::V3OrV2, Ok), Ok(Selected::New(..))),
            "a failed source on a new server is"
        );
        assert!(
            s.dead_count.load(Ordering::Relaxed) > before,
            "the revival is signalled to the workers"
        );
    }

    // After a revival, connections to the old server at
    // that name are dropped at the next range, a stale view's failure does
    // not retire the new server, and alternate() only reuses a connection
    // of the validated view.
    #[test]
    fn a_stale_view_of_a_revived_source_is_dropped_and_harmless() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]);
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        s.state.lock().validated.insert("a".into(), view("a", 5));
        s.state.lock().revivals.insert("a".into(), 1);
        // The revived server's lineage is 5 (`view` uses the epoch).
        s.state.lock().lineages.insert("name:a".into(), 5);
        let conn = |n: &str, epoch| match fleet.open(n) {
            Ok(src) => Conn {
                src,
                view: view(n, epoch),
            },
            Err(e) => panic!("open {n}: {e}"),
        };
        let mut conns = HashMap::from([("a".to_string(), conn("a", 3))]);
        let mut seen = 0;
        s.dead_count.fetch_add(1, Ordering::Relaxed); // the revival
        s.prune(&mut seen, &mut conns);
        assert!(conns.is_empty(), "the old server's connection is dropped");
        let mut conns = HashMap::from([("a".to_string(), conn("a", 5))]);
        s.dead_count.fetch_add(1, Ordering::Relaxed);
        s.prune(&mut seen, &mut conns);
        assert_eq!(conns.len(), 1, "the validated view's connection stays");
        s.retire(&view("a", 3), "old server failed".into());
        assert!(
            !s.is_dead("a"),
            "a stale view does not retire the new server"
        );
        // The new server's view was given a fresh epoch since (a bad
        // source elsewhere): its older view is still the new server.
        s.state.lock().validated.insert(
            "a".into(),
            Arc::new(View {
                epoch: 7,
                lineage: 5,
                name: "a".into(),
                generations: vec![],
            }),
        );
        s.retire(&view("a", 5), "new server failed".into());
        assert!(
            s.is_dead("a"),
            "a view made after the revival is the new server"
        );
        // alternate(): a held connection to an older view of b is not used.
        s.state.lock().validated.insert("b".into(), view("b", 2));
        let held = HashMap::from([("b".to_string(), conn("b", 1))]);
        assert!(matches!(s.alternate(&[], "a", &held), Some(Alt::New(_))));
    }

    // A worker still on the old server's view of a revived
    // name reports bad data. That view is stale (the new server stays),
    // but the new server's view is below the refetch bar unless it is
    // renewed too: its writes would be refused forever.
    #[test]
    fn a_stale_bad_view_renews_the_revived_server() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]);
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        {
            let mut st = s.state.lock();
            let v = view("a", 5);
            st.validated.insert("a".into(), Arc::clone(&v));
            st.current = Some(v);
            st.revivals.insert("a".into(), 1);
            st.lineages.insert("name:a".into(), 5);
            st.next_epoch = 6;
            st.next_lineage = 6;
        }
        s.retire_as(&view("a", 3), Dead::Corrupt("truncated".into()));
        let bar = s.bar_after(3);
        let st = s.state.lock();
        assert!(!st.dead.contains_key("a"), "stale: the new server stays");
        let cur = st.current.clone().expect("current");
        assert!(
            cur.epoch >= bar,
            "current a@{} is below the refetch bar {bar}",
            cur.epoch
        );
        assert_eq!(cur.lineage, 5, "still the new server");
    }

    // A source renewed after another one served bad data
    // has views of two epochs. Bad data reported through either bans the
    // source (its lineage), so no view of it writes again, and the files
    // any of its views wrote are refetched.
    #[test]
    fn a_bad_source_is_banned_in_every_view_of_it() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]);
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        {
            let mut st = s.state.lock();
            // Epochs apart from lineages, so the test tells them apart.
            st.next_epoch = 10;
            let a = st.new_view("name:a".into(), "a".into(), vec![]);
            st.validated.insert("a".into(), Arc::clone(&a));
            st.current = Some(a);
            let b = st.new_view("name:b".into(), "b".into(), vec![]);
            st.validated.insert("b".into(), b);
        }
        let b = s.state.lock().validated.get("b").cloned().expect("b");
        let old_a = s.state.lock().validated.get("a").cloned().expect("a");
        s.retire_as(&b, Dead::Corrupt("b truncated".into()));
        let fresh = s
            .state
            .lock()
            .validated
            .get("a")
            .cloned()
            .expect("a validated");
        assert!(
            fresh.epoch > old_a.epoch && fresh.epoch >= s.bar_after(b.lineage),
            "a renewed past the bar"
        );
        assert_eq!(fresh.lineage, old_a.lineage, "the same server");
        // A worker not yet pruned, still on the old view, finds bad data.
        s.retire_as(&old_a, Dead::Corrupt("a truncated".into()));
        assert!(s.is_dead("a"));
        assert!(
            s.banned.read().contains(&fresh.lineage),
            "the renewed view is banned too"
        );
        assert_eq!(
            lineage_bit(fresh.lineage),
            lineage_bit(old_a.lineage),
            "one writer bit for both views"
        );
    }

    // One server reached by two names (its IPv4 and IPv6
    // addresses) is one lineage. Bad data through one name bans the
    // server and takes out its other name too, so nothing of it lands
    // through the other, and the files either name wrote are refetched.
    #[test]
    fn bad_data_takes_out_every_name_of_the_server() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a4", &e), ("a6", &e), ("b", &e)])
            .identity("a4", "srvA")
            .identity("a6", "srvA")
            .identity("b", "srvB");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        let (a4, a6) = {
            let mut st = s.state.lock();
            let add = |st: &mut SelState, n: &str| {
                let v = st.new_view(s.server_key(n), n.into(), vec![]);
                st.validated.insert(n.into(), Arc::clone(&v));
                v
            };
            let a4 = add(&mut st, "a4");
            st.current = Some(Arc::clone(&a4));
            let a6 = add(&mut st, "a6");
            add(&mut st, "b");
            (a4, a6)
        };
        assert_eq!(a4.lineage, a6.lineage, "one server, one lineage");
        s.retire_as(&a4, Dead::Corrupt("truncated".into()));
        assert!(s.banned.read().contains(&a6.lineage));
        assert!(
            s.is_dead("a4") && s.is_dead("a6"),
            "every name of the bad server is out"
        );
        assert!(!s.is_dead("b"));
        assert!(
            matches!(s.state.lock().dead.get("a6"), Some(Dead::Corrupt(_))),
            "never revived"
        );
    }

    // A worker still on the old server's view of a revived
    // name sees a generation change (its connection now reaches the new
    // server): the new server is not taken out for it.
    #[test]
    fn a_stale_change_does_not_take_out_the_revived_server() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]);
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        {
            let mut st = s.state.lock();
            st.validated.insert("a".into(), view("a", 5));
            st.revivals.insert("a".into(), 1);
            st.lineages.insert("name:a".into(), 5);
            st.next_epoch = 6;
            st.next_lineage = 6;
        }
        assert!(s.changed(&view("a", 3), "f"));
        assert!(!s.is_dead("a"), "the revived server stays");
        assert!(
            s.state.lock().changed.is_empty(),
            "not recorded as a change"
        );
    }

    // A second name of a server that served bad data,
    // opened only after the ban (its identity is learned at open), is
    // never made a source: a view of a banned server would have every
    // write refused and be handed out again forever.
    #[test]
    fn an_alias_opened_after_the_ban_is_never_used() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a4", &e), ("a6", &e)]).identity("a4", "srvA");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        let a4 = {
            let mut st = s.state.lock();
            let v = st.new_view(s.server_key("a4"), "a4".into(), vec![]);
            st.validated.insert("a4".into(), Arc::clone(&v));
            st.current = Some(Arc::clone(&v));
            v
        };
        s.retire_as(&a4, Dead::Corrupt("truncated".into()));
        // RdmaSources learns a6's identity only when a6 is first opened.
        fleet
            .state
            .identity
            .lock()
            .insert("a6".into(), "srvA".into());
        match s.attach(&[]) {
            Ok(Attached::Conn(c)) => assert!(!s.banned.read().contains(&c.view.lineage),
                "current view is of the banned server: every write refused, attach returns it again forever"),
            Ok(Attached::Busy(_)) => panic!("busy"),
            Err(_) => {}
        }
        assert!(
            matches!(s.state.lock().dead.get("a6"), Some(Dead::Corrupt(_))),
            "the alias is out as Corrupt"
        );
    }

    // A revived name's new server has its lineage from
    // the revival on, so a failure of a connection to the old server
    // while the new one is checked is stale and does not take it out.
    #[test]
    fn a_stale_failure_while_the_revived_server_is_checked_is_ignored() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]).identity("a", "srvY");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        let old = {
            let mut st = s.state.lock();
            st.lineages.insert("id:srvX".into(), 0);
            st.next_lineage = 1;
            st.next_epoch = 1;
            Arc::new(View {
                epoch: 0,
                lineage: 0,
                name: "a".into(),
                generations: vec![],
            })
        };
        dead_long_ago(&s, "a", Dead::Failed("down".into()), "srvX");
        assert!(s.reprobe("a").is_some(), "srvY revives a");
        // Before select's check of srvY makes its view, a worker's
        // connection to srvX (old view) fails.
        s.retire(&old, "old server's connection broke".into());
        assert!(
            !s.is_dead("a"),
            "a stale view of srvX took out the revived srvY"
        );
    }

    // Only revival makes a view stale. A live name whose
    // address comes to reach another known server still has its failures
    // counted, so its view is never kept as current while failing.
    #[test]
    fn a_live_name_is_never_stale() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)])
            .identity("a", "srvA")
            .identity("b", "srvB");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        let a = {
            let mut st = s.state.lock();
            let a = st.new_view(s.server_key("a"), "a".into(), vec![]);
            st.validated.insert("a".into(), Arc::clone(&a));
            st.current = Some(Arc::clone(&a));
            let b = st.new_view(s.server_key("b"), "b".into(), vec![]);
            st.validated.insert("b".into(), b);
            a
        };
        // a's address now reaches srvB (re-anchored, not revived).
        fleet
            .state
            .identity
            .lock()
            .insert("a".into(), "srvB".into());
        s.retire(&a, "failed".into());
        assert!(s.is_dead("a"), "a live name's failure counts");
    }

    // A refused write seen while a ban is still being
    // recorded does not retire the source with the backstop's generic
    // reason before the retirement with the real one.
    #[test]
    fn the_backstop_never_replaces_the_real_reason() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)])
            .identity("a", "srvA")
            .identity("b", "srvB");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        let a = {
            let mut st = s.state.lock();
            let a = st.new_view(s.server_key("a"), "a".into(), vec![]);
            st.validated.insert("a".into(), Arc::clone(&a));
            st.current = Some(Arc::clone(&a));
            let b = st.new_view(s.server_key("b"), "b".into(), vec![]);
            st.validated.insert("b".into(), b);
            a
        };
        // W1 published the ban (first statement of retire_as) ...
        s.banned.write().insert(a.lineage);
        // ... W2's write is refused and its backstop takes the lock first.
        s.retire_banned(&a);
        assert!(!s.is_dead("a"), "left to the retirement recording the ban");
        s.retire_as(&a, Dead::Corrupt("f at 0: crc mismatch".into()));
        let d = s.state.lock().dead.get("a").cloned();
        assert!(
            matches!(&d, Some(Dead::Corrupt(w)) if w.contains("crc")),
            "real reason lost"
        );
        // Once recorded, the backstop retires other views of the server.
        let a2 = s.state.lock().renew(&a);
        s.retire_banned(&a2);
        assert!(
            matches!(s.state.lock().dead.get("a"), Some(Dead::Corrupt(w)) if w.contains("crc"))
        );
    }

    // Bad data seen through a stale view (the server that
    // used to answer at a revived name) takes out that server's other
    // names, so none is handed out again.
    #[test]
    fn bad_data_through_a_stale_view_takes_out_the_old_servers_other_names() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("x", &e), ("b", &e)])
            .identity("a", "srvX")
            .identity("x", "srvX")
            .identity("b", "srvB");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        let old_a = {
            let mut st = s.state.lock();
            let old_a = st.new_view(s.server_key("a"), "a".into(), vec![]);
            let x = st.new_view(s.server_key("x"), "x".into(), vec![]);
            st.validated.insert("x".into(), x);
            let b = st.new_view(s.server_key("b"), "b".into(), vec![]);
            st.validated.insert("b".into(), Arc::clone(&b));
            st.current = Some(b);
            old_a
        };
        // a died and was revived on srvY.
        fleet
            .state
            .identity
            .lock()
            .insert("a".into(), "srvY".into());
        {
            let mut st = s.state.lock();
            st.revivals.insert("a".into(), 1);
            st.lineage("id:srvY".into());
        }
        // A worker still on old_a (srvX) gets a CRC failure.
        s.retire_as(&old_a, Dead::Corrupt("crc".into()));
        // b fails; attach must not hand out x (srvX, banned).
        let b = s.state.lock().validated.get("b").cloned().expect("b");
        s.retire(&b, "down".into());
        if let Ok(Attached::Conn(c)) = s.attach(&[]) {
            assert!(
                !s.banned.read().contains(&c.view.lineage),
                "handed out {} of a banned server",
                c.view.name
            );
        }
        assert!(s.is_dead("x"), "the old server's other name is out");
        assert!(!s.is_dead("a"), "the revived name (another server) stays");
    }

    // Bad data seen only through a stale view marks no
    // name dead (the name is another server now), yet the fetch, if it
    // cannot finish, still ends as an integrity failure naming it.
    #[test]
    fn bad_data_seen_only_through_a_stale_view_still_ends_as_corrupt() {
        let (_d, e) = source_tree();
        let o = multi_opts(1, 0);
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)])
            .identity("a", "srvX")
            .identity("b", "srvB");
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        let old_a = {
            let mut st = s.state.lock();
            let old_a = st.new_view(s.server_key("a"), "a".into(), vec![]);
            let b = st.new_view(s.server_key("b"), "b".into(), vec![]);
            st.validated.insert("b".into(), Arc::clone(&b));
            st.current = Some(b);
            old_a
        };
        fleet
            .state
            .identity
            .lock()
            .insert("a".into(), "srvY".into());
        {
            let mut st = s.state.lock();
            st.revivals.insert("a".into(), 1);
            st.lineage("id:srvY".into());
            let a = st.new_view(s.server_key("a"), "a".into(), vec![]);
            st.validated.insert("a".into(), a);
        }
        s.retire_as(&old_a, Dead::Corrupt("f at 0: crc mismatch".into()));
        assert!(s.banned.read().contains(&old_a.lineage));
        let (a, b) = {
            let st = s.state.lock();
            (
                st.validated.get("a").cloned().expect("a"),
                st.validated.get("b").cloned().expect("b"),
            )
        };
        s.retire(&a, "down".into());
        s.retire(&b, "down".into());
        let err = Sources::<Fleet>::exhausted(&s.state.lock());
        assert!(
            matches!(&err, FetchError::Corrupt(m) if m.contains("crc mismatch")),
            "bad data forgotten: {err}"
        );
    }

    // Reprobes of black-holed dead names are bounded: at
    // most REPROBES_PER_ROUND a round, each REPROBE_DIAL long, none past
    // the busy bound.
    #[test]
    fn reprobes_of_black_holed_names_are_bounded() {
        within(Duration::from_secs(60), || {
            let (_d, e) = source_tree();
            let names = ["d1", "d2", "d3", "d4", "d5"];
            let exports: Vec<(&str, &Arc<DirExport>)> = names.iter().map(|n| (*n, &e)).collect();
            let fleet = Fleet::new(&exports);
            fleet
                .state
                .black_hole
                .lock()
                .extend(names.iter().map(|n| n.to_string()));
            let mut o = multi_opts(1, 0);
            o.wait = Duration::from_millis(300);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            for n in names {
                dead_long_ago(&s, n, Dead::Failed("down".into()), "old");
            }
            let started = Instant::now();
            assert!(s.select(false, Want::V3OrV2, Ok).is_err());
            // One reprobe at most, and its dial capped by the 300 ms bound.
            assert!(
                started.elapsed() < Duration::from_millis(800),
                "{:?}",
                started.elapsed()
            );
        });
    }

    // After a change, a failure of sources that were left
    // but busy is reported as what it is (with the change mentioned).
    #[test]
    fn a_busy_failure_after_a_change_is_reported_as_busy() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let path = d.path().join("export/ckpt/chunks/b.bin");
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
                .change_after("origin", 7, path, b"rewritten on the origin")
                .busy_ranges("replica", 0, u32::MAX);
            let mut o = multi_opts(1, seed);
            o.wait = Duration::from_millis(300);
            let err = fetch_from(&fleet, "ckpt/chunks", &d.path().join("dest"), &o)
                .expect_err("replica busy");
            assert!(
                matches!(err, FetchError::Unavailable(ref m) if m.contains("changed on origin")),
                "{err}"
            );
        });
    }

    // A queued release does not drop a newer anchor of the
    // name (opened after it was given up), and a failed open that puts a
    // source out releases it once, not twice.
    #[test]
    fn queued_releases_spare_a_newer_anchor_and_never_double() {
        let (_d, e) = source_tree();
        let mut o = multi_opts(1, 0);
        o.max_retries = 0;
        let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]);
        let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
        {
            let mut st = s.state.lock();
            s.mark_dead(&mut st, "a", Dead::Failed("down".into()));
            // Not released yet: still queued while the lock is held.
        }
        drop(fleet.open("a")); // revived and opened again meanwhile
        s.unlock(s.state.lock());
        assert!(
            !fleet.state.released.lock().contains(&"a".to_string()),
            "the newer anchor is kept"
        );
        fleet.state.down.lock().insert("b".into());
        assert!(s.check("b", None, Want::V3OrV2, &mut Ok).is_err());
        assert!(s.is_dead("b"));
        let released = fleet
            .state
            .released
            .lock()
            .iter()
            .filter(|n| *n == "b")
            .count();
        assert_eq!(released, 1, "released once");
    }

    // A source whose version 3 listing answers busy is
    // busy: under Want::V3 it is neither refused as serving version 2
    // only nor accepted through its version 2 listing; once it answers,
    // its version 3 listing is taken.
    #[test]
    fn a_busy_v3_listing_is_busy_not_v2_only() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]);
            fleet
                .state
                .v3_busy_until
                .lock()
                .insert("a".into(), Instant::now() + Duration::from_secs(3600));
            let mut o = multi_opts(1, 0);
            o.max_busy_backoff = Duration::from_millis(50);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            let got = s.check("a", None, Want::V3, &mut Ok);
            let why = match &got {
                Err(Some(
                    Dead::Manifest(w)
                    | Dead::Mismatch(w)
                    | Dead::Failed(w)
                    | Dead::Corrupt(w)
                    | Dead::Refused(w),
                )) => w.clone(),
                Err(None) => String::new(),
                Ok(_) => "accepted".into(),
            };
            assert!(matches!(got, Err(None)), "busy, not out: {why}");
            assert!(
                !s.is_dead("a") && s.state.lock().cooling.contains_key("a"),
                "cooled, not dead"
            );
            // It answers after a few busy rounds: version 3.
            fleet
                .state
                .v3_busy_until
                .lock()
                .insert("a".into(), Instant::now() + Duration::from_millis(10));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            match s.check("a", None, Want::V3, &mut Ok) {
                Ok((_, m)) => assert_eq!(m.version, manifest::VERSION),
                Err(e) => panic!("{:?}", e.map(|_| ())),
            }
        });
    }

    // The version 3 and version 2 listing reads of one
    // check share one busy budget.
    #[test]
    fn one_check_waits_at_most_one_busy_budget() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e)]).old("a");
            // Busy on version 3 for most of the budget, then NOT_FOUND (an
            // older export); its version 2 listing stays busy.
            fleet
                .state
                .v3_busy_until
                .lock()
                .insert("a".into(), Instant::now() + Duration::from_millis(150));
            fleet.state.v2_busy.lock().insert("a".into());
            let mut o = multi_opts(1, 0);
            o.max_busy_backoff = Duration::from_millis(200);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            let started = Instant::now();
            assert!(matches!(
                s.check("a", None, Want::V3OrV2, &mut Ok),
                Err(None)
            ));
            assert!(
                started.elapsed() < Duration::from_millis(300),
                "{:?}: two budgets",
                started.elapsed()
            );
        });
    }

    // Releasing an anchor (a connection teardown) runs
    // outside the state lock, for a retired source and for a reprobe.
    #[test]
    fn anchors_are_released_outside_the_state_lock() {
        within(Duration::from_secs(30), || {
            let (_d, e) = source_tree();
            let fleet = Fleet::new(&[("a", &e), ("b", &e)]).identity("b", "same");
            *fleet.state.slow_release.lock() = Some(Duration::from_millis(300));
            let mut o = multi_opts(1, 0);
            o.wait = Duration::from_millis(200);
            let (r, c) = (AtomicU64::new(0), AtomicU64::new(0));
            let s = Sources::new(&fleet, "ckpt", &o, &r, &c);
            s.state.lock().validated.insert("a".into(), view("a", 0));
            let probe_lock = |s: &Sources<'_, Fleet>| {
                std::thread::sleep(Duration::from_millis(100));
                let t = Instant::now();
                drop(s.state.lock());
                t.elapsed()
            };
            std::thread::scope(|scope| {
                let h = scope.spawn(|| s.retire(&view("a", 0), "down".into()));
                assert!(
                    probe_lock(&s) < Duration::from_millis(100),
                    "retire held the lock while releasing"
                );
                h.join().expect("join");
            });
            dead_long_ago(&s, "b", Dead::Failed("down".into()), "same");
            std::thread::scope(|scope| {
                let h = scope.spawn(|| s.reprobe("b").is_none());
                assert!(
                    probe_lock(&s) < Duration::from_millis(100),
                    "reprobe held the lock while releasing"
                );
                assert!(h.join().expect("join"), "the same server stays dead");
            });
        });
    }

    // A file that changes on one of two sources moves
    // the copy to the other instead of failing it.
    #[test]
    fn a_file_changed_on_one_source_moves_the_copy_to_another() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let replica_dir = d.path().join("replica");
            let replica = replica_of(&e, &replica_dir);
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let changing = d.path().join("export/ckpt/chunks/b.bin");
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)]).change_after(
                "origin",
                7,
                changing,
                b"rewritten on the origin",
            );
            let dest = d.path().join("dest");
            let r = fetch_from(&fleet, "ckpt/chunks", &dest, &multi_opts(1, seed))
                .expect("completed elsewhere");
            assert!(r.failovers >= 1);
            assert_eq!(
                r.changed_on,
                ["origin"],
                "the report says where the file changed"
            );
            assert_same_tree(&replica_dir.join("ckpt/chunks"), &dest);
        });
    }

    // With --wait-s 0, a busy answer still moves the
    // range to an idle source instead of ending the fetch.
    #[test]
    fn a_zero_wait_still_moves_work_to_an_idle_source() {
        within(Duration::from_secs(30), || {
            let (d, e) = source_tree();
            let replica = replica_of(&e, &d.path().join("replica"));
            let seed = seed_putting_first("origin", &["origin", "replica"]);
            let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)]).busy_ranges(
                "origin",
                2,
                u32::MAX,
            );
            let mut o = multi_opts(1, seed);
            o.wait = Duration::ZERO;
            let dest = d.path().join("dest");
            fetch_from(&fleet, "ckpt/chunks", &dest, &o).expect("moved to the replica");
            assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
        });
    }

    #[test]
    fn an_unreachable_source_is_given_up_after_the_retry_budget() {
        let (d, e) = source_tree();
        let fleet = Fleet::new(&[("a", &e)]).down("a");
        let err = fetch_from(&fleet, "ckpt", &d.path().join("dest"), &multi_opts(2, 0))
            .expect_err("down");
        assert!(
            matches!(err, FetchError::Connect(ref m) if m.contains("refused")),
            "{err}"
        );
        // With another source up, the dead one (tried first) is skipped.
        let fleet = Fleet::new(&[("a", &e), ("b", &e)]).down("a");
        let seed = seed_putting_first("a", &["a", "b"]);
        let r = fetch_from(
            &fleet,
            "ckpt",
            &d.path().join("dest2"),
            &multi_opts(2, seed),
        )
        .expect("b");
        assert_eq!(r.source, "b");
        assert!(
            fleet.state.released.lock().contains(&"a".to_string()),
            "a failed source is released"
        );
    }

    #[test]
    fn a_source_dying_mid_copy_hands_the_rest_to_another() {
        let (d, e) = source_tree();
        let replica = replica_of(&e, &d.path().join("replica"));
        let seed = seed_putting_first("origin", &["origin", "replica"]);
        // One worker, ranges in path order: a.bin (6 ranges) completes,
        // then the origin dies during b.bin (after 2 of its 4 ranges).
        let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)]).kill_after("origin", 8);
        let dest = d.path().join("dest");
        let r = fetch_from(&fleet, "ckpt/chunks", &dest, &multi_opts(1, seed)).expect("fetch");
        assert_eq!((r.source.as_str(), r.failovers), ("origin", 1));
        assert_same_tree(&d.path().join("export/ckpt/chunks"), &dest);
        let from_replica = fleet.served_by("replica");
        // The completed file stays; the half-done one resumes where it
        // stopped, under the replica's own generations.
        assert!(
            from_replica.iter().all(|(k, _)| k != "ckpt/chunks/a.bin"),
            "{from_replica:?}"
        );
        let b: Vec<u64> = from_replica
            .iter()
            .filter(|(k, _)| k == "ckpt/chunks/b.bin")
            .map(|x| x.1)
            .collect();
        assert_eq!(b, [2 * 65536, 3 * 65536]);
        assert!(fleet.state.released.lock().contains(&"origin".to_string()));

        // Many workers: same outcome, whatever each was doing.
        let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)]).kill_after("origin", 20);
        let dest = d.path().join("dest-many");
        let r = fetch_from(&fleet, "ckpt", &dest, &multi_opts(8, seed)).expect("fetch");
        assert_eq!(r.failovers, 1);
        assert_same_tree(&d.path().join("export/ckpt"), &dest);
    }

    #[test]
    fn a_source_with_other_content_is_never_used() {
        let (d, e) = source_tree();
        let seed = seed_putting_first("origin", &["origin", "other"]);
        // A copy that is not from this origin (plain cp): same bytes, but
        // nothing vouches for them, so its identities are its own.
        let other_dir = d.path().join("other");
        copy_tree(&d.path().join("export"), &other_dir);
        let other = DirExport::open(&other_dir).expect("other");
        let fleet = Fleet::new(&[("origin", &e), ("other", &other)]).kill_after("origin", 8);
        let dest = d.path().join("dest");
        let err =
            fetch_from(&fleet, "ckpt/chunks", &dest, &multi_opts(1, seed)).expect_err("mismatch");
        assert!(
            matches!(err, FetchError::Mismatch(ref m) if m.contains("ckpt/chunks/b.bin")),
            "{err}"
        );
        assert!(
            fleet.served_by("other").is_empty(),
            "no byte comes from a mismatched source"
        );
        assert!(fleet.state.released.lock().contains(&"other".to_string()));
        // a.bin was complete before the origin died and stays; the
        // half-done b.bin never appears.
        assert_eq!(
            std::fs::read(dest.join("a.bin")).expect("a"),
            std::fs::read(d.path().join("export/ckpt/chunks/a.bin")).expect("a")
        );
        assert!(!dest.join("b.bin").exists());
        assert_no_staging(&dest);

        // A replica that changed a file after its copy: that file no
        // longer matches; the others would.
        let replica_dir = d.path().join("replica");
        let replica = replica_of(&e, &replica_dir);
        std::thread::sleep(Duration::from_millis(10));
        let b = replica_dir.join("ckpt/chunks/b.bin");
        let bytes = std::fs::read(&b).expect("b");
        std::fs::write(&b, &bytes).expect("rewrite, same bytes");
        let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)]).kill_after("origin", 8);
        let dest = d.path().join("dest2");
        let err =
            fetch_from(&fleet, "ckpt/chunks", &dest, &multi_opts(1, seed)).expect_err("mismatch");
        let FetchError::Mismatch(m) = &err else {
            panic!("{err}")
        };
        assert!(m.contains("b.bin") && !m.contains("c.bin"), "{m}");
    }

    fn copy_tree(from: &Path, to: &Path) {
        let mut files = Vec::new();
        walk(from, from, &mut files);
        for (rel, bytes) in files {
            let p = to.join(rel);
            std::fs::create_dir_all(p.parent().expect("parent")).expect("mkdir");
            std::fs::write(p, bytes).expect("write");
        }
    }

    #[test]
    fn a_fetched_copy_records_content_identities() {
        let (d, e) = source_tree();
        let dir = d.path().join("replica");
        let replica = replica_of(&e, &dir);
        let origin = files_of(&read_all(&e, &manifest::key("ckpt")));
        let copy = files_of(&read_all(&replica, &manifest::key("ckpt")));
        assert_eq!(origin.len(), copy.len());
        for (o, c) in origin.iter().zip(&copy) {
            assert_eq!((&o.path, o.size, o.content), (&c.path, c.size, c.content));
            assert_ne!(o.generation, c.generation, "generations are per node");
        }
        assert_eq!(
            manifest::content_digest(&origin),
            manifest::content_digest(&copy)
        );
        // Served under a prefix, a replica of a prefix answers for it.
        let sub = d.path().join("sub");
        fetch_dir(
            source(&e, 65536, vec![]),
            "ckpt/chunks",
            &sub,
            &opts(2, 65536),
        )
        .expect("sub");
        let as_prefix = DirExport::open_as(&sub, "ckpt/chunks").expect("open_as");
        let o = files_of(&read_all(&e, &manifest::key("ckpt/chunks")));
        let c = files_of(&read_all(&as_prefix, &manifest::key("ckpt/chunks")));
        assert_eq!(manifest::content_digest(&o), manifest::content_digest(&c));
    }

    fn files_of(bytes: &[u8]) -> Vec<Entry> {
        manifest::decode(bytes)
            .expect("decode")
            .files()
            .cloned()
            .collect()
    }

    fn read_all(e: &DirExport, key: &str) -> Vec<u8> {
        let size = e.read(key, 0, &mut []).expect("stat").size as usize;
        let mut buf = vec![0u8; size];
        let r = e.read(key, 0, &mut buf).expect("read");
        assert_eq!(r.len, size);
        buf
    }

    // ----- attributes -----

    use std::cell::Cell;
    use std::os::unix::fs::{MetadataExt, PermissionsExt};

    thread_local! {
        /// Replaces /proc/self/uid_map and gid_map on this thread.
        pub(super) static ID_MAP: std::cell::RefCell<Option<String>> = const { std::cell::RefCell::new(None) };
        /// Makes `owner_capability` say yes on this thread, so the owner path
        /// runs without root (its chown of another uid then fails, or a
        /// chown to the caller's own ids succeeds).
        pub(super) static OWNER_CAPS: Cell<Caps> = const { Cell::new(Caps::Missing) };
        /// Bytes allocated by this thread (see `Counting`).
        static ALLOCATED: Cell<usize> = const { Cell::new(0) };
    }

    /// Counts what each thread allocates, so a test can bound what one
    /// call costs.
    struct Counting;
    // SAFETY: forwards to the system allocator; only adds a counter.
    unsafe impl std::alloc::GlobalAlloc for Counting {
        unsafe fn alloc(&self, layout: std::alloc::Layout) -> *mut u8 {
            let _ = ALLOCATED.try_with(|a| a.set(a.get() + layout.size()));
            // SAFETY: the caller's layout, passed on.
            unsafe { std::alloc::System.alloc(layout) }
        }
        unsafe fn dealloc(&self, ptr: *mut u8, layout: std::alloc::Layout) {
            // SAFETY: allocated by `alloc` above with this layout.
            unsafe { std::alloc::System.dealloc(ptr, layout) }
        }
        unsafe fn realloc(
            &self,
            ptr: *mut u8,
            layout: std::alloc::Layout,
            new_size: usize,
        ) -> *mut u8 {
            let _ = ALLOCATED.try_with(|a| a.set(a.get() + new_size));
            // SAFETY: as the caller's.
            unsafe { std::alloc::System.realloc(ptr, layout, new_size) }
        }
    }

    #[global_allocator]
    static GLOBAL: Counting = Counting;

    fn allocated() -> usize {
        ALLOCATED.with(|a| a.get())
    }

    /// What `owner_capability` answers on a test's thread: by default
    /// that CAP_CHOWN is missing (owners kept), whoever runs the tests; a
    /// test that wants owners applied says so ([`ForceChown`]), and the
    /// one that needs the process's real capabilities asks for them.
    #[derive(Clone, Copy, Debug)]
    pub(super) enum Caps {
        Missing,
        Present,
        Real,
    }

    struct ForceChown;
    impl ForceChown {
        fn on() -> Self {
            OWNER_CAPS.with(|c| c.set(Caps::Present));
            ForceChown
        }
    }
    impl Drop for ForceChown {
        fn drop(&mut self) {
            OWNER_CAPS.with(|c| c.set(Caps::Missing));
        }
    }

    /// A uid and gid that are not this process's (and not root's), for a
    /// file of "another user" when the tests run as root.
    const FOREIGN: u32 = 4321;

    fn euid() -> u32 {
        // SAFETY: no arguments, cannot fail.
        unsafe { libc::geteuid() }
    }

    /// (mode & 0o7777, mtime s, mtime ns, uid, gid) of `p`, not following
    /// a symlink.
    fn attrs_of(p: &Path) -> (u32, i64, i64, u32, u32) {
        let m = std::fs::symlink_metadata(p).unwrap_or_else(|e| panic!("{}: {e}", p.display()));
        (
            m.mode() & 0o7777,
            m.mtime(),
            m.mtime_nsec(),
            m.uid(),
            m.gid(),
        )
    }

    fn mode_of(p: &Path) -> u32 {
        attrs_of(p).0
    }

    fn set_mtime(p: &Path, sec: i64, nsec: i64) {
        let c = CString::new(p.as_os_str().as_encoded_bytes()).expect("path");
        let times = [
            libc::timespec {
                tv_sec: 0,
                tv_nsec: libc::UTIME_OMIT,
            },
            libc::timespec {
                tv_sec: sec,
                tv_nsec: nsec,
            },
        ];
        // SAFETY: valid C string and two timespecs.
        assert_eq!(
            unsafe { libc::utimensat(libc::AT_FDCWD, c.as_ptr(), times.as_ptr(), 0) },
            0,
            "utimensat"
        );
    }

    fn chmod(p: &Path, mode: u32) {
        std::fs::set_permissions(p, std::fs::Permissions::from_mode(mode)).expect("chmod");
    }

    /// Makes every directory under its path owner-accessible again when
    /// dropped, so the temporary tree (read-only and 0000 directories
    /// included) can be removed. Declare it after the `TempDir`.
    struct Writable(PathBuf);
    impl Drop for Writable {
        fn drop(&mut self) {
            fn open_up(p: &Path) {
                let Ok(m) = std::fs::symlink_metadata(p) else {
                    return;
                };
                if m.is_dir() {
                    let _ = std::fs::set_permissions(p, std::fs::Permissions::from_mode(0o755));
                    if let Ok(items) = std::fs::read_dir(p) {
                        for i in items.flatten() {
                            open_up(&i.path());
                        }
                    }
                }
            }
            open_up(&self.0);
        }
    }

    /// Directories of the attribute tree, deepest first, with their modes.
    const ATTR_DIRS: [(&str, u32); 9] = [
        ("nest/deep/empty2", 0o750),
        ("nest/deep", 0o700),
        ("nest/sub", 0o2775),
        ("nest", 0o751),
        ("empty", 0o700),
        ("ro", 0o555),
        ("open", 0o777),
        ("tmp", 0o1777),
        ("", 0o750),
    ];

    /// The mode a fetch gives `listed`: set-id bits dropped (unless
    /// `setid`), a directory's o+w dropped unless sticky.
    fn expected_mode(listed: u32, is_dir: bool, setid: bool) -> u32 {
        let mut m = if setid { listed } else { listed & 0o1777 };
        if is_dir && m & 0o1000 == 0 {
            m &= !0o002;
        }
        m
    }

    /// Files of every mode the issue names, set-id files, a read-only
    /// directory with files in it, nested, empty, world-writable and
    /// sticky directories, all with past mtimes (nanoseconds included).
    /// Returns the export of `<tmp>/export` and the paths under its `ckpt`
    /// with the modes a default fetch gives them.
    fn attr_tree(d: &Path) -> (Arc<DirExport>, Vec<(&'static str, u32)>) {
        let r = d.join("export/ckpt");
        for (dir, _) in ATTR_DIRS {
            std::fs::create_dir_all(r.join(dir)).expect("mkdir");
        }
        let big: Vec<u8> = (0..3 * 65536 + 5).map(|i| (i * 13 % 251) as u8).collect();
        let files: [(&str, &[u8], u32); 8] = [
            ("private.key", b"secret", 0o600),
            ("run.sh", b"#!/bin/sh\n", 0o755),
            ("data.bin", &big, 0o640),
            ("suid", b"u", 0o4755),
            ("sgid", b"g", 0o2750),
            ("ro/a", b"a", 0o444),
            ("ro/b", b"bb", 0o644),
            ("nest/sub/f", b"f", 0o664),
        ];
        for (i, (rel, bytes, mode)) in files.iter().enumerate() {
            let p = r.join(rel);
            std::fs::write(&p, bytes).expect("write");
            chmod(&p, *mode);
            set_mtime(&p, 1_500_000_000 + i as i64, 123_456_789 + i as i64);
        }
        // Directories last, deepest first, so nothing moves their mtimes.
        for (i, (rel, mode)) in ATTR_DIRS.iter().enumerate() {
            let p = if rel.is_empty() {
                r.clone()
            } else {
                r.join(rel)
            };
            chmod(&p, *mode);
            set_mtime(&p, 1_400_000_000 + i as i64 * 1000, 999_999_999 - i as i64);
        }
        let mut expect: Vec<(&str, u32)> = files
            .iter()
            .map(|(rel, _, mode)| (*rel, expected_mode(*mode, false, false)))
            .collect();
        expect.extend(
            ATTR_DIRS
                .iter()
                .filter(|(rel, _)| !rel.is_empty())
                .map(|(rel, mode)| (*rel, expected_mode(*mode, true, false))),
        );
        (DirExport::open(&d.join("export")).expect("export"), expect)
    }

    /// Every expected path under `dst` has the source's mtime and the
    /// expected mode, and is the caller's.
    fn assert_attrs(src: &Path, dst: &Path, expect: &[(&str, u32)]) {
        for (rel, mode) in expect {
            let (s, d) = (attrs_of(&src.join(rel)), attrs_of(&dst.join(rel)));
            assert_eq!((d.0, d.1, d.2), (*mode, s.1, s.2), "{rel}: mode, mtime");
            assert_eq!(d.3, euid(), "{rel}: owner");
        }
    }

    const NOT_ROOT: Attributes = Attributes {
        modes: Applied::Yes,
        owners: Applied::NoCapability("CAP_CHOWN"),
    };

    /// A source whose version 3 listing is edited by `edit` (attributes,
    /// authority), as a server could send it. Data passes through.
    struct Patched {
        inner: Local,
        edit: Arc<dyn Fn(&mut Manifest) + Send + Sync>,
        bytes: Vec<u8>,
    }
    impl Patched {
        fn listing(&mut self, key: &str) -> Result<(), GetError> {
            let size = self
                .inner
                .export
                .read(key, 0, &mut [])
                .map_err(|_| GetError::NotFound)?
                .size as usize;
            let mut buf = vec![0u8; size];
            self.inner
                .export
                .read(key, 0, &mut buf)
                .map_err(|_| GetError::NotFound)?;
            let mut m = manifest::decode(&buf).map_err(GetError::Transport)?;
            (self.edit)(&mut m);
            self.bytes = manifest::encode(&m.entries, m.authority);
            Ok(())
        }
    }
    impl RangeSource for Patched {
        fn max_range(&self) -> usize {
            self.inner.max_range()
        }
        fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
            if !is_v3(key) {
                return self.inner.stat(key);
            }
            self.listing(key)?;
            Ok((self.bytes.len() as u64, 1))
        }
        fn fetch(
            &mut self,
            key: &str,
            offset: u64,
            generation: u128,
            len: usize,
        ) -> Result<&[u8], GetError> {
            if !is_v3(key) {
                return self.inner.fetch(key, offset, generation, len);
            }
            let start = (offset as usize).min(self.bytes.len());
            Ok(&self.bytes[start..(start + len).min(self.bytes.len())])
        }
        fn reconnect(&mut self) -> Result<(), HydrateError> {
            Ok(())
        }
    }

    fn patched(
        export: &Arc<DirExport>,
        edit: impl Fn(&mut Manifest) + Send + Sync + 'static,
    ) -> impl Fn() -> Result<Patched, HydrateError> + Sync {
        let inner = source(export, 65536, vec![]);
        let edit: Arc<dyn Fn(&mut Manifest) + Send + Sync> = Arc::new(edit);
        move || {
            Ok(Patched {
                inner: inner()?,
                edit: Arc::clone(&edit),
                bytes: Vec::new(),
            })
        }
    }

    #[test]
    fn a_fetch_preserves_modes_mtimes_and_empty_directories() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, expect) = attr_tree(d.path());
        let src = d.path().join("export/ckpt");
        let dest = d.path().join("dest");
        let r =
            fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(3, 65536)).expect("fetch");
        assert_eq!(r.attributes, NOT_ROOT, "no CAP_CHOWN: owner kept");
        assert_eq!(r.files, 8);
        assert_attrs(&src, &dest, &expect);
        // Hardened: o+w only with the sticky bit, no set-id on directories.
        assert_eq!(
            (
                mode_of(&dest.join("open")),
                mode_of(&dest.join("tmp")),
                mode_of(&dest.join("nest/sub"))
            ),
            (0o775, 0o1777, 0o775)
        );
        assert!(dest.join("empty").is_dir() && dest.join("nest/deep/empty2").is_dir());
        assert_eq!(
            std::fs::read(dest.join("data.bin")).expect("data"),
            std::fs::read(src.join("data.bin")).expect("src")
        );
        assert_no_staging(&dest);

        // Changed locally, fetched again over the copy (read-only
        // directory included): the source's attributes come back.
        chmod(&dest.join("private.key"), 0o666);
        set_mtime(&dest.join("run.sh"), 1, 0);
        chmod(&dest.join("empty"), 0o777);
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(3, 65536)).expect("refetch");
        assert_attrs(&src, &dest, &expect);

        // The copy, served (what --then-serve does), lists what was
        // applied, and a fetch from it reproduces the tree.
        let replica = DirExport::open_as(&dest, "ckpt").expect("replica");
        let listed =
            manifest::decode(&read_all(&replica, &manifest::key_v3("ckpt"))).expect("listing");
        assert!(listed.has_attrs());
        for (rel, mode) in &expect {
            let x = listed
                .entries
                .iter()
                .find(|x| x.path == format!("ckpt/{rel}"))
                .expect(rel);
            let s = attrs_of(&src.join(rel));
            assert_eq!(
                (
                    x.attrs.mode,
                    x.attrs.mtime_sec,
                    i64::from(x.attrs.mtime_nsec)
                ),
                (*mode, s.1, s.2),
                "{rel}"
            );
        }
        let second = d.path().join("second");
        fetch_dir(
            source(&replica, 65536, vec![]),
            "ckpt",
            &second,
            &opts(2, 65536),
        )
        .expect("second hop");
        assert_attrs(&src, &second, &expect);
    }

    #[test]
    fn while_copying_files_and_directories_are_private() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = d.path().join("dest");
        // SAFETY: sets the process umask; nothing else here depends on it,
        // and 022 lets a 0666 or 0777 creation show.
        unsafe { libc::umask(0o022) };
        let bad = Arc::new(Mutex::new(Vec::<String>::new()));
        let checks = Arc::new(AtomicUsize::new(0));
        let watch = |dirs: &'static [(&'static str, u32)]| {
            let (dest, bad, checks) = (dest.clone(), Arc::clone(&bad), Arc::clone(&checks));
            move |_: &str, _: u64| {
                checks.fetch_add(1, Ordering::Relaxed);
                for item in std::fs::read_dir(&dest).expect("dest").flatten() {
                    if item
                        .file_name()
                        .to_string_lossy()
                        .starts_with(STAGING_PREFIX)
                    {
                        for f in std::fs::read_dir(item.path()).expect("staging").flatten() {
                            let m = mode_of(&f.path());
                            if m != 0o600 {
                                bad.lock()
                                    .push(format!("staging {:?} {m:o}", f.file_name()));
                            }
                        }
                    }
                }
                for (rel, want) in dirs {
                    let m = mode_of(&dest.join(rel));
                    if m != *want {
                        bad.lock()
                            .push(format!("{rel} {m:o} during the copy, expected {want:o}"));
                    }
                }
            }
        };
        const NEW: &[(&str, u32)] = &[
            ("empty", 0o700),
            ("ro", 0o700),
            ("nest", 0o700),
            ("nest/deep/empty2", 0o700),
        ];
        fetch_dir(
            hooked(&e, 65536, watch(NEW)),
            "ckpt",
            &dest,
            &opts(1, 65536),
        )
        .expect("fetch");
        assert!(checks.load(Ordering::Relaxed) > 0);
        assert_eq!(*bad.lock(), Vec::<String>::new());
        assert_eq!(mode_of(&dest.join("ro")), 0o555);
        // Again over the copy: the 0555 directory gets owner rwx (only)
        // for the copy, then its listed mode.
        const AGAIN: &[(&str, u32)] = &[("ro", 0o755), ("empty", 0o700), ("nest", 0o751)];
        fetch_dir(
            hooked(&e, 65536, watch(AGAIN)),
            "ckpt",
            &dest,
            &opts(1, 65536),
        )
        .expect("refetch");
        assert_eq!(*bad.lock(), Vec::<String>::new());
        assert_eq!(mode_of(&dest.join("ro")), 0o555);
    }

    #[test]
    fn setid_bits_are_dropped_unless_asked_for() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = d.path().join("dropped");
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536)).expect("fetch");
        assert_eq!(mode_of(&dest.join("suid")), 0o755);
        assert_eq!(mode_of(&dest.join("sgid")), 0o750);
        assert_eq!(
            mode_of(&dest.join("nest/sub")),
            0o775,
            "a directory's set-group-ID too"
        );
        // Asked for, but the owner is not applied (no CAP_CHOWN): a file
        // listed 04755 of another uid must not become set-user-ID of the
        // copier. Dropped, and the report says so.
        let o = FetchOptions {
            preserve_setid: true,
            ..opts(2, 65536)
        };
        let dest = d.path().join("not-owned");
        let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &o).expect("fetch");
        assert_eq!(
            (r.attributes.owners, r.setid_kept),
            (Applied::NoCapability("CAP_CHOWN"), false)
        );
        assert_eq!(
            (mode_of(&dest.join("suid")), mode_of(&dest.join("nest/sub"))),
            (0o755, 0o775)
        );
        // Nor from a source whose owners are not its own.
        let _force = ForceChown::on();
        let mut o2 = o.clone();
        o2.preserve_setid = true;
        let not_theirs = |m: &mut Manifest| m.authority.owners = false;
        let dest = d.path().join("not-authoritative");
        let r = fetch_dir(patched(&e, not_theirs), "ckpt", &dest, &o2).expect("fetch");
        assert_eq!(
            (r.attributes.owners, r.setid_kept),
            (Applied::NotAuthoritative, false)
        );
        assert_eq!(mode_of(&dest.join("suid")), 0o755);
        // With the owner applied (to the caller's own ids here), kept.
        let dest = d.path().join("kept");
        let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &o).expect("fetch");
        assert_eq!((r.attributes.owners, r.setid_kept), (Applied::Yes, true));
        assert_eq!(mode_of(&dest.join("suid")), 0o4755);
        assert_eq!(mode_of(&dest.join("sgid")), 0o2750);
        assert_eq!(mode_of(&dest.join("nest/sub")), 0o2775);
        assert_eq!(
            mode_of(&dest.join("open")),
            0o775,
            "o+w still dropped from a non-sticky directory"
        );
        // And never together with owners off.
        let bad = FetchOptions { owners: false, ..o };
        assert!(matches!(
            fetch_dir(source(&e, 65536, vec![]), "ckpt", &d.path().join("x"), &bad),
            Err(FetchError::Invalid(_))
        ));
    }

    #[test]
    fn attributes_are_given_mtime_and_mode_before_the_owner() {
        let a = Attrs {
            mode: 0o4755,
            mtime_sec: 9,
            mtime_nsec: 8,
            uid: 7,
            gid: 6,
        };
        let apply = |owners, preserve_setid| Apply {
            attributes: Attributes {
                modes: Applied::Yes,
                owners,
            },
            preserve_setid,
        };
        // mtime and mode while the caller still owns it (no CAP_FOWNER
        // needed), then the owner, then the set-id bits the chown cleared.
        assert_eq!(
            apply(Applied::Yes, true).steps(&a, false),
            [
                Step::Mtime(9, 8),
                Step::Mode(0o755),
                Step::Owner(7, 6),
                Step::SetidMode(0o4755)
            ]
        );
        assert_eq!(
            apply(Applied::Yes, false).steps(&a, false),
            [Step::Mtime(9, 8), Step::Mode(0o755), Step::Owner(7, 6)]
        );
        // No owner applied: no set-id bits, asked for or not.
        assert_eq!(
            apply(Applied::NoCapability("CAP_CHOWN"), true).steps(&a, false),
            [Step::Mtime(9, 8), Step::Mode(0o755)]
        );
        assert_eq!(
            apply(Applied::Off, true).steps(&a, false),
            [Step::Mtime(9, 8), Step::Mode(0o755)]
        );
        let dir = Attrs { mode: 0o2777, ..a };
        assert_eq!(
            apply(Applied::Yes, true).steps(&dir, true),
            [
                Step::Mtime(9, 8),
                Step::Mode(0o775),
                Step::Owner(7, 6),
                Step::SetidMode(0o2775)
            ]
        );
        let plain = Attrs { mode: 0o640, ..a };
        assert_eq!(
            apply(Applied::Yes, true).steps(&plain, false),
            [Step::Mtime(9, 8), Step::Mode(0o640), Step::Owner(7, 6)],
            "no set-id bits: no second chmod"
        );
    }

    #[test]
    fn without_attributes_everything_stays_private() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = d.path().join("dest");
        let o = FetchOptions {
            attrs: false,
            ..opts(2, 65536)
        };
        let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &o).expect("fetch");
        assert_eq!(
            r.attributes,
            Attributes {
                modes: Applied::Off,
                owners: Applied::Off
            }
        );
        for f in ["private.key", "run.sh", "ro/a", "suid"] {
            assert_eq!(mode_of(&dest.join(f)), 0o600, "{f}");
        }
        for dir in ["empty", "ro", "open", "nest/deep/empty2"] {
            assert_eq!(mode_of(&dest.join(dir)), 0o700, "{dir}");
        }
        assert!(
            attrs_of(&dest.join("private.key")).1 > 1_600_000_000,
            "copy time, not the source's"
        );
        // A 0555 directory of an earlier copy gets its mode back.
        let full = d.path().join("full");
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &full, &opts(2, 65536)).expect("full");
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &full, &o).expect("bytes only over it");
        assert_eq!(mode_of(&full.join("ro")), 0o555);
    }

    #[test]
    fn a_source_without_attributes_is_refused_by_name_unless_no_attrs() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, expect) = attr_tree(d.path());
        let fleet = Fleet::new(&[("old", &e)]).old("old");
        let dest = d.path().join("dest");
        let err = fetch_from(&fleet, "ckpt", &dest, &multi_opts(2, 0)).expect_err("old source");
        let FetchError::Manifest(m) = &err else {
            panic!("{err}")
        };
        assert!(
            m.contains("old: serves manifest version 2 only")
                && m.contains("upgrade the source first")
                && m.contains("0600"),
            "{m}"
        );
        assert!(!dest.exists(), "nothing is created");
        assert!(fleet.state.released.lock().contains(&"old".to_string()));
        // A prefix that is missing on a current source is reported as
        // missing, not as an old source.
        let fleet = Fleet::new(&[("new", &e)]);
        let err = fetch_from(&fleet, "nope", &dest, &multi_opts(2, 0)).expect_err("missing");
        assert!(
            matches!(&err, FetchError::Manifest(m) if m.contains("no such directory")),
            "{err}"
        );

        // --no-attrs takes it, bytes only (version 2: no directory list).
        let fleet = Fleet::new(&[("old", &e)]).old("old");
        let o = FetchOptions {
            attrs: false,
            ..multi_opts(2, 0)
        };
        let r = fetch_from(&fleet, "ckpt", &dest, &o).expect("no-attrs");
        assert_eq!(r.attributes.modes, Applied::Off);
        assert_eq!(
            std::fs::read(dest.join("private.key")).expect("key"),
            b"secret"
        );
        assert_eq!(mode_of(&dest.join("run.sh")), 0o600);

        // With a current source too, the old one is passed over.
        let seed = seed_putting_first("old", &["old", "new"]);
        let fleet = Fleet::new(&[("old", &e), ("new", &e)]).old("old");
        let dest = d.path().join("dest2");
        let r = fetch_from(&fleet, "ckpt", &dest, &multi_opts(2, seed)).expect("new");
        assert_eq!(r.source, "new");
        assert_attrs(&d.path().join("export/ckpt"), &dest, &expect);
    }

    #[test]
    fn after_a_failover_the_attributes_still_come_from_the_first_manifest() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, expect) = attr_tree(d.path());
        let replica_dir = d.path().join("replica");
        let replica = replica_of(&e, &replica_dir);
        // The replica's directories differ from the origin's (its files
        // cannot without changing their identity), and it serves no
        // version 3 listing: neither stops it from serving the bytes.
        chmod(&replica_dir.join("ckpt/empty"), 0o755);
        set_mtime(&replica_dir.join("ckpt/nest"), 2_000_000_000, 0);
        let seed = seed_putting_first("origin", &["origin", "replica"]);
        let fleet = Fleet::new(&[("origin", &e), ("replica", &replica)])
            .old("replica")
            .kill_after("origin", 3);
        let dest = d.path().join("dest");
        let r = fetch_from(&fleet, "ckpt", &dest, &multi_opts(1, seed)).expect("fetch");
        assert_eq!((r.source.as_str(), r.failovers), ("origin", 1));
        assert!(!fleet.served_by("replica").is_empty());
        assert_attrs(&d.path().join("export/ckpt"), &dest, &expect);
    }

    #[test]
    fn a_refused_chown_ends_the_fetch_at_once_naming_no_owner() {
        if euid() == 0 {
            eprintln!("skipped: root may chown to any id");
            return;
        }
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let _force = ForceChown::on();
        // Every entry owned by root: what an unmapped id or root_squash
        // turns a chown into (EPERM).
        let to_root = |m: &mut Manifest| m.entries.iter_mut().for_each(|x| x.attrs.uid = 0);
        let dest = d.path().join("dest");
        let started = Instant::now();
        let err =
            fetch_dir(patched(&e, to_root), "ckpt", &dest, &opts(2, 65536)).expect_err("EPERM");
        assert!(
            matches!(&err, FetchError::Io { error, .. } if error.to_string().contains("--no-owner")),
            "{err}"
        );
        assert!(started.elapsed() < Duration::from_secs(10));
        let mut files = Vec::new();
        walk(&dest, &dest, &mut files);
        assert!(
            files.is_empty(),
            "no file appears without its owner: {:?}",
            files.iter().map(|f| &f.0).collect::<Vec<_>>()
        );
        // --no-owner: mode and mtime, the caller's ownership.
        let o = FetchOptions {
            owners: false,
            ..opts(2, 65536)
        };
        let r = fetch_dir(patched(&e, to_root), "ckpt", &dest, &o).expect("no-owner");
        assert_eq!(
            r.attributes,
            Attributes {
                modes: Applied::Yes,
                owners: Applied::Off
            }
        );
        assert_eq!(attrs_of(&dest.join("private.key")).3, euid());
        assert_eq!(mode_of(&dest.join("private.key")), 0o600);
    }

    #[test]
    fn owners_and_modes_a_copy_did_not_get_are_not_passed_on() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, expect) = attr_tree(d.path());
        let src = d.path().join("export/ckpt");
        // A replica fetched without CAP_CHOWN: its owners are the copier's.
        let replica_dir = d.path().join("replica");
        let replica = replica_of(&e, &replica_dir);
        let listed = manifest::decode(&read_all(&replica, &manifest::key_v3(""))).expect("listing");
        assert_eq!(
            listed.authority,
            Authority {
                modes: true,
                owners: false
            }
        );
        let _force = ForceChown::on();
        let dest = d.path().join("dest");
        let r = fetch_dir(
            source(&replica, 65536, vec![]),
            "ckpt",
            &dest,
            &opts(2, 65536),
        )
        .expect("fetch");
        assert_eq!(
            r.attributes,
            Attributes {
                modes: Applied::Yes,
                owners: Applied::NotAuthoritative
            }
        );
        assert_attrs(&src, &dest, &expect);
        // A copy made with --no-attrs: its 0600 and copy times are not the
        // origin's either.
        let bytes_only = d.path().join("bytes-only");
        fetch_dir(
            source(&e, 65536, vec![]),
            "",
            &bytes_only,
            &FetchOptions {
                attrs: false,
                ..opts(2, 65536)
            },
        )
        .expect("no-attrs copy");
        let served = DirExport::open(&bytes_only).expect("serve");
        let dest = d.path().join("dest2");
        let r = fetch_dir(
            source(&served, 65536, vec![]),
            "ckpt",
            &dest,
            &opts(2, 65536),
        )
        .expect("fetch");
        assert_eq!(
            r.attributes,
            Attributes {
                modes: Applied::NotAuthoritative,
                owners: Applied::NotAuthoritative
            }
        );
        assert_eq!(mode_of(&dest.join("run.sh")), 0o600);
        // From the origin, with the right to chown (to the caller's own
        // ids here), the copy has full authority.
        let dest = d.path().join("dest3");
        let r = fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(2, 65536)).expect("fetch");
        assert_eq!(
            r.attributes,
            Attributes {
                modes: Applied::Yes,
                owners: Applied::Yes
            }
        );
        // The chown came after the mtime: it moved ctime only.
        assert_attrs(&src, &dest.join("ckpt"), &expect);
        let root = dirfd::open_dir(&dest).expect("root");
        assert_eq!(content::load_authority(&root), Authority::FULL);
        // An interrupted fetch never raises what was recorded.
        content::record_authority(&root, Authority::NONE).expect("record");
        let flaky = source_with(&e, 65536, vec![None, None, Some(GetError::NotFound)], 0);
        fetch_dir(flaky, "", &dest, &opts(1, 65536)).expect_err("fails mid-copy");
        assert_eq!(content::load_authority(&root), Authority::NONE);
        // Nor does a complete one into a DEST that held other files: they
        // keep what was recorded for them.
        fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(2, 65536)).expect("fetch");
        assert_eq!(content::load_authority(&root), Authority::NONE);
        // Into an empty DEST it records its own.
        let dest = d.path().join("dest4");
        fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(2, 65536)).expect("fetch");
        assert_eq!(
            content::load_authority(&dirfd::open_dir(&dest).expect("root")),
            Authority::FULL
        );
    }

    #[test]
    fn a_copy_s_record_holds_for_its_subdirectories_and_its_parents() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        // A copy without CAP_CHOWN, one level down in a parent directory.
        let parent = d.path().join("parent");
        let copy = parent.join("copy");
        replica_of(&e, &copy);
        let partial = Authority {
            modes: true,
            owners: false,
        };
        let authority_of = |root: &Path, mount: &str, prefix: &str| {
            let x = DirExport::open_as(root, mount).expect("export");
            manifest::decode(&read_all(&x, &manifest::key_v3(prefix)))
                .expect("listing")
                .authority
        };
        assert_eq!(authority_of(&copy, "", ""), partial);
        // `serve --root copy/ckpt/nest`: the record is above the root.
        assert_eq!(authority_of(&copy.join("ckpt/nest"), "", ""), partial);
        // A prefix below the served root.
        assert_eq!(authority_of(&copy, "", "ckpt/nest"), partial);
        // `serve --root parent`: the record is below the listed directory.
        assert_eq!(authority_of(&parent, "", ""), partial);
        // An original tree beside it: full.
        assert_eq!(
            authority_of(&d.path().join("export"), "", ""),
            Authority::FULL
        );
    }

    #[test]
    fn a_replica_does_not_list_its_mount_directories() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let replica_dir = d.path().join("replica");
        fetch_dir(
            source(&e, 65536, vec![]),
            "ckpt",
            &replica_dir,
            &opts(2, 65536),
        )
        .expect("replica");
        // The replica's own root is the operator's (a 1777 scratch area,
        // say): never applied from the origin, so never listed.
        chmod(&replica_dir, 0o1777);
        let replica = DirExport::open_as(&replica_dir, "a/b").expect("open_as");
        let listed = manifest::decode(&read_all(&replica, &manifest::key_v3(""))).expect("listing");
        assert!(
            listed.entries.iter().all(|x| x.path.starts_with("a/b/")),
            "{:?}",
            listed
                .entries
                .iter()
                .filter(|x| !x.path.starts_with("a/b/"))
                .map(|x| &x.path)
                .collect::<Vec<_>>()
        );
        // A fetch through the mount's parents creates them private, the
        // fetcher's, like any directory no listing vouches for.
        let _force = ForceChown::on();
        let dest = d.path().join("dest");
        fetch_dir(source(&replica, 65536, vec![]), "", &dest, &opts(2, 65536)).expect("fetch");
        assert_eq!(
            (mode_of(&dest.join("a")), mode_of(&dest.join("a/b"))),
            (0o700, 0o700)
        );
        assert_eq!(attrs_of(&dest.join("a/b")).3, euid());
        assert_eq!(
            std::fs::read(dest.join("a/b/private.key")).expect("key"),
            b"secret"
        );
    }

    #[test]
    fn one_directory_failing_at_the_end_does_not_stop_the_others() {
        if euid() == 0 {
            eprintln!("skipped: root may chown to any id");
            return;
        }
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let _force = ForceChown::on();
        // The deepest directory cannot be given its owner; files can.
        let deep_to_root = |m: &mut Manifest| {
            if let Some(x) = m
                .entries
                .iter_mut()
                .find(|x| x.path == "ckpt/nest/deep/empty2")
            {
                x.attrs.uid = 0;
            }
        };
        let dest = d.path().join("dest");
        let err = fetch_dir(patched(&e, deep_to_root), "ckpt", &dest, &opts(2, 65536))
            .expect_err("EPERM");
        assert!(
            matches!(&err, FetchError::Io { path, .. } if path.ends_with("nest/deep/empty2")),
            "{err}"
        );
        assert_eq!(
            (
                mode_of(&dest.join("ro")),
                mode_of(&dest.join("nest")),
                mode_of(&dest.join("open"))
            ),
            (0o555, 0o751, 0o775),
            "shallower directories still got theirs"
        );
        // The one whose owner failed has its listed mode taken back: 0750
        // of the fetcher's would open it to the fetcher's group.
        assert_eq!(mode_of(&dest.join("nest/deep/empty2")), 0o700);
    }

    thread_local! {
        /// Makes `lock` report a filesystem without flock on this thread.
        pub(super) static NO_LOCK: Cell<bool> = const { Cell::new(false) };
        /// Makes the 0700 reset after a failed directory chown fail.
        pub(super) static FAIL_RESET: Cell<bool> = const { Cell::new(false) };
        /// Stands in for a crash right after a directory's attributes
        /// failed: its start record is not put back.
        pub(super) static CRASH_AFTER_ERROR: Cell<bool> = const { Cell::new(false) };
        /// Makes the rename of a put-back start record fail.
        pub(super) static FAIL_PUT_BACK: Cell<bool> = const { Cell::new(false) };
        /// Makes a directory's last step (its mtime) fail, after its end
        /// record.
        pub(super) static FAIL_FINAL_MODE: Cell<bool> = const { Cell::new(false) };
    }

    #[test]
    fn without_the_lock_a_destination_is_never_taken_for_empty() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let _force = ForceChown::on();
        // Only nvstor-peer's own names (a record of no authority) in it.
        let run = |name: &str, no_lock: bool| {
            let dest = d.path().join(name);
            std::fs::create_dir_all(&dest).expect("mkdir");
            let root = dirfd::open_dir(&dest).expect("root");
            content::record_authority(&root, Authority::NONE).expect("record");
            NO_LOCK.with(|n| n.set(no_lock));
            let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536));
            NO_LOCK.with(|n| n.set(false));
            r.expect("fetch");
            content::load_authority(&root)
        };
        assert_eq!(
            run("locked", false),
            Authority::FULL,
            "empty and locked: its own record"
        );
        // Its directories got the end record too (the start one was none).
        let nest = dirfd::open_dir(&d.path().join("locked/nest")).expect("nest");
        assert_eq!(content::load_authority(&nest), Authority::FULL);
        assert_eq!(
            run("unlocked", true),
            Authority::NONE,
            "no lock: another fetch may be filling it"
        );
    }

    #[test]
    fn a_copy_describes_itself_in_every_directory() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let copy = d.path().join("copy");
        replica_of(&e, &copy);
        let partial = Authority {
            modes: true,
            owners: false,
        };
        for dir in ["ckpt", "ckpt/nest", "ckpt/nest/deep/empty2", "ckpt/ro"] {
            assert_eq!(
                content::load_authority(&dirfd::open_dir(&copy.join(dir)).expect("dir")),
                partial,
                "{dir}"
            );
        }
        // Interrupted: the directories already carry the start record.
        let part = d.path().join("part");
        let flaky = source_with(&e, 65536, vec![None, None, Some(GetError::NotFound)], 0);
        fetch_dir(flaky, "", &part, &opts(1, 65536)).expect_err("fails mid-copy");
        assert_eq!(
            content::load_authority(&dirfd::open_dir(&part.join("ckpt/nest")).expect("dir")),
            partial
        );
        // Never listed, so never copied on.
        let served = DirExport::open(&copy).expect("serve");
        let listed = manifest::decode(&read_all(&served, &manifest::key_v3(""))).expect("listing");
        assert!(listed
            .entries
            .iter()
            .all(|x| !x.path.contains(content::RESERVED_PREFIX)));
        // A directory of it served on its own, from a place whose
        // ancestors hold no record (what a bind mount or subPath shows):
        // still not the origin's owners.
        let elsewhere = d.path().join("elsewhere");
        std::fs::create_dir_all(&elsewhere).expect("mkdir");
        std::fs::rename(copy.join("ckpt/nest"), elsewhere.join("nest")).expect("move");
        let alone = DirExport::open(&elsewhere.join("nest")).expect("serve");
        assert_eq!(
            manifest::decode(&read_all(&alone, &manifest::key_v3("")))
                .expect("listing")
                .authority,
            partial
        );
    }

    #[test]
    fn a_directory_that_cannot_be_made_private_again_is_said_so() {
        if euid() == 0 {
            eprintln!("skipped: root may chown to any id");
            return;
        }
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let _force = ForceChown::on();
        let deep_to_root = |m: &mut Manifest| {
            if let Some(x) = m
                .entries
                .iter_mut()
                .find(|x| x.path == "ckpt/nest/deep/empty2")
            {
                x.attrs.uid = 0;
            }
        };
        FAIL_RESET.with(|f| f.set(true));
        let r = fetch_dir(
            patched(&e, deep_to_root),
            "ckpt",
            &d.path().join("dest"),
            &opts(2, 65536),
        );
        FAIL_RESET.with(|f| f.set(false));
        let err = r.expect_err("EPERM");
        assert!(
            err.to_string().contains("could not be made private again"),
            "{err}"
        );
        // The chmod's own error, not whatever ran after it.
        assert!(
            err.to_string()
                .contains(&io::Error::from_raw_os_error(libc::EROFS).to_string()),
            "{err}"
        );
    }

    thread_local! {
        /// Makes `kernel_clears_setid` do what the kernel does to a process
        /// without CAP_FSETID setting S_ISGID for a group not its own.
        static KERNEL_CLEARS: Cell<bool> = const { Cell::new(false) };
    }

    /// Called right after set-id bits are set: on this thread, when asked
    /// to, drops S_ISGID again, as the kernel can without an error.
    pub(super) fn kernel_clears_setid(fd: i32) {
        if KERNEL_CLEARS.with(|k| k.get()) {
            // SAFETY: `st` is written by fstat before it is read.
            let mut st: libc::stat = unsafe { std::mem::zeroed() };
            // SAFETY: valid descriptor and out pointer; valid descriptor.
            unsafe {
                assert_eq!(libc::fstat(fd, &mut st), 0);
                assert_eq!(libc::fchmod(fd, st.st_mode & 0o7777 & !libc::S_ISGID), 0);
            }
        }
    }

    #[test]
    fn set_id_bits_the_kernel_cleared_are_reported() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let _force = ForceChown::on();
        KERNEL_CLEARS.with(|k| k.set(true));
        // One connection: every file and directory is finished on this
        // thread, where the seam is on.
        let o = FetchOptions {
            preserve_setid: true,
            ..opts(1, 65536)
        };
        let dest = d.path().join("dest");
        let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &o);
        KERNEL_CLEARS.with(|k| k.set(false));
        let r = r.expect("fetch");
        // suid keeps S_ISUID (only S_ISGID is cleared); sgid and nest/sub
        // lose theirs, and the report says so.
        assert_eq!(
            (
                mode_of(&dest.join("suid")),
                mode_of(&dest.join("sgid")),
                mode_of(&dest.join("nest/sub"))
            ),
            (0o4755, 0o750, 0o775)
        );
        assert!(r.setid_kept);
        assert_eq!(r.setid_cleared, 2);
    }

    #[test]
    fn a_fetch_into_a_directory_of_a_reduced_copy_does_not_raise_it() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        // A copy made without CAP_CHOWN: owners not its own.
        let copy = d.path().join("data/copy");
        replica_of(&e, &copy);
        // Then, with the right to chown, a fetch into a directory of it
        // that already holds files.
        let _force = ForceChown::on();
        let nest = copy.join("ckpt/nest");
        let partial = Authority {
            modes: true,
            owners: false,
        };
        let record_of = |dir: &Path| content::load_authority(&dirfd::open_dir(dir).expect("dir"));
        // Interrupted: the record written before any rename already
        // carries what holds above.
        // The listing, then a range that fails for good (nest has one).
        let flaky = source_with(&e, 65536, vec![None, Some(GetError::NotFound)], 0);
        fetch_dir(flaky, "ckpt/nest", &nest, &opts(1, 65536)).expect_err("fails mid-copy");
        assert_eq!(record_of(&nest), partial, "record written at the start");
        let r = fetch_dir(
            source(&e, 65536, vec![]),
            "ckpt/nest",
            &nest,
            &opts(2, 65536),
        )
        .expect("fetch");
        assert_eq!(r.attributes.owners, Applied::Yes);
        assert_eq!(
            record_of(&nest),
            partial,
            "the record there keeps what holds above it"
        );
        // Into a new, empty directory of the copy: its record is replaced
        // at the end, and still with what holds above.
        let new = copy.join("ckpt/new");
        fetch_dir(
            source(&e, 65536, vec![]),
            "ckpt/nest",
            &new,
            &opts(2, 65536),
        )
        .expect("fetch");
        assert_eq!(
            record_of(&new),
            partial,
            "record written at the end into a fresh DEST"
        );
        let served = DirExport::open(&nest).expect("serve");
        assert_eq!(
            manifest::decode(&read_all(&served, &manifest::key_v3("")))
                .expect("listing")
                .authority,
            partial
        );
    }

    #[test]
    fn records_are_and_ed_all_the_way_up_and_errors_give_no_authority() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let copy = d.path().join("copy");
        replica_of(&e, &copy);
        let partial = Authority {
            modes: true,
            owners: false,
        };
        let authority_of = |root: &Path| {
            let x = DirExport::open(root).expect("export");
            manifest::decode(&read_all(&x, &manifest::key_v3("")))
                .expect("listing")
                .authority
        };
        // A record nearer the served directory that claims more does not
        // win over the one above it: records only lower.
        let ckpt = copy.join("ckpt");
        content::record_authority(&dirfd::open_dir(&ckpt).expect("ckpt"), Authority::FULL)
            .expect("record");
        assert_eq!(authority_of(&ckpt.join("nest")), partial);
        // A directory on the way up without read permission (search only)
        // is gone through: `..` is opened O_PATH.
        chmod(&ckpt, 0o311);
        assert_eq!(authority_of(&ckpt.join("nest")), partial);
        chmod(&ckpt, 0o755);
        // One that cannot be searched either: the walk cannot see past
        // it, so nothing is authoritative. (Root searches it anyway:
        // checked only when not root.)
        let nest = DirExport::open(&ckpt.join("nest")).expect("export");
        chmod(&ckpt, 0o600);
        let listed = manifest::decode(&read_all(&nest, &manifest::key_v3(""))).expect("listing");
        chmod(&ckpt, 0o755);
        if euid() != 0 {
            assert_eq!(listed.authority, Authority::NONE);
        }
        // A record of another user's counts as none.
        let fresh = d.path().join("fresh");
        fetch_dir(source(&e, 65536, vec![]), "", &fresh, &opts(2, 65536)).expect("fetch");
        // As root, the record really is another user's; otherwise the
        // server takes another uid for its own.
        let rec = fresh.join(content::ATTRS_FILE);
        let other = if euid() == 0 {
            std::os::unix::fs::lchown(&rec, Some(FOREIGN), Some(FOREIGN)).expect("chown");
            let other = authority_of(&fresh);
            std::os::unix::fs::lchown(&rec, Some(0), Some(0)).expect("chown back");
            other
        } else {
            content::tests::SERVER_EUID.with(|u| u.set(Some(euid() + 1)));
            let other = authority_of(&fresh);
            content::tests::SERVER_EUID.with(|u| u.set(None));
            other
        };
        assert_eq!(other, Authority::NONE);
        assert_eq!(
            authority_of(&fresh),
            partial,
            "the same record, the server's own"
        );
    }

    #[test]
    fn stale_temporary_records_are_removed_when_a_fetch_starts() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = d.path().join("dest");
        std::fs::create_dir_all(&dest).expect("mkdir");
        let fifo = CString::new(
            dest.join(".nvstor-peer-attrs.tmp")
                .as_os_str()
                .as_encoded_bytes(),
        )
        .expect("path");
        // SAFETY: valid C string.
        assert_eq!(unsafe { libc::mkfifo(fifo.as_ptr(), 0o644) }, 0, "mkfifo");
        std::os::unix::fs::symlink("/etc/hostname", dest.join(".nvstor-peer-content.tmp-0123"))
            .expect("symlink");
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536)).expect("fetch");
        let left: Vec<String> = std::fs::read_dir(&dest)
            .expect("dest")
            .flatten()
            .map(|x| x.file_name().to_string_lossy().into_owned())
            .filter(|n| n.contains(".tmp"))
            .collect();
        assert!(left.is_empty(), "{left:?}");
    }

    #[test]
    fn a_transient_error_on_the_version_3_listing_is_not_an_older_source() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, expect) = attr_tree(d.path());
        // Every attempt of the first listing fails (its retries spent):
        // the source failed to open, it is not an older nvstor-peer, and
        // it serves its version 3 listing on the next round.
        let fleet = Fleet::new(&[("a", &e)]).flaky_v3("a", 4);
        let dest = d.path().join("dest");
        let r = fetch_from(&fleet, "ckpt", &dest, &multi_opts(2, 0)).expect("fetch");
        assert_eq!(r.attributes.modes, Applied::Yes);
        assert_attrs(&d.path().join("export/ckpt"), &dest, &expect);
    }

    #[test]
    fn a_directory_that_cannot_be_made_fails_alone() {
        if euid() == 0 {
            eprintln!("skipped: root opens a 0000 directory");
            return;
        }
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = d.path().join("dest");
        for dir in ["empty", "nest"] {
            std::fs::create_dir_all(dest.join(dir)).expect("mkdir");
            chmod(&dest.join(dir), 0o000);
        }
        let err = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536))
            .expect_err("incomplete");
        let FetchError::Incomplete { report, failed } = &err else {
            panic!("{err}")
        };
        let mut names: Vec<&str> = failed.iter().map(|(p, _)| p.as_str()).collect();
        names.sort();
        assert_eq!(
            names,
            [
                "ckpt/empty",
                "ckpt/nest",
                "ckpt/nest/deep",
                "ckpt/nest/deep/empty2",
                "ckpt/nest/sub",
                "ckpt/nest/sub/f"
            ]
        );
        assert_eq!(report.files, 7, "everything else is copied");
        assert_eq!(std::fs::read(dest.join("ro/b")).expect("ro/b"), b"bb");
        assert_eq!(mode_of(&dest.join("ro")), 0o555);
    }

    #[test]
    fn after_a_failed_fetch_directories_still_get_their_attributes() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = d.path().join("dest");
        // The listing, one range, then a range that fails for good.
        let flaky = || source_with(&e, 65536, vec![None, None, Some(GetError::NotFound)], 0);
        fetch_dir(flaky(), "ckpt", &dest, &opts(1, 65536)).expect_err("fails mid-copy");
        assert_eq!(
            (
                mode_of(&dest.join("ro")),
                mode_of(&dest.join("nest")),
                mode_of(&dest.join("nest/deep/empty2"))
            ),
            (0o555, 0o751, 0o750),
            "listed modes, not the 0700 of the copy"
        );
        // Bytes only over an earlier copy: a 0555 directory opened up for
        // the copy gets its mode back even when the fetch fails.
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536)).expect("complete");
        let o = FetchOptions {
            attrs: false,
            ..opts(1, 65536)
        };
        fetch_dir(flaky(), "ckpt", &dest, &o).expect_err("fails mid-copy");
        assert_eq!(mode_of(&dest.join("ro")), 0o555);
    }

    #[test]
    fn directory_modes_apply_deepest_first() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        // A directory without search permission above others: set first,
        // its children could not be opened any more.
        let closed = |m: &mut Manifest| {
            if let Some(x) = m.entries.iter_mut().find(|x| x.path == "ckpt/nest") {
                x.attrs.mode = 0o600;
            }
        };
        let dest = d.path().join("dest");
        fetch_dir(patched(&e, closed), "ckpt", &dest, &opts(2, 65536)).expect("fetch");
        assert_eq!(mode_of(&dest.join("nest")), 0o600);
        chmod(&dest.join("nest"), 0o751);
        assert_eq!(mode_of(&dest.join("nest/deep/empty2")), 0o750);
    }

    #[test]
    fn owners_need_cap_chown_dac_override_and_fowner_in_the_effective_set() {
        // Root's full set, and exactly the three: on.
        assert_eq!(owner_capability_from(Some(0x1ff_ffff_ffff)), Ok(()));
        assert_eq!(owner_capability_from(Some(0b1011)), Ok(()));
        // Each one missing: off, naming it.
        assert_eq!(owner_capability_from(Some(0b1010)), Err("CAP_CHOWN"));
        assert_eq!(owner_capability_from(Some(0b1001)), Err("CAP_DAC_OVERRIDE"));
        assert_eq!(owner_capability_from(Some(0b0011)), Err("CAP_FOWNER"));
        assert_eq!(owner_capability_from(Some(0)), Err("CAP_CHOWN"));
        // A set that cannot be read: off.
        assert!(owner_capability_from(None).is_err());
        // capget answers here; this unprivileged test process has none of
        // the three.
        let caps = effective_capabilities().expect("capget");
        if euid() != 0 {
            assert_eq!(caps & 0b1011, 0, "{caps:x}");
        }
    }

    #[test]
    fn ids_must_be_mapped_in_this_user_namespace() {
        let map = "         0     100000      65536\n";
        assert!(id_in_map(map, 0) && id_in_map(map, 65535));
        assert!(!id_in_map(map, 65536) && !id_in_map(map, u32::MAX));
        assert!(id_in_map("0 0 4294967295\n", 4_000_000_000));
        assert!(!id_in_map("garbage\n", 0));
        let file = |uid, gid| Entry {
            attrs: Attrs {
                uid,
                gid,
                ..Attrs::default()
            },
            ..file_entry("x")
        };
        ID_MAP.with(|m| *m.borrow_mut() = Some(map.into()));
        let (inside, outside) = (
            ids_mapped(&[file(0, 0), file(1000, 65535)]),
            ids_mapped(&[file(1000, 70000)]),
        );
        ID_MAP.with(|m| *m.borrow_mut() = None);
        assert!(inside);
        assert!(!outside);
    }

    #[test]
    fn owners_whose_ids_are_not_mapped_are_kept_without_failing() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, expect) = attr_tree(d.path());
        let _force = ForceChown::on();
        // A user namespace that maps only ids far from this user's.
        ID_MAP.with(|m| *m.borrow_mut() = Some(format!("{} 0 1\n", euid() + 1)));
        let dest = d.path().join("dest");
        let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536));
        ID_MAP.with(|m| *m.borrow_mut() = None);
        let r = r.expect("no EINVAL: owners are kept instead");
        assert_eq!(r.attributes.owners, Applied::IdsNotMapped);
        assert!(r
            .attributes
            .describe()
            .contains("owner kept: ids not mapped in this user namespace"));
        assert_attrs(&d.path().join("export/ckpt"), &dest, &expect);
    }

    /// Owner, group, set-id after the chown, and an mtime the chown did not
    /// move. Runs with CAP_CHOWN, CAP_DAC_OVERRIDE and CAP_FOWNER (without
    /// all three owners are not applied) and the ids 4321 and 8765 usable:
    /// as root, or in a user namespace that maps them, e.g.
    /// `unshare --map-root-user --map-auto cargo test -p nvstor-peer --lib
    /// as_root -- --ignored` (needs a subuid/subgid range for the user).
    #[test]
    #[ignore = "needs CAP_CHOWN, CAP_DAC_OVERRIDE, CAP_FOWNER: root, or a user namespace with a subuid map"]
    fn as_root_owner_and_group_are_applied_before_the_mode() {
        OWNER_CAPS.with(|c| c.set(Caps::Real));
        let real = owner_capability();
        OWNER_CAPS.with(|c| c.set(Caps::Missing));
        assert_eq!(
            real,
            Ok(()),
            "run with CAP_CHOWN, CAP_DAC_OVERRIDE and CAP_FOWNER"
        );
        let _real = ForceChown::on();
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, expect) = attr_tree(d.path());
        let src = d.path().join("export/ckpt");
        for (rel, _) in &expect {
            let p = src.join(rel);
            let before = attrs_of(&p);
            std::os::unix::fs::lchown(&p, Some(4321), Some(8765)).expect("chown");
            // chown cleared set-id bits and moved nothing else: restore them.
            chmod(&p, before.0);
            set_mtime(&p, before.1, before.2);
        }
        let dest = d.path().join("dest");
        let r =
            fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536)).expect("fetch");
        assert_eq!(
            r.attributes,
            Attributes {
                modes: Applied::Yes,
                owners: Applied::Yes
            }
        );
        for (rel, mode) in &expect {
            let (s, x) = (attrs_of(&src.join(rel)), attrs_of(&dest.join(rel)));
            assert_eq!(x, (*mode, s.1, s.2, 4321, 8765), "{rel}");
        }
        // With --preserve-setid the bits survive the chown (it comes first).
        let dest = d.path().join("kept");
        let o = FetchOptions {
            preserve_setid: true,
            ..opts(2, 65536)
        };
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &o).expect("fetch");
        let s = attrs_of(&src.join("suid"));
        assert_eq!(attrs_of(&dest.join("suid")), (0o4755, s.1, s.2, 4321, 8765));
    }

    fn dir_entry(p: &str, a: Attrs) -> Entry {
        Entry {
            path: p.into(),
            kind: manifest::Kind::Dir,
            attrs: a,
            ..Entry::default()
        }
    }

    fn file_entry(p: &str) -> Entry {
        Entry {
            path: p.into(),
            size: 1,
            generation: 1,
            content: 1,
            ..Entry::default()
        }
    }

    #[test]
    fn plan_creates_listed_directories_and_refuses_bad_ones() {
        let a = Attrs {
            mode: 0o750,
            mtime_sec: 5,
            mtime_nsec: 6,
            uid: 1,
            gid: 2,
        };
        let entries = vec![
            dir_entry("p/e", a),
            dir_entry("p/e/f", a),
            file_entry("p/x/y"),
            dir_entry(&format!("p/{STAGING_PREFIX}z"), a),
        ];
        let p = plan(&entries, "p", 192).expect("plan");
        assert_eq!(
            p.dirs.iter().map(|d| d.0).collect::<Vec<_>>(),
            ["e/f", "e", "x", ""],
            "deepest first"
        );
        assert_eq!(p.dirs[0].1, Some(a));
        assert_eq!(
            p.dirs[2].1, None,
            "a parent the manifest does not list has no attributes"
        );
        assert_eq!(p.files.iter().map(|f| f.1).collect::<Vec<_>>(), ["x/y"]);
        assert_eq!(p.failed.len(), 1, "reserved directory names are refused");
        assert!(
            matches!(
                plan(&[dir_entry("p", a)], "p", 192),
                Err(FetchError::Manifest(_))
            ),
            "the prefix itself"
        );
        for bad in [
            vec![dir_entry("a", a), file_entry("a")],
            vec![file_entry("a"), dir_entry("a", a)],
            vec![file_entry("a"), file_entry("a/b")],
            vec![file_entry("a/b"), file_entry("a")],
            vec![dir_entry("a", a), file_entry("a/b"), dir_entry("a/b", a)],
            vec![dir_entry("a", a), dir_entry("a", a)],
            vec![file_entry("a"), file_entry("a")],
        ] {
            assert!(
                matches!(plan(&bad, "", 192), Err(FetchError::Manifest(_))),
                "{:?}",
                bad.iter().map(|x| &x.path).collect::<Vec<_>>()
            );
        }
        // Listed after its children: still the same directory.
        let late = [file_entry("a/b"), dir_entry("a", a)];
        let p = plan(&late, "", 192).expect("plan");
        assert_eq!(p.dirs, [("a", Some(a)), ("", None)]);
    }

    #[test]
    fn paths_the_destination_cannot_have_are_refused_per_entry() {
        let a = Attrs::default();
        let long_name = "n".repeat(MAX_NAME + 1);
        let too_long = format!("x/{}f", "a/".repeat(MAX_REL_PATH / 2));
        let entries = vec![
            dir_entry(&format!("d/{long_name}"), a),
            file_entry(&format!("{long_name}/f")),
            dir_entry(&too_long, a),
            file_entry("ok"),
        ];
        let p = plan(&entries, "", usize::MAX).expect("plan");
        assert_eq!(p.failed.len(), 3, "{:?}", p.failed);
        assert_eq!(p.files.len(), 1);
        // A 65 KB path must be refused per entry, fast, without large allocations.
        let deep = vec![dir_entry(&format!("x/{}f", "a/".repeat(32_000)), a)];
        let (before, started) = (allocated(), Instant::now());
        let p = plan(&deep, "", usize::MAX).expect("plan");
        assert_eq!((p.failed.len(), p.dirs.len()), (1, 1));
        assert!(
            allocated() - before < 1 << 20,
            "{} bytes",
            allocated() - before
        );
        assert!(started.elapsed() < Duration::from_secs(1));
    }

    #[test]
    fn planning_costs_the_manifests_size_not_its_depth() {
        // Deep paths that share no parent: the case where copying every
        // ancestor's path costs each directory its whole path (~2 KB on
        // average here, 50 x ~4 MB in all). Built as a tree, a directory
        // costs a fixed few hundred bytes, however deep it is; and there
        // are at most half as many directories as path bytes.
        let entries: Vec<Entry> = (0..50)
            .map(|i| file_entry(&format!("d{i:02}/{}f", "a/".repeat(1990))))
            .collect();
        let before = allocated();
        let p = plan(&entries, "", usize::MAX).expect("plan");
        let used = allocated() - before;
        assert_eq!((p.files.len(), p.failed.len()), (50, 0));
        let dirs = p.dirs.len();
        assert_eq!(dirs, 50 * 1991 + 1);
        assert!(
            used < 600 * dirs,
            "{used} bytes allocated for {dirs} directories ({} per directory)",
            used / dirs
        );
    }

    #[test]
    fn an_earlier_copy_s_lower_record_in_a_directory_is_kept() {
        // A copy without owners into dest/ckpt, then a
        // fetch with owners of the whole tree into dest that does not
        // place one of the earlier copy's files.
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = d.path().join("dest");
        fetch_dir(
            source(&e, 65536, vec![]),
            "ckpt",
            &dest.join("ckpt"),
            &opts(2, 65536),
        )
        .expect("no-owner copy");
        let partial = Authority {
            modes: true,
            owners: false,
        };
        let _force = ForceChown::on();
        let omit = |m: &mut Manifest| m.entries.retain(|x| x.path != "ckpt/private.key");
        let r = fetch_dir(patched(&e, omit), "", &dest, &opts(2, 65536)).expect("owner fetch");
        assert_eq!(r.attributes.owners, Applied::Yes);
        // private.key is still the earlier copier's: ckpt and its
        // directories keep saying owners are not the origin's.
        for dir in ["ckpt", "ckpt/nest", "ckpt/ro"] {
            assert_eq!(
                content::load_authority(&dirfd::open_dir(&dest.join(dir)).expect("dir")),
                partial,
                "{dir}"
            );
        }
        let served = DirExport::open(&dest.join("ckpt")).expect("serve");
        assert_eq!(
            manifest::decode(&read_all(&served, &manifest::key_v3("")))
                .expect("listing")
                .authority,
            partial
        );
    }

    #[test]
    fn a_directory_whose_record_cannot_be_written_is_skipped_and_restored() {
        // A pre-existing 0555 directory (opened up for
        // the copy) whose record cannot be replaced.
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = d.path().join("dest");
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536)).expect("first copy");
        let ro = dest.join("ro");
        chmod(&ro, 0o755);
        let _ = std::fs::remove_file(ro.join(content::ATTRS_FILE));
        std::fs::create_dir_all(ro.join(content::ATTRS_FILE).join("x")).expect("plant");
        chmod(&ro, 0o555);
        let err = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536))
            .expect_err("incomplete");
        let FetchError::Incomplete { failed, .. } = &err else {
            panic!("{err}")
        };
        let why = |p: &str| {
            failed
                .iter()
                .find(|f| f.0 == p)
                .map(|f| f.1.clone())
                .unwrap_or_default()
        };
        assert!(why("ckpt/ro").contains(content::ATTRS_FILE), "{failed:?}");
        assert!(
            why("ckpt/ro/a").contains("its directory ro: cannot write its .nvstor-peer-attrs"),
            "{failed:?}"
        );
        assert_eq!(
            mode_of(&ro),
            0o555,
            "its mode back, not the 0755 of the copy"
        );
    }

    #[test]
    fn a_record_that_fails_at_the_end_does_not_stop_the_directory_s_pass() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let _force = ForceChown::on();
        // DEST says nothing is authoritative: every directory starts with
        // that record and ends with the fetch's own.
        let dest = d.path().join("dest");
        std::fs::create_dir_all(&dest).expect("mkdir");
        content::record_authority(&dirfd::open_dir(&dest).expect("dest"), Authority::NONE)
            .expect("record");
        let nest = dest.join("nest");
        let planted = AtomicBool::new(false);
        let plant = move |_: &str, _: u64| {
            if !planted.swap(true, Ordering::Relaxed) {
                let rec = nest.join(content::ATTRS_FILE);
                std::fs::remove_file(&rec).expect("start record there");
                std::fs::create_dir_all(rec.join("x")).expect("plant");
            }
        };
        let err = fetch_dir(hooked(&e, 65536, plant), "ckpt", &dest, &opts(1, 65536))
            .expect_err("record fails");
        assert!(
            matches!(&err, FetchError::Io { path, error } if path.ends_with("nest")
                         && error.to_string().contains(content::ATTRS_FILE)),
            "{err}"
        );
        assert_eq!(
            mode_of(&dest.join("nest")),
            0o751,
            "its attributes all the same"
        );
    }

    #[test]
    fn records_are_written_only_where_they_say_something() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        // Full authority into an empty DEST: no record file in any
        // directory (absence says it).
        let _force = ForceChown::on();
        let full = d.path().join("full");
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &full, &opts(2, 65536)).expect("fetch");
        for dir in ["nest", "nest/deep", "ro"] {
            assert!(!full.join(dir).join(content::ATTRS_FILE).exists(), "{dir}");
        }
        drop(_force);
        // Without owners: one record per directory, written once (the end
        // record equals the start one).
        let partial = d.path().join("partial");
        let inode = |p: &Path| std::fs::metadata(p).map(|m| m.ino()).ok();
        let seen = Arc::new(Mutex::new(None));
        let watch = {
            let (rec, seen) = (
                partial.join("nest").join(content::ATTRS_FILE),
                Arc::clone(&seen),
            );
            move |_: &str, _: u64| {
                let mut s = seen.lock();
                if s.is_none() {
                    *s = inode(&rec);
                }
            }
        };
        fetch_dir(hooked(&e, 65536, watch), "ckpt", &partial, &opts(1, 65536)).expect("fetch");
        let during = seen.lock().expect("a start record during the copy");
        assert_eq!(
            inode(&partial.join("nest").join(content::ATTRS_FILE)),
            Some(during),
            "not rewritten"
        );
    }

    #[test]
    fn stale_temporaries_in_directories_are_removed_only_under_the_lock() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let run = |name: &str, no_lock: bool| {
            let dest = d.path().join(name);
            std::fs::create_dir_all(dest.join("nest")).expect("mkdir");
            content::record_authority(&dirfd::open_dir(&dest).expect("dest"), Authority::NONE)
                .expect("record");
            let stale = dest.join("nest/.nvstor-peer-attrs.tmp-dead");
            std::fs::write(&stale, b"x").expect("stale");
            NO_LOCK.with(|n| n.set(no_lock));
            let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(2, 65536));
            NO_LOCK.with(|n| n.set(false));
            r.expect("fetch");
            stale.exists()
        };
        assert!(!run("locked", false), "removed under the lock");
        assert!(
            run("unlocked", true),
            "without the lock it may be another fetch's"
        );
    }

    /// Wall time of a fetch of a tree of 2,000 directories with one small
    /// file each, as a non-root user (so every directory gets a record).
    /// `cargo test -p nvstor-peer --lib time_2000 -- --ignored --nocapture`
    #[test]
    #[ignore = "timing, not a check"]
    fn time_2000_directories() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let r = d.path().join("export");
        for i in 0..2000 {
            let p = r.join(format!("t/d{:02}/e{i}", i % 40));
            std::fs::create_dir_all(&p).expect("mkdir");
            std::fs::write(p.join("f"), b"x").expect("write");
        }
        let e = DirExport::open(&r).expect("export");
        for run in 0..3 {
            let dest = d.path().join(format!("dest{run}"));
            let started = Instant::now();
            fetch_dir(source(&e, 65536, vec![]), "", &dest, &opts(32, 65536)).expect("fetch");
            eprintln!(
                "TIMING 2041 directories, 2000 files: {:.3} s",
                started.elapsed().as_secs_f64()
            );
        }
    }

    /// DEST whose record says owners are not the origin's: every
    /// directory a fetch creates in it starts with that and, the fetch
    /// applying owners, would end with full authority.
    fn partial_dest(d: &Path, name: &str) -> PathBuf {
        let dest = d.join(name);
        std::fs::create_dir_all(&dest).expect("mkdir");
        content::record_authority(
            &dirfd::open_dir(&dest).expect("dest"),
            Authority {
                modes: true,
                owners: false,
            },
        )
        .expect("record");
        dest
    }

    #[test]
    fn zz_unlocked_concurrent_created_dir_raise() {
        // Without the lock, a directory this fetch made
        // can receive another fetch's files (copier-owned) meanwhile.
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let dest = partial_dest(d.path(), "dest");
        let _force = ForceChown::on();
        let nest = dest.join("nest");
        let once = AtomicBool::new(false);
        let other_fetch = move |_: &str, _: u64| {
            if !once.swap(true, Ordering::Relaxed) {
                std::fs::write(nest.join("theirs"), b"copier-owned").expect("rename in");
            }
        };
        NO_LOCK.with(|n| n.set(true));
        let r = fetch_dir(
            hooked(&e, 65536, other_fetch),
            "ckpt",
            &dest,
            &opts(1, 65536),
        );
        NO_LOCK.with(|n| n.set(false));
        assert_eq!(r.expect("fetch").attributes.owners, Applied::Yes);
        let partial = Authority {
            modes: true,
            owners: false,
        };
        for dir in ["nest", "ro", "nest/deep/empty2"] {
            assert_eq!(
                content::load_authority(&dirfd::open_dir(&dest.join(dir)).expect("dir")),
                partial,
                "{dir}: no higher than it started without the lock"
            );
        }
        // With the lock, a directory it made is all its own: it may rise.
        let locked = partial_dest(d.path(), "locked");
        fetch_dir(source(&e, 65536, vec![]), "ckpt", &locked, &opts(2, 65536)).expect("fetch");
        assert_eq!(
            content::load_authority(&dirfd::open_dir(&locked.join("nest")).expect("nest")),
            Authority::FULL
        );
    }

    #[test]
    fn a_directory_whose_owner_fails_keeps_its_start_record() {
        if euid() == 0 {
            eprintln!("skipped: root may chown to any id");
            return;
        }
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let _force = ForceChown::on();
        let deep_to_root = |m: &mut Manifest| {
            if let Some(x) = m
                .entries
                .iter_mut()
                .find(|x| x.path == "ckpt/nest/deep/empty2")
            {
                x.attrs.uid = 0;
            }
        };
        let partial = Authority {
            modes: true,
            owners: false,
        };
        let record_of = |p: &Path| content::load_authority(&dirfd::open_dir(p).expect("dir"));
        // The chown fails, and nothing runs after it (a crash): the end
        // record must not be there already.
        let dest = partial_dest(d.path(), "crash");
        CRASH_AFTER_ERROR.with(|c| c.set(true));
        let r = fetch_dir(patched(&e, deep_to_root), "ckpt", &dest, &opts(1, 65536));
        CRASH_AFTER_ERROR.with(|c| c.set(false));
        r.expect_err("EPERM");
        assert_eq!(
            record_of(&dest.join("nest/deep/empty2")),
            partial,
            "its start record"
        );
        assert_eq!(mode_of(&dest.join("nest/deep/empty2")), 0o700);
        assert_eq!(
            record_of(&dest.join("nest")),
            Authority::FULL,
            "the others got theirs"
        );
        // A failure after the end record (the final mode): put back.
        let dest = partial_dest(d.path(), "late");
        FAIL_FINAL_MODE.with(|f| f.set(true));
        let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(1, 65536));
        FAIL_FINAL_MODE.with(|f| f.set(false));
        r.expect_err("final mode");
        assert_eq!(
            record_of(&dest.join("nest/deep/empty2")),
            partial,
            "its start record put back"
        );
    }

    #[test]
    fn a_directory_gets_its_owner_first_and_its_record_after_owner_and_mode() {
        let a = Attrs {
            mode: 0o2555,
            mtime_sec: 9,
            mtime_nsec: 8,
            uid: 7,
            gid: 6,
        };
        let apply = |owners, preserve_setid| Apply {
            attributes: Attributes {
                modes: Applied::Yes,
                owners,
            },
            preserve_setid,
        };
        let partial = Authority {
            modes: true,
            owners: false,
        };
        // The owner while it is still 0700 and the fetch's; owner rwx for
        // the record; the record; the final mode with set-id; mtime last.
        assert_eq!(
            dir_steps(apply(Applied::Yes, true), &a, Some(partial)),
            [
                DirStep::Owner(7, 6),
                DirStep::InterimMode(0o755),
                DirStep::Record(partial),
                DirStep::FinalMode(0o2555),
                DirStep::Mtime(9, 8)
            ]
        );
        // No record to write: the final mode at once.
        assert_eq!(
            dir_steps(apply(Applied::Yes, false), &a, None),
            [
                DirStep::Owner(7, 6),
                DirStep::InterimMode(0o555),
                DirStep::Mtime(9, 8)
            ]
        );
        // No owner applied: no chown, no set-id.
        assert_eq!(
            dir_steps(
                apply(Applied::NoCapability("CAP_CHOWN"), true),
                &a,
                Some(partial)
            ),
            [
                DirStep::InterimMode(0o755),
                DirStep::Record(partial),
                DirStep::FinalMode(0o555),
                DirStep::Mtime(9, 8)
            ]
        );
    }

    #[test]
    fn a_start_record_that_cannot_be_renamed_back_is_rewritten_in_place() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let _w = Writable(d.path().to_path_buf());
        let (e, _) = attr_tree(d.path());
        let _force = ForceChown::on();
        let partial = Authority {
            modes: true,
            owners: false,
        };
        let dest = partial_dest(d.path(), "dest");
        // The final mode fails after each end record (full: every
        // directory was made by this fetch, under the lock), and putting
        // the start record back by rename fails too.
        FAIL_FINAL_MODE.with(|f| f.set(true));
        FAIL_PUT_BACK.with(|f| f.set(true));
        let r = fetch_dir(source(&e, 65536, vec![]), "ckpt", &dest, &opts(1, 65536));
        FAIL_FINAL_MODE.with(|f| f.set(false));
        FAIL_PUT_BACK.with(|f| f.set(false));
        r.expect_err("final mode");
        for dir in ["nest/deep/empty2", "nest", "ro"] {
            assert_eq!(
                content::load_authority(&dirfd::open_dir(&dest.join(dir)).expect("dir")),
                partial,
                "{dir}: never left with the higher end record"
            );
        }
    }

    // Workers that find selection already failed must report the same
    // kind of error as the one that failed it: whichever reaches the
    // fetch's error slot first decides the exit code.
    #[test]
    fn later_callers_get_the_error_selection_ended_with() {
        let rebuilt = |e: FetchError| {
            let (make, why) = gave_up(&e);
            (
                std::mem::discriminant(&make(why.clone())),
                why,
                std::mem::discriminant(&e),
            )
        };
        for e in [
            FetchError::Corrupt("c".into()),
            FetchError::Refused("r".into()),
            FetchError::Mismatch("m".into()),
            FetchError::Manifest("x".into()),
            FetchError::Unavailable("u".into()),
            FetchError::Connect("k".into()),
        ] {
            let text = e.to_string();
            let (got, why, want) = rebuilt(e);
            assert_eq!(got, want, "{text}");
            assert!(
                text.ends_with(&why),
                "the message is not wrapped twice: {text} / {why}"
            );
        }
    }

    // Two workers report the same source: one for a transport error, one
    // for bad data. Whichever retires it first, the source is recorded
    // as having served bad data, and running out of sources ends Corrupt.
    #[test]
    fn corrupt_dominates_whichever_worker_retires_first() {
        let set = Fleet::new(&[]);
        let opts = FetchOptions::default();
        let (retries, reconnects) = (AtomicU64::new(0), AtomicU64::new(0));
        for corrupt_first in [false, true] {
            let sources = Sources::new(&set, "", &opts, &retries, &reconnects);
            let view = sources.start("a:1".into(), vec![]);
            let (first, second) = (
                Dead::Failed("transport".into()),
                Dead::Corrupt("crc".into()),
            );
            let (first, second) = if corrupt_first {
                (second, first)
            } else {
                (first, second)
            };
            sources.retire_as(&view, first);
            sources.retire_as(&view, second);
            let st = sources.state.lock();
            assert!(
                matches!(st.dead.get("a:1"), Some(Dead::Corrupt(_))),
                "corrupt_first {corrupt_first}"
            );
            assert!(matches!(
                Sources::<Fleet>::exhausted(&st),
                FetchError::Corrupt(_)
            ));
            assert!(st.current.is_none());
        }
    }

    // When the wait runs out, only a source that served bad data makes
    // the outcome an integrity failure; one that serves other content
    // (Mismatch) leaves it "unavailable".
    #[test]
    fn at_the_end_of_the_wait_only_bad_data_is_an_integrity_failure() {
        let dir = tempfile::tempdir().expect("tempdir");
        let export = DirExport::open(dir.path()).expect("export");
        let set = Fleet::new(&[("b:1", &export)]).down("b:1");
        let opts = FetchOptions {
            wait: Duration::ZERO,
            max_retries: 0,
            ..FetchOptions::default()
        };
        let (retries, reconnects) = (AtomicU64::new(0), AtomicU64::new(0));
        for (dead, corrupt) in [
            (Dead::Mismatch("other content".into()), false),
            (Dead::Corrupt("crc".into()), true),
        ] {
            let sources = Sources::new(&set, "", &opts, &retries, &reconnects);
            sources.state.lock().dead.insert("a:1".into(), dead);
            match sources.select(false, Want::V2, Ok) {
                Err(FetchError::Corrupt(_)) => assert!(corrupt),
                Err(FetchError::Unavailable(_)) => assert!(!corrupt),
                Err(e) => panic!("{e}"),
                Ok(_) => panic!("b:1 is down"),
            }
        }
    }

    // Once a file is refetched from a newer source view, writes from older
    // views are refused, and the switch waits for writes in progress.
    #[test]
    fn writes_from_older_views_are_refused_after_a_refetch() {
        let t = Arc::new(Target {
            key: "f".into(),
            size: 1,
            generation: 1,
            content: 0,
            rel: "f".into(),
            attrs: manifest::Attrs::default(),
            staging: CString::new("0").expect("cstring"),
            remaining: AtomicU64::new(1),
            done: AtomicBool::new(false),
            min_epoch: AtomicU64::new(0),
            rewrite: RwLock::new(()),
            writers: AtomicU64::new(0),
            setid_counted: AtomicBool::new(false),
            refetching: Mutex::new(()),
        });
        assert!(t.write_guard(0).is_some());
        let writing = t.write_guard(1).expect("before the refetch");
        let switched = Arc::new(AtomicBool::new(false));
        let refetch = {
            let (t, switched) = (Arc::clone(&t), Arc::clone(&switched));
            std::thread::spawn(move || {
                t.refetch_from(2);
                switched.store(true, Ordering::SeqCst);
            })
        };
        std::thread::sleep(Duration::from_millis(50));
        assert!(
            !switched.load(Ordering::SeqCst),
            "the refetch waits for the write in progress"
        );
        drop(writing);
        refetch.join().expect("join");
        assert!(
            t.write_guard(1).is_none(),
            "an older view may not write any more"
        );
        assert!(t.write_guard(2).is_some() && t.write_guard(3).is_some());
        assert!(!t.refetch_from(1), "a lower epoch claims nothing");
        assert!(!t.refetch_from(2), "the same epoch is claimed once");
        assert!(t.refetch_from(3), "a newer one is claimed again");
        assert!(t.write_guard(1).is_none(), "the bar never moves back");
    }
}
