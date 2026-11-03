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

//! S3 HTTP handlers — bucket CRUD, object CRUD, multipart, batch delete.
use crc32fast;

use axum::body::Body;
use axum::extract::{Path, Query, State};
use axum::http::{HeaderMap, StatusCode};
use axum::response::Response;

use serde::Deserialize;
use tracing::{debug, warn};

use super::xml;
use super::AppState;

// ---------------------------------------------------------------------------
// Query parameter structs
// ---------------------------------------------------------------------------

#[derive(Debug, Deserialize, Default)]
pub struct ListObjectsParams {
    pub prefix: Option<String>,
    #[serde(rename = "max-keys")]
    pub max_keys: Option<u32>,
    #[serde(rename = "continuation-token")]
    pub continuation_token: Option<String>,
    #[serde(rename = "start-after")]
    pub start_after: Option<String>,
    #[serde(rename = "list-type")]
    pub list_type: Option<String>,
    /// If present, this is a ListObjectVersions request.
    pub versions: Option<String>,
    /// If present, this is a GetBucketVersioning request.
    pub versioning: Option<String>,
}

#[derive(Debug, Deserialize, Default)]
pub struct MultipartParams {
    pub uploads: Option<String>,
    #[serde(rename = "uploadId")]
    pub upload_id: Option<String>,
    #[serde(rename = "partNumber")]
    pub part_number: Option<u32>,
    #[serde(rename = "versionId")]
    pub version_id: Option<String>,
}

#[derive(Debug, Deserialize, Default)]
pub struct DeleteParams {
    pub delete: Option<String>,
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const XML_CONTENT_TYPE: &str = "application/xml";
const NULL_VERSION_ID: &str = "null";

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum BucketVersioningState {
    Disabled,
    Enabled,
    Suspended,
}

fn s3_error(status: StatusCode, code: &str, message: &str, resource: &str) -> Response {
    let body = xml::error_xml(code, message, resource, "nvstor");
    Response::builder()
        .status(status)
        .header("Content-Type", XML_CONTENT_TYPE)
        .body(Body::from(body))
        .expect("response build")
}

/// Format a Unix timestamp (seconds, UTC) as an RFC 7231 HTTP date
/// (IMF-fixdate), e.g. "Fri, 13 Mar 2026 00:00:00 GMT", by converting the
/// day count to a proleptic Gregorian date.
fn format_http_date(timestamp: i64) -> String {
    let days = timestamp.div_euclid(86_400);
    let secs_of_day = timestamp.rem_euclid(86_400);
    // 1970-01-01 was a Thursday.
    let weekday = ["Thu", "Fri", "Sat", "Sun", "Mon", "Tue", "Wed"][days.rem_euclid(7) as usize];
    // Days since 1970-01-01 to a proleptic Gregorian date (H. Hinnant's
    // civil_from_days); exact for every i64 day count in range.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1_460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    let months = [
        "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
    ];
    format!(
        "{}, {:02} {} {:04} {:02}:{:02}:{:02} GMT",
        weekday,
        day,
        months[(month - 1) as usize],
        year,
        secs_of_day / 3600,
        (secs_of_day % 3600) / 60,
        secs_of_day % 60
    )
}

fn object_id(bucket: &str, key: &str) -> String {
    format!("{bucket}/{key}")
}

fn version_object_id(bucket: &str, key: &str, version_id: &str) -> String {
    format!("__versions__/{bucket}/{version_id}/{key}")
}

/// Key for storing encryption metadata alongside an object.
fn encryption_meta_key(object_id: &str) -> String {
    format!("enc/{object_id}")
}

/// Store encryption metadata for an object.
fn store_encryption_metadata(
    state: &AppState,
    object_id: &str,
    meta: &nvstor_kms::types::EncryptionMetadata,
) -> Result<(), String> {
    let key = encryption_meta_key(object_id);
    let bytes = serde_json::to_vec(meta).map_err(|e| format!("serialize enc meta: {e}"))?;
    state
        .metadata
        .put_inline(&key, &bytes)
        .map_err(|e| format!("put enc meta: {e}"))
}

/// Load encryption metadata for an object (if encrypted).
fn load_encryption_metadata(
    state: &AppState,
    object_id: &str,
) -> Option<nvstor_kms::types::EncryptionMetadata> {
    let key = encryption_meta_key(object_id);
    state
        .metadata
        .get_inline(&key)
        .ok()
        .flatten()
        .and_then(|bytes| serde_json::from_slice(&bytes).ok())
}

/// Delete encryption metadata for an object.
fn delete_encryption_metadata(state: &AppState, object_id: &str) {
    let key = encryption_meta_key(object_id);
    let _ = state.metadata.delete_inline(&key);
}

/// Parse SSE mode from request headers.
fn parse_sse_from_headers(headers: &HeaderMap) -> nvstor_kms::types::SseMode {
    let encryption = headers
        .get("x-amz-server-side-encryption")
        .and_then(|v| v.to_str().ok());
    let kms_key_id = headers
        .get("x-amz-server-side-encryption-aws-kms-key-id")
        .and_then(|v| v.to_str().ok());
    let customer_algo = headers
        .get("x-amz-server-side-encryption-customer-algorithm")
        .and_then(|v| v.to_str().ok());
    nvstor_kms::manager::KmsManager::parse_sse_headers(encryption, kms_key_id, customer_algo)
}

/// Parse a Range header like "bytes=0-99" into (start, end_exclusive).
fn parse_range(header: &str, total_size: u64) -> Option<(u64, u64)> {
    let range = header.strip_prefix("bytes=")?;
    let parts: Vec<&str> = range.splitn(2, '-').collect();
    if parts.len() != 2 {
        return None;
    }

    match (parts[0].is_empty(), parts[1].is_empty()) {
        // "bytes=-N" => last N bytes
        (true, false) => {
            let suffix: u64 = parts[1].parse().ok()?;
            let start = total_size.saturating_sub(suffix);
            Some((start, total_size))
        }
        // "bytes=N-" => from N to end
        (false, true) => {
            let start: u64 = parts[0].parse().ok()?;
            Some((start, total_size))
        }
        // "bytes=N-M"
        (false, false) => {
            let start: u64 = parts[0].parse().ok()?;
            let end: u64 = parts[1].parse().ok()?;
            Some((start, end + 1)) // HTTP ranges are inclusive, we need exclusive
        }
        _ => None,
    }
}

fn decode_percent_encoded(input: &str) -> Result<String, String> {
    let bytes = input.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'%' if i + 2 < bytes.len() => {
                let hi = (bytes[i + 1] as char)
                    .to_digit(16)
                    .ok_or_else(|| format!("invalid percent-encoding in '{input}'"))?;
                let lo = (bytes[i + 2] as char)
                    .to_digit(16)
                    .ok_or_else(|| format!("invalid percent-encoding in '{input}'"))?;
                out.push(((hi << 4) | lo) as u8);
                i += 3;
            }
            b'%' => return Err(format!("truncated percent-encoding in '{input}'")),
            byte => {
                out.push(byte);
                i += 1;
            }
        }
    }

    String::from_utf8(out).map_err(|_| format!("invalid utf-8 in percent-encoded value '{input}'"))
}

/// Propose a metadata mutation through Raft (if enabled) or apply directly.
/// Returns Ok(()) on success, Err with error message on failure.
async fn propose_or_apply(
    state: &AppState,
    entry: nvstor_raft::types::LogEntryData,
) -> Result<(), String> {
    if let Some(ref raft) = state.raft {
        match raft.propose(entry).await {
            nvstor_raft::types::ProposeResult::Committed { .. } => Ok(()),
            nvstor_raft::types::ProposeResult::NotLeader { leader_id } => {
                Err(format!("not leader, leader is node {leader_id}"))
            }
            nvstor_raft::types::ProposeResult::Timeout => Err("raft propose timeout".into()),
        }
    } else {
        // Single-node mode: no Raft, write directly.
        // The state machine would apply the entry, but since we don't have Raft,
        // we skip it and let the handler write to storage directly.
        Ok(())
    }
}

/// Not-leader redirect. If this node is a Raft follower, mutating
/// S3 requests can't be committed here — Raft `propose` would return
/// NotLeader. Instead of failing, return an HTTP 307 Temporary Redirect to
/// the leader's S3 endpoint (the AWS-native way S3 routes a request to the
/// right host; SDKs follow it). `Location` preserves the original path +
/// query so the re-sent request is identical.
///
/// Returns None — meaning "handle locally" — when we're the leader, Raft is
/// inactive (single-node), the leader is currently unknown (election in
/// flight; let `propose` surface a 503 so the client retries), or the
/// leader's S3 endpoint isn't in the cluster map.
fn redirect_to_leader(state: &AppState, uri: &axum::http::Uri) -> Option<Response> {
    let raft = state.raft.as_ref()?;
    if raft.is_leader() {
        return None;
    }
    let leader_id = raft.leader_id();
    if leader_id == 0 {
        return None;
    }
    let dm = state.distributed.as_ref()?;
    let cluster = dm.cluster_map();
    let node = cluster.nodes.iter().find(|n| n.id == leader_id)?;
    let path_and_query = uri
        .path_and_query()
        .map(|pq| pq.as_str())
        .unwrap_or(uri.path());
    let location = leader_redirect_location(&node.s3_address, path_and_query);
    Some(
        Response::builder()
            .status(StatusCode::TEMPORARY_REDIRECT)
            .header("Location", location)
            .header("x-amz-nvstor-leader", leader_id.to_string())
            .body(Body::empty())
            .expect("response build"),
    )
}

/// Build the `Location` for a not-leader redirect: the leader's S3 base
/// endpoint (scheme://host:port, trailing slash trimmed) concatenated with
/// the original request's path-and-query so the re-sent request is byte-identical.
fn leader_redirect_location(leader_s3_address: &str, path_and_query: &str) -> String {
    format!(
        "{}{}",
        leader_s3_address.trim_end_matches('/'),
        path_and_query
    )
}

/// Check if bucket exists in persistent metadata store.
fn bucket_exists(state: &AppState, bucket: &str) -> bool {
    state.metadata.get_bucket(bucket).ok().flatten().is_some()
}

fn bucket_versioning_state(state: &AppState, bucket: &str) -> BucketVersioningState {
    state
        .metadata
        .get_bucket(bucket)
        .ok()
        .flatten()
        .map(|b| match b.versioning {
            1 => BucketVersioningState::Enabled,
            2 => BucketVersioningState::Suspended,
            _ => BucketVersioningState::Disabled,
        })
        .unwrap_or(BucketVersioningState::Disabled)
}

