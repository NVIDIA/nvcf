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

//! The file list a peer export serves under reserved keys.
//!
//! An export serves two listings of a directory: version 3 under
//! [`key_v3`] (`/manifest3:<prefix>`: files and directories with their
//! attributes) and version 2 under [`key`] (`/manifest:<prefix>`: files
//! only, without attributes), so a fetcher that knows only version 2
//! keeps working against a newer export. A fetcher that wants attributes
//! asks for the version 3 key; an older export answers it NOT_FOUND
//! (`/manifest3:` is not a file key either). Upgrade sources first.
//!
//! Wire format (little-endian), version 3:
//!
//! ```text
//! magic "BRPM" | version u32 = 3 | authority u32 | count u64
//! count x { path_len u16 | path (UTF-8) | kind u8 (0 = file, 1 = directory)
//!           | mode u32 | mtime_sec i64 | mtime_nsec u32 | uid u32 | gid u32
//!           | file only: size u64 | generation u128 | content u128 }
//! ```
//!
//! `authority` says which attributes the listed tree really has: bit 0
//! (`AUTH_MODES`) its modes and mtimes, bit 1 (`AUTH_OWNERS`) its owners
//! and groups; other bits are refused. An original tree has both. A tree
//! that a fetch produced without applying them (a copy made with
//! `--no-attrs`, or without the right to chown) clears the bit, so a
//! fetch from it does not take the copier's uid, or a private 0600, for
//! the origin's (see `crate::content`).
//!
//! A file entry is at least 68 bytes, a directory entry at least 28.
//! `mode` is the permission, set-id and sticky bits of `st_mode` only
//! (`& 0o7777`; the decoder refuses any other bit), `mtime_nsec` is below
//! one second. Directories are every directory below the listed one (not
//! the listed directory itself: a fetch's destination root belongs to
//! whoever runs the fetch), so a fetch can recreate empty directories and
//! give each its mode, mtime and owner.
//!
//! Versions 1 and 2 (files only, no attributes) are still decoded:
//!
//! ```text
//! version 2: count x { path_len u16 | path | size u64 | generation u128 | content u128 }
//! version 1: count x { path_len u16 | path | size u64 | generation u128 }
//! ```
//!
//! (header without `authority`), with `content` set to the generation in
//! version 1 and the attributes zero and not authoritative.
//! [`Manifest::version`] says which one was read, so a caller that needs
//! attributes refuses an older listing rather than applying zeros. A
//! fetched copy's content record (`crate::content`) is written in version
//! 2 ([`encode_v2`]): it carries identities, not attributes.
//!
//! `generation` is the serving node's own version token for the file
//! (from its `fstat`), the one ranges are pinned to; two nodes holding
//! the same bytes have different generations. `content` names the bytes
//! themselves, across nodes: the generation the file had on the node it
//! was first exported from, carried along by every fetch (see
//! `crate::export` for how a replica serves it). A fetch that moves from
//! one source to another compares `size` and `content`, never
//! generations, and never attributes: [`content_digest`] covers file
//! paths, sizes and `content` only, so it is the same for a version 2 and
//! a version 3 listing of the same bytes.
//!
//! Paths are relative to the export root, `/`-separated, with only
//! normal components. The decoder enforces that, for directory entries
//! too, so a client never creates anything outside its destination
//! because a server said so. uid and gid are numbers, applied as they
//! are (like `rsync --numeric-ids`): no name is looked up on either
//! side.

const MAGIC: &[u8; 4] = b"BRPM";
/// The version [`encode`] writes.
pub const VERSION: u32 = 3;
const VERSION_2: u32 = 2;
const VERSION_1: u32 = 1;
/// Smallest encoded entry per version: path length (2) + a 1-byte path,
/// then for version 3 kind (1) + attributes (24), and for files size (8)
/// + generation (16) [+ content (16)].
const MIN_ENTRY_V3: usize = 2 + 1 + 1 + ATTRS_LEN;
const MIN_ENTRY_V2: usize = 2 + 1 + 8 + 16 + 16;
const MIN_ENTRY_V1: usize = 2 + 1 + 8 + 16;
/// Header: magic, version, count; version 3 adds `authority`.
const HEADER_V2: usize = 4 + 4 + 8;
pub const HEADER_V3: usize = HEADER_V2 + 4;
/// `authority` bit: the listed modes and mtimes are the tree's own.
pub const AUTH_MODES: u32 = 1;
/// `authority` bit: the listed owners and groups are the tree's own.
pub const AUTH_OWNERS: u32 = 2;
const ATTRS_LEN: usize = 4 + 8 + 4 + 4 + 4;
/// The bits of `st_mode` a manifest carries.
pub const MODE_BITS: u32 = 0o7777;
const KIND_FILE: u8 = 0;
const KIND_DIR: u8 = 1;

