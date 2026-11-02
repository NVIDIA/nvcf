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

//! Filesystem pass-through VFS — serves a real directory tree over NFS.
//!
//! Instead of the synthetic VFS (RocksDB + ChunkManager), this directly
//! maps NFS operations to filesystem syscalls on an existing directory.
//!
//! Use case: serve unpacked container images, model weights, checkpoints, or
//! any existing directory over NFS v4.2 without data import.
//!
//! Usage:
//!   let vfs = PassthroughVfs::with_fh_key(dir, FhKey::load_or_create(key_path)?)?;
//!   let processor = CompoundProcessor::new_passthrough(Arc::new(vfs));
//!
//! # Containment
//!
//! Nothing is ever opened by an absolute path. Every inode is opened either
//! relative to a descriptor of the export root with `openat2(RESOLVE_BENEATH
//! | RESOLVE_NO_SYMLINKS)`, or from its own kernel file handle. Symlinks in
//! the export are objects the client reads with READLINK and resolves in its
//! own namespace; the server never follows one, as a path component or as
//! the object of READ, WRITE, SETATTR, COPY, ALLOCATE or READDIR. FIFOs and
//! device nodes are never opened for I/O.
//!
//! # Filehandles
//!
//! ```text
//! root:   ino=1 (8 bytes)
//! other:  ino (8) | MAC (16) | kernel handle type (4) | kernel handle (n)
//!         ino (8) | MAC (16)                 (filesystem without handles)
//! ```
//!
//! The kernel handle is the inode's own (`name_to_handle_at`), which on
//! ext4, xfs, btrfs and tmpfs carries the inode generation: a handle of a
//! deleted file is NFS4ERR_STALE even after a new file reuses the inode
//! number. The MAC is HMAC-SHA256 under a per-export secret ([`FhKey`]), so
//! clients cannot forge handles to inodes they were never given. With
//! CAP_DAC_READ_SEARCH the server resolves a handle it has no record of
//! (after a restart, or after its inode cache evicted the entry) with
//! `open_by_handle_at`; handles therefore survive a restart as long as the
//! key does.
//!
//! The inode cache maps inode numbers to (parent, name, kernel handle). It
//! is bounded ([`DEFAULT_NODE_CACHE`]) and holds no descriptors, so it pins
//! no inodes: deleted files free their space at once. Without
//! CAP_DAC_READ_SEARCH the cache is the only way back to an inode, so a
//! handle whose entry was evicted is stale.
//!
//! # Access control
//!
//! The server opens inodes with its own privileges (normally root, which
//! `open_by_handle_at` needs anyway), so the kernel's permission checks are
//! the server's, not the client's. Every operation therefore takes the
//! caller's credentials ([`CallerCtx`]: the AUTH_SYS uid, gid and
//! supplementary gids after root squashing) and checks them against the
//! owner, group and mode of the inode it actually opened, with POSIX rules
//! ([`perm`]): search permission on a directory to look a name up in it,
//! write and search permission on a directory to create, remove or rename
//! in it, the sticky bit on removal and rename, read permission to list a
//! directory, read or write permission on a file's data (the owner always
//! passes, as with Linux nfsd), only the owner or root may chmod, only root
//! may give a file away. Files, directories and symlinks a client creates
//! are owned by the caller (the group of a set-group-id parent directory is
//! inherited), set with `fchown` on the new inode itself.
//!
//! Checks are explicit rather than per-thread `setfsuid`/`setfsgid`: a
//! COMPOUND runs on a shared blocking-pool thread, glibc's `setgroups`
//! changes every thread of the process, a non-zero fsuid drops the
//! CAP_DAC_READ_SEARCH that `open_by_handle_at` needs, and decisions made in
//! one place are testable without root.

use std::collections::HashMap;
use std::ffi::{CStr, CString, OsStr, OsString};
use std::fs;
use std::io;
use std::os::fd::{AsRawFd, OwnedFd};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{FileExt, OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;

use parking_lot::Mutex;
use sha2::{Digest, Sha256};
use tracing::{debug, warn};

use crate::kernel_handle::{self, KernelHandle};
use crate::nfs4::CallerCtx;
use crate::vfs::{DirEntry, FileType, InodeAttrs};
use nvstor_core::error::{NvstorError, Result};

/// Root inode number.
const ROOT_INO: u64 = 1;

/// NFS4_FHSIZE.
const NFS4_FHSIZE: usize = 128;
/// Filehandle layout (see the module docs).
const FH_INO: usize = 8;
const FH_MAC: usize = 16;
const FH_SEALED: usize = FH_INO + FH_MAC;
const FH_HANDLE: usize = FH_SEALED + 4;

/// Entries per READDIR reply.
const READDIR_MAX_ENTRIES: usize = 128;

/// Default inode cache capacity, in entries (roughly 100-200 bytes each).
pub const DEFAULT_NODE_CACHE: usize = 1 << 19;

/// Deepest directory chain followed when rebuilding a path or checking that
/// a directory lies inside the export.
const MAX_DEPTH: usize = 4096;

/// Map a `std::io::Error` to a `NvstorError` with the right NFS4 status.
///
/// Critically, `ENOENT` must round-trip to `NFS4ERR_NOENT`, not
/// `NFS4ERR_IO`. The Linux kernel client treats EIO as a hard fault and
/// aborts the entire COMPOUND; ENOENT is recoverable. This mismatch
/// surfaced as cascading IO errors after rmdir/rm in the comprehensive
/// smoketest — the kernel issued GETATTR on a freshly-deleted file and
/// our `fs::symlink_metadata` ENOENT mapped to EIO.
fn map_io_err(e: io::Error) -> NvstorError {
    use std::io::ErrorKind;
    // Translate by errno first — the older `ErrorKind` variants don't
    // exhaustively cover EISDIR / ENOTDIR / ENOTEMPTY across MSRV.
    if let Some(errno) = e.raw_os_error() {
        match errno {
            libc::EISDIR => return NvstorError::IsDir,
            libc::ENOTDIR => return NvstorError::NotDir,
            libc::ENOTEMPTY => return NvstorError::NotEmpty,
            libc::ENOSPC => return NvstorError::NoSpace,
            libc::ENAMETOOLONG => return NvstorError::NameTooLong,
            libc::ESTALE => return NvstorError::StaleHandle,
            libc::ELOOP => return symlink_err(),
            libc::EACCES => return NvstorError::PermissionDenied,
            libc::EPERM => return NvstorError::NotPermitted,
            _ => {}
        }
    }
    match e.kind() {
        ErrorKind::NotFound => NvstorError::NotFound,
        ErrorKind::AlreadyExists => NvstorError::Exists,
        ErrorKind::PermissionDenied => NvstorError::PermissionDenied,
        _ => NvstorError::Io(e),
    }
}

/// The operation's object is a symlink, which the server never follows.
fn symlink_err() -> NvstorError {
    NvstorError::Symlink
}

fn errno(e: &io::Error) -> i32 {
    e.raw_os_error().unwrap_or(0)
}

fn cstring(bytes: &[u8]) -> Result<CString> {
    CString::new(bytes)
        .map_err(|_| NvstorError::InvalidArgument("name contains a null byte".into()))
}

fn kind_of(st: &libc::stat) -> libc::mode_t {
    st.st_mode & libc::S_IFMT
}

/// (st_dev, st_ino): an inode's identity within this host.
#[allow(clippy::unnecessary_cast)] // the field types differ between architectures
fn dev_ino(st: &libc::stat) -> (u64, u64) {
    (st.st_dev as u64, st.st_ino as u64)
}

/// Thin syscall wrappers. Every descriptor is O_CLOEXEC.
mod sys {
    use std::ffi::CStr;
    use std::io;
    use std::mem::MaybeUninit;
    use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};

    /// RESOLVE_BENEATH: the path cannot leave the directory it starts from
    /// (no "..", absolute path or link that escapes). RESOLVE_NO_SYMLINKS:
    /// no component, the last included, is followed if it is a symlink.
    const RESOLVE: u64 =
        libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS | libc::RESOLVE_NO_MAGICLINKS;

    /// `struct open_how` (libc's is non-exhaustive).
    #[repr(C)]
    struct OpenHow {
        flags: u64,
        mode: u64,
        resolve: u64,
    }

    fn cvt(rc: libc::c_int) -> io::Result<()> {
        if rc < 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(())
        }
    }

    fn owned(fd: libc::c_int) -> io::Result<OwnedFd> {
        cvt(fd)?;
        // SAFETY: the syscall returned a new descriptor that nothing else owns.
        Ok(unsafe { OwnedFd::from_raw_fd(fd) })
    }

    /// `path` (relative) beneath `dir`, with no symlink followed. A final
    /// symlink opened with O_PATH | O_NOFOLLOW yields the link itself.
    pub fn openat2(dir: &OwnedFd, path: &CStr, flags: libc::c_int) -> io::Result<OwnedFd> {
        let how = OpenHow {
            flags: (flags | libc::O_CLOEXEC) as u64,
            mode: 0,
            resolve: RESOLVE,
        };
        for _ in 0..16 {
            // SAFETY: `path` is NUL-terminated and `how` is a valid open_how
            // of the size passed.
            let rc = unsafe {
                libc::syscall(
                    libc::SYS_openat2,
                    dir.as_raw_fd(),
                    path.as_ptr(),
                    &how as *const OpenHow,
                    std::mem::size_of::<OpenHow>(),
                )
            };
            if rc >= 0 {
                // SAFETY: a new descriptor that nothing else owns.
                return Ok(unsafe { OwnedFd::from_raw_fd(rc as libc::c_int) });
            }
            let e = io::Error::last_os_error();
            // EAGAIN: a concurrent rename or mount raced RESOLVE_BENEATH.
            if !matches!(e.raw_os_error(), Some(libc::EAGAIN) | Some(libc::EINTR)) {
                return Err(e);
            }
        }
        Err(io::Error::from_raw_os_error(libc::EAGAIN))
    }

    /// A single name in `dir`. Callers pass validated single components.
    pub fn openat(
        dir: &OwnedFd,
        name: &CStr,
        flags: libc::c_int,
        mode: libc::mode_t,
    ) -> io::Result<OwnedFd> {
        // SAFETY: `name` is NUL-terminated.
        owned(unsafe {
            libc::openat(
                dir.as_raw_fd(),
                name.as_ptr(),
                flags | libc::O_CLOEXEC,
                mode as libc::c_uint,
            )
        })
    }

    pub fn fstat(fd: &OwnedFd) -> io::Result<libc::stat> {
        let mut st = MaybeUninit::<libc::stat>::uninit();
        // SAFETY: `st` is valid storage for one stat.
        cvt(unsafe { libc::fstat(fd.as_raw_fd(), st.as_mut_ptr()) })?;
        // SAFETY: fstat succeeded and initialized it.
        Ok(unsafe { st.assume_init() })
    }

    /// stat of `name` in `dir`, not following a symlink.
    pub fn fstatat(dir: &OwnedFd, name: &CStr) -> io::Result<libc::stat> {
        let mut st = MaybeUninit::<libc::stat>::uninit();
        // SAFETY: `name` is NUL-terminated; `st` is valid storage.
        cvt(unsafe {
            libc::fstatat(
                dir.as_raw_fd(),
                name.as_ptr(),
                st.as_mut_ptr(),
                libc::AT_SYMLINK_NOFOLLOW,
            )
        })?;
        // SAFETY: fstatat succeeded and initialized it.
        Ok(unsafe { st.assume_init() })
    }

    pub fn mkdirat(dir: &OwnedFd, name: &CStr, mode: libc::mode_t) -> io::Result<()> {
        // SAFETY: `name` is NUL-terminated.
        cvt(unsafe { libc::mkdirat(dir.as_raw_fd(), name.as_ptr(), mode) })
    }

    pub fn unlinkat(dir: &OwnedFd, name: &CStr, flags: libc::c_int) -> io::Result<()> {
        // SAFETY: `name` is NUL-terminated.
        cvt(unsafe { libc::unlinkat(dir.as_raw_fd(), name.as_ptr(), flags) })
    }

    pub fn symlinkat(target: &CStr, dir: &OwnedFd, name: &CStr) -> io::Result<()> {
        // SAFETY: both strings are NUL-terminated.
        cvt(unsafe { libc::symlinkat(target.as_ptr(), dir.as_raw_fd(), name.as_ptr()) })
    }

    pub fn renameat(
        from: &OwnedFd,
        from_name: &CStr,
        to: &OwnedFd,
        to_name: &CStr,
    ) -> io::Result<()> {
        // SAFETY: both names are NUL-terminated.
        cvt(unsafe {
            libc::renameat(
                from.as_raw_fd(),
                from_name.as_ptr(),
                to.as_raw_fd(),
                to_name.as_ptr(),
            )
        })
    }

    /// Hard link to the inode open as `src`. AT_EMPTY_PATH needs
    /// CAP_DAC_READ_SEARCH; without it a regular file is linked through its
    /// /proc/self/fd entry, which names the open inode.
    pub fn link_fd(
        src: &OwnedFd,
        src_is_symlink: bool,
        dir: &OwnedFd,
        name: &CStr,
    ) -> io::Result<()> {
        // SAFETY: names are NUL-terminated.
        let rc = unsafe {
            libc::linkat(
                src.as_raw_fd(),
                c"".as_ptr(),
                dir.as_raw_fd(),
                name.as_ptr(),
                libc::AT_EMPTY_PATH,
            )
        };
        if rc == 0 {
            return Ok(());
        }
        let e = io::Error::last_os_error();
        // Following /proc/self/fd/N for a symlink would follow the link.
        if src_is_symlink || !matches!(e.raw_os_error(), Some(libc::ENOENT) | Some(libc::EPERM)) {
            return Err(e);
        }
        let proc = std::ffi::CString::new(format!("/proc/self/fd/{}", src.as_raw_fd()))
            .map_err(|_| io::Error::from_raw_os_error(libc::EINVAL))?;
        // SAFETY: names are NUL-terminated.
        cvt(unsafe {
            libc::linkat(
                libc::AT_FDCWD,
                proc.as_ptr(),
                dir.as_raw_fd(),
                name.as_ptr(),
                libc::AT_SYMLINK_FOLLOW,
            )
        })
    }

    /// Target of the symlink open (O_PATH) as `fd`.
    pub fn readlink(fd: &OwnedFd) -> io::Result<Vec<u8>> {
        let mut buf = vec![0u8; 4096];
        loop {
            // SAFETY: `buf` has `buf.len()` writable bytes.
            let n = unsafe {
                libc::readlinkat(
                    fd.as_raw_fd(),
                    c"".as_ptr(),
                    buf.as_mut_ptr() as *mut libc::c_char,
                    buf.len(),
                )
            };
            if n < 0 {
                return Err(io::Error::last_os_error());
            }
            let n = n as usize;
            if n < buf.len() {
                buf.truncate(n);
                return Ok(buf);
            }
            buf.resize(buf.len() * 2, 0);
        }
    }

    pub fn fchmod(fd: &OwnedFd, mode: libc::mode_t) -> io::Result<()> {
        // SAFETY: plain syscall on an owned descriptor.
        cvt(unsafe { libc::fchmod(fd.as_raw_fd(), mode) })
    }

    /// chown of the inode open as `fd` (O_PATH included; a symlink is
    /// changed itself). `u32::MAX` leaves an id unchanged.
    pub fn fchown(fd: &OwnedFd, uid: u32, gid: u32) -> io::Result<()> {
        // SAFETY: the empty name is NUL-terminated.
        cvt(unsafe {
            libc::fchownat(
                fd.as_raw_fd(),
                c"".as_ptr(),
                uid,
                gid,
                libc::AT_EMPTY_PATH | libc::AT_SYMLINK_NOFOLLOW,
            )
        })
    }

    /// utimensat of the inode open as `fd` (O_PATH included).
    pub fn futimens(fd: &OwnedFd, times: &[libc::timespec; 2]) -> io::Result<()> {
        // SAFETY: the empty name is NUL-terminated; `times` holds two entries.
        cvt(unsafe {
            libc::utimensat(
                fd.as_raw_fd(),
                c"".as_ptr(),
                times.as_ptr(),
                libc::AT_EMPTY_PATH | libc::AT_SYMLINK_NOFOLLOW,
            )
        })
    }

    /// F_SETFL: replaces O_NONBLOCK / O_DIRECT / O_APPEND / O_NOATIME.
    pub fn set_status_flags(fd: &OwnedFd, flags: libc::c_int) -> io::Result<()> {
        // SAFETY: plain fcntl on an owned descriptor.
        cvt(unsafe { libc::fcntl(fd.as_raw_fd(), libc::F_SETFL, flags) })
    }

    pub fn seek(fd: &OwnedFd, offset: i64) -> io::Result<()> {
        // SAFETY: plain lseek on an owned descriptor.
        let rc = unsafe { libc::lseek(fd.as_raw_fd(), offset as libc::off_t, libc::SEEK_SET) };
        if rc < 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(())
        }
    }

    /// getdents64 into `buf`; 0 at end of directory.
    pub fn getdents(fd: &OwnedFd, buf: &mut [u8]) -> io::Result<usize> {
        // SAFETY: `buf` has `buf.len()` writable bytes.
        let n = unsafe {
            libc::syscall(
                libc::SYS_getdents64,
                fd.as_raw_fd(),
                buf.as_mut_ptr(),
                buf.len(),
            )
        };
        if n < 0 {
            Err(io::Error::last_os_error())
        } else {
            Ok(n as usize)
        }
    }

    pub fn fsync(fd: &OwnedFd) -> io::Result<()> {
        // SAFETY: plain fsync on an owned descriptor.
        cvt(unsafe { libc::fsync(fd.as_raw_fd()) })
    }

    pub fn getrandom(buf: &mut [u8]) -> io::Result<()> {
        let mut done = 0;
        while done < buf.len() {
            // SAFETY: the range is writable.
            let n = unsafe {
                libc::getrandom(
                    buf[done..].as_mut_ptr() as *mut libc::c_void,
                    buf.len() - done,
                    0,
                )
            };
            if n < 0 {
                let e = io::Error::last_os_error();
                if e.raw_os_error() == Some(libc::EINTR) {
                    continue;
                }
                return Err(e);
            }
            done += n as usize;
        }
        Ok(())
    }
}

