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

use bytes::Bytes;
use stargate_protocol::ProtocolError;
use stargate_protocol::protocol::{
    QuicBody, QuicHeader, QuicMessage, QuicTrailer, write_to_stream,
};
use stargate_protocol::stream::RecvStream;

#[test]
fn raw_message_construction_remains_public() {
    let message = QuicMessage::Body(QuicBody {
        content: Bytes::from_static(b"body"),
    });

    assert_eq!(message.to_string(), "Body");
    drop(message.to_builder().expect("public message should build"));
}

#[test]
fn public_header_and_trailer_builders_enforce_entry_limit() {
    const LIMIT: usize = 4096;
    for trailer in [false, true] {
        let message = |len| {
            let entries = vec![("x".to_owned(), "y".to_owned()); len];
            if trailer {
                QuicMessage::Trailer(QuicTrailer { entries })
            } else {
                QuicMessage::Header(QuicHeader { entries })
            }
        };
        assert!(message(LIMIT).to_builder().is_ok());
        let Err(ProtocolError::ProtocolViolation(error)) = message(LIMIT + 1).to_builder() else {
            panic!("entry count above limit must be a protocol violation")
        };
        assert_eq!(error, "too many raw QUIC tunnel headers: 4097");
    }
}

#[allow(dead_code)]
async fn raw_stream_methods_remain_public(send: &mut quinn::SendStream, recv: &mut RecvStream) {
    let message = QuicMessage::Body(QuicBody {
        content: Bytes::new(),
    });
    write_to_stream(send, message.to_builder().unwrap())
        .await
        .unwrap();
    let _ = recv.recv_any().await.unwrap();
}
