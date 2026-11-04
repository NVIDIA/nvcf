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

//! Wire protocol request/response enums for the nvstor native client.
//!
//! Messages are bincode-serialized and sent as length-prefixed frames
//! over [`TcpConnection`](nvstor_transport::tcp::TcpConnection).

use serde::{Deserialize, Serialize};
use uuid::Uuid;

/// A client-to-server request.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub enum Request {
    Ping,
    PutObject {
        key: String,
        data: Vec<u8>,
        content_type: String,
    },
    GetObject {
        key: String,
    },
    DeleteObject {
        key: String,
    },
    HeadObject {
        key: String,
    },
    ListObjects {
        prefix: String,
        max_keys: u32,
        start_after: Option<String>,
    },
    CopyObject {
        src: String,
        dst: String,
    },
    VolumeCreate {
        id: Uuid,
        name: String,
        size: u64,
        replication: u8,
    },
    VolumeDelete {
        id: Uuid,
    },
    VolumeWrite {
        id: Uuid,
        offset: u64,
        data: Vec<u8>,
    },
    VolumeRead {
        id: Uuid,
        offset: u64,
        length: u32,
    },
    VolumeInfo {
        id: Uuid,
    },
}

/// An entry in an object listing.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ObjectEntry {
    pub key: String,
    pub size: u64,
    pub etag: String,
}

/// A server-to-client response.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub enum Response {
    Ok,
    Error {
        code: u16,
        message: String,
    },
    Pong,
    Object {
        data: Vec<u8>,
        size: u64,
        content_type: String,
        etag: String,
    },
    Head {
        size: u64,
        content_type: String,
        etag: String,
    },
    ObjectList {
        entries: Vec<ObjectEntry>,
        is_truncated: bool,
    },
    PutResult {
        etag: String,
    },
    VolumeData {
        data: Vec<u8>,
    },
    VolumeInfoResult {
        size: u64,
        access_mode: u8,
        consistency: u8,
    },
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bincode_roundtrip<
        T: Serialize + serde::de::DeserializeOwned + PartialEq + std::fmt::Debug,
    >(
        val: &T,
    ) {
        let encoded = bincode::serialize(val).expect("serialize");
        let decoded: T = bincode::deserialize(&encoded).expect("deserialize");
        assert_eq!(*val, decoded);
    }

    #[test]
    fn request_ping_roundtrip() {
        bincode_roundtrip(&Request::Ping);
    }

    #[test]
    fn request_put_object_roundtrip() {
        bincode_roundtrip(&Request::PutObject {
            key: "bucket/key".into(),
            data: vec![1, 2, 3, 4],
            content_type: "application/octet-stream".into(),
        });
    }

    #[test]
    fn request_get_object_roundtrip() {
        bincode_roundtrip(&Request::GetObject {
            key: "bucket/key".into(),
        });
    }

    #[test]
    fn request_list_objects_roundtrip() {
        bincode_roundtrip(&Request::ListObjects {
            prefix: "data/".into(),
            max_keys: 1000,
            start_after: Some("data/file099".into()),
        });
    }

    #[test]
    fn request_volume_create_roundtrip() {
        bincode_roundtrip(&Request::VolumeCreate {
            id: Uuid::new_v4(),
            name: "vol-0".into(),
            size: 1024 * 1024 * 1024,
            replication: 3,
        });
    }

    #[test]
    fn request_volume_read_roundtrip() {
        bincode_roundtrip(&Request::VolumeRead {
            id: Uuid::new_v4(),
            offset: 4096,
            length: 8192,
        });
    }

    #[test]
    fn response_ok_roundtrip() {
        bincode_roundtrip(&Response::Ok);
    }

    #[test]
    fn response_error_roundtrip() {
        bincode_roundtrip(&Response::Error {
            code: 404,
            message: "not found".into(),
        });
    }

    #[test]
    fn response_pong_roundtrip() {
        bincode_roundtrip(&Response::Pong);
    }

    #[test]
    fn response_object_roundtrip() {
        bincode_roundtrip(&Response::Object {
            data: vec![0xAB; 256],
            size: 256,
            content_type: "text/plain".into(),
            etag: "abc123".into(),
        });
    }

    #[test]
    fn response_head_roundtrip() {
        bincode_roundtrip(&Response::Head {
            size: 1024,
            content_type: "application/json".into(),
            etag: "def456".into(),
        });
    }

    #[test]
    fn response_object_list_roundtrip() {
        bincode_roundtrip(&Response::ObjectList {
            entries: vec![
                ObjectEntry {
                    key: "a/b".into(),
                    size: 100,
                    etag: "e1".into(),
                },
                ObjectEntry {
                    key: "a/c".into(),
                    size: 200,
                    etag: "e2".into(),
                },
            ],
            is_truncated: true,
        });
    }

    #[test]
    fn response_put_result_roundtrip() {
        bincode_roundtrip(&Response::PutResult {
            etag: "etag-value".into(),
        });
    }

    #[test]
    fn response_volume_data_roundtrip() {
        bincode_roundtrip(&Response::VolumeData {
            data: vec![0xFF; 4096],
        });
    }

    #[test]
    fn response_volume_info_roundtrip() {
        bincode_roundtrip(&Response::VolumeInfoResult {
            size: 10 * 1024 * 1024 * 1024,
            access_mode: 0,
            consistency: 1,
        });
    }
}
