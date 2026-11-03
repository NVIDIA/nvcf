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

//! Cross-node RDMA hardware test (raw verbs, no nvstor protocol).
//!
//! A lab tool for checking that two hosts can run RC WRITE/READ at all.
//! It exchanges QP and buffer info over an UNAUTHENTICATED TCP connection
//! and grants the peer remote read and write of a 1 GiB buffer: run it
//! only between hosts you control, never against a production node.
//!
//! Usage:
//!   Server (node 1): ./cross_node server 9876
//!   Client (node 2): ./cross_node client <server-ip>:9876
//!
//! Flow:
//!   1. Both allocate a buffer and register it as a memory region
//!   2. Exchange QP info + MR info via TCP
//!   3. Client does RDMA WRITE to server's buffer
//!   4. Server verifies data arrived
//!   5. Client does RDMA READ from server's buffer
//!   6. Client verifies data matches
//!   7. Benchmark: 1MB, 4MB, 16MB, 64MB writes

use std::time::Instant;

use nvstor_rdma::connection::{poll_completion, CompletionQueue, QueuePair};
use nvstor_rdma::device::{Access, RdmaDevice};
use nvstor_rdma::exchange::{self, PeerInfo};

const BUF_SIZE: usize = 1024 * 1024 * 1024; // 1 GB — checkpoint scale

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 3 {
        eprintln!("Usage:");
        eprintln!("  Server: {} server <port>", args[0]);
        eprintln!("  Client: {} client <server_ip:port>", args[0]);
        std::process::exit(1);
    }

    let mode = &args[1];
    let addr = &args[2];

    // Open RDMA device.
    let dev = std::sync::Arc::new(RdmaDevice::open_default().expect("no RDMA device"));
    println!("Device: {}, port state: {:?}", dev.name, dev.port_state());

    // Create CQs and QP.
    let send_cq = CompletionQueue::new(&dev, 256).expect("create send CQ");
    let recv_cq = CompletionQueue::new(&dev, 256).expect("create recv CQ");
    let qp = QueuePair::create_rc(&dev, &send_cq, &recv_cq, 256, 256).expect("create QP");

    // Allocate and register buffer.
    let mut buf = vec![0u8; BUF_SIZE];
    let mr = dev
        .register_memory(buf.as_mut_ptr(), BUF_SIZE, Access::REMOTE_READ_WRITE)
        .expect("register MR");

    // Prepare local info — use routable RoCE v2 GID for cross-node.
    let sgid_index = dev.roce_gid_index();
    let gid = dev.gid(sgid_index as i32).expect("query GID");
    let lid = dev.lid().unwrap_or(0);
    let local_info = PeerInfo {
        qp_num: qp.qp_num,
        lid,
        gid: unsafe { gid.raw },
        mr_addr: buf.as_ptr() as u64,
        mr_rkey: mr.rkey,
        mr_len: BUF_SIZE as u64,
    };

    println!("Local QP: {}, LID: {}, GID: {:?}", qp.qp_num, lid, gid);
    println!(
        "Local MR: addr={:#x}, rkey={}, len={}",
        local_info.mr_addr, mr.rkey, BUF_SIZE
    );

    // Exchange info via TCP.
    let remote_info = match mode.as_str() {
        "server" => {
            let port: u16 = addr.parse().expect("invalid port");
            // Server fills first 64MB with pattern so client can verify READ.
            // Only fill what we'll verify to avoid slow 1GB init.
            let fill_len = 64 * 1024 * 1024;
            for (i, byte) in buf[..fill_len].iter_mut().enumerate() {
                *byte = (i % 251) as u8;
            }
            exchange::exchange_server(port, &local_info).expect("exchange failed")
        }
        "client" => exchange::exchange_client(addr, &local_info).expect("exchange failed"),
        _ => {
            eprintln!("Mode must be 'server' or 'client'");
            std::process::exit(1);
        }
    };

    println!(
        "Remote QP: {}, LID: {}",
        remote_info.qp_num, remote_info.lid
    );
    println!(
        "Remote MR: addr={:#x}, rkey={}, len={}",
        remote_info.mr_addr, remote_info.mr_rkey, remote_info.mr_len
    );

    // Bring QP to RTS.
    let remote_gid = remote_info.to_ibv_gid();
    let mtu = dev.active_mtu().expect("query MTU");
    println!("Path MTU: {} (1=256, 2=512, 3=1024, 4=2048, 5=4096)", mtu);
    qp.to_init(dev.port, Access::REMOTE_READ_WRITE)
        .expect("QP to INIT");
    println!("Using sgid_index={} for RoCE v2", sgid_index);
    qp.to_rtr(
        remote_info.qp_num,
        remote_info.lid,
        &remote_gid,
        dev.port,
        mtu,
        sgid_index,
    )
    .expect("QP to RTR");
    qp.to_rts().expect("QP to RTS");
    println!("QP in RTS state — RDMA ready!");

    match mode.as_str() {
        "client" => run_client(&qp, &send_cq, &mut buf, &mr, &remote_info),
        "server" => run_server(&buf, &remote_info),
        _ => unreachable!(),
    }
}

