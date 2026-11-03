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

//! AWS Signature Version 4 authentication.
//!
//! Supports:
//! - SigV4 Authorization header parsing and verification
//! - Anonymous mode (when no credentials are configured)
//! - Axum middleware layer

use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use axum::extract::Request;
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use hmac::{Hmac, Mac};
use parking_lot::RwLock;
use sha2::{Digest, Sha256};
use tracing::{debug, warn};

type HmacSha256 = Hmac<Sha256>;
const MAX_SIGV4_TIME_SKEW_SECS: u64 = 15 * 60;

// ---------------------------------------------------------------------------
// Credential store
// ---------------------------------------------------------------------------

/// A single access key / secret key pair.
#[derive(Debug, Clone)]
pub struct Credential {
    pub access_key: String,
    pub secret_key: String,
}

/// Thread-safe store of access credentials.
#[derive(Debug, Clone)]
pub struct CredentialStore {
    /// Map from access_key_id to Credential.
    credentials: Arc<RwLock<HashMap<String, Credential>>>,
    /// If true, all requests are allowed without auth.
    anonymous: Arc<AtomicBool>,
}

impl CredentialStore {
    /// Create a new credential store in anonymous mode (no auth required).
    pub fn anonymous() -> Self {
        Self {
            credentials: Arc::new(RwLock::new(HashMap::new())),
            anonymous: Arc::new(AtomicBool::new(true)),
        }
    }

    /// Create a new credential store with a single credential.
    pub fn with_credential(access_key: &str, secret_key: &str) -> Self {
        let mut map = HashMap::new();
        map.insert(
            access_key.to_string(),
            Credential {
                access_key: access_key.to_string(),
                secret_key: secret_key.to_string(),
            },
        );
        Self {
            credentials: Arc::new(RwLock::new(map)),
            anonymous: Arc::new(AtomicBool::new(false)),
        }
    }

    /// Load credentials from an IAM store (persisted in RocksDB).
    /// This ensures credentials survive server restart.
    pub fn from_iam_store(iam: &nvstor_iam::store::IamStore) -> Self {
        let mut map = HashMap::new();
        if let Ok(users) = iam.list_users() {
            for user in users {
                map.insert(
                    user.access_key.clone(),
                    Credential {
                        access_key: user.access_key,
                        secret_key: user.secret_key,
                    },
                );
            }
        }
        let anonymous = map.is_empty();
        Self {
            credentials: Arc::new(RwLock::new(map)),
            anonymous: Arc::new(AtomicBool::new(anonymous)), // If no users configured, allow anonymous
        }
    }

    /// Add a credential.
    pub fn add(&self, access_key: &str, secret_key: &str) {
        self.credentials.write().insert(
            access_key.to_string(),
            Credential {
                access_key: access_key.to_string(),
                secret_key: secret_key.to_string(),
            },
        );
        self.anonymous.store(false, Ordering::Relaxed);
    }

    /// Look up a credential by access key.
    pub fn get(&self, access_key: &str) -> Option<Credential> {
        self.credentials.read().get(access_key).cloned()
    }

    /// Whether anonymous access is allowed.
    pub fn is_anonymous(&self) -> bool {
        self.anonymous.load(Ordering::Relaxed)
    }
}

// ---------------------------------------------------------------------------
// SigV4 parsing
// ---------------------------------------------------------------------------

/// Parsed components of an AWS SigV4 Authorization header.
#[derive(Debug)]
pub struct SigV4Auth {
    pub access_key: String,
    pub date: String,
    pub region: String,
    pub service: String,
    pub signed_headers: Vec<String>,
    pub signature: String,
}

/// Parse the Authorization header value.
///
/// Format: `AWS4-HMAC-SHA256 Credential=AKID/20240101/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abcdef...`
pub fn parse_auth_header(header: &str) -> Option<SigV4Auth> {
    let rest = header.strip_prefix("AWS4-HMAC-SHA256 ")?;

    let mut credential = None;
    let mut signed_headers = None;
    let mut signature = None;

    for part in rest.split(", ") {
        if let Some(val) = part.strip_prefix("Credential=") {
            credential = Some(val);
        } else if let Some(val) = part.strip_prefix("SignedHeaders=") {
            signed_headers = Some(val);
        } else if let Some(val) = part.strip_prefix("Signature=") {
            signature = Some(val);
        }
    }

    let cred = credential?;
    let sig = signature?;
    let sh = signed_headers?;

    // Parse credential: AKID/date/region/service/aws4_request
    let cred_parts: Vec<&str> = cred.splitn(5, '/').collect();
    if cred_parts.len() != 5 {
        return None;
    }

    Some(SigV4Auth {
        access_key: cred_parts[0].to_string(),
        date: cred_parts[1].to_string(),
        region: cred_parts[2].to_string(),
        service: cred_parts[3].to_string(),
        signed_headers: sh.split(';').map(String::from).collect(),
        signature: sig.to_string(),
    })
}

