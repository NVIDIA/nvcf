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

//! S3 HTTP integration tests — validates the full S3 API via HTTP requests.
//!
//! These tests start a real Axum HTTP server and send S3-compatible HTTP
//! requests, testing the exact protocol that AWS CLI and SDKs use.

use std::sync::Arc;

use axum::body::Body;
use http::Request;
use nvstor_metadata::MetadataStore;
use nvstor_object::s3::{self, AppState};
use nvstor_storage::backend::PosixBackend;
use nvstor_storage::manager::{ChunkManager, ChunkManagerConfig};
use tower::util::ServiceExt; // for oneshot()

fn setup_state() -> (tempfile::TempDir, AppState) {
    let tmp = tempfile::TempDir::new().expect("tempdir");
    let data_dir = tmp.path().join("data");
    let meta_dir = tmp.path().join("meta");
    let backend = Box::new(PosixBackend::new(&data_dir).expect("backend"));
    let metadata = Arc::new(MetadataStore::open(&meta_dir).expect("metadata"));
    let config = ChunkManagerConfig {
        disk_usage_threshold: 1.0,
        ..ChunkManagerConfig::default()
    };
    let mgr = Arc::new(ChunkManager::new(backend, metadata.clone(), config));
    let state = AppState {
        manager: mgr,
        metadata,
        quota: Arc::new(nvstor_object::quota::QuotaManager::unlimited()),
        raft: None,
        iam: None,
        kms: None,
        distributed: None,
    };
    (tmp, state)
}

fn router(state: AppState) -> axum::Router {
    s3::router(state)
}

async fn body_string(body: Body) -> String {
    let bytes = axum::body::to_bytes(body, 1024 * 1024).await.unwrap();
    String::from_utf8(bytes.to_vec()).unwrap()
}

async fn body_bytes(body: Body) -> Vec<u8> {
    axum::body::to_bytes(body, 1024 * 1024)
        .await
        .unwrap()
        .to_vec()
}

// ---------------------------------------------------------------------------
// Bucket operations
// ---------------------------------------------------------------------------

#[tokio::test]
async fn create_list_delete_bucket() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    // Create bucket.
    let req = Request::builder()
        .method("PUT")
        .uri("/test-bucket")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    // List buckets.
    let req = Request::builder()
        .method("GET")
        .uri("/")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    assert!(xml.contains("<Name>test-bucket</Name>"));

    // Head bucket.
    let req = Request::builder()
        .method("HEAD")
        .uri("/test-bucket")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    // Delete bucket.
    let req = Request::builder()
        .method("DELETE")
        .uri("/test-bucket")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);

    // Head bucket should now fail.
    let req = Request::builder()
        .method("HEAD")
        .uri("/test-bucket")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 404);
}

#[tokio::test]
async fn create_duplicate_bucket() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/dupe")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/dupe")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 409); // BucketAlreadyOwnedByYou
}

#[tokio::test]
async fn delete_bucket_rejects_hidden_version_history() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/histbucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/histbucket?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Enabled</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/histbucket/object.txt")
        .body(Body::from("persisted"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let version_1 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("DELETE")
        .uri("/histbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);
    let delete_marker = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("DELETE")
        .uri("/histbucket")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 409);

    let req = Request::builder()
        .method("DELETE")
        .uri(format!("/histbucket/object.txt?versionId={delete_marker}"))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);

    let req = Request::builder()
        .method("DELETE")
        .uri(format!("/histbucket/object.txt?versionId={version_1}"))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);

    let req = Request::builder()
        .method("DELETE")
        .uri("/histbucket")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);
}

// ---------------------------------------------------------------------------
// Object operations
// ---------------------------------------------------------------------------

