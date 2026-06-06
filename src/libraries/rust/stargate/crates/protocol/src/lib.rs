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

#[allow(dead_code, clippy::all)]
mod quic_capnp {
    include!(concat!(env!("OUT_DIR"), "/quic_capnp.rs"));
}

use serde::{Deserialize, Serialize};

pub mod common;
pub mod protocol;
pub mod stream;
pub mod tunnel_contract;
pub mod webtransport;
pub mod webtransport_http;

pub use protocol::{
    HandshakeAck, HandshakeRequest, StreamStopCode, read_handshake, read_handshake_ack,
    write_handshake, write_handshake_ack,
};
pub use stream::{RecvBodyFrame, RecvStream, SendStream};
pub use webtransport::{
    WebTransportBidiHeader, read_webtransport_bidi_header,
    write_precomputed_webtransport_bidi_header, write_webtransport_bidi_header,
};
pub use webtransport_http::{
    WebTransportHttpRequestHead, WebTransportHttpResponseHead, finish_webtransport_http_stream,
    read_webtransport_http_body_chunk, read_webtransport_http_request_head,
    read_webtransport_http_response_head, write_webtransport_http_body,
    write_webtransport_http_request_head_after_prefix, write_webtransport_http_response_head,
};

pub const HTTP3_ALPN: &[u8] = b"h3";
pub const WEBTRANSPORT_BIDI_STREAM_TYPE: u64 = 0x41;

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum TunnelTransportProtocol {
    #[default]
    Custom,
    Http3,
    WebTransport,
}

impl std::fmt::Display for TunnelTransportProtocol {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Custom => f.write_str("custom"),
            Self::Http3 => f.write_str("http3"),
            Self::WebTransport => f.write_str("webtransport"),
        }
    }
}

impl Serialize for TunnelTransportProtocol {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        serializer.serialize_str(&self.to_string())
    }
}

impl<'de> Deserialize<'de> for TunnelTransportProtocol {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let value = <&str>::deserialize(deserializer)?;
        value.parse().map_err(serde::de::Error::custom)
    }
}

impl std::str::FromStr for TunnelTransportProtocol {
    type Err = String;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        match value {
            "custom" => Ok(Self::Custom),
            "http3" | "h3" => Ok(Self::Http3),
            "webtransport" | "web-transport" | "wt" => Ok(Self::WebTransport),
            other => Err(format!(
                "unsupported tunnel protocol '{other}', expected 'custom', 'http3', or 'webtransport'"
            )),
        }
    }
}

impl TunnelTransportProtocol {
    pub fn alpn_protocols(self) -> Vec<Vec<u8>> {
        match self {
            Self::Custom => Vec::new(),
            Self::Http3 | Self::WebTransport => vec![HTTP3_ALPN.to_vec()],
        }
    }
}

#[derive(Debug, thiserror::Error)]
pub enum ProtocolError {
    #[error("stream I/O failed: {0}")]
    Io(#[from] std::io::Error),
    #[error("capnp serialization failed: {0}")]
    Capnp(#[from] capnp::Error),
    #[error("invalid header: {0}")]
    InvalidHeader(String),
    #[error("protocol violation: {0}")]
    ProtocolViolation(String),
}

#[cfg(test)]
mod build_plan;

#[cfg(test)]
#[path = "../build.rs"]
mod build_script;

#[cfg(test)]
mod tests {
    use super::*;
    use crate::build_plan::capnp_build_plan;

    #[test]
    fn capnp_build_plan_tracks_schema_and_rerun_trigger() {
        let plan = capnp_build_plan();

        assert_eq!(plan.schema_file, "quic.capnp");
        assert_eq!(plan.rerun_if_changed, "quic.capnp");
        assert_eq!(
            crate::build_script::planned_capnp_schema_file(),
            "quic.capnp"
        );
    }

    #[test]
    fn tunnel_protocol_accepts_webtransport_aliases() {
        assert_eq!(
            "webtransport".parse::<TunnelTransportProtocol>().unwrap(),
            TunnelTransportProtocol::WebTransport
        );
        assert_eq!(
            "web-transport".parse::<TunnelTransportProtocol>().unwrap(),
            TunnelTransportProtocol::WebTransport
        );
        assert_eq!(
            "wt".parse::<TunnelTransportProtocol>().unwrap(),
            TunnelTransportProtocol::WebTransport
        );
        assert_eq!(
            TunnelTransportProtocol::WebTransport.to_string(),
            "webtransport"
        );
        assert_eq!(
            TunnelTransportProtocol::WebTransport.alpn_protocols(),
            vec![HTTP3_ALPN.to_vec()]
        );
    }

    #[test]
    fn tunnel_protocol_serde_uses_canonical_display_strings() {
        assert_eq!(
            serde_json::to_string(&TunnelTransportProtocol::Http3).unwrap(),
            "\"http3\""
        );
        assert_eq!(
            serde_json::to_string(&TunnelTransportProtocol::WebTransport).unwrap(),
            "\"webtransport\""
        );
        assert_eq!(
            serde_json::from_str::<TunnelTransportProtocol>("\"h3\"").unwrap(),
            TunnelTransportProtocol::Http3
        );
        assert_eq!(
            serde_json::from_str::<TunnelTransportProtocol>("\"web-transport\"").unwrap(),
            TunnelTransportProtocol::WebTransport
        );
    }
}
