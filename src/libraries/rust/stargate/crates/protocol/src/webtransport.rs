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
use bytes::Bytes;

const MAX_QUIC_VARINT: u64 = (1 << 62) - 1;
const MAX_QUIC_VARINT_ENCODED_LEN: usize = 8;
const MAX_WEBTRANSPORT_BIDI_HEADER_LEN: usize = MAX_QUIC_VARINT_ENCODED_LEN * 2;
const COMMON_WEBTRANSPORT_BIDI_HEADER: [u8; 3] = [0x40, 0x41, 0x00];

#[derive(Debug, PartialEq, Eq)]
enum BidiHeaderDecode {
    Complete(u64, usize),
    Incomplete(usize),
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct WebTransportBidiHeader {
    bytes: [u8; MAX_WEBTRANSPORT_BIDI_HEADER_LEN],
    len: usize,
}

impl WebTransportBidiHeader {
    pub fn new(session_id: u64) -> Result<Self, ProtocolError> {
        let mut bytes = [0_u8; MAX_WEBTRANSPORT_BIDI_HEADER_LEN];
        let len = encode_webtransport_bidi_header(session_id, &mut bytes)?;
        Ok(Self { bytes, len })
    }

    pub fn as_slice(&self) -> &[u8] {
        &self.bytes[..self.len]
    }

    pub fn to_bytes(self) -> Bytes {
        Bytes::copy_from_slice(self.as_slice())
    }
}

pub async fn write_webtransport_bidi_header(
    tx: &mut quinn::SendStream,
    session_id: u64,
) -> Result<(), ProtocolError> {
    write_precomputed_webtransport_bidi_header(tx, &WebTransportBidiHeader::new(session_id)?).await
}

pub async fn write_precomputed_webtransport_bidi_header(
    tx: &mut quinn::SendStream,
    header: &WebTransportBidiHeader,
) -> Result<(), ProtocolError> {
    tx.write_all(header.as_slice())
        .await
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))
}

pub async fn read_webtransport_bidi_header(
    rx: &mut quinn::RecvStream,
) -> Result<u64, ProtocolError> {
    let mut header = [0_u8; MAX_WEBTRANSPORT_BIDI_HEADER_LEN];
    let mut read_len = 3;
    rx.read_exact(&mut header[..read_len])
        .await
        .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))?;
    if header[..read_len] == COMMON_WEBTRANSPORT_BIDI_HEADER {
        return Ok(0);
    }

    loop {
        match parse_webtransport_bidi_header(&header[..read_len])? {
            BidiHeaderDecode::Complete(session_id, _) => return Ok(session_id),
            BidiHeaderDecode::Incomplete(required_len) => {
                rx.read_exact(&mut header[read_len..required_len])
                    .await
                    .map_err(|error| ProtocolError::Io(std::io::Error::other(error)))?;
                read_len = required_len;
            }
        }
    }
}

fn encode_webtransport_bidi_header(
    session_id: u64,
    out: &mut [u8; MAX_WEBTRANSPORT_BIDI_HEADER_LEN],
) -> Result<usize, ProtocolError> {
    validate_quic_varint(session_id)?;

    let mut offset = 0;
    offset += encode_quic_varint(crate::WEBTRANSPORT_BIDI_STREAM_TYPE, &mut out[offset..])?;
    offset += encode_quic_varint(session_id, &mut out[offset..])?;
    Ok(offset)
}

fn validate_quic_varint(value: u64) -> Result<(), ProtocolError> {
    (value <= MAX_QUIC_VARINT).then_some(()).ok_or_else(|| {
        ProtocolError::ProtocolViolation(format!("QUIC varint value out of range: {value}"))
    })
}

fn encode_quic_varint(value: u64, out: &mut [u8]) -> Result<usize, ProtocolError> {
    validate_quic_varint(value)?;

    let len = quic_varint_len_for_value(value);
    let tag = match len {
        1 => 0b00_u8,
        2 => 0b01_u8,
        4 => 0b10_u8,
        _ => 0b11_u8,
    };

    if out.len() < len {
        return Err(ProtocolError::ProtocolViolation(format!(
            "QUIC varint output buffer too short: need {len}, got {}",
            out.len()
        )));
    }

    for (offset, byte_index) in (0..len).rev().enumerate() {
        let mut byte = ((value >> (byte_index * 8)) & 0xff) as u8;
        if byte_index == len - 1 {
            byte = (byte & 0x3f) | (tag << 6);
        }
        out[offset] = byte;
    }
    Ok(len)
}

fn parse_webtransport_bidi_header(bytes: &[u8]) -> Result<BidiHeaderDecode, ProtocolError> {
    let Some(first) = bytes.first().copied() else {
        return Ok(BidiHeaderDecode::Incomplete(1));
    };
    let stream_type_len = quic_varint_len_from_first(first);
    if bytes.len() < stream_type_len {
        return Ok(BidiHeaderDecode::Incomplete(stream_type_len));
    }
    let stream_type = decode_quic_varint_bytes(&bytes[..stream_type_len])?;
    if stream_type != crate::WEBTRANSPORT_BIDI_STREAM_TYPE {
        return Err(ProtocolError::ProtocolViolation(format!(
            "expected WebTransport bidi stream type {:#x}, got {stream_type:#x}",
            crate::WEBTRANSPORT_BIDI_STREAM_TYPE
        )));
    }

    let Some(session_first) = bytes.get(stream_type_len).copied() else {
        return Ok(BidiHeaderDecode::Incomplete(stream_type_len + 1));
    };
    let session_varint_len = quic_varint_len_from_first(session_first);
    let consumed = stream_type_len + session_varint_len;
    if bytes.len() < consumed {
        return Ok(BidiHeaderDecode::Incomplete(consumed));
    }
    let session_id = decode_quic_varint_bytes(&bytes[stream_type_len..consumed])?;
    Ok(BidiHeaderDecode::Complete(session_id, consumed))
}

