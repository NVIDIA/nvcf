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
use crate::common::{append_header_entry, header_value_to_str};
use crate::quic_capnp;
use bytes::Bytes;
use capnp::message::{TypedBuilder, TypedReader};
use capnp::traits::Owned;
use http::HeaderMap;
use quinn::VarInt;

pub const ALPN_PROTOCOL: &str = "stargate-quic/1";
const MAX_QUIC_HEADER_COUNT: usize = 4096;
const CAPNP_BYTES_PER_WORD: usize = 8;
const CAPNP_BODY_MESSAGE_FIXED_WORDS: usize = 4;
const CAPNP_LIST_ELEMENT_SIZE_BYTE: u64 = 2;
const CAPNP_LIST_ELEMENT_SIZE_INLINE_COMPOSITE: u64 = 7;
const CAPNP_MESSAGE_POINTER: u64 = (1 << 32) | (1 << 48);
const CAPNP_PAYLOAD_POINTER: u64 = 1 << 48;
// Cap'n Proto stores list lengths in a 29-bit pointer field; Text/Data are byte lists.
const CAPNP_LIST_ELEMENT_COUNT_MAX: usize = (1usize << 29) - 1;
// Intra-segment pointer offsets are signed 30-bit word counts.
const CAPNP_POINTER_OFFSET_WORDS_MAX: usize = (1usize << 29) - 1;
static CAPNP_ZERO_PADDING: [u8; CAPNP_BYTES_PER_WORD - 1] = [0; CAPNP_BYTES_PER_WORD - 1];

const READER_OPTIONS: capnp::message::ReaderOptions = capnp::message::ReaderOptions {
    traversal_limit_in_words: Some(1024 * 1024 * 128), // 1GB
    nesting_limit: 64,
};

#[derive(Debug, Clone)]
pub enum QuicMessage {
    Header(QuicHeader),
    Body(QuicBody),
    Trailer(QuicTrailer),
}

impl std::fmt::Display for QuicMessage {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str(match self {
            Self::Header(_) => "Header",
            Self::Body(_) => "Body",
            Self::Trailer(_) => "Trailer",
        })
    }
}

impl QuicMessage {
    pub fn to_builder(&self) -> Result<TypedBuilder<quic_capnp::message::Owned>, ProtocolError> {
        let mut builder = typed_builder::<quic_capnp::message::Owned>();
        match self {
            Self::Header(header) => {
                write_entries(builder.get_root()?.init_headers(), &header.entries)?
            }
            Self::Body(body) => builder.get_root()?.init_body().set_content(&body.content),
            Self::Trailer(trailer) => {
                write_entries(builder.get_root()?.init_trailers(), &trailer.entries)?
            }
        }
        Ok(builder)
    }

    pub fn from_reader(
        reader: TypedReader<capnp::serialize::OwnedSegments, quic_capnp::message::Owned>,
    ) -> Result<Self, ProtocolError> {
        match reader.get()?.which().map_err(unknown_discriminant)? {
            quic_capnp::message::Which::Headers(Ok(headers)) => Ok(Self::Header(QuicHeader {
                entries: entries_from_capnp_map(headers, ("header key", "header value"))?,
            })),
            quic_capnp::message::Which::Body(Ok(body)) => Ok(Self::Body(QuicBody {
                content: body.get_content()?.to_vec().into(),
            })),
            quic_capnp::message::Which::Trailers(Ok(trailers)) => Ok(Self::Trailer(QuicTrailer {
                entries: entries_from_capnp_map(trailers, ("trailer key", "trailer value"))?,
            })),
            _ => Err(ProtocolError::ProtocolViolation(
                "unknown QuicMessage type".to_string(),
            )),
        }
    }
}

#[derive(Debug, Clone)]
pub struct QuicHeader {
    pub entries: Vec<(String, String)>,
}

#[derive(Debug, Clone)]
pub struct QuicBody {
    pub content: Bytes,
}

#[derive(Debug, Clone)]
pub struct QuicTrailer {
    pub entries: Vec<(String, String)>,
}

fn unknown_discriminant(error: capnp::NotInSchema) -> ProtocolError {
    ProtocolError::ProtocolViolation(format!("unknown QuicMessage discriminant: {error}"))
}

