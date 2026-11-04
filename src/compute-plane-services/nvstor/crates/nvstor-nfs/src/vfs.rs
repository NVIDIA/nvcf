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

//! Virtual Filesystem layer — maps NFS/POSIX operations to ChunkManager.
//!
//! This is the translation layer between POSIX file semantics and nvstor's
//! object-storage engine. In-memory inode table serves as hot cache, backed
//! by RocksDB (via MetadataStore inline CF) for crash-safe persistence.
//!
//! On startup, all inodes are loaded from RocksDB into memory.
//! Every mutation writes to both memory and RocksDB atomically.
//!
//! Design follows the C reference `nvio/vfs.h` / `vfs.c`.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use parking_lot::{Mutex, RwLock};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use tracing::{debug, warn};

use nvstor_core::error::{NvstorError, Result};
use nvstor_metadata::MetadataStore;
use nvstor_storage::manager::ChunkManager;
use nvstor_storage::pm::PmStore;
use nvstor_storage::SyncMode;
use uuid::Uuid;

/// Namespace UUID for deriving per-inode PmStore volume IDs.
///
/// Each NFS file inode maps deterministically to one
/// PmStore volume via `Uuid::new_v5(NVSTOR_NFS_PM_NS, ino_bytes)`. The
/// namespace is a fixed 16-byte namespace constant; an on-disk identity,
/// so it never changes: changing it would orphan the content of every
/// existing file.
const NVSTOR_NFS_PM_NS: Uuid = Uuid::from_u128(0x6265_6472_6f63_6b2d_6e66_732d_706d_2d21);

/// Per-volume reservation when a PmStore-backed file is first written.
/// Sparse files cost ~no disk; the cap caps unbounded growth in case a
/// rogue write tries to extend to absurd offsets. ext4 max file size is
/// 16 TiB; we sit comfortably under that.
const PM_VOLUME_CAP: u64 = 1 << 40; // 1 TiB

/// Map an `io::Error` from the PmStore layer to a `NvstorError`.
fn io_to_nvstor(e: std::io::Error) -> NvstorError {
    use std::io::ErrorKind;
    match e.kind() {
        ErrorKind::NotFound => NvstorError::NotFound,
        ErrorKind::AlreadyExists => NvstorError::Exists,
        ErrorKind::PermissionDenied => NvstorError::PermissionDenied,
        _ => NvstorError::Io(e),
    }
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/// Root inode number (always 1, matching C reference NVIO_VFS_ROOT_INO).
const ROOT_INO: u64 = 1;

/// Maximum filename length.
const NAME_MAX: usize = 255;

/// NFS file handle size in bytes (SHA-256 digest).
const FH_SIZE: usize = 32;

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/// File type — regular file, directory, or symbolic link.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum FileType {
    Regular,
    Directory,
    Symlink,
}

/// The operation needs a directory: a symbolic link is
/// `NvstorError::Symlink` (NFS4ERR_SYMLINK), anything else `NotDir`.
fn require_dir(file_type: FileType) -> Result<()> {
    match file_type {
        FileType::Directory => Ok(()),
        FileType::Symlink => Err(NvstorError::Symlink),
        FileType::Regular => Err(NvstorError::NotDir),
    }
}

/// The operation needs a regular file: a directory is `IsDir`, a
/// symbolic link `Symlink` (READ, WRITE and COMMIT never follow it).
fn require_file(file_type: FileType) -> Result<()> {
    match file_type {
        FileType::Regular => Ok(()),
        FileType::Directory => Err(NvstorError::IsDir),
        FileType::Symlink => Err(NvstorError::Symlink),
    }
}

/// An 8-byte READDIR cookie verifier from `parts`: never all zero, which
/// clients send when they hold no verifier.
pub(crate) fn cookie_verifier(parts: &[&[u8]]) -> [u8; 8] {
    let mut hasher = Sha256::new();
    for part in parts {
        hasher.update((part.len() as u64).to_le_bytes());
        hasher.update(part);
    }
    let digest = hasher.finalize();
    let mut verifier = [0u8; 8];
    verifier.copy_from_slice(&digest[..8]);
    if verifier == [0u8; 8] {
        verifier[7] = 1;
    }
    verifier
}

/// Inode attributes returned from VFS operations.
#[derive(Debug, Clone)]
pub struct InodeAttrs {
    pub ino: u64,
    pub size: u64,
    pub file_type: FileType,
    pub mode: u32,
    pub nlink: u32,
    pub uid: u32,
    pub gid: u32,
    pub atime: i64,
    pub mtime: i64,
    pub ctime: i64,
    pub generation: u32,
}

/// Lock type for byte-range locking.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum LockType {
    ReadLock,
    WriteLock,
}

/// A byte-range lock on a file.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LockRange {
    pub offset: u64,
    pub length: u64,
    pub lock_type: LockType,
    pub owner: Vec<u8>,
    pub clientid: u64,
}

/// Directory entry returned from readdir.
#[derive(Debug, Clone)]
pub struct DirEntry {
    pub ino: u64,
    pub name: String,
    pub file_type: FileType,
    pub cookie: u64,
}

/// Internal inode metadata. Serialized to RocksDB for persistence.
#[derive(Clone, Serialize, Deserialize)]
struct InodeEntry {
    ino: u64,
    parent_ino: u64,
    name: String,
    file_type: FileType,
    size: u64,
    mode: u32,
    uid: u32,
    gid: u32,
    atime: i64,
    mtime: i64,
    ctime: i64,
    nlink: u32,
    generation: u32,
    /// For regular files: the object key in ChunkManager.
    object_id: Option<String>,
    /// For symlinks: the target path.
    symlink_target: Option<String>,
}

impl InodeEntry {
    fn to_attrs(&self) -> InodeAttrs {
        InodeAttrs {
            ino: self.ino,
            size: self.size,
            file_type: self.file_type,
            mode: self.mode,
            nlink: self.nlink,
            uid: self.uid,
            gid: self.gid,
            atime: self.atime,
            mtime: self.mtime,
            ctime: self.ctime,
            generation: self.generation,
        }
    }
}

// ---------------------------------------------------------------------------
// Vfs
// ---------------------------------------------------------------------------

/// Virtual filesystem: in-memory inode table + RocksDB persistence.
///
/// The in-memory HashMaps are the hot read path. Every mutation writes
/// to both memory and RocksDB. On startup, inodes are loaded from RocksDB.
///
/// RocksDB key schema (in inline CF):
///   "nfs/i/{ino}" → InodeEntry (bincode)
///   "nfs/d/{parent_ino}/{name}" → child_ino (u64 le bytes)
///   "nfs/fh/{filehandle_hex}" → ino (u64 le bytes)
///   "nfs/counter/next_ino" → u64 le bytes
///   "nfs/counter/next_gen" → u64 le bytes
pub struct Vfs {
    mgr: Arc<ChunkManager>,
    /// Optional metadata store for persistence. None = in-memory only (tests).
    metadata: Option<Arc<MetadataStore>>,
    /// Optional position-mapped store for file content.
    ///
    /// When set, regular-file `read`/`write`/`setattr(size)`/`unlink`
    /// route through `pm_store` instead of `mgr`. Each inode owns one
    /// sparse-file volume keyed by `pm_volume_id(ino)`. Callers that
    /// don't want PM (legacy, tests) can construct a Vfs without it and
    /// stay on the chunk-manager path.
    pm_store: Option<Arc<dyn PmStore>>,
    /// Serialize size changes and writes to the same file. Readers pin its size.
    data_locks: [RwLock<()>; 1024],
    namespace_lock: Mutex<()>,
    /// Inode table: ino -> InodeEntry.
    inodes: RwLock<HashMap<u64, InodeEntry>>,
    /// Reverse map: (parent_ino, name) -> child_ino for directory lookup.
    dir_children: RwLock<HashMap<(u64, String), u64>>,
    /// Reverse filehandle map: filehandle bytes -> ino (O(1) lookup).
    fh_map: RwLock<HashMap<Vec<u8>, u64>>,
    /// Byte-range locks: ino -> Vec<LockRange>.
    locks: RwLock<HashMap<u64, Vec<LockRange>>>,
    /// Next inode number to allocate.
    next_ino: AtomicU64,
    /// Object key prefix for this export (e.g. "nfs/").
    root_prefix: String,
    /// Generation counter for NFS filehandle validation.
    next_generation: AtomicU64,
}

impl Vfs {
    /// Create a new VFS instance (in-memory only, for tests).
    pub fn new(mgr: Arc<ChunkManager>, root_prefix: &str) -> Self {
        Self::with_metadata_and_pm(mgr, None, None, root_prefix)
    }

    /// Create a VFS instance with RocksDB persistence.
    pub fn with_metadata(
        mgr: Arc<ChunkManager>,
        metadata: Option<Arc<MetadataStore>>,
        root_prefix: &str,
    ) -> Self {
        Self::with_metadata_and_pm(mgr, metadata, None, root_prefix)
    }

    /// Create a VFS instance with RocksDB persistence + a PmStore for
    /// regular-file content. When `pm_store` is `Some`, file I/O
    /// uses position-mapped sparse-file volumes (one per inode) instead
    /// of the chunk manager's CA path. Avoids the read-modify-write
    /// amplification on every NFS WRITE.
    pub fn with_metadata_and_pm(
        mgr: Arc<ChunkManager>,
        metadata: Option<Arc<MetadataStore>>,
        pm_store: Option<Arc<dyn PmStore>>,
        root_prefix: &str,
    ) -> Self {
        // Try to load persisted state from RocksDB.
        if let Some(ref meta) = metadata {
            if let Some(mut vfs) = Self::load_from_rocksdb(&mgr, meta, root_prefix) {
                vfs.pm_store = pm_store;
                return vfs;
            }
        }

        // Fresh VFS: create root inode.
        let now = now_secs();
        let root = InodeEntry {
            ino: ROOT_INO,
            parent_ino: ROOT_INO,
            name: String::new(),
            file_type: FileType::Directory,
            size: 0,
            mode: 0o755,
            uid: 0,
            gid: 0,
            atime: now,
            mtime: now,
            ctime: now,
            nlink: 2,
            generation: 1,
            object_id: None,
            symlink_target: None,
        };

        let fh = Self::make_fh(ROOT_INO, 1);
        let mut fh_map = HashMap::new();
        fh_map.insert(fh, ROOT_INO);

        let mut inodes = HashMap::new();
        inodes.insert(ROOT_INO, root);

        let vfs = Self {
            mgr,
            metadata: metadata.clone(),
            pm_store,
            inodes: RwLock::new(inodes),
            dir_children: RwLock::new(HashMap::new()),
            fh_map: RwLock::new(fh_map),
            locks: RwLock::new(HashMap::new()),
            data_locks: std::array::from_fn(|_| RwLock::new(())),
            namespace_lock: Mutex::new(()),
            next_ino: AtomicU64::new(ROOT_INO + 1),
            root_prefix: root_prefix.to_string(),
            next_generation: AtomicU64::new(2),
        };

        // Persist initial state.
        if metadata.is_some() {
            vfs.persist_counters();
            let inodes = vfs.inodes.read();
            if let Some(root) = inodes.get(&ROOT_INO) {
                vfs.persist_inode(root);
            }
        }

        vfs
    }

    /// Load VFS state from RocksDB. Returns None if no persisted state found.
    fn load_from_rocksdb(
        mgr: &Arc<ChunkManager>,
        metadata: &Arc<MetadataStore>,
        root_prefix: &str,
    ) -> Option<Self> {
        // Check if counters exist (indicator of persisted state).
        let next_ino_bytes = metadata.get_inline("nfs/counter/next_ino").ok()??;
        let next_gen_bytes = metadata.get_inline("nfs/counter/next_gen").ok()??;

        if next_ino_bytes.len() != 8 || next_gen_bytes.len() != 8 {
            return None;
        }

        let next_ino = u64::from_le_bytes(next_ino_bytes[..8].try_into().ok()?);
        let next_gen = u64::from_le_bytes(next_gen_bytes[..8].try_into().ok()?);

        // Load all inodes.
        let inode_entries = metadata.scan_inline_prefix("nfs/i/").ok()?;
        if inode_entries.is_empty() {
            return None;
        }

        let mut inodes = HashMap::new();
        let mut dir_children = HashMap::new();
        let mut fh_map = HashMap::new();

        for (_key, bytes) in &inode_entries {
            let entry: InodeEntry = match bincode::deserialize(bytes) {
                Ok(e) => e,
                Err(e) => {
                    warn!(error = %e, "failed to deserialize inode, skipping");
                    continue;
                }
            };

            let fh = Self::make_fh(entry.ino, entry.generation);
            fh_map.insert(fh, entry.ino);
            inodes.insert(entry.ino, entry);
        }

        // Load dir_children.
        let dirent_entries = metadata.scan_inline_prefix("nfs/d/").ok()?;
        for (key, bytes) in &dirent_entries {
            if bytes.len() != 8 {
                continue;
            }
            let child_ino = u64::from_le_bytes(bytes[..8].try_into().unwrap_or([0; 8]));
            // Parse key: "nfs/d/{parent_ino}/{name}"
            let suffix = key.strip_prefix("nfs/d/")?;
            let slash_pos = suffix.find('/')?;
            let parent_ino: u64 = suffix[..slash_pos].parse().ok()?;
            let name = suffix[slash_pos + 1..].to_string();
            dir_children.insert((parent_ino, name), child_ino);
        }

        debug!(
            inodes = inodes.len(),
            dirents = dir_children.len(),
            next_ino,
            "vfs: loaded from RocksDB"
        );

        Some(Self {
            mgr: mgr.clone(),
            metadata: Some(metadata.clone()),
            pm_store: None, // installed by `with_metadata_and_pm` after loading
            inodes: RwLock::new(inodes),
            dir_children: RwLock::new(dir_children),
            fh_map: RwLock::new(fh_map),
            locks: RwLock::new(HashMap::new()),
            data_locks: std::array::from_fn(|_| RwLock::new(())),
            namespace_lock: Mutex::new(()),
            next_ino: AtomicU64::new(next_ino),
            root_prefix: root_prefix.to_string(),
            next_generation: AtomicU64::new(next_gen),
        })
    }

