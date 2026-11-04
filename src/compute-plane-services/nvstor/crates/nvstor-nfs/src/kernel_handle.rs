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

//! Kernel file handles (`name_to_handle_at` / `open_by_handle_at`).
//!
//! A kernel file handle names an inode independently of any path or open
//! file, and stays valid across process restarts for as long as the inode
//! exists. On ext4, xfs, btrfs and tmpfs it carries the inode generation, so
//! the handle of a deleted file never names a new file that reuses the inode
//! number. The passthrough export embeds each inode's own handle in its NFS
//! filehandle, both as the inode's identity and so that a restarted server
//! can resolve handles that clients obtained from the previous process.
//! Encoding needs no privilege; decoding needs CAP_DAC_READ_SEARCH.

use std::ffi::{CStr, CString};
use std::io;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::ffi::OsStrExt;
use std::path::Path;

/// MAX_HANDLE_SZ in the kernel.
const MAX_HANDLE_BYTES: usize = 128;

/// `struct file_handle` with inline storage.
#[repr(C)]
struct RawHandle {
    handle_bytes: u32,
    handle_type: i32,
    f_handle: [u8; MAX_HANDLE_BYTES],
}

/// An opaque kernel file handle.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct KernelHandle {
    pub handle_type: i32,
    pub bytes: Vec<u8>,
}

impl KernelHandle {
    /// Handle for `path` (symlinks not followed) and the id of the mount it
    /// lives on.
    #[cfg(test)]
    pub fn of_path(path: &Path) -> io::Result<(KernelHandle, i32)> {
        let name = CString::new(path.as_os_str().as_bytes())
            .map_err(|_| io::Error::from_raw_os_error(libc::EINVAL))?;
        Self::at(libc::AT_FDCWD, &name, 0)
    }

    /// Handle for an open file descriptor (O_PATH included) and the id of
    /// its mount.
    pub fn of_fd(fd: &OwnedFd) -> io::Result<(KernelHandle, i32)> {
        Self::at(fd.as_raw_fd(), c"", libc::AT_EMPTY_PATH)
    }

    /// Handle for `name` in directory `dir` (a symlink is not followed) and
    /// the id of its mount.
    pub fn of_child(dir: &OwnedFd, name: &CStr) -> io::Result<(KernelHandle, i32)> {
        Self::at(dir.as_raw_fd(), name, 0)
    }

    fn at(dirfd: libc::c_int, name: &CStr, flags: libc::c_int) -> io::Result<(KernelHandle, i32)> {
        let mut raw = RawHandle {
            handle_bytes: MAX_HANDLE_BYTES as u32,
            handle_type: 0,
            f_handle: [0; MAX_HANDLE_BYTES],
        };
        let mut mount_id: libc::c_int = 0;
        // SAFETY: `raw` is a file_handle with MAX_HANDLE_BYTES of storage,
        // which `handle_bytes` declares; `name` is NUL-terminated.
        let rc = unsafe {
            libc::syscall(
                libc::SYS_name_to_handle_at,
                dirfd,
                name.as_ptr(),
                &mut raw as *mut RawHandle,
                &mut mount_id as *mut libc::c_int,
                flags,
            )
        };
        if rc != 0 {
            return Err(io::Error::last_os_error());
        }
        let len = (raw.handle_bytes as usize).min(MAX_HANDLE_BYTES);
        Ok((
            KernelHandle {
                handle_type: raw.handle_type,
                bytes: raw.f_handle[..len].to_vec(),
            },
            mount_id,
        ))
    }

    /// Open the inode as an O_PATH descriptor. See [`Self::open_with`].
    pub fn open(&self, mount_fd: &OwnedFd) -> io::Result<OwnedFd> {
        self.open_with(mount_fd, libc::O_PATH)
    }

