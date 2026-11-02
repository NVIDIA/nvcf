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

//! VFS backend dispatcher — routes NFS operations to either
//! the synthetic VFS (ChunkManager-backed) or the pass-through VFS
//! (real filesystem).
//!
//! Operations that touch a name or an inode's data take the caller's
//! credentials. The pass-through VFS authorizes them itself, against the
//! inode it opened (see `passthrough.rs`, "Access control"); the synthetic
//! VFS keeps its own checks in the COMPOUND processor and ignores them here.

use std::sync::Arc;

use crate::nfs4::CallerCtx;
use crate::passthrough::PassthroughVfs;
use crate::vfs::{DirEntry, InodeAttrs, LockRange, LockType, Vfs};
use nvstor_core::error::{NvstorError, Result};

/// VFS backend — either synthetic (nvstor storage) or pass-through (real FS).
pub enum VfsBackend {
    /// nvstor's synthetic VFS backed by ChunkManager + RocksDB.
    Synthetic(Arc<Vfs>),
    /// Pass-through to a real directory.
    Passthrough(Arc<PassthroughVfs>),
}

impl VfsBackend {
    pub fn ino_to_filehandle(&self, ino: u64) -> Vec<u8> {
        match self {
            Self::Synthetic(v) => v.ino_to_filehandle(ino),
            Self::Passthrough(v) => v.ino_to_filehandle(ino),
        }
    }

    pub fn filehandle_to_ino(&self, fh: &[u8]) -> Result<u64> {
        match self {
            Self::Synthetic(v) => v.filehandle_to_ino(fh),
            Self::Passthrough(v) => v.filehandle_to_ino(fh),
        }
    }

    pub fn lookup(&self, parent_ino: u64, name: &str, ctx: &CallerCtx) -> Result<InodeAttrs> {
        match self {
            Self::Synthetic(v) => v.lookup(parent_ino, name),
            Self::Passthrough(v) => v.lookup(parent_ino, name, ctx),
        }
    }

    pub fn getattr(&self, ino: u64) -> Result<InodeAttrs> {
        match self {
            Self::Synthetic(v) => v.getattr(ino),
            Self::Passthrough(v) => v.getattr(ino),
        }
    }

    /// Full SETATTR for passthrough — wires every field NFS SETATTR can
    /// carry. Synthetic VFS only knows size today; the other fields are
    /// silently ignored there until the synthetic VFS grows full attr
    /// support (tracked separately).
    #[allow(clippy::too_many_arguments)]
    pub fn setattr_full(
        &self,
        ino: u64,
        size: Option<u64>,
        mode: Option<u32>,
        uid: Option<u32>,
        gid: Option<u32>,
        atime: Option<i64>,
        mtime: Option<i64>,
        ctx: &CallerCtx,
    ) -> Result<InodeAttrs> {
        match self {
            Self::Synthetic(v) => {
                if let Some(m) = mode {
                    v.set_mode(ino, m);
                }
                if uid.is_some() || gid.is_some() {
                    v.set_owner(ino, uid.unwrap_or(0), gid.unwrap_or(0));
                }
                let _ = (atime, mtime);
                v.setattr(ino, size)
            }
            Self::Passthrough(v) => v.setattr(ino, size, mode, uid, gid, atime, mtime, ctx),
        }
    }

    pub fn readdir(
        &self,
        dir_ino: u64,
        cookie: u64,
        ctx: &CallerCtx,
    ) -> Result<(Vec<DirEntry>, bool)> {
        match self {
            Self::Synthetic(v) => v.readdir(dir_ino, cookie),
            Self::Passthrough(v) => v.readdir(dir_ino, cookie, ctx),
        }
    }