    // -----------------------------------------------------------------
    // Position-mapped helpers
    // -----------------------------------------------------------------

    /// Deterministic PmStore volume id for an inode. Same `ino` always
    /// maps to the same UUID so we can persist `entry.size` without
    /// also persisting a separate volume mapping.
    fn pm_volume_id(ino: u64) -> Uuid {
        Uuid::new_v5(&NVSTOR_NFS_PM_NS, &ino.to_le_bytes())
    }

    /// Ensure a PM volume exists for `ino` and is at least `min_size`
    /// bytes long. Creates lazily on first use; grows via `set_size` if
    /// the desired write extends past the current logical end.
    fn pm_ensure_volume(&self, store: &Arc<dyn PmStore>, ino: u64, min_size: u64) -> Result<Uuid> {
        let id = Self::pm_volume_id(ino);
        match store.size(id) {
            Ok(cur) => {
                if cur < min_size {
                    let target = min_size.min(PM_VOLUME_CAP).max(cur);
                    store.set_size(id, target).map_err(io_to_nvstor)?;
                }
                Ok(id)
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                let initial = min_size.min(PM_VOLUME_CAP).max(0);
                store.create(id, initial).map_err(io_to_nvstor)?;
                Ok(id)
            }
            Err(e) => Err(io_to_nvstor(e)),
        }
    }

    // -----------------------------------------------------------------------
    // Inode allocation
    // -----------------------------------------------------------------------

    fn alloc_ino(&self) -> u64 {
        let ino = self.next_ino.fetch_add(1, Ordering::Relaxed);
        self.persist_counters();
        ino
    }

    fn alloc_generation(&self) -> u32 {
        let gen = self.next_generation.fetch_add(1, Ordering::Relaxed) as u32;
        self.persist_counters();
        gen
    }

    /// Build the ChunkManager object key for a file inode.
    fn object_key(&self, ino: u64) -> String {
        format!("{}{}", self.root_prefix, ino)
    }

    // -----------------------------------------------------------------------
    // Persistence helpers
    // -----------------------------------------------------------------------

    fn persist_inode(&self, entry: &InodeEntry) {
        if let Some(ref meta) = self.metadata {
            let key = format!("nfs/i/{}", entry.ino);
            match bincode::serialize(entry) {
                Ok(bytes) => {
                    if let Err(e) = meta.put_inline(&key, &bytes) {
                        warn!(ino = entry.ino, error = %e, "vfs: failed to persist inode");
                    }
                }
                Err(e) => warn!(ino = entry.ino, error = %e, "vfs: failed to serialize inode"),
            }
        }
    }

    /// Hot-path inode persist that skips the WAL fsync.
    ///
    /// NFS WRITE doesn't have to be durable per call — protocol
    /// durability is provided by NFS COMMIT, which the server
    /// already turns into an explicit `flush()` on the underlying
    /// PmStore + a subsequent metadata barrier. Per-WRITE fsync on
    /// the inode size made every WRITE wait for an fsync, which
    /// bounded throughput by fsync rate rather than by the data path.
    fn persist_inode_nosync(&self, entry: &InodeEntry) {
        if let Some(ref meta) = self.metadata {
            let key = format!("nfs/i/{}", entry.ino);
            match bincode::serialize(entry) {
                Ok(bytes) => {
                    if let Err(e) = meta.put_inline_nosync(&key, &bytes) {
                        warn!(ino = entry.ino, error = %e, "vfs: failed to persist inode");
                    }
                }
                Err(e) => warn!(ino = entry.ino, error = %e, "vfs: failed to serialize inode"),
            }
        }
    }

    /// Atomic batch: persist inode + dirent + parent inode update together.
    /// Ensures crash consistency — either all changes are visible or none.
    fn persist_create_atomic(
        &self,
        entry: &InodeEntry,
        parent_ino: u64,
        name: &str,
        parent: &InodeEntry,
        fh: &[u8],
    ) {
        if let Some(ref meta) = self.metadata {
            let inode_key = format!("nfs/i/{}", entry.ino);
            let dirent_key = format!("nfs/d/{parent_ino}/{name}");
            let parent_key = format!("nfs/i/{parent_ino}");
            let fh_key = format!("nfs/fh/{}", hex::encode(fh));

            let inode_val = match bincode::serialize(entry) {
                Ok(v) => v,
                Err(e) => {
                    warn!(error = %e, "serialize inode");
                    return;
                }
            };
            let parent_val = match bincode::serialize(parent) {
                Ok(v) => v,
                Err(e) => {
                    warn!(error = %e, "serialize parent");
                    return;
                }
            };
            let child_ino_bytes = entry.ino.to_le_bytes();
            let ino_bytes = entry.ino.to_le_bytes();

            if let Err(e) = meta.batch_inline(
                &[
                    (&inode_key, &inode_val),
                    (&dirent_key, &child_ino_bytes),
                    (&parent_key, &parent_val),
                    (&fh_key, &ino_bytes),
                ],
                &[],
            ) {
                warn!(error = %e, "vfs: atomic create persist failed");
            }
        }
    }

    /// Atomic batch: remove inode + dirent + update parent.
    #[allow(dead_code)]
    fn persist_remove_atomic(
        &self,
        child_ino: u64,
        parent_ino: u64,
        name: &str,
        parent: &InodeEntry,
        generation: u32,
    ) {
        if let Some(ref meta) = self.metadata {
            let inode_key = format!("nfs/i/{child_ino}");
            let dirent_key = format!("nfs/d/{parent_ino}/{name}");
            let parent_key = format!("nfs/i/{parent_ino}");
            let fh = Self::make_fh(child_ino, generation);
            let fh_key = format!("nfs/fh/{}", hex::encode(&fh));

            let parent_val = match bincode::serialize(parent) {
                Ok(v) => v,
                Err(e) => {
                    warn!(error = %e, "serialize parent");
                    return;
                }
            };

            if let Err(e) = meta.batch_inline(
                &[(&parent_key, &parent_val)],
                &[&inode_key, &dirent_key, &fh_key],
            ) {
                warn!(error = %e, "vfs: atomic remove persist failed");
            }
        }
    }

    fn delete_persisted_inode(&self, ino: u64) {
        if let Some(ref meta) = self.metadata {
            let key = format!("nfs/i/{ino}");
            let _ = meta.delete_inline(&key);
        }
    }

    fn persist_dirent(&self, parent_ino: u64, name: &str, child_ino: u64) {
        if let Some(ref meta) = self.metadata {
            let key = format!("nfs/d/{parent_ino}/{name}");
            let _ = meta.put_inline(&key, &child_ino.to_le_bytes());
        }
    }

    fn delete_persisted_dirent(&self, parent_ino: u64, name: &str) {
        if let Some(ref meta) = self.metadata {
            let key = format!("nfs/d/{parent_ino}/{name}");
            let _ = meta.delete_inline(&key);
        }
    }

    fn persist_counters(&self) {
        if let Some(ref meta) = self.metadata {
            let ino = self.next_ino.load(Ordering::Relaxed);
            let gen = self.next_generation.load(Ordering::Relaxed);
            let _ = meta.put_inline("nfs/counter/next_ino", &ino.to_le_bytes());
            let _ = meta.put_inline("nfs/counter/next_gen", &gen.to_le_bytes());
        }
    }

    // -----------------------------------------------------------------------
    // Filehandle encoding — O(1) via reverse map
    // -----------------------------------------------------------------------

    /// Create an opaque NFS filehandle from an inode number.
    /// The handle is SHA-256(ino || generation), 32 bytes.
    pub fn ino_to_filehandle(&self, ino: u64) -> Vec<u8> {
        let inodes = self.inodes.read();
        let gen = inodes.get(&ino).map_or(0u32, |e| e.generation);
        drop(inodes);
        Self::make_fh(ino, gen)
    }

    /// Resolve an opaque filehandle back to an inode number.
    /// O(1) lookup via reverse filehandle map.
    pub fn filehandle_to_ino(&self, fh: &[u8]) -> Result<u64> {
        if fh.len() != FH_SIZE {
            // Not a layout this server issues: NFS4ERR_BADHANDLE, not STALE.
            return Err(NvstorError::InvalidArgument("malformed filehandle".into()));
        }

        let fh_map = self.fh_map.read();
        fh_map.get(fh).copied().ok_or(NvstorError::StaleHandle)
    }

    /// Register a filehandle in the reverse map.
    fn register_fh(&self, ino: u64, generation: u32) {
        let fh = Self::make_fh(ino, generation);
        self.fh_map.write().insert(fh.clone(), ino);
        // Persist FH mapping.
        if let Some(ref meta) = self.metadata {
            let key = format!("nfs/fh/{}", hex::encode(&fh));
            let _ = meta.put_inline(&key, &ino.to_le_bytes());
        }
    }

    /// Unregister a filehandle from the reverse map.
    fn unregister_fh(&self, ino: u64, generation: u32) {
        let fh = Self::make_fh(ino, generation);
        self.fh_map.write().remove(&fh);
        if let Some(ref meta) = self.metadata {
            let key = format!("nfs/fh/{}", hex::encode(&fh));
            let _ = meta.delete_inline(&key);
        }
    }

    fn make_fh(ino: u64, generation: u32) -> Vec<u8> {
        let mut hasher = Sha256::new();
        hasher.update(ino.to_le_bytes());
        hasher.update(generation.to_le_bytes());
        hasher.finalize().to_vec()
    }

    // -----------------------------------------------------------------------
    // Directory operations
    // -----------------------------------------------------------------------

    /// Look up a name in a directory. Returns attributes of the found entry.
    pub fn lookup(&self, parent_ino: u64, name: &str) -> Result<InodeAttrs> {
        if name.len() > NAME_MAX {
            return Err(NvstorError::NameTooLong);
        }

        let inodes = self.inodes.read();

        // Verify parent is a directory.
        let parent = inodes.get(&parent_ino).ok_or(NvstorError::NotFound)?;
        require_dir(parent.file_type)?;

        // Handle "." and "..".
        if name == "." {
            return Ok(parent.to_attrs());
        }
        if name == ".." {
            let pp = inodes
                .get(&parent.parent_ino)
                .ok_or(NvstorError::NotFound)?;
            return Ok(pp.to_attrs());
        }

        drop(inodes);

        let children = self.dir_children.read();
        let child_ino = children
            .get(&(parent_ino, name.to_string()))
            .copied()
            .ok_or(NvstorError::NotFound)?;
        drop(children);

        let inodes = self.inodes.read();
        let child = inodes.get(&child_ino).ok_or(NvstorError::NotFound)?;
        Ok(child.to_attrs())
    }

    /// Get attributes of an inode.
    pub fn getattr(&self, ino: u64) -> Result<InodeAttrs> {
        let inodes = self.inodes.read();
        let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;
        Ok(entry.to_attrs())
    }

