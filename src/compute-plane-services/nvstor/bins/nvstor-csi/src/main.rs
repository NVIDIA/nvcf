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

//! nvstor CSI Driver — Kubernetes Container Storage Interface.
//!
//! Real gRPC server on a Unix socket, implementing the CSI v1 spec.
//! Kubelet connects to this socket to provision, attach, and mount volumes.
//!
//! Architecture:
//!   Controller mode: CreateVolume/DeleteVolume (provisions NFS subdirs)
//!   Node mode: NodePublishVolume/NodeUnpublishVolume (mount -t nfs4)
//!   Both modes serve Identity RPCs.

use std::collections::HashMap;
use std::sync::Arc;

use clap::Parser;
use parking_lot::RwLock;
use tokio::net::UnixListener;
use tokio_stream::wrappers::UnixListenerStream;
use tonic::{Request, Response, Status};
use tracing::{info, warn};

// Generated CSI proto types.
pub mod csi {
    tonic::include_proto!("csi.v1");
}

use csi::controller_server::{Controller, ControllerServer};
use csi::identity_server::{Identity, IdentityServer};
use csi::node_server::{Node, NodeServer};

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

#[derive(Parser)]
#[command(name = "nvstor-csi", about = "nvstor CSI driver for Kubernetes")]
struct Cli {
    /// CSI endpoint (unix socket path, e.g. unix:///csi/csi.sock).
    #[arg(long, default_value = "unix:///csi/csi.sock")]
    endpoint: String,

    /// nvstor NFS server address (host or host:port).
    #[arg(long, default_value = "127.0.0.1:2049")]
    nvstor_server: String,

    /// Node name (for node plugin mode).
    #[arg(long, default_value = "")]
    node_name: String,

    /// Path to the CSI metadata RocksDB. Defaults to the system
    /// install location; override for sandboxed tests
    /// (csi-sanity, dev iteration) where /var/lib isn't writable.
    #[arg(long)]
    db_path: Option<std::path::PathBuf>,
}

// ---------------------------------------------------------------------------
// Volume registry
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
struct VolumeInfo {
    volume_id: String,
    capacity_bytes: i64,
    nfs_server: String,
    nfs_path: String,
}

/// Default path for CSI RocksDB state.
const CSI_DB_PATH: &str = "/var/lib/nvstor-csi/db";

/// Directory lifecycle is shared by the controller and its filesystem tests.
trait VolumeDirectories: Send + Sync {
    fn change(&self, volume: &VolumeInfo, create: bool) -> Result<(), String>;
}

fn volume_leaf(volume: &VolumeInfo) -> Result<&str, String> {
    let id = volume
        .volume_id
        .strip_prefix("nvstor-")
        .ok_or("invalid volume id")?;
    let parsed = uuid::Uuid::parse_str(id).map_err(|_| "invalid volume UUID")?;
    if parsed.to_string() != id || volume.nfs_path != format!("/{}", volume.volume_id) {
        return Err("volume path does not match its identity".into());
    }
    Ok(&volume.volume_id)
}

fn change_directory(
    root: &std::path::Path,
    volume: &VolumeInfo,
    create: bool,
) -> Result<(), String> {
    let path = root.join(volume_leaf(volume)?);
    if create {
        match std::fs::create_dir(&path) {
            Ok(()) => Ok(()),
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {
                let meta = std::fs::symlink_metadata(&path).map_err(|e| e.to_string())?;
                if meta.file_type().is_dir() {
                    Ok(())
                } else {
                    Err("volume path is not a directory".into())
                }
            }
            Err(e) => Err(e.to_string()),
        }
    } else {
        match std::fs::symlink_metadata(&path) {
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
            Err(e) => Err(e.to_string()),
            Ok(meta) if !meta.file_type().is_dir() => Err("volume path is not a directory".into()),
            Ok(_) => std::fs::remove_dir_all(path).map_err(|e| e.to_string()),
        }
    }
}

struct NfsDirectories;

impl VolumeDirectories for NfsDirectories {
    fn change(&self, volume: &VolumeInfo, create: bool) -> Result<(), String> {
        volume_leaf(volume)?;
        let (host, port) = match volume.nfs_server.rsplit_once(':') {
            Some((host, port)) if !port.is_empty() && port.bytes().all(|b| b.is_ascii_digit()) => {
                (host, port)
            }
            _ => (volume.nfs_server.as_str(), "2049"),
        };
        let port: u16 = port.parse().map_err(|_| "invalid NFS port")?;
        let source = format!("{host}:/");
        // Keep the path before mounting: TempDir's recursive Drop must never run on a live export.
        let mountpoint = tempfile::Builder::new()
            .prefix("nvstor-csi-")
            .tempdir()
            .map_err(|e| e.to_string())?
            .keep();
        let options = format!("nfsvers=4.2,soft,timeo=50,retrans=2,port={port}");
        let mounted = std::process::Command::new("mount")
            .args(["-t", "nfs4", "-o", &options, &source])
            .arg(&mountpoint)
            .output();
        match mounted {
            Ok(out) if out.status.success() => {}
            result => {
                let _ = std::fs::remove_dir(&mountpoint);
                return Err(match result {
                    Ok(out) => format!(
                        "NFS root mount failed: {}",
                        String::from_utf8_lossy(&out.stderr)
                    ),
                    Err(e) => format!("NFS mount exec: {e}"),
                });
            }
        }
        let result = change_directory(&mountpoint, volume, create);
        let unmounted = std::process::Command::new("umount")
            .arg(&mountpoint)
            .output()
            .map_err(|e| format!("NFS unmount exec: {e}"))?;
        if !unmounted.status.success() {
            return Err(format!(
                "NFS unmount failed at {}: {}",
                mountpoint.display(),
                String::from_utf8_lossy(&unmounted.stderr)
            ));
        }
        let _ = std::fs::remove_dir(&mountpoint);
        result
    }
}

#[cfg(test)]
struct LocalDirectories(std::path::PathBuf);
#[cfg(test)]
impl VolumeDirectories for LocalDirectories {
    fn change(&self, volume: &VolumeInfo, create: bool) -> Result<(), String> {
        change_directory(&self.0, volume, create)
    }
}

