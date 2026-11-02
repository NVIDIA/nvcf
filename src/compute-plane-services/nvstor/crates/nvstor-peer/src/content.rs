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

//! What a fetched copy remembers about where its bytes came from, so it
//! can serve them to the next fetcher under the same content identity.
//!
//! A fetch records, for every file it renamed into place, the file's
//! path under the destination, its size, the generation the local copy
//! had right after the rename (from `fstat`), and the `content` identity
//! from the manifest it was fetched under. It keeps that in
//! `DEST/.nvstor-peer-content` (version 2 of the manifest format of
//! `crate::manifest`, with the local generation in `generation`; no
//! attributes, which the files themselves carry).
//!
//! An export ([`crate::export::DirExport`]) serving that directory then
//! advertises `content` for each file whose generation is still the
//! recorded one, and the file's own generation for any other file (one
//! rewritten since, or never fetched): a file changed after the copy is
//! never presented as the original's bytes.
//!
//! A fetch also records which attributes it gave the tree, in
//! `DEST/.nvstor-peer-attrs` ([`ATTRS_FILE`], one line such as
//! `modes=1 owners=0`): the source's authority (`manifest::Authority`)
//! and'ed with what the fetch applied. It is written before the fetch
//! renames anything (and'ed with what was recorded, so an interrupted
//! fetch never raises it) and replaced with the fetch's own once it is
//! done, when DEST held nothing else when the fetch started (otherwise
//! and'ed with the record, so it never rises over files this fetch did
//! not place); both times and'ed with every record above DEST. An export
//! serves its version 3 manifest with the AND of every record from the
//! directory it lists up to `/` ([`authority_above`]; any error on the
//! way gives none) and of every record below it, so a tree
//! copied without owners (a fetch without CAP_CHOWN) or without modes
//! (`--no-attrs`), or any directory of it, does not pass the copier's
//! uid, or its private 0600, on as the origin's. No record means an
//! original tree: full authority. A record that cannot be read means
//! none, and so does one owned by anyone but the serving user or root.
//! Records are written under fresh random temporary names (O_CREAT |
//! O_EXCL | O_NOFOLLOW) and renamed into place.
//!
//! Every name starting with [`RESERVED_PREFIX`] belongs to nvstor-peer:
//! an export never lists one, and a fetch refuses to create one.

use std::collections::HashMap;
use std::fs::File;
use std::io::{self, Read, Write};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};

use tracing::warn;

use crate::dirfd;
use crate::manifest::{self, Authority, Entry};

/// Names nvstor-peer keeps for itself in a destination or export.
pub const RESERVED_PREFIX: &str = ".nvstor-peer-";
/// The record, at the root of a fetch destination.
pub const CONTENT_FILE: &str = ".nvstor-peer-content";
/// Where a new record is written before it is renamed over the old one.
/// A fixed name is safe: only the fetch holding the destination's lock
/// writes it.
const CONTENT_TMP: &str = ".nvstor-peer-content.tmp";
/// Which attributes the tree under a fetch destination really has.
pub const ATTRS_FILE: &str = ".nvstor-peer-attrs";
const ATTRS_TMP: &str = ".nvstor-peer-attrs.tmp";
/// Longest attributes record read back.
const MAX_ATTRS: u64 = 64;
/// Largest record read back (about two million files).
const MAX_RECORD: u64 = 256 << 20;

/// Recorded files by path: (local generation, content).
pub type Record = HashMap<String, (u128, u128)>;

/// Read the record under `root`; empty when there is none. A record that
/// cannot be read or decoded is ignored with a warning (every file then
/// serves under its own generation, which is safe).
pub fn load(root: &OwnedFd) -> Record {
    match read(root) {
        Ok(Some(entries)) => entries
            .into_iter()
            .filter(Entry::is_file)
            .map(|e| (e.path, (e.generation, e.content)))
            .collect(),
        Ok(None) => Record::new(),
        Err(e) => {
            warn!(error = %e, "ignoring an unreadable {CONTENT_FILE}");
            Record::new()
        }
    }
}

