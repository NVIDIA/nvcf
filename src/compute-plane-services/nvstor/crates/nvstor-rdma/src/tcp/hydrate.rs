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

//! S3-over-TCP [`RangeSource`] for [`crate::hydrate`]: one TCP
//! connection per worker, ranges read into a reusable buffer and
//! pwritten from there. A transport failure replaces the socket.

use std::path::Path;
use std::sync::Arc;

use crate::auth::Credentials;
use crate::hydrate::RangeSource;
pub use crate::hydrate::{HydrateError, HydrateOptions, HydrateReport};
use crate::object::GetError;
use crate::tcp::client::S3TcpClient;
use crate::tcp::proto::MAX_GET_RANGE;
use crate::tcp::transport::TcpTransport;

/// One worker's connection to an S3-over-TCP server.
pub struct TcpSource {
    addr: String,
    creds: Credentials,
    transport: Arc<TcpTransport>,
    client: S3TcpClient,
    buf: Vec<u8>,
}

impl TcpSource {
    pub fn open(addr: &str, creds: &Credentials, range_size: usize) -> Result<Self, HydrateError> {
        let (transport, client) = Self::dial(addr, creds)?;
        let buf = vec![0u8; range_size.min(MAX_GET_RANGE as usize)];
        Ok(Self {
            addr: addr.to_string(),
            creds: creds.clone(),
            transport,
            client,
            buf,
        })
    }

    fn dial(
        addr: &str,
        creds: &Credentials,
    ) -> Result<(Arc<TcpTransport>, S3TcpClient), HydrateError> {
        let transport = TcpTransport::new();
        transport.set_credentials(creds.clone());
        let peer = transport
            .connect(addr)
            .map_err(|e| HydrateError::Connect(format!("{addr}: {e}")))?;
        let client = S3TcpClient::new(Arc::clone(&transport), peer);
        Ok((transport, client))
    }
}

impl RangeSource for TcpSource {
    fn max_range(&self) -> usize {
        self.buf.len()
    }

    fn stat(&mut self, key: &str) -> Result<(u64, u128), GetError> {
        self.client.stat(key)
    }

    fn fetch(
        &mut self,
        key: &str,
        offset: u64,
        generation: u128,
        len: usize,
    ) -> Result<&[u8], GetError> {
        let len = len.min(self.buf.len());
        let r = self
            .client
            .get_range_into(key, offset, generation, &mut self.buf[..len])?;
        Ok(&self.buf[..r.len])
    }

    fn reconnect(&mut self) -> Result<(), HydrateError> {
        let (transport, client) = Self::dial(&self.addr, &self.creds)?;
        self.transport.shutdown();
        self.transport = transport;
        self.client = client;
        Ok(())
    }
}

impl Drop for TcpSource {
    fn drop(&mut self) {
        self.transport.shutdown();
    }
}

