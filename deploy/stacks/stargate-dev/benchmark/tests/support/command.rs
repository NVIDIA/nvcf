// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::ffi::OsString;

#[cfg(not(test))]
#[path = "pod.rs"]
mod pod;

pub fn arguments_bytes(arguments: &[OsString]) -> Vec<u8> {
    let mut encoded = Vec::new();
    for argument in arguments {
        encoded.extend_from_slice(argument.as_encoded_bytes());
        encoded.push(0);
    }
    encoded
}

pub fn key(arguments: &[OsString]) -> String {
    // Only a fixture filename. The full argument bytes are also compared.
    let hash = arguments_bytes(arguments)
        .iter()
        .fold(0xcbf29ce484222325_u64, |hash, byte| {
            (hash ^ u64::from(*byte)).wrapping_mul(0x100000001b3)
        });
    format!("{hash:016x}")
}

#[cfg(not(test))]
fn main() -> std::io::Result<()> {
    use std::fs::{self, OpenOptions};
    use std::io::{Read, Seek, SeekFrom, Write};

    let executable = std::env::current_exe()?;
    let root = executable.parent().unwrap();
    let arguments: Vec<_> = std::env::args_os().skip(1).collect();
    let mut counter = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(root.join("counter"))?;
    counter.lock()?;
    let mut previous = String::new();
    counter.read_to_string(&mut previous)?;
    let index = previous.parse::<usize>().unwrap_or(0);
    fs::write(
        root.join(format!("call-{index:06}")),
        arguments_bytes(&arguments),
    )?;
    counter.seek(SeekFrom::Start(0))?;
    counter.set_len(0)?;
    write!(counter, "{}", index + 1)?;
    counter.unlock()?;

    let barrier = root.join("wait-for-calls");
    if barrier.exists() {
        let expected = fs::read_to_string(barrier)?.parse::<usize>().unwrap();
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
        loop {
            let mut calls = 0;
            for entry in fs::read_dir(root)? {
                if entry?.file_name().to_string_lossy().starts_with("call-") {
                    calls += 1;
                }
            }
            if calls >= expected {
                break;
            }
            if std::time::Instant::now() >= deadline {
                return Err(std::io::Error::other("parallel command barrier timed out"));
            }
            std::thread::sleep(std::time::Duration::from_millis(10));
        }
    }

    if root.join("pod-fixture").exists() && pod::run(root, &arguments)? {
        return Ok(());
    }

    let keyed = root.join("responses").join(key(&arguments));
    let response = if keyed.is_dir() {
        keyed
    } else {
        root.join("responses/default")
    };
    if response.join("arguments").exists()
        && fs::read(response.join("arguments"))? != arguments_bytes(&arguments)
    {
        return Err(std::io::Error::other("fixture argument hash collision"));
    }
    let mut counter = OpenOptions::new()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(response.join("counter"))?;
    counter.lock()?;
    let mut previous = String::new();
    counter.read_to_string(&mut previous)?;
    let index = previous.parse::<usize>().unwrap_or(0);
    let count = fs::read_to_string(response.join("count"))?
        .parse::<usize>()
        .unwrap();
    let selected = index.min(count - 1);
    counter.seek(SeekFrom::Start(0))?;
    counter.set_len(0)?;
    write!(counter, "{}", index + 1)?;
    counter.unlock()?;
    std::io::stdout().write_all(&fs::read(response.join(format!("{selected}.out")))?)?;
    std::io::stderr().write_all(&fs::read(response.join(format!("{selected}.err")))?)?;
    let status = fs::read_to_string(response.join(format!("{selected}.code")))?
        .parse()
        .unwrap();
    std::process::exit(status);
}