    /// Set attributes on an inode. Currently supports truncate (size).
    pub fn setattr(&self, ino: u64, size_opt: Option<u64>) -> Result<InodeAttrs> {
        let _guard = self.data_locks[ino as usize % self.data_locks.len()].write();
        if size_opt.is_some_and(|size| size > PM_VOLUME_CAP) && self.pm_store.is_some() {
            return Err(NvstorError::OutOfBounds);
        }
        let now = now_secs();
        let mut inodes = self.inodes.write();
        let entry = inodes.get_mut(&ino).ok_or(NvstorError::NotFound)?;

        if entry.file_type == FileType::Directory {
            return Err(NvstorError::IsDir);
        }

        if let Some(new_size) = size_opt {
            // PmStore truncate is a single ftruncate.
            // No read-rewrite cycle, no rehash, no chunk churn.
            if let Some(pm) = self.pm_store.as_ref() {
                let id = Self::pm_volume_id(ino);
                if new_size == 0 {
                    // Drop the volume entirely; recreated on next write.
                    match pm.delete(id) {
                        Ok(()) => {}
                        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
                        Err(error) => return Err(io_to_nvstor(error)),
                    }
                } else {
                    // Ensure-then-set covers grow + shrink uniformly.
                    if pm.size(id).is_err() {
                        // No volume yet — only meaningful for grow.
                        let initial = new_size.min(PM_VOLUME_CAP);
                        pm.create(id, initial).map_err(io_to_nvstor)?;
                    } else {
                        pm.set_size(id, new_size.min(PM_VOLUME_CAP))
                            .map_err(io_to_nvstor)?;
                    }
                }
                entry.size = new_size;
            } else if new_size == 0 {
                // Legacy CA-chunk path (no pm_store): delete backing object.
                if let Some(ref obj_id) = entry.object_id {
                    let _ = self.mgr.object_delete(obj_id);
                }
                entry.size = 0;
            } else if new_size < entry.size {
                if let Some(ref obj_id) = entry.object_id {
                    match self.mgr.object_read_range(obj_id, 0, new_size) {
                        Ok(data) => {
                            let _ = self.mgr.object_write(obj_id, &data, None);
                        }
                        Err(_) => {}
                    }
                }
                entry.size = new_size;
            } else if new_size > entry.size {
                let obj_key = entry
                    .object_id
                    .clone()
                    .unwrap_or_else(|| self.object_key(ino));
                let mut data = if entry.size > 0 {
                    self.mgr.object_read(&obj_key).unwrap_or_default()
                } else {
                    Vec::new()
                };
                data.resize(new_size as usize, 0);
                let _ = self.mgr.object_write(&obj_key, &data, None);
                entry.object_id = Some(obj_key);
                entry.size = new_size;
            }
            entry.mtime = now;
            entry.ctime = now;
        }

        let attrs = entry.to_attrs();
        self.persist_inode(entry);
        Ok(attrs)
    }

    /// Create a regular file in a directory.
    pub fn create_file(&self, parent_ino: u64, name: &str, mode: u32) -> Result<InodeAttrs> {
        let _namespace = self.namespace_lock.lock();
        if name.len() > NAME_MAX {
            return Err(NvstorError::NameTooLong);
        }

        // Check parent is a directory and name doesn't exist.
        {
            let inodes = self.inodes.read();
            let parent = inodes.get(&parent_ino).ok_or(NvstorError::NotFound)?;
            require_dir(parent.file_type)?;
        }
        {
            let children = self.dir_children.read();
            if children.contains_key(&(parent_ino, name.to_string())) {
                return Err(NvstorError::Exists);
            }
        }

        let now = now_secs();
        let ino = self.alloc_ino();
        let gen = self.alloc_generation();
        let obj_key = self.object_key(ino);

        let entry = InodeEntry {
            ino,
            parent_ino,
            name: name.to_string(),
            file_type: FileType::Regular,
            size: 0,
            mode,
            uid: 0,
            gid: 0,
            atime: now,
            mtime: now,
            ctime: now,
            nlink: 1,
            generation: gen,
            object_id: Some(obj_key),
            symlink_target: None,
        };

        let attrs = entry.to_attrs();
        let fh = Self::make_fh(ino, gen);

        // Update in-memory state.
        self.inodes.write().insert(ino, entry);
        self.dir_children
            .write()
            .insert((parent_ino, name.to_string()), ino);
        self.fh_map.write().insert(fh.clone(), ino);

        // Update parent mtime + atomic persist (inode + dirent + parent + fh).
        {
            let mut inodes = self.inodes.write();
            if let Some(parent) = inodes.get_mut(&parent_ino) {
                parent.mtime = now;
            }
            // Atomic batch persist.
            if let (Some(child), Some(par)) = (inodes.get(&ino), inodes.get(&parent_ino)) {
                self.persist_create_atomic(child, parent_ino, name, par, &fh);
            }
        }

        debug!(ino, name, "vfs: create_file");
        Ok(attrs)
    }

    /// Create a directory.
    pub fn mkdir(&self, parent_ino: u64, name: &str, mode: u32) -> Result<InodeAttrs> {
        let _namespace = self.namespace_lock.lock();
        if name.len() > NAME_MAX {
            return Err(NvstorError::NameTooLong);
        }

        {
            let inodes = self.inodes.read();
            let parent = inodes.get(&parent_ino).ok_or(NvstorError::NotFound)?;
            require_dir(parent.file_type)?;
        }
        {
            let children = self.dir_children.read();
            if children.contains_key(&(parent_ino, name.to_string())) {
                return Err(NvstorError::Exists);
            }
        }

        let now = now_secs();
        let ino = self.alloc_ino();
        let gen = self.alloc_generation();

        let entry = InodeEntry {
            ino,
            parent_ino,
            name: name.to_string(),
            file_type: FileType::Directory,
            size: 0,
            mode,
            uid: 0,
            gid: 0,
            atime: now,
            mtime: now,
            ctime: now,
            nlink: 2,
            generation: gen,
            object_id: None,
            symlink_target: None,
        };

        let attrs = entry.to_attrs();

        self.persist_inode(&entry);
        self.persist_dirent(parent_ino, name, ino);
        self.register_fh(ino, gen);

        self.inodes.write().insert(ino, entry);
        self.dir_children
            .write()
            .insert((parent_ino, name.to_string()), ino);

        // Increment parent nlink for the new subdirectory.
        {
            let mut inodes = self.inodes.write();
            if let Some(parent) = inodes.get_mut(&parent_ino) {
                parent.nlink += 1;
                parent.mtime = now;
                self.persist_inode(parent);
            }
        }

        debug!(ino, name, "vfs: mkdir");
        Ok(attrs)
    }

    /// Remove a file (not a directory).
    pub fn unlink(&self, parent_ino: u64, name: &str) -> Result<()> {
        let _namespace = self.namespace_lock.lock();
        let child_ino = {
            let children = self.dir_children.read();
            *children
                .get(&(parent_ino, name.to_string()))
                .ok_or(NvstorError::NotFound)?
        };

        let _data = self.data_locks[child_ino as usize % self.data_locks.len()].write();

        {
            let inodes = self.inodes.read();
            let child = inodes.get(&child_ino).ok_or(NvstorError::NotFound)?;
            if child.file_type == FileType::Directory {
                return Err(NvstorError::IsDir);
            }
        }

        // Remove directory entry.
        self.delete_persisted_dirent(parent_ino, name);
        self.dir_children
            .write()
            .remove(&(parent_ino, name.to_string()));

        // Decrement nlink. Only remove inode + backing object when nlink reaches 0.
        let should_remove = {
            let mut inodes = self.inodes.write();
            if let Some(entry) = inodes.get_mut(&child_ino) {
                entry.nlink = entry.nlink.saturating_sub(1);
                if entry.nlink == 0 {
                    let generation = entry.generation;
                    if let Some(ref obj_id) = entry.object_id {
                        let _ = self.mgr.object_delete(obj_id);
                    }
                    // Drop the PmStore volume too.
                    if let Some(pm) = self.pm_store.as_ref() {
                        let _ = pm.delete(Self::pm_volume_id(child_ino));
                    }
                    inodes.remove(&child_ino);
                    Some(generation)
                } else {
                    self.persist_inode(entry);
                    None
                }
            } else {
                None
            }
        };

        if let Some(generation) = should_remove {
            self.delete_persisted_inode(child_ino);
            self.unregister_fh(child_ino, generation);
        }

        // Update parent mtime.
        let now = now_secs();
        {
            let mut inodes = self.inodes.write();
            if let Some(parent) = inodes.get_mut(&parent_ino) {
                parent.mtime = now;
                self.persist_inode(parent);
            }
        }

        debug!(child_ino, name, "vfs: unlink");
        Ok(())
    }

    /// Remove an empty directory.
    pub fn rmdir(&self, parent_ino: u64, name: &str) -> Result<()> {
        let _namespace = self.namespace_lock.lock();
        let child_ino = {
            let children = self.dir_children.read();
            *children
                .get(&(parent_ino, name.to_string()))
                .ok_or(NvstorError::NotFound)?
        };

        {
            let inodes = self.inodes.read();
            let child = inodes.get(&child_ino).ok_or(NvstorError::NotFound)?;
            if child.file_type != FileType::Directory {
                return Err(NvstorError::NotDir);
            }
        }

        // Check directory is empty.
        {
            let children = self.dir_children.read();
            let has_children = children.keys().any(|(p, _)| *p == child_ino);
            if has_children {
                return Err(NvstorError::NotEmpty);
            }
        }

        // Get generation for FH cleanup.
        let generation = {
            let inodes = self.inodes.read();
            inodes.get(&child_ino).map_or(0, |e| e.generation)
        };

        self.delete_persisted_inode(child_ino);
        self.delete_persisted_dirent(parent_ino, name);
        self.unregister_fh(child_ino, generation);

        self.dir_children
            .write()
            .remove(&(parent_ino, name.to_string()));
        self.inodes.write().remove(&child_ino);

        // Decrement parent nlink.
        let now = now_secs();
        {
            let mut inodes = self.inodes.write();
            if let Some(parent) = inodes.get_mut(&parent_ino) {
                parent.nlink = parent.nlink.saturating_sub(1);
                parent.mtime = now;
                self.persist_inode(parent);
            }
        }

        debug!(child_ino, name, "vfs: rmdir");
        Ok(())
    }

    /// Rename a file or directory.
    pub fn rename(
        &self,
        old_parent: u64,
        old_name: &str,
        new_parent: u64,
        new_name: &str,
    ) -> Result<()> {
        let _namespace = self.namespace_lock.lock();
        for name in [old_name, new_name] {
            if name.len() > 255 {
                return Err(NvstorError::NameTooLong);
            }
            if name.is_empty() || name == "." || name == ".." || name.contains('/') {
                return Err(NvstorError::InvalidArgument("invalid rename name".into()));
            }
        }
        let old_key = (old_parent, old_name.to_string());
        let new_key = (new_parent, new_name.to_string());
        let (ino, target) = {
            let children = self.dir_children.read();
            (
                *children.get(&old_key).ok_or(NvstorError::NotFound)?,
                children.get(&new_key).copied(),
            )
        };
        if target == Some(ino) {
            return Ok(());
        }
        // Pin the replaced file while retiring its last link. Lock order matches file I/O.
        let _target_data =
            target.map(|id| self.data_locks[id as usize % self.data_locks.len()].write());
        let mut inodes = self.inodes.write();
        let mut children = self.dir_children.write();
        let mut source = inodes.get(&ino).ok_or(NvstorError::NotFound)?.clone();
        let mut parents = HashMap::new();
        for id in [old_parent, new_parent] {
            let parent = inodes.get(&id).ok_or(NvstorError::NotFound)?;
            require_dir(parent.file_type)?;
            parents.insert(id, parent.clone());
        }
        let directory = source.file_type == FileType::Directory;
        if directory {
            let mut ancestor = new_parent;
            loop {
                if ancestor == ino {
                    return Err(NvstorError::InvalidArgument(
                        "cannot move directory into itself".into(),
                    ));
                }
                let parent = inodes
                    .get(&ancestor)
                    .ok_or(NvstorError::NotFound)?
                    .parent_ino;
                if parent == ancestor {
                    break;
                }
                ancestor = parent;
            }
        }
        let now = now_secs();
        let mut replaced = target
            .map(|id| inodes.get(&id).cloned().ok_or(NvstorError::NotFound))
            .transpose()?;
        if let Some(ref mut entry) = replaced {
            let target_directory = entry.file_type == FileType::Directory;
            if directory && !target_directory {
                return Err(NvstorError::NotDir);
            }
            if !directory && target_directory {
                return Err(NvstorError::IsDir);
            }
            if target_directory {
                if children.keys().any(|(parent, _)| *parent == entry.ino) {
                    return Err(NvstorError::NotEmpty);
                }
                entry.nlink = 0;
                let parent = parents
                    .get_mut(&new_parent)
                    .expect("invariant: both parents were inserted above");
                parent.nlink = parent.nlink.saturating_sub(1);
            } else {
                entry.nlink = entry.nlink.saturating_sub(1);
            }
            entry.ctime = now;
        }
        if directory && old_parent != new_parent {
            let parent = parents
                .get_mut(&old_parent)
                .expect("invariant: both parents were inserted above");
            parent.nlink = parent.nlink.saturating_sub(1);
            parents
                .get_mut(&new_parent)
                .expect("invariant: both parents were inserted above")
                .nlink += 1;
        }
        source.parent_ino = new_parent;
        source.name = new_name.to_string();
        source.ctime = now;
        for parent in parents.values_mut() {
            parent.mtime = now;
            parent.ctime = now;
        }
        if let Some(ref meta) = self.metadata {
            let mut puts = vec![(
                format!("nfs/d/{new_parent}/{new_name}"),
                ino.to_le_bytes().to_vec(),
            )];
            for entry in std::iter::once(&source)
                .chain(parents.values())
                .chain(replaced.iter().filter(|e| e.nlink > 0))
            {
                puts.push((
                    format!("nfs/i/{}", entry.ino),
                    bincode::serialize(entry).map_err(|e| NvstorError::Internal(e.to_string()))?,
                ));
            }
            let mut deletes = vec![format!("nfs/d/{old_parent}/{old_name}")];
            if let Some(entry) = replaced.as_ref().filter(|e| e.nlink == 0) {
                deletes.push(format!("nfs/i/{}", entry.ino));
                deletes.push(format!(
                    "nfs/fh/{}",
                    hex::encode(Self::make_fh(entry.ino, entry.generation))
                ));
            }
            let put_refs: Vec<_> = puts
                .iter()
                .map(|(k, v)| (k.as_str(), v.as_slice()))
                .collect();
            let delete_refs: Vec<_> = deletes.iter().map(String::as_str).collect();
            meta.batch_inline(&put_refs, &delete_refs)
                .map_err(|e| NvstorError::Internal(e.to_string()))?;
        }
        // Publish in memory only after the whole namespace transaction is durable.
        children.remove(&old_key);
        children.insert(new_key, ino);
        inodes.insert(ino, source);
        inodes.extend(parents);
        if let Some(ref entry) = replaced {
            if entry.nlink == 0 {
                inodes.remove(&entry.ino);
            } else {
                inodes.insert(entry.ino, entry.clone());
            }
        }
        drop(children);
        drop(inodes);
        if let Some(entry) = replaced.filter(|e| e.nlink == 0) {
            self.fh_map
                .write()
                .remove(&Self::make_fh(entry.ino, entry.generation));
            if let Some(pm) = self.pm_store.as_ref() {
                if let Err(e) = pm.delete(Self::pm_volume_id(entry.ino)) {
                    if e.kind() != std::io::ErrorKind::NotFound {
                        warn!(error = %e, "rename backing cleanup failed");
                    }
                }
            } else if let Some(ref object_id) = entry.object_id {
                if let Err(e) = self.mgr.object_delete(object_id) {
                    if !matches!(e, NvstorError::NotFound) {
                        warn!(error = %e, "rename backing cleanup failed");
                    }
                }
            }
        }
        Ok(())
    }

