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

//! Tests for the pass-through VFS (`passthrough.rs`).

use super::*;
use std::os::unix::fs::symlink;

/// A DIRECT_IO_ALIGN-aligned window of `len` bytes inside `backing`.
fn aligned(backing: &mut Vec<u8>, len: usize) -> &mut [u8] {
    backing.resize(len + DIRECT_IO_ALIGN, 0);
    let skip = backing.as_ptr().align_offset(DIRECT_IO_ALIGN);
    &mut backing[skip..skip + len]
}

/// The test process's own credentials: owner of everything it creates.
fn me() -> CallerCtx {
    // SAFETY: geteuid / getegid have no preconditions and cannot fail.
    unsafe {
        CallerCtx {
            uid: libc::geteuid(),
            gid: libc::getegid(),
            groups: Vec::new(),
        }
    }
}

fn rw(root: &Path) -> PassthroughVfs {
    let mut vfs = PassthroughVfs::new(root).expect("vfs");
    vfs.set_read_write();
    vfs
}

fn is_stale(r: Result<impl std::fmt::Debug>) -> bool {
    matches!(r, Err(NvstorError::StaleHandle))
}

#[test]
fn read_into_direct_unaligned_and_end_of_file() {
    // The crate directory is on a real filesystem (O_DIRECT path); the
    // default temp dir may be tmpfs (buffered fallback). Check both.
    let roots = [
        tempfile::tempdir_in(env!("CARGO_MANIFEST_DIR")).expect("tmpdir"),
        tempfile::tempdir().expect("tmpdir"),
    ];
    for dir in &roots {
        let data: Vec<u8> = (0..3 * DIRECT_IO_ALIGN + 100)
            .map(|i| (i % 251) as u8)
            .collect();
        fs::write(dir.path().join("f"), &data).expect("write");
        let vfs = PassthroughVfs::new(dir.path()).expect("vfs");
        let ino = vfs.lookup(vfs.root_ino(), "f", &me()).expect("lookup").ino;
        let mut backing = Vec::new();
        // Aligned, whole blocks.
        let buf = aligned(&mut backing, 2 * DIRECT_IO_ALIGN);
        assert_eq!(
            vfs.read_into(ino, DIRECT_IO_ALIGN as u64, buf, &me())
                .unwrap(),
            2 * DIRECT_IO_ALIGN
        );
        assert_eq!(buf, &data[DIRECT_IO_ALIGN..3 * DIRECT_IO_ALIGN]);
        // Aligned request running past end of file: short at EOF.
        let buf = aligned(&mut backing, 2 * DIRECT_IO_ALIGN);
        assert_eq!(
            vfs.read_into(ino, 2 * DIRECT_IO_ALIGN as u64, buf, &me())
                .unwrap(),
            DIRECT_IO_ALIGN + 100
        );
        assert_eq!(&buf[..DIRECT_IO_ALIGN + 100], &data[2 * DIRECT_IO_ALIGN..]);
        // Unaligned offset and length: page cache.
        let mut small = vec![0; 333];
        assert_eq!(vfs.read_into(ino, 7, &mut small, &me()).unwrap(), 333);
        assert_eq!(small, &data[7..340]);
        // Past end of file.
        assert_eq!(
            vfs.read_into(ino, data.len() as u64 + 1, &mut small, &me())
                .unwrap(),
            0
        );
    }
}

#[test]
fn passthrough_basic_operations() {
    let dir = tempfile::tempdir().expect("create tmpdir");
    let root = dir.path();

    fs::write(root.join("hello.txt"), b"hello world").expect("write");
    fs::write(root.join("data.bin"), [0xAB; 1024]).expect("write");
    fs::create_dir(root.join("subdir")).expect("mkdir");
    fs::write(root.join("subdir/nested.txt"), b"nested content").expect("write");

    let vfs = PassthroughVfs::new(root).expect("create vfs");
    let root_ino = vfs.root_ino();

    let hello = vfs
        .lookup(root_ino, "hello.txt", &me())
        .expect("lookup hello");
    assert_eq!(hello.size, 11);
    assert_eq!(hello.file_type, FileType::Regular);

    let subdir = vfs
        .lookup(root_ino, "subdir", &me())
        .expect("lookup subdir");
    assert_eq!(subdir.file_type, FileType::Directory);

    assert_eq!(
        vfs.read(hello.ino, 0, 100, &me()).expect("read"),
        b"hello world"
    );
    assert_eq!(
        vfs.read(hello.ino, 6, 5, &me()).expect("partial read"),
        b"world"
    );

    let (entries, eof) = vfs.readdir(root_ino, 0, &me()).expect("readdir");
    assert!(eof);
    let names: Vec<&str> = entries.iter().map(|e| e.name.as_str()).collect();
    assert!(names.contains(&"hello.txt"));
    assert!(names.contains(&"data.bin"));
    assert!(names.contains(&"subdir"));
    assert!(!names.contains(&".") && !names.contains(&".."));

    let nested = vfs
        .lookup(subdir.ino, "nested.txt", &me())
        .expect("lookup nested");
    assert_eq!(
        vfs.read(nested.ino, 0, 100, &me()).expect("read nested"),
        b"nested content"
    );

    assert_eq!(vfs.getattr(hello.ino).expect("getattr").size, 11);

    let fh = vfs.ino_to_filehandle(hello.ino);
    assert_eq!(vfs.filehandle_to_ino(&fh).expect("resolve fh"), hello.ino);

    assert_eq!(
        vfs.lookupp(subdir.ino, &me()).expect("lookupp").ino,
        root_ino
    );
    assert!(matches!(
        vfs.lookupp(hello.ino, &me()),
        Err(NvstorError::NotDir)
    ));
}

/// Files and directories get sealed handles carrying their own kernel
/// handle; the root keeps the 8-byte form every client starts from.
#[test]
fn filehandles_are_sealed_and_carry_own_kernel_handles() {
    let dir = tempfile::tempdir().expect("tmpdir");
    fs::create_dir(dir.path().join("d")).expect("mkdir");
    fs::write(dir.path().join("d/f"), b"x").expect("write");
    let vfs = PassthroughVfs::new(dir.path()).expect("vfs");
    assert!(
        vfs.mount_id.is_some(),
        "temp filesystem exports kernel handles"
    );
    let d = vfs.lookup(vfs.root_ino(), "d", &me()).expect("lookup d");
    let f = vfs.lookup(d.ino, "f", &me()).expect("lookup f");
    assert_eq!(vfs.ino_to_filehandle(ROOT_INO).len(), FH_INO);
    for ino in [d.ino, f.ino] {
        let fh = vfs.ino_to_filehandle(ino);
        assert!(
            fh.len() > FH_HANDLE && fh.len() <= NFS4_FHSIZE,
            "fh len {}",
            fh.len()
        );
        assert_eq!(vfs.filehandle_to_ino(&fh).expect("resolve"), ino);
    }
    assert_ne!(
        vfs.ino_to_filehandle(d.ino)[FH_SEALED..],
        vfs.ino_to_filehandle(f.ino)[FH_SEALED..]
    );
}