fn io_error(error: impl std::error::Error + Send + Sync + 'static) -> ProtocolError {
    ProtocolError::Io(std::io::Error::other(error))
}

fn typed_builder<T: Owned>() -> TypedBuilder<T> {
    let mut builder = TypedBuilder::new_default();
    builder.init_root();
    builder
}

fn write_entries(
    mut map: quic_capnp::map::Builder<'_, capnp::text::Owned, capnp::text::Owned>,
    entries: &[(String, String)],
) -> Result<(), ProtocolError> {
    validate_quic_header_count(entries.len())?;
    let mut output = map.reborrow().init_entries(entries.len() as u32);
    for (index, (key, value)) in entries.iter().enumerate() {
        let mut entry = output.reborrow().get(index as u32);
        entry.set_key(key)?;
        entry.set_value(value)?;
    }
    Ok(())
}

#[derive(Debug, Clone)]
pub enum QuicBodyOrTrailer {
    Body(Bytes),
    Trailer(HeaderMap),
}

pub async fn read_from_stream<T: Owned>(
    rx: &mut quinn::RecvStream,
) -> Result<Option<TypedReader<capnp::serialize::OwnedSegments, T>>, ProtocolError> {
    let reader = capnp_futures::serialize::try_read_message(rx, READER_OPTIONS).await?;
    Ok(reader.map(TypedReader::new))
}

pub async fn write_to_stream(
    tx: &mut quinn::SendStream,
    builder: TypedBuilder<quic_capnp::message::Owned>,
) -> Result<(), ProtocolError> {
    tx.write_chunk(serialize_message(builder))
        .await
        .map_err(io_error)
}

pub async fn write_header_map_to_stream(
    tx: &mut quinn::SendStream,
    headers: &HeaderMap,
) -> Result<(), ProtocolError> {
    write_header_map(tx, headers, HeaderMapMessageKind::Header).await
}

pub async fn write_trailer_map_to_stream(
    tx: &mut quinn::SendStream,
    trailers: &HeaderMap,
) -> Result<(), ProtocolError> {
    write_header_map(tx, trailers, HeaderMapMessageKind::Trailer).await
}

async fn write_header_map(
    tx: &mut quinn::SendStream,
    headers: &HeaderMap,
    kind: HeaderMapMessageKind,
) -> Result<(), ProtocolError> {
    tx.write_chunk(serialize_header_map_message(headers, kind)?)
        .await
        .map_err(io_error)
}

pub async fn write_body_to_stream(
    tx: &mut quinn::SendStream,
    body: Bytes,
) -> Result<(), ProtocolError> {
    let (prefix, body, padding) = body_message_chunks(body)?;
    if body.is_empty() {
        tx.write_chunk(prefix).await.map_err(io_error)
    } else if padding.is_empty() {
        let mut chunks = [prefix, body];
        tx.write_all_chunks(&mut chunks).await.map_err(io_error)
    } else {
        let mut chunks = [prefix, body, padding];
        tx.write_all_chunks(&mut chunks).await.map_err(io_error)
    }
}

pub async fn read_header_map_from_stream(
    rx: &mut quinn::RecvStream,
) -> Result<Option<HeaderMap>, ProtocolError> {
    let Some(reader) = read_from_stream::<quic_capnp::message::Owned>(rx).await? else {
        return Ok(None);
    };
    match reader.get()?.which().map_err(unknown_discriminant)? {
        quic_capnp::message::Which::Headers(headers) => {
            header_map_from_capnp_map(headers?).map(Some)
        }
        quic_capnp::message::Which::Body(_) => Err(ProtocolError::ProtocolViolation(
            "expected header message, got body".to_string(),
        )),
        quic_capnp::message::Which::Trailers(_) => Err(ProtocolError::ProtocolViolation(
            "expected header message, got trailer".to_string(),
        )),
    }
}