fn read(root: &OwnedFd) -> io::Result<Option<Vec<Entry>>> {
    let fd = match dirfd::openat2(
        root,
        CONTENT_FILE,
        libc::O_RDONLY | libc::O_NOFOLLOW | libc::O_NONBLOCK,
        libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS,
    ) {
        Ok(fd) => fd,
        Err(e) if e.raw_os_error() == Some(libc::ENOENT) => return Ok(None),
        Err(e) => return Err(e),
    };
    let st = dirfd::fstat(&fd)?;
    if !dirfd::is_reg(&st) {
        return Err(io::Error::other("not a regular file"));
    }
    if st.st_size as u64 > MAX_RECORD {
        return Err(io::Error::other(format!(
            "{} bytes, over the {MAX_RECORD}-byte limit",
            st.st_size
        )));
    }
    let mut bytes = Vec::with_capacity(st.st_size as usize);
    File::from(fd)
        .take(MAX_RECORD + 1)
        .read_to_end(&mut bytes)?;
    manifest::decode(&bytes)
        .map(|m| Some(m.entries))
        .map_err(io::Error::other)
}

/// Merge `fetched` (paths relative to `root`) into the record and
/// replace it atomically. Entries kept from the old record are dropped
/// unless their file is still there with the recorded generation.
pub fn record(root: &OwnedFd, fetched: &[Entry]) -> io::Result<()> {
    let mut merged: HashMap<String, Entry> = HashMap::new();
    for (path, (generation, content)) in load(root) {
        if let Some(size) = unchanged_size(root, &path, generation) {
            merged.insert(
                path.clone(),
                Entry {
                    size,
                    path,
                    generation,
                    content,
                    ..Entry::default()
                },
            );
        }
    }
    for e in fetched.iter().filter(|e| e.is_file()) {
        merged.insert(e.path.clone(), e.clone());
    }
    let mut entries: Vec<Entry> = merged.into_values().collect();
    entries.sort_by(|a, b| a.path.cmp(&b.path));
    replace(
        root,
        CONTENT_TMP,
        CONTENT_FILE,
        &manifest::encode_v2(&entries),
        true,
    )
    .map(drop)
}

/// The authority recorded under `root` (see the module docs).
pub fn load_authority(root: &OwnedFd) -> Authority {
    match read_authority(root) {
        Ok(a) => a,
        Err(e) => {
            warn!(error = %e, "an unreadable {ATTRS_FILE}: serving no attribute as authoritative");
            Authority::NONE
        }
    }
}

/// The authority that holds for the directory `dir`: the AND of every
/// record in `dir` and in each directory above it up to the filesystem
/// root (opened through `..` with O_PATH, which needs no read permission
/// and is never a symlink). Fail-safe: any error on the way (a directory
/// that cannot be opened or stat'ed, a record that cannot be read or does
/// not parse) gives no authority at all. Only reaching the root without
/// an error and without a record gives full authority. A record lowers,
/// never raises, so a planted one can only take authority away.
pub fn authority_above(dir: &OwnedFd) -> Authority {
    walk_up(dir, true)
}

/// [`authority_above`] of the parent of `dir` (full if `dir` is the root):
/// what holds over `dir` from outside it.
pub fn authority_above_parent(dir: &OwnedFd) -> Authority {
    walk_up(dir, false)
}

/// Directories a walk up goes through before it gives up (no path is
/// that deep).
const MAX_WALK: usize = 4096;

fn walk_up(dir: &OwnedFd, include_self: bool) -> Authority {
    let walked = (|| -> io::Result<Authority> {
        let mut cur = dirfd::openat2(dir, "", libc::O_PATH | libc::O_DIRECTORY, 0)?;
        let mut a = Authority::FULL;
        if !include_self {
            match step_up(&cur)? {
                Some(p) => cur = p,
                None => return Ok(a),
            }
        }
        for _ in 0..MAX_WALK {
            if let Some(r) = read_record(&cur)? {
                a = a.and(r);
            }
            match step_up(&cur)? {
                Some(p) => cur = p,
                None => return Ok(a),
            }
        }
        Err(io::Error::other(format!(
            "more than {MAX_WALK} directories above"
        )))
    })();
    walked.unwrap_or_else(|e| {
        warn!(error = %e, "cannot read every {ATTRS_FILE} above a directory: no attribute is authoritative");
        Authority::NONE
    })
}

