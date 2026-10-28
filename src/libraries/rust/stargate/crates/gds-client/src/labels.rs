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

//! Well-known GDS labels and annotations, mirroring `api/labels.go` of the
//! global-discovery module. Charts, the MCS controller and both clients
//! share these values.

/// Prefix of every GDS label, annotation and finalizer key.
pub const PREFIX: &str = "gds.nvidia.com/";

/// Service label that makes the controller export the Service; the value is
/// [`EXPORT_ENABLED`].
pub const LABEL_EXPORT: &str = "gds.nvidia.com/export";
/// Value of [`LABEL_EXPORT`] that enables the export.
pub const EXPORT_ENABLED: &str = "true";
/// Service label naming what GDS derives from the export.
pub const LABEL_ROLE: &str = "gds.nvidia.com/role";
/// Service label associating the export with a pool by pool name.
pub const LABEL_POOL: &str = "gds.nvidia.com/pool";

/// [`LABEL_ROLE`] value that derives a Router.
pub const ROLE_ROUTER: &str = "router";
/// [`LABEL_ROLE`] value of a pool's OpenAI frontend Service.
pub const ROLE_POOL_FRONTEND: &str = "pool-frontend";
/// [`LABEL_ROLE`] value of a pool's KV event relay (Dynamo PoolRelay).
pub const ROLE_KV_RELAY: &str = "kv-relay";
/// [`LABEL_ROLE`] value of a pool's Pylon transport: an ingress that dials
/// routers and carries requests over its reverse tunnel.
pub const ROLE_PYLON: &str = "pylon";
/// Service annotation: comma separated pool roles of a process that fills
/// several (a frontend embedding Pylon). Replaces [`LABEL_ROLE`] for pool
/// derivation when present; `router` is not allowed in it.
pub const ANNOTATION_ROLES: &str = "gds.nvidia.com/roles";

/// Service annotation: comma separated model ids.
pub const ANNOTATION_POOL_MODELS: &str = "gds.nvidia.com/pool-models";
/// Service annotation: hardware class, for example `b200-ib`.
pub const ANNOTATION_HARDWARE_CLASS: &str = "gds.nvidia.com/hardware-class";
/// Service annotation: data center.
pub const ANNOTATION_DC: &str = "gds.nvidia.com/dc";
/// Service annotation: scheme clients dial the exported Service with, through
/// router seeds or the derived VIP.
pub const ANNOTATION_DIAL_SCHEME: &str = "gds.nvidia.com/dial-scheme";
/// Service annotation: name of the port that carries gRPC.
pub const ANNOTATION_GRPC_PORT_NAME: &str = "gds.nvidia.com/grpc-port-name";
/// Service annotation: name of the port that carries HTTP.
pub const ANNOTATION_HTTP_PORT_NAME: &str = "gds.nvidia.com/http-port-name";
/// Service annotation: name of the UDP port that carries the QUIC reverse
/// tunnel.
pub const ANNOTATION_QUIC_PORT_NAME: &str = "gds.nvidia.com/quic-port-name";

/// [`ANNOTATION_DIAL_SCHEME`] value for plain HTTP.
pub const DIAL_SCHEME_HTTP: &str = "http";
/// [`ANNOTATION_DIAL_SCHEME`] value for TLS.
pub const DIAL_SCHEME_HTTPS: &str = "https";
/// Port name used when [`ANNOTATION_GRPC_PORT_NAME`] is absent.
pub const DEFAULT_GRPC_PORT_NAME: &str = "grpc";
/// Port name used when [`ANNOTATION_HTTP_PORT_NAME`] is absent.
pub const DEFAULT_HTTP_PORT_NAME: &str = "http";
/// Port name used when [`ANNOTATION_QUIC_PORT_NAME`] is absent.
pub const DEFAULT_QUIC_PORT_NAME: &str = "quic";

/// Label on objects the controller creates; the value is
/// [`MANAGED_BY_CONTROLLER`].
pub const LABEL_MANAGED_BY: &str = "gds.nvidia.com/managed-by";
/// Value of [`LABEL_MANAGED_BY`].
pub const MANAGED_BY_CONTROLLER: &str = "gds-mcs-controller";
/// Label scoping controller-created objects to a clusterset.
pub const LABEL_CLUSTERSET: &str = "gds.nvidia.com/clusterset";
/// Label on a ServiceExport created for an API-registered pool.
pub const LABEL_POOL_ID: &str = "gds.nvidia.com/pool-id";