/// Secret that seals filehandles (HMAC-SHA256, see the module docs).
///
/// A handle sealed under one key is stale under any other, so a server
/// that must honour handles across restarts loads the same key each time
/// ([`FhKey::load_or_create`]).
#[derive(Clone)]
pub struct FhKey {
    /// SHA-256 states after absorbing key ^ ipad and key ^ opad.
    inner: Sha256,
    outer: Sha256,
}

impl std::fmt::Debug for FhKey {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("FhKey(..)")
    }
}

impl FhKey {
    /// Key length in bytes.
    pub const LEN: usize = 32;

    /// Key from raw bytes.
    pub fn from_bytes(key: &[u8; Self::LEN]) -> Self {
        Self::hmac(key)
    }

    /// HMAC-SHA256 key schedule for a key of at most one block (64 bytes).
    fn hmac(key: &[u8]) -> Self {
        let mut ipad = [0x36u8; 64];
        let mut opad = [0x5cu8; 64];
        for (i, b) in key.iter().take(64).enumerate() {
            ipad[i] ^= b;
            opad[i] ^= b;
        }
        let mut inner = Sha256::new();
        inner.update(ipad);
        let mut outer = Sha256::new();
        outer.update(opad);
        Self { inner, outer }
    }

    /// A fresh random key, valid for this process only.
    pub fn random() -> io::Result<Self> {
        let mut key = [0u8; Self::LEN];
        sys::getrandom(&mut key)?;
        Ok(Self::from_bytes(&key))
    }

    /// The key stored at `path`, creating it (mode 0600, random) if absent.
    /// An existing file must be a regular file of exactly [`Self::LEN`]
    /// bytes that neither group nor others can access.
    pub fn load_or_create(path: &Path) -> io::Result<Self> {
        let invalid = |what: String| {
            io::Error::new(
                io::ErrorKind::InvalidData,
                format!("{}: {what}", path.display()),
            )
        };
        match fs::OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW)
            .open(path)
        {
            Ok(file) => {
                let meta = file.metadata()?;
                if !meta.is_file() {
                    return Err(invalid("not a regular file".into()));
                }
                if meta.permissions().mode() & 0o077 != 0 {
                    return Err(invalid(
                        "group or others can access the filehandle key (chmod 600 it)".into(),
                    ));
                }
                let mut key = [0u8; Self::LEN];
                let n = file.read_at(&mut key, 0)?;
                if n != Self::LEN || meta.len() != Self::LEN as u64 {
                    return Err(invalid(format!(
                        "expected {} bytes, found {}",
                        Self::LEN,
                        meta.len()
                    )));
                }
                Ok(Self::from_bytes(&key))
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                let mut key = [0u8; Self::LEN];
                sys::getrandom(&mut key)?;
                let file = match fs::OpenOptions::new()
                    .write(true)
                    .create_new(true)
                    .mode(0o600)
                    .custom_flags(libc::O_NOFOLLOW)
                    .open(path)
                {
                    Ok(file) => file,
                    // Another process created it first: use theirs.
                    Err(e) if e.kind() == io::ErrorKind::AlreadyExists => {
                        return Self::load_or_create(path)
                    }
                    Err(e) => return Err(e),
                };
                file.write_all_at(&key, 0)?;
                file.sync_all()?;
                let parent = path
                    .parent()
                    .filter(|p| !p.as_os_str().is_empty())
                    .unwrap_or(Path::new("."));
                fs::File::open(parent)?.sync_all()?;
                Ok(Self::from_bytes(&key))
            }
            Err(e) => Err(e),
        }
    }

    /// Truncated HMAC-SHA256 over the concatenation of `parts`.
    fn tag(&self, parts: &[&[u8]]) -> [u8; FH_MAC] {
        let mut inner = self.inner.clone();
        for part in parts {
            inner.update(part);
        }
        let mut outer = self.outer.clone();
        outer.update(inner.finalize());
        let full = outer.finalize();
        let mut tag = [0u8; FH_MAC];
        tag.copy_from_slice(&full[..FH_MAC]);
        tag
    }
}

/// Comparison whose time does not depend on where the inputs differ.
fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    a.len() == b.len() && a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

/// What the inode cache knows about one inode.
#[derive(Debug, Clone)]
struct Node {
    /// Parent directory and name it was last seen under. None when it was
    /// resolved from its kernel handle alone.
    link: Option<(u64, OsString)>,
    dev: u64,
    /// File type (S_IFMT bits); never changes for an inode.
    kind: libc::mode_t,
    /// The inode's own kernel handle; None on a filesystem (or a mount
    /// below the export) that has none.
    handle: Option<KernelHandle>,
}

/// Bounded inode cache. Two generations approximate LRU in O(1): hits in
/// the old generation move to the new one, and when the new one is full
/// the old one is dropped. At most `2 * half` entries are held.
struct NodeCache {
    hot: HashMap<u64, Arc<Node>>,
    cold: HashMap<u64, Arc<Node>>,
    half: usize,
}