    /// Open the inode with `flags` (O_CLOEXEC is added). `mount_fd` is any
    /// descriptor on the filesystem the handle came from, opened without
    /// O_PATH (the kernel looks it up with `fdget`, which ignores O_PATH
    /// files and fails with EBADF). No path is resolved, so no symlink is
    /// followed; opening a symlink without O_PATH fails with ELOOP. Fails
    /// with ESTALE once the inode is gone or its generation changed, and
    /// with EPERM without CAP_DAC_READ_SEARCH.
    pub fn open_with(&self, mount_fd: &OwnedFd, flags: libc::c_int) -> io::Result<OwnedFd> {
        if self.bytes.len() > MAX_HANDLE_BYTES {
            return Err(io::Error::from_raw_os_error(libc::EINVAL));
        }
        let mut raw = RawHandle {
            handle_bytes: self.bytes.len() as u32,
            handle_type: self.handle_type,
            f_handle: [0; MAX_HANDLE_BYTES],
        };
        raw.f_handle[..self.bytes.len()].copy_from_slice(&self.bytes);
        // SAFETY: `raw` is a valid file_handle whose `handle_bytes` fits its
        // storage.
        let fd = unsafe {
            libc::syscall(
                libc::SYS_open_by_handle_at,
                mount_fd.as_raw_fd(),
                &mut raw as *mut RawHandle,
                flags | libc::O_CLOEXEC,
            )
        };
        if fd < 0 {
            return Err(io::Error::last_os_error());
        }
        // SAFETY: the syscall returned a new descriptor that nothing else owns.
        Ok(unsafe { OwnedFd::from_raw_fd(fd as libc::c_int) })
    }
}

/// Open a directory read-only: usable both for `name_to_handle_at` and as
/// the mount descriptor of `open_by_handle_at` (which rejects O_PATH).
pub fn open_dir_path(path: &Path) -> io::Result<OwnedFd> {
    let name = CString::new(path.as_os_str().as_bytes())
        .map_err(|_| io::Error::from_raw_os_error(libc::EINVAL))?;
    // SAFETY: `name` is NUL-terminated.
    let fd = unsafe {
        libc::open(
            name.as_ptr(),
            libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC,
        )
    };
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }
    // SAFETY: open returned a new descriptor that nothing else owns.
    Ok(unsafe { OwnedFd::from_raw_fd(fd) })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn handle_of_path_matches_handle_of_fd() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let fd = open_dir_path(dir.path()).expect("open");
        let (by_path, mount_a) = KernelHandle::of_path(dir.path()).expect("of_path");
        let (by_fd, mount_b) = KernelHandle::of_fd(&fd).expect("of_fd");
        assert_eq!(by_path, by_fd);
        assert_eq!(mount_a, mount_b);
        assert!(!by_path.bytes.is_empty());
    }

    /// A child's handle names that inode (a symlink's handle is the link's
    /// own), and a file recreated under the same name gets a new handle.
    #[test]
    fn child_handles_name_the_inode_itself() {
        let dir = tempfile::tempdir().expect("tmpdir");
        std::fs::write(dir.path().join("f"), b"x").expect("write");
        std::os::unix::fs::symlink("f", dir.path().join("l")).expect("symlink");
        let fd = open_dir_path(dir.path()).expect("open");
        let f = KernelHandle::of_child(&fd, c"f").expect("f").0;
        let l = KernelHandle::of_child(&fd, c"l").expect("l").0;
        assert_ne!(f, l);
        assert_eq!(
            f,
            KernelHandle::of_path(&dir.path().join("f"))
                .expect("path")
                .0
        );
        std::fs::remove_file(dir.path().join("f")).expect("rm");
        std::fs::write(dir.path().join("f"), b"y").expect("write");
        assert_ne!(f, KernelHandle::of_child(&fd, c"f").expect("f again").0);
    }

    /// `open_by_handle_at` looks up its mount descriptor with `fdget`, which
    /// ignores O_PATH files: an O_PATH root fails every decode with EBADF,
    /// privileged or not. The kernel checks the descriptor before the
    /// capability, so without CAP_DAC_READ_SEARCH a usable descriptor gets
    /// EPERM and an unusable one EBADF; with it, the decode succeeds.
    #[test]
    fn decode_accepts_the_directory_descriptor() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let fd = open_dir_path(dir.path()).expect("open");
        let handle = KernelHandle::of_fd(&fd).expect("encode").0;
        match handle.open(&fd) {
            Ok(_) => {}
            Err(e) => assert_eq!(e.raw_os_error(), Some(libc::EPERM), "{e}"),
        }
    }

    #[test]
    fn missing_path_and_oversized_handle_fail() {
        let dir = tempfile::tempdir().expect("tmpdir");
        let err = KernelHandle::of_path(&dir.path().join("absent")).unwrap_err();
        assert_eq!(err.raw_os_error(), Some(libc::ENOENT));
        let fd = open_dir_path(dir.path()).expect("open");
        let huge = KernelHandle {
            handle_type: 1,
            bytes: vec![0; MAX_HANDLE_BYTES + 1],
        };
        assert_eq!(
            huge.open(&fd).unwrap_err().raw_os_error(),
            Some(libc::EINVAL)
        );
    }
}
