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

//! Serve a local directory, read-only, through the object read callback
//! (`nvstor_rdma::object::ReadFn`) that the RDMA transport calls with a
//! registered buffer.
//!
//! - A file's key is its path relative to the export root. Every open is
//!   `openat2(root, key, RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS)`, so
//!   neither `..` nor a symlink can reach outside the root. Opens are
//!   O_NONBLOCK and anything but a regular file is refused before a read,
//!   so a FIFO or device node under the root cannot hang a worker.
//! - A file's generation is derived from `fstat` of the descriptor the
//!   bytes are read from (device, inode, size, mtime, ctime). It is taken
//!   before and after every read, and a read during which it changed is
//!   repeated, so the bytes and the version reported with them belong to
//!   the same file contents. Rewriting or replacing the file changes it,
//!   and a client fetching ranges pinned to the old generation gets
//!   GENERATION_CHANGED. (A rewrite that changes neither the size nor a
//!   timestamp, within one timestamp tick, is invisible to any stat-based
//!   version; kernels with multigrain ctime close that window.)
//! - Reads into a 4 KiB-aligned buffer at an aligned offset with an
//!   aligned length use O_DIRECT: the device writes straight into the
//!   registered buffer the NIC sends from, and the page cache is left
//!   alone. Anything else, or a filesystem that refuses O_DIRECT, is read
//!   buffered.
//! - `manifest::key_v3(prefix)` returns the list of regular files and
//!   directories under `prefix` (version 3), each with its mode, mtime,
//!   owner and group, and the tree's recorded authority over them
//!   (`crate::content`); `manifest::key(prefix)` the version 2 list of the
//!   same files, without directories or attributes, for older fetchers.
//!   Attributes are `manifest::Attrs` from the walk's `lstat`; symlinks,
//!   special files and fetch staging directories are skipped and logged. The
//!   directory `prefix` itself is not listed. The walk opens every directory
//!   from the root fd without following symlinks and skips entries that
//!   vanish while it runs. The list is built when stat-ed and served from
//!   a cache for its ranges, so a multi-range manifest is one consistent
//!   listing. Names starting with `crate::content::RESERVED_PREFIX`
//!   (fetch staging directories, a fetched copy's content record) are
//!   never listed or served.
//! - Each manifest entry carries a `content` identity next to its
//!   generation: the generation itself for an original file, or, for a
//!   file a fetch put here and nobody changed since, the identity it was
//!   fetched under (`crate::content`). Fetchers that move between sources
//!   compare that, not generations.
//! - [`DirExport::open_as`] serves the root under a path prefix, so a
//!   replica that fetched `--prefix P` into a directory serves it as `P`,
//!   the same keys as the node it fetched from.

use std::collections::HashMap;
use std::io;
use std::os::fd::{AsRawFd, OwnedFd};
use std::path::{Path, PathBuf};
use std::sync::Arc;

use nvstor_rdma::object::ObjectRead;
use parking_lot::Mutex;
use sha2::{Digest, Sha256};
use tracing::warn;

use crate::content::{self, RESERVED_PREFIX};
use crate::dirfd;
use crate::manifest::{self, Attrs, Authority, Entry, Kind};

const DIRECT_ALIGN: usize = 4096;
/// Manifests kept for range reads; listings of more prefixes than this
/// at once rebuild on demand.
const MANIFEST_CACHE: usize = 16;
/// Reads of one range before a file that changes during every one of
/// them is reported as an error.
const READ_ATTEMPTS: u32 = 4;

pub struct DirExport {
    root: PathBuf,
    root_fd: OwnedFd,
    /// Key prefix the root is served under ("" for none).
    mount: String,
    manifests: ManifestCache,
}

/// Encoded manifests by key: (the identity they were built for, the bytes).
type ManifestCache = Mutex<HashMap<String, Arc<(u128, Vec<u8>)>>>;

impl DirExport {
    pub fn open(root: &Path) -> io::Result<Arc<Self>> {
        Self::open_as(root, "")
    }

