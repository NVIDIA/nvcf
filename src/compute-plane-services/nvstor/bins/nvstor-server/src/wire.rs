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

//! Wire protocol server — handles nvstor-client native protocol.
//!
//! Framing: [u32 LE length][bincode payload]
//! Protocol: nvstor_client::protocol::Request → Response

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use tokio::io::{AsyncReadExt, AsyncWriteExt, BufReader, BufWriter};
use tokio::net::{TcpListener, TcpStream};
use tracing::{debug, info, warn};

use nvstor_client::protocol::{ObjectEntry, Request, Response};
use nvstor_storage::manager::ChunkManager;
use nvstor_storage::pm::PmStore;
use nvstor_storage::SyncMode;

/// Maximum payload size (256 MB).
const MAX_PAYLOAD: u32 = 256 * 1024 * 1024;

/// Wire protocol server.
pub struct WireServer {
    mgr: Arc<ChunkManager>,
    pm: Arc<dyn PmStore>,
    max_connections: usize,
    request_timeout: Duration,
    idle_timeout: Duration,
    active: Arc<AtomicUsize>,
}

impl WireServer {
    pub fn new(
        mgr: Arc<ChunkManager>,
        pm: Arc<dyn PmStore>,
        max_connections: usize,
        request_timeout: Duration,
        idle_timeout: Duration,
    ) -> Self {
        Self {
            mgr,
            pm,
            max_connections,
            request_timeout,
            idle_timeout,
            active: Arc::new(AtomicUsize::new(0)),
        }
    }

