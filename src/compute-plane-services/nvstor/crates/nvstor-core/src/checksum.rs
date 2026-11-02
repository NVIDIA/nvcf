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

//! Checksum utilities.
//!
//! Two distinct 32-bit CRCs, which must not be confused:
//!
//! - [`crc32`] — IEEE CRC-32 (polynomial 0x04C11DB7, reflected 0xEDB88320),
//!   via `crc32fast`. This is the checksum persisted alongside every
//!   `ChunkManager` chunk and the S3 `x-amz-checksum-crc32` header. Its
//!   algorithm is part of the on-disk format and must never change.
//! - [`crc32c`] — CRC-32C / Castagnoli (polynomial 0x1EDC6F41, reflected
//!   0x82F63B78). Used for the S3 `x-amz-checksum-crc32c` header and the
//!   per-range integrity check on the object transports. Hardware
//!   accelerated: SSE4.2 `crc32` on x86_64, ARMv8 `crc32c*` on aarch64
//!   (Grace), with a slicing-by-8 table fallback.

use crate::error::{NvstorError, Result};

/// Compute the IEEE CRC-32 of `data`.
pub fn crc32(data: &[u8]) -> u32 {
    crc32fast::hash(data)
}

/// Verify that `data` matches `expected` IEEE CRC-32.
///
/// Returns `Ok(())` on match, or [`NvstorError::ChecksumMismatch`] on failure.
pub fn verify_crc32(data: &[u8], expected: u32) -> Result<()> {
    let actual = crc32(data);
    if actual == expected {
        Ok(())
    } else {
        Err(NvstorError::ChecksumMismatch { expected, actual })
    }
}

/// Compute the CRC-32C (Castagnoli) of `data`.
pub fn crc32c(data: &[u8]) -> u32 {
    crc32c_append(0, data)
}

/// Extend a CRC-32C over more bytes: `crc32c_append(crc32c(a), b)`
/// equals `crc32c(a ++ b)`. Lets a caller checksum a range that arrives
/// in pieces without buffering it.
pub fn crc32c_append(crc: u32, data: &[u8]) -> u32 {
    !crc32c_update(!crc, data)
}

/// Verify that `data` matches `expected` CRC-32C.
///
/// Returns `Ok(())` on match, or [`NvstorError::ChecksumMismatch`] on failure.
pub fn verify_crc32c(data: &[u8], expected: u32) -> Result<()> {
    let actual = crc32c(data);
    if actual == expected {
        Ok(())
    } else {
        Err(NvstorError::ChecksumMismatch { expected, actual })
    }
}

/// Raw CRC-32C register update (no pre/post inversion).
fn crc32c_update(state: u32, data: &[u8]) -> u32 {
    #[cfg(target_arch = "x86_64")]
    {
        if std::is_x86_feature_detected!("sse4.2") {
            // SAFETY: the sse4.2 feature was detected at runtime.
            return unsafe { update_sse42(state, data) };
        }
    }
    #[cfg(target_arch = "aarch64")]
    {
        if std::arch::is_aarch64_feature_detected!("crc") {
            // SAFETY: the crc feature was detected at runtime.
            return unsafe { update_armv8(state, data) };
        }
    }
    update_table(state, data)
}

#[cfg(target_arch = "x86_64")]
#[target_feature(enable = "sse4.2")]
unsafe fn update_sse42(state: u32, data: &[u8]) -> u32 {
    use std::arch::x86_64::{_mm_crc32_u64, _mm_crc32_u8};
    let mut crc = state as u64;
    let mut words = data.chunks_exact(8);
    for w in &mut words {
        let v = u64::from_le_bytes(
            w.try_into()
                .expect("invariant: chunks_exact(8) yields 8 bytes"),
        );
        crc = _mm_crc32_u64(crc, v);
    }
    let mut crc = crc as u32;
    for &b in words.remainder() {
        crc = _mm_crc32_u8(crc, b);
    }
    crc
}

#[cfg(target_arch = "aarch64")]
#[target_feature(enable = "crc")]
unsafe fn update_armv8(state: u32, data: &[u8]) -> u32 {
    use std::arch::aarch64::{__crc32cb, __crc32cd};
    let mut crc = state;
    let mut words = data.chunks_exact(8);
    for w in &mut words {
        let v = u64::from_le_bytes(
            w.try_into()
                .expect("invariant: chunks_exact(8) yields 8 bytes"),
        );
        crc = __crc32cd(crc, v);
    }
    for &b in words.remainder() {
        crc = __crc32cb(crc, b);
    }
    crc
}

const CASTAGNOLI_REFLECTED: u32 = 0x82F6_3B78;

/// Slicing-by-8 tables, built at compile time.
static TABLES: [[u32; 256]; 8] = build_tables();