fn uses_version_history(state: &AppState, bucket: &str) -> bool {
    bucket_versioning_state(state, bucket) != BucketVersioningState::Disabled
}

fn version_id_for_new_write(state: &AppState, bucket: &str) -> Option<String> {
    match bucket_versioning_state(state, bucket) {
        BucketVersioningState::Disabled => None,
        BucketVersioningState::Enabled => {
            Some(nvstor_metadata::MetadataStore::generate_version_id())
        }
        BucketVersioningState::Suspended => Some(NULL_VERSION_ID.to_string()),
    }
}

fn object_id_for_version(bucket: &str, key: &str, version_id: Option<&str>) -> String {
    match version_id {
        None | Some(NULL_VERSION_ID) => object_id(bucket, key),
        Some(version_id) => version_object_id(bucket, key, version_id),
    }
}

async fn create_delete_marker(state: &AppState, bucket: &str, key: &str) -> Result<String, String> {
    let version_id = match bucket_versioning_state(state, bucket) {
        BucketVersioningState::Disabled => {
            return Err("delete marker requested for unversioned bucket".to_string());
        }
        BucketVersioningState::Enabled => nvstor_metadata::MetadataStore::generate_version_id(),
        BucketVersioningState::Suspended => NULL_VERSION_ID.to_string(),
    };
    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64;
    let marker = nvstor_metadata::ObjectVersion {
        version_id: version_id.clone(),
        bucket: bucket.to_string(),
        key: key.to_string(),
        is_delete_marker: true,
        is_latest: true,
        size: 0,
        etag: String::new(),
        content_type: String::new(),
        last_modified: now,
        owner: "default".to_string(),
        manifest_object_id: String::new(),
    };
    state
        .metadata
        .put_object_version(&marker)
        .map_err(|e| e.to_string())?;
    // Strong consistency: replicate the delete marker to every node.
    replicate_object_version(state, &marker).await?;
    sync_current_object_alias(state, bucket, key)?;
    Ok(version_id)
}

fn load_requested_version(
    state: &AppState,
    bucket: &str,
    key: &str,
    version_id: Option<&str>,
) -> Result<Option<nvstor_metadata::ObjectVersion>, String> {
    if let Some(version_id) = version_id {
        return state
            .metadata
            .get_object_version(bucket, key, version_id)
            .map_err(|e| e.to_string());
    }

    if uses_version_history(state, bucket) {
        return state
            .metadata
            .get_latest_version(bucket, key)
            .map_err(|e| e.to_string());
    }

    Ok(None)
}

fn no_such_version_response(key: &str, version_id: &str) -> Response {
    let body = xml::error_xml(
        "NoSuchVersion",
        "The specified version does not exist.",
        key,
        "nvstor",
    );
    Response::builder()
        .status(StatusCode::NOT_FOUND)
        .header("Content-Type", XML_CONTENT_TYPE)
        .header("x-amz-version-id", version_id)
        .body(Body::from(body))
        .expect("response build")
}

fn version_not_found_response(key: &str, version: &nvstor_metadata::ObjectVersion) -> Response {
    let mut resp = Response::builder()
        .status(StatusCode::NOT_FOUND)
        .header("Content-Type", XML_CONTENT_TYPE)
        .header("x-amz-delete-marker", "true");
    if !version.version_id.is_empty() {
        resp = resp.header("x-amz-version-id", &version.version_id);
    }
    resp.body(Body::from(xml::error_xml(
        "NoSuchKey",
        "The specified key does not exist.",
        key,
        "nvstor",
    )))
    .expect("response build")
}

fn sync_current_object_alias(state: &AppState, bucket: &str, key: &str) -> Result<(), String> {
    let canonical_oid = object_id(bucket, key);
    let latest = state
        .metadata
        .get_latest_version(bucket, key)
        .map_err(|e| e.to_string())?;

    match latest {
        Some(version) if !version.is_delete_marker && !version.manifest_object_id.is_empty() => {
            if version.manifest_object_id != canonical_oid {
                state
                    .manager
                    .object_copy(&version.manifest_object_id, &canonical_oid)
                    .map_err(|e| e.to_string())?;
            }

            if let Some(enc_meta) = load_encryption_metadata(state, &version.manifest_object_id) {
                store_encryption_metadata(state, &canonical_oid, &enc_meta)?;
            } else {
                delete_encryption_metadata(state, &canonical_oid);
            }
        }
        _ => {
            let _ = state.manager.object_delete(&canonical_oid);
            delete_encryption_metadata(state, &canonical_oid);
        }
    }

    Ok(())
}

fn other_versions_reference_object_id(
    state: &AppState,
    bucket: &str,
    key: &str,
    object_id: &str,
    excluded_version_id: &str,
) -> Result<bool, String> {
    let versions = state
        .metadata
        .list_object_versions(bucket, key)
        .map_err(|e| e.to_string())?;
    Ok(versions.iter().any(|version| {
        version.version_id != excluded_version_id && version.manifest_object_id == object_id
    }))
}

fn copy_encryption_metadata_for_object(
    state: &AppState,
    src_object_id: &str,
    dst_object_id: &str,
) -> Result<(), String> {
    if let Some(enc_meta) = load_encryption_metadata(state, src_object_id) {
        store_encryption_metadata(state, dst_object_id, &enc_meta)?;
    } else {
        delete_encryption_metadata(state, dst_object_id);
    }
    Ok(())
}

fn resolve_copy_source(
    state: &AppState,
    copy_source: &str,
) -> Result<(String, Option<String>), Response> {
    let copy_source = copy_source.strip_prefix('/').unwrap_or(copy_source);
    let (path, query) = copy_source.split_once('?').unwrap_or((copy_source, ""));
    let decoded_path = match decode_percent_encoded(path) {
        Ok(path) => path,
        Err(e) => {
            return Err(s3_error(
                StatusCode::BAD_REQUEST,
                "InvalidRequest",
                &e,
                copy_source,
            ));
        }
    };
    let Some((bucket, key)) = decoded_path.split_once('/') else {
        return Err(s3_error(
            StatusCode::BAD_REQUEST,
            "InvalidRequest",
            "Invalid x-amz-copy-source header",
            copy_source,
        ));
    };

    let mut version_id = None;
    for pair in query.split('&') {
        if let Some(value) = pair.strip_prefix("versionId=") {
            if !value.is_empty() {
                version_id = Some(match decode_percent_encoded(value) {
                    Ok(value) => value,
                    Err(e) => {
                        return Err(s3_error(
                            StatusCode::BAD_REQUEST,
                            "InvalidRequest",
                            &e,
                            copy_source,
                        ));
                    }
                });
            }
        }
    }

    if let Some(version_id) = version_id {
        let version = match load_requested_version(state, bucket, key, Some(&version_id)) {
            Ok(Some(version)) => version,
            Ok(None) => return Err(no_such_version_response(key, &version_id)),
            Err(e) => {
                return Err(s3_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "InternalError",
                    &e,
                    key,
                ));
            }
        };

        if version.is_delete_marker {
            return Err(version_not_found_response(key, &version));
        }

        return Ok((version.manifest_object_id, Some(version_id)));
    }

    Ok((decoded_path, None))
}

/// Strong consistency: replicate an object's manifest metadata to every
/// node through Raft. Re-reads the just-written manifest from the local
/// MetadataStore (giving the metadata-layer ChunkRef list) and proposes a
/// `ManifestPut`. The state machine applies it on every node so a follower
/// that never received the chunk DATA still has a complete, readable
/// manifest and can fetch chunk bytes from a replica on read.
///
/// No-op when Raft is inactive (single-node) — the direct `object_write`
/// is the only path there. Returns Err on NotLeader / commit timeout so
/// the caller can roll the write back rather than diverge.
async fn propose_manifest_put(state: &AppState, object_id: &str) -> Result<(), String> {
    if state.raft.is_none() {
        return Ok(());
    }
    let mm = state
        .metadata
        .get_manifest(object_id)
        .map_err(|e| e.to_string())?
        .ok_or_else(|| "manifest missing after write".to_string())?;
    let entry = nvstor_raft::types::LogEntryData::ManifestPutV2 { manifest: mm };
    propose_or_apply(state, entry).await
}

/// Strong consistency: replicate an object-version record to every node
/// through Raft so versioned GET-by-version-id and ListObjectVersions
/// return the same answer regardless of which node serves the read.
/// No-op single-node. Err on NotLeader / commit failure.
async fn replicate_object_version(
    state: &AppState,
    version: &nvstor_metadata::ObjectVersion,
) -> Result<(), String> {
    if state.raft.is_none() {
        return Ok(());
    }
    let entry = nvstor_raft::types::LogEntryData::ObjectVersionPut {
        version: version.clone(),
    };
    propose_or_apply(state, entry).await
}

/// Strong consistency: replicate an object-version deletion to every node.
async fn replicate_object_version_delete(
    state: &AppState,
    bucket: &str,
    key: &str,
    version_id: &str,
) -> Result<(), String> {
    if state.raft.is_none() {
        return Ok(());
    }
    let entry = nvstor_raft::types::LogEntryData::ObjectVersionDelete {
        bucket: bucket.to_string(),
        key: key.to_string(),
        version_id: version_id.to_string(),
    };
    propose_or_apply(state, entry).await
}

/// Strong consistency: replicate an object's SSE encryption metadata to
/// every node through Raft so a follower serving the read can decrypt.
/// The `object_id` here is whatever oid the GET path loads enc metadata
/// for (version-suffixed for versioned writes). No-op single-node.
async fn replicate_encryption_meta_put(
    state: &AppState,
    object_id: &str,
    meta: &nvstor_kms::types::EncryptionMetadata,
) -> Result<(), String> {
    if state.raft.is_none() {
        return Ok(());
    }
    let bytes = serde_json::to_vec(meta).map_err(|e| format!("serialize enc meta: {e}"))?;
    let entry = nvstor_raft::types::LogEntryData::InlinePut {
        key: encryption_meta_key(object_id),
        value: bytes,
    };
    propose_or_apply(state, entry).await
}

/// Strong consistency: replicate deletion of an object's SSE encryption
/// metadata to every node.
async fn replicate_encryption_meta_delete(state: &AppState, object_id: &str) -> Result<(), String> {
    if state.raft.is_none() {
        return Ok(());
    }
    let entry = nvstor_raft::types::LogEntryData::InlineDelete {
        key: encryption_meta_key(object_id),
    };
    propose_or_apply(state, entry).await
}