    /// Verifier of the READDIR cookies of directory `dir_ino`, returned with
    /// every listing and checked when a client continues from a cookie.
    /// The synthetic VFS's cookies are positions in its sorted listing, so
    /// its verifier changes with every entry added, removed or renamed. The
    /// pass-through cookies are the filesystem's own directory offsets
    /// (`d_off`), valid for as long as the directory itself exists, so the
    /// verifier names the directory: a digest of its sealed filehandle,
    /// which carries the inode generation and changes with the key.
    pub fn readdir_verifier(&self, dir_ino: u64) -> Result<[u8; 8]> {
        match self {
            Self::Synthetic(v) => v.readdir_verifier(dir_ino),
            Self::Passthrough(v) => Ok(crate::vfs::cookie_verifier(&[
                b"nvstor-nfs passthrough readdir",
                &v.ino_to_filehandle(dir_ino),
            ])),
        }
    }

    pub fn read(&self, ino: u64, offset: u64, len: u64, ctx: &CallerCtx) -> Result<Vec<u8>> {
        match self {
            Self::Synthetic(v) => v.read(ino, offset, len),
            Self::Passthrough(v) => v.read(ino, offset, len, ctx),
        }
    }

    /// Read into `buf` (no intermediate allocation). Returns the bytes valid
    /// at the front of `buf`; fewer than `buf.len()` only at end of file.
    pub fn read_into(
        &self,
        ino: u64,
        offset: u64,
        buf: &mut [u8],
        ctx: &CallerCtx,
    ) -> Result<usize> {
        match self {
            Self::Synthetic(v) => v.read_into(ino, offset, buf),
            Self::Passthrough(v) => v.read_into(ino, offset, buf, ctx),
        }
    }

    pub fn write(&self, ino: u64, offset: u64, data: &[u8], ctx: &CallerCtx) -> Result<usize> {
        match self {
            Self::Synthetic(v) => v.write(ino, offset, data),
            Self::Passthrough(v) => v.write(ino, offset, data, ctx),
        }
    }

    /// Create regular file `name`, or open it if it exists. Returns its
    /// attributes and whether this call created it (the synthetic VFS
    /// refuses an existing name with `Exists`).
    pub fn create_file(
        &self,
        parent_ino: u64,
        name: &str,
        mode: u32,
        ctx: &CallerCtx,
    ) -> Result<(InodeAttrs, bool)> {
        match self {
            Self::Synthetic(v) => v
                .create_file(parent_ino, name, mode)
                .map(|attrs| (attrs, true)),
            Self::Passthrough(v) => v.create_file(parent_ino, name, mode, ctx),
        }
    }

    pub fn mkdir(
        &self,
        parent_ino: u64,
        name: &str,
        mode: u32,
        ctx: &CallerCtx,
    ) -> Result<InodeAttrs> {
        match self {
            Self::Synthetic(v) => v.mkdir(parent_ino, name, mode),
            Self::Passthrough(v) => v.mkdir(parent_ino, name, mode, ctx),
        }
    }

    pub fn unlink(&self, parent_ino: u64, name: &str, ctx: &CallerCtx) -> Result<()> {
        match self {
            Self::Synthetic(v) => v.unlink(parent_ino, name),
            Self::Passthrough(v) => v.unlink(parent_ino, name, ctx),
        }
    }

    pub fn rmdir(&self, parent_ino: u64, name: &str, ctx: &CallerCtx) -> Result<()> {
        match self {
            Self::Synthetic(v) => v.rmdir(parent_ino, name),
            Self::Passthrough(v) => v.rmdir(parent_ino, name, ctx),
        }
    }

    pub fn lookupp(&self, ino: u64, ctx: &CallerCtx) -> Result<InodeAttrs> {
        match self {
            Self::Synthetic(v) => v.lookupp(ino),
            Self::Passthrough(v) => v.lookupp(ino, ctx),
        }
    }

    pub fn readlink(&self, ino: u64) -> Result<String> {
        match self {
            Self::Synthetic(v) => v.readlink(ino),
            Self::Passthrough(v) => v.readlink(ino),
        }
    }

    pub fn rename(
        &self,
        from_dir: u64,
        from_name: &str,
        to_dir: u64,
        to_name: &str,
        ctx: &CallerCtx,
    ) -> Result<()> {
        match self {
            Self::Synthetic(v) => v.rename(from_dir, from_name, to_dir, to_name),
            Self::Passthrough(v) => v.rename(from_dir, from_name, to_dir, to_name, ctx),
        }
    }

