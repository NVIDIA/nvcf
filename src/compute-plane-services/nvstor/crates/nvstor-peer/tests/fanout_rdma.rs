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

//! The fetcher cap over real RDMA transports. Needs an ACTIVE RDMA
//! device; run explicitly on a provisioned host:
//!
//!   cargo test -p nvstor-peer --test fanout_rdma -- --ignored

use std::sync::Arc;
use std::time::{Duration, Instant};

use nvstor_peer::fetch::{fetch_from, FetchOptions, OpenError, SourceSet};
use nvstor_peer::rdma_sources::RdmaSources;
use nvstor_peer::server::{self, ServeOptions};
use nvstor_peer::sessions;
use nvstor_rdma::auth::Credentials;
use nvstor_rdma::rail::{DeviceSelect, Rails};

const SECRET: &str = "fanout-test-secret-0123";

fn fetcher(addr: &str) -> (Arc<Rails>, RdmaSources) {
    let id = sessions::new_session_id().expect("session id");
    let creds = Credentials::new(sessions::session_key(&id), SECRET);
    let rails = Rails::open_selected(&DeviceSelect::AllActive, 1 << 20, 8, &creds).expect("rails");
    let sources =
        RdmaSources::new(Arc::clone(&rails), vec![addr.to_string()], "").expect("sources");
    (rails, sources)
}

#[test]
#[ignore = "needs an ACTIVE RDMA device"]
fn the_second_fetcher_is_busy_until_the_first_one_leaves() {
    let dir = tempfile::tempdir().expect("tempdir");
    std::fs::write(dir.path().join("w.bin"), vec![7u8; 3 << 20]).expect("write");
    let srv = server::start(
        &ServeOptions {
            root: dir.path().to_path_buf(),
            export_as: String::new(),
            port: 0,
            listen: std::net::Ipv6Addr::UNSPECIFIED.into(),
            devices: DeviceSelect::AllActive,
            slot_size: 1 << 20,
            slots: 8,
            max_in_flight: 16,
            max_fetchers: 1,
        },
        SECRET,
    )
    .expect("serve");
    let addr = format!("127.0.0.1:{}", srv.rails[0].1.port);

    let (rails_a, a) = fetcher(&addr);
    let conn_a = a.open(&addr).expect("first fetcher admitted");
    assert_eq!(srv.cap.active(), 1);
    let (rails_b, b) = fetcher(&addr);
    let refused = Instant::now();
    assert!(
        matches!(b.open(&addr), Err(OpenError::Busy(_))),
        "second fetcher must be told BUSY"
    );
    assert!(
        refused.elapsed() < Duration::from_secs(2),
        "BUSY is an answer, not a timeout"
    );
    assert!(srv.cap.refused() >= 1);

    // The first fetcher goes away; the server frees its place when its
    // connections' bootstrap streams close.
    drop(conn_a);
    a.close();
    rails_a.shutdown();
    let deadline = Instant::now() + Duration::from_secs(5);
    while srv.cap.active() > 0 {
        assert!(
            Instant::now() < deadline,
            "the first fetcher's place was never freed"
        );
        std::thread::sleep(Duration::from_millis(50));
    }

    let dest = tempfile::tempdir().expect("dest");
    let o = FetchOptions {
        connections: 4,
        range_size: 1 << 20,
        ..FetchOptions::default()
    };
    let r = fetch_from(&b, "", dest.path(), &o).expect("second fetcher copies once admitted");
    assert_eq!(r.files, 1);
    assert_eq!(
        std::fs::read(dest.path().join("w.bin")).expect("read"),
        vec![7u8; 3 << 20]
    );
    b.close();
    rails_b.shutdown();
}