// ---------------------------------------------------------------------------
// Signing
// ---------------------------------------------------------------------------

/// Compute the HMAC-SHA256 signing key.
pub fn signing_key(secret_key: &str, date: &str, region: &str, service: &str) -> Vec<u8> {
    let k_date = hmac_sha256(format!("AWS4{secret_key}").as_bytes(), date.as_bytes());
    let k_region = hmac_sha256(&k_date, region.as_bytes());
    let k_service = hmac_sha256(&k_region, service.as_bytes());
    hmac_sha256(&k_service, b"aws4_request")
}

/// Canonicalize already URI-encoded query pairs, retaining duplicate keys.
/// A bare S3 subresource such as `?uploads` signs as `uploads=`.
fn canonical_query(query: &str) -> String {
    if query.is_empty() {
        return String::new();
    }
    let mut pairs: Vec<_> = query
        .split('&')
        .map(|part| part.split_once('=').unwrap_or((part, "")))
        .collect();
    pairs.sort_unstable();
    pairs
        .into_iter()
        .map(|(key, value)| format!("{key}={value}"))
        .collect::<Vec<_>>()
        .join("&")
}

/// Build the canonical request string.
pub fn canonical_request(
    method: &str,
    uri: &str,
    query_string: &str,
    headers: &[(String, String)],
    signed_headers: &[String],
    payload_hash: &str,
) -> String {
    let canonical_headers: String = signed_headers
        .iter()
        .map(|h| {
            let val = headers
                .iter()
                .find(|(k, _)| k.to_lowercase() == *h)
                .map(|(_, v)| v.trim())
                .unwrap_or("");
            format!("{h}:{val}\n")
        })
        .collect();

    let signed = signed_headers.join(";");
    let query_string = canonical_query(query_string);

    format!("{method}\n{uri}\n{query_string}\n{canonical_headers}\n{signed}\n{payload_hash}")
}

/// Build the string to sign.
pub fn string_to_sign(
    datetime: &str,
    date: &str,
    region: &str,
    service: &str,
    canonical_request: &str,
) -> String {
    let cr_hash = hex::encode(Sha256::digest(canonical_request.as_bytes()));
    format!("AWS4-HMAC-SHA256\n{datetime}\n{date}/{region}/{service}/aws4_request\n{cr_hash}")
}

/// Compute the final signature.
pub fn compute_signature(signing_key: &[u8], string_to_sign: &str) -> String {
    hex::encode(hmac_sha256(signing_key, string_to_sign.as_bytes()))
}

/// Verify a SigV4 signature against a request.
pub fn verify_signature(
    auth: &SigV4Auth,
    secret_key: &str,
    method: &str,
    uri: &str,
    query_string: &str,
    headers: &[(String, String)],
    payload_hash: &str,
    datetime: &str,
) -> bool {
    let cr = canonical_request(
        method,
        uri,
        query_string,
        headers,
        &auth.signed_headers,
        payload_hash,
    );
    let sts = string_to_sign(datetime, &auth.date, &auth.region, &auth.service, &cr);
    let key = signing_key(secret_key, &auth.date, &auth.region, &auth.service);
    let expected = compute_signature(&key, &sts);
    expected == auth.signature
}