/// The parent of `cur`, or `None` at the root (where `..` is itself).
fn step_up(cur: &OwnedFd) -> io::Result<Option<OwnedFd>> {
    let parent = dirfd::openat2(cur, "..", libc::O_PATH | libc::O_DIRECTORY, 0)?;
    let (a, b) = (dirfd::fstat(cur)?, dirfd::fstat(&parent)?);
    Ok((a.st_dev != b.st_dev || a.st_ino != b.st_ino).then_some(parent))
}

fn read_authority(root: &OwnedFd) -> io::Result<Authority> {
    Ok(read_record(root)?.unwrap_or(Authority::FULL))
}

/// Records written by anyone but this process's user or root do not
/// vouch for anything: they count as no authority.
fn trusted_owner(uid: u32) -> bool {
    #[cfg(test)]
    if let Some(me) = tests::SERVER_EUID.with(|f| f.get()) {
        return uid == me || uid == 0;
    }
    // SAFETY: geteuid has no arguments and cannot fail.
    uid == unsafe { libc::geteuid() } || uid == 0
}

/// The record in `dir`, if there is one (one of another user's reads as
/// no authority).
pub fn read_record(dir: &OwnedFd) -> io::Result<Option<Authority>> {
    let fd = match dirfd::openat2(
        dir,
        ATTRS_FILE,
        libc::O_RDONLY | libc::O_NOFOLLOW | libc::O_NONBLOCK,
        libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS,
    ) {
        Ok(fd) => fd,
        Err(e) if e.raw_os_error() == Some(libc::ENOENT) => return Ok(None),
        Err(e) => return Err(e),
    };
    let st = dirfd::fstat(&fd)?;
    if !dirfd::is_reg(&st) {
        return Err(io::Error::other("not a regular file"));
    }
    if !trusted_owner(st.st_uid) {
        warn!(
            uid = st.st_uid,
            "a {ATTRS_FILE} of another user: no attribute is authoritative under it"
        );
        return Ok(Some(Authority::NONE));
    }
    let mut text = String::new();
    File::from(fd).take(MAX_ATTRS).read_to_string(&mut text)?;
    let (mut modes, mut owners) = (None, None);
    for word in text.split_whitespace() {
        let flag = match word {
            "modes=1" | "modes=0" => &mut modes,
            "owners=1" | "owners=0" => &mut owners,
            _ => return Err(io::Error::other(format!("unknown field {word:?}"))),
        };
        if flag.replace(word.ends_with('1')).is_some() {
            return Err(io::Error::other(format!("repeated field {word:?}")));
        }
    }
    match (modes, owners) {
        (Some(modes), Some(owners)) => Ok(Some(Authority { modes, owners })),
        _ => Err(io::Error::other("expected modes= and owners=")),
    }
}

/// Replace the authority record under `root` atomically and durably.
pub fn record_authority(root: &OwnedFd, a: Authority) -> io::Result<()> {
    let f = replace(
        root,
        ATTRS_TMP,
        ATTRS_FILE,
        authority_text(a).as_bytes(),
        true,
    )?;
    drop(f);
    Ok(())
}

/// [`record_authority`] without syncing: the written record's file, for
/// the caller to sync (with others, in parallel: [`sync_all`]) before it
/// relies on the record surviving a crash. Until then a crash leaves the
/// old record, or an empty or partial one, which reads as no authority.
pub fn record_authority_unsynced(dir: &OwnedFd, a: Authority) -> io::Result<File> {
    replace(
        dir,
        ATTRS_TMP,
        ATTRS_FILE,
        authority_text(a).as_bytes(),
        false,
    )
}

/// Rewrite the record already in `dir` in place with `a` (truncate,
/// write, fsync): the fallback when a new one cannot be renamed in. Opened
/// without truncating, following a symlink or blocking (a FIFO without a
/// reader fails the open), and refused unless it is a regular file with
/// one link (a hard link's other name is never rewritten) owned by the
/// serving user or root (a record read back from anyone else's counts as
/// none anyway).
pub fn rewrite_record_in_place(dir: &OwnedFd, a: Authority) -> io::Result<()> {
    let fd = dirfd::openat2(
        dir,
        ATTRS_FILE,
        libc::O_WRONLY | libc::O_NOFOLLOW | libc::O_NONBLOCK,
        libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS,
    )?;
    let st = dirfd::fstat(&fd)?;
    if !dirfd::is_reg(&st) {
        return Err(io::Error::other("not a regular file"));
    }
    if st.st_nlink != 1 {
        return Err(io::Error::other(format!("{} links to it", st.st_nlink)));
    }
    if !trusted_owner(st.st_uid) {
        return Err(io::Error::other(format!("owned by uid {}", st.st_uid)));
    }
    let mut f = File::from(fd);
    f.set_len(0)?;
    f.write_all(authority_text(a).as_bytes())?;
    f.sync_all()
}

