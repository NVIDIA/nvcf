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

//! Server-side config types (metrics, tracing, resource) used when loading app settings.
use serde::{Deserialize, Serialize};
use std::collections::HashMap;

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct MetricsExporter {
    pub exporter: String,
    pub protocol: String,
    pub endpoint: String,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct MetricsSettings {
    pub enabled: Option<bool>,
    pub exporters: Vec<MetricsExporter>,
    pub service_name: Option<String>,
    pub meter_name: Option<String>,
    pub resource_attributes: Option<serde_json::Value>,
    #[serde(default = "default_metrics_idle_timeout")]
    pub idle_timeout_seconds: u64,
}

fn default_metrics_idle_timeout() -> u64 {
    2700
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TracingSettings {
    pub endpoint_ip: Option<String>,
    pub endpoint_port: Option<u16>,
    pub headers: Option<HashMap<String, String>>,
    pub tracing_envfilter_directive: Option<String>,
    pub logging_envfilter_directive: Option<String>,
    pub logging_nonblocking_output: Option<bool>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TonicSettings {
    pub initial_connection_window_size: Option<u32>,
    pub initial_stream_window_size: Option<u32>,
    pub max_decoding_message_size: Option<u32>,
    pub request_timeout: Option<u32>,
}

/// OpenTelemetry resource attributes (key-value pairs for spans).
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct OtelResourceSettings {
    #[serde(default)]
    pub attributes: Vec<(String, String)>,
}

impl OtelResourceSettings {
    pub fn add_attributes(&mut self, attrs: &[(&str, String)]) {
        for (k, v) in attrs {
            self.attributes.push(((*k).to_string(), v.clone()));
        }
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ServerSettings {
    pub ip_address: Option<String>,
    pub port: Option<String>,
    pub metrics: MetricsSettings,
    pub tracing: TracingSettings,
    pub resource: Option<OtelResourceSettings>,
    pub envfilter_directive: Option<String>,
    pub tonic: Option<TonicSettings>,
}

impl ServerSettings {
    pub fn default_with_service_name(service_name: String) -> Self {
        Self {
            metrics: MetricsSettings {
                service_name: Some(service_name),
                ..Default::default()
            },
            ..Default::default()
        }
    }
}