/// Handles are sealed, so a handle with a changed inode number,
/// a changed kernel handle, or one sealed by another key is stale at once
/// (no directory scan, no decode).
#[test]
fn forged_filehandles_are_stale() {
    let dir = tempfile::tempdir().expect("tmpdir");
    fs::write(dir.path().join("a"), b"a").expect("write");
    fs::write(dir.path().join("b"), b"b").expect("write");
    let vfs = PassthroughVfs::new(dir.path()).expect("vfs");
    let a = vfs.lookup(ROOT_INO, "a", &me()).expect("a");
    let b = vfs.lookup(ROOT_INO, "b", &me()).expect("b");
    let fh = vfs.ino_to_filehandle(a.ino);
    // Inode number of another, cached, file.
    let mut other = fh.clone();
    other[..FH_INO].copy_from_slice(&b.ino.to_le_bytes());
    assert!(is_stale(vfs.filehandle_to_ino(&other)));
    // A random inode number.
    let mut random = fh.clone();
    random[..FH_INO].copy_from_slice(&0x1234_5678_9abc_u64.to_le_bytes());
    assert!(is_stale(vfs.filehandle_to_ino(&random)));
    // Corrupt kernel handle bytes.
    let mut corrupt = fh.clone();
    let last = corrupt.len() - 1;
    corrupt[last] ^= 0x5a;
    assert!(is_stale(vfs.filehandle_to_ino(&corrupt)));
    // Valid layout, other key.
    let other_key = PassthroughVfs::new(dir.path()).expect("vfs");
    other_key.lookup(ROOT_INO, "a", &me()).expect("a");
    assert!(is_stale(
        vfs.filehandle_to_ino(&other_key.ino_to_filehandle(a.ino))
    ));
    // Short and oversized handles are malformed (NFS4ERR_BADHANDLE), not
    // stale; the 8-byte form only names the root.
    let malformed = |r: Result<u64>| matches!(r, Err(NvstorError::InvalidArgument(_)));
    assert!(malformed(vfs.filehandle_to_ino(&fh[..4])));
    assert!(malformed(vfs.filehandle_to_ino(&a.ino.to_le_bytes())));
    assert!(malformed(vfs.filehandle_to_ino(&fh[..FH_SEALED + 2])));
    assert!(malformed(vfs.filehandle_to_ino(&[0u8; NFS4_FHSIZE + 1])));
    assert_eq!(vfs.filehandle_to_ino(&fh).expect("genuine"), a.ino);
}

/// A restarted server resolves filehandles issued by the previous one
/// (same key), rejects directories outside the export even when sealed by
/// the right key, and reports deleted files stale. Decoding needs
/// CAP_DAC_READ_SEARCH; without it every unknown handle must still come
/// back stale rather than resolve wrongly.
#[test]
fn filehandles_resolve_after_restart() {
    let parent = tempfile::tempdir().expect("tmpdir");
    let root = parent.path().join("export");
    fs::create_dir_all(root.join("a/b")).expect("mkdir");
    fs::write(root.join("top"), b"top").expect("write");
    fs::write(root.join("a/b/deep"), b"deep").expect("write");
    fs::write(root.join("a/gone"), b"gone").expect("write");
    fs::create_dir(parent.path().join("outside")).expect("mkdir");
    let key = FhKey::from_bytes(&[7; FhKey::LEN]);

    let before = PassthroughVfs::with_fh_key(&root, key.clone()).expect("vfs");
    let a = before.lookup(ROOT_INO, "a", &me()).expect("a");
    let b = before.lookup(a.ino, "b", &me()).expect("b");
    let deep = before.lookup(b.ino, "deep", &me()).expect("deep");
    let top = before.lookup(ROOT_INO, "top", &me()).expect("top");
    let gone = before.lookup(a.ino, "gone", &me()).expect("gone");
    let held: Vec<(u64, Vec<u8>)> = [a.ino, b.ino, deep.ino, top.ino]
        .iter()
        .map(|&ino| (ino, before.ino_to_filehandle(ino)))
        .collect();
    let gone_fh = before.ino_to_filehandle(gone.ino);
    // A directory outside the export, sealed with the export's own key by
    // a server rooted one level up.
    let wide = PassthroughVfs::with_fh_key(parent.path(), key.clone()).expect("vfs");
    let outside = wide.lookup(ROOT_INO, "outside", &me()).expect("outside");
    let outside_fh = wide.ino_to_filehandle(outside.ino);
    drop(before);
    drop(wide);
    fs::remove_file(root.join("a/gone")).expect("rm");

    let after = PassthroughVfs::with_fh_key(&root, key).expect("vfs");
    assert!(
        is_stale(after.filehandle_to_ino(&gone_fh)),
        "deleted file must be stale"
    );
    assert!(
        is_stale(after.filehandle_to_ino(&outside_fh)),
        "directory outside the export"
    );
    if !after.restart_safe() {
        for (_, fh) in &held {
            assert!(is_stale(after.filehandle_to_ino(fh)));
        }
        eprintln!("restart resolution not exercised: needs CAP_DAC_READ_SEARCH (run as root)");
        return;
    }
    for (ino, fh) in &held {
        assert_eq!(
            after.filehandle_to_ino(fh).expect("resolve after restart"),
            *ino
        );
    }
    assert_eq!(after.read(deep.ino, 0, 16, &me()).expect("read"), b"deep");
    assert_eq!(after.read(top.ino, 0, 16, &me()).expect("read"), b"top");
    assert_eq!(after.lookupp(b.ino, &me()).expect("lookupp").ino, a.ino);
    assert_eq!(
        after.lookup(b.ino, "deep", &me()).expect("lookup").ino,
        deep.ino
    );
}

#[test]
fn root_filehandle_is_inode_1() {
    // Regression test for NFS4ERR_STALE bug.
    // PUTROOTFH always uses inode 1. PassthroughVfs must map inode 1 to root.
    let dir = tempfile::tempdir().expect("create tmpdir");
    fs::write(dir.path().join("test.txt"), b"data").expect("write");
    let vfs = PassthroughVfs::new(dir.path()).expect("create vfs");
    assert_eq!(vfs.root_ino(), 1);
    let fh = vfs.ino_to_filehandle(1);
    assert_eq!(vfs.filehandle_to_ino(&fh).expect("resolve root fh"), 1);
    assert_eq!(
        vfs.lookup(1, "test.txt", &me())
            .expect("lookup from root")
            .size,
        4
    );
    assert_eq!(
        vfs.getattr(1).expect("getattr root").file_type,
        FileType::Directory
    );
}

/// READ must use positional I/O, not slurp the whole file.
#[test]
fn passthrough_pread_returns_only_requested_window() {
    let dir = tempfile::tempdir().expect("create tmpdir");
    let mut contents = vec![0u8; 4 * 1024 * 1024];
    for (i, b) in contents.iter_mut().enumerate() {
        *b = (i % 251) as u8;
    }
    fs::write(dir.path().join("big.dat"), &contents).expect("write");
    let vfs = PassthroughVfs::new(dir.path()).expect("create vfs");
    let attrs = vfs
        .lookup(vfs.root_ino(), "big.dat", &me())
        .expect("lookup");

    let window = vfs
        .read(attrs.ino, 2 * 1024 * 1024, 1024, &me())
        .expect("read middle");
    assert_eq!(
        &window[..],
        &contents[2 * 1024 * 1024..2 * 1024 * 1024 + 1024]
    );
    let past_eof = vfs
        .read(attrs.ino, (contents.len() as u64) + 100, 1024, &me())
        .expect("past-eof read");
    assert!(past_eof.is_empty());
    let at_eof = vfs
        .read(attrs.ino, contents.len() as u64 - 10, 100, &me())
        .expect("eof read");
    assert_eq!(&at_eof[..], &contents[contents.len() - 10..]);
}

/// WRITE must use positional I/O, not read-modify-write.
#[test]
fn passthrough_pwrite_only_modifies_requested_window() {
    let dir = tempfile::tempdir().expect("create tmpdir");
    let mut contents = vec![0xAAu8; 1024 * 1024];
    fs::write(dir.path().join("target.bin"), &contents).expect("write");
    let vfs = rw(dir.path());
    let attrs = vfs
        .lookup(vfs.root_ino(), "target.bin", &me())
        .expect("lookup");
    let patch = b"SIXTEEN_BYTE_SZ.";
    assert_eq!(
        vfs.write(attrs.ino, 100_000, patch, &me()).expect("write"),
        16
    );
    contents[100_000..100_016].copy_from_slice(patch);
    assert_eq!(
        fs::read(dir.path().join("target.bin")).expect("readback"),
        contents
    );
}

#[test]
fn passthrough_read_only_rejects_writes() {
    let dir = tempfile::tempdir().expect("create tmpdir");
    fs::write(dir.path().join("test.txt"), b"data").expect("write");
    let vfs = PassthroughVfs::new(dir.path()).expect("create vfs");
    let file = vfs.lookup(ROOT_INO, "test.txt", &me()).expect("lookup");
    assert!(vfs.write(file.ino, 0, b"new", &me()).is_err());
    assert!(vfs.create_file(ROOT_INO, "new.txt", 0o644, &me()).is_err());
    assert!(vfs.mkdir(ROOT_INO, "newdir", 0o755, &me()).is_err());
    assert!(vfs
        .setattr(file.ino, Some(0), None, None, None, None, None, &me())
        .is_err());
    assert_eq!(
        fs::read(dir.path().join("test.txt")).expect("read"),
        b"data"
    );
}

