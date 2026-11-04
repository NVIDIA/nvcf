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

//! Presigned URL generation and verification.
//!
//! Generates time-limited URLs that grant temporary access to objects
//! without requiring the caller to have credentials. Used for:
//! - Web UI downloads
//! - Sharing model weights
//! - Distributed training data access
//!
//! Compatible with AWS S3 presigned URL format (v4 query string auth).

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use hmac::{Hmac, Mac};
use sha2::Sha256;

type HmacSha256 = Hmac<Sha256>;
const MAX_PRESIGN_FUTURE_SKEW_SECS: u64 = 15 * 60;

/// Generate a presigned URL for GET (download).
///
/// Returns a URL string with authentication parameters embedded.
/// The URL is valid for `expires_in` duration from now.
pub fn presign_get(
    endpoint: &str,
    bucket: &str,
    key: &str,
    access_key: &str,
    secret_key: &str,
    region: &str,
    expires_in: Duration,
) -> String {
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    let date = format_date(now.as_secs());
    let datetime = format_datetime(now.as_secs());
    let expires = expires_in.as_secs();

    let credential_scope = format!("{date}/{region}/s3/aws4_request");
    let credential = format!("{access_key}/{credential_scope}");

    let path = format!("/{bucket}/{key}");

    // Query string parameters (sorted alphabetically).
    let query = format!(
        "X-Amz-Algorithm=AWS4-HMAC-SHA256\
         &X-Amz-Credential={}\
         &X-Amz-Date={}\
         &X-Amz-Expires={}\
         &X-Amz-SignedHeaders=host",
        urlencoded(&credential),
        datetime,
        expires,
    );

    // Canonical request.
    let host = endpoint
        .trim_start_matches("http://")
        .trim_start_matches("https://");
    let canonical_request = format!("GET\n{path}\n{query}\nhost:{host}\n\nhost\nUNSIGNED-PAYLOAD");

    // String to sign.
    let cr_hash = hex::encode(sha2::Sha256::digest(canonical_request.as_bytes()));
    let string_to_sign = format!("AWS4-HMAC-SHA256\n{datetime}\n{credential_scope}\n{cr_hash}");

    // Signing key.
    let signing_key = derive_signing_key(secret_key, &date, region, "s3");
    let signature = hex::encode(hmac_sha256(&signing_key, string_to_sign.as_bytes()));

    format!("{endpoint}{path}?{query}&X-Amz-Signature={signature}")
}

/// Generate a presigned URL for PUT (upload).
pub fn presign_put(
    endpoint: &str,
    bucket: &str,
    key: &str,
    access_key: &str,
    secret_key: &str,
    region: &str,
    expires_in: Duration,
) -> String {
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    let date = format_date(now.as_secs());
    let datetime = format_datetime(now.as_secs());
    let expires = expires_in.as_secs();

    let credential_scope = format!("{date}/{region}/s3/aws4_request");
    let credential = format!("{access_key}/{credential_scope}");
    let path = format!("/{bucket}/{key}");

    let query = format!(
        "X-Amz-Algorithm=AWS4-HMAC-SHA256\
         &X-Amz-Credential={}\
         &X-Amz-Date={}\
         &X-Amz-Expires={}\
         &X-Amz-SignedHeaders=host",
        urlencoded(&credential),
        datetime,
        expires,
    );

    let host = endpoint
        .trim_start_matches("http://")
        .trim_start_matches("https://");
    let canonical_request = format!("PUT\n{path}\n{query}\nhost:{host}\n\nhost\nUNSIGNED-PAYLOAD");

    let cr_hash = hex::encode(sha2::Sha256::digest(canonical_request.as_bytes()));
    let string_to_sign = format!("AWS4-HMAC-SHA256\n{datetime}\n{credential_scope}\n{cr_hash}");

    let signing_key = derive_signing_key(secret_key, &date, region, "s3");
    let signature = hex::encode(hmac_sha256(&signing_key, string_to_sign.as_bytes()));

    format!("{endpoint}{path}?{query}&X-Amz-Signature={signature}")
}