pub async fn read_body_or_trailer_from_stream(
    rx: &mut quinn::RecvStream,
) -> Result<Option<QuicBodyOrTrailer>, ProtocolError> {
    let Some(reader) = read_from_stream::<quic_capnp::message::Owned>(rx).await? else {
        return Ok(None);
    };
    match reader.get()?.which().map_err(unknown_discriminant)? {
        quic_capnp::message::Which::Body(body) => {
            let content = body?.get_content()?.to_vec().into();
            Ok(Some(QuicBodyOrTrailer::Body(content)))
        }
        quic_capnp::message::Which::Trailers(trailers) => header_map_from_capnp_map(trailers?)
            .map(QuicBodyOrTrailer::Trailer)
            .map(Some),
        quic_capnp::message::Which::Headers(_) => Err(ProtocolError::ProtocolViolation(
            "expected body message, got header".to_string(),
        )),
    }
}

pub(crate) async fn expect_stream_end(rx: &mut quinn::RecvStream) -> Result<(), ProtocolError> {
    let Some(reader) = read_from_stream::<quic_capnp::message::Owned>(rx).await? else {
        return Ok(());
    };
    let message = QuicMessage::from_reader(reader)?;
    Err(ProtocolError::ProtocolViolation(format!(
        "expected none after trailer, got {message}"
    )))
}

fn serialize_message(builder: TypedBuilder<quic_capnp::message::Owned>) -> Bytes {
    Bytes::from(capnp::serialize::write_message_to_words(
        builder.borrow_inner(),
    ))
}

fn serialize_header_map_message(
    headers: &HeaderMap,
    kind: HeaderMapMessageKind,
) -> Result<Bytes, ProtocolError> {
    validate_header_map(headers)?;
    let entry_count = headers.len();
    // The header-count limit makes these conversions and this multiplication exact.
    let entry_count_u32 = entry_count as u32;
    let entry_words = entry_count_u32 * 2;
    let mut text_cursor_words = 5 + entry_words as usize;
    for (index, (key, value)) in headers.iter().enumerate() {
        let value = header_value_to_str(key, value)?;
        let mut text_words = [0; 2];
        for (text_word, byte_len) in text_words.iter_mut().zip([key.as_str().len(), value.len()]) {
            *text_word = text_cursor_words;
            text_cursor_words = text_cursor_words
                .checked_add(capnp_text_word_len(byte_len)?)
                .ok_or_else(|| raw_quic_header_too_large(entry_count))?;
        }
        for (field, text_word) in text_words.into_iter().enumerate() {
            checked_capnp_pointer_offset_words(5 + index * 2 + field, text_word)?;
        }
    }

    let segment_words_u32 =
        u32::try_from(text_cursor_words).map_err(|_| raw_quic_header_too_large(entry_count))?;
    let total_len = text_cursor_words
        .checked_add(1)
        .and_then(|words| words.checked_mul(CAPNP_BYTES_PER_WORD))
        .ok_or_else(|| raw_quic_header_too_large(entry_count))?;
    let mut encoded = vec![0_u8; total_len];

    put_u32_le(&mut encoded, 4, segment_words_u32);
    put_capnp_word(&mut encoded, 0, CAPNP_MESSAGE_POINTER);
    put_capnp_word(&mut encoded, 1, kind as u64);
    put_capnp_word(&mut encoded, 2, CAPNP_PAYLOAD_POINTER);
    put_capnp_word(
        &mut encoded,
        3,
        capnp_list_pointer_word(0, CAPNP_LIST_ELEMENT_SIZE_INLINE_COMPOSITE, entry_words),
    );
    put_capnp_word(&mut encoded, 4, capnp_struct_list_tag_word(entry_count_u32));

    let mut text_word = 5 + entry_words as usize;
    for (index, (name, value)) in headers.iter().enumerate() {
        let value = header_value_to_str(name, value)?;
        for (field, text) in [name.as_str(), value].into_iter().enumerate() {
            let pointer_word = 5 + index * 2 + field;
            put_capnp_word(
                &mut encoded,
                pointer_word,
                capnp_text_pointer_word(pointer_word, text_word, text.len())?,
            );
            let text_offset = CAPNP_BYTES_PER_WORD + text_word * CAPNP_BYTES_PER_WORD;
            encoded[text_offset..text_offset + text.len()].copy_from_slice(text.as_bytes());
            text_word += capnp_text_word_len(text.len())?;
        }
    }

    Ok(Bytes::from(encoded))
}