#[test]
fn passthrough_read_write_mode() {
    let dir = tempfile::tempdir().expect("create tmpdir");
    let vfs = rw(dir.path());
    let (file, created) = vfs
        .create_file(ROOT_INO, "created.txt", 0o644, &me())
        .expect("create");
    assert!(created);
    assert_eq!(file.file_type, FileType::Regular);
    vfs.write(file.ino, 0, b"written data", &me())
        .expect("write");
    assert_eq!(
        vfs.read(file.ino, 0, 100, &me()).expect("read"),
        b"written data"
    );
    // CREATE of an existing file opens it; it never truncates.
    let (reopened, created) = vfs
        .create_file(ROOT_INO, "created.txt", 0o644, &me())
        .expect("reopen");
    assert_eq!((reopened.size, created), (12, false));

    let sub = vfs.mkdir(ROOT_INO, "sub", 0o755, &me()).expect("mkdir");
    vfs.rename(ROOT_INO, "created.txt", sub.ino, "moved.txt", &me())
        .expect("rename");
    // The filehandle follows the file across the rename.
    assert_eq!(
        vfs.read(file.ino, 0, 7, &me()).expect("read after rename"),
        b"written"
    );
    let linked = vfs
        .link(file.ino, ROOT_INO, "hard.txt", &me())
        .expect("link");
    assert_eq!(linked.ino, file.ino);
    vfs.setattr(
        file.ino,
        Some(4),
        Some(0o600),
        None,
        None,
        Some(1_000_000),
        Some(2_000_000),
        &me(),
    )
    .expect("setattr");
    let attrs = vfs.getattr(file.ino).expect("getattr");
    assert_eq!(
        (attrs.size, attrs.mode, attrs.atime, attrs.mtime),
        (4, 0o600, 1_000_000, 2_000_000)
    );

    vfs.unlink(sub.ino, "moved.txt", &me()).expect("unlink");
    vfs.unlink(ROOT_INO, "hard.txt", &me()).expect("unlink");
    assert!(vfs.lookup(sub.ino, "moved.txt", &me()).is_err());
    assert!(
        is_stale(vfs.getattr(file.ino)),
        "deleted file is stale, not another file"
    );
    vfs.rmdir(ROOT_INO, "sub", &me()).expect("rmdir");
    assert!(matches!(
        vfs.unlink(ROOT_INO, "missing", &me()),
        Err(NvstorError::NotFound)
    ));
}

/// A page cookie keeps its position when entries change between
/// pages: nothing that existed throughout is skipped or listed twice.
#[test]
fn readdir_cookies_survive_directory_changes() {
    let dir = tempfile::tempdir().expect("tmpdir");
    for i in 0..300 {
        fs::write(dir.path().join(format!("file-{i:04}")), b"x").expect("write");
    }
    let vfs = PassthroughVfs::new(dir.path()).expect("vfs");
    let (first, eof) = vfs.readdir(ROOT_INO, 0, &me()).expect("page 1");
    assert_eq!(first.len(), READDIR_MAX_ENTRIES);
    assert!(!eof);
    // Remove 20 already-listed entries and add 50 new ones.
    for entry in first.iter().take(20) {
        fs::remove_file(dir.path().join(&entry.name)).expect("rm");
    }
    for i in 0..50 {
        fs::write(dir.path().join(format!("new-{i:04}")), b"y").expect("write");
    }
    let mut seen: Vec<String> = first.iter().map(|e| e.name.clone()).collect();
    let mut cookie = first.last().expect("entries").cookie;
    loop {
        let (page, eof) = vfs.readdir(ROOT_INO, cookie, &me()).expect("next page");
        if let Some(last) = page.last() {
            cookie = last.cookie;
        }
        seen.extend(page.into_iter().map(|e| e.name));
        if eof {
            break;
        }
    }
    for i in 0..300 {
        let name = format!("file-{i:04}");
        assert_eq!(seen.iter().filter(|n| **n == name).count(), 1, "{name}");
    }
}

#[test]
fn passthrough_large_directory() {
    let dir = tempfile::tempdir().expect("create tmpdir");
    for i in 0..200 {
        fs::write(
            dir.path().join(format!("file-{i:04}.txt")),
            format!("content-{i}"),
        )
        .expect("write");
    }
    let vfs = PassthroughVfs::new(dir.path()).expect("create vfs");
    let (entries, eof) = vfs.readdir(ROOT_INO, 0, &me()).expect("readdir");
    assert_eq!(entries.len(), 128);
    assert!(!eof);
    let (more, eof) = vfs
        .readdir(ROOT_INO, entries[127].cookie, &me())
        .expect("readdir page 2");
    assert_eq!(more.len(), 72);
    assert!(eof);
}

/// The inode cache stays within its bound however many inodes are
/// listed, and evicted inodes are found again by name.
#[test]
fn inode_cache_is_bounded() {
    let dir = tempfile::tempdir().expect("tmpdir");
    for i in 0..1000 {
        fs::write(dir.path().join(format!("f{i}")), b"x").expect("write");
    }
    let mut vfs = PassthroughVfs::new(dir.path()).expect("vfs");
    vfs.set_node_cache_capacity(64);
    let mut cookie = 0;
    loop {
        let (page, eof) = vfs.readdir(ROOT_INO, cookie, &me()).expect("readdir");
        assert!(vfs.cached_nodes() <= 64, "{} cached", vfs.cached_nodes());
        if eof {
            break;
        }
        cookie = page.last().expect("entries").cookie;
    }
    let f = vfs.lookup(ROOT_INO, "f7", &me()).expect("lookup");
    assert_eq!(vfs.read(f.ino, 0, 1, &me()).expect("read"), b"x");
}

/// Regression test: names containing `/`, `.`, `..`, or null
/// bytes must be rejected.
#[test]
fn passthrough_rejects_path_traversal_names() {
    let dir = tempfile::tempdir().expect("create tmpdir");
    fs::write(dir.path().join("visible.txt"), b"ok").expect("write");
    let vfs = PassthroughVfs::new(dir.path()).expect("create vfs");
    assert!(vfs.lookup(ROOT_INO, "visible.txt", &me()).is_ok());
    for bad in &[
        "..",
        ".",
        "../outside-secret",
        "a/b",
        "",
        "sub/../../x",
        "x\0y",
    ] {
        let err = vfs.lookup(ROOT_INO, bad, &me()).err().unwrap_or_else(|| {
            panic!("lookup({bad:?}) should have failed");
        });
        assert!(
            matches!(
                err,
                NvstorError::InvalidArgument(_) | NvstorError::NameTooLong
            ),
            "lookup({bad:?}): expected InvalidArgument, got {err:?}"
        );
    }
}

#[test]
fn passthrough_symlinks() {
    let dir = tempfile::tempdir().expect("create tmpdir");
    fs::write(dir.path().join("target.txt"), b"real file").expect("write");
    symlink("target.txt", dir.path().join("link.txt")).expect("symlink");
    let vfs = PassthroughVfs::new(dir.path()).expect("create vfs");
    let link = vfs
        .lookup(ROOT_INO, "link.txt", &me())
        .expect("lookup link");
    assert_eq!(link.file_type, FileType::Symlink);
    assert_eq!(vfs.readlink(link.ino).expect("readlink"), "target.txt");
    let target = vfs
        .lookup(ROOT_INO, "target.txt", &me())
        .expect("lookup target");
    assert!(
        vfs.readlink(target.ino).is_err(),
        "READLINK of a regular file"
    );
}

/// Export `root/export` beside `root/outside/secret`, with links that
/// leave the export by absolute path, by `..`, and to a directory.
fn escape_fixture() -> (tempfile::TempDir, PathBuf, PathBuf) {
    let top = tempfile::tempdir().expect("tmpdir");
    let export = top.path().join("export");
    let outside = top.path().join("outside");
    fs::create_dir_all(export.join("sub")).expect("mkdir");
    fs::create_dir(&outside).expect("mkdir");
    fs::write(outside.join("secret"), b"SECRET").expect("write");
    symlink(outside.join("secret"), export.join("abs")).expect("symlink");
    symlink("../../outside/secret", export.join("sub/rel")).expect("symlink");
    symlink(&outside, export.join("dirlink")).expect("symlink");
    symlink("..", export.join("up")).expect("symlink");
    (top, export, outside)
}