#[tokio::test]
async fn put_get_head_delete_object() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    // Create bucket first.
    let req = Request::builder()
        .method("PUT")
        .uri("/mybucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    // PUT object.
    let data = b"Hello, nvstor S3!";
    let req = Request::builder()
        .method("PUT")
        .uri("/mybucket/greeting.txt")
        .header("content-type", "text/plain")
        .body(Body::from(data.to_vec()))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert!(resp.headers().contains_key("etag"));

    // GET object.
    let req = Request::builder()
        .method("GET")
        .uri("/mybucket/greeting.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["content-type"], "text/plain");
    let body = body_bytes(resp.into_body()).await;
    assert_eq!(body, data);

    // HEAD object.
    let req = Request::builder()
        .method("HEAD")
        .uri("/mybucket/greeting.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["content-length"], data.len().to_string());

    // DELETE object.
    let req = Request::builder()
        .method("DELETE")
        .uri("/mybucket/greeting.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);

    // GET should now 404.
    let req = Request::builder()
        .method("GET")
        .uri("/mybucket/greeting.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 404);
}

#[tokio::test]
async fn get_nonexistent_object() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/b")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("GET")
        .uri("/b/nope.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 404);
}

#[tokio::test]
async fn range_read() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/rb")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let data = b"0123456789ABCDEF";
    let req = Request::builder()
        .method("PUT")
        .uri("/rb/data.bin")
        .body(Body::from(data.to_vec()))
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    // Range: bytes=4-7
    let req = Request::builder()
        .method("GET")
        .uri("/rb/data.bin")
        .header("range", "bytes=4-7")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 206); // Partial Content
    let body = body_bytes(resp.into_body()).await;
    assert_eq!(body, b"4567");
}

// ---------------------------------------------------------------------------
// ListObjectsV2
// ---------------------------------------------------------------------------

#[tokio::test]
async fn list_objects() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/listbucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    // Put 3 objects.
    for name in &["a.txt", "b.txt", "c.txt"] {
        let req = Request::builder()
            .method("PUT")
            .uri(format!("/listbucket/{name}"))
            .body(Body::from("data"))
            .unwrap();
        app.clone().oneshot(req).await.unwrap();
    }

    // List objects.
    let req = Request::builder()
        .method("GET")
        .uri("/listbucket?list-type=2")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    assert!(xml.contains("<Key>a.txt</Key>"));
    assert!(xml.contains("<Key>b.txt</Key>"));
    assert!(xml.contains("<Key>c.txt</Key>"));
}

// ---------------------------------------------------------------------------
// Copy object
// ---------------------------------------------------------------------------

#[tokio::test]
async fn copy_object() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/copybucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    // Put source.
    let data = b"checkpoint model weights";
    let req = Request::builder()
        .method("PUT")
        .uri("/copybucket/source.pt")
        .body(Body::from(data.to_vec()))
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    // Copy via x-amz-copy-source header.
    let req = Request::builder()
        .method("PUT")
        .uri("/copybucket/dest.pt")
        .header("x-amz-copy-source", "/copybucket/source.pt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    // Read copy.
    let req = Request::builder()
        .method("GET")
        .uri("/copybucket/dest.pt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let body = body_bytes(resp.into_body()).await;
    assert_eq!(body, data);
}

#[tokio::test]
async fn copy_object_with_versioned_source_and_destination() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/copyver")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/copyver?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Enabled</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/copyver/source.txt")
        .body(Body::from("first source"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let source_v1 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("PUT")
        .uri("/copyver/source.txt")
        .body(Body::from("second source"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/copyver/dest.txt")
        .header("x-amz-copy-source", "/copyver/source.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let dest_v1 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("GET")
        .uri("/copyver/dest.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], dest_v1.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"second source");

    let req = Request::builder()
        .method("PUT")
        .uri("/copyver/dest.txt")
        .header(
            "x-amz-copy-source",
            format!("/copyver/source.txt?versionId={source_v1}"),
        )
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let dest_v2 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();
    assert_ne!(dest_v1, dest_v2);

    let req = Request::builder()
        .method("GET")
        .uri("/copyver/dest.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], dest_v2.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"first source");

    let req = Request::builder()
        .method("GET")
        .uri(format!("/copyver/dest.txt?versionId={dest_v1}"))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], dest_v1.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"second source");
}

#[tokio::test]
async fn copy_object_decodes_percent_encoded_source_key() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/copyenc")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/copyenc/source%20file.txt")
        .body(Body::from("encoded source"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/copyenc/dest.txt")
        .header("x-amz-copy-source", "/copyenc/source%20file.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("GET")
        .uri("/copyenc/dest.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(body_bytes(resp.into_body()).await, b"encoded source");
}

// ---------------------------------------------------------------------------
// Batch delete
// ---------------------------------------------------------------------------

#[tokio::test]
async fn batch_delete() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/delbucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    for name in &["x.txt", "y.txt", "z.txt"] {
        let req = Request::builder()
            .method("PUT")
            .uri(format!("/delbucket/{name}"))
            .body(Body::from("data"))
            .unwrap();
        app.clone().oneshot(req).await.unwrap();
    }

    // Batch delete via POST with ?delete
    let delete_xml =
        r#"<Delete><Object><Key>x.txt</Key></Object><Object><Key>y.txt</Key></Object></Delete>"#;
    let req = Request::builder()
        .method("POST")
        .uri("/delbucket?delete")
        .body(Body::from(delete_xml))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    assert!(xml.contains("<Deleted>"));
    assert!(xml.contains("<Key>x.txt</Key>"));
    assert!(xml.contains("<Key>y.txt</Key>"));

    // z.txt should still exist.
    let req = Request::builder()
        .method("HEAD")
        .uri("/delbucket/z.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
}

#[tokio::test]
async fn batch_delete_with_versioning() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/delver")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/delver?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Enabled</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/delver/object.txt")
        .body(Body::from("first"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let version_1 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("PUT")
        .uri("/delver/object.txt")
        .body(Body::from("second"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let version_2 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let delete_xml = r#"<Delete><Object><Key>object.txt</Key></Object></Delete>"#;
    let req = Request::builder()
        .method("POST")
        .uri("/delver?delete")
        .body(Body::from(delete_xml))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    assert!(xml.contains("<DeleteMarker>true</DeleteMarker>"));
    let delete_marker_version = xml
        .split("<DeleteMarkerVersionId>")
        .nth(1)
        .unwrap()
        .split("</DeleteMarkerVersionId>")
        .next()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("GET")
        .uri("/delver/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 404);
    assert_eq!(resp.headers()["x-amz-delete-marker"], "true");

    let delete_xml = format!(
        "<Delete><Object><Key>object.txt</Key><VersionId>{delete_marker_version}</VersionId></Object></Delete>"
    );
    let req = Request::builder()
        .method("POST")
        .uri("/delver?delete")
        .body(Body::from(delete_xml))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    assert!(xml.contains(&format!("<VersionId>{delete_marker_version}</VersionId>")));

    let req = Request::builder()
        .method("GET")
        .uri("/delver/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], version_2.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"second");

    let delete_xml = format!(
        "<Delete><Object><Key>object.txt</Key><VersionId>{version_2}</VersionId></Object></Delete>"
    );
    let req = Request::builder()
        .method("POST")
        .uri("/delver?delete")
        .body(Body::from(delete_xml))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    assert!(xml.contains(&format!("<VersionId>{version_2}</VersionId>")));

    let req = Request::builder()
        .method("GET")
        .uri("/delver/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], version_1.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"first");
}

// ---------------------------------------------------------------------------
// Multipart upload
// ---------------------------------------------------------------------------

#[tokio::test]
async fn multipart_upload() {
    let (_tmp, state) = setup_state();
    let app = router(state).layer(axum::extract::DefaultBodyLimit::max(8 * 1024 * 1024));

    let req = Request::builder()
        .method("PUT")
        .uri("/mpbucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    // Initiate multipart.
    let req = Request::builder()
        .method("POST")
        .uri("/mpbucket/big-model.pt?uploads")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    // Extract upload ID from XML.
    let upload_id = xml
        .split("<UploadId>")
        .nth(1)
        .unwrap()
        .split("</UploadId>")
        .next()
        .unwrap()
        .to_string();
    assert!(!upload_id.is_empty());

    // Upload 2 parts.
    let part1_data = vec![0xAA; 5 * 1024 * 1024]; // Minimum non-final part size
    let req = Request::builder()
        .method("PUT")
        .uri(format!(
            "/mpbucket/big-model.pt?partNumber=1&uploadId={upload_id}"
        ))
        .body(Body::from(part1_data.clone()))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let part2_data = vec![0xBB; 3 * 1024]; // 3 KB
    let req = Request::builder()
        .method("PUT")
        .uri(format!(
            "/mpbucket/big-model.pt?partNumber=2&uploadId={upload_id}"
        ))
        .body(Body::from(part2_data.clone()))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    // Complete multipart.
    let complete_xml = format!(
        "<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>{:08x}</ETag></Part><Part><PartNumber>2</PartNumber><ETag>{:08x}</ETag></Part></CompleteMultipartUpload>",
        crc32fast::hash(&part1_data), crc32fast::hash(&part2_data)
    );
    let req = Request::builder()
        .method("POST")
        .uri(format!("/mpbucket/big-model.pt?uploadId={upload_id}"))
        .body(Body::from(complete_xml))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    // Read the assembled object.
    let req = Request::builder()
        .method("GET")
        .uri("/mpbucket/big-model.pt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let body = axum::body::to_bytes(resp.into_body(), 8 * 1024 * 1024)
        .await
        .unwrap();
    assert_eq!(body.len(), part1_data.len() + part2_data.len());
    assert_eq!(&body[..part1_data.len()], &part1_data[..]);
    assert_eq!(&body[part1_data.len()..], &part2_data[..]);
}

#[tokio::test]
async fn multipart_upload_creates_new_version() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/mpver")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/mpver?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Enabled</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/mpver/model.bin")
        .body(Body::from("seed"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let seed_version = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("POST")
        .uri("/mpver/model.bin?uploads")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    let upload_id = xml
        .split("<UploadId>")
        .nth(1)
        .unwrap()
        .split("</UploadId>")
        .next()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("PUT")
        .uri(format!(
            "/mpver/model.bin?partNumber=1&uploadId={upload_id}"
        ))
        .body(Body::from("hello world"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let complete_xml = format!(
        r#"<CompleteMultipartUpload>
        <Part><PartNumber>1</PartNumber><ETag>"{:08x}"</ETag></Part>
    </CompleteMultipartUpload>"#,
        crc32fast::hash(b"hello world")
    );
    let req = Request::builder()
        .method("POST")
        .uri(format!("/mpver/model.bin?uploadId={upload_id}"))
        .body(Body::from(complete_xml))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let multipart_version = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();
    assert_ne!(seed_version, multipart_version);

    let req = Request::builder()
        .method("GET")
        .uri("/mpver/model.bin")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(
        resp.headers()["x-amz-version-id"],
        multipart_version.as_str()
    );
    assert_eq!(body_bytes(resp.into_body()).await, b"hello world");

    let req = Request::builder()
        .method("GET")
        .uri(format!("/mpver/model.bin?versionId={seed_version}"))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], seed_version.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"seed");
}

// ---------------------------------------------------------------------------
// Large object
// ---------------------------------------------------------------------------

#[tokio::test]
async fn large_object_1mb() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/lgbucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    // 1 MB object.
    let data: Vec<u8> = (0..1024 * 1024).map(|i| (i % 256) as u8).collect();
    let req = Request::builder()
        .method("PUT")
        .uri("/lgbucket/large.bin")
        .body(Body::from(data.clone()))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    // Read back and verify.
    let req = Request::builder()
        .method("GET")
        .uri("/lgbucket/large.bin")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let body = body_bytes(resp.into_body()).await;
    assert_eq!(body.len(), 1024 * 1024);
    assert_eq!(body, data);
}

#[tokio::test]
async fn versioning_put_get_delete_marker_and_restore() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/verbucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/verbucket?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Enabled</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/verbucket/object.txt")
        .body(Body::from("first"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let version_1 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("PUT")
        .uri("/verbucket/object.txt")
        .body(Body::from("second"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let version_2 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();
    assert_ne!(version_1, version_2);

    let req = Request::builder()
        .method("GET")
        .uri("/verbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], version_2.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"second");

    let req = Request::builder()
        .method("GET")
        .uri(format!("/verbucket/object.txt?versionId={version_1}"))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], version_1.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"first");

    let req = Request::builder()
        .method("DELETE")
        .uri(format!("/verbucket/object.txt?versionId={version_2}"))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);
    assert_eq!(resp.headers()["x-amz-version-id"], version_2.as_str());

    let req = Request::builder()
        .method("GET")
        .uri("/verbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], version_1.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"first");

    let req = Request::builder()
        .method("PUT")
        .uri("/verbucket/object.txt")
        .body(Body::from("third"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let version_3 = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("DELETE")
        .uri("/verbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);
    assert_eq!(resp.headers()["x-amz-delete-marker"], "true");
    let delete_marker_version = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("GET")
        .uri("/verbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 404);
    assert_eq!(resp.headers()["x-amz-delete-marker"], "true");

    let req = Request::builder()
        .method("DELETE")
        .uri(format!(
            "/verbucket/object.txt?versionId={delete_marker_version}"
        ))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);
    assert_eq!(resp.headers()["x-amz-delete-marker"], "true");

    let req = Request::builder()
        .method("GET")
        .uri("/verbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], version_3.as_str());
    assert_eq!(body_bytes(resp.into_body()).await, b"third");
}

#[tokio::test]
async fn suspended_versioning_uses_null_version() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendbucket")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendbucket?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Enabled</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendbucket/object.txt")
        .body(Body::from("archived"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let archived_version = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendbucket?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Suspended</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendbucket/object.txt")
        .body(Body::from("mutable"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], "null");

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendbucket/object.txt")
        .body(Body::from("mutable-2"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], "null");

    let req = Request::builder()
        .method("GET")
        .uri("/suspendbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], "null");
    assert_eq!(body_bytes(resp.into_body()).await, b"mutable-2");

    let req = Request::builder()
        .method("GET")
        .uri(format!(
            "/suspendbucket/object.txt?versionId={archived_version}"
        ))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(
        resp.headers()["x-amz-version-id"],
        archived_version.as_str()
    );
    assert_eq!(body_bytes(resp.into_body()).await, b"archived");

    let req = Request::builder()
        .method("DELETE")
        .uri("/suspendbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);
    assert_eq!(resp.headers()["x-amz-version-id"], "null");
    assert_eq!(resp.headers()["x-amz-delete-marker"], "true");

    let req = Request::builder()
        .method("GET")
        .uri("/suspendbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 404);
    assert_eq!(resp.headers()["x-amz-delete-marker"], "true");
    assert_eq!(resp.headers()["x-amz-version-id"], "null");

    let req = Request::builder()
        .method("DELETE")
        .uri("/suspendbucket/object.txt?versionId=null")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 204);
    assert_eq!(resp.headers()["x-amz-version-id"], "null");
    assert_eq!(resp.headers()["x-amz-delete-marker"], "true");

    let req = Request::builder()
        .method("GET")
        .uri("/suspendbucket/object.txt")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(
        resp.headers()["x-amz-version-id"],
        archived_version.as_str()
    );
    assert_eq!(body_bytes(resp.into_body()).await, b"archived");
}

#[tokio::test]
async fn suspended_versioning_multipart_uses_null_version() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendmp")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendmp?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Enabled</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendmp/object.bin")
        .body(Body::from("archived"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let archived_version = resp.headers()["x-amz-version-id"]
        .to_str()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("PUT")
        .uri("/suspendmp?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Suspended</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("POST")
        .uri("/suspendmp/object.bin?uploads")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    let xml = body_string(resp.into_body()).await;
    let upload_id = xml
        .split("<UploadId>")
        .nth(1)
        .unwrap()
        .split("</UploadId>")
        .next()
        .unwrap()
        .to_string();

    let req = Request::builder()
        .method("PUT")
        .uri(format!(
            "/suspendmp/object.bin?partNumber=1&uploadId={upload_id}"
        ))
        .body(Body::from("hello world"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let complete_xml = format!(
        r#"<CompleteMultipartUpload>
        <Part><PartNumber>1</PartNumber><ETag>"{:08x}"</ETag></Part>
    </CompleteMultipartUpload>"#,
        crc32fast::hash(b"hello world")
    );
    let req = Request::builder()
        .method("POST")
        .uri(format!("/suspendmp/object.bin?uploadId={upload_id}"))
        .body(Body::from(complete_xml))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], "null");

    let req = Request::builder()
        .method("GET")
        .uri("/suspendmp/object.bin")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["x-amz-version-id"], "null");
    assert_eq!(body_bytes(resp.into_body()).await, b"hello world");

    let req = Request::builder()
        .method("GET")
        .uri(format!(
            "/suspendmp/object.bin?versionId={archived_version}"
        ))
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(
        resp.headers()["x-amz-version-id"],
        archived_version.as_str()
    );
    assert_eq!(body_bytes(resp.into_body()).await, b"archived");
}

#[tokio::test]
async fn get_missing_version_returns_no_such_version() {
    let (_tmp, state) = setup_state();
    let app = router(state);

    let req = Request::builder()
        .method("PUT")
        .uri("/noversion")
        .body(Body::empty())
        .unwrap();
    app.clone().oneshot(req).await.unwrap();

    let req = Request::builder()
        .method("PUT")
        .uri("/noversion?versioning")
        .body(Body::from(
            r#"<?xml version="1.0" encoding="UTF-8"?>
            <VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
              <Status>Enabled</Status>
            </VersioningConfiguration>"#,
        ))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("PUT")
        .uri("/noversion/object.txt")
        .body(Body::from("present"))
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 200);

    let req = Request::builder()
        .method("GET")
        .uri("/noversion/object.txt?versionId=missing-version")
        .body(Body::empty())
        .unwrap();
    let resp = app.clone().oneshot(req).await.unwrap();
    assert_eq!(resp.status(), 404);
    assert_eq!(resp.headers()["x-amz-version-id"], "missing-version");
    let body = body_string(resp.into_body()).await;
    assert!(body.contains("<Code>NoSuchVersion</Code>"));
}

// ---------------------------------------------------------------------------
// Virtual-host-style addressing
// ---------------------------------------------------------------------------

/// Router with virtual-host addressing enabled for `s3.test:9000`.
fn vhost_router(state: AppState) -> axum::Router {
    s3::router_with_virtual_host(
        state,
        s3::VirtualHostConfig::new(Some("s3.test:9000".into())),
    )
}

/// A virtual-host PUT then GET must address the OBJECT, not be misread as
/// a bucket operation. Misread, the GET would return
/// `<ListBucketResult><Name>justkey</Name>` with HTTP 200 — a silent wrong answer the client could not detect.
#[tokio::test]
async fn virtual_host_put_get_addresses_the_object() {
    let (_dir, state) = setup_state();

    // Bucket created path-style.
    let resp = vhost_router(state.clone())
        .oneshot(
            Request::builder()
                .method("PUT")
                .uri("/testbucket")
                .header("Host", "s3.test:9000")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200, "path-style bucket create");

    // Virtual-host PUT: bucket in Host, key alone in the path.
    let resp = vhost_router(state.clone())
        .oneshot(
            Request::builder()
                .method("PUT")
                .uri("/justkey")
                .header("Host", "testbucket.s3.test:9000")
                .body(Body::from("world2"))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200, "virtual-host PUT");

    // Virtual-host GET returns the object body, not a bucket listing.
    let resp = vhost_router(state.clone())
        .oneshot(
            Request::builder()
                .uri("/justkey")
                .header("Host", "testbucket.s3.test:9000")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    let body = body_string(resp.into_body()).await;
    assert_eq!(body, "world2", "must return the object, got: {body}");
    assert!(
        !body.contains("ListBucketResult"),
        "regressed to a bucket listing"
    );

    // And the object is genuinely in `testbucket` — path-style sees it.
    let resp = vhost_router(state)
        .oneshot(
            Request::builder()
                .uri("/testbucket/justkey")
                .header("Host", "s3.test:9000")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(body_string(resp.into_body()).await, "world2");
}

/// Path-style must be unaffected when virtual-host addressing is enabled.
#[tokio::test]
async fn path_style_still_works_with_virtual_host_enabled() {
    let (_dir, state) = setup_state();

    for (method, uri, body) in [
        ("PUT", "/pathbucket", Body::empty()),
        ("PUT", "/pathbucket/a/b/c.txt", Body::from("nested")),
    ] {
        let resp = vhost_router(state.clone())
            .oneshot(
                Request::builder()
                    .method(method)
                    .uri(uri)
                    .header("Host", "s3.test:9000")
                    .body(body)
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(resp.status(), 200, "{method} {uri}");
    }

    let resp = vhost_router(state)
        .oneshot(
            Request::builder()
                .uri("/pathbucket/a/b/c.txt")
                .header("Host", "s3.test:9000")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(body_string(resp.into_body()).await, "nested");
}

/// With virtual-host addressing disabled (no domain configured), a client
/// key that is not a legal bucket name must fail loudly rather than create
/// a junk bucket. This is the LMCache shape — keys flattened to `a_b_c`.
#[tokio::test]
async fn misrouted_virtual_host_key_is_rejected_not_silently_created() {
    let (_dir, state) = setup_state();

    let resp = router(state)
        .oneshot(
            Request::builder()
                .method("PUT")
                .uri("/vllm_kvcache_layer0")
                .header("Host", "mybucket.127.0.0.1:9000")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(resp.status(), 400, "must not silently create a bucket");
    let body = body_string(resp.into_body()).await;
    assert!(body.contains("InvalidBucketName"), "got: {body}");
}

#[tokio::test]
async fn multipart_completion_validates_selection_before_publishing() {
    let (_tmp, state) = setup_state();
    let app = router(state.clone());
    app.clone()
        .oneshot(
            Request::builder()
                .method("PUT")
                .uri("/selected")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/selected/object?uploads")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    let response = body_string(response.into_body()).await;
    let upload_id = response
        .split("<UploadId>")
        .nth(1)
        .unwrap()
        .split("</UploadId>")
        .next()
        .unwrap();
    let mut etags = Vec::new();
    for (index, data) in ["unused", "selected"].iter().enumerate() {
        let response = app
            .clone()
            .oneshot(
                Request::builder()
                    .method("PUT")
                    .uri(format!(
                        "/selected/object?uploadId={upload_id}&partNumber={}",
                        index + 1
                    ))
                    .body(Body::from(data.to_string()))
                    .unwrap(),
            )
            .await
            .unwrap();
        etags.push(response.headers()["etag"].to_str().unwrap().to_string());
    }
    for (parts, expected) in [
        ("<Part><PartNumber>2</PartNumber><ETag>wrong</ETag></Part>".to_string(), "InvalidPart"),
        (format!("<Part><PartNumber>2</PartNumber><ETag>{}</ETag></Part><Part><PartNumber>1</PartNumber><ETag>{}</ETag></Part>", etags[1], etags[0]), "InvalidPartOrder"),
        (format!("<Part><PartNumber>1</PartNumber><ETag>{}</ETag></Part><Part><PartNumber>2</PartNumber><ETag>{}</ETag></Part>", etags[0], etags[1]), "EntityTooSmall"),
    ] {
        let response = app.clone().oneshot(Request::builder().method("POST")
            .uri(format!("/selected/object?uploadId={upload_id}"))
            .body(Body::from(format!("<CompleteMultipartUpload>{parts}</CompleteMultipartUpload>"))).unwrap()).await.unwrap();
        assert_eq!(response.status(), 400);
        assert!(body_string(response.into_body()).await.contains(expected));
        assert!(state.metadata.get_multipart_upload(upload_id).unwrap().is_some());
    }
    let selected = format!("<CompleteMultipartUpload><Part><PartNumber>2</PartNumber><ETag>{}</ETag></Part></CompleteMultipartUpload>", etags[1]);
    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .method("POST")
                .uri(format!("/selected/object?uploadId={upload_id}"))
                .body(Body::from(selected))
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), 200);
    let response = app
        .clone()
        .oneshot(
            Request::builder()
                .uri("/selected/object")
                .body(Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(body_bytes(response.into_body()).await, b"selected");
    assert!(state.metadata.list_parts(upload_id).unwrap().is_empty());
}
