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

//! XDR (External Data Representation) encoding/decoding per RFC 4506.
//!
//! All NFS messages are XDR-encoded. This module provides the low-level
//! codec used by the RPC and NFS layers.
//!
//! Key rules:
//! - All integers are big-endian (network byte order)
//! - All data is padded to 4-byte boundaries
//! - Variable-length data prefixed with u32 length

use std::fmt;

/// XDR encoding errors.
#[derive(Debug)]
pub enum XdrError {
    /// Not enough data to decode.
    Underflow { needed: usize, available: usize },
    /// String is not valid UTF-8.
    InvalidUtf8,
    /// Data exceeds maximum allowed length.
    TooLong { len: usize, max: usize },
}

impl fmt::Display for XdrError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Underflow { needed, available } => {
                write!(f, "XDR underflow: need {needed} bytes, have {available}")
            }
            Self::InvalidUtf8 => write!(f, "XDR string is not valid UTF-8"),
            Self::TooLong { len, max } => {
                write!(f, "XDR data too long: {len} > {max}")
            }
        }
    }
}

impl std::error::Error for XdrError {}

/// Padding to align to 4-byte boundary.
fn pad_len(len: usize) -> usize {
    (4 - (len % 4)) % 4
}

/// Maximum size of a single XDR opaque field, in bytes.
///
/// Guards against malicious clients advertising a 4 GB length to force the
/// server to allocate before the bytes are actually on the wire. NFS READ /
/// WRITE payloads are still bounded by this; tune upward if we ever want to
/// advertise a larger `FATTR4_MAXREAD` / `FATTR4_MAXWRITE`.
pub const MAX_OPAQUE_LEN: usize = 64 * 1024 * 1024; // 64 MiB

// ============================================================================
// Writer
// ============================================================================

/// XDR encoder — writes big-endian values to an internal buffer.
pub struct XdrWriter {
    buf: Vec<u8>,
}

impl XdrWriter {
    pub fn new() -> Self {
        Self {
            buf: Vec::with_capacity(256),
        }
    }

    pub fn with_capacity(cap: usize) -> Self {
        Self {
            buf: Vec::with_capacity(cap),
        }
    }

    pub fn put_u32(&mut self, v: u32) {
        self.buf.extend_from_slice(&v.to_be_bytes());
    }

    pub fn put_i32(&mut self, v: i32) {
        self.buf.extend_from_slice(&v.to_be_bytes());
    }

    pub fn put_u64(&mut self, v: u64) {
        self.buf.extend_from_slice(&v.to_be_bytes());
    }

    pub fn put_i64(&mut self, v: i64) {
        self.buf.extend_from_slice(&v.to_be_bytes());
    }

    pub fn put_bool(&mut self, v: bool) {
        self.put_u32(if v { 1 } else { 0 });
    }

    /// Write exactly `data.len()` bytes, padded to 4-byte boundary.
    pub fn put_opaque_fixed(&mut self, data: &[u8]) {
        self.buf.extend_from_slice(data);
        let pad = pad_len(data.len());
        for _ in 0..pad {
            self.buf.push(0);
        }
    }

    /// Write variable-length opaque: u32 length + data + padding.
    pub fn put_opaque(&mut self, data: &[u8]) {
        self.put_u32(data.len() as u32);
        self.buf.extend_from_slice(data);
        let pad = pad_len(data.len());
        for _ in 0..pad {
            self.buf.push(0);
        }
    }

    /// Write a string (variable-length opaque with UTF-8 content).
    pub fn put_string(&mut self, s: &str) {
        self.put_opaque(s.as_bytes());
    }

    /// Write raw bytes without length prefix or padding.
    pub fn put_raw(&mut self, data: &[u8]) {
        self.buf.extend_from_slice(data);
    }

    /// Consume the writer and return the encoded bytes.
    pub fn into_bytes(self) -> Vec<u8> {
        self.buf
    }

    /// Current encoded length.
    pub fn len(&self) -> usize {
        self.buf.len()
    }

    /// Is the buffer empty?
    pub fn is_empty(&self) -> bool {
        self.buf.is_empty()
    }
}

impl Default for XdrWriter {
    fn default() -> Self {
        Self::new()
    }
}

// ============================================================================
// Reader
// ============================================================================

/// XDR decoder — reads big-endian values from a byte slice.
pub struct XdrReader<'a> {
    buf: &'a [u8],
    pos: usize,
}

impl<'a> XdrReader<'a> {
    pub fn new(data: &'a [u8]) -> Self {
        Self { buf: data, pos: 0 }
    }

    fn ensure(&self, n: usize) -> Result<(), XdrError> {
        if self.pos + n > self.buf.len() {
            Err(XdrError::Underflow {
                needed: n,
                available: self.buf.len() - self.pos,
            })
        } else {
            Ok(())
        }
    }