/// Read a full object, falling back to a cross-node fetch. On a node that
/// holds the manifest (replicated via Raft) but not the chunk DATA — a
/// non-placement-target follower — the local read misses; `distributed_read`
/// pulls the bytes from a replica that does hold them. This is what makes
/// "any node serves any object" true. No-op difference single-node:
/// the local read is the only path.
async fn fetch_full_object(state: &AppState, object_id: &str) -> Result<Vec<u8>, String> {
    if let Ok(data) = state.manager.object_read(object_id) {
        return Ok(data);
    }
    if let Some(ref dm) = state.distributed {
        return dm
            .distributed_read(object_id)
            .await
            .map_err(|e| e.to_string());
    }
    Err("object data not found locally and no distributed backend".into())
}

/// Read a byte range, falling back to a cross-node full fetch + slice when
/// the local range read misses. Range reads can't be issued remotely
/// (the replica may not hold contiguous chunks), so the fallback fetches
/// the whole object and slices — correct, if less efficient, for the
/// follower-miss case.
async fn fetch_object_range(
    state: &AppState,
    object_id: &str,
    start: u64,
    end: u64,
) -> Result<Vec<u8>, String> {
    if let Ok(data) = state.manager.object_read_range(object_id, start, end) {
        return Ok(data);
    }
    let full = fetch_full_object(state, object_id).await?;
    let s = (start as usize).min(full.len());
    let e = (end as usize).min(full.len());
    if s > e {
        return Err("range out of bounds".into());
    }
    Ok(full[s..e].to_vec())
}

async fn replicate_object_from_storage(
    state: &AppState,
    object_id: &str,
    content_type: Option<&str>,
) -> Result<(), String> {
    if let Some(ref dm) = state.distributed {
        let data = state
            .manager
            .object_read(object_id)
            .map_err(|e| e.to_string())?;
        dm.distributed_write(object_id, &data, content_type)
            .await
            .map_err(|e| e.to_string())?;
    }
    Ok(())
}

async fn persist_object_version(
    state: &AppState,
    bucket: &str,
    key: &str,
    object_id: &str,
    manifest: &nvstor_core::types::ObjectManifest,
    content_type: &str,
    version_id: Option<&str>,
) -> Result<Option<String>, String> {
    let Some(version_id) = version_id else {
        return Ok(None);
    };

    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64;
    let version = nvstor_metadata::ObjectVersion {
        version_id: version_id.to_string(),
        bucket: bucket.to_string(),
        key: key.to_string(),
        is_delete_marker: false,
        is_latest: true,
        size: manifest.size,
        etag: manifest.etag.clone().unwrap_or_default(),
        content_type: content_type.to_string(),
        last_modified: now,
        owner: "default".to_string(),
        manifest_object_id: object_id.to_string(),
    };
    state
        .metadata
        .put_object_version(&version)
        .map_err(|e| e.to_string())?;
    // Strong consistency: replicate the version record to every node.
    replicate_object_version(state, &version).await?;
    sync_current_object_alias(state, bucket, key)?;
    Ok(Some(version_id.to_string()))
}

fn rollback_versioned_object_write(
    state: &AppState,
    bucket: &str,
    key: &str,
    object_id: &str,
    version_id: Option<&str>,
) {
    if let Some(version_id) = version_id {
        let _ = state
            .metadata
            .delete_object_version(bucket, key, version_id);
    }
    let _ = state.manager.object_delete(object_id);
    delete_encryption_metadata(state, object_id);
    if let Some(ref dm) = state.distributed {
        dm.replicate_delete(object_id);
    }
    if version_id.is_some() {
        let _ = sync_current_object_alias(state, bucket, key);
    }
}

// ---------------------------------------------------------------------------
// Bucket handlers — all backed by RocksDB `buckets` CF
// ---------------------------------------------------------------------------

/// GET / — ListBuckets (from RocksDB, not manifest scan)
pub async fn list_buckets(State(state): State<AppState>) -> Response {
    match state.metadata.list_buckets() {
        Ok(buckets) => {
            let names: Vec<String> = buckets.into_iter().map(|b| b.name).collect();
            let body = xml::list_buckets_xml(&names);
            Response::builder()
                .status(StatusCode::OK)
                .header("Content-Type", XML_CONTENT_TYPE)
                .body(Body::from(body))
                .expect("response build")
        }
        Err(e) => s3_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "InternalError",
            &e.to_string(),
            "/",
        ),
    }
}

/// HEAD /{bucket}
pub async fn head_bucket(State(state): State<AppState>, Path(bucket): Path<String>) -> Response {
    match state.metadata.get_bucket(&bucket) {
        Ok(Some(meta)) => Response::builder()
            .status(StatusCode::OK)
            .header("x-amz-bucket-region", &meta.region)
            .body(Body::empty())
            .expect("response build"),
        _ => s3_error(
            StatusCode::NOT_FOUND,
            "NoSuchBucket",
            "The specified bucket does not exist",
            &bucket,
        ),
    }
}

/// GET /{bucket} — ListObjectsV2, or ListObjectVersions, or GetBucketVersioning
pub async fn get_bucket(
    State(state): State<AppState>,
    Path(bucket): Path<String>,
    Query(params): Query<ListObjectsParams>,
) -> Response {
    // Dispatch: ?versioning → GetBucketVersioning
    if params.versioning.is_some() {
        return get_bucket_versioning(State(state), Path(bucket)).await;
    }
    // Dispatch: ?versions → ListObjectVersions
    if params.versions.is_some() {
        return list_object_versions(State(state), Path(bucket)).await;
    }

    if !bucket_exists(&state, &bucket) {
        return s3_error(
            StatusCode::NOT_FOUND,
            "NoSuchBucket",
            "The specified bucket does not exist",
            &bucket,
        );
    }

    let prefix = params.prefix.unwrap_or_default();
    let max_keys = params.max_keys.unwrap_or(1000);
    let full_prefix = format!("{bucket}/{prefix}");

    // Use continuation_token or start_after for pagination.
    let start_after = params
        .continuation_token
        .as_deref()
        .or(params.start_after.as_deref())
        .map(|s| format!("{bucket}/{s}"));

    let (manifests, truncated) =
        match state
            .manager
            .object_list(&full_prefix, start_after.as_deref(), max_keys)
        {
            Ok(r) => r,
            Err(e) => {
                return s3_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "InternalError",
                    &e.to_string(),
                    &bucket,
                );
            }
        };

    let body = xml::list_bucket_result_xml(
        &bucket,
        &prefix,
        &manifests,
        truncated,
        max_keys,
        params.continuation_token.as_deref(),
    );

    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", XML_CONTENT_TYPE)
        .body(Body::from(body))
        .expect("response build")
}

/// PUT /{bucket} — CreateBucket or PutBucketVersioning
/// Validate an S3 bucket name against the AWS naming rules.
///
/// Beyond spec compliance, this is what makes a *misrouted* request fail
/// loudly. A virtual-host-style client sends `PUT /{key}` with the bucket in
/// `Host`; if the server only understands path-style it reads the key as a
/// bucket name. Without validation that silently creates a junk bucket and
/// returns 200, and the client cannot tell. Real object
/// keys routinely contain characters no bucket name may contain — LMCache's
/// S3 connector, for instance, flattens keys to `a_b_c`, and `_` is illegal
/// in a bucket name — so validating here converts a silent wrong answer into
/// a clear 400.
///
/// Rules (AWS general-purpose buckets):
///   * 3–63 characters
///   * lowercase letters, digits, hyphens and dots only
///   * must start and end with a letter or digit
///   * no consecutive dots, and no dot adjacent to a hyphen
///   * must not be formatted as an IPv4 address
///   * reserved prefixes/suffixes (`xn--`, `sthree-`, `-s3alias`, `--ol-s3`)
fn validate_bucket_name(name: &str) -> Result<(), &'static str> {
    if name.len() < 3 || name.len() > 63 {
        return Err("Bucket name must be between 3 and 63 characters long");
    }
    if !name
        .bytes()
        .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-' || b == b'.')
    {
        return Err("Bucket name can contain only lowercase letters, numbers, dots and hyphens");
    }
    let first = name.as_bytes()[0];
    let last = name.as_bytes()[name.len() - 1];
    if !(first.is_ascii_lowercase() || first.is_ascii_digit())
        || !(last.is_ascii_lowercase() || last.is_ascii_digit())
    {
        return Err("Bucket name must begin and end with a letter or number");
    }
    if name.contains("..") || name.contains(".-") || name.contains("-.") {
        return Err("Bucket name must not contain consecutive dots or a dot next to a hyphen");
    }
    // An all-numeric dotted name would be ambiguous with a path-style
    // endpoint address, which is why AWS forbids it.
    if name.split('.').count() == 4
        && name
            .split('.')
            .all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()))
    {
        return Err("Bucket name must not be formatted as an IP address");
    }
    for prefix in ["xn--", "sthree-", "amzn-s3-demo-"] {
        if name.starts_with(prefix) {
            return Err("Bucket name uses a reserved prefix");
        }
    }
    for suffix in ["-s3alias", "--ol-s3"] {
        if name.ends_with(suffix) {
            return Err("Bucket name uses a reserved suffix");
        }
    }
    Ok(())
}

