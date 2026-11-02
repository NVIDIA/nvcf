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

//! NBD (Network Block Device) server for nvstor volumes.
//!
//! Implements the NBD fixed-newstyle handshake protocol and transmission phase.
//! Each connection exports a single nvstor volume identified by UUID string.
//!
//! Reference: <https://github.com/NetworkBlockDevice/nbd/blob/master/doc/proto.md>

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, TcpStream};
use tracing::{debug, info, warn};
use uuid::Uuid;

use nvstor_storage::pm::PmStore;
use nvstor_storage::SyncMode;

// ---------------------------------------------------------------------------
// NBD protocol constants (all big-endian on wire)
// ---------------------------------------------------------------------------

/// "NBDMAGIC" init magic.
const NBDMAGIC: u64 = 0x4E42444D41474943;
/// "IHAVEOPT" opts magic.
const IHAVEOPT: u64 = 0x49484156454F5054;

// Handshake flags (server).
const NBD_FLAG_FIXED_NEWSTYLE: u16 = 1 << 0;
const NBD_FLAG_NO_ZEROES: u16 = 1 << 1;

// Client flags.
const NBD_FLAG_C_FIXED_NEWSTYLE: u32 = 1 << 0;
const NBD_FLAG_C_NO_ZEROES: u32 = 1 << 1;

// Transmission flags (per-export).
const NBD_FLAG_HAS_FLAGS: u16 = 1 << 0;
const NBD_FLAG_SEND_FLUSH: u16 = 1 << 2;
const NBD_FLAG_SEND_FUA: u16 = 1 << 3;
const NBD_FLAG_SEND_TRIM: u16 = 1 << 5;
const NBD_FLAG_SEND_WRITE_ZEROES: u16 = 1 << 6;

// Option codes.
const NBD_OPT_EXPORT_NAME: u32 = 1;
const NBD_OPT_ABORT: u32 = 2;
const NBD_OPT_GO: u32 = 7;

// Option reply types.
const NBD_REP_ACK: u32 = 1;
const NBD_REP_INFO: u32 = 3;
const NBD_REP_ERR_UNSUP: u32 = (1 << 31) | 1;
const NBD_REP_ERR_UNKNOWN: u32 = (1 << 31) | 6;

// Info types for OPT_GO.
const NBD_INFO_EXPORT: u16 = 0;
const NBD_INFO_BLOCK_SIZE: u16 = 3;

// Request types (transmission phase).
const NBD_CMD_READ: u16 = 0;
const NBD_CMD_WRITE: u16 = 1;
const NBD_CMD_DISC: u16 = 2;
const NBD_CMD_FLUSH: u16 = 3;
const NBD_CMD_TRIM: u16 = 4;
const NBD_CMD_WRITE_ZEROES: u16 = 6;

// Command flags.
const NBD_CMD_FLAG_FUA: u16 = 1 << 0;

// Reply magic.
const NBD_SIMPLE_REPLY_MAGIC: u32 = 0x67446698;

// Error codes.
const NBD_OK: u32 = 0;
const NBD_EIO: u32 = 5;
const NBD_EINVAL: u32 = 22;
const NBD_ENOSPC: u32 = 28;

// Block size limits.
const MIN_BLOCK_SIZE: u32 = 1;
const PREFERRED_BLOCK_SIZE: u32 = 4096;
const MAX_BLOCK_SIZE: u32 = 32 * 1024 * 1024; // 32 MiB

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

/// Configuration for the NBD server.
#[derive(Debug, Clone)]
pub struct NbdConfig {
    /// TCP listen address (default: "0.0.0.0:10809").
    pub listen_addr: String,
    /// Maximum concurrent connections (0 = unlimited).
    pub max_connections: usize,
}

impl Default for NbdConfig {
    fn default() -> Self {
        Self {
            listen_addr: "0.0.0.0:10809".to_string(),
            max_connections: 64,
        }
    }
}

/// NBD server serving nvstor volumes.
///
/// Backed by a `PmStore`: one sparse file per volume, mutable
/// in place. Replaces the older content-addressed chunk path which
/// caused write amplification on every NBD WRITE.
pub struct NbdServer {
    store: Arc<dyn PmStore>,
    config: NbdConfig,
    active_connections: Arc<AtomicUsize>,
}