/// READ and READDIR never follow a symlink out of the export.
#[test]
fn read_only_export_never_follows_symlinks() {
    let (_top, export, _outside) = escape_fixture();
    let vfs = PassthroughVfs::new(&export).expect("vfs");
    let sub = vfs.lookup(ROOT_INO, "sub", &me()).expect("sub");
    for (parent, name) in [(ROOT_INO, "abs"), (sub.ino, "rel")] {
        let link = vfs.lookup(parent, name, &me()).expect("lookup link");
        assert_eq!(link.file_type, FileType::Symlink);
        assert!(
            vfs.read(link.ino, 0, 64, &me()).is_err(),
            "READ through {name}"
        );
        let mut buf = vec![0u8; 64];
        assert!(
            vfs.read_into(link.ino, 0, &mut buf, &me()).is_err(),
            "READ (RDMA) through {name}"
        );
        assert!(vfs.getxattr(link.ino, "user.x", &me()).is_err());
        assert!(vfs.readlink(link.ino).is_ok(), "READLINK returns the text");
    }
    for name in ["dirlink", "up"] {
        let link = vfs.lookup(ROOT_INO, name, &me()).expect("lookup dirlink");
        assert!(
            matches!(vfs.readdir(link.ino, 0, &me()), Err(NvstorError::Symlink)),
            "READDIR of {name}"
        );
        assert!(matches!(
            vfs.lookup(link.ino, "secret", &me()),
            Err(NvstorError::Symlink)
        ));
        assert!(matches!(
            vfs.lookupp(link.ino, &me()),
            Err(NvstorError::Symlink)
        ));
    }
    // Nothing outside the export was ever cached or listed.
    let (entries, _) = vfs.readdir(ROOT_INO, 0, &me()).expect("readdir root");
    assert!(entries.iter().all(|e| e.name != "secret"));
}

/// A directory replaced by a symlink after the client looked it up
/// does not lead its old filehandles (or children) outside the export.
#[test]
fn directory_swapped_for_symlink_is_stale() {
    let (_top, export, outside) = escape_fixture();
    fs::write(export.join("sub/secret"), b"inside").expect("write");
    let vfs = PassthroughVfs::new(&export).expect("vfs");
    let sub = vfs.lookup(ROOT_INO, "sub", &me()).expect("sub");
    let inner = vfs.lookup(sub.ino, "secret", &me()).expect("secret");
    fs::rename(export.join("sub"), export.join("old")).expect("rename");
    symlink(&outside, export.join("sub")).expect("symlink");
    if vfs.restart_safe() {
        // Resolved by kernel handle: still the original inside file.
        assert_eq!(vfs.read(inner.ino, 0, 64, &me()).expect("read"), b"inside");
    } else {
        assert!(is_stale(vfs.read(inner.ino, 0, 64, &me())));
        assert!(is_stale(vfs.readdir(sub.ino, 0, &me())));
    }
}

/// A FIFO in the export is never opened (READ would block a worker).
#[test]
fn fifo_is_never_opened() {
    let dir = tempfile::tempdir().expect("tmpdir");
    let path = CString::new(dir.path().join("pipe").as_os_str().as_bytes()).expect("path");
    // SAFETY: `path` is NUL-terminated.
    assert_eq!(unsafe { libc::mkfifo(path.as_ptr(), 0o644) }, 0);
    let vfs = rw(dir.path());
    let fifo = vfs.lookup(ROOT_INO, "pipe", &me()).expect("lookup");
    assert!(vfs.read(fifo.ino, 0, 1, &me()).is_err());
    assert!(vfs.write(fifo.ino, 0, b"x", &me()).is_err());
    assert!(vfs.getattr(fifo.ino).is_ok());
}

/// With --read-write, a client-created symlink is never a way to
/// modify the file it names.
#[test]
fn read_write_export_never_writes_through_symlinks() {
    let (_top, export, outside) = escape_fixture();
    let vfs = rw(&export);
    let victim = outside.join("secret");
    let mode_before = fs::metadata(&victim).expect("stat").permissions().mode();
    let link = vfs
        .create_symlink(ROOT_INO, "x", victim.to_str().expect("utf8"), &me())
        .expect("symlink");
    let (data, _) = vfs
        .create_file(ROOT_INO, "data", 0o644, &me())
        .expect("create");
    vfs.write(data.ino, 0, b"payload", &me()).expect("write");

    assert!(
        vfs.setattr(link.ino, Some(0), None, None, None, None, None, &me())
            .is_err(),
        "truncate"
    );
    assert!(
        vfs.setattr(link.ino, None, Some(0o666), None, None, None, None, &me())
            .is_err(),
        "chmod"
    );
    assert!(vfs.write(link.ino, 0, b"owned", &me()).is_err(), "write");
    assert!(
        vfs.copy_file(data.ino, 0, link.ino, 0, 0, &me()).is_err(),
        "copy into"
    );
    assert!(
        vfs.allocate(link.ino, 0, 4096, false, &me()).is_err(),
        "allocate"
    );
    assert!(
        vfs.allocate(link.ino, 0, 4096, true, &me()).is_err(),
        "deallocate"
    );
    assert!(
        vfs.setxattr(link.ino, "user.x", b"1", 0, &me()).is_err(),
        "setxattr"
    );
    // CREATE/OPEN of the link's name opens nothing through it.
    assert!(
        vfs.create_file(ROOT_INO, "x", 0o644, &me()).is_err(),
        "open-create through link"
    );
    // Times on the link change the link itself.
    vfs.setattr(link.ino, None, None, None, None, Some(1), Some(1), &me())
        .expect("utimes on link");
    // The client-written relative link climbing out is equally inert.
    let up = vfs
        .create_symlink(ROOT_INO, "y", "../outside/secret", &me())
        .expect("symlink");
    assert!(vfs.write(up.ino, 0, b"owned", &me()).is_err());
    let mkdir_through = vfs.lookup(ROOT_INO, "dirlink", &me()).expect("dirlink");
    assert!(vfs
        .create_file(mkdir_through.ino, "planted", 0o644, &me())
        .is_err());
    assert!(vfs
        .mkdir(mkdir_through.ino, "planted", 0o755, &me())
        .is_err());

    assert_eq!(fs::read(&victim).expect("read victim"), b"SECRET");
    assert_eq!(
        fs::metadata(&victim).expect("stat").permissions().mode(),
        mode_before
    );
    assert!(!outside.join("planted").exists());
    assert_eq!(
        fs::read_link(export.join("x")).expect("link intact"),
        victim
    );
}

/// A deleted file's handle is stale even after its inode number is
/// reused by a new file, never an alias of that file.
#[test]
fn reused_inode_number_is_stale() {
    let dir = tempfile::tempdir_in(env!("CARGO_MANIFEST_DIR")).expect("tmpdir");
    let vfs = PassthroughVfs::new(dir.path()).expect("vfs");
    fs::write(dir.path().join("ckpt"), b"v1").expect("write");
    let old = vfs.lookup(ROOT_INO, "ckpt", &me()).expect("lookup");
    let old_fh = vfs.ino_to_filehandle(old.ino);
    fs::remove_file(dir.path().join("ckpt")).expect("rm");
    let mut reused = false;
    for i in 0..256 {
        let name = format!("n{i}");
        fs::write(dir.path().join(&name), b"v2-other").expect("write");
        let new = vfs.lookup(ROOT_INO, &name, &me()).expect("lookup new");
        if new.ino == old.ino {
            reused = true;
            // The new file is cached under the reused number now.
            assert!(
                is_stale(vfs.filehandle_to_ino(&old_fh)),
                "old handle must be stale"
            );
            assert_eq!(
                vfs.read(new.ino, 0, 16, &me()).expect("new file"),
                b"v2-other"
            );
            break;
        }
    }
    if !reused {
        assert!(is_stale(
            vfs.filehandle_to_ino(&old_fh)
                .and_then(|ino| vfs.read(ino, 0, 16, &me()))
        ));
        eprintln!("inode number not reused by the filesystem; checked plain deletion only");
    }
}