    /// Serve `root` as the directory `mount` (a relative path; "" for the
    /// export root): file `x` under `root` has key `mount/x`, and the
    /// manifest of `mount`, of any directory below it, or of any parent
    /// of it lists the files with those keys.
    pub fn open_as(root: &Path, mount: &str) -> io::Result<Arc<Self>> {
        let mount = mount.trim_end_matches('/');
        if !mount.is_empty() && !manifest::is_safe_relative(mount) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidInput,
                format!("{mount:?} is not a plain relative path"),
            ));
        }
        let root = root.canonicalize()?;
        let root_fd = dirfd::open_dir(&root)?;
        Ok(Arc::new(Self {
            root,
            root_fd,
            mount: mount.to_string(),
            manifests: Mutex::new(HashMap::new()),
        }))
    }

    pub fn root(&self) -> &Path {
        &self.root
    }

    /// The key prefix the root is served under ("" for none).
    pub fn mount(&self) -> &str {
        &self.mount
    }

    /// The `ReadFn` contract: read up to `buf.len()` bytes of `key` at
    /// `offset`; an empty `buf` is a stat.
    pub fn read(&self, key: &str, offset: u64, buf: &mut [u8]) -> io::Result<ObjectRead> {
        if let Some((version, prefix)) = manifest::listing_of(key) {
            return self.read_manifest(key, version, prefix, offset, buf);
        }
        if !manifest::is_safe_relative(key) {
            return Err(io::Error::new(io::ErrorKind::NotFound, "not a file key"));
        }
        let key = self
            .under_mount(key)
            .filter(|k| !k.split('/').any(|c| c.starts_with(RESERVED_PREFIX)))
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "not a file key"))?;
        let aligned = offset.is_multiple_of(DIRECT_ALIGN as u64)
            && buf.len().is_multiple_of(DIRECT_ALIGN)
            && !buf.is_empty()
            && (buf.as_ptr() as usize).is_multiple_of(DIRECT_ALIGN);
        let fd = if aligned {
            match self.open_beneath(key, libc::O_DIRECT) {
                Err(e) if e.raw_os_error() == Some(libc::EINVAL) => self.open_beneath(key, 0)?,
                r => r?,
            }
        } else {
            self.open_beneath(key, 0)?
        };
        let first = stat_fd(&fd)?;
        let size = first.0;
        if buf.is_empty() || offset >= size {
            return Ok(ObjectRead {
                len: 0,
                size,
                generation: first.1,
            });
        }
        let (len, size, generation) = consistent(
            first,
            || stat_fd(&fd),
            |size| {
                let want = buf
                    .len()
                    .min(usize::try_from(size.saturating_sub(offset)).unwrap_or(usize::MAX));
                if want == 0 {
                    return Ok(0);
                }
                // With O_DIRECT the request stays block-sized (the whole
                // aligned buffer); the read ends early at end of file.
                let request = if aligned { buf.len() } else { want };
                Ok(pread_full(&fd, &mut buf[..request], offset, want)?.min(want))
            },
        )?;
        Ok(ObjectRead {
            len,
            size,
            generation,
        })
    }

    /// Open a regular file beneath the root. O_NONBLOCK keeps the open
    /// of a FIFO or device node from blocking (it is refused after the
    /// `fstat`); it is cleared again before any read.
    fn open_beneath(&self, key: &str, extra: i32) -> io::Result<OwnedFd> {
        let fd = dirfd::openat2(
            &self.root_fd,
            key,
            libc::O_RDONLY | libc::O_NONBLOCK | extra,
            libc::RESOLVE_BENEATH | libc::RESOLVE_NO_MAGICLINKS,
        )
        .map_err(|e| match e.raw_os_error() {
            // Escapes (EXDEV) look like missing files to the client.
            Some(libc::EXDEV) | Some(libc::ELOOP) => io::Error::new(io::ErrorKind::NotFound, e),
            _ => e,
        })?;
        stat_fd(&fd)?;
        // SAFETY: valid descriptor; F_SETFL gets flags read by F_GETFL.
        let flags = unsafe { libc::fcntl(fd.as_raw_fd(), libc::F_GETFL) };
        if flags < 0
            || unsafe { libc::fcntl(fd.as_raw_fd(), libc::F_SETFL, flags & !libc::O_NONBLOCK) } < 0
        {
            return Err(io::Error::last_os_error());
        }
        Ok(fd)
    }

    /// `key` relative to the root, if it is below the mount.
    fn under_mount<'k>(&self, key: &'k str) -> Option<&'k str> {
        if self.mount.is_empty() {
            return Some(key);
        }
        key.strip_prefix(self.mount.as_str())?.strip_prefix('/')
    }

    /// The directory under the root a manifest of `prefix` lists, or
    /// `None` when `prefix` is outside the mount.
    fn listed_dir<'p>(&self, prefix: &'p str) -> Option<&'p str> {
        if self.mount.is_empty() || prefix == self.mount {
            return Some(if self.mount.is_empty() { prefix } else { "" });
        }
        if let Some(below) = self.under_mount(prefix) {
            return Some(below);
        }
        // A parent of the mount (or the whole export) lists everything.
        (prefix.is_empty()
            || self
                .mount
                .strip_prefix(prefix)
                .is_some_and(|r| r.starts_with('/')))
        .then_some("")
    }

    /// Serve the listing `key` names: version 3 (directories, attributes
    /// and the authority recorded for this tree, `crate::content`) or
    /// version 2 (files only), for fetchers of either version.
    fn read_manifest(
        &self,
        key: &str,
        version: u32,
        prefix: &str,
        offset: u64,
        buf: &mut [u8],
    ) -> io::Result<ObjectRead> {
        let cached = if buf.is_empty() {
            None
        } else {
            self.manifests.lock().get(key).cloned()
        };
        let m = match cached {
            Some(m) => m,
            None => {
                let (entries, authority) = self.list(prefix)?;
                let bytes = if version >= manifest::VERSION {
                    manifest::encode(&entries, authority)
                } else {
                    manifest::encode_v2(&entries)
                };
                let m = Arc::new((nonzero(&Sha256::digest(&bytes)), bytes));
                let mut cache = self.manifests.lock();
                if cache.len() >= MANIFEST_CACHE && !cache.contains_key(key) {
                    cache.clear();
                }
                cache.insert(key.to_string(), Arc::clone(&m));
                m
            }
        };
        let (generation, bytes) = (&m.0, &m.1);
        let size = bytes.len() as u64;
        let start = offset.min(size) as usize;
        let len = buf.len().min(bytes.len() - start);
        buf[..len].copy_from_slice(&bytes[start..start + len]);
        Ok(ObjectRead {
            len,
            size,
            generation: *generation,
        })
    }

    /// Regular files and directories under `prefix` (not `prefix`
    /// itself), sorted by path, each with its attributes from the same
    /// `stat` as its size.
    ///
    /// Every directory is opened from the root fd with `RESOLVE_BENEATH |
    /// RESOLVE_NO_SYMLINKS`, so neither the prefix nor the walk passes
    /// through a symlink, even one swapped in mid-walk. An entry that
    /// vanishes or changes type between readdir and the open (a writer
    /// renaming its temp files) is skipped: the listing is of the files
    /// that exist, not an error.
    fn list(&self, prefix: &str) -> io::Result<(Vec<Entry>, Authority)> {
        if !prefix.is_empty() && !manifest::is_safe_relative(prefix) {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                "not a directory key",
            ));
        }
        let Some(prefix) = self.listed_dir(prefix) else {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                format!("{prefix:?}: not under the export"),
            ));
        };
        if prefix.split('/').any(|c| c.starts_with(RESERVED_PREFIX)) {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                "not a directory key",
            ));
        }
        let record = content::load(&self.root_fd);
        let mut out = Vec::new();
        let mut authority = None;
        let mut skipped = 0usize;
        let mut stack = vec![prefix.to_string()];
        while let Some(rel) = stack.pop() {
            let dir = match dirfd::open_dir_beneath(&self.root_fd, &rel) {
                Ok(d) => d,
                Err(e) if rel == prefix => {
                    return Err(match e.raw_os_error() {
                        Some(libc::ENOENT | libc::ENOTDIR | libc::ELOOP | libc::EXDEV) => {
                            io::Error::new(
                                io::ErrorKind::NotFound,
                                format!("{prefix:?}: not a directory under the root"),
                            )
                        }
                        _ => e,
                    })
                }
                Err(e) if vanished(&e) => {
                    skipped += 1;
                    continue;
                }
                Err(e) => return Err(e),
            };
            if rel == prefix {
                // Every record at or above the listed directory.
                authority = Some(content::authority_above(&dir));
                #[cfg(test)]
                tests::between_reads();
            }
            for name in dirfd::names(&dir)? {
                let Ok(name_str) = name.to_str() else {
                    skipped += 1;
                    continue;
                };
                let path = if rel.is_empty() {
                    name_str.to_string()
                } else {
                    format!("{rel}/{name_str}")
                };
                let Some(st) = dirfd::stat_at(&dir, &name)? else {
                    continue;
                };
                if name_str.starts_with(RESERVED_PREFIX) {
                    // Fetch staging, a content record: nvstor-peer's own.
                    // A copy below the listed directory carries its own
                    // record: what it lacks, the listing lacks.
                    if name_str == content::ATTRS_FILE && rel != prefix {
                        authority =
                            authority.map(|a: Authority| a.and(content::load_authority(&dir)));
                    }
                    continue;
                }
                let key = if self.mount.is_empty() {
                    path.clone()
                } else {
                    format!("{}/{path}", self.mount)
                };
                if key.len() > u16::MAX as usize {
                    skipped += 1;
                } else if dirfd::is_dir(&st) {
                    out.push(Entry {
                        path: key,
                        kind: Kind::Dir,
                        attrs: Attrs::of(&st),
                        ..Entry::default()
                    });
                    stack.push(path);
                } else if dirfd::is_reg(&st) {
                    let generation = generation_of(&st);
                    let content = match record.get(&path) {
                        Some(&(recorded, content)) if recorded == generation => content,
                        _ => generation,
                    };
                    out.push(Entry {
                        size: st.st_size as u64,
                        generation,
                        content,
                        path: key,
                        kind: Kind::File,
                        attrs: Attrs::of(&st),
                    });
                } else {
                    // Symlinks, special files.
                    skipped += 1;
                }
            }
        }
        if skipped > 0 {
            warn!(prefix, skipped, "manifest skips entries that are not regular files or directories, \
                                    not UTF-8, with paths too long for a key, or changed during the walk");
        }
        out.sort_by(|a, b| a.path.cmp(&b.path));
        // Again after the walk: a fetch into this tree may have started
        // (written its lowered record, renamed its files in) while it ran,
        // and what was listed must not be served under the authority from
        // before. Records only lower, so the AND of both holds for all of
        // it.
        let after = dirfd::open_dir_beneath(&self.root_fd, prefix)
            .map_or(Authority::NONE, |d| content::authority_above(&d));
        Ok((out, authority.map_or(Authority::NONE, |a| a.and(after))))
    }
}

