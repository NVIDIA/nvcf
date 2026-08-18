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

use std::collections::{BTreeMap, HashMap};
use std::fmt;
use std::str::FromStr;

use serde::{Deserialize, Deserializer};

use super::ClusterComparator;

#[derive(Clone, Debug)]
pub enum LoadBalancerModelConfig {
    Name(LoadBalancerAlgorithm),
    Detailed(Box<LoadBalancerAlgorithmConfig>),
}

impl<'de> Deserialize<'de> for LoadBalancerModelConfig {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        match serde_json::Value::deserialize(deserializer)? {
            serde_json::Value::String(name) => name
                .parse()
                .map(Self::Name)
                .map_err(serde::de::Error::custom),
            value @ serde_json::Value::Object(_) => serde_json::from_value(value)
                .map(|config| Self::Detailed(Box::new(config)))
                .map_err(serde::de::Error::custom),
            _ => Err(serde::de::Error::custom(
                "load-balancer model config must be an algorithm name or detailed config object",
            )),
        }
    }
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum LoadBalancerAlgorithm {
    #[default]
    #[serde(
        alias = "power-of-two",
        alias = "powerOf2",
        alias = "powerOfN",
        alias = "powerof2",
        alias = "powerofn"
    )]
    PowerOfN,
    #[serde(alias = "groq-multiregion")]
    WaitAndWiden,
    RoundRobin,
    Random,
    Pulsar,
    #[serde(alias = "pulsar-multiregion")]
    PulsarWaitAndWiden,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum PulsarRendezvousWeight {
    #[default]
    LastMeanInputTps,
    MaxInputTps,
}

impl LoadBalancerAlgorithm {
    pub const ALL: [Self; 6] = [
        Self::PowerOfN,
        Self::WaitAndWiden,
        Self::RoundRobin,
        Self::Random,
        Self::Pulsar,
        Self::PulsarWaitAndWiden,
    ];
}

impl fmt::Display for LoadBalancerAlgorithm {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let name = match self {
            Self::PowerOfN => "power-of-n",
            Self::WaitAndWiden => "wait-and-widen",
            Self::RoundRobin => "round-robin",
            Self::Random => "random",
            Self::Pulsar => "pulsar",
            Self::PulsarWaitAndWiden => "pulsar-wait-and-widen",
        };
        f.write_str(name)
    }
}

impl FromStr for LoadBalancerAlgorithm {
    type Err = serde::de::value::Error;

