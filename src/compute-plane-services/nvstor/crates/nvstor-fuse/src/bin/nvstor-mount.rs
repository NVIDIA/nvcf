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

//! nvstor-mount — mount a FUSE filesystem (local or remote).
//!
//! Local passthrough:
//!   nvstor-mount /source/dir /mount/point
//!
//! Remote (fetch from nvstor-fs-serve):
//!   nvstor-mount --remote host:9879 /mount/point
//!   nvstor-mount --remote host:9879 --cache-dir /var/cache/nvstor --cache-size 10G /mnt/layer

use std::path::PathBuf;

fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()),
        )
        .init();

    let args: Vec<String> = std::env::args().collect();
    if args.len() < 2 {
        print_usage(&args[0]);
        std::process::exit(1);
    }

    let mut remote_addr: Option<String> = None;
    let mut cache_dir = PathBuf::from("/var/cache/nvstor");
    let mut cache_size: u64 = 10 * 1024 * 1024 * 1024; // 10 GB default
    let mut allow_other = true;
    let mut attr_timeout = 3600u64;
    let mut entry_timeout = 3600u64;
    let mut positional = Vec::new();

    let mut i = 1;
    while i < args.len() {
        match args[i].as_str() {
            "--remote" => {
                i += 1;
                remote_addr = args.get(i).cloned();
            }
            "--cache-dir" => {
                i += 1;
                if let Some(d) = args.get(i) {
                    cache_dir = PathBuf::from(d);
                }
            }
            "--cache-size" => {
                i += 1;
                if let Some(s) = args.get(i) {
                    cache_size = parse_size(s);
                }
            }
            "--allow-other" => allow_other = true,
            "--no-allow-other" => allow_other = false,
            "--attr-timeout" => {
                i += 1;
                attr_timeout = args.get(i).and_then(|s| s.parse().ok()).unwrap_or(3600);
            }
            "--entry-timeout" => {
                i += 1;
                entry_timeout = args.get(i).and_then(|s| s.parse().ok()).unwrap_or(3600);
            }
            "--help" | "-h" => {
                print_usage(&args[0]);
                std::process::exit(0);
            }
            _ => positional.push(args[i].clone()),
        }
        i += 1;
    }

    if let Some(addr) = remote_addr {
        // Remote mode: fetch from nvstor-fs-serve.
        if positional.len() != 1 {
            eprintln!("Error: remote mode requires exactly 1 arg: <mountpoint>");
            eprintln!("Usage: {} --remote host:port /mnt/target", args[0]);
            std::process::exit(1);
        }
        let mountpoint = PathBuf::from(&positional[0]);

        tracing::info!(
            remote = %addr,
            mountpoint = %mountpoint.display(),
            cache_dir = %cache_dir.display(),
            cache_size_mb = cache_size / (1024 * 1024),
            "mounting remote FUSE"
        );

        let fs = match nvstor_fuse::remote::RemoteFs::new(&addr, &cache_dir, cache_size) {
            Ok(fs) => fs.with_ttl(attr_timeout, entry_timeout),
            Err(e) => {
                eprintln!("Error: {e}");
                std::process::exit(1);
            }
        };

        ctrlc::set_handler(|| {
            tracing::info!("signal received, unmounting...");
            std::process::exit(0);
        })
        .ok();

        if let Err(e) = fs.mount(&mountpoint, allow_other) {
            eprintln!("Mount failed: {e}");
            std::process::exit(1);
        }
    } else {
        // Local passthrough mode.
        if positional.len() != 2 {
            eprintln!("Error: passthrough mode requires 2 args: <source_dir> <mountpoint>");
            eprintln!("Usage: {} /source/dir /mnt/target", args[0]);
            std::process::exit(1);
        }
        let source = PathBuf::from(&positional[0]);
        let mountpoint = PathBuf::from(&positional[1]);

        if !source.is_dir() {
            eprintln!("Error: {} is not a directory", source.display());
            std::process::exit(1);
        }

        tracing::info!(
            source = %source.display(),
            mountpoint = %mountpoint.display(),
            "mounting local FUSE passthrough"
        );

        let options = nvstor_fuse::passthrough::MountOptions {
            allow_other,
            attr_timeout_secs: attr_timeout,
            entry_timeout_secs: entry_timeout,
        };

        let fs = match nvstor_fuse::passthrough::PassthroughFs::new(source) {
            Ok(fs) => fs.with_ttl(attr_timeout, entry_timeout),
            Err(e) => {
                eprintln!("Error: {e}");
                std::process::exit(1);
            }
        };

        ctrlc::set_handler(|| {
            tracing::info!("signal received, unmounting...");
            std::process::exit(0);
        })
        .ok();

        if let Err(e) = fs.mount(&mountpoint, options) {
            eprintln!("Mount failed: {e}");
            std::process::exit(1);
        }
    }
}

fn print_usage(prog: &str) {
    eprintln!("Usage:");
    eprintln!("  Local:  {prog} [options] <source_dir> <mountpoint>");
    eprintln!("  Remote: {prog} --remote <host:port> [options] <mountpoint>");
    eprintln!("");
    eprintln!("Options:");
    eprintln!("  --remote HOST:PORT   Connect to remote nvstor-fs-serve");
    eprintln!("  --cache-dir DIR      Local cache directory (default: /var/cache/nvstor)");
    eprintln!("  --cache-size SIZE    Max cache size: 1G, 10G, etc. (default: 10G)");
    eprintln!("  --allow-other        Allow other users (default: enabled)");
    eprintln!("  --no-allow-other     Restrict to mounting user");
    eprintln!("  --attr-timeout N     Attribute cache seconds (default: 3600)");
    eprintln!("  --entry-timeout N    Entry cache seconds (default: 3600)");
    eprintln!("");
    eprintln!("Examples:");
    eprintln!("  # Serve local directory");
    eprintln!("  {prog} /var/lib/committed/sha256:abc /mnt/layer");
    eprintln!("");
    eprintln!("  # Mount remote (CVMFS-style)");
    eprintln!("  # Server: nvstor-fs-serve /var/lib/committed/sha256:abc");
    eprintln!("  # Client: {prog} --remote 10.0.0.1:9879 /mnt/layer");
}

fn parse_size(s: &str) -> u64 {
    let s = s.trim();
    if s.ends_with('G') || s.ends_with('g') {
        s[..s.len() - 1].parse::<u64>().unwrap_or(10) * 1024 * 1024 * 1024
    } else if s.ends_with('M') || s.ends_with('m') {
        s[..s.len() - 1].parse::<u64>().unwrap_or(1024) * 1024 * 1024
    } else {
        s.parse::<u64>().unwrap_or(10 * 1024 * 1024 * 1024)
    }
}