fn body_message_chunks(body: Bytes) -> Result<(Bytes, Bytes, Bytes), ProtocolError> {
    let data_words = body.len().div_ceil(CAPNP_BYTES_PER_WORD);
    let segment_words = CAPNP_BODY_MESSAGE_FIXED_WORDS
        .checked_add(data_words)
        .ok_or_else(|| raw_quic_body_too_large(body.len()))?;
    let segment_words_u32 =
        u32::try_from(segment_words).map_err(|_| raw_quic_body_too_large(body.len()))?;
    let body_len = checked_body_list_element_count(body.len())?;

    let mut prefix =
        vec![0; CAPNP_BYTES_PER_WORD + CAPNP_BODY_MESSAGE_FIXED_WORDS * CAPNP_BYTES_PER_WORD];
    put_u32_le(&mut prefix, 4, segment_words_u32);
    put_capnp_word(&mut prefix, 0, CAPNP_MESSAGE_POINTER);
    put_capnp_word(&mut prefix, 1, 1);
    put_capnp_word(&mut prefix, 2, CAPNP_PAYLOAD_POINTER);
    put_capnp_word(
        &mut prefix,
        3,
        capnp_list_pointer_word(0, CAPNP_LIST_ELEMENT_SIZE_BYTE, body_len),
    );
    let padding_len = data_words * CAPNP_BYTES_PER_WORD - body.len();
    let padding = Bytes::from_static(&CAPNP_ZERO_PADDING[..padding_len]);
    Ok((Bytes::from(prefix), body, padding))
}