/// A directory removed, or replaced by a file or symlink, since it was
/// listed.
fn vanished(e: &io::Error) -> bool {
    matches!(
        e.raw_os_error(),
        Some(libc::ENOENT | libc::ENOTDIR | libc::ELOOP)
    )
}

/// Read under a stable generation: `stat` before and after `read`, and
/// read again if they differ, so the bytes returned were read while the
/// file had the generation returned with them. A file rewritten during
/// the read is read again; one that changes during every attempt is an
/// error rather than torn bytes. The server compares the generation with
/// the one the client pinned, so a file rewritten since the manifest
/// still fails the range with GENERATION_CHANGED.
fn consistent(
    first: (u64, u128),
    mut stat: impl FnMut() -> io::Result<(u64, u128)>,
    mut read: impl FnMut(u64) -> io::Result<usize>,
) -> io::Result<(usize, u64, u128)> {
    let mut before = first;
    for _ in 0..READ_ATTEMPTS {
        let len = read(before.0)?;
        let after = stat()?;
        if after == before {
            return Ok((len, after.0, after.1));
        }
        before = after;
    }
    Err(io::Error::other(format!(
        "file changed during each of {READ_ATTEMPTS} reads"
    )))
}

/// (size, generation) of an open regular file.
fn stat_fd(fd: &OwnedFd) -> io::Result<(u64, u128)> {
    let st = dirfd::fstat(fd)?;
    if !dirfd::is_reg(&st) {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            "not a regular file",
        ));
    }
    Ok((st.st_size as u64, generation_of(&st)))
}