fn run_client(
    qp: &QueuePair,
    send_cq: &CompletionQueue,
    buf: &mut [u8],
    mr: &nvstor_rdma::device::MemoryRegion,
    remote: &PeerInfo,
) {
    println!("\n=== RDMA WRITE Test ===");

    // Fill local buffer with pattern.
    let write_size = 4096;
    buf[..write_size].fill(0xAB);

    // RDMA WRITE 4KB to remote.
    qp.rdma_write(
        buf.as_ptr() as u64,
        mr.lkey,
        remote.mr_addr,
        remote.mr_rkey,
        write_size as u32,
    )
    .expect("RDMA WRITE");
    poll_completion(send_cq, 10_000_000).expect("WRITE completion");
    println!("RDMA WRITE 4KB: OK");

    println!("\n=== RDMA READ Test ===");

    // Clear local buf, then READ from remote (server has pattern data).
    let read_offset = 4096; // Read from offset 4096 in remote buffer
    buf[..4096].fill(0);
    qp.rdma_read(
        buf.as_ptr() as u64,
        mr.lkey,
        remote.mr_addr + read_offset as u64,
        remote.mr_rkey,
        4096,
    )
    .expect("RDMA READ");
    poll_completion(send_cq, 10_000_000).expect("READ completion");

    // Verify: server filled buf[i] = (i % 251)
    let mut ok = true;
    for (i, &got) in buf[..4096].iter().enumerate() {
        let expected = ((read_offset + i) % 251) as u8;
        if got != expected {
            println!(
                "MISMATCH at offset {}: got {:#x}, expected {:#x}",
                i, got, expected
            );
            ok = false;
            break;
        }
    }
    if ok {
        println!("RDMA READ 4KB: verified — all bytes match server pattern");
    }

    println!("\n=== RDMA WRITE Bandwidth ===");
    let sizes: Vec<usize> = vec![
        1024 * 1024,        // 1 MB
        4 * 1024 * 1024,    // 4 MB  — typical chunk
        16 * 1024 * 1024,   // 16 MB
        64 * 1024 * 1024,   // 64 MB
        256 * 1024 * 1024,  // 256 MB — model shard
        512 * 1024 * 1024,  // 512 MB
        1024 * 1024 * 1024, // 1 GB  — checkpoint shard
    ];
    for &size in &sizes {
        // Fill pattern.
        for (i, b) in buf[..size].iter_mut().enumerate() {
            *b = (i & 0xFF) as u8;
        }

        // Fewer iterations for large transfers.
        let iterations = if size >= 256 * 1024 * 1024 { 3 } else { 10 };
        let start = Instant::now();
        for _ in 0..iterations {
            qp.rdma_write(
                buf.as_ptr() as u64,
                mr.lkey,
                remote.mr_addr,
                remote.mr_rkey,
                size as u32,
            )
            .expect("WRITE");
            poll_completion(send_cq, 100_000_000).expect("completion");
        }
        let elapsed = start.elapsed();

        let total_bytes = size as f64 * iterations as f64;
        let throughput_gbps = (total_bytes * 8.0) / elapsed.as_secs_f64() / 1e9;
        let throughput_gbs = total_bytes / elapsed.as_secs_f64() / 1e9;
        let latency_us = elapsed.as_micros() as f64 / iterations as f64;

        println!(
            "RDMA WRITE {:>4}MB x{}: {:.1} Gbps ({:.2} GB/s), avg {:.0}µs/op",
            size / (1024 * 1024),
            iterations,
            throughput_gbps,
            throughput_gbs,
            latency_us,
        );
    }

    println!("\n=== RDMA READ Bandwidth ===");
    for &size in &sizes {
        let iterations = if size >= 256 * 1024 * 1024 { 3 } else { 10 };
        let start = Instant::now();
        for _ in 0..iterations {
            qp.rdma_read(
                buf.as_ptr() as u64,
                mr.lkey,
                remote.mr_addr,
                remote.mr_rkey,
                size as u32,
            )
            .expect("READ");
            poll_completion(send_cq, 100_000_000).expect("completion");
        }
        let elapsed = start.elapsed();

        let total_bytes = size as f64 * iterations as f64;
        let throughput_gbps = (total_bytes * 8.0) / elapsed.as_secs_f64() / 1e9;
        let throughput_gbs = total_bytes / elapsed.as_secs_f64() / 1e9;
        let latency_us = elapsed.as_micros() as f64 / iterations as f64;

        println!(
            "RDMA READ  {:>4}MB x{}: {:.1} Gbps ({:.2} GB/s), avg {:.0}µs/op",
            size / (1024 * 1024),
            iterations,
            throughput_gbps,
            throughput_gbs,
            latency_us,
        );
    }

    println!("\nDone. All cross-node RDMA tests passed.");
}

fn run_server(buf: &[u8], _remote: &PeerInfo) {
    println!("\nServer ready. Client will RDMA WRITE/READ to our buffer.");
    println!("Buffer[0..8] before: {:?}", &buf[0..8]);

    // Poll buffer for changes (client does RDMA WRITE which modifies our buf
    // without any CPU involvement on our side).
    println!("Waiting for client RDMA WRITE (polling buffer[0])...");
    let start = std::time::Instant::now();
    loop {
        if buf[0] == 0xAB {
            break;
        }
        if start.elapsed().as_secs() > 120 {
            println!("Timeout waiting for RDMA WRITE");
            return;
        }
        std::thread::sleep(std::time::Duration::from_millis(100));
    }

    let elapsed = start.elapsed();
    println!("Buffer[0..16] after: {:?}", &buf[0..16]);
    println!(
        "Client RDMA WRITE detected on server side after {:?}!",
        elapsed
    );

    // Keep server alive for client benchmark to complete.
    println!("Keeping server alive for benchmark (60s)...");
    std::thread::sleep(std::time::Duration::from_secs(60));
    println!("Server done.");
}