fn put_u32_le(out: &mut [u8], offset: usize, value: u32) {
    out[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
}

fn put_capnp_word(out: &mut [u8], segment_word_index: usize, word: u64) {
    let offset = CAPNP_BYTES_PER_WORD + segment_word_index * CAPNP_BYTES_PER_WORD;
    out[offset..offset + CAPNP_BYTES_PER_WORD].copy_from_slice(&word.to_le_bytes());
}

fn capnp_list_pointer_word(offset_words: i32, element_size: u64, element_count: u32) -> u64 {
    const CAPNP_POINTER_KIND_LIST: u64 = 1;
    let offset = (offset_words as u32) & 0x3fff_ffff;
    CAPNP_POINTER_KIND_LIST
        | u64::from(offset << 2)
        | (element_size << 32)
        | (u64::from(element_count) << 35)
}

fn capnp_struct_list_tag_word(element_count: u32) -> u64 {
    u64::from(element_count << 2) | (2 << 48)
}

fn capnp_text_pointer_word(
    pointer_word_index: usize,
    text_word_index: usize,
    byte_len: usize,
) -> Result<u64, ProtocolError> {
    let offset_words = checked_capnp_pointer_offset_words(pointer_word_index, text_word_index)?;
    Ok(capnp_list_pointer_word(
        offset_words,
        CAPNP_LIST_ELEMENT_SIZE_BYTE,
        checked_capnp_text_len(byte_len)?,
    ))
}

fn checked_capnp_text_len(byte_len: usize) -> Result<u32, ProtocolError> {
    let len_with_nul = byte_len.checked_add(1).ok_or_else(|| {
        ProtocolError::ProtocolViolation("raw QUIC tunnel header too large".into())
    })?;
    checked_capnp_list_element_count(len_with_nul, || {
        format!("raw QUIC tunnel header field too large: {byte_len} bytes")
    })
}

fn checked_capnp_pointer_offset_words(
    pointer_word_index: usize,
    target_word_index: usize,
) -> Result<i32, ProtocolError> {
    let offset_words = target_word_index
        .checked_sub(pointer_word_index + 1)
        .ok_or_else(|| {
            ProtocolError::ProtocolViolation("invalid Cap'n Proto pointer layout".into())
        })?;
    if offset_words > CAPNP_POINTER_OFFSET_WORDS_MAX {
        return Err(ProtocolError::ProtocolViolation(
            "raw QUIC tunnel header text offset too large".into(),
        ));
    }
    Ok(offset_words as i32)
}

fn capnp_text_word_len(byte_len: usize) -> Result<usize, ProtocolError> {
    Ok((checked_capnp_text_len(byte_len)? as usize).div_ceil(CAPNP_BYTES_PER_WORD))
}

fn checked_capnp_list_element_count<F>(count: usize, message: F) -> Result<u32, ProtocolError>
where
    F: FnOnce() -> String,
{
    if count <= CAPNP_LIST_ELEMENT_COUNT_MAX {
        Ok(count as u32)
    } else {
        Err(ProtocolError::ProtocolViolation(message()))
    }
}

fn checked_body_list_element_count(body_len: usize) -> Result<u32, ProtocolError> {
    checked_capnp_list_element_count(body_len, || {
        format!("raw QUIC tunnel body too large: {body_len} bytes")
    })
}

fn validate_header_map(headers: &HeaderMap) -> Result<(), ProtocolError> {
    validate_quic_header_count(headers.len())?;
    headers
        .iter()
        .try_for_each(|(key, value)| header_value_to_str(key, value).map(|_| ()))
}

fn raw_quic_header_too_large(entry_count: usize) -> ProtocolError {
    ProtocolError::ProtocolViolation(format!("too many raw QUIC tunnel headers: {entry_count}"))
}

fn raw_quic_body_too_large(body_len: usize) -> ProtocolError {
    ProtocolError::ProtocolViolation(format!("raw QUIC tunnel body too large: {body_len} bytes"))
}

#[derive(Debug, Clone, Copy)]
enum HeaderMapMessageKind {
    Header = 0,
    Trailer = 2,
}

fn header_map_from_capnp_map(
    headers: quic_capnp::map::Reader<'_, capnp::text::Owned, capnp::text::Owned>,
) -> Result<HeaderMap, ProtocolError> {
    let entries = headers.get_entries()?;
    let entry_count = entries.len() as usize;
    validate_quic_header_count(entry_count)?;
    let mut header_map = HeaderMap::with_capacity(entry_count);
    for entry in entries {
        let (key, value) = capnp_entry_text(entry, ("header key", "header value"))?;
        append_header_entry(&mut header_map, key, value)?;
    }
    Ok(header_map)
}

fn entries_from_capnp_map(
    map: quic_capnp::map::Reader<'_, capnp::text::Owned, capnp::text::Owned>,
    labels: (&str, &str),
) -> Result<Vec<(String, String)>, ProtocolError> {
    let entries = map.get_entries()?;
    validate_quic_header_count(entries.len() as usize)?;
    let mut output = Vec::with_capacity(entries.len() as usize);
    for entry in entries {
        let (key, value) = capnp_entry_text(entry, labels)?;
        output.push((key.to_owned(), value.to_owned()));
    }
    Ok(output)
}

fn capnp_entry_text<'a>(
    entry: quic_capnp::map::entry::Reader<'a, capnp::text::Owned, capnp::text::Owned>,
    (key_label, value_label): (&str, &str),
) -> Result<(&'a str, &'a str), ProtocolError> {
    let key = entry
        .get_key()?
        .to_str()
        .map_err(|error| ProtocolError::InvalidHeader(format!("non-UTF8 {key_label}: {error}")))?;
    let value = entry.get_value()?.to_str().map_err(|error| {
        ProtocolError::InvalidHeader(format!("non-UTF8 {value_label}: {error}"))
    })?;
    Ok((key, value))
}

fn validate_quic_header_count(entry_count: usize) -> Result<(), ProtocolError> {
    if entry_count > MAX_QUIC_HEADER_COUNT {
        return Err(raw_quic_header_too_large(entry_count));
    }
    Ok(())
}

#[derive(Debug, Clone)]
pub struct HandshakeRequest {
    pub inference_server_id: String,
    pub auth_token: Option<String>,
}

impl HandshakeRequest {
    pub fn to_builder(&self) -> Result<TypedBuilder<quic_capnp::handshake::Owned>, ProtocolError> {
        let mut builder = typed_builder::<quic_capnp::handshake::Owned>();
        let mut root = builder.get_root()?;
        root.reborrow()
            .set_inference_server_id(&self.inference_server_id);
        if let Some(token) = &self.auth_token {
            root.reborrow().set_auth_token(token);
        }
        Ok(builder)
    }

    pub fn from_reader(
        reader: TypedReader<capnp::serialize::OwnedSegments, quic_capnp::handshake::Owned>,
    ) -> Result<Self, ProtocolError> {
        let root = reader.get()?;
        let inference_server_id = protocol_text(
            root.get_inference_server_id()?,
            "inference_server_id in handshake",
        )?;
        let auth_token = root
            .has_auth_token()
            .then(|| protocol_text(root.get_auth_token()?, "auth_token in handshake"))
            .transpose()?;
        Ok(Self {
            inference_server_id,
            auth_token,
        })
    }
}