fn authority_text(a: Authority) -> String {
    format!(
        "modes={} owners={}\n",
        u8::from(a.modes),
        u8::from(a.owners)
    )
}

/// Writers [`sync_all`] uses at most.
const SYNC_THREADS: usize = 16;

/// fsync every one of `fds` (files or directories), on up to
/// `SYNC_THREADS` threads; the first error, if any.
pub fn sync_all(fds: &[OwnedFd]) -> io::Result<()> {
    if fds.is_empty() {
        return Ok(());
    }
    let per = fds.len().div_ceil(SYNC_THREADS.min(fds.len()));
    let sync = |chunk: &[OwnedFd]| -> io::Result<()> {
        for fd in chunk {
            // SAFETY: valid descriptor.
            if unsafe { libc::fsync(fd.as_raw_fd()) } != 0 {
                return Err(io::Error::last_os_error());
            }
        }
        Ok(())
    };
    std::thread::scope(|s| {
        let mut first: io::Result<()> = Ok(());
        let mut workers = Vec::new();
        for chunk in fds.chunks(per) {
            // A thread that cannot be made (out of threads or memory) is
            // no reason to give up: that share is synced on this thread.
            match spawn_sync(s, move || sync(chunk)) {
                Ok(w) => workers.push(w),
                Err(e) => {
                    warn!(error = %e, "cannot start a sync thread; syncing on this one");
                    first = first.and(sync(chunk));
                }
            }
        }
        for w in workers {
            first = first.and(
                w.join()
                    .unwrap_or_else(|_| Err(io::Error::other("sync thread panicked"))),
            );
        }
        first
    })
}

fn spawn_sync<'s, 'e, F>(
    s: &'s std::thread::Scope<'s, 'e>,
    f: F,
) -> io::Result<std::thread::ScopedJoinHandle<'s, io::Result<()>>>
where
    F: FnOnce() -> io::Result<()> + Send + 's,
{
    #[cfg(test)]
    if tests::FAIL_SPAWN.with(|f| f.get()) {
        return Err(io::Error::other("no threads (test)"));
    }
    std::thread::Builder::new()
        .name("nvstor-peer-sync".into())
        .spawn_scoped(s, f)
}

/// Write `bytes` to a new temporary file under `root`, sync it and
/// rename it over `name`. The temporary name is `<tmp>-<random>`, created
/// with O_CREAT | O_EXCL | O_NOFOLLOW: whatever someone placed at a name
/// (a FIFO that would block the open, a hard link or symlink that would
/// be written through) makes the create fail rather than being opened,
/// and another random name is tried. Stale ones are removed at the start
/// of a fetch ([`remove_stale_temps`]).
fn replace(root: &OwnedFd, tmp: &str, name: &str, bytes: &[u8], sync: bool) -> io::Result<File> {
    let mut last = io::Error::other("no attempt");
    for _ in 0..TEMP_ATTEMPTS {
        let tmp = dirfd::cstring(format!("{tmp}{TEMP_MARK}{:016x}", temp_suffix()?).as_bytes())?;
        // SAFETY: valid dirfd and C string; the descriptor is owned below.
        let fd = unsafe {
            libc::openat(
                root.as_raw_fd(),
                tmp.as_ptr(),
                libc::O_WRONLY | libc::O_CREAT | libc::O_EXCL | libc::O_NOFOLLOW | libc::O_CLOEXEC,
                0o644 as libc::c_uint,
            )
        };
        if fd < 0 {
            last = io::Error::last_os_error();
            if last.raw_os_error() == Some(libc::EEXIST) {
                continue;
            }
            return Err(last);
        }
        // SAFETY: `fd` is a fresh descriptor nothing else owns.
        let mut f = File::from(unsafe { OwnedFd::from_raw_fd(fd) });
        let written = f
            .write_all(bytes)
            .and_then(|()| if sync { f.sync_data() } else { Ok(()) })
            .and_then(|()| dirfd::rename_at(root, &tmp, root, &dirfd::cstring(name.as_bytes())?));
        if let Err(e) = written {
            let _ = dirfd::unlink_at(root, &tmp, 0);
            return Err(e);
        }
        return Ok(f);
    }
    Err(last)
}

