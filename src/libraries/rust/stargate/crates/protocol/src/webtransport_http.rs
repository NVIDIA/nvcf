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

use crate::ProtocolError;
use crate::common::{append_header_entry_bytes, header_value_to_str};
use bytes::Bytes;
use http::{HeaderMap, Method, StatusCode};

// WebTransport carries an HTTP-like head followed by raw body bytes on each
// bidirectional stream. Keep this codec independent from the custom QUIC
// Cap'n Proto tunnel framing.
const WEBTRANSPORT_HTTP_MAGIC: &[u8; 8] = b"SGWTHTP1";
const WEBTRANSPORT_HTTP_KIND_REQUEST: u8 = 1;
const WEBTRANSPORT_HTTP_KIND_RESPONSE: u8 = 2;
const WEBTRANSPORT_HTTP_PREFIX_LEN: usize = WEBTRANSPORT_HTTP_MAGIC.len() + 1 + 4;
const MAX_WEBTRANSPORT_HTTP_HEAD_LEN: usize = 1024 * 1024;
const MAX_WEBTRANSPORT_HTTP_HEADER_COUNT: u32 = 4096;
const MIN_WEBTRANSPORT_HTTP_HEADER_ENTRY_LEN: usize = 2 + 4;

#[derive(Debug, Clone)]
pub struct WebTransportHttpRequestHead {
    pub method: Method,
    pub path_and_query: String,
    pub headers: HeaderMap,
}

#[derive(Debug, Clone)]
pub struct WebTransportHttpResponseHead {
    pub status: StatusCode,
    pub headers: HeaderMap,
}

pub async fn write_webtransport_http_request_head_after_prefix(
    tx: &mut quinn::SendStream,
    prefix: Bytes,
    head: &WebTransportHttpRequestHead,
) -> Result<(), ProtocolError> {
    let request_head = encode_webtransport_http_request_head(head)?;
    let mut chunks = [prefix, request_head];
    tx.write_all_chunks(&mut chunks)
        .await
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))
}

pub async fn write_webtransport_http_response_head(
    tx: &mut quinn::SendStream,
    head: &WebTransportHttpResponseHead,
) -> Result<(), ProtocolError> {
    tx.write_chunk(encode_webtransport_http_response_head(head)?)
        .await
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))
}

pub async fn read_webtransport_http_request_head(
    rx: &mut quinn::RecvStream,
) -> Result<WebTransportHttpRequestHead, ProtocolError> {
    let payload = read_webtransport_http_head_payload(rx, WEBTRANSPORT_HTTP_KIND_REQUEST).await?;
    decode_webtransport_http_request_head_payload(&payload)
}

pub async fn read_webtransport_http_response_head(
    rx: &mut quinn::RecvStream,
) -> Result<WebTransportHttpResponseHead, ProtocolError> {
    let payload = read_webtransport_http_head_payload(rx, WEBTRANSPORT_HTTP_KIND_RESPONSE).await?;
    decode_webtransport_http_response_head_payload(&payload)
}

pub async fn write_webtransport_http_body(
    tx: &mut quinn::SendStream,
    chunk: Bytes,
) -> Result<(), ProtocolError> {
    if chunk.is_empty() {
        return Ok(());
    }
    tx.write_chunk(chunk)
        .await
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))
}

pub async fn read_webtransport_http_body_chunk(
    rx: &mut quinn::RecvStream,
) -> Result<Option<Bytes>, ProtocolError> {
    let chunk = rx
        .read_chunk(usize::MAX, true)
        .await
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))?;
    Ok(chunk.map(|chunk| chunk.bytes))
}

pub fn finish_webtransport_http_stream(tx: &mut quinn::SendStream) -> Result<(), ProtocolError> {
    tx.finish()
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))
}

