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

//! Smoke tests for the `nvstor-nfs-export` binary.
//!
//! These catch regressions in the CLI surface and server wiring — places
//! pure-library tests miss (tokio runtime feature flags, argv parsing,
//! the passthrough VFS → CompoundProcessor → NfsServer chain as shipped).
//! Protocol correctness is covered by `tests/integration.rs`.

use std::net::TcpStream;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

/// Path to the `nvstor-nfs-export` binary, provided by Cargo when running
/// `cargo test`.
fn binary_path() -> &'static str {
    env!("CARGO_BIN_EXE_nvstor-nfs-export")
}

/// Running the binary with no arguments must print usage and exit
/// non-zero — not panic, not hang.
#[test]
fn binary_without_args_prints_usage() {
    let out = Command::new(binary_path())
        .output()
        .expect("spawn nvstor-nfs-export");
    assert!(
        !out.status.success(),
        "no-arg invocation should exit non-zero"
    );
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        stderr.contains("Usage"),
        "stderr should contain 'Usage', got: {stderr}"
    );
    assert!(
        stderr.contains("--port"),
        "stderr should mention --port flag"
    );
}

/// Running the binary against a non-existent directory must fail cleanly
/// with a clear error — not a panic, not a silent success.
#[test]
fn binary_with_missing_directory_fails_cleanly() {
    let out = Command::new(binary_path())
        .arg("/nonexistent/path/that/cannot/possibly/exist")
        .output()
        .expect("spawn");
    assert!(!out.status.success());
    let stderr = String::from_utf8_lossy(&out.stderr);
    assert!(
        stderr.contains("not a directory") || stderr.contains("Error"),
        "expected a clear error, got: {stderr}"
    );
}

#[test]
fn rdma_cli_help_and_invalid_port_are_explicit() {
    let help = Command::new(binary_path()).arg("--help").output().unwrap();
    assert!(help.status.success());
    assert!(String::from_utf8_lossy(&help.stderr).contains("--rdma-port"));
    let dir = tempfile::tempdir().unwrap();
    let invalid = Command::new(binary_path())
        .args(["--rdma-port", "not-a-port"])
        .arg(dir.path())
        .output()
        .unwrap();
    assert!(!invalid.status.success());
    assert!(String::from_utf8_lossy(&invalid.stderr).contains("--rdma-port requires"));
}

/// The root-squash flags are documented and validated.
#[test]
fn root_squash_flags_are_documented_and_validated() {
    let help = Command::new(binary_path()).arg("--help").output().unwrap();
    let help = String::from_utf8_lossy(&help.stderr);
    for flag in ["--no-root-squash", "--anon-uid", "--anon-gid"] {
        assert!(help.contains(flag), "--help must document {flag}");
    }
    let dir = tempfile::tempdir().unwrap();
    for (flag, value) in [
        ("--anon-uid", "nobody"),
        ("--anon-gid", "-1"),
        ("--anon-uid", "4294967296"),
    ] {
        let out = Command::new(binary_path())
            .args([
                "--rdma-port",
                "0",
                "--allow-client",
                "127.0.0.1",
                flag,
                value,
            ])
            .arg(dir.path())
            .output()
            .unwrap();
        assert!(!out.status.success(), "{flag} {value}");
        assert!(
            String::from_utf8_lossy(&out.stderr).contains(&format!("{flag} requires")),
            "{flag} {value}"
        );
    }
}

/// The filehandle key must not be readable through the export, and a bad
/// key file stops startup instead of silently sealing with a random key.
#[test]
fn fh_key_inside_export_or_malformed_is_refused() {
    let dir = tempfile::tempdir().unwrap();
    let inside = Command::new(binary_path())
        .args([
            "--rdma-port",
            "0",
            "--allow-client",
            "127.0.0.1",
            "--fh-key",
        ])
        .arg(dir.path().join("fh.key"))
        .arg(dir.path())
        .output()
        .unwrap();
    assert!(!inside.status.success());
    assert!(String::from_utf8_lossy(&inside.stderr).contains("must not lie inside the export"));
    assert!(!dir.path().join("fh.key").exists());

    let keys = tempfile::tempdir().unwrap();
    let bad = keys.path().join("short.key");
    std::fs::write(&bad, b"abc").unwrap();
    let malformed = Command::new(binary_path())
        .args([
            "--rdma-port",
            "0",
            "--allow-client",
            "127.0.0.1",
            "--fh-key",
        ])
        .arg(&bad)
        .arg(dir.path())
        .output()
        .unwrap();
    assert!(!malformed.status.success());
    assert!(String::from_utf8_lossy(&malformed.stderr).contains("--fh-key"));
}

