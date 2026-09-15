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

#[derive(Debug, Clone)]
pub struct RequestQualityMonitorConfig {
    pub collect_quality_metrics: bool,
    pub collect_quality_metrics_min_tokens: u32,
    pub output_tokens_threshold_min: Option<u32>,
    pub output_compression_threshold_max: Option<f64>,
    pub output_degeneracy_threshold_min: Option<f64>,
    pub output_repetition_1gram_threshold_min: Option<f64>,
    pub output_repetition_2gram_threshold_min: Option<f64>,
    pub output_repetition_3gram_threshold_min: Option<f64>,
    pub median_logprob_threshold_max: Option<f32>,
}

impl Default for RequestQualityMonitorConfig {
    fn default() -> Self {
        Self {
            collect_quality_metrics: false,
            collect_quality_metrics_min_tokens: 20,
            output_tokens_threshold_min: None,
            output_compression_threshold_max: None,
            output_degeneracy_threshold_min: None,
            output_repetition_1gram_threshold_min: None,
            output_repetition_2gram_threshold_min: None,
            output_repetition_3gram_threshold_min: None,
            median_logprob_threshold_max: None,
        }
    }
}

impl RequestQualityMonitorConfig {
    pub fn enabled(&self) -> bool {
        self.collect_quality_metrics
            || self.output_tokens_threshold_min.is_some()
            || self.output_compression_threshold_max.is_some()
            || self.output_degeneracy_threshold_min.is_some()
            || self.output_repetition_1gram_threshold_min.is_some()
            || self.output_repetition_2gram_threshold_min.is_some()
            || self.output_repetition_3gram_threshold_min.is_some()
            || self.median_logprob_threshold_max.is_some()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quality_monitor_is_disabled_by_default() {
        assert!(!RequestQualityMonitorConfig::default().enabled());
    }

    #[test]
    fn quality_monitor_is_enabled_by_each_individual_option() {
        let enabled_configs = [
            RequestQualityMonitorConfig {
                collect_quality_metrics: true,
                ..RequestQualityMonitorConfig::default()
            },
            RequestQualityMonitorConfig {
                output_tokens_threshold_min: Some(1),
                ..RequestQualityMonitorConfig::default()
            },
            RequestQualityMonitorConfig {
                output_compression_threshold_max: Some(0.5),
                ..RequestQualityMonitorConfig::default()
            },
            RequestQualityMonitorConfig {
                output_degeneracy_threshold_min: Some(0.5),
                ..RequestQualityMonitorConfig::default()
            },
            RequestQualityMonitorConfig {
                output_repetition_1gram_threshold_min: Some(0.5),
                ..RequestQualityMonitorConfig::default()
            },
            RequestQualityMonitorConfig {
                output_repetition_2gram_threshold_min: Some(0.5),
                ..RequestQualityMonitorConfig::default()
            },
            RequestQualityMonitorConfig {
                output_repetition_3gram_threshold_min: Some(0.5),
                ..RequestQualityMonitorConfig::default()
            },
            RequestQualityMonitorConfig {
                median_logprob_threshold_max: Some(-5.0),
                ..RequestQualityMonitorConfig::default()
            },
        ];

        for config in enabled_configs {
            assert!(config.enabled(), "config should enable quality monitoring");
        }
    }
}