    fn from_str(name: &str) -> Result<Self, Self::Err> {
        Self::deserialize(serde::de::value::StrDeserializer::<serde::de::value::Error>::new(name))
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct LoadBalancerAlgorithmOverride {
    raw: String,
    algorithm: LoadBalancerAlgorithm,
}

impl LoadBalancerAlgorithmOverride {
    pub fn parse(value: &str) -> Result<Self, LoadBalancerRoutingAlgorithmError> {
        value.parse()
    }

    pub fn requested_algorithm(&self) -> &str {
        &self.raw
    }

    pub fn algorithm(&self) -> LoadBalancerAlgorithm {
        self.algorithm
    }
}

impl FromStr for LoadBalancerAlgorithmOverride {
    type Err = LoadBalancerRoutingAlgorithmError;

    fn from_str(value: &str) -> Result<Self, Self::Err> {
        let raw = value.trim();
        let algorithm = raw
            .to_ascii_lowercase()
            .replace('_', "-")
            .parse()
            .map_err(|_| LoadBalancerRoutingAlgorithmError::Unknown {
                raw: raw.to_string(),
            })?;

        Ok(Self {
            raw: raw.to_string(),
            algorithm,
        })
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LoadBalancerRoutingAlgorithmError {
    Unknown {
        raw: String,
    },
    Unavailable {
        raw: String,
        algorithm: LoadBalancerAlgorithm,
    },
}

impl LoadBalancerRoutingAlgorithmError {
    pub fn requested_algorithm(&self) -> &str {
        match self {
            Self::Unknown { raw } | Self::Unavailable { raw, .. } => raw,
        }
    }

    pub fn reason(&self) -> &'static str {
        match self {
            Self::Unknown { .. } => "unknown",
            Self::Unavailable { .. } => "unavailable",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LoadBalancerSeedError {
    Unsupported { algorithm: LoadBalancerAlgorithm },
}

impl fmt::Display for LoadBalancerSeedError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let Self::Unsupported { algorithm } = self;
        write!(f, "seed is not supported for {algorithm}")
    }
}

impl std::error::Error for LoadBalancerSeedError {}

#[derive(Debug, Clone, Default, PartialEq)]
pub struct LoadBalancerRequestPolicy {
    pub require_cache_affinity_key: bool,
    pub require_input_tokens: bool,
    pub consider_kv_free_tokens: bool,
}

#[derive(Debug, Clone, Default, PartialEq, Deserialize)]
pub struct WaitAndWidenAlgorithmConfig {
    pub seed: Option<String>,
    #[serde(default, deserialize_with = "deserialize_present_comparator")]
    pub comparator: Option<ClusterComparator>,
    pub cache_affinity_virtual_nodes: Option<usize>,
    pub cache_affinity_backend_selection_count: Option<usize>,
    pub max_queue_time_floor_ms: Option<u64>,
    pub max_queue_time_ceil_ms: Option<u64>,
    pub ttft_bucket_size_ms: Option<u64>,
    pub next_bucket_unlock_factor: Option<f64>,
    pub n: Option<usize>,
    pub max_queued: Option<u64>,
    pub ignore_queue_time: Option<bool>,
    pub ignore_input_processing_time: Option<bool>,
}

fn deserialize_present_comparator<'de, D>(
    deserializer: D,
) -> Result<Option<ClusterComparator>, D::Error>
where
    D: Deserializer<'de>,
{
    ClusterComparator::deserialize(deserializer).map(Some)
}

const DEFAULT_POWER_OF_N_SAMPLE_COUNT: usize = 2;
pub const MAX_POWER_OF_N_SAMPLE_COUNT: usize = 64;

#[derive(Debug, Clone, PartialEq, Deserialize)]
#[serde(default)]
pub struct PowerOfNAlgorithmConfig {
    pub sample_count: usize,
    pub comparator: ClusterComparator,
}

impl Default for PowerOfNAlgorithmConfig {
    fn default() -> Self {
        Self {
            sample_count: DEFAULT_POWER_OF_N_SAMPLE_COUNT,
            comparator: ClusterComparator::default(),
        }
    }
}

impl PowerOfNAlgorithmConfig {
    pub(crate) fn validated_sample_count(&self) -> Result<usize, String> {
        let sample_count = self.sample_count;
        if !(1..=MAX_POWER_OF_N_SAMPLE_COUNT).contains(&sample_count) {
            return Err(format!(
                "power-of-n sample_count must be between 1 and {MAX_POWER_OF_N_SAMPLE_COUNT}, got {sample_count}"
            ));
        }
        Ok(sample_count)
    }
}

#[derive(Debug, Clone, PartialEq)]
pub enum LoadBalancerAlgorithmSettings {
    PowerOfN(PowerOfNAlgorithmConfig),
    WaitAndWiden(WaitAndWidenAlgorithmConfig),
    RoundRobin,
    Random,
    Pulsar(Option<String>),
    PulsarWaitAndWiden(WaitAndWidenAlgorithmConfig),
}

impl Default for LoadBalancerAlgorithmSettings {
    fn default() -> Self {
        Self::PowerOfN(PowerOfNAlgorithmConfig::default())
    }
}

impl LoadBalancerAlgorithmSettings {
    fn algorithm(&self) -> LoadBalancerAlgorithm {
        match self {
            Self::PowerOfN(_) => LoadBalancerAlgorithm::PowerOfN,
            Self::WaitAndWiden(_) => LoadBalancerAlgorithm::WaitAndWiden,
            Self::RoundRobin => LoadBalancerAlgorithm::RoundRobin,
            Self::Random => LoadBalancerAlgorithm::Random,
            Self::Pulsar(_) => LoadBalancerAlgorithm::Pulsar,
            Self::PulsarWaitAndWiden(_) => LoadBalancerAlgorithm::PulsarWaitAndWiden,
        }
    }
}

#[derive(Debug, Clone, Default)]
pub struct LoadBalancerAlgorithmConfig {
    pub request_policy: LoadBalancerRequestPolicy,
    pub max_input_work_seconds: Option<f64>,
    pub request_algorithms: HashMap<LoadBalancerAlgorithm, LoadBalancerModelConfig>,
    pub settings: LoadBalancerAlgorithmSettings,
    pub(crate) rendezvous_weight: PulsarRendezvousWeight,
}

impl LoadBalancerAlgorithmConfig {
    pub fn algorithm(&self) -> LoadBalancerAlgorithm {
        self.settings.algorithm()
    }

    pub fn pulsar_rendezvous_weight(&self) -> PulsarRendezvousWeight {
        self.rendezvous_weight
    }

    pub fn requires_cache_affinity_key(&self) -> bool {
        self.request_policy.require_cache_affinity_key
    }

    pub fn requires_input_tokens(&self) -> bool {
        self.request_policy.require_input_tokens || self.considers_kv_free_tokens()
    }

    pub fn considers_kv_free_tokens(&self) -> bool {
        self.request_policy.consider_kv_free_tokens
    }

    pub(crate) fn comparator(&self) -> Option<ClusterComparator> {
        match &self.settings {
            LoadBalancerAlgorithmSettings::PowerOfN(config) => Some(config.comparator),
            LoadBalancerAlgorithmSettings::WaitAndWiden(config) => {
                Some(config.comparator.unwrap_or_default())
            }
            LoadBalancerAlgorithmSettings::RoundRobin
            | LoadBalancerAlgorithmSettings::Random
            | LoadBalancerAlgorithmSettings::Pulsar(_)
            | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(_) => None,
        }
    }

    pub fn request_policy_mut(&mut self) -> &mut LoadBalancerRequestPolicy {
        &mut self.request_policy
    }

    pub fn seed(&self) -> Option<&str> {
        match &self.settings {
            LoadBalancerAlgorithmSettings::Pulsar(seed) => seed.as_deref(),
            LoadBalancerAlgorithmSettings::WaitAndWiden(config)
            | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(config) => config.seed.as_deref(),
            LoadBalancerAlgorithmSettings::PowerOfN(_)
            | LoadBalancerAlgorithmSettings::RoundRobin
            | LoadBalancerAlgorithmSettings::Random => None,
        }
    }

    pub fn set_seed(
        &mut self,
        seed: impl Into<Option<String>>,
    ) -> Result<(), LoadBalancerSeedError> {
        let seed = seed.into();
        let algorithm = self.algorithm();
        match &mut self.settings {
            LoadBalancerAlgorithmSettings::Pulsar(current_seed) => {
                *current_seed = seed;
                Ok(())
            }
            LoadBalancerAlgorithmSettings::WaitAndWiden(config)
            | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(config) => {
                config.seed = seed;
                Ok(())
            }
            LoadBalancerAlgorithmSettings::PowerOfN(_)
            | LoadBalancerAlgorithmSettings::RoundRobin
            | LoadBalancerAlgorithmSettings::Random => {
                Err(LoadBalancerSeedError::Unsupported { algorithm })
            }
        }
    }

    pub fn wait_and_widen_settings(&self) -> Option<&WaitAndWidenAlgorithmConfig> {
        match &self.settings {
            LoadBalancerAlgorithmSettings::WaitAndWiden(config)
            | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(config) => Some(config),
            LoadBalancerAlgorithmSettings::PowerOfN(_)
            | LoadBalancerAlgorithmSettings::RoundRobin
            | LoadBalancerAlgorithmSettings::Random
            | LoadBalancerAlgorithmSettings::Pulsar(_) => None,
        }
    }

    pub fn wait_and_widen_settings_mut(&mut self) -> Option<&mut WaitAndWidenAlgorithmConfig> {
        match &mut self.settings {
            LoadBalancerAlgorithmSettings::WaitAndWiden(config)
            | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(config) => Some(config),
            LoadBalancerAlgorithmSettings::PowerOfN(_)
            | LoadBalancerAlgorithmSettings::RoundRobin
            | LoadBalancerAlgorithmSettings::Random
            | LoadBalancerAlgorithmSettings::Pulsar(_) => None,
        }
    }

    pub fn power_of_n_settings(&self) -> Option<&PowerOfNAlgorithmConfig> {
        match &self.settings {
            LoadBalancerAlgorithmSettings::PowerOfN(config) => Some(config),
            LoadBalancerAlgorithmSettings::WaitAndWiden(_)
            | LoadBalancerAlgorithmSettings::RoundRobin
            | LoadBalancerAlgorithmSettings::Random
            | LoadBalancerAlgorithmSettings::Pulsar(_)
            | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(_) => None,
        }
    }

    pub fn power_of_n_settings_mut(&mut self) -> Option<&mut PowerOfNAlgorithmConfig> {
        match &mut self.settings {
            LoadBalancerAlgorithmSettings::PowerOfN(config) => Some(config),
            LoadBalancerAlgorithmSettings::WaitAndWiden(_)
            | LoadBalancerAlgorithmSettings::RoundRobin
            | LoadBalancerAlgorithmSettings::Random
            | LoadBalancerAlgorithmSettings::Pulsar(_)
            | LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(_) => None,
        }
    }
}

impl From<LoadBalancerAlgorithm> for LoadBalancerAlgorithmConfig {
    fn from(algorithm: LoadBalancerAlgorithm) -> Self {
        Self {
            settings: LoadBalancerAlgorithmSettings::from(algorithm),
            ..Self::default()
        }
    }
}

impl From<LoadBalancerAlgorithm> for LoadBalancerAlgorithmSettings {
    fn from(algorithm: LoadBalancerAlgorithm) -> Self {
        match algorithm {
            LoadBalancerAlgorithm::PowerOfN => Self::PowerOfN(Default::default()),
            LoadBalancerAlgorithm::WaitAndWiden => Self::WaitAndWiden(Default::default()),
            LoadBalancerAlgorithm::RoundRobin => Self::RoundRobin,
            LoadBalancerAlgorithm::Random => Self::Random,
            LoadBalancerAlgorithm::Pulsar => Self::Pulsar(None),
            LoadBalancerAlgorithm::PulsarWaitAndWiden => {
                Self::PulsarWaitAndWiden(Default::default())
            }
        }
    }
}

#[derive(Debug, Default, Deserialize)]
struct RawCommonAlgorithmConfig {
    require_cache_affinity_key: Option<bool>,
    require_input_tokens: Option<bool>,
    max_input_work_seconds: Option<f64>,
    #[serde(default)]
    request_algorithms: HashMap<LoadBalancerAlgorithm, LoadBalancerModelConfig>,
    #[serde(flatten)]
    unsupported_fields: BTreeMap<String, serde_json::Value>,
}

impl RawCommonAlgorithmConfig {
    fn into_config(
        self,
        settings: LoadBalancerAlgorithmSettings,
        consider_kv_free_tokens: Option<bool>,
        rendezvous_weight: PulsarRendezvousWeight,
    ) -> Result<LoadBalancerAlgorithmConfig, String> {
        let algorithm = settings.algorithm();
        if !self.unsupported_fields.is_empty() {
            return Err(format!(
                "{} config does not support field(s): {}",
                algorithm,
                self.unsupported_fields
                    .keys()
                    .map(String::as_str)
                    .collect::<Vec<_>>()
                    .join(", ")
            ));
        }
        Ok(LoadBalancerAlgorithmConfig {
            request_policy: LoadBalancerRequestPolicy {
                require_cache_affinity_key: self.require_cache_affinity_key.unwrap_or_default(),
                require_input_tokens: self.require_input_tokens.unwrap_or_default(),
                consider_kv_free_tokens: consider_kv_free_tokens.unwrap_or_default(),
            },
            max_input_work_seconds: self.max_input_work_seconds,
            request_algorithms: self.request_algorithms,
            settings,
            rendezvous_weight,
        })
    }
}

#[derive(Debug, Deserialize)]
#[serde(tag = "algorithm", rename_all = "kebab-case")]
enum RawLoadBalancerAlgorithmConfig {
    // Technical debt: these aliases duplicate LoadBalancerAlgorithm because serde parses this
    // tagged detailed-config enum independently. Keep both lists in sync until NVIDIA/nvcf#831
    // centralizes algorithm-name parsing.
    #[serde(
        alias = "power-of-two",
        alias = "powerOf2",
        alias = "powerOfN",
        alias = "powerof2",
        alias = "powerofn"
    )]
    PowerOfN {
        #[serde(flatten)]
        settings: PowerOfNAlgorithmConfig,
        #[serde(flatten)]
        common: RawCommonAlgorithmConfig,
    },
    #[serde(alias = "groq-multiregion")]
    WaitAndWiden {
        #[serde(flatten)]
        settings: WaitAndWidenAlgorithmConfig,
        #[serde(flatten)]
        common: RawCommonAlgorithmConfig,
    },
    RoundRobin(RawCommonAlgorithmConfig),
    Random(RawCommonAlgorithmConfig),
    Pulsar {
        seed: Option<String>,
        consider_kv_free_tokens: Option<bool>,
        #[serde(default)]
        rendezvous_weight: PulsarRendezvousWeight,
        #[serde(flatten)]
        common: RawCommonAlgorithmConfig,
    },
    #[serde(alias = "pulsar-multiregion")]
    PulsarWaitAndWiden {
        #[serde(flatten)]
        settings: WaitAndWidenAlgorithmConfig,
        consider_kv_free_tokens: Option<bool>,
        #[serde(default)]
        rendezvous_weight: PulsarRendezvousWeight,
        #[serde(flatten)]
        common: RawCommonAlgorithmConfig,
    },
}

impl RawLoadBalancerAlgorithmConfig {
    fn normalized(
        self,
    ) -> (
        RawCommonAlgorithmConfig,
        LoadBalancerAlgorithmSettings,
        Option<bool>,
        PulsarRendezvousWeight,
    ) {
        match self {
            Self::PowerOfN { settings, common } => (
                common,
                LoadBalancerAlgorithmSettings::PowerOfN(settings),
                None,
                PulsarRendezvousWeight::default(),
            ),
            Self::WaitAndWiden { settings, common } => (
                common,
                LoadBalancerAlgorithmSettings::WaitAndWiden(settings),
                None,
                PulsarRendezvousWeight::default(),
            ),
            Self::RoundRobin(common) => (
                common,
                LoadBalancerAlgorithmSettings::RoundRobin,
                None,
                PulsarRendezvousWeight::default(),
            ),
            Self::Random(common) => (
                common,
                LoadBalancerAlgorithmSettings::Random,
                None,
                PulsarRendezvousWeight::default(),
            ),
            Self::Pulsar {
                seed,
                consider_kv_free_tokens,
                rendezvous_weight,
                common,
            } => (
                common,
                LoadBalancerAlgorithmSettings::Pulsar(seed),
                consider_kv_free_tokens,
                rendezvous_weight,
            ),
            Self::PulsarWaitAndWiden {
                settings,
                consider_kv_free_tokens,
                rendezvous_weight,
                common,
            } => (
                common,
                LoadBalancerAlgorithmSettings::PulsarWaitAndWiden(settings),
                consider_kv_free_tokens,
                rendezvous_weight,
            ),
        }
    }