impl NodeCache {
    fn new(capacity: usize) -> Self {
        Self {
            hot: HashMap::new(),
            cold: HashMap::new(),
            half: (capacity / 2).max(1),
        }
    }

    fn get(&mut self, ino: u64) -> Option<Arc<Node>> {
        if let Some(node) = self.hot.get(&ino) {
            return Some(node.clone());
        }
        let node = self.cold.remove(&ino)?;
        self.put(ino, node.clone());
        Some(node)
    }

    fn put(&mut self, ino: u64, node: Arc<Node>) {
        if self.hot.len() >= self.half && !self.hot.contains_key(&ino) {
            self.cold = std::mem::take(&mut self.hot);
        }
        self.cold.remove(&ino);
        self.hot.insert(ino, node);
    }

    /// Drop `ino` if it still maps to `node`.
    fn remove_if(&mut self, ino: u64, node: &Arc<Node>) {
        for map in [&mut self.hot, &mut self.cold] {
            if map.get(&ino).is_some_and(|n| Arc::ptr_eq(n, node)) {
                map.remove(&ino);
            }
        }
    }

    #[cfg(test)]
    fn len(&self) -> usize {
        self.hot.len() + self.cold.len()
    }
}

/// Type an operation needs its object to be.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Want {
    /// Anything; opened O_PATH.
    Any,
    Dir,
    Reg,
    RegOrDir,
}

fn check_kind(kind: libc::mode_t, want: Want) -> Result<()> {
    match (want, kind) {
        (Want::Any, _) => Ok(()),
        (Want::Dir, libc::S_IFDIR) => Ok(()),
        (Want::Dir, libc::S_IFLNK) => Err(symlink_err()),
        (Want::Dir, _) => Err(NvstorError::NotDir),
        (_, libc::S_IFREG) => Ok(()),
        (Want::RegOrDir, libc::S_IFDIR) => Ok(()),
        (_, libc::S_IFDIR) => Err(NvstorError::IsDir),
        (_, libc::S_IFLNK) => Err(symlink_err()),
        _ => Err(NvstorError::InvalidArgument("not a regular file".into())),
    }
}

/// Permission decisions: pure functions of an inode's owner, group and
/// mode and the caller's credentials, mirroring the Linux kernel and nfsd.
/// uid 0 is privileged here only when root squashing let it through.
pub mod perm {
    use crate::nfs4::CallerCtx;

    /// Permission bits a check asks for, as in `access(2)`.
    pub const MAY_READ: u32 = 4;
    pub const MAY_WRITE: u32 = 2;
    pub const MAY_EXEC: u32 = 1;

    /// NFSv4 ACCESS bits (RFC 8881 §18.1, RFC 8276 §8.4.1).
    pub const ACCESS4_READ: u32 = 0x01;
    pub const ACCESS4_LOOKUP: u32 = 0x02;
    pub const ACCESS4_MODIFY: u32 = 0x04;
    pub const ACCESS4_EXTEND: u32 = 0x08;
    pub const ACCESS4_DELETE: u32 = 0x10;
    pub const ACCESS4_EXECUTE: u32 = 0x20;
    pub const ACCESS4_XAREAD: u32 = 0x40;
    pub const ACCESS4_XAWRITE: u32 = 0x80;
    pub const ACCESS4_XALIST: u32 = 0x100;

    const S_IFMT: u32 = libc::S_IFMT as u32;
    const S_IFDIR: u32 = libc::S_IFDIR as u32;
    const S_IFREG: u32 = libc::S_IFREG as u32;
    const S_ISUID: u32 = libc::S_ISUID as u32;
    const S_ISGID: u32 = libc::S_ISGID as u32;
    const S_ISVTX: u32 = libc::S_ISVTX as u32;
    const S_IXGRP: u32 = libc::S_IXGRP as u32;

    /// Owner, group and mode (file type included) of an inode.
    #[derive(Debug, Clone, Copy, PartialEq, Eq)]
    pub struct Perm {
        pub mode: u32,
        pub uid: u32,
        pub gid: u32,
    }

    impl Perm {
        #[allow(clippy::unnecessary_cast)] // st_mode's type differs between architectures
        pub fn of(st: &libc::stat) -> Self {
            Self {
                mode: st.st_mode as u32,
                uid: st.st_uid,
                gid: st.st_gid,
            }
        }

        pub fn is_dir(&self) -> bool {
            self.mode & S_IFMT == S_IFDIR
        }

        pub fn is_reg(&self) -> bool {
            self.mode & S_IFMT == S_IFREG
        }

        pub fn is_sticky(&self) -> bool {
            self.mode & S_ISVTX != 0
        }

        pub fn is_setgid(&self) -> bool {
            self.mode & S_ISGID != 0
        }
    }

    /// POSIX mode check: does `cred` hold every `MAY_*` bit of `want`?
    /// The owner class applies to the owner, else the group class to a
    /// member of the file's group (primary or supplementary), else the
    /// other class. Root passes everything except execute on a non-directory
    /// that nobody may execute.
    pub fn may(p: Perm, cred: &CallerCtx, want: u32) -> bool {
        let want = want & 0o7;
        if cred.uid == 0 {
            return want & MAY_EXEC == 0 || p.is_dir() || p.mode & 0o111 != 0;
        }
        let class = if cred.uid == p.uid {
            p.mode >> 6
        } else if cred.in_group(p.gid) {
            p.mode >> 3
        } else {
            p.mode
        };
        class & want == want
    }

    /// READ (and COPY source) of file data: the owner always (Linux nfsd's
    /// owner override: the creator of a mode-0444 file keeps the access its
    /// open gave it), read permission, or execute permission on a regular
    /// file (a client must read a program to run it).
    pub fn may_read_data(p: Perm, cred: &CallerCtx) -> bool {
        cred.uid == p.uid || may(p, cred, MAY_READ) || (p.is_reg() && may(p, cred, MAY_EXEC))
    }

    /// WRITE, truncate, ALLOCATE, COPY destination: the owner always, or
    /// write permission.
    pub fn may_write_data(p: Perm, cred: &CallerCtx) -> bool {
        cred.uid == p.uid || may(p, cred, MAY_WRITE)
    }

    /// OPEN of an existing file with `share_access` (NFS4 bits: 1 read,
    /// 2 write) and `share_deny` (1 deny read): read (or, for a regular
    /// file, execute) permission to read, write permission to write or to
    /// deny others reading. No owner override: a client's open(2) fails
    /// here exactly as it would locally.
    pub fn may_open(p: Perm, cred: &CallerCtx, share_access: u32, share_deny: u32) -> bool {
        let read_ok = share_access & 1 == 0
            || may(p, cred, MAY_READ)
            || (p.is_reg() && may(p, cred, MAY_EXEC));
        let write_ok = (share_access & 2 == 0 && share_deny & 1 == 0) || may(p, cred, MAY_WRITE);
        read_ok && write_ok
    }

    /// Removing `victim` from directory `dir`, or renaming it out of `dir`
    /// or replacing it by a rename: write and search permission on `dir`,
    /// and in a sticky directory the caller must own `victim` or `dir`.
    pub fn may_delete(dir: Perm, victim: Perm, cred: &CallerCtx) -> bool {
        may(dir, cred, MAY_WRITE | MAY_EXEC)
            && (!dir.is_sticky() || cred.uid == 0 || cred.uid == victim.uid || cred.uid == dir.uid)
    }

    /// Hard link to `src` (the kernel's fs.protected_hardlinks=1 rule): the
    /// owner, root, or a caller who may read and write a regular file that
    /// is neither set-user-id nor executable set-group-id.
    pub fn may_link_source(src: Perm, cred: &CallerCtx) -> bool {
        cred.uid == 0
            || cred.uid == src.uid
            || (src.is_reg()
                && src.mode & S_ISUID == 0
                && src.mode & (S_ISGID | S_IXGRP) != (S_ISGID | S_IXGRP)
                && may(src, cred, MAY_READ | MAY_WRITE))
    }

    /// chmod and explicit (client-supplied) times: the owner or root.
    pub fn is_owner(p: Perm, cred: &CallerCtx) -> bool {
        cred.uid == 0 || cred.uid == p.uid
    }

    /// The mode a chmod by `cred` actually sets: a caller other than root
    /// who is not in the file's group cannot set the set-group-id bit.
    pub fn chmod_mode(p: Perm, cred: &CallerCtx, mode: u32) -> u32 {
        let mode = mode & 0o7777;
        if cred.uid != 0 && !cred.in_group(p.gid) {
            mode & !S_ISGID
        } else {
            mode
        }
    }

    /// chown: root may set any owner and group. Anyone else must own the
    /// file, may "set" the owner only to itself (a no-op), and may set the
    /// group only to one it belongs to (or leave it).
    pub fn may_chown(p: Perm, cred: &CallerCtx, uid: Option<u32>, gid: Option<u32>) -> bool {
        if cred.uid == 0 {
            return true;
        }
        uid.is_none_or(|u| cred.uid == p.uid && u == p.uid)
            && gid.is_none_or(|g| cred.uid == p.uid && (g == p.gid || cred.in_group(g)))
    }

    /// Mode bits a data change by `cred` must clear, as the kernel does for
    /// a writer without CAP_FSETID: set-user-id, and set-group-id when group
    /// execute is set. Root (unsquashed) keeps them.
    pub fn privs_to_drop(p: Perm, cred: &CallerCtx) -> u32 {
        if cred.uid == 0 || !p.is_reg() {
            return 0;
        }
        let mut kill = p.mode & S_ISUID;
        if p.mode & (S_ISGID | S_IXGRP) == (S_ISGID | S_IXGRP) {
            kill |= S_ISGID;
        }
        kill
    }

    /// NFSv4 ACCESS: `(supported, granted)` for the `requested` bits, as
    /// Linux nfsd evaluates them. On a directory: READ = read, LOOKUP =
    /// search, MODIFY / EXTEND / DELETE = write and search. On anything
    /// else: READ = read, EXECUTE = execute, MODIFY / EXTEND = write. The
    /// xattr bits follow read (XAREAD, XALIST) and write (XAWRITE). Nothing
    /// that changes the object is granted on a read-only export.
    pub fn access(p: Perm, cred: &CallerCtx, read_only: bool, requested: u32) -> (u32, u32) {
        const CHANGES: u32 = ACCESS4_MODIFY | ACCESS4_EXTEND | ACCESS4_DELETE | ACCESS4_XAWRITE;
        let map: &[(u32, u32)] = if p.is_dir() {
            &[
                (ACCESS4_READ, MAY_READ),
                (ACCESS4_LOOKUP, MAY_EXEC),
                (ACCESS4_MODIFY, MAY_WRITE | MAY_EXEC),
                (ACCESS4_EXTEND, MAY_WRITE | MAY_EXEC),
                (ACCESS4_DELETE, MAY_WRITE | MAY_EXEC),
                (ACCESS4_XAREAD, MAY_READ),
                (ACCESS4_XAWRITE, MAY_WRITE),
                (ACCESS4_XALIST, MAY_READ),
            ]
        } else {
            &[
                (ACCESS4_READ, MAY_READ),
                (ACCESS4_EXECUTE, MAY_EXEC),
                (ACCESS4_MODIFY, MAY_WRITE),
                (ACCESS4_EXTEND, MAY_WRITE),
                (ACCESS4_XAREAD, MAY_READ),
                (ACCESS4_XAWRITE, MAY_WRITE),
                (ACCESS4_XALIST, MAY_READ),
            ]
        };
        let (mut supported, mut granted) = (0, 0);
        for &(bit, want) in map {
            if requested & bit == 0 {
                continue;
            }
            supported |= bit;
            if !(read_only && bit & CHANGES != 0) && may(p, cred, want) {
                granted |= bit;
            }
        }
        (supported, granted)
    }
}

