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

//! Authenticated TCP bootstrap for RDMA connections.
//!
//! Before an RC connection exists, both sides must learn each other's QP
//! numbers and GID over some other channel; here that is a TCP
//! connection to the transport's listening port. That exchange used to be
//! anonymous, and it handed every connector the server pool's address,
//! rkey and length, so anyone who could reach the port could RDMA-READ
//! and RDMA-WRITE the bytes being served without ever sending a signed
//! request. This module makes the bootstrap the authentication point:
//! the server creates no QP, CQ or memory registration for a connector
//! until the connector has proven it holds a credential.
//!
//! ```text
//! server → client  HELLO   magic, version, 32-byte random challenge,
//!                          max lanes, server LID + GID
//! client → server  AUTH    access key, client LID + GID, one QPN per
//!                          lane, HMAC-SHA256(secret, CONTEXT || HELLO ||
//!                          AUTH-without-MAC)
//! server → client  ACCEPT  server LID + GID, one QPN per lane
//!               or REJECT  + reason byte: credentials refused, setup
//!                          failed, or malformed (see [`RejectReason`])
//!               or BUSY    (authenticated, but the server's
//!                          [`SessionAdmitter`] has no room now)
//! ```
//!
//! * The challenge is fresh per connection, so a captured AUTH is useless
//!   on any other connection (no replay), and no timestamp or nonce
//!   cache is needed.
//! * The MAC covers the client's own QPNs and GID, so a man in the middle
//!   that relays a genuine AUTH cannot point the server's QPs at itself.
//! * The MAC input starts with a context string that no request signature
//!   (`crate::auth::sign`, whose input starts with a timestamp) can
//!   collide with, so neither can be lifted onto the other.
//! * Servers never produce a MAC, so there is nothing to reflect.
//!
//! The server decides through a [`HandshakeVerifier`] that its owner
//! configures, which maps the presented credential to a [`PeerRole`].
//! With no verifier the server stays open to anyone (role `Client`), as
//! it was before; it still never grants a peer remote access to its
//! memory (see `crate::transport`).
//!
//! The TCP connection stays open for the life of the RDMA connection and
//! carries nothing more: end-of-stream on either side means the peer is
//! gone.

use std::any::Any;
use std::collections::HashMap;
use std::io::{self, Read, Write};
use std::net::IpAddr;
use std::sync::Arc;
use std::time::Duration;

use hmac::{Hmac, Mac};
use parking_lot::Mutex;
use sha2::Sha256;

use crate::auth::{Credentials, SIGNATURE_LEN};

type HmacSha256 = Hmac<Sha256>;

/// First bytes of every bootstrap message. A v1 peer opened with a raw
/// 42-byte QP record, which never starts with these.
pub const MAGIC: [u8; 4] = *b"BRDM";

/// Version of the bootstrap and of the S3-over-RDMA control protocol it
/// sets up (`crate::s3_rdma_proto`). Bump on any incompatible change to
/// either, so mismatched peers fail here with a clear message. v3 is v2
/// with the project's new name in the MAC contexts.
pub const PROTOCOL_VERSION: u16 = 3;

/// Domain-separation prefix of the AUTH MAC input.
const CONTEXT: &[u8] = b"nvstor-rdma-bootstrap-v3\0";

pub const CHALLENGE_LEN: usize = 32;

/// Most QP lanes one connection may ask for.
pub const MAX_LANES: usize = 64;

const KIND_HELLO: u8 = 1;
const KIND_AUTH: u8 = 2;
const KIND_ACCEPT: u8 = 3;
const KIND_REJECT: u8 = 4;
const KIND_BUSY: u8 = 5;

/// Text every BUSY refusal carries in its message, so a caller that only
/// sees an error string (a `String` error from a connect path) can tell
/// "try another server" from "this server is broken"; see [`is_busy`].
pub const BUSY_MARKER: &str = "[nvstor-rdma BUSY]";

/// Whether an error message is a BUSY refusal ([`BootstrapError::Busy`]).
pub fn is_busy(message: &str) -> bool {
    message.contains(BUSY_MARKER)
}

/// Text a credential refusal carries in its message, for the same reason
/// as [`BUSY_MARKER`]; see [`is_rejected`].
pub const REJECTED_MARKER: &str = "[nvstor-rdma REJECTED]";

/// Whether an error message is a credential refusal: a REJECT whose
/// reason is [`RejectReason::Credentials`], or [`RejectReason::Unspecified`]
/// (a server that predates reasons; most of its REJECTs were credential
/// refusals). A server that failed to set the connection up, or found
/// the handshake malformed, says so and is not a credential refusal.
pub fn is_rejected(message: &str) -> bool {
    message.contains(REJECTED_MARKER)
}

/// Why a server sent REJECT. Carried in one byte after the REJECT
/// header, an extension within protocol v2 that both directions of
/// version skew tolerate: a client that predates it stops reading after
/// the header and closes, so it never sees the byte; a server that
/// predates it sends the bare header and closes, which reads here as
/// `Unspecified`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RejectReason {
    /// No reason given (an older server), or one this build does not know.
    Unspecified,
    /// The connector's credentials were refused.
    Credentials,
    /// The server could not build its side of the connection (out of QPs,
    /// memory): worth retrying, here or elsewhere.
    Setup,
    /// The connector's handshake broke the protocol (too many lanes).
    Malformed,
}

const REJECT_CREDENTIALS: u8 = 1;
const REJECT_SETUP: u8 = 2;
const REJECT_MALFORMED: u8 = 3;

impl RejectReason {
    fn code(self) -> Option<u8> {
        match self {
            Self::Unspecified => None,
            Self::Credentials => Some(REJECT_CREDENTIALS),
            Self::Setup => Some(REJECT_SETUP),
            Self::Malformed => Some(REJECT_MALFORMED),
        }
    }

    fn from_code(b: u8) -> Self {
        match b {
            REJECT_CREDENTIALS => Self::Credentials,
            REJECT_SETUP => Self::Setup,
            REJECT_MALFORMED => Self::Malformed,
            _ => Self::Unspecified,
        }
    }

    /// Read the reason after a REJECT header. End of stream (or any read
    /// error) before it means an older server: `Unspecified`.
    fn read_from<R: Read>(r: &mut R) -> Self {
        let mut b = [0u8; 1];
        match r.read_exact(&mut b) {
            Ok(()) => Self::from_code(b[0]),
            Err(_) => Self::Unspecified,
        }
    }
}

