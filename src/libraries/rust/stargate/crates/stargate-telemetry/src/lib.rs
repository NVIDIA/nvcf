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

use http::HeaderMap;
use opentelemetry::global;
use opentelemetry::trace::TracerProvider as _;
use opentelemetry_otlp::WithExportConfig;
use opentelemetry_sdk::Resource;
use opentelemetry_sdk::propagation::TraceContextPropagator;
use opentelemetry_sdk::trace::SdkTracerProvider;
use tracing::warn;
use tracing_opentelemetry::OpenTelemetryLayer;
use tracing_subscriber::prelude::*;
use tracing_subscriber::util::SubscriberInitExt;
use tracing_subscriber::{EnvFilter, filter, fmt};

pub struct TelemetryGuard {
    tracer_provider: Option<SdkTracerProvider>,
}

impl Drop for TelemetryGuard {
    fn drop(&mut self) {
        if let Some(provider) = self.tracer_provider.as_mut()
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
) -> anyhow::Result<TelemetryGuard> {
    global::set_text_map_propagator(TraceContextPropagator::new());

    let env_filter = EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info"));
    let fmt_layer = fmt::layer().with_target(false).compact();

    if let Some(endpoint) = otel_endpoint {
        let exporter = opentelemetry_otlp::SpanExporter::builder()
            .with_tonic()
            .with_endpoint(endpoint.trim().to_string())
            .build()?;

        let provider = SdkTracerProvider::builder()
            .with_resource(telemetry_resource(service_name))
            .with_batch_exporter(exporter)
            .build();

        let tracer = provider.tracer(service_name.to_string());
        let otel_layer = OpenTelemetryLayer::new(tracer).with_filter(filter::dynamic_filter_fn(
            move |metadata, cx| {
                if metadata.is_span() && metadata.name() == traced_root_span {
                    return true;
                }
                cx.lookup_current()
                    .map(|span| {
                        span.scope()
                            .from_root()
                            .any(|s| s.name() == traced_root_span)
                    })
                    .unwrap_or(false)
            },
        ));

        if let Err(err) = install_subscriber(
            tracing_subscriber::registry()
                .with(fmt_layer.with_filter(env_filter))
                .with(otel_layer),
        ) {
            if let Err(shutdown_err) = provider.shutdown() {
                warn!(
                    "failed to shutdown tracer provider after telemetry init failure: {shutdown_err}"
                );
            }
            return Err(err);
        }

        Ok(TelemetryGuard {
            tracer_provider: Some(provider),
        })
    } else {
        install_subscriber(tracing_subscriber::registry().with(fmt_layer.with_filter(env_filter)))?;

        Ok(TelemetryGuard {
            tracer_provider: None,
        })
    }
}

fn install_subscriber<S>(subscriber: S) -> anyhow::Result<()>
where
    S: tracing::Subscriber + Send + Sync + 'static,
{
    subscriber
        .try_init()
        .map_err(|err| anyhow::anyhow!("failed to initialize telemetry subscriber: {err}"))
}

pub fn telemetry_resource(service_name: &str) -> Resource {
    Resource::builder()
        .with_service_name(service_name.to_string())
        .build()
}

pub fn parent_context_from_headers(headers: &HeaderMap) -> opentelemetry::Context {
    global::get_text_map_propagator(|propagator| {
        propagator.extract(&opentelemetry_http::HeaderExtractor(headers))
    })
}

pub fn inject_trace_context(headers: &mut HeaderMap, context: &opentelemetry::Context) {
    global::get_text_map_propagator(|propagator| {
        propagator.inject_context(context, &mut opentelemetry_http::HeaderInjector(headers));
    });
}

pub fn traceparent_from_headers(headers: &HeaderMap) -> Option<&str> {
    header_str(headers, "traceparent")
}

fn header_str<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers
        .get(name)
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

        let result = init_telemetry(None, "llm-request-router", "request");
        let err = match result {
            Ok(_) => panic!("init_telemetry should fail when a global subscriber already exists"),
            Err(err) => err,
        };

        assert!(
            err.to_string()
                .contains("failed to initialize telemetry subscriber"),
            "unexpected telemetry initialization error: {err:#}"
        );
    }

    #[tokio::test]
    async fn init_telemetry_with_exporter_reports_existing_global_subscriber_as_error() {
        let _ = tracing_subscriber::registry().try_init();

        let result = init_telemetry(
            Some("http://127.0.0.1:4317"),
            "llm-request-router",
            "request",
        );
        let err = match result {
            Ok(_) => panic!("init_telemetry should fail when a global subscriber already exists"),
            Err(err) => err,
        };

        assert!(
            err.to_string()
                .contains("failed to initialize telemetry subscriber"),
            "unexpected telemetry initialization error: {err:#}"
        );
    }

    #[test]
    fn init_telemetry_without_export_installs_trace_context_propagator() {
        global::set_text_map_propagator(NoopTextMapPropagator::new());

        let _guard = init_telemetry(None, "llm-request-router", "request").ok();
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