use perm::Perm;

/// EACCES: the mode bits do not allow it.
fn denied() -> NvstorError {
    NvstorError::PermissionDenied
}

/// EPERM: only the owner (or root) may do it.
fn not_permitted() -> NvstorError {
    NvstorError::NotPermitted
}

/// `Ok` when `allowed`, else `err()`.
fn require(allowed: bool, err: fn() -> NvstorError) -> Result<()> {
    if allowed {
        Ok(())
    } else {
        Err(err())
    }
}

/// Namespace every NFS xattr lives in (RFC 8276 exposes user xattrs only;
/// the client strips "user." and the server adds it back, as Linux nfsd
/// does), so a client can never reach trusted.*, security.* or system.*
/// xattrs through the server's privileges.
const XATTR_USER_PREFIX: &str = "user.";

fn user_xattr_name(name: &str) -> Result<CString> {
    if name.is_empty() {
        return Err(NvstorError::InvalidArgument("empty xattr name".into()));
    }
    cstring(format!("{XATTR_USER_PREFIX}{name}").as_bytes())
}

/// Filesystem pass-through VFS.
///
/// Maps NFS operations directly to real filesystem calls.
/// Filehandles are based on real inode numbers.
pub struct PassthroughVfs {
    /// Canonical export root (logging only; all access goes through `root_fd`).
    root: PathBuf,
    /// Read-only descriptor of the export root: the base of every path
    /// resolution, and the mount descriptor of `open_by_handle_at` (which
    /// rejects O_PATH descriptors).
    root_fd: OwnedFd,
    /// (st_dev, st_ino) of the export root.
    root_id: (u64, u64),
    /// Mount of the export root. Inodes on other mounts get no kernel
    /// handle; None when the filesystem cannot export handles at all.
    mount_id: Option<i32>,
    /// CAP_DAC_READ_SEARCH: handles not in the cache can be decoded.
    decode: bool,
    key: FhKey,
    nodes: Mutex<NodeCache>,
    /// Read-only mode (default true for container image serving).
    read_only: bool,
    /// Effective uid of this process: the owner of every inode it creates
    /// before handing it to the caller.
    server_uid: u32,
}

impl PassthroughVfs {
    /// Export `root` with a random filehandle key: handles do not survive
    /// a restart. Servers use [`Self::with_fh_key`].
    pub fn new(root: impl AsRef<Path>) -> Result<Self> {
        Self::with_fh_key(root, FhKey::random().map_err(NvstorError::Io)?)
    }

    /// Export `root` (canonicalized), sealing filehandles with `key`.
    pub fn with_fh_key(root: impl AsRef<Path>, key: FhKey) -> Result<Self> {
        let root = fs::canonicalize(root.as_ref()).map_err(map_io_err)?;
        if !root.is_dir() {
            return Err(NvstorError::NotFound);
        }
        let root_fd = kernel_handle::open_dir_path(&root).map_err(map_io_err)?;
        match sys::openat2(&root_fd, c".", libc::O_PATH) {
            Ok(_) => {}
            Err(e) if errno(&e) == libc::ENOSYS => {
                return Err(NvstorError::Internal(
                    "openat2 (Linux 5.6 or later) is required".into(),
                ))
            }
            Err(e) => return Err(map_io_err(e)),
        }
        let root_id = dev_ino(&sys::fstat(&root_fd).map_err(map_io_err)?);
        let (mount_id, decode) = match KernelHandle::of_fd(&root_fd) {
            Ok((handle, mount_id)) => {
                let decode = match handle.open(&root_fd) {
                    Ok(_) => true,
                    Err(e) if e.raw_os_error() == Some(libc::EPERM) => {
                        warn!(
                            root = %root.display(),
                            "NFS filehandles will not survive a server restart or inode cache eviction: decoding kernel handles needs CAP_DAC_READ_SEARCH"
                        );
                        false
                    }
                    Err(e) => {
                        warn!(
                            root = %root.display(), error = %e,
                            "NFS filehandles will not survive a server restart or inode cache eviction: decoding a kernel handle of the export root failed"
                        );
                        false
                    }
                };
                (Some(mount_id), decode)
            }
            Err(e) => {
                warn!(
                    root = %root.display(), error = %e,
                    "NFS filehandles will not survive a server restart, and reused inode numbers cannot be told apart: filesystem has no kernel file handles"
                );
                (None, false)
            }
        };
        Ok(Self {
            root,
            root_fd,
            root_id,
            mount_id,
            decode,
            key,
            nodes: Mutex::new(NodeCache::new(DEFAULT_NODE_CACHE)),
            read_only: true,
            // SAFETY: geteuid has no preconditions and cannot fail.
            server_uid: unsafe { libc::geteuid() },
        })
    }

    /// Set read-write mode (default is read-only).
    pub fn set_read_write(&mut self) {
        self.read_only = false;
    }

    /// Bound the inode cache to about `entries` entries.
    pub fn set_node_cache_capacity(&mut self, entries: usize) {
        self.nodes = Mutex::new(NodeCache::new(entries));
    }

    /// Canonical path of the export root.
    pub fn root(&self) -> &Path {
        &self.root
    }

    /// Whether filehandles survive a restart (given the same [`FhKey`]).
    pub fn restart_safe(&self) -> bool {
        self.decode
    }

    /// Get the root inode number. Always 1 (NFS convention).
    pub fn root_ino(&self) -> u64 {
        ROOT_INO
    }

    fn check_writable(&self) -> Result<()> {
        if self.read_only {
            return Err(NvstorError::InvalidArgument("read-only export".into()));
        }
        Ok(())
    }

    /// Validate a single path component sent over NFS LOOKUP / CREATE.
    ///
    /// Kernel NFS clients already split paths on `/` and send one component
    /// per LOOKUP, but a malicious raw-RPC client can embed separators or
    /// `..` to escape the export. Reject anything that isn't a single
    /// non-empty component with no separators, null bytes, or traversal.
    fn validate_name(name: &str) -> Result<CString> {
        if name.is_empty() {
            return Err(NvstorError::InvalidArgument("empty name".into()));
        }
        if name.len() > 255 {
            return Err(NvstorError::NameTooLong);
        }
        if name == "." || name == ".." {
            return Err(NvstorError::InvalidArgument(
                "dot names not allowed in NFS LOOKUP".into(),
            ));
        }
        if name.as_bytes().contains(&b'/') {
            return Err(NvstorError::InvalidArgument(
                "name contains separator or null".into(),
            ));
        }
        cstring(name.as_bytes())
    }

    // =====================================================================
    // Inode resolution
    // =====================================================================

    fn node(&self, ino: u64) -> Result<Arc<Node>> {
        self.nodes.lock().get(ino).ok_or(NvstorError::StaleHandle)
    }

    fn forget(&self, ino: u64, node: &Arc<Node>) {
        self.nodes.lock().remove_if(ino, node);
    }

    /// Record `name` in directory `parent_ino` (open as `dir`), whose stat
    /// is `st`, and return its inode number.
    fn remember(
        &self,
        parent_ino: u64,
        name: &CStr,
        dir: &OwnedFd,
        st: &libc::stat,
    ) -> Result<u64> {
        let (dev, ino) = dev_ino(st);
        if (dev, ino) == self.root_id {
            return Ok(ROOT_INO);
        }
        if ino == ROOT_INO {
            return Err(NvstorError::InvalidArgument(
                "inode number 1 is reserved for the export root".into(),
            ));
        }
        let handle = self
            .mount_id
            .and_then(|root_mount| match KernelHandle::of_child(dir, name) {
                Ok((handle, mount))
                    if mount == root_mount && FH_HANDLE + handle.bytes.len() <= NFS4_FHSIZE =>
                {
                    Some(handle)
                }
                _ => None,
            });
        let node = Node {
            link: Some((parent_ino, OsStr::from_bytes(name.to_bytes()).to_owned())),
            dev,
            kind: kind_of(st),
            handle,
        };
        self.nodes.lock().put(ino, Arc::new(node));
        Ok(ino)
    }

    /// Path of `node` relative to the root, from the cached parent chain.
    fn rel_path(&self, node: &Node) -> Option<CString> {
        let mut names: Vec<OsString> = Vec::new();
        {
            let mut nodes = self.nodes.lock();
            let (mut parent, name) = node.link.clone()?;
            names.push(name);
            while parent != ROOT_INO {
                if names.len() > MAX_DEPTH {
                    return None;
                }
                let (grand, name) = nodes.get(parent)?.link.clone()?;
                names.push(name);
                parent = grand;
            }
        }
        let mut path = Vec::new();
        for (i, name) in names.iter().rev().enumerate() {
            if i > 0 {
                path.push(b'/');
            }
            path.extend_from_slice(name.as_bytes());
        }
        CString::new(path).ok()
    }

    /// Open inode `ino` with `flags` (O_PATH for metadata only) and verify
    /// it is still the inode the cache (and so the client's filehandle)
    /// names: same device, inode number, type and kernel handle, and not
    /// deleted. A symlink is never followed and, unless `Want::Any`, never
    /// opened; a FIFO or device node is never opened beyond O_PATH.
    fn open_node(&self, ino: u64, flags: libc::c_int, want: Want) -> Result<(OwnedFd, libc::stat)> {
        let node = if ino == ROOT_INO {
            None
        } else {
            Some(self.node(ino)?)
        };
        let kind = node.as_ref().map_or(libc::S_IFDIR, |n| n.kind);
        check_kind(kind, want)?;
        let path_only = flags & libc::O_PATH != 0;
        let mut open_flags = flags | libc::O_NOFOLLOW;
        if !path_only {
            // Guards the open itself against a FIFO or device that took the
            // name after the type check; cleared again below.
            open_flags |= libc::O_NONBLOCK | libc::O_NOCTTY;
        }
        let fd = match &node {
            None => sys::openat2(&self.root_fd, c".", open_flags).map_err(map_io_err)?,
            Some(node) => self.open_known(ino, node, open_flags)?,
        };
        let st = sys::fstat(&fd).map_err(map_io_err)?;
        if let Some(node) = &node {
            if dev_ino(&st) != (node.dev, ino) || kind_of(&st) != node.kind || st.st_nlink == 0 {
                self.forget(ino, node);
                return Err(NvstorError::StaleHandle);
            }
        }
        if !path_only && kind == libc::S_IFREG {
            sys::set_status_flags(&fd, flags & libc::O_DIRECT).map_err(map_io_err)?;
        }
        Ok((fd, st))
    }

    fn open_known(&self, ino: u64, node: &Arc<Node>, flags: libc::c_int) -> Result<OwnedFd> {
        if let (Some(handle), true) = (&node.handle, self.decode) {
            // The handle carries the generation: a reused inode number is
            // ESTALE here.
            return handle
                .open_with(&self.root_fd, flags)
                .map_err(|e| match errno(&e) {
                    libc::ESTALE | libc::ENOENT => {
                        self.forget(ino, node);
                        NvstorError::StaleHandle
                    }
                    _ => map_io_err(e),
                });
        }
        let rel = self.rel_path(node).ok_or(NvstorError::StaleHandle)?;
        let fd = sys::openat2(&self.root_fd, &rel, flags).map_err(|e| match errno(&e) {
            // Deleted or renamed, or a directory on the way replaced by a
            // symlink or a path that leaves the export.
            libc::ENOENT | libc::ENOTDIR | libc::ELOOP | libc::EXDEV => NvstorError::StaleHandle,
            _ => map_io_err(e),
        })?;
        if let Some(handle) = &node.handle {
            match KernelHandle::of_fd(&fd) {
                Ok((current, _)) if current == *handle => {}
                _ => {
                    self.forget(ino, node);
                    return Err(NvstorError::StaleHandle);
                }
            }
        }
        Ok(fd)
    }

