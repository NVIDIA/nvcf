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
use opentelemetry::propagation::TextMapCompositePropagator;
use opentelemetry::trace::TracerProvider as _;
use opentelemetry::KeyValue;
use opentelemetry_otlp::{SpanExporter, WithExportConfig, WithTonicConfig};
use opentelemetry_sdk::propagation::{BaggagePropagator, TraceContextPropagator};
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
    register_text_map_propagator();

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

/// Register a W3C trace-context + baggage propagator as the OpenTelemetry
/// global. Inbound `traceparent` headers, outbound NATS carriers, and the
/// `OtelGrpcLayer` / `reqwest_tracing::TracingMiddleware` outbound clients all
/// resolve through this global; without it they silently degrade to a noop and
/// every request starts a fresh root trace.
fn register_text_map_propagator() {
    opentelemetry::global::set_text_map_propagator(TextMapCompositePropagator::new(vec![
        Box::new(TraceContextPropagator::new()),
        Box::new(BaggagePropagator::new()),
    ]));
}

#[cfg(test)]
mod tests {
    use super::*;
    use opentelemetry::trace::TraceContextExt;
    use std::collections::HashMap;

    // Regression test: the propagator registration was silently dropped once
    // during a refactor (nv_svc_facilities -> in-tree telemetry). If it breaks
    // again, every request will start a fresh root trace and no downstream
    // service will see a `traceparent`.
    #[test]
    fn global_propagator_extracts_w3c_traceparent() {
        register_text_map_propagator();

        let mut carrier: HashMap<String, String> = HashMap::new();
        carrier.insert(
            "traceparent".into(),
            "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01".into(),
        );

        let cx = opentelemetry::global::get_text_map_propagator(|p| p.extract(&carrier));
        let span_context = cx.span().span_context().clone();

        assert!(
            span_context.is_valid(),
            "global propagator did not extract the traceparent — is it registered?"
        );
        assert_eq!(
            span_context.trace_id().to_string(),
            "0af7651916cd43dd8448eb211c80319c"
        );
        assert_eq!(span_context.span_id().to_string(), "b7ad6b7169203331");
    }

    // Outbound side of the same wire — exercised in production by NATS publish,
    // OtelGrpcLayer, and reqwest TracingMiddleware.
    #[test]
    fn global_propagator_injects_w3c_traceparent() {
        use opentelemetry::trace::{SpanContext, SpanId, TraceFlags, TraceId, TraceState};
        register_text_map_propagator();

        let span_context = SpanContext::new(
            TraceId::from_hex("0af7651916cd43dd8448eb211c80319c").unwrap(),
            SpanId::from_hex("b7ad6b7169203331").unwrap(),
            TraceFlags::SAMPLED,
            true,
            TraceState::default(),
        );
        let cx = opentelemetry::Context::new().with_remote_span_context(span_context);

        let mut carrier: HashMap<String, String> = HashMap::new();
        opentelemetry::global::get_text_map_propagator(|p| p.inject_context(&cx, &mut carrier));

        let traceparent = carrier
            .get("traceparent")
            .expect("global propagator did not inject `traceparent` — is it registered?");
        assert!(
            traceparent.contains("0af7651916cd43dd8448eb211c80319c"),
            "unexpected traceparent value: {traceparent}"
        );
    }
}