    /// Verifier for the READDIR cookies of `dir_ino`. Cookies here are
    /// positions in the name-sorted listing, so they stay valid only while
    /// the set of entries is unchanged: the verifier is a digest of the
    /// entries and changes with every create, remove or rename in the
    /// directory.
    pub fn readdir_verifier(&self, dir_ino: u64) -> Result<[u8; 8]> {
        {
            let inodes = self.inodes.read();
            let dir = inodes.get(&dir_ino).ok_or(NvstorError::NotFound)?;
            require_dir(dir.file_type)?;
        }
        let children = self.dir_children.read();
        let mut entries: Vec<(&str, u64)> = children
            .iter()
            .filter(|((parent, _), _)| *parent == dir_ino)
            .map(|((_, name), &ino)| (name.as_str(), ino))
            .collect();
        entries.sort_unstable();
        let mut parts: Vec<Vec<u8>> = Vec::with_capacity(entries.len() + 1);
        parts.push(dir_ino.to_le_bytes().to_vec());
        for (name, ino) in entries {
            let mut part = name.as_bytes().to_vec();
            part.extend_from_slice(&ino.to_le_bytes());
            parts.push(part);
        }
        let refs: Vec<&[u8]> = parts.iter().map(Vec::as_slice).collect();
        Ok(cookie_verifier(&refs))
    }

    /// Read directory entries starting from a cookie.
    /// Cookie 0 means start from the beginning.
    /// Returns entries and whether EOF was reached.
    pub fn readdir(&self, dir_ino: u64, cookie: u64) -> Result<(Vec<DirEntry>, bool)> {
        {
            let inodes = self.inodes.read();
            let dir = inodes.get(&dir_ino).ok_or(NvstorError::NotFound)?;
            require_dir(dir.file_type)?;
        }

        let children = self.dir_children.read();
        let inodes = self.inodes.read();

        // Collect all children of this directory, sorted by name for stable ordering.
        let mut entries: Vec<DirEntry> = Vec::new();

        // Add "." and ".." first.
        if cookie == 0 {
            if let Some(dir_entry) = inodes.get(&dir_ino) {
                entries.push(DirEntry {
                    ino: dir_ino,
                    name: ".".to_string(),
                    file_type: FileType::Directory,
                    cookie: 1,
                });
                entries.push(DirEntry {
                    ino: dir_entry.parent_ino,
                    name: "..".to_string(),
                    file_type: FileType::Directory,
                    cookie: 2,
                });
            }
        }

        // Collect real children.
        let mut child_list: Vec<(String, u64)> = children
            .iter()
            .filter(|((p, _), _)| *p == dir_ino)
            .map(|((_, name), &ino)| (name.clone(), ino))
            .collect();
        child_list.sort_by(|a, b| a.0.cmp(&b.0));

        let start_cookie = if cookie <= 2 {
            0
        } else {
            (cookie - 2) as usize
        };

        for (idx, (name, ino)) in child_list.iter().enumerate().skip(start_cookie) {
            if let Some(child) = inodes.get(ino) {
                entries.push(DirEntry {
                    ino: *ino,
                    name: name.clone(),
                    file_type: child.file_type,
                    cookie: (idx + 3) as u64, // cookies: 1=., 2=.., 3+=real entries
                });
            }
        }

        Ok((entries, true)) // Always EOF for in-memory dirs (no pagination needed yet).
    }

    // -----------------------------------------------------------------------
    // File I/O
    // -----------------------------------------------------------------------

    /// Read data from a file at the given offset.
    pub fn read(&self, ino: u64, offset: u64, len: u64) -> Result<Vec<u8>> {
        let _guard = self.data_locks[ino as usize % self.data_locks.len()].read();
        self.read_inner(ino, offset, len)
    }

