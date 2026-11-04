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

//! High-level async client for nvstor storage.

use std::net::SocketAddr;
use std::time::Duration;

use nvstor_transport::pool::{ConnectionPool, PoolConfig};
use nvstor_transport::tcp::TcpConfig;
use uuid::Uuid;

use crate::error::ClientError;
use crate::protocol::{ObjectEntry, Request, Response};

/// Result of a successful PUT operation.
#[derive(Debug, Clone, PartialEq)]
pub struct PutResult {
    pub etag: String,
}

/// Result of a successful GET operation.
#[derive(Debug, Clone, PartialEq)]
pub struct GetResult {
    pub data: Vec<u8>,
    pub size: u64,
    pub content_type: String,
    pub etag: String,
}

/// Result of a successful HEAD operation.
#[derive(Debug, Clone, PartialEq)]
pub struct HeadResult {
    pub size: u64,
    pub content_type: String,
    pub etag: String,
}

/// Result of a successful LIST operation.
#[derive(Debug, Clone, PartialEq)]
pub struct ListResult {
    pub entries: Vec<ObjectEntry>,
    pub is_truncated: bool,
}

/// Client configuration.
#[derive(Debug, Clone)]
pub struct ClientConfig {
    /// TCP connect timeout.
    pub connect_timeout: Duration,
    /// Per-operation I/O timeout.
    pub io_timeout: Duration,
    /// Max connections per server.
    pub max_connections: usize,
    /// Max in-flight requests per connection.
    pub max_in_flight: usize,
}

impl Default for ClientConfig {
    fn default() -> Self {
        Self {
            connect_timeout: Duration::from_secs(5),
            io_timeout: Duration::from_secs(30),
            max_connections: 4,
            max_in_flight: 100,
        }
    }
}

/// Native async client for nvstor storage.
///
/// Uses length-prefixed bincode frames over TCP via the transport layer's
/// [`ConnectionPool`].
#[derive(Debug)]
pub struct NvstorClient {
    pool: ConnectionPool,
    primary_addr: SocketAddr,
}

impl NvstorClient {
    /// Connect to an nvstor server using default configuration.
    pub async fn connect(addr: &str) -> Result<Self, ClientError> {
        Self::connect_with_config(addr, ClientConfig::default()).await
    }

    /// Connect to an nvstor server with custom configuration.
    pub async fn connect_with_config(
        addr: &str,
        config: ClientConfig,
    ) -> Result<Self, ClientError> {
        let primary_addr: SocketAddr = addr
            .parse()
            .map_err(|_| ClientError::InvalidAddress(addr.to_string()))?;

        let pool_config = PoolConfig {
            tcp: TcpConfig {
                connect_timeout: config.connect_timeout,
                io_timeout: config.io_timeout,
                ..TcpConfig::default()
            },
            max_connections_per_peer: config.max_connections,
            max_in_flight: config.max_in_flight,
            ..PoolConfig::default()
        };

        let pool = ConnectionPool::new(pool_config);

        Ok(Self { pool, primary_addr })
    }

    // ----- Object API -----