    pub fn get_u32(&mut self) -> Result<u32, XdrError> {
        self.ensure(4)?;
        let v = u32::from_be_bytes(
            self.buf[self.pos..self.pos + 4]
                .try_into()
                .expect("fixed-size slice"),
        );
        self.pos += 4;
        Ok(v)
    }

    pub fn get_i32(&mut self) -> Result<i32, XdrError> {
        self.ensure(4)?;
        let v = i32::from_be_bytes(
            self.buf[self.pos..self.pos + 4]
                .try_into()
                .expect("fixed-size slice"),
        );
        self.pos += 4;
        Ok(v)
    }

    pub fn get_u64(&mut self) -> Result<u64, XdrError> {
        self.ensure(8)?;
        let v = u64::from_be_bytes(
            self.buf[self.pos..self.pos + 8]
                .try_into()
                .expect("fixed-size slice"),
        );
        self.pos += 8;
        Ok(v)
    }

    pub fn get_i64(&mut self) -> Result<i64, XdrError> {
        self.ensure(8)?;
        let v = i64::from_be_bytes(
            self.buf[self.pos..self.pos + 8]
                .try_into()
                .expect("fixed-size slice"),
        );
        self.pos += 8;
        Ok(v)
    }

    pub fn get_bool(&mut self) -> Result<bool, XdrError> {
        Ok(self.get_u32()? != 0)
    }

    /// Read exactly `len` bytes of fixed-length opaque, consuming padding.
    pub fn get_opaque_fixed(&mut self, len: usize) -> Result<Vec<u8>, XdrError> {
        if len > MAX_OPAQUE_LEN {
            return Err(XdrError::TooLong {
                len,
                max: MAX_OPAQUE_LEN,
            });
        }
        let padded = len + pad_len(len);
        self.ensure(padded)?;
        let data = self.buf[self.pos..self.pos + len].to_vec();
        self.pos += padded;
        Ok(data)
    }

    /// Read variable-length opaque: u32 length + data + padding.
    ///
    /// Rejects lengths exceeding `MAX_OPAQUE_LEN` before allocating so a
    /// malicious client can't make us allocate a 4 GB buffer by claiming it's
    /// coming.
    pub fn get_opaque(&mut self) -> Result<Vec<u8>, XdrError> {
        let len = self.get_u32()? as usize;
        self.get_opaque_fixed(len)
    }

    /// Read a string (variable-length opaque, UTF-8 validated).
    pub fn get_string(&mut self) -> Result<String, XdrError> {
        let bytes = self.get_opaque()?;
        String::from_utf8(bytes).map_err(|_| XdrError::InvalidUtf8)
    }

    /// Read raw bytes without padding.
    pub fn get_raw(&mut self, len: usize) -> Result<Vec<u8>, XdrError> {
        self.ensure(len)?;
        let data = self.buf[self.pos..self.pos + len].to_vec();
        self.pos += len;
        Ok(data)
    }

    /// Remaining unread bytes.
    pub fn remaining(&self) -> usize {
        self.buf.len() - self.pos
    }

    /// Current read position.
    pub fn position(&self) -> usize {
        self.pos
    }

