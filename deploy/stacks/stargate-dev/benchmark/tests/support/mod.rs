// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

mod command;

use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::OnceLock;

pub struct FakeCommand {
    directory: tempfile::TempDir,
}

/// Compile the stand-in once per test binary, next to that binary. The rustup
/// proxy keeps the toolchain Cargo selected through `RUSTUP_TOOLCHAIN`.
fn compiled() -> &'static Path {
    static COMPILED: OnceLock<PathBuf> = OnceLock::new();
    COMPILED.get_or_init(|| {
        let test_binary = std::env::current_exe().unwrap();
        let name = test_binary.file_name().unwrap().to_string_lossy();
        let compiled = test_binary.with_file_name(format!("{name}-fake-command"));
        let staged = test_binary.with_file_name(format!("{name}-fake-command.tmp"));
        let source = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/support/command.rs");
        let output = Command::new("rustc")
            .args(["--edition=2024", "-C", "debuginfo=0"])
            .arg(source)
            .arg("-o")
            .arg(&staged)
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        fs::rename(&staged, &compiled).unwrap();
        compiled
    })
}

impl FakeCommand {
    pub fn new() -> Self {
        // The stand-in keeps per-test state beside its own path, so each test
        // needs a private link. A hard link avoids holding a writable descriptor
        // that a concurrently forked child could inherit (ETXTBSY).
        let compiled = compiled();
        let directory = tempfile::Builder::new()
            .prefix("fake-command-")
            .tempdir_in(compiled.parent().unwrap())
            .unwrap();
        fs::hard_link(compiled, directory.path().join("command")).unwrap();
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
