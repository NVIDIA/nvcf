// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::fs::{self, File};
use std::io::{BufReader, Read, Write};
use std::path::{Component, Path, PathBuf};

use anyhow::{Context, Result, ensure};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

pub fn below(output: &Path, relative: &Path) -> Result<PathBuf> {
    ensure!(
        !relative.as_os_str().is_empty()
            && relative
                .components()
                .all(|part| matches!(part, Component::Normal(_))),
        "artifact path must remain below the output root"
    );
    let path = output.join(relative);
    let mut existing = path.as_path();
    loop {
        match fs::symlink_metadata(existing) {
            Ok(_) => {
                ensure!(
                    existing.canonicalize()?.starts_with(output),
                    "artifact symlink escapes the output root"
                );
                break;
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                existing = existing
                    .parent()
                    .context("artifact has no existing parent")?;
            }
            Err(error) => return Err(error).context("inspect artifact path"),
        }
    }
    Ok(path)
}

pub fn read_json<T: for<'de> Deserialize<'de>>(path: &Path) -> Result<T> {
    serde_json::from_reader(BufReader::new(
        File::open(path).with_context(|| format!("open {}", path.display()))?,
    ))
    .with_context(|| format!("parse {}", path.display()))
}

pub fn atomic_json(path: &Path, value: &impl Serialize) -> Result<()> {
    let parent = path.parent().context("artifact has no parent directory")?;
    fs::create_dir_all(parent)?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent)?;
    serde_json::to_writer_pretty(&mut temporary, value)?;
    temporary.write_all(b"\n")?;
    temporary.as_file().sync_all()?;
    temporary
        .persist(path)
        .with_context(|| format!("commit {}", path.display()))?;
    File::open(parent)?.sync_all()?;
    Ok(())
}

pub fn hash_file(path: &Path) -> Result<String> {
    let mut file =
        File::open(path).with_context(|| format!("open {} for hashing", path.display()))?;
    let mut hasher = Sha256::new();
    let mut buffer = [0_u8; 64 * 1024];
    loop {
        let count = file.read(&mut buffer)?;
        if count == 0 {
            break;
        }
        hasher.update(&buffer[..count]);
    }
    Ok(format!("{:x}", hasher.finalize()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn rejects_paths_and_parent_symlinks_outside_the_managed_root() -> Result<()> {
        let root = tempfile::tempdir()?;
        let outside = tempfile::tempdir()?;
        std::os::unix::fs::symlink(outside.path(), root.path().join("escape"))?;
        for relative in ["../outside", "/absolute", "escape/new/file.json", ""] {
            assert!(
                below(root.path(), Path::new(relative)).is_err(),
                "accepted {relative}"
            );
        }
        assert_eq!(
            below(root.path(), Path::new("new/file.json"))?,
            root.path().join("new/file.json")
        );
        Ok(())
    }

    #[test]
    fn replaces_json_and_hashes_the_committed_bytes() -> Result<()> {
        let directory = tempfile::tempdir()?;
        let path = directory.path().join("record.json");
        atomic_json(&path, &json!({"phase":"before"}))?;
        let expected = json!({"phase":"after"});
        atomic_json(&path, &expected)?;
        assert_eq!(read_json::<serde_json::Value>(&path)?, expected);
        assert_eq!(
            hash_file(&path)?,
            format!("{:x}", Sha256::digest(fs::read(&path)?))
        );
        Ok(())
    }
}