fn decode_quic_varint_bytes(bytes: &[u8]) -> Result<u64, ProtocolError> {
    let first = bytes
        .first()
        .copied()
        .ok_or_else(|| ProtocolError::ProtocolViolation("empty QUIC varint".to_string()))?;
    let len = quic_varint_len_from_first(first);
    if bytes.len() < len {
        return Err(ProtocolError::ProtocolViolation(format!(
            "incomplete QUIC varint: need {len}, got {}",
            bytes.len()
        )));
    }
    let mut value = u64::from(first & 0x3f);
    for byte in &bytes[1..len] {
        value = (value << 8) | u64::from(*byte);
    }
    Ok(value)
}

fn quic_varint_len_for_value(value: u64) -> usize {
    match value {
        0..=0x3f => 1,
        0x40..=0x3fff => 2,
        0x4000..=0x3fff_ffff => 4,
        _ => 8,
    }
}

fn quic_varint_len_from_first(first: u8) -> usize {
    match first >> 6 {
        0b00 => 1,
        0b01 => 2,
        0b10 => 4,
        _ => 8,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use proptest::prelude::*;

    fn decode(bytes: &[u8]) -> Option<(u64, usize)> {
        match parse_webtransport_bidi_header(bytes).unwrap() {
            BidiHeaderDecode::Complete(session_id, consumed) => Some((session_id, consumed)),
            BidiHeaderDecode::Incomplete(_) => None,
        }
    }

    fn assert_decodes(bytes: &[u8], expected: Option<(u64, usize)>) {
        assert_eq!(decode(bytes), expected);
    }

    proptest! {
        #[test]
        fn webtransport_bidi_header_roundtrips_any_valid_session_id(
            session_id in 0..=MAX_QUIC_VARINT,
        ) {
            let header = WebTransportBidiHeader::new(session_id).unwrap();
            let decoded = decode(header.as_slice());
            prop_assert_eq!(decoded, Some((session_id, header.as_slice().len())));
        }
    }

    #[test]
    fn webtransport_bidi_header_varints_match_expected_wire_bytes() {
        let mut encoded = [0_u8; MAX_WEBTRANSPORT_BIDI_HEADER_LEN];
        let len = encode_webtransport_bidi_header(0, &mut encoded).unwrap();

        assert_eq!(&encoded[..len], &[0x40, 0x41, 0x00]);
    }

    #[test]
    fn webtransport_bidi_header_common_fast_path_matches_encoder() {
        let header = WebTransportBidiHeader::new(0).unwrap();

        assert_eq!(header.as_slice(), COMMON_WEBTRANSPORT_BIDI_HEADER);
    }

    #[test]
    fn webtransport_bidi_header_stack_encoder_handles_wide_session_ids() {
        let mut encoded = [0_u8; MAX_WEBTRANSPORT_BIDI_HEADER_LEN];
        let len = encode_webtransport_bidi_header(0x3fff, &mut encoded).unwrap();

        assert_eq!(&encoded[..len], &[0x40, 0x41, 0x7f, 0xff]);
    }

    #[test]
    fn webtransport_bidi_header_rejects_out_of_range_session_id_without_partial_write() {
        let mut encoded = [0xaa_u8; MAX_WEBTRANSPORT_BIDI_HEADER_LEN];
        let error = encode_webtransport_bidi_header(MAX_QUIC_VARINT + 1, &mut encoded).unwrap_err();

        assert!(matches!(error, ProtocolError::ProtocolViolation(_)));
        assert_eq!(encoded, [0xaa_u8; MAX_WEBTRANSPORT_BIDI_HEADER_LEN]);
    }

    #[test]
    fn webtransport_bidi_header_precomputes_reusable_slice() {
        let header = WebTransportBidiHeader::new(0x3fff).unwrap();

        assert_eq!(header.as_slice(), &[0x40, 0x41, 0x7f, 0xff]);
    }

    #[test]
    fn webtransport_bidi_header_decodes_common_prefix_without_extra_bytes() {
        assert_decodes(&[0x40, 0x41, 0x00], Some((0, 3)));
    }

    #[test]
    fn webtransport_bidi_header_decodes_non_minimal_stream_type_varints() {
        assert_decodes(&[0x80, 0x00, 0x00, 0x41, 0x00], Some((0, 5)));
        assert_decodes(
            &[0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x41, 0x00],
            Some((0, 9)),
        );
    }

    #[test]
    fn webtransport_bidi_header_waits_for_non_minimal_stream_type_bytes() {
        assert_decodes(&[0x80, 0x00, 0x00], None);
        assert_decodes(&[0x80, 0x00, 0x00, 0x41], None);
    }

    #[test]
    fn webtransport_bidi_header_waits_for_wide_session_id_bytes() {
        assert_decodes(&[0x40, 0x41, 0x7f], None);
        assert_decodes(&[0x40, 0x41, 0x7f, 0xff], Some((0x3fff, 4)));
    }

    #[test]
    fn webtransport_bidi_header_rejects_wrong_stream_type_prefix() {
        let error = parse_webtransport_bidi_header(&[0x00, 0x41, 0x00]).unwrap_err();

        assert!(matches!(error, ProtocolError::ProtocolViolation(_)));
    }
}
