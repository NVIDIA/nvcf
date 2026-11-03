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

//! XXH3-64 (seed 0, default secret), written from the xxHash reference
//! (v0.8.3, `XXH3_64bits`) and checked against it.
//!
//! The KV tier's 64-bit block check is
//! XXH3-64 over (namespace ‖ key ‖ payload): a writer computes it while
//! copying a block out, the server stores it and returns it verbatim, and
//! the reader verifies it. Unlike CRC-32C it is not affine, so a block
//! misrouted to another key passes only by chance (about 2⁻⁶⁴).
//!
//! [`Xxh3`] hashes a value given in pieces (the namespace, the key and
//! the payload never need to be copied into one buffer); [`xxh3_64`] is
//! the one-shot form. Both give exactly `XXH3_64bits` of the
//! concatenation.

const PRIME32_1: u64 = 0x9E37_79B1;
const PRIME32_2: u64 = 0x85EB_CA77;
const PRIME32_3: u64 = 0xC2B2_AE3D;
const PRIME64_1: u64 = 0x9E37_79B1_85EB_CA87;
const PRIME64_2: u64 = 0xC2B2_AE3D_27D4_EB4F;
const PRIME64_3: u64 = 0x1656_67B1_9E37_79F9;
const PRIME64_4: u64 = 0x85EB_CA77_C2B2_AE63;
const PRIME64_5: u64 = 0x27D4_EB2F_1656_67C5;
const PRIME_MX1: u64 = 0x1656_6791_9E37_79F9;
const PRIME_MX2: u64 = 0x9FB2_1C65_1E98_DF25;

const STRIPE_LEN: usize = 64;
const SECRET_CONSUME_RATE: usize = 8;
const SECRET_LASTACC_START: usize = 7;
const SECRET_MERGEACCS_START: usize = 11;
const MIDSIZE_MAX: usize = 240;
const MIDSIZE_STARTOFFSET: usize = 3;
const MIDSIZE_LASTOFFSET: usize = 17;
const SECRET_SIZE_MIN: usize = 136;

/// `XXH3_kSecret`, the default secret.
const SECRET: [u8; 192] = [
    0xb8, 0xfe, 0x6c, 0x39, 0x23, 0xa4, 0x4b, 0xbe, 0x7c, 0x01, 0x81, 0x2c, 0xf7, 0x21, 0xad, 0x1c,
    0xde, 0xd4, 0x6d, 0xe9, 0x83, 0x90, 0x97, 0xdb, 0x72, 0x40, 0xa4, 0xa4, 0xb7, 0xb3, 0x67, 0x1f,
    0xcb, 0x79, 0xe6, 0x4e, 0xcc, 0xc0, 0xe5, 0x78, 0x82, 0x5a, 0xd0, 0x7d, 0xcc, 0xff, 0x72, 0x21,
    0xb8, 0x08, 0x46, 0x74, 0xf7, 0x43, 0x24, 0x8e, 0xe0, 0x35, 0x90, 0xe6, 0x81, 0x3a, 0x26, 0x4c,
    0x3c, 0x28, 0x52, 0xbb, 0x91, 0xc3, 0x00, 0xcb, 0x88, 0xd0, 0x65, 0x8b, 0x1b, 0x53, 0x2e, 0xa3,
    0x71, 0x64, 0x48, 0x97, 0xa2, 0x0d, 0xf9, 0x4e, 0x38, 0x19, 0xef, 0x46, 0xa9, 0xde, 0xac, 0xd8,
    0xa8, 0xfa, 0x76, 0x3f, 0xe3, 0x9c, 0x34, 0x3f, 0xf9, 0xdc, 0xbb, 0xc7, 0xc7, 0x0b, 0x4f, 0x1d,
    0x8a, 0x51, 0xe0, 0x4b, 0xcd, 0xb4, 0x59, 0x31, 0xc8, 0x9f, 0x7e, 0xc9, 0xd9, 0x78, 0x73, 0x64,
    0xea, 0xc5, 0xac, 0x83, 0x34, 0xd3, 0xeb, 0xc3, 0xc5, 0x81, 0xa0, 0xff, 0xfa, 0x13, 0x63, 0xeb,
    0x17, 0x0d, 0xdd, 0x51, 0xb7, 0xf0, 0xda, 0x49, 0xd3, 0x16, 0x55, 0x26, 0x29, 0xd4, 0x68, 0x9e,
    0x2b, 0x16, 0xbe, 0x58, 0x7d, 0x47, 0xa1, 0xfc, 0x8f, 0xf8, 0xb8, 0xd1, 0x7a, 0xd0, 0x31, 0xce,
    0x45, 0xcb, 0x3a, 0x8f, 0x95, 0x16, 0x04, 0x28, 0xaf, 0xd7, 0xfb, 0xca, 0xbb, 0x4b, 0x40, 0x7e,
];