pub async fn create_bucket(
    State(state): State<AppState>,
    axum::extract::OriginalUri(uri): axum::extract::OriginalUri,
    Path(bucket): Path<String>,
    Query(params): Query<ListObjectsParams>,
    body: axum::body::Bytes,
) -> Response {
    if let Some(r) = redirect_to_leader(&state, &uri) {
        return r;
    }
    // Dispatch: ?versioning → PutBucketVersioning
    if params.versioning.is_some() {
        return put_bucket_versioning(State(state), Path(bucket), body).await;
    }

    if let Err(reason) = validate_bucket_name(&bucket) {
        return s3_error(
            StatusCode::BAD_REQUEST,
            "InvalidBucketName",
            reason,
            &bucket,
        );
    }

    if bucket_exists(&state, &bucket) {
        return s3_error(
            StatusCode::CONFLICT,
            "BucketAlreadyOwnedByYou",
            "Your previous request to create the named bucket succeeded",
            &bucket,
        );
    }

    // Propose bucket creation through Raft (replicated) or write directly (single-node).
    let entry = nvstor_raft::types::LogEntryData::BucketPut {
        name: bucket.clone(),
        owner: "default".to_string(),
        region: "us-east-1".to_string(),
        versioning: 0,
    };

    if let Err(msg) = propose_or_apply(&state, entry).await {
        return s3_error(StatusCode::SERVICE_UNAVAILABLE, "SlowDown", &msg, &bucket);
    }

    // If no Raft (single-node), write directly to metadata store.
    if state.raft.is_none() {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs() as i64;
        let bucket_meta = nvstor_metadata::BucketMeta {
            name: bucket.clone(),
            owner: "default".to_string(),
            created_at: now,
            region: "us-east-1".to_string(),
            versioning: 0,
        };
        if let Err(e) = state.metadata.put_bucket(&bucket_meta) {
            return s3_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "InternalError",
                &e.to_string(),
                &bucket,
            );
        }
    }
    // When Raft is active, the state machine applies the entry to every
    // node's MetadataStore after commit — so the legacy best-effort RPC
    // fanout below is both redundant and WRONG (it hardcodes
    // versioning=0, diverging a versioning-enabled bucket on peers).
    // Only use it in the no-Raft fallback.
    if state.raft.is_none() {
        if let Some(ref dm) = state.distributed {
            dm.replicate_bucket_put(&bucket);
        }
    }

    debug!(bucket = %bucket, "bucket created");
    Response::builder()
        .status(StatusCode::OK)
        .header("Location", format!("/{bucket}"))
        .body(Body::empty())
        .expect("response build")
}

/// DELETE /{bucket}
pub async fn delete_bucket(
    State(state): State<AppState>,
    axum::extract::OriginalUri(uri): axum::extract::OriginalUri,
    Path(bucket): Path<String>,
) -> Response {
    if let Some(r) = redirect_to_leader(&state, &uri) {
        return r;
    }
    if !bucket_exists(&state, &bucket) {
        return s3_error(
            StatusCode::NOT_FOUND,
            "NoSuchBucket",
            "The specified bucket does not exist",
            &bucket,
        );
    }

    // Check bucket is empty.
    let prefix = format!("{bucket}/");
    match state.manager.object_list(&prefix, None, 1) {
        Ok((objects, _)) if !objects.is_empty() => {
            return s3_error(
                StatusCode::CONFLICT,
                "BucketNotEmpty",
                "The bucket you tried to delete is not empty",
                &bucket,
            );
        }
        Err(e) => {
            return s3_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "InternalError",
                &e.to_string(),
                &bucket,
            );
        }
        _ => {}
    }

    if uses_version_history(&state, &bucket) {
        match state.metadata.list_bucket_versions(&bucket, 1) {
            Ok(versions) if !versions.is_empty() => {
                return s3_error(
                    StatusCode::CONFLICT,
                    "BucketNotEmpty",
                    "The bucket you tried to delete is not empty",
                    &bucket,
                );
            }
            Err(e) => {
                return s3_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "InternalError",
                    &e.to_string(),
                    &bucket,
                );
            }
            _ => {}
        }
    }

    // Propose through Raft or apply directly.
    let entry = nvstor_raft::types::LogEntryData::BucketDelete {
        name: bucket.clone(),
    };
    if let Err(msg) = propose_or_apply(&state, entry).await {
        return s3_error(StatusCode::SERVICE_UNAVAILABLE, "SlowDown", &msg, &bucket);
    }

    if state.raft.is_none() {
        if let Err(e) = state.metadata.delete_bucket(&bucket) {
            return s3_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "InternalError",
                &e.to_string(),
                &bucket,
            );
        }
        // Legacy best-effort fanout only in the no-Raft fallback; with
        // Raft, the state machine deletes on every node after commit.
        if let Some(ref dm) = state.distributed {
            dm.replicate_bucket_delete(&bucket);
        }
    }

    debug!(bucket = %bucket, "bucket deleted");
    Response::builder()
        .status(StatusCode::NO_CONTENT)
        .body(Body::empty())
        .expect("response build")
}

/// POST /{bucket} — BatchDelete (when ?delete query param is present)
pub async fn post_bucket(
    State(state): State<AppState>,
    axum::extract::OriginalUri(uri): axum::extract::OriginalUri,
    Path(bucket): Path<String>,
    Query(params): Query<DeleteParams>,
    body: axum::body::Bytes,
) -> Response {
    if let Some(r) = redirect_to_leader(&state, &uri) {
        return r;
    }
    if params.delete.is_none() {
        return s3_error(
            StatusCode::BAD_REQUEST,
            "InvalidRequest",
            "Missing ?delete query parameter",
            &bucket,
        );
    }

    // Parse keys from the XML body. Simple extraction of <Key>...</Key> elements.
    let body_str = String::from_utf8_lossy(&body);
    let objects = extract_delete_objects_from_xml(&body_str);

    let mut deleted = Vec::new();
    let mut errors = Vec::new();

    for object in objects {
        match delete_object_for_batch(&state, &bucket, &object.key, object.version_id.as_deref())
            .await
        {
            Ok(result) => deleted.push(result),
            Err((code, message)) => {
                errors.push((object.key, object.version_id, code, message));
            }
        }
    }

    let xml_body = xml::delete_result_xml(&deleted, &errors);
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", XML_CONTENT_TYPE)
        .body(Body::from(xml_body))
        .expect("response build")
}

// ---------------------------------------------------------------------------
// Object handlers
// ---------------------------------------------------------------------------