    /// Read into a caller buffer. PmStore-backed files are read in place;
    /// chunk-backed files are assembled and copied.
    pub fn read_into(&self, ino: u64, offset: u64, buf: &mut [u8]) -> Result<usize> {
        let _guard = self.data_locks[ino as usize % self.data_locks.len()].read();
        let file_size = {
            let inodes = self.inodes.read();
            let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;
            require_file(entry.file_type)?;
            entry.size
        };
        if offset >= file_size || buf.is_empty() {
            return Ok(0);
        }
        let want = ((file_size - offset) as usize).min(buf.len());
        if let Some(pm) = self.pm_store.as_ref() {
            let id = Self::pm_volume_id(ino);
            return match pm.read(id, offset, &mut buf[..want]) {
                Ok(n) => {
                    buf[n.min(want)..want].fill(0);
                    Ok(want)
                }
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                    buf[..want].fill(0);
                    Ok(want)
                }
                Err(e) => Err(io_to_nvstor(e)),
            };
        }
        let data = self.read_inner(ino, offset, want as u64)?;
        let n = data.len().min(want);
        buf[..n].copy_from_slice(&data[..n]);
        Ok(n)
    }

    fn read_inner(&self, ino: u64, offset: u64, len: u64) -> Result<Vec<u8>> {
        let (obj_id, file_size) = {
            let inodes = self.inodes.read();
            let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;

            require_file(entry.file_type)?;

            if entry.size == 0 || offset >= entry.size {
                return Ok(Vec::new());
            }

            let obj_id = entry.object_id.clone().ok_or(NvstorError::NotFound)?;
            (obj_id, entry.size)
        };

        let end = (offset + len).min(file_size);
        let want = (end - offset) as usize;

        // PmStore-backed read — pread at the right
        // offset, no chunk indirection. Falls back to chunk manager
        // when the Vfs wasn't constructed with a pm_store.
        if let Some(pm) = self.pm_store.as_ref() {
            let id = Self::pm_volume_id(ino);
            let mut buf = vec![0u8; want];
            match pm.read(id, offset, &mut buf) {
                Ok(n) => {
                    if n < want {
                        buf[n..].fill(0);
                    }
                    return Ok(buf);
                }
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                    // Volume never created (file marked as having data
                    // in metadata but PmStore knows nothing). Treat as
                    // sparse zeros.
                    return Ok(vec![0u8; want]);
                }
                Err(e) => return Err(io_to_nvstor(e)),
            }
        }

        if offset == 0 && end >= file_size {
            self.mgr.object_read(&obj_id)
        } else {
            self.mgr.object_read_range(&obj_id, offset, end)
        }
    }

    /// Write data to a file at the given offset. Returns bytes written.
    ///
    /// Optimized write path:
    /// - New file (size==0): direct write, no read needed.
    /// - Append (offset == size): streaming append via object_write_begin/chunk/finish.
    /// - Overwrite within existing data: read existing, patch, rewrite.
    ///   For small files (< 64KB inline threshold), this is fast.
    ///   For large files, only reads the necessary data.
    pub fn write(&self, ino: u64, offset: u64, data: &[u8]) -> Result<usize> {
        let _guard = self.data_locks[ino as usize % self.data_locks.len()].write();
        self.write_inner(ino, offset, data)
    }

    fn write_inner(&self, ino: u64, offset: u64, data: &[u8]) -> Result<usize> {
        let new_end = offset
            .checked_add(data.len() as u64)
            .ok_or(NvstorError::OutOfBounds)?;
        if self.pm_store.is_some() && new_end > PM_VOLUME_CAP {
            return Err(NvstorError::OutOfBounds);
        }
        if data.is_empty() {
            return Ok(0);
        }

        let now = now_secs();
        let (obj_key, old_size) = {
            let inodes = self.inodes.read();
            let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;
            require_file(entry.file_type)?;
            let key = entry
                .object_id
                .clone()
                .unwrap_or_else(|| self.object_key(ino));
            (key, entry.size)
        };

        let new_size = new_end.max(old_size);

        // PmStore-backed write — single pwrite at
        // offset, no read-modify-write, no rehash, no per-byte
        // metadata churn. Drops the object_read+object_write path
        // that turned every NFS WRITE into O(filesize) work.
        //
        // SyncMode::None mirrors the NBD path: NFS COMMIT triggers
        // an explicit fsync via flush(), and SETATTR+atime updates
        // ride the per-COMPOUND WriteBatch. Per-WRITE durability is
        // not required by the protocol and was killing throughput.
        if let Some(pm) = self.pm_store.as_ref() {
            self.pm_ensure_volume(pm, ino, new_end)?;
            let id = Self::pm_volume_id(ino);
            pm.write(id, offset, data, SyncMode::None)
                .map_err(io_to_nvstor)?;
        } else if old_size == 0 && offset == 0 {
            // Fallback (no pm_store): legacy CA-chunk path.
            self.mgr.object_write(&obj_key, data, None)?;
        } else if offset == old_size {
            let mut ctx = self.mgr.object_write_begin(&obj_key, None);
            let existing = self.mgr.object_read(&obj_key)?;
            self.mgr.object_write_chunk(&mut ctx, &existing)?;
            self.mgr.object_write_chunk(&mut ctx, data)?;
            self.mgr.object_write_finish(ctx)?;
        } else {
            let mut buf = self
                .mgr
                .object_read(&obj_key)
                .unwrap_or_else(|_| vec![0u8; old_size as usize]);
            if buf.len() < new_size as usize {
                buf.resize(new_size as usize, 0);
            }
            buf[offset as usize..(offset as usize + data.len())].copy_from_slice(data);
            self.mgr.object_write(&obj_key, &buf, None)?;
        }

        // Update inode. Use the nosync variant so we don't pay an
        // fsync per WRITE. Durability rides the NFS COMMIT path
        // (which is implemented as an explicit PmStore::flush + a
        // synchronous metadata barrier on the same xid).
        {
            let mut inodes = self.inodes.write();
            if let Some(entry) = inodes.get_mut(&ino) {
                // Grow-only under concurrency. `new_size` was computed
                // from an `old_size` snapshot read before this write's
                // I/O; a concurrent WRITE at a higher offset may have
                // already grown `entry.size` in the interim. Storing
                // `new_size` unconditionally would shrink the recorded
                // size back and make that other write's tail invisible
                // (reads clamp to entry.size). A WRITE never shrinks a
                // file — truncate takes the SETATTR path — so max() is
                // correct and closes the race.
                entry.size = entry.size.max(new_size);
                entry.mtime = now;
                entry.object_id = Some(obj_key);
                self.persist_inode_nosync(entry);
            }
        }

        Ok(data.len())
    }

    /// Look up parent directory of an inode.
    pub fn lookupp(&self, ino: u64) -> Result<InodeAttrs> {
        let inodes = self.inodes.read();
        let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;
        require_dir(entry.file_type)?;
        let parent_ino = entry.parent_ino;
        let parent = inodes.get(&parent_ino).ok_or(NvstorError::NotFound)?;
        Ok(parent.to_attrs())
    }

    /// Create a symbolic link.
    pub fn create_symlink(&self, parent_ino: u64, name: &str, target: &str) -> Result<InodeAttrs> {
        let _namespace = self.namespace_lock.lock();
        if name.len() > NAME_MAX {
            return Err(NvstorError::NameTooLong);
        }
        {
            let inodes = self.inodes.read();
            let parent = inodes.get(&parent_ino).ok_or(NvstorError::NotFound)?;
            require_dir(parent.file_type)?;
        }
        {
            let children = self.dir_children.read();
            if children.contains_key(&(parent_ino, name.to_string())) {
                return Err(NvstorError::Exists);
            }
        }

        let now = now_secs();
        let ino = self.alloc_ino();
        let gen = self.alloc_generation();

        let entry = InodeEntry {
            ino,
            parent_ino,
            name: name.to_string(),
            file_type: FileType::Symlink,
            size: target.len() as u64,
            mode: 0o777,
            uid: 0,
            gid: 0,
            atime: now,
            mtime: now,
            ctime: now,
            nlink: 1,
            generation: gen,
            object_id: None,
            symlink_target: Some(target.to_string()),
        };

        let attrs = entry.to_attrs();
        self.persist_inode(&entry);
        self.persist_dirent(parent_ino, name, ino);
        self.register_fh(ino, gen);

        self.inodes.write().insert(ino, entry);
        self.dir_children
            .write()
            .insert((parent_ino, name.to_string()), ino);

        {
            let mut inodes = self.inodes.write();
            if let Some(parent) = inodes.get_mut(&parent_ino) {
                parent.mtime = now;
                self.persist_inode(parent);
            }
        }

        debug!(ino, name, target, "vfs: create_symlink");
        Ok(attrs)
    }

    /// Read symlink target.
    pub fn readlink(&self, ino: u64) -> Result<String> {
        let inodes = self.inodes.read();
        let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;
        if entry.file_type != FileType::Symlink {
            return Err(NvstorError::InvalidArgument("not a symlink".into()));
        }
        entry
            .symlink_target
            .clone()
            .ok_or(NvstorError::InvalidArgument("missing target".into()))
    }

    /// Create a hard link (new directory entry pointing to existing inode).
    pub fn link(&self, target_ino: u64, parent_ino: u64, name: &str) -> Result<InodeAttrs> {
        let _namespace = self.namespace_lock.lock();
        if name.len() > NAME_MAX {
            return Err(NvstorError::NameTooLong);
        }

        // Verify target exists and is not a directory.
        {
            let inodes = self.inodes.read();
            let target = inodes.get(&target_ino).ok_or(NvstorError::NotFound)?;
            if target.file_type == FileType::Directory {
                return Err(NvstorError::IsDir);
            }
            let parent = inodes.get(&parent_ino).ok_or(NvstorError::NotFound)?;
            require_dir(parent.file_type)?;
        }

        // Check name doesn't exist.
        {
            let children = self.dir_children.read();
            if children.contains_key(&(parent_ino, name.to_string())) {
                return Err(NvstorError::Exists);
            }
        }

        let now = now_secs();

        // Add directory entry (no new inode — reuse target_ino).
        self.persist_dirent(parent_ino, name, target_ino);
        self.dir_children
            .write()
            .insert((parent_ino, name.to_string()), target_ino);

        // Increment nlink on the target.
        let attrs = {
            let mut inodes = self.inodes.write();
            if let Some(entry) = inodes.get_mut(&target_ino) {
                entry.nlink += 1;
                entry.ctime = now;
                self.persist_inode(entry);
                entry.to_attrs()
            } else {
                return Err(NvstorError::NotFound);
            }
        };

        // Update parent mtime.
        {
            let mut inodes = self.inodes.write();
            if let Some(parent) = inodes.get_mut(&parent_ino) {
                parent.mtime = now;
                self.persist_inode(parent);
            }
        }

        debug!(target_ino, parent_ino, name, "vfs: link");
        Ok(attrs)
    }

    // -----------------------------------------------------------------------
    // File locking
    // -----------------------------------------------------------------------

    /// Acquire a byte-range lock. Returns Ok if acquired, Err(Exists) if conflict.
    pub fn lock(
        &self,
        ino: u64,
        offset: u64,
        length: u64,
        lock_type: LockType,
        owner: &[u8],
        clientid: u64,
    ) -> Result<()> {
        // Verify inode exists.
        {
            let inodes = self.inodes.read();
            if !inodes.contains_key(&ino) {
                return Err(NvstorError::NotFound);
            }
        }

        let mut locks = self.locks.write();
        let file_locks = locks.entry(ino).or_default();

        // Check for conflicts.
        let end = if length == 0 {
            u64::MAX
        } else {
            offset + length
        };
        for existing in file_locks.iter() {
            let ex_end = if existing.length == 0 {
                u64::MAX
            } else {
                existing.offset + existing.length
            };
            // Ranges overlap?
            if offset < ex_end && existing.offset < end {
                // Same owner can upgrade/downgrade.
                if existing.owner == owner && existing.clientid == clientid {
                    continue;
                }
                // Write locks conflict with everything; read locks conflict with writes.
                if lock_type == LockType::WriteLock || existing.lock_type == LockType::WriteLock {
                    return Err(NvstorError::Exists); // Conflict
                }
            }
        }

        file_locks.push(LockRange {
            offset,
            length,
            lock_type,
            owner: owner.to_vec(),
            clientid,
        });

        // Persist lock state.
        self.persist_locks(ino, file_locks);
        Ok(())
    }

    /// Test if a lock would conflict without acquiring it.
    /// Returns None if no conflict, Some(conflicting_lock) if conflict.
    pub fn lock_test(
        &self,
        ino: u64,
        offset: u64,
        length: u64,
        lock_type: LockType,
        owner: &[u8],
    ) -> Option<LockRange> {
        let locks = self.locks.read();
        let file_locks = locks.get(&ino)?;
        let end = if length == 0 {
            u64::MAX
        } else {
            offset + length
        };

        for existing in file_locks {
            let ex_end = if existing.length == 0 {
                u64::MAX
            } else {
                existing.offset + existing.length
            };
            if offset < ex_end && existing.offset < end {
                if existing.owner == owner {
                    continue;
                }
                if lock_type == LockType::WriteLock || existing.lock_type == LockType::WriteLock {
                    return Some(existing.clone());
                }
            }
        }
        None
    }

    /// Release a lock. Removes the first matching lock range for the owner.
    pub fn unlock(&self, ino: u64, offset: u64, length: u64, owner: &[u8]) -> Result<()> {
        let mut locks = self.locks.write();
        let file_locks = locks.get_mut(&ino).ok_or(NvstorError::NotFound)?;
        let before = file_locks.len();
        file_locks.retain(|l| !(l.offset == offset && l.length == length && l.owner == owner));
        if file_locks.len() == before {
            return Err(NvstorError::NotFound); // No matching lock
        }
        self.persist_locks(ino, file_locks);
        Ok(())
    }

    /// Release all locks held by a specific owner on all files.
    pub fn release_locks_by_owner(&self, owner: &[u8]) {
        let mut locks = self.locks.write();
        for (ino, file_locks) in locks.iter_mut() {
            let before = file_locks.len();
            file_locks.retain(|l| l.owner != owner);
            if file_locks.len() != before {
                self.persist_locks(*ino, file_locks);
            }
        }
        // Remove empty entries.
        locks.retain(|_, v| !v.is_empty());
    }

    fn persist_locks(&self, ino: u64, locks: &[LockRange]) {
        if let Some(ref meta) = self.metadata {
            let key = format!("nfs/l/{ino}");
            if locks.is_empty() {
                let _ = meta.delete_inline(&key);
            } else {
                match bincode::serialize(locks) {
                    Ok(bytes) => {
                        let _ = meta.put_inline(&key, &bytes);
                    }
                    Err(e) => warn!(ino, error = %e, "failed to persist locks"),
                }
            }
        }
    }

    /// Server-side copy: copy data from src to dst. Returns bytes copied.
    /// Uses the active data backend and preserves bytes outside the copied range.
    pub fn copy_file(
        &self,
        src_ino: u64,
        src_offset: u64,
        dst_ino: u64,
        dst_offset: u64,
        count: u64,
    ) -> Result<u64> {
        let src_index = src_ino as usize % self.data_locks.len();
        let dst_index = dst_ino as usize % self.data_locks.len();
        let _first = self.data_locks[src_index.min(dst_index)].write();
        let _second = if src_index != dst_index {
            Some(self.data_locks[src_index.max(dst_index)].write())
        } else {
            None
        };
        let (src_obj, src_size) = {
            let inodes = self.inodes.read();
            let entry = inodes.get(&src_ino).ok_or(NvstorError::NotFound)?;
            if entry.file_type != FileType::Regular {
                return Err(NvstorError::InvalidArgument(
                    "source must be regular file".into(),
                ));
            }
            let obj = entry.object_id.clone().ok_or(NvstorError::NotFound)?;
            (obj, entry.size)
        };

        let dst_obj = {
            let inodes = self.inodes.read();
            let entry = inodes.get(&dst_ino).ok_or(NvstorError::NotFound)?;
            if entry.file_type != FileType::Regular {
                return Err(NvstorError::InvalidArgument(
                    "dest must be regular file".into(),
                ));
            }
            entry
                .object_id
                .clone()
                .unwrap_or_else(|| self.object_key(dst_ino))
        };

        // Determine actual bytes to copy.
        let actual_count = if count == 0 {
            src_size.saturating_sub(src_offset)
        } else {
            count.min(src_size.saturating_sub(src_offset))
        };
        if actual_count == 0 {
            return Ok(0);
        }

        let dst_end = dst_offset
            .checked_add(actual_count)
            .ok_or(NvstorError::OutOfBounds)?;
        if self.pm_store.is_some() {
            if dst_end > PM_VOLUME_CAP {
                return Err(NvstorError::OutOfBounds);
            }
            // Bounded memory and memmove semantics for overlapping ranges.
            let backwards = src_ino == dst_ino
                && dst_offset > src_offset
                && dst_offset < src_offset + actual_count;
            let mut copied = 0;
            while copied < actual_count {
                let length = (actual_count - copied).min(1024 * 1024);
                let position = if backwards {
                    actual_count - copied - length
                } else {
                    copied
                };
                let data = self.read_inner(src_ino, src_offset + position, length)?;
                if data.len() as u64 != length {
                    return Err(NvstorError::Internal(
                        "short source read during copy".into(),
                    ));
                }
                self.write_inner(dst_ino, dst_offset + position, &data)?;
                copied += length;
            }
            self.fsync(dst_ino)?;
            return Ok(copied);
        }

        // A whole-source copy may still cover only a prefix of the destination.
        let old_dst_size = self.getattr(dst_ino)?.size;
        if src_offset == 0
            && dst_offset == 0
            && actual_count == src_size
            && old_dst_size <= actual_count
        {
            self.mgr.object_copy(&src_obj, &dst_obj)?;
        } else {
            // Partial copy: read source range, write to destination.
            let data =
                self.mgr
                    .object_read_range(&src_obj, src_offset, src_offset + actual_count)?;

            // Read existing dst data, patch in the copied range, rewrite.
            let old_dst_size = {
                let inodes = self.inodes.read();
                inodes.get(&dst_ino).map_or(0, |e| e.size)
            };
            let new_end = dst_offset + data.len() as u64;
            let new_size = new_end.max(old_dst_size);

            let mut buf = if old_dst_size > 0 {
                self.mgr
                    .object_read(&dst_obj)
                    .unwrap_or_else(|_| vec![0u8; old_dst_size as usize])
            } else {
                Vec::new()
            };
            buf.resize(new_size as usize, 0);
            buf[dst_offset as usize..(dst_offset as usize + data.len())].copy_from_slice(&data);
            self.mgr.object_write(&dst_obj, &buf, None)?;
        }

        // Update destination inode.
        let now = now_secs();
        {
            let mut inodes = self.inodes.write();
            if let Some(entry) = inodes.get_mut(&dst_ino) {
                let new_dst_end = dst_offset + actual_count;
                if new_dst_end > entry.size {
                    entry.size = new_dst_end;
                }
                entry.mtime = now;
                entry.object_id = Some(dst_obj);
                self.persist_inode(entry);
            }
        }

        debug!(src_ino, dst_ino, bytes = actual_count, "vfs: copy_file");
        Ok(actual_count)
    }

    /// Allocate space for a file (fallocate). Extends file if needed.
    pub fn allocate(&self, ino: u64, offset: u64, length: u64) -> Result<()> {
        let needed = offset + length;
        let inodes = self.inodes.read();
        let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;
        if entry.size < needed {
            drop(inodes);
            self.setattr(ino, Some(needed))?;
        }
        Ok(())
    }

    /// Seek for data or hole. Returns (eof: bool, offset: u64).
    /// Since we don't support sparse files, SEEK_DATA returns the offset
    /// if < size, SEEK_HOLE returns file size (EOF).
    pub fn seek(&self, ino: u64, offset: u64, what: u32) -> Result<(bool, u64)> {
        let inodes = self.inodes.read();
        let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;
        if what == 0 {
            // SEEK_DATA
            if offset >= entry.size {
                Ok((true, entry.size)) // EOF
            } else {
                Ok((false, offset)) // Data starts here
            }
        } else {
            // SEEK_HOLE — all data is non-sparse, so first hole is at end of file.
            Ok((offset >= entry.size, entry.size))
        }
    }

    /// Check POSIX file permissions.
    /// Returns Ok if access is allowed, Err(PermissionDenied) if not.
    ///
    /// access_mask: 4=read, 2=write, 1=execute
    pub fn check_permission(&self, ino: u64, uid: u32, gid: u32, access_mask: u32) -> Result<()> {
        let inodes = self.inodes.read();
        let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;

        // Root (uid 0) can do anything.
        if uid == 0 {
            return Ok(());
        }

        let mode = entry.mode;
        let bits = if uid == entry.uid {
            (mode >> 6) & 0o7 // owner bits
        } else if gid == entry.gid {
            (mode >> 3) & 0o7 // group bits
        } else {
            mode & 0o7 // other bits
        };

        if (bits & access_mask) == access_mask {
            Ok(())
        } else {
            Err(NvstorError::PermissionDenied)
        }
    }

    /// Check if there are any active locks.
    pub fn locks_empty(&self) -> bool {
        self.locks.read().is_empty()
    }

    /// Set the mode (permissions) on an inode.
    pub fn set_mode(&self, ino: u64, mode: u32) {
        let mut inodes = self.inodes.write();
        if let Some(entry) = inodes.get_mut(&ino) {
            entry.mode = mode;
            entry.ctime = now_secs();
            self.persist_inode(entry);
        }
    }

    /// Set owner uid/gid on an inode.
    pub fn set_owner(&self, ino: u64, uid: u32, gid: u32) {
        let mut inodes = self.inodes.write();
        if let Some(entry) = inodes.get_mut(&ino) {
            entry.uid = uid;
            entry.gid = gid;
            entry.ctime = now_secs();
            self.persist_inode(entry);
        }
    }

    /// Flush an inode's dirty data to stable storage. This is the
    /// real durability barrier behind NFS COMMIT (op_commit) and
    /// stable WRITEs.
    ///
    /// File content rides `PmStore` written with
    /// `SyncMode::None` (page cache only) for throughput — so this is
    /// NOT a no-op: it must issue an `fdatasync` on the backing
    /// volume, otherwise a COMMIT that returns NFS4_OK is a lie and a
    /// power loss silently drops the client's acknowledged data. The
    /// old "ChunkManager writes are synchronous — nothing to flush"
    /// comment was stale from before the PmStore migration.
    pub fn fsync(&self, ino: u64) -> Result<()> {
        // Confirm the inode exists (and drop the read lock before I/O
        // so we don't hold it across the fdatasync).
        {
            let inodes = self.inodes.read();
            let entry = inodes.get(&ino).ok_or(NvstorError::NotFound)?;
            if entry.file_type == FileType::Symlink {
                return Err(NvstorError::Symlink);
            }
        }
        if let Some(pm) = self.pm_store.as_ref() {
            let id = Self::pm_volume_id(ino);
            // SyncMode::Data == fdatasync: file *data* durable. File
            // metadata (size/mtime) durability rides the per-COMPOUND
            // synchronous RocksDB batch, so Data (not Full) is the
            // right cost here. A not-yet-created volume (no bytes
            // written) has nothing to flush → treat NotFound as ok.
            match pm.flush(id, SyncMode::Data) {
                Ok(()) => Ok(()),
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(()),
                Err(e) => Err(io_to_nvstor(e)),
            }
        } else {
            // Legacy CA-chunk path: object_write already fsyncs per
            // chunk (SyncMode::Full), so nothing deferred to flush.
            Ok(())
        }
    }
}