    /// Whether directory `dir` lies inside the export: walk ".." up to the
    /// root (no path strings involved).
    fn check_beneath(&self, dir: OwnedFd) -> Result<()> {
        let mut cur = dir;
        let mut id = dev_ino(&sys::fstat(&cur).map_err(map_io_err)?);
        for _ in 0..MAX_DEPTH {
            if id == self.root_id {
                return Ok(());
            }
            let up = sys::openat(
                &cur,
                c"..",
                libc::O_PATH | libc::O_DIRECTORY | libc::O_NOFOLLOW,
                0,
            )
            .map_err(map_io_err)?;
            let up_id = dev_ino(&sys::fstat(&up).map_err(map_io_err)?);
            if up_id == id {
                break; // the filesystem root: ".." is itself
            }
            cur = up;
            id = up_id;
        }
        Err(NvstorError::StaleHandle)
    }

    #[allow(clippy::unnecessary_cast)] // the field types differ between architectures
    fn attrs(st: &libc::stat, ino: u64) -> InodeAttrs {
        let file_type = match kind_of(st) {
            libc::S_IFDIR => FileType::Directory,
            libc::S_IFLNK => FileType::Symlink,
            _ => FileType::Regular,
        };
        InodeAttrs {
            ino,
            size: st.st_size as u64,
            file_type,
            mode: (st.st_mode & 0o7777) as u32,
            uid: st.st_uid,
            gid: st.st_gid,
            nlink: st.st_nlink as u32,
            atime: st.st_atime as i64,
            mtime: st.st_mtime as i64,
            ctime: st.st_ctime as i64,
            generation: 0,
        }
    }

    // =====================================================================
    // Filehandles
    // =====================================================================

    /// Filehandle for an inode (layout in the module docs).
    pub fn ino_to_filehandle(&self, ino: u64) -> Vec<u8> {
        let mut fh = ino.to_le_bytes().to_vec();
        if ino == ROOT_INO {
            return fh;
        }
        // Every inode reaches a client through LOOKUP, READDIR, CREATE or a
        // decoded filehandle, all of which cache it first; an entry evicted
        // in between yields a handle that is stale on use.
        let mut body = Vec::new();
        if let Some(handle) = self.nodes.lock().get(ino).and_then(|n| n.handle.clone()) {
            body.extend_from_slice(&handle.handle_type.to_le_bytes());
            body.extend_from_slice(&handle.bytes);
        }
        let tag = self.key.tag(&[&fh, &body]);
        fh.extend_from_slice(&tag);
        fh.extend_from_slice(&body);
        fh
    }

    /// Resolve a filehandle to an inode number. A handle whose layout this
    /// server never issues is malformed (InvalidArgument, NFS4ERR_BADHANDLE).
    /// A well-formed handle not sealed by this export's key is stale without
    /// touching the filesystem. One this process has no record of (it
    /// restarted, or the cache evicted the inode) is decoded from its kernel
    /// handle.
    pub fn filehandle_to_ino(&self, fh: &[u8]) -> Result<u64> {
        let malformed = || NvstorError::InvalidArgument("malformed filehandle".into());
        if fh.len() < FH_INO {
            return Err(malformed());
        }
        let ino = u64::from_le_bytes(fh[..FH_INO].try_into().expect("invariant: len checked"));
        if fh.len() == FH_INO {
            return if ino == ROOT_INO {
                Ok(ROOT_INO)
            } else {
                Err(malformed())
            };
        }
        if ino == ROOT_INO
            || fh.len() < FH_SEALED
            || (fh.len() > FH_SEALED && fh.len() <= FH_HANDLE)
            || fh.len() > NFS4_FHSIZE
        {
            return Err(malformed());
        }
        let body = &fh[FH_SEALED..];
        if !constant_time_eq(
            &self.key.tag(&[&fh[..FH_INO], body]),
            &fh[FH_INO..FH_SEALED],
        ) {
            return Err(NvstorError::StaleHandle);
        }
        let handle = (!body.is_empty()).then(|| KernelHandle {
            handle_type: i32::from_le_bytes(body[..4].try_into().expect("invariant: len checked")),
            bytes: body[4..].to_vec(),
        });
        if let Some(node) = self.nodes.lock().get(ino) {
            if node.handle == handle {
                return Ok(ino);
            }
        }
        let handle = match handle {
            Some(handle) if self.decode => handle,
            _ => return Err(NvstorError::StaleHandle),
        };
        let fd = handle.open(&self.root_fd).map_err(|e| match errno(&e) {
            // Out of descriptors or memory: the handle may be fine.
            libc::EMFILE | libc::ENFILE | libc::ENOMEM => map_io_err(e),
            _ => {
                debug!(ino, error = %e, "filehandle did not resolve");
                NvstorError::StaleHandle
            }
        })?;
        let st = sys::fstat(&fd).map_err(map_io_err)?;
        if dev_ino(&st).1 != ino || st.st_nlink == 0 {
            return Err(NvstorError::StaleHandle);
        }
        let kind = kind_of(&st);
        if kind == libc::S_IFDIR {
            // Sealed handles are only issued for inodes inside the export,
            // but a directory can be moved out of it since.
            self.check_beneath(fd)?;
        }
        let node = Node {
            link: None,
            dev: dev_ino(&st).0,
            kind,
            handle: Some(handle),
        };
        self.nodes.lock().put(ino, Arc::new(node));
        Ok(ino)
    }

    // =====================================================================
    // VFS operations — map to real filesystem
    // =====================================================================

    /// Look `name` up in directory `parent_ino`; needs search permission
    /// on the directory.
    pub fn lookup(&self, parent_ino: u64, name: &str, cred: &CallerCtx) -> Result<InodeAttrs> {
        let cname = Self::validate_name(name)?;
        let (dir, dir_st) =
            self.open_node(parent_ino, libc::O_PATH | libc::O_DIRECTORY, Want::Dir)?;
        require(perm::may(Perm::of(&dir_st), cred, perm::MAY_EXEC), denied)?;
        let st = sys::fstatat(&dir, &cname).map_err(map_io_err)?;
        let ino = self.remember(parent_ino, &cname, &dir, &st)?;
        Ok(Self::attrs(&st, ino))
    }

    pub fn getattr(&self, ino: u64) -> Result<InodeAttrs> {
        let (_, st) = self.open_node(ino, libc::O_PATH, Want::Any)?;
        Ok(Self::attrs(&st, ino))
    }

    /// One page of directory entries after `cookie` (0 = start). Cookies
    /// are the filesystem's own directory offsets (getdents64 `d_off`, the
    /// value `telldir` returns), so a cookie keeps naming the same position
    /// while entries are created and removed between pages, as with Linux
    /// nfsd. The cookie verifier names the directory (see
    /// `VfsBackend::readdir_verifier`).
    ///
    /// Needs read permission on the directory (as Linux nfsd's READDIR).
    pub fn readdir(
        &self,
        dir_ino: u64,
        cookie: u64,
        cred: &CallerCtx,
    ) -> Result<(Vec<DirEntry>, bool)> {
        let (dir, dir_st) =
            self.open_node(dir_ino, libc::O_RDONLY | libc::O_DIRECTORY, Want::Dir)?;
        require(perm::may(Perm::of(&dir_st), cred, perm::MAY_READ), denied)?;
        if cookie != 0 {
            let offset = i64::try_from(cookie)
                .map_err(|_| NvstorError::InvalidArgument("bad readdir cookie".into()))?;
            sys::seek(&dir, offset).map_err(map_io_err)?;
        }
        let mut result = Vec::new();
        let mut buf = vec![0u8; 32 * 1024];
        loop {
            let n = sys::getdents(&dir, &mut buf).map_err(map_io_err)?;
            if n == 0 {
                return Ok((result, true));
            }
            let mut pos = 0;
            while pos + 19 <= n {
                // struct linux_dirent64: d_ino u64, d_off i64, d_reclen u16,
                // d_type u8, d_name NUL-terminated.
                let rec = &buf[pos..n];
                let off =
                    u64::from_ne_bytes(rec[8..16].try_into().expect("invariant: 19-byte header"));
                let reclen =
                    u16::from_ne_bytes(rec[16..18].try_into().expect("invariant: 19-byte header"))
                        as usize;
                if reclen < 19 || reclen > rec.len() {
                    return Err(NvstorError::Io(io::Error::from_raw_os_error(libc::EIO)));
                }
                pos += reclen;
                let Ok(name) = CStr::from_bytes_until_nul(&rec[19..reclen]) else {
                    continue;
                };
                if name.to_bytes() == b"." || name.to_bytes() == b".." {
                    continue;
                }
                if result.len() >= READDIR_MAX_ENTRIES {
                    return Ok((result, false));
                }
                let st = match sys::fstatat(&dir, name) {
                    Ok(st) => st,
                    Err(e) => {
                        debug!(name = ?name, error = %e, "readdir: entry vanished");
                        continue;
                    }
                };
                let ino = match self.remember(dir_ino, name, &dir, &st) {
                    Ok(ino) => ino,
                    Err(e) => {
                        debug!(name = ?name, error = %e, "readdir: entry skipped");
                        continue;
                    }
                };
                let file_type = Self::attrs(&st, ino).file_type;
                result.push(DirEntry {
                    name: String::from_utf8_lossy(name.to_bytes()).into_owned(),
                    ino,
                    cookie: off,
                    file_type,
                });
            }
        }
    }

    /// Read `len` bytes from `offset` using positional I/O.
    ///
    /// The previous implementation loaded the entire file into RAM
    /// (`fs::read`), then sliced. That made a 10 GB READ of a 10 GB file
    /// allocate 10 GB. `pread` reads exactly the requested window — no slurp,
    /// no unbounded allocation, no head-of-line blocking from a single slow
    /// disk stretching across unrelated offsets.
    ///
    /// Access: [`perm::may_read_data`], against the inode opened.
    pub fn read(&self, ino: u64, offset: u64, len: u64, cred: &CallerCtx) -> Result<Vec<u8>> {
        let (fd, st) = self.open_node(ino, libc::O_RDONLY, Want::Reg)?;
        require(perm::may_read_data(Perm::of(&st), cred), denied)?;
        if len == 0 {
            return Ok(Vec::new());
        }
        let file = fs::File::from(fd);
        let mut buf = vec![0u8; len as usize];
        // `read_at` is the positional I/O primitive; it does not move the
        // file's seek cursor, so concurrent reads at different offsets do not
        // interfere with each other.
        let n = file.read_at(&mut buf, offset).map_err(map_io_err)?;
        buf.truncate(n);
        Ok(buf)
    }