const fn build_tables() -> [[u32; 256]; 8] {
    let mut t = [[0u32; 256]; 8];
    let mut i = 0;
    while i < 256 {
        let mut c = i as u32;
        let mut k = 0;
        while k < 8 {
            c = if c & 1 != 0 {
                (c >> 1) ^ CASTAGNOLI_REFLECTED
            } else {
                c >> 1
            };
            k += 1;
        }
        t[0][i] = c;
        i += 1;
    }
    let mut s = 1;
    while s < 8 {
        let mut i = 0;
        while i < 256 {
            let prev = t[s - 1][i];
            t[s][i] = (prev >> 8) ^ t[0][(prev & 0xFF) as usize];
            i += 1;
        }
        s += 1;
    }
    t
}

fn update_table(state: u32, data: &[u8]) -> u32 {
    let mut crc = state;
    let mut words = data.chunks_exact(8);
    for w in &mut words {
        let lo = crc ^ u32::from_le_bytes([w[0], w[1], w[2], w[3]]);
        let hi = u32::from_le_bytes([w[4], w[5], w[6], w[7]]);
        crc = TABLES[7][(lo & 0xFF) as usize]
            ^ TABLES[6][((lo >> 8) & 0xFF) as usize]
            ^ TABLES[5][((lo >> 16) & 0xFF) as usize]
            ^ TABLES[4][(lo >> 24) as usize]
            ^ TABLES[3][(hi & 0xFF) as usize]
            ^ TABLES[2][((hi >> 8) & 0xFF) as usize]
            ^ TABLES[1][((hi >> 16) & 0xFF) as usize]
            ^ TABLES[0][(hi >> 24) as usize];
    }
    for &b in words.remainder() {
        crc = (crc >> 8) ^ TABLES[0][((crc ^ b as u32) & 0xFF) as usize];
    }
    crc
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn crc32_ieee_check_value() {
        // Standard CRC-32/ISO-HDLC check value.
        assert_eq!(crc32(b"123456789"), 0xCBF4_3926);
        assert_eq!(crc32(b""), 0);
    }

    #[test]
    fn verify_crc32_ok_and_mismatch() {
        let data = b"verify me";
        assert!(verify_crc32(data, crc32(data)).is_ok());
        let err = verify_crc32(data, crc32(data) ^ 0xFF).unwrap_err();
        assert!(matches!(err, NvstorError::ChecksumMismatch { .. }));
    }

    #[test]
    fn crc32c_check_value() {
        // Standard CRC-32C/ISCSI check value.
        assert_eq!(crc32c(b"123456789"), 0xE306_9283);
        assert_eq!(crc32c(b""), 0);
    }

    #[test]
    fn crc32c_rfc3720_vectors() {
        // RFC 3720 §B.4 test vectors.
        assert_eq!(crc32c(&[0u8; 32]), 0x8A91_36AA);
        assert_eq!(crc32c(&[0xFFu8; 32]), 0x62A8_AB43);
        let inc: Vec<u8> = (0u8..32).collect();
        assert_eq!(crc32c(&inc), 0x46DD_794E);
        let dec: Vec<u8> = (0u8..32).rev().collect();
        assert_eq!(crc32c(&dec), 0x113F_DB5C);
    }

    #[test]
    fn crc32c_differs_from_ieee() {
        assert_ne!(crc32c(b"123456789"), crc32(b"123456789"));
    }

    #[test]
    fn crc32c_hardware_matches_table() {
        // Every length and alignment around the 8-byte word boundary.
        let buf: Vec<u8> = (0..4096u32)
            .map(|i| (i.wrapping_mul(2_654_435_761) >> 13) as u8)
            .collect();
        for start in 0..9 {
            for len in [0, 1, 7, 8, 9, 15, 16, 17, 63, 64, 65, 1000, 4000] {
                let s = &buf[start..start + len];
                assert_eq!(
                    crc32c_update(!0, s),
                    update_table(!0, s),
                    "start={start} len={len}"
                );
            }
        }
    }

    #[test]
    fn crc32c_append_equals_whole() {
        let data: Vec<u8> = (0..10_000u32).map(|i| i as u8).collect();
        for split in [0, 1, 8, 13, 4096, 9999, 10_000] {
            let (a, b) = data.split_at(split);
            assert_eq!(crc32c_append(crc32c(a), b), crc32c(&data), "split={split}");
        }
    }

    #[test]
    fn verify_crc32c_ok_and_mismatch() {
        let data = b"verify me";
        assert!(verify_crc32c(data, crc32c(data)).is_ok());
        let err = verify_crc32c(data, crc32c(data) ^ 0xFF).unwrap_err();
        assert!(matches!(err, NvstorError::ChecksumMismatch { .. }));
    }
}