/// Marks a temporary name: `<record>.tmp-<16 hex digits>`.
const TEMP_MARK: &str = "-";
/// Names tried before giving up on finding a free one.
const TEMP_ATTEMPTS: u32 = 8;

fn temp_suffix() -> io::Result<u64> {
    #[cfg(test)]
    if let Some(fixed) = tests::FIXED_SUFFIX.with(|f| f.get()) {
        return Ok(fixed);
    }
    let mut r = [0u8; 8];
    // SAFETY: the pointer and length name `r`.
    if unsafe { libc::getrandom(r.as_mut_ptr().cast(), r.len(), 0) } != r.len() as isize {
        return Err(io::Error::last_os_error());
    }
    Ok(u64::from_le_bytes(r))
}

/// Remove the temporary names a fetch that died left under `root`
/// (`.nvstor-peer-content.tmp*`, `.nvstor-peer-attrs.tmp*`), whatever
/// they are: unlinking a name never follows it or opens it. Only called
/// with the destination's lock held.
pub fn remove_stale_temps(root: &OwnedFd) -> io::Result<()> {
    for n in dirfd::names(root)? {
        let Ok(s) = n.to_str() else { continue };
        if [CONTENT_TMP, ATTRS_TMP].iter().any(|t| s.starts_with(t)) {
            match dirfd::unlink_at(root, &n, 0) {
                Ok(()) => warn!(
                    name = s,
                    "removed a temporary record of an interrupted fetch"
                ),
                Err(e) => {
                    warn!(name = s, error = %e, "cannot remove a temporary record of an interrupted fetch")
                }
            }
        }
    }
    Ok(())
}

