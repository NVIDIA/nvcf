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
/// Router Service annotation: template of a router replica's identity (gRPC
/// authority, QUIC SNI, TLS name) with one [`POD_NAME_PLACEHOLDER`], for
/// example `{pod_name}.stargate.example.com`. See [`replica_authority`].
pub const ANNOTATION_HOSTNAME_TEMPLATE: &str = "gds.nvidia.com/hostname-template";
/// Router Service annotation: what the router replicas are,
/// [`ROUTER_MODE_BACKEND_ROUTER`] or [`ROUTER_MODE_DIRECT`]. Absent means
/// [`ROUTER_MODE_BACKEND_ROUTER`].
pub const ANNOTATION_ROUTER_MODE: &str = "gds.nvidia.com/router-mode";

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

/// [`ANNOTATION_ROUTER_MODE`] value: the router replicas are
/// stargate-k8s-router pods that forward registration by authority and the
/// QUIC tunnel by SNI to the Stargate pods of their cluster.
pub const ROUTER_MODE_BACKEND_ROUTER: &str = "backend-router";
/// [`ANNOTATION_ROUTER_MODE`] value: the router replicas are the Stargate
/// pods themselves, dialled directly with the authority from
/// [`replica_authority`].
pub const ROUTER_MODE_DIRECT: &str = "direct";
/// Placeholder of [`ANNOTATION_HOSTNAME_TEMPLATE`] replaced by a replica's
/// identity.
pub const POD_NAME_PLACEHOLDER: &str = "{pod_name}";
/// Endpoint attribute holding the name of the endpoint's target reference
/// (the pod name), set by the MCS controller.
pub const ENDPOINT_ATTR_TARGET_NAME: &str = "targetRef.name";

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

/// The router mode of a router export from its annotations:
/// [`ROUTER_MODE_BACKEND_ROUTER`] or [`ROUTER_MODE_DIRECT`], matched after
/// trimming spaces and ignoring case. `None` when [`ANNOTATION_ROUTER_MODE`]
/// is absent or holds another value; callers treat `None` as
/// [`ROUTER_MODE_BACKEND_ROUTER`]. Mirrors `api.RouterModeOf`.
pub fn router_mode_of<'a>(
    mut annotations: impl Iterator<Item = (&'a String, &'a String)>,
) -> Option<&'static str> {
    let (_, value) = annotations.find(|(key, _)| key.as_str() == ANNOTATION_ROUTER_MODE)?;
    let value = value.trim();
    [ROUTER_MODE_BACKEND_ROUTER, ROUTER_MODE_DIRECT]
        .into_iter()
        .find(|mode| value.eq_ignore_ascii_case(mode))
}

/// The authority of a router replica,
/// `<template with {pod_name} replaced by identity>:<port>`, for example
/// `stargate-0.stargate.example.com:50071`. It is the replica's gRPC
/// authority; the host part is its QUIC SNI and TLS name. `None` when
/// `template` or `identity` is empty after trimming spaces, when `template`
/// does not contain [`POD_NAME_PLACEHOLDER`] exactly once, or when `port` is
/// not in 1..=65535. The host is a DNS name, never bracketed. Mirrors
/// `api.ReplicaAuthority`.
pub fn replica_authority(template: &str, identity: &str, port: i32) -> Option<String> {
    let (template, identity) = (template.trim(), identity.trim());
    let port = u16::try_from(port).ok().filter(|port| *port != 0)?;
    if template.is_empty()
        || identity.is_empty()
        || template.matches(POD_NAME_PLACEHOLDER).count() != 1
    {
        return None;
    }
    Some(format!(
        "{}:{port}",
        template.replacen(POD_NAME_PLACEHOLDER, identity, 1)
    ))
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
            ANNOTATION_HOSTNAME_TEMPLATE,
            ANNOTATION_ROUTER_MODE,
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

    #[test]
    fn router_mode_values_match_go() {
        assert_eq!(
            ANNOTATION_HOSTNAME_TEMPLATE,
            "gds.nvidia.com/hostname-template"
        );
        assert_eq!(ANNOTATION_ROUTER_MODE, "gds.nvidia.com/router-mode");
        assert_eq!(ROUTER_MODE_BACKEND_ROUTER, "backend-router");
        assert_eq!(ROUTER_MODE_DIRECT, "direct");
        assert_eq!(POD_NAME_PLACEHOLDER, "{pod_name}");
        assert_eq!(ENDPOINT_ATTR_TARGET_NAME, "targetRef.name");
    }

    #[test]
    fn router_mode_is_normalised() {
        let mode = |value: Option<&str>| {
            let annotations: HashMap<String, String> = value
                .map(|v| (ANNOTATION_ROUTER_MODE.to_string(), v.to_string()))
                .into_iter()
                .chain([("other".to_string(), "direct".to_string())])
                .collect();
            router_mode_of(annotations.iter())
        };
        assert_eq!(mode(None), None);
        assert_eq!(mode(Some("")), None);
        assert_eq!(mode(Some("direct")), Some(ROUTER_MODE_DIRECT));
        assert_eq!(mode(Some(" Direct ")), Some(ROUTER_MODE_DIRECT));
        assert_eq!(
            mode(Some("backend-router")),
            Some(ROUTER_MODE_BACKEND_ROUTER)
        );
        assert_eq!(
            mode(Some("BACKEND-ROUTER")),
            Some(ROUTER_MODE_BACKEND_ROUTER)
        );
        assert_eq!(mode(Some("backend_router")), None);
        assert_eq!(mode(Some("k8s-router")), None);
    }

    #[test]
    fn replica_authority_replaces_the_placeholder() {
        let cases: [(&str, &str, i32, Option<&str>); 14] = [
            (
                "{pod_name}.stargate.example.com",
                "stargate-0",
                50071,
                Some("stargate-0.stargate.example.com:50071"),
            ),
            (
                " {pod_name}.llm.svc.cluster.local ",
                " stargate-1 ",
                443,
                Some("stargate-1.llm.svc.cluster.local:443"),
            ),
            (
                "sg-{pod_name}-eu.example.com",
                "abc",
                1,
                Some("sg-abc-eu.example.com:1"),
            ),
            ("{pod_name}", "stargate-0", 65535, Some("stargate-0:65535")),
            ("", "stargate-0", 50071, None),
            ("  ", "stargate-0", 50071, None),
            ("{pod_name}.example.com", "", 50071, None),
            ("{pod_name}.example.com", " ", 50071, None),
            ("stargate.example.com", "stargate-0", 50071, None),
            (
                "{pod_name}.{pod_name}.example.com",
                "stargate-0",
                50071,
                None,
            ),
            ("{POD_NAME}.example.com", "stargate-0", 50071, None),
            ("{pod_name}.example.com", "stargate-0", 0, None),
            ("{pod_name}.example.com", "stargate-0", -1, None),
            ("{pod_name}.example.com", "stargate-0", 65536, None),
        ];
        for (template, identity, port, want) in cases {
            assert_eq!(
                replica_authority(template, identity, port).as_deref(),
                want,
                "{template:?} {identity:?} {port}"
            );
        }
    }
}