    /// Read into a caller buffer. Requests aligned to [`DIRECT_IO_ALIGN`]
    /// (offset, length and buffer address) use O_DIRECT, so the device DMAs
    /// straight into `buf` — for an RDMA transport that is registered memory
    /// the NIC then sends from, with no CPU copy anywhere. Linux keeps direct
    /// reads coherent with buffered writes (dirty pages are written back
    /// first). Unaligned requests, and filesystems that refuse O_DIRECT, use
    /// the page cache. Access as [`Self::read`].
    pub fn read_into(
        &self,
        ino: u64,
        offset: u64,
        buf: &mut [u8],
        cred: &CallerCtx,
    ) -> Result<usize> {
        if buf.is_empty() {
            let (_, st) = self.open_node(ino, libc::O_PATH, Want::Reg)?;
            require(perm::may_read_data(Perm::of(&st), cred), denied)?;
            return Ok(0);
        }
        let aligned = offset.is_multiple_of(DIRECT_IO_ALIGN as u64)
            && buf.len().is_multiple_of(DIRECT_IO_ALIGN)
            && (buf.as_ptr() as usize).is_multiple_of(DIRECT_IO_ALIGN);
        if aligned {
            match self.open_node(ino, libc::O_RDONLY | libc::O_DIRECT, Want::Reg) {
                Ok((_, st)) if !perm::may_read_data(Perm::of(&st), cred) => return Err(denied()),
                Ok((fd, _)) => match read_full(&fs::File::from(fd), buf, offset, true) {
                    Ok(n) => return Ok(n),
                    // Device-level alignment stricter than DIRECT_IO_ALIGN.
                    Err(e) if e.raw_os_error() == Some(libc::EINVAL) => {}
                    Err(e) => return Err(map_io_err(e)),
                },
                // Filesystem without O_DIRECT support (e.g. tmpfs).
                Err(NvstorError::Io(e)) if e.raw_os_error() == Some(libc::EINVAL) => {}
                Err(e) => return Err(e),
            }
        }
        let (fd, st) = self.open_node(ino, libc::O_RDONLY, Want::Reg)?;
        require(perm::may_read_data(Perm::of(&st), cred), denied)?;
        read_full(&fs::File::from(fd), buf, offset, false).map_err(map_io_err)
    }

    /// Write `data` at `offset` using positional I/O.
    ///
    /// The previous implementation was read-modify-write of the
    /// entire file per RPC — O(filesize) per WRITE. `pwrite` writes exactly
    /// the requested window; sparse holes are preserved by the underlying
    /// filesystem.
    ///
    /// Access: [`perm::may_write_data`]. A write by anyone but root clears
    /// set-user-id (and executable set-group-id), as the kernel would.
    pub fn write(&self, ino: u64, offset: u64, data: &[u8], cred: &CallerCtx) -> Result<usize> {
        self.check_writable()?;
        let (fd, st) = self.open_node(ino, libc::O_WRONLY, Want::Reg)?;
        require(perm::may_write_data(Perm::of(&st), cred), denied)?;
        if data.is_empty() {
            return Ok(0);
        }
        Self::drop_privs(&fd, &st, cred)?;
        fs::File::from(fd)
            .write_at(data, offset)
            .map_err(map_io_err)
    }

    /// Clear the mode bits a data change by `cred` must clear
    /// ([`perm::privs_to_drop`]) on the inode open as `fd`.
    #[allow(clippy::unnecessary_cast)] // st_mode's type differs between architectures
    fn drop_privs(fd: &OwnedFd, st: &libc::stat, cred: &CallerCtx) -> Result<()> {
        let kill = perm::privs_to_drop(Perm::of(st), cred);
        if kill == 0 {
            return Ok(());
        }
        let mode = (st.st_mode as u32 & 0o7777) & !kill;
        sys::fchmod(fd, mode as libc::mode_t).map_err(map_io_err)
    }

    /// Target of a symlink, never followed.
    pub fn readlink(&self, ino: u64) -> Result<String> {
        let (fd, st) = self.open_node(ino, libc::O_PATH, Want::Any)?;
        if kind_of(&st) != libc::S_IFLNK {
            return Err(NvstorError::InvalidArgument("not a symbolic link".into()));
        }
        let target = sys::readlink(&fd).map_err(map_io_err)?;
        Ok(String::from_utf8_lossy(&target).into_owned())
    }

    /// Parent of directory `ino`; the export root is its own parent.
    /// Needs search permission on `ino` (looking up "..").
    pub fn lookupp(&self, ino: u64, cred: &CallerCtx) -> Result<InodeAttrs> {
        let (dir, dir_st) = self.open_node(ino, libc::O_PATH | libc::O_DIRECTORY, Want::Dir)?;
        require(perm::may(Perm::of(&dir_st), cred, perm::MAY_EXEC), denied)?;
        if ino == ROOT_INO {
            return Ok(Self::attrs(&dir_st, ROOT_INO));
        }
        let parent = sys::openat(
            &dir,
            c"..",
            libc::O_PATH | libc::O_DIRECTORY | libc::O_NOFOLLOW,
            0,
        )
        .map_err(map_io_err)?;
        let st = sys::fstat(&parent).map_err(map_io_err)?;
        let (dev, parent_ino) = dev_ino(&st);
        if (dev, parent_ino) == self.root_id {
            return self.getattr(ROOT_INO);
        }
        let handle = self
            .mount_id
            .and_then(|root_mount| match KernelHandle::of_fd(&parent) {
                Ok((handle, mount))
                    if mount == root_mount && FH_HANDLE + handle.bytes.len() <= NFS4_FHSIZE =>
                {
                    Some(handle)
                }
                _ => None,
            });
        // Never hand out a directory above the export.
        self.check_beneath(parent)?;
        let known = self.nodes.lock().get(parent_ino);
        if !known.is_some_and(|n| n.dev == dev && n.handle == handle) {
            let node = Node {
                link: None,
                dev,
                kind: libc::S_IFDIR,
                handle,
            };
            self.nodes.lock().put(parent_ino, Arc::new(node));
        }
        Ok(Self::attrs(&st, parent_ino))
    }

    /// Check if locks are empty (for grace period).
    pub fn locks_empty(&self) -> bool {
        true // Passthrough doesn't manage locks.
    }

    /// Flush inode `ino` to stable storage (fsync: data and metadata).
    /// Backs COMMIT and WRITE with DATA_SYNC4 / FILE_SYNC4: the reply
    /// claims durability only once this returns. Symlinks and special
    /// files hold no data of their own; their directory entry was synced
    /// when they were created.
    pub fn fsync(&self, ino: u64) -> Result<()> {
        if self.read_only {
            return Ok(());
        }
        let kind = if ino == ROOT_INO {
            libc::S_IFDIR
        } else {
            self.node(ino)?.kind
        };
        if kind != libc::S_IFREG && kind != libc::S_IFDIR {
            // Still report a handle that no longer resolves.
            self.getattr(ino)?;
            return if kind == libc::S_IFLNK {
                Err(symlink_err())
            } else {
                Ok(())
            };
        }
        let (fd, _) = self.open_node(ino, libc::O_RDONLY, Want::RegOrDir)?;
        sys::fsync(&fd).map_err(map_io_err)
    }

    // File creation/deletion for read-write mode. An NFS client treats a
    // namespace change as durable once the server replies (there is no
    // COMMIT for it), so each one syncs the directories it changed, as
    // Linux nfsd's commit_metadata does.

    /// Directory `ino` opened for *at calls (not O_PATH, so it can be
    /// synced), with its stat.
    fn open_dir(&self, ino: u64) -> Result<(OwnedFd, libc::stat)> {
        self.open_node(ino, libc::O_RDONLY | libc::O_DIRECTORY, Want::Dir)
    }

    /// Directory `ino`, in which `cred` may add or remove names (write and
    /// search permission).
    fn open_dir_to_change(&self, ino: u64, cred: &CallerCtx) -> Result<(OwnedFd, libc::stat)> {
        let (dir, st) = self.open_dir(ino)?;
        require(
            perm::may(Perm::of(&st), cred, perm::MAY_WRITE | perm::MAY_EXEC),
            denied,
        )?;
        Ok((dir, st))
    }

    fn sync(fd: &OwnedFd) -> Result<()> {
        sys::fsync(fd).map_err(map_io_err)
    }

    /// Owner and group of a new inode in a directory with stat `dir`: the
    /// caller's uid, and the caller's gid unless the directory is
    /// set-group-id (then its group, which the kernel already assigned).
    fn new_owner(dir: &libc::stat, cred: &CallerCtx) -> (u32, u32) {
        let dir = Perm::of(dir);
        (cred.uid, if dir.is_setgid() { dir.gid } else { cred.gid })
    }

    /// Give the inode this call just created as `name` in `dir` (open as
    /// `fd`, stat `st`) to the caller, with `fchown` on the inode itself.
    /// If that fails (a server without CAP_CHOWN creating for another uid)
    /// the new entry is removed again and the create fails: nothing is left
    /// behind owned by the wrong user. Returns the inode's stat afterwards.
    fn hand_over(
        dir: &OwnedFd,
        dir_st: &libc::stat,
        name: &CStr,
        fd: &OwnedFd,
        st: libc::stat,
        cred: &CallerCtx,
    ) -> Result<libc::stat> {
        let (uid, gid) = Self::new_owner(dir_st, cred);
        if (st.st_uid, st.st_gid) == (uid, gid) {
            return Ok(st);
        }
        if let Err(e) = sys::fchown(fd, uid, gid) {
            let flags = if kind_of(&st) == libc::S_IFDIR {
                libc::AT_REMOVEDIR
            } else {
                0
            };
            if sys::fstatat(dir, name).is_ok_and(|now| dev_ino(&now) == dev_ino(&st)) {
                if let Err(rm) = sys::unlinkat(dir, name, flags) {
                    warn!(name = ?name, error = %rm, "create: removing an entry that could not be given to its creator failed");
                }
            }
            warn!(uid, gid, error = %e, "create: could not give the new inode to the caller (the server needs CAP_CHOWN)");
            return Err(map_io_err(e));
        }
        sys::fstat(fd).map_err(map_io_err)
    }

    /// Open (`flags`, never following a symlink) the entry this call just
    /// created as `name` in `dir` and give it to the caller. An entry that
    /// is neither owned by this process nor already the caller's is not the
    /// one created here: another client replaced the name in between.
    fn claim_new(
        &self,
        dir: &OwnedFd,
        dir_st: &libc::stat,
        name: &CStr,
        flags: libc::c_int,
        cred: &CallerCtx,
    ) -> Result<(OwnedFd, libc::stat)> {
        let fd = sys::openat(dir, name, flags | libc::O_NOFOLLOW, 0).map_err(map_io_err)?;
        let st = sys::fstat(&fd).map_err(map_io_err)?;
        if st.st_uid != self.server_uid && (st.st_uid, st.st_gid) != Self::new_owner(dir_st, cred) {
            return Err(NvstorError::Exists);
        }
        let st = Self::hand_over(dir, dir_st, name, &fd, st, cred)?;
        Ok((fd, st))
    }

