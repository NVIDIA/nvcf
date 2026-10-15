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

use std::net::IpAddr;

use anyhow::Context;
use tonic::Status;
use tracing::warn;
use url::Url;

use crate::routing_state::RegistrationIdentity;

use stargate_proto::pb::InferenceServerRegistration;

pub(super) fn admit_initial_registration(
    update: &InferenceServerRegistration,
    reverse_tunnel_configured: bool,
    routing_key: Option<&str>,
) -> Result<RegistrationIdentity, Status> {
    if update.inference_server_id.is_empty() {
        warn!("inference_server_id is empty; denying registration");
        return Err(Status::invalid_argument("inference_server_id is empty"));
    }

    if update.inference_server_url.is_empty() {
        warn!(
            inference_server_id = %update.inference_server_id,
            "inference_server_url is empty; denying registration"
        );
        return Err(Status::invalid_argument("inference_server_url is empty"));
    }

    if let Err(error) =
        validate_inference_server_url(&update.inference_server_url, update.reverse_tunnel)
    {
        warn!(
            inference_server_id = %update.inference_server_id,
            inference_server_url = %update.inference_server_url,
            reverse_tunnel = update.reverse_tunnel,
            error = %error,
            "invalid inference_server_url; denying registration"
        );
        return Err(Status::invalid_argument(format!(
            "invalid inference_server_url: {error}"
        )));
    }

    if update.reverse_tunnel && !reverse_tunnel_configured {
        warn!(
            inference_server_id = %update.inference_server_id,
            "reverse tunnel flag set but no reverse tunnel config; denying registration"
        );
        return Err(Status::invalid_argument(
            "reverse tunnel flag set but no reverse tunnel config",
        ));
    }

    Ok(RegistrationIdentity {
        inference_server_id: update.inference_server_id.clone(),
        cluster_id: effective_cluster_id(update).to_owned(),
        inference_server_url: update.inference_server_url.clone(),
        routing_key: routing_key.map(ToOwned::to_owned),
        reverse_tunnel: update.reverse_tunnel,
    })
}

pub(super) fn validate_running_update(
    identity: &RegistrationIdentity,
    update: &InferenceServerRegistration,
) -> Option<Status> {
    if update.reverse_tunnel != identity.reverse_tunnel {
        warn!(
            inference_server_id = %update.inference_server_id,
            reverse_tunnel = %update.reverse_tunnel,
            "reverse tunnel flag changed; denying registration"
        );
        return Some(Status::invalid_argument("reverse tunnel flag changed"));
    }
    if update.inference_server_url != identity.inference_server_url {
        warn!(
            inference_server_id = %update.inference_server_id,
            inference_server_url = %update.inference_server_url,
            "inference_server_url changed; denying registration"
        );
        return Some(Status::invalid_argument("inference_server_url changed"));
    }
    if update.inference_server_id != identity.inference_server_id {
        warn!(
            inference_server_id = %update.inference_server_id,
            "inference_server_id changed; denying registration"
        );
        return Some(Status::invalid_argument("inference_server_id changed"));
    }
    if effective_cluster_id(update) != identity.cluster_id {
        warn!(
            inference_server_id = %update.inference_server_id,
            cluster_id = %update.cluster_id,
            "cluster_id changed; denying registration"
        );
        return Some(Status::invalid_argument("cluster_id changed"));
    }
    None
}

fn effective_cluster_id(update: &InferenceServerRegistration) -> &str {
    match update.cluster_id.as_str() {
        "" => &update.inference_server_id,
        cluster_id => cluster_id,
    }
}