    fn into_config(self) -> Result<LoadBalancerAlgorithmConfig, String> {
        let (common, settings, consider_kv_free_tokens, rendezvous_weight) = self.normalized();
        if let LoadBalancerAlgorithmSettings::PowerOfN(config) = &settings {
            config.validated_sample_count()?;
        }
        common.into_config(settings, consider_kv_free_tokens, rendezvous_weight)
    }
}

impl<'de> Deserialize<'de> for LoadBalancerAlgorithmConfig {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        RawLoadBalancerAlgorithmConfig::deserialize(deserializer)?
            .into_config()
            .map_err(serde::de::Error::custom)
    }
}

impl LoadBalancerModelConfig {
    pub fn into_algorithm_config(self) -> LoadBalancerAlgorithmConfig {
        match self {
            Self::Name(algorithm) => LoadBalancerAlgorithmConfig::from(algorithm),
            Self::Detailed(config) => *config,
        }
    }
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LoadBalancerConfig {
    #[serde(default)]
    pub default: LoadBalancerAlgorithm,
    #[serde(default)]
    pub request_algorithms: HashMap<LoadBalancerAlgorithm, LoadBalancerModelConfig>,
    #[serde(default)]
    pub models: HashMap<String, LoadBalancerModelConfig>,
}

impl LoadBalancerConfig {
    /// Config used when no config file is given: `power-of-n` by default,
    /// and any built-in algorithm can be selected per request.
    pub fn permissive_default() -> Self {
        Self {
            request_algorithms: LoadBalancerAlgorithm::ALL
                .iter()
                .map(|algorithm| (*algorithm, LoadBalancerModelConfig::Name(*algorithm)))
                .collect(),
            ..Self::default()
        }
    }
}