    pub fn lock(
        &self,
        ino: u64,
        offset: u64,
        length: u64,
        lock_type: LockType,
        owner: &[u8],
        clientid: u64,
    ) -> Result<()> {
        match self {
            Self::Synthetic(v) => v.lock(ino, offset, length, lock_type, owner, clientid),
            Self::Passthrough(_) => Ok(()), // Passthrough ignores locks.
        }
    }

    pub fn lock_test(
        &self,
        ino: u64,
        offset: u64,
        length: u64,
        lock_type: LockType,
        owner: &[u8],
    ) -> Option<LockRange> {
        match self {
            Self::Synthetic(v) => v.lock_test(ino, offset, length, lock_type, owner),
            Self::Passthrough(_) => None, // No locks in passthrough.
        }
    }

    pub fn unlock(&self, ino: u64, offset: u64, length: u64, owner: &[u8]) -> Result<()> {
        match self {
            Self::Synthetic(v) => v.unlock(ino, offset, length, owner),
            Self::Passthrough(_) => Ok(()),
        }
    }

    pub fn locks_empty(&self) -> bool {
        match self {
            Self::Synthetic(v) => v.locks_empty(),
            Self::Passthrough(v) => v.locks_empty(),
        }
    }

    pub fn create_symlink(
        &self,
        parent_ino: u64,
        name: &str,
        target: &str,
        ctx: &CallerCtx,
    ) -> Result<InodeAttrs> {
        match self {
            Self::Synthetic(v) => v.create_symlink(parent_ino, name, target),
            Self::Passthrough(v) => v.create_symlink(parent_ino, name, target, ctx),
        }
    }

    pub fn link(
        &self,
        target_ino: u64,
        parent_ino: u64,
        name: &str,
        ctx: &CallerCtx,
    ) -> Result<InodeAttrs> {
        match self {
            Self::Synthetic(v) => v.link(target_ino, parent_ino, name),
            Self::Passthrough(v) => v.link(target_ino, parent_ino, name, ctx),
        }
    }

    #[allow(clippy::too_many_arguments)]
    pub fn copy_file(
        &self,
        src_ino: u64,
        src_offset: u64,
        dst_ino: u64,
        dst_offset: u64,
        count: u64,
        ctx: &CallerCtx,
    ) -> Result<u64> {
        match self {
            Self::Synthetic(v) => v.copy_file(src_ino, src_offset, dst_ino, dst_offset, count),
            Self::Passthrough(v) => {
                v.copy_file(src_ino, src_offset, dst_ino, dst_offset, count, ctx)
            }
        }
    }

    pub fn set_mode(&self, ino: u64, mode: u32) {
        match self {
            Self::Synthetic(v) => v.set_mode(ino, mode),
            Self::Passthrough(_) => {} // chmod not supported in passthrough.
        }
    }

    /// Set uid/gid on an inode.
    ///
    /// Used by `op_create` / `op_open(CREATE)` after a file is created so
    /// the new inode's owner reflects the caller. The pass-through
    /// VFS gives every inode it creates to the caller itself (`fchown` on
    /// the new inode, see `passthrough.rs`), so this is a no-op there.
    pub fn set_owner(&self, ino: u64, uid: u32, gid: u32) {
        match self {
            Self::Synthetic(v) => v.set_owner(ino, uid, gid),
            Self::Passthrough(_) => {}
        }
    }

    pub fn allocate(&self, ino: u64, offset: u64, length: u64, ctx: &CallerCtx) -> Result<()> {
        match self {
            Self::Synthetic(v) => v.allocate(ino, offset, length),
            Self::Passthrough(v) => v.allocate(ino, offset, length, false, ctx),
        }
    }

    pub fn deallocate(&self, ino: u64, offset: u64, length: u64, ctx: &CallerCtx) -> Result<()> {
        match self {
            // Synthetic Vfs treats DEALLOCATE the same as a zero fill — until it
            // grows native sparse-file support. Acceptable since v4.2 deallocate
            // is "may be served as a zero-fill" per RFC 7862 §15.4.
            Self::Synthetic(v) => v.allocate(ino, offset, length),
            Self::Passthrough(v) => v.allocate(ino, offset, length, true, ctx),
        }
    }

