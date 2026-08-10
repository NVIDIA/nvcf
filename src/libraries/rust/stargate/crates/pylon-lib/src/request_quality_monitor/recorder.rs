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

use super::{
    QualityCheckResult, RequestQualityMonitorConfig, TextQualityMetrics,
    approximate_output_token_count, evaluate_quality,
};
use sonic_rs::{JsonContainerTrait, JsonValueTrait, Value};
use std::collections::BTreeMap;

#[derive(Debug, Default)]
struct ChoiceQualityObservation {
    output_text: String,
    observed_logprobs: Vec<f32>,
    observed_output_tokens: u64,
}

#[derive(Default)]
struct ChoiceQualitySummary {
    representative: Option<TextQualityMetrics>,
    best_match: Option<(&'static str, TextQualityMetrics)>,
    estimated_tokens: u64,
    attributable_tokens: u64,
    has_scoreable_choice: bool,
}

#[derive(Debug, Default)]
pub struct RequestQualityRecorder {
    choices: BTreeMap<usize, ChoiceQualityObservation>,
    output_tokens: u64,
    observed_chunk: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum RequestOutputTokenProgress {
    Delta(u64),
    Cumulative { tokens: u64, delta: u64 },
}

impl RequestQualityRecorder {
    pub fn new() -> Self {
        Self::default()
    }

    pub(crate) fn observe_json_chunk(
        &mut self,
        value: Option<&Value>,
        output_token_progress: Option<RequestOutputTokenProgress>,
    ) {
        self.observed_chunk = true;
        let choices = value.and_then(|value| value["choices"].as_array());
        let single_chunk_choice = choices
            .filter(|choices| choices.len() == 1)
            .map(|choices| choice_index(&choices[0], 0));
        for (fallback_index, choice) in choices.into_iter().flatten().enumerate() {
            let choice_index = choice_index(choice, fallback_index);
            if let Some(content) = choice["delta"]["content"].as_str() {
                self.choices
                    .entry(choice_index)
                    .or_default()
                    .output_text
                    .push_str(content);
            }
            if let Some(entries) = choice["logprobs"]["content"].as_array() {
                let mut values = entries
                    .iter()
                    .filter_map(|entry| entry["logprob"].as_f64())
                    .map(|value| value as f32)
                    .peekable();
                if values.peek().is_some() {
                    self.choices
                        .entry(choice_index)
                        .or_default()
                        .observed_logprobs
                        .extend(values);
                }
            }
        }
        if let Some(progress) = output_token_progress {
            self.observe_output_token_progress_for_choice(progress, single_chunk_choice);
        }
    }

    fn observe_output_token_progress_for_choice(
        &mut self,
        progress: RequestOutputTokenProgress,
        single_chunk_choice: Option<usize>,
    ) {
        match progress {
            RequestOutputTokenProgress::Delta(delta) => {
                // Token observations are telemetry counters; saturate instead of wrapping.
                self.output_tokens = self.output_tokens.saturating_add(delta);
                let choice_index = single_chunk_choice.or_else(|| {
                    if self.choices.len() == 1 {
                        self.choices.keys().next().copied()
                    } else {
                        None
                    }
                });
                if let Some(choice_index) = choice_index {
                    let tokens = &mut self
                        .choices
                        .entry(choice_index)
                        .or_default()
                        .observed_output_tokens;
                    *tokens = tokens.saturating_add(delta);
                }
            }
            RequestOutputTokenProgress::Cumulative { tokens, delta } => {
                self.output_tokens = tokens;
                if self.choices.len() == 1
                    && let Some(choice) = self.choices.values_mut().next()
                {
                    choice.observed_output_tokens = tokens;
                } else if let Some(choice_index) = single_chunk_choice {
                    let choice = self.choices.entry(choice_index).or_default();
                    choice.observed_output_tokens =
                        choice.observed_output_tokens.saturating_add(delta);
                }
            }
        }
    }