    /// Store an object.
    pub async fn put(
        &self,
        key: &str,
        data: &[u8],
        content_type: &str,
    ) -> Result<PutResult, ClientError> {
        let resp = self
            .roundtrip(Request::PutObject {
                key: key.to_string(),
                data: data.to_vec(),
                content_type: content_type.to_string(),
            })
            .await?;
        match resp {
            Response::PutResult { etag } => Ok(PutResult { etag }),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    /// Retrieve an object.
    pub async fn get(&self, key: &str) -> Result<GetResult, ClientError> {
        let resp = self
            .roundtrip(Request::GetObject {
                key: key.to_string(),
            })
            .await?;
        match resp {
            Response::Object {
                data,
                size,
                content_type,
                etag,
            } => Ok(GetResult {
                data,
                size,
                content_type,
                etag,
            }),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    /// Delete an object.
    pub async fn delete(&self, key: &str) -> Result<(), ClientError> {
        let resp = self
            .roundtrip(Request::DeleteObject {
                key: key.to_string(),
            })
            .await?;
        match resp {
            Response::Ok => Ok(()),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    /// Get object metadata without the body.
    pub async fn head(&self, key: &str) -> Result<HeadResult, ClientError> {
        let resp = self
            .roundtrip(Request::HeadObject {
                key: key.to_string(),
            })
            .await?;
        match resp {
            Response::Head {
                size,
                content_type,
                etag,
            } => Ok(HeadResult {
                size,
                content_type,
                etag,
            }),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    /// List objects by prefix.
    pub async fn list(&self, prefix: &str, max_keys: u32) -> Result<ListResult, ClientError> {
        let resp = self
            .roundtrip(Request::ListObjects {
                prefix: prefix.to_string(),
                max_keys,
                start_after: None,
            })
            .await?;
        match resp {
            Response::ObjectList {
                entries,
                is_truncated,
            } => Ok(ListResult {
                entries,
                is_truncated,
            }),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    /// Copy an object server-side.
    pub async fn copy(&self, src: &str, dst: &str) -> Result<(), ClientError> {
        let resp = self
            .roundtrip(Request::CopyObject {
                src: src.to_string(),
                dst: dst.to_string(),
            })
            .await?;
        match resp {
            Response::Ok => Ok(()),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    // ----- Volume API -----

    /// Create a new volume.
    pub async fn volume_create(&self, id: Uuid, name: &str, size: u64) -> Result<(), ClientError> {
        let resp = self
            .roundtrip(Request::VolumeCreate {
                id,
                name: name.to_string(),
                size,
                replication: 1,
            })
            .await?;
        match resp {
            Response::Ok => Ok(()),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    /// Write data to a volume at the given offset.
    pub async fn volume_write(
        &self,
        id: Uuid,
        offset: u64,
        data: &[u8],
    ) -> Result<(), ClientError> {
        let resp = self
            .roundtrip(Request::VolumeWrite {
                id,
                offset,
                data: data.to_vec(),
            })
            .await?;
        match resp {
            Response::Ok => Ok(()),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    /// Read data from a volume.
    pub async fn volume_read(
        &self,
        id: Uuid,
        offset: u64,
        length: u32,
    ) -> Result<Vec<u8>, ClientError> {
        let resp = self
            .roundtrip(Request::VolumeRead { id, offset, length })
            .await?;
        match resp {
            Response::VolumeData { data } => Ok(data),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    // ----- Utility -----

    /// Ping the server.
    pub async fn ping(&self) -> Result<(), ClientError> {
        let resp = self.roundtrip(Request::Ping).await?;
        match resp {
            Response::Pong => Ok(()),
            Response::Error { code, message } => Err(ClientError::Server { code, message }),
            _ => Err(ClientError::UnexpectedResponse),
        }
    }

    // ----- Internal -----

    /// Send a request and receive a response (single round-trip).
    async fn roundtrip(&self, req: Request) -> Result<Response, ClientError> {
        let payload = bincode::serialize(&req)?;
        let raw_response = self.pool.send(self.primary_addr, &payload).await?;
        let resp: Response = bincode::deserialize(&raw_response)?;
        Ok(resp)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{Request, Response};
    use std::sync::Arc;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpListener;

    /// Start a mock server that decodes a Request, runs `handler`, and sends the Response.
    async fn mock_server<F>(handler: F) -> SocketAddr
    where
        F: Fn(Request) -> Response + Send + Sync + 'static,
    {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        let handler = Arc::new(handler);
        tokio::spawn(async move {
            loop {
                let (mut stream, _) = listener.accept().await.unwrap();
                let handler = handler.clone();
                tokio::spawn(async move {
                    loop {
                        // Read length-prefixed frame.
                        let mut header = [0u8; 4];
                        if stream.read_exact(&mut header).await.is_err() {
                            break;
                        }
                        let len = u32::from_le_bytes(header) as usize;
                        let mut payload = vec![0u8; len];
                        if stream.read_exact(&mut payload).await.is_err() {
                            break;
                        }
                        let req: Request = bincode::deserialize(&payload).unwrap();
                        let resp = handler(req);
                        let resp_bytes = bincode::serialize(&resp).unwrap();
                        let resp_header = (resp_bytes.len() as u32).to_le_bytes();
                        let _ = stream.write_all(&resp_header).await;
                        let _ = stream.write_all(&resp_bytes).await;
                        let _ = stream.flush().await;
                    }
                });
            }
        });
        addr
    }

    #[tokio::test]
    async fn test_ping() {
        let addr = mock_server(|req| match req {
            Request::Ping => Response::Pong,
            _ => Response::Error {
                code: 400,
                message: "unexpected".into(),
            },
        })
        .await;

        let client = NvstorClient::connect(&addr.to_string()).await.unwrap();
        client.ping().await.unwrap();
    }

    #[tokio::test]
    async fn test_put_get_delete() {
        let addr = mock_server(|req| match req {
            Request::PutObject { key, data, .. } => Response::PutResult {
                etag: format!("etag-{}-{}", key, data.len()),
            },
            Request::GetObject { key } => Response::Object {
                data: b"hello".to_vec(),
                size: 5,
                content_type: "text/plain".into(),
                etag: format!("etag-{}-5", key),
            },
            Request::DeleteObject { .. } => Response::Ok,
            _ => Response::Error {
                code: 400,
                message: "unexpected".into(),
            },
        })
        .await;

        let client = NvstorClient::connect(&addr.to_string()).await.unwrap();

        let put = client.put("mykey", b"hello", "text/plain").await.unwrap();
        assert_eq!(put.etag, "etag-mykey-5");

        let get = client.get("mykey").await.unwrap();
        assert_eq!(get.data, b"hello");
        assert_eq!(get.size, 5);

        client.delete("mykey").await.unwrap();
    }

    #[tokio::test]
    async fn test_head() {
        let addr = mock_server(|req| match req {
            Request::HeadObject { .. } => Response::Head {
                size: 1024,
                content_type: "application/octet-stream".into(),
                etag: "etag-head".into(),
            },
            _ => Response::Error {
                code: 400,
                message: "unexpected".into(),
            },
        })
        .await;

        let client = NvstorClient::connect(&addr.to_string()).await.unwrap();
        let head = client.head("key").await.unwrap();
        assert_eq!(head.size, 1024);
        assert_eq!(head.etag, "etag-head");
    }

    #[tokio::test]
    async fn test_list() {
        let addr = mock_server(|req| match req {
            Request::ListObjects {
                prefix,
                max_keys: _,
                ..
            } => Response::ObjectList {
                entries: vec![crate::protocol::ObjectEntry {
                    key: format!("{prefix}file1"),
                    size: 100,
                    etag: "e1".into(),
                }],
                is_truncated: false,
            },
            _ => Response::Error {
                code: 400,
                message: "unexpected".into(),
            },
        })
        .await;

        let client = NvstorClient::connect(&addr.to_string()).await.unwrap();
        let list = client.list("data/", 100).await.unwrap();
        assert_eq!(list.entries.len(), 1);
        assert!(!list.is_truncated);
    }

    #[tokio::test]
    async fn test_server_error_propagation() {
        let addr = mock_server(|_req| Response::Error {
            code: 404,
            message: "not found".into(),
        })
        .await;

        let client = NvstorClient::connect(&addr.to_string()).await.unwrap();
        let err = client.get("missing").await.unwrap_err();
        match err {
            ClientError::Server { code, message } => {
                assert_eq!(code, 404);
                assert_eq!(message, "not found");
            }
            other => panic!("expected Server error, got: {other}"),
        }
    }

    #[tokio::test]
    async fn test_connect_with_config() {
        let addr = mock_server(|_| Response::Pong).await;
        let config = ClientConfig {
            connect_timeout: Duration::from_secs(2),
            io_timeout: Duration::from_secs(10),
            max_connections: 2,
            max_in_flight: 50,
        };
        let client = NvstorClient::connect_with_config(&addr.to_string(), config)
            .await
            .unwrap();
        client.ping().await.unwrap();
    }

    #[tokio::test]
    async fn test_invalid_address() {
        let err = NvstorClient::connect("not-an-address").await.unwrap_err();
        assert!(matches!(err, ClientError::InvalidAddress(_)));
    }

    #[tokio::test]
    async fn test_volume_create_write_read() {
        let addr = mock_server(|req| match req {
            Request::VolumeCreate { .. } => Response::Ok,
            Request::VolumeWrite { .. } => Response::Ok,
            Request::VolumeRead { length, .. } => Response::VolumeData {
                data: vec![0xAB; length as usize],
            },
            _ => Response::Error {
                code: 400,
                message: "unexpected".into(),
            },
        })
        .await;

        let client = NvstorClient::connect(&addr.to_string()).await.unwrap();
        let vol_id = Uuid::new_v4();

        client
            .volume_create(vol_id, "test-vol", 1024 * 1024)
            .await
            .unwrap();
        client.volume_write(vol_id, 0, &[1, 2, 3, 4]).await.unwrap();
        let data = client.volume_read(vol_id, 0, 4096).await.unwrap();
        assert_eq!(data.len(), 4096);
    }

    #[test]
    fn result_types_are_complete() {
        // Verify result structs have the expected fields.
        let put = PutResult { etag: "e".into() };
        assert_eq!(put.etag, "e");

        let get = GetResult {
            data: vec![],
            size: 0,
            content_type: String::new(),
            etag: String::new(),
        };
        assert_eq!(get.size, 0);

        let head = HeadResult {
            size: 42,
            content_type: "ct".into(),
            etag: "et".into(),
        };
        assert_eq!(head.size, 42);

        let list = ListResult {
            entries: vec![],
            is_truncated: false,
        };
        assert!(!list.is_truncated);
    }

    #[test]
    fn error_types_complete() {
        // Verify all error variants are constructible.
        let _ = ClientError::Io(std::io::Error::new(std::io::ErrorKind::Other, "test"));
        let _ = ClientError::Server {
            code: 500,
            message: "internal".into(),
        };
        let _ = ClientError::UnexpectedResponse;
        let _ = ClientError::InvalidAddress("bad".into());

        // Verify Display works.
        let err = ClientError::Server {
            code: 404,
            message: "not found".into(),
        };
        let msg = format!("{err}");
        assert!(msg.contains("404"));
        assert!(msg.contains("not found"));
    }
}
