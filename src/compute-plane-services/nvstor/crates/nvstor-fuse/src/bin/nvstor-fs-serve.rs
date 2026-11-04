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

//! nvstor-fs-serve — filesystem RPC server for remote FUSE clients.
//!
//! Usage:
//!   nvstor-fs-serve /path/to/directory
//!   nvstor-fs-serve --port 9879 /var/lib/committed/sha256:abc
//!
//! Client:
//!   nvstor-mount --remote host:9879 /mnt/layer

fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()),
        )
        .init();

    let args: Vec<String> = std::env::args().collect();
    if args.len() < 2 {
        eprintln!("Usage: {} [--port PORT] <directory>", args[0]);
        eprintln!("");
        eprintln!("Serve a directory for remote FUSE clients.");
        eprintln!("Default port: 9879");
        eprintln!("");
        eprintln!("Client: nvstor-mount --remote host:PORT /mnt/target");
        std::process::exit(1);
    }

    let mut port = 9879u16;
    let mut dir_path = None;

    let mut i = 1;
    while i < args.len() {
        match args[i].as_str() {
            "--port" => {
                i += 1;
                port = args.get(i).and_then(|s| s.parse().ok()).unwrap_or(9879);
            }
            _ => dir_path = Some(std::path::PathBuf::from(&args[i])),
        }
        i += 1;
    }

    let dir = match dir_path {
        Some(d) if d.is_dir() => d,
        Some(d) => {
            eprintln!("Error: {} is not a directory", d.display());
            std::process::exit(1);
        }
        None => {
            eprintln!("Error: no directory specified");
            std::process::exit(1);
        }
    };

    tracing::info!(directory = %dir.display(), port, "starting filesystem RPC server");

    let server = nvstor_fuse::fs_server::FsServer::new(dir);
    if let Err(e) = server.run(port) {
        eprintln!("Server failed: {e}");
        std::process::exit(1);
    }
}
