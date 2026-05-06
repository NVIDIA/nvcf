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

use serde::{Deserialize, Serialize};
use std::collections::HashMap;

/// Top-level server observability settings, loaded from config file under the `server:` key.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ServerSettings {
    #[serde(default)]
    pub metrics: MetricsSettings,
    #[serde(default)]
    pub tracing: TracingSettings,
    /// OpenTelemetry resource attributes injected programmatically at startup.
    #[serde(default)]
    pub resource: Option<OtelResourceSettings>,
    /// `tracing` crate `EnvFilter` directive (e.g. `"info,server=debug"`).
    #[serde(default)]
    pub envfilter_directive: Option<String>,
}

impl ServerSettings {
    pub fn default_with_service_name(_name: String) -> Self {
        Self::default()
    }
}

/// Metrics exporter configuration, loaded from `server.metrics:` in the config file.
///
/// Unknown fields (e.g. `enabled`, `service_name`, `meter_name`) are silently ignored by serde.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct MetricsSettings {
    #[serde(default)]
    pub exporters: Vec<ExporterConfig>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ExporterConfig {
    /// Exporter type identifier, e.g. `"prometheus"`.
    pub exporter: String,
    /// Listener address, e.g. `"http://0.0.0.0:41337"`.
    pub endpoint: String,
}

/// Tracing exporter configuration, loaded from `server.tracing:` in the config file.
///
/// The OTLP endpoint is derived from `endpoint_ip` + `endpoint_port` to match the existing
/// yaml schema (`endpoint_ip: https://...`, `endpoint_port: 8282`).
///
/// Unknown fields (`service_name`, `tracing_envfilter_directive`, etc.) are silently ignored.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct TracingSettings {
    /// OTLP collector host/scheme, e.g. `"https://prod.otel.example.com"`.
    #[serde(default)]
    pub endpoint_ip: Option<String>,
    /// OTLP collector port, e.g. `8282`. Accepts int or string; empty string → `None`.
    #[serde(default, deserialize_with = "de_optional_u16")]
    pub endpoint_port: Option<u16>,
    /// HTTP/gRPC headers forwarded to the OTLP collector (e.g. auth tokens).
    #[serde(default)]
    pub headers: Option<HashMap<String, String>>,
}

impl TracingSettings {
    /// Construct the full OTLP endpoint URL from `endpoint_ip` and `endpoint_port`.
    pub fn otlp_endpoint(&self) -> Option<String> {
        match (&self.endpoint_ip, self.endpoint_port) {
            (Some(ip), Some(port)) => Some(format!("{ip}:{port}")),
            (Some(ip), None) => Some(ip.clone()),
            _ => None,
        }
    }
}

/// OpenTelemetry resource attributes attached to every exported span/metric.
///
/// Populated programmatically at startup (service name, version, host ID, region).
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct OtelResourceSettings {
    #[serde(default)]
    pub attributes: Vec<(String, String)>,
}

impl OtelResourceSettings {
    pub fn add_attributes(&mut self, attrs: &[(&str, String)]) {
        for (k, v) in attrs {
            self.attributes.push((k.to_string(), v.clone()));
        }
    }
}

fn de_optional_u16<'de, D>(deserializer: D) -> Result<Option<u16>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    use serde_json::Value;
    match Value::deserialize(deserializer)? {
        Value::Null => Ok(None),
        Value::Number(n) => n
            .as_u64()
            .and_then(|v| u16::try_from(v).ok())
            .map(Some)
            .ok_or_else(|| serde::de::Error::custom(format!("port out of u16 range: {n}"))),
        Value::String(s) if s.trim().is_empty() => Ok(None),
        Value::String(s) => s
            .trim()
            .parse::<u16>()
            .map(Some)
            .map_err(|_| serde::de::Error::custom(format!("invalid port: {s:?}"))),
        other => Err(serde::de::Error::custom(format!(
            "expected integer or string for port, got {other}"
        ))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse_tracing(json: &str) -> Result<TracingSettings, serde_json::Error> {
        serde_json::from_str(json)
    }

    #[test]
    fn endpoint_port_from_integer() {
        let t = parse_tracing(r#"{"endpoint_port": 8282}"#).unwrap();
        assert_eq!(t.endpoint_port, Some(8282));
    }

    #[test]
    fn endpoint_port_from_string() {
        let t = parse_tracing(r#"{"endpoint_port": "4317"}"#).unwrap();
        assert_eq!(t.endpoint_port, Some(4317));
    }

    #[test]
    fn endpoint_port_empty_string_is_none() {
        let t = parse_tracing(r#"{"endpoint_port": ""}"#).unwrap();
        assert_eq!(t.endpoint_port, None);
    }

    #[test]
    fn endpoint_port_null_is_none() {
        let t = parse_tracing(r#"{"endpoint_port": null}"#).unwrap();
        assert_eq!(t.endpoint_port, None);
    }

    #[test]
    fn endpoint_port_absent_is_none() {
        let t = parse_tracing(r#"{}"#).unwrap();
        assert_eq!(t.endpoint_port, None);
    }

    #[test]
    fn endpoint_port_invalid_string_errors() {
        assert!(parse_tracing(r#"{"endpoint_port": "abc"}"#).is_err());
    }

    #[test]
    fn endpoint_port_out_of_range_errors() {
        assert!(parse_tracing(r#"{"endpoint_port": 99999}"#).is_err());
    }
}
