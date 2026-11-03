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

//! Directory-relative file operations that never follow a symlink the
//! caller did not name itself.
//!
//! Both sides of a peer copy work below a directory someone else may be
//! able to write into: the export serves a tree a workload is still
//! changing, and the fetcher writes into a destination a shared cache or
//! another container can touch. Path-based calls (`std::fs`) resolve
//! symlinks in every component, so these helpers take a directory
//! descriptor and resolve with `openat2(RESOLVE_BENEATH |
//! RESOLVE_NO_SYMLINKS)` or single-component `*at` calls instead.

use std::ffi::{CStr, CString};
use std::io;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd};
use std::os::unix::ffi::OsStrExt;
use std::path::Path;

pub fn cstring(s: &[u8]) -> io::Result<CString> {
    CString::new(s).map_err(|_| io::Error::new(io::ErrorKind::InvalidInput, "path contains NUL"))
}

fn owned(fd: libc::c_long) -> io::Result<OwnedFd> {
    if fd < 0 {
        return Err(io::Error::last_os_error());
    }
    let fd = i32::try_from(fd).map_err(|_| io::Error::other("descriptor out of range"))?;
    // SAFETY: `fd` is a fresh descriptor nothing else owns.
    Ok(unsafe { OwnedFd::from_raw_fd(fd) })
}

/// Open a directory by an absolute or cwd-relative path (following
/// symlinks: the caller named it).
pub fn open_dir(path: &Path) -> io::Result<OwnedFd> {
    let c = cstring(path.as_os_str().as_bytes())?;
    // SAFETY: valid C string; the descriptor is owned by the result.
    owned(
        unsafe {
            libc::open(
                c.as_ptr(),
                libc::O_RDONLY | libc::O_DIRECTORY | libc::O_CLOEXEC,
            )
        }
        .into(),
    )
}

/// Times `openat2` is tried when it answers EAGAIN.
const OPENAT2_ATTEMPTS: u32 = 32;

/// `openat2(dir, rel, flags, resolve)`. An empty `rel` reopens `dir`
/// itself. With RESOLVE_BENEATH (or RESOLVE_IN_ROOT) the kernel answers
/// EAGAIN when a rename or mount anywhere on the system raced the walk
/// through a `..`; it asks the caller to retry, which this does.
pub fn openat2(dir: &OwnedFd, rel: &str, flags: i32, resolve: u64) -> io::Result<OwnedFd> {
    let path = cstring(if rel.is_empty() { b"." } else { rel.as_bytes() })?;
    // SAFETY: open_how is plain integers; all-zero is valid.
    let mut how: libc::open_how = unsafe { std::mem::zeroed() };
    how.flags = (flags | libc::O_CLOEXEC) as u64;
    how.resolve = resolve;
    let mut attempt = 0;
    loop {
        // SAFETY: valid dirfd, C string and open_how of the size passed.
        let r = owned(unsafe {
            libc::syscall(
                libc::SYS_openat2,
                dir.as_raw_fd(),
                path.as_ptr(),
                &how as *const libc::open_how,
                std::mem::size_of::<libc::open_how>(),
            )
        });
        match r {
            Err(e) if e.raw_os_error() == Some(libc::EAGAIN) && attempt + 1 < OPENAT2_ATTEMPTS => {
                attempt += 1;
                // Let the rename that raced the walk finish: yield first,
                // then sleep a little longer each time (at most ~25 ms in
                // all).
                if attempt < 4 {
                    std::thread::yield_now();
                } else {
                    std::thread::sleep(std::time::Duration::from_micros(50 * u64::from(attempt)));
                }
            }
            r => return r,
        }
    }
}

/// `rel` under `dir` as a directory, refusing a symlink in any
/// component and any escape from `dir`.
pub fn open_dir_beneath(dir: &OwnedFd, rel: &str) -> io::Result<OwnedFd> {
    openat2(
        dir,
        rel,
        libc::O_RDONLY | libc::O_DIRECTORY,
        libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS,
    )
}

/// `lstat` of one name in `dir`; `None` if it does not exist.
pub fn stat_at(dir: &OwnedFd, name: &CStr) -> io::Result<Option<libc::stat>> {
    // SAFETY: `st` is written by fstatat before it is read.
    let mut st: libc::stat = unsafe { std::mem::zeroed() };
    // SAFETY: valid dirfd, C string and out pointer.
    if unsafe {
        libc::fstatat(
            dir.as_raw_fd(),
            name.as_ptr(),
            &mut st,
            libc::AT_SYMLINK_NOFOLLOW,
        )
    } != 0
    {
        let e = io::Error::last_os_error();
        return if e.raw_os_error() == Some(libc::ENOENT) {
            Ok(None)
        } else {
            Err(e)
        };
    }
    Ok(Some(st))
}

