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
use opentelemetry_otlp::WithHttpConfig;
use opentelemetry_sdk::trace::SdkTracerProvider;
use opentelemetry_sdk::Resource;
use std::collections::HashMap;
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

    let endpoint = match (
        tracing_settings.endpoint_ip.as_deref(),
        tracing_settings.endpoint_port,
    ) {
        (Some(host), Some(port)) => {
            let host = host
                .trim_start_matches("https://")
                .trim_start_matches("http://");
            format!("http://{}:{}/v1/traces", host, port)
        }
        (Some(host), None) => {
            let host = host
                .trim_start_matches("https://")
                .trim_start_matches("http://");
            format!("http://{}:4318/v1/traces", host)
        }
        _ => "http://127.0.0.1:4318/v1/traces".to_string(),
    };

    let mut exporter_builder = opentelemetry_otlp::SpanExporter::builder()
        .with_http()
        .with_endpoint(&endpoint)
        .with_timeout(std::time::Duration::from_secs(10));

    if let Some(headers) = &tracing_settings.headers {
        let map: HashMap<String, String> = headers.clone();
        exporter_builder = exporter_builder.with_headers(map);
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
