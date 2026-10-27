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

//! Derived Service names, golden values shared with the Go module
//! (api/derived_test.go of global-discovery).

use gds_client::derived::{DERIVED_SERVICE_PREFIX, derived_service_host};
use gds_client::derived_service_name;

const GOLDEN: &[(&str, &str, &str)] = &[
    ("default", "nginx", "derived-iuq6ea9f4j"),
    ("llm", "llm-request-router-backend", "derived-b756q8b74k"),
    ("llm-stack", "backend-router", "derived-t1d4et30ia"),
    ("dynamo", "pool-frontend", "derived-apb6bcevn9"),
    ("a", "b", "derived-o56drg1jup"),
    ("kube-system", "kube-dns", "derived-bnmpvejc1s"),
    ("", "", "derived-h9fdlck2cc"),
];

#[test]
fn derived_service_name_golden() {
    for (namespace, name, want) in GOLDEN {
        assert_eq!(
            derived_service_name(namespace, name),
            *want,
            "{namespace}/{name}"
        );
    }
}

#[test]
fn derived_service_name_is_a_dns_label() {
    for (namespace, name, _) in GOLDEN {
        let derived = derived_service_name(namespace, name);
        let hash = derived
            .strip_prefix(DERIVED_SERVICE_PREFIX)
            .expect("prefix");
        assert_eq!(hash.len(), 10);
        assert!(
            hash.bytes()
                .all(|byte| byte.is_ascii_digit() || (b'a'..=b'v').contains(&byte)),
            "{derived}"
        );
    }
}

#[test]
fn separator_is_part_of_the_hash() {
    assert_ne!(
        derived_service_name("ab", "c"),
        derived_service_name("a", "bc")
    );
}

#[test]
fn host_is_the_cluster_local_name() {
    assert_eq!(
        derived_service_host("dynamo", "pool-frontend"),
        "derived-apb6bcevn9.dynamo.svc.cluster.local"
    );
}