/// magic + version + kind.
const HEADER_LEN: usize = 4 + 2 + 1;

/// What an authenticated connector may do.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PeerRole {
    /// Refused: the server creates nothing for it.
    Denied,
    /// An object-protocol client (GET / HEAD / PUT within its policy).
    Client,
    /// A member of this server's cluster: may also replicate into it.
    Cluster,
}

/// What a [`HandshakeVerifier`] is shown.
pub struct HandshakeAuth<'a> {
    /// The access key the connector presented ("" when it has none).
    pub access_key: &'a str,
    hello: &'a [u8],
    signed: &'a [u8],
    mac: &'a [u8; SIGNATURE_LEN],
}

impl HandshakeAuth<'_> {
    /// Whether the connector's MAC was made with `secret`. Constant time.
    pub fn verify(&self, secret: &str) -> bool {
        let Ok(mut m) = <HmacSha256 as Mac>::new_from_slice(secret.as_bytes()) else {
            return false;
        };
        m.update(CONTEXT);
        m.update(self.hello);
        m.update(self.signed);
        m.verify_slice(self.mac).is_ok()
    }
}

/// Server-supplied decision on a connector: look up the access key's
/// secret, check [`HandshakeAuth::verify`], and return the role (or
/// `Denied`). Runs on the accept thread before any verbs object exists.
pub type HandshakeVerifier = Arc<dyn Fn(&HandshakeAuth) -> PeerRole + Send + Sync>;

/// An admitted connector's hold on a server-side session limit. The
/// transport keeps it with the peer and drops it when the peer is torn
/// down, whatever the cause (disconnect, end of the bootstrap stream,
/// shutdown), which is how the limit's owner learns the slot is free.
pub type SessionClaim = Box<dyn Any + Send + Sync>;

/// Server-supplied capacity check, run after a connector authenticated
/// (with its access key and role) and before any verbs object exists.
/// `Some(claim)` admits it; `None` answers BUSY, so a refused connector
/// costs the server no QP, CQ or memory registration and learns at once
/// that it should go elsewhere instead of timing out.
pub type SessionAdmitter = Arc<dyn Fn(&str, PeerRole) -> Option<SessionClaim> + Send + Sync>;

/// One side's addressing for an RC connection: its port's LID and GID and
/// one QP number per lane.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Endpoint {
    pub lid: u16,
    pub gid: [u8; 16],
    pub qpns: Vec<u32>,
}

/// The server's opening message.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Hello {
    pub challenge: [u8; CHALLENGE_LEN],
    /// Most lanes the server will build; the client asks for at most this.
    pub max_lanes: u16,
    pub lid: u16,
    pub gid: [u8; 16],
}

#[derive(Debug)]
pub enum BootstrapError {
    Io(io::Error),
    /// The peer does not speak this bootstrap at all (an older nvstor,
    /// or not nvstor).
    NotNvstor,
    /// The peer speaks another version of it.
    Version(u16),
    Malformed(String),
    /// The server refused this connection, for this reason.
    Rejected(RejectReason),
    /// The connector authenticated, but the server is at its session
    /// limit: try another server, or this one later.
    Busy,
    /// (Server side) the connector failed authentication.
    Denied,
    /// (Server side) building this side of the connection failed.
    Setup(String),
}

impl std::fmt::Display for BootstrapError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Io(e) => write!(f, "bootstrap I/O: {e}"),
            Self::NotNvstor => write!(
                f,
                "peer does not speak the nvstor RDMA bootstrap \
                (v{PROTOCOL_VERSION}); is it an older nvstor build?"
            ),
            Self::Version(v) => write!(
                f,
                "peer speaks nvstor RDMA protocol v{v}, \
                this build speaks v{PROTOCOL_VERSION}; upgrade both sides"
            ),
            Self::Malformed(m) => write!(f, "malformed bootstrap message: {m}"),
            Self::Rejected(RejectReason::Credentials) => write!(
                f,
                "server refused this connector's credentials {REJECTED_MARKER}"
            ),
            Self::Rejected(RejectReason::Unspecified) => write!(
                f,
                "server refused the connection without a reason (an older server: bad or missing \
                credentials, or it could not set up the connection) {REJECTED_MARKER}"
            ),
            Self::Rejected(RejectReason::Setup) => write!(
                f,
                "server could not set up the connection; retry, or try another server"
            ),
            Self::Rejected(RejectReason::Malformed) => {
                write!(f, "server refused a malformed handshake")
            }
            Self::Busy => write!(
                f,
                "server is at its limit of concurrent sessions; try another \
                source or retry later {BUSY_MARKER}"
            ),
            Self::Denied => write!(f, "connector failed authentication"),
            Self::Setup(e) => write!(f, "connection setup: {e}"),
        }
    }
}

impl From<io::Error> for BootstrapError {
    fn from(e: io::Error) -> Self {
        Self::Io(e)
    }
}

fn header(kind: u8) -> Vec<u8> {
    let mut b = Vec::with_capacity(128);
    b.extend_from_slice(&MAGIC);
    b.extend_from_slice(&PROTOCOL_VERSION.to_le_bytes());
    b.push(kind);
    b
}

/// Read a message header, accumulating raw bytes into `raw`, and return
/// its kind.
fn read_header<R: Read>(r: &mut R, raw: &mut Vec<u8>) -> Result<u8, BootstrapError> {
    let mut h = [0u8; HEADER_LEN];
    r.read_exact(&mut h)?;
    raw.extend_from_slice(&h);
    if h[..4] != MAGIC {
        return Err(BootstrapError::NotNvstor);
    }
    let version = u16::from_le_bytes([h[4], h[5]]);
    if version != PROTOCOL_VERSION {
        return Err(BootstrapError::Version(version));
    }
    Ok(h[6])
}

fn read_bytes<R: Read, const N: usize>(
    r: &mut R,
    raw: &mut Vec<u8>,
) -> Result<[u8; N], BootstrapError> {
    let mut b = [0u8; N];
    r.read_exact(&mut b)?;
    raw.extend_from_slice(&b);
    Ok(b)
}