/// Whether Service labels request a label-driven export.
pub fn is_exported<'a>(mut labels: impl Iterator<Item = (&'a String, &'a String)>) -> bool {
    labels.any(|(key, value)| key == LABEL_EXPORT && value == EXPORT_ENABLED)
}

/// Whether `role` is a value of [`LABEL_ROLE`].
pub fn is_role(role: &str) -> bool {
    matches!(
        role,
        ROLE_ROUTER | ROLE_POOL_FRONTEND | ROLE_KV_RELAY | ROLE_PYLON
    )
}

/// Whether `role` attaches an export to a pool.
pub fn is_pool_role(role: &str) -> bool {
    matches!(role, ROLE_POOL_FRONTEND | ROLE_KV_RELAY | ROLE_PYLON)
}

/// The pool roles of an exported Service: the entries of an
/// [`ANNOTATION_ROLES`] value when `roles` is `Some` and non-empty, else the
/// [`LABEL_ROLE`] value. Unknown entries, `router` and repeats are dropped in
/// first-seen order. Mirrors `api.ParseRoles`.
pub fn parse_roles(role_label: Option<&str>, roles_annotation: Option<&str>) -> Vec<String> {
    let mut roles: Vec<String> = Vec::new();
    let mut add = |role: &str| {
        let role = role.trim();
        if is_pool_role(role) && !roles.iter().any(|seen| seen == role) {
            roles.push(role.to_string());
        }
    };
    if let Some(value) = roles_annotation.map(str::trim).filter(|v| !v.is_empty()) {
        for role in value.split(',') {
            add(role);
        }
        return roles;
    }
    if let Some(role) = role_label {
        add(role);
    }
    roles
}

/// Splits a [`ANNOTATION_POOL_MODELS`] value into model ids, trimming spaces
/// and dropping empty and repeated entries in first-seen order.
pub fn parse_pool_models(value: &str) -> Vec<String> {
    let mut models: Vec<String> = Vec::new();
    for model in value.split(',').map(str::trim) {
        if !model.is_empty() && !models.iter().any(|seen| seen == model) {
            models.push(model.to_string());
        }
    }
    models
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use super::*;

    #[test]
    fn keys_share_the_prefix() {
        for key in [
            LABEL_EXPORT,
            LABEL_ROLE,
            LABEL_POOL,
            ANNOTATION_POOL_MODELS,
            ANNOTATION_HARDWARE_CLASS,
            ANNOTATION_DC,
            ANNOTATION_DIAL_SCHEME,
            ANNOTATION_GRPC_PORT_NAME,
            ANNOTATION_HTTP_PORT_NAME,
            ANNOTATION_QUIC_PORT_NAME,
            ANNOTATION_ROLES,
            LABEL_MANAGED_BY,
            LABEL_CLUSTERSET,
            LABEL_POOL_ID,
        ] {
            assert!(key.starts_with(PREFIX) && key.len() > PREFIX.len(), "{key}");
        }
    }

    #[test]
    fn export_and_roles() {
        let labels = HashMap::from([(LABEL_EXPORT.to_string(), "true".to_string())]);
        assert!(is_exported(labels.iter()));
        let labels = HashMap::from([(LABEL_EXPORT.to_string(), "yes".to_string())]);
        assert!(!is_exported(labels.iter()));
        assert!(is_role("router") && is_role("pool-frontend") && is_role("kv-relay"));
        assert!(is_role("pylon") && is_pool_role("pylon") && !is_pool_role("router"));
        assert!(!is_role("frontend"));
        assert_eq!(parse_roles(Some("pylon"), None), vec!["pylon"]);
        assert_eq!(parse_roles(Some("router"), None), Vec::<String>::new());
        assert_eq!(
            parse_roles(
                Some("kv-relay"),
                Some(" pool-frontend, pylon ,router,pylon")
            ),
            vec!["pool-frontend", "pylon"]
        );
        assert_eq!(parse_roles(Some("kv-relay"), Some("  ")), vec!["kv-relay"]);
    }

    #[test]
    fn pool_models_are_trimmed_and_unique() {
        assert_eq!(
            parse_pool_models(" llama-3 , ,mixtral,llama-3,"),
            ["llama-3", "mixtral"]
        );
        assert!(parse_pool_models("").is_empty());
    }
}
