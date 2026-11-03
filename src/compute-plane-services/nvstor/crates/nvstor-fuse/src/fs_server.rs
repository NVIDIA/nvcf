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

//! Filesystem RPC server — serves a directory tree over TCP for remote FUSE clients.
//!
//! Protocol: simple binary RPC over TCP.
//! All operations take a path string and return operation-specific data.
//!
//! This is the server counterpart to remote.rs (the FUSE client).
//! Run on the source node; FUSE clients on follower nodes connect to it.
//!
//! Ops:
//!   STAT       (0x09) → FileStat (type, size, mode, uid, gid, mtime, nlink, rdev)
//!   LIST_DIR   (0x0A) → Vec<DirEntry> (name, type)
//!   READLINK   (0x0B) → target path string
//!   READ_FILE  (0x0C) → file data bytes (offset, length)
//!   GETXATTR   (0x0D) → xattr value bytes
//!   LISTXATTR  (0x0E) → null-separated xattr names

use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{FileTypeExt, MetadataExt};
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::Duration;

use tracing::{debug, info, warn};

// Op codes.
const OP_STAT: u8 = 0x09;
const OP_LIST_DIR: u8 = 0x0A;
const OP_READLINK: u8 = 0x0B;
const OP_READ_FILE: u8 = 0x0C;
const OP_GETXATTR: u8 = 0x0D;
const OP_LISTXATTR: u8 = 0x0E;
const OP_LIST_DIR_STAT: u8 = 0x0F; // readdir + stat all children in one call

const STATUS_OK: u8 = 0x00;
const STATUS_NOT_FOUND: u8 = 0x01;
const STATUS_ERROR: u8 = 0x02;

// Limits — prevent resource exhaustion from malicious or buggy clients.
const MAX_PATH_LEN: usize = 4096;
const MAX_RESPONSE_BYTES: usize = 256 * 1024 * 1024; // 256 MB per response
const MAX_FILE_READ_BYTES: usize = 64 * 1024 * 1024; // 64 MB per read
const MAX_XATTR_VALUE_BYTES: usize = 65536;
const MAX_CONCURRENT_CLIENTS: u64 = 256;
const CLIENT_IDLE_TIMEOUT: Duration = Duration::from_secs(300);

/// Filesystem RPC server.
pub struct FsServer {
    root: PathBuf,
    shutdown: Arc<AtomicBool>,
    active_clients: Arc<AtomicU64>,
}

impl FsServer {
    pub fn new(root: PathBuf) -> Self {
        Self {
            root,
            shutdown: Arc::new(AtomicBool::new(false)),
            active_clients: Arc::new(AtomicU64::new(0)),
        }
    }

    /// Start the server. Blocks until shutdown.
    pub fn run(&self, port: u16) -> std::io::Result<()> {
        let listener = TcpListener::bind(format!("0.0.0.0:{port}"))?;
        listener.set_nonblocking(true)?;

        info!(port, root = %self.root.display(), "filesystem RPC server listening");

        loop {
            if self.shutdown.load(Ordering::Relaxed) {
                break;
            }
            match listener.accept() {
                Ok((stream, addr)) => {
                    let active = self.active_clients.load(Ordering::Relaxed);
                    if active >= MAX_CONCURRENT_CLIENTS {
                        warn!(%addr, active, "rejecting client: too many connections");
                        drop(stream);
                        continue;
                    }
                    debug!(%addr, "client connected");
                    let root = self.root.clone();
                    let counter = Arc::clone(&self.active_clients);
                    counter.fetch_add(1, Ordering::Relaxed);
                    thread::spawn(move || {
                        if let Err(e) = handle_client(stream, &root) {
                            debug!(error = %e, "client disconnected");
                        }
                        counter.fetch_sub(1, Ordering::Relaxed);
                    });
                }
                Err(ref e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(10));
                }
                Err(e) => {
                    warn!(error = %e, "accept failed");
                }
            }
        }
        Ok(())
    }

    /// Start in background thread.
    pub fn start_background(root: PathBuf, port: u16) -> Arc<Self> {
        let server = Arc::new(Self::new(root));
        let s = Arc::clone(&server);
        thread::spawn(move || {
            if let Err(e) = s.run(port) {
                warn!(error = %e, "fs server failed");
            }
        });
        server
    }

    pub fn shutdown(&self) {
        self.shutdown.store(true, Ordering::Relaxed);
    }

    pub fn active_clients(&self) -> u64 {
        self.active_clients.load(Ordering::Relaxed)
    }
}