    pub fn evaluate(
        &self,
        config: &RequestQualityMonitorConfig,
    ) -> (TextQualityMetrics, QualityCheckResult) {
        let summary = self.evaluate_choices(config);
        let output_tokens = if self.output_tokens == 0 {
            summary.estimated_tokens
        } else {
            self.output_tokens
        };
        let request_output_tokens_evaluable = config.output_tokens_threshold_min.is_some()
            && if self.output_tokens == 0 {
                output_tokens > 0
            } else {
                summary.has_scoreable_choice
                    && (self.choices.len() == 1
                        || summary.attributable_tokens == self.output_tokens)
            };
        let output_tokens_match_reason = (request_output_tokens_evaluable
            && config
                .output_tokens_threshold_min
                .is_some_and(|threshold| output_tokens > u64::from(threshold)))
        .then_some("output_tokens");
        let threshold_match_reason =
            output_tokens_match_reason.or(summary.best_match.map(|(reason, _)| reason));
        let representative_metrics = if output_tokens_match_reason.is_some() {
            summary.representative.unwrap_or_default()
        } else {
            summary
                .best_match
                .map(|(_, metrics)| metrics)
                .or(summary.representative)
                .unwrap_or_default()
        };

        (
            representative_metrics,
            QualityCheckResult {
                evaluated: request_output_tokens_evaluable || summary.representative.is_some(),
                threshold_match_reason,
            },
        )
    }

    fn evaluate_choices(&self, config: &RequestQualityMonitorConfig) -> ChoiceQualitySummary {
        // Request-level token thresholds operate on the full request, but text and
        // logprob heuristics score each streamed choice independently.
        let config = RequestQualityMonitorConfig {
            output_tokens_threshold_min: None,
            ..config.clone()
        };
        let mut summary = ChoiceQualitySummary {
            has_scoreable_choice: self.output_tokens == 0,
            ..Default::default()
        };
        for choice in self.choices.values() {
            let approximate_tokens = approximate_output_token_count(&choice.output_text);
            let estimated = if self.output_tokens == 0 {
                approximate_tokens
            } else {
                0
            };
            summary.estimated_tokens += estimated;
            summary.attributable_tokens += choice.observed_output_tokens;
            summary.has_scoreable_choice |=
                !choice.observed_logprobs.is_empty() || approximate_tokens > 0;
            let tokens = if choice.observed_output_tokens != 0 {
                choice.observed_output_tokens
            } else if !choice.observed_logprobs.is_empty() {
                choice.observed_logprobs.len() as u64
            } else if self.output_tokens == 0 {
                estimated
            } else if self.choices.len() == 1 {
                self.output_tokens
            } else {
                // Request-wide usage cannot be safely split across multiple choices.
                0
            };
            let (metrics, result) = evaluate_quality(
                &choice.output_text,
                tokens,
                median_logprob(&choice.observed_logprobs),
                &config,
            );
            if result.evaluated {
                summary.representative.get_or_insert(metrics);
            }
            if let Some(reason) = result.threshold_match_reason
                && summary.best_match.is_none_or(|(best_reason, _)| {
                    threshold_match_priority(reason) < threshold_match_priority(best_reason)
                })
            {
                summary.best_match = Some((reason, metrics));
            }
        }
        summary
    }