struct NvstorDriver {
    nvstor_server: String,
    node_name: String,
    /// RocksDB-backed metadata store for persistent volume state.
    metadata: Arc<nvstor_metadata::MetadataStore>,
    /// In-memory cache (loaded from RocksDB on startup).
    volumes: RwLock<HashMap<String, VolumeInfo>>,
    name_to_id: RwLock<HashMap<String, String>>,
    operations: tokio::sync::Mutex<()>,
    directories: Arc<dyn VolumeDirectories>,
}

impl NvstorDriver {
    fn new(nvstor_server: String, node_name: String) -> Self {
        Self::with_db_path(nvstor_server, node_name, std::path::Path::new(CSI_DB_PATH))
    }

    fn with_db_path(nvstor_server: String, node_name: String, db_path: &std::path::Path) -> Self {
        if let Some(parent) = db_path.parent() {
            let _ = std::fs::create_dir_all(parent);
        }
        let metadata = Arc::new(
            nvstor_metadata::MetadataStore::open(db_path)
                .expect("failed to open CSI metadata store"),
        );

        // Load persisted volumes from RocksDB.
        let mut volumes = HashMap::new();
        let mut name_to_id = HashMap::new();
        if let Ok(entries) = metadata.scan_inline_prefix("csi/vol/") {
            for (_key, bytes) in entries {
                if let Ok(info) = serde_json::from_slice::<VolumeInfo>(&bytes) {
                    volumes.insert(info.volume_id.clone(), info);
                }
            }
        }
        // Load name→id mappings.
        if let Ok(entries) = metadata.scan_inline_prefix("csi/name/") {
            for (key, bytes) in entries {
                let name = key.strip_prefix("csi/name/").unwrap_or(&key).to_string();
                if let Ok(vol_id) = String::from_utf8(bytes) {
                    name_to_id.insert(name, vol_id);
                }
            }
        }

        info!(
            volumes = volumes.len(),
            "CSI: loaded persisted volumes from RocksDB"
        );

        Self {
            nvstor_server,
            node_name,
            metadata,
            volumes: RwLock::new(volumes),
            name_to_id: RwLock::new(name_to_id),
            operations: tokio::sync::Mutex::new(()),
            directories: Arc::new(NfsDirectories),
        }
    }

    async fn change_directory(&self, info: &VolumeInfo, create: bool) -> Result<(), Status> {
        let directories = self.directories.clone();
        let info = info.clone();
        tokio::task::spawn_blocking(move || directories.change(&info, create))
            .await
            .map_err(|e| Status::internal(format!("directory worker: {e}")))?
            .map_err(|e| Status::unavailable(format!("NFS directory operation: {e}")))
    }
}

// ---------------------------------------------------------------------------
// CSI Identity Service
// ---------------------------------------------------------------------------

#[tonic::async_trait]
impl Identity for Arc<NvstorDriver> {
    async fn get_plugin_info(
        &self,
        _request: Request<csi::GetPluginInfoRequest>,
    ) -> Result<Response<csi::GetPluginInfoResponse>, Status> {
        Ok(Response::new(csi::GetPluginInfoResponse {
            name: "nvstor.storage.io".into(),
            vendor_version: env!("CARGO_PKG_VERSION").into(),
            manifest: HashMap::new(),
        }))
    }

    async fn get_plugin_capabilities(
        &self,
        _request: Request<csi::GetPluginCapabilitiesRequest>,
    ) -> Result<Response<csi::GetPluginCapabilitiesResponse>, Status> {
        Ok(Response::new(csi::GetPluginCapabilitiesResponse {
            capabilities: vec![csi::PluginCapability {
                r#type: Some(csi::plugin_capability::Type::Service(
                    csi::plugin_capability::Service {
                        r#type: csi::plugin_capability::service::Type::ControllerService as i32,
                    },
                )),
            }],
        }))
    }

    async fn probe(
        &self,
        _request: Request<csi::ProbeRequest>,
    ) -> Result<Response<csi::ProbeResponse>, Status> {
        Ok(Response::new(csi::ProbeResponse {
            ready: Some(true.into()),
        }))
    }
}

// ---------------------------------------------------------------------------
// CSI Controller Service
// ---------------------------------------------------------------------------

