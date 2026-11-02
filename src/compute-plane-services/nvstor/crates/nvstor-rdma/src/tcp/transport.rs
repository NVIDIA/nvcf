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

//! `TcpTransport` — TCP-backed connection manager for the s3-tcp
//! protocol. One `TcpStream` per peer; concurrent senders
//! serialize through a per-peer mutex on the write half. Future
//! work: multi-stream per peer for true bandwidth fan-out (nconnect
//! style); for v1 a single stream per peer + io_uring on the server
//! side is the simple starting point.

use std::collections::HashMap;
use std::io::{self, BufReader, BufWriter, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::os::unix::io::AsRawFd;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::Arc;
use std::thread;

use parking_lot::{Mutex, RwLock};
use tracing::{debug, info, warn};

use crate::tcp::proto::{read_frame, write_frame, Request, Response, MAX_CONTROL_FRAME};

/// Buffer sizes tuned for moving 100s of GB through this transport.
///
///   * `BUF_CAPACITY = 4 MiB`: BufReader/BufWriter capacity per peer.
///     Std `BufWriter::write_all` *bypasses* the buffer when the
///     request exceeds capacity, and `BufReader::read_exact` does
///     the same — so for a 16 MiB payload after a small control
///     frame, we get 1 small buffered write (control header flush)
///     followed by 1 large direct write of the payload. 4 MiB keeps
///     the typical small-frame batching fast without holding huge
///     RAM per peer.
///   * `SOCK_BUF = 8 MiB`: explicit SO_SNDBUF / SO_RCVBUF on the
///     TcpStream. Linux autotune caps default tcp_wmem/rmem around
///     4-6 MiB; pinning to 8 MiB ensures the kernel queues stay
///     deep enough to absorb a full BDP at multi-GB/s on
///     intra-DC latencies.
const BUF_CAPACITY: usize = 4 * 1024 * 1024;
const SOCK_BUF: i32 = 8 * 1024 * 1024;

/// Callback fired exactly once per peer that completes the TCP
/// handshake on the server side. Mirrors the shape used by
/// `RdmaTransport::set_on_peer_accepted` so the dispatcher
/// integration is symmetric.
pub type OnPeerAccepted = Arc<dyn Fn(u64) + Send + Sync>;

struct PeerConnection {
    /// Buffered reader half. Held under its own mutex so the
    /// dispatcher thread can read control frames + payload bytes
    /// in sequence without racing the writer half.
    reader: Mutex<BufReader<TcpStream>>,
    /// Buffered writer half. Concurrent senders serialize here.
    writer: Mutex<BufWriter<TcpStream>>,
    /// Atomic-transaction lock. Held by the client side for the
    /// duration of one logical request: (send_request, optional
    /// write_payload, recv_response, optional read_payload). The
    /// writer mutex alone isn't enough because PUT needs
    /// `send_request` and `write_payload` to land back-to-back on
    /// the wire — without this lock another worker could squeeze
    /// its header between A's header and A's payload, leaving the
    /// server reading garbage. Server side doesn't need this lock
    /// because the dispatcher is single-threaded per peer.
    /// `Arc` so the client can clone + hold the lock for the
    /// duration of a transaction without holding a borrow on the
    /// transport itself.
    txn: Arc<Mutex<()>>,
    addr: String,
}

pub struct TcpTransport {
    next_peer_id: AtomicU64,
    peers: RwLock<HashMap<u64, Arc<PeerConnection>>>,
    on_peer_accepted: RwLock<Option<OnPeerAccepted>>,
    shutdown: Arc<AtomicBool>,
    listener_handle: Mutex<Option<thread::JoinHandle<()>>>,
    /// Client credentials used to sign outgoing requests. `None` on a
    /// server-side transport, which only receives.
    credentials: RwLock<Option<crate::auth::Credentials>>,
}

impl TcpTransport {
    pub fn new() -> Arc<Self> {
        Arc::new(Self {
            next_peer_id: AtomicU64::new(1),
            credentials: RwLock::new(None),
            peers: RwLock::new(HashMap::new()),
            on_peer_accepted: RwLock::new(None),
            shutdown: Arc::new(AtomicBool::new(false)),
            listener_handle: Mutex::new(None),
        })
    }

    pub fn set_on_peer_accepted(&self, cb: OnPeerAccepted) {
        *self.on_peer_accepted.write() = Some(cb);
    }

    /// Connect to a remote `host:port`. Returns the peer_id used
    /// by all subsequent `send_request`/`recv_response` calls.
    pub fn connect(self: &Arc<Self>, addr: &str) -> io::Result<u64> {
        let stream = TcpStream::connect(addr)?;
        tune_socket(&stream)?;
        let reader_half = stream.try_clone()?;
        let writer_half = stream;
        let peer_id = self.next_peer_id.fetch_add(1, Ordering::Relaxed);
        let peer = Arc::new(PeerConnection {
            reader: Mutex::new(BufReader::with_capacity(BUF_CAPACITY, reader_half)),
            writer: Mutex::new(BufWriter::with_capacity(BUF_CAPACITY, writer_half)),
            txn: Arc::new(Mutex::new(())),
            addr: addr.to_string(),
        });
        self.peers.write().insert(peer_id, peer);
        info!(peer_id, addr, "s3-tcp connected");
        Ok(peer_id)
    }

    /// Start listening for incoming peers on `port`. The callback
    /// registered via `set_on_peer_accepted` fires for every
    /// successful accept, on the accept thread.
    pub fn listen(self: &Arc<Self>, port: u16) -> io::Result<std::net::SocketAddr> {
        let listener = TcpListener::bind(format!("0.0.0.0:{port}"))?;
        let local = listener.local_addr()?;
        listener.set_nonblocking(false)?;
        let transport = Arc::clone(self);
        let shutdown = Arc::clone(&self.shutdown);
        let handle = thread::Builder::new()
            .name("s3-tcp-accept".into())
            .spawn(move || {
                info!(addr = %local, "s3-tcp transport listening");
                for incoming in listener.incoming() {
                    if shutdown.load(Ordering::Relaxed) {
                        break;
                    }
                    match incoming {
                        Ok(stream) => {
                            let peer_addr = stream
                                .peer_addr()
                                .map(|a| a.to_string())
                                .unwrap_or_default();
                            if let Err(e) = tune_socket(&stream) {
                                warn!("tune_socket: {e}");
                            }
                            let reader_half = match stream.try_clone() {
                                Ok(s) => s,
                                Err(e) => {
                                    warn!("clone stream: {e}");
                                    continue;
                                }
                            };
                            let writer_half = stream;
                            let peer_id = transport.next_peer_id.fetch_add(1, Ordering::Relaxed);
                            let peer = Arc::new(PeerConnection {
                                reader: Mutex::new(BufReader::with_capacity(
                                    BUF_CAPACITY,
                                    reader_half,
                                )),
                                writer: Mutex::new(BufWriter::with_capacity(
                                    BUF_CAPACITY,
                                    writer_half,
                                )),
                                txn: Arc::new(Mutex::new(())),
                                addr: peer_addr.clone(),
                            });
                            transport.peers.write().insert(peer_id, peer);
                            info!(peer_id, addr = %peer_addr, "s3-tcp peer accepted");
                            if let Some(cb) = transport.on_peer_accepted.read().clone() {
                                cb(peer_id);
                            }
                        }
                        Err(e) => warn!("s3-tcp accept error: {e}"),
                    }
                }
            })?;
        *self.listener_handle.lock() = Some(handle);
        Ok(local)
    }

    pub fn shutdown(&self) {
        self.shutdown.store(true, Ordering::Relaxed);
    }

    fn peer(&self, peer_id: u64) -> io::Result<Arc<PeerConnection>> {
        self.peers
            .read()
            .get(&peer_id)
            .cloned()
            .ok_or_else(|| io::Error::new(io::ErrorKind::NotFound, "peer not found"))
    }

    pub fn peer_addr(&self, peer_id: u64) -> Option<String> {
        self.peers.read().get(&peer_id).map(|p| p.addr.clone())
    }

    /// Drop the peer's connection state. Called by the server's
    /// dispatcher on EOF / IO error so subsequent operations on
    /// the peer_id fail fast.
    pub fn drop_peer(&self, peer_id: u64) {
        self.peers.write().remove(&peer_id);
    }

    // -----------------------------------------------------------------
    // Control-plane frames. Identical role to the SEND/RECV pair on
    // `RdmaTransport`, but over TCP. No memory registration, no QP.
    // -----------------------------------------------------------------

    /// Set the credentials used to sign outgoing requests.
    pub fn set_credentials(&self, creds: crate::auth::Credentials) {
        *self.credentials.write() = Some(creds);
    }

    pub fn send_request(&self, peer_id: u64, req: &Request) -> io::Result<()> {
        let peer = self.peer(peer_id)?;
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        // Sign when credentials are configured. A server that enforces
        // auth rejects anything unsigned, so a misconfigured client fails
        // loudly at the peer instead of sending anonymous traffic.
        // The envelope is ALWAYS present so the frame format is uniform and
        // a server never has to guess whether a frame is signed. A client
        // with no credentials sends an empty access key, which the server
        // rejects explicitly rather than silently treating as anonymous.
        let creds = self
            .credentials
            .read()
            .clone()
            .unwrap_or_else(|| crate::auth::Credentials::new("", ""));
        let nonce = crate::auth::next_nonce().map_err(io::Error::other)?;
        let n = super::proto::encode_signed(req, &creds, nonce, &mut buf)?;
        let mut w = peer.writer.lock();
        write_frame(&mut *w, &buf[..n])?;
        w.flush()?;
        debug!(peer_id, op = ?req, "s3-tcp send_request");
        Ok(())
    }

    pub fn recv_response(&self, peer_id: u64) -> io::Result<Response> {
        let peer = self.peer(peer_id)?;
        let mut scratch = [0u8; MAX_CONTROL_FRAME];
        let mut r = peer.reader.lock();
        let frame = read_frame(&mut *r, &mut scratch)?;
        Response::decode(frame)
    }

    /// Returns the signed envelope: auth header, decoded request, and the
    /// raw body the signature covers. The caller verifies before acting.
    pub fn recv_request(
        &self,
        peer_id: u64,
    ) -> io::Result<(crate::auth::AuthHeader, Request, Vec<u8>)> {
        let peer = self.peer(peer_id)?;
        let mut scratch = [0u8; MAX_CONTROL_FRAME];
        let mut r = peer.reader.lock();
        let frame = read_frame(&mut *r, &mut scratch)?;
        super::proto::decode_signed(frame)
    }

    pub fn send_response(&self, peer_id: u64, resp: &Response) -> io::Result<()> {
        let peer = self.peer(peer_id)?;
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        let n = resp.encode(&mut buf)?;
        let mut w = peer.writer.lock();
        write_frame(&mut *w, &buf[..n])?;
        w.flush()?;
        Ok(())
    }

    // -----------------------------------------------------------------
    // Payload streaming. For PUT the client follows the request frame
    // with `length` raw bytes on the same socket; for GET the server
    // does the same after sending Ok. No framing on the payload —
    // both sides size it from the control-frame `length` field.
    // -----------------------------------------------------------------

    /// Response frame and its payload under ONE writer-lock hold. The
    /// server runs several workers per peer; with separate
    /// `send_response` + `write_payload` calls another worker's frame
    /// could land between a GET header and its payload and desync the
    /// client.
    pub fn send_response_with_payload(
        &self,
        peer_id: u64,
        resp: &Response,
        payload: &[u8],
    ) -> io::Result<()> {
        let peer = self.peer(peer_id)?;
        let mut buf = [0u8; MAX_CONTROL_FRAME];
        let n = resp.encode(&mut buf)?;
        let mut w = peer.writer.lock();
        write_frame(&mut *w, &buf[..n])?;
        w.write_all(payload)?;
        w.flush()?;
        Ok(())
    }

    pub fn write_payload(&self, peer_id: u64, data: &[u8]) -> io::Result<()> {
        let peer = self.peer(peer_id)?;
        let mut w = peer.writer.lock();
        w.write_all(data)?;
        w.flush()?;
        Ok(())
    }

    pub fn read_payload(&self, peer_id: u64, buf: &mut [u8]) -> io::Result<()> {
        let peer = self.peer(peer_id)?;
        let mut r = peer.reader.lock();
        r.read_exact(buf)
    }

    /// One round-trip: send `req`, read `Response`. Atomic under
    /// the per-peer txn lock so concurrent threads sharing one
    /// `TcpTransport` peer don't mis-frame each other on the wire.
    pub fn request_response(&self, peer_id: u64, req: &Request) -> io::Result<Response> {
        let txn_arc = self.txn_arc(peer_id)?;
        let _guard = txn_arc.lock();
        self.send_request(peer_id, req)?;
        self.recv_response(peer_id)
    }

    /// Hand the caller the per-peer transaction mutex `Arc` so it
    /// can hold it for the lifetime of a multi-step exchange:
    /// PUT = (send_request + write_payload + recv_response),
    /// GET = (send_request + recv_response + read_payload). The
    /// dispatcher (server) doesn't need this — it's single-threaded
    /// per peer.
    pub fn txn_arc(&self, peer_id: u64) -> io::Result<Arc<Mutex<()>>> {
        Ok(Arc::clone(&self.peer(peer_id)?.txn))
    }
}

/// Pin TCP_NODELAY + large SO_SNDBUF/SO_RCVBUF on the socket. The
/// kernel autotunes wmem/rmem up to net.ipv4.tcp_{w,r}mem max (~4-6
/// MiB by default), which is on the edge for the multi-GB/s flows
/// we want. Pinning to 8 MiB ensures the queues stay deep enough
/// to keep the NIC fed under load on intra-DC latencies (50-200 µs
/// RTT, BDP ≈ 1-5 MiB per QP at 200 Gbps).
///
/// SO_SNDBUF/SO_RCVBUF requests above `net.core.{wmem,rmem}_max`
/// are silently clamped by the kernel; that's fine — we get
/// whatever the sysctl allows.
fn tune_socket(s: &TcpStream) -> io::Result<()> {
    s.set_nodelay(true)?;
    let fd = s.as_raw_fd();
    unsafe {
        let v: libc::c_int = SOCK_BUF;
        let _ = libc::setsockopt(
            fd,
            libc::SOL_SOCKET,
            libc::SO_SNDBUF,
            &v as *const _ as *const _,
            std::mem::size_of_val(&v) as libc::socklen_t,
        );
        let _ = libc::setsockopt(
            fd,
            libc::SOL_SOCKET,
            libc::SO_RCVBUF,
            &v as *const _ as *const _,
            std::mem::size_of_val(&v) as libc::socklen_t,
        );
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Duration;

    #[test]
    fn loopback_send_recv_request_response() {
        let server = TcpTransport::new();
        let client = TcpTransport::new();

        let accepted = Arc::new(AtomicU64::new(0));
        let accepted_clone = Arc::clone(&accepted);
        server.set_on_peer_accepted(Arc::new(move |pid| {
            accepted_clone.store(pid, Ordering::Relaxed);
        }));

        let addr = server.listen(0).expect("listen");
        thread::sleep(Duration::from_millis(50));
        // `listen` binds the wildcard address; dial loopback explicitly.
        let peer_id = client
            .connect(&format!("127.0.0.1:{}", addr.port()))
            .expect("connect");
        thread::sleep(Duration::from_millis(100));

        // Spawn a server-side echo dispatcher.
        let server_pid = accepted.load(Ordering::Relaxed);
        assert!(server_pid > 0, "callback should have fired");
        let server_t = Arc::clone(&server);
        thread::spawn(move || {
            let (_hdr, req, _body) = server_t.recv_request(server_pid).expect("recv");
            let resp = Response::Ok {
                req_id: req.req_id(),
                length: 0,
                crc32c: 0,
            };
            server_t.send_response(server_pid, &resp).expect("send");
        });

        let req = Request::Head {
            req_id: 42,
            key: "loopback/test".into(),
        };
        let resp = client.request_response(peer_id, &req).expect("rt");
        assert_eq!(resp.req_id(), 42);
        match resp {
            Response::Ok { .. } => {}
            other => panic!("expected Ok, got {other:?}"),
        }
    }
}