/// HEAD /{bucket}/{key}
pub async fn head_object(
    State(state): State<AppState>,
    Path((bucket, key)): Path<(String, String)>,
    Query(params): Query<MultipartParams>,
) -> Response {
    let canonical_oid = object_id(&bucket, &key);
    let version = match load_requested_version(&state, &bucket, &key, params.version_id.as_deref())
    {
        Ok(version) => version,
        Err(e) => return s3_error(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", &e, &key),
    };
    if let Some(version_id) = params.version_id.as_deref() {
        if version.is_none() {
            return no_such_version_response(&key, version_id);
        }
    }
    if let Some(ref version) = version {
        if version.is_delete_marker {
            return version_not_found_response(&key, version);
        }
    }

    let oid = version
        .as_ref()
        .map(|version| version.manifest_object_id.as_str())
        .unwrap_or(canonical_oid.as_str());

    match state.manager.object_get_manifest(oid) {
        Ok(manifest) => {
            let mut resp = Response::builder()
                .status(StatusCode::OK)
                .header("Content-Length", manifest.size.to_string())
                .header(
                    "Content-Type",
                    manifest
                        .content_type
                        .as_deref()
                        .unwrap_or("application/octet-stream"),
                )
                .header("ETag", manifest.etag.as_deref().unwrap_or("\"\""))
                .header("Last-Modified", format_http_date(manifest.modified_at));
            if let Some(ref version) = version {
                resp = resp.header("x-amz-version-id", &version.version_id);
            }
            resp.body(Body::empty()).expect("response build")
        }
        Err(_) => s3_error(
            StatusCode::NOT_FOUND,
            "NoSuchKey",
            "The specified key does not exist.",
            &key,
        ),
    }
}

/// GET /{bucket}/{key} — GetObject with optional Range
pub async fn get_object(
    State(state): State<AppState>,
    Path((bucket, key)): Path<(String, String)>,
    Query(params): Query<MultipartParams>,
    headers: HeaderMap,
) -> Response {
    let canonical_oid = object_id(&bucket, &key);
    let version = match load_requested_version(&state, &bucket, &key, params.version_id.as_deref())
    {
        Ok(version) => version,
        Err(e) => return s3_error(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", &e, &key),
    };
    if let Some(version_id) = params.version_id.as_deref() {
        if version.is_none() {
            return no_such_version_response(&key, version_id);
        }
    }
    if let Some(ref version) = version {
        if version.is_delete_marker {
            return version_not_found_response(&key, version);
        }
    }
    let oid = version
        .as_ref()
        .map(|version| version.manifest_object_id.clone())
        .unwrap_or(canonical_oid);

    // Get manifest first for metadata.
    let manifest = match state.manager.object_get_manifest(&oid) {
        Ok(m) => m,
        Err(_) => {
            return s3_error(
                StatusCode::NOT_FOUND,
                "NoSuchKey",
                "The specified key does not exist.",
                &key,
            );
        }
    };

    let content_type = manifest
        .content_type
        .as_deref()
        .unwrap_or("application/octet-stream");
    let etag = manifest.etag.as_deref().unwrap_or("\"\"");
    let last_modified = format_http_date(manifest.modified_at);

    // Check for Range header.
    if let Some(range_header) = headers.get("range").and_then(|v| v.to_str().ok()) {
        // Is this object SSE-encrypted? If so, its bytes on disk are a
        // whole-object AES-GCM ciphertext (single nonce+tag), and
        // `manifest.size` is the CIPHERTEXT length. A range cannot be
        // served from ciphertext — and the client's range is in
        // PLAINTEXT coordinates — so we must decrypt the full object
        // and slice the range against the true plaintext length.
        // Previously this branch returned raw ciphertext with a 206,
        // which the client treated as plaintext (silent corruption /
        // encryption bypass).
        let enc_meta = state
            .kms
            .as_ref()
            .and_then(|_| load_encryption_metadata(&state, &oid));

        // Each arm yields Some((bytes, start, total_len)) for a
        // satisfiable range, or None to fall through to the full read.
        let ranged: Option<(Vec<u8>, u64, u64)> = if let Some(enc_meta) = enc_meta {
            let kms = state
                .kms
                .as_ref()
                .expect("kms present when enc_meta loaded");
            let plaintext = match fetch_full_object(&state, &oid).await {
                Ok(ct) => match kms.decrypt(&ct, &enc_meta) {
                    Ok(pt) => pt,
                    Err(e) => {
                        warn!(key = %key, error = %e, "range decrypt failed");
                        return s3_error(
                            StatusCode::INTERNAL_SERVER_ERROR,
                            "ServerSideEncryptionError",
                            &e,
                            &key,
                        );
                    }
                },
                Err(e) => {
                    warn!(key = %key, error = %e, "range read failed");
                    return s3_error(
                        StatusCode::RANGE_NOT_SATISFIABLE,
                        "InvalidRange",
                        "The requested range is not satisfiable",
                        &key,
                    );
                }
            };
            let plen = plaintext.len() as u64;
            match parse_range(range_header, plen) {
                Some((s, e)) => Some((plaintext[s as usize..e as usize].to_vec(), s, plen)),
                None => {
                    return s3_error(
                        StatusCode::RANGE_NOT_SATISFIABLE,
                        "InvalidRange",
                        "The requested range is not satisfiable",
                        &key,
                    )
                }
            }
        } else {
            match parse_range(range_header, manifest.size) {
                Some((start, end)) => match fetch_object_range(&state, &oid, start, end).await {
                    Ok(data) => Some((data, start, manifest.size)),
                    Err(e) => {
                        warn!(key = %key, error = %e, "range read failed");
                        return s3_error(
                            StatusCode::RANGE_NOT_SATISFIABLE,
                            "InvalidRange",
                            "The requested range is not satisfiable",
                            &key,
                        );
                    }
                },
                None => None, // not a satisfiable range → full read below
            }
        };

        if let Some((data, start, total_len)) = ranged {
            let returned = data.len() as u64;
            let content_range = format!(
                "bytes {}-{}/{}",
                start,
                start + returned.saturating_sub(1),
                total_len
            );
            let mut resp = Response::builder()
                .status(StatusCode::PARTIAL_CONTENT)
                .header("Content-Type", content_type)
                .header("Content-Length", returned.to_string())
                .header("Content-Range", content_range)
                .header("ETag", etag);
            if let Some(ref version) = version {
                resp = resp.header("x-amz-version-id", &version.version_id);
            }
            return resp.body(Body::from(data)).expect("response build");
        }
    }

    // Full read.
    match fetch_full_object(&state, &oid).await {
        Ok(data) => {
            // Decrypt if this object was encrypted with SSE.
            let (output_data, sse_algo) = if let Some(ref kms) = state.kms {
                if let Some(enc_meta) = load_encryption_metadata(&state, &oid) {
                    match kms.decrypt(&data, &enc_meta) {
                        Ok(plaintext) => {
                            let algo = match enc_meta.wrapped_key.master_key_id.as_str() {
                                "SSE-C" => None,
                                id if id == "nvstor-default" => Some("AES256"),
                                _ => Some("aws:kms"),
                            };
                            (plaintext, algo)
                        }
                        Err(e) => {
                            warn!(key = %key, error = %e, "decryption failed");
                            return s3_error(
                                StatusCode::INTERNAL_SERVER_ERROR,
                                "ServerSideEncryptionError",
                                &e,
                                &key,
                            );
                        }
                    }
                } else {
                    (data, None)
                }
            } else {
                (data, None)
            };

            let mut resp = Response::builder()
                .status(StatusCode::OK)
                .header("Content-Type", content_type)
                .header("Content-Length", output_data.len().to_string())
                .header("ETag", etag)
                .header("Last-Modified", &last_modified);
            if let Some(ref version) = version {
                resp = resp.header("x-amz-version-id", &version.version_id);
            }
            if let Some(algo) = sse_algo {
                resp = resp.header("x-amz-server-side-encryption", algo);
            }
            resp.body(Body::from(output_data)).expect("response build")
        }
        Err(e) => {
            warn!(key = %key, error = %e, "object read failed (local + cross-node)");
            s3_error(
                StatusCode::NOT_FOUND,
                "NoSuchKey",
                "The specified key does not exist.",
                &key,
            )
        }
    }
}

/// PUT /{bucket}/{key} — PutObject or CopyObject
pub async fn put_object(
    State(state): State<AppState>,
    axum::extract::OriginalUri(uri): axum::extract::OriginalUri,
    Path((bucket, key)): Path<(String, String)>,
    headers: HeaderMap,
    Query(params): Query<MultipartParams>,
    body: axum::body::Bytes,
) -> Response {
    if let Some(r) = redirect_to_leader(&state, &uri) {
        return r;
    }
    let body = match super::upload::decode(&headers, body) {
        Ok(body) => body,
        Err(error) => {
            let (status, code, message) = match error {
                super::upload::UploadError::BadDigest => (
                    StatusCode::BAD_REQUEST,
                    "BadDigest",
                    "Upload checksum does not match its contents",
                ),
                super::upload::UploadError::InvalidRequest => (
                    StatusCode::BAD_REQUEST,
                    "InvalidRequest",
                    "Malformed AWS upload framing",
                ),
                super::upload::UploadError::Unsupported => (
                    StatusCode::NOT_IMPLEMENTED,
                    "NotImplemented",
                    "Upload signing mode or checksum algorithm is not supported",
                ),
            };
            return s3_error(status, code, message, &key);
        }
    };
    // Upload part: PUT with ?partNumber=N&uploadId=X
    if let (Some(part_number), Some(upload_id)) = (params.part_number, params.upload_id.as_deref())
    {
        return upload_part(&state, &upload_id, part_number, body.to_vec()).await;
    }

    let version_id = version_id_for_new_write(&state, &bucket);
    let oid = object_id_for_version(&bucket, &key, version_id.as_deref());

    // CopyObject: x-amz-copy-source header present.
    if let Some(copy_source) = headers
        .get("x-amz-copy-source")
        .and_then(|v| v.to_str().ok())
    {
        let (src, _) = match resolve_copy_source(&state, copy_source) {
            Ok(source) => source,
            Err(resp) => return resp,
        };
        return copy_object(&state, &src, &oid, &bucket, &key, version_id.as_deref()).await;
    }

    // Regular PutObject.
    let content_type = headers
        .get("content-type")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("application/octet-stream");

    // Quota check before write.
    if let Err(msg) = state.quota.check_quota(&bucket, body.len() as u64) {
        return s3_error(StatusCode::FORBIDDEN, "QuotaExceeded", &msg, &key);
    }
    if let Err(msg) = state.quota.check_rate() {
        return s3_error(StatusCode::TOO_MANY_REQUESTS, "SlowDown", &msg, &key);
    }

    // Server-side encryption: encrypt data if SSE headers present and KMS configured.
    let sse_mode = parse_sse_from_headers(&headers);
    let (write_data, enc_meta): (
        std::borrow::Cow<[u8]>,
        Option<nvstor_kms::types::EncryptionMetadata>,
    ) = if sse_mode != nvstor_kms::types::SseMode::None {
        if let Some(ref kms) = state.kms {
            match kms.encrypt(&body, &sse_mode) {
                Ok((ct, meta)) => (std::borrow::Cow::Owned(ct), Some(meta)),
                Err(e) => {
                    return s3_error(
                        StatusCode::INTERNAL_SERVER_ERROR,
                        "ServerSideEncryptionError",
                        &e,
                        &key,
                    )
                }
            }
        } else {
            // The client explicitly asked for server-side
            // encryption but KMS is not configured. FAIL CLOSED —
            // previously we ignored the header and stored PLAINTEXT
            // while returning 200, so the client believed its data
            // was encrypted when it wasn't. Refuse the write
            // instead of silently downgrading security.
            return s3_error(
                StatusCode::BAD_REQUEST,
                "InvalidRequest",
                "Server-side encryption was requested but is not configured on this server",
                &key,
            );
        }
    } else {
        (std::borrow::Cow::Borrowed(body.as_ref()), None)
    };

    match state
        .manager
        .object_write(&oid, &write_data, Some(content_type))
    {
        Ok(manifest) => {
            let etag = manifest.etag.clone().unwrap_or_default();

            // Store encryption metadata if data was encrypted.
            if let Some(ref meta) = enc_meta {
                if let Err(e) = store_encryption_metadata(&state, &oid, meta) {
                    warn!(error = %e, "failed to store encryption metadata");
                }
                // Strong consistency: replicate the SSE metadata to every
                // node so a follower serving the read can decrypt.
                if let Err(e) = replicate_encryption_meta_put(&state, &oid, meta).await {
                    warn!(error = %e, "failed to replicate encryption metadata via Raft");
                }
            }

            // Replicate to other nodes if distributed mode is enabled.
            // If replication fails below quorum, the PUT fails — no silent data loss.
            if let Some(ref dm) = state.distributed {
                if let Err(e) = dm
                    .distributed_write(&oid, &write_data, Some(content_type))
                    .await
                {
                    warn!(error = %e, "replication failed below quorum");
                    let _ = state.manager.object_delete(&oid);
                    delete_encryption_metadata(&state, &oid);
                    dm.replicate_delete(&oid);
                    return s3_error(
                        StatusCode::SERVICE_UNAVAILABLE,
                        "SlowDown",
                        &format!("Replication failed: {e}"),
                        &key,
                    );
                }
            }

            // Strong consistency: replicate the manifest metadata to
            // every node through Raft (not just the placement targets
            // `distributed_write` sent the DATA to). Roll the local +
            // replica write back on NotLeader / commit failure so a
            // failed PUT leaves nothing behind.
            if let Err(msg) = propose_manifest_put(&state, &oid).await {
                warn!(error = %msg, "manifest Raft propose failed; rolling back");
                let _ = state.manager.object_delete(&oid);
                delete_encryption_metadata(&state, &oid);
                if let Some(ref dm) = state.distributed {
                    dm.replicate_delete(&oid);
                }
                return s3_error(StatusCode::SERVICE_UNAVAILABLE, "SlowDown", &msg, &key);
            }

            // Record quota usage.
            state.quota.record_put(&bucket, manifest.size);

            let version_id = match persist_object_version(
                &state,
                &bucket,
                &key,
                &oid,
                &manifest,
                content_type,
                version_id.as_deref(),
            )
            .await
            {
                Ok(version_id) => version_id,
                Err(e) => {
                    rollback_versioned_object_write(
                        &state,
                        &bucket,
                        &key,
                        &oid,
                        version_id.as_deref(),
                    );
                    warn!(error = %e, "failed to persist object version");
                    return s3_error(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", &e, &key);
                }
            };

            let mut resp = Response::builder()
                .status(StatusCode::OK)
                .header("ETag", format!("\"{etag}\""));
            if let Some(vid) = version_id {
                resp = resp.header("x-amz-version-id", vid);
            }
            // SSE response headers.
            match &sse_mode {
                nvstor_kms::types::SseMode::SseS3 => {
                    resp = resp.header("x-amz-server-side-encryption", "AES256");
                }
                nvstor_kms::types::SseMode::SseKms { key_id } => {
                    resp = resp.header("x-amz-server-side-encryption", "aws:kms");
                    resp = resp.header(
                        "x-amz-server-side-encryption-aws-kms-key-id",
                        key_id.as_str(),
                    );
                }
                _ => {}
            }
            resp.body(Body::empty()).expect("response build")
        }
        Err(e) => s3_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "InternalError",
            &e.to_string(),
            &key,
        ),
    }
}

/// DELETE /{bucket}/{key}
pub async fn delete_object(
    State(state): State<AppState>,
    axum::extract::OriginalUri(uri): axum::extract::OriginalUri,
    Path((bucket, key)): Path<(String, String)>,
    Query(params): Query<MultipartParams>,
) -> Response {
    if let Some(r) = redirect_to_leader(&state, &uri) {
        return r;
    }
    let canonical_oid = object_id(&bucket, &key);
    // Abort multipart: DELETE with ?uploadId=X
    if let Some(upload_id) = params.upload_id.as_deref() {
        return abort_multipart(&state, &bucket, &key, upload_id).await;
    }

    if let Some(version_id) = params.version_id.as_deref() {
        let version = match state.metadata.get_object_version(&bucket, &key, version_id) {
            Ok(Some(version)) => version,
            Ok(None) => {
                return Response::builder()
                    .status(StatusCode::NO_CONTENT)
                    .body(Body::empty())
                    .expect("response build");
            }
            Err(e) => {
                return s3_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "InternalError",
                    &e.to_string(),
                    &key,
                )
            }
        };

        if let Err(e) = state
            .metadata
            .delete_object_version(&bucket, &key, version_id)
        {
            return s3_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "InternalError",
                &e.to_string(),
                &key,
            );
        }
        // Strong consistency: drop the version record on every node.
        if let Err(e) = replicate_object_version_delete(&state, &bucket, &key, version_id).await {
            warn!(key = %key, error = %e, "ObjectVersionDelete Raft propose failed");
        }

        if !version.is_delete_marker
            && !version.manifest_object_id.is_empty()
            && !other_versions_reference_object_id(
                &state,
                &bucket,
                &key,
                &version.manifest_object_id,
                version_id,
            )
            .unwrap_or(true)
        {
            let _ = state.manager.object_delete(&version.manifest_object_id);
            delete_encryption_metadata(&state, &version.manifest_object_id);
            if let Some(ref dm) = state.distributed {
                dm.replicate_delete(&version.manifest_object_id);
            }
            // Strong consistency: drop the now-unreferenced manifest + its
            // encryption metadata on every node through Raft.
            let _ = replicate_encryption_meta_delete(&state, &version.manifest_object_id).await;
            if state.raft.is_some() {
                let entry = nvstor_raft::types::LogEntryData::ManifestDelete {
                    object_id: version.manifest_object_id.clone(),
                };
                if let Err(msg) = propose_or_apply(&state, entry).await {
                    warn!(key = %key, error = %msg, "ManifestDelete Raft propose failed");
                }
            }
        }

        if uses_version_history(&state, &bucket) {
            if let Err(e) = sync_current_object_alias(&state, &bucket, &key) {
                return s3_error(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", &e, &key);
            }
        }

        let mut resp = Response::builder()
            .status(StatusCode::NO_CONTENT)
            .header("x-amz-version-id", version_id);
        if version.is_delete_marker {
            resp = resp.header("x-amz-delete-marker", "true");
        }
        return resp.body(Body::empty()).expect("response build");
    }

    // If versioning is enabled, create a delete marker instead of actually deleting.
    if uses_version_history(&state, &bucket) {
        let vid = match create_delete_marker(&state, &bucket, &key).await {
            Ok(version_id) => version_id,
            Err(e) => {
                return s3_error(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", &e, &key)
            }
        };

        return Response::builder()
            .status(StatusCode::NO_CONTENT)
            .header("x-amz-version-id", &vid)
            .header("x-amz-delete-marker", "true")
            .body(Body::empty())
            .expect("response build");
    }

    // No versioning — actually delete.
    let oid = canonical_oid;
    // Get size before delete for quota tracking.
    let size = state
        .manager
        .object_get_manifest(&oid)
        .ok()
        .map(|m| m.size)
        .unwrap_or(0);

    if let Err(e) = state.manager.object_delete(&oid) {
        warn!(key = %key, error = %e, "delete_object failed");
    } else {
        state.quota.record_delete(&bucket, size);
        // Clean up encryption metadata if present.
        delete_encryption_metadata(&state, &oid);
        // Replicate delete to remote nodes.
        if let Some(ref dm) = state.distributed {
            dm.replicate_delete(&oid);
        }
    }

    // Strong consistency: drop the manifest on every node through Raft.
    // The local chunk cleanup above already unref'd this leader's chunks;
    // the state machine's delete_manifest is idempotent here and removes
    // the manifest on followers that only hold the metadata. Best-effort
    // (the local object is already gone); a NotLeader response is logged
    // rather than surfaced since S3 delete is idempotent and the not-leader
    // redirect front-stops writes once wired.
    let _ = replicate_encryption_meta_delete(&state, &oid).await;
    if state.raft.is_some() {
        let entry = nvstor_raft::types::LogEntryData::ManifestDelete {
            object_id: oid.clone(),
        };
        if let Err(msg) = propose_or_apply(&state, entry).await {
            warn!(key = %key, error = %msg, "manifest ManifestDelete Raft propose failed");
        }
    }

    // S3 delete is always 204 (idempotent).
    Response::builder()
        .status(StatusCode::NO_CONTENT)
        .body(Body::empty())
        .expect("response build")
}

/// POST /{bucket}/{key} — InitiateMultipartUpload or CompleteMultipartUpload
pub async fn post_object(
    State(state): State<AppState>,
    axum::extract::OriginalUri(uri): axum::extract::OriginalUri,
    Path((bucket, key)): Path<(String, String)>,
    Query(params): Query<MultipartParams>,
    body: axum::body::Bytes,
) -> Response {
    if let Some(r) = redirect_to_leader(&state, &uri) {
        return r;
    }
    // Initiate: POST with ?uploads
    if params.uploads.is_some() {
        return initiate_multipart(&state, &bucket, &key).await;
    }

    // Complete: POST with ?uploadId=X
    if let Some(upload_id) = params.upload_id.as_deref() {
        return complete_multipart(&state, &bucket, &key, upload_id, &body).await;
    }

    s3_error(
        StatusCode::BAD_REQUEST,
        "InvalidRequest",
        "Missing query parameter",
        &key,
    )
}

// ---------------------------------------------------------------------------
// Multipart helpers — ALL persistent via RocksDB + ChunkManager
// No in-memory state. Survives server restart.
// ---------------------------------------------------------------------------

/// Part data object ID convention.
fn part_object_id(upload_id: &str, part_number: u32) -> String {
    format!("__multipart__/{upload_id}/part-{part_number:05}")
}

async fn initiate_multipart(state: &AppState, bucket: &str, key: &str) -> Response {
    let upload_id = uuid::Uuid::new_v4().to_string();

    let now = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64;

    let upload_meta = nvstor_metadata::MultipartUploadMeta {
        upload_id: upload_id.clone(),
        bucket: bucket.to_string(),
        key: key.to_string(),
        content_type: "application/octet-stream".to_string(),
        created_at: now,
        owner: "default".to_string(),
    };

    if let Err(e) = state.metadata.put_multipart_upload(&upload_meta) {
        return s3_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "InternalError",
            &e.to_string(),
            key,
        );
    }

    let body = xml::initiate_multipart_xml(bucket, key, &upload_id);
    debug!(bucket, key, upload_id = %upload_id, "multipart initiated (persistent)");
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", XML_CONTENT_TYPE)
        .body(Body::from(body))
        .expect("response build")
}

async fn upload_part(
    state: &AppState,
    upload_id: &str,
    part_number: u32,
    data: Vec<u8>,
) -> Response {
    if !(1..=10_000).contains(&part_number) {
        return s3_error(
            StatusCode::BAD_REQUEST,
            "InvalidArgument",
            "Part number must be between 1 and 10000",
            upload_id,
        );
    }
    // Verify upload exists in metadata.
    match state.metadata.get_multipart_upload(upload_id) {
        Ok(Some(_)) => {}
        _ => {
            return s3_error(
                StatusCode::NOT_FOUND,
                "NoSuchUpload",
                "The specified upload does not exist",
                upload_id,
            )
        }
    }

    // Store part data as an object in ChunkManager (persistent on disk, not heap).
    let part_oid = part_object_id(upload_id, part_number);
    let etag = format!("{:08x}", crc32fast::hash(&data));

    if let Err(e) = state
        .manager
        .object_write(&part_oid, &data, Some("application/octet-stream"))
    {
        return s3_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "InternalError",
            &e.to_string(),
            upload_id,
        );
    }

    // Record part metadata in RocksDB.
    let part_meta = nvstor_metadata::PartMeta {
        part_number,
        etag: etag.clone(),
        size: data.len() as u64,
        object_id: part_oid,
    };
    if let Err(e) = state.metadata.put_part(upload_id, &part_meta) {
        return s3_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "InternalError",
            &e.to_string(),
            upload_id,
        );
    }

    Response::builder()
        .status(StatusCode::OK)
        .header("ETag", format!("\"{etag}\""))
        .body(Body::empty())
        .expect("response build")
}