fn validate_inference_server_url(url: &str, reverse_tunnel: bool) -> anyhow::Result<()> {
    let parsed = Url::parse(url).context("inference_server_url must be a valid URL")?;
    if reverse_tunnel {
        if !matches!(parsed.scheme(), "http" | "https") {
            anyhow::bail!("inference_server_url scheme must be http or https");
        }
    } else if parsed.scheme() != "quic" {
        anyhow::bail!("inference_server_url scheme must be quic");
    }
    let host = parsed
        .host_str()
        .context("inference_server_url must include host")?;
    if !reverse_tunnel && host.parse::<IpAddr>().is_err() {
        anyhow::bail!("inference_server_url host must be an IP address");
    }
    if parsed.port_or_known_default().is_none() {
        anyhow::bail!("inference_server_url must include port");
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn make_update(id: &str, url: &str, reverse_tunnel: bool) -> InferenceServerRegistration {
        InferenceServerRegistration {
            inference_server_id: id.to_string(),
            cluster_id: String::new(),
            inference_server_url: url.to_string(),
            reverse_tunnel,
            models: Default::default(),
        }
    }

    fn make_identity() -> RegistrationIdentity {
        RegistrationIdentity {
            inference_server_id: "server-1".to_string(),
            cluster_id: "server-1".to_string(),
            inference_server_url: "quic://10.0.0.1:8080".to_string(),
            routing_key: None,
            reverse_tunnel: false,
        }
    }

    fn assert_invalid_argument(status: Status, expected_message: &str) {
        assert_eq!(status.code(), tonic::Code::InvalidArgument);
        assert_eq!(status.message(), expected_message);
    }

    fn assert_url_error(url: &str, expected_message: &str) {
        let error = validate_inference_server_url(url, false).expect_err("URL should be rejected");
        assert_eq!(
            error.to_string(),
            format!("inference_server_url {expected_message}")
        );
    }

    fn assert_running_update_rejected(
        update: &InferenceServerRegistration,
        expected_message: &str,
    ) {
        let status = validate_running_update(&make_identity(), update)
            .expect("changed registration identity should be rejected");
        assert_invalid_argument(status, expected_message);
    }

    #[test]
    fn initial_registration_rejects_empty_id() {
        let update = make_update("", "quic://10.0.0.1:8080", false);

        let status = admit_initial_registration(&update, false, None)
            .expect_err("empty inference_server_id should be rejected");

        assert_invalid_argument(status, "inference_server_id is empty");
    }

    #[test]
    fn initial_registration_rejects_empty_url() {
        let update = make_update("server-1", "", true);

        let status = admit_initial_registration(&update, true, None)
            .expect_err("empty inference_server_url should be rejected");

        assert_invalid_argument(status, "inference_server_url is empty");
    }

    #[test]
    fn direct_registration_accepts_ip_quic_url_and_derives_identity() {
        let update = InferenceServerRegistration {
            cluster_id: "cluster-a".to_string(),
            ..make_update("server-1", "quic://10.0.0.1:8080", false)
        };

        let identity = admit_initial_registration(&update, false, Some("tenant-a"))
            .expect("valid direct registration should be admitted");

        assert_eq!(identity.inference_server_id, "server-1");
        assert_eq!(identity.cluster_id, "cluster-a");
        assert_eq!(identity.inference_server_url, "quic://10.0.0.1:8080");
        assert_eq!(identity.routing_key.as_deref(), Some("tenant-a"));
        assert!(!identity.reverse_tunnel);
    }

    #[test]
    fn direct_registration_defaults_empty_cluster_id_to_server_id() {
        let update = make_update("server-1", "quic://10.0.0.1:8080", false);

        let identity = admit_initial_registration(&update, false, None)
            .expect("valid direct registration should be admitted");

        assert_eq!(identity.cluster_id, "server-1");
    }

    #[test]
    fn validate_inference_server_url_rejects_http() {
        assert_url_error("http://10.0.0.1:8080", "scheme must be quic");
    }

    #[test]
    fn validate_inference_server_url_rejects_missing_port() {
        assert_url_error("quic://10.0.0.1", "must include port");
    }

    #[test]
    fn validate_inference_server_url_accepts_ip_host() {
        validate_inference_server_url("quic://10.0.0.1:8080", false)
            .expect("direct quic URL with IP host and port should be valid");
    }

    #[test]
    fn validate_inference_server_url_rejects_hostname_host() {
        assert_url_error(
            "quic://backend.default.svc:8080",
            "host must be an IP address",
        );
    }

    #[test]
    fn validate_inference_server_url_rejects_garbage() {
        let result = validate_inference_server_url("not a url at all", false);
        assert!(result.is_err());
    }

    #[test]
    fn reverse_tunnel_registration_rejects_non_http_url() {
        let update = make_update("server-1", "quic://10.0.0.1:8080", true);

        let status = admit_initial_registration(&update, true, None)
            .expect_err("reverse-tunnel registration with non-HTTP URL should be rejected");

        assert_invalid_argument(
            status,
            "invalid inference_server_url: inference_server_url scheme must be http or https",
        );
    }

    #[test]
    fn reverse_tunnel_registration_requires_reverse_tunnel_config() {
        let update = make_update("server-1", "http://backend.default.svc:8080", true);

        let status = admit_initial_registration(&update, false, None)
            .expect_err("reverse-tunnel registration without config should be rejected");

        assert_invalid_argument(
            status,
            "reverse tunnel flag set but no reverse tunnel config",
        );
    }

    #[test]
    fn reverse_tunnel_registration_accepts_http_url_when_configured() {
        let update = make_update("server-1", "http://backend.default.svc:8080", true);

        let identity = admit_initial_registration(&update, true, None)
            .expect("valid reverse tunnel registration should be admitted");

        assert!(identity.reverse_tunnel);
        assert_eq!(
            identity.inference_server_url,
            "http://backend.default.svc:8080"
        );
    }

    #[test]
    fn validate_running_update_rejects_changed_url() {
        let update = make_update("server-1", "quic://10.0.0.2:9090", false);
        assert_running_update_rejected(&update, "inference_server_url changed");
    }

    #[test]
    fn validate_running_update_rejects_changed_id() {
        let update = make_update("server-2", "quic://10.0.0.1:8080", false);
        assert_running_update_rejected(&update, "inference_server_id changed");
    }

    #[test]
    fn validate_running_update_rejects_toggled_reverse_tunnel() {
        let update = make_update("server-1", "quic://10.0.0.1:8080", true);
        assert_running_update_rejected(&update, "reverse tunnel flag changed");
    }

    #[test]
    fn validate_running_update_rejects_changed_cluster_id() {
        let update = InferenceServerRegistration {
            cluster_id: "different-cluster".to_string(),
            ..make_update("server-1", "quic://10.0.0.1:8080", false)
        };

        assert_running_update_rejected(&update, "cluster_id changed");
    }
}