#[tonic::async_trait]
impl Controller for Arc<NvstorDriver> {
    async fn create_volume(
        &self,
        request: Request<csi::CreateVolumeRequest>,
    ) -> Result<Response<csi::CreateVolumeResponse>, Status> {
        let _operation = self.operations.lock().await;
        let req = request.into_inner();
        let name = &req.name;

        // CSI spec: name and volume_capabilities are required.
        // csi-sanity asserts the driver returns InvalidArgument
        // (not Internal) when either is missing.
        if name.is_empty() {
            return Err(Status::invalid_argument("name must be provided"));
        }
        if req.volume_capabilities.is_empty() {
            return Err(Status::invalid_argument(
                "volume_capabilities must be provided",
            ));
        }

        let requested_capacity = req
            .capacity_range
            .as_ref()
            .map(|r| r.required_bytes)
            .unwrap_or(10 * 1024 * 1024 * 1024); // Default 10 GB

        if let Some(range) = &req.capacity_range {
            if range.required_bytes < 0
                || range.limit_bytes < 0
                || (range.limit_bytes > 0 && range.required_bytes > range.limit_bytes)
            {
                return Err(Status::invalid_argument("invalid capacity range"));
            }
        }

        // Check for existing volume with same name (idempotent). CSI
        // spec: idempotent when *every* relevant field matches; if
        // capacity differs, return AlreadyExists with a clear msg
        // so the orchestrator knows it can't reuse this name for the
        // new request.
        if let Some(existing_id) = self.name_to_id.read().get(name) {
            if let Some(vol) = self.volumes.read().get(existing_id) {
                if req.capacity_range.is_some() && vol.capacity_bytes != requested_capacity {
                    return Err(Status::already_exists(format!(
                        "volume {name} exists with capacity {} but request asked for {requested_capacity}",
                        vol.capacity_bytes
                    )));
                }
                return Ok(Response::new(csi::CreateVolumeResponse {
                    volume: Some(csi::Volume {
                        volume_id: vol.volume_id.clone(),
                        capacity_bytes: 0,
                        volume_context: HashMap::from([
                            ("server".into(), vol.nfs_server.clone()),
                            ("share".into(), vol.nfs_path.clone()),
                        ]),
                        ..Default::default()
                    }),
                }));
            }
        }

        let capacity = requested_capacity;

        let volume_id = format!("nvstor-{}", uuid::Uuid::new_v4());
        let nfs_path = format!("/{volume_id}");

        let info = VolumeInfo {
            volume_id: volume_id.clone(),
            capacity_bytes: capacity,
            nfs_server: self.nvstor_server.clone(),
            nfs_path: nfs_path.clone(),
        };

        info!(volume_id = %volume_id, name = %name, capacity, "CSI: CreateVolume");

        self.change_directory(&info, true).await?;
        let bytes = serde_json::to_vec(&info).map_err(|e| Status::internal(e.to_string()))?;
        self.metadata
            .batch_inline(
                &[
                    (&format!("csi/vol/{volume_id}"), &bytes),
                    (&format!("csi/name/{name}"), volume_id.as_bytes()),
                ],
                &[],
            )
            .map_err(|e| Status::internal(format!("persist volume: {e}")))?;
        self.volumes.write().insert(volume_id.clone(), info);
        self.name_to_id
            .write()
            .insert(name.to_string(), volume_id.clone());

        // Pass through StorageClass.parameters["mountOptions"] so
        // operators can tune NFS client behavior per StorageClass
        // (e.g. `nconnect=8,actimeo=0`) without rebuilding the
        // driver. Empty string means "use NodePublishVolume defaults".
        let mount_options = req
            .parameters
            .get("mountOptions")
            .cloned()
            .unwrap_or_default();

        let mut volume_context = HashMap::from([
            ("server".into(), self.nvstor_server.clone()),
            ("share".into(), nfs_path),
        ]);
        if !mount_options.is_empty() {
            volume_context.insert("mountOptions".into(), mount_options);
        }

        Ok(Response::new(csi::CreateVolumeResponse {
            volume: Some(csi::Volume {
                volume_id,
                capacity_bytes: 0,
                volume_context,
                ..Default::default()
            }),
        }))
    }

    async fn delete_volume(
        &self,
        request: Request<csi::DeleteVolumeRequest>,
    ) -> Result<Response<csi::DeleteVolumeResponse>, Status> {
        let volume_id = request.into_inner().volume_id;
        if volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        let _operation = self.operations.lock().await;
        let info = self.volumes.read().get(&volume_id).cloned().or_else(|| {
            // PVs outlive controller pods. A lost local catalog must not turn
            // deletion into a false success while the NFS directory remains.
            let info = VolumeInfo {
                volume_id: volume_id.clone(),
                capacity_bytes: 0,
                nfs_server: self.nvstor_server.clone(),
                nfs_path: format!("/{volume_id}"),
            };
            volume_leaf(&info).ok().map(|_| info.clone())
        });
        if let Some(info) = info {
            // Preserve the durable catalog on cleanup failure so Kubernetes can retry.
            self.change_directory(&info, false).await?;
        }
        let mut deletes = vec![format!("csi/vol/{volume_id}")];
        for (key, value) in self
            .metadata
            .scan_inline_prefix("csi/name/")
            .map_err(|e| Status::internal(e.to_string()))?
        {
            if value == volume_id.as_bytes() {
                deletes.push(key);
            }
        }
        let delete_refs: Vec<_> = deletes.iter().map(String::as_str).collect();
        self.metadata
            .batch_inline(&[], &delete_refs)
            .map_err(|e| Status::internal(e.to_string()))?;
        self.volumes.write().remove(&volume_id);
        self.name_to_id.write().retain(|_, id| id != &volume_id);
        Ok(Response::new(csi::DeleteVolumeResponse {}))
    }

    async fn controller_publish_volume(
        &self,
        _request: Request<csi::ControllerPublishVolumeRequest>,
    ) -> Result<Response<csi::ControllerPublishVolumeResponse>, Status> {
        // NFS doesn't need controller-level attach — it's done at mount time.
        Ok(Response::new(csi::ControllerPublishVolumeResponse {
            publish_context: HashMap::new(),
        }))
    }

    async fn controller_unpublish_volume(
        &self,
        _request: Request<csi::ControllerUnpublishVolumeRequest>,
    ) -> Result<Response<csi::ControllerUnpublishVolumeResponse>, Status> {
        Ok(Response::new(csi::ControllerUnpublishVolumeResponse {}))
    }

    async fn validate_volume_capabilities(
        &self,
        request: Request<csi::ValidateVolumeCapabilitiesRequest>,
    ) -> Result<Response<csi::ValidateVolumeCapabilitiesResponse>, Status> {
        let req = request.into_inner();
        if req.volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        if req.volume_capabilities.is_empty() {
            return Err(Status::invalid_argument(
                "volume_capabilities must be provided",
            ));
        }
        let exists = self.volumes.read().contains_key(&req.volume_id);
        if !exists {
            return Err(Status::not_found(format!(
                "volume {} not found",
                req.volume_id
            )));
        }

        Ok(Response::new(csi::ValidateVolumeCapabilitiesResponse {
            confirmed: Some(csi::validate_volume_capabilities_response::Confirmed {
                volume_capabilities: req.volume_capabilities,
                volume_context: HashMap::new(),
                parameters: HashMap::new(),
                mutable_parameters: HashMap::new(),
            }),
            message: String::new(),
        }))
    }