/// The root is canonicalized, so LOOKUPP inside an export given by a
/// symlinked or relative path returns the real parent.
#[test]
fn export_root_is_canonical() {
    let top = tempfile::tempdir().expect("tmpdir");
    fs::create_dir_all(top.path().join("real/a/b")).expect("mkdir");
    symlink(top.path().join("real"), top.path().join("alias")).expect("symlink");
    let vfs = PassthroughVfs::new(top.path().join("alias")).expect("vfs");
    assert_eq!(
        vfs.root(),
        fs::canonicalize(top.path().join("real")).expect("canon")
    );
    let a = vfs.lookup(ROOT_INO, "a", &me()).expect("a");
    let b = vfs.lookup(a.ino, "b", &me()).expect("b");
    assert_eq!(vfs.lookupp(b.ino, &me()).expect("lookupp").ino, a.ino);
    assert_eq!(vfs.lookupp(a.ino, &me()).expect("lookupp").ino, ROOT_INO);
    assert_eq!(
        vfs.lookupp(ROOT_INO, &me()).expect("lookupp root").ino,
        ROOT_INO
    );
}

/// RFC 4231 test case 1 for the HMAC-SHA256 construction.
#[test]
fn fh_key_hmac_matches_rfc4231() {
    let key = FhKey::hmac(&[0x0b; 20]);
    let expected = [
        0xb0, 0x34, 0x4c, 0x61, 0xd8, 0xdb, 0x38, 0x53, 0x5c, 0xa8, 0xaf, 0xce, 0xaf, 0x0b, 0xf1,
        0x2b,
    ];
    assert_eq!(key.tag(&[b"Hi ", b"There"]), expected);
}

#[test]
fn fh_key_file_is_created_private_and_reloaded() {
    let dir = tempfile::tempdir().expect("tmpdir");
    let path = dir.path().join("fh.key");
    let created = FhKey::load_or_create(&path).expect("create");
    let meta = fs::metadata(&path).expect("stat");
    assert_eq!(meta.len(), FhKey::LEN as u64);
    assert_eq!(meta.permissions().mode() & 0o777, 0o600);
    let loaded = FhKey::load_or_create(&path).expect("load");
    assert_eq!(created.tag(&[b"x"]), loaded.tag(&[b"x"]));
    assert_ne!(
        created.tag(&[b"x"]),
        FhKey::random().expect("random").tag(&[b"x"])
    );

    fs::set_permissions(&path, fs::Permissions::from_mode(0o640)).expect("chmod");
    assert!(FhKey::load_or_create(&path).is_err(), "group-readable key");
    fs::write(dir.path().join("short"), b"abc").expect("write");
    fs::set_permissions(dir.path().join("short"), fs::Permissions::from_mode(0o600))
        .expect("chmod");
    assert!(
        FhKey::load_or_create(&dir.path().join("short")).is_err(),
        "wrong length"
    );
    symlink(&path, dir.path().join("via-link")).expect("symlink");
    assert!(
        FhKey::load_or_create(&dir.path().join("via-link")).is_err(),
        "symlinked key"
    );
}

/// COMMIT / stable WRITE go through a real fsync of the file: it
/// succeeds on live inodes and fails on a handle that no longer resolves
/// (the old no-op reported success for anything).
#[test]
fn fsync_reaches_the_file() {
    let dir = tempfile::tempdir().expect("tmpdir");
    let vfs = Arc::new(rw(dir.path()));
    let backend = crate::vfs_backend::VfsBackend::Passthrough(vfs.clone());
    let (file, _) = vfs
        .create_file(ROOT_INO, "ckpt", 0o644, &me())
        .expect("create");
    vfs.write(file.ino, 0, b"durable", &me()).expect("write");
    backend.fsync(file.ino).expect("fsync file");
    backend.fsync(ROOT_INO).expect("fsync directory");
    let link = vfs
        .create_symlink(ROOT_INO, "l", "ckpt", &me())
        .expect("symlink");
    // COMMIT of a symlink is NFS4ERR_SYMLINK (RFC 8881 §18.3.4).
    assert!(matches!(backend.fsync(link.ino), Err(NvstorError::Symlink)));
    fs::remove_file(dir.path().join("ckpt")).expect("rm");
    assert!(
        backend.fsync(file.ino).is_err(),
        "fsync of a deleted file must fail"
    );
    assert!(
        backend.fsync(0xdead_beef).is_err(),
        "fsync of an unknown inode must fail"
    );
}

/// The passthrough's errno mapping keeps the errno: ENOSPC is NoSpace,
/// and errnos it has no variant for (EROFS, EDQUOT, EIO) stay `Io` with
/// their errno, which `nfs4::nvstor_err_to_nfs4` turns into the matching
/// status (tested there). An unprivileged test cannot fill a filesystem,
/// so what the kernel client reports on a full export is checked against
/// a real mount.
#[test]
fn data_path_errors_keep_their_errno() {
    let full = map_io_err(io::Error::from_raw_os_error(libc::ENOSPC));
    assert!(matches!(full, NvstorError::NoSpace), "ENOSPC: {full:?}");
    let stale = map_io_err(io::Error::from_raw_os_error(libc::ESTALE));
    assert!(
        matches!(stale, NvstorError::StaleHandle),
        "ESTALE: {stale:?}"
    );
    for errno in [libc::EROFS, libc::EDQUOT, libc::EIO] {
        let e = map_io_err(io::Error::from_raw_os_error(errno));
        assert!(
            matches!(&e, NvstorError::Io(io) if io.raw_os_error() == Some(errno)),
            "errno {errno}: {e:?}"
        );
    }
}

// =====================================================================
// Access control
// =====================================================================

use perm::*;

/// A caller with `uid`, primary `gid` and supplementary `groups`.
fn caller(uid: u32, gid: u32, groups: &[u32]) -> CallerCtx {
    CallerCtx {
        uid,
        gid,
        groups: groups.to_vec(),
    }
}

const OWNER: u32 = 1000;
const GROUP: u32 = 2000;

fn file(mode: u32) -> Perm {
    Perm {
        mode: libc::S_IFREG as u32 | mode,
        uid: OWNER,
        gid: GROUP,
    }
}

fn dir(mode: u32) -> Perm {
    Perm {
        mode: libc::S_IFDIR as u32 | mode,
        uid: OWNER,
        gid: GROUP,
    }
}

/// `may` against every permission mode, every want and every caller
/// class: the owner uses only the owner bits (even when group or other
/// bits grant more), a member of the file's group (primary or
/// supplementary) only the group bits, everyone else the other bits.
#[test]
fn may_follows_posix_classes_exhaustively() {
    let classes = [
        (caller(OWNER, 9, &[]), 6),
        // The owner is also in the file's group: still the owner class.
        (caller(OWNER, GROUP, &[]), 6),
        (caller(3000, GROUP, &[]), 3),
        (caller(3000, 9, &[8, GROUP]), 3),
        (caller(3000, 9, &[8]), 0),
        // The owner's uid number as a gid, the group's gid as a uid.
        (caller(GROUP, OWNER, &[]), 0),
    ];
    for mode in 0..0o10000u32 {
        for want in 1..=7u32 {
            for (cred, shift) in &classes {
                let expect = (mode >> shift) & want == want;
                assert_eq!(
                    may(file(mode), cred, want),
                    expect,
                    "mode {mode:o} want {want} {cred:?}"
                );
                assert_eq!(
                    may(dir(mode), cred, want),
                    expect,
                    "dir mode {mode:o} want {want} {cred:?}"
                );
            }
        }
    }
}

/// Root (only reachable with --no-root-squash) reads and writes anything,
/// searches any directory, and executes a file only if some x bit is set.
#[test]
fn root_bypasses_everything_but_execute_without_x() {
    let root = CallerCtx::root();
    for mode in 0..0o10000u32 {
        assert!(may(file(mode), &root, MAY_READ | MAY_WRITE));
        assert!(may(dir(mode), &root, MAY_READ | MAY_WRITE | MAY_EXEC));
        assert_eq!(
            may(file(mode), &root, MAY_EXEC),
            mode & 0o111 != 0,
            "mode {mode:o}"
        );
    }
}