/// Verify a presigned URL's signature and expiration.
/// Returns Ok(()) if valid, Err with reason if invalid.
pub fn verify_presigned(
    method: &str,
    path: &str,
    query_params: &std::collections::HashMap<String, String>,
    host: &str,
    secret_key: &str,
) -> Result<(), String> {
    let algorithm = query_params
        .get("X-Amz-Algorithm")
        .ok_or("missing X-Amz-Algorithm")?;
    if algorithm != "AWS4-HMAC-SHA256" {
        return Err(format!("unsupported algorithm: {algorithm}"));
    }

    let credential = query_params
        .get("X-Amz-Credential")
        .ok_or("missing X-Amz-Credential")?;
    let datetime = query_params.get("X-Amz-Date").ok_or("missing X-Amz-Date")?;
    let expires_str = query_params
        .get("X-Amz-Expires")
        .ok_or("missing X-Amz-Expires")?;
    let provided_sig = query_params
        .get("X-Amz-Signature")
        .ok_or("missing X-Amz-Signature")?;

    // Check expiration.
    let expires: u64 = expires_str.parse().map_err(|_| "invalid X-Amz-Expires")?;
    let sign_time = parse_datetime(datetime).ok_or("invalid X-Amz-Date")?;
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();

    if now > sign_time + expires {
        return Err("presigned URL has expired".into());
    }
    if sign_time > now + MAX_PRESIGN_FUTURE_SKEW_SECS {
        return Err("presigned URL date is too far in the future".into());
    }

    // Parse credential scope.
    let parts: Vec<&str> = credential.splitn(2, '/').collect();
    if parts.len() != 2 {
        return Err("invalid credential format".into());
    }
    let scope_parts: Vec<&str> = parts[1].split('/').collect();
    if scope_parts.len() != 4 {
        return Err("invalid credential scope".into());
    }
    let date = scope_parts[0];
    let region = scope_parts[1];
    let service = scope_parts[2];

    // Reconstruct query string (without signature).
    let query = format!(
        "X-Amz-Algorithm=AWS4-HMAC-SHA256\
         &X-Amz-Credential={}\
         &X-Amz-Date={}\
         &X-Amz-Expires={}\
         &X-Amz-SignedHeaders=host",
        urlencoded(credential),
        datetime,
        expires_str,
    );

    let canonical_request =
        format!("{method}\n{path}\n{query}\nhost:{host}\n\nhost\nUNSIGNED-PAYLOAD");

    let cr_hash = hex::encode(sha2::Sha256::digest(canonical_request.as_bytes()));
    let credential_scope = format!("{date}/{region}/{service}/aws4_request");
    let string_to_sign = format!("AWS4-HMAC-SHA256\n{datetime}\n{credential_scope}\n{cr_hash}");

    let signing_key = derive_signing_key(secret_key, date, region, service);
    let expected_sig = hex::encode(hmac_sha256(&signing_key, string_to_sign.as_bytes()));

    if expected_sig != *provided_sig {
        return Err("signature mismatch".into());
    }

    Ok(())
}

// ============================================================================
// Helpers
// ============================================================================

fn derive_signing_key(secret_key: &str, date: &str, region: &str, service: &str) -> Vec<u8> {
    let k_date = hmac_sha256(format!("AWS4{secret_key}").as_bytes(), date.as_bytes());
    let k_region = hmac_sha256(&k_date, region.as_bytes());
    let k_service = hmac_sha256(&k_region, service.as_bytes());
    hmac_sha256(&k_service, b"aws4_request")
}

fn hmac_sha256(key: &[u8], data: &[u8]) -> Vec<u8> {
    let mut mac = HmacSha256::new_from_slice(key).expect("valid key size");
    mac.update(data);
    mac.finalize().into_bytes().to_vec()
}