    pub fn has_observed_stream_output(&self) -> bool {
        self.observed_chunk
    }
}

fn choice_index(choice: &Value, fallback_index: usize) -> usize {
    choice["index"]
        .as_u64()
        .map(|index| index as usize)
        .unwrap_or(fallback_index)
}

fn median_logprob(values: &[f32]) -> Option<f32> {
    let mut sorted = values.to_vec();
    if sorted.is_empty() {
        return None;
    }
    sorted.sort_by(f32::total_cmp);
    Some(sorted[sorted.len() / 2])
}

fn threshold_match_priority(reason: &str) -> usize {
    const REASONS: &str = "compression_ratio repetition_1gram repetition_2gram repetition_3gram degeneracy_score median_logprob";
    REASONS
        .split(' ')
        .position(|candidate| candidate == reason)
        .unwrap_or(usize::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn delta(tokens: u64) -> Option<RequestOutputTokenProgress> {
        Some(RequestOutputTokenProgress::Delta(tokens))
    }

    fn observe(
        recorder: &mut RequestQualityRecorder,
        raw_data: &str,
        progress: Option<RequestOutputTokenProgress>,
    ) {
        let value = sonic_rs::from_str(raw_data).ok();
        recorder.observe_json_chunk(value.as_ref(), progress);
    }

    macro_rules! config {
        ($($field:ident: $value:expr),* $(,)?) => {
            RequestQualityMonitorConfig {
                $($field: $value,)*
                ..Default::default()
            }
        };
    }

    macro_rules! evaluate {
        ($config:expr; $($raw_data:expr => $progress:expr),+ $(,)?) => {{
            let mut recorder = RequestQualityRecorder::new();
            $(observe(&mut recorder, $raw_data, $progress);)+
            recorder.evaluate(&$config)
        }};
    }

    #[test]
    fn recorder_passes_median_logprob_to_quality_evaluator() {
        let (_metrics, result) = evaluate!(
            config!(median_logprob_threshold_max: Some(-7.0));
            r#"{"choices":[{"delta":{"content":"word"},"logprobs":{"content":[{"token":"word","logprob":-7.5}]}}]}"# => delta(1)
        );
        assert_eq!(result.threshold_match_reason, Some("median_logprob"));
    }

    #[test]
    fn recorder_tracks_whether_stream_output_was_observed() {
        let mut recorder = RequestQualityRecorder::new();
        assert!(!recorder.has_observed_stream_output());

        observe(
            &mut recorder,
            r#"{"choices":[{"delta":{"content":"hello"}}]}"#,
            delta(1),
        );
        assert!(recorder.has_observed_stream_output());
    }

    #[test]
    fn recorder_accumulates_text_and_token_deltas_across_multiple_chunks() {
        let (metrics, result) = evaluate!(
            config!(
                collect_quality_metrics: true,
                collect_quality_metrics_min_tokens: 1,
                output_tokens_threshold_min: Some(3),
            );
            r#"{"choices":[{"delta":{"content":"alpha beta"}}]}"# => delta(2),
            r#"{"choices":[{"delta":{"content":" gamma delta"}}]}"# => delta(2)
        );

        assert_eq!(metrics.compression_ratio, 1.0);
        assert_eq!(result.threshold_match_reason, Some("output_tokens"));
    }

    #[test]
    fn recorder_falls_back_to_whitespace_count_when_usage_deltas_are_absent() {
        let (_metrics, result) = evaluate!(
            config!(output_tokens_threshold_min: Some(2));
            r#"{"choices":[{"delta":{"content":"alpha beta"}}]}"# => None,
            r#"{"choices":[{"delta":{"content":" gamma"}}]}"# => None
        );

        assert_eq!(result.threshold_match_reason, Some("output_tokens"));
    }

    #[test]
    fn recorder_ignores_malformed_or_irrelevant_chunks_without_matching() {
        let (metrics, result) = evaluate!(
            config!(
                collect_quality_metrics: true,
                collect_quality_metrics_min_tokens: 1,
                output_compression_threshold_max: Some(0.5),
                median_logprob_threshold_max: Some(-1.0),
            );
            "not-json" => None,
            r#"{"object":"other"}"# => None
        );

        assert_eq!(metrics.compression_ratio, 0.0);
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn recorder_matches_when_any_choice_is_repetitive() {
        let (_metrics, result) = evaluate!(
            config!(
                collect_quality_metrics: true,
                collect_quality_metrics_min_tokens: 1,
                output_repetition_1gram_threshold_min: Some(0.3),
            );
            r#"{"choices":[{"index":0,"delta":{"content":"alpha beta gamma delta"}},{"index":1,"delta":{"content":"loop loop loop loop"}}]}"# => None
        );

        assert_eq!(result.threshold_match_reason, Some("repetition_1gram"));
    }

    #[test]
    fn recorder_does_not_concatenate_choices_for_text_heuristics() {
        let (_metrics, result) = evaluate!(
            config!(
                collect_quality_metrics: true,
                collect_quality_metrics_min_tokens: 1,
                output_repetition_1gram_threshold_min: Some(0.3),
            );
            r#"{"choices":[{"index":0,"delta":{"content":"alpha beta"}},{"index":1,"delta":{"content":"alpha beta"}}]}"# => None
        );

        assert!(result.evaluated);
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn recorder_uses_observed_output_tokens_to_gate_per_choice_metrics() {
        let (_metrics, result) = evaluate!(
            config!(
                collect_quality_metrics: true,
                collect_quality_metrics_min_tokens: 4,
            );
            r#"{"choices":[{"index":0,"delta":{"content":"{\"k\":[1,2,3,4]}"}}]}"# => delta(8)
        );

        assert!(
            result.evaluated,
            "real completion token deltas should open the per-choice metric gate"
        );
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn recorder_cumulative_output_tokens_correct_prior_estimate() {
        let (_metrics, result) = evaluate!(
            config!(output_tokens_threshold_min: Some(4));
            r#"{"choices":[{"index":0,"delta":{"content":"alpha beta gamma delta epsilon"}}]}"# => delta(5),
            r#"{"choices":[{"index":0,"delta":{},"usage":{"completion_tokens":3}}]}"# => Some(RequestOutputTokenProgress::Cumulative { tokens: 3, delta: 0 })
        );

        assert!(result.evaluated);
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn recorder_cumulative_progress_uses_delta_for_multi_choice_attribution() {
        let (_metrics, result) = evaluate!(
            config!(
                collect_quality_metrics: true,
                collect_quality_metrics_min_tokens: 3,
                output_repetition_1gram_threshold_min: Some(0.3),
            );
            r#"{"choices":[{"index":0,"delta":{"content":"alpha beta gamma"}}]}"# => Some(RequestOutputTokenProgress::Cumulative { tokens: 3, delta: 3 }),
            r#"{"choices":[{"index":1,"delta":{"content":"loop loop loop loop"}}]}"# => Some(RequestOutputTokenProgress::Cumulative { tokens: 4, delta: 1 })
        );

        assert!(result.evaluated);
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn recorder_accepts_terminal_usage_chunk_without_choices() {
        let (_metrics, result) = evaluate!(
            config!(output_tokens_threshold_min: Some(2));
            r#"{"choices":[{"index":0,"delta":{"content":"alpha beta gamma"}}]}"# => delta(3),
            r#"{"object":"chat.completion.chunk","choices":[],"usage":{"completion_tokens":3}}"# => Some(RequestOutputTokenProgress::Cumulative { tokens: 3, delta: 0 })
        );

        assert!(result.evaluated);
        assert_eq!(result.threshold_match_reason, Some("output_tokens"));
    }

    #[test]
    fn recorder_usage_less_no_space_output_still_reaches_quality_thresholds() {
        let (_metrics, result) = evaluate!(
            config!(
                collect_quality_metrics: true,
                collect_quality_metrics_min_tokens: 4,
                output_degeneracy_threshold_min: Some(0.8),
            );
            r#"{"choices":[{"index":0,"delta":{"content":"哈哈哈哈哈哈"}}]}"# => None
        );

        assert!(result.evaluated);
        assert_eq!(result.threshold_match_reason, Some("degeneracy_score"));
    }

    #[test]
    fn recorder_usage_less_structured_output_counts_token_like_units() {
        let (_metrics, result) = evaluate!(
            config!(output_tokens_threshold_min: Some(6));
            r#"{"choices":[{"index":0,"delta":{"content":"{\"k\":[1,2,3,4,5,6]}"}}]}"# => None
        );

        assert!(result.evaluated);
        assert_eq!(result.threshold_match_reason, Some("output_tokens"));
    }

    #[test]
    fn recorder_does_not_mark_role_only_stream_as_evaluated_for_token_threshold() {
        let (_metrics, result) = evaluate!(
            config!(output_tokens_threshold_min: Some(10));
            r#"{"choices":[{"index":0,"delta":{"role":"assistant"}}],"usage":{"completion_tokens":3}}"# => delta(3)
        );

        assert!(!result.evaluated);
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn recorder_marks_below_threshold_text_stream_as_clean_for_token_threshold() {
        let (_metrics, result) = evaluate!(
            config!(output_tokens_threshold_min: Some(10));
            r#"{"choices":[{"index":0,"delta":{"content":"alpha beta gamma"}}],"usage":{"completion_tokens":3}}"# => delta(3)
        );

        assert!(result.evaluated);
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn recorder_skips_ambiguous_multi_choice_usage_for_text_metrics() {
        let (_metrics, result) = evaluate!(
            config!(
                collect_quality_metrics: true,
                collect_quality_metrics_min_tokens: 4,
                output_compression_threshold_max: Some(0.8),
            );
            r#"{"choices":[{"index":0,"delta":{"content":"alpha beta gamma delta epsilon zeta"}},{"index":1,"delta":{"content":"loop loop"}}],"usage":{"completion_tokens":8}}"# => delta(8)
        );

        assert!(!result.evaluated);
        assert_eq!(result.threshold_match_reason, None);
    }

    #[test]
    fn recorder_aggregates_logprobs_from_all_choices() {
        let (_metrics, result) = evaluate!(
            config!(median_logprob_threshold_max: Some(-7.0));
            r#"{"choices":[{"index":0,"delta":{"content":"alpha"}},{"index":1,"delta":{"content":"beta"},"logprobs":{"content":[{"token":"beta","logprob":-7.5}]}}]}"# => delta(2)
        );

        assert_eq!(result.threshold_match_reason, Some("median_logprob"));
    }

    #[test]
    fn median_logprob_uses_upper_middle_value_for_even_sample_count() {
        assert_eq!(median_logprob(&[-8.0, -6.0, -7.0, -5.0]), Some(-6.0));
    }
}