    /// Open (creating if absent) regular file `name`. An existing file is
    /// opened as is, never truncated, and a symlink under the name is not
    /// followed. Looking the name up needs search permission on the
    /// directory, creating it write and search permission; a created file
    /// belongs to the caller. Returns its attributes and whether this call
    /// created it (OPEN checks an existing file's own permissions).
    pub fn create_file(
        &self,
        parent_ino: u64,
        name: &str,
        mode: u32,
        cred: &CallerCtx,
    ) -> Result<(InodeAttrs, bool)> {
        self.check_writable()?;
        let cname = Self::validate_name(name)?;
        let (dir, dir_st) = self.open_dir(parent_ino)?;
        let dir_perm = Perm::of(&dir_st);
        require(perm::may(dir_perm, cred, perm::MAY_EXEC), denied)?;
        let existing = |dir: &OwnedFd| -> Result<(InodeAttrs, bool)> {
            let st = sys::fstatat(dir, &cname).map_err(map_io_err)?;
            check_kind(kind_of(&st), Want::Reg)?;
            let ino = self.remember(parent_ino, &cname, dir, &st)?;
            Ok((Self::attrs(&st, ino), false))
        };
        match sys::fstatat(&dir, &cname) {
            Ok(_) => return existing(&dir),
            Err(e) if errno(&e) == libc::ENOENT => {}
            Err(e) => return Err(map_io_err(e)),
        }
        require(
            perm::may(dir_perm, cred, perm::MAY_WRITE | perm::MAY_EXEC),
            denied,
        )?;
        let flags = libc::O_CREAT
            | libc::O_EXCL
            | libc::O_WRONLY
            | libc::O_NOFOLLOW
            | libc::O_NONBLOCK
            | libc::O_NOCTTY;
        let fd = match sys::openat(&dir, &cname, flags, (mode & 0o7777) as libc::mode_t) {
            Ok(fd) => fd,
            // Created by another client since the lookup: open theirs.
            Err(e) if errno(&e) == libc::EEXIST => return existing(&dir),
            Err(e) => return Err(map_io_err(e)),
        };
        let st = sys::fstat(&fd).map_err(map_io_err)?;
        check_kind(kind_of(&st), Want::Reg)?;
        let st = Self::hand_over(&dir, &dir_st, &cname, &fd, st, cred)?;
        Self::sync(&fd)?;
        Self::sync(&dir)?;
        let ino = self.remember(parent_ino, &cname, &dir, &st)?;
        Ok((Self::attrs(&st, ino), true))
    }

    /// Create directory `name`, owned by the caller; needs write and search
    /// permission on the parent.
    pub fn mkdir(
        &self,
        parent_ino: u64,
        name: &str,
        mode: u32,
        cred: &CallerCtx,
    ) -> Result<InodeAttrs> {
        self.check_writable()?;
        let cname = Self::validate_name(name)?;
        let (dir, dir_st) = self.open_dir_to_change(parent_ino, cred)?;
        sys::mkdirat(&dir, &cname, (mode & 0o7777) as libc::mode_t).map_err(map_io_err)?;
        let (new, st) = self.claim_new(
            &dir,
            &dir_st,
            &cname,
            libc::O_RDONLY | libc::O_DIRECTORY,
            cred,
        )?;
        Self::sync(&new)?;
        Self::sync(&dir)?;
        let ino = self.remember(parent_ino, &cname, &dir, &st)?;
        Ok(Self::attrs(&st, ino))
    }

    /// Remove `name` (anything but a directory) from `parent_ino`.
    pub fn unlink(&self, parent_ino: u64, name: &str, cred: &CallerCtx) -> Result<()> {
        self.remove(parent_ino, name, 0, cred)
    }

    pub fn rmdir(&self, parent_ino: u64, name: &str, cred: &CallerCtx) -> Result<()> {
        self.remove(parent_ino, name, libc::AT_REMOVEDIR, cred)
    }

    /// Needs write and search permission on the directory and, if it is
    /// sticky, ownership of the directory or of the entry.
    fn remove(
        &self,
        parent_ino: u64,
        name: &str,
        flags: libc::c_int,
        cred: &CallerCtx,
    ) -> Result<()> {
        self.check_writable()?;
        let cname = Self::validate_name(name)?;
        let (dir, dir_st) = self.open_dir_to_change(parent_ino, cred)?;
        let st = sys::fstatat(&dir, &cname).map_err(map_io_err)?;
        require(
            perm::may_delete(Perm::of(&dir_st), Perm::of(&st), cred),
            not_permitted,
        )?;
        sys::unlinkat(&dir, &cname, flags).map_err(map_io_err)?;
        Self::sync(&dir)?;
        let ino = dev_ino(&st).1;
        let mut nodes = self.nodes.lock();
        if let Some(node) = nodes.get(ino) {
            if node
                .link
                .as_ref()
                .is_some_and(|(p, n)| *p == parent_ino && n.as_bytes() == cname.to_bytes())
            {
                nodes.remove_if(ino, &node);
            }
        }
        Ok(())
    }

    /// Create a symlink at `parent_ino/name` pointing at `target`, owned by
    /// the caller; needs write and search permission on the parent.
    /// The target itself is opaque data and may legitimately point outside
    /// the export — clients resolve symlinks in their own namespace, and the
    /// server never follows one.
    pub fn create_symlink(
        &self,
        parent_ino: u64,
        name: &str,
        target: &str,
        cred: &CallerCtx,
    ) -> Result<InodeAttrs> {
        self.check_writable()?;
        let cname = Self::validate_name(name)?;
        let ctarget = cstring(target.as_bytes())?;
        let (dir, dir_st) = self.open_dir_to_change(parent_ino, cred)?;
        sys::symlinkat(&ctarget, &dir, &cname).map_err(map_io_err)?;
        let (_, st) = self.claim_new(&dir, &dir_st, &cname, libc::O_PATH, cred)?;
        Self::sync(&dir)?;
        let ino = self.remember(parent_ino, &cname, &dir, &st)?;
        Ok(Self::attrs(&st, ino))
    }

    /// Create a hardlink at `parent_ino/name` pointing at the inode for
    /// `target_ino`. Needs write and search permission on the directory,
    /// and [`perm::may_link_source`] on the target.
    pub fn link(
        &self,
        target_ino: u64,
        parent_ino: u64,
        name: &str,
        cred: &CallerCtx,
    ) -> Result<InodeAttrs> {
        self.check_writable()?;
        let cname = Self::validate_name(name)?;
        let (src, st) = self.open_node(target_ino, libc::O_PATH, Want::Any)?;
        if kind_of(&st) == libc::S_IFDIR {
            return Err(NvstorError::IsDir);
        }
        let (dir, _) = self.open_dir_to_change(parent_ino, cred)?;
        require(perm::may_link_source(Perm::of(&st), cred), not_permitted)?;
        sys::link_fd(&src, kind_of(&st) == libc::S_IFLNK, &dir, &cname).map_err(map_io_err)?;
        Self::sync(&dir)?;
        let st = sys::fstatat(&dir, &cname).map_err(map_io_err)?;
        let ino = self.remember(parent_ino, &cname, &dir, &st)?;
        Ok(Self::attrs(&st, ino))
    }

    /// Rename `from_dir/from_name` to `to_dir/to_name`. Filehandles of the
    /// renamed inode stay valid. Needs write and search permission on both
    /// directories; in a sticky directory, ownership of the directory or
    /// of the entry moved away or replaced; and write permission on a
    /// directory moved to another parent (its ".." changes).
    pub fn rename(
        &self,
        from_dir: u64,
        from_name: &str,
        to_dir: u64,
        to_name: &str,
        cred: &CallerCtx,
    ) -> Result<()> {
        self.check_writable()?;
        let from_c = Self::validate_name(from_name)?;
        let to_c = Self::validate_name(to_name)?;
        let (from, from_st) = self.open_dir_to_change(from_dir, cred)?;
        let (to, to_st) = self.open_dir_to_change(to_dir, cred)?;
        let st = sys::fstatat(&from, &from_c).map_err(map_io_err)?;
        require(
            perm::may_delete(Perm::of(&from_st), Perm::of(&st), cred),
            not_permitted,
        )?;
        match sys::fstatat(&to, &to_c) {
            Ok(target) => require(
                perm::may_delete(Perm::of(&to_st), Perm::of(&target), cred),
                not_permitted,
            )?,
            Err(e) if errno(&e) == libc::ENOENT => {}
            Err(e) => return Err(map_io_err(e)),
        }
        if kind_of(&st) == libc::S_IFDIR && dev_ino(&from_st) != dev_ino(&to_st) {
            require(perm::may(Perm::of(&st), cred, perm::MAY_WRITE), denied)?;
        }
        sys::renameat(&from, &from_c, &to, &to_c).map_err(map_io_err)?;
        Self::sync(&to)?;
        if from_dir != to_dir {
            Self::sync(&from)?;
        }
        let (dev, ino) = dev_ino(&st);
        let mut nodes = self.nodes.lock();
        if let Some(node) = nodes.get(ino) {
            if node.dev == dev {
                let moved = Node {
                    link: Some((to_dir, OsString::from(to_name))),
                    ..(*node).clone()
                };
                nodes.put(ino, Arc::new(moved));
            }
        }
        Ok(())
    }

    /// Apply NFS SETATTR fields. NFS SETATTR's bitmap-driven shape lives
    /// in the dispatcher; this method takes already-extracted optional
    /// fields and applies them to the inode itself (a symlink's owner and
    /// times change the link, never its target; its size and mode cannot be
    /// set).
    ///
    /// Every field is authorized before anything changes: size needs
    /// [`perm::may_write_data`]; mode and client-supplied times need the
    /// owner or root; owner and group [`perm::may_chown`]. A chmod by a
    /// caller outside the file's group drops set-group-id, and a truncate by
    /// anyone but root drops set-user-id, as the kernel does locally.
    #[allow(clippy::too_many_arguments)]
    pub fn setattr(
        &self,
        ino: u64,
        size: Option<u64>,
        mode: Option<u32>,
        uid: Option<u32>,
        gid: Option<u32>,
        atime: Option<i64>,
        mtime: Option<i64>,
        cred: &CallerCtx,
    ) -> Result<InodeAttrs> {
        self.check_writable()?;
        let (path_fd, st) = self.open_node(ino, libc::O_PATH, Want::Any)?;
        let p = Perm::of(&st);
        if size.is_some() {
            require(perm::may_write_data(p, cred), denied)?;
        }
        if mode.is_some() || atime.is_some() || mtime.is_some() {
            require(perm::is_owner(p, cred), not_permitted)?;
        }
        if uid.is_some() || gid.is_some() {
            require(perm::may_chown(p, cred, uid, gid), not_permitted)?;
        }
        if let Some(new_size) = size {
            let (fd, fst) = self.open_node(ino, libc::O_WRONLY, Want::Reg)?;
            Self::drop_privs(&fd, &fst, cred)?;
            fs::File::from(fd).set_len(new_size).map_err(map_io_err)?;
        }
        // Owner before mode: a chown clears set-user-id, which must not
        // undo a mode set in the same request.
        if uid.is_some() || gid.is_some() {
            // u32::MAX means "leave unchanged" per chown(2).
            sys::fchown(&path_fd, uid.unwrap_or(u32::MAX), gid.unwrap_or(u32::MAX))
                .map_err(map_io_err)?;
        }
        if let Some(new_mode) = mode {
            let (fd, _) = self.open_node(ino, libc::O_RDONLY, Want::RegOrDir)?;
            let group_after = Perm {
                gid: gid.unwrap_or(p.gid),
                ..p
            };
            let new_mode = perm::chmod_mode(group_after, cred, new_mode);
            sys::fchmod(&fd, new_mode as libc::mode_t).map_err(map_io_err)?;
        }
        if atime.is_some() || mtime.is_some() {
            let make = |sec: Option<i64>| match sec {
                Some(s) => libc::timespec {
                    tv_sec: s as libc::time_t,
                    tv_nsec: 0,
                },
                None => libc::timespec {
                    tv_sec: 0,
                    tv_nsec: libc::UTIME_OMIT,
                },
            };
            sys::futimens(&path_fd, &[make(atime), make(mtime)]).map_err(map_io_err)?;
        }
        self.getattr(ino)
    }

