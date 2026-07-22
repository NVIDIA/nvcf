/*
 * SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

//! OpenTelemetry tracing initialization.

use crate::settings::{OtelResourceSettings, TracingSettings};
use opentelemetry::trace::TracerProvider;
use opentelemetry::KeyValue;
use opentelemetry_otlp::WithExportConfig;
use opentelemetry_otlp::WithTonicConfig;
use opentelemetry_sdk::trace::SdkTracerProvider;
use opentelemetry_sdk::Resource;
use tonic_otel::metadata::{Ascii, MetadataKey, MetadataMap, MetadataValue};
use tonic_otel::transport::ClientTlsConfig;
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt, EnvFilter};

/// Initialize tracing and return a guard that flushes on drop.
pub fn initialize_tracing(
    service_name: &str,
    tracing_settings: &TracingSettings,
    resource_attrs: Option<&OtelResourceSettings>,
    envfilter_directive: Option<String>,
) -> TracingGuard {
    let filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| {
        EnvFilter::try_new(envfilter_directive.as_deref().unwrap_or("info")).unwrap()
    });

    let resource_kvs: Vec<KeyValue> = resource_attrs
        .map(|r| {
            r.attributes
                .iter()
                .map(|(k, v)| KeyValue::new(k.clone(), v.clone()))
                .collect()
        })
        .unwrap_or_else(|| vec![KeyValue::new("service.name", service_name.to_string())]);

    let resource = Resource::builder().with_attributes(resource_kvs).build();

    let endpoint = build_otlp_grpc_endpoint(tracing_settings);

    let mut exporter_builder = opentelemetry_otlp::SpanExporter::builder()
        .with_tonic()
        .with_endpoint(&endpoint)
        .with_timeout(std::time::Duration::from_secs(10));

    if endpoint.starts_with("https://") {
        exporter_builder =
            exporter_builder.with_tls_config(ClientTlsConfig::new().with_enabled_roots());
    }

    if let Some(headers) = &tracing_settings.headers {
        let mut metadata = MetadataMap::new();

        for (key, value) in headers {
            if let (Ok(key), Ok(value)) = (
                key.parse::<MetadataKey<Ascii>>(),
                value.parse::<MetadataValue<Ascii>>(),
            ) {
                metadata.insert(key, value);
            }
        }

        exporter_builder = exporter_builder.with_metadata(metadata);
    }

    let exporter = exporter_builder.build().expect("OTLP span exporter build");

    let tracer_provider = SdkTracerProvider::builder()
        .with_resource(resource)
        .with_batch_exporter(exporter)
        .build();

    let tracer = tracer_provider.tracer("rs-autoscaler");

    opentelemetry::global::set_tracer_provider(tracer_provider.clone());

    let telemetry_layer = tracing_opentelemetry::layer().with_tracer(tracer);

    tracing_subscriber::registry()
        .with(filter)
        .with(telemetry_layer)
        .with(tracing_subscriber::fmt::layer())
        .try_init()
        .expect("tracing init");

    TracingGuard {
        provider: tracer_provider,
    }
}

fn build_otlp_grpc_endpoint(tracing_settings: &TracingSettings) -> String {
    match tracing_settings.endpoint_ip.as_deref() {
        Some(host) => {
            let mut endpoint = normalize_endpoint_host(host);
            if !endpoint_has_port(&endpoint) {
                endpoint = format!(
                    "{}:{}",
                    endpoint,
                    tracing_settings.endpoint_port.unwrap_or(4317)
                );
            }
            endpoint
        }
        _ => "http://127.0.0.1:4317".to_string(),
    }
}

fn normalize_endpoint_host(host: &str) -> String {
    let host = host.trim().trim_end_matches('/');
    if host.starts_with("http://") || host.starts_with("https://") {
        host.to_string()
    } else {
        format!("http://{host}")
    }
}

fn endpoint_has_port(endpoint: &str) -> bool {
    let authority = endpoint
        .trim_start_matches("http://")
        .trim_start_matches("https://")
        .split('/')
        .next()
        .unwrap_or(endpoint);

    authority
        .rsplit_once(':')
        .map(|(_, port)| port.parse::<u16>().is_ok())
        .unwrap_or(false)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn build_otlp_grpc_endpoint_preserves_https_scheme() {
        let settings = TracingSettings {
            endpoint_ip: Some("https://otel.example.com".to_string()),
            endpoint_port: Some(8282),
            ..Default::default()
        };

        assert_eq!(
            build_otlp_grpc_endpoint(&settings),
            "https://otel.example.com:8282"
        );
    }

    #[test]
    fn build_otlp_grpc_endpoint_defaults_bare_hosts_to_http() {
        let settings = TracingSettings {
            endpoint_ip: Some("localhost".to_string()),
            endpoint_port: Some(4317),
            ..Default::default()
        };

        assert_eq!(build_otlp_grpc_endpoint(&settings), "http://localhost:4317");
    }

    #[test]
    fn build_otlp_grpc_endpoint_preserves_embedded_port_with_configured_port() {
        let settings = TracingSettings {
            endpoint_ip: Some("https://otel.example.com:8282".to_string()),
            endpoint_port: Some(8282),
            ..Default::default()
        };

        assert_eq!(
            build_otlp_grpc_endpoint(&settings),
            "https://otel.example.com:8282"
        );
    }

    #[test]
    fn build_otlp_grpc_endpoint_preserves_embedded_port_without_configured_port() {
        let settings = TracingSettings {
            endpoint_ip: Some("https://otel.example.com:8282".to_string()),
            endpoint_port: None,
            ..Default::default()
        };

        assert_eq!(
            build_otlp_grpc_endpoint(&settings),
            "https://otel.example.com:8282"
        );
    }

    #[test]
    fn build_otlp_grpc_endpoint_uses_local_default_without_host() {
        let settings = TracingSettings::default();

        assert_eq!(build_otlp_grpc_endpoint(&settings), "http://127.0.0.1:4317");
    }
}

pub struct TracingGuard {
    provider: SdkTracerProvider,
}

impl Drop for TracingGuard {
    fn drop(&mut self) {
        if let Err(e) = self.provider.shutdown() {
            eprintln!("TracerProvider shutdown error: {:?}", e);
        }
    }
}