/// What an entry is.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Kind {
    #[default]
    File,
    Dir,
}

/// The attributes a manifest carries for every entry (version 3).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Attrs {
    /// `st_mode & 0o7777`: permission, set-id and sticky bits.
    pub mode: u32,
    pub mtime_sec: i64,
    /// Below 1_000_000_000.
    pub mtime_nsec: u32,
    pub uid: u32,
    pub gid: u32,
}

impl Attrs {
    /// The attributes of a `stat`.
    pub fn of(st: &libc::stat) -> Self {
        Self {
            mode: st.st_mode & MODE_BITS,
            mtime_sec: st.st_mtime,
            // The kernel keeps it in [0, 1e9).
            mtime_nsec: u32::try_from(st.st_mtime_nsec).unwrap_or(0),
            uid: st.st_uid,
            gid: st.st_gid,
        }
    }
}

/// One file or directory under the export.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Entry {
    /// Relative to the export root; also a file's key on the wire.
    pub path: String,
    pub kind: Kind,
    /// 0 for a directory.
    pub size: u64,
    /// The file's version token: changes when it is rewritten or
    /// replaced. Ranges are fetched pinned to it. 0 for a directory.
    pub generation: u128,
    /// Identity of the file's bytes across nodes (see the module docs).
    /// 0 for a directory.
    pub content: u128,
    /// Zero in a version 1 or 2 listing (see [`Manifest::version`]).
    pub attrs: Attrs,
}

impl Entry {
    pub fn is_file(&self) -> bool {
        self.kind == Kind::File
    }
}

/// Which of the listed attributes are the tree's own (see the module
/// docs).
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Authority {
    /// Modes and mtimes.
    pub modes: bool,
    /// Owners and groups.
    pub owners: bool,
}

impl Authority {
    pub const FULL: Authority = Authority {
        modes: true,
        owners: true,
    };
    pub const NONE: Authority = Authority {
        modes: false,
        owners: false,
    };

    fn bits(self) -> u32 {
        (if self.modes { AUTH_MODES } else { 0 }) | (if self.owners { AUTH_OWNERS } else { 0 })
    }

    /// What holds for both.
    pub fn and(self, other: Authority) -> Authority {
        Authority {
            modes: self.modes && other.modes,
            owners: self.owners && other.owners,
        }
    }
}

/// A decoded listing.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Manifest {
    /// The version it was encoded in: below 3, entries are files only
    /// and their `attrs` are zero, not the files' attributes.
    pub version: u32,
    /// Which attributes are the tree's own; none below version 3.
    pub authority: Authority,
    pub entries: Vec<Entry>,
}

impl Manifest {
    /// Whether the entries carry attributes and the directories are
    /// listed (version 3 and later).
    pub fn has_attrs(&self) -> bool {
        self.version >= VERSION
    }

    /// The file entries.
    pub fn files(&self) -> impl Iterator<Item = &Entry> {
        self.entries.iter().filter(|e| e.is_file())
    }
}

/// Key under which the export serves the version 2 manifest of `prefix`
/// (a directory relative to the root; empty for the whole export): files
/// only, no attributes; what every nvstor-peer since version 2 asks for.
/// File keys are relative paths and never start with `/`, so this cannot
/// collide with a file.
pub fn key(prefix: &str) -> String {
    format!("/manifest:{prefix}")
}

/// Key under which the export serves the version 3 manifest of `prefix`.
pub fn key_v3(prefix: &str) -> String {
    format!("/manifest3:{prefix}")
}