fn hmac_sha256(key: &[u8], data: &[u8]) -> Vec<u8> {
    let mut mac = HmacSha256::new_from_slice(key).expect("HMAC accepts any key size");
    mac.update(data);
    mac.finalize().into_bytes().to_vec()
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

/// Axum middleware that checks SigV4 authentication.
///
/// If the CredentialStore is in anonymous mode, all requests pass through.
pub async fn auth_middleware(
    axum::extract::Extension(creds): axum::extract::Extension<Arc<CredentialStore>>,
    request: Request,
    next: Next,
) -> Response {
    if creds.is_anonymous() {
        return next.run(request).await;
    }

    let method = request.method().to_string();
    let uri = request.uri().path().to_string();
    let query = request.uri().query().unwrap_or("").to_string();
    let query_params = parse_query_params(&query);

    let auth_header = request
        .headers()
        .get("authorization")
        .and_then(|v| v.to_str().ok());

    let access_key = if let Some(auth_header) = auth_header {
        let parsed = match parse_auth_header(auth_header) {
            Some(a) => a,
            None => {
                warn!("invalid Authorization header format");
                return (StatusCode::FORBIDDEN, "InvalidAuth").into_response();
            }
        };

        let cred = match creds.get(&parsed.access_key) {
            Some(c) => c,
            None => {
                warn!(access_key = %parsed.access_key, "unknown access key");
                return (StatusCode::FORBIDDEN, "InvalidAccessKeyId").into_response();
            }
        };

        let headers = request.headers();

        // Build headers list for canonical request.
        let header_pairs: Vec<(String, String)> = parsed
            .signed_headers
            .iter()
            .filter_map(|name| {
                headers
                    .get(name.as_str())
                    .and_then(|v| v.to_str().ok())
                    .map(|val| (name.clone(), val.trim().to_string()))
            })
            .collect();

        // Payload hash: use x-amz-content-sha256 if provided, else UNSIGNED-PAYLOAD.
        let payload_hash = headers
            .get("x-amz-content-sha256")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("UNSIGNED-PAYLOAD")
            .to_string();

        let amz_date = headers
            .get("x-amz-date")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        if !request_time_is_fresh(amz_date, MAX_SIGV4_TIME_SKEW_SECS) {
            warn!(access_key = %parsed.access_key, amz_date, "stale or invalid x-amz-date");
            return (StatusCode::FORBIDDEN, "RequestTimeTooSkewed").into_response();
        }

        let canon_req = canonical_request(
            &method,
            &uri,
            &query,
            &header_pairs,
            &parsed.signed_headers,
            &payload_hash,
        );
        let sts = string_to_sign(
            amz_date,
            &parsed.date,
            &parsed.region,
            &parsed.service,
            &canon_req,
        );

        let key = signing_key(
            &cred.secret_key,
            &parsed.date,
            &parsed.region,
            &parsed.service,
        );
        let expected_sig = compute_signature(&key, &sts);

        if expected_sig != parsed.signature {
            warn!(access_key = %parsed.access_key, "signature mismatch");
            return (StatusCode::FORBIDDEN, "SignatureDoesNotMatch").into_response();
        }
        debug!(access_key = %parsed.access_key, "signature verified");
        parsed.access_key
    } else if query_params.contains_key("X-Amz-Algorithm") {
        let access_key = match presigned_access_key(&query_params) {
            Some(access_key) => access_key,
            None => {
                warn!("invalid presigned credential");
                return (StatusCode::FORBIDDEN, "InvalidAccessKeyId").into_response();
            }
        };

        let cred = match creds.get(&access_key) {
            Some(c) => c,
            None => {
                warn!(access_key = %access_key, "unknown presigned access key");
                return (StatusCode::FORBIDDEN, "InvalidAccessKeyId").into_response();
            }
        };

        let host = request
            .headers()
            .get("host")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");

        if let Err(err) =
            crate::presign::verify_presigned(&method, &uri, &query_params, host, &cred.secret_key)
        {
            warn!(access_key = %access_key, error = %err, "invalid presigned request");
            return (StatusCode::FORBIDDEN, "SignatureDoesNotMatch").into_response();
        }

        debug!(access_key = %access_key, "presigned request verified");
        access_key
    } else {
        warn!("missing Authorization header");
        return (StatusCode::FORBIDDEN, "AccessDenied").into_response();
    };

    // IAM policy authorization (if IAM store is configured).
    if let Some(iam_store) = request
        .extensions()
        .get::<Arc<nvstor_iam::store::IamStore>>()
    {
        if let Ok(Some(user)) = iam_store.get_user(&access_key) {
            if !user.enabled {
                warn!(access_key = %access_key, "user disabled");
                return (StatusCode::FORBIDDEN, "AccessDenied: user disabled").into_response();
            }

            // Resolve effective policies.
            let policy_docs = iam_store.resolve_policies(&user);

            if !policy_docs.is_empty() {
                // Determine S3 action and resource from request.
                let path = request.uri().path();
                let parts: Vec<&str> = path.trim_start_matches('/').splitn(2, '/').collect();
                let is_bucket_op = parts.len() <= 1 || parts[1].is_empty();
                let s3_action = nvstor_iam::policy::s3_action(&method, is_bucket_op);
                let s3_resource = if parts.is_empty() || parts[0].is_empty() {
                    "*".to_string()
                } else if is_bucket_op {
                    nvstor_iam::policy::s3_resource_arn(parts[0], None)
                } else {
                    nvstor_iam::policy::s3_resource_arn(parts[0], Some(parts[1]))
                };

                let access_request = nvstor_iam::types::AccessRequest {
                    action: s3_action.to_string(),
                    resource: s3_resource,
                };
                let decision = nvstor_iam::policy::evaluate(&policy_docs, &access_request);

                match decision {
                    nvstor_iam::types::AccessDecision::Deny => {
                        warn!(
                            access_key = %access_key,
                            action = %access_request.action,
                            "IAM policy denied"
                        );
                        return (StatusCode::FORBIDDEN, "AccessDenied").into_response();
                    }
                    nvstor_iam::types::AccessDecision::DefaultDeny => {
                        warn!(
                            access_key = %access_key,
                            action = %access_request.action,
                            "IAM no matching policy"
                        );
                        return (StatusCode::FORBIDDEN, "AccessDenied").into_response();
                    }
                    nvstor_iam::types::AccessDecision::Allow => {
                        debug!(access_key = %access_key, action = %s3_action, "IAM allowed");
                    }
                }
            }
            // If user has no policies, allow through (backward compatibility).
        }
        // If user not found in IAM, allow through (credential-only auth).
    }

    next.run(request).await
}

fn parse_query_params(query: &str) -> HashMap<String, String> {
    query
        .split('&')
        .filter(|pair| !pair.is_empty())
        .filter_map(|pair| {
            let mut parts = pair.splitn(2, '=');
            let key = parts.next()?;
            let value = parts.next().unwrap_or_default();
            Some((key.to_string(), aws_query_decode(value)))
        })
        .collect()
}

fn aws_query_decode(value: &str) -> String {
    value
        .replace("%2F", "/")
        .replace("%2B", "+")
        .replace("%3D", "=")
}

fn presigned_access_key(query_params: &HashMap<String, String>) -> Option<String> {
    query_params
        .get("X-Amz-Credential")
        .and_then(|credential| credential.split('/').next())
        .filter(|access_key| !access_key.is_empty())
        .map(str::to_string)
}

fn request_time_is_fresh(amz_date: &str, max_skew_secs: u64) -> bool {
    let request_time = match parse_amz_datetime(amz_date) {
        Some(ts) => ts,
        None => return false,
    };
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();

    if request_time > now {
        request_time - now <= max_skew_secs
    } else {
        now - request_time <= max_skew_secs
    }
}

fn parse_amz_datetime(s: &str) -> Option<u64> {
    if s.len() != 16 || !s.ends_with('Z') {
        return None;
    }
    let y: i64 = s[0..4].parse().ok()?;
    let m: u64 = s[4..6].parse().ok()?;
    let d: u64 = s[6..8].parse().ok()?;
    let h: u64 = s[9..11].parse().ok()?;
    let min: u64 = s[11..13].parse().ok()?;
    let sec: u64 = s[13..15].parse().ok()?;

    let days = date_to_days(y, m as i64, d as i64);
    Some(days as u64 * 86400 + h * 3600 + min * 60 + sec)
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

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn canonical_query_handles_s3_subresources_order_and_duplicates() {
        assert_eq!(canonical_query("uploads"), "uploads=");
        assert_eq!(
            canonical_query("uploadId=a%2Fb&partNumber=2"),
            "partNumber=2&uploadId=a%2Fb"
        );
        assert_eq!(canonical_query("z=last&a=2&a=1&acl"), "a=1&a=2&acl=&z=last");
    }

    #[test]
    fn test_parse_auth_header() {
        let header = "AWS4-HMAC-SHA256 Credential=AKID/20240101/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=abcdef1234567890";
        let auth = parse_auth_header(header).unwrap();
        assert_eq!(auth.access_key, "AKID");
        assert_eq!(auth.date, "20240101");
        assert_eq!(auth.region, "us-east-1");
        assert_eq!(auth.service, "s3");
        assert_eq!(auth.signed_headers, vec!["host", "x-amz-date"]);
        assert_eq!(auth.signature, "abcdef1234567890");
    }

    #[test]
    fn test_parse_auth_header_invalid() {
        assert!(parse_auth_header("Basic abc").is_none());
        assert!(parse_auth_header("").is_none());
    }

    #[test]
    fn test_signing_key_deterministic() {
        let k1 = signing_key("secret", "20240101", "us-east-1", "s3");
        let k2 = signing_key("secret", "20240101", "us-east-1", "s3");
        assert_eq!(k1, k2);
        assert_eq!(k1.len(), 32);
    }

    #[test]
    fn test_signing_key_different_inputs() {
        let k1 = signing_key("secret1", "20240101", "us-east-1", "s3");
        let k2 = signing_key("secret2", "20240101", "us-east-1", "s3");
        assert_ne!(k1, k2);
    }

    #[test]
    fn test_canonical_request() {
        let headers = vec![
            ("host".to_string(), "localhost:9000".to_string()),
            ("x-amz-date".to_string(), "20240101T000000Z".to_string()),
        ];
        let cr = canonical_request(
            "GET",
            "/bucket/key",
            "",
            &headers,
            &["host".to_string(), "x-amz-date".to_string()],
            "UNSIGNED-PAYLOAD",
        );
        assert!(cr.starts_with("GET\n"));
        assert!(cr.contains("host:localhost:9000\n"));
    }

    #[test]
    fn test_compute_and_verify_signature() {
        let secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY";
        let date = "20240101";
        let region = "us-east-1";
        let service = "s3";
        let datetime = "20240101T000000Z";

        let headers = vec![
            ("host".to_string(), "localhost:9000".to_string()),
            ("x-amz-date".to_string(), datetime.to_string()),
        ];
        let signed_headers = vec!["host".to_string(), "x-amz-date".to_string()];

        let cr = canonical_request(
            "GET",
            "/",
            "",
            &headers,
            &signed_headers,
            "UNSIGNED-PAYLOAD",
        );
        let sts = string_to_sign(datetime, date, region, service, &cr);
        let key = signing_key(secret, date, region, service);
        let sig = compute_signature(&key, &sts);

        let auth = SigV4Auth {
            access_key: "AKID".to_string(),
            date: date.to_string(),
            region: region.to_string(),
            service: service.to_string(),
            signed_headers,
            signature: sig,
        };

        assert!(verify_signature(
            &auth,
            secret,
            "GET",
            "/",
            "",
            &headers,
            "UNSIGNED-PAYLOAD",
            datetime
        ));
    }

    #[test]
    fn test_credential_store_anonymous() {
        let store = CredentialStore::anonymous();
        assert!(store.is_anonymous());
        assert!(store.get("any").is_none());
    }

    #[test]
    fn test_credential_store_with_credential() {
        let store = CredentialStore::with_credential("AKID", "secret");
        assert!(!store.is_anonymous());
        let cred = store.get("AKID").unwrap();
        assert_eq!(cred.secret_key, "secret");
        assert!(store.get("other").is_none());
    }

    #[test]
    fn test_credential_store_add() {
        let store = CredentialStore::anonymous();
        store.add("key1", "secret1");
        assert!(!store.is_anonymous());
        assert!(store.get("key1").is_some());
    }

    #[test]
    fn test_parse_query_params_decodes_credential_scope() {
        let params = parse_query_params(
            "X-Amz-Credential=AKID%2F20240101%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20240101T000000Z",
        );
        assert_eq!(
            params.get("X-Amz-Credential").map(String::as_str),
            Some("AKID/20240101/us-east-1/s3/aws4_request")
        );
    }

    #[test]
    fn test_request_time_is_fresh_rejects_invalid_datetime() {
        assert!(!request_time_is_fresh(
            "not-a-date",
            MAX_SIGV4_TIME_SKEW_SECS
        ));
    }

    #[test]
    fn test_presigned_access_key_extracts_key_id() {
        let mut params = HashMap::new();
        params.insert(
            "X-Amz-Credential".to_string(),
            "AKID/20240101/us-east-1/s3/aws4_request".to_string(),
        );
        assert_eq!(presigned_access_key(&params).as_deref(), Some("AKID"));
    }
}
