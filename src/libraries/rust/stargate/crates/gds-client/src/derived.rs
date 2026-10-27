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

//! Names of the derived Services the MCS controller creates for imports.

use sha2::{Digest, Sha256};

/// Prefix of every derived Service name.
pub const DERIVED_SERVICE_PREFIX: &str = "derived-";

/// The default Kubernetes cluster domain.
pub const DEFAULT_CLUSTER_DOMAIN: &str = "cluster.local";

/// The name of the derived Service an importing cluster creates for the
/// ServiceImport `namespace/name`: `derived-` followed by the first ten
/// characters of the lowercase, unpadded base32hex encoding of the SHA-256 of
/// `namespace/name`.
///
/// This is the `derivedName` function of the sigs.k8s.io/mcs-api
/// controllers and `api.DerivedServiceName` of the GDS Go module, so every
/// cluster and every MCS implementation that follows mcs-api agrees on it.
pub fn derived_service_name(namespace: &str, name: &str) -> String {
    let digest = Sha256::digest(format!("{namespace}/{name}").as_bytes());
    let encoded = data_encoding::BASE32HEX_NOPAD.encode(&digest);
    let mut derived = String::with_capacity(DERIVED_SERVICE_PREFIX.len() + 10);
    derived.push_str(DERIVED_SERVICE_PREFIX);
    derived.push_str(&encoded[..10].to_ascii_lowercase());
    derived
}

/// The in-cluster DNS name of the derived Service:
/// `derived-<hash>.<namespace>.svc.cluster.local`.
pub fn derived_service_host(namespace: &str, name: &str) -> String {
    derived_service_host_in(namespace, name, DEFAULT_CLUSTER_DOMAIN)
}

/// [`derived_service_host`] for a cluster with another cluster domain.
pub fn derived_service_host_in(namespace: &str, name: &str, cluster_domain: &str) -> String {
    format!(
        "{}.{namespace}.svc.{}",
        derived_service_name(namespace, name),
        cluster_domain.trim_matches('.')
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn host_uses_cluster_domain() {
        assert_eq!(
            derived_service_host("default", "nginx"),
            "derived-iuq6ea9f4j.default.svc.cluster.local"
        );
        assert_eq!(
            derived_service_host_in("default", "nginx", "example.org."),
            "derived-iuq6ea9f4j.default.svc.example.org"
        );
    }
}
