// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

mod command;

use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

pub struct FakeCommand {
    directory: tempfile::TempDir,
}

impl FakeCommand {
    pub fn new() -> Self {
        let directory = tempfile::tempdir().unwrap();
        let source = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/support/command.rs");
        let compiled = Command::new("rustup")
            .args(["run", "stable", "rustc", "--edition=2024"])
            .arg(source)
            .arg("-o")
            .arg(directory.path().join("command"))
            .output()
            .unwrap();
        assert!(
            compiled.status.success(),
            "{}",
            String::from_utf8_lossy(&compiled.stderr)
        );
        Self { directory }
    }

    pub fn executable(&self) -> PathBuf {
        self.directory.path().join("command")
    }

    pub fn respond(&self, arguments: &[&str], responses: &[(i32, &str, &str)]) {
        assert!(!responses.is_empty());
        let arguments: Vec<OsString> = arguments.iter().map(OsString::from).collect();
        let directory = self
            .directory
            .path()
            .join("responses")
            .join(command::key(&arguments));
        fs::create_dir_all(&directory).unwrap();
        fs::write(
            directory.join("arguments"),
            command::arguments_bytes(&arguments),
        )
        .unwrap();
        Self::write_responses(&directory, responses);
    }

    pub fn default_response(&self, responses: &[(i32, &str, &str)]) {
        assert!(!responses.is_empty());
        let directory = self.directory.path().join("responses/default");
        fs::create_dir_all(&directory).unwrap();
        Self::write_responses(&directory, responses);
    }

    fn write_responses(directory: &Path, responses: &[(i32, &str, &str)]) {
        fs::write(directory.join("count"), responses.len().to_string()).unwrap();
        for (index, (status, stdout, stderr)) in responses.iter().enumerate() {
            fs::write(directory.join(format!("{index}.out")), stdout).unwrap();
            fs::write(directory.join(format!("{index}.err")), stderr).unwrap();
            fs::write(directory.join(format!("{index}.code")), status.to_string()).unwrap();
        }
    }

    pub fn calls(&self) -> Vec<Vec<OsString>> {
        use std::os::unix::ffi::OsStringExt;
        let mut paths: Vec<_> = fs::read_dir(self.directory.path())
            .unwrap()
            .map(|entry| entry.unwrap().path())
            .filter(|path| {
                path.file_name()
                    .unwrap()
                    .to_string_lossy()
                    .starts_with("call-")
            })
            .collect();
        paths.sort();
        paths
            .into_iter()
            .map(|path| {
                let bytes = fs::read(path).unwrap();
                if bytes.is_empty() {
                    return Vec::new();
                }
                bytes[..bytes.len() - 1]
                    .split(|byte| *byte == 0)
                    .map(|part| OsString::from_vec(part.to_vec()))
                    .collect()
            })
            .collect()
    }
}