fn encode_webtransport_http_request_head_payload(
    head: &WebTransportHttpRequestHead,
) -> Result<Bytes, ProtocolError> {
    let mut payload = Vec::new();
    write_len_prefixed_bytes(
        &mut payload,
        head.method.as_str().as_bytes(),
        u16::MAX as usize,
    )?;
    write_len_prefixed_bytes(
        &mut payload,
        head.path_and_query.as_bytes(),
        u32::MAX as usize,
    )?;
    encode_header_map(&mut payload, &head.headers)?;
    Ok(Bytes::from(payload))
}

fn encode_webtransport_http_response_head_payload(
    head: &WebTransportHttpResponseHead,
) -> Result<Bytes, ProtocolError> {
    let mut payload = Vec::new();
    payload.extend_from_slice(&head.status.as_u16().to_be_bytes());
    encode_header_map(&mut payload, &head.headers)?;
    Ok(Bytes::from(payload))
}

fn encode_webtransport_http_request_head(
    head: &WebTransportHttpRequestHead,
) -> Result<Bytes, ProtocolError> {
    encode_webtransport_http_head(
        WEBTRANSPORT_HTTP_KIND_REQUEST,
        encode_webtransport_http_request_head_payload(head)?,
    )
}

fn encode_webtransport_http_response_head(
    head: &WebTransportHttpResponseHead,
) -> Result<Bytes, ProtocolError> {
    encode_webtransport_http_head(
        WEBTRANSPORT_HTTP_KIND_RESPONSE,
        encode_webtransport_http_response_head_payload(head)?,
    )
}

fn encode_webtransport_http_head(kind: u8, payload: Bytes) -> Result<Bytes, ProtocolError> {
    if payload.len() > MAX_WEBTRANSPORT_HTTP_HEAD_LEN {
        return Err(ProtocolError::ProtocolViolation(format!(
            "WebTransport HTTP head too large: {} bytes",
            payload.len()
        )));
    }
    let payload_len = u32::try_from(payload.len()).map_err(|_| {
        ProtocolError::ProtocolViolation(format!(
            "WebTransport HTTP head too large: {} bytes",
            payload.len()
        ))
    })?;
    let mut encoded = Vec::with_capacity(WEBTRANSPORT_HTTP_PREFIX_LEN + payload.len());
    encoded.extend_from_slice(WEBTRANSPORT_HTTP_MAGIC);
    encoded.push(kind);
    encoded.extend_from_slice(&payload_len.to_be_bytes());
    encoded.extend_from_slice(&payload);
    Ok(Bytes::from(encoded))
}

async fn read_webtransport_http_head_payload(
    rx: &mut quinn::RecvStream,
    expected_kind: u8,
) -> Result<Bytes, ProtocolError> {
    let mut prefix = [0_u8; WEBTRANSPORT_HTTP_PREFIX_LEN];
    rx.read_exact(&mut prefix)
        .await
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))?;
    if &prefix[..WEBTRANSPORT_HTTP_MAGIC.len()] != WEBTRANSPORT_HTTP_MAGIC {
        return Err(ProtocolError::ProtocolViolation(
            "invalid WebTransport HTTP head magic".to_string(),
        ));
    }
    let kind = prefix[WEBTRANSPORT_HTTP_MAGIC.len()];
    if kind != expected_kind {
        return Err(ProtocolError::ProtocolViolation(format!(
            "unexpected WebTransport HTTP head kind: expected {expected_kind}, got {kind}"
        )));
    }
    let payload_len_offset = WEBTRANSPORT_HTTP_MAGIC.len() + 1;
    let payload_len = u32::from_be_bytes(
        prefix[payload_len_offset..payload_len_offset + 4]
            .try_into()
            .expect("fixed-size payload length slice"),
    ) as usize;
    if payload_len > MAX_WEBTRANSPORT_HTTP_HEAD_LEN {
        return Err(ProtocolError::ProtocolViolation(format!(
            "WebTransport HTTP head too large: {payload_len} bytes"
        )));
    }
    let mut payload = vec![0_u8; payload_len];
    rx.read_exact(&mut payload)
        .await
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))?;
    Ok(Bytes::from(payload))
}