fn put_endpoint(b: &mut Vec<u8>, e: &Endpoint) -> Result<(), BootstrapError> {
    if e.qpns.is_empty() || e.qpns.len() > MAX_LANES {
        return Err(BootstrapError::Malformed(format!(
            "{} lanes (1..={MAX_LANES})",
            e.qpns.len()
        )));
    }
    b.extend_from_slice(&e.lid.to_le_bytes());
    b.extend_from_slice(&e.gid);
    b.push(e.qpns.len() as u8);
    for q in &e.qpns {
        b.extend_from_slice(&q.to_le_bytes());
    }
    Ok(())
}

fn read_endpoint<R: Read>(r: &mut R, raw: &mut Vec<u8>) -> Result<Endpoint, BootstrapError> {
    let lid = u16::from_le_bytes(read_bytes::<_, 2>(r, raw)?);
    let gid = read_bytes::<_, 16>(r, raw)?;
    let [n] = read_bytes::<_, 1>(r, raw)?;
    let n = usize::from(n);
    if n == 0 || n > MAX_LANES {
        return Err(BootstrapError::Malformed(format!(
            "{n} lanes (1..={MAX_LANES})"
        )));
    }
    let mut qpns = Vec::with_capacity(n);
    for _ in 0..n {
        qpns.push(u32::from_le_bytes(read_bytes::<_, 4>(r, raw)?));
    }
    Ok(Endpoint { lid, gid, qpns })
}

impl Hello {
    /// A hello with a fresh random challenge.
    pub fn new(max_lanes: u16, lid: u16, gid: [u8; 16]) -> Result<Self, String> {
        let mut challenge = [0u8; CHALLENGE_LEN];
        getrandom::getrandom(&mut challenge)
            .map_err(|e| format!("bootstrap challenge entropy: {e}"))?;
        Ok(Self {
            challenge,
            max_lanes,
            lid,
            gid,
        })
    }

    pub fn encode(&self) -> Vec<u8> {
        let mut b = header(KIND_HELLO);
        b.extend_from_slice(&self.challenge);
        b.extend_from_slice(&self.max_lanes.to_le_bytes());
        b.extend_from_slice(&self.lid.to_le_bytes());
        b.extend_from_slice(&self.gid);
        b
    }

    /// Read a hello; also returns its raw bytes, which the AUTH MAC covers.
    pub fn read_from<R: Read>(r: &mut R) -> Result<(Self, Vec<u8>), BootstrapError> {
        let mut raw = Vec::with_capacity(64);
        match read_header(r, &mut raw)? {
            KIND_HELLO => {}
            KIND_REJECT => return Err(BootstrapError::Rejected(RejectReason::read_from(r))),
            k => {
                return Err(BootstrapError::Malformed(format!(
                    "expected HELLO, got kind {k}"
                )))
            }
        }
        let challenge = read_bytes::<_, CHALLENGE_LEN>(r, &mut raw)?;
        let max_lanes = u16::from_le_bytes(read_bytes::<_, 2>(r, &mut raw)?);
        let lid = u16::from_le_bytes(read_bytes::<_, 2>(r, &mut raw)?);
        let gid = read_bytes::<_, 16>(r, &mut raw)?;
        if max_lanes == 0 {
            return Err(BootstrapError::Malformed("server offers 0 lanes".into()));
        }
        Ok((
            Self {
                challenge,
                max_lanes,
                lid,
                gid,
            },
            raw,
        ))
    }
}

/// The AUTH message, as the server read it.
struct ClientAuth {
    access_key: String,
    endpoint: Endpoint,
    /// Raw bytes of the message up to the MAC: what the MAC covers after
    /// the context and the hello.
    signed: Vec<u8>,
    mac: [u8; SIGNATURE_LEN],
}

/// Encode an AUTH answering the hello whose raw bytes are `hello`.
pub fn encode_auth(
    hello: &[u8],
    creds: &Credentials,
    local: &Endpoint,
) -> Result<Vec<u8>, BootstrapError> {
    let key = creds.access_key.as_bytes();
    if key.len() > crate::s3_rdma_proto::MAX_ACCESS_KEY_LEN {
        return Err(BootstrapError::Malformed(format!(
            "access key longer than {} bytes",
            crate::s3_rdma_proto::MAX_ACCESS_KEY_LEN
        )));
    }
    let mut b = header(KIND_AUTH);
    b.push(key.len() as u8);
    b.extend_from_slice(key);
    put_endpoint(&mut b, local)?;
    let mut m = <HmacSha256 as Mac>::new_from_slice(creds.secret_key.as_bytes())
        .map_err(|e| BootstrapError::Malformed(format!("secret: {e}")))?;
    m.update(CONTEXT);
    m.update(hello);
    m.update(&b);
    let mac = m.finalize().into_bytes();
    b.extend_from_slice(&mac[..SIGNATURE_LEN]);
    Ok(b)
}

fn read_auth<R: Read>(r: &mut R) -> Result<ClientAuth, BootstrapError> {
    let mut raw = Vec::with_capacity(128);
    if read_header(r, &mut raw)? != KIND_AUTH {
        return Err(BootstrapError::Malformed("expected AUTH".into()));
    }
    let [klen] = read_bytes::<_, 1>(r, &mut raw)?;
    let klen = usize::from(klen);
    if klen > crate::s3_rdma_proto::MAX_ACCESS_KEY_LEN {
        return Err(BootstrapError::Malformed(format!(
            "access key of {klen} bytes"
        )));
    }
    let mut key = vec![0u8; klen];
    r.read_exact(&mut key)?;
    raw.extend_from_slice(&key);
    let access_key = String::from_utf8(key)
        .map_err(|_| BootstrapError::Malformed("access key is not UTF-8".into()))?;
    let endpoint = read_endpoint(r, &mut raw)?;
    let mut mac = [0u8; SIGNATURE_LEN];
    r.read_exact(&mut mac)?;
    Ok(ClientAuth {
        access_key,
        endpoint,
        signed: raw,
        mac,
    })
}

pub fn encode_accept(local: &Endpoint) -> Result<Vec<u8>, BootstrapError> {
    let mut b = header(KIND_ACCEPT);
    put_endpoint(&mut b, local)?;
    Ok(b)
}

/// REJECT, with `reason` unless it is `Unspecified`.
pub fn encode_reject(reason: RejectReason) -> Vec<u8> {
    let mut b = header(KIND_REJECT);
    b.extend(reason.code());
    b
}

