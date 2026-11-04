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

use crate::cassandra::cassandra_settings::CassandraSettings;
use crate::nvcf_api::nvcf_client::NvcfApiSettings;
use crate::scaling::ScalingSettings;
use crate::timeseries_db::TimeseriesDbSettings;
use clap::Parser;
use config::{Config, Environment, File};
use serde::{Deserialize, Serialize};
use std::path::PathBuf;
use std::process::exit;

pub mod server_settings;
pub use server_settings::{MetricsSettings, OtelResourceSettings, ServerSettings, TracingSettings};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AppSettings {
    #[serde(default)]
    pub server: ServerSettings,
    #[serde(default = "default_region")]
    pub region: String,
    pub secrets_path: Option<PathBuf>,
    #[serde(default)]
    pub cassandra: CassandraSettings,
    #[serde(default)]
    pub timeseries_db: TimeseriesDbSettings,
    #[serde(default)]
    pub scaling: ScalingSettings,
    #[serde(default)]
    pub nvcf_api: NvcfApiSettings,
}

fn default_region() -> String {
    std::env::var("AWS_REGION").unwrap_or_else(|_| "us-west-1".to_string())
}

impl Default for AppSettings {
    fn default() -> Self {
        let region = default_region();
        let cassandra = CassandraSettings::default();
        let timeseries_db = TimeseriesDbSettings::default();
        let scaling = ScalingSettings::default();
        let secrets_path = Some(PathBuf::from("local_env/vault/secrets.json"));

        AppSettings {
            server: ServerSettings::default(),
            region,
            secrets_path,
            cassandra,
            timeseries_db,
            scaling,
            nvcf_api: NvcfApiSettings::default(),
        }
    }
}

#[derive(Parser, Debug, Serialize, Deserialize)]
#[command(version, about, long_about = None)]
pub struct AppCliArgs {
    /// Path to configuration file (YAML, JSON, or TOML)
    #[arg(short, long, env = "CONFIG")]
    pub config: Option<PathBuf>,
}

/// Load settings from config file (if -c/--config given) and environment variables.
/// Env vars use __ as separator, e.g. SCALING__LOOKBACK_SECONDS=90.
pub fn parse_settings() -> (AppCliArgs, AppSettings) {
    let args = AppCliArgs::parse();

    let mut builder = Config::builder();
    if let Some(ref path) = args.config {
        builder = builder.add_source(File::from(path.clone()).required(false));
    }
    builder = builder.add_source(Environment::default().separator("__").try_parsing(true));

    let (args, mut settings) = match builder.build() {
        Ok(cfg) => match cfg.try_deserialize::<AppSettings>() {
            Ok(s) => (args, s),
            Err(e) => {
                eprintln!("Error deserializing config: {}", e);
                exit(-1);
            }
        },
        Err(e) => {
            eprintln!("Error loading config: {}", e);
            exit(-1);
        }
    };

    // When no config file was provided, use the local default secrets path.
    if args.config.is_none() {
        let defaults = AppSettings::default();
        if settings.secrets_path.is_none() {
            settings.secrets_path = defaults.secrets_path;
        }
        if settings.region.is_empty() {
            settings.region = defaults.region;
        }
    }

    // Inject OpenTelemetry resource attributes for all spans
    let mut resource = settings
        .server
        .resource
        .clone()
        .unwrap_or_else(OtelResourceSettings::default);

    resource.add_attributes(&[
        (
            opentelemetry_semantic_conventions::resource::SERVICE_NAME,
            env!("CARGO_PKG_NAME").to_string(),
        ),
        (
            opentelemetry_semantic_conventions::resource::SERVICE_VERSION,
            env!("CARGO_PKG_VERSION").to_string(),
        ),
        (
            opentelemetry_semantic_conventions::resource::HOST_ID,
            gethostname::gethostname()
                .to_str()
                .map(|s| s.to_string())
                .unwrap_or_else(|| uuid::Uuid::new_v4().to_string()),
        ),
        (
            opentelemetry_semantic_conventions::resource::CLOUD_REGION,
            settings.region.clone(),
        ),
        ("host.dc", settings.region.clone()),
    ]);
    settings.server.resource = Some(resource);

    (args, settings)
}