fn handle_client(mut stream: TcpStream, root: &Path) -> Result<(), String> {
    stream
        .set_nonblocking(false)
        .map_err(|e| format!("blocking: {e}"))?;
    stream.set_read_timeout(Some(CLIENT_IDLE_TIMEOUT)).ok();
    stream.set_write_timeout(Some(Duration::from_secs(60))).ok();

    loop {
        // Read op.
        let mut op = [0u8; 1];
        match stream.read_exact(&mut op) {
            Ok(()) => {}
            Err(ref e) if e.kind() == std::io::ErrorKind::UnexpectedEof => return Ok(()),
            Err(ref e) if e.kind() == std::io::ErrorKind::TimedOut => return Ok(()),
            Err(ref e) if e.kind() == std::io::ErrorKind::WouldBlock => return Ok(()),
            Err(e) => return Err(format!("read op: {e}")),
        }

        // Read path.
        let mut plen = [0u8; 2];
        stream
            .read_exact(&mut plen)
            .map_err(|e| format!("read plen: {e}"))?;
        let path_len = u16::from_le_bytes(plen) as usize;
        if path_len > MAX_PATH_LEN {
            send_error(&mut stream, STATUS_ERROR, "path too long")?;
            continue;
        }
        let mut path_buf = vec![0u8; path_len];
        stream
            .read_exact(&mut path_buf)
            .map_err(|e| format!("read path: {e}"))?;
        let rel_path = String::from_utf8_lossy(&path_buf).to_string();

        // Resolve to real path (prevent directory traversal).
        let full_path = if rel_path == "/" {
            root.to_path_buf()
        } else {
            let clean = rel_path.trim_start_matches('/');
            let candidate = root.join(clean);
            // Canonicalize to resolve .. and symlinks, then verify it's under root.
            match candidate.canonicalize() {
                Ok(resolved) => {
                    let canon_root = root.canonicalize().unwrap_or_else(|_| root.to_path_buf());
                    if !resolved.starts_with(&canon_root) {
                        send_error(&mut stream, STATUS_ERROR, "path traversal blocked")?;
                        continue;
                    }
                    resolved
                }
                Err(_) => {
                    // Path doesn't exist — use the join result for not-found errors.
                    if !candidate.starts_with(root) {
                        send_error(&mut stream, STATUS_ERROR, "path traversal blocked")?;
                        continue;
                    }
                    candidate
                }
            }
        };

        match op[0] {
            OP_STAT => handle_stat(&mut stream, &full_path)?,
            OP_LIST_DIR => handle_list_dir(&mut stream, &full_path)?,
            OP_READLINK => handle_readlink(&mut stream, &full_path)?,
            OP_READ_FILE => handle_read_file(&mut stream, &full_path)?,
            OP_GETXATTR => handle_getxattr(&mut stream, &full_path)?,
            OP_LISTXATTR => handle_listxattr(&mut stream, &full_path)?,
            OP_LIST_DIR_STAT => handle_list_dir_stat(&mut stream, &full_path)?,
            _ => send_error(&mut stream, STATUS_ERROR, "unknown op")?,
        }
    }
}

fn handle_stat(stream: &mut TcpStream, path: &Path) -> Result<(), String> {
    match std::fs::symlink_metadata(path) {
        Ok(meta) => {
            let file_type = if meta.is_dir() {
                1u8
            } else if meta.file_type().is_symlink() {
                2
            } else if meta.file_type().is_block_device() {
                3
            } else if meta.file_type().is_char_device() {
                4
            } else if meta.file_type().is_fifo() {
                5
            } else if meta.file_type().is_socket() {
                6
            } else {
                0
            };

            let mut resp = Vec::with_capacity(41);
            resp.push(file_type);
            resp.extend_from_slice(&meta.size().to_le_bytes());
            resp.extend_from_slice(&meta.mode().to_le_bytes());
            resp.extend_from_slice(&meta.uid().to_le_bytes());
            resp.extend_from_slice(&meta.gid().to_le_bytes());
            resp.extend_from_slice(&meta.mtime().to_le_bytes());
            resp.extend_from_slice(&(meta.nlink() as u32).to_le_bytes());
            resp.extend_from_slice(&meta.rdev().to_le_bytes());
            // Optional generation suffix. Older clients consume the framed
            // response and continue to decode its original 41-byte prefix.
            resp.extend_from_slice(&meta.dev().to_le_bytes());
            resp.extend_from_slice(&meta.ino().to_le_bytes());
            resp.extend_from_slice(&meta.mtime_nsec().to_le_bytes());
            resp.extend_from_slice(&meta.ctime().to_le_bytes());
            resp.extend_from_slice(&meta.ctime_nsec().to_le_bytes());
            send_ok(stream, &resp)
        }
        Err(_) => send_error(stream, STATUS_NOT_FOUND, "not found"),
    }
}