#[test]
fn data_access_rules() {
    let owner = caller(OWNER, 9, &[]);
    let member = caller(3000, GROUP, &[]);
    let other = caller(3000, 9, &[]);
    // 0600 / 0640 / 0644: who may read.
    for (mode, member_reads, other_reads) in [
        (0o600, false, false),
        (0o640, true, false),
        (0o644, true, true),
    ] {
        assert!(may_read_data(file(mode), &owner));
        assert_eq!(may_read_data(file(mode), &member), member_reads, "{mode:o}");
        assert_eq!(may_read_data(file(mode), &other), other_reads, "{mode:o}");
        assert!(may_write_data(file(mode), &owner));
        assert!(!may_write_data(file(mode), &member) && !may_write_data(file(mode), &other));
    }
    // The owner keeps data access whatever the mode (nfsd owner override).
    assert!(may_read_data(file(0), &owner) && may_write_data(file(0), &owner));
    // Execute-only regular file: readable (a client must read a program
    // to run it); an execute-only directory is not listable.
    assert!(may_read_data(file(0o711), &other));
    assert!(!may_read_data(dir(0o711), &other));
    assert!(may_write_data(file(0o662), &other) && may_write_data(file(0o620), &member));
    // OPEN has no owner override; write share access needs w, and so does
    // denying others read access.
    assert!(!may_open(file(0o200), &owner, 1, 0));
    assert!(may_open(file(0o400), &owner, 1, 0));
    assert!(!may_open(file(0o400), &owner, 2, 0));
    assert!(!may_open(file(0o400), &owner, 1, 1));
    assert!(may_open(file(0o600), &owner, 3, 1));
    assert!(may_open(file(0o711), &other, 1, 0));
    assert!(!may_open(file(0o644), &other, 3, 0));
}

#[test]
fn delete_rename_and_link_rules() {
    let owner = caller(OWNER, 9, &[]);
    let other = caller(3000, 9, &[]);
    let third = caller(4000, 9, &[]);
    let theirs = Perm {
        uid: 3000,
        ..file(0o644)
    };
    // Write and search on the directory are both needed.
    assert!(may_delete(dir(0o777), file(0o644), &other));
    assert!(!may_delete(dir(0o775), file(0o644), &other));
    assert!(!may_delete(dir(0o776), file(0o644), &other));
    // Sticky (/tmp-like): the entry's owner and the directory's owner may,
    // anyone else may not; root always may.
    let sticky = dir(0o1777);
    assert!(may_delete(sticky, theirs, &other));
    assert!(may_delete(sticky, theirs, &owner));
    assert!(!may_delete(sticky, theirs, &third));
    assert!(may_delete(sticky, theirs, &CallerCtx::root()));
    // Hard links: owner, root, or read+write on a plain regular file.
    assert!(may_link_source(file(0o600), &owner));
    assert!(!may_link_source(file(0o644), &other));
    assert!(may_link_source(file(0o666), &other));
    assert!(!may_link_source(file(0o4666), &other), "set-user-id");
    assert!(
        !may_link_source(file(0o2676), &other),
        "executable set-group-id"
    );
    assert!(
        may_link_source(file(0o2666), &other),
        "set-group-id without group execute"
    );
    assert!(!may_link_source(dir(0o777), &other));
    assert!(may_link_source(file(0), &CallerCtx::root()));
}

#[test]
fn chmod_chown_and_privilege_rules() {
    let owner = caller(OWNER, 9, &[]);
    let owner_in_group = caller(OWNER, 9, &[GROUP, 77]);
    let other = caller(3000, GROUP, &[]);
    let root = CallerCtx::root();
    assert!(is_owner(file(0o644), &owner) && is_owner(file(0o644), &root));
    assert!(
        !is_owner(file(0o777), &other),
        "write permission does not allow chmod"
    );
    // Set-group-id is dropped unless the caller is in the file's group or root.
    assert_eq!(chmod_mode(file(0), &owner, 0o2755), 0o755);
    assert_eq!(chmod_mode(file(0), &owner_in_group, 0o2755), 0o2755);
    assert_eq!(chmod_mode(file(0), &root, 0o6755), 0o6755);
    assert_eq!(chmod_mode(file(0), &owner, 0o4755 | 0o170000), 0o4755);
    // chown: root anything; the owner only a no-op uid and its own groups.
    assert!(may_chown(file(0o644), &root, Some(5), Some(6)));
    assert!(
        !may_chown(file(0o644), &owner, Some(3000), None),
        "give-away"
    );
    assert!(may_chown(file(0o644), &owner, Some(OWNER), None), "no-op");
    assert!(
        may_chown(file(0o644), &owner, None, Some(GROUP)),
        "unchanged group"
    );
    assert!(!may_chown(file(0o644), &owner, None, Some(77)));
    assert!(may_chown(
        file(0o644),
        &owner_in_group,
        Some(OWNER),
        Some(77)
    ));
    assert!(
        !may_chown(file(0o644), &other, Some(OWNER), None),
        "non-owner no-op"
    );
    assert!(
        !may_chown(file(0o644), &other, None, Some(GROUP)),
        "non-owner chgrp"
    );
    assert!(may_chown(file(0o644), &other, None, None));
    // A data change by anyone but root drops setuid and executable setgid.
    assert_eq!(
        privs_to_drop(file(0o6755), &owner),
        (libc::S_ISUID | libc::S_ISGID) as u32
    );
    assert_eq!(
        privs_to_drop(file(0o2745), &other),
        0,
        "setgid without group execute stays"
    );
    assert_eq!(privs_to_drop(file(0o4755), &other), libc::S_ISUID as u32);
    assert_eq!(privs_to_drop(file(0o6755), &root), 0);
    assert_eq!(privs_to_drop(dir(0o2775), &owner), 0);
}

#[test]
fn access_reports_what_is_allowed() {
    const ALL: u32 = 0x1ff;
    let owner = caller(OWNER, 9, &[]);
    let other = caller(3000, 9, &[]);
    let file_bits = ACCESS4_READ
        | ACCESS4_MODIFY
        | ACCESS4_EXTEND
        | ACCESS4_EXECUTE
        | ACCESS4_XAREAD
        | ACCESS4_XAWRITE
        | ACCESS4_XALIST;
    let dir_bits = ALL & !ACCESS4_EXECUTE;
    assert_eq!(
        access(file(0o600), &owner, false, ALL),
        (file_bits, file_bits & !ACCESS4_EXECUTE)
    );
    assert_eq!(access(file(0o600), &other, false, ALL), (file_bits, 0));
    assert_eq!(
        access(file(0o755), &other, false, ALL),
        (
            file_bits,
            ACCESS4_READ | ACCESS4_EXECUTE | ACCESS4_XAREAD | ACCESS4_XALIST
        )
    );
    // Read-only export: nothing that changes the object.
    assert_eq!(
        access(file(0o600), &owner, true, ALL),
        (file_bits, ACCESS4_READ | ACCESS4_XAREAD | ACCESS4_XALIST)
    );
    assert_eq!(access(dir(0o755), &owner, false, ALL), (dir_bits, dir_bits));
    assert_eq!(
        access(dir(0o755), &other, false, ALL),
        (
            dir_bits,
            ACCESS4_READ | ACCESS4_LOOKUP | ACCESS4_XAREAD | ACCESS4_XALIST
        )
    );
    // Write without search grants no namespace change.
    assert_eq!(
        access(dir(0o602), &other, false, ACCESS4_MODIFY | ACCESS4_DELETE).1,
        0
    );
    // Only what was asked is reported.
    assert_eq!(
        access(file(0o644), &other, false, ACCESS4_READ),
        (ACCESS4_READ, ACCESS4_READ)
    );
}

/// A uid other than the test process's (the owner of every file the tests
/// create). The checks are the server's, so acting as it needs no
/// privileges.
fn stranger() -> CallerCtx {
    let uid = if me().uid == 4_242_424 {
        4_242_425
    } else {
        4_242_424
    };
    caller(uid, uid, &[])
}

/// The stranger, member of the test process's group (supplementary).
fn member() -> CallerCtx {
    let mut cred = stranger();
    cred.groups.push(me().gid);
    cred
}

fn chmod(path: &Path, mode: u32) {
    fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("chmod");
}