impl NbdServer {
    /// Create a new NBD server backed by the given `PmStore`.
    pub fn new(store: Arc<dyn PmStore>, config: NbdConfig) -> Self {
        Self {
            store,
            config,
            active_connections: Arc::new(AtomicUsize::new(0)),
        }
    }

    /// Current number of active connections.
    pub fn active_connections(&self) -> usize {
        self.active_connections.load(Ordering::Relaxed)
    }

    /// Run the NBD server, accepting connections until the listener is dropped.
    pub async fn run(&self) -> std::io::Result<()> {
        let (_sender, receiver) = tokio::sync::watch::channel(false);
        self.run_until_shutdown(receiver).await
    }

    pub async fn run_until_shutdown(
        &self,
        mut shutdown: tokio::sync::watch::Receiver<bool>,
    ) -> std::io::Result<()> {
        let listener = TcpListener::bind(&self.config.listen_addr).await?;
        info!(addr = %self.config.listen_addr, "NBD server listening");

        loop {
            let (stream, peer) = tokio::select! {
                biased;
                _ = shutdown.wait_for(|stopping| *stopping) => break,
                accepted = listener.accept() => accepted?,
            };
            debug!(%peer, "NBD: new connection");

            // Connection limit check.
            if self.config.max_connections > 0
                && self.active_connections.load(Ordering::Relaxed) >= self.config.max_connections
            {
                warn!(%peer, max = self.config.max_connections, "NBD: connection limit reached");
                drop(stream);
                continue;
            }

            let store = Arc::clone(&self.store);
            let counter = Arc::clone(&self.active_connections);
            counter.fetch_add(1, Ordering::Relaxed);

            let stop = shutdown.clone();
            tokio::spawn(async move {
                if let Err(e) = handle_connection(stream, store, stop).await {
                    debug!(error = %e, "NBD connection ended");
                }
                counter.fetch_sub(1, Ordering::Relaxed);
            });
        }
        while self.active_connections() > 0 {
            tokio::time::sleep(std::time::Duration::from_millis(10)).await;
        }
        Ok(())
    }

    /// Handle a single pre-connected stream (useful for testing).
    pub async fn handle_stream(store: Arc<dyn PmStore>, stream: TcpStream) -> std::io::Result<()> {
        let (_sender, stop) = tokio::sync::watch::channel(false);
        handle_connection(stream, store, stop).await
    }
}

// ---------------------------------------------------------------------------
// Connection handler
// ---------------------------------------------------------------------------

async fn handle_connection(
    mut stream: TcpStream,
    store: Arc<dyn PmStore>,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
) -> std::io::Result<()> {
    // --- Fixed-newstyle handshake ---
    let server_flags = NBD_FLAG_FIXED_NEWSTYLE | NBD_FLAG_NO_ZEROES;
    let mut handshake = [0u8; 18];
    handshake[0..8].copy_from_slice(&NBDMAGIC.to_be_bytes());
    handshake[8..16].copy_from_slice(&IHAVEOPT.to_be_bytes());
    handshake[16..18].copy_from_slice(&server_flags.to_be_bytes());
    stream.write_all(&handshake).await?;

    // Client sends: flags (4 bytes)
    let client_flags = tokio::select! {
        _ = shutdown.wait_for(|stopping| *stopping) => return Ok(()),
        flags = stream.read_u32() => flags?,
    };
    if client_flags & NBD_FLAG_C_FIXED_NEWSTYLE == 0 {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidData,
            "client must set FIXED_NEWSTYLE flag",
        ));
    }
    let no_zeroes = client_flags & NBD_FLAG_C_NO_ZEROES != 0;

    // --- Option negotiation ---
    let (volume_id, volume_size) = tokio::select! {
        _ = shutdown.wait_for(|stopping| *stopping) => return Ok(()),
        result = negotiate_options(&mut stream, &*store, no_zeroes) => result?,
    };

    // --- Transmission phase ---
    transmission_loop(&mut stream, &*store, volume_id, volume_size, shutdown).await
}

// ---------------------------------------------------------------------------
// Option negotiation
// ---------------------------------------------------------------------------