fn format_date(epoch_secs: u64) -> String {
    // YYYYMMDD
    let days = epoch_secs / 86400;
    let (y, m, d) = days_to_date(days as i64);
    format!("{y:04}{m:02}{d:02}")
}

fn format_datetime(epoch_secs: u64) -> String {
    // YYYYMMDDTHHMMSSZ
    let days = epoch_secs / 86400;
    let time_of_day = epoch_secs % 86400;
    let (y, m, d) = days_to_date(days as i64);
    let h = time_of_day / 3600;
    let min = (time_of_day % 3600) / 60;
    let s = time_of_day % 60;
    format!("{y:04}{m:02}{d:02}T{h:02}{min:02}{s:02}Z")
}

fn parse_datetime(s: &str) -> Option<u64> {
    // Parse YYYYMMDDTHHMMSSZ
    if s.len() != 16 || !s.ends_with('Z') {
        return None;
    }
    let y: i64 = s[0..4].parse().ok()?;
    let m: u64 = s[4..6].parse().ok()?;
    let d: u64 = s[6..8].parse().ok()?;
    let h: u64 = s[9..11].parse().ok()?;
    let min: u64 = s[11..13].parse().ok()?;
    let sec: u64 = s[13..15].parse().ok()?;

    // Simplified: approximate days from epoch
    let days = date_to_days(y, m as i64, d as i64);
    Some(days as u64 * 86400 + h * 3600 + min * 60 + sec)
}

fn days_to_date(days: i64) -> (i64, i64, i64) {
    let z = days + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = (z - era * 146097) as u64;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    (y, m as i64, d as i64)
}

fn date_to_days(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = (y - era * 400) as u64;
    let m = if m > 2 { m - 3 } else { m + 9 } as u64;
    let doy = (153 * m + 2) / 5 + d as u64 - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146097 + doe as i64 - 719468
}

fn urlencoded(s: &str) -> String {
    s.replace('/', "%2F")
        .replace('+', "%2B")
        .replace('=', "%3D")
}