/// Fetch `key` from the S3-over-TCP server at `addr` into `dest`; see
/// [`crate::hydrate`] for the integrity and durability contract.
pub fn hydrate_to_file(
    addr: &str,
    creds: &Credentials,
    key: &str,
    dest: &Path,
    opts: &HydrateOptions,
) -> Result<HydrateReport, HydrateError> {
    crate::hydrate::hydrate_to_file(
        || TcpSource::open(addr, creds, opts.range_size),
        key,
        dest,
        opts,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::hydrate::partial_path;
    use crate::object::{DeleteFn, ReadFn, WriteFn};
    use crate::tcp::server::S3TcpServer;
    use std::io;
    use std::io::{Read, Write};
    use std::net::{TcpListener, TcpStream};
    use std::sync::atomic::AtomicUsize;
    use std::sync::atomic::Ordering;
    use std::time::Duration;

    fn creds() -> Credentials {
        Credentials::new("AKIAHYDRATE", "hydrate-secret")
    }

    fn allow() -> crate::auth::AuthFn {
        Arc::new(|ctx: &crate::auth::AuthContext| {
            let c = creds();
            if ctx.header.access_key == c.access_key
                && crate::auth::verify_signature(
                    &c.secret_key,
                    ctx.header.timestamp,
                    ctx.header.nonce,
                    ctx.body,
                    &ctx.header.signature,
                )
            {
                Ok("hydrate-test".into())
            } else {
                Err(crate::auth::AuthError::BadSignature)
            }
        })
    }

    /// Serve `fns`; returns the server transport and its loopback address.
    fn serve(fns: (WriteFn, ReadFn, DeleteFn)) -> (Arc<TcpTransport>, String) {
        let t = TcpTransport::new();
        let put = crate::object::buffered_put(fns.0);
        let server = S3TcpServer::new(put, fns.1, fns.2, Arc::clone(&t)).with_auth(allow());
        server.start();
        let addr = t.listen(0).expect("listen");
        (t, format!("127.0.0.1:{}", addr.port()))
    }

    fn pattern(len: usize) -> Vec<u8> {
        (0..len as u32)
            .map(|i| (i.wrapping_mul(2_654_435_761) >> 9) as u8)
            .collect()
    }

    fn opts(connections: usize, range_size: usize) -> HydrateOptions {
        HydrateOptions {
            connections,
            range_size,
            max_retries: 5,
            retry_backoff: Duration::from_millis(5),
            ..HydrateOptions::default()
        }
    }

    #[test]
    fn hydrates_multi_range_object_in_parallel() {
        let fns = crate::object::mem_store();
        let seed = Arc::clone(&fns.0);
        let (server, addr) = serve(fns);
        let data = pattern(48 * 1024 * 1024 + 4097);
        seed("model/shard-0", &data).expect("seed");

        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("shard-0.safetensors");
        let report = hydrate_to_file(
            &addr,
            &creds(),
            "model/shard-0",
            &dest,
            &opts(4, 4 * 1024 * 1024),
        )
        .expect("hydrate");
        assert_eq!(report.bytes, data.len() as u64);
        assert_eq!(report.ranges, 13);
        assert_eq!((report.retries, report.reconnects), (0, 0));
        assert_eq!(std::fs::read(&dest).expect("read dest"), data);
        assert!(!partial_path(&dest).exists());
        server.shutdown();
    }

    #[test]
    fn hydrates_empty_object() {
        let fns = crate::object::mem_store();
        let seed = Arc::clone(&fns.0);
        let (server, addr) = serve(fns);
        seed("empty", b"").expect("seed");
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("empty");
        let report =
            hydrate_to_file(&addr, &creds(), "empty", &dest, &opts(4, 1024)).expect("hydrate");
        assert_eq!((report.bytes, report.ranges), (0, 0));
        assert_eq!(std::fs::read(&dest).expect("read").len(), 0);
        server.shutdown();
    }

    #[test]
    fn missing_key_fails_without_creating_files() {
        let (server, addr) = serve(crate::object::mem_store());
        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("nope");
        let err = hydrate_to_file(&addr, &creds(), "nope", &dest, &opts(2, 1024)).unwrap_err();
        assert!(
            matches!(
                err,
                HydrateError::Get {
                    error: GetError::NotFound,
                    ..
                }
            ),
            "{err}"
        );
        assert!(!dest.exists() && !partial_path(&dest).exists());
        server.shutdown();
    }

    /// The object is replaced after the transfer starts: hydration fails
    /// with GenerationChanged and leaves neither the final file nor a
    /// partial.
    #[test]
    fn replace_mid_transfer_fails_cleanly() {
        let (write_fn, read_fn, delete_fn) = crate::object::mem_store();
        let replacer = Arc::clone(&write_fn);
        let reads = Arc::new(AtomicUsize::new(0));
        let counted = Arc::clone(&reads);
        let inner = Arc::clone(&read_fn);
        let read_fn: ReadFn = Arc::new(move |k, off, buf| {
            // Read #4 is a few ranges in (two reads per range: stat + data).
            if counted.fetch_add(1, Ordering::SeqCst) == 4 {
                replacer(k, &pattern(1024)).map_err(io::Error::other)?;
            }
            inner(k, off, buf)
        });
        let (server, addr) = serve((Arc::clone(&write_fn), read_fn, delete_fn));
        write_fn("obj", &pattern(8 * 1024 * 1024)).expect("seed");

        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("obj");
        let err =
            hydrate_to_file(&addr, &creds(), "obj", &dest, &opts(1, 1024 * 1024)).unwrap_err();
        assert!(
            matches!(
                err,
                HydrateError::Get {
                    error: GetError::GenerationChanged,
                    ..
                }
            ),
            "{err}"
        );
        assert!(!dest.exists() && !partial_path(&dest).exists());
        server.shutdown();
    }

    /// A proxy cuts the first two connections mid-stream. The workers
    /// reconnect, retry only the interrupted ranges, and the file is
    /// still byte-identical.
    #[test]
    fn reconnects_after_connection_drop() {
        let fns = crate::object::mem_store();
        let seed = Arc::clone(&fns.0);
        let (server, upstream) = serve(fns);
        let data = pattern(24 * 1024 * 1024);
        seed("obj", &data).expect("seed");

        let listener = TcpListener::bind("127.0.0.1:0").expect("proxy bind");
        let proxy_addr = listener.local_addr().expect("addr").to_string();
        std::thread::spawn(move || {
            for (n, down) in listener.incoming().enumerate() {
                let Ok(down) = down else { return };
                let Ok(up) = TcpStream::connect(&upstream) else {
                    return;
                };
                // Connections 0 and 1 die after ~5 MiB of response bytes.
                let budget = if n < 2 { 5 * 1024 * 1024 } else { usize::MAX };
                pipe(
                    down.try_clone().expect("clone"),
                    up.try_clone().expect("clone"),
                    usize::MAX,
                );
                pipe(up, down, budget);
            }
        });

        let dir = tempfile::tempdir().expect("tempdir");
        let dest = dir.path().join("obj");
        let report = hydrate_to_file(
            &proxy_addr,
            &creds(),
            "obj",
            &dest,
            &opts(2, 2 * 1024 * 1024),
        )
        .expect("hydrate survives drops");
        assert!(report.reconnects >= 1, "{report:?}");
        assert_eq!(std::fs::read(&dest).expect("read"), data);
        server.shutdown();
    }

    /// Copy `from` → `to` until EOF or `budget` bytes, then shut both down.
    fn pipe(mut from: TcpStream, mut to: TcpStream, budget: usize) {
        std::thread::spawn(move || {
            let mut buf = vec![0u8; 64 * 1024];
            let mut left = budget;
            while left > 0 {
                let chunk = buf.len().min(left);
                let n = match from.read(&mut buf[..chunk]) {
                    Ok(0) | Err(_) => break,
                    Ok(n) => n,
                };
                if to.write_all(&buf[..n]).is_err() {
                    break;
                }
                left -= n;
            }
            let _ = from.shutdown(std::net::Shutdown::Both);
            let _ = to.shutdown(std::net::Shutdown::Both);
        });
    }

    #[test]
    fn unreachable_server_is_a_connect_error() {
        let dir = tempfile::tempdir().expect("tempdir");
        // Bind then drop to get a port nothing listens on.
        let port = TcpListener::bind("127.0.0.1:0")
            .expect("bind")
            .local_addr()
            .expect("addr")
            .port();
        let err = hydrate_to_file(
            &format!("127.0.0.1:{port}"),
            &creds(),
            "k",
            &dir.path().join("x"),
            &opts(1, 1024),
        )
        .unwrap_err();
        assert!(matches!(err, HydrateError::Connect(_)), "{err}");
    }
}