    pub async fn run_until_shutdown(
        &self,
        listener: TcpListener,
        mut shutdown: tokio::sync::watch::Receiver<bool>,
    ) {
        info!(
            addr = %listener.local_addr().unwrap_or_else(|_| "0.0.0.0:0".parse().expect("valid fallback addr")),
            "wire protocol server listening"
        );

        loop {
            let accepted = tokio::select! {
                biased;
                _ = shutdown.wait_for(|stopping| *stopping) => break,
                accepted = listener.accept() => accepted,
            };
            match accepted {
                Ok((stream, addr)) => {
                    let current = self.active.load(Ordering::Relaxed);
                    if current >= self.max_connections {
                        warn!(%addr, current, max = self.max_connections, "wire: rejected");
                        drop(stream);
                        continue;
                    }

                    self.active.fetch_add(1, Ordering::Relaxed);
                    debug!(%addr, "wire: new connection");

                    let mgr = self.mgr.clone();
                    let pm = self.pm.clone();
                    let active = self.active.clone();
                    let req_timeout = self.request_timeout;
                    let idle_timeout = self.idle_timeout;

                    let stop = shutdown.clone();
                    tokio::spawn(async move {
                        if let Err(e) =
                            handle_connection(stream, mgr, pm, req_timeout, idle_timeout, stop)
                                .await
                        {
                            debug!("wire: connection closed: {e}");
                        }
                        active.fetch_sub(1, Ordering::Relaxed);
                    });
                }
                Err(e) => {
                    warn!(error = %e, "wire: accept error");
                }
            }
        }
        while self.active.load(Ordering::Relaxed) > 0 {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    }
}

async fn handle_connection(
    stream: TcpStream,
    mgr: Arc<ChunkManager>,
    pm: Arc<dyn PmStore>,
    request_timeout: Duration,
    idle_timeout: Duration,
    mut shutdown: tokio::sync::watch::Receiver<bool>,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let (rd, wr) = stream.into_split();
    let mut reader = BufReader::new(rd);
    let mut writer = BufWriter::new(wr);

    loop {
        // Read header with idle timeout.
        let mut header = [0u8; 4];
        let read = tokio::select! {
            biased;
            _ = shutdown.wait_for(|stopping| *stopping) => return Ok(()),
            read = tokio::time::timeout(idle_timeout, reader.read_exact(&mut header)) => read,
        };
        match read {
            Ok(Ok(_)) => {}
            Ok(Err(e)) if e.kind() == std::io::ErrorKind::UnexpectedEof => return Ok(()),
            Ok(Err(e)) => return Err(e.into()),
            Err(_) => return Ok(()), // Idle timeout
        }

        let len = u32::from_le_bytes(header);
        if len > MAX_PAYLOAD {
            return Err("payload too large".into());
        }

        // Read payload with request timeout.
        let mut payload = vec![0u8; len as usize];
        if len > 0 {
            match tokio::time::timeout(request_timeout, reader.read_exact(&mut payload)).await {
                Ok(Ok(_)) => {}
                Ok(Err(e)) => return Err(e.into()),
                Err(_) => return Err("request timeout".into()),
            }
        }

        // Deserialize request.
        let request: Request = match bincode::deserialize(&payload) {
            Ok(r) => r,
            Err(e) => {
                let resp = Response::Error {
                    code: 400,
                    message: format!("bad request: {e}"),
                };
                send_response(&mut writer, &resp).await?;
                continue;
            }
        };

        // Dispatch.
        let response = dispatch(&mgr, &*pm, request);

        // Send response.
        tokio::time::timeout(request_timeout, send_response(&mut writer, &response)).await??;
    }
}

async fn send_response(
    writer: &mut BufWriter<tokio::net::tcp::OwnedWriteHalf>,
    resp: &Response,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let encoded = bincode::serialize(resp)?;
    writer
        .write_all(&(encoded.len() as u32).to_le_bytes())
        .await?;
    writer.write_all(&encoded).await?;
    writer.flush().await?;
    Ok(())
}

fn dispatch(mgr: &ChunkManager, pm: &dyn PmStore, req: Request) -> Response {
    match req {
        Request::Ping => Response::Pong,

        Request::PutObject {
            key,
            data,
            content_type,
        } => match mgr.object_write(&key, &data, Some(&content_type)) {
            Ok(manifest) => Response::PutResult {
                etag: manifest.etag.unwrap_or_default(),
            },
            Err(e) => error_response(e),
        },

        Request::GetObject { key } => {
            let manifest = match mgr.object_get_manifest(&key) {
                Ok(m) => m,
                Err(e) => return error_response(e),
            };
            match mgr.object_read(&key) {
                Ok(data) => Response::Object {
                    data,
                    size: manifest.size,
                    content_type: manifest.content_type.unwrap_or_default(),
                    etag: manifest.etag.unwrap_or_default(),
                },
                Err(e) => error_response(e),
            }
        }

        Request::DeleteObject { key } => match mgr.object_delete(&key) {
            Ok(()) => Response::Ok,
            Err(e) => error_response(e),
        },

        Request::HeadObject { key } => match mgr.object_get_manifest(&key) {
            Ok(m) => Response::Head {
                size: m.size,
                content_type: m.content_type.unwrap_or_default(),
                etag: m.etag.unwrap_or_default(),
            },
            Err(e) => error_response(e),
        },

        Request::ListObjects {
            prefix,
            max_keys,
            start_after,
        } => match mgr.object_list(&prefix, start_after.as_deref(), max_keys) {
            Ok((manifests, is_truncated)) => {
                let entries = manifests
                    .into_iter()
                    .map(|m| ObjectEntry {
                        key: m.object_id,
                        size: m.size,
                        etag: m.etag.unwrap_or_default(),
                    })
                    .collect();
                Response::ObjectList {
                    entries,
                    is_truncated,
                }
            }
            Err(e) => error_response(e),
        },

        Request::CopyObject { src, dst } => match mgr.object_copy(&src, &dst) {
            Ok(_) => Response::Ok,
            Err(e) => error_response(e),
        },

        Request::VolumeCreate {
            id,
            name: _,
            size,
            replication: _,
        } => {
            if size == 0 {
                return Response::Error {
                    code: 400,
                    message: "volume size must be positive".into(),
                };
            }
            if mgr.volume_get_info(id).is_ok() {
                return Response::Error {
                    code: 409,
                    message: "legacy volume exists; migrate before reusing its ID".into(),
                };
            }
            match pm.create(id, size) {
                Ok(()) => Response::Ok,
                Err(e) => io_error_response(e),
            }
        }

        Request::VolumeDelete { id } => match pm.delete(id) {
            Ok(()) => Response::Ok,
            Err(e) => io_error_response(e),
        },

        Request::VolumeWrite { id, offset, data } => {
            if mgr.volume_get_info(id).is_ok() && pm.size(id).is_err() {
                return Response::Error {
                    code: 409,
                    message: "legacy volume is read-only; copy to a new PM volume before writing"
                        .into(),
                };
            }
            match pm.write(id, offset, &data, SyncMode::Full) {
                Ok(()) => Response::Ok,
                Err(e) => io_error_response(e),
            }
        }

        Request::VolumeRead { id, offset, length } => {
            if length > MAX_PAYLOAD {
                return Response::Error {
                    code: 400,
                    message: "volume read exceeds response limit".into(),
                };
            }
            if pm.size(id).is_err() && mgr.volume_get_info(id).is_ok() {
                let mut data = vec![0u8; length as usize];
                return match mgr.volume_read(id, offset, &mut data, None) {
                    Ok(n) => {
                        data.truncate(n);
                        Response::VolumeData { data }
                    }
                    Err(error) => error_response(error),
                };
            }
            let mut buf = vec![0u8; length as usize];
            match pm.read(id, offset, &mut buf) {
                Ok(n) => {
                    buf.truncate(n);
                    Response::VolumeData { data: buf }
                }
                Err(e) => io_error_response(e),
            }
        }

        Request::VolumeInfo { id } => {
            if pm.size(id).is_err() {
                if let Ok(info) = mgr.volume_get_info(id) {
                    return Response::VolumeInfoResult {
                        size: info.size,
                        access_mode: info.access_mode as u8,
                        consistency: info.consistency as u8,
                    };
                }
            }
            match pm.size(id) {
                Ok(size) => Response::VolumeInfoResult {
                    size,
                    access_mode: 0,
                    consistency: 0,
                },
                Err(e) => io_error_response(e),
            }
        }
    }
}

fn error_response(e: nvstor_core::error::NvstorError) -> Response {
    let code = match &e {
        nvstor_core::error::NvstorError::NotFound => 404,
        nvstor_core::error::NvstorError::Exists => 409,
        nvstor_core::error::NvstorError::PermissionDenied
        | nvstor_core::error::NvstorError::AccessDenied => 403,
        nvstor_core::error::NvstorError::VolumeLocked => 423,
        nvstor_core::error::NvstorError::OutOfBounds => 416,
        nvstor_core::error::NvstorError::InvalidArgument(_) => 400,
        _ => 500,
    };
    Response::Error {
        code,
        message: e.to_string(),
    }
}

fn io_error_response(error: std::io::Error) -> Response {
    let code = match error.kind() {
        std::io::ErrorKind::NotFound => 404,
        std::io::ErrorKind::AlreadyExists => 409,
        std::io::ErrorKind::PermissionDenied => 403,
        std::io::ErrorKind::InvalidInput => 416,
        _ => 500,
    };
    Response::Error {
        code,
        message: error.to_string(),
    }
}