fn now_secs() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64
}

// ===========================================================================
// Tests
// ===========================================================================

#[cfg(test)]
mod tests {
    use super::*;
    use nvstor_metadata::MetadataStore;
    use nvstor_storage::backend::PosixBackend;
    use nvstor_storage::manager::ChunkManagerConfig;
    use tempfile::TempDir;

    fn setup() -> (TempDir, Arc<ChunkManager>) {
        let tmp = TempDir::new().unwrap();
        let data_dir = tmp.path().join("data");
        let meta_dir = tmp.path().join("meta");
        let backend = Box::new(PosixBackend::new(&data_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let config = ChunkManagerConfig {
            disk_usage_threshold: 1.0,
            ..ChunkManagerConfig::default()
        };
        let mgr = Arc::new(ChunkManager::new(backend, Arc::new(metadata), config));
        (tmp, mgr)
    }

    /// Helper: build a VFS where regular-file content
    /// goes through a `PosixPmStore` instead of the legacy CA-chunk
    /// path. The TempDir is leaked into the closure return so the
    /// PmStore's backing files outlive the test body.
    fn setup_with_pm() -> (TempDir, Arc<Vfs>) {
        let tmp = TempDir::new().unwrap();
        let data_dir = tmp.path().join("data");
        let meta_dir = tmp.path().join("meta");
        let pm_dir = tmp.path().join("pm");
        let backend = Box::new(PosixBackend::new(&data_dir).unwrap());
        let metadata = MetadataStore::open(&meta_dir).unwrap();
        let config = ChunkManagerConfig {
            disk_usage_threshold: 1.0,
            ..ChunkManagerConfig::default()
        };
        let mgr = Arc::new(ChunkManager::new(backend, Arc::new(metadata), config));
        let pm: Arc<dyn PmStore> =
            Arc::new(nvstor_storage::pm::posix::PosixPmStore::new(&pm_dir).unwrap());
        let vfs = Arc::new(Vfs::with_metadata_and_pm(mgr, None, Some(pm), "test/"));
        (tmp, vfs)
    }

    #[test]
    fn read_into_matches_read_for_pm_and_chunk_backed_files() {
        let (_tmp, pm_vfs) = setup_with_pm();
        let (_tmp2, mgr) = setup();
        let chunk_vfs = Arc::new(Vfs::new(mgr, "test2/"));
        let data: Vec<u8> = (0..10_000).map(|i| (i % 253) as u8).collect();
        for vfs in [pm_vfs, chunk_vfs] {
            let file = vfs.create_file(1, "f", 0o644).unwrap();
            vfs.write(file.ino, 0, &data).unwrap();
            let mut buf = vec![0xff; 4096];
            assert_eq!(vfs.read_into(file.ino, 9000, &mut buf).unwrap(), 1000);
            assert_eq!(&buf[..1000], &data[9000..]);
            assert_eq!(vfs.read_into(file.ino, 100, &mut buf).unwrap(), 4096);
            assert_eq!(buf, vfs.read(file.ino, 100, 4096).unwrap());
            assert_eq!(vfs.read_into(file.ino, 20_000, &mut buf).unwrap(), 0);
        }
    }

    #[test]
    fn rename_replacement_preserves_other_hard_links_and_survives_reload() {
        let (tmp, base) = setup_with_pm();
        let metadata = Arc::new(MetadataStore::open(&tmp.path().join("namespace")).unwrap());
        let vfs = Vfs::with_metadata_and_pm(
            base.mgr.clone(),
            Some(metadata.clone()),
            base.pm_store.clone(),
            "test/",
        );
        let source = vfs.create_file(ROOT_INO, "source", 0o644).unwrap();
        let target = vfs.create_file(ROOT_INO, "target", 0o644).unwrap();
        vfs.write(source.ino, 0, b"new").unwrap();
        vfs.write(target.ino, 0, b"old").unwrap();
        vfs.link(target.ino, ROOT_INO, "alias").unwrap();
        vfs.rename(ROOT_INO, "source", ROOT_INO, "target").unwrap();
        drop(vfs);
        let vfs = Vfs::with_metadata_and_pm(
            base.mgr.clone(),
            Some(metadata),
            base.pm_store.clone(),
            "test/",
        );
        assert!(vfs.lookup(ROOT_INO, "source").is_err());
        assert_eq!(vfs.lookup(ROOT_INO, "target").unwrap().ino, source.ino);
        assert_eq!(vfs.lookup(ROOT_INO, "alias").unwrap().nlink, 1);
        assert_eq!(vfs.read(target.ino, 0, 3).unwrap(), b"old");
        vfs.rename(ROOT_INO, "target", ROOT_INO, "alias").unwrap();
        assert!(vfs.getattr(target.ino).is_err());
        assert!(base
            .pm_store
            .as_ref()
            .unwrap()
            .size(Vfs::pm_volume_id(target.ino))
            .is_err());
        assert_eq!(vfs.read(source.ino, 0, 3).unwrap(), b"new");
    }

    #[test]
    fn rename_directory_checks_cycles_types_and_link_counts() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let a = vfs.mkdir(ROOT_INO, "a", 0o755).unwrap();
        let b = vfs.mkdir(ROOT_INO, "b", 0o755).unwrap();
        let c = vfs.mkdir(a.ino, "c", 0o755).unwrap();
        assert!(vfs.rename(ROOT_INO, "a", c.ino, "cycle").is_err());
        vfs.create_file(b.ino, "file", 0o644).unwrap();
        assert!(vfs.rename(a.ino, "c", b.ino, "file").is_err());
        vfs.rename(a.ino, "c", b.ino, "c").unwrap();
        assert_eq!(vfs.getattr(a.ino).unwrap().nlink, 2);
        assert_eq!(vfs.getattr(b.ino).unwrap().nlink, 3);
    }

    #[test]
    fn rename_same_inode_preserves_pm_file_and_hard_links() {
        let (_tmp, vfs) = setup_with_pm();
        let file = vfs.create_file(ROOT_INO, "original", 0o644).unwrap();
        vfs.write(file.ino, 0, b"preserved").unwrap();
        vfs.rename(ROOT_INO, "original", ROOT_INO, "original")
            .unwrap();
        vfs.link(file.ino, ROOT_INO, "alias").unwrap();
        vfs.rename(ROOT_INO, "original", ROOT_INO, "alias").unwrap();
        assert_eq!(vfs.lookup(ROOT_INO, "original").unwrap().ino, file.ino);
        assert_eq!(vfs.lookup(ROOT_INO, "alias").unwrap().nlink, 2);
        assert_eq!(vfs.read(file.ino, 0, 100).unwrap(), b"preserved");
    }

    #[test]
    fn pm_copy_preserves_destination_tail_and_handles_overlap() {
        let (_tmp, vfs) = setup_with_pm();
        let src = vfs.create_file(ROOT_INO, "source", 0o644).unwrap().ino;
        let dst = vfs.create_file(ROOT_INO, "destination", 0o644).unwrap().ino;
        vfs.write(src, 0, b"ABCDE").unwrap();
        vfs.write(dst, 0, b"0123456789").unwrap();
        assert_eq!(vfs.copy_file(src, 0, dst, 0, 0).unwrap(), 5);
        assert_eq!(vfs.read(dst, 0, 100).unwrap(), b"ABCDE56789");
        vfs.copy_file(dst, 0, dst, 2, 5).unwrap();
        assert_eq!(vfs.read(dst, 0, 100).unwrap(), b"ABABCDE789");
        vfs.copy_file(dst, 2, dst, 0, 5).unwrap();
        assert_eq!(vfs.read(dst, 0, 100).unwrap(), b"ABCDEDE789");
    }

    #[test]
    fn concurrent_pm_growth_keeps_every_completed_write() {
        let (_tmp, vfs) = setup_with_pm();
        let ino = vfs.create_file(ROOT_INO, "growing", 0o644).unwrap().ino;
        std::thread::scope(|scope| {
            for index in 0..16u64 {
                let vfs = &vfs;
                scope.spawn(move || {
                    for _ in 0..20 {
                        vfs.write(ino, index * 4096, &vec![index as u8 + 1; 4096])
                            .unwrap();
                    }
                });
            }
        });
        assert_eq!(vfs.getattr(ino).unwrap().size, 16 * 4096);
        for index in 0..16u64 {
            assert_eq!(
                vfs.read(ino, index * 4096, 4096).unwrap(),
                vec![index as u8 + 1; 4096]
            );
        }
        vfs.setattr(ino, Some(17)).unwrap();
        vfs.write(ino, 8192, b"tail").unwrap();
        assert_eq!(vfs.getattr(ino).unwrap().size, 8196);
        assert_eq!(vfs.read(ino, 17, 8192 - 17).unwrap(), vec![0; 8192 - 17]);
    }

    #[test]
    fn root_exists() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let attrs = vfs.getattr(ROOT_INO).unwrap();
        assert_eq!(attrs.ino, ROOT_INO);
        assert_eq!(attrs.file_type, FileType::Directory);
        assert_eq!(attrs.nlink, 2);
    }