/// Through the VFS: 0600 / 0640 / 0644 files of the test uid read by the
/// owner, a group member and anyone else; listing and lookup need r and x
/// on the directory; root is only root when not squashed.
#[test]
fn vfs_reads_follow_the_callers_credentials() {
    let dir = tempfile::tempdir().expect("tmpdir");
    for (name, mode) in [("0600", 0o600), ("0640", 0o640), ("0644", 0o644)] {
        fs::write(dir.path().join(name), b"secret").expect("write");
        chmod(&dir.path().join(name), mode);
    }
    chmod(dir.path(), 0o755);
    let vfs = PassthroughVfs::new(dir.path()).expect("vfs");
    let anon = CallerCtx::anonymous();
    for (name, member_reads, other_reads) in [
        ("0600", false, false),
        ("0640", true, false),
        ("0644", true, true),
    ] {
        let ino = vfs.lookup(ROOT_INO, name, &stranger()).expect("lookup").ino;
        assert_eq!(vfs.read(ino, 0, 16, &me()).expect("owner reads"), b"secret");
        let mut buf = vec![0u8; 16];
        assert_eq!(
            vfs.read_into(ino, 0, &mut buf, &me())
                .expect("owner reads (RDMA path)"),
            6
        );
        assert_eq!(
            vfs.read(ino, 0, 16, &member()).is_ok(),
            member_reads,
            "{name} member"
        );
        assert_eq!(
            vfs.read(ino, 0, 16, &stranger()).is_ok(),
            other_reads,
            "{name} other"
        );
        assert_eq!(
            vfs.read_into(ino, 0, &mut buf, &stranger()).is_ok(),
            other_reads,
            "{name} other (RDMA path)"
        );
        assert_eq!(
            vfs.read(ino, 0, 16, &anon).is_ok(),
            other_reads,
            "{name} anonymous"
        );
        assert!(
            vfs.read(ino, 0, 16, &CallerCtx::root()).is_ok(),
            "{name} unsquashed root"
        );
        let xattr_denied = vfs
            .getxattr(ino, "x", &stranger())
            .is_err_and(|e| matches!(e, NvstorError::PermissionDenied));
        assert_eq!(xattr_denied, !other_reads, "{name} getxattr");
    }
    let private = vfs.lookup(ROOT_INO, "0600", &me()).expect("lookup").ino;
    assert!(matches!(
        vfs.read(private, 0, 1, &stranger()),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(
        matches!(
            vfs.read(private, 0, 0, &stranger()),
            Err(NvstorError::PermissionDenied)
        ),
        "even empty"
    );

    // Directory: no x, no LOOKUP or LOOKUPP; no r, no READDIR.
    fs::create_dir(dir.path().join("d")).expect("mkdir");
    fs::write(dir.path().join("d/f"), b"x").expect("write");
    let d = vfs.lookup(ROOT_INO, "d", &me()).expect("d").ino;
    chmod(&dir.path().join("d"), 0o744);
    assert!(vfs.readdir(d, 0, &stranger()).is_ok());
    assert!(matches!(
        vfs.lookup(d, "f", &stranger()),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(matches!(
        vfs.lookupp(d, &stranger()),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(vfs.lookup(d, "f", &me()).is_ok());
    chmod(&dir.path().join("d"), 0o711);
    assert!(matches!(
        vfs.readdir(d, 0, &stranger()),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(vfs.lookup(d, "f", &stranger()).is_ok());
    assert_eq!(vfs.lookupp(d, &stranger()).expect("lookupp").ino, ROOT_INO);
    chmod(dir.path(), 0o700);
    assert!(matches!(
        vfs.lookup(ROOT_INO, "0644", &stranger()),
        Err(NvstorError::PermissionDenied)
    ));
    chmod(dir.path(), 0o755);

    // ACCESS through the VFS on a read-only export, and OPEN.
    assert_eq!(
        vfs.access(private, ACCESS4_READ | ACCESS4_MODIFY, &me())
            .expect("access"),
        (0x5, 0x1)
    );
    assert_eq!(
        vfs.access(private, ACCESS4_READ | ACCESS4_MODIFY, &stranger())
            .expect("access"),
        (0x5, 0)
    );
    assert!(matches!(
        vfs.check_open(private, 1, 0, &stranger()),
        Err(NvstorError::PermissionDenied)
    ));
    vfs.check_open(private, 1, 0, &me()).expect("owner opens");
}

/// Through the VFS in --read-write mode: data changes, SETATTR, COPY,
/// xattrs and namespace changes by the owner and by others.
#[test]
fn vfs_changes_follow_the_callers_credentials() {
    let dir = tempfile::tempdir().expect("tmpdir");
    chmod(dir.path(), 0o755);
    let vfs = rw(dir.path());
    let other = stranger();
    for (name, mode) in [
        ("0600", 0o600),
        ("0640", 0o640),
        ("0644", 0o644),
        ("0666", 0o666),
    ] {
        fs::write(dir.path().join(name), b"original").expect("write");
        chmod(&dir.path().join(name), mode);
        let ino = vfs.lookup(ROOT_INO, name, &me()).expect("lookup").ino;
        let other_writes = mode & 0o002 != 0;
        assert_eq!(
            vfs.write(ino, 0, b"O", &other).is_ok(),
            other_writes,
            "{name}"
        );
        assert_eq!(
            vfs.allocate(ino, 0, 8, false, &other).is_ok(),
            other_writes,
            "{name} allocate"
        );
        assert_eq!(
            vfs.setattr(ino, Some(8), None, None, None, None, None, &other)
                .is_ok(),
            other_writes,
            "{name} truncate"
        );
        assert_eq!(
            vfs.setxattr(ino, "k", b"v", 0, &other).is_ok(),
            other_writes,
            "{name} setxattr"
        );
        vfs.write(ino, 1, b"W", &me()).expect("owner writes");
        // Only the owner (or root) chmods or sets times, whatever the mode.
        assert!(matches!(
            vfs.setattr(ino, None, Some(0o777), None, None, None, None, &other),
            Err(NvstorError::NotPermitted)
        ));
        assert!(matches!(
            vfs.setattr(ino, None, None, None, None, Some(1), Some(1), &other),
            Err(NvstorError::NotPermitted)
        ));
        assert!(matches!(
            vfs.setattr(ino, None, None, Some(me().uid), None, None, None, &other),
            Err(NvstorError::NotPermitted)
        ));
        vfs.setattr(
            ino,
            None,
            Some(mode),
            Some(me().uid),
            Some(me().gid),
            Some(5),
            Some(6),
            &me(),
        )
        .expect("owner setattr");
        // The owner cannot give the file away.
        assert!(matches!(
            vfs.setattr(ino, None, None, Some(other.uid), None, None, None, &me()),
            Err(NvstorError::NotPermitted)
        ));
    }
    let shared = vfs.lookup(ROOT_INO, "0666", &me()).expect("lookup").ino;
    let private = vfs.lookup(ROOT_INO, "0600", &me()).expect("lookup").ino;
    assert!(
        vfs.copy_file(shared, 0, private, 0, 0, &other).is_err(),
        "COPY into a file it may not write"
    );
    assert!(
        vfs.copy_file(private, 0, shared, 0, 0, &other).is_err(),
        "COPY out of a file it may not read"
    );
    vfs.copy_file(shared, 0, private, 0, 0, &me())
        .expect("owner copies");
    // A chmod by the owner outside the file's group cannot set setgid.
    let outside_group = caller(me().uid, stranger().gid, &[]);
    vfs.setattr(
        private,
        None,
        Some(0o2755),
        None,
        None,
        None,
        None,
        &outside_group,
    )
    .expect("chmod");
    assert_eq!(
        fs::metadata(dir.path().join("0600"))
            .expect("stat")
            .permissions()
            .mode()
            & 0o7777,
        0o755
    );
    chmod(&dir.path().join("0600"), 0o600);
    // xattrs live in user.* only: "k" is stored as user.k.
    vfs.setxattr(private, "k", b"v", 0, &me())
        .expect("setxattr");
    assert_eq!(vfs.getxattr(private, "k", &me()).expect("getxattr"), b"v");
    assert!(vfs
        .listxattrs(private, &me())
        .expect("list")
        .contains(&"k".to_string()));
    assert!(xattr_names(&dir.path().join("0600")).contains(&"user.k".to_string()));
    vfs.removexattr(private, "k", &me()).expect("removexattr");

    // Namespace changes need write and search on the directory.
    assert!(matches!(
        vfs.create_file(ROOT_INO, "new", 0o644, &other),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(matches!(
        vfs.mkdir(ROOT_INO, "newdir", 0o755, &other),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(matches!(
        vfs.create_symlink(ROOT_INO, "newlink", "x", &other),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(matches!(
        vfs.unlink(ROOT_INO, "0666", &other),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(matches!(
        vfs.rename(ROOT_INO, "0666", ROOT_INO, "moved", &other),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(matches!(
        vfs.link(shared, ROOT_INO, "hard", &other),
        Err(NvstorError::PermissionDenied)
    ));
    assert!(fs::symlink_metadata(dir.path().join("new")).is_err());
    // An existing file is opened without write permission on the directory.
    assert!(
        !vfs.create_file(ROOT_INO, "0644", 0o644, &other)
            .expect("existing")
            .1
    );

    // A world-writable sticky directory: anyone creates, only owners remove.
    fs::create_dir(dir.path().join("tmp")).expect("mkdir");
    chmod(&dir.path().join("tmp"), 0o1777);
    let tmp = vfs.lookup(ROOT_INO, "tmp", &me()).expect("tmp").ino;
    vfs.create_file(tmp, "mine", 0o644, &me()).expect("create");
    vfs.mkdir(tmp, "mydir", 0o755, &me()).expect("mkdir");
    let mine = vfs.lookup(tmp, "mine", &me()).expect("mine").ino;
    assert!(matches!(
        vfs.unlink(tmp, "mine", &other),
        Err(NvstorError::NotPermitted)
    ));
    assert!(matches!(
        vfs.rmdir(tmp, "mydir", &other),
        Err(NvstorError::NotPermitted)
    ));
    assert!(matches!(
        vfs.rename(tmp, "mine", tmp, "stolen", &other),
        Err(NvstorError::NotPermitted)
    ));
    // A hard link to a file it can only read (protected_hardlinks).
    assert!(matches!(
        vfs.link(mine, tmp, "pinned", &other),
        Err(NvstorError::NotPermitted)
    ));
    vfs.rename(tmp, "mine", tmp, "renamed", &me())
        .expect("owner renames");
    vfs.unlink(tmp, "renamed", &me()).expect("owner removes");
    vfs.rmdir(tmp, "mydir", &me())
        .expect("owner removes a directory");

    // Moving a directory to another parent changes its "..": that needs
    // write permission on the directory itself, even for its owner.
    fs::create_dir(dir.path().join("tmp/ro")).expect("mkdir");
    chmod(&dir.path().join("tmp/ro"), 0o555);
    fs::create_dir(dir.path().join("dest")).expect("mkdir");
    chmod(&dir.path().join("dest"), 0o777);
    let dest = vfs.lookup(ROOT_INO, "dest", &me()).expect("dest").ino;
    assert!(
        matches!(
            vfs.rename(tmp, "ro", dest, "ro", &member()),
            Err(NvstorError::NotPermitted)
        ),
        "sticky"
    );
    vfs.rename(tmp, "ro", tmp, "ro2", &me())
        .expect("same parent");
    assert!(matches!(
        vfs.rename(tmp, "ro2", dest, "ro", &me()),
        Err(NvstorError::PermissionDenied)
    ));
    chmod(&dir.path().join("tmp/ro2"), 0o755);
    vfs.rename(tmp, "ro2", dest, "ro", &me())
        .expect("owner moves a writable directory");
}

/// Files, directories and symlinks a client creates belong to it. A
/// server that cannot give an inode away (no CAP_CHOWN) refuses the create
/// and leaves nothing behind; a set-group-id directory's group is
/// inherited.
#[test]
fn created_inodes_belong_to_the_caller() {
    use std::os::unix::fs::MetadataExt;
    let dir = tempfile::tempdir().expect("tmpdir");
    chmod(dir.path(), 0o777);
    let vfs = rw(dir.path());
    let me = me();
    let (attrs, created) = vfs
        .create_file(ROOT_INO, "by-me", 0o644, &me)
        .expect("create");
    assert!(created);
    assert_eq!((attrs.uid, attrs.gid), (me.uid, me.gid));
    let privileged = me.uid == 0;
    // The test uid with another primary group, and another uid.
    for cred in [caller(me.uid, stranger().gid, &[]), stranger()] {
        for kind in ["file", "dir", "symlink"] {
            let name = format!("{kind}-{}-{}", cred.uid, cred.gid);
            let result = match kind {
                "file" => vfs
                    .create_file(ROOT_INO, &name, 0o644, &cred)
                    .map(|(a, _)| a),
                "dir" => vfs.mkdir(ROOT_INO, &name, 0o755, &cred),
                _ => vfs.create_symlink(ROOT_INO, &name, "target", &cred),
            };
            let on_disk = fs::symlink_metadata(dir.path().join(&name));
            if privileged {
                let attrs = result.expect("create");
                assert_eq!((attrs.uid, attrs.gid), (cred.uid, cred.gid), "{name}");
                let meta = on_disk.expect("created");
                assert_eq!((meta.uid(), meta.gid()), (cred.uid, cred.gid), "{name}");
            } else {
                assert!(
                    matches!(result, Err(NvstorError::NotPermitted)),
                    "{name}: {result:?}"
                );
                assert!(on_disk.is_err(), "{name} must be removed again");
            }
        }
    }
    // In a set-group-id directory the new inode takes the directory's group,
    // so the caller's own primary group is not needed.
    fs::create_dir(dir.path().join("sg")).expect("mkdir");
    chmod(&dir.path().join("sg"), 0o2777);
    let sg = vfs.lookup(ROOT_INO, "sg", &me).expect("sg").ino;
    let sg_gid = fs::metadata(dir.path().join("sg")).expect("stat").gid();
    let cred = caller(me.uid, stranger().gid, &[]);
    let (attrs, _) = vfs
        .create_file(sg, "f", 0o644, &cred)
        .expect("create in setgid dir");
    assert_eq!((attrs.uid, attrs.gid), (me.uid, sg_gid));
    let sub = vfs
        .mkdir(sg, "d", 0o755, &cred)
        .expect("mkdir in setgid dir");
    assert_eq!(sub.gid, sg_gid);
    assert_ne!(
        sub.mode & libc::S_ISGID as u32,
        0,
        "subdirectories inherit set-group-id"
    );
}

/// A read-only export changes nothing whoever asks, root included.
#[test]
fn read_only_export_refuses_every_caller() {
    let dir = tempfile::tempdir().expect("tmpdir");
    fs::write(dir.path().join("f"), b"data").expect("write");
    chmod(&dir.path().join("f"), 0o666);
    let vfs = PassthroughVfs::new(dir.path()).expect("vfs");
    let f = vfs.lookup(ROOT_INO, "f", &me()).expect("lookup").ino;
    for cred in [me(), stranger(), CallerCtx::root()] {
        assert!(vfs.write(f, 0, b"x", &cred).is_err());
        assert!(vfs.create_file(ROOT_INO, "n", 0o644, &cred).is_err());
        assert!(vfs
            .setattr(f, None, Some(0o600), None, None, None, None, &cred)
            .is_err());
        assert!(vfs.setxattr(f, "k", b"v", 0, &cred).is_err());
        assert!(vfs.unlink(ROOT_INO, "f", &cred).is_err());
        let (_, granted) = vfs.access(f, 0x1ff, &cred).expect("access");
        assert_eq!(
            granted & (ACCESS4_MODIFY | ACCESS4_EXTEND | ACCESS4_XAWRITE),
            0
        );
    }
    assert_eq!(fs::read(dir.path().join("f")).expect("read"), b"data");
}

fn xattr_names(path: &Path) -> Vec<String> {
    let c = CString::new(path.as_os_str().as_bytes()).expect("path");
    let mut buf = vec![0u8; 4096];
    // SAFETY: `c` is NUL-terminated and `buf` has `buf.len()` writable bytes.
    let n =
        unsafe { libc::llistxattr(c.as_ptr(), buf.as_mut_ptr() as *mut libc::c_char, buf.len()) };
    assert!(n >= 0, "llistxattr");
    buf[..n as usize]
        .split(|b| *b == 0)
        .filter(|s| !s.is_empty())
        .map(|s| String::from_utf8_lossy(s).into_owned())
        .collect()
}