/// Stripes per block (a scramble follows each block) with the default secret.
const STRIPES_PER_BLOCK: usize = (SECRET.len() - STRIPE_LEN) / SECRET_CONSUME_RATE;

const INIT_ACC: [u64; 8] = [
    PRIME32_3, PRIME64_1, PRIME64_2, PRIME64_3, PRIME64_4, PRIME32_2, PRIME64_5, PRIME32_1,
];

#[inline(always)]
fn read32(b: &[u8], at: usize) -> u64 {
    u32::from_le_bytes(b[at..at + 4].try_into().expect("invariant: 4-byte slice")) as u64
}
#[inline(always)]
fn read64(b: &[u8], at: usize) -> u64 {
    u64::from_le_bytes(b[at..at + 8].try_into().expect("invariant: 8-byte slice"))
}
#[inline(always)]
fn mul128_fold64(a: u64, b: u64) -> u64 {
    let p = (a as u128).wrapping_mul(b as u128);
    (p as u64) ^ ((p >> 64) as u64)
}
#[inline(always)]
fn xorshift(v: u64, s: u32) -> u64 {
    v ^ (v >> s)
}
fn avalanche(h: u64) -> u64 {
    xorshift(xorshift(h, 37).wrapping_mul(PRIME_MX1), 32)
}
fn xxh64_avalanche(mut h: u64) -> u64 {
    h ^= h >> 33;
    h = h.wrapping_mul(PRIME64_2);
    h ^= h >> 29;
    h = h.wrapping_mul(PRIME64_3);
    h ^ (h >> 32)
}
fn rrmxmx(mut h: u64, len: u64) -> u64 {
    h ^= h.rotate_left(49) ^ h.rotate_left(24);
    h = h.wrapping_mul(PRIME_MX2);
    h ^= (h >> 35).wrapping_add(len);
    h = h.wrapping_mul(PRIME_MX2);
    xorshift(h, 28)
}
fn mix16(input: &[u8], at: usize, secret_at: usize) -> u64 {
    mul128_fold64(
        read64(input, at) ^ read64(&SECRET, secret_at),
        read64(input, at + 8) ^ read64(&SECRET, secret_at + 8),
    )
}

fn len_0to16(input: &[u8]) -> u64 {
    let len = input.len();
    if len > 8 {
        let lo = read64(input, 0) ^ (read64(&SECRET, 24) ^ read64(&SECRET, 32));
        let hi = read64(input, len - 8) ^ (read64(&SECRET, 40) ^ read64(&SECRET, 48));
        let acc = (len as u64)
            .wrapping_add(lo.swap_bytes())
            .wrapping_add(hi)
            .wrapping_add(mul128_fold64(lo, hi));
        avalanche(acc)
    } else if len >= 4 {
        let input64 = read32(input, len - 4).wrapping_add(read32(input, 0) << 32);
        let bitflip = read64(&SECRET, 8) ^ read64(&SECRET, 16);
        rrmxmx(input64 ^ bitflip, len as u64)
    } else if len > 0 {
        let combined = ((input[0] as u32) << 16)
            | ((input[len >> 1] as u32) << 24)
            | (input[len - 1] as u32)
            | ((len as u32) << 8);
        xxh64_avalanche(combined as u64 ^ (read32(&SECRET, 0) ^ read32(&SECRET, 4)))
    } else {
        xxh64_avalanche(read64(&SECRET, 56) ^ read64(&SECRET, 64))
    }
}