fn handle_list_dir(stream: &mut TcpStream, path: &Path) -> Result<(), String> {
    let rd = match std::fs::read_dir(path) {
        Ok(r) => r,
        Err(_) => return send_error(stream, STATUS_NOT_FOUND, "not a directory"),
    };

    let mut resp = Vec::new();
    for entry in rd.filter_map(|e| e.ok()) {
        let name = entry.file_name();
        let name_bytes = name.as_encoded_bytes();
        if name_bytes.len() > u16::MAX as usize {
            continue; // Skip names that can't be encoded.
        }
        let file_type = match entry.file_type() {
            Ok(ft) if ft.is_dir() => 1u8,
            Ok(ft) if ft.is_symlink() => 2u8,
            _ => 0u8,
        };

        // Entry format: [name_len:u16] [name] [type:u8]
        resp.extend_from_slice(&(name_bytes.len() as u16).to_le_bytes());
        resp.extend_from_slice(name_bytes);
        resp.push(file_type);

        // Bound response size.
        if resp.len() > MAX_RESPONSE_BYTES {
            break;
        }
    }
    send_ok(stream, &resp)
}

fn handle_readlink(stream: &mut TcpStream, path: &Path) -> Result<(), String> {
    match std::fs::read_link(path) {
        Ok(target) => {
            let bytes = target.as_os_str().as_bytes();
            send_ok(stream, bytes)
        }
        Err(_) => send_error(stream, STATUS_NOT_FOUND, "not a symlink"),
    }
}

fn handle_read_file(stream: &mut TcpStream, path: &Path) -> Result<(), String> {
    // Read offset and length from request.
    let mut obuf = [0u8; 8];
    stream
        .read_exact(&mut obuf)
        .map_err(|e| format!("read offset: {e}"))?;
    let offset = u64::from_le_bytes(obuf) as usize;
    let mut lbuf = [0u8; 4];
    stream
        .read_exact(&mut lbuf)
        .map_err(|e| format!("read length: {e}"))?;
    let length = (u32::from_le_bytes(lbuf) as usize).min(MAX_FILE_READ_BYTES);

    let data = match std::fs::read(path) {
        Ok(d) => d,
        Err(_) => return send_error(stream, STATUS_NOT_FOUND, "file not found"),
    };

    let start = offset.min(data.len());
    let end = (start + length).min(data.len());
    send_ok(stream, &data[start..end])
}

/// Combined readdir + stat: returns entries with full metadata in one response.
/// Eliminates N+1 round-trips (1 readdir + N stat → 1 call).
/// Format per entry: [name_len:u16] [name] [type:u8] [size:u64] [mode:u32]
///                   [uid:u32] [gid:u32] [mtime:i64] [nlink:u32] [rdev:u64]
fn handle_list_dir_stat(stream: &mut TcpStream, path: &Path) -> Result<(), String> {
    let rd = match std::fs::read_dir(path) {
        Ok(r) => r,
        Err(_) => return send_error(stream, STATUS_NOT_FOUND, "not a directory"),
    };

    let mut resp = Vec::with_capacity(8192);
    for entry in rd.filter_map(|e| e.ok()) {
        let name = entry.file_name();
        let name_bytes = name.as_encoded_bytes();
        if name_bytes.len() > u16::MAX as usize {
            continue;
        }

        let child_path = path.join(&name);
        let meta = match std::fs::symlink_metadata(&child_path) {
            Ok(m) => m,
            Err(_) => continue,
        };

        let file_type = if meta.is_dir() {
            1u8
        } else if meta.file_type().is_symlink() {
            2
        } else {
            0
        };

        // Entry: [name_len:2] [name] [type:1] [size:8] [mode:4] [uid:4] [gid:4] [mtime:8] [nlink:4] [rdev:8]
        resp.extend_from_slice(&(name_bytes.len() as u16).to_le_bytes());
        resp.extend_from_slice(name_bytes);
        resp.push(file_type);
        resp.extend_from_slice(&meta.size().to_le_bytes());
        resp.extend_from_slice(&meta.mode().to_le_bytes());
        resp.extend_from_slice(&meta.uid().to_le_bytes());
        resp.extend_from_slice(&meta.gid().to_le_bytes());
        resp.extend_from_slice(&meta.mtime().to_le_bytes());
        resp.extend_from_slice(&(meta.nlink() as u32).to_le_bytes());
        resp.extend_from_slice(&meta.rdev().to_le_bytes());

        if resp.len() > MAX_RESPONSE_BYTES {
            break;
        }
    }
    send_ok(stream, &resp)
}

