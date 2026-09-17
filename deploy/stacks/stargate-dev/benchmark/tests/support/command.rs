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

    if root.join("forward-exit-on-start").exists()
        && arguments.iter().any(|argument| argument == "port-forward")
    {
        let port = arguments
            .last()
            .unwrap()
            .to_str()
            .unwrap()
            .split_once(':')
            .unwrap()
            .0
            .parse::<u16>()
            .unwrap();
        let _listener = std::net::TcpListener::bind(("127.0.0.1", port))?;
        for _ in 0..1000 {
            if root.join("started").exists() {
                return Ok(());
            }
            std::thread::sleep(std::time::Duration::from_millis(10));
        }
        return Err(std::io::Error::other("fixture traffic never started"));
    }

    if root.join("pod-fixture").exists() && pod::run(root, &arguments)? {
        return Ok(());
    }

    if root.join("docker-report.json").exists()
        && arguments
            .first()
            .is_some_and(|arg| arg == "container" || arg == "image")
    {
        return docker(root, &arguments, index);
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

#[cfg(not(test))]
fn docker(root: &std::path::Path, args: &[OsString], index: usize) -> std::io::Result<()> {
    use std::fs;
    let args: Vec<_> = args.iter().map(|arg| arg.to_str().unwrap()).collect();
    let value = |flag: &str| args[args.iter().position(|arg| *arg == flag).unwrap() + 1];
    if args[0] == "image" {
        println!("sha256:{}", "a".repeat(64));
        return Ok(());
    }
    let containers = root.join("containers");
    fs::create_dir_all(&containers)?;
    match args[1] {
        "create" => {
            let id = format!("{index:064x}");
            let name = value("--name");
            let labels: Vec<_> = args
                .windows(2)
                .filter(|pair| pair[0] == "--label")
                .map(|pair| pair[1].split_once('=').unwrap().1)
                .collect();
            let output = value("-v").strip_suffix(":/campaign").unwrap();
            let report = value("--output").strip_prefix("/campaign/").unwrap();
            fs::write(
                containers.join(&id),
                format!(
                    "{name}\n{}\n{}\n{output}/{report}\ncreated",
                    labels[0], labels[1]
                ),
            )?;
            fs::write(value("--cidfile"), &id)?;
            println!("{id}");
        }
        "start" => {
            let id = args.last().unwrap();
            let path = containers.join(id);
            let record = fs::read_to_string(&path)?;
            let fields: Vec<_> = record.lines().collect();
            if root.join("hold-start").exists() {
                fs::write(&path, format!("{}\nrunning", fields[..4].join("\n")))?;
                fs::write(root.join("started"), id)?;
                // Bound the fixture so a failed test cannot leave a lasting child.
                for _ in 0..1000 {
                    if !path.exists() {
                        return Ok(());
                    }
                    std::thread::sleep(std::time::Duration::from_millis(10));
                }
                return Err(std::io::Error::other("fixture start was not cancelled"));
            }
            let report = if fields[3].ends_with("/warm/spark.json") {
                "docker-warm-report.json"
            } else {
                "docker-report.json"
            };
            fs::copy(root.join(report), fields[3])?;
            fs::write(path, format!("{}\nexited", fields[..4].join("\n")))?;
        }
        "inspect" => {
            let requested = args[2];
            for entry in fs::read_dir(containers)? {
                let entry = entry?;
                let record = fs::read_to_string(entry.path())?;
                let fields: Vec<_> = record.lines().collect();
                if entry.file_name() != requested && fields[0] != requested {
                    continue;
                }
                println!(
                    r#"[{{"Id":"{}","Name":"/{}","Config":{{"Labels":{{"nvcf.stargate-bench.owner":"{}","nvcf.stargate-bench.campaign":"{}"}}}},"State":{{"Status":"{}","Running":{},"ExitCode":0}}}}]"#,
                    entry.file_name().to_str().unwrap(),
                    fields[0],
                    fields[1],
                    fields[2],
                    fields[4],
                    fields[4] == "running"
                );
                return Ok(());
            }
            eprintln!("No such container");
            std::process::exit(1);
        }
        "rm" => fs::remove_file(containers.join(args.last().unwrap()))?,
        operation => {
            return Err(std::io::Error::other(format!(
                "unexpected Docker operation {operation}"
            )));
        }
    }
    Ok(())
}