async fn complete_multipart(
    state: &AppState,
    bucket: &str,
    key: &str,
    upload_id: &str,
    body: &[u8],
) -> Response {
    // Verify upload exists.
    let upload = match state.metadata.get_multipart_upload(upload_id) {
        Ok(Some(u)) => u,
        _ => {
            return s3_error(
                StatusCode::NOT_FOUND,
                "NoSuchUpload",
                "The specified upload does not exist",
                upload_id,
            )
        }
    };

    if upload.bucket != bucket || upload.key != key {
        return s3_error(
            StatusCode::NOT_FOUND,
            "NoSuchUpload",
            "Upload does not belong to this object",
            key,
        );
    }
    let requested = match xml::parse_complete_multipart(body) {
        Ok(parts) => parts,
        Err(code) => {
            return s3_error(
                StatusCode::BAD_REQUEST,
                code,
                "Invalid multipart completion document",
                key,
            )
        }
    };
    // Retain every stored part for cleanup, but assemble only the requested ones.
    let all_parts = match state.metadata.list_parts(upload_id) {
        Ok(p) => p,
        Err(e) => {
            return s3_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "InternalError",
                &e.to_string(),
                key,
            )
        }
    };

    let mut parts = Vec::with_capacity(requested.len());
    for (index, (number, etag)) in requested.iter().enumerate() {
        let Some(part) = all_parts.iter().find(|part| part.part_number == *number) else {
            return s3_error(
                StatusCode::BAD_REQUEST,
                "InvalidPart",
                "Requested part is missing",
                key,
            );
        };
        if part.etag.trim_matches('"') != etag {
            return s3_error(
                StatusCode::BAD_REQUEST,
                "InvalidPart",
                "Part ETag does not match",
                key,
            );
        }
        if index + 1 < requested.len() && part.size < 5 * 1024 * 1024 {
            return s3_error(
                StatusCode::BAD_REQUEST,
                "EntityTooSmall",
                "Non-final parts must be at least 5 MiB",
                key,
            );
        }
        parts.push(part);
    }

    // Assemble final object from part data via streaming write.
    let version_id = version_id_for_new_write(state, bucket);
    let oid = object_id_for_version(bucket, key, version_id.as_deref());
    let mut ctx = state
        .manager
        .object_write_begin(&oid, Some("application/octet-stream"));

    for part in &parts {
        // Read part data from ChunkManager (persistent, not heap).
        let part_data = match state.manager.object_read(&part.object_id) {
            Ok(d) => d,
            Err(e) => {
                let _ = state.manager.object_write_abort(ctx);
                return s3_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    "InternalError",
                    &e.to_string(),
                    key,
                );
            }
        };

        if let Err(e) = state.manager.object_write_chunk(&mut ctx, &part_data) {
            let _ = state.manager.object_write_abort(ctx);
            return s3_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "InternalError",
                &e.to_string(),
                key,
            );
        }
    }

    match state.manager.object_write_finish(ctx) {
        Ok(manifest) => {
            if let Err(e) =
                replicate_object_from_storage(state, &oid, Some("application/octet-stream")).await
            {
                rollback_versioned_object_write(state, bucket, key, &oid, version_id.as_deref());
                return s3_error(
                    StatusCode::SERVICE_UNAVAILABLE,
                    "SlowDown",
                    &format!("Replication failed: {e}"),
                    key,
                );
            }

            // Strong consistency: replicate the assembled object's
            // manifest to every node through Raft.
            if let Err(msg) = propose_manifest_put(state, &oid).await {
                rollback_versioned_object_write(state, bucket, key, &oid, version_id.as_deref());
                return s3_error(StatusCode::SERVICE_UNAVAILABLE, "SlowDown", &msg, key);
            }

            let completed_version_id = match persist_object_version(
                state,
                bucket,
                key,
                &oid,
                &manifest,
                "application/octet-stream",
                version_id.as_deref(),
            )
            .await
            {
                Ok(version_id) => version_id,
                Err(e) => {
                    rollback_versioned_object_write(
                        state,
                        bucket,
                        key,
                        &oid,
                        version_id.as_deref(),
                    );
                    return s3_error(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", &e, key);
                }
            };

            state.quota.record_put(bucket, manifest.size);

            // Clean up selected and unselected parts after publication.
            for part in &all_parts {
                let _ = state.manager.object_delete(&part.object_id);
            }
            let _ = state.metadata.delete_all_parts(upload_id);
            let _ = state.metadata.delete_multipart_upload(upload_id);

            let etag = manifest.etag.as_deref().unwrap_or("\"\"");
            let body = xml::complete_multipart_xml(bucket, key, etag);
            debug!(
                bucket,
                key,
                parts = parts.len(),
                "multipart completed (persistent)"
            );
            let mut resp = Response::builder()
                .status(StatusCode::OK)
                .header("Content-Type", XML_CONTENT_TYPE);
            if let Some(version_id) = completed_version_id {
                resp = resp.header("x-amz-version-id", version_id);
            }
            resp.body(Body::from(body)).expect("response build")
        }
        Err(e) => s3_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "InternalError",
            &e.to_string(),
            key,
        ),
    }
}