    /// Remaining bytes as a slice.
    pub fn remaining_bytes(&self) -> &'a [u8] {
        &self.buf[self.pos..]
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn u32_roundtrip() {
        let mut w = XdrWriter::new();
        w.put_u32(0);
        w.put_u32(42);
        w.put_u32(u32::MAX);
        let data = w.into_bytes();
        assert_eq!(data.len(), 12);

        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_u32().unwrap(), 0);
        assert_eq!(r.get_u32().unwrap(), 42);
        assert_eq!(r.get_u32().unwrap(), u32::MAX);
        assert_eq!(r.remaining(), 0);
    }

    #[test]
    fn i32_roundtrip() {
        let mut w = XdrWriter::new();
        w.put_i32(-1);
        w.put_i32(i32::MIN);
        w.put_i32(i32::MAX);
        let data = w.into_bytes();
        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_i32().unwrap(), -1);
        assert_eq!(r.get_i32().unwrap(), i32::MIN);
        assert_eq!(r.get_i32().unwrap(), i32::MAX);
    }

    #[test]
    fn u64_roundtrip() {
        let mut w = XdrWriter::new();
        w.put_u64(0x0102030405060708);
        let data = w.into_bytes();
        assert_eq!(data.len(), 8);
        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_u64().unwrap(), 0x0102030405060708);
    }

    #[test]
    fn i64_roundtrip() {
        let mut w = XdrWriter::new();
        w.put_i64(-12345678901234);
        let data = w.into_bytes();
        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_i64().unwrap(), -12345678901234);
    }

    #[test]
    fn bool_roundtrip() {
        let mut w = XdrWriter::new();
        w.put_bool(true);
        w.put_bool(false);
        let data = w.into_bytes();
        assert_eq!(data.len(), 8); // 2 × u32
        let mut r = XdrReader::new(&data);
        assert!(r.get_bool().unwrap());
        assert!(!r.get_bool().unwrap());
    }

    #[test]
    fn opaque_fixed_with_padding() {
        // 3 bytes → padded to 4
        let mut w = XdrWriter::new();
        w.put_opaque_fixed(&[1, 2, 3]);
        let data = w.into_bytes();
        assert_eq!(data.len(), 4); // 3 + 1 pad
        assert_eq!(data, &[1, 2, 3, 0]);

        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_opaque_fixed(3).unwrap(), &[1, 2, 3]);
        assert_eq!(r.remaining(), 0);
    }

    #[test]
    fn opaque_fixed_exact_alignment() {
        // 4 bytes → no padding needed
        let mut w = XdrWriter::new();
        w.put_opaque_fixed(&[1, 2, 3, 4]);
        let data = w.into_bytes();
        assert_eq!(data.len(), 4);
    }

    #[test]
    fn opaque_variable() {
        let mut w = XdrWriter::new();
        w.put_opaque(&[0xAB, 0xCD, 0xEF]);
        let data = w.into_bytes();
        // 4 (length) + 3 (data) + 1 (pad) = 8
        assert_eq!(data.len(), 8);

        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_opaque().unwrap(), &[0xAB, 0xCD, 0xEF]);
    }

    #[test]
    fn string_roundtrip() {
        let mut w = XdrWriter::new();
        w.put_string("hello");
        let data = w.into_bytes();
        // 4 (len) + 5 (data) + 3 (pad) = 12
        assert_eq!(data.len(), 12);

        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_string().unwrap(), "hello");
    }

    #[test]
    fn empty_string() {
        let mut w = XdrWriter::new();
        w.put_string("");
        let data = w.into_bytes();
        assert_eq!(data.len(), 4); // Just the length=0

        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_string().unwrap(), "");
    }

    #[test]
    fn empty_opaque() {
        let mut w = XdrWriter::new();
        w.put_opaque(&[]);
        let data = w.into_bytes();
        assert_eq!(data.len(), 4);

        let mut r = XdrReader::new(&data);
        assert!(r.get_opaque().unwrap().is_empty());
    }

    #[test]
    fn underflow_error() {
        let data = [0u8; 2];
        let mut r = XdrReader::new(&data);
        let err = r.get_u32().unwrap_err();
        assert!(matches!(
            err,
            XdrError::Underflow {
                needed: 4,
                available: 2
            }
        ));
    }

    #[test]
    fn multiple_values_sequence() {
        let mut w = XdrWriter::new();
        w.put_u32(1);
        w.put_string("test");
        w.put_u64(999);
        w.put_bool(true);
        w.put_opaque(&[0xAA; 5]);

        let data = w.into_bytes();
        let mut r = XdrReader::new(&data);
        assert_eq!(r.get_u32().unwrap(), 1);
        assert_eq!(r.get_string().unwrap(), "test");
        assert_eq!(r.get_u64().unwrap(), 999);
        assert!(r.get_bool().unwrap());
        assert_eq!(r.get_opaque().unwrap(), &[0xAA; 5]);
        assert_eq!(r.remaining(), 0);
    }

    #[test]
    fn padding_for_various_sizes() {
        for size in 0..=8 {
            let data = vec![0xBB; size];
            let mut w = XdrWriter::new();
            w.put_opaque(&data);
            let encoded = w.into_bytes();

            let mut r = XdrReader::new(&encoded);
            assert_eq!(r.get_opaque().unwrap(), data, "failed for size={size}");
            assert_eq!(r.remaining(), 0, "leftover bytes for size={size}");
        }
    }

    #[test]
    fn large_opaque() {
        let data = vec![0xCC; 100_000];
        let mut w = XdrWriter::new();
        w.put_opaque(&data);
        let encoded = w.into_bytes();

        let mut r = XdrReader::new(&encoded);
        assert_eq!(r.get_opaque().unwrap(), data);
    }

    /// A malicious client can advertise a length > MAX_OPAQUE_LEN in
    /// an XDR opaque prefix. Must fail before any allocation.
    #[test]
    fn get_opaque_rejects_length_over_max() {
        let oversize = (MAX_OPAQUE_LEN as u32).saturating_add(1);
        let buf = oversize.to_be_bytes().to_vec();
        let mut r = XdrReader::new(&buf);
        let err = r.get_opaque().unwrap_err();
        assert!(
            matches!(err, XdrError::TooLong { max, .. } if max == MAX_OPAQUE_LEN),
            "expected TooLong(max=MAX_OPAQUE_LEN), got {err:?}"
        );
    }

    /// Cap also fires on the fixed-length form used by SEQUENCE / session IDs
    /// if a caller ever passes a len > MAX_OPAQUE_LEN programmatically.
    #[test]
    fn get_opaque_fixed_rejects_length_over_max() {
        let buf = vec![0u8; 16];
        let mut r = XdrReader::new(&buf);
        let err = r.get_opaque_fixed(MAX_OPAQUE_LEN + 1).unwrap_err();
        assert!(matches!(err, XdrError::TooLong { .. }));
    }
}
