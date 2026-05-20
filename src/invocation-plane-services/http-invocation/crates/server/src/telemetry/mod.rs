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

pub mod settings;

use crate::telemetry::settings::{OtelResourceSettings, TracingSettings};
use opentelemetry::trace::TracerProvider as _;
use opentelemetry::KeyValue;
use opentelemetry_otlp::{SpanExporter, WithExportConfig, WithTonicConfig};
use opentelemetry_sdk::trace::SdkTracerProvider;
use opentelemetry_sdk::Resource;
use tonic::transport::ClientTlsConfig;
use tracing_subscriber::layer::SubscriberExt;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::{fmt, EnvFilter};

/// RAII guard that shuts down the global OTel tracer provider on drop.
pub struct TracingGuard {
    provider: SdkTracerProvider,
}

impl Drop for TracingGuard {
    fn drop(&mut self) {
        if let Err(e) = self.provider.shutdown() {
            eprintln!("Failed to shutdown tracer provider: {e}");
        }
    }
}

/// Initialize the global `tracing` subscriber with:
/// - An `EnvFilter` built from `env_filter`
/// - A `fmt` layer for local log output
/// - An OpenTelemetry layer exporting spans via OTLP/gRPC to the endpoint in `settings`
///
/// Returns a [`TracingGuard`] whose `Drop` impl flushes and shuts down the exporter.
/// Keep it alive for the duration of `main`.
pub fn initialize_tracing(
    service_name: &str,
    settings: &TracingSettings,
    resource: Option<&OtelResourceSettings>,
    env_filter: impl Into<String>,
) -> TracingGuard {
    // --- Build OTel Resource ---
    let mut attrs: Vec<KeyValue> = vec![KeyValue::new("service.name", service_name.to_string())];
    if let Some(r) = resource {
        for (k, v) in &r.attributes {
            attrs.push(KeyValue::new(k.clone(), v.clone()));
        }
    }
    let otel_resource = Resource::builder_empty().with_attributes(attrs).build();

    // --- Build OTLP span exporter (gRPC/tonic) ---
    let mut exporter_builder = SpanExporter::builder().with_tonic();

    if let Some(endpoint) = settings.otlp_endpoint() {
        // tonic only enables TLS when an explicit `ClientTlsConfig` is supplied; the
        // `tls-webpki-roots` feature alone is not enough — without this the channel will
        // fail with "Connecting to HTTPS without TLS enabled" against an `https://` collector.
        if endpoint.starts_with("https://") {
            exporter_builder =
                exporter_builder.with_tls_config(ClientTlsConfig::new().with_enabled_roots());
        }
        exporter_builder = exporter_builder.with_endpoint(endpoint);
    }

    if let Some(headers) = &settings.headers {
        let mut metadata = tonic::metadata::MetadataMap::new();
        for (k, v) in headers {
            // Headers with hyphens (e.g. "lightstep-access-token") are valid ASCII metadata keys.
            if let (Ok(key), Ok(val)) = (
                k.parse::<tonic::metadata::MetadataKey<tonic::metadata::Ascii>>(),
                v.parse::<tonic::metadata::MetadataValue<tonic::metadata::Ascii>>(),
            ) {
                metadata.insert(key, val);
            }
        }
        exporter_builder = exporter_builder.with_metadata(metadata);
    }

    let exporter = exporter_builder
        .build()
        .expect("failed to build OTLP span exporter");

    // --- Build tracer provider ---
    let provider = SdkTracerProvider::builder()
        .with_batch_exporter(exporter)
        .with_resource(otel_resource)
        .build();

    opentelemetry::global::set_tracer_provider(provider.clone());

    // --- Build tracing-subscriber and install as global default ---
    let tracer = provider.tracer(service_name.to_string());
    let otel_layer = tracing_opentelemetry::layer().with_tracer(tracer);

    let filter = EnvFilter::try_new(env_filter.into()).unwrap_or_else(|_| EnvFilter::new("info"));

    tracing_subscriber::registry()
        .with(filter)
        .with(fmt::layer())
        .with(otel_layer)
        .init();

    TracingGuard { provider }
}