/// Full end-to-end: launch the binary on a high port, verify it binds and
/// accepts TCP connections, then shut it down.  This proves the tokio
/// runtime feature-flag wiring (`rt-multi-thread`), the server bootstrap,
/// and the passthrough VFS creation all work as a real user would see them.
#[test]
fn binary_launches_binds_and_accepts_tcp() {
    let dir = tempfile::tempdir().expect("tmpdir");
    // Pick a high port unlikely to collide.  If we hit an occupied port
    // the spawn step will log an error; bind waits below will time out and
    // the test will fail — a flake, not a false pass.
    let port = 32049u16;
    let keys = tempfile::tempdir().expect("tmpdir");
    let key = keys.path().join("fh.key");

    let mut child = Command::new(binary_path())
        .arg("--port")
        .arg(port.to_string())
        .args(["--rdma-port", "0", "--allow-client", "127.0.0.1"])
        .arg("--fh-key")
        .arg(&key)
        .arg(dir.path())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("spawn server");

    // Poll for up to 5 s waiting for the socket to accept.
    let deadline = Instant::now() + Duration::from_secs(5);
    let addr = format!("127.0.0.1:{port}");
    let mut connected = false;
    while Instant::now() < deadline {
        if TcpStream::connect_timeout(&addr.parse().expect("addr"), Duration::from_millis(200))
            .is_ok()
        {
            connected = true;
            break;
        }
        std::thread::sleep(Duration::from_millis(100));
    }

    // Clean up first; then assert.
    let _ = child.kill();
    let _ = child.wait();

    assert!(
        connected,
        "server did not accept TCP on port {port} within 5 s \
         (port conflict? build broken?)"
    );
    use std::os::unix::fs::PermissionsExt;
    let meta = std::fs::metadata(&key).expect("--fh-key file created");
    assert_eq!((meta.len(), meta.permissions().mode() & 0o777), (32, 0o600));
}

/// The export has no implicit "everyone": without --allow-client it
/// refuses to start, and a malformed pattern is an error, not a pattern
/// that silently matches nobody.
#[test]
fn allow_client_is_required_and_validated() {
    let dir = tempfile::tempdir().unwrap();
    let missing = Command::new(binary_path())
        .args(["--rdma-port", "0"])
        .arg(dir.path())
        .output()
        .unwrap();
    assert!(!missing.status.success());
    let stderr = String::from_utf8_lossy(&missing.stderr);
    assert!(stderr.contains("no --allow-client"), "{stderr}");
    for bad in ["10.0.0.0/33", "fd00::/129", "everyone", ""] {
        let out = Command::new(binary_path())
            .args(["--rdma-port", "0", "--allow-client", bad])
            .arg(dir.path())
            .output()
            .unwrap();
        assert!(!out.status.success(), "{bad:?} accepted");
        assert!(
            String::from_utf8_lossy(&out.stderr).contains("--allow-client requires"),
            "{bad:?}"
        );
    }
    let help = Command::new(binary_path()).arg("--help").output().unwrap();
    assert!(String::from_utf8_lossy(&help.stderr).contains("--allow-client CIDR"));
}

/// Start the export on `port` admitting only `allow`, connect from
/// 127.0.0.1, and report whether the server closed the connection.
fn closed_by_server(port: u16, allow: &str) -> bool {
    use std::io::Read;
    let dir = tempfile::tempdir().expect("tmpdir");
    let mut child = Command::new(binary_path())
        .args([
            "--rdma-port",
            "0",
            "--metrics-port",
            "0",
            "--allow-client",
            allow,
            "--port",
        ])
        .arg(port.to_string())
        .arg(dir.path())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn server");
    let deadline = Instant::now() + Duration::from_secs(5);
    let addr = format!("127.0.0.1:{port}").parse().expect("addr");
    let mut stream = None;
    while stream.is_none() && Instant::now() < deadline {
        stream = TcpStream::connect_timeout(&addr, Duration::from_millis(200)).ok();
        if stream.is_none() {
            std::thread::sleep(Duration::from_millis(100));
        }
    }
    let closed = stream.map(|mut s| {
        s.set_read_timeout(Some(Duration::from_millis(500)))
            .expect("timeout");
        // EOF (or a reset) means the server dropped us; a timeout means the
        // connection is being served.
        !matches!(s.read(&mut [0u8; 1]), Err(e) if e.kind() == std::io::ErrorKind::WouldBlock
            || e.kind() == std::io::ErrorKind::TimedOut)
    });
    let _ = child.kill();
    let _ = child.wait();
    closed.expect("server never accepted a connection")
}

