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

//! S3 HTTP API — router, handlers, multipart, XML.

pub mod handlers;
mod upload;
pub mod xml;
// Note: multipart upload logic is in handlers.rs using MetadataStore directly.
// The old multipart.rs had an in-memory-only registry — removed.

use std::sync::Arc;

use axum::routing::{delete, get, head, post, put};
use axum::Router;

use nvstor_metadata::MetadataStore;
use nvstor_storage::manager::ChunkManager;

/// Shared application state for all S3 handlers.
///
/// All state is persistent — MetadataStore backs buckets, multipart uploads,
/// and parts to RocksDB. ChunkManager backs object/part data to disk.
/// No in-memory-only state. Server restart preserves everything.
///
/// When `raft` is Some, metadata writes go through Raft consensus before
/// being applied. When None (single-node), writes go directly to storage.
#[derive(Clone)]
pub struct AppState {
    pub manager: Arc<ChunkManager>,
    pub metadata: Arc<MetadataStore>,
    pub quota: Arc<crate::quota::QuotaManager>,
    /// Optional Raft node for replicated writes.
    /// When present, bucket/object mutations are proposed through Raft.
    pub raft: Option<Arc<nvstor_raft::node::RaftNode>>,
    /// Optional IAM store for policy-based authorization.
    /// When None, authorization is skipped (anonymous mode).
    pub iam: Option<Arc<nvstor_iam::store::IamStore>>,
    /// Optional KMS manager for server-side encryption.
    /// When None, SSE headers are ignored (no encryption).
    pub kms: Option<Arc<nvstor_kms::manager::KmsManager>>,
    /// Optional distributed manager for cross-node replication.
    /// When Some, writes are replicated to other nodes in the cluster.
    pub distributed: Option<Arc<nvstor_placement::distributed::DistributedManager>>,
}

/// Rewrites a virtual-host-style request into the path-style form the router
/// understands.
///
/// A virtual-host request carries the bucket in `Host` and the object key
/// alone in the path: `GET /my-key` with `Host: my-bucket.s3.example.com`.
/// The router is path-style by construction, so without this the key is
/// parsed as a *bucket* name — `GET /my-key` becomes a bucket listing, and
/// `PUT /my-key` creates a junk bucket, both returning 200. The client
/// cannot detect either, which is why this matters more than a spec gap:
/// it is a silent wrong answer, not an outage.
///
/// Detection requires knowing the service's own domain. Guessing is not
/// safe — `Host: foo.example.com` is a bucket `foo` only if we are serving
/// `example.com` — so virtual-host support is enabled by configuring
/// `[network] s3_domain`. With it unset, behaviour is unchanged (path-style
/// only), which keeps existing deployments working.
///
/// AWS SDKs default to virtual-host addressing, so any deployment with
/// SDK clients wants this set.
#[derive(Clone)]
pub struct VirtualHostConfig {
    /// Bare service domain, lowercased, e.g. "s3.example.com" or
    /// "127.0.0.1:9000". `None` disables virtual-host detection.
    domain: Option<String>,
}

impl VirtualHostConfig {
    pub fn new(domain: Option<String>) -> Self {
        Self {
            domain: domain.map(|d| d.trim().trim_end_matches('.').to_ascii_lowercase()),
        }
    }

    /// Extract the bucket from a `Host` value, if it is virtual-host style.
    ///
    /// Returns `None` when the host is the bare domain (path-style), when no
    /// domain is configured, or when the prefix isn't a plausible bucket.
    pub fn bucket_from_host(&self, host: &str) -> Option<String> {
        let domain = self.domain.as_deref()?;
        // Strip any port from the host before comparing, but compare against
        // the domain both with and without its own port so operators can
        // write either form in config.
        let host = host.trim().trim_end_matches('.').to_ascii_lowercase();
        let domain_no_port = domain.split(':').next().unwrap_or(domain);

        for suffix in [domain, domain_no_port] {
            if host == suffix {
                return None; // bare domain → path-style
            }
            if let Some(prefix) = host.strip_suffix(suffix) {
                if let Some(bucket) = prefix.strip_suffix('.') {
                    if !bucket.is_empty() && !bucket.contains('/') {
                        return Some(bucket.to_string());
                    }
                }
            }
            // Host may carry a port the configured domain does not.
            let host_no_port = host.split(':').next().unwrap_or(&host);
            if host_no_port == suffix {
                return None;
            }
            if let Some(prefix) = host_no_port.strip_suffix(suffix) {
                if let Some(bucket) = prefix.strip_suffix('.') {
                    if !bucket.is_empty() && !bucket.contains('/') {
                        return Some(bucket.to_string());
                    }
                }
            }
        }
        None
    }
}