/// The version and prefix a manifest key names, or `None` for a file
/// key.
pub fn listing_of(key: &str) -> Option<(u32, &str)> {
    if let Some(p) = key.strip_prefix("/manifest3:") {
        return Some((VERSION, p));
    }
    key.strip_prefix("/manifest:").map(|p| (VERSION_2, p))
}

/// The prefix a manifest key (of either version) names, or `None` for a
/// file key.
pub fn prefix_of(key: &str) -> Option<&str> {
    listing_of(key).map(|(_, p)| p)
}

/// `path` is relative and made only of normal components, so joining it
/// to a directory stays inside that directory.
/// Checked textually: `Path::components` would quietly drop `.` and
/// repeated slashes.
pub fn is_safe_relative(path: &str) -> bool {
    !path.contains('\0')
        && path
            .split('/')
            .all(|c| !c.is_empty() && c != "." && c != "..")
}

/// Encode `entries` in the current version, attributes and directories
/// included, saying which attributes are the tree's own.
pub fn encode(entries: &[Entry], authority: Authority) -> Vec<u8> {
    let mut out =
        Vec::with_capacity(HEADER_V3 + entries.iter().map(|e| 68 + e.path.len()).sum::<usize>());
    out.extend_from_slice(MAGIC);
    out.extend_from_slice(&VERSION.to_le_bytes());
    out.extend_from_slice(&authority.bits().to_le_bytes());
    out.extend_from_slice(&(entries.len() as u64).to_le_bytes());
    for e in entries {
        out.extend_from_slice(&(e.path.len() as u16).to_le_bytes());
        out.extend_from_slice(e.path.as_bytes());
        out.push(if e.is_file() { KIND_FILE } else { KIND_DIR });
        out.extend_from_slice(&e.attrs.mode.to_le_bytes());
        out.extend_from_slice(&e.attrs.mtime_sec.to_le_bytes());
        out.extend_from_slice(&e.attrs.mtime_nsec.to_le_bytes());
        out.extend_from_slice(&e.attrs.uid.to_le_bytes());
        out.extend_from_slice(&e.attrs.gid.to_le_bytes());
        if e.is_file() {
            out.extend_from_slice(&e.size.to_le_bytes());
            out.extend_from_slice(&e.generation.to_le_bytes());
            out.extend_from_slice(&e.content.to_le_bytes());
        }
    }
    out
}

/// Encode the file entries of `entries` in version 2 (no attributes, no
/// directories): what [`key`] serves, and the format of a fetched copy's
/// content record, which every nvstor-peer since version 2 reads.
pub fn encode_v2(entries: &[Entry]) -> Vec<u8> {
    let files: Vec<&Entry> = entries.iter().filter(|e| e.is_file()).collect();
    let mut out = Vec::with_capacity(16 + files.iter().map(|e| 42 + e.path.len()).sum::<usize>());
    out.extend_from_slice(MAGIC);
    out.extend_from_slice(&VERSION_2.to_le_bytes());
    out.extend_from_slice(&(files.len() as u64).to_le_bytes());
    for e in files {
        out.extend_from_slice(&(e.path.len() as u16).to_le_bytes());
        out.extend_from_slice(e.path.as_bytes());
        out.extend_from_slice(&e.size.to_le_bytes());
        out.extend_from_slice(&e.generation.to_le_bytes());
        out.extend_from_slice(&e.content.to_le_bytes());
    }
    out
}

/// SHA-256 over every file entry's path, size and content, in path
/// order: equal for two manifests of the same bytes under the same
/// names, from whichever node, in whichever version, and printed by
/// `fetch` so copies can be compared. Directories and attributes are not
/// part of it: they say nothing about the bytes, and a copy made before
/// attributes were listed must still match one made after.
pub fn content_digest(entries: &[Entry]) -> String {
    use sha2::{Digest, Sha256};
    let mut sorted: Vec<&Entry> = entries.iter().filter(|e| e.is_file()).collect();
    sorted.sort_by(|a, b| a.path.cmp(&b.path));
    let mut h = Sha256::new();
    for e in sorted {
        h.update((e.path.len() as u64).to_le_bytes());
        h.update(e.path.as_bytes());
        h.update(e.size.to_le_bytes());
        h.update(e.content.to_le_bytes());
    }
    h.finalize().iter().map(|b| format!("{b:02x}")).collect()
}