    pub fn release_locks_by_owner(&self, owner: &[u8]) {
        match self {
            Self::Synthetic(v) => v.release_locks_by_owner(owner),
            Self::Passthrough(_) => {}
        }
    }

    pub fn getxattr(&self, ino: u64, name: &str, ctx: &CallerCtx) -> Result<Vec<u8>> {
        match self {
            // Synthetic Vfs has no xattr backing — return ENOATTR-equivalent.
            Self::Synthetic(_) => Err(NvstorError::NotFound),
            Self::Passthrough(v) => v.getxattr(ino, name, ctx),
        }
    }

    pub fn setxattr(
        &self,
        ino: u64,
        name: &str,
        value: &[u8],
        option: u32,
        ctx: &CallerCtx,
    ) -> Result<()> {
        match self {
            Self::Synthetic(_) => Err(NvstorError::InvalidArgument(
                "xattrs not supported on synthetic VFS".into(),
            )),
            Self::Passthrough(v) => v.setxattr(ino, name, value, option, ctx),
        }
    }

    pub fn listxattrs(&self, ino: u64, ctx: &CallerCtx) -> Result<Vec<String>> {
        match self {
            Self::Synthetic(_) => Ok(Vec::new()),
            Self::Passthrough(v) => v.listxattrs(ino, ctx),
        }
    }

    pub fn removexattr(&self, ino: u64, name: &str, ctx: &CallerCtx) -> Result<()> {
        match self {
            Self::Synthetic(_) => Err(NvstorError::NotFound),
            Self::Passthrough(v) => v.removexattr(ino, name, ctx),
        }
    }

    /// Make inode `ino` durable. WRITE (DATA_SYNC4 / FILE_SYNC4) and COMMIT
    /// report stability only after this succeeds.
    pub fn fsync(&self, ino: u64) -> Result<()> {
        match self {
            // File content rides PmStore written without sync; Vfs::fsync
            // issues the fdatasync that COMMIT promises.
            Self::Synthetic(v) => v.fsync(ino),
            // Writes land in the page cache; fsync the file itself.
            Self::Passthrough(v) => v.fsync(ino),
        }
    }

    pub fn seek(&self, ino: u64, offset: u64, what: u32) -> Result<(bool, u64)> {
        match self {
            Self::Synthetic(v) => v.seek(ino, offset, what),
            Self::Passthrough(_) => {
                // Passthrough doesn't support SEEK_DATA/SEEK_HOLE.
                Ok((true, offset))
            }
        }
    }

    /// Mode check for READ / WRITE of `ino` on the synthetic VFS. The
    /// pass-through VFS checks inside `read`, `write` and the other data
    /// operations, against the inode they open (no second open, no window
    /// between check and use), so this passes there.
    pub fn check_permission(&self, ino: u64, uid: u32, gid: u32, access: u32) -> Result<()> {
        match self {
            Self::Synthetic(v) => v.check_permission(ino, uid, gid, access),
            Self::Passthrough(_) => Ok(()),
        }
    }

    /// NFSv4 ACCESS: `(supported, granted)` for the `requested` bits. The
    /// synthetic VFS grants whatever is asked (its checks happen per
    /// operation); the pass-through VFS reports what the caller's
    /// credentials actually allow.
    pub fn access(&self, ino: u64, requested: u32, ctx: &CallerCtx) -> Result<(u32, u32)> {
        match self {
            Self::Synthetic(_) => Ok((requested, requested)),
            Self::Passthrough(v) => v.access(ino, requested, ctx),
        }
    }

    /// OPEN of an existing inode with `share_access` / `share_deny`
    /// (NFS4 OPEN4_SHARE_* bits). Only the pass-through VFS checks.
    pub fn check_open(
        &self,
        ino: u64,
        share_access: u32,
        share_deny: u32,
        ctx: &CallerCtx,
    ) -> Result<()> {
        match self {
            Self::Synthetic(_) => Ok(()),
            Self::Passthrough(v) => v.check_open(ino, share_access, share_deny, ctx),
        }
    }
}