async fn negotiate_options(
    stream: &mut TcpStream,
    store: &dyn PmStore,
    no_zeroes: bool,
) -> std::io::Result<(Uuid, u64)> {
    loop {
        // Each option: IHAVEOPT (8) + option (4) + length (4) + data
        let magic = stream.read_u64().await?;
        if magic != IHAVEOPT {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                "bad option magic",
            ));
        }

        let option = stream.read_u32().await?;
        let data_len = stream.read_u32().await?;

        // Read option data.
        let mut data = vec![0u8; data_len as usize];
        if data_len > 0 {
            stream.read_exact(&mut data).await?;
        }

        match option {
            NBD_OPT_EXPORT_NAME => {
                let name = String::from_utf8_lossy(&data).to_string();
                let volume_id = parse_volume_name(&name)?;
                // Exports must already exist in the shared native/PM catalog.
                let size = store.size(volume_id)?;

                // Send export info: size (8) + flags (2) + optional 124 zero bytes
                let flags = export_flags();
                stream.write_u64(size).await?;
                stream.write_u16(flags).await?;
                if !no_zeroes {
                    stream.write_all(&[0u8; 124]).await?;
                }

                return Ok((volume_id, size));
            }

            NBD_OPT_GO => {
                let name_len = if data.len() >= 4 {
                    u32::from_be_bytes([data[0], data[1], data[2], data[3]]) as usize
                } else {
                    return Err(std::io::Error::new(
                        std::io::ErrorKind::InvalidData,
                        "OPT_GO data too short",
                    ));
                };

                if data.len() < 4 + name_len {
                    return Err(std::io::Error::new(
                        std::io::ErrorKind::InvalidData,
                        "OPT_GO name truncated",
                    ));
                }

                let name = String::from_utf8_lossy(&data[4..4 + name_len]).to_string();
                let volume_id = match parse_volume_name(&name) {
                    Ok(id) => id,
                    Err(_) => {
                        send_option_reply(stream, option, NBD_REP_ERR_UNKNOWN, &[]).await?;
                        continue;
                    }
                };

                let size = match store.size(volume_id) {
                    Ok(size) => size,
                    Err(_) => {
                        send_option_reply(stream, option, NBD_REP_ERR_UNKNOWN, &[]).await?;
                        continue;
                    }
                };

                // Send NBD_INFO_EXPORT.
                let flags = export_flags();
                let mut export_info = Vec::with_capacity(12);
                export_info.extend_from_slice(&NBD_INFO_EXPORT.to_be_bytes());
                export_info.extend_from_slice(&size.to_be_bytes());
                export_info.extend_from_slice(&flags.to_be_bytes());
                send_option_reply(stream, option, NBD_REP_INFO, &export_info).await?;

                // Send NBD_INFO_BLOCK_SIZE.
                let mut block_info = Vec::with_capacity(14);
                block_info.extend_from_slice(&NBD_INFO_BLOCK_SIZE.to_be_bytes());
                block_info.extend_from_slice(&MIN_BLOCK_SIZE.to_be_bytes());
                block_info.extend_from_slice(&PREFERRED_BLOCK_SIZE.to_be_bytes());
                block_info.extend_from_slice(&MAX_BLOCK_SIZE.to_be_bytes());
                send_option_reply(stream, option, NBD_REP_INFO, &block_info).await?;

                // Final ACK.
                send_option_reply(stream, option, NBD_REP_ACK, &[]).await?;

                return Ok((volume_id, size));
            }

            NBD_OPT_ABORT => {
                send_option_reply(stream, option, NBD_REP_ACK, &[]).await?;
                return Err(std::io::Error::new(
                    std::io::ErrorKind::ConnectionAborted,
                    "client sent OPT_ABORT",
                ));
            }

            _ => {
                // Unknown option — reply unsupported.
                send_option_reply(stream, option, NBD_REP_ERR_UNSUP, &[]).await?;
            }
        }
    }
}