#[derive(Debug, Clone)]
pub struct HandshakeAck {
    pub accepted: bool,
    pub reason: String,
}

impl HandshakeAck {
    pub fn to_builder(
        &self,
    ) -> Result<TypedBuilder<quic_capnp::handshake_ack::Owned>, ProtocolError> {
        let mut builder = typed_builder::<quic_capnp::handshake_ack::Owned>();
        let mut root = builder.get_root()?;
        root.reborrow().set_accepted(self.accepted);
        root.reborrow().set_reason(&self.reason);
        Ok(builder)
    }

    pub fn from_reader(
        reader: TypedReader<capnp::serialize::OwnedSegments, quic_capnp::handshake_ack::Owned>,
    ) -> Result<Self, ProtocolError> {
        let root = reader.get()?;
        Ok(Self {
            accepted: root.get_accepted(),
            reason: protocol_text(root.get_reason()?, "reason in handshake ack")?,
        })
    }
}

fn protocol_text(text: capnp::text::Reader<'_>, label: &str) -> Result<String, ProtocolError> {
    text.to_str()
        .map(str::to_owned)
        .map_err(|error| ProtocolError::ProtocolViolation(format!("non-UTF8 {label}: {error}")))
}

async fn write_capnp<T: Owned>(
    tx: &mut quinn::SendStream,
    builder: TypedBuilder<T>,
) -> Result<(), ProtocolError> {
    capnp_futures::serialize::write_message(tx, builder.borrow_inner())
        .await
        .map_err(io_error)
}

async fn read_required<T: Owned>(
    rx: &mut quinn::RecvStream,
    missing: &'static str,
) -> Result<TypedReader<capnp::serialize::OwnedSegments, T>, ProtocolError> {
    read_from_stream(rx)
        .await?
        .ok_or_else(|| ProtocolError::ProtocolViolation(missing.to_string()))
}

pub async fn write_handshake(
    tx: &mut quinn::SendStream,
    request: &HandshakeRequest,
) -> Result<(), ProtocolError> {
    write_capnp(tx, request.to_builder()?).await
}

pub async fn read_handshake(rx: &mut quinn::RecvStream) -> Result<HandshakeRequest, ProtocolError> {
    HandshakeRequest::from_reader(read_required(rx, "expected handshake message, got none").await?)
}

pub async fn write_handshake_ack(
    tx: &mut quinn::SendStream,
    ack: &HandshakeAck,
) -> Result<(), ProtocolError> {
    write_capnp(tx, ack.to_builder()?).await
}

pub async fn read_handshake_ack(rx: &mut quinn::RecvStream) -> Result<HandshakeAck, ProtocolError> {
    HandshakeAck::from_reader(read_required(rx, "expected handshake ack message, got none").await?)
}

pub enum StreamStopCode {
    Ok = 0,
    Cancelled = 1,
    Unknown = 2,
    GoAway = 3,
}