pub fn decode(bytes: &[u8]) -> Result<Manifest, String> {
    let mut r = Reader { bytes, at: 0 };
    if r.take(4)? != MAGIC {
        return Err("not an nvstor-peer manifest".into());
    }
    let version = u32::from_le_bytes(r.array()?);
    let min_entry = match version {
        VERSION => MIN_ENTRY_V3,
        VERSION_2 => MIN_ENTRY_V2,
        VERSION_1 => MIN_ENTRY_V1,
        _ => {
            return Err(format!(
                "manifest version {version}, expected {VERSION_1} to {VERSION}"
            ))
        }
    };
    let authority = if version == VERSION {
        let bits = u32::from_le_bytes(r.array()?);
        if bits & !(AUTH_MODES | AUTH_OWNERS) != 0 {
            return Err(format!("manifest authority {bits:#x} has unknown bits"));
        }
        Authority {
            modes: bits & AUTH_MODES != 0,
            owners: bits & AUTH_OWNERS != 0,
        }
    } else {
        Authority::NONE
    };
    let count = u64::from_le_bytes(r.array()?);
    // Reject counts the input cannot hold before allocating for them.
    if count > (bytes.len() / min_entry) as u64 {
        return Err(format!(
            "manifest claims {count} entries in {} bytes",
            bytes.len()
        ));
    }
    let mut entries = Vec::with_capacity(count as usize);
    for _ in 0..count {
        let len = u16::from_le_bytes(r.array()?) as usize;
        let path = std::str::from_utf8(r.take(len)?)
            .map_err(|_| "manifest path is not UTF-8".to_string())?
            .to_string();
        if !is_safe_relative(&path) {
            return Err(format!(
                "manifest path {path:?} is not a plain relative path"
            ));
        }
        let (kind, attrs) = if version == VERSION {
            let kind = match r.array::<1>()?[0] {
                KIND_FILE => Kind::File,
                KIND_DIR => Kind::Dir,
                k => return Err(format!("manifest entry {path:?} has unknown kind {k}")),
            };
            let attrs = Attrs {
                mode: u32::from_le_bytes(r.array()?),
                mtime_sec: i64::from_le_bytes(r.array()?),
                mtime_nsec: u32::from_le_bytes(r.array()?),
                uid: u32::from_le_bytes(r.array()?),
                gid: u32::from_le_bytes(r.array()?),
            };
            if attrs.mode & !MODE_BITS != 0 {
                return Err(format!(
                    "manifest entry {path:?} has mode {:#o}, beyond {MODE_BITS:#o}",
                    attrs.mode
                ));
            }
            if attrs.mtime_nsec >= 1_000_000_000 {
                return Err(format!(
                    "manifest entry {path:?} has {} ns in its mtime",
                    attrs.mtime_nsec
                ));
            }
            (kind, attrs)
        } else {
            (Kind::File, Attrs::default())
        };
        let e = if kind == Kind::Dir {
            Entry {
                path,
                kind,
                size: 0,
                generation: 0,
                content: 0,
                attrs,
            }
        } else {
            let size = u64::from_le_bytes(r.array()?);
            let generation = u128::from_le_bytes(r.array()?);
            let content = if version == VERSION_1 {
                generation
            } else {
                u128::from_le_bytes(r.array()?)
            };
            Entry {
                path,
                kind,
                size,
                generation,
                content,
                attrs,
            }
        };
        entries.push(e);
    }
    if r.at != bytes.len() {
        return Err(format!(
            "{} trailing bytes after the manifest",
            bytes.len() - r.at
        ));
    }
    Ok(Manifest {
        version,
        authority,
        entries,
    })
}

struct Reader<'a> {
    bytes: &'a [u8],
    at: usize,
}