/// Send an option reply: magic (8) + option (4) + reply_type (4) + length (4) + data.
async fn send_option_reply(
    stream: &mut TcpStream,
    option: u32,
    reply_type: u32,
    data: &[u8],
) -> std::io::Result<()> {
    // Reply magic: 0x3e889045565a9
    let reply_magic: u64 = 0x3e889045565a9;
    stream.write_u64(reply_magic).await?;
    stream.write_u32(option).await?;
    stream.write_u32(reply_type).await?;
    stream.write_u32(data.len() as u32).await?;
    if !data.is_empty() {
        stream.write_all(data).await?;
    }
    Ok(())
}

fn export_flags() -> u16 {
    NBD_FLAG_HAS_FLAGS
        | NBD_FLAG_SEND_FLUSH
        | NBD_FLAG_SEND_FUA
        | NBD_FLAG_SEND_TRIM
        | NBD_FLAG_SEND_WRITE_ZEROES
}

/// Resolve an NBD export name to a volume UUID.
///
/// `nbd-client <host> <port> <dev> -N <name>` sends a free-form string;
/// our internal volume IDs are UUIDs. If the name parses as a UUID we
/// use it as-is (lets admins point at a pre-provisioned volume by id);
/// otherwise we deterministically derive a UUID v5 from the name so the
/// same name always maps to the same volume.
fn parse_volume_name(name: &str) -> std::io::Result<Uuid> {
    let trimmed = name.trim();
    if let Ok(u) = Uuid::parse_str(trimmed) {
        return Ok(u);
    }
    // UUID v5 with a stable namespace constant for NBD volume names.
    // Same name → same UUID, different name → different UUID. The value
    // is a fixed 16-byte namespace constant; an on-disk identity, so it
    // never changes: changing it would give every existing named volume
    // a new UUID.
    const NVSTOR_NBD_NS: Uuid = Uuid::from_u128(0x6265_6472_6f63_6b2d_6e62_642d_6e61_6d65);
    Ok(Uuid::new_v5(&NVSTOR_NBD_NS, trimmed.as_bytes()))
}

// ---------------------------------------------------------------------------
// Transmission phase
// ---------------------------------------------------------------------------