pub fn encode_busy() -> Vec<u8> {
    header(KIND_BUSY)
}

/// Client side, after sending AUTH: the server's endpoint, or why not.
pub fn read_verdict<R: Read>(r: &mut R, lanes: usize) -> Result<Endpoint, BootstrapError> {
    let mut raw = Vec::with_capacity(64);
    match read_header(r, &mut raw)? {
        KIND_ACCEPT => {}
        KIND_REJECT => return Err(BootstrapError::Rejected(RejectReason::read_from(r))),
        KIND_BUSY => return Err(BootstrapError::Busy),
        k => {
            return Err(BootstrapError::Malformed(format!(
                "expected ACCEPT, got kind {k}"
            )))
        }
    }
    let e = read_endpoint(r, &mut raw)?;
    if e.qpns.len() != lanes {
        return Err(BootstrapError::Malformed(format!(
            "server built {} lanes, asked for {lanes}",
            e.qpns.len()
        )));
    }
    Ok(e)
}

/// Server side of the handshake on an accepted stream. The caller has
/// already bounded the stream's read and write time.
///
/// Sends `hello`, reads and authenticates the connector's AUTH and only
/// then calls `build` with the connector's role and endpoint. `build`
/// creates this side's QPs (lanes) and returns their endpoint together
/// with whatever the caller needs to keep; on success the ACCEPT is sent.
/// A connector that fails authentication gets REJECT and `build` is never
/// called, so it costs the server no verbs resources.
pub fn serve<S, T>(
    stream: &mut S,
    hello: &Hello,
    verifier: Option<&HandshakeVerifier>,
    build: impl FnOnce(PeerRole, &Endpoint) -> Result<(Endpoint, T), String>,
) -> Result<(PeerRole, T), BootstrapError>
where
    S: Read + Write,
{
    serve_admitted(stream, hello, verifier, None, build).map(|(role, _, kept)| (role, kept))
}

/// [`serve`] with a session limit: after authentication and before
/// `build`, `admitter` (when set) decides whether the connector may join
/// now. A connector it turns away gets BUSY (and this returns
/// [`BootstrapError::Busy`]); `build` never runs for it. On success the
/// claim is returned for the caller to keep for as long as the peer
/// lives.
pub fn serve_admitted<S, T>(
    stream: &mut S,
    hello: &Hello,
    verifier: Option<&HandshakeVerifier>,
    admitter: Option<&SessionAdmitter>,
    build: impl FnOnce(PeerRole, &Endpoint) -> Result<(Endpoint, T), String>,
) -> Result<(PeerRole, Option<SessionClaim>, T), BootstrapError>
where
    S: Read + Write,
{
    serve_identified(stream, hello, verifier, admitter, build)
        .map(|(role, claim, _, kept)| (role, claim, kept))
}

/// [`serve_admitted`] that also returns the access key the connector
/// authenticated with (its identity, for a protocol that binds signed
/// requests to the connection, such as the KV service).
pub fn serve_identified<S, T>(
    stream: &mut S,
    hello: &Hello,
    verifier: Option<&HandshakeVerifier>,
    admitter: Option<&SessionAdmitter>,
    build: impl FnOnce(PeerRole, &Endpoint) -> Result<(Endpoint, T), String>,
) -> Result<(PeerRole, Option<SessionClaim>, String, T), BootstrapError>
where
    S: Read + Write,
{
    let hello_raw = hello.encode();
    stream.write_all(&hello_raw)?;
    let auth = read_auth(stream)?;
    if auth.endpoint.qpns.len() > usize::from(hello.max_lanes) {
        let _ = stream.write_all(&encode_reject(RejectReason::Malformed));
        return Err(BootstrapError::Malformed(format!(
            "{} lanes requested, {} offered",
            auth.endpoint.qpns.len(),
            hello.max_lanes
        )));
    }
    let role = match verifier {
        None => PeerRole::Client,
        Some(v) => v(&HandshakeAuth {
            access_key: &auth.access_key,
            hello: &hello_raw,
            signed: &auth.signed,
            mac: &auth.mac,
        }),
    };
    if role == PeerRole::Denied {
        let _ = stream.write_all(&encode_reject(RejectReason::Credentials));
        return Err(BootstrapError::Denied);
    }
    let claim = match admitter {
        None => None,
        Some(admit) => match admit(&auth.access_key, role) {
            Some(claim) => Some(claim),
            None => {
                let _ = stream.write_all(&encode_busy());
                return Err(BootstrapError::Busy);
            }
        },
    };
    let (local, kept) = match build(role, &auth.endpoint) {
        Ok(v) => v,
        Err(e) => {
            let _ = stream.write_all(&encode_reject(RejectReason::Setup));
            return Err(BootstrapError::Setup(e));
        }
    };
    stream.write_all(&encode_accept(&local)?)?;
    Ok((role, claim, auth.access_key, kept))
}

/// Limits on what unauthenticated and authenticated connectors may hold
/// on a listening transport.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct AcceptLimits {
    /// Handshakes in progress at once. A connector that opens TCP and
    /// sends nothing holds one until `handshake_timeout`.
    pub max_handshakes: usize,
    /// Peers (handshaking plus established) at once.
    pub max_peers: usize,
    /// Peers (handshaking plus established) from one source address
    /// (IPv4-mapped IPv6 counted as the IPv4 address). An IPv6 /64 as a
    /// whole may hold [`PREFIX_FACTOR`] times this many: a host can use
    /// any address of its /64, while many hosts (every instance of a
    /// cloud subnet) can share one /64. See [`source_key`], [`prefix_key`].
    pub max_peers_per_source: usize,
    /// Bound on each read and write of the handshake.
    pub handshake_timeout: Duration,
}

impl Default for AcceptLimits {
    /// Sized for a source pod serving many fetchers: a fetch opens about
    /// 33 peers per server (32 workers plus a rail lookup), and every
    /// peer costs 4 QPs, 8 CQs and 9 control buffers per lane.
    fn default() -> Self {
        Self {
            max_handshakes: 64,
            max_peers: 4096,
            max_peers_per_source: 512,
            handshake_timeout: Duration::from_secs(5),
        }
    }
}

#[derive(Default)]
struct GateState {
    handshakes: usize,
    peers: usize,
    per_source: HashMap<IpAddr, usize>,
    per_prefix: HashMap<IpAddr, usize>,
}