/// The size of `path` under `root` if it is a regular file that still
/// has `generation`.
fn unchanged_size(root: &OwnedFd, path: &str, generation: u128) -> Option<u64> {
    if !manifest::is_safe_relative(path) {
        return None;
    }
    let fd = dirfd::openat2(
        root,
        path,
        libc::O_RDONLY | libc::O_NOFOLLOW | libc::O_NONBLOCK,
        libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS,
    )
    .ok()?;
    let st = dirfd::fstat(&fd).ok()?;
    (dirfd::is_reg(&st) && crate::export::generation_of(&st) == generation)
        .then_some(st.st_size as u64)
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    fn gen_of(root: &std::path::Path, rel: &str) -> u128 {
        let fd = dirfd::open_dir(root).expect("root");
        let f = dirfd::openat2(&fd, rel, libc::O_RDONLY, libc::RESOLVE_BENEATH).expect("open");
        crate::export::generation_of(&dirfd::fstat(&f).expect("fstat"))
    }

    #[test]
    fn records_merge_and_drop_files_that_changed() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        std::fs::create_dir_all(d.path().join("sub")).expect("mkdir");
        std::fs::write(d.path().join("a"), b"aa").expect("write");
        std::fs::write(d.path().join("sub/b"), b"bbb").expect("write");
        let root = dirfd::open_dir(d.path()).expect("root");
        assert!(load(&root).is_empty(), "no record yet");

        let a = Entry {
            path: "a".into(),
            size: 2,
            generation: gen_of(d.path(), "a"),
            content: 11,
            ..Entry::default()
        };
        record(&root, std::slice::from_ref(&a)).expect("record a");
        let b = Entry {
            path: "sub/b".into(),
            size: 3,
            generation: gen_of(d.path(), "sub/b"),
            content: 22,
            ..Entry::default()
        };
        record(&root, std::slice::from_ref(&b)).expect("record b");
        let got = load(&root);
        assert_eq!(got.get("a"), Some(&(a.generation, 11)));
        assert_eq!(got.get("sub/b"), Some(&(b.generation, 22)));

        // `a` is rewritten: the next record drops it.
        std::thread::sleep(std::time::Duration::from_millis(10));
        std::fs::write(d.path().join("a"), b"AA").expect("rewrite");
        record(&root, &[]).expect("record nothing");
        let got = load(&root);
        assert!(!got.contains_key("a"), "{got:?}");
        assert!(got.contains_key("sub/b"));
        assert!(no_temps(d.path()));
        // Identities only: version 2 of the manifest format, which older
        // nvstor-peer binaries read too.
        let bytes = std::fs::read(d.path().join(CONTENT_FILE)).expect("record");
        assert_eq!(manifest::decode(&bytes).expect("decode").version, 2);
    }

    thread_local! {
        /// Makes this thread's temporary names predictable.
        pub(crate) static FIXED_SUFFIX: std::cell::Cell<Option<u64>> = const { std::cell::Cell::new(None) };
        /// Makes `spawn_sync` fail on this thread.
        pub(crate) static FAIL_SPAWN: std::cell::Cell<bool> = const { std::cell::Cell::new(false) };
        /// The user this thread's reads take for the server's.
        pub(crate) static SERVER_EUID: std::cell::Cell<Option<u32>> = const { std::cell::Cell::new(None) };
    }

    fn no_temps(dir: &std::path::Path) -> bool {
        std::fs::read_dir(dir)
            .expect("dir")
            .flatten()
            .all(|e| !e.file_name().to_string_lossy().contains(".tmp"))
    }

    #[test]
    fn a_planted_temporary_name_is_never_opened() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let outside = tempfile::tempdir_in(crate::test_tmp()).expect("outside");
        let target = outside.path().join("target");
        std::fs::write(&target, b"keep").expect("write");
        let p = d.path().to_path_buf();
        // Every name this thread will try is taken: a FIFO (an open for
        // writing would block with no reader) and a symlink out of the
        // tree (an open would write through it).
        let planted = format!("{TEMP_MARK}{:016x}", 7u64);
        let fifo =
            std::ffi::CString::new(format!("{}/{ATTRS_TMP}{planted}", p.display())).expect("path");
        // SAFETY: valid C string.
        assert_eq!(unsafe { libc::mkfifo(fifo.as_ptr(), 0o644) }, 0, "mkfifo");
        std::os::unix::fs::symlink(&target, p.join(format!("{CONTENT_TMP}{planted}")))
            .expect("symlink");
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            FIXED_SUFFIX.with(|f| f.set(Some(7)));
            let root = dirfd::open_dir(&p).expect("root");
            let a = record_authority(&root, Authority::NONE).map_err(|e| e.raw_os_error());
            let c = replace(&root, CONTENT_TMP, CONTENT_FILE, b"x", true)
                .map(drop)
                .map_err(|e| e.raw_os_error());
            let _ = tx.send((a, c));
        });
        let (a, c) = rx
            .recv_timeout(std::time::Duration::from_secs(5))
            .expect("blocked on a planted FIFO");
        assert_eq!((a, c), (Err(Some(libc::EEXIST)), Err(Some(libc::EEXIST))));
        assert_eq!(
            std::fs::read(&target).expect("target"),
            b"keep",
            "nothing written through the symlink"
        );
        // A fetch's start removes them, whatever they are; the target stays.
        let root = dirfd::open_dir(d.path()).expect("root");
        std::fs::write(d.path().join(ATTRS_TMP), b"old fixed name").expect("write");
        remove_stale_temps(&root).expect("remove");
        assert!(no_temps(d.path()));
        assert_eq!(std::fs::read(&target).expect("target"), b"keep");
        record_authority(&root, Authority::NONE).expect("random names work");
        assert_eq!(load_authority(&root), Authority::NONE);
    }

    #[test]
    fn a_record_is_rewritten_in_place_only_when_it_is_its_own_regular_file() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let root = dirfd::open_dir(d.path()).expect("root");
        let rec = d.path().join(ATTRS_FILE);
        // Its own regular file: rewritten.
        record_authority(&root, Authority::FULL).expect("record");
        rewrite_record_in_place(&root, Authority::NONE).expect("in place");
        assert_eq!(load_authority(&root), Authority::NONE);
        // A hard link to something else: refused, the other name untouched.
        let other = d.path().join("other");
        std::fs::remove_file(&rec).expect("rm");
        std::fs::write(&other, b"keep").expect("write");
        std::fs::hard_link(&other, &rec).expect("link");
        assert!(rewrite_record_in_place(&root, Authority::NONE).is_err());
        assert_eq!(std::fs::read(&other).expect("other"), b"keep");
        std::fs::remove_file(&rec).expect("rm");
        // Another user's: refused. As root the record is really someone
        // else's; otherwise the server takes another uid for its own.
        record_authority(&root, Authority::FULL).expect("record");
        // SAFETY: no arguments, cannot fail.
        let me = unsafe { libc::geteuid() };
        let theirs = if me == 0 {
            std::os::unix::fs::lchown(&rec, Some(4321), Some(4321)).expect("chown");
            rewrite_record_in_place(&root, Authority::NONE)
        } else {
            SERVER_EUID.with(|u| u.set(Some(me + 1)));
            let theirs = rewrite_record_in_place(&root, Authority::NONE);
            SERVER_EUID.with(|u| u.set(None));
            theirs
        };
        assert!(theirs.is_err());
        assert_eq!(std::fs::read(&rec).expect("record"), b"modes=1 owners=1\n");
        std::fs::remove_file(&rec).expect("rm");
        // A FIFO at the name: refused at once, not blocked on.
        let fifo = std::ffi::CString::new(rec.as_os_str().as_encoded_bytes()).expect("path");
        // SAFETY: valid C string.
        assert_eq!(unsafe { libc::mkfifo(fifo.as_ptr(), 0o644) }, 0, "mkfifo");
        let p = d.path().to_path_buf();
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let root = dirfd::open_dir(&p).expect("root");
            let _ = tx.send(rewrite_record_in_place(&root, Authority::NONE).is_err());
        });
        assert!(rx
            .recv_timeout(std::time::Duration::from_secs(5))
            .expect("blocked on a FIFO"));
    }

    #[test]
    fn records_are_synced_even_when_no_thread_can_be_started() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let fds: Vec<OwnedFd> = (0..40)
            .map(|i| {
                let p = d.path().join(format!("f{i}"));
                std::fs::write(&p, b"x").expect("write");
                OwnedFd::from(File::open(&p).expect("open"))
            })
            .collect();
        sync_all(&fds).expect("threads");
        FAIL_SPAWN.with(|f| f.set(true));
        let r = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| sync_all(&fds)));
        FAIL_SPAWN.with(|f| f.set(false));
        r.expect("no panic").expect("synced on this thread");
    }

    #[test]
    fn the_authority_record_round_trips_and_fails_safe() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let root = dirfd::open_dir(d.path()).expect("root");
        assert_eq!(load_authority(&root), Authority::FULL, "an original tree");
        for a in [
            Authority::NONE,
            Authority {
                modes: true,
                owners: false,
            },
            Authority::FULL,
        ] {
            record_authority(&root, a).expect("record");
            assert_eq!(load_authority(&root), a);
        }
        assert!(no_temps(d.path()));
        for garbage in [
            "",
            "modes=1",
            "modes=1 owners=2",
            "x=1 owners=1",
            "modes=1 owners=1 more",
            "modes=1 modes=1",
        ] {
            std::fs::write(d.path().join(ATTRS_FILE), garbage).expect("write");
            assert_eq!(load_authority(&root), Authority::NONE, "{garbage:?}");
        }
        std::fs::remove_file(d.path().join(ATTRS_FILE)).expect("rm");
        std::os::unix::fs::symlink("/etc/hostname", d.path().join(ATTRS_FILE)).expect("symlink");
        assert_eq!(
            load_authority(&root),
            Authority::NONE,
            "a symlinked record is not followed"
        );
    }

    #[test]
    fn a_garbage_or_symlinked_record_is_ignored() {
        let d = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let root = dirfd::open_dir(d.path()).expect("root");
        std::fs::write(d.path().join(CONTENT_FILE), b"not a manifest").expect("write");
        assert!(load(&root).is_empty());
        std::fs::remove_file(d.path().join(CONTENT_FILE)).expect("rm");
        let outside = tempfile::tempdir_in(crate::test_tmp()).expect("outside");
        let real = outside.path().join("rec");
        std::fs::write(
            &real,
            manifest::encode_v2(&[Entry {
                path: "x".into(),
                size: 1,
                generation: 1,
                content: 2,
                ..Entry::default()
            }]),
        )
        .expect("write");
        std::os::unix::fs::symlink(&real, d.path().join(CONTENT_FILE)).expect("symlink");
        assert!(load(&root).is_empty(), "a symlinked record is not followed");
    }
}