async fn abort_multipart(state: &AppState, _bucket: &str, key: &str, upload_id: &str) -> Response {
    // Verify upload exists.
    match state.metadata.get_multipart_upload(upload_id) {
        Ok(Some(_)) => {}
        _ => {
            return s3_error(
                StatusCode::NOT_FOUND,
                "NoSuchUpload",
                "The specified upload does not exist",
                key,
            )
        }
    }

    // Delete all part data objects from ChunkManager.
    if let Ok(parts) = state.metadata.list_parts(upload_id) {
        for part in &parts {
            let _ = state.manager.object_delete(&part.object_id);
        }
    }

    // Delete metadata.
    let _ = state.metadata.delete_all_parts(upload_id);
    let _ = state.metadata.delete_multipart_upload(upload_id);

    debug!(upload_id, "multipart aborted (persistent cleanup)");
    Response::builder()
        .status(StatusCode::NO_CONTENT)
        .body(Body::empty())
        .expect("response build")
}

async fn copy_object(
    state: &AppState,
    src: &str,
    dst: &str,
    bucket: &str,
    key: &str,
    version_id: Option<&str>,
) -> Response {
    match state.manager.object_copy(src, dst) {
        Ok(manifest) => {
            if let Err(e) = copy_encryption_metadata_for_object(state, src, dst) {
                rollback_versioned_object_write(state, bucket, key, dst, version_id);
                return s3_error(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", &e, key);
            }
            // Strong consistency: replicate the copy's SSE metadata state
            // (present or absent) to every node.
            let enc_result = match load_encryption_metadata(state, dst) {
                Some(meta) => replicate_encryption_meta_put(state, dst, &meta).await,
                None => replicate_encryption_meta_delete(state, dst).await,
            };
            if let Err(e) = enc_result {
                warn!(error = %e, "failed to replicate copy encryption metadata via Raft");
            }

            let content_type = manifest
                .content_type
                .as_deref()
                .unwrap_or("application/octet-stream");
            if let Err(e) = replicate_object_from_storage(state, dst, Some(content_type)).await {
                rollback_versioned_object_write(state, bucket, key, dst, version_id);
                return s3_error(
                    StatusCode::SERVICE_UNAVAILABLE,
                    "SlowDown",
                    &format!("Replication failed: {e}"),
                    key,
                );
            }

            // Strong consistency: replicate the copy's manifest to every
            // node through Raft.
            if let Err(msg) = propose_manifest_put(state, dst).await {
                rollback_versioned_object_write(state, bucket, key, dst, version_id);
                return s3_error(StatusCode::SERVICE_UNAVAILABLE, "SlowDown", &msg, key);
            }

            let copied_version_id = match persist_object_version(
                state,
                bucket,
                key,
                dst,
                &manifest,
                content_type,
                version_id,
            )
            .await
            {
                Ok(version_id) => version_id,
                Err(e) => {
                    rollback_versioned_object_write(state, bucket, key, dst, version_id);
                    return s3_error(StatusCode::INTERNAL_SERVER_ERROR, "InternalError", &e, key);
                }
            };

            state.quota.record_put(bucket, manifest.size);
            let etag = manifest.etag.as_deref().unwrap_or("\"\"");
            let body = xml::copy_object_result_xml(etag, manifest.modified_at);
            let mut resp = Response::builder()
                .status(StatusCode::OK)
                .header("Content-Type", XML_CONTENT_TYPE);
            if let Some(version_id) = copied_version_id {
                resp = resp.header("x-amz-version-id", version_id);
            }
            resp.body(Body::from(body)).expect("response build")
        }
        Err(nvstor_core::error::NvstorError::NotFound) => s3_error(
            StatusCode::NOT_FOUND,
            "NoSuchKey",
            "The specified key does not exist.",
            key,
        ),
        Err(e) => s3_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "InternalError",
            &e.to_string(),
            key,
        ),
    }
}

// ---------------------------------------------------------------------------
// Versioning handlers
// ---------------------------------------------------------------------------

#[derive(Deserialize, Default)]
pub struct VersioningQuery {
    pub versioning: Option<String>,
    pub versions: Option<String>,
}

/// PUT /{bucket}?versioning — PutBucketVersioning
pub async fn put_bucket_versioning(
    State(state): State<AppState>,
    Path(bucket): Path<String>,
    body: axum::body::Bytes,
) -> Response {
    let mut meta = match state.metadata.get_bucket(&bucket) {
        Ok(Some(m)) => m,
        _ => {
            return s3_error(
                StatusCode::NOT_FOUND,
                "NoSuchBucket",
                "Bucket not found",
                &bucket,
            )
        }
    };

    // Parse XML body: <VersioningConfiguration><Status>Enabled|Suspended</Status></VersioningConfiguration>
    let body_str = String::from_utf8_lossy(&body);
    if body_str.contains("<Status>Enabled</Status>") {
        meta.versioning = 1;
    } else if body_str.contains("<Status>Suspended</Status>") {
        meta.versioning = 2;
    } else {
        return s3_error(
            StatusCode::BAD_REQUEST,
            "MalformedXML",
            "Invalid versioning configuration",
            &bucket,
        );
    }

    if let Err(e) = state.metadata.put_bucket(&meta) {
        return s3_error(
            StatusCode::INTERNAL_SERVER_ERROR,
            "InternalError",
            &e.to_string(),
            &bucket,
        );
    }

    debug!(bucket = %bucket, versioning = meta.versioning, "bucket versioning updated");
    Response::builder()
        .status(StatusCode::OK)
        .body(Body::empty())
        .expect("response build")
}

/// GET /{bucket}?versioning — GetBucketVersioning
pub async fn get_bucket_versioning(
    State(state): State<AppState>,
    Path(bucket): Path<String>,
) -> Response {
    let meta = match state.metadata.get_bucket(&bucket) {
        Ok(Some(m)) => m,
        _ => {
            return s3_error(
                StatusCode::NOT_FOUND,
                "NoSuchBucket",
                "Bucket not found",
                &bucket,
            )
        }
    };

    let status = match meta.versioning {
        1 => "Enabled",
        2 => "Suspended",
        _ => "", // Not set = empty element
    };

    let body = if status.is_empty() {
        "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<VersioningConfiguration xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\"/>".to_string()
    } else {
        format!(
            "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<VersioningConfiguration xmlns=\"http://s3.amazonaws.com/doc/2006-03-01/\"><Status>{status}</Status></VersioningConfiguration>"
        )
    };

    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", XML_CONTENT_TYPE)
        .body(Body::from(body))
        .expect("response build")
}

/// GET /{bucket}?versions — ListObjectVersions
pub async fn list_object_versions(
    State(state): State<AppState>,
    Path(bucket): Path<String>,
) -> Response {
    if !bucket_exists(&state, &bucket) {
        return s3_error(
            StatusCode::NOT_FOUND,
            "NoSuchBucket",
            "Bucket not found",
            &bucket,
        );
    }

    let versions = match state.metadata.list_bucket_versions(&bucket, 1000) {
        Ok(v) => v,
        Err(e) => {
            return s3_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                "InternalError",
                &e.to_string(),
                &bucket,
            )
        }
    };

    let body = xml::list_versions_xml(&bucket, &versions);
    Response::builder()
        .status(StatusCode::OK)
        .header("Content-Type", XML_CONTENT_TYPE)
        .body(Body::from(body))
        .expect("response build")
}