/// Admission control for accepted connections. Each admitted connection
/// holds an [`AcceptTicket`] until it is closed.
#[derive(Default)]
pub struct AcceptGate {
    limits: Mutex<AcceptLimits>,
    state: Mutex<GateState>,
}

/// How many times `max_peers_per_source` one IPv6 /64 may hold.
pub const PREFIX_FACTOR: usize = 8;

/// What the per-source limit counts a connector as: its address, an
/// IPv4-mapped IPv6 address as that IPv4 address.
pub fn source_key(ip: IpAddr) -> IpAddr {
    ip.to_canonical()
}

/// The IPv6 /64 a connector's address is in, for the per-prefix limit;
/// `None` for IPv4.
pub fn prefix_key(ip: IpAddr) -> Option<IpAddr> {
    match ip.to_canonical() {
        IpAddr::V6(a) => Some(IpAddr::V6(std::net::Ipv6Addr::from(
            u128::from(a) & !((1u128 << 64) - 1),
        ))),
        IpAddr::V4(_) => None,
    }
}

impl AcceptGate {
    pub fn new(limits: AcceptLimits) -> Arc<Self> {
        Arc::new(Self {
            limits: Mutex::new(limits),
            state: Mutex::new(GateState::default()),
        })
    }

    pub fn limits(&self) -> AcceptLimits {
        *self.limits.lock()
    }

    pub fn set_limits(&self, limits: AcceptLimits) {
        *self.limits.lock() = limits;
    }

    /// Admit a new connection from `source` into the handshake stage, or
    /// say which limit refused it.
    pub fn try_admit(self: &Arc<Self>, source: IpAddr) -> Result<AcceptTicket, &'static str> {
        let prefix = prefix_key(source);
        let source = source_key(source);
        let limits = self.limits();
        let mut s = self.state.lock();
        if s.handshakes >= limits.max_handshakes {
            return Err("too many handshakes in progress");
        }
        if s.handshakes + s.peers >= limits.max_peers {
            return Err("peer limit reached");
        }
        let from_source = s.per_source.get(&source).copied().unwrap_or(0);
        if from_source >= limits.max_peers_per_source {
            return Err("per-source peer limit reached");
        }
        if let Some(p) = prefix {
            let from_prefix = s.per_prefix.get(&p).copied().unwrap_or(0);
            if from_prefix >= limits.max_peers_per_source.saturating_mul(PREFIX_FACTOR) {
                return Err("per-/64 peer limit reached");
            }
            s.per_prefix.insert(p, from_prefix + 1);
        }
        s.handshakes += 1;
        s.per_source.insert(source, from_source + 1);
        Ok(AcceptTicket {
            gate: Arc::clone(self),
            source,
            prefix,
            established: false,
        })
    }

    /// (handshakes in progress, established peers).
    pub fn counts(&self) -> (usize, usize) {
        let s = self.state.lock();
        (s.handshakes, s.peers)
    }
}

/// One admitted connection's claim on the [`AcceptGate`]. Released on drop.
pub struct AcceptTicket {
    gate: Arc<AcceptGate>,
    source: IpAddr,
    /// The IPv6 /64 counted against the per-prefix cap.
    prefix: Option<IpAddr>,
    established: bool,
}

impl AcceptTicket {
    /// The handshake succeeded: move from the handshake count to the
    /// peer count.
    pub fn establish(&mut self) {
        if !self.established {
            let mut s = self.gate.state.lock();
            s.handshakes = s.handshakes.saturating_sub(1);
            s.peers += 1;
            self.established = true;
        }
    }
}