/// --allow-client is enforced at accept.
#[test]
fn allow_client_admits_only_listed_clients() {
    assert!(
        !closed_by_server(32051, "127.0.0.0/8"),
        "listed client must be served"
    );
    assert!(
        closed_by_server(32052, "10.255.255.1"),
        "unlisted client must be dropped"
    );
    assert!(
        closed_by_server(32053, "::/0"),
        "an IPv6 prefix admits no IPv4 client"
    );
}

/// Whether something accepts TCP on `addr` within 5 s.
fn accepts_within(addr: &str) -> bool {
    let addr: std::net::SocketAddr = addr.parse().expect("addr");
    let deadline = Instant::now() + Duration::from_secs(5);
    while Instant::now() < deadline {
        if TcpStream::connect_timeout(&addr, Duration::from_millis(200)).is_ok() {
            return true;
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    false
}

/// --bind picks the TCP listen addresses; nothing else is bound.
#[test]
fn bind_limits_the_tcp_listener_to_named_addresses() {
    let dir = tempfile::tempdir().expect("tmpdir");
    let port = 32061u16;
    let mut child = Command::new(binary_path())
        .args([
            "--rdma-port",
            "0",
            "--metrics-port",
            "0",
            "--allow-client",
            "127.0.0.0/8",
            "--bind",
            "127.0.0.2",
            "--port",
        ])
        .arg(port.to_string())
        .arg(dir.path())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn server");
    let on_bound = accepts_within(&format!("127.0.0.2:{port}"));
    let on_other = TcpStream::connect_timeout(
        &format!("127.0.0.1:{port}").parse().expect("addr"),
        Duration::from_millis(300),
    )
    .is_ok();
    let _ = child.kill();
    let _ = child.wait();
    assert!(on_bound, "server did not accept on the --bind address");
    assert!(
        !on_other,
        "server accepted on an address that was not bound"
    );
}

/// Bad --bind values and contradictory flags are refused, and
/// --no-tcp without an RDMA listener has nothing to serve.
#[test]
fn bind_and_no_tcp_are_validated() {
    let dir = tempfile::tempdir().expect("tmpdir");
    let run = |args: &[&str]| {
        let out = Command::new(binary_path())
            .args(["--metrics-port", "0", "--allow-client", "127.0.0.1"])
            .args(args)
            .arg(dir.path())
            .output()
            .expect("spawn");
        (
            out.status.success(),
            String::from_utf8_lossy(&out.stderr).into_owned(),
        )
    };
    for bad in ["localhost", "10.0.0.1:2049", "", "fd00::1/64"] {
        let (ok, stderr) = run(&["--rdma-port", "0", "--bind", bad]);
        assert!(!ok, "--bind {bad:?} accepted");
        assert!(stderr.contains("--bind requires"), "{bad:?}: {stderr}");
    }
    let (ok, stderr) = run(&["--rdma-port", "0", "--no-tcp", "--bind", "127.0.0.1"]);
    assert!(!ok);
    assert!(stderr.contains("--no-tcp"), "{stderr}");
    let (ok, stderr) = run(&["--rdma-port", "0", "--no-tcp"]);
    assert!(!ok, "--no-tcp without RDMA must not start");
    assert!(stderr.contains("nothing to serve"), "{stderr}");
    let (ok, stderr) = run(&["--rdma-port", "0", "--no-tcp", "--pnfs-ds", "10.0.0.1"]);
    assert!(!ok);
    assert!(stderr.contains("--pnfs-ds"), "{stderr}");
    let help = Command::new(binary_path())
        .arg("--help")
        .output()
        .expect("spawn");
    let help = String::from_utf8_lossy(&help.stderr);
    assert!(
        help.contains("--bind ADDR") && help.contains("--no-tcp"),
        "{help}"
    );
}