/// A file's version token on this node, from its `stat`.
pub(crate) fn generation_of(st: &libc::stat) -> u128 {
    generation(
        st.st_dev,
        st.st_ino,
        st.st_size as u64,
        st.st_mtime,
        st.st_mtime_nsec,
        st.st_ctime,
        st.st_ctime_nsec,
    )
}

fn generation(
    dev: u64,
    ino: u64,
    size: u64,
    mtime: i64,
    mtime_ns: i64,
    ctime: i64,
    ctime_ns: i64,
) -> u128 {
    let mut h = Sha256::new();
    for v in [dev, ino, size] {
        h.update(v.to_le_bytes());
    }
    for v in [mtime, mtime_ns, ctime, ctime_ns] {
        h.update(v.to_le_bytes());
    }
    nonzero(&h.finalize())
}

/// First 16 bytes of a digest as a generation; never 0 (0 means "any").
fn nonzero(digest: &[u8]) -> u128 {
    let mut a = [0u8; 16];
    a.copy_from_slice(&digest[..16]);
    u128::from_le_bytes(a).max(1)
}

/// pread into `buf` until `stop` bytes are in or end of file. `buf` may
/// be longer than `stop` (an O_DIRECT read keeps block-sized requests);
/// stopping at `stop` keeps the loop from retrying at the unaligned
/// end-of-file offset, which O_DIRECT rejects.
fn pread_full(fd: &OwnedFd, buf: &mut [u8], offset: u64, stop: usize) -> io::Result<usize> {
    let mut done = 0usize;
    while done < stop.min(buf.len()) {
        let at = libc::off_t::try_from(offset + done as u64)
            .map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "offset beyond off_t"))?;
        // SAFETY: the pointer and length name the unfilled tail of `buf`.
        let n = unsafe {
            libc::pread(
                fd.as_raw_fd(),
                buf[done..].as_mut_ptr().cast(),
                buf.len() - done,
                at,
            )
        };
        match n {
            0 => break,
            n if n > 0 => done += n as usize,
            _ => {
                let e = io::Error::last_os_error();
                if e.kind() != io::ErrorKind::Interrupted {
                    return Err(e);
                }
            }
        }
    }
    Ok(done)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::fetch::STAGING_PREFIX;
    use std::ffi::CString;
    use std::os::unix::ffi::OsStrExt;

    /// 4 KiB-aligned scratch buffer, so tests exercise the O_DIRECT path
    /// where the filesystem supports it.
    struct Aligned(Vec<u8>, usize);
    impl Aligned {
        fn new(len: usize) -> Self {
            let v = vec![0u8; len + DIRECT_ALIGN];
            let off = (DIRECT_ALIGN - v.as_ptr() as usize % DIRECT_ALIGN) % DIRECT_ALIGN;
            Self(v, off)
        }
        fn get(&mut self, len: usize) -> &mut [u8] {
            &mut self.0[self.1..self.1 + len]
        }
    }

    thread_local! {
        /// Runs between the first authority read of a listing and its walk.
        static BETWEEN: std::cell::RefCell<Option<Box<dyn Fn()>>> = const { std::cell::RefCell::new(None) };
    }

    pub(super) fn between_reads() {
        BETWEEN.with(|b| {
            if let Some(f) = b.borrow().as_ref() {
                f();
            }
        });
    }

    #[test]
    fn a_fetch_that_starts_during_a_listing_lowers_it() {
        let (d, e) = tree();
        let root = d.path().to_path_buf();
        // Between the first read and the walk: a fetch into this tree
        // writes its lowered record and renames a file of the copier's in.
        BETWEEN.with(|b| {
            *b.borrow_mut() = Some(Box::new(move || {
                let fd = dirfd::open_dir(&root).expect("root");
                content::record_authority(
                    &fd,
                    Authority {
                        modes: true,
                        owners: false,
                    },
                )
                .expect("record");
                std::fs::write(root.join("ckpt/copied"), b"c").expect("write");
            }))
        });
        let listed = manifest::decode(&fetch_all(&e, &manifest::key_v3("")))
            .map(|m| (m.authority, m.entries));
        BETWEEN.with(|b| *b.borrow_mut() = None);
        let (authority, entries) = listed.expect("listing");
        assert!(
            entries.iter().any(|x| x.path == "ckpt/copied"),
            "the file renamed in is listed"
        );
        assert_eq!(
            authority,
            Authority {
                modes: true,
                owners: false
            },
            "and not under the earlier authority"
        );
    }

    /// The file entries of a manifest.
    fn files_of(bytes: &[u8]) -> Result<Vec<Entry>, String> {
        manifest::decode(bytes).map(|m| m.files().cloned().collect())
    }

    fn tree() -> (tempfile::TempDir, Arc<DirExport>) {
        // Under the crate, not /tmp: tmpfs refuses O_DIRECT, and both
        // paths should be covered.
        let dir = tempfile::tempdir_in(crate::test_tmp()).expect("tempdir");
        let r = dir.path();
        std::fs::create_dir_all(r.join("ckpt/pages")).expect("mkdir");
        std::fs::write(r.join("ckpt/core.img"), b"core").expect("write");
        let big: Vec<u8> = (0..3 * 4096 + 100).map(|i| (i % 251) as u8).collect();
        std::fs::write(r.join("ckpt/pages/pages-1.img"), &big).expect("write");
        std::fs::write(r.join("top.bin"), b"").expect("write");
        std::os::unix::fs::symlink("/etc/passwd", r.join("ckpt/escape")).expect("symlink");
        std::os::unix::fs::symlink("../top.bin", r.join("ckpt/inside")).expect("symlink");
        let export = DirExport::open(r).expect("open");
        (dir, export)
    }

    fn fetch_all(e: &DirExport, key: &str) -> Vec<u8> {
        let st = e.read(key, 0, &mut []).expect("stat");
        let mut out = Vec::new();
        let mut buf = Aligned::new(4096);
        while (out.len() as u64) < st.size {
            let r = e.read(key, out.len() as u64, buf.get(4096)).expect("read");
            assert_eq!((r.size, r.generation), (st.size, st.generation));
            out.extend_from_slice(&buf.get(4096)[..r.len]);
        }
        out
    }

    #[test]
    fn manifest_lists_regular_files_only() {
        let (_d, e) = tree();
        let all = files_of(&fetch_all(&e, &manifest::key_v3(""))).expect("decode");
        let paths: Vec<&str> = all.iter().map(|x| x.path.as_str()).collect();
        assert_eq!(
            paths,
            ["ckpt/core.img", "ckpt/pages/pages-1.img", "top.bin"]
        );
        assert_eq!(all[1].size, 3 * 4096 + 100);
        let sub = files_of(&fetch_all(&e, &manifest::key_v3("ckpt/pages"))).expect("decode");
        assert_eq!(sub.len(), 1);
        assert_eq!(
            sub[0], all[1],
            "same generation from listing and from fstat"
        );
        let st = e.read("ckpt/pages/pages-1.img", 0, &mut []).expect("stat");
        assert_eq!(st.generation, all[1].generation);
        for bad in ["../", "nope", "ckpt/core.img"] {
            assert!(e.read(&manifest::key_v3(bad), 0, &mut []).is_err(), "{bad}");
        }
    }

    fn set_attrs(p: &Path, mode: u32, sec: i64, nsec: i64) {
        use std::os::unix::fs::PermissionsExt;
        let c = CString::new(p.as_os_str().as_bytes()).expect("path");
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
        std::fs::set_permissions(p, std::fs::Permissions::from_mode(mode)).expect("chmod");
    }

    #[test]
    fn manifest_lists_directories_and_every_entrys_attributes() {
        let (d, e) = tree();
        let r = d.path();
        std::fs::create_dir_all(r.join("ckpt/empty/deeper")).expect("mkdir");
        set_attrs(&r.join("ckpt/core.img"), 0o640, 1_600_000_000, 123_456_789);
        set_attrs(&r.join("ckpt/pages"), 0o750, 1_500_000_000, 1);
        set_attrs(
            &r.join("ckpt/empty/deeper"),
            0o700,
            1_400_000_000,
            999_999_999,
        );
        // SAFETY: plain syscalls without arguments.
        let me = (unsafe { libc::geteuid() }, unsafe { libc::getegid() });
        let m = manifest::decode(&fetch_all(&e, &manifest::key_v3(""))).expect("decode");
        assert_eq!(m.version, manifest::VERSION);
        let kinds: Vec<(&str, Kind)> = m
            .entries
            .iter()
            .map(|x| (x.path.as_str(), x.kind))
            .collect();
        assert_eq!(
            kinds,
            [
                ("ckpt", Kind::Dir),
                ("ckpt/core.img", Kind::File),
                ("ckpt/empty", Kind::Dir),
                ("ckpt/empty/deeper", Kind::Dir),
                ("ckpt/pages", Kind::Dir),
                ("ckpt/pages/pages-1.img", Kind::File),
                ("top.bin", Kind::File)
            ]
        );
        let get = |p: &str| m.entries.iter().find(|x| x.path == p).expect(p).clone();
        assert_eq!(
            get("ckpt/core.img").attrs,
            Attrs {
                mode: 0o640,
                mtime_sec: 1_600_000_000,
                mtime_nsec: 123_456_789,
                uid: me.0,
                gid: me.1
            }
        );
        assert_eq!(
            get("ckpt/pages").attrs,
            Attrs {
                mode: 0o750,
                mtime_sec: 1_500_000_000,
                mtime_nsec: 1,
                uid: me.0,
                gid: me.1
            }
        );
        let deeper = get("ckpt/empty/deeper");
        assert_eq!((deeper.size, deeper.generation, deeper.content), (0, 0, 0));
        assert_eq!(
            (
                deeper.attrs.mode,
                deeper.attrs.mtime_sec,
                deeper.attrs.mtime_nsec
            ),
            (0o700, 1_400_000_000, 999_999_999)
        );
        // The listed directory itself is not an entry; those below it are.
        let sub = manifest::decode(&fetch_all(&e, &manifest::key_v3("ckpt/empty"))).expect("sub");
        let paths: Vec<&str> = sub.entries.iter().map(|x| x.path.as_str()).collect();
        assert_eq!(paths, ["ckpt/empty/deeper"]);
        // Under a mount, directory keys carry the mount like file keys.
        let mounted = DirExport::open_as(r, "m").expect("open_as");
        let sub = manifest::decode(&fetch_all(&mounted, &manifest::key_v3("m/ckpt/empty")))
            .expect("mounted");
        assert_eq!(
            sub.entries
                .iter()
                .map(|x| x.path.as_str())
                .collect::<Vec<_>>(),
            ["m/ckpt/empty/deeper"]
        );
        // A mode change alone changes the listing (and its generation).
        let before = e
            .read(&manifest::key_v3(""), 0, &mut [])
            .expect("stat")
            .generation;
        set_attrs(&r.join("ckpt/core.img"), 0o600, 1_600_000_000, 123_456_789);
        assert_ne!(
            e.read(&manifest::key_v3(""), 0, &mut [])
                .expect("stat")
                .generation,
            before
        );
    }

    #[test]
    fn serves_version_2_under_the_old_key_and_the_recorded_authority_in_version_3() {
        let (d, e) = tree();
        std::fs::create_dir_all(d.path().join("ckpt/empty")).expect("mkdir");
        // An older fetcher asks /manifest:<prefix> and gets what it reads:
        // version 2, the same files with the same identities, no directories.
        let v2 = manifest::decode(&fetch_all(&e, &manifest::key(""))).expect("v2");
        let v3 = manifest::decode(&fetch_all(&e, &manifest::key_v3(""))).expect("v3");
        assert_eq!((v2.version, v3.version), (2, 3));
        assert!(v2.entries.iter().all(Entry::is_file));
        let strip = |m: &manifest::Manifest| -> Vec<(String, u64, u128, u128)> {
            m.files()
                .map(|x| (x.path.clone(), x.size, x.generation, x.content))
                .collect()
        };
        assert_eq!(strip(&v2), strip(&v3));
        assert_eq!(v3.authority, manifest::Authority::FULL, "an original tree");
        // A tree a fetch copied without owners says so.
        let root = dirfd::open_dir(d.path()).expect("root");
        let partial = manifest::Authority {
            modes: true,
            owners: false,
        };
        content::record_authority(&root, partial).expect("record");
        let v3 = manifest::decode(&fetch_all(&e, &manifest::key_v3(""))).expect("v3");
        assert_eq!(v3.authority, partial);
        assert!(v3
            .entries
            .iter()
            .all(|x| !x.path.starts_with(content::RESERVED_PREFIX)));
    }

    #[test]
    fn reads_aligned_unaligned_and_tail() {
        let (d, e) = tree();
        let want = std::fs::read(d.path().join("ckpt/pages/pages-1.img")).expect("read");
        assert_eq!(fetch_all(&e, "ckpt/pages/pages-1.img"), want);
        // Unaligned offset and length take the buffered path.
        let mut buf = vec![0u8; 10];
        let r = e
            .read("ckpt/pages/pages-1.img", 4097, &mut buf)
            .expect("read");
        assert_eq!((r.len, &buf[..]), (10, &want[4097..4107]));
        // Past the end: nothing, not an error.
        let r = e
            .read("ckpt/pages/pages-1.img", 1 << 20, &mut buf)
            .expect("read");
        assert_eq!(r.len, 0);
        assert_eq!(fetch_all(&e, "top.bin"), b"");
        // Symlinks resolve only beneath the root.
        assert_eq!(fetch_all(&e, "ckpt/inside"), b"");
    }

    #[test]
    fn refuses_anything_outside_the_root() {
        let (_d, e) = tree();
        for key in [
            "ckpt/escape",
            "../etc/passwd",
            "/etc/passwd",
            "ckpt/../../x",
            "ckpt",
            "missing",
        ] {
            let err = e.read(key, 0, &mut [0u8; 8]).expect_err(key);
            assert_eq!(err.kind(), io::ErrorKind::NotFound, "{key}: {err}");
        }
    }

    #[test]
    fn manifest_never_walks_through_a_symlinked_directory() {
        // A prefix through a symlinked directory resolved outside
        // the root and listed the host tree there.
        let outside = tempfile::tempdir_in(crate::test_tmp()).expect("outside");
        std::fs::create_dir_all(outside.path().join("sub")).expect("mkdir");
        std::fs::write(outside.path().join("sub/secret"), b"s").expect("write");
        let (d, e) = tree();
        std::os::unix::fs::symlink(outside.path(), d.path().join("link")).expect("symlink");
        for prefix in ["link", "link/sub", "ckpt/../link/sub"] {
            let err = e
                .read(&manifest::key_v3(prefix), 0, &mut [])
                .expect_err(prefix);
            assert_eq!(err.kind(), io::ErrorKind::NotFound, "{prefix}: {err}");
        }
        let all = files_of(&fetch_all(&e, &manifest::key_v3(""))).expect("decode");
        assert!(all.iter().all(|x| !x.path.starts_with("link")), "{all:?}");
    }

    #[test]
    fn manifest_skips_entries_that_vanish_during_the_walk() {
        // A file removed between readdir and stat failed the whole
        // listing as NOT_FOUND.
        let (d, e) = tree();
        let churn = d.path().join("ckpt/churn");
        std::fs::create_dir_all(&churn).expect("mkdir");
        let stop = Arc::new(std::sync::atomic::AtomicBool::new(false));
        let writer = {
            let (churn, stop) = (churn.clone(), Arc::clone(&stop));
            std::thread::spawn(move || {
                let mut i = 0u64;
                while !stop.load(std::sync::atomic::Ordering::Relaxed) {
                    let p = churn.join(format!("t{}", i % 64));
                    let _ = std::fs::write(&p, b"x");
                    let _ = std::fs::remove_file(&p);
                    let q = churn.join(format!("d{}", i % 16));
                    let _ = std::fs::create_dir(&q);
                    let _ = std::fs::remove_dir(&q);
                    i += 1;
                }
            })
        };
        let mut errors = 0;
        for _ in 0..300 {
            if e.read(&manifest::key_v3("ckpt"), 0, &mut []).is_err() {
                errors += 1;
            }
        }
        stop.store(true, std::sync::atomic::Ordering::Relaxed);
        writer.join().expect("writer");
        assert_eq!(errors, 0, "listings failed while the tree changed");
    }

    #[test]
    fn a_fifo_is_refused_without_blocking() {
        // `open(O_RDONLY)` on a FIFO with no writer blocked forever.
        let (d, e) = tree();
        let fifo = CString::new(d.path().join("ckpt/fifo").as_os_str().as_bytes()).expect("path");
        // SAFETY: valid C string.
        assert_eq!(unsafe { libc::mkfifo(fifo.as_ptr(), 0o644) }, 0, "mkfifo");
        let (tx, rx) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let mut buf = [0u8; 8];
            let _ = tx.send((
                e.read("ckpt/fifo", 0, &mut buf)
                    .map(|_| ())
                    .map_err(|x| x.kind()),
                e.read("ckpt/fifo", 0, &mut [])
                    .map(|_| ())
                    .map_err(|x| x.kind()),
            ));
        });
        let got = rx
            .recv_timeout(std::time::Duration::from_secs(5))
            .expect("read of a FIFO blocked");
        assert_eq!(
            got,
            (Err(io::ErrorKind::NotFound), Err(io::ErrorKind::NotFound))
        );
        let all = files_of(&fetch_all_fresh(d.path())).expect("decode");
        assert!(all.iter().all(|x| x.path != "ckpt/fifo"));
    }

    #[test]
    fn a_read_returns_the_generation_its_bytes_were_read_under() {
        // The generation was sampled before the pread only, so a
        // rewrite during the read was served as the old version.
        let stats = std::cell::RefCell::new(vec![(10, 2), (10, 3), (10, 3)]);
        let stat = || Ok(stats.borrow_mut().remove(0));
        let reads = std::cell::Cell::new(0);
        let read = |_| {
            reads.set(reads.get() + 1);
            Ok(10)
        };
        // Changed during the first read (1 -> 2): read again under 2,
        // which then changes to 3 during the second; the third is stable.
        assert_eq!(
            consistent((10, 1), stat, read).expect("stable"),
            (10, 10, 3)
        );
        assert_eq!(reads.get(), 3);
        // A file that changes during every read is an error, not bytes.
        let n = std::cell::Cell::new(1u128);
        let err = consistent(
            (10, 1),
            || {
                n.set(n.get() + 1);
                Ok((10, n.get()))
            },
            |_| Ok(10),
        )
        .expect_err("never stable");
        assert!(err.to_string().contains("changed"), "{err}");
        // A size change re-reads with the new size.
        let sizes = std::cell::RefCell::new(Vec::new());
        let mut st = vec![(20, 2), (20, 2)];
        consistent(
            (10, 1),
            || Ok(st.remove(0)),
            |size| {
                sizes.borrow_mut().push(size);
                Ok(0)
            },
        )
        .expect("stable");
        assert_eq!(*sizes.borrow(), [10, 20]);
    }

    #[test]
    fn manifest_skips_fetch_staging_directories() {
        let (d, e) = tree();
        let staging = d.path().join(format!("{STAGING_PREFIX}00ff"));
        std::fs::create_dir_all(&staging).expect("mkdir");
        std::fs::write(staging.join("0"), b"half").expect("write");
        let all = files_of(&fetch_all(&e, &manifest::key_v3(""))).expect("decode");
        assert!(
            all.iter().all(|x| !x.path.starts_with(STAGING_PREFIX)),
            "{all:?}"
        );
    }

    #[test]
    fn serves_the_root_under_a_mount_prefix() {
        let (d, _) = tree();
        let e = DirExport::open_as(d.path(), "models/llama/").expect("open_as");
        assert_eq!(e.mount(), "models/llama");
        let want = files_of(&fetch_all(
            &DirExport::open(d.path()).expect("plain"),
            &manifest::key_v3(""),
        ))
        .expect("decode");
        for prefix in ["", "models", "models/llama"] {
            let got = files_of(&fetch_all(&e, &manifest::key_v3(prefix))).expect(prefix);
            let paths: Vec<String> = got.iter().map(|x| x.path.clone()).collect();
            let expect: Vec<String> = want
                .iter()
                .map(|x| format!("models/llama/{}", x.path))
                .collect();
            assert_eq!(paths, expect, "{prefix}");
        }
        let sub =
            files_of(&fetch_all(&e, &manifest::key_v3("models/llama/ckpt/pages"))).expect("sub");
        assert_eq!(sub.len(), 1);
        assert_eq!(sub[0].path, "models/llama/ckpt/pages/pages-1.img");
        assert_eq!(fetch_all(&e, "models/llama/ckpt/core.img"), b"core");
        for bad in [
            "ckpt/core.img",
            "models/ckpt/core.img",
            "models/llamaX/ckpt/core.img",
        ] {
            assert_eq!(
                e.read(bad, 0, &mut [0u8; 4]).expect_err(bad).kind(),
                io::ErrorKind::NotFound
            );
        }
        for bad in ["other", "models/llamaX", "mod"] {
            assert!(e.read(&manifest::key_v3(bad), 0, &mut []).is_err(), "{bad}");
        }
        assert!(DirExport::open_as(d.path(), "../x").is_err());
    }

    #[test]
    fn content_identity_comes_from_the_record_while_the_file_is_unchanged() {
        let (d, e) = tree();
        let all = files_of(&fetch_all(&e, &manifest::key_v3(""))).expect("decode");
        // Originals: content is the generation.
        assert!(all.iter().all(|x| x.content == x.generation));
        let core = all
            .iter()
            .find(|x| x.path == "ckpt/core.img")
            .expect("core")
            .clone();
        let root = dirfd::open_dir(d.path()).expect("root");
        content::record(
            &root,
            &[Entry {
                content: 777,
                ..core.clone()
            }],
        )
        .expect("record");
        let all = files_of(&fetch_all(&e, &manifest::key_v3(""))).expect("decode");
        let got = all
            .iter()
            .find(|x| x.path == "ckpt/core.img")
            .expect("core");
        assert_eq!((got.generation, got.content), (core.generation, 777));
        assert!(
            all.iter()
                .all(|x| !x.path.starts_with(content::RESERVED_PREFIX)),
            "{all:?}"
        );
        assert!(
            e.read(content::CONTENT_FILE, 0, &mut [0u8; 8]).is_err(),
            "the record is not served"
        );
        // Rewritten after the copy: no longer presented as the original.
        std::thread::sleep(std::time::Duration::from_millis(10));
        std::fs::write(d.path().join("ckpt/core.img"), b"CORE").expect("rewrite");
        let all = files_of(&fetch_all(&e, &manifest::key_v3("ckpt"))).expect("decode");
        let got = all
            .iter()
            .find(|x| x.path == "ckpt/core.img")
            .expect("core");
        assert_eq!(got.content, got.generation);
        assert_ne!(got.content, 777);
    }

    fn fetch_all_fresh(root: &Path) -> Vec<u8> {
        let e = DirExport::open(root).expect("open");
        fetch_all(&e, &manifest::key_v3(""))
    }

    #[test]
    fn generation_changes_when_the_file_does() {
        let (d, e) = tree();
        let before = e
            .read("ckpt/core.img", 0, &mut [])
            .expect("stat")
            .generation;
        std::thread::sleep(std::time::Duration::from_millis(10));
        std::fs::write(d.path().join("ckpt/core.img"), b"CORE").expect("rewrite");
        let after = e
            .read("ckpt/core.img", 0, &mut [])
            .expect("stat")
            .generation;
        assert_ne!(before, after);
        assert_ne!(after, 0);
    }
}