// ---------------------------------------------------------------------------
// XML parsing helpers
// ---------------------------------------------------------------------------

/// Simple extraction of <Key>...</Key> from delete XML body.
struct DeleteObjectRequest {
    key: String,
    version_id: Option<String>,
}

fn extract_xml_value(body: &str, tag: &str) -> Option<String> {
    let start_tag = format!("<{tag}>");
    let end_tag = format!("</{tag}>");
    let start = body.find(&start_tag)?;
    let after_tag = &body[start + start_tag.len()..];
    let end = after_tag.find(&end_tag)?;
    Some(after_tag[..end].to_string())
}

fn extract_delete_objects_from_xml(body: &str) -> Vec<DeleteObjectRequest> {
    let mut objects = Vec::new();
    let mut remaining = body;
    while let Some(start_idx) = remaining.find("<Object>") {
        let after_tag = &remaining[start_idx + "<Object>".len()..];
        if let Some(end_idx) = after_tag.find("</Object>") {
            let object_xml = &after_tag[..end_idx];
            if let Some(key) = extract_xml_value(object_xml, "Key") {
                objects.push(DeleteObjectRequest {
                    key,
                    version_id: extract_xml_value(object_xml, "VersionId"),
                });
            }
            remaining = &after_tag[end_idx + "</Object>".len()..];
        } else {
            break;
        }
    }
    objects
}

async fn delete_object_for_batch(
    state: &AppState,
    bucket: &str,
    key: &str,
    version_id: Option<&str>,
) -> Result<(String, Option<String>, bool, Option<String>), (String, String)> {
    if let Some(version_id) = version_id {
        let version = state
            .metadata
            .get_object_version(bucket, key, version_id)
            .map_err(|e| ("InternalError".to_string(), e.to_string()))?;

        let Some(version) = version else {
            return Ok((key.to_string(), Some(version_id.to_string()), false, None));
        };

        state
            .metadata
            .delete_object_version(bucket, key, version_id)
            .map_err(|e| ("InternalError".to_string(), e.to_string()))?;
        // Strong consistency: drop the version record on every node.
        if let Err(e) = replicate_object_version_delete(state, bucket, key, version_id).await {
            warn!(bucket, key, version_id, error = %e, "ObjectVersionDelete Raft propose failed");
        }

        if !version.is_delete_marker && !version.manifest_object_id.is_empty() {
            let shared = other_versions_reference_object_id(
                state,
                bucket,
                key,
                &version.manifest_object_id,
                version_id,
            )
            .map_err(|e| ("InternalError".to_string(), e))?;
            if !shared {
                let _ = state.manager.object_delete(&version.manifest_object_id);
                delete_encryption_metadata(state, &version.manifest_object_id);
                if let Some(ref dm) = state.distributed {
                    dm.replicate_delete(&version.manifest_object_id);
                }
                // Strong consistency: drop the now-unreferenced manifest +
                // its encryption metadata on every node.
                let _ = replicate_encryption_meta_delete(state, &version.manifest_object_id).await;
                if state.raft.is_some() {
                    let entry = nvstor_raft::types::LogEntryData::ManifestDelete {
                        object_id: version.manifest_object_id.clone(),
                    };
                    if let Err(e) = propose_or_apply(state, entry).await {
                        warn!(key, error = %e, "ManifestDelete Raft propose failed");
                    }
                }
            }
        }

        if uses_version_history(state, bucket) {
            sync_current_object_alias(state, bucket, key)
                .map_err(|e| ("InternalError".to_string(), e))?;
        }

        return Ok((
            key.to_string(),
            Some(version_id.to_string()),
            version.is_delete_marker,
            version.is_delete_marker.then(|| version_id.to_string()),
        ));
    }

    if uses_version_history(state, bucket) {
        let marker_version_id = create_delete_marker(state, bucket, key)
            .await
            .map_err(|e| ("InternalError".to_string(), e))?;
        return Ok((
            key.to_string(),
            (marker_version_id != NULL_VERSION_ID).then(|| marker_version_id.clone()),
            true,
            Some(marker_version_id),
        ));
    }

    let oid = object_id(bucket, key);
    let size = state
        .manager
        .object_get_manifest(&oid)
        .ok()
        .map(|m| m.size)
        .unwrap_or(0);
    match state.manager.object_delete(&oid) {
        Ok(()) => {
            state.quota.record_delete(bucket, size);
            delete_encryption_metadata(state, &oid);
            if let Some(ref dm) = state.distributed {
                dm.replicate_delete(&oid);
            }
            // Strong consistency: drop the manifest + encryption metadata
            // on every node.
            let _ = replicate_encryption_meta_delete(state, &oid).await;
            if state.raft.is_some() {
                let entry = nvstor_raft::types::LogEntryData::ManifestDelete {
                    object_id: oid.clone(),
                };
                if let Err(e) = propose_or_apply(state, entry).await {
                    warn!(key, error = %e, "ManifestDelete Raft propose failed");
                }
            }
            Ok((key.to_string(), None, false, None))
        }
        Err(nvstor_core::error::NvstorError::NotFound) => Ok((key.to_string(), None, false, None)),
        Err(e) => Err(("InternalError".to_string(), e.to_string())),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn http_dates_match_rfc_7231_for_any_timestamp() {
        assert_eq!(format_http_date(0), "Thu, 01 Jan 1970 00:00:00 GMT");
        assert_eq!(format_http_date(-1), "Wed, 31 Dec 1969 23:59:59 GMT");
        assert_eq!(format_http_date(951782400), "Tue, 29 Feb 2000 00:00:00 GMT");
        assert_eq!(format_http_date(951868799), "Tue, 29 Feb 2000 23:59:59 GMT");
        assert_eq!(
            format_http_date(4102444800),
            "Fri, 01 Jan 2100 00:00:00 GMT"
        );
        assert_eq!(
            format_http_date(253402300799),
            "Fri, 31 Dec 9999 23:59:59 GMT"
        );
        assert_eq!(
            format_http_date(-62135596800),
            "Mon, 01 Jan 0001 00:00:00 GMT"
        );
        assert_eq!(
            format_http_date(1784246399),
            "Thu, 16 Jul 2026 23:59:59 GMT"
        );
        assert_eq!(
            format_http_date(1700000000),
            "Tue, 14 Nov 2023 22:13:20 GMT"
        );
    }

    // Bucket-name validation is what turns a MISROUTED virtual-host
    // request into a visible error instead of a junk bucket + HTTP 200.
    #[test]
    fn bucket_name_validation_matches_aws_rules() {
        for ok in ["abc", "my-bucket", "my.bucket", "bucket123", "a-b-c-1"] {
            assert!(validate_bucket_name(ok).is_ok(), "{ok} should be valid");
        }
        for bad in [
            "ab",               // too short
            &"a".repeat(64),    // too long
            "UPPERCASE",        // uppercase
            "has_underscore",   // underscore
            "-leading-dash",    // must start alnum
            "trailing-dash-",   // must end alnum
            "x.y..z",           // consecutive dots
            "x.-y",             // dot next to hyphen
            "192.168.1.1",      // IP-formatted
            "xn--puny",         // reserved prefix
            "mybucket-s3alias", // reserved suffix
        ] {
            assert!(
                validate_bucket_name(bad).is_err(),
                "{bad} should be invalid"
            );
        }
    }

    // The concrete LMCache shape: its S3 connector flattens keys to `a_b_c`,
    // and `_` is illegal in a bucket name — so a virtual-host client hitting
    // a path-style-only server now gets 400 rather than a silent success.
    #[test]
    fn flattened_object_key_is_rejected_as_a_bucket_name() {
        assert!(validate_bucket_name("vllm_kvcache_layer0_chunk7").is_err());
    }

    #[test]
    fn test_parse_range_bytes_start_end() {
        assert_eq!(parse_range("bytes=0-99", 1000), Some((0, 100)));
    }

    #[test]
    fn test_parse_range_suffix() {
        assert_eq!(parse_range("bytes=-100", 1000), Some((900, 1000)));
    }

    #[test]
    fn test_parse_range_from_offset() {
        assert_eq!(parse_range("bytes=500-", 1000), Some((500, 1000)));
    }

    #[test]
    fn test_parse_range_invalid() {
        assert_eq!(parse_range("invalid", 1000), None);
    }

    #[test]
    fn test_decode_percent_encoded() {
        assert_eq!(
            decode_percent_encoded("folder%20name%2Ffile.txt").unwrap(),
            "folder name/file.txt"
        );
    }

    #[test]
    fn test_decode_percent_encoded_invalid() {
        assert!(decode_percent_encoded("%zz").is_err());
        assert!(decode_percent_encoded("%").is_err());
    }

    #[test]
    fn test_extract_delete_objects_from_xml() {
        let xml = r#"<Delete><Object><Key>file1.txt</Key></Object><Object><Key>file2.txt</Key><VersionId>ver-2</VersionId></Object></Delete>"#;
        let objects = extract_delete_objects_from_xml(xml);
        assert_eq!(objects.len(), 2);
        assert_eq!(objects[0].key, "file1.txt");
        assert_eq!(objects[0].version_id, None);
        assert_eq!(objects[1].key, "file2.txt");
        assert_eq!(objects[1].version_id.as_deref(), Some("ver-2"));
    }

    #[test]
    fn test_object_id() {
        assert_eq!(object_id("mybucket", "path/to/key"), "mybucket/path/to/key");
    }

    #[test]
    fn test_leader_redirect_location_preserves_path_and_query() {
        // Versioned delete: path + query must both survive so the re-sent
        // request to the leader is identical.
        assert_eq!(
            leader_redirect_location("http://10.0.0.2:9000", "/b/k?versionId=v1"),
            "http://10.0.0.2:9000/b/k?versionId=v1"
        );
        // Plain object PUT.
        assert_eq!(
            leader_redirect_location("http://node1:9000", "/bucket/obj.bin"),
            "http://node1:9000/bucket/obj.bin"
        );
    }

    #[test]
    fn test_leader_redirect_location_trims_trailing_slash() {
        // A trailing slash on the advertised endpoint must not produce "//".
        assert_eq!(
            leader_redirect_location("http://10.0.0.2:9000/", "/b/k"),
            "http://10.0.0.2:9000/b/k"
        );
    }
}