    /// Whether `cred` may do what an NFSv4 ACCESS call asks: returns
    /// `(supported, granted)` ACCESS4 bits for `ino` ([`perm::access`]).
    /// Nothing that changes the object is granted on a read-only export.
    pub fn access(&self, ino: u64, requested: u32, cred: &CallerCtx) -> Result<(u32, u32)> {
        let (_, st) = self.open_node(ino, libc::O_PATH, Want::Any)?;
        Ok(perm::access(Perm::of(&st), cred, self.read_only, requested))
    }

    /// OPEN of existing inode `ino` ([`perm::may_open`]). A file the same
    /// OPEN just created is the caller's and needs no check.
    pub fn check_open(
        &self,
        ino: u64,
        share_access: u32,
        share_deny: u32,
        cred: &CallerCtx,
    ) -> Result<()> {
        let (_, st) = self.open_node(ino, libc::O_PATH, Want::Any)?;
        require(
            perm::may_open(Perm::of(&st), cred, share_access, share_deny),
            denied,
        )
    }

    /// Server-side copy `count` bytes from `src_ino[src_offset..]` to
    /// `dst_ino[dst_offset..]`; both must be regular files, the source
    /// readable ([`perm::may_read_data`]) and the destination writable
    /// ([`perm::may_write_data`]) by the caller.
    ///
    /// NFS v4.2 COPY semantics: `count == 0` means "copy from src_offset to
    /// EOF of source" (RFC 7862 §15.2.3). We translate that to the source
    /// file's size minus src_offset before calling copy_file_range(2),
    /// because the syscall itself treats count=0 as "copy nothing".
    ///
    /// We loop until the requested byte count is copied or the source hits
    /// EOF — copy_file_range may return short copies on some filesystems.
    /// On ENOSYS / EXDEV we fall back to read+write.
    #[allow(clippy::too_many_arguments)]
    pub fn copy_file(
        &self,
        src_ino: u64,
        src_offset: u64,
        dst_ino: u64,
        dst_offset: u64,
        count: u64,
        cred: &CallerCtx,
    ) -> Result<u64> {
        self.check_writable()?;
        let (src, src_st) = self.open_node(src_ino, libc::O_RDONLY, Want::Reg)?;
        require(perm::may_read_data(Perm::of(&src_st), cred), denied)?;
        let (dst, dst_st) = self.open_node(dst_ino, libc::O_WRONLY, Want::Reg)?;
        require(perm::may_write_data(Perm::of(&dst_st), cred), denied)?;
        Self::drop_privs(&dst, &dst_st, cred)?;
        let (src, dst) = (fs::File::from(src), fs::File::from(dst));

        // Resolve count=0 ("everything from src_offset") to the actual
        // remaining byte count on the source.
        let want: u64 = if count == 0 {
            (src_st.st_size as u64).saturating_sub(src_offset)
        } else {
            count
        };
        if want == 0 {
            return Ok(0);
        }

        let mut sof = src_offset as libc::off_t;
        let mut dof = dst_offset as libc::off_t;
        let mut copied: u64 = 0;
        let mut use_syscall = true;
        while copied < want {
            let remaining = (want - copied) as libc::size_t;
            if use_syscall {
                // SAFETY: both fds are owned by the File handles for the
                // duration of the call.
                let n = unsafe {
                    libc::copy_file_range(
                        src.as_raw_fd(),
                        &mut sof,
                        dst.as_raw_fd(),
                        &mut dof,
                        remaining,
                        0,
                    )
                };
                if n < 0 {
                    let err = io::Error::last_os_error();
                    // Fall back to userspace copy on kernels / fs that don't
                    // support copy_file_range.
                    if matches!(errno(&err), libc::ENOSYS | libc::EXDEV | libc::EOPNOTSUPP) {
                        use_syscall = false;
                        continue;
                    }
                    return Err(map_io_err(err));
                }
                if n == 0 {
                    // EOF on source.
                    break;
                }
                copied += n as u64;
            } else {
                let mut buf = vec![0u8; remaining.min(64 * 1024)];
                let n = src.read_at(&mut buf, sof as u64).map_err(map_io_err)?;
                if n == 0 {
                    break;
                }
                dst.write_all_at(&buf[..n], dof as u64)
                    .map_err(map_io_err)?;
                sof += n as libc::off_t;
                dof += n as libc::off_t;
                copied += n as u64;
            }
        }
        Ok(copied)
    }

    /// Writing a user xattr (as the kernel's `xattr_permission`): write
    /// permission, and on a sticky directory only its owner or root.
    fn may_write_xattr(st: &libc::stat, cred: &CallerCtx) -> Result<()> {
        let p = Perm::of(st);
        require(perm::may(p, cred, perm::MAY_WRITE), denied)?;
        require(
            !p.is_dir() || !p.is_sticky() || perm::is_owner(p, cred),
            not_permitted,
        )
    }

    /// Get user extended attribute `name` ("user." is implied, see
    /// [`XATTR_USER_PREFIX`]) of a regular file or directory; needs read
    /// permission.
    pub fn getxattr(&self, ino: u64, name: &str, cred: &CallerCtx) -> Result<Vec<u8>> {
        let (fd, st) = self.open_node(ino, libc::O_RDONLY, Want::RegOrDir)?;
        require(perm::may(Perm::of(&st), cred, perm::MAY_READ), denied)?;
        let cname = user_xattr_name(name)?;
        // First call with size=0 to discover required buffer.
        // SAFETY: a null buffer of size 0 only queries the size.
        let needed =
            unsafe { libc::fgetxattr(fd.as_raw_fd(), cname.as_ptr(), std::ptr::null_mut(), 0) };
        if needed < 0 {
            return Err(map_io_err(io::Error::last_os_error()));
        }
        let mut buf = vec![0u8; needed as usize];
        // SAFETY: `buf` has `buf.len()` writable bytes.
        let n = unsafe {
            libc::fgetxattr(
                fd.as_raw_fd(),
                cname.as_ptr(),
                buf.as_mut_ptr() as *mut libc::c_void,
                buf.len(),
            )
        };
        if n < 0 {
            return Err(map_io_err(io::Error::last_os_error()));
        }
        buf.truncate(n as usize);
        Ok(buf)
    }

    /// Set user extended attribute `name` ("user." implied); needs write
    /// permission. `option` mirrors NFS v4.2 SETXATTR4_OPTION:
    /// 0 = SETXATTR4_EITHER, 1 = SETXATTR4_CREATE, 2 = SETXATTR4_REPLACE.
    pub fn setxattr(
        &self,
        ino: u64,
        name: &str,
        value: &[u8],
        option: u32,
        cred: &CallerCtx,
    ) -> Result<()> {
        self.check_writable()?;
        let (fd, st) = self.open_node(ino, libc::O_RDONLY, Want::RegOrDir)?;
        Self::may_write_xattr(&st, cred)?;
        let cname = user_xattr_name(name)?;
        let flags = match option {
            1 => libc::XATTR_CREATE,
            2 => libc::XATTR_REPLACE,
            _ => 0,
        };
        // SAFETY: `value` is readable for its length; `cname` is NUL-terminated.
        let rc = unsafe {
            libc::fsetxattr(
                fd.as_raw_fd(),
                cname.as_ptr(),
                value.as_ptr() as *const libc::c_void,
                value.len(),
                flags,
            )
        };
        if rc != 0 {
            return Err(map_io_err(io::Error::last_os_error()));
        }
        Ok(())
    }

    /// List user extended attribute names, without the "user." prefix;
    /// needs read permission (as Linux nfsd's LISTXATTRS).
    pub fn listxattrs(&self, ino: u64, cred: &CallerCtx) -> Result<Vec<String>> {
        let (fd, st) = self.open_node(ino, libc::O_RDONLY, Want::RegOrDir)?;
        require(perm::may(Perm::of(&st), cred, perm::MAY_READ), denied)?;
        // SAFETY: a null buffer of size 0 only queries the size.
        let needed = unsafe { libc::flistxattr(fd.as_raw_fd(), std::ptr::null_mut(), 0) };
        if needed < 0 {
            return Err(map_io_err(io::Error::last_os_error()));
        }
        if needed == 0 {
            return Ok(Vec::new());
        }
        let mut buf = vec![0u8; needed as usize];
        // SAFETY: `buf` has `buf.len()` writable bytes.
        let n = unsafe {
            libc::flistxattr(
                fd.as_raw_fd(),
                buf.as_mut_ptr() as *mut libc::c_char,
                buf.len(),
            )
        };
        if n < 0 {
            return Err(map_io_err(io::Error::last_os_error()));
        }
        buf.truncate(n as usize);
        Ok(buf
            .split(|b| *b == 0)
            .filter_map(|s| s.strip_prefix(XATTR_USER_PREFIX.as_bytes()))
            .filter(|s| !s.is_empty())
            .map(|s| String::from_utf8_lossy(s).into_owned())
            .collect())
    }

    /// Remove user extended attribute `name` ("user." implied); needs
    /// write permission.
    pub fn removexattr(&self, ino: u64, name: &str, cred: &CallerCtx) -> Result<()> {
        self.check_writable()?;
        let (fd, st) = self.open_node(ino, libc::O_RDONLY, Want::RegOrDir)?;
        Self::may_write_xattr(&st, cred)?;
        let cname = user_xattr_name(name)?;
        // SAFETY: `cname` is NUL-terminated.
        let rc = unsafe { libc::fremovexattr(fd.as_raw_fd(), cname.as_ptr()) };
        if rc != 0 {
            return Err(map_io_err(io::Error::last_os_error()));
        }
        Ok(())
    }

    /// fallocate / fdeallocate on a regular file. `deallocate=false`
    /// allocates with mode=0; `deallocate=true` punches a hole
    /// (FALLOC_FL_PUNCH_HOLE | KEEP_SIZE). Access as [`Self::write`].
    pub fn allocate(
        &self,
        ino: u64,
        offset: u64,
        length: u64,
        deallocate: bool,
        cred: &CallerCtx,
    ) -> Result<()> {
        self.check_writable()?;
        let (fd, st) = self.open_node(ino, libc::O_WRONLY, Want::Reg)?;
        require(perm::may_write_data(Perm::of(&st), cred), denied)?;
        Self::drop_privs(&fd, &st, cred)?;
        let mode = if deallocate {
            libc::FALLOC_FL_PUNCH_HOLE | libc::FALLOC_FL_KEEP_SIZE
        } else {
            0
        };
        // SAFETY: fd is owned for the syscall's duration.
        let rc = unsafe {
            libc::fallocate(
                fd.as_raw_fd(),
                mode,
                offset as libc::off_t,
                length as libc::off_t,
            )
        };
        if rc != 0 {
            return Err(map_io_err(io::Error::last_os_error()));
        }
        Ok(())
    }

    #[cfg(test)]
    fn cached_nodes(&self) -> usize {
        self.nodes.lock().len()
    }
}

#[cfg(test)]
#[path = "passthrough_tests.rs"]
mod tests;

/// Alignment for O_DIRECT reads: covers 512 B and 4 KiB logical blocks.
pub const DIRECT_IO_ALIGN: usize = 4096;

/// Positional read until `buf` is full or end of file. A direct read is
/// short only at end of file, and a follow-up at the unaligned offset would
/// fail with EINVAL, so `direct` stops at the first short read.
fn read_full(file: &fs::File, buf: &mut [u8], offset: u64, direct: bool) -> io::Result<usize> {
    let mut done = 0;
    while done < buf.len() {
        match file.read_at(&mut buf[done..], offset + done as u64) {
            Ok(0) => break,
            Ok(n) => {
                done += n;
                if direct && !done.is_multiple_of(DIRECT_IO_ALIGN) {
                    break;
                }
            }
            Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
            Err(e) => return Err(e),
        }
    }
    Ok(done)
}