fn len_17to128(input: &[u8]) -> u64 {
    let len = input.len();
    let mut acc = (len as u64).wrapping_mul(PRIME64_1);
    if len > 32 {
        if len > 64 {
            if len > 96 {
                acc = acc.wrapping_add(mix16(input, 48, 96)).wrapping_add(mix16(
                    input,
                    len - 64,
                    112,
                ));
            }
            acc = acc
                .wrapping_add(mix16(input, 32, 64))
                .wrapping_add(mix16(input, len - 48, 80));
        }
        acc = acc
            .wrapping_add(mix16(input, 16, 32))
            .wrapping_add(mix16(input, len - 32, 48));
    }
    acc = acc
        .wrapping_add(mix16(input, 0, 0))
        .wrapping_add(mix16(input, len - 16, 16));
    avalanche(acc)
}

fn len_129to240(input: &[u8]) -> u64 {
    let len = input.len();
    let mut acc = (len as u64).wrapping_mul(PRIME64_1);
    for i in 0..8 {
        acc = acc.wrapping_add(mix16(input, 16 * i, 16 * i));
    }
    let mut acc_end = mix16(input, len - 16, SECRET_SIZE_MIN - MIDSIZE_LASTOFFSET);
    acc = avalanche(acc);
    for i in 8..len / 16 {
        acc_end = acc_end.wrapping_add(mix16(input, 16 * i, 16 * (i - 8) + MIDSIZE_STARTOFFSET));
    }
    avalanche(acc.wrapping_add(acc_end))
}

#[inline(always)]
fn accumulate_512(acc: &mut [u64; 8], stripe: &[u8], secret_at: usize) {
    for lane in 0..8 {
        let data = read64(stripe, lane * 8);
        let key = data ^ read64(&SECRET, secret_at + lane * 8);
        acc[lane ^ 1] = acc[lane ^ 1].wrapping_add(data);
        acc[lane] = acc[lane].wrapping_add((key & 0xFFFF_FFFF).wrapping_mul(key >> 32));
    }
}

fn scramble(acc: &mut [u64; 8]) {
    let at = SECRET.len() - STRIPE_LEN;
    for (lane, a) in acc.iter_mut().enumerate() {
        *a = (xorshift(*a, 47) ^ read64(&SECRET, at + lane * 8)).wrapping_mul(PRIME32_1);
    }
}

fn finalize_long(acc: &[u64; 8], len: u64) -> u64 {
    let mut result = len.wrapping_mul(PRIME64_1);
    for i in 0..4 {
        let at = SECRET_MERGEACCS_START + 16 * i;
        result = result.wrapping_add(mul128_fold64(
            acc[2 * i] ^ read64(&SECRET, at),
            acc[2 * i + 1] ^ read64(&SECRET, at + 8),
        ));
    }
    avalanche(result)
}

/// XXH3-64 of `input` (seed 0): `XXH3_64bits`.
pub fn xxh3_64(input: &[u8]) -> u64 {
    let mut h = Xxh3::new();
    h.update(input);
    h.finish()
}

/// Streaming XXH3-64 (seed 0). An input of at most 240 bytes is buffered
/// and hashed by the short paths at [`finish`](Xxh3::finish). A longer one
/// is consumed a stripe at a time, a stripe only once a byte after it has
/// arrived: exactly the reference's "every full stripe of the first
/// len - 1 bytes". The last 64 bytes are kept for the final round.
pub struct Xxh3 {
    acc: [u64; 8],
    /// Unconsumed bytes: the whole input while it is at most 240 bytes,
    /// then at most one stripe.
    buf: Vec<u8>,
    /// The last 64 bytes of the input so far (its tail when shorter).
    last: [u8; STRIPE_LEN],
    /// Stripes consumed in the current block.
    stripes: usize,
    total: u64,
}