    async fn list_volumes(
        &self,
        request: Request<csi::ListVolumesRequest>,
    ) -> Result<Response<csi::ListVolumesResponse>, Status> {
        // CSI spec: if starting_token is set but not parseable, return
        // Aborted. We don't currently paginate (everything fits in one
        // response), so the only valid token is the empty string.
        let req = request.into_inner();
        if !req.starting_token.is_empty() {
            return Err(Status::aborted(format!(
                "invalid starting_token: {}",
                req.starting_token
            )));
        }
        let volumes = self.volumes.read();
        let entries: Vec<csi::list_volumes_response::Entry> = volumes
            .values()
            .map(|v| csi::list_volumes_response::Entry {
                volume: Some(csi::Volume {
                    volume_id: v.volume_id.clone(),
                    capacity_bytes: 0,
                    volume_context: HashMap::from([
                        ("server".into(), v.nfs_server.clone()),
                        ("share".into(), v.nfs_path.clone()),
                    ]),
                    ..Default::default()
                }),
                status: None,
            })
            .collect();

        Ok(Response::new(csi::ListVolumesResponse {
            entries,
            next_token: String::new(),
        }))
    }

    async fn get_capacity(
        &self,
        _request: Request<csi::GetCapacityRequest>,
    ) -> Result<Response<csi::GetCapacityResponse>, Status> {
        Err(Status::unimplemented(
            "NFS directory volumes have no reserved capacity or per-volume quota",
        ))
    }

    async fn controller_get_capabilities(
        &self,
        _request: Request<csi::ControllerGetCapabilitiesRequest>,
    ) -> Result<Response<csi::ControllerGetCapabilitiesResponse>, Status> {
        use csi::controller_service_capability::rpc::Type;
        let caps = vec![Type::CreateDeleteVolume, Type::ListVolumes];
        Ok(Response::new(csi::ControllerGetCapabilitiesResponse {
            capabilities: caps
                .into_iter()
                .map(|t| csi::ControllerServiceCapability {
                    r#type: Some(csi::controller_service_capability::Type::Rpc(
                        csi::controller_service_capability::Rpc { r#type: t as i32 },
                    )),
                })
                .collect(),
        }))
    }

    async fn create_snapshot(
        &self,
        _request: Request<csi::CreateSnapshotRequest>,
    ) -> Result<Response<csi::CreateSnapshotResponse>, Status> {
        Err(Status::unimplemented("snapshots not yet supported"))
    }

    async fn delete_snapshot(
        &self,
        _request: Request<csi::DeleteSnapshotRequest>,
    ) -> Result<Response<csi::DeleteSnapshotResponse>, Status> {
        Err(Status::unimplemented("snapshots not yet supported"))
    }

    async fn list_snapshots(
        &self,
        _request: Request<csi::ListSnapshotsRequest>,
    ) -> Result<Response<csi::ListSnapshotsResponse>, Status> {
        Ok(Response::new(csi::ListSnapshotsResponse {
            entries: vec![],
            next_token: String::new(),
        }))
    }

    async fn controller_expand_volume(
        &self,
        request: Request<csi::ControllerExpandVolumeRequest>,
    ) -> Result<Response<csi::ControllerExpandVolumeResponse>, Status> {
        let req = request.into_inner();
        if req.volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        if req.capacity_range.is_none() {
            return Err(Status::invalid_argument("capacity_range must be provided"));
        }
        Err(Status::unimplemented(
            "NFS directory volumes have no enforceable size to expand",
        ))
    }

    async fn controller_get_volume(
        &self,
        request: Request<csi::ControllerGetVolumeRequest>,
    ) -> Result<Response<csi::ControllerGetVolumeResponse>, Status> {
        let vol_id = &request.into_inner().volume_id;
        if vol_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        let volumes = self.volumes.read();
        match volumes.get(vol_id.as_str()) {
            Some(v) => Ok(Response::new(csi::ControllerGetVolumeResponse {
                volume: Some(csi::Volume {
                    volume_id: v.volume_id.clone(),
                    capacity_bytes: 0,
                    ..Default::default()
                }),
                status: Some(csi::controller_get_volume_response::VolumeStatus {
                    published_node_ids: vec![],
                    volume_condition: None,
                }),
            })),
            None => Err(Status::not_found("volume not found")),
        }
    }

    async fn controller_modify_volume(
        &self,
        _request: Request<csi::ControllerModifyVolumeRequest>,
    ) -> Result<Response<csi::ControllerModifyVolumeResponse>, Status> {
        Ok(Response::new(csi::ControllerModifyVolumeResponse {}))
    }
}

// ---------------------------------------------------------------------------
// CSI Node Service
// ---------------------------------------------------------------------------

#[tonic::async_trait]
impl Node for Arc<NvstorDriver> {
    async fn node_stage_volume(
        &self,
        request: Request<csi::NodeStageVolumeRequest>,
    ) -> Result<Response<csi::NodeStageVolumeResponse>, Status> {
        let req = request.into_inner();
        if req.volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        if req.staging_target_path.is_empty() {
            return Err(Status::invalid_argument(
                "staging_target_path must be provided",
            ));
        }
        if req.volume_capability.is_none() {
            return Err(Status::invalid_argument(
                "volume_capability must be provided",
            ));
        }
        // NFS doesn't need staging — mount happens at publish time.
        Ok(Response::new(csi::NodeStageVolumeResponse {}))
    }

    async fn node_unstage_volume(
        &self,
        request: Request<csi::NodeUnstageVolumeRequest>,
    ) -> Result<Response<csi::NodeUnstageVolumeResponse>, Status> {
        let req = request.into_inner();
        if req.volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        if req.staging_target_path.is_empty() {
            return Err(Status::invalid_argument(
                "staging_target_path must be provided",
            ));
        }
        Ok(Response::new(csi::NodeUnstageVolumeResponse {}))
    }

