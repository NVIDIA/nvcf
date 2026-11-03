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

//! Decode AWS streaming upload framing before object storage and verify checksums.

use axum::body::Bytes;
use base64::Engine;
use http::HeaderMap;
use sha2::{Digest, Sha256};

#[derive(Debug, PartialEq)]
pub enum UploadError {
    InvalidRequest,
    BadDigest,
    Unsupported,
}

fn checksum(name: &str, data: &[u8]) -> Result<Vec<u8>, UploadError> {
    Ok(match name {
        "x-amz-checksum-crc32" => crc32fast::hash(data).to_be_bytes().to_vec(),
        "x-amz-checksum-crc32c" => nvstor_core::checksum::crc32c(data).to_be_bytes().to_vec(),
        "x-amz-checksum-crc64nvme" => {
            let mut digest = crc64fast_nvme::Digest::new();
            digest.write(data);
            digest.sum64().to_be_bytes().to_vec()
        }
        "x-amz-checksum-sha256" => Sha256::digest(data).to_vec(),
        _ => return Err(UploadError::Unsupported),
    })
}

fn verify(name: &str, encoded: &str, data: &[u8]) -> Result<(), UploadError> {
    let supplied = base64::engine::general_purpose::STANDARD
        .decode(encoded.trim())
        .map_err(|_| UploadError::BadDigest)?;
    if checksum(name, data)? != supplied {
        return Err(UploadError::BadDigest);
    }
    Ok(())
}

fn line<'a>(input: &mut &'a [u8]) -> Result<&'a [u8], UploadError> {
    let end = input
        .windows(2)
        .position(|pair| pair == b"\r\n")
        .filter(|end| *end <= 1024)
        .ok_or(UploadError::InvalidRequest)?;
    let result = &input[..end];
    *input = &input[end + 2..];
    Ok(result)
}

pub fn decode(headers: &HeaderMap, body: Bytes) -> Result<Bytes, UploadError> {
    let encoding = headers
        .get("content-encoding")
        .and_then(|value| value.to_str().ok())
        .unwrap_or("");
    let chunked = encoding
        .split(',')
        .any(|part| part.trim().eq_ignore_ascii_case("aws-chunked"));
    let payload_mode = headers
        .get("x-amz-content-sha256")
        .and_then(|value| value.to_str().ok())
        .unwrap_or("");
    if payload_mode.starts_with("STREAMING-") && !chunked {
        return Err(UploadError::InvalidRequest);
    }
    let body = if chunked {
        // Signed chunk streams require per-chunk signature verification, which is
        // deliberately not claimed by this decoder.
        if payload_mode != "STREAMING-UNSIGNED-PAYLOAD-TRAILER" {
            return Err(UploadError::Unsupported);
        }
        let expected: usize = headers
            .get("x-amz-decoded-content-length")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.parse().ok())
            .ok_or(UploadError::InvalidRequest)?;
        let trailer_name = headers
            .get("x-amz-trailer")
            .and_then(|value| value.to_str().ok())
            .ok_or(UploadError::InvalidRequest)?
            .trim()
            .to_ascii_lowercase();
        // Validate the algorithm before decoding, including an empty upload.
        checksum(&trailer_name, &[])?;
        let mut input = body.as_ref();
        let mut decoded = Vec::with_capacity(expected.min(body.len()));
        loop {
            let size =
                std::str::from_utf8(line(&mut input)?).map_err(|_| UploadError::InvalidRequest)?;
            if size.is_empty() || !size.bytes().all(|byte| byte.is_ascii_hexdigit()) {
                return Err(UploadError::InvalidRequest);
            }
            let size = usize::from_str_radix(size, 16).map_err(|_| UploadError::InvalidRequest)?;
            if size == 0 {
                break;
            }
            if size > input.len() || size > expected.saturating_sub(decoded.len()) {
                return Err(UploadError::InvalidRequest);
            }
            decoded.extend_from_slice(&input[..size]);
            input = &input[size..];
            if !input.starts_with(b"\r\n") {
                return Err(UploadError::InvalidRequest);
            }
            input = &input[2..];
        }
        if decoded.len() != expected {
            return Err(UploadError::InvalidRequest);
        }
        let trailer =
            std::str::from_utf8(line(&mut input)?).map_err(|_| UploadError::InvalidRequest)?;
        let (name, value) = trailer.split_once(':').ok_or(UploadError::InvalidRequest)?;
        if !name.eq_ignore_ascii_case(&trailer_name)
            || !line(&mut input)?.is_empty()
            || !input.is_empty()
        {
            return Err(UploadError::InvalidRequest);
        }
        verify(&trailer_name, value, &decoded)?;
        Bytes::from(decoded)
    } else {
        body
    };
    for (name, value) in headers {
        if name.as_str().starts_with("x-amz-checksum-") && name.as_str() != "x-amz-checksum-type" {
            verify(
                name.as_str(),
                value.to_str().map_err(|_| UploadError::InvalidRequest)?,
                &body,
            )?;
        }
    }
    Ok(body)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn crc_checksums_match_aws_reference_values() {
        // Values an AWS SDK would send for b"hello world". Computed
        // independently of this crate so a wrong polynomial can't pass.
        let b64 = |v: Vec<u8>| base64::engine::general_purpose::STANDARD.encode(v);
        assert_eq!(
            b64(checksum("x-amz-checksum-crc32", b"hello world").unwrap()),
            "DUoRhQ=="
        );
        assert_eq!(
            b64(checksum("x-amz-checksum-crc32c", b"hello world").unwrap()),
            "yZRlqg=="
        );
    }

    #[test]
    fn unsigned_stream_decodes_data_and_verifies_trailer() {
        for algorithm in ["crc32", "crc32c", "crc64nvme", "sha256"] {
            let name = format!("x-amz-checksum-{algorithm}");
            let sum = base64::engine::general_purpose::STANDARD
                .encode(checksum(&name, b"hello world").unwrap());
            let body = format!("5\r\nhello\r\n6\r\n world\r\n0\r\n{name}:{sum}\r\n\r\n");
            let mut headers = HeaderMap::new();
            headers.insert("content-encoding", "aws-chunked".parse().unwrap());
            headers.insert(
                "x-amz-content-sha256",
                "STREAMING-UNSIGNED-PAYLOAD-TRAILER".parse().unwrap(),
            );
            headers.insert("x-amz-decoded-content-length", "11".parse().unwrap());
            headers.insert("x-amz-trailer", name.parse().unwrap());
            assert_eq!(
                decode(&headers, Bytes::from(body.clone())).unwrap(),
                "hello world"
            );
            assert_eq!(
                decode(&headers, Bytes::from(body.replace("hello", "jello"))),
                Err(UploadError::BadDigest)
            );
            headers.insert("x-amz-decoded-content-length", "10".parse().unwrap());
            assert_eq!(
                decode(&headers, Bytes::from(body)),
                Err(UploadError::InvalidRequest)
            );
        }
    }
}
