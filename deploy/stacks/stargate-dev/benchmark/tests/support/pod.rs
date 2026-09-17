// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::ffi::{OsStr, OsString};
use std::fs;
use std::io;
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::Command;

pub fn run(root: &Path, arguments: &[OsString]) -> io::Result<bool> {
    let invoked = std::env::args_os().next().unwrap();
    if Path::new(&invoked).file_name() == Some(OsStr::new("spark")) {
        if arguments == ["--version"] {
            println!("Spark fixture 1");
        } else {
            let output = arguments
                .windows(2)
                .find(|pair| pair[0] == "--output")
                .ok_or_else(|| io::Error::other("Spark output missing"))?;
            let output = Path::new(&output[1]);
            let stream = output
                .parent()
                .unwrap()
                .file_name()
                .unwrap()
                .to_str()
                .unwrap();
            if root.join("hold-mixed-launches").exists() && matches!(stream, "hot" | "short") {
                fs::write(
                    root.join(format!("spark-running-{stream}")),
                    std::process::id().to_string(),
                )?;
                std::thread::sleep(std::time::Duration::from_secs(30));
                return Err(io::Error::other("mixed Spark fixture was not cancelled"));
            }
            let report = if output.ends_with("warm/spark.json") {
                "docker-warm-report.json"
            } else {
                "docker-report.json"
            };
            fs::copy(root.join(report), output)?;
        }
        return Ok(true);
    }
    if arguments.windows(2).any(|pair| pair == ["get", "pod"])
        && arguments.iter().any(|argument| argument == "spark-test")
    {
        print!("{}", fs::read_to_string(root.join("pod.json"))?);
        return Ok(true);
    }
    if arguments.iter().any(|argument| argument == "exec") {
        let separator = arguments
            .iter()
            .position(|argument| argument == "--")
            .ok_or_else(|| io::Error::other("kubectl exec separator missing"))?;
        let arguments: Vec<_> = arguments[separator + 1..]
            .iter()
            .map(|argument| remote_path(root, argument))
            .collect();
        let mut command = Command::new(&arguments[0]);
        command
            .args(&arguments[1..])
            .env("PATH", format!("{}:/usr/bin:/bin", root.display()));
        // The simulated container has a process group separate from kubectl.
        let status = command.process_group(0).status()?;
        if !status.success() {
            std::process::exit(status.code().unwrap_or(1));
        }
        if root.join("lose-launch-ack").exists()
            && arguments.iter().any(|argument| {
                argument
                    .to_string_lossy()
                    .starts_with("IFS= read -r OPENAI_API_KEY")
            })
        {
            eprintln!("connection reset after remote launch");
            std::process::exit(1);
        }
        if root.join("hold-mixed-launches").exists()
            && arguments.iter().any(|argument| {
                argument
                    .to_string_lossy()
                    .starts_with("IFS= read -r OPENAI_API_KEY")
            })
        {
            let output = arguments
                .windows(2)
                .find(|pair| pair[0] == "--output")
                .unwrap();
            let stream = Path::new(&output[1])
                .parent()
                .unwrap()
                .file_name()
                .unwrap()
                .to_str()
                .unwrap();
            if matches!(stream, "hot" | "short") {
                fs::write(
                    root.join(format!("ack-held-{stream}")),
                    std::process::id().to_string(),
                )?;
                std::thread::sleep(std::time::Duration::from_secs(30));
                return Err(io::Error::other(
                    "mixed acknowledgement fixture was not cancelled",
                ));
            }
        }
        return Ok(true);
    }
    if let Some(copy) = arguments.iter().position(|argument| argument == "cp") {
        let mut paths = Vec::new();
        let mut args = arguments[copy + 1..].iter();
        while let Some(argument) = args.next() {
            if argument == "-c" || argument == "--container" || argument == "--retries" {
                args.next();
                continue;
            }
            if argument.as_encoded_bytes().starts_with(b"--") {
                continue;
            }
            let text = argument.to_str().unwrap();
            let path = text.split_once(':').map_or(text, |(_, path)| path);
            paths.push(PathBuf::from(remote_path(root, OsStr::new(path))));
        }
        if paths.len() != 2 {
            return Err(io::Error::other("kubectl cp requires two paths"));
        }
        let destination = if paths[1].is_dir() {
            paths[1].join(paths[0].file_name().unwrap())
        } else {
            paths[1].clone()
        };
        copy_tree(&paths[0], &destination)?;
        return Ok(true);
    }
    Ok(false)
}

fn remote_path(root: &Path, value: &OsStr) -> OsString {
    value
        .to_str()
        .unwrap()
        .replace(
            "/tmp/stargate-bench/",
            &format!("{}/remote/", root.display()),
        )
        .into()
}

fn copy_tree(source: &Path, destination: &Path) -> io::Result<()> {
    if source.is_dir() {
        fs::create_dir_all(destination)?;
        for entry in fs::read_dir(source)? {
            let entry = entry?;
            copy_tree(&entry.path(), &destination.join(entry.file_name()))?;
        }
    } else {
        fs::copy(source, destination)?;
    }
    Ok(())
}