impl Default for Xxh3 {
    fn default() -> Self {
        Self::new()
    }
}

impl Xxh3 {
    pub fn new() -> Self {
        Xxh3 {
            acc: INIT_ACC,
            buf: Vec::with_capacity(MIDSIZE_MAX),
            last: [0; STRIPE_LEN],
            stripes: 0,
            total: 0,
        }
    }

    /// Consume one stripe, scrambling at the end of each block.
    fn stripe(&mut self, stripe: &[u8]) {
        accumulate_512(&mut self.acc, stripe, self.stripes * SECRET_CONSUME_RATE);
        self.stripes += 1;
        if self.stripes == STRIPES_PER_BLOCK {
            scramble(&mut self.acc);
            self.stripes = 0;
        }
    }

    pub fn update(&mut self, mut input: &[u8]) {
        if input.is_empty() {
            return;
        }
        self.total += input.len() as u64;
        // Keep the input's last 64 bytes.
        if input.len() >= STRIPE_LEN {
            self.last
                .copy_from_slice(&input[input.len() - STRIPE_LEN..]);
        } else {
            self.last.copy_within(input.len().., 0);
            self.last[STRIPE_LEN - input.len()..].copy_from_slice(input);
        }
        if self.total <= MIDSIZE_MAX as u64 {
            self.buf.extend_from_slice(input);
            return;
        }
        // Buffered bytes first: complete a stripe and consume it once a
        // byte after it exists.
        while !self.buf.is_empty() {
            if self.buf.len() < STRIPE_LEN {
                let take = (STRIPE_LEN - self.buf.len()).min(input.len());
                self.buf.extend_from_slice(&input[..take]);
                input = &input[take..];
            }
            let more = self.buf.len() > STRIPE_LEN || !input.is_empty();
            if self.buf.len() < STRIPE_LEN || !more {
                return;
            }
            let first: [u8; STRIPE_LEN] = self.buf[..STRIPE_LEN]
                .try_into()
                .expect("invariant: one stripe");
            self.stripe(&first);
            self.buf.drain(..STRIPE_LEN);
        }
        // Then straight from the input, holding back its last stripe.
        while input.len() > STRIPE_LEN {
            let (s, rest) = input.split_at(STRIPE_LEN);
            self.stripe(s);
            input = rest;
        }
        self.buf.extend_from_slice(input);
    }

