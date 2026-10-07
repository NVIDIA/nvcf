// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

use std::io::Write;
use std::time::Duration;

use crate::common::{
    direct_registration_config, init_crypto, make_stargate_runtime_with_lb, start_dummy_inst,
    wait_for_routing, with_proxy_headers,
};
use pylon_lib::{CurrentModelStats, InferenceServerRegistrationClient, PylonRuntimeState};
use stargate::metrics::StargateMetrics;
use stargate_proto::pb::InferenceServerStatus;

fn selections(metrics: &StargateMetrics, algorithm: Option<&str>) -> f64 {
    metrics
        .registry()
        .gather()
        .iter()
        .filter(|family| family.name() == "stargate_routing_selections_total")
        .flat_map(|family| family.get_metric())
        .filter(|metric| {
            algorithm.is_none_or(|algorithm| {
                metric
                    .get_label()
                    .iter()
                    .any(|label| label.name() == "algorithm" && label.value() == algorithm)
            })
        })
        .map(|metric| metric.get_counter().value())
        .sum()
}

#[tokio::test]
async fn routing_expressions_proxy_valid_values_and_reject_before_selection() {
    init_crypto();
    let model = "routing-expressions-model";
    let mut config = tempfile::NamedTempFile::new().unwrap();
    config
        .write_all(
            br#"{
        "default": "round-robin",
        "request_algorithms": {
            "random": "random",
            "power-of-n": "power-of-n",
            "pulsar": "pulsar",
            "wait-and-widen": "wait-and-widen",
            "pulsar-wait-and-widen": "pulsar-wait-and-widen"
        }
    }"#,
        )
        .unwrap();
    let (grpc_addr, http_addr, runtime) = make_stargate_runtime_with_lb(
        "test-routing-expressions",
        Some(config.path().to_string_lossy().into_owned()),
    );
    let handle = runtime.start().await.unwrap();
    let (backend_addr, backend_url, _tunnel) = start_dummy_inst(model).await;
    let backend_state = PylonRuntimeState::new(InferenceServerStatus::Active, &[model.to_owned()]);
    backend_state.set_model_stats(
        model,
        CurrentModelStats {
            last_mean_input_tps: 1000.0,
            ..CurrentModelStats::default()
        },
    );
    let mut backend = InferenceServerRegistrationClient::default();
    backend
        .start(direct_registration_config(
            vec![grpc_addr.to_string()],
            "routing-expressions-backend",
            backend_url,
            format!("http://{backend_addr}"),
            backend_state,
        ))
        .unwrap();
    wait_for_routing(http_addr, model, Duration::from_secs(10)).await;
    let metrics = handle.metrics();
    let client = reqwest::Client::new();
    let url = format!("http://{http_addr}/v1/chat/completions");
    for (header, algorithm) in [
        (
            "round-robin;require_input_tokens=true;max_input_work_seconds=2.5",
            "round-robin",
        ),
        (
            "random;require_input_tokens=true;max_input_work_seconds=2.5",
            "random",
        ),
        (
            "power-of-n;sample_count=1;comparator=queue-time",
            "power-of-n",
        ),
        (
            "pulsar;seed=stable-a;consider_kv_free_tokens=false",
            "pulsar",
        ),
        (
            "wait-and-widen;n=2;ttft_bucket_size_ms=50;next_bucket_unlock_factor=\"0.0625\"",
            "wait-and-widen",
        ),
        (
            "pulsar-wait-and-widen;seed=stable-a;n=2;max_queue_time_floor_ms=100;max_queue_time_ceil_ms=500",
            "pulsar-wait-and-widen",
        ),
        ("PULSAR_WAIT_AND_WIDEN", "pulsar-wait-and-widen"),
    ] {
        let before = selections(&metrics, Some(algorithm));
        let response = with_proxy_headers(client.post(&url), model, "expression-valid")
            .header("x-routing-method", header)
            .header("x-cache-affinity-key", "stable-prefix")
            .json(&serde_json::json!({"model": model, "messages": [], "stream": true}))
            .send()
            .await
            .unwrap();
        let status = response.status();
        let body = response.bytes().await.unwrap();
        assert_eq!(status, reqwest::StatusCode::OK, "{header}: {body:?}");
        assert!(!body.is_empty(), "{header}");
        assert_eq!(
            selections(&metrics, Some(algorithm)) - before,
            1.0,
            "{header}"
        );
    }

    let before = selections(&metrics, None);
    for (header, class) in [
        ("pulsar;seed", "malformed_expression"),
        ("fastest;seed=x", "unknown_method"),
        ("fastest", "unknown_method"),
        ("pulsar;widen=2", "unknown_parameter"),
        ("round-robin;seed=x", "not_applicable"),
        ("power-of-n;sample_count=banana", "invalid_value"),
        (
            "wait-and-widen;max_queue_time_floor_ms=100",
            "inert_combination",
        ),
        ("wait-and-widen;ttft_bucket_size_ms=0", "inert_value"),
    ] {
        let response = with_proxy_headers(client.post(&url), model, "expression-invalid")
            .header("x-routing-method", header)
            .json(&serde_json::json!({"model": model, "messages": []}))
            .send()
            .await
            .unwrap();
        assert_eq!(
            response.status(),
            reqwest::StatusCode::BAD_REQUEST,
            "{header}"
        );
        assert_eq!(
            response.headers()["x-stargate-error-code"],
            class,
            "{header}"
        );
        assert_eq!(
            response.headers()["content-type"],
            "application/json",
            "{header}"
        );
        let body: serde_json::Value = response.json().await.unwrap();
        assert_eq!(body["code"], class, "{header}");
        assert!(
            body["error"]
                .as_str()
                .is_some_and(|error| !error.is_empty()),
            "{header}"
        );
        assert_eq!(body.as_object().unwrap().len(), 2, "{header}");
    }
    assert_eq!(selections(&metrics, None), before);
    backend.stop();
    handle.begin_shutdown();
    assert!(handle.wait_for_shutdown(Duration::from_secs(5)).await);
}

#[tokio::test]
async fn routing_expressions_reject_unavailable_methods_with_json() {
    init_crypto();
    let mut config = tempfile::NamedTempFile::new().unwrap();
    config.write_all(br#"{"default":"round-robin"}"#).unwrap();
    let (_, http_addr, runtime) = make_stargate_runtime_with_lb(
        "test-routing-expressions-unavailable",
        Some(config.path().to_string_lossy().into_owned()),
    );
    let handle = runtime.start().await.unwrap();
    let client = reqwest::Client::new();
    for header in ["pulsar;seed=x", "pulsar"] {
        let response = with_proxy_headers(
            client.post(format!("http://{http_addr}/v1/chat/completions")),
            "model",
            "expression-unavailable",
        )
        .header("x-routing-method", header)
        .json(&serde_json::json!({"model":"model", "messages":[]}))
        .send()
        .await
        .unwrap();
        assert_eq!(
            response.status(),
            reqwest::StatusCode::BAD_REQUEST,
            "{header}"
        );
        assert_eq!(
            response.headers()["x-stargate-error-code"],
            "unavailable",
            "{header}"
        );
        let body: serde_json::Value = response.json().await.unwrap();
        assert_eq!(body["code"], "unavailable", "{header}");
        assert!(
            body["error"]
                .as_str()
                .is_some_and(|error| !error.is_empty())
        );
    }
    assert_eq!(selections(&handle.metrics(), None), 0.0);
    handle.begin_shutdown();
    assert!(handle.wait_for_shutdown(Duration::from_secs(5)).await);
}