impl<'a> Reader<'a> {
    fn take(&mut self, n: usize) -> Result<&'a [u8], String> {
        let end = self
            .at
            .checked_add(n)
            .filter(|&e| e <= self.bytes.len())
            .ok_or_else(|| "manifest is truncated".to_string())?;
        let s = &self.bytes[self.at..end];
        self.at = end;
        Ok(s)
    }

    fn array<const N: usize>(&mut self) -> Result<[u8; N], String> {
        let s = self.take(N)?;
        let mut a = [0u8; N];
        a.copy_from_slice(s);
        Ok(a)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn attrs(n: u32) -> Attrs {
        let n = n % 1000;
        Attrs {
            mode: 0o4000 | (n & 0o777),
            mtime_sec: -(n as i64) * 1_000_003,
            mtime_nsec: 999_999_999 - n,
            uid: u32::MAX - n,
            gid: 100 + n,
        }
    }

    fn e(path: &str, size: u64, generation: u128) -> Entry {
        Entry {
            path: path.into(),
            kind: Kind::File,
            size,
            generation,
            content: generation ^ 0x5a,
            attrs: attrs(size as u32),
        }
    }

    fn dir(path: &str, n: u32) -> Entry {
        Entry {
            path: path.into(),
            kind: Kind::Dir,
            attrs: attrs(n),
            ..Entry::default()
        }
    }

    /// A version 2 listing of `files`, byte for byte as version 2 wrote it.
    fn v2(files: &[(&str, u64, u128, u128)]) -> Vec<u8> {
        let mut b = Vec::new();
        b.extend_from_slice(MAGIC);
        b.extend_from_slice(&2u32.to_le_bytes());
        b.extend_from_slice(&(files.len() as u64).to_le_bytes());
        for &(p, size, generation, content) in files {
            b.extend_from_slice(&(p.len() as u16).to_le_bytes());
            b.extend_from_slice(p.as_bytes());
            b.extend_from_slice(&size.to_le_bytes());
            b.extend_from_slice(&generation.to_le_bytes());
            b.extend_from_slice(&content.to_le_bytes());
        }
        b
    }

    #[test]
    fn round_trips_every_field_of_files_and_directories() {
        let entries = vec![
            e("a", 0, 1),
            dir("dir", 0o750),
            e("dir/b.img", 1 << 40, u128::MAX),
            dir("dir/empty", 7),
            e("ünï/c", 7, 9),
            Entry {
                attrs: Attrs {
                    mode: 0o7777,
                    mtime_sec: i64::MIN,
                    mtime_nsec: 0,
                    uid: 0,
                    gid: 0,
                },
                ..e("max", u64::MAX, 3)
            },
        ];
        let m = decode(&encode(&entries, Authority::FULL)).expect("decode");
        assert_eq!((m.version, m.authority), (VERSION, Authority::FULL));
        assert!(m.has_attrs());
        assert_eq!(m.entries, entries);
        assert_eq!(m.files().count(), 4);
        let empty = decode(&encode(&[], Authority::FULL)).expect("empty");
        assert_eq!((empty.version, empty.entries), (VERSION, vec![]));
    }

    #[test]
    fn short_entries_round_trip_and_the_count_is_bounded() {
        // One-byte paths make the smallest entries (68 bytes for a file,
        // 28 for a directory); the count bound must not refuse them.
        let es: Vec<Entry> = ["a", "b", "c", "d"].iter().map(|p| e(p, 1, 2)).collect();
        let bytes = encode(&es, Authority::FULL);
        assert_eq!(bytes.len(), HEADER_V3 + 4 * 68);
        assert_eq!(decode(&bytes).expect("decode").entries, es);
        let ds: Vec<Entry> = ["a", "b", "c", "d"].iter().map(|p| dir(p, 1)).collect();
        let bytes = encode(&ds, Authority::FULL);
        assert_eq!(bytes.len(), HEADER_V3 + 4 * MIN_ENTRY_V3);
        assert_eq!(decode(&bytes).expect("decode").entries, ds);
        // One more entry than the bytes can hold is still refused.
        let mut over = bytes.clone();
        over[12..20].copy_from_slice(&5u64.to_le_bytes());
        assert!(decode(&over).is_err());
    }

    #[test]
    fn decodes_versions_1_and_2_as_files_without_attributes() {
        let mut v1 = Vec::new();
        v1.extend_from_slice(MAGIC);
        v1.extend_from_slice(&1u32.to_le_bytes());
        v1.extend_from_slice(&1u64.to_le_bytes());
        v1.extend_from_slice(&3u16.to_le_bytes());
        v1.extend_from_slice(b"a/b");
        v1.extend_from_slice(&7u64.to_le_bytes());
        v1.extend_from_slice(&42u128.to_le_bytes());
        let got = decode(&v1).expect("v1");
        assert_eq!(got.version, 1);
        assert!(!got.has_attrs());
        assert_eq!(
            got.entries,
            vec![Entry {
                path: "a/b".into(),
                size: 7,
                generation: 42,
                content: 42,
                ..Entry::default()
            }]
        );
        let got = decode(&v2(&[("x", 1, 2, 3)])).expect("v2");
        assert_eq!(got.version, 2);
        assert!(!got.has_attrs());
        assert_eq!(
            got.entries,
            vec![Entry {
                path: "x".into(),
                size: 1,
                generation: 2,
                content: 3,
                ..Entry::default()
            }]
        );
        // A short v2 entry (43 bytes) is not refused by the count bound.
        assert_eq!(v2(&[("a", 1, 1, 1)]).len(), 16 + MIN_ENTRY_V2);
    }

    #[test]
    fn the_content_record_is_version_2_without_directories() {
        let entries = vec![e("a", 1, 2), dir("d", 1), e("d/b", 3, 4)];
        let bytes = encode_v2(&entries);
        assert_eq!(bytes, v2(&[("a", 1, 2, 2 ^ 0x5a), ("d/b", 3, 4, 4 ^ 0x5a)]));
        let m = decode(&bytes).expect("record");
        assert_eq!(m.version, 2);
        assert_eq!(
            m.entries
                .iter()
                .map(|x| (x.path.as_str(), x.size, x.generation, x.content))
                .collect::<Vec<_>>(),
            [("a", 1, 2, 2 ^ 0x5a), ("d/b", 3, 4, 4 ^ 0x5a)]
        );
    }

    #[test]
    fn content_digest_ignores_generations_and_order() {
        let a = vec![e("x", 1, 1), e("y", 2, 2)];
        let mut b: Vec<Entry> = a.iter().rev().cloned().collect();
        for x in &mut b {
            x.generation += 100;
        }
        assert_eq!(content_digest(&a), content_digest(&b));
        b[0].content += 1;
        assert_ne!(content_digest(&a), content_digest(&b));
        let mut c = a.clone();
        c[1].size += 1;
        assert_ne!(content_digest(&a), content_digest(&c));
    }

    #[test]
    fn content_digest_ignores_attributes_and_directories() {
        // A copy made from a version 2 source (before attributes) and one
        // made from a version 3 source of the same bytes must agree, or
        // fan-out failover between them would refuse.
        let old = decode(&v2(&[
            ("a", 7, 1, 0x11),
            ("d/b", 1 << 33, 2, (1 << 127) + 5),
        ]))
        .expect("v2");
        let new = vec![
            dir("d", 0o700),
            Entry {
                path: "d/b".into(),
                size: 1 << 33,
                generation: 99,
                content: (1 << 127) + 5,
                attrs: attrs(3),
                ..Entry::default()
            },
            dir("d/empty", 0o555),
            Entry {
                path: "a".into(),
                size: 7,
                generation: 98,
                content: 0x11,
                attrs: attrs(4),
                ..Entry::default()
            },
        ];
        let new = decode(&encode(&new, Authority::FULL)).expect("v3");
        assert_eq!(content_digest(&old.entries), content_digest(&new.entries));
        // Pinned: the digest of this listing as every version computes it.
        assert_eq!(
            content_digest(&new.entries),
            "c4ada268414896fa2e5f824e9cde036a7ff3dae0bc3d0ef9013d799d573e2e5b"
        );
        let mut other = new.entries.clone();
        for x in &mut other {
            x.attrs = Attrs::default();
        }
        other.push(dir("more", 1));
        assert_eq!(content_digest(&other), content_digest(&new.entries));
    }

    #[test]
    fn rejects_malformed_input() {
        let good = encode(&[e("a/b", 1, 2), dir("a", 0o755)], Authority::FULL);
        for n in 0..good.len() {
            assert!(decode(&good[..n]).is_err(), "truncated at {n}");
        }
        let mut trailing = good.clone();
        trailing.push(0);
        assert!(decode(&trailing).is_err());
        let mut magic = good.clone();
        magic[0] = b'X';
        assert!(decode(&magic).is_err());
        // A version this decoder does not know (what a version 2 fetch
        // says of a version 3 manifest) is refused, not misparsed.
        let mut version = good.clone();
        version[4] = 4;
        let err = decode(&version).expect_err("version 4");
        assert!(err.contains("version 4"), "{err}");
        let mut huge = good;
        huge[12..20].copy_from_slice(&u64::MAX.to_le_bytes());
        assert!(decode(&huge).is_err());
    }

    #[test]
    fn rejects_absurd_attributes_and_kinds() {
        // Offsets in a one-entry listing: header 20, path_len 2, path 1.
        let kind_at = HEADER_V3 + 2 + 1;
        let mode_at = kind_at + 1;
        let nsec_at = mode_at + 4 + 8;
        let good = encode(&[dir("a", 0o755)], Authority::FULL);
        let mut kind = good.clone();
        kind[kind_at] = 2;
        assert!(decode(&kind).expect_err("kind").contains("kind"));
        for mode in [0o10000u32, 0o100644, u32::MAX] {
            let mut m = good.clone();
            m[mode_at..mode_at + 4].copy_from_slice(&mode.to_le_bytes());
            assert!(decode(&m).expect_err("mode").contains("mode"), "{mode:o}");
        }
        let mut nsec = good.clone();
        nsec[nsec_at..nsec_at + 4].copy_from_slice(&1_000_000_000u32.to_le_bytes());
        assert!(decode(&nsec).expect_err("nsec").contains("ns"));
        // A file entry turned into a directory leaves 40 bytes over.
        let mut as_dir = encode(&[e("a", 1, 1)], Authority::FULL);
        as_dir[kind_at] = KIND_DIR;
        assert!(decode(&as_dir).is_err());
    }

    #[test]
    fn rejects_paths_that_escape() {
        for bad in [
            "",
            "/etc/passwd",
            "../x",
            "a/../../x",
            "a/./b",
            "a//b/..",
            "nul\0",
        ] {
            assert!(!is_safe_relative(bad), "{bad:?}");
            assert!(
                decode(&encode(&[e(bad, 1, 1)], Authority::FULL)).is_err(),
                "{bad:?}"
            );
            assert!(
                decode(&encode(&[dir(bad, 1)], Authority::FULL)).is_err(),
                "dir {bad:?}"
            );
            assert!(decode(&v2(&[(bad, 1, 1, 1)])).is_err(), "v2 {bad:?}");
        }
        for ok in ["a", "a/b", "a.b/c-d_e", ".hidden/x"] {
            assert!(is_safe_relative(ok), "{ok:?}");
        }
    }

    #[test]
    fn authority_round_trips_and_unknown_bits_are_refused() {
        let es = [e("a", 1, 2)];
        for a in [
            Authority::FULL,
            Authority::NONE,
            Authority {
                modes: true,
                owners: false,
            },
            Authority {
                modes: false,
                owners: true,
            },
        ] {
            assert_eq!(decode(&encode(&es, a)).expect("decode").authority, a);
        }
        let mut bad = encode(&es, Authority::FULL);
        bad[8..12].copy_from_slice(&4u32.to_le_bytes());
        assert!(decode(&bad).expect_err("unknown bit").contains("authority"));
        // Older versions carry none.
        assert_eq!(
            decode(&encode_v2(&es)).expect("v2").authority,
            Authority::NONE
        );
        assert_eq!(
            Authority::FULL.and(Authority {
                modes: true,
                owners: false
            }),
            Authority {
                modes: true,
                owners: false
            }
        );
    }

    #[test]
    fn manifest_keys_never_look_like_files() {
        assert_eq!(listing_of(&key("a/b")), Some((2, "a/b")));
        assert_eq!(listing_of(&key_v3("a/b")), Some((3, "a/b")));
        assert_eq!(listing_of(&key_v3("")), Some((3, "")));
        assert_eq!(prefix_of(&key_v3("x")), Some("x"));
        assert!(!is_safe_relative(&key_v3("x")));
        assert_eq!(listing_of("a/b"), None);
        assert_eq!(prefix_of(&key("")), Some(""));
        assert_eq!(prefix_of(&key("ckpt/run1")), Some("ckpt/run1"));
        assert_eq!(prefix_of("ckpt/run1"), None);
        assert!(!is_safe_relative(&key("x")));
    }
}