use sha2::Digest;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn presign_get_produces_valid_url() {
        let url = presign_get(
            "http://localhost:9000",
            "mybucket",
            "mykey",
            "AKID",
            "SECRET",
            "us-east-1",
            Duration::from_secs(3600),
        );

        assert!(url.starts_with("http://localhost:9000/mybucket/mykey?"));
        assert!(url.contains("X-Amz-Algorithm=AWS4-HMAC-SHA256"));
        assert!(url.contains("X-Amz-Credential="));
        assert!(url.contains("X-Amz-Expires=3600"));
        assert!(url.contains("X-Amz-Signature="));
    }

    #[test]
    fn presign_put_produces_valid_url() {
        let url = presign_put(
            "http://localhost:9000",
            "mybucket",
            "upload.bin",
            "AKID",
            "SECRET",
            "us-east-1",
            Duration::from_secs(600),
        );

        assert!(url.contains("/mybucket/upload.bin?"));
        assert!(url.contains("X-Amz-Expires=600"));
    }

    #[test]
    fn presign_roundtrip_verification() {
        let secret = "mysecret";
        let url = presign_get(
            "http://localhost:9000",
            "bucket",
            "key",
            "AKID",
            secret,
            "us-east-1",
            Duration::from_secs(3600),
        );

        // Parse query params from URL.
        let query_str = url.split('?').nth(1).expect("has query");
        let mut params = std::collections::HashMap::new();
        for pair in query_str.split('&') {
            let mut parts = pair.splitn(2, '=');
            let k = parts.next().expect("key");
            let v = parts.next().expect("value");
            params.insert(k.to_string(), v.replace("%2F", "/").replace("%3D", "="));
        }

        let result = verify_presigned("GET", "/bucket/key", &params, "localhost:9000", secret);

        assert!(result.is_ok(), "verification failed: {:?}", result);
    }

    #[test]
    fn expired_url_rejected() {
        // Create a URL with 0-second expiry (immediately expired).
        let url = presign_get(
            "http://localhost:9000",
            "bucket",
            "key",
            "AKID",
            "SECRET",
            "us-east-1",
            Duration::from_secs(1),
        );

        let query_str = url.split('?').nth(1).expect("has query");
        let mut params = std::collections::HashMap::new();
        for pair in query_str.split('&') {
            let mut parts = pair.splitn(2, '=');
            let k = parts.next().expect("key");
            let v = parts.next().expect("value");
            params.insert(k.to_string(), v.replace("%2F", "/").replace("%3D", "="));
        }

        // Wait a bit to ensure expiry.
        std::thread::sleep(Duration::from_secs(2));

        let result = verify_presigned("GET", "/bucket/key", &params, "localhost:9000", "SECRET");
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("expired"));
    }

    #[test]
    fn wrong_secret_rejected() {
        let url = presign_get(
            "http://localhost:9000",
            "bucket",
            "key",
            "AKID",
            "correct-secret",
            "us-east-1",
            Duration::from_secs(3600),
        );

        let query_str = url.split('?').nth(1).expect("has query");
        let mut params = std::collections::HashMap::new();
        for pair in query_str.split('&') {
            let mut parts = pair.splitn(2, '=');
            let k = parts.next().expect("key");
            let v = parts.next().expect("value");
            params.insert(k.to_string(), v.replace("%2F", "/").replace("%3D", "="));
        }

        let result = verify_presigned(
            "GET",
            "/bucket/key",
            &params,
            "localhost:9000",
            "wrong-secret",
        );
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("signature"));
    }

    #[test]
    fn future_dated_url_rejected() {
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        let future_ts = now + MAX_PRESIGN_FUTURE_SKEW_SECS + 60;
        let date = format_date(future_ts);
        let datetime = format_datetime(future_ts);
        let credential_scope = format!("{date}/us-east-1/s3/aws4_request");
        let credential = format!("AKID/{credential_scope}");
        let query = format!(
            "X-Amz-Algorithm=AWS4-HMAC-SHA256\
             &X-Amz-Credential={}\
             &X-Amz-Date={}\
             &X-Amz-Expires=3600\
             &X-Amz-SignedHeaders=host",
            urlencoded(&credential),
            datetime,
        );
        let canonical_request =
            format!("GET\n/bucket/key\n{query}\nhost:localhost:9000\n\nhost\nUNSIGNED-PAYLOAD");
        let cr_hash = hex::encode(sha2::Sha256::digest(canonical_request.as_bytes()));
        let string_to_sign = format!("AWS4-HMAC-SHA256\n{datetime}\n{credential_scope}\n{cr_hash}");
        let signing_key = derive_signing_key("SECRET", &date, "us-east-1", "s3");
        let signature = hex::encode(hmac_sha256(&signing_key, string_to_sign.as_bytes()));

        let mut params = std::collections::HashMap::new();
        params.insert(
            "X-Amz-Algorithm".to_string(),
            "AWS4-HMAC-SHA256".to_string(),
        );
        params.insert("X-Amz-Credential".to_string(), credential);
        params.insert("X-Amz-Date".to_string(), datetime);
        params.insert("X-Amz-Expires".to_string(), "3600".to_string());
        params.insert("X-Amz-SignedHeaders".to_string(), "host".to_string());
        params.insert("X-Amz-Signature".to_string(), signature);

        let result = verify_presigned("GET", "/bucket/key", &params, "localhost:9000", "SECRET");
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("future"));
    }

    #[test]
    fn format_date_correct() {
        // 2024-01-15 00:00:00 UTC
        let ts = 1705276800;
        assert_eq!(format_date(ts), "20240115");
    }

    #[test]
    fn format_datetime_correct() {
        let ts = 1705276800;
        assert_eq!(format_datetime(ts), "20240115T000000Z");
    }
}
