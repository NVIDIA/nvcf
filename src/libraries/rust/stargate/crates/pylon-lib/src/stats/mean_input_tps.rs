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

use std::time::Duration;

use crate::RequestObservation;
use crate::request_observer::RequestObservationState;

use super::aggregator::{tps_for_units, valid_last_mean_input_tps};
use super::collector::StatsCollectorConfig;
use super::projection::{MeanInputDirectInputSample, observed_direct_mean_input_sample};
use super::token_metrics::TpsDistribution;

pub(super) struct MeanInputTpsAggregatorConfig {
    pub(super) min_input_tokens: u64,
    pub(super) duration_floor: Duration,
}

impl From<&StatsCollectorConfig> for MeanInputTpsAggregatorConfig {
    fn from(config: &StatsCollectorConfig) -> Self {
        Self {
            min_input_tokens: config.min_input_tokens,
            duration_floor: config.duration_floor,
        }
    }
}

#[derive(Debug, Clone, PartialEq)]
pub(super) struct MeanInputTpsUpdate {
    pub(super) model_id: String,
    pub(super) last_mean_input_tps: f64,
}

#[derive(Debug, Clone)]
pub(super) struct MeanInputTpsObservation {
    pub(super) model_id: String,
    pub(super) input_tokens: u64,
    pub(super) direct_sample: Option<MeanInputDirectInputSample>,
    pub(super) state: RequestObservationState,
}

impl MeanInputTpsObservation {
    pub(super) fn from_request_observation(observation: &RequestObservation) -> Self {
        Self {
            model_id: observation.model_id.clone(),
            input_tokens: observation.input_tokens,
            direct_sample: observed_direct_mean_input_sample(observation),
            state: observation.state,
        }
    }
}

#[derive(Debug)]
pub(super) struct MeanInputModelState {
    pub(super) distribution: TpsDistribution,
}

impl MeanInputModelState {
    pub(super) fn new() -> Self {
        Self {
            distribution: TpsDistribution::default(),
        }
    }
}

pub(super) struct MeanInputTpsAggregator {
    pub(super) config: MeanInputTpsAggregatorConfig,
    pub(super) per_model: std::collections::HashMap<String, MeanInputModelState>,
}

impl MeanInputTpsAggregator {
    pub(super) fn new(config: MeanInputTpsAggregatorConfig) -> Self {
        Self {
            config,
            per_model: std::collections::HashMap::new(),
        }
    }

    #[cfg(test)]
    pub(super) fn record_request_observation(
        &mut self,
        observation: &RequestObservation,
    ) -> Vec<MeanInputTpsUpdate> {
        self.record_observation(MeanInputTpsObservation::from_request_observation(
            observation,
        ))
    }

    pub(super) fn record_observation(
        &mut self,
        observation: MeanInputTpsObservation,
    ) -> Vec<MeanInputTpsUpdate> {
        let track_input_tps = observation.input_tokens >= self.config.min_input_tokens;
        if !track_input_tps || observation.state != RequestObservationState::Complete {
            return Vec::new();
        }

        let mut updates = Vec::new();
        if let Some(sample) = observation.direct_sample
            && let Some(update) = self.record_direct_sample(&observation.model_id, sample)
        {
            updates.push(update);
        }

        updates
    }

    pub(super) fn record_sample(
        &mut self,
        model_id: &str,
        sample: f64,
    ) -> Option<MeanInputTpsUpdate> {
        if !valid_last_mean_input_tps(sample) {
            return None;
        }
        let model = self.model_state(model_id);
        model.distribution.update(sample);
        model
            .distribution
            .has_sufficient_data()
            .then(|| MeanInputTpsUpdate {
                model_id: model_id.to_string(),
                last_mean_input_tps: model.distribution.mean,
            })
            .filter(|update| valid_last_mean_input_tps(update.last_mean_input_tps))
    }

    pub(super) fn record_direct_sample(
        &mut self,
        model_id: &str,
        sample: MeanInputDirectInputSample,
    ) -> Option<MeanInputTpsUpdate> {
        let duration = if sample.clamp_duration_to_floor {
            sample.duration.max(self.config.duration_floor)
        } else {
            sample.duration
        };
        let sample = tps_for_units(sample.input_tokens, duration, self.config.duration_floor)?;
        self.record_sample(model_id, sample)
    }

    pub(super) fn model_state(&mut self, model_id: &str) -> &mut MeanInputModelState {
        self.per_model
            .entry(model_id.to_string())
            .or_insert_with(MeanInputModelState::new)
    }
}

pub(super) async fn run_mean_input_tps_aggregator(
    config: MeanInputTpsAggregatorConfig,
    observation_rx: flume::Receiver<MeanInputTpsObservation>,
    update_tx: flume::Sender<MeanInputTpsUpdate>,
) {
    let mut aggregator = MeanInputTpsAggregator::new(config);

    loop {
        let Ok(observation) = observation_rx.recv_async().await else {
            return;
        };
        let updates = aggregator.record_observation(observation);

        for update in updates {
            if update_tx.send_async(update).await.is_err() {
                return;
            }
        }
    }
}