    pub fn finish(&self) -> u64 {
        if self.total <= MIDSIZE_MAX as u64 {
            let input = &self.buf[..];
            return match input.len() {
                0..=16 => len_0to16(input),
                17..=128 => len_17to128(input),
                _ => len_129to240(input),
            };
        }
        // Every full stripe of the first len - 1 bytes is consumed; the
        // final round takes the last 64 bytes under the last-stripe secret.
        let mut acc = self.acc;
        accumulate_512(
            &mut acc,
            &self.last,
            SECRET.len() - STRIPE_LEN - SECRET_LASTACC_START,
        );
        finalize_long(&acc, self.total)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn pattern(n: usize) -> Vec<u8> {
        (0..n).map(|i| ((i * 31 + 7) ^ (i >> 8)) as u8).collect()
    }

    /// From the reference implementation (xxHash v0.8.3, XXH3_64bits),
    /// over `pattern(len)`.
    const VECTORS: &[(usize, u64)] = &[
        (0, 0x2d06800538d394c2),
        (1, 0x4c5cca45d0f4811f),
        (2, 0xa7e250c97710ff27),
        (3, 0x15f7093b173d005c),
        (4, 0xdca012f95811b6b9),
        (5, 0xb290cafc7b254345),
        (7, 0x7561869c23da3c1b),
        (8, 0xdec6a9a43575982e),
        (9, 0xcbe393399f17ffbd),
        (15, 0x545e19990471dc37),
        (16, 0x7e484c18d74895d0),
        (17, 0x208bde5ee2bed407),
        (31, 0xa937652b0119ca11),
        (32, 0x03df0ac5255d1446),
        (33, 0x199a362122d71f46),
        (63, 0x76d4eec1f092847f),
        (64, 0xdd30702ab46b3745),
        (65, 0xfab36b851b94ce20),
        (96, 0xd245cd2541582982),
        (97, 0x60e3e1d0d43785b3),
        (127, 0xa915ed6396db8cc0),
        (128, 0xf92b70eaa21a6288),
        (129, 0xf8f76713f2bb60fa),
        (130, 0x303a2c3823c3c34a),
        (160, 0xc90911ffcef461e2),
        (239, 0xcaa9b7a588464745),
        (240, 0xccc7375172c41f03),
        (241, 0x0b3b630948ce4a00),
        (255, 0x89932170686cdd9a),
        (256, 0xec85b75bafe6ca74),
        (319, 0xabf572b0d8324ec1),
        (320, 0x3f654a8b51b4b1f7),
        (321, 0x4c66fbcc67a8a346),
        (1023, 0x22ff72bf7a712151),
        (1024, 0xd218d699d62a6d8b),
        (1025, 0x38f5f1f86ddfa599),
        (1087, 0x9ba9cf664f00786f),
        (1088, 0x9df03fc840d44bf8),
        (2047, 0xeabacf598d10dfab),
        (2048, 0xe29245b34db354a9),
        (2049, 0xbff009d82d71d075),
        (4096, 0x99aa8007ca97e119),
        (65536, 0x649a1298fb4436c7),
        (100003, 0x42282b032f56d111),
        (3052800, 0xb98b03e269a7b222),
    ];

    #[test]
    fn one_shot_matches_the_reference_on_every_path() {
        let data = pattern(3_052_800);
        for &(len, want) in VECTORS {
            assert_eq!(xxh3_64(&data[..len]), want, "length {len}");
        }
    }

    #[test]
    fn any_split_of_the_input_gives_the_same_hash() {
        let data = pattern(3_052_800);
        for &(len, want) in VECTORS {
            let input = &data[..len];
            // Split points around every boundary that matters.
            for cut in [
                0,
                1,
                3,
                16,
                17,
                63,
                64,
                65,
                128,
                129,
                240,
                241,
                1023,
                1024,
                1025,
                len / 2,
                len.saturating_sub(1),
            ] {
                let cut = cut.min(len);
                let mut h = Xxh3::new();
                h.update(&input[..cut]);
                h.update(&input[cut..]);
                assert_eq!(h.finish(), want, "length {len} cut at {cut}");
            }
            // Byte-sized and odd-sized pieces.
            if len <= 4096 {
                for piece in [1, 7, 64, 100] {
                    let mut h = Xxh3::new();
                    for c in input.chunks(piece) {
                        h.update(c);
                    }
                    assert_eq!(h.finish(), want, "length {len} in pieces of {piece}");
                }
            }
        }
    }

    #[test]
    fn the_kv_block_check_hashes_namespace_key_and_payload_in_pieces() {
        let data = pattern(3_052_800);
        let (ns, key, payload) = (&data[..16], &data[16..52], &data[52..]);
        let mut h = Xxh3::new();
        h.update(ns);
        h.update(key);
        h.update(payload);
        assert_eq!(h.finish(), xxh3_64(&data));
    }
}

#[cfg(test)]
mod throughput {
    use super::*;

    /// Run with `cargo test --release -p nvstor-core xxh3::throughput -- --ignored --nocapture`.
    #[test]
    #[ignore = "a measurement, not a check"]
    fn hashes_a_glm_block_per_core() {
        let block: Vec<u8> = (0..3_052_800usize).map(|i| (i * 131) as u8).collect();
        let start = std::time::Instant::now();
        let mut x = 0u64;
        for _ in 0..200 {
            x = x.wrapping_add(std::hint::black_box(xxh3_64(std::hint::black_box(&block))));
        }
        let secs = start.elapsed().as_secs_f64();
        println!(
            "xxh3_64: {:.2} GiB/s on one core ({x:x})",
            200.0 * block.len() as f64 / secs / (1u64 << 30) as f64
        );
    }
}