/// Axum middleware applying [`VirtualHostConfig`].
///
/// Rewrites the URI in place — `/key` → `/bucket/key`, `/` → `/bucket` —
/// so every existing handler and the leader-redirect logic keep working
/// unchanged. `OriginalUri` still reports what the client sent, which is
/// what the 307 leader redirect must echo back.
pub async fn virtual_host_layer(
    axum::extract::State(cfg): axum::extract::State<VirtualHostConfig>,
    mut request: axum::extract::Request,
    next: axum::middleware::Next,
) -> axum::response::Response {
    let host = request
        .headers()
        .get(axum::http::header::HOST)
        .and_then(|h| h.to_str().ok())
        .map(str::to_string);

    if let Some(host) = host {
        if let Some(bucket) = cfg.bucket_from_host(&host) {
            let path = request.uri().path();
            let query = request
                .uri()
                .query()
                .map(|q| format!("?{q}"))
                .unwrap_or_default();
            let key = path.trim_start_matches('/');
            let new_path = if key.is_empty() {
                format!("/{bucket}")
            } else {
                format!("/{bucket}/{key}")
            };
            if let Ok(uri) = format!("{new_path}{query}").parse::<axum::http::Uri>() {
                *request.uri_mut() = uri;
            }
        }
    }

    next.run(request).await
}

/// Build the S3 router with virtual-host addressing applied.
///
/// The rewrite MUST happen before route matching, and a middleware added
/// with `Router::layer` does not: axum resolves the route first and only
/// then runs layered middleware, so mutating the URI there has no effect on
/// which handler was chosen. Wrapping the real router as the fallback
/// service of an outer router fixes the ordering — the outer router matches
/// trivially, the layer rewrites the URI, and the inner router then matches
/// on the rewritten path.
pub fn router_with_virtual_host(state: AppState, vhost: VirtualHostConfig) -> Router {
    Router::new()
        .fallback_service(router(state))
        .layer(axum::middleware::from_fn_with_state(
            vhost,
            virtual_host_layer,
        ))
}

/// Build the S3 router with all routes.
pub fn router(state: AppState) -> Router {
    Router::new()
        // Service-level: list buckets
        .route("/", get(handlers::list_buckets))
        // Bucket-level
        .route("/{bucket}", head(handlers::head_bucket))
        .route("/{bucket}", get(handlers::get_bucket))
        .route("/{bucket}", put(handlers::create_bucket))
        .route("/{bucket}", delete(handlers::delete_bucket))
        // Batch delete
        .route("/{bucket}", post(handlers::post_bucket))
        // Object-level
        .route("/{bucket}/{*key}", head(handlers::head_object))
        .route("/{bucket}/{*key}", get(handlers::get_object))
        .route("/{bucket}/{*key}", put(handlers::put_object))
        .route("/{bucket}/{*key}", delete(handlers::delete_object))
        // Multipart: initiate (POST with ?uploads), upload part (PUT with ?partNumber&uploadId),
        // complete (POST with ?uploadId), abort (DELETE with ?uploadId)
        // These are dispatched from the same routes via query params in the handlers.
        .route("/{bucket}/{*key}", post(handlers::post_object))
        .with_state(state)
}

#[cfg(test)]
mod vhost_tests {
    use super::*;

    #[test]
    fn extracts_bucket_from_virtual_host() {
        let cfg = VirtualHostConfig::new(Some("127.0.0.1:9000".into()));
        assert_eq!(
            cfg.bucket_from_host("testbucket.127.0.0.1:9000"),
            Some("testbucket".to_string())
        );
        // bare domain stays path-style
        assert_eq!(cfg.bucket_from_host("127.0.0.1:9000"), None);
    }

    #[test]
    fn disabled_without_domain() {
        // Unset domain must not guess: `foo.example.com` is only bucket
        // `foo` if we are actually serving `example.com`.
        let cfg = VirtualHostConfig::new(None);
        assert_eq!(cfg.bucket_from_host("testbucket.127.0.0.1:9000"), None);
        assert_eq!(cfg.bucket_from_host("anything.at.all"), None);
    }

    #[test]
    fn handles_dns_domain_and_port_variants() {
        let cfg = VirtualHostConfig::new(Some("s3.example.com".into()));
        assert_eq!(
            cfg.bucket_from_host("my-bucket.s3.example.com"),
            Some("my-bucket".to_string())
        );
        // Client may include a port the configured domain omits.
        assert_eq!(
            cfg.bucket_from_host("my-bucket.s3.example.com:9000"),
            Some("my-bucket".to_string())
        );
        assert_eq!(cfg.bucket_from_host("s3.example.com"), None);
        // Case-insensitive per RFC 4343.
        assert_eq!(
            cfg.bucket_from_host("My-Bucket.S3.Example.COM"),
            Some("my-bucket".to_string())
        );
        // A host that merely ends in similar text but isn't a subdomain.
        assert_eq!(cfg.bucket_from_host("nots3.example.com"), None);
    }

    #[test]
    fn dotted_bucket_prefix_is_taken_whole() {
        // A bucket name may itself contain dots; everything left of the
        // domain is the bucket.
        let cfg = VirtualHostConfig::new(Some("s3.example.com".into()));
        assert_eq!(
            cfg.bucket_from_host("my.dotted.bucket.s3.example.com"),
            Some("my.dotted.bucket".to_string())
        );
    }
}