fn handle_getxattr(stream: &mut TcpStream, path: &Path) -> Result<(), String> {
    let mut nlen = [0u8; 2];
    stream
        .read_exact(&mut nlen)
        .map_err(|e| format!("read xattr name len: {e}"))?;
    let name_len = u16::from_le_bytes(nlen) as usize;
    if name_len > MAX_PATH_LEN {
        return send_error(stream, STATUS_ERROR, "xattr name too long");
    }
    let mut name_buf = vec![0u8; name_len];
    stream
        .read_exact(&mut name_buf)
        .map_err(|e| format!("read xattr name: {e}"))?;

    let c_path = match std::ffi::CString::new(path.to_string_lossy().as_bytes()) {
        Ok(c) => c,
        Err(_) => return send_error(stream, STATUS_ERROR, "invalid path"),
    };
    let c_name = match std::ffi::CString::new(name_buf) {
        Ok(c) => c,
        Err(_) => return send_error(stream, STATUS_ERROR, "invalid xattr name"),
    };

    let mut buf = vec![0u8; MAX_XATTR_VALUE_BYTES];
    let ret = unsafe {
        libc::lgetxattr(
            c_path.as_ptr(),
            c_name.as_ptr(),
            buf.as_mut_ptr() as *mut libc::c_void,
            buf.len(),
        )
    };
    if ret < 0 {
        send_error(stream, STATUS_NOT_FOUND, "xattr not found")
    } else {
        send_ok(stream, &buf[..ret as usize])
    }
}

fn handle_listxattr(stream: &mut TcpStream, path: &Path) -> Result<(), String> {
    let c_path = match std::ffi::CString::new(path.to_string_lossy().as_bytes()) {
        Ok(c) => c,
        Err(_) => return send_error(stream, STATUS_ERROR, "invalid path"),
    };

    let mut buf = vec![0u8; MAX_XATTR_VALUE_BYTES];
    let ret = unsafe {
        libc::llistxattr(
            c_path.as_ptr(),
            buf.as_mut_ptr() as *mut libc::c_char,
            buf.len(),
        )
    };
    if ret < 0 {
        send_ok(stream, &[]) // No xattrs.
    } else {
        send_ok(stream, &buf[..ret as usize])
    }
}

fn send_ok(stream: &mut TcpStream, data: &[u8]) -> Result<(), String> {
    stream
        .write_all(&[STATUS_OK])
        .map_err(|e| format!("write status: {e}"))?;
    stream
        .write_all(&(data.len() as u32).to_le_bytes())
        .map_err(|e| format!("write len: {e}"))?;
    stream
        .write_all(data)
        .map_err(|e| format!("write data: {e}"))?;
    Ok(())
}