impl From<StreamStopCode> for VarInt {
    fn from(val: StreamStopCode) -> Self {
        VarInt::from_u32(val as u32)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn message_reader<T: Owned>(encoded: &[u8]) -> TypedReader<capnp::serialize::OwnedSegments, T> {
        let mut encoded = encoded;
        TypedReader::new(
            capnp::serialize::read_message(&mut encoded, capnp::message::ReaderOptions::default())
                .unwrap(),
        )
    }

    fn decode_header_map(
        encoded: &[u8],
        kind: HeaderMapMessageKind,
    ) -> Result<HeaderMap, ProtocolError> {
        let reader = message_reader::<quic_capnp::message::Owned>(encoded);
        let map = match (kind, reader.get().unwrap().which().unwrap()) {
            (HeaderMapMessageKind::Header, quic_capnp::message::Which::Headers(map))
            | (HeaderMapMessageKind::Trailer, quic_capnp::message::Which::Trailers(map)) => map?,
            _ => panic!("unexpected raw QUIC message kind"),
        };
        header_map_from_capnp_map(map)
    }

    fn assert_protocol_violation<T>(result: Result<T, ProtocolError>, expected: &str) {
        let Err(ProtocolError::ProtocolViolation(message)) = result else {
            panic!("expected protocol violation")
        };
        assert!(message.contains(expected));
    }

    fn body_message_builder(content: &[u8]) -> TypedBuilder<quic_capnp::message::Owned> {
        let mut builder = typed_builder::<quic_capnp::message::Owned>();
        let mut root = builder.get_root().unwrap();
        root.reborrow().init_body().set_content(content);
        builder
    }

    fn header_map_message_builder(
        headers: &HeaderMap,
        kind: HeaderMapMessageKind,
    ) -> TypedBuilder<quic_capnp::message::Owned> {
        let entries = headers
            .iter()
            .map(|(key, value)| (key.to_string(), value.to_str().unwrap().to_owned()))
            .collect();
        match kind {
            HeaderMapMessageKind::Header => QuicMessage::Header(QuicHeader { entries }),
            HeaderMapMessageKind::Trailer => QuicMessage::Trailer(QuicTrailer { entries }),
        }
        .to_builder()
        .unwrap()
    }

    fn oversized_entry_message_builder(
        kind: HeaderMapMessageKind,
    ) -> TypedBuilder<quic_capnp::message::Owned> {
        let mut builder = typed_builder::<quic_capnp::message::Owned>();
        let mut root = builder.get_root().unwrap();
        let entry_count = (MAX_QUIC_HEADER_COUNT + 1) as u32;
        match kind {
            HeaderMapMessageKind::Header => root.reborrow().init_headers(),
            HeaderMapMessageKind::Trailer => root.reborrow().init_trailers(),
        }
        .init_entries(entry_count);
        builder
    }

    #[test]
    fn raw_quic_parser_rejects_excessive_header_and_trailer_entries() {
        for kind in [HeaderMapMessageKind::Header, HeaderMapMessageKind::Trailer] {
            let encoded = serialize_message(oversized_entry_message_builder(kind));
            assert_protocol_violation(
                decode_header_map(&encoded, kind),
                "too many raw QUIC tunnel headers",
            );
            assert_protocol_violation(
                QuicMessage::from_reader(message_reader(&encoded)),
                "too many raw QUIC tunnel headers",
            );
        }
    }

    #[test]
    fn quic_message_public_roundtrip_preserves_variants_and_entries() {
        use QuicMessage::{Body, Header, Trailer};
        let entries = [("a", "1"), ("a", "2"), ("b", "3")]
            .map(|(key, value)| (key.to_owned(), value.to_owned()))
            .to_vec();
        let header = Header(QuicHeader {
            entries: entries.clone(),
        });
        let body = Body(QuicBody {
            content: Bytes::from_static(b"body"),
        });
        let trailer = Trailer(QuicTrailer { entries });
        for message in [header, body, trailer] {
            let encoded = serialize_message(message.to_builder().unwrap());
            let decoded = QuicMessage::from_reader(message_reader(&encoded)).unwrap();
            assert_eq!(serialize_message(decoded.to_builder().unwrap()), encoded);
        }
    }

    #[test]
    fn multi_value_header_and_trailer_roundtrip() {
        use HeaderMapMessageKind::{Header, Trailer};
        for (kind, name, expected) in [
            (Header, "set-cookie", ["a=1", "b=2"]),
            (Trailer, "x-multi", ["first", "second"]),
        ] {
            let mut headers = HeaderMap::new();
            for value in expected {
                headers.append(name, value.parse().unwrap());
            }
            let encoded = serialize_header_map_message(&headers, kind).unwrap();
            let decoded = decode_header_map(&encoded, kind).unwrap();
            let actual: Vec<_> = decoded
                .get_all(name)
                .iter()
                .map(|value| value.to_str().unwrap())
                .collect();
            assert_eq!(actual, expected);
        }
    }

    #[test]
    fn optimized_body_serialization_matches_capnp_builder_wire_format() {
        for len in [0_usize, 1, 7, 8, 9, 31, 32, 33, 1024] {
            let content: Bytes = (0..len).map(|index| index as u8).collect::<Vec<_>>().into();
            let (prefix, body, padding) = body_message_chunks(content.clone()).unwrap();
            let optimized = Bytes::from([prefix, body, padding].concat());
            let reference = serialize_message(body_message_builder(&content));
            assert_eq!(optimized, reference, "body length {len}");
        }
    }

    #[test]
    fn optimized_body_serialization_rejects_unrepresentable_capnp_data_length() {
        assert_eq!(
            checked_body_list_element_count(CAPNP_LIST_ELEMENT_COUNT_MAX).unwrap(),
            CAPNP_LIST_ELEMENT_COUNT_MAX as u32
        );

        assert_protocol_violation(
            checked_body_list_element_count(CAPNP_LIST_ELEMENT_COUNT_MAX + 1),
            "raw QUIC tunnel body too large",
        );
    }

    #[test]
    fn optimized_header_serialization_matches_capnp_builder_wire_format() {
        let mut populated = HeaderMap::new();
        populated.append("x-method", "POST".parse().unwrap());
        populated.append("x-path", "/v1/chat/completions".parse().unwrap());
        populated.append("set-cookie", "a=1".parse().unwrap());
        populated.append("set-cookie", "b=2".parse().unwrap());

        for headers in [HeaderMap::new(), populated] {
            for kind in [HeaderMapMessageKind::Header, HeaderMapMessageKind::Trailer] {
                let optimized = serialize_header_map_message(&headers, kind).unwrap();
                let reference = serialize_message(header_map_message_builder(&headers, kind));

                assert_eq!(
                    optimized,
                    reference,
                    "{kind:?} with {} headers",
                    headers.len()
                );
            }
        }
    }

    #[test]
    fn optimized_header_serialization_rejects_unrepresentable_capnp_text_length() {
        let max_text_without_nul = CAPNP_LIST_ELEMENT_COUNT_MAX - 1;
        assert_eq!(
            capnp_text_word_len(max_text_without_nul).unwrap(),
            CAPNP_LIST_ELEMENT_COUNT_MAX.div_ceil(CAPNP_BYTES_PER_WORD)
        );

        let word = capnp_text_pointer_word(0, 1, max_text_without_nul).unwrap();
        assert_eq!(word >> 35, CAPNP_LIST_ELEMENT_COUNT_MAX as u64);

        assert_protocol_violation(
            capnp_text_word_len(CAPNP_LIST_ELEMENT_COUNT_MAX),
            "raw QUIC tunnel header field too large",
        );
        assert_protocol_violation(
            capnp_text_pointer_word(0, 1, CAPNP_LIST_ELEMENT_COUNT_MAX),
            "raw QUIC tunnel header field too large",
        );
    }

    #[test]
    fn optimized_header_serialization_rejects_unrepresentable_capnp_text_offset() {
        let word = capnp_text_pointer_word(0, CAPNP_POINTER_OFFSET_WORDS_MAX + 1, 0).unwrap();
        assert_eq!(
            (word >> 2) & 0x3fff_ffff,
            CAPNP_POINTER_OFFSET_WORDS_MAX as u64
        );

        assert_protocol_violation(
            capnp_text_pointer_word(0, CAPNP_POINTER_OFFSET_WORDS_MAX + 2, 0),
            "raw QUIC tunnel header text offset too large",
        );
    }

    #[test]
    fn quic_message_reader_preserves_malformed_discriminant_error() {
        let mut encoded =
            serialize_header_map_message(&HeaderMap::new(), HeaderMapMessageKind::Header)
                .unwrap()
                .to_vec();
        encoded[16..18].copy_from_slice(&u16::MAX.to_le_bytes());
        assert_protocol_violation(
            QuicMessage::from_reader(message_reader(&encoded)),
            "unknown QuicMessage discriminant",
        );
    }

    #[test]
    fn handshake_reports_malformed_inference_id_before_malformed_auth_token() {
        let mut builder = typed_builder::<quic_capnp::handshake::Owned>();
        let mut root = builder.get_root().unwrap();
        let malformed = capnp::text::Reader(&[0xff]);
        root.reborrow().set_inference_server_id(malformed);
        root.reborrow().set_auth_token(malformed);
        let encoded = capnp::serialize::write_message_to_words(builder.borrow_inner());
        let reader = message_reader(&encoded);
        assert_protocol_violation(
            HandshakeRequest::from_reader(reader),
            "non-UTF8 inference_server_id in handshake:",
        );
    }
}
