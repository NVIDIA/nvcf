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

use anyhow::Context;
use http::HeaderMap;
use opentelemetry::global;
use opentelemetry::trace::TracerProvider as _;
use opentelemetry_http::{HeaderExtractor, HeaderInjector};
use opentelemetry_otlp::{WithExportConfig, WithTonicConfig};
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::propagation::TraceContextPropagator;
use opentelemetry_sdk::trace::SdkTracerProvider;
use tonic::metadata::{MetadataMap, MetadataValue};
use tracing::warn;
use tracing_opentelemetry::OpenTelemetryLayer;
use tracing_subscriber::prelude::*;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::{EnvFilter, filter, fmt};

pub struct TelemetryGuard(Option<SdkTracerProvider>);

impl Drop for TelemetryGuard {
    fn drop(&mut self) {
        if let Some(provider) = self.0.as_mut()
            && let Err(err) = provider.shutdown()
        {
            warn!("failed to shutdown tracer provider: {}", err);
        }
    }
}

pub fn init_telemetry(
    otel_endpoint: Option<&str>,
    service_name: &str,
    traced_root_span: &'static str,
    access_token: Option<&str>,
) -> anyhow::Result<TelemetryGuard> {
    global::set_text_map_propagator(TraceContextPropagator::new());

    let env_filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    let fmt_layer = fmt::layer().with_target(false).compact();

    let (provider, otel_layer) = if let Some(endpoint) = otel_endpoint {
        let mut exporter_builder = opentelemetry_otlp::SpanExporter::builder()
            .with_tonic()
            .with_endpoint(endpoint.trim().to_string());
        // Load CA roots (webpki set is embedded) so https collectors don't fail
        // with UnknownIssuer on the distroless image.
        if endpoint.trim().starts_with("https://") {
            exporter_builder = exporter_builder
                .with_tls_config(tonic::transport::ClientTlsConfig::new().with_enabled_roots());
        }
        if let Some(token) = access_token
            .map(str::trim)
            .filter(|token| !token.is_empty())
        {
            let value = MetadataValue::try_from(token)
                .context("lightstep access token is not a valid gRPC metadata value")?;
            let mut metadata = MetadataMap::new();
            metadata.insert("lightstep-access-token", value);
            exporter_builder = exporter_builder.with_metadata(metadata);
        }
        let exporter = exporter_builder.build()?;
        let provider = SdkTracerProvider::builder()
            .with_resource(telemetry_resource(service_name))
            .with_batch_exporter(exporter)
            .build();
        let tracer = provider.tracer(service_name.to_string());
        let otel_layer = OpenTelemetryLayer::new(tracer).with_filter(filter::dynamic_filter_fn(
            move |metadata, cx| {
                metadata.is_span() && metadata.name() == traced_root_span
                    || cx.lookup_current().is_some_and(|span| {
                        span.scope()
                            .from_root()
                            .any(|s| s.name() == traced_root_span)
                    })
            },
        ));
        (Some(provider), Some(otel_layer))
    } else {
        (None, None)
    };
    let mut guard = TelemetryGuard(provider);
    tracing_subscriber::registry()
        .with(fmt_layer.with_filter(env_filter))
        .with(otel_layer)
        .try_init()
        .map_err(|err| {
            if let Some(provider) = guard.0.take()
                && let Err(shutdown_err) = provider.shutdown()
            {
                warn!(
                    "failed to shutdown tracer provider after telemetry init failure: {shutdown_err}"
                );
            }
            anyhow::anyhow!("failed to initialize telemetry subscriber: {err}")
        })?;
    Ok(guard)
}

pub fn telemetry_resource(service_name: &str) -> Resource {
    Resource::builder()
        .with_service_name(service_name.to_string())
        .build()
}

pub fn parent_context_from_headers(headers: &HeaderMap) -> opentelemetry::Context {
    global::get_text_map_propagator(|propagator| propagator.extract(&HeaderExtractor(headers)))
}