fn send_error(stream: &mut TcpStream, status: u8, msg: &str) -> Result<(), String> {
    stream
        .write_all(&[status])
        .map_err(|e| format!("write status: {e}"))?;
    let bytes = msg.as_bytes();
    stream
        .write_all(&(bytes.len() as u32).to_le_bytes())
        .map_err(|e| format!("write len: {e}"))?;
    stream
        .write_all(bytes)
        .map_err(|e| format!("write msg: {e}"))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::sync::atomic::AtomicU16;

    // Use unique ports per test to avoid conflicts in parallel.
    static NEXT_PORT: AtomicU16 = AtomicU16::new(19900);
    fn next_port() -> u16 {
        NEXT_PORT.fetch_add(1, Ordering::Relaxed)
    }

    #[test]
    fn server_handles_stat() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::write(dir.path().join("test.txt"), b"hello").expect("write");

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let mut stream = TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");
        let path = b"/test.txt";
        stream.write_all(&[OP_STAT]).expect("write op");
        stream
            .write_all(&(path.len() as u16).to_le_bytes())
            .expect("write plen");
        stream.write_all(path).expect("write path");

        let mut status = [0u8; 1];
        stream.read_exact(&mut status).expect("read status");
        assert_eq!(status[0], STATUS_OK);

        let mut rlen = [0u8; 4];
        stream.read_exact(&mut rlen).expect("read rlen");
        let resp_len = u32::from_le_bytes(rlen) as usize;
        assert!(resp_len >= 41);
        let mut resp = vec![0u8; resp_len];
        stream.read_exact(&mut resp).expect("read resp");

        assert_eq!(resp[0], 0); // Regular file.
        let size = u64::from_le_bytes(resp[1..9].try_into().expect("bounds"));
        assert_eq!(size, 5);

        server.shutdown();
    }

    #[test]
    fn server_handles_list_dir_with_symlinks() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::write(dir.path().join("a.txt"), b"aaa").expect("write");
        fs::create_dir(dir.path().join("sub")).expect("mkdir");
        std::os::unix::fs::symlink("/tmp", dir.path().join("link")).expect("symlink");

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let mut stream = TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");
        let path = b"/";
        stream.write_all(&[OP_LIST_DIR]).expect("write op");
        stream
            .write_all(&(path.len() as u16).to_le_bytes())
            .expect("write plen");
        stream.write_all(path).expect("write path");

        let mut status = [0u8; 1];
        stream.read_exact(&mut status).expect("read status");
        assert_eq!(status[0], STATUS_OK);

        let mut rlen = [0u8; 4];
        stream.read_exact(&mut rlen).expect("read rlen");
        let resp_len = u32::from_le_bytes(rlen) as usize;
        let mut resp = vec![0u8; resp_len];
        stream.read_exact(&mut resp).expect("read resp");

        let mut names = Vec::new();
        let mut types = Vec::new();
        let mut pos = 0;
        while pos + 2 < resp.len() {
            let nlen = u16::from_le_bytes(resp[pos..pos + 2].try_into().expect("bounds")) as usize;
            pos += 2;
            if pos + nlen + 1 > resp.len() {
                break;
            }
            let name = String::from_utf8_lossy(&resp[pos..pos + nlen]).to_string();
            pos += nlen;
            let ftype = resp[pos];
            pos += 1;
            names.push(name.clone());
            types.push((name, ftype));
        }

        assert!(names.contains(&"a.txt".to_string()));
        assert!(names.contains(&"sub".to_string()));
        assert!(names.contains(&"link".to_string()));

        // Verify types.
        let link_type = types.iter().find(|(n, _)| n == "link").map(|(_, t)| *t);
        assert_eq!(link_type, Some(2u8), "symlink should have type 2");
        let sub_type = types.iter().find(|(n, _)| n == "sub").map(|(_, t)| *t);
        assert_eq!(sub_type, Some(1u8), "directory should have type 1");
        let file_type = types.iter().find(|(n, _)| n == "a.txt").map(|(_, t)| *t);
        assert_eq!(file_type, Some(0u8), "regular file should have type 0");

        server.shutdown();
    }

    #[test]
    fn server_handles_read_file() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::write(dir.path().join("data.bin"), b"file content here").expect("write");

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let mut stream = TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");
        let path = b"/data.bin";
        stream.write_all(&[OP_READ_FILE]).expect("write op");
        stream
            .write_all(&(path.len() as u16).to_le_bytes())
            .expect("write plen");
        stream.write_all(path).expect("write path");
        stream.write_all(&0u64.to_le_bytes()).expect("write offset");
        stream
            .write_all(&1024u32.to_le_bytes())
            .expect("write length");

        let mut status = [0u8; 1];
        stream.read_exact(&mut status).expect("read status");
        assert_eq!(status[0], STATUS_OK);

        let mut rlen = [0u8; 4];
        stream.read_exact(&mut rlen).expect("read rlen");
        let resp_len = u32::from_le_bytes(rlen) as usize;
        let mut resp = vec![0u8; resp_len];
        stream.read_exact(&mut resp).expect("read resp");

        assert_eq!(&resp, b"file content here");
        server.shutdown();
    }

    #[test]
    fn server_handles_readlink() {
        let dir = tempfile::tempdir().expect("tmpdir");
        std::os::unix::fs::symlink("/usr/lib/libcuda.so", dir.path().join("link"))
            .expect("symlink");

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let mut stream = TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");
        let path = b"/link";
        stream.write_all(&[OP_READLINK]).expect("write op");
        stream
            .write_all(&(path.len() as u16).to_le_bytes())
            .expect("write plen");
        stream.write_all(path).expect("write path");

        let mut status = [0u8; 1];
        stream.read_exact(&mut status).expect("read status");
        assert_eq!(status[0], STATUS_OK);

        let mut rlen = [0u8; 4];
        stream.read_exact(&mut rlen).expect("read rlen");
        let resp_len = u32::from_le_bytes(rlen) as usize;
        let mut resp = vec![0u8; resp_len];
        stream.read_exact(&mut resp).expect("read resp");

        assert_eq!(String::from_utf8_lossy(&resp), "/usr/lib/libcuda.so");
        server.shutdown();
    }

    #[test]
    fn server_handles_nested_directories() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::create_dir_all(dir.path().join("a/b/c")).expect("mkdir");
        fs::write(dir.path().join("a/b/c/deep.txt"), b"deep content").expect("write");

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let mut stream = TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");

        // LIST_DIR on nested path.
        let path = b"/a/b/c";
        stream.write_all(&[OP_LIST_DIR]).expect("write op");
        stream
            .write_all(&(path.len() as u16).to_le_bytes())
            .expect("write plen");
        stream.write_all(path).expect("write path");

        let mut status = [0u8; 1];
        stream.read_exact(&mut status).expect("read status");
        assert_eq!(status[0], STATUS_OK);

        let mut rlen = [0u8; 4];
        stream.read_exact(&mut rlen).expect("read rlen");
        let resp_len = u32::from_le_bytes(rlen) as usize;
        let mut resp = vec![0u8; resp_len];
        stream.read_exact(&mut resp).expect("read resp");
        assert!(resp_len > 0, "nested dir should have at least one entry");

        // READ_FILE on nested path (same connection — tests connection reuse).
        let path2 = b"/a/b/c/deep.txt";
        stream.write_all(&[OP_READ_FILE]).expect("write op2");
        stream
            .write_all(&(path2.len() as u16).to_le_bytes())
            .expect("write plen2");
        stream.write_all(path2).expect("write path2");
        stream
            .write_all(&0u64.to_le_bytes())
            .expect("write offset2");
        stream
            .write_all(&4096u32.to_le_bytes())
            .expect("write length2");

        stream.read_exact(&mut status).expect("read status2");
        assert_eq!(status[0], STATUS_OK);
        stream.read_exact(&mut rlen).expect("read rlen2");
        let resp_len2 = u32::from_le_bytes(rlen) as usize;
        let mut resp2 = vec![0u8; resp_len2];
        stream.read_exact(&mut resp2).expect("read resp2");
        assert_eq!(&resp2, b"deep content");

        server.shutdown();
    }

    #[test]
    fn server_handles_partial_read() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::write(dir.path().join("big.txt"), b"0123456789abcdef").expect("write");

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let mut stream = TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");
        let path = b"/big.txt";
        stream.write_all(&[OP_READ_FILE]).expect("write op");
        stream
            .write_all(&(path.len() as u16).to_le_bytes())
            .expect("write plen");
        stream.write_all(path).expect("write path");
        stream.write_all(&5u64.to_le_bytes()).expect("write offset"); // offset=5
        stream.write_all(&4u32.to_le_bytes()).expect("write length"); // length=4

        let mut status = [0u8; 1];
        stream.read_exact(&mut status).expect("read status");
        assert_eq!(status[0], STATUS_OK);

        let mut rlen = [0u8; 4];
        stream.read_exact(&mut rlen).expect("read rlen");
        let resp_len = u32::from_le_bytes(rlen) as usize;
        let mut resp = vec![0u8; resp_len];
        stream.read_exact(&mut resp).expect("read resp");
        assert_eq!(&resp, b"5678", "partial read at offset 5, length 4");

        server.shutdown();
    }

    #[test]
    fn server_handles_not_found() {
        let dir = tempfile::tempdir().expect("tmpdir");

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let mut stream = TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");
        let path = b"/nonexistent.txt";
        stream.write_all(&[OP_STAT]).expect("write op");
        stream
            .write_all(&(path.len() as u16).to_le_bytes())
            .expect("write plen");
        stream.write_all(path).expect("write path");

        let mut status = [0u8; 1];
        stream.read_exact(&mut status).expect("read status");
        assert_eq!(status[0], STATUS_NOT_FOUND);

        // Consume the error response body.
        let mut rlen = [0u8; 4];
        stream.read_exact(&mut rlen).expect("read rlen");
        let resp_len = u32::from_le_bytes(rlen) as usize;
        let mut resp = vec![0u8; resp_len];
        if resp_len > 0 {
            stream.read_exact(&mut resp).expect("read resp");
        }

        // Connection should still be usable after not-found.
        let path2 = b"/also-missing";
        stream.write_all(&[OP_READ_FILE]).expect("write op2");
        stream
            .write_all(&(path2.len() as u16).to_le_bytes())
            .expect("write plen2");
        stream.write_all(path2).expect("write path2");
        stream
            .write_all(&0u64.to_le_bytes())
            .expect("write offset2");
        stream
            .write_all(&1024u32.to_le_bytes())
            .expect("write length2");

        stream.read_exact(&mut status).expect("read status2");
        assert_eq!(status[0], STATUS_NOT_FOUND);

        // Consume second error body.
        stream.read_exact(&mut rlen).expect("read rlen2");
        let resp_len2 = u32::from_le_bytes(rlen) as usize;
        if resp_len2 > 0 {
            let mut resp2 = vec![0u8; resp_len2];
            stream.read_exact(&mut resp2).expect("read resp2");
        }

        server.shutdown();
    }

    #[test]
    fn server_concurrent_clients() {
        let dir = tempfile::tempdir().expect("tmpdir");
        for i in 0..10 {
            fs::write(
                dir.path().join(format!("file{i}.txt")),
                format!("content{i}").as_bytes(),
            )
            .expect("write");
        }

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let handles: Vec<_> = (0..10)
            .map(|i| {
                thread::spawn(move || {
                    let mut stream =
                        TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");
                    let path = format!("/file{i}.txt");
                    let path_bytes = path.as_bytes();
                    stream.write_all(&[OP_READ_FILE]).expect("write op");
                    stream
                        .write_all(&(path_bytes.len() as u16).to_le_bytes())
                        .expect("write plen");
                    stream.write_all(path_bytes).expect("write path");
                    stream.write_all(&0u64.to_le_bytes()).expect("write offset");
                    stream
                        .write_all(&4096u32.to_le_bytes())
                        .expect("write length");

                    let mut status = [0u8; 1];
                    stream.read_exact(&mut status).expect("read status");
                    assert_eq!(status[0], STATUS_OK);

                    let mut rlen = [0u8; 4];
                    stream.read_exact(&mut rlen).expect("read rlen");
                    let resp_len = u32::from_le_bytes(rlen) as usize;
                    let mut resp = vec![0u8; resp_len];
                    stream.read_exact(&mut resp).expect("read resp");
                    assert_eq!(resp, format!("content{i}").as_bytes());
                })
            })
            .collect();

        for h in handles {
            h.join().expect("thread panicked");
        }

        server.shutdown();
    }

    #[test]
    fn server_blocks_path_traversal() {
        let dir = tempfile::tempdir().expect("tmpdir");
        fs::write(dir.path().join("safe.txt"), b"ok").expect("write");

        let port = next_port();
        let server = FsServer::start_background(dir.path().to_path_buf(), port);
        std::thread::sleep(Duration::from_millis(100));

        let mut stream = TcpStream::connect(format!("127.0.0.1:{port}")).expect("connect");
        let path = b"/../../../etc/passwd";
        stream.write_all(&[OP_STAT]).expect("write op");
        stream
            .write_all(&(path.len() as u16).to_le_bytes())
            .expect("write plen");
        stream.write_all(path).expect("write path");

        let mut status = [0u8; 1];
        stream.read_exact(&mut status).expect("read status");
        assert_ne!(status[0], STATUS_OK, "path traversal should be rejected");
        server.shutdown();
    }
}