pub fn fstat(fd: &OwnedFd) -> io::Result<libc::stat> {
    // SAFETY: `st` is written by fstat before it is read.
    let mut st: libc::stat = unsafe { std::mem::zeroed() };
    // SAFETY: valid descriptor and out pointer.
    if unsafe { libc::fstat(fd.as_raw_fd(), &mut st) } != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(st)
}

pub fn is_dir(st: &libc::stat) -> bool {
    st.st_mode & libc::S_IFMT == libc::S_IFDIR
}

pub fn is_reg(st: &libc::stat) -> bool {
    st.st_mode & libc::S_IFMT == libc::S_IFREG
}

/// Every name in the directory `dir` except `.` and `..`, in readdir
/// order. Reads through its own descriptor, so `dir`'s offset is
/// untouched.
pub fn names(dir: &OwnedFd) -> io::Result<Vec<CString>> {
    let fd = openat2(dir, "", libc::O_RDONLY | libc::O_DIRECTORY, 0)?;
    // SAFETY: fdopendir takes ownership of the descriptor on success.
    let d = unsafe { libc::fdopendir(fd.as_raw_fd()) };
    if d.is_null() {
        return Err(io::Error::last_os_error());
    }
    std::mem::forget(fd);
    let mut out = Vec::new();
    let result = loop {
        // SAFETY: errno is thread-local; clearing it tells end of
        // directory (NULL, errno 0) from an error (NULL, errno set).
        unsafe { *libc::__errno_location() = 0 };
        // SAFETY: `d` is a valid DIR* until closedir below.
        let ent = unsafe { libc::readdir64(d) };
        if ent.is_null() {
            let e = io::Error::last_os_error();
            break if e.raw_os_error() == Some(0) {
                Ok(())
            } else {
                Err(e)
            };
        }
        // SAFETY: d_name is NUL-terminated within the entry readdir
        // returned, which stays valid until the next readdir.
        let name = unsafe { CStr::from_ptr((*ent).d_name.as_ptr()) };
        if name.to_bytes() != b"." && name.to_bytes() != b".." {
            out.push(name.to_owned());
        }
    };
    // SAFETY: `d` came from fdopendir and is closed once.
    unsafe { libc::closedir(d) };
    result.map(|()| out)
}

/// `mkdirat`: whether it made the directory (`false`: the name exists;
/// the caller opens it without following symlinks to check what it is).
pub fn mkdir_made_at(dir: &OwnedFd, name: &CStr, mode: libc::mode_t) -> io::Result<bool> {
    // SAFETY: valid dirfd and C string.
    if unsafe { libc::mkdirat(dir.as_raw_fd(), name.as_ptr(), mode) } != 0 {
        let e = io::Error::last_os_error();
        return if e.raw_os_error() == Some(libc::EEXIST) {
            Ok(false)
        } else {
            Err(e)
        };
    }
    Ok(true)
}

/// `mkdirat` that fails if the name exists.
pub fn mkdir_new_at(dir: &OwnedFd, name: &CStr, mode: libc::mode_t) -> io::Result<()> {
    // SAFETY: valid dirfd and C string.
    if unsafe { libc::mkdirat(dir.as_raw_fd(), name.as_ptr(), mode) } != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

pub fn unlink_at(dir: &OwnedFd, name: &CStr, flags: i32) -> io::Result<()> {
    // SAFETY: valid dirfd and C string.
    if unsafe { libc::unlinkat(dir.as_raw_fd(), name.as_ptr(), flags) } != 0 {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

/// `renameat`: moves the name, never follows a symlink at either end
/// (a symlink at `to` is replaced, not written through).
pub fn rename_at(from_dir: &OwnedFd, from: &CStr, to_dir: &OwnedFd, to: &CStr) -> io::Result<()> {
    // SAFETY: valid dirfds and C strings.
    if unsafe {
        libc::renameat(
            from_dir.as_raw_fd(),
            from.as_ptr(),
            to_dir.as_raw_fd(),
            to.as_ptr(),
        )
    } != 0
    {
        return Err(io::Error::last_os_error());
    }
    Ok(())
}

/// Remove the directory `name` in `dir` and the files directly in it.
/// Only for directories this crate created flat (staging); anything
/// that is not a regular file inside is left, and the rmdir then fails.
pub fn remove_flat_dir(dir: &OwnedFd, name: &str) -> io::Result<()> {
    let sub = openat2(
        dir,
        name,
        libc::O_RDONLY | libc::O_DIRECTORY | libc::O_NOFOLLOW,
        libc::RESOLVE_BENEATH | libc::RESOLVE_NO_SYMLINKS,
    )?;
    for n in names(&sub)? {
        if let Ok(Some(st)) = stat_at(&sub, &n) {
            if !is_dir(&st) {
                let _ = unlink_at(&sub, &n, 0);
            }
        }
    }
    unlink_at(dir, &cstring(name.as_bytes())?, libc::AT_REMOVEDIR)
}