pub fn inject_trace_context(headers: &mut HeaderMap, context: &opentelemetry::Context) {
    global::get_text_map_propagator(|propagator| {
        propagator.inject_context(context, &mut HeaderInjector(headers));
    });
}

pub fn traceparent_from_headers(headers: &HeaderMap) -> Option<&str> {
    headers
        .get("traceparent")
        .and_then(|value| value.to_str().ok())
        .map(str::trim)
        .filter(|value| !value.is_empty())
}

#[cfg(test)]
mod tests {
    use super::*;
    use http::header::{HeaderName, HeaderValue};
    use opentelemetry::Key;
    use opentelemetry::trace::TraceContextExt;
    use opentelemetry::trace::noop::NoopTextMapPropagator;

    fn expect_init_error(result: anyhow::Result<TelemetryGuard>) -> anyhow::Error {
        let error = match result {
            Ok(_) => panic!("telemetry init should fail when subscriber already exists"),
            Err(error) => error,
        };
        assert!(
            error
                .to_string()
                .contains("failed to initialize telemetry subscriber"),
            "unexpected telemetry initialization error: {error:#}"
        );
        error
    }

    #[test]
    fn telemetry_resource_uses_configured_service_name() {
        let resource = telemetry_resource("llm-request-router");

        assert_eq!(
            resource
                .get(&Key::new("service.name"))
                .map(|value| value.to_string()),
            Some("llm-request-router".to_string())
        );
    }

    #[test]
    fn init_telemetry_reports_existing_global_subscriber_as_error() {
        let _ = tracing_subscriber::registry().try_init();

        expect_init_error(init_telemetry(None, "llm-request-router", "request", None));
    }

    #[tokio::test]
    async fn init_telemetry_with_exporter_reports_existing_global_subscriber_as_error() {
        let _ = tracing_subscriber::registry().try_init();

        expect_init_error(init_telemetry(
            Some("  http://127.0.0.1:4317  "),
            "llm-request-router",
            "request",
            Some("test-access-token"),
        ));
    }

    #[tokio::test]
    async fn init_telemetry_with_https_exporter_configures_tls() {
        // Regression: an https endpoint must get past OTLP exporter TLS setup
        // (the opentelemetry-otlp tls-aws-lc crypto-provider feature) and only
        // fail on the already-installed global subscriber -- not on a missing
        // TLS provider, which otherwise crashes pylon/stargate at startup.
        let _ = tracing_subscriber::registry().try_init();

        let error = expect_init_error(init_telemetry(
            Some("https://127.0.0.1:4317"),
            "llm-request-router",
            "request",
            Some("test-access-token"),
        ));
        assert!(
            !error.to_string().contains("no TLS feature is enabled"),
            "https exporter hit the missing-TLS-provider path: {error:#}"
        );
    }

    #[test]
    fn init_telemetry_without_export_installs_trace_context_propagator() {
        global::set_text_map_propagator(NoopTextMapPropagator::new());

        let _guard = init_telemetry(None, "llm-request-router", "request", None).ok();
        let mut source_headers = HeaderMap::new();
        source_headers.insert(
            HeaderName::from_static("traceparent"),
            HeaderValue::from_static("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
        );

        let parent_context = parent_context_from_headers(&source_headers);
        let span_context = parent_context.span().span_context().clone();
        assert!(span_context.is_valid());
        assert!(span_context.is_remote());
        assert_eq!(
            span_context.trace_id().to_string(),
            "4bf92f3577b34da6a3ce929d0e0e4736"
        );

        let mut forwarded_headers = HeaderMap::new();
        inject_trace_context(&mut forwarded_headers, &parent_context);

        assert_eq!(
            forwarded_headers.get("traceparent"),
            source_headers.get("traceparent")
        );
    }

    #[test]
    fn traceparent_header_trims_empty_values() {
        let mut headers = HeaderMap::new();
        headers.insert("traceparent", "  ".parse().expect("valid header value"));

        assert_eq!(traceparent_from_headers(&headers), None);
    }
}