    async fn node_publish_volume(
        &self,
        request: Request<csi::NodePublishVolumeRequest>,
    ) -> Result<Response<csi::NodePublishVolumeResponse>, Status> {
        let req = request.into_inner();
        let target_path = &req.target_path;
        let volume_id = &req.volume_id;

        if volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        if target_path.is_empty() {
            return Err(Status::invalid_argument("target_path must be provided"));
        }
        if req.volume_capability.is_none() {
            return Err(Status::invalid_argument(
                "volume_capability must be provided",
            ));
        }

        let server = req
            .volume_context
            .get("server")
            .cloned()
            .unwrap_or_else(|| self.nvstor_server.clone());
        let share = req
            .volume_context
            .get("share")
            .cloned()
            .unwrap_or_else(|| format!("/{volume_id}"));

        // Create mount point.
        std::fs::create_dir_all(target_path)
            .map_err(|e| Status::internal(format!("mkdir {target_path}: {e}")))?;

        // Split off a non-default NFS port if the server address is
        // host:port — `mount` doesn't accept port in the source URL,
        // it must go in -o port=N. Lets us coexist with kernel nfsd on
        // 2049 (smoketest configs run our NFS on 12049).
        let (host, port_opt) = match server.rsplit_once(':') {
            Some((h, p)) if p.chars().all(|c| c.is_ascii_digit()) => {
                (h.to_string(), Some(p.to_string()))
            }
            _ => (server.clone(), None),
        };
        // Mount NFS v4.2. Defaults tuned for AI checkpoint workloads:
        //  * `nconnect=4`: kernel client opens 4 TCP connections to
        //    the server, so one mount is no longer limited to what a
        //    single TCP stream carries. Overridable via the
        //    `mountOptions` key in StorageClass.parameters.
        //  * `hard,intr`: never give up on the server, but let
        //    SIGINT escape.
        //  * `rsize=wsize=1 MiB`: max RPC payload per request.
        let source = format!("{host}:{share}");
        let mut opts = String::from("nfsvers=4.2,hard,intr,rsize=1048576,wsize=1048576,nconnect=4");
        if let Some(p) = port_opt {
            opts.push_str(&format!(",port={p}"));
        }
        // Operator-supplied mountOptions (from StorageClass.parameters)
        // are appended verbatim. Last-wins: a later `nconnect=8` here
        // overrides the default 4 in the kernel's mount-options parse.
        if let Some(extra) = req.volume_context.get("mountOptions") {
            if !extra.is_empty() {
                opts.push(',');
                opts.push_str(extra);
            }
        }

        info!(volume_id, source = %source, target = %target_path, "CSI: NodePublishVolume");

        // nvstor-server's NFS VFS doesn't auto-create top-level
        // directories — CreateVolume records a `share` like
        // /<volume_id>, but kubelet/mount can't traverse into a path
        // that doesn't exist yet. Bootstrap by mounting the root one
        // level up, mkdir-ing the leaf, unmounting, then mounting
        // the per-volume share. The transient root mount is short
        // (a few ms) and uses a private mount namespace via -O.
        if share != "/" {
            let parent = match share.rfind('/') {
                Some(0) => "/".to_string(),
                Some(i) => share[..i].to_string(),
                None => "/".to_string(),
            };
            let leaf = match share.rfind('/') {
                Some(i) => share[i + 1..].to_string(),
                None => share.clone(),
            };
            if !leaf.is_empty() {
                let bootstrap_src = format!("{host}:{parent}");
                let bootstrap_dir = format!("{target_path}.bootstrap");
                let _ = std::fs::create_dir_all(&bootstrap_dir);
                let m = std::process::Command::new("mount")
                    .args(["-t", "nfs4", "-o", &opts, &bootstrap_src, &bootstrap_dir])
                    .output();
                if let Ok(out) = m {
                    if out.status.success() {
                        let _ = std::fs::create_dir_all(format!("{bootstrap_dir}/{leaf}"));
                        let _ = std::process::Command::new("umount")
                            .arg(&bootstrap_dir)
                            .output();
                    } else {
                        warn!(
                            stderr = %String::from_utf8_lossy(&out.stderr),
                            "CSI bootstrap mount failed; trying NodePublish anyway"
                        );
                    }
                }
                let _ = std::fs::remove_dir(&bootstrap_dir);
            }
        }

        let output = std::process::Command::new("mount")
            .args(["-t", "nfs4", "-o", &opts, &source, target_path])
            .output()
            .map_err(|e| Status::internal(format!("mount exec: {e}")))?;

        if !output.status.success() {
            let stderr = String::from_utf8_lossy(&output.stderr);
            return Err(Status::internal(format!("mount failed: {stderr}")));
        }

        info!(volume_id, target = %target_path, "CSI: volume mounted");
        Ok(Response::new(csi::NodePublishVolumeResponse {}))
    }

    async fn node_unpublish_volume(
        &self,
        request: Request<csi::NodeUnpublishVolumeRequest>,
    ) -> Result<Response<csi::NodeUnpublishVolumeResponse>, Status> {
        let req = request.into_inner();
        let target_path = &req.target_path;

        if req.volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        if target_path.is_empty() {
            return Err(Status::invalid_argument("target_path must be provided"));
        }

        info!(volume_id = %req.volume_id, target = %target_path, "CSI: NodeUnpublishVolume");

        let output = std::process::Command::new("umount")
            .arg(target_path)
            .output()
            .map_err(|e| Status::internal(format!("umount exec: {e}")))?;

        if !output.status.success() {
            warn!(target = %target_path, "umount returned non-zero (may already be unmounted)");
        }

        let _ = std::fs::remove_dir(target_path);
        Ok(Response::new(csi::NodeUnpublishVolumeResponse {}))
    }

    async fn node_get_volume_stats(
        &self,
        request: Request<csi::NodeGetVolumeStatsRequest>,
    ) -> Result<Response<csi::NodeGetVolumeStatsResponse>, Status> {
        let req = request.into_inner();
        if req.volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        if req.volume_path.is_empty() {
            return Err(Status::invalid_argument("volume_path must be provided"));
        }
        Err(Status::unimplemented("volume stats not yet supported"))
    }

    async fn node_expand_volume(
        &self,
        request: Request<csi::NodeExpandVolumeRequest>,
    ) -> Result<Response<csi::NodeExpandVolumeResponse>, Status> {
        let req = request.into_inner();
        if req.volume_id.is_empty() {
            return Err(Status::invalid_argument("volume_id must be provided"));
        }
        if req.volume_path.is_empty() {
            return Err(Status::invalid_argument("volume_path must be provided"));
        }
        Err(Status::unimplemented(
            "NFS directory volumes do not support expansion",
        ))
    }