fn decode_webtransport_http_request_head_payload(
    payload: &[u8],
) -> Result<WebTransportHttpRequestHead, ProtocolError> {
    let mut cursor = PayloadCursor::new(payload);
    let method = cursor.read_len_prefixed_bytes_u16("method")?;
    let path_and_query = cursor.read_len_prefixed_string_u32("path_and_query")?;
    let headers = decode_header_map(&mut cursor)?;
    cursor.finish()?;
    let method = Method::from_bytes(method).map_err(|error| {
        ProtocolError::ProtocolViolation(format!("invalid WebTransport HTTP method: {error}"))
    })?;
    Ok(WebTransportHttpRequestHead {
        method,
        path_and_query,
        headers,
    })
}

fn decode_webtransport_http_response_head_payload(
    payload: &[u8],
) -> Result<WebTransportHttpResponseHead, ProtocolError> {
    let mut cursor = PayloadCursor::new(payload);
    let status = cursor.read_u16("status")?;
    let headers = decode_header_map(&mut cursor)?;
    cursor.finish()?;
    let status = StatusCode::from_u16(status).map_err(|error| {
        ProtocolError::ProtocolViolation(format!("invalid WebTransport HTTP status: {error}"))
    })?;
    Ok(WebTransportHttpResponseHead { status, headers })
}

fn encode_header_map(payload: &mut Vec<u8>, headers: &HeaderMap) -> Result<(), ProtocolError> {
    let entry_count = u32::try_from(headers.len()).map_err(|_| {
        ProtocolError::ProtocolViolation(format!(
            "too many WebTransport HTTP headers: {}",
            headers.len()
        ))
    })?;
    if entry_count > MAX_WEBTRANSPORT_HTTP_HEADER_COUNT {
        return Err(ProtocolError::ProtocolViolation(format!(
            "too many WebTransport HTTP headers: {entry_count}"
        )));
    }
    payload.extend_from_slice(&entry_count.to_be_bytes());
    for (name, value) in headers {
        let value = header_value_to_str(name, value)?;
        write_len_prefixed_bytes(payload, name.as_str().as_bytes(), u16::MAX as usize)?;
        write_len_prefixed_bytes(payload, value.as_bytes(), u32::MAX as usize)?;
    }
    Ok(())
}

fn decode_header_map(cursor: &mut PayloadCursor<'_>) -> Result<HeaderMap, ProtocolError> {
    let entry_count = cursor.read_u32("header_count")?;
    if entry_count > MAX_WEBTRANSPORT_HTTP_HEADER_COUNT {
        return Err(ProtocolError::ProtocolViolation(format!(
            "too many WebTransport HTTP headers: {entry_count}"
        )));
    }
    let min_header_bytes = entry_count as usize * MIN_WEBTRANSPORT_HTTP_HEADER_ENTRY_LEN;
    if cursor.remaining() < min_header_bytes {
        return Err(ProtocolError::ProtocolViolation(format!(
            "incomplete WebTransport HTTP headers: {entry_count} entries need at least {min_header_bytes} bytes, have {}",
            cursor.remaining()
        )));
    }
    let mut header_map = HeaderMap::with_capacity(entry_count as usize);
    for _ in 0..entry_count {
        let name = cursor.read_len_prefixed_bytes_u16("header_name")?;
        let value = cursor.read_len_prefixed_bytes_u32("header_value")?;
        append_header_entry_bytes(&mut header_map, name, value)?;
    }
    Ok(header_map)
}

fn write_len_prefixed_bytes(
    payload: &mut Vec<u8>,
    bytes: &[u8],
    max_len: usize,
) -> Result<(), ProtocolError> {
    if bytes.len() > max_len {
        return Err(ProtocolError::ProtocolViolation(format!(
            "WebTransport HTTP field too large: {} bytes",
            bytes.len()
        )));
    }
    if max_len == u16::MAX as usize {
        let len = u16::try_from(bytes.len()).expect("length checked against u16::MAX");
        payload.extend_from_slice(&len.to_be_bytes());
    } else {
        let len = u32::try_from(bytes.len()).expect("length checked against u32::MAX");
        payload.extend_from_slice(&len.to_be_bytes());
    }
    payload.extend_from_slice(bytes);
    Ok(())
}

