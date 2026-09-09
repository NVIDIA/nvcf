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
use std::net::{IpAddr, SocketAddr};

const DEFAULT_SERVER_IP_ADDRESS: &str = "0.0.0.0";
const DEFAULT_SERVER_PORT: u16 = 8080;

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct MetricsExporter {
    pub exporter: String,
    pub endpoint: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct MetricsSettings {
    #[serde(default)]
    pub enabled: Option<bool>,
    #[serde(default)]
    pub exporters: Vec<MetricsExporter>,
    #[serde(default = "default_metrics_idle_timeout")]
    pub idle_timeout_seconds: u64,
}

impl Default for MetricsSettings {
    fn default() -> Self {
        Self {
            enabled: None,
            exporters: Vec::new(),
            idle_timeout_seconds: default_metrics_idle_timeout(),
        }
    }
}

impl MetricsSettings {
    pub fn is_enabled(&self) -> bool {
        self.enabled.unwrap_or(true)
    }
}

fn default_metrics_idle_timeout() -> u64 {
    2700
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct TracingSettings {
    #[serde(default)]
    pub endpoint_ip: Option<String>,
    #[serde(default, deserialize_with = "deserialize_optional_u16")]
    pub endpoint_port: Option<u16>,
    #[serde(default)]
    pub headers: Option<HashMap<String, String>>,
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
    #[serde(default)]
    pub ip_address: Option<String>,
    #[serde(default, deserialize_with = "deserialize_optional_u16")]
    pub port: Option<u16>,
    #[serde(default)]
    pub metrics: MetricsSettings,
    #[serde(default)]
    pub tracing: TracingSettings,
    #[serde(default)]
    pub resource: Option<OtelResourceSettings>,
    #[serde(default)]
    pub envfilter_directive: Option<String>,
}

impl ServerSettings {
    pub fn listen_addr(&self) -> Result<SocketAddr, String> {
        let ip_address = self
            .ip_address
            .as_deref()
            .unwrap_or(DEFAULT_SERVER_IP_ADDRESS)
            .parse::<IpAddr>()
            .map_err(|error| format!("invalid server.ip_address: {error}"))?;

        Ok(SocketAddr::new(
            ip_address,
            self.port.unwrap_or(DEFAULT_SERVER_PORT),
        ))
    }
}

fn deserialize_optional_u16<'de, D>(deserializer: D) -> Result<Option<u16>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    use serde_json::Value;

    match Value::deserialize(deserializer)? {
        Value::Null => Ok(None),
        Value::Number(number) => number
            .as_u64()
            .and_then(|value| u16::try_from(value).ok())
            .map(Some)
            .ok_or_else(|| serde::de::Error::custom(format!("port out of u16 range: {number}"))),
        Value::String(value) if value.trim().is_empty() => Ok(None),
        Value::String(value) => value
            .trim()
            .parse::<u16>()
            .map(Some)
            .map_err(|_| serde::de::Error::custom(format!("invalid port: {value:?}"))),
        value => Err(serde::de::Error::custom(format!(
            "expected integer or string for port, got {value}"
        ))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn server_address_uses_runtime_defaults() {
        assert_eq!(
            ServerSettings::default().listen_addr().unwrap(),
            "0.0.0.0:8080".parse().unwrap()
        );
    }

    #[test]
    fn server_address_uses_configured_values() {
        let settings: ServerSettings =
            serde_json::from_str(r#"{"ip_address":"127.0.0.1","port":"8083"}"#).unwrap();

        assert_eq!(
            settings.listen_addr().unwrap(),
            "127.0.0.1:8083".parse().unwrap()
        );
    }

    #[test]
    fn metrics_are_enabled_by_default() {
        assert!(MetricsSettings::default().is_enabled());
        assert_eq!(
            MetricsSettings::default().idle_timeout_seconds,
            default_metrics_idle_timeout()
        );
    }

    #[test]
    fn metrics_can_be_disabled() {
        let settings: MetricsSettings = serde_json::from_str(r#"{"enabled":false}"#).unwrap();

        assert!(!settings.is_enabled());
    }
}