    async fn node_get_capabilities(
        &self,
        _request: Request<csi::NodeGetCapabilitiesRequest>,
    ) -> Result<Response<csi::NodeGetCapabilitiesResponse>, Status> {
        Ok(Response::new(csi::NodeGetCapabilitiesResponse {
            capabilities: vec![],
        }))
    }

    async fn node_get_info(
        &self,
        _request: Request<csi::NodeGetInfoRequest>,
    ) -> Result<Response<csi::NodeGetInfoResponse>, Status> {
        Ok(Response::new(csi::NodeGetInfoResponse {
            node_id: self.node_name.clone(),
            max_volumes_per_node: 256,
            accessible_topology: None,
        }))
    }
}

// ---------------------------------------------------------------------------
// Main — gRPC server on Unix socket
// ---------------------------------------------------------------------------

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()),
        )
        .init();

    let cli = Cli::parse();

    info!(
        driver = "nvstor.storage.io",
        version = env!("CARGO_PKG_VERSION"),
        server = %cli.nvstor_server,
        endpoint = %cli.endpoint,
        "nvstor-csi starting"
    );

    let driver = Arc::new(match cli.db_path.as_deref() {
        Some(p) => NvstorDriver::with_db_path(cli.nvstor_server, cli.node_name, p),
        None => NvstorDriver::new(cli.nvstor_server, cli.node_name),
    });

    // Parse socket path from endpoint URI.
    let socket_path = cli
        .endpoint
        .strip_prefix("unix://")
        .unwrap_or(&cli.endpoint);

    // Remove stale socket file.
    let _ = std::fs::remove_file(socket_path);

    // Create parent directory.
    if let Some(parent) = std::path::Path::new(socket_path).parent() {
        std::fs::create_dir_all(parent)?;
    }

    let uds = UnixListener::bind(socket_path)?;
    let uds_stream = UnixListenerStream::new(uds);

    info!(socket = %socket_path, "CSI gRPC server listening on Unix socket");

    tonic::transport::Server::builder()
        .add_service(IdentityServer::new(driver.clone()))
        .add_service(ControllerServer::new(driver.clone()))
        .add_service(NodeServer::new(driver.clone()))
        .serve_with_incoming(uds_stream)
        .await?;

    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    fn test_driver() -> (tempfile::TempDir, Arc<NvstorDriver>) {
        let tmp = tempfile::TempDir::new().expect("tempdir");
        let mut driver = NvstorDriver::with_db_path(
            "10.0.0.1:2049".into(),
            "test-node".into(),
            &tmp.path().join("csi-db"),
        );
        let root = tmp.path().join("export");
        std::fs::create_dir(&root).unwrap();
        driver.directories = Arc::new(LocalDirectories(root));
        let driver = Arc::new(driver);
        (tmp, driver) // TempDir must live as long as driver
    }

    #[tokio::test]
    async fn reclamation_removes_data_and_name_and_preserves_failed_cleanup() {
        let (tmp, driver) = test_driver();
        let request = csi::CreateVolumeRequest {
            name: "reclaim".into(),
            volume_capabilities: default_volume_caps(),
            ..Default::default()
        };
        let volume = Controller::create_volume(&driver, Request::new(request.clone()))
            .await
            .unwrap()
            .into_inner()
            .volume
            .unwrap();
        let dir = tmp.path().join("export").join(&volume.volume_id);
        std::fs::write(dir.join("checkpoint"), b"payload").unwrap();
        let deletion = csi::DeleteVolumeRequest {
            volume_id: volume.volume_id.clone(),
            ..Default::default()
        };
        Controller::delete_volume(&driver, Request::new(deletion.clone()))
            .await
            .unwrap();
        assert!(!dir.exists());
        assert!(driver
            .metadata
            .get_inline("csi/name/reclaim")
            .unwrap()
            .is_none());
        Controller::delete_volume(&driver, Request::new(deletion))
            .await
            .unwrap();
        let next = Controller::create_volume(&driver, Request::new(request))
            .await
            .unwrap()
            .into_inner()
            .volume
            .unwrap();
        assert_ne!(next.volume_id, volume.volume_id);
        let path = tmp.path().join("export").join(&next.volume_id);
        std::fs::remove_dir(&path).unwrap();
        std::fs::write(&path, b"not a directory").unwrap();
        assert!(Controller::delete_volume(
            &driver,
            Request::new(csi::DeleteVolumeRequest {
                volume_id: next.volume_id.clone(),
                ..Default::default()
            })
        )
        .await
        .is_err());
        assert!(driver.volumes.read().contains_key(&next.volume_id));
        assert!(driver
            .metadata
            .get_inline("csi/name/reclaim")
            .unwrap()
            .is_some());
    }

    #[tokio::test]
    async fn reclamation_recovers_volume_path_after_controller_catalog_loss() {
        let (tmp, original) = test_driver();
        let volume = Controller::create_volume(
            &original,
            Request::new(csi::CreateVolumeRequest {
                name: "old-controller".into(),
                volume_capabilities: default_volume_caps(),
                ..Default::default()
            }),
        )
        .await
        .unwrap()
        .into_inner()
        .volume
        .unwrap();
        let export = tmp.path().join("export");
        std::fs::write(
            export.join(&volume.volume_id).join("checkpoint"),
            b"retired",
        )
        .unwrap();
        let mut replacement = NvstorDriver::with_db_path(
            "10.0.0.1:2049".into(),
            "new-controller".into(),
            &tmp.path().join("new-db"),
        );
        replacement.directories = Arc::new(LocalDirectories(export.clone()));
        Controller::delete_volume(
            &Arc::new(replacement),
            Request::new(csi::DeleteVolumeRequest {
                volume_id: volume.volume_id.clone(),
                ..Default::default()
            }),
        )
        .await
        .unwrap();
        assert!(!export.join(volume.volume_id).exists());
    }

    #[tokio::test]
    async fn identity_get_plugin_info() {
        let (_tmp, driver) = test_driver();
        let resp = Identity::get_plugin_info(&driver, Request::new(csi::GetPluginInfoRequest {}))
            .await
            .unwrap();
        assert_eq!(resp.into_inner().name, "nvstor.storage.io");
    }

    #[tokio::test]
    async fn identity_probe_ready() {
        let (_tmp, driver) = test_driver();
        let resp = Identity::probe(&driver, Request::new(csi::ProbeRequest {}))
            .await
            .unwrap();
        assert_eq!(resp.into_inner().ready, Some(true.into()));
    }

    /// Build a valid `VolumeCapability` for tests. After the
    /// CSI-sanity input-validation tightening, `create_volume`
    /// requires `volume_capabilities` to be non-empty.
    fn default_volume_caps() -> Vec<csi::VolumeCapability> {
        vec![csi::VolumeCapability {
            access_mode: Some(csi::volume_capability::AccessMode {
                mode: csi::volume_capability::access_mode::Mode::SingleNodeWriter as i32,
            }),
            access_type: Some(csi::volume_capability::AccessType::Mount(
                csi::volume_capability::MountVolume {
                    fs_type: "nfs4".into(),
                    mount_flags: vec![],
                    volume_mount_group: String::new(),
                },
            )),
        }]
    }

    #[tokio::test]
    async fn identity_capabilities() {
        let (_tmp, driver) = test_driver();
        let resp = Identity::get_plugin_capabilities(
            &driver,
            Request::new(csi::GetPluginCapabilitiesRequest {}),
        )
        .await
        .unwrap();
        assert!(!resp.into_inner().capabilities.is_empty());
    }

    #[tokio::test]
    async fn controller_create_delete_volume() {
        let (_tmp, driver) = test_driver();

        let resp = Controller::create_volume(
            &driver,
            Request::new(csi::CreateVolumeRequest {
                name: "test-pvc".into(),
                capacity_range: Some(csi::CapacityRange {
                    required_bytes: 100 * 1024 * 1024 * 1024,
                    limit_bytes: 0,
                }),
                volume_capabilities: default_volume_caps(),
                ..Default::default()
            }),
        )
        .await
        .unwrap();

        let vol = resp.into_inner().volume.unwrap();
        assert!(vol.volume_id.starts_with("nvstor-"));
        assert_eq!(vol.capacity_bytes, 0);
        assert!(vol.volume_context.contains_key("server"));
        assert!(vol.volume_context.contains_key("share"));

        // Delete.
        Controller::delete_volume(
            &driver,
            Request::new(csi::DeleteVolumeRequest {
                volume_id: vol.volume_id.clone(),
                ..Default::default()
            }),
        )
        .await
        .unwrap();

        // Verify deleted.
        let list =
            Controller::list_volumes(&driver, Request::new(csi::ListVolumesRequest::default()))
                .await
                .unwrap();
        assert!(list.into_inner().entries.is_empty());
    }

    #[tokio::test]
    async fn controller_list_volumes() {
        let (_tmp, driver) = test_driver();

        // Create 3 volumes.
        for i in 0..3 {
            Controller::create_volume(
                &driver,
                Request::new(csi::CreateVolumeRequest {
                    name: format!("vol-{i}"),
                    volume_capabilities: default_volume_caps(),
                    ..Default::default()
                }),
            )
            .await
            .unwrap();
        }

        let list =
            Controller::list_volumes(&driver, Request::new(csi::ListVolumesRequest::default()))
                .await
                .unwrap();
        assert_eq!(list.into_inner().entries.len(), 3);
    }

    #[tokio::test]
    async fn controller_get_capabilities() {
        let (_tmp, driver) = test_driver();
        let resp = Controller::controller_get_capabilities(
            &driver,
            Request::new(csi::ControllerGetCapabilitiesRequest {}),
        )
        .await
        .unwrap();
        let caps = resp.into_inner().capabilities;
        assert_eq!(caps.len(), 2); // CREATE_DELETE and LIST only
    }

    #[tokio::test]
    async fn controller_expand_volume() {
        let (_tmp, driver) = test_driver();

        let resp = Controller::create_volume(
            &driver,
            Request::new(csi::CreateVolumeRequest {
                name: "expand-test".into(),
                volume_capabilities: default_volume_caps(),
                capacity_range: Some(csi::CapacityRange {
                    required_bytes: 10 * 1024 * 1024 * 1024,
                    limit_bytes: 0,
                }),
                ..Default::default()
            }),
        )
        .await
        .unwrap();
        let vol_id = resp.into_inner().volume.unwrap().volume_id;

        let resp = Controller::controller_expand_volume(
            &driver,
            Request::new(csi::ControllerExpandVolumeRequest {
                volume_id: vol_id,
                capacity_range: Some(csi::CapacityRange {
                    required_bytes: 50 * 1024 * 1024 * 1024,
                    limit_bytes: 0,
                }),
                ..Default::default()
            }),
        )
        .await
        .unwrap_err();
        assert_eq!(resp.code(), tonic::Code::Unimplemented);
    }

    #[tokio::test]
    async fn node_get_info() {
        let (_tmp, driver) = test_driver();
        let resp = Node::node_get_info(&driver, Request::new(csi::NodeGetInfoRequest {}))
            .await
            .unwrap();
        let info = resp.into_inner();
        assert_eq!(info.node_id, "test-node");
        assert_eq!(info.max_volumes_per_node, 256);
    }

    #[tokio::test]
    async fn validate_volume_capabilities() {
        let (_tmp, driver) = test_driver();

        let vol = Controller::create_volume(
            &driver,
            Request::new(csi::CreateVolumeRequest {
                name: "cap-test".into(),
                volume_capabilities: default_volume_caps(),
                ..Default::default()
            }),
        )
        .await
        .unwrap()
        .into_inner()
        .volume
        .unwrap();

        let resp = Controller::validate_volume_capabilities(
            &driver,
            Request::new(csi::ValidateVolumeCapabilitiesRequest {
                volume_id: vol.volume_id,
                volume_capabilities: vec![csi::VolumeCapability {
                    access_mode: Some(csi::volume_capability::AccessMode {
                        mode: csi::volume_capability::access_mode::Mode::MultiNodeMultiWriter
                            as i32,
                    }),
                    access_type: Some(csi::volume_capability::AccessType::Mount(
                        csi::volume_capability::MountVolume {
                            fs_type: "nfs4".into(),
                            mount_flags: vec![],
                            volume_mount_group: String::new(),
                        },
                    )),
                }],
                ..Default::default()
            }),
        )
        .await
        .unwrap();
        assert!(resp.into_inner().confirmed.is_some());
    }

    #[tokio::test]
    async fn idempotent_create_volume() {
        let (_tmp, driver) = test_driver();

        let resp1 = Controller::create_volume(
            &driver,
            Request::new(csi::CreateVolumeRequest {
                name: "idem-vol".into(),
                volume_capabilities: default_volume_caps(),
                ..Default::default()
            }),
        )
        .await
        .unwrap();
        let vol1 = resp1.into_inner().volume.unwrap();

        let resp2 = Controller::create_volume(
            &driver,
            Request::new(csi::CreateVolumeRequest {
                name: "idem-vol".into(),
                volume_capabilities: default_volume_caps(),
                ..Default::default()
            }),
        )
        .await
        .unwrap();
        let vol2 = resp2.into_inner().volume.unwrap();

        // Same volume ID returned (idempotent).
        assert_eq!(vol1.volume_id, vol2.volume_id);
    }

    /// End-to-end test: spin up the real `tonic` gRPC server on a
    /// unix socket and drive it through the **generated client** that
    /// kubelet itself uses against the CSI proto. The pre-existing
    /// tests bypass `tonic` and call the impls directly, so they
    /// catch logic bugs but miss the entire serve-over-socket path —
    /// exactly the nvstor-nfs-export class of "first test failed"
    /// regression the corrections log warns against.
    #[tokio::test]
    async fn end_to_end_over_unix_socket() {
        use std::time::Duration;
        use tokio::io::AsyncWriteExt;
        use tokio::net::UnixStream;
        use tokio_stream::wrappers::UnixListenerStream;
        use tonic::transport::{Endpoint, Uri};
        use tower::service_fn;

        // Bring up the gRPC server in-process on a tempfile socket.
        let (_tmp, driver) = test_driver();
        let socket_dir = tempfile::TempDir::new().expect("socket dir");
        let socket_path = socket_dir.path().join("csi.sock");
        let socket_clone = socket_path.clone();

        let uds = UnixListener::bind(&socket_path).expect("bind uds");
        let server_handle = tokio::spawn(async move {
            tonic::transport::Server::builder()
                .add_service(IdentityServer::new(driver.clone()))
                .add_service(ControllerServer::new(driver.clone()))
                .add_service(NodeServer::new(driver.clone()))
                .serve_with_incoming(UnixListenerStream::new(uds))
                .await
                .expect("server failed");
        });

        // Wait briefly for the listener to be ready.
        for _ in 0..20 {
            if UnixStream::connect(&socket_path).await.is_ok() {
                break;
            }
            tokio::time::sleep(Duration::from_millis(50)).await;
        }

        // Build a tonic channel that dials the unix socket. tonic
        // doesn't speak `unix://` URIs natively; the standard
        // pattern is a custom connector via `Endpoint::connect_with_connector`.
        let channel = Endpoint::try_from("http://[::1]:50051")
            .expect("dummy endpoint")
            .connect_with_connector(service_fn(move |_: Uri| {
                let p = socket_clone.clone();
                async move {
                    Ok::<_, std::io::Error>(hyper_util::rt::TokioIo::new(
                        UnixStream::connect(p).await?,
                    ))
                }
            }))
            .await
            .expect("client channel");

        // 1. Identity probe + GetPluginInfo — the very first thing
        //    kubelet does after starting the driver. If this fails,
        //    the driver is dead-on-arrival in a real cluster.
        let mut identity = csi::identity_client::IdentityClient::new(channel.clone());
        let info = identity
            .get_plugin_info(csi::GetPluginInfoRequest {})
            .await
            .expect("get_plugin_info")
            .into_inner();
        assert_eq!(info.name, "nvstor.storage.io");
        assert!(!info.vendor_version.is_empty());

        let probe = identity
            .probe(csi::ProbeRequest {})
            .await
            .expect("probe")
            .into_inner();
        assert_eq!(probe.ready, Some(true.into()));

        // 2. Controller: create_volume → delete_volume.
        //    This is what `kubectl apply -f pvc.yaml` triggers.
        let mut controller = csi::controller_client::ControllerClient::new(channel.clone());
        let caps = controller
            .controller_get_capabilities(csi::ControllerGetCapabilitiesRequest {})
            .await
            .expect("controller_get_capabilities")
            .into_inner();
        assert!(
            !caps.capabilities.is_empty(),
            "driver must advertise at least CREATE_DELETE_VOLUME"
        );

        let create = controller
            .create_volume(csi::CreateVolumeRequest {
                name: "e2e-test-vol".into(),
                volume_capabilities: default_volume_caps(),
                ..Default::default()
            })
            .await
            .expect("create_volume")
            .into_inner();
        let vol = create.volume.expect("CreateVolumeResponse.volume present");
        assert!(!vol.volume_id.is_empty(), "CSI volume_id must be set");
        assert_eq!(vol.capacity_bytes, 0, "NFS share capacity is unknown");

        // 3. Node: get_info (cheap, no actual mount syscalls).
        let mut node = csi::node_client::NodeClient::new(channel.clone());
        let node_info = node
            .node_get_info(csi::NodeGetInfoRequest {})
            .await
            .expect("node_get_info")
            .into_inner();
        assert_eq!(node_info.node_id, "test-node");

        // 4. Idempotent delete.
        controller
            .delete_volume(csi::DeleteVolumeRequest {
                volume_id: vol.volume_id.clone(),
                ..Default::default()
            })
            .await
            .expect("delete_volume");
        // Re-delete is a no-op per CSI spec.
        controller
            .delete_volume(csi::DeleteVolumeRequest {
                volume_id: vol.volume_id,
                ..Default::default()
            })
            .await
            .expect("delete_volume (idempotent)");

        // Clean up the server task.
        server_handle.abort();
        // Touch the socket to ensure it's still openable (catches early
        // server crash). Best-effort write of a single byte then close.
        if let Ok(mut s) = UnixStream::connect(&socket_path).await {
            let _ = s.write_all(b"\0").await;
        }
    }
}