struct PayloadCursor<'a> {
    payload: &'a [u8],
    offset: usize,
}

impl<'a> PayloadCursor<'a> {
    fn new(payload: &'a [u8]) -> Self {
        Self { payload, offset: 0 }
    }

    fn read_u16(&mut self, field: &str) -> Result<u16, ProtocolError> {
        let bytes = self.read_exact(field, 2)?;
        Ok(u16::from_be_bytes(
            bytes.try_into().expect("fixed-size u16 slice"),
        ))
    }

    fn read_u32(&mut self, field: &str) -> Result<u32, ProtocolError> {
        let bytes = self.read_exact(field, 4)?;
        Ok(u32::from_be_bytes(
            bytes.try_into().expect("fixed-size u32 slice"),
        ))
    }

    fn read_len_prefixed_string_u32(&mut self, field: &str) -> Result<String, ProtocolError> {
        let bytes = self.read_len_prefixed_bytes_u32(field)?;
        self.read_string(field, bytes)
    }

    fn read_len_prefixed_bytes_u16(&mut self, field: &str) -> Result<&'a [u8], ProtocolError> {
        let len = self.read_u16(field)? as usize;
        self.read_exact(field, len)
    }

    fn read_len_prefixed_bytes_u32(&mut self, field: &str) -> Result<&'a [u8], ProtocolError> {
        let len = self.read_u32(field)? as usize;
        self.read_exact(field, len)
    }

    fn read_string(&mut self, field: &str, bytes: &'a [u8]) -> Result<String, ProtocolError> {
        std::str::from_utf8(bytes)
            .map(str::to_owned)
            .map_err(|error| {
                ProtocolError::ProtocolViolation(format!(
                    "invalid UTF-8 WebTransport HTTP {field}: {error}"
                ))
            })
    }

    fn read_exact(&mut self, field: &str, len: usize) -> Result<&'a [u8], ProtocolError> {
        let end = self.offset.checked_add(len).ok_or_else(|| {
            ProtocolError::ProtocolViolation(format!(
                "WebTransport HTTP {field} length overflow: {len}"
            ))
        })?;
        if end > self.payload.len() {
            return Err(ProtocolError::ProtocolViolation(format!(
                "incomplete WebTransport HTTP {field}: need {len} bytes, have {}",
                self.remaining()
            )));
        }
        let bytes = &self.payload[self.offset..end];
        self.offset = end;
        Ok(bytes)
    }

    fn remaining(&self) -> usize {
        // Keep parser diagnostics safe even if a malformed length advances offset past the payload.
        self.payload.len().saturating_sub(self.offset)
    }

    fn finish(&self) -> Result<(), ProtocolError> {
        if self.offset != self.payload.len() {
            return Err(ProtocolError::ProtocolViolation(format!(
                "trailing WebTransport HTTP head bytes: {}",
                self.payload.len() - self.offset
            )));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::tunnel_contract::{HEADER_MODEL, HEADER_STARGATE_RETRYABLE};

    #[test]
    fn request_head_roundtrips_http_semantics() {
        let mut headers = HeaderMap::new();
        headers.append(HEADER_MODEL, "llama".parse().unwrap());
        headers.append("set-cookie", "a=1".parse().unwrap());
        headers.append("set-cookie", "b=2".parse().unwrap());
        let head = WebTransportHttpRequestHead {
            method: Method::POST,
            path_and_query: "/v1/chat/completions?trace=1".to_string(),
            headers,
        };

        let encoded = encode_webtransport_http_request_head(&head).unwrap();
        assert_eq!(
            &encoded[..WEBTRANSPORT_HTTP_MAGIC.len()],
            WEBTRANSPORT_HTTP_MAGIC
        );
        assert_eq!(
            encoded[WEBTRANSPORT_HTTP_MAGIC.len()],
            WEBTRANSPORT_HTTP_KIND_REQUEST
        );
        let payload = &encoded[WEBTRANSPORT_HTTP_PREFIX_LEN..];
        let decoded = decode_webtransport_http_request_head_payload(payload).unwrap();

        assert_eq!(decoded.method, Method::POST);
        assert_eq!(decoded.path_and_query, "/v1/chat/completions?trace=1");
        assert_eq!(decoded.headers.get(HEADER_MODEL).unwrap(), "llama");
        let cookies: Vec<_> = decoded
            .headers
            .get_all("set-cookie")
            .iter()
            .map(|value| value.to_str().unwrap())
            .collect();
        assert_eq!(cookies, vec!["a=1", "b=2"]);
    }

    #[test]
    fn response_head_roundtrips_status_and_headers() {
        let mut headers = HeaderMap::new();
        headers.append("content-type", "text/event-stream".parse().unwrap());
        headers.append(HEADER_STARGATE_RETRYABLE, "false".parse().unwrap());
        let head = WebTransportHttpResponseHead {
            status: StatusCode::SERVICE_UNAVAILABLE,
            headers,
        };

        let encoded = encode_webtransport_http_response_head(&head).unwrap();
        assert_eq!(
            &encoded[..WEBTRANSPORT_HTTP_MAGIC.len()],
            WEBTRANSPORT_HTTP_MAGIC
        );
        assert_eq!(
            encoded[WEBTRANSPORT_HTTP_MAGIC.len()],
            WEBTRANSPORT_HTTP_KIND_RESPONSE
        );
        let payload = &encoded[WEBTRANSPORT_HTTP_PREFIX_LEN..];
        let decoded = decode_webtransport_http_response_head_payload(payload).unwrap();

        assert_eq!(decoded.status, StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(
            decoded.headers.get("content-type").unwrap(),
            "text/event-stream"
        );
        assert_eq!(
            decoded.headers.get(HEADER_STARGATE_RETRYABLE).unwrap(),
            "false"
        );
    }

    #[test]
    fn request_head_rejects_truncated_payload() {
        let error = decode_webtransport_http_request_head_payload(&[0x00, 0x03, b'G']).unwrap_err();

        assert!(matches!(error, ProtocolError::ProtocolViolation(_)));
    }

    #[test]
    fn request_head_rejects_excessive_header_count_before_allocation() {
        let mut payload = Vec::new();
        payload.extend_from_slice(&3_u16.to_be_bytes());
        payload.extend_from_slice(b"GET");
        payload.extend_from_slice(&1_u32.to_be_bytes());
        payload.extend_from_slice(b"/");
        payload.extend_from_slice(&(MAX_WEBTRANSPORT_HTTP_HEADER_COUNT + 1).to_be_bytes());

        let error = decode_webtransport_http_request_head_payload(&payload).unwrap_err();

        assert!(
            error
                .to_string()
                .contains("too many WebTransport HTTP headers"),
            "unexpected error: {error}"
        );
    }

    #[test]
    fn request_head_rejects_non_utf8_header_value() {
        let mut payload = Vec::new();
        payload.extend_from_slice(&3_u16.to_be_bytes());
        payload.extend_from_slice(b"GET");
        payload.extend_from_slice(&1_u32.to_be_bytes());
        payload.extend_from_slice(b"/");
        payload.extend_from_slice(&1_u32.to_be_bytes());
        payload.extend_from_slice(&8_u16.to_be_bytes());
        payload.extend_from_slice(b"x-binary");
        payload.extend_from_slice(&1_u32.to_be_bytes());
        payload.push(0xff);

        let error = decode_webtransport_http_request_head_payload(&payload).unwrap_err();

        assert!(
            error.to_string().contains("non-UTF8 header value"),
            "unexpected error: {error}"
        );
    }
}