impl Drop for AcceptTicket {
    fn drop(&mut self) {
        let mut s = self.gate.state.lock();
        if self.established {
            s.peers = s.peers.saturating_sub(1);
        } else {
            s.handshakes = s.handshakes.saturating_sub(1);
        }
        if let Some(n) = s.per_source.get_mut(&self.source) {
            *n = n.saturating_sub(1);
            if *n == 0 {
                s.per_source.remove(&self.source);
            }
        }
        if let Some(p) = self.prefix {
            if let Some(n) = s.per_prefix.get_mut(&p) {
                *n = n.saturating_sub(1);
                if *n == 0 {
                    s.per_prefix.remove(&p);
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::net::UnixStream;
    use std::sync::atomic::{AtomicBool, Ordering};

    const SECRET: &str = "cluster-secret-0123456789";

    fn verifier() -> HandshakeVerifier {
        Arc::new(|a: &HandshakeAuth| match a.access_key {
            "cluster" if a.verify(SECRET) => PeerRole::Cluster,
            "client" if a.verify("client-secret") => PeerRole::Client,
            _ => PeerRole::Denied,
        })
    }

    fn client_endpoint() -> Endpoint {
        Endpoint {
            lid: 0,
            gid: [7; 16],
            qpns: vec![101, 102, 103, 104],
        }
    }

    fn server_endpoint(n: usize) -> Endpoint {
        Endpoint {
            lid: 0,
            gid: [9; 16],
            qpns: (0..n as u32).map(|i| 900 + i).collect(),
        }
    }

    /// The client half, as `RdmaTransport::dial` + `connect_dialed` run it.
    fn client(
        mut s: UnixStream,
        creds: Credentials,
        local: Endpoint,
    ) -> Result<(Hello, Endpoint), BootstrapError> {
        let (hello, raw) = Hello::read_from(&mut s)?;
        s.write_all(&encode_auth(&raw, &creds, &local)?)?;
        let server = read_verdict(&mut s, local.qpns.len())?;
        Ok((hello, server))
    }

    type ServerSide = Result<(PeerRole, Endpoint), BootstrapError>;
    type ClientSide = Result<(Hello, Endpoint), BootstrapError>;

    /// Both halves over a socketpair: (server result, client result,
    /// whether the server's `build` ran).
    fn run(
        creds: Credentials,
        verifier: Option<HandshakeVerifier>,
    ) -> (ServerSide, ClientSide, bool) {
        let (mut a, b) = UnixStream::pair().expect("socketpair");
        let c = std::thread::spawn(move || client(b, creds, client_endpoint()));
        let built = AtomicBool::new(false);
        let hello = Hello::new(8, 0, [9; 16]).expect("hello");
        let r = serve(&mut a, &hello, verifier.as_ref(), |_, remote| {
            built.store(true, Ordering::SeqCst);
            Ok((server_endpoint(remote.qpns.len()), remote.clone()))
        });
        drop(a);
        (
            r,
            c.join().expect("client thread"),
            built.load(Ordering::SeqCst),
        )
    }

    #[test]
    fn authenticated_cluster_peer_gets_its_role_and_endpoints() {
        let (server, client, built) = run(Credentials::new("cluster", SECRET), Some(verifier()));
        let (role, remote) = server.expect("server side");
        assert_eq!(role, PeerRole::Cluster);
        assert_eq!(remote, client_endpoint());
        let (hello, ep) = client.expect("client side");
        assert_eq!(hello.gid, [9; 16]);
        assert_eq!(ep, server_endpoint(4));
        assert!(built);
    }

    #[test]
    fn client_credential_maps_to_client_role() {
        let (server, client, _) = run(
            Credentials::new("client", "client-secret"),
            Some(verifier()),
        );
        assert_eq!(server.expect("server").0, PeerRole::Client);
        assert!(client.is_ok());
    }

    // A connector without the secret is refused before the
    // server builds a single QP or CQ, and learns nothing but "rejected".
    #[test]
    fn wrong_secret_is_rejected_before_any_resource_is_built() {
        for creds in [
            Credentials::new("cluster", "not-the-secret"),
            Credentials::new("nobody", SECRET),
            Credentials::new("", ""),
        ] {
            let (server, client, built) = run(creds, Some(verifier()));
            assert!(matches!(server, Err(BootstrapError::Denied)), "{server:?}");
            assert!(
                matches!(
                    client,
                    Err(BootstrapError::Rejected(RejectReason::Credentials))
                ),
                "{client:?}"
            );
            assert!(is_rejected(&client.expect_err("rejected").to_string()));
            assert!(!built, "build ran for an unauthenticated connector");
        }
    }

    // No verifier: the transport stays open as before (role Client).
    #[test]
    fn no_verifier_admits_anyone_as_client() {
        let (server, client, built) = run(Credentials::new("", ""), None);
        assert_eq!(server.expect("server").0, PeerRole::Client);
        assert!(client.is_ok() && built);
    }

    // The MAC binds the connector's own QPNs and GID: a relay that swaps
    // in its endpoint under a genuine MAC is refused.
    #[test]
    fn tampered_endpoint_fails_authentication() {
        let (mut a, mut b) = UnixStream::pair().expect("socketpair");
        let c = std::thread::spawn(move || {
            let (_, raw) = Hello::read_from(&mut b)?;
            let mut auth = encode_auth(
                &raw,
                &Credentials::new("cluster", SECRET),
                &client_endpoint(),
            )?;
            // Flip a byte of the GID (header 7 + key len 1 + "cluster" 7 + lid 2).
            auth[7 + 1 + 7 + 2] ^= 1;
            b.write_all(&auth)?;
            read_verdict(&mut b, 4)
        });
        let hello = Hello::new(8, 0, [9; 16]).expect("hello");
        let r = serve(
            &mut a,
            &hello,
            Some(&verifier()),
            |_, _| -> Result<(Endpoint, ()), String> { panic!("build must not run") },
        );
        assert!(matches!(r, Err(BootstrapError::Denied)));
        assert!(matches!(
            c.join().expect("join"),
            Err(BootstrapError::Rejected(RejectReason::Credentials))
        ));
    }

    // The reason byte both ways across versions: a server that predates
    // it sends the bare header and closes, which reads as Unspecified (a
    // credential refusal, as before); a client that predates it reads
    // the 7-byte header as REJECT and stops, never misreading the byte.
    #[test]
    fn reject_reasons_survive_version_skew() {
        for (reason, code) in [
            (RejectReason::Credentials, Some(1u8)),
            (RejectReason::Setup, Some(2)),
            (RejectReason::Malformed, Some(3)),
            (RejectReason::Unspecified, None),
        ] {
            let wire = encode_reject(reason);
            assert_eq!(wire.len(), HEADER_LEN + usize::from(code.is_some()));
            assert_eq!(wire.get(HEADER_LEN).copied(), code);
            // What an older client reads: the header, kind REJECT.
            let mut raw = Vec::new();
            assert_eq!(
                read_header(&mut &wire[..], &mut raw).expect("header"),
                KIND_REJECT
            );
            // What this client reads, after ACCEPT is expected or HELLO.
            assert!(
                matches!(read_verdict(&mut &wire[..], 1), Err(BootstrapError::Rejected(r)) if r == reason)
            );
            assert!(
                matches!(Hello::read_from(&mut &wire[..]), Err(BootstrapError::Rejected(r)) if r == reason)
            );
        }
        // An older server: bare header, then end of stream.
        let bare = header(KIND_REJECT);
        assert!(matches!(
            read_verdict(&mut &bare[..], 1),
            Err(BootstrapError::Rejected(RejectReason::Unspecified))
        ));
        assert!(
            is_rejected(&BootstrapError::Rejected(RejectReason::Unspecified).to_string()),
            "a reasonless REJECT still counts as a credential refusal"
        );
        // A reason from a newer server this build does not know.
        let mut newer = header(KIND_REJECT);
        newer.push(200);
        assert!(matches!(
            read_verdict(&mut &newer[..], 1),
            Err(BootstrapError::Rejected(RejectReason::Unspecified))
        ));
        for r in [RejectReason::Setup, RejectReason::Malformed] {
            assert!(
                !is_rejected(&BootstrapError::Rejected(r).to_string()),
                "{r:?}"
            );
        }
    }

    // A captured AUTH answers one challenge only: replaying it to a new
    // connection (new challenge) is refused.
    #[test]
    fn captured_auth_does_not_verify_against_a_new_challenge() {
        let creds = Credentials::new("cluster", SECRET);
        let first = Hello::new(8, 0, [9; 16]).expect("hello").encode();
        let captured = encode_auth(&first, &creds, &client_endpoint()).expect("auth");
        let (mut a, mut b) = UnixStream::pair().expect("socketpair");
        let c = std::thread::spawn(move || -> Result<Endpoint, BootstrapError> {
            Hello::read_from(&mut b)?;
            b.write_all(&captured)?;
            read_verdict(&mut b, 4)
        });
        let second = Hello::new(8, 0, [9; 16]).expect("hello");
        assert_ne!(second.encode(), first, "challenges must differ");
        let r = serve(&mut a, &second, Some(&verifier()), |_, r| {
            Ok((server_endpoint(r.qpns.len()), ()))
        });
        assert!(matches!(r, Err(BootstrapError::Denied)));
        assert!(c.join().expect("join").is_err());
    }

    // A connector that opens the stream and sends nothing is dropped
    // when the read bound expires, without building anything.
    #[test]
    fn silent_connector_times_out_without_resources() {
        let (mut a, _b) = UnixStream::pair().expect("socketpair");
        a.set_read_timeout(Some(Duration::from_millis(100)))
            .expect("timeout");
        let hello = Hello::new(8, 0, [9; 16]).expect("hello");
        let started = std::time::Instant::now();
        let r = serve(
            &mut a,
            &hello,
            Some(&verifier()),
            |_, _| -> Result<(Endpoint, ()), String> { panic!("build must not run") },
        );
        assert!(matches!(r, Err(BootstrapError::Io(_))), "{r:?}");
        assert!(started.elapsed() < Duration::from_secs(2));
    }

    #[test]
    fn version_and_foreign_peers_fail_clearly() {
        // A v1 server opened with a raw 42-byte QP record.
        let mut v1 = std::io::Cursor::new(vec![0x11u8; 42]);
        assert!(matches!(
            Hello::read_from(&mut v1),
            Err(BootstrapError::NotNvstor)
        ));
        // Same magic, another version.
        let mut other = Hello::new(1, 0, [0; 16]).expect("hello").encode();
        other[4..6].copy_from_slice(&2u16.to_le_bytes());
        let e = Hello::read_from(&mut std::io::Cursor::new(other)).expect_err("version");
        assert!(matches!(e, BootstrapError::Version(2)));
        assert!(
            e.to_string().contains("v2") && e.to_string().contains(&format!("v{PROTOCOL_VERSION}"))
        );
    }

    #[test]
    fn lane_counts_are_bounded() {
        let creds = Credentials::new("cluster", SECRET);
        let raw = Hello::new(2, 0, [0; 16]).expect("hello").encode();
        let none = Endpoint {
            lid: 0,
            gid: [0; 16],
            qpns: vec![],
        };
        assert!(encode_auth(&raw, &creds, &none).is_err());
        let many = Endpoint {
            lid: 0,
            gid: [0; 16],
            qpns: vec![1; MAX_LANES + 1],
        };
        assert!(encode_auth(&raw, &creds, &many).is_err());
        // More lanes than the server offered: refused before build.
        let (mut a, b) = UnixStream::pair().expect("socketpair");
        let c = std::thread::spawn(move || {
            client(b, Credentials::new("cluster", SECRET), client_endpoint())
        });
        let hello = Hello::new(2, 0, [9; 16]).expect("hello");
        let r = serve(
            &mut a,
            &hello,
            Some(&verifier()),
            |_, _| -> Result<(Endpoint, ()), String> { panic!("build must not run") },
        );
        assert!(matches!(r, Err(BootstrapError::Malformed(_))));
        let refused = c.join().expect("join");
        assert!(
            matches!(
                refused,
                Err(BootstrapError::Rejected(RejectReason::Malformed))
            ),
            "{refused:?}"
        );
        assert!(
            !is_rejected(&refused.expect_err("rejected").to_string()),
            "not a credential refusal"
        );
    }

    #[test]
    fn setup_failure_is_reported_to_the_client() {
        let (mut a, b) = UnixStream::pair().expect("socketpair");
        let c = std::thread::spawn(move || {
            client(b, Credentials::new("cluster", SECRET), client_endpoint())
        });
        let hello = Hello::new(8, 0, [9; 16]).expect("hello");
        let r = serve(
            &mut a,
            &hello,
            Some(&verifier()),
            |_, _| -> Result<(Endpoint, ()), String> { Err("no QPs left".into()) },
        );
        assert!(matches!(r, Err(BootstrapError::Setup(_))));
        let refused = c.join().expect("join");
        assert!(
            matches!(refused, Err(BootstrapError::Rejected(RejectReason::Setup))),
            "{refused:?}"
        );
        assert!(
            !is_rejected(&refused.expect_err("rejected").to_string()),
            "a setup failure is retryable"
        );
    }

    // The bootstrap MAC and a request signature never share an input:
    // the same secret over the "same" bytes gives different MACs.
    #[test]
    fn bootstrap_mac_is_domain_separated_from_request_signatures() {
        let raw = Hello::new(1, 0, [0; 16]).expect("hello").encode();
        let creds = Credentials::new("k", SECRET);
        let ep = Endpoint {
            lid: 0,
            gid: [0; 16],
            qpns: vec![1],
        };
        let auth = encode_auth(&raw, &creds, &ep).expect("auth");
        let mac: [u8; SIGNATURE_LEN] = auth[auth.len() - SIGNATURE_LEN..].try_into().expect("mac");
        let signed = &auth[..auth.len() - SIGNATURE_LEN];
        let mut body = raw.clone();
        body.extend_from_slice(signed);
        assert_ne!(crate::auth::sign(SECRET, 0, 0, &body), mac);
        assert!(HandshakeAuth {
            access_key: "k",
            hello: &raw,
            signed,
            mac: &mac
        }
        .verify(SECRET));
    }

    // An authenticated connector the admitter has no room for gets
    // BUSY (not REJECT, not a timeout) before `build` runs; an admitted
    // one gets its claim back, and dropping the claim frees the room.
    #[test]
    fn admitter_refuses_with_busy_before_any_resource_is_built() {
        use std::sync::atomic::AtomicUsize;
        struct Slot(Arc<AtomicUsize>);
        impl Drop for Slot {
            fn drop(&mut self) {
                self.0.fetch_add(1, Ordering::SeqCst);
            }
        }
        let room = Arc::new(AtomicUsize::new(1));
        let admitter: SessionAdmitter = {
            let room = Arc::clone(&room);
            Arc::new(move |key: &str, role| {
                assert_eq!((key, role), ("client", PeerRole::Client));
                room.fetch_update(Ordering::SeqCst, Ordering::SeqCst, |n| n.checked_sub(1))
                    .ok()
                    .map(|_| Box::new(Slot(Arc::clone(&room))) as SessionClaim)
            })
        };
        type Attempt = (
            Result<(PeerRole, Option<SessionClaim>, ()), BootstrapError>,
            ClientSide,
            bool,
        );
        let attempt = |creds: Credentials| -> Attempt {
            let (mut a, b) = UnixStream::pair().expect("socketpair");
            let c = std::thread::spawn(move || client(b, creds, client_endpoint()));
            let built = AtomicBool::new(false);
            let hello = Hello::new(8, 0, [9; 16]).expect("hello");
            let r = serve_admitted(
                &mut a,
                &hello,
                Some(&verifier()),
                Some(&admitter),
                |_, remote| {
                    built.store(true, Ordering::SeqCst);
                    Ok((server_endpoint(remote.qpns.len()), ()))
                },
            );
            drop(a);
            (r, c.join().expect("client"), built.load(Ordering::SeqCst))
        };
        let (first, client_ok, built) = attempt(Credentials::new("client", "client-secret"));
        let (role, claim, ()) = first.expect("admitted");
        assert_eq!(role, PeerRole::Client);
        assert!(claim.is_some() && client_ok.is_ok() && built);

        let (second, refused, built) = attempt(Credentials::new("client", "client-secret"));
        assert!(matches!(second, Err(BootstrapError::Busy)), "{second:?}");
        let refused = refused.expect_err("client sees BUSY");
        assert!(matches!(refused, BootstrapError::Busy));
        assert!(is_busy(&refused.to_string()), "{refused}");
        assert!(!built, "build ran for a connector over the limit");

        // An unauthenticated connector is REJECTed, never told BUSY, and
        // never reaches the admitter.
        let (bad, bad_client, _) = attempt(Credentials::new("client", "wrong"));
        assert!(matches!(bad, Err(BootstrapError::Denied)));
        assert!(matches!(
            bad_client,
            Err(BootstrapError::Rejected(RejectReason::Credentials))
        ));
        assert!(!is_busy(
            &BootstrapError::Rejected(RejectReason::Credentials).to_string()
        ));
        assert!(is_rejected(
            &BootstrapError::Rejected(RejectReason::Credentials).to_string()
        ));
        assert!(!is_rejected(&BootstrapError::Busy.to_string()));
        assert!(!is_rejected(&BootstrapError::Denied.to_string()));

        drop(claim);
        let (third, _, built) = attempt(Credentials::new("client", "client-secret"));
        assert!(third.is_ok() && built, "dropping the claim frees the slot");
    }

    #[test]
    fn gate_caps_handshakes_peers_and_sources() {
        let gate = AcceptGate::new(AcceptLimits {
            max_handshakes: 2,
            max_peers: 4,
            max_peers_per_source: 2,
            handshake_timeout: Duration::from_secs(1),
        });
        let a: IpAddr = "10.0.0.1".parse().expect("ip");
        let b: IpAddr = "10.0.0.2".parse().expect("ip");
        let mut t1 = gate.try_admit(a).expect("first");
        let t2 = gate.try_admit(b).expect("second");
        assert_eq!(
            gate.try_admit(b).err(),
            Some("too many handshakes in progress")
        );
        t1.establish();
        assert_eq!(gate.counts(), (1, 1));
        let mut t3 = gate.try_admit(a).expect("a's second");
        t3.establish();
        assert_eq!(gate.counts(), (1, 2));
        assert_eq!(
            gate.try_admit(a).err(),
            Some("per-source peer limit reached")
        );
        let mut t4 = gate.try_admit(b).expect("b's second");
        t4.establish();
        assert_eq!(gate.try_admit(b).err(), Some("peer limit reached"));
        drop(t2); // a handshake that failed
        drop(t1); // a peer that disconnected
        assert_eq!(gate.counts(), (0, 2));
        let t5 = gate.try_admit(a).expect("a has room again");
        let t6 = gate.try_admit(b).expect("b has room again");
        assert_eq!(gate.counts(), (2, 2));
        drop((t3, t4, t5, t6));
        assert_eq!(gate.counts(), (0, 0));
        assert!(
            gate.state.lock().per_source.is_empty(),
            "per-source entries are released"
        );
    }

    // Per address (IPv4-mapped as IPv4), and an IPv6 /64 as a whole up to
    // PREFIX_FACTOR times that: hosts sharing a cloud subnet's /64 do not
    // starve each other, and one host cycling through its /64 is bounded.
    #[test]
    fn per_source_limit_is_per_address_with_a_cap_per_slash64() {
        let gate = AcceptGate::new(AcceptLimits {
            max_handshakes: 1000,
            max_peers: 1000,
            max_peers_per_source: 2,
            handshake_timeout: Duration::from_secs(1),
        });
        let ip = |s: &str| s.parse::<IpAddr>().expect("ip");
        let mut held = Vec::new();
        held.push(gate.try_admit(ip("fd00::1")).expect("first"));
        held.push(gate.try_admit(ip("fd00::1")).expect("second"));
        assert_eq!(
            gate.try_admit(ip("fd00::1")).err(),
            Some("per-source peer limit reached")
        );
        // Other hosts of the same /64 have their own quota, up to 8 x 2.
        for host in 2..=8u16 {
            for _ in 0..2 {
                held.push(
                    gate.try_admit(ip(&format!("fd00::{host:x}")))
                        .expect("another host of the /64"),
                );
            }
        }
        assert_eq!(
            gate.try_admit(ip("fd00::99")).err(),
            Some("per-/64 peer limit reached")
        );
        held.push(gate.try_admit(ip("fd00:0:0:1::1")).expect("another /64"));
        // IPv4 per address; v4-mapped is the same source; no /64 cap.
        held.push(gate.try_admit(ip("10.0.0.1")).expect("v4"));
        held.push(gate.try_admit(ip("::ffff:10.0.0.1")).expect("v4-mapped"));
        assert_eq!(
            gate.try_admit(ip("10.0.0.1")).err(),
            Some("per-source peer limit reached")
        );
        held.clear();
        let s = gate.state.lock();
        assert!(
            s.per_source.is_empty() && s.per_prefix.is_empty(),
            "released on drop"
        );
        drop(s);
        assert_eq!(source_key(ip("::ffff:10.1.2.3")), ip("10.1.2.3"));
        assert_eq!(prefix_key(ip("fd00::1:2:3:4")), Some(ip("fd00::")));
        assert_eq!(prefix_key(ip("::ffff:10.1.2.3")), None);
    }
}