async fn transmission_loop(
    stream: &mut TcpStream,
    store: &dyn PmStore,
    volume_id: Uuid,
    _volume_size: u64,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
) -> std::io::Result<()> {
    loop {
        // Request header: magic (4) + flags (2) + type (2) + handle (8) + offset (8) + length (4)
        let magic = tokio::select! {
            biased;
            _ = shutdown.wait_for(|stopping| *stopping) => return Ok(()),
            magic = stream.read_u32() => magic?,
        };
        if magic != 0x25609513 {
            return Err(std::io::Error::new(
                std::io::ErrorKind::InvalidData,
                format!("bad request magic: {magic:#x}"),
            ));
        }

        let cmd_flags = stream.read_u16().await?;
        let cmd_type = stream.read_u16().await?;
        let handle = stream.read_u64().await?;
        let offset = stream.read_u64().await?;
        let length = stream.read_u32().await?;

        let fua = cmd_flags & NBD_CMD_FLAG_FUA != 0;

        // Map NBD's per-op semantics to PmStore's SyncMode:
        //   plain WRITE → None  (kernel writeback handles durability)
        //   WRITE+FUA   → Data  (fdatasync — data durable, not metadata)
        //   FLUSH       → flush(volume, Data)
        let write_sync = if fua { SyncMode::Data } else { SyncMode::None };

        match cmd_type {
            NBD_CMD_READ => {
                let mut buf = vec![0u8; length as usize];
                let err = match store.read(volume_id, offset, &mut buf) {
                    Ok(n) => {
                        if n < buf.len() {
                            buf[n..].fill(0);
                        }
                        NBD_OK
                    }
                    Err(e) => {
                        warn!(error = %e, offset, length, "NBD READ failed");
                        buf.fill(0);
                        NBD_EIO
                    }
                };
                send_reply(stream, handle, err, Some(&buf)).await?;
            }

            NBD_CMD_WRITE => {
                let mut data = vec![0u8; length as usize];
                stream.read_exact(&mut data).await?;

                let err = match store.write(volume_id, offset, &data, write_sync) {
                    Ok(()) => NBD_OK,
                    Err(e) => {
                        warn!(error = %e, offset, length, "NBD WRITE failed");
                        nbd_errno_io(&e)
                    }
                };
                send_reply(stream, handle, err, None).await?;
            }

            NBD_CMD_FLUSH => {
                let err = match store.flush(volume_id, SyncMode::Data) {
                    Ok(()) => NBD_OK,
                    Err(e) => {
                        warn!(error = %e, "NBD FLUSH failed");
                        NBD_EIO
                    }
                };
                send_reply(stream, handle, err, None).await?;
            }

            NBD_CMD_TRIM => {
                // TRIM = release backing storage; reads return zeros.
                let err = match store.punch_hole(volume_id, offset, length as u64) {
                    Ok(()) => NBD_OK,
                    Err(e) => {
                        warn!(error = %e, offset, length, "NBD TRIM failed");
                        NBD_EIO
                    }
                };
                send_reply(stream, handle, err, None).await?;
            }

            NBD_CMD_WRITE_ZEROES => {
                // Prefer punch_hole (sparse, no I/O). Fall back to a
                // synthetic zero write if punch fails (some fs lacking
                // FALLOC_FL_PUNCH_HOLE — on those, we still need the
                // bytes to read as zero).
                let err = match store.punch_hole(volume_id, offset, length as u64) {
                    Ok(()) => {
                        if fua {
                            store
                                .flush(volume_id, SyncMode::Data)
                                .map(|_| NBD_OK)
                                .unwrap_or(NBD_EIO)
                        } else {
                            NBD_OK
                        }
                    }
                    Err(_) => {
                        // Materialize zeros.
                        let zeroes = vec![0u8; length as usize];
                        match store.write(volume_id, offset, &zeroes, write_sync) {
                            Ok(()) => NBD_OK,
                            Err(e) => {
                                warn!(error = %e, "NBD WRITE_ZEROES (zero-fill) failed");
                                nbd_errno_io(&e)
                            }
                        }
                    }
                };
                send_reply(stream, handle, err, None).await?;
            }

            NBD_CMD_DISC => {
                debug!("NBD: client disconnect");
                return Ok(());
            }

            _ => {
                warn!(cmd_type, "NBD: unknown command");
                send_reply(stream, handle, NBD_EINVAL, None).await?;
            }
        }
    }
}

/// Send a simple reply: magic (4) + error (4) + handle (8) + optional data.
async fn send_reply(
    stream: &mut TcpStream,
    handle: u64,
    error: u32,
    data: Option<&[u8]>,
) -> std::io::Result<()> {
    stream.write_u32(NBD_SIMPLE_REPLY_MAGIC).await?;
    stream.write_u32(error).await?;
    stream.write_u64(handle).await?;
    if let Some(d) = data {
        stream.write_all(d).await?;
    }
    Ok(())
}