    #[test]
    fn lookup_root_dot() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let attrs = vfs.lookup(ROOT_INO, ".").unwrap();
        assert_eq!(attrs.ino, ROOT_INO);
    }

    #[test]
    fn create_file_and_lookup() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let attrs = vfs.create_file(ROOT_INO, "hello.txt", 0o644).unwrap();
        assert_eq!(attrs.file_type, FileType::Regular);
        assert_eq!(attrs.mode, 0o644);
        assert_eq!(attrs.size, 0);

        let found = vfs.lookup(ROOT_INO, "hello.txt").unwrap();
        assert_eq!(found.ino, attrs.ino);
    }

    #[test]
    fn create_file_getattr() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let created = vfs.create_file(ROOT_INO, "data.bin", 0o600).unwrap();
        let got = vfs.getattr(created.ino).unwrap();
        assert_eq!(got.ino, created.ino);
        assert_eq!(got.mode, 0o600);
        assert_eq!(got.file_type, FileType::Regular);
    }

    #[test]
    fn mkdir_and_readdir() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let dir_attrs = vfs.mkdir(ROOT_INO, "subdir", 0o755).unwrap();
        assert_eq!(dir_attrs.file_type, FileType::Directory);

        let (entries, eof) = vfs.readdir(ROOT_INO, 0).unwrap();
        assert!(eof);
        // Should contain ".", "..", and "subdir"
        let names: Vec<&str> = entries.iter().map(|e| e.name.as_str()).collect();
        assert!(names.contains(&"."));
        assert!(names.contains(&".."));
        assert!(names.contains(&"subdir"));
    }

    #[test]
    fn write_and_read_file() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let file = vfs.create_file(ROOT_INO, "test.txt", 0o644).unwrap();
        let data = b"Hello, nvstor NFS!";
        let n = vfs.write(file.ino, 0, data).unwrap();
        assert_eq!(n, data.len());

        let read_back = vfs.read(file.ino, 0, 1024).unwrap();
        assert_eq!(read_back, data);

        // Verify size updated.
        let attrs = vfs.getattr(file.ino).unwrap();
        assert_eq!(attrs.size, data.len() as u64);
    }

    #[test]
    fn unlink_file() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        vfs.create_file(ROOT_INO, "doomed.txt", 0o644).unwrap();
        assert!(vfs.lookup(ROOT_INO, "doomed.txt").is_ok());

        vfs.unlink(ROOT_INO, "doomed.txt").unwrap();
        assert!(vfs.lookup(ROOT_INO, "doomed.txt").is_err());
    }

    #[test]
    fn rename_file() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let file = vfs.create_file(ROOT_INO, "old.txt", 0o644).unwrap();
        vfs.write(file.ino, 0, b"content").unwrap();

        vfs.rename(ROOT_INO, "old.txt", ROOT_INO, "new.txt")
            .unwrap();

        assert!(vfs.lookup(ROOT_INO, "old.txt").is_err());
        let found = vfs.lookup(ROOT_INO, "new.txt").unwrap();
        assert_eq!(found.ino, file.ino);
    }

    #[test]
    fn rmdir_empty() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        vfs.mkdir(ROOT_INO, "empty_dir", 0o755).unwrap();
        vfs.rmdir(ROOT_INO, "empty_dir").unwrap();
        assert!(vfs.lookup(ROOT_INO, "empty_dir").is_err());
    }

    #[test]
    fn rmdir_notempty_fails() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let dir = vfs.mkdir(ROOT_INO, "full_dir", 0o755).unwrap();
        vfs.create_file(dir.ino, "child.txt", 0o644).unwrap();

        let err = vfs.rmdir(ROOT_INO, "full_dir").unwrap_err();
        assert!(matches!(err, NvstorError::NotEmpty));
    }

    #[test]
    fn filehandle_roundtrip() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let file = vfs.create_file(ROOT_INO, "fh_test.txt", 0o644).unwrap();
        let fh = vfs.ino_to_filehandle(file.ino);
        assert_eq!(fh.len(), FH_SIZE);

        let resolved = vfs.filehandle_to_ino(&fh).unwrap();
        assert_eq!(resolved, file.ino);
    }

    #[test]
    fn filehandle_stale() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let bogus = vec![0u8; FH_SIZE];
        let err = vfs.filehandle_to_ino(&bogus).unwrap_err();
        assert!(matches!(err, NvstorError::StaleHandle));
    }

    #[test]
    fn lookup_nonexistent() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let err = vfs.lookup(ROOT_INO, "nope").unwrap_err();
        assert!(matches!(err, NvstorError::NotFound));
    }

    #[test]
    fn create_duplicate_fails() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        vfs.create_file(ROOT_INO, "dup.txt", 0o644).unwrap();
        let err = vfs.create_file(ROOT_INO, "dup.txt", 0o644).unwrap_err();
        assert!(matches!(err, NvstorError::Exists));
    }

    #[test]
    fn fsync_valid_inode() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let file = vfs.create_file(ROOT_INO, "sync.txt", 0o644).unwrap();
        vfs.fsync(file.ino).unwrap();
    }

    #[test]
    fn persistence_across_restart() {
        let tmp = TempDir::new().unwrap();
        let data_dir = tmp.path().join("data");
        let meta_dir = tmp.path().join("meta");

        // Session 1: create files and write data.
        {
            let backend = Box::new(PosixBackend::new(&data_dir).unwrap());
            let metadata = Arc::new(MetadataStore::open(&meta_dir).unwrap());
            let mgr = Arc::new(ChunkManager::new(
                backend,
                metadata.clone(),
                ChunkManagerConfig {
                    disk_usage_threshold: 1.0,
                    ..ChunkManagerConfig::default()
                },
            ));
            let vfs = Vfs::with_metadata(mgr, Some(metadata), "persist-test/");

            let file = vfs.create_file(ROOT_INO, "checkpoint.pt", 0o644).unwrap();
            vfs.write(file.ino, 0, b"model weights v1").unwrap();

            vfs.mkdir(ROOT_INO, "subdir", 0o755).unwrap();
        }

        // Session 2: reopen and verify everything survived.
        {
            let backend = Box::new(PosixBackend::new(&data_dir).unwrap());
            let metadata = Arc::new(MetadataStore::open(&meta_dir).unwrap());
            let mgr = Arc::new(ChunkManager::new(
                backend,
                metadata.clone(),
                ChunkManagerConfig {
                    disk_usage_threshold: 1.0,
                    ..ChunkManagerConfig::default()
                },
            ));
            let vfs = Vfs::with_metadata(mgr, Some(metadata), "persist-test/");

            // Root should exist.
            let root = vfs.getattr(ROOT_INO).unwrap();
            assert_eq!(root.file_type, FileType::Directory);

            // File should exist with data.
            let file = vfs.lookup(ROOT_INO, "checkpoint.pt").unwrap();
            assert_eq!(file.file_type, FileType::Regular);
            assert_eq!(file.size, 16); // "model weights v1"

            let data = vfs.read(file.ino, 0, 1024).unwrap();
            assert_eq!(&data, b"model weights v1");

            // Directory should exist.
            let dir = vfs.lookup(ROOT_INO, "subdir").unwrap();
            assert_eq!(dir.file_type, FileType::Directory);

            // Filehandle resolution should work (O(1) via fh_map).
            let fh = vfs.ino_to_filehandle(file.ino);
            let resolved = vfs.filehandle_to_ino(&fh).unwrap();
            assert_eq!(resolved, file.ino);
        }
    }

    #[test]
    fn write_at_offset() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");

        let file = vfs.create_file(ROOT_INO, "offset.txt", 0o644).unwrap();
        vfs.write(file.ino, 0, b"AAAA").unwrap();
        vfs.write(file.ino, 2, b"BB").unwrap();

        let data = vfs.read(file.ino, 0, 100).unwrap();
        assert_eq!(&data, b"AABB");
    }

    #[test]
    fn lookupp_from_root() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        // Parent of root is root.
        let attrs = vfs.lookupp(ROOT_INO).unwrap();
        assert_eq!(attrs.ino, ROOT_INO);
    }

    #[test]
    fn lookupp_from_subdir() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let dir = vfs.mkdir(ROOT_INO, "sub", 0o755).unwrap();
        let parent = vfs.lookupp(dir.ino).unwrap();
        assert_eq!(parent.ino, ROOT_INO);
    }

    #[test]
    fn symlink_create_readlink() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let link = vfs
            .create_symlink(ROOT_INO, "mylink", "/target/path")
            .unwrap();
        assert_eq!(link.file_type, FileType::Symlink);
        assert_eq!(link.size, 12); // "/target/path".len()
        let target = vfs.readlink(link.ino).unwrap();
        assert_eq!(target, "/target/path");
    }

    #[test]
    fn readlink_on_non_symlink_fails() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "file.txt", 0o644).unwrap();
        assert!(vfs.readlink(file.ino).is_err());
    }

    #[test]
    fn hardlink() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "original", 0o644).unwrap();
        vfs.write(file.ino, 0, b"data").unwrap();

        let linked = vfs.link(file.ino, ROOT_INO, "hardlink").unwrap();
        assert_eq!(linked.ino, file.ino);
        assert_eq!(linked.nlink, 2);

        // Both names resolve to same ino.
        let a = vfs.lookup(ROOT_INO, "original").unwrap();
        let b = vfs.lookup(ROOT_INO, "hardlink").unwrap();
        assert_eq!(a.ino, b.ino);

        // Unlink one, other still works.
        vfs.unlink(ROOT_INO, "original").unwrap();
        let still = vfs.lookup(ROOT_INO, "hardlink").unwrap();
        assert_eq!(still.ino, file.ino);
    }

    #[test]
    fn lock_read_read_no_conflict() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "f.txt", 0o644).unwrap();
        vfs.lock(file.ino, 0, 100, LockType::ReadLock, b"owner1", 1)
            .unwrap();
        vfs.lock(file.ino, 0, 100, LockType::ReadLock, b"owner2", 2)
            .unwrap();
    }

    #[test]
    fn lock_write_conflict() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "f.txt", 0o644).unwrap();
        vfs.lock(file.ino, 0, 100, LockType::WriteLock, b"owner1", 1)
            .unwrap();
        assert!(vfs
            .lock(file.ino, 50, 100, LockType::WriteLock, b"owner2", 2)
            .is_err());
    }

    #[test]
    fn lock_test_returns_conflict() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "f.txt", 0o644).unwrap();
        vfs.lock(file.ino, 0, 100, LockType::WriteLock, b"owner1", 1)
            .unwrap();
        let conflict = vfs.lock_test(file.ino, 50, 50, LockType::ReadLock, b"owner2");
        assert!(conflict.is_some());
        assert_eq!(conflict.unwrap().owner, b"owner1");
    }

    #[test]
    fn unlock_releases_lock() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "f.txt", 0o644).unwrap();
        vfs.lock(file.ino, 0, 100, LockType::WriteLock, b"owner1", 1)
            .unwrap();
        vfs.unlock(file.ino, 0, 100, b"owner1").unwrap();
        // Now should be able to lock with different owner.
        vfs.lock(file.ino, 0, 100, LockType::WriteLock, b"owner2", 2)
            .unwrap();
    }

    #[test]
    fn release_locks_by_owner() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let f1 = vfs.create_file(ROOT_INO, "f1.txt", 0o644).unwrap();
        let f2 = vfs.create_file(ROOT_INO, "f2.txt", 0o644).unwrap();
        vfs.lock(f1.ino, 0, 100, LockType::WriteLock, b"owner1", 1)
            .unwrap();
        vfs.lock(f2.ino, 0, 100, LockType::WriteLock, b"owner1", 1)
            .unwrap();
        vfs.release_locks_by_owner(b"owner1");
        // Both files should now be lockable by another owner.
        vfs.lock(f1.ino, 0, 100, LockType::WriteLock, b"owner2", 2)
            .unwrap();
        vfs.lock(f2.ino, 0, 100, LockType::WriteLock, b"owner2", 2)
            .unwrap();
    }

    #[test]
    fn permission_check_owner_read() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "priv.txt", 0o640).unwrap();
        // Owner (uid=0) can read.
        assert!(vfs.check_permission(file.ino, 0, 0, 4).is_ok());
        // Other user can't read (other bits = 0).
        assert!(vfs.check_permission(file.ino, 1000, 1000, 4).is_err());
        // Group can read (group bits = 4).
        assert!(vfs.check_permission(file.ino, 1000, 0, 4).is_ok());
    }

    #[test]
    fn concurrent_file_creation() {
        let (_tmp, mgr) = setup();
        let vfs = Arc::new(Vfs::new(mgr, "test/"));

        // Create 100 files concurrently from 10 threads.
        let mut handles = Vec::new();
        for t in 0..10 {
            let vfs = vfs.clone();
            handles.push(std::thread::spawn(move || {
                for i in 0..10 {
                    let name = format!("thread{t}_file{i}.txt");
                    vfs.create_file(ROOT_INO, &name, 0o644).expect("create");
                }
            }));
        }
        for h in handles {
            h.join().expect("thread");
        }

        // Verify all 100 files exist.
        let (entries, _) = vfs.readdir(ROOT_INO, 0).expect("readdir");
        // entries includes "." and "..", so expect 102.
        assert_eq!(entries.len(), 102);
    }

    #[test]
    fn concurrent_write_read() {
        let (_tmp, mgr) = setup();
        let vfs = Arc::new(Vfs::new(mgr, "test/"));
        let file = vfs
            .create_file(ROOT_INO, "concurrent.bin", 0o644)
            .expect("create");

        // Write initial data.
        vfs.write(file.ino, 0, &[0u8; 4096]).expect("write");

        // 10 readers + 1 writer concurrently.
        let mut handles = Vec::new();
        let vfs_w = vfs.clone();
        let ino = file.ino;
        handles.push(std::thread::spawn(move || {
            for i in 0..100 {
                let data = vec![(i % 256) as u8; 128];
                vfs_w.write(ino, 0, &data).expect("write");
            }
        }));

        for _ in 0..10 {
            let vfs_r = vfs.clone();
            handles.push(std::thread::spawn(move || {
                for _ in 0..100 {
                    let _ = vfs_r.read(ino, 0, 4096);
                    // Read may fail transiently during writes, that's OK.
                }
            }));
        }

        for h in handles {
            h.join().expect("thread");
        }

        // File should still be readable.
        let data = vfs.read(file.ino, 0, 4096).expect("read");
        assert!(!data.is_empty());
    }

    #[test]
    fn concurrent_lock_contention() {
        let (_tmp, mgr) = setup();
        let vfs = Arc::new(Vfs::new(mgr, "test/"));
        let file = vfs
            .create_file(ROOT_INO, "locked.bin", 0o644)
            .expect("create");

        // 10 threads try to acquire a write lock on the same range.
        // Only one should succeed at a time.
        let success_count = Arc::new(std::sync::atomic::AtomicU32::new(0));
        let mut handles = Vec::new();

        for t in 0..10 {
            let vfs = vfs.clone();
            let count = success_count.clone();
            let ino = file.ino;
            handles.push(std::thread::spawn(move || {
                let owner = format!("owner-{t}");
                match vfs.lock(ino, 0, 100, LockType::WriteLock, owner.as_bytes(), t as u64) {
                    Ok(()) => {
                        count.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                        // Hold lock briefly.
                        std::thread::sleep(std::time::Duration::from_millis(1));
                        let _ = vfs.unlock(ino, 0, 100, owner.as_bytes());
                    }
                    Err(_) => {} // Expected — lock contention.
                }
            }));
        }

        for h in handles {
            h.join().expect("thread");
        }

        // At least one thread should have succeeded.
        assert!(success_count.load(std::sync::atomic::Ordering::Relaxed) >= 1);
    }

    #[test]
    fn permission_check_root_bypasses() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "restricted.txt", 0o000).unwrap();
        // Root can always access.
        assert!(vfs.check_permission(file.ino, 0, 0, 7).is_ok());
    }

    #[test]
    fn hardlink_to_directory_fails() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let dir = vfs.mkdir(ROOT_INO, "mydir", 0o755).unwrap();
        assert!(vfs.link(dir.ino, ROOT_INO, "dirlink").is_err());
    }

    #[test]
    fn copy_file_full() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let src = vfs.create_file(ROOT_INO, "src.pt", 0o644).unwrap();
        vfs.write(src.ino, 0, b"model weights v1").unwrap();
        let dst = vfs.create_file(ROOT_INO, "dst.pt", 0o644).unwrap();

        let copied = vfs.copy_file(src.ino, 0, dst.ino, 0, 0).unwrap();
        assert_eq!(copied, 16);

        let data = vfs.read(dst.ino, 0, 1024).unwrap();
        assert_eq!(&data, b"model weights v1");
    }

    #[test]
    fn copy_file_partial() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let src = vfs.create_file(ROOT_INO, "src.bin", 0o644).unwrap();
        vfs.write(src.ino, 0, b"AABBCCDD").unwrap();
        let dst = vfs.create_file(ROOT_INO, "dst.bin", 0o644).unwrap();

        let copied = vfs.copy_file(src.ino, 2, dst.ino, 0, 4).unwrap();
        assert_eq!(copied, 4);

        let data = vfs.read(dst.ino, 0, 100).unwrap();
        assert_eq!(&data, b"BBCC");
    }

    #[test]
    fn seek_data_and_hole() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "data.bin", 0o644).unwrap();
        vfs.write(file.ino, 0, b"12345").unwrap();

        // SEEK_DATA at offset 0 → returns 0 (data here)
        let (eof, off) = vfs.seek(file.ino, 0, 0).unwrap();
        assert!(!eof);
        assert_eq!(off, 0);

        // SEEK_HOLE → returns file size (no holes)
        let (_, off) = vfs.seek(file.ino, 0, 1).unwrap();
        assert_eq!(off, 5);
    }

    #[test]
    fn allocate_extends_file() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "alloc.bin", 0o644).unwrap();
        vfs.allocate(file.ino, 0, 1024).unwrap();
        let attrs = vfs.getattr(file.ino).unwrap();
        assert_eq!(attrs.size, 1024);
    }

    #[test]
    fn set_mode() {
        let (_tmp, mgr) = setup();
        let vfs = Vfs::new(mgr, "test/");
        let file = vfs.create_file(ROOT_INO, "perm.txt", 0o644).unwrap();
        assert_eq!(vfs.getattr(file.ino).unwrap().mode, 0o644);
        vfs.set_mode(file.ino, 0o755);
        assert_eq!(vfs.getattr(file.ino).unwrap().mode, 0o755);
        vfs.set_mode(file.ino, 0o600);
        assert_eq!(vfs.getattr(file.ino).unwrap().mode, 0o600);
    }

    #[test]
    fn set_mode_persists() {
        let tmp = TempDir::new().unwrap();
        let data_dir = tmp.path().join("data");
        let meta_dir = tmp.path().join("meta");
        {
            let backend = Box::new(PosixBackend::new(&data_dir).unwrap());
            let metadata = Arc::new(MetadataStore::open(&meta_dir).unwrap());
            let mgr = Arc::new(ChunkManager::new(
                backend,
                metadata.clone(),
                ChunkManagerConfig {
                    disk_usage_threshold: 1.0,
                    ..ChunkManagerConfig::default()
                },
            ));
            let vfs = Vfs::with_metadata(mgr, Some(metadata), "mode-test/");
            let file = vfs.create_file(ROOT_INO, "f.txt", 0o644).unwrap();
            vfs.set_mode(file.ino, 0o755);
        }
        {
            let backend = Box::new(PosixBackend::new(&data_dir).unwrap());
            let metadata = Arc::new(MetadataStore::open(&meta_dir).unwrap());
            let mgr = Arc::new(ChunkManager::new(
                backend,
                metadata.clone(),
                ChunkManagerConfig {
                    disk_usage_threshold: 1.0,
                    ..ChunkManagerConfig::default()
                },
            ));
            let vfs = Vfs::with_metadata(mgr, Some(metadata), "mode-test/");
            let file = vfs.lookup(ROOT_INO, "f.txt").unwrap();
            assert_eq!(file.mode, 0o755);
        }
    }

    // -----------------------------------------------------------------
    // NFS file content over PmStore
    // -----------------------------------------------------------------

    #[test]
    fn pm_write_read_roundtrip() {
        let (_tmp, vfs) = setup_with_pm();
        let attrs = vfs.create_file(ROOT_INO, "pm.bin", 0o644).unwrap();
        let n = vfs.write(attrs.ino, 0, b"hello pm").unwrap();
        assert_eq!(n, 8);

        let data = vfs.read(attrs.ino, 0, 8).unwrap();
        assert_eq!(&data, b"hello pm");

        let updated = vfs.getattr(attrs.ino).unwrap();
        assert_eq!(updated.size, 8);
    }

    #[test]
    fn pm_write_at_offset_does_not_amplify() {
        // The point of the position-mapped store: a 1 KiB write at offset 4 MiB on a
        // sparse file touches only those 1 KiB. The legacy CA-chunk
        // path would have read 4+ MiB, hashed it, and re-written the
        // whole thing. We don't have a perf assertion here — but we
        // assert the two reads (0..4096 should be all zeros, the
        // written region should be the data) which can only happen
        // if the underlying volume is sparse, not chunked-and-rewritten.
        let (_tmp, vfs) = setup_with_pm();
        let attrs = vfs.create_file(ROOT_INO, "sparse.bin", 0o644).unwrap();

        let payload = vec![0xAB; 1024];
        vfs.write(attrs.ino, 4 * 1024 * 1024, &payload).unwrap();

        // Region beyond the write but within file size: zero.
        let zeros = vfs.read(attrs.ino, 0, 4096).unwrap();
        assert!(
            zeros.iter().all(|&b| b == 0),
            "sparse hole must read as zero"
        );

        // Written region: what we wrote.
        let payload_back = vfs.read(attrs.ino, 4 * 1024 * 1024, 1024).unwrap();
        assert_eq!(payload_back, payload);

        let updated = vfs.getattr(attrs.ino).unwrap();
        assert_eq!(updated.size, 4 * 1024 * 1024 + 1024);
    }

    #[test]
    fn pm_truncate_grows_then_shrinks() {
        let (_tmp, vfs) = setup_with_pm();
        let attrs = vfs.create_file(ROOT_INO, "trunc.bin", 0o644).unwrap();

        vfs.write(attrs.ino, 0, &[0xCC; 4096]).unwrap();
        assert_eq!(vfs.getattr(attrs.ino).unwrap().size, 4096);

        // Grow.
        vfs.setattr(attrs.ino, Some(8192)).unwrap();
        assert_eq!(vfs.getattr(attrs.ino).unwrap().size, 8192);

        // Shrink — bytes past 1024 must be gone.
        vfs.setattr(attrs.ino, Some(1024)).unwrap();
        assert_eq!(vfs.getattr(attrs.ino).unwrap().size, 1024);

        // Re-read short of the new size.
        let data = vfs.read(attrs.ino, 0, 1024).unwrap();
        assert_eq!(data.len(), 1024);
        assert!(data.iter().all(|&b| b == 0xCC));

        // Truncate to zero deletes the volume; subsequent reads yield empty.
        vfs.setattr(attrs.ino, Some(0)).unwrap();
        assert_eq!(vfs.getattr(attrs.ino).unwrap().size, 0);
        let empty = vfs.read(attrs.ino, 0, 1024).unwrap();
        assert!(empty.is_empty());
    }

    #[test]
    fn fsync_flushes_written_file() {
        // COMMIT / fsync must succeed on a written PmStore-backed file
        // (previously a no-op that returned Ok without flushing) and
        // the data must round-trip. We can't unit-test power-loss
        // durability, but this proves the flush path is wired and
        // doesn't error on a real volume.
        let (_tmp, vfs) = setup_with_pm();
        let attrs = vfs.create_file(ROOT_INO, "commit.bin", 0o644).unwrap();
        vfs.write(attrs.ino, 0, b"durable bytes").unwrap();
        vfs.fsync(attrs.ino).expect("fsync/commit must succeed");
        let data = vfs.read(attrs.ino, 0, 13).unwrap();
        assert_eq!(&data, b"durable bytes");
    }

    #[test]
    fn fsync_missing_inode_errors() {
        let (_tmp, vfs) = setup_with_pm();
        assert!(
            vfs.fsync(999_999).is_err(),
            "fsync of unknown inode must error"
        );
    }

    #[test]
    fn fsync_before_any_write_is_ok() {
        // A file created but never written has no backing volume yet;
        // COMMIT on it must not error (nothing to flush).
        let (_tmp, vfs) = setup_with_pm();
        let attrs = vfs.create_file(ROOT_INO, "empty.bin", 0o644).unwrap();
        vfs.fsync(attrs.ino)
            .expect("commit on unwritten file is a no-op ok");
    }

    #[test]
    fn concurrent_writes_grow_size_monotonically() {
        // Regression for the non-monotonic size race: two writes at
        // different offsets, the higher one committed first. The
        // recorded size must reflect the true high-water mark so the
        // low write's region stays readable and the high write's tail
        // is not clipped. We drive it sequentially in worst-case order
        // (high offset write lands, then low offset write) — the old
        // `entry.size = new_size` would clobber the size down to the
        // low write's end; the `.max` keeps it at the high-water mark.
        let (_tmp, vfs) = setup_with_pm();
        let attrs = vfs.create_file(ROOT_INO, "grow.bin", 0o644).unwrap();
        // High-offset write first: file grows to 20 MiB + 4.
        vfs.write(attrs.ino, 20 * 1024 * 1024, b"tail").unwrap();
        let hi = vfs.getattr(attrs.ino).unwrap().size;
        assert_eq!(hi, 20 * 1024 * 1024 + 4);
        // Low-offset write second: must NOT shrink the recorded size.
        vfs.write(attrs.ino, 0, b"head").unwrap();
        let after = vfs.getattr(attrs.ino).unwrap().size;
        assert_eq!(after, hi, "low write must not clobber the high-water size");
        // The tail written earlier is still readable.
        let tail = vfs.read(attrs.ino, 20 * 1024 * 1024, 4).unwrap();
        assert_eq!(&tail, b"tail");
    }

    #[test]
    fn pm_unlink_removes_volume() {
        let (_tmp, vfs) = setup_with_pm();
        let attrs = vfs.create_file(ROOT_INO, "drop.bin", 0o644).unwrap();
        vfs.write(attrs.ino, 0, b"data").unwrap();
        vfs.unlink(ROOT_INO, "drop.bin").unwrap();
        // Looking up the inode after unlink must fail.
        assert!(vfs.getattr(attrs.ino).is_err());
    }

    /// COMMIT reaches `Vfs::fsync` through the backend dispatcher: the
    /// synthetic arm used to return Ok without flushing PmStore and for
    /// inodes that do not exist.
    #[test]
    fn backend_commit_flushes_synthetic_vfs() {
        let (_tmp, vfs) = setup_with_pm();
        let backend = crate::vfs_backend::VfsBackend::Synthetic(vfs.clone());
        let file = vfs.create_file(ROOT_INO, "c.bin", 0o644).unwrap();
        vfs.write(file.ino, 0, b"durable").unwrap();
        backend.fsync(file.ino).expect("COMMIT of a written file");
        assert!(
            backend.fsync(999_999).is_err(),
            "COMMIT of an unknown inode must fail"
        );
    }
}