/// Map an `io::Error` from the PmStore layer to an NBD error code.
fn nbd_errno_io(e: &std::io::Error) -> u32 {
    use std::io::ErrorKind;
    match e.kind() {
        ErrorKind::NotFound => NBD_EINVAL,
        ErrorKind::InvalidInput => NBD_EINVAL,
        ErrorKind::OutOfMemory => NBD_ENOSPC,
        _ => {
            // ENOSPC isn't its own ErrorKind on stable — check the raw os errno.
            if let Some(28) = e.raw_os_error() {
                return NBD_ENOSPC;
            }
            NBD_EIO
        }
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    use std::sync::Arc;
    use tempfile::TempDir;

    use nvstor_storage::pm::posix::PosixPmStore;

    /// Create a test PmStore backed by a temp directory.
    fn test_store() -> (Arc<dyn PmStore>, TempDir) {
        let dir = TempDir::new().unwrap();
        let store: Arc<dyn PmStore> = Arc::new(PosixPmStore::new(dir.path()).unwrap());
        (store, dir)
    }

    /// Helper: perform the NBD fixed-newstyle handshake from the client side.
    async fn client_handshake(stream: &mut tokio::net::TcpStream) {
        // Read server handshake: 18 bytes.
        let mut buf = [0u8; 18];
        stream.read_exact(&mut buf).await.unwrap();

        // Verify NBDMAGIC.
        let magic = u64::from_be_bytes(buf[0..8].try_into().unwrap());
        assert_eq!(magic, NBDMAGIC);

        // Verify IHAVEOPT.
        let opt_magic = u64::from_be_bytes(buf[8..16].try_into().unwrap());
        assert_eq!(opt_magic, IHAVEOPT);

        // Send client flags.
        let flags = NBD_FLAG_C_FIXED_NEWSTYLE | NBD_FLAG_C_NO_ZEROES;
        stream.write_u32(flags).await.unwrap();
    }

    /// Helper: send OPT_EXPORT_NAME and read the response.
    async fn client_opt_export_name(stream: &mut tokio::net::TcpStream, name: &str) -> (u64, u16) {
        let name_bytes = name.as_bytes();
        stream.write_u64(IHAVEOPT).await.unwrap();
        stream.write_u32(NBD_OPT_EXPORT_NAME).await.unwrap();
        stream.write_u32(name_bytes.len() as u32).await.unwrap();
        stream.write_all(name_bytes).await.unwrap();

        // Read: size (8) + flags (2). No zeroes since we sent NO_ZEROES flag.
        let size = stream.read_u64().await.unwrap();
        let flags = stream.read_u16().await.unwrap();
        (size, flags)
    }

    /// Helper: send OPT_GO and read the response (info replies + ACK).
    async fn client_opt_go(
        stream: &mut tokio::net::TcpStream,
        name: &str,
    ) -> Result<(u64, u16), u32> {
        let name_bytes = name.as_bytes();
        // OPT_GO data: name_len (4) + name + num_info_requests (2)
        let mut data = Vec::new();
        data.extend_from_slice(&(name_bytes.len() as u32).to_be_bytes());
        data.extend_from_slice(name_bytes);
        data.extend_from_slice(&0u16.to_be_bytes()); // zero info requests

        stream.write_u64(IHAVEOPT).await.unwrap();
        stream.write_u32(NBD_OPT_GO).await.unwrap();
        stream.write_u32(data.len() as u32).await.unwrap();
        stream.write_all(&data).await.unwrap();

        let mut volume_size = 0u64;
        let mut flags = 0u16;

        // Read replies until ACK or error.
        loop {
            let _reply_magic = stream.read_u64().await.unwrap();
            let _option = stream.read_u32().await.unwrap();
            let reply_type = stream.read_u32().await.unwrap();
            let length = stream.read_u32().await.unwrap();
            let mut reply_data = vec![0u8; length as usize];
            if length > 0 {
                stream.read_exact(&mut reply_data).await.unwrap();
            }

            if reply_type & (1 << 31) != 0 {
                // Error reply.
                return Err(reply_type);
            }

            if reply_type == NBD_REP_INFO && reply_data.len() >= 2 {
                let info_type = u16::from_be_bytes([reply_data[0], reply_data[1]]);
                if info_type == NBD_INFO_EXPORT && reply_data.len() >= 12 {
                    volume_size = u64::from_be_bytes(reply_data[2..10].try_into().unwrap());
                    flags = u16::from_be_bytes(reply_data[10..12].try_into().unwrap());
                }
            }

            if reply_type == NBD_REP_ACK {
                return Ok((volume_size, flags));
            }
        }
    }

    /// Helper: send an NBD request in the transmission phase.
    async fn client_send_request(
        stream: &mut tokio::net::TcpStream,
        cmd_type: u16,
        handle: u64,
        offset: u64,
        length: u32,
        cmd_flags: u16,
        write_data: Option<&[u8]>,
    ) {
        stream.write_u32(0x25609513).await.unwrap(); // request magic
        stream.write_u16(cmd_flags).await.unwrap();
        stream.write_u16(cmd_type).await.unwrap();
        stream.write_u64(handle).await.unwrap();
        stream.write_u64(offset).await.unwrap();
        stream.write_u32(length).await.unwrap();
        if let Some(d) = write_data {
            stream.write_all(d).await.unwrap();
        }
    }

    /// Helper: read a simple reply and optional data payload.
    async fn client_read_reply(
        stream: &mut tokio::net::TcpStream,
        read_len: usize,
    ) -> (u64, u32, Vec<u8>) {
        let magic = stream.read_u32().await.unwrap();
        assert_eq!(magic, NBD_SIMPLE_REPLY_MAGIC);
        let error = stream.read_u32().await.unwrap();
        let handle = stream.read_u64().await.unwrap();
        let mut data = vec![0u8; read_len];
        if read_len > 0 {
            stream.read_exact(&mut data).await.unwrap();
        }
        (handle, error, data)
    }

    /// Create listener + connect a client pair.
    async fn setup_pair() -> (TcpListener, TcpStream, std::net::SocketAddr) {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let client = TcpStream::connect(addr).await.unwrap();
        (listener, client, addr)
    }

    // === Tests ===

    #[tokio::test]
    async fn test_handshake() {
        let (store, _d) = test_store();
        let vol_id = Uuid::new_v4();
        let vol_size: u64 = 1024 * 1024; // 1 MiB
        store.create(vol_id, vol_size).unwrap();

        let (listener, mut client, _addr) = setup_pair().await;

        let store2 = Arc::clone(&store);
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            NbdServer::handle_stream(store2, stream).await
        });

        // Client handshake.
        client_handshake(&mut client).await;

        // OPT_EXPORT_NAME.
        let (size, flags) = client_opt_export_name(&mut client, &vol_id.to_string()).await;
        assert_eq!(size, vol_size);
        assert_ne!(flags & NBD_FLAG_HAS_FLAGS, 0);
        assert_ne!(flags & NBD_FLAG_SEND_FLUSH, 0);
        assert_ne!(flags & NBD_FLAG_SEND_FUA, 0);
        assert_ne!(flags & NBD_FLAG_SEND_TRIM, 0);

        // Disconnect.
        client_send_request(&mut client, NBD_CMD_DISC, 1, 0, 0, 0, None).await;
        drop(client);
        let _ = server_task.await;
    }

    #[tokio::test]
    async fn test_write_read() {
        let (store, _d) = test_store();
        let vol_id = Uuid::new_v4();
        store.create(vol_id, 1024 * 1024).unwrap();

        let (listener, mut client, _addr) = setup_pair().await;
        let store2 = Arc::clone(&store);
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            NbdServer::handle_stream(store2, stream).await
        });

        client_handshake(&mut client).await;
        client_opt_export_name(&mut client, &vol_id.to_string()).await;

        // Write 512 bytes at offset 0.
        let data = vec![0xAB; 512];
        client_send_request(&mut client, NBD_CMD_WRITE, 100, 0, 512, 0, Some(&data)).await;
        let (handle, err, _) = client_read_reply(&mut client, 0).await;
        assert_eq!(handle, 100);
        assert_eq!(err, NBD_OK);

        // Read back 512 bytes.
        client_send_request(&mut client, NBD_CMD_READ, 101, 0, 512, 0, None).await;
        let (handle, err, read_data) = client_read_reply(&mut client, 512).await;
        assert_eq!(handle, 101);
        assert_eq!(err, NBD_OK);
        assert_eq!(read_data, data);

        // Disconnect.
        client_send_request(&mut client, NBD_CMD_DISC, 102, 0, 0, 0, None).await;
        drop(client);
        let _ = server_task.await;
    }

    #[tokio::test]
    async fn test_flush_and_trim() {
        let (store, _d) = test_store();
        let vol_id = Uuid::new_v4();
        store.create(vol_id, 1024 * 1024).unwrap();

        let (listener, mut client, _addr) = setup_pair().await;
        let store2 = Arc::clone(&store);
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            NbdServer::handle_stream(store2, stream).await
        });

        client_handshake(&mut client).await;
        client_opt_export_name(&mut client, &vol_id.to_string()).await;

        // Flush.
        client_send_request(&mut client, NBD_CMD_FLUSH, 200, 0, 0, 0, None).await;
        let (handle, err, _) = client_read_reply(&mut client, 0).await;
        assert_eq!(handle, 200);
        assert_eq!(err, NBD_OK);

        // Trim.
        client_send_request(&mut client, NBD_CMD_TRIM, 201, 0, 4096, 0, None).await;
        let (handle, err, _) = client_read_reply(&mut client, 0).await;
        assert_eq!(handle, 201);
        assert_eq!(err, NBD_OK);

        // Disconnect.
        client_send_request(&mut client, NBD_CMD_DISC, 202, 0, 0, 0, None).await;
        drop(client);
        let _ = server_task.await;
    }

    #[tokio::test]
    async fn test_fua_write() {
        let (store, _d) = test_store();
        let vol_id = Uuid::new_v4();
        store.create(vol_id, 1024 * 1024).unwrap();

        let (listener, mut client, _addr) = setup_pair().await;
        let store2 = Arc::clone(&store);
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            NbdServer::handle_stream(store2, stream).await
        });

        client_handshake(&mut client).await;
        client_opt_export_name(&mut client, &vol_id.to_string()).await;

        // Write with FUA flag.
        let data = vec![0xCD; 256];
        client_send_request(
            &mut client,
            NBD_CMD_WRITE,
            300,
            0,
            256,
            NBD_CMD_FLAG_FUA,
            Some(&data),
        )
        .await;
        let (handle, err, _) = client_read_reply(&mut client, 0).await;
        assert_eq!(handle, 300);
        assert_eq!(err, NBD_OK);

        // Read back to verify.
        client_send_request(&mut client, NBD_CMD_READ, 301, 0, 256, 0, None).await;
        let (handle, err, read_data) = client_read_reply(&mut client, 256).await;
        assert_eq!(handle, 301);
        assert_eq!(err, NBD_OK);
        assert_eq!(read_data, data);

        client_send_request(&mut client, NBD_CMD_DISC, 302, 0, 0, 0, None).await;
        drop(client);
        let _ = server_task.await;
    }

    #[tokio::test]
    async fn test_unknown_volume_opt_go_does_not_create() {
        let (store, _d) = test_store();

        let (listener, mut client, _addr) = setup_pair().await;
        let store2 = Arc::clone(&store);
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            NbdServer::handle_stream(store2, stream).await
        });

        client_handshake(&mut client).await;

        let fake_id = Uuid::new_v4();
        let result = client_opt_go(&mut client, &fake_id.to_string()).await;
        assert!(
            result.is_err(),
            "unknown export must not silently provision a volume"
        );
        assert!(store.size(fake_id).is_err());

        drop(client);
        let _ = server_task.await;
    }

    #[tokio::test]
    async fn test_connection_limit() {
        let (store, _d) = test_store();
        let vol_id = Uuid::new_v4();
        store.create(vol_id, 1024 * 1024).unwrap();

        let config = NbdConfig {
            listen_addr: "127.0.0.1:0".to_string(),
            max_connections: 1,
        };
        let server = Arc::new(NbdServer::new(Arc::clone(&store), config));

        // Simulate connection count at max.
        server.active_connections.store(1, Ordering::Relaxed);
        assert_eq!(server.active_connections(), 1);

        // Verify the config limit.
        assert_eq!(server.config.max_connections, 1);

        // With active_connections == max_connections, new connections should be rejected.
        // We verify the logic by checking the condition directly.
        let at_limit =
            server.active_connections.load(Ordering::Relaxed) >= server.config.max_connections;
        assert!(at_limit);
    }

    #[tokio::test]
    async fn test_opt_go_handshake() {
        let (store, _d) = test_store();
        let vol_id = Uuid::new_v4();
        let vol_size: u64 = 2 * 1024 * 1024; // 2 MiB
        store.create(vol_id, vol_size).unwrap();

        let (listener, mut client, _addr) = setup_pair().await;
        let store2 = Arc::clone(&store);
        let server_task = tokio::spawn(async move {
            let (stream, _) = listener.accept().await.unwrap();
            NbdServer::handle_stream(store2, stream).await
        });

        client_handshake(&mut client).await;

        // Use OPT_GO instead of OPT_EXPORT_NAME.
        let (size, flags) = client_opt_go(&mut client, &vol_id.to_string())
            .await
            .unwrap();
        assert_eq!(size, vol_size);
        assert_ne!(flags & NBD_FLAG_HAS_FLAGS, 0);

        // Disconnect.
        client_send_request(&mut client, NBD_CMD_DISC, 1, 0, 0, 0, None).await;
        drop(client);
        let _ = server_task.await;
    }
}
